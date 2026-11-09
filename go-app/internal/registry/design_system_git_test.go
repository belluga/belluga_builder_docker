package registry

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

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
