package registry

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	ac "belluga-builder-viewer/artifact_catalog"
)

func TestGitDesignSystemReadFetchesBlobsBeforeCredentiallessEvaluation(t *testing.T) {
	root := t.TempDir()
	remote := filepath.Join(root, "private.git")
	work := filepath.Join(root, "work")
	runFixtureGit(t, root, "init", "--bare", "--quiet", remote)
	runFixtureGit(t, remote, "config", "uploadpack.allowFilter", "true")
	if err := os.Mkdir(work, 0o700); err != nil {
		t.Fatal(err)
	}
	runFixtureGit(t, work, "init", "--quiet", "--initial-branch=main")
	runFixtureGit(t, work, "config", "user.name", "Private Registry Fixture")
	runFixtureGit(t, work, "config", "user.email", "private-fixture@example.invalid")
	manifest := []byte(`{"schema_version":"1","id":"private-system","name":"Private Fixture","owner":{"level":"project","id":"private-project"},"tokens":[{"id":"label","type":"string","value":"Private"}],"components":[],"assets":[],"contrast_pairs":[]}`)
	manifestPath := filepath.Join(work, filepath.FromSlash(designSystemDefinitionPath))
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	runFixtureGit(t, work, "add", ".")
	runFixtureGit(t, work, "commit", "--quiet", "-m", "private fixture")
	revision := fixtureGitOutput(t, work, "rev-parse", "HEAD")
	runFixtureGit(t, work, "remote", "add", "origin", remote)
	runFixtureGit(t, work, "push", "--quiet", "origin", "main")

	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	// The fetch wrapper makes the private remote unavailable immediately after
	// fetch. A blobless partial fetch used to pass credentials only to fetch,
	// then fail when the evaluator lazily requested the manifest blob.
	hiddenRemote := remote + ".temporarily-hidden"
	wrapper := filepath.Join(root, "git-fetch-then-hide-remote")
	program := fmt.Sprintf("#!/bin/sh\n%s \"$@\"\nstatus=$?\nif [ \"$1\" = fetch ]; then /bin/mv '%s' '%s'; fi\nexit $status\n", gitPath, remote, hiddenRemote)
	if err := os.WriteFile(wrapper, []byte(program), 0o700); err != nil {
		t.Fatal(err)
	}
	binding := validTestBinding()
	binding.RepositoryID = "belluga/private-fixture"
	verifier := gitDesignSystemVerifier{
		repositoryURL: func(string) string { return remote }, gitExecutable: wrapper, readCache: newDesignSystemReadCache(),
	}
	evaluation, _, err := verifier.ReadDesignSystem(context.Background(), "private-project", "company-a", binding.RepositoryID, revision, "main", "project", "")
	if err != nil || evaluation.Outcome != "go" || evaluation.DesignSystemValidation != "valid" {
		t.Fatalf("exact shallow private read required an uncredentialed lazy blob fetch: outcome=%s validation=%s err=%v", evaluation.Outcome, evaluation.DesignSystemValidation, err)
	}
}

