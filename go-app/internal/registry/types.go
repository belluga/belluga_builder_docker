package registry

import (
	"context"
	"encoding/json"
	"errors"

	"go.mongodb.org/mongo-driver/v2/bson"
)

var (
	ErrInvalid     = errors.New("invalid")
	ErrNotFound    = errors.New("not_found")
	ErrConflict    = errors.New("conflict")
	ErrUnavailable = errors.New("unavailable")
)

type Binding struct {
	WorkspaceRoot                  string `json:"workspace_root"`
	ProjectRoot                    string `json:"project_root"`
	FoundationRoot                 string `json:"foundation_root"`
	RepositoryID                   string `json:"repository_id,omitempty"`
	DockerRepositoryID             string `json:"docker_repository_id,omitempty"`
	PublicationBranch              string `json:"publication_branch,omitempty"`
	SourceBindingsPath             string `json:"source_bindings_path,omitempty"`
	SourceBindingsStatePath        string `json:"source_bindings_state_path,omitempty"`
	SourceBindingsDigest           string `json:"source_bindings_digest,omitempty"`
	DefaultOwnerID                 string `json:"default_owner_id,omitempty"`
	LegacyFoundationSourceDisabled bool   `json:"legacy_foundation_source_disabled,omitempty" bson:"legacy_foundation_source_disabled,omitempty"`
}

type Artifact struct {
	ID         string   `json:"id"`
	Kind       string   `json:"kind"`
	Name       string   `json:"name"`
	State      string   `json:"state"`
	Entry      string   `json:"entry_point,omitempty"`
	Files      []string `json:"files,omitempty"`
	Diagnostic string   `json:"diagnostic,omitempty"`
}

type Company struct {
	ObjectID     bson.ObjectID   `json:"-" bson:"_id,omitempty"`
	ID           string          `json:"id" bson:"id"`
	Name         string          `json:"name" bson:"name"`
	DatabaseName string          `json:"database_name,omitempty" bson:"database_name,omitempty"`
	Domains      []string        `json:"domains,omitempty" bson:"domains,omitempty"`
	AppIDs       []AppIdentifier `json:"app_ids,omitempty" bson:"app_ids,omitempty"`
	DomainKeys   []string        `json:"-" bson:"domain_keys,omitempty"`
	AppKeys      []string        `json:"-" bson:"app_keys,omitempty"`
}

type AppIdentifier struct {
	Platform string `json:"platform" bson:"platform"`
	Value    string `json:"value" bson:"value"`
}

type Project struct {
	ID                    string   `json:"id" bson:"id"`
	Name                  string   `json:"name" bson:"name"`
	CompanyID             string   `json:"company_id" bson:"company_id"`
	RoadmapRevision       int64    `json:"roadmap_revision" bson:"roadmap_revision"`
	Binding               *Binding `json:"binding,omitempty" bson:"binding,omitempty"`
	BindingRevision       int64    `json:"binding_revision" bson:"binding_revision"`
	RegisteredAt          string   `json:"registered_at" bson:"registered_at"`
	PreparationState      string   `json:"preparation_state" bson:"preparation_state"`
	PreparationDiagnostic string   `json:"preparation_diagnostic,omitempty" bson:"preparation_diagnostic,omitempty"`
	SnapshotFingerprint   string   `json:"snapshot_fingerprint,omitempty" bson:"snapshot_fingerprint,omitempty"`
	KnowledgeGenerationID string   `json:"knowledge_generation_id,omitempty" bson:"knowledge_generation_id,omitempty"`
}

type Catalog struct {
	SchemaVersion string       `json:"schema_version"`
	Companies     []Company    `json:"companies"`
	Projects      []Project    `json:"projects"`
	Preferences   []Preference `json:"preferences"`
}

type Preference struct {
	CompanyID  string  `json:"company_id" bson:"company_id"`
	ProjectID  string  `json:"project_id" bson:"project_id"`
	Binding    Binding `json:"binding" bson:"binding"`
	DeclinedAt string  `json:"declined_at" bson:"declined_at"`
}

type Registration struct {
	CompanyID   string  `json:"company_id"`
	CompanyName string  `json:"company_name"`
	ProjectID   string  `json:"project_id"`
	ProjectName string  `json:"project_name"`
	Binding     Binding `json:"binding"`
}

type FirstBindingRequest struct {
	ProjectID        string  `json:"project_id"`
	ExpectedRevision int64   `json:"expected_revision"`
	OperationID      string  `json:"operation_id"`
	Binding          Binding `json:"binding"`
}

// RepositoryBinding is the Project-facing Git capability configuration. The
// legacy Binding remains private persistence data for consumers still reading
// Foundation snapshots; its host paths are never returned by this API.
type RepositoryBinding struct {
	RepositoryID      string `json:"repository_id"`
	PublicationBranch string `json:"publication_branch"`
	State             string `json:"state"`
	Revision          int64  `json:"binding_revision"`
}

type RepositoryBindingRequest struct {
	Action           string `json:"action"`
	ProjectID        string `json:"project_id"`
	ExpectedRevision int64  `json:"expected_revision"`
	OperationID      string `json:"operation_id"`
	Actor            string `json:"actor"`
	RepositoryID     string `json:"repository_id,omitempty"`
	Branch           string `json:"publication_branch,omitempty"`
}

type SnapshotReference struct {
	CompanyID           string `json:"company_id"`
	BindingRevision     int64  `json:"binding_revision"`
	BindingDigest       string `json:"binding_digest"`
	ExpectedFingerprint string `json:"expected_fingerprint"`
	Fingerprint         string `json:"fingerprint"`
	PreparationState    string `json:"preparation_state"`
}

type LifecycleControl struct {
	Revision int64           `json:"revision"`
	Value    json.RawMessage `json:"value"`
}

type TenantResolution struct {
	CompanyID string  `json:"company_id"`
	Company   Company `json:"company"`
}

type TenantRequest struct {
	Type       string `json:"type"`
	Identifier string `json:"identifier"`
	CompanyID  string `json:"company_id,omitempty"`
}

type Service interface {
	Catalog(context.Context) (Catalog, error)
	Project(context.Context, string, string) (Project, error)
	Register(context.Context, Registration) (Project, error)
	BindFirst(context.Context, string, string, FirstBindingRequest) (Project, error)
	Preference(context.Context, string, string) (Preference, error)
	Decline(context.Context, string, string, Binding) error
	UpdateSnapshotReference(context.Context, string, string, SnapshotReference) (Project, error)
	LifecycleControl(context.Context) (LifecycleControl, error)
	UpdateLifecycleControl(context.Context, int64, json.RawMessage) (LifecycleControl, error)
	Roadmap(context.Context, string, string) (json.RawMessage, error)
	ResolveTenant(context.Context, TenantRequest) (TenantResolution, error)
}

type repositoryBindingService interface {
	ReadRepositoryBinding(context.Context, string, string) (RepositoryBinding, error)
	ChangeRepositoryBinding(context.Context, string, string, RepositoryBindingRequest) (RepositoryBinding, error)
}
