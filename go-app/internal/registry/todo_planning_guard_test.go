package registry

import (
	"context"
	"strings"
	"testing"
)

func TestPlanningCodeAnchorFailsClosedWithoutApprovedCodeFact(t *testing.T) {
	content := TODOAuthoredContent{TODOContent: TODOContent{Planning: &TODOPlanning{Assumptions: []TODOPlanningAssumption{{Status: "live", CodeRefs: []TODOPlanningCodeRef{{RepositoryID: "owner/other", Path: "src/main.go"}}}}}}, DiffExpectation: []TODORepositoryScope{{RepositoryID: "owner/repo", Baseline: strings.Repeat("a", 40), ExpectedPaths: []string{"src/**"}, NotExpectedPaths: []string{"src/generated/**"}}}}
	store := &MongoStore{}
	if err := store.verifyPlanningCodeAnchors(context.Background(), "company-a", "project-a", content); err == nil || !strings.Contains(err.Error(), "code_refs") {
		t.Fatalf("unapproved repo accepted: %v", err)
	}
	content.Planning.Assumptions[0].CodeRefs[0] = TODOPlanningCodeRef{RepositoryID: "owner/repo", Path: "docs/overview.md"}
	if err := store.verifyPlanningCodeAnchors(context.Background(), "company-a", "project-a", content); err == nil {
		t.Fatal("documentation-only anchor accepted")
	}
	content.Planning.Assumptions[0].CodeRefs[0] = TODOPlanningCodeRef{RepositoryID: "owner/repo", Path: "outside/main.go"}
	if err := store.verifyPlanningCodeAnchors(context.Background(), "company-a", "project-a", content); err == nil || !strings.Contains(err.Error(), "code_refs") {
		t.Fatalf("out-of-scope code anchor accepted: %v", err)
	}
	content.Planning.Assumptions[0].CodeRefs[0].Path = "src/generated/main.go"
	if err := store.verifyPlanningCodeAnchors(context.Background(), "company-a", "project-a", content); err == nil || !strings.Contains(err.Error(), "code_refs") {
		t.Fatalf("excluded code anchor accepted: %v", err)
	}
	content.Planning.Assumptions[0].CodeRefs[0].Path = "src/main.go"
	if err := store.verifyPlanningCodeAnchors(context.Background(), "company-a", "project-a", content); err == nil || !strings.Contains(err.Error(), "Reader") {
		t.Fatalf("missing Reader accepted: %v", err)
	}
}

func testAuditTriggers() *TODOAuditTriggers {
	f := false
	return &TODOAuditTriggers{BlastRadius: "local", ArchitectureApplicability: "not_needed", BehavioralChangeOrBugfix: &f, ChangesPublicContract: &f, TouchesAuthOrTenant: &f, TouchesRuntimeOrInfra: &f, TouchesTests: &f, CriticalUserJourney: &f, ReleaseOrPromotionCritical: &f, HighSeverityPlanReviewIssue: &f, ExplicitThreeLaneRequest: &f}
}

func TestMongoAuditFloorsRequireExplicitInputsAndDeriveEscalation(t *testing.T) {
	p := &TODOPlanning{Complexity: "small", AuditTriggers: testAuditTriggers()}
	p.AuditTriggers.TouchesTests = nil
	if _, err := deriveTODOAuditFloors(p); err == nil || !strings.Contains(err.Error(), "touches_tests") {
		t.Fatalf("missing trigger: %v", err)
	}
	p.AuditTriggers = testAuditTriggers()
	floors, err := deriveTODOAuditFloors(p)
	if err != nil || floors["critique"] != "required" || floors["test_quality_audit"] != "not_needed" {
		t.Fatalf("baseline floors: %v %v", floors, err)
	}
	yes := true
	p.Complexity = "big"
	p.AuditTriggers.ReleaseOrPromotionCritical = &yes
	p.AuditTriggers.TouchesAuthOrTenant = &yes
	p.AuditTriggers.TouchesRuntimeOrInfra = &yes
	p.AuditTriggers.ArchitectureApplicability = "required"
	floors, err = deriveTODOAuditFloors(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"triple_review", "test_quality_audit", "security_review", "performance_concurrency", "verification_debt", "architecture_decision_review", "architecture_adherence_review"} {
		if floors[name] != "required" {
			t.Errorf("%s = %s", name, floors[name])
		}
	}
}

