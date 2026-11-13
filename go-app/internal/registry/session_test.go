package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestSessionIdentityAndBindingValidation(t *testing.T) {
	provider, externalID, err := normalizeSessionIdentity(" Codex ", "conversation-1")
	if err != nil || provider != "codex" || externalID != "conversation-1" {
		t.Fatalf("normalized identity = %q/%q, %v", provider, externalID, err)
	}
	for _, input := range [][2]string{{"", "id"}, {"bad/provider", "id"}, {"codex", " id"}, {"codex", strings.Repeat("x", 513)}} {
		if _, _, err := normalizeSessionIdentity(input[0], input[1]); err == nil {
			t.Errorf("accepted invalid identity %q/%q", input[0], input[1])
		}
	}
	valid := SessionBindingRequest{Type: "codex", ExternalID: "conversation-1", CompanyID: "company-a", ProjectID: "project-a", ExpectedRevision: 0, OperationID: "bind-1", OwnerReference: "owner-confirmed-project-a"}
	if err := validateSessionBindingRequest(valid); err != nil {
		t.Fatalf("valid binding rejected: %v", err)
	}
	valid.ProjectID = "project-b"
	valid.ExpectedRevision = 1
	if err := validateSessionBindingRequest(valid); err == nil {
		t.Fatal("accepted a rebind revision")
	}
}

func TestSessionStoresProjectAssociationOnlyInBindingObject(t *testing.T) {
	session := Session{ID: bson.NewObjectID(), Type: "codex", ExternalID: "conversation-1", Binding: SessionProjectBinding{CompanyID: "company-a", ProjectID: "project-a"}, Revision: 1, OwnerReference: "owner-confirmed"}
	encoded, err := bson.Marshal(session)
	if err != nil {
		t.Fatal(err)
	}
	var document bson.M
	if err := bson.Unmarshal(encoded, &document); err != nil {
		t.Fatal(err)
	}
	binding, ok := document["binding"].(bson.D)
	values := map[string]any{}
	for _, entry := range binding {
		values[entry.Key] = entry.Value
	}
	if !ok || values["company_id"] != "company-a" || values["project_id"] != "project-a" {
		t.Fatalf("binding document = %#v", document["binding"])
	}
	if _, exists := document["company_id"]; exists {
		t.Fatal("Company association escaped the immutable binding object")
	}
	if _, exists := document["project_id"]; exists {
		t.Fatal("Project association escaped the immutable binding object")
	}
}

type sessionHTTPService struct {
	testService
	bindRequest SessionBindingRequest
	session     Session
	listCompany string
	listProject string
	listAfter   bson.ObjectID
	listLimit   int
}

func (s *sessionHTTPService) BindSession(_ context.Context, request SessionBindingRequest) (Session, error) {
	s.bindRequest = request
	return s.session, nil
}
func (s *sessionHTTPService) ReadSession(_ context.Context, provider, externalID string) (Session, error) {
	if provider != s.session.Type || externalID != s.session.ExternalID {
		return Session{}, ErrNotFound
	}
	return s.session, nil
}
func (s *sessionHTTPService) ListProjectSessions(_ context.Context, company, project string, after bson.ObjectID, limit int) (SessionList, error) {
	s.listCompany, s.listProject, s.listAfter, s.listLimit = company, project, after, limit
	return SessionList{Items: []Session{s.session}}, nil
}

func TestSessionRoutesExposeBindingReadSelfAndScopedList(t *testing.T) {
	objectID := bson.NewObjectID()
	service := &sessionHTTPService{session: Session{ID: objectID, Type: "codex", ExternalID: "conversation-1", Binding: SessionProjectBinding{CompanyID: "company-a", ProjectID: "project-a"}, Revision: 1}}
	handler := NewHandler(service)

	response := httptest.NewRecorder()
	body := `{"type":"codex","external_id":"conversation-1","company_id":"company-a","project_id":"project-a","expected_revision":0,"operation_id":"bind-1","owner_reference":"owner-confirmed-project-a"}`
	request := httptest.NewRequest(http.MethodPost, "/internal/v1/sessions/bind", strings.NewReader(body))
	request.Header.Set(sessionTypeHeader, "codex")
	request.Header.Set(sessionExternalIDHeader, "conversation-1")
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated || service.bindRequest.ProjectID != "project-a" || !strings.Contains(response.Body.String(), `"revision":1`) {
		t.Fatalf("bind response status=%d request=%+v body=%s", response.Code, service.bindRequest, response.Body)
	}

	response = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "/internal/v1/sessions/codex/conversation-1", nil)
	request.Header.Set(sessionTypeHeader, "codex")
	request.Header.Set(sessionExternalIDHeader, "conversation-1")
	handler.ServeHTTP(response, request)
	var read Session
	if err := json.Unmarshal(response.Body.Bytes(), &read); err != nil || response.Code != http.StatusOK || read.Binding.ProjectID != "project-a" {
		t.Fatalf("read response status=%d session=%+v decode=%v body=%s", response.Code, read, err, response.Body)
	}

	cursor := bson.NewObjectID()
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/internal/v1/companies/company-a/projects/project-a/sessions?limit=5&after="+cursor.Hex(), nil))
	if response.Code != http.StatusOK || service.listCompany != "company-a" || service.listProject != "project-a" || service.listAfter != cursor || service.listLimit != 5 {
		t.Fatalf("list response status=%d scope=%s/%s cursor=%s limit=%d body=%s", response.Code, service.listCompany, service.listProject, service.listAfter.Hex(), service.listLimit, response.Body)
	}
}

