package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

type sequencingTestService struct {
	*testService
	changes int
	action  string
	request RoadmapChange
	read    ReleaseSequencingPlanRead
}

func (s *sequencingTestService) ReadReleaseSequencingPlan(_ context.Context, companyID, projectID, releaseID string) (ReleaseSequencingPlanRead, error) {
	s.read.CompanyID, s.read.ProjectID, s.read.ReleaseID = companyID, projectID, releaseID
	return s.read, nil
}

func (s *sequencingTestService) ChangeRoadmap(_ context.Context, companyID, action string, request RoadmapChange) (RoadmapChangeResult, error) {
	s.changes++
	s.action, s.request = action, request
	return RoadmapChangeResult{CompanyID: companyID, ProjectID: request.ProjectID, ID: request.ID, Action: action, RoadmapRevision: 8}, nil
}

func TestReleaseSequencingPlanHTTPContract(t *testing.T) {
	service := &sequencingTestService{testService: &testService{}, read: ReleaseSequencingPlanRead{CompanyID: "company-a", ProjectID: "project-a", ReleaseID: "release-a", RoadmapRevision: 7, State: "absent"}}
	handler := NewHandler(service)
	base := "/internal/v1/companies/company-a/projects/project-a/roadmap/releases/release-a/sequencing-plan"
	cases := []struct {
		method, suffix, body, want string
		status                     int
	}{
		{http.MethodGet, "", "", `"state":"absent"`, http.StatusOK},
		{http.MethodGet, "/template", "", `"expected_roadmap_revision":7`, http.StatusOK},
		{http.MethodPut, "", `{"project_id":"project-a","id":"release-a","expected_roadmap_revision":7,"operation_id":"set-plan-1","sequencing_plan":{"title":"Draft","rows":[{"todo_id":"todo-a","depends_on":[],"rationale":"Order","uncertainties":[],"checkpoint_proposal":"provisional"}]}}`, `"action":"set-sequencing-plan"`, http.StatusOK},
		{http.MethodDelete, "", `{"project_id":"project-a","id":"release-a","expected_roadmap_revision":8,"operation_id":"clear-plan-1"}`, `"action":"clear-sequencing-plan"`, http.StatusOK},
		{http.MethodPut, "", `{"project_id":"other","id":"release-a","expected_roadmap_revision":7,"operation_id":"bad-plan","sequencing_plan":{"title":"Draft","rows":[]}}`, `"error"`, http.StatusConflict},
		{http.MethodPut, "", `{"project_id":"project-a","id":"release-a","expected_roadmap_revision":7,"operation_id":"bad-plan","unexpected":{"nested":true},"sequencing_plan":{"title":"Draft","rows":[]}}`, `"error"`, http.StatusBadRequest},
		{http.MethodPut, "", `{"project_id":"project-a","id":"other-release","expected_roadmap_revision":7,"operation_id":"bad-plan","sequencing_plan":{"title":"Draft","rows":[]}}`, `"teach":"TEACH: id `, http.StatusConflict},
	}
	for _, check := range cases {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(check.method, base+check.suffix, strings.NewReader(check.body)))
		if response.Code != check.status || !strings.Contains(response.Body.String(), check.want) {
			t.Fatalf("%s %s: %d %s, want %d containing %q", check.method, check.suffix, response.Code, response.Body, check.status, check.want)
		}
	}
	if service.changes != 2 || service.action != "clear-sequencing-plan" || service.request.ID != "release-a" {
		t.Fatalf("writes=%d action=%q request=%+v", service.changes, service.action, service.request)
	}
}

func TestReleaseSequencingPlanValidation(t *testing.T) {
	valid := &ReleaseSequencingPlan{Title: "Draft", Rows: []ReleaseSequencingPlanRow{
		{TODOID: "todo-a", DependsOn: []string{}, Rationale: "First", Uncertainties: []string{}, CheckpointProposal: "Provisional"},
		{TODOID: "todo-b", DependsOn: []string{"todo-a"}, Rationale: "Second", Uncertainties: []string{}, CheckpointProposal: "Pending"},
	}}
	if err := validateReleaseSequencingPlan(valid); err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		name string
		edit func(*ReleaseSequencingPlan)
	}{
		{"duplicate TODO", func(v *ReleaseSequencingPlan) { v.Rows[1].TODOID = v.Rows[0].TODOID }},
		{"forward dependency", func(v *ReleaseSequencingPlan) { v.Rows[0].DependsOn = []string{"todo-b"} }},
		{"self dependency", func(v *ReleaseSequencingPlan) { v.Rows[0].DependsOn = []string{"todo-a"} }},
		{"too many rows", func(v *ReleaseSequencingPlan) { v.Rows = make([]ReleaseSequencingPlanRow, 501) }},
		{"field byte limit", func(v *ReleaseSequencingPlan) { v.Rows[0].Rationale = strings.Repeat("x", 2049) }},
		{"title rune limit", func(v *ReleaseSequencingPlan) { v.Title = strings.Repeat("x", 161) }},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			copy := *valid
			copy.Rows = append([]ReleaseSequencingPlanRow(nil), valid.Rows...)
			check.edit(&copy)
			if err := validateReleaseSequencingPlan(&copy); err == nil {
				t.Fatal("invalid plan accepted")
			}
		})
	}
	large := &ReleaseSequencingPlan{Title: "Draft", Rows: make([]ReleaseSequencingPlanRow, 200)}
	for i := range large.Rows {
		large.Rows[i] = ReleaseSequencingPlanRow{TODOID: fmt.Sprintf("todo-%03d", i), DependsOn: []string{}, Rationale: strings.Repeat("r", 2048), Uncertainties: []string{}, CheckpointProposal: strings.Repeat("c", 2048)}
	}
	if err := validateReleaseSequencingPlan(large); err == nil {
		t.Fatal("document above 512 KiB was accepted")
	}
}

