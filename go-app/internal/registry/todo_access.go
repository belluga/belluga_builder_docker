package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

var todoOperationIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

func validTODOOperationKind(value string) bool {
	return value == "create-todo" || value == "transition" || value == "verify-current-stage" || value == "content-edit" || value == "evidence-edit" || value == "placement-edit" || value == "next-step-edit" || value == "historical-adoption" || value == "content-migration" || value == "grant-todo-review-waiver" || value == "remove-todo"
}

type TODOReadResponse struct {
	CompanyID               string                  `json:"company_id"`
	ProjectID               string                  `json:"project_id"`
	TODO                    TODORecord              `json:"todo"`
	ReleaseDeliveryBranches []releaseDeliveryBranch `json:"release_delivery_branches"`
	Removal                 *TODORemovalDetails     `json:"removal,omitempty"`
	TODORevision            int64                   `json:"todo_revision"`
	ContentRevision         *int64                  `json:"content_revision,omitempty"`
	ContentDigest           string                  `json:"content_digest,omitempty"`
	AuditFloors             map[string]string       `json:"audit_floors,omitempty"`
}

type TODOActionRequest struct {
	ProjectID                string                        `json:"project_id"`
	TODOID                   string                        `json:"todo_id"`
	ExpectedRevision         *int64                        `json:"expected_revision"`
	OperationID              string                        `json:"operation_id"`
	RequestedStage           string                        `json:"requested_stage"`
	ProofReferences          []TODOProofReference          `json:"proof_refs"`
	WaiverReferences         []TODOProofReference          `json:"waiver_refs"`
	LocalProof               *LocalProofResult             `json:"local_proof,omitempty"`
	KnowledgeDelivery        *TODOKnowledgeDeliveryClaim   `json:"knowledge_delivery,omitempty"`
	KnowledgeFinalAcceptance *TODOKnowledgeFinalAcceptance `json:"knowledge_final_acceptance,omitempty"`
}

type VerifyCurrentTODOStageRequest struct {
	ProjectID        string               `json:"project_id"`
	TODOID           string               `json:"todo_id"`
	ExpectedRevision *int64               `json:"expected_revision"`
	OperationID      string               `json:"operation_id"`
	ProofReferences  []TODOProofReference `json:"proof_refs"`
}

type TODOActionResponse struct {
	CompanyID         string `json:"company_id"`
	ProjectID         string `json:"project_id"`
	TODOID            string `json:"todo_id"`
	TODORevision      int64  `json:"todo_revision"`
	Stage             string `json:"stage"`
	StageVerification string `json:"stage_verification"`
	NoOp              bool   `json:"no_op"`
}

type todoRevisionConflict struct{ Revision int64 }

func (e *todoRevisionConflict) Error() string {
	return fmt.Sprintf("TEACH: TODO revision is now %d; reload the Project Roadmap and retry with expected_revision=%d", e.Revision, e.Revision)
}
func (e *todoRevisionConflict) Unwrap() error { return ErrConflict }

func (m *MongoStore) ReadTODO(ctx context.Context, companyID, projectID, todoID string, expectedRevision *int64) (TODOReadResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	project, collection, err := m.selectedTODOCollection(ctx, companyID, projectID, todoID)
	if err != nil {
		return TODOReadResponse{}, err
	}
	record, err := readSelectedTODO(ctx, collection, projectID, todoID)
	if err != nil {
		return TODOReadResponse{}, err
	}
	if err := m.exactTODORelease(ctx, companyID, project, record); err != nil {
		return TODOReadResponse{}, err
	}
	if expectedRevision != nil && *expectedRevision != record.Revision {
		return TODOReadResponse{}, &todoRevisionConflict{Revision: record.Revision}
	}
	response := TODOReadResponse{CompanyID: companyID, ProjectID: projectID, TODO: record, TODORevision: record.Revision}
	if record.Removed {
		var event TODOHistoryEvent
		if err := collection.Database().Collection(todoEventsCollection).FindOne(ctx, bson.D{{Key: "project_id", Value: projectID}, {Key: "todo_id", Value: todoID}, {Key: "kind", Value: "todo-removed"}}).Decode(&event); err != nil || event.ActorClaim == nil || event.Changes.Removed == nil || !*event.Changes.Removed {
			return TODOReadResponse{}, todoTeach("removal", "resolve to the immutable removal event for this stable TODO ID", ErrConflict)
		}
		response.Removal = &TODORemovalDetails{Reason: event.Reason, OccurredAt: event.OccurredAt, ActorClaim: *event.ActorClaim, PermissionDecision: event.PermissionDecision}
	}
	company, err := m.company(ctx, companyID)
	if err != nil {
		return TODOReadResponse{}, err
	}
	database := m.client.Database(company.DatabaseName)
	var latest TODOContentVersion
	if err := database.Collection(todoContentVersionsCollection).FindOne(ctx, bson.D{{Key: "project_id", Value: projectID}, {Key: "todo_id", Value: todoID}, {Key: "revision", Value: bson.D{{Key: "$lte", Value: record.Revision}}}}, options.FindOne().SetSort(bson.D{{Key: "revision", Value: -1}})).Decode(&latest); err == nil {
		expected, digestErr := todoContentRevisionDigest(projectID, todoID, latest.Revision, latest.Content)
		if validSourceDigest(latest.Digest) && (digestErr != nil || expected != latest.Digest) {
			return TODOReadResponse{}, todoTeach("content_digest", "match the canonical immutable Project/TODO/content-revision hash", ErrConflict)
		}
		if validSourceDigest(latest.Digest) {
			response.ContentRevision, response.ContentDigest = &latest.Revision, latest.Digest
		}
		if latest.Content.SchemaVersion == todoContentSchemaVersion {
			if !validSourceDigest(latest.Digest) {
				return TODOReadResponse{}, todoTeach("content_digest", "exist and match the canonical immutable Project/TODO/content-revision hash", ErrConflict)
			}
			response.TODO.Content = &latest.Content.TODOContent
		}
	} else if !errors.Is(err, mongo.ErrNoDocuments) {
		return TODOReadResponse{}, ErrUnavailable
	}
	if usesTypedMongoAuthority(response.TODO) || usesImportedTODOContentStageProof(record) {
		record.Content = response.TODO.Content
		contentRevision, contentDigest, _, hasCurrentContent, err := currentTODOContentSubject(ctx, database, record)
		if err != nil {
			return TODOReadResponse{}, err
		}
		// Historical Foundation imports can have a revision without a stored
		// digest. Keep them readable, but never expose an incomplete content
		// identity that consumers could mistake for current proof.
		if hasCurrentContent && validSourceDigest(contentDigest) {
			response.ContentRevision, response.ContentDigest = &contentRevision, contentDigest
			var version TODOContentVersion
			if err := database.Collection(todoContentVersionsCollection).FindOne(ctx, bson.D{{Key: "project_id", Value: projectID}, {Key: "todo_id", Value: todoID}, {Key: "revision", Value: contentRevision}}).Decode(&version); err != nil {
				return TODOReadResponse{}, ErrUnavailable
			}
			if version.Content.SchemaVersion == todoContentSchemaVersion {
				response.TODO.Content = &version.Content.TODOContent
			}
		}
	}
	if response.TODO.Content != nil && response.TODO.Content.SchemaVersion == todoContentSchemaVersion {
		projected := *response.TODO.Content
		projected.LegacyContext = nil
		response.TODO.Content = &projected
		if projected.Planning != nil {
			response.AuditFloors, _ = deriveTODOAuditFloors(projected.Planning)
		}
		response.TODO.Sections = recordRecordSections(response.TODO)
	}
	response.ReleaseDeliveryBranches, err = selectedTODOReleaseBranches(ctx, database, response.TODO)
	if err != nil {
		return TODOReadResponse{}, err
	}
	if response.TODO.AdministrativeAcceptance != nil && response.TODO.Disposition == nil && (!validSourceDigest(response.ContentDigest) || response.TODO.AdministrativeAcceptance.ContentDigest != response.ContentDigest) {
		return TODOReadResponse{}, todoTeach("administrative_acceptance.content_digest", "match the current immutable typed content revision before treating the accepted stage as effective", ErrConflict)
	}
	return response, nil
}

