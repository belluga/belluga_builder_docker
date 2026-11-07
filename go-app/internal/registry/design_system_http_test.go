package registry

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	ac "belluga-builder-viewer/artifact_catalog"
)

type designSystemReadHookVerifier struct {
	designSystemRemoteVerifier
	reader    designSystemGitReader
	afterRead func() error
}

func (v designSystemReadHookVerifier) ReadDesignSystem(ctx context.Context, projectID, companyID, repositoryID, revision, branch, level, assetPath string) (ac.DesignSystemResponse, []byte, error) {
	evaluation, data, err := v.reader.ReadDesignSystem(ctx, projectID, companyID, repositoryID, revision, branch, level, assetPath)
	if err == nil && v.afterRead != nil {
		err = v.afterRead()
	}
	return evaluation, data, err
}

func TestDesignSystemMutationHTTPRoutesPersistVerifyAndFinalizeDisposableGit(t *testing.T) {
	uri := os.Getenv("REGISTRY_TEST_MONGODB_URI")
	if uri == "" {
		t.Skip("isolated MongoDB fixture not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	stamp := fmt.Sprintf("test_ds_http_%d", time.Now().UnixNano())
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

	projectID := "design-system-http"
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	work := filepath.Join(root, "work")
	runFixtureGit(t, root, "init", "--bare", "--quiet", remote)
	if err := os.Mkdir(work, 0o700); err != nil {
		t.Fatal(err)
	}
	runFixtureGit(t, work, "init", "--quiet", "--initial-branch=main")
	runFixtureGit(t, work, "config", "user.name", "Registry HTTP Fixture")
	runFixtureGit(t, work, "config", "user.email", "registry-http-fixture@example.invalid")
	if err := os.WriteFile(filepath.Join(work, "README"), []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	runFixtureGit(t, work, "add", "README")
	runFixtureGit(t, work, "commit", "--quiet", "-m", "initial")
	expectedHead := fixtureGitOutput(t, work, "rev-parse", "HEAD")
	runFixtureGit(t, work, "remote", "add", "origin", remote)
	runFixtureGit(t, work, "push", "--quiet", "origin", "main")

	binding := validTestBinding()
	binding.RepositoryID = "belluga/fixture"
	binding.PublicationBranch = "main"
	if _, err := registerTestProjectWithBinding(ctx, store, Registration{CompanyID: "company-a", CompanyName: "Company A", ProjectID: projectID, ProjectName: projectID, Binding: binding}); err != nil {
		t.Fatal(err)
	}
	store.designSystemVerifier = gitDesignSystemVerifier{repositoryURL: func(id string) string {
		if id != binding.RepositoryID {
			return ""
		}
		return remote
	}}

	manifest := []byte(`{"schema_version":"1","id":"fixture-system","name":"Fixture","owner":{"level":"project","id":"design-system-http"},"tokens":[{"id":"label","type":"string","value":"Fixture"}],"components":[],"assets":["assets/logo.svg"],"contrast_pairs":[]}`)
	manifestPath := filepath.Join(work, filepath.FromSlash(designSystemDefinitionPath))
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	assetPath := filepath.Join(work, filepath.FromSlash(designSystemRoot+"/assets/logo.svg"))
	if err := os.MkdirAll(filepath.Dir(assetPath), 0o700); err != nil {
		t.Fatal(err)
	}
	assetBytes := []byte("<svg xmlns=\"http://www.w3.org/2000/svg\"><title>fixture</title></svg>")
	if err := os.WriteFile(assetPath, assetBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	runFixtureGit(t, work, "add", designSystemRoot)
	operationID := "http-create-ds-1"
	runFixtureGit(t, work, "commit", "--quiet", "-m", "create design system", "-m", "Builder-Operation-ID: "+operationID)
	preparedSHA := fixtureGitOutput(t, work, "rev-parse", "HEAD")
	treeSHA := fixtureGitOutput(t, work, "rev-parse", "HEAD^{tree}")
	source, err := ac.NewGitTree(work, preparedSHA)
	if err != nil {
		t.Fatal(err)
	}
	evaluation := ac.EvaluateDesignSystem(source, projectID, "company-a", "project", ac.SourceBinding{
		RepositoryID: binding.RepositoryID, Revision: &preparedSHA, DefinitionPath: designSystemDefinitionPath,
	})
	if evaluation.Outcome != "go" || evaluation.InventoryDigest == nil {
		t.Fatalf("disposable candidate did not pass shared evaluator: outcome=%s diagnostics=%+v", evaluation.Outcome, evaluation.Diagnostics)
	}
	intent := DesignSystemMutationIntent{
		Operation: "create", OperationID: operationID, Actor: "fixture-owner", PayloadDigest: *evaluation.InventoryDigest,
		ExpectedBindingRevision: 1, ExpectedHead: expectedHead, CandidateTreeSHA: treeSHA, PreparedCommitSHA: preparedSHA,
		InventoryDigest: *evaluation.InventoryDigest,
	}
	intentBody, err := json.Marshal(intent)
	if err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(NewHandler(store))
	defer server.Close()
	endpoint := "/internal/v1/companies/company-a/projects/" + projectID + "/design-system"
	call := func(method, path string, body []byte) (int, []byte) {
		t.Helper()
		var requestBody io.Reader
		if body != nil {
			requestBody = bytes.NewReader(body)
		}
		request, err := http.NewRequest(method, server.URL+path, requestBody)
		if err != nil {
			t.Fatal(err)
		}
		if body != nil {
			request.Header.Set("Content-Type", "application/json")
		}
		response, err := server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, data
	}

	status, body := call(http.MethodGet, endpoint+"/state", nil)
	if status != http.StatusOK || !bytes.Contains(body, []byte(`"state":"unselected"`)) || !bytes.Contains(body, []byte(`"repository_id":"belluga/fixture"`)) {
		t.Fatalf("initial scoped state status=%d body=%s", status, body)
	}
	if status, body = call(http.MethodGet, endpoint, nil); status != http.StatusServiceUnavailable || !bytes.Contains(body, []byte("source_bindings")) {
		t.Fatalf("unselected Project source without private fallback map did not fail closed: status=%d body=%s", status, body)
	}
	if status, body = call(http.MethodGet, "/internal/v1/companies/company-b/projects/"+projectID+"/design-system/state", nil); status != http.StatusNotFound {
		t.Fatalf("cross-Company state read status=%d body=%s", status, body)
	}

	// Two real concurrent HTTP requests with the same complete operation must
	// converge on the one Mongo pending intent.
	const concurrentRetries = 2
	start := make(chan struct{})
	statuses := make([]int, concurrentRetries)
	bodies := make([][]byte, concurrentRetries)
	var wait sync.WaitGroup
	for index := 0; index < concurrentRetries; index++ {
		index := index
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			statuses[index], bodies[index] = call(http.MethodPost, endpoint+"/mutations", intentBody)
		}()
	}
	close(start)
	wait.Wait()
	for index := range statuses {
		if statuses[index] != http.StatusOK || !bytes.Contains(bodies[index], []byte(`"operation_id":"`+operationID+`"`)) {
			t.Fatalf("same-key concurrent begin[%d] status=%d body=%s", index, statuses[index], bodies[index])
		}
	}
	competing := intent
	competing.OperationID = "http-create-ds-2"
	competingBody, _ := json.Marshal(competing)
	if status, body = call(http.MethodPost, endpoint+"/mutations", competingBody); status != http.StatusConflict || !bytes.Contains(body, []byte("pending_operation")) {
		t.Fatalf("competing mutation did not receive a field-addressed conflict: status=%d body=%s", status, body)
	}
	if status, body = call(http.MethodPost, endpoint+"/mutations/"+operationID+"/recover", []byte(`{"remote_head":"`+preparedSHA+`"}`)); status != http.StatusBadRequest {
		t.Fatalf("caller-supplied remote proof was accepted: status=%d body=%s", status, body)
	}
	status, body = call(http.MethodGet, endpoint+"/state", nil)
	if status != http.StatusOK || !bytes.Contains(body, []byte(`"pending_operation"`)) {
		t.Fatalf("pending intent was not readable before push: status=%d body=%s", status, body)
	}

	runFixtureGit(t, work, "push", "--quiet", "origin", "HEAD:refs/heads/main")
	status, body = call(http.MethodPost, endpoint+"/mutations/"+operationID+"/recover", nil)
	if status != http.StatusOK || !bytes.Contains(body, []byte(`"outcome":"published"`)) || !bytes.Contains(body, []byte(`"selected_sha":"`+preparedSHA+`"`)) {
		t.Fatalf("server-owned remote verification did not finalize exact commit: status=%d body=%s", status, body)
	}
	// Exact replay returns the server-persisted outcome without trusting a
	// second remote claim or attempting a different operation.
	if status, body = call(http.MethodPost, endpoint+"/mutations/"+operationID+"/recover", nil); status != http.StatusOK || !bytes.Contains(body, []byte(`"outcome":"published"`)) {
		t.Fatalf("same operation recovery replay was not idempotent: status=%d body=%s", status, body)
	}
	status, body = call(http.MethodGet, endpoint+"/state", nil)
	if status != http.StatusOK || !bytes.Contains(body, []byte(`"state":"selected"`)) || !bytes.Contains(body, []byte(`"sha":"`+preparedSHA+`"`)) || bytes.Contains(body, []byte(`"pending_operation"`)) {
		t.Fatalf("final selection state does not match verified commit: status=%d body=%s", status, body)
	}
	if status, body = call(http.MethodPost, endpoint+"/mutations/not-the-last-operation/recover", nil); status != http.StatusConflict || !strings.Contains(string(body), "operation_id") {
		t.Fatalf("unrelated recovery ID did not conflict: status=%d body=%s", status, body)
	}
	if status, body = call(http.MethodGet, endpoint+"/"+preparedSHA, nil); status != http.StatusOK || !bytes.Contains(body, []byte(`"owner_level":"project"`)) || !bytes.Contains(body, []byte(`"inventory_digest":"`+*evaluation.InventoryDigest+`"`)) {
		t.Fatalf("exact-SHA detail route failed: status=%d body=%s", status, body)
	}
	if status, body = call(http.MethodGet, endpoint+"/"+strings.Repeat("b", 40), nil); status != http.StatusConflict || !strings.Contains(string(body), "revision") {
		t.Fatalf("detail route admitted a non-selected SHA: status=%d body=%s", status, body)
	}
	assetRequest, err := http.NewRequest(http.MethodGet, server.URL+endpoint+"/"+preparedSHA+"/assets/assets/logo.svg", nil)
	if err != nil {
		t.Fatal(err)
	}
	assetResponse, err := server.Client().Do(assetRequest)
	if err != nil {
		t.Fatal(err)
	}
	assetResponseBody, _ := io.ReadAll(assetResponse.Body)
	assetETag := assetResponse.Header.Get("ETag")
	if closeErr := assetResponse.Body.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	if assetResponse.StatusCode != http.StatusOK || !bytes.Equal(assetResponseBody, assetBytes) || assetResponse.Header.Get("X-Design-System-Revision") != preparedSHA || assetETag == "" {
		t.Fatalf("registered exact-SHA asset read failed: status=%d headers=%v body=%q", assetResponse.StatusCode, assetResponse.Header, assetResponseBody)
	}
	assetRequest, _ = http.NewRequest(http.MethodGet, server.URL+endpoint+"/"+preparedSHA+"/assets/assets/logo.svg", nil)
	assetRequest.Header.Set("If-None-Match", assetETag)
	assetResponse, err = server.Client().Do(assetRequest)
	if err != nil {
		t.Fatal(err)
	}
	_ = assetResponse.Body.Close()
	if assetResponse.StatusCode != http.StatusNotModified {
		t.Fatalf("immutable asset ETag revalidation status=%d", assetResponse.StatusCode)
	}
	if status, body = call(http.MethodGet, endpoint+"/"+preparedSHA+"/assets/assets/not-listed.svg", nil); status != http.StatusNotFound || !strings.Contains(string(body), "asset_path") {
		t.Fatalf("unregistered asset path was served: status=%d body=%s", status, body)
	}
	verifier := store.designSystemVerifier
	reader, ok := verifier.(designSystemGitReader)
	if !ok {
		t.Fatal("configured Git verifier does not expose the read adapter")
	}
	store.designSystemVerifier = designSystemReadHookVerifier{
		designSystemRemoteVerifier: verifier,
		reader:                     reader,
		afterRead: func() error {
			_, err := store.ChangeRepositoryBinding(ctx, "company-a", projectID, RepositoryBindingRequest{
				Action: "replace", ProjectID: projectID, ExpectedRevision: 1, OperationID: "read-race-rebind", Actor: "fixture-owner", RepositoryID: "belluga/fixture-next", Branch: "main",
			})
			return err
		},
	}
	if status, body = call(http.MethodGet, endpoint+"/"+preparedSHA+"/assets/assets/logo.svg", nil); status != http.StatusConflict || !strings.Contains(string(body), "selection") {
		t.Fatalf("asset read returned bytes after a concurrent rebind: status=%d body=%s", status, body)
	}
}

func TestMongoGitRecoverySupersedesUnreachableCandidateAfterBranchAdvance(t *testing.T) {
	uri := os.Getenv("REGISTRY_TEST_MONGODB_URI")
	if uri == "" {
		t.Skip("isolated MongoDB fixture not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	stamp := fmt.Sprintf("test_ds_superseded_%d", time.Now().UnixNano())
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

	projectID := "design-system-superseded"
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	work := filepath.Join(root, "candidate")
	runFixtureGit(t, root, "init", "--bare", "--quiet", remote)
	if err := os.Mkdir(work, 0o700); err != nil {
		t.Fatal(err)
	}
	runFixtureGit(t, work, "init", "--quiet", "--initial-branch=main")
	runFixtureGit(t, work, "config", "user.name", "Registry Race Fixture")
	runFixtureGit(t, work, "config", "user.email", "registry-race@example.invalid")
	if err := os.WriteFile(filepath.Join(work, "README"), []byte("base"), 0o600); err != nil {
		t.Fatal(err)
	}
	runFixtureGit(t, work, "add", "README")
	runFixtureGit(t, work, "commit", "--quiet", "-m", "base")
	expectedHead := fixtureGitOutput(t, work, "rev-parse", "HEAD")
	runFixtureGit(t, work, "remote", "add", "origin", remote)
	runFixtureGit(t, work, "push", "--quiet", "origin", "main")
	binding := validTestBinding()
	binding.RepositoryID = "belluga/superseded-fixture"
	binding.PublicationBranch = "main"
	if _, err := registerTestProjectWithBinding(ctx, store, Registration{CompanyID: "company-a", CompanyName: "Company A", ProjectID: projectID, ProjectName: projectID, Binding: binding}); err != nil {
		t.Fatal(err)
	}

	manifest := []byte(`{"schema_version":"1","id":"fixture-system","name":"Fixture","owner":{"level":"project","id":"design-system-superseded"},"tokens":[{"id":"label","type":"string","value":"Fixture"}],"components":[],"assets":[],"contrast_pairs":[]}`)
	manifestPath := filepath.Join(work, filepath.FromSlash(designSystemDefinitionPath))
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	runFixtureGit(t, work, "add", designSystemRoot)
	operationID := "superseded-candidate"
	runFixtureGit(t, work, "commit", "--quiet", "-m", "candidate", "-m", "Builder-Operation-ID: "+operationID)
	preparedSHA := fixtureGitOutput(t, work, "rev-parse", "HEAD")
	treeSHA := fixtureGitOutput(t, work, "rev-parse", "HEAD^{tree}")
	source, err := ac.NewGitTree(work, preparedSHA)
	if err != nil {
		t.Fatal(err)
	}
	evaluation := ac.EvaluateDesignSystem(source, projectID, "company-a", "project", ac.SourceBinding{
		RepositoryID: binding.RepositoryID, Revision: &preparedSHA, DefinitionPath: designSystemDefinitionPath,
	})
	if evaluation.Outcome != "go" || evaluation.InventoryDigest == nil {
		t.Fatalf("candidate failed shared evaluator: %+v", evaluation.Diagnostics)
	}
	intent := DesignSystemMutationIntent{Operation: "create", OperationID: operationID, Actor: "fixture-owner", PayloadDigest: *evaluation.InventoryDigest,
		ExpectedBindingRevision: 1, ExpectedHead: expectedHead, CandidateTreeSHA: treeSHA, PreparedCommitSHA: preparedSHA, InventoryDigest: *evaluation.InventoryDigest}
	if _, err := store.BeginDesignSystemMutation(ctx, "company-a", projectID, intent); err != nil {
		t.Fatal(err)
	}

	competitor := filepath.Join(root, "competitor")
	runFixtureGit(t, root, "clone", "--quiet", "--branch", "main", remote, competitor)
	runFixtureGit(t, competitor, "config", "user.name", "Unrelated Publisher")
	runFixtureGit(t, competitor, "config", "user.email", "unrelated@example.invalid")
	runFixtureGit(t, competitor, "commit", "--allow-empty", "--quiet", "-m", "unrelated branch advance")
	runFixtureGit(t, competitor, "push", "--quiet", "origin", "main")
	newHead := fixtureGitOutput(t, competitor, "rev-parse", "HEAD")
	if newHead == expectedHead {
		t.Fatal("fixture branch did not advance")
	}
	store.designSystemVerifier = gitDesignSystemVerifier{repositoryURL: func(id string) string {
		if id != binding.RepositoryID {
			return ""
		}
		return remote
	}}
	result, err := store.RecoverDesignSystemMutation(ctx, "company-a", projectID, operationID)
	if err != nil || result.Outcome != "superseded" || result.State != "unselected" {
		t.Fatalf("unreachable candidate was not superseded after verified branch advance: %+v, %v", result, err)
	}
	state, err := store.ReadDesignSystemState(ctx, "company-a", projectID)
	if err != nil || state.PendingOperation != nil || state.Selection.State != "unselected" || state.LastOperation == nil || state.LastOperation.Outcome != "superseded" {
		t.Fatalf("superseded operation did not clear pending intent while preserving prior state: %+v, %v", state, err)
	}
}
