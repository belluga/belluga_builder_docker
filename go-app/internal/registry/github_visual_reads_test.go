package registry

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestGitHubVisualWarmPrototypeAssetAndConditionalReadRequireLiveAccess(t *testing.T) {
	const (
		revision = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		path     = "prototypes/demo/index.html"
		etag     = `"prototype-digest"`
	)
	projects := map[string]Project{
		"a": {CompanyID: "team-a", ID: "a", Binding: &Binding{RepositoryID: "company/repo", PublicationBranch: "main"}, BindingRevision: 3, KnowledgeGenerationID: "gen", CanonicalWriteEpoch: 1},
		"b": {CompanyID: "team-b", ID: "b", Binding: &Binding{RepositoryID: "other/repo", PublicationBranch: "main"}, BindingRevision: 7, KnowledgeGenerationID: "gen", CanonicalWriteEpoch: 2},
	}
	blockA := 0
	mints, verified := map[int64]int{}, map[int64]int{}
	app := githubAppTestClient(t, func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/app/installations/9/access_tokens":
			mints[9]++
			if blockA != 0 {
				return githubAppTestResponse(blockA, `{}`), nil
			}
			return githubAppTestResponse(201, `{"token":"test-a","expires_at":"2026-11-25T01:00:00Z"}`), nil
		case "/app/installations/10/access_tokens":
			mints[10]++
			return githubAppTestResponse(201, `{"token":"test-b","expires_at":"2026-11-25T01:00:00Z"}`), nil
		case "/repositories/33":
			verified[33]++
			if r.Header.Get("Authorization") != "Bearer test-a" {
				t.Fatal("wrong scoped token")
			}
			return githubAppTestResponse(200, githubAppTestRepo), nil
		case "/repositories/34":
			verified[34]++
			if r.Header.Get("Authorization") != "Bearer test-b" {
				t.Fatal("other Project token escaped its scope")
			}
			return githubAppTestResponse(200, `{"id":34,"node_id":"R_34","full_name":"other/repo","default_branch":"main","private":true,"owner":{"id":19}}`), nil
		default:
			t.Fatalf("unexpected GitHub response requested: %s", r.URL.Path)
			return nil, ErrUnavailable
		}
	})
	store := &MongoStore{githubApp: app, prototypeHot: newPrototypeHotCache(), prototypeFileCache: newPrototypeFileCache()}
	store.prototypeIdentityReader = func(_ context.Context, companyID, projectID string) (Project, error) {
		p, ok := projects[projectID]
		if !ok || p.CompanyID != companyID {
			return Project{}, ErrNotFound
		}
		return p, nil
	}
	store.githubVisualSnapshotReader = func(ctx context.Context, companyID, projectID string) (githubVisualSnapshot, error) {
		p, err := store.prototypeIdentityReader(ctx, companyID, projectID)
		if err != nil {
			return githubVisualSnapshot{}, err
		}
		id, account, install := int64(33), int64(18), int64(9)
		if projectID == "b" {
			id, account, install = 34, 19, 10
		}
		return githubVisualSnapshot{repositoryBindingFromProject(p), githubRepositoryConnection{ProjectID: projectID, GitHubRepositoryID: id, RepositoryNodeID: "R_" + strconv.FormatInt(id, 10), AccountID: account, InstallationID: install, Revision: 1}}, nil
	}
	for _, p := range projects {
		catalog := identityTestCatalog(p.CompanyID, p.ID, p.Binding.RepositoryID, revision, p.BindingRevision, p.CanonicalWriteEpoch, p.KnowledgeGenerationID, path)
		store.prototypeHot.replace(p.CompanyID, p.ID, &catalog)
		store.prototypeFileCache.put(prototypeFileCacheKey(p.CompanyID, p.ID, catalog, path), prototypeFileResponse{Data: []byte(p.ID), ContentType: "text/html", ETag: etag, Revision: revision, InventoryDigest: catalog.InventoryDigest})
	}
	mux := NewHandler(store)
	request := func(method, company, project, ifNone string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/internal/v1/companies/"+company+"/projects/"+project+"/prototypes/"+revision+"/files/"+path, nil)
		if ifNone != "" {
			r.Header.Set("If-None-Match", ifNone)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	if w := request(http.MethodGet, "team-a", "a", ""); w.Code != 200 || w.Body.String() != "a" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("warm authorized response = %d %q", w.Code, w.Body.String())
	}
	if w := request(http.MethodHead, "team-a", "a", etag); w.Code != 304 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("conditional authorized response = %d", w.Code)
	}
	blockA = http.StatusUnprocessableEntity
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		w := request(method, "team-a", "a", etag)
		if w.Code != http.StatusConflict || w.Header().Get("ETag") != "" || !strings.Contains(w.Body.String(), "github_visual_access_denied") {
			t.Fatalf("cached %s escaped revoked access: %d %s", method, w.Code, w.Body.String())
		}
	}
	blockA = http.StatusServiceUnavailable
	if w := request(http.MethodGet, "team-a", "a", etag); w.Code != 503 || !strings.Contains(w.Body.String(), "github_visual_provider_unavailable") {
		t.Fatalf("provider outage treated as revocation or content: %d %s", w.Code, w.Body.String())
	}
	if w := request(http.MethodGet, "team-b", "b", ""); w.Code != 200 || w.Body.String() != "b" {
		t.Fatalf("other Project was affected: %d %q", w.Code, w.Body.String())
	}
	if w := request(http.MethodGet, "team-a", "b", ""); w.Code != 404 {
		t.Fatalf("cross-Company visual read returned %d", w.Code)
	}
	if mints[9] != 5 || mints[10] != 1 || verified[33] != 2 || verified[34] != 1 {
		t.Fatalf("each read must authorize exactly once: mints %v verification %v", mints, verified)
	}
	blockA = http.StatusForbidden
	for _, route := range []string{
		"/design-system", "/design-system/" + revision, "/design-system/history",
		"/design-system/history/" + revision, "/design-system/validate/" + revision,
		"/design-system/" + revision + "/assets/logo.png",
		"/prototypes", "/prototypes/" + revision + "/demo",
		"/landing", "/landing/3/" + revision + "/files/index.html",
		"/portfolio/3/" + revision + "/" + strings.Repeat("b", 64) + "/files/project_portfolio.json",
	} {
		t.Run(route, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/internal/v1/companies/team-a/projects/a"+route, nil)
			r.Header.Set("If-None-Match", etag)
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, r)
			if w.Code != http.StatusConflict || w.Header().Get("Cache-Control") != "no-store" || !strings.Contains(w.Body.String(), "github_visual_access_denied") {
				t.Fatalf("revoked route bypassed authorization: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestGitHubVisualFenceDiscardsOutputAfterConnectionChanges(t *testing.T) {
	app := githubAppTestClient(t, func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodPost {
			return githubAppTestResponse(201, `{"token":"read-token","expires_at":"2026-11-25T01:00:00Z"}`), nil
		}
		return githubAppTestResponse(200, githubAppTestRepo), nil
	})
	binding := RepositoryBinding{RepositoryID: "legacy-alias/repo", State: "configured", Revision: 1, PublicationBranch: "main"}
	conn := githubRepositoryConnection{ProjectID: "project", GitHubRepositoryID: 33, RepositoryNodeID: "R_33", InstallationID: 9, AccountID: 18, Revision: 1}
	store := &MongoStore{githubApp: app, githubVisualSnapshotReader: func(context.Context, string, string) (githubVisualSnapshot, error) {
		return githubVisualSnapshot{binding, conn}, nil
	}}
	ctx := context.Background()
	value, err := readGitHubVisual(ctx, store, "company", "project", func(scoped context.Context) (string, error) {
		lease, ok := githubVisualLeaseFrom(scoped)
		if !ok || lease.remote != "https://github.com/company/repo.git" || lease.snapshot.binding.RepositoryID != "legacy-alias/repo" {
			t.Fatal("lost verified canonical URL or configured Builder alias")
		}
		conn.Revision++
		return "cached asset", nil
	})
	if value != "" || !errors.Is(err, ErrConflict) {
		t.Fatalf("cached content escaped changed connection: %q %v", value, err)
	}
}

func TestGitHubVisualScopedGitEnvironmentAvoidsAmbientCredentials(t *testing.T) {
	t.Setenv("DELPHI_GITHUB_READ_TOKEN", "legacy-secret")
	t.Setenv("BUILDER_GITHUB_APP_CLIENT_SECRET", "oauth-secret")
	t.Setenv("GIT_CONFIG_VALUE_0", "inherited-token")
	lease := githubVisualLease{remote: "https://github.com/company/repo.git", token: githubAppToken{value: "scoped-test-token", expiresAt: time.Now().Add(time.Hour)}}
	withToken := strings.Join(githubVisualGitEnvironment(lease, true), "\n")
	withoutToken := strings.Join(githubVisualGitEnvironment(lease, false), "\n")
	for _, value := range []string{"legacy-secret", "oauth-secret", "inherited-token"} {
		if strings.Contains(withToken, value) || strings.Contains(withoutToken, value) {
			t.Fatalf("passed unsafe ambient Git environment value")
		}
	}
	if !strings.Contains(withToken, "http."+lease.remote+".extraHeader") || !strings.Contains(withToken, "http.followRedirects\nGIT_CONFIG_VALUE_1=false") || strings.Contains(withoutToken, "extraHeader") {
		t.Fatal("credential was not limited to the authorized remote and credentialed Git operation")
	}
}