func (m *MongoStore) TransitionTODO(ctx context.Context, companyID string, request TODOActionRequest) (TODOActionResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	if err := validateTODOAction(request); err != nil {
		return TODOActionResponse{}, err
	}
	project, collection, err := m.selectedTODOCollection(ctx, companyID, request.ProjectID, request.TODOID)
	if err != nil {
		return TODOActionResponse{}, err
	}
	record, err := readSelectedTODO(ctx, collection, request.ProjectID, request.TODOID)
	if err != nil {
		return TODOActionResponse{}, err
	}
	digest, err := todoRequestDigest(request)
	if err != nil {
		return TODOActionResponse{}, ErrInvalid
	}
	if retried, ok, err := exactTODOOperationRetry(companyID, record, request.OperationID, "transition", digest, *request.ExpectedRevision); ok || err != nil {
		return retried, err
	}
	if retried, ok, err := m.exactTODOHistoryRetry(ctx, companyID, record, request.OperationID, digest); ok || err != nil {
		return retried, err
	}
	if record.Revision != *request.ExpectedRevision {
		return TODOActionResponse{}, &todoRevisionConflict{Revision: record.Revision}
	}
	if err := requireActiveTODO(record); err != nil {
		return TODOActionResponse{}, err
	}
	if record.Disposition != nil {
		return TODOActionResponse{}, todoTeach("disposition", "use a separately governed future operation before attempting delivery-stage transitions on a disposed TODO", ErrConflict)
	}
	company, err := m.company(ctx, companyID)
	if err != nil {
		return TODOActionResponse{}, err
	}
	if _, _, _, err := requireCurrentTypedTODOStageAuthority(ctx, m.client.Database(company.DatabaseName), record); err != nil {
		return TODOActionResponse{}, err
	}
	if err := m.exactTODORelease(ctx, companyID, project, record); err != nil {
		return TODOActionResponse{}, err
	}
	content, contentErr := m.contentAtRevision(ctx, m.client.Database(company.DatabaseName), record.ProjectID, record.ID, record.Revision)
	if contentErr == nil && content.SchemaVersion == todoContentSchemaVersion && content.Delivery != nil && content.Delivery.Kind == "knowledge-only" {
		record.Content = &content.TODOContent
		return m.transitionKnowledgeTODO(ctx, companyID, project, collection, record, request, digest)
	}
	if contentErr != nil && !errors.Is(contentErr, ErrNotFound) {
		return TODOActionResponse{}, contentErr
	}
	if request.KnowledgeDelivery != nil || request.KnowledgeFinalAcceptance != nil {
		return TODOActionResponse{}, todoTeach("knowledge_delivery/knowledge_final_acceptance", "use Knowledge proof fields only for a TODO explicitly contracted as knowledge-only", ErrConflict)
	}
	if record.Stage == "Local-Implemented" && request.RequestedStage == "Completed" && record.StageVerification == "verified" && record.VerifiedTransitionRef != nil && !containsTODOString(record.Qualifiers, "Blocked") {
		{
			company, companyErr := m.company(ctx, companyID)
			if companyErr != nil {
				return TODOActionResponse{}, companyErr
			}
			content, contentErr := m.contentAtRevision(ctx, m.client.Database(company.DatabaseName), record.ProjectID, record.ID, record.Revision)
			if contentErr != nil && !errors.Is(contentErr, ErrNotFound) {
				return TODOActionResponse{}, contentErr
			}
			if content.SchemaVersion == todoContentSchemaVersion {
				record.Content = &content.TODOContent
				_, contentDigest, _, _, subjectErr := currentTODOContentSubject(ctx, m.client.Database(company.DatabaseName), record)
				if subjectErr != nil {
					return TODOActionResponse{}, subjectErr
				}
				if err := validateTODOAuditDeliveryReferences(record, contentDigest); err != nil {
					return TODOActionResponse{}, err
				}
			}
		}
		var projectDockerRepository string
		if project.Binding != nil {
			projectDockerRepository = project.Binding.DockerRepositoryID
		}
		proof, err := m.verifyMVPProduction(ctx, companyID, record, request, projectDockerRepository)
		if err != nil {
			return TODOActionResponse{}, err
		}
		company, err := m.company(ctx, companyID)
		if err != nil {
			return TODOActionResponse{}, err
		}
		return writeVerifiedTODO(ctx, m.client.Database(company.DatabaseName), collection, companyID, record, "Completed", proof, request.OperationID, "transition", digest)
	}
	var proof TODOProofReference
	var verifiedRequirementRefs []TODOProofReference
	typedAuthority := record.Source.Kind == "mongo-content-revision"
	if !typedAuthority {
		company, companyErr := m.company(ctx, companyID)
		if companyErr != nil {
			return TODOActionResponse{}, companyErr
		}
		content, contentErr := m.contentAtRevision(ctx, m.client.Database(company.DatabaseName), record.ProjectID, record.ID, record.Revision)
		if contentErr != nil && !errors.Is(contentErr, ErrNotFound) {
			return TODOActionResponse{}, contentErr
		}
		typedAuthority = content.SchemaVersion == todoContentSchemaVersion
		if typedAuthority {
			record.Content = &content.TODOContent
		}
	}
	if typedAuthority {
		if record.Content == nil || record.Content.SchemaVersion != todoContentSchemaVersion {
			company, companyErr := m.company(ctx, companyID)
			if companyErr != nil {
				return TODOActionResponse{}, companyErr
			}
			content, contentErr := m.contentAtRevision(ctx, m.client.Database(company.DatabaseName), record.ProjectID, record.ID, record.Revision)
			if contentErr != nil {
				return TODOActionResponse{}, contentErr
			}
			record.Content = &content.TODOContent
		}
		// The current typed Mongo content owns new edges even when provenance is
		// an imported Foundation record. Keep the stored provenance unchanged.
		current := record
		current.Source.Kind = "mongo-content-revision"
		proof, err = m.mongoBornTransitionProof(ctx, companyID, project, current, request)
		if err != nil {
			return TODOActionResponse{}, err
		}
		if record.Stage == "In-Progress" && request.RequestedStage == "Local-Implemented" && requiresLocalProofForTODO(record) {
			company, companyErr := m.company(ctx, companyID)
			if companyErr != nil {
				return TODOActionResponse{}, companyErr
			}
			_, contentDigest, _, available, subjectErr := currentTODOContentSubject(ctx, m.client.Database(company.DatabaseName), record)
			if subjectErr != nil {
				return TODOActionResponse{}, subjectErr
			}
			if !available {
				return TODOActionResponse{}, todoTeach("content_revision", "resolve the current immutable Mongo content before verifying delivery evidence", ErrConflict)
			}
			verifiedRequirementRefs, err = verifyLocalRequirementReferences(record.Content, record.ProofReferences, request.LocalProof, contentDigest)
			if err != nil {
				return TODOActionResponse{}, err
			}
		}
	} else {
		return TODOActionResponse{}, todoTeach("source/content_revision", "this transition requires the exact current typed Mongo v2 content revision; Foundation-imported and v1 stage proofs are not authority", ErrConflict)
	}
	company, err = m.company(ctx, companyID)
	if err != nil {
		return TODOActionResponse{}, err
	}
	if verifiedRequirementRefs != nil {
		return writeVerifiedTODO(ctx, m.client.Database(company.DatabaseName), collection, companyID, record, request.RequestedStage, proof, request.OperationID, "transition", digest, verifiedRequirementRefs)
	}
	return writeVerifiedTODO(ctx, m.client.Database(company.DatabaseName), collection, companyID, record, request.RequestedStage, proof, request.OperationID, "transition", digest)
}

func requiresLocalProofForTODO(record TODORecord) bool {
	foundationOnly := len(record.DiffExpectation) > 0
	for _, scope := range record.DiffExpectation {
		if !foundationScope(scope.Repository) {
			foundationOnly = false
			break
		}
	}
	return !foundationOnly
}

func (m *MongoStore) mongoBornTransitionProof(ctx context.Context, companyID string, project Project, record TODORecord, request TODOActionRequest) (TODOProofReference, error) {
	if record.ReleaseID == "" || record.Position == nil {
		return TODOProofReference{}, todoTeach("release_id/position", "attach this Backlog TODO to a same-Project Release before leaving Planning", ErrConflict)
	}
	company, err := m.company(ctx, companyID)
	if err != nil {
		return TODOProofReference{}, err
	}
	contentRevision, contentDigest, contentRef, err := currentMongoContentSubject(ctx, m.client.Database(company.DatabaseName), record)
	if err != nil {
		return TODOProofReference{}, err
	}
	content, err := m.contentAtRevision(ctx, m.client.Database(company.DatabaseName), record.ProjectID, record.ID, record.Revision)
	if err != nil {
		return TODOProofReference{}, err
	}
	if content.SchemaVersion == todoContentSchemaVersion {
		record.Content = &content.TODOContent
		for _, scope := range record.DiffExpectation {
			if foundationScope(scope.Repository) {
				return TODOProofReference{}, todoTeach("diff_expectation.repository", "omit Foundation repositories from the current v2 TODO contract", ErrConflict)
			}
		}
		for _, claim := range record.ImplementationCommits {
			if foundationScope(claim.Repository) {
				return TODOProofReference{}, todoTeach("implementation_commits.repository", "omit Foundation commits from current v2 TODO evidence", ErrConflict)
			}
		}
	}
	if record.Stage == "Planning" && request.RequestedStage == "Implementation-Approval-Pending" {
		if content.SchemaVersion == todoContentSchemaVersion {
			if err := m.requireFreshPlanningAfterPlacement(ctx, m.client.Database(company.DatabaseName), record, contentRevision, content); err != nil {
				return TODOProofReference{}, err
			}
			if err := validateMongoPlanningContract(record, content); err != nil {
				return TODOProofReference{}, err
			}
			if err := requireTODOImplementationRepositories(ctx, m, companyID, record); err != nil {
				return TODOProofReference{}, err
			}
			if err := m.verifyPlanningCodeAnchors(ctx, companyID, record.ProjectID, content); err != nil {
				return TODOProofReference{}, err
			}
			if err := requirePlanningReviewSubjects(ctx, m.client.Database(company.DatabaseName), record, contentRevision, contentDigest, content.Planning); err != nil {
				return TODOProofReference{}, err
			}
		}
	}
	if record.Stage == "In-Progress" && request.RequestedStage == "Local-Implemented" {
		if err := requireApprovedCurrentContent(record, contentDigest); err != nil {
			return TODOProofReference{}, err
		}
		if content.SchemaVersion == todoContentSchemaVersion {
			if err := validateTODOAuditDeliveryReferences(record, contentDigest); err != nil {
				return TODOProofReference{}, err
			}
		}
		if len(request.ProofReferences) != 0 || len(request.WaiverReferences) != 0 {
			return TODOProofReference{}, todoTeach("proof_refs/waiver_refs", "use the PACED local run and remotely verified implementation commits", ErrConflict)
		}
		foundationOnly := len(record.DiffExpectation) > 0
		for _, scope := range record.DiffExpectation {
			if !foundationScope(scope.Repository) {
				foundationOnly = false
				break
			}
		}
		if foundationOnly {
			if request.LocalProof != nil {
				return TODOProofReference{}, todoTeach("local_proof", "Foundation-only delivery uses its published revision, not a code run", ErrConflict)
			}
			return m.verifyRemoteLocalDelivery(ctx, companyID, "", record, contentDigest, TODOProofReference{})
		}
		if content.SchemaVersion == todoContentSchemaVersion {
			if err := validateLocalProofSubject(ctx, m.client.Database(company.DatabaseName), companyID, record, contentDigest, request.LocalProof); err != nil {
				return TODOProofReference{}, err
			}
		}
		local, err := m.validateLocalProof(record.ID, request.LocalProof)
		if err != nil {
			return TODOProofReference{}, err
		}
		return m.verifyRemoteLocalDelivery(ctx, companyID, "", record, contentDigest, local)
	}
	return mongoBornStageProof(record, request, contentDigest, contentRef)
}

