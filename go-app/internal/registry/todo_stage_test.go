package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestMVPRepositoryComesFromApprovedMongoDiffScope(t *testing.T) {
	todo := TODORecord{Source: TODOSource{Kind: "mongo-content-revision"}, DiffExpectation: []TODORepositoryScope{{Repository: "Builder", RepositoryID: "belluga/belluga_builder_docker"}}}
	if got := mvpApprovedDockerRepository(todo); got != "belluga/belluga_builder_docker" {
		t.Fatalf("approved production repository = %q", got)
	}
	todo.DiffExpectation[0].RepositoryID = ""
	if got := mvpApprovedDockerRepository(todo); got != "" {
		t.Fatalf("missing approval produced repository %q", got)
	}
	todo.Source.Kind = ""
	todo.DiffExpectation[0].RepositoryID = "belluga/belluga_builder_docker"
	if got := mvpApprovedDockerRepository(todo); got != "" {
		t.Fatalf("historical import fabricated approved Mongo repository %q", got)
	}
}

func TestTODOCompletionRequiresActiveConstitutionMode(t *testing.T) {
	for _, mode := range []string{"", "caller-selected", "complete"} {
		if err := requireTODOCompletionMode(mode); err == nil {
			t.Fatalf("mode %q allowed direct Local-Implemented to Completed", mode)
		}
	}
	if err := requireTODOCompletionMode("mvp"); err != nil {
		t.Fatalf("MVP completion behavior changed: %v", err)
	}
}

func TestCompleteLocalPinRejectsModeAndConstitutionDrift(t *testing.T) {
	digest := sourceHash([]byte("active constitution"))
	pin := todoConstitutionPin{Mode: "complete", GenerationID: "g1", RecordID: "builder", Revision: 3, ContentDigest: digest}
	proof, err := bindTODOConstitutionPin(TODOProofReference{Kind: "remote-code-delivery", Source: "private-reader", Reference: "repository_node_id=owner/repo:R_kgDOImmutable", SourceDigest: sourceHash([]byte("local proof")), VerificationState: "verified"}, pin)
	if err != nil {
		t.Fatal(err)
	}
	if err := requireTODOConstitutionPin(&proof, pin); err != nil {
		t.Fatalf("matching immutable Complete pin rejected: %v", err)
	}
	for name, drifted := range map[string]todoConstitutionPin{
		"mode":       {Mode: "mvp", GenerationID: pin.GenerationID, RecordID: pin.RecordID, Revision: pin.Revision, ContentDigest: pin.ContentDigest},
		"generation": {Mode: pin.Mode, GenerationID: "g2", RecordID: pin.RecordID, Revision: pin.Revision, ContentDigest: pin.ContentDigest},
		"revision":   {Mode: pin.Mode, GenerationID: pin.GenerationID, RecordID: pin.RecordID, Revision: pin.Revision + 1, ContentDigest: pin.ContentDigest},
		"digest":     {Mode: pin.Mode, GenerationID: pin.GenerationID, RecordID: pin.RecordID, Revision: pin.Revision, ContentDigest: sourceHash([]byte("changed constitution"))},
	} {
		t.Run(name, func(t *testing.T) {
			if err := requireTODOConstitutionPin(&proof, drifted); err == nil {
				t.Fatalf("%s drift accepted", name)
			}
		})
	}
}

func TestCompleteArrivalRequiresOriginalsForEverySelectedRepository(t *testing.T) {
	digest := sourceHash([]byte("current authored content"))
	commit, secondaryCommit := strings.Repeat("a", 40), strings.Repeat("b", 40)
	record := TODORecord{
		DiffExpectation: []TODORepositoryScope{
			{Repository: "Builder", RepositoryID: "belluga/builder"},
			{Repository: "PACED", RepositoryID: "belluga/paced"},
		},
		ImplementationCommits: []TODOCommitReference{
			{Repository: "Builder", Commit: commit, SourceDigest: digest, VerificationState: "unverified"},
			{Repository: "PACED", Commit: secondaryCommit, SourceDigest: digest, VerificationState: "unverified"},
		},
	}
	set, err := todoOriginalCommits(record, digest)
	if err != nil || len(set.Repositories) != 2 || set.Repositories["belluga/builder"][0] != commit || set.Repositories["belluga/paced"][0] != secondaryCommit {
		t.Fatalf("original commit set=%+v err=%v", set, err)
	}

	record.ImplementationCommits[0].Repository = "Unknown"
	if _, err := todoOriginalCommits(record, digest); err == nil {
		t.Fatal("commit outside the declared repository set was accepted")
	}
	record.ImplementationCommits = nil
	if _, err := todoOriginalCommits(record, digest); err == nil {
		t.Fatal("arrival without any original implementation commits was accepted")
	}
	record.ImplementationCommits = []TODOCommitReference{{Repository: "Builder", Commit: commit, SourceDigest: digest, VerificationState: "unverified"}}
	if _, err := todoOriginalCommits(record, digest); err == nil {
		t.Fatal("arrival without an original commit for every selected repository was accepted")
	}
	tooMany := make([]TODOCommitReference, 33)
	for i := range tooMany {
		tooMany[i] = TODOCommitReference{Repository: "Builder", Commit: fmt.Sprintf("%040x", i+1), SourceDigest: digest, VerificationState: "unverified"}
	}
	record.ImplementationCommits = tooMany
	if _, err := todoOriginalCommits(record, digest); err == nil {
		t.Fatal("arrival with more than 32 original commits was accepted")
	}
}

func TestCompletePromotionTargetsAllowDevAndStageForEachApprovedRepository(t *testing.T) {
	scopes := []TODORepositoryScope{{Repository: "Builder", RepositoryID: "belluga/builder"}, {Repository: "PACED", RepositoryID: "belluga/paced"}}
	for name, targets := range map[string]map[string]string{
		"dev":   {"belluga/builder": "dev", "belluga/paced": "dev"},
		"stage": {"belluga/builder": "stage", "belluga/paced": "stage"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateCompletePromotionTargets(scopes, targets); err != nil {
				t.Fatalf("valid %s targets rejected: %v", name, err)
			}
		})
	}
	for name, targets := range map[string]map[string]string{
		"missing alias":  {"belluga/builder": "stage"},
		"unknown alias":  {"belluga/builder": "dev", "belluga/other": "stage"},
		"release branch": {"belluga/builder": "release", "belluga/paced": "stage"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateCompletePromotionTargets(scopes, targets); err == nil {
				t.Fatalf("invalid target map accepted: %+v", targets)
			}
		})
	}
}

func TestMVPClaimsAllowPACEDOnlyWithoutInventingBuilderCommit(t *testing.T) {
	commit := strings.Repeat("a", 40)
	todo := TODORecord{ImplementationCommits: []TODOCommitReference{{Repository: "PACED", Commit: commit}}}
	builder, paced, foundation, err := mvpImplementationClaims(todo)
	if err != nil || len(builder) != 0 || len(foundation) != 0 || len(paced) != 1 || paced[0] != commit {
		t.Fatalf("PACED-only claims = %v %v %v, %v", builder, paced, foundation, err)
	}
}

