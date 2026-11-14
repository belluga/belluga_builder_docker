package registry

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
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
}

func TestTODOBranchReadbackKeepsLegacyOrUnconfirmedScopesReadable(t *testing.T) {
	for _, record := range []TODORecord{
		{ProjectID: "builder", Stage: "Local-Implemented", StageVerification: "verified", DiffExpectation: []TODORepositoryScope{{Repository: "Builder", DeliveryBranch: "r-12-rc"}}},
		{ProjectID: "builder", Stage: "Planning", DiffExpectation: []TODORepositoryScope{{Repository: "Builder"}}},
		{ProjectID: "builder", Stage: "Planning", Content: &TODOContent{SchemaVersion: todoContentSchemaVersion, Delivery: &TODODeliveryContract{Kind: "knowledge-only"}}},
	} {
		branches, err := selectedTODOReleaseBranches(context.Background(), nil, record)
		if err != nil || len(branches) != 0 {
			t.Fatalf("readback blocked a legacy, branchless, or knowledge-only TODO: %+v, %v", branches, err)
		}
	}
}

func TestCommitPathDoubleStarMatchesRootAndNestedFiles(t *testing.T) {
	pattern := "builder-app/web/src/**/*.test.tsx"
	for _, changed := range []string{
		"builder-app/web/src/projects.test.tsx",
		"builder-app/web/src/components/projects.test.tsx",
		"builder-app/web/src/components/nested/projects.test.tsx",
	} {
		if !matchesCommitPath(pattern, changed) {
			t.Fatalf("approved recursive test path rejected: %s", changed)
		}
	}
	for _, changed := range []string{
		"builder-app/web/src/projects.tsx",
		"builder-app/web/src/projects.test.ts",
		"builder-app/web/src-other/projects.test.tsx",
	} {
		if matchesCommitPath(pattern, changed) {
			t.Fatalf("outside recursive test path accepted: %s", changed)
		}
	}
	if matchesCommitPath("go-app/vendor/**", "go-app/vendor") || !matchesCommitPath("go-app/vendor/**", "go-app/vendor/module/file.go") {
		t.Fatal("directory subtree scope changed")
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
		_ = json.NewEncoder(w).Encode(remoteCommitResult{CompanyID: q.Get("company_id"), ProjectID: q.Get("project_id"), Repository: q.Get("repository"), Branch: q.Get("branch"), CommitSHA: q.Get("commit_sha"), RemoteHead: sha, ParentSHA: strings.Repeat("b", 40), Paths: []string{changed}, RepositoryNodeID: "R_kgDOImmutable", Outcome: "verified"})
	}))
	defer server.Close()
	store := &MongoStore{proofReaderURL: server.URL, proofReaderToken: strings.Repeat("x", 32), proofHTTPClient: server.Client()}
	digest := sourceHash([]byte("approved content"))
	todo := TODORecord{ID: "test", ProjectID: "builder", ReleaseID: "r-08", DiffExpectation: []TODORepositoryScope{
		{Repository: "belluga_builder_docker", RepositoryID: "belluga/belluga_builder_docker", ExpectedPaths: []string{"go-app/internal/registry/todo_access.go"}, NotExpectedPaths: []string{}},
		{Repository: "delphi-ai", RepositoryID: "belluga/delphi-ai", ExpectedPaths: []string{"local-api/**"}, NotExpectedPaths: []string{}},
	}}
	branches := map[string]string{"belluga/belluga_builder_docker": "r-08-rc", "belluga/delphi-ai": "builder"}
	local := TODOProofReference{Kind: "local-run-verified", Source: "paced-local-runner", Reference: "run", SourceDigest: digest, VerificationState: "verified"}
	todo.ImplementationCommits = []TODOCommitReference{{Repository: "belluga_builder_docker", Commit: sha, SourceDigest: digest, VerificationState: "unverified"}}
	if _, err := store.verifyRemoteLocalDeliveryWithBranchPlan(context.Background(), "belluga-solutions", "belluga/belluga_builder_foundation_documentation", todo, digest, local, branches); err == nil {
		t.Fatal("missing PACED implementation was accepted")
	}
	todo.ImplementationCommits = append(todo.ImplementationCommits, TODOCommitReference{Repository: "delphi-ai", Commit: sha, SourceDigest: digest, VerificationState: "unverified"})
	if proof, err := store.verifyRemoteLocalDeliveryWithBranchPlan(context.Background(), "belluga-solutions", "belluga/belluga_builder_foundation_documentation", todo, digest, local, branches); err != nil || proof.Kind != "remote-local-delivery" {
		t.Fatalf("complete remote proof=%+v err=%v", proof, err)
	}
	badPACEDPath = true
	if _, err := store.verifyRemoteLocalDeliveryWithBranchPlan(context.Background(), "belluga-solutions", "belluga/belluga_builder_foundation_documentation", todo, digest, local, branches); err == nil {
		t.Fatal("PACED commit changing a Builder path was accepted")
	}
	badPACEDPath = false
	todo.DiffExpectation[0].Repository, todo.DiffExpectation[1].Repository = "Builder", "PACED"
	todo.ImplementationCommits[0].Repository, todo.ImplementationCommits[1].Repository = "Builder", "PACED"
	if _, err := store.verifyRemoteLocalDeliveryWithBranchPlan(context.Background(), "belluga-solutions", "belluga/belluga_builder_foundation_documentation", todo, digest, local, branches); err != nil {
		t.Fatalf("legacy Builder/PACED repository labels were rejected: %v", err)
	}
}

