package registry

import (
	"context"
	"testing"
)

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
