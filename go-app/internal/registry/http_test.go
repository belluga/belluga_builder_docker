package registry

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type testService struct {
	Service
	catalog          Catalog
	project          Project
	roadmap          json.RawMessage
	registerCalls    int
	lastRegistration Registration
	lastTenant       TenantRequest
}

func (s *testService) Catalog(context.Context) (Catalog, error) { return s.catalog, nil }
func (s *testService) Project(context.Context, string, string) (Project, error) {
	return s.project, nil
}
func (s *testService) Register(_ context.Context, in Registration) (Project, error) {
	s.registerCalls++
	s.lastRegistration = in
	return Project{ID: in.ProjectID, CompanyID: in.CompanyID}, nil
}
func (s *testService) Decline(context.Context, string, string, Binding) error { return nil }
func (s *testService) UpdateSnapshotReference(_ context.Context, _, id string, _ SnapshotReference) (Project, error) {
	return Project{ID: id}, nil
}
func (s *testService) LifecycleControl(context.Context) (LifecycleControl, error) {
	return LifecycleControl{Revision: 3, Value: json.RawMessage(`{"state":"started"}`)}, nil
}
func (s *testService) UpdateLifecycleControl(_ context.Context, revision int64, value json.RawMessage) (LifecycleControl, error) {
	return LifecycleControl{Revision: revision + 1, Value: value}, nil
}
func (s *testService) Roadmap(context.Context, string, string) (json.RawMessage, error) {
	return s.roadmap, nil
}
func (s *testService) ResolveTenant(_ context.Context, input TenantRequest) (TenantResolution, error) {
	s.lastTenant = input
	return TenantResolution{CompanyID: "company-1", Company: Company{ID: "company-1", Name: "Company"}}, nil
}

func TestPrivateRoutesPreserveExplicitEmptyRoadmap(t *testing.T) {
	service := &testService{roadmap: json.RawMessage(`{"epics":[],"backlog":{"todos":[],"folders":[]}}`), project: Project{ID: "builder", CompanyID: "company-1"}}
	handler := NewHandler(service)
	checks := []struct {
		method, path, body, want string
		status                   int
	}{
		{http.MethodGet, "/internal/v1/catalog", "", `"companies":null`, http.StatusOK},
		{http.MethodGet, "/internal/v1/companies/company-1/projects/builder", "", `"id":"builder"`, http.StatusOK},
		{http.MethodPost, "/internal/v1/companies/company-1/projects", `{"company_id":"company-1","project_id":"builder"}`, `"id":"builder"`, http.StatusOK},
		{http.MethodPut, "/internal/v1/companies/company-1/projects/builder/snapshot-reference", `{"company_id":"company-1"}`, `"id":"builder"`, http.StatusOK},
		{http.MethodGet, "/internal/v1/companies/company-1/projects/builder/roadmap", "", `"epics":[]`, http.StatusOK},
		{http.MethodGet, "/internal/v1/tenants/resolve?type=host&identifier=example.test", "", `"company_id":"company-1"`, http.StatusOK},
		{http.MethodGet, "/internal/v1/lifecycle-control", "", `"revision":3`, http.StatusOK},
		{http.MethodPut, "/internal/v1/lifecycle-control", `{"expected_revision":3,"value":{"state":"complete"}}`, `"revision":4`, http.StatusOK},
	}
	for _, check := range checks {
		t.Run(check.method+" "+check.path, func(t *testing.T) {
			req := httptest.NewRequest(check.method, check.path, strings.NewReader(check.body))
			res := httptest.NewRecorder()
			handler.ServeHTTP(res, req)
			if res.Code != check.status || !strings.Contains(res.Body.String(), check.want) {
				t.Fatalf("response = %d %s; want %d containing %q", res.Code, res.Body.String(), check.status, check.want)
			}
		})
	}
	if service.registerCalls != 1 {
		t.Fatalf("register calls = %d, want 1", service.registerCalls)
	}
	if service.lastTenant.Type != "host" || service.lastTenant.Identifier != "example.test" {
		t.Fatalf("tenant request = %#v, want host example.test", service.lastTenant)
	}
}

