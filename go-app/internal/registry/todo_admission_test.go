package registry

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func genericTODOAdmissionFixture() TODOAdmissionRequest {
	return TODOAdmissionRequest{TODO: TODORecord{
		ID: "builder-mongo-todo-history-storage-implementation", ProjectID: "builder", ReleaseID: "r-08", Position: intPointer(3),
		Title: "TODO: Mongo TODO history", Objective: "Establish immutable TODO content versions and semantic events.",
		NextExactStep: "Admit this TODO to its existing Mongo Release.", ArtifactType: "tactical_execution_contract",
		Stage: "Planning", Qualifiers: []string{}, WorkState: "planning",
		Sections:        []TODOSection{{Key: "contract_boundary", Title: "Contract Boundary", BodyMarkdown: "One bounded narrative section.", Position: 0, Ownership: "contract"}},
		ProofReferences: []TODOProofReference{}, WaiverReferences: []TODOProofReference{}, DiffExpectation: []TODORepositoryScope{},
		ImplementationCommits: []TODOCommitReference{}, Source: TODOSource{
			Foundation: "builder", Revision: "0123456789abcdef0123456789abcdef01234567",
			Path:   "todos/active/TODO-builder-mongo-todo-history-storage-implementation.md",
			Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		},
	}}
}

func intPointer(value int) *int { return &value }

func TestValidateGenericTODOAdmission(t *testing.T) {
	valid := genericTODOAdmissionFixture()
	if err := validateGenericTODOAdmission(valid.TODO); err != nil {
		t.Fatalf("valid individual TODO admission rejected: %v", err)
	}
	cases := map[string]func(*TODORecord){
		"unstable identity":         func(record *TODORecord) { record.ID = "Not a stable ID" },
		"invalid source path":       func(record *TODORecord) { record.Source.Path = "../outside.md" },
		"caller supplied revision":  func(record *TODORecord) { record.Revision = 1 },
		"caller asserted proof":     func(record *TODORecord) { record.StageVerification = "verified" },
		"ambiguous section order":   func(record *TODORecord) { record.Sections[0].Position = 1 },
		"missing section ownership": func(record *TODORecord) { record.Sections[0].Ownership = "" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			request := genericTODOAdmissionFixture()
			mutate(&request.TODO)
			if err := validateGenericTODOAdmission(request.TODO); err == nil {
				t.Fatal("invalid admission was accepted")
			}
		})
	}
}

func mongoBornTODOFixture() TODORecord {
	record := genericTODOAdmissionFixture().TODO
	record.ID = "builder-mongo-todo-section-schema-analysis"
	record.ReleaseID = ""
	record.Position = nil
	record.Source = TODOSource{}
	record.Title = "TODO: Mongo TODO section schema analysis"
	record.NextExactStep = "Review the approved section contract."
	record.Stage = "Planning"
	record.StageVerification = ""
	record.Sections = []TODOSection{{Key: "analysis", Title: "Analysis", BodyMarkdown: "Bounded analysis contract.", Position: 0, Ownership: "contract"}}
	return record
}

func TestMongoBornTODOCreationContractAndVersionedTemplate(t *testing.T) {
	record := mongoBornTODOFixture()
	bypass := mongoBornTODOFixture()
	bypass.Stage = "Completed"
	if _, err := (&MongoStore{}).AdmitTODO(context.Background(), "company-a", "builder", TODOAdmissionRequest{TODO: bypass}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unversioned source-less admission bypass = %v; want invalid", err)
	}
	if err := validateMongoTODOCreation(record); err != nil {
		t.Fatalf("valid Mongo-born Backlog TODO rejected: %v", err)
	}
	template, err := ProjectTODOCreationTemplate("builder")
	if err != nil || template.SchemaVersion != todoCreationSchemaVersion || template.InitialStage != "Planning" || template.Example.SchemaVersion != template.SchemaVersion {
		t.Fatalf("versioned Project template = %+v, %v", template, err)
	}
	if err := validateMongoTODOCreation(template.Example.TODO); err != nil {
		t.Fatalf("template example rejected by write validator: %v", err)
	}
	if !containsTODOString(template.Optional, "todo.release_id") || !containsTODOString(template.Optional, "todo.position") || template.Limits["record_bytes"] != maxTODORecordBytes {
		t.Fatalf("template omits Backlog placement or record limits: %+v", template)
	}
	for name, mutate := range map[string]func(*TODORecord){
		"later initial stage":          func(value *TODORecord) { value.Stage = "In-Progress" },
		"partial placement":            func(value *TODORecord) { value.ReleaseID = "r-08" },
		"forged Foundation provenance": func(value *TODORecord) { value.Source.Foundation = "builder" },
		"forged operational revision":  func(value *TODORecord) { value.Revision = 9 },
	} {
		t.Run(name, func(t *testing.T) {
			invalid := mongoBornTODOFixture()
			mutate(&invalid)
			if err := validateMongoTODOCreation(invalid); err == nil {
				t.Fatal("invalid Mongo-born creation accepted")
			}
		})
	}
}

type testIndividualTODOAdmitter struct {
	*testService
	calls     int
	companyID string
	projectID string
	request   TODOAdmissionRequest
}

func (s *testIndividualTODOAdmitter) TODOCreationTemplate(_ context.Context, companyID, projectID string) (TODOCreationTemplate, error) {
	s.companyID, s.projectID = companyID, projectID
	return ProjectTODOCreationTemplate(projectID)
}

func (s *testIndividualTODOAdmitter) AdmitTODO(_ context.Context, companyID, projectID string, request TODOAdmissionRequest) (TODOAdmissionReport, error) {
	s.calls++
	s.companyID, s.projectID, s.request = companyID, projectID, request
	return TODOAdmissionReport{CompanyID: companyID, ProjectID: projectID, ReleaseID: request.TODO.ReleaseID, TODOID: request.TODO.ID, Revision: 0}, nil
}

