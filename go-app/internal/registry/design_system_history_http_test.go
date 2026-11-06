package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	ac "belluga-builder-viewer/artifact_catalog"
	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestDesignSystemHistoryAndAsOfUseConfiguredBranchAndExactRevision(t *testing.T) {
	uri := os.Getenv("REGISTRY_TEST_MONGODB_URI")
	if uri == "" {
		t.Skip("isolated MongoDB fixture not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	stamp := fmt.Sprintf("test_ds_history_%d", time.Now().UnixNano())
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

	projectID := "design-system-history"
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	work := filepath.Join(root, "work")
	runFixtureGit(t, root, "init", "--bare", "--quiet", remote)
	if err := os.Mkdir(work, 0o700); err != nil {
		t.Fatal(err)
	}
	runFixtureGit(t, work, "init", "--quiet", "--initial-branch=main")
	runFixtureGit(t, work, "config", "user.name", "Registry History Fixture")
	runFixtureGit(t, work, "config", "user.email", "registry-history-fixture@example.invalid")
	manifestPath := filepath.Join(work, filepath.FromSlash(designSystemDefinitionPath))
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, historyFixtureManifest("v1"), 0o600); err != nil {
		t.Fatal(err)
	}
	runFixtureGit(t, work, "add", designSystemRoot)
	runFixtureGit(t, work, "commit", "--quiet", "-m", "Design System v1")
	olderSHA := fixtureGitOutput(t, work, "rev-parse", "HEAD")
	if err := os.WriteFile(manifestPath, historyFixtureManifest("v2"), 0o600); err != nil {
		t.Fatal(err)
	}
	runFixtureGit(t, work, "add", designSystemRoot)
	runFixtureGit(t, work, "commit", "--quiet", "-m", "Design System v2")
	currentSHA := fixtureGitOutput(t, work, "rev-parse", "HEAD")
	runFixtureGit(t, work, "remote", "add", "origin", remote)
	runFixtureGit(t, work, "push", "--quiet", "origin", "main")
	runFixtureGit(t, work, "checkout", "--quiet", "-b", "side")
	if err := os.WriteFile(manifestPath, historyFixtureManifest("side-only"), 0o600); err != nil {
		t.Fatal(err)
	}
	runFixtureGit(t, work, "add", designSystemRoot)
	runFixtureGit(t, work, "commit", "--quiet", "-m", "Design System side-only commit")
	sideOnlySHA := fixtureGitOutput(t, work, "rev-parse", "HEAD")
	runFixtureGit(t, work, "push", "--quiet", "origin", "side")
	runFixtureGit(t, work, "checkout", "--quiet", "main")

	oldSource, err := ac.NewGitTree(work, olderSHA)
	if err != nil {
		t.Fatal(err)
	}
	oldEvaluation := ac.EvaluateDesignSystem(oldSource, projectID, "company-a", "project", ac.SourceBinding{RepositoryID: "belluga/history-fixture", DefinitionPath: designSystemDefinitionPath, Revision: &olderSHA})
	currentSource, err := ac.NewGitTree(work, currentSHA)
	if err != nil {
		t.Fatal(err)
	}
	currentEvaluation := ac.EvaluateDesignSystem(currentSource, projectID, "company-a", "project", ac.SourceBinding{RepositoryID: "belluga/history-fixture", DefinitionPath: designSystemDefinitionPath, Revision: &currentSHA})
	if oldEvaluation.Outcome != "go" || currentEvaluation.Outcome != "go" || currentEvaluation.InventoryDigest == nil {
		t.Fatalf("history fixture invalid: old=%+v current=%+v", oldEvaluation.Diagnostics, currentEvaluation.Diagnostics)
	}

	binding := validTestBinding()
	binding.RepositoryID = "belluga/history-fixture"
	binding.PublicationBranch = "main"
	if _, err := registerTestProjectWithBinding(ctx, store, Registration{CompanyID: "company-a", CompanyName: "Company A", ProjectID: projectID, ProjectName: projectID, Binding: binding}); err != nil {
		t.Fatal(err)
	}
	store.designSystemVerifier = gitDesignSystemVerifier{repositoryURL: func(id string) string {
		if id == binding.RepositoryID {
			return remote
		}
		return ""
	}}
	collection := store.client.Database(store.tenantDatabase("company-a")).Collection(projectsCollection)
	if _, err := collection.UpdateOne(ctx, bson.D{{Key: "id", Value: projectID}}, bson.M{"$set": bson.M{"design_system_state": "selected", "design_system_sha": currentSHA, "design_system_inventory_digest": *currentEvaluation.InventoryDigest}}); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(NewHandler(store))
	defer server.Close()
	base := "/internal/v1/companies/company-a/projects/" + projectID + "/design-system"
	call := func(path string) (int, []byte) {
		t.Helper()
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, body
	}
	if status, body := call(base + "/history?limit=101"); status != http.StatusBadRequest || !strings.Contains(string(body), "limit") {
		t.Fatalf("unbounded history query status=%d body=%s", status, body)
	}
	if status, body := call(base + "/history?limit=1"); status != http.StatusOK || !strings.Contains(string(body), `"branch_head_sha":"`+currentSHA+`"`) || !strings.Contains(string(body), `"truncated":true`) {
		t.Fatalf("bounded history page status=%d body=%s", status, body)
	}
	status, body := call(base + "/history")
	var history DesignSystemHistoryResponse
	if status != http.StatusOK || json.Unmarshal(body, &history) != nil || history.BranchHeadSHA != currentSHA || len(history.Revisions) != 2 || history.Revisions[0].SHA != currentSHA || !history.Revisions[0].Current || history.Revisions[1].SHA != olderSHA || history.Revisions[1].Current {
		t.Fatalf("exact Design System history = status %d %+v body=%s", status, history, body)
	}
	status, body = call(base + "/history/" + olderSHA)
	var asOf DesignSystemAsOfResponse
	if status != http.StatusOK || json.Unmarshal(body, &asOf) != nil || asOf.Revision != olderSHA || asOf.Current || asOf.BranchHeadSHA != currentSHA || asOf.Evaluation.Outcome != "go" || asOf.Evaluation.Revision == nil || *asOf.Evaluation.Revision != olderSHA {
		t.Fatalf("as-of exact older revision status=%d response=%+v body=%s", status, asOf, body)
	}
	if status, body = call(base + "/validate/" + olderSHA); status != http.StatusOK || !strings.Contains(string(body), `"outcome":"go"`) || !strings.Contains(string(body), `"current_selected_revision":false`) {
		t.Fatalf("exact historical validation status=%d body=%s", status, body)
	}
	if status, body = call(base + "/" + olderSHA); status != http.StatusConflict || !strings.Contains(string(body), "revision") {
		t.Fatalf("selected-only revision route misrepresented old SHA: status=%d body=%s", status, body)
	}
	if status, body = call(base + "/" + currentSHA); status != http.StatusOK || !strings.Contains(string(body), `"sha":"`+currentSHA+`"`) {
		t.Fatalf("current selected exact route changed behavior: status=%d body=%s", status, body)
	}
	if status, body = call(base + "/history/not-a-full-sha"); status != http.StatusBadRequest || !strings.Contains(string(body), "revision") {
		t.Fatalf("invalid as-of SHA status=%d body=%s", status, body)
	}
	if status, body = call(base + "/history/" + sideOnlySHA); status != http.StatusNotFound || !strings.Contains(string(body), "not_found") {
		t.Fatalf("commit from another branch was admitted as configured history: status=%d body=%s", status, body)
	}
}

func historyFixtureManifest(value string) []byte {
	return []byte(fmt.Sprintf(`{"schema_version":"1","id":"history-system","name":"History System","owner":{"level":"project","id":"design-system-history"},"tokens":[{"id":"label","type":"string","value":%q}],"components":[],"assets":[],"contrast_pairs":[]}`, value))
}
