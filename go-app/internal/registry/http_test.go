package registry

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

type testCanonicalHistoryService struct {
	*testService
	canonicalKnowledgeService
	cursor string
	limit  int
	calls  int
}

type testRepositoryBindingService struct {
	*testService
	request RepositoryBindingRequest
}

type testDesignSystemMutationService struct {
	*testService
	state            DesignSystemState
	intent           DesignSystemMutationIntent
	result           DesignSystemMutationResult
	readCompany      string
	readProject      string
	beginCompany     string
	beginProject     string
	recoverCompany   string
	recoverProject   string
	recoverOperation string
	recoverDeadline  bool
	recoverCalls     int
	beginErr         error
	recoverErr       error
}

type testDesignSystemReadService struct {
	*testService
	companyID  string
	projectID  string
	revision   string
	assetPath  string
	readCalls  int
	assetCalls int
	deadline   bool
}

func (s *testDesignSystemReadService) ReadDesignSystem(ctx context.Context, companyID, projectID, revision string) (DesignSystemReadResponse, error) {
	s.readCalls++
	_, s.deadline = ctx.Deadline()
	s.companyID, s.projectID, s.revision = companyID, projectID, revision
	return DesignSystemReadResponse{Selection: DesignSystemSelection{State: "selected", SHA: strings.Repeat("a", 40)}}, nil
}
func (s *testDesignSystemReadService) ReadDesignSystemAsset(_ context.Context, companyID, projectID, revision, assetPath string) (designSystemAssetResponse, error) {
	s.assetCalls++
	s.companyID, s.projectID, s.revision, s.assetPath = companyID, projectID, revision, assetPath
	return designSystemAssetResponse{Data: []byte("fixture"), ContentType: "image/svg+xml", ETag: `"fixture-etag"`, Revision: revision, InventoryDigest: "sha256:" + strings.Repeat("b", 64)}, nil
}

func (s *testDesignSystemMutationService) ReadDesignSystemState(_ context.Context, companyID, projectID string) (DesignSystemState, error) {
	s.readCompany, s.readProject = companyID, projectID
	return s.state, nil
}
func (s *testDesignSystemMutationService) BeginDesignSystemMutation(_ context.Context, companyID, projectID string, intent DesignSystemMutationIntent) (DesignSystemMutationIntent, error) {
	s.beginCompany, s.beginProject, s.intent = companyID, projectID, intent
	return intent, s.beginErr
}
func (s *testDesignSystemMutationService) RecoverDesignSystemMutation(ctx context.Context, companyID, projectID, operationID string) (DesignSystemMutationResult, error) {
	s.recoverCalls++
	_, s.recoverDeadline = ctx.Deadline()
	s.recoverCompany, s.recoverProject, s.recoverOperation = companyID, projectID, operationID
	return s.result, s.recoverErr
}

func (s *testRepositoryBindingService) ReadRepositoryBinding(context.Context, string, string) (RepositoryBinding, error) {
	return RepositoryBinding{RepositoryID: "belluga/repo", PublicationBranch: "main", State: "configured", Revision: 2}, nil
}
func (s *testRepositoryBindingService) ChangeRepositoryBinding(_ context.Context, _, _ string, request RepositoryBindingRequest) (RepositoryBinding, error) {
	s.request = request
	return RepositoryBinding{State: "absent", Revision: request.ExpectedRevision + 1}, nil
}

func TestRepositoryBindingRoutesAreProjectScopedAndPathFree(t *testing.T) {
	service := &testRepositoryBindingService{testService: &testService{}}
	handler := NewHandler(service)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/internal/v1/companies/company-a/projects/project-a/binding", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"state":"configured"`) || strings.Contains(response.Body.String(), "foundation_root") {
		t.Fatalf("binding GET: %d %s", response.Code, response.Body)
	}
	body := `{"action":"unbind","project_id":"project-a","expected_revision":2,"operation_id":"unbind-2","actor":"owner"}`
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/internal/v1/companies/company-a/projects/project-a/binding", strings.NewReader(body)))
	if response.Code != http.StatusOK || service.request.Actor != "owner" || service.request.ExpectedRevision != 2 || service.request.Action != "unbind" {
		t.Fatalf("binding mutation: %d request=%+v body=%s", response.Code, service.request, response.Body)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/internal/v1/companies/company-a/projects/project-a/binding", strings.NewReader(`{"action":"bind","project_id":"other-project"}`)))
	if response.Code != http.StatusConflict {
		t.Fatalf("cross-Project request identity status=%d body=%s", response.Code, response.Body)
	}
}