// Reviews are human citations, not machine-verified outcomes. Their evidence
// event must postdate the immutable subject and every placement change.
func requirePlanningReviewSubjects(ctx context.Context, database *mongo.Database, record TODORecord, contentRevision int64, contentDigest string, planning *TODOPlanning) error {
	var placement TODOHistoryEvent
	err := database.Collection(todoEventsCollection).FindOne(ctx, bson.D{{Key: "project_id", Value: record.ProjectID}, {Key: "todo_id", Value: record.ID}, {Key: "kind", Value: "placement_changed"}, {Key: "revision", Value: bson.D{{Key: "$lte", Value: record.Revision}}}}, options.FindOne().SetSort(bson.D{{Key: "revision", Value: -1}})).Decode(&placement)
	if err != nil && !errors.Is(err, mongo.ErrNoDocuments) {
		return ErrUnavailable
	}
	minimum := contentRevision
	if err == nil && placement.Revision > minimum {
		minimum = placement.Revision
	}
	var event TODOHistoryEvent
	err = database.Collection(todoEventsCollection).FindOne(ctx, bson.D{{Key: "project_id", Value: record.ProjectID}, {Key: "todo_id", Value: record.ID}, {Key: "kind", Value: "evidence_changed"}, {Key: "revision", Value: bson.D{{Key: "$gt", Value: minimum}, {Key: "$lte", Value: record.Revision}}}}, options.FindOne().SetSort(bson.D{{Key: "revision", Value: -1}})).Decode(&event)
	if err != nil && !errors.Is(err, mongo.ErrNoDocuments) {
		return ErrUnavailable
	}
	for _, review := range planning.Reviews {
		field := "proof_refs.planning_review_" + review.Kind
		var matched bool
		var matchedRef *TODOProofReference
		for _, ref := range record.ProofReferences {
			if ref.Kind != "planning_review_"+review.Kind || ref.Source != "human-review" || ref.Reference != review.Reference || ref.SourceDigest != contentDigest || ref.VerificationState != "unverified" {
				continue
			}
			if matchedRef != nil {
				return todoTeach(field, "retain exactly one current human-review citation for this review subject", ErrConflict)
			}
			matchedCopy := ref
			matchedRef = &matchedCopy
			if err == nil && event.Changes.ProofReferences != nil {
				for _, recorded := range *event.Changes.ProofReferences {
					if recorded == ref {
						matched = true
						break
					}
				}
			}
		}
		if !matched {
			return todoTeach(field, "record this human review citation against the current content digest after the latest placement change", ErrConflict)
		}
		if err := requireReviewOutcome(record, *matchedRef, "planning_review_"+review.Kind, contentDigest, field); err != nil {
			return err
		}
	}
	return nil
}

// mongoBornMigrationTransitionProof carries an already verified owner approval
// across the one exact, database-verified reviewed migration edge. Callers must
// establish that edge with requireApprovedCurrentContentOrMigration first.
func mongoBornMigrationTransitionProof(record TODORecord, request TODOActionRequest, contentDigest, contentRef string, reviewedMigration bool) (TODOProofReference, bool) {
	return TODOProofReference{}, false
}

func requireApprovedCurrentContent(record TODORecord, contentDigest string) error {
	if record.StageVerification != "verified" || record.VerifiedTransitionRef == nil || record.VerifiedTransitionRef.VerificationState != "verified" || record.VerifiedTransitionRef.SourceDigest != contentDigest {
		return todoTeach("approval/content_revision", "reconcile approval of the current Mongo TODO contract before Local-Implemented", ErrConflict)
	}
	return nil
}

func (m *MongoStore) requireFreshPlanningAfterPlacement(ctx context.Context, database *mongo.Database, record TODORecord, contentRevision int64, content TODOAuthoredContent) error {
	var event TODOHistoryEvent
	err := database.Collection(todoEventsCollection).FindOne(ctx, bson.D{{Key: "project_id", Value: record.ProjectID}, {Key: "todo_id", Value: record.ID}, {Key: "kind", Value: "placement_changed"}, {Key: "revision", Value: bson.D{{Key: "$lte", Value: record.Revision}}}}, options.FindOne().SetSort(bson.D{{Key: "revision", Value: -1}})).Decode(&event)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil
	}
	if err != nil {
		return ErrUnavailable
	}
	if contentRevision <= event.Revision {
		return todoTeach("content.planning.reviews", "edit the current content and record fresh reviews after the Release placement change", ErrConflict)
	}
	previous, err := m.contentAtRevision(ctx, database, record.ProjectID, record.ID, event.Revision-1)
	if err != nil {
		return err
	}
	old := map[string]string{}
	if previous.Planning != nil {
		for _, review := range previous.Planning.Reviews {
			old[review.Kind] = review.Reference
		}
	}
	if content.Planning != nil {
		for _, review := range content.Planning.Reviews {
			if old[review.Kind] != "" && old[review.Kind] == review.Reference {
				return todoTeach("content.planning.reviews."+review.Kind, "record a fresh review reference for the changed Release placement", ErrConflict)
			}
		}
	}
	return nil
}

func requireApprovedCurrentContentOrMigration(ctx context.Context, database *mongo.Database, record TODORecord, contentDigest string) error {
	if err := requireApprovedCurrentContent(record, contentDigest); err == nil {
		return nil
	}
	if record.StageVerification != "verified" || record.VerifiedTransitionRef == nil || record.VerifiedTransitionRef.VerificationState != "verified" || !validSourceDigest(record.VerifiedTransitionRef.SourceDigest) {
		return todoTeach("approval/content_revision", "retain verified approval for the exact source content or its explicitly reviewed migration target", ErrConflict)
	}
	var latest TODOContentVersion
	if err := database.Collection(todoContentVersionsCollection).FindOne(ctx, bson.D{{Key: "project_id", Value: record.ProjectID}, {Key: "todo_id", Value: record.ID}}, options.FindOne().SetSort(bson.D{{Key: "revision", Value: -1}})).Decode(&latest); err != nil {
		return todoTeach("approval/content_revision", "retain a verified approval bound to the current immutable content or migration edge", ErrConflict)
	}
	if latest.Digest != contentDigest || latest.Revision <= 0 {
		return todoTeach("approval/content_revision", "use the latest immutable content revision", ErrConflict)
	}
	var migration TODOHistoryEvent
	err := database.Collection(todoEventsCollection).FindOne(ctx, bson.D{
		{Key: "project_id", Value: record.ProjectID}, {Key: "todo_id", Value: record.ID},
		{Key: "kind", Value: todoContentMigrationEventKind}, {Key: "content_revision", Value: latest.Revision},
	}).Decode(&migration)
	if err != nil || !validSourceDigest(migration.RequestDigest) || !validSourceDigest(migration.FromContentDigest) || !validSourceDigest(migration.ToContentDigest) || migration.OccurredAt.IsZero() || migration.FromContentDigest != record.VerifiedTransitionRef.SourceDigest || migration.ToContentDigest != contentDigest || migration.ReviewedAcceptance != todoContentMigrationAcceptance || migration.PermissionDecision != "pre-iam-private-api-allow-all" || migration.FromContentRevision == nil || *migration.FromContentRevision >= latest.Revision {
		return todoTeach("approval/content_revision", "use the exact reviewed v1-to-v2 migration target of the already approved content", ErrConflict)
	}
	var source TODOContentVersion
	err = database.Collection(todoContentVersionsCollection).FindOne(ctx, bson.D{{Key: "project_id", Value: record.ProjectID}, {Key: "todo_id", Value: record.ID}, {Key: "revision", Value: *migration.FromContentRevision}}).Decode(&source)
	computedSourceDigest, digestErr := todoContentRevisionDigest(record.ProjectID, record.ID, source.Revision, source.Content)
	if err != nil || digestErr != nil || computedSourceDigest != migration.FromContentDigest || (source.Digest != "" && source.Digest != migration.FromContentDigest) {
		return todoTeach("approval/content_revision", "resolve the migration source digest to its exact immutable content version", ErrConflict)
	}
	return nil
}

