package registry

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

const githubConnectionIntentLifetime = 15 * time.Minute

type githubOperator struct {
	Issuer  string
	Subject string
}

type githubConnectionStartRequest struct {
	OperationID                string `json:"operation_id"`
	Role                       string `json:"role"`
	Action                     string `json:"action"`
	ExpectedRoleRevision       *int64 `json:"expected_role_revision"`
	ExpectedConnectionRevision *int64 `json:"expected_connection_revision"`
	TargetAccountID            int64  `json:"target_account_id"`
	TargetRepositoryName       string `json:"target_repository_name,omitempty"`
	PublicationBranch          string `json:"publication_branch,omitempty"`
}

type githubConnectionFinalizeRequest struct {
	GitHubRepositoryID         int64  `json:"github_repository_id,omitempty"`
	ExpectedRoleRevision       *int64 `json:"expected_role_revision"`
	ExpectedConnectionRevision *int64 `json:"expected_connection_revision"`
}

type githubConnectionRepository struct {
	ID                 int64  `json:"github_repository_id"`
	FullName           string `json:"repository_full_name"`
	NodeID             string `json:"repository_node_id"`
	Branch             string `json:"default_branch"`
	ConnectionRevision int64  `json:"connection_revision,omitempty"`
}

type githubConnectionStatus struct {
	State              githubConnectionState       `json:"state"`
	OperationID        string                      `json:"operation_id"`
	CompanyID          string                      `json:"company_id"`
	ProjectID          string                      `json:"project_id"`
	AuthorizationURL   string                      `json:"authorization_url,omitempty"`
	GitHubAccountLogin string                      `json:"github_account_login,omitempty"`
	Repository         *githubConnectionRepository `json:"repository,omitempty"`
}

type githubConnectionRepositoryPage struct {
	Repositories []githubConnectionRepository `json:"repositories"`
	HasNext      bool                         `json:"has_next"`
}

type githubConnectionLocator struct {
	CompanyID   string `json:"company_id"`
	ProjectID   string `json:"project_id"`
	OperationID string `json:"operation_id"`
	Random      string `json:"nonce"`
}

type githubOperationKey struct {
	company, project, operation, issuer, subject string
}

type githubEphemeralOAuth struct {
	verifier string
	digest   string
	expires  time.Time
}

type githubConnectionAdmin struct {
	store *MongoStore
	app   *githubAppClient
	mu    sync.Mutex
	oauth map[githubOperationKey]githubEphemeralOAuth
	users map[githubOperationKey]githubAppToken
}

func newGitHubConnectionAdmin(store *MongoStore, app *githubAppClient) *githubConnectionAdmin {
	return &githubConnectionAdmin{store: store, app: app, oauth: make(map[githubOperationKey]githubEphemeralOAuth), users: make(map[githubOperationKey]githubAppToken)}
}

func githubAdminKey(companyID string, op githubConnectionOperation) githubOperationKey {
	return githubOperationKey{companyID, op.ProjectID, op.OperationID, op.OperatorIssuer, op.OperatorSubject}
}

func githubStateValue(company, project, operation string) (string, error) {
	var randomness [32]byte
	if _, err := rand.Read(randomness[:]); err != nil {
		return "", ErrUnavailable
	}
	state, err := json.Marshal(githubConnectionLocator{company, project, operation, base64.RawURLEncoding.EncodeToString(randomness[:])})
	if err != nil {
		return "", ErrUnavailable
	}
	return base64.RawURLEncoding.EncodeToString(state), nil
}

