package registry

import (
	"fmt"
	"reflect"
	"strings"
)

// validateMongoPlanningContract checks the objective, typed admission rules
// that can be decided from one immutable Mongo content revision. It does not
// treat free-text review claims or a caller-supplied guard result as proof.
func validateMongoPlanningContract(record TODORecord, content TODOAuthoredContent) error {
	operationalContent := content.TODOContent
	operationalContent.LegacyContext = nil
	if err := ValidateTODOContent(operationalContent, false); err != nil {
		return err
	}
	if err := validateMongoPlanningInputs(content); err != nil {
		return err
	}
	covered := make(map[string]bool, len(content.Criteria))
	for _, scenario := range content.ValidationScenarios {
		for _, criterionID := range scenario.CriterionIDs {
			covered[criterionID] = true
		}
	}
	for _, criterion := range content.Criteria {
		if !covered[criterion.ID] {
			return todoTeach("content.validation_scenarios", "cover criterion "+criterion.ID+" with an explicit validation scenario before approval", ErrConflict)
		}
	}
	if !(len(record.DiffExpectation) == 0 && len(content.DiffExpectation) == 0) && !reflect.DeepEqual(record.DiffExpectation, content.DiffExpectation) {
		return todoTeach("content.diff_expectation", "match the current Mongo TODO aggregate and immutable content revision", ErrConflict)
	}
	for i, scope := range content.DiffExpectation {
		field := fmt.Sprintf("content.diff_expectation[%d]", i)
		if foundationScope(scope.Repository) {
			return todoTeach(field+".repository", "omit Foundation repositories from the current v2 TODO contract", ErrConflict)
		}
		if !validGitCommit(scope.Baseline) {
			return todoTeach(field+".baseline", "identify the exact approved Git baseline commit", ErrConflict)
		}
		if !validGitRepositoryID(scope.RepositoryID) || !validDeliveryBranch(scope.DeliveryBranch) {
			return todoTeach(field+".repository_id/delivery_branch", "identify the exact implementing owner/repository and remote delivery branch before approval", ErrConflict)
		}
		if len(scope.ExpectedPaths) == 0 {
			return todoTeach(field+".expected_paths", "identify at least one approved attributable path for this repository", ErrConflict)
		}
		for _, expected := range scope.ExpectedPaths {
			for _, excluded := range scope.NotExpectedPaths {
				if expected == excluded {
					return todoTeach(field+".not_expected_paths", "not exclude the same path pattern required by expected_paths", ErrConflict)
				}
			}
		}
	}
	return nil
}

func validateMongoPlanningInputs(content TODOAuthoredContent) error {
	p := content.Planning
	if p == nil || (p.Complexity != "small" && p.Complexity != "medium" && p.Complexity != "big") {
		return todoTeach("content.planning.complexity", "declare small, medium or big planning complexity", ErrConflict)
	}
	if _, err := deriveTODOAuditFloors(p); err != nil {
		return err
	}
	if content.Architecture != nil && p.AuditTriggers.ArchitectureApplicability != "required" {
		return todoTeach("content.planning.audit_triggers.architecture_applicability", "declare required when the typed architecture decision is present", ErrConflict)
	}
	if p.Impact.CrossModule && p.AuditTriggers.BlastRadius == "local" || len(content.DiffExpectation) > 1 && p.AuditTriggers.BlastRadius == "local" {
		return todoTeach("content.planning.audit_triggers.blast_radius", "declare at least cross-module for cross-module or multi-repository scope", ErrConflict)
	}
	if p.Impact.PublicContract && !*p.AuditTriggers.ChangesPublicContract || p.Impact.RuntimeSensitive && !*p.AuditTriggers.TouchesRuntimeOrInfra {
		return todoTeach("content.planning.audit_triggers", "align audit triggers with declared planning impact", ErrConflict)
	}
	if p.Assumptions == nil || len(p.Assumptions) > 64 || p.Reviews == nil || len(p.Reviews) > 32 {
		return todoTeach("content.planning.assumptions/reviews", "provide bounded arrays of assumptions and human review references", ErrConflict)
	}
	seen := map[string]bool{}
	liveAssumptions := false
	for i, assumption := range p.Assumptions {
		field := fmt.Sprintf("content.planning.assumptions[%d]", i)
		if !validID(assumption.ID) || seen[assumption.ID] || (assumption.Status != "live" && assumption.Status != "resolved") || !validSummaryField(assumption.Statement, 2048) || assumption.CodeRefs == nil || len(assumption.CodeRefs) > 32 || assumption.Status == "live" && len(assumption.CodeRefs) == 0 {
			return todoTeach(field, "provide a unique ID, live or resolved status, statement and code references for each live assumption", ErrConflict)
		}
		seen[assumption.ID] = true
		liveAssumptions = liveAssumptions || assumption.Status == "live"
		for _, ref := range assumption.CodeRefs {
			if !validGitRepositoryID(ref.RepositoryID) || !validRepositoryPattern(ref.Path) || strings.ContainsAny(ref.Path, "*?[]") {
				return todoTeach(field+".code_refs", "identify an exact repository and safe repository-relative code path", ErrConflict)
			}
		}
	}
	reviews := map[string]bool{}
	for i, review := range p.Reviews {
		if (review.Kind != "plan" && review.Kind != "critique" && review.Kind != "coherence" && review.Kind != "architecture_opinion") || reviews[review.Kind] || !validSummaryField(review.Reference, 2048) {
			return todoTeach(fmt.Sprintf("content.planning.reviews[%d]", i), "provide a unique plan, critique, coherence or architecture_opinion review and its human review reference", ErrConflict)
		}
		reviews[review.Kind] = true
	}
	if !reviews["critique"] {
		return todoTeach("content.planning.reviews.critique", "record the required independent human critique reference", ErrConflict)
	}
	if (p.AuditTriggers.ArchitectureApplicability == "required" || content.Architecture != nil) && !reviews["architecture_opinion"] {
		return todoTeach("content.planning.reviews.architecture_opinion", "record the required human architecture decision review reference", ErrConflict)
	}
	if (p.Complexity == "medium" || p.Complexity == "big") && !reviews["plan"] {
		return todoTeach("content.planning.reviews.plan", "record the required human plan review reference", ErrConflict)
	}
	if liveAssumptions && !reviews["coherence"] {
		return todoTeach("content.planning.reviews.coherence", "record the human assumption-versus-code review reference", ErrConflict)
	}
	return nil
}

