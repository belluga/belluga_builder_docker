package registry

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// A visual identity pin belongs to a specific existing binding revision. An
// ordinary binding change leaves the old revision's pin unusable.
type githubVisualConnectionPin struct {
	BindingRevision    int64 `bson:"binding_revision"`
	GitHubRepositoryID int64 `bson:"github_repository_id"`
}

func (a *githubConnectionAdmin) finalize(ctx context.Context, company, project string, operator githubOperator, operation string, req githubConnectionFinalizeRequest) (githubConnectionStatus, error) {
	zero := githubConnectionStatus{}
	db, op, err := a.load(ctx, company, project, operator, operation)
	if err != nil {
		return zero, err
	}
	if req.ExpectedRoleRevision == nil || req.ExpectedConnectionRevision == nil ||
		*req.ExpectedRoleRevision != op.ExpectedRoleRevision || *req.ExpectedConnectionRevision != op.ExpectedConnectionRevision ||
		(op.Action == "connect" && req.GitHubRepositoryID <= 0) || (op.Action == "create" && req.GitHubRepositoryID != 0) {
		return zero, ErrConflict
	}
	digest := githubRequestDigest(req)
	if op.FinalizeDigest != "" && op.FinalizeDigest != digest {
		return zero, ErrConflict
	}
	switch op.State {
	case githubConnectionCreating:
		// A pre-POST claim can survive a restart with no provider receipt.
		// After the HTTP deadline and receipt persistence window, make the
		// uncertainty explicit without issuing a second create request.
		now := a.app.now().UTC()
		if now.Sub(op.UpdatedAt) > 30*time.Second {
			op.State = githubConnectionUncertain
			if err := recordGitHubCreationResult(ctx, db, op, githubAppRepository{}, now); err != nil {
				return zero, err
			}
		}
		return githubOperationStatus(company, op), nil
	case githubConnectionCompleted, githubConnectionUncertain, githubConnectionFailed:
		return githubOperationStatus(company, op), nil
	case githubConnectionVerified, githubConnectionAwaitingRepositoryAccess:
	default:
		return zero, ErrConflict
	}
	if op.ExpiresAt != nil && !op.ExpiresAt.After(a.app.now()) {
		return zero, todoTeach("operation_id", "start a new GitHub connection operation after expiry", ErrConflict)
	}
	doc, err := readGitHubAdminProject(ctx, db, company, project)
	if err != nil {
		return zero, err
	}
	if githubRoleRevision(doc, op.Role) != op.ExpectedRoleRevision {
		return zero, todoTeach("expected_role_revision", "reload the current Project role and start a fresh operation", ErrConflict)
	}
	token, ok := a.userToken(company, op)
	if !ok {
		return a.beginOAuth(ctx, db, company, op)
	}
	user, err := a.app.user(ctx, token)
	if err != nil {
		return zero, githubAdminProviderError(err)
	}
	if user.ID != op.GitHubOperatorID {
		return zero, ErrConflict
	}
	installation, err := a.app.userInstallation(ctx, token, op.VerifiedInstallationID, op.TargetAccountID)
	if err != nil {
		return zero, githubAdminProviderError(err)
	}

	if op.Action == "create" && op.State == githubConnectionVerified {
		if installation.Account.Type != "Organization" || op.ExpectedConnectionRevision != 0 {
			return zero, ErrInvalid
		}
		now := a.app.now().UTC()
		if err := claimGitHubRepositoryCreation(ctx, db, op, digest, now); err != nil {
			if errors.Is(err, ErrConflict) {
				_, current, readErr := a.load(ctx, company, project, operator, operation)
				if readErr != nil {
					return zero, readErr
				}
				if current.FinalizeDigest == digest && current.State != githubConnectionVerified {
					return githubOperationStatus(company, current), nil
				}
			}
			return zero, err
		}
		op.State, op.FinalizeDigest = githubConnectionCreating, digest
		repo, createErr := a.app.createOrganizationRepository(ctx, token, installation, op.TargetRepositoryName)
		// Keep the provider receipt even when the browser disconnects. This
		// only persists local Mongo state; it never retries the provider POST.
		persist, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if createErr != nil {
			op.State = githubConnectionFailed
			if errors.Is(createErr, errGitHubCreationUncertain) {
				op.State = githubConnectionUncertain
			}
			if err := recordGitHubCreationResult(persist, db, op, githubAppRepository{}, a.app.now().UTC()); err != nil {
				return zero, err
			}
			a.forgetUserToken(company, op)
			if op.State == githubConnectionFailed {
				return zero, githubAdminProviderError(createErr)
			}
			return githubOperationStatus(company, op), nil
		}
		op.State = githubConnectionAwaitingRepositoryAccess
		if err := recordGitHubCreationResult(persist, db, op, repo, a.app.now().UTC()); err != nil {
			return zero, err
		}
		op.ResultRepositoryID, op.ResultRepositoryNodeID = repo.ID, repo.NodeID
		op.ResultRepositoryFullName, op.ResultRepositoryDefaultBranch = repo.FullName, repo.DefaultBranch
	}

	id := req.GitHubRepositoryID
	if op.Action == "create" {
		id = op.ResultRepositoryID
	}
	if id <= 0 {
		return zero, ErrConflict
	}
	repo, err := a.app.userInstallationRepository(ctx, token, installation.ID, op.TargetAccountID, id)
	if err != nil {
		if op.State == githubConnectionAwaitingRepositoryAccess && errors.Is(err, errGitHubAccessDenied) {
			return githubOperationStatus(company, op), nil
		}
		return zero, githubAdminProviderError(err)
	}
	if op.Action == "create" && repo.NodeID != op.ResultRepositoryNodeID {
		return zero, ErrConflict
	}
	connection := githubRepositoryConnection{ProjectID: project, GitHubRepositoryID: repo.ID, RepositoryNodeID: repo.NodeID,
		RepositoryFullName: repo.FullName, AccountID: repo.Owner.ID, InstallationID: installation.ID}
	scoped, metadata, err := a.app.repositoryReadToken(ctx, connection)
	if err != nil {
		if op.State == githubConnectionAwaitingRepositoryAccess && errors.Is(err, errGitHubAccessDenied) {
			return githubOperationStatus(company, op), nil
		}
		return zero, githubAdminProviderError(err)
	}
	connection.RepositoryFullName = metadata.FullName
	if op.Role == "visual" {
		if op.Action == "create" && op.PublicationBranch == "" {
			// Auto-initialization follows the organization's default branch.
			// The durable provider receipt, not a guessed "main", supplies it.
			op.PublicationBranch = op.ResultRepositoryDefaultBranch
		}
		if !validGitBranch(op.PublicationBranch) {
			return zero, ErrConflict
		}
		var branch struct {
			Name string `json:"name"`
		}
		if _, err := a.app.apiJSON(ctx, http.MethodGet, "/repos/"+metadata.FullName+"/branches/"+url.PathEscape(op.PublicationBranch), scoped.value, nil, &branch); err != nil {
			if errors.Is(err, errGitHubAccessDenied) {
				if op.Action == "create" {
					return zero, todoTeach("publication_branch", "the repository was created; use a new connect operation for an existing branch without creating again", ErrConflict)
				}
				return zero, todoTeach("publication_branch", "select an existing authorized publication branch", ErrConflict)
			}
			return zero, githubAdminProviderError(err)
		}
		if branch.Name != op.PublicationBranch {
			return zero, ErrConflict
		}
	}
	out, err := a.complete(ctx, db, company, operator, op, digest, connection, metadata)
	if err == nil {
		a.forgetUserToken(company, op)
	}
	return out, err
}