func TestMVPUsesVerifiedLocalPACEDIdentityForOlderScope(t *testing.T) {
	first, second := strings.Repeat("a", 40), strings.Repeat("b", 40)
	proof := TODOProofReference{Kind: "remote-local-delivery", Source: "private-reader-and-paced-runner", VerificationState: "verified", Reference: "run=approved;belluga/delphi-ai@builder:" + first + "/parent=" + strings.Repeat("c", 40) + "/head=" + second + ";belluga/delphi-ai@builder:" + second + "/parent=" + first + "/head=" + second}
	todo := TODORecord{DiffExpectation: []TODORepositoryScope{{Repository: "PACED"}}, VerifiedTransitionRef: &proof, ImplementationCommits: []TODOCommitReference{{Repository: "PACED", Commit: first}, {Repository: "PACED", Commit: second}}}
	repo, branch, err := mvpPACEDRepository(todo, []string{first, second})
	if err != nil || repo != "belluga/delphi-ai" || branch != "builder" {
		t.Fatalf("verified older PACED identity = %q @ %q, %v", repo, branch, err)
	}
	todo.VerifiedTransitionRef.VerificationState = "unverified"
	if _, _, err := mvpPACEDRepository(todo, []string{first, second}); err == nil {
		t.Fatal("unverified historical identity accepted")
	}
	todo.VerifiedTransitionRef.VerificationState = "verified"
	if _, _, err := mvpPACEDRepository(todo, []string{first, strings.Repeat("d", 40)}); err == nil {
		t.Fatal("unmatched implementation commit accepted")
	}
	if _, _, err := mvpPACEDRepository(todo, nil); err == nil {
		t.Fatal("missing implementation commits accepted")
	}
	todo.DiffExpectation[0].RepositoryID = "another-owner/delphi-ai"
	if _, _, err := mvpPACEDRepository(todo, []string{first, second}); err == nil {
		t.Fatal("scope identity mismatch with previously verified proof accepted")
	}
	todo.DiffExpectation[0].RepositoryID = "belluga/delphi-ai"
	todo.DiffExpectation[0].DeliveryBranch = "wrong-legacy-branch"
	if repo, branch, err := mvpPACEDRepository(todo, []string{first, second}); err != nil || repo != "belluga/delphi-ai" || branch != "builder" {
		t.Fatalf("verified historical identity changed by stale TODO metadata: %q @ %q, %v", repo, branch, err)
	}
	ambiguous := *todo.VerifiedTransitionRef
	ambiguous.Reference += ";another-owner/delphi-ai@another-branch:" + first + "/parent=" + strings.Repeat("c", 40) + "/head=" + second
	todo.VerifiedTransitionRef = &ambiguous
	if _, _, err := mvpPACEDRepository(todo, []string{first, second}); err == nil {
		t.Fatal("ambiguous verified repository identity accepted")
	}
}

func TestMVPUsesExactNewRemoteCodeIdentity(t *testing.T) {
	commit, parent, head := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40)
	proof := TODOProofReference{Kind: "remote-code-delivery", Source: "private-reader", VerificationState: "verified", Reference: "belluga/delphi-ai@builder:" + commit + "/parent=" + parent + "/head=" + head}
	todo := TODORecord{DiffExpectation: []TODORepositoryScope{{Repository: "PACED", RepositoryID: "belluga/delphi-ai"}}, VerifiedTransitionRef: &proof, ImplementationCommits: []TODOCommitReference{{Repository: "PACED", Commit: commit}}}
	if repo, branch, err := mvpPACEDRepository(todo, []string{commit}); err != nil || repo != "belluga/delphi-ai" || branch != "builder" {
		t.Fatalf("new proof identity = %q @ %q, %v", repo, branch, err)
	}
	bad := proof
	bad.Reference = "run=forged;" + proof.Reference
	todo.VerifiedTransitionRef = &bad
	if _, _, err := mvpPACEDRepository(todo, []string{commit}); err == nil {
		t.Fatal("new remote proof accepted a run alias")
	}
	bad = proof
	bad.Source = "private-reader-and-paced-runner"
	todo.VerifiedTransitionRef = &bad
	if _, _, err := mvpPACEDRepository(todo, []string{commit}); err == nil {
		t.Fatal("forged source alias accepted")
	}
}

func TestFoundationFreeTypedMVPRejectsFoundationCommit(t *testing.T) {
	todo := TODORecord{Content: &TODOContent{SchemaVersion: todoContentSchemaVersion}, ImplementationCommits: []TODOCommitReference{{Repository: "belluga_builder_foundation_documentation", Commit: strings.Repeat("a", 40)}}}
	if _, _, _, err := mvpImplementationClaims(todo); err == nil || !strings.Contains(err.Error(), "implementation_commits.repository") {
		t.Fatalf("Foundation claim accepted or wrong TEACH: %v", err)
	}
	todo.Content = nil
	if _, _, _, err := mvpImplementationClaims(todo); err != nil {
		t.Fatalf("historical Foundation claim changed: %v", err)
	}
}

func TestMVPProductionRequiresReaderToConfirmPACEDClaims(t *testing.T) {
	commit, root, digest := strings.Repeat("a", 40), strings.Repeat("d", 40), sourceHash([]byte("contract"))
	for _, proof := range []TODOProofReference{
		{Kind: "remote-local-delivery", Source: "private-reader-and-paced-runner", Reference: "run=approved;belluga/delphi-ai@builder:" + commit + "/parent=" + strings.Repeat("b", 40) + "/head=" + strings.Repeat("c", 40), VerificationState: "verified", SourceDigest: digest},
		{Kind: "remote-code-delivery", Source: "private-reader", Reference: "belluga/delphi-ai@builder:" + commit + "/parent=" + strings.Repeat("b", 40) + "/head=" + strings.Repeat("c", 40), VerificationState: "verified", SourceDigest: digest},
	} {
		t.Run(proof.Kind, func(t *testing.T) {
			todo := TODORecord{ID: "todo-a", ProjectID: "builder", ReleaseID: "r-08", Stage: "Local-Implemented", StageVerification: "verified", VerifiedTransitionRef: &proof, Source: TODOSource{Digest: digest}, ImplementationCommits: []TODOCommitReference{{Repository: "PACED", Commit: commit}}}
			confirmed := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				response := remoteProductionResult{CompanyID: "company-a", ProjectID: "builder", TODOID: todo.ID, DeploymentID: 17, Environment: "production", RootSHA: root, ModulePath: "project", CommitSHA: root, Outcome: "verified", ProjectMode: "mvp", ReaderSHA: strings.Repeat("e", 40)}
				if confirmed {
					response.PACEDCommits = []string{commit}
				}
				_ = json.NewEncoder(w).Encode(response)
			}))
			defer server.Close()
			store := &MongoStore{proofReaderURL: server.URL, proofReaderToken: "secret", proofHTTPClient: server.Client()}
			claim := TODOProofReference{Kind: "mvp-deployment-claim", Source: "github-deployment", Reference: "17@" + root, SourceDigest: digest, VerificationState: "unverified"}
			request := TODOActionRequest{ProofReferences: []TODOProofReference{claim}}
			if _, err := store.verifyMVPProduction(context.Background(), "company-a", todo, request, ""); err == nil {
				t.Fatal("unconfirmed PACED commits accepted")
			}
			confirmed = true
			if _, err := store.verifyMVPProduction(context.Background(), "company-a", todo, request, ""); err != nil {
				t.Fatalf("confirmed PACED-only proof: %v", err)
			}
		})
	}
}

