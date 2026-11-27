package registry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	ac "belluga-builder-viewer/artifact_catalog"
	"go.mongodb.org/mongo-driver/v2/bson"
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

type observedDesignSystemGit struct {
	gitDesignSystemVerifier
	branchCalls atomic.Int32
	readCalls   atomic.Int32
}

func (v *observedDesignSystemGit) ResolveDesignSystemBranch(ctx context.Context, binding Binding) (string, error) {
	v.branchCalls.Add(1)
	return v.gitDesignSystemVerifier.ResolveDesignSystemBranch(ctx, binding)
}

func (v *observedDesignSystemGit) ReadDesignSystem(ctx context.Context, projectID, companyID, repositoryID, revision, branch, level, assetPath string) (ac.DesignSystemResponse, []byte, error) {
	v.readCalls.Add(1)
	return v.gitDesignSystemVerifier.ReadDesignSystem(ctx, projectID, companyID, repositoryID, revision, branch, level, assetPath)
}

type designSystemDeadlineHTTPService struct {
	Service
	detailRemaining time.Duration
	assetRemaining  time.Duration
}

func (s *designSystemDeadlineHTTPService) ReadDesignSystem(ctx context.Context, _, _, _ string) (DesignSystemReadResponse, error) {
	if deadline, ok := ctx.Deadline(); ok {
		s.detailRemaining = time.Until(deadline)
	}
	return DesignSystemReadResponse{}, nil
}

func (s *designSystemDeadlineHTTPService) ReadDesignSystemAsset(ctx context.Context, _, _, _, _ string) (designSystemAssetResponse, error) {
	if deadline, ok := ctx.Deadline(); ok {
		s.assetRemaining = time.Until(deadline)
	}
	return designSystemAssetResponse{Data: []byte("asset"), ContentType: "text/plain", ETag: `"asset"`}, nil
}

