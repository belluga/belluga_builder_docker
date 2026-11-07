package registry

import (
	"context"
	"errors"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// designSystemRemoteVerifier is an internal server boundary. Implementations
// must inspect the configured private remote; no values from an API request may
// be used as verification results.
type designSystemRemoteVerifier interface {
	VerifyDesignSystemMutation(context.Context, string, string, Binding, DesignSystemMutationIntent) (designSystemRemoteState, error)
}

type designSystemRemoteState struct {
	RemoteHead           string
	CommitSHA            string
	ParentSHA            string
	TreeSHA              string
	InventoryDigest      string
	Reachable            bool
	ReachabilityVerified bool
}

type DesignSystemMutationResult struct {
	OperationID   string `json:"operation_id" bson:"operation_id"`
	Outcome       string `json:"outcome" bson:"outcome"`
	SelectedSHA   string `json:"selected_sha,omitempty" bson:"selected_sha,omitempty"`
	InventoryHash string `json:"inventory_digest,omitempty" bson:"inventory_digest,omitempty"`
	State         string `json:"state" bson:"state"`
}

// recoverDesignSystemMutation is intentionally package-private until the
// Git-backed server adapter can provide remote reachability and tree evidence.
// Uncertain or inconsistent evidence leaves the durable intent pending.
func (m *MongoStore) recoverDesignSystemMutation(ctx context.Context, companyID, projectID string) (DesignSystemMutationResult, error) {
	if m.designSystemVerifier == nil || !validID(companyID) || !validID(projectID) {
		return DesignSystemMutationResult{}, ErrInvalid
	}
	company, err := m.company(ctx, companyID)
	if err != nil {
		return DesignSystemMutationResult{}, err
	}
	collection := m.client.Database(company.DatabaseName).Collection(projectsCollection)
	var stored projectDocument
	if err := collection.FindOne(ctx, bson.D{{Key: "id", Value: projectID}}).Decode(&stored); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return DesignSystemMutationResult{}, ErrNotFound
		}
		return DesignSystemMutationResult{}, normalizeMongoError(err)
	}
	if stored.CompanyID != companyID || stored.Binding == nil || !validBinding(*stored.Binding) || stored.PendingDesignSystemMutation == nil {
		return DesignSystemMutationResult{}, ErrUnavailable
	}
	intent := *stored.PendingDesignSystemMutation
	if !validDesignSystemMutationIntent(intent) {
		return DesignSystemMutationResult{}, ErrUnavailable
	}
	remote, err := m.designSystemVerifier.VerifyDesignSystemMutation(ctx, companyID, projectID, *stored.Binding, intent)
	if err != nil {
		return DesignSystemMutationResult{}, normalizeMongoError(err)
	}
	result := DesignSystemMutationResult{OperationID: intent.OperationID}
	switch {
	case remote.ReachabilityVerified && remote.Reachable && remote.CommitSHA == intent.PreparedCommitSHA && remote.ParentSHA == intent.ExpectedHead && remote.TreeSHA == intent.CandidateTreeSHA && remote.InventoryDigest == intent.InventoryDigest && validFullGitSHA(remote.RemoteHead):
		result.Outcome = "published"
		if intent.Operation == "delete" {
			result.State = "absent"
		} else {
			result.State = "selected"
			result.SelectedSHA = intent.PreparedCommitSHA
			result.InventoryHash = intent.InventoryDigest
		}
	case remote.ReachabilityVerified && !remote.Reachable && remote.RemoteHead == intent.ExpectedHead:
		result.Outcome = "not_pushed"
		result.State = stored.DesignSystemState
		if result.State == "" {
			result.State = "unselected"
		}
		result.SelectedSHA = stored.DesignSystemSHA
		result.InventoryHash = stored.DesignSystemInventoryDigest
	case remote.ReachabilityVerified && !remote.Reachable && validFullGitSHA(remote.RemoteHead) && remote.RemoteHead != intent.ExpectedHead:
		// The trusted verifier has confirmed the branch moved and the prepared
		// commit is not reachable from its current head. Preserve any prior
		// selection, clear this stale intent, and require a new operation against
		// the new HEAD instead of leaving recovery permanently pending.
		result.Outcome = "superseded"
		result.State = stored.DesignSystemState
		if result.State == "" {
			result.State = "unselected"
		}
		result.SelectedSHA = stored.DesignSystemSHA
		result.InventoryHash = stored.DesignSystemInventoryDigest
	default:
		return DesignSystemMutationResult{}, todoTeach("pending_operation", "remote Git state is uncertain or does not match the prepared commit; keep the Project Design System operation pending for recovery", ErrUnavailable)
	}
	filter := bson.D{
		{Key: "_id", Value: stored.ObjectID},
		{Key: "company_id", Value: companyID},
		{Key: "binding", Value: *stored.Binding},
		{Key: "pending_design_system_mutation", Value: intent},
	}
	if stored.BindingRevision == 0 {
		filter = append(filter, bson.E{Key: "binding_revision", Value: bson.M{"$in": []any{nil, int64(0)}}})
	} else {
		filter = append(filter, bson.E{Key: "binding_revision", Value: stored.BindingRevision})
	}
	update := bson.M{"$set": bson.M{"design_system_last_operation": result}, "$unset": bson.M{"pending_design_system_mutation": ""}}
	if result.Outcome == "published" {
		update["$set"].(bson.M)["design_system_state"] = result.State
		if result.State == "selected" {
			update["$set"].(bson.M)["design_system_sha"] = result.SelectedSHA
			update["$set"].(bson.M)["design_system_inventory_digest"] = result.InventoryHash
		} else {
			update["$unset"].(bson.M)["design_system_sha"] = ""
			update["$unset"].(bson.M)["design_system_inventory_digest"] = ""
		}
	}
	write, err := collection.UpdateOne(ctx, filter, update)
	if err != nil {
		return DesignSystemMutationResult{}, normalizeMongoError(err)
	}
	if write.MatchedCount != 1 {
		return DesignSystemMutationResult{}, todoTeach("pending_operation", "Project Design System state changed during remote verification; reload and recover the pending operation", ErrConflict)
	}
	return result, nil
}

