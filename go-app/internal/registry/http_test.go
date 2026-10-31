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
	catalog       Catalog
	project       Project
	roadmap       json.RawMessage
	registerCalls int
	lastTenant    TenantRequest
}

func (s *testService) Catalog(context.Context) (Catalog, error)         { return s.catalog, nil }
func (s *testService) Project(context.Context, string, string) (Project, error) { return s.project, nil }
func (s *testService) Register(_ context.Context, in Registration) (Project, error) {
	s.registerCalls++
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
	service := &testService{roadmap: json.RawMessage(`{"epics":[],"backlog":{"todos":[],"folders":[]}}`)}
	handler := NewHandler(service)
	checks := []struct {
		method, path, body, want string
		status                   int
	}{
		{http.MethodGet, "/internal/v1/catalog", "", `"companies":null`, http.StatusOK},
		{http.MethodGet, "/internal/v1/companies/company-1/projects/builder", "", `"id":""`, http.StatusOK},
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