func TestDesignSystemMutationRoutesAreScopedBoundedAndProofFree(t *testing.T) {
	intent := DesignSystemMutationIntent{Operation: "create", OperationID: "ds-op-1", Actor: "owner", PayloadDigest: "sha256:" + strings.Repeat("e", 64), ExpectedBindingRevision: 1, ExpectedHead: strings.Repeat("b", 40), CandidateTreeSHA: strings.Repeat("c", 40), PreparedCommitSHA: strings.Repeat("d", 40), InventoryDigest: "sha256:" + strings.Repeat("e", 64)}
	service := &testDesignSystemMutationService{testService: &testService{}, intent: intent, state: DesignSystemState{
		Selection:        DesignSystemSelection{State: "unselected", RepositoryID: "belluga/repo", PublicationBranch: "main", BindingRevision: 1},
		PendingOperation: &intent,
	}, result: DesignSystemMutationResult{OperationID: intent.OperationID, Outcome: "published", State: "selected", SelectedSHA: intent.PreparedCommitSHA, InventoryHash: intent.InventoryDigest}}
	handler := NewHandler(service)
	base := "/internal/v1/companies/company-a/projects/project-a/design-system"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, base+"/state", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"repository_id":"belluga/repo"`) || !strings.Contains(response.Body.String(), `"pending_operation"`) || !strings.Contains(response.Body.String(), `"code":"design_system_operation_pending"`) || !strings.Contains(response.Body.String(), `"effects":"unknown"`) || !strings.Contains(response.Body.String(), `"retry":"recover_operation"`) || service.readCompany != "company-a" || service.readProject != "project-a" {
		t.Fatalf("Design System state GET status=%d scope=%s/%s body=%s", response.Code, service.readCompany, service.readProject, response.Body)
	}

	body, err := json.Marshal(intent)
	if err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, base+"/mutations", strings.NewReader(string(body))))
	if response.Code != http.StatusOK || service.beginCompany != "company-a" || service.beginProject != "project-a" || service.intent != intent || !strings.Contains(response.Body.String(), `"effects":"committed"`) || !strings.Contains(response.Body.String(), `"operation_id":"ds-op-1"`) {
		t.Fatalf("Design System begin route status=%d scope=%s/%s intent=%+v body=%s", response.Code, service.beginCompany, service.beginProject, service.intent, response.Body)
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, base+"/mutations/ds-op-1/recover", nil))
	if response.Code != http.StatusOK || !service.recoverDeadline || service.recoverCalls != 1 || service.recoverCompany != "company-a" || service.recoverProject != "project-a" || service.recoverOperation != intent.OperationID || !strings.Contains(response.Body.String(), `"outcome":"published"`) || !strings.Contains(response.Body.String(), `"effects":"committed"`) || !strings.Contains(response.Body.String(), `"retry":"none"`) {
		t.Fatalf("Design System recover route status=%d service=%+v body=%s", response.Code, service, response.Body)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, base+"/mutations/ds-op-1/recover", strings.NewReader(`{"remote_head":"`+strings.Repeat("f", 40)+`"}`)))
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"teach"`) || service.recoverCalls != 1 {
		t.Fatalf("client proof was not rejected before recovery: status=%d calls=%d body=%s", response.Code, service.recoverCalls, response.Body)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, base+"/mutations", strings.NewReader(`{"operation":"create","remote_head":"`+strings.Repeat("f", 40)+`"}`)))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("begin route accepted caller-supplied verification fields: %d %s", response.Code, response.Body)
	}
	service.recoverErr = ErrUnavailable
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, base+"/mutations/ds-op-1/recover", nil))
	var recoveryFailure errorResponse
	if json.Unmarshal(response.Body.Bytes(), &recoveryFailure) != nil || response.Code != http.StatusServiceUnavailable || recoveryFailure.TeachDetail == nil || recoveryFailure.TeachDetail.Code != "design_system_recovery_uncertain" || recoveryFailure.TeachDetail.OperationID != "ds-op-1" || recoveryFailure.TeachDetail.Effects != TeachEffectsUnknown || recoveryFailure.TeachDetail.Retry != TeachRetryRecoverOperation {
		t.Fatalf("uncertain recovery lacks safe operation guidance: status=%d body=%s", response.Code, response.Body)
	}
	service.beginErr = todoTeach("expected_binding_revision", "reload the current binding and reassess", ErrConflict)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, base+"/mutations", strings.NewReader(string(body))))
	var rejectedBegin errorResponse
	if json.Unmarshal(response.Body.Bytes(), &rejectedBegin) != nil || response.Code != http.StatusConflict || rejectedBegin.TeachDetail == nil || rejectedBegin.TeachDetail.Code != "registry_validation" || rejectedBegin.TeachDetail.FieldPath != "expected_binding_revision" || !strings.HasPrefix(rejectedBegin.TeachDetail.NextAction, "reload the current binding and reassess") || !strings.Contains(rejectedBegin.TeachDetail.NextAction, "only if it is the pending operation") || rejectedBegin.TeachDetail.Effects != TeachEffectsUnknown || rejectedBegin.TeachDetail.OperationID != "ds-op-1" || rejectedBegin.TeachDetail.Retry != TeachRetryReadReassess {
		t.Fatalf("begin error lost cause or conservative operation context: status=%d body=%s", response.Code, response.Body)
	}
}

