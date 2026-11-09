package registry

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

type testTODORemovalService struct {
	*testService
	companyID string
	request   TODORemovalRequest
}

func (service *testTODORemovalService) RemoveTODO(_ context.Context, companyID string, request TODORemovalRequest) (TODORemovalResponse, error) {
	service.companyID, service.request = companyID, request
	return TODORemovalResponse{CompanyID: companyID, ProjectID: request.ProjectID, TODOID: request.TODOID, TODORevision: 1, Removed: true}, nil
}

func TestTODORemovalRouteRejectsCrossProjectIdentity(t *testing.T) {
	service := &testTODORemovalService{testService: &testService{}}
	body := `{"project_id":"other","todo_id":"remove-test-todo","expected_revision":0,"operation_id":"remove-001","actor":"owner","reason":"Superseded"}`
	request := httptest.NewRequest(http.MethodPost, "/internal/v1/companies/company-a/projects/builder/todos/remove-test-todo/remove", strings.NewReader(body))
	response := httptest.NewRecorder()
	NewHandler(service).ServeHTTP(response, request)
	if response.Code != http.StatusConflict || service.companyID != "" {
		t.Fatalf("cross-Project remove = %d %s; service=%+v", response.Code, response.Body.String(), service)
	}
	body = strings.Replace(body, `"project_id":"other"`, `"project_id":"builder"`, 1)
	request = httptest.NewRequest(http.MethodPost, "/internal/v1/companies/company-a/projects/builder/todos/remove-test-todo/remove", strings.NewReader(body))
	response = httptest.NewRecorder()
	NewHandler(service).ServeHTTP(response, request)
	if response.Code != http.StatusOK || service.companyID != "company-a" || service.request.TODOID != "remove-test-todo" {
		t.Fatalf("Project-scoped remove = %d %s; service=%+v", response.Code, response.Body.String(), service)
	}
}

