package registry

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

const githubConnectionIntentLifetime = 15 * time.Minute

type githubOperator struct {
	Issuer  string
	Subject string
}

type githubConnectionStartRequest struct {
	InstallationID             int64  `json:"installation_id,omitempty"`
	OperationID                string `json:"operation_id"`
	Role                       string `json:"role"`
	Action                     string `json:"action"`
	ExpectedRoleRevision       *int64 `json:"expected_role_revision"`
	ExpectedConnectionRevision *int64 `json:"expected_connection_revision"`
	TargetAccountID            int64  `json:"target_account_id"`
	TargetRepositoryName       string `json:"target_repository_name,omitempty"`
	PublicationBranch          string `json:"publication_branch,omitempty"`
	DiscoveryOperation         string `json:"discovery_operation_id,omitempty"`
	Visibility                 string `json:"visibility,omitempty"`
	allowCompanyDiscovery      bool
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

type githubInstallationChoice struct {
	ID          int64  `json:"installation_id"`
	Login       string `json:"login"`
	Type        string `json:"type"`
	SettingsURL string `json:"settings_url,omitempty"`
}

type githubConnectionStatus struct {
	InstallationRequestedAt *time.Time                  `json:"installation_requested_at,omitempty"`
	ExpiresAt               *time.Time                  `json:"expires_at,omitempty"`
	DiscoveryExpired        bool                        `json:"discovery_expired,omitempty"`
	Installations           *[]githubInstallationChoice `json:"installations,omitempty"`
	State                   githubConnectionState       `json:"state"`
	OperationID             string                      `json:"operation_id"`
	CompanyID               string                      `json:"company_id"`
	ProjectID               string                      `json:"project_id"`
	SettingsURL             string                      `json:"settings_url,omitempty"`
	AuthorizationURL        string                      `json:"authorization_url,omitempty"`
	GitHubAccountLogin      string                      `json:"github_account_login,omitempty"`
	GitHubAccountType       string                      `json:"github_account_type,omitempty"`
	Repository              *githubConnectionRepository `json:"repository,omitempty"`
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

type githubUserScope struct {
	company, issuer, subject string
}

type githubUserAuthorization struct {
	token        githubAppToken
	githubUserID int64
}

type githubConnectionAdmin struct {
	store          *MongoStore
	app            *githubAppClient
	mu             sync.Mutex
	oauth          map[githubOperationKey]githubEphemeralOAuth
	users          map[githubOperationKey]githubAppToken
	authorizations map[githubUserScope]githubUserAuthorization
}

func newGitHubConnectionAdmin(store *MongoStore, app *githubAppClient) *githubConnectionAdmin {
	return &githubConnectionAdmin{store: store, app: app, oauth: make(map[githubOperationKey]githubEphemeralOAuth), users: make(map[githubOperationKey]githubAppToken), authorizations: make(map[githubUserScope]githubUserAuthorization)}
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
	if err != nil || json.Unmarshal(decoded, &state) != nil || !validID(state.CompanyID) || (state.ProjectID != "" && !validID(state.ProjectID)) || !validID(state.OperationID) {
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

var githubInstallationLogin = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,37}[A-Za-z0-9])?$`)

func githubInstallationSettingsURL(id int64, login, accountType string) string {
	if id <= 0 || !githubInstallationLogin.MatchString(login) {
		return ""
	}
	const origin = "https://github.com"
	suffix := "/settings/installations/" + strconv.FormatInt(id, 10)
	switch accountType {
	case "User":
		return origin + suffix
	case "Organization":
		return origin + "/organizations/" + login + suffix
	default:
		return ""
	}
}

func githubOperationStatus(company string, op githubConnectionOperation) githubConnectionStatus {
	out := githubConnectionStatus{CompanyID: company, ProjectID: op.ProjectID, OperationID: op.OperationID, State: op.State, GitHubAccountLogin: op.GitHubAccountLogin, GitHubAccountType: op.GitHubAccountType}
	if op.State == githubConnectionAwaitingOrganizationApproval {
		out.InstallationRequestedAt, out.ExpiresAt = op.InstallationRequestedAt, op.ExpiresAt
		out.GitHubAccountLogin, out.GitHubAccountType = "", ""
		return out
	}
	if op.Action == "discover" && op.State == githubConnectionVerified {
		out.SettingsURL = githubInstallationSettingsURL(op.VerifiedInstallationID, op.GitHubAccountLogin, op.GitHubAccountType)
	}
	if op.ResultRepositoryID > 0 {
		out.Repository = &githubConnectionRepository{ID: op.ResultRepositoryID, NodeID: op.ResultRepositoryNodeID, FullName: op.ResultRepositoryFullName, Branch: op.ResultRepositoryDefaultBranch}
	}
	return out
}

func githubOperatorFilter(project string, operator githubOperator, operation string) bson.M {
	return bson.M{"project_id": project, "operation_id": operation, "operator_issuer": operator.Issuer, "operator_subject": operator.Subject}
}

func (a *githubConnectionAdmin) database(ctx context.Context, company, project string, operator githubOperator) (*mongo.Database, error) {
	if !validID(company) || (project != "" && !validID(project)) || !validSummaryField(operator.Issuer, 256) || !validSummaryField(operator.Subject, 256) {
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
		return db, op, ErrNotFound
	}
	if err != nil {
		return nil, op, normalizeMongoError(err)
	}
	return db, op, nil
}

func (a *githubConnectionAdmin) start(ctx context.Context, company, project string, operator githubOperator, request githubConnectionStartRequest) (githubConnectionStatus, error) {
	var zero githubConnectionStatus
	switch request.Action {
	case "discover":
		return a.openDiscovery(ctx, company, project, operator, request.OperationID, true)
	case "resume-discovery":
		return a.openDiscovery(ctx, company, project, operator, request.OperationID, false)
	case "install-app":
		return a.installDiscovery(ctx, company, project, operator, request.OperationID)
	case "select-installation":
		return a.selectDiscoveryInstallation(ctx, company, project, operator, request.OperationID, request.InstallationID)
	}
	if !validID(project) || !validID(request.OperationID) || request.ExpectedRoleRevision == nil || request.ExpectedConnectionRevision == nil ||
		*request.ExpectedRoleRevision < 0 || *request.ExpectedConnectionRevision < 0 ||
		(request.DiscoveryOperation == "" && request.TargetAccountID <= 0) ||
		(request.DiscoveryOperation != "" && (!validID(request.DiscoveryOperation) || request.TargetAccountID != 0 || request.DiscoveryOperation == request.OperationID)) ||
		(request.Role != "visual" && request.Role != "implementation") ||
		(request.Action != "connect" && request.Action != "create") ||
		(request.Role == "visual" && (request.Action == "connect" && request.DiscoveryOperation == "" || request.PublicationBranch != "") && !validGitBranch(request.PublicationBranch)) ||
		(request.Role == "implementation" && request.PublicationBranch != "") ||
		(request.Action == "create" && !validGitRepositoryID("owner/"+request.TargetRepositoryName)) ||
		(request.Visibility != "" && (request.Action != "create" || request.Visibility != "public" && request.Visibility != "private")) ||
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
	if request.DiscoveryOperation != "" {
		return a.startDiscovered(ctx, db, company, project, operator, request, digest, doc)
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
		PublicationBranch: request.PublicationBranch, Visibility: request.Visibility, State: githubConnectionPending, CreatedAt: now, UpdatedAt: now,
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
	return a.discoveryStatus(ctx, company, op)
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

const githubInstallationRequestRetention = 30 * 24 * time.Hour

func pendingInstallationReceipt(op githubConnectionOperation, digest string, now time.Time) bool {
	return op.State == githubConnectionAwaitingOrganizationApproval && op.InstallationRequestedAt != nil &&
		op.ExpiresAt != nil && now.Before(*op.ExpiresAt) && op.SetupNonce.ConsumedAt != nil && op.SetupNonce.Digest == digest
}

func (a *githubConnectionAdmin) requestInstallationCallback(ctx context.Context, operator githubOperator, raw string) (githubConnectionStatus, error) {
	state, err := parseGitHubState(raw)
	if err != nil {
		return githubConnectionStatus{}, ErrInvalid
	}
	db, op, err := a.load(ctx, state.CompanyID, state.ProjectID, operator, state.OperationID)
	if err != nil {
		return githubConnectionStatus{}, err
	}
	now := a.app.now().UTC().Truncate(time.Millisecond)
	digest := sourceHash([]byte(raw))
	if pendingInstallationReceipt(op, digest, now) {
		return githubOperationStatus(state.CompanyID, op), nil
	}
	if op.State != githubConnectionPending || op.ExpiresAt == nil || !now.Before(*op.ExpiresAt) || !githubNonceUsable(op.SetupNonce, raw, now) {
		return githubConnectionStatus{}, ErrConflict
	}
	filter := githubOperatorFilter(state.ProjectID, operator, state.OperationID)
	filter["state"] = githubConnectionPending
	filter["expires_at"] = bson.M{"$gt": now}
	filter["setup_nonce.digest"] = digest
	filter["setup_nonce.consumed_at"] = bson.M{"$exists": false}
	filter["setup_nonce.expires_at"] = bson.M{"$gt": now}
	expiry := now.Add(githubInstallationRequestRetention)
	update := bson.M{"$set": bson.M{"state": githubConnectionAwaitingOrganizationApproval, "setup_nonce.consumed_at": now, "installation_requested_at": now, "updated_at": now, "expires_at": expiry}}
	if err := githubAdminUpdate(ctx, db, filter, update); err != nil {
		if errors.Is(err, ErrConflict) {
			_, current, readErr := a.load(ctx, state.CompanyID, state.ProjectID, operator, state.OperationID)
			if readErr == nil && pendingInstallationReceipt(current, digest, now) {
				return githubOperationStatus(state.CompanyID, current), nil
			}
		}
		return githubConnectionStatus{}, err
	}
	op.State, op.InstallationRequestedAt, op.ExpiresAt = githubConnectionAwaitingOrganizationApproval, &now, &expiry
	return githubOperationStatus(state.CompanyID, op), nil
}

type githubPendingInstallation struct {
	OperationID             string                `bson:"operation_id" json:"operation_id"`
	State                   githubConnectionState `bson:"state" json:"state"`
	InstallationRequestedAt time.Time             `bson:"installation_requested_at" json:"installation_requested_at"`
	ExpiresAt               time.Time             `bson:"expires_at" json:"expires_at"`
}

type githubPendingInstallationResponse struct {
	Operation *githubPendingInstallation `json:"operation"`
}

func (a *githubConnectionAdmin) pendingInstallation(ctx context.Context, company, project string, operator githubOperator) (githubPendingInstallationResponse, error) {
	db, err := a.database(ctx, company, project, operator)
	if err != nil {
		return githubPendingInstallationResponse{}, err
	}
	filter := bson.M{"project_id": project, "operator_issuer": operator.Issuer, "operator_subject": operator.Subject, "state": githubConnectionAwaitingOrganizationApproval, "expires_at": bson.M{"$gt": a.app.now().UTC()}}
	query := options.FindOne().SetSort(bson.D{{Key: "installation_requested_at", Value: -1}, {Key: "operation_id", Value: -1}}).
		SetProjection(bson.M{"_id": 0, "operation_id": 1, "state": 1, "installation_requested_at": 1, "expires_at": 1})
	var op githubPendingInstallation
	if err := db.Collection(githubConnectionOperationsCollection).FindOne(ctx, filter, query).Decode(&op); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return githubPendingInstallationResponse{}, nil
		}
		return githubPendingInstallationResponse{}, normalizeMongoError(err)
	}
	return githubPendingInstallationResponse{Operation: &op}, nil
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
	for scope, authorization := range a.authorizations {
		if !authorization.token.expiresAt.After(now) {
			delete(a.authorizations, scope)
		}
	}
}

func (a *githubConnectionAdmin) beginOAuth(ctx context.Context, db *mongo.Database, company string, op githubConnectionOperation) (githubConnectionStatus, error) {
	out := githubOperationStatus(company, op)
	if (op.SetupInstallationID <= 0 && op.Action != "discover") || (op.State != githubConnectionPending && op.State != githubConnectionVerified && op.State != githubConnectionAwaitingRepositoryAccess) {
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
	filter["state"] = op.State
	if op.SetupInstallationID == 0 {
		filter["setup_installation_id"] = bson.M{"$exists": false}
	} else {
		filter["setup_installation_id"] = op.SetupInstallationID
	}
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
	if (op.SetupInstallationID <= 0 && op.Action != "discover") || op.OAuthNonce == nil || !githubNonceUsable(*op.OAuthNonce, raw, now) ||
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
	filter["oauth_nonce.consumed_at"] = now
	fields := bson.M{"github_operator_id": user.ID, "updated_at": a.app.now().UTC()}
	op.GitHubOperatorID = user.ID
	if op.SetupInstallationID > 0 {
		installation, err := a.app.userInstallation(ctx, token, op.SetupInstallationID, op.TargetAccountID)
		if err != nil {
			return zero, githubAdminProviderError(err)
		}
		if op.State == githubConnectionPending {
			op.State = githubConnectionVerified
		}
		fields["state"], fields["verified_installation_id"] = op.State, installation.ID
		fields["github_account_login"], fields["github_account_type"], fields["target_account_id"] = installation.Account.Login, installation.Account.Type, installation.Account.ID
		op.VerifiedInstallationID, op.GitHubAccountLogin, op.GitHubAccountType = installation.ID, installation.Account.Login, installation.Account.Type
	}
	update := bson.M{"$set": fields}
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
	scope := githubUserScope{state.CompanyID, operator.Issuer, operator.Subject}
	if _, exists := a.authorizations[scope]; exists || len(a.authorizations) < 1024 {
		a.authorizations[scope] = githubUserAuthorization{token: token, githubUserID: user.ID}
	}
	a.mu.Unlock()
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
	if op.State != githubConnectionVerified || op.Action != "connect" && !a.discoveryActive(op) || page < 1 {
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
	out.HasNext = more && page < 100
	return out, nil
}