func parseGitHubState(value string) (githubConnectionLocator, error) {
	var state githubConnectionLocator
	if len(value) > 1024 {
		return state, ErrInvalid
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || json.Unmarshal(decoded, &state) != nil || !validID(state.CompanyID) || !validID(state.ProjectID) || !validID(state.OperationID) {
		return state, ErrInvalid
	}
	random, err := base64.RawURLEncoding.DecodeString(state.Random)
	if err != nil || len(random) != 32 {
		return state, ErrInvalid
	}
	return state, nil
}

func githubRequestDigest(value any) string {
	encoded, _ := json.Marshal(value)
	return sourceHash(encoded)
}

func githubOperationStatus(company string, op githubConnectionOperation) githubConnectionStatus {
	out := githubConnectionStatus{CompanyID: company, ProjectID: op.ProjectID, OperationID: op.OperationID, State: op.State, GitHubAccountLogin: op.GitHubAccountLogin}
	if op.ResultRepositoryID > 0 {
		out.Repository = &githubConnectionRepository{ID: op.ResultRepositoryID, NodeID: op.ResultRepositoryNodeID, FullName: op.ResultRepositoryFullName, Branch: op.ResultRepositoryDefaultBranch}
	}
	return out
}

func githubOperatorFilter(project string, operator githubOperator, operation string) bson.M {
	return bson.M{"project_id": project, "operation_id": operation, "operator_issuer": operator.Issuer, "operator_subject": operator.Subject}
}

func (a *githubConnectionAdmin) database(ctx context.Context, company, project string, operator githubOperator) (*mongo.Database, error) {
	if !validID(company) || !validID(project) || !validSummaryField(operator.Issuer, 256) || !validSummaryField(operator.Subject, 256) {
		return nil, ErrInvalid
	}
	tenant, err := a.store.company(ctx, company)
	if err != nil {
		return nil, err
	}
	return a.store.client.Database(tenant.DatabaseName), nil
}

func readGitHubAdminProject(ctx context.Context, db *mongo.Database, company, project string) (projectDocument, error) {
	var doc projectDocument
	err := db.Collection(projectsCollection).FindOne(ctx, bson.M{"id": project, "company_id": company}).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return doc, ErrNotFound
	}
	if err != nil {
		return doc, normalizeMongoError(err)
	}
	return doc, nil
}

func githubRoleRevision(doc projectDocument, role string) int64 {
	if role == "visual" {
		return doc.BindingRevision
	}
	return doc.ImplementationRepositoriesRevision
}

func (a *githubConnectionAdmin) load(ctx context.Context, company, project string, operator githubOperator, operation string) (*mongo.Database, githubConnectionOperation, error) {
	var op githubConnectionOperation
	if !validID(operation) {
		return nil, op, ErrInvalid
	}
	db, err := a.database(ctx, company, project, operator)
	if err != nil {
		return nil, op, err
	}
	err = db.Collection(githubConnectionOperationsCollection).FindOne(ctx, githubOperatorFilter(project, operator, operation)).Decode(&op)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, op, ErrNotFound
	}
	if err != nil {
		return nil, op, normalizeMongoError(err)
	}
	return db, op, nil
}