func TestMongoReleaseSequencingPlanDraftHTTPAndStaleRead(t *testing.T) {
	store, ctx := releaseSequencingMongoFixture(t)
	server := httptest.NewServer(NewHandler(store))
	defer server.Close()
	planPath := "/internal/v1/companies/company-a/projects/builder/roadmap/releases/release-a/sequencing-plan"
	call := func(method, path string, body any) (int, []byte) {
		t.Helper()
		status, responseBody := sequencingFixtureHTTP(t, server, method, path, body)
		return status, responseBody
	}
	status, body := call(http.MethodGet, planPath, nil)
	var before ReleaseSequencingPlanRead
	if status != http.StatusOK || json.Unmarshal(body, &before) != nil || before.State != "absent" || len(before.CurrentMembers) != 2 || before.CurrentMembers[0].Stage != "Planning" || before.CurrentMembers[1].Stage != "Planning" {
		t.Fatalf("unbound Planning fixture HTTP read = %d %s", status, body)
	}
	plan := &ReleaseSequencingPlan{Title: "Draft", Rows: []ReleaseSequencingPlanRow{
		{TODOID: "todo-b", DependsOn: []string{}, Rationale: "Second proposal", Uncertainties: []string{"Estimate remains provisional"}, CheckpointProposal: "Review its boundary near execution"},
		{TODOID: "todo-a", DependsOn: []string{"todo-b"}, Rationale: "Follows the prerequisite", Uncertainties: []string{}, CheckpointProposal: "Pending current approval"},
	}}
	setPlan := func(revision int64, operation string, proposed *ReleaseSequencingPlan) {
		t.Helper()
		request := RoadmapChange{ProjectID: "builder", ID: "release-a", ExpectedRoadmapRevision: int64Pointer(revision), OperationID: operation, SequencingPlan: proposed}
		status, body := call(http.MethodPut, planPath, request)
		if status != http.StatusOK {
			t.Fatalf("set Draft %s = %d %s", operation, status, body)
		}
	}
	setPlan(before.RoadmapRevision, "set-sequence-plan-01", plan)
	status, body = call(http.MethodGet, planPath, nil)
	var after ReleaseSequencingPlanRead
	if status != http.StatusOK || json.Unmarshal(body, &after) != nil || after.State != "available" || after.Document == nil || len(after.Document.Rows) != 2 || after.Document.Rows[0].TODOID != "todo-b" || len(after.StaleReferences) != 0 {
		t.Fatalf("saved Draft HTTP readback = %d %s", status, body)
	}
	if after.CurrentMembers[0].TODOID != "todo-a" || after.CurrentMembers[1].TODOID != "todo-b" || after.CurrentMembers[0].Stage != "Planning" || after.CurrentMembers[1].Stage != "Planning" {
		t.Fatalf("proposal changed canonical membership order or TODO stages: %+v", after.CurrentMembers)
	}
	initialStudy, err := store.ReadReleaseInvestigation(ctx, "company-a", "builder", "release-a")
	if err != nil || initialStudy.Document == nil || initialStudy.Document.Title != "Separate study" {
		t.Fatalf("sequencing write changed Release Investigation: %+v, %v", initialStudy, err)
	}
	initialProject, err := store.Project(ctx, "company-a", "builder")
	if err != nil || initialProject.RoadmapRevision != after.RoadmapRevision {
		t.Fatalf("plan read changed Project Roadmap revision: %+v, %v", initialProject, err)
	}

	// A known content and baseline edit remains Planning and is visible through
	// the saved-plan membership read without changing the plan itself.
	todoRead, err := store.ReadTODO(ctx, "company-a", "builder", "todo-a", nil)
	if err != nil {
		t.Fatal(err)
	}
	content := TODOAuthoredContent{TODOContent: validTODOContent(), Title: todoRead.TODO.Title, Objective: "Known Planning content and baseline change", DiffExpectation: []TODORepositoryScope{{Repository: "Builder", RepositoryID: "owner/repo", DeliveryBranch: "builder", Baseline: strings.Repeat("a", 40), ExpectedPaths: []string{"go-app/**"}, NotExpectedPaths: []string{"flutter-app/**"}}}, AffectedFiles: []string{}}
	content.Planning = &TODOPlanning{Complexity: "small", AuditTriggers: testAuditTriggers(), Assumptions: []TODOPlanningAssumption{}, Reviews: []TODOPlanningReview{{Kind: "critique", Reference: "review/sequence-critique"}}}
	contentEdit := TODOContentEditRequest{ProjectID: "builder", TODOID: "todo-a", ExpectedRevision: &todoRead.TODORevision, OperationID: "sequence-edit-todo-a", Content: content, Reason: "Record the current planning content and baseline"}
	contentEditResult, err := store.EditTODOContent(ctx, "company-a", contentEdit)
	if err != nil || contentEditResult.Revision != todoRead.TODORevision+1 || contentEditResult.ContentDigest == "" {
		t.Fatalf("fixture content/baseline edit = %+v, %v", contentEditResult, err)
	}
	status, body = call(http.MethodGet, "/internal/v1/companies/company-a/projects/builder/todos/todo-a", nil)
	var editedTODO TODOReadResponse
	if status != http.StatusOK || json.Unmarshal(body, &editedTODO) != nil || editedTODO.TODORevision != contentEditResult.Revision || editedTODO.ContentDigest != contentEditResult.ContentDigest || editedTODO.TODO.DiffExpectation[0].Baseline != strings.Repeat("a", 40) {
		t.Fatalf("known content/baseline HTTP readback = %d %s", status, body)
	}
	status, body = call(http.MethodGet, planPath, nil)
	if status != http.StatusOK || json.Unmarshal(body, &after) != nil || after.CurrentMembers[0].Revision != contentEditResult.Revision || len(after.Document.Rows) != 2 || after.Document.Rows[0].TODOID != "todo-b" {
		t.Fatalf("saved plan did not reflect current content before refresh = %d %s", status, body)
	}

	// Add a member and change canonical placement through existing APIs. The
	// proposal remains independent until explicitly refreshed below.
	added := mongoBornTODOFixture()
	added.ID, added.ProjectID, added.ReleaseID, added.Position = "todo-c", "builder", "release-a", intPointer(2)
	status, body = call(http.MethodPost, "/internal/v1/companies/company-a/projects/builder/todos", TODOAdmissionRequest{SchemaVersion: todoCreationSchemaVersion, OperationID: "admit-sequence-todo-c", TODO: added})
	if status != http.StatusOK {
		t.Fatalf("add current TODO member = %d %s", status, body)
	}
	project, err := store.Project(ctx, "company-a", "builder")
	if err != nil {
		t.Fatal(err)
	}
	aRead, err := store.ReadTODO(ctx, "company-a", "builder", "todo-a", nil)
	if err != nil {
		t.Fatal(err)
	}
	status, body = call(http.MethodPost, "/internal/v1/companies/company-a/projects/builder/todos/todo-a/placement", TODOPlacementEditRequest{ProjectID: "builder", TODOID: "todo-a", ExpectedRevision: &aRead.TODORevision, ExpectedRoadmapRevision: &project.RoadmapRevision, OperationID: "sequence-move-todo-a", ReleaseID: "release-a", Position: 3, Reason: "Exercise current placement refresh"})
	if status != http.StatusOK {
		t.Fatalf("move current TODO member = %d %s", status, body)
	}
	status, body = call(http.MethodGet, planPath, nil)
	if status != http.StatusOK || json.Unmarshal(body, &after) != nil || len(after.Document.Rows) != 2 || len(after.CurrentMembers) != 3 || after.CurrentMembers[0].TODOID != "todo-b" || after.CurrentMembers[1].TODOID != "todo-c" || after.CurrentMembers[2].TODOID != "todo-a" || after.CurrentMembers[2].Revision != 2 {
		t.Fatalf("current membership/content/placement not refreshed by plan read = %d %s", status, body)
	}
	refreshed := &ReleaseSequencingPlan{Title: "Refreshed Draft", Rows: []ReleaseSequencingPlanRow{
		{TODOID: "todo-c", DependsOn: []string{}, Rationale: "Newly admitted prerequisite", Uncertainties: []string{}, CheckpointProposal: "Revisit when approved"},
		{TODOID: "todo-b", DependsOn: []string{"todo-c"}, Rationale: "Follow the added prerequisite", Uncertainties: []string{}, CheckpointProposal: "Choose after current review"},
		{TODOID: "todo-a", DependsOn: []string{"todo-b"}, Rationale: "Follow the moved member", Uncertainties: []string{}, CheckpointProposal: "Provisional"},
	}}
	setPlan(after.RoadmapRevision, "set-sequence-plan-refresh", refreshed)
	status, body = call(http.MethodGet, planPath, nil)
	if status != http.StatusOK || json.Unmarshal(body, &after) != nil || len(after.Document.Rows) != 3 || after.Document.Rows[0].TODOID != "todo-c" {
		t.Fatalf("refreshed saved plan HTTP read = %d %s", status, body)
	}
	project, err = store.Project(ctx, "company-a", "builder")
	if err != nil {
		t.Fatal(err)
	}
	status, body = call(http.MethodPost, "/internal/v1/companies/company-a/projects/builder/roadmap/epics/epic-a/releases", RoadmapChange{ProjectID: "builder", ExpectedRoadmapRevision: int64Pointer(project.RoadmapRevision), OperationID: "create-sequence-other-release", ID: "release-other", EpicID: "epic-a", Name: "Other release"})
	if status != http.StatusOK {
		t.Fatalf("create second Release for moved-reference fixture = %d %s", status, body)
	}
	cRead, err := store.ReadTODO(ctx, "company-a", "builder", "todo-c", nil)
	if err != nil {
		t.Fatal(err)
	}
	project, err = store.Project(ctx, "company-a", "builder")
	if err != nil {
		t.Fatal(err)
	}
	move := func(releaseID, operation string) {
		t.Helper()
		request := TODOPlacementEditRequest{ProjectID: "builder", TODOID: "todo-c", ExpectedRevision: &cRead.TODORevision, ExpectedRoadmapRevision: &project.RoadmapRevision, OperationID: operation, ReleaseID: releaseID, Position: 0, Reason: "Exercise current Release membership diagnostics"}
		status, response := call(http.MethodPost, "/internal/v1/companies/company-a/projects/builder/todos/todo-c/placement", request)
		if status != http.StatusOK {
			t.Fatalf("move planned member to %s = %d %s", releaseID, status, response)
		}
		cRead, err = store.ReadTODO(ctx, "company-a", "builder", "todo-c", nil)
		if err != nil {
			t.Fatal(err)
		}
		project, err = store.Project(ctx, "company-a", "builder")
		if err != nil {
			t.Fatal(err)
		}
	}
	move("release-other", "move-sequence-member-other-release")
	status, body = call(http.MethodGet, planPath, nil)
	if status != http.StatusOK || json.Unmarshal(body, &after) != nil || len(after.StaleReferences) != 1 || after.StaleReferences[0].TODOID != "todo-c" || after.StaleReferences[0].Status != "moved" || after.StaleReferences[0].CurrentReleaseID != "release-other" {
		t.Fatalf("moved planned reference diagnostic = %d %s", status, body)
	}
	move("release-a", "restore-sequence-member-release")

	// A real removal is returned as the documented generic missing reference
	// (absent or removed); the active membership list excludes it.
	cRead, err = store.ReadTODO(ctx, "company-a", "builder", "todo-c", nil)
	if err != nil {
		t.Fatal(err)
	}
	status, body = call(http.MethodPost, "/internal/v1/companies/company-a/projects/builder/todos/todo-c/remove", TODORemovalRequest{ProjectID: "builder", TODOID: "todo-c", ExpectedRevision: &cRead.TODORevision, OperationID: "remove-sequence-todo-c", Actor: "fixture-owner", Reason: "Remove the planning fixture member"})
	if status != http.StatusOK {
		t.Fatalf("remove current TODO member = %d %s", status, body)
	}
	status, body = call(http.MethodGet, planPath, nil)
	if status != http.StatusOK || json.Unmarshal(body, &after) != nil || len(after.CurrentMembers) != 2 || len(after.StaleReferences) != 1 || after.StaleReferences[0].TODOID != "todo-c" || after.StaleReferences[0].Status != "missing" {
		t.Fatalf("removed planned reference diagnostic = %d %s", status, body)
	}
	currentPlan := &ReleaseSequencingPlan{Title: "Current Draft", Rows: []ReleaseSequencingPlanRow{
		{TODOID: "todo-b", DependsOn: []string{}, Rationale: "Reconciled after the prerequisite was removed", Uncertainties: []string{}, CheckpointProposal: "Choose after current review"},
		refreshed.Rows[2],
	}}
	setPlan(after.RoadmapRevision, "set-sequence-plan-remove-stale", currentPlan)

	// Give the code-backed TODO the ordinary prerequisites for the current
	// Planning -> pending -> Ready lifecycle, independently of its sequence.
	if _, err := store.ChangeImplementationRepository(ctx, "company-a", "builder", ImplementationRepositoryChange{Action: "confirm", ProjectID: "builder", OperationID: "sequence-confirm-repo", ExpectedRevision: 0, Actor: "fixture-owner", OwnerReference: "fixture-owner-confirmed-owner-repo", RepositoryID: "owner/repo"}); err != nil {
		t.Fatalf("confirm fixture implementation repository: %v", err)
	}
	project, err = store.Project(ctx, "company-a", "builder")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ChangeRoadmap(ctx, "company-a", "confirm-release-branch", RoadmapChange{ProjectID: "builder", ExpectedRoadmapRevision: int64Pointer(project.RoadmapRevision), OperationID: "sequence-confirm-branch", ID: "release-a", RepositoryID: "owner/repo", Branch: "builder", OwnerReference: "fixture-owner-confirmed-release-branch"}); err != nil {
		t.Fatalf("confirm fixture Release branch: %v", err)
	}
	postMoveRead, err := store.ReadTODO(ctx, "company-a", "builder", "todo-a", nil)
	if err != nil {
		t.Fatal(err)
	}
	stalePlacementRequest := TODOActionRequest{ProjectID: "builder", TODOID: "todo-a", ExpectedRevision: &postMoveRead.TODORevision, OperationID: "sequence-reject-stale-placement-review", RequestedStage: "Implementation-Approval-Pending", ProofReferences: []TODOProofReference{}, WaiverReferences: []TODOProofReference{}}
	status, body = call(http.MethodPost, "/internal/v1/companies/company-a/projects/builder/todos/todo-a/transition", stalePlacementRequest)
	if status != http.StatusConflict {
		t.Fatalf("stale pre-move Planning review transition = %d %s, want conflict", status, body)
	}
	stillPlanning, err := store.ReadTODO(ctx, "company-a", "builder", "todo-a", nil)
	if err != nil || stillPlanning.TODORevision != postMoveRead.TODORevision || stillPlanning.TODO.Stage != "Planning" {
		t.Fatalf("stale placement review rejection changed TODO: %+v, %v", stillPlanning.TODO, err)
	}
	refreshedContent := contentFromTODO(stillPlanning.TODO)
	refreshedContent.Planning.Reviews[0].Reference = "review/sequence-placement-refresh"
	refreshedContent.Planning.Reviews = append(refreshedContent.Planning.Reviews, TODOPlanningReview{Kind: "coherence", Reference: "review/sequence-placement-coherence"})
	contentRefresh, err := store.EditTODOContent(ctx, "company-a", TODOContentEditRequest{ProjectID: "builder", TODOID: "todo-a", ExpectedRevision: &stillPlanning.TODORevision, OperationID: "sequence-refresh-placement-review", Content: refreshedContent, Reason: "Review the current Planning placement"})
	if err != nil || contentRefresh.Revision != stillPlanning.TODORevision+1 || contentRefresh.ContentDigest == "" {
		t.Fatalf("refresh current placement Planning review: %+v, %v", contentRefresh, err)
	}
	status, body = call(http.MethodGet, "/internal/v1/companies/company-a/projects/builder/todos/todo-a", nil)
	var refreshedTODO TODOReadResponse
	if status != http.StatusOK || json.Unmarshal(body, &refreshedTODO) != nil || refreshedTODO.TODORevision != contentRefresh.Revision || refreshedTODO.ContentDigest != contentRefresh.ContentDigest {
		t.Fatalf("current reviewed TODO HTTP readback = %d %s", status, body)
	}
	status, body = call(http.MethodGet, planPath, nil)
	if status != http.StatusOK || json.Unmarshal(body, &after) != nil || after.CurrentMembers[1].TODOID != "todo-a" || after.CurrentMembers[1].Revision != contentRefresh.Revision {
		t.Fatalf("current reviewed plan HTTP readback = %d %s", status, body)
	}
	currentTODORevision := recordPlanningReviewSubjects(t, ctx, store, "builder", "todo-a", "sequence-review-subject")
	pendingRequest := TODOActionRequest{ProjectID: "builder", TODOID: "todo-a", ExpectedRevision: &currentTODORevision, OperationID: "sequence-normal-pending", RequestedStage: "Implementation-Approval-Pending", ProofReferences: []TODOProofReference{}, WaiverReferences: []TODOProofReference{}}
	status, body = call(http.MethodPost, "/internal/v1/companies/company-a/projects/builder/todos/todo-a/transition", pendingRequest)
	if status != http.StatusOK {
		t.Fatalf("normal Planning approval transition = %d %s", status, body)
	}
	pendingRead, err := store.ReadTODO(ctx, "company-a", "builder", "todo-a", nil)
	if err != nil || pendingRead.TODO.Stage != "Implementation-Approval-Pending" {
		t.Fatalf("current TODO did not reach approval pending: %+v, %v", pendingRead.TODO, err)
	}
	readyRequest := func(operation string, proof []TODOProofReference) TODOActionRequest {
		return TODOActionRequest{ProjectID: "builder", TODOID: "todo-a", ExpectedRevision: &pendingRead.TODORevision, OperationID: operation, RequestedStage: "Ready-for-Implementation", ProofReferences: proof, WaiverReferences: []TODOProofReference{}}
	}
	for _, attempt := range []struct {
		name string
		body TODOActionRequest
	}{
		{"absent owner approval", readyRequest("sequence-ready-no-approval", []TODOProofReference{})},
		{"stale owner approval", readyRequest("sequence-ready-stale-approval", []TODOProofReference{{Kind: "owner-approval", Source: "owner-chat", Reference: "APROVADO", SourceDigest: "sha256:" + strings.Repeat("0", 64), VerificationState: "unverified"}})},
	} {
		status, body = call(http.MethodPost, "/internal/v1/companies/company-a/projects/builder/todos/todo-a/transition", attempt.body)
		if status != http.StatusConflict {
			t.Fatalf("%s transition = %d %s, want conflict", attempt.name, status, body)
		}
	}
	unchanged, err := store.ReadTODO(ctx, "company-a", "builder", "todo-a", nil)
	if err != nil || unchanged.TODORevision != pendingRead.TODORevision || unchanged.TODO.Stage != "Implementation-Approval-Pending" {
		t.Fatalf("rejected absent/stale approvals changed TODO authority: %+v, %v", unchanged.TODO, err)
	}
	status, body = call(http.MethodGet, planPath, nil)
	var stillPlanned ReleaseSequencingPlanRead
	if status != http.StatusOK || json.Unmarshal(body, &stillPlanned) != nil || stillPlanned.State != "available" || len(stillPlanned.Document.Rows) != 2 {
		t.Fatalf("rejected approval altered the saved Draft: %d %s", status, body)
	}
	currentApproval := TODOProofReference{Kind: "owner-approval", Source: "owner-chat", Reference: "APROVADO", SourceDigest: pendingRead.ContentDigest, VerificationState: "unverified"}
	status, body = call(http.MethodPost, "/internal/v1/companies/company-a/projects/builder/todos/todo-a/transition", readyRequest("sequence-ready-current-approval", []TODOProofReference{currentApproval}))
	if status != http.StatusOK {
		t.Fatalf("current normal owner approval transition = %d %s", status, body)
	}
	readyRead, err := store.ReadTODO(ctx, "company-a", "builder", "todo-a", nil)
	if err != nil || readyRead.TODO.Stage != "Ready-for-Implementation" || readyRead.TODO.VerifiedTransitionRef == nil || readyRead.TODO.VerifiedTransitionRef.SourceDigest != pendingRead.ContentDigest {
		t.Fatalf("plan supplied approval or normal lifecycle failed: %+v, %v", readyRead.TODO, err)
	}
	inProgressRequest := TODOActionRequest{ProjectID: "builder", TODOID: "todo-a", ExpectedRevision: &readyRead.TODORevision, OperationID: "sequence-normal-in-progress", RequestedStage: "In-Progress", ProofReferences: []TODOProofReference{}, WaiverReferences: []TODOProofReference{}}
	status, body = call(http.MethodPost, "/internal/v1/companies/company-a/projects/builder/todos/todo-a/transition", inProgressRequest)
	if status != http.StatusOK {
		t.Fatalf("normal Ready-to-In-Progress transition = %d %s", status, body)
	}

	// Clearing the planning note remains independent from branches, Investigation,
	// canonical order and lifecycle state.
	project, err = store.Project(ctx, "company-a", "builder")
	if err != nil {
		t.Fatal(err)
	}
	database := store.client.Database(store.tenantDatabase("company-a"))
	var branchesBefore roadmapRelease
	if err := database.Collection(releasesCollection).FindOne(ctx, bson.D{{Key: "project_id", Value: "builder"}, {Key: "id", Value: "release-a"}}).Decode(&branchesBefore); err != nil || len(branchesBefore.DeliveryBranches) != 1 {
		t.Fatalf("fixture Release branch before clear = %+v, %v", branchesBefore, err)
	}
	clearRequest := RoadmapChange{ProjectID: "builder", ID: "release-a", ExpectedRoadmapRevision: int64Pointer(project.RoadmapRevision), OperationID: "clear-sequence-plan-final"}
	status, body = call(http.MethodDelete, planPath, clearRequest)
	if status != http.StatusOK {
		t.Fatalf("clear sequencing plan = %d %s", status, body)
	}
	status, body = call(http.MethodGet, planPath, nil)
	var cleared ReleaseSequencingPlanRead
	if status != http.StatusOK || json.Unmarshal(body, &cleared) != nil || cleared.State != "absent" {
		t.Fatalf("cleared plan HTTP read = %d %s", status, body)
	}
	var branchesAfter roadmapRelease
	if err := database.Collection(releasesCollection).FindOne(ctx, bson.D{{Key: "project_id", Value: "builder"}, {Key: "id", Value: "release-a"}}).Decode(&branchesAfter); err != nil || !reflect.DeepEqual(branchesBefore.DeliveryBranches, branchesAfter.DeliveryBranches) {
		t.Fatalf("clear changed Release branch data: before=%+v after=%+v err=%v", branchesBefore.DeliveryBranches, branchesAfter.DeliveryBranches, err)
	}
	study, err := store.ReadReleaseInvestigation(ctx, "company-a", "builder", "release-a")
	if err != nil || study.Document == nil || study.Document.Title != "Separate study" {
		t.Fatalf("plan lifecycle changed Release Investigation: %+v, %v", study, err)
	}
	finalRead, err := store.ReadReleaseSequencingPlan(ctx, "company-a", "builder", "release-a")
	if err != nil || len(finalRead.CurrentMembers) != 2 || finalRead.CurrentMembers[0].TODOID != "todo-b" || finalRead.CurrentMembers[1].TODOID != "todo-a" || finalRead.CurrentMembers[0].Stage != "Planning" || finalRead.CurrentMembers[1].Stage != "In-Progress" {
		t.Fatalf("plan lifecycle changed active TODO order/stages: %+v, %v", finalRead.CurrentMembers, err)
	}
}

