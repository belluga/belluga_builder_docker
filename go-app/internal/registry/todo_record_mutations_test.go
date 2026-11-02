package registry

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