// deriveTODOAuditFloors computes the legacy audit decisions from declared,
// typed risk inputs. These are requirements, not claims that reviews happened.
func deriveTODOAuditFloors(p *TODOPlanning) (map[string]string, error) {
	if p.AuditTriggers == nil {
		return nil, todoTeach("content.planning.audit_triggers", "declare blast_radius, architecture_applicability and every audit trigger explicitly", ErrConflict)
	}
	t := p.AuditTriggers
	if t.BlastRadius != "local" && t.BlastRadius != "cross-module" && t.BlastRadius != "cross-stack" {
		return nil, todoTeach("content.planning.audit_triggers.blast_radius", "declare local, cross-module or cross-stack", ErrConflict)
	}
	if t.ArchitectureApplicability != "required" && t.ArchitectureApplicability != "not_needed" {
		return nil, todoTeach("content.planning.audit_triggers.architecture_applicability", "declare required or not_needed", ErrConflict)
	}
	inputs := []struct {
		name  string
		value *bool
	}{
		{"behavioral_change_or_bugfix", t.BehavioralChangeOrBugfix}, {"changes_public_contract", t.ChangesPublicContract}, {"touches_auth_or_tenant", t.TouchesAuthOrTenant}, {"touches_runtime_or_infra", t.TouchesRuntimeOrInfra}, {"touches_tests", t.TouchesTests}, {"critical_user_journey", t.CriticalUserJourney}, {"release_or_promotion_critical", t.ReleaseOrPromotionCritical}, {"high_severity_plan_review_issue", t.HighSeverityPlanReviewIssue}, {"explicit_three_lane_request", t.ExplicitThreeLaneRequest},
	}
	var missing []string
	for _, in := range inputs {
		if in.value == nil {
			missing = append(missing, in.name)
		}
	}
	if len(missing) > 0 {
		return nil, todoTeach("content.planning.audit_triggers", "explicitly declare true or false for: "+strings.Join(missing, ", "), ErrConflict)
	}
	behavior, public, auth, runtime, tests := *t.BehavioralChangeOrBugfix, *t.ChangesPublicContract, *t.TouchesAuthOrTenant, *t.TouchesRuntimeOrInfra, *t.TouchesTests
	critical, release, issue, explicit := *t.CriticalUserJourney, *t.ReleaseOrPromotionCritical, *t.HighSeverityPlanReviewIssue, *t.ExplicitThreeLaneRequest
	big, medium := p.Complexity == "big", p.Complexity == "medium"
	crossModule, crossStack := t.BlastRadius == "cross-module", t.BlastRadius == "cross-stack"
	floors := map[string]string{"critique": "required", "final_review": "required", "test_quality_audit": "not_needed", "triple_review": "not_needed", "security_review": "not_needed", "performance_concurrency": "not_needed", "verification_debt": "not_needed", "architecture_decision_review": "not_needed", "architecture_adherence_review": "not_needed"}
	if behavior || public || tests || critical || release {
		floors["test_quality_audit"] = "required"
	} else if big || medium {
		floors["test_quality_audit"] = "recommended"
	}
	if explicit || release && (big || crossStack || critical || runtime || auth || issue) {
		floors["triple_review"] = "required"
	} else if big && (public || critical || runtime || auth) || release && crossModule {
		floors["triple_review"] = "recommended"
	}
	if auth {
		floors["security_review"] = "required"
	} else if public && critical {
		floors["security_review"] = "recommended"
	}
	if runtime {
		floors["performance_concurrency"] = "required"
	} else if release && (critical || crossModule || crossStack || big) {
		floors["performance_concurrency"] = "recommended"
	}
	if big || medium || release {
		floors["verification_debt"] = "required"
	} else if public || tests {
		floors["verification_debt"] = "recommended"
	}
	if t.ArchitectureApplicability == "required" {
		floors["architecture_decision_review"] = "required"
		floors["architecture_adherence_review"] = "required"
	}
	return floors, nil
}

