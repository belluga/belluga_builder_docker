package registry

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestMongoGitHubConnectionsKeepIdentityAndReserveCreation(t *testing.T) {
	uri := os.Getenv("REGISTRY_TEST_MONGODB_URI")
	if uri == "" {
		if os.Getenv("REGISTRY_REQUIRE_MONGO_INTEGRATION") == "1" {
			t.Fatal("required Mongo integration fixture unavailable")
		}
		t.Skip("isolated MongoDB fixture not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	stamp := fmt.Sprintf("github_connections_%d", time.Now().UnixNano())
	store, err := OpenMongo(ctx, uri, stamp)
	if err != nil {
		t.Fatal(err)
	}
	db := store.client.Database(stamp + "_tenant")
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		_ = db.Drop(cleanup)
		_ = store.control.Drop(cleanup)
		_ = store.Close(cleanup)
	})
	if err := ensureGitHubConnectionIndexes(ctx, db); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	connection := githubRepositoryConnection{
		ProjectID: "project-a", GitHubRepositoryID: 10, RepositoryNodeID: "node-10",
		RepositoryFullName: "belluga/test", InstallationID: 20, AccountID: 30,
	}
	if err := writeGitHubRepositoryConnectionCAS(ctx, db, connection, 0, now); err != nil {
		t.Fatal(err)
	}
	if err := writeGitHubRepositoryConnectionCAS(ctx, db, connection, 0, now); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate connection = %v, want conflict", err)
	}
	if _, err := db.Collection(projectsCollection).InsertOne(ctx, bson.M{
		"id": "project-a", "implementation_repositories": []string{"belluga/test"}, "implementation_repositories_revision": 1,
	}); err != nil {
		t.Fatal(err)
	}
	previous := connection
	previous.Revision = 1
	lease := githubProofLease{project: "project-a", alias: "belluga/test", binding: githubProofBinding{membershipRevision: 1, connection: previous}}
	if err := guardGitHubProofLease(ctx, db, lease); err != nil {
		t.Fatalf("guard current proof lease: %v", err)
	}
	connection.InstallationID = 21
	if err := writeGitHubRepositoryConnectionCAS(ctx, db, connection, 1, now); err != nil {
		t.Fatal(err)
	}
	if err := writeGitHubRepositoryConnectionCAS(ctx, db, connection, 1, now); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale revision = %v, want conflict", err)
	}
	if err := guardGitHubProofLease(ctx, db, lease); !errors.Is(err, ErrConflict) {
		t.Fatalf("accepted proof from replaced installation: %v", err)
	}
	connection.RepositoryNodeID = "different-node"
	if err := writeGitHubRepositoryConnectionCAS(ctx, db, connection, 2, now); !errors.Is(err, ErrConflict) {
		t.Fatalf("replaced immutable identity = %v, want conflict", err)
	}

	digest := "sha256:" + strings.Repeat("a", 64)
	expires := now.Add(time.Hour)
	op := githubConnectionOperation{
		ProjectID: "project-a", OperationID: "op-a", OperatorIssuer: "https://access.test",
		OperatorSubject: "operator-a", Role: "visual", Action: "create", StartDigest: digest,
		TargetAccountID: 30, TargetRepositoryName: "Test", TargetRepositoryNameNormalized: "test",
		VerifiedInstallationID: 20, GitHubOperatorID: 40, State: githubConnectionVerified, ExpiresAt: &expires,
	}
	other := op
	other.ProjectID, other.OperationID = "project-b", "op-b"
	ops := db.Collection(githubConnectionOperationsCollection)
	if _, err := ops.InsertMany(ctx, []any{op, other}); err != nil {
		t.Fatal(err)
	}
	for _, alter := range []func(*githubConnectionOperation){
		func(o *githubConnectionOperation) { o.ExpectedRoleRevision++ },
		func(o *githubConnectionOperation) { o.ExpectedConnectionRevision++ },
		func(o *githubConnectionOperation) { o.OperatorSubject = "another-operator" },
	} {
		stale := op
		alter(&stale)
		if err := claimGitHubRepositoryCreation(ctx, db, stale, digest, now); !errors.Is(err, ErrConflict) {
			t.Fatalf("altered claim = %v, want conflict", err)
		}
	}
	type result struct {
		op  githubConnectionOperation
		err error
	}
	results := make(chan result, 2)
	for _, contender := range []githubConnectionOperation{op, other} {
		go func() {
			results <- result{contender, claimGitHubRepositoryCreation(ctx, db, contender, digest, now)}
		}()
	}
	var winner githubConnectionOperation
	for range 2 {
		r := <-results
		switch {
		case r.err == nil && winner.OperationID == "":
			winner = r.op
		case errors.Is(r.err, ErrConflict):
		default:
			t.Fatalf("unexpected claim %q = %v", r.op.OperationID, r.err)
		}
	}
	if winner.OperationID == "" {
		t.Fatal("neither concurrent creation obtained the target reservation")
	}
	if err := claimGitHubRepositoryCreation(ctx, db, winner, digest, now); !errors.Is(err, ErrConflict) {
		t.Fatalf("replay of winning claim = %v, want conflict", err)
	}
	var claimed githubConnectionOperation
	if err := ops.FindOne(ctx, bson.M{"project_id": winner.ProjectID, "operation_id": winner.OperationID}).Decode(&claimed); err != nil {
		t.Fatal(err)
	}
	if claimed.State != githubConnectionCreating || !claimed.CreateReserved || claimed.ExpiresAt != nil || claimed.FinalizeDigest != digest {
		t.Fatalf("creation is not durably claimed: %+v", claimed)
	}
	claimed.State = githubConnectionUncertain
	if err := recordGitHubCreationResult(ctx, db, claimed, githubAppRepository{}, now); err != nil {
		t.Fatal(err)
	}
	claimed.State = githubConnectionAwaitingRepositoryAccess
	receipt := githubAppRepository{ID: 77, NodeID: "node-77", FullName: "company/test", DefaultBranch: "trunk"}
	if err := recordGitHubCreationResult(ctx, db, claimed, receipt, now); err != nil {
		t.Fatalf("late receipt rejected: %v", err)
	}
	otherReceipt := receipt
	otherReceipt.FullName = "company/renamed"
	if err := recordGitHubCreationResult(ctx, db, claimed, otherReceipt, now); !errors.Is(err, ErrConflict) {
		t.Fatalf("replacement receipt was accepted: %v", err)
	}
	var stored githubConnectionOperation
	if err := ops.FindOne(ctx, bson.M{"project_id": winner.ProjectID, "operation_id": winner.OperationID}).Decode(&stored); err != nil ||
		stored.ResultRepositoryID != receipt.ID || stored.ResultRepositoryFullName != receipt.FullName || stored.ResultRepositoryDefaultBranch != receipt.DefaultBranch {
		t.Fatalf("original receipt was not preserved: %+v, err=%v", stored, err)
	}

}