func TestMongoReleaseSequencingPlanHTTPMutationRetriesAndCAS(t *testing.T) {
	store, _ := releaseSequencingMongoFixture(t)
	handler := NewHandler(store)
	server := httptest.NewServer(handler)
	defer server.Close()
	read, err := store.ReadReleaseSequencingPlan(context.Background(), "company-a", "builder", "release-a")
	if err != nil {
		t.Fatal(err)
	}
	baseRevision := read.RoadmapRevision
	planFor := func(todoID, rationale string) *ReleaseSequencingPlan {
		return &ReleaseSequencingPlan{Title: "Draft", Rows: []ReleaseSequencingPlanRow{{TODOID: todoID, DependsOn: []string{}, Rationale: rationale, Uncertainties: []string{}, CheckpointProposal: "Provisional"}}}
	}
	first := RoadmapChange{ProjectID: "builder", ID: "release-a", ExpectedRoadmapRevision: int64Pointer(baseRevision), OperationID: "seq-retry-one", SequencingPlan: planFor("todo-a", "First proposal")}
	set := func(request RoadmapChange) *httptest.ResponseRecorder {
		return sequencingHTTP(t, handler, http.MethodPut, request)
	}
	winner := set(first)
	if winner.Code != http.StatusOK {
		t.Fatalf("initial set = %d %s", winner.Code, winner.Body)
	}
	replay := set(first)
	if replay.Code != http.StatusOK || !strings.Contains(replay.Body.String(), `"no_op":true`) {
		t.Fatalf("adjacent identical retry = %d %s", replay.Code, replay.Body)
	}
	divergent := first
	divergent.SequencingPlan = planFor("todo-b", "Divergent payload")
	if response := set(divergent); response.Code != http.StatusConflict {
		t.Fatalf("divergent operation-ID reuse = %d %s", response.Code, response.Body)
	}
	current, err := store.ReadReleaseSequencingPlan(context.Background(), "company-a", "builder", "release-a")
	if err != nil {
		t.Fatal(err)
	}
	other := RoadmapChange{ProjectID: "builder", ID: "release-a", ExpectedRoadmapRevision: int64Pointer(current.RoadmapRevision), OperationID: "seq-intervening-write", SequencingPlan: planFor("todo-b", "Intervening update")}
	if response := set(other); response.Code != http.StatusOK {
		t.Fatalf("intervening set = %d %s", response.Code, response.Body)
	}
	if response := set(first); response.Code != http.StatusConflict {
		t.Fatalf("stale retry after intervening Roadmap write = %d %s", response.Code, response.Body)
	}
	current, err = store.ReadReleaseSequencingPlan(context.Background(), "company-a", "builder", "release-a")
	if err != nil || current.Document.Rows[0].TODOID != "todo-b" {
		t.Fatalf("stale retry changed plan: %+v, %v", current, err)
	}

	concurrentSetSet(t, store, server, current.RoadmapRevision)
	current, err = store.ReadReleaseSequencingPlan(context.Background(), "company-a", "builder", "release-a")
	if err != nil {
		t.Fatal(err)
	}
	concurrentSetClear(t, store, server, current.RoadmapRevision)
}