func (a *githubConnectionAdmin) start(ctx context.Context, company, project string, operator githubOperator, request githubConnectionStartRequest) (githubConnectionStatus, error) {
	var zero githubConnectionStatus
	if !validID(request.OperationID) || request.ExpectedRoleRevision == nil || request.ExpectedConnectionRevision == nil ||
		*request.ExpectedRoleRevision < 0 || *request.ExpectedConnectionRevision < 0 || request.TargetAccountID <= 0 ||
		(request.Role != "visual" && request.Role != "implementation") ||
		(request.Action != "connect" && request.Action != "create") ||
		(request.Role == "visual" && (request.Action == "connect" || request.PublicationBranch != "") && !validGitBranch(request.PublicationBranch)) ||
		(request.Role == "implementation" && request.PublicationBranch != "") ||
		(request.Action == "create" && !validGitRepositoryID("owner/"+request.TargetRepositoryName)) ||
		(request.Action == "connect" && request.TargetRepositoryName != "") {
		return zero, ErrInvalid
	}
	db, err := a.database(ctx, company, project, operator)
	if err != nil {
		return zero, err
	}
	collection := db.Collection(githubConnectionOperationsCollection)
	digest := githubRequestDigest(request)
	var old githubConnectionOperation
	err = collection.FindOne(ctx, bson.M{"project_id": project, "operation_id": request.OperationID}).Decode(&old)
	if err == nil {
		if old.OperatorIssuer != operator.Issuer || old.OperatorSubject != operator.Subject || old.StartDigest != digest {
			return zero, ErrConflict
		}
		if old.State != githubConnectionPending || old.SetupNonce.ConsumedAt != nil {
			if (old.State == githubConnectionPending || old.State == githubConnectionVerified || old.State == githubConnectionAwaitingRepositoryAccess) && old.SetupInstallationID > 0 {
				if _, ok := a.userToken(company, old); !ok {
					return a.beginOAuth(ctx, db, company, old)
				}
			}
			return githubOperationStatus(company, old), nil
		}
		if old.ExpiresAt == nil {
			return zero, ErrConflict
		}
		// An exact POST may rotate an unconsumed setup nonce after a reload.
		// A GET never mutates, and callbacks for the replaced nonce are rejected.
		raw, stateErr := githubStateValue(company, project, request.OperationID)
		if stateErr != nil {
			return zero, stateErr
		}
		now := a.app.now().UTC()
		filter := githubOperatorFilter(project, operator, request.OperationID)
		filter["state"] = githubConnectionPending
		filter["setup_nonce.digest"] = old.SetupNonce.Digest
		filter["setup_nonce.consumed_at"] = bson.M{"$exists": false}
		filter["expires_at"] = bson.M{"$gt": now}
		if err := githubAdminUpdate(ctx, db, filter, bson.M{"$set": bson.M{"setup_nonce": githubConnectionNonce{Digest: sourceHash([]byte(raw)), ExpiresAt: *old.ExpiresAt}, "updated_at": now}}); err != nil {
			return zero, err
		}
		out := githubOperationStatus(company, old)
		out.AuthorizationURL = a.app.setupURL(raw)
		return out, nil
	}
	if !errors.Is(err, mongo.ErrNoDocuments) {
		return zero, normalizeMongoError(err)
	}
	doc, err := readGitHubAdminProject(ctx, db, company, project)
	if err != nil {
		return zero, err
	}
	if githubRoleRevision(doc, request.Role) != *request.ExpectedRoleRevision {
		return zero, ErrConflict
	}
	raw, err := githubStateValue(company, project, request.OperationID)
	if err != nil {
		return zero, err
	}
	now := a.app.now().UTC()
	expiry := now.Add(githubConnectionIntentLifetime)
	op := githubConnectionOperation{
		ProjectID: project, OperationID: request.OperationID, OperatorIssuer: operator.Issuer, OperatorSubject: operator.Subject,
		Role: request.Role, Action: request.Action, StartDigest: digest, ExpectedRoleRevision: *request.ExpectedRoleRevision,
		ExpectedConnectionRevision: *request.ExpectedConnectionRevision, TargetAccountID: request.TargetAccountID,
		TargetRepositoryName: request.TargetRepositoryName, TargetRepositoryNameNormalized: strings.ToLower(request.TargetRepositoryName),
		PublicationBranch: request.PublicationBranch, State: githubConnectionPending, CreatedAt: now, UpdatedAt: now,
		SetupNonce: githubConnectionNonce{Digest: sourceHash([]byte(raw)), ExpiresAt: expiry}, ExpiresAt: &expiry,
	}
	if _, err := collection.InsertOne(ctx, op); err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return zero, ErrConflict
		}
		return zero, normalizeMongoError(err)
	}
	out := githubOperationStatus(company, op)
	out.AuthorizationURL = a.app.setupURL(raw)
	return out, nil
}

