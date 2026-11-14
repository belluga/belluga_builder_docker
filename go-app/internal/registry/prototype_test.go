package registry

import (
	"context"
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

func TestPrototypeGitCacheStaysInsideGoStoreAndReusesExactRevision(t *testing.T) {
	root := t.TempDir()
	remote := filepath.Join(root, "remote")
	cache := filepath.Join(root, "go-cache")
	if err := os.Mkdir(remote, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(cache, 0o700); err != nil {
		t.Fatal(err)
	}
	runGit := func(args ...string) string {
		t.Helper()
		command := exec.Command("git", append([]string{"-C", remote}, args...)...)
		out, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	runGit("init", "--quiet", "-b", "main")
	runGit("config", "user.name", "Prototype Test")
	runGit("config", "user.email", "prototype@example.invalid")
	writeRevision := func(body string) string {
		t.Helper()
		if err := os.WriteFile(filepath.Join(remote, "screen.html"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		runGit("add", "screen.html")
		runGit("commit", "--quiet", "-m", "screen")
		return runGit("rev-parse", "HEAD")
	}
	first := writeRevision("first")
	reader := gitDesignSystemVerifier{repositoryURL: func(string) string { return remote }, prototypeCacheRoot: cache}
	binding := RepositoryBinding{State: "configured", RepositoryID: "owner/project", PublicationBranch: "main"}
	open := func(companyID, projectID, revision, expected string) string {
		t.Helper()
		source, err := reader.OpenRevision(context.Background(), companyID, projectID, binding, revision)
		if err != nil {
			t.Fatal(err)
		}
		defer source.close()
		data, mode, err := source.source.Read("screen.html", ac.MaxFileBytes)
		if err != nil || mode != ac.ModeRegular || string(data) != expected {
			t.Fatalf("cached revision read: data=%q mode=%v err=%v", data, mode, err)
		}
		if strings.HasPrefix(source.workspace, remote) || !strings.HasPrefix(source.workspace, cache+string(os.PathSeparator)) {
			t.Fatalf("Go cache escaped its private root: %s", source.workspace)
		}
		return source.workspace
	}
	workspace := open("company-a", "project-a", first, "first")
	if _, err := os.Stat(filepath.Join(workspace, ".git")); err != nil {
		t.Fatalf("cache removed after read: %v", err)
	}
	if reused := open("company-a", "project-a", first, "first"); reused != workspace {
		t.Fatalf("same repository used a new cache directory: %s != %s", reused, workspace)
	}
	second := writeRevision("second")
	otherWorkspace := open("company-b", "project-b", second, "second")
	if otherWorkspace == workspace {
		t.Fatal("different Company/Project reused the same physical Git cache")
	}
	if retained := open("company-a", "project-a", first, "first"); retained != workspace {
		t.Fatalf("another Project's refresh displaced the pinned revision: %s != %s", retained, workspace)
	}
	if refreshed := open("company-a", "project-a", second, "second"); refreshed != workspace {
		t.Fatalf("branch update used a new cache directory: %s != %s", refreshed, workspace)
	}
	if out, err := exec.Command("git", "-C", workspace, "cat-file", "-e", first+"^{commit}").CombinedOutput(); err == nil {
		t.Fatalf("superseded Git commit was retained by the cache: %s", out)
	}
}

type countingPrototypeGitReader struct {
	headCalls int
	head      string
}

func (r *countingPrototypeGitReader) BranchHead(context.Context, RepositoryBinding) (string, error) {
	r.headCalls++
	return r.head, nil
}
func (*countingPrototypeGitReader) OpenRevision(context.Context, string, string, RepositoryBinding, string) (prototypeGitSource, error) {
	return prototypeGitSource{}, ErrUnavailable
}

func TestPrototypeRevisionUsesRemoteOnlyForCatalogRefresh(t *testing.T) {
	reader := &countingPrototypeGitReader{head: strings.Repeat("b", 40)}
	pinned := strings.Repeat("a", 40)
	got, err := prototypeReadRevision(context.Background(), reader, RepositoryBinding{}, pinned)
	if err != nil || got != pinned || reader.headCalls != 0 {
		t.Fatalf("pinned read contacted Git: revision=%q calls=%d err=%v", got, reader.headCalls, err)
	}
	got, err = prototypeReadRevision(context.Background(), reader, RepositoryBinding{}, "")
	if err != nil || got != reader.head || reader.headCalls != 1 {
		t.Fatalf("catalog refresh did not verify the current head exactly once: revision=%q calls=%d err=%v", got, reader.headCalls, err)
	}
	if _, err := prototypeReadRevision(context.Background(), reader, RepositoryBinding{}, "invalid"); !errors.Is(err, ErrInvalid) || reader.headCalls != 1 {
		t.Fatalf("invalid pinned revision reached Git: calls=%d err=%v", reader.headCalls, err)
	}
}

func TestPrototypeCachesAreScopedByCompanyProjectAndRevision(t *testing.T) {
	revision := strings.Repeat("a", 40)
	path := "prototypes/demo/index.html"
	item := ac.Item{ID: "demo", Root: "prototypes/demo", AllowedPaths: []string{path}}
	catalog := PrototypeCatalogResponse{RepositoryID: "owner/repo", PublicationBranch: "main", Revision: revision,
		InventoryDigest: "sha256:" + strings.Repeat("b", 64), Items: []PrototypeSummary{{ID: "demo"}}, navigation: []ac.Item{item}}
	store := &MongoStore{prototypeHot: newPrototypeHotCache(), prototypeFileCache: newPrototypeFileCache()}
	store.prototypeHot.replace("company", "project", &catalog)
	store.prototypeFileCache.put(prototypeFileCacheKey("company", "project", catalog, path), prototypeFileResponseFor([]byte("<main>ready</main>"), path, catalog))
	if hot, found := store.prototypeHot.get("company", "project", revision); !found || hot.catalog.Revision != revision {
		t.Fatal("own project did not find its verified projection")
	}
	for _, identity := range [][2]string{{"company", "other-project"}, {"other-company", "project"}} {
		if _, found := store.prototypeHot.get(identity[0], identity[1], revision); found {
			t.Fatalf("projection crossed company/project boundary: %v", identity)
		}
		if _, found := store.prototypeFileCache.get(prototypeFileCacheKey(identity[0], identity[1], catalog, path)); found {
			t.Fatalf("file bytes crossed company/project boundary: %v", identity)
		}
	}
	if _, found := store.prototypeHot.get("company", "project", strings.Repeat("c", 40)); found {
		t.Fatal("another revision reused the hot projection")
	}
	if file, found := store.prototypeFileCache.get(prototypeFileCacheKey("company", "project", catalog, path)); !found || string(file.Data) != "<main>ready</main>" {
		t.Fatal("own project did not find its file bytes")
	}
	store.prototypeHot.replace("company", "project", nil)
	if _, found := store.prototypeHot.get("company", "project", revision); found {
		t.Fatal("catalog refresh did not invalidate the previous projection")
	}
}

func TestPrototypeBindingFenceRejectsProjectRebind(t *testing.T) {
	catalog := PrototypeCatalogResponse{RepositoryID: "owner/repo", PublicationBranch: "main", BindingRevision: 3}
	current := RepositoryBinding{State: "configured", RepositoryID: "owner/repo", PublicationBranch: "main", Revision: 3}
	if !prototypeBindingMatchesCatalog(current, catalog) {
		t.Fatal("unchanged binding should remain valid")
	}
	for _, changed := range []RepositoryBinding{
		{State: "absent", Revision: 4},
		{State: "configured", RepositoryID: "owner/other", PublicationBranch: "main", Revision: 4},
		{State: "configured", RepositoryID: "owner/repo", PublicationBranch: "release", Revision: 4},
		{State: "configured", RepositoryID: "owner/repo", PublicationBranch: "main", Revision: 4},
	} {
		if prototypeBindingMatchesCatalog(changed, catalog) {
			t.Fatalf("changed binding reused cached prototype: %+v", changed)
		}
	}
}

func TestPrototypeProjectionRejectsChangedKnowledgeAtSameGitRevision(t *testing.T) {
	binding := RepositoryBinding{State: "configured", RepositoryID: "owner/repo", PublicationBranch: "main", Revision: 3}
	revision := strings.Repeat("a", 40)
	active := prototypeActiveDocument{
		RepositoryID: binding.RepositoryID, PublicationBranch: binding.PublicationBranch,
		BindingRevision: binding.Revision, Revision: revision, EvaluatorVersion: prototypeEvaluatorVersion,
		KnowledgeGeneration: "generation-1", KnowledgeDigest: "digest-1",
	}
	if !prototypeProjectionIdentityMatches(active, binding, revision, "generation-1", "digest-1") {
		t.Fatal("unchanged knowledge and Git identity must match")
	}
	for _, changed := range []struct{ generation, digest string }{
		{"generation-2", "digest-1"},
		{"generation-1", "digest-2"},
		{"", ""},
	} {
		if prototypeProjectionIdentityMatches(active, binding, revision, changed.generation, changed.digest) {
			t.Fatalf("stale knowledge projection matched current identity: %+v", changed)
		}
	}
}

func TestPrototypeAllowlistIncludesDeclaredNavigationAndEvidencePathsOnly(t *testing.T) {
	item := ac.Item{
		AllowedPaths: []string{"prototypes/demo/prototype.json", "prototypes/demo/index.html", "prototypes/demo/media/cover.webp"},
		Related:      []ac.Related{{Kind: "todo", Path: "todos/active/evidence.md"}},
		Screens:      []ac.Screen{{States: []ac.State{{ApprovedReferences: []ac.ApprovedReference{{ApprovalEvidence: "evidence/approval.md"}}}}}},
	}
	allowed := prototypeAllowedPaths(PrototypeCatalogResponse{navigation: []ac.Item{item}})
	for _, path := range []string{"prototypes/demo/prototype.json", "prototypes/demo/index.html", "prototypes/demo/media/cover.webp", "todos/active/evidence.md", "evidence/approval.md"} {
		if _, ok := allowed[path]; !ok {
			t.Errorf("declared path %q was not admitted", path)
		}
	}
	for _, path := range []string{"prototypes/demo/secret.env", "../outside", "/etc/passwd"} {
		if _, ok := allowed[path]; ok {
			t.Errorf("undeclared or unsafe path %q was admitted", path)
		}
	}
}

func TestPrototypeFileCacheCopiesBytesAndEvictsOldEntries(t *testing.T) {
	cache := newPrototypeFileCache()
	first := prototypeFileResponse{Data: []byte("first"), ContentType: "text/plain"}
	cache.put("first", first)
	first.Data[0] = 'X'
	read, ok := cache.get("first")
	if !ok || string(read.Data) != "first" {
		t.Fatalf("cache did not copy inserted bytes: %#v, %t", read, ok)
	}
	read.Data[0] = 'Y'
	readAgain, _ := cache.get("first")
	if string(readAgain.Data) != "first" {
		t.Fatalf("cache returned shared mutable bytes: %q", readAgain.Data)
	}
	for index := 0; index < prototypeFileCacheEntries; index++ {
		cache.put("entry-"+string(rune('a'+index)), prototypeFileResponse{Data: []byte("x")})
	}
	if _, ok := cache.get("first"); ok {
		t.Fatal("oldest cache entry was not evicted at the entry bound")
	}
}

type prototypeHTTPTestReader struct{}

type prototypeHTTPTestService struct {
	Service
	prototypeHTTPTestReader
}

func (prototypeHTTPTestReader) ReadPrototypes(context.Context, string, string) (PrototypeCatalogResponse, error) {
	return PrototypeCatalogResponse{}, nil
}
func (prototypeHTTPTestReader) ReadPrototype(context.Context, string, string, string, string) (PrototypeReadResponse, error) {
	return PrototypeReadResponse{}, nil
}
func (prototypeHTTPTestReader) ReadPrototypeFile(context.Context, string, string, string, string) (prototypeFileResponse, error) {
	return prototypeFileResponse{Data: []byte("<main>ok</main>"), ContentType: "text/html", ETag: `"revision-content"`, Revision: strings.Repeat("a", 40), InventoryDigest: "sha256:" + strings.Repeat("b", 64)}, nil
}

func TestPrototypeFileRouteSupportsHeadAndConditionalGet(t *testing.T) {
	handler := NewHandler(prototypeHTTPTestService{})
	path := "/internal/v1/companies/company-a/projects/project-a/prototypes/" + strings.Repeat("a", 40) + "/files/prototypes/demo/index.html"
	head := httptest.NewRecorder()
	handler.ServeHTTP(head, httptest.NewRequest(http.MethodHead, path, nil))
	if head.Code != http.StatusOK || head.Body.Len() != 0 || head.Header().Get("Content-Length") != "15" || head.Header().Get("X-Prototype-Revision") != strings.Repeat("a", 40) || head.Header().Get("X-Prototype-Inventory-Digest") == "" {
		t.Fatalf("HEAD response status=%d headers=%v body=%q", head.Code, head.Header(), head.Body.String())
	}
	conditional := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.Header.Set("If-None-Match", `"revision-content"`)
	handler.ServeHTTP(conditional, request)
	if conditional.Code != http.StatusNotModified || conditional.Body.Len() != 0 {
		t.Fatalf("conditional GET status=%d body=%q", conditional.Code, conditional.Body.String())
	}
}

var _ http.Handler = NewHandler(prototypeHTTPTestService{})
