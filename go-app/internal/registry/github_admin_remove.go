package registry

import (
	"context"
	"errors"
	"net/http"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type githubRoleRemoval struct {
	Role             string `json:"role"`
	RepositoryID     string `json:"repository_id"`
	ExpectedRevision *int64 `json:"expected_revision"`
	OperationID      string `json:"operation_id"`
}

func (h *githubAdminHTTP) removeRole(r *http.Request, t githubAdminTarget) (any, error) {
	var request githubRoleRemoval
	if err := githubAdminDecode(r, &request); err != nil {
		return nil, err
	}
	if !validID(request.OperationID) || !validGitRepositoryID(request.RepositoryID) || request.ExpectedRevision == nil ||
		*request.ExpectedRevision < 0 || (request.Role != "visual" && request.Role != "implementation") {
		return nil, ErrInvalid
	}
	ctx := r.Context()
	company, err := h.store.company(ctx, t.company)
	if err != nil {
		return nil, err
	}

	db := h.store.client.Database(company.DatabaseName)
	session, err := h.store.client.StartSession()
	if err != nil {
		return nil, ErrUnavailable
	}
	defer session.EndSession(ctx)
	operation := "github-remove-" + githubRequestDigest([]any{request.Role, request.RepositoryID, request.OperationID, *request.ExpectedRevision, t.operator.Issuer, t.operator.Subject})[7:39]
	_, err = session.WithTransaction(ctx, func(tx context.Context) (any, error) {
		doc, err := readGitHubAdminProject(tx, db, t.company, t.project)
		if err != nil {
			return nil, err
		}
		if request.Role == "visual" && doc.BindingOperation == operation {
			return nil, nil
		}
		if request.Role == "implementation" && doc.ImplementationRepositoriesLastOperation != nil &&
			doc.ImplementationRepositoriesLastOperation.ID == operation {
			return nil, nil
		}
		if githubRoleRevision(doc, request.Role) != *request.ExpectedRevision {
			return nil, todoTeach("expected_revision", "recarregue os vínculos do projeto e confirme a remoção novamente", ErrConflict)
		}

		if request.Role == "visual" {
			if doc.Binding == nil || doc.Binding.RepositoryID != request.RepositoryID {
				return nil, ErrConflict
			}
			if _, err := h.store.ChangeRepositoryBinding(tx, t.company, t.project, RepositoryBindingRequest{
				Action: "unbind", ProjectID: t.project, ExpectedRevision: *request.ExpectedRevision, OperationID: operation, Actor: t.operator.Subject,
			}); err != nil {
				return nil, err
			}
			_, err := db.Collection(projectsCollection).UpdateByID(tx, doc.ObjectID, bson.M{"$unset": bson.M{"github_visual_connection": ""}})
			return nil, err
		}

		if !containsTODOString(doc.ImplementationRepositories, request.RepositoryID) {
			return nil, ErrConflict
		}
		if err := requireUnusedCodeLink(tx, db, t.project, request.RepositoryID); err != nil {
			return nil, err
		}
		// Release branch confirmation and governed code approvals already
		// require Project membership. Confirmation and removal both write the
		// same Project document, so a competing confirmation forces a retry.
		if _, err := h.store.ChangeImplementationRepository(tx, t.company, t.project, ImplementationRepositoryChange{
			Action: "remove", ProjectID: t.project, RepositoryID: request.RepositoryID, ExpectedRevision: *request.ExpectedRevision,
			OperationID: operation, Actor: t.operator.Subject, OwnerReference: "GitHub App operator removal " + request.OperationID,
		}); err != nil {
			return nil, err
		}
		_, err = db.Collection(implementationRepositoryIdentityCollection).DeleteOne(tx, bson.M{"project_id": t.project, "repository_id": request.RepositoryID})
		return nil, err
	})
	if err != nil {
		return nil, normalizeMongoError(err)
	}
	return h.admin.roles(ctx, t.company, t.project, t.operator)
}

func requireUnusedCodeLink(ctx context.Context, db *mongo.Database, project, repository string) error {
	// Planning scopes can name a repository before its membership or Release
	// branch is confirmed. Governed reliance starts with approval.
	dependency := struct {
		ID string `bson:"id"`
	}{}
	err := db.Collection(todosCollection).FindOne(ctx, bson.M{
		"project_id": project, "removed": bson.M{"$ne": true}, "disposition": bson.M{"$exists": false},
		"stage": bson.M{"$nin": bson.A{"Planning", "Completed"}}, "diff_expectation.repository_id": repository,
	}, options.FindOne().SetProjection(bson.M{"id": 1})).Decode(&dependency)
	if err == nil {
		return todoTeach("repository_id", "resolva a dependência do TODO "+dependency.ID+" antes de remover o vínculo de código", ErrConflict)
	}
	if !errors.Is(err, mongo.ErrNoDocuments) {
		return err
	}
	// The roadmap has no archived Release state. A surviving confirmation is active.
	err = db.Collection(releasesCollection).FindOne(ctx, bson.M{
		"project_id": project, "delivery_branches.repository_id": repository,
	}, options.FindOne().SetProjection(bson.M{"id": 1})).Decode(&dependency)
	if err == nil {
		return todoTeach("repository_id", "resolva a branch confirmada do Release "+dependency.ID+" antes de remover o vínculo de código", ErrConflict)
	}
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil
	}
	return err
}