func importedEditedTODOCurrentDeliveryDigest(record TODORecord, contentDigest string) (string, error) {
	if !usesImportedTODOContentStageProof(record) {
		return "", todoTeach("source/content_revision", "use the current content digest only for a generic edited Foundation import", ErrConflict)
	}
	if err := requireApprovedCurrentContent(record, contentDigest); err != nil {
		return "", err
	}
	return contentDigest, nil
}

func importedEditedTODOCurrentDeliveryDigestWithMigration(ctx context.Context, database *mongo.Database, record TODORecord, contentDigest string) (string, error) {
	if !usesImportedTODOContentStageProof(record) {
		return "", todoTeach("source/content_revision", "use the current content digest only for a generic edited Foundation import", ErrConflict)
	}
	if err := requireApprovedCurrentContentOrMigration(ctx, database, record, contentDigest); err != nil {
		return "", err
	}
	return contentDigest, nil
}

func mongoBornStageProof(record TODORecord, request TODOActionRequest, contentDigest, contentRef string) (TODOProofReference, error) {
	if request.LocalProof != nil || len(request.WaiverReferences) != 0 {
		return TODOProofReference{}, todoTeach("local_proof/waiver_refs", "provide these only for the Local-Implemented edge", ErrConflict)
	}
	switch {
	case record.Stage == "Planning" && request.RequestedStage == "Implementation-Approval-Pending":
		if record.StageVerification != "verified" || record.VerifiedTransitionRef == nil || len(request.ProofReferences) != 0 {
			return TODOProofReference{}, todoTeach("stage_verification/proof_refs", "verify Planning and omit owner approval when requesting Implementation-Approval-Pending", ErrConflict)
		}
		return TODOProofReference{Kind: "planning-reviewed", Source: "mongo-content-revision", Reference: contentRef, SourceDigest: contentDigest, VerificationState: "verified"}, nil
	case record.Stage == "Implementation-Approval-Pending" && request.RequestedStage == "Ready-for-Implementation":
		// Preserve already verified approvals written by the earlier Mongo edge.
		if record.StageVerification == "verified" && record.VerifiedTransitionRef != nil && record.VerifiedTransitionRef.Kind == "owner-approval" && record.VerifiedTransitionRef.SourceDigest == contentDigest && len(request.ProofReferences) == 0 {
			return *record.VerifiedTransitionRef, nil
		}
		if record.StageVerification != "verified" || record.VerifiedTransitionRef == nil || record.VerifiedTransitionRef.Kind != "planning-reviewed" || record.VerifiedTransitionRef.Reference != contentRef || record.VerifiedTransitionRef.SourceDigest != contentDigest || len(request.ProofReferences) != 1 || request.ProofReferences[0].Kind != "owner-approval" || request.ProofReferences[0].Source != "owner-chat" || request.ProofReferences[0].Reference != "APROVADO" || request.ProofReferences[0].SourceDigest != contentDigest || request.ProofReferences[0].VerificationState != "unverified" {
			return TODOProofReference{}, todoTeach("approval/content_revision", "supply one explicit owner approval bound to the current immutable Mongo content revision before Ready-for-Implementation", ErrConflict)
		}
		proof := request.ProofReferences[0]
		proof.VerificationState = "verified"
		return proof, nil
	case record.Stage == "Ready-for-Implementation" && request.RequestedStage == "In-Progress":
		if len(request.ProofReferences) != 0 || record.StageVerification != "verified" || record.VerifiedTransitionRef == nil || record.VerifiedTransitionRef.Kind != "owner-approval" || record.VerifiedTransitionRef.SourceDigest != contentDigest {
			return TODOProofReference{}, todoTeach("approval/content_revision", "start only the currently approved immutable contract; record the first concrete work checkpoint before calling this edge", ErrConflict)
		}
		return TODOProofReference{Kind: "work-started", Source: "mongo-todo-api", Reference: fmt.Sprintf("%s/revision/%d", contentRef, record.Revision+1), SourceDigest: contentDigest, VerificationState: "verified"}, nil
	case record.Stage == "In-Progress" && request.RequestedStage == "In-Progress":
		if record.StageVerification != "unverified" || record.VerifiedTransitionRef != nil || len(request.ProofReferences) != 1 || request.ProofReferences[0].Kind != "owner-approval" || request.ProofReferences[0].Source != "mongo-content-revision" || request.ProofReferences[0].Reference != contentRef || request.ProofReferences[0].SourceDigest != contentDigest || request.ProofReferences[0].VerificationState != "unverified" {
			return TODOProofReference{}, todoTeach("approval/content_revision", "reapprove the current Mongo TODO contract after an In-Progress content edit; this is not acceptance of a test run", ErrConflict)
		}
		proof := request.ProofReferences[0]
		proof.VerificationState = "verified"
		return proof, nil
	default:
		return TODOProofReference{}, todoTeach("requested_stage", "use the next governed Mongo TODO edge; Local-Implemented additionally requires remote release-branch evidence", ErrConflict)
	}
}

func currentContentOwnerApprovalProof(record TODORecord, request VerifyCurrentTODOStageRequest, revision int64, digest, reference string) (TODOProofReference, error) {
	if request.ProjectID != record.ProjectID || request.TODOID != record.ID || request.ExpectedRevision == nil || *request.ExpectedRevision != record.Revision {
		return TODOProofReference{}, todoTeach("project_id/todo_id/expected_revision", "match the exact current TODO identity and Mongo revision", ErrConflict)
	}
	if record.Stage != "In-Progress" || record.StageVerification != "unverified" || record.VerifiedTransitionRef != nil {
		return TODOProofReference{}, todoTeach("stage_verification", "revalidate only an unverified In-Progress stage after a content edit", ErrConflict)
	}
	if revision < 1 || !validSourceDigest(digest) || reference != fmt.Sprintf("%s/%s/content/%d", record.ProjectID, record.ID, revision) {
		return TODOProofReference{}, todoTeach("content_revision/digest", "use the canonical current TODO content revision and digest", ErrConflict)
	}
	if !hasCurrentOwnerApproval(record.ProofReferences, reference, digest) {
		return TODOProofReference{}, todoTeach("proof_refs", "the canonical TODO record must already contain the owner approval bound to this content revision and digest", ErrConflict)
	}
	if len(request.ProofReferences) != 1 {
		return TODOProofReference{}, todoTeach("proof_refs", "provide exactly one owner-approval reference bound to the current content revision and digest", ErrConflict)
	}
	proof := request.ProofReferences[0]
	if proof.Kind != "owner-approval" || proof.Source != "mongo-content-revision" || proof.Reference != reference || proof.SourceDigest != digest || proof.VerificationState != "unverified" || !validProofReference(proof) {
		return TODOProofReference{}, todoTeach("proof_refs", "match the canonical owner approval, current content reference, and current content digest", ErrConflict)
	}
	proof.VerificationState = "verified"
	return proof, nil
}

func hasCurrentOwnerApproval(proofs []TODOProofReference, reference, digest string) bool {
	for _, proof := range proofs {
		if proof.Kind == "owner-approval" && proof.Source == "mongo-content-revision" && proof.Reference == reference && proof.SourceDigest == digest && proof.VerificationState == "unverified" {
			return true
		}
	}
	return false
}

func currentMongoContentSubject(ctx context.Context, database *mongo.Database, record TODORecord) (int64, string, string, error) {
	var version TODOContentVersion
	err := database.Collection(todoContentVersionsCollection).FindOne(ctx, bson.D{{Key: "project_id", Value: record.ProjectID}, {Key: "todo_id", Value: record.ID}, {Key: "revision", Value: bson.D{{Key: "$lte", Value: record.Revision}}}}, options.FindOne().SetSort(bson.D{{Key: "revision", Value: -1}})).Decode(&version)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return 0, "", "", todoTeach("content_revision", "exist in immutable TODO history before approval", ErrConflict)
		}
		return 0, "", "", ErrUnavailable
	}
	expected, digestErr := todoContentRevisionDigest(record.ProjectID, record.ID, version.Revision, version.Content)
	if digestErr != nil || expected != version.Digest {
		return 0, "", "", todoTeach("content_digest", "match the canonical immutable Project/TODO/content-revision hash", ErrConflict)
	}
	return version.Revision, version.Digest, fmt.Sprintf("%s/%s/content/%d", record.ProjectID, record.ID, version.Revision), nil
}

func requireCurrentTypedTODOStageAuthority(ctx context.Context, database *mongo.Database, record TODORecord) (int64, string, string, error) {
	if !hasTypedMongoTODOStageAuthority(record) {
		return 0, "", "", todoTeach("source/content_revision", "stage changes require this exact TODO's current typed Mongo v2 content; Foundation-imported, v1, and historical stage proofs are not authority", ErrConflict)
	}
	revision, digest, reference, err := currentMongoContentSubject(ctx, database, record)
	if err != nil {
		return 0, "", "", err
	}
	if revision < 0 || !validSourceDigest(digest) || digest != record.Source.Digest || reference != record.Source.Ref {
		return 0, "", "", todoTeach("content_revision/digest", "match the exact current immutable Mongo v2 content revision and digest", ErrConflict)
	}
	return revision, digest, reference, nil
}

func hasTypedMongoTODOStageAuthority(record TODORecord) bool {
	return record.Source.Kind == "mongo-content-revision" && record.Content != nil && record.Content.SchemaVersion == todoContentSchemaVersion
}

