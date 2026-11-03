package registry

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// HistoricalTODOAdoptionRequest is a separately governed acceptance of
// already-existing imported state. It never produces fresh implementation
// evidence and cannot target Mongo-born TODOs.
type HistoricalTODOAdoptionRequest struct {
	ProjectID            string                       `json:"project_id"`
	OperationID          string                       `json:"operation_id"`
	Reason               string                       `json:"reason"`
	HistoricalAcceptance bool                         `json:"historical_acceptance"`
	Items                []HistoricalTODOAdoptionItem `json:"items"`
}

type HistoricalTODOAdoptionItem struct {
	TODOID           string `json:"todo_id"`
	ExpectedRevision int64  `json:"expected_revision"`
	RequestedStage   string `json:"requested_stage"`
	SourceReference  string `json:"source_reference"`
	SourceDigest     string `json:"source_digest"`
}

type HistoricalTODOAdoptionResult struct {
	TODOID       string `json:"todo_id"`
	Revision     int64  `json:"todo_revision"`
	Stage        string `json:"stage"`
	SourceDigest string `json:"source_digest"`
	NoOp         bool   `json:"no_op,omitempty"`
}

type HistoricalTODOAdoptionResponse struct {
	CompanyID string                         `json:"company_id"`
	ProjectID string                         `json:"project_id"`
	Results   []HistoricalTODOAdoptionResult `json:"results"`
	NoOp      bool                           `json:"no_op,omitempty"`
}

type TODOHistoricalAdoptionPermission struct {
	Allowed  bool
	Decision string
}

// canAdoptHistoricalTODO is the private pre-IAM permission seam. Its explicit
// company, project, and operation intent inputs are the future policy boundary.
func canAdoptHistoricalTODO(_ context.Context, _ string, _ string, _ string) TODOHistoricalAdoptionPermission {
	return TODOHistoricalAdoptionPermission{Allowed: true, Decision: "pre-iam-private-api-allow-all"}
}

func validateHistoricalTODOAdoption(request HistoricalTODOAdoptionRequest) error {
	if !validID(request.ProjectID) || !todoOperationIDPattern.MatchString(request.OperationID) || !validSummaryField(request.Reason, 400) || !request.HistoricalAcceptance || len(request.Items) == 0 || len(request.Items) > 64 {
		return todoTeach("project_id/operation_id/reason/historical_acceptance/items", "provide one explicit historical-acceptance batch with valid identity, reason, operation ID, and 1..64 TODOs", ErrInvalid)
	}
	seen := make(map[string]bool, len(request.Items))
	for i, item := range request.Items {
		field := fmt.Sprintf("items[%d]", i)
		if !validID(item.TODOID) || seen[item.TODOID] || item.ExpectedRevision < 0 || item.ExpectedRevision == math.MaxInt64 || !containsTODOString(canonicalTODOStages, item.RequestedStage) || !validSummaryField(item.SourceReference, 512) || !validSourceDigest(item.SourceDigest) {
			return todoTeach(field, "name a unique TODO, expected aggregate revision, canonical stage, and exact source reference/digest", ErrInvalid)
		}
		seen[item.TODOID] = true
	}
	return nil
}