func TestGitDesignSystemReadCacheServesMultipleAssetsWithOneFetch(t *testing.T) {
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	work := filepath.Join(root, "work")
	runFixtureGit(t, root, "init", "--bare", "--quiet", remote)
	if err := os.Mkdir(work, 0o700); err != nil {
		t.Fatal(err)
	}
	runFixtureGit(t, work, "init", "--quiet", "--initial-branch=main")
	runFixtureGit(t, work, "config", "user.name", "Registry Fixture")
	runFixtureGit(t, work, "config", "user.email", "registry-fixture@example.invalid")
	assetNames := make([]string, 65)
	assetPaths := make([]string, len(assetNames))
	for index := range assetNames {
		assetNames[index] = fmt.Sprintf("asset-%02d.svg", index)
		assetPaths[index] = fmt.Sprintf("%q", "assets/"+assetNames[index])
	}
	manifest := []byte(fmt.Sprintf(`{"schema_version":"1","id":"cached-system","name":"Cached Fixture","owner":{"level":"project","id":"cached-project"},"tokens":[{"id":"label","type":"string","value":"Cached"}],"components":[],"assets":[%s],"contrast_pairs":[]}`, strings.Join(assetPaths, ",")))
	manifestPath := filepath.Join(work, filepath.FromSlash(designSystemDefinitionPath))
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range assetNames {
		path := filepath.Join(work, designSystemRoot, "assets", name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("<svg>"+name+"</svg>"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	runFixtureGit(t, work, "add", ".")
	runFixtureGit(t, work, "commit", "--quiet", "-m", "cached fixture")
	revision := fixtureGitOutput(t, work, "rev-parse", "HEAD")
	runFixtureGit(t, work, "remote", "add", "origin", remote)
	runFixtureGit(t, work, "push", "--quiet", "origin", "main")

	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	fetchCounter := filepath.Join(root, "fetch-count")
	wrapper := filepath.Join(root, "counting-git")
	program := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = fetch ]; then printf x >> '%s'; fi\nexec %s \"$@\"\n", fetchCounter, gitPath)
	if err := os.WriteFile(wrapper, []byte(program), 0o700); err != nil {
		t.Fatal(err)
	}
	binding := validTestBinding()
	binding.RepositoryID = "belluga/cache-fixture"
	verifier := gitDesignSystemVerifier{repositoryURL: func(string) string { return remote }, gitExecutable: wrapper, readCache: newDesignSystemReadCache()}
	if evaluation, _, err := verifier.ReadDesignSystem(context.Background(), "cached-project", "company-a", binding.RepositoryID, revision, "main", "project", ""); err != nil || evaluation.Outcome != "go" {
		t.Fatalf("cache warm detail read: outcome=%s err=%v", evaluation.Outcome, err)
	}
	for path, want := range map[string]string{"assets/asset-00.svg": "<svg>asset-00.svg</svg>", "assets/asset-64.svg": "<svg>asset-64.svg</svg>"} {
		_, data, err := verifier.ReadDesignSystem(context.Background(), "cached-project", "company-a", binding.RepositoryID, revision, "main", "project", path)
		if err != nil || string(data) != want {
			t.Fatalf("cached exact-SHA asset %s = %q, %v", path, data, err)
		}
	}
	count, err := os.ReadFile(fetchCounter)
	if err != nil || len(count) != 1 {
		t.Fatalf("multiple asset reads repeated remote fetch/evaluation: fetches=%d err=%v", len(count), err)
	}
}

func TestGitDesignSystemPersistentCacheHitAfterVerifierRecreation(t *testing.T) {
	root := t.TempDir()
	remote, _, revision := createDesignSystemGitFixture(t, root, "First", nil)
	cacheRoot := filepath.Join(root, "prototype-cache")
	if err := os.Mkdir(cacheRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	fetchCounter := filepath.Join(root, "fetch-count")
	wrapper := countingGitWrapper(t, root, fetchCounter)
	t.Setenv("DELPHI_PROTOTYPE_GIT_CACHE_ROOT", cacheRoot)
	newVerifier := func() gitDesignSystemVerifier {
		verifier, ok := newGitDesignSystemVerifier().(gitDesignSystemVerifier)
		if !ok {
			t.Fatal("default Design System verifier has an unexpected implementation")
		}
		verifier.repositoryURL = func(string) string { return remote }
		verifier.gitExecutable = wrapper
		return verifier
	}
	const companyID, projectID, repositoryID = "company-a", "fixture-project", "belluga/cache-fixture"
	first, _, err := newVerifier().ReadDesignSystem(context.Background(), projectID, companyID, repositoryID, revision, "main", "project", "")
	if err != nil || first.Outcome != "go" || first.InventoryDigest == nil {
		t.Fatalf("persistent-cache warm read: outcome=%s digest=%v err=%v", first.Outcome, first.InventoryDigest, err)
	}
	second, asset, err := newVerifier().ReadDesignSystem(context.Background(), projectID, companyID, repositoryID, revision, "main", "project", "assets/mark.svg")
	if err != nil || second.Outcome != "go" || second.Revision == nil || *second.Revision != revision || second.InventoryDigest == nil || *second.InventoryDigest != *first.InventoryDigest || string(asset) != "<svg>First</svg>" {
		t.Fatalf("recreated verifier did not read the persisted exact-SHA detail/asset: evaluation=%+v asset=%q err=%v", second, asset, err)
	}
	fetches, err := os.ReadFile(fetchCounter)
	if err != nil || len(fetches) != 1 {
		t.Fatalf("second verifier fetched Git objects after a persistent hit: fetches=%d err=%v", len(fetches), err)
	}
}

func TestGitDesignSystemUsesExactSHAForColdAndNonCurrentCacheFallback(t *testing.T) {
	root := t.TempDir()
	remote, work, revisionA := createDesignSystemGitFixture(t, root, "Pinned", nil)
	revisionB := commitDesignSystemGitFixture(t, work, "Current")
	runFixtureGit(t, work, "push", "--quiet", "origin", "main")
	if revisionA == revisionB {
		t.Fatal("fixture revisions should differ")
	}
	const companyID, projectID, repositoryID = "company-a", "fixture-project", "belluga/cache-fixture"

	coldCounter := filepath.Join(root, "cold-fetch-count")
	cold := gitDesignSystemVerifier{repositoryURL: func(string) string { return remote }, gitExecutable: countingGitWrapper(t, root, coldCounter), readCache: newDesignSystemReadCache()}
	coldEvaluation, _, err := cold.ReadDesignSystem(context.Background(), projectID, companyID, repositoryID, revisionA, "main", "project", "")
	if err != nil || coldEvaluation.Outcome != "go" || coldEvaluation.Revision == nil || *coldEvaluation.Revision != revisionA {
		t.Fatalf("cold exact-SHA fallback substituted current HEAD or failed: evaluation=%+v err=%v", coldEvaluation, err)
	}
	coldFetches, err := os.ReadFile(coldCounter)
	if err != nil || len(coldFetches) != 1 {
		t.Fatalf("cold exact-SHA fallback fetch count=%d err=%v", len(coldFetches), err)
	}

	cacheCounter := filepath.Join(root, "noncurrent-fetch-count")
	cacheRoot := filepath.Join(root, "prototype-cache")
	if err := os.Mkdir(cacheRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	cached := gitDesignSystemVerifier{
		repositoryURL: func(string) string { return remote }, gitExecutable: countingGitWrapper(t, root, cacheCounter),
		readCache: newDesignSystemReadCache(), prototypeCacheRoot: cacheRoot,
	}
	selected, _, err := cached.ReadDesignSystem(context.Background(), projectID, companyID, repositoryID, revisionA, "main", "project", "")
	if err != nil || selected.Outcome != "go" || selected.Revision == nil || *selected.Revision != revisionA || string(selected.Items[0].Tokens[0].Value) != `"Pinned"` {
		t.Fatalf("non-current selected SHA did not use its exact temporary fallback: evaluation=%+v err=%v", selected, err)
	}
	cacheFetches, err := os.ReadFile(cacheCounter)
	if err != nil || len(cacheFetches) != 2 {
		t.Fatalf("non-current cache fallback should fetch current cache ref then exact selected SHA: fetches=%d err=%v", len(cacheFetches), err)
	}
}

func TestGitDesignSystemConfiguredCacheRejectionFallsBackToExactSHA(t *testing.T) {
	root := t.TempDir()
	remote, work, revisionA := createDesignSystemGitFixture(t, root, "Pinned", nil)
	revisionB := commitDesignSystemGitFixture(t, work, "Current")
	runFixtureGit(t, work, "push", "--quiet", "origin", "main")
	if revisionA == revisionB {
		t.Fatal("fixture revisions should differ")
	}
	cacheRoot := filepath.Join(root, "cache-root-is-a-file")
	if err := os.WriteFile(cacheRoot, []byte("reject cache root"), 0o600); err != nil {
		t.Fatal(err)
	}
	fetchCounter := filepath.Join(root, "fetch-count")
	verifier := gitDesignSystemVerifier{
		repositoryURL: func(string) string { return remote }, gitExecutable: countingGitWrapper(t, root, fetchCounter),
		readCache: newDesignSystemReadCache(), prototypeCacheRoot: cacheRoot,
	}
	const companyID, projectID, repositoryID = "company-a", "fixture-project", "belluga/cache-fixture"
	evaluation, _, err := verifier.ReadDesignSystem(context.Background(), projectID, companyID, repositoryID, revisionA, "main", "project", "")
	if err != nil || evaluation.Outcome != "go" || evaluation.Revision == nil || *evaluation.Revision != revisionA || string(evaluation.Items[0].Tokens[0].Value) != `"Pinned"` {
		t.Fatalf("rejected persistent cache did not fall back to the exact selected SHA: evaluation=%+v err=%v", evaluation, err)
	}
	fetches, err := os.ReadFile(fetchCounter)
	if err != nil || len(fetches) != 1 {
		t.Fatalf("rejected cache fallback should perform exactly one temporary fetch: fetches=%d err=%v", len(fetches), err)
	}

	absentSHA := strings.Repeat("f", 40)
	if absentSHA == revisionA || absentSHA == revisionB {
		t.Fatal("fixture unexpectedly uses the absent SHA")
	}
	missing, _, err := verifier.ReadDesignSystem(context.Background(), projectID, companyID, repositoryID, absentSHA, "main", "project", "")
	if err == nil {
		t.Fatalf("absent selected SHA must fail closed instead of substituting branch HEAD: evaluation=%+v err=%v", missing, err)
	}
	fetches, err = os.ReadFile(fetchCounter)
	if err != nil || len(fetches) != 2 {
		t.Fatalf("absent exact-SHA attempt should be the only additional fetch: fetches=%d err=%v", len(fetches), err)
	}
}

func TestGitDesignSystemMissingPersistentBlobFallsBackToExactSHA(t *testing.T) {
	root := t.TempDir()
	remote, work, revision := createDesignSystemGitFixture(t, root, "Blob fallback", nil)
	const companyID, projectID, repositoryID = "company-a", "fixture-project", "belluga/cache-fixture"
	cacheRoot := filepath.Join(root, "prototype-cache")
	if err := os.Mkdir(cacheRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	workspace := designSystemGitCacheWorkspace(cacheRoot, companyID, projectID, repositoryID, remote)
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	runFixtureGit(t, workspace, "init", "--quiet")
	runFixtureGit(t, workspace, "remote", "add", "origin", remote)
	manifestOID := fixtureGitOutput(t, work, "rev-parse", revision+":"+designSystemDefinitionPath)
	for objectID := range gitRevisionObjectIDs(t, work, revision) {
		if objectID == manifestOID {
			continue
		}
		objectType := fixtureGitOutput(t, work, "cat-file", "-t", objectID)
		objectBytes := fixtureGitBytes(t, work, "cat-file", objectType, objectID)
		command := exec.Command("git", "hash-object", "-w", "-t", objectType, "--stdin")
		command.Dir = workspace
		command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
		command.Stdin = bytes.NewReader(objectBytes)
		output, err := command.Output()
		if err != nil || strings.TrimSpace(string(output)) != objectID {
			t.Fatalf("seed selected commit/tree while omitting its manifest blob: object=%s type=%s got=%q err=%v", objectID, objectType, output, err)
		}
	}
	if _, err := exec.Command("git", "-C", workspace, "cat-file", "-e", manifestOID).Output(); err == nil {
		t.Fatal("fixture unexpectedly contains the selected manifest blob")
	}
	if _, err := exec.Command("git", "-C", workspace, "cat-file", "-e", revision+"^{commit}").Output(); err != nil {
		t.Fatalf("fixture must contain the selected commit and tree: %v", err)
	}
	fetchCounter := filepath.Join(root, "fetch-count")
	verifier := gitDesignSystemVerifier{
		repositoryURL: func(string) string { return remote }, gitExecutable: countingGitWrapper(t, root, fetchCounter),
		readCache: newDesignSystemReadCache(), prototypeCacheRoot: cacheRoot,
	}
	evaluation, _, err := verifier.ReadDesignSystem(context.Background(), projectID, companyID, repositoryID, revision, "main", "project", "")
	if err != nil || evaluation.Outcome != "go" || evaluation.DesignSystemValidation != "valid" || evaluation.Revision == nil || *evaluation.Revision != revision || evaluation.InventoryDigest == nil {
		t.Fatalf("missing cached manifest blob did not recover through an exact temporary fetch: evaluation=%+v err=%v", evaluation, err)
	}
	newSource, err := ac.NewGitTree(work, revision)
	if err != nil {
		t.Fatal(err)
	}
	expectedRevision := revision
	expected := ac.EvaluateDesignSystem(newSource, projectID, companyID, "project", ac.SourceBinding{
		RepositoryID: repositoryID, Revision: &expectedRevision, DefinitionPath: designSystemDefinitionPath,
	})
	if expected.Outcome != "go" || expected.InventoryDigest == nil || *evaluation.InventoryDigest != *expected.InventoryDigest {
		t.Fatalf("exact fallback digest differs from full source evaluation: got=%v expected=%v", evaluation.InventoryDigest, expected.InventoryDigest)
	}
	fetches, err := os.ReadFile(fetchCounter)
	if err != nil || len(fetches) != 1 {
		t.Fatalf("missing-blob recovery should perform one exact temporary fetch, got %d, err=%v", len(fetches), err)
	}
	if _, err := exec.Command("git", "-C", workspace, "cat-file", "-e", manifestOID).Output(); err == nil {
		t.Fatal("exact temporary fallback unexpectedly repaired or mutated the incomplete persistent cache")
	}
}

func TestGitDesignSystemCompleteMissingAssetDoesNotFallback(t *testing.T) {
	root := t.TempDir()
	remote, work, _ := createDesignSystemGitFixture(t, root, "Initial", nil)
	invalidManifest := []byte(`{"schema_version":"1","id":"fixture-system","name":"Fixture","owner":{"level":"project","id":"fixture-project"},"tokens":[{"id":"label","type":"string","value":"Missing asset"}],"components":[],"assets":["assets/absent.svg"],"contrast_pairs":[]}`)
	writeDesignSystemGitFixtureManifest(t, work, invalidManifest)
	runFixtureGit(t, work, "add", designSystemDefinitionPath)
	runFixtureGit(t, work, "commit", "--quiet", "-m", "Design System references an absent asset")
	missingAssetRevision := fixtureGitOutput(t, work, "rev-parse", "HEAD")
	runFixtureGit(t, work, "push", "--quiet", "origin", "main")
	cacheRoot := filepath.Join(root, "prototype-cache")
	if err := os.Mkdir(cacheRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	fetchCounter := filepath.Join(root, "fetch-count")
	verifier := gitDesignSystemVerifier{
		repositoryURL: func(string) string { return remote }, gitExecutable: countingGitWrapper(t, root, fetchCounter),
		readCache: newDesignSystemReadCache(), prototypeCacheRoot: cacheRoot,
	}
	evaluation, _, err := verifier.ReadDesignSystem(context.Background(), "fixture-project", "company-a", "belluga/cache-fixture", missingAssetRevision, "main", "project", "")
	if err != nil || evaluation.Outcome == "go" || !designSystemHasDiagnostic(evaluation, "missing_file") {
		t.Fatalf("complete but semantically invalid Design System did not retain its missing-asset result: evaluation=%+v err=%v", evaluation, err)
	}
	fetches, err := os.ReadFile(fetchCounter)
	if err != nil || len(fetches) != 1 {
		t.Fatalf("complete semantic invalidity must not trigger an exact-SHA fallback: fetches=%d err=%v", len(fetches), err)
	}
}

func TestGitDesignSystemRevisionAdvanceAddsOnlyMissingObjects(t *testing.T) {
	root := t.TempDir()
	largeBlob := make([]byte, 2<<20)
	if _, err := rand.New(rand.NewSource(1701)).Read(largeBlob); err != nil {
		t.Fatal(err)
	}
	remote, work, revisionA := createDesignSystemGitFixture(t, root, "First", largeBlob)
	remoteURL := "file://" + remote
	const companyID, projectID, repositoryID = "company-a", "fixture-project", "belluga/cache-fixture"
	cacheRoot := filepath.Join(root, "prototype-cache")
	if err := os.Mkdir(cacheRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	verifier := gitDesignSystemVerifier{repositoryURL: func(string) string { return remoteURL }, readCache: newDesignSystemReadCache(), prototypeCacheRoot: cacheRoot}
	first, _, err := verifier.ReadDesignSystem(context.Background(), projectID, companyID, repositoryID, revisionA, "main", "project", "")
	if err != nil || first.Outcome != "go" || first.InventoryDigest == nil {
		t.Fatalf("initial persistent-cache read: evaluation=%+v err=%v", first, err)
	}
	cacheWorkspace := designSystemGitCacheWorkspace(cacheRoot, companyID, projectID, repositoryID, remoteURL)
	objectsBefore := gitObjectIDs(t, cacheWorkspace)
	largeOID := fixtureGitOutput(t, work, "rev-parse", revisionA+":outside/large.bin")
	if _, found := objectsBefore[largeOID]; !found {
		t.Fatal("large unchanged blob was not present in the warmed object database")
	}

	outsidePath := filepath.Join(work, "outside", "added-after-advance.txt")
	if err := os.WriteFile(outsidePath, []byte("new non-Design-System object"), 0o600); err != nil {
		t.Fatal(err)
	}
	runFixtureGit(t, work, "add", "outside/added-after-advance.txt")
	revisionB := commitDesignSystemGitFixture(t, work, "Changed")
	runFixtureGit(t, work, "push", "--quiet", "origin", "main")
	beforeCapture := filepath.Join(root, "object-ids-before-fetch")
	afterCapture := filepath.Join(root, "object-ids-after-fetch")
	packTrace := filepath.Join(root, "received-pack.pack")
	fetchCounter := filepath.Join(root, "fetch-count")
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	wrapper := gitObjectDeltaWrapper(t, root, gitPath, beforeCapture, afterCapture, packTrace, fetchCounter)
	verifier.gitExecutable = wrapper
	verifier.readCache = newDesignSystemReadCache()
	second, _, err := verifier.ReadDesignSystem(context.Background(), projectID, companyID, repositoryID, revisionB, "main", "project", "")
	if err != nil || second.Outcome != "go" || second.DesignSystemValidation != "valid" || second.Revision == nil || *second.Revision != revisionB || second.InventoryDigest == nil {
		t.Fatalf("advanced exact SHA was not evaluated in full: evaluation=%+v err=%v", second, err)
	}
	newSource, err := ac.NewGitTree(work, revisionB)
	if err != nil {
		t.Fatal(err)
	}
	expectedRevision := revisionB
	expected := ac.EvaluateDesignSystem(newSource, projectID, companyID, "project", ac.SourceBinding{
		RepositoryID: repositoryID, Revision: &expectedRevision, DefinitionPath: designSystemDefinitionPath,
	})
	if expected.Outcome != "go" || expected.InventoryDigest == nil || *second.InventoryDigest != *expected.InventoryDigest || *second.InventoryDigest == *first.InventoryDigest {
		gotDigest, expectedDigest, previousDigest := "", "", ""
		if second.InventoryDigest != nil {
			gotDigest = *second.InventoryDigest
		}
		if expected.InventoryDigest != nil {
			expectedDigest = *expected.InventoryDigest
		}
		if first.InventoryDigest != nil {
			previousDigest = *first.InventoryDigest
		}
		t.Fatalf("new SHA digest does not match full repository evaluation: got=%q expected=%q previous=%q", gotDigest, expectedDigest, previousDigest)
	}
	objectsBeforeFetch := objectIDsFromFile(t, beforeCapture)
	objectsAfterFetch := objectIDsFromFile(t, afterCapture)
	if len(objectsBeforeFetch) == 0 || len(objectsAfterFetch) == 0 {
		t.Fatalf("fetch object inventory was not captured: before=%d after=%d", len(objectsBeforeFetch), len(objectsAfterFetch))
	}
	reachableB := gitRevisionObjectIDs(t, work, revisionB)
	expectedAdded := objectIDDifference(reachableB, objectsBefore)
	added := objectIDDifference(objectsAfterFetch, objectsBeforeFetch)
	if !equalObjectIDs(added, expectedAdded) {
		t.Fatalf("fetch object delta differs from missing objects required by the new SHA: added=%d expected=%d", len(added), len(expectedAdded))
	}
	if _, found := objectsAfterFetch[largeOID]; !found {
		t.Fatal("unchanged large blob disappeared from the object database after branch advance")
	}
	if _, fetched := added[largeOID]; fetched {
		t.Fatal("unchanged large blob was fetched again instead of reused")
	}
	pack, err := os.ReadFile(packTrace)
	if err != nil || len(pack) < 12 || string(pack[:4]) != "PACK" {
		t.Fatalf("did not capture a raw incoming Git pack: bytes=%d err=%v", len(pack), err)
	}
	packObjectCount := binary.BigEndian.Uint32(pack[8:12])
	addedOutsideOID := fixtureGitOutput(t, work, "rev-parse", revisionB+":outside/added-after-advance.txt")
	if _, fetched := added[addedOutsideOID]; !fetched {
		t.Fatal("new object outside design/system was absent from the post-fetch object delta")
	}
	if packObjectCount != uint32(len(expectedAdded)) {
		t.Fatalf("incoming pack object count differs from the exact missing-object set: header=%d expected=%d", packObjectCount, len(expectedAdded))
	}
	fetches, err := os.ReadFile(fetchCounter)
	if err != nil || len(fetches) != 1 {
		t.Fatalf("branch advance should perform one fetch into the persistent cache: fetches=%d err=%v", len(fetches), err)
	}
}

func TestGitDesignSystemPersistentCacheSerializesConcurrentReads(t *testing.T) {
	root := t.TempDir()
	remote, work, revisionA := createDesignSystemGitFixtureForProject(t, root, "project-a", "Concurrent", nil)
	cacheRoot := filepath.Join(root, "prototype-cache")
	if err := os.Mkdir(cacheRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	overlapPath := filepath.Join(root, "overlapping-git-processes")
	fetchCounter := filepath.Join(root, "fetch-count")
	fetchWorkspaces := filepath.Join(root, "fetch-workspaces")
	wrapper := filepath.Join(root, "serialized-git")
	program := fmt.Sprintf("#!/bin/sh\nactive=\"$PWD.cache-read-active\"\nowns=0\nif [ -d \"$PWD/.git\" ]; then\n  if mkdir \"$active\" 2>/dev/null; then owns=1; else printf x >> '%s'; fi\n  sleep 0.01\nfi\nif [ \"$1\" = fetch ]; then printf x >> '%s'; pwd >> '%s'; fi\n'%s' \"$@\"\nstatus=$?\nif [ \"$owns\" = 1 ]; then rmdir \"$active\"; fi\nexit $status\n", overlapPath, fetchCounter, fetchWorkspaces, gitPath)
	if err := os.WriteFile(wrapper, []byte(program), 0o700); err != nil {
		t.Fatal(err)
	}
	const companyA, companyB, projectA, projectB, repositoryID = "company-a", "company-b", "project-a", "project-b", "belluga/cache-fixture"
	type readResult struct {
		evaluation ac.DesignSystemResponse
		err        error
	}
	const workers = 8
	start := make(chan struct{})
	results := make(chan readResult, workers)
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			verifier := gitDesignSystemVerifier{
				repositoryURL: func(string) string { return remote }, gitExecutable: wrapper,
				readCache: newDesignSystemReadCache(), prototypeCacheRoot: cacheRoot,
			}
			evaluation, _, err := verifier.ReadDesignSystem(context.Background(), projectA, companyA, repositoryID, revisionA, "main", "project", "")
			results <- readResult{evaluation: evaluation, err: err}
		}()
	}
	close(start)
	group.Wait()
	close(results)
	var firstDigest string
	for err := range results {
		if err.err != nil || err.evaluation.Outcome != "go" || err.evaluation.Revision == nil || *err.evaluation.Revision != revisionA || err.evaluation.ProjectID != projectA || err.evaluation.CompanyID != companyA || err.evaluation.InventoryDigest == nil {
			t.Errorf("unexpected concurrent evaluation: outcome=%s revision=%v company=%s project=%s digest=%v err=%v", err.evaluation.Outcome, err.evaluation.Revision, err.evaluation.CompanyID, err.evaluation.ProjectID, err.evaluation.InventoryDigest, err.err)
			continue
		}
		if firstDigest == "" {
			firstDigest = *err.evaluation.InventoryDigest
		} else if *err.evaluation.InventoryDigest != firstDigest {
			t.Errorf("same-key concurrent read returned a different digest: got=%s want=%s", *err.evaluation.InventoryDigest, firstDigest)
		}
	}
	if overlap, err := os.ReadFile(overlapPath); err == nil && len(overlap) != 0 {
		t.Fatalf("concurrent readers ran Git operations in the same persistent repository without the shared lock: overlaps=%d", len(overlap))
	} else if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read concurrency marker: %v", err)
	}
	fetches, err := os.ReadFile(fetchCounter)
	if err != nil || len(fetches) != 1 {
		t.Fatalf("concurrent same-key readers should share one fetch, got %d, err=%v", len(fetches), err)
	}
	workspaceA := designSystemGitCacheWorkspace(cacheRoot, companyA, projectA, repositoryID, remote)
	workspaceCompanyB := designSystemGitCacheWorkspace(cacheRoot, companyB, projectA, repositoryID, remote)
	initialFetches, err := os.ReadFile(fetchWorkspaces)
	if err != nil || strings.TrimSpace(string(initialFetches)) != workspaceA {
		t.Fatalf("concurrent same-key readers did not fetch into their shared persistent workspace: paths=%q want=%q err=%v", initialFetches, workspaceA, err)
	}
	if workspaceCompanyB == workspaceA {
		t.Fatal("Company identity did not produce a distinct persistent cache workspace")
	}
	if info, err := os.Stat(filepath.Join(workspaceA, ".git")); err != nil || !info.IsDir() {
		t.Fatalf("expected same-key reads to use persistent Git workspace %s: info=%v err=%v", workspaceA, info, err)
	}

	warmVerifier := gitDesignSystemVerifier{
		repositoryURL: func(string) string { return remote }, gitExecutable: wrapper,
		readCache: newDesignSystemReadCache(), prototypeCacheRoot: cacheRoot,
	}
	if _, _, err := warmVerifier.ReadDesignSystem(context.Background(), projectA, companyB, repositoryID, revisionA, "main", "project", ""); err != nil {
		t.Fatal(err)
	}
	revisionB := commitDesignSystemGitFixtureForProject(t, work, projectB, "Project B")
	runFixtureGit(t, work, "push", "--quiet", "origin", "main")
	identities := []struct{ company, project, revision string }{
		{companyB, projectA, revisionA},
		{companyA, projectB, revisionB},
		{companyB, projectB, revisionB},
	}
	var variants [3]readResult
	start = make(chan struct{})
	for i, identity := range identities {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			verifier := gitDesignSystemVerifier{
				repositoryURL: func(string) string { return remote }, gitExecutable: wrapper,
				readCache: newDesignSystemReadCache(), prototypeCacheRoot: cacheRoot,
			}
			evaluation, _, err := verifier.ReadDesignSystem(context.Background(), identity.project, identity.company, repositoryID, identity.revision, "main", "project", "")
			variants[i] = readResult{evaluation: evaluation, err: err}
		}()
	}
	close(start)
	group.Wait()
	for i, identity := range identities {
		evaluation, err := variants[i].evaluation, variants[i].err
		if err != nil || evaluation.Outcome != "go" || evaluation.Revision == nil || *evaluation.Revision != identity.revision || evaluation.ProjectID != identity.project || evaluation.CompanyID != identity.company || evaluation.InventoryDigest == nil {
			t.Fatalf("identity-scoped read was not exact: identity=%+v evaluation=%+v err=%v", identity, evaluation, err)
		}
	}
	if *variants[0].evaluation.InventoryDigest != firstDigest {
		t.Fatal("cached Company/Project identity changed the exact source digest")
	}
	projectVariant := variants[1].evaluation
	newSource, err := ac.NewGitTree(work, revisionB)
	if err != nil {
		t.Fatal(err)
	}
	expectedRevision := revisionB
	expected := ac.EvaluateDesignSystem(newSource, projectB, companyA, "project", ac.SourceBinding{
		RepositoryID: repositoryID, Revision: &expectedRevision, DefinitionPath: designSystemDefinitionPath,
	})
	if expected.Outcome != "go" || expected.InventoryDigest == nil || *projectVariant.InventoryDigest != *expected.InventoryDigest || *projectVariant.InventoryDigest == firstDigest {
		t.Fatalf("Project-isolated response digest does not match its exact full evaluation: got=%v expected=%v previous=%s", projectVariant.InventoryDigest, expected.InventoryDigest, firstDigest)
	}
	companyProjectVariant := variants[2].evaluation
	if *companyProjectVariant.InventoryDigest != *projectVariant.InventoryDigest {
		t.Fatalf("combined identity read changed the exact source digest: got=%s want=%s", *companyProjectVariant.InventoryDigest, *projectVariant.InventoryDigest)
	}
	workspaceProjectB := designSystemGitCacheWorkspace(cacheRoot, companyA, projectB, repositoryID, remote)
	workspaceCompanyBProjectB := designSystemGitCacheWorkspace(cacheRoot, companyB, projectB, repositoryID, remote)
	for _, workspace := range []string{workspaceA, workspaceCompanyB, workspaceProjectB, workspaceCompanyBProjectB} {
		if info, err := os.Stat(filepath.Join(workspace, ".git")); err != nil || !info.IsDir() {
			t.Fatalf("identity-scoped read did not use its persistent workspace %s: info=%v err=%v", workspace, info, err)
		}
	}
	if workspaceProjectB == workspaceA || workspaceCompanyBProjectB == workspaceProjectB || workspaceProjectB == workspaceCompanyB {
		t.Fatal("distinct Company/Project identities shared a persistent Git workspace")
	}
	allFetches, err := os.ReadFile(fetchCounter)
	if err != nil || len(allFetches) != 4 {
		t.Fatalf("same key and each distinct identity should fetch once: got %d fetches, err=%v", len(allFetches), err)
	}
	fetchedWorkspaces, err := os.ReadFile(fetchWorkspaces)
	if err != nil {
		t.Fatalf("read persistent workspace identities for fetches: %v", err)
	}
	fetchedWorkspaceSet := make(map[string]struct{})
	for _, workspace := range strings.Fields(string(fetchedWorkspaces)) {
		if _, duplicate := fetchedWorkspaceSet[workspace]; duplicate {
			t.Fatalf("a cache identity fetched more than once: %s", workspace)
		}
		fetchedWorkspaceSet[workspace] = struct{}{}
	}
	wantWorkspaceSet := map[string]struct{}{
		workspaceA: {}, workspaceCompanyB: {}, workspaceProjectB: {}, workspaceCompanyBProjectB: {},
	}
	if !equalObjectIDs(fetchedWorkspaceSet, wantWorkspaceSet) {
		t.Fatalf("fetches did not use exactly one shared persistent workspace per Company/Project key: got=%v want=%v", fetchedWorkspaceSet, wantWorkspaceSet)
	}
}

func TestDesignSystemReadCacheKeySeparatesEveryEvaluatorIdentity(t *testing.T) {
	base := designSystemReadCacheKey("project-a", "company-a", "belluga/repo", strings.Repeat("a", 40), "project")
	variants := []string{
		designSystemReadCacheKey("project-b", "company-a", "belluga/repo", strings.Repeat("a", 40), "project"),
		designSystemReadCacheKey("project-a", "company-b", "belluga/repo", strings.Repeat("a", 40), "project"),
		designSystemReadCacheKey("project-a", "company-a", "belluga/other", strings.Repeat("a", 40), "project"),
		designSystemReadCacheKey("project-a", "company-a", "belluga/repo", strings.Repeat("b", 40), "project"),
		designSystemReadCacheKey("project-a", "company-a", "belluga/repo", strings.Repeat("a", 40), "company"),
	}
	for index, key := range variants {
		if key == base {
			t.Fatalf("cache key variant %d aliases another tenant/source/evaluator identity", index)
		}
	}
}

func TestGitDesignSystemAsOfHydratesExactBlobsAfterFilteredHistoryScan(t *testing.T) {
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	work := filepath.Join(root, "work")
	runFixtureGit(t, root, "init", "--bare", "--quiet", remote)
	runFixtureGit(t, remote, "config", "uploadpack.allowFilter", "true")
	if err := os.Mkdir(work, 0o700); err != nil {
		t.Fatal(err)
	}
	runFixtureGit(t, work, "init", "--quiet", "--initial-branch=main")
	runFixtureGit(t, work, "config", "user.name", "History Fixture")
	runFixtureGit(t, work, "config", "user.email", "history-fixture@example.invalid")
	manifest := []byte(`{"schema_version":"1","id":"history-system","name":"History Fixture","owner":{"level":"project","id":"history-project"},"tokens":[{"id":"label","type":"string","value":"History"}],"components":[],"assets":[],"contrast_pairs":[]}`)
	manifestPath := filepath.Join(work, filepath.FromSlash(designSystemDefinitionPath))
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	runFixtureGit(t, work, "add", ".")
	runFixtureGit(t, work, "commit", "--quiet", "-m", "history fixture")
	revision := fixtureGitOutput(t, work, "rev-parse", "HEAD")
	runFixtureGit(t, work, "remote", "add", "origin", remote)
	runFixtureGit(t, work, "push", "--quiet", "origin", "main")
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	hiddenRemote := remote + ".temporarily-hidden"
	wrapper := filepath.Join(root, "git-history-fetch-boundary")
	program := fmt.Sprintf("#!/bin/sh\ncase \" $* \" in\n  *' --depth=256 '*)\n    %s \"$@\"; status=$?; /bin/mv '%s' '%s'; exit $status;;\n  *' --depth=1 '*)\n    /bin/mv '%s' '%s'; %s \"$@\"; status=$?; /bin/mv '%s' '%s'; exit $status;;\n  *ls-remote*)\n    /bin/mv '%s' '%s'; %s \"$@\"; status=$?; /bin/mv '%s' '%s'; exit $status;;\n  *) exec %s \"$@\";;\nesac\n", gitPath, remote, hiddenRemote, hiddenRemote, remote, gitPath, remote, hiddenRemote, hiddenRemote, remote, gitPath, remote, hiddenRemote, gitPath)
	if err := os.WriteFile(wrapper, []byte(program), 0o700); err != nil {
		t.Fatal(err)
	}
	verifier := gitDesignSystemVerifier{repositoryURL: func(string) string { return remote }, gitExecutable: wrapper, readCache: newDesignSystemReadCache()}
	evaluation, head, err := verifier.ReadDesignSystemAsOf(context.Background(), "history-project", "company-a", "belluga/history-fixture", "main", revision)
	if err != nil || evaluation.Outcome != "go" || evaluation.DesignSystemValidation != "valid" || head != revision {
		t.Fatalf("as-of exact commit was not fetched with blobs after metadata traversal: outcome=%s validation=%s head=%s err=%v", evaluation.Outcome, evaluation.DesignSystemValidation, head, err)
	}
}

func TestGitDesignSystemHistoryMarksBoundedShallowScanTruncated(t *testing.T) {
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	work := filepath.Join(root, "work")
	runFixtureGit(t, root, "init", "--bare", "--quiet", remote)
	if err := os.Mkdir(work, 0o700); err != nil {
		t.Fatal(err)
	}
	runFixtureGit(t, work, "init", "--quiet", "--initial-branch=main")
	runFixtureGit(t, work, "config", "user.name", "History Fixture")
	runFixtureGit(t, work, "config", "user.email", "history-fixture@example.invalid")
	manifestPath := filepath.Join(work, filepath.FromSlash(designSystemDefinitionPath))
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := []byte(`{"schema_version":"1","id":"old-system","name":"Old System","owner":{"level":"project","id":"history-project"},"tokens":[{"id":"label","type":"string","value":"Old"}],"components":[],"assets":[],"contrast_pairs":[]}`)
	if err := os.WriteFile(manifestPath, manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	runFixtureGit(t, work, "add", ".")
	runFixtureGit(t, work, "commit", "--quiet", "-m", "old design system revision")
	oldRevision := fixtureGitOutput(t, work, "rev-parse", "HEAD")
	loop := exec.Command("sh", "-c", "for i in $(seq 1 260); do git commit --allow-empty --quiet -m \"unrelated history $i\" || exit; done")
	loop.Dir = work
	if output, err := loop.CombinedOutput(); err != nil {
		t.Fatalf("create history beyond bounded scan: %v: %s", err, output)
	}
	runFixtureGit(t, work, "remote", "add", "origin", remote)
	runFixtureGit(t, work, "push", "--quiet", "origin", "main")
	verifier := gitDesignSystemVerifier{repositoryURL: func(string) string { return remote }}
	result, err := verifier.ListDesignSystemHistory(context.Background(), "history-project", "company-a", "belluga/history-fixture", "main", 10)
	if err != nil {
		t.Fatalf("list bounded history: %v", err)
	}
	for _, revision := range result.Revisions {
		if revision.SHA == oldRevision {
			t.Fatalf("old revision beyond shallow boundary unexpectedly appeared: %+v", result)
		}
	}
	if !result.Truncated {
		t.Fatalf("history with an older Design System change beyond the 256-commit scan was reported complete: %+v", result)
	}
}

func TestGitDesignSystemVerifierChecksReachabilityTreeManifestAndInventory(t *testing.T) {
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	work := filepath.Join(root, "work")
	runFixtureGit(t, root, "init", "--bare", "--quiet", remote)
	if err := os.Mkdir(work, 0o700); err != nil {
		t.Fatal(err)
	}
	runFixtureGit(t, work, "init", "--quiet", "--initial-branch=main")
	runFixtureGit(t, work, "config", "user.name", "Registry Fixture")
	runFixtureGit(t, work, "config", "user.email", "registry-fixture@example.invalid")
	manifestPath := filepath.Join(work, filepath.FromSlash(designSystemDefinitionPath))
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := []byte(`{"schema_version":"1","id":"fixture-system","name":"Fixture","owner":{"level":"project","id":"fixture-project"},"tokens":[{"id":"label","type":"string","value":"Fixture"}],"components":[],"assets":[],"contrast_pairs":[]}`)
	if err := os.WriteFile(manifestPath, manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	runFixtureGit(t, work, "add", ".")
	runFixtureGit(t, work, "commit", "--quiet", "-m", "initial")
	expectedHead := fixtureGitOutput(t, work, "rev-parse", "HEAD")
	runFixtureGit(t, work, "remote", "add", "origin", remote)
	runFixtureGit(t, work, "push", "--quiet", "origin", "main")

	manifest = []byte(`{"schema_version":"2","id":"fixture-system","name":"Fixture Updated","owner":{"level":"project","id":"fixture-project"},"tokens":[{"id":"ink","type":"color","value":"#000000"},{"id":"paper","type":"color","value":"#FFFFFF"},{"id":"font","type":"string","value":"Inter, Arial"},{"id":"body","type":"number","value":16},{"id":"display","type":"number","value":36},{"id":"radius","type":"number","value":8}],"presentation":{"default_context":"main","contexts":[{"id":"main","roles":{"canvas":"ink","surface":"ink","text":"paper","muted_text":"paper","border":"paper","accent":"paper","accent_text":"paper","on_accent":"ink","focus":"paper","success_text":"paper","font_family":"font","body_size":"body","display_size":"display","card_radius":"radius","control_radius":"radius"}}]},"components":[],"assets":[],"contrast_pairs":[]}`)
	if err := os.WriteFile(manifestPath, manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	runFixtureGit(t, work, "add", ".")
	operationID := "fixture-ds-operation"
	runFixtureGit(t, work, "commit", "--quiet", "-m", "publish design system", "-m", "Builder-Operation-ID: "+operationID)
	prepared := fixtureGitOutput(t, work, "rev-parse", "HEAD")
	tree := fixtureGitOutput(t, work, "rev-parse", "HEAD^{tree}")
	runFixtureGit(t, work, "push", "--quiet", "origin", "main")
	digest := fixtureInventoryDigest("belluga/fixture", designSystemDefinitionPath, manifest)
	intent := DesignSystemMutationIntent{
		Operation: "create", OperationID: operationID, Actor: "owner", PayloadDigest: digest,
		ExpectedBindingRevision: 1, ExpectedHead: expectedHead, CandidateTreeSHA: tree, PreparedCommitSHA: prepared, InventoryDigest: digest,
	}
	binding := validTestBinding()
	binding.RepositoryID = "belluga/fixture"
	verifier := gitDesignSystemVerifier{repositoryURL: func(id string) string {
		if id != binding.RepositoryID {
			return ""
		}
		return remote
	}}
	state, err := verifier.VerifyDesignSystemMutation(context.Background(), "company-a", "fixture-project", binding, intent)
	if err != nil || !state.Reachable || state.RemoteHead != prepared || state.CommitSHA != prepared || state.ParentSHA != expectedHead || state.TreeSHA != tree || state.InventoryDigest != digest {
		t.Fatalf("valid pushed Design System commit was not verified: %+v, %v", state, err)
	}
	// Once another commit lands, the prepared commit is the shallow boundary
	// of the verifier's depth-2 fetch. Its raw parent must remain verifiable.
	runFixtureGit(t, work, "commit", "--allow-empty", "--quiet", "-m", "unrelated later publication")
	laterHead := fixtureGitOutput(t, work, "rev-parse", "HEAD")
	runFixtureGit(t, work, "push", "--quiet", "origin", "main")
	state, err = verifier.VerifyDesignSystemMutation(context.Background(), "company-a", "fixture-project", binding, intent)
	if err != nil || !state.Reachable || state.RemoteHead != laterHead || state.CommitSHA != prepared || state.ParentSHA != expectedHead {
		t.Fatalf("prepared commit at shallow boundary was not verified: %+v, %v", state, err)
	}

	badDigest := intent
	badDigest.InventoryDigest = "sha256:" + strings.Repeat("f", 64)
	badDigest.PayloadDigest = badDigest.InventoryDigest
	state, err = verifier.VerifyDesignSystemMutation(context.Background(), "company-a", "fixture-project", binding, badDigest)
	if err != nil || state.InventoryDigest != digest {
		t.Fatalf("verifier did not return observed manifest inventory for Registry comparison: %+v, %v", state, err)
	}

	missingTrailer := intent
	missingTrailer.OperationID = "some-other-operation"
	if _, err := verifier.VerifyDesignSystemMutation(context.Background(), "company-a", "fixture-project", binding, missingTrailer); err == nil {
		t.Fatal("commit with a mismatched operation trailer was accepted")
	}

	invalidManifest := []byte(strings.Replace(string(manifest), `"fixture-project"`, `"another-project"`, 1))
	if err := os.WriteFile(manifestPath, invalidManifest, 0o600); err != nil {
		t.Fatal(err)
	}
	runFixtureGit(t, work, "add", ".")
	invalidOperationID := "fixture-invalid-owner"
	runFixtureGit(t, work, "commit", "--quiet", "-m", "invalid owner", "-m", "Builder-Operation-ID: "+invalidOperationID)
	invalidPrepared := fixtureGitOutput(t, work, "rev-parse", "HEAD")
	invalidTree := fixtureGitOutput(t, work, "rev-parse", "HEAD^{tree}")
	runFixtureGit(t, work, "push", "--quiet", "origin", "main")
	invalidIntent := intent
	invalidIntent.OperationID = invalidOperationID
	invalidIntent.ExpectedHead = prepared
	invalidIntent.PreparedCommitSHA = invalidPrepared
	invalidIntent.CandidateTreeSHA = invalidTree
	invalidIntent.InventoryDigest = fixtureInventoryDigest("belluga/fixture", designSystemDefinitionPath, invalidManifest)
	invalidIntent.PayloadDigest = invalidIntent.InventoryDigest
	if _, err := verifier.VerifyDesignSystemMutation(context.Background(), "company-a", "fixture-project", binding, invalidIntent); err == nil {
		t.Fatal("production verifier accepted a Git-reachable candidate rejected by the shared semantic evaluator")
	}

	// A previously selected v1 remains readable, but it cannot be selected by
	// any new create/replace mutation after the v2 publication cutover.
	legacyManifest := []byte(`{"schema_version":"1","id":"fixture-system","name":"Legacy candidate","owner":{"level":"project","id":"fixture-project"},"tokens":[{"id":"label","type":"string","value":"Legacy"}],"components":[],"assets":[],"contrast_pairs":[]}`)
	if err := os.WriteFile(manifestPath, legacyManifest, 0o600); err != nil {
		t.Fatal(err)
	}
	runFixtureGit(t, work, "add", ".")
	legacyOperationID := "fixture-legacy-candidate"
	runFixtureGit(t, work, "commit", "--quiet", "-m", "legacy candidate", "-m", "Builder-Operation-ID: "+legacyOperationID)
	legacyPrepared := fixtureGitOutput(t, work, "rev-parse", "HEAD")
	legacyTree := fixtureGitOutput(t, work, "rev-parse", "HEAD^{tree}")
	runFixtureGit(t, work, "push", "--quiet", "origin", "main")
	legacyIntent := intent
	legacyIntent.OperationID = legacyOperationID
	legacyIntent.ExpectedHead = invalidPrepared
	legacyIntent.PreparedCommitSHA = legacyPrepared
	legacyIntent.CandidateTreeSHA = legacyTree
	legacyIntent.InventoryDigest = fixtureInventoryDigest("belluga/fixture", designSystemDefinitionPath, legacyManifest)
	legacyIntent.PayloadDigest = legacyIntent.InventoryDigest
	if _, err := verifier.VerifyDesignSystemMutation(context.Background(), "company-a", "fixture-project", binding, legacyIntent); err == nil {
		t.Fatal("new Git-reachable v1 candidate was accepted after the v2 cutover")
	}
	invalidPrepared = legacyPrepared

	// Deletion is proved from the published exact tree, without evaluating a
	// manifest that is expected to be absent.
	runFixtureGit(t, work, "rm", "-r", "--quiet", designSystemRoot)
	deleteOperationID := "fixture-delete"
	runFixtureGit(t, work, "commit", "--quiet", "-m", "delete design system", "-m", "Builder-Operation-ID: "+deleteOperationID)
	deletePrepared := fixtureGitOutput(t, work, "rev-parse", "HEAD")
	deleteTree := fixtureGitOutput(t, work, "rev-parse", "HEAD^{tree}")
	runFixtureGit(t, work, "push", "--quiet", "origin", "main")
	emptyDigest, err := ac.InventoryDigest(binding.RepositoryID, nil)
	if err != nil {
		t.Fatal(err)
	}
	deleteIntent := DesignSystemMutationIntent{
		Operation: "delete", OperationID: deleteOperationID, Actor: "owner", PayloadDigest: emptyDigest,
		ExpectedBindingRevision: 1, ExpectedHead: invalidPrepared, CandidateTreeSHA: deleteTree,
		PreparedCommitSHA: deletePrepared, InventoryDigest: emptyDigest,
	}
	deleted, err := verifier.VerifyDesignSystemMutation(context.Background(), "company-a", "fixture-project", binding, deleteIntent)
	if err != nil || !deleted.Reachable || deleted.InventoryDigest != emptyDigest {
		t.Fatalf("published full deletion was not verified with canonical empty inventory: %+v, %v", deleted, err)
	}

	// A delete that leaves any managed subtree is incomplete, and an unrelated
	// replacement operation cannot use a missing source as a deletion proof.
	partialPath := filepath.Join(work, designSystemRoot, "partial.txt")
	if err := os.MkdirAll(filepath.Dir(partialPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(partialPath, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	runFixtureGit(t, work, "add", ".")
	partialOperationID := "fixture-partial-delete"
	runFixtureGit(t, work, "commit", "--quiet", "-m", "partial delete", "-m", "Builder-Operation-ID: "+partialOperationID)
	partialPrepared := fixtureGitOutput(t, work, "rev-parse", "HEAD")
	partialTree := fixtureGitOutput(t, work, "rev-parse", "HEAD^{tree}")
	runFixtureGit(t, work, "push", "--quiet", "origin", "main")
	partialIntent := deleteIntent
	partialIntent.OperationID = partialOperationID
	partialIntent.ExpectedHead = deletePrepared
	partialIntent.PreparedCommitSHA = partialPrepared
	partialIntent.CandidateTreeSHA = partialTree
	if _, err := verifier.VerifyDesignSystemMutation(context.Background(), "company-a", "fixture-project", binding, partialIntent); err == nil {
		t.Fatal("delete verifier accepted a published commit retaining a partial managed subtree")
	}
	partialIntent.Operation = "replace"
	if _, err := verifier.VerifyDesignSystemMutation(context.Background(), "company-a", "fixture-project", binding, partialIntent); err == nil {
		t.Fatal("replace verifier accepted a candidate with an unexpectedly missing Design System source")
	}
}

func TestGitDesignSystemVerifierProvesNoPushOnlyAtExpectedRemoteHead(t *testing.T) {
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	work := filepath.Join(root, "work")
	runFixtureGit(t, root, "init", "--bare", "--quiet", remote)
	if err := os.Mkdir(work, 0o700); err != nil {
		t.Fatal(err)
	}
	runFixtureGit(t, work, "init", "--quiet", "--initial-branch=main")
	runFixtureGit(t, work, "config", "user.name", "Registry Fixture")
	runFixtureGit(t, work, "config", "user.email", "registry-fixture@example.invalid")
	if err := os.MkdirAll(filepath.Join(work, designSystemRoot), 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := []byte(`{"schema_version":"1","id":"fixture-system","name":"Fixture","owner":{"level":"project","id":"fixture-project"},"tokens":[{"id":"label","type":"string","value":"Fixture"}],"components":[],"assets":[],"contrast_pairs":[]}`)
	if err := os.WriteFile(filepath.Join(work, filepath.FromSlash(designSystemDefinitionPath)), manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	runFixtureGit(t, work, "add", ".")
	runFixtureGit(t, work, "commit", "--quiet", "-m", "initial")
	expected := fixtureGitOutput(t, work, "rev-parse", "HEAD")
	runFixtureGit(t, work, "remote", "add", "origin", remote)
	runFixtureGit(t, work, "push", "--quiet", "origin", "main")
	runFixtureGit(t, work, "commit", "--allow-empty", "--quiet", "-m", "prepared but not pushed")
	prepared := fixtureGitOutput(t, work, "rev-parse", "HEAD")
	tree := fixtureGitOutput(t, work, "rev-parse", "HEAD^{tree}")
	intent := DesignSystemMutationIntent{
		Operation: "create", OperationID: "fixture-not-pushed", Actor: "owner", PayloadDigest: fixtureInventoryDigest("belluga/fixture", designSystemDefinitionPath, manifest),
		ExpectedBindingRevision: 1, ExpectedHead: expected, CandidateTreeSHA: tree, PreparedCommitSHA: prepared,
		InventoryDigest: fixtureInventoryDigest("belluga/fixture", designSystemDefinitionPath, manifest),
	}
	binding := validTestBinding()
	binding.RepositoryID = "belluga/fixture"
	verifier := gitDesignSystemVerifier{repositoryURL: func(string) string { return remote }}
	state, err := verifier.VerifyDesignSystemMutation(context.Background(), "company-a", "fixture-project", binding, intent)
	if err != nil || state.Reachable || state.RemoteHead != expected {
		t.Fatalf("unchanged remote did not prove no push: %+v, %v", state, err)
	}
	// Advance the publication branch without the prepared commit. The full
	// history proves the candidate unreachable, so it can be superseded.
	runFixtureGit(t, work, "reset", "--hard", expected)
	if err := os.WriteFile(filepath.Join(work, "sibling.txt"), []byte("advance"), 0o600); err != nil {
		t.Fatal(err)
	}
	runFixtureGit(t, work, "add", "sibling.txt")
	runFixtureGit(t, work, "commit", "--quiet", "-m", "sibling advance")
	advanced := fixtureGitOutput(t, work, "rev-parse", "HEAD")
	runFixtureGit(t, work, "push", "--quiet", "--force", "origin", "main")
	state, err = verifier.VerifyDesignSystemMutation(context.Background(), "company-a", "fixture-project", binding, intent)
	if err != nil || state.RemoteHead != advanced || state.Reachable || !state.ReachabilityVerified {
		t.Fatalf("advanced remote state was not proved unreachable: %+v, %v", state, err)
	}
}

func TestGitDesignSystemAncestorCheckKeepsOperationalFailuresPending(t *testing.T) {
	root := t.TempDir()
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(root, "git-wrapper")
	program := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = merge-base ]; then exit 128; fi\nexec %s \"$@\"\n", gitPath)
	if err := os.WriteFile(wrapper, []byte(program), 0o700); err != nil {
		t.Fatal(err)
	}
	verifier := gitDesignSystemVerifier{gitExecutable: wrapper}
	if reachable, err := verifier.gitIsAncestor(context.Background(), root, strings.Repeat("a", 40), strings.Repeat("b", 40)); err == nil || reachable {
		t.Fatalf("Git execution failure must not prove non-reachability: reachable=%v err=%v", reachable, err)
	}
}

func fixtureInventoryDigest(repositoryID, path string, content []byte) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte("builder-artifact-inventory-v1\x00"))
	var size [4]byte
	for _, value := range [][]byte{[]byte(repositoryID), []byte(path)} {
		binary.BigEndian.PutUint32(size[:], uint32(len(value)))
		_, _ = hash.Write(size[:])
		_, _ = hash.Write(value)
	}
	contentDigest := sha256.Sum256(content)
	_, _ = hash.Write(contentDigest[:])
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

func runFixtureGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("fixture git %v failed: %v: %s", args, err, output)
	}
}

func fixtureGitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	output, err := command.Output()
	if err != nil {
		t.Fatalf("fixture git %v failed: %v", args, err)
	}
	return strings.TrimSpace(string(output))
}

func fixtureGitBytes(t *testing.T, dir string, args ...string) []byte {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	output, err := command.Output()
	if err != nil {
		t.Fatalf("fixture git %v failed: %v", args, err)
	}
	return output
}

func createDesignSystemGitFixture(t *testing.T, root, tokenValue string, largeBlob []byte) (string, string, string) {
	return createDesignSystemGitFixtureForProject(t, root, "fixture-project", tokenValue, largeBlob)
}

func createDesignSystemGitFixtureForProject(t *testing.T, root, projectID, tokenValue string, largeBlob []byte) (string, string, string) {
	t.Helper()
	remote := filepath.Join(root, "remote.git")
	work := filepath.Join(root, "work")
	runFixtureGit(t, root, "init", "--bare", "--quiet", remote)
	runFixtureGit(t, remote, "config", "uploadpack.allowReachableSHAInWant", "true")
	if err := os.Mkdir(work, 0o700); err != nil {
		t.Fatal(err)
	}
	runFixtureGit(t, work, "init", "--quiet", "--initial-branch=main")
	runFixtureGit(t, work, "config", "user.name", "Design System Fixture")
	runFixtureGit(t, work, "config", "user.email", "design-system-fixture@example.invalid")
	writeDesignSystemGitFixtureManifest(t, work, designSystemGitFixtureManifestForProject(projectID, tokenValue))
	assetPath := filepath.Join(work, filepath.FromSlash(designSystemRoot+"/assets/mark.svg"))
	if err := os.MkdirAll(filepath.Dir(assetPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(assetPath, []byte("<svg>First</svg>"), 0o600); err != nil {
		t.Fatal(err)
	}
	if len(largeBlob) != 0 {
		largePath := filepath.Join(work, "outside", "large.bin")
		if err := os.MkdirAll(filepath.Dir(largePath), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(largePath, largeBlob, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	runFixtureGit(t, work, "add", ".")
	runFixtureGit(t, work, "commit", "--quiet", "-m", "Design System fixture")
	revision := fixtureGitOutput(t, work, "rev-parse", "HEAD")
	runFixtureGit(t, work, "remote", "add", "origin", remote)
	runFixtureGit(t, work, "push", "--quiet", "origin", "main")
	return remote, work, revision
}

func designSystemGitFixtureManifest(tokenValue string) []byte {
	return designSystemGitFixtureManifestForProject("fixture-project", tokenValue)
}

func designSystemGitFixtureManifestForProject(projectID, tokenValue string) []byte {
	return []byte(fmt.Sprintf(`{"schema_version":"1","id":"fixture-system","name":"Fixture","owner":{"level":"project","id":%q},"tokens":[{"id":"label","type":"string","value":%q}],"components":[],"assets":["assets/mark.svg"],"contrast_pairs":[]}`, projectID, tokenValue))
}

func writeDesignSystemGitFixtureManifest(t *testing.T, work string, manifest []byte) {
	t.Helper()
	path := filepath.Join(work, filepath.FromSlash(designSystemDefinitionPath))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, manifest, 0o600); err != nil {
		t.Fatal(err)
	}
}

func commitDesignSystemGitFixture(t *testing.T, work, tokenValue string) string {
	return commitDesignSystemGitFixtureForProject(t, work, "fixture-project", tokenValue)
}

func commitDesignSystemGitFixtureForProject(t *testing.T, work, projectID, tokenValue string) string {
	t.Helper()
	writeDesignSystemGitFixtureManifest(t, work, designSystemGitFixtureManifestForProject(projectID, tokenValue))
	runFixtureGit(t, work, "add", designSystemDefinitionPath)
	runFixtureGit(t, work, "commit", "--quiet", "-m", "change Design System token")
	return fixtureGitOutput(t, work, "rev-parse", "HEAD")
}

func countingGitWrapper(t *testing.T, root, fetchCounter string) string {
	t.Helper()
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(root, "counting-git")
	program := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = fetch ]; then printf x >> '%s'; fi\nexec '%s' \"$@\"\n", fetchCounter, gitPath)
	if err := os.WriteFile(wrapper, []byte(program), 0o700); err != nil {
		t.Fatal(err)
	}
	return wrapper
}

func gitObjectDeltaWrapper(t *testing.T, root, gitPath, beforePath, afterPath, packTrace, fetchCounter string) string {
	t.Helper()
	wrapper := filepath.Join(root, "git-object-delta")
	program := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = fetch ]; then\n  printf x >> '%s'\n  '%s' -C \"$PWD\" cat-file --batch-all-objects --batch-check='%%(objectname)' > '%s'\n  GIT_TRACE_PACKFILE='%s' '%s' \"$@\"\n  status=$?\n  '%s' -C \"$PWD\" cat-file --batch-all-objects --batch-check='%%(objectname)' > '%s'\n  exit $status\nfi\nexec '%s' \"$@\"\n", fetchCounter, gitPath, beforePath, packTrace, gitPath, gitPath, afterPath, gitPath)
	if err := os.WriteFile(wrapper, []byte(program), 0o700); err != nil {
		t.Fatal(err)
	}
	return wrapper
}

func designSystemGitCacheWorkspace(root, companyID, projectID, repositoryID, remote string) string {
	key := sha256.Sum256([]byte(companyID + "\x00" + projectID + "\x00" + repositoryID + "\x00" + remote))
	return filepath.Join(root, hex.EncodeToString(key[:]))
}

func gitObjectIDs(t *testing.T, repository string) map[string]struct{} {
	t.Helper()
	return objectIDsFromText(fixtureGitOutput(t, repository, "cat-file", "--batch-all-objects", "--batch-check=%(objectname)"))
}

func gitRevisionObjectIDs(t *testing.T, repository, revision string) map[string]struct{} {
	t.Helper()
	return objectIDsFromText(fixtureGitOutput(t, repository, "rev-list", "--objects", revision))
}

func objectIDsFromFile(t *testing.T, path string) map[string]struct{} {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read Git object inventory %s: %v", path, err)
	}
	return objectIDsFromText(string(data))
}

func objectIDsFromText(output string) map[string]struct{} {
	objects := make(map[string]struct{})
	for _, line := range strings.Split(output, "\n") {
		if fields := strings.Fields(line); len(fields) != 0 {
			objects[fields[0]] = struct{}{}
		}
	}
	return objects
}

func designSystemHasDiagnostic(evaluation ac.DesignSystemResponse, code string) bool {
	for _, diagnostic := range evaluation.Diagnostics {
		if diagnostic.Code == code {
			return true
		}
	}
	return false
}

func objectIDDifference(left, right map[string]struct{}) map[string]struct{} {
	difference := make(map[string]struct{})
	for objectID := range left {
		if _, present := right[objectID]; !present {
			difference[objectID] = struct{}{}
		}
	}
	return difference
}

func equalObjectIDs(left, right map[string]struct{}) bool {
	if len(left) != len(right) {
		return false
	}
	for objectID := range left {
		if _, found := right[objectID]; !found {
			return false
		}
	}
	return true
}

func TestGitDesignSystemCancelsWhenPrototypeHoldsCache(t *testing.T) {
	root := t.TempDir()
	remote, _, revision := createDesignSystemGitFixtureForProject(t, root, "project-a", "Cancellation", nil)
	const company, project, repo = "company-a", "project-a", "belluga/cache-fixture"
	fetchCounter := filepath.Join(root, "fetch-count")
	owner := gitDesignSystemVerifier{
		repositoryURL:      func(string) string { return remote },
		prototypeCacheRoot: t.TempDir(),
		gitExecutable:      countingGitWrapper(t, root, fetchCounter),
	}
	source, err := owner.OpenRevision(context.Background(), company, project, RepositoryBinding{
		State: "configured", RepositoryID: repo, PublicationBranch: "main",
	}, revision)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if source.release != nil {
			source.close()
		}
	}()
	reader := owner
	reader.readCache = newDesignSystemReadCache()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, _, err := reader.ReadDesignSystem(ctx, project, company, repo, revision, "main", "project", "")
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expected deadline while Prototype holds the cache, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Design System read is still waiting for Prototype")
	}
	source.close()
	source.release = nil
	result, _, err := reader.ReadDesignSystem(context.Background(), project, company, repo, revision, "main", "project", "")
	if err != nil || result.Outcome != "go" || result.Revision == nil || *result.Revision != revision || result.InventoryDigest == nil {
		t.Fatalf("expected exact revision after release, got outcome %s, err %v", result.Outcome, err)
	}
	if count, err := os.ReadFile(fetchCounter); err != nil || string(count) != "x" {
		t.Fatalf("expected just the initial Prototype fetch, got %q, err %v", count, err)
	}
}

func TestGitDesignSystemRetriesCacheAfterInterruptedInitialization(t *testing.T) {
	root := t.TempDir()
	remote, _, revision := createDesignSystemGitFixtureForProject(t, root, "project-a", "Retry initialization", nil)
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "interrupted")
	wrapper := filepath.Join(root, "interrupt-git")
	script := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = remote ] && [ \"$2\" = add ] && [ ! -e '%s' ]; then touch '%s'; exit 1; fi\nexec '%s' \"$@\"\n", marker, marker, gitPath)
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	verifier := gitDesignSystemVerifier{
		repositoryURL:      func(string) string { return remote },
		prototypeCacheRoot: t.TempDir(),
		gitExecutable:      wrapper,
	}
	const company, project, repo = "company-a", "project-a", "belluga/cache-fixture"
	binding := RepositoryBinding{State: "configured", RepositoryID: repo, PublicationBranch: "main"}
	if source, err := verifier.OpenRevision(context.Background(), company, project, binding, revision); err == nil {
		source.close()
		t.Fatal("expected the interrupted initialization to fail")
	}
	source, err := verifier.OpenRevision(context.Background(), company, project, binding, revision)
	if err != nil {
		t.Fatalf("cache did not recover after interrupted initialization: %v", err)
	}
	defer source.close()
	if source.sha != revision || source.transient {
		t.Fatal("expected the exact revision in the persistent cache")
	}
}

