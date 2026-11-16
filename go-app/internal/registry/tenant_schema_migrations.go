package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

const companyRegistrationTimeout = 95 * time.Second

// A migration is compiled into the Registry. Its definition is frozen once a
// Company records its digest; a later change must append another version.
type companySchemaStep struct {
	version    int64
	definition string
	apply      func(context.Context, *MongoStore, Company) error
	verify     func(context.Context, *MongoStore, Company) error
}

var companySchemaCatalog = []companySchemaStep{{
	version:    1,
	definition: "company-schema-v1:projects-preferences-roadmap-overview-canonical-prototype-todos-history-knowledge-proof-repository-identity",
	apply: func(ctx context.Context, m *MongoStore, company Company) error {
		if err := m.preflightExistingCompanySchema(ctx, company); err != nil {
			return err
		}
		if err := m.ensureTenantIndexes(ctx, company.DatabaseName); err != nil {
			return err
		}
		if err := m.ProvisionTODOCollection(ctx, company.ID); err != nil {
			return err
		}
		if err := m.ProvisionTODOHistory(ctx, company.ID); err != nil {
			return err
		}
		if err := ensureImplementationRepositoryIdentityIndex(ctx, m.client.Database(company.DatabaseName)); err != nil {
			return err
		}
		return m.ensureKnowledgeProofIndex(ctx, company.DatabaseName)
	},
	verify: func(ctx context.Context, m *MongoStore, company Company) error {
		return m.verifyCompanySchemaV1(ctx, company)
	},
}, {
	version:    2,
	definition: "company-schema-v2:todo-content-image-attachments",
	apply: func(ctx context.Context, m *MongoStore, company Company) error {
		db := m.client.Database(company.DatabaseName)
		return db.RunCommand(ctx, bson.D{
			{Key: "collMod", Value: todoContentVersionsCollection},
			{Key: "validator", Value: TODOContentVersionsValidator()},
			{Key: "validationLevel", Value: "strict"},
			{Key: "validationAction", Value: "error"},
		}).Err()
	},
	verify: func(ctx context.Context, m *MongoStore, company Company) error {
		return verifyTODOHistoryCollections(ctx, m.client.Database(company.DatabaseName))
	},
}}

func (m *MongoStore) schemaCatalog() []companySchemaStep {
	if m.schemaMigrations != nil {
		return m.schemaMigrations
	}
	return companySchemaCatalog
}

func (m *MongoStore) preflightExistingCompanySchema(ctx context.Context, company Company) error {
	db := m.client.Database(company.DatabaseName)
	for _, name := range []string{canonicalMandates, canonicalDomains, canonicalConstitutions, canonicalRoadmaps, canonicalModules, canonicalEndpoints, canonicalPolicies} {
		if err := verifyExistingCompanyValidator(ctx, db, name, canonicalDocumentValidator()); err != nil {
			return err
		}
	}
	for _, spec := range prototypeCollectionSpecs() {
		if err := verifyExistingCompanyValidator(ctx, db, spec.name, prototypeCollectionValidator(spec)); err != nil {
			return err
		}
	}
	// TODO history has one explicitly recognized strict-validator predecessor.
	collections, err := db.ListCollectionSpecifications(ctx, bson.D{{Key: "name", Value: todosCollection}})
	if err != nil {
		return ErrUnavailable
	}
	if len(collections) > 1 {
		return todoTeach("todos.validator", "match the current or recognized predecessor schema", ErrConflict)
	}
	if len(collections) == 1 {
		if collections[0].Type != "collection" {
			return todoTeach("todos.validator", "match an ordinary collection", ErrConflict)
		}
		validator := collections[0].Options.Lookup("validator")
		if validator.Type != bson.TypeEmbeddedDocument || !recognizedTODOCollectionValidator(bson.Raw(validator.Document())) && verifyTODOCollection(ctx, db) != nil {
			return todoTeach("todos.validator", "match the current or recognized predecessor schema", ErrConflict)
		}
	}
	return nil
}