func TestMongoRemoveTODOIsTombstonedAndIdempotent(t *testing.T) {
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
	for _, predecessor := range []struct {
		collection string
		validator  bson.D
	}{
		{todosCollection, TODOCollectionValidatorBeforeEvidenceCapacity()},
		{todoEventsCollection, TODOEventsValidatorBeforeRemoval()},
	} {
		if err := database.RunCommand(ctx, bson.D{{Key: "collMod", Value: predecessor.collection}, {Key: "validator", Value: predecessor.validator}, {Key: "validationLevel", Value: "strict"}, {Key: "validationAction", Value: "error"}}).Err(); err != nil {
			t.Fatalf("install exact pre-removal %s validator: %v", predecessor.collection, err)
		}
	}
	if err := upgradeTODOCollectionForHistory(ctx, database); err != nil {
		t.Fatalf("upgrade pre-removal TODO validator: %v", err)
	}
	if err := store.ProvisionTODOHistory(ctx, "company-a"); err != nil {
		t.Fatalf("upgrade pre-removal history validator: %v", err)
	}
	legacyEvents := historyValidatorBeforeReviewOutcome(TODOEventsValidatorBeforeRemoval())
	if err := database.RunCommand(ctx, bson.D{{Key: "collMod", Value: todoEventsCollection}, {Key: "validator", Value: legacyEvents}, {Key: "validationLevel", Value: "strict"}, {Key: "validationAction", Value: "error"}}).Err(); err != nil {
		t.Fatalf("install pre-review/pre-removal events validator: %v", err)
	}
	if err := store.ProvisionTODOHistory(ctx, "company-a"); err != nil {
		t.Fatalf("upgrade pre-review/pre-removal events validator: %v", err)
	}
	if err := verifyTODOCollection(ctx, database); err != nil {
		t.Fatalf("upgraded TODO validator: %v", err)
	}
	change := func(action, id, epicID string, revision int64) {
		t.Helper()
		_, err := store.ChangeRoadmap(ctx, "company-a", action, RoadmapChange{ProjectID: "builder", ExpectedRoadmapRevision: &revision, OperationID: "removal-" + action, ID: id, EpicID: epicID, Name: "Test " + id, Description: "Removal contract fixture"})
		if err != nil {
			t.Fatal(err)
		}
	}
	change("create-epic", "e-01", "", 0)
	change("create-release", "r-01", "e-01", 1)
	record := mongoBornTODOFixture()
	record.ID = "remove-test-todo"
	record.ReleaseID = "r-01"
	record.Position = intPointer(0)
	if _, err := store.AdmitTODO(ctx, "company-a", "builder", TODOAdmissionRequest{SchemaVersion: todoCreationSchemaVersion, OperationID: "removal-test-create", TODO: record}); err != nil {
		t.Fatal(err)
	}
	zero := int64(0)
	request := TODORemovalRequest{ProjectID: "builder", TODOID: record.ID, ExpectedRevision: &zero, OperationID: "remove-fixture-001", Actor: "test-owner", Reason: "Superseded by the canonical plan"}
	removed, err := store.RemoveTODO(ctx, "company-a", request)
	if err != nil || !removed.Removed || removed.NoOp || removed.TODORevision != 1 {
		t.Fatalf("removal = %+v, %v", removed, err)
	}
	retry, err := store.RemoveTODO(ctx, "company-a", request)
	if err != nil || !retry.NoOp || retry.TODORevision != 1 {
		t.Fatalf("exact retry = %+v, %v", retry, err)
	}
	read, err := store.ReadTODO(ctx, "company-a", "builder", record.ID, nil)
	if err != nil || !read.TODO.Removed || read.Removal == nil || read.Removal.Reason != request.Reason || read.Removal.ActorClaim.Value != request.Actor {
		t.Fatalf("removed exact read = %+v, %v", read, err)
	}
	before, err := store.ReadTODOAsOf(ctx, "company-a", "builder", record.ID, 0)
	if err != nil || before.State.Removed || before.State.Stage != "Planning" || before.State.ReleaseID != "r-01" {
		t.Fatalf("before removal = %+v, %v", before, err)
	}
	after, err := store.ReadTODOAsOf(ctx, "company-a", "builder", record.ID, 1)
	if err != nil || !after.State.Removed || after.State.Stage != before.State.Stage || after.State.ReleaseID != before.State.ReleaseID {
		t.Fatalf("at removal = %+v, %v", after, err)
	}
	roadmap, err := store.Roadmap(ctx, "builder", "company-a")
	if err != nil {
		t.Fatal(err)
	}
	var view struct {
		RoadmapRevision int64             `json:"roadmap_revision"`
		Epics           []roadmapEpicView `json:"epics"`
	}
	if err := json.Unmarshal(roadmap, &view); err != nil || view.RoadmapRevision != 4 || len(view.Epics) != 1 || len(view.Epics[0].Releases) != 1 || len(view.Epics[0].Releases[0].TODOs) != 0 {
		t.Fatalf("removed TODO remains on Roadmap: %s, %v", roadmap, err)
	}
	if _, err := store.TransitionTODO(ctx, "company-a", TODOActionRequest{ProjectID: "builder", TODOID: record.ID, ExpectedRevision: int64Pointer(1), OperationID: "removed-transition", RequestedStage: "Implementation-Approval-Pending"}); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "removed") {
		t.Fatalf("removed TODO accepted lifecycle mutation: %v", err)
	}
	if _, err := store.AdmitTODO(ctx, "company-a", "builder", TODOAdmissionRequest{SchemaVersion: todoCreationSchemaVersion, OperationID: "recreate-removed-id", TODO: record}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stable removed ID was reused: %v", err)
	}
	stale := request
	stale.OperationID = "remove-fixture-stale"
	if _, err := store.RemoveTODO(ctx, "company-a", stale); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "revision") {
		t.Fatalf("stale removal = %v", err)
	}
	currentRevision := int64(1)
	stale.ExpectedRevision = &currentRevision
	if _, err := store.RemoveTODO(ctx, "company-a", stale); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "removed") {
		t.Fatalf("different operation after removal = %v", err)
	}
	missingReason := request
	missingReason.Reason = ""
	if _, err := store.RemoveTODO(ctx, "company-a", missingReason); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "reason") {
		t.Fatalf("missing reason = %v", err)
	}
	changedRetry := request
	changedRetry.Reason = "Different reason"
	if _, err := store.RemoveTODO(ctx, "company-a", changedRetry); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "operation_id") {
		t.Fatalf("operation reuse = %v", err)
	}
	if _, err := store.EditTODONextStep(ctx, "company-a", TODONextStepEditRequest{ProjectID: "builder", TODOID: record.ID, ExpectedRevision: &currentRevision, OperationID: "removed-next-step", NextExactStep: "Cannot edit removed", Reason: "Test"}); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "removed") {
		t.Fatalf("removed TODO accepted next-step mutation: %v", err)
	}
	if _, err := store.EditTODOEvidence(ctx, "company-a", TODOEvidenceEditRequest{ProjectID: "builder", TODOID: record.ID, ExpectedRevision: &currentRevision, OperationID: "removed-evidence", ProofReferences: []TODOProofReference{}, WaiverReferences: []TODOProofReference{}, ImplementationCommits: []TODOCommitReference{}, RecordSections: []TODOSection{}, Reason: "Test"}); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "removed") {
		t.Fatalf("removed TODO accepted evidence mutation: %v", err)
	}
	roadmapRevision := int64(4)
	if _, err := store.EditTODOPlacement(ctx, "company-a", TODOPlacementEditRequest{ProjectID: "builder", TODOID: record.ID, ExpectedRevision: &currentRevision, ExpectedRoadmapRevision: &roadmapRevision, OperationID: "removed-placement", Action: "detach", Detach: true, Reason: "Test"}); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "removed") {
		t.Fatalf("removed TODO accepted placement mutation: %v", err)
	}
	if _, err := store.VerifyCurrentTODOStage(ctx, "company-a", VerifyCurrentTODOStageRequest{ProjectID: "builder", TODOID: record.ID, ExpectedRevision: &currentRevision, OperationID: "removed-verify"}); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "removed") {
		t.Fatalf("removed TODO accepted verification: %v", err)
	}
	history, err := store.ReadTODOHistory(ctx, "company-a", "builder", record.ID, -1, 10)
	if err != nil || len(history.Events) != 2 || history.Events[len(history.Events)-1].Kind != "todo-removed" {
		t.Fatalf("removed history = %+v, %v", history, err)
	}
	change("delete-release", "r-01", "e-01", 4)
	read, err = store.ReadTODO(ctx, "company-a", "builder", record.ID, nil)
	if err != nil || !read.TODO.Removed || read.TODO.ReleaseID != "r-01" || read.Removal == nil {
		t.Fatalf("removed read after Release deletion = %+v, %v", read, err)
	}
	if _, err := store.ReadTODOAsOf(ctx, "company-a", "builder", record.ID, 0); err != nil {
		t.Fatalf("removed history after Release deletion: %v", err)
	}
	concurrent := mongoBornTODOFixture()
	concurrent.ID = "remove-concurrent"
	if _, err := store.AdmitTODO(ctx, "company-a", "builder", TODOAdmissionRequest{SchemaVersion: todoCreationSchemaVersion, OperationID: "removal-concurrent-create", TODO: concurrent}); err != nil {
		t.Fatal(err)
	}
	concurrentRequest := TODORemovalRequest{ProjectID: "builder", TODOID: concurrent.ID, ExpectedRevision: &zero, OperationID: "remove-concurrent-001", Actor: "test-owner", Reason: "Concurrent exact retry"}
	results := make([]TODORemovalResponse, 2)
	errs := make([]error, 2)
	var wait sync.WaitGroup
	for i := range results {
		wait.Add(1)
		go func(i int) {
			defer wait.Done()
			results[i], errs[i] = store.RemoveTODO(ctx, "company-a", concurrentRequest)
		}(i)
	}
	wait.Wait()
	if errs[0] != nil || errs[1] != nil || !results[0].Removed || !results[1].Removed || results[0].NoOp == results[1].NoOp {
		t.Fatalf("concurrent exact removal = %+v, %+v, %v, %v", results[0], results[1], errs[0], errs[1])
	}
	concurrentHistory, err := store.ReadTODOHistory(ctx, "company-a", "builder", concurrent.ID, -1, 10)
	if err != nil || len(concurrentHistory.Events) != 2 {
		t.Fatalf("concurrent removal history = %+v, %v", concurrentHistory, err)
	}
	advanced := mongoBornTODOFixture()
	advanced.ID = "advanced-removal-test"
	if _, err := store.AdmitTODO(ctx, "company-a", "builder", TODOAdmissionRequest{SchemaVersion: todoCreationSchemaVersion, OperationID: "removal-advanced-create", TODO: advanced}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Collection(todosCollection).UpdateOne(ctx, bson.D{{Key: "project_id", Value: "builder"}, {Key: "id", Value: advanced.ID}}, bson.D{{Key: "$set", Value: bson.D{{Key: "stage", Value: "In-Progress"}}}}); err != nil {
		t.Fatal(err)
	}
	advancedRequest := TODORemovalRequest{ProjectID: "builder", TODOID: advanced.ID, ExpectedRevision: &zero, OperationID: "remove-advanced-001", Actor: "test-owner", Reason: "Must retain delivery proof"}
	if _, err := store.RemoveTODO(ctx, "company-a", advancedRequest); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "Planning") {
		t.Fatalf("advanced stage removal = %v", err)
	}
	advancedRead, err := store.ReadTODO(ctx, "company-a", "builder", advanced.ID, nil)
	if err != nil || advancedRead.TODO.Removed || advancedRead.TODO.Stage != "In-Progress" || advancedRead.TODORevision != 0 {
		t.Fatalf("advanced TODO changed on rejected removal = %+v, %v", advancedRead, err)
	}
	for round := 0; round < 5; round++ {
		item := mongoBornTODOFixture()
		item.ID = "remove-distinct-" + strconv.Itoa(round)
		if _, err := store.AdmitTODO(ctx, "company-a", "builder", TODOAdmissionRequest{SchemaVersion: todoCreationSchemaVersion, OperationID: "removal-item-create-" + item.ID, TODO: item}); err != nil {
			t.Fatal(err)
		}
		beforeRoadmap, err := store.Roadmap(ctx, "builder", "company-a")
		if err != nil {
			t.Fatal(err)
		}
		var beforeVersion struct {
			RoadmapRevision int64 `json:"roadmap_revision"`
		}
		if err := json.Unmarshal(beforeRoadmap, &beforeVersion); err != nil {
			t.Fatal(err)
		}
		var outcomes [2]TODORemovalResponse
		var outcomeErrors [2]error
		var race sync.WaitGroup
		for index := range outcomes {
			race.Add(1)
			go func(index int) {
				defer race.Done()
				input := TODORemovalRequest{ProjectID: "builder", TODOID: item.ID, ExpectedRevision: &zero, OperationID: "remove-distinct-" + strconv.Itoa(round) + "-" + strconv.Itoa(index), Actor: "test-owner", Reason: "Concurrent distinct operation"}
				outcomes[index], outcomeErrors[index] = store.RemoveTODO(ctx, "company-a", input)
			}(index)
		}
		race.Wait()
		successes := 0
		for index := range outcomes {
			if outcomeErrors[index] == nil && outcomes[index].Removed {
				successes++
			} else if !errors.Is(outcomeErrors[index], ErrConflict) {
				t.Fatalf("distinct race %d response %d = %+v, %v", round, index, outcomes[index], outcomeErrors[index])
			}
		}
		if successes != 1 {
			t.Fatalf("distinct race %d accepted %d operations: %+v, %+v", round, successes, outcomes, outcomeErrors)
		}
		afterRoadmap, err := store.Roadmap(ctx, "builder", "company-a")
		if err != nil {
			t.Fatal(err)
		}
		var afterVersion struct {
			RoadmapRevision int64 `json:"roadmap_revision"`
		}
		if err := json.Unmarshal(afterRoadmap, &afterVersion); err != nil {
			t.Fatal(err)
		}
		events, err := store.ReadTODOHistory(ctx, "company-a", "builder", item.ID, -1, 10)
		if err != nil || len(events.Events) != 2 || afterVersion.RoadmapRevision != beforeVersion.RoadmapRevision+1 {
			t.Fatalf("distinct race %d partial history/revision: events %+v, before %d, after %d, err %v", round, events, beforeVersion.RoadmapRevision, afterVersion.RoadmapRevision, err)
		}
	}
}
