package registry

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

const (
	frozenCompanySchemaV2Definition = "company-schema-v2:todo-content-image-attachments"
	frozenCompanySchemaV2Digest     = "sha256:0fae006ba32a9fb4917cac8cf2e7be3a649a53e69f508c8675e54858e5416a5e"
)

func frozenTODOContentVersionsValidatorV2() bson.D {
	v2 := todoAuthoredContentV2ValidatorBeforePrototype()
	content := bson.D{{Key: "bsonType", Value: "object"}, {Key: "oneOf", Value: bson.A{TODOAuthoredContentV1Validator(), v2}}}
	return replaceBSONValueAtPath(TODOContentVersionsValidator(), []string{"$jsonSchema", "properties", "content"}, content)
}

func rawCompanyMigrationEntries(ctx context.Context, store *MongoStore, companyID string) ([][]byte, error) {
	var company bson.Raw
	if err := store.control.Collection(companiesCollection).FindOne(ctx, bson.D{{Key: "id", Value: companyID}}).Decode(&company); err != nil {
		return nil, err
	}
	entries, ok := company.Lookup("schema_migrations").ArrayOK()
	if !ok {
		return nil, errors.New("schema_migrations is not a BSON array")
	}
	values, err := entries.Values()
	if err != nil {
		return nil, err
	}
	result := make([][]byte, 0, len(values))
	for _, value := range values {
		document, ok := value.DocumentOK()
		if !ok {
			return nil, errors.New("schema_migrations entry is not a BSON document")
		}
		result = append(result, append([]byte(nil), document...))
	}
	return result, nil
}

func TestCompanySchemaV2DigestMatchesFrozenPredecessor(t *testing.T) {
	if !reflect.DeepEqual(TODOContentVersionsValidatorBeforePrototype(), frozenTODOContentVersionsValidatorV2()) {
		t.Fatal("v2 migration validator differs from frozen pre-Prototype validator")
	}
	definition, err := json.Marshal([]any{frozenCompanySchemaV2Definition, frozenTODOContentVersionsValidatorV2()})
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(definition)
	if got := "sha256:" + hex.EncodeToString(sum[:]); got != frozenCompanySchemaV2Digest {
		t.Fatalf("frozen historical v2 validator digest drifted: got=%s want=%s", got, frozenCompanySchemaV2Digest)
	}
	if got := companySchemaCatalog[1].digest(); got != frozenCompanySchemaV2Digest {
		t.Fatalf("Company migration v2 digest changed: got=%s want frozen predecessor=%s", got, frozenCompanySchemaV2Digest)
	}
}