func TestRestoredLocalProofRequiresExactDeliveredContract(t *testing.T) {
	proof := TODOProofReference{Kind: "remote-local-delivery", Source: "private-reader-and-paced-runner", Reference: "run=approved", SourceDigest: "sha256:" + strings.Repeat("a", 64), VerificationState: "verified"}
	commits := []TODOCommitReference{{Repository: "belluga_builder_docker", Commit: strings.Repeat("a", 40), SourceDigest: proof.SourceDigest, VerificationState: "verified"}}
	position := 4
	content := TODOAuthoredContent{Title: "Delivered slice", Objective: "Create TODOs", Sections: []TODOSection{{Key: "intent", Title: "Intent", BodyMarkdown: "Approved", Position: 0, Ownership: "contract"}}, DiffExpectation: []TODORepositoryScope{}, AffectedFiles: []string{}}
	record := TODORecord{ID: "todo-a", ProjectID: "builder", Revision: 53, Stage: "Local-Implemented", StageVerification: "unverified", ReleaseID: "r-08", Position: &position, ImplementationCommits: commits}
	prior := TODOAsOfResponse{ProjectID: "builder", TODOID: "todo-a", Revision: 39, Content: content, State: TODOAsOfState{Stage: "Local-Implemented", StageVerification: "verified", VerifiedTransitionRef: &proof, ReleaseID: "r-08", Position: position, PlacementAssigned: true, ImplementationCommits: commits}}
	got, err := restoredLocalProof(record, content, prior)
	if err != nil || got != proof {
		t.Fatalf("exact restored proof = %+v, %v", got, err)
	}
	changed := content
	changed.Title = "Future scope"
	if _, err := restoredLocalProof(record, changed, prior); err == nil {
		t.Fatal("future contract reused old local delivery proof")
	}
	prior.State.ImplementationCommits = []TODOCommitReference{}
	if _, err := restoredLocalProof(record, content, prior); err == nil {
		t.Fatal("changed implementation attribution reused old proof")
	}
	prior.State.ImplementationCommits = commits
	record.StageVerification = "verified"
	if _, err := restoredLocalProof(record, content, prior); err == nil {
		t.Fatal("already verified stage accepted recovery")
	}
	if !reflect.DeepEqual(commits, prior.State.ImplementationCommits) {
		t.Fatal("test fixture changed")
	}
}

func TestRestoredRemoteCodeProofRequiresExactDeliveredContract(t *testing.T) {
	digest := "sha256:" + strings.Repeat("b", 64)
	proof := TODOProofReference{Kind: "remote-code-delivery", Source: "private-reader", Reference: "belluga/delphi-ai@builder:" + strings.Repeat("a", 40), SourceDigest: digest, VerificationState: "verified"}
	commits := []TODOCommitReference{{Repository: "delphi-ai", Commit: strings.Repeat("a", 40), SourceDigest: digest, VerificationState: "verified"}}
	position := 4
	content := TODOAuthoredContent{Title: "Delivered slice", Objective: "Create TODOs", Sections: []TODOSection{{Key: "intent", Title: "Intent", BodyMarkdown: "Approved", Position: 0, Ownership: "contract"}}}
	record := TODORecord{ID: "todo-a", ProjectID: "builder", Revision: 53, Stage: "Local-Implemented", StageVerification: "unverified", ReleaseID: "r-08", Position: &position, ImplementationCommits: commits}
	prior := TODOAsOfResponse{ProjectID: "builder", TODOID: record.ID, Revision: 39, Content: content, State: TODOAsOfState{Stage: "Local-Implemented", StageVerification: "verified", VerifiedTransitionRef: &proof, ReleaseID: record.ReleaseID, Position: position, PlacementAssigned: true, ImplementationCommits: commits}}
	got, err := restoredLocalProof(record, content, prior)
	if err != nil || got != proof {
		t.Fatalf("exact remote code proof = %+v, %v", got, err)
	}
	prior.State.VerifiedTransitionRef = &TODOProofReference{Kind: "remote-code-delivery", Source: "paced-local-runner", Reference: proof.Reference, SourceDigest: digest, VerificationState: "verified"}
	if _, err := restoredLocalProof(record, content, prior); err == nil {
		t.Fatal("unrecognized proof source accepted")
	}
	prior.State.VerifiedTransitionRef = &proof
	record.ImplementationCommits = nil
	if _, err := restoredLocalProof(record, content, prior); err == nil {
		t.Fatal("changed implementation commits reused remote code proof")
	}
}

func TestMVPCompletionRequiresReaderVerifiedDeploymentAndCurrentContent(t *testing.T) {
	root := strings.Repeat("a", 40)
	digest := "sha256:" + strings.Repeat("b", 64)
	local := TODOProofReference{Kind: "remote-local-delivery", Source: "private-reader", Reference: "local", SourceDigest: digest, VerificationState: "verified"}
	implementation := strings.Repeat("c", 40)
	foundation := strings.Repeat("d", 40)
	todo := TODORecord{ID: "todo-a", ProjectID: "builder", ReleaseID: "r-08", Stage: "Local-Implemented", StageVerification: "verified", VerifiedTransitionRef: &local, Source: TODOSource{Digest: digest}, ImplementationCommits: []TODOCommitReference{{Repository: "Builder", Commit: implementation}, {Repository: "belluga_builder_foundation_documentation", Commit: foundation}}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" || r.URL.Query().Get("project_mode") != "mvp" || r.URL.Query().Get("root_sha") != root || r.URL.Query().Get("commit_sha") != root || r.URL.Query().Get("project_commits") != implementation || r.URL.Query().Get("foundation_commits") != foundation || r.URL.Query().Get("todo_id") != todo.ID {
			t.Errorf("invalid private Reader request: %s", r.URL.String())
		}
		_, _ = w.Write([]byte(`{"company_id":"company-a","project_id":"builder","todo_id":"todo-a","deployment_id":17,"environment":"production","root_sha":"` + root + `","module_path":"project","commit_sha":"` + root + `","project_commits":["` + implementation + `"],"foundation_commits":["` + foundation + `"],"outcome":"verified","project_mode":"mvp","reader_sha":"` + strings.Repeat("e", 40) + `"}`))
	}))
	defer server.Close()
	store := &MongoStore{proofReaderURL: server.URL, proofReaderToken: "secret", proofHTTPClient: server.Client()}
	request := TODOActionRequest{ProofReferences: []TODOProofReference{{Kind: "mvp-deployment-claim", Source: "github-deployment", Reference: "17@" + root, SourceDigest: digest, VerificationState: "unverified"}}}
	proof, err := store.verifyMVPProduction(context.Background(), "company-a", todo, request, "")
	if err != nil || proof.Kind != "production-deployment" || proof.VerificationState != "verified" {
		t.Fatalf("verified MVP completion = %+v, %v", proof, err)
	}
	request.ProofReferences[0].SourceDigest = "sha256:" + strings.Repeat("c", 64)
	if _, err := store.verifyMVPProduction(context.Background(), "company-a", todo, request, ""); err == nil {
		t.Fatal("stale content claim accepted")
	}
	request.ProofReferences[0].SourceDigest = digest
	todo.StageVerification = "unverified"
	if _, err := store.verifyMVPProduction(context.Background(), "company-a", todo, request, ""); err == nil {
		t.Fatal("unverified local delivery accepted")
	}
}

