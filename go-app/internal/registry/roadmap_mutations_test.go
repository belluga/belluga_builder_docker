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

type roadmapChangeCapture struct {
	*testService
	calls   int
	action  string
	request RoadmapChange
}

func (s *roadmapChangeCapture) ChangeRoadmap(_ context.Context, _, action string, request RoadmapChange) (RoadmapChangeResult, error) {
	s.calls++
	s.action, s.request = action, request
	return RoadmapChangeResult{ProjectID: request.ProjectID, Action: action, ID: request.ID, RoadmapRevision: 4}, nil
}

func TestCreateReleaseRouteReturnsFieldAddressedTeachBeforeMutation(t *testing.T) {
	base := "/internal/v1/companies/company-a/projects/builder/roadmap/epics/e-01/releases"
	cases := []struct {
		name, body, field string
	}{
		{"unknown nested field", `{"project_id":"builder","expected_roadmap_revision":3,"operation_id":"release-op-001","id":"r-10","name":"Release","extra":{"nested":{"field":true}}}`, "extra.nested.field"},
		{"invalid field type", `{"project_id":"builder","expected_roadmap_revision":"3","operation_id":"release-op-001","id":"r-10","name":"Release"}`, "expected_roadmap_revision"},
		{"missing field", `{"project_id":"builder","expected_roadmap_revision":3,"operation_id":"release-op-001","name":"Release"}`, "id"},
		{"route-irrelevant known field", `{"project_id":"builder","expected_roadmap_revision":3,"operation_id":"release-op-001","id":"r-10","name":"Release","ordered_ids":[]}`, "ordered_ids"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			service := &roadmapChangeCapture{testService: &testService{}}
			response := httptest.NewRecorder()
			NewHandler(service).ServeHTTP(response, httptest.NewRequest(http.MethodPost, base, strings.NewReader(tc.body)))
			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"teach":"TEACH: `+tc.field+` `) || service.calls != 0 {
				t.Fatalf("invalid create-release response=%d %s; calls=%d, want field-addressed TEACH before mutation", response.Code, response.Body, service.calls)
			}
		})
	}
}

func TestCreateReleaseRouteAcceptsFlatRequestAndUsesEpicPath(t *testing.T) {
	service := &roadmapChangeCapture{testService: &testService{}}
	base := "/internal/v1/companies/company-a/projects/builder/roadmap/epics/e-01/releases"
	body := `{"project_id":"builder","expected_roadmap_revision":3,"operation_id":"release-op-001","id":"r-10","name":"Release","description":"First release"}`
	response := httptest.NewRecorder()
	NewHandler(service).ServeHTTP(response, httptest.NewRequest(http.MethodPost, base, strings.NewReader(body)))
	if response.Code != http.StatusOK || service.calls != 1 || service.action != "create-release" || service.request.EpicID != "e-01" || service.request.ID != "r-10" {
		t.Fatalf("flat create-release response=%d %s; action=%q calls=%d request=%+v", response.Code, response.Body, service.action, service.calls, service.request)
	}
}

