package registry

import (
	"context"
	"errors"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

type githubConnectionRoleRepository struct {
	RepositoryID       string `json:"repository_id,omitempty"`
	GitHubRepositoryID int64  `json:"github_repository_id,omitempty"`
	AccountID          int64  `json:"github_account_id,omitempty"`
	ConnectionRevision int64  `json:"connection_revision,omitempty"`
}

type githubConnectionVisualRole struct {
	githubConnectionRoleRepository
	Revision          int64  `json:"revision"`
	PublicationBranch string `json:"publication_branch,omitempty"`
}

type githubConnectionImplementationRole struct {
	Revision     int64                            `json:"revision"`
	Repositories []githubConnectionRoleRepository `json:"repositories"`
}

type githubConnectionRoles struct {
	Visual         githubConnectionVisualRole         `json:"visual"`
	Implementation githubConnectionImplementationRole `json:"implementation"`
}

func githubConnectionRole(alias string, c githubRepositoryConnection) githubConnectionRoleRepository {
	return githubConnectionRoleRepository{RepositoryID: alias, GitHubRepositoryID: c.GitHubRepositoryID, AccountID: c.AccountID, ConnectionRevision: c.Revision}
}

func (a *githubConnectionAdmin) roles(ctx context.Context, company, project string, operator githubOperator) (githubConnectionRoles, error) {
	var out githubConnectionRoles
	db, err := a.database(ctx, company, project, operator)
	if err != nil {
		return out, err
	}
	doc, err := readGitHubAdminProject(ctx, db, company, project)
	if err != nil {
		return out, err
	}
	out.Visual.Revision = doc.BindingRevision
	collection := db.Collection(githubRepositoryConnectionsCollection)
	if doc.Binding != nil {
		out.Visual.RepositoryID, out.Visual.PublicationBranch = doc.Binding.RepositoryID, doc.Binding.PublicationBranch
		if pin := doc.GitHubVisualConnection; pin != nil && pin.BindingRevision == doc.BindingRevision && pin.GitHubRepositoryID > 0 {
			var c githubRepositoryConnection
			err := collection.FindOne(ctx, bson.M{"project_id": project, "github_repository_id": pin.GitHubRepositoryID}).Decode(&c)
			if err == nil {
				out.Visual.githubConnectionRoleRepository = githubConnectionRole(doc.Binding.RepositoryID, c)
			} else if !errors.Is(err, mongo.ErrNoDocuments) {
				return githubConnectionRoles{}, normalizeMongoError(err)
			}
		}
	}
	out.Implementation.Revision = doc.ImplementationRepositoriesRevision
	out.Implementation.Repositories = make([]githubConnectionRoleRepository, 0, len(doc.ImplementationRepositories))
	pins, err := readImplementationRepositoryNodeIDs(ctx, db, project)
	if err != nil {
		return githubConnectionRoles{}, err
	}
	nodes := make([]string, 0, len(doc.ImplementationRepositories))
	for _, alias := range doc.ImplementationRepositories {
		if pin := pins[alias]; pin != "" {
			nodes = append(nodes, pin)
		}
	}
	byNode := make(map[string]githubRepositoryConnection, len(nodes))
	if len(nodes) > 0 {
		cursor, err := collection.Find(ctx, bson.M{"project_id": project, "repository_node_id": bson.M{"$in": nodes}})
		if err != nil {
			return githubConnectionRoles{}, normalizeMongoError(err)
		}
		defer cursor.Close(ctx)
		for cursor.Next(ctx) {
			var c githubRepositoryConnection
			if cursor.Decode(&c) != nil || byNode[c.RepositoryNodeID].GitHubRepositoryID != 0 {
				return githubConnectionRoles{}, ErrUnavailable
			}
			byNode[c.RepositoryNodeID] = c
		}
		if err := cursor.Err(); err != nil {
			return githubConnectionRoles{}, normalizeMongoError(err)
		}
	}
	for _, alias := range doc.ImplementationRepositories {
		out.Implementation.Repositories = append(out.Implementation.Repositories, githubConnectionRole(alias, byNode[pins[alias]]))
	}
	return out, nil
}
