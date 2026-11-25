package registry

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

const githubScopedProofTokenHeader = "X-Builder-GitHub-Proof-Token"

type githubProofBinding struct {
	membershipRevision int64
	connection         githubRepositoryConnection
}

type githubProofLease struct {
	company, project, alias string
	binding                 githubProofBinding
}

type githubProofLeasesKey struct{}

func guardGitHubProofLease(ctx context.Context, db *mongo.Database, lease githubProofLease) error {
	err := db.Collection(projectsCollection).FindOne(ctx, bson.M{
		"id": lease.project, "implementation_repositories": lease.alias,
		"implementation_repositories_revision": lease.binding.membershipRevision,
	}, options.FindOne().SetProjection(bson.M{"_id": 1})).Err()
	if errors.Is(err, mongo.ErrNoDocuments) {
		return githubProofBindingChanged()
	}
	if err != nil {
		return normalizeMongoTransactionError(err)
	}
	connection := lease.binding.connection
	// Write the connection in the transaction to contend with a relink,
	// without changing the externally visible connection revision.
	result, err := db.Collection(githubRepositoryConnectionsCollection).UpdateOne(ctx, bson.M{
		"project_id": lease.project, "github_repository_id": connection.GitHubRepositoryID,
		"repository_node_id": connection.RepositoryNodeID, "revision": connection.Revision,
		"installation_id": connection.InstallationID, "account_id": connection.AccountID,
	}, bson.M{"$inc": bson.M{"proof_write_fence": int64(1)}})
	if err != nil {
		return normalizeMongoTransactionError(err)
	}
	if result.MatchedCount != 1 {
		return githubProofBindingChanged()
	}
	return nil
}

func githubProofBindingChanged() error {
	return todoTeach("remote_proof.repository_identity", "reload the current Project repository confirmation and connection before requesting proof", ErrConflict)
}

func (m *MongoStore) ConfigureScopedProofReader(token string) error {
	if m.githubApp == nil {
		if token != "" {
			return ErrInvalid
		}
		return nil
	}
	if len(token) < 32 || !validGitHubAppBearer(token) || token == m.proofReaderToken {
		return ErrInvalid
	}
	m.scopedProofReaderToken = token
	return nil
}

func (m *MongoStore) githubProofReadBinding(ctx context.Context, company, project, alias string) (githubProofBinding, error) {
	var zero githubProofBinding
	if !validID(company) || !validID(project) || !validGitRepositoryID(alias) {
		return zero, ErrInvalid
	}
	if m.githubProofBindingReader != nil {
		return m.githubProofBindingReader(ctx, company, project, alias)
	}
	tenant, err := m.company(ctx, company)
	if err != nil {
		return zero, err
	}
	db := m.client.Database(tenant.DatabaseName)
	doc, err := readGitHubAdminProject(ctx, db, company, project)
	if err != nil {
		return zero, err
	}
	if !containsTODOString(doc.ImplementationRepositories, alias) {
		return zero, todoTeach("diff_expectation.repository_id", "confirm this exact code repository for the Project before requesting proof", ErrConflict)
	}
	pins, err := readImplementationRepositoryNodeIDs(ctx, db, project)
	if err != nil {
		return zero, err
	}
	node := pins[alias]
	if node == "" {
		return zero, githubProofAccessDenied()
	}
	rows, err := db.Collection(githubRepositoryConnectionsCollection).Find(ctx, bson.M{"project_id": project, "repository_node_id": node}, options.Find().SetLimit(2))
	if err != nil {
		return zero, normalizeMongoError(err)
	}
	defer rows.Close(ctx)
	var connections []githubRepositoryConnection
	if err := rows.All(ctx, &connections); err != nil {
		return zero, normalizeMongoError(err)
	}
	if len(connections) != 1 || connections[0].Revision < 1 || connections[0].GitHubRepositoryID < 1 || connections[0].ProjectID != project || connections[0].RepositoryNodeID != node {
		return zero, githubProofAccessDenied()
	}
	return githubProofBinding{membershipRevision: doc.ImplementationRepositoriesRevision, connection: connections[0]}, nil
}