func TestDesignSystemReadRoutesUseNinetySecondDeadline(t *testing.T) {
	if designSystemRecoveryTimeout != 15*time.Second {
		t.Fatalf("Design System recovery timeout changed: %s", designSystemRecoveryTimeout)
	}
	service := &designSystemDeadlineHTTPService{}
	handler := NewHandler(service)
	for _, path := range []string{
		"/internal/v1/companies/company-a/projects/project-a/design-system",
		"/internal/v1/companies/company-a/projects/project-a/design-system/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/assets/design/system/assets/mark.svg",
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("Design System read route %s returned %d: %s", path, response.Code, response.Body.String())
		}
	}
	for name, remaining := range map[string]time.Duration{"detail": service.detailRemaining, "asset": service.assetRemaining} {
		if remaining < 89*time.Second || remaining > 90*time.Second {
			t.Errorf("%s read deadline has %s remaining; want close to 90s", name, remaining)
		}
	}
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
	binding := validTestBinding()
	binding.RepositoryID = "belluga/fixture"
	binding.PublicationBranch = "release/design-system"
	branchRef := "refs/heads/" + binding.PublicationBranch
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	work := filepath.Join(root, "work")
	runFixtureGit(t, root, "init", "--bare", "--quiet", remote)
	if err := os.Mkdir(work, 0o700); err != nil {
		t.Fatal(err)
	}
	runFixtureGit(t, work, "init", "--quiet", "--initial-branch="+binding.PublicationBranch)
	runFixtureGit(t, work, "config", "user.name", "Registry HTTP Fixture")
	runFixtureGit(t, work, "config", "user.email", "registry-http-fixture@example.invalid")
	if err := os.WriteFile(filepath.Join(work, "README"), []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	runFixtureGit(t, work, "add", "README")
	runFixtureGit(t, work, "commit", "--quiet", "-m", "initial")
	expectedHead := fixtureGitOutput(t, work, "rev-parse", "HEAD")
	runFixtureGit(t, work, "remote", "add", "origin", remote)
	runFixtureGit(t, work, "push", "--quiet", "origin", "HEAD:"+branchRef)

	if _, err := registerTestProjectWithBinding(ctx, store, Registration{CompanyID: "company-a", CompanyName: "Company A", ProjectID: projectID, ProjectName: projectID, Binding: binding}); err != nil {
		t.Fatal(err)
	}
	gitReader := &observedDesignSystemGit{gitDesignSystemVerifier: gitDesignSystemVerifier{repositoryURL: func(id string) string {
		if id != binding.RepositoryID {
			return ""
		}
		return remote
	}}}
	store.designSystemVerifier = gitReader

	manifest := []byte(`{"schema_version":"2","id":"fixture-system","name":"Fixture","owner":{"level":"project","id":"design-system-http"},"tokens":[{"id":"ink","type":"color","value":"#000000"},{"id":"paper","type":"color","value":"#FFFFFF"},{"id":"font","type":"string","value":"Inter, Arial"},{"id":"body","type":"number","value":16},{"id":"display","type":"number","value":36},{"id":"radius","type":"number","value":8}],"presentation":{"default_context":"main","contexts":[{"id":"main","roles":{"canvas":"ink","surface":"ink","text":"paper","muted_text":"paper","border":"paper","accent":"paper","accent_text":"paper","on_accent":"ink","focus":"paper","success_text":"paper","font_family":"font","body_size":"body","display_size":"display","card_radius":"radius","control_radius":"radius"}}]},"components":[],"assets":["assets/logo.svg"],"contrast_pairs":[]}`)
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
	projectCollection := store.client.Database(store.tenantDatabase("company-a")).Collection(projectsCollection)
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
	if status, body = call(http.MethodGet, endpoint, nil); status != http.StatusServiceUnavailable || !bytes.Contains(body, []byte("design_system_source")) {
		t.Fatalf("unselected Project read admitted a branch with no Design System: status=%d body=%s", status, body)
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

	runFixtureGit(t, work, "push", "--quiet", "origin", "HEAD:"+branchRef)
	// Git patterns match ref suffixes. Put the nested branch first with a
	// different SHA to prove the read selects the exact ref.
	suffixBranch := "refs/heads/feature/" + branchRef
	runFixtureGit(t, work, "push", "--quiet", "origin", expectedHead+":"+suffixBranch)
	refs := fixtureGitOutput(t, work, "ls-remote", "origin", branchRef)
	first, second, ok := strings.Cut(refs, "\n")
	if !ok || first != expectedHead+"\t"+suffixBranch || second != preparedSHA+"\t"+branchRef || expectedHead == preparedSHA {
		t.Fatalf("fixture must put the different suffix match before the exact ref: %s", refs)
	}
	assertDetail := func(path, wantState, wantRevision, wantDigest string) {
		t.Helper()
		status, body := call(http.MethodGet, path, nil)
		var result DesignSystemReadResponse
		if status != http.StatusOK || json.Unmarshal(body, &result) != nil || result.Selection.State != wantState ||
			result.Selection.RepositoryID != binding.RepositoryID || result.Selection.PublicationBranch != binding.PublicationBranch ||
			result.Evaluation.Revision == nil || *result.Evaluation.Revision != wantRevision ||
			result.Evaluation.SelectedLevel == nil || *result.Evaluation.SelectedLevel != "project" ||
			result.Evaluation.InventoryDigest == nil || (wantDigest != "" && *result.Evaluation.InventoryDigest != wantDigest) ||
			(wantState == "unselected" && (result.Selection.SHA != "" || result.Selection.InventoryDigest != "")) {
			t.Fatalf("detail expected state=%s revision=%s digest=%s: status=%d body=%s", wantState, wantRevision, wantDigest, status, body)
		}
	}
	assertGitCalls := func(wantBranches, wantReads int32) {
		t.Helper()
		if branches, reads := gitReader.branchCalls.Load(), gitReader.readCalls.Load(); branches != wantBranches || reads != wantReads {
			t.Fatalf("Git calls: branch=%d immutable=%d, want branch=%d immutable=%d", branches, reads, wantBranches, wantReads)
		}
	}
	branchCalls, readCalls := gitReader.branchCalls.Load(), gitReader.readCalls.Load()
	assertDetail(endpoint, "unselected", preparedSHA, *evaluation.InventoryDigest)
	assertGitCalls(branchCalls+1, readCalls+1)

	if err := os.WriteFile(assetPath, []byte("<svg><title>next</title></svg>"), 0o600); err != nil {
		t.Fatal(err)
	}
	runFixtureGit(t, work, "add", designSystemRoot)
	runFixtureGit(t, work, "commit", "--quiet", "-m", "advance Design System branch")
	nextSHA := fixtureGitOutput(t, work, "rev-parse", "HEAD")
	runFixtureGit(t, work, "push", "--quiet", "origin", "HEAD:"+branchRef)
	assertDetail(endpoint, "unselected", nextSHA, "")
	assertGitCalls(branchCalls+2, readCalls+2)
	assetRoute := endpoint + "/" + preparedSHA + "/assets/assets/logo.svg"
	if status, body := call(http.MethodGet, assetRoute, nil); status != http.StatusOK || !bytes.Equal(body, assetBytes) {
		t.Fatalf("unselected immutable asset after branch advance: status=%d body=%s", status, body)
	}
	assertGitCalls(branchCalls+2, readCalls+3)
	if status, body := call(http.MethodGet, endpoint+"/state", nil); status != http.StatusOK ||
		!bytes.Contains(body, []byte(`"state":"unselected"`)) || bytes.Contains(body, []byte(`"sha":`)) {
		t.Fatalf("read persisted selection: status=%d body=%s", status, body)
	}

	// The suffix collision belongs to the branch-read fixture only.
	runFixtureGit(t, work, "push", "--quiet", "origin", ":"+suffixBranch)
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
	assertDetail(endpoint, "selected", preparedSHA, *evaluation.InventoryDigest)
	assertGitCalls(branchCalls+2, readCalls+4)
	if status, body := call(http.MethodGet, assetRoute, nil); status != http.StatusOK || !bytes.Equal(body, assetBytes) {
		t.Fatalf("pinned asset after branch advance: status=%d body=%s", status, body)
	}
	assertGitCalls(branchCalls+2, readCalls+5)
	if status, body := call(http.MethodGet, endpoint+"/"+nextSHA, nil); status != http.StatusConflict || !bytes.Contains(body, []byte("revision")) {
		t.Fatalf("selected read admitted conflicting branch SHA: status=%d body=%s", status, body)
	}
	assertGitCalls(branchCalls+2, readCalls+5)
	invalidPin := "sha256:" + strings.Repeat("f", 64)
	if invalidPin == *evaluation.InventoryDigest {
		t.Fatal("fixture mismatch digest equals the selected digest")
	}
	setPinnedDigest := func(digest string) {
		t.Helper()
		if _, err := projectCollection.UpdateOne(ctx, bson.M{"id": projectID}, bson.M{"$set": bson.M{"design_system_inventory_digest": digest}}); err != nil {
			t.Fatal(err)
		}
	}
	setPinnedDigest(invalidPin)
	for _, path := range []string{endpoint, assetRoute} {
		if status, body := call(http.MethodGet, path, nil); status != http.StatusServiceUnavailable ||
			!bytes.Contains(body, []byte("design_system_source")) || bytes.Contains(body, assetBytes) {
			t.Fatalf("inconsistent pinned digest served content at %s: status=%d body=%s", path, status, body)
		}
	}
	assertGitCalls(branchCalls+2, readCalls+7)
	setPinnedDigest(*evaluation.InventoryDigest)

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
				Action: "replace", ProjectID: projectID, ExpectedRevision: 1, OperationID: "read-race-rebind", Actor: "fixture-owner", RepositoryID: "belluga/fixture-next", Branch: binding.PublicationBranch,
			})
			return err
		},
	}
	if status, body = call(http.MethodGet, endpoint+"/"+preparedSHA+"/assets/assets/logo.svg", nil); status != http.StatusConflict || !strings.Contains(string(body), "selection") {
		t.Fatalf("asset read returned bytes after a concurrent rebind: status=%d body=%s", status, body)
	}

	store.designSystemVerifier = gitReader
	branchCalls, readCalls = gitReader.branchCalls.Load(), gitReader.readCalls.Load()
	projectFilter := bson.M{"id": projectID}
	partialBinding := bson.M{"repositoryid": binding.RepositoryID}
	for _, test := range []struct {
		name    string
		binding any
	}{
		{name: "missing"},
		{name: "invalid", binding: &Binding{RepositoryID: binding.RepositoryID, PublicationBranch: "bad..branch"}},
		{name: "partial", binding: partialBinding},
	} {
		t.Run(test.name+" visual binding takes precedence over residual selection", func(t *testing.T) {
			residual := bson.M{"binding": test.binding, "design_system_state": "selected", "design_system_sha": preparedSHA, "design_system_inventory_digest": *evaluation.InventoryDigest}
			if _, err := projectCollection.UpdateOne(ctx, bson.M{"id": projectID}, bson.M{"$set": residual}); err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{endpoint, assetRoute} {
				status, body := call(http.MethodGet, path, nil)
				var failure struct {
					TeachDetail TeachDiagnostic `json:"teach_detail"`
				}
				if status != http.StatusServiceUnavailable || json.Unmarshal(body, &failure) != nil ||
					failure.TeachDetail.FieldPath != "repository_binding" ||
					!strings.Contains(failure.TeachDetail.NextAction, "configured with a valid Project visual repository and publication branch") {
					t.Fatalf("binding TEACH missing for %s: status=%d body=%s", path, status, body)
				}
			}
			catalog, err := store.Catalog(ctx)
			if err != nil || len(catalog.Projects) != 1 || catalog.Projects[0].ID != projectID || catalog.Projects[0].Binding != nil {
				t.Fatalf("catalog did not isolate %s visual binding: %+v, %v", test.name, catalog, err)
			}
			if project, err := store.Project(ctx, "company-a", projectID); err != nil || project.ID != projectID || project.Binding != nil {
				t.Fatalf("Project metadata did not isolate %s visual binding: %+v, %v", test.name, project, err)
			}
			var stored projectDocument
			found := projectCollection.FindOne(ctx, projectFilter)
			if err := found.Decode(&stored); err != nil {
				t.Fatal(err)
			}
			if test.name == "partial" {
				if stored.Binding == nil || stored.Binding.RepositoryID != binding.RepositoryID || stored.Binding.PublicationBranch != "" {
					t.Fatalf("fixture did not decode a binding with only the repository populated: %+v", stored.Binding)
				}
				raw, err := found.Raw()
				if err != nil {
					t.Fatal(err)
				}
				previous, ok := raw.Lookup("binding").DocumentOK()
				if !ok || previous.Lookup("publicationbranch").Type != 0 {
					t.Fatalf("partial binding fixture unexpectedly contains a BSON branch field: %v", previous)
				}
			}
			projection := repositoryBindingFromProject(Project{Binding: stored.Binding, BindingRevision: stored.BindingRevision})
			if projection.State != "absent" || projection.RepositoryID != "" || projection.PublicationBranch != "" || projection.Revision != 2 {
				t.Fatalf("visual Git projection exposed %s binding: %+v", test.name, projection)
			}
			actual, err := store.ReadRepositoryBinding(ctx, "company-a", projectID)
			if test.binding == nil {
				if err != nil || actual != projection {
					t.Fatalf("missing binding should read as absent: %+v, %v", actual, err)
				}
			} else {
				var teach *TODOValidationError
				if !errors.Is(err, ErrUnavailable) || !errors.As(err, &teach) || teach.Field != "repository_binding" ||
					actual.RepositoryID != "" || actual.PublicationBranch != "" || actual.State == "configured" {
					t.Fatalf("invalid binding read should return scoped TEACH: %+v, %v", actual, err)
				}
			}
			if overview, err := store.ReadOverview(ctx, "company-a", projectID, 0); err != nil || overview.State != "not_authored" {
				t.Fatalf("Overview unavailable with %s visual binding: %+v, %v", test.name, overview, err)
			}
			if roadmap, err := store.Roadmap(ctx, projectID, "company-a"); err != nil || !bytes.Contains(roadmap, []byte(projectID)) {
				t.Fatalf("Roadmap unavailable with %s visual binding: %s, %v", test.name, roadmap, err)
			}
			if _, err := store.ReadTODO(ctx, "company-a", projectID, "missing-todo", nil); !errors.Is(err, ErrNotFound) {
				t.Fatalf("exact TODO lookup did not reach not-found with %s visual binding: %v", test.name, err)
			}
			assertGitCalls(branchCalls, readCalls)
		})
	}
	repair := RepositoryBindingRequest{
		Action: "replace", ProjectID: projectID, ExpectedRevision: 2, OperationID: "repair-invalid-binding", Actor: "fixture-owner", RepositoryID: binding.RepositoryID, Branch: binding.PublicationBranch,
	}
	// A valid replacement pair cannot carry along malformed private metadata.
	badBinding := Binding{RepositoryID: binding.RepositoryID, PublicationBranch: "bad..branch", SourceBindingsPath: "../old-state"}
	if _, err := projectCollection.UpdateOne(ctx, projectFilter, bson.M{"$set": bson.M{"binding": badBinding}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ChangeRepositoryBinding(ctx, "company-a", projectID, repair); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("replace admitted invalid resulting binding: %v", err)
	}
	var preserved projectDocument
	if err := projectCollection.FindOne(ctx, projectFilter).Decode(&preserved); err != nil ||
		preserved.Binding == nil || *preserved.Binding != badBinding || preserved.BindingRevision != repair.ExpectedRevision {
		t.Fatalf("failed repair modified invalid binding or consumed revision: %+v, %v", preserved.Binding, err)
	}
	for _, candidate := range []any{
		Binding{RepositoryID: binding.RepositoryID, PublicationBranch: "bad..branch"},
		partialBinding,
	} {
		if _, err := projectCollection.UpdateOne(ctx, projectFilter, bson.M{"$set": bson.M{"binding": candidate}}); err != nil {
			t.Fatal(err)
		}
		repaired, err := store.ChangeRepositoryBinding(ctx, "company-a", projectID, repair)
		if err != nil || repaired.State != "configured" || repaired.Revision != repair.ExpectedRevision+1 {
			t.Fatalf("cannot repair malformed visual binding through replace: %+v, %v", repaired, err)
		}
		assertDetail(endpoint, "unselected", nextSHA, "")
		branchCalls++
		readCalls++
		assertGitCalls(branchCalls, readCalls)
		repair.ExpectedRevision = repaired.Revision
		repair.OperationID = "repair-partial-binding"
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