func githubAdminUpdate(ctx context.Context, db *mongo.Database, filter, update bson.M) error {
	result, err := db.Collection(githubConnectionOperationsCollection).UpdateOne(ctx, filter, update)
	if mongo.IsDuplicateKeyError(err) {
		return ErrConflict
	}
	if err != nil {
		if mongoTransactionShouldRetry(err) {
			return err
		}
		return normalizeMongoError(err)
	}
	if result.MatchedCount != 1 {
		return ErrConflict
	}
	return nil
}

func (a *githubConnectionAdmin) status(ctx context.Context, company, project string, operator githubOperator, operation string) (githubConnectionStatus, error) {
	_, op, err := a.load(ctx, company, project, operator, operation)
	if err != nil {
		return githubConnectionStatus{}, err
	}
	return githubOperationStatus(company, op), nil
}

func githubNonceUsable(n githubConnectionNonce, raw string, now time.Time) bool {
	return n.ConsumedAt == nil && now.Before(n.ExpiresAt) && n.Digest == sourceHash([]byte(raw))
}

func (a *githubConnectionAdmin) setupCallback(ctx context.Context, operator githubOperator, raw string, installationID int64) (githubConnectionStatus, error) {
	state, err := parseGitHubState(raw)
	if err != nil || installationID <= 0 {
		return githubConnectionStatus{}, ErrInvalid
	}
	db, op, err := a.load(ctx, state.CompanyID, state.ProjectID, operator, state.OperationID)
	if err != nil {
		return githubConnectionStatus{}, err
	}
	now := a.app.now().UTC()
	if op.State != githubConnectionPending || !githubNonceUsable(op.SetupNonce, raw, now) {
		return githubConnectionStatus{}, ErrConflict
	}
	filter := githubOperatorFilter(op.ProjectID, operator, op.OperationID)
	filter["state"] = githubConnectionPending
	filter["setup_nonce.digest"] = op.SetupNonce.Digest
	filter["setup_nonce.consumed_at"] = bson.M{"$exists": false}
	filter["setup_nonce.expires_at"] = bson.M{"$gt": now}
	update := bson.M{"$set": bson.M{"setup_nonce.consumed_at": now, "setup_installation_id": installationID, "updated_at": now}}
	if err := githubAdminUpdate(ctx, db, filter, update); err != nil {
		return githubConnectionStatus{}, err
	}
	op.SetupNonce.ConsumedAt, op.SetupInstallationID = &now, installationID
	return a.beginOAuth(ctx, db, state.CompanyID, op)
}

func (a *githubConnectionAdmin) pruneExpiredLocked(now time.Time) {
	for key, pending := range a.oauth {
		if !pending.expires.After(now) {
			delete(a.oauth, key)
		}
	}
	for key, token := range a.users {
		if !token.expiresAt.After(now) {
			delete(a.users, key)
		}
	}
}

func (a *githubConnectionAdmin) beginOAuth(ctx context.Context, db *mongo.Database, company string, op githubConnectionOperation) (githubConnectionStatus, error) {
	out := githubOperationStatus(company, op)
	if op.SetupInstallationID <= 0 || (op.State != githubConnectionPending && op.State != githubConnectionVerified && op.State != githubConnectionAwaitingRepositoryAccess) {
		return out, ErrConflict
	}
	now := a.app.now().UTC()
	raw, err := githubStateValue(company, op.ProjectID, op.OperationID)
	if err != nil {
		return out, err
	}
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return out, ErrUnavailable
	}
	verifier := base64.RawURLEncoding.EncodeToString(random[:])
	digest := sourceHash([]byte(raw))
	expiry := now.Add(githubConnectionIntentLifetime)
	if op.ExpiresAt != nil {
		if !now.Before(*op.ExpiresAt) {
			return out, ErrConflict
		}
		if op.ExpiresAt.Before(expiry) {
			expiry = *op.ExpiresAt
		}
	}
	key := githubAdminKey(company, op)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.pruneExpiredLocked(now)
	if _, ok := a.oauth[key]; !ok && len(a.oauth) >= 1024 {
		return out, ErrUnavailable
	}
	filter := githubOperatorFilter(op.ProjectID, githubOperator{op.OperatorIssuer, op.OperatorSubject}, op.OperationID)
	filter["state"], filter["setup_installation_id"] = op.State, op.SetupInstallationID
	if op.OAuthNonce == nil {
		filter["oauth_nonce"] = bson.M{"$exists": false}
	} else {
		filter["oauth_nonce"] = op.OAuthNonce
	}
	update := bson.M{"$set": bson.M{"oauth_nonce": githubConnectionNonce{Digest: digest, ExpiresAt: expiry}, "updated_at": now}}
	if err := githubAdminUpdate(ctx, db, filter, update); err != nil {
		return out, err
	}
	a.oauth[key] = githubEphemeralOAuth{verifier: verifier, digest: digest, expires: expiry}
	out.AuthorizationURL = a.app.authorizationURL(raw, verifier)
	return out, nil
}