func TestMongoReleaseSequencingPlanRejectsCrossScopeReferencesWithoutWrites(t *testing.T) {
	store, ctx := releaseSequencingMongoFixture(t)
	project, err := store.Project(ctx, "company-a", "builder")
	if err != nil {
		t.Fatal(err)
	}
	revision := project.RoadmapRevision
	if _, err := store.ChangeRoadmap(ctx, "company-a", "create-release", RoadmapChange{ProjectID: "builder", ExpectedRoadmapRevision: int64Pointer(revision), OperationID: "create-foreign-release", ID: "release-foreign", EpicID: "epic-a", Name: "Foreign release"}); err != nil {
		t.Fatal(err)
	}
	if err := addSequencingMongoScope(t, store, "company-a", "builder", "release-foreign", "todo-foreign-release"); err != nil {
		t.Fatal(err)
	}
	if err := addSequencingMongoScope(t, store, "company-a", "other-project", "release-other-project", "todo-other-project"); err != nil {
		t.Fatal(err)
	}
	if err := addSequencingMongoScope(t, store, "company-b", "other-company", "release-other-company", "todo-other-company"); err != nil {
		t.Fatal(err)
	}
	project, err = store.Project(ctx, "company-a", "builder")
	if err != nil {
		t.Fatal(err)
	}
	revision = project.RoadmapRevision
	for index, todoID := range []string{"todo-foreign-release", "todo-other-project", "todo-other-company"} {
		request := RoadmapChange{ProjectID: "builder", ID: "release-a", ExpectedRoadmapRevision: int64Pointer(revision), OperationID: fmt.Sprintf("reject-cross-scope-%d", index), SequencingPlan: &ReleaseSequencingPlan{Title: "Draft", Rows: []ReleaseSequencingPlanRow{{TODOID: todoID, DependsOn: []string{}, Rationale: "Invalid scope", Uncertainties: []string{}, CheckpointProposal: "Provisional"}}}}
		if _, err := store.ChangeRoadmap(ctx, "company-a", "set-sequencing-plan", request); !errors.Is(err, ErrInvalid) {
			t.Fatalf("cross-scope TODO %q result = %v, want invalid", todoID, err)
		}
		current, err := store.Project(ctx, "company-a", "builder")
		read, readErr := store.ReadReleaseSequencingPlan(ctx, "company-a", "builder", "release-a")
		if err != nil || readErr != nil || current.RoadmapRevision != revision || read.State != "absent" {
			t.Fatalf("invalid TODO %q caused partial plan/revision write: revision=%+v read=%+v errors=%v/%v", todoID, current, read, err, readErr)
		}
	}
}