func TestRemoteLocalDeliveryUsesExplicitCodeOriginWithoutFoundation(t *testing.T) {
	sha := strings.Repeat("a", 40)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("repository") != "belluga/belluga_builder_docker" || q.Get("branch") != "r-10-rc" {
			t.Errorf("proof target = %s @ %s", q.Get("repository"), q.Get("branch"))
		}
		_ = json.NewEncoder(w).Encode(remoteCommitResult{CompanyID: q.Get("company_id"), ProjectID: q.Get("project_id"), Repository: q.Get("repository"), Branch: q.Get("branch"), CommitSHA: q.Get("commit_sha"), RemoteHead: sha, ParentSHA: strings.Repeat("b", 40), Paths: []string{"go-app/internal/registry/todo_access.go"}, RepositoryNodeID: "R_kgDOImmutable", Outcome: "verified"})
	}))
	defer server.Close()
	store := &MongoStore{proofReaderURL: server.URL, proofReaderToken: strings.Repeat("x", 32), proofHTTPClient: server.Client()}
	digest := sourceHash([]byte("approved Mongo content"))
	todo := TODORecord{ID: "mongo-only", ProjectID: "builder", ReleaseID: "r-10", Source: TODOSource{Kind: "mongo-content-revision"}, DiffExpectation: []TODORepositoryScope{{Repository: "Builder", RepositoryID: "belluga/belluga_builder_docker", DeliveryBranch: "wrong-legacy-branch", ExpectedPaths: []string{"go-app/internal/registry/todo_access.go"}, NotExpectedPaths: []string{}}}, ImplementationCommits: []TODOCommitReference{{Repository: "Builder", Commit: sha, SourceDigest: digest, VerificationState: "unverified"}}}
	local := TODOProofReference{Kind: "local-run-verified", Source: "paced-local-runner", Reference: "run", SourceDigest: digest, VerificationState: "verified"}
	if proof, err := store.verifyRemoteLocalDeliveryWithBranchPlan(context.Background(), "belluga-solutions", "", todo, digest, local, map[string]string{"belluga/belluga_builder_docker": "r-10-rc"}); err != nil || proof.Kind != "remote-local-delivery" {
		t.Fatalf("Foundation-free proof = %+v, %v", proof, err)
	}
}

