package registry

import (
	"errors"
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
	if _, err := verifyLocalRequirementReferences(content, refs, result, digest); err == nil || !strings.Contains(err.Error(), "check ID") {
		t.Fatalf("unknown check accepted: %v", err)
	}
	refs[0].Reference = "G-TEST"
	if _, err := verifyLocalRequirementReferences(content, refs, result, "sha256:"+strings.Repeat("c", 64)); err == nil || !strings.Contains(err.Error(), "proof_refs") {
		t.Fatalf("stale-only refs satisfied current coverage: %v", err)
	}
}

func TestVerifyLocalRequirementReferencesTeachesExactCorrectionForSourceReferenceAndState(t *testing.T) {
	digest := "sha256:" + strings.Repeat("d", 64)
	content := &TODOContent{
		SchemaVersion:       todoContentSchemaVersion,
		LocalChecks:         []TODOLocalCheck{{ID: "CHECK-BUILD", RepositoryID: "belluga/repo", Argv: []string{"go", "test"}, WorkingDirectory: ".", InputPaths: []string{"**"}}},
		Criteria:            []TODOContentItem{{ID: "criterion-build", Text: "The build check passes."}},
		ValidationScenarios: []TODOValidationScenario{{ID: "scenario-build", CriterionIDs: []string{"criterion-build"}, Method: "Run the declared build check.", Expected: "Exit zero."}},
	}
	result := &LocalProofResult{RunID: "run-123", Checks: []localProofCheck{{ID: "CHECK-BUILD", RepositoryID: "belluga/repo", ExitCode: 0}}}
	valid := []TODOProofReference{
		{Kind: "criterion:criterion-build", Source: "paced-local-runner", Reference: "CHECK-BUILD", SourceDigest: digest, VerificationState: "unverified"},
		{Kind: "scenario:scenario-build", Source: "paced-local-runner", Reference: "CHECK-BUILD", SourceDigest: digest, VerificationState: "unverified"},
	}
	cases := []struct {
		name     string
		mutate   func([]TODOProofReference)
		field    string
		guidance string
	}{
		{"wrong source", func(refs []TODOProofReference) { refs[0].Source = "local-run" }, "proof_refs.criterion:criterion-build.source", "paced-local-runner"},
		{"argv is not a check reference", func(refs []TODOProofReference) { refs[0].Reference = "docker build --target test" }, "proof_refs.criterion:criterion-build.ref", "check ID"},
		{"run id is not a check reference", func(refs []TODOProofReference) { refs[0].Reference = result.RunID }, "proof_refs.criterion:criterion-build.ref", "check ID"},
		{"invalid state", func(refs []TODOProofReference) { refs[0].VerificationState = "pending" }, "proof_refs.criterion:criterion-build.verification_state", "unverified"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			refs := append([]TODOProofReference(nil), valid...)
			tc.mutate(refs)
			_, err := verifyLocalRequirementReferences(content, refs, result, digest)
			var teach *TODOValidationError
			if !errors.As(err, &teach) || teach.Field != tc.field || !strings.Contains(teach.Expected, tc.guidance) {
				t.Fatalf("TEACH = %#v (%v); want field %q and guidance %q", teach, err, tc.field, tc.guidance)
			}
		})
	}
}