func addSequencingMongoScope(t *testing.T, store *MongoStore, companyID, projectID, releaseID, todoID string) error {
	t.Helper()
	ctx := context.Background()
	if companyID != "company-a" || projectID != "builder" {
		companyName := "Company A"
		if companyID != "company-a" {
			companyName = "Company B"
		}
		if _, err := store.Register(ctx, Registration{CompanyID: companyID, CompanyName: companyName, ProjectID: projectID, ProjectName: projectID}); err != nil {
			return err
		}
		if err := store.ProvisionTODOCollection(ctx, companyID); err != nil {
			return err
		}
	}
	if companyID != "company-a" || projectID != "builder" {
		if _, err := store.ChangeRoadmap(ctx, companyID, "create-epic", RoadmapChange{ProjectID: projectID, ExpectedRoadmapRevision: int64Pointer(0), OperationID: "create-foreign-epic-" + projectID, ID: "epic-foreign", Name: "Planning"}); err != nil {
			return err
		}
		if _, err := store.ChangeRoadmap(ctx, companyID, "create-release", RoadmapChange{ProjectID: projectID, ExpectedRoadmapRevision: int64Pointer(1), OperationID: "create-foreign-release-" + projectID, ID: releaseID, EpicID: "epic-foreign", Name: "Planning"}); err != nil {
			return err
		}
	}
	record := mongoBornTODOFixture()
	record.ID, record.ProjectID, record.ReleaseID, record.Position = todoID, projectID, releaseID, intPointer(0)
	_, err := store.AdmitTODO(ctx, companyID, projectID, TODOAdmissionRequest{SchemaVersion: todoCreationSchemaVersion, OperationID: "admit-foreign-" + projectID, TODO: record})
	return err
}

