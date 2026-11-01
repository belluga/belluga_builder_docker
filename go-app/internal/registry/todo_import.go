package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

func (m *MongoStore) ImportSelectedTODOs(ctx context.Context, request SelectedTODOImportRequest) (SelectedTODOImportReport, error) {
	report, err := ValidateSelectedTODOImport(request)
	if err != nil {
		return SelectedTODOImportReport{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	database := m.tenantDatabase("belluga-solutions")
	if err := m.ensureTenantIndexes(ctx, database); err != nil {
		return SelectedTODOImportReport{}, ErrUnavailable
	}
	tenant := m.client.Database(database)
	if err := verifyTODOCollection(ctx, tenant); err != nil {
		return SelectedTODOImportReport{}, err
	}
	if err := verifyTODOIndexes(ctx, tenant.Collection(todosCollection)); err != nil {
		return SelectedTODOImportReport{}, err
	}
	records := canonicalSelectedTODOs(request.TODOs)
	mappingDigests := make(map[string]string, len(records))
	for i := range records {
		digest, err := todoMappingDigest(records[i])
		if err != nil {
			return SelectedTODOImportReport{}, ErrInvalid
		}
		mappingDigests[records[i].ID] = digest
		records[i].Source.MappingDigest = digest
	}
	session, err := m.client.StartSession()
	if err != nil {
		return SelectedTODOImportReport{}, ErrUnavailable
	}
	defer session.EndSession(ctx)
	var imported bool
	_, err = session.WithTransaction(ctx, func(txCtx context.Context) (any, error) {
		imported = false
		if err := validateSelectedProjects(txCtx, m.client.Database(database)); err != nil {
			return nil, err
		}
		if err := ensureBuilderImportHierarchy(txCtx, m.client.Database(database)); err != nil {
			return nil, err
		}
		if err := verifyBoorav071Release(txCtx, m.client.Database(database)); err != nil {
			return nil, err
		}
		collection := m.client.Database(database).Collection(todosCollection)
		existing := 0
		for _, record := range records {
			var prior TODORecord
			findErr := collection.FindOne(txCtx, bson.D{{Key: "project_id", Value: record.ProjectID}, {Key: "id", Value: record.ID}}).Decode(&prior)
			if findErr == nil {
				if prior.ObjectID.IsZero() || prior.Source.Foundation != record.Source.Foundation || prior.Source.Revision != record.Source.Revision || prior.Source.Path != record.Source.Path || prior.Source.Digest != record.Source.Digest || prior.Source.MappingDigest != mappingDigests[record.ID] || prior.ProjectID != record.ProjectID || prior.ReleaseID != record.ReleaseID || prior.Revision < 0 {
					return nil, fmt.Errorf("%w: existing TODO %s conflicts with pinned source", ErrConflict, record.ID)
				}
				existing++
			} else if !errors.Is(findErr, mongo.ErrNoDocuments) {
				return nil, ErrUnavailable
			}
		}
		if existing == len(records) {
			return false, nil
		}
		if existing != 0 {
			return nil, fmt.Errorf("%w: selected TODO set is partially present", ErrConflict)
		}
		now := bson.DateTime(time.Now().UTC().UnixMilli())
		for _, record := range records {
			record.ObjectID = bson.NewObjectID()
			record.Revision = 0
			record.CreatedAt = now
			record.UpdatedAt = now
			record.Source.ImportedAt = now
			record.StageVerification = "unverified"
			if _, err := collection.InsertOne(txCtx, record); err != nil {
				return nil, normalizeMongoError(err)
			}
		}
		imported = true
		return true, nil
	})
	if err != nil {
		if errors.Is(err, ErrInvalid) || errors.Is(err, ErrConflict) {
			return SelectedTODOImportReport{}, err
		}
		return SelectedTODOImportReport{}, normalizeMongoError(err)
	}
	report.NoOp = !imported
	return report, nil
}

func canonicalSelectedTODOs(input []TODORecord) []TODORecord {
	result := append([]TODORecord(nil), input...)
	sortSelectedTODOs(result)
	return result
}

func sortSelectedTODOs(records []TODORecord) {
	sort.Slice(records, func(i, j int) bool {
		if records[i].ProjectID != records[j].ProjectID {
			return records[i].ProjectID < records[j].ProjectID
		}
		if records[i].ReleaseID != records[j].ReleaseID {
			return records[i].ReleaseID < records[j].ReleaseID
		}
		return *records[i].Position < *records[j].Position
	})
}

func todoMappingDigest(record TODORecord) (string, error) {
	record.ObjectID = bson.NilObjectID
	record.Revision = 0
	record.CreatedAt = 0
	record.UpdatedAt = 0
	record.Source.ImportedAt = 0
	record.Source.MappingDigest = ""
	data, err := bson.Marshal(record)
	if err != nil || len(data) > maxTODORecordBytes {
		return "", ErrInvalid
	}
	hash := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(hash[:]), nil
}

func validateSelectedProjects(ctx context.Context, database *mongo.Database) error {
	projects := database.Collection(projectsCollection)
	for _, projectID := range []string{"boora", "builder"} {
		var project projectDocument
		if err := projects.FindOne(ctx, bson.D{{Key: "id", Value: projectID}}).Decode(&project); err != nil {
			if errors.Is(err, mongo.ErrNoDocuments) {
				return fmt.Errorf("%w: selected Project %s is absent", ErrConflict, projectID)
			}
			return ErrUnavailable
		}
		if project.ObjectID.IsZero() || project.ID != projectID || project.CompanyID != "belluga-solutions" {
			return fmt.Errorf("%w: selected Project %s is outside the Belluga Solutions Tenant", ErrConflict, projectID)
		}
	}
	return nil
}

func ensureBuilderImportHierarchy(ctx context.Context, database *mongo.Database) error {
	epic := roadmapEpic{ProjectID: "builder", ID: "e-06", Name: "Conexão Banco de Dados", Description: "Estabelecer a persistência MongoDB do Builder por empresa, com leitura de Roadmap e migração gradual dos TODOs.", Position: 4}
	release := roadmapRelease{ProjectID: "builder", ID: "r-08", EpicID: "e-06", Name: "Conexão provisória sem autenticação", Description: "Conectar empresas, projetos e hierarquia inicial ao MongoDB sem atribuir identidade ou permissões de usuário; migrar os TODOs de v0.7.1 em um contrato separado.", Position: 0}
	if err := insertOrVerifyEpic(ctx, database.Collection(epicsCollection), epic); err != nil {
		return err
	}
	if err := insertOrVerifyRelease(ctx, database.Collection(releasesCollection), release); err != nil {
		return err
	}
	return nil
}

func insertOrVerifyEpic(ctx context.Context, collection *mongo.Collection, expected roadmapEpic) error {
	var prior roadmapEpic
	err := collection.FindOne(ctx, bson.D{{Key: "project_id", Value: expected.ProjectID}, {Key: "id", Value: expected.ID}}).Decode(&prior)
	if err == nil {
		if prior.StorageID.IsZero() || prior.ProjectID != expected.ProjectID || prior.ID != expected.ID || prior.Name != expected.Name || prior.Description != expected.Description || prior.Position != expected.Position {
			return fmt.Errorf("%w: Builder Epic e-06 conflicts with the pinned catalog", ErrConflict)
		}
		return nil
	}
	if !errors.Is(err, mongo.ErrNoDocuments) {
		return ErrUnavailable
	}
	expected.StorageID = bson.NewObjectID()
	if _, err := collection.InsertOne(ctx, expected); err != nil {
		return normalizeMongoError(err)
	}
	return nil
}

func insertOrVerifyRelease(ctx context.Context, collection *mongo.Collection, expected roadmapRelease) error {
	var prior roadmapRelease
	err := collection.FindOne(ctx, bson.D{{Key: "project_id", Value: expected.ProjectID}, {Key: "id", Value: expected.ID}}).Decode(&prior)
	if err == nil {
		if prior.StorageID.IsZero() || prior.ProjectID != expected.ProjectID || prior.ID != expected.ID || prior.EpicID != expected.EpicID || prior.Name != expected.Name || prior.Description != expected.Description || prior.Position != expected.Position {
			return fmt.Errorf("%w: Builder Release r-08 conflicts with the pinned catalog", ErrConflict)
		}
		return nil
	}
	if !errors.Is(err, mongo.ErrNoDocuments) {
		return ErrUnavailable
	}
	expected.StorageID = bson.NewObjectID()
	if _, err := collection.InsertOne(ctx, expected); err != nil {
		return normalizeMongoError(err)
	}
	return nil
}

func verifyBoorav071Release(ctx context.Context, database *mongo.Database) error {
	var release roadmapRelease
	err := database.Collection(releasesCollection).FindOne(ctx, bson.D{{Key: "project_id", Value: "boora"}, {Key: "id", Value: "release-v0-7-1-participant-media"}}).Decode(&release)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return fmt.Errorf("%w: Bóora v0.7.1 Release is absent", ErrConflict)
		}
		return ErrUnavailable
	}
	if release.StorageID.IsZero() || release.ProjectID != "boora" || release.ID != "release-v0-7-1-participant-media" || release.EpicID != "epic-pilot" || release.Name != "v0.7.1 — participant media" {
		return fmt.Errorf("%w: Bóora v0.7.1 Release does not belong to Project boora", ErrConflict)
	}
	return nil
}