func TestMongoAuditDeliveryReferencesRequireCurrentDigest(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	record := TODORecord{Content: &TODOContent{Planning: &TODOPlanning{Complexity: "small", AuditTriggers: testAuditTriggers()}}}
	if err := validateTODOAuditDeliveryReferences(record, digest); err == nil || !strings.Contains(err.Error(), "final_review") {
		t.Fatalf("missing final review: %v", err)
	}
	record.ProofReferences = []TODOProofReference{{Kind: "final_review", Source: "human-review", Reference: "review/final", SourceDigest: "sha256:" + strings.Repeat("b", 64), VerificationState: "unverified", Outcome: "go"}}
	if err := validateTODOAuditDeliveryReferences(record, digest); err == nil {
		t.Fatal("stale review accepted")
	}
	record.ProofReferences[0].SourceDigest = digest
	if err := validateTODOAuditDeliveryReferences(record, digest); err != nil {
		t.Fatalf("current citation: %v", err)
	}
	for _, outcome := range []string{"", "no-go", "waived"} {
		record.ProofReferences[0].Outcome = outcome
		if err := validateTODOAuditDeliveryReferences(record, digest); err == nil || !strings.Contains(err.Error(), "proof_refs.final_review.outcome") {
			t.Fatalf("required final-review outcome %q was accepted: %v", outcome, err)
		}
	}
	record.ProofReferences[0].Outcome = "go"
}

func TestMongoDecisionAdherenceRequiresOneCurrentGoCitationPerDecision(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	stale := "sha256:" + strings.Repeat("b", 64)
	record := TODORecord{
		Content: &TODOContent{SchemaVersion: todoContentSchemaVersion,
			Decisions: []TODOContentItem{{ID: "d-1", Text: "Keep Mongo as the TODO authority."}},
			Planning:  &TODOPlanning{Complexity: "small", AuditTriggers: testAuditTriggers()}},
		ProofReferences: []TODOProofReference{{Kind: "final_review", Source: "human-review", Reference: "review/final", SourceDigest: digest, VerificationState: "unverified", Outcome: "go"}},
	}
	if err := validateTODOAuditDeliveryReferences(record, digest); err == nil || !strings.Contains(err.Error(), "decision_adherence:d-1") {
		t.Fatalf("missing decision adherence accepted: %v", err)
	}
	adherence := TODOProofReference{Kind: "decision_adherence:d-1", Source: "human-review", Reference: "review/decision-d-1", SourceDigest: stale, VerificationState: "unverified", Outcome: "go"}
	record.ProofReferences = append(record.ProofReferences, adherence)
	if err := validateTODOAuditDeliveryReferences(record, digest); err == nil || !strings.Contains(err.Error(), "decision_adherence:d-1") {
		t.Fatalf("stale decision adherence accepted: %v", err)
	}
	record.ProofReferences[1].SourceDigest = digest
	record.ProofReferences[1].Outcome = "no-go"
	if err := validateTODOAuditDeliveryReferences(record, digest); err == nil || !strings.Contains(err.Error(), "no-go") {
		t.Fatalf("no-go decision adherence accepted: %v", err)
	}
	record.ProofReferences[1].Outcome = "go"
	if err := validateTODOAuditDeliveryReferences(record, digest); err != nil {
		t.Fatalf("current decision adherence rejected: %v", err)
	}
	record.ProofReferences = append(record.ProofReferences, record.ProofReferences[1])
	if err := validateTODOAuditDeliveryReferences(record, digest); err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("duplicate decision adherence accepted: %v", err)
	}
}

func TestMongoAuditDeliveryReferencesEnforceRequiredButNotRecommendedReviews(t *testing.T) {
	digest := "sha256:" + strings.Repeat("f", 64)
	planning := &TODOPlanning{Complexity: "medium", AuditTriggers: testAuditTriggers()}
	floors, err := deriveTODOAuditFloors(planning)
	if err != nil || floors["test_quality_audit"] != "recommended" {
		t.Fatalf("expected a recommended test-quality review, got floors=%v err=%v", floors, err)
	}
	record := TODORecord{
		Content: &TODOContent{SchemaVersion: todoContentSchemaVersion, Planning: planning},
		ProofReferences: []TODOProofReference{
			{Kind: "final_review", Source: "human-review", Reference: "review/final", SourceDigest: digest, VerificationState: "unverified", Outcome: "go"},
			{Kind: "verification_debt", Source: "human-review", Reference: "review/debt", SourceDigest: digest, VerificationState: "unverified", Outcome: "go"},
			// Builder's declared Go/Docker/React/Vite capabilities do not, by
			// themselves, make browser or Flutter review evidence applicable.
			{Kind: "browser_review", Source: "human-review", Reference: "review/browser", SourceDigest: digest, VerificationState: "unverified"},
			{Kind: "flutter_device_review", Source: "human-review", Reference: "review/flutter", SourceDigest: digest, VerificationState: "unverified"},
		},
	}
	if err := validateTODOAuditDeliveryReferences(record, digest); err != nil {
		t.Fatalf("recommended/unrelated reviews blocked delivery: %v", err)
	}

	yes := true
	planning.AuditTriggers.TouchesTests = &yes
	floors, err = deriveTODOAuditFloors(planning)
	if err != nil || floors["test_quality_audit"] != "required" {
		t.Fatalf("expected required test-quality review, got floors=%v err=%v", floors, err)
	}
	if err := validateTODOAuditDeliveryReferences(record, digest); err == nil || !strings.Contains(err.Error(), "test_quality_audit") {
		t.Fatalf("missing required review was accepted: %v", err)
	}
	record.ProofReferences = append(record.ProofReferences, TODOProofReference{
		Kind: "test_quality_audit", Source: "human-review", Reference: "review/tests", SourceDigest: "sha256:" + strings.Repeat("e", 64), VerificationState: "unverified", Outcome: "go",
	})
	if err := validateTODOAuditDeliveryReferences(record, digest); err == nil || !strings.Contains(err.Error(), "test_quality_audit") {
		t.Fatalf("wrong-digest required review was accepted: %v", err)
	}
	record.ProofReferences[len(record.ProofReferences)-1].SourceDigest = digest
	if err := validateTODOAuditDeliveryReferences(record, digest); err != nil {
		t.Fatalf("current required citation rejected: %v", err)
	}
}

