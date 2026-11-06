package registry

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestSameLifecycleMigrationTreatsReorderedJSONAsEquivalent(t *testing.T) {
	expected := &LifecycleMigration{
		State:       "complete",
		ProjectIDs:  []string{"boora", "builder"},
		Excluded:    []LifecycleMigrationDiagnostic{{ProjectID: "builder", TODOID: "todo-a", Path: "todos/active/todo-a.md", Message: "excluded at cutover"}},
		StartedAt:   "2026-10-31T12:00:00Z",
		CompletedAt: "2026-10-31T12:30:00Z",
	}
	reordered := json.RawMessage(`{
		"completed_at": "2026-10-31T12:30:00Z",
		"excluded": [{ "message": "excluded at cutover", "path": "todos/active/todo-a.md", "todo_id": "todo-a", "project_id": "builder" }],
		"project_ids": ["boora", "builder"],
		"state": "complete",
		"started_at": "2026-10-31T12:00:00Z"
	}`)
	if !sameLifecycleMigration(reordered, expected) {
		t.Fatalf("reordered semantically equal lifecycle migration was not accepted: %s", reordered)
	}
	if sameLifecycleMigration(json.RawMessage(`{"state":"incomplete"}`), expected) {
		t.Fatal("semantically different lifecycle migration was accepted")
	}
	if sameLifecycleMigration(json.RawMessage(`not-json`), expected) {
		t.Fatal("malformed lifecycle JSON was accepted")
	}
	if sameLifecycleMigration(reordered, nil) {
		t.Fatal("nil expected lifecycle migration was accepted")
	}
}

func validTestBinding() Binding {
	return Binding{WorkspaceRoot: "/workspace", ProjectRoot: "project", FoundationRoot: "foundation", RepositoryID: "belluga/test", PublicationBranch: "main"}
}

func registerTestProjectWithBinding(ctx context.Context, store *MongoStore, input Registration) (Project, error) {
	binding := input.Binding
	input.Binding = Binding{}
	project, err := store.Register(ctx, input)
	if err != nil || binding == (Binding{}) {
		return project, err
	}
	identityDigest := sha256.Sum256([]byte(input.CompanyID + "/" + input.ProjectID))
	return store.BindFirst(ctx, input.CompanyID, input.ProjectID, FirstBindingRequest{
		ProjectID:        input.ProjectID,
		ExpectedRevision: 0,
		OperationID:      fmt.Sprintf("test-bind-%x", identityDigest[:24]),
		Binding:          binding,
	})
}

func TestProjectDocumentIsFlatWithObjectIDBindingAndBSONDate(t *testing.T) {
	project := Project{ID: "shared", CompanyID: "company-a", Name: "Shared", Binding: ptrTestBinding(validTestBinding()), RegisteredAt: "2026-10-30T12:34:56.123456789Z", PreparationState: "not_ready"}
	objectID := bson.NewObjectID()
	document, err := projectToDocument(project, objectID)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := bson.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	var raw bson.M
	if err := bson.Unmarshal(encoded, &raw); err != nil {
		t.Fatal(err)
	}
	if raw["_id"] != objectID {
		t.Fatalf("_id = %T %v", raw["_id"], raw["_id"])
	}
	if _, found := raw["data"]; found {
		t.Fatal("generic data wrapper present")
	}
	if _, found := raw["artifacts"]; found {
		t.Fatal("artifact inventory copied into Project")
	}
	if _, found := raw["binding"]; !found {
		t.Fatal("fixed binding missing from Project")
	}
	if _, ok := raw["registered_at"].(bson.DateTime); !ok {
		t.Fatalf("registered_at is %T, want BSON Date", raw["registered_at"])
	}
	if _, found := raw["new_todo_creation_authority"]; found {
		t.Fatal("Project persisted a per-Project Roadmap authority switch")
	}
	api := document.toAPI()
	if api.ID != project.ID || api.CompanyID != project.CompanyID || api.Binding == nil || *api.Binding != *project.Binding {
		t.Fatalf("Project round trip changed identity or binding: %+v", api)
	}
	want := time.Date(2026, 10, 30, 12, 34, 56, 123000000, time.UTC)
	got, err := time.Parse(time.RFC3339Nano, api.RegisteredAt)
	if err != nil || !got.Equal(want) {
		t.Fatalf("registered_at = %s, %v; want %s", api.RegisteredAt, err, want)
	}
}

