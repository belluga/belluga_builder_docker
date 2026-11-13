package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func genericTODOAdmissionFixture() TODOAdmissionRequest {
	return TODOAdmissionRequest{OperationID: "test-create-operation", TODO: TODORecord{
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

func int64Pointer(value int64) *int64 { return &value }

func TestValidateGenericTODOAdmission(t *testing.T) {
	valid := genericTODOAdmissionFixture()
	if err := validateGenericTODOAdmission(valid.TODO); err != nil {
		t.Fatalf("valid individual TODO admission rejected: %v", err)
	}
	cases := map[string]func(*TODORecord){
		"unstable identity":         func(record *TODORecord) { record.ID = "Not a stable ID" },
		"invalid source path":       func(record *TODORecord) { record.Source.Path = "../outside.md" },
		"caller supplied revision":  func(record *TODORecord) { record.Revision = 1 },
		"caller supplied tombstone": func(record *TODORecord) { record.Removed = true },
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

func TestMultiRevisionEvidenceCapacityPreservesVerifiedReferences(t *testing.T) {
	record := genericTODOAdmissionFixture().TODO
	for i := 0; i < 65; i++ {
		record.ProofReferences = append(record.ProofReferences, TODOProofReference{Kind: fmt.Sprintf("criterion:older-%d", i), Source: "paced-local-runner", Reference: "G-TEST", SourceDigest: "sha256:" + strings.Repeat("a", 64), VerificationState: "unverified"})
	}
	if err := validateGenericTODOAdmission(record); err != nil {
		t.Fatalf("bounded multi-revision evidence rejected: %v", err)
	}
	verified := append([]TODOProofReference(nil), record.ProofReferences...)
	for i := range verified {
		verified[i].VerificationState = "verified"
	}
	if err := preserveVerifiedEvidence(verified, verified, "proof_refs"); err != nil {
		t.Fatalf("preserved verified history rejected: %v", err)
	}
	forged := append([]TODOProofReference(nil), verified...)
	forged[0].Reference = "forged-new-reference"
	if err := preserveVerifiedEvidence(nil, forged, "proof_refs"); err == nil || !strings.Contains(err.Error(), "not assert verification") {
		t.Fatalf("new caller-asserted verified reference accepted: %v", err)
	}
	removed := append([]TODOProofReference(nil), verified[1:]...)
	if err := preserveVerifiedEvidence(verified, removed, "proof_refs"); err == nil || !strings.Contains(err.Error(), "preserve every previously verified") {
		t.Fatalf("removed verified reference accepted: %v", err)
	}
	record.ProofReferences = append(record.ProofReferences, make([]TODOProofReference, maxTODOProofs-len(record.ProofReferences)+1)...)
	if err := validateGenericTODOAdmission(record); err == nil {
		t.Fatal("evidence above the shared bound was accepted")
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
	record.Sections = []TODOSection{}
	record.Content = &TODOContent{SchemaVersion: todoContentSchemaVersion}
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
	if !containsTODOString(template.Required, "todo.sections") || template.AdministrativeExample.TODO.Stage != "Completed" || template.AdministrativeExample.InitialStateAcceptance == nil {
		t.Fatalf("template omits required sections or complete administrative example: %+v", template)
	}
	if err := validateMongoTODOCreationRequest(template.AdministrativeExample); err != nil {
		t.Fatalf("administrative template example rejected: %v", err)
	}
	if !containsTODOString(template.Optional, "todo.release_id") || !containsTODOString(template.Optional, "todo.position") || template.Limits["record_bytes"] != maxTODORecordBytes {
		t.Fatalf("template omits Backlog placement or record limits: %+v", template)
	}
	if !containsTODOString(template.Optional, "todo.content.delivery") || !containsTODOString(template.ContentEditRequired, "content") || !containsTODOString(template.ContentEditRequired, "content.title") || !containsTODOString(template.ContentEditRequired, "content.diff_expectation") || !strings.Contains(template.ContentEditGuide, "read-todo") {
		t.Fatalf("template does not teach a governed content edit: %+v", template)
	}
	if !strings.Contains(template.ContentShape["proof_refs"], "paced-local-runner") || !strings.Contains(template.ContentShape["proof_refs"], "exact TODO content_digest") || !strings.Contains(template.ContentEditGuide, "BEFORE running a fresh local runner") || !strings.Contains(template.ContentEditGuide, "<successful-check-id>") || len(template.Example.TODO.Content.Criteria) != 1 || len(template.Example.TODO.Content.ValidationScenarios) != 1 {
		t.Fatalf("template does not teach copyable current-digest local proof references: %+v", template)
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

func TestAdministrativeInitialStateAndDispositionValidation(t *testing.T) {
	for _, stage := range canonicalTODOStages[1:] {
		t.Run(stage, func(t *testing.T) {
			record := mongoBornTODOFixture()
			record.Stage = stage
			request := TODOAdmissionRequest{SchemaVersion: todoCreationSchemaVersion, OperationID: "admin-stage-create", TODO: record,
				InitialStateAcceptance: &TODOInitialStateAcceptance{OwnerReference: "owner-acceptance-17", Reason: "Owner confirms this recorded initial state."}}
			if err := validateMongoTODOCreationRequest(request); err != nil {
				t.Fatalf("valid accepted initial stage rejected: %v", err)
			}
			request.InitialStateAcceptance = nil
			if err := validateMongoTODOCreationRequest(request); !errors.Is(err, ErrInvalid) {
				t.Fatalf("non-Planning stage without acceptance = %v; want invalid", err)
			}
		})
	}
	planning := mongoBornTODOFixture()
	request := TODOAdmissionRequest{SchemaVersion: todoCreationSchemaVersion, OperationID: "disposition-create", TODO: planning,
		InitialStateAcceptance: &TODOInitialStateAcceptance{OwnerReference: "owner-acceptance-18", Reason: "Owner confirms non-delivery."},
		Disposition:            &TODODisposition{Kind: "Superseded", Reason: "Replaced by the current TODO.", SuccessorTODOID: "builder-current-todo"}}
	if err := validateMongoTODOCreationRequest(request); err != nil {
		t.Fatalf("valid disposition rejected: %v", err)
	}
	for name, mutate := range map[string]func(*TODOAdmissionRequest){
		"completed conflict": func(value *TODOAdmissionRequest) { value.TODO.Stage = "Completed" },
		"missing reason":     func(value *TODOAdmissionRequest) { value.Disposition.Reason = "" },
		"self successor":     func(value *TODOAdmissionRequest) { value.Disposition.SuccessorTODOID = value.TODO.ID },
		"missing acceptance": func(value *TODOAdmissionRequest) { value.InitialStateAcceptance = nil },
	} {
		t.Run(name, func(t *testing.T) {
			invalid := request
			invalid.Disposition = ptrDisposition(*request.Disposition)
			invalid.InitialStateAcceptance = ptrInitialAcceptance(*request.InitialStateAcceptance)
			mutate(&invalid)
			if err := validateMongoTODOCreationRequest(invalid); !errors.Is(err, ErrInvalid) {
				t.Fatalf("invalid disposition accepted: %v", err)
			}
		})
	}
}

func ptrDisposition(value TODODisposition) *TODODisposition { return &value }
func ptrInitialAcceptance(value TODOInitialStateAcceptance) *TODOInitialStateAcceptance {
	return &value
}

func TestRoadmapSeparatesAdministrativeAcceptanceAndDisposition(t *testing.T) {
	accepted := mongoBornTODOFixture()
	accepted.Stage = "Completed"
	accepted.StageVerification = "unverified"
	accepted.AdministrativeAcceptance = &TODOAdministrativeAcceptance{AcceptedStage: "Completed", OwnerReference: "owner-ref", Reason: "Owner accepted current state.", PermissionDecision: "pre-iam-private-api-allow-all", AcceptedAt: bson.DateTime(time.Now().UTC().UnixMilli()), ContentDigest: sourceHash([]byte("content"))}
	view := roadmapTODOViewFromRecord(accepted)
	if view.DeliveryStage != "Completed" || view.DeliveryStageState != "administrative" || view.DeliveryStageBasis != "administrative-acceptance" || view.AdministrativeAcceptance == nil {
		t.Fatalf("administrative Roadmap projection = %+v", view)
	}
	disposed := mongoBornTODOFixture()
	disposed.Disposition = &TODODisposition{Kind: "Discarded", Reason: "No longer needed."}
	disposed.DispositionState = "valid"
	disposed.AdministrativeAcceptance = &TODOAdministrativeAcceptance{AcceptedStage: "Planning", OwnerReference: "owner-ref", Reason: "Owner accepted disposition.", PermissionDecision: "pre-iam-private-api-allow-all", AcceptedAt: bson.DateTime(time.Now().UTC().UnixMilli()), ContentDigest: sourceHash([]byte("content"))}
	disposedView := roadmapTODOViewFromRecord(disposed)
	if disposedView.Kind != "non-delivery" || disposedView.DispositionState != "valid" || disposedView.Disposition == nil || disposedView.DeliveryStage != "" {
		t.Fatalf("non-delivery Roadmap projection = %+v", disposedView)
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
	if response.Code != http.StatusOK || service.companyID != "belluga-solutions" || service.projectID != "builder" || !strings.Contains(response.Body.String(), `"schema_version":"builder-todo-creation-v2"`) {
		t.Fatalf("Project creation template response = %d %s; company=%q project=%q", response.Code, response.Body.String(), service.companyID, service.projectID)
	}
	var publicTemplate TODOCreationTemplate
	if err := json.Unmarshal(response.Body.Bytes(), &publicTemplate); err != nil {
		t.Fatal(err)
	}
	if !containsTODOString(publicTemplate.ContentEditRequired, "content") || !containsTODOString(publicTemplate.ContentEditRequired, "content.title") || !strings.Contains(publicTemplate.ContentEditGuide, "todo_revision") || !strings.Contains(publicTemplate.ContentEditGuide, "content.delivery") || !strings.Contains(publicTemplate.ContentEditGuide, "paced-local-runner") || !strings.Contains(publicTemplate.ContentEditGuide, "<successful-check-id>") || !strings.Contains(response.Body.String(), "example-criterion") {
		t.Fatalf("served template does not teach the update-todo envelope: %+v", publicTemplate)
	}
}

func TestMongoImportedEditedTODOStageFlow(t *testing.T) {
	uri := os.Getenv("REGISTRY_TEST_MONGODB_URI")
	if uri == "" {
		if os.Getenv("REGISTRY_REQUIRE_MONGO_INTEGRATION") == "1" {
			t.Fatal("required Mongo replica-set integration fixture is unavailable: set REGISTRY_TEST_MONGODB_URI")
		}
		t.Skip("isolated replica-set MongoDB fixture not configured")
	}
	ctx := context.Background()
	stamp := strconv.FormatInt(time.Now().UnixNano(), 36)
	store, err := OpenMongo(ctx, uri, stamp)
	if err != nil {
		t.Fatal(err)
	}
	store.tenantPrefix = stamp
	databaseName := store.tenantDatabase("company-a")
	database := store.client.Database(databaseName)
	t.Cleanup(func() {
		if err := database.Drop(context.Background()); err != nil {
			t.Errorf("drop isolated tenant database %q: %v", databaseName, err)
		}
		if err := store.control.Drop(context.Background()); err != nil {
			t.Errorf("drop isolated control database %q: %v", stamp, err)
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
	project := Project{ID: "builder", CompanyID: "company-a", Name: "Builder", Binding: ptrTestBinding(validTestBinding()), RegisteredAt: time.Now().UTC().Format(time.RFC3339Nano), PreparationState: "not_ready"}
	projectRecord, err := projectToDocument(project, bson.NewObjectID())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Collection(projectsCollection).InsertOne(ctx, projectRecord); err != nil {
		t.Fatal(err)
	}
	if err := store.ProvisionTODOCollection(ctx, "company-a"); err != nil {
		t.Fatal(err)
	}
	if err := store.ProvisionTODOHistory(ctx, "company-a"); err != nil {
		t.Fatal(err)
	}
	epic := roadmapEpic{StorageID: bson.NewObjectID(), ProjectID: "builder", ID: "e-06", Name: "Database", Position: 0}
	release := roadmapRelease{StorageID: bson.NewObjectID(), ProjectID: "builder", ID: "r-08", EpicID: "e-06", Name: "Mongo", Position: 0}
	if _, err := database.Collection(epicsCollection).InsertOne(ctx, epic); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Collection(releasesCollection).InsertOne(ctx, release); err != nil {
		t.Fatal(err)
	}
	admission := genericTODOAdmissionFixture()
	admission.TODO.ID = "builder-mongo-imported-edited-stage-flow"
	admission.TODO.Source.Path = "todos/active/TODO-" + admission.TODO.ID + ".md"
	admission.TODO.Source.Digest = sourceHash([]byte(admission.TODO.ID))
	if _, err := store.AdmitTODO(ctx, "company-a", "builder", admission); err != nil {
		t.Fatalf("admit imported TODO: %v", err)
	}
	legacyContent := contentFromTODO(admission.TODO)
	if _, err := database.Collection(todoContentVersionsCollection).UpdateOne(ctx,
		bson.D{{Key: "project_id", Value: "builder"}, {Key: "todo_id", Value: admission.TODO.ID}, {Key: "revision", Value: int64(0)}},
		bson.D{{Key: "$unset", Value: bson.D{{Key: "digest", Value: ""}}}}); err != nil {
		t.Fatalf("make revision-zero import history legacy-digestless: %v", err)
	}
	for revision := int64(1); revision <= 4; revision++ {
		version := TODOContentVersion{ObjectID: bson.NewObjectID(), ProjectID: "builder", TODOID: admission.TODO.ID, Revision: revision, Content: legacyContent}
		if _, err := database.Collection(todoContentVersionsCollection).InsertOne(ctx, version); err != nil {
			t.Fatalf("insert legacy digestless content version %d: %v", revision, err)
		}
		event := TODOHistoryEvent{ObjectID: bson.NewObjectID(), ProjectID: "builder", TODOID: admission.TODO.ID, Revision: revision, Kind: "content_changed", OccurredAt: time.Now().UTC(), OperationID: fmt.Sprintf("fixture-legacy-content-%d", revision), RequestDigest: sourceHash([]byte(fmt.Sprintf("fixture-%d", revision))), ContentRevision: &revision}
		if _, err := database.Collection(todoEventsCollection).InsertOne(ctx, event); err != nil {
			t.Fatalf("insert legacy content event %d: %v", revision, err)
		}
	}
	if _, err := database.Collection(todosCollection).UpdateOne(ctx, bson.D{{Key: "project_id", Value: "builder"}, {Key: "id", Value: admission.TODO.ID}}, bson.D{{Key: "$set", Value: bson.D{{Key: "revision", Value: int64(4)}}}}); err != nil {
		t.Fatalf("set observed imported TODO revision: %v", err)
	}
	read, err := store.ReadTODO(ctx, "company-a", "builder", admission.TODO.ID, nil)
	if err != nil || read.ContentRevision != nil || read.ContentDigest != "" {
		t.Fatalf("legacy digestless imported TODO read = %+v, %v", read, err)
	}
	readJSON, marshalErr := json.Marshal(read)
	if marshalErr != nil || strings.Contains(string(readJSON), `"content_revision"`) || strings.Contains(string(readJSON), `"content_digest"`) {
		t.Fatalf("legacy digestless TODO serialized an incomplete content identity: %s (marshal error: %v)", readJSON, marshalErr)
	}
	transition := func(revision int64, operation, stage string, proofs []TODOProofReference) (TODOActionResponse, error) {
		return store.TransitionTODO(ctx, "company-a", TODOActionRequest{ProjectID: "builder", TODOID: admission.TODO.ID, ExpectedRevision: int64Pointer(revision), OperationID: operation, RequestedStage: stage, ProofReferences: proofs, WaiverReferences: []TODOProofReference{}})
	}
	oldProof := TODOProofReference{Kind: "owner-approval", Source: "owner-chat", Reference: "APROVADO", SourceDigest: admission.TODO.Source.Digest, VerificationState: "unverified"}
	if _, err := transition(4, "legacy-digestless-shortcut", "In-Progress", []TODOProofReference{oldProof}); !errors.Is(err, ErrConflict) {
		t.Fatalf("digestless imported TODO reused stale Foundation approval: %v", err)
	}
	content := contentFromTODO(read.TODO)
	content.Title += " authored edit"
	edit, err := store.EditTODOContent(ctx, "company-a", TODOContentEditRequest{
		ProjectID: "builder", TODOID: admission.TODO.ID, ExpectedRevision: int64Pointer(4), OperationID: "imported-edited-content", Content: content, Reason: "Bind stage approvals to the authored imported revision",
	})
	if err != nil || edit.ContentRevision == nil || *edit.ContentRevision != 5 || !validSourceDigest(edit.ContentDigest) {
		t.Fatalf("imported authored content edit = %+v, %v", edit, err)
	}
	if _, err := transition(5, "stale-shortcut", "In-Progress", []TODOProofReference{oldProof}); !errors.Is(err, ErrConflict) {
		t.Fatalf("edited imported legacy shortcut = %v; want conflict", err)
	}
	currentProof := TODOProofReference{Kind: "owner-approval", Source: "mongo-content-revision", Reference: "builder/" + admission.TODO.ID + "/content/5", SourceDigest: admission.TODO.Source.Digest, VerificationState: "unverified"}
	if _, err := transition(5, "stale-content-digest", "Implementation-Approval-Pending", []TODOProofReference{currentProof}); !errors.Is(err, ErrConflict) {
		t.Fatalf("Foundation digest on imported content approval = %v; want conflict", err)
	}
	currentProof.SourceDigest = edit.ContentDigest
	if _, err := transition(5, "current-content-approval", "Implementation-Approval-Pending", []TODOProofReference{currentProof}); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "source/content_revision") {
		t.Fatalf("v1 authored edit became operational stage authority: %v", err)
	}
	read, err = store.ReadTODO(ctx, "company-a", "builder", admission.TODO.ID, nil)
	if err != nil || read.TODO.Stage != "Planning" || read.TODO.Revision != 5 || read.TODO.StageVerification != "unverified" {
		t.Fatalf("refused v1 transition changed operational state: %+v, %v", read.TODO, err)
	}
	if _, err := store.VerifyCurrentTODOStage(ctx, "company-a", VerifyCurrentTODOStageRequest{ProjectID: "builder", TODOID: admission.TODO.ID, ExpectedRevision: int64Pointer(5), OperationID: "reject-v1-stage-verification"}); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "source/content_revision") {
		t.Fatalf("v1 historical stage verification was not refused: %v", err)
	}
	// Seed an imported record with a current v2 history snapshot. The source
	// remains Foundation provenance; all newly requested edges use Mongo content.
	imported := genericTODOAdmissionFixture()
	imported.TODO.ID = "imported-typed-planning"
	imported.TODO.Source.Path = "todos/active/TODO-imported-typed-planning.md"
	if _, err := store.AdmitTODO(ctx, "company-a", "builder", imported); err != nil {
		t.Fatal(err)
	}
	typed := TODOAuthoredContent{TODOContent: validTODOContent(), Title: imported.TODO.Title, Objective: imported.TODO.Objective, DiffExpectation: []TODORepositoryScope{}, AffectedFiles: []string{}}
	typed.ValidationScenarios = append(typed.ValidationScenarios,
		TODOValidationScenario{ID: "scenario-vet", CriterionIDs: []string{"criterion-one"}, Method: "Run Go vet in the selected test image.", Expected: "Static checks pass."},
		TODOValidationScenario{ID: "scenario-image", CriterionIDs: []string{"criterion-one"}, Method: "Build the registry runtime image.", Expected: "The image builds."},
	)
	typed.DiffExpectation = []TODORepositoryScope{{Repository: "Builder", RepositoryID: "owner/repo", DeliveryBranch: "r-08-rc", Baseline: strings.Repeat("a", 40), ExpectedPaths: []string{"go-app/**"}, NotExpectedPaths: []string{"flutter-app/**"}}}
	typed.LocalChecks = []TODOLocalCheck{
		{ID: "G-TEST", RepositoryID: "owner/repo", Argv: []string{"go", "test", "./..."}, WorkingDirectory: ".", InputPaths: []string{"go-app/**"}},
		{ID: "G-VET", RepositoryID: "owner/repo", Argv: []string{"go", "vet", "./..."}, WorkingDirectory: ".", InputPaths: []string{"go-app/**"}},
		{ID: "G-IMAGE", RepositoryID: "owner/repo", Argv: []string{"docker", "build", "--target", "runtime", "."}, WorkingDirectory: ".", InputPaths: []string{"go-app/**", "go-app/Dockerfile"}},
	}
	typed.Planning = &TODOPlanning{Complexity: "small", AuditTriggers: testAuditTriggers(), Assumptions: []TODOPlanningAssumption{}, Reviews: []TODOPlanningReview{{Kind: "critique", Reference: "review/critique"}}}
	typedDigest, err := todoContentRevisionDigest("builder", imported.TODO.ID, 1, typed)
	if err != nil {
		t.Fatal(err)
	}
	version := TODOContentVersion{ObjectID: bson.NewObjectID(), ProjectID: "builder", TODOID: imported.TODO.ID, Revision: 1, Content: typed, Digest: typedDigest}
	if _, err := database.Collection(todoContentVersionsCollection).InsertOne(ctx, version); err != nil {
		t.Fatal(err)
	}
	one := int64(1)
	event := TODOHistoryEvent{ObjectID: bson.NewObjectID(), ProjectID: "builder", TODOID: imported.TODO.ID, Revision: one, Kind: "content_changed", OccurredAt: time.Now().UTC(), OperationID: "fixture-typed-content", RequestDigest: sourceHash([]byte("typed")), ContentRevision: &one}
	if _, err := database.Collection(todoEventsCollection).InsertOne(ctx, event); err != nil {
		t.Fatal(err)
	}
	planningProof := TODOProofReference{Kind: "planning-stage", Source: "mongo-content-revision", Reference: "builder/" + imported.TODO.ID + "/content/1", SourceDigest: typedDigest, VerificationState: "verified"}
	if _, err := database.Collection(todosCollection).UpdateOne(ctx, bson.D{{Key: "project_id", Value: "builder"}, {Key: "id", Value: imported.TODO.ID}}, bson.D{{Key: "$set", Value: bson.D{{Key: "revision", Value: one}, {Key: "diff_expectation", Value: typed.DiffExpectation}, {Key: "stage_verification", Value: "verified"}, {Key: "verified_transition_ref", Value: planningProof}}}}); err != nil {
		t.Fatal(err)
	}
	reviewRevision := recordPlanningReviewSubjects(t, ctx, store, "builder", imported.TODO.ID, "typed-review-subject")
	if _, err := store.TransitionTODO(ctx, "company-a", TODOActionRequest{
		ProjectID: "builder", TODOID: imported.TODO.ID, ExpectedRevision: &reviewRevision,
		OperationID: "reject-unconfirmed-implementation-repository", RequestedStage: "Implementation-Approval-Pending",
	}); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "confirm this Git-backed delivery repository") {
		t.Fatalf("Planning exit accepted an unconfirmed Project repository: %v", err)
	}
	if _, err := store.ChangeImplementationRepository(ctx, "company-a", "builder", ImplementationRepositoryChange{
		Action: "confirm", ProjectID: "builder", ExpectedRevision: 0,
		OperationID: "typed-planning-repository-confirmation", Actor: "fixture-owner",
		OwnerReference: "fixture-owner-confirmed-owner-repo", RepositoryID: "owner/repo",
	}); err != nil {
		t.Fatalf("confirm the code-backed fixture repository before Planning exit: %v", err)
	}
	projectBeforeBranch, err := store.Project(ctx, "company-a", "builder")
	if err != nil {
		t.Fatal(err)
	}
	branchRevision := projectBeforeBranch.RoadmapRevision
	if _, err := store.ChangeRoadmap(ctx, "company-a", "confirm-release-branch", RoadmapChange{
		ProjectID: "builder", ExpectedRoadmapRevision: &branchRevision,
		OperationID: "typed-planning-release-branch", ID: "r-08", RepositoryID: "owner/repo",
		Branch: "r-08-rc", OwnerReference: "fixture-owner-confirmed-release-branch",
	}); err != nil {
		t.Fatalf("confirm the code-backed fixture Release branch before Planning exit: %v", err)
	}
	// Two independent agents may submit the same approved predecessor at once.
	// Exactly one stage event must win the aggregate revision CAS.
	results := make(chan error, 2)
	for _, operationID := range []string{"typed-planning-a", "typed-planning-b"} {
		go func(operationID string) {
			_, transitionErr := store.TransitionTODO(ctx, "company-a", TODOActionRequest{
				ProjectID: "builder", TODOID: imported.TODO.ID, ExpectedRevision: &reviewRevision,
				OperationID: operationID, RequestedStage: "Implementation-Approval-Pending",
				ProofReferences: []TODOProofReference{}, WaiverReferences: []TODOProofReference{},
			})
			results <- transitionErr
		}(operationID)
	}
	wins, conflicts := 0, 0
	for range 2 {
		switch transitionErr := <-results; {
		case transitionErr == nil:
			wins++
		case errors.Is(transitionErr, ErrConflict):
			conflicts++
		default:
			t.Fatalf("concurrent typed planning transition: %v", transitionErr)
		}
	}
	if wins != 1 || conflicts != 1 {
		t.Fatalf("concurrent typed planning outcomes: wins=%d conflicts=%d", wins, conflicts)
	}
	current, err := store.ReadTODO(ctx, "company-a", "builder", imported.TODO.ID, nil)
	if err != nil || current.ContentDigest != typedDigest || current.TODO.Source.Kind != "mongo-content-revision" || current.TODO.Source.Foundation != "" || current.TODO.Source.Path != "" || current.TODO.Source.Digest != typedDigest || current.TODO.VerifiedTransitionRef == nil || current.TODO.VerifiedTransitionRef.Kind != "planning-reviewed" {
		t.Fatalf("typed imported transition exposed legacy source or ignored Mongo content: %+v, %v", current, err)
	}
	two := int64(3)
	approval := TODOProofReference{Kind: "owner-approval", Source: "owner-chat", Reference: "APROVADO", SourceDigest: typedDigest, VerificationState: "unverified"}
	if _, err := store.TransitionTODO(ctx, "company-a", TODOActionRequest{ProjectID: "builder", TODOID: imported.TODO.ID, ExpectedRevision: &two, OperationID: "typed-approval", RequestedStage: "Ready-for-Implementation", ProofReferences: []TODOProofReference{approval}}); err != nil {
		t.Fatalf("typed imported approval: %v", err)
	}
	projectBeforeTypedPlacement, err := store.Project(ctx, "company-a", "builder")
	if err != nil {
		t.Fatal(err)
	}
	three, roadmapRevision := int64(4), projectBeforeTypedPlacement.RoadmapRevision
	if _, err := store.EditTODOPlacement(ctx, "company-a", TODOPlacementEditRequest{ProjectID: "builder", TODOID: imported.TODO.ID, ExpectedRevision: &three, ExpectedRoadmapRevision: &roadmapRevision, OperationID: "typed-position-change", ReleaseID: "r-08", Position: 4, Reason: "Review the changed placement"}); err != nil {
		t.Fatalf("typed imported position change: %v", err)
	}
	current, err = store.ReadTODO(ctx, "company-a", "builder", imported.TODO.ID, nil)
	if err != nil || current.TODO.Stage != "Planning" || current.TODO.Position == nil || *current.TODO.Position != 4 {
		t.Fatalf("changed placement retained prior approval: %+v, %v", current, err)
	}
	migration := genericTODOAdmissionFixture()
	migration.TODO.ID = "imported-migrated-planning"
	migration.TODO.Source.Path = "todos/active/TODO-imported-migrated-planning.md"
	migration.TODO.DiffExpectation = []TODORepositoryScope{{Repository: "Foundation", Baseline: strings.Repeat("b", 40), ExpectedPaths: []string{"todos/**"}, NotExpectedPaths: []string{}}}
	if _, err := store.AdmitTODO(ctx, "company-a", "builder", migration); err != nil {
		t.Fatal(err)
	}
	var legacyVersion TODOContentVersion
	if err := database.Collection(todoContentVersionsCollection).FindOne(ctx, bson.D{{Key: "project_id", Value: "builder"}, {Key: "todo_id", Value: migration.TODO.ID}, {Key: "revision", Value: int64(0)}}).Decode(&legacyVersion); err != nil {
		t.Fatal(err)
	}
	zeroMigration := int64(0)
	migrationContent := typed
	section := migration.TODO.Sections[0]
	migrationContent.LegacyContext = []TODOLegacyContext{{SourceRevision: migration.TODO.Source.Revision, SectionPosition: 0, Ownership: "contract", Title: section.Title, Body: section.BodyMarkdown, Digest: todoBodyDigest(section.BodyMarkdown)}}
	migrated, err := store.MigrateTODOContent(ctx, "company-a", TODOContentMigrationRequest{ProjectID: "builder", TODOID: migration.TODO.ID, ExpectedRevision: &zeroMigration, ExpectedContentRevision: 0, ExpectedContentDigest: legacyVersion.Digest, ExpectedSourceReference: todoHistoricalSourceReference(migration.TODO.Source), ExpectedSourceDigest: migration.TODO.Source.Digest, OperationID: "migrate-imported-planning", Reason: "Convert the reviewed contract", ReviewedAcceptance: todoContentMigrationAcceptance, Content: migrationContent})
	if err != nil {
		t.Fatalf("public imported conversion: %v", err)
	}
	if _, err := store.RestoreTODOContent(ctx, "company-a", TODOContentRestoreRequest{ProjectID: "builder", TODOID: migration.TODO.ID, ExpectedRevision: &migrated.Revision, OperationID: "reject-v1-rollback", ContentRevision: 0, Reason: "Historical content must not become active"}); err == nil || !strings.Contains(err.Error(), "content.schema_version") {
		t.Fatalf("v2-to-v1 restore accepted or wrong TEACH: %v", err)
	}
	if _, err := store.EditTODOContent(ctx, "company-a", TODOContentEditRequest{ProjectID: "builder", TODOID: migration.TODO.ID, ExpectedRevision: &migrated.Revision, OperationID: "reject-v1-edit", Content: legacyVersion.Content, Reason: "Historical content must not become active"}); err == nil || !strings.Contains(err.Error(), "content.schema_version") {
		t.Fatalf("v2-to-v1 edit accepted or wrong TEACH: %v", err)
	}
	if _, err := store.NormalizeTODOSectionOwnership(ctx, "company-a", TODOSectionOwnershipRequest{ProjectID: "builder", TODOID: migration.TODO.ID, ExpectedRevision: &migrated.Revision, OperationID: "reject-v1-normalization", Sections: migration.TODO.Sections, Reason: "Historical sections must not become active"}); err == nil || !strings.Contains(err.Error(), "content.schema_version") {
		t.Fatalf("v2-to-v1 normalization accepted or wrong TEACH: %v", err)
	}
	// Model a previously migrated aggregate whose old v1 diff row was retained.
	if _, err := database.Collection(todosCollection).UpdateOne(ctx, bson.D{{Key: "project_id", Value: "builder"}, {Key: "id", Value: migration.TODO.ID}}, bson.D{{Key: "$set", Value: bson.D{{Key: "diff_expectation", Value: migration.TODO.DiffExpectation}}}}); err != nil {
		t.Fatal(err)
	}
	projectedRead, err := store.ReadTODO(ctx, "company-a", "builder", migration.TODO.ID, nil)
	if err != nil || !reflect.DeepEqual(projectedRead.TODO.DiffExpectation, typed.DiffExpectation) {
		t.Fatal(err)
	}
	roadmap, err := store.Roadmap(ctx, "builder", "company-a")
	if err != nil || strings.Contains(string(roadmap), migration.TODO.Source.Path) {
		t.Fatalf("Roadmap exposed Foundation path for v2: %s, %v", roadmap, err)
	}
	unchanged, err := store.EditTODOContent(ctx, "company-a", TODOContentEditRequest{ProjectID: "builder", TODOID: migration.TODO.ID, ExpectedRevision: &migrated.Revision, OperationID: "unchanged-projected-contract", Content: contentFromTODO(projectedRead.TODO), Reason: "Confirm unchanged operational content"})
	if err != nil || !unchanged.NoOp || unchanged.Revision != migrated.Revision || unchanged.ContentDigest != migrated.ContentDigest {
		t.Fatalf("projected no-op created an archive-only revision: %+v, %v", unchanged, err)
	}
	if _, err := store.VerifyCurrentTODOStage(ctx, "company-a", VerifyCurrentTODOStageRequest{ProjectID: "builder", TODOID: migration.TODO.ID, ExpectedRevision: &migrated.Revision, OperationID: "verify-migrated-planning"}); err != nil {
		t.Fatalf("verify converted Planning: %v", err)
	}
	twoMigration := recordPlanningReviewSubjects(t, ctx, store, "builder", migration.TODO.ID, "migrated-review-subject")
	if _, err := store.TransitionTODO(ctx, "company-a", TODOActionRequest{ProjectID: "builder", TODOID: migration.TODO.ID, ExpectedRevision: &twoMigration, OperationID: "review-migrated-planning", RequestedStage: "Implementation-Approval-Pending"}); err != nil {
		t.Fatalf("review converted Planning: %v", err)
	}
	migrationRead, err := store.ReadTODO(ctx, "company-a", "builder", migration.TODO.ID, nil)
	if err != nil || migrationRead.ContentDigest != migrated.ContentDigest || migrationRead.TODO.Content == nil || migrationRead.TODO.Content.LegacyContext != nil || migrationRead.TODO.Source.Kind != "mongo-content-revision" || migrationRead.TODO.Source.Foundation != "" || migrationRead.TODO.Source.Digest != migrated.ContentDigest || migrationRead.TODO.VerifiedTransitionRef == nil || migrationRead.TODO.VerifiedTransitionRef.SourceDigest != migrated.ContentDigest {
		t.Fatalf("converted TODO did not project current Mongo source: %+v, %v", migrationRead, err)
	}
	oldApproval := TODOProofReference{Kind: "owner-approval", Source: "owner-chat", Reference: "APROVADO", SourceDigest: legacyVersion.Digest, VerificationState: "verified"}
	priorApprovalRecord := migrationRead.TODO
	priorApprovalRecord.Stage, priorApprovalRecord.StageVerification, priorApprovalRecord.VerifiedTransitionRef = "Ready-for-Implementation", "verified", &oldApproval
	if _, err := store.mongoBornTransitionProof(ctx, "company-a", project, priorApprovalRecord, TODOActionRequest{RequestedStage: "In-Progress"}); err == nil || !strings.Contains(err.Error(), "approval/content_revision") {
		t.Fatalf("old v1 approval authorized a new v2 stage: %v", err)
	}
	priorApprovalRecord.Stage = "In-Progress"
	if _, err := store.mongoBornTransitionProof(ctx, "company-a", project, priorApprovalRecord, TODOActionRequest{RequestedStage: "Local-Implemented"}); err == nil || !strings.Contains(err.Error(), "approval/content_revision") {
		t.Fatalf("old v1 approval authorized v2 local delivery: %v", err)
	}
	var storedMigration TODOContentVersion
	if err := database.Collection(todoContentVersionsCollection).FindOne(ctx, bson.D{{Key: "project_id", Value: "builder"}, {Key: "todo_id", Value: migration.TODO.ID}, {Key: "revision", Value: *migrated.ContentRevision}}).Decode(&storedMigration); err != nil || len(storedMigration.Content.LegacyContext) != 1 || storedMigration.Digest != migrated.ContentDigest {
		t.Fatalf("archived legacy content was not retained: %+v, %v", storedMigration, err)
	}
	if _, err := store.Register(ctx, Registration{CompanyID: "company-a", CompanyName: "Company A", ProjectID: "no-foundation", ProjectName: "No Foundation"}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Collection(epicsCollection).InsertOne(ctx, roadmapEpic{StorageID: bson.NewObjectID(), ProjectID: "no-foundation", ID: "e-01", Name: "Delivery", Position: 0}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Collection(releasesCollection).InsertOne(ctx, roadmapRelease{StorageID: bson.NewObjectID(), ProjectID: "no-foundation", ID: "r-01", EpicID: "e-01", Name: "Release", Position: 0}); err != nil {
		t.Fatal(err)
	}
	builderBorn := mongoBornTODOFixture()
	builderBorn.ID, builderBorn.ProjectID, builderBorn.ReleaseID, builderBorn.Position = "no-foundation-todo", "no-foundation", "r-01", intPointer(0)
	builderBorn.Content = &typed.TODOContent
	builderBorn.DiffExpectation = typed.DiffExpectation
	if _, err := store.AdmitTODO(ctx, "company-a", "no-foundation", TODOAdmissionRequest{SchemaVersion: todoCreationSchemaVersion, OperationID: "builder-born-no-foundation", TODO: builderBorn}); err != nil {
		t.Fatalf("Foundation-free TODO admission: %v", err)
	}
	zero := int64(0)
	projectBeforeMove, err := store.Project(ctx, "company-a", "no-foundation")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.EditTODOPlacement(ctx, "company-a", TODOPlacementEditRequest{ProjectID: "no-foundation", TODOID: builderBorn.ID, ExpectedRevision: &zero, ExpectedRoadmapRevision: &projectBeforeMove.RoadmapRevision, OperationID: "planning-placement-change", ReleaseID: "r-01", Position: 1, Reason: "Change placement while planning"}); err != nil {
		t.Fatalf("move while Planning: %v", err)
	}
	currentRevision := int64(1)
	if _, err := store.TransitionTODO(ctx, "company-a", TODOActionRequest{ProjectID: "no-foundation", TODOID: builderBorn.ID, ExpectedRevision: &currentRevision, OperationID: "reject-planning-placement-review", RequestedStage: "Implementation-Approval-Pending"}); err == nil || !strings.Contains(err.Error(), "planning.reviews") {
		t.Fatalf("old Planning review authorized changed placement: %v", err)
	}
	planningRead, err := store.ReadTODO(ctx, "company-a", "no-foundation", builderBorn.ID, nil)
	if err != nil || planningRead.TODO.Stage != "Planning" || planningRead.TODORevision != currentRevision {
		t.Fatalf("rejected transition mutated stage: %+v %v", planningRead, err)
	}
	newPlanning := contentFromTODO(planningRead.TODO)
	newPlanning.LocalChecks = []TODOLocalCheck{
		{ID: "G-TEST", RepositoryID: "owner/repo", Argv: []string{"go", "test", "./..."}, WorkingDirectory: ".", InputPaths: []string{"go-app/**"}},
		{ID: "G-VET", RepositoryID: "owner/repo", Argv: []string{"go", "vet", "./..."}, WorkingDirectory: ".", InputPaths: []string{"go-app/**"}},
		{ID: "G-IMAGE", RepositoryID: "owner/repo", Argv: []string{"docker", "build", "--target", "runtime", "."}, WorkingDirectory: ".", InputPaths: []string{"go-app/**", "go-app/Dockerfile"}},
	}
	newPlanning.Planning.Reviews[0].Reference = "review/critique-after-planning-move"
	newPlanning.Planning.Assumptions = []TODOPlanningAssumption{{ID: "live-code", Status: "live", Statement: "The approved code exists.", CodeRefs: []TODOPlanningCodeRef{{RepositoryID: "owner/repo", Path: "go-app/main.go"}}}}
	newPlanning.Planning.Reviews = append(newPlanning.Planning.Reviews, TODOPlanningReview{Kind: "coherence", Reference: "review/coherence-after-move"})
	if _, err := store.EditTODOContent(ctx, "company-a", TODOContentEditRequest{ProjectID: "no-foundation", TODOID: builderBorn.ID, ExpectedRevision: &currentRevision, OperationID: "refresh-planning-placement-review", Content: newPlanning, Reason: "Review changed placement"}); err != nil {
		t.Fatalf("refresh Planning review: %v", err)
	}
	currentRevision++
	if _, err := store.ChangeImplementationRepository(ctx, "company-a", "no-foundation", ImplementationRepositoryChange{
		Action: "confirm", ProjectID: "no-foundation", ExpectedRevision: 0,
		OperationID: "anchor-fixture-repository-confirmation", Actor: "fixture-owner",
		OwnerReference: "fixture-owner-confirmed-owner-repo", RepositoryID: "owner/repo",
	}); err != nil {
		t.Fatalf("confirm the code-anchor fixture repository before Planning exit: %v", err)
	}
	projectBeforeAnchorBranch, err := store.Project(ctx, "company-a", "no-foundation")
	if err != nil {
		t.Fatal(err)
	}
	anchorBranchRevision := projectBeforeAnchorBranch.RoadmapRevision
	if _, err := store.ChangeRoadmap(ctx, "company-a", "confirm-release-branch", RoadmapChange{
		ProjectID: "no-foundation", ExpectedRoadmapRevision: &anchorBranchRevision,
		OperationID: "anchor-fixture-release-branch", ID: "r-01", RepositoryID: "owner/repo",
		Branch: "builder", OwnerReference: "fixture-owner-confirmed-anchor-release-branch",
	}); err != nil {
		t.Fatalf("confirm the code-anchor fixture Release branch before Planning exit: %v", err)
	}
	anchorExists := false
	anchorReader := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path != "/internal/v1/proofs/code-anchor" || q.Get("repository") != "owner/repo" || q.Get("baseline") != strings.Repeat("a", 40) || q.Get("path") != "go-app/main.go" {
			t.Errorf("wrong anchor proof request: %s", r.URL.String())
			http.Error(w, "wrong", http.StatusBadRequest)
			return
		}
		if !anchorExists {
			http.Error(w, "missing", http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"company_id": "company-a", "project_id": "no-foundation", "repository": "owner/repo", "baseline": strings.Repeat("a", 40), "path": "go-app/main.go", "type": "file"})
	}))
	defer anchorReader.Close()
	store.proofReaderURL, store.proofReaderToken, store.proofHTTPClient = anchorReader.URL, "secret", anchorReader.Client()
	if _, err := store.TransitionTODO(ctx, "company-a", TODOActionRequest{ProjectID: "no-foundation", TODOID: builderBorn.ID, ExpectedRevision: &currentRevision, OperationID: "reject-missing-code-anchor", RequestedStage: "Implementation-Approval-Pending"}); err == nil || !strings.Contains(err.Error(), "assumptions[0].code_refs") {
		t.Fatalf("missing code anchor accepted: %v", err)
	}
	anchorExists = true
	if _, err := store.TransitionTODO(ctx, "company-a", TODOActionRequest{ProjectID: "no-foundation", TODOID: builderBorn.ID, ExpectedRevision: &currentRevision, OperationID: "reject-missing-review-subject", RequestedStage: "Implementation-Approval-Pending"}); err == nil || !strings.Contains(err.Error(), "proof_refs.planning_review_critique") {
		t.Fatalf("missing subject accepted: %v", err)
	}
	badRef := TODOProofReference{Kind: "planning_review_critique", Source: "human-review", Reference: newPlanning.Planning.Reviews[0].Reference, SourceDigest: sourceHash([]byte("old content")), VerificationState: "unverified"}
	if _, err := store.EditTODOEvidence(ctx, "company-a", TODOEvidenceEditRequest{ProjectID: "no-foundation", TODOID: builderBorn.ID, ExpectedRevision: &currentRevision, OperationID: "wrong-review-subject", ProofReferences: []TODOProofReference{badRef}, WaiverReferences: []TODOProofReference{}, ImplementationCommits: []TODOCommitReference{}, RecordSections: []TODOSection{}, Reason: "Exercise stale subject"}); err != nil {
		t.Fatal(err)
	}
	currentRevision++
	if _, err := store.TransitionTODO(ctx, "company-a", TODOActionRequest{ProjectID: "no-foundation", TODOID: builderBorn.ID, ExpectedRevision: &currentRevision, OperationID: "reject-wrong-review-subject", RequestedStage: "Implementation-Approval-Pending"}); err == nil || !strings.Contains(err.Error(), "proof_refs.planning_review_critique") {
		t.Fatalf("wrong digest subject accepted: %v", err)
	}
	currentRevision = recordPlanningReviewSubjects(t, ctx, store, "no-foundation", builderBorn.ID, "no-foundation-review-subject")
	withNoGo, err := store.ReadTODO(ctx, "company-a", "no-foundation", builderBorn.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	noGoRefs := append([]TODOProofReference{}, withNoGo.TODO.ProofReferences...)
	for i := range noGoRefs {
		if noGoRefs[i].Kind == "planning_review_critique" {
			noGoRefs[i].Outcome = "no-go"
		}
	}
	noGoEdit, err := store.EditTODOEvidence(ctx, "company-a", TODOEvidenceEditRequest{ProjectID: "no-foundation", TODOID: builderBorn.ID, ExpectedRevision: &currentRevision, OperationID: "no-go-planning-review", ProofReferences: noGoRefs, WaiverReferences: append([]TODOProofReference{}, withNoGo.TODO.WaiverReferences...), ImplementationCommits: append([]TODOCommitReference{}, withNoGo.TODO.ImplementationCommits...), RecordSections: recordRecordSections(withNoGo.TODO), Reason: "Exercise blocking review outcome"})
	if err != nil {
		t.Fatal(err)
	}
	currentRevision = noGoEdit.Revision
	if _, err := store.TransitionTODO(ctx, "company-a", TODOActionRequest{ProjectID: "no-foundation", TODOID: builderBorn.ID, ExpectedRevision: &currentRevision, OperationID: "reject-no-go-planning", RequestedStage: "Implementation-Approval-Pending"}); err == nil || !strings.Contains(err.Error(), "no-go") {
		t.Fatalf("no-go planning review did not block approval: %v", err)
	}
	currentRevision = recordPlanningReviewSubjects(t, ctx, store, "no-foundation", builderBorn.ID, "restore-go-planning-review")
	if _, err := store.TransitionTODO(ctx, "company-a", TODOActionRequest{ProjectID: "no-foundation", TODOID: builderBorn.ID, ExpectedRevision: &currentRevision, OperationID: "no-foundation-planning", RequestedStage: "Implementation-Approval-Pending"}); err != nil {
		t.Fatalf("Foundation-free planning: %v", err)
	}
	currentRevision++
	noFoundation, err := store.Project(ctx, "company-a", "no-foundation")
	if err != nil || noFoundation.Binding != nil {
		t.Fatalf("unexpected Foundation binding: %+v, %v", noFoundation, err)
	}
	assertTypedDelivery(t, ctx, store, "builder", migration.TODO.ID, 4, "r-08-rc", migrated.ContentDigest)
	noFoundationTODO, err := store.ReadTODO(ctx, "company-a", "no-foundation", builderBorn.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertTypedDelivery(t, ctx, store, "no-foundation", builderBorn.ID, currentRevision, "builder", noFoundationTODO.ContentDigest)
}

func recordPlanningReviewSubjects(t *testing.T, ctx context.Context, store *MongoStore, projectID, todoID, operation string) int64 {
	t.Helper()
	read, err := store.ReadTODO(ctx, "company-a", projectID, todoID, nil)
	if err != nil {
		t.Fatal(err)
	}
	refs := []TODOProofReference{}
	for _, ref := range read.TODO.ProofReferences {
		if !strings.HasPrefix(ref.Kind, "planning_review_") {
			refs = append(refs, ref)
		}
	}
	for _, review := range read.TODO.Content.Planning.Reviews {
		refs = append(refs, TODOProofReference{Kind: "planning_review_" + review.Kind, Source: "human-review", Reference: review.Reference, SourceDigest: read.ContentDigest, VerificationState: "unverified", Outcome: "go"})
	}
	result, err := store.EditTODOEvidence(ctx, "company-a", TODOEvidenceEditRequest{ProjectID: projectID, TODOID: todoID, ExpectedRevision: &read.TODORevision, OperationID: operation, ProofReferences: refs, WaiverReferences: append([]TODOProofReference{}, read.TODO.WaiverReferences...), ImplementationCommits: append([]TODOCommitReference{}, read.TODO.ImplementationCommits...), RecordSections: recordRecordSections(read.TODO), Reason: "Record fixture review subjects"})
	if err != nil {
		t.Fatalf("record planning review subjects: %v", err)
	}
	return result.Revision
}

func assertTypedDelivery(t *testing.T, ctx context.Context, store *MongoStore, projectID, todoID string, revision int64, branch, digest string) {
	t.Helper()
	commit := strings.Repeat("a", 40)
	root := strings.Repeat("d", 40)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if strings.HasSuffix(r.URL.Path, "/code-anchor") {
			_ = json.NewEncoder(w).Encode(map[string]string{"company_id": "company-a", "project_id": projectID, "repository": q.Get("repository"), "baseline": q.Get("baseline"), "path": q.Get("path"), "type": "file"})
			return
		}
		if q.Get("todo_authority") != "mongo-v2" {
			t.Errorf("typed proof omitted current Mongo authority: %s", r.URL.String())
		}
		if strings.HasSuffix(r.URL.Path, "/commit") {
			if q.Get("repository") != "owner/repo" || q.Get("branch") != branch {
				t.Errorf("wrong approved repository: %s", r.URL.String())
			}
			if testedHead := q.Get("tested_head_sha"); testedHead != "" && testedHead != commit {
				t.Errorf("implementation commit was compared against wrong tested head: %s", testedHead)
			}
			_ = json.NewEncoder(w).Encode(remoteCommitResult{CompanyID: "company-a", ProjectID: projectID, Repository: "owner/repo", Branch: branch, CommitSHA: commit, TestedHeadSHA: q.Get("tested_head_sha"), RemoteHead: commit, ParentSHA: strings.Repeat("b", 40), Paths: []string{"go-app/internal/registry/todo_access.go"}, Outcome: "verified"})
			return
		}
		if q.Get("docker_repository_id") != "owner/repo" {
			t.Errorf("production proof missed approved repository: %s", r.URL.String())
		}
		_, _ = fmt.Fprintf(w, `{"company_id":"company-a","project_id":%q,"todo_id":%q,"deployment_id":17,"environment":"production","root_sha":%q,"module_path":"project","commit_sha":%q,"project_commits":[%q],"outcome":"verified","project_mode":"mvp","reader_sha":%q}`, projectID, todoID, root, root, commit, strings.Repeat("e", 40))
	}))
	defer server.Close()
	store.proofReaderURL, store.proofReaderToken, store.proofHTTPClient = server.URL, "secret", server.Client()
	approval := TODOProofReference{Kind: "owner-approval", Source: "owner-chat", Reference: "APROVADO", SourceDigest: digest, VerificationState: "unverified"}
	if _, err := store.TransitionTODO(ctx, "company-a", TODOActionRequest{ProjectID: projectID, TODOID: todoID, ExpectedRevision: &revision, OperationID: "approve-delivery", RequestedStage: "Ready-for-Implementation", ProofReferences: []TODOProofReference{approval}}); err != nil {
		t.Fatalf("approve: %v", err)
	}
	revision++
	if _, err := store.TransitionTODO(ctx, "company-a", TODOActionRequest{ProjectID: projectID, TODOID: todoID, ExpectedRevision: &revision, OperationID: "start-delivery", RequestedStage: "In-Progress"}); err != nil {
		t.Fatalf("start: %v", err)
	}
	revision++
	claim := TODOCommitReference{Repository: "Builder", Commit: commit, SourceDigest: digest, VerificationState: "unverified"}
	review := TODOProofReference{Kind: "final_review", Source: "human-review", Reference: "review/final", SourceDigest: digest, VerificationState: "unverified", Outcome: "go"}
	criterionProof := TODOProofReference{Kind: "criterion:criterion-one", Source: "paced-local-runner", Reference: "G-TEST", SourceDigest: digest, VerificationState: "unverified"}
	scenarioProof := TODOProofReference{Kind: "scenario:scenario-one", Source: "paced-local-runner", Reference: "G-TEST", SourceDigest: digest, VerificationState: "unverified"}
	vetProof := TODOProofReference{Kind: "scenario:scenario-vet", Source: "paced-local-runner", Reference: "G-VET", SourceDigest: digest, VerificationState: "unverified"}
	imageProof := TODOProofReference{Kind: "scenario:scenario-image", Source: "paced-local-runner", Reference: "G-IMAGE", SourceDigest: digest, VerificationState: "unverified"}
	if _, err := store.EditTODOEvidence(ctx, "company-a", TODOEvidenceEditRequest{ProjectID: projectID, TODOID: todoID, ExpectedRevision: &revision, OperationID: "delivery-commit", ProofReferences: []TODOProofReference{review, criterionProof, scenarioProof, vetProof, imageProof}, WaiverReferences: []TODOProofReference{}, ImplementationCommits: []TODOCommitReference{claim}, RecordSections: []TODOSection{}, Reason: "Attribute the implementation"}); err != nil {
		t.Fatalf("evidence: %v", err)
	}
	revision++
	now := time.Now().UTC()
	preflight, err := store.ReadTODO(ctx, "company-a", projectID, todoID, nil)
	if err != nil {
		t.Fatal(err)
	}
	proofRevision := revision
	result := &LocalProofResult{SchemaVersion: "builder-local-proof-result-v3", CompanyID: "company-a", ProjectID: projectID, TODOID: todoID, TODORevision: &proofRevision, ContentDigest: digest, ReleaseID: preflight.TODO.ReleaseID, Position: preflight.TODO.Position, RunID: "11111111-2222-4333-8444-555555555555", StartedAt: now.Add(-time.Minute).Format(time.RFC3339Nano), FinishedAt: now.Format(time.RFC3339Nano)}
	branchPlan := map[string]string{}
	for _, branch := range preflight.ReleaseDeliveryBranches {
		branchPlan[branch.RepositoryID] = branch.Branch
	}
	for repositoryID, branch := range branchPlan {
		result.Repositories = append(result.Repositories, localProofRepository{RepositoryID: repositoryID, Branch: branch, HeadBefore: commit, HeadAfter: commit, InputsDigestBefore: digest, InputsDigestAfter: digest})
	}
	for _, check := range preflight.TODO.Content.LocalChecks {
		result.Checks = append(result.Checks, localProofCheck{ID: check.ID, RepositoryID: check.RepositoryID, ExitCode: 0})
	}
	seal := func() {
		bytes, _ := json.Marshal(result)
		var fields map[string]any
		_ = json.Unmarshal(bytes, &fields)
		delete(fields, "result_digest")
		bytes, _ = json.Marshal(fields)
		result.ResultDigest = sourceHash(bytes)
	}
	seal()
	badCriterionProof := criterionProof
	badCriterionProof.Source = "local-run"
	badProofRefs := []TODOProofReference{review, badCriterionProof, scenarioProof, vetProof, imageProof}
	if _, err := store.EditTODOEvidence(ctx, "company-a", TODOEvidenceEditRequest{ProjectID: projectID, TODOID: todoID, ExpectedRevision: &revision, OperationID: "bad-local-proof-source", ProofReferences: badProofRefs, WaiverReferences: []TODOProofReference{}, ImplementationCommits: []TODOCommitReference{claim}, RecordSections: []TODOSection{}, Reason: "Exercise local proof source guidance"}); err != nil {
		t.Fatalf("store bad-source evidence fixture: %v", err)
	}
	revision++
	proofRevision = revision
	result.TODORevision = &proofRevision
	result.RunID = "22222222-3333-4444-8555-666666666666"
	result.StartedAt = time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
	result.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
	seal()
	_, err = store.TransitionTODO(ctx, "company-a", TODOActionRequest{ProjectID: projectID, TODOID: todoID, ExpectedRevision: &revision, OperationID: "reject-local-proof-source", RequestedStage: "Local-Implemented", LocalProof: result})
	var sourceTeach *TODOValidationError
	if !errors.As(err, &sourceTeach) || sourceTeach.Field != "proof_refs.criterion:criterion-one.source" || !strings.Contains(sourceTeach.Expected, "paced-local-runner") {
		t.Fatalf("Mongo local transition source guidance = %#v (%v)", sourceTeach, err)
	}
	unchanged, err := store.ReadTODO(ctx, "company-a", projectID, todoID, nil)
	if err != nil || unchanged.TODO.Stage != "In-Progress" || unchanged.TODORevision != revision || unchanged.TODO.ProofReferences[1] != badCriterionProof {
		t.Fatalf("failed source check mutated Mongo transition state: %+v, %v", unchanged, err)
	}
	if _, err := store.EditTODOEvidence(ctx, "company-a", TODOEvidenceEditRequest{ProjectID: projectID, TODOID: todoID, ExpectedRevision: &revision, OperationID: "restore-local-proof-source", ProofReferences: []TODOProofReference{review, criterionProof, scenarioProof, vetProof, imageProof}, WaiverReferences: []TODOProofReference{}, ImplementationCommits: []TODOCommitReference{claim}, RecordSections: []TODOSection{}, Reason: "Restore exact PACED source reference"}); err != nil {
		t.Fatalf("restore valid evidence fixture: %v", err)
	}
	revision++
	proofRevision = revision
	result.TODORevision = &proofRevision
	result.RunID = "33333333-4444-4555-8666-777777777777"
	result.StartedAt = time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
	result.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
	seal()
	if _, err := store.EditTODONextStep(ctx, "company-a", TODONextStepEditRequest{ProjectID: projectID, TODOID: todoID, ExpectedRevision: &revision, OperationID: "next-step-during-proof", NextExactStep: "Request the verified local transition.", Reason: "Update the operational next step"}); err != nil {
		t.Fatalf("next-step edit: %v", err)
	}
	revision++
	stale := revision - 1
	if _, err := store.TransitionTODO(ctx, "company-a", TODOActionRequest{ProjectID: projectID, TODOID: todoID, ExpectedRevision: &stale, OperationID: "stale-local-delivery", RequestedStage: "Local-Implemented", LocalProof: result}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale local revision accepted: %v", err)
	}
	localRequest := TODOActionRequest{ProjectID: projectID, TODOID: todoID, ExpectedRevision: &revision, OperationID: "local-delivery", RequestedStage: "Local-Implemented", LocalProof: result}
	localCompetitor := localRequest
	if projectID == "builder" {
		localCompetitor.OperationID = "competing-local-delivery"
	}
	assertConcurrentStageTransition(t, ctx, store, projectID, todoID, revision, localRequest, localCompetitor)
	revision++
	beforeMove, err := store.ReadTODO(ctx, "company-a", projectID, todoID, nil)
	if err != nil {
		t.Fatal(err)
	}
	changedClaim := claim
	changedClaim.Commit = strings.Repeat("c", 40)
	if _, err := store.EditTODOEvidence(ctx, "company-a", TODOEvidenceEditRequest{ProjectID: projectID, TODOID: todoID, ExpectedRevision: &revision, OperationID: "reject-local-commit-change", ProofReferences: []TODOProofReference{}, WaiverReferences: []TODOProofReference{}, ImplementationCommits: []TODOCommitReference{changedClaim}, RecordSections: []TODOSection{}, Reason: "Reject changed commit after local delivery"}); err == nil || !strings.Contains(err.Error(), "implementation_commits") {
		t.Fatalf("local implementation commit changed or wrong TEACH: %v", err)
	}
	afterEvidence, err := store.ReadTODO(ctx, "company-a", projectID, todoID, nil)
	if err != nil || afterEvidence.TODORevision != revision || len(afterEvidence.TODO.ImplementationCommits) != 1 || afterEvidence.TODO.ImplementationCommits[0] != claim {
		t.Fatalf("rejected commit change mutated evidence: %+v, %v", afterEvidence, err)
	}
	deployment := TODOProofReference{Kind: "mvp-deployment-claim", Source: "github-deployment", Reference: "17@" + root, SourceDigest: digest, VerificationState: "unverified"}
	legacyLocal := beforeMove.TODO
	legacyProof := TODOProofReference{Kind: "local-run-accepted", Source: "historical", Reference: "old", SourceDigest: digest, VerificationState: "verified"}
	legacyLocal.VerifiedTransitionRef = &legacyProof
	if _, err := store.verifyMVPProduction(ctx, "company-a", legacyLocal, TODOActionRequest{ProofReferences: []TODOProofReference{deployment}}, ""); err == nil || !strings.Contains(err.Error(), "local_proof") {
		t.Fatalf("v2 completion accepted a historical local proof: %v", err)
	}
	staleLocal := beforeMove.TODO
	staleLocal.ImplementationCommits = append([]TODOCommitReference{}, beforeMove.TODO.ImplementationCommits...)
	staleLocal.ImplementationCommits[0].SourceDigest = sourceHash([]byte("older content"))
	if _, err := store.verifyMVPProduction(ctx, "company-a", staleLocal, TODOActionRequest{ProofReferences: []TODOProofReference{deployment}}, ""); err == nil || !strings.Contains(err.Error(), "implementation_commits.source_digest") {
		t.Fatalf("v2 completion accepted commits from an older digest: %v", err)
	}
	project, err := store.Project(ctx, "company-a", projectID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.EditTODOPlacement(ctx, "company-a", TODOPlacementEditRequest{ProjectID: projectID, TODOID: todoID, ExpectedRevision: &revision, ExpectedRoadmapRevision: &project.RoadmapRevision, OperationID: "change-local-placement", ReleaseID: beforeMove.TODO.ReleaseID, Position: *beforeMove.TODO.Position + 1, Reason: "Reopen review for changed placement"}); err != nil {
		t.Fatalf("change local placement: %v", err)
	}
	revision++
	afterMove, err := store.ReadTODO(ctx, "company-a", projectID, todoID, nil)
	if err != nil || afterMove.TODORevision != revision || afterMove.TODO.Stage != "Planning" || *afterMove.TODO.Position != *beforeMove.TODO.Position+1 {
		t.Fatalf("changed placement did not reopen review: %+v, %v", afterMove, err)
	}
	for _, ref := range afterMove.TODO.ProofReferences {
		if ref.Source == "human-review" {
			t.Fatal("placement change retained an active review citation")
		}
	}
	if _, err := store.TransitionTODO(ctx, "company-a", TODOActionRequest{ProjectID: projectID, TODOID: todoID, ExpectedRevision: &revision, OperationID: "reject-prior-local-proof", RequestedStage: "Completed", ProofReferences: []TODOProofReference{deployment}}); err == nil {
		t.Fatal("prior local proof authorized a different placement")
	}
	project, err = store.Project(ctx, "company-a", projectID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.EditTODOPlacement(ctx, "company-a", TODOPlacementEditRequest{ProjectID: projectID, TODOID: todoID, ExpectedRevision: &revision, ExpectedRoadmapRevision: &project.RoadmapRevision, OperationID: "restore-local-placement", ReleaseID: beforeMove.TODO.ReleaseID, Position: *beforeMove.TODO.Position, Reason: "Confirm original placement still requires fresh proof"}); err != nil {
		t.Fatalf("restore placement: %v", err)
	}
	revision++
	afterMove, err = store.ReadTODO(ctx, "company-a", projectID, todoID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.TransitionTODO(ctx, "company-a", TODOActionRequest{ProjectID: projectID, TODOID: todoID, ExpectedRevision: &revision, OperationID: "reject-stale-placement-review", RequestedStage: "Implementation-Approval-Pending"}); err == nil || !strings.Contains(err.Error(), "planning.reviews") {
		t.Fatalf("unchanged review accepted: %v", err)
	}
	updated := contentFromTODO(afterMove.TODO)
	updated.Context += " Reviewed after placement."
	unchangedReview, err := store.EditTODOContent(ctx, "company-a", TODOContentEditRequest{ProjectID: projectID, TODOID: todoID, ExpectedRevision: &revision, OperationID: "placement-with-old-review", Content: updated, Reason: "Check the prior review cannot be reused"})
	if err != nil {
		t.Fatalf("edit without fresh review: %v", err)
	}
	revision = unchangedReview.Revision
	if _, err := store.TransitionTODO(ctx, "company-a", TODOActionRequest{ProjectID: projectID, TODOID: todoID, ExpectedRevision: &revision, OperationID: "reject-reused-review", RequestedStage: "Implementation-Approval-Pending"}); err == nil || !strings.Contains(err.Error(), "planning.reviews.critique") {
		t.Fatalf("reused review accepted: %v", err)
	}
	updated.Planning.Reviews[0].Reference = "review/critique-new-placement"
	for i := 1; i < len(updated.Planning.Reviews); i++ {
		updated.Planning.Reviews[i].Reference += "-new-placement"
	}
	edit, err := store.EditTODOContent(ctx, "company-a", TODOContentEditRequest{ProjectID: projectID, TODOID: todoID, ExpectedRevision: &revision, OperationID: "fresh-placement-review", Content: updated, Reason: "Review the changed placement"})
	if err != nil {
		t.Fatalf("refresh review: %v", err)
	}
	revision = edit.Revision
	digest = edit.ContentDigest
	approval.SourceDigest, claim.SourceDigest, review.SourceDigest, deployment.SourceDigest = digest, digest, digest, digest
	criterionProof.SourceDigest, scenarioProof.SourceDigest = digest, digest
	vetProof.SourceDigest, imageProof.SourceDigest = digest, digest
	result.ContentDigest = digest
	placementProofs := append([]TODOProofReference{}, afterMove.TODO.ProofReferences...)
	placementProofs = append(placementProofs, review, criterionProof, scenarioProof, vetProof, imageProof)
	if _, err := store.EditTODOEvidence(ctx, "company-a", TODOEvidenceEditRequest{ProjectID: projectID, TODOID: todoID, ExpectedRevision: &revision, OperationID: "rebind-placement-commit", ProofReferences: placementProofs, WaiverReferences: []TODOProofReference{}, ImplementationCommits: []TODOCommitReference{claim}, RecordSections: []TODOSection{}, Reason: "Bind implementation to fresh review"}); err != nil {
		t.Fatalf("rebind evidence: %v", err)
	}
	revision++
	revision = recordPlanningReviewSubjects(t, ctx, store, projectID, todoID, "repeat-planning-review-subject")
	for _, stage := range []string{"Implementation-Approval-Pending", "Ready-for-Implementation", "In-Progress", "Local-Implemented"} {
		transition := TODOActionRequest{ProjectID: projectID, TODOID: todoID, ExpectedRevision: &revision, OperationID: "repeat-" + strings.ToLower(stage), RequestedStage: stage}
		if stage == "Ready-for-Implementation" {
			transition.ProofReferences = []TODOProofReference{approval}
		}
		if stage == "Local-Implemented" {
			transition.LocalProof = result
			if _, err := store.TransitionTODO(ctx, "company-a", transition); err == nil || !strings.Contains(err.Error(), "local_proof.todo_revision") {
				t.Fatalf("replayed local proof accepted after placement change: %v", err)
			}
			proofRevision = revision
			result.Position = afterMove.TODO.Position
			result.RunID = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
			seal()
			transition.OperationID = "repeat-local-fresh"
			transition.LocalProof = result
		}
		if _, err := store.TransitionTODO(ctx, "company-a", transition); err != nil {
			t.Fatalf("revalidate %s: %v", stage, err)
		}
		revision++
	}
	wrongDeployment := deployment
	wrongDeployment.SourceDigest = sourceHash([]byte("wrong contract"))
	if _, err := store.TransitionTODO(ctx, "company-a", TODOActionRequest{ProjectID: projectID, TODOID: todoID, ExpectedRevision: &revision, OperationID: "wrong-production-digest", RequestedStage: "Completed", ProofReferences: []TODOProofReference{wrongDeployment}}); err == nil || !strings.Contains(err.Error(), "proof_refs") {
		t.Fatalf("stale production digest accepted or wrong TEACH: %v", err)
	}
	completedRequest := TODOActionRequest{ProjectID: projectID, TODOID: todoID, ExpectedRevision: &revision, OperationID: "production-delivery", RequestedStage: "Completed", ProofReferences: []TODOProofReference{deployment}}
	completedCompetitor := completedRequest
	if projectID == "no-foundation" {
		completedCompetitor.OperationID = "competing-production-delivery"
	}
	assertConcurrentStageTransition(t, ctx, store, projectID, todoID, revision, completedRequest, completedCompetitor)
}

func assertConcurrentStageTransition(t *testing.T, ctx context.Context, store *MongoStore, projectID, todoID string, revision int64, first, second TODOActionRequest) {
	t.Helper()
	type outcome struct {
		operationID string
		err         error
	}
	start := make(chan struct{})
	results := make(chan outcome, 2)
	for _, request := range []TODOActionRequest{first, second} {
		go func(request TODOActionRequest) {
			<-start
			_, err := store.TransitionTODO(ctx, "company-a", request)
			results <- outcome{operationID: request.OperationID, err: err}
		}(request)
	}
	close(start)
	var winningOperation string
	successes, conflicts := 0, 0
	var conflictErrors []string
	for range 2 {
		result := <-results
		switch {
		case result.err == nil:
			successes++
			winningOperation = result.operationID
		case errors.Is(result.err, ErrConflict):
			conflicts++
			conflictErrors = append(conflictErrors, result.operationID+": "+result.err.Error())
		default:
			t.Fatalf("concurrent %s transition: %v", first.RequestedStage, result.err)
		}
	}
	if first.OperationID == second.OperationID {
		if successes < 1 || successes+conflicts != 2 {
			t.Fatalf("same-key %s outcomes: successes=%d conflicts=%d errors=%v", first.RequestedStage, successes, conflicts, conflictErrors)
		}
	} else if successes != 1 || conflicts != 1 {
		t.Fatalf("competing-key %s outcomes: successes=%d conflicts=%d", first.RequestedStage, successes, conflicts)
	}
	current, err := store.ReadTODO(ctx, "company-a", projectID, todoID, nil)
	if err != nil || current.TODORevision != revision+1 || current.TODO.Stage != first.RequestedStage || current.TODO.StageVerification != "verified" {
		t.Fatalf("concurrent %s left wrong aggregate: %+v, %v", first.RequestedStage, current, err)
	}
	company, err := store.company(ctx, "company-a")
	if err != nil {
		t.Fatal(err)
	}
	retry := first
	if winningOperation == second.OperationID {
		retry = second
	}
	if _, err := store.TransitionTODO(ctx, "company-a", retry); err != nil {
		t.Fatalf("exact %s retry: %v", first.RequestedStage, err)
	}
	if first.OperationID != second.OperationID {
		loser := second
		if winningOperation == second.OperationID {
			loser = first
		}
		if _, err := store.TransitionTODO(ctx, "company-a", loser); !errors.Is(err, ErrConflict) {
			t.Fatalf("losing %s key did not conflict: %v", first.RequestedStage, err)
		}
	}
	eventsAfterRetry, err := store.client.Database(company.DatabaseName).Collection(todoEventsCollection).CountDocuments(ctx, bson.D{
		{Key: "project_id", Value: projectID}, {Key: "todo_id", Value: todoID},
		{Key: "operation_id", Value: bson.D{{Key: "$in", Value: bson.A{first.OperationID, second.OperationID}}}},
	})
	if err != nil || eventsAfterRetry != 1 {
		t.Fatalf("retry of %s wrote %d events: %v", first.RequestedStage, eventsAfterRetry, err)
	}
}

func TestMongoIndividualTODOAdmissionReadRoadmapAndGovernedStart(t *testing.T) {
	uri := os.Getenv("REGISTRY_TEST_MONGODB_URI")
	if uri == "" {
		if os.Getenv("REGISTRY_REQUIRE_MONGO_INTEGRATION") == "1" {
			t.Fatal("required Mongo replica-set integration fixture is unavailable: set REGISTRY_TEST_MONGODB_URI")
		}
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

	admitImportedTODO := func(id string, position int) TODORecord {
		t.Helper()
		admission := genericTODOAdmissionFixture()
		admission.TODO.ID = id
		admission.TODO.Position = intPointer(position)
		admission.TODO.Source.Path = "todos/active/TODO-" + id + ".md"
		admission.TODO.Source.Digest = sourceHash([]byte(id))
		if _, err := store.AdmitTODO(ctx, "company-a", "builder", admission); err != nil {
			t.Fatalf("admit imported TODO %q: %v", id, err)
		}
		return admission.TODO
	}

	staleApprovalTODO := admitImportedTODO("builder-mongo-imported-stale-approval", 4)
	staleStartProof := TODOProofReference{Kind: "owner-approval", Source: "owner-chat", Reference: "APROVADO", SourceDigest: staleApprovalTODO.Source.Digest, VerificationState: "unverified"}
	staleStart, err := store.TransitionTODO(ctx, "company-a", TODOActionRequest{
		ProjectID: "builder", TODOID: staleApprovalTODO.ID, ExpectedRevision: int64Pointer(0), OperationID: "imported-legacy-start", RequestedStage: "In-Progress",
		ProofReferences: []TODOProofReference{staleStartProof}, WaiverReferences: []TODOProofReference{},
	})
	if err == nil || !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "source/content_revision") {
		t.Fatalf("unmodified imported TODO legacy start = %+v, %v; want v1 authority TEACH", staleStart, err)
	}
	staleContent, err := store.ReadTODO(ctx, "company-a", "builder", staleApprovalTODO.ID, nil)
	if err != nil || staleContent.TODO.Stage != "Planning" || staleContent.TODO.Revision != 0 {
		t.Fatal(err)
	}
	staleContentEdit := contentFromTODO(staleContent.TODO)
	staleContentEdit.Title += " authored edit"
	staleEdit, err := store.EditTODOContent(ctx, "company-a", TODOContentEditRequest{
		ProjectID: "builder", TODOID: staleApprovalTODO.ID, ExpectedRevision: int64Pointer(0), OperationID: "imported-stale-content-edit",
		Content: staleContentEdit, Reason: "Exercise imported content approval invalidation",
	})
	if err != nil || staleEdit.Revision != 1 || staleEdit.ContentRevision == nil || *staleEdit.ContentRevision != 1 || !validSourceDigest(staleEdit.ContentDigest) {
		t.Fatalf("edited imported TODO content subject = %+v, %v", staleEdit, err)
	}
	staleContent, err = store.ReadTODO(ctx, "company-a", "builder", staleApprovalTODO.ID, nil)
	if err != nil || staleContent.ContentRevision == nil || *staleContent.ContentRevision != 1 || staleContent.ContentDigest != staleEdit.ContentDigest || staleContent.TODO.StageVerification != "unverified" || staleContent.TODO.VerifiedTransitionRef != nil || staleContent.TODO.Source.Digest != staleApprovalTODO.Source.Digest {
		t.Fatalf("imported edit retained stale approval or source provenance: %+v, %v", staleContent, err)
	}
	if _, err := store.TransitionTODO(ctx, "company-a", TODOActionRequest{
		ProjectID: "builder", TODOID: staleApprovalTODO.ID, ExpectedRevision: int64Pointer(1), OperationID: "imported-stale-reapproval",
		RequestedStage: "In-Progress", ProofReferences: []TODOProofReference{staleStartProof}, WaiverReferences: []TODOProofReference{},
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale Foundation-digest approval was accepted after an authored edit: %v", err)
	}

	editedFlowTODO := admitImportedTODO("builder-mongo-imported-edited-flow", 5)
	flowRead, err := store.ReadTODO(ctx, "company-a", "builder", editedFlowTODO.ID, nil)
	if err != nil || flowRead.ContentRevision == nil || *flowRead.ContentRevision != 0 || !validSourceDigest(flowRead.ContentDigest) || flowRead.TODO.Content != nil || flowRead.TODO.Source.Foundation != "builder" {
		t.Fatalf("unmodified imported TODO read = %+v, %v", flowRead, err)
	}
	flowContent := contentFromTODO(flowRead.TODO)
	flowContent.Title += " authored edit"
	flowEdit, err := store.EditTODOContent(ctx, "company-a", TODOContentEditRequest{
		ProjectID: "builder", TODOID: editedFlowTODO.ID, ExpectedRevision: int64Pointer(0), OperationID: "imported-flow-content-edit",
		Content: flowContent, Reason: "Use the edited imported content as the stage proof subject",
	})
	if err != nil || flowEdit.Revision != 1 || flowEdit.ContentRevision == nil || *flowEdit.ContentRevision != 1 || !validSourceDigest(flowEdit.ContentDigest) {
		t.Fatalf("edited imported TODO = %+v, %v", flowEdit, err)
	}
	flowRead, err = store.ReadTODO(ctx, "company-a", "builder", editedFlowTODO.ID, nil)
	if err != nil || flowRead.ContentRevision == nil || *flowRead.ContentRevision != 1 || flowRead.ContentDigest != flowEdit.ContentDigest || flowRead.TODO.Source.Foundation != "builder" || flowRead.TODO.Source.Digest != editedFlowTODO.Source.Digest {
		t.Fatalf("edited imported readback lost current content subject or Foundation provenance: %+v, %v", flowRead, err)
	}
	oldSourceProof := TODOProofReference{Kind: "owner-approval", Source: "owner-chat", Reference: "APROVADO", SourceDigest: editedFlowTODO.Source.Digest, VerificationState: "unverified"}
	if _, err := store.TransitionTODO(ctx, "company-a", TODOActionRequest{
		ProjectID: "builder", TODOID: editedFlowTODO.ID, ExpectedRevision: int64Pointer(1), OperationID: "imported-flow-old-shortcut",
		RequestedStage: "In-Progress", ProofReferences: []TODOProofReference{oldSourceProof}, WaiverReferences: []TODOProofReference{},
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("edited imported TODO accepted the legacy Planning-to-In-Progress shortcut: %v", err)
	}
	contentRef := "builder/" + editedFlowTODO.ID + "/content/1"
	staleContentProof := TODOProofReference{Kind: "owner-approval", Source: "mongo-content-revision", Reference: contentRef, SourceDigest: editedFlowTODO.Source.Digest, VerificationState: "unverified"}
	if _, err := store.TransitionTODO(ctx, "company-a", TODOActionRequest{
		ProjectID: "builder", TODOID: editedFlowTODO.ID, ExpectedRevision: int64Pointer(1), OperationID: "imported-flow-stale-content-digest",
		RequestedStage: "Implementation-Approval-Pending", ProofReferences: []TODOProofReference{staleContentProof}, WaiverReferences: []TODOProofReference{},
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("edited imported TODO accepted an owner approval bound to Foundation provenance: %v", err)
	}
	currentContentProof := staleContentProof
	currentContentProof.SourceDigest = flowEdit.ContentDigest
	startedForApproval, err := store.TransitionTODO(ctx, "company-a", TODOActionRequest{
		ProjectID: "builder", TODOID: editedFlowTODO.ID, ExpectedRevision: int64Pointer(1), OperationID: "imported-flow-owner-approval",
		RequestedStage: "Implementation-Approval-Pending", ProofReferences: []TODOProofReference{currentContentProof}, WaiverReferences: []TODOProofReference{},
	})
	if err == nil || !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "source/content_revision") {
		t.Fatalf("edited v1 imported current-content approval = %+v, %v; want v1 authority TEACH", startedForApproval, err)
	}
	flowRead, err = store.ReadTODO(ctx, "company-a", "builder", editedFlowTODO.ID, nil)
	if err != nil || flowRead.TODO.Stage != "Planning" || flowRead.TODO.Revision != 1 || flowRead.TODO.StageVerification != "unverified" || flowRead.TODO.VerifiedTransitionRef != nil || flowRead.ContentDigest != flowEdit.ContentDigest {
		t.Fatalf("refused v1 approval changed imported state: %+v, %v", flowRead, err)
	}

	// The Project-scoped template and POST admit a Mongo-born TODO directly to
	// Planning Backlog, without Foundation source metadata or a placeholder Release.
	creationTemplate, err := store.TODOCreationTemplate(ctx, "company-a", "builder")
	if err != nil || creationTemplate.SchemaVersion != todoCreationSchemaVersion {
		t.Fatalf("Mongo Project creation template = %+v, %v", creationTemplate, err)
	}
	mongoBorn := mongoBornTODOFixture()
	completeMongoBornContent := validTODOContent()
	completeMongoBornContent.Planning = &TODOPlanning{Complexity: "small", AuditTriggers: testAuditTriggers(), Assumptions: []TODOPlanningAssumption{}, Reviews: []TODOPlanningReview{{Kind: "critique", Reference: "review/critique"}}}
	mongoBorn.Content = &completeMongoBornContent
	setTODOValidator(TODOCollectionValidatorBeforeCreation())
	if err := database.RunCommand(ctx, bson.D{{Key: "collMod", Value: todoContentVersionsCollection}, {Key: "validator", Value: historyValidatorBeforeContentDigest(TODOContentVersionsValidator())}, {Key: "validationLevel", Value: "strict"}, {Key: "validationAction", Value: "error"}}).Err(); err != nil {
		t.Fatalf("set pre-creation content-version validator: %v", err)
	}
	if err := database.RunCommand(ctx, bson.D{{Key: "collMod", Value: todoEventsCollection}, {Key: "validator", Value: historyValidatorBeforeClearPlacement(TODOEventsValidator())}, {Key: "validationLevel", Value: "strict"}, {Key: "validationAction", Value: "error"}}).Err(); err != nil {
		t.Fatalf("set pre-creation event validator: %v", err)
	}
	mongoAdmission, err := store.AdmitTODO(ctx, "company-a", "builder", TODOAdmissionRequest{SchemaVersion: creationTemplate.SchemaVersion, OperationID: "mongo-born-backlog-create", TODO: mongoBorn})
	if err != nil || mongoAdmission.TODOID != mongoBorn.ID || mongoAdmission.ReleaseID != "" || mongoAdmission.ContentRevision != 0 || !validSourceDigest(mongoAdmission.ContentDigest) {
		t.Fatalf("Mongo-born Backlog admission = %+v, %v", mongoAdmission, err)
	}
	if err := verifyTODOCollection(ctx, database); err != nil {
		t.Fatalf("Mongo-born admission did not upgrade the recognized pre-creation validator: %v", err)
	}
	mongoCreationEvent, err := readHistoryBaseline(ctx, database, "builder", mongoBorn.ID)
	if err != nil || mongoCreationEvent.Changes.VerifiedTransitionRef == nil || mongoCreationEvent.Changes.VerifiedTransitionRef.Kind != "created" || mongoCreationEvent.Changes.VerifiedTransitionRef.SourceDigest != mongoAdmission.ContentDigest {
		t.Fatalf("Mongo-born Planning baseline lacks its Mongo creation proof: %+v, %v", mongoCreationEvent, err)
	}
	mongoRead, err := store.ReadTODO(ctx, "company-a", "builder", mongoBorn.ID, nil)
	if err != nil || mongoRead.TODO.Stage != "Planning" || mongoRead.TODO.StageVerification != "verified" || mongoRead.TODO.Source.Kind != "mongo-content-revision" || mongoRead.TODO.Source.Path != "" || mongoRead.TODO.ReleaseID != "" || mongoRead.TODO.Position != nil || mongoRead.ContentRevision == nil || *mongoRead.ContentRevision != 0 || mongoRead.ContentDigest != mongoAdmission.ContentDigest || mongoRead.TODO.Content == nil || mongoRead.TODO.Content.Context != completeMongoBornContent.Context {
		t.Fatalf("Mongo-born Planning detail = %+v, %v", mongoRead, err)
	}
	var backlogRoadmap struct {
		Backlog struct {
			TODOs []roadmapTODOView `json:"todos"`
		} `json:"backlog"`
		Epics []roadmapEpicView `json:"epics"`
	}
	backlogBytes, err := store.Roadmap(ctx, "builder", "company-a")
	if err == nil {
		err = json.Unmarshal(backlogBytes, &backlogRoadmap)
	}
	mongoBornInRelease := false
	if err == nil && len(backlogRoadmap.Epics) > 0 && len(backlogRoadmap.Epics[0].Releases) > 0 {
		for _, releaseTODO := range backlogRoadmap.Epics[0].Releases[0].TODOs {
			mongoBornInRelease = mongoBornInRelease || releaseTODO.ID == mongoBorn.ID
		}
	}
	if err != nil || len(backlogRoadmap.Backlog.TODOs) != 1 || backlogRoadmap.Backlog.TODOs[0].ID != mongoBorn.ID || mongoBornInRelease {
		t.Fatalf("Mongo-born TODO was not projected to Backlog: %s, %v", backlogBytes, err)
	}
	// A second registered Project, including Bóora's public Project ID, uses the
	// same Mongo creation route without a per-Project authority switch.
	booraProject := Project{ID: "boora", CompanyID: "company-a", Name: "Bóora", Binding: ptrTestBinding(validTestBinding()), RegisteredAt: time.Now().UTC().Format(time.RFC3339Nano), PreparationState: "not_ready"}
	booraDocument, err := projectToDocument(booraProject, bson.NewObjectID())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Collection(projectsCollection).InsertOne(ctx, booraDocument); err != nil {
		t.Fatalf("register Bóora fixture Project: %v", err)
	}
	booraTemplate, err := store.TODOCreationTemplate(ctx, "company-a", "boora")
	booraTODO := mongoBornTODOFixture()
	booraTODO.ID, booraTODO.ProjectID = "boora-mongo-created-backlog", "boora"
	booraAdmission, admitErr := store.AdmitTODO(ctx, "company-a", "boora", TODOAdmissionRequest{SchemaVersion: booraTemplate.SchemaVersion, OperationID: "boora-backlog-create", TODO: booraTODO})
	var booraRoadmap struct {
		Backlog struct {
			TODOs []roadmapTODOView `json:"todos"`
		} `json:"backlog"`
	}
	booraBytes, booraRoadmapErr := store.Roadmap(ctx, "boora", "company-a")
	if booraRoadmapErr == nil {
		booraRoadmapErr = json.Unmarshal(booraBytes, &booraRoadmap)
	}
	if err != nil || booraTemplate.SchemaVersion != creationTemplate.SchemaVersion || admitErr != nil || booraAdmission.TODOID != booraTODO.ID || booraAdmission.ReleaseID != "" || booraRoadmapErr != nil || len(booraRoadmap.Backlog.TODOs) != 1 || booraRoadmap.Backlog.TODOs[0].ID != booraTODO.ID {
		t.Fatalf("Bóora did not use the common API template/create/Backlog route: template=%+v admission=%+v roadmap=%s errors=(%v,%v,%v)", booraTemplate, booraAdmission, booraBytes, err, admitErr, booraRoadmapErr)
	}
	zeroMongoRevision := int64(0)
	if _, err := store.TransitionTODO(ctx, "company-a", TODOActionRequest{ProjectID: "builder", TODOID: mongoBorn.ID, ExpectedRevision: &zeroMongoRevision, OperationID: "backlog-transition-001", RequestedStage: "Implementation-Approval-Pending", ProofReferences: []TODOProofReference{}, WaiverReferences: []TODOProofReference{}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("unassigned Mongo-born TODO advanced from Planning: %v", err)
	}
	projectBeforePlacement, err := store.Project(ctx, "company-a", "builder")
	if err != nil {
		t.Fatal(err)
	}
	roadmapRevision := projectBeforePlacement.RoadmapRevision
	attach := TODOPlacementEditRequest{ProjectID: "builder", TODOID: mongoBorn.ID, ExpectedRevision: &zeroMongoRevision, ExpectedRoadmapRevision: &roadmapRevision, OperationID: "mongo-attach-001", Action: "attach", ReleaseID: "r-08", Position: 4, Reason: "Attach the TODO to a user-selected same-Project Release"}
	attached, err := store.EditTODOPlacement(ctx, "company-a", attach)
	if err != nil || attached.Revision != 1 {
		t.Fatalf("attach Mongo-born TODO = %+v, %v", attached, err)
	}
	oneMongoRevision := int64(1)
	roadmapRevision++
	detached, err := store.EditTODOPlacement(ctx, "company-a", TODOPlacementEditRequest{ProjectID: "builder", TODOID: mongoBorn.ID, ExpectedRevision: &oneMongoRevision, ExpectedRoadmapRevision: &roadmapRevision, OperationID: "mongo-detach-001", Action: "detach", Reason: "Return the TODO to Backlog"})
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
	roadmapRevision++
	attach.ExpectedRoadmapRevision = &roadmapRevision
	if _, err := store.EditTODOPlacement(ctx, "company-a", attach); err != nil {
		t.Fatalf("reattach Mongo-born TODO: %v", err)
	}
	placementReviewed, err := store.ReadTODO(ctx, "company-a", "builder", mongoBorn.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	placementContent := contentFromTODO(placementReviewed.TODO)
	placementContent.Planning.Reviews[0].Reference = "review/critique-after-reattach"
	if _, err := store.EditTODOContent(ctx, "company-a", TODOContentEditRequest{ProjectID: "builder", TODOID: mongoBorn.ID, ExpectedRevision: int64Pointer(3), OperationID: "mongo-placement-reviewed", Content: placementContent, Reason: "Refresh review after placement changes"}); err != nil {
		t.Fatalf("refresh placement review: %v", err)
	}
	recordPlanningReviewSubjects(t, ctx, store, "builder", mongoBorn.ID, "mongo-placement-review-subject")
	threeMongoRevision := int64(5)
	wrongProof := TODOProofReference{Kind: "owner-approval", Source: "mongo-content-revision", Reference: "builder/" + mongoBorn.ID + "/content/0", SourceDigest: "sha256:" + strings.Repeat("0", 64), VerificationState: "unverified"}
	wrongStart := TODOActionRequest{ProjectID: "builder", TODOID: mongoBorn.ID, ExpectedRevision: &threeMongoRevision, OperationID: "mongo-start-wrong-digest", RequestedStage: "Implementation-Approval-Pending", ProofReferences: []TODOProofReference{wrongProof}, WaiverReferences: []TODOProofReference{}}
	if _, err := store.TransitionTODO(ctx, "company-a", wrongStart); !errors.Is(err, ErrConflict) {
		t.Fatalf("pre-approval edge accepted owner approval: %v", err)
	}
	correctStart := wrongStart
	correctStart.OperationID = "mongo-start-001"
	correctStart.ProofReferences = []TODOProofReference{}
	startedMongo, err := store.TransitionTODO(ctx, "company-a", correctStart)
	if err != nil || startedMongo.Stage != "Implementation-Approval-Pending" || startedMongo.TODORevision != 6 {
		t.Fatalf("governed Mongo-born first lifecycle edge = %+v, %v", startedMongo, err)
	}
	currentMongo, err := store.ReadTODOAsOf(ctx, "company-a", "builder", mongoBorn.ID, 6)
	if err != nil || currentMongo.State.Stage != "Implementation-Approval-Pending" || currentMongo.State.PlacementAssigned != true {
		t.Fatalf("Mongo-born first edge history = %+v, %v", currentMongo, err)
	}
	backlogBytes, err = store.Roadmap(ctx, "builder", "company-a")
	if err == nil {
		err = json.Unmarshal(backlogBytes, &backlogRoadmap)
	}
	mongoBornReleaseCount := 0
	if err == nil && len(backlogRoadmap.Epics) > 0 && len(backlogRoadmap.Epics[0].Releases) > 0 {
		for _, releaseTODO := range backlogRoadmap.Epics[0].Releases[0].TODOs {
			if releaseTODO.ID == mongoBorn.ID {
				mongoBornReleaseCount++
			}
		}
	}
	if err != nil || len(backlogRoadmap.Backlog.TODOs) != 0 || mongoBornReleaseCount != 1 {
		t.Fatalf("assigned TODO duplicated or missing in Backlog/Release projection: %s, %v", backlogBytes, err)
	}
	currentMongoRead, err := store.ReadTODO(ctx, "company-a", "builder", mongoBorn.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	unchangedContent := contentFromTODO(currentMongoRead.TODO)
	noOpEdit, err := store.EditTODOContent(ctx, "company-a", TODOContentEditRequest{ProjectID: "builder", TODOID: mongoBorn.ID, ExpectedRevision: int64Pointer(6), OperationID: "mongo-content-no-op-001", Content: unchangedContent, Reason: "Keep the reviewed plan unchanged"})
	if err != nil || !noOpEdit.NoOp || noOpEdit.Revision != 6 {
		t.Fatalf("unchanged approval-pending content changed the TODO: %+v, %v", noOpEdit, err)
	}
	updatedMongoContent := contentFromTODO(currentMongoRead.TODO)
	updatedMongoContent.Title += " revised"
	updatedMongoContent.Context += " Updated typed context."
	fourMongoRevision := int64(6)
	contentEdit, err := store.EditTODOContent(ctx, "company-a", TODOContentEditRequest{ProjectID: "builder", TODOID: mongoBorn.ID, ExpectedRevision: &fourMongoRevision, OperationID: "mongo-content-edit-001", Content: updatedMongoContent, Reason: "Change authored contract content"})
	if err != nil || contentEdit.Revision != 7 || contentEdit.ContentRevision == nil || *contentEdit.ContentRevision != 7 || !validSourceDigest(contentEdit.ContentDigest) {
		t.Fatalf("Mongo-born content edit = %+v, %v", contentEdit, err)
	}
	currentMongoRead, err = store.ReadTODO(ctx, "company-a", "builder", mongoBorn.ID, nil)
	newPlanningProof := currentMongoRead.TODO.VerifiedTransitionRef
	if err != nil || currentMongoRead.TODO.Stage != "Planning" || currentMongoRead.TODO.StageVerification != "verified" || newPlanningProof == nil || newPlanningProof.Kind != "planning-stage" || newPlanningProof.Source != "mongo-content-revision" || newPlanningProof.Reference != "builder/"+mongoBorn.ID+"/content/7" || newPlanningProof.SourceDigest != contentEdit.ContentDigest || newPlanningProof.VerificationState != "verified" || currentMongoRead.ContentRevision == nil || *currentMongoRead.ContentRevision != 7 || currentMongoRead.ContentDigest != contentEdit.ContentDigest {
		t.Fatalf("revised approval-pending TODO did not return to verified Planning: %+v, %v", currentMongoRead, err)
	}
	previousReview, err := store.ReadTODOAsOf(ctx, "company-a", "builder", mongoBorn.ID, 6)
	if err != nil || previousReview.State.Stage != "Implementation-Approval-Pending" || previousReview.State.StageVerification != "verified" {
		t.Fatalf("prior reviewed plan was lost from history: %+v, %v", previousReview, err)
	}
	revisedPlanning, err := store.ReadTODOAsOf(ctx, "company-a", "builder", mongoBorn.ID, 7)
	if err != nil || revisedPlanning.State.Stage != "Planning" || revisedPlanning.State.StageVerification != "verified" || revisedPlanning.State.VerifiedTransitionRef == nil || revisedPlanning.State.VerifiedTransitionRef.SourceDigest != contentEdit.ContentDigest {
		t.Fatalf("revised Planning was not recorded in history: %+v, %v", revisedPlanning, err)
	}
	editHistory, err := store.ReadTODOHistory(ctx, "company-a", "builder", mongoBorn.ID, 6, 10)
	if err != nil || len(editHistory.Events) != 1 || editHistory.Events[0].Kind != "content_changed" || editHistory.Events[0].Changes.Stage == nil || *editHistory.Events[0].Changes.Stage != "Planning" || editHistory.Events[0].Changes.VerifiedTransitionRef == nil || editHistory.Events[0].Changes.VerifiedTransitionRef.SourceDigest != contentEdit.ContentDigest {
		t.Fatalf("content edit did not record its Planning transition: %+v, %v", editHistory, err)
	}
	reviewComparison, err := store.CompareTODOHistory(ctx, "company-a", "builder", mongoBorn.ID, 6, 7)
	if err != nil || !reflect.DeepEqual(reviewComparison.Changed, []string{"context", "stage", "title", "verified_transition_ref"}) {
		t.Fatalf("reviewed-to-revised comparison = %+v, %v", reviewComparison, err)
	}
	var editedMongoVersion TODOContentVersion
	if err := database.Collection(todoContentVersionsCollection).FindOne(ctx, bson.D{{Key: "project_id", Value: "builder"}, {Key: "todo_id", Value: mongoBorn.ID}, {Key: "revision", Value: int64(7)}}).Decode(&editedMongoVersion); err != nil {
		t.Fatalf("read edited immutable content version: %v", err)
	}
	canonicalMongoDigest, err := todoContentRevisionDigest("builder", mongoBorn.ID, 7, editedMongoVersion.Content)
	if err != nil || editedMongoVersion.Digest != canonicalMongoDigest || editedMongoVersion.Digest == mongoAdmission.ContentDigest {
		t.Fatalf("content revision digest = %q, canonical %q, err %v", editedMongoVersion.Digest, canonicalMongoDigest, err)
	}
	recordPlanningReviewSubjects(t, ctx, store, "builder", mongoBorn.ID, "mongo-revised-review-subject")
	fiveMongoRevision := int64(8)
	reReviewed, err := store.TransitionTODO(ctx, "company-a", TODOActionRequest{ProjectID: "builder", TODOID: mongoBorn.ID, ExpectedRevision: &fiveMongoRevision, OperationID: "mongo-review-revised-001", RequestedStage: "Implementation-Approval-Pending", ProofReferences: []TODOProofReference{}, WaiverReferences: []TODOProofReference{}})
	if err != nil || reReviewed.Stage != "Implementation-Approval-Pending" || reReviewed.TODORevision != 9 {
		t.Fatalf("revised Planning could not re-enter approval pending: %+v, %v", reReviewed, err)
	}
	if _, err := store.TransitionTODO(ctx, "company-a", TODOActionRequest{ProjectID: "builder", TODOID: mongoBorn.ID, ExpectedRevision: int64Pointer(9), OperationID: "mongo-ready-without-approval", RequestedStage: "Ready-for-Implementation", ProofReferences: []TODOProofReference{}, WaiverReferences: []TODOProofReference{}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("re-reviewed plan advanced without owner approval: %v", err)
	}
	staleApproval := TODOProofReference{Kind: "owner-approval", Source: "owner-chat", Reference: "APROVADO", SourceDigest: mongoAdmission.ContentDigest, VerificationState: "unverified"}
	if _, err := store.TransitionTODO(ctx, "company-a", TODOActionRequest{ProjectID: "builder", TODOID: mongoBorn.ID, ExpectedRevision: int64Pointer(9), OperationID: "mongo-ready-stale-approval", RequestedStage: "Ready-for-Implementation", ProofReferences: []TODOProofReference{staleApproval}, WaiverReferences: []TODOProofReference{}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("re-reviewed plan accepted approval for old content: %v", err)
	}
	restoredMongo, err := store.RestoreTODOContent(ctx, "company-a", TODOContentRestoreRequest{ProjectID: "builder", TODOID: mongoBorn.ID, ExpectedRevision: int64Pointer(9), OperationID: "mongo-restore-pending-001", ContentRevision: 4, Reason: "Restore the placement-reviewed authored plan"})
	if err != nil || restoredMongo.Revision != 10 || restoredMongo.ContentRevision == nil || *restoredMongo.ContentRevision != 10 {
		t.Fatalf("restore of a reviewed plan = %+v, %v", restoredMongo, err)
	}
	restoredMongoRead, err := store.ReadTODO(ctx, "company-a", "builder", mongoBorn.ID, nil)
	if err != nil || restoredMongoRead.TODO.Stage != "Planning" || restoredMongoRead.TODO.StageVerification != "verified" || restoredMongoRead.TODO.Title != mongoBorn.Title || restoredMongoRead.TODO.VerifiedTransitionRef == nil || restoredMongoRead.TODO.VerifiedTransitionRef.SourceDigest != restoredMongo.ContentDigest {
		t.Fatalf("restore did not return to verified Planning: %+v, %v", restoredMongoRead, err)
	}
	recordPlanningReviewSubjects(t, ctx, store, "builder", mongoBorn.ID, "mongo-restored-review-subject")
	reReviewedRestored, err := store.TransitionTODO(ctx, "company-a", TODOActionRequest{ProjectID: "builder", TODOID: mongoBorn.ID, ExpectedRevision: int64Pointer(11), OperationID: "mongo-review-restored-001", RequestedStage: "Implementation-Approval-Pending", ProofReferences: []TODOProofReference{}, WaiverReferences: []TODOProofReference{}})
	if err != nil || reReviewedRestored.Stage != "Implementation-Approval-Pending" || reReviewedRestored.TODORevision != 12 {
		t.Fatalf("restored Planning could not re-enter approval pending: %+v, %v", reReviewedRestored, err)
	}
	currentApproval := staleApproval
	currentApproval.SourceDigest = restoredMongo.ContentDigest
	readyAfterReview, err := store.TransitionTODO(ctx, "company-a", TODOActionRequest{ProjectID: "builder", TODOID: mongoBorn.ID, ExpectedRevision: int64Pointer(12), OperationID: "mongo-ready-current-approval", RequestedStage: "Ready-for-Implementation", ProofReferences: []TODOProofReference{currentApproval}, WaiverReferences: []TODOProofReference{}})
	if err != nil || readyAfterReview.Stage != "Ready-for-Implementation" || readyAfterReview.StageVerification != "verified" || readyAfterReview.TODORevision != 13 {
		t.Fatalf("re-reviewed plan rejected approval for current content: %+v, %v", readyAfterReview, err)
	}
	readyBeforeNoOp, err := store.ReadTODO(ctx, "company-a", "builder", mongoBorn.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	readyContent := contentFromTODO(readyBeforeNoOp.TODO)
	readyNoOp, err := store.EditTODOContent(ctx, "company-a", TODOContentEditRequest{
		ProjectID: "builder", TODOID: mongoBorn.ID, ExpectedRevision: int64Pointer(13), OperationID: "mongo-ready-content-noop",
		Content: readyContent, Reason: "Confirm identical content does not reopen an approved plan",
	})
	if err != nil || !readyNoOp.NoOp || readyNoOp.Revision != 13 {
		t.Fatalf("identical Ready content edit = %+v, %v", readyNoOp, err)
	}
	readyAfterNoOp, err := store.ReadTODO(ctx, "company-a", "builder", mongoBorn.ID, nil)
	if err != nil || readyAfterNoOp.TODO.Stage != "Ready-for-Implementation" || readyAfterNoOp.TODO.StageVerification != "verified" || readyAfterNoOp.TODO.VerifiedTransitionRef == nil || readyAfterNoOp.TODO.VerifiedTransitionRef.SourceDigest != restoredMongo.ContentDigest {
		t.Fatalf("no-op content edit changed verified Ready state: %+v, %v", readyAfterNoOp, err)
	}
	readyContent.Title += " revised after approval"
	readyEdit, err := store.EditTODOContent(ctx, "company-a", TODOContentEditRequest{
		ProjectID: "builder", TODOID: mongoBorn.ID, ExpectedRevision: int64Pointer(13), OperationID: "mongo-ready-content-edit",
		Content: readyContent, Reason: "A substantive edit after Ready requires planning review again",
	})
	if err != nil || readyEdit.NoOp || readyEdit.Revision != 14 || readyEdit.ContentRevision == nil || *readyEdit.ContentRevision != 14 || !validSourceDigest(readyEdit.ContentDigest) {
		t.Fatalf("substantive Ready content edit = %+v, %v", readyEdit, err)
	}
	readyAfterEdit, err := store.ReadTODO(ctx, "company-a", "builder", mongoBorn.ID, nil)
	if err != nil || readyAfterEdit.TODO.Stage != "Planning" || readyAfterEdit.TODO.StageVerification != "verified" || readyAfterEdit.TODO.VerifiedTransitionRef == nil || readyAfterEdit.TODO.VerifiedTransitionRef.Kind != "planning-stage" || readyAfterEdit.TODO.VerifiedTransitionRef.SourceDigest != readyEdit.ContentDigest || readyAfterEdit.ContentRevision == nil || *readyAfterEdit.ContentRevision != 14 || readyAfterEdit.ContentDigest != readyEdit.ContentDigest {
		t.Fatalf("Ready edit did not return to digest-bound verified Planning: %+v, %v", readyAfterEdit, err)
	}
	readyEditHistory, err := store.ReadTODOHistory(ctx, "company-a", "builder", mongoBorn.ID, 13, 10)
	if err != nil || len(readyEditHistory.Events) != 1 || readyEditHistory.Events[0].Kind != "content_changed" || readyEditHistory.Events[0].ContentRevision == nil || *readyEditHistory.Events[0].ContentRevision != 14 || readyEditHistory.Events[0].Changes.Stage == nil || *readyEditHistory.Events[0].Changes.Stage != "Planning" || readyEditHistory.Events[0].Changes.StageVerification == nil || *readyEditHistory.Events[0].Changes.StageVerification != "verified" || readyEditHistory.Events[0].Changes.VerifiedTransitionRef == nil || readyEditHistory.Events[0].Changes.VerifiedTransitionRef.SourceDigest != readyEdit.ContentDigest {
		t.Fatalf("Ready edit history lacks the Planning proof and content revision: %+v, %v", readyEditHistory, err)
	}
	recordPlanningReviewSubjects(t, ctx, store, "builder", mongoBorn.ID, "mongo-ready-review-subject")
	reapprovedAfterEdit, err := store.TransitionTODO(ctx, "company-a", TODOActionRequest{ProjectID: "builder", TODOID: mongoBorn.ID, ExpectedRevision: int64Pointer(15), OperationID: "mongo-ready-edit-re-review", RequestedStage: "Implementation-Approval-Pending", ProofReferences: []TODOProofReference{}, WaiverReferences: []TODOProofReference{}})
	if err != nil || reapprovedAfterEdit.Stage != "Implementation-Approval-Pending" || reapprovedAfterEdit.TODORevision != 16 {
		t.Fatalf("edited Ready plan could not re-enter review: %+v, %v", reapprovedAfterEdit, err)
	}
	approvalAfterEdit := currentApproval
	approvalAfterEdit.SourceDigest = readyEdit.ContentDigest
	readyAgain, err := store.TransitionTODO(ctx, "company-a", TODOActionRequest{ProjectID: "builder", TODOID: mongoBorn.ID, ExpectedRevision: int64Pointer(16), OperationID: "mongo-ready-edit-reapproved", RequestedStage: "Ready-for-Implementation", ProofReferences: []TODOProofReference{approvalAfterEdit}, WaiverReferences: []TODOProofReference{}})
	if err != nil || readyAgain.Stage != "Ready-for-Implementation" || readyAgain.TODORevision != 17 {
		t.Fatalf("edited plan could not return to Ready: %+v, %v", readyAgain, err)
	}
	startedAfterEdit, err := store.TransitionTODO(ctx, "company-a", TODOActionRequest{ProjectID: "builder", TODOID: mongoBorn.ID, ExpectedRevision: int64Pointer(17), OperationID: "mongo-ready-edit-started", RequestedStage: "In-Progress", ProofReferences: []TODOProofReference{}, WaiverReferences: []TODOProofReference{}})
	if err != nil || startedAfterEdit.Stage != "In-Progress" || startedAfterEdit.TODORevision != 18 {
		t.Fatalf("reapproved plan could not start implementation: %+v, %v", startedAfterEdit, err)
	}
	inProgressContent := contentFromTODO(readyAfterEdit.TODO)
	inProgressContent.Title += " implementation refinement"
	inProgressEdit, err := store.EditTODOContent(ctx, "company-a", TODOContentEditRequest{
		ProjectID: "builder", TODOID: mongoBorn.ID, ExpectedRevision: int64Pointer(18), OperationID: "mongo-in-progress-content-edit",
		Content: inProgressContent, Reason: "Confirm implementation-stage edits retain the existing rule",
	})
	if err != nil || inProgressEdit.Revision != 19 || inProgressEdit.ContentRevision == nil || *inProgressEdit.ContentRevision != 19 {
		t.Fatalf("In-Progress content edit = %+v, %v", inProgressEdit, err)
	}
	inProgressAfterEdit, err := store.ReadTODO(ctx, "company-a", "builder", mongoBorn.ID, nil)
	if err != nil || inProgressAfterEdit.TODO.Stage != "Planning" || inProgressAfterEdit.TODO.StageVerification != "verified" || inProgressAfterEdit.TODO.VerifiedTransitionRef == nil || inProgressAfterEdit.TODO.VerifiedTransitionRef.Kind != "planning-stage" || inProgressAfterEdit.TODO.VerifiedTransitionRef.SourceDigest != inProgressEdit.ContentDigest {
		t.Fatalf("In-Progress content edit did not reopen verified Planning: %+v, %v", inProgressAfterEdit, err)
	}
	for _, advancedStage := range []string{"Local-Implemented", "Promotion-Lane", "Completed"} {
		advancedRecord, readErr := readSelectedTODO(ctx, database.Collection(todosCollection), "builder", mongoBorn.ID)
		if readErr != nil {
			t.Fatal(readErr)
		}
		fixtureProof := TODOProofReference{Kind: "test-stage-proof", Source: "test-fixture", Reference: advancedStage, SourceDigest: sourceHash([]byte(advancedStage)), VerificationState: "verified"}
		seeded, seedErr := writeVerifiedTODO(ctx, database, database.Collection(todosCollection), "company-a", advancedRecord, advancedStage, fixtureProof, "seed-"+advancedStage, "transition", sourceHash([]byte("seed-"+advancedStage)))
		if seedErr != nil {
			t.Fatalf("seed %s: %v", advancedStage, seedErr)
		}
		advancedRead, readErr := store.ReadTODO(ctx, "company-a", "builder", mongoBorn.ID, nil)
		if readErr != nil {
			t.Fatal(readErr)
		}
		changed := contentFromTODO(advancedRead.TODO)
		changed.Context += " Revised from " + advancedStage + "."
		edit, editErr := store.EditTODOContent(ctx, "company-a", TODOContentEditRequest{ProjectID: "builder", TODOID: mongoBorn.ID, ExpectedRevision: int64Pointer(seeded.TODORevision), OperationID: "replan-" + advancedStage, Content: changed, Reason: "Replan changed advanced content"})
		if editErr != nil || edit.Revision != seeded.TODORevision+1 {
			t.Fatalf("edit from %s: %+v, %v", advancedStage, edit, editErr)
		}
		before, beforeErr := store.ReadTODOAsOf(ctx, "company-a", "builder", mongoBorn.ID, seeded.TODORevision)
		after, afterErr := store.ReadTODOAsOf(ctx, "company-a", "builder", mongoBorn.ID, edit.Revision)
		if beforeErr != nil || afterErr != nil || before.State.Stage != advancedStage || before.State.StageVerification != "verified" || after.State.Stage != "Planning" || after.State.StageVerification != "verified" || after.State.VerifiedTransitionRef == nil || after.State.VerifiedTransitionRef.SourceDigest != edit.ContentDigest {
			t.Fatalf("replan from %s lost stage history: before=%+v (%v), after=%+v (%v)", advancedStage, before, beforeErr, after, afterErr)
		}
	}
	advancedRecord, err := readSelectedTODO(ctx, database.Collection(todosCollection), "builder", mongoBorn.ID)
	if err != nil {
		t.Fatal(err)
	}
	fixtureProof := TODOProofReference{Kind: "test-stage-proof", Source: "test-fixture", Reference: "stranded-local", SourceDigest: sourceHash([]byte("stranded-local")), VerificationState: "verified"}
	seeded, err := writeVerifiedTODO(ctx, database, database.Collection(todosCollection), "company-a", advancedRecord, "Local-Implemented", fixtureProof, "seed-stranded-local", "transition", sourceHash([]byte("seed-stranded-local")))
	if err != nil {
		t.Fatal(err)
	}
	strandedRevision := seeded.TODORevision + 1
	unverified := "unverified"
	if _, err := database.Collection(todosCollection).UpdateOne(ctx, bson.D{{Key: "project_id", Value: "builder"}, {Key: "id", Value: mongoBorn.ID}, {Key: "revision", Value: seeded.TODORevision}}, bson.D{{Key: "$set", Value: bson.D{{Key: "stage_verification", Value: unverified}}}, {Key: "$unset", Value: bson.D{{Key: "verified_transition_ref", Value: ""}}}, {Key: "$inc", Value: bson.D{{Key: "revision", Value: 1}}}}); err != nil {
		t.Fatal(err)
	}
	strandedEvent := TODOHistoryEvent{ObjectID: bson.NewObjectID(), ProjectID: "builder", TODOID: mongoBorn.ID, Revision: strandedRevision, Kind: "state_changed", OccurredAt: time.Now().UTC(), OperationID: "seed-stranded-unverified", RequestDigest: sourceHash([]byte("seed-stranded-unverified")), Changes: TODOHistoryChanges{StageVerification: &unverified, ClearVerifiedTransitionRef: true}}
	if _, err := database.Collection(todoEventsCollection).InsertOne(ctx, strandedEvent); err != nil {
		t.Fatal(err)
	}
	strandedRead, err := store.ReadTODO(ctx, "company-a", "builder", mongoBorn.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	versionCountBefore, err := database.Collection(todoContentVersionsCollection).CountDocuments(ctx, bson.D{{Key: "project_id", Value: "builder"}, {Key: "todo_id", Value: mongoBorn.ID}})
	if err != nil {
		t.Fatal(err)
	}
	replanRequest := TODOContentEditRequest{ProjectID: "builder", TODOID: mongoBorn.ID, ExpectedRevision: &strandedRevision, OperationID: "recover-stranded-advanced", Content: contentFromTODO(strandedRead.TODO), Reason: "Recover an already unverified advanced content edit"}
	replannedAdvanced, err := store.EditTODOContent(ctx, "company-a", replanRequest)
	if err != nil || replannedAdvanced.NoOp || replannedAdvanced.Revision != strandedRevision+1 || replannedAdvanced.ContentDigest != strandedRead.ContentDigest || replannedAdvanced.ContentRevision == nil || strandedRead.ContentRevision == nil || *replannedAdvanced.ContentRevision != *strandedRead.ContentRevision {
		t.Fatalf("same-content governed replan = %+v, %v", replannedAdvanced, err)
	}
	recoveredRead, err := store.ReadTODO(ctx, "company-a", "builder", mongoBorn.ID, nil)
	versionCountAfter, countErr := database.Collection(todoContentVersionsCollection).CountDocuments(ctx, bson.D{{Key: "project_id", Value: "builder"}, {Key: "todo_id", Value: mongoBorn.ID}})
	if err != nil || countErr != nil || recoveredRead.TODO.Stage != "Planning" || recoveredRead.TODO.StageVerification != "verified" || recoveredRead.TODO.VerifiedTransitionRef == nil || recoveredRead.TODO.VerifiedTransitionRef.SourceDigest != strandedRead.ContentDigest || versionCountAfter != versionCountBefore {
		t.Fatalf("recovery did not preserve content version and prior proof: read=%+v, versions=%d/%d, errors=%v/%v", recoveredRead, versionCountBefore, versionCountAfter, err, countErr)
	}
	retry, retryErr := store.EditTODOContent(ctx, "company-a", replanRequest)
	if retryErr != nil || !retry.NoOp || retry.Revision != replannedAdvanced.Revision {
		t.Fatalf("same-content recovery retry = %+v, %v", retry, retryErr)
	}
	zero := int64(0)
	// Seed a previously verified v1 stage for the immutable-history exercise.
	// The public transition route must not create this state after the v2 cutoff.
	legacyRecord, err := readSelectedTODO(ctx, database.Collection(todosCollection), "builder", request.TODO.ID)
	if err != nil {
		t.Fatal(err)
	}
	proof := TODOProofReference{Kind: "owner-approval", Source: "owner-chat", Reference: "APROVADO", SourceDigest: request.TODO.Source.Digest, VerificationState: "verified"}
	legacyStartRequest := TODOActionRequest{ProjectID: "builder", TODOID: request.TODO.ID, ExpectedRevision: &zero, OperationID: "individual-start-001", RequestedStage: "In-Progress", ProofReferences: []TODOProofReference{{Kind: proof.Kind, Source: proof.Source, Reference: proof.Reference, SourceDigest: proof.SourceDigest, VerificationState: "unverified"}}, WaiverReferences: []TODOProofReference{}}
	legacyStartDigest, err := todoRequestDigest(legacyStartRequest)
	if err != nil {
		t.Fatal(err)
	}
	started, err := writeVerifiedTODO(ctx, database, database.Collection(todosCollection), "company-a", legacyRecord, "In-Progress", proof, "individual-start-001", "transition", legacyStartDigest)
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
	one := int64(1)
	startedRetry, err := store.TransitionTODO(ctx, "company-a", TODOActionRequest{
		ProjectID: "builder", TODOID: request.TODO.ID, ExpectedRevision: &one, OperationID: "individual-start-replay-after-cutoff", RequestedStage: "In-Progress",
		ProofReferences: []TODOProofReference{{Kind: proof.Kind, Source: proof.Source, Reference: proof.Reference, SourceDigest: proof.SourceDigest, VerificationState: "unverified"}}, WaiverReferences: []TODOProofReference{},
	})
	if err == nil || !strings.Contains(err.Error(), "source/content_revision") {
		t.Fatalf("v1 stage replay bypassed the v2 cutoff = %+v, %v", startedRetry, err)
	}
	page, err = store.ReadTODOHistory(ctx, "company-a", "builder", request.TODO.ID, -1, 100)
	if err != nil || len(page.Events) != 2 {
		t.Fatalf("exact retry added duplicate history event: %+v, %v", page, err)
	}
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
	if err != nil || !reflect.DeepEqual(comparison.Changed, []string{"sections", "stage_verification", "title", "verified_transition_ref"}) {
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
	projectBeforePlacement, err = store.Project(ctx, "company-a", "builder")
	if err != nil {
		t.Fatal(err)
	}
	roadmapRevision = projectBeforePlacement.RoadmapRevision
	placementRequest := TODOPlacementEditRequest{ProjectID: "builder", TODOID: request.TODO.ID, ExpectedRevision: &five, ExpectedRoadmapRevision: &roadmapRevision, OperationID: "placement-edit-001", ReleaseID: "r-09", Position: 4, Reason: "Move TODO to another Release in the same Project"}
	placement, err := store.EditTODOPlacement(ctx, "company-a", placementRequest)
	if err != nil || placement.Revision != 5 || placement.NoOp {
		t.Fatalf("placement-only edit = %+v, %v", placement, err)
	}
	six := int64(5)
	placementNoOp := placementRequest
	placementNoOp.ExpectedRevision = &six
	placementNoOp.OperationID = "placement-noop-001"
	roadmapRevision++
	placementNoOp.ExpectedRoadmapRevision = &roadmapRevision
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
	badMove := TODOPlacementEditRequest{ProjectID: "builder", TODOID: request.TODO.ID, ExpectedRevision: &six, ExpectedRoadmapRevision: &roadmapRevision, OperationID: "placement-foreign-001", ReleaseID: "foreign-r-01", Position: 0, Reason: "Reject cross-Project Release"}
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
	startedRetry, err = store.TransitionTODO(ctx, "company-a", legacyStartRequest)
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
	proofResult := &LocalProofResult{
		SchemaVersion: "builder-local-proof-result-v3", TODOID: request.TODO.ID,
		RunID:      "11111111-2222-4333-8444-555555555555",
		StartedAt:  time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano),
		FinishedAt: time.Now().UTC().Format(time.RFC3339Nano),
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
	_, err = store.TransitionTODO(ctx, "company-a", TODOActionRequest{ProjectID: "builder", TODOID: request.TODO.ID, ExpectedRevision: &seven, OperationID: "local-proof-001", RequestedStage: "Local-Implemented", LocalProof: proofResult})
	if err == nil || !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "source/content_revision") {
		t.Fatalf("Local-Implemented accepted a v1 historical stage after the cutoff: %v", err)
	}
	unchangedAfterLocalAttempt, readErr := store.ReadTODO(ctx, "company-a", "builder", request.TODO.ID, &seven)
	if readErr != nil || unchangedAfterLocalAttempt.TODO.Stage != "In-Progress" || unchangedAfterLocalAttempt.TODO.StageVerification != "unverified" || unchangedAfterLocalAttempt.TODO.VerifiedTransitionRef != nil {
		t.Fatalf("rejected Local-Implemented attempt changed approval state: %+v, %v", unchangedAfterLocalAttempt, readErr)
	}
	if _, err := store.ReadTODOAsOf(ctx, "company-a", "builder", request.TODO.ID, 6); err != nil {
		t.Fatalf("prior as-of state changed after local transition: %v", err)
	}
	// Planning is a lifecycle fact, not approval of a specific content revision.
	planning := mongoBornTODOFixture()
	planning.ID, planning.ReleaseID, planning.Position = "planning-edit-regression", "r-09", intPointer(1)
	if _, err := store.AdmitTODO(ctx, "company-a", "builder", TODOAdmissionRequest{SchemaVersion: todoCreationSchemaVersion, OperationID: "planning-create-test", TODO: planning}); err != nil {
		t.Fatalf("create Planning regression TODO: %v", err)
	}
	planningContent := TODOAuthoredContent{TODOContent: validTODOContent(), Title: planning.Title, Objective: planning.Objective, DiffExpectation: []TODORepositoryScope{}, AffectedFiles: []string{}}
	planningContent.Planning = &TODOPlanning{Complexity: "small", AuditTriggers: testAuditTriggers(), Assumptions: []TODOPlanningAssumption{}, Reviews: []TODOPlanningReview{{Kind: "critique", Reference: "review/critique"}}}
	planningContent.Objective = "Refined while still in Planning"
	zeroPlanning := int64(0)
	if _, err := store.EditTODOContent(ctx, "company-a", TODOContentEditRequest{ProjectID: "builder", TODOID: planning.ID, ExpectedRevision: &zeroPlanning, OperationID: "planning-edit-001", Content: planningContent, Reason: "Refine before approval"}); err != nil {
		t.Fatalf("refine Planning TODO: %v", err)
	}
	planningRead, err := store.ReadTODO(ctx, "company-a", "builder", planning.ID, nil)
	if err != nil || planningRead.TODO.Stage != "Planning" || planningRead.TODO.StageVerification != "verified" || planningRead.ContentRevision == nil || *planningRead.ContentRevision != 1 {
		t.Fatalf("Planning content edit lost stage proof: %+v, %v", planningRead, err)
	}
	if _, err := database.Collection(todosCollection).UpdateOne(ctx, bson.D{{Key: "project_id", Value: "builder"}, {Key: "id", Value: planning.ID}}, bson.D{{Key: "$set", Value: bson.D{{Key: "stage_verification", Value: "unverified"}}}, {Key: "$unset", Value: bson.D{{Key: "verified_transition_ref", Value: ""}}}}); err != nil {
		t.Fatalf("prepare prior-regression fixture: %v", err)
	}
	onePlanning := int64(1)
	recovered, err := store.VerifyCurrentTODOStage(ctx, "company-a", VerifyCurrentTODOStageRequest{ProjectID: "builder", TODOID: planning.ID, ExpectedRevision: &onePlanning, OperationID: "recover-planning-001", ProofReferences: []TODOProofReference{}})
	if err != nil || recovered.Stage != "Planning" || recovered.StageVerification != "verified" {
		t.Fatalf("recover already affected Planning TODO: %+v, %v", recovered, err)
	}
	twoPlanning := recordPlanningReviewSubjects(t, ctx, store, "builder", planning.ID, "recovered-planning-review-subject")
	pending, err := store.TransitionTODO(ctx, "company-a", TODOActionRequest{ProjectID: "builder", TODOID: planning.ID, ExpectedRevision: &twoPlanning, OperationID: "review-planning-001", RequestedStage: "Implementation-Approval-Pending", ProofReferences: []TODOProofReference{}, WaiverReferences: []TODOProofReference{}})
	if err != nil || pending.Stage != "Implementation-Approval-Pending" || pending.StageVerification != "verified" {
		t.Fatalf("pre-approval transition: %+v, %v", pending, err)
	}

	// A source-blind administrative create must remain idempotent even after
	// later placement and content revisions. Placement alone is not a new
	// decision about the accepted stage.
	accepted := mongoBornTODOFixture()
	accepted.ID, accepted.Stage = "builder-administratively-accepted", "Completed"
	accepted.ReleaseID, accepted.Position = "r-08", intPointer(8)
	adminCreate := TODOAdmissionRequest{SchemaVersion: todoCreationSchemaVersion, OperationID: "admin-accepted-create", TODO: accepted,
		InitialStateAcceptance: &TODOInitialStateAcceptance{OwnerReference: "owner-approval", Reason: "Owner confirms delivery without asserting deployment proof."}}
	original, err := store.AdmitTODO(ctx, "company-a", "builder", adminCreate)
	if err != nil || original.Revision != 0 || !validSourceDigest(original.ContentDigest) {
		t.Fatalf("administrative create: %+v, %v", original, err)
	}
	adminRead, err := store.ReadTODO(ctx, "company-a", "builder", accepted.ID, nil)
	if err != nil || adminRead.TODO.Stage != "Completed" || adminRead.TODO.StageVerification != "unverified" || adminRead.TODO.VerifiedTransitionRef != nil || adminRead.TODO.AdministrativeAcceptance == nil {
		t.Fatalf("administrative readback claimed verified delivery: %+v, %v", adminRead, err)
	}
	adminNoOp, err := store.EditTODOContent(ctx, "company-a", TODOContentEditRequest{ProjectID: "builder", TODOID: accepted.ID,
		ExpectedRevision: int64Pointer(0), OperationID: "admin-accepted-noop", Content: contentFromTODO(adminRead.TODO), Reason: "Confirm unchanged content"})
	if err != nil || !adminNoOp.NoOp || adminNoOp.Revision != 0 {
		t.Fatalf("unchanged content revoked administrative acceptance: %+v, %v", adminNoOp, err)
	}
	projectBeforeAdminMove, err := store.Project(ctx, "company-a", "builder")
	if err != nil {
		t.Fatal(err)
	}
	adminMoved, err := store.EditTODOPlacement(ctx, "company-a", TODOPlacementEditRequest{ProjectID: "builder", TODOID: accepted.ID,
		ExpectedRevision: int64Pointer(0), ExpectedRoadmapRevision: &projectBeforeAdminMove.RoadmapRevision,
		OperationID: "admin-accepted-move", ReleaseID: "r-09", Position: 8, Reason: "Owner moved grouping without changing acceptance"})
	if err != nil || adminMoved.Revision != 1 {
		t.Fatalf("accepted placement edit: %+v, %v", adminMoved, err)
	}
	adminRead, err = store.ReadTODO(ctx, "company-a", "builder", accepted.ID, nil)
	if err != nil || adminRead.TODO.Stage != "Completed" || adminRead.TODO.AdministrativeAcceptance == nil {
		t.Fatalf("placement revoked administrative acceptance: %+v, %v", adminRead, err)
	}
	adminContent := contentFromTODO(adminRead.TODO)
	adminContent.Title += " revised"
	if _, err := store.EditTODOContent(ctx, "company-a", TODOContentEditRequest{ProjectID: "builder", TODOID: accepted.ID,
		ExpectedRevision: int64Pointer(1), OperationID: "admin-accepted-content-revision", Content: adminContent,
		Reason: "Substantive contract change requires replanning"}); err != nil {
		t.Fatalf("edit accepted content: %v", err)
	}
	adminRead, err = store.ReadTODO(ctx, "company-a", "builder", accepted.ID, nil)
	if err != nil || adminRead.TODO.Stage != "Planning" || adminRead.TODO.AdministrativeAcceptance != nil {
		t.Fatalf("content edit retained stale effective acceptance: %+v, %v", adminRead, err)
	}
	replayed, err := store.AdmitTODO(ctx, "company-a", "builder", adminCreate)
	if err != nil || replayed != original {
		t.Fatalf("create replay after edits lost original result: %+v, %v", replayed, err)
	}
	eventCount, err := database.Collection(todoEventsCollection).CountDocuments(ctx, bson.D{{Key: "project_id", Value: "builder"}, {Key: "todo_id", Value: accepted.ID}, {Key: "operation_id", Value: adminCreate.OperationID}})
	if err != nil || eventCount != 1 {
		t.Fatalf("create replay wrote %d initial events: %v", eventCount, err)
	}
	conflicting := adminCreate
	conflicting.InitialStateAcceptance = ptrInitialAcceptance(*adminCreate.InitialStateAcceptance)
	conflicting.InitialStateAcceptance.Reason = "Different claim under the same key"
	if _, err := store.AdmitTODO(ctx, "company-a", "builder", conflicting); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting create replay = %v; want conflict", err)
	}
	disposed := mongoBornTODOFixture()
	disposed.ID, disposed.ReleaseID, disposed.Position = "builder-discarded-record", "r-08", intPointer(9)
	disposedCreate := TODOAdmissionRequest{SchemaVersion: todoCreationSchemaVersion, OperationID: "admin-disposed-create", TODO: disposed,
		InitialStateAcceptance: &TODOInitialStateAcceptance{OwnerReference: "owner-disposition", Reason: "Owner decided this work will not be delivered."},
		Disposition:            &TODODisposition{Kind: "Discarded", Reason: "No longer required."}}
	if _, err := store.AdmitTODO(ctx, "company-a", "builder", disposedCreate); err != nil {
		t.Fatalf("create non-delivery history: %v", err)
	}
	disposedRead, err := store.ReadTODO(ctx, "company-a", "builder", disposed.ID, nil)
	if err != nil || disposedRead.TODO.Disposition == nil || disposedRead.TODO.Disposition.Kind != "Discarded" || disposedRead.TODO.Stage != "Planning" {
		t.Fatalf("non-delivery readback: %+v, %v", disposedRead, err)
	}
	disposedRoadmap, err := store.Roadmap(ctx, "builder", "company-a")
	if err != nil || !strings.Contains(string(disposedRoadmap), `"kind":"non-delivery"`) || !strings.Contains(string(disposedRoadmap), disposed.ID) {
		t.Fatalf("non-delivery not projected in Release: %s, %v", disposedRoadmap, err)
	}
}