func recordGitHubCreationResult(ctx context.Context, db *mongo.Database, op githubConnectionOperation, repo githubAppRepository, now time.Time) error {
	filter := githubOperatorFilter(op.ProjectID, githubOperator{op.OperatorIssuer, op.OperatorSubject}, op.OperationID)
	filter["state"], filter["finalize_digest"] = githubConnectionCreating, op.FinalizeDigest
	filter["result_repository_id"] = bson.M{"$exists": false}
	filter["create_reserved"] = true
	if op.State == githubConnectionAwaitingRepositoryAccess {
		filter["state"] = bson.M{"$in": []githubConnectionState{githubConnectionCreating, githubConnectionUncertain}}
	}
	fields := bson.M{"state": op.State, "updated_at": now}
	if op.State == githubConnectionFailed {
		fields["create_reserved"] = false
	}
	if op.State == githubConnectionAwaitingRepositoryAccess {
		fields["result_repository_id"], fields["result_repository_node_id"] = repo.ID, repo.NodeID
		fields["result_repository_full_name"], fields["result_repository_default_branch"] = repo.FullName, repo.DefaultBranch
	}
	return githubAdminUpdate(ctx, db, filter, bson.M{"$set": fields})
}

func (a *githubConnectionAdmin) complete(ctx context.Context, db *mongo.Database, company string, operator githubOperator, op githubConnectionOperation, digest string, connection githubRepositoryConnection, metadata githubAppRepository) (githubConnectionStatus, error) {
	session, err := a.store.client.StartSession()
	if err != nil {
		return githubConnectionStatus{}, ErrUnavailable
	}
	defer session.EndSession(ctx)
	result, err := session.WithTransaction(ctx, func(tx context.Context) (any, error) {
		var live githubConnectionOperation
		filter := githubOperatorFilter(op.ProjectID, operator, op.OperationID)
		if err := db.Collection(githubConnectionOperationsCollection).FindOne(tx, filter).Decode(&live); err != nil {
			if mongoTransactionShouldRetry(err) {
				return nil, err
			}
			return nil, normalizeMongoError(err)
		}
		if live.State == githubConnectionCompleted && live.FinalizeDigest == digest {
			return githubOperationStatus(company, live), nil
		}
		now := a.app.now().UTC()
		if live.State != op.State || live.GitHubOperatorID != op.GitHubOperatorID || live.VerifiedInstallationID != op.VerifiedInstallationID ||
			live.StartDigest != op.StartDigest || live.FinalizeDigest != op.FinalizeDigest ||
			(live.ExpiresAt != nil && !live.ExpiresAt.After(now)) {
			return nil, ErrConflict
		}
		doc, err := readGitHubAdminProject(tx, db, company, op.ProjectID)
		if err != nil {
			return nil, err
		}
		if githubRoleRevision(doc, op.Role) != op.ExpectedRoleRevision {
			return nil, ErrConflict
		}
		if err := writeGitHubRepositoryConnectionCAS(tx, db, connection, op.ExpectedConnectionRevision, now); err != nil {
			return nil, err
		}
		if err := a.applyRole(tx, db, company, operator, op, doc, connection); err != nil {
			return nil, err
		}
		filter["state"] = op.State
		fields := bson.M{"state": githubConnectionCompleted, "finalize_digest": digest, "updated_at": now, "create_reserved": false}
		if op.Action == "connect" {
			fields["result_repository_id"], fields["result_repository_node_id"] = metadata.ID, metadata.NodeID
			fields["result_repository_full_name"], fields["result_repository_default_branch"] = metadata.FullName, metadata.DefaultBranch
			live.ResultRepositoryID, live.ResultRepositoryNodeID = metadata.ID, metadata.NodeID
			live.ResultRepositoryFullName, live.ResultRepositoryDefaultBranch = metadata.FullName, metadata.DefaultBranch
		}
		if err := githubAdminUpdate(tx, db, filter, bson.M{"$set": fields, "$unset": bson.M{"expires_at": ""}}); err != nil {
			return nil, err
		}
		live.State = githubConnectionCompleted
		return githubOperationStatus(company, live), nil
	})
	if err != nil {
		return githubConnectionStatus{}, err
	}
	return result.(githubConnectionStatus), nil
}