// Delivery citations are human-authored and digest-bound; their presence is
// not a claim that the API independently evaluated the review's quality.
func isTODOAuditDeliveryKind(kind string) bool {
	switch kind {
	case "final_review", "test_quality_audit", "triple_review", "security_review", "performance_concurrency", "verification_debt", "architecture_adherence_review":
		return true
	default:
		return false
	}
}

func validateTODOAuditDeliveryReferences(record TODORecord, digest string) error {
	if record.Content == nil || record.Content.Planning == nil || record.Content.Planning.AuditTriggers == nil {
		return nil // verified pre-cutover stages are not retroactively revalidated
	}
	for _, decision := range record.Content.Decisions {
		kind := "decision_adherence:" + decision.ID
		field := "proof_refs." + kind
		var matched *TODOProofReference
		for _, ref := range record.ProofReferences {
			if ref.Kind != kind || ref.Source != "human-review" || ref.SourceDigest != digest || ref.VerificationState != "unverified" || !validSummaryField(ref.Reference, 2048) {
				continue
			}
			if matched != nil {
				return todoTeach(field, "retain exactly one current adherence citation for this decision", ErrConflict)
			}
			copy := ref
			matched = &copy
		}
		if matched == nil {
			return todoTeach(field, "record one adherence citation for decision "+decision.ID+" bound to the current TODO content digest before local delivery", ErrConflict)
		}
		if err := requireReviewGo(*matched, field); err != nil {
			return err
		}
	}
	floors, err := deriveTODOAuditFloors(record.Content.Planning)
	if err != nil {
		return err
	}
	for _, name := range []string{"final_review", "test_quality_audit", "triple_review", "security_review", "performance_concurrency", "verification_debt", "architecture_adherence_review"} {
		if floors[name] != "required" {
			continue
		}
		found := false
		var matchedRef *TODOProofReference
		for _, ref := range record.ProofReferences {
			if ref.Kind == name && ref.Source == "human-review" && ref.SourceDigest == digest && ref.VerificationState == "unverified" && validSummaryField(ref.Reference, 2048) {
				if matchedRef != nil {
					return todoTeach("proof_refs."+name, "retain exactly one current human-review citation for this required review", ErrConflict)
				}
				matchedCopy := ref
				matchedRef = &matchedCopy
				found = true
			}
		}
		if !found {
			return todoTeach("proof_refs."+name, "record the required human review citation bound to the current content digest before delivery promotion", ErrConflict)
		}
		if err := requireReviewOutcome(record, *matchedRef, name, digest, "proof_refs."+name); err != nil {
			return err
		}
	}
	return nil
}

func requireReviewGo(reference TODOProofReference, field string) error {
	switch reference.Outcome {
	case "go":
		return nil
	case "no-go":
		return todoTeach(field+".outcome", "resolve the review's no-go finding and record a new go citation for the current content digest", ErrConflict)
	case "waived":
		return todoTeach(field+".outcome", "record a review-specific governed waiver approval bound to this subject and digest; generic waiver references and whole-TODO APROVADO do not authorize it", ErrConflict)
	case "":
		return todoTeach(field+".outcome", "set the explicit review outcome to go before this stage", ErrConflict)
	default:
		return todoTeach(field+".outcome", "use one of go, no-go, or waived", ErrInvalid)
	}
}

func requireReviewOutcome(record TODORecord, reference TODOProofReference, kind, digest, field string) error {
	if reference.Outcome == "waived" {
		for _, waiver := range record.WaiverReferences {
			if waiver.Kind == "review-waiver:"+kind && waiver.Source == "mongo-admin-waiver" && waiver.Reference != "" && waiver.SourceDigest == digest && waiver.VerificationState == "verified" {
				return nil
			}
		}
		return todoTeach(field+".outcome", "grant the matching review-specific waiver with `builder-project grant-todo-review-waiver` for this exact Project/TODO, review kind, and current content digest; generic waivers and whole-TODO APROVADO do not authorize it", ErrConflict)
	}
	return requireReviewGo(reference, field)
}
