package registry

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestSectionOwnershipNormalizationRequiresExactTextAndExplicitClassification(t *testing.T) {
	legacy := []TODOSection{
		{Key: "contract-boundary", Title: "Contract Boundary", BodyMarkdown: "Keep this authored contract.", Position: 0},
		{Key: "delivery-status-canon", Title: "Delivery Status Canon", BodyMarkdown: "Planning", Position: 1},
		{Key: "execution-checkpoint-2026-10-31", Title: "Execution checkpoint — 2026-10-31", BodyMarkdown: "Observed operation record.", Position: 2},
	}
	classified := []TODOSection{
		{Key: "contract-boundary", Title: "Contract Boundary", BodyMarkdown: "Keep this authored contract.", Position: 0, Ownership: "contract"},
		{Key: "execution-checkpoint-2026-10-31", Title: "Execution checkpoint — 2026-10-31", BodyMarkdown: "Observed operation record.", Position: 1, Ownership: "record"},
	}
	if err := validateSectionOwnershipNormalization(legacy, classified); err != nil {
		t.Fatalf("complete explicit normalization rejected: %v", err)
	}
	changedBody := append([]TODOSection{}, classified...)
	changedBody[0].BodyMarkdown = "rewritten"
	if err := validateSectionOwnershipNormalization(legacy, changedBody); err == nil {
		t.Fatal("normalization accepted a body rewrite")
	}
	missingOwner := append([]TODOSection{}, classified...)
	missingOwner[1].Ownership = ""
	if err := validateSectionOwnershipNormalization(legacy, missingOwner); err == nil {
		t.Fatal("normalization accepted a missing ownership")
	}
}

func TestMongoPlacementReopensOnlyUnfinishedApprovalOnPlacementChange(t *testing.T) {
	for _, tc := range []struct {
		stage, currentRelease, nextRelease string
		want                               bool
	}{
		{"Implementation-Approval-Pending", "r-09", "r-10", true},
		{"Ready-for-Implementation", "r-09", "r-10", true},
		{"In-Progress", "r-09", "r-10", true},
		{"In-Progress", "r-09", "r-09", false},
		{"Planning", "r-09", "r-10", false},
		{"Local-Implemented", "r-09", "r-10", true},
		{"Promotion-Lane", "r-09", "r-10", true},
		{"Completed", "r-09", "r-10", false},
	} {
		record := TODORecord{Stage: tc.stage, ReleaseID: tc.currentRelease, Source: TODOSource{Kind: "mongo-content-revision"}}
		if got := placementReopensPlanning(record, tc.nextRelease, nil); got != tc.want {
			t.Errorf("%s %s → %s: got %t, want %t", tc.stage, tc.currentRelease, tc.nextRelease, got, tc.want)
		}
		record.Source.Kind = ""
		if placementReopensPlanning(record, tc.nextRelease, nil) {
			t.Error("historical Foundation stage unexpectedly reopened")
		}
	}
	record := TODORecord{Stage: "In-Progress", ReleaseID: "r-09", Position: intPointer(1), Source: TODOSource{Kind: "mongo-content-revision"}}
	if !placementReopensPlanning(record, "r-09", intPointer(2)) {
		t.Fatal("changing position retained approval for the old placement")
	}
}

func TestEvidenceEditRejectsOversizedResultingBSONRecord(t *testing.T) {
	record := mongoBornTODOFixture()
	operation := TODOOperationState{ID: "evidence-1", Kind: "evidence-edit", RequestDigest: "sha256:" + strings.Repeat("a", 64), ResultRevision: 1}
	fields := bson.D{{Key: "last_operation", Value: operation}, {Key: "updated_at", Value: bson.DateTime(time.Now().UTC().UnixMilli())}}
	persisted, err := bson.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateTODORecordMutationSize(persisted, fields, nil); err != nil {
		t.Fatalf("bounded evidence edit rejected: %v", err)
	}
	oversized := append(bson.D{}, fields...)
	oversized = append(oversized, bson.E{Key: "proof_refs", Value: []TODOProofReference{{Kind: "criterion:large", Source: "paced-local-runner", Reference: strings.Repeat("x", maxTODORecordBytes), SourceDigest: "sha256:" + strings.Repeat("a", 64), VerificationState: "unverified"}}})
	if err := validateTODORecordMutationSize(persisted, oversized, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversized resulting BSON record accepted: %v", err)
	}
}

