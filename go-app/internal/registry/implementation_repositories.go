package registry

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type ImplementationRepositorySet struct {
	CompanyID     string                                 `json:"company_id"`
	ProjectID     string                                 `json:"project_id"`
	Revision      int64                                  `json:"revision"`
	Items         []string                               `json:"repository_ids"`
	Confirmations []ImplementationRepositoryConfirmation `json:"confirmations"`
	GitHubNodeIDs map[string]string                      `json:"github_node_ids,omitempty"`
}

const implementationRepositoryIdentityCollection = "project_implementation_repository_identities"

type implementationRepositoryIdentity struct {
	ProjectID    string    `bson:"project_id"`
	RepositoryID string    `bson:"repository_id"`
	GitHubNodeID string    `bson:"github_node_id"`
	ProofDigest  string    `bson:"proof_digest"`
	PinnedAt     time.Time `bson:"pinned_at"`
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
	identities, err := readImplementationRepositoryNodeIDs(ctx, m.client.Database(company.DatabaseName), projectID)
	if err != nil {
		return ImplementationRepositorySet{}, err
	}
	return ImplementationRepositorySet{CompanyID: companyID, ProjectID: projectID, Revision: document.ImplementationRepositoriesRevision, Items: items, Confirmations: append([]ImplementationRepositoryConfirmation{}, document.ImplementationRepositoryConfirmations...), GitHubNodeIDs: identities}, nil
}

func ensureImplementationRepositoryIdentityIndex(ctx context.Context, database *mongo.Database) error {
	_, err := database.Collection(implementationRepositoryIdentityCollection).Indexes().CreateOne(ctx, mongo.IndexModel{Keys: bson.D{{Key: "project_id", Value: 1}, {Key: "repository_id", Value: 1}}, Options: options.Index().SetName("project_repository_identity_unique").SetUnique(true)})
	if err != nil {
		return ErrUnavailable
	}
	return nil
}

// pinImplementationRepositoryNodeIDInTransaction applies the insert-only CAS
// inside the TODO transition transaction so a later alias or revision conflict
// cannot leave a partial identity pin behind.
func pinImplementationRepositoryNodeIDInTransaction(ctx context.Context, database *mongo.Database, projectID, repositoryID, nodeID, proofDigest string) error {
	if !validID(projectID) || !validGitRepositoryID(repositoryID) || nodeID == "" || len(nodeID) > 128 || !validSourceDigest(proofDigest) {
		return todoTeach("repository_identity", "pin a valid alias, immutable GitHub node ID and verified proof digest", ErrInvalid)
	}
	collection := database.Collection(implementationRepositoryIdentityCollection)
	filter := bson.D{{Key: "project_id", Value: projectID}, {Key: "repository_id", Value: repositoryID}}
	var prior implementationRepositoryIdentity
	err := collection.FindOne(ctx, filter).Decode(&prior)
	if err == nil {
		if prior.GitHubNodeID == nodeID {
			return nil
		}
		return todoTeach("repository_identity.github_node_id", "match the immutable ID already pinned to this repository alias", ErrConflict)
	}
	if !errors.Is(err, mongo.ErrNoDocuments) {
		return ErrUnavailable
	}
	_, err = collection.InsertOne(ctx, implementationRepositoryIdentity{ProjectID: projectID, RepositoryID: repositoryID, GitHubNodeID: nodeID, ProofDigest: proofDigest, PinnedAt: time.Now().UTC()})
	if err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return todoTeach("repository_identity.github_node_id", "remain unclaimed until the alias identity is resolved", ErrConflict)
		}
		return ErrUnavailable
	}
	return nil
}