func TestDesignSystemReadRoutesUseExactProjectRevisionAndDeclaredAssetPath(t *testing.T) {
	service := &testDesignSystemReadService{testService: &testService{}}
	handler := NewHandler(service)
	base := "/internal/v1/companies/company-a/projects/project-a/design-system"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, base, nil))
	if response.Code != http.StatusOK || !service.deadline || service.readCalls != 1 || service.companyID != "company-a" || service.projectID != "project-a" || service.revision != "" {
		t.Fatalf("effective Design System read status=%d service=%+v body=%s", response.Code, service, response.Body)
	}
	sha := strings.Repeat("a", 40)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, base+"/"+sha, nil))
	if response.Code != http.StatusOK || service.revision != sha || service.readCalls != 2 {
		t.Fatalf("exact Design System read status=%d service=%+v body=%s", response.Code, service, response.Body)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, base+"/"+sha+"/assets/assets/logo.svg", nil))
	if response.Code != http.StatusOK || response.Body.String() != "fixture" || response.Header().Get("ETag") != `"fixture-etag"` || response.Header().Get("X-Design-System-Revision") != sha || service.assetPath != "assets/logo.svg" || service.assetCalls != 1 {
		t.Fatalf("exact Design System asset status=%d service=%+v headers=%v body=%s", response.Code, service, response.Header(), response.Body)
	}
	response = httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, base+"/"+sha+"?revision=other", nil)
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || service.readCalls != 2 {
		t.Fatalf("Design System read accepted unscoped query: status=%d calls=%d body=%s", response.Code, service.readCalls, response.Body)
	}
}

func (s *testCanonicalHistoryService) CanonicalHistory(_ context.Context, _, _, cursor string, limit int) (CanonicalHistoryPage, error) {
	s.calls++
	s.cursor, s.limit = cursor, limit
	return CanonicalHistoryPage{Events: []CanonicalEvent{}, NextCursor: "opaque-next"}, nil
}

func TestCanonicalHistoryRouteUsesOpaqueCursorAndRejectsTimestampPagination(t *testing.T) {
	service := &testCanonicalHistoryService{testService: &testService{}}
	cursor := encodeCanonicalHistoryCursor("company-a", "project-a", time.Date(2026, 11, 4, 12, 30, 0, 123000000, time.UTC), bson.NewObjectID())
	response := httptest.NewRecorder()
	NewHandler(service).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/internal/v1/companies/company-a/projects/project-a/knowledge/history?limit=7&cursor="+cursor, nil))
	if response.Code != http.StatusOK || service.calls != 1 || service.cursor != cursor || service.limit != 7 || !strings.Contains(response.Body.String(), `"next_cursor":"opaque-next"`) {
		t.Fatalf("cursor history route status=%d call=%d cursor=%q limit=%d response=%s", response.Code, service.calls, service.cursor, service.limit, response.Body)
	}
	for _, query := range []string{"after=2026-11-04T12%3A30%3A00Z", "cursor=invalid/opaque", "cursor=" + cursor + "&cursor=" + cursor, "unused=1"} {
		response := httptest.NewRecorder()
		NewHandler(service).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/internal/v1/companies/company-a/projects/project-a/knowledge/history?"+query, nil))
		if response.Code != http.StatusBadRequest || service.calls != 1 {
			t.Fatalf("invalid history query %q status=%d calls=%d body=%s", query, response.Code, service.calls, response.Body)
		}
	}
}

