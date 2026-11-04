package registry

import (
	"context"
	"errors"
	"math"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

const reviewWaiverEventKind = "review-waiver-granted"
const reviewWaiverPermissionDecision = "pre-iam-private-api-allow-all"

type TODOReviewWaiverRequest struct {
	ProjectID             string `json:"project_id"`
	TODOID                string `json:"todo_id"`
	ExpectedRevision      *int64 `json:"expected_revision"`
	ExpectedContentDigest string `json:"expected_content_digest"`
	OperationID           string `json:"operation_id"`
	ReviewKind            string `json:"review_kind"`
	Reason                string `json:"reason"`
}

func (m *MongoStore) GrantTODOReviewWaiver(ctx context.Context, companyID string, request TODOReviewWaiverRequest) (TODOContentEditResponse, error) {
	if request.ExpectedRevision == nil || *request.ExpectedRevision < 0 || *request.ExpectedRevision == math.MaxInt64 || !validSourceDigest(request.ExpectedContentDigest) || !todoOperationIDPattern.MatchString(request.OperationID) || !validSummaryField(request.Reason, 512) {
		return TODOContentEditResponse{}, todoTeach("expected_revision/expected_content_digest/operation_id/reason", "provide the current revision and content digest, a stable operation ID, and a nonblank reason", ErrInvalid)
	}
	project, collection, err := m.selectedTODOCollection(ctx, companyID, request.ProjectID, request.TODOID)
	if err != nil {
		return TODOContentEditResponse{}, err
	}
	if project.ID != request.ProjectID {
		return TODOContentEditResponse{}, ErrNotFound
	}
	permission := authorizeTODOAdministrativeOperation(ctx, companyID, request.ProjectID, "grant-todo-review-waiver")
	if !permission.Allowed {
		return TODOContentEditResponse{}, ErrConflict
	}
	database, err := m.todoTenantDatabase(ctx, companyID)
	if err != nil {
		return TODOContentEditResponse{}, err
	}
	digest, err := todoRequestDigest(request)
	if err != nil {
		return TODOContentEditResponse{}, ErrInvalid
	}
	var prior TODOHistoryEvent
	err = database.Collection(todoEventsCollection).FindOne(ctx, bson.D{{Key: "project_id", Value: request.ProjectID}, {Key: "todo_id", Value: request.TODOID}, {Key: "operation_id", Value: request.OperationID}}).Decode(&prior)
	if err == nil {
		if prior.Kind != reviewWaiverEventKind || prior.RequestDigest != digest || prior.ProofReference == nil {
			return TODOContentEditResponse{}, todoTeach("operation_id", "not be reused with another review-waiver request", ErrConflict)
		}
		return TODOContentEditResponse{CompanyID: companyID, ProjectID: request.ProjectID, TODOID: request.TODOID, Revision: prior.Revision, ContentRevision: prior.ContentRevision, ContentDigest: prior.ProofReference.SourceDigest, NoOp: true}, nil
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
	var version TODOContentVersion
	if err := database.Collection(todoContentVersionsCollection).FindOne(ctx, bson.D{{Key: "project_id", Value: record.ProjectID}, {Key: "todo_id", Value: record.ID}}, options.FindOne().SetSort(bson.D{{Key: "revision", Value: -1}})).Decode(&version); err != nil {
		return TODOContentEditResponse{}, ErrUnavailable
	}
	contentDigest, err := todoContentRevisionDigest(record.ProjectID, record.ID, version.Revision, version.Content)
	if err != nil || version.Digest != contentDigest || contentDigest != request.ExpectedContentDigest {
		return TODOContentEditResponse{}, todoTeach("expected_content_digest", "match the exact current immutable content digest; reload the TODO after content changes", ErrConflict)
	}
	if !knownRequiredReviewKind(version.Content.TODOContent, request.ReviewKind) {
		return TODOContentEditResponse{}, todoTeach("review_kind", "name a current planning_review_<kind> subject or a currently required delivery audit kind", ErrInvalid)
	}
	waiver := TODOProofReference{Kind: "review-waiver:" + request.ReviewKind, Source: "mongo-admin-waiver", Reference: request.OperationID, SourceDigest: contentDigest, VerificationState: "verified"}
	for _, existing := range record.WaiverReferences {
		if existing.Kind == waiver.Kind && existing.SourceDigest == contentDigest {
			if existing == waiver {
				return TODOContentEditResponse{}, todoTeach("operation_id", "use the operation ID that created the existing exact review waiver", ErrConflict)
			}
			return TODOContentEditResponse{}, todoTeach("waiver_refs", "avoid duplicate current-digest waivers for this review kind", ErrConflict)
		}
	}
	waivers := append(append([]TODOProofReference(nil), record.WaiverReferences...), waiver)
	nextRevision := record.Revision + 1
	operation := TODOOperationState{ID: request.OperationID, Kind: "grant-todo-review-waiver", RequestDigest: digest, ResultRevision: nextRevision}
	contentRevision := version.Revision
	session, err := m.client.StartSession()
	if err != nil {
		return TODOContentEditResponse{}, ErrUnavailable
	}
	defer session.EndSession(ctx)
	_, err = session.WithTransaction(ctx, func(txCtx context.Context) (any, error) {
		update := bson.D{{Key: "$set", Value: bson.D{{Key: "waiver_refs", Value: waivers}, {Key: "last_operation", Value: operation}, {Key: "updated_at", Value: bson.DateTime(time.Now().UTC().UnixMilli())}}}, {Key: "$inc", Value: bson.D{{Key: "revision", Value: 1}}}}
		result, err := collection.UpdateOne(txCtx, bson.D{{Key: "_id", Value: record.ObjectID}, {Key: "project_id", Value: record.ProjectID}, {Key: "id", Value: record.ID}, {Key: "revision", Value: record.Revision}}, update)
		if err != nil {
			return nil, normalizeMongoError(err)
		}
		if result.MatchedCount != 1 {
			return nil, &todoRevisionConflict{Revision: nextRevision}
		}
		waiverCopy := waiver
		waiversCopy := append([]TODOProofReference(nil), waivers...)
		event := TODOHistoryEvent{ObjectID: bson.NewObjectID(), ProjectID: record.ProjectID, TODOID: record.ID, Revision: nextRevision, Kind: reviewWaiverEventKind, OccurredAt: time.Now().UTC(), OperationID: request.OperationID, RequestDigest: digest, ContentRevision: &contentRevision, PermissionDecision: permission.Decision, Reason: strings.TrimSpace(request.Reason), ProofReference: &waiverCopy, Changes: TODOHistoryChanges{WaiverReferences: &waiversCopy}}
		if _, err := database.Collection(todoEventsCollection).InsertOne(txCtx, event); err != nil {
			return nil, normalizeMongoError(err)
		}
		return nil, nil
	})
	if err != nil {
		return TODOContentEditResponse{}, normalizeMongoError(err)
	}
	return TODOContentEditResponse{CompanyID: companyID, ProjectID: record.ProjectID, TODOID: record.ID, Revision: nextRevision, ContentRevision: &contentRevision, ContentDigest: contentDigest}, nil
}

func knownRequiredReviewKind(content TODOContent, kind string) bool {
	if strings.HasPrefix(kind, "planning_review_") {
		if content.Planning == nil {
			return false
		}
		name := strings.TrimPrefix(kind, "planning_review_")
		for _, review := range content.Planning.Reviews {
			if review.Kind == name {
				return true
			}
		}
		return false
	}
	if !isTODOAuditDeliveryKind(kind) {
		return false
	}
	floors, err := deriveTODOAuditFloors(content.Planning)
	return err == nil && floors[kind] == "required"
}