func TestProjectSerializationHasNoPerProjectRoadmapAuthority(t *testing.T) {
	project := Project{ID: "builder", CompanyID: "belluga", Name: "Builder", Binding: ptrTestBinding(validTestBinding()), RegisteredAt: "2026-10-30T12:34:56Z", PreparationState: "not_ready"}
	document, err := projectToDocument(project, bson.NewObjectID())
	if err != nil {
		t.Fatal(err)
	}
	legacyBinding := validTestBinding()
	legacy := projectDocument{ID: "builder", CompanyID: "belluga", Binding: &legacyBinding, RegisteredAt: time.Now().UTC()}
	encoded, err := bson.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	var raw bson.M
	if err := bson.Unmarshal(encoded, &raw); err != nil {
		t.Fatal(err)
	}
	if _, found := raw["new_todo_creation_authority"]; found {
		t.Fatal("Project BSON includes per-Project Roadmap authority")
	}
	if api := legacy.toAPI(); api.ID != "builder" || api.CompanyID != "belluga" {
		t.Fatalf("legacy Project did not retain identity: %+v", api)
	}
}

func TestSnapshotReferenceDoesNotAcceptArtifactInventory(t *testing.T) {
	data := []byte(`{"company_id":"company-a","binding_digest":"sha256:abc","fingerprint":"abc","artifacts":[{"id":"landing"}]}`)
	var reference SnapshotReference
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&reference); err == nil {
		t.Fatal("snapshot reference accepted artifact inventory")
	}
}

func TestCompanyTenantIdentifiersAreNormalizedAndUnique(t *testing.T) {
	company, err := normalizeCompany(Company{ID: "company-a", Name: "Company", Domains: []string{"Example.COM."}, AppIDs: []AppIdentifier{{Platform: "app_android", Value: "COM.Example.App"}, {Platform: "app_ios", Value: "COM.Example.App"}}})
	if err != nil || len(company.DomainKeys) != 1 || company.DomainKeys[0] != "host:example.com" || len(company.AppKeys) != 2 || company.AppIDs[0].Value != "com.example.app" {
		t.Fatalf("normalized identifiers = %+v, %v", company, err)
	}
	if _, err := normalizeCompany(Company{ID: "company-a", Name: "Company", Domains: []string{"example.com", "EXAMPLE.COM"}}); err != ErrInvalid {
		t.Fatalf("duplicate normalized domain accepted: %v", err)
	}
}

func ptrTestBinding(value Binding) *Binding { return &value }

func TestRepositoryBindingValidationAndPublicProjection(t *testing.T) {
	for _, branch := range []string{"main", "release/r-11", "feature.with.dot"} {
		if !validGitBranch(branch) {
			t.Errorf("valid branch rejected: %q", branch)
		}
	}
	for _, branch := range []string{"", "-bad", "/bad", "bad/", "a..b", "a//b", "a/.hidden", "a./b", "a/b.lock", "a@{b", "a b", strings.Repeat("a", 256)} {
		if validGitBranch(branch) {
			t.Errorf("invalid branch accepted: %q", branch)
		}
	}
	if validGitRepositoryIdentity("belluga") || validGitRepositoryIdentity("belluga/../repo") || validGitRepositoryIdentity("belluga/repo extra") || !validGitRepositoryIdentity("belluga/repo") {
		t.Fatal("repository identity validation failed")
	}
	project := Project{Binding: ptrTestBinding(Binding{WorkspaceRoot: "/private/workspace", ProjectRoot: "project", FoundationRoot: "foundation", RepositoryID: "belluga/repo", PublicationBranch: "main", DockerRepositoryID: "belluga/docker"}), BindingRevision: 9}
	projection := repositoryBindingFromProject(project)
	if projection.State != "configured" || projection.Revision != 9 || projection.RepositoryID != "belluga/repo" || projection.PublicationBranch != "main" {
		t.Fatalf("public repository projection: %+v", projection)
	}
	encoded, _ := json.Marshal(projection)
	if strings.Contains(string(encoded), "private") || strings.Contains(string(encoded), "foundation_root") || strings.Contains(string(encoded), "docker") {
		t.Fatalf("private binding metadata leaked: %s", encoded)
	}
	project.Binding = nil
	projection = repositoryBindingFromProject(project)
	if projection.State != "absent" || projection.Revision != 9 || projection.RepositoryID != "" {
		t.Fatalf("absent repository projection: %+v", projection)
	}
}

func TestRegisterRejectsRepositoryBindingAndDirectsToCASAPI(t *testing.T) {
	store := &MongoStore{}
	_, err := store.Register(context.Background(), Registration{CompanyID: "company-a", CompanyName: "Company A", ProjectID: "project-a", ProjectName: "Project A", Binding: validTestBinding()})
	if !errors.Is(err, ErrInvalid) || err == nil || !strings.Contains(err.Error(), "TEACH:") || !strings.Contains(err.Error(), "compare-and-set repository binding API") {
		t.Fatalf("Register(binding) error = %v; want actionable rejection", err)
	}
}

