package registry

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

const (
	githubAccessTestIssuer     = "https://fixture.cloudflareaccess.com"
	githubAccessTestOrigin     = "https://builder.example.test"
	githubAccessTestAudience   = "builder-admin-audience"
	githubAccessTestSubject    = "operator-subject"
	githubAccessTestOperations = "/internal/v1/companies/team/projects/project/github-connections/operations"
)

func githubAccessTestClaims(now time.Time) map[string]any {
	return map[string]any{"iss": githubAccessTestIssuer, "sub": githubAccessTestSubject, "aud": []string{githubAccessTestAudience}, "exp": now.Add(time.Minute).Unix(), "nbf": now.Add(-time.Minute).Unix()}
}

func githubAccessTestAssertion(t *testing.T, key *rsa.PrivateKey, kID, alg string, claims map[string]any) string {
	t.Helper()
	encode := func(value any) string {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	data := encode(map[string]string{"alg": alg, "kid": kID}) + "." + encode(claims)
	hash := sha256.Sum256([]byte(data))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, hash[:])
	if err != nil {
		t.Fatal(err)
	}
	return data + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func githubAccessTestKeys(t *testing.T, key *rsa.PublicKey) string {
	t.Helper()
	keys, err := json.Marshal(map[string]any{"keys": []map[string]string{{
		"kty": "RSA", "alg": "RS256", "use": "sig", "kid": "key",
		"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	return string(keys)
}

func TestGitHubAccessVerifiesSignedClaimsAndPinsKeyOrigin(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Truncate(time.Second)
	calls := 0
	verifier, err := newGitHubAccessVerifier(githubAccessTestIssuer, githubAccessTestAudience, `{"operator-subject":["team"]}`, &http.Client{Transport: githubAppTestTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.String() != githubAccessTestIssuer+"/cdn-cgi/access/certs" || r.Method != http.MethodGet || r.Header.Get("Authorization") != "" {
			t.Fatalf("unexpected trusted key request: %s", r.URL)
		}
		return githubAppTestResponse(http.StatusOK, githubAccessTestKeys(t, &key.PublicKey)), nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	verifier.now = func() time.Time { return now }
	ctx := context.Background()
	token := githubAccessTestAssertion(t, key, "key", "RS256", githubAccessTestClaims(now))
	op, err := verifier.verify(ctx, token)
	if err != nil || op.Subject != githubAccessTestSubject || !verifier.permits(op, "team") || verifier.permits(op, "other") {
		t.Fatal("valid assertion or exact Company scope rejected")
	}
	for name, replace := range map[string]map[string]any{
		"issuer":          {"iss": "https://wrong.cloudflareaccess.com"},
		"audience":        {"aud": []string{"wrong"}},
		"expiry":          {"exp": now.Unix()},
		"not yet valid":   {"nbf": now.Add(time.Second).Unix()},
		"missing subject": {"sub": ""},
	} {
		t.Run(name, func(t *testing.T) {
			claims := githubAccessTestClaims(now)
			for field, value := range replace {
				claims[field] = value
			}
			if _, err := verifier.verify(ctx, githubAccessTestAssertion(t, key, "key", "RS256", claims)); err == nil {
				t.Fatal("invalid signed assertion accepted")
			}
		})
	}
	for name, token := range map[string]string{
		"wrong signature": githubAccessTestAssertion(t, other, "key", "RS256", githubAccessTestClaims(now)),
		"wrong algorithm": githubAccessTestAssertion(t, key, "key", "HS256", githubAccessTestClaims(now)),
		"unknown kid":     githubAccessTestAssertion(t, key, "unknown", "RS256", githubAccessTestClaims(now)),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := verifier.verify(ctx, token); err == nil {
				t.Fatal("invalid assertion accepted")
			}
		})
	}
	if calls != 1 {
		t.Fatalf("unexpected number of trusted key fetches: %d", calls)
	}
}

type githubAdminHTTPSpy struct {
	githubAdminService
	starts    int
	callbacks int
}

func (s *githubAdminHTTPSpy) start(_ context.Context, company, project string, operator githubOperator, input githubConnectionStartRequest) (githubConnectionStatus, error) {
	s.starts++
	return githubConnectionStatus{CompanyID: company, ProjectID: project, OperationID: input.OperationID, State: githubConnectionPending}, nil
}
func (s *githubAdminHTTPSpy) setupCallback(_ context.Context, _ githubOperator, _ string, _ int64) (githubConnectionStatus, error) {
	s.callbacks++
	return githubConnectionStatus{State: githubConnectionPending}, nil
}

func TestGitHubAdminHTTPGuardsMutationsAndCallbackCompany(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Truncate(time.Second)
	verifier := &githubAccessVerifier{
		issuer:    githubAccessTestIssuer,
		audience:  githubAccessTestAudience,
		now:       func() time.Time { return now },
		operators: map[string]map[string]bool{githubAccessTestSubject: {"team": true}},
		keys:      map[string]*rsa.PublicKey{"key": &key.PublicKey},
		fetched:   now,
	}
	token := githubAccessTestAssertion(t, key, "key", "RS256", githubAccessTestClaims(now))
	spy := &githubAdminHTTPSpy{}
	mux := http.NewServeMux()
	(&githubAdminHTTP{admin: spy, access: verifier, origin: githubAccessTestOrigin}).register(mux)
	send := func(method, path, assertion, origin, contentType, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if assertion != "" {
			r.Header.Set(githubAccessAssertionHeader, assertion)
		}
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if contentType != "" {
			r.Header.Set("Content-Type", contentType)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	const valid = `{"operation_id":"start","role":"visual","action":"connect","expected_role_revision":0,"expected_connection_revision":0,"target_account_id":8,"publication_branch":"main"}`
	for _, tc := range []struct {
		name, path, assertion, origin, contentType, body string
		code                                             int
	}{
		{"unauthenticated", githubAccessTestOperations, "", githubAccessTestOrigin, "application/json", valid, 401},
		{"wrong origin", githubAccessTestOperations, token, "https://attacker.test", "application/json", valid, 403},
		{"wrong company", strings.Replace(githubAccessTestOperations, "/team/", "/other/", 1), token, githubAccessTestOrigin, "application/json", valid, 403},
		{"wrong content type", githubAccessTestOperations, token, githubAccessTestOrigin, "text/plain", valid, 415},
		{"bounded body", githubAccessTestOperations, token, githubAccessTestOrigin, "application/json", strings.Repeat(" ", 9<<10) + valid, 400},
		{"unknown fields", githubAccessTestOperations, token, githubAccessTestOrigin, "application/json", `{"operator":"forged"}`, 400},
		{"duplicate fields", githubAccessTestOperations, token, githubAccessTestOrigin, "application/json", `{"operation_id":"one","operation_id":"two"}`, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := send(http.MethodPost, tc.path, tc.assertion, tc.origin, tc.contentType, tc.body)
			if w.Code != tc.code || spy.starts != 0 || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("code=%d wants %d, mutation calls=%d", w.Code, tc.code, spy.starts)
			}
		})
	}
	w := send(http.MethodPost, githubAccessTestOperations, token, githubAccessTestOrigin, "application/json", valid)
	if w.Code != http.StatusOK || spy.starts != 1 || !strings.Contains(w.Body.String(), `"company_id":"team"`) {
		t.Fatalf("authorized start not served; code=%d calls=%d", w.Code, spy.starts)
	}
	for _, company := range []string{"other", "team"} {
		state, err := githubStateValue(company, "project", "start")
		if err != nil {
			t.Fatal(err)
		}
		callback := "/internal/v1/admin/github/callback?" + (url.Values{"state": {state}, "installation_id": {"8"}}).Encode()
		w := send(http.MethodGet, callback, token, "", "", "")
		if company == "other" && (w.Code != http.StatusForbidden || spy.callbacks != 0) {
			t.Fatal("callback crossed Company authorization")
		}
		if company == "team" && (w.Code != http.StatusOK || spy.callbacks != 1) {
			t.Fatal("authorized callback not served")
		}
	}
}

func TestGitHubAppAdminConfigDisabledOrFailsClosed(t *testing.T) {
	s := &MongoStore{}
	if err := s.ConfigureGitHubApp(GitHubAppConfig{}); err != nil || s.githubAdminHTTP != nil {
		t.Fatal("admin unexpectedly enabled by empty config")
	}
	if err := s.ConfigureGitHubApp(GitHubAppConfig{AdminOrigin: githubAccessTestOrigin}); err == nil || s.githubAdminHTTP != nil {
		t.Fatal("partial config enabled administration")
	}
}