func currentTODOContentSubject(ctx context.Context, database *mongo.Database, record TODORecord) (int64, string, string, bool, error) {
	if usesTypedMongoAuthority(record) {
		revision, digest, reference, err := currentMongoContentSubject(ctx, database, record)
		return revision, digest, reference, true, err
	}
	if !usesImportedTODOContentStageProof(record) {
		return 0, "", "", false, nil
	}
	var version TODOContentVersion
	err := database.Collection(todoContentVersionsCollection).FindOne(ctx,
		bson.D{{Key: "project_id", Value: record.ProjectID}, {Key: "todo_id", Value: record.ID}},
		options.FindOne().SetSort(bson.D{{Key: "revision", Value: -1}})).Decode(&version)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return 0, "", "", false, nil
		}
		return 0, "", "", false, ErrUnavailable
	}
	return importedTODOContentSubject(record, version)
}

func usesTypedMongoAuthority(record TODORecord) bool {
	return record.Source.Kind == "mongo-content-revision" || record.Content != nil && record.Content.SchemaVersion == todoContentSchemaVersion
}

func importedTODOContentSubject(record TODORecord, version TODOContentVersion) (int64, string, string, bool, error) {
	if version.Revision < 0 {
		return 0, "", "", false, todoTeach("content_revision", "be nonnegative for imported TODO content history", ErrConflict)
	}
	reference := fmt.Sprintf("%s/%s/content/%d", record.ProjectID, record.ID, version.Revision)
	if version.Digest == "" {
		if version.Revision == 0 {
			return 0, "", "", false, nil
		}
		// Historical imported versions may predate content digests. Expose their
		// observed revision for read/repair, but never treat them as approval subjects.
		return version.Revision, "", reference, true, nil
	}
	expected, digestErr := todoContentRevisionDigest(record.ProjectID, record.ID, version.Revision, version.Content)
	if digestErr != nil || expected != version.Digest {
		return 0, "", "", false, todoTeach("content_digest", "match the canonical immutable Project/TODO/content-revision hash", ErrConflict)
	}
	if version.Revision == 0 {
		return 0, "", "", false, nil
	}
	return version.Revision, version.Digest, reference, true, nil
}

func importedEditedTODOStageProof(record TODORecord, request TODOActionRequest, contentDigest, contentRef string) (TODOProofReference, error) {
	if !usesImportedTODOContentStageProof(record) {
		return TODOProofReference{}, todoTeach("source/content_revision", "use this proof flow only for an edited Foundation-imported TODO with immutable Mongo content history", ErrConflict)
	}
	if !validSourceDigest(contentDigest) {
		return TODOProofReference{}, todoTeach("content_digest", "author a new content revision with its canonical digest before stage approval", ErrConflict)
	}
	if record.ReleaseID == "" || record.Position == nil {
		return TODOProofReference{}, todoTeach("release_id/position", "attach this imported TODO to its same-Project Release before leaving Planning", ErrConflict)
	}
	// Imported Foundation TODOs retain their previously published approval edge.
	if record.Stage == "Planning" && request.RequestedStage == "Implementation-Approval-Pending" {
		if len(request.ProofReferences) != 1 || request.ProofReferences[0].Kind != "owner-approval" || request.ProofReferences[0].Source != "mongo-content-revision" || request.ProofReferences[0].Reference != contentRef || request.ProofReferences[0].SourceDigest != contentDigest || request.ProofReferences[0].VerificationState != "unverified" {
			return TODOProofReference{}, todoTeach("proof_refs", "bind the imported TODO's owner approval to its current immutable content", ErrConflict)
		}
		proof := request.ProofReferences[0]
		proof.VerificationState = "verified"
		return proof, nil
	}
	return mongoBornStageProof(record, request, contentDigest, contentRef)
}

func importedEditedTODOContentStageEdge(record TODORecord, request TODOActionRequest) bool {
	switch {
	case record.Stage == "Planning" && request.RequestedStage == "In-Progress":
		return true // explicitly reject the legacy non-adjacent shortcut
	case record.Stage == "Planning" && request.RequestedStage == "Implementation-Approval-Pending":
		return true
	case record.Stage == "Implementation-Approval-Pending" && request.RequestedStage == "Ready-for-Implementation":
		return true
	case record.Stage == "Ready-for-Implementation" && request.RequestedStage == "In-Progress":
		return true
	default:
		return false
	}
}

func usesImportedTODOContentStageProof(record TODORecord) bool {
	if record.Source.Kind != "" || record.Source.Foundation == "" {
		return false
	}
	_, selected := selectedTODOContract(record.ID)
	return !selected
}

func invalidatesContentApproval(record TODORecord) bool {
	// Planning proves the TODO's lifecycle position, not approval of its text.
	// Refining the plan therefore cannot revoke that stage proof.
	if usesTypedMongoAuthority(record) && record.Stage == "Planning" {
		return false
	}
	return usesTypedMongoAuthority(record) || usesImportedTODOContentStageProof(record)
}

func (m *MongoStore) VerifyCurrentTODOStage(ctx context.Context, companyID string, request VerifyCurrentTODOStageRequest) (TODOActionResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	if !validID(request.ProjectID) || !todoOperationIDPattern.MatchString(request.OperationID) || request.ExpectedRevision == nil || *request.ExpectedRevision < 0 || *request.ExpectedRevision == math.MaxInt64 {
		return TODOActionResponse{}, todoTeach("expected_revision/operation_id", "provide the current revision and stable verifier operation ID", ErrInvalid)
	}
	project, collection, err := m.selectedTODOCollection(ctx, companyID, request.ProjectID, request.TODOID)
	if err != nil {
		return TODOActionResponse{}, err
	}
	record, err := readSelectedTODO(ctx, collection, request.ProjectID, request.TODOID)
	if err != nil {
		return TODOActionResponse{}, err
	}
	digest, err := todoRequestDigest(request)
	if err != nil {
		return TODOActionResponse{}, ErrInvalid
	}
	if retried, ok, err := exactTODOOperationRetry(companyID, record, request.OperationID, "verify-current-stage", digest, *request.ExpectedRevision); ok || err != nil {
		return retried, err
	}
	if retried, ok, err := m.exactTODOHistoryRetry(ctx, companyID, record, request.OperationID, digest); ok || err != nil {
		return retried, err
	}
	if record.Revision != *request.ExpectedRevision {
		return TODOActionResponse{}, &todoRevisionConflict{Revision: record.Revision}
	}
	if err := requireActiveTODO(record); err != nil {
		return TODOActionResponse{}, err
	}
	if record.Disposition != nil {
		return TODOActionResponse{}, todoTeach("disposition", "keep a disposed TODO out of delivery-stage verification", ErrConflict)
	}
	company, err := m.company(ctx, companyID)
	if err != nil {
		return TODOActionResponse{}, err
	}
	_, contentDigest, contentRef, err := requireCurrentTypedTODOStageAuthority(ctx, m.client.Database(company.DatabaseName), record)
	if err != nil {
		return TODOActionResponse{}, err
	}
	if err := m.exactTODORelease(ctx, companyID, project, record); err != nil {
		return TODOActionResponse{}, err
	}
	var proof TODOProofReference
	if record.Stage == "Planning" && record.StageVerification == "unverified" && record.VerifiedTransitionRef == nil {
		if len(request.ProofReferences) != 0 {
			return TODOActionResponse{}, todoTeach("proof_refs", "omit approval proof when recovering a Planning stage; use the current exact TODO revision", ErrConflict)
		}
		proof = TODOProofReference{Kind: "planning-stage", Source: "mongo-content-revision", Reference: contentRef, SourceDigest: contentDigest, VerificationState: "verified"}
	} else if record.Stage == "In-Progress" && record.StageVerification == "unverified" && record.Content != nil && record.Content.SchemaVersion == todoContentSchemaVersion {
		if record.ReleaseID == "" || record.Position == nil {
			return TODOActionResponse{}, todoTeach("release_id/position", "attach this In-Progress TODO to its same-Project Release before verifying its current stage", ErrConflict)
		}
		contentRevision, _, _, err := currentMongoContentSubject(ctx, m.client.Database(company.DatabaseName), record)
		if err != nil {
			return TODOActionResponse{}, err
		}
		proof, err = currentContentOwnerApprovalProof(record, request, contentRevision, contentDigest, contentRef)
		if err != nil {
			return TODOActionResponse{}, err
		}
	} else {
		return TODOActionResponse{}, todoTeach("verify-current-stage", "revalidate only Planning or In-Progress using the exact current typed Mongo v2 content and proof; legacy Foundation and historical stage proofs are not authority", ErrConflict)
	}
	return writeVerifiedTODO(ctx, m.client.Database(company.DatabaseName), collection, companyID, record, record.Stage, proof, request.OperationID, "verify-current-stage", digest)
}

