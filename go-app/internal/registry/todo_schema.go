package registry

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"slices"
	"sort"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type TODOCollectionSchemaDiagnostic struct {
	CompanyID         string                      `json:"company_id"`
	DatabaseName      string                      `json:"database_name"`
	Collections       []string                    `json:"collections"`
	TODOCount         int64                       `json:"todo_count"`
	ValidTODOCount    int64                       `json:"valid_todo_count"`
	Projects          []string                    `json:"project_ids"`
	ByteEqual         bool                        `json:"byte_equal"`
	SemanticEqual     bool                        `json:"semantic_equal"`
	ValidationLevel   string                      `json:"validation_level"`
	ValidationAction  string                      `json:"validation_action"`
	ActualValidator   string                      `json:"actual_validator_extjson"`
	ExpectedValidator string                      `json:"expected_validator_extjson"`
	HistoryReadChecks []TODOHistoryReadDiagnostic `json:"history_read_checks,omitempty"`
}

type TODOHistoryReadDiagnostic struct {
	ProjectID string `json:"project_id"`
	TODOID    string `json:"todo_id"`
	Revision  int64  `json:"revision"`
	Readable  bool   `json:"readable"`
	Error     string `json:"error,omitempty"`
}

func (m *MongoStore) ProvisionTODOCollection(ctx context.Context, companyID string) error {
	if !validID(companyID) {
		return todoTeach("company_id", "name an existing Company tenant", ErrInvalid)
	}
	company, err := m.company(ctx, companyID)
	if err != nil {
		return err
	}
	if company.DatabaseName != m.tenantDatabase(companyID) {
		return todoTeach("company.database_name", "match the server-owned Company tenant database", ErrConflict)
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	database := m.client.Database(company.DatabaseName)
	collections, err := database.ListCollectionSpecifications(ctx, bson.D{{Key: "name", Value: todosCollection}})
	if err != nil {
		return ErrUnavailable
	}
	if len(collections) == 0 {
		createOptions := options.CreateCollection().SetValidator(TODOCollectionValidator()).SetValidationLevel("strict").SetValidationAction("error")
		if err := database.CreateCollection(ctx, todosCollection, createOptions); err != nil {
			if !namespaceExists(err) {
				return ErrUnavailable
			}
		}
	}
	if err := verifyTODOCollection(ctx, database); err != nil {
		return err
	}
	collection := database.Collection(todosCollection)
	models := todoIndexModels()
	if _, err := collection.Indexes().CreateMany(ctx, models); err != nil {
		return ErrUnavailable
	}
	return verifyTODOIndexes(ctx, collection)
}

func verifyTODOCollection(ctx context.Context, database *mongo.Database) error {
	collections, err := database.ListCollectionSpecifications(ctx, bson.D{{Key: "name", Value: todosCollection}})
	if err != nil {
		return ErrUnavailable
	}
	if len(collections) != 1 || collections[0].Type != "collection" {
		return todoTeach("todos collection", "be explicitly provisioned with the approved strict/error validator", ErrConflict)
	}
	optionsDocument := collections[0].Options
	validator := optionsDocument.Lookup("validator")
	level, levelOK := optionsDocument.Lookup("validationLevel").StringValueOK()
	action, actionOK := optionsDocument.Lookup("validationAction").StringValueOK()
	expectedValidator, err := bson.Marshal(TODOCollectionValidator())
	if err != nil || validator.Type != bson.TypeEmbeddedDocument || !bytes.Equal(validator.Document(), expectedValidator) || !levelOK || level != "strict" || !actionOK || action != "error" {
		return todoTeach("todos.validator", "match the canonical stage/qualifier schema with validationLevel=strict and validationAction=error", ErrConflict)
	}
	return nil
}

// upgradeTODOCollectionForHistory permits one known additive evolution: the
// prior strict validator lacked the content-edit idempotency marker. Existing
// documents are validated before the server-owned collMod broadens that enum.
func upgradeTODOCollectionForHistory(ctx context.Context, database *mongo.Database) error {
	collections, err := database.ListCollectionSpecifications(ctx, bson.D{{Key: "name", Value: todosCollection}})
	if err != nil {
		return ErrUnavailable
	}
	if len(collections) != 1 || collections[0].Type != "collection" {
		return todoTeach("todos collection", "be explicitly provisioned with the approved strict/error validator", ErrConflict)
	}
	optionsDocument := collections[0].Options
	validator := optionsDocument.Lookup("validator")
	level, levelOK := optionsDocument.Lookup("validationLevel").StringValueOK()
	action, actionOK := optionsDocument.Lookup("validationAction").StringValueOK()
	if validator.Type != bson.TypeEmbeddedDocument || !levelOK || level != "strict" || !actionOK || action != "error" {
		return todoTeach("todos.validator", "use the recognized strict/error schema before the additive content-edit upgrade", ErrConflict)
	}
	actual := bson.Raw(validator.Document())
	if bsonValidatorEqual(actual, TODOCollectionValidator()) {
		return nil
	}
	if !recognizedTODOCollectionValidator(actual) {
		return todoTeach("todos.validator", "match the canonical schema or a recognized strict/error predecessor", ErrConflict)
	}
	cursor, err := database.Collection(todosCollection).Find(ctx, bson.D{})
	if err != nil {
		return ErrUnavailable
	}
	for cursor.Next(ctx) {
		var record TODORecord
		if cursor.Decode(&record) != nil {
			_ = cursor.Close(ctx)
			return ErrUnavailable
		}
		if err := validateStoredTODO(record); err != nil {
			_ = cursor.Close(ctx)
			return todoTeach("todos.existing_records", "pass the current typed TODO contract before additive validator upgrade", ErrConflict)
		}
	}
	if cursor.Err() != nil {
		_ = cursor.Close(ctx)
		return ErrUnavailable
	}
	if err := cursor.Close(ctx); err != nil {
		return ErrUnavailable
	}
	command := bson.D{
		{Key: "collMod", Value: todosCollection},
		{Key: "validator", Value: TODOCollectionValidator()},
		{Key: "validationLevel", Value: "strict"},
		{Key: "validationAction", Value: "error"},
	}
	if err := database.RunCommand(ctx, command).Err(); err != nil {
		return ErrUnavailable
	}
	return verifyTODOCollection(ctx, database)
}

func recognizedTODOCollectionValidator(actual bson.Raw) bool {
	predecessors := []bson.D{
		TODOCollectionValidatorBeforeEvidenceCapacity(),
		TODOCollectionValidatorBeforeMVPParentUpgrade(),
		TODOCollectionValidatorBeforeRepositoryIdentity(), TODOCollectionValidatorBeforeReviewWaiver(), TODOCollectionValidatorBeforeNextStepEdit(), priorTODOCollectionValidator(),
		TODOCollectionValidatorBeforeContentMigration(), TODOCollectionValidatorBeforeHistoricalAdoption(), TODOCollectionValidatorBeforeOwnership(),
		TODOCollectionValidatorBeforeRecordEvents(), TODOCollectionValidatorBeforeCreation(), TODOCollectionValidatorBeforeReviewOutcome(),
	}
	for _, predecessor := range predecessors {
		if bsonValidatorEqual(actual, predecessor) || bsonValidatorEqual(actual, todoCollectionValidatorWithoutReviewOutcome(predecessor)) {
			return true
		}
		predecessor = todoValidatorWithEvidenceCapacity(predecessor, 64)
		if bsonValidatorEqual(actual, predecessor) || bsonValidatorEqual(actual, todoCollectionValidatorWithoutReviewOutcome(predecessor)) {
			return true
		}
	}
	return false
}

// Keep the exact deployed 64-reference validator as an upgrade predecessor.
// Earlier validator fixtures must retain that bound even after the current
// collection increases its bounded evidence capacity.
func TODOCollectionValidatorBeforeEvidenceCapacity() bson.D {
	return todoValidatorWithEvidenceCapacity(TODOCollectionValidator(), 64)
}

func todoValidatorWithEvidenceCapacity(validator bson.D, limit int) bson.D {
	for _, field := range []string{"proof_refs", "waiver_refs", "implementation_commits"} {
		validator = replaceBSONValueAtPath(validator, []string{"$jsonSchema", "properties", field, "maxItems"}, limit)
	}
	return validator
}

// TODOCollectionValidatorBeforeMVPParentUpgrade is the strict/error validator
// deployed by the MVP parent before PR #10. Its exact shape is pinned to the
// schema at the merged MVP parent: no diff repository identity fields, only
// the then-supported operation kinds, and no review outcomes.
func TODOCollectionValidatorBeforeMVPParentUpgrade() bson.D {
	// MVP parent merge 03ba3dc carried the original seven operation kinds.
	validator := todoCollectionValidatorWithOperationKinds(bson.A{"transition", "verify-current-stage", "content-edit", "evidence-edit", "placement-edit", "historical-adoption", "content-migration"})
	validator = withoutBSONFieldsAtPath(validator, []string{"$jsonSchema", "properties", "diff_expectation", "items", "properties"}, "repository_id", "delivery_branch")
	return todoCollectionValidatorWithoutReviewOutcome(validator)
}

// TODOCollectionValidatorBeforeRepositoryIdentity is the deployed strict
// predecessor that did not allow the approved repository and delivery branch
// on a diff scope. No other fields or validation rules are relaxed.
func TODOCollectionValidatorBeforeRepositoryIdentity() bson.D {
	raw, err := bson.Marshal(TODOCollectionValidatorBeforeEvidenceCapacity())
	if err != nil {
		return nil
	}
	var clone bson.D
	if bson.Unmarshal(raw, &clone) != nil {
		return nil
	}
	return withoutBSONFieldsAtPath(clone, []string{"$jsonSchema", "properties", "diff_expectation", "items", "properties"}, "repository_id", "delivery_branch")
}

func withoutBSONFieldsAtPath(document bson.D, path []string, fields ...string) bson.D {
	if len(path) == 0 {
		filtered := make(bson.D, 0, len(document))
		for _, element := range document {
			if !slices.Contains(fields, element.Key) {
				filtered = append(filtered, element)
			}
		}
		return filtered
	}
	for i := range document {
		if document[i].Key == path[0] {
			child, ok := document[i].Value.(bson.D)
			if !ok {
				return nil
			}
			document[i].Value = withoutBSONFieldsAtPath(child, path[1:], fields...)
			return document
		}
	}
	return nil
}

func replaceBSONValueAtPath(document bson.D, path []string, value any) bson.D {
	if len(path) == 0 {
		return nil
	}
	for i := range document {
		if document[i].Key != path[0] {
			continue
		}
		if len(path) == 1 {
			document[i].Value = value
			return document
		}
		child, ok := document[i].Value.(bson.D)
		if !ok {
			return nil
		}
		document[i].Value = replaceBSONValueAtPath(child, path[1:], value)
		return document
	}
	return nil
}

// TODOCollectionValidatorBeforeHistoricalAdoption is the exact strict
// predecessor whose last-operation enum did not yet include this private
// server-owned mutation. Existing records are scanned before collMod.
func TODOCollectionValidatorBeforeHistoricalAdoption() bson.D {
	return todoCollectionValidatorWithOperationKinds(bson.A{"transition", "verify-current-stage", "content-edit", "evidence-edit", "placement-edit"})
}

func TODOCollectionValidatorBeforeContentMigration() bson.D {
	return todoCollectionValidatorWithOperationKinds(bson.A{"transition", "verify-current-stage", "content-edit", "evidence-edit", "placement-edit", "historical-adoption"})
}

func TODOCollectionValidatorBeforeNextStepEdit() bson.D {
	return todoCollectionValidatorWithOperationKinds(bson.A{"transition", "verify-current-stage", "content-edit", "evidence-edit", "placement-edit", "historical-adoption", "content-migration"})
}

func TODOCollectionValidatorBeforeReviewWaiver() bson.D {
	return todoCollectionValidatorWithOperationKinds(bson.A{"transition", "verify-current-stage", "content-edit", "evidence-edit", "placement-edit", "historical-adoption", "content-migration", "next-step-edit"})
}

func todoCollectionValidatorWithOperationKinds(kinds bson.A) bson.D {
	validator := TODOCollectionValidatorBeforeEvidenceCapacity()
	raw, err := bson.Marshal(validator)
	if err != nil {
		return nil
	}
	var clone bson.D
	if bson.Unmarshal(raw, &clone) != nil {
		return nil
	}
	for i := range clone {
		if clone[i].Key != "$jsonSchema" {
			continue
		}
		schema, ok := clone[i].Value.(bson.D)
		if !ok {
			return nil
		}
		for j := range schema {
			if schema[j].Key != "properties" {
				continue
			}
			properties, ok := schema[j].Value.(bson.D)
			if !ok {
				return nil
			}
			for k := range properties {
				if properties[k].Key != "last_operation" {
					continue
				}
				operation, ok := properties[k].Value.(bson.D)
				if !ok {
					return nil
				}
				for n := range operation {
					if operation[n].Key != "properties" {
						continue
					}
					operationProperties, ok := operation[n].Value.(bson.D)
					if !ok {
						return nil
					}
					for q := range operationProperties {
						if operationProperties[q].Key != "kind" {
							continue
						}
						kind, ok := operationProperties[q].Value.(bson.D)
						if !ok {
							return nil
						}
						for p := range kind {
							if kind[p].Key == "enum" {
								kind[p].Value = kinds
							}
						}
						operationProperties[q].Value = kind
					}
					operation[n].Value = operationProperties
				}
				properties[k].Value = operation
			}
			schema[j].Value = properties
		}
		clone[i].Value = schema
	}
	return clone
}

func bsonValidatorEqual(actual bson.Raw, expected bson.D) bool {
	var actualMap, expectedMap bson.M
	expectedRaw, err := bson.Marshal(expected)
	if err != nil || bson.Unmarshal(actual, &actualMap) != nil || bson.Unmarshal(expectedRaw, &expectedMap) != nil {
		return false
	}
	return reflect.DeepEqual(actualMap, expectedMap)
}

// DiagnoseTODOCollectionSchema is a read-only CLI diagnostic. It intentionally
// stays outside ordinary admission errors so schema internals are not exposed
// through API TEACH responses.
func (m *MongoStore) DiagnoseTODOCollectionSchema(ctx context.Context, companyID string) (TODOCollectionSchemaDiagnostic, error) {
	company, err := m.company(ctx, companyID)
	if err != nil {
		return TODOCollectionSchemaDiagnostic{}, err
	}
	if company.DatabaseName != m.tenantDatabase(companyID) {
		return TODOCollectionSchemaDiagnostic{}, todoTeach("company.database_name", "match the server-owned Company tenant database", ErrConflict)
	}
	database := m.client.Database(company.DatabaseName)
	collectionNames, err := database.ListCollectionNames(ctx, bson.D{})
	if err != nil {
		return TODOCollectionSchemaDiagnostic{}, ErrUnavailable
	}
	sort.Strings(collectionNames)
	specifications, err := database.ListCollectionSpecifications(ctx, bson.D{{Key: "name", Value: todosCollection}})
	if err != nil {
		return TODOCollectionSchemaDiagnostic{}, ErrUnavailable
	}
	if len(specifications) != 1 || specifications[0].Type != "collection" {
		return TODOCollectionSchemaDiagnostic{}, todoTeach("todos collection", "exist as one Mongo collection", ErrNotFound)
	}
	optionsDocument := specifications[0].Options
	actualValue := optionsDocument.Lookup("validator")
	if actualValue.Type != bson.TypeEmbeddedDocument {
		return TODOCollectionSchemaDiagnostic{}, todoTeach("todos.validator", "be a BSON document", ErrConflict)
	}
	actualRaw := bson.Raw(actualValue.Document())
	expectedRaw, err := bson.Marshal(TODOCollectionValidator())
	if err != nil {
		return TODOCollectionSchemaDiagnostic{}, ErrUnavailable
	}
	actualJSON, err := bson.MarshalExtJSON(actualRaw, true, false)
	if err != nil {
		return TODOCollectionSchemaDiagnostic{}, ErrUnavailable
	}
	expectedJSON, err := bson.MarshalExtJSON(TODOCollectionValidator(), true, false)
	if err != nil {
		return TODOCollectionSchemaDiagnostic{}, ErrUnavailable
	}
	var actualValueMap, expectedValueMap bson.M
	if bson.Unmarshal(actualRaw, &actualValueMap) != nil || bson.Unmarshal(expectedRaw, &expectedValueMap) != nil {
		return TODOCollectionSchemaDiagnostic{}, ErrUnavailable
	}
	level, _ := optionsDocument.Lookup("validationLevel").StringValueOK()
	action, _ := optionsDocument.Lookup("validationAction").StringValueOK()
	todoCount, err := database.Collection(todosCollection).CountDocuments(ctx, bson.D{})
	if err != nil {
		return TODOCollectionSchemaDiagnostic{}, ErrUnavailable
	}
	cursor, err := database.Collection(todosCollection).Find(ctx, bson.D{})
	if err != nil {
		return TODOCollectionSchemaDiagnostic{}, ErrUnavailable
	}
	projectSet := make(map[string]struct{})
	var validCount int64
	historyChecks := make([]TODOHistoryReadDiagnostic, 0)
	for cursor.Next(ctx) {
		var record TODORecord
		if cursor.Decode(&record) != nil {
			_ = cursor.Close(ctx)
			return TODOCollectionSchemaDiagnostic{}, ErrUnavailable
		}
		if err := validateStoredTODO(record); err != nil {
			_ = cursor.Close(ctx)
			return TODOCollectionSchemaDiagnostic{}, todoTeach("todos.existing_records."+record.ID, "pass the current typed TODO contract before schema migration", ErrConflict)
		}
		projectSet[record.ProjectID] = struct{}{}
		validCount++
		_, historyErr := m.ReadTODOAsOf(ctx, companyID, record.ProjectID, record.ID, record.Revision)
		check := TODOHistoryReadDiagnostic{ProjectID: record.ProjectID, TODOID: record.ID, Revision: record.Revision, Readable: historyErr == nil}
		if historyErr != nil {
			check.Error = historyErr.Error()
		}
		historyChecks = append(historyChecks, check)
	}
	if cursor.Err() != nil {
		_ = cursor.Close(ctx)
		return TODOCollectionSchemaDiagnostic{}, ErrUnavailable
	}
	if err := cursor.Close(ctx); err != nil {
		return TODOCollectionSchemaDiagnostic{}, ErrUnavailable
	}
	projects := make([]string, 0, len(projectSet))
	for projectID := range projectSet {
		projects = append(projects, projectID)
	}
	sort.Strings(projects)
	return TODOCollectionSchemaDiagnostic{
		CompanyID: companyID, DatabaseName: company.DatabaseName, Collections: collectionNames,
		TODOCount: todoCount, ValidTODOCount: validCount, Projects: projects,
		ByteEqual: bytes.Equal(actualRaw, expectedRaw), SemanticEqual: reflect.DeepEqual(actualValueMap, expectedValueMap),
		ValidationLevel: level, ValidationAction: action, ActualValidator: string(actualJSON), ExpectedValidator: string(expectedJSON),
		HistoryReadChecks: historyChecks,
	}, nil
}

func todoIndexModels() []mongo.IndexModel {
	return []mongo.IndexModel{
		{Keys: bson.D{{Key: "project_id", Value: 1}, {Key: "id", Value: 1}}, Options: options.Index().SetName("todos_project_id_unique").SetUnique(true)},
		{Keys: bson.D{{Key: "project_id", Value: 1}, {Key: "release_id", Value: 1}, {Key: "position", Value: 1}}, Options: options.Index().SetName("todos_project_release_position")},
		{Keys: bson.D{{Key: "project_id", Value: 1}, {Key: "affected_files", Value: 1}}, Options: options.Index().SetName("todos_project_affected_file")},
	}
}

func verifyTODOIndexes(ctx context.Context, collection *mongo.Collection) error {
	specifications, err := collection.Indexes().ListSpecifications(ctx)
	if err != nil {
		return ErrUnavailable
	}
	byName := make(map[string]mongo.IndexSpecification, len(specifications))
	for _, specification := range specifications {
		byName[specification.Name] = specification
	}
	for _, expected := range []struct {
		name   string
		keys   bson.D
		unique bool
	}{
		{"todos_project_id_unique", bson.D{{Key: "project_id", Value: 1}, {Key: "id", Value: 1}}, true},
		{"todos_project_release_position", bson.D{{Key: "project_id", Value: 1}, {Key: "release_id", Value: 1}, {Key: "position", Value: 1}}, false},
		{"todos_project_affected_file", bson.D{{Key: "project_id", Value: 1}, {Key: "affected_files", Value: 1}}, false},
	} {
		actual, ok := byName[expected.name]
		if !ok {
			return todoTeach("todos.indexes."+expected.name, "be installed before import", ErrConflict)
		}
		wantKeys, marshalErr := bson.Marshal(expected.keys)
		actualUnique := actual.Unique != nil && *actual.Unique
		if marshalErr != nil || !bytes.Equal(actual.KeysDocument, wantKeys) || actualUnique != expected.unique {
			return todoTeach("todos.indexes."+expected.name, "match the approved keys and uniqueness", ErrConflict)
		}
	}
	return nil
}

func namespaceExists(err error) bool {
	if err == nil {
		return false
	}
	var commandError mongo.CommandError
	return errors.As(err, &commandError) && commandError.Code == 48 || strings.Contains(err.Error(), "NamespaceExists")
}
