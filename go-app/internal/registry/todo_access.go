package registry

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"unicode/utf8"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

var todoOperationIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

type TODOReadResponse struct {
	CompanyID           string     `json:"company_id"`
	ProjectID           string     `json:"project_id"`
	SnapshotFingerprint string     `json:"snapshot_fingerprint"`
	TODO                TODORecord `json:"todo"`
	TODORevision        int64      `json:"todo_revision"`
}

type TODOActionRequest struct {
	ProjectID        string               `json:"project_id"`
	TODOID           string               `json:"todo_id"`
	ExpectedRevision *int64               `json:"expected_revision"`
	OperationID      string               `json:"operation_id"`
	RequestedStage   string               `json:"requested_stage"`
	ProofReferences  []TODOProofReference `json:"proof_refs"`
	WaiverReferences []TODOProofReference `json:"waiver_refs"`
}

type VerifyCurrentTODOStageRequest struct {
	ProjectID        string `json:"project_id"`
	TODOID           string `json:"todo_id"`
	ExpectedRevision *int64 `json:"expected_revision"`
	OperationID      string `json:"operation_id"`
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
	return TODOReadResponse{CompanyID: companyID, ProjectID: projectID, SnapshotFingerprint: project.SnapshotFingerprint, TODO: record, TODORevision: record.Revision}, nil
}

func (m *MongoStore) TransitionTODO(ctx context.Context, companyID string, request TODOActionRequest) (TODOActionResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	if err := validateTODOAction(request); err != nil {
		return TODOActionResponse{}, err
	}
	_, collection, err := m.selectedTODOCollection(ctx, companyID, request.ProjectID, request.TODOID)
	if err != nil {
		return TODOActionResponse{}, err
	}
	record, err := readSelectedTODO(ctx, collection, request.ProjectID, request.TODOID)
	if err != nil {
		return TODOActionResponse{}, err
	}
	if record.Revision != *request.ExpectedRevision {
		return TODOActionResponse{}, &todoRevisionConflict{Revision: record.Revision}
	}
	return TODOActionResponse{}, todoTeach("requested_stage", "require a deterministic Go proof guard defined and approved in a later TODO; this operation is unavailable and no write occurred", ErrConflict)
}

func (m *MongoStore) VerifyCurrentTODOStage(ctx context.Context, companyID string, request VerifyCurrentTODOStageRequest) (TODOActionResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	if !validID(request.ProjectID) || !todoOperationIDPattern.MatchString(request.OperationID) || request.ExpectedRevision == nil || *request.ExpectedRevision < 0 || *request.ExpectedRevision == math.MaxInt64 {
		return TODOActionResponse{}, todoTeach("expected_revision/operation_id", "provide the current revision and stable verifier operation ID", ErrInvalid)
	}
	_, collection, err := m.selectedTODOCollection(ctx, companyID, request.ProjectID, request.TODOID)
	if err != nil {
		return TODOActionResponse{}, err
	}
	record, err := readSelectedTODO(ctx, collection, request.ProjectID, request.TODOID)
	if err != nil {
		return TODOActionResponse{}, err
	}
	if record.Revision != *request.ExpectedRevision {
		return TODOActionResponse{}, &todoRevisionConflict{Revision: record.Revision}
	}
	return TODOActionResponse{}, todoTeach("verify-current-stage", "require a deterministic Go proof guard defined and approved in a later TODO; this operation is unavailable and no write occurred", ErrConflict)
}