func TestCompanyMigrateAllAppliesPendingVersionsAndResumesPartialBackfill(t *testing.T) {
	uri := os.Getenv("REGISTRY_TEST_MONGODB_URI")
	if uri == "" {
		t.Skip("requires isolated Mongo integration fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	control := fmt.Sprintf("builder_company_migrations_%d", time.Now().UnixNano())
	store, err := OpenMongo(ctx, uri, control)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close(context.Background())
	store.tenantPrefix = control
	defer func() {
		for _, id := range []string{"company-a", "company-b", "company-c"} {
			_ = store.client.Database(store.tenantDatabase(id)).Drop(context.Background())
		}
		_ = store.control.Drop(context.Background())
	}()
	for _, id := range []string{"company-a", "company-b"} {
		for _, projectID := range []string{"project-a", "project-b"} {
			_, err := store.Register(ctx, Registration{CompanyID: id, CompanyName: id, ProjectID: projectID, ProjectName: projectID})
			if err != nil {
				t.Fatalf("register %s/%s: %v", id, projectID, err)
			}
		}
	}
	failedOnce := false
	backfillVersion := companySchemaCatalog[len(companySchemaCatalog)-1].version + 1
	completeHistoryLength := len(companySchemaCatalog) + 1
	store.schemaMigrations = append(append([]companySchemaStep{}, companySchemaCatalog...), companySchemaStep{
		version: backfillVersion, definition: "test-only-safe-project-backfill",
		apply: func(ctx context.Context, m *MongoStore, company Company) error {
			collection := m.client.Database(company.DatabaseName).Collection(projectsCollection)
			if company.ID == "company-b" && !failedOnce {
				failedOnce = true
				_, err := collection.UpdateOne(ctx, bson.D{{Key: "id", Value: "project-a"}}, bson.D{{Key: "$set", Value: bson.D{{Key: "migration_test", Value: true}}}})
				if err != nil {
					return err
				}
				return errors.New("injected interruption after first backfill write")
			}
			_, err := collection.UpdateMany(ctx, bson.D{}, bson.D{{Key: "$set", Value: bson.D{{Key: "migration_test", Value: true}}}})
			return err
		},
		verify: func(ctx context.Context, m *MongoStore, company Company) error {
			count, err := m.client.Database(company.DatabaseName).Collection(projectsCollection).CountDocuments(ctx, bson.D{{Key: "migration_test", Value: bson.D{{Key: "$ne", Value: true}}}})
			if err != nil || count != 0 {
				return errors.New("backfill postcondition incomplete")
			}
			return nil
		},
	})
	if _, err := store.Project(ctx, "company-a", "project-a"); err == nil || !strings.Contains(err.Error(), "migrate-all") {
		t.Fatalf("behind-catalog Project must be gated: %v", err)
	}
	var report bytes.Buffer
	if err := store.MigrateAllCompanies(ctx, &report); err == nil {
		t.Fatal("partial Company backfill must fail the fleet command")
	}
	var lines []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(report.String()), "\n") {
		var item map[string]any
		if err := json.Unmarshal([]byte(line), &item); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, item)
	}
	if len(lines) != 3 || lines[2]["complete"] != false || lines[2]["failed"] != float64(1) {
		t.Fatalf("incomplete report = %s", report.String())
	}
	if lines[0]["company_id"] != "company-a" || !reflect.DeepEqual(lines[0]["applied_versions"], []any{float64(backfillVersion)}) ||
		lines[1]["company_id"] != "company-b" || lines[1]["failed_version"] != float64(backfillVersion) || !reflect.DeepEqual(lines[1]["pending_versions"], []any{float64(backfillVersion)}) {
		t.Fatalf("per-Company version report = %s", report.String())
	}
	companyA, err := store.rawCompany(ctx, "company-a")
	if err != nil || len(companyA.SchemaMigrations) != completeHistoryLength {
		t.Fatalf("verified Company must retain the full catalog and backfill version: %+v, %v", companyA, err)
	}
	stale := companyA
	stale.SchemaMigrations = stale.SchemaMigrations[:len(companySchemaCatalog)]
	store.markCompanySchemaFailed(ctx, stale)
	companyA, err = store.rawCompany(ctx, "company-a")
	if err != nil || companyA.SchemaState != "ready" {
		t.Fatalf("stale failure overwrote a winner's ready state: %+v, %v", companyA, err)
	}
	companyB, err := store.rawCompany(ctx, "company-b")
	if err != nil || len(companyB.SchemaMigrations) != len(companySchemaCatalog) {
		t.Fatalf("partial Company must not record backfill version %d: %+v, %v", backfillVersion, companyB, err)
	}
	report.Reset()
	if err := store.MigrateAllCompanies(ctx, &report); err != nil || !strings.Contains(report.String(), `"complete":true`) {
		t.Fatalf("retry = %v, report %s", err, report.String())
	}
	companyA, _ = store.rawCompany(ctx, "company-a")
	companyB, _ = store.rawCompany(ctx, "company-b")
	if len(companyA.SchemaMigrations) != completeHistoryLength || len(companyB.SchemaMigrations) != completeHistoryLength {
		t.Fatalf("retry duplicated or skipped history: A=%d B=%d", len(companyA.SchemaMigrations), len(companyB.SchemaMigrations))
	}
	if _, err := store.Register(ctx, Registration{CompanyID: "company-c", CompanyName: "company-c", ProjectID: "project-a", ProjectName: "project-a"}); err != nil {
		t.Fatalf("fresh Company must run full catalog: %v", err)
	}
	companyC, _ := store.rawCompany(ctx, "company-c")
	if len(companyC.SchemaMigrations) != completeHistoryLength {
		t.Fatalf("fresh Company versions = %d", len(companyC.SchemaMigrations))
	}
	inventoryCtx, stopInventory := context.WithCancel(ctx)
	stopInventory()
	report.Reset()
	if err := store.MigrateAllCompanies(inventoryCtx, &report); err == nil || !strings.Contains(report.String(), `"inventory_complete":false`) || strings.Contains(report.String(), `"complete":true`) {
		t.Fatalf("failed inventory claimed completion: %v, %s", err, report.String())
	}
}

