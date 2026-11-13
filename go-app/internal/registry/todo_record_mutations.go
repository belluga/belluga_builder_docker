package registry

import (
	"context"
	"errors"
	"math"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

type TODOEvidenceEditRequest struct {
	ProjectID             string                `json:"project_id"`
	TODOID                string                `json:"todo_id"`
	ExpectedRevision      *int64                `json:"expected_revision"`
	OperationID           string                `json:"operation_id"`
	ProofReferences       []TODOProofReference  `json:"proof_refs"`
	WaiverReferences      []TODOProofReference  `json:"waiver_refs"`
	ImplementationCommits []TODOCommitReference `json:"implementation_commits"`
	RecordSections        []TODOSection         `json:"record_sections"`
	Reason                string                `json:"reason"`
}

type TODONextStepEditRequest struct {
	ProjectID        string `json:"project_id"`
	TODOID           string `json:"todo_id"`
	ExpectedRevision *int64 `json:"expected_revision"`
	OperationID      string `json:"operation_id"`
	NextExactStep    string `json:"next_exact_step"`
	Reason           string `json:"reason"`
}

type TODOPlacementEditRequest struct {
	ProjectID               string `json:"project_id"`
	TODOID                  string `json:"todo_id"`
	ExpectedRevision        *int64 `json:"expected_revision"`
	ExpectedRoadmapRevision *int64 `json:"expected_roadmap_revision"`
	OperationID             string `json:"operation_id"`
	Action                  string `json:"action,omitempty"`
	Detach                  bool   `json:"detach,omitempty"`
	ReleaseID               string `json:"release_id"`
	Position                int    `json:"position"`
	Reason                  string `json:"reason"`
}

type TODOSectionOwnershipRequest struct {
	ProjectID        string        `json:"project_id"`
	TODOID           string        `json:"todo_id"`
	ExpectedRevision *int64        `json:"expected_revision"`
	OperationID      string        `json:"operation_id"`
	Sections         []TODOSection `json:"sections"`
	Reason           string        `json:"reason"`
}

// NormalizeTODOSectionOwnership records an explicit classification as a new
// content revision. It never edits a baseline or changes immutable provenance.
func (m *MongoStore) NormalizeTODOSectionOwnership(ctx context.Context, companyID string, request TODOSectionOwnershipRequest) (TODOContentEditResponse, error) {
	if !validRecordMutationIdentity(request.ProjectID, request.TODOID, request.ExpectedRevision, request.OperationID, request.Reason) || request.Sections == nil || len(request.Sections) > maxTODOSections {
		return TODOContentEditResponse{}, todoTeach("sections", "provide a complete ordered section classification with current revision and operation ID", ErrInvalid)
	}
	project, collection, err := m.selectedTODOCollection(ctx, companyID, request.ProjectID, request.TODOID)
	if err != nil {
		return TODOContentEditResponse{}, err
	}
	record, err := readSelectedTODO(ctx, collection, request.ProjectID, request.TODOID)
	if err != nil {
		return TODOContentEditResponse{}, err
	}
	digest, err := todoRequestDigest(request)
	if err != nil {
		return TODOContentEditResponse{}, ErrInvalid
	}
	database, err := m.todoTenantDatabase(ctx, companyID)
	if err != nil {
		return TODOContentEditResponse{}, err
	}
	var prior TODOHistoryEvent
	findErr := database.Collection(todoEventsCollection).FindOne(ctx, bson.D{{Key: "project_id", Value: request.ProjectID}, {Key: "todo_id", Value: request.TODOID}, {Key: "operation_id", Value: request.OperationID}}).Decode(&prior)
	if findErr == nil {
		if prior.Kind != "content_changed" || prior.RequestDigest != digest {
			return TODOContentEditResponse{}, todoTeach("operation_id", "not be reused with a different section classification", ErrConflict)
		}
		return TODOContentEditResponse{CompanyID: companyID, ProjectID: request.ProjectID, TODOID: request.TODOID, Revision: prior.Revision, NoOp: true}, nil
	}
	if !errors.Is(findErr, mongo.ErrNoDocuments) {
		return TODOContentEditResponse{}, ErrUnavailable
	}
	if err := requireActiveTODO(record); err != nil {
		return TODOContentEditResponse{}, err
	}
	if record.Revision != *request.ExpectedRevision {
		return TODOContentEditResponse{}, &todoRevisionConflict{Revision: record.Revision}
	}
	if record.Content != nil {
		if err := requireNoTODOContentDowngrade(record.Content.SchemaVersion, 1); err != nil {
			return TODOContentEditResponse{}, err
		}
	}
	if err := m.exactTODORelease(ctx, companyID, project, record); err != nil {
		return TODOContentEditResponse{}, err
	}
	if err := requireTODOHistoryBaseline(ctx, database, record); err != nil {
		return TODOContentEditResponse{}, err
	}
	if err := validateSectionOwnershipNormalization(record.Sections, request.Sections); err != nil {
		return TODOContentEditResponse{}, err
	}
	content := contentFromTODO(TODORecord{Title: record.Title, Objective: record.Objective, Sections: request.Sections, DiffExpectation: record.DiffExpectation, AffectedFiles: record.AffectedFiles})
	if err := validateAuthoredContent(content); err != nil {
		return TODOContentEditResponse{}, err
	}
	next := record.Revision + 1
	newSections := append([]TODOSection{}, request.Sections...)
	for i := range newSections {
		newSections[i].Position = i
	}
	stateSections := make([]TODOSection, 0)
	for _, section := range newSections {
		if section.Ownership == "record" {
			stateSections = append(stateSections, section)
		}
	}
	operation := TODOOperationState{ID: request.OperationID, Kind: "content-edit", RequestDigest: digest, ResultRevision: next}
	set := bson.D{{Key: "sections", Value: newSections}, {Key: "last_operation", Value: operation}, {Key: "updated_at", Value: bson.DateTime(time.Now().UTC().UnixMilli())}}
	persisted, err := persistedTODORecordBSON(ctx, collection, record)
	if err != nil {
		return TODOContentEditResponse{}, err
	}
	if err := validateTODORecordMutationSize(persisted, set, nil); err != nil {
		return TODOContentEditResponse{}, err
	}
	update := bson.D{{Key: "$set", Value: set}, {Key: "$inc", Value: bson.D{{Key: "revision", Value: 1}}}}
	session, err := m.client.StartSession()
	if err != nil {
		return TODOContentEditResponse{}, ErrUnavailable
	}
	defer session.EndSession(ctx)
	_, err = session.WithTransaction(ctx, func(txCtx context.Context) (any, error) {
		result, err := collection.UpdateOne(txCtx, bson.D{{Key: "_id", Value: record.ObjectID}, {Key: "project_id", Value: request.ProjectID}, {Key: "id", Value: request.TODOID}, {Key: "revision", Value: record.Revision}}, update)
		if err != nil {
			return nil, normalizeMongoError(err)
		}
		if result.MatchedCount != 1 {
			return nil, &todoRevisionConflict{Revision: next}
		}
		contentDigest, digestErr := todoContentRevisionDigest(request.ProjectID, request.TODOID, next, content)
		if digestErr != nil {
			return nil, ErrInvalid
		}
		version := TODOContentVersion{ObjectID: bson.NewObjectID(), ProjectID: request.ProjectID, TODOID: request.TODOID, Revision: next, Digest: contentDigest, Content: content}
		if _, err := database.Collection(todoContentVersionsCollection).InsertOne(txCtx, version); err != nil {
			return nil, normalizeMongoError(err)
		}
		contentRevision := next
		changes := TODOHistoryChanges{RecordSections: &stateSections}
		event := TODOHistoryEvent{ObjectID: bson.NewObjectID(), ProjectID: request.ProjectID, TODOID: request.TODOID, Revision: next, Kind: "content_changed", OccurredAt: time.Now().UTC(), OperationID: request.OperationID, RequestDigest: digest, Changes: changes, ContentRevision: &contentRevision, Reason: request.Reason}
		if _, err := database.Collection(todoEventsCollection).InsertOne(txCtx, event); err != nil {
			return nil, normalizeMongoError(err)
		}
		return nil, nil
	})
	if err != nil {
		return TODOContentEditResponse{}, normalizeMongoError(err)
	}
	return TODOContentEditResponse{CompanyID: companyID, ProjectID: request.ProjectID, TODOID: request.TODOID, Revision: next}, nil
}

func validateSectionOwnershipNormalization(current, replacement []TODOSection) error {
	want := make([]TODOSection, 0, len(current))
	for _, section := range current {
		if !historyRenderOnlySection(section.Key, section.Title) {
			want = append(want, section)
		}
	}
	if len(want) != len(replacement) {
		return todoTeach("sections", "preserve every retained section and remove only known render-only legacy mirrors", ErrConflict)
	}
	seen := map[string]bool{}
	for i, section := range replacement {
		prior := want[i]
		if section.Key != prior.Key || section.Title != prior.Title || section.BodyMarkdown != prior.BodyMarkdown || section.Position != i || (section.Ownership != "contract" && section.Ownership != "record") || seen[section.Title] {
			return todoTeach("sections["+strconv.Itoa(i)+"].ownership", "preserve exact ordered source section text and classify it contract or record", ErrConflict)
		}
		seen[section.Title] = true
	}
	return nil
}

func (m *MongoStore) EditTODOEvidence(ctx context.Context, companyID string, request TODOEvidenceEditRequest) (TODOContentEditResponse, error) {
	if !validRecordMutationIdentity(request.ProjectID, request.TODOID, request.ExpectedRevision, request.OperationID, request.Reason) || request.ProofReferences == nil || request.WaiverReferences == nil || request.ImplementationCommits == nil || request.RecordSections == nil {
		return TODOContentEditResponse{}, todoTeach("evidence", "provide valid identity, revision, operation, reason, and non-null typed evidence arrays", ErrInvalid)
	}
	proofs := append([]TODOProofReference{}, request.ProofReferences...)
	for _, proof := range proofs {
		field := "proof_refs." + proof.Kind
		switch {
		case strings.TrimSpace(proof.Kind) == "":
			return TODOContentEditResponse{}, todoTeach("proof_refs.kind", "provide the exact evidence kind", ErrInvalid)
		case strings.TrimSpace(proof.Source) == "":
			return TODOContentEditResponse{}, todoTeach(field+".source", "provide the exact evidence source; new criterion and scenario citations use external-evidence", ErrInvalid)
		case strings.TrimSpace(proof.Reference) == "":
			return TODOContentEditResponse{}, todoTeach(field+".ref", "provide a non-empty attributable evidence reference", ErrInvalid)
		case !validSourceDigest(proof.SourceDigest):
			return TODOContentEditResponse{}, todoTeach(field+".source_digest", "provide the exact current TODO content digest for new evidence", ErrInvalid)
		case !validReviewOutcome(proof.Outcome):
			return TODOContentEditResponse{}, todoTeach(field+".outcome", "provide a valid review outcome when required", ErrInvalid)
		case proof.VerificationState != "unverified" && proof.VerificationState != "verified":
			return TODOContentEditResponse{}, todoTeach(field+".verification_state", "submit new evidence as unverified; preserve existing verified references byte-for-byte", ErrInvalid)
		}
	}
	waivers := append([]TODOProofReference{}, request.WaiverReferences...)
	for _, proof := range waivers {
		if !validProofReference(proof) || (proof.VerificationState != "unverified" && proof.VerificationState != "verified") {
			return TODOContentEditResponse{}, todoTeach("waiver_refs", "contain valid source-bound references; verified entries must be preserved from current state", ErrInvalid)
		}
	}
	commits := append([]TODOCommitReference{}, request.ImplementationCommits...)
	for _, commit := range commits {
		if !validName(commit.Repository) || !validGitCommit(commit.Commit) || !validSourceDigest(commit.SourceDigest) || (commit.VerificationState != "unverified" && commit.VerificationState != "verified") {
			return TODOContentEditResponse{}, todoTeach("implementation_commits", "contain valid source-bound references; verified entries must be preserved from current state", ErrInvalid)
		}
	}
	_, collection, err := m.selectedTODOCollection(ctx, companyID, request.ProjectID, request.TODOID)
	if err != nil {
		return TODOContentEditResponse{}, err
	}
	if err := upgradeTODOCollectionForHistory(ctx, collection.Database()); err != nil {
		return TODOContentEditResponse{}, err
	}
	current, err := readSelectedTODO(ctx, collection, request.ProjectID, request.TODOID)
	if err != nil {
		return TODOContentEditResponse{}, err
	}
	if current.Content != nil && current.Content.SchemaVersion == todoContentSchemaVersion {
		for _, commit := range commits {
			if foundationScope(commit.Repository) {
				return TODOContentEditResponse{}, todoTeach("implementation_commits.repository", "omit Foundation commits from current v2 TODO evidence", ErrConflict)
			}
		}
		if (current.Stage == "Local-Implemented" || current.Stage == "Promotion-Lane" || current.Stage == "Completed") && !reflect.DeepEqual(current.ImplementationCommits, commits) {
			return TODOContentEditResponse{}, todoTeach("implementation_commits", "retain the exact commits bound to the verified local delivery", ErrConflict)
		}
	}
	if err := preserveVerifiedEvidence(current.ProofReferences, proofs, "proof_refs"); err != nil {
		return TODOContentEditResponse{}, err
	}
	if err := preserveVerifiedEvidence(current.WaiverReferences, waivers, "waiver_refs"); err != nil {
		return TODOContentEditResponse{}, err
	}
	if err := preserveVerifiedCommits(current.ImplementationCommits, commits); err != nil {
		return TODOContentEditResponse{}, err
	}
	if len(proofs)+len(waivers)+len(commits) > maxTODOProofs {
		return TODOContentEditResponse{}, todoTeach("evidence", "contain at most "+strconv.Itoa(maxTODOProofs)+" combined typed references and commits", ErrInvalid)
	}
	seen := map[string]bool{}
	sections := append([]TODOSection{}, request.RecordSections...)
	for i := range sections {
		section := &sections[i]
		if section.Ownership != "record" || section.Position != i || !validID(section.Key) || !validSummaryField(section.Title, 256) || !utf8.ValidString(section.BodyMarkdown) || len(section.BodyMarkdown) > 64<<10 || seen[section.Key] || strings.TrimSpace(section.BodyMarkdown) == "" {
			return TODOContentEditResponse{}, todoTeach("record_sections", "contain unique, bounded record-owned evidence sections", ErrInvalid)
		}
		seen[section.Key] = true
	}
	return m.writeTODORecordMutation(ctx, companyID, request.ProjectID, request.TODOID, *request.ExpectedRevision, request.OperationID, "evidence-edit", request, request.Reason,
		bson.D{{Key: "proof_refs", Value: proofs}, {Key: "waiver_refs", Value: waivers}, {Key: "implementation_commits", Value: commits}}, func(record TODORecord) (TODOHistoryChanges, error) {
			merged := make([]TODOSection, 0, len(record.Sections)+len(sections))
			for _, section := range record.Sections {
				if sectionOwnership(section) != "record" {
					merged = append(merged, section)
				}
			}
			merged = append(merged, sections...)
			for i := range merged {
				merged[i].Position = i
			}
			if len(merged) > maxTODOSections {
				return TODOHistoryChanges{}, todoTeach("record_sections", "keep the retained section list within its document bound", ErrInvalid)
			}
			stateSections := make([]TODOSection, 0, len(sections))
			for _, section := range merged {
				if sectionOwnership(section) == "record" {
					stateSections = append(stateSections, section)
				}
			}
			return TODOHistoryChanges{ProofReferences: &proofs, WaiverReferences: &waivers, ImplementationCommits: &commits, RecordSections: &stateSections}, nil
		}, func(record TODORecord) bson.D {
			// Replace record-owned sections while preserving authored/render-only sections.
			all := make([]TODOSection, 0, len(record.Sections)+len(sections))
			for _, section := range record.Sections {
				if sectionOwnership(section) != "record" {
					all = append(all, section)
				}
			}
			all = append(all, sections...)
			for i := range all {
				all[i].Position = i
			}
			return bson.D{{Key: "sections", Value: all}}
		})
}

func (m *MongoStore) EditTODOPlacement(ctx context.Context, companyID string, request TODOPlacementEditRequest) (TODOContentEditResponse, error) {
	detach := request.Detach || request.Action == "detach"
	if request.Action != "" && request.Action != "attach" && request.Action != "detach" || !validRecordMutationIdentity(request.ProjectID, request.TODOID, request.ExpectedRevision, request.OperationID, request.Reason) || request.ExpectedRoadmapRevision == nil || *request.ExpectedRoadmapRevision < 0 || *request.ExpectedRoadmapRevision == math.MaxInt64 || (!detach && (!validID(request.ReleaseID) || request.Position < 0)) || (detach && request.ReleaseID != "") {
		return TODOContentEditResponse{}, todoTeach("placement.action/release_id/position", "attach with a same-Project Release and nonnegative position, or detach with no Release ID", ErrInvalid)
	}
	return m.writeTODORecordMutation(ctx, companyID, request.ProjectID, request.TODOID, *request.ExpectedRevision, request.OperationID, "placement-edit", request, request.Reason,
		bson.D{}, func(record TODORecord) (TODOHistoryChanges, error) {
			if detach {
				return TODOHistoryChanges{ClearPlacement: true}, nil
			}
			release := request.ReleaseID
			position := request.Position
			return TODOHistoryChanges{ReleaseID: &release, Position: &position}, nil
		}, func(TODORecord) bson.D {
			if detach {
				return bson.D{}
			}
			return bson.D{{Key: "release_id", Value: request.ReleaseID}, {Key: "position", Value: request.Position}}
		})
}

func placementReopensPlanning(record TODORecord, nextRelease string, nextPosition *int) bool {
	if (record.Source.Kind != "mongo-content-revision" && (record.Content == nil || record.Content.SchemaVersion != todoContentSchemaVersion)) || record.ReleaseID == nextRelease && reflect.DeepEqual(record.Position, nextPosition) {
		return false
	}
	if record.AdministrativeAcceptance != nil {
		// Placement does not revise the content digest or revoke the owner's
		// explicit initial-state decision. Only a substantive content edit does.
		return false
	}
	switch record.Stage {
	case "Implementation-Approval-Pending", "Ready-for-Implementation", "In-Progress", "Local-Implemented", "Promotion-Lane":
		return true
	default:
		return false
	}
}

func validRecordMutationIdentity(projectID, todoID string, expected *int64, operationID, reason string) bool {
	return validID(projectID) && validID(todoID) && expected != nil && *expected >= 0 && *expected < math.MaxInt64 && todoOperationIDPattern.MatchString(operationID) && validSummaryField(reason, 512)
}

func (m *MongoStore) EditTODONextStep(ctx context.Context, companyID string, request TODONextStepEditRequest) (TODOContentEditResponse, error) {
	if !validRecordMutationIdentity(request.ProjectID, request.TODOID, request.ExpectedRevision, request.OperationID, request.Reason) || !validSummaryField(request.NextExactStep, 512) {
		return TODOContentEditResponse{}, todoTeach("next_exact_step", "provide a nonempty next step of at most 512 bytes with current TODO revision, operation ID and reason", ErrInvalid)
	}
	step := request.NextExactStep
	return m.writeTODORecordMutation(ctx, companyID, request.ProjectID, request.TODOID, *request.ExpectedRevision, request.OperationID, "next-step-edit", request, request.Reason,
		bson.D{{Key: "next_exact_step", Value: step}},
		func(TODORecord) (TODOHistoryChanges, error) { return TODOHistoryChanges{NextExactStep: &step}, nil },
		func(TODORecord) bson.D { return nil })
}

// Check the stored BSON shape after a record mutation, including the CAS
// marker and revision. Field and reference-count limits alone do not bound it.
func persistedTODORecordBSON(ctx context.Context, collection *mongo.Collection, record TODORecord) (bson.Raw, error) {
	var persisted bson.Raw
	err := collection.FindOne(ctx, bson.D{{Key: "_id", Value: record.ObjectID}, {Key: "project_id", Value: record.ProjectID}, {Key: "id", Value: record.ID}, {Key: "revision", Value: record.Revision}}).Decode(&persisted)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, &todoRevisionConflict{Revision: record.Revision + 1}
	}
	if err != nil {
		return nil, ErrUnavailable
	}
	return persisted, nil
}