type sequencingHTTPOutcome struct {
	status int
	value  string
	body   string
}

func concurrentSetSet(t *testing.T, store *MongoStore, server *httptest.Server, revision int64) {
	t.Helper()
	start := make(chan struct{})
	responses := make(chan sequencingHTTPOutcome, 2)
	var wait sync.WaitGroup
	for i, id := range []string{"todo-a", "todo-b"} {
		request := RoadmapChange{ProjectID: "builder", ID: "release-a", ExpectedRoadmapRevision: int64Pointer(revision), OperationID: "seq-race-set-" + strconv.Itoa(i), SequencingPlan: &ReleaseSequencingPlan{Title: "Draft", Rows: []ReleaseSequencingPlanRow{{TODOID: id, DependsOn: []string{}, Rationale: "Concurrent proposal", Uncertainties: []string{}, CheckpointProposal: "Provisional"}}}}
		wait.Add(1)
		go func(id string) {
			defer wait.Done()
			<-start
			responses <- sequencingHTTPClient(t, server, http.MethodPut, request, id)
		}(id)
	}
	close(start)
	wait.Wait()
	close(responses)
	winner, ok, conflict := countHTTPOutcomes(responses)
	if ok != 1 || conflict != 1 {
		t.Fatalf("concurrent set/set outcomes: success=%d conflict=%d winner=%q", ok, conflict, winner)
	}
	read, err := store.ReadReleaseSequencingPlan(context.Background(), "company-a", "builder", "release-a")
	if err != nil || read.State != "available" || read.RoadmapRevision != revision+1 || len(read.Document.Rows) != 1 || read.Document.Rows[0].TODOID != winner {
		t.Fatalf("concurrent set/set persisted state = %+v, winner=%q, %v", read, winner, err)
	}
	assertSequencingFixturePreserved(t, store)
}