func TestMVPImplementationClaimsRejectUnknownSource(t *testing.T) {
	commit := strings.Repeat("a", 40)
	local := TODOProofReference{Kind: "remote-local-delivery"}
	todo := TODORecord{VerifiedTransitionRef: &local, ImplementationCommits: []TODOCommitReference{{Repository: "Builder", Commit: commit}, {Repository: "PACED", Commit: commit}}}
	builder, paced, foundation, err := mvpImplementationClaims(todo)
	if err != nil || len(builder) != 1 || len(paced) != 1 || len(foundation) != 0 {
		t.Fatalf("classified code sources = %v %v %v", builder, paced, err)
	}
	todo.ImplementationCommits = append(todo.ImplementationCommits, TODOCommitReference{Repository: "belluga_builder_foundation_documentation", Commit: commit})
	_, _, foundation, err = mvpImplementationClaims(todo)
	if err != nil || len(foundation) != 1 {
		t.Fatalf("published Foundation source = %v, %v", foundation, err)
	}
	todo.ImplementationCommits[1].Repository = "unregistered-tool"
	if _, _, _, err := mvpImplementationClaims(todo); err == nil {
		t.Fatal("unknown implementation source accepted")
	}
	todo.ImplementationCommits = nil
	if _, _, _, err := mvpImplementationClaims(todo); err == nil {
		t.Fatal("missing implementation claims accepted without historical local run")
	}
	local.Kind = "local-run-accepted"
	if _, _, _, err := mvpImplementationClaims(todo); err != nil {
		t.Fatalf("previously accepted legacy local run rejected: %v", err)
	}
}

func TestMongoBornAdjacentStartRequiresCurrentApproval(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	ref := "builder/todo-a/content/1"
	approval := TODOProofReference{Kind: "owner-approval", Source: "mongo-content-revision", Reference: ref, SourceDigest: digest, VerificationState: "verified"}
	record := TODORecord{ID: "todo-a", Stage: "Planning", StageVerification: "verified", VerifiedTransitionRef: &TODOProofReference{Kind: "created", SourceDigest: digest, VerificationState: "verified"}, Revision: 1}
	request := TODOActionRequest{RequestedStage: "Implementation-Approval-Pending", ProofReferences: []TODOProofReference{}, WaiverReferences: []TODOProofReference{}}
	proof, err := mongoBornStageProof(record, request, digest, ref)
	if err != nil || proof.Kind != "planning-reviewed" || proof.SourceDigest != digest {
		t.Fatalf("pre-approval edge = %+v, %v", proof, err)
	}
	record.Stage = "Implementation-Approval-Pending"
	record.VerifiedTransitionRef = &proof
	record.Revision++
	request.RequestedStage = "Ready-for-Implementation"
	if _, err := mongoBornStageProof(record, request, digest, ref); err == nil {
		t.Fatal("Ready accepted without owner approval")
	}
	request.ProofReferences = []TODOProofReference{{Kind: "owner-approval", Source: "owner-chat", Reference: "APROVADO", SourceDigest: digest, VerificationState: "unverified"}}
	proof, err = mongoBornStageProof(record, request, digest, ref)
	if err != nil || proof.Kind != "owner-approval" || proof.VerificationState != "verified" {
		t.Fatalf("approved Ready edge = %+v, %v", proof, err)
	}
	record.VerifiedTransitionRef = &proof
	request.ProofReferences = nil
	record.Stage = "Ready-for-Implementation"
	record.Revision++
	request.RequestedStage = "In-Progress"
	proof, err = mongoBornStageProof(record, request, digest, ref)
	if err != nil || proof.Kind != "work-started" || proof.Source != "mongo-todo-api" || proof.SourceDigest != digest {
		t.Fatalf("governed start edge = %+v, %v", proof, err)
	}
	record.Stage = "Implementation-Approval-Pending"
	record.VerifiedTransitionRef = &approval
	if _, err := mongoBornStageProof(record, TODOActionRequest{RequestedStage: "Ready-for-Implementation"}, digest+"changed", ref); err == nil {
		t.Fatal("Ready accepted stale content approval")
	}
	if _, err := mongoBornStageProof(record, TODOActionRequest{RequestedStage: "Completed"}, digest, ref); err == nil {
		t.Fatal("Mongo-born TODO skipped adjacent lifecycle stages")
	}
}

func TestMongoBornMigrationCannotCarryApprovalAcrossStageEdges(t *testing.T) {
	oldDigest := "sha256:" + strings.Repeat("a", 64)
	currentDigest := "sha256:" + strings.Repeat("b", 64)
	approval := TODOProofReference{Kind: "owner-approval", Source: "owner-chat", Reference: "APROVADO", SourceDigest: oldDigest, VerificationState: "verified"}
	record := TODORecord{ID: "todo-a", ProjectID: "builder", Revision: 7, Stage: "Implementation-Approval-Pending", StageVerification: "verified", VerifiedTransitionRef: &approval}
	request := TODOActionRequest{RequestedStage: "Ready-for-Implementation"}
	if _, ok := mongoBornMigrationTransitionProof(record, request, currentDigest, "builder/todo-a/content/7", true); ok {
		t.Fatal("migration-carried owner approval authorized a new stage")
	}

	record.Stage = "Ready-for-Implementation"
	request.RequestedStage = "In-Progress"
	if _, ok := mongoBornMigrationTransitionProof(record, request, currentDigest, "builder/todo-a/content/7", false); ok {
		t.Fatal("unverified migration lineage authorized a new stage")
	}
	record.VerifiedTransitionRef.SourceDigest = currentDigest
	if _, ok := mongoBornMigrationTransitionProof(record, request, currentDigest, "builder/todo-a/content/7", true); ok {
		t.Fatal("same-digest approval was treated as migration lineage")
	}
	request.ProofReferences = []TODOProofReference{{Kind: "owner-approval"}}
	if _, ok := mongoBornMigrationTransitionProof(record, request, currentDigest, "builder/todo-a/content/7", true); ok {
		t.Fatal("migration continuity accepted caller-supplied replacement approval")
	}
}