// Project from persisted BSON: readSelectedTODO may overlay a shorter synthetic
// Mongo source on an imported record without changing the stored document.
func validateTODORecordMutationSize(persisted bson.Raw, set bson.D, unset []string) error {
	var projected bson.M
	if err := bson.Unmarshal(persisted, &projected); err != nil {
		return ErrUnavailable
	}
	for _, field := range set {
		projected[field.Key] = field.Value
	}
	for _, field := range unset {
		delete(projected, field)
	}
	switch revision := projected["revision"].(type) {
	case int32:
		projected["revision"] = int64(revision) + 1
	case int64:
		projected["revision"] = revision + 1
	default:
		return ErrUnavailable
	}
	encoded, err := bson.Marshal(projected)
	if err != nil {
		return ErrUnavailable
	}
	if len(encoded) > maxTODORecordBytes {
		return todoTeach("todo.record_bytes", "keep the resulting TODO record within the 1 MiB BSON limit", ErrInvalid)
	}
	return nil
}

func (m *MongoStore) writeTODORecordMutation(ctx context.Context, companyID, projectID, todoID string, expected int64, operationID, kind string, request any, reason string, fields bson.D, changes func(TODORecord) (TODOHistoryChanges, error), extra func(TODORecord) bson.D) (TODOContentEditResponse, error) {
	project, collection, err := m.selectedTODOCollection(ctx, companyID, projectID, todoID)
	if err != nil {
		return TODOContentEditResponse{}, err
	}
	record, err := readSelectedTODO(ctx, collection, projectID, todoID)
	if err != nil {
		return TODOContentEditResponse{}, err
	}
	digest, err := todoRequestDigest(request)
	if err != nil {
		return TODOContentEditResponse{}, ErrInvalid
	}
	database, err := m.todoTenantDatabase(ctx, companyID)
	if err != nil {
		return TODOContentEditResponse{}, err
	}
	eventKind := map[string]string{"evidence-edit": "evidence_changed", "placement-edit": "placement_changed", "next-step-edit": "state_changed"}[kind]
	var prior TODOHistoryEvent
	findErr := database.Collection(todoEventsCollection).FindOne(ctx, bson.D{{Key: "project_id", Value: projectID}, {Key: "todo_id", Value: todoID}, {Key: "operation_id", Value: operationID}}).Decode(&prior)
	if findErr == nil {
		if prior.Kind != eventKind || prior.RequestDigest != digest {
			return TODOContentEditResponse{}, todoTeach("operation_id", "not be reused with a different record mutation", ErrConflict)
		}
		return TODOContentEditResponse{CompanyID: companyID, ProjectID: projectID, TODOID: todoID, Revision: prior.Revision, NoOp: true}, nil
	}
	if !errors.Is(findErr, mongo.ErrNoDocuments) {
		return TODOContentEditResponse{}, ErrUnavailable
	}
	if err := requireActiveTODO(record); err != nil {
		return TODOContentEditResponse{}, err
	}
	if record.Revision != expected {
		return TODOContentEditResponse{}, &todoRevisionConflict{Revision: record.Revision}
	}
	if kind == "placement-edit" && project.RoadmapRevision != *request.(TODOPlacementEditRequest).ExpectedRoadmapRevision {
		return TODOContentEditResponse{}, todoTeach("expected_roadmap_revision", "reload the current Project Roadmap before changing TODO placement", ErrConflict)
	}
	if err := m.exactTODORelease(ctx, companyID, project, record); err != nil {
		return TODOContentEditResponse{}, err
	}
	if err := requireTODOHistoryBaseline(ctx, database, record); err != nil {
		return TODOContentEditResponse{}, err
	}
	if err := validateExistingSectionOwnership(record); err != nil {
		return TODOContentEditResponse{}, err
	}
	if kind == "placement-edit" {
		placement := request.(TODOPlacementEditRequest)
		if (placement.Detach || placement.Action == "detach") && record.ReleaseID == "" && record.Position == nil || !(placement.Detach || placement.Action == "detach") && record.ReleaseID == placement.ReleaseID && record.Position != nil && *record.Position == placement.Position {
			return TODOContentEditResponse{CompanyID: companyID, ProjectID: projectID, TODOID: todoID, Revision: record.Revision, NoOp: true}, nil
		}
		if record.Content != nil && record.Content.SchemaVersion == todoContentSchemaVersion && record.Stage == "Completed" && record.AdministrativeAcceptance == nil {
			return TODOContentEditResponse{}, todoTeach("release_id/position", "retain the completed placement; create a separately reviewed TODO for new work", ErrConflict)
		}
	}
	newChanges, err := changes(record)
	if err != nil {
		return TODOContentEditResponse{}, err
	}
	if kind == "evidence-edit" && reflect.DeepEqual(newChanges.ProofReferences, ptrProofRefs(record.ProofReferences)) && reflect.DeepEqual(newChanges.WaiverReferences, ptrProofRefs(record.WaiverReferences)) && reflect.DeepEqual(newChanges.ImplementationCommits, ptrCommitRefs(record.ImplementationCommits)) && reflect.DeepEqual(newChanges.RecordSections, ptrTODOSections(recordRecordSections(record))) {
		return TODOContentEditResponse{CompanyID: companyID, ProjectID: projectID, TODOID: todoID, Revision: record.Revision, NoOp: true}, nil
	}
	if kind == "next-step-edit" && newChanges.NextExactStep != nil && *newChanges.NextExactStep == record.NextExactStep {
		return TODOContentEditResponse{CompanyID: companyID, ProjectID: projectID, TODOID: todoID, Revision: record.Revision, NoOp: true}, nil
	}
	next := record.Revision + 1
	operation := TODOOperationState{ID: operationID, Kind: kind, RequestDigest: digest, ResultRevision: next}
	set := append(bson.D{}, fields...)
	set = append(set, extra(record)...)
	var nextRelease string
	var nextPosition *int
	if kind == "placement-edit" {
		placement := request.(TODOPlacementEditRequest)
		if record.Source.Kind != "mongo-content-revision" {
			content, contentErr := m.contentAtRevision(ctx, database, projectID, todoID, record.Revision)
			if contentErr != nil && !errors.Is(contentErr, ErrNotFound) {
				return TODOContentEditResponse{}, contentErr
			}
			if content.SchemaVersion == todoContentSchemaVersion {
				record.Content = &content.TODOContent
			}
		}
		nextRelease = placement.ReleaseID
		nextPosition = &placement.Position
		if placement.Detach || placement.Action == "detach" {
			nextRelease = ""
			nextPosition = nil
		}
		if record.Content != nil && record.Content.SchemaVersion == todoContentSchemaVersion && record.AdministrativeAcceptance == nil {
			retained := make([]TODOProofReference, 0, len(record.ProofReferences))
			for _, ref := range record.ProofReferences {
				if ref.Source != "human-review" || !isTODOAuditDeliveryKind(ref.Kind) && !strings.HasPrefix(ref.Kind, "planning_review_") {
					retained = append(retained, ref)
				}
			}
			set = append(set, bson.E{Key: "proof_refs", Value: retained})
			newChanges.ProofReferences = &retained
		}
		if placementReopensPlanning(record, nextRelease, nextPosition) {
			_, contentDigest, contentRef, available, subjectErr := currentTODOContentSubject(ctx, database, record)
			if subjectErr != nil {
				return TODOContentEditResponse{}, subjectErr
			}
			if !available || !validSourceDigest(contentDigest) {
				return TODOContentEditResponse{}, todoTeach("content_revision", "resolve the current immutable Mongo content before changing approved Release membership", ErrConflict)
			}
			planning, verified, work := "Planning", "verified", "planning"
			proof := TODOProofReference{Kind: "planning-stage", Source: "mongo-content-revision", Reference: contentRef, SourceDigest: contentDigest, VerificationState: verified}
			set = append(set, bson.E{Key: "stage", Value: planning}, bson.E{Key: "stage_verification", Value: verified}, bson.E{Key: "work_state", Value: work}, bson.E{Key: "verified_transition_ref", Value: proof})
			if record.AdministrativeAcceptance != nil {
				newChanges.ClearAdministrativeAcceptance = true
			}
			newChanges.Stage, newChanges.StageVerification, newChanges.WorkState, newChanges.VerifiedTransitionRef = &planning, &verified, &work, &proof
		}
	}
	set = append(set, bson.E{Key: "last_operation", Value: operation}, bson.E{Key: "updated_at", Value: bson.DateTime(time.Now().UTC().UnixMilli())})
	update := bson.D{{Key: "$set", Value: set}, {Key: "$inc", Value: bson.D{{Key: "revision", Value: 1}}}}
	var unset []string
	if kind == "placement-edit" {
		placement := request.(TODOPlacementEditRequest)
		if placement.Detach || placement.Action == "detach" {
			update = append(update, bson.E{Key: "$unset", Value: bson.D{{Key: "release_id", Value: ""}, {Key: "position", Value: ""}}})
			unset = []string{"release_id", "position"}
		}
		if placementReopensPlanning(record, nextRelease, nextPosition) && record.AdministrativeAcceptance != nil {
			update = append(update, bson.E{Key: "$unset", Value: bson.D{{Key: "administrative_acceptance", Value: ""}}})
			unset = append(unset, "administrative_acceptance")
		}
	}
	persisted, err := persistedTODORecordBSON(ctx, collection, record)
	if err != nil {
		return TODOContentEditResponse{}, err
	}
	if err := validateTODORecordMutationSize(persisted, set, unset); err != nil {
		return TODOContentEditResponse{}, err
	}
	session, err := m.client.StartSession()
	if err != nil {
		return TODOContentEditResponse{}, ErrUnavailable
	}
	defer session.EndSession(ctx)
	_, err = session.WithTransaction(ctx, func(txCtx context.Context) (any, error) {
		if kind == "placement-edit" {
			placement := request.(TODOPlacementEditRequest)
			if err := bumpRoadmapRevision(txCtx, database, companyID, projectID, *placement.ExpectedRoadmapRevision); err != nil {
				return nil, err
			}
			if !placement.Detach && placement.Action != "detach" {
				if err := validateTODOReleaseMembership(txCtx, database, companyID, projectID, placement.ReleaseID); err != nil {
					return nil, err
				}
			}
		}
		result, err := collection.UpdateOne(txCtx, bson.D{{Key: "_id", Value: record.ObjectID}, {Key: "project_id", Value: projectID}, {Key: "id", Value: todoID}, {Key: "revision", Value: record.Revision}}, update)
		if err != nil {
			return nil, normalizeMongoError(err)
		}
		if result.MatchedCount != 1 {
			return nil, &todoRevisionConflict{Revision: next}
		}
		event := TODOHistoryEvent{ObjectID: bson.NewObjectID(), ProjectID: projectID, TODOID: todoID, Revision: next, Kind: eventKind, OccurredAt: time.Now().UTC(), OperationID: operationID, RequestDigest: digest, Changes: newChanges, Reason: reason}
		if _, err := database.Collection(todoEventsCollection).InsertOne(txCtx, event); err != nil {
			return nil, normalizeMongoError(err)
		}
		return nil, nil
	})
	if err != nil {
		return TODOContentEditResponse{}, normalizeMongoError(err)
	}
	return TODOContentEditResponse{CompanyID: companyID, ProjectID: projectID, TODOID: todoID, Revision: next}, nil
}