func (m *MongoStore) AdoptHistoricalTODOStages(ctx context.Context, companyID string, request HistoricalTODOAdoptionRequest) (HistoricalTODOAdoptionResponse, error) {
	if err := validateHistoricalTODOAdoption(request); err != nil {
		return HistoricalTODOAdoptionResponse{}, err
	}
	permission := canAdoptHistoricalTODO(ctx, companyID, request.ProjectID, "historical-stage-adoption")
	if !permission.Allowed {
		return HistoricalTODOAdoptionResponse{}, ErrConflict
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	project, collection, err := m.selectedTODOCollection(ctx, companyID, request.ProjectID, request.Items[0].TODOID)
	if err != nil {
		return HistoricalTODOAdoptionResponse{}, err
	}
	company, err := m.company(ctx, companyID)
	if err != nil {
		return HistoricalTODOAdoptionResponse{}, err
	}
	database := m.client.Database(company.DatabaseName)
	if err := upgradeTODOCollectionForHistory(ctx, database); err != nil {
		return HistoricalTODOAdoptionResponse{}, err
	}
	if err := verifyTODOCollection(ctx, database); err != nil {
		return HistoricalTODOAdoptionResponse{}, err
	}
	if err := provisionHistoryCollection(ctx, database, todoContentVersionsCollection, TODOContentVersionsValidator(), todoContentVersionIndexes()); err != nil {
		return HistoricalTODOAdoptionResponse{}, err
	}
	if err := provisionHistoryCollection(ctx, database, todoEventsCollection, TODOEventsValidator(), todoEventIndexes()); err != nil {
		return HistoricalTODOAdoptionResponse{}, err
	}
	if err := verifyTODOHistoryCollections(ctx, database); err != nil {
		return HistoricalTODOAdoptionResponse{}, err
	}
	digest, err := todoRequestDigest(request)
	if err != nil {
		return HistoricalTODOAdoptionResponse{}, ErrInvalid
	}
	response := HistoricalTODOAdoptionResponse{CompanyID: companyID, ProjectID: request.ProjectID, Results: make([]HistoricalTODOAdoptionResult, 0, len(request.Items))}
	records := make([]TODORecord, len(request.Items))
	contentRevisions := make([]int64, len(request.Items))
	existingEvents := 0
	for i, item := range request.Items {
		record, readErr := readSelectedTODO(ctx, collection, request.ProjectID, item.TODOID)
		if readErr != nil {
			return HistoricalTODOAdoptionResponse{}, readErr
		}
		var event TODOHistoryEvent
		findErr := database.Collection(todoEventsCollection).FindOne(ctx, bson.D{{Key: "project_id", Value: request.ProjectID}, {Key: "todo_id", Value: item.TODOID}, {Key: "operation_id", Value: request.OperationID}}).Decode(&event)
		if findErr == nil {
			if event.Kind != "historical-adoption" || event.RequestDigest != digest || event.Revision != item.ExpectedRevision+1 || event.Changes.Stage == nil || *event.Changes.Stage != item.RequestedStage || event.ProofReference == nil || event.ProofReference.Reference != item.SourceReference || event.ProofReference.SourceDigest != item.SourceDigest {
				return HistoricalTODOAdoptionResponse{}, todoTeach("operation_id", "not be reused for a different historical adoption", ErrConflict)
			}
			existingEvents++
			response.Results = append(response.Results, HistoricalTODOAdoptionResult{TODOID: item.TODOID, Revision: event.Revision, Stage: item.RequestedStage, SourceDigest: item.SourceDigest, NoOp: true})
		} else if !errors.Is(findErr, mongo.ErrNoDocuments) {
			return HistoricalTODOAdoptionResponse{}, ErrUnavailable
		} else {
			if record.Revision != item.ExpectedRevision {
				return HistoricalTODOAdoptionResponse{}, &todoRevisionConflict{Revision: record.Revision}
			}
			if err := validateHistoricalTODOAdoptionTarget(record, item); err != nil {
				return HistoricalTODOAdoptionResponse{}, err
			}
			if err := m.exactTODORelease(ctx, companyID, project, record); err != nil {
				return HistoricalTODOAdoptionResponse{}, err
			}
			if err := requireTODOHistoryBaseline(ctx, database, record); err != nil {
				return HistoricalTODOAdoptionResponse{}, err
			}
			var contentVersion TODOContentVersion
			if err := database.Collection(todoContentVersionsCollection).FindOne(ctx, bson.D{{Key: "project_id", Value: request.ProjectID}, {Key: "todo_id", Value: item.TODOID}}, options.FindOne().SetSort(bson.D{{Key: "revision", Value: -1}})).Decode(&contentVersion); err != nil {
				if errors.Is(err, mongo.ErrNoDocuments) {
					return HistoricalTODOAdoptionResponse{}, todoTeach("history.content_version", "have an exact immutable content revision before historical adoption", ErrConflict)
				}
				return HistoricalTODOAdoptionResponse{}, ErrUnavailable
			}
			if contentVersion.Revision < 0 || contentVersion.Revision > record.Revision {
				return HistoricalTODOAdoptionResponse{}, todoTeach("history.content_version.revision", "be nonnegative and not exceed the TODO aggregate revision before historical adoption", ErrConflict)
			}
			records[i] = record
			contentRevisions[i] = contentVersion.Revision
		}
	}
	if existingEvents != 0 && existingEvents != len(request.Items) {
		return HistoricalTODOAdoptionResponse{}, todoTeach("items", "retry the entire atomic adoption batch; partial prior acceptance is a conflict", ErrConflict)
	}
	if existingEvents == len(request.Items) {
		response.NoOp = true
		return response, nil
	}
	session, err := m.client.StartSession()
	if err != nil {
		return HistoricalTODOAdoptionResponse{}, ErrUnavailable
	}
	defer session.EndSession(ctx)
	_, err = session.WithTransaction(ctx, func(txCtx context.Context) (any, error) {
		for i, item := range request.Items {
			record := records[i]
			nextRevision := record.Revision + 1
			proof := TODOProofReference{Kind: "historical-adoption", Source: todoHistoricalSourceName(record.Source), Reference: item.SourceReference, SourceDigest: item.SourceDigest, VerificationState: "verified"}
			operation := TODOOperationState{ID: request.OperationID, Kind: "historical-adoption", RequestDigest: digest, ResultRevision: nextRevision}
			update := bson.D{{Key: "$set", Value: bson.D{{Key: "stage", Value: item.RequestedStage}, {Key: "stage_verification", Value: "verified"}, {Key: "verified_transition_ref", Value: proof}, {Key: "last_operation", Value: operation}, {Key: "updated_at", Value: bson.DateTime(time.Now().UTC().UnixMilli())}}}, {Key: "$inc", Value: bson.D{{Key: "revision", Value: 1}}}}
			result, updateErr := collection.UpdateOne(txCtx, bson.D{{Key: "_id", Value: record.ObjectID}, {Key: "project_id", Value: request.ProjectID}, {Key: "id", Value: item.TODOID}, {Key: "revision", Value: item.ExpectedRevision}}, update)
			if updateErr != nil {
				return nil, normalizeMongoError(updateErr)
			}
			if result.MatchedCount != 1 {
				return nil, &todoRevisionConflict{Revision: nextRevision}
			}
			stage, verified := item.RequestedStage, "verified"
			contentRevision := contentRevisions[i]
			event := TODOHistoryEvent{ObjectID: bson.NewObjectID(), ProjectID: request.ProjectID, TODOID: item.TODOID, Revision: nextRevision, Kind: "historical-adoption", OccurredAt: time.Now().UTC(), OperationID: request.OperationID, RequestDigest: digest, Changes: TODOHistoryChanges{Stage: &stage, StageVerification: &verified, VerifiedTransitionRef: &proof}, ContentRevision: &contentRevision, Reason: request.Reason, ProofReference: &proof, PermissionDecision: permission.Decision}
			if _, insertErr := database.Collection(todoEventsCollection).InsertOne(txCtx, event); insertErr != nil {
				return nil, normalizeMongoError(insertErr)
			}
		}
		return nil, nil
	})
	if err != nil {
		return HistoricalTODOAdoptionResponse{}, normalizeMongoError(err)
	}
	for _, item := range request.Items {
		response.Results = append(response.Results, HistoricalTODOAdoptionResult{TODOID: item.TODOID, Revision: item.ExpectedRevision + 1, Stage: item.RequestedStage, SourceDigest: item.SourceDigest})
	}
	return response, nil
}

func validateHistoricalTODOAdoptionTarget(record TODORecord, item HistoricalTODOAdoptionItem) error {
	if record.Source.Kind == "mongo-content-revision" || todoHistoricalSourceName(record.Source) == "" || !validSourceDigest(record.Source.Digest) {
		return todoTeach("todo.source", "refer to an existing immutable non-Mongo source for historical adoption", ErrConflict)
	}
	if record.StageVerification == "verified" || record.VerifiedTransitionRef != nil {
		return todoTeach("todo.stage_verification", "be unverified and have no prior verified stage reference before historical adoption", ErrConflict)
	}
	if todoHistoricalSourceReference(record.Source) != item.SourceReference || record.Source.Digest != item.SourceDigest {
		return todoTeach("items[].source_reference/source_digest", "match the exact immutable source already admitted for this TODO", ErrConflict)
	}
	return nil
}

func todoHistoricalSourceName(source TODOSource) string {
	if source.Kind != "" {
		return source.Kind
	}
	return source.Foundation
}

func todoHistoricalSourceReference(source TODOSource) string {
	if source.Ref != "" {
		return source.Ref
	}
	if source.Foundation == "" || !validGitCommit(source.Revision) || source.Path == "" {
		return ""
	}
	return source.Foundation + "@" + source.Revision + ":" + source.Path
}
