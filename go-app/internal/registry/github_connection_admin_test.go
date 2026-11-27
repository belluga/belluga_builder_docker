package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
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
	const company, project, otherCompany = "company-a", "project-a", "company-b"
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		_ = store.client.Database(store.tenantDatabase(company)).Drop(cleanup)
		_ = store.client.Database(store.tenantDatabase(otherCompany)).Drop(cleanup)
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
	var revokeUserAuthorization atomic.Bool
	var repositoryReads atomic.Int32
	const receiptRepo = `{"id":34,"node_id":"R_34","full_name":"company/new","default_branch":"trunk","private":true,"owner":{"id":18,"login":"company"}}`
	const publicRepo = `{"id":35,"node_id":"R_35","full_name":"company/public","default_branch":"trunk","private":false,"owner":{"id":18,"login":"company"}}`
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
			if revokeUserAuthorization.Load() {
				return githubAppTestResponse(401, `{}`), nil
			}
			if synchronize.Load() {
				arrived <- struct{}{}
				select {
				case <-release:
				case <-r.Context().Done():
					return nil, r.Context().Err()
				}
			}
			return githubAppTestResponse(200, `{"installations":[{"id":9,"app_id":42,"account":{"id":18,"login":"company","type":"Organization"}},{"id":10,"app_id":42,"account":{"id":81,"login":"operator","type":"User"}},{"id":11,"app_id":99,"account":{"id":11,"login":"wrong-app","type":"Organization"}},{"id":12,"app_id":42,"account":{"id":12,"login":"suspended","type":"Organization"},"suspended_at":"2024-01-01T00:00:00Z"}]}`), nil
		case "/user/installations/9/repositories":
			repositoryReads.Add(1)
			if creates.Load() == 3 {
				return githubAppTestResponse(200, `{"repositories":[`+publicRepo+`]}`), nil
			}
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
		case "/repositories/35":
			return githubAppTestResponse(200, publicRepo), nil
		case "/repos/company/public/branches/trunk":
			return githubAppTestResponse(200, `{"name":"trunk"}`), nil
		case "/repos/company/repo/branches/main":
			return githubAppTestResponse(200, `{"name":"main"}`), nil
		case "/orgs/company/repos":
			attempt := creates.Add(1)
			if r.Header.Get("Authorization") != "Bearer user-one" || r.Method != http.MethodPost {
				t.Fatal("create did not use the current operator authorization")
			}
			if attempt == 3 {
				var fields struct {
					Name    string `json:"name"`
					Private *bool  `json:"private"`
				}
				if err := json.NewDecoder(r.Body).Decode(&fields); err != nil || fields.Name != "public" || fields.Private == nil || *fields.Private {
					t.Fatalf("unexpected provider request: %+v, %v", fields, err)
				}
				return githubAppTestResponse(201, publicRepo), nil
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

	// Company discovery uses the same signed operator's OAuth grant after the
	// previous binding has completed and released its operation-local token.
	discovery, err := restarted.openDiscovery(ctx, company, "", operator, "new-project-discovery", false)
	if err != nil || discovery.AuthorizationURL != "" || discovery.State != githubConnectionPending || discovery.GitHubAccountLogin != "" || discovery.Installations == nil {
		t.Fatalf("expected direct picker without copying an installation: %+v, %v", discovery, err)
	}
	scope := githubUserScope{company, operator.Issuer, operator.Subject}
	_, resumed, err := restarted.load(ctx, company, "", operator, discovery.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if token, ok := restarted.userToken(company, resumed); !ok || token != restarted.authorizations[scope].token {
		t.Fatal("scope reuse changed the token or its expiry")
	}
	if _, err := store.Register(ctx, Registration{CompanyID: otherCompany, CompanyName: "Other Company", ProjectID: project, ProjectName: "Project"}); err != nil {
		t.Fatal(err)
	}
	for _, denied := range []struct {
		company  string
		operator githubOperator
	}{
		{company, githubOperator{operator.Issuer, "different-subject"}},
		{company, githubOperator{"https://other-access.test", operator.Subject}},
		{otherCompany, operator},
	} {
		state, err := restarted.openDiscovery(ctx, denied.company, "", denied.operator, "no-scoped-grant", false)
		if err != nil || state.Installations != nil || state.AuthorizationURL != "" {
			t.Fatalf("scoped authorization escaped to %+v: %+v, %v", denied, state, err)
		}
		if _, _, err := restarted.load(ctx, denied.company, "", denied.operator, "no-scoped-grant"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("probe without a grant persisted an operation: %v", err)
		}
	}
	cold := newGitHubConnectionAdmin(store, app)
	unavailable, err := cold.openDiscovery(ctx, company, "", operator, "cold-discovery", false)
	if err != nil || unavailable.Installations != nil || unavailable.AuthorizationURL != "" {
		t.Fatalf("restart reused in-memory credentials: %+v, %v", unavailable, err)
	}
	oauth, err := cold.openDiscovery(ctx, company, "", operator, "cold-discovery", true)
	if err != nil {
		t.Fatal(err)
	}
	if u, err := url.Parse(oauth.AuthorizationURL); err != nil || u.Path != "/login/oauth/authorize" {
		t.Fatalf("discovery did not fall back to OAuth: %v", err)
	}
	pending, err := cold.oauthCallback(ctx, operator, stateFromURL(oauth), "code")
	if err != nil || pending.State != githubConnectionPending || pending.GitHubAccountLogin != "" {
		t.Fatalf("OAuth unexpectedly selected an installation: %+v, %v", pending, err)
	}
	picker, err := restarted.status(ctx, company, "", operator, discovery.OperationID)
	if err != nil || picker.Installations == nil || len(*picker.Installations) != 2 || (*picker.Installations)[0].ID != 9 || (*picker.Installations)[1].ID != 10 ||
		(*picker.Installations)[0].SettingsURL != "https://github.com/organizations/company/settings/installations/9" ||
		(*picker.Installations)[1].SettingsURL != "https://github.com/settings/installations/10" {
		t.Fatalf("unexpected authorized choices: %+v, %v", picker.Installations, err)
	}
	others := githubOperator{operator.Issuer, "different-subject"}
	if _, err := restarted.selectDiscoveryInstallation(ctx, company, "", others, discovery.OperationID, 9); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another subject selected the installation: %v", err)
	}
	for _, id := range []int64{11, 12, 99} {
		if _, err := restarted.selectDiscoveryInstallation(ctx, company, "", operator, discovery.OperationID, id); !errors.Is(err, ErrConflict) {
			t.Fatalf("unauthorized installation %d accepted: %v", id, err)
		}
	}
	verified, err := restarted.selectDiscoveryInstallation(ctx, company, "", operator, discovery.OperationID, 9)
	if err != nil || verified.State != githubConnectionVerified || verified.GitHubAccountLogin != "company" || verified.SettingsURL != "https://github.com/organizations/company/settings/installations/9" {
		t.Fatalf("account selection: %+v, %v", verified, err)
	}
	selectedStatus, err := restarted.status(ctx, company, "", operator, discovery.OperationID)
	if err != nil || selectedStatus.State != githubConnectionVerified || selectedStatus.GitHubAccountLogin != "company" {
		t.Fatalf("live selection was not restored: %+v, %v", selectedStatus, err)
	}
	coldStatus, err := cold.status(ctx, company, "", operator, discovery.OperationID)
	if err != nil || coldStatus.State != githubConnectionPending || coldStatus.GitHubAccountLogin != "" || coldStatus.DiscoveryExpired {
		t.Fatalf("a durable selection without a live token looked authorized: %+v, %v", coldStatus, err)
	}
	if _, err := restarted.selectDiscoveryInstallation(ctx, company, "", operator, discovery.OperationID, 10); !errors.Is(err, ErrConflict) {
		t.Fatalf("selected installation changed without a new discovery: %v", err)
	}
	// Requesting installation returns no installation ID. The receipt survives a cold Registry
	// and a concurrent callback replay, but cannot be used as a discovery/authorization operation.
	pendingDiscovery, err := restarted.openDiscovery(ctx, company, "", operator, "pending-org-approval", true)
	if err != nil {
		t.Fatal(err)
	}
	type callbackResult struct {
		status githubConnectionStatus
		err    error
	}
	installResults := make(chan callbackResult, 2)
	for range 2 {
		go func() {
			status, installErr := restarted.installDiscovery(ctx, company, "", operator, pendingDiscovery.OperationID)
			installResults <- callbackResult{status, installErr}
		}()
	}
	issued, refused := <-installResults, <-installResults
	if issued.err != nil {
		issued, refused = refused, issued
	}
	if issued.err != nil || issued.status.AuthorizationURL == "" || !errors.Is(refused.err, ErrConflict) || refused.status.AuthorizationURL != "" {
		t.Fatalf("expected one setup link and an actionable conflict: %+v %+v", issued, refused)
	}
	if result, err := restarted.installDiscovery(ctx, company, "", operator, pendingDiscovery.OperationID); !errors.Is(err, ErrConflict) || result.AuthorizationURL != "" {
		t.Fatalf("repeated install overwrote the issued setup nonce: %+v %v", result, err)
	}
	rawPendingState := stateFromURL(issued.status)
	if _, err := restarted.requestInstallationCallback(ctx, others, rawPendingState); !errors.Is(err, ErrNotFound) {
		t.Fatalf("different operator callback: %v", err)
	}
	callbackResults := make(chan callbackResult, 2)
	for range 2 {
		go func() {
			status, callbackErr := restarted.requestInstallationCallback(ctx, operator, rawPendingState)
			callbackResults <- callbackResult{status, callbackErr}
		}()
	}
	first, second := <-callbackResults, <-callbackResults
	if first.err != nil || second.err != nil || first.status.State != githubConnectionAwaitingOrganizationApproval ||
		first.status.InstallationRequestedAt == nil || second.status.InstallationRequestedAt == nil ||
		!first.status.InstallationRequestedAt.Equal(*second.status.InstallationRequestedAt) ||
		first.status.ExpiresAt == nil || first.status.ExpiresAt.Sub(*first.status.InstallationRequestedAt) != 30*24*time.Hour {
		t.Fatalf("request and concurrent replay diverged: %+v %+v", first, second)
	}
	persisted, err := cold.status(ctx, company, "", operator, pendingDiscovery.OperationID)
	if err != nil || persisted.State != githubConnectionAwaitingOrganizationApproval || persisted.Installations != nil || persisted.AuthorizationURL != "" {
		t.Fatalf("cold receipt leaked authorization or lost state: %+v %v", persisted, err)
	}
	latest, err := cold.pendingInstallation(ctx, company, "", operator)
	if err != nil || latest.Operation == nil || latest.Operation.OperationID != pendingDiscovery.OperationID {
		t.Fatalf("missing receipt: %+v %v", latest, err)
	}
	for _, scope := range []struct {
		company, project string
		operator         githubOperator
	}{
		{company, project, operator}, {otherCompany, "", operator}, {company, "", others},
	} {
		latest, err := cold.pendingInstallation(ctx, scope.company, scope.project, scope.operator)
		if err != nil || latest.Operation != nil {
			t.Fatalf("receipt crossed scope: %+v %v", latest, err)
		}
	}
	if _, err := cold.openDiscovery(ctx, company, "", operator, pendingDiscovery.OperationID, true); !errors.Is(err, ErrConflict) {
		t.Fatalf("receipt became actionable: %v", err)
	}
	if _, err := cold.setupCallback(ctx, operator, rawPendingState, 10); !errors.Is(err, ErrConflict) {
		t.Fatalf("consumed state still accepted installation: %v", err)
	}
	if _, err := db.Collection(githubConnectionOperationsCollection).UpdateOne(ctx, githubOperatorFilter("", operator, pendingDiscovery.OperationID), bson.M{"$set": bson.M{"expires_at": app.now().Add(-time.Second)}}); err != nil {
		t.Fatal(err)
	}
	if latest, err := cold.pendingInstallation(ctx, company, "", operator); err != nil || latest.Operation != nil {
		t.Fatalf("expired receipt was visible: %+v %v", latest, err)
	}

	fallback, err := restarted.openDiscovery(ctx, company, project, operator, "install-other-account", true)
	if err != nil || fallback.Installations == nil || fallback.AuthorizationURL != "" {
		t.Fatalf("changing account did not reopen the scoped picker: %+v, %v", fallback, err)
	}
	install, err := restarted.installDiscovery(ctx, company, project, operator, fallback.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if u, err := url.Parse(install.AuthorizationURL); err != nil || !strings.HasSuffix(u.Path, "/installations/new") {
		t.Fatalf("explicit install did not return setup link: %v", err)
	}
	setup, err := restarted.setupCallback(ctx, operator, stateFromURL(install), 10)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := restarted.oauthCallback(ctx, operator, stateFromURL(setup), "code")
	if err != nil || resolved.State != githubConnectionVerified || resolved.GitHubAccountLogin != "operator" || resolved.GitHubAccountType != "User" {
		t.Fatalf("explicit install did not verify provider account: %+v, %v", resolved, err)
	}
	if _, err := restarted.finalize(ctx, company, "", operator, "new-project-discovery", input); !errors.Is(err, ErrConflict) {
		t.Fatalf("discovery could be finalized directly: %v", err)
	}
	newProject := "project-onboarded"
	if _, err := store.createAdminProject(ctx, company, operator, adminRegistration{Name: "Onboarded", Slug: newProject, Operation: "new-project", DiscoveryOperation: "new-project-discovery"}); err != nil {
		t.Fatal(err)
	}
	binding := githubConnectionStartRequest{OperationID: "bind-discovery", Role: "visual", Action: "connect",
		ExpectedRoleRevision: &zero, ExpectedConnectionRevision: &zero, DiscoveryOperation: "new-project-discovery", allowCompanyDiscovery: true}
	const olderProject = "older-project"
	if _, err := store.createAdminProject(ctx, company, operator, adminRegistration{Name: "Older", Slug: olderProject, Operation: "older-project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.start(ctx, company, olderProject, operator, binding); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Company discovery attached to an unrelated Project from the same creator: %v", err)
	}
	withoutCompanyAuthority := binding
	withoutCompanyAuthority.allowCompanyDiscovery = false
	if _, err := restarted.start(ctx, company, newProject, operator, withoutCompanyAuthority); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Company discovery attached without Company write authority: %v", err)
	}
	if result, err := restarted.start(ctx, company, newProject, operator, binding); err != nil || result.State != githubConnectionVerified || result.AuthorizationURL != "" {
		t.Fatalf("bind verified discovery: %+v, %v", result, err)
	}
	double := binding
	double.OperationID = "bind-discovery-again"
	if _, err := restarted.start(ctx, company, newProject, operator, double); !errors.Is(err, ErrConflict) {
		t.Fatalf("discovery was used for a second operation: %v", err)
	}
	bindInput := githubConnectionFinalizeRequest{GitHubRepositoryID: 33, ExpectedRoleRevision: &zero, ExpectedConnectionRevision: &zero}
	if result, err := restarted.finalize(ctx, company, newProject, operator, binding.OperationID, bindInput); err != nil || result.State != githubConnectionCompleted {
		t.Fatalf("complete verified binding: %+v, %v", result, err)
	}

	// An unexpired credential must not make consumed or expired discovery
	// records eligible for provider repository reads.
	_, consumed, err := restarted.load(ctx, company, "", operator, discovery.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	restarted.users[githubAdminKey(company, consumed)] = restarted.authorizations[scope].token
	beforeReads := repositoryReads.Load()
	if _, err := restarted.repositories(ctx, company, "", operator, discovery.OperationID, 1); !errors.Is(err, ErrConflict) || repositoryReads.Load() != beforeReads {
		t.Fatalf("consumed discovery reached GitHub: %v", err)
	}
	expiring, err := restarted.openDiscovery(ctx, company, project, operator, "expiring-repositories", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.selectDiscoveryInstallation(ctx, company, project, operator, expiring.OperationID, 9); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.repositories(ctx, company, project, operator, expiring.OperationID, 1); err != nil {
		t.Fatalf("live discovery cannot list repositories: %v", err)
	}
	if _, err := db.Collection(githubConnectionOperationsCollection).UpdateOne(ctx, bson.M{"operation_id": expiring.OperationID}, bson.M{"$set": bson.M{"expires_at": app.now().Add(time.Minute)}}); err != nil {
		t.Fatal(err)
	}
	beforeReads = repositoryReads.Load()
	clockBeforeExpiry := app.now
	afterDiscoveryExpiry := clockBeforeExpiry().Add(2 * time.Minute)
	app.now = func() time.Time { return afterDiscoveryExpiry }
	_, err = restarted.repositories(ctx, company, project, operator, expiring.OperationID, 1)
	app.now = clockBeforeExpiry
	if !errors.Is(err, ErrConflict) || repositoryReads.Load() != beforeReads {
		t.Fatalf("expired discovery reached GitHub: %v", err)
	}

	public := githubConnectionStartRequest{OperationID: "create-public", Role: "visual", Action: "create",
		ExpectedRoleRevision: &zero, ExpectedConnectionRevision: &zero, TargetAccountID: 18, TargetRepositoryName: "public", Visibility: "public"}
	// The older Project still has no repository binding after the denied attach.
	publicStart, err := admin.start(ctx, company, olderProject, operator, public)
	if err != nil {
		t.Fatal(err)
	}
	publicOAuth, err := admin.setupCallback(ctx, operator, stateFromURL(publicStart), 9)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.oauthCallback(ctx, operator, stateFromURL(publicOAuth), "code"); err != nil {
		t.Fatal(err)
	}
	changedVisibility := public
	changedVisibility.Visibility = "private"
	if _, err := admin.start(ctx, company, olderProject, operator, changedVisibility); !errors.Is(err, ErrConflict) {
		t.Fatalf("a visibility-only change was accepted as a replay: %v", err)
	}
	publicFinalize := githubConnectionFinalizeRequest{ExpectedRoleRevision: &zero, ExpectedConnectionRevision: &zero}
	for range 2 {
		result, err := admin.finalize(ctx, company, olderProject, operator, public.OperationID, publicFinalize)
		if err != nil || result.State != githubConnectionCompleted || result.Repository == nil || result.Repository.Branch != "trunk" || creates.Load() != 3 {
			t.Fatalf("public create/replay did not preserve receipt and avoid another POST: %+v, %v", result, err)
		}
	}
	var publicRecord bson.M
	if err := db.Collection(githubConnectionOperationsCollection).FindOne(ctx, bson.M{"operation_id": public.OperationID}).Decode(&publicRecord); err != nil || publicRecord["result_repository_visibility"] != "public" {
		t.Fatalf("public visibility absent from durable receipt: %v", err)
	}

	for _, id := range []string{"revoked-status", "revoked-retry"} {
		if state, err := restarted.openDiscovery(ctx, company, "", operator, id, false); err != nil || state.Installations == nil {
			t.Fatalf("could not prepare live authorization: %+v, %v", state, err)
		}
	}
	revokeUserAuthorization.Store(true)
	revoked, err := restarted.status(ctx, company, "", operator, "revoked-status")
	if err != nil || revoked.State != githubConnectionPending || revoked.AuthorizationURL != "" || revoked.Installations != nil {
		t.Fatalf("passive status did not make revocation recoverable: %+v, %v", revoked, err)
	}
	if _, ok := restarted.authorizations[scope]; ok {
		t.Fatal("denied credential remained in Company scope")
	}
	replacement := githubUserAuthorization{token: githubAppToken{value: "newer-user-token", expiresAt: app.now().Add(time.Minute)}, githubUserID: 81}
	restarted.authorizations[scope] = replacement
	for _, id := range []string{"revoked-status", "revoked-retry"} {
		resumed, err := restarted.openDiscovery(ctx, company, "", operator, id, true)
		if err != nil || !strings.Contains(resumed.AuthorizationURL, "/login/oauth/authorize?") {
			t.Fatalf("explicit retry did not reauthorize %s: %+v, %v", id, resumed, err)
		}
	}
	if restarted.authorizations[scope] != replacement {
		t.Fatal("revocation removed a newer scoped credential")
	}
	revokeUserAuthorization.Store(false)

	originalClock := app.now
	expiredAt := originalClock().Add(2 * time.Hour)
	app.now = func() time.Time { return expiredAt }
	defer func() { app.now = originalClock }()
	expiredSelection, err := restarted.status(ctx, company, project, operator, fallback.OperationID)
	if err != nil || !expiredSelection.DiscoveryExpired || expiredSelection.State != githubConnectionPending || expiredSelection.GitHubAccountLogin != "" {
		t.Fatalf("expired saved discovery looked authorized: %+v, %v", expiredSelection, err)
	}
	expired, err := restarted.openDiscovery(ctx, company, "", operator, "expired-discovery", false)
	if err != nil || expired.Installations != nil || expired.AuthorizationURL != "" {
		t.Fatalf("expired authorization opened the picker: %+v, %v", expired, err)
	}
	fresh, err := restarted.openDiscovery(ctx, company, "", operator, "expired-discovery", true)
	if err != nil || !strings.Contains(fresh.AuthorizationURL, "/login/oauth/authorize?") {
		t.Fatalf("explicit connection did not reauthorize after expiry: %+v, %v", fresh, err)
	}

}

func TestGitHubInstallationSettingsURLUsesVerifiedIdentity(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		id                       int64
		login, accountType, want string
	}{
		{"personal", 10, "user-10", "User", "https://github.com/settings/installations/10"},
		{"organization", 11, "Org-11", "Organization", "https://github.com/organizations/Org-11/settings/installations/11"},
		{"not verified", 0, "Org-11", "Organization", ""},
		{"encoded slash", 11, "org%2Fsettings", "Organization", ""},
		{"unknown type", 11, "user-10", "Enterprise", ""},
		{"missing personal identity", 11, "", "User", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := githubInstallationSettingsURL(tc.id, tc.login, tc.accountType); got != tc.want {
				t.Fatalf("settings URL = %q, want %q", got, tc.want)
			}
			op := githubConnectionOperation{Action: "discover", State: githubConnectionVerified, VerifiedInstallationID: tc.id, GitHubAccountLogin: tc.login, GitHubAccountType: tc.accountType}
			if got := githubOperationStatus("alpha", op).SettingsURL; got != tc.want {
				t.Fatalf("verified operation settings URL = %q, want %q", got, tc.want)
			}
			op.State = githubConnectionPending
			if got := githubOperationStatus("alpha", op).SettingsURL; got != "" {
				t.Fatalf("pending operation exposed settings URL: %q", got)
			}
		})
	}
}
