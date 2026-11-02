package registry

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
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
	legacy := projectDocument{ID: "builder", CompanyID: "belluga", Binding: validTestBinding(), RegisteredAt: time.Now().UTC()}
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
		return store.Register(ctx, Registration{CompanyID: companyID, CompanyName: companyID, ProjectID: "shared", ProjectName: "Shared", Binding: validTestBinding()})
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
			_, err := store.Register(ctx, Registration{CompanyID: "company-contended", CompanyName: companyName, ProjectID: "contended", ProjectName: "Contended", Binding: validTestBinding()})
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
	_, failed := store.Register(ctx, Registration{CompanyID: "company-empty", CompanyName: "Empty", ProjectID: "first", ProjectName: "First", Binding: validTestBinding()})
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
	if _, err := store.Register(ctx, Registration{CompanyID: "company-empty", CompanyName: "Empty", ProjectID: "first", ProjectName: "First", Binding: validTestBinding()}); err != nil {
		t.Fatalf("exact retry: %v", err)
	}
	if _, err := store.Register(ctx, Registration{CompanyID: "company-a", CompanyName: "other", ProjectID: "shared", ProjectName: "Shared", Binding: validTestBinding()}); err != ErrConflict {
		t.Fatalf("conflicting Company: %v", err)
	}
	const fingerprint = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	updated, err := store.UpdateSnapshotReference(ctx, "company-a", "shared", SnapshotReference{CompanyID: "company-a", BindingDigest: bindingDigest(validTestBinding()), ExpectedFingerprint: "", Fingerprint: fingerprint, PreparationState: "ready"})
	if err != nil || updated.SnapshotFingerprint != fingerprint {
		t.Fatalf("snapshot pointer update = %+v, %v", updated, err)
	}
	changedBinding := validTestBinding()
	changedBinding.DockerRepositoryID = "belluga/docker"
	updated, err = store.Register(ctx, Registration{CompanyID: "company-a", CompanyName: "company-a", ProjectID: "shared", ProjectName: "Shared", Binding: changedBinding})
	if err != nil || updated.Binding == nil || *updated.Binding != changedBinding || updated.SnapshotFingerprint != "" || updated.PreparationState != "not_ready" {
		t.Fatalf("atomic binding refresh = %+v, %v", updated, err)
	}
	nextBinding := changedBinding
	nextBinding.DockerRepositoryID = "belluga/next-docker"
	store.beforeSnapshotWrite = func() error {
		_, err := store.Register(ctx, Registration{CompanyID: "company-a", CompanyName: "company-a", ProjectID: "shared", ProjectName: "Shared", Binding: nextBinding})
		return err
	}
	_, err = store.UpdateSnapshotReference(ctx, "company-a", "shared", SnapshotReference{CompanyID: "company-a", BindingDigest: bindingDigest(changedBinding), ExpectedFingerprint: "", Fingerprint: fingerprint, PreparationState: "ready"})
	store.beforeSnapshotWrite = nil
	if err != ErrConflict {
		t.Fatalf("snapshot update crossed a binding change: %v", err)
	}
	current, err := store.Project(ctx, "company-a", "shared")
	if err != nil || current.Binding == nil || *current.Binding != nextBinding || current.SnapshotFingerprint != "" {
		t.Fatalf("stale snapshot was attached to new binding: %+v, %v", current, err)
	}
	if _, err := store.UpdateSnapshotReference(ctx, "company-b", "shared", SnapshotReference{CompanyID: "company-a", BindingDigest: bindingDigest(validTestBinding()), Fingerprint: fingerprint, PreparationState: "ready"}); err != ErrInvalid {
		t.Fatalf("mismatched snapshot Company: %v", err)
	}
	catalog, err := store.Catalog(ctx)
	if err != nil || len(catalog.Projects) != 4 {
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
	if _, err := store.Register(ctx, Registration{CompanyID: "company-a", CompanyName: "company-a", ProjectID: "declined", ProjectName: "Declined", Binding: validTestBinding()}); err != nil {
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
