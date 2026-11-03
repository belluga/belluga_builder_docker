package registry

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

const todoContentMigrationEventKind = "content_migrated"
const todoContentMigrationAcceptance = "reviewed-v2-contract-preserves-v1-history"

// TODOContentMigrationRequest is an explicit, per-record v1→v2 conversion.
// The caller supplies typed JSON and binds it to both the current TODO revision
// and its exact immutable v1 content and source identities.
type TODOContentMigrationRequest struct {
	ProjectID               string              `json:"project_id"`
	TODOID                  string              `json:"todo_id"`
	ExpectedRevision        *int64              `json:"expected_revision"`
	ExpectedContentRevision int64               `json:"expected_content_revision"`
	ExpectedContentDigest   string              `json:"expected_content_digest"`
	ExpectedSourceReference string              `json:"expected_source_reference"`
	ExpectedSourceDigest    string              `json:"expected_source_digest"`
	OperationID             string              `json:"operation_id"`
	Reason                  string              `json:"reason"`
	ReviewedAcceptance      string              `json:"reviewed_acceptance"`
	Content                 TODOAuthoredContent `json:"content"`
}

func (m *MongoStore) MigrateTODOContent(ctx context.Context, companyID string, request TODOContentMigrationRequest) (TODOContentEditResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	if !validTODOContentMigrationRequest(request) {
		return TODOContentEditResponse{}, todoTeach("migration", "provide a pinned admitted TODO, exact v1/source identities, stable operation ID, reason, and complete schema-v2 content", ErrInvalid)
	}
	projectID, collection, err := m.selectedTODOCollection(ctx, companyID, request.ProjectID, request.TODOID)
	if err != nil {
		return TODOContentEditResponse{}, err
	}
	if projectID.ID != request.ProjectID {
		return TODOContentEditResponse{}, ErrNotFound
	}
	company, err := m.company(ctx, companyID)
	if err != nil {
		return TODOContentEditResponse{}, err
	}
	database := m.client.Database(company.DatabaseName)
	digest, err := todoRequestDigest(request)
	if err != nil {
		return TODOContentEditResponse{}, ErrInvalid
	}
	var prior TODOHistoryEvent
	err = database.Collection(todoEventsCollection).FindOne(ctx, bson.D{{Key: "project_id", Value: request.ProjectID}, {Key: "todo_id", Value: request.TODOID}, {Key: "operation_id", Value: request.OperationID}}).Decode(&prior)
	if err == nil {
		if prior.Kind != todoContentMigrationEventKind || prior.RequestDigest != digest || prior.ContentRevision == nil {
			return TODOContentEditResponse{}, todoTeach("operation_id", "not be reused with another migration payload", ErrConflict)
		}
		var migrated TODOContentVersion
		if err := database.Collection(todoContentVersionsCollection).FindOne(ctx, bson.D{{Key: "project_id", Value: request.ProjectID}, {Key: "todo_id", Value: request.TODOID}, {Key: "revision", Value: *prior.ContentRevision}}).Decode(&migrated); err != nil {
			return TODOContentEditResponse{}, ErrUnavailable
		}
		return TODOContentEditResponse{CompanyID: companyID, ProjectID: request.ProjectID, TODOID: request.TODOID, Revision: prior.Revision, ContentRevision: prior.ContentRevision, ContentDigest: migrated.Digest, NoOp: true}, nil
	}
	if !errors.Is(err, mongo.ErrNoDocuments) {
		return TODOContentEditResponse{}, ErrUnavailable
	}
	record, err := readSelectedTODO(ctx, collection, request.ProjectID, request.TODOID)
	if err != nil {
		return TODOContentEditResponse{}, err
	}
	if record.Revision != *request.ExpectedRevision {
		return TODOContentEditResponse{}, &todoRevisionConflict{Revision: record.Revision}
	}
	if err := m.exactTODORelease(ctx, companyID, projectID, record); err != nil {
		return TODOContentEditResponse{}, err
	}
	if err := requireTODOHistoryBaseline(ctx, database, record); err != nil {
		return TODOContentEditResponse{}, err
	}
	var current TODOContentVersion
	if err := database.Collection(todoContentVersionsCollection).FindOne(ctx, bson.D{{Key: "project_id", Value: record.ProjectID}, {Key: "todo_id", Value: record.ID}}, options.FindOne().SetSort(bson.D{{Key: "revision", Value: -1}})).Decode(&current); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return TODOContentEditResponse{}, todoTeach("history.content_version", "have an exact immutable v1 content version before migration", ErrConflict)
		}
		return TODOContentEditResponse{}, ErrUnavailable
	}
	if current.Content.SchemaVersion != 0 && current.Content.SchemaVersion != 1 {
		return TODOContentEditResponse{}, todoTeach("content.schema_version", "migrate only a current v1 authored contract", ErrConflict)
	}
	expectedCurrentDigest, err := todoContentRevisionDigest(record.ProjectID, record.ID, current.Revision, current.Content)
	if err != nil || current.Revision != request.ExpectedContentRevision ||
		(request.ExpectedContentDigest != current.Digest && !(current.Digest == "" && request.ExpectedContentDigest == expectedCurrentDigest)) ||
		(request.ExpectedContentDigest != expectedCurrentDigest) {
		return TODOContentEditResponse{}, todoTeach("expected_content_revision/digest", "match the exact latest immutable v1 content and its canonical computed digest", ErrConflict)
	}
	sourceReference, sourceDigest := todoContentMigrationSource(record)
	if sourceReference == "" || request.ExpectedSourceReference != sourceReference || request.ExpectedSourceDigest != sourceDigest {
		return TODOContentEditResponse{}, todoTeach("expected_source_reference/digest", "match the exact admitted Foundation or Mongo content source", ErrConflict)
	}
	if err := validateTODOContentMigrationTarget(record, request.Content); err != nil {
		return TODOContentEditResponse{}, err
	}
	if *request.ExpectedRevision == int64(^uint64(0)>>1) {
		return TODOContentEditResponse{}, ErrInvalid
	}
	nextRevision := record.Revision + 1
	content := withContractOwnership(request.Content)
	contentDigest, err := todoContentRevisionDigest(record.ProjectID, record.ID, nextRevision, content)
	if err != nil {
		return TODOContentEditResponse{}, ErrInvalid
	}
	operation := TODOOperationState{ID: request.OperationID, Kind: "content-migration", RequestDigest: digest, ResultRevision: nextRevision}
	session, err := m.client.StartSession()
	if err != nil {
		return TODOContentEditResponse{}, ErrUnavailable
	}
	defer session.EndSession(ctx)
	_, err = session.WithTransaction(ctx, func(txCtx context.Context) (any, error) {
		update := bson.D{{Key: "$set", Value: bson.D{
			{Key: "title", Value: content.Title},
			{Key: "objective", Value: content.Objective},
			{Key: "last_operation", Value: operation},
			{Key: "updated_at", Value: bson.DateTime(time.Now().UTC().UnixMilli())},
		}}, {Key: "$inc", Value: bson.D{{Key: "revision", Value: 1}}}}
		result, err := collection.UpdateOne(txCtx, bson.D{{Key: "_id", Value: record.ObjectID}, {Key: "project_id", Value: record.ProjectID}, {Key: "id", Value: record.ID}, {Key: "revision", Value: record.Revision}}, update)
		if err != nil {
			return nil, normalizeMongoError(err)
		}
		if result.MatchedCount != 1 {
			return nil, &todoRevisionConflict{Revision: nextRevision}
		}
		contentRevision := nextRevision
		version := TODOContentVersion{ObjectID: bson.NewObjectID(), ProjectID: record.ProjectID, TODOID: record.ID, Revision: contentRevision, Digest: contentDigest, Content: content}
		if _, err := database.Collection(todoContentVersionsCollection).InsertOne(txCtx, version); err != nil {
			return nil, normalizeMongoError(err)
		}
		fromContentRevision := current.Revision
		event := TODOHistoryEvent{ObjectID: bson.NewObjectID(), ProjectID: record.ProjectID, TODOID: record.ID, Revision: nextRevision, Kind: todoContentMigrationEventKind, OccurredAt: time.Now().UTC(), OperationID: request.OperationID, RequestDigest: digest, ContentRevision: &contentRevision, FromContentRevision: &fromContentRevision, FromContentDigest: expectedCurrentDigest, ToContentDigest: contentDigest, ReviewedAcceptance: request.ReviewedAcceptance, PermissionDecision: "pre-iam-private-api-allow-all", Reason: request.Reason}
		if _, err := database.Collection(todoEventsCollection).InsertOne(txCtx, event); err != nil {
			return nil, normalizeMongoError(err)
		}
		return nil, nil
	})
	if err != nil {
		return TODOContentEditResponse{}, normalizeMongoError(err)
	}
	contentRevision := nextRevision
	return TODOContentEditResponse{CompanyID: companyID, ProjectID: record.ProjectID, TODOID: record.ID, Revision: nextRevision, ContentRevision: &contentRevision, ContentDigest: contentDigest}, nil
}