func TestPrivateAPIRejectsUnknownFieldsAndOversizedBodies(t *testing.T) {
	service := &testService{}
	handler := NewHandler(service)
	for name, body := range map[string]string{
		"unknown field": `{"company_id":"company-1","project_id":"builder","database_name":"caller-choice"}`,
		"oversized":     `{"padding":"` + strings.Repeat("x", maxRequestBody) + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/internal/v1/companies/company-1/projects", strings.NewReader(body))
			res := httptest.NewRecorder()
			handler.ServeHTTP(res, req)
			if res.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body %s; want 400", res.Code, res.Body.String())
			}
		})
	}
	if service.registerCalls != 0 {
		t.Fatalf("invalid requests reached service %d times", service.registerCalls)
	}
}

func TestPrivateAPIRejectsCompanyMismatchBeforeRegister(t *testing.T) {
	service := &testService{}
	req := httptest.NewRequest(http.MethodPost, "/internal/v1/companies/company-a/projects", strings.NewReader(`{"company_id":"company-b","project_id":"project-a"}`))
	res := httptest.NewRecorder()
	NewHandler(service).ServeHTTP(res, req)
	if res.Code != http.StatusBadRequest || service.registerCalls != 0 {
		t.Fatalf("mismatched Company reached registry: status=%d calls=%d", res.Code, service.registerCalls)
	}
}

type testSelectedTODOImporter struct {
	*testService
	calls int
}

func (s *testSelectedTODOImporter) ImportSelectedTODOs(_ context.Context, request SelectedTODOImportRequest) (SelectedTODOImportReport, error) {
	s.calls++
	return ValidateSelectedTODOImport(request)
}

func TestLegacySelectedTODOImportRoutesTeachWithoutWrites(t *testing.T) {
	service := &testSelectedTODOImporter{testService: &testService{}}
	for _, path := range []string{"/internal/v1/todos/import", "/internal/v1/todos/admit-builder-r08"} {
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
		response := httptest.NewRecorder()
		NewHandler(service).ServeHTTP(response, request)
		if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"teach":"TEACH:`) || service.calls != 0 {
			t.Fatalf("legacy import route %s response = %d %s; calls=%d", path, response.Code, response.Body.String(), service.calls)
		}
	}
}

type testSelectedTODOLifecycle struct {
	*testService
	readCalls       int
	transitionCalls int
	verifyCalls     int
	verifyRequest   *VerifyCurrentTODOStageRequest
	readRevision    *int64
	readErr         error
}

func (s *testSelectedTODOLifecycle) ReadTODO(_ context.Context, companyID, projectID, todoID string, expected *int64) (TODOReadResponse, error) {
	s.readCalls++
	s.readRevision = expected
	if s.readErr != nil {
		return TODOReadResponse{}, s.readErr
	}
	return TODOReadResponse{CompanyID: companyID, ProjectID: projectID, SnapshotFingerprint: "fingerprint", TODO: TODORecord{ID: todoID, ProjectID: projectID, Stage: "Completed", StageVerification: "unverified", Revision: 4}, TODORevision: 4}, nil
}

func (s *testSelectedTODOLifecycle) TransitionTODO(_ context.Context, _ string, _ TODOActionRequest) (TODOActionResponse, error) {
	s.transitionCalls++
	return TODOActionResponse{}, todoTeach("requested_stage", "require a deterministic Go proof guard defined and approved in a later TODO; this operation is unavailable and no write occurred", ErrConflict)
}

func (s *testSelectedTODOLifecycle) VerifyCurrentTODOStage(_ context.Context, companyID string, request VerifyCurrentTODOStageRequest) (TODOActionResponse, error) {
	s.verifyCalls++
	s.verifyRequest = &request
	return TODOActionResponse{}, todoTeach("verify-current-stage", "require a deterministic Go proof guard defined and approved in a later TODO; this operation is unavailable and no write occurred", ErrConflict)
}

