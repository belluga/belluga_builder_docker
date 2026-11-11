package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	ac "delphi-local-api/artifactcatalog"
)

func TestLandingManifestV2PinsSHAInventoryIdentityAndAPIOrigin(t *testing.T) {
	root, revision, binding := landingFixture(t, "<main>Landing</main>")
	source, err := ac.NewGitTree(root, revision)
	if err != nil {
		t.Fatal(err)
	}
	origin := landingOriginFixture()
	activeOrigin := origin
	catalog, err := evaluateLandingSource(source, "company-a", "project-a", "owner/repo", binding, revision, activeOrigin)
	if err != nil {
		t.Fatalf("valid v2 manifest rejected: %v", err)
	}
	if catalog.Revision != revision || catalog.BindingRevision != binding.Revision || catalog.InventoryDigest == "" || catalog.ManifestVersion != "2" || catalog.EntryPoint != "design/landing/index.html" {
		t.Fatalf("Landing was not pinned to exact Git and binding identity: %+v", catalog)
	}
	if len(catalog.Files) != 2 || catalog.KnowledgeOrigin.GenerationID != origin.GenerationID || len(catalog.KnowledgeOrigin.Documents) != len(origin.Documents) || catalog.KnowledgeOrigin.Documents[0] != origin.Documents[0] || catalog.KnowledgeOrigin.Documents[1] != origin.Documents[1] {
		t.Fatalf("manifest inventory/API origin mismatch: %+v", catalog)
	}
	staleOrigin := activeOrigin
	staleOrigin.Documents = append([]CanonicalManifestEntry(nil), activeOrigin.Documents...)
	staleOrigin.Documents[0].ContentDigest = "sha256:" + strings.Repeat("e", 64)
	if _, err := evaluateLandingSource(source, "company-a", "project-a", "owner/repo", binding, revision, staleOrigin); err == nil {
		t.Fatal("manifest referencing a stale API document digest was admitted")
	}
}

func TestSameLandingOriginRequiresCompleteCanonicalDocumentSet(t *testing.T) {
	complete := landingOriginFixture()
	subset := complete
	subset.Documents = complete.Documents[:1]
	if sameLandingOrigin(subset, complete) || sameLandingOrigin(complete, subset) {
		t.Fatal("Landing origin with an omitted canonical document was treated as complete")
	}
	if !sameLandingOrigin(complete, complete) {
		t.Fatal("identical complete canonical origins must match")
	}
}

func TestLandingRejectsUnsupportedManifestWrongIdentityAndUnsafePaths(t *testing.T) {
	root, _, binding := landingFixture(t, "<main>Landing</main>")
	writeLandingFixtureManifest(t, root, "1", "project-a", "company-a", true, false)
	commitLanding(t, root)
	revision := gitLanding(t, root, "rev-parse", "HEAD")
	source, _ := ac.NewGitTree(root, revision)
	if _, err := evaluateLandingSource(source, "company-a", "project-a", "owner/repo", binding, revision, landingOriginFixture()); !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "design/landing/manifest.json") || !strings.Contains(err.Error(), "schema_version 2") {
		t.Fatalf("unsupported manifest must return schema-specific TEACH, got %v", err)
	}
	writeLandingFixtureManifest(t, root, "2", "other-project", "company-a", false, false)
	commitLanding(t, root)
	revision = gitLanding(t, root, "rev-parse", "HEAD")
	source, _ = ac.NewGitTree(root, revision)
	if _, err := evaluateLandingSource(source, "company-a", "project-a", "owner/repo", binding, revision, landingOriginFixture()); err == nil {
		t.Fatal("wrong Project identity was admitted")
	}
	for _, path := range []string{"../secret", "design/landing/../secret", "project_landing.md", "/etc/passwd", "design/landing/.git/config", "design/landing/unknown.bin"} {
		if landingMIMEType(path) != "" {
			t.Errorf("unsafe or unsupported path %q admitted", path)
		}
	}
}

