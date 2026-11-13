package registry

import (
	"context"
	"testing"
)

func TestSelectTODOReleaseBranchesIgnoresUnselectedReleaseEntries(t *testing.T) {
	record := TODORecord{DiffExpectation: []TODORepositoryScope{{RepositoryID: "belluga/selected"}}}
	branches := map[string]string{"belluga/selected": "builder", "belluga/other": "other-branch"}
	selected := selectTODOReleaseBranchMap(record, branches)
	if len(selected) != 1 || selected["belluga/selected"] != "builder" {
		t.Fatalf("selected branch map = %#v; unrelated Release entry must not enter TODO proof", selected)
	}
	branches["belluga/new"] = "new-branch"
	selected = selectTODOReleaseBranchMap(record, branches)
	if len(selected) != 1 || selected["belluga/selected"] != "builder" {
		t.Fatalf("adding an unrelated Release branch invalidated the TODO subset: %#v", selected)
	}
}

func TestImplementationRepositoryChangeRequiresOwnerConfirmation(t *testing.T) {
	store := &MongoStore{}
	request := ImplementationRepositoryChange{
		Action: "confirm", ProjectID: "project-a", ExpectedRevision: 0,
		OperationID: "confirm-1", Actor: "operator", RepositoryID: "belluga/shared",
	}
	if _, err := store.ChangeImplementationRepository(context.Background(), "company-a", "project-a", request); err == nil {
		t.Fatal("change without an explicit owner confirmation reference was accepted")
	}
}
