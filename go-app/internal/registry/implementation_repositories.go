package registry

import (
	"context"
	"fmt"
	"sort"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

type ImplementationRepositorySet struct {
	CompanyID     string                                 `json:"company_id"`
	ProjectID     string                                 `json:"project_id"`
	Revision      int64                                  `json:"revision"`
	Items         []string                               `json:"repository_ids"`
	Confirmations []ImplementationRepositoryConfirmation `json:"confirmations"`
}

// Confirmation belongs to the current membership, so a second repository
// cannot erase the owner reference that authorized the first one.
type ImplementationRepositoryConfirmation struct {
	RepositoryID   string    `json:"repository_id" bson:"repository_id"`
	Actor          string    `json:"actor" bson:"actor"`
	OwnerReference string    `json:"owner_reference" bson:"owner_reference"`
	OperationID    string    `json:"operation_id" bson:"operation_id"`
	ConfirmedAt    time.Time `json:"confirmed_at" bson:"confirmed_at"`
}

type ImplementationRepositoryChange struct {
	Action           string `json:"action"`
	ProjectID        string `json:"project_id"`
	ExpectedRevision int64  `json:"expected_revision"`
	OperationID      string `json:"operation_id"`
	Actor            string `json:"actor"`
	OwnerReference   string `json:"owner_reference"`
	RepositoryID     string `json:"repository_id"`
}

type implementationRepositoryOperation struct {
	ID             string `bson:"id"`
	Digest         string `bson:"digest"`
	Actor          string `bson:"actor"`
	OwnerReference string `bson:"owner_reference"`
	Action         string `bson:"action"`
	Repository     string `bson:"repository_id"`
}

type implementationRepositoryService interface {
	ReadImplementationRepositories(context.Context, string, string) (ImplementationRepositorySet, error)
	ChangeImplementationRepository(context.Context, string, string, ImplementationRepositoryChange) (ImplementationRepositorySet, error)
}

func (m *MongoStore) ReadImplementationRepositories(ctx context.Context, companyID, projectID string) (ImplementationRepositorySet, error) {
	company, err := m.company(ctx, companyID)
	if err != nil {
		return ImplementationRepositorySet{}, err
	}
	var document projectDocument
	if err := m.client.Database(company.DatabaseName).Collection(projectsCollection).FindOne(ctx, bson.M{"id": projectID}).Decode(&document); err != nil {
		if err == mongo.ErrNoDocuments {
			return ImplementationRepositorySet{}, ErrNotFound
		}
		return ImplementationRepositorySet{}, ErrUnavailable
	}
	if document.ID != projectID || document.CompanyID != companyID || document.ObjectID.IsZero() {
		return ImplementationRepositorySet{}, ErrUnavailable
	}
	items := append([]string{}, document.ImplementationRepositories...)
	sort.Strings(items)
	return ImplementationRepositorySet{CompanyID: companyID, ProjectID: projectID, Revision: document.ImplementationRepositoriesRevision, Items: items, Confirmations: append([]ImplementationRepositoryConfirmation{}, document.ImplementationRepositoryConfirmations...)}, nil
}

func (m *MongoStore) ChangeImplementationRepository(ctx context.Context, companyID, projectID string, request ImplementationRepositoryChange) (ImplementationRepositorySet, error) {
	if request.ProjectID != projectID || !validID(companyID) || !validID(projectID) || request.ExpectedRevision < 0 || !validID(request.OperationID) || !validSummaryField(request.Actor, 256) || !validSummaryField(request.OwnerReference, 512) || !validGitRepositoryID(request.RepositoryID) || request.Action != "confirm" && request.Action != "remove" {
		return ImplementationRepositorySet{}, todoTeach("request", "provide exact Project identity, current revision, stable operation ID, actor, nonblank owner confirmation reference, repository ID and confirm/remove action", ErrInvalid)
	}
	company, err := m.company(ctx, companyID)
	if err != nil {
		return ImplementationRepositorySet{}, err
	}
	collection := m.client.Database(company.DatabaseName).Collection(projectsCollection)
	var document projectDocument
	if err := collection.FindOne(ctx, bson.M{"id": projectID}).Decode(&document); err != nil {
		if err == mongo.ErrNoDocuments {
			return ImplementationRepositorySet{}, ErrNotFound
		}
		return ImplementationRepositorySet{}, ErrUnavailable
	}
	if document.CompanyID != companyID || document.ObjectID.IsZero() {
		return ImplementationRepositorySet{}, ErrUnavailable
	}
	digest := sourceHash([]byte(request.Action + "\x00" + request.ProjectID + "\x00" + request.RepositoryID + "\x00" + request.Actor + "\x00" + request.OwnerReference))
	if last := document.ImplementationRepositoriesLastOperation; last != nil && last.ID == request.OperationID {
		if last.Digest != digest {
			return ImplementationRepositorySet{}, todoTeach("operation_id", "reuse an operation ID only for the identical implementation repository request", ErrConflict)
		}
		return implementationRepositorySetFromDocument(document, companyID, projectID), nil
	}
	if document.ImplementationRepositoriesRevision != request.ExpectedRevision {
		return ImplementationRepositorySet{}, todoTeach("expected_revision", "match the current Project implementation repository revision", ErrConflict)
	}
	items := append([]string{}, document.ImplementationRepositories...)
	position := -1
	for index, item := range items {
		if item == request.RepositoryID {
			position = index
			break
		}
	}
	if request.Action == "confirm" && position >= 0 || request.Action == "remove" && position < 0 {
		return implementationRepositorySetFromDocument(document, companyID, projectID), nil
	}
	if request.Action == "confirm" {
		items = append(items, request.RepositoryID)
	} else {
		items = append(items[:position], items[position+1:]...)
	}
	sort.Strings(items)
	confirmations := append([]ImplementationRepositoryConfirmation{}, document.ImplementationRepositoryConfirmations...)
	if request.Action == "confirm" {
		confirmations = append(confirmations, ImplementationRepositoryConfirmation{RepositoryID: request.RepositoryID, Actor: request.Actor, OwnerReference: request.OwnerReference, OperationID: request.OperationID, ConfirmedAt: time.Now().UTC()})
	} else {
		for index, confirmation := range confirmations {
			if confirmation.RepositoryID == request.RepositoryID {
				confirmations = append(confirmations[:index], confirmations[index+1:]...)
				break
			}
		}
	}
	sort.Slice(confirmations, func(i, j int) bool { return confirmations[i].RepositoryID < confirmations[j].RepositoryID })
	nextRevision := document.ImplementationRepositoriesRevision + 1
	filter := bson.M{"_id": document.ObjectID, "implementation_repositories_revision": document.ImplementationRepositoriesRevision}
	if document.ImplementationRepositoriesRevision == 0 {
		filter = bson.M{"_id": document.ObjectID, "$or": bson.A{bson.M{"implementation_repositories_revision": 0}, bson.M{"implementation_repositories_revision": bson.M{"$exists": false}}}}
	}
	operation := implementationRepositoryOperation{ID: request.OperationID, Digest: digest, Actor: request.Actor, OwnerReference: request.OwnerReference, Action: request.Action, Repository: request.RepositoryID}
	result, err := collection.UpdateOne(ctx, filter, bson.M{"$set": bson.M{"implementation_repositories": items, "implementation_repository_confirmations": confirmations, "implementation_repositories_revision": nextRevision, "implementation_repositories_last_operation": operation}})
	if err != nil {
		return ImplementationRepositorySet{}, ErrUnavailable
	}
	if result.ModifiedCount != 1 {
		return ImplementationRepositorySet{}, todoTeach("expected_revision", "reload Project implementation repositories and retry the compare-and-set change", ErrConflict)
	}
	return ImplementationRepositorySet{CompanyID: companyID, ProjectID: projectID, Revision: nextRevision, Items: items, Confirmations: confirmations}, nil
}

func implementationRepositorySetFromDocument(document projectDocument, companyID, projectID string) ImplementationRepositorySet {
	items := append([]string{}, document.ImplementationRepositories...)
	sort.Strings(items)
	return ImplementationRepositorySet{CompanyID: companyID, ProjectID: projectID, Revision: document.ImplementationRepositoriesRevision, Items: items, Confirmations: append([]ImplementationRepositoryConfirmation{}, document.ImplementationRepositoryConfirmations...)}
}

func requireTODOImplementationRepositories(ctx context.Context, store *MongoStore, companyID string, record TODORecord) error {
	if record.Content != nil && record.Content.Delivery != nil && record.Content.Delivery.Kind == "knowledge-only" {
		return nil
	}
	set, err := store.ReadImplementationRepositories(ctx, companyID, record.ProjectID)
	if err != nil {
		return err
	}
	confirmed := make(map[string]bool, len(set.Items))
	for _, repositoryID := range set.Items {
		confirmed[repositoryID] = true
	}
	for index, scope := range record.DiffExpectation {
		if !confirmed[scope.RepositoryID] {
			return todoTeach(fmt.Sprintf("diff_expectation[%d].repository_id", index), "confirm this Git-backed delivery repository for the exact Project before leaving Planning; repository-free Planning remains available", ErrConflict)
		}
	}
	return nil
}