func TestLandingMissingManifestReturnsNotAuthoredTEACH(t *testing.T) {
	root, _, binding := landingFixture(t, "<main>Landing</main>")
	if err := os.Remove(filepath.Join(root, landingManifestPath)); err != nil {
		t.Fatal(err)
	}
	commitLanding(t, root)
	revision := gitLanding(t, root, "rev-parse", "HEAD")
	source, err := ac.NewGitTree(root, revision)
	if err != nil {
		t.Fatal(err)
	}
	_, err = evaluateLandingSource(source, "company-a", "project-a", "owner/repo", binding, revision, landingOriginFixture())
	if !errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "design/landing/manifest.json") || !strings.Contains(err.Error(), "schema_version 2") || !strings.Contains(err.Error(), "knowledge_origin") || !strings.Contains(err.Error(), "root project_landing.manifest.json") || !strings.Contains(err.Error(), "not runtime fallbacks") {
		t.Fatalf("missing Landing manifest must return precise not-authored TEACH, got %v", err)
	}
}

func TestLandingIgnoresRootLegacyManifestAndReturnsPathSpecificTEACH(t *testing.T) {
	root, _, binding := landingFixture(t, "<main>Landing</main>")
	if err := os.Remove(filepath.Join(root, landingManifestPath)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "project_landing.manifest.json"), []byte(`{"schema_version":"2","local_visual_artifact":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	commitLanding(t, root)
	revision := gitLanding(t, root, "rev-parse", "HEAD")
	source, err := ac.NewGitTree(root, revision)
	if err != nil {
		t.Fatal(err)
	}
	_, err = evaluateLandingSource(source, "company-a", "project-a", "owner/repo", binding, revision, landingOriginFixture())
	if !errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "design/landing/manifest.json") || !strings.Contains(err.Error(), "schema_version 2") || !strings.Contains(err.Error(), "root project_landing.manifest.json") {
		t.Fatalf("root legacy manifest must not select Landing and TEACH must name the required contract, got %v", err)
	}
}

func TestLandingRejectsManifestOriginDriftAndOversizedFile(t *testing.T) {
	root, revision, binding := landingFixture(t, "<main>Landing</main>")
	source, _ := ac.NewGitTree(root, revision)
	wrongOrigin := landingOriginFixture()
	wrongOrigin.GenerationID = "generation-2"
	if _, err := evaluateLandingSource(source, "company-a", "project-a", "owner/repo", binding, revision, wrongOrigin); err == nil {
		t.Fatal("manifest with stale API knowledge origin was admitted")
	}
	tooLarge := strings.Repeat("x", landingMaxFileBytes+1)
	if err := os.WriteFile(filepath.Join(root, "design/landing/oversized.css"), []byte(tooLarge), 0o600); err != nil {
		t.Fatal(err)
	}
	writeLandingFixtureManifest(t, root, "2", "project-a", "company-a", false, false)
	appendLandingFixtureFile(t, root, "design/landing/oversized.css", []byte(tooLarge))
	commitLanding(t, root)
	revision = gitLanding(t, root, "rev-parse", "HEAD")
	source, _ = ac.NewGitTree(root, revision)
	if _, err := evaluateLandingSource(source, "company-a", "project-a", "owner/repo", binding, revision, landingOriginFixture()); err == nil {
		t.Fatal("file over the declared 2 MiB bound was admitted")
	}
}

func TestLandingCatalogRouteReturnsPinnedManifestInventory(t *testing.T) {
	service := &landingHTTPFixture{testService: &testService{}, catalog: LandingCatalogResponse{
		CompanyID: "company-a", ProjectID: "project-a", RepositoryID: "owner/repo", PublicationBranch: "main",
		BindingRevision: 7, Revision: strings.Repeat("a", 40), InventoryDigest: "sha256:" + strings.Repeat("b", 64),
		EntryPoint: "design/landing/index.html", Files: []LandingFileLink{{Path: "design/landing/index.html", URL: "/landing/index.html"}},
	}}
	response := httptest.NewRecorder()
	NewHandler(service).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/internal/v1/companies/company-a/projects/project-a/landing", nil))
	body := response.Body.String()
	for _, expected := range []string{service.catalog.Revision, service.catalog.InventoryDigest, `"binding_revision":7`, `"entry_point":"design/landing/index.html"`} {
		if response.Code != http.StatusOK || !strings.Contains(body, expected) {
			t.Fatalf("Landing catalog missing pinned contract %q: status=%d body=%s", expected, response.Code, body)
		}
	}
}

func TestLandingFileRouteIsRevisionBoundAndSetsMIMECSPAndBounds(t *testing.T) {
	revision := strings.Repeat("a", 40)
	service := &landingHTTPFixture{testService: &testService{}, catalog: LandingCatalogResponse{
		CompanyID: "company-a", ProjectID: "project-a", BindingRevision: 7, Revision: revision,
		InventoryDigest: "sha256:" + strings.Repeat("b", 64),
	}, file: landingFileResponse{Data: []byte("<main>rendered</main>"), ContentType: "text/html; charset=utf-8", Revision: revision, InventoryDigest: "sha256:" + strings.Repeat("b", 64), ETag: `"landing-test"`}}
	path := "/internal/v1/companies/company-a/projects/project-a/landing/7/" + revision + "/files/design/landing/index.html"
	response := httptest.NewRecorder()
	NewHandler(service).ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
	policy := response.Header().Get("Content-Security-Policy")
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "text/html; charset=utf-8" || response.Header().Get("X-Landing-Revision") != revision || response.Header().Get("X-Landing-Inventory-Digest") == "" || response.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("Landing bytes are not bounded/MIME/revision protected: status=%d headers=%v", response.Code, response.Header())
	}
	for _, directive := range []string{"sandbox", "connect-src 'none'", "object-src 'none'", "base-uri 'none'"} {
		if !strings.Contains(policy, directive) {
			t.Fatalf("Landing CSP missing %q: %s", directive, policy)
		}
	}
	if service.revision != revision || service.path != "design/landing/index.html" {
		t.Fatalf("route lost exact pinned SHA/path: %s %s", service.revision, service.path)
	}
	unsafe := &landingHTTPFixture{testService: &testService{}, catalog: service.catalog, file: landingFileResponse{Data: []byte("unsafe"), ContentType: "text/html\r\nX-Injected: yes", ETag: `"unsafe"`, Revision: revision, InventoryDigest: service.catalog.InventoryDigest}}
	badResponse := httptest.NewRecorder()
	NewHandler(unsafe).ServeHTTP(badResponse, httptest.NewRequest(http.MethodGet, path, nil))
	if badResponse.Code != http.StatusServiceUnavailable {
		t.Fatalf("untrusted response MIME reached HTTP headers: %d %v", badResponse.Code, badResponse.Header())
	}
}

func TestLandingBindingFenceRejectsRebindButAllowsAtoBPinning(t *testing.T) {
	a := LandingCatalogResponse{RepositoryID: "owner/repo", PublicationBranch: "main", BindingRevision: 7, Revision: strings.Repeat("a", 40)}
	b := a
	b.Revision = strings.Repeat("b", 40)
	if !landingBindingMatchesCatalog(RepositoryBinding{State: "configured", RepositoryID: "owner/repo", PublicationBranch: "main", Revision: 7}, a) || a.Revision == b.Revision {
		t.Fatal("A/B must remain separately pinned under the unchanged binding")
	}
	if landingBindingMatchesCatalog(RepositoryBinding{State: "configured", RepositoryID: "owner/other", PublicationBranch: "main", Revision: 8}, a) {
		t.Fatal("old Landing catalog survived Project binding change")
	}
}

func TestMongoLandingReadsPinABAndDoNotResolveBranchPerFile(t *testing.T) {
	root, revisionA, _ := landingFixture(t, "<main>A</main>")
	binding := RepositoryBinding{State: "configured", RepositoryID: "owner/repo", PublicationBranch: "main", Revision: 7}
	apiManifest := landingAPIFixture()
	gitReader := &landingGitFixture{root: root, head: revisionA}
	store := landingMongoFixture(binding, apiManifest, gitReader)
	catalogA, err := store.ReadLanding(context.Background(), "company-a", "project-a")
	if err != nil {
		t.Fatalf("read A catalog: %v", err)
	}
	fileA, err := store.ReadLandingFile(context.Background(), "company-a", "project-a", binding.Revision, revisionA, "design/landing/index.html")
	if err != nil || string(fileA.Data) != "<main>A</main>" {
		t.Fatalf("read pinned A file: %q, %v", fileA.Data, err)
	}
	if err := os.WriteFile(filepath.Join(root, "design/landing/index.html"), []byte("<main>B</main>"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeLandingFixtureManifest(t, root, "2", "project-a", "company-a", false, false)
	commitLanding(t, root)
	revisionB := gitLanding(t, root, "rev-parse", "HEAD")
	gitReader.head = revisionB
	catalogB, err := store.ReadLanding(context.Background(), "company-a", "project-a")
	if err != nil || catalogB.Revision != revisionB || catalogA.Revision != revisionA {
		t.Fatalf("A→B catalog refresh failed: A=%s B=%s err=%v", catalogA.Revision, catalogB.Revision, err)
	}
	fileB, err := store.ReadLandingFile(context.Background(), "company-a", "project-a", binding.Revision, revisionB, "design/landing/index.html")
	if err != nil || string(fileB.Data) != "<main>B</main>" {
		t.Fatalf("read pinned B file: %q, %v", fileB.Data, err)
	}
	fileA, err = store.ReadLandingFile(context.Background(), "company-a", "project-a", binding.Revision, revisionA, "design/landing/index.html")
	if err != nil || string(fileA.Data) != "<main>A</main>" {
		t.Fatalf("A revision was not immutable after B publication: %q, %v", fileA.Data, err)
	}
	if gitReader.branchHeads != 2 {
		t.Fatalf("file requests resolved branch head; calls=%d want=2 catalog refreshes", gitReader.branchHeads)
	}
}

func TestMongoLandingRejectsOldPinnedFileAfterBindingChange(t *testing.T) {
	root, revision, _ := landingFixture(t, "<main>A</main>")
	binding := RepositoryBinding{State: "configured", RepositoryID: "owner/repo", PublicationBranch: "main", Revision: 7}
	gitReader := &landingGitFixture{root: root, head: revision}
	store := landingMongoFixture(binding, landingAPIFixture(), gitReader)
	catalog, err := store.ReadLanding(context.Background(), "company-a", "project-a")
	if err != nil {
		t.Fatal(err)
	}
	store.landingBindingReader = func(context.Context, string, string) (RepositoryBinding, error) {
		return RepositoryBinding{State: "configured", RepositoryID: "owner/other", PublicationBranch: "main", Revision: 8}, nil
	}
	if _, err := store.ReadLandingFile(context.Background(), "company-a", "project-a", catalog.BindingRevision, catalog.Revision, "design/landing/index.html"); err != ErrConflict {
		t.Fatalf("old URL survived repository rebind: %v", err)
	}
}

func TestMongoLandingFencesBindingChangeDuringCatalogRead(t *testing.T) {
	root, revision, _ := landingFixture(t, "<main>A</main>")
	binding := RepositoryBinding{State: "configured", RepositoryID: "owner/repo", PublicationBranch: "main", Revision: 7}
	gitReader := &landingGitFixture{root: root, head: revision}
	store := landingMongoFixture(binding, landingAPIFixture(), gitReader)
	reads := 0
	store.landingBindingReader = func(context.Context, string, string) (RepositoryBinding, error) {
		reads++
		if reads == 1 {
			return binding, nil
		}
		return RepositoryBinding{State: "configured", RepositoryID: "owner/other", PublicationBranch: "main", Revision: 8}, nil
	}
	if _, err := store.ReadLanding(context.Background(), "company-a", "project-a"); err != ErrConflict {
		t.Fatalf("catalog was published despite a concurrent rebind: %v", err)
	}
}

type landingHTTPFixture struct {
	*testService
	catalog  LandingCatalogResponse
	file     landingFileResponse
	revision string
	path     string
}

func (s *landingHTTPFixture) ReadLanding(_ context.Context, companyID, projectID string) (LandingCatalogResponse, error) {
	if companyID != s.catalog.CompanyID || projectID != s.catalog.ProjectID {
		return LandingCatalogResponse{}, ErrNotFound
	}
	return s.catalog, nil
}

func (s *landingHTTPFixture) ReadLandingFile(_ context.Context, companyID, projectID string, bindingRevision int64, revision, path string) (landingFileResponse, error) {
	if companyID != s.catalog.CompanyID || projectID != s.catalog.ProjectID || revision != s.catalog.Revision || bindingRevision != s.catalog.BindingRevision {
		return landingFileResponse{}, ErrNotFound
	}
	s.revision, s.path = revision, path
	return s.file, nil
}

var _ landingReader = (*landingHTTPFixture)(nil)

func landingFixture(t *testing.T, html string) (string, string, RepositoryBinding) {
	t.Helper()
	root := t.TempDir()
	gitLanding(t, root, "init", "--quiet", "-b", "main")
	gitLanding(t, root, "config", "user.name", "Landing Test")
	gitLanding(t, root, "config", "user.email", "landing@example.invalid")
	if err := os.MkdirAll(filepath.Join(root, "design/landing"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "design/landing/index.html"), []byte(html), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "design/landing/style.css"), []byte("body{color:#123}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeLandingFixtureManifest(t, root, "2", "project-a", "company-a", false, false)
	commitLanding(t, root)
	return root, gitLanding(t, root, "rev-parse", "HEAD"), RepositoryBinding{State: "configured", RepositoryID: "owner/repo", PublicationBranch: "main", Revision: 7}
}

func writeLandingFixtureManifest(t *testing.T, root, version, projectID, companyID string, markdown, unsafe bool) {
	t.Helper()
	paths := []string{"design/landing/index.html", "design/landing/style.css"}
	if markdown {
		paths = append(paths, "project_landing.md")
		if err := os.WriteFile(filepath.Join(root, "project_landing.md"), []byte("# archived\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if unsafe {
		paths = append(paths, "design/landing/../secret.txt")
	}
	files := make([]landingManifestFile, 0, len(paths))
	for _, path := range paths {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		files = append(files, landingManifestFile{Path: path, Digest: "sha256:" + hex.EncodeToString(sum[:])})
	}
	manifest := landingManifestV2{SchemaVersion: version}
	manifest.Visual.Path, manifest.Visual.ProjectID, manifest.Visual.CompanyID = "design/landing/index.html", projectID, companyID
	manifest.Visual.ArtifactFiles, manifest.Visual.KnowledgeOrigin = files, landingOriginFixture()
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, landingManifestPath), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func appendLandingFixtureFile(t *testing.T, root, path string, content []byte) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, landingManifestPath))
	if err != nil {
		t.Fatal(err)
	}
	var manifest landingManifestV2
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	manifest.Visual.ArtifactFiles = append(manifest.Visual.ArtifactFiles, landingManifestFile{Path: path, Digest: "sha256:" + hex.EncodeToString(sum[:])})
	data, err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, landingManifestPath), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func landingOriginFixture() LandingKnowledgeOrigin {
	return LandingKnowledgeOrigin{ProjectID: "project-a", GenerationID: "generation-1", Documents: []CanonicalManifestEntry{
		{Family: FamilyMandate, ID: "mandate", Revision: 4, ContentDigest: "sha256:" + strings.Repeat("c", 64)},
		{Family: FamilyRoadmap, ID: "roadmap", Revision: 9, ContentDigest: "sha256:" + strings.Repeat("d", 64)},
	}}
}

func landingAPIFixture() CanonicalManifest {
	origin := landingOriginFixture()
	documents := append([]CanonicalManifestEntry(nil), origin.Documents...)
	return CanonicalManifest{ProjectID: origin.ProjectID, GenerationID: origin.GenerationID, Active: true, State: "active", Documents: documents}
}

func landingMongoFixture(binding RepositoryBinding, manifest CanonicalManifest, git *landingGitFixture) *MongoStore {
	return &MongoStore{
		prototypeGit: git, prototypeFileCache: newPrototypeFileCache(),
		landingBindingReader:  func(context.Context, string, string) (RepositoryBinding, error) { return binding, nil },
		landingManifestReader: func(context.Context, string, string) (CanonicalManifest, error) { return manifest, nil },
	}
}

type landingGitFixture struct {
	root        string
	head        string
	branchHeads int
	openReads   int
}

func (f *landingGitFixture) BranchHead(context.Context, RepositoryBinding) (string, error) {
	f.branchHeads++
	return f.head, nil
}

func (f *landingGitFixture) OpenRevision(_ context.Context, _, _ string, _ RepositoryBinding, revision string) (prototypeGitSource, error) {
	f.openReads++
	source, err := ac.NewGitTree(f.root, revision)
	if err != nil {
		return prototypeGitSource{}, err
	}
	return prototypeGitSource{source: source}, nil
}

func commitLanding(t *testing.T, root string) {
	t.Helper()
	gitLanding(t, root, "add", "--all")
	gitLanding(t, root, "commit", "--quiet", "-m", "landing fixture")
}

func gitLanding(t *testing.T, root string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}