func (m *MongoStore) selectedTODOCollection(ctx context.Context, companyID, projectID, todoID string) (Project, *mongo.Collection, error) {
	contract, selected := selectedTODOSources[todoID]
	if !selected || !validID(companyID) || !validID(projectID) || contract.ProjectID != projectID {
		return Project{}, nil, todoTeach("company_id/project_id/todo_id", "name one of the four migrated TODOs under its pinned Project", ErrNotFound)
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
	if err := validateStoredSelectedTODO(record, selectedTODOSources[todoID]); err != nil {
		return TODORecord{}, err
	}
	return record, nil
}

func validateStoredSelectedTODO(record TODORecord, contract selectedTODOSourceContract) error {
	if record.ObjectID.IsZero() || record.ID == "" || record.ProjectID != contract.ProjectID || record.ReleaseID != contract.ReleaseID || record.Position == nil || *record.Position != contract.Position ||
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
	if record.LastOperation != nil && (!todoOperationIDPattern.MatchString(record.LastOperation.ID) || (record.LastOperation.Kind != "transition" && record.LastOperation.Kind != "verify-current-stage") || !validSourceDigest(record.LastOperation.RequestDigest) || record.LastOperation.ResultRevision < 1 || record.LastOperation.ResultRevision > record.Revision) {
		return todoTeach("last_operation", "contain a valid latest idempotency marker at or before the current revision", ErrConflict)
	}
	return nil
}

func validateTODOAction(request TODOActionRequest) error {
	if !validID(request.ProjectID) || !todoOperationIDPattern.MatchString(request.OperationID) || request.ExpectedRevision == nil || *request.ExpectedRevision < 0 || *request.ExpectedRevision == math.MaxInt64 || !containsTODOString(canonicalTODOStages, request.RequestedStage) {
		return todoTeach("project_id/expected_revision/operation_id/requested_stage", "provide the pinned Project, nonnegative expected_revision, stable operation_id, and canonical requested_stage", ErrInvalid)
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
	releaseByID := make(map[string]roadmapRelease, len(releases))
	for _, release := range releases {
		releaseIDs[release.ID] = true
		releaseByID[release.ID] = release
	}
	result := make([]roadmapTODOView, 0)
	for cursor.Next(ctx) {
		var record TODORecord
		if cursor.Decode(&record) != nil {
			return nil, ErrUnavailable
		}
		contract, ok := selectedTODOSources[record.ID]
		if !ok || contract.ProjectID != projectID || !releaseIDs[record.ReleaseID] {
			return nil, todoTeach("todos[].project_id/release_id", "match an admitted migrated TODO and Release in this Project", ErrConflict)
		}
		if releaseByID[record.ReleaseID].EpicID != contract.EpicID {
			return nil, todoTeach("todos[].release_id/epic_id", "match the pinned same-Project Epic and Release relationship", ErrConflict)
		}
		if err := validateStoredSelectedTODO(record, contract); err != nil {
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
	company, err := m.company(ctx, companyID)
	if err != nil {
		return err
	}
	var release roadmapRelease
	err = m.client.Database(company.DatabaseName).Collection(releasesCollection).FindOne(ctx, bson.D{{Key: "project_id", Value: project.ID}, {Key: "id", Value: todo.ReleaseID}}).Decode(&release)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return todoTeach("release_id", "reference an existing Release in this Project", ErrConflict)
		}
		return ErrUnavailable
	}
	contract := selectedTODOSources[todo.ID]
	if release.StorageID.IsZero() || release.ProjectID != project.ID || release.ID != todo.ReleaseID || release.EpicID != contract.EpicID {
		return todoTeach("release_id/epic_id", "reference the exact pinned Release under Epic "+contract.EpicID+" in this Project", ErrConflict)
	}
	var epic roadmapEpic
	if err := m.client.Database(company.DatabaseName).Collection(epicsCollection).FindOne(ctx, bson.D{{Key: "project_id", Value: project.ID}, {Key: "id", Value: contract.EpicID}}).Decode(&epic); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return todoTeach("release_id/epic_id", "belong to an existing Epic in the same Project", ErrConflict)
		}
		return ErrUnavailable
	}
	if epic.StorageID.IsZero() || epic.ProjectID != project.ID || epic.ID != contract.EpicID {
		return todoTeach("release_id/epic_id", "belong to an existing Epic in the same Project", ErrConflict)
	}
	return nil
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