func TestRemoteCodeDeliveryMintsOnlyReaderVerifiedCodeFacts(t *testing.T) {
	baseline, commit, head := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("todo_authority") != "mongo-v2" || q.Get("tested_head_sha") != "" || q.Get("baseline_sha") != baseline {
			t.Errorf("new proof passed wrong Reader bounds: %s", r.URL.String())
		}
		_ = json.NewEncoder(w).Encode(remoteCommitResult{CompanyID: q.Get("company_id"), ProjectID: q.Get("project_id"), Repository: q.Get("repository"), Branch: q.Get("branch"), CommitSHA: q.Get("commit_sha"), BaselineSHA: q.Get("baseline_sha"), RemoteHead: head, ParentSHA: baseline, Paths: []string{"go-app/internal/registry/todo_access.go"}, RepositoryNodeID: "R_kgDOImmutable", Outcome: "verified"})
	}))
	defer server.Close()
	store := &MongoStore{proofReaderURL: server.URL, proofReaderToken: strings.Repeat("x", 32), proofHTTPClient: server.Client()}
	digest := sourceHash([]byte("approved code only"))
	todo := TODORecord{ID: "code-only", ProjectID: "builder", ReleaseID: "r-12", Source: TODOSource{Kind: "mongo-content-revision"}, Content: &TODOContent{SchemaVersion: todoContentSchemaVersion}, DiffExpectation: []TODORepositoryScope{{Repository: "Builder", RepositoryID: "belluga/belluga_builder_docker", Baseline: baseline, ExpectedPaths: []string{"go-app/internal/registry/**"}}}, ImplementationCommits: []TODOCommitReference{{Repository: "Builder", Commit: commit, SourceDigest: digest, VerificationState: "unverified"}}}
	proof, err := store.verifyRemoteLocalDeliveryWithBranchPlan(context.Background(), "company-a", "", todo, digest, TODOProofReference{}, map[string]string{"belluga/belluga_builder_docker": "r-12-rc"})
	if err != nil || proof.Kind != "remote-code-delivery" || proof.Source != "private-reader" || proof.VerificationState != "verified" || strings.Contains(proof.Reference, "run=") || !strings.Contains(proof.Reference, "belluga/belluga_builder_docker@r-12-rc:"+commit) {
		t.Fatalf("remote code proof=%+v err=%v", proof, err)
	}
}

func TestRemoteCodeDeliveryBoundsReaderRequestsBeforeDispatch(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Error(w, "unexpected Reader call", http.StatusInternalServerError)
	}))
	defer server.Close()
	store := &MongoStore{proofReaderURL: server.URL, proofReaderToken: strings.Repeat("x", 32), proofHTTPClient: server.Client()}
	digest := sourceHash([]byte("approved code only"))
	commits := make([]TODOCommitReference, maxLocalDeliveryCommits+1)
	for i := range commits {
		commits[i] = TODOCommitReference{Repository: "Builder", Commit: strings.Repeat("a", 40), SourceDigest: digest, VerificationState: "unverified"}
	}
	todo := TODORecord{ID: "code-only", ProjectID: "builder", Source: TODOSource{Kind: "mongo-content-revision"}, Content: &TODOContent{SchemaVersion: todoContentSchemaVersion}, ImplementationCommits: commits}
	_, err := store.verifyRemoteLocalDeliveryWithBranchPlan(context.Background(), "company-a", "", todo, digest, TODOProofReference{}, nil)
	if err == nil || !strings.Contains(err.Error(), "implementation_commits") || calls != 0 {
		t.Fatalf("oversized delivery reached Reader: calls=%d err=%v", calls, err)
	}
}