func (a *githubConnectionAdmin) applyRole(ctx context.Context, db *mongo.Database, company string, operator githubOperator, op githubConnectionOperation, doc projectDocument, connection githubRepositoryConnection) error {
	// Derive a stable internal ID for the existing role mutation. The caller's
	// operation ID cannot collide with an ordinary Project role request.
	roleOp := "github-role-" + strings.TrimPrefix(githubRequestDigest([]string{company, op.ProjectID, op.OperationID}), "sha256:")[:32]
	if op.Role == "visual" {
		revision := doc.BindingRevision
		if doc.Binding != nil && strings.EqualFold(doc.Binding.RepositoryID, connection.RepositoryFullName) &&
			doc.Binding.PublicationBranch == op.PublicationBranch && doc.GitHubVisualConnection != nil &&
			doc.GitHubVisualConnection.BindingRevision == doc.BindingRevision &&
			doc.GitHubVisualConnection.GitHubRepositoryID != connection.GitHubRepositoryID {
			return todoTeach("github_repository_id", "replace the visual binding explicitly after immutable repository identity changes", ErrConflict)
		}
		if doc.Binding == nil || !strings.EqualFold(doc.Binding.RepositoryID, connection.RepositoryFullName) || doc.Binding.PublicationBranch != op.PublicationBranch {
			action := "bind"
			if doc.Binding != nil && (doc.Binding.RepositoryID != "" || doc.Binding.PublicationBranch != "") {
				action = "replace"
			}
			binding, err := a.store.ChangeRepositoryBinding(ctx, company, op.ProjectID, RepositoryBindingRequest{
				Action: action, ProjectID: op.ProjectID, RepositoryID: connection.RepositoryFullName, Branch: op.PublicationBranch,
				ExpectedRevision: revision, OperationID: roleOp, Actor: operator.Subject,
			})
			if err != nil {
				return err
			}
			revision = binding.Revision
		}
		filter := bson.M{"_id": doc.ObjectID, "company_id": company, "binding_revision": revision}
		if revision == 0 {
			filter["binding_revision"] = bson.M{"$in": bson.A{nil, int64(0)}}
		}
		res, err := db.Collection(projectsCollection).UpdateOne(ctx, filter, bson.M{"$set": bson.M{"github_visual_connection": githubVisualConnectionPin{revision, connection.GitHubRepositoryID}}})
		if err != nil {
			if mongoTransactionShouldRetry(err) {
				return err
			}
			return normalizeMongoError(err)
		}
		if res.MatchedCount != 1 {
			return ErrConflict
		}
		return nil
	}

	alias := connection.RepositoryFullName
	pins, err := readImplementationRepositoryNodeIDs(ctx, db, op.ProjectID)
	if err != nil {
		return err
	}
	var matching string
	for _, existing := range doc.ImplementationRepositories {
		if pins[existing] == connection.RepositoryNodeID {
			if matching != "" {
				return todoTeach("github_repository_id", "resolve multiple confirmed aliases pinned to this exact repository", ErrConflict)
			}
			matching = existing
		}
	}
	if matching != "" {
		alias = matching
	}
	if _, err := a.store.ChangeImplementationRepository(ctx, company, op.ProjectID, ImplementationRepositoryChange{
		Action: "confirm", ProjectID: op.ProjectID, RepositoryID: alias, ExpectedRevision: doc.ImplementationRepositoriesRevision,
		OperationID: roleOp, Actor: operator.Subject, OwnerReference: "GitHub App operator connection " + op.OperationID,
	}); err != nil {
		return err
	}
	if containsTODOString(doc.ImplementationRepositories, alias) {
		// Confirm is a no-op for existing members. Fence the membership in
		// the same transaction so removal cannot race a new credential pin.
		if err := advanceImplementationRepositoryMembershipCASInTransaction(ctx, db, op.ProjectID, []string{alias}); err != nil {
			return err
		}
	}
	return pinImplementationRepositoryNodeIDInTransaction(ctx, db, op.ProjectID, alias, connection.RepositoryNodeID,
		githubRequestDigest([]any{connection.GitHubRepositoryID, connection.RepositoryNodeID, connection.AccountID, connection.InstallationID}))
}