func validTODOContentMigrationRequest(request TODOContentMigrationRequest) bool {
	return validID(request.ProjectID) && validID(request.TODOID) && request.ExpectedRevision != nil && *request.ExpectedRevision >= 0 && request.ExpectedContentRevision >= 0 && validSourceDigest(request.ExpectedContentDigest) && request.ExpectedSourceReference != "" && validSummaryField(request.ExpectedSourceReference, 4096) && validSourceDigest(request.ExpectedSourceDigest) && todoOperationIDPattern.MatchString(request.OperationID) && validSummaryField(request.Reason, 512) && request.ReviewedAcceptance == todoContentMigrationAcceptance && request.Content.SchemaVersion == todoContentSchemaVersion
}

func todoContentMigrationSource(record TODORecord) (string, string) {
	if record.Source.Kind == "" && record.Source.Foundation != "" && validGitCommit(record.Source.Revision) && record.Source.Path != "" && validSourceDigest(record.Source.Digest) {
		return todoHistoricalSourceReference(record.Source), record.Source.Digest
	}
	if record.Source.Kind == "mongo-content-revision" && record.Source.Ref != "" && validSourceDigest(record.Source.Digest) {
		return record.Source.Ref, record.Source.Digest
	}
	return "", ""
}

func validateTODOContentMigrationTarget(record TODORecord, target TODOAuthoredContent) error {
	if target.SchemaVersion != todoContentSchemaVersion || target.Sections != nil || !validName(target.Title) || !validSummaryField(target.Objective, 1024) || target.DiffExpectation == nil || target.AffectedFiles == nil {
		return todoTeach("content", "provide a bounded schema-v2 typed contract without replacing the legacy section stream", ErrInvalid)
	}
	if err := ValidateTODOContent(target.TODOContent, true); err != nil {
		return err
	}
	if err := validateAuthoredEnvelope(target); err != nil {
		return err
	}
	sourceRevision := record.Source.Revision
	if sourceRevision == "" {
		sourceRevision = record.Source.Ref
	}
	for i, entry := range target.LegacyContext {
		if sourceRevision == "" || entry.SourceRevision != sourceRevision || entry.SectionPosition >= len(record.Sections) {
			return todoTeach(fmt.Sprintf("content.legacy_context[%d]", i), "point to an exact section of this TODO's pinned v1 source revision", ErrConflict)
		}
		section := record.Sections[entry.SectionPosition]
		if section.Position != entry.SectionPosition || section.Title != entry.Title || section.BodyMarkdown != entry.Body || todoBodyDigest(section.BodyMarkdown) != entry.Digest {
			return todoTeach(fmt.Sprintf("content.legacy_context[%d]", i), "preserve the exact source position, title, body bytes, and body digest", ErrConflict)
		}
	}
	return nil
}