func concurrentSetClear(t *testing.T, store *MongoStore, server *httptest.Server, revision int64) {
	t.Helper()
	start := make(chan struct{})
	responses := make(chan sequencingHTTPOutcome, 2)
	requests := []struct {
		method string
		body   RoadmapChange
	}{
		{http.MethodPut, RoadmapChange{ProjectID: "builder", ID: "release-a", ExpectedRoadmapRevision: int64Pointer(revision), OperationID: "seq-race-mixed-set", SequencingPlan: &ReleaseSequencingPlan{Title: "Draft", Rows: []ReleaseSequencingPlanRow{{TODOID: "todo-a", DependsOn: []string{}, Rationale: "Race set", Uncertainties: []string{}, CheckpointProposal: "Provisional"}}}}},
		{http.MethodDelete, RoadmapChange{ProjectID: "builder", ID: "release-a", ExpectedRoadmapRevision: int64Pointer(revision), OperationID: "seq-race-mixed-clear"}},
	}
	var wait sync.WaitGroup
	for _, item := range requests {
		item := item
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			responses <- sequencingHTTPClient(t, server, item.method, item.body, item.method)
		}()
	}
	close(start)
	wait.Wait()
	close(responses)
	winner, ok, conflict := countHTTPOutcomes(responses)
	if ok != 1 || conflict != 1 {
		t.Fatalf("concurrent set/clear outcomes: success=%d conflict=%d winner=%q", ok, conflict, winner)
	}
	read, err := store.ReadReleaseSequencingPlan(context.Background(), "company-a", "builder", "release-a")
	winnerCleared := winner == http.MethodDelete
	if err != nil || read.RoadmapRevision != revision+1 || winnerCleared != (read.State == "absent") {
		t.Fatalf("concurrent set/clear persisted state = %+v, winner=%q, %v", read, winner, err)
	}
	assertSequencingFixturePreserved(t, store)
}

func countHTTPOutcomes(responses <-chan sequencingHTTPOutcome) (string, int, int) {
	success, conflict := 0, 0
	winner := ""
	for response := range responses {
		if response.status == http.StatusOK {
			success++
			winner = response.value
		} else if response.status == http.StatusConflict {
			conflict++
		}
	}
	return winner, success, conflict
}