func TestStageAuthorityRequiresTypedMongoV2RegardlessOfTODOIdentity(t *testing.T) {
	v2 := &TODOContent{SchemaVersion: todoContentSchemaVersion}
	for _, record := range []TODORecord{
		{ID: "builder-r-08-selected", Source: TODOSource{Foundation: "foundation", Digest: "sha256:" + strings.Repeat("a", 64)}, Content: v2},
		{ID: "edited-v1", Source: TODOSource{Foundation: "foundation", Digest: "sha256:" + strings.Repeat("a", 64)}, Content: &TODOContent{SchemaVersion: 1}},
		{ID: "imported-v1", Source: TODOSource{Foundation: "foundation", Digest: "sha256:" + strings.Repeat("a", 64)}},
		{ID: "mongo-v1", Source: TODOSource{Kind: "mongo-content-revision"}, Content: &TODOContent{SchemaVersion: 1}},
	} {
		if hasTypedMongoTODOStageAuthority(record) {
			t.Fatalf("non-v2 TODO %q had stage authority", record.ID)
		}
	}
	if !hasTypedMongoTODOStageAuthority(TODORecord{ID: "new-v2", Source: TODOSource{Kind: "mongo-content-revision"}, Content: v2}) {
		t.Fatal("current typed Mongo v2 TODO lacks stage authority")
	}
}

func TestPlanningRefinementPreservesOnlyMongoBornStageProof(t *testing.T) {
	mongo := TODORecord{Stage: "Planning", Source: TODOSource{Kind: "mongo-content-revision"}}
	if invalidatesContentApproval(mongo) {
		t.Fatal("Planning text refinement invalidated Mongo lifecycle position")
	}
	mongo.Stage = "Implementation-Approval-Pending"
	if !invalidatesContentApproval(mongo) {
		t.Fatal("edited approval-pending content retained its review proof")
	}
	imported := TODORecord{Stage: "Planning", Source: TODOSource{Foundation: "builder"}}
	if !invalidatesContentApproval(imported) {
		t.Fatal("Foundation import unexpectedly changed its content proof behavior")
	}
}

func TestMongoBornLocalDeliveryRejectsUnapprovedContentEdit(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	proof := TODOProofReference{Kind: "work-started", SourceDigest: digest, VerificationState: "verified"}
	record := TODORecord{Stage: "In-Progress", StageVerification: "verified", VerifiedTransitionRef: &proof}
	if err := requireApprovedCurrentContent(record, digest); err != nil {
		t.Fatalf("current approved content rejected: %v", err)
	}
	record.StageVerification = "unverified"
	record.VerifiedTransitionRef = nil
	if err := requireApprovedCurrentContent(record, digest); err == nil {
		t.Fatal("unverified content edit was accepted for Local-Implemented")
	}
	record.StageVerification = "verified"
	proof.SourceDigest = "sha256:" + strings.Repeat("b", 64)
	record.VerifiedTransitionRef = &proof
	if err := requireApprovedCurrentContent(record, digest); err == nil {
		t.Fatal("approval for an older content digest was accepted")
	}
}

func TestEditedImportedLocalDeliveryUsesCurrentContentDigest(t *testing.T) {
	currentDigest := "sha256:" + strings.Repeat("c", 64)
	foundationDigest := "sha256:" + strings.Repeat("a", 64)
	record := TODORecord{
		ID: "imported-todo", ProjectID: "builder", ReleaseID: "r-08", Stage: "In-Progress", StageVerification: "verified",
		Source:                TODOSource{Foundation: "builder", Digest: foundationDigest},
		VerifiedTransitionRef: &TODOProofReference{Kind: "work-started", SourceDigest: currentDigest, VerificationState: "verified"},
	}
	if digest, err := importedEditedTODOCurrentDeliveryDigest(record, currentDigest); err != nil || digest != currentDigest || digest == foundationDigest {
		t.Fatalf("edited imported delivery digest = %q, %v; want current content digest", digest, err)
	}
	commitSHA := strings.Repeat("d", 40)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		_ = json.NewEncoder(w).Encode(remoteCommitResult{CompanyID: query.Get("company_id"), ProjectID: query.Get("project_id"), Repository: query.Get("repository"), Branch: query.Get("branch"), CommitSHA: query.Get("commit_sha"), RemoteHead: commitSHA, ParentSHA: strings.Repeat("e", 40), Paths: []string{"go-app/internal/registry/todo_access.go"}, RepositoryNodeID: "R_kgDOImmutable", Outcome: "verified"})
	}))
	defer server.Close()
	store := &MongoStore{proofReaderURL: server.URL, proofReaderToken: strings.Repeat("x", 32), proofHTTPClient: server.Client()}
	record.DiffExpectation = []TODORepositoryScope{{Repository: "Builder", RepositoryID: "owner/repo", ExpectedPaths: []string{"go-app/internal/registry/todo_access.go"}, NotExpectedPaths: []string{}}}
	record.ImplementationCommits = []TODOCommitReference{{Repository: "Builder", Commit: commitSHA, SourceDigest: currentDigest, VerificationState: "unverified"}}
	local := TODOProofReference{Kind: "local-run-verified", Source: "paced-local-runner", Reference: "run", SourceDigest: currentDigest, VerificationState: "verified"}
	branches := map[string]string{"owner/repo": "r-08"}
	foundationRepository := "belluga/belluga_builder_foundation_documentation"
	remote, err := store.verifyRemoteLocalDeliveryWithBranchPlan(context.Background(), "company-a", foundationRepository, record, currentDigest, local, branches)
	if err != nil || remote.Kind != "remote-local-delivery" {
		t.Fatalf("remote delivery proof bound to current imported content = %+v, %v", remote, err)
	}
	record.ImplementationCommits[0].SourceDigest = foundationDigest
	if _, err := store.verifyRemoteLocalDeliveryWithBranchPlan(context.Background(), "company-a", foundationRepository, record, currentDigest, local, branches); err == nil {
		t.Fatal("remote delivery accepted a commit bound to stale Foundation provenance")
	}
	record.VerifiedTransitionRef.SourceDigest = foundationDigest
	if _, err := importedEditedTODOCurrentDeliveryDigest(record, currentDigest); err == nil {
		t.Fatal("edited imported TODO with stale Foundation-bound approval was accepted for Local-Implemented")
	}
	record.VerifiedTransitionRef.SourceDigest = currentDigest
	record.StageVerification = "unverified"
	if _, err := importedEditedTODOCurrentDeliveryDigest(record, currentDigest); err == nil {
		t.Fatal("edited imported TODO without current verified stage proof was accepted for Local-Implemented")
	}
}