func TestIndividualTODOAdmissionRouteIsCompanyProjectScoped(t *testing.T) {
	service := &testIndividualTODOAdmitter{testService: &testService{}}
	body, err := json.Marshal(genericTODOAdmissionFixture())
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/internal/v1/companies/belluga-solutions/projects/builder/todos", strings.NewReader(string(body)))
	response := httptest.NewRecorder()
	NewHandler(service).ServeHTTP(response, request)
	if response.Code != http.StatusOK || service.calls != 1 || service.companyID != "belluga-solutions" || service.projectID != "builder" || service.request.TODO.ID != "builder-mongo-todo-history-storage-implementation" {
		t.Fatalf("individual admission response = %d %s; service calls=%d company=%q project=%q TODO=%q", response.Code, response.Body.String(), service.calls, service.companyID, service.projectID, service.request.TODO.ID)
	}
	if !strings.Contains(response.Body.String(), `"release_id":"r-08"`) || !strings.Contains(response.Body.String(), `"todo_revision":0`) {
		t.Fatalf("admission response did not identify the admitted Release and revision: %s", response.Body.String())
	}
}

func TestTODOCreationTemplateRouteIsCompanyProjectScoped(t *testing.T) {
	service := &testIndividualTODOAdmitter{testService: &testService{}}
	request := httptest.NewRequest(http.MethodGet, "/internal/v1/companies/belluga-solutions/projects/builder/todos/template", nil)
	response := httptest.NewRecorder()
	NewHandler(service).ServeHTTP(response, request)
	if response.Code != http.StatusOK || service.companyID != "belluga-solutions" || service.projectID != "builder" || !strings.Contains(response.Body.String(), `"schema_version":"builder-todo-creation-v1"`) {
		t.Fatalf("Project creation template response = %d %s; company=%q project=%q", response.Code, response.Body.String(), service.companyID, service.projectID)
	}
}