func sequencingHTTPClient(t *testing.T, server *httptest.Server, method string, request RoadmapChange, winner string) sequencingHTTPOutcome {
	t.Helper()
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	client := http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest(method, server.URL+"/internal/v1/companies/company-a/projects/builder/roadmap/releases/release-a/sequencing-plan", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	responseBody, readErr := io.ReadAll(response.Body)
	if readErr != nil {
		t.Fatalf("read concurrent %s response: %v", method, readErr)
	}
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusConflict {
		t.Errorf("concurrent %s response = %d %s", method, response.StatusCode, responseBody)
	}
	return sequencingHTTPOutcome{status: response.StatusCode, value: winner, body: string(responseBody)}
}

func sequencingFixtureHTTP(t *testing.T, server *httptest.Server, method, path string, value any) (int, []byte) {
	t.Helper()
	var body io.Reader
	if value != nil {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		body = strings.NewReader(string(encoded))
	}
	request, err := http.NewRequest(method, server.URL+path, body)
	if err != nil {
		t.Fatal(err)
	}
	if value != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	client := http.Client{Timeout: 10 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, responseBody
}

func assertSequencingFixturePreserved(t *testing.T, store *MongoStore) {
	t.Helper()
	ctx := context.Background()
	study, err := store.ReadReleaseInvestigation(ctx, "company-a", "builder", "release-a")
	if err != nil || study.Document == nil || study.Document.Title != "Separate study" {
		t.Fatalf("Investigation changed during plan CAS: %+v, %v", study, err)
	}
	read, err := store.ReadReleaseSequencingPlan(ctx, "company-a", "builder", "release-a")
	if err != nil || len(read.CurrentMembers) != 2 || read.CurrentMembers[0].TODOID != "todo-a" || read.CurrentMembers[1].TODOID != "todo-b" {
		t.Fatalf("canonical TODO order changed during plan CAS: %+v, %v", read, err)
	}
	for _, member := range read.CurrentMembers {
		if member.Stage != "Planning" {
			t.Fatalf("TODO stage changed during plan CAS: %+v", member)
		}
	}
}

func sequencingHTTP(t *testing.T, handler http.Handler, method string, request RoadmapChange) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(method, "/internal/v1/companies/company-a/projects/builder/roadmap/releases/release-a/sequencing-plan", strings.NewReader(string(body))))
	return response
}

func releaseSequencingMongoFixture(t *testing.T) (*MongoStore, context.Context) {
	t.Helper()
	uri := os.Getenv("REGISTRY_TEST_MONGODB_URI")
	if uri == "" {
		t.Skip("isolated replica-set MongoDB fixture not configured; required delivery proof sets REGISTRY_TEST_MONGODB_URI")
	}
	ctx := context.Background()
	stamp := "sequencing-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	store, err := OpenMongo(ctx, uri, stamp)
	if err != nil {
		t.Fatal(err)
	}
	store.tenantPrefix = stamp
	t.Cleanup(func() {
		_ = store.client.Database(store.tenantDatabase("company-a")).Drop(context.Background())
		_ = store.client.Database(store.tenantDatabase("company-b")).Drop(context.Background())
		_ = store.control.Drop(context.Background())
		_ = store.Close(context.Background())
	})
	if _, err := store.Register(ctx, Registration{CompanyID: "company-a", CompanyName: "Company A", ProjectID: "builder", ProjectName: "Builder"}); err != nil {
		t.Fatal(err)
	}
	if err := store.ProvisionTODOCollection(ctx, "company-a"); err != nil {
		t.Fatal(err)
	}
	revision := int64(0)
	if _, err := store.ChangeRoadmap(ctx, "company-a", "create-epic", RoadmapChange{ProjectID: "builder", ExpectedRoadmapRevision: &revision, OperationID: "create-sequence-epic", ID: "epic-a", Name: "Planning"}); err != nil {
		t.Fatal(err)
	}
	revision = 1
	if _, err := store.ChangeRoadmap(ctx, "company-a", "create-release", RoadmapChange{ProjectID: "builder", ExpectedRoadmapRevision: &revision, OperationID: "create-sequence-release", ID: "release-a", EpicID: "epic-a", Name: "Sequence"}); err != nil {
		t.Fatal(err)
	}
	revision = 2
	if _, err := store.ChangeRoadmap(ctx, "company-a", "set-investigation", RoadmapChange{ProjectID: "builder", ExpectedRoadmapRevision: &revision, OperationID: "set-sequence-investigation", ID: "release-a", Investigation: &ReleaseInvestigation{Title: "Separate study", Materials: []ReleaseInvestigationMaterial{}}}); err != nil {
		t.Fatal(err)
	}
	for index, todoID := range []string{"todo-a", "todo-b"} {
		record := mongoBornTODOFixture()
		record.ID, record.ProjectID, record.ReleaseID, record.Position = todoID, "builder", "release-a", intPointer(index)
		if _, err := store.AdmitTODO(ctx, "company-a", "builder", TODOAdmissionRequest{SchemaVersion: todoCreationSchemaVersion, OperationID: "admit-sequence-" + todoID, TODO: record}); err != nil {
			t.Fatal(err)
		}
	}
	project, err := store.Project(ctx, "company-a", "builder")
	if err != nil || project.Binding != nil {
		t.Fatalf("fixture unexpectedly has Git binding: %+v, %v", project, err)
	}
	return store, ctx
}

func TestReleaseSequencingMutationRejectsNonPlanPayloads(t *testing.T) {
	revision := int64(1)
	plan := &ReleaseSequencingPlan{Title: "Draft", Rows: []ReleaseSequencingPlanRow{}}
	base := RoadmapChange{ProjectID: "builder", ID: "release-a", ExpectedRoadmapRevision: &revision, OperationID: "sequence-op", SequencingPlan: plan}
	if validRoadmapChange("set-investigation", base) || validRoadmapChange("confirm-release-branch", base) || validRoadmapChange("edit-release", base) {
		t.Fatal("sequencing_plan was accepted by an unrelated Roadmap action")
	}
	base.Name = "unrelated field"
	if validRoadmapChange("set-sequencing-plan", base) {
		t.Fatal("set-sequencing-plan accepted an unrelated Roadmap field")
	}
	base.Name = ""
	base.SequencingPlan = nil
	base.Investigation = &ReleaseInvestigation{Title: "other payload", Materials: []ReleaseInvestigationMaterial{}}
	if validRoadmapChange("clear-sequencing-plan", base) {
		t.Fatal("clear-sequencing-plan accepted an Investigation payload")
	}
}
