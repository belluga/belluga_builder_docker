package registry

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

const (
	githubRepositoryConnectionsCollection = "project_github_repository_connections"
	githubConnectionOperationsCollection  = "project_github_connection_operations"
)

type githubRepositoryConnection struct {
	ProjectID          string    `bson:"project_id"`
	GitHubRepositoryID int64     `bson:"github_repository_id"`
	RepositoryNodeID   string    `bson:"repository_node_id"`
	RepositoryFullName string    `bson:"repository_full_name"`
	InstallationID     int64     `bson:"installation_id"`
	AccountID          int64     `bson:"account_id"`
	Revision           int64     `bson:"revision"`
	CreatedAt          time.Time `bson:"created_at"`
	UpdatedAt          time.Time `bson:"updated_at"`
}

type githubConnectionNonce struct {
	Digest     string     `bson:"digest"`
	ExpiresAt  time.Time  `bson:"expires_at"`
	ConsumedAt *time.Time `bson:"consumed_at,omitempty"`
}

type githubConnectionState string

const (
	githubConnectionPending                  githubConnectionState = "pending"
	githubConnectionVerified                 githubConnectionState = "verified"
	githubConnectionCreating                 githubConnectionState = "creating"
	githubConnectionUncertain                githubConnectionState = "uncertain"
	githubConnectionAwaitingRepositoryAccess githubConnectionState = "awaiting_repository_access"
	githubConnectionCompleted                githubConnectionState = "completed"
	githubConnectionFailed                   githubConnectionState = "failed"
)

const githubConnectionAwaitingOrganizationApproval githubConnectionState = "awaiting_organization_approval"

type githubConnectionOperation struct {
	InstallationRequestedAt        *time.Time             `bson:"installation_requested_at,omitempty"`
	ProjectID                      string                 `bson:"project_id"`
	OperationID                    string                 `bson:"operation_id"`
	OperatorIssuer                 string                 `bson:"operator_issuer"`
	OperatorSubject                string                 `bson:"operator_subject"`
	Role                           string                 `bson:"role"`
	Action                         string                 `bson:"action"`
	StartDigest                    string                 `bson:"start_digest"`
	ExpectedRoleRevision           int64                  `bson:"expected_role_revision"`
	ExpectedConnectionRevision     int64                  `bson:"expected_connection_revision"`
	TargetAccountID                int64                  `bson:"target_account_id"`
	TargetRepositoryName           string                 `bson:"target_repository_name,omitempty"`
	TargetRepositoryNameNormalized string                 `bson:"target_repository_name_normalized,omitempty"`
	PublicationBranch              string                 `bson:"publication_branch,omitempty"`
	Visibility                     string                 `bson:"visibility,omitempty"`
	ResultRepositoryVisibility     string                 `bson:"result_repository_visibility,omitempty"`
	SetupNonce                     githubConnectionNonce  `bson:"setup_nonce"`
	OAuthNonce                     *githubConnectionNonce `bson:"oauth_nonce,omitempty"`
	ExpiresAt                      *time.Time             `bson:"expires_at,omitempty"`
	SetupInstallationID            int64                  `bson:"setup_installation_id,omitempty"`
	VerifiedInstallationID         int64                  `bson:"verified_installation_id,omitempty"`
	GitHubOperatorID               int64                  `bson:"github_operator_id,omitempty"`
	GitHubAccountType              string                 `bson:"github_account_type,omitempty"`
	DiscoveryProject               string                 `bson:"discovery_project,omitempty"`
	DiscoveryBindingOperation      string                 `bson:"discovery_binding_operation,omitempty"`
	GitHubAccountLogin             string                 `bson:"github_account_login,omitempty"`
	FinalizeDigest                 string                 `bson:"finalize_digest,omitempty"`
	State                          githubConnectionState  `bson:"state"`
	CreateReserved                 bool                   `bson:"create_reserved,omitempty"`
	ResultRepositoryID             int64                  `bson:"result_repository_id,omitempty"`
	ResultRepositoryNodeID         string                 `bson:"result_repository_node_id,omitempty"`
	ResultRepositoryFullName       string                 `bson:"result_repository_full_name,omitempty"`
	ResultRepositoryDefaultBranch  string                 `bson:"result_repository_default_branch,omitempty"`
	CreatedAt                      time.Time              `bson:"created_at"`
	UpdatedAt                      time.Time              `bson:"updated_at"`
}

type githubConnectionIndex struct {
	collection    string
	name          string
	keys          bson.D
	unique        bool
	expirySeconds *int32
	partial       []bson.M
}

// Frozen v4 index definitions, shared by apply and verification.
func githubConnectionIndexes() []githubConnectionIndex {
	immediate := int32(0)
	return []githubConnectionIndex{
		{collection: githubRepositoryConnectionsCollection, name: "project_github_repository_unique",
			keys: bson.D{{Key: "project_id", Value: 1}, {Key: "github_repository_id", Value: 1}}, unique: true},
		{collection: githubRepositoryConnectionsCollection, name: "project_github_repository_node",
			keys: bson.D{{Key: "project_id", Value: 1}, {Key: "repository_node_id", Value: 1}}},
		{collection: githubConnectionOperationsCollection, name: "project_github_operation_unique",
			keys: bson.D{{Key: "project_id", Value: 1}, {Key: "operation_id", Value: 1}}, unique: true},
		{collection: githubConnectionOperationsCollection, name: "github_intent_expiry",
			keys: bson.D{{Key: "expires_at", Value: 1}}, expirySeconds: &immediate},
		{collection: githubConnectionOperationsCollection, name: "github_create_target_reserved",
			keys:   bson.D{{Key: "target_account_id", Value: 1}, {Key: "target_repository_name_normalized", Value: 1}},
			unique: true, partial: []bson.M{{"create_reserved": true}}},
	}
}

