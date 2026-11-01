package registry

import (
	"bytes"
	"context"
	"errors"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

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