func TestMongoBornInProgressContentReapproval(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	ref := "builder/todo-a/content/8"
	record := TODORecord{Stage: "In-Progress", StageVerification: "unverified"}
	request := TODOActionRequest{RequestedStage: "In-Progress", ProofReferences: []TODOProofReference{{Kind: "owner-approval", Source: "mongo-content-revision", Reference: ref, SourceDigest: digest, VerificationState: "unverified"}}}
	proof, err := mongoBornStageProof(record, request, digest, ref)
	if err != nil || proof.VerificationState != "verified" || proof.SourceDigest != digest {
		t.Fatalf("current content reapproval = %+v, %v", proof, err)
	}
	request.ProofReferences[0].SourceDigest = "sha256:" + strings.Repeat("b", 64)
	if _, err := mongoBornStageProof(record, request, digest, ref); err == nil {
		t.Fatal("stale content reapproval was accepted")
	}
	record.StageVerification = "verified"
	request.ProofReferences[0].SourceDigest = digest
	if _, err := mongoBornStageProof(record, request, digest, ref); err == nil {
		t.Fatal("already verified In-Progress TODO was reapproved without a content edit")
	}
}

func TestCurrentContentOwnerApprovalProofRequiresCanonicalApprovalAndExactRevision(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	revision := int64(4)
	position := 1
	approval := TODOProofReference{Kind: "owner-approval", Source: "mongo-content-revision", Reference: "builder/todo-a/content/7", SourceDigest: digest, VerificationState: "unverified"}
	record := TODORecord{ID: "todo-a", ProjectID: "builder", ReleaseID: "r-08", Position: &position, Stage: "In-Progress", StageVerification: "unverified", Revision: revision, ProofReferences: []TODOProofReference{approval}}
	request := VerifyCurrentTODOStageRequest{
		ProjectID: "builder", TODOID: "todo-a", ExpectedRevision: &revision, OperationID: "verify-content-4",
		ProofReferences: []TODOProofReference{{Kind: "owner-approval", Source: "mongo-content-revision", Reference: "builder/todo-a/content/7", SourceDigest: digest, VerificationState: "unverified"}},
	}
	proof, err := currentContentOwnerApprovalProof(record, request, 7, digest, "builder/todo-a/content/7")
	if err != nil || proof.VerificationState != "verified" || proof.SourceDigest != digest {
		t.Fatalf("current approved-content proof = %+v, %v", proof, err)
	}

	withoutRecordEvidence := record
	withoutRecordEvidence.ProofReferences = nil
	if _, err := currentContentOwnerApprovalProof(withoutRecordEvidence, request, 7, digest, "builder/todo-a/content/7"); err == nil {
		t.Fatal("caller-supplied owner approval passed without canonical record evidence")
	}
	wrongRecordApproval := record
	wrongRecordApproval.ProofReferences = []TODOProofReference{{Kind: "owner-approval", Source: "mongo-content-revision", Reference: "builder/todo-a/content/6", SourceDigest: digest, VerificationState: "unverified"}}
	if _, err := currentContentOwnerApprovalProof(wrongRecordApproval, request, 7, digest, "builder/todo-a/content/7"); err == nil {
		t.Fatal("record approval for another revision passed")
	}

	for name, mutate := range map[string]func(*VerifyCurrentTODOStageRequest){
		"missing proof": func(request *VerifyCurrentTODOStageRequest) { request.ProofReferences = nil },
		"forged digest": func(request *VerifyCurrentTODOStageRequest) {
			request.ProofReferences[0].SourceDigest = "sha256:" + strings.Repeat("b", 64)
		},
		"forged reference": func(request *VerifyCurrentTODOStageRequest) {
			request.ProofReferences[0].Reference = "builder/other-todo/content/7"
		},
		"wrong todo identity":      func(request *VerifyCurrentTODOStageRequest) { request.TODOID = "other-todo" },
		"stale aggregate revision": func(request *VerifyCurrentTODOStageRequest) { stale := int64(3); request.ExpectedRevision = &stale },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := request
			candidate.ProofReferences = append([]TODOProofReference(nil), request.ProofReferences...)
			mutate(&candidate)
			if _, err := currentContentOwnerApprovalProof(record, candidate, 7, digest, "builder/todo-a/content/7"); err == nil {
				t.Fatalf("invalid verification request %+v was accepted", candidate)
			}
		})
	}
}

func TestEditedImportedTODORequiresCurrentContentApprovalAndAdjacentStages(t *testing.T) {
	contentDigest := "sha256:" + strings.Repeat("c", 64)
	foundationDigest := "sha256:" + strings.Repeat("a", 64)
	contentRef := "builder/imported-todo/content/5"
	position := 2
	record := TODORecord{
		ID: "imported-todo", ProjectID: "builder", ReleaseID: "r-08", Position: &position,
		Stage: "Planning", StageVerification: "unverified", Revision: 5,
		Source: TODOSource{Foundation: "builder", Revision: strings.Repeat("b", 40), Path: "todos/active/TODO-imported-todo.md", Digest: foundationDigest},
	}
	request := TODOActionRequest{RequestedStage: "In-Progress", ProofReferences: []TODOProofReference{{
		Kind: "owner-approval", Source: "owner-chat", Reference: "APROVADO", SourceDigest: foundationDigest, VerificationState: "unverified",
	}}, WaiverReferences: []TODOProofReference{}}
	if !importedEditedTODOContentStageEdge(record, request) {
		t.Fatal("legacy Planning-to-In-Progress shortcut was not intercepted")
	}
	if _, err := importedEditedTODOStageProof(record, request, contentDigest, contentRef); err == nil {
		t.Fatal("legacy Planning-to-In-Progress approval accepted an edited imported TODO")
	}

	request.RequestedStage = "Implementation-Approval-Pending"
	request.ProofReferences[0] = TODOProofReference{Kind: "owner-approval", Source: "mongo-content-revision", Reference: contentRef, SourceDigest: foundationDigest, VerificationState: "unverified"}
	if _, err := importedEditedTODOStageProof(record, request, contentDigest, contentRef); err == nil {
		t.Fatal("approval bound to the immutable Foundation provenance digest was accepted")
	}
	request.ProofReferences[0].SourceDigest = contentDigest
	approval, err := importedEditedTODOStageProof(record, request, contentDigest, contentRef)
	if err != nil || approval.VerificationState != "verified" || approval.Reference != contentRef || approval.SourceDigest != contentDigest {
		t.Fatalf("current-content approval = %+v, %v", approval, err)
	}
	if record.Source.Digest != foundationDigest || record.Source.Foundation != "builder" {
		t.Fatalf("Foundation import provenance changed during approval: %+v", record.Source)
	}

	record.Stage, record.StageVerification, record.VerifiedTransitionRef = "Implementation-Approval-Pending", "verified", &approval
	record.Revision++
	request.RequestedStage, request.ProofReferences = "Ready-for-Implementation", []TODOProofReference{}
	readyProof, err := importedEditedTODOStageProof(record, request, contentDigest, contentRef)
	if err != nil || readyProof != approval {
		t.Fatalf("current-content Ready edge = %+v, %v", readyProof, err)
	}
	record.Stage, record.Revision = "Ready-for-Implementation", record.Revision+1
	request.RequestedStage = "In-Progress"
	startedProof, err := importedEditedTODOStageProof(record, request, contentDigest, contentRef)
	if err != nil || startedProof.Kind != "work-started" || startedProof.SourceDigest != contentDigest || startedProof.Reference != "builder/imported-todo/content/5/revision/8" {
		t.Fatalf("current-content work-start edge = %+v, %v", startedProof, err)
	}
	if record.Source.Digest != foundationDigest || record.Source.Foundation != "builder" {
		t.Fatalf("Foundation import provenance changed across adjacent stage proof: %+v", record.Source)
	}
	request.RequestedStage = "Local-Implemented"
	if importedEditedTODOContentStageEdge(record, request) {
		t.Fatal("post-start local completion was incorrectly routed through the imported approval flow")
	}
}