// abandon releases a reserved uncertain target after the original operator
// explicitly accepts that its provider outcome is unknown. It never calls
// GitHub, adopts a name collision, or deletes a repository.
func (a *githubConnectionAdmin) abandon(ctx context.Context, company, project string, operator githubOperator, operation string, acknowledgeUncertainResult bool) (githubConnectionStatus, error) {
	if !acknowledgeUncertainResult {
		return githubConnectionStatus{}, ErrInvalid
	}
	db, op, err := a.load(ctx, company, project, operator, operation)
	if err != nil {
		return githubConnectionStatus{}, err
	}
	if op.Action != "create" || op.State != githubConnectionUncertain || !op.CreateReserved || op.FinalizeDigest == "" || op.ResultRepositoryID != 0 {
		return githubConnectionStatus{}, ErrConflict
	}
	filter := githubOperatorFilter(op.ProjectID, operator, op.OperationID)
	filter["state"], filter["create_reserved"], filter["finalize_digest"] = githubConnectionUncertain, true, op.FinalizeDigest
	filter["result_repository_id"] = bson.M{"$exists": false}
	now := a.app.now().UTC()
	if err := githubAdminUpdate(ctx, db, filter, bson.M{"$set": bson.M{"state": githubConnectionFailed, "create_reserved": false, "updated_at": now, "abandoned_at": now}}); err != nil {
		return githubConnectionStatus{}, err
	}
	op.State = githubConnectionFailed
	a.forgetUserToken(company, op)
	return githubOperationStatus(company, op), nil
}
