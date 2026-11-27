package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type ProjectPromotionMode struct {
	CompanyID     string                        `json:"company_id"`
	ProjectID     string                        `json:"project_id"`
	EffectiveMode string                        `json:"effective_mode"`
	Source        string                        `json:"source"`
	Revision      int64                         `json:"revision"`
	Decision      *ProjectPromotionModeDecision `json:"decision,omitempty"`
	NoOp          bool                          `json:"no_op,omitempty"`
}

type ProjectPromotionModeDecision struct {
	Actor              string    `json:"actor" bson:"actor"`
	OwnerReference     string    `json:"owner_reference" bson:"owner_reference"`
	OperationID        string    `json:"operation_id" bson:"operation_id"`
	ConfirmedAt        time.Time `json:"confirmed_at" bson:"confirmed_at"`
	PermissionDecision string    `json:"permission_decision" bson:"permission_decision"`
}

type projectPromotionModeConfiguration struct {
	Mode     string                       `bson:"mode"`
	Revision int64                        `bson:"revision"`
	Digest   string                       `bson:"request_digest"`
	Decision ProjectPromotionModeDecision `bson:"decision"`
}

type ProjectPromotionModeChange struct {
	ProjectID        string `json:"project_id"`
	Mode             string `json:"mode"`
	ExpectedRevision *int64 `json:"expected_revision"`
	OperationID      string `json:"operation_id"`
	Actor            string `json:"actor"`
	OwnerReference   string `json:"owner_reference"`
}

type projectPromotionModeService interface {
	ReadProjectPromotionMode(context.Context, string, string) (ProjectPromotionMode, error)
	ChangeProjectPromotionMode(context.Context, string, string, ProjectPromotionModeChange) (ProjectPromotionMode, error)
}

func validProjectPromotionMode(mode string) bool {
	return mode == "mvp" || mode == "complete"
}

func readPromotionProject(ctx context.Context, database *mongo.Database, companyID, projectID string) (projectDocument, error) {
	var project projectDocument
	err := database.Collection(projectsCollection).FindOne(ctx, bson.M{"id": projectID, "company_id": companyID}).Decode(&project)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return projectDocument{}, ErrNotFound
	}
	if err != nil {
		return projectDocument{}, normalizeMongoTransactionError(err)
	}
	if project.ObjectID.IsZero() || project.Binding != nil && !validBinding(*project.Binding) {
		return projectDocument{}, ErrUnavailable
	}
	return project, nil
}

func projectPromotionModeState(project projectDocument) ProjectPromotionMode {
	state := ProjectPromotionMode{CompanyID: project.CompanyID, ProjectID: project.ID, Source: "unconfigured"}
	if configuration := project.PromotionMode; configuration != nil {
		state.EffectiveMode = configuration.Mode
		state.Source = "project"
		state.Revision = configuration.Revision
		state.Decision = &configuration.Decision
	}
	return state
}