func verifyExistingCompanyValidator(ctx context.Context, db *mongo.Database, name string, expected bson.M) error {
	collections, err := db.ListCollectionSpecifications(ctx, bson.D{{Key: "name", Value: name}})
	if err != nil {
		return ErrUnavailable
	}
	if len(collections) == 0 {
		return nil
	}
	if len(collections) != 1 || collections[0].Type != "collection" {
		return todoTeach(name+".validator", "match exactly one ordinary collection", ErrConflict)
	}
	options := collections[0].Options
	if level, ok := options.Lookup("validationLevel").StringValueOK(); !ok || level != "strict" {
		return todoTeach(name+".validationLevel", "retain strict validation", ErrConflict)
	}
	if action, ok := options.Lookup("validationAction").StringValueOK(); !ok || action != "error" {
		return todoTeach(name+".validationAction", "retain error validation", ErrConflict)
	}
	actual := options.Lookup("validator")
	if actual.Type != bson.TypeEmbeddedDocument {
		return todoTeach(name+".validator", "match the frozen migration schema", ErrConflict)
	}
	encoded, err := bson.Marshal(expected)
	if err != nil {
		return ErrUnavailable
	}
	var actualMap, expectedMap bson.M
	if bson.Unmarshal(actual.Document(), &actualMap) != nil || bson.Unmarshal(encoded, &expectedMap) != nil || !reflect.DeepEqual(normalizeCompanyBSON(actualMap), normalizeCompanyBSON(expectedMap)) {
		return todoTeach(name+".validator", "match the frozen migration schema; unknown drift is not changed automatically", ErrConflict)
	}
	return nil
}

func normalizeCompanyBSON(value any) any {
	switch item := value.(type) {
	case bson.M:
		result := make(map[string]any, len(item))
		for key, child := range item {
			result[key] = normalizeCompanyBSON(child)
		}
		return result
	case bson.D:
		result := make(map[string]any, len(item))
		for _, entry := range item {
			result[entry.Key] = normalizeCompanyBSON(entry.Value)
		}
		return result
	case bson.A:
		result := make([]any, len(item))
		for i, child := range item {
			result[i] = normalizeCompanyBSON(child)
		}
		return result
	default:
		return value
	}
}

func verifyCompanyIndex(ctx context.Context, collection *mongo.Collection, name string, keys bson.D, unique bool, partial ...bson.M) error {
	cursor, err := collection.Indexes().List(ctx)
	if err != nil {
		return ErrUnavailable
	}
	defer cursor.Close(ctx)
	want, err := bson.Marshal(keys)
	if err != nil {
		return ErrUnavailable
	}
	for cursor.Next(ctx) {
		raw := cursor.Current
		indexName, ok := raw.Lookup("name").StringValueOK()
		if !ok || indexName != name {
			continue
		}
		key := raw.Lookup("key")
		if key.Type != bson.TypeEmbeddedDocument || !reflect.DeepEqual([]byte(key.Document()), want) {
			return todoTeach(collection.Name()+"."+name, "match the frozen index keys", ErrConflict)
		}
		actualUnique := raw.Lookup("unique")
		if actualUnique.Type == bson.TypeBoolean && actualUnique.Boolean() != unique || unique && actualUnique.Type != bson.TypeBoolean {
			return todoTeach(collection.Name()+"."+name, "match the frozen index uniqueness", ErrConflict)
		}
		actualPartial := raw.Lookup("partialFilterExpression")
		if len(partial) == 0 {
			if actualPartial.Type != 0 {
				return todoTeach(collection.Name()+"."+name, "have no unexpected partial filter", ErrConflict)
			}
		} else {
			if actualPartial.Type != bson.TypeEmbeddedDocument {
				return todoTeach(collection.Name()+"."+name, "match the frozen partial filter", ErrConflict)
			}
			expectedPartial, err := bson.Marshal(partial[0])
			if err != nil {
				return ErrUnavailable
			}
			var actualMap, expectedMap bson.M
			if bson.Unmarshal(actualPartial.Document(), &actualMap) != nil || bson.Unmarshal(expectedPartial, &expectedMap) != nil || !reflect.DeepEqual(normalizeCompanyBSON(actualMap), normalizeCompanyBSON(expectedMap)) {
				return todoTeach(collection.Name()+"."+name, "match the frozen partial filter", ErrConflict)
			}
		}
		for _, option := range []string{"sparse", "expireAfterSeconds", "collation", "hidden"} {
			if raw.Lookup(option).Type != 0 {
				return todoTeach(collection.Name()+"."+name, "contain no unexpected index options", ErrConflict)
			}
		}
		return nil
	}
	if cursor.Err() != nil {
		return ErrUnavailable
	}
	return todoTeach(collection.Name()+"."+name, "be provisioned by the Company migration", ErrConflict)
}

