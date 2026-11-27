package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestGitHubScopedReaderProofKeepsAliasAndFencesTwoRepositoryConnections(t *testing.T) {
	const company, project = "team", "project"
	const aliasA, aliasB = "former/alpha", "former/beta"
	const shared = "shared-reader-fixture-only-01234567890123"
	const dedicated = "scoped-reader-fixture-only-01234567890123"
	commit, head, parent := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40)
	bindings := map[string]githubProofBinding{
		aliasA: {membershipRevision: 4, connection: githubRepositoryConnection{ProjectID: project, GitHubRepositoryID: 33, RepositoryNodeID: "R_A", AccountID: 18, InstallationID: 9, Revision: 1}},
		aliasB: {membershipRevision: 4, connection: githubRepositoryConnection{ProjectID: project, GitHubRepositoryID: 34, RepositoryNodeID: "R_B", AccountID: 19, InstallationID: 10, Revision: 2}},
	}
	revoked, changed, foreignNode, redirect := false, false, false, false
	remoteCalls := 0
	app := githubAppTestClient(t, func(r *http.Request) (*http.Response, error) {
		var id int64
		var token string
		switch r.URL.Path {
		case "/app/installations/9/access_tokens":
			id, token = 33, "one-repo-a"
		case "/app/installations/10/access_tokens":
			id, token = 34, "one-repo-b"
		case "/repositories/33":
			if r.Header.Get("Authorization") != "Bearer one-repo-a" {
				t.Fatal("wrong access token on A metadata")
			}
			return githubAppTestResponse(200, `{"id":33,"node_id":"R_A","full_name":"new/alpha","owner":{"id":18}}`), nil
		case "/repositories/34":
			if r.Header.Get("Authorization") != "Bearer one-repo-b" {
				t.Fatal("wrong access token on B metadata")
			}
			return githubAppTestResponse(200, `{"id":34,"node_id":"R_B","full_name":"new/beta","owner":{"id":19}}`), nil
		default:
			t.Errorf("unexpected GitHub API path: %s", r.URL.Path)
			return nil, ErrUnavailable
		}
		var req struct {
			RepositoryIDs []int64 `json:"repository_ids"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil || len(req.RepositoryIDs) != 1 || req.RepositoryIDs[0] != id {
			t.Fatalf("GitHub installation scope mismatch: %+v", req.RepositoryIDs)
		}
		if revoked && id == 34 {
			return githubAppTestResponse(422, `{}`), nil
		}
		return githubAppTestResponse(201, fmt.Sprintf(`{"token":%q,"expires_at":"2026-11-25T01:00:00Z"}`, token)), nil
	})
	m := &MongoStore{githubApp: app, proofReaderURL: "http://reader.test", proofReaderToken: shared}
	if err := m.ConfigureScopedProofReader(shared); err == nil {
		t.Fatal("shared bearer accepted as dedicated")
	}
	if err := m.ConfigureScopedProofReader(dedicated); err != nil {
		t.Fatal(err)
	}
	m.githubProofBindingReader = func(_ context.Context, c, p, a string) (githubProofBinding, error) {
		b, ok := bindings[a]
		if !ok || c != company || p != project {
			return githubProofBinding{}, ErrNotFound
		}
		return b, nil
	}
	m.proofHTTPClient = &http.Client{Transport: githubAppTestTransport(func(r *http.Request) (*http.Response, error) {
		remoteCalls++
		if r.URL.Host != "reader.test" || r.URL.Path != "/internal/v1/proofs/commit" {
			t.Fatal("private proof credentials followed a redirect or left the owning route")
		}
		q := r.URL.Query()
		alias := q.Get("repository")
		binding := bindings[alias]
		var canonical, token string
		if alias == aliasA {
			canonical, token = "new/alpha", "one-repo-a"
		}
		if alias == aliasB {
			canonical, token = "new/beta", "one-repo-b"
		}
		node := binding.connection.RepositoryNodeID
		if canonical == "" || q.Get("github_repository") != canonical || q.Get("github_repository_node_id") != node ||
			r.Header.Get("Authorization") != "Bearer "+dedicated || r.Header.Get(githubScopedProofTokenHeader) != token ||
			strings.Contains(r.URL.RawQuery, token) || strings.Contains(r.URL.RawQuery, dedicated) {
			t.Fatal("invalid or mixed proof delegation scope")
		}
		if redirect {
			response := githubAppTestResponse(302, `{}`)
			response.Header.Set("Location", "http://other.test/sensitive")
			return response, nil
		}
		if changed {
			binding.connection.Revision++
			bindings[alias] = binding
		}
		if foreignNode {
			node = "R_different"
		}
		return githubAppTestResponse(200, fmt.Sprintf(`{"company_id":%q,"project_id":%q,"repository":%q,"repository_node_id":%q,"branch":"main","commit_sha":%q,"remote_head":%q,"parent_sha":%q,"paths":["app/main.go"],"outcome":"verified"}`, company, project, alias, node, commit, head, parent)), nil
	})}
	ctx := context.Background()
	read := func(a string) (remoteCommitResult, error) {
		return m.readRemoteCommit(ctx, company, project, a, "main", commit, true)
	}
	for _, alias := range []string{aliasA, aliasB} {
		result, err := read(alias)
		if err != nil || result.Repository != alias || result.RepositoryNodeID != bindings[alias].connection.RepositoryNodeID {
			t.Fatalf("scope not preserved in proof: %+v, %v", result, err)
		}
	}
	revoked = true
	if _, err := read(aliasB); !errors.Is(err, ErrConflict) || remoteCalls != 2 {
		t.Fatalf("revoked repository fell back to private Reader: %v, calls=%d", err, remoteCalls)
	}
	if _, err := m.readRemoteCommit(ctx, company, project, aliasA, "main", commit, false); !errors.Is(err, ErrConflict) || remoteCalls != 2 {
		t.Fatalf("legacy call reached Reader in App profile: %v, calls=%d", err, remoteCalls)
	}
	if _, err := m.readRemoteCommit(ctx, "other", project, aliasA, "main", commit, true); !errors.Is(err, ErrNotFound) || remoteCalls != 2 {
		t.Fatalf("cross-Company alias reached Reader: %v", err)
	}
	changed = true
	if _, err := read(aliasA); !errors.Is(err, ErrConflict) {
		t.Fatalf("connection revision changed without proof fence: %v", err)
	}
	changed = false
	foreignNode = true
	if _, err := read(aliasA); !errors.Is(err, ErrConflict) {
		t.Fatalf("wrong repository node was accepted: %v", err)
	}
	foreignNode = false
	redirect = true
	before := remoteCalls
	if _, err := read(aliasA); err == nil || remoteCalls != before+1 {
		t.Fatal("scoped proof Reader redirect was followed")
	}
}