func resolveProjectPromotionMode(ctx context.Context, database *mongo.Database, project projectDocument) (ProjectPromotionMode, error) {
	state := projectPromotionModeState(project)
	if project.PromotionMode != nil {
		if !validProjectPromotionMode(state.EffectiveMode) || state.Revision < 1 {
			return ProjectPromotionMode{}, todoTeach("project.promotion_mode", "resolve a valid explicit Project promotion mode and revision", ErrConflict)
		}
		return state, nil
	}
	if project.KnowledgeGenerationID == "" {
		return state, nil
	}

	// Only the constitution selected by the active generation can supply fallback.
	// Bound the query to the two records needed to diagnose ambiguity.
	filter := bson.M{"project_id": project.ID, "generation_id": project.KnowledgeGenerationID, "deleted": bson.M{"$ne": true}}
	cursor, err := database.Collection(canonicalConstitutions).Find(ctx, filter, options.Find().SetLimit(2))
	if err != nil {
		return ProjectPromotionMode{}, normalizeMongoTransactionError(err)
	}
	var records []canonicalStored
	if err := cursor.All(ctx, &records); err != nil {
		return ProjectPromotionMode{}, normalizeMongoTransactionError(err)
	}
	if len(records) == 0 {
		return state, nil
	}
	if len(records) != 1 {
		return ProjectPromotionMode{}, todoTeach("constitution", "resolve the active constitution to exactly one canonical record", ErrConflict)
	}
	record := records[0]
	if record.Family != FamilyConstitution || !validID(record.ID) || record.Revision < 1 || !validSourceDigest(record.ContentDigest) {
		return ProjectPromotionMode{}, todoTeach("constitution", "resolve the active Project constitution with its immutable revision and digest", ErrConflict)
	}
	value, present := record.Payload["promotion_mode"]
	if !present {
		return state, nil
	}
	mode, valid := value.(string)
	if !valid || !validProjectPromotionMode(mode) {
		return ProjectPromotionMode{}, todoTeach("constitution.promotion_mode", "resolve a valid active mvp or complete mode", ErrConflict)
	}
	state.EffectiveMode, state.Source = mode, "constitution"
	return state, nil
}

func (m *MongoStore) ReadProjectPromotionMode(ctx context.Context, companyID, projectID string) (ProjectPromotionMode, error) {
	if !validID(companyID) || !validID(projectID) {
		return ProjectPromotionMode{}, ErrInvalid
	}
	database, err := m.todoTenantDatabase(ctx, companyID)
	if err != nil {
		return ProjectPromotionMode{}, err
	}
	project, err := readPromotionProject(ctx, database, companyID, projectID)
	if err != nil {
		return ProjectPromotionMode{}, err
	}
	return resolveProjectPromotionMode(ctx, database, project)
}

func (m *MongoStore) ChangeProjectPromotionMode(ctx context.Context, companyID, projectID string, request ProjectPromotionModeChange) (ProjectPromotionMode, error) {
	switch {
	case !validID(companyID) || !validID(projectID) || request.ProjectID != projectID:
		return ProjectPromotionMode{}, todoTeach("project_id", "match the exact Company and Project path identity", ErrInvalid)
	case !validProjectPromotionMode(request.Mode):
		return ProjectPromotionMode{}, todoTeach("mode", "select mvp or complete", ErrInvalid)
	case request.ExpectedRevision == nil || *request.ExpectedRevision < 0 || *request.ExpectedRevision == math.MaxInt64:
		return ProjectPromotionMode{}, todoTeach("expected_revision", "use the current explicit mode revision returned by GET promotion-mode", ErrInvalid)
	case !validID(request.OperationID):
		return ProjectPromotionMode{}, todoTeach("operation_id", "provide a stable operation ID", ErrInvalid)
	case !validSummaryField(request.Actor, 256):
		return ProjectPromotionMode{}, todoTeach("actor", "provide the attributable operator", ErrInvalid)
	case !validSummaryField(request.OwnerReference, 512):
		return ProjectPromotionMode{}, todoTeach("owner_reference", "provide the explicit Project owner decision reference", ErrInvalid)
	}

	database, err := m.todoTenantDatabase(ctx, companyID)
	if err != nil {
		return ProjectPromotionMode{}, err
	}
	document, err := readPromotionProject(ctx, database, companyID, projectID)
	if err != nil {
		return ProjectPromotionMode{}, err
	}
	permission := authorizeTODOAdministrativeOperation(ctx, companyID, projectID, "set-promotion-mode")
	if !permission.Allowed {
		return ProjectPromotionMode{}, ErrConflict
	}
	requestJSON, err := json.Marshal(request)
	if err != nil {
		return ProjectPromotionMode{}, ErrInvalid
	}
	digest := sourceHash(requestJSON)
	current := projectPromotionModeState(document)
	if current.Decision != nil && current.Decision.OperationID == request.OperationID {
		return replayProjectPromotionMode(current, document.PromotionMode, digest)
	}
	if current.Revision != *request.ExpectedRevision {
		return ProjectPromotionMode{}, projectPromotionModeRevisionConflict(current.Revision, request.OperationID)
	}
	if current.EffectiveMode == request.Mode {
		current.NoOp = true
		return current, nil
	}

	update := projectPromotionModeConfiguration{
		Mode: request.Mode, Revision: current.Revision + 1, Digest: digest,
		Decision: ProjectPromotionModeDecision{
			Actor: request.Actor, OwnerReference: request.OwnerReference, OperationID: request.OperationID,
			ConfirmedAt: canonicalTime(), PermissionDecision: permission.Decision,
		},
	}
	filter := bson.M{"_id": document.ObjectID, "id": projectID, "company_id": companyID}
	if document.PromotionMode == nil {
		filter["promotion_mode"] = nil
	} else {
		filter["promotion_mode.revision"] = current.Revision
	}
	result, err := database.Collection(projectsCollection).UpdateOne(ctx, filter, bson.M{"$set": bson.M{"promotion_mode": update}})
	if err != nil {
		return ProjectPromotionMode{}, normalizeMongoError(err)
	}
	if result.MatchedCount != 1 {
		latest, err := readPromotionProject(ctx, database, companyID, projectID)
		if err != nil {
			return ProjectPromotionMode{}, err
		}
		state := projectPromotionModeState(latest)
		if state.Decision != nil && state.Decision.OperationID == request.OperationID {
			return replayProjectPromotionMode(state, latest.PromotionMode, digest)
		}
		return ProjectPromotionMode{}, projectPromotionModeRevisionConflict(state.Revision, request.OperationID)
	}
	document.PromotionMode = &update
	return projectPromotionModeState(document), nil
}