func TestMongoRepositoryBindingConcurrentIdempotencyAndConflict(t *testing.T) {
	uri := os.Getenv("REGISTRY_TEST_MONGODB_URI")
	if uri == "" {
		t.Skip("isolated MongoDB fixture not configured")
	}
	ctx := context.Background()
	stamp := fmt.Sprintf("test_binding_cas_%d", time.Now().UnixNano())
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
	for _, projectID := range []string{"same-operation", "distinct-operation"} {
		if _, err := store.Register(ctx, Registration{CompanyID: "company-a", CompanyName: "Company A", ProjectID: projectID, ProjectName: projectID}); err != nil {
			t.Fatal(err)
		}
	}
	type result struct {
		binding RepositoryBinding
		err     error
	}
	concurrent := func(requests []RepositoryBindingRequest) []result {
		t.Helper()
		start := make(chan struct{})
		results := make(chan result, len(requests))
		var wg sync.WaitGroup
		for _, request := range requests {
			request := request
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				binding, err := store.ChangeRepositoryBinding(ctx, "company-a", request.ProjectID, request)
				results <- result{binding: binding, err: err}
			}()
		}
		close(start)
		wg.Wait()
		close(results)
		out := make([]result, 0, len(requests))
		for item := range results {
			out = append(out, item)
		}
		return out
	}
	same := RepositoryBindingRequest{Action: "bind", ProjectID: "same-operation", ExpectedRevision: 0, OperationID: "same-op", Actor: "owner", RepositoryID: "belluga/shared", Branch: "main"}
	sameResults := concurrent([]RepositoryBindingRequest{same, same})
	if len(sameResults) != 2 || sameResults[0].err != nil || sameResults[1].err != nil || sameResults[0].binding != sameResults[1].binding || sameResults[0].binding.Revision != 1 {
		t.Fatalf("concurrent exact retries did not converge idempotently: %+v", sameResults)
	}
	distinctA := RepositoryBindingRequest{Action: "bind", ProjectID: "distinct-operation", ExpectedRevision: 0, OperationID: "distinct-a", Actor: "owner", RepositoryID: "belluga/repo-a", Branch: "main"}
	distinctB := distinctA
	distinctB.OperationID = "distinct-b"
	distinctB.RepositoryID = "belluga/repo-b"
	distinctResults := concurrent([]RepositoryBindingRequest{distinctA, distinctB})
	succeeded, conflicted := 0, 0
	for _, item := range distinctResults {
		if item.err == nil && item.binding.Revision == 1 {
			succeeded++
		} else if errors.Is(item.err, ErrConflict) && strings.Contains(item.err.Error(), "TEACH:") {
			conflicted++
		} else {
			t.Fatalf("distinct concurrent operation returned unexpected result: %+v", item)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("distinct concurrent operations must have one winner and one actionable conflict: %+v", distinctResults)
	}
}

