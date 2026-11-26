package registry

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

type githubAppTestTransport func(*http.Request) (*http.Response, error)

func (f githubAppTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func githubAppTestResponse(status int, data string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(data))}
}

func githubAppTestClient(t *testing.T, f githubAppTestTransport) *githubAppClient {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	client, err := newGitHubAppClient(42, "builder-app", "client", "server-secret", "https://preview.example.test/admin/github/oauth/callback", pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), &http.Client{Transport: f})
	if err != nil {
		t.Fatal(err)
	}
	client.now = func() time.Time { return time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC) }
	return client
}

const githubAppTestRepo = `{"id":33,"node_id":"R_33","full_name":"company/repo","default_branch":"main","private":true,"owner":{"id":18,"login":"company"}}`

func TestGitHubAppRepositoryReadTokenIsScopedAndVerifiesExactIdentity(t *testing.T) {
	var signed string
	var calls int
	client := githubAppTestClient(t, func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Scheme != "https" || r.URL.Host != "api.github.com" {
			t.Fatalf("unexpected destination: %s", r.URL)
		}
		switch r.URL.Path {
		case "/app/installations/9/access_tokens":
			signed = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			var body struct {
				RepositoryIDs []int64           `json:"repository_ids"`
				Permissions   map[string]string `json:"permissions"`
			}
			if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&body) != nil || len(body.RepositoryIDs) != 1 || body.RepositoryIDs[0] != 33 || len(body.Permissions) != 1 || body.Permissions["contents"] != "read" {
				t.Fatalf("incorrect token scope: %+v", body)
			}
			return githubAppTestResponse(201, `{"token":"test-installation","expires_at":"2026-11-25T01:00:00Z"}`), nil
		case "/repositories/33":
			if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer test-installation" {
				t.Fatal("identity check did not use emitted credential")
			}
			return githubAppTestResponse(200, githubAppTestRepo), nil
		default:
			t.Fatalf("unexpected GitHub request: %s", r.URL)
			return nil, errors.New("unexpected request")
		}
	})
	connection := githubRepositoryConnection{InstallationID: 9, GitHubRepositoryID: 33, AccountID: 18, RepositoryNodeID: "R_33"}
	token, repo, err := client.repositoryReadToken(context.Background(), connection)
	if err != nil || token.value != "test-installation" || repo.ID != 33 || calls != 2 {
		t.Fatalf("expected scoped token and identity; repo=%+v calls=%d error=%v", repo, calls, err)
	}
	parts := strings.Split(signed, ".")
	if len(parts) != 3 {
		t.Fatal("invalid signed App assertion")
	}
	claims, _ := base64.RawURLEncoding.DecodeString(parts[1])
	signature, _ := base64.RawURLEncoding.DecodeString(parts[2])
	hash := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if !strings.Contains(string(claims), `"iss":"42"`) || rsa.VerifyPKCS1v15(&client.key.PublicKey, crypto.SHA256, hash[:], signature) != nil {
		t.Fatal("invalid App JWT")
	}
	connection.RepositoryNodeID = "R_different"
	if token, _, err := client.repositoryReadToken(context.Background(), connection); !errors.Is(err, errGitHubAccessDenied) || token.value != "" {
		t.Fatal("credential escaped a mismatched immutable identity")
	}
}

func TestGitHubAppOAuthAndExactUserAuthorization(t *testing.T) {
	client := githubAppTestClient(t, func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/login/oauth/access_token":
			if r.URL.Host != "github.com" || r.Method != http.MethodPost || r.ParseForm() != nil || r.Form.Get("code_verifier") != "verifier" || r.Form.Get("code") != "code" || r.Form.Get("client_secret") != "server-secret" {
				t.Fatal("invalid OAuth exchange")
			}
			return githubAppTestResponse(200, `{"access_token":"test-user","expires_in":28800}`), nil
		case "/user":
			return githubAppTestResponse(200, `{"id":52,"login":"operator"}`), nil
		case "/user/installations":
			return githubAppTestResponse(200, `{"installations":[{"id":9,"app_id":42,"account":{"id":18,"login":"company","type":"Organization"}}]}`), nil
		case "/user/installations/9/repositories":
			return githubAppTestResponse(200, `{"repositories":[`+githubAppTestRepo+`]}`), nil
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
			return nil, errors.New("unexpected request")
		}
	})
	setup, err := url.Parse(client.setupURL("setup-state"))
	if err != nil || setup.Host != "github.com" || setup.Path != "/apps/builder-app/installations/new" || setup.Query().Get("state") != "setup-state" {
		t.Fatal("invalid setup URL")
	}
	auth, err := url.Parse(client.authorizationURL("oauth-state", "verifier"))
	hash := sha256.Sum256([]byte("verifier"))
	if err != nil || auth.Host != "github.com" || auth.Query().Get("state") != "oauth-state" || auth.Query().Get("code_challenge") != base64.RawURLEncoding.EncodeToString(hash[:]) || auth.Query().Get("code_challenge_method") != "S256" {
		t.Fatal("invalid PKCE authorization URL")
	}
	ctx := context.Background()
	token, err := client.exchangeUserCode(ctx, "code", "verifier")
	if err != nil {
		t.Fatal(err)
	}
	if user, err := client.user(ctx, token); err != nil || user.ID != 52 {
		t.Fatalf("invalid current GitHub user: %v", err)
	}
	if _, err := client.userInstallation(ctx, token, 9, 18); err != nil {
		t.Fatal(err)
	}
	if _, err := client.userInstallation(ctx, token, 9, 19); !errors.Is(err, errGitHubAccessDenied) {
		t.Fatal("wrong account authorized")
	}
	if repo, err := client.userInstallationRepository(ctx, token, 9, 18, 33); err != nil || repo.ID != 33 {
		t.Fatalf("invalid selected repository: %v", err)
	}
	if _, err := client.userInstallationRepository(ctx, token, 9, 18, 34); !errors.Is(err, errGitHubAccessDenied) {
		t.Fatal("other repository authorized")
	}
	token.expiresAt = client.now()
	if _, err := client.user(ctx, token); !errors.Is(err, errGitHubAccessDenied) {
		t.Fatal("expired user authorization accepted")
	}
}