func TestMigratedTODOPrivateReadAndConditionalRoutes(t *testing.T) {
	service := &testSelectedTODOLifecycle{testService: &testService{}}
	handler := NewHandler(service)
	read := httptest.NewRecorder()
	handler.ServeHTTP(read, httptest.NewRequest(http.MethodGet, "/internal/v1/companies/belluga-solutions/projects/boora/todos/t-b9e3ea9e3a616b01?todo_revision=4", nil))
	if read.Code != http.StatusOK || !strings.Contains(read.Body.String(), `"todo_revision":4`) || !strings.Contains(read.Body.String(), `"snapshot_fingerprint":"fingerprint"`) || service.readCalls != 1 || service.readRevision == nil || *service.readRevision != 4 {
		t.Fatalf("exact TODO read = %d %s; calls=%d revision=%v", read.Code, read.Body.String(), service.readCalls, service.readRevision)
	}

	service.readErr = &todoRevisionConflict{Revision: 5}
	stale := httptest.NewRecorder()
	handler.ServeHTTP(stale, httptest.NewRequest(http.MethodGet, "/internal/v1/companies/belluga-solutions/projects/boora/todos/t-b9e3ea9e3a616b01?todo_revision=4", nil))
	if stale.Code != http.StatusConflict || !strings.Contains(stale.Body.String(), `"todo_revision":5`) || !strings.Contains(stale.Body.String(), "reload the Project Roadmap") {
		t.Fatalf("stale TODO detail response = %d %s", stale.Code, stale.Body.String())
	}

	transition := httptest.NewRecorder()
	body := `{"project_id":"builder","todo_id":"builder-company-tenant-mongo-registry","expected_revision":4,"operation_id":"op-1","requested_stage":"Local-Implemented","proof_refs":[],"waiver_refs":[]}`
	handler.ServeHTTP(transition, httptest.NewRequest(http.MethodPost, "/internal/v1/companies/belluga-solutions/projects/builder/todos/builder-company-tenant-mongo-registry/transition", strings.NewReader(body)))
	if transition.Code != http.StatusConflict || !strings.Contains(transition.Body.String(), "proof guard defined and approved in a later TODO") || !strings.Contains(transition.Body.String(), "no write occurred") || strings.Contains(transition.Body.String(), `"stage":"Local-Implemented"`) || service.transitionCalls != 1 {
		t.Fatalf("guardless transition did not fail closed = %d %s; calls=%d", transition.Code, transition.Body.String(), service.transitionCalls)
	}

	verify := httptest.NewRecorder()
	body = `{"project_id":"boora","todo_id":"t-b9e3ea9e3a616b01","expected_revision":4,"operation_id":"verify-1"}`
	handler.ServeHTTP(verify, httptest.NewRequest(http.MethodPost, "/internal/v1/companies/belluga-solutions/projects/boora/todos/t-b9e3ea9e3a616b01/verify-current-stage", strings.NewReader(body)))
	if verify.Code != http.StatusConflict || strings.Contains(verify.Body.String(), `"stage_verification":"verified"`) || !strings.Contains(verify.Body.String(), "proof guard defined and approved in a later TODO") || !strings.Contains(verify.Body.String(), "no write occurred") || service.verifyCalls != 1 {
		t.Fatalf("verify-current-stage did not fail closed = %d %s; calls=%d", verify.Code, verify.Body.String(), service.verifyCalls)
	}

	invalid := httptest.NewRecorder()
	body = `{"project_id":"boora","todo_id":"t-b9e3ea9e3a616b01","expected_revision":4,"operation_id":"verify-2","verified_proof":{"kind":"publication","source":"production-record","ref":"release-proof","source_digest":"sha256:` + strings.Repeat("a", 64) + `"}}`
	handler.ServeHTTP(invalid, httptest.NewRequest(http.MethodPost, "/internal/v1/companies/belluga-solutions/projects/boora/todos/t-b9e3ea9e3a616b01/verify-current-stage", strings.NewReader(body)))
	if invalid.Code != http.StatusBadRequest || service.verifyCalls != 1 {
		t.Fatalf("caller-supplied proof claim reached verifier: %d %s calls=%d", invalid.Code, invalid.Body.String(), service.verifyCalls)
	}

	reapproval := httptest.NewRecorder()
	body = `{"project_id":"builder","todo_id":"todo-a","expected_revision":4,"operation_id":"verify-current-content","proof_refs":[{"kind":"owner-approval","source":"mongo-content-revision","ref":"builder/todo-a/content/7","source_digest":"sha256:` + strings.Repeat("a", 64) + `","verification_state":"unverified"}]}`
	handler.ServeHTTP(reapproval, httptest.NewRequest(http.MethodPost, "/internal/v1/companies/belluga-solutions/projects/builder/todos/todo-a/verify-current-stage", strings.NewReader(body)))
	if reapproval.Code != http.StatusConflict || service.verifyCalls != 2 || service.verifyRequest == nil || len(service.verifyRequest.ProofReferences) != 1 || service.verifyRequest.ProofReferences[0].Reference != "builder/todo-a/content/7" {
		t.Fatalf("current-content verification request was not passed through to the deterministic verifier: %d %s request=%+v calls=%d", reapproval.Code, reapproval.Body.String(), service.verifyRequest, service.verifyCalls)
	}
}