func pendingGitHubInstallationIndex() githubConnectionIndex {
	return githubConnectionIndex{
		collection: githubConnectionOperationsCollection, name: "github_pending_installation_by_operator",
		keys:    bson.D{{Key: "project_id", Value: 1}, {Key: "operator_issuer", Value: 1}, {Key: "operator_subject", Value: 1}, {Key: "state", Value: 1}, {Key: "installation_requested_at", Value: -1}, {Key: "operation_id", Value: -1}},
		partial: []bson.M{{"state": githubConnectionAwaitingOrganizationApproval}},
	}
}

func ensureGitHubConnectionIndexes(ctx context.Context, database *mongo.Database) error {
	return ensureGitHubIndexes(ctx, database, githubConnectionIndexes())
}

func ensureGitHubIndexes(ctx context.Context, database *mongo.Database, indexes []githubConnectionIndex) error {
	for _, index := range indexes {
		builder := options.Index().SetName(index.name)
		if index.unique {
			builder.SetUnique(true)
		}
		if index.expirySeconds != nil {
			builder.SetExpireAfterSeconds(*index.expirySeconds)
		}
		if len(index.partial) != 0 {
			builder.SetPartialFilterExpression(index.partial[0])
		}
		if _, err := database.Collection(index.collection).Indexes().CreateOne(ctx, mongo.IndexModel{Keys: index.keys, Options: builder}); err != nil {
			return err
		}
	}
	return nil
}

func verifyGitHubConnectionIndexes(ctx context.Context, database *mongo.Database) error {
	for _, index := range githubConnectionIndexes() {
		if err := verifyCompanyIndexOptions(ctx, database.Collection(index.collection), index.name, index.keys, index.unique, index.expirySeconds, index.partial...); err != nil {
			return err
		}
	}
	return nil
}

// claimGitHubRepositoryCreation must run before the external POST. The
// verified→creating transition is a single write and also reserves the target
// through the partial unique index. A caller finding creating after a crash
// must reconcile; it never receives a second dispatch claim.
func claimGitHubRepositoryCreation(ctx context.Context, db *mongo.Database, op githubConnectionOperation, finalizeDigest string, now time.Time) error {
	if op.Action != "create" || op.TargetAccountID <= 0 || op.TargetRepositoryNameNormalized == "" ||
		op.VerifiedInstallationID <= 0 || op.GitHubOperatorID <= 0 || !validSourceDigest(finalizeDigest) {
		return ErrInvalid
	}
	filter := bson.M{
		"project_id": op.ProjectID, "operation_id": op.OperationID,
		"operator_issuer": op.OperatorIssuer, "operator_subject": op.OperatorSubject,
		"state": githubConnectionVerified, "action": "create", "start_digest": op.StartDigest,
		"role": op.Role, "expected_role_revision": op.ExpectedRoleRevision,
		"expected_connection_revision": op.ExpectedConnectionRevision,
		"target_account_id":            op.TargetAccountID, "target_repository_name_normalized": op.TargetRepositoryNameNormalized,
		"verified_installation_id": op.VerifiedInstallationID, "github_operator_id": op.GitHubOperatorID,
		"expires_at": bson.M{"$gt": now}, "finalize_digest": bson.M{"$exists": false},
	}
	update := bson.M{
		"$set":   bson.M{"state": githubConnectionCreating, "create_reserved": true, "finalize_digest": finalizeDigest, "updated_at": now},
		"$unset": bson.M{"expires_at": ""},
	}
	res, err := db.Collection(githubConnectionOperationsCollection).UpdateOne(ctx, filter, update)
	if mongo.IsDuplicateKeyError(err) {
		return ErrConflict
	}
	if err != nil {
		return normalizeMongoError(err)
	}
	if res.MatchedCount != 1 {
		return ErrConflict
	}
	return nil
}

// writeGitHubRepositoryConnectionCAS is used inside the transaction that also
// applies the existing role's CAS and records operation completion. This write
// grants no role. It refuses to replace the GitHub node identity for a numeric ID.
func writeGitHubRepositoryConnectionCAS(ctx context.Context, db *mongo.Database, connection githubRepositoryConnection, expectedRevision int64, now time.Time) error {
	if !validID(connection.ProjectID) || connection.GitHubRepositoryID <= 0 ||
		connection.RepositoryNodeID == "" || len(connection.RepositoryNodeID) > 128 ||
		!validGitRepositoryID(connection.RepositoryFullName) || connection.InstallationID <= 0 || connection.AccountID <= 0 || expectedRevision < 0 {
		return ErrInvalid
	}
	collection := db.Collection(githubRepositoryConnectionsCollection)
	if expectedRevision == 0 {
		connection.Revision = 1
		connection.CreatedAt, connection.UpdatedAt = now, now
		_, err := collection.InsertOne(ctx, connection)
		if err == nil {
			return nil
		}
		if mongo.IsDuplicateKeyError(err) {
			return ErrConflict
		}
		return normalizeMongoError(err)
	}
	filter := bson.M{"project_id": connection.ProjectID, "github_repository_id": connection.GitHubRepositoryID,
		"repository_node_id": connection.RepositoryNodeID, "account_id": connection.AccountID, "revision": expectedRevision}
	update := bson.M{"$set": bson.M{"repository_full_name": connection.RepositoryFullName,
		"installation_id": connection.InstallationID, "updated_at": now}, "$inc": bson.M{"revision": int64(1)}}
	res, err := collection.UpdateOne(ctx, filter, update)
	if err != nil {
		return normalizeMongoError(err)
	}
	if res.MatchedCount != 1 {
		return ErrConflict
	}
	return nil
}
