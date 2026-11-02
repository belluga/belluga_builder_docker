package registry

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

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

func TestMVPCompletionRequiresReaderVerifiedDeploymentAndCurrentContent(t *testing.T) {
	root := strings.Repeat("a", 40)
	digest := "sha256:" + strings.Repeat("b", 64)
	local := TODOProofReference{Kind: "remote-local-delivery", Source: "private-reader", Reference: "local", SourceDigest: digest, VerificationState: "verified"}
	implementation := strings.Repeat("c", 40)
	todo := TODORecord{ID: "todo-a", ProjectID: "builder", ReleaseID: "r-08", Stage: "Local-Implemented", StageVerification: "verified", VerifiedTransitionRef: &local, Source: TODOSource{Digest: digest}, ImplementationCommits: []TODOCommitReference{{Repository: "Builder", Commit: implementation}}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" || r.URL.Query().Get("project_mode") != "mvp" || r.URL.Query().Get("root_sha") != root || r.URL.Query().Get("commit_sha") != root || r.URL.Query().Get("project_commits") != implementation || r.URL.Query().Get("todo_id") != todo.ID {
			t.Errorf("invalid private Reader request: %s", r.URL.String())
		}
		_, _ = w.Write([]byte(`{"company_id":"company-a","project_id":"builder","todo_id":"todo-a","deployment_id":17,"environment":"production","root_sha":"` + root + `","module_path":"project","commit_sha":"` + root + `","project_commits":["` + implementation + `"],"outcome":"verified","project_mode":"mvp","reader_sha":"` + strings.Repeat("e", 40) + `"}`))
	}))
	defer server.Close()
	store := &MongoStore{proofReaderURL: server.URL, proofReaderToken: "secret", proofHTTPClient: server.Client()}
	request := TODOActionRequest{ProofReferences: []TODOProofReference{{Kind: "mvp-deployment-claim", Source: "github-deployment", Reference: "17@" + root, SourceDigest: digest, VerificationState: "unverified"}}}
	proof, err := store.verifyMVPProduction(context.Background(), "company-a", todo, request)
	if err != nil || proof.Kind != "production-deployment" || proof.VerificationState != "verified" {
		t.Fatalf("verified MVP completion = %+v, %v", proof, err)
	}
	request.ProofReferences[0].SourceDigest = "sha256:" + strings.Repeat("c", 64)
	if _, err := store.verifyMVPProduction(context.Background(), "company-a", todo, request); err == nil {
		t.Fatal("stale content claim accepted")
	}
	request.ProofReferences[0].SourceDigest = digest
	todo.StageVerification = "unverified"
	if _, err := store.verifyMVPProduction(context.Background(), "company-a", todo, request); err == nil {
		t.Fatal("unverified local delivery accepted")
	}
}