type testService struct {
	Service
	catalog          Catalog
	project          Project
	roadmap          json.RawMessage
	registerCalls    int
	lastRegistration Registration
	lastTenant       TenantRequest
}

func TestLegacyBindingFirstRouteReturnsScopedTeach(t *testing.T) {
	service := &testService{}
	response := httptest.NewRecorder()
	NewHandler(service).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/internal/v1/companies/company-a/projects/project-a/binding/first", strings.NewReader(`{"binding":{}}`)))
	var body errorResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || response.Code != http.StatusBadRequest || !strings.Contains(body.Teach, "PUT /binding") {
		t.Fatalf("retired binding/first response status=%d body=%s decode=%v", response.Code, response.Body, err)
	}
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
		{http.MethodPut, "/internal/v1/companies/company-1/projects/builder/snapshot-reference", `{"company_id":"company-1"}`, `404 page not found`, http.StatusNotFound},
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

func TestTODOContentEditUnknownFieldReturnsActionableTeach(t *testing.T) {
	handler := NewHandler(&MongoStore{})
	request := httptest.NewRequest(http.MethodPost, "/internal/v1/companies/unifast/projects/accountzz/todos/todo-1/content", strings.NewReader(`{"project_id":"accountzz","todo_id":"todo-1","expected_revision":16,"operation_id":"invalid-edit","reason":"diagnose contract","new_content":{}}`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	var body errorResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusBadRequest || !strings.Contains(body.Teach, "request.new_content") || !strings.Contains(body.Teach, "use content for the authored TODO fields") {
		t.Fatalf("unknown field response = %d %+v", response.Code, body)
	}
	missing := httptest.NewRequest(http.MethodPost, "/internal/v1/companies/unifast/projects/accountzz/todos/todo-1/content", strings.NewReader(`{"project_id":"accountzz","todo_id":"todo-1","expected_revision":16,"operation_id":"missing-edit","reason":"diagnose contract"}`))
	missingResponse := httptest.NewRecorder()
	handler.ServeHTTP(missingResponse, missing)
	var missingBody errorResponse
	if err := json.Unmarshal(missingResponse.Body.Bytes(), &missingBody); err != nil {
		t.Fatal(err)
	}
	if missingResponse.Code != http.StatusBadRequest || !strings.Contains(missingBody.Teach, "content must provide") {
		t.Fatalf("missing content response = %d %+v", missingResponse.Code, missingBody)
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

func assertRegistrationRetryTeachHTTP(t *testing.T, ctx context.Context, store *MongoStore, input Registration) {
	t.Helper()
	request, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	path := "/internal/v1/companies/" + input.CompanyID + "/projects"
	response := httptest.NewRecorder()
	NewHandler(store).ServeHTTP(response, httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(request))).WithContext(ctx))
	var body errorResponse
	const waitGuidance = "finish schema preparation before you retry registration"
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || response.Code != http.StatusConflict ||
		body.Error != ErrConflict.Error() || !strings.Contains(body.Teach, waitGuidance) || body.TeachDetail == nil ||
		body.TeachDetail.FieldPath != "company.schema_migrations" || !strings.Contains(body.TeachDetail.NextAction, waitGuidance) {
		t.Fatalf("registration response during schema preparation: status=%d body=%s decode=%v", response.Code, response.Body, err)
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
	transitionErr   error
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
	return TODOReadResponse{CompanyID: companyID, ProjectID: projectID, TODO: TODORecord{ID: todoID, ProjectID: projectID, Stage: "Completed", StageVerification: "unverified", Revision: 4}, TODORevision: 4}, nil
}

func (s *testSelectedTODOLifecycle) TransitionTODO(_ context.Context, _ string, _ TODOActionRequest) (TODOActionResponse, error) {
	s.transitionCalls++
	if s.transitionErr != nil {
		return TODOActionResponse{}, s.transitionErr
	}
	return TODOActionResponse{}, todoTeach("requested_stage", "require a deterministic Go proof guard defined and approved in a later TODO; this operation is unavailable and no write occurred", ErrConflict)
}

func (s *testSelectedTODOLifecycle) VerifyCurrentTODOStage(_ context.Context, companyID string, request VerifyCurrentTODOStageRequest) (TODOActionResponse, error) {
	s.verifyCalls++
	s.verifyRequest = &request
	return TODOActionResponse{}, todoTeach("verify-current-stage", "require a deterministic Go proof guard defined and approved in a later TODO; this operation is unavailable and no write occurred", ErrConflict)
}

func TestTODOBodyLimitReturnsActionable413OnBothDecoderReads(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"first read", `{"value":"` + strings.Repeat("a", 128) + `"}`},
		{"trailing read", `{}` + strings.Repeat(" ", 128)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &Handler{}
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/todo", strings.NewReader(tc.body))
			var payload map[string]any
			if h.decodeTODOLimit(recorder, request, &payload, 128) {
				t.Fatal("oversized TODO body was accepted")
			}
			var errorBody errorResponse
			decodeErr := json.Unmarshal(recorder.Body.Bytes(), &errorBody)
			if decodeErr != nil || recorder.Code != http.StatusRequestEntityTooLarge || errorBody.Error != "request_too_large" || errorBody.TeachDetail == nil || errorBody.TeachDetail.SchemaVersion != "1" || errorBody.TeachDetail.Code != "request_too_large" || errorBody.TeachDetail.FieldPath != "request.body" || errorBody.TeachDetail.Effects != TeachEffectsNone || !strings.Contains(recorder.Body.String(), "128-byte") {
				t.Fatalf("wrong TODO limit response: status=%d body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
	generic := httptest.NewRecorder()
	if (&Handler{}).decodeLimit(generic, httptest.NewRequest(http.MethodPost, "/other", strings.NewReader(`{"value":"`+strings.Repeat("a", 128)+`"}`)), new(map[string]any), 128) || generic.Code != http.StatusBadRequest {
		t.Fatal("non-TODO route changed its existing invalid-body response")
	}
	malformed := httptest.NewRecorder()
	if (&Handler{}).decodeTODOLimit(malformed, httptest.NewRequest(http.MethodPost, "/todo", strings.NewReader(`{"value":`)), new(map[string]any), 128) || malformed.Code != http.StatusBadRequest {
		t.Fatal("malformed JSON was mistaken for an oversized TODO request")
	}
}

func TestMigratedTODOPrivateReadAndConditionalRoutes(t *testing.T) {
	service := &testSelectedTODOLifecycle{testService: &testService{}}
	handler := NewHandler(service)
	read := httptest.NewRecorder()
	handler.ServeHTTP(read, httptest.NewRequest(http.MethodGet, "/internal/v1/companies/belluga-solutions/projects/boora/todos/t-b9e3ea9e3a616b01?todo_revision=4", nil))
	if read.Code != http.StatusOK || !strings.Contains(read.Body.String(), `"todo_revision":4`) || strings.Contains(read.Body.String(), `"snapshot_fingerprint"`) || service.readCalls != 1 || service.readRevision == nil || *service.readRevision != 4 {
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

func TestLocalProofTransitionReturnsFieldSpecificTEACHOverHTTP(t *testing.T) {
	service := &testSelectedTODOLifecycle{
		testService:   &testService{},
		transitionErr: todoTeach("proof_refs.criterion:criterion-build.source", "be exactly paced-local-runner for PACED command evidence", ErrConflict),
	}
	body := `{"project_id":"builder","todo_id":"todo-a","expected_revision":7,"operation_id":"local-run-7","requested_stage":"Local-Implemented","proof_refs":[],"waiver_refs":[],"local_proof":{"schema_version":"builder-local-proof-result-v3"}}`
	response := httptest.NewRecorder()
	NewHandler(service).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/internal/v1/companies/belluga-solutions/projects/builder/todos/todo-a/transition", strings.NewReader(body)))
	var errorBody errorResponse
	decodeErr := json.Unmarshal(response.Body.Bytes(), &errorBody)
	if decodeErr != nil || response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"teach":"TEACH: proof_refs.criterion:criterion-build.source must be exactly paced-local-runner`) || errorBody.TeachDetail == nil || errorBody.TeachDetail.FieldPath != "proof_refs.criterion:criterion-build.source" || errorBody.TeachDetail.OperationID != "local-run-7" || errorBody.TeachDetail.Effects != TeachEffectsUnknown || service.transitionCalls != 1 {
		t.Fatalf("field-specific local proof TEACH was not surfaced by HTTP: %d %s calls=%d", response.Code, response.Body.String(), service.transitionCalls)
	}
}

func TestMCPDesignSystemSameOperationRecoveryAgainstMongoAndGit(t *testing.T) {
	binary, uri := os.Getenv("BUILDER_MCP_FIXTURE_BINARY"), os.Getenv("REGISTRY_TEST_MONGODB_URI")
	if binary == "" || uri == "" {
		t.Skip("required MCP delivery fixture sets BUILDER_MCP_FIXTURE_BINARY and REGISTRY_TEST_MONGODB_URI")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	identity := fmt.Sprintf("mcp_ds_%d", time.Now().UnixNano())
	store, err := OpenMongo(ctx, uri, identity)
	if err != nil {
		t.Fatal(err)
	}
	store.tenantPrefix = identity
	const company, project = "mcp-fixture", "design-system-recovery"
	t.Cleanup(func() {
		bg, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		_ = store.client.Database(store.tenantDatabase(company)).Drop(bg)
		_ = store.control.Drop(bg)
		_ = store.Close(bg)
	})
	root := t.TempDir()
	runFixtureGit(t, root, "init", "--quiet", "--initial-branch=main")
	runFixtureGit(t, root, "-c", "user.name=isolated fixture", "-c", "user.email=fixture@example.invalid",
		"commit", "--quiet", "--allow-empty", "-m", "Seed recovery head")
	head := fixtureGitOutput(t, root, "rev-parse", "HEAD")
	var gitAvailable atomic.Bool
	store.designSystemVerifier = gitDesignSystemVerifier{repositoryURL: func(string) string {
		if gitAvailable.Load() {
			return "file://" + root
		}
		return "file://" + filepath.Join(root, "unavailable")
	}}
	binding := validTestBinding()
	binding.RepositoryID = "fixture/design-system"
	registered, err := registerTestProjectWithBinding(ctx, store, Registration{
		CompanyID: company, CompanyName: "MCP recovery fixture", ProjectID: project, ProjectName: "Recovery fixture", Binding: binding,
	})
	if err != nil {
		t.Fatal(err)
	}
	const nativeID = "b1ddbc77-0361-4609-8c04-bb04ab5fbe93"
	if _, err := store.BindSession(ctx, SessionBindingRequest{
		Type: "codex", ExternalID: nativeID, CompanyID: company, ProjectID: project,
		OperationID: "bind-recovery-fixture", OwnerReference: "owned test session",
	}); err != nil {
		t.Fatal(err)
	}
	registry := httptest.NewServer(NewHandler(store))
	defer registry.Close()
	address := startFixtureMCP(t, ctx, binary, registry.URL, company)
	type wireReply struct {
		Status int             `json:"http_status"`
		Data   json.RawMessage `json:"data"`
		Teach  TeachDiagnostic `json:"teach_detail"`
	}
	call := func(tool string, input map[string]any, want int) wireReply {
		t.Helper()
		raw, err := json.Marshal(map[string]any{
			"jsonrpc": "2.0", "id": 1, "method": "tools/call",
			"params": map[string]any{"name": tool, "arguments": input},
		})
		if err != nil {
			t.Fatal(err)
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+address+"/mcp", bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+fixtureMCPToken)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Accept", "application/json, text/event-stream")
		request.Header.Set("MCP-Protocol-Version", "2025-11-25")
		request.Header.Set(sessionTypeHeader, "codex")
		request.Header.Set(sessionExternalIDHeader, nativeID)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var output struct {
			Result struct {
				IsError bool `json:"isError"`
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"result"`
		}
		if err := json.NewDecoder(response.Body).Decode(&output); err != nil ||
			response.StatusCode != http.StatusOK || len(output.Result.Content) != 1 {
			t.Fatalf("%s unexpected MCP transport result: status=%d, output=%+v, err=%v", tool, response.StatusCode, output, err)
		}
		var reply wireReply
		if err := json.Unmarshal([]byte(output.Result.Content[0].Text), &reply); err != nil ||
			reply.Status != want || output.Result.IsError != (want >= 400) {
			t.Fatalf("%s unexpected outcome: %s err=%v", tool, output.Result.Content[0].Text, err)
		}
		return reply
	}
	const operationID = "design-system-recovery-op"
	digest := "sha256:" + strings.Repeat("b", 64)
	intent := DesignSystemMutationIntent{
		Operation: "create", OperationID: operationID, Actor: "isolated fixture", PayloadDigest: digest,
		ExpectedBindingRevision: registered.BindingRevision, ExpectedHead: head, CandidateTreeSHA: strings.Repeat("a", 40),
		PreparedCommitSHA: strings.Repeat("c", 40), InventoryDigest: digest,
	}
	call("builder_design_system_begin", map[string]any{"company_id": company, "project_id": project, "body": intent}, http.StatusOK)
	readArgs := map[string]any{"company_id": company, "project_id": project}
	before := call("builder_design_system_state", readArgs, http.StatusOK)
	recoverArgs := map[string]any{"company_id": company, "project_id": project, "operation_id": operationID}
	unknown := call("builder_design_system_recover", recoverArgs, http.StatusServiceUnavailable)
	if unknown.Teach.Effects != TeachEffectsUnknown || unknown.Teach.OperationID != operationID {
		t.Fatalf("owner uncertainty lost: %+v", unknown.Teach)
	}
	if after := call("builder_design_system_state", readArgs, http.StatusOK); !bytes.Equal(before.Data, after.Data) {
		t.Fatalf("unknown recovery changed the selected or pending state: %s versus %s", before.Data, after.Data)
	}

	gitAvailable.Store(true)
	recovered := call("builder_design_system_recover", recoverArgs, http.StatusOK)
	var outcome DesignSystemMutationResult
	if err := json.Unmarshal(recovered.Data, &outcome); err != nil ||
		outcome.OperationID != operationID || outcome.Outcome != "not_pushed" || outcome.State != "unselected" {
		t.Fatalf("the same operation did not recover against verified Git: %s err=%v", recovered.Data, err)
	}
	final := call("builder_design_system_state", readArgs, http.StatusOK)
	var state DesignSystemState
	if err := json.Unmarshal(final.Data, &state); err != nil ||
		state.PendingOperation != nil || state.LastOperation == nil || *state.LastOperation != outcome {
		t.Fatalf("MCP readback did not expose the persisted exact result: %s err=%v", final.Data, err)
	}
	if replay := call("builder_design_system_recover", recoverArgs, http.StatusOK); !bytes.Equal(replay.Data, recovered.Data) {
		t.Fatalf("same operation replay changed the result: %s", replay.Data)
	}
	collection := store.client.Database(store.tenantDatabase(company)).Collection(projectsCollection)
	var document projectDocument
	if err := collection.FindOne(ctx, bson.M{"id": project}).Decode(&document); err != nil ||
		document.PendingDesignSystemMutation != nil || document.DesignSystemLastOperation == nil ||
		*document.DesignSystemLastOperation != outcome {
		t.Fatalf("Mongo does not contain the one final operation result: %+v err=%v", document.DesignSystemLastOperation, err)
	}
	t.Logf("live adapter + Registry + Mongo + local Git: %s pending, outage effects=%s, exact recovery=%s, replay unchanged",
		operationID, unknown.Teach.Effects, outcome.Outcome)
}

const fixtureMCPToken = "isolated-mcp-registry-fixture-token-01"

func startFixtureMCP(t *testing.T, ctx context.Context, binary, upstream, company string) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	command := exec.CommandContext(ctx, binary)
	command.Env = []string{
		"BUILDER_MCP_COMPANY_IDS=" + company,
		"BUILDER_MCP_BEARER_TOKEN=" + fixtureMCPToken,
		"BUILDER_MCP_ALLOWED_HOSTS=" + address,
		"BUILDER_MCP_LISTEN_ADDR=" + address,
		"BUILDER_MCP_REGISTRY_URL=" + upstream,
		"GOMEMLIMIT=768MiB", "GOMAXPROCS=2",
	}
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = command.Process.Kill()
		_ = command.Wait()
	})
	wait, stop := context.WithTimeout(ctx, 10*time.Second)
	defer stop()
	for {
		connection, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
		if err == nil {
			_ = connection.Close()
			return address
		}
		select {
		case <-wait.Done():
			t.Fatal("owned MCP fixture did not start")
		case <-time.After(25 * time.Millisecond):
		}
	}
}
