package registry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	ac "delphi-local-api/artifactcatalog"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// This raw contract test intentionally compiles against the predecessor Go
// source: a schema implementation must reject the new kind semantically, not
// merely fail because the DTO is missing a field.
func TestPrototypeDeliveryContractIsExplicitAndClosed(t *testing.T) {
	const raw = `{"schema_version":2,"delivery":{"kind":"prototype","prototype_id":"main-prototype"}}`
	var content TODOContent
	if err := json.Unmarshal([]byte(raw), &content); err != nil {
		t.Fatal(err)
	}
	if err := ValidateTODOContentDraft(content); err != nil {
		t.Fatalf("explicit prototype contract was rejected: %v", err)
	}
	encoded, err := json.Marshal(content.Delivery)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 2 || string(fields["kind"]) != `"prototype"` || string(fields["prototype_id"]) != `"main-prototype"` {
		t.Fatalf("prototype contract serialized unexpected fields: %s", encoded)
	}
	mixed := validTODOContent()
	mixed.Delivery = content.Delivery
	mixed.LocalChecks = []TODOLocalCheck{{ID: "check-one", RepositoryID: "belluga/test", Argv: []string{"go", "test"}, WorkingDirectory: ".", InputPaths: []string{"go-app"}}}
	if err := validatePrototypeNoCodeFields(&mixed, TODORecord{Content: &mixed}, nil); err == nil {
		t.Fatal("Prototype content accepted a code local-check contract")
	}
}

func TestPrototypeDeliveryContractRejectsKnowledgeAndUnknownShape(t *testing.T) {
	for _, raw := range []string{
		`{"schema_version":2,"delivery":{"kind":"prototype"}}`,
		`{"schema_version":2,"delivery":{"kind":"prototype","prototype_id":"main-prototype","targets":[{"family":"module","record_id":"m","generation_id":"g","baseline_revision":0,"change_kind":"create"}]}}`,
		`{"schema_version":2,"delivery":{"kind":"other","prototype_id":"main-prototype"}}`,
	} {
		var content TODOContent
		if decodeErr := json.Unmarshal([]byte(raw), &content); decodeErr == nil && ValidateTODOContentDraft(content) == nil {
			t.Fatalf("invalid delivery contract was accepted: %s", raw)
		}
	}
}

func TestTODOActionDigestPreservesPrePrototypeCodeAndKnowledgeShape(t *testing.T) {
	type legacyAction struct {
		ProjectID                string                        `json:"project_id"`
		TODOID                   string                        `json:"todo_id"`
		ExpectedRevision         *int64                        `json:"expected_revision"`
		OperationID              string                        `json:"operation_id"`
		RequestedStage           string                        `json:"requested_stage"`
		PromotionTargets         map[string]string             `json:"promotion_targets,omitempty"`
		ProofReferences          []TODOProofReference          `json:"proof_refs"`
		WaiverReferences         []TODOProofReference          `json:"waiver_refs"`
		LocalProof               *LocalProofResult             `json:"local_proof,omitempty"`
		KnowledgeDelivery        *TODOKnowledgeDeliveryClaim   `json:"knowledge_delivery,omitempty"`
		KnowledgeFinalAcceptance *TODOKnowledgeFinalAcceptance `json:"knowledge_final_acceptance,omitempty"`
	}
	revision := int64(7)
	request := TODOActionRequest{ProjectID: "builder", TODOID: "todo-a", ExpectedRevision: &revision, OperationID: "legacy-code-retry", RequestedStage: "Completed", ProofReferences: []TODOProofReference{}, WaiverReferences: []TODOProofReference{}}
	legacy := legacyAction{ProjectID: request.ProjectID, TODOID: request.TODOID, ExpectedRevision: request.ExpectedRevision, OperationID: request.OperationID, RequestedStage: request.RequestedStage, PromotionTargets: request.PromotionTargets, ProofReferences: request.ProofReferences, WaiverReferences: request.WaiverReferences, LocalProof: request.LocalProof, KnowledgeDelivery: request.KnowledgeDelivery, KnowledgeFinalAcceptance: request.KnowledgeFinalAcceptance}
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	want := sourceHash(data)
	got, err := todoRequestDigest(request)
	if err != nil || got != want {
		t.Fatalf("pre-prototype code/Knowledge operation digest changed: got=%s want=%s err=%v", got, want, err)
	}
}

