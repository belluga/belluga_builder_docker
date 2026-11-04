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
	if _, err := store.Register(ctx, Registration{CompanyID: "company-a", CompanyName: "Company A", ProjectID: "builder", ProjectName: "Builder", Binding: validTestBinding()}); err != nil {
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
	if _, err := store.AdmitTODO(ctx, "company-a", "builder", TODOAdmissionRequest{SchemaVersion: todoCreationSchemaVersion, TODO: record}); err != nil {
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