func TestSessionBindingRouteRejectsUnknownAndTrailingFields(t *testing.T) {
	service := &sessionHTTPService{}
	for _, body := range []string{
		`{"type":"codex","external_id":"x","company_id":"c","project_id":"p","expected_revision":0,"operation_id":"o","owner_reference":"owner","target_project":"other"}`,
		`{"type":"codex","external_id":"x","company_id":"c","project_id":"p","expected_revision":0,"operation_id":"o","owner_reference":"owner"}{}`,
	} {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/internal/v1/sessions/bind", strings.NewReader(body))
		request.Header.Set(sessionTypeHeader, "codex")
		request.Header.Set(sessionExternalIDHeader, "x")
		NewHandler(service).ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Errorf("accepted invalid session request %s: status=%d body=%s", body, response.Code, response.Body)
		}
	}
}

func TestConversationLaneEnforcesImmutableProjectBinding(t *testing.T) {
	service := &sessionHTTPService{
		testService: testService{roadmap: json.RawMessage(`{"epics":[]}`)},
		session:     Session{Type: "codex", ExternalID: "conversation-1", Binding: SessionProjectBinding{CompanyID: "company-a", ProjectID: "project-a"}, Revision: 1},
	}
	request := httptest.NewRequest(http.MethodGet, "/internal/v1/companies/company-a/projects/project-b/roadmap", nil)
	request.Header.Set(sessionLaneHeader, "conversation")
	request.Header.Set(sessionTypeHeader, "codex")
	request.Header.Set(sessionExternalIDHeader, "conversation-1")
	request.Header.Set("X-Actor-Authorized-Projects", "project-a,project-b")
	response := httptest.NewRecorder()
	NewHandler(service).ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("multi-Project actor bypassed bound session: status=%d body=%s", response.Code, response.Body)
	}

	request = httptest.NewRequest(http.MethodGet, "/internal/v1/companies/company-a/projects/project-a/roadmap", nil)
	request.Header.Set(sessionLaneHeader, "conversation")
	request.Header.Set(sessionTypeHeader, "codex")
	request.Header.Set(sessionExternalIDHeader, "conversation-1")
	response = httptest.NewRecorder()
	NewHandler(service).ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("bound Project request status=%d body=%s", response.Code, response.Body)
	}

	request = httptest.NewRequest(http.MethodGet, "/internal/v1/companies/company-a/projects/project-a/roadmap", nil)
	request.Header.Set(sessionLaneHeader, "conversation")
	response = httptest.NewRecorder()
	NewHandler(service).ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("missing session identity status=%d body=%s", response.Code, response.Body)
	}
}