// advanceImplementationRepositoryMembershipCASInTransaction writes the
// Project document as part of the proof transaction. This makes a concurrent
// owner removal contend on the same revisioned document, including aliases
// whose immutable GitHub IDs were pinned by an earlier TODO transition.
func advanceImplementationRepositoryMembershipCASInTransaction(ctx context.Context, database *mongo.Database, projectID string, repositoryIDs []string) error {
	if !validID(projectID) || len(repositoryIDs) == 0 || len(repositoryIDs) > 32 {
		return todoTeach("repository_identity", "guard a bounded non-empty set of owner-confirmed aliases", ErrInvalid)
	}
	var project projectDocument
	if err := database.Collection(projectsCollection).FindOne(ctx, bson.M{"id": projectID}).Decode(&project); err != nil {
		return ErrUnavailable
	}
	seen := make(map[string]bool, len(repositoryIDs))
	for _, repositoryID := range repositoryIDs {
		if !validGitRepositoryID(repositoryID) || seen[repositoryID] || !containsTODOString(project.ImplementationRepositories, repositoryID) {
			return todoTeach("repository_identity.repository_id", "remain in the Project's owner-confirmed implementation repository set", ErrConflict)
		}
		seen[repositoryID] = true
	}
	filter := bson.M{"_id": project.ObjectID, "implementation_repositories": bson.M{"$all": repositoryIDs}}
	if project.ImplementationRepositoriesRevision == 0 {
		filter["$or"] = bson.A{bson.M{"implementation_repositories_revision": 0}, bson.M{"implementation_repositories_revision": bson.M{"$exists": false}}}
	} else {
		filter["implementation_repositories_revision"] = project.ImplementationRepositoriesRevision
	}
	result, err := database.Collection(projectsCollection).UpdateOne(ctx, filter, bson.M{"$inc": bson.M{"implementation_repositories_revision": 1}})
	if err != nil {
		return normalizeMongoError(err)
	}
	if result.MatchedCount != 1 {
		return todoTeach("repository_identity.repository_id", "remain owner-confirmed at the guarded Project revision", ErrConflict)
	}
	return nil
}

func readImplementationRepositoryNodeIDs(ctx context.Context, database *mongo.Database, projectID string) (map[string]string, error) {
	result := map[string]string{}
	cursor, err := database.Collection(implementationRepositoryIdentityCollection).Find(ctx, bson.D{{Key: "project_id", Value: projectID}}, options.Find().SetLimit(33))
	if errors.Is(err, mongo.ErrNoDocuments) {
		return result, nil
	}
	if err != nil {
		return nil, ErrUnavailable
	}
	defer cursor.Close(ctx)
	for cursor.Next(ctx) {
		var item implementationRepositoryIdentity
		if cursor.Decode(&item) != nil || !validGitRepositoryID(item.RepositoryID) || item.GitHubNodeID == "" || !validSourceDigest(item.ProofDigest) {
			return nil, ErrUnavailable
		}
		result[item.RepositoryID] = item.GitHubNodeID
	}
	if cursor.Err() != nil {
		return nil, ErrUnavailable
	}
	if len(result) > 32 {
		return nil, ErrUnavailable
	}
	return result, nil
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

func selectedTODOReleaseBranches(ctx context.Context, database *mongo.Database, record TODORecord) ([]releaseDeliveryBranch, error) {
	if record.Content != nil && record.Content.Delivery != nil && record.Content.Delivery.Kind == "knowledge-only" || len(record.DiffExpectation) == 0 || record.ReleaseID == "" {
		return []releaseDeliveryBranch{}, nil
	}
	selected := make(map[string]bool, len(record.DiffExpectation))
	for _, scope := range record.DiffExpectation {
		if validGitRepositoryID(scope.RepositoryID) {
			selected[scope.RepositoryID] = true
		}
	}
	if len(selected) == 0 {
		return []releaseDeliveryBranch{}, nil
	}
	var release roadmapRelease
	if err := database.Collection(releasesCollection).FindOne(ctx, bson.D{{Key: "project_id", Value: record.ProjectID}, {Key: "id", Value: record.ReleaseID}}).Decode(&release); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return []releaseDeliveryBranch{}, nil
		}
		return nil, ErrUnavailable
	}
	branches := make([]releaseDeliveryBranch, 0, len(selected))
	for _, branch := range release.DeliveryBranches {
		if selected[branch.RepositoryID] {
			if validDeliveryBranch(branch.Branch) && validGitRepositoryID(branch.RepositoryID) {
				branches = append(branches, branch)
			}
		}
	}
	sort.Slice(branches, func(i, j int) bool { return branches[i].RepositoryID < branches[j].RepositoryID })
	return branches, nil
}

