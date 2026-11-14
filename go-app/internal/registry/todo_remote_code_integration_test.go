package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
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
		t.Run(targetBranch+"-then-main", func(t *testing.T) {
			testMongoRemoteCodeDeliveryTransition(t, targetBranch)
		})
	}
}

func testMongoRemoteCodeDeliveryTransition(t *testing.T, targetBranch string) {
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
		canonicalTestRecord(FamilyConstitution, projectID, `{"promotion_mode":"complete","capability_namespaces":["go"],"rules":[{"id":"authority","status":"current","statement":"Project API records verified delivery"}]}`),
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
	if _, err := store.ActivateCanonical(ctx, companyID, projectID, canonical.GenerationID, "fixture-canonical-activate", "fixture-owner"); err != nil {
		t.Fatal(err)
	}
	if err := store.ProvisionTODOCollection(ctx, companyID); err != nil {
		t.Fatal(err)
	}
	if err := store.ProvisionTODOHistory(ctx, companyID); err != nil {
		t.Fatal(err)
	}
	const repository, branch, secondaryRepository = "owner/repo", "r-08-rc", "owner/secondary"
	baseline, commit, secondaryCommit, head := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("d", 40), strings.Repeat("c", 40)
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
	var arrivalFailure, arrivalIdentityMismatch, reachableOnRelease atomic.Bool
	var injectPromotionIdentityConflict atomic.Bool
	var removeSecondaryOnArrival atomic.Bool
	var arrivalDelay atomic.Int64
	reader := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		readerCalls.Add(1)
		q := r.URL.Query()
		if r.URL.Path == "/internal/v1/proofs/arrival" {
			if delay := time.Duration(arrivalDelay.Load()); delay > 0 {
				time.Sleep(delay)
			}
			targetBranch := q.Get("branch")
			arrivalRepository := q.Get("repository")
			wantedCommits := commit
			if arrivalRepository == secondaryRepository {
				wantedCommits = secondaryCommit
			}
			if q.Get("company_id") != companyID || q.Get("project_id") != projectID || arrivalRepository != repository && arrivalRepository != secondaryRepository || q.Get("todo_authority") != "mongo-v2" || targetBranch != "dev" && targetBranch != "stage" && targetBranch != "main" && targetBranch != branch || q.Get("commit_shas") != wantedCommits {
				t.Errorf("Reader received wrong Complete arrival query: %s", r.URL)
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
			if arrivalFailure.Load() {
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(`{"teach":"original commit has not arrived on the selected branch"}`))
				return
			}
			repoCommits := []map[string]any{}
			nodeID := "R_kgDOImmutable"
			if arrivalRepository == repository {
				repoCommits = append(repoCommits, map[string]any{"commit_sha": commit, "reachable": true})
			} else {
				repoCommits = append(repoCommits, map[string]any{"commit_sha": secondaryCommit, "reachable": true})
				nodeID = "R_kgDOImmutable-secondary"
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
			_ = json.NewEncoder(w).Encode(map[string]any{"company_id": companyID, "project_id": projectID, "repository": arrivalRepository, "repository_node_id": nodeID, "branch": targetBranch, "head_sha": head, "commits": repoCommits, "outcome": "verified"})
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
	legacyLocalProof := *redelivered.TODO.VerifiedTransitionRef
	legacyItems := make([]string, 0)
	for _, item := range strings.Split(legacyLocalProof.Reference, ";") {
		if strings.HasPrefix(item, "repository_node_id=") || strings.HasPrefix(item, todoConstitutionPinPrefix) {
			continue
		}
		legacyItems = append(legacyItems, item)
	}
	legacyLocalProof.Reference = strings.Join(legacyItems, ";")
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
	if _, err := store.resolveTODOConstitutionPin(ctx, companyID, legacyCurrent.TODO, legacyCurrent.TODO.VerifiedTransitionRef); err != nil {
		var debugEvent TODOHistoryEvent
		_ = database.Collection(todoEventsCollection).FindOne(ctx, localEventFilter).Decode(&debugEvent)
		t.Fatalf("legacy Local event did not resolve its historical constitution: %v; event=%+v current=%+v", err, debugEvent.Changes.VerifiedTransitionRef, legacyCurrent.TODO.VerifiedTransitionRef)
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
	arrivalFailure.Store(true)
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
	arrivalFailure.Store(false)
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
	completed := TODOActionRequest{ProjectID: projectID, TODOID: todoID, ExpectedRevision: &promoted.TODORevision, OperationID: "complete-main-arrival", RequestedStage: "Completed"}
	extraCompletedFields := completed
	extraCompletedFields.OperationID = "reject-caller-main-proof"
	extraCompletedFields.LocalProof = &LocalProofResult{SchemaVersion: "builder-local-proof-result-v3"}
	if _, err := store.TransitionTODO(ctx, companyID, extraCompletedFields); err == nil || !strings.Contains(err.Error(), "proof_refs/waiver_refs/local_proof/promotion_targets") {
		t.Fatalf("Complete main arrival accepted caller proof fields: %v", err)
	}
	arrivalFailure.Store(true)
	completed.OperationID = "reject-unarrived-main"
	if _, err := store.TransitionTODO(ctx, companyID, completed); err == nil || !strings.Contains(err.Error(), "remote_proof") {
		t.Fatalf("unarrived main commit advanced Complete TODO: %v", err)
	}
	unchanged, err = store.ReadTODO(ctx, companyID, projectID, todoID, nil)
	if err != nil || unchanged.TODO.Stage != "Promotion-Lane" || unchanged.TODORevision != promoted.TODORevision {
		t.Fatalf("failed main arrival mutated TODO state: %+v err=%v", unchanged, err)
	}
	arrivalFailure.Store(false)
	completed.OperationID = "complete-main-arrival"
	if _, err := store.TransitionTODO(ctx, companyID, completed); err != nil {
		t.Fatalf("Complete main arrival: %v", err)
	}
	final, err := store.ReadTODO(ctx, companyID, projectID, todoID, nil)
	if err != nil || final.TODO.Stage != "Completed" || final.TODO.VerifiedTransitionRef == nil || final.TODO.VerifiedTransitionRef.Kind != "complete-git-arrival" || !strings.Contains(final.TODO.VerifiedTransitionRef.Reference, `"arrival_stage":"main"`) {
		t.Fatalf("main arrival was not recorded as Git-only Completed: %+v err=%v", final, err)
	}
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
		return nil, advanceImplementationRepositoryMembershipCASInTransaction(txCtx, database, projectID, []string{repository, secondaryRepository})
	})
	if raceErr == nil {
		t.Fatal("stale transaction membership CAS succeeded after concurrent alias removal")
	}
	setAfterRace, err := store.ReadImplementationRepositories(ctx, companyID, projectID)
	if err != nil || containsTODOString(setAfterRace.Items, secondaryRepository) || setAfterRace.Revision != setBeforeRace.Revision+1 {
		t.Fatalf("concurrent owner removal did not remain the sole Project mutation: set=%+v err=%v", setAfterRace, err)
	}
}
