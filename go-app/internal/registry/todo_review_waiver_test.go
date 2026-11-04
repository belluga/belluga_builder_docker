package registry

import "testing"

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
	content := TODOContent{Planning: &TODOPlanning{Reviews: []TODOPlanningReview{{Kind: "critique", Reference: "review/critique"}}, AuditTriggers: testAuditTriggers()}}
	if !knownRequiredReviewKind(content, "planning_review_critique") {
		t.Fatal("current planning review was not recognized")
	}
	if knownRequiredReviewKind(content, "planning_review_unknown") || knownRequiredReviewKind(content, "generic-waiver") {
		t.Fatal("unknown planning or generic waiver kind was recognized")
	}
}