func TestDigestlessLegacyImportedContentCanBeReadButNotApproved(t *testing.T) {
	record := TODORecord{ProjectID: "builder", ID: "imported-todo"}
	content := TODOAuthoredContent{Title: "TODO: imported", Objective: "Legacy authored contract", Sections: []TODOSection{}, DiffExpectation: []TODORepositoryScope{}, AffectedFiles: []string{}}
	legacy := TODOContentVersion{ProjectID: record.ProjectID, TODOID: record.ID, Revision: 4, Content: content}
	revision, digest, reference, available, err := importedTODOContentSubject(record, legacy)
	if err != nil || !available || revision != 4 || digest != "" || reference != "builder/imported-todo/content/4" {
		t.Fatalf("digestless legacy content subject = rev=%d digest=%q ref=%q available=%t err=%v", revision, digest, reference, available, err)
	}
	if _, err := importedEditedTODOStageProof(TODORecord{ID: record.ID, ProjectID: record.ProjectID, Source: TODOSource{Foundation: "builder"}}, TODOActionRequest{}, digest, reference); err == nil {
		t.Fatal("digestless legacy content was accepted as a stage approval subject")
	}
	legacy.Digest = "sha256:" + strings.Repeat("a", 64)
	if _, _, _, _, err := importedTODOContentSubject(record, legacy); err == nil {
		t.Fatal("nonempty invalid legacy digest was accepted")
	}
	legacy.Revision = 0
	legacy.Digest = ""
	if _, _, _, available, err := importedTODOContentSubject(record, legacy); err != nil || available {
		t.Fatalf("digestless revision-zero baseline availability=%t err=%v; want original unedited behavior", available, err)
	}
}

func TestSelectedImportedTODOKeepsSelectedProofContract(t *testing.T) {
	record := TODORecord{
		ID:     "builder-mongo-selected-todo-stage-verification",
		Source: TODOSource{Foundation: "builder", Revision: strings.Repeat("b", 40), Path: "todos/active/TODO-builder-mongo-selected-todo-stage-verification.md", Digest: "sha256:" + strings.Repeat("a", 64)},
	}
	if usesImportedTODOContentStageProof(record) || invalidatesContentApproval(record) {
		t.Fatal("selected import was routed through generic content-bound stage proof")
	}
}

func TestLocalProofMustMatchExactTypedChecksAndReleaseBranches(t *testing.T) {
	store := &MongoStore{}
	content := TODOAuthoredContent{TODOContent: TODOContent{LocalChecks: []TODOLocalCheck{{ID: "G-TEST", RepositoryID: "belluga/repo", Argv: []string{"go", "test", "./..."}, WorkingDirectory: ".", InputPaths: []string{"**"}}}}}
	branches := map[string]string{"belluga/repo": "builder"}
	result := &LocalProofResult{SchemaVersion: "builder-local-proof-result-v3", RunID: "11111111-2222-4333-8444-555555555555", StartedAt: time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano), FinishedAt: time.Now().UTC().Format(time.RFC3339Nano), Repositories: []localProofRepository{{RepositoryID: "belluga/repo", Branch: "builder", HeadBefore: strings.Repeat("a", 40), HeadAfter: strings.Repeat("a", 40), InputsDigestBefore: "sha256:" + strings.Repeat("b", 64), InputsDigestAfter: "sha256:" + strings.Repeat("b", 64)}}, Checks: []localProofCheck{{ID: "G-TEST", RepositoryID: "belluga/repo", ExitCode: 0}}}
	seal := func() {
		data, _ := json.Marshal(result)
		var value map[string]any
		_ = json.Unmarshal(data, &value)
		delete(value, "result_digest")
		canonical, _ := json.Marshal(value)
		result.ResultDigest = sourceHash(canonical)
	}
	seal()
	if proof, err := store.validateLocalProof(content, branches, result); err != nil || proof.Kind != "local-run-verified" {
		t.Fatalf("valid repository-scoped proof = %+v, %v", proof, err)
	}
	wrongBranch := *result
	wrongBranch.Repositories = append([]localProofRepository(nil), result.Repositories...)
	wrongBranch.Repositories[0].Branch = "main"
	sealResult := func(candidate *LocalProofResult) {
		data, _ := json.Marshal(candidate)
		var value map[string]any
		_ = json.Unmarshal(data, &value)
		delete(value, "result_digest")
		canonical, _ := json.Marshal(value)
		candidate.ResultDigest = sourceHash(canonical)
	}
	sealResult(&wrongBranch)
	if _, err := store.validateLocalProof(content, branches, &wrongBranch); err == nil {
		t.Fatal("branch not owned by the Release was accepted")
	}
	failed := *result
	failed.Checks = append([]localProofCheck(nil), result.Checks...)
	failed.Checks[0].ExitCode = 1
	sealResult(&failed)
	if _, err := store.validateLocalProof(content, branches, &failed); err == nil {
		t.Fatal("failed TODO-selected check was accepted")
	}
	if _, err := store.validateLocalProof(content, branches, nil); err == nil {
		t.Fatal("missing local result was accepted")
	}
	clone := func() *LocalProofResult {
		candidate := *result
		candidate.Repositories = append([]localProofRepository(nil), result.Repositories...)
		candidate.Checks = append([]localProofCheck(nil), result.Checks...)
		return &candidate
	}
	for name, mutate := range map[string]func(*LocalProofResult){
		"tampered digest": func(candidate *LocalProofResult) { candidate.ResultDigest = "sha256:" + strings.Repeat("c", 64) },
		"changed head":    func(candidate *LocalProofResult) { candidate.Repositories[0].HeadAfter = strings.Repeat("d", 40) },
		"changed input digest": func(candidate *LocalProofResult) {
			candidate.Repositories[0].InputsDigestAfter = "sha256:" + strings.Repeat("e", 64)
		},
		"missing check":            func(candidate *LocalProofResult) { candidate.Checks = nil },
		"duplicate check":          func(candidate *LocalProofResult) { candidate.Checks = append(candidate.Checks, candidate.Checks[0]) },
		"foreign check repository": func(candidate *LocalProofResult) { candidate.Checks[0].RepositoryID = "belluga/other" },
		"missing repository":       func(candidate *LocalProofResult) { candidate.Repositories = nil },
		"duplicate repository": func(candidate *LocalProofResult) {
			candidate.Repositories = append(candidate.Repositories, candidate.Repositories[0])
		},
		"foreign repository": func(candidate *LocalProofResult) { candidate.Repositories[0].RepositoryID = "belluga/other" },
	} {
		candidate := clone()
		mutate(candidate)
		if name != "tampered digest" {
			sealResult(candidate)
		}
		if _, err := store.validateLocalProof(content, branches, candidate); err == nil {
			t.Errorf("%s local proof mutation was accepted", name)
		}
	}
}

