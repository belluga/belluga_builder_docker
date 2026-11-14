package registry

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	ac "delphi-local-api/artifactcatalog"
)

func TestReadPrototypeFileUsesOneEpochFenceAndRejectsStalePinnedURLs(t *testing.T) {
	const revision = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const pathA = "prototypes/demo/a.html"
	const pathB = "prototypes/demo/b.html"
	project := Project{ID: "project-a", CompanyID: "company-a", Binding: &Binding{RepositoryID: "owner/repo", PublicationBranch: "main"}, BindingRevision: 3, KnowledgeGenerationID: "generation-a", CanonicalWriteEpoch: 9}
	catalog := identityTestCatalog("company-a", "project-a", "owner/repo", revision, 3, 9, "generation-a", pathA, pathB)
	store := &MongoStore{prototypeHot: newPrototypeHotCache(), prototypeFileCache: newPrototypeFileCache()}
	var manifests atomic.Int32
	store.prototypeCanonicalManifestReader = func(context.Context, string, string) (CanonicalManifest, error) {
		manifests.Add(1)
		return CanonicalManifest{}, nil
	}
	store.prototypeHot.replace(project.CompanyID, project.ID, &catalog)
	store.prototypeFileCache.put(prototypeFileCacheKey(project.CompanyID, project.ID, catalog, pathA), prototypeFileResponse{Data: []byte("A"), Revision: revision, InventoryDigest: catalog.InventoryDigest})
	store.prototypeFileCache.put(prototypeFileCacheKey(project.CompanyID, project.ID, catalog, pathB), prototypeFileResponse{Data: []byte("B"), Revision: revision, InventoryDigest: catalog.InventoryDigest})
	var reads atomic.Int32
	store.prototypeIdentityReader = func(context.Context, string, string) (Project, error) {
		reads.Add(1)
		return project, nil
	}
	for _, path := range []string{pathA, pathB, pathA} {
		file, err := store.ReadPrototypeFile(context.Background(), project.CompanyID, project.ID, revision, path)
		if err != nil || len(file.Data) != 1 {
			t.Fatalf("warm pinned file %s: %+v, %v", path, file, err)
		}
	}
	if got := reads.Load(); got != 3 {
		t.Fatalf("warm file requests did not use exactly one authoritative Project read apiece: got %d, want 3", got)
	}
	if got := manifests.Load(); got != 0 {
		t.Fatalf("warm file requests reread canonical manifests: got %d reads, want 0", got)
	}
	project.CanonicalWriteEpoch++
	if _, err := store.ReadPrototypeFile(context.Background(), project.CompanyID, project.ID, revision, pathA); err != ErrConflict {
		t.Fatalf("stale Knowledge URL survived canonical write epoch change: %v", err)
	}
	project.CanonicalWriteEpoch--
	project.BindingRevision++
	if _, err := store.ReadPrototypeFile(context.Background(), project.CompanyID, project.ID, revision, pathA); err != ErrConflict {
		t.Fatalf("stale URL survived repository binding change: %v", err)
	}
	project.BindingRevision--
	project.KnowledgeGenerationID = "generation-b"
	if _, err := store.ReadPrototypeFile(context.Background(), project.CompanyID, project.ID, revision, pathA); err != ErrConflict {
		t.Fatalf("stale URL survived Knowledge activation change: %v", err)
	}
}

func TestPrototypeProjectEpochFenceIsolatedAndConcurrent(t *testing.T) {
	const revision = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	store := &MongoStore{prototypeHot: newPrototypeHotCache(), prototypeFileCache: newPrototypeFileCache()}
	projects := map[string]Project{
		"project-a": {ID: "project-a", CompanyID: "company-a", Binding: &Binding{RepositoryID: "owner/a", PublicationBranch: "main"}, BindingRevision: 1, KnowledgeGenerationID: "generation-a", CanonicalWriteEpoch: 4},
		"project-b": {ID: "project-b", CompanyID: "company-a", Binding: &Binding{RepositoryID: "owner/b", PublicationBranch: "main"}, BindingRevision: 2, KnowledgeGenerationID: "generation-b", CanonicalWriteEpoch: 7},
	}
	var reads atomic.Int32
	store.prototypeIdentityReader = func(_ context.Context, companyID, projectID string) (Project, error) {
		reads.Add(1)
		project, ok := projects[projectID]
		if !ok || project.CompanyID != companyID {
			return Project{}, ErrNotFound
		}
		return project, nil
	}
	for projectID, project := range projects {
		catalog := identityTestCatalog(project.CompanyID, projectID, project.Binding.RepositoryID, revision, project.BindingRevision, project.CanonicalWriteEpoch, project.KnowledgeGenerationID,
			"prototypes/demo/index.html")
		store.prototypeHot.replace(project.CompanyID, projectID, &catalog)
		store.prototypeFileCache.put(prototypeFileCacheKey(project.CompanyID, projectID, catalog, "prototypes/demo/index.html"), prototypeFileResponse{Data: []byte(projectID), Revision: revision, InventoryDigest: catalog.InventoryDigest})
	}
	for projectID, expected := range map[string]string{"project-a": "project-a", "project-b": "project-b"} {
		file, err := store.ReadPrototypeFile(context.Background(), "company-a", projectID, revision, "prototypes/demo/index.html")
		if err != nil || string(file.Data) != expected {
			t.Fatalf("Project %s crossed an identity/cache boundary: %q, %v", projectID, file.Data, err)
		}
	}
	if reads.Load() != 2 {
		t.Fatalf("each Project must be fenced independently: reads=%d", reads.Load())
	}
	var wait sync.WaitGroup
	for i := 0; i < 12; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if _, err := store.ReadPrototypeFile(context.Background(), "company-a", "project-a", revision, "prototypes/demo/index.html"); err != nil {
				t.Errorf("concurrent warm read failed: %v", err)
			}
		}()
	}
	wait.Wait()
	if reads.Load() != 14 {
		t.Fatalf("concurrent warm reads skipped or duplicated their one-read fence: reads=%d", reads.Load())
	}
}

