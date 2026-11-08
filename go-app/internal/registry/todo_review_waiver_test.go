package registry

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestReviewWaiverMustExactlyMatchSubjectAndDigest(t *testing.T) {
	const digest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	ref := TODOProofReference{Kind: "final_review", Source: "human-review", Reference: "review-final", SourceDigest: digest, VerificationState: "unverified", Outcome: "waived"}
	waiver := TODOProofReference{Kind: "review-waiver:final_review", Source: "mongo-admin-waiver", Reference: "waiver-1", SourceDigest: digest, VerificationState: "verified"}
	record := TODORecord{WaiverReferences: []TODOProofReference{waiver}}
	if err := requireReviewOutcome(record, ref, "final_review", digest, "proof_refs.final_review"); err != nil {
		t.Fatalf("exact current review waiver rejected: %v", err)
	}
	for name, bad := range map[string]TODOProofReference{
		"wrong digest": func() TODOProofReference {
			x := waiver
			x.SourceDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
			return x
		}(),
		"wrong subject": func() TODOProofReference { x := waiver; x.Kind = "review-waiver:security_review"; return x }(),
		"wrong source":  func() TODOProofReference { x := waiver; x.Source = "generic-waiver"; return x }(),
		"unverified":    func() TODOProofReference { x := waiver; x.VerificationState = "unverified"; return x }(),
	} {
		t.Run(name, func(t *testing.T) {
			if err := requireReviewOutcome(TODORecord{WaiverReferences: []TODOProofReference{bad}}, ref, "final_review", digest, "proof_refs.final_review"); err == nil {
				t.Fatal("unmatched waiver authorized a waived review")
			}
		})
	}
	if err := requireReviewOutcome(TODORecord{}, ref, "final_review", digest, "proof_refs.final_review"); err == nil {
		t.Fatal("missing review-specific waiver authorized a waived review")
	}
}

func TestReviewWaiverKindMustBeCurrentAndRequired(t *testing.T) {
	if knownRequiredReviewKind(TODOContent{}, "final_review") {
		t.Fatal("delivery review without planning must not be recognized")
	}
	content := TODOContent{Planning: &TODOPlanning{Reviews: []TODOPlanningReview{{Kind: "critique", Reference: "review/critique"}}, AuditTriggers: testAuditTriggers()}}
	if !knownRequiredReviewKind(content, "planning_review_critique") {
		t.Fatal("current planning review was not recognized")
	}
	if knownRequiredReviewKind(content, "planning_review_unknown") || knownRequiredReviewKind(content, "generic-waiver") {
		t.Fatal("unknown planning or generic waiver kind was recognized")
	}
}

func TestReviewWaiverWithoutPlanningReturnsTeach(t *testing.T) {
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
	t.Cleanup(func() {
		_ = store.client.Database(store.tenantDatabase("company-a")).Drop(context.Background())
		_ = store.control.Drop(context.Background())
		_ = store.Close(context.Background())
	})
	if _, err := registerTestProjectWithBinding(ctx, store, Registration{CompanyID: "company-a", CompanyName: "Company A", ProjectID: "builder", ProjectName: "Builder", Binding: validTestBinding()}); err != nil {
		t.Fatal(err)
	}
	if err := store.ProvisionTODOCollection(ctx, "company-a"); err != nil {
		t.Fatal(err)
	}
	if err := store.ProvisionTODOHistory(ctx, "company-a"); err != nil {
		t.Fatal(err)
	}
	record := mongoBornTODOFixture()
	record.ID = "waiver-without-planning"
	if _, err := store.AdmitTODO(ctx, "company-a", "builder", TODOAdmissionRequest{SchemaVersion: todoCreationSchemaVersion, OperationID: "waiver-test-create", TODO: record}); err != nil {
		t.Fatal(err)
	}
	current, err := store.ReadTODO(ctx, "company-a", "builder", record.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.GrantTODOReviewWaiver(ctx, "company-a", TODOReviewWaiverRequest{
		ProjectID: "builder", TODOID: record.ID, ExpectedRevision: &current.TODORevision,
		ExpectedContentDigest: current.ContentDigest, OperationID: "missing-planning-waiver",
		ReviewKind: "final_review", Reason: "Verify missing planning is rejected safely",
	})
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "review_kind") {
		t.Fatalf("waiver without planning = %v; want field-addressed TEACH", err)
	}
	after, err := store.ReadTODO(ctx, "company-a", "builder", record.ID, nil)
	if err != nil || after.TODORevision != current.TODORevision {
		t.Fatalf("rejected waiver changed TODO revision: before=%d after=%d err=%v", current.TODORevision, after.TODORevision, err)
	}
	events, err := store.client.Database(store.tenantDatabase("company-a")).Collection(todoEventsCollection).CountDocuments(ctx, bson.D{{Key: "todo_id", Value: record.ID}, {Key: "kind", Value: reviewWaiverEventKind}})
	if err != nil || events != 0 {
		t.Fatalf("rejected waiver wrote %d events: %v", events, err)
	}
}
