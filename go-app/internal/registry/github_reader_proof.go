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
	company, project, alias     string
	binding                     githubProofBinding
	providerName, providerToken string
}

type githubProofLeasesKey struct{}

func guardGitHubProofLease(ctx context.Context, db *mongo.Database, lease githubProofLease) error {
	filter := bson.M{"id": lease.project, "implementation_repositories": lease.alias, "implementation_repositories_revision": lease.binding.membershipRevision}
	if lease.binding.membershipRevision == 0 {
		delete(filter, "implementation_repositories_revision")
		filter["$or"] = bson.A{bson.M{"implementation_repositories_revision": 0}, bson.M{"implementation_repositories_revision": bson.M{"$exists": false}}}
	}
	err := db.Collection(projectsCollection).FindOne(ctx, filter, options.FindOne().SetProjection(bson.M{"_id": 1})).Err()
	if errors.Is(err, mongo.ErrNoDocuments) {
		return githubProofBindingChangedFor(&lease)
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
		return githubProofBindingChangedFor(&lease)
	}
	return nil
}

func githubProofBindingChanged() error {
	return todoTeach("remote_proof.repository_identity", "reload the current Project repository confirmation and connection before requesting proof", ErrConflict)
}

func githubProofBindingChangedFor(lease *githubProofLease) error {
	return promotionRepositoryConnectionError(githubProofBindingChanged(), lease.alias, lease.binding.connection.RepositoryNodeID, true)
}

func promotionRepositoryConnectionError(err error, alias, node string, changed bool) error {
	code, status := "repository_connection_unavailable", "connection_unavailable"
	if changed {
		code, status = "repository_connection_changed", "connection_changed"
	}
	row := promotionRepositoryDiagnostic{RepositoryID: alias, Status: status, ExpectedNodeID: node}
	return withTeachDiagnostic(err, TeachDiagnostic{
		Code: code, FieldPath: "remote_proof.repository_identity", Effects: TeachEffectsNone, Retry: TeachRetryReadReassess,
		NextAction: "Read the confirmed repository (MCP: builder_implementation_repositories_get) and compare its Node ID. If installation state is unavailable, ask the operator to inspect this exact connection. Then read the TODO and reassess before retrying.",
		Promotion:  &promotionDiagnostic{Repositories: []promotionRepositoryDiagnostic{row}},
	})
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
	return m.requestReaderProofWithLease(ctx, route, query, appEligible, nil)
}

func (m *MongoStore) requestReaderProofWithLease(ctx context.Context, route string, query url.Values, appEligible bool, lease *githubProofLease) (*http.Response, *githubProofLease, error) {
	if m.proofReaderURL == "" || m.proofHTTPClient == nil || m.proofReaderToken == "" {
		return nil, nil, todoTeach("remote_proof", "configure the private Project Reader before requesting proof", ErrUnavailable)
	}
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
		if lease == nil {
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
			lease = &githubProofLease{company: company, project: project, alias: alias, binding: binding, providerName: repo.FullName, providerToken: credential.value}
		} else {
			if route != "/internal/v1/proofs/arrival" || lease.company != company || lease.project != project || lease.alias != alias ||
				lease.providerName == "" || lease.providerToken == "" {
				return nil, nil, ErrInvalid
			}
			if err := m.fenceGitHubReaderProof(ctx, lease, lease.binding.connection.RepositoryNodeID); err != nil {
				return nil, lease, err
			}
		}
		query.Set("github_repository", lease.providerName)
		query.Set("github_repository_node_id", lease.binding.connection.RepositoryNodeID)
		bearer = m.scopedProofReaderToken
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
		request.Header.Set(githubScopedProofTokenHeader, lease.providerToken)
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
		if errors.Is(err, ErrConflict) || errors.Is(err, ErrNotFound) {
			return githubProofBindingChangedFor(lease)
		}
		return err
	}
	previous := lease.binding
	if current.membershipRevision != previous.membershipRevision || current.connection.ProjectID != previous.connection.ProjectID ||
		current.connection.Revision != previous.connection.Revision || current.connection.GitHubRepositoryID != previous.connection.GitHubRepositoryID ||
		current.connection.RepositoryNodeID != previous.connection.RepositoryNodeID || current.connection.InstallationID != previous.connection.InstallationID ||
		current.connection.AccountID != previous.connection.AccountID {
		return githubProofBindingChangedFor(lease)
	}
	if leases, ok := ctx.Value(githubProofLeasesKey{}).(map[string]*githubProofLease); ok {
		if earlier := leases[lease.alias]; earlier != nil && earlier.binding != lease.binding {
			return githubProofBindingChangedFor(lease)
		}
		leases[lease.alias] = lease
	}
	return nil
}
