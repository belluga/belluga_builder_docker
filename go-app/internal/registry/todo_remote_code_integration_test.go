package registry

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// The setup seeds an already approved In-Progress predecessor. This test owns
// the evidence-edit, Reader-backed transition, persistence and readback edge.
func TestMongoRemoteCodeDeliveryTransition(t *testing.T) {
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
	if err := store.ProvisionTODOCollection(ctx, companyID); err != nil {
		t.Fatal(err)
	}
	if err := store.ProvisionTODOHistory(ctx, companyID); err != nil {
		t.Fatal(err)
	}
	const repository, branch = "owner/repo", "r-08-rc"
	baseline, commit, head := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40)
	if _, err := database.Collection(epicsCollection).InsertOne(ctx, roadmapEpic{StorageID: bson.NewObjectID(), ProjectID: projectID, ID: "e-06", Name: "Database", Position: 0}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Collection(releasesCollection).InsertOne(ctx, roadmapRelease{StorageID: bson.NewObjectID(), ProjectID: projectID, ID: "r-08", EpicID: "e-06", Name: "Mongo", Position: 0, DeliveryBranches: []releaseDeliveryBranch{{RepositoryID: repository, Branch: branch, OwnerReference: "fixture-owner", OperationID: "fixture-branch", ConfirmedAt: time.Now().UTC()}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ChangeImplementationRepository(ctx, companyID, projectID, ImplementationRepositoryChange{Action: "confirm", ProjectID: projectID, ExpectedRevision: 0, OperationID: "fixture-repository", Actor: "fixture-owner", OwnerReference: "fixture-owner", RepositoryID: repository}); err != nil {
		t.Fatal(err)
	}
	record := mongoBornTODOFixture()
	record.ID, record.ReleaseID = todoID, "r-08"
	position := 0
	record.Position = &position
	if _, err := store.AdmitTODO(ctx, companyID, projectID, TODOAdmissionRequest{SchemaVersion: todoCreationSchemaVersion, OperationID: "fixture-create", TODO: record}); err != nil {
		t.Fatal(err)
	}
	content := TODOAuthoredContent{TODOContent: validTODOContent(), Title: record.Title, Objective: record.Objective, DiffExpectation: []TODORepositoryScope{{Repository: "Builder", RepositoryID: repository, Baseline: baseline, ExpectedPaths: []string{"go-app/internal/registry/**"}, NotExpectedPaths: []string{}}}, AffectedFiles: []string{}}
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
	readerCalls := 0
	reader := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		readerCalls++
		q := r.URL.Query()
		if q.Get("company_id") != companyID || q.Get("project_id") != projectID || q.Get("repository") != repository || q.Get("branch") != branch || q.Get("commit_sha") != commit || q.Get("baseline_sha") != baseline || q.Get("todo_authority") != "mongo-v2" {
			t.Errorf("Reader received wrong scoped proof query: %s", r.URL)
		}
		_ = json.NewEncoder(w).Encode(remoteCommitResult{CompanyID: companyID, ProjectID: projectID, Repository: repository, Branch: branch, CommitSHA: commit, BaselineSHA: baseline, ParentSHA: baseline, RemoteHead: head, Paths: []string{"go-app/internal/registry/todo_access.go"}, Outcome: "verified"})
	}))
	defer reader.Close()
	store.proofReaderURL, store.proofReaderToken, store.proofHTTPClient = reader.URL, "fixture-token", reader.Client()
	current, err = store.ReadTODO(ctx, companyID, projectID, todoID, nil)
	if err != nil || current.TODO.Stage != "In-Progress" {
		t.Fatalf("fixture predecessor: %+v %v", current, err)
	}
	criterion := TODOProofReference{Kind: "criterion:criterion-one", Source: "external-evidence", Reference: "fixture/criterion", SourceDigest: edit.ContentDigest, VerificationState: "unverified"}
	scenario := TODOProofReference{Kind: "scenario:scenario-one", Source: "external-evidence", Reference: "fixture/scenario", SourceDigest: edit.ContentDigest, VerificationState: "unverified"}
	claim := TODOCommitReference{Repository: "Builder", Commit: commit, SourceDigest: edit.ContentDigest, VerificationState: "unverified"}
	revision := current.TODORevision
	stale := criterion
	stale.SourceDigest = sourceHash([]byte("older content"))
	if _, err := store.EditTODOEvidence(ctx, companyID, TODOEvidenceEditRequest{ProjectID: projectID, TODOID: todoID, ExpectedRevision: &revision, OperationID: "stale-citations", ProofReferences: []TODOProofReference{stale, scenario}, WaiverReferences: []TODOProofReference{}, ImplementationCommits: []TODOCommitReference{claim}, RecordSections: []TODOSection{}, Reason: "Exercise stale citation rejection"}); err != nil {
		t.Fatal(err)
	}
	revision++
	request := TODOActionRequest{ProjectID: projectID, TODOID: todoID, ExpectedRevision: &revision, OperationID: "reject-stale", RequestedStage: "Local-Implemented"}
	if _, err := store.TransitionTODO(ctx, companyID, request); err == nil || !strings.Contains(err.Error(), "proof_refs.criterion:criterion-one") || readerCalls != 0 {
		t.Fatalf("stale citation reached Reader or stage: calls=%d err=%v", readerCalls, err)
	}
	if _, err := store.EditTODOEvidence(ctx, companyID, TODOEvidenceEditRequest{ProjectID: projectID, TODOID: todoID, ExpectedRevision: &revision, OperationID: "current-citations", ProofReferences: []TODOProofReference{criterion, scenario}, WaiverReferences: []TODOProofReference{}, ImplementationCommits: []TODOCommitReference{claim}, RecordSections: []TODOSection{}, Reason: "Set current external citations"}); err != nil {
		t.Fatal(err)
	}
	revision++
	request.ExpectedRevision, request.OperationID = &revision, "reject-inline-local-proof"
	request.LocalProof = &LocalProofResult{SchemaVersion: "builder-local-proof-result-v3"}
	if _, err := store.TransitionTODO(ctx, companyID, request); err == nil || !strings.Contains(err.Error(), "local_proof/proof_refs/waiver_refs") || readerCalls != 0 {
		t.Fatalf("inline local proof reached Reader or stage: calls=%d err=%v", readerCalls, err)
	}
	request.LocalProof, request.OperationID = nil, "remote-code-delivery"
	if _, err := store.TransitionTODO(ctx, companyID, request); err != nil {
		t.Fatal(err)
	}
	actual, err := store.ReadTODO(ctx, companyID, projectID, todoID, nil)
	if err != nil || actual.TODO.Stage != "Local-Implemented" || actual.TODO.StageVerification != "verified" || actual.TODO.VerifiedTransitionRef == nil || actual.TODO.VerifiedTransitionRef.Kind != "remote-code-delivery" || actual.TODO.VerifiedTransitionRef.Source != "private-reader" || actual.TODO.VerifiedTransitionRef.VerificationState != "verified" || actual.TODORevision != revision+1 || readerCalls != 1 {
		t.Fatalf("API-only transition/readback: %+v calls=%d err=%v", actual, readerCalls, err)
	}
	if actual.TODO.ProofReferences[0].VerificationState != "unverified" || actual.TODO.ProofReferences[1].VerificationState != "unverified" {
		t.Fatalf("external citations were promoted to verified execution: %+v", actual.TODO.ProofReferences)
	}
}