func replayProjectPromotionMode(state ProjectPromotionMode, configuration *projectPromotionModeConfiguration, digest string) (ProjectPromotionMode, error) {
	if configuration.Digest != digest {
		return ProjectPromotionMode{}, todoTeach("operation_id", "use the retained operation ID only for the identical mode change request", ErrConflict)
	}
	state.NoOp = true
	return state, nil
}

func projectPromotionModeRevisionConflict(revision int64, operationID string) error {
	next := "Read the current explicit mode revision with builder-project read-promotion-mode and reassess the owner decision before submitting a new change."
	return withTeachDiagnostic(todoTeach("expected_revision", next, ErrConflict), TeachDiagnostic{
		Code: "promotion_mode_revision_conflict", FieldPath: "expected_revision", NextAction: next,
		Revision: &revision, OperationID: operationID, Effects: TeachEffectsNone, Retry: TeachRetryReadReassess,
	})
}

func requireProjectPromotionMode(state ProjectPromotionMode, operationID string, todoRevision int64) (string, error) {
	if state.EffectiveMode != "" {
		return state.EffectiveMode, nil
	}
	identity := fmt.Sprintf("--company-id %s --project-id %s", state.CompanyID, state.ProjectID)
	next := fmt.Sprintf("Run builder-project read-promotion-mode %[1]s, then builder-project set-promotion-mode %[1]s --mode <mvp|complete> --expected-revision <revision> --operation-id <mode-operation-id> --actor <operator> --owner-reference <owner-decision>. Retry the original TODO transition with its same revision and operation ID after the mode is set.", identity)
	return "", withTeachDiagnostic(todoTeach("project.promotion_mode", next, ErrConflict), TeachDiagnostic{
		Code: "promotion_mode_required", FieldPath: "project.promotion_mode", AcceptedValues: []string{"mvp", "complete"},
		NextAction: next, OperationID: operationID, Revision: &todoRevision, Effects: TeachEffectsNone, Retry: TeachRetrySameOperation,
	})
}

type todoPromotionPolicyCheck struct {
	mode            string
	bindingRevision *int64
	repositoryIDs   map[string]string
}