func restoredLocalProof(record TODORecord, current TODOAuthoredContent, prior TODOAsOfResponse) (TODOProofReference, error) {
	if record.Stage != "Local-Implemented" || record.StageVerification != "unverified" || record.VerifiedTransitionRef != nil ||
		prior.ProjectID != record.ProjectID || prior.TODOID != record.ID || prior.Revision >= record.Revision ||
		prior.State.Stage != "Local-Implemented" || prior.State.StageVerification != "verified" || prior.State.VerifiedTransitionRef == nil ||
		prior.State.VerifiedTransitionRef.Kind != "remote-local-delivery" || prior.State.VerifiedTransitionRef.VerificationState != "verified" ||
		record.Position == nil || !prior.State.PlacementAssigned || prior.State.ReleaseID != record.ReleaseID || prior.State.Position != *record.Position ||
		!reflect.DeepEqual(current, prior.Content) || !reflect.DeepEqual(record.ImplementationCommits, prior.State.ImplementationCommits) {
		return TODOProofReference{}, todoTeach("local_delivery_recovery", "restore the exact previously verified content, Release placement and implementation commits before reusing its immutable local proof", ErrConflict)
	}
	return *prior.State.VerifiedTransitionRef, nil
}

func todoRequestDigest(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return sourceHash(data), nil
}

func exactTODOOperationRetry(companyID string, record TODORecord, id, kind, digest string, expected int64) (TODOActionResponse, bool, error) {
	last := record.LastOperation
	if last == nil || last.ID != id {
		return TODOActionResponse{}, false, nil
	}
	if last.Kind != kind || last.RequestDigest != digest || last.ResultRevision != record.Revision || expected != record.Revision-1 {
		return TODOActionResponse{}, true, todoTeach("operation_id", "the retained operation ID already belongs to different input; reload and choose a new ID", ErrConflict)
	}
	return TODOActionResponse{CompanyID: companyID, ProjectID: record.ProjectID, TODOID: record.ID, TODORevision: record.Revision, Stage: record.Stage, StageVerification: record.StageVerification, NoOp: true}, true, nil
}

func (m *MongoStore) exactTODOHistoryRetry(ctx context.Context, companyID string, record TODORecord, operationID, digest string) (TODOActionResponse, bool, error) {
	database, err := m.todoTenantDatabase(ctx, companyID)
	if err != nil {
		return TODOActionResponse{}, false, err
	}
	var event TODOHistoryEvent
	err = database.Collection(todoEventsCollection).FindOne(ctx, bson.D{{Key: "project_id", Value: record.ProjectID}, {Key: "todo_id", Value: record.ID}, {Key: "operation_id", Value: operationID}}).Decode(&event)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return TODOActionResponse{}, false, nil
	}
	if err != nil {
		return TODOActionResponse{}, false, ErrUnavailable
	}
	if event.Kind != "state_changed" || event.RequestDigest != digest || event.Changes.Stage == nil || event.Changes.StageVerification == nil {
		return TODOActionResponse{}, true, todoTeach("operation_id", "not be reused for a different accepted mutation payload", ErrConflict)
	}
	return TODOActionResponse{CompanyID: companyID, ProjectID: record.ProjectID, TODOID: record.ID, TODORevision: event.Revision,
		Stage: *event.Changes.Stage, StageVerification: *event.Changes.StageVerification, NoOp: true}, true, nil
}

func writeVerifiedTODO(ctx context.Context, database *mongo.Database, collection *mongo.Collection, companyID string, record TODORecord, stage string, proof TODOProofReference, operationID, kind, digest string, verifiedProofReferences ...[]TODOProofReference) (TODOActionResponse, error) {
	return writeVerifiedTODOWithKnowledge(ctx, database, collection, companyID, record, stage, proof, operationID, kind, digest, nil, nil, verifiedProofReferences...)
}

func writeVerifiedTODOWithKnowledge(ctx context.Context, database *mongo.Database, collection *mongo.Collection, companyID string, record TODORecord, stage string, proof TODOProofReference, operationID, kind, digest string, knowledge *TODOKnowledgeDeliveryProof, acceptance *TODOKnowledgeFinalAcceptance, verifiedProofReferences ...[]TODOProofReference) (TODOActionResponse, error) {
	if !validProofReference(proof) || proof.VerificationState != "verified" {
		return TODOActionResponse{}, ErrInvalid
	}
	nextRevision := record.Revision + 1
	operation := TODOOperationState{ID: operationID, Kind: kind, RequestDigest: digest, ResultRevision: nextRevision}
	update := bson.D{{Key: "$set", Value: bson.D{
		{Key: "stage", Value: stage}, {Key: "stage_verification", Value: "verified"},
		{Key: "verified_transition_ref", Value: proof}, {Key: "last_operation", Value: operation},
		{Key: "updated_at", Value: bson.DateTime(time.Now().UTC().UnixMilli())},
	}}, {Key: "$inc", Value: bson.D{{Key: "revision", Value: 1}}}}
	if record.AdministrativeAcceptance != nil {
		update = append(update, bson.E{Key: "$unset", Value: bson.D{{Key: "administrative_acceptance", Value: ""}}})
	}
	if knowledge != nil {
		update[0].Value = append(update[0].Value.(bson.D), bson.E{Key: "knowledge_delivery_proof", Value: knowledge})
	}
	var additionalProofRefs []TODOProofReference
	if len(verifiedProofReferences) > 0 {
		additionalProofRefs = verifiedProofReferences[0]
		if stage != "Local-Implemented" || len(additionalProofRefs) != len(record.ProofReferences) {
			return TODOActionResponse{}, ErrInvalid
		}
		update[0].Value = append(update[0].Value.(bson.D), bson.E{Key: "proof_refs", Value: additionalProofRefs})
	}
	var updated bool
	session, err := database.Client().StartSession()
	if err != nil {
		return TODOActionResponse{}, ErrUnavailable
	}
	defer session.EndSession(ctx)
	_, err = session.WithTransaction(ctx, func(txCtx context.Context) (any, error) {
		if err := requireTODOHistoryBaseline(txCtx, database, record); err != nil {
			return nil, err
		}
		result, err := collection.UpdateOne(txCtx, bson.D{{Key: "_id", Value: record.ObjectID}, {Key: "project_id", Value: record.ProjectID}, {Key: "id", Value: record.ID}, {Key: "revision", Value: record.Revision}}, update)
		if err != nil {
			return nil, normalizeMongoError(err)
		}
		if result.MatchedCount != 1 {
			return nil, &todoRevisionConflict{Revision: nextRevision}
		}
		changedStage, verification := stage, "verified"
		verifiedProof := proof
		changes := TODOHistoryChanges{Stage: &changedStage, StageVerification: &verification, VerifiedTransitionRef: &verifiedProof}
		if knowledge != nil {
			changes.KnowledgeDeliveryProof = knowledge
		}
		if record.AdministrativeAcceptance != nil {
			changes.ClearAdministrativeAcceptance = true
		}
		if additionalProofRefs != nil {
			changes.ProofReferences = &additionalProofRefs
		}
		event := TODOHistoryEvent{ObjectID: bson.NewObjectID(), ProjectID: record.ProjectID, TODOID: record.ID, Revision: nextRevision, Kind: "state_changed",
			OccurredAt: time.Now().UTC(), OperationID: operationID, RequestDigest: digest, Changes: changes}
		if acceptance != nil {
			event.PermissionDecision = "pre-iam-private-api-allow-all"
			event.Reason = acceptance.Reason
		}
		if _, err := database.Collection(todoEventsCollection).InsertOne(txCtx, event); err != nil {
			return nil, normalizeMongoError(err)
		}
		updated = true
		return nil, nil
	})
	if err != nil {
		// A concurrent writer may make Mongo abort this transaction before the
		// revision-filtered update runs. Report the observed CAS conflict, not
		// an infrastructure outage, when the aggregate has already advanced.
		current, readErr := readSelectedTODO(ctx, collection, record.ProjectID, record.ID)
		if readErr == nil && current.Revision != record.Revision {
			return TODOActionResponse{}, &todoRevisionConflict{Revision: current.Revision}
		}
		return TODOActionResponse{}, normalizeMongoError(err)
	}
	if !updated {
		return TODOActionResponse{}, ErrUnavailable
	}
	return TODOActionResponse{CompanyID: companyID, ProjectID: record.ProjectID, TODOID: record.ID, TODORevision: nextRevision, Stage: stage, StageVerification: "verified"}, nil
}

func (m *MongoStore) verifyCoordinatorProduction(ctx context.Context, companyID string, collection *mongo.Collection, coordinator TODORecord) (TODOProofReference, error) {
	for _, id := range []string{"t-cf5c0ffd07c4f50d", "t-b9e3ea9e3a616b01"} {
		child, err := readSelectedTODO(ctx, collection, coordinator.ProjectID, id)
		if err != nil || child.ReleaseID != coordinator.ReleaseID || child.Stage != "Completed" || child.StageVerification != "verified" || child.VerifiedTransitionRef == nil || child.VerifiedTransitionRef.Kind != "production-deployment" || child.VerifiedTransitionRef.Reference != strconv.FormatInt(booraV071DeploymentID, 10)+"@"+booraV071RootSHA || containsTODOString(child.Qualifiers, "Blocked") {
			return TODOProofReference{}, todoTeach("coordination_children", "verify both exact v0.7.1 implementation TODOs as Completed before the coordinator", ErrConflict)
		}
	}
	return m.verifyProductionCommit(ctx, companyID, coordinator, "project", TODOCommitReference{Repository: "Docker", Commit: booraV071RootSHA})
}