func TestMVPImplementationClaimsRejectUnknownSource(t *testing.T) {
	commit := strings.Repeat("a", 40)
	local := TODOProofReference{Kind: "remote-local-delivery"}
	todo := TODORecord{VerifiedTransitionRef: &local, ImplementationCommits: []TODOCommitReference{{Repository: "Builder", Commit: commit}, {Repository: "PACED", Commit: commit}}}
	builder, paced, err := mvpImplementationClaims(todo)
	if err != nil || len(builder) != 1 || len(paced) != 1 {
		t.Fatalf("classified code sources = %v %v %v", builder, paced, err)
	}
	todo.ImplementationCommits[1].Repository = "unregistered-tool"
	if _, _, err := mvpImplementationClaims(todo); err == nil {
		t.Fatal("unknown implementation source accepted")
	}
	todo.ImplementationCommits = nil
	if _, _, err := mvpImplementationClaims(todo); err == nil {
		t.Fatal("missing implementation claims accepted without historical local run")
	}
	local.Kind = "local-run-accepted"
	if _, _, err := mvpImplementationClaims(todo); err != nil {
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
		_ = json.NewEncoder(w).Encode(remoteCommitResult{CompanyID: query.Get("company_id"), ProjectID: query.Get("project_id"), Repository: query.Get("repository"), Branch: query.Get("branch"), CommitSHA: query.Get("commit_sha"), RemoteHead: commitSHA, ParentSHA: strings.Repeat("e", 40), Paths: []string{"go-app/internal/registry/todo_access.go"}, Outcome: "verified"})
	}))
	defer server.Close()
	store := &MongoStore{proofReaderURL: server.URL, proofReaderToken: strings.Repeat("x", 32), proofHTTPClient: server.Client()}
	record.DiffExpectation = []TODORepositoryScope{{Repository: "Builder", ExpectedPaths: []string{"go-app/internal/registry/todo_access.go"}, NotExpectedPaths: []string{}}}
	record.ImplementationCommits = []TODOCommitReference{{Repository: "Builder", Commit: commitSHA, SourceDigest: currentDigest, VerificationState: "unverified"}}
	local := TODOProofReference{Kind: "local-run-verified", Source: "paced-local-runner", Reference: "run", SourceDigest: currentDigest, VerificationState: "verified"}
	remote, err := store.verifyRemoteLocalDelivery(context.Background(), "company-a", "belluga/belluga_builder_foundation_documentation", record, currentDigest, local)
	if err != nil || remote.Kind != "remote-local-delivery" {
		t.Fatalf("remote delivery proof bound to current imported content = %+v, %v", remote, err)
	}
	record.ImplementationCommits[0].SourceDigest = foundationDigest
	if _, err := store.verifyRemoteLocalDelivery(context.Background(), "company-a", "belluga/belluga_builder_foundation_documentation", record, currentDigest, local); err == nil {
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
	record := TODORecord{ID: "todo-a", ProjectID: "builder", ReleaseID: "r-08", Position: &position, Stage: "In-Progress", StageVerification: "unverified", Revision: revision, ProofReferences: []TODOProofReference{approval}, Sections: []TODOSection{{Key: "approval", BodyMarkdown: "- **Approval status:** approved for Mongo content revision 7, digest " + digest + "."}}}
	content := TODOAuthoredContent{Sections: []TODOSection{{Key: "contract", BodyMarkdown: "approved decisions are immutable content"}}}
	request := VerifyCurrentTODOStageRequest{
		ProjectID: "builder", TODOID: "todo-a", ExpectedRevision: &revision, OperationID: "verify-content-4",
		ProofReferences: []TODOProofReference{{Kind: "owner-approval", Source: "mongo-content-revision", Reference: "builder/todo-a/content/7", SourceDigest: digest, VerificationState: "unverified"}},
	}
	proof, err := currentContentOwnerApprovalProof(record, request, content, 7, digest, "builder/todo-a/content/7")
	if err != nil || proof.VerificationState != "verified" || proof.SourceDigest != digest {
		t.Fatalf("current approved-content proof = %+v, %v", proof, err)
	}

	withoutCanonicalApproval := record
	withoutCanonicalApproval.Sections = nil
	if _, err := currentContentOwnerApprovalProof(withoutCanonicalApproval, request, content, 7, digest, "builder/todo-a/content/7"); err == nil {
		t.Fatal("caller-supplied owner approval passed without canonical record approval")
	}
	wrongRecordApproval := record
	wrongRecordApproval.Sections = []TODOSection{{Key: "approval", BodyMarkdown: "- **Approval status:** approved for Mongo content revision 6, digest " + digest + "."}}
	if _, err := currentContentOwnerApprovalProof(wrongRecordApproval, request, content, 7, digest, "builder/todo-a/content/7"); err == nil {
		t.Fatal("record approval for another revision passed")
	}
	withoutRecordEvidence := record
	withoutRecordEvidence.ProofReferences = nil
	if _, err := currentContentOwnerApprovalProof(withoutRecordEvidence, request, content, 7, digest, "builder/todo-a/content/7"); err == nil {
		t.Fatal("caller-supplied owner approval passed without canonical record evidence")
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
			if _, err := currentContentOwnerApprovalProof(record, candidate, content, 7, digest, "builder/todo-a/content/7"); err == nil {
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

func TestSelectedLocalProofRequiresExactMatrixRun(t *testing.T) {
	store := &MongoStore{}
	if err := store.ConfigureLocalMatrix(filepath.Join("..", "..", "config", "selected_todo_local_checks.json")); err != nil {
		t.Fatal(err)
	}
	id := "builder-company-tenant-mongo-registry"
	digest := "sha256:" + strings.Repeat("a", 64)
	now := time.Now().UTC()
	result := &LocalProofResult{
		SchemaVersion: "builder-local-proof-result-v1", TODOID: id,
		RunID: "11111111-2222-4333-8444-555555555555", MatrixDigest: store.localMatrix.digest,
		StartedAt: now.Add(-time.Minute).Format(time.RFC3339Nano), FinishedAt: now.Format(time.RFC3339Nano),
		Before: localProofSnapshot{BuilderHead: strings.Repeat("a", 40), PACEDHead: strings.Repeat("b", 40), BuilderInputsDigest: digest, PACEDInputsDigest: digest},
	}
	result.After = result.Before
	result.OutsideUntrackedBefore = map[string]map[string]string{"builder": {}, "paced": {}}
	result.OutsideUntrackedAfter = map[string]map[string]string{"builder": {}, "paced": {}}
	for _, command := range store.localMatrix.RequiredBy[id] {
		result.Commands = append(result.Commands, localProofCommand{ID: command, ExitCode: 0})
	}
	seal := func() {
		data, _ := json.Marshal(result)
		var value map[string]any
		_ = json.Unmarshal(data, &value)
		delete(value, "result_digest")
		canonical, _ := json.Marshal(value)
		result.ResultDigest = sourceHash(canonical)
	}
	seal()
	if proof, err := store.validateLocalProof(id, result); err != nil || proof.Kind != "local-run-verified" {
		t.Fatalf("valid proof = %+v, %v", proof, err)
	}
	result.TODOID = "builder-mongo-todo-history-storage-implementation"
	result.Commands = result.Commands[:len(store.localMatrix.DefaultRequired)]
	seal()
	if proof, err := store.validateLocalProof(result.TODOID, result); err != nil || proof.Kind != "local-run-verified" {
		t.Fatalf("individually admitted TODO proof = %+v, %v", proof, err)
	}
	result.TODOID = id
	result.Commands = append(result.Commands, localProofCommand{ID: "P-TEST", ExitCode: 0})
	seal()
	if _, err := store.validateLocalProof(id, nil); err == nil {
		t.Fatal("missing local result was accepted")
	}
	result.ResultDigest = digest
	if _, err := store.validateLocalProof(id, result); err == nil {
		t.Fatal("wrong local result digest was accepted")
	}
	seal()
	result.Commands[0].ExitCode = 1
	seal()
	if _, err := store.validateLocalProof(id, result); err == nil {
		t.Fatal("failed fixed command was accepted")
	}
	result.Commands[0].ExitCode = 0
	seal()
	result.After.BuilderInputsDigest = "sha256:" + strings.Repeat("b", 64)
	seal()
	if _, err := store.validateLocalProof(id, result); err == nil {
		t.Fatal("changed test input was accepted")
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