func TestNextStepCannotGrowNearLimitTODORecordPastBSONCap(t *testing.T) {
	record := mongoBornTODOFixture()
	record.ProofReferences = []TODOProofReference{{Kind: "criterion:large", Source: "paced-local-runner", Reference: "x", SourceDigest: "sha256:" + strings.Repeat("a", 64), VerificationState: "unverified"}}
	encoded, err := bson.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	record.ProofReferences[0].Reference = strings.Repeat("x", maxTODORecordBytes-len(encoded)-64+1)
	encoded, err = bson.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	set := bson.D{{Key: "next_exact_step", Value: strings.Repeat("n", 512)}, {Key: "last_operation", Value: TODOOperationState{ID: "next-step-1", Kind: "next-step-edit", RequestDigest: "sha256:" + strings.Repeat("a", 64), ResultRevision: 1}}, {Key: "updated_at", Value: bson.DateTime(time.Now().UTC().UnixMilli())}}
	if err := validateTODORecordMutationSize(encoded, set, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("next-step growth past the record cap accepted: %v", err)
	}
}

func TestSectionOwnershipCannotGrowNearLimitTODORecordPastBSONCap(t *testing.T) {
	record := mongoBornTODOFixture()
	record.ProofReferences = []TODOProofReference{{Kind: "criterion:large", Source: "paced-local-runner", Reference: "x", SourceDigest: "sha256:" + strings.Repeat("a", 64), VerificationState: "unverified"}}
	encoded, err := bson.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	record.ProofReferences[0].Reference = strings.Repeat("x", maxTODORecordBytes-len(encoded)-64+1)
	encoded, err = bson.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	set := bson.D{{Key: "sections", Value: []TODOSection{{Key: "scope", Title: "Scope", Ownership: "contract", BodyMarkdown: strings.Repeat("s", 512)}}}, {Key: "last_operation", Value: TODOOperationState{ID: "classify-1", Kind: "content-edit", RequestDigest: "sha256:" + strings.Repeat("a", 64), ResultRevision: 1}}, {Key: "updated_at", Value: bson.DateTime(time.Now().UTC().UnixMilli())}}
	if err := validateTODORecordMutationSize(encoded, set, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("section ownership growth past the record cap accepted: %v", err)
	}
}

