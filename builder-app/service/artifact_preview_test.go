package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRegisteredHandlerRetiresSnapshotRoutes(t *testing.T) {
	for _, route := range []struct {
		path string
		body string
	}{
		{path: "/api/local-preview?company_id=company-a&project_id=project-a", body: "project_snapshot_retired"},
		{path: "/snapshot/company-a/project-a/" + strings.Repeat("a", 64) + "/design/landing/index.html", body: "project_snapshot_retired"},
		{path: "/api/local-todo-source?company_id=company-a&project_id=project-a", body: "todo_source_retired"},
	} {
		t.Run(route.path, func(t *testing.T) {
			handler := registeredHandler("/unused", t.TempDir(), "")
			request := httptest.NewRequest(http.MethodGet, route.path, nil)
			request.Host = "localhost"
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusGone || !strings.Contains(response.Body.String(), route.body) {
				t.Fatalf("retired route status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestRegisteredHandlerPreservesRoadmapAndFamilyRoutes(t *testing.T) {
	roadmap := `{"schema_version":"builder-roadmap-projection-v3","project_id":"project-a","focus_release_id":"","epics":[],"backlog":{"todos":[],"folders":[]},"unclassified":[],"diagnostics":[]}`
	registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/internal/v1/catalog":
			_, _ = w.Write([]byte(`{"schema_version":"1","companies":[{"id":"company-a","name":"Company A"}],"projects":[{"id":"project-a","name":"Project A","company_id":"company-a"}]}`))
		case "/internal/v1/companies/company-a/projects/project-a/roadmap":
			_, _ = w.Write([]byte(roadmap))
		default:
			http.NotFound(w, r)
		}
	}))
	defer registry.Close()
	t.Setenv("DELPHI_BUILDER_REGISTRY_URL", registry.URL)

	handler := registeredHandler("/unused", t.TempDir(), "")
	for _, route := range []struct {
		path       string
		wantStatus int
		wantBody   string
	}{
		{path: "/api/catalog", wantStatus: http.StatusOK, wantBody: `"project-a"`},
		{path: "/api/roadmap?company_id=company-a&project_id=project-a", wantStatus: http.StatusOK, wantBody: `"project_id":"project-a"`},
		{path: "/api/prototypes", wantStatus: http.StatusBadRequest, wantBody: "prototype_request_invalid"},
		{path: "/api/design-system", wantStatus: http.StatusBadRequest, wantBody: "design_system_request_invalid"},
	} {
		t.Run(route.path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, route.path, nil)
			request.Host = "localhost"
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != route.wantStatus || !strings.Contains(response.Body.String(), route.wantBody) {
				t.Fatalf("route status=%d body=%s; wanted status=%d containing %q", response.Code, response.Body.String(), route.wantStatus, route.wantBody)
			}
		})
	}
}
