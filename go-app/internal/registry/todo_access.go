package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
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
	return value == "transition" || value == "verify-current-stage" || value == "content-edit" || value == "evidence-edit" || value == "placement-edit"
}

type TODOReadResponse struct {
	CompanyID           string     `json:"company_id"`
	ProjectID           string     `json:"project_id"`
	SnapshotFingerprint string     `json:"snapshot_fingerprint"`
	TODO                TODORecord `json:"todo"`
	TODORevision        int64      `json:"todo_revision"`
	ContentRevision     *int64     `json:"content_revision,omitempty"`
	ContentDigest       string     `json:"content_digest,omitempty"`
}

type TODOActionRequest struct {
	ProjectID        string               `json:"project_id"`
	TODOID           string               `json:"todo_id"`
	ExpectedRevision *int64               `json:"expected_revision"`
	OperationID      string               `json:"operation_id"`
	RequestedStage   string               `json:"requested_stage"`
	ProofReferences  []TODOProofReference `json:"proof_refs"`
	WaiverReferences []TODOProofReference `json:"waiver_refs"`
	LocalProof       *LocalProofResult    `json:"local_proof,omitempty"`
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
	response := TODOReadResponse{CompanyID: companyID, ProjectID: projectID, SnapshotFingerprint: project.SnapshotFingerprint, TODO: record, TODORevision: record.Revision}
	if record.Source.Kind == "mongo-content-revision" || usesImportedTODOContentStageProof(record) {
		company, err := m.company(ctx, companyID)
		if err != nil {
			return TODOReadResponse{}, err
		}
		contentRevision, contentDigest, _, hasCurrentContent, err := currentTODOContentSubject(ctx, m.client.Database(company.DatabaseName), record)
		if err != nil {
			return TODOReadResponse{}, err
		}
		// Historical Foundation imports can have a revision without a stored
		// digest. Keep them readable, but never expose an incomplete content
		// identity that consumers could mistake for current proof.
		if hasCurrentContent && validSourceDigest(contentDigest) {
			response.ContentRevision, response.ContentDigest = &contentRevision, contentDigest
		}
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
	if err := m.exactTODORelease(ctx, companyID, project, record); err != nil {
		return TODOActionResponse{}, err
	}
	var proof TODOProofReference
	if record.Source.Kind == "mongo-content-revision" {
		proof, err = m.mongoBornTransitionProof(ctx, companyID, project, record, request)
		if err != nil {
			return TODOActionResponse{}, err
		}
	} else {
		hasEditedImportedContent := false
		var contentDigest, contentRef string
		if usesImportedTODOContentStageProof(record) {
			company, companyErr := m.company(ctx, companyID)
			if companyErr != nil {
				return TODOActionResponse{}, companyErr
			}
			_, contentDigest, contentRef, hasEditedImportedContent, err = currentTODOContentSubject(ctx, m.client.Database(company.DatabaseName), record)
			if err != nil {
				return TODOActionResponse{}, err
			}
		}
		if hasEditedImportedContent && importedEditedTODOContentStageEdge(record, request) {
			proof, err = importedEditedTODOStageProof(record, request, contentDigest, contentRef)
			if err != nil {
				return TODOActionResponse{}, err
			}
		} else if _, selected := selectedTODOContract(record.ID); !selected {
			switch {
			case record.Stage == "Planning" && request.RequestedStage == "In-Progress":
				if request.LocalProof != nil || len(request.WaiverReferences) != 0 ||
					len(request.ProofReferences) != 1 || request.ProofReferences[0].Kind != "owner-approval" || request.ProofReferences[0].Source != "owner-chat" || request.ProofReferences[0].Reference != "APROVADO" || request.ProofReferences[0].SourceDigest != record.Source.Digest || request.ProofReferences[0].VerificationState != "unverified" {
					return TODOActionResponse{}, todoTeach("requested_stage/proof_refs", "start an individually admitted TODO from Planning to In-Progress with one owner-approval reference bound to its immutable source digest", ErrConflict)
				}
				proof = request.ProofReferences[0]
				proof.VerificationState = "verified"
			case request.ProjectID == "builder" && record.Stage == "In-Progress" && request.RequestedStage == "Local-Implemented":
				if len(request.ProofReferences) != 0 || len(request.WaiverReferences) != 0 {
					return TODOActionResponse{}, todoTeach("proof_refs/waiver_refs", "use only the fixed local result and remotely verified commits", ErrConflict)
				}
				deliveryDigest := record.Source.Digest
				if hasEditedImportedContent {
					deliveryDigest, err = importedEditedTODOCurrentDeliveryDigest(record, contentDigest)
					if err != nil {
						return TODOActionResponse{}, err
					}
				}
				proof, err = m.validateLocalProof(record.ID, request.LocalProof)
				if err != nil {
					return TODOActionResponse{}, err
				}
				if project.Binding == nil {
					return TODOActionResponse{}, todoTeach("project.binding", "register the Project Foundation repository before remote delivery proof", ErrConflict)
				}
				proof, err = m.verifyRemoteLocalDelivery(ctx, companyID, project.Binding.RepositoryID, record, deliveryDigest, proof)
				if err != nil {
					return TODOActionResponse{}, err
				}
			default:
				return TODOActionResponse{}, todoTeach("requested_stage", "use a governed transition supported by the individually admitted TODO proof contract", ErrConflict)
			}
		} else {
			switch request.ProjectID {
			case "builder":
				if request.TODOID == "builder-mongo-selected-todo-stage-verification" && record.Stage == "Planning" && request.RequestedStage == "In-Progress" {
					if request.LocalProof != nil || len(request.WaiverReferences) != 0 {
						return TODOActionResponse{}, todoTeach("local_proof/waiver_refs", "do not attach local-completion or waiver evidence to the actual-start operation", ErrConflict)
					}
					if len(request.ProofReferences) != 1 || request.ProofReferences[0].Kind != "owner-approval" || request.ProofReferences[0].Source != "owner-chat" || request.ProofReferences[0].Reference != "APROVADO" || request.ProofReferences[0].SourceDigest != record.Source.Digest || request.ProofReferences[0].VerificationState != "unverified" {
						return TODOActionResponse{}, todoTeach("proof_refs", "provide this TODO's exact owner approval bound to its source digest before starting", ErrConflict)
					}
					proof = request.ProofReferences[0]
					proof.VerificationState = "verified"
				} else if record.Stage == "In-Progress" && request.RequestedStage == "Local-Implemented" {
					if len(request.ProofReferences) != 0 || len(request.WaiverReferences) != 0 {
						return TODOActionResponse{}, todoTeach("proof_refs/waiver_refs", "use only the fixed local result and remotely verified commits", ErrConflict)
					}
					proof, err = m.validateLocalProof(record.ID, request.LocalProof)
					if err != nil {
						return TODOActionResponse{}, err
					}
					if project.Binding == nil {
						return TODOActionResponse{}, todoTeach("project.binding", "register the Project Foundation repository before remote delivery proof", ErrConflict)
					}
					proof, err = m.verifyRemoteLocalDelivery(ctx, companyID, project.Binding.RepositoryID, record, record.Source.Digest, proof)
					if err != nil {
						return TODOActionResponse{}, err
					}
				} else {
					return TODOActionResponse{}, todoTeach("requested_stage", "use the approved actual-start or verified-local-run transition for this selected Builder TODO", ErrConflict)
				}
			case "boora":
				if len(request.ProofReferences) != 0 || len(request.WaiverReferences) != 0 || request.LocalProof != nil {
					return TODOActionResponse{}, todoTeach("proof_refs/waiver_refs/local_proof", "the Reader, not caller-authored evidence, verifies this production transition", ErrConflict)
				}
				if request.RequestedStage != "Completed" {
					return TODOActionResponse{}, todoTeach("requested_stage", "this cut verifies only the selected v0.7.1 production completion", ErrConflict)
				}
				if record.ID == "t-b9e3ea9e3a616b01" {
					return TODOActionResponse{}, todoTeach("requested_stage", "use verify-current-stage for this already-authored Completed TODO", ErrConflict)
				}
				source, _ := selectedTODOContract(record.ID)
				if record.Stage != source.Stage {
					return TODOActionResponse{}, todoTeach("stage", "reconcile this selected TODO's exact authored predecessor before a late Completed write", ErrConflict)
				}
				if record.ID == "t-9936500c9582e07e" {
					proof, err = m.verifyCoordinatorProduction(ctx, companyID, collection, record)
				} else {
					modulePath, claim, claimErr := productionModulePath(record)
					if claimErr != nil {
						return TODOActionResponse{}, claimErr
					}
					proof, err = m.verifyProductionCommit(ctx, companyID, record, modulePath, claim)
				}
				if err != nil {
					return TODOActionResponse{}, err
				}
			default:
				return TODOActionResponse{}, ErrNotFound
			}
		}
	}
	company, err := m.company(ctx, companyID)
	if err != nil {
		return TODOActionResponse{}, err
	}
	return writeVerifiedTODO(ctx, m.client.Database(company.DatabaseName), collection, companyID, record, request.RequestedStage, proof, request.OperationID, "transition", digest)
}

func (m *MongoStore) mongoBornTransitionProof(ctx context.Context, companyID string, project Project, record TODORecord, request TODOActionRequest) (TODOProofReference, error) {
	if record.ReleaseID == "" || record.Position == nil {
		return TODOProofReference{}, todoTeach("release_id/position", "attach this Backlog TODO to a same-Project Release before leaving Planning", ErrConflict)
	}
	company, err := m.company(ctx, companyID)
	if err != nil {
		return TODOProofReference{}, err
	}
	_, contentDigest, contentRef, err := currentMongoContentSubject(ctx, m.client.Database(company.DatabaseName), record)
	if err != nil {
		return TODOProofReference{}, err
	}
	if record.Stage == "In-Progress" && request.RequestedStage == "Local-Implemented" {
		if err := requireApprovedCurrentContent(record, contentDigest); err != nil {
			return TODOProofReference{}, err
		}
		if project.Binding == nil {
			return TODOProofReference{}, todoTeach("project.binding", "register the Project Foundation repository before remote delivery proof", ErrConflict)
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
			return m.verifyRemoteLocalDelivery(ctx, companyID, project.Binding.RepositoryID, record, contentDigest, TODOProofReference{})
		}
		local, err := m.validateLocalProof(record.ID, request.LocalProof)
		if err != nil {
			return TODOProofReference{}, err
		}
		return m.verifyRemoteLocalDelivery(ctx, companyID, project.Binding.RepositoryID, record, contentDigest, local)
	}
	return mongoBornStageProof(record, request, contentDigest, contentRef)
}

func requireApprovedCurrentContent(record TODORecord, contentDigest string) error {
	if record.StageVerification != "verified" || record.VerifiedTransitionRef == nil || record.VerifiedTransitionRef.VerificationState != "verified" || record.VerifiedTransitionRef.SourceDigest != contentDigest {
		return todoTeach("approval/content_revision", "reconcile approval of the current Mongo TODO contract before Local-Implemented", ErrConflict)
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

func mongoBornStageProof(record TODORecord, request TODOActionRequest, contentDigest, contentRef string) (TODOProofReference, error) {
	if request.LocalProof != nil || len(request.WaiverReferences) != 0 {
		return TODOProofReference{}, todoTeach("local_proof/waiver_refs", "provide these only for the Local-Implemented edge", ErrConflict)
	}
	switch {
	case record.Stage == "Planning" && request.RequestedStage == "Implementation-Approval-Pending":
		if len(request.ProofReferences) != 1 || request.ProofReferences[0].Kind != "owner-approval" || request.ProofReferences[0].Source != "mongo-content-revision" || request.ProofReferences[0].Reference != contentRef || request.ProofReferences[0].SourceDigest != contentDigest || request.ProofReferences[0].VerificationState != "unverified" {
			return TODOProofReference{}, todoTeach("proof_refs", "bind the owner approval to the current immutable Mongo content revision and digest", ErrConflict)
		}
		proof := request.ProofReferences[0]
		proof.VerificationState = "verified"
		return proof, nil
	case record.Stage == "Implementation-Approval-Pending" && request.RequestedStage == "Ready-for-Implementation":
		if len(request.ProofReferences) != 0 || record.StageVerification != "verified" || record.VerifiedTransitionRef == nil || record.VerifiedTransitionRef.Kind != "owner-approval" || record.VerifiedTransitionRef.Reference != contentRef || record.VerifiedTransitionRef.SourceDigest != contentDigest {
			return TODOProofReference{}, todoTeach("approval/content_revision", "preserve the verified owner approval for the current immutable contract before Ready-for-Implementation", ErrConflict)
		}
		return *record.VerifiedTransitionRef, nil
	case record.Stage == "Ready-for-Implementation" && request.RequestedStage == "In-Progress":
		if len(request.ProofReferences) != 0 || record.StageVerification != "verified" || record.VerifiedTransitionRef == nil || record.VerifiedTransitionRef.Kind != "owner-approval" || record.VerifiedTransitionRef.Reference != contentRef || record.VerifiedTransitionRef.SourceDigest != contentDigest {
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

func currentContentOwnerApprovalProof(record TODORecord, request VerifyCurrentTODOStageRequest, content TODOAuthoredContent, revision int64, digest, reference string) (TODOProofReference, error) {
	if request.ProjectID != record.ProjectID || request.TODOID != record.ID || request.ExpectedRevision == nil || *request.ExpectedRevision != record.Revision {
		return TODOProofReference{}, todoTeach("project_id/todo_id/expected_revision", "match the exact current TODO identity and Mongo revision", ErrConflict)
	}
	if record.Stage != "In-Progress" || record.StageVerification != "unverified" || record.VerifiedTransitionRef != nil {
		return TODOProofReference{}, todoTeach("stage_verification", "revalidate only an unverified In-Progress stage after a content edit", ErrConflict)
	}
	if revision < 1 || !validSourceDigest(digest) || reference != fmt.Sprintf("%s/%s/content/%d", record.ProjectID, record.ID, revision) {
		return TODOProofReference{}, todoTeach("content_revision/digest", "use the canonical current TODO content revision and digest", ErrConflict)
	}
	if len(content.Sections) == 0 || !recordHasCurrentOwnerApproval(record.Sections, revision, digest) {
		return TODOProofReference{}, todoTeach("approval/content_revision", "the canonical record must approve this current immutable content revision and digest", ErrConflict)
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

func recordHasCurrentOwnerApproval(sections []TODOSection, revision int64, digest string) bool {
	for _, section := range sections {
		if section.Key != "approval" {
			continue
		}
		body := strings.ToLower(section.BodyMarkdown)
		marker := "approval status:"
		index := strings.Index(body, marker)
		if index < 0 {
			continue
		}
		status := strings.TrimLeft(strings.TrimSpace(body[index+len(marker):]), "*` \t\r\n")
		if strings.HasPrefix(status, "approved") && strings.Contains(body, fmt.Sprintf("content revision %d", revision)) && strings.Contains(body, strings.ToLower(digest)) {
			return true
		}
	}
	return false
}

func currentMongoContentSubject(ctx context.Context, database *mongo.Database, record TODORecord) (int64, string, string, error) {
	var version TODOContentVersion
	err := database.Collection(todoContentVersionsCollection).FindOne(ctx, bson.D{{Key: "project_id", Value: record.ProjectID}, {Key: "todo_id", Value: record.ID}}, options.FindOne().SetSort(bson.D{{Key: "revision", Value: -1}})).Decode(&version)
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

func currentTODOContentSubject(ctx context.Context, database *mongo.Database, record TODORecord) (int64, string, string, bool, error) {
	if record.Source.Kind == "mongo-content-revision" {
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
	return record.Source.Kind == "mongo-content-revision" || usesImportedTODOContentStageProof(record)
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
	if err := m.exactTODORelease(ctx, companyID, project, record); err != nil {
		return TODOActionResponse{}, err
	}
	var proof TODOProofReference
	if request.ProjectID == "boora" && record.ID == "t-b9e3ea9e3a616b01" && record.Stage == "Completed" {
		if len(request.ProofReferences) != 0 {
			return TODOActionResponse{}, todoTeach("proof_refs", "the production reader supplies this existing Completed proof; caller-authored references are not accepted", ErrConflict)
		}
		modulePath, claim, claimErr := productionModulePath(record)
		if claimErr != nil {
			return TODOActionResponse{}, claimErr
		}
		proof, err = m.verifyProductionCommit(ctx, companyID, record, modulePath, claim)
		if err != nil {
			return TODOActionResponse{}, err
		}
	} else if record.Stage == "In-Progress" && record.StageVerification == "unverified" &&
		(record.Source.Kind == "mongo-content-revision" || usesImportedTODOContentStageProof(record)) {
		if record.ReleaseID == "" || record.Position == nil {
			return TODOActionResponse{}, todoTeach("release_id/position", "attach this In-Progress TODO to its same-Project Release before verifying its current stage", ErrConflict)
		}
		company, companyErr := m.company(ctx, companyID)
		if companyErr != nil {
			return TODOActionResponse{}, companyErr
		}
		database := m.client.Database(company.DatabaseName)
		contentRevision, contentDigest, contentRef, hasCurrentContent, contentErr := currentTODOContentSubject(ctx, database, record)
		if contentErr != nil {
			return TODOActionResponse{}, contentErr
		}
		if !hasCurrentContent {
			return TODOActionResponse{}, todoTeach("content_revision", "revalidate only against the current immutable TODO content revision", ErrConflict)
		}
		content, contentErr := m.contentAtRevision(ctx, database, request.ProjectID, request.TODOID, contentRevision)
		if contentErr != nil {
			return TODOActionResponse{}, contentErr
		}
		proof, err = currentContentOwnerApprovalProof(record, request, content, contentRevision, contentDigest, contentRef)
		if err != nil {
			return TODOActionResponse{}, err
		}
	} else {
		return TODOActionResponse{}, todoTeach("verify-current-stage", "verify only the existing governed production Completed proof or an unverified In-Progress TODO with current immutable approved content", ErrConflict)
	}
	company, err := m.company(ctx, companyID)
	if err != nil {
		return TODOActionResponse{}, err
	}
	return writeVerifiedTODO(ctx, m.client.Database(company.DatabaseName), collection, companyID, record, record.Stage, proof, request.OperationID, "verify-current-stage", digest)
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

func writeVerifiedTODO(ctx context.Context, database *mongo.Database, collection *mongo.Collection, companyID string, record TODORecord, stage string, proof TODOProofReference, operationID, kind, digest string) (TODOActionResponse, error) {
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
		event := TODOHistoryEvent{ObjectID: bson.NewObjectID(), ProjectID: record.ProjectID, TODOID: record.ID, Revision: nextRevision, Kind: "state_changed",
			OccurredAt: time.Now().UTC(), OperationID: operationID, RequestDigest: digest, Changes: changes}
		if _, err := database.Collection(todoEventsCollection).InsertOne(txCtx, event); err != nil {
			return nil, normalizeMongoError(err)
		}
		updated = true
		return nil, nil
	})
	if err != nil {
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
	if err := validateStoredTODO(record); err != nil {
		return TODORecord{}, err
	}
	return record, nil
}

func validateStoredTODO(record TODORecord) error {
	if contract, selected := selectedTODOContract(record.ID); selected {
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
	if record.LastOperation != nil && (!todoOperationIDPattern.MatchString(record.LastOperation.ID) || !validTODOOperationKind(record.LastOperation.Kind) || !validSourceDigest(record.LastOperation.RequestDigest) || record.LastOperation.ResultRevision < 1 || record.LastOperation.ResultRevision > record.Revision) {
		return todoTeach("last_operation", "contain a valid latest idempotency marker at or before the current revision", ErrConflict)
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
	if record.LastOperation != nil && (!todoOperationIDPattern.MatchString(record.LastOperation.ID) || !validTODOOperationKind(record.LastOperation.Kind) || !validSourceDigest(record.LastOperation.RequestDigest) || record.LastOperation.ResultRevision < 1 || record.LastOperation.ResultRevision > record.Revision) {
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
		return nil, todoTeach(field, "contain at most 64 unverified references", ErrInvalid)
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
	return strings.TrimSpace(proof.Kind) != "" && strings.TrimSpace(proof.Source) != "" && strings.TrimSpace(proof.Reference) != "" && validSourceDigest(proof.SourceDigest)
}

func (m *MongoStore) roadmapTODOs(ctx context.Context, collection *mongo.Collection, projectID string, releases []roadmapRelease) ([]roadmapTODOView, error) {
	cursor, err := collection.Find(ctx, bson.D{{Key: "project_id", Value: projectID}}, options.Find().SetSort(bson.D{{Key: "position", Value: 1}, {Key: "id", Value: 1}}))
	if err != nil {
		return nil, ErrUnavailable
	}
	defer cursor.Close(ctx)
	releaseIDs := make(map[string]bool, len(releases))
	for _, release := range releases {
		releaseIDs[release.ID] = true
	}
	result := make([]roadmapTODOView, 0)
	for cursor.Next(ctx) {
		var record TODORecord
		if cursor.Decode(&record) != nil {
			return nil, ErrUnavailable
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
	if cursor.Err() != nil {
		return nil, ErrUnavailable
	}
	return result, nil
}

func (m *MongoStore) exactTODORelease(ctx context.Context, companyID string, project Project, todo TODORecord) error {
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