func TestVerifyLocalRequirementReferencesKeepsStaleHistoryAndRequestsCurrentDigest(t *testing.T) {
	digest := "sha256:" + strings.Repeat("e", 64)
	oldDigest := "sha256:" + strings.Repeat("f", 64)
	content := &TODOContent{
		SchemaVersion:       todoContentSchemaVersion,
		LocalChecks:         []TODOLocalCheck{{ID: "P-TEST", RepositoryID: "belluga/delphi-ai", Argv: []string{"docker", "build"}, WorkingDirectory: ".", InputPaths: []string{"local-api/**"}}},
		Criteria:            []TODOContentItem{{ID: "criterion-template", Text: "The prepared test image passes."}},
		ValidationScenarios: []TODOValidationScenario{{ID: "scenario-template", CriterionIDs: []string{"criterion-template"}, Method: "Run the declared P-TEST check.", Expected: "Exit zero."}},
	}
	result := &LocalProofResult{RunID: "run-current", Checks: []localProofCheck{{ID: "P-TEST", RepositoryID: "belluga/delphi-ai", ExitCode: 0}}}
	old := TODOProofReference{Kind: "criterion:criterion-template", Source: "legacy-source", Reference: "historical-command", SourceDigest: oldDigest, VerificationState: "verified"}
	currentScenario := TODOProofReference{Kind: "scenario:scenario-template", Source: "paced-local-runner", Reference: "P-TEST", SourceDigest: digest, VerificationState: "unverified"}
	if _, err := verifyLocalRequirementReferences(content, []TODOProofReference{old, currentScenario}, result, digest); err == nil {
		t.Fatal("stale-only criterion reference satisfied current digest coverage")
	} else {
		var teach *TODOValidationError
		if !errors.As(err, &teach) || teach.Field != "proof_refs.criterion:criterion-template.source_digest" || !strings.Contains(teach.Expected, "current") || !strings.Contains(teach.Expected, digest) || !strings.Contains(teach.Expected, "historical") {
			t.Fatalf("stale-only guidance = %#v (%v); want add-current-digest guidance that preserves history", teach, err)
		}
	}
	currentCriterion := TODOProofReference{Kind: "criterion:criterion-template", Source: "paced-local-runner", Reference: "P-TEST", SourceDigest: digest, VerificationState: "unverified"}
	verified, err := verifyLocalRequirementReferences(content, []TODOProofReference{old, currentCriterion, currentScenario}, result, digest)
	if err != nil || len(verified) != 3 || verified[0] != old || verified[1].VerificationState != "verified" || verified[2].VerificationState != "verified" {
		t.Fatalf("historical plus current coverage = %+v, %v; want preserved history and verified current refs", verified, err)
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

func TestExternalRequirementCitationsStayUnverified(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	content := &TODOContent{SchemaVersion: todoContentSchemaVersion,
		Criteria:            []TODOContentItem{{ID: "criterion-one", Text: "Code is scoped."}},
		ValidationScenarios: []TODOValidationScenario{{ID: "scenario-one", CriterionIDs: []string{"criterion-one"}, Method: "Inspect external evidence.", Expected: "Citation is recorded."}},
	}
	criterion := TODOProofReference{Kind: "criterion:criterion-one", Source: "external-evidence", Reference: "https://example.invalid/review/1", SourceDigest: digest, VerificationState: "unverified"}
	scenario := TODOProofReference{Kind: "scenario:scenario-one", Source: "external-evidence", Reference: "https://example.invalid/run/2", SourceDigest: digest, VerificationState: "unverified"}
	old := criterion
	old.SourceDigest = "sha256:" + strings.Repeat("b", 64)
	old.VerificationState = "verified"
	if err := validateExternalRequirementReferences(content, []TODOProofReference{old, criterion, scenario}, digest); err != nil {
		t.Fatalf("valid citations rejected: %v", err)
	}
	if err := validateExternalRequirementReferences(content, []TODOProofReference{old, scenario}, digest); err == nil {
		t.Fatal("stale citation satisfied current criterion")
	}
	if err := validateExternalRequirementReferences(content, []TODOProofReference{criterion, criterion, scenario}, digest); err == nil {
		t.Fatal("duplicate current citation accepted")
	}
	forged := criterion
	forged.VerificationState = "verified"
	if err := validateExternalRequirementReferences(content, []TODOProofReference{forged, scenario}, digest); err == nil {
		t.Fatal("caller-declared verified test accepted")
	}
	forged = criterion
	forged.Source = "paced-local-runner"
	if err := validateExternalRequirementReferences(content, []TODOProofReference{forged, scenario}, digest); err == nil {
		t.Fatal("local runner citation accepted for new code proof")
	}
}
