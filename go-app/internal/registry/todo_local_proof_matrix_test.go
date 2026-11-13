package registry

import (
	"strings"
	"testing"
)

func TestVerifyLocalRequirementReferencesBindsCriteriaAndScenariosToTypedChecks(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	content := &TODOContent{
		SchemaVersion:       todoContentSchemaVersion,
		LocalChecks:         []TODOLocalCheck{{ID: "G-TEST", RepositoryID: "belluga/repo", Argv: []string{"go", "test"}, WorkingDirectory: ".", InputPaths: []string{"**"}}},
		Criteria:            []TODOContentItem{{ID: "C-1", Text: "Unit tests pass."}},
		ValidationScenarios: []TODOValidationScenario{{ID: "S-1", CriterionIDs: []string{"C-1"}, Method: "Run G-TEST.", Expected: "Exit zero."}},
	}
	result := &LocalProofResult{Checks: []localProofCheck{{ID: "G-TEST", RepositoryID: "belluga/repo", ExitCode: 0}}}
	refs := []TODOProofReference{
		{Kind: "criterion:C-1", Source: "paced-local-runner", Reference: "G-TEST", SourceDigest: digest, VerificationState: "unverified"},
		{Kind: "scenario:S-1", Source: "paced-local-runner", Reference: "G-TEST", SourceDigest: digest, VerificationState: "unverified"},
	}
	verified, err := verifyLocalRequirementReferences(content, refs, result, digest)
	if err != nil || len(verified) != 2 || verified[0].VerificationState != "verified" || verified[1].VerificationState != "verified" {
		t.Fatalf("verified refs=%+v err=%v", verified, err)
	}
	if refs[0].VerificationState != "unverified" {
		t.Fatal("verification mutated caller references")
	}
	result.Checks[0].ExitCode = 1
	if _, err := verifyLocalRequirementReferences(content, refs, result, digest); err == nil || !strings.Contains(err.Error(), "exited zero") {
		t.Fatalf("failing typed check accepted: %v", err)
	}
}

func TestVerifyLocalRequirementReferencesRejectsUnknownCheckAndStaleCoverage(t *testing.T) {
	digest := "sha256:" + strings.Repeat("b", 64)
	content := &TODOContent{SchemaVersion: todoContentSchemaVersion, LocalChecks: []TODOLocalCheck{{ID: "G-TEST", RepositoryID: "belluga/repo", Argv: []string{"go", "test"}, WorkingDirectory: ".", InputPaths: []string{"**"}}}, Criteria: []TODOContentItem{{ID: "C-1", Text: "Criterion."}}, ValidationScenarios: []TODOValidationScenario{{ID: "S-1", CriterionIDs: []string{"C-1"}, Method: "Run check.", Expected: "Pass."}}}
	result := &LocalProofResult{Checks: []localProofCheck{{ID: "G-TEST", RepositoryID: "belluga/repo", ExitCode: 0}}}
	refs := []TODOProofReference{
		{Kind: "criterion:C-1", Source: "paced-local-runner", Reference: "G-UNKNOWN", SourceDigest: digest, VerificationState: "unverified"},
		{Kind: "scenario:S-1", Source: "paced-local-runner", Reference: "G-TEST", SourceDigest: digest, VerificationState: "unverified"},
	}
	if _, err := verifyLocalRequirementReferences(content, refs, result, digest); err == nil || !strings.Contains(err.Error(), "present in this exact local result") {
		t.Fatalf("unknown check accepted: %v", err)
	}
	refs[0].Reference = "G-TEST"
	if _, err := verifyLocalRequirementReferences(content, refs, result, "sha256:"+strings.Repeat("c", 64)); err == nil || !strings.Contains(err.Error(), "proof_refs") {
		t.Fatalf("stale-only refs satisfied current coverage: %v", err)
	}
}

func TestLocalProofRequirementMatchesDeliveryScope(t *testing.T) {
	if !requiresLocalProofForTODO(TODORecord{}) {
		t.Fatal("empty repository scope must not bypass the local runner")
	}
	if !requiresLocalProofForTODO(TODORecord{DiffExpectation: []TODORepositoryScope{{Repository: "Builder"}}}) {
		t.Fatal("implementation scope did not require the local runner")
	}
	if requiresLocalProofForTODO(TODORecord{DiffExpectation: []TODORepositoryScope{{Repository: "Foundation"}}}) {
		t.Fatal("Foundation-only publication unexpectedly required local code proof")
	}
	if !requiresLocalProofForTODO(TODORecord{Source: TODOSource{Kind: "mongo-content-revision"}, DiffExpectation: []TODORepositoryScope{{Repository: "Foundation", RepositoryID: "owner/repo"}}}) {
		t.Fatal("Mongo code-backed TODO trusted a Foundation label as a proof bypass")
	}
	if !requiresLocalProofForTODO(TODORecord{DiffExpectation: []TODORepositoryScope{{Repository: "Foundation"}, {Repository: "Builder"}}}) {
		t.Fatal("mixed Foundation and implementation scope skipped the local runner")
	}
}
