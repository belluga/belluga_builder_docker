package registry

import (
	"context"
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

type TODORemovalRequest struct {
	ProjectID        string `json:"project_id"`
	TODOID           string `json:"todo_id"`
	ExpectedRevision *int64 `json:"expected_revision"`
	OperationID      string `json:"operation_id"`
	Actor            string `json:"actor"`
	Reason           string `json:"reason"`
}

type TODORemovalResponse struct {
	CompanyID    string `json:"company_id"`
	ProjectID    string `json:"project_id"`
	TODOID       string `json:"todo_id"`
	TODORevision int64  `json:"todo_revision"`
	Removed      bool   `json:"removed"`
	NoOp         bool   `json:"no_op"`
}

type TODORemovalDetails struct {
	Reason             string         `json:"reason"`
	OccurredAt         time.Time      `json:"occurred_at"`
	ActorClaim         TODOActorClaim `json:"actor_claim"`
	PermissionDecision string         `json:"permission_decision"`
}

func requireActiveTODO(record TODORecord) error {
	if record.Removed {
		return todoTeach("todo_id", "refer to an active TODO; this stable ID was removed and remains available only for exact read/history", ErrConflict)
	}
	return nil
}

func (m *MongoStore) RemoveTODO(ctx context.Context, companyID string, request TODORemovalRequest) (TODORemovalResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	if !validID(companyID) || !validRecordMutationIdentity(request.ProjectID, request.TODOID, request.ExpectedRevision, request.OperationID, request.Reason) || !validSummaryField(request.Actor, 128) {
		return TODORemovalResponse{}, todoTeach("company_id/project_id/todo_id/expected_revision/operation_id/actor/reason", "provide exact Project/TODO identity, current revision, stable operation ID, claimed actor and nonblank reason", ErrInvalid)
	}
	permission := authorizeTODOAdministrativeOperation(ctx, companyID, request.ProjectID, "remove-todo")
	if !permission.Allowed {
		return TODORemovalResponse{}, todoTeach("permission", "obtain Project TODO removal authority", ErrConflict)
	}
	project, collection, err := m.selectedTODOCollection(ctx, companyID, request.ProjectID, request.TODOID)
	if err != nil {
		return TODORemovalResponse{}, err
	}
	if err := upgradeTODOCollectionForHistory(ctx, collection.Database()); err != nil {
		return TODORemovalResponse{}, err
	}
	if err := m.ProvisionTODOHistory(ctx, companyID); err != nil {
		return TODORemovalResponse{}, err
	}
	record, err := readSelectedTODO(ctx, collection, request.ProjectID, request.TODOID)
	if err != nil {
		return TODORemovalResponse{}, err
	}
	database := collection.Database()
	digest, err := todoRequestDigest(request)
	if err != nil {
		return TODORemovalResponse{}, ErrInvalid
	}
	retry := func() (TODORemovalResponse, bool, error) {
		var event TODOHistoryEvent
		err := database.Collection(todoEventsCollection).FindOne(ctx, bson.D{{Key: "project_id", Value: request.ProjectID}, {Key: "todo_id", Value: request.TODOID}, {Key: "operation_id", Value: request.OperationID}}).Decode(&event)
		if errors.Is(err, mongo.ErrNoDocuments) {
			return TODORemovalResponse{}, false, nil
		}
		if err != nil {
			return TODORemovalResponse{}, false, ErrUnavailable
		}
		if event.Kind != "todo-removed" || event.RequestDigest != digest {
			return TODORemovalResponse{}, false, todoTeach("operation_id", "not be reused with different TODO removal content", ErrConflict)
		}
		return TODORemovalResponse{CompanyID: companyID, ProjectID: request.ProjectID, TODOID: request.TODOID, TODORevision: event.Revision, Removed: true, NoOp: true}, true, nil
	}
	if result, ok, err := retry(); ok || err != nil {
		return result, err
	}
	if record.Revision != *request.ExpectedRevision {
		return TODORemovalResponse{}, &todoRevisionConflict{Revision: record.Revision}
	}
	if err := requireActiveTODO(record); err != nil {
		return TODORemovalResponse{}, err
	}
	if record.Stage != "Planning" {
		return TODORemovalResponse{}, todoTeach("stage", "be Planning for removal; preserve delivery proof for advanced stages", ErrConflict)
	}
	if err := m.exactTODORelease(ctx, companyID, project, record); err != nil {
		return TODORemovalResponse{}, err
	}
	if err := requireTODOHistoryBaseline(ctx, database, record); err != nil {
		return TODORemovalResponse{}, err
	}
	next := record.Revision + 1
	removed := true
	now := time.Now().UTC()
	op := TODOOperationState{ID: request.OperationID, Kind: "remove-todo", RequestDigest: digest, ResultRevision: next}
	actor := TODOActorClaim{Value: request.Actor, VerificationStatus: "unverified"}
	event := TODOHistoryEvent{ObjectID: bson.NewObjectID(), ProjectID: request.ProjectID, TODOID: request.TODOID, Revision: next, Kind: "todo-removed", OccurredAt: now, OperationID: request.OperationID, RequestDigest: digest, Changes: TODOHistoryChanges{Removed: &removed}, ActorClaim: &actor, PermissionDecision: permission.Decision, Reason: request.Reason}
	session, err := m.client.StartSession()
	if err != nil {
		return TODORemovalResponse{}, ErrUnavailable
	}
	defer session.EndSession(ctx)
	_, err = session.WithTransaction(ctx, func(txCtx context.Context) (any, error) {
		if err := bumpRoadmapRevision(txCtx, database, companyID, request.ProjectID, project.RoadmapRevision); err != nil {
			return nil, err
		}
		result, err := collection.UpdateOne(txCtx, bson.D{{Key: "_id", Value: record.ObjectID}, {Key: "project_id", Value: request.ProjectID}, {Key: "id", Value: request.TODOID}, {Key: "revision", Value: record.Revision}, {Key: "removed", Value: bson.D{{Key: "$ne", Value: true}}}}, bson.D{{Key: "$set", Value: bson.D{{Key: "removed", Value: true}, {Key: "last_operation", Value: op}, {Key: "updated_at", Value: bson.DateTime(now.UnixMilli())}}}, {Key: "$inc", Value: bson.D{{Key: "revision", Value: 1}}}})
		if err != nil {
			return nil, normalizeMongoError(err)
		}
		if result.MatchedCount != 1 {
			return nil, &todoRevisionConflict{Revision: next}
		}
		if _, err := database.Collection(todoEventsCollection).InsertOne(txCtx, event); err != nil {
			return nil, normalizeMongoError(err)
		}
		return nil, nil
	})
	if err != nil {
		if result, ok, retryErr := retry(); ok || retryErr != nil {
			return result, retryErr
		}
		current, readErr := readSelectedTODO(ctx, collection, request.ProjectID, request.TODOID)
		if readErr == nil && current.Revision != record.Revision {
			return TODORemovalResponse{}, &todoRevisionConflict{Revision: current.Revision}
		}
		return TODORemovalResponse{}, normalizeMongoError(err)
	}
	return TODORemovalResponse{CompanyID: companyID, ProjectID: request.ProjectID, TODOID: request.TODOID, TODORevision: next, Removed: true}, nil
}