func TestRequiredReviewOutcomeMustBeGo(t *testing.T) {
	base := TODOProofReference{Kind: "final_review", Source: "human-review", Reference: "review/final", SourceDigest: "sha256:" + strings.Repeat("a", 64), VerificationState: "unverified"}
	for _, tc := range []struct {
		name    string
		outcome string
		teach   string
	}{
		{name: "missing", outcome: "", teach: ".outcome"},
		{name: "no-go", outcome: "no-go", teach: "no-go"},
		{name: "waived", outcome: "waived", teach: "review-specific governed waiver"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ref := base
			ref.Outcome = tc.outcome
			if err := requireReviewGo(ref, "proof_refs.final_review"); err == nil || !strings.Contains(err.Error(), tc.teach) {
				t.Fatalf("outcome %q was not blocked with expected TEACH: %v", tc.outcome, err)
			}
		})
	}
	base.Outcome = "go"
	if err := requireReviewGo(base, "proof_refs.final_review"); err != nil {
		t.Fatalf("go outcome rejected: %v", err)
	}
	base.Outcome = "pass"
	if validProofReference(base) {
		t.Fatal("unsupported outcome was valid proof data")
	}
}

func TestMongoPlanningGuardRequiresCoveredCriteriaAndExplicitCodeOrigin(t *testing.T) {
	content := TODOAuthoredContent{
		TODOContent: validTODOContent(),
		Title:       "TODO: Guarded planning",
		Objective:   "Keep the Mongo TODO contract authoritative.",
		DiffExpectation: []TODORepositoryScope{{
			Repository: "Builder", Baseline: strings.Repeat("a", 40),
			RepositoryID: "belluga/belluga_builder_docker", DeliveryBranch: "r-10-rc",
			ExpectedPaths: []string{"go-app/internal/registry/**"}, NotExpectedPaths: []string{"flutter-app/**"},
		}},
		AffectedFiles: []string{},
	}
	record := TODORecord{DiffExpectation: content.DiffExpectation}
	content.Planning = &TODOPlanning{Complexity: "small", AuditTriggers: testAuditTriggers(), Assumptions: []TODOPlanningAssumption{}, Reviews: []TODOPlanningReview{{Kind: "critique", Reference: "review/critique"}}}
	if err := validateMongoPlanningContract(record, content); err != nil {
		t.Fatalf("valid typed planning contract rejected: %v", err)
	}
	archived := content
	archived.LegacyContext = []TODOLegacyContext{{Body: "archived only"}}
	if err := validateMongoPlanningContract(record, archived); err != nil {
		t.Fatalf("archived legacy data influenced v2 planning: %v", err)
	}
	withoutOrigin := content
	withoutOrigin.DiffExpectation = append([]TODORepositoryScope{}, content.DiffExpectation...)
	withoutOrigin.DiffExpectation[0].RepositoryID = ""
	withoutOrigin.DiffExpectation[0].DeliveryBranch = ""
	record.DiffExpectation = withoutOrigin.DiffExpectation
	if err := validateMongoPlanningContract(record, withoutOrigin); err == nil || !strings.Contains(err.Error(), "repository_id/delivery_branch") {
		t.Fatalf("missing code origin accepted or wrong TEACH: %v", err)
	}
	record.DiffExpectation = content.DiffExpectation
	uncovered := content
	uncovered.Criteria = append([]TODOContentItem{}, content.Criteria...)
	uncovered.Criteria = append(uncovered.Criteria, TODOContentItem{ID: "uncovered", Text: "A new acceptance criterion must have a validation scenario."})
	if err := validateMongoPlanningContract(record, uncovered); err == nil || !strings.Contains(err.Error(), "validation_scenarios") {
		t.Fatalf("uncovered criterion accepted or wrong TEACH: %v", err)
	}
	overlap := content
	overlap.DiffExpectation = append([]TODORepositoryScope{}, content.DiffExpectation...)
	overlap.DiffExpectation[0].NotExpectedPaths = []string{"go-app/internal/registry/**"}
	record.DiffExpectation = overlap.DiffExpectation
	if err := validateMongoPlanningContract(record, overlap); err == nil || !strings.Contains(err.Error(), "not_expected_paths") {
		t.Fatalf("contradictory diff scope accepted or wrong TEACH: %v", err)
	}
	emptyPaths := content
	emptyPaths.DiffExpectation = append([]TODORepositoryScope{}, content.DiffExpectation...)
	emptyPaths.DiffExpectation[0].ExpectedPaths = []string{}
	record.DiffExpectation = emptyPaths.DiffExpectation
	if err := validateMongoPlanningContract(record, emptyPaths); err == nil || !strings.Contains(err.Error(), "expected_paths") {
		t.Fatalf("empty approved scope accepted or wrong TEACH: %v", err)
	}
	foundation := content
	foundation.DiffExpectation = append([]TODORepositoryScope{}, content.DiffExpectation...)
	foundation.DiffExpectation[0].Repository = "Foundation"
	record.DiffExpectation = foundation.DiffExpectation
	if err := validateMongoPlanningContract(record, foundation); err == nil || !strings.Contains(err.Error(), "diff_expectation[0].repository") {
		t.Fatalf("Foundation scope accepted for v2: %v", err)
	}
}