func (a *githubConnectionAdmin) oauthCallback(ctx context.Context, operator githubOperator, raw, code string) (githubConnectionStatus, error) {
	zero := githubConnectionStatus{}
	state, err := parseGitHubState(raw)
	if err != nil || !validSummaryField(code, 512) {
		return zero, ErrInvalid
	}
	db, op, err := a.load(ctx, state.CompanyID, state.ProjectID, operator, state.OperationID)
	if err != nil {
		return zero, err
	}
	now := a.app.now().UTC()
	if op.SetupInstallationID <= 0 || op.OAuthNonce == nil || !githubNonceUsable(*op.OAuthNonce, raw, now) ||
		(op.State != githubConnectionPending && op.State != githubConnectionVerified && op.State != githubConnectionAwaitingRepositoryAccess) {
		return zero, ErrConflict
	}
	key := githubAdminKey(state.CompanyID, op)
	a.mu.Lock()
	a.pruneExpiredLocked(now)
	temp, ok := a.oauth[key]
	a.mu.Unlock()
	if !ok || temp.digest != op.OAuthNonce.Digest {
		return zero, todoTeach("github_oauth", "resume authorization to generate fresh OAuth state", ErrConflict)
	}
	filter := githubOperatorFilter(op.ProjectID, operator, op.OperationID)
	filter["state"], filter["oauth_nonce.digest"] = op.State, op.OAuthNonce.Digest
	filter["oauth_nonce.consumed_at"] = bson.M{"$exists": false}
	filter["oauth_nonce.expires_at"] = bson.M{"$gt": now}
	if err := githubAdminUpdate(ctx, db, filter, bson.M{"$set": bson.M{"oauth_nonce.consumed_at": now, "updated_at": now}}); err != nil {
		return zero, err
	}
	a.mu.Lock()
	if current, ok := a.oauth[key]; ok && current.digest == temp.digest {
		delete(a.oauth, key)
	}
	a.mu.Unlock()

	token, err := a.app.exchangeUserCode(ctx, code, temp.verifier)
	if err != nil {
		return zero, githubAdminProviderError(err)
	}
	user, err := a.app.user(ctx, token)
	if err != nil {
		return zero, githubAdminProviderError(err)
	}
	if op.GitHubOperatorID != 0 && op.GitHubOperatorID != user.ID {
		return zero, todoTeach("github_operator_id", "reauthorize using the original GitHub operator", ErrConflict)
	}
	installation, err := a.app.userInstallation(ctx, token, op.SetupInstallationID, op.TargetAccountID)
	if err != nil {
		return zero, githubAdminProviderError(err)
	}
	delete(filter, "oauth_nonce.consumed_at")
	filter["oauth_nonce.consumed_at"] = now
	next := op.State
	if next == githubConnectionPending {
		next = githubConnectionVerified
	}
	update := bson.M{"$set": bson.M{"state": next, "github_operator_id": user.ID, "verified_installation_id": installation.ID, "github_account_login": installation.Account.Login, "updated_at": a.app.now().UTC()}}
	if err := githubAdminUpdate(ctx, db, filter, update); err != nil {
		return zero, err
	}
	now = a.app.now().UTC()
	// The process holds the App user credential only for the administrative
	// session. A restarted process must authorize the original operator again.
	if limit := now.Add(githubConnectionIntentLifetime); token.expiresAt.After(limit) {
		token.expiresAt = limit
	}
	a.mu.Lock()
	a.pruneExpiredLocked(now)
	if len(a.users) < 1024 {
		a.users[key] = token
	}
	a.mu.Unlock()
	op.State, op.GitHubOperatorID, op.VerifiedInstallationID = next, user.ID, installation.ID
	op.GitHubAccountLogin = installation.Account.Login
	return githubOperationStatus(state.CompanyID, op), nil
}

