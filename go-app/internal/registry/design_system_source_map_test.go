package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	ac "belluga-builder-viewer/artifact_catalog"
	"go.mongodb.org/mongo-driver/v2/bson"
)

type sourceMapReadFixture struct {
	level string
	err   error
}

func (f *sourceMapReadFixture) ReadDesignSystem(_ context.Context, projectID, companyID, repositoryID, revision, _, level, _ string) (ac.DesignSystemResponse, []byte, error) {
	f.level = level
	if f.err != nil {
		return ac.DesignSystemResponse{}, nil, f.err
	}
	inv := "sha256:" + fmt.Sprintf("%064x", 1)
	return ac.DesignSystemResponse{Outcome: "go", DesignSystemValidation: "valid", InventoryDigest: &inv, SelectedLevel: &level, Items: []ac.DesignSystemDefinition{{}}}, nil, nil
}

func TestDesignSystemSourceMapPrecedenceMaskingAndFailClosed(t *testing.T) {
	uri := os.Getenv("REGISTRY_TEST_MONGODB_URI")
	if uri == "" {
		t.Skip("isolated MongoDB fixture not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	stamp := fmt.Sprintf("test_ds_map_%d", time.Now().UnixNano())
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

	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := store.ConfigureDesignSystemSourceMapRoot(root); err != nil {
		t.Fatal(err)
	}
	shaProject, shaCompany, shaDefault := fixtureSHA(1), fixtureSHA(2), fixtureSHA(3)
	bindings := ac.SourceBindings{SchemaVersion: "1", ProjectID: "design-system-map", CompanyID: "company-a", Sources: ac.BindingSources{
		Project: sourceMapBinding("belluga/project-source", shaProject), Company: sourceMapBinding("belluga/company-source", shaCompany), Default: sourceMapBinding("belluga/default-source", shaDefault),
	}}
	mapPath := "control/source-bindings/design-system-map.json"
	digest := writeSourceMapFixture(t, root, mapPath, bindings)
	binding := Binding{SourceBindingsStatePath: mapPath, SourceBindingsDigest: digest}
	projectID := bindings.ProjectID
	if _, err := store.Register(ctx, Registration{CompanyID: "company-a", CompanyName: "Company A", ProjectID: projectID, ProjectName: projectID}); err != nil {
		t.Fatal(err)
	}
	collection := store.client.Database(store.tenantDatabase("company-a")).Collection(projectsCollection)
	if _, err := collection.UpdateOne(ctx, bson.D{{Key: "id", Value: projectID}}, bson.M{"$set": bson.M{"binding": binding}}); err != nil {
		t.Fatal(err)
	}
	reader := &sourceMapReadFixture{}
	store.designSystemVerifier = designSystemReadHookVerifier{reader: reader}

	read := func(wantLevel string) {
		t.Helper()
		response, err := store.ReadDesignSystem(ctx, "company-a", projectID, "")
		if err != nil {
			t.Fatalf("read selected %s source: %v", wantLevel, err)
		}
		if response.Evaluation.SelectedLevel == nil || *response.Evaluation.SelectedLevel != wantLevel || reader.level != wantLevel {
			t.Fatalf("source precedence got evaluation=%v reader=%q want=%q", response.Evaluation.SelectedLevel, reader.level, wantLevel)
		}
	}
	read("project") // Never-bound Project selection wins over Company/default.

	if _, err := store.ChangeRepositoryBinding(ctx, "company-a", projectID, RepositoryBindingRequest{Action: "bind", ProjectID: projectID, ExpectedRevision: 0, OperationID: "map-bind", Actor: "fixture-owner", RepositoryID: "belluga/active", Branch: "main"}); err != nil {
		t.Fatal(err)
	}
	read("company") // Bound Project source-map slot is masked before validation/fetch.
	if _, err := store.ChangeRepositoryBinding(ctx, "company-a", projectID, RepositoryBindingRequest{Action: "unbind", ProjectID: projectID, ExpectedRevision: 1, OperationID: "map-unbind", Actor: "fixture-owner"}); err != nil {
		t.Fatal(err)
	}
	read("company") // Unbind must not revive the old Foundation Project slot.
	bindingsWithoutCompany := bindings
	bindingsWithoutCompany.Sources.Company = nil
	digest = writeSourceMapFixture(t, root, mapPath, bindingsWithoutCompany)
	binding.SourceBindingsDigest = digest
	if _, err := collection.UpdateOne(ctx, bson.D{{Key: "id", Value: projectID}}, bson.M{"$set": bson.M{"binding": binding}}); err != nil {
		t.Fatal(err)
	}
	loaded, loadErr := (fileDesignSystemSourceMapProvider{root: root}).Read(ctx, projectID, "company-a", Binding{SourceBindingsStatePath: mapPath, SourceBindingsDigest: digest})
	if loadErr != nil {
		t.Fatalf("read default-only source map: %v", loadErr)
	}
	if diagnostics := ac.ValidateSourceBindingsFor(loaded, "committed", projectID, "company-a"); len(diagnostics) != 0 {
		t.Fatalf("invalid default-only map: %+v", diagnostics)
	}
	loaded.Sources.Project = nil
	if diagnostics := ac.ValidateSourceBindingsFor(loaded, "committed", projectID, "company-a"); len(diagnostics) != 0 {
		t.Fatalf("invalid map after Project masking: %+v", diagnostics)
	}
	read("default") // Default is selected only when Company is absent.
	digest = writeSourceMapFixture(t, root, mapPath, bindings)
	binding.SourceBindingsDigest = digest
	if _, err := collection.UpdateOne(ctx, bson.D{{Key: "id", Value: projectID}}, bson.M{"$set": bson.M{"binding": binding}}); err != nil {
		t.Fatal(err)
	}

	if _, err := collection.UpdateOne(ctx, bson.D{{Key: "id", Value: projectID}}, bson.M{"$set": bson.M{"design_system_state": "absent"}}); err != nil {
		t.Fatal(err)
	}
	read("company") // A deliberate delete remains absent at Project level and inherits Company.

	bad := bindings
	bad.Sources.Company = sourceMapBinding("belluga/company-source", "") // Invalid unpinned selected fallback must not skip to default.
	digest = writeSourceMapFixture(t, root, mapPath, bad)
	binding.SourceBindingsDigest = digest
	if _, err := collection.UpdateOne(ctx, bson.D{{Key: "id", Value: projectID}}, bson.M{"$set": bson.M{"binding": binding}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadDesignSystem(ctx, "company-a", projectID, ""); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("invalid selected Company pin did not fail closed: %v", err)
	}

	// Missing map is not permission to fall through to a lower-priority source.
	binding.SourceBindingsStatePath = "control/source-bindings/missing.json"
	if _, err := collection.UpdateOne(ctx, bson.D{{Key: "id", Value: projectID}}, bson.M{"$set": bson.M{"binding": binding}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadDesignSystem(ctx, "company-a", projectID, ""); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("missing source map did not fail closed: %v", err)
	}

	// An exact Mongo Project selection always wins; an unavailable selected SHA
	// must not trigger Company/default fallback.
	bindings.Sources.Company = sourceMapBinding("belluga/company-source", shaCompany)
	digest = writeSourceMapFixture(t, root, mapPath, bindings)
	binding.SourceBindingsStatePath = mapPath
	binding.SourceBindingsDigest = digest
	binding.RepositoryID = "belluga/active"
	binding.PublicationBranch = "main"
	if _, err := collection.UpdateOne(ctx, bson.D{{Key: "id", Value: projectID}}, bson.M{"$set": bson.M{
		"binding": binding, "design_system_state": "selected",
		"design_system_sha": fixtureSHA(4), "design_system_inventory_digest": "sha256:" + fmt.Sprintf("%064x", 9),
	}}); err != nil {
		t.Fatal(err)
	}
	reader.err = ErrUnavailable
	if _, err := store.ReadDesignSystem(ctx, "company-a", projectID, ""); !errors.Is(err, ErrUnavailable) || reader.level != "project" {
		t.Fatalf("missing selected Project source fell back: level=%q err=%v", reader.level, err)
	}
}

func fixtureSHA(value int) string { return fmt.Sprintf("%040x", value) }

func sourceMapBinding(repositoryID, revision string) *ac.SourceBinding {
	var pinned *string
	if revision != "" {
		pinned = &revision
	}
	return &ac.SourceBinding{RepositoryID: repositoryID, CheckoutRoot: "repo", Revision: pinned, DefinitionPath: "design/system/design-system.json"}
}

func writeSourceMapFixture(t *testing.T, root, relative string, bindings ac.SourceBindings) string {
	t.Helper()
	data, err := json.Marshal(bindings)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}
