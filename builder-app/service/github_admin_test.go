package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitHubAdminPageAndOperationsUseRegistryAuthorization(t *testing.T) {
	const host = "builder.example"
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get(githubAdminAssertion) != "signed-assertion" || r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
			t.Errorf("incorrect headers forwarded: %v", r.Header)
		}
		const path = "/internal/v1/companies/alpha/projects/site/github-connections"
		if r.Method == http.MethodGet && r.URL.Path == path {
			_, _ = io.WriteString(w, "{}")
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != path+"/operations" || r.Header.Get("Origin") != "https://"+host || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected upstream request: %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusBadRequest)
		}
		_, _ = io.WriteString(w, "{}")
	}))
	defer upstream.Close()

	t.Setenv("DELPHI_BUILDER_REGISTRY_URL", upstream.URL)
	t.Setenv("BUILDER_GITHUB_ADMIN_ORIGIN", "https://"+host)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("authenticated page"), 0600); err != nil {
		t.Fatal(err)
	}
	handler := registeredHandler("", root, host)
	request := func(method, path, assertion, origin string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "https://"+host+path, strings.NewReader(""))
		req.Header.Set("Cookie", "dont-forward=true")
		req.Header.Set("Authorization", "Bearer dont-forward")
		if assertion != "" {
			req.Header.Set(githubAdminAssertion, assertion)
		}
		if method == http.MethodPost {
			req.Header.Set("Origin", origin)
			req.Header.Set("Content-Type", "application/json")
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		return res
	}
	const page = "/admin/github?company_id=alpha&project_id=site"
	if res := request(http.MethodGet, page, "", ""); res.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous HTML status = %d", res.Code)
	}
	if res := request(http.MethodGet, page, "signed-assertion", ""); res.Code != http.StatusOK || !strings.Contains(res.Body.String(), "authenticated page") {
		t.Fatalf("authenticated page: %d %q", res.Code, res.Body.String())
	}
	const start = "/admin/github/companies/alpha/projects/site/operations"
	if res := request(http.MethodPost, start, "signed-assertion", "https://another.example"); res.Code != http.StatusForbidden {
		t.Fatalf("cross-origin POST status = %d", res.Code)
	}
	if res := request(http.MethodPost, start, "signed-assertion", "https://"+host); res.Code != http.StatusOK {
		t.Fatalf("same-origin POST status = %d, body %q", res.Code, res.Body.String())
	}
	if calls != 2 {
		t.Fatalf("expected exactly two authorized requests upstream, got %d", calls)
	}
}

func TestGitHubAdminCallbackRejectsExternalRedirectAndCleansSecrets(t *testing.T) {
	const host = "builder.example"
	result := `{"company_id":"alpha","project_id":"site","operation_id":"github-1","authorization_url":"https://github.com.evil.example/login/oauth/authorize?state=secret"}`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/v1/admin/github/oauth/callback" || r.URL.Query().Get("code") != "callback-secret" {
			t.Errorf("unexpected upstream callback: %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, result)
	}))
	defer upstream.Close()
	t.Setenv("DELPHI_BUILDER_REGISTRY_URL", upstream.URL)
	t.Setenv("BUILDER_GITHUB_ADMIN_ORIGIN", "https://"+host)
	handler := registeredHandler("", t.TempDir(), host)
	call := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "https://"+host+"/admin/github/oauth/callback?state=locator&code=callback-secret", nil)
		req.Header.Set(githubAdminAssertion, "signed-assertion")
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		return res
	}
	if denied := call(); denied.Code != http.StatusBadGateway || denied.Header().Get("Location") != "" || strings.Contains(denied.Body.String(), "secret") {
		t.Fatalf("unsafe redirect was not suppressed: %d", denied.Code)
	}
	result = `{"company_id":"alpha","project_id":"site","operation_id":"github-1"}`
	res := call()
	if res.Code != http.StatusSeeOther || res.Header().Get("Location") != "/admin/github?company_id=alpha&operation_id=github-1&project_id=site" {
		t.Fatalf("wrong callback redirect: %d %q", res.Code, res.Header().Get("Location"))
	}
}

func TestGitHubAdminSetupRequestShowsPendingApproval(t *testing.T) {
	const host = "builder.example"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/v1/admin/github/callback" || r.URL.Query().Get("setup_action") != "request" || r.URL.Query().Has("installation_id") || r.URL.Query().Get("state") != "private-locator" {
			t.Errorf("unexpected callback request %s", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"company_id":"alpha","operation_id":"github-1","state":"awaiting_organization_approval"}`)
	}))
	defer upstream.Close()
	t.Setenv("DELPHI_BUILDER_REGISTRY_URL", upstream.URL)
	t.Setenv("BUILDER_GITHUB_ADMIN_ORIGIN", "https://"+host)
	handler := registeredHandler("", t.TempDir(), host)
	get := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "https://"+host+path, nil)
		req.Header.Set(githubAdminAssertion, "signed-assertion")
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, req)
		return out
	}
	response := get("/admin/github/callback?setup_action=request&state=private-locator")
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/admin/github?company_id=alpha&new_project=1&operation_id=github-1" {
		t.Fatalf("unexpected pending redirect: %d %q", response.Code, response.Header().Get("Location"))
	}
	for _, path := range []string{"/admin/github/callback?setup_action=request", "/admin/github/callback?setup_action=install&state=locator", "/admin/github/oauth/callback?setup_action=request&state=locator"} {
		if got := get(path); got.Code != http.StatusBadRequest {
			t.Fatalf("incomplete callback accepted: %s returned %d", path, got.Code)
		}
	}
}
