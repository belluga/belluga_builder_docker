package registry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
)

type investigationTestService struct {
	*testService
	changes int
}

func (s *investigationTestService) ReadReleaseInvestigation(_ context.Context, companyID, projectID, releaseID string) (ReleaseInvestigationRead, error) {
	return ReleaseInvestigationRead{CompanyID: companyID, ProjectID: projectID, ReleaseID: releaseID, RoadmapRevision: 7, State: "absent"}, nil
}

func (s *investigationTestService) ChangeRoadmap(_ context.Context, companyID, action string, request RoadmapChange) (RoadmapChangeResult, error) {
	s.changes++
	return RoadmapChangeResult{CompanyID: companyID, ProjectID: request.ProjectID, Action: action, ID: request.ID, RoadmapRevision: 8}, nil
}

func TestReleaseInvestigationRoutes(t *testing.T) {
	service := &investigationTestService{testService: &testService{}}
	handler := NewHandler(service)
	base := "/internal/v1/companies/company-a/projects/project-a/roadmap/releases/release-a/investigation"
	for _, check := range []struct {
		method, suffix, body, want string
		status                     int
	}{
		{http.MethodGet, "", "", `"state":"absent"`, http.StatusOK},
		{http.MethodGet, "/template", "", `"expected_roadmap_revision":7`, http.StatusOK},
		{http.MethodPut, "", `{"project_id":"project-a","expected_roadmap_revision":7,"operation_id":"set-study-1","investigation":{"title":"Research","materials":[]}}`, `"action":"set-investigation"`, http.StatusOK},
		{http.MethodDelete, "", `{"project_id":"project-a","expected_roadmap_revision":8,"operation_id":"clear-study-1"}`, `"action":"clear-investigation"`, http.StatusOK},
		{http.MethodPut, "", `{"project_id":"other","expected_roadmap_revision":7,"operation_id":"bad-study","investigation":{"title":"Research","materials":[]}}`, `"error"`, http.StatusConflict},
	} {
		req := httptest.NewRequest(check.method, base+check.suffix, strings.NewReader(check.body))
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != check.status || !strings.Contains(res.Body.String(), check.want) {
			t.Fatalf("%s %s: %d %s, want %d containing %q", check.method, check.suffix, res.Code, res.Body.String(), check.status, check.want)
		}
	}
	if service.changes != 2 {
		t.Fatalf("writes = %d, want 2", service.changes)
	}
}

func TestReleaseInvestigationValidation(t *testing.T) {
	valid := &ReleaseInvestigation{Title: "Research", Materials: []ReleaseInvestigationMaterial{{ID: "study-1", Title: "Finding", BodyMarkdown: "Evidence"}}}
	if err := validateReleaseInvestigation(valid); err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		name string
		edit func(*ReleaseInvestigation)
	}{
		{"missing title", func(v *ReleaseInvestigation) { v.Title = " " }},
		{"duplicate ID", func(v *ReleaseInvestigation) { v.Materials = append(v.Materials, v.Materials[0]) }},
		{"oversized body", func(v *ReleaseInvestigation) { v.Materials[0].BodyMarkdown = strings.Repeat("x", (64<<10)+1) }},
		{"too many materials", func(v *ReleaseInvestigation) { v.Materials = make([]ReleaseInvestigationMaterial, 33) }},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			value := *valid
			value.Materials = append([]ReleaseInvestigationMaterial(nil), valid.Materials...)
			check.edit(&value)
			if err := validateReleaseInvestigation(&value); err == nil {
				t.Fatal("invalid Investigation was accepted")
			}
		})
	}
}

func TestRoadmapListContainsInvestigationTitleButNotBodies(t *testing.T) {
	value, err := projectRoadmap("project-a", 4,
		[]roadmapEpic{{StorageID: bson.NewObjectID(), ProjectID: "project-a", ID: "epic-a", Name: "Epic"}},
		[]roadmapRelease{{StorageID: bson.NewObjectID(), ProjectID: "project-a", ID: "release-a", EpicID: "epic-a", Name: "Release", Investigation: &ReleaseInvestigation{Title: "Research", Introduction: "private introduction", Materials: []ReleaseInvestigationMaterial{{ID: "study-a", Title: "Study", BodyMarkdown: "private material body"}}}}}, nil)
	if err != nil || !strings.Contains(string(value), `"investigation":{"title":"Research"}`) || strings.Contains(string(value), "private introduction") || strings.Contains(string(value), "private material body") {
		t.Fatalf("Roadmap leaked Investigation body: %s, %v", value, err)
	}
}
