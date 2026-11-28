package registry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// The setup seeds an already approved In-Progress predecessor. This test owns
// the evidence-edit, Reader-backed transition, persistence and readback edge.
func TestMongoRemoteCodeDeliveryTransition(t *testing.T) {
	for _, targetBranch := range []string{"dev", "stage"} {
		for _, legacyProof := range []bool{false, true} {
			name := "current-proof-"
			if legacyProof {
				name = "legacy-proof-"
			}
			t.Run(name+targetBranch+"-then-main", func(t *testing.T) {
				testMongoRemoteCodeDeliveryTransition(t, targetBranch, legacyProof)
			})
		}
	}
}

func testMongoRemoteCodeDeliveryTransition(t *testing.T, targetBranch string, legacyProof bool) {
	uri := os.Getenv("REGISTRY_TEST_MONGODB_URI")
	if uri == "" {
		if os.Getenv("REGISTRY_REQUIRE_MONGO_INTEGRATION") == "1" {
			t.Fatal("required Mongo replica-set integration fixture is unavailable")
		}
		t.Skip("isolated replica-set MongoDB fixture not configured")
	}
	ctx := context.Background()
	stamp := strconv.FormatInt(time.Now().UnixNano(), 36)
	store, err := OpenMongo(ctx, uri, stamp)
	if err != nil {
		t.Fatal(err)
	}
	store.tenantPrefix = stamp
	companyID, projectID, todoID := "company-a", "builder", "api-only-delivery"
	const constitutionID = "builder-constitution"
	database := store.client.Database(store.tenantDatabase(companyID))
	t.Cleanup(func() {
		_ = database.Drop(context.Background())
		_ = store.control.Drop(context.Background())
		_ = store.Close(context.Background())
	})
	if _, err := registerTestProjectWithBinding(ctx, store, Registration{CompanyID: companyID, CompanyName: "Company A", ProjectID: projectID, ProjectName: "Builder", Binding: validTestBinding()}); err != nil {
		t.Fatal(err)
	}
	canonical := CanonicalImport{ProjectID: projectID, GenerationID: "g1", SourceRevision: strings.Repeat("a", 40), OperationID: "fixture-canonical-import", Actor: "fixture-owner", Records: []CanonicalImportRecord{
		canonicalTestRecord(FamilyMandate, projectID, `{"purpose":"Build safely","scope":["project"]}`),
		canonicalTestRecord(FamilyDomain, projectID, `{"entities":[{"id":"Project","name":"Project","description":""}],"relations":[{"id":"OWNS","from":"Project","to":"Project","kind":"owns"}],"distinctions":[]}`),
		canonicalTestRecord(FamilyConstitution, constitutionID, `{"promotion_mode":"complete","capability_namespaces":["go"],"rules":[{"id":"authority","status":"current","statement":"Project API records verified delivery"}]}`),
		canonicalTestRecord(FamilyRoadmap, projectID, `{"stages":[{"id":"delivery","name":"Delivery","status":"planned"}]}`),
		canonicalTestRecord(FamilyModule, "projects", `{"purpose":"Owns project work","decisions":[{"id":"DEC-PROJECT-01","status":"current","statement":"Project owns TODO delivery"}],"schemas":[{"id":"todo","name":"todos","kind":"collection","fields":[{"name":"stage","type":"string"}]}]}`),
		canonicalTestRecord(FamilyPolicy, "access-policy", `{"rules":[{"id":"retention","status":"current","statement":"Keep audit records"}],"ownership":[{"owner_kind":"module","owner_id":"projects","decision_id":"DEC-PROJECT-01"}]}`),
	}}
	for i := range canonical.Records {
		canonical.Records[i].Record.ProjectID = projectID
		canonical.Records[i].Record.GenerationID = canonical.GenerationID
	}
	if _, err := store.ImportCanonicalDraft(ctx, companyID, canonical); err != nil {
		t.Fatal(err)
	}
	if legacyProof || targetBranch == "dev" {
		if _, err := store.ActivateCanonical(ctx, companyID, projectID, canonical.GenerationID, "fixture-canonical-activate", "fixture-owner"); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.ProvisionTODOCollection(ctx, companyID); err != nil {
		t.Fatal(err)
	}
	if err := store.ProvisionTODOHistory(ctx, companyID); err != nil {
		t.Fatal(err)
	}
	const repository, branch, secondaryRepository = "owner/repo", "r-08-rc", "owner/secondary"
	gitDir, baseline, commit, secondaryCommit, heads := createPromotionGitFixture(t, branch)
	head := heads[branch]
	if _, err := database.Collection(epicsCollection).InsertOne(ctx, roadmapEpic{StorageID: bson.NewObjectID(), ProjectID: projectID, ID: "e-06", Name: "Database", Position: 0}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Collection(releasesCollection).InsertOne(ctx, roadmapRelease{StorageID: bson.NewObjectID(), ProjectID: projectID, ID: "r-08", EpicID: "e-06", Name: "Mongo", Position: 0, DeliveryBranches: []releaseDeliveryBranch{{RepositoryID: repository, Branch: branch, OwnerReference: "fixture-owner", OperationID: "fixture-branch", ConfirmedAt: time.Now().UTC()}, {RepositoryID: secondaryRepository, Branch: branch, OwnerReference: "fixture-owner", OperationID: "fixture-branch-secondary", ConfirmedAt: time.Now().UTC()}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ChangeImplementationRepository(ctx, companyID, projectID, ImplementationRepositoryChange{Action: "confirm", ProjectID: projectID, ExpectedRevision: 0, OperationID: "fixture-repository", Actor: "fixture-owner", OwnerReference: "fixture-owner", RepositoryID: repository}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ChangeImplementationRepository(ctx, companyID, projectID, ImplementationRepositoryChange{Action: "confirm", ProjectID: projectID, ExpectedRevision: 1, OperationID: "fixture-repository-secondary", Actor: "fixture-owner", OwnerReference: "fixture-owner", RepositoryID: secondaryRepository}); err != nil {
		t.Fatal(err)
	}
	record := mongoBornTODOFixture()
	record.ID, record.ReleaseID = todoID, "r-08"
	position := 0
	record.Position = &position
	if _, err := store.AdmitTODO(ctx, companyID, projectID, TODOAdmissionRequest{SchemaVersion: todoCreationSchemaVersion, OperationID: "fixture-create", TODO: record}); err != nil {
		t.Fatal(err)
	}
	content := TODOAuthoredContent{TODOContent: validTODOContent(), Title: record.Title, Objective: record.Objective, DiffExpectation: []TODORepositoryScope{{Repository: "Builder", RepositoryID: repository, Baseline: baseline, ExpectedPaths: []string{"go-app/internal/registry/**"}, NotExpectedPaths: []string{}}, {Repository: "Secondary", RepositoryID: secondaryRepository, Baseline: baseline, ExpectedPaths: []string{"go-app/internal/registry/**"}, NotExpectedPaths: []string{}}}, AffectedFiles: []string{}}
	content.Planning = &TODOPlanning{Complexity: "small", AuditTriggers: testAuditTriggers(), Assumptions: []TODOPlanningAssumption{}, Reviews: []TODOPlanningReview{{Kind: "critique", Reference: "review/initial-critique"}}}
	content.Planning.AuditTriggers.BlastRadius = "cross-module"
	zero := int64(0)
	edit, err := store.EditTODOContent(ctx, companyID, TODOContentEditRequest{ProjectID: projectID, TODOID: todoID, ExpectedRevision: &zero, OperationID: "fixture-content", Content: content, Reason: "Set approved code scope"})
	if err != nil {
		t.Fatal(err)
	}
	current, err := store.ReadTODO(ctx, companyID, projectID, todoID, nil)
	if err != nil {
		t.Fatal(err)
	}
	seed := TODOProofReference{Kind: "implementation-started", Source: "mongo-content-revision", Reference: "fixture-approved-predecessor", SourceDigest: edit.ContentDigest, VerificationState: "verified"}
	if _, err := writeVerifiedTODO(ctx, database, database.Collection(todosCollection), companyID, current.TODO, "In-Progress", seed, "fixture-approved-start", "transition", sourceHash([]byte("fixture-approved-start"))); err != nil {
		t.Fatal(err)
	}
	var readerCalls atomic.Int64
	var arrivalIdentityMismatch, reachableOnRelease atomic.Bool
	var expectedArrivalBranch atomic.Value
	expectedArrivalBranch.Store(targetBranch)
	var injectPromotionIdentityConflict atomic.Bool
	var removeSecondaryOnArrival atomic.Bool
	var arrivalDelay atomic.Int64
	var changeConstitutionOnArrival atomic.Bool
	var scopedReader, relinkFirstOnSecondArrival atomic.Bool
	var diagnosedArrival atomic.Bool
	var scopedTokensMinted atomic.Int64
	var legacyArrivalFailure, hideDiagnosticAlternatives, diagnosticPrimaryArrived atomic.Bool
	var overlap atomic.Pointer[fixtureArrivalBarrier]
	const scopedFixtureBearer = "scoped-fixture-reader-proof-bearer-01234567"
	editConstitution := func(mode, statement, operationID string) error {
		current, err := store.ReadCanonical(ctx, companyID, projectID, FamilyConstitution, constitutionID)
		if err != nil {
			return err
		}
		current.Payload = json.RawMessage(fmt.Sprintf(`{"promotion_mode":%q,"capability_namespaces":["go"],"rules":[{"id":"authority","status":"current","statement":%q}]}`, mode, statement))
		_, err = store.EditCanonical(ctx, companyID, CanonicalMutation{
			ProjectID: projectID, Family: FamilyConstitution, Record: current, ExpectedRevision: current.Revision,
			OperationID: operationID, Actor: "fixture-owner",
		})
		return err
	}
	reader := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		readerCalls.Add(1)
		readerBearer := "fixture-token"
		if scopedReader.Load() {
			readerBearer = scopedFixtureBearer
			wantToken := "repo-a-token"
			if r.URL.Query().Get("repository") == secondaryRepository {
				wantToken = "repo-b-token"
			}
			if r.Header.Get(githubScopedProofTokenHeader) != wantToken {
				t.Error("Reader did not receive the exact repository's GitHub App credential")
			}
		}
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer "+readerBearer {
			t.Error("Reader request must be an authenticated GET")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		q := r.URL.Query()
		if r.URL.Path == "/internal/v1/proofs/arrival" {
			if delay := time.Duration(arrivalDelay.Load()); delay > 0 {
				time.Sleep(delay)
			}
			requestedBranch := q.Get("branch")
			arrivalRepository := q.Get("repository")
			wantedCommits := commit
			if arrivalRepository == secondaryRepository {
				wantedCommits = secondaryCommit
			}
			if legacyArrivalFailure.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(`{"state":"unavailable","teach":"TEACH: target Git branch head is unavailable"}`))
				return
			}
			if diagnosedArrival.Load() {
				if q.Get("company_id") != companyID || q.Get("project_id") != projectID || q.Get("commit_shas") != wantedCommits ||
					q.Get("todo_authority") != "mongo-v2" || (arrivalRepository != repository && arrivalRepository != secondaryRepository) ||
					(requestedBranch != targetBranch && requestedBranch != branch && (arrivalRepository != secondaryRepository || requestedBranch != "dev")) {
					t.Errorf("unexpected diagnostic request: %s", r.URL)
				}
				node := "R_kgDOImmutable"
				if arrivalRepository == secondaryRepository {
					node += "-secondary"
				}
				if requestedBranch == targetBranch && (arrivalRepository == secondaryRepository || !diagnosticPrimaryArrived.Load()) {
					status, missing := "commit_not_reachable", arrivalRepository == secondaryRepository
					observedHead := heads[requestedBranch]
					if missing {
						status, observedHead = "unchecked", ""
					}
					w.WriteHeader(http.StatusServiceUnavailable)
					_ = json.NewEncoder(w).Encode(map[string]any{"teach": "remote branch not verified", "company_id": companyID, "project_id": projectID, "head_sha": observedHead, "repository": arrivalRepository, "repository_node_id": node, "branch": requestedBranch, "branch_not_found": missing, "observations": []map[string]string{{"commit_sha": wantedCommits, "status": status}}})
				} else if hideDiagnosticAlternatives.Load() {
					w.WriteHeader(http.StatusServiceUnavailable)
					_, _ = w.Write([]byte(`{"state":"unavailable","teach":"GitHub Contents is not available"}`))
				} else {
					_ = json.NewEncoder(w).Encode(map[string]any{"company_id": companyID, "project_id": projectID, "repository": arrivalRepository, "repository_node_id": node, "branch": requestedBranch, "head_sha": heads[branch], "commits": []map[string]any{{"commit_sha": wantedCommits, "reachable": true}}, "outcome": "verified"})
				}
				return
			}
			if gate := overlap.Load(); gate != nil && arrivalRepository == secondaryRepository {
				if gate.arrived.Add(1) == 2 {
					close(gate.ready)
				}
				select {
				case <-gate.ready:
				case <-r.Context().Done():
					return
				}
			}
			if q.Get("company_id") != companyID || q.Get("project_id") != projectID || arrivalRepository != repository && arrivalRepository != secondaryRepository || q.Get("todo_authority") != "mongo-v2" || requestedBranch != expectedArrivalBranch.Load().(string) || q.Get("commit_shas") != wantedCommits {
				t.Errorf("Reader received wrong Complete arrival query: %s", r.URL)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if arrivalRepository == secondaryRepository && relinkFirstOnSecondArrival.CompareAndSwap(true, false) {
				first := githubRepositoryConnection{ProjectID: projectID, GitHubRepositoryID: 33, RepositoryNodeID: "R_kgDOImmutable",
					RepositoryFullName: repository, AccountID: 18, InstallationID: 10}
				if err := writeGitHubRepositoryConnectionCAS(ctx, database, first, 1, time.Now().UTC()); err != nil {
					t.Errorf("relink first repository during second Reader request: %v", err)
				}
			}
			if removeSecondaryOnArrival.CompareAndSwap(true, false) {
				set, readErr := store.ReadImplementationRepositories(ctx, companyID, projectID)
				if readErr != nil {
					t.Errorf("read implementation membership for concurrent removal: %v", readErr)
				} else {
					_, removeErr := store.ChangeImplementationRepository(ctx, companyID, projectID, ImplementationRepositoryChange{
						Action: "remove", ProjectID: projectID, ExpectedRevision: set.Revision,
						OperationID: "concurrent-proof-membership-removal", Actor: "fixture-owner", OwnerReference: "fixture-owner",
						RepositoryID: secondaryRepository,
					})
					if removeErr != nil {
						t.Errorf("concurrently remove verified alias: %v", removeErr)
					}
				}
			}
			if changeConstitutionOnArrival.CompareAndSwap(true, false) {
				if err := editConstitution("mvp", "Owner changed policy during Git verification", "concurrent-constitution-mode-change"); err != nil {
					t.Errorf("change the fallback constitution during Reader proof: %v", err)
				}
			}
			actualRef := "refs/heads/" + requestedBranch
			headOutput, err := exec.Command("git", "--git-dir", gitDir, "rev-parse", "--verify", actualRef).CombinedOutput()
			targetSHA := strings.TrimSpace(string(headOutput))
			if err != nil || !validGitCommit(targetSHA) {
				t.Errorf("read fixture branch %s: %v: %s", requestedBranch, err, headOutput)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			git, err := exec.Command("git", "--git-dir", gitDir, "merge-base", "--is-ancestor", wantedCommits, targetSHA).CombinedOutput()
			if err != nil {
				var exited *exec.ExitError
				if !errors.As(err, &exited) || exited.ExitCode() != 1 {
					t.Errorf("verify fixture ancestry: %v: %s", err, git)
				}
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(`{"teach":"original commit has not arrived on the selected branch"}`))
				return
			}
			repoCommits := []map[string]any{{"commit_sha": wantedCommits, "reachable": true}}
			nodeID := "R_kgDOImmutable"
			if arrivalRepository == secondaryRepository {
				nodeID += "-secondary"
			}
			if arrivalIdentityMismatch.Load() {
				nodeID += "-retargeted"
			}
			if injectPromotionIdentityConflict.Load() && arrivalRepository == secondaryRepository {
				_, insertErr := database.Collection(implementationRepositoryIdentityCollection).InsertOne(ctx, implementationRepositoryIdentity{ProjectID: projectID, RepositoryID: secondaryRepository, GitHubNodeID: "R_kgDOPromotionConflict", ProofDigest: sourceHash([]byte("fixture")), PinnedAt: time.Now().UTC()})
				if insertErr != nil {
					t.Errorf("seed concurrent promotion identity conflict: %v", insertErr)
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"company_id": companyID, "project_id": projectID, "repository": arrivalRepository, "repository_node_id": nodeID, "branch": requestedBranch, "head_sha": targetSHA, "commits": repoCommits, "outcome": "verified"})
			return
		}
		if r.URL.Path != "/internal/v1/proofs/commit" {
			t.Errorf("unexpected Reader route: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		localRepository, localCommit, nodeID := repository, commit, "R_kgDOImmutable"
		if q.Get("repository") == secondaryRepository {
			localRepository, localCommit, nodeID = secondaryRepository, secondaryCommit, "R_kgDOImmutable-secondary"
		}
		if q.Get("company_id") != companyID || q.Get("project_id") != projectID || q.Get("repository") != localRepository || q.Get("branch") != branch || q.Get("commit_sha") != localCommit || q.Get("baseline_sha") != baseline || q.Get("todo_authority") != "mongo-v2" {
			t.Errorf("Reader received wrong scoped proof query: %s", r.URL)
		}
		if !reachableOnRelease.Load() {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"teach":"commit is reachable only on dev, not the confirmed Release branch"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(remoteCommitResult{CompanyID: companyID, ProjectID: projectID, Repository: localRepository, Branch: branch, CommitSHA: localCommit, BaselineSHA: baseline, ParentSHA: baseline, RemoteHead: head, Paths: []string{"go-app/internal/registry/todo_access.go"}, RepositoryNodeID: nodeID, Outcome: "verified"})
	}))
	defer reader.Close()
	store.proofReaderURL, store.proofReaderToken, store.proofHTTPClient = reader.URL, "fixture-token", reader.Client()
	current, err = store.ReadTODO(ctx, companyID, projectID, todoID, nil)
	if err != nil || current.TODO.Stage != "In-Progress" {
		t.Fatalf("fixture predecessor: %+v %v", current, err)
	}
	criterion := TODOProofReference{Kind: "criterion:criterion-one", Source: "external-evidence", Reference: "fixture/criterion", SourceDigest: edit.ContentDigest, VerificationState: "unverified"}
	scenario := TODOProofReference{Kind: "scenario:scenario-one", Source: "external-evidence", Reference: "fixture/scenario", SourceDigest: edit.ContentDigest, VerificationState: "unverified"}
	finalReview := TODOProofReference{Kind: "final_review", Source: "human-review", Reference: "review/initial-final", SourceDigest: edit.ContentDigest, VerificationState: "unverified", Outcome: "go"}
	claim := TODOCommitReference{Repository: "Builder", Commit: commit, SourceDigest: edit.ContentDigest, VerificationState: "unverified"}
	secondaryClaim := TODOCommitReference{Repository: "Secondary", Commit: secondaryCommit, SourceDigest: edit.ContentDigest, VerificationState: "unverified"}
	revision := current.TODORevision
	stale := criterion
	stale.SourceDigest = sourceHash([]byte("older content"))
	if _, err := store.EditTODOEvidence(ctx, companyID, TODOEvidenceEditRequest{ProjectID: projectID, TODOID: todoID, ExpectedRevision: &revision, OperationID: "stale-citations", ProofReferences: []TODOProofReference{stale, scenario}, WaiverReferences: []TODOProofReference{}, ImplementationCommits: []TODOCommitReference{claim, secondaryClaim}, RecordSections: []TODOSection{}, Reason: "Exercise stale citation rejection"}); err != nil {
		t.Fatal(err)
	}
	revision++
	request := TODOActionRequest{ProjectID: projectID, TODOID: todoID, ExpectedRevision: &revision, OperationID: "reject-stale", RequestedStage: "Local-Implemented"}
	if _, err := store.TransitionTODO(ctx, companyID, request); err == nil || !strings.Contains(err.Error(), "proof_refs.criterion:criterion-one") || readerCalls.Load() != 0 {
		t.Fatalf("stale citation reached Reader or stage: calls=%d err=%v", readerCalls.Load(), err)
	}
	if _, err := store.EditTODOEvidence(ctx, companyID, TODOEvidenceEditRequest{ProjectID: projectID, TODOID: todoID, ExpectedRevision: &revision, OperationID: "current-citations", ProofReferences: []TODOProofReference{criterion, scenario, finalReview}, WaiverReferences: []TODOProofReference{}, ImplementationCommits: []TODOCommitReference{claim, secondaryClaim}, RecordSections: []TODOSection{}, Reason: "Set current external citations"}); err != nil {
		t.Fatal(err)
	}
	revision++
	request.ExpectedRevision, request.OperationID = &revision, "reject-inline-local-proof"
	request.LocalProof = &LocalProofResult{SchemaVersion: "builder-local-proof-result-v3"}
	if _, err := store.TransitionTODO(ctx, companyID, request); err == nil || !strings.Contains(err.Error(), "local_proof/proof_refs/waiver_refs") || readerCalls.Load() != 0 {
		t.Fatalf("inline local proof reached Reader or stage: calls=%d err=%v", readerCalls.Load(), err)
	}
	request.LocalProof, request.OperationID = nil, "reject-dev-only-commit"
	if _, err := store.TransitionTODO(ctx, companyID, request); err == nil || !strings.Contains(err.Error(), "remote_proof") || readerCalls.Load() != 1 {
		t.Fatalf("commit reachable only on dev advanced the Release delivery: calls=%d err=%v", readerCalls.Load(), err)
	}
	stillInProgress, err := store.ReadTODO(ctx, companyID, projectID, todoID, nil)
	if err != nil || stillInProgress.TODO.Stage != "In-Progress" || stillInProgress.TODORevision != revision {
		t.Fatalf("dev-only proof changed the TODO stage: %+v %v", stillInProgress, err)
	}
	reachableOnRelease.Store(true)
	company, err := store.company(ctx, companyID)
	if err != nil {
		t.Fatal(err)
	}
	database = store.client.Database(company.DatabaseName)
	firstAlias, conflictingAlias := repository, secondaryRepository
	_, err = database.Collection(implementationRepositoryIdentityCollection).InsertOne(ctx, implementationRepositoryIdentity{ProjectID: projectID, RepositoryID: conflictingAlias, GitHubNodeID: "R_kgDOConflict", ProofDigest: sourceHash([]byte("fixture")), PinnedAt: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	request.OperationID = "reject-partial-identity-pin"
	if _, err := store.TransitionTODO(ctx, companyID, request); err == nil || !strings.Contains(err.Error(), "github_node_id") {
		t.Fatalf("conflicting second alias did not reject Local transition: %v", err)
	}
	var partialPin implementationRepositoryIdentity
	if err := database.Collection(implementationRepositoryIdentityCollection).FindOne(ctx, bson.D{{Key: "project_id", Value: projectID}, {Key: "repository_id", Value: firstAlias}}).Decode(&partialPin); !errors.Is(err, mongo.ErrNoDocuments) {
		t.Fatalf("first alias pin survived failed atomic transition: %+v err=%v", partialPin, err)
	}
	if _, err := database.Collection(implementationRepositoryIdentityCollection).DeleteOne(ctx, bson.D{{Key: "project_id", Value: projectID}, {Key: "repository_id", Value: conflictingAlias}}); err != nil {
		t.Fatal(err)
	}
	request.OperationID = "remote-code-delivery"
	if _, err := store.TransitionTODO(ctx, companyID, request); err != nil {
		t.Fatal(err)
	}
	actual, err := store.ReadTODO(ctx, companyID, projectID, todoID, nil)
	if err != nil || actual.TODO.Stage != "Local-Implemented" || actual.TODO.StageVerification != "verified" || actual.TODO.VerifiedTransitionRef == nil || actual.TODO.VerifiedTransitionRef.Kind != "remote-code-delivery" || actual.TODO.VerifiedTransitionRef.Source != "private-reader" || actual.TODO.VerifiedTransitionRef.VerificationState != "verified" || actual.TODORevision != revision+1 || readerCalls.Load() != 5 {
		t.Fatalf("API-only transition/readback: %+v calls=%d err=%v", actual, readerCalls.Load(), err)
	}
	if actual.TODO.ProofReferences[0].VerificationState != "unverified" || actual.TODO.ProofReferences[1].VerificationState != "unverified" {
		t.Fatalf("external citations were promoted to verified execution: %+v", actual.TODO.ProofReferences)
	}
	oldProof := *actual.TODO.VerifiedTransitionRef
	project, err := store.Project(ctx, companyID, projectID)
	if err != nil {
		t.Fatal(err)
	}
	roadmapRevision := project.RoadmapRevision
	if _, err := store.EditTODOPlacement(ctx, companyID, TODOPlacementEditRequest{ProjectID: projectID, TODOID: todoID, ExpectedRevision: &actual.TODORevision, ExpectedRoadmapRevision: &roadmapRevision, OperationID: "move-delivered-todo", ReleaseID: "r-08", Position: 1, Reason: "Change the delivered placement"}); err != nil {
		t.Fatal(err)
	}
	moved, err := store.ReadTODO(ctx, companyID, projectID, todoID, nil)
	if err != nil || moved.TODO.Stage != "Planning" || moved.TODO.Position == nil || *moved.TODO.Position != 1 || moved.TODO.VerifiedTransitionRef == nil || moved.TODO.VerifiedTransitionRef.Kind != "planning-stage" || moved.TODO.VerifiedTransitionRef.Reference == oldProof.Reference {
		t.Fatalf("placement retained old delivery authority: %+v err=%v", moved, err)
	}
	for _, ref := range moved.TODO.ProofReferences {
		if ref.Source == "human-review" && (strings.HasPrefix(ref.Kind, "planning_review_") || isTODOAuditDeliveryKind(ref.Kind)) {
			t.Fatalf("placement retained old human review: %+v", ref)
		}
	}
	for _, stage := range []string{"Completed", "Local-Implemented", "Implementation-Approval-Pending"} {
		request := TODOActionRequest{ProjectID: projectID, TODOID: todoID, ExpectedRevision: &moved.TODORevision, OperationID: "reject-old-proof-" + stage, RequestedStage: stage}
		if _, err := store.TransitionTODO(ctx, companyID, request); err == nil {
			t.Fatalf("old delivery/review authorized %s after placement move", stage)
		}
	}
	refreshed := contentFromTODO(moved.TODO)
	refreshed.Planning.Reviews[0].Reference = "review/critique-after-move"
	edit, err = store.EditTODOContent(ctx, companyID, TODOContentEditRequest{ProjectID: projectID, TODOID: todoID, ExpectedRevision: &moved.TODORevision, OperationID: "review-moved-content", Content: refreshed, Reason: "Review the changed placement"})
	if err != nil {
		t.Fatal(err)
	}
	current, err = store.ReadTODO(ctx, companyID, projectID, todoID, nil)
	if err != nil {
		t.Fatal(err)
	}
	newDigest := edit.ContentDigest
	if _, err := store.TransitionTODO(ctx, companyID, TODOActionRequest{ProjectID: projectID, TODOID: todoID, ExpectedRevision: &current.TODORevision, OperationID: "reject-unreviewed-move", RequestedStage: "Implementation-Approval-Pending"}); err == nil || !strings.Contains(err.Error(), "planning_review_critique") {
		t.Fatalf("changed placement skipped current review: %v", err)
	}
	revision = recordPlanningReviewSubjects(t, ctx, store, projectID, todoID, "fresh-placement-review")
	for _, step := range []struct{ stage, operation string }{{"Implementation-Approval-Pending", "review-moved-planning"}, {"Ready-for-Implementation", "approve-moved-delivery"}, {"In-Progress", "start-moved-delivery"}} {
		request := TODOActionRequest{ProjectID: projectID, TODOID: todoID, ExpectedRevision: &revision, OperationID: step.operation, RequestedStage: step.stage}
		if step.stage == "Ready-for-Implementation" {
			request.ProofReferences = []TODOProofReference{{Kind: "owner-approval", Source: "owner-chat", Reference: "APROVADO", SourceDigest: newDigest, VerificationState: "unverified"}}
		}
		if _, err := store.TransitionTODO(ctx, companyID, request); err != nil {
			t.Fatalf("%s after placement review: %v", step.stage, err)
		}
		revision++
	}
	criterion.SourceDigest, scenario.SourceDigest, claim.SourceDigest, secondaryClaim.SourceDigest, finalReview.SourceDigest = newDigest, newDigest, newDigest, newDigest, newDigest
	finalReview.Reference = "review/final-after-move"
	if _, err := store.EditTODOEvidence(ctx, companyID, TODOEvidenceEditRequest{ProjectID: projectID, TODOID: todoID, ExpectedRevision: &revision, OperationID: "fresh-moved-citations", ProofReferences: []TODOProofReference{criterion, scenario, finalReview}, WaiverReferences: []TODOProofReference{}, ImplementationCommits: []TODOCommitReference{claim, secondaryClaim}, RecordSections: []TODOSection{}, Reason: "Cite the reviewed moved delivery"}); err != nil {
		t.Fatal(err)
	}
	revision++
	if _, err := store.TransitionTODO(ctx, companyID, TODOActionRequest{ProjectID: projectID, TODOID: todoID, ExpectedRevision: &revision, OperationID: "fresh-moved-delivery", RequestedStage: "Local-Implemented"}); err != nil {
		t.Fatalf("fresh Reader verification after placement review: %v", err)
	}
	redelivered, err := store.ReadTODO(ctx, companyID, projectID, todoID, nil)
	if err != nil || redelivered.TODO.Stage != "Local-Implemented" || redelivered.TODO.VerifiedTransitionRef == nil || redelivered.TODO.VerifiedTransitionRef.Kind != "remote-code-delivery" || redelivered.TODO.VerifiedTransitionRef.VerificationState != "verified" || redelivered.ContentDigest != newDigest || readerCalls.Load() != 7 {
		t.Fatalf("fresh delivery after moved placement: stage=%s proof=%+v digest=%s calls=%d err=%v", redelivered.TODO.Stage, redelivered.TODO.VerifiedTransitionRef, newDigest, readerCalls.Load(), err)
	}
	if !legacyProof {
		originalLocalProof := *redelivered.TODO.VerifiedTransitionRef
		if !strings.Contains(originalLocalProof.Reference, "repository_node_id=") || strings.Contains(originalLocalProof.Reference, todoConstitutionPinPrefix) {
			t.Fatalf("new Local proof lacks repository identity or embeds a policy pin: %+v", originalLocalProof)
		}
		promotion := TODOActionRequest{ProjectID: projectID, TODOID: todoID, ExpectedRevision: &redelivered.TODORevision, OperationID: "current-proof-" + targetBranch + "-promotion", RequestedStage: "Promotion-Lane", PromotionTargets: map[string]string{repository: targetBranch, secondaryRepository: targetBranch}}
		if targetBranch == "stage" {
			if _, err := store.TransitionTODO(ctx, companyID, promotion); err == nil || !strings.Contains(err.Error(), "builder-project set-promotion-mode") {
				t.Fatalf("missing policy did not give actionable TEACH: %v", err)
			}
			configureFixturePromotionMode(t, ctx, store, companyID, projectID, "complete", 0, "set-explicit-complete")
		} else {
			changeConstitutionOnArrival.Store(true)
			if _, err := store.TransitionTODO(ctx, companyID, promotion); err == nil || !strings.Contains(err.Error(), "promotion mode changed during proof verification") {
				t.Fatalf("changed canonical policy published stale Git proof: %v", err)
			}
			if err := editConstitution("complete", "Owner restored Complete", "restore-canonical-mode"); err != nil {
				t.Fatal(err)
			}
		}
		refused, err := store.ReadTODO(ctx, companyID, projectID, todoID, nil)
		if err != nil || refused.TODORevision != redelivered.TODORevision || *refused.TODO.VerifiedTransitionRef != originalLocalProof {
			t.Fatalf("mode refusal changed original Local proof: %+v err=%v", refused, err)
		}
		registry := httptest.NewServer(NewHandler(store))
		defer registry.Close()

		// An otherwise valid deployment claim cannot bypass Complete promotion.
		mvpAttempt := TODOActionRequest{
			ProjectID: projectID, TODOID: todoID, ExpectedRevision: &redelivered.TODORevision,
			OperationID: "reject-mvp-on-complete", RequestedStage: "Completed",
			ProofReferences: []TODOProofReference{{
				Kind: "mvp-deployment-claim", Source: "github-deployment", Reference: "17@" + heads["main"],
				SourceDigest: redelivered.ContentDigest, VerificationState: "unverified",
			}},
		}
		callsBeforeMVP := readerCalls.Load()
		if code, body := submitFixturePromotionHTTP(t, registry, companyID, mvpAttempt); code != http.StatusConflict ||
			!strings.Contains(string(body), "then Promotion-Lane to Completed") || readerCalls.Load() != callsBeforeMVP {
			t.Fatalf("Complete accepted direct MVP completion: status=%d body=%s", code, body)
		}

		store.githubApp = githubAppTestClient(t, func(r *http.Request) (*http.Response, error) {
			switch r.URL.Path {
			case "/app/installations/9/access_tokens", "/app/installations/10/access_tokens":
				scopedTokensMinted.Add(1)
				var scope struct {
					RepositoryIDs []int64 `json:"repository_ids"`
				}
				if err := json.NewDecoder(r.Body).Decode(&scope); err != nil || len(scope.RepositoryIDs) != 1 {
					return nil, ErrInvalid
				}
				token := "repo-a-token"
				if scope.RepositoryIDs[0] == 34 {
					token = "repo-b-token"
				}
				return githubAppTestResponse(201, fmt.Sprintf(`{"token":%q,"expires_at":"2026-11-25T01:00:00Z"}`, token)), nil
			case "/repositories/33":
				return githubAppTestResponse(200, `{"id":33,"node_id":"R_kgDOImmutable","full_name":"owner/repo","owner":{"id":18}}`), nil
			case "/repositories/34":
				return githubAppTestResponse(200, `{"id":34,"node_id":"R_kgDOImmutable-secondary","full_name":"owner/secondary","owner":{"id":18}}`), nil
			default:
				t.Errorf("unexpected GitHub lookup %s", r.URL.Path)
				return nil, ErrUnavailable
			}
		})
		if err := store.ConfigureScopedProofReader(scopedFixtureBearer); err != nil {
			t.Fatal(err)
		}
		for _, conn := range []githubRepositoryConnection{
			{ProjectID: projectID, GitHubRepositoryID: 33, RepositoryNodeID: "R_kgDOImmutable", RepositoryFullName: repository, AccountID: 18, InstallationID: 9},
			{ProjectID: projectID, GitHubRepositoryID: 34, RepositoryNodeID: "R_kgDOImmutable-secondary", RepositoryFullName: secondaryRepository, AccountID: 18, InstallationID: 9},
		} {
			if err := writeGitHubRepositoryConnectionCAS(ctx, database, conn, 0, time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
		}
		scopedReader.Store(true)
		relinkFirstOnSecondArrival.Store(true)
		relinkAttempt := promotion
		relinkAttempt.OperationID = "reject-concurrent-installation-change"
		callsBeforeRelink := readerCalls.Load()
		relinkCode, relinkBody := submitFixturePromotionHTTP(t, registry, companyID, relinkAttempt)
		if relinkCode != http.StatusConflict || !strings.Contains(string(relinkBody), "reload the current Project repository confirmation and connection") ||
			!strings.Contains(string(relinkBody), "repository_connection_changed") || readerCalls.Load()-callsBeforeRelink != 2 || relinkFirstOnSecondArrival.Load() {
			t.Fatalf("promotion did not reject stale connection after both proofs: status=%d body=%s", relinkCode, relinkBody)
		}
		afterRelink, err := store.ReadTODO(ctx, companyID, projectID, todoID, nil)
		if err != nil || afterRelink.TODORevision != redelivered.TODORevision || afterRelink.TODO.Stage != "Local-Implemented" {
			t.Fatalf("rejected promotion mutated the TODO: %+v err=%v", afterRelink, err)
		}

		// Match PACED's code-promotion payload with empty proof and waiver arrays.
		promotion.ProofReferences, promotion.WaiverReferences = []TODOProofReference{}, []TODOProofReference{}
		if targetBranch == "stage" {
			legacyAttempt := promotion
			legacyAttempt.OperationID = "legacy-reader-teach-without-observations"
			legacyArrivalFailure.Store(true)
			callsBeforeLegacy := readerCalls.Load()
			legacyCode, legacyBody := submitFixturePromotionHTTP(t, registry, companyID, legacyAttempt)
			legacyArrivalFailure.Store(false)
			if legacyCode != http.StatusConflict || !strings.Contains(string(legacyBody), "TEACH: target Git branch head is unavailable") ||
				strings.Contains(string(legacyBody), `"promotion":`) || readerCalls.Load()-callsBeforeLegacy != 1 {
				t.Fatalf("legacy Reader error changed: code=%d body=%s", legacyCode, legacyBody)
			}
			diagnosedArrival.Store(true)
			diagnosticAttempt := promotion
			diagnosticAttempt.OperationID = "diagnose-branches-at-stage"
			callsBeforeDiagnostic, tokensBeforeDiagnostic := readerCalls.Load(), scopedTokensMinted.Load()
			code, body := submitFixturePromotionHTTP(t, registry, companyID, diagnosticAttempt)
			diagnosedArrival.Store(false)
			var response errorResponse
			if json.Unmarshal(body, &response) != nil || code != http.StatusConflict || response.TeachDetail == nil ||
				response.TeachDetail.Code != "promotion_evidence_incomplete" || response.TeachDetail.Effects != TeachEffectsNone ||
				readerCalls.Load()-callsBeforeDiagnostic != 4 || scopedTokensMinted.Load()-tokensBeforeDiagnostic != 2 {
				t.Fatalf("unexpected diagnostic or probe budget: %d %s; calls=%d", code, body, readerCalls.Load()-callsBeforeDiagnostic)
			}
			detail := response.TeachDetail.Promotion
			if detail == nil || detail.RequestedStage != "Promotion-Lane" || len(detail.Repositories) != 2 ||
				detail.Repositories[0].RepositoryID != repository || detail.Repositories[0].SourceBranch != branch ||
				detail.Repositories[0].Status != "commit_not_reachable" || detail.Repositories[1].Status != "branch_missing" ||
				len(detail.Repositories[1].Alternatives) != 1 || detail.Repositories[1].Alternatives[0].Branch != "dev" ||
				detail.Repositories[1].Alternatives[0].CommitReachable == nil || !*detail.Repositories[1].Alternatives[0].CommitReachable ||
				!detail.Repositories[1].Alternatives[0].OwnerConfirmationRequired {
				t.Fatalf("incomplete diagnosis: %s", body)
			}
			diagnosedArrival.Store(true)
			hideDiagnosticAlternatives.Store(true)
			diagnosticAttempt.OperationID = "diagnose-stage-without-proven-contents"
			code, hidden := submitFixturePromotionHTTP(t, registry, companyID, diagnosticAttempt)
			diagnosedArrival.Store(false)
			hideDiagnosticAlternatives.Store(false)
			if json.Unmarshal(hidden, &response) != nil || code != http.StatusConflict || response.TeachDetail == nil || response.TeachDetail.Promotion == nil {
				t.Fatalf("unavailable branch should retain a typed diagnostic: %d %s", code, hidden)
			}
			unknown := response.TeachDetail.Promotion.Repositories[1]
			if unknown.Status != "observation_unavailable" || len(unknown.Alternatives) != 1 || unknown.Alternatives[0].Exists != nil ||
				unknown.Alternatives[0].CommitReachable != nil {
				t.Fatalf("unobserved ref was treated as absent or known: %s", hidden)
			}
			diagnosedArrival.Store(true)
			diagnosticPrimaryArrived.Store(true)
			diagnosticAttempt.OperationID = "diagnose-mixed-verified-and-blocked"
			code, mixed := submitFixturePromotionHTTP(t, registry, companyID, diagnosticAttempt)
			diagnosedArrival.Store(false)
			diagnosticPrimaryArrived.Store(false)
			if json.Unmarshal(mixed, &response) != nil || code != http.StatusConflict || response.TeachDetail == nil ||
				response.TeachDetail.Promotion == nil || len(response.TeachDetail.Promotion.Repositories) != 2 ||
				response.TeachDetail.Promotion.Repositories[0].Status != "verified" ||
				response.TeachDetail.Promotion.Repositories[1].Status != "branch_missing" {
				t.Fatalf("verified repository was lost in a partial failure: %d %s", code, mixed)
			}
			stillLocal, err := store.ReadTODO(ctx, companyID, projectID, todoID, nil)
			if err != nil || stillLocal.TODORevision != redelivered.TODORevision {
				t.Fatalf("diagnostic advanced TODO: %+v %v", stillLocal, err)
			}
			if output := os.Getenv("BUILDER_TEACH_HTTP_FIXTURE"); output != "" {
				if err := os.WriteFile(output, body, 0644); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(output, 0644); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(filepath.Dir(output), "registry-relink.json"), relinkBody, 0644); err != nil {
					t.Fatal(err)
				}
			}
			peer := admitConcurrentPromotionFixture(t, ctx, store, companyID, projectID, record, refreshed, claim, secondaryClaim, criterion, scenario, finalReview)
			peerPromotion := promotion
			peerPromotion.TODOID, peerPromotion.OperationID, peerPromotion.ExpectedRevision = peer.TODO.ID, "promote-peer-overlap", &peer.TODORevision
			setBefore, err := store.ReadImplementationRepositories(ctx, companyID, projectID)
			if err != nil {
				t.Fatal(err)
			}
			overlap.Store(&fixtureArrivalBarrier{ready: make(chan struct{})})
			callsBeforeOverlap := readerCalls.Load()
			result := make(chan error, 2)
			for _, attempt := range []TODOActionRequest{promotion, peerPromotion} {
				go func(request TODOActionRequest) {
					response, err := store.TransitionTODO(ctx, companyID, request)
					if err == nil && (response.Stage != "Promotion-Lane" || response.StageVerification != "verified") {
						err = fmt.Errorf("unexpected verified transition: %+v", response)
					}
					result <- err
				}(attempt)
			}
			for range 2 {
				select {
				case err := <-result:
					if err != nil {
						t.Errorf("overlapping promotions with stable bindings: %v", err)
					}
				case <-time.After(20 * time.Second):
					t.Fatal("both promotions did not reach the Reader barrier")
				}
			}
			overlap.Store(nil)
			setAfter, err := store.ReadImplementationRepositories(ctx, companyID, projectID)
			if err != nil || setBefore.Revision != setAfter.Revision || readerCalls.Load()-callsBeforeOverlap != 4 {
				t.Fatalf("overlap changed public revision or grew success probes: before=%d after=%d calls=%d err=%v", setBefore.Revision, setAfter.Revision, readerCalls.Load()-callsBeforeOverlap, err)
			}
		} else {
			assertFixturePromotionHTTP(t, registry, companyID, promotion, "Promotion-Lane")
		}
		promoted, err := store.ReadTODO(ctx, companyID, projectID, todoID, nil)
		if err != nil || promoted.TODO.Stage != "Promotion-Lane" || promoted.TODO.VerifiedTransitionRef == nil || promoted.TODO.VerifiedTransitionRef.Kind != "complete-git-arrival" {
			t.Fatalf("current Complete %s promotion readback: %+v err=%v", targetBranch, promoted, err)
		}
		assertFixtureArrival(t, *promoted.TODO.VerifiedTransitionRef, "promotion", targetBranch, heads[targetBranch])

		expectedArrivalBranch.Store("main")
		setPromotionGitRef(t, gitDir, "main", heads["main"])
		completed := TODOActionRequest{ProjectID: projectID, TODOID: todoID, ExpectedRevision: &promoted.TODORevision, OperationID: "current-proof-main-arrival", RequestedStage: "Completed"}
		if targetBranch == "dev" {
			completed.ProofReferences, completed.WaiverReferences = []TODOProofReference{}, []TODOProofReference{}
		}
		assertFixturePromotionHTTP(t, registry, companyID, completed, "Completed")
		final, err := store.ReadTODO(ctx, companyID, projectID, todoID, nil)
		if err != nil || final.TODO.Stage != "Completed" || final.TODO.VerifiedTransitionRef == nil || final.TODO.VerifiedTransitionRef.Kind != "complete-git-arrival" || !strings.Contains(final.TODO.VerifiedTransitionRef.Reference, `"arrival_stage":"main"`) || !strings.Contains(final.TODO.VerifiedTransitionRef.Reference, commit) || !strings.Contains(final.TODO.VerifiedTransitionRef.Reference, secondaryCommit) {
			t.Fatalf("current Complete main readback lost original commits: %+v err=%v", final, err)
		}
		assertFixtureArrival(t, *final.TODO.VerifiedTransitionRef, "main", "main", heads["main"])
		localEventFilter := bson.D{{Key: "project_id", Value: projectID}, {Key: "todo_id", Value: todoID}, {Key: "kind", Value: "state_changed"}, {Key: "revision", Value: redelivered.TODORevision}, {Key: "changes.stage", Value: "Local-Implemented"}}
		var preservedLocalEvent TODOHistoryEvent
		if err := database.Collection(todoEventsCollection).FindOne(ctx, localEventFilter).Decode(&preservedLocalEvent); err != nil || preservedLocalEvent.Changes.VerifiedTransitionRef == nil || *preservedLocalEvent.Changes.VerifiedTransitionRef != originalLocalProof {
			t.Fatalf("promotion rewrote the current Local proof: %+v err=%v", preservedLocalEvent, err)
		}
		identities, err := readImplementationRepositoryNodeIDs(ctx, database, projectID)
		if err != nil || identities[repository] != "R_kgDOImmutable" || identities[secondaryRepository] != "R_kgDOImmutable-secondary" {
			t.Fatalf("current proof lost immutable repository identities: %+v err=%v", identities, err)
		}
		return
	}
	legacyLocalProof := *redelivered.TODO.VerifiedTransitionRef
	legacyItems := make([]string, 0)
	for _, item := range strings.Split(legacyLocalProof.Reference, ";") {
		if strings.HasPrefix(item, "repository_node_id=") || strings.HasPrefix(item, todoConstitutionPinPrefix) {
			continue
		}
		legacyItems = append(legacyItems, item)
	}
	legacyLocalProof.Reference = strings.Join(legacyItems, ";")
	if targetBranch == "stage" {
		pin := todoConstitutionPin{
			Mode: "mvp", GenerationID: "old-constitution-generation", RecordID: constitutionID, Revision: 1,
			ContentDigest: sourceHash([]byte("original MVP constitution")),
			Repositories:  todoProofRepositoryNodeIDs(redelivered.TODO.VerifiedTransitionRef.Reference),
		}
		pinJSON, err := json.Marshal(pin)
		if err != nil {
			t.Fatal(err)
		}
		legacyLocalProof.Reference += ";" + todoConstitutionPinPrefix + string(pinJSON)
		if err := editConstitution("mvp", "Legacy mode before correction", "before-explicit-correction"); err != nil {
			t.Fatal(err)
		}
		configureFixturePromotionMode(t, ctx, store, companyID, projectID, "complete", 0, "explicit-mode-correction")
	}
	if _, err := database.Collection(todosCollection).UpdateOne(ctx, bson.D{{Key: "project_id", Value: projectID}, {Key: "id", Value: todoID}}, bson.D{{Key: "$set", Value: bson.D{{Key: "verified_transition_ref", Value: legacyLocalProof}}}}); err != nil {
		t.Fatal(err)
	}
	localEventFilter := bson.D{{Key: "project_id", Value: projectID}, {Key: "todo_id", Value: todoID}, {Key: "kind", Value: "state_changed"}, {Key: "revision", Value: redelivered.TODORevision}, {Key: "changes.stage", Value: "Local-Implemented"}}
	eventUpdate, err := database.Collection(todoEventsCollection).UpdateOne(ctx, localEventFilter, bson.D{{Key: "$set", Value: bson.D{{Key: "changes.verified_transition_ref", Value: legacyLocalProof}}}})
	if err != nil {
		t.Fatal(err)
	}
	if eventUpdate.MatchedCount != 1 {
		t.Fatalf("legacy Local event fixture matched %d events", eventUpdate.MatchedCount)
	}
	arrivalIdentityMismatch.Store(true)
	retargetedPromotion := TODOActionRequest{ProjectID: projectID, TODOID: todoID, ExpectedRevision: &redelivered.TODORevision, OperationID: "reject-retargeted-alias", RequestedStage: "Promotion-Lane", PromotionTargets: map[string]string{repository: targetBranch, secondaryRepository: targetBranch}}
	if _, err := store.TransitionTODO(ctx, companyID, retargetedPromotion); err == nil || !strings.Contains(err.Error(), "remote_proof.repository_identity") {
		t.Fatalf("retargeted GitHub node ID advanced legacy promotion: %v", err)
	}
	arrivalIdentityMismatch.Store(false)
	if _, err := database.Collection(implementationRepositoryIdentityCollection).DeleteMany(ctx, bson.D{{Key: "project_id", Value: projectID}}); err != nil {
		t.Fatal(err)
	}
	legacyCurrent, err := store.ReadTODO(ctx, companyID, projectID, todoID, nil)
	if err != nil || legacyCurrent.TODO.VerifiedTransitionRef == nil || *legacyCurrent.TODO.VerifiedTransitionRef != legacyLocalProof {
		t.Fatalf("legacy fixture snapshot not installed: %+v err=%v", legacyCurrent.TODO.VerifiedTransitionRef, err)
	}
	store.proofHTTPClient.Timeout = 5 * time.Millisecond
	arrivalDelay.Store(int64(50 * time.Millisecond))
	timeoutPromotion := TODOActionRequest{ProjectID: projectID, TODOID: todoID, ExpectedRevision: &redelivered.TODORevision, OperationID: "reject-reader-timeout", RequestedStage: "Promotion-Lane", PromotionTargets: map[string]string{repository: targetBranch, secondaryRepository: targetBranch}}
	if _, err := store.TransitionTODO(ctx, companyID, timeoutPromotion); err == nil || !strings.Contains(err.Error(), "remote_proof") {
		t.Fatalf("Reader timeout advanced promotion: %v", err)
	}
	store.proofHTTPClient.Timeout = 0
	arrivalDelay.Store(0)
	if identities, err := readImplementationRepositoryNodeIDs(ctx, database, projectID); err != nil || len(identities) != 0 {
		t.Fatalf("Reader timeout left partial alias pins: %+v err=%v", identities, err)
	}
	concurrentBefore, err := store.ReadTODO(ctx, companyID, projectID, todoID, nil)
	if err != nil {
		t.Fatal(err)
	}
	eventsBeforeConcurrentRemoval, err := database.Collection(todoEventsCollection).CountDocuments(ctx, bson.D{{Key: "project_id", Value: projectID}, {Key: "todo_id", Value: todoID}})
	if err != nil {
		t.Fatal(err)
	}
	identitiesBeforeConcurrentRemoval, err := readImplementationRepositoryNodeIDs(ctx, database, projectID)
	if err != nil || len(identitiesBeforeConcurrentRemoval) != 0 {
		t.Fatalf("unexpected identity pins before membership race: %+v err=%v", identitiesBeforeConcurrentRemoval, err)
	}
	removeSecondaryOnArrival.Store(true)
	concurrentRemovalRequest := TODOActionRequest{ProjectID: projectID, TODOID: todoID, ExpectedRevision: &redelivered.TODORevision, OperationID: "reject-concurrent-membership-removal", RequestedStage: "Promotion-Lane", PromotionTargets: map[string]string{repository: targetBranch, secondaryRepository: targetBranch}}
	if _, err := store.TransitionTODO(ctx, companyID, concurrentRemovalRequest); err == nil || (!strings.Contains(err.Error(), "owner-confirmed Project repository alias") && !strings.Contains(err.Error(), "remain in the Project's owner-confirmed implementation repository set")) {
		t.Fatalf("Complete Promotion was not rejected after alias removal during Reader proof: %v", err)
	}
	concurrentAfter, err := store.ReadTODO(ctx, companyID, projectID, todoID, nil)
	if err != nil || concurrentAfter.TODO.Stage != concurrentBefore.TODO.Stage || concurrentAfter.TODORevision != concurrentBefore.TODORevision {
		t.Fatalf("concurrent alias removal changed TODO revision/stage: before=%+v after=%+v err=%v", concurrentBefore, concurrentAfter, err)
	}
	eventsAfterConcurrentRemoval, err := database.Collection(todoEventsCollection).CountDocuments(ctx, bson.D{{Key: "project_id", Value: projectID}, {Key: "todo_id", Value: todoID}})
	if err != nil || eventsAfterConcurrentRemoval != eventsBeforeConcurrentRemoval {
		t.Fatalf("concurrent alias removal wrote a TODO history event: before=%d after=%d err=%v", eventsBeforeConcurrentRemoval, eventsAfterConcurrentRemoval, err)
	}
	identitiesAfterConcurrentRemoval, err := readImplementationRepositoryNodeIDs(ctx, database, projectID)
	if err != nil || len(identitiesAfterConcurrentRemoval) != len(identitiesBeforeConcurrentRemoval) {
		t.Fatalf("concurrent alias removal left partial identity pins: before=%+v after=%+v err=%v", identitiesBeforeConcurrentRemoval, identitiesAfterConcurrentRemoval, err)
	}
	setAfterConcurrentRemoval, err := store.ReadImplementationRepositories(ctx, companyID, projectID)
	if err != nil || containsTODOString(setAfterConcurrentRemoval.Items, secondaryRepository) {
		t.Fatalf("concurrent owner removal was not applied: set=%+v err=%v", setAfterConcurrentRemoval, err)
	}
	if _, err := store.ChangeImplementationRepository(ctx, companyID, projectID, ImplementationRepositoryChange{
		Action: "confirm", ProjectID: projectID, ExpectedRevision: setAfterConcurrentRemoval.Revision,
		OperationID: "restore-secondary-after-concurrent-test", Actor: "fixture-owner", OwnerReference: "fixture-owner",
		RepositoryID: secondaryRepository,
	}); err != nil {
		t.Fatalf("restore alias after no-mutation assertion: %v", err)
	}
	setPromotionGitRef(t, gitDir, targetBranch, baseline)
	failedPromotion := TODOActionRequest{ProjectID: projectID, TODOID: todoID, ExpectedRevision: &redelivered.TODORevision, OperationID: "reject-incomplete-promotion", RequestedStage: "Promotion-Lane", PromotionTargets: map[string]string{repository: targetBranch, secondaryRepository: targetBranch}}
	if _, err := store.TransitionTODO(ctx, companyID, failedPromotion); err == nil || !strings.Contains(err.Error(), "remote_proof") {
		t.Fatalf("incomplete %s arrival advanced Complete TODO: %v", targetBranch, err)
	}
	unchanged, err := store.ReadTODO(ctx, companyID, projectID, todoID, nil)
	if err != nil || unchanged.TODO.Stage != "Local-Implemented" || unchanged.TODORevision != redelivered.TODORevision {
		t.Fatalf("failed promotion mutated TODO state: %+v err=%v", unchanged, err)
	}
	extraPromotionFields := failedPromotion
	extraPromotionFields.OperationID = "reject-caller-promotion-proof"
	extraPromotionFields.ProofReferences = []TODOProofReference{{Kind: "external", Source: "caller", Reference: "must-not-be-used", SourceDigest: redelivered.ContentDigest, VerificationState: "unverified"}}
	if _, err := store.TransitionTODO(ctx, companyID, extraPromotionFields); err == nil || !strings.Contains(err.Error(), "proof_refs/waiver_refs/local_proof") {
		t.Fatalf("Complete promotion accepted caller proof fields: %v", err)
	}
	setPromotionGitRef(t, gitDir, targetBranch, heads[targetBranch])
	injectPromotionIdentityConflict.Store(true)
	failedPromotion.OperationID = "reject-partial-promotion-pin"
	if _, err := store.TransitionTODO(ctx, companyID, failedPromotion); err == nil || !strings.Contains(err.Error(), "repository_identity.github_node_id") {
		t.Fatalf("conflicting second Promotion pin did not reject transition: %v", err)
	}
	injectPromotionIdentityConflict.Store(false)
	if err := database.Collection(implementationRepositoryIdentityCollection).FindOne(ctx, bson.D{{Key: "project_id", Value: projectID}, {Key: "repository_id", Value: repository}}).Decode(&partialPin); !errors.Is(err, mongo.ErrNoDocuments) {
		t.Fatalf("primary Promotion pin survived failed transaction: %+v err=%v", partialPin, err)
	}
	if _, err := database.Collection(implementationRepositoryIdentityCollection).DeleteOne(ctx, bson.D{{Key: "project_id", Value: projectID}, {Key: "repository_id", Value: secondaryRepository}}); err != nil {
		t.Fatal(err)
	}
	failedPromotion.OperationID = "complete-" + targetBranch + "-promotion"
	if _, err := store.TransitionTODO(ctx, companyID, failedPromotion); err != nil {
		t.Fatalf("Complete %s arrival: %v", targetBranch, err)
	}
	promoted, err := store.ReadTODO(ctx, companyID, projectID, todoID, nil)
	if err != nil || promoted.TODO.Stage != "Promotion-Lane" || promoted.TODO.VerifiedTransitionRef == nil || promoted.TODO.VerifiedTransitionRef.Kind != "complete-git-arrival" {
		t.Fatalf("%s arrival was not recorded as Promotion-Lane: %+v err=%v", targetBranch, promoted, err)
	}
	assertFixtureArrival(t, *promoted.TODO.VerifiedTransitionRef, "promotion", targetBranch, heads[targetBranch])
	expectedArrivalBranch.Store("main")
	tamperedPromotionProof := *promoted.TODO.VerifiedTransitionRef
	tamperedPromotionProof.Reference += ";tampered=1"
	if _, err := database.Collection(todosCollection).UpdateOne(ctx, bson.D{{Key: "project_id", Value: projectID}, {Key: "id", Value: todoID}}, bson.D{{Key: "$set", Value: bson.D{{Key: "verified_transition_ref", Value: tamperedPromotionProof}}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.TransitionTODO(ctx, companyID, TODOActionRequest{ProjectID: projectID, TODOID: todoID, ExpectedRevision: &promoted.TODORevision, OperationID: "reject-promotion-snapshot-tamper", RequestedStage: "Completed"}); err == nil || !strings.Contains(err.Error(), "promotion_history.event") {
		t.Fatalf("tampered Promotion-Lane snapshot was accepted: %v", err)
	}
	if _, err := database.Collection(todosCollection).UpdateOne(ctx, bson.D{{Key: "project_id", Value: projectID}, {Key: "id", Value: todoID}}, bson.D{{Key: "$set", Value: bson.D{{Key: "verified_transition_ref", Value: *promoted.TODO.VerifiedTransitionRef}}}}); err != nil {
		t.Fatal(err)
	}
	currentPromotionProof := *promoted.TODO.VerifiedTransitionRef
	legacyPromotionProof := seedLegacyPromotionArrival(t, ctx, database, promoted)
	assertInvalidPersistedArrivals(t, ctx, store, database, companyID, promoted, currentPromotionProof, legacyPromotionProof, &readerCalls)
	completed := TODOActionRequest{ProjectID: projectID, TODOID: todoID, ExpectedRevision: &promoted.TODORevision, OperationID: "complete-main-arrival", RequestedStage: "Completed"}
	if targetBranch == "dev" {
		if err := editConstitution("complete", "Editorial wording after verified promotion", "constitution-editorial-change"); err != nil {
			t.Fatal(err)
		}
	} else {
		configureFixturePromotionMode(t, ctx, store, companyID, projectID, "mvp", 1, "conflicting-after-promotion")
		if _, err := store.TransitionTODO(ctx, companyID, completed); err == nil || !strings.Contains(err.Error(), "restore the Complete mode preserved") {
			t.Fatalf("MVP unexpectedly completed an existing Complete promotion: %v", err)
		}
		stillPromoted, err := store.ReadTODO(ctx, companyID, projectID, todoID, nil)
		if err != nil || stillPromoted.TODORevision != promoted.TODORevision || *stillPromoted.TODO.VerifiedTransitionRef != legacyPromotionProof {
			t.Fatalf("divergent mode changed the legacy promotion: %+v err=%v", stillPromoted, err)
		}
		configureFixturePromotionMode(t, ctx, store, companyID, projectID, "complete", 2, "restore-complete-after-promotion")
	}
	extraCompletedFields := completed
	extraCompletedFields.OperationID = "reject-caller-main-proof"
	extraCompletedFields.LocalProof = &LocalProofResult{SchemaVersion: "builder-local-proof-result-v3"}
	if _, err := store.TransitionTODO(ctx, companyID, extraCompletedFields); err == nil || !strings.Contains(err.Error(), "proof_refs/waiver_refs/local_proof") {
		t.Fatalf("Complete main arrival accepted caller proof fields: %v", err)
	}
	completed.OperationID = "reject-unarrived-main"
	if _, err := store.TransitionTODO(ctx, companyID, completed); err == nil || !strings.Contains(err.Error(), "remote_proof") {
		t.Fatalf("unarrived main commit advanced Complete TODO: %v", err)
	}
	unchanged, err = store.ReadTODO(ctx, companyID, projectID, todoID, nil)
	if err != nil || unchanged.TODO.Stage != "Promotion-Lane" || unchanged.TODORevision != promoted.TODORevision {
		t.Fatalf("failed main arrival mutated TODO state: %+v err=%v", unchanged, err)
	}
	setPromotionGitRef(t, gitDir, "main", heads["main"])
	completed.OperationID = "complete-main-arrival"
	if _, err := store.TransitionTODO(ctx, companyID, completed); err != nil {
		t.Fatalf("Complete main arrival: %v", err)
	}
	final, err := store.ReadTODO(ctx, companyID, projectID, todoID, nil)
	if err != nil || final.TODO.Stage != "Completed" || final.TODO.VerifiedTransitionRef == nil || final.TODO.VerifiedTransitionRef.Kind != "complete-git-arrival" || !strings.Contains(final.TODO.VerifiedTransitionRef.Reference, `"arrival_stage":"main"`) {
		t.Fatalf("main arrival was not recorded as Git-only Completed: %+v err=%v", final, err)
	}
	assertFixtureArrival(t, *final.TODO.VerifiedTransitionRef, "main", "main", heads["main"])
	eventsBeforeRetry, err := database.Collection(todoEventsCollection).CountDocuments(ctx, bson.D{{Key: "project_id", Value: projectID}, {Key: "todo_id", Value: todoID}})
	if err != nil {
		t.Fatal(err)
	}
	retry, err := store.TransitionTODO(ctx, companyID, completed)
	if err != nil || !retry.NoOp || retry.TODORevision != final.TODORevision || retry.Stage != "Completed" {
		t.Fatalf("exact main-arrival retry did not return the original result: %+v err=%v", retry, err)
	}
	eventsAfterRetry, err := database.Collection(todoEventsCollection).CountDocuments(ctx, bson.D{{Key: "project_id", Value: projectID}, {Key: "todo_id", Value: todoID}})
	if err != nil || eventsAfterRetry != eventsBeforeRetry {
		t.Fatalf("exact main-arrival retry added a TODO event: before=%d after=%d err=%v", eventsBeforeRetry, eventsAfterRetry, err)
	}
	retried, err := store.ReadTODO(ctx, companyID, projectID, todoID, nil)
	if err != nil || retried.TODORevision != final.TODORevision || retried.TODO.VerifiedTransitionRef == nil || *retried.TODO.VerifiedTransitionRef != *final.TODO.VerifiedTransitionRef {
		t.Fatalf("exact main-arrival retry changed original proof: before=%+v after=%+v err=%v", final.TODO.VerifiedTransitionRef, retried.TODO.VerifiedTransitionRef, err)
	}
	promotionHistory, err := store.ReadTODOAsOf(ctx, companyID, projectID, todoID, promoted.TODORevision)
	if err != nil || promotionHistory.State.VerifiedTransitionRef == nil || *promotionHistory.State.VerifiedTransitionRef != legacyPromotionProof {
		t.Fatalf("completion changed the historical Promotion-Lane proof: %+v err=%v", promotionHistory, err)
	}
	var preservedLocalEvent TODOHistoryEvent
	if err := database.Collection(todoEventsCollection).FindOne(ctx, localEventFilter).Decode(&preservedLocalEvent); err != nil || preservedLocalEvent.Changes.VerifiedTransitionRef == nil || *preservedLocalEvent.Changes.VerifiedTransitionRef != legacyLocalProof {
		t.Fatalf("promotion rewrote the historical legacy Local proof: %+v err=%v", preservedLocalEvent, err)
	}
	identities, err := readImplementationRepositoryNodeIDs(ctx, database, projectID)
	if err != nil || len(identities) != 2 || identities[repository] == "" || identities[secondaryRepository] == "" {
		t.Fatalf("promotion failed to pin legacy aliases: %+v err=%v", identities, err)
	}

	// Freeze the Project membership inside transaction A, commit an owner removal
	// in transaction B, then attempt the proof CAS from A against its stale view.
	// The Project document write must conflict before any TODO/event mutation.
	setBeforeRace, err := store.ReadImplementationRepositories(ctx, companyID, projectID)
	if err != nil {
		t.Fatal(err)
	}
	session, err := database.Client().StartSession()
	if err != nil {
		t.Fatal(err)
	}
	defer session.EndSession(ctx)
	_, raceErr := session.WithTransaction(ctx, func(txCtx context.Context) (any, error) {
		var staleProject projectDocument
		if err := database.Collection(projectsCollection).FindOne(txCtx, bson.D{{Key: "id", Value: projectID}}).Decode(&staleProject); err != nil {
			return nil, err
		}
		removed, err := store.ChangeImplementationRepository(ctx, companyID, projectID, ImplementationRepositoryChange{
			Action: "remove", ProjectID: projectID, ExpectedRevision: setBeforeRace.Revision,
			OperationID: "membership-race-removal", Actor: "fixture-owner", OwnerReference: "fixture-owner",
			RepositoryID: secondaryRepository,
		})
		if err != nil || containsTODOString(removed.Items, secondaryRepository) {
			return nil, fmt.Errorf("concurrent owner removal failed: set=%+v err=%v", removed, err)
		}
		return nil, writeTODORepositoryProofFence(txCtx, database, projectID, []string{repository, secondaryRepository})
	})
	if raceErr == nil {
		t.Fatal("stale transaction membership CAS succeeded after concurrent alias removal")
	}
	setAfterRace, err := store.ReadImplementationRepositories(ctx, companyID, projectID)
	if err != nil || containsTODOString(setAfterRace.Items, secondaryRepository) || setAfterRace.Revision != setBeforeRace.Revision+1 {
		t.Fatalf("concurrent owner removal did not remain the sole Project mutation: set=%+v err=%v", setAfterRace, err)
	}
}

func createPromotionGitFixture(t *testing.T, releaseBranch string) (string, string, string, string, map[string]string) {
	t.Helper()
	gitDir := filepath.Join(t.TempDir(), "arrival.git")
	git := func(args ...string) string {
		t.Helper()
		command := exec.Command("git", append([]string{"--git-dir", gitDir, "-c", "user.email=fixture@example.com", "-c", "user.name=Fixture"}, args...)...)
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("git fixture %v: %v: %s", args, err, output)
		}
		return strings.TrimSpace(string(output))
	}
	git("init", "--bare", "--quiet")
	empty := exec.Command("git", "--git-dir", gitDir, "mktree")
	out, err := empty.Output()
	if err != nil {
		t.Fatal(err)
	}
	tree := strings.TrimSpace(string(out))
	baseline := git("commit-tree", tree, "-m", "baseline")
	primary := git("commit-tree", tree, "-p", baseline, "-m", "primary implementation")
	secondary := git("commit-tree", tree, "-p", baseline, "-m", "secondary implementation")
	heads := make(map[string]string, 4)
	for _, branch := range []string{"dev", "stage", "main", releaseBranch} {
		heads[branch] = git("commit-tree", tree, "-p", primary, "-p", secondary, "-m", "arrival on "+branch)
	}
	for _, branch := range []string{"dev", "stage", releaseBranch} {
		setPromotionGitRef(t, gitDir, branch, heads[branch])
	}
	// The primary commit has arrived in main, while the secondary has not.
	setPromotionGitRef(t, gitDir, "main", primary)
	return gitDir, baseline, primary, secondary, heads
}

func setPromotionGitRef(t *testing.T, gitDir, branch, commit string) {
	t.Helper()
	out, err := exec.Command("git", "--git-dir", gitDir, "update-ref", "refs/heads/"+branch, commit).CombinedOutput()
	if err != nil {
		t.Fatalf("set fixture branch %s: %v: %s", branch, err, out)
	}
}

func assertFixtureArrival(t *testing.T, proof TODOProofReference, stage, branch, head string) {
	t.Helper()
	var payload completeArrivalProof
	if err := json.Unmarshal([]byte(proof.Reference), &payload); err != nil || payload.Stage != stage || len(payload.Repositories) != 2 {
		t.Fatalf("invalid stored %s arrival: %+v err=%v", stage, payload, err)
	}
	for _, evidence := range payload.Repositories {
		if evidence.Branch != branch || evidence.HeadSHA != head {
			t.Fatalf("%s proof for %s used %s@%s, want %s@%s", stage, evidence.Repository, evidence.Branch, evidence.HeadSHA, branch, head)
		}
	}
}

type fixtureArrivalBarrier struct {
	ready   chan struct{}
	arrived atomic.Int32
}

func admitConcurrentPromotionFixture(t *testing.T, ctx context.Context, store *MongoStore, company, project string, record TODORecord, content TODOAuthoredContent, first, second TODOCommitReference, proofs ...TODOProofReference) TODOReadResponse {
	t.Helper()
	const id = "api-only-delivery-peer"
	position := 50
	record.ID, record.Position = id, &position
	if _, err := store.AdmitTODO(ctx, company, project, TODOAdmissionRequest{SchemaVersion: todoCreationSchemaVersion, OperationID: "create-peer-overlap", TODO: record}); err != nil {
		t.Fatal(err)
	}
	zero := int64(0)
	edit, err := store.EditTODOContent(ctx, company, TODOContentEditRequest{ProjectID: project, TODOID: id, ExpectedRevision: &zero, OperationID: "content-peer-overlap", Content: content, Reason: "Confirm peer promotion fixture scope"})
	if err != nil {
		t.Fatal(err)
	}
	rev := recordPlanningReviewSubjects(t, ctx, store, project, id, "review-peer-overlap")
	for _, stage := range []string{"Implementation-Approval-Pending", "Ready-for-Implementation", "In-Progress"} {
		request := TODOActionRequest{ProjectID: project, TODOID: id, ExpectedRevision: &rev, OperationID: "peer-" + stage, RequestedStage: stage}
		if stage == "Ready-for-Implementation" {
			request.ProofReferences = []TODOProofReference{{Kind: "owner-approval", Source: "owner-chat", Reference: "APROVADO", SourceDigest: edit.ContentDigest, VerificationState: "unverified"}}
		}
		if _, err := store.TransitionTODO(ctx, company, request); err != nil {
			t.Fatalf("peer %s: %v", stage, err)
		}
		rev++
	}
	for i := range proofs {
		proofs[i].SourceDigest = edit.ContentDigest
	}
	first.SourceDigest, second.SourceDigest = edit.ContentDigest, edit.ContentDigest
	if _, err := store.EditTODOEvidence(ctx, company, TODOEvidenceEditRequest{ProjectID: project, TODOID: id, ExpectedRevision: &rev, OperationID: "evidence-peer-overlap", ProofReferences: proofs, WaiverReferences: []TODOProofReference{}, ImplementationCommits: []TODOCommitReference{first, second}, RecordSections: []TODOSection{}, Reason: "Record exact peer evidence"}); err != nil {
		t.Fatal(err)
	}
	rev++
	if _, err := store.TransitionTODO(ctx, company, TODOActionRequest{ProjectID: project, TODOID: id, ExpectedRevision: &rev, OperationID: "local-peer-overlap", RequestedStage: "Local-Implemented"}); err != nil {
		t.Fatal(err)
	}
	read, err := store.ReadTODO(ctx, company, project, id, nil)
	if err != nil {
		t.Fatal(err)
	}
	return read
}

func submitFixturePromotionHTTP(t *testing.T, registry *httptest.Server, companyID string, request TODOActionRequest) (int, []byte) {
	t.Helper()
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := registry.URL + "/internal/v1/companies/" + companyID + "/projects/" + request.ProjectID + "/todos/" + request.TODOID + "/transition"
	response, err := registry.Client().Post(endpoint, "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, body
}

func assertFixturePromotionHTTP(t *testing.T, registry *httptest.Server, companyID string, request TODOActionRequest, wantStage string) {
	t.Helper()
	code, body := submitFixturePromotionHTTP(t, registry, companyID, request)
	var result TODOActionResponse
	if err := json.Unmarshal(body, &result); err != nil || code != http.StatusOK || result.Stage != wantStage ||
		result.StageVerification != "verified" || result.TODORevision != *request.ExpectedRevision+1 {
		t.Fatalf("verified %s via Registry HTTP: status=%d body=%s err=%v", wantStage, code, body, err)
	}
}

func configureFixturePromotionMode(t *testing.T, ctx context.Context, store *MongoStore, companyID, projectID, mode string, revision int64, operationID string) {
	t.Helper()
	state, err := store.ChangeProjectPromotionMode(ctx, companyID, projectID, ProjectPromotionModeChange{
		ProjectID: projectID, Mode: mode, ExpectedRevision: &revision, OperationID: operationID,
		Actor: "fixture-operator", OwnerReference: "Explicit fixture owner decision",
	})
	if err != nil || state.EffectiveMode != mode || state.Revision != revision+1 {
		t.Fatalf("configure %s: %+v err=%v", mode, state, err)
	}
}

func updateFixtureTODOStageProof(t *testing.T, ctx context.Context, database *mongo.Database, read TODOReadResponse, proof TODOProofReference) {
	t.Helper()
	filter := bson.M{"project_id": read.TODO.ProjectID, "id": read.TODO.ID, "revision": read.TODORevision}
	result, err := database.Collection(todosCollection).UpdateOne(ctx, filter, bson.M{"$set": bson.M{"verified_transition_ref": proof}})
	if err != nil || result.MatchedCount != 1 {
		t.Fatalf("seed historical proof snapshot: %+v err=%v", result, err)
	}
	filter = bson.M{"project_id": read.TODO.ProjectID, "todo_id": read.TODO.ID, "revision": read.TODORevision, "kind": "state_changed"}
	result, err = database.Collection(todoEventsCollection).UpdateOne(ctx, filter, bson.M{"$set": bson.M{"changes.verified_transition_ref": proof}})
	if err != nil || result.MatchedCount != 1 {
		t.Fatalf("seed immutable historical proof event: %+v err=%v", result, err)
	}
}

func seedLegacyPromotionArrival(t *testing.T, ctx context.Context, database *mongo.Database, promoted TODOReadResponse) TODOProofReference {
	t.Helper()
	legacy := *promoted.TODO.VerifiedTransitionRef
	var payload completeArrivalProof
	if err := json.Unmarshal([]byte(legacy.Reference), &payload); err != nil {
		t.Fatal(err)
	}
	ids := make(map[string]string, len(payload.Repositories))
	for _, repository := range payload.Repositories {
		ids[repository.Repository] = repository.NodeID
	}
	pin := todoConstitutionPin{Mode: "complete", GenerationID: "legacy-generation", RecordID: "legacy-constitution",
		Revision: 4, ContentDigest: sourceHash([]byte("legacy constitution content")), Repositories: ids}
	legacyPayload := struct {
		Stage           string                      `json:"arrival_stage"`
		ContentRevision int64                       `json:"content_revision"`
		ContentDigest   string                      `json:"content_digest"`
		Constitution    todoConstitutionPin         `json:"constitution"`
		Repositories    []completeArrivalRepository `json:"repositories"`
	}{payload.Stage, payload.ContentRevision, payload.ContentDigest, pin, payload.Repositories}
	payloadJSON, err := json.Marshal(legacyPayload)
	if err != nil {
		t.Fatal(err)
	}
	pinJSON, err := json.Marshal(pin)
	if err != nil {
		t.Fatal(err)
	}
	legacy.Reference = string(payloadJSON) + ";" + todoConstitutionPinPrefix + string(pinJSON)
	legacy.SourceDigest = sourceHash(payloadJSON)
	updateFixtureTODOStageProof(t, ctx, database, promoted, legacy)
	return legacy
}

func assertInvalidPersistedArrivals(t *testing.T, ctx context.Context, store *MongoStore, database *mongo.Database, companyID string, promoted TODOReadResponse, current, legacy TODOProofReference, readerCalls *atomic.Int64) {
	t.Helper()
	differentDigest := sourceHash([]byte("different immutable proof"))
	badDigest := current
	badDigest.SourceDigest = differentDigest

	var payload completeArrivalProof
	if err := json.Unmarshal([]byte(current.Reference), &payload); err != nil {
		t.Fatal(err)
	}
	control := payload
	payload.LocalEventRevision++
	badRevision := fixtureArrivalWithPayload(t, current, payload)
	payload = control
	payload.LocalProofDigest = differentDigest
	badLocalDigest := fixtureArrivalWithPayload(t, current, payload)

	hashed, suffix, ok := strings.Cut(legacy.Reference, ";"+todoConstitutionPinPrefix)
	var legacyPin todoConstitutionPin
	if !ok || json.Unmarshal([]byte(suffix), &legacyPin) != nil {
		t.Fatal("missing historical suffix fixture")
	}
	legacyPin.Repositories["owner/repo"] = "R_kgDOContradictory"
	pinJSON, err := json.Marshal(legacyPin)
	if err != nil {
		t.Fatal(err)
	}
	contradictoryLegacy := legacy
	contradictoryLegacy.Reference = hashed + ";" + todoConstitutionPinPrefix + string(pinJSON)

	events := database.Collection(todoEventsCollection)
	filter := bson.M{"project_id": promoted.TODO.ProjectID, "todo_id": promoted.TODO.ID}
	before, err := events.CountDocuments(ctx, filter)
	if err != nil {
		t.Fatal(err)
	}
	for _, variant := range []struct {
		name, expected string
		proof          TODOProofReference
	}{
		{"wrong-arrival-digest", "match the immutable arrival payload", badDigest},
		{"contradictory-legacy-pin", "match the mode and identities in the hashed payload and legacy pin", contradictoryLegacy},
		{"wrong-local-event-revision", "match the preserved Complete mode and exact Local-Implemented event", badRevision},
		{"wrong-local-proof-digest", "match the preserved Complete mode and exact Local-Implemented event", badLocalDigest},
	} {
		t.Run(variant.name, func(t *testing.T) {
			updateFixtureTODOStageProof(t, ctx, database, promoted, variant.proof)
			count := readerCalls.Load()
			if _, err := store.TransitionTODO(ctx, companyID, TODOActionRequest{
				ProjectID: promoted.TODO.ProjectID, TODOID: promoted.TODO.ID, ExpectedRevision: &promoted.TODORevision,
				OperationID: variant.name, RequestedStage: "Completed",
			}); !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), variant.expected) {
				t.Fatalf("contradictory immutable proof failed at the wrong boundary: %v", err)
			}
			if readerCalls.Load() != count {
				t.Fatal("invalid persisted proof reached the Reader")
			}
			after, err := store.ReadTODO(ctx, companyID, promoted.TODO.ProjectID, promoted.TODO.ID, nil)
			afterEvents, countErr := events.CountDocuments(ctx, filter)
			if err != nil || countErr != nil || afterEvents != before || after.TODORevision != promoted.TODORevision ||
				after.ContentDigest != promoted.ContentDigest || after.TODO.Stage != "Promotion-Lane" ||
				after.TODO.VerifiedTransitionRef == nil || *after.TODO.VerifiedTransitionRef != variant.proof {
				t.Fatalf("invalid persisted proof changed TODO state or history: %+v, events %d -> %d, read=%v count=%v", after, before, afterEvents, err, countErr)
			}
		})
	}
	updateFixtureTODOStageProof(t, ctx, database, promoted, legacy)
}

func fixtureArrivalWithPayload(t *testing.T, proof TODOProofReference, payload completeArrivalProof) TODOProofReference {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	proof.Reference, proof.SourceDigest = string(data), sourceHash(data)
	return proof
}