func TestPrototypeDeliveryBindingRevisionDistinguishesNullFromZero(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
		want bool
	}{
		{name: "explicit zero", body: `{"content_digest":"sha256:` + strings.Repeat("a", 64) + `","revision":"` + strings.Repeat("b", 40) + `","inventory_digest":"sha256:` + strings.Repeat("c", 64) + `","binding_revision":0}`, want: true},
		{name: "null", body: `{"content_digest":"sha256:` + strings.Repeat("a", 64) + `","revision":"` + strings.Repeat("b", 40) + `","inventory_digest":"sha256:` + strings.Repeat("c", 64) + `","binding_revision":null}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			var claim TODOPrototypeDeliveryClaim
			err := json.Unmarshal([]byte(test.body), &claim)
			if (err == nil) != test.want || test.want && (!claim.bindingRevisionPresent || claim.BindingRevision != 0) {
				t.Fatalf("binding_revision decoding did not distinguish null and zero: claim=%+v present=%v err=%v", claim, claim.bindingRevisionPresent, err)
			}
		})
	}
}

type prototypeDeliveryFixtureSource map[string][]byte

func (s prototypeDeliveryFixtureSource) Mode() string        { return "git-commit" }
func (s prototypeDeliveryFixtureSource) Revision() *string   { return nil }
func (s prototypeDeliveryFixtureSource) VerifyStable() error { return nil }
func (s prototypeDeliveryFixtureSource) List(prefix string) ([]ac.Entry, error) {
	entries := []ac.Entry{{Path: "prototypes", Mode: ac.ModeDirectory}, {Path: "prototypes/main-prototype", Mode: ac.ModeDirectory}}
	for path, data := range s {
		if path == "prototypes/catalog.json" || strings.HasPrefix(path, prefix+"/") {
			entries = append(entries, ac.Entry{Path: path, Mode: ac.ModeRegular, Size: int64(len(data))})
		}
	}
	return entries, nil
}
func (s prototypeDeliveryFixtureSource) Read(path string, _ int64) ([]byte, ac.FileMode, error) {
	data, ok := s[path]
	if !ok {
		return nil, ac.ModeSpecial, fmt.Errorf("missing fixture file %s", path)
	}
	return data, ac.ModeRegular, nil
}
func (s prototypeDeliveryFixtureSource) CheckRegular(path string, _ int64) error {
	if _, ok := s[path]; !ok {
		return fmt.Errorf("missing fixture file %s", path)
	}
	return nil
}

type prototypeDeliveryGitReader struct {
	root      string
	sha       string
	openCount int
	afterHead func()
	afterOpen func()
}

func (r *prototypeDeliveryGitReader) BranchHead(context.Context, RepositoryBinding) (string, error) {
	head := r.sha
	if r.afterHead != nil {
		callback := r.afterHead
		r.afterHead = nil
		callback()
	}
	return head, nil
}
func (r *prototypeDeliveryGitReader) OpenRevision(_ context.Context, _, _ string, _ RepositoryBinding, revision string) (prototypeGitSource, error) {
	r.openCount++
	if revision != r.sha {
		return prototypeGitSource{}, ErrConflict
	}
	source, err := ac.NewGitTree(r.root, revision)
	if err != nil {
		return prototypeGitSource{}, err
	}
	if r.afterOpen != nil {
		callback := r.afterOpen
		r.afterOpen = nil
		callback()
	}
	return prototypeGitSource{workspace: r.root, source: source, sha: revision}, nil
}

// This integration test exercises the normal prototype lifecycle and the
// direct Completed HTTP edge against the isolated Mongo replica set. The
// branch starts without the approved candidate; it is published before the
// final observation, matching the Planning contract's forward-looking shape.
func TestMongoPrototypeDeliveryCompletesDirectlyFromCurrentMain(t *testing.T) {
	for _, mode := range []string{"mvp", "complete"} {
		t.Run(mode, func(t *testing.T) { testMongoPrototypeDeliveryCompletesDirectlyFromCurrentMain(t, mode) })
	}
}

func testMongoPrototypeDeliveryCompletesDirectlyFromCurrentMain(t *testing.T, mode string) {
	uri := os.Getenv("REGISTRY_TEST_MONGODB_URI")
	if uri == "" {
		if os.Getenv("REGISTRY_REQUIRE_MONGO_INTEGRATION") == "1" {
			t.Fatal("required Mongo replica-set fixture is unavailable")
		}
		t.Skip("isolated Mongo replica-set fixture not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	stamp := strconv.FormatInt(time.Now().UnixNano(), 36)
	store, err := OpenMongo(ctx, uri, "prototype_delivery_"+stamp)
	if err != nil {
		t.Fatal(err)
	}
	store.tenantPrefix = "prototype_delivery_" + stamp
	databaseName := store.tenantDatabase("company-prototype")
	database := store.client.Database(databaseName)
	t.Cleanup(func() {
		_ = database.Drop(context.Background())
		_ = store.control.Drop(context.Background())
		_ = store.Close(context.Background())
	})
	company, err := normalizeCompany(Company{ID: "company-prototype", Name: "Prototype", DatabaseName: databaseName})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.control.Collection(companiesCollection).InsertOne(ctx, company); err != nil {
		t.Fatal(err)
	}
	migrateCompanyFixture(t, ctx, store)
	project, err := registerTestProjectWithBinding(ctx, store, Registration{CompanyID: "company-prototype", CompanyName: "Prototype", ProjectID: "builder", ProjectName: "Builder", Binding: validTestBinding()})
	if err != nil {
		t.Fatal(err)
	}
	canonical := CanonicalImport{ProjectID: "builder", GenerationID: "prototype-mode-" + mode, SourceRevision: strings.Repeat("a", 40), OperationID: "prototype-mode-import-" + mode, Actor: "integration-test", Records: []CanonicalImportRecord{
		canonicalTestRecord(FamilyMandate, "builder", `{"purpose":"Exercise Prototype direct delivery","scope":["project"]}`),
		canonicalTestRecord(FamilyDomain, "builder", `{"entities":[{"id":"Project","name":"Project","description":""}],"relations":[{"id":"OWNS","from":"Project","to":"Project","kind":"owns"}],"distinctions":[]}`),
		canonicalTestRecord(FamilyConstitution, "builder", fmt.Sprintf(`{"promotion_mode":%q,"capability_namespaces":["go"],"rules":[{"id":"authority","status":"current","statement":"Project API records verified delivery"}]}`, mode)),
		canonicalTestRecord(FamilyRoadmap, "builder", `{"stages":[{"id":"delivery","name":"Delivery","status":"planned"}]}`),
		canonicalTestRecord(FamilyModule, "projects", `{"purpose":"Owns project work","decisions":[{"id":"DEC-PROJECT-01","status":"current","statement":"Project owns TODO delivery"}],"schemas":[{"id":"todo","name":"todos","kind":"collection","fields":[{"name":"stage","type":"string"}]}]}`),
		canonicalTestRecord(FamilyPolicy, "access-policy", `{"rules":[{"id":"retention","status":"current","statement":"Keep audit records"}],"ownership":[{"owner_kind":"module","owner_id":"projects","decision_id":"DEC-PROJECT-01"}]}`),
	}}
	for i := range canonical.Records {
		canonical.Records[i].Record.ProjectID = "builder"
		canonical.Records[i].Record.GenerationID = canonical.GenerationID
	}
	if _, err := store.ImportCanonicalDraft(ctx, "company-prototype", canonical); err != nil {
		t.Fatalf("import %s-mode Constitution fixture: %v", mode, err)
	}
	if _, err := store.ActivateCanonical(ctx, "company-prototype", "builder", canonical.GenerationID, "prototype-mode-activate-"+mode, "integration-test"); err != nil {
		t.Fatalf("activate %s-mode Constitution fixture: %v", mode, err)
	}
	if err := store.ProvisionTODOCollection(ctx, "company-prototype"); err != nil {
		t.Fatal(err)
	}
	if err := store.ProvisionTODOHistory(ctx, "company-prototype"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Collection(epicsCollection).InsertOne(ctx, roadmapEpic{StorageID: bson.NewObjectID(), ProjectID: "builder", ID: "e-prototype", Name: "Prototype", Position: 0}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Collection(releasesCollection).InsertOne(ctx, roadmapRelease{StorageID: bson.NewObjectID(), ProjectID: "builder", ID: "r-prototype", EpicID: "e-prototype", Name: "Prototype", Position: 0}); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	git("init", "--quiet", "--initial-branch=main")
	git("config", "user.name", "Prototype Integration")
	git("config", "user.email", "prototype@example.invalid")
	publishCount := 0
	candidateStatus := "active"
	invalidInventory := false
	writeCatalog := func(withCandidate bool) string {
		t.Helper()
		publishCount++
		if err := os.MkdirAll(filepath.Join(root, "prototypes/main-prototype/images"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(root, "todos/active"), 0o700); err != nil {
			t.Fatal(err)
		}
		catalog := ac.Catalog{SchemaVersion: ac.PrototypeSchemaVersion, ProjectID: "builder", Prototypes: []ac.Prototype{}}
		if withCandidate {
			rootPath := "prototypes/main-prototype"
			if invalidInventory {
				rootPath = "../invalid"
			}
			catalog.Prototypes = []ac.Prototype{{ID: "main-prototype", Name: "Main prototype", Root: rootPath, Status: candidateStatus}}
			manifest := ac.Manifest{SchemaVersion: ac.PrototypeSchemaVersion, ID: "main-prototype", AuthoringMode: "design_system_first", EntryPoint: "index.html", Screens: []ac.Screen{{ID: "home", Name: "Home", Path: "index.html", DefaultStateID: "default", States: []ac.State{{ID: "default", Name: "Default", IdentifierImage: "images/home.webp", ApprovedReferences: []ac.ApprovedReference{}}}}}, Sources: []string{"index.html"}, Assets: []string{"images/home.webp"}, Transitions: []ac.Transition{}, Scenarios: []ac.Scenario{}, Related: []ac.Related{{Kind: "todo", Path: "todos/active/evidence.md"}}}
			manifestBytes, _ := json.Marshal(manifest)
			if err := os.WriteFile(filepath.Join(root, "prototypes/main-prototype/prototype.json"), manifestBytes, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "prototypes/main-prototype/index.html"), []byte(fmt.Sprintf("<main>Ready %d</main>", publishCount)), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "prototypes/main-prototype/images/home.webp"), []byte("image"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "todos/active/evidence.md"), []byte("evidence"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		catalogBytes, _ := json.Marshal(catalog)
		if err := os.WriteFile(filepath.Join(root, "prototypes/catalog.json"), catalogBytes, 0o600); err != nil {
			t.Fatal(err)
		}
		git("add", "prototypes", "todos")
		git("commit", "--quiet", "-m", "publish prototype inventory")
		return git("rev-parse", "HEAD")
	}
	reader := &prototypeDeliveryGitReader{root: root, sha: writeCatalog(false)}
	store.prototypeGit = reader
	mutateBindingOnIdentity := false
	store.prototypeIdentityReader = func(ctx context.Context, companyID, projectID string) (Project, error) {
		value, err := store.Project(ctx, companyID, projectID)
		if err == nil && mutateBindingOnIdentity {
			mutateBindingOnIdentity = false
			if _, err := store.ChangeRepositoryBinding(ctx, companyID, projectID, RepositoryBindingRequest{Action: "replace", ProjectID: projectID, ExpectedRevision: value.BindingRevision, OperationID: "prototype-binding-race", Actor: "integration-test", RepositoryID: "belluga/other", Branch: "main"}); err != nil {
				return Project{}, err
			}
		}
		return value, err
	}
	knowledgeChanged := false
	store.prototypeCanonicalManifestReader = func(ctx context.Context, companyID, projectID string) (CanonicalManifest, error) {
		manifest, err := store.CanonicalManifest(ctx, companyID, projectID, "")
		if err != nil {
			return manifest, err
		}
		if knowledgeChanged {
			manifest.Documents = append(manifest.Documents, CanonicalManifestEntry{Family: FamilyPolicy, ID: "access-policy-race", Revision: 1, ContentDigest: "sha256:" + strings.Repeat("1", 64)})
		}
		return manifest, nil
	}
	_ = project

	content := validTODOContent()
	content.Delivery = &TODODeliveryContract{Kind: "prototype", PrototypeID: "main-prototype"}
	content.Planning = &TODOPlanning{Complexity: "small", AuditTriggers: testAuditTriggers(), Impact: TODOPlanningImpact{}, Assumptions: []TODOPlanningAssumption{{ID: "assumption-one", Status: "resolved", Statement: "The prototype branch is the delivery surface.", CodeRefs: []TODOPlanningCodeRef{}}}, Reviews: []TODOPlanningReview{{Kind: "critique", Reference: "review/prototype-critique"}}}
	todo := mongoBornTODOFixture()
	todo.ID, todo.ReleaseID, todo.Position = "prototype-delivery", "r-prototype", intPointer(0)
	todo.Title = "TODO: Prototype direct delivery"
	todo.Content = &content
	_, err = store.AdmitTODO(ctx, "company-prototype", "builder", TODOAdmissionRequest{SchemaVersion: todoCreationSchemaVersion, OperationID: "prototype-admit", TODO: todo})
	if err != nil {
		t.Fatalf("admit prototype TODO without candidate present: %v", err)
	}
	beforeDelivery, err := store.prototypeSnapshot(ctx, "company-prototype", "builder", "")
	if err != nil {
		t.Fatalf("valid main inventory without the future candidate was rejected: %v", err)
	}
	for _, candidate := range beforeDelivery.Items {
		if candidate.ID == "main-prototype" {
			t.Fatal("candidate was present before the approved prototype delivery")
		}
	}
	current, err := store.ReadTODO(ctx, "company-prototype", "builder", todo.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	refs := []TODOProofReference{
		{Kind: "planning_review_critique", Source: "human-review", Reference: "review/prototype-critique", SourceDigest: current.ContentDigest, VerificationState: "unverified", Outcome: "go"},
		{Kind: "criterion:criterion-one", Source: "external-evidence", Reference: "evidence/criterion-one", SourceDigest: current.ContentDigest, VerificationState: "unverified"},
		{Kind: "scenario:scenario-one", Source: "external-evidence", Reference: "evidence/scenario-one", SourceDigest: current.ContentDigest, VerificationState: "unverified"},
		{Kind: "final_review", Source: "human-review", Reference: "review/prototype-final", SourceDigest: current.ContentDigest, VerificationState: "unverified", Outcome: "go"},
	}
	if _, err := store.EditTODOEvidence(ctx, "company-prototype", TODOEvidenceEditRequest{ProjectID: "builder", TODOID: todo.ID, ExpectedRevision: &current.TODORevision, OperationID: "prototype-evidence", ProofReferences: refs, WaiverReferences: []TODOProofReference{}, ImplementationCommits: []TODOCommitReference{}, RecordSections: recordRecordSections(current.TODO), Reason: "Record prototype planning and delivery evidence"}); err != nil {
		t.Fatalf("record current planning and external evidence: %v", err)
	}
	advance := func(operation, stage string, approval bool) {
		t.Helper()
		current, err = store.ReadTODO(ctx, "company-prototype", "builder", todo.ID, nil)
		if err != nil {
			t.Fatal(err)
		}
		request := TODOActionRequest{ProjectID: "builder", TODOID: todo.ID, ExpectedRevision: &current.TODORevision, OperationID: operation, RequestedStage: stage}
		if approval {
			request.ProofReferences = []TODOProofReference{{Kind: "owner-approval", Source: "owner-chat", Reference: "APROVADO", SourceDigest: current.ContentDigest, VerificationState: "unverified"}}
		}
		if _, err := store.TransitionTODO(ctx, "company-prototype", request); err != nil {
			t.Fatalf("transition %s: %v", stage, err)
		}
	}
	advance("prototype-planning", "Implementation-Approval-Pending", false)
	advance("prototype-approved", "Ready-for-Implementation", true)
	advance("prototype-started", "In-Progress", false)
	reader.sha = writeCatalog(true)
	catalog, err := store.prototypeSnapshot(ctx, "company-prototype", "builder", "")
	if err != nil {
		t.Fatalf("read published candidate after delivery: %v", err)
	}
	mutateBindingOnIdentity = true
	if err := store.fencePrototypeCurrent(ctx, "company-prototype", "builder", catalog); !errors.Is(err, ErrConflict) {
		t.Fatalf("binding replacement between identity and BranchHead was not fenced: %v", err)
	}
	currentProject, err := store.Project(ctx, "company-prototype", "builder")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ChangeRepositoryBinding(ctx, "company-prototype", "builder", RepositoryBindingRequest{Action: "replace", ProjectID: "builder", ExpectedRevision: currentProject.BindingRevision, OperationID: "prototype-binding-restore", Actor: "integration-test", RepositoryID: "belluga/test", Branch: "main"}); err != nil {
		t.Fatalf("restore bound repository after race test: %v", err)
	}
	// Re-observe after restoring the binding so the claim uses its new revision,
	// then prove the active projection is warm before injecting a head race.
	catalog, err = store.prototypeSnapshot(ctx, "company-prototype", "builder", "")
	if err != nil {
		t.Fatalf("refresh catalog after restoring repository binding: %v", err)
	}
	opensAfterRefresh := reader.openCount
	warmCatalog, err := store.prototypeSnapshot(ctx, "company-prototype", "builder", "")
	if err != nil {
		t.Fatalf("confirm restored-binding projection cache hit: %v", err)
	}
	if reader.openCount != opensAfterRefresh || warmCatalog.BindingRevision != catalog.BindingRevision || warmCatalog.Revision != catalog.Revision {
		t.Fatalf("restored-binding projection was not warm: opens=%d want=%d catalog=%+v warm=%+v", reader.openCount, opensAfterRefresh, catalog, warmCatalog)
	}
	catalog = warmCatalog
	current, err = store.ReadTODO(ctx, "company-prototype", "builder", todo.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	claim := TODOPrototypeDeliveryClaim{ContentDigest: current.ContentDigest, Revision: catalog.Revision, InventoryDigest: catalog.InventoryDigest, BindingRevision: catalog.BindingRevision}
	pathRoot := "/internal/v1/companies/company-prototype/projects/builder/todos/" + todo.ID
	path := pathRoot + "/transition"
	handler := NewHandler(store)
	postPath := func(suffix string, body []byte) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, path+suffix, bytes.NewReader(body))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	post := func(body []byte) *httptest.ResponseRecorder { return postPath("", body) }
	baseBody := func(operation string, tuple TODOPrototypeDeliveryClaim) []byte {
		data, _ := json.Marshal(map[string]any{"project_id": "builder", "todo_id": todo.ID, "expected_revision": current.TODORevision, "operation_id": operation, "requested_stage": "Completed", "prototype_delivery": tuple})
		return data
	}
	assertRejected := func(name string, body []byte, status int, field string) {
		t.Helper()
		before, readErr := store.ReadTODO(ctx, "company-prototype", "builder", todo.ID, nil)
		if readErr != nil {
			t.Fatal(readErr)
		}
		beforeEvents, countErr := database.Collection(todoEventsCollection).CountDocuments(ctx, bson.D{{Key: "project_id", Value: "builder"}, {Key: "todo_id", Value: todo.ID}})
		if countErr != nil {
			t.Fatal(countErr)
		}
		response := post(body)
		var rejection errorResponse
		if err := json.Unmarshal(response.Body.Bytes(), &rejection); err != nil {
			t.Fatalf("%s rejection was not JSON: status=%d body=%s err=%v", name, response.Code, response.Body.String(), err)
		}
		if response.Code != status || !strings.Contains(rejection.Teach, "TEACH: "+field+" ") {
			t.Fatalf("%s returned wrong rejection: status=%d want=%d teach=%q body=%s", name, response.Code, status, rejection.Teach, response.Body.String())
		}
		after, readErr := store.ReadTODO(ctx, "company-prototype", "builder", todo.ID, nil)
		if readErr != nil {
			t.Fatal(readErr)
		}
		afterEvents, countErr := database.Collection(todoEventsCollection).CountDocuments(ctx, bson.D{{Key: "project_id", Value: "builder"}, {Key: "todo_id", Value: todo.ID}})
		if countErr != nil {
			t.Fatal(countErr)
		}
		if after.TODORevision != before.TODORevision || after.TODO.Stage != before.TODO.Stage || afterEvents != beforeEvents {
			t.Fatalf("%s rejection mutated TODO/history: before=%d/%s/%d after=%d/%s/%d", name, before.TODORevision, before.TODO.Stage, beforeEvents, after.TODORevision, after.TODO.Stage, afterEvents)
		}
	}
	assertMutationRejected := func(name, suffix string, body []byte, status int, field string) {
		t.Helper()
		before, readErr := store.ReadTODO(ctx, "company-prototype", "builder", todo.ID, nil)
		if readErr != nil {
			t.Fatal(readErr)
		}
		beforeEvents, countErr := database.Collection(todoEventsCollection).CountDocuments(ctx, bson.D{{Key: "project_id", Value: "builder"}, {Key: "todo_id", Value: todo.ID}})
		if countErr != nil {
			t.Fatal(countErr)
		}
		request := httptest.NewRequest(http.MethodPost, pathRoot+suffix, bytes.NewReader(body))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		var rejection errorResponse
		if err := json.Unmarshal(response.Body.Bytes(), &rejection); err != nil {
			t.Fatalf("%s content rejection was not JSON: status=%d body=%s err=%v", name, response.Code, response.Body.String(), err)
		}
		if response.Code != status || !strings.Contains(rejection.Teach, "TEACH: "+field+" ") {
			t.Fatalf("%s returned wrong content rejection: status=%d want=%d teach=%q body=%s", name, response.Code, status, rejection.Teach, response.Body.String())
		}
		after, readErr := store.ReadTODO(ctx, "company-prototype", "builder", todo.ID, nil)
		if readErr != nil {
			t.Fatal(readErr)
		}
		afterEvents, countErr := database.Collection(todoEventsCollection).CountDocuments(ctx, bson.D{{Key: "project_id", Value: "builder"}, {Key: "todo_id", Value: todo.ID}})
		if countErr != nil {
			t.Fatal(countErr)
		}
		if after.TODORevision != before.TODORevision || after.TODO.Stage != before.TODO.Stage || afterEvents != beforeEvents {
			t.Fatalf("%s content rejection mutated TODO/history: before=%d/%s/%d after=%d/%s/%d", name, before.TODORevision, before.TODO.Stage, beforeEvents, after.TODORevision, after.TODO.Stage, afterEvents)
		}
	}
	// Force a cache-hit race: the snapshot observes the cached revision, then
	// the remote main head advances before the final currentness fence.
	advancedHead := writeCatalog(true)
	reader.sha = catalog.Revision
	headMoveObserved := false
	reader.afterHead = func() {
		headMoveObserved = true
		reader.sha = advancedHead
	}
	assertRejected("cache-hit main movement", baseBody("prototype-cache-hit-head-race", claim), http.StatusConflict, "prototype_delivery.currentness")
	if !headMoveObserved {
		t.Fatal("cache-hit main movement hook did not run during the transition")
	}
	reader.sha = advancedHead
	catalog, err = store.prototypeSnapshot(ctx, "company-prototype", "builder", "")
	if err != nil {
		t.Fatalf("refresh observation after cache-hit race: %v", err)
	}
	// Force an uncached Git open and move main immediately after the fake
	// source is pinned. The miss path must run its final fence too.
	missHead := writeCatalog(true)
	reader.sha = missHead
	reader.afterOpen = func() { reader.sha = writeCatalog(true) }
	if _, err := store.prototypeSnapshot(ctx, "company-prototype", "builder", ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("cache-miss observation did not reject main movement: %v", err)
	}
	catalog, err = store.prototypeSnapshot(ctx, "company-prototype", "builder", "")
	if err != nil {
		t.Fatalf("refresh observation after cache-miss race: %v", err)
	}
	current, err = store.ReadTODO(ctx, "company-prototype", "builder", todo.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	claim = TODOPrototypeDeliveryClaim{ContentDigest: current.ContentDigest, Revision: catalog.Revision, InventoryDigest: catalog.InventoryDigest, BindingRevision: catalog.BindingRevision}
	reader.afterHead = func() { knowledgeChanged = true }
	assertRejected("Knowledge movement", baseBody("prototype-knowledge-race", claim), http.StatusConflict, "prototype_delivery.currentness")
	knowledgeChanged = false
	catalog, err = store.prototypeSnapshot(ctx, "company-prototype", "builder", "")
	if err != nil {
		t.Fatalf("refresh observation after Knowledge race: %v", err)
	}
	current, err = store.ReadTODO(ctx, "company-prototype", "builder", todo.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	claim = TODOPrototypeDeliveryClaim{ContentDigest: current.ContentDigest, Revision: catalog.Revision, InventoryDigest: catalog.InventoryDigest, BindingRevision: catalog.BindingRevision}
	// The catalog may be structurally valid while its referenced candidate is
	// archived; an archived candidate must not complete the approved TODO.
	candidateStatus = "archived"
	reader.sha = writeCatalog(true)
	archived, err := store.prototypeSnapshot(ctx, "company-prototype", "builder", "")
	if err != nil {
		t.Fatalf("read archived-candidate inventory: %v", err)
	}
	archivedClaim := TODOPrototypeDeliveryClaim{ContentDigest: current.ContentDigest, Revision: archived.Revision, InventoryDigest: archived.InventoryDigest, BindingRevision: archived.BindingRevision}
	assertRejected("archived candidate", baseBody("prototype-archived-candidate", archivedClaim), http.StatusConflict, "prototype_delivery.prototype_id")
	candidateStatus = "active"
	reader.sha = writeCatalog(true)
	invalidInventory = true
	reader.sha = writeCatalog(true)
	if _, err := store.prototypeSnapshot(ctx, "company-prototype", "builder", ""); err == nil {
		t.Fatal("invalid Prototype inventory passed admission")
	}
	invalidInventory = false
	reader.sha = writeCatalog(true)
	catalog, err = store.prototypeSnapshot(ctx, "company-prototype", "builder", "")
	if err != nil {
		t.Fatalf("restore active candidate inventory: %v", err)
	}
	current, err = store.ReadTODO(ctx, "company-prototype", "builder", todo.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	claim = TODOPrototypeDeliveryClaim{ContentDigest: current.ContentDigest, Revision: catalog.Revision, InventoryDigest: catalog.InventoryDigest, BindingRevision: catalog.BindingRevision}
	for name, scenario := range map[string]struct {
		mutate func(map[string]any)
		status int
		field  string
	}{
		"wrong content digest": {func(body map[string]any) {
			body["prototype_delivery"].(map[string]any)["content_digest"] = "sha256:" + strings.Repeat("f", 64)
		}, http.StatusConflict, "prototype_delivery"},
		"wrong sha": {func(body map[string]any) {
			body["prototype_delivery"].(map[string]any)["revision"] = strings.Repeat("e", 40)
		}, http.StatusConflict, "prototype_delivery"},
		"wrong inventory": {func(body map[string]any) {
			body["prototype_delivery"].(map[string]any)["inventory_digest"] = "sha256:" + strings.Repeat("e", 64)
		}, http.StatusConflict, "prototype_delivery"},
		"wrong binding": {func(body map[string]any) {
			body["prototype_delivery"].(map[string]any)["binding_revision"] = float64(claim.BindingRevision + 1)
		}, http.StatusConflict, "prototype_delivery.binding_revision"},
		"empty inline proof field":   {func(body map[string]any) { body["proof_refs"] = []any{} }, http.StatusConflict, "prototype_delivery/proof_refs/waiver_refs/local_proof/promotion_targets/knowledge_delivery"},
		"null inline proof field":    {func(body map[string]any) { body["proof_refs"] = nil }, http.StatusConflict, "prototype_delivery/proof_refs/waiver_refs/local_proof/promotion_targets/knowledge_delivery"},
		"empty waiver field":         {func(body map[string]any) { body["waiver_refs"] = []any{} }, http.StatusConflict, "prototype_delivery/proof_refs/waiver_refs/local_proof/promotion_targets/knowledge_delivery"},
		"null waiver field":          {func(body map[string]any) { body["waiver_refs"] = nil }, http.StatusConflict, "prototype_delivery/proof_refs/waiver_refs/local_proof/promotion_targets/knowledge_delivery"},
		"empty promotion field":      {func(body map[string]any) { body["promotion_targets"] = map[string]string{} }, http.StatusConflict, "prototype_delivery/proof_refs/waiver_refs/local_proof/promotion_targets/knowledge_delivery"},
		"null promotion field":       {func(body map[string]any) { body["promotion_targets"] = nil }, http.StatusConflict, "prototype_delivery/proof_refs/waiver_refs/local_proof/promotion_targets/knowledge_delivery"},
		"empty local proof field":    {func(body map[string]any) { body["local_proof"] = map[string]any{} }, http.StatusConflict, "prototype_delivery/proof_refs/waiver_refs/local_proof/promotion_targets/knowledge_delivery"},
		"null local proof field":     {func(body map[string]any) { body["local_proof"] = nil }, http.StatusConflict, "prototype_delivery/proof_refs/waiver_refs/local_proof/promotion_targets/knowledge_delivery"},
		"forged Knowledge field":     {func(body map[string]any) { body["knowledge_delivery"] = map[string]any{} }, http.StatusConflict, "prototype_delivery/proof_refs/waiver_refs/local_proof/promotion_targets/knowledge_delivery"},
		"null Knowledge field":       {func(body map[string]any) { body["knowledge_delivery"] = nil }, http.StatusConflict, "prototype_delivery/proof_refs/waiver_refs/local_proof/promotion_targets/knowledge_delivery"},
		"empty Knowledge acceptance": {func(body map[string]any) { body["knowledge_final_acceptance"] = map[string]any{} }, http.StatusConflict, "prototype_delivery/proof_refs/waiver_refs/local_proof/promotion_targets/knowledge_delivery"},
		"null Knowledge acceptance":  {func(body map[string]any) { body["knowledge_final_acceptance"] = nil }, http.StatusConflict, "prototype_delivery/proof_refs/waiver_refs/local_proof/promotion_targets/knowledge_delivery"},
		"missing binding revision":   {func(body map[string]any) { delete(body["prototype_delivery"].(map[string]any), "binding_revision") }, http.StatusBadRequest, "prototype_delivery.binding_revision"},
		"unknown tuple field":        {func(body map[string]any) { body["prototype_delivery"].(map[string]any)["unexpected"] = true }, http.StatusBadRequest, "prototype_delivery.unexpected"},
		"wrong Project context":      {func(body map[string]any) { body["project_id"] = "other-project" }, http.StatusConflict, "project_id/todo_id"},
	} {
		claimJSON, _ := json.Marshal(claim)
		var claimMap map[string]any
		_ = json.Unmarshal(claimJSON, &claimMap)
		body := map[string]any{"project_id": "builder", "todo_id": todo.ID, "expected_revision": current.TODORevision, "operation_id": "prototype-reject-" + strings.ReplaceAll(name, " ", "-"), "requested_stage": "Completed", "prototype_delivery": claimMap}
		scenario.mutate(body)
		data, _ := json.Marshal(body)
		assertRejected(name, data, scenario.status, scenario.field)
	}
	for _, stage := range []string{"Local-Implemented", "Promotion-Lane"} {
		body := map[string]any{"project_id": "builder", "todo_id": todo.ID, "expected_revision": current.TODORevision, "operation_id": "prototype-forbidden-" + strings.ToLower(strings.ReplaceAll(stage, "-", "")), "requested_stage": stage}
		data, _ := json.Marshal(body)
		field := "requested_stage"
		status := http.StatusConflict
		if stage == "Promotion-Lane" {
			field = "promotion_targets"
			status = http.StatusBadRequest
		}
		assertRejected("forbidden Prototype stage "+stage, data, status, field)
	}
	promotionPayload := map[string]any{"project_id": "builder", "todo_id": todo.ID, "expected_revision": current.TODORevision, "operation_id": "prototype-promotion-with-target", "requested_stage": "Promotion-Lane", "promotion_targets": map[string]string{"belluga/test": "stage"}}
	promotionPayloadJSON, _ := json.Marshal(promotionPayload)
	assertRejected("Prototype Promotion-Lane with required target", promotionPayloadJSON, http.StatusConflict, "prototype_delivery/proof_refs/waiver_refs/local_proof/promotion_targets/knowledge_delivery")
	precompletionAuthored, err := store.contentAtRevision(ctx, database, "builder", todo.ID, *current.ContentRevision)
	if err != nil {
		t.Fatal(err)
	}
	precompletionAuthored.TODOContent = validTODOContent()
	precompletionAuthored.Delivery = &TODODeliveryContract{Kind: "prototype", PrototypeID: "main-prototype"}
	precompletionAuthored.Scope = &TODOContentScope{Included: []string{"prototypes/main-prototype"}, Excluded: []string{"generated/**"}}
	precompletionAuthored.Decisions = []TODOContentItem{{ID: "decision-one", Text: "Keep the observation tuple server-authored."}}
	precompletionAuthored.Risks = []TODOContentRisk{{ID: "risk-one", Text: "A stale publication could be observed.", Mitigation: "Fence the observed main revision."}}
	precompletionAuthored.References = []TODOContentReference{{Kind: "contract", Ref: "prototype main delivery"}}
	baseContentJSON, err := json.Marshal(precompletionAuthored)
	if err != nil {
		t.Fatal(err)
	}
	var cleanBase TODOAuthoredContent
	if err := json.Unmarshal(baseContentJSON, &cleanBase); err != nil {
		t.Fatalf("unmarshal clean base content fixture: %v", err)
	}
	if err := ValidateTODOContent(cleanBase.TODOContent, false); err != nil {
		t.Fatalf("clean base content fixture is invalid before mixed-field mutation: %v", err)
	}
	for name, mutate := range map[string]func(*TODOAuthoredContent){
		"diff expectation": func(value *TODOAuthoredContent) {
			value.DiffExpectation = []TODORepositoryScope{{Repository: "Builder", RepositoryID: "belluga/test", DeliveryBranch: "r-test", Baseline: strings.Repeat("a", 40), ExpectedPaths: []string{"go-app/**"}, NotExpectedPaths: []string{}}}
		},
		"affected files": func(value *TODOAuthoredContent) { value.AffectedFiles = []string{"go-app/internal/registry/main.go"} },
		"local checks": func(value *TODOAuthoredContent) {
			value.LocalChecks = []TODOLocalCheck{{ID: "run-check", RepositoryID: "belluga/test", Argv: []string{"go", "test"}, WorkingDirectory: ".", InputPaths: []string{"go-app"}}}
		},
	} {
		request := TODOContentEditRequest{ProjectID: "builder", TODOID: todo.ID, ExpectedRevision: &current.TODORevision, OperationID: "prototype-mixed-" + strings.ReplaceAll(name, " ", "-"), Content: precompletionAuthored, Reason: "Reject cross-kind code contract"}
		mutate(&request.Content)
		data, _ := json.Marshal(request)
		assertMutationRejected(name, "/content", data, http.StatusConflict, "content.delivery")
	}
	knowledgeTargetRequest := TODOContentEditRequest{ProjectID: "builder", TODOID: todo.ID, ExpectedRevision: &current.TODORevision, OperationID: "prototype-mixed-knowledge-target", Content: precompletionAuthored, Reason: "Reject a Knowledge target on a Prototype contract"}
	knowledgeTargetJSON, _ := json.Marshal(knowledgeTargetRequest)
	knowledgeTargetJSON = []byte(strings.Replace(string(knowledgeTargetJSON), `"prototype_id":"main-prototype"`, `"prototype_id":"main-prototype","targets":[{"family":"module","record_id":"projects","generation_id":"g1","baseline_revision":1,"change_kind":"edit"}]`, 1))
	assertMutationRejected("Knowledge targets", "/content", knowledgeTargetJSON, http.StatusBadRequest, "content.delivery.targets")
	evidenceRequest := TODOEvidenceEditRequest{ProjectID: "builder", TODOID: todo.ID, ExpectedRevision: &current.TODORevision, OperationID: "prototype-mixed-implementation-commit", ProofReferences: current.TODO.ProofReferences, WaiverReferences: current.TODO.WaiverReferences, ImplementationCommits: []TODOCommitReference{{Repository: "Builder", Commit: strings.Repeat("b", 40), SourceDigest: current.ContentDigest, VerificationState: "unverified"}}, RecordSections: recordRecordSections(current.TODO), Reason: "Reject code commit evidence on a Prototype TODO"}
	evidenceJSON, _ := json.Marshal(evidenceRequest)
	assertMutationRejected("implementation commits", "/evidence", evidenceJSON, http.StatusConflict, "content.delivery")
	body := baseBody("prototype-completed", claim)
	// Move main after the full currentness fence and server proof timestamp/
	// encoding, but immediately before the aggregate/event writer. The stored
	// tuple must remain a truthful historical observation.
	postObservationHead := ""
	store.afterPrototypeObservation = func() {
		postObservationHead = writeCatalog(true)
		reader.sha = postObservationHead
	}
	response := post(body)
	store.afterPrototypeObservation = nil
	if response.Code != http.StatusOK {
		t.Fatalf("direct Prototype Completed HTTP transition: status=%d body=%s", response.Code, response.Body.String())
	}
	completed, err := store.ReadTODO(ctx, "company-prototype", "builder", todo.ID, nil)
	if err != nil || completed.TODO.Stage != "Completed" || completed.TODO.StageVerification != "verified" || completed.TODO.VerifiedTransitionRef == nil || completed.TODO.VerifiedTransitionRef.Kind != "prototype-delivery" || completed.TODO.VerifiedTransitionRef.SourceDigest != completed.ContentDigest {
		t.Fatalf("Completed readback lost server proof identity: response=%+v err=%v", completed, err)
	}
	var proof TODOPrototypeDeliveryProof
	proof, err = decodePrototypeDeliveryProof(completed.TODO.VerifiedTransitionRef.Reference)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodePrototypeDeliveryProof(completed.TODO.VerifiedTransitionRef.Reference + "x"); err == nil {
		t.Fatal("tampered server proof encoding was accepted")
	}
	oversized := proof
	oversized.KnowledgeGeneration = strings.Repeat("g", maxPrototypeDeliveryProofBytes)
	if _, err := encodePrototypeDeliveryProof(oversized); err == nil {
		t.Fatal("oversized server proof escaped the 8 KiB bound")
	}
	if proof.Version != prototypeDeliveryProofVersion || proof.CompanyID != "company-prototype" || proof.ProjectID != "builder" || proof.TODOID != todo.ID || proof.ContentDigest != current.ContentDigest || proof.PrototypeID != "main-prototype" || proof.RepositoryID != catalog.RepositoryID || proof.Revision != catalog.Revision || proof.InventoryDigest != catalog.InventoryDigest || proof.BindingRevision != catalog.BindingRevision || proof.PublicationBranch != catalog.PublicationBranch || proof.EvaluatorVersion != catalog.EvaluatorVersion || proof.KnowledgeGeneration != catalog.KnowledgeGeneration || proof.KnowledgeDigest != catalog.KnowledgeDigest || proof.VerifiedAt.IsZero() || postObservationHead == "" || postObservationHead == proof.Revision {
		t.Fatalf("server proof tuple does not match observation: proof=%+v catalog=%+v postObservationHead=%q", proof, catalog, postObservationHead)
	}
	asOf, err := store.ReadTODOAsOf(ctx, "company-prototype", "builder", todo.ID, completed.TODORevision)
	if err != nil {
		t.Fatalf("Completed as-of readback rejected immutable proof/event: %v", err)
	}
	if asOf.State.VerifiedTransitionRef == nil || asOf.State.VerifiedTransitionRef.Reference != completed.TODO.VerifiedTransitionRef.Reference {
		t.Fatal("Completed as-of history does not preserve the canonical proof bytes")
	}
	var completedEvent TODOHistoryEvent
	if err := database.Collection(todoEventsCollection).FindOne(ctx, bson.D{{Key: "project_id", Value: "builder"}, {Key: "todo_id", Value: todo.ID}, {Key: "revision", Value: completed.TODORevision}}).Decode(&completedEvent); err != nil {
		t.Fatalf("read immutable Completed event: %v", err)
	}
	if completedEvent.Changes.VerifiedTransitionRef == nil || completedEvent.Changes.VerifiedTransitionRef.Reference != completed.TODO.VerifiedTransitionRef.Reference {
		t.Fatal("Completed event does not preserve the canonical proof bytes")
	}
	completedRevision := completed.TODORevision
	proofReference := completed.TODO.VerifiedTransitionRef.Reference
	reader.sha = writeCatalog(true)
	afterMovement, err := store.ReadTODO(ctx, "company-prototype", "builder", todo.ID, nil)
	if err != nil || afterMovement.TODO.Stage != "Completed" || afterMovement.TODO.VerifiedTransitionRef == nil || afterMovement.TODO.VerifiedTransitionRef.Reference != proofReference {
		t.Fatalf("later main movement rewrote or reopened historical completion: %+v %v", afterMovement, err)
	}
	var forgedRetry map[string]any
	_ = json.Unmarshal(body, &forgedRetry)
	forgedRetry["proof_refs"] = []any{}
	forgedRetryBody, _ := json.Marshal(forgedRetry)
	assertRejected("retry with forged proof field", forgedRetryBody, http.StatusConflict, "prototype_delivery/proof_refs/waiver_refs/local_proof/promotion_targets/knowledge_delivery")
	eventsBeforeRetry, err := database.Collection(todoEventsCollection).CountDocuments(ctx, bson.D{{Key: "project_id", Value: "builder"}, {Key: "todo_id", Value: todo.ID}})
	if err != nil {
		t.Fatal(err)
	}
	retry := post(body)
	var retried TODOActionResponse
	if retry.Code != http.StatusOK || json.Unmarshal(retry.Body.Bytes(), &retried) != nil || retried.Stage != "Completed" || !retried.NoOp {
		t.Fatalf("idempotent Completed retry changed proof: status=%d body=%s", retry.Code, retry.Body.String())
	}
	retriedTODO, readErr := store.ReadTODO(ctx, "company-prototype", "builder", todo.ID, nil)
	if readErr != nil || retriedTODO.TODO.VerifiedTransitionRef == nil || retriedTODO.TODO.VerifiedTransitionRef.Reference != proofReference {
		t.Fatalf("idempotent Completed retry changed stored proof: %+v err=%v", retriedTODO, readErr)
	}
	eventsAfterRetry, err := database.Collection(todoEventsCollection).CountDocuments(ctx, bson.D{{Key: "project_id", Value: "builder"}, {Key: "todo_id", Value: todo.ID}})
	if err != nil || eventsAfterRetry != eventsBeforeRetry {
		t.Fatalf("idempotent Completed retry wrote another history event: before=%d after=%d err=%v", eventsBeforeRetry, eventsAfterRetry, err)
	}
	var changedRetry map[string]any
	_ = json.Unmarshal(body, &changedRetry)
	changedRetry["prototype_delivery"].(map[string]any)["inventory_digest"] = "sha256:" + strings.Repeat("d", 64)
	changedRetryBody, _ := json.Marshal(changedRetry)
	assertRejected("same operation with changed tuple", changedRetryBody, http.StatusConflict, "operation_id")
	var staleCAS map[string]any
	_ = json.Unmarshal(body, &staleCAS)
	staleCAS["expected_revision"] = completedRevision + 1
	staleCAS["operation_id"] = "prototype-stale-cas"
	staleCASBody, _ := json.Marshal(staleCAS)
	staleBefore, err := store.ReadTODO(ctx, "company-prototype", "builder", todo.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	staleEventsBefore, err := database.Collection(todoEventsCollection).CountDocuments(ctx, bson.D{{Key: "project_id", Value: "builder"}, {Key: "todo_id", Value: todo.ID}})
	if err != nil {
		t.Fatal(err)
	}
	staleResponse := post(staleCASBody)
	var staleRejection errorResponse
	if err := json.Unmarshal(staleResponse.Body.Bytes(), &staleRejection); err != nil || staleResponse.Code != http.StatusConflict || staleRejection.TODORevision == nil || *staleRejection.TODORevision != completedRevision || !strings.Contains(staleRejection.Teach, "expected_revision=") {
		t.Fatalf("stale aggregate revision rejection is imprecise: status=%d teach=%q todo_revision=%v err=%v", staleResponse.Code, staleRejection.Teach, staleRejection.TODORevision, err)
	}
	staleAfter, err := store.ReadTODO(ctx, "company-prototype", "builder", todo.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	staleEventsAfter, err := database.Collection(todoEventsCollection).CountDocuments(ctx, bson.D{{Key: "project_id", Value: "builder"}, {Key: "todo_id", Value: todo.ID}})
	if err != nil || staleAfter.TODORevision != staleBefore.TODORevision || staleAfter.TODO.Stage != staleBefore.TODO.Stage || staleEventsAfter != staleEventsBefore {
		t.Fatalf("stale aggregate revision rejection mutated TODO/history: before=%d/%s/%d after=%d/%s/%d err=%v", staleBefore.TODORevision, staleBefore.TODO.Stage, staleEventsBefore, staleAfter.TODORevision, staleAfter.TODO.Stage, staleEventsAfter, err)
	}
	authored, err := store.contentAtRevision(ctx, database, "builder", todo.ID, completedRevision)
	if err != nil {
		t.Fatal(err)
	}
	noOp, err := store.EditTODOContent(ctx, "company-prototype", TODOContentEditRequest{ProjectID: "builder", TODOID: todo.ID, ExpectedRevision: &completed.TODORevision, OperationID: "prototype-content-noop", Content: authored, Reason: "Confirm identical prototype contract"})
	if err != nil || !noOp.NoOp {
		t.Fatalf("identical content edit should be a no-op: %+v %v", noOp, err)
	}
	completed, err = store.ReadTODO(ctx, "company-prototype", "builder", todo.ID, nil)
	if err != nil || completed.TODO.Stage != "Completed" || completed.ContentDigest != proof.ContentDigest {
		t.Fatalf("no-op content edit reopened or replaced the proof: %+v %v", completed, err)
	}
	mixedContent := authored
	mixedContent.LocalChecks = []TODOLocalCheck{{ID: "check-one", RepositoryID: "belluga/test", Argv: []string{"go", "test"}, WorkingDirectory: ".", InputPaths: []string{"go-app"}}}
	if _, err := store.EditTODOContent(ctx, "company-prototype", TODOContentEditRequest{ProjectID: "builder", TODOID: todo.ID, ExpectedRevision: &completed.TODORevision, OperationID: "prototype-mixed-content", Content: mixedContent, Reason: "Reject code execution contract on Prototype TODO"}); err == nil {
		t.Fatal("Prototype content edit accepted a code local-check contract")
	}
	if _, err := store.EditTODOEvidence(ctx, "company-prototype", TODOEvidenceEditRequest{ProjectID: "builder", TODOID: todo.ID, ExpectedRevision: &completed.TODORevision, OperationID: "prototype-mixed-evidence", ProofReferences: completed.TODO.ProofReferences, WaiverReferences: completed.TODO.WaiverReferences, ImplementationCommits: []TODOCommitReference{{}}, RecordSections: recordRecordSections(completed.TODO), Reason: "Reject mixed code evidence on Prototype TODO"}); err == nil {
		t.Fatal("Prototype evidence edit accepted a code implementation claim")
	}
	changed := authored
	changed.Objective = "Clarify the approved Prototype outcome after delivery."
	changed.Planning.Reviews[0].Reference = "review/prototype-critique-v2"
	edited, err := store.EditTODOContent(ctx, "company-prototype", TODOContentEditRequest{ProjectID: "builder", TODOID: todo.ID, ExpectedRevision: &completed.TODORevision, OperationID: "prototype-content-edit", Content: changed, Reason: "Clarify the approved outcome"})
	if err != nil || edited.NoOp || edited.ContentDigest == proof.ContentDigest {
		t.Fatalf("substantive content edit did not create a new digest: %+v %v", edited, err)
	}
	reopened, err := store.ReadTODO(ctx, "company-prototype", "builder", todo.ID, nil)
	if err != nil || reopened.TODO.Stage != "Planning" || reopened.ContentDigest != edited.ContentDigest {
		t.Fatalf("content edit did not reopen Planning: %+v %v", reopened, err)
	}
	if historical, err := store.ReadTODOAsOf(ctx, "company-prototype", "builder", todo.ID, completedRevision); err != nil || historical.State.Stage != "Completed" || historical.State.VerifiedTransitionRef == nil || historical.State.VerifiedTransitionRef.Reference != completed.TODO.VerifiedTransitionRef.Reference {
		t.Fatalf("content reopen lost immutable Completed history: %+v %v", historical.State, err)
	}
	// A changed authored digest requires fresh review/evidence, approval, and a
	// new server observation before the second direct completion.
	current, err = store.ReadTODO(ctx, "company-prototype", "builder", todo.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	secondRefs := []TODOProofReference{
		{Kind: "planning_review_critique", Source: "human-review", Reference: "review/prototype-critique-v2", SourceDigest: current.ContentDigest, VerificationState: "unverified", Outcome: "go"},
		{Kind: "criterion:criterion-one", Source: "external-evidence", Reference: "evidence/criterion-one-v2", SourceDigest: current.ContentDigest, VerificationState: "unverified"},
		{Kind: "scenario:scenario-one", Source: "external-evidence", Reference: "evidence/scenario-one-v2", SourceDigest: current.ContentDigest, VerificationState: "unverified"},
		{Kind: "final_review", Source: "human-review", Reference: "review/prototype-final-v2", SourceDigest: current.ContentDigest, VerificationState: "unverified", Outcome: "go"},
	}
	if _, err := store.EditTODOEvidence(ctx, "company-prototype", TODOEvidenceEditRequest{ProjectID: "builder", TODOID: todo.ID, ExpectedRevision: &current.TODORevision, OperationID: "prototype-evidence-v2", ProofReferences: secondRefs, WaiverReferences: []TODOProofReference{}, ImplementationCommits: []TODOCommitReference{}, RecordSections: recordRecordSections(current.TODO), Reason: "Record evidence for the revised prototype contract"}); err != nil {
		t.Fatalf("record evidence for revised content: %v", err)
	}
	advance("prototype-planning-v2", "Implementation-Approval-Pending", false)
	advance("prototype-approved-v2", "Ready-for-Implementation", true)
	advance("prototype-started-v2", "In-Progress", false)
	catalog, err = store.prototypeSnapshot(ctx, "company-prototype", "builder", "")
	if err != nil {
		t.Fatalf("observe revised prototype delivery: %v", err)
	}
	current, err = store.ReadTODO(ctx, "company-prototype", "builder", todo.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	secondClaim := TODOPrototypeDeliveryClaim{ContentDigest: current.ContentDigest, Revision: catalog.Revision, InventoryDigest: catalog.InventoryDigest, BindingRevision: catalog.BindingRevision}
	secondBody := baseBody("prototype-completed-v2", secondClaim)
	if response := post(secondBody); response.Code != http.StatusOK {
		t.Fatalf("reapproved revised direct completion: status=%d body=%s", response.Code, response.Body.String())
	}
	secondCompleted, err := store.ReadTODO(ctx, "company-prototype", "builder", todo.ID, nil)
	if err != nil || secondCompleted.TODO.Stage != "Completed" || secondCompleted.ContentDigest != edited.ContentDigest || secondCompleted.TODO.VerifiedTransitionRef == nil || secondCompleted.TODO.VerifiedTransitionRef.SourceDigest == proof.ContentDigest {
		t.Fatalf("revised completion did not persist a fresh proof: %+v %v", secondCompleted, err)
	}
}
