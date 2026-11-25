package registry

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestGitHubConnectionStateOnlyCarriesRouting(t *testing.T) {
	state, err := githubStateValue("company-a", "project-a", "op-a")
	if err != nil {
		t.Fatal(err)
	}
	locator, err := parseGitHubState(state)
	if err != nil || locator.CompanyID != "company-a" || locator.ProjectID != "project-a" || locator.OperationID != "op-a" {
		t.Fatalf("unexpected routing state: %+v, %v", locator, err)
	}
	now := time.Now().UTC()
	nonce := githubConnectionNonce{Digest: sourceHash([]byte(state)), ExpiresAt: now.Add(time.Minute)}
	if !githubNonceUsable(nonce, state, now) || githubNonceUsable(nonce, state+"A", now) ||
		githubNonceUsable(nonce, state, nonce.ExpiresAt) {
		t.Fatal("nonce accepted a mismatched value or expiry")
	}
	nonce.ConsumedAt = &now
	if githubNonceUsable(nonce, state, now) {
		t.Fatal("consumed nonce reused")
	}
}

func TestMongoGitHubAdminConnectAndDoNotRepeatUncertainCreate(t *testing.T) {
	uri := os.Getenv("REGISTRY_TEST_MONGODB_URI")
	if uri == "" {
		if os.Getenv("REGISTRY_REQUIRE_MONGO_INTEGRATION") == "1" {
			t.Fatal("required Mongo integration fixture unavailable")
		}
		t.Skip("isolated MongoDB fixture not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	stamp := fmt.Sprintf("github_admin_%d", time.Now().UnixNano())
	store, err := OpenMongo(ctx, uri, stamp)
	if err != nil {
		t.Fatal(err)
	}
	store.tenantPrefix = stamp
	const company, project = "company-a", "project-a"
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		_ = store.client.Database(store.tenantDatabase(company)).Drop(cleanup)
		_ = store.control.Drop(cleanup)
		_ = store.Close(cleanup)
	})
	if _, err := store.Register(ctx, Registration{CompanyID: company, CompanyName: "Company", ProjectID: project, ProjectName: "Project"}); err != nil {
		t.Fatal(err)
	}
	db := store.client.Database(store.tenantDatabase(company))
	if err := ensureGitHubConnectionIndexes(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := ensureImplementationRepositoryIdentityIndex(ctx, db); err != nil {
		t.Fatal(err)
	}
	var creates atomic.Int32
	var synchronize atomic.Bool
	var newRepositoryVisible atomic.Bool
	const receiptRepo = `{"id":34,"node_id":"R_34","full_name":"company/new","default_branch":"trunk","private":true,"owner":{"id":18,"login":"company"}}`
	const renamedRepo = `{"id":34,"node_id":"R_34","full_name":"company/new-renamed","default_branch":"main","private":true,"owner":{"id":18,"login":"company"}}`
	arrived := make(chan struct{}, 2)
	release := make(chan struct{})
	app := githubAppTestClient(t, func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/login/oauth/access_token":
			return githubAppTestResponse(200, `{"access_token":"user-one","expires_in":3600}`), nil
		case "/user":
			return githubAppTestResponse(200, `{"id":81,"login":"operator"}`), nil
		case "/user/installations":
			if synchronize.Load() {
				arrived <- struct{}{}
				select {
				case <-release:
				case <-r.Context().Done():
					return nil, r.Context().Err()
				}
			}
			return githubAppTestResponse(200, `{"installations":[{"id":9,"app_id":42,"account":{"id":18,"login":"company","type":"Organization"}}]}`), nil
		case "/user/installations/9/repositories":
			if newRepositoryVisible.Load() {
				return githubAppTestResponse(200, `{"repositories":[`+githubAppTestRepo+`,`+renamedRepo+`]}`), nil
			}
			return githubAppTestResponse(200, `{"repositories":[`+githubAppTestRepo+`]}`), nil
		case "/app/installations/9/access_tokens":
			expiry := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
			return githubAppTestResponse(201, fmt.Sprintf(`{"token":"installation-one","expires_at":%q}`, expiry)), nil
		case "/repositories/33":
			return githubAppTestResponse(200, githubAppTestRepo), nil
		case "/repositories/34":
			return githubAppTestResponse(200, renamedRepo), nil
		case "/repos/company/repo/branches/main":
			return githubAppTestResponse(200, `{"name":"main"}`), nil
		case "/orgs/company/repos":
			attempt := creates.Add(1)
			if r.Header.Get("Authorization") != "Bearer user-one" || r.Method != http.MethodPost {
				t.Fatal("create did not use the current operator authorization")
			}
			if attempt == 2 {
				return githubAppTestResponse(201, receiptRepo), nil
			}
			return nil, errors.New("lost response")
		default:
			t.Fatalf("unexpected request: %s", r.URL.Path)
			return nil, errors.New("unexpected request")
		}
	})
	app.now = time.Now
	operator := githubOperator{Issuer: "https://access.test", Subject: "operator-a"}
	admin := newGitHubConnectionAdmin(store, app)
	stateFromURL := func(call githubConnectionStatus) string {
		u, err := url.Parse(call.AuthorizationURL)
		if err != nil || u.Query().Get("state") == "" {
			t.Fatal("no OAuth/setup state in redirect")
		}
		return u.Query().Get("state")
	}
	authorize := func(request githubConnectionStartRequest) {
		t.Helper()
		started, err := admin.start(ctx, company, project, operator, request)
		if err != nil {
			t.Fatal(err)
		}
		firstState := stateFromURL(started)
		rotated, err := admin.start(ctx, company, project, operator, request)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := admin.setupCallback(ctx, operator, firstState, 9); !errors.Is(err, ErrConflict) {
			t.Fatalf("rotated setup nonce accepted: %v", err)
		}
		if _, err := admin.setupCallback(ctx, githubOperator{operator.Issuer, "different-subject"}, stateFromURL(rotated), 9); !errors.Is(err, ErrNotFound) {
			t.Fatalf("state escaped operator: %v", err)
		}
		oauth, err := admin.setupCallback(ctx, operator, stateFromURL(rotated), 9)
		if err != nil {
			t.Fatal(err)
		}
		rawOAuth := stateFromURL(oauth)
		got, err := admin.oauthCallback(ctx, operator, rawOAuth, "code")
		if err != nil || got.State != githubConnectionVerified {
			t.Fatalf("authorization outcome: %+v, %v", got, err)
		}
		if _, err := admin.oauthCallback(ctx, operator, rawOAuth, "code"); !errors.Is(err, ErrConflict) {
			t.Fatalf("OAuth replay accepted: %v", err)
		}
	}

	zero, one := int64(0), int64(1)
	authorize(githubConnectionStartRequest{OperationID: "connect-op", Role: "implementation", Action: "connect",
		ExpectedRoleRevision: &zero, ExpectedConnectionRevision: &zero, TargetAccountID: 18})
	choices, err := admin.repositories(ctx, company, project, operator, "connect-op", 1)
	if err != nil || len(choices.Repositories) != 1 || choices.Repositories[0].ID != 33 {
		t.Fatalf("choices: %+v, %v", choices, err)
	}
	input := githubConnectionFinalizeRequest{GitHubRepositoryID: 33, ExpectedRoleRevision: &zero, ExpectedConnectionRevision: &zero}
	done, err := admin.finalize(ctx, company, project, operator, "connect-op", input)
	if err != nil || done.State != githubConnectionCompleted || done.Repository == nil || done.Repository.ID != 33 {
		t.Fatalf("connect finalization: %+v, %v", done, err)
	}
	set, err := store.ReadImplementationRepositories(ctx, company, project)
	if err != nil || len(set.Items) != 1 || set.Items[0] != "company/repo" || set.GitHubNodeIDs["company/repo"] != "R_33" {
		t.Fatalf("role binding: %+v, %v", set, err)
	}
	reopened := newGitHubConnectionAdmin(store, app)
	replayed, err := reopened.finalize(ctx, company, project, operator, "connect-op", input)
	if err != nil || replayed.State != githubConnectionCompleted {
		t.Fatalf("connect replay: %+v, %v", replayed, err)
	}
	visual, err := store.ChangeRepositoryBinding(ctx, company, project, RepositoryBindingRequest{
		Action: "bind", ProjectID: project, ExpectedRevision: 0, OperationID: "seed-visual", Actor: "operator",
		RepositoryID: "company/repo", Branch: "main",
	})
	if err != nil || visual.Revision != 1 {
		t.Fatalf("seed existing visual role: %+v, %v", visual, err)
	}
	_, err = db.Collection(projectsCollection).UpdateOne(ctx, bson.M{"id": project}, bson.M{"$set": bson.M{
		"design_system_state": "selected", "design_system_sha": "unchanged-selection",
	}})
	if err != nil {
		t.Fatal(err)
	}
	authorize(githubConnectionStartRequest{OperationID: "visual-op", Role: "visual", Action: "connect",
		ExpectedRoleRevision: &one, ExpectedConnectionRevision: &one, TargetAccountID: 18, PublicationBranch: "main"})
	attached, err := admin.finalize(ctx, company, project, operator, "visual-op", githubConnectionFinalizeRequest{
		GitHubRepositoryID: 33, ExpectedRoleRevision: &one, ExpectedConnectionRevision: &one,
	})
	if err != nil || attached.State != githubConnectionCompleted {
		t.Fatalf("attach credential to existing visual role: %+v, %v", attached, err)
	}
	stored, err := readGitHubAdminProject(ctx, db, company, project)
	if err != nil || stored.BindingRevision != 1 || stored.DesignSystemState != "selected" ||
		stored.DesignSystemSHA != "unchanged-selection" || stored.GitHubVisualConnection == nil ||
		stored.GitHubVisualConnection.GitHubRepositoryID != 33 || stored.GitHubVisualConnection.BindingRevision != 1 {
		t.Fatalf("adding access changed existing visual selection or failed to pin repo: %v", err)
	}
	input.GitHubRepositoryID++
	if _, err := reopened.finalize(ctx, company, project, operator, "connect-op", input); !errors.Is(err, ErrConflict) {
		t.Fatalf("different completed digest accepted: %v", err)
	}

	authorize(githubConnectionStartRequest{OperationID: "create-op", Role: "implementation", Action: "create",
		ExpectedRoleRevision: &one, ExpectedConnectionRevision: &zero, TargetAccountID: 18, TargetRepositoryName: "new"})
	create := githubConnectionFinalizeRequest{ExpectedRoleRevision: &one, ExpectedConnectionRevision: &zero}
	type finalizeResult struct {
		status githubConnectionStatus
		err    error
	}
	results := make(chan finalizeResult, 2)
	synchronize.Store(true)
	for range 2 {
		go func() {
			status, err := admin.finalize(ctx, company, project, operator, "create-op", create)
			results <- finalizeResult{status, err}
		}()
	}
	for range 2 {
		select {
		case <-arrived:
		case <-ctx.Done():
			t.Fatal("both finalizations did not reach the provider barrier")
		}
	}
	synchronize.Store(false)
	close(release)
	uncertain := false
	for range 2 {
		result := <-results
		if result.err != nil || (result.status.State != githubConnectionCreating && result.status.State != githubConnectionUncertain) {
			t.Fatalf("concurrent create result: %+v, err=%v", result.status, result.err)
		}
		uncertain = uncertain || result.status.State == githubConnectionUncertain
	}
	if !uncertain || creates.Load() != 1 {
		t.Fatalf("lost create result: uncertain=%t, posts=%d", uncertain, creates.Load())
	}
	for range 2 {
		again, err := reopened.finalize(ctx, company, project, operator, "create-op", create)
		if err != nil || again.State != githubConnectionUncertain || creates.Load() != 1 {
			t.Fatalf("uncertain create replay: %+v, posts=%d, err=%v", again, creates.Load(), err)
		}
	}
	var raw bson.M
	if err := db.Collection(githubConnectionOperationsCollection).FindOne(ctx, bson.M{"operation_id": "create-op"}).Decode(&raw); err != nil {
		t.Fatal(err)
	}
	if raw["expires_at"] != nil || raw["create_reserved"] != true {
		t.Fatal("uncertain effect was not durably reserved")
	}

	if _, err := reopened.abandon(ctx, company, project, githubOperator{operator.Issuer, "different-subject"}, "create-op", true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("different operator abandoned creation: %v", err)
	}
	if _, err := reopened.abandon(ctx, company, project, operator, "create-op", false); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing acknowledgement abandoned creation: %v", err)
	}
	abandoned, err := reopened.abandon(ctx, company, project, operator, "create-op", true)
	if err != nil || abandoned.State != githubConnectionFailed || creates.Load() != 1 {
		t.Fatalf("abandon outcome: %+v, posts=%d, err=%v", abandoned, creates.Load(), err)
	}
	raw = nil
	if err := db.Collection(githubConnectionOperationsCollection).FindOne(ctx, bson.M{"operation_id": "create-op"}).Decode(&raw); err != nil || raw["create_reserved"] != false || raw["abandoned_at"] == nil {
		t.Fatalf("abandon did not durably release reservation: %v", err)
	}
	authorize(githubConnectionStartRequest{OperationID: "create-success", Role: "implementation", Action: "create",
		ExpectedRoleRevision: &one, ExpectedConnectionRevision: &zero, TargetAccountID: 18, TargetRepositoryName: "new"})
	waiting, err := admin.finalize(ctx, company, project, operator, "create-success", create)
	if err != nil || waiting.State != githubConnectionAwaitingRepositoryAccess || waiting.Repository == nil ||
		waiting.Repository.ID != 34 || creates.Load() != 2 {
		t.Fatalf("provider receipt not retained while waiting for App access: %+v, posts=%d, err=%v", waiting, creates.Load(), err)
	}
	restarted := newGitHubConnectionAdmin(store, app)
	reauth, err := restarted.finalize(ctx, company, project, operator, "create-success", create)
	if err != nil || reauth.AuthorizationURL == "" || creates.Load() != 2 {
		t.Fatalf("restart did not require same-operator authorization: %+v, err=%v", reauth, err)
	}
	authorized, err := restarted.oauthCallback(ctx, operator, stateFromURL(reauth), "code")
	if err != nil || authorized.State != githubConnectionAwaitingRepositoryAccess || authorized.GitHubAccountLogin != "company" {
		t.Fatalf("expected reauthorization of the original account: %+v, err=%v", authorized, err)
	}
	newRepositoryVisible.Store(true)
	completed, err := restarted.finalize(ctx, company, project, operator, "create-success", create)
	if err != nil || completed.State != githubConnectionCompleted || completed.Repository == nil ||
		completed.Repository.FullName != "company/new" || completed.Repository.Branch != "trunk" || creates.Load() != 2 {
		t.Fatalf("resumed create did not preserve the original 201 receipt: %+v, posts=%d, err=%v", completed, creates.Load(), err)
	}
	set, err = store.ReadImplementationRepositories(ctx, company, project)
	if err != nil || set.GitHubNodeIDs["company/new-renamed"] != "R_34" {
		t.Fatalf("authorized current repository was not bound: %+v, err=%v", set, err)
	}

}