func TestPrototypeEpochFenceMutationDuringReadHasSingleReadLinearization(t *testing.T) {
	const revision = "cccccccccccccccccccccccccccccccccccccccc"
	const path = "prototypes/demo/index.html"
	project := Project{ID: "project-a", CompanyID: "company-a", Binding: &Binding{RepositoryID: "owner/repo", PublicationBranch: "main"}, BindingRevision: 1, KnowledgeGenerationID: "generation-a", CanonicalWriteEpoch: 4}
	catalog := identityTestCatalog("company-a", "project-a", "owner/repo", revision, 1, 4, "generation-a", path)
	store := &MongoStore{prototypeHot: newPrototypeHotCache(), prototypeFileCache: newPrototypeFileCache()}
	store.prototypeHot.replace(project.CompanyID, project.ID, &catalog)
	store.prototypeFileCache.put(prototypeFileCacheKey(project.CompanyID, project.ID, catalog, path), prototypeFileResponse{Data: []byte("pinned bytes"), Revision: revision, InventoryDigest: catalog.InventoryDigest})
	readStarted, releaseRead := make(chan struct{}), make(chan struct{})
	var reads atomic.Int32
	var mu sync.Mutex
	store.prototypeIdentityReader = func(context.Context, string, string) (Project, error) {
		reads.Add(1)
		mu.Lock()
		observed := project
		mu.Unlock()
		if reads.Load() == 1 {
			close(readStarted)
			<-releaseRead
		}
		return observed, nil
	}
	firstDone := make(chan error, 1)
	go func() {
		file, err := store.ReadPrototypeFile(context.Background(), project.CompanyID, project.ID, revision, path)
		if err == nil && string(file.Data) != "pinned bytes" {
			err = ErrConflict
		}
		firstDone <- err
	}()
	<-readStarted
	mu.Lock()
	project.CanonicalWriteEpoch++
	mu.Unlock()
	close(releaseRead)
	if err := <-firstDone; err != nil {
		t.Fatalf("in-flight read that linearized before mutation should complete: %v", err)
	}
	if _, err := store.ReadPrototypeFile(context.Background(), project.CompanyID, project.ID, revision, path); err != ErrConflict {
		t.Fatalf("read after concurrent epoch mutation was not rejected: %v", err)
	}
	if reads.Load() != 2 {
		t.Fatalf("each request must make one fenced Project read: got %d", reads.Load())
	}
}

func TestPrototypeEpochFenceDoesNotMaskRegistryOutage(t *testing.T) {
	store := &MongoStore{prototypeHot: newPrototypeHotCache(), prototypeFileCache: newPrototypeFileCache()}
	store.prototypeIdentityReader = func(context.Context, string, string) (Project, error) {
		return Project{}, ErrUnavailable
	}
	catalog := identityTestCatalog("company-a", "project-a", "owner/repo", "dddddddddddddddddddddddddddddddddddddddd", 1, 1, "generation-a", "prototypes/demo/index.html")
	if err := store.fencePrototypeIdentity(context.Background(), "company-a", "project-a", catalog); err != ErrUnavailable {
		t.Fatalf("identity read outage must remain unavailable, not be misreported as stale URL: %v", err)
	}
}

func identityTestCatalog(companyID, projectID, repositoryID, revision string, bindingRevision, writeEpoch int64, generation string, paths ...string) PrototypeCatalogResponse {
	allowed := make([]string, len(paths))
	copy(allowed, paths)
	return PrototypeCatalogResponse{
		RepositoryID: repositoryID, PublicationBranch: "main", BindingRevision: bindingRevision, Revision: revision,
		EvaluatorVersion: prototypeEvaluatorVersion, InventoryDigest: "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		KnowledgeGeneration: generation, KnowledgeDigest: "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
		KnowledgeWriteEpoch: writeEpoch, navigation: []ac.Item{{ID: "demo", AllowedPaths: allowed}},
	}
}