func TestMongoIndividualTODOAdmissionReadRoadmapAndGovernedStart(t *testing.T) {
	uri := os.Getenv("REGISTRY_TEST_MONGODB_URI")
	if uri == "" {
		t.Skip("isolated replica-set MongoDB fixture not configured")
	}
	ctx := context.Background()
	// Base-36 nanoseconds retain unique run IDs while keeping the derived
	// builder_tenant_<stamp>_company_a name within Atlas's 38-byte limit.
	stamp := strconv.FormatInt(time.Now().UnixNano(), 36)
	store, err := OpenMongo(ctx, uri, stamp)
	if err != nil {
		t.Fatal(err)
	}
	store.tenantPrefix = stamp
	databaseName := store.tenantDatabase("company-a")
	t.Cleanup(func() {
		if err := store.client.Database(databaseName).Drop(context.Background()); err != nil {
			t.Errorf("drop isolated tenant database %q: %v", databaseName, err)
		}
		if err := store.control.Drop(context.Background()); err != nil {
			t.Errorf("drop isolated control database %q: %v", stamp, err)
		}
		databaseNames, err := store.client.ListDatabaseNames(context.Background(), bson.D{})
		if err != nil {
			t.Errorf("verify disposable database cleanup: %v", err)
		} else {
			for _, name := range databaseNames {
				if name == databaseName || name == stamp {
					t.Errorf("disposable test database %q remains after cleanup", name)
				}
			}
		}
		_ = store.Close(context.Background())
	})
	company, err := normalizeCompany(Company{ID: "company-a", Name: "Company A", DatabaseName: databaseName})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.control.Collection(companiesCollection).InsertOne(ctx, company); err != nil {
		t.Fatal(err)
	}
	database := store.client.Database(databaseName)
	project := Project{ID: "builder", CompanyID: "company-a", Name: "Builder", Binding: ptrTestBinding(validTestBinding()), RegisteredAt: time.Now().UTC().Format(time.RFC3339Nano), PreparationState: "not_ready"}
	projectRecord, err := projectToDocument(project, bson.NewObjectID())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Collection(projectsCollection).InsertOne(ctx, projectRecord); err != nil {
		t.Fatal(err)
	}
	epic := roadmapEpic{StorageID: bson.NewObjectID(), ProjectID: "builder", ID: "e-06", Name: "Database", Position: 0}
	release := roadmapRelease{StorageID: bson.NewObjectID(), ProjectID: "builder", ID: "r-08", EpicID: "e-06", Name: "Mongo", Position: 0}
	nextRelease := roadmapRelease{StorageID: bson.NewObjectID(), ProjectID: "builder", ID: "r-09", EpicID: "e-06", Name: "Mongo Follow-up", Position: 1}
	if _, err := database.Collection(epicsCollection).InsertOne(ctx, epic); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Collection(releasesCollection).InsertOne(ctx, release); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Collection(releasesCollection).InsertOne(ctx, nextRelease); err != nil {
		t.Fatal(err)
	}
	if err := store.ProvisionTODOCollection(ctx, "company-a"); err != nil {
		t.Fatal(err)
	}
	if err := store.ProvisionTODOHistory(ctx, "company-a"); err != nil {
		t.Fatalf("provision TODO history: %v", err)
	}
	for name, validator := range map[string]bson.D{todoContentVersionsCollection: TODOContentVersionsValidator(), todoEventsCollection: TODOEventsValidator()} {
		command := bson.D{{Key: "collMod", Value: name}, {Key: "validator", Value: historyValidatorBeforeOwnership(validator)}, {Key: "validationLevel", Value: "strict"}, {Key: "validationAction", Value: "error"}}
		if err := database.RunCommand(ctx, command).Err(); err != nil {
			t.Fatalf("set exact prior %s validator: %v", name, err)
		}
	}
	if err := store.ProvisionTODOHistory(ctx, "company-a"); err != nil {
		t.Fatalf("additive history ownership validator upgrade: %v", err)
	}
	request := genericTODOAdmissionFixture()
	report, err := store.AdmitTODO(ctx, "company-a", "builder", request)
	if err != nil || report.TODOID != request.TODO.ID || report.ReleaseID != "r-08" || report.Revision != 0 {
		t.Fatalf("individual admission = %+v, %v", report, err)
	}
	setTODOValidator := func(validator bson.D) {
		t.Helper()
		command := bson.D{{Key: "collMod", Value: todosCollection}, {Key: "validator", Value: validator}, {Key: "validationLevel", Value: "strict"}, {Key: "validationAction", Value: "error"}}
		if err := database.RunCommand(ctx, command).Err(); err != nil {
			t.Fatalf("set isolated TODO validator: %v", err)
		}
	}
	unrelatedDrift := TODOCollectionValidator()
	jsonSchema := unrelatedDrift[0].Value.(bson.D)
	for i := range jsonSchema {
		if jsonSchema[i].Key != "properties" {
			continue
		}
		properties := jsonSchema[i].Value.(bson.D)
		for j := range properties {
			if properties[j].Key != "artifact_type" {
				continue
			}
			artifactType := properties[j].Value.(bson.D)
			for k := range artifactType {
				if artifactType[k].Key == "enum" {
					artifactType[k].Value = bson.A{"unrelated-drift"}
				}
			}
			properties[j].Value = artifactType
		}
		jsonSchema[i].Value = properties
	}
	unrelatedDrift[0].Value = jsonSchema
	setTODOValidator(unrelatedDrift)
	if err := store.ProvisionTODOHistory(ctx, "company-a"); !errors.Is(err, ErrConflict) {
		t.Fatalf("unrelated TODO validator drift = %v; want fail-closed conflict", err)
	}
	setTODOValidator(priorTODOCollectionValidator())
	if err := store.ProvisionTODOHistory(ctx, "company-a"); err != nil {
		t.Fatalf("recognized prior validator with existing typed record should upgrade additively: %v", err)
	}
	if err := verifyTODOCollection(ctx, database); err != nil {
		t.Fatalf("upgraded TODO validator readback: %v", err)
	}
	if _, err := store.AdmitTODO(ctx, "company-a", "builder", request); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate individual admission = %v; want conflict", err)
	}
	baselineEvent, err := readHistoryBaseline(ctx, database, "builder", request.TODO.ID)
	if err != nil || baselineEvent.Revision != 0 || baselineEvent.Kind != "baseline" {
		t.Fatalf("admission did not atomically create revision-0 baseline before stage write: %+v, %v", baselineEvent, err)
	}
	var baselineVersion TODOContentVersion
	if err := database.Collection(todoContentVersionsCollection).FindOne(ctx, bson.D{{Key: "project_id", Value: "builder"}, {Key: "todo_id", Value: request.TODO.ID}, {Key: "revision", Value: int64(0)}}).Decode(&baselineVersion); err != nil {
		t.Fatalf("admission did not atomically create revision-0 content version: %v", err)
	}
	read, err := store.ReadTODO(ctx, "company-a", "builder", request.TODO.ID, nil)
	if err != nil || read.TODO.ReleaseID != "r-08" || read.TODORevision != 0 {
		t.Fatalf("generic admitted TODO read = %+v, %v", read, err)
	}
	if _, err := store.ReadTODO(ctx, "company-a", "builder", "builder-never-admitted", nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unadmitted TODO read = %v; want 404/not found", err)
	}
	if _, err := store.ReadTODO(ctx, "company-b", "builder", request.TODO.ID, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("wrong-Company TODO read = %v; want isolated tenant not-found", err)
	}
	roadmap, err := store.Roadmap(ctx, "builder", "company-a")
	if err != nil || !strings.Contains(string(roadmap), request.TODO.ID) || !strings.Contains(string(roadmap), `"id":"r-08"`) {
		t.Fatalf("generic admitted TODO missing from Roadmap: %s, %v", roadmap, err)
	}

	// The Project-scoped template and POST admit a Mongo-born TODO directly to
	// Planning Backlog, without Foundation source metadata or a placeholder Release.
	creationTemplate, err := store.TODOCreationTemplate(ctx, "company-a", "builder")
	if err != nil || creationTemplate.SchemaVersion != todoCreationSchemaVersion {
		t.Fatalf("Mongo Project creation template = %+v, %v", creationTemplate, err)
	}
	mongoBorn := mongoBornTODOFixture()
	setTODOValidator(TODOCollectionValidatorBeforeCreation())
	if err := database.RunCommand(ctx, bson.D{{Key: "collMod", Value: todoContentVersionsCollection}, {Key: "validator", Value: historyValidatorBeforeContentDigest(TODOContentVersionsValidator())}, {Key: "validationLevel", Value: "strict"}, {Key: "validationAction", Value: "error"}}).Err(); err != nil {
		t.Fatalf("set pre-creation content-version validator: %v", err)
	}
	if err := database.RunCommand(ctx, bson.D{{Key: "collMod", Value: todoEventsCollection}, {Key: "validator", Value: historyValidatorBeforeClearPlacement(TODOEventsValidator())}, {Key: "validationLevel", Value: "strict"}, {Key: "validationAction", Value: "error"}}).Err(); err != nil {
		t.Fatalf("set pre-creation event validator: %v", err)
	}
	mongoAdmission, err := store.AdmitTODO(ctx, "company-a", "builder", TODOAdmissionRequest{SchemaVersion: creationTemplate.SchemaVersion, TODO: mongoBorn})
	if err != nil || mongoAdmission.TODOID != mongoBorn.ID || mongoAdmission.ReleaseID != "" || mongoAdmission.ContentRevision != 0 || !validSourceDigest(mongoAdmission.ContentDigest) {
		t.Fatalf("Mongo-born Backlog admission = %+v, %v", mongoAdmission, err)
	}
	if err := verifyTODOCollection(ctx, database); err != nil {
		t.Fatalf("Mongo-born admission did not upgrade the recognized pre-creation validator: %v", err)
	}
	mongoCreationEvent, err := readHistoryBaseline(ctx, database, "builder", mongoBorn.ID)
	if err != nil || mongoCreationEvent.ProofReference == nil || mongoCreationEvent.ProofReference.Kind != "created" || mongoCreationEvent.ProofReference.SourceDigest != mongoAdmission.ContentDigest {
		t.Fatalf("Mongo-born Planning baseline lacks its Mongo creation proof: %+v, %v", mongoCreationEvent, err)
	}
	mongoRead, err := store.ReadTODO(ctx, "company-a", "builder", mongoBorn.ID, nil)
	if err != nil || mongoRead.TODO.Stage != "Planning" || mongoRead.TODO.StageVerification != "verified" || mongoRead.TODO.Source.Kind != "mongo-content-revision" || mongoRead.TODO.Source.Path != "" || mongoRead.TODO.ReleaseID != "" || mongoRead.TODO.Position != nil || mongoRead.ContentRevision == nil || *mongoRead.ContentRevision != 0 || mongoRead.ContentDigest != mongoAdmission.ContentDigest {
		t.Fatalf("Mongo-born Planning detail = %+v, %v", mongoRead, err)
	}
	var backlogRoadmap struct {
		Backlog struct {
			TODOs []roadmapTODOView `json:"todos"`
		} `json:"backlog"`
		Epics []roadmapEpicView `json:"epics"`
	}
	backlogBytes, err := store.Roadmap(ctx, "builder", "company-a")
	if err != nil || json.Unmarshal(backlogBytes, &backlogRoadmap) != nil || len(backlogRoadmap.Backlog.TODOs) != 1 || backlogRoadmap.Backlog.TODOs[0].ID != mongoBorn.ID || len(backlogRoadmap.Epics[0].Releases[0].TODOs) != 1 {
		t.Fatalf("Mongo-born TODO was not projected exclusively to Backlog: %s, %v", backlogBytes, err)
	}
	zeroMongoRevision := int64(0)
	if _, err := store.TransitionTODO(ctx, "company-a", TODOActionRequest{ProjectID: "builder", TODOID: mongoBorn.ID, ExpectedRevision: &zeroMongoRevision, OperationID: "backlog-transition-001", RequestedStage: "Implementation-Approval-Pending", ProofReferences: []TODOProofReference{}, WaiverReferences: []TODOProofReference{}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("unassigned Mongo-born TODO advanced from Planning: %v", err)
	}
	attach := TODOPlacementEditRequest{ProjectID: "builder", TODOID: mongoBorn.ID, ExpectedRevision: &zeroMongoRevision, OperationID: "mongo-attach-001", Action: "attach", ReleaseID: "r-08", Position: 4, Reason: "Attach the TODO to a user-selected same-Project Release"}
	attached, err := store.EditTODOPlacement(ctx, "company-a", attach)
	if err != nil || attached.Revision != 1 {
		t.Fatalf("attach Mongo-born TODO = %+v, %v", attached, err)
	}
	oneMongoRevision := int64(1)
	detached, err := store.EditTODOPlacement(ctx, "company-a", TODOPlacementEditRequest{ProjectID: "builder", TODOID: mongoBorn.ID, ExpectedRevision: &oneMongoRevision, OperationID: "mongo-detach-001", Action: "detach", Reason: "Return the TODO to Backlog"})
	if err != nil || detached.Revision != 2 {
		t.Fatalf("detach Mongo-born TODO = %+v, %v", detached, err)
	}
	mongoRead, err = store.ReadTODO(ctx, "company-a", "builder", mongoBorn.ID, nil)
	if err != nil || mongoRead.TODO.ReleaseID != "" || mongoRead.TODO.Position != nil || mongoRead.TODORevision != 2 {
		t.Fatalf("detached TODO identity/placement = %+v, %v", mongoRead, err)
	}
	var contentVersionCount int64
	contentVersionCount, err = database.Collection(todoContentVersionsCollection).CountDocuments(ctx, bson.D{{Key: "project_id", Value: "builder"}, {Key: "todo_id", Value: mongoBorn.ID}})
	if err != nil || contentVersionCount != 1 {
		t.Fatalf("placement changed authored content history: versions=%d err=%v", contentVersionCount, err)
	}
	twoMongoRevision := int64(2)
	attach.ExpectedRevision, attach.OperationID = &twoMongoRevision, "mongo-attach-002"
	if _, err := store.EditTODOPlacement(ctx, "company-a", attach); err != nil {
		t.Fatalf("reattach Mongo-born TODO: %v", err)
	}
	threeMongoRevision := int64(3)
	wrongProof := TODOProofReference{Kind: "owner-approval", Source: "mongo-content-revision", Reference: "builder/" + mongoBorn.ID + "/content/0", SourceDigest: "sha256:" + strings.Repeat("0", 64), VerificationState: "unverified"}
	wrongStart := TODOActionRequest{ProjectID: "builder", TODOID: mongoBorn.ID, ExpectedRevision: &threeMongoRevision, OperationID: "mongo-start-wrong-digest", RequestedStage: "Implementation-Approval-Pending", ProofReferences: []TODOProofReference{wrongProof}, WaiverReferences: []TODOProofReference{}}
	if _, err := store.TransitionTODO(ctx, "company-a", wrongStart); !errors.Is(err, ErrConflict) {
		t.Fatalf("wrong content digest approved: %v", err)
	}
	correctProof := wrongProof
	correctProof.SourceDigest = mongoAdmission.ContentDigest
	correctStart := wrongStart
	correctStart.OperationID = "mongo-start-001"
	correctStart.ProofReferences = []TODOProofReference{correctProof}
	startedMongo, err := store.TransitionTODO(ctx, "company-a", correctStart)
	if err != nil || startedMongo.Stage != "Implementation-Approval-Pending" || startedMongo.TODORevision != 4 {
		t.Fatalf("governed Mongo-born first lifecycle edge = %+v, %v", startedMongo, err)
	}
	currentMongo, err := store.ReadTODOAsOf(ctx, "company-a", "builder", mongoBorn.ID, 4)
	if err != nil || currentMongo.State.Stage != "Implementation-Approval-Pending" || currentMongo.State.PlacementAssigned != true {
		t.Fatalf("Mongo-born first edge history = %+v, %v", currentMongo, err)
	}
	backlogBytes, err = store.Roadmap(ctx, "builder", "company-a")
	if err != nil || json.Unmarshal(backlogBytes, &backlogRoadmap) != nil || len(backlogRoadmap.Backlog.TODOs) != 0 || len(backlogRoadmap.Epics[0].Releases[0].TODOs) != 2 {
		t.Fatalf("assigned TODO duplicated or missing in Backlog/Release projection: %s, %v", backlogBytes, err)
	}
	currentMongoRead, err := store.ReadTODO(ctx, "company-a", "builder", mongoBorn.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	updatedMongoContent := contentFromTODO(currentMongoRead.TODO)
	updatedMongoContent.Title += " revised"
	fourMongoRevision := int64(4)
	contentEdit, err := store.EditTODOContent(ctx, "company-a", TODOContentEditRequest{ProjectID: "builder", TODOID: mongoBorn.ID, ExpectedRevision: &fourMongoRevision, OperationID: "mongo-content-edit-001", Content: updatedMongoContent, Reason: "Change authored contract content"})
	if err != nil || contentEdit.Revision != 5 || contentEdit.ContentRevision == nil || *contentEdit.ContentRevision != 5 || !validSourceDigest(contentEdit.ContentDigest) {
		t.Fatalf("Mongo-born content edit = %+v, %v", contentEdit, err)
	}
	currentMongoRead, err = store.ReadTODO(ctx, "company-a", "builder", mongoBorn.ID, nil)
	if err != nil || currentMongoRead.TODO.StageVerification != "unverified" || currentMongoRead.TODO.VerifiedTransitionRef != nil || currentMongoRead.ContentRevision == nil || *currentMongoRead.ContentRevision != 5 || currentMongoRead.ContentDigest != contentEdit.ContentDigest {
		t.Fatalf("content edit retained prior approval: %+v, %v", currentMongoRead, err)
	}
	var editedMongoVersion TODOContentVersion
	if err := database.Collection(todoContentVersionsCollection).FindOne(ctx, bson.D{{Key: "project_id", Value: "builder"}, {Key: "todo_id", Value: mongoBorn.ID}, {Key: "revision", Value: int64(5)}}).Decode(&editedMongoVersion); err != nil {
		t.Fatalf("read edited immutable content version: %v", err)
	}
	canonicalMongoDigest, err := todoContentRevisionDigest("builder", mongoBorn.ID, 5, editedMongoVersion.Content)
	if err != nil || editedMongoVersion.Digest != canonicalMongoDigest || editedMongoVersion.Digest == mongoAdmission.ContentDigest {
		t.Fatalf("content revision digest = %q, canonical %q, err %v", editedMongoVersion.Digest, canonicalMongoDigest, err)
	}
	zero := int64(0)
	proof := TODOProofReference{Kind: "owner-approval", Source: "owner-chat", Reference: "APROVADO", SourceDigest: request.TODO.Source.Digest, VerificationState: "unverified"}
	started, err := store.TransitionTODO(ctx, "company-a", TODOActionRequest{
		ProjectID: "builder", TODOID: request.TODO.ID, ExpectedRevision: &zero, OperationID: "individual-start-001", RequestedStage: "In-Progress",
		ProofReferences: []TODOProofReference{proof}, WaiverReferences: []TODOProofReference{},
	})
	if err != nil || started.Stage != "In-Progress" || started.TODORevision != 1 {
		t.Fatalf("governed generic start = %+v, %v", started, err)
	}
	baseline, err := store.ReadTODOAsOf(ctx, "company-a", "builder", request.TODO.ID, 0)
	if err != nil || baseline.State.Stage != "Planning" || baseline.Content.Title != request.TODO.Title {
		t.Fatalf("revision 0 history = %+v, %v", baseline, err)
	}
	current, err := store.ReadTODOAsOf(ctx, "company-a", "builder", request.TODO.ID, 1)
	if err != nil || current.State.Stage != "In-Progress" || current.State.StageVerification != "verified" {
		t.Fatalf("revision 1 history = %+v, %v", current, err)
	}
	page, err := store.ReadTODOHistory(ctx, "company-a", "builder", request.TODO.ID, -1, 100)
	if err != nil || len(page.Events) != 2 || page.Events[0].Kind != "baseline" || page.Events[1].Kind != "state_changed" {
		t.Fatalf("baseline and stage events = %+v, %v", page, err)
	}
	comparison, err := store.CompareTODOHistory(ctx, "company-a", "builder", request.TODO.ID, 0, 1)
	if err != nil || !reflect.DeepEqual(comparison.Changed, []string{"stage", "stage_verification", "verified_transition_ref"}) {
		t.Fatalf("deterministic as-of comparison = %+v, %v", comparison, err)
	}
	startedRetry, err := store.TransitionTODO(ctx, "company-a", TODOActionRequest{
		ProjectID: "builder", TODOID: request.TODO.ID, ExpectedRevision: &zero, OperationID: "individual-start-001", RequestedStage: "In-Progress",
		ProofReferences: []TODOProofReference{proof}, WaiverReferences: []TODOProofReference{},
	})
	if err != nil || !startedRetry.NoOp || startedRetry.TODORevision != 1 {
		t.Fatalf("exact start retry = %+v, %v", startedRetry, err)
	}
	page, err = store.ReadTODOHistory(ctx, "company-a", "builder", request.TODO.ID, -1, 100)
	if err != nil || len(page.Events) != 2 {
		t.Fatalf("exact retry added duplicate history event: %+v, %v", page, err)
	}
	one := int64(1)
	if _, err := store.TransitionTODO(ctx, "company-a", TODOActionRequest{ProjectID: "builder", TODOID: request.TODO.ID, ExpectedRevision: &one, OperationID: "unsupported-local-001", RequestedStage: "Local-Implemented"}); err == nil || !errors.Is(err, ErrConflict) {
		t.Fatalf("unguarded generic Local-Implemented transition = %v; want fail-closed TEACH", err)
	}
	unchanged, err := store.ReadTODO(ctx, "company-a", "builder", request.TODO.ID, &one)
	if err != nil || unchanged.TODO.Stage != "In-Progress" {
		t.Fatalf("failed guard changed current TODO: %+v, %v", unchanged, err)
	}
	content := contentFromTODO(unchanged.TODO)
	content.Title = "TODO: Authored history content updated"
	content.Sections = append(content.Sections, TODOSection{Key: "large-authored-evidence", Title: "Large authored evidence", BodyMarkdown: strings.Repeat("c", 47_482), Position: 1, Ownership: "contract"})
	two := int64(1)
	editRequest := TODOContentEditRequest{ProjectID: "builder", TODOID: request.TODO.ID, ExpectedRevision: &two, OperationID: "content-edit-001", Content: content, Reason: "Focused history integration test"}
	if _, err := store.EditTODOContent(ctx, "company-b", editRequest); !errors.Is(err, ErrNotFound) {
		t.Fatalf("wrong-Company content write = %v; want isolated tenant not-found", err)
	}
	// Tighten only this disposable fixture's event validator so the current CAS
	// and content-version insert succeed before the linked event insert fails.
	// The transaction must roll all three writes back.
	abortValidator := TODOEventsValidator()
	eventSchema := abortValidator[0].Value.(bson.D)
	for i := range eventSchema {
		if eventSchema[i].Key != "properties" {
			continue
		}
		properties := eventSchema[i].Value.(bson.D)
		for j := range properties {
			if properties[j].Key != "kind" {
				continue
			}
			kindSchema := properties[j].Value.(bson.D)
			for k := range kindSchema {
				if kindSchema[k].Key == "enum" {
					kindSchema[k].Value = bson.A{"baseline"}
				}
			}
			properties[j].Value = kindSchema
		}
		eventSchema[i].Value = properties
	}
	abortValidator[0].Value = eventSchema
	if err := database.RunCommand(ctx, bson.D{{Key: "collMod", Value: todoEventsCollection}, {Key: "validator", Value: abortValidator}, {Key: "validationLevel", Value: "strict"}, {Key: "validationAction", Value: "error"}}).Err(); err != nil {
		t.Fatalf("install isolated transaction-abort validator: %v", err)
	}
	abortRequest := editRequest
	abortRequest.OperationID = "content-abort-001"
	if _, err := store.EditTODOContent(ctx, "company-a", abortRequest); err == nil {
		t.Fatal("content edit with rejected event insert unexpectedly committed")
	}
	if err := database.RunCommand(ctx, bson.D{{Key: "collMod", Value: todoEventsCollection}, {Key: "validator", Value: TODOEventsValidator()}, {Key: "validationLevel", Value: "strict"}, {Key: "validationAction", Value: "error"}}).Err(); err != nil {
		t.Fatalf("restore canonical isolated event validator: %v", err)
	}
	unchanged, err = store.ReadTODO(ctx, "company-a", "builder", request.TODO.ID, &one)
	if err != nil || unchanged.TODORevision != 1 {
		t.Fatalf("aborted transaction changed current TODO: %+v, %v", unchanged, err)
	}
	var versionsAfterAbort, eventsAfterAbort int64
	versionsAfterAbort, err = database.Collection(todoContentVersionsCollection).CountDocuments(ctx, bson.D{{Key: "project_id", Value: "builder"}, {Key: "todo_id", Value: request.TODO.ID}})
	if err != nil || versionsAfterAbort != 1 {
		t.Fatalf("aborted transaction content versions=%d err=%v; want baseline only", versionsAfterAbort, err)
	}
	eventsAfterAbort, err = database.Collection(todoEventsCollection).CountDocuments(ctx, bson.D{{Key: "project_id", Value: "builder"}, {Key: "todo_id", Value: request.TODO.ID}})
	if err != nil || eventsAfterAbort != 2 {
		t.Fatalf("aborted transaction events=%d err=%v; want baseline and stage only", eventsAfterAbort, err)
	}
	contentWriteStarted := time.Now()
	edited, err := store.EditTODOContent(ctx, "company-a", editRequest)
	contentWriteLatency := time.Since(contentWriteStarted)
	if err != nil || edited.Revision != 2 || edited.NoOp {
		t.Fatalf("content edit = %+v, %v", edited, err)
	}
	var measuredVersion TODOContentVersion
	if err := database.Collection(todoContentVersionsCollection).FindOne(ctx, bson.D{{Key: "project_id", Value: "builder"}, {Key: "todo_id", Value: request.TODO.ID}, {Key: "revision", Value: int64(2)}}).Decode(&measuredVersion); err != nil {
		t.Fatalf("read measured content version: %v", err)
	}
	measuredVersionBSON, err := bson.Marshal(measuredVersion)
	if err != nil {
		t.Fatal(err)
	}
	editedRetry, err := store.EditTODOContent(ctx, "company-a", editRequest)
	if err != nil || !editedRetry.NoOp || editedRetry.Revision != 2 {
		t.Fatalf("exact content retry = %+v, %v", editedRetry, err)
	}
	changedPayload := editRequest
	changedPayload.Content.Title += " changed"
	if _, err := store.EditTODOContent(ctx, "company-a", changedPayload); err == nil || !errors.Is(err, ErrConflict) {
		t.Fatalf("operation ID payload collision = %v; want conflict", err)
	}
	staleRequest := editRequest
	staleRequest.ExpectedRevision = &zero
	staleRequest.OperationID = "content-stale-001"
	if _, err := store.EditTODOContent(ctx, "company-a", staleRequest); err == nil || !errors.Is(err, ErrConflict) {
		t.Fatalf("stale content edit = %v; want revision conflict", err)
	}
	versionAtTwo, err := store.ReadTODOAsOf(ctx, "company-a", "builder", request.TODO.ID, 2)
	if err != nil || versionAtTwo.Content.Title != content.Title || versionAtTwo.State.Stage != "In-Progress" {
		t.Fatalf("content as-of revision 2 = %+v, %v", versionAtTwo, err)
	}
	comparison, err = store.CompareTODOHistory(ctx, "company-a", "builder", request.TODO.ID, 1, 2)
	if err != nil || !reflect.DeepEqual(comparison.Changed, []string{"sections", "title"}) {
		t.Fatalf("content comparison = %+v, %v", comparison, err)
	}
	three := int64(2)
	restoreRequest := TODOContentRestoreRequest{ProjectID: "builder", TODOID: request.TODO.ID, ExpectedRevision: &three, OperationID: "content-restore-001", ContentRevision: 0, Reason: "Restore original authored title"}
	restored, err := store.RestoreTODOContent(ctx, "company-a", restoreRequest)
	if err != nil || restored.Revision != 3 || restored.NoOp {
		t.Fatalf("content restore = %+v, %v", restored, err)
	}
	versionAtThree, err := store.ReadTODOAsOf(ctx, "company-a", "builder", request.TODO.ID, 3)
	if err != nil || versionAtThree.Content.Title != request.TODO.Title || versionAtThree.State.Stage != "In-Progress" {
		t.Fatalf("restored as-of revision 3 = %+v, %v", versionAtThree, err)
	}
	proofEdit := TODOProofReference{Kind: "implementation-evidence", Source: "owner-chat", Reference: "test evidence pointer", SourceDigest: request.TODO.Source.Digest, VerificationState: "unverified"}
	four := int64(3)
	evidenceRequest := TODOEvidenceEditRequest{ProjectID: "builder", TODOID: request.TODO.ID, ExpectedRevision: &four, OperationID: "evidence-edit-001", ProofReferences: []TODOProofReference{proofEdit}, WaiverReferences: []TODOProofReference{}, ImplementationCommits: []TODOCommitReference{}, RecordSections: []TODOSection{{Key: "execution-evidence", Title: "Execution Evidence", BodyMarkdown: strings.Repeat("e", 47_482), Position: 0, Ownership: "record"}}, Reason: "Record typed evidence pointer"}
	evidenceWriteStarted := time.Now()
	evidence, err := store.EditTODOEvidence(ctx, "company-a", evidenceRequest)
	evidenceWriteLatency := time.Since(evidenceWriteStarted)
	if err != nil || evidence.Revision != 4 || evidence.NoOp {
		t.Fatalf("evidence-only edit = %+v, %v", evidence, err)
	}
	var measuredEvent TODOHistoryEvent
	if err := database.Collection(todoEventsCollection).FindOne(ctx, bson.D{{Key: "project_id", Value: "builder"}, {Key: "todo_id", Value: request.TODO.ID}, {Key: "operation_id", Value: evidenceRequest.OperationID}}).Decode(&measuredEvent); err != nil {
		t.Fatalf("read measured event-only record: %v", err)
	}
	measuredEventBSON, err := bson.Marshal(measuredEvent)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("isolated Mongo sample: content_version_bson_bytes=%d content_write_latency=%s event_only_bson_bytes=%d event_only_write_latency=%s large_section_bytes=47482", len(measuredVersionBSON), contentWriteLatency, len(measuredEventBSON), evidenceWriteLatency)
	five := int64(4)
	placementRequest := TODOPlacementEditRequest{ProjectID: "builder", TODOID: request.TODO.ID, ExpectedRevision: &five, OperationID: "placement-edit-001", ReleaseID: "r-09", Position: 4, Reason: "Move TODO to another Release in the same Project"}
	placement, err := store.EditTODOPlacement(ctx, "company-a", placementRequest)
	if err != nil || placement.Revision != 5 || placement.NoOp {
		t.Fatalf("placement-only edit = %+v, %v", placement, err)
	}
	six := int64(5)
	placementNoOp := placementRequest
	placementNoOp.ExpectedRevision = &six
	placementNoOp.OperationID = "placement-noop-001"
	if result, err := store.EditTODOPlacement(ctx, "company-a", placementNoOp); err != nil || !result.NoOp || result.Revision != 5 {
		t.Fatalf("same-placement no-op = %+v, %v", result, err)
	}
	evidenceAsOf, err := store.ReadTODOAsOf(ctx, "company-a", "builder", request.TODO.ID, 4)
	if err != nil || evidenceAsOf.State.ProofReferences[0].VerificationState != "unverified" || len(evidenceAsOf.State.RecordSections) != 1 {
		t.Fatalf("evidence as-of revision 4 = %+v, %v", evidenceAsOf, err)
	}
	placementBefore, err := store.ReadTODOAsOf(ctx, "company-a", "builder", request.TODO.ID, 4)
	if err != nil || placementBefore.State.Position != 3 || placementBefore.State.ReleaseID != "r-08" {
		t.Fatalf("pre-placement as-of revision 4 = %+v, %v", placementBefore, err)
	}
	placementAsOf, err := store.ReadTODOAsOf(ctx, "company-a", "builder", request.TODO.ID, 5)
	if err != nil || placementAsOf.State.Position != 4 || placementAsOf.State.ReleaseID != "r-09" {
		t.Fatalf("placement as-of revision 5 = %+v, %v", placementAsOf, err)
	}
	currentAfterMove, err := store.ReadTODO(ctx, "company-a", "builder", request.TODO.ID, &six)
	if err != nil || currentAfterMove.TODO.Source.Path != request.TODO.Source.Path || currentAfterMove.TODO.Source.Digest != request.TODO.Source.Digest {
		t.Fatalf("Release move changed immutable source provenance: %+v, %v", currentAfterMove, err)
	}
	badMove := TODOPlacementEditRequest{ProjectID: "builder", TODOID: request.TODO.ID, ExpectedRevision: &six, OperationID: "placement-foreign-001", ReleaseID: "foreign-r-01", Position: 0, Reason: "Reject cross-Project Release"}
	if _, err := store.EditTODOPlacement(ctx, "company-a", badMove); err == nil || !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign/unknown Release placement = %v; want not-found TEACH", err)
	}
	if _, err := store.ReadTODO(ctx, "company-a", "builder", request.TODO.ID, &six); err != nil {
		t.Fatalf("failed foreign placement changed TODO revision: %v", err)
	}
	versionsCount, err := database.Collection(todoContentVersionsCollection).CountDocuments(ctx, bson.D{{Key: "project_id", Value: "builder"}, {Key: "todo_id", Value: request.TODO.ID}})
	if err != nil || versionsCount != 3 {
		t.Fatalf("event-only mutations created content versions: count=%d err=%v", versionsCount, err)
	}
	startedRetry, err = store.TransitionTODO(ctx, "company-a", TODOActionRequest{
		ProjectID: "builder", TODOID: request.TODO.ID, ExpectedRevision: &zero, OperationID: "individual-start-001", RequestedStage: "In-Progress",
		ProofReferences: []TODOProofReference{proof}, WaiverReferences: []TODOProofReference{},
	})
	if err != nil || !startedRetry.NoOp || startedRetry.TODORevision != 1 {
		t.Fatalf("durable stage retry after later events = %+v, %v", startedRetry, err)
	}
	page, err = store.ReadTODOHistory(ctx, "company-a", "builder", request.TODO.ID, -1, 100)
	if err != nil || len(page.Events) != 6 {
		t.Fatalf("durable stage retry added another history event: %+v, %v", page, err)
	}
	// Simulate a legacy current record that predates explicit section ownership.
	if _, err := database.Collection(todosCollection).UpdateOne(ctx, bson.D{{Key: "project_id", Value: "builder"}, {Key: "id", Value: request.TODO.ID}}, bson.D{{Key: "$unset", Value: bson.D{{Key: "sections.$[].ownership", Value: ""}}}}); err != nil {
		t.Fatalf("prepare legacy section ownership fixture: %v", err)
	}
	legacyRead, err := store.ReadTODO(ctx, "company-a", "builder", request.TODO.ID, &six)
	if err != nil {
		t.Fatalf("read legacy ownership fixture: %v", err)
	}
	normalizedSections := make([]TODOSection, 0, len(legacyRead.TODO.Sections))
	for _, section := range legacyRead.TODO.Sections {
		if historyRenderOnlySection(section.Key, section.Title) {
			continue
		}
		section.Ownership = "contract"
		if section.Key == "execution-evidence" || historyRecordSection(section.Key, section.Title) {
			section.Ownership = "record"
		}
		section.Position = len(normalizedSections)
		normalizedSections = append(normalizedSections, section)
	}
	sectionNormalization, err := store.NormalizeTODOSectionOwnership(ctx, "company-a", TODOSectionOwnershipRequest{ProjectID: "builder", TODOID: request.TODO.ID, ExpectedRevision: &six, OperationID: "section-ownership-001", Sections: normalizedSections, Reason: "Classify retained legacy sections explicitly"})
	if err != nil || sectionNormalization.Revision != 6 {
		t.Fatalf("governed section normalization = %+v, %v", sectionNormalization, err)
	}
	normalizedAsOf, err := store.ReadTODOAsOf(ctx, "company-a", "builder", request.TODO.ID, 6)
	if err != nil || len(normalizedAsOf.Content.Sections) != 1 || len(normalizedAsOf.State.RecordSections) != 1 || normalizedAsOf.State.ReleaseID != "r-09" {
		t.Fatalf("normalized as-of revision 6 = %+v, %v", normalizedAsOf, err)
	}
	var finalEventCount, finalVersionCount int64
	finalEventCount, err = database.Collection(todoEventsCollection).CountDocuments(ctx, bson.D{{Key: "project_id", Value: "builder"}, {Key: "todo_id", Value: request.TODO.ID}})
	if err != nil || finalEventCount != 7 {
		t.Fatalf("section normalization event count=%d err=%v; want 7", finalEventCount, err)
	}
	finalVersionCount, err = database.Collection(todoContentVersionsCollection).CountDocuments(ctx, bson.D{{Key: "project_id", Value: "builder"}, {Key: "todo_id", Value: request.TODO.ID}})
	if err != nil || finalVersionCount != 4 {
		t.Fatalf("section normalization content version count=%d err=%v; want 4", finalVersionCount, err)
	}
	if err := store.ConfigureLocalMatrix(filepath.Join("..", "..", "config", "selected_todo_local_checks.json")); err != nil {
		t.Fatalf("load local proof matrix: %v", err)
	}
	proofResult := &LocalProofResult{
		SchemaVersion: "builder-local-proof-result-v1", TODOID: request.TODO.ID,
		RunID: "11111111-2222-4333-8444-555555555555", MatrixDigest: store.localMatrix.digest,
		StartedAt:              time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano),
		FinishedAt:             time.Now().UTC().Format(time.RFC3339Nano),
		Before:                 localProofSnapshot{BuilderHead: strings.Repeat("a", 40), PACEDHead: strings.Repeat("b", 40), BuilderInputsDigest: "sha256:" + strings.Repeat("a", 64), PACEDInputsDigest: "sha256:" + strings.Repeat("b", 64)},
		OutsideUntrackedBefore: map[string]map[string]string{"builder": {}, "paced": {}},
	}
	proofResult.After = proofResult.Before
	proofResult.OutsideUntrackedAfter = proofResult.OutsideUntrackedBefore
	for _, command := range store.localMatrix.DefaultRequired {
		proofResult.Commands = append(proofResult.Commands, localProofCommand{ID: command, ExitCode: 0})
	}
	proofBytes, err := json.Marshal(proofResult)
	if err != nil {
		t.Fatal(err)
	}
	var proofFields map[string]any
	if err := json.Unmarshal(proofBytes, &proofFields); err != nil {
		t.Fatal(err)
	}
	delete(proofFields, "result_digest")
	proofBytes, err = json.Marshal(proofFields)
	if err != nil {
		t.Fatal(err)
	}
	proofResult.ResultDigest = sourceHash(proofBytes)
	seven := int64(6)
	local, err := store.TransitionTODO(ctx, "company-a", TODOActionRequest{ProjectID: "builder", TODOID: request.TODO.ID, ExpectedRevision: &seven, OperationID: "local-proof-001", RequestedStage: "Local-Implemented", LocalProof: proofResult})
	if err != nil || local.TODORevision != 7 || local.Stage != "Local-Implemented" || local.StageVerification != "verified" {
		t.Fatalf("individually admitted local transition = %+v, %v", local, err)
	}
	if _, err := store.ReadTODOAsOf(ctx, "company-a", "builder", request.TODO.ID, 6); err != nil {
		t.Fatalf("prior as-of state changed after local transition: %v", err)
	}
}