func confirmedTODOReleaseBranches(ctx context.Context, store *MongoStore, companyID string, record TODORecord) (map[string]string, error) {
	if record.Content != nil && record.Content.Delivery != nil && record.Content.Delivery.Kind == "knowledge-only" {
		return map[string]string{}, nil
	}
	if len(record.DiffExpectation) == 0 {
		return nil, todoTeach("diff_expectation.repository_id", "identify each implementation repository for code-backed delivery; only explicit Knowledge-only delivery is repository-free", ErrConflict)
	}
	set, err := store.ReadImplementationRepositories(ctx, companyID, record.ProjectID)
	if err != nil {
		return nil, err
	}
	confirmed := make(map[string]bool, len(set.Items))
	for _, repositoryID := range set.Items {
		confirmed[repositoryID] = true
	}
	selected := make(map[string]bool, len(record.DiffExpectation))
	for index, scope := range record.DiffExpectation {
		if !validGitRepositoryID(scope.RepositoryID) {
			return nil, todoTeach(fmt.Sprintf("diff_expectation[%d].repository_id", index), "select an exact repository from this Project's confirmed implementation repository set", ErrConflict)
		}
		if selected[scope.RepositoryID] {
			return nil, todoTeach(fmt.Sprintf("diff_expectation[%d].repository_id", index), "list each implementing repository only once", ErrConflict)
		}
		selected[scope.RepositoryID] = true
		if !confirmed[scope.RepositoryID] {
			return nil, todoTeach(fmt.Sprintf("diff_expectation[%d].repository_id", index), "confirm this Git-backed delivery repository for the exact Project before leaving Planning; repository-free Planning remains available", ErrConflict)
		}
	}
	database, err := store.todoTenantDatabase(ctx, companyID)
	if err != nil {
		return nil, err
	}
	var release roadmapRelease
	if err := database.Collection(releasesCollection).FindOne(ctx, bson.D{{Key: "project_id", Value: record.ProjectID}, {Key: "id", Value: record.ReleaseID}}).Decode(&release); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, todoTeach("release_id", "assign this TODO to an existing same-Project Release before requesting implementation approval", ErrNotFound)
		}
		return nil, ErrUnavailable
	}
	branches := make(map[string]string, len(release.DeliveryBranches))
	for _, branch := range release.DeliveryBranches {
		branches[branch.RepositoryID] = branch.Branch
	}
	for _, scope := range record.DiffExpectation {
		if branches[scope.RepositoryID] == "" {
			return nil, todoTeach("release_id.delivery_branches."+scope.RepositoryID, "confirm this repository's immutable branch through `builder-project roadmap-change --action confirm-release-branch` using the current Roadmap revision and owner reference", ErrConflict)
		}
	}
	return selectTODOReleaseBranchMap(record, branches), nil
}

func selectTODOReleaseBranchMap(record TODORecord, releaseBranches map[string]string) map[string]string {
	selected := make(map[string]string, len(record.DiffExpectation))
	for _, scope := range record.DiffExpectation {
		if branch := releaseBranches[scope.RepositoryID]; branch != "" {
			selected[scope.RepositoryID] = branch
		}
	}
	return selected
}

func requireTODOImplementationRepositories(ctx context.Context, store *MongoStore, companyID string, record TODORecord) error {
	_, err := confirmedTODOReleaseBranches(ctx, store, companyID, record)
	return err
}