func githubAdminProviderError(err error) error {
	if errors.Is(err, errGitHubAccessDenied) {
		return todoTeach("github_access", "reconnect or request access to the exact GitHub installation and repository", ErrConflict)
	}
	if errors.Is(err, ErrInvalid) {
		return ErrInvalid
	}
	return todoTeach("github_access", "retry when GitHub is available", ErrUnavailable)
}

func (a *githubConnectionAdmin) userToken(company string, op githubConnectionOperation) (githubAppToken, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.pruneExpiredLocked(a.app.now())
	token, ok := a.users[githubAdminKey(company, op)]
	return token, ok && a.app.validUserToken(token)
}

func (a *githubConnectionAdmin) forgetUserToken(company string, op githubConnectionOperation) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.users, githubAdminKey(company, op))
}

func (a *githubConnectionAdmin) repositories(ctx context.Context, company, project string, operator githubOperator, operation string, page int) (githubConnectionRepositoryPage, error) {
	out := githubConnectionRepositoryPage{Repositories: []githubConnectionRepository{}}
	db, op, err := a.load(ctx, company, project, operator, operation)
	if err != nil {
		return out, err
	}
	if op.State != githubConnectionVerified || op.Action != "connect" || page < 1 {
		return out, ErrConflict
	}
	token, ok := a.userToken(company, op)
	if !ok {
		return out, todoTeach("github_oauth", "resume authorization before listing repositories", ErrConflict)
	}
	repositories, more, err := a.app.userInstallationRepositoryPage(ctx, token, op.VerifiedInstallationID, page)
	if err != nil {
		return out, githubAdminProviderError(err)
	}
	selected := make([]githubAppRepository, 0, len(repositories))
	ids := make([]int64, 0, len(repositories))
	for _, repo := range repositories {
		if repo.validIdentity() && repo.Owner.ID == op.TargetAccountID {
			selected = append(selected, repo)
			ids = append(ids, repo.ID)
		}
	}
	byID := make(map[int64]githubRepositoryConnection, len(selected))
	if len(ids) > 0 {
		cursor, err := db.Collection(githubRepositoryConnectionsCollection).Find(ctx, bson.M{"project_id": project, "github_repository_id": bson.M{"$in": ids}})
		if err != nil {
			return out, normalizeMongoError(err)
		}
		defer cursor.Close(ctx)
		for cursor.Next(ctx) {
			var conn githubRepositoryConnection
			if cursor.Decode(&conn) != nil {
				return out, ErrUnavailable
			}
			byID[conn.GitHubRepositoryID] = conn
		}
		if err := cursor.Err(); err != nil {
			return out, normalizeMongoError(err)
		}
	}
	for _, repo := range selected {
		var revision int64
		if conn := byID[repo.ID]; conn.RepositoryNodeID == repo.NodeID && conn.AccountID == repo.Owner.ID {
			revision = conn.Revision
		}
		out.Repositories = append(out.Repositories, githubConnectionRepository{
			ID: repo.ID, FullName: repo.FullName, NodeID: repo.NodeID, Branch: repo.DefaultBranch, ConnectionRevision: revision,
		})
	}
	out.HasNext = more
	return out, nil
}
