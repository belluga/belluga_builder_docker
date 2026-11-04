package registry

import (
	"strings"
	"testing"
)

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
	if err := validateMongoPlanningContract(record, content); err != nil {
		t.Fatalf("valid typed planning contract rejected: %v", err)
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
}