func (m *MongoStore) selectedTODOCollection(ctx context.Context, companyID, projectID, todoID string) (Project, *mongo.Collection, error) {
	if !validID(companyID) || !validID(projectID) || !validID(todoID) {
		return Project{}, nil, todoTeach("company_id/project_id/todo_id", "name valid identities for an admitted TODO in this Project", ErrNotFound)
	}
	project, err := m.Project(ctx, companyID, projectID)
	if err != nil {
		return Project{}, nil, err
	}
	if project.CompanyID != companyID {
		return Project{}, nil, todoTeach("company_id/project_id", "match the registered Project pair", ErrConflict)
	}
	company, err := m.company(ctx, companyID)
	if err != nil {
		return Project{}, nil, err
	}
	return project, m.client.Database(company.DatabaseName).Collection(todosCollection), nil
}

func readSelectedTODO(ctx context.Context, collection *mongo.Collection, projectID, todoID string) (TODORecord, error) {
	var record TODORecord
	err := collection.FindOne(ctx, bson.D{{Key: "project_id", Value: projectID}, {Key: "id", Value: todoID}}).Decode(&record)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return TODORecord{}, ErrNotFound
		}
		return TODORecord{}, ErrUnavailable
	}
	var current TODOContentVersion
	err = collection.Database().Collection(todoContentVersionsCollection).FindOne(ctx, bson.D{{Key: "project_id", Value: projectID}, {Key: "todo_id", Value: todoID}, {Key: "revision", Value: bson.D{{Key: "$lte", Value: record.Revision}}}}, options.FindOne().SetSort(bson.D{{Key: "revision", Value: -1}})).Decode(&current)
	if err != nil && !errors.Is(err, mongo.ErrNoDocuments) {
		return TODORecord{}, ErrUnavailable
	}
	if err == nil && current.Content.SchemaVersion == todoContentSchemaVersion {
		digest, digestErr := todoContentRevisionDigest(projectID, todoID, current.Revision, current.Content)
		if digestErr != nil || !validSourceDigest(current.Digest) || digest != current.Digest {
			return TODORecord{}, todoTeach("content_digest", "match the current immutable Mongo content revision", ErrConflict)
		}
		record.Content = &current.Content.TODOContent
		record.DiffExpectation = current.Content.DiffExpectation
		record.AffectedFiles = current.Content.AffectedFiles
		record.Source = TODOSource{Kind: "mongo-content-revision", Ref: fmt.Sprintf("%s/%s/content/0", projectID, todoID), Digest: current.Digest}
	}
	if err := validateStoredTODO(record); err != nil {
		return TODORecord{}, err
	}
	if record.Content != nil && record.Content.SchemaVersion == todoContentSchemaVersion {
		record.Source.Ref = fmt.Sprintf("%s/%s/content/%d", projectID, todoID, current.Revision)
	}
	return record, nil
}

func validateStoredTODO(record TODORecord) error {
	if contract, selected := selectedTODOContract(record.ID); selected && (record.Content == nil || record.Content.SchemaVersion != todoContentSchemaVersion) {
		return validateStoredSelectedTODO(record, contract)
	}
	mongoBorn := record.Source.Kind == "mongo-content-revision"
	placementInvalid := (record.ReleaseID == "") != (record.Position == nil) || (record.ReleaseID != "" && (!validID(record.ReleaseID) || *record.Position < 0))
	provenanceInvalid := record.Source.MappingDigest == "" || record.Source.ImportedAt == 0
	if mongoBorn {
		provenanceInvalid = record.Source.Ref != fmt.Sprintf("%s/%s/content/0", record.ProjectID, record.ID) || !validSourceDigest(record.Source.Digest) || record.Source.Foundation != "" || record.Source.Revision != "" || record.Source.Path != "" || record.Source.MappingDigest != "" || record.Source.ImportedAt != 0
		if record.Stage == "Planning" && record.ReleaseID == "" && record.StageVerification != "verified" {
			return todoTeach("stage_verification", "verify the initial Mongo-created Planning state", ErrConflict)
		}
	} else {
		placementInvalid = placementInvalid || record.ReleaseID == "" || record.Position == nil
	}
	if record.ObjectID.IsZero() || !validID(record.ID) || !validID(record.ProjectID) || placementInvalid ||
		provenanceInvalid || record.Revision < 0 ||
		record.ArtifactType != "tactical_execution_contract" || !validName(record.Title) || !validSummaryField(record.Objective, 1024) || !validSummaryField(record.NextExactStep, 512) || !validSummaryField(record.WorkState, 128) ||
		!containsTODOString(canonicalTODOStages, record.Stage) || !validGenericTODOSectionsAndEvidence(record) {
		return todoTeach("todo.identity/source/revision/content", "match an admitted typed TODO under the requested Project", ErrConflict)
	}
	if record.StageVerification != "unverified" && record.StageVerification != "verified" {
		return todoTeach("stage_verification", "be unverified or verified by the private current-stage verifier", ErrConflict)
	}
	if (record.StageVerification == "verified") != (record.VerifiedTransitionRef != nil) {
		return todoTeach("verified_transition_ref", "be present exactly when stage_verification is verified", ErrConflict)
	}
	if proof := record.VerifiedTransitionRef; proof != nil && (proof.VerificationState != "verified" || !validProofReference(*proof)) {
		return todoTeach("verified_transition_ref", "contain the exact valid verified proof reference", ErrConflict)
	}
	if err := validateAdministrativeTODOState(record); err != nil {
		return err
	}
	if record.LastOperation != nil && (!todoOperationIDPattern.MatchString(record.LastOperation.ID) || !validTODOOperationKind(record.LastOperation.Kind) || !validSourceDigest(record.LastOperation.RequestDigest) || record.LastOperation.ResultRevision < 0 || record.LastOperation.ResultRevision > record.Revision || (record.LastOperation.ResultRevision == 0 && record.LastOperation.Kind != "create-todo")) {
		return todoTeach("last_operation", "contain a valid latest idempotency marker at or before the current revision", ErrConflict)
	}
	return nil
}

func validateAdministrativeTODOState(record TODORecord) error {
	if basis := record.AdministrativeAcceptance; basis != nil {
		actorValid := basis.ActorClaim == nil || (validSummaryField(basis.ActorClaim.Value, 160) && basis.ActorClaim.VerificationStatus == "unverified")
		if basis.AcceptedStage != record.Stage || !validSummaryField(basis.OwnerReference, 512) || !validSummaryField(basis.Reason, 512) || basis.PermissionDecision != "pre-iam-private-api-allow-all" || basis.AcceptedAt == 0 || !validSourceDigest(basis.ContentDigest) || !actorValid || record.StageVerification != "unverified" || record.VerifiedTransitionRef != nil {
			return todoTeach("administrative_acceptance", "match the authored stage and current unverified state with a valid owner basis, permission decision, timestamp, and content digest", ErrConflict)
		}
	}
	if record.Disposition == nil {
		if record.DispositionState != "" {
			return todoTeach("disposition_state", "be omitted when no non-delivery disposition exists", ErrConflict)
		}
		return nil
	}
	d := record.Disposition
	if record.DispositionState != "valid" || record.AdministrativeAcceptance == nil || record.Stage != "Planning" || (d.Kind != "Superseded" && d.Kind != "Discarded") || !validSummaryField(d.Reason, 512) || (d.SuccessorTODOID != "" && (!validID(d.SuccessorTODOID) || d.SuccessorTODOID == record.ID)) {
		return todoTeach("disposition/disposition_state", "be a valid administratively accepted non-delivery record at Planning with an optional distinct successor", ErrConflict)
	}
	return nil
}

func validGenericTODOSectionsAndEvidence(record TODORecord) bool {
	provenanceValid := validID(record.Source.Foundation) && validGitCommit(record.Source.Revision) && validRepositoryPattern(record.Source.Path) && validSourceDigest(record.Source.Digest)
	if record.Source.Kind == "mongo-content-revision" {
		provenanceValid = record.Source.Ref != "" && validSourceDigest(record.Source.Digest) && record.Source.Foundation == "" && record.Source.Revision == "" && record.Source.Path == ""
	}
	if record.Qualifiers == nil || record.Sections == nil || len(record.Sections) > maxTODOSections || len(record.ProofReferences)+len(record.WaiverReferences)+len(record.ImplementationCommits) > maxTODOProofs || !provenanceValid {
		return false
	}
	seenQualifiers := map[string]bool{}
	for _, qualifier := range record.Qualifiers {
		if !containsTODOString(canonicalTODOQualifiers, qualifier) || seenQualifiers[qualifier] {
			return false
		}
		seenQualifiers[qualifier] = true
	}
	seenTitles := map[string]bool{}
	for i, section := range record.Sections {
		if strings.TrimSpace(section.Key) == "" || strings.TrimSpace(section.Title) == "" || section.Position != i || seenTitles[section.Title] || !utf8.ValidString(section.BodyMarkdown) || len(section.BodyMarkdown) > 64<<10 {
			return false
		}
		seenTitles[section.Title] = true
	}
	for _, proof := range append(append([]TODOProofReference{}, record.ProofReferences...), record.WaiverReferences...) {
		if !validProofReference(proof) || proof.VerificationState != "unverified" && proof.VerificationState != "verified" {
			return false
		}
	}
	for _, commit := range record.ImplementationCommits {
		if strings.TrimSpace(commit.Repository) == "" || !validGitCommit(commit.Commit) || !validSourceDigest(commit.SourceDigest) || commit.VerificationState != "unverified" && commit.VerificationState != "verified" {
			return false
		}
	}
	for _, scope := range record.DiffExpectation {
		if !validName(scope.Repository) || strings.TrimSpace(scope.Baseline) == "" || scope.ExpectedPaths == nil || scope.NotExpectedPaths == nil {
			return false
		}
		for _, pattern := range append(append([]string{}, scope.ExpectedPaths...), scope.NotExpectedPaths...) {
			if !validRepositoryPattern(pattern) {
				return false
			}
		}
	}
	for _, file := range record.AffectedFiles {
		if !validRepositoryPattern(file) {
			return false
		}
	}
	return true
}