func TestGitHubAppOrganizationCreateDistinguishesDenialFromUncertainty(t *testing.T) {
	status, body := 201, githubAppTestRepo
	var calls int
	client := githubAppTestClient(t, func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Host != "api.github.com" || r.URL.Path != "/orgs/company/repos" || r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer test-user" {
			t.Fatal("request did not use exact organization and operator token")
		}
		var fields struct {
			Name     string `json:"name"`
			Private  bool   `json:"private"`
			AutoInit bool   `json:"auto_init"`
		}
		if json.NewDecoder(r.Body).Decode(&fields) != nil || fields.Name != "repo" || !fields.Private || !fields.AutoInit {
			t.Fatalf("incorrect creation request: %+v", fields)
		}
		resp := githubAppTestResponse(status, body)
		if status == 302 {
			resp.Header.Set("Location", "https://other.example.test/sensitive")
		}
		return resp, nil
	})
	installation := githubAppInstallation{ID: 9, AppID: 42, Account: githubAppAccount{ID: 18, Login: "company", Type: "Organization"}}
	token := githubAppToken{"test-user", client.now().Add(time.Hour)}
	ctx := context.Background()
	if repo, err := client.createOrganizationRepository(ctx, token, installation, "repo", true); err != nil || repo.ID != 33 {
		t.Fatalf("valid receipt rejected: %v", err)
	}
	status = 403
	if _, err := client.createOrganizationRepository(ctx, token, installation, "repo", true); !errors.Is(err, errGitHubAccessDenied) {
		t.Fatal("definitive denial was not propagated")
	}
	status = 503
	if _, err := client.createOrganizationRepository(ctx, token, installation, "repo", true); !errors.Is(err, errGitHubCreationUncertain) {
		t.Fatal("upstream failure did not preserve uncertain effect")
	}
	status, body = 201, `{}`
	if _, err := client.createOrganizationRepository(ctx, token, installation, "repo", true); !errors.Is(err, errGitHubCreationUncertain) {
		t.Fatal("invalid creation receipt was treated as definitive")
	}
	status = 302
	if _, err := client.createOrganizationRepository(ctx, token, installation, "repo", true); !errors.Is(err, errGitHubCreationUncertain) || calls != 5 {
		t.Fatal("redirect followed or creation outcome uncertain was lost")
	}
}

func TestGitHubAppOrganizationCreateConfirmsPublicVisibility(t *testing.T) {
	const publicRepo = `{"id":33,"node_id":"R_33","full_name":"company/repo","default_branch":"trunk","owner":{"id":18,"login":"company"},"private":false}`
	body := publicRepo
	client := githubAppTestClient(t, func(r *http.Request) (*http.Response, error) {
		var fields struct {
			Private *bool `json:"private"`
		}
		if err := json.NewDecoder(r.Body).Decode(&fields); err != nil || fields.Private == nil || *fields.Private {
			t.Fatalf("public create did not send private=false: %+v, %v", fields, err)
		}
		return githubAppTestResponse(201, body), nil
	})
	installation := githubAppInstallation{ID: 9, AppID: 42, Account: githubAppAccount{ID: 18, Login: "company", Type: "Organization"}}
	token := githubAppToken{"test-user", client.now().Add(time.Hour)}
	ctx := context.Background()
	if result, err := client.createOrganizationRepository(ctx, token, installation, "repo", false); err != nil || result.DefaultBranch != "trunk" {
		t.Fatalf("public receipt rejected: %+v, %v", result, err)
	}
	for _, visibility := range []string{`,"private":true`, ""} {
		body = strings.Replace(publicRepo, `,"private":false`, visibility, 1)
		if _, err := client.createOrganizationRepository(ctx, token, installation, "repo", false); !errors.Is(err, errGitHubCreationUncertain) {
			t.Fatalf("unverified public result was accepted with receipt %s: %v", body, err)
		}
	}
}