func ptrProofRefs(value []TODOProofReference) *[]TODOProofReference {
	v := append([]TODOProofReference{}, value...)
	return &v
}
func ptrCommitRefs(value []TODOCommitReference) *[]TODOCommitReference {
	v := append([]TODOCommitReference{}, value...)
	return &v
}
func ptrTODOSections(value []TODOSection) *[]TODOSection {
	v := append([]TODOSection{}, value...)
	return &v
}
func recordRecordSections(record TODORecord) []TODOSection {
	v := []TODOSection{}
	for _, s := range record.Sections {
		if sectionOwnership(s) == "record" {
			v = append(v, s)
		}
	}
	return v
}

func preserveVerifiedEvidence(current, replacement []TODOProofReference, field string) error {
	for _, old := range current {
		if old.VerificationState == "verified" {
			found := false
			for _, next := range replacement {
				if reflect.DeepEqual(old, next) {
					found = true
					break
				}
			}
			if !found {
				return todoTeach(field, "preserve every previously verified reference byte-for-byte", ErrConflict)
			}
		}
	}
	for _, next := range replacement {
		if next.VerificationState == "verified" {
			found := false
			for _, old := range current {
				if old.VerificationState == "verified" && reflect.DeepEqual(old, next) {
					found = true
					break
				}
			}
			if !found {
				return todoTeach(field, "not assert verification for a new or changed reference", ErrConflict)
			}
		}
	}
	return nil
}

func preserveVerifiedCommits(current, replacement []TODOCommitReference) error {
	for _, old := range current {
		if old.VerificationState == "verified" {
			found := false
			for _, next := range replacement {
				if reflect.DeepEqual(old, next) {
					found = true
					break
				}
			}
			if !found {
				return todoTeach("implementation_commits", "preserve every previously verified commit reference byte-for-byte", ErrConflict)
			}
		}
	}
	for _, next := range replacement {
		if next.VerificationState == "verified" {
			found := false
			for _, old := range current {
				if old.VerificationState == "verified" && reflect.DeepEqual(old, next) {
					found = true
					break
				}
			}
			if !found {
				return todoTeach("implementation_commits", "not assert verification for a new or changed commit reference", ErrConflict)
			}
		}
	}
	return nil
}