// ReadDesignSystemState returns only Project-selected state and the durable
// operation needed for recovery; repository credentials and local paths stay
// private to the Registry.
func (m *MongoStore) ReadDesignSystemState(ctx context.Context, companyID, projectID string) (DesignSystemState, error) {
	if !validID(companyID) || !validID(projectID) {
		return DesignSystemState{}, todoTeach("company_id/project_id", "use valid identities from the route", ErrInvalid)
	}
	company, err := m.company(ctx, companyID)
	if err != nil {
		return DesignSystemState{}, err
	}
	var stored projectDocument
	err = m.client.Database(company.DatabaseName).Collection(projectsCollection).FindOne(ctx, bson.D{{Key: "id", Value: projectID}}).Decode(&stored)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return DesignSystemState{}, ErrNotFound
	}
	if err != nil {
		return DesignSystemState{}, normalizeMongoError(err)
	}
	if stored.CompanyID != companyID || stored.ObjectID.IsZero() {
		return DesignSystemState{}, ErrNotFound
	}
	selectionState := stored.DesignSystemState
	bound := stored.Binding != nil && stored.Binding.RepositoryID != "" && stored.Binding.PublicationBranch != ""
	if !bound {
		if stored.DesignSystemSHA != "" || stored.DesignSystemInventoryDigest != "" || selectionState == "selected" {
			return DesignSystemState{}, ErrUnavailable
		}
		if selectionState == "" {
			if stored.BindingRevision == 0 {
				selectionState = "never_bound"
			} else {
				selectionState = "unbound"
			}
		}
	} else {
		if !validBinding(*stored.Binding) {
			return DesignSystemState{}, ErrUnavailable
		}
		if selectionState == "" {
			selectionState = "unselected"
		}
	}
	if selectionState != "never_bound" && selectionState != "unbound" && selectionState != "unselected" && selectionState != "selected" && selectionState != "absent" {
		return DesignSystemState{}, ErrUnavailable
	}
	selection := DesignSystemSelection{State: selectionState, BindingRevision: stored.BindingRevision}
	if bound {
		selection.RepositoryID = stored.Binding.RepositoryID
		selection.PublicationBranch = stored.Binding.PublicationBranch
	}
	if selectionState == "selected" {
		if !validFullGitSHA(stored.DesignSystemSHA) || !validSourceDigest(stored.DesignSystemInventoryDigest) {
			return DesignSystemState{}, ErrUnavailable
		}
		selection.SHA = stored.DesignSystemSHA
		selection.InventoryDigest = stored.DesignSystemInventoryDigest
	}
	if stored.PendingDesignSystemMutation != nil && !validDesignSystemMutationIntent(*stored.PendingDesignSystemMutation) {
		return DesignSystemState{}, ErrUnavailable
	}
	return DesignSystemState{
		Selection:        selection,
		PendingOperation: stored.PendingDesignSystemMutation,
		LastOperation:    stored.DesignSystemLastOperation,
	}, nil
}

