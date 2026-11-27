package registry

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

var errGitHubVisualAccessDenied = errors.New("GitHub visual repository connection or access unavailable")

type githubVisualSnapshot struct {
	binding    RepositoryBinding
	connection githubRepositoryConnection
}

type githubVisualLease struct {
	company, project string
	snapshot         githubVisualSnapshot
	remote           string
	token            githubAppToken
}

type githubVisualLeaseContextKey struct{}

func githubVisualLeaseFrom(ctx context.Context) (githubVisualLease, bool) {
	lease, ok := ctx.Value(githubVisualLeaseContextKey{}).(githubVisualLease)
	return lease, ok
}

func githubVisualDenied() error {
	return withTeachDiagnostic(errors.Join(ErrConflict, errGitHubVisualAccessDenied), *readTeach("github_visual_access_denied", "repository_binding", "Connect or restore GitHub App access to this Project's exact visual repository."))
}

func githubVisualUnavailable() error {
	return withTeachDiagnostic(ErrUnavailable, *readTeach("github_visual_provider_unavailable", "repository_binding", "Restore GitHub App repository verification before requesting this artifact again."))
}

func (m *MongoStore) githubVisualReadSnapshot(ctx context.Context, companyID, projectID string) (githubVisualSnapshot, error) {
	var zero githubVisualSnapshot
	if !validID(companyID) || !validID(projectID) {
		return zero, ErrInvalid
	}
	if m.githubVisualSnapshotReader != nil {
		return m.githubVisualSnapshotReader(ctx, companyID, projectID)
	}
	company, err := m.company(ctx, companyID)
	if err != nil {
		return zero, err
	}
	db := m.client.Database(company.DatabaseName)
	doc, err := readGitHubAdminProject(ctx, db, companyID, projectID)
	if err != nil {
		return zero, err
	}
	binding := repositoryBindingFromProject(doc.toAPI())
	pin := doc.GitHubVisualConnection
	if binding.State != "configured" || !validGitRepositoryIdentity(binding.RepositoryID) || !validGitBranch(binding.PublicationBranch) ||
		pin == nil || pin.BindingRevision != binding.Revision || pin.GitHubRepositoryID <= 0 {
		return zero, githubVisualDenied()
	}
	var connection githubRepositoryConnection
	err = db.Collection(githubRepositoryConnectionsCollection).FindOne(ctx, bson.M{"project_id": projectID, "github_repository_id": pin.GitHubRepositoryID}).Decode(&connection)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return zero, githubVisualDenied()
	}
	if err != nil {
		return zero, normalizeMongoError(err)
	}
	if connection.ProjectID != projectID || connection.GitHubRepositoryID != pin.GitHubRepositoryID || connection.Revision < 1 {
		return zero, githubVisualDenied()
	}
	return githubVisualSnapshot{binding, connection}, nil
}

// Authorize even warm/pinned reads. Only the Git transport consumes the
// per-request credential. Existing evaluators still read hydrated local objects.
func readGitHubVisual[T any](ctx context.Context, m *MongoStore, companyID, projectID string, read func(context.Context) (T, error)) (T, error) {
	if m.githubApp == nil {
		return read(ctx)
	}
	var zero T
	snapshot, err := m.githubVisualReadSnapshot(ctx, companyID, projectID)
	if err != nil {
		return zero, err
	}
	token, repo, err := m.githubApp.repositoryReadToken(ctx, snapshot.connection)
	if errors.Is(err, errGitHubAccessDenied) {
		return zero, githubVisualDenied()
	}
	if err != nil {
		return zero, githubVisualUnavailable()
	}
	scoped := context.WithValue(ctx, githubVisualLeaseContextKey{}, githubVisualLease{
		company: companyID, project: projectID, snapshot: snapshot, remote: "https://github.com/" + repo.FullName + ".git", token: token,
	})
	value, err := read(scoped)
	if err != nil {
		return zero, err
	}
	current, err := m.githubVisualReadSnapshot(ctx, companyID, projectID)
	if err != nil {
		return zero, err
	}
	// Keep storage revocation/rebinding from returning warmed bytes, even if
	// the repository changed while the underlying Git or cache read was running.
	if current.binding != snapshot.binding || current.connection.ProjectID != snapshot.connection.ProjectID ||
		current.connection.GitHubRepositoryID != snapshot.connection.GitHubRepositoryID ||
		current.connection.RepositoryNodeID != snapshot.connection.RepositoryNodeID ||
		current.connection.AccountID != snapshot.connection.AccountID ||
		current.connection.InstallationID != snapshot.connection.InstallationID ||
		current.connection.Revision != snapshot.connection.Revision {
		return zero, todoTeach("repository_binding", "reload the Project visual repository after its connection changed", ErrConflict)
	}
	return value, nil
}

func (v gitDesignSystemVerifier) readRemote(ctx context.Context, repository string) string {
	if lease, ok := githubVisualLeaseFrom(ctx); ok {
		if lease.snapshot.binding.RepositoryID != repository {
			return ""
		}
		return lease.remote
	}
	return v.repositoryURL(repository)
}

func githubVisualCacheSuffix(ctx context.Context, company, project, repository string) (string, bool) {
	lease, ok := githubVisualLeaseFrom(ctx)
	if !ok {
		return "", true
	}
	if lease.company != company || lease.project != project || lease.snapshot.binding.RepositoryID != repository {
		return "", false
	}
	return "\x00github-app\x00" + strconv.FormatInt(lease.snapshot.connection.GitHubRepositoryID, 10) + "\x00" + lease.snapshot.connection.RepositoryNodeID, true
}

func githubVisualGitEnvironment(lease githubVisualLease, credentialed bool) []string {
	env := designSystemGitEnvironment(false)
	if !credentialed {
		return append(env, "GIT_ALLOW_PROTOCOL=")
	}
	auth := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + lease.token.value))
	return append(env[:len(env)-1],
		"GIT_ALLOW_PROTOCOL=https",
		"GIT_CONFIG_KEY_1=http.followRedirects", "GIT_CONFIG_VALUE_1=false",
		"GIT_CONFIG_KEY_2=http."+lease.remote+".extraHeader", "GIT_CONFIG_VALUE_2=AUTHORIZATION: basic "+auth,
		"GIT_CONFIG_COUNT=3")
}

func (h *Handler) githubVisualNoStore(w http.ResponseWriter) {
	if store, ok := h.service.(*MongoStore); ok && store.githubApp != nil {
		w.Header().Set("Cache-Control", "no-store")
	}
}

func setVisualFileDefaultCache(w http.ResponseWriter) {
	if w.Header().Get("Cache-Control") == "" {
		w.Header().Set("Cache-Control", "private, max-age=0, must-revalidate")
	}
}
