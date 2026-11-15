package registry

import (
	"bytes"
	"context"
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
	store.schemaMigrations = append(append([]companySchemaStep{}, companySchemaCatalog...), companySchemaStep{
		version: 2, definition: "test-only-safe-project-backfill-v2",
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
	if lines[0]["company_id"] != "company-a" || !reflect.DeepEqual(lines[0]["applied_versions"], []any{float64(2)}) ||
		lines[1]["company_id"] != "company-b" || lines[1]["failed_version"] != float64(2) || !reflect.DeepEqual(lines[1]["pending_versions"], []any{float64(2)}) {
		t.Fatalf("per-Company version report = %s", report.String())
	}
	companyA, err := store.rawCompany(ctx, "company-a")
	if err != nil || len(companyA.SchemaMigrations) != 2 {
		t.Fatalf("verified Company must retain both versions: %+v, %v", companyA, err)
	}
	stale := companyA
	stale.SchemaMigrations = stale.SchemaMigrations[:1]
	store.markCompanySchemaFailed(ctx, stale)
	companyA, err = store.rawCompany(ctx, "company-a")
	if err != nil || companyA.SchemaState != "ready" {
		t.Fatalf("stale failure overwrote a winner's ready state: %+v, %v", companyA, err)
	}
	companyB, err := store.rawCompany(ctx, "company-b")
	if err != nil || len(companyB.SchemaMigrations) != 1 {
		t.Fatalf("partial Company must not record v2: %+v, %v", companyB, err)
	}
	report.Reset()
	if err := store.MigrateAllCompanies(ctx, &report); err != nil || !strings.Contains(report.String(), `"complete":true`) {
		t.Fatalf("retry = %v, report %s", err, report.String())
	}
	companyA, _ = store.rawCompany(ctx, "company-a")
	companyB, _ = store.rawCompany(ctx, "company-b")
	if len(companyA.SchemaMigrations) != 2 || len(companyB.SchemaMigrations) != 2 {
		t.Fatalf("retry duplicated or skipped history: A=%d B=%d", len(companyA.SchemaMigrations), len(companyB.SchemaMigrations))
	}
	if _, err := store.Register(ctx, Registration{CompanyID: "company-c", CompanyName: "company-c", ProjectID: "project-a", ProjectName: "project-a"}); err != nil {
		t.Fatalf("fresh Company must run full catalog: %v", err)
	}
	companyC, _ := store.rawCompany(ctx, "company-c")
	if len(companyC.SchemaMigrations) != 2 {
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
	if err != nil || len(company.SchemaMigrations) != 1 || company.SchemaState != "ready" {
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