func TestRemoteLocalDeliveryClaimsMustBeAtOrBeforeTheTestedHead(t *testing.T) {
	older, testedHead, later := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40)
	digest := sourceHash([]byte("approved content"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("tested_head_sha") != testedHead {
			t.Errorf("missing/wrong tested head in commit proof request: %s", r.URL)
			http.Error(w, `{"teach":"tested head missing"}`, http.StatusConflict)
			return
		}
		if q.Get("commit_sha") == later {
			http.Error(w, `{"teach":"implementation commit is later than or unrelated to the tested checkout head"}`, http.StatusConflict)
			return
		}
		_ = json.NewEncoder(w).Encode(remoteCommitResult{
			CompanyID: q.Get("company_id"), ProjectID: q.Get("project_id"), Repository: q.Get("repository"),
			Branch: q.Get("branch"), CommitSHA: q.Get("commit_sha"), TestedHeadSHA: q.Get("tested_head_sha"),
			RemoteHead: later, ParentSHA: strings.Repeat("d", 40), Paths: []string{"go-app/internal/registry/todo_access.go"}, RepositoryNodeID: "R_kgDOImmutable", Outcome: "verified",
		})
	}))
	defer server.Close()
	store := &MongoStore{proofReaderURL: server.URL, proofReaderToken: strings.Repeat("x", 32), proofHTTPClient: server.Client()}
	todo := TODORecord{
		ID: "tested-head-binding", ProjectID: "builder", ReleaseID: "r-08",
		Source:          TODOSource{Kind: "mongo-content-revision"},
		Content:         &TODOContent{SchemaVersion: todoContentSchemaVersion},
		DiffExpectation: []TODORepositoryScope{{Repository: "Builder", RepositoryID: "belluga/belluga_builder_docker", ExpectedPaths: []string{"go-app/internal/registry/**"}}},
	}
	local := TODOProofReference{Kind: "local-run-verified", Source: "paced-local-runner", Reference: "run", SourceDigest: digest, VerificationState: "verified"}
	branches := map[string]string{"belluga/belluga_builder_docker": "r-08-rc"}
	testedHeads := map[string]string{"belluga/belluga_builder_docker": testedHead}
	for _, claim := range []string{older, testedHead} {
		todo.ImplementationCommits = []TODOCommitReference{{Repository: "Builder", Commit: claim, SourceDigest: digest, VerificationState: "unverified"}}
		if proof, err := store.verifyRemoteLocalDeliveryWithBranchPlan(context.Background(), "company-a", "", todo, digest, local, branches, testedHeads); err != nil || proof.Kind != "remote-local-delivery" {
			t.Fatalf("ancestor-or-equal claim %s rejected: %+v %v", claim, proof, err)
		}
	}
	todo.ImplementationCommits = []TODOCommitReference{{Repository: "Builder", Commit: later, SourceDigest: digest, VerificationState: "unverified"}}
	if _, err := store.verifyRemoteLocalDeliveryWithBranchPlan(context.Background(), "company-a", "", todo, digest, local, branches, testedHeads); err == nil || !strings.Contains(err.Error(), "later than or unrelated") {
		t.Fatalf("later untested claim accepted or wrong TEACH: %v", err)
	}
}

func TestRemoteCommitProofRequiresRepositorySpecificTestedHeadEcho(t *testing.T) {
	firstHead, secondHead := strings.Repeat("a", 40), strings.Repeat("b", 40)
	firstRepo, secondRepo := "belluga/first", "belluga/second"
	serve := func(t *testing.T, responseFor func(string, string) remoteCommitResult) *MongoStore {
		t.Helper()
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			_ = json.NewEncoder(w).Encode(responseFor(q.Get("repository"), q.Get("tested_head_sha")))
		}))
		t.Cleanup(server.Close)
		return &MongoStore{proofReaderURL: server.URL, proofReaderToken: strings.Repeat("x", 32), proofHTTPClient: server.Client()}
	}
	resultFor := func(repository, testedHead string) remoteCommitResult {
		return remoteCommitResult{CompanyID: "company-a", ProjectID: "project-a", Repository: repository,
			Branch: "release", CommitSHA: strings.Repeat("c", 40), TestedHeadSHA: testedHead,
			RemoteHead: strings.Repeat("d", 40), ParentSHA: strings.Repeat("e", 40),
			Paths: []string{"src/file.go"}, RepositoryNodeID: "R_kgDOImmutable", Outcome: "verified"}
	}

	validStore := serve(t, func(repository, testedHead string) remoteCommitResult {
		return resultFor(repository, testedHead)
	})
	for repository, head := range map[string]string{firstRepo: firstHead, secondRepo: secondHead} {
		if _, err := validStore.readRemoteCommit(context.Background(), "company-a", "project-a", repository,
			"release", strings.Repeat("c", 40), true, head); err != nil {
			t.Fatalf("repository-specific tested head for %s rejected: %v", repository, err)
		}
	}

	tests := []struct {
		name       string
		repository string
		requested  string
		response   func(string, string) remoteCommitResult
	}{
		{name: "omitted echo", repository: firstRepo, requested: firstHead, response: func(repository, _ string) remoteCommitResult {
			return resultFor(repository, "")
		}},
		{name: "wrong head for requested repository", repository: secondRepo, requested: secondHead, response: func(repository, _ string) remoteCommitResult {
			return resultFor(repository, firstHead)
		}},
		{name: "head from another repository", repository: secondRepo, requested: secondHead, response: func(_, _ string) remoteCommitResult {
			return resultFor(firstRepo, firstHead)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := serve(t, test.response)
			if _, err := store.readRemoteCommit(context.Background(), "company-a", "project-a", test.repository,
				"release", strings.Repeat("c", 40), true, test.requested); err == nil {
				t.Fatal("proof accepted an omitted or cross-repository tested-head echo")
			}
		})
	}
}

