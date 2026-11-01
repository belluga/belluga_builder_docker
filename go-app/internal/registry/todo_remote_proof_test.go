package registry

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCommitAttributionUsesOnlyApprovedRepositoryPaths(t *testing.T) {
	scope := TODORepositoryScope{Repository: "belluga_builder_docker", ExpectedPaths: []string{"go-app/internal/registry/todo_local_proof.go", "go-app/internal/registry/*test.go"}, NotExpectedPaths: []string{"builder-app/**"}}
	for _, path := range []string{"go-app/internal/registry/todo_local_proof.go", "go-app/internal/registry/todo_remote_proof_test.go"} {
		if !approvedCommitPath(path, scope) {
			t.Fatalf("approved path rejected: %s", path)
		}
	}
	for _, path := range []string{"builder-app/web/src/projects.tsx", "go-app/internal/registry/http.go", "../secret", "/root"} {
		if approvedCommitPath(path, scope) {
			t.Fatalf("outside path admitted: %s", path)
		}
	}
	if branch, ok := implementationBranch("builder", "r-08", "belluga_builder_docker"); !ok || branch != "r-08-rc" {
		t.Fatal("Builder release branch was not selected")
	}
	if branch, ok := implementationBranch("builder", "r-08", "delphi-ai"); !ok || branch != "builder" {
		t.Fatal("PACED branch was not selected")
	}
	if _, ok := implementationBranch("boora", "v0.7.1", "laravel-app"); ok {
		t.Fatal("unknown Project branch was invented")
	}
}

func TestRemoteLocalDeliveryRequiresEveryImplementingRepository(t *testing.T) {
	sha := strings.Repeat("a", 40)
	badPACEDPath := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		changed := "go-app/internal/registry/todo_access.go"
		if q.Get("repository") == "belluga/delphi-ai" && !badPACEDPath {
			changed = "local-api/internal/project_reader/http.go"
		}
		_ = json.NewEncoder(w).Encode(remoteCommitResult{CompanyID: q.Get("company_id"), ProjectID: q.Get("project_id"), Repository: q.Get("repository"), Branch: q.Get("branch"), CommitSHA: q.Get("commit_sha"), RemoteHead: sha, ParentSHA: strings.Repeat("b", 40), Paths: []string{changed}, Outcome: "verified"})
	}))
	defer server.Close()
	store := &MongoStore{proofReaderURL: server.URL, proofReaderToken: strings.Repeat("x", 32), proofHTTPClient: server.Client()}
	digest := sourceHash([]byte("approved content"))
	todo := TODORecord{ID: "test", ProjectID: "builder", ReleaseID: "r-08", DiffExpectation: []TODORepositoryScope{
		{Repository: "belluga_builder_docker", ExpectedPaths: []string{"go-app/internal/registry/todo_access.go"}, NotExpectedPaths: []string{}},
		{Repository: "delphi-ai", ExpectedPaths: []string{"local-api/**"}, NotExpectedPaths: []string{}},
	}}
	local := TODOProofReference{Kind: "local-run-accepted", Source: "paced-local-runner", Reference: "run", SourceDigest: digest, VerificationState: "verified"}
	todo.ImplementationCommits = []TODOCommitReference{{Repository: "belluga_builder_docker", Commit: sha, SourceDigest: digest, VerificationState: "unverified"}}
	if _, err := store.verifyRemoteLocalDelivery(context.Background(), "belluga-solutions", "belluga/belluga_builder_foundation_documentation", todo, digest, local); err == nil {
		t.Fatal("missing PACED implementation was accepted")
	}
	todo.ImplementationCommits = append(todo.ImplementationCommits, TODOCommitReference{Repository: "delphi-ai", Commit: sha, SourceDigest: digest, VerificationState: "unverified"})
	if proof, err := store.verifyRemoteLocalDelivery(context.Background(), "belluga-solutions", "belluga/belluga_builder_foundation_documentation", todo, digest, local); err != nil || proof.Kind != "remote-local-delivery" {
		t.Fatalf("complete remote proof=%+v err=%v", proof, err)
	}
	badPACEDPath = true
	if _, err := store.verifyRemoteLocalDelivery(context.Background(), "belluga-solutions", "belluga/belluga_builder_foundation_documentation", todo, digest, local); err == nil {
		t.Fatal("PACED commit changing a Builder path was accepted")
	}
	badPACEDPath = false
	todo.DiffExpectation[0].Repository, todo.DiffExpectation[1].Repository = "Builder", "PACED"
	todo.ImplementationCommits[0].Repository, todo.ImplementationCommits[1].Repository = "Builder", "PACED"
	if _, err := store.verifyRemoteLocalDelivery(context.Background(), "belluga-solutions", "belluga/belluga_builder_foundation_documentation", todo, digest, local); err != nil {
		t.Fatalf("legacy Builder/PACED repository labels were rejected: %v", err)
	}
}

func TestFoundationOnlyDeliveryUsesPublishedMainCommit(t *testing.T) {
	sha := strings.Repeat("a", 40)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		_ = json.NewEncoder(w).Encode(remoteCommitResult{CompanyID: q.Get("company_id"), ProjectID: q.Get("project_id"), Repository: q.Get("repository"), Branch: q.Get("branch"), CommitSHA: q.Get("commit_sha"), RemoteHead: sha, ParentSHA: strings.Repeat("b", 40), Paths: []string{"modules/promotion_lane_module.md"}, Outcome: "verified"})
	}))
	defer server.Close()
	store := &MongoStore{proofReaderURL: server.URL, proofReaderToken: strings.Repeat("x", 32), proofHTTPClient: server.Client()}
	digest := sourceHash([]byte("document contract"))
	todo := TODORecord{ID: "foundation-only", ProjectID: "builder", ReleaseID: "r-08", DiffExpectation: []TODORepositoryScope{{Repository: "belluga_builder_foundation_documentation", ExpectedPaths: []string{"modules/promotion_lane_module.md"}, NotExpectedPaths: []string{}}}}
	todo.ImplementationCommits = []TODOCommitReference{{Repository: "belluga_builder_foundation_documentation", Commit: sha, SourceDigest: digest, VerificationState: "unverified"}}
	proof, err := store.verifyRemoteLocalDelivery(context.Background(), "belluga-solutions", "belluga/belluga_builder_foundation_documentation", todo, digest, TODOProofReference{})
	if err != nil || proof.Kind != "foundation-publication" || !strings.Contains(proof.Reference, "@main:") {
		t.Fatalf("Foundation proof=%+v err=%v", proof, err)
	}
	todo.ImplementationCommits = nil
	if _, err := store.verifyRemoteLocalDelivery(context.Background(), "belluga-solutions", "belluga/belluga_builder_foundation_documentation", todo, digest, TODOProofReference{}); err == nil {
		t.Fatal("missing Foundation commit accepted")
	}
}
