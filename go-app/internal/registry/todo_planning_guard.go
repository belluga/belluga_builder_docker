package registry

import (
	"fmt"
	"reflect"
)

// validateMongoPlanningContract checks the objective, typed admission rules
// that can be decided from one immutable Mongo content revision. It does not
// treat free-text review claims or a caller-supplied guard result as proof.
func validateMongoPlanningContract(record TODORecord, content TODOAuthoredContent) error {
	if err := ValidateTODOContent(content.TODOContent, false); err != nil {
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
	if !reflect.DeepEqual(record.DiffExpectation, content.DiffExpectation) {
		return todoTeach("content.diff_expectation", "match the current Mongo TODO aggregate and immutable content revision", ErrConflict)
	}
	for i, scope := range content.DiffExpectation {
		field := fmt.Sprintf("content.diff_expectation[%d]", i)
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