func TestMongoRegistrationIsCompanyScopedAndRetryable(t *testing.T) {
	uri := os.Getenv("REGISTRY_TEST_MONGODB_URI")
	if uri == "" {
		t.Skip("isolated MongoDB fixture not configured")
	}
	ctx := context.Background()
	stamp := fmt.Sprintf("test_%d", time.Now().UnixNano())
	store, err := OpenMongo(ctx, uri, stamp)
	if err != nil {
		t.Fatal(err)
	}
	store.tenantPrefix = stamp
	t.Cleanup(func() {
		for _, companyID := range []string{"company-a", "company-b", "company-empty", "company-contended"} {
			_ = store.client.Database(store.tenantDatabase(companyID)).Drop(context.Background())
		}
		_ = store.control.Drop(context.Background())
		_ = store.Close(context.Background())
	})
	register := func(companyID string) (Project, error) {
		return store.Register(ctx, Registration{CompanyID: companyID, CompanyName: companyID, ProjectID: "shared", ProjectName: "Shared"})
	}
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := register("company-a"); results <- err }()
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatalf("same-pair concurrent registration failed: %v", err)
		}
	}
	startConflict := make(chan struct{})
	type registrationResult struct {
		companyName string
		err         error
	}
	conflicts := make(chan registrationResult, 8)
	for index := range 8 {
		name := "Contended Company A"
		if index%2 == 1 {
			name = "Contended Company B"
		}
		go func(companyName string) {
			<-startConflict
			_, err := store.Register(ctx, Registration{CompanyID: "company-contended", CompanyName: companyName, ProjectID: "contended", ProjectName: "Contended"})
			conflicts <- registrationResult{companyName: companyName, err: err}
		}(name)
	}
	close(startConflict)
	winningName := ""
	conflictCount := 0
	for range 8 {
		result := <-conflicts
		if result.err == nil {
			if winningName != "" && winningName != result.companyName {
				t.Fatalf("conflicting Company identities both registered: %q and %q", winningName, result.companyName)
			}
			winningName = result.companyName
		} else if result.err == ErrConflict {
			conflictCount++
		} else {
			t.Fatalf("concurrent conflicting registration returned unexpected error: %v", result.err)
		}
	}
	company, err := store.company(ctx, "company-contended")
	if err != nil || winningName == "" || conflictCount == 0 || company.Name != winningName {
		t.Fatalf("conflicting registration did not converge on one Company identity: company=%+v winner=%q conflicts=%d err=%v", company, winningName, conflictCount, err)
	}
	contendedProject, err := store.Project(ctx, "company-contended", "contended")
	if err != nil || contendedProject.CompanyID != "company-contended" {
		t.Fatalf("winning registration did not produce exactly its scoped Project: project=%+v err=%v", contendedProject, err)
	}
	projectCount, err := store.client.Database(store.tenantDatabase("company-contended")).Collection(projectsCollection).CountDocuments(ctx, bson.D{})
	if err != nil || projectCount != 1 {
		t.Fatalf("contended registration wrote multiple Projects: count=%d err=%v", projectCount, err)
	}
	if _, err := register("company-b"); err != nil {
		t.Fatal(err)
	}
	for _, companyID := range []string{"company-a", "company-b"} {
		if _, err := store.BindFirst(ctx, companyID, "shared", FirstBindingRequest{ProjectID: "shared", ExpectedRevision: 0, OperationID: "register-test-binding-" + companyID, Binding: validTestBinding()}); err != nil {
			t.Fatalf("explicit first binding for %s: %v", companyID, err)
		}
	}
	projectB, err := store.Project(ctx, "company-b", "shared")
	if err != nil || projectB.ID != "shared" || projectB.CompanyID != "company-b" {
		t.Fatalf("registration did not return the registered Project: project=%+v err=%v", projectB, err)
	}
	company, err = normalizeCompany(Company{ID: "company-a", Name: "company-a", Domains: []string{"example.test"}, AppIDs: []AppIdentifier{{Platform: "app_android", Value: "com.example.app"}, {Platform: "app_ios", Value: "com.example.ios"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.control.Collection(companiesCollection).UpdateOne(ctx, bson.M{"id": "company-a"}, bson.M{"$set": bson.M{"domains": company.Domains, "domain_keys": company.DomainKeys, "app_ids": company.AppIDs, "app_keys": company.AppKeys}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.control.Collection(companiesCollection).UpdateOne(ctx, bson.M{"id": "company-b"}, bson.M{"$set": bson.M{"domain_keys": []string{"host:example.test"}}}); err == nil {
		t.Fatal("duplicate normalized domain crossed Companies")
	}
	for _, probe := range []TenantRequest{{Type: "host", Identifier: "EXAMPLE.TEST."}, {Type: "app_android", Identifier: "COM.EXAMPLE.APP"}, {Type: "app_ios", Identifier: "com.example.ios"}} {
		resolved, err := store.ResolveTenant(ctx, probe)
		if err != nil || resolved.CompanyID != "company-a" {
			t.Fatalf("tenant resolution = %+v, %v", resolved, err)
		}
	}
	if _, err := store.ResolveTenant(ctx, TenantRequest{Type: "host", Identifier: "unknown.test"}); err != ErrNotFound {
		t.Fatalf("unknown Host: %v", err)
	}
	if _, err := store.ResolveTenant(ctx, TenantRequest{Type: "host", Identifier: "example.test", CompanyID: "company-b"}); err != ErrConflict {
		t.Fatalf("mismatched Company: %v", err)
	}
	for _, companyID := range []string{"company-a", "company-b"} {
		project, err := store.Project(ctx, companyID, "shared")
		if err != nil || project.CompanyID != companyID {
			t.Fatalf("Project(%s) = %+v, %v", companyID, project, err)
		}
	}
	if _, err := store.Project(ctx, "company-a", "absent"); err != ErrNotFound {
		t.Fatalf("missing pair: %v", err)
	}
	if _, err := store.Project(ctx, "", "shared"); err != ErrInvalid {
		t.Fatalf("missing Company: %v", err)
	}
	store.beforeProjectWrite = func() error { return ErrUnavailable }
	_, failed := store.Register(ctx, Registration{CompanyID: "company-empty", CompanyName: "Empty", ProjectID: "first", ProjectName: "First"})
	store.beforeProjectWrite = nil
	if failed != ErrUnavailable {
		t.Fatalf("injected Project write failure: %v", failed)
	}
	if _, err := store.company(ctx, "company-empty"); err != nil {
		t.Fatalf("failed Project write lost valid empty Company: %v", err)
	}
	if _, err := store.Project(ctx, "company-empty", "first"); err != ErrNotFound {
		t.Fatalf("partial Project record: %v", err)
	}
	if _, err := store.Register(ctx, Registration{CompanyID: "company-empty", CompanyName: "Empty", ProjectID: "first", ProjectName: "First"}); err != nil {
		t.Fatalf("exact retry: %v", err)
	}
	if _, err := store.Register(ctx, Registration{CompanyID: "company-a", CompanyName: "other", ProjectID: "shared", ProjectName: "Shared"}); err != ErrConflict {
		t.Fatalf("conflicting Company: %v", err)
	}
	const fingerprint = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	updated, err := store.UpdateSnapshotReference(ctx, "company-a", "shared", SnapshotReference{CompanyID: "company-a", BindingRevision: 1, BindingDigest: bindingDigest(validTestBinding()), ExpectedFingerprint: "", Fingerprint: fingerprint, PreparationState: "ready"})
	if err != nil || updated.SnapshotFingerprint != fingerprint {
		t.Fatalf("snapshot pointer update = %+v, %v", updated, err)
	}
	changedBinding := validTestBinding()
	changedBinding.DockerRepositoryID = "belluga/docker"
	_, err = store.Register(ctx, Registration{CompanyID: "company-a", CompanyName: "company-a", ProjectID: "shared", ProjectName: "Shared", Binding: changedBinding})
	if err == nil || !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "TEACH:") {
		t.Fatalf("registration accepted a repository binding: %v", err)
	}
	genesis, err := store.Register(ctx, Registration{CompanyID: "company-a", CompanyName: "company-a", ProjectID: "genesis", ProjectName: "Genesis"})
	if err != nil || genesis.Binding != nil || genesis.SnapshotFingerprint != "" {
		t.Fatalf("Genesis registration: %+v, %v", genesis, err)
	}
	if retry, err := store.Register(ctx, Registration{CompanyID: "company-a", CompanyName: "company-a", ProjectID: "genesis", ProjectName: "Genesis"}); err != nil || retry.Binding != nil {
		t.Fatalf("Genesis retry: %+v, %v", retry, err)
	}
	first := FirstBindingRequest{ProjectID: "genesis", ExpectedRevision: 0, OperationID: "first-binding", Binding: validTestBinding()}
	first.Binding.DockerRepositoryID = "belluga/docker"
	first.Binding.SourceBindingsPath = "local-api/source-bindings.json"
	first.Binding.SourceBindingsStatePath = "control/source-bindings.json"
	first.Binding.SourceBindingsDigest = "sha256:" + strings.Repeat("a", 64)
	bound, err := store.BindFirst(ctx, "company-a", "genesis", first)
	if err != nil || bound.Binding == nil || bound.BindingRevision != 1 || *bound.Binding != first.Binding {
		t.Fatalf("first binding: %+v, %v", bound, err)
	}
	if retry, err := store.BindFirst(ctx, "company-a", "genesis", first); err != nil || retry.BindingRevision != 1 {
		t.Fatalf("first binding retry: %+v, %v", retry, err)
	}
	repositoryRequest := RepositoryBindingRequest{Action: "replace", ProjectID: "genesis", ExpectedRevision: 1, OperationID: "replace-repo", Actor: "owner", RepositoryID: "belluga/new-repo", Branch: "release/r-11"}
	configured, err := store.ChangeRepositoryBinding(ctx, "company-a", "genesis", repositoryRequest)
	if err != nil || configured.State != "configured" || configured.Revision != 2 || configured.RepositoryID != "belluga/new-repo" {
		t.Fatalf("repository replace: %+v, %v", configured, err)
	}
	if retry, err := store.ChangeRepositoryBinding(ctx, "company-a", "genesis", repositoryRequest); err != nil || retry != configured {
		t.Fatalf("repository exact retry: %+v, %v", retry, err)
	}
	projectAfterReplace, err := store.Project(ctx, "company-a", "genesis")
	if err != nil || projectAfterReplace.Binding == nil || !projectAfterReplace.Binding.LegacyFoundationSourceDisabled || projectAfterReplace.Binding.SourceBindingsPath != first.Binding.SourceBindingsPath || projectAfterReplace.Binding.SourceBindingsStatePath != first.Binding.SourceBindingsStatePath || projectAfterReplace.Binding.SourceBindingsDigest != first.Binding.SourceBindingsDigest {
		t.Fatalf("repository replace did not persist legacy-source fence and independent metadata: %+v, %v", projectAfterReplace, err)
	}
	staleSnapshot := SnapshotReference{CompanyID: "company-a", BindingRevision: 1, BindingDigest: bindingDigest(first.Binding), ExpectedFingerprint: "", Fingerprint: fingerprint, PreparationState: "ready"}
	if _, err := store.UpdateSnapshotReference(ctx, "company-a", "genesis", staleSnapshot); err != ErrConflict {
		t.Fatalf("stale snapshot pointer accepted after repository replace: %v", err)
	}
	currentSnapshot := staleSnapshot
	currentSnapshot.BindingRevision = 2
	currentSnapshot.BindingDigest = bindingDigest(*projectAfterReplace.Binding)
	if updated, err := store.UpdateSnapshotReference(ctx, "company-a", "genesis", currentSnapshot); err != nil || updated.SnapshotFingerprint != fingerprint {
		t.Fatalf("current snapshot pointer rejected: %+v, %v", updated, err)
	}
	if _, err := store.ChangeRepositoryBinding(ctx, "company-a", "genesis", RepositoryBindingRequest{Action: "bind", ProjectID: "genesis", ExpectedRevision: 2, OperationID: "wrong-state-bind", Actor: "owner", RepositoryID: "belluga/other", Branch: "main"}); !errors.Is(err, ErrConflict) || err == nil || !strings.Contains(err.Error(), "TEACH:") {
		t.Fatalf("bind on configured Project did not return actionable conflict: %v", err)
	}
	stale := repositoryRequest
	stale.OperationID = "stale-replace"
	if _, err := store.ChangeRepositoryBinding(ctx, "company-a", "genesis", stale); !errors.Is(err, ErrConflict) || err == nil || !strings.Contains(err.Error(), "TEACH:") || !strings.Contains(err.Error(), "expected_revision") {
		t.Fatalf("stale repository replace did not return scoped TEACH: %v", err)
	}
	changedRetry := repositoryRequest
	changedRetry.Branch = "main"
	if _, err := store.ChangeRepositoryBinding(ctx, "company-a", "genesis", changedRetry); !errors.Is(err, ErrConflict) || err == nil || !strings.Contains(err.Error(), "TEACH:") || !strings.Contains(err.Error(), "operation_id") {
		t.Fatalf("operation ID reuse did not return scoped TEACH: %v", err)
	}
	unbound, err := store.ChangeRepositoryBinding(ctx, "company-a", "genesis", RepositoryBindingRequest{Action: "unbind", ProjectID: "genesis", ExpectedRevision: 2, OperationID: "unbind-repo", Actor: "owner"})
	if err != nil || unbound.State != "absent" || unbound.Revision != 3 {
		t.Fatalf("repository unbind: %+v, %v", unbound, err)
	}
	projectAfterUnbind, err := store.Project(ctx, "company-a", "genesis")
	if err != nil || projectAfterUnbind.Binding == nil || !projectAfterUnbind.Binding.LegacyFoundationSourceDisabled || projectAfterUnbind.SnapshotFingerprint != "" {
		t.Fatalf("repository unbind did not retain the legacy-source fence or clear the snapshot: %+v, %v", projectAfterUnbind, err)
	}
	if _, err := store.ChangeRepositoryBinding(ctx, "company-a", "genesis", RepositoryBindingRequest{Action: "bind", ProjectID: "genesis", ExpectedRevision: 3, OperationID: "bind-after-unbind", Actor: "owner", RepositoryID: "belluga/next-repo", Branch: "main"}); err != nil {
		t.Fatalf("bind after unbind: %v", err)
	}
	projectAfterRebind, err := store.Project(ctx, "company-a", "genesis")
	if err != nil || projectAfterRebind.BindingRevision != 4 || projectAfterRebind.Binding == nil || projectAfterRebind.Binding.DockerRepositoryID != first.Binding.DockerRepositoryID || !projectAfterRebind.Binding.LegacyFoundationSourceDisabled {
		t.Fatalf("repository rebind lost independent metadata: %+v, %v", projectAfterRebind, err)
	}
	if _, err := store.UpdateSnapshotReference(ctx, "company-a", "genesis", currentSnapshot); err != ErrConflict {
		t.Fatalf("stale snapshot pointer accepted after repository rebind: %v", err)
	}
	first.OperationID = "conflicting-binding"
	if _, err := store.BindFirst(ctx, "company-a", "genesis", first); err != ErrConflict {
		t.Fatalf("first binding accepted a new operation: %v", err)
	}
	if _, err := store.Register(ctx, Registration{CompanyID: "company-a", CompanyName: "company-a", ProjectID: "genesis", ProjectName: "Genesis"}); err != nil {
		t.Fatalf("identity retry after binding: %v", err)
	}
	if _, err := store.UpdateSnapshotReference(ctx, "company-b", "shared", SnapshotReference{CompanyID: "company-a", BindingDigest: bindingDigest(validTestBinding()), Fingerprint: fingerprint, PreparationState: "ready"}); err != ErrInvalid {
		t.Fatalf("mismatched snapshot Company: %v", err)
	}
	catalog, err := store.Catalog(ctx)
	if err != nil || len(catalog.Projects) != 5 {
		t.Fatalf("catalog Projects = %d, %v", len(catalog.Projects), err)
	}
	for _, companyID := range []string{"company-a", "company-b"} {
		if err := store.Decline(ctx, companyID, "declined", validTestBinding()); err != nil {
			t.Fatal(err)
		}
	}
	catalog, err = store.Catalog(ctx)
	if err != nil || len(catalog.Preferences) != 2 || catalog.Preferences[0].CompanyID == catalog.Preferences[1].CompanyID {
		t.Fatalf("company-scoped preferences = %+v, %v", catalog.Preferences, err)
	}
	if _, err := store.Register(ctx, Registration{CompanyID: "company-a", CompanyName: "company-a", ProjectID: "declined", ProjectName: "Declined"}); err != nil {
		t.Fatalf("declined Project registration: %v", err)
	}
	catalog, err = store.Catalog(ctx)
	if err != nil || len(catalog.Preferences) != 1 || catalog.Preferences[0].CompanyID != "company-b" {
		t.Fatalf("registered Project still declined: %+v, %v", catalog.Preferences, err)
	}
	if err := store.Decline(ctx, "company-a", "shared", validTestBinding()); err != ErrConflict {
		t.Fatalf("registered Project decline: %v", err)
	}
	control, err := store.UpdateLifecycleControl(ctx, 0, json.RawMessage(`{"state":"complete"}`))
	if err != nil || control.Revision != 1 {
		t.Fatalf("lifecycle compare-and-set = %+v, %v", control, err)
	}
	control, err = store.LifecycleControl(ctx)
	if err != nil || control.Revision != 1 || string(control.Value) != `{"state":"complete"}` {
		t.Fatalf("lifecycle readback = %+v, %v", control, err)
	}
	if _, err := store.UpdateLifecycleControl(ctx, 0, json.RawMessage(`{"state":"started"}`)); err != ErrConflict {
		t.Fatalf("stale lifecycle revision accepted: %v", err)
	}
	roadmap, err := store.Roadmap(ctx, "shared", "company-b")
	if err != nil || !bytes.Contains(roadmap, []byte(`"epics":[]`)) {
		t.Fatalf("empty Roadmap = %s, %v", roadmap, err)
	}
}

func TestMongoImportIsRepeatableAndRejectsChangedHierarchy(t *testing.T) {
	uri := os.Getenv("REGISTRY_TEST_MONGODB_URI")
	if uri == "" {
		t.Skip("isolated MongoDB fixture not configured")
	}
	ctx := context.Background()
	stamp := fmt.Sprintf("test_%d", time.Now().UnixNano())
	store, err := OpenMongo(ctx, uri, stamp)
	if err != nil {
		t.Fatal(err)
	}
	store.tenantPrefix = stamp
	t.Cleanup(func() {
		for _, companyID := range []string{"belluga-solutions", "unifast"} {
			_ = store.client.Database(store.tenantDatabase(companyID)).Drop(context.Background())
		}
		_ = store.control.Drop(context.Background())
		_ = store.Close(context.Background())
	})
	binding := validTestBinding()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	input := LegacyImport{
		SchemaVersion: registrySchema,
		Companies:     map[string]Company{"belluga-solutions": {ID: "belluga-solutions", Name: "Belluga Solutions"}, "unifast": {ID: "unifast", Name: "Unifast"}},
		Projects: map[string]Project{
			"builder":     {ID: "builder", CompanyID: "belluga-solutions", Name: "Builder", Binding: &binding, RegisteredAt: now, PreparationState: "not_ready"},
			"boora":       {ID: "boora", CompanyID: "belluga-solutions", Name: "Bóora!", Binding: &binding, RegisteredAt: now, PreparationState: "not_ready"},
			"belluga-pay": {ID: "belluga-pay", CompanyID: "unifast", Name: "Belluga Pay", Binding: &binding, RegisteredAt: now, PreparationState: "not_ready"},
		},
		Preferences: map[string]Preference{},
		Roadmap: LegacyRoadmap{SchemaVersion: "builder-roadmap-v1", ProjectID: "boora", Epics: []LegacyEpic{
			{ID: "epic-mvp", Name: "MVP"},
			{ID: "epic-pilot", Name: "APP Público Piloto (Guarappari)", Releases: []LegacyRelease{{ID: "release-v0-7-1-participant-media", Name: "v0.7.1 — participant media"}}},
			{ID: "epic-plans", Name: "Planos Pagos + Gestão de Conta"},
			{ID: "epic-monetization", Name: "Funcionalidades de Monetização"},
		}},
		LifecycleMigration: &LifecycleMigration{
			State:       "complete",
			ProjectIDs:  []string{"belluga-pay", "boora", "builder"},
			Excluded:    []LifecycleMigrationDiagnostic{{ProjectID: "builder", TODOID: "todo-a", Path: "todos/active/todo-a.md", Message: "excluded at cutover"}},
			StartedAt:   now,
			CompletedAt: now,
		},
	}
	guardStamp := stamp + "_guard"
	guardStore, err := OpenMongo(ctx, uri, guardStamp)
	if err != nil {
		t.Fatal(err)
	}
	guardStore.tenantPrefix = guardStamp
	t.Cleanup(func() {
		for _, companyID := range []string{"belluga-solutions", "unifast"} {
			_ = guardStore.client.Database(guardStore.tenantDatabase(companyID)).Drop(context.Background())
		}
		_ = guardStore.control.Drop(context.Background())
		_ = guardStore.Close(context.Background())
	})
	if _, err := guardStore.UpdateLifecycleControl(ctx, 0, json.RawMessage(`{"state":"already-started"}`)); err != nil {
		t.Fatalf("seed nonempty lifecycle control: %v", err)
	}
	if _, err := guardStore.Import(ctx, input); err != ErrConflict {
		t.Fatalf("import with preexisting control state = %v, want conflict", err)
	}
	var companies []Company
	companyCursor, err := guardStore.control.Collection(companiesCollection).Find(ctx, bson.D{})
	if err != nil || companyCursor.All(ctx, &companies) != nil || len(companies) != 0 {
		t.Fatalf("rejected import partially created Companies: count=%d err=%v", len(companies), err)
	}
	for _, companyID := range []string{"belluga-solutions", "unifast"} {
		count, countErr := guardStore.client.Database(guardStore.tenantDatabase(companyID)).Collection(projectsCollection).CountDocuments(ctx, bson.D{})
		if countErr != nil || count != 0 {
			t.Fatalf("rejected import partially created %s Projects: count=%d err=%v", companyID, count, countErr)
		}
	}

	first, err := store.Import(ctx, input)
	if err != nil || first.NoOp {
		t.Fatalf("first import = %+v, %v", first, err)
	}
	control, err := store.LifecycleControl(ctx)
	if err != nil || !sameLifecycleMigration(control.Value, input.LifecycleMigration) {
		t.Fatalf("stored lifecycle state does not match the canonical readback: stored=%s expected=%+v err=%v", control.Value, input.LifecycleMigration, err)
	}
	second, err := store.Import(ctx, input)
	if err != nil || !second.NoOp {
		t.Fatalf("repeated import = %+v, %v", second, err)
	}
	roadmap, err := store.Roadmap(ctx, "boora", "belluga-solutions")
	if err != nil || bytes.Count(roadmap, []byte(`"id":"epic-`)) != 4 || bytes.Count(roadmap, []byte(`"id":"release-v0-7-1-participant-media"`)) != 1 || !bytes.Contains(roadmap, []byte(`"todos":[]`)) {
		t.Fatalf("imported Roadmap = %s, %v", roadmap, err)
	}
	for _, project := range []struct{ company, id string }{{"belluga-solutions", "builder"}, {"unifast", "belluga-pay"}} {
		roadmap, err := store.Roadmap(ctx, project.id, project.company)
		if err != nil || !bytes.Contains(roadmap, []byte(`"epics":[]`)) {
			t.Fatalf("unexpected Roadmap for %s: %s, %v", project.id, roadmap, err)
		}
	}
	input.Roadmap.Epics[1].Description = "changed"
	if _, err := store.Import(ctx, input); err != ErrConflict {
		t.Fatalf("changed imported Epic accepted: %v", err)
	}
}