func TestGitDesignSystemInvalidCachedTreeFallsBackToExactSHA(t *testing.T) {
	root := t.TempDir()
	remote, work, revision := createDesignSystemGitFixture(t, root, "Pinned", nil)
	const company, project, repo = "company-a", "fixture-project", "belluga/cache-fixture"
	count := filepath.Join(root, "fetch-count")
	verifier := gitDesignSystemVerifier{
		repositoryURL:      func(string) string { return remote },
		prototypeCacheRoot: t.TempDir(),
		gitExecutable:      countingGitWrapper(t, root, count),
	}
	ctx := context.Background()
	binding := RepositoryBinding{State: "configured", RepositoryID: repo, PublicationBranch: "main"}
	cached, err := verifier.OpenRevision(ctx, company, project, binding, revision)
	if err != nil {
		t.Fatal(err)
	}
	cached.close()
	runFixtureGit(t, cached.workspace, "config", "core.bare", "true")
	commitDesignSystemGitFixture(t, work, "Current")
	runFixtureGit(t, work, "push", "--quiet", "origin", "main")
	evaluation, _, err := verifier.ReadDesignSystem(ctx, project, company, repo, revision, "main", "project", "")
	if err != nil || evaluation.Outcome != "go" || evaluation.Revision == nil || *evaluation.Revision != revision {
		t.Fatalf("invalid cached tree should fall back to the pinned revision: outcome=%s err=%v", evaluation.Outcome, err)
	}
	if data, err := os.ReadFile(count); err != nil || string(data) != "xx" {
		t.Fatalf("expected one initial fetch and one exact fallback fetch: count=%q err=%v", data, err)
	}
}
