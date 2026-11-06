package registry

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigureLocalMatrixAcceptsAnyValidTODOID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "matrix.json")
	data := `{"schema_version":"builder-local-proof-v1","default_required":["G-TEST"],"commands":[{"id":"G-TEST","argv":["go","test"]}],"required_by":{"builder-new-mongo-guard-todo-42":["G-TEST"]}}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}

	store := &MongoStore{}
	if err := store.ConfigureLocalMatrix(path); err != nil {
		t.Fatalf("valid matrix rejected: %v", err)
	}
	if got := store.localMatrix.RequiredBy["builder-new-mongo-guard-todo-42"]; len(got) != 1 || got[0] != "G-TEST" {
		t.Fatalf("required commands = %v", got)
	}
}

func TestVerifyLocalRequirementReferencesBindsCriteriaAndScenariosToSuccessfulRun(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	content := &TODOContent{
		SchemaVersion:       todoContentSchemaVersion,
		Criteria:            []TODOContentItem{{ID: "C-1"}},
		ValidationScenarios: []TODOValidationScenario{{ID: "S-1", CriterionIDs: []string{"C-1"}}, {ID: "S-2", CriterionIDs: []string{"C-1"}}},
	}
	result := &LocalProofResult{Commands: []localProofCommand{{ID: "G-UNIT", ExitCode: 0}, {ID: "G-BUILD", ExitCode: 0}}}
	refs := []TODOProofReference{
		{Kind: "criterion:C-1", Source: "paced-local-runner", Reference: "G-UNIT", SourceDigest: digest, VerificationState: "unverified"},
		{Kind: "scenario:S-1", Source: "paced-local-runner", Reference: "G-BUILD", SourceDigest: digest, VerificationState: "unverified"},
		{Kind: "scenario:S-2", Source: "paced-local-runner", Reference: "G-UNIT", SourceDigest: digest, VerificationState: "unverified"},
	}
	verified, err := verifyLocalRequirementReferences(content, refs, result, digest)
	if err != nil {
		t.Fatalf("valid requirement references rejected: %v", err)
	}
	for _, ref := range verified {
		if ref.VerificationState != "verified" {
			t.Errorf("reference %q was not API-verified: %+v", ref.Kind, ref)
		}
	}
	if refs[0].VerificationState != "unverified" {
		t.Fatal("verification mutated the caller's reference slice")
	}
}

func TestVerifyLocalRequirementReferencesRejectsUnmappedRequiredCommand(t *testing.T) {
	digest := "sha256:" + strings.Repeat("f", 64)
	content := &TODOContent{
		SchemaVersion:       todoContentSchemaVersion,
		Criteria:            []TODOContentItem{{ID: "C-1"}},
		ValidationScenarios: []TODOValidationScenario{{ID: "S-1", CriterionIDs: []string{"C-1"}}},
	}
	result := &LocalProofResult{Commands: []localProofCommand{{ID: "G-UNIT", ExitCode: 0}, {ID: "G-BUILD", ExitCode: 0}}}
	refs := []TODOProofReference{
		{Kind: "criterion:C-1", Source: "paced-local-runner", Reference: "G-UNIT", SourceDigest: digest, VerificationState: "unverified"},
		{Kind: "scenario:S-1", Source: "paced-local-runner", Reference: "G-UNIT", SourceDigest: digest, VerificationState: "unverified"},
	}
	if _, err := verifyLocalRequirementReferences(content, refs, result, digest); err == nil || !strings.Contains(err.Error(), "proof_refs.scenario") {
		t.Fatalf("unmapped required build was accepted or lacked scoped TEACH: %v", err)
	}
}

func TestVerifyLocalRequirementReferencesAllowsSeveralCommandsForOneScenario(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	content := &TODOContent{
		SchemaVersion: todoContentSchemaVersion,
		Criteria: []TODOContentItem{{ID: "C-1"}},
		ValidationScenarios: []TODOValidationScenario{{ID: "S-1", CriterionIDs: []string{"C-1"}}},
	}
	result := &LocalProofResult{Commands: []localProofCommand{{ID: "G-TEST"}, {ID: "G-IMAGE"}}}
	refs := []TODOProofReference{
		{Kind: "criterion:C-1", Source: "paced-local-runner", Reference: "G-TEST", SourceDigest: digest, VerificationState: "unverified"},
		{Kind: "scenario:S-1", Source: "paced-local-runner", Reference: "G-TEST", SourceDigest: digest, VerificationState: "unverified"},
		{Kind: "scenario:S-1", Source: "paced-local-runner", Reference: "G-IMAGE", SourceDigest: digest, VerificationState: "unverified"},
	}
	if _, err := verifyLocalRequirementReferences(content, refs, result, digest); err != nil {
		t.Fatalf("multiple commands for one scenario were rejected: %v", err)
	}
	refs = append(refs, refs[1])
	if _, err := verifyLocalRequirementReferences(content, refs, result, digest); err == nil || !strings.Contains(err.Error(), "do not repeat") {
		t.Fatalf("duplicate scenario/command reference accepted or lacked TEACH: %v", err)
	}
}

func TestVerifyLocalRequirementReferencesRejectsMissingUnknownFailingAndStaleEvidence(t *testing.T) {
	digest := "sha256:" + strings.Repeat("b", 64)
	content := &TODOContent{
		SchemaVersion:       todoContentSchemaVersion,
		Criteria:            []TODOContentItem{{ID: "C-1"}},
		ValidationScenarios: []TODOValidationScenario{{ID: "S-1", CriterionIDs: []string{"C-1"}}},
	}
	refs := []TODOProofReference{
		{Kind: "criterion:C-1", Source: "paced-local-runner", Reference: "G-UNIT", SourceDigest: digest, VerificationState: "unverified"},
		{Kind: "scenario:S-1", Source: "paced-local-runner", Reference: "G-UNIT", SourceDigest: digest, VerificationState: "unverified"},
	}
	tests := map[string]struct {
		refs   []TODOProofReference
		result *LocalProofResult
		digest string
	}{
		"missing criterion": {refs: refs[1:], result: &LocalProofResult{Commands: []localProofCommand{{ID: "G-UNIT", ExitCode: 0}}}, digest: digest},
		"unknown command":   {refs: []TODOProofReference{{Kind: "criterion:C-1", Source: "paced-local-runner", Reference: "G-UNKNOWN", SourceDigest: digest, VerificationState: "unverified"}, refs[1]}, result: &LocalProofResult{Commands: []localProofCommand{{ID: "G-UNIT", ExitCode: 0}}}, digest: digest},
		"failing command":   {refs: refs, result: &LocalProofResult{Commands: []localProofCommand{{ID: "G-UNIT", ExitCode: 1}}}, digest: digest},
		"stale digest":      {refs: refs, result: &LocalProofResult{Commands: []localProofCommand{{ID: "G-UNIT", ExitCode: 0}}}, digest: "sha256:" + strings.Repeat("c", 64)},
		"duplicate subject": {refs: append(append([]TODOProofReference{}, refs...), refs[0]), result: &LocalProofResult{Commands: []localProofCommand{{ID: "G-UNIT", ExitCode: 0}}}, digest: digest},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := verifyLocalRequirementReferences(content, tc.refs, tc.result, tc.digest); err == nil {
				t.Fatal("invalid requirement evidence accepted")
			}
		})
	}
}

func TestVerifyLocalRequirementReferencesRetainsStaleRowsWithoutCountingThem(t *testing.T) {
	currentDigest := "sha256:" + strings.Repeat("d", 64)
	oldDigest := "sha256:" + strings.Repeat("e", 64)
	content := &TODOContent{
		SchemaVersion:       todoContentSchemaVersion,
		Criteria:            []TODOContentItem{{ID: "C-1"}},
		ValidationScenarios: []TODOValidationScenario{{ID: "S-1", CriterionIDs: []string{"C-1"}}},
	}
	result := &LocalProofResult{Commands: []localProofCommand{{ID: "G-UNIT", ExitCode: 0}}}
	old := []TODOProofReference{
		{Kind: "criterion:C-1", Source: "paced-local-runner", Reference: "G-UNIT", SourceDigest: oldDigest, VerificationState: "verified"},
		{Kind: "scenario:S-1", Source: "paced-local-runner", Reference: "G-UNIT", SourceDigest: oldDigest, VerificationState: "verified"},
	}
	current := []TODOProofReference{
		{Kind: "criterion:C-1", Source: "paced-local-runner", Reference: "G-UNIT", SourceDigest: currentDigest, VerificationState: "unverified"},
		{Kind: "scenario:S-1", Source: "paced-local-runner", Reference: "G-UNIT", SourceDigest: currentDigest, VerificationState: "unverified"},
	}
	verified, err := verifyLocalRequirementReferences(content, append(append([]TODOProofReference{}, old...), current...), result, currentDigest)
	if err != nil {
		t.Fatalf("stale historical refs blocked current evidence: %v", err)
	}
	if len(verified) != 4 || verified[0] != old[0] || verified[1] != old[1] || verified[2].VerificationState != "verified" || verified[3].VerificationState != "verified" {
		t.Fatalf("historical/current refs after verification = %+v", verified)
	}
	if _, err := verifyLocalRequirementReferences(content, old, result, currentDigest); err == nil || !strings.Contains(err.Error(), "proof_refs.") {
		t.Fatalf("stale-only refs satisfied current coverage or lacked field TEACH: %v", err)
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
	if !requiresLocalProofForTODO(TODORecord{DiffExpectation: []TODORepositoryScope{{Repository: "Foundation"}, {Repository: "Builder"}}}) {
		t.Fatal("mixed Foundation and implementation scope skipped the local runner")
	}
}

func TestConfigureLocalMatrixRejectsMalformedTODOIDsAndCommands(t *testing.T) {
	const prefix = `{"schema_version":"builder-local-proof-v1",`
	const command = `"commands":[{"id":"G-TEST","argv":["go","test"]}]}`
	tests := map[string]string{
		"malformed TODO ID":          prefix + `"default_required":["G-TEST"],` + command[:len(command)-1] + `,"required_by":{"Builder-bad":["G-TEST"]}}`,
		"duplicate command ID":       prefix + `"default_required":["G-TEST"],"commands":[{"id":"G-TEST","argv":["go","test"]},{"id":"G-TEST","argv":["go","vet"]}],"required_by":{"builder-valid-todo":["G-TEST"]}}`,
		"unsupported schema":         `{"schema_version":"builder-local-proof-v2","default_required":["G-TEST"],` + command[:len(command)-1] + `,"required_by":{"builder-valid-todo":["G-TEST"]}}`,
		"duplicate default command":  prefix + `"default_required":["G-TEST","G-TEST"],` + command[:len(command)-1] + `,"required_by":{"builder-valid-todo":["G-TEST"]}}`,
		"unknown default command":    prefix + `"default_required":["G-UNKNOWN"],` + command[:len(command)-1] + `,"required_by":{"builder-valid-todo":["G-TEST"]}}`,
		"duplicate required command": prefix + `"default_required":["G-TEST"],` + command[:len(command)-1] + `,"required_by":{"builder-valid-todo":["G-TEST","G-TEST"]}}`,
		"unknown required command":   prefix + `"default_required":["G-TEST"],` + command[:len(command)-1] + `,"required_by":{"builder-valid-todo":["G-UNKNOWN"]}}`,
	}
	for name, data := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "matrix.json")
			if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := (&MongoStore{}).ConfigureLocalMatrix(path); err == nil {
				t.Fatal("invalid matrix accepted")
			}
		})
	}
}