func TestMongoRoadmapPlanningMutationsAndPlacementShareProjectRevision(t *testing.T) {
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
	change := func(action, id, epicID, name string, order []string, revision int64) (RoadmapChangeResult, error) {
		return store.ChangeRoadmap(ctx, "company-a", action, RoadmapChange{ProjectID: "builder", ExpectedRoadmapRevision: &revision, OperationID: "planning-" + action + "-" + id + "-" + strconv.FormatInt(revision, 10), ID: id, EpicID: epicID, Name: name, Description: "User chosen description", OrderedIDs: order})
	}
	first, err := change("create-epic", "e-01", "", "First", nil, 0)
	if err != nil || first.RoadmapRevision != 1 {
		t.Fatalf("create Epic = %+v, %v", first, err)
	}
	firstRetry, err := change("create-epic", "e-01", "", "First", nil, 0)
	if err != nil || !firstRetry.NoOp || firstRetry.RoadmapRevision != 1 {
		t.Fatalf("exact retry = %+v, %v", firstRetry, err)
	}
	if _, err := change("create-epic", "e-02", "", "Second", nil, 0); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale revision accepted: %v", err)
	}
	if _, err := change("create-epic", "e-02", "", "Second", nil, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := change("order-epics", "", "", "", []string{"e-02", "e-01"}, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := change("create-release", "r-01", "e-01", "Release One", nil, 3); err != nil {
		t.Fatal(err)
	}
	if _, err := change("create-release", "r-02", "e-01", "Release Two", nil, 4); err != nil {
		t.Fatal(err)
	}
	if _, err := change("order-releases", "", "e-01", "", []string{"r-02", "r-01"}, 5); err != nil {
		t.Fatal(err)
	}
	if _, err := change("order-releases", "", "e-01", "", []string{"r-02", "r-02"}, 6); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate order accepted: %v", err)
	}
	if _, err := change("edit-release", "r-01", "e-02", "Moved Release", nil, 6); err != nil {
		t.Fatal(err)
	}
	if _, err := change("delete-epic", "e-01", "", "", nil, 7); !errors.Is(err, ErrConflict) {
		t.Fatalf("nonempty Epic deleted: %v", err)
	}
	viewBytes, err := store.Roadmap(ctx, "builder", "company-a")
	var view struct {
		RoadmapRevision int64             `json:"roadmap_revision"`
		Epics           []roadmapEpicView `json:"epics"`
	}
	if err == nil {
		err = json.Unmarshal(viewBytes, &view)
	}
	if err != nil || view.RoadmapRevision != 7 || len(view.Epics) != 2 || view.Epics[0].ID != "e-02" || len(view.Epics[0].Releases) != 1 || view.Epics[0].Releases[0].ID != "r-01" {
		t.Fatalf("Roadmap after planning changes = %s, %v", viewBytes, err)
	}
	created := mongoBornTODOFixture()
	created.ID, created.ProjectID = "roadmap-attach-test", "builder"
	if _, err := store.AdmitTODO(ctx, "company-a", "builder", TODOAdmissionRequest{SchemaVersion: todoCreationSchemaVersion, TODO: created}); err != nil {
		t.Fatal(err)
	}
	if _, err := change("delete-release", "r-02", "", "", nil, 7); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale deletion accepted after TODO creation: %v", err)
	}
	zero := int64(0)
	currentRoadmap := int64(8)
	attach := TODOPlacementEditRequest{ProjectID: "builder", TODOID: created.ID, ExpectedRevision: &zero, ExpectedRoadmapRevision: &currentRoadmap, OperationID: "roadmap-attach-001", Action: "attach", ReleaseID: "r-02", Position: 0, Reason: "Owner chose Release Two"}
	var wg sync.WaitGroup
	start := make(chan struct{})
	var attachErr, deleteErr error
	wg.Add(2)
	go func() { defer wg.Done(); <-start; _, attachErr = store.EditTODOPlacement(ctx, "company-a", attach) }()
	go func() { defer wg.Done(); <-start; _, deleteErr = change("delete-release", "r-02", "", "", nil, 8) }()
	close(start)
	wg.Wait()
	if (attachErr == nil) == (deleteErr == nil) {
		t.Fatalf("competing attachment/deletion outcomes: attach=%v delete=%v", attachErr, deleteErr)
	}
	finalBytes, err := store.Roadmap(ctx, "builder", "company-a")
	if err != nil {
		t.Fatalf("concurrent mutation left invalid Roadmap: %v", err)
	}
	var final struct {
		RoadmapRevision int64             `json:"roadmap_revision"`
		Epics           []roadmapEpicView `json:"epics"`
	}
	if err := json.Unmarshal(finalBytes, &final); err != nil || final.RoadmapRevision != 9 {
		t.Fatalf("final revision = %s, %v", finalBytes, err)
	}
	if attachErr == nil {
		if len(final.Epics[1].Releases) != 1 || len(final.Epics[1].Releases[0].TODOs) != 1 {
			t.Fatalf("accepted attachment missing from Release: %s", finalBytes)
		}
	} else if len(final.Epics[1].Releases) != 0 {
		t.Fatalf("accepted empty Release deletion did not remove Release: %s", finalBytes)
	}
	if _, err := store.ChangeRoadmap(ctx, "company-b", "create-epic", RoadmapChange{ProjectID: "builder", ExpectedRoadmapRevision: &zero, OperationID: "foreign-create", ID: "e-foreign", Name: "Foreign"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign Company wrote Builder Roadmap: %v", err)
	}
	count, err := store.client.Database(store.tenantDatabase("company-a")).Collection(epicsCollection).CountDocuments(ctx, bson.D{{Key: "project_id", Value: "builder"}})
	if err != nil || count != 2 {
		t.Fatalf("Epic count changed after rejected operations: %d, %v", count, err)
	}
}