func (m *MongoStore) verifyCompanySchemaV1(ctx context.Context, company Company) error {
	db := m.client.Database(company.DatabaseName)
	if err := preflightOverviewIndexes(ctx, db); err != nil {
		return err
	}
	if err := verifyTODOCollection(ctx, db); err != nil {
		return err
	}
	if err := verifyTODOIndexes(ctx, db.Collection(todosCollection)); err != nil {
		return err
	}
	if err := verifyTODOHistoryCollections(ctx, db); err != nil {
		return err
	}
	for _, name := range []string{canonicalMandates, canonicalDomains, canonicalConstitutions, canonicalRoadmaps, canonicalModules, canonicalEndpoints, canonicalPolicies} {
		if err := verifyExistingCompanyValidator(ctx, db, name, canonicalDocumentValidator()); err != nil {
			return err
		}
		if err := verifyCompanyIndex(ctx, db.Collection(name), name+"_identity_unique", bson.D{{Key: "project_id", Value: 1}, {Key: "generation_id", Value: 1}, {Key: "id", Value: 1}}, true); err != nil {
			return err
		}
	}
	for _, index := range []struct {
		collection, name string
		keys             bson.D
		unique           bool
	}{
		{canonicalVersions, "canonical_versions_lookup", bson.D{{Key: "project_id", Value: 1}, {Key: "generation_id", Value: 1}, {Key: "family", Value: 1}, {Key: "record_id", Value: 1}, {Key: "revision", Value: 1}}, true},
		{canonicalEvents, "canonical_events_timeline", bson.D{{Key: "project_id", Value: 1}, {Key: "generation_id", Value: 1}, {Key: "occurred_at", Value: 1}, {Key: "_id", Value: 1}}, false},
		{canonicalEvents, "canonical_events_project_timeline", bson.D{{Key: "project_id", Value: 1}, {Key: "occurred_at", Value: 1}, {Key: "_id", Value: 1}}, false},
		{canonicalEvents, "canonical_events_operation_unique", bson.D{{Key: "project_id", Value: 1}, {Key: "operation_id", Value: 1}, {Key: "family", Value: 1}, {Key: "record_id", Value: 1}, {Key: "kind", Value: 1}}, true},
		{canonicalEndpoints, "endpoint_method_route_host_unique", bson.D{{Key: "project_id", Value: 1}, {Key: "generation_id", Value: 1}, {Key: "payload.method", Value: 1}, {Key: "payload.route", Value: 1}, {Key: "payload.host_scope", Value: 1}}, true},
		{canonicalEndpoints, "endpoint_module_navigation", bson.D{{Key: "project_id", Value: 1}, {Key: "generation_id", Value: 1}, {Key: "payload.module_id", Value: 1}, {Key: "id", Value: 1}}, false},
	} {
		if err := verifyCompanyIndex(ctx, db.Collection(index.collection), index.name, index.keys, index.unique); err != nil {
			return err
		}
	}
	for _, spec := range prototypeCollectionSpecs() {
		if err := verifyExistingCompanyValidator(ctx, db, spec.name, prototypeCollectionValidator(spec)); err != nil {
			return err
		}
	}
	for _, index := range []struct {
		collection, name string
		keys             bson.D
		unique           bool
		partial          bson.M
	}{
		{projectsCollection, "projects_id_unique", bson.D{{Key: "id", Value: 1}}, true, bson.M{"id": bson.M{"$type": "string"}}},
		{preferencesCollection, "registration_preferences_project_unique", bson.D{{Key: "project_id", Value: 1}}, true, bson.M{"project_id": bson.M{"$type": "string"}}},
		{epicsCollection, "roadmap_epic_identity_unique", bson.D{{Key: "project_id", Value: 1}, {Key: "id", Value: 1}}, true, bson.M{"id": bson.M{"$type": "string"}}},
		{releasesCollection, "roadmap_release_identity_unique", bson.D{{Key: "project_id", Value: 1}, {Key: "id", Value: 1}}, true, bson.M{"id": bson.M{"$type": "string"}}},
		{implementationRepositoryIdentityCollection, "project_repository_identity_unique", bson.D{{Key: "project_id", Value: 1}, {Key: "repository_id", Value: 1}}, true, nil},
		{canonicalEvents, "canonical_events_record_revision", bson.D{{Key: "project_id", Value: 1}, {Key: "generation_id", Value: 1}, {Key: "family", Value: 1}, {Key: "record_id", Value: 1}, {Key: "revision", Value: 1}}, false, nil},
		{prototypeGenerationsCollection, "prototype_generation_identity_unique", bson.D{{Key: "company_id", Value: 1}, {Key: "project_id", Value: 1}, {Key: "generation_key", Value: 1}}, true, nil},
		{prototypeItemsCollection, "prototype_item_identity_unique", bson.D{{Key: "company_id", Value: 1}, {Key: "project_id", Value: 1}, {Key: "generation_key", Value: 1}, {Key: "prototype_id", Value: 1}}, true, nil},
		{prototypeActiveCollection, "prototype_active_project_unique", bson.D{{Key: "company_id", Value: 1}, {Key: "project_id", Value: 1}}, true, nil},
	} {
		var err error
		if index.partial != nil {
			err = verifyCompanyIndex(ctx, db.Collection(index.collection), index.name, index.keys, index.unique, index.partial)
		} else {
			err = verifyCompanyIndex(ctx, db.Collection(index.collection), index.name, index.keys, index.unique)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// The v1 digest is already recorded by deployed Companies. Its schema must
// remain frozen; image attachments are introduced by v2 above.
const companySchemaV1Digest = "sha256:c952456ba2b452b41d7cfdf030d51425692bc3b28141c3d431cae385018bf12e"

func (step companySchemaStep) digest() string {
	if step.version == 1 && step.definition == companySchemaCatalog[0].definition {
		return companySchemaV1Digest
	}
	if step.version == 2 && step.definition == companySchemaCatalog[1].definition {
		definition, err := json.Marshal([]any{step.definition, TODOContentVersionsValidator()})
		if err != nil {
			panic("invalid compiled Company schema migration definition: " + err.Error())
		}
		sum := sha256.Sum256(definition)
		return "sha256:" + hex.EncodeToString(sum[:])
	}
	sum := sha256.Sum256([]byte(step.definition))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func companySchemaV1Definition() any {
	validators := []any{
		companySchemaCatalog[0].definition,
		canonicalDocumentValidator(),
		TODOCollectionValidator(),
		TODOContentVersionsValidator(),
		TODOEventsValidator(),
	}
	for _, spec := range prototypeCollectionSpecs() {
		validators = append(validators, []any{spec.name, prototypeCollectionValidator(spec)})
	}
	var overview []any
	for _, spec := range overviewIndexSpecs() {
		overview = append(overview, []any{spec.collection, spec.name, spec.keys})
	}
	indexKeys := func(models []mongo.IndexModel) []any {
		keys := make([]any, 0, len(models))
		for _, model := range models {
			keys = append(keys, model.Keys)
		}
		return keys
	}
	historyIndexes := func(specs []historyIndexSpec) []any {
		items := make([]any, 0, len(specs))
		for _, spec := range specs {
			items = append(items, []any{spec.name, spec.keys, spec.unique})
		}
		return items
	}
	return []any{
		validators,
		indexKeys(todoIndexModels()),
		historyIndexes(todoContentVersionIndexes()),
		historyIndexes(todoEventIndexes()),
		overview,
	}
}

func (m *MongoStore) companySchemaReady(company Company) error {
	steps := m.schemaCatalog()
	if company.SchemaState != "ready" || len(company.SchemaMigrations) != len(steps) {
		return todoTeach("company.schema_migrations", "run the Registry migrate-all command; this Company has pending schema migrations", ErrConflict)
	}
	for i, step := range steps {
		if step.version != int64(i+1) || company.SchemaMigrations[i].Version != step.version || company.SchemaMigrations[i].Digest != step.digest() {
			return todoTeach("company.schema_migrations", "run the Registry migrate-all command; this Company has a missing or changed migration", ErrConflict)
		}
	}
	return nil
}

func (m *MongoStore) validateCompanyMigrationPrefix(company Company) error {
	steps := m.schemaCatalog()
	if len(company.SchemaMigrations) > len(steps) {
		return todoTeach("company.schema_migrations", "retain the recorded migration catalog; a deployed version is missing", ErrConflict)
	}
	for i, entry := range company.SchemaMigrations {
		if steps[i].version != int64(i+1) || entry.Version != steps[i].version || entry.Digest != steps[i].digest() || entry.AppliedAt.IsZero() {
			return todoTeach("company.schema_migrations", "match the immutable ordered migration catalog without gaps or changed digests", ErrConflict)
		}
	}
	return nil
}

// convergeCompanySchema is reached only from registration of a newly inserted
// Company or from the Registry's operator command. Project routes never call it.
func (m *MongoStore) convergeCompanySchema(ctx context.Context, id string, operator bool) error {
	for {
		company, err := m.rawCompany(ctx, id)
		if err != nil {
			return err
		}
		if err := m.validateCompanyMigrationPrefix(company); err != nil {
			return err
		}
		if !operator && company.SchemaState == "" {
			return todoTeach("company.schema_migrations", "run the Registry migrate-all command to adopt this existing Company", ErrConflict)
		}
		if !operator && company.SchemaState == "ready" {
			return m.companySchemaReady(company)
		}
		steps := m.schemaCatalog()
		if len(company.SchemaMigrations) == len(steps) {
			if operator && len(steps) > 0 {
				if err := steps[len(steps)-1].verify(ctx, m, company); err != nil {
					return err
				}
			}
			if company.SchemaState != "ready" {
				_, err := m.control.Collection(companiesCollection).UpdateOne(ctx, bson.D{{Key: "id", Value: id}}, bson.D{{Key: "$set", Value: bson.D{{Key: "schema_state", Value: "ready"}}}})
				return err
			}
			return nil
		}
		step := steps[len(company.SchemaMigrations)]
		if operator && company.SchemaState == "" {
			// A legacy Company with an exact current schema is adopted without DDL.
			if err := step.verify(ctx, m, company); err == nil {
				// Exact readback authorizes the same CAS as an applied step.
			} else if err := step.apply(ctx, m, company); err != nil {
				return err
			}
		} else if err := step.apply(ctx, m, company); err != nil {
			m.markCompanySchemaFailed(ctx, company)
			return err
		}
		if err := step.verify(ctx, m, company); err != nil {
			m.markCompanySchemaFailed(ctx, company)
			return err
		}
		filter := companySchemaPrefixFilter(company)
		state := "pending"
		if len(company.SchemaMigrations)+1 == len(steps) {
			state = "ready"
		}
		entry := CompanySchemaMigration{Version: step.version, Digest: step.digest(), AppliedAt: time.Now().UTC()}
		update := bson.D{{Key: "$push", Value: bson.D{{Key: "schema_migrations", Value: entry}}}, {Key: "$set", Value: bson.D{{Key: "schema_state", Value: state}}}}
		result, err := m.control.Collection(companiesCollection).UpdateOne(ctx, filter, update)
		if err != nil {
			return ErrUnavailable
		}
		if result.MatchedCount == 0 {
			// Another worker won the exact-prefix CAS. Re-read its verified state.
			continue
		}
	}
}

func companySchemaPrefixFilter(company Company) bson.D {
	filter := bson.D{{Key: "id", Value: company.ID}}
	if len(company.SchemaMigrations) == 0 {
		return append(filter, bson.E{Key: "schema_migrations", Value: bson.D{{Key: "$exists", Value: false}}})
	}
	return append(filter, bson.E{Key: "schema_migrations", Value: company.SchemaMigrations})
}

func (m *MongoStore) markCompanySchemaFailed(ctx context.Context, company Company) {
	// A concurrent migrator may already have accepted the next version. A
	// stale attempt must never replace its ready state with failed.
	_, _ = m.control.Collection(companiesCollection).UpdateOne(ctx, companySchemaPrefixFilter(company), bson.D{{Key: "$set", Value: bson.D{{Key: "schema_state", Value: "failed"}}}})
}

type companyMigrationResult struct {
	Type            string  `json:"type"`
	CompanyID       string  `json:"company_id"`
	Applied         int     `json:"applied"`
	AppliedVersions []int64 `json:"applied_versions"`
	Skipped         int     `json:"skipped"`
	SkippedVersions []int64 `json:"skipped_versions"`
	Failed          bool    `json:"failed"`
	FailedVersion   int64   `json:"failed_version,omitempty"`
	Pending         int     `json:"pending"`
	PendingVersions []int64 `json:"pending_versions"`
	Error           string  `json:"error,omitempty"`
}

type companyMigrationSummary struct {
	Type              string `json:"type"`
	Companies         int    `json:"companies"`
	Applied           int    `json:"applied"`
	Skipped           int    `json:"skipped"`
	Failed            int    `json:"failed"`
	Pending           int    `json:"pending"`
	InventoryComplete bool   `json:"inventory_complete"`
	Complete          bool   `json:"complete"`
}

// MigrateAllCompanies writes bounded JSONL results as it advances a Mongo
// cursor. A final summary is emitted only after the complete inventory scan.
func (m *MongoStore) MigrateAllCompanies(ctx context.Context, output io.Writer) error {
	cursor, err := m.control.Collection(companiesCollection).Find(ctx, bson.D{}, options.Find().SetSort(bson.D{{Key: "id", Value: 1}}).SetBatchSize(128))
	if err != nil {
		_ = json.NewEncoder(output).Encode(companyMigrationSummary{Type: "summary"})
		return todoTeach("companies", "complete the Company inventory before claiming migration rollout", ErrUnavailable)
	}
	defer cursor.Close(ctx)
	enc := json.NewEncoder(output)
	summary := companyMigrationSummary{Type: "summary"}
	incomplete := func() error {
		_ = enc.Encode(summary)
		return todoTeach("companies", "complete the Company inventory; the emitted report is incomplete", ErrUnavailable)
	}
	for cursor.Next(ctx) {
		var company Company
		if err := cursor.Decode(&company); err != nil || !validID(company.ID) || company.DatabaseName != m.tenantDatabase(company.ID) {
			return incomplete()
		}
		before := len(company.SchemaMigrations)
		result := companyMigrationResult{Type: "company", CompanyID: company.ID, AppliedVersions: []int64{}, SkippedVersions: []int64{}, PendingVersions: []int64{}}
		if err := m.convergeCompanySchema(ctx, company.ID, true); err != nil {
			result.Failed = true
			result.Error = err.Error()
			summary.Failed++
		}
		after, err := m.rawCompany(ctx, company.ID)
		if err != nil {
			return incomplete()
		}
		if before > len(after.SchemaMigrations) {
			before = len(after.SchemaMigrations)
		}
		result.Applied = len(after.SchemaMigrations) - before
		result.Skipped = before
		catalog := m.schemaCatalog()
		if len(after.SchemaMigrations) < len(catalog) {
			result.Pending = len(catalog) - len(after.SchemaMigrations)
			for _, step := range catalog[len(after.SchemaMigrations):] {
				result.PendingVersions = append(result.PendingVersions, step.version)
			}
		}
		for _, entry := range after.SchemaMigrations[:before] {
			result.SkippedVersions = append(result.SkippedVersions, entry.Version)
		}
		for _, entry := range after.SchemaMigrations[before:] {
			result.AppliedVersions = append(result.AppliedVersions, entry.Version)
		}
		if result.Failed && len(result.PendingVersions) > 0 {
			result.FailedVersion = result.PendingVersions[0]
		}
		summary.Companies++
		summary.Applied += result.Applied
		summary.Skipped += result.Skipped
		summary.Pending += result.Pending
		if err := enc.Encode(result); err != nil {
			return err
		}
	}
	if err := cursor.Err(); err != nil {
		return incomplete()
	}
	summary.InventoryComplete = true
	summary.Complete = summary.Failed == 0 && summary.Pending == 0
	if err := enc.Encode(summary); err != nil {
		return err
	}
	if !summary.Complete {
		return errors.New("TEACH: migrate-all left failed or pending Companies; inspect the JSONL report and rerun after repair")
	}
	return nil
}