// RecoverDesignSystemMutation is idempotent by exact operation ID. It exposes
// no remote proof inputs; only the configured server-side Git verifier can
// clear or finalize the pending operation.
func (m *MongoStore) RecoverDesignSystemMutation(ctx context.Context, companyID, projectID, operationID string) (DesignSystemMutationResult, error) {
	if !validID(operationID) {
		return DesignSystemMutationResult{}, todoTeach("operation_id", "use the exact stable operation ID from the pending intent", ErrInvalid)
	}
	state, err := m.ReadDesignSystemState(ctx, companyID, projectID)
	if err != nil {
		return DesignSystemMutationResult{}, err
	}
	if state.PendingOperation == nil {
		if state.LastOperation != nil && state.LastOperation.OperationID == operationID {
			return *state.LastOperation, nil
		}
		return DesignSystemMutationResult{}, todoTeach("operation_id", "match the Project's pending or most recently completed Design System operation", ErrConflict)
	}
	if state.PendingOperation.OperationID != operationID {
		return DesignSystemMutationResult{}, todoTeach("pending_operation", "recover the exact pending Project Design System operation", ErrConflict)
	}
	result, recoverErr := m.recoverDesignSystemMutation(ctx, companyID, projectID)
	if recoverErr == nil {
		return result, nil
	}
	// A concurrent same-key recovery may have won the Mongo CAS while this call
	// was verifying. Return only that server-persisted result on exact ID match.
	latest, readErr := m.ReadDesignSystemState(ctx, companyID, projectID)
	if readErr == nil && latest.LastOperation != nil && latest.LastOperation.OperationID == operationID && latest.PendingOperation == nil {
		return *latest.LastOperation, nil
	}
	return DesignSystemMutationResult{}, recoverErr
}

func (m *MongoStore) BeginDesignSystemMutation(ctx context.Context, companyID, projectID string, intent DesignSystemMutationIntent) (DesignSystemMutationIntent, error) {
	if !validID(companyID) || !validID(projectID) || intent.OperationID == "" || !validID(intent.OperationID) ||
		!validName(intent.Actor) || intent.ExpectedBindingRevision < 0 || !validDesignSystemMutationIntent(intent) {
		return DesignSystemMutationIntent{}, todoTeach("operation", "provide a supported Design System mutation, attributable actor, digest, exact binding revision, Git HEAD, candidate tree and prepared commit SHA", ErrInvalid)
	}
	company, err := m.company(ctx, companyID)
	if err != nil {
		return DesignSystemMutationIntent{}, err
	}
	collection := m.client.Database(company.DatabaseName).Collection(projectsCollection)
	var stored projectDocument
	if err := collection.FindOne(ctx, bson.D{{Key: "id", Value: projectID}}).Decode(&stored); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return DesignSystemMutationIntent{}, ErrNotFound
		}
		return DesignSystemMutationIntent{}, normalizeMongoError(err)
	}
	if stored.CompanyID != companyID || stored.ObjectID.IsZero() || stored.Binding == nil || !validBinding(*stored.Binding) {
		return DesignSystemMutationIntent{}, ErrUnavailable
	}
	if stored.PendingDesignSystemMutation != nil {
		current := *stored.PendingDesignSystemMutation
		if !validDesignSystemMutationIntent(current) {
			return DesignSystemMutationIntent{}, ErrUnavailable
		}
		if current.OperationID == intent.OperationID {
			if current == intent {
				return current, nil
			}
			return DesignSystemMutationIntent{}, todoTeach("operation_id", "retry the exact prepared Design System operation or use a new operation ID", ErrConflict)
		}
		return DesignSystemMutationIntent{}, todoTeach("pending_operation", "recover or resolve the existing Project Design System Git operation before starting another mutation", ErrConflict)
	}
	if stored.BindingRevision != intent.ExpectedBindingRevision {
		return DesignSystemMutationIntent{}, todoTeach("expected_binding_revision", "reload the shared Project repository binding and prepare a new candidate against its current revision", ErrConflict)
	}
	state := stored.DesignSystemState
	if state == "" {
		state = "unselected"
	}
	if intent.ExpectedSelectedSHA != stored.DesignSystemSHA || intent.ExpectedInventoryDigest != stored.DesignSystemInventoryDigest ||
		(intent.Operation == "create" && state != "unselected" && state != "absent") ||
		((intent.Operation == "replace" || intent.Operation == "delete") && (state != "selected" || intent.ExpectedSelectedSHA == "")) {
		return DesignSystemMutationIntent{}, todoTeach("expected_design_system_selection", "reload the current selected Design System SHA and inventory before preparing a mutation", ErrConflict)
	}
	filter := bson.D{
		{Key: "_id", Value: stored.ObjectID},
		{Key: "company_id", Value: companyID},
		{Key: "binding", Value: *stored.Binding},
		{Key: "pending_design_system_mutation", Value: bson.M{"$exists": false}},
	}
	if stored.BindingRevision == 0 {
		filter = append(filter, bson.E{Key: "binding_revision", Value: bson.M{"$in": []any{nil, int64(0)}}})
	} else {
		filter = append(filter, bson.E{Key: "binding_revision", Value: stored.BindingRevision})
	}
	filter = appendSelectionCAS(filter, "design_system_state", stored.DesignSystemState)
	filter = appendSelectionCAS(filter, "design_system_sha", stored.DesignSystemSHA)
	filter = appendSelectionCAS(filter, "design_system_inventory_digest", stored.DesignSystemInventoryDigest)
	result, err := collection.UpdateOne(ctx, filter, bson.M{"$set": bson.M{"pending_design_system_mutation": intent}})
	if err != nil {
		return DesignSystemMutationIntent{}, normalizeMongoError(err)
	}
	if result.MatchedCount == 1 {
		return intent, nil
	}
	var latest projectDocument
	if readErr := collection.FindOne(ctx, bson.D{{Key: "id", Value: projectID}}).Decode(&latest); readErr != nil {
		if errors.Is(readErr, mongo.ErrNoDocuments) {
			return DesignSystemMutationIntent{}, ErrNotFound
		}
		return DesignSystemMutationIntent{}, normalizeMongoError(readErr)
	}
	if latest.CompanyID != companyID || latest.ObjectID.IsZero() {
		return DesignSystemMutationIntent{}, ErrUnavailable
	}
	if latest.PendingDesignSystemMutation != nil {
		current := *latest.PendingDesignSystemMutation
		if current == intent {
			return current, nil
		}
		if current.OperationID == intent.OperationID {
			return DesignSystemMutationIntent{}, todoTeach("operation_id", "retry the exact prepared Design System operation or use a new operation ID", ErrConflict)
		}
		return DesignSystemMutationIntent{}, todoTeach("pending_operation", "recover or resolve the existing Project Design System Git operation before starting another mutation", ErrConflict)
	}
	return DesignSystemMutationIntent{}, todoTeach("expected_binding_revision", "reload the shared Project repository binding and prepare a new candidate against its current revision", ErrConflict)
}