func githubProofAccessDenied() error {
	return withTeachDiagnostic(ErrConflict, *readTeach("github_proof_access_denied", "repository_id", "Connect or restore GitHub App access for this exact implementing repository before requesting proof."))
}

func githubProofProviderUnavailable() error {
	return withTeachDiagnostic(ErrUnavailable, *readTeach("github_proof_provider_unavailable", "remote_proof", "Restore GitHub App repository verification before requesting proof."))
}

func (m *MongoStore) requestReaderProof(ctx context.Context, route string, query url.Values, appEligible bool) (*http.Response, *githubProofLease, error) {
	if m.proofReaderURL == "" || m.proofHTTPClient == nil || m.proofReaderToken == "" {
		return nil, nil, todoTeach("remote_proof", "configure the private Project Reader before requesting proof", ErrUnavailable)
	}
	var lease *githubProofLease
	var token string
	bearer := m.proofReaderToken
	client := m.proofHTTPClient
	if m.githubApp != nil {
		if !appEligible {
			return nil, nil, todoTeach("remote_proof", "use a current Project TODO proof with its explicit repository and GitHub App connection", ErrConflict)
		}
		switch route {
		case "/internal/v1/proofs/commit", "/internal/v1/proofs/code-anchor", "/internal/v1/proofs/arrival":
		default:
			return nil, nil, ErrInvalid
		}
		if m.scopedProofReaderToken == "" || m.scopedProofReaderToken == bearer {
			return nil, nil, todoTeach("remote_proof", "configure the dedicated Registry-to-Reader scoped proof credential", ErrUnavailable)
		}
		company, project, alias := query.Get("company_id"), query.Get("project_id"), query.Get("repository")
		binding, err := m.githubProofReadBinding(ctx, company, project, alias)
		if err != nil {
			return nil, nil, err
		}
		credential, repo, err := m.githubApp.repositoryReadToken(ctx, binding.connection)
		if errors.Is(err, errGitHubAccessDenied) {
			return nil, nil, githubProofAccessDenied()
		}
		if err != nil {
			return nil, nil, githubProofProviderUnavailable()
		}
		lease = &githubProofLease{company: company, project: project, alias: alias, binding: binding}
		query.Set("github_repository", repo.FullName)
		query.Set("github_repository_node_id", repo.NodeID)
		token, bearer = credential.value, m.scopedProofReaderToken
		scoped := *client
		scoped.Jar = nil
		scoped.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		client = &scoped
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, m.proofReaderURL+route+"?"+query.Encode(), nil)
	if err != nil {
		return nil, nil, ErrUnavailable
	}
	request.Header.Set("Authorization", "Bearer "+bearer)
	if lease != nil {
		request.Header.Set(githubScopedProofTokenHeader, token)
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, nil, todoTeach("remote_proof", "retry when the private Reader can verify the remote repository", ErrUnavailable)
	}
	return response, lease, nil
}

// Fence the result before it can advance a TODO. Registry retains the
// historical Builder alias in all proof output.
func (m *MongoStore) fenceGitHubReaderProof(ctx context.Context, lease *githubProofLease, resultNode string) error {
	if lease == nil {
		return nil
	}
	if resultNode != lease.binding.connection.RepositoryNodeID {
		return todoTeach("remote_proof.repository_identity", "match the immutable repository authorized for this private proof", ErrConflict)
	}
	current, err := m.githubProofReadBinding(ctx, lease.company, lease.project, lease.alias)
	if err != nil {
		return err
	}
	previous := lease.binding
	if current.membershipRevision != previous.membershipRevision || current.connection.ProjectID != previous.connection.ProjectID ||
		current.connection.Revision != previous.connection.Revision || current.connection.GitHubRepositoryID != previous.connection.GitHubRepositoryID ||
		current.connection.RepositoryNodeID != previous.connection.RepositoryNodeID || current.connection.InstallationID != previous.connection.InstallationID ||
		current.connection.AccountID != previous.connection.AccountID {
		return githubProofBindingChanged()
	}
	if leases, ok := ctx.Value(githubProofLeasesKey{}).(map[string]*githubProofLease); ok {
		if earlier := leases[lease.alias]; earlier != nil && earlier.binding != lease.binding {
			return githubProofBindingChanged()
		}
		leases[lease.alias] = lease
	}
	return nil
}