func migrateCompanyFixture(t *testing.T, ctx context.Context, store *MongoStore) {
	t.Helper()
	var report bytes.Buffer
	if err := store.MigrateAllCompanies(ctx, &report); err != nil {
		t.Fatalf("migrate Company fixture: %v; report=%s", err, report.String())
	}
}

func TestCompanyImageAttachmentMigrationUpgradesExistingV1Validator(t *testing.T) {
	testCompanyTODOValidatorMigration(t, false)
}

func TestCompanyPrototypeMigrationUpgradesExistingV2Validator(t *testing.T) {
	testCompanyTODOValidatorMigration(t, true)
}

func testCompanyTODOValidatorMigration(t *testing.T, fromV2 bool) {
	t.Helper()
	uri := os.Getenv("REGISTRY_TEST_MONGODB_URI")
	if uri == "" {
		t.Skip("requires isolated Mongo integration fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	control := fmt.Sprintf("builder_image_migration_%d", time.Now().UnixNano())
	store, err := OpenMongo(ctx, uri, control)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close(context.Background())
	store.tenantPrefix = control
	defer func() {
		_ = store.client.Database(store.tenantDatabase("company-a")).Drop(context.Background())
		_ = store.control.Drop(context.Background())
	}()
	if _, err := store.Register(ctx, Registration{CompanyID: "company-a", CompanyName: "Company A", ProjectID: "project-a", ProjectName: "Project A"}); err != nil {
		t.Fatal(err)
	}
	company, err := store.rawCompany(ctx, "company-a")
	if err != nil {
		t.Fatal(err)
	}
	v2Content := todoAuthoredContentV2ValidatorBeforePrototype()
	if !fromV2 {
		v2Content = withoutBSONFieldsAtPath(v2Content, []string{"properties"}, "image_attachments")
	}
	oldContent := bson.D{{Key: "bsonType", Value: "object"}, {Key: "oneOf", Value: bson.A{TODOAuthoredContentV1Validator(), v2Content}}}
	oldValidator := frozenTODOContentVersionsValidatorV2()
	if !fromV2 {
		oldValidator = replaceBSONValueAtPath(TODOContentVersionsValidator(), []string{"$jsonSchema", "properties", "content"}, oldContent)
	}
	db := store.client.Database(company.DatabaseName)
	if err := db.RunCommand(ctx, bson.D{{Key: "collMod", Value: todoContentVersionsCollection}, {Key: "validator", Value: oldValidator}, {Key: "validationLevel", Value: "strict"}, {Key: "validationAction", Value: "error"}}).Err(); err != nil {
		t.Fatal(err)
	}
	priorHistory := append([]CompanySchemaMigration(nil), company.SchemaMigrations[:1]...)
	if fromV2 {
		legacyHistory := append([]CompanySchemaMigration(nil), company.SchemaMigrations[:2]...)
		legacyHistory[1].Digest = frozenCompanySchemaV2Digest
		priorHistory = legacyHistory
	}
	_, err = store.control.Collection(companiesCollection).UpdateOne(ctx, bson.D{{Key: "id", Value: company.ID}}, bson.D{{Key: "$set", Value: bson.D{{Key: "schema_migrations", Value: priorHistory}, {Key: "schema_state", Value: "ready"}}}})
	if err != nil {
		t.Fatalf("install frozen deployed v1/v2 migration history: %v", err)
	}
	company, err = store.rawCompany(ctx, company.ID)
	if err != nil {
		t.Fatal(err)
	}
	priorHistory = append([]CompanySchemaMigration(nil), company.SchemaMigrations...)
	var priorHistoryBytes [][]byte
	if fromV2 {
		priorHistoryBytes, err = rawCompanyMigrationEntries(ctx, store, company.ID)
		if err != nil || len(priorHistoryBytes) != 2 {
			t.Fatalf("read exact deployed v1/v2 history bytes: entries=%d err=%v", len(priorHistoryBytes), err)
		}
	}
	if _, err := store.Project(ctx, company.ID, "project-a"); err == nil || !strings.Contains(err.Error(), "migrate-all") {
		t.Fatalf("pre-upgrade Company should be gated before migration: %v", err)
	}
	migrateCompanyFixture(t, ctx, store)
	company, err = store.rawCompany(ctx, company.ID)
	if err != nil || len(company.SchemaMigrations) != len(companySchemaCatalog) || company.SchemaMigrations[0].Digest != companySchemaV1Digest || company.SchemaMigrations[1].Digest != frozenCompanySchemaV2Digest || company.SchemaMigrations[2].Digest != companySchemaCatalog[2].digest() {
		t.Fatalf("migration history: %+v, %v", company, err)
	}
	if fromV2 && !reflect.DeepEqual(company.SchemaMigrations[:2], priorHistory) {
		t.Fatalf("v1/v2 migration history changed while applying v3: before=%+v after=%+v", priorHistory, company.SchemaMigrations[:2])
	}
	if fromV2 {
		afterHistoryBytes, readErr := rawCompanyMigrationEntries(ctx, store, company.ID)
		if readErr != nil || len(afterHistoryBytes) != len(companySchemaCatalog) || !bytes.Equal(priorHistoryBytes[0], afterHistoryBytes[0]) || !bytes.Equal(priorHistoryBytes[1], afterHistoryBytes[1]) {
			t.Fatalf("v3 did not preserve the exact stored v1/v2 migration entry bytes: before=%x after=%x err=%v", priorHistoryBytes, afterHistoryBytes, readErr)
		}
	}
	if err := verifyTODOHistoryCollections(ctx, db); err != nil {
		t.Fatalf("strict v3 TODO history validator was not installed: %v", err)
	}
	if _, err := store.Project(ctx, company.ID, "project-a"); err != nil {
		t.Fatalf("project not readable after migration: %v", err)
	}
	if fromV2 {
		firstMigrationHistory := append([]CompanySchemaMigration(nil), company.SchemaMigrations...)
		migrateCompanyFixture(t, ctx, store)
		company, err = store.rawCompany(ctx, company.ID)
		if err != nil || !reflect.DeepEqual(company.SchemaMigrations, firstMigrationHistory) {
			t.Fatalf("repeat migrate-all changed immutable history: before=%+v after=%+v err=%v", firstMigrationHistory, company.SchemaMigrations, err)
		}
	}
}

func TestCompanyMigrateAllAdoptsExactLegacySchemaAndRejectsUnknownDrift(t *testing.T) {
	uri := os.Getenv("REGISTRY_TEST_MONGODB_URI")
	if uri == "" {
		t.Skip("requires isolated Mongo integration fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	control := fmt.Sprintf("builder_company_adopt_%d", time.Now().UnixNano())
	store, err := OpenMongo(ctx, uri, control)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close(context.Background())
	store.tenantPrefix = control
	defer func() {
		_ = store.client.Database(store.tenantDatabase("company-a")).Drop(context.Background())
		_ = store.control.Drop(context.Background())
	}()
	input := Registration{CompanyID: "company-a", CompanyName: "Company A", ProjectID: "project-a", ProjectName: "Project A"}
	if _, err := store.Register(ctx, input); err != nil {
		t.Fatal(err)
	}
	companies := store.control.Collection(companiesCollection)
	removeHistory := func() {
		t.Helper()
		_, err := companies.UpdateOne(ctx, bson.D{{Key: "id", Value: "company-a"}}, bson.D{{Key: "$unset", Value: bson.D{{Key: "schema_state", Value: ""}, {Key: "schema_migrations", Value: ""}}}})
		if err != nil {
			t.Fatal(err)
		}
	}
	removeHistory()
	if _, err := store.Register(ctx, input); err == nil || !strings.Contains(err.Error(), "migrate-all") {
		t.Fatalf("legacy HTTP registration must direct to operator: %v", err)
	}
	var report bytes.Buffer
	if err := store.MigrateAllCompanies(ctx, &report); err != nil {
		t.Fatalf("exact legacy adoption: %v; report=%s", err, report.String())
	}
	company, err := store.rawCompany(ctx, "company-a")
	if err != nil || len(company.SchemaMigrations) != len(companySchemaCatalog) || company.SchemaState != "ready" {
		t.Fatalf("legacy migration history: %+v, %v", company, err)
	}
	if _, err := store.Project(ctx, "company-a", "project-a"); err != nil {
		t.Fatalf("adoption changed Project data: %v", err)
	}
	removeHistory()
	db := store.client.Database(store.tenantDatabase("company-a"))
	if err := db.RunCommand(ctx, bson.D{{Key: "collMod", Value: canonicalMandates}, {Key: "validator", Value: bson.M{"$jsonSchema": bson.M{"bsonType": "object"}}}, {Key: "validationLevel", Value: "strict"}, {Key: "validationAction", Value: "error"}}).Err(); err != nil {
		t.Fatal(err)
	}
	report.Reset()
	if err := store.MigrateAllCompanies(ctx, &report); err == nil || !strings.Contains(report.String(), `"complete":false`) {
		t.Fatalf("unknown validator drift was accepted: %v; report=%s", err, report.String())
	}
	company, _ = store.rawCompany(ctx, "company-a")
	if len(company.SchemaMigrations) != 0 {
		t.Fatal("unknown drift was stamped as an applied migration")
	}
}

func TestPrototypeRequestDetectsRemovedMigrationCollectionWithoutDDL(t *testing.T) {
	uri := os.Getenv("REGISTRY_TEST_MONGODB_URI")
	if uri == "" {
		t.Skip("requires isolated Mongo integration fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	control := fmt.Sprintf("builder_company_prototype_%d", time.Now().UnixNano())
	store, err := OpenMongo(ctx, uri, control)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close(context.Background())
	store.tenantPrefix = control
	defer func() {
		_ = store.client.Database(store.tenantDatabase("company-a")).Drop(context.Background())
		_ = store.control.Drop(context.Background())
	}()
	if _, err := store.Register(ctx, Registration{CompanyID: "company-a", CompanyName: "Company A", ProjectID: "project-a", ProjectName: "Project A"}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := store.prototypeCollections(ctx, "company-a"); err != nil {
		t.Fatalf("initial Prototype schema: %v", err)
	}
	db := store.client.Database(store.tenantDatabase("company-a"))
	if err := db.Collection(prototypeItemsCollection).Drop(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := store.prototypeCollections(ctx, "company-a"); err == nil || !strings.Contains(err.Error(), "migrate-all") {
		t.Fatalf("dropped collection should return operator TEACH: %v", err)
	}
	names, err := db.ListCollectionNames(ctx, bson.D{{Key: "name", Value: prototypeItemsCollection}})
	if err != nil || len(names) != 0 {
		t.Fatalf("Project request recreated a tenant collection: %v, %v", names, err)
	}
}

func TestCompanyGitHubConnectionMigrationUpgradesStampedV3AndFreshCompanies(t *testing.T) {
	uri := os.Getenv("REGISTRY_TEST_MONGODB_URI")
	if uri == "" {
		if os.Getenv("REGISTRY_REQUIRE_MONGO_INTEGRATION") == "1" {
			t.Fatal("required Mongo integration fixture unavailable")
		}
		t.Skip("requires isolated Mongo integration fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	control := fmt.Sprintf("builder_github_migration_%d", time.Now().UnixNano())
	store, err := OpenMongo(ctx, uri, control)
	if err != nil {
		t.Fatal(err)
	}
	store.tenantPrefix = control
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		_ = store.client.Database(store.tenantDatabase("company-existing")).Drop(cleanup)
		_ = store.client.Database(store.tenantDatabase("company-fresh")).Drop(cleanup)
		_ = store.control.Drop(cleanup)
		_ = store.Close(cleanup)
	})
	store.schemaMigrations = append([]companySchemaStep{}, companySchemaCatalog[:3]...)
	if _, err := store.Register(ctx, Registration{CompanyID: "company-existing", CompanyName: "Existing", ProjectID: "project-a", ProjectName: "Project A"}); err != nil {
		t.Fatal(err)
	}
	before, err := rawCompanyMigrationEntries(ctx, store, "company-existing")
	if err != nil || len(before) != 3 {
		t.Fatalf("v3 history: %d entries, %v", len(before), err)
	}
	existing, err := store.rawCompany(ctx, "company-existing")
	if err != nil {
		t.Fatal(err)
	}
	db := store.client.Database(existing.DatabaseName)
	names, err := db.ListCollectionNames(ctx, bson.M{"name": bson.M{"$in": bson.A{githubRepositoryConnectionsCollection, githubConnectionOperationsCollection}}})
	if err != nil || len(names) != 0 {
		t.Fatalf("v1-v3 unexpectedly provisioned v4 collections: %v, %v", names, err)
	}
	store.schemaMigrations = nil
	if _, err := store.Project(ctx, "company-existing", "project-a"); err == nil || !strings.Contains(err.Error(), "migrate-all") {
		t.Fatalf("v3 Company should require migrate-all: %v", err)
	}
	migrateCompanyFixture(t, ctx, store)
	after, err := rawCompanyMigrationEntries(ctx, store, "company-existing")
	if err != nil || len(after) != 5 || !reflect.DeepEqual(before, after[:3]) {
		t.Fatalf("v4 did not append to unchanged v1-v3 history: %d entries, %v", len(after), err)
	}
	existing, err = store.rawCompany(ctx, "company-existing")
	if err != nil || len(existing.SchemaMigrations) != 5 || existing.SchemaMigrations[3].Version != 4 ||
		existing.SchemaMigrations[3].Digest != companySchemaCatalog[3].digest() {
		t.Fatalf("missing v4 digest: %+v, %v", existing.SchemaMigrations, err)
	}
	if err := verifyGitHubConnectionIndexes(ctx, db); err != nil {
		t.Fatal(err)
	}
	pendingIndex := pendingGitHubInstallationIndex()
	if err := verifyCompanyIndexOptions(ctx, db.Collection(pendingIndex.collection), pendingIndex.name, pendingIndex.keys, false, nil, pendingIndex.partial...); err != nil {
		t.Fatalf("existing Company missing v5 partial index: %v", err)
	}
	migrateCompanyFixture(t, ctx, store)
	afterRetry, err := rawCompanyMigrationEntries(ctx, store, "company-existing")
	if err != nil || !reflect.DeepEqual(after, afterRetry) {
		t.Fatalf("migration retry changed stored history: %v", err)
	}

	if _, err := store.Register(ctx, Registration{CompanyID: "company-fresh", CompanyName: "Fresh", ProjectID: "project-a", ProjectName: "Project A"}); err != nil {
		t.Fatal(err)
	}
	fresh, err := store.rawCompany(ctx, "company-fresh")
	if err != nil || len(fresh.SchemaMigrations) != 5 || fresh.SchemaMigrations[3].Digest != companySchemaCatalog[3].digest() {
		t.Fatalf("fresh Company missing v4: %+v, %v", fresh.SchemaMigrations, err)
	}
	if err := verifyGitHubConnectionIndexes(ctx, store.client.Database(fresh.DatabaseName)); err != nil {
		t.Fatal(err)
	}

	if err := verifyCompanyIndexOptions(ctx, store.client.Database(fresh.DatabaseName).Collection(pendingIndex.collection), pendingIndex.name, pendingIndex.keys, false, nil, pendingIndex.partial...); err != nil {
		t.Fatalf("fresh Company missing v5 partial index: %v", err)
	}

	// A same-name TTL index must not be considered the frozen v4 definition
	// just because the keys, uniqueness and partial filters still match.
	if err := db.RunCommand(ctx, bson.D{{Key: "collMod", Value: githubConnectionOperationsCollection},
		{Key: "index", Value: bson.D{{Key: "name", Value: "github_intent_expiry"}, {Key: "expireAfterSeconds", Value: 60}}}}).Err(); err != nil {
		t.Fatal(err)
	}
	if err := verifyGitHubConnectionIndexes(ctx, db); !errors.Is(err, ErrConflict) {
		t.Fatalf("unexpected TTL accepted: %v", err)
	}
}
