package registry

import (
	"context"
	"errors"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

func (a *githubConnectionAdmin) openDiscovery(ctx context.Context, company, project string, operator githubOperator, id string, allowOAuth bool) (githubConnectionStatus, error) {
	var zero githubConnectionStatus
	if !validID(id) {
		return zero, ErrInvalid
	}
	db, op, err := a.load(ctx, company, project, operator, id)
	if err == nil {
		if op.State == githubConnectionAwaitingOrganizationApproval || !a.discoveryActive(op) {
			return zero, ErrConflict
		}
		if _, ok := a.userToken(company, op); ok && (op.State != githubConnectionPending || op.SetupInstallationID == 0) {
			status, err := a.discoveryStatus(ctx, company, op)
			if err != nil || !allowOAuth || status.State != githubConnectionPending || status.Installations != nil {
				return status, err
			}
		}
	} else {
		if db == nil || !errors.Is(err, ErrNotFound) {
			return zero, err
		}
		now := a.app.now().UTC()
		expiry := now.Add(githubConnectionIntentLifetime)
		op = githubConnectionOperation{ProjectID: project, OperationID: id, OperatorIssuer: operator.Issuer, OperatorSubject: operator.Subject, Action: "discover", StartDigest: githubRequestDigest(id), ExpiresAt: &expiry, State: githubConnectionPending, CreatedAt: now, UpdatedAt: now}
		scope := githubUserScope{company, operator.Issuer, operator.Subject}
		a.mu.Lock()
		a.pruneExpiredLocked(now)
		authorization, found := a.authorizations[scope]
		a.mu.Unlock()
		var choices []githubInstallationChoice
		if found && a.app.validUserToken(authorization.token) {
			var listErr error
			choices, listErr = a.installationChoices(ctx, authorization.token)
			if errors.Is(listErr, errGitHubAccessDenied) {
				a.forgetRevokedDiscoveryToken(company, op, authorization.token)
			} else if listErr != nil {
				return zero, githubAdminProviderError(listErr)
			} else if a.app.validUserToken(authorization.token) {
				op.GitHubOperatorID = authorization.githubUserID
			}
		}
		if op.GitHubOperatorID == 0 && !allowOAuth {
			return githubOperationStatus(company, op), nil
		}
		if _, err := db.Collection(githubConnectionOperationsCollection).InsertOne(ctx, op); err != nil {
			return zero, normalizeMongoError(err)
		}
		if op.GitHubOperatorID > 0 {
			a.mu.Lock()
			a.pruneExpiredLocked(a.app.now())
			if len(a.users) >= 1024 {
				a.mu.Unlock()
				return zero, ErrUnavailable
			}
			a.users[githubAdminKey(company, op)] = authorization.token
			a.mu.Unlock()
			out := githubOperationStatus(company, op)
			out.Installations = &choices
			return out, nil
		}
	}
	if !allowOAuth {
		return githubOperationStatus(company, op), nil
	}
	return a.beginOAuth(ctx, db, company, op)
}

func (a *githubConnectionAdmin) discoveryActive(op githubConnectionOperation) bool {
	return op.Action == "discover" && op.DiscoveryBindingOperation == "" && op.ExpiresAt != nil && a.app.now().Before(*op.ExpiresAt)
}

func selectableGitHubInstallation(installation githubAppInstallation) bool {
	return validSummaryField(installation.Account.Login, 128) &&
		(installation.Account.Type == "Organization" || installation.Account.Type == "User")
}

func (a *githubConnectionAdmin) discoveryStatus(ctx context.Context, company string, op githubConnectionOperation) (githubConnectionStatus, error) {
	out := githubOperationStatus(company, op)
	if op.State == githubConnectionAwaitingOrganizationApproval {
		if op.ExpiresAt == nil || !a.app.now().Before(*op.ExpiresAt) {
			return githubConnectionStatus{}, ErrNotFound
		}
		return out, nil
	}
	if op.Action != "discover" {
		return out, nil
	}
	token, ok := a.userToken(company, op)
	out.DiscoveryExpired = !a.discoveryActive(op)
	if out.DiscoveryExpired || !ok {
		out.State = githubConnectionPending
		out.GitHubAccountLogin, out.GitHubAccountType, out.SettingsURL = "", "", ""
		return out, nil
	}
	if op.State != githubConnectionPending || op.GitHubOperatorID <= 0 || op.SetupInstallationID > 0 {
		return out, nil
	}
	choices, err := a.installationChoices(ctx, token)
	if errors.Is(err, errGitHubAccessDenied) {
		a.forgetRevokedDiscoveryToken(company, op, token)
		return out, nil
	}
	if err != nil {
		return out, githubAdminProviderError(err)
	}
	out.Installations = &choices
	return out, nil
}

// Discard the denied credential without affecting a newer OAuth result.
func (a *githubConnectionAdmin) forgetRevokedDiscoveryToken(company string, op githubConnectionOperation, denied githubAppToken) {
	key := githubAdminKey(company, op)
	scope := githubUserScope{company, op.OperatorIssuer, op.OperatorSubject}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.users[key] == denied {
		delete(a.users, key)
	}
	if a.authorizations[scope].token == denied {
		delete(a.authorizations, scope)
	}
}

func (a *githubConnectionAdmin) installationChoices(ctx context.Context, token githubAppToken) ([]githubInstallationChoice, error) {
	choices := []githubInstallationChoice{}
	err := a.app.userInstallations(ctx, token, func(installation githubAppInstallation) bool {
		if selectableGitHubInstallation(installation) {
			choices = append(choices, githubInstallationChoice{ID: installation.ID, Login: installation.Account.Login, Type: installation.Account.Type, SettingsURL: githubInstallationSettingsURL(installation.ID, installation.Account.Login, installation.Account.Type)})
		}
		return false
	})
	return choices, err
}

func discoverySelectionFilter(op githubConnectionOperation, now time.Time) bson.M {
	filter := githubOperatorFilter(op.ProjectID, githubOperator{op.OperatorIssuer, op.OperatorSubject}, op.OperationID)
	filter["state"] = githubConnectionPending
	filter["github_operator_id"] = op.GitHubOperatorID
	filter["setup_installation_id"] = bson.M{"$exists": false}
	filter["expires_at"] = bson.M{"$gt": now}
	filter["oauth_nonce"] = op.OAuthNonce
	return filter
}

func (a *githubConnectionAdmin) installDiscovery(ctx context.Context, company, project string, operator githubOperator, id string) (githubConnectionStatus, error) {
	db, op, err := a.load(ctx, company, project, operator, id)
	if err != nil {
		return githubConnectionStatus{}, err
	}
	out := githubOperationStatus(company, op)
	if !a.discoveryActive(op) || op.State != githubConnectionPending || op.GitHubOperatorID <= 0 || op.SetupInstallationID != 0 {
		return out, ErrConflict
	}
	if _, ok := a.userToken(company, op); !ok {
		return out, todoTeach("github_oauth", "reauthorize before installing the GitHub App", ErrConflict)
	}
	now := a.app.now().UTC()
	if op.SetupNonce.Digest != "" && now.Before(op.SetupNonce.ExpiresAt) {
		return out, githubInstallationLinkAlreadyIssued()
	}
	raw, err := githubStateValue(company, project, id)
	if err != nil {
		return out, err
	}
	filter := discoverySelectionFilter(op, now)
	filter["$or"] = bson.A{
		bson.M{"setup_nonce.digest": nil},
		bson.M{"setup_nonce.digest": ""},
		bson.M{"setup_nonce.expires_at": bson.M{"$lte": now}},
	}
	nonce := githubConnectionNonce{Digest: sourceHash([]byte(raw)), ExpiresAt: *op.ExpiresAt}
	if err := githubAdminUpdate(ctx, db, filter, bson.M{"$set": bson.M{"setup_nonce": nonce, "updated_at": now}}); err != nil {
		if errors.Is(err, ErrConflict) {
			return out, githubInstallationLinkAlreadyIssued()
		}
		return out, err
	}
	out.AuthorizationURL = a.app.setupURL(raw)
	return out, nil
}

func githubInstallationLinkAlreadyIssued() error {
	return todoTeach("setup_nonce", "use the open GitHub installation link or refresh available accounts to start a new attempt", ErrConflict)
}

func (a *githubConnectionAdmin) selectDiscoveryInstallation(ctx context.Context, company, project string, operator githubOperator, id string, installationID int64) (githubConnectionStatus, error) {
	db, op, err := a.load(ctx, company, project, operator, id)
	if err != nil {
		return githubConnectionStatus{}, err
	}
	out := githubOperationStatus(company, op)
	if installationID <= 0 {
		return out, ErrInvalid
	}
	if !a.discoveryActive(op) || op.GitHubOperatorID <= 0 {
		return out, ErrConflict
	}
	if op.State == githubConnectionVerified && op.VerifiedInstallationID == installationID {
		return out, nil
	}
	if op.State != githubConnectionPending || op.SetupInstallationID != 0 {
		return out, ErrConflict
	}
	token, ok := a.userToken(company, op)
	if !ok {
		return out, todoTeach("github_oauth", "reauthorize before selecting an installation", ErrConflict)
	}
	installation, err := a.app.userInstallation(ctx, token, installationID, 0)
	if err != nil {
		return out, githubAdminProviderError(err)
	}
	if !selectableGitHubInstallation(installation) {
		return out, githubAdminProviderError(errGitHubAccessDenied)
	}
	now := a.app.now().UTC()
	fields := bson.M{"state": githubConnectionVerified, "setup_installation_id": installation.ID, "verified_installation_id": installation.ID,
		"github_account_login": installation.Account.Login, "github_account_type": installation.Account.Type, "target_account_id": installation.Account.ID, "updated_at": now}
	if err := githubAdminUpdate(ctx, db, discoverySelectionFilter(op, now), bson.M{"$set": fields}); err != nil {
		return out, err
	}
	op.State, op.VerifiedInstallationID = githubConnectionVerified, installation.ID
	op.GitHubAccountLogin, op.GitHubAccountType = installation.Account.Login, installation.Account.Type
	return githubOperationStatus(company, op), nil
}

func (a *githubConnectionAdmin) startDiscovered(ctx context.Context, db *mongo.Database, company, project string, operator githubOperator, req githubConnectionStartRequest, digest string, doc projectDocument) (githubConnectionStatus, error) {
	var zero githubConnectionStatus
	_, discovery, err := a.load(ctx, company, project, operator, req.DiscoveryOperation)
	if errors.Is(err, ErrNotFound) && req.allowCompanyDiscovery && doc.CreatedBy.permits(operator) && doc.CreatedBy.DiscoveryOperation == req.DiscoveryOperation {
		_, discovery, err = a.load(ctx, company, "", operator, req.DiscoveryOperation)
	}
	if err != nil {
		return zero, err
	}
	now := a.app.now().UTC()
	if discovery.Action != "discover" || discovery.State != githubConnectionVerified || discovery.TargetAccountID <= 0 || discovery.ExpiresAt == nil || !now.Before(*discovery.ExpiresAt) {
		return zero, ErrConflict
	}
	token, ok := a.userToken(company, discovery)
	if !ok {
		return zero, todoTeach("github_oauth", "resume the GitHub connection before confirming the repository", ErrConflict)
	}
	op := githubConnectionOperation{
		ProjectID: project, OperationID: req.OperationID, OperatorIssuer: operator.Issuer, OperatorSubject: operator.Subject,
		Role: req.Role, Action: req.Action, StartDigest: digest, ExpectedRoleRevision: *req.ExpectedRoleRevision, ExpectedConnectionRevision: *req.ExpectedConnectionRevision,
		TargetAccountID: discovery.TargetAccountID, TargetRepositoryName: req.TargetRepositoryName, TargetRepositoryNameNormalized: strings.ToLower(req.TargetRepositoryName), PublicationBranch: req.PublicationBranch, Visibility: req.Visibility,
		SetupInstallationID: discovery.SetupInstallationID, VerifiedInstallationID: discovery.VerifiedInstallationID, GitHubOperatorID: discovery.GitHubOperatorID, GitHubAccountLogin: discovery.GitHubAccountLogin, GitHubAccountType: discovery.GitHubAccountType,
		State: githubConnectionVerified, CreatedAt: now, UpdatedAt: now, ExpiresAt: discovery.ExpiresAt,
	}
	session, err := a.store.client.StartSession()
	if err != nil {
		return zero, normalizeMongoError(err)
	}
	defer session.EndSession(ctx)
	_, err = session.WithTransaction(ctx, func(tx context.Context) (any, error) {
		if discovery.DiscoveryBindingOperation != "" {
			return nil, ErrConflict
		}
		filter := githubOperatorFilter(discovery.ProjectID, operator, discovery.OperationID)
		filter["state"] = githubConnectionVerified
		filter["discovery_binding_operation"] = bson.M{"$exists": false}
		filter["expires_at"] = bson.M{"$gt": now}
		filter["verified_installation_id"] = discovery.VerifiedInstallationID
		filter["github_operator_id"] = discovery.GitHubOperatorID
		if err := githubAdminUpdate(tx, db, filter, bson.M{"$set": bson.M{"discovery_project": project, "discovery_binding_operation": req.OperationID, "updated_at": now}}); err != nil {
			return nil, err
		}
		_, err := db.Collection(githubConnectionOperationsCollection).InsertOne(tx, op)
		return nil, err
	})
	if err != nil {
		return zero, normalizeMongoError(err)
	}
	a.mu.Lock()
	a.pruneExpiredLocked(now)
	delete(a.users, githubAdminKey(company, discovery))
	a.users[githubAdminKey(company, op)] = token
	a.mu.Unlock()
	return githubOperationStatus(company, op), nil
}