// Promotion, mode configuration, canonical Knowledge writes, and activation all
// write the Project document. A concurrent policy write either precedes the
// transaction snapshot or forces Mongo to retry it against the new policy.
func checkTODOProjectPromotionPolicy(ctx context.Context, database *mongo.Database, companyID, projectID string, check todoPromotionPolicyCheck) (bson.ObjectID, error) {
	project, err := readPromotionProject(ctx, database, companyID, projectID)
	if err != nil {
		return bson.NilObjectID, err
	}
	state, err := resolveProjectPromotionMode(ctx, database, project)
	if err != nil {
		return bson.NilObjectID, err
	}
	if state.EffectiveMode != check.mode {
		return bson.NilObjectID, todoTeach("project.promotion_mode", "promotion mode changed during proof verification; read the current mode and reassess the transition", ErrConflict)
	}
	if check.bindingRevision != nil && project.BindingRevision != *check.bindingRevision {
		return bson.NilObjectID, todoTeach("project.binding", "the binding changed during production proof verification; read the current binding and retry", ErrConflict)
	}
	if len(check.repositoryIDs) != 0 {
		registered, err := readImplementationRepositoryNodeIDs(ctx, database, projectID)
		if err != nil {
			return bson.NilObjectID, err
		}
		for alias, nodeID := range check.repositoryIDs {
			if registered[alias] != "" && registered[alias] != nodeID {
				return bson.NilObjectID, todoTeach("local_history.proof.repository_identity", "match the immutable Project Git identity for the original production commits", ErrConflict)
			}
		}
	}
	return project.ObjectID, nil
}

func advanceProjectPromotionPolicyFence(ctx context.Context, database *mongo.Database, companyID, projectID string, projectObjectID bson.ObjectID) error {
	filter := bson.M{"_id": projectObjectID, "company_id": companyID, "id": projectID}
	result, err := database.Collection(projectsCollection).UpdateOne(ctx, filter, bson.M{"$inc": bson.M{"promotion_policy_fence": 1}})
	if err != nil {
		return normalizeMongoTransactionError(err)
	}
	if result.MatchedCount != 1 {
		return ErrConflict
	}
	return nil
}

func (h *Handler) readProjectPromotionMode(w http.ResponseWriter, r *http.Request) {
	service, ok := h.service.(projectPromotionModeService)
	if !ok {
		h.writeError(w, ErrUnavailable)
		return
	}
	if r.URL.RawQuery != "" || r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
		h.writeError(w, todoTeach("request", "omit query parameters and the request body", ErrInvalid))
		return
	}
	result, err := service.ReadProjectPromotionMode(r.Context(), r.PathValue("companyID"), r.PathValue("projectID"))
	h.respond(w, result, err)
}

func (h *Handler) changeProjectPromotionMode(w http.ResponseWriter, r *http.Request) {
	service, ok := h.service.(projectPromotionModeService)
	if !ok {
		h.writeError(w, ErrUnavailable)
		return
	}
	var request ProjectPromotionModeChange
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10))
	decoder.DisallowUnknownFields()
	if r.URL.RawQuery != "" || decoder.Decode(&request) != nil {
		h.writeError(w, todoTeach("request", "provide one promotion-mode change object within 8 KiB with no query parameters", ErrInvalid))
		return
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		h.writeError(w, todoTeach("request", "contain exactly one JSON object", ErrInvalid))
		return
	}
	result, err := service.ChangeProjectPromotionMode(r.Context(), r.PathValue("companyID"), r.PathValue("projectID"), request)
	h.respondMutation(w, result, err, request.OperationID)
}