func TestRemoteLocalDeliveryAcceptsCompleteLargeCommitPathList(t *testing.T) {
	sha := strings.Repeat("a", 40)
	paths := make([]string, 450)
	for i := range paths {
		paths[i] = "go-app/internal/registry/large-batch/changed-" + strconv.Itoa(i) + "-" + strings.Repeat("x", 72) + ".go"
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		result := remoteCommitResult{CompanyID: q.Get("company_id"), ProjectID: q.Get("project_id"), Repository: q.Get("repository"), Branch: q.Get("branch"), CommitSHA: q.Get("commit_sha"), RemoteHead: sha, ParentSHA: strings.Repeat("b", 40), Paths: paths, RepositoryNodeID: "R_kgDOImmutable", Outcome: "verified"}
		encoded, err := json.Marshal(result)
		if err != nil || len(encoded) <= 32<<10 {
			t.Errorf("fixture must exceed former 32 KiB response limit: bytes=%d err=%v", len(encoded), err)
			return
		}
		_, _ = w.Write(encoded)
	}))
	defer server.Close()
	store := &MongoStore{proofReaderURL: server.URL, proofReaderToken: strings.Repeat("x", 32), proofHTTPClient: server.Client()}
	digest := sourceHash([]byte("approved large commit"))
	todo := TODORecord{ID: "large-commit", ProjectID: "builder", ReleaseID: "r-12", Source: TODOSource{Kind: "mongo-content-revision"}, DiffExpectation: []TODORepositoryScope{{Repository: "Builder", RepositoryID: "belluga/belluga_builder_docker", DeliveryBranch: "wrong-legacy-branch", ExpectedPaths: []string{"go-app/internal/registry/**"}}}, ImplementationCommits: []TODOCommitReference{{Repository: "Builder", Commit: sha, SourceDigest: digest, VerificationState: "unverified"}}}
	local := TODOProofReference{Kind: "local-run-verified", Source: "paced-local-runner", Reference: "run", SourceDigest: digest, VerificationState: "verified"}
	if proof, err := store.verifyRemoteLocalDeliveryWithBranchPlan(context.Background(), "belluga-solutions", "", todo, digest, local, map[string]string{"belluga/belluga_builder_docker": "r-12-rc"}); err != nil || proof.Kind != "remote-local-delivery" {
		t.Fatalf("large complete commit proof rejected: proof=%+v err=%v", proof, err)
	}
}

func TestFoundationOnlyDeliveryUsesPublishedMainCommit(t *testing.T) {
	sha := strings.Repeat("a", 40)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		_ = json.NewEncoder(w).Encode(remoteCommitResult{CompanyID: q.Get("company_id"), ProjectID: q.Get("project_id"), Repository: q.Get("repository"), Branch: q.Get("branch"), CommitSHA: q.Get("commit_sha"), RemoteHead: sha, ParentSHA: strings.Repeat("b", 40), Paths: []string{"modules/promotion_lane_module.md"}, RepositoryNodeID: "R_kgDOImmutable", Outcome: "verified"})
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