func TestConversationLaneRejectsForeignProjectAcrossRoutes(t *testing.T) {
	service := &sessionHTTPService{
		testService: testService{roadmap: json.RawMessage(`{"epics":[]}`)},
		session:     Session{Type: "codex", ExternalID: "session-a", Binding: SessionProjectBinding{CompanyID: "company-a", ProjectID: "project-a"}, Revision: 1},
	}
	handler := NewHandler(service)
	foreignRoutes := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/internal/v1/companies/company-a/projects/project-b/knowledge/manifest"},
		{http.MethodGet, "/internal/v1/companies/company-a/projects/project-b/todos/t-foreign"},
		{http.MethodPost, "/internal/v1/companies/company-a/projects/project-b/todos/t-foreign/content"},
		{http.MethodGet, "/internal/v1/companies/company-a/projects/project-b/design-system/state"},
		{http.MethodGet, "/internal/v1/companies/company-a/projects/project-b/sessions"},
		{http.MethodPost, "/internal/v1/companies/company-a/projects/project-b/roadmap/epics"},
	}
	for _, route := range foreignRoutes {
		t.Run(route.method+route.path, func(t *testing.T) {
			request := httptest.NewRequest(route.method, route.path, nil)
			request.Header.Set(sessionLaneHeader, "conversation")
			request.Header.Set(sessionTypeHeader, "codex")
			request.Header.Set(sessionExternalIDHeader, "session-a")
			request.Header.Set("X-Actor-Authorized-Projects", "project-a,project-b")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusForbidden || service.listProject != "" {
				t.Fatalf("foreign route status=%d listProject=%q body=%s", response.Code, service.listProject, response.Body)
			}
		})
	}
	for _, identity := range [][2]string{{"", ""}, {"codex", "unknown"}, {"codex", " session-a"}, {"other", "session-a"}} {
		request := httptest.NewRequest(http.MethodGet, "/internal/v1/companies/company-a/projects/project-a/roadmap", nil)
		request.Header.Set(sessionLaneHeader, "conversation")
		request.Header.Set(sessionTypeHeader, identity[0])
		request.Header.Set(sessionExternalIDHeader, identity[1])
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("unbound identity %q/%q status=%d body=%s", identity[0], identity[1], response.Code, response.Body)
		}
	}
}

func TestSessionListRequiresBoundIdentityAndExposesPreIAMSubstitutionLimit(t *testing.T) {
	projectA := &sessionHTTPService{session: Session{Type: "codex", ExternalID: "session-a", Binding: SessionProjectBinding{CompanyID: "company-a", ProjectID: "project-a"}, Revision: 1}}
	projectB := &sessionHTTPService{session: Session{Type: "codex", ExternalID: "session-b", Binding: SessionProjectBinding{CompanyID: "company-a", ProjectID: "project-b"}, Revision: 1}}
	path := "/internal/v1/companies/company-a/projects/project-b/sessions"
	requestList := func(service *sessionHTTPService, provider, externalID string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set(sessionLaneHeader, "conversation")
		request.Header.Set(sessionTypeHeader, provider)
		request.Header.Set(sessionExternalIDHeader, externalID)
		response := httptest.NewRecorder()
		NewHandler(service).ServeHTTP(response, request)
		return response
	}
	if response := requestList(projectA, "codex", "session-a"); response.Code != http.StatusForbidden || projectA.listProject != "" {
		t.Fatalf("foreign session list status=%d listed=%q body=%s", response.Code, projectA.listProject, response.Body)
	}
	for _, identity := range [][2]string{{"", ""}, {"codex", "unknown"}, {"codex", " session-b"}, {"other", "session-b"}} {
		if response := requestList(projectB, identity[0], identity[1]); response.Code != http.StatusUnauthorized || projectB.listProject != "" {
			t.Fatalf("unknown/malformed identity %q/%q status=%d listed=%q body=%s", identity[0], identity[1], response.Code, projectB.listProject, response.Body)
		}
	}
	// A caller who substitutes B's valid ID can list B before IAM authenticates
	// header ownership. This is the documented trust limitation, not isolation.
	if response := requestList(projectB, "codex", "session-b"); response.Code != http.StatusOK || projectB.listProject != "project-b" {
		t.Fatalf("valid B identity status=%d listed=%q body=%s", response.Code, projectB.listProject, response.Body)
	}
}

func TestConversationLaneRejectsEncodedOrNoncanonicalProjectIdentity(t *testing.T) {
	service := &sessionHTTPService{
		testService: testService{project: Project{ID: "project-b", CompanyID: "company-a", Name: "Other Project"}},
		session:     Session{Type: "codex", ExternalID: "conversation-1", Binding: SessionProjectBinding{CompanyID: "company-a", ProjectID: "project-a"}, Revision: 1},
	}
	handler := NewHandler(service)
	for _, path := range []string{
		"/internal/v1/companies/company-a/projects/%20project-b%20",
		"/internal/v1/companies/%20company-a%20/projects/project-b",
		"/internal/v1/companies/company-a/projects/project-b%2Falias",
		"/%69nternal/v1/companies/company-a/projects/project-b",
		"/internal/v1/%63ompanies/company-a/projects/project-b",
	} {
		t.Run(path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, path, nil)
			request.Header.Set(sessionLaneHeader, "conversation")
			request.Header.Set(sessionTypeHeader, "codex")
			request.Header.Set(sessionExternalIDHeader, "conversation-1")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusForbidden || strings.Contains(response.Body.String(), "Other Project") {
				t.Fatalf("noncanonical route escaped session binding: status=%d body=%s", response.Code, response.Body)
			}
		})
	}
}