func TestLocalProofHeadMustBePublishedOnReleaseBranchButNeedNotBeBranchTip(t *testing.T) {
	const head = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const advanced = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	published := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("repository") != "belluga/repo" || r.URL.Query().Get("branch") != "builder" || r.URL.Query().Get("commit_sha") != head {
			t.Errorf("wrong tested-head verification request: %s", r.URL)
		}
		if !published {
			http.Error(w, "not published", http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(remoteCommitResult{CompanyID: "company-a", ProjectID: "builder-project", Repository: "belluga/repo", Branch: "builder", CommitSHA: head, RemoteHead: advanced, ParentSHA: strings.Repeat("c", 40), Paths: []string{"go-app/main.go"}, RepositoryNodeID: "R_kgDOImmutable", Outcome: "verified"})
	}))
	defer server.Close()
	store := &MongoStore{}
	if err := store.ConfigureProofReader(server.URL, strings.Repeat("x", 32)); err != nil {
		t.Fatal(err)
	}
	todo := TODORecord{ProjectID: "builder-project"}
	result := &LocalProofResult{Repositories: []localProofRepository{{RepositoryID: "belluga/repo", Branch: "builder", HeadBefore: head}}}
	branches := map[string]string{"belluga/repo": "builder"}
	if err := store.verifyLocalProofHeads(context.Background(), "company-a", todo, branches, result); err == nil {
		t.Fatal("unpublished local-only test head was accepted")
	}
	published = true
	if err := store.verifyLocalProofHeads(context.Background(), "company-a", todo, branches, result); err != nil {
		t.Fatalf("published tested ancestor rejected after Release branch advanced: %v", err)
	}
}

func TestLocalProofSubjectRejectsStaleOrForeignPreflight(t *testing.T) {
	position, revision := 2, int64(7)
	digest := sourceHash([]byte("current content"))
	record := TODORecord{ID: "todo-a", ProjectID: "builder", Revision: revision, ReleaseID: "r-10", Position: &position}
	valid := LocalProofResult{SchemaVersion: "builder-local-proof-result-v3", CompanyID: "company-a", ProjectID: "builder", TODOID: "todo-a", TODORevision: &revision, ContentDigest: digest, ReleaseID: "r-10", Position: &position}
	if err := validateLocalProofSubject(context.Background(), nil, "company-a", record, digest, &valid); err != nil {
		t.Fatalf("matching subject rejected: %v", err)
	}
	for name, mutate := range map[string]func(*LocalProofResult){
		"old runner":      func(p *LocalProofResult) { p.SchemaVersion = "builder-local-proof-result-v2" },
		"company":         func(p *LocalProofResult) { p.CompanyID = "company-b" },
		"project":         func(p *LocalProofResult) { p.ProjectID = "other" },
		"digest":          func(p *LocalProofResult) { p.ContentDigest = sourceHash([]byte("old")) },
		"release":         func(p *LocalProofResult) { p.ReleaseID = "r-09" },
		"position":        func(p *LocalProofResult) { p.Position = intPointer(3) },
		"future revision": func(p *LocalProofResult) { p.TODORevision = int64Pointer(8) },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			mutate(&candidate)
			if err := validateLocalProofSubject(context.Background(), nil, "company-a", record, digest, &candidate); err == nil || !strings.Contains(err.Error(), "local_proof") {
				t.Fatalf("wrong subject accepted: %v", err)
			}
		})
	}
}

func TestRetainedTODOOperationRetryRejectsChangedInputAndOldRevision(t *testing.T) {
	record := TODORecord{ID: "t-cf5c0ffd07c4f50d", ProjectID: "boora", Revision: 4, Stage: "Completed", StageVerification: "verified", LastOperation: &TODOOperationState{ID: "op-1", Kind: "transition", RequestDigest: "sha256:" + strings.Repeat("a", 64), ResultRevision: 4}}
	if response, ok, err := exactTODOOperationRetry("belluga-solutions", record, "op-1", "transition", record.LastOperation.RequestDigest, 3); !ok || err != nil || !response.NoOp || response.CompanyID != "belluga-solutions" {
		t.Fatalf("retry = %+v %v %v", response, ok, err)
	}
	if _, ok, err := exactTODOOperationRetry("belluga-solutions", record, "op-1", "transition", "sha256:"+strings.Repeat("b", 64), 3); !ok || err == nil {
		t.Fatal("same ID with changed input was accepted")
	}
	if _, ok, err := exactTODOOperationRetry("belluga-solutions", record, "op-1", "transition", record.LastOperation.RequestDigest, 2); !ok || err == nil {
		t.Fatal("old revision was accepted")
	}
}

func TestCoordinatorCountsAsExecutableAndOnlyVerifiedStageIsEffective(t *testing.T) {
	record := TODORecord{ID: "t-9936500c9582e07e", Title: "Release coordination", Objective: "Coordinate children", NextExactStep: "Confirm publication", Stage: "Planning", StageVerification: "unverified", Qualifiers: []string{}}
	view := roadmapTODOViewFromRecord(record)
	if view.Kind != "executable" || view.DeliveryStageState != "unverified" || view.DeliveryStage != "" {
		t.Fatalf("unverified coordinator view = %+v", view)
	}
	record.Stage = "Completed"
	record.StageVerification = "verified"
	record.VerifiedTransitionRef = &TODOProofReference{Kind: "production-deployment", Source: "private-reader", Reference: "17@root", SourceDigest: "sha256:" + strings.Repeat("a", 64), VerificationState: "verified"}
	view = roadmapTODOViewFromRecord(record)
	if view.Kind != "executable" || view.DeliveryStage != "Completed" || view.DeliveryStageState != "verified" {
		t.Fatalf("verified coordinator view = %+v", view)
	}
}

func TestAuthoredContentDoesNotDuplicateRecordOwnedApproval(t *testing.T) {
	record := TODORecord{Title: "Planning", Objective: "Refine a plan", Sections: []TODOSection{
		{Key: "intent", Title: "Intent", BodyMarkdown: "Scope", Position: 0, Ownership: "contract"},
		{Key: "approval", Title: "Approval", BodyMarkdown: "APROVADO", Position: 1, Ownership: "contract"},
	}}
	content := contentFromTODO(record)
	content.DiffExpectation = []TODORepositoryScope{}
	content.AffectedFiles = []string{}
	if len(content.Sections) != 1 || content.Sections[0].Key != "intent" {
		t.Fatalf("record-owned approval leaked into authored content: %+v", content.Sections)
	}
	if err := validateNewAuthoredContent(content); err != nil {
		t.Fatalf("contract-only content was rejected: %v", err)
	}
	content.Sections = append(content.Sections, record.Sections[1])
	if err := validateNewAuthoredContent(content); err == nil {
		t.Fatal("record-owned approval was accepted in an authored content edit")
	}
}