func validateStoredSelectedTODO(record TODORecord, contract selectedTODOSourceContract) error {
	if record.ObjectID.IsZero() || record.ID == "" || record.ProjectID != contract.ProjectID || !validID(record.ReleaseID) || record.Position == nil || *record.Position < 0 ||
		record.Source.Foundation != contract.Foundation || record.Source.Revision != contract.FoundationSHA || record.Source.Path != contract.Path || record.Source.Digest != contract.SourceDigest ||
		record.Source.MappingDigest == "" || record.Source.ImportedAt == 0 || record.Revision < 0 || !containsTODOString(canonicalTODOStages, record.Stage) ||
		record.ArtifactType != "tactical_execution_contract" || !validName(record.Title) || !validSummaryField(record.Objective, 1024) || !validSummaryField(record.NextExactStep, 512) {
		return todoTeach("todo.identity/source/revision/stage", "match the pinned migrated TODO identity and canonical Mongo record", ErrConflict)
	}
	if record.StageVerification != "unverified" && record.StageVerification != "verified" {
		return todoTeach("stage_verification", "be unverified or verified by the private current-stage verifier", ErrConflict)
	}
	if (record.StageVerification == "verified") != (record.VerifiedTransitionRef != nil) {
		return todoTeach("verified_transition_ref", "be present exactly when stage_verification is verified", ErrConflict)
	}
	if proof := record.VerifiedTransitionRef; proof != nil && (proof.VerificationState != "verified" || !validProofReference(*proof)) {
		return todoTeach("verified_transition_ref", "contain the exact valid verified proof reference", ErrConflict)
	}
	if record.LastOperation != nil && (!todoOperationIDPattern.MatchString(record.LastOperation.ID) || !validTODOOperationKind(record.LastOperation.Kind) || !validSourceDigest(record.LastOperation.RequestDigest) || record.LastOperation.ResultRevision < 0 || record.LastOperation.ResultRevision > record.Revision || (record.LastOperation.ResultRevision == 0 && record.LastOperation.Kind != "create-todo")) {
		return todoTeach("last_operation", "contain a valid latest idempotency marker at or before the current revision", ErrConflict)
	}
	return nil
}

func validateTODOAction(request TODOActionRequest) error {
	if !validID(request.ProjectID) || !todoOperationIDPattern.MatchString(request.OperationID) || request.ExpectedRevision == nil || *request.ExpectedRevision < 0 || *request.ExpectedRevision == math.MaxInt64 || !containsTODOString(canonicalTODOStages, request.RequestedStage) {
		return todoTeach("project_id/expected_revision/operation_id/requested_stage", "provide the registered Project, nonnegative expected_revision, stable operation_id, and canonical requested_stage", ErrInvalid)
	}
	return nil
}

func unverifiedProofs(proofs []TODOProofReference, field string) ([]TODOProofReference, error) {
	if proofs == nil {
		return []TODOProofReference{}, nil
	}
	if len(proofs) > maxTODOProofs {
		return nil, todoTeach(field, fmt.Sprintf("contain at most %d unverified references", maxTODOProofs), ErrInvalid)
	}
	result := append([]TODOProofReference(nil), proofs...)
	for _, proof := range result {
		if !validProofReference(proof) || proof.VerificationState != "unverified" {
			return nil, todoTeach(field, "contain valid references marked unverified; only verify-current-stage can persist verified", ErrInvalid)
		}
	}
	return result, nil
}

func validProofReference(proof TODOProofReference) bool {
	return strings.TrimSpace(proof.Kind) != "" && strings.TrimSpace(proof.Source) != "" && strings.TrimSpace(proof.Reference) != "" && validSourceDigest(proof.SourceDigest) && validReviewOutcome(proof.Outcome)
}

func (m *MongoStore) roadmapTODOs(ctx context.Context, collection *mongo.Collection, projectID string, releases []roadmapRelease) ([]roadmapTODOView, error) {
	pipeline := mongo.Pipeline{
		bson.D{{Key: "$match", Value: bson.D{{Key: "project_id", Value: projectID}, {Key: "removed", Value: bson.D{{Key: "$ne", Value: true}}}}}},
		bson.D{{Key: "$sort", Value: bson.D{{Key: "position", Value: 1}, {Key: "id", Value: 1}}}},
		bson.D{{Key: "$lookup", Value: bson.D{
			{Key: "from", Value: todoContentVersionsCollection},
			{Key: "let", Value: bson.D{{Key: "todo_id", Value: "$id"}}},
			{Key: "pipeline", Value: mongo.Pipeline{
				bson.D{{Key: "$match", Value: bson.D{{Key: "project_id", Value: projectID}, {Key: "$expr", Value: bson.D{{Key: "$eq", Value: bson.A{"$todo_id", "$$todo_id"}}}}}}},
				bson.D{{Key: "$sort", Value: bson.D{{Key: "revision", Value: -1}}}},
				bson.D{{Key: "$limit", Value: 1}},
			}},
			{Key: "as", Value: "latest_content_version"},
		}}},
	}
	cursor, err := collection.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer cursor.Close(ctx)
	releaseIDs := make(map[string]bool, len(releases))
	for _, release := range releases {
		releaseIDs[release.ID] = true
	}
	records := make([]TODORecord, 0)
	latestByID := make(map[string]TODOContentVersion)
	for cursor.Next(ctx) {
		var row struct {
			TODORecord `bson:",inline"`
			Latest     []TODOContentVersion `bson:"latest_content_version"`
		}
		if cursor.Decode(&row) != nil || len(row.Latest) > 1 {
			return nil, ErrUnavailable
		}
		records = append(records, row.TODORecord)
		if len(row.Latest) == 1 {
			latestByID[row.ID] = row.Latest[0]
		}
	}
	if cursor.Err() != nil {
		return nil, ErrUnavailable
	}
	result := make([]roadmapTODOView, 0, len(records))
	for _, record := range records {
		if current, ok := latestByID[record.ID]; ok && current.Content.SchemaVersion == todoContentSchemaVersion {
			if current.Revision > record.Revision {
				return nil, todoTeach("todos[].revision", "reload the Roadmap after the concurrent content change", ErrConflict)
			}
			digest, digestErr := todoContentRevisionDigest(projectID, record.ID, current.Revision, current.Content)
			if digestErr != nil || !validSourceDigest(current.Digest) || digest != current.Digest {
				return nil, todoTeach("todos[].content_digest", "match immutable Mongo content before projecting the Roadmap", ErrConflict)
			}
			record.Content = &current.Content.TODOContent
			record.DiffExpectation = current.Content.DiffExpectation
			record.AffectedFiles = current.Content.AffectedFiles
			record.Source = TODOSource{Kind: "mongo-content-revision", Ref: fmt.Sprintf("%s/%s/content/0", projectID, record.ID), Digest: current.Digest}
			if record.AdministrativeAcceptance != nil && record.Disposition == nil && record.AdministrativeAcceptance.ContentDigest != current.Digest {
				return nil, todoTeach("todos[].administrative_acceptance.content_digest", "match the current immutable typed content before Roadmap projection", ErrConflict)
			}
		}
		assigned := record.ReleaseID != "" && record.Position != nil && releaseIDs[record.ReleaseID]
		backlog := record.Source.Kind == "mongo-content-revision" && record.ReleaseID == "" && record.Position == nil
		if !validID(record.ID) || record.ProjectID != projectID || (!assigned && !backlog) {
			return nil, todoTeach("todos[].project_id/release_id", "match an admitted migrated TODO and Release in this Project", ErrConflict)
		}
		if err := validateStoredTODO(record); err != nil {
			return nil, err
		}
		result = append(result, roadmapTODOViewFromRecord(record))
	}
	return result, nil
}

func (m *MongoStore) exactTODORelease(ctx context.Context, companyID string, project Project, todo TODORecord) error {
	if todo.Removed {
		return nil
	}
	if todo.Source.Kind == "mongo-content-revision" && todo.ReleaseID == "" && todo.Position == nil {
		company, err := m.company(ctx, companyID)
		if err != nil {
			return err
		}
		return validateTODOProjectMembership(ctx, m.client.Database(company.DatabaseName), companyID, project.ID)
	}
	if todo.ReleaseID == "" || todo.Position == nil {
		return todoTeach("release_id/position", "be jointly present for an assigned TODO or jointly absent for a Mongo Backlog TODO", ErrConflict)
	}
	company, err := m.company(ctx, companyID)
	if err != nil {
		return err
	}
	return validateTODOReleaseMembership(ctx, m.client.Database(company.DatabaseName), companyID, project.ID, todo.ReleaseID)
}

func validSummaryField(value string, maxBytes int) bool {
	return strings.TrimSpace(value) != "" && utf8.ValidString(value) && len([]byte(value)) <= maxBytes
}

func TODOConflictRevision(err error) (int64, bool) {
	var conflict *todoRevisionConflict
	if !errors.As(err, &conflict) {
		return 0, false
	}
	return conflict.Revision, true
}