func (m *MongoStore) transitionCodePromotion(ctx context.Context, collection *mongo.Collection, companyID string, project Project, record TODORecord, request TODOActionRequest, requestDigest string) (TODOActionResponse, error) {
	if record.StageVerification != "verified" || record.VerifiedTransitionRef == nil || containsTODOString(record.Qualifiers, "Blocked") {
		return TODOActionResponse{}, todoTeach("local_proof", "require an unblocked verified code delivery stage", ErrConflict)
	}
	database := collection.Database()
	currentEvent, err := readCurrentTODOPromotionEvent(ctx, database, record)
	if err != nil {
		return TODOActionResponse{}, err
	}
	localEvent := currentEvent
	if record.Stage == "Promotion-Lane" {
		localEvent, err = readVerifiedTODOStageEvent(ctx, database, record, "Local-Implemented", currentEvent.Revision-1)
		if err != nil {
			return TODOActionResponse{}, err
		}
	}
	localProof := localEvent.Changes.VerifiedTransitionRef
	if !verifiedCodeDeliveryIdentity(localProof) || !validProofReference(*localProof) {
		return TODOActionResponse{}, todoTeach("local_history.proof", "resolve the immutable verified Local-Implemented code delivery", ErrConflict)
	}
	historical := record
	historical.Revision = localEvent.Revision
	contentRevision, contentDigest, _, err := requireCurrentTypedTODOStageAuthority(ctx, database, historical)
	if err != nil {
		return TODOActionResponse{}, err
	}

	state, err := m.ReadProjectPromotionMode(ctx, companyID, record.ProjectID)
	if err != nil {
		return TODOActionResponse{}, err
	}
	mode, err := requireProjectPromotionMode(state, request.OperationID, record.Revision)
	if err != nil {
		return TODOActionResponse{}, err
	}
	policy := todoPromotionPolicyCheck{mode: mode}
	localIDs, err := localProofRepositoryIdentities(*localProof)
	if err != nil {
		return TODOActionResponse{}, err
	}
	var proof TODOProofReference
	var identityPins []implementationRepositoryIdentity
	if record.Stage == "Local-Implemented" && request.RequestedStage == "Completed" {
		if mode != "mvp" {
			return TODOActionResponse{}, todoTeach("requested_stage", "verify Local-Implemented to Promotion-Lane and then Promotion-Lane to Completed against original commits on main", ErrConflict)
		}
		if err := validateTODOAuditDeliveryReferences(record, contentDigest); err != nil {
			return TODOActionResponse{}, err
		}
		var dockerRepository string
		if project.Binding != nil {
			dockerRepository = project.Binding.DockerRepositoryID
		}
		if err := verifyMVPLocalDelivery(record, contentDigest, localIDs, dockerRepository); err != nil {
			return TODOActionResponse{}, err
		}
		policy.bindingRevision = &project.BindingRevision
		policy.repositoryIDs = localIDs
		proof, err = m.verifyMVPProduction(ctx, companyID, record, request, dockerRepository)
	} else {
		// PACED can send empty or null proof arrays on Complete code transitions.
		if request.LocalProof != nil || request.localProofPresent || len(request.ProofReferences) != 0 || len(request.WaiverReferences) != 0 {
			return TODOActionResponse{}, todoTeach("proof_refs/waiver_refs/local_proof", "leave proof and waiver lists empty and omit inline local proof because the Registry verifies Git arrival", ErrConflict)
		}
		if mode != "complete" {
			if record.Stage == "Promotion-Lane" {
				return TODOActionResponse{}, todoTeach("project.promotion_mode", "restore the Complete mode preserved by the verified Promotion-Lane proof before completing on main", ErrConflict)
			}
			return TODOActionResponse{}, todoTeach("project.promotion_mode", "use Complete mode for Promotion-Lane or request Completed with verified MVP production proof", ErrConflict)
		}
		proof, identityPins, err = m.verifyCompletePromotionArrival(ctx, companyID, record, localEvent, localIDs, contentRevision, contentDigest, request.PromotionTargets)
	}
	if err != nil {
		return TODOActionResponse{}, err
	}
	return writeVerifiedTODOWithKnowledgeAndIdentityPins(ctx, database, collection, companyID, record, request.RequestedStage, proof, request.OperationID, "transition", requestDigest, nil, nil, identityPins, &policy)
}