func TestRecordCapAcceptsBSONInt32Revision(t *testing.T) {
	persisted, err := bson.Marshal(bson.D{{Key: "revision", Value: int32(0)}, {Key: "source", Value: bson.D{{Key: "kind", Value: "foundation-import"}, {Key: "path", Value: "legacy.md"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := validateTODORecordMutationSize(persisted, bson.D{{Key: "next_exact_step", Value: "Review"}}, nil); err != nil {
		t.Fatalf("valid BSON int32 revision rejected: %v", err)
	}
}

type recordMutationHTTPService struct {
	*testService
	called  string
	company string
	project string
	todo    string
}

func (s *recordMutationHTTPService) EditTODOEvidence(_ context.Context, company string, input TODOEvidenceEditRequest) (TODOContentEditResponse, error) {
	s.called, s.company, s.project, s.todo = "evidence", company, input.ProjectID, input.TODOID
	return TODOContentEditResponse{CompanyID: company, ProjectID: input.ProjectID, TODOID: input.TODOID, Revision: *input.ExpectedRevision + 1}, nil
}
func (s *recordMutationHTTPService) EditTODOPlacement(_ context.Context, company string, input TODOPlacementEditRequest) (TODOContentEditResponse, error) {
	s.called, s.company, s.project, s.todo = "placement", company, input.ProjectID, input.TODOID
	return TODOContentEditResponse{CompanyID: company, ProjectID: input.ProjectID, TODOID: input.TODOID, Revision: *input.ExpectedRevision + 1}, nil
}
func (s *recordMutationHTTPService) EditTODONextStep(_ context.Context, company string, input TODONextStepEditRequest) (TODOContentEditResponse, error) {
	s.called, s.company, s.project, s.todo = "next-step", company, input.ProjectID, input.TODOID
	return TODOContentEditResponse{CompanyID: company, ProjectID: input.ProjectID, TODOID: input.TODOID, Revision: *input.ExpectedRevision + 1}, nil
}
func (s *recordMutationHTTPService) NormalizeTODOSectionOwnership(_ context.Context, company string, input TODOSectionOwnershipRequest) (TODOContentEditResponse, error) {
	s.called, s.company, s.project, s.todo = "ownership", company, input.ProjectID, input.TODOID
	return TODOContentEditResponse{CompanyID: company, ProjectID: input.ProjectID, TODOID: input.TODOID, Revision: *input.ExpectedRevision + 1}, nil
}

func TestTypedRecordMutationRoutesAreProjectAndTODOScoped(t *testing.T) {
	service := &recordMutationHTTPService{testService: &testService{}}
	handler := NewHandler(service)
	tests := []struct{ name, path, body, kind string }{
		{"evidence", "/internal/v1/companies/c1/projects/p1/todos/t1/evidence", `{"project_id":"p1","todo_id":"t1","expected_revision":4,"operation_id":"e1","proof_refs":[],"waiver_refs":[],"implementation_commits":[],"record_sections":[],"reason":"evidence"}`, "evidence"},
		{"placement", "/internal/v1/companies/c1/projects/p1/todos/t1/placement", `{"project_id":"p1","todo_id":"t1","expected_revision":4,"operation_id":"p1","release_id":"r1","position":2,"reason":"placement"}`, "placement"},
		{"next-step", "/internal/v1/companies/c1/projects/p1/todos/t1/next-step", `{"project_id":"p1","todo_id":"t1","expected_revision":4,"operation_id":"n1","next_exact_step":"Apply the approved change.","reason":"Correct stale guidance"}`, "next-step"},
		{"ownership", "/internal/v1/companies/c1/projects/p1/todos/t1/sections/ownership", `{"project_id":"p1","todo_id":"t1","expected_revision":4,"operation_id":"o1","sections":[],"reason":"ownership"}`, "ownership"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body)))
			if response.Code != http.StatusOK || service.called != tc.kind || service.company != "c1" || service.project != "p1" || service.todo != "t1" || !strings.Contains(response.Body.String(), `"todo_revision":5`) {
				t.Fatalf("typed mutation route = %d %s; service=%s/%s/%s/%s", response.Code, response.Body.String(), service.called, service.company, service.project, service.todo)
			}
			var body map[string]any
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestMongoNextStepEditPreservesContentAndStage(t *testing.T) {
	uri := os.Getenv("REGISTRY_TEST_MONGODB_URI")
	if uri == "" {
		t.Skip("isolated replica-set MongoDB fixture not configured")
	}
	ctx := context.Background()
	stamp := strconv.FormatInt(time.Now().UnixNano(), 36)
	store, err := OpenMongo(ctx, uri, stamp)
	if err != nil {
		t.Fatal(err)
	}
	store.tenantPrefix = stamp
	t.Cleanup(func() {
		_ = store.client.Database(store.tenantDatabase("company-a")).Drop(context.Background())
		_ = store.control.Drop(context.Background())
		_ = store.Close(context.Background())
	})
	if _, err := registerTestProjectWithBinding(ctx, store, Registration{CompanyID: "company-a", CompanyName: "Company A", ProjectID: "builder", ProjectName: "Builder", Binding: validTestBinding()}); err != nil {
		t.Fatal(err)
	}
	if err := store.ProvisionTODOCollection(ctx, "company-a"); err != nil {
		t.Fatal(err)
	}
	if err := store.ProvisionTODOHistory(ctx, "company-a"); err != nil {
		t.Fatal(err)
	}
	database := store.client.Database(store.tenantDatabase("company-a"))
	if err := database.RunCommand(ctx, bson.D{{Key: "collMod", Value: todosCollection}, {Key: "validator", Value: TODOCollectionValidatorBeforeNextStepEdit()}}).Err(); err != nil {
		t.Fatal(err)
	}
	if err := store.ProvisionTODOHistory(ctx, "company-a"); err != nil {
		t.Fatalf("upgrade previous strict validator: %v", err)
	}
	record := mongoBornTODOFixture()
	record.ID = "next-step-edit-fixture"
	if _, err := store.AdmitTODO(ctx, "company-a", "builder", TODOAdmissionRequest{SchemaVersion: todoCreationSchemaVersion, OperationID: "next-step-edit-create", TODO: record}); err != nil {
		t.Fatal(err)
	}
	before, err := store.ReadTODO(ctx, "company-a", "builder", record.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	zero := int64(0)
	request := TODONextStepEditRequest{ProjectID: "builder", TODOID: record.ID, ExpectedRevision: &zero, OperationID: "next-step-1", NextExactStep: "Implement the approved contract.", Reason: "Correct outdated guidance"}
	result, err := store.EditTODONextStep(ctx, "company-a", request)
	if err != nil || result.Revision != 1 || result.NoOp {
		t.Fatalf("next-step edit = %+v, %v", result, err)
	}
	after, err := store.ReadTODO(ctx, "company-a", "builder", record.ID, nil)
	if err != nil || after.TODO.NextExactStep != request.NextExactStep || after.TODO.Stage != before.TODO.Stage || after.TODO.StageVerification != before.TODO.StageVerification || after.TODO.ReleaseID != before.TODO.ReleaseID || after.ContentDigest != before.ContentDigest || !reflect.DeepEqual(after.ContentRevision, before.ContentRevision) {
		t.Fatalf("edit changed protected fields: before=%+v after=%+v err=%v", before, after, err)
	}
	retry, err := store.EditTODONextStep(ctx, "company-a", request)
	if err != nil || !retry.NoOp || retry.Revision != 1 {
		t.Fatalf("exact retry = %+v, %v", retry, err)
	}
	changedPayload := request
	changedPayload.NextExactStep = "Different step"
	if _, err := store.EditTODONextStep(ctx, "company-a", changedPayload); !errors.Is(err, ErrConflict) {
		t.Fatalf("reused operation ID accepted: %v", err)
	}
	changedPayload.OperationID = "next-step-2"
	if _, err := store.EditTODONextStep(ctx, "company-a", changedPayload); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale revision accepted: %v", err)
	}
	one := int64(1)
	noOp := request
	noOp.ExpectedRevision, noOp.OperationID = &one, "next-step-noop"
	if result, err := store.EditTODONextStep(ctx, "company-a", noOp); err != nil || !result.NoOp || result.Revision != 1 {
		t.Fatalf("unchanged step = %+v, %v", result, err)
	}
	for _, invalid := range []string{"", "   ", strings.Repeat("x", 513)} {
		bad := noOp
		bad.NextExactStep, bad.OperationID = invalid, "invalid-step-"+strconv.Itoa(len(invalid))
		if _, err := store.EditTODONextStep(ctx, "company-a", bad); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid step length %d accepted: %v", len(invalid), err)
		}
	}
	if _, err := store.EditTODONextStep(ctx, "another-company", noOp); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-company write accepted: %v", err)
	}
	asOfBefore, err := store.ReadTODOAsOf(ctx, "company-a", "builder", record.ID, 0)
	if err != nil || asOfBefore.State.NextExactStep != record.NextExactStep {
		t.Fatalf("before as-of = %+v, %v", asOfBefore, err)
	}
	asOfAfter, err := store.ReadTODOAsOf(ctx, "company-a", "builder", record.ID, 1)
	if err != nil || asOfAfter.State.NextExactStep != request.NextExactStep || asOfAfter.Content.Title != asOfBefore.Content.Title {
		t.Fatalf("after as-of = %+v, %v", asOfAfter, err)
	}
	comparison, err := store.CompareTODOHistory(ctx, "company-a", "builder", record.ID, 0, 1)
	if err != nil || !reflect.DeepEqual(comparison.Changed, []string{"next_exact_step"}) {
		t.Fatalf("history comparison = %+v, %v", comparison, err)
	}
	count, err := database.Collection(todoContentVersionsCollection).CountDocuments(ctx, bson.D{{Key: "project_id", Value: "builder"}, {Key: "todo_id", Value: record.ID}})
	if err != nil || count != 1 {
		t.Fatalf("next-step edit created content version: count=%d err=%v", count, err)
	}
}

func TestMongoEvidenceEditUpgradesPriorCapacityAndPersistsHistory(t *testing.T) {
	uri := os.Getenv("REGISTRY_TEST_MONGODB_URI")
	if uri == "" {
		t.Skip("isolated replica-set MongoDB fixture not configured")
	}
	ctx := context.Background()
	stamp := strconv.FormatInt(time.Now().UnixNano(), 36)
	store, err := OpenMongo(ctx, uri, stamp)
	if err != nil {
		t.Fatal(err)
	}
	store.tenantPrefix = stamp
	t.Cleanup(func() {
		_ = store.client.Database(store.tenantDatabase("company-a")).Drop(context.Background())
		_ = store.control.Drop(context.Background())
		_ = store.Close(context.Background())
	})
	if _, err := registerTestProjectWithBinding(ctx, store, Registration{CompanyID: "company-a", CompanyName: "Company A", ProjectID: "builder", ProjectName: "Builder", Binding: validTestBinding()}); err != nil {
		t.Fatal(err)
	}
	if err := store.ProvisionTODOCollection(ctx, "company-a"); err != nil {
		t.Fatal(err)
	}
	if err := store.ProvisionTODOHistory(ctx, "company-a"); err != nil {
		t.Fatal(err)
	}
	record := mongoBornTODOFixture()
	record.ID = "capacity-upgrade-fixture"
	if _, err := store.AdmitTODO(ctx, "company-a", "builder", TODOAdmissionRequest{SchemaVersion: todoCreationSchemaVersion, OperationID: "capacity-upgrade-create", TODO: record}); err != nil {
		t.Fatal(err)
	}
	database := store.client.Database(store.tenantDatabase("company-a"))
	if err := database.RunCommand(ctx, bson.D{{Key: "collMod", Value: todosCollection}, {Key: "validator", Value: TODOCollectionValidatorBeforeEvidenceCapacity()}, {Key: "validationLevel", Value: "strict"}, {Key: "validationAction", Value: "error"}}).Err(); err != nil {
		t.Fatal(err)
	}
	proofs := make([]TODOProofReference, 65)
	for i := range proofs {
		proofs[i] = TODOProofReference{Kind: "criterion:" + strconv.Itoa(i), Source: "paced-local-runner", Reference: "G-TEST", SourceDigest: "sha256:" + strings.Repeat("a", 64), VerificationState: "unverified"}
	}
	zero := int64(0)
	request := TODOEvidenceEditRequest{ProjectID: "builder", TODOID: record.ID, ExpectedRevision: &zero, OperationID: "evidence-capacity-1", ProofReferences: proofs, WaiverReferences: []TODOProofReference{}, ImplementationCommits: []TODOCommitReference{}, RecordSections: []TODOSection{}, Reason: "Preserve bounded multi-revision proof"}
	result, err := store.EditTODOEvidence(ctx, "company-a", request)
	if err != nil || result.Revision != 1 || result.NoOp {
		t.Fatalf("evidence edit after validator upgrade = %+v, %v", result, err)
	}
	if err := verifyTODOCollection(ctx, database); err != nil {
		t.Fatalf("validator was not upgraded: %v", err)
	}
	read, err := store.ReadTODO(ctx, "company-a", "builder", record.ID, nil)
	if err != nil || read.TODORevision != 1 || len(read.TODO.ProofReferences) != len(proofs) {
		t.Fatalf("persisted evidence = %+v, %v", read, err)
	}
	prior, err := store.ReadTODOAsOf(ctx, "company-a", "builder", record.ID, 0)
	if err != nil || len(prior.State.ProofReferences) != 0 {
		t.Fatalf("prior history changed: %+v, %v", prior, err)
	}
	current, err := store.ReadTODOAsOf(ctx, "company-a", "builder", record.ID, 1)
	if err != nil || len(current.State.ProofReferences) != len(proofs) {
		t.Fatalf("new evidence history missing: %+v, %v", current, err)
	}
	if _, err := database.Collection(epicsCollection).InsertOne(ctx, roadmapEpic{StorageID: bson.NewObjectID(), ProjectID: "builder", ID: "e-06", Name: "Database", Position: 0}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Collection(releasesCollection).InsertOne(ctx, roadmapRelease{StorageID: bson.NewObjectID(), ProjectID: "builder", ID: "r-08", EpicID: "e-06", Name: "Mongo", Position: 0}); err != nil {
		t.Fatal(err)
	}
	imported := genericTODOAdmissionFixture()
	imported.TODO.ID = "imported-cap-fixture"
	imported.TODO.Source.Path = "todos/active/TODO-imported-cap-fixture.md"
	if _, err := store.AdmitTODO(ctx, "company-a", "builder", imported); err != nil {
		t.Fatal(err)
	}
	typed := TODOAuthoredContent{TODOContent: validTODOContent(), Title: imported.TODO.Title, Objective: imported.TODO.Objective, DiffExpectation: []TODORepositoryScope{}, AffectedFiles: []string{}}
	typedDigest, err := todoContentRevisionDigest("builder", imported.TODO.ID, 1, typed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Collection(todoContentVersionsCollection).InsertOne(ctx, TODOContentVersion{ObjectID: bson.NewObjectID(), ProjectID: "builder", TODOID: imported.TODO.ID, Revision: 1, Content: typed, Digest: typedDigest}); err != nil {
		t.Fatal(err)
	}
	one := int64(1)
	if _, err := database.Collection(todoEventsCollection).InsertOne(ctx, TODOHistoryEvent{ObjectID: bson.NewObjectID(), ProjectID: "builder", TODOID: imported.TODO.ID, Revision: one, Kind: "content_changed", OccurredAt: time.Now().UTC(), OperationID: "fixture-typed-content", RequestDigest: sourceHash([]byte("typed")), ContentRevision: &one}); err != nil {
		t.Fatal(err)
	}
	sections := make([]TODOSection, 17)
	for i := range sections {
		sections[i] = TODOSection{Key: "section-" + strconv.Itoa(i), Title: "Section " + strconv.Itoa(i), BodyMarkdown: strings.Repeat("x", 60<<10), Position: i, Ownership: "record"}
	}
	var importedRecord TODORecord
	if err := database.Collection(todosCollection).FindOne(ctx, bson.D{{Key: "project_id", Value: "builder"}, {Key: "id", Value: imported.TODO.ID}}).Decode(&importedRecord); err != nil {
		t.Fatal(err)
	}
	var projected bson.M
	encoded, err := bson.Marshal(importedRecord)
	if err != nil {
		t.Fatal(err)
	}
	if err := bson.Unmarshal(encoded, &projected); err != nil {
		t.Fatal(err)
	}
	projected["sections"] = sections
	projected["revision"] = one
	encoded, err = bson.Marshal(projected)
	if err != nil {
		t.Fatal(err)
	}
	trim := len(encoded) - (maxTODORecordBytes - 64)
	lastBodyLength := len(sections[16].BodyMarkdown) - trim
	if lastBodyLength <= 0 || lastBodyLength > 64<<10 {
		t.Fatalf("near-limit imported fixture cannot be sized: trim=%d", trim)
	}
	sections[16].BodyMarkdown = strings.Repeat("x", lastBodyLength)
	if _, err := database.Collection(todosCollection).UpdateOne(ctx, bson.D{{Key: "project_id", Value: "builder"}, {Key: "id", Value: imported.TODO.ID}}, bson.D{{Key: "$set", Value: bson.D{{Key: "revision", Value: one}, {Key: "sections", Value: sections}}}}); err != nil {
		t.Fatal(err)
	}
	before, err := store.ReadTODO(ctx, "company-a", "builder", imported.TODO.ID, nil)
	if err != nil || before.TODO.Source.Kind != "mongo-content-revision" {
		t.Fatalf("migrated imported TODO not readable: %+v, %v", before, err)
	}
	step := TODONextStepEditRequest{ProjectID: "builder", TODOID: imported.TODO.ID, ExpectedRevision: &one, OperationID: "imported-cap-next-step", NextExactStep: strings.Repeat("n", 512), Reason: "Exercise the persisted-source size boundary"}
	stepDigest, err := todoRequestDigest(step)
	if err != nil {
		t.Fatal(err)
	}
	set := bson.D{{Key: "next_exact_step", Value: step.NextExactStep}, {Key: "last_operation", Value: TODOOperationState{ID: step.OperationID, Kind: "next-step-edit", RequestDigest: stepDigest, ResultRevision: 2}}, {Key: "updated_at", Value: bson.DateTime(time.Now().UTC().UnixMilli())}}
	projectedSize := func(raw bson.Raw) int {
		var doc bson.M
		if err := bson.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		for _, field := range set {
			doc[field.Key] = field.Value
		}
		doc["revision"] = int64(2)
		encoded, err := bson.Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		return len(encoded)
	}
	persisted, err := persistedTODORecordBSON(ctx, database.Collection(todosCollection), TODORecord{ObjectID: importedRecord.ObjectID, ProjectID: "builder", ID: imported.TODO.ID, Revision: one})
	if err != nil {
		t.Fatal(err)
	}
	synthetic, err := bson.Marshal(before.TODO)
	if err != nil {
		t.Fatal(err)
	}
	gap := projectedSize(persisted) - projectedSize(synthetic)
	if gap < 2 {
		t.Fatalf("imported source was not larger than synthetic source: gap=%d", gap)
	}
	shrink := projectedSize(persisted) - (maxTODORecordBytes + gap/2)
	lastBodyLength = len(sections[16].BodyMarkdown) - shrink
	if lastBodyLength <= 0 || lastBodyLength > 64<<10 {
		t.Fatalf("cannot calibrate imported source gap: shrink=%d gap=%d", shrink, gap)
	}
	sections[16].BodyMarkdown = strings.Repeat("x", lastBodyLength)
	if _, err := database.Collection(todosCollection).UpdateOne(ctx, bson.D{{Key: "project_id", Value: "builder"}, {Key: "id", Value: imported.TODO.ID}}, bson.D{{Key: "$set", Value: bson.D{{Key: "sections", Value: sections}}}}); err != nil {
		t.Fatal(err)
	}
	before, err = store.ReadTODO(ctx, "company-a", "builder", imported.TODO.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err = persistedTODORecordBSON(ctx, database.Collection(todosCollection), TODORecord{ObjectID: importedRecord.ObjectID, ProjectID: "builder", ID: imported.TODO.ID, Revision: one})
	if err != nil {
		t.Fatal(err)
	}
	synthetic, err = bson.Marshal(before.TODO)
	if err != nil {
		t.Fatal(err)
	}
	if projectedSize(persisted) <= maxTODORecordBytes || projectedSize(synthetic) > maxTODORecordBytes {
		t.Fatalf("fixture does not distinguish persisted and synthetic projection: persisted=%d synthetic=%d", projectedSize(persisted), projectedSize(synthetic))
	}
	if _, err := store.EditTODONextStep(ctx, "company-a", step); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "todo.record_bytes") {
		t.Fatalf("near-limit imported TODO accepted oversized next step: %v", err)
	}
	after, err := store.ReadTODO(ctx, "company-a", "builder", imported.TODO.ID, nil)
	if err != nil || after.TODORevision != one || after.TODO.NextExactStep != before.TODO.NextExactStep {
		t.Fatalf("rejected edit changed imported TODO: %+v, %v", after, err)
	}
	if count, err := database.Collection(todoEventsCollection).CountDocuments(ctx, bson.D{{Key: "project_id", Value: "builder"}, {Key: "todo_id", Value: imported.TODO.ID}, {Key: "operation_id", Value: step.OperationID}}); err != nil || count != 0 {
		t.Fatalf("rejected edit wrote history: count=%d err=%v", count, err)
	}
}