func TestMongoPlanningGuardRequiresRiskDrivenReviewReferences(t *testing.T) {
	content := TODOAuthoredContent{TODOContent: validTODOContent(), DiffExpectation: []TODORepositoryScope{}, AffectedFiles: []string{}}
	record := TODORecord{DiffExpectation: content.DiffExpectation}
	if err := validateMongoPlanningContract(record, content); err == nil || !strings.Contains(err.Error(), "content.planning") {
		t.Fatalf("missing planning inputs accepted: %v", err)
	}
	content.Planning = &TODOPlanning{Complexity: "big", AuditTriggers: testAuditTriggers(), Assumptions: []TODOPlanningAssumption{}, Reviews: []TODOPlanningReview{}}
	if err := validateMongoPlanningContract(record, content); err == nil || !strings.Contains(err.Error(), "critique") {
		t.Fatalf("missing required critique accepted: %v", err)
	}
	content.Planning.Reviews = []TODOPlanningReview{{Kind: "critique", Reference: "review/critique-1"}, {Kind: "plan", Reference: "review/plan-1"}}
	if err := validateMongoPlanningContract(record, content); err != nil {
		t.Fatalf("bounded planning inputs rejected: %v", err)
	}
	content.Planning.Assumptions = []TODOPlanningAssumption{{ID: "a-1", Status: "resolved", Statement: "This assumption was resolved.", CodeRefs: []TODOPlanningCodeRef{}}}
	if err := validateMongoPlanningContract(record, content); err != nil {
		t.Fatalf("resolved assumption required a fabricated code path: %v", err)
	}
	content.Planning.Assumptions[0].Status = "live"
	if err := validateMongoPlanningContract(record, content); err == nil || !strings.Contains(err.Error(), "assumptions") {
		t.Fatalf("live assumption without code references accepted: %v", err)
	}
	content.Planning.Assumptions[0].CodeRefs = []TODOPlanningCodeRef{{RepositoryID: "owner/repo", Path: "src/**"}}
	if err := validateMongoPlanningContract(record, content); err == nil || !strings.Contains(err.Error(), "code_refs") {
		t.Fatalf("wildcard code reference accepted: %v", err)
	}
	content.Planning.Assumptions[0].CodeRefs[0].Path = "src/main.go"
	if err := validateMongoPlanningContract(record, content); err == nil || !strings.Contains(err.Error(), "coherence") {
		t.Fatalf("missing coherence review accepted: %v", err)
	}
	content.Planning.Reviews = append(content.Planning.Reviews, TODOPlanningReview{Kind: "coherence", Reference: "review/coherence-1"})
	if err := validateMongoPlanningContract(record, content); err != nil {
		t.Fatalf("live assumption with review rejected: %v", err)
	}
}