func appendSelectionCAS(filter bson.D, field, value string) bson.D {
	if value == "" {
		return append(filter, bson.E{Key: field, Value: bson.M{"$in": []any{nil, ""}}})
	}
	return append(filter, bson.E{Key: field, Value: value})
}

// ReadDesignSystemMutation is an internal recovery read. The presence of an
// intent does not prove that its prepared commit reached the remote branch.
func (m *MongoStore) ReadDesignSystemMutation(ctx context.Context, companyID, projectID string) (*DesignSystemMutationIntent, error) {
	if !validID(companyID) || !validID(projectID) {
		return nil, ErrInvalid
	}
	company, err := m.company(ctx, companyID)
	if err != nil {
		return nil, err
	}
	var stored projectDocument
	if err := m.client.Database(company.DatabaseName).Collection(projectsCollection).FindOne(ctx, bson.D{{Key: "id", Value: projectID}}).Decode(&stored); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, ErrNotFound
		}
		return nil, normalizeMongoError(err)
	}
	if stored.CompanyID != companyID || stored.ObjectID.IsZero() {
		return nil, ErrUnavailable
	}
	if stored.PendingDesignSystemMutation == nil {
		return nil, nil
	}
	if !validDesignSystemMutationIntent(*stored.PendingDesignSystemMutation) {
		return nil, ErrUnavailable
	}
	intent := *stored.PendingDesignSystemMutation
	return &intent, nil
}

func validDesignSystemMutationIntent(intent DesignSystemMutationIntent) bool {
	if intent.Operation != "create" && intent.Operation != "replace" && intent.Operation != "delete" {
		return false
	}
	return intent.OperationID != "" && validID(intent.OperationID) && validName(intent.Actor) &&
		validSourceDigest(intent.PayloadDigest) && intent.PayloadDigest == intent.InventoryDigest && intent.ExpectedBindingRevision >= 0 &&
		(intent.ExpectedSelectedSHA == "" || validFullGitSHA(intent.ExpectedSelectedSHA)) &&
		(intent.ExpectedInventoryDigest == "" || validSourceDigest(intent.ExpectedInventoryDigest)) &&
		validFullGitSHA(intent.ExpectedHead) && validFullGitSHA(intent.CandidateTreeSHA) &&
		validFullGitSHA(intent.PreparedCommitSHA) && validSourceDigest(intent.InventoryDigest)
}

func validFullGitSHA(value string) bool {
	if len(value) != 40 || strings.ToLower(value) != value {
		return false
	}
	return validGitCommit(value)
}