func TestProjectReaderLaneWithoutConversationIdentityRemainsAvailable(t *testing.T) {
	service := &sessionHTTPService{testService: testService{project: Project{ID: "project-a", CompanyID: "company-a", Name: "Project A"}}}
	response := httptest.NewRecorder()
	NewHandler(service).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/internal/v1/companies/company-a/projects/project-a", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"name":"Project A"`) {
		t.Fatalf("service reader response status=%d body=%s", response.Code, response.Body)
	}
}

func TestMongoSessionsAreGloballyUniqueImmutableAndProjectListed(t *testing.T) {
	uri := os.Getenv("REGISTRY_TEST_MONGODB_URI")
	if uri == "" {
		t.Skip("isolated MongoDB fixture not configured")
	}
	ctx := context.Background()
	stamp := fmt.Sprintf("session_guard_%d", time.Now().UnixNano())
	store, err := OpenMongo(ctx, uri, stamp)
	if err != nil {
		t.Fatal(err)
	}
	store.tenantPrefix = stamp
	t.Cleanup(func() {
		for _, company := range []string{"company-a", "company-b"} {
			_ = store.client.Database(store.tenantDatabase(company)).Drop(context.Background())
		}
		_ = store.control.Drop(context.Background())
		_ = store.Close(context.Background())
	})
	for _, item := range []struct{ company, project string }{{"company-a", "project-a"}, {"company-a", "project-b"}, {"company-b", "project-c"}} {
		if _, err := store.Register(ctx, Registration{CompanyID: item.company, CompanyName: item.company, ProjectID: item.project, ProjectName: item.project}); err != nil {
			t.Fatalf("register %s/%s: %v", item.company, item.project, err)
		}
	}
	request := SessionBindingRequest{Type: "codex", ExternalID: "same-conversation", CompanyID: "company-a", ProjectID: "project-a", ExpectedRevision: 0, OperationID: "bind-session-a", OwnerReference: "owner-confirmed-project-a"}
	first, err := store.BindSession(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := store.BindSession(ctx, request)
	if err != nil || retry.ID != first.ID || retry.Binding != first.Binding {
		t.Fatalf("same request retry = %+v, %v", retry, err)
	}
	rebind := request
	rebind.ProjectID, rebind.OperationID = "project-b", "attempt-rebind"
	if _, err := store.BindSession(ctx, rebind); !errors.Is(err, ErrConflict) {
		t.Fatalf("same-Company Project rebind error = %v", err)
	}
	crossCompany := request
	crossCompany.CompanyID, crossCompany.ProjectID, crossCompany.OperationID = "company-b", "project-c", "cross-company-rebind"
	if _, err := store.BindSession(ctx, crossCompany); !errors.Is(err, ErrConflict) {
		t.Fatalf("cross-Company provider identity reuse error = %v", err)
	}
	unknownTarget := request
	unknownTarget.ProjectID, unknownTarget.OperationID = "not-registered", "unknown-target-rebind"
	if _, err := store.BindSession(ctx, unknownTarget); !errors.Is(err, ErrConflict) {
		t.Fatalf("immutable identity leaked target Project existence: %v", err)
	}
	listed, err := store.ListProjectSessions(ctx, "company-a", "project-a", bson.NilObjectID, 1)
	if err != nil || len(listed.Items) != 1 || listed.Items[0].ID != first.ID || listed.Items[0].Binding.ProjectID != "project-a" {
		t.Fatalf("Project session list = %+v, %v", listed, err)
	}
	if _, err := store.ListProjectSessions(ctx, "company-a", "project-b", bson.NilObjectID, 1); err != nil {
		t.Fatal(err)
	} else if other, _ := store.ListProjectSessions(ctx, "company-a", "project-b", bson.NilObjectID, 1); len(other.Items) != 0 {
		t.Fatalf("foreign session disclosed in another Project: %+v", other)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, target := range []SessionBindingRequest{
		{Type: "codex", ExternalID: "race-conversation", CompanyID: "company-a", ProjectID: "project-a", OperationID: "race-a", OwnerReference: "owner-a"},
		{Type: "codex", ExternalID: "race-conversation", CompanyID: "company-b", ProjectID: "project-c", OperationID: "race-b", OwnerReference: "owner-b"},
	} {
		go func(target SessionBindingRequest) {
			<-start
			_, bindErr := store.BindSession(ctx, target)
			results <- bindErr
		}(target)
	}
	close(start)
	successes, conflicts := 0, 0
	for range 2 {
		switch err := <-results; {
		case err == nil:
			successes++
		case errors.Is(err, ErrConflict):
			conflicts++
		default:
			t.Fatalf("concurrent first binding returned unexpected error: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent first binding must have one winner and one conflict: successes=%d conflicts=%d", successes, conflicts)
	}
}

func TestMongoConversationRoadmapMutationStaysWithinBoundProject(t *testing.T) {
	uri := os.Getenv("REGISTRY_TEST_MONGODB_URI")
	if uri == "" {
		t.Skip("isolated MongoDB fixture not configured")
	}
	ctx := context.Background()
	stamp := fmt.Sprintf("session_roadmap_%d", time.Now().UnixNano())
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
	for _, project := range []string{"project-a", "project-b"} {
		if _, err := store.Register(ctx, Registration{CompanyID: "company-a", CompanyName: "Company A", ProjectID: project, ProjectName: project}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.BindSession(ctx, SessionBindingRequest{Type: "codex", ExternalID: "session-a", CompanyID: "company-a", ProjectID: "project-a", OperationID: "bind-a", OwnerReference: "owner-a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.BindSession(ctx, SessionBindingRequest{Type: "codex", ExternalID: "session-b", CompanyID: "company-a", ProjectID: "project-b", OperationID: "bind-b", OwnerReference: "owner-b"}); err != nil {
		t.Fatal(err)
	}
	// Authorization-allow fixture: one actor is entitled to both Projects.
	// The private API does not implement IAM, so this assumption cannot bind
	// a second Project to the same session or bypass the session guard.
	actorAuthorizedProjects := []string{"project-a", "project-b"}
	if _, err := store.BindSession(ctx, SessionBindingRequest{Type: "codex", ExternalID: "session-a", CompanyID: "company-a", ProjectID: "project-b", OperationID: "rebind-a-to-b", OwnerReference: "same-actor"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("multi-Project actor rebound session A: %v", err)
	}
	if bound, err := store.ReadSession(ctx, "codex", "session-a"); err != nil || bound.Binding.ProjectID != "project-a" {
		t.Fatalf("session A binding changed after rejected rebind: %+v, %v", bound.Binding, err)
	}
	handler := NewHandler(store)
	createEpic := func(sessionID, projectID, operationID string, revision int64) *httptest.ResponseRecorder {
		t.Helper()
		body := fmt.Sprintf(`{"project_id":%q,"expected_roadmap_revision":%d,"operation_id":%q,"id":"e-session-test","name":"Session test","description":"Bound Project"}`, projectID, revision, operationID)
		request := httptest.NewRequest(http.MethodPost, "/internal/v1/companies/company-a/projects/"+projectID+"/roadmap/epics", strings.NewReader(body))
		request.Header.Set(sessionLaneHeader, "conversation")
		request.Header.Set(sessionTypeHeader, "codex")
		request.Header.Set(sessionExternalIDHeader, sessionID)
		// Actor membership in both Projects cannot override the bound session.
		request.Header.Set("X-Actor-Authorized-Projects", strings.Join(actorAuthorizedProjects, ","))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	roadmapRevision := func(projectID string) int64 {
		t.Helper()
		data, err := store.Roadmap(ctx, projectID, "company-a")
		if err != nil {
			t.Fatal(err)
		}
		var view struct {
			RoadmapRevision int64 `json:"roadmap_revision"`
		}
		if err := json.Unmarshal(data, &view); err != nil {
			t.Fatal(err)
		}
		return view.RoadmapRevision
	}
	if response := createEpic("session-a", "project-b", "foreign-write", 0); response.Code != http.StatusForbidden {
		t.Fatalf("foreign mutation status=%d body=%s", response.Code, response.Body)
	}
	if revision := roadmapRevision("project-b"); revision != 0 {
		t.Fatalf("foreign Project changed after denied mutation: revision=%d", revision)
	}
	if response := createEpic("session-a", "project-a", "own-write", 0); response.Code != http.StatusOK {
		t.Fatalf("own mutation status=%d body=%s", response.Code, response.Body)
	}
	if revision := roadmapRevision("project-a"); revision != 1 {
		t.Fatalf("own Project revision=%d, want 1", revision)
	}
	if revision := roadmapRevision("project-b"); revision != 0 {
		t.Fatalf("other Project changed after own mutation: revision=%d", revision)
	}
	if response := createEpic("session-b", "project-b", "second-own-write", 0); response.Code != http.StatusOK {
		t.Fatalf("second bound Project mutation status=%d body=%s", response.Code, response.Body)
	}
	// Before IAM, the caller-supplied session identity remains a trust boundary:
	// substituting B's valid ID is equivalent to acting as B, not authentication.
	if revision := roadmapRevision("project-b"); revision != 1 {
		t.Fatalf("second bound Project revision=%d, want 1", revision)
	}
}

func TestMongoImplementationRepositorySetIsProjectOwnedAndGatesPlanning(t *testing.T) {
	uri := os.Getenv("REGISTRY_TEST_MONGODB_URI")
	if uri == "" {
		t.Skip("isolated MongoDB fixture not configured")
	}
	ctx := context.Background()
	stamp := fmt.Sprintf("repo_set_guard_%d", time.Now().UnixNano())
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
	for _, projectID := range []string{"project-a", "project-b"} {
		if _, err := store.Register(ctx, Registration{CompanyID: "company-a", CompanyName: "Company A", ProjectID: projectID, ProjectName: projectID}); err != nil {
			t.Fatal(err)
		}
	}
	change := ImplementationRepositoryChange{Action: "confirm", ProjectID: "project-a", ExpectedRevision: 0, OperationID: "confirm-shared-a", Actor: "owner", OwnerReference: "owner-confirmation-a", RepositoryID: "belluga/shared"}
	set, err := store.ChangeImplementationRepository(ctx, "company-a", "project-a", change)
	if err != nil || set.Revision != 1 || len(set.Items) != 1 {
		t.Fatalf("confirm implementation repository = %+v, %v", set, err)
	}
	retry, err := store.ChangeImplementationRepository(ctx, "company-a", "project-a", change)
	if err != nil || retry.Revision != 1 {
		t.Fatalf("exact operation retry = %+v, %v", retry, err)
	}
	duplicate := change
	duplicate.ExpectedRevision, duplicate.OperationID = 1, "confirm-shared-again"
	if set, err = store.ChangeImplementationRepository(ctx, "company-a", "project-a", duplicate); err != nil || set.Revision != 1 {
		t.Fatalf("duplicate repository confirmation was not idempotent: %+v, %v", set, err)
	}
	second := ImplementationRepositoryChange{Action: "confirm", ProjectID: "project-a", ExpectedRevision: 1, OperationID: "confirm-other-a", Actor: "second-owner", OwnerReference: "owner-confirmation-second", RepositoryID: "belluga/other"}
	if set, err = store.ChangeImplementationRepository(ctx, "company-a", "project-a", second); err != nil || set.Revision != 2 || len(set.Confirmations) != 2 {
		t.Fatalf("second confirmation lost membership provenance: %+v, %v", set, err)
	}
	if set.Confirmations[0].OwnerReference != "owner-confirmation-second" || set.Confirmations[1].OwnerReference != "owner-confirmation-a" {
		t.Fatalf("confirmation references not preserved per repository: %+v", set.Confirmations)
	}
	if set, err = store.ReadImplementationRepositories(ctx, "company-a", "project-b"); err != nil || len(set.Items) != 0 {
		t.Fatalf("repository confirmation leaked to another Project: %+v, %v", set, err)
	}
	if _, err := store.ChangeImplementationRepository(ctx, "company-a", "project-b", ImplementationRepositoryChange{Action: "confirm", ProjectID: "project-b", ExpectedRevision: 0, OperationID: "confirm-shared-b", Actor: "owner", OwnerReference: "owner-confirmation-b", RepositoryID: "belluga/shared"}); err != nil {
		t.Fatalf("shared repository could not be confirmed for another Project: %v", err)
	}
	record := TODORecord{ProjectID: "project-a", DiffExpectation: []TODORepositoryScope{{RepositoryID: "belluga/unconfirmed"}}}
	if err := requireTODOImplementationRepositories(ctx, store, "company-a", record); err == nil || !errors.Is(err, ErrConflict) {
		t.Fatalf("unconfirmed Git delivery repository passed Planning gate: %v", err)
	}
	record.DiffExpectation[0].RepositoryID = "belluga/shared"
	if err := requireTODOImplementationRepositories(ctx, store, "company-a", record); err != nil {
		t.Fatalf("confirmed repository failed Planning gate: %v", err)
	}
}
