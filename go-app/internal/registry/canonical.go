package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path"
	"reflect"
	"regexp"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// Canonical knowledge is stored in family-specific collections. Data remains
// typed at the boundary; the raw payload is never interpreted as Markdown.
const (
	canonicalMandates      = "project_mandates"
	canonicalDomains       = "domain_catalogs"
	canonicalConstitutions = "project_constitutions"
	canonicalRoadmaps      = "strategic_roadmaps"
	canonicalModules       = "modules"
	canonicalEndpoints     = "endpoint_contracts"
	canonicalPolicies      = "policies"
	canonicalVersions      = "canonical_content_versions"
	canonicalEvents        = "canonical_events"
	maxCanonicalPayload    = 4 << 20
)

var canonicalID = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
var canonicalSourceRevision = regexp.MustCompile(`^[a-f0-9]{40}$`)

type CanonicalFamily string

const (
	FamilyMandate      CanonicalFamily = "project_mandate"
	FamilyDomain       CanonicalFamily = "domain_catalog"
	FamilyConstitution CanonicalFamily = "project_constitution"
	FamilyRoadmap      CanonicalFamily = "strategic_roadmap"
	FamilyModule       CanonicalFamily = "module"
	FamilyEndpoint     CanonicalFamily = "endpoint_contract"
	FamilyPolicy       CanonicalFamily = "policy"
)

type CanonicalRecord struct {
	ID            string          `json:"id" bson:"id"`
	ProjectID     string          `json:"project_id" bson:"project_id"`
	GenerationID  string          `json:"generation_id" bson:"generation_id"`
	Revision      int64           `json:"revision" bson:"revision"`
	ContentDigest string          `json:"content_digest" bson:"content_digest"`
	UpdatedAt     time.Time       `json:"updated_at" bson:"updated_at"`
	Payload       json.RawMessage `json:"payload" bson:"-"`
}

type CanonicalEvent struct {
	ObjectID       bson.ObjectID   `bson:"_id,omitempty" json:"-"`
	ProjectID      string          `bson:"project_id" json:"project_id"`
	GenerationID   string          `bson:"generation_id" json:"generation_id"`
	Family         CanonicalFamily `bson:"family" json:"family"`
	RecordID       string          `bson:"record_id" json:"record_id"`
	Kind           string          `bson:"kind" json:"kind"`
	Revision       int64           `bson:"revision" json:"revision"`
	OperationID    string          `bson:"operation_id" json:"operation_id"`
	Actor          string          `bson:"actor" json:"actor"`
	SourceRevision string          `bson:"source_revision,omitempty" json:"source_revision,omitempty"`
	OccurredAt     time.Time       `bson:"occurred_at" json:"occurred_at"`
}

type CanonicalMutation struct {
	ProjectID        string          `json:"project_id"`
	Family           CanonicalFamily `json:"family"`
	Record           CanonicalRecord `json:"record"`
	ExpectedRevision int64           `json:"expected_revision"`
	OperationID      string          `json:"operation_id"`
	Actor            string          `json:"actor"`
}

type CanonicalRestoreRequest struct {
	ProjectID        string          `json:"project_id"`
	Family           CanonicalFamily `json:"family"`
	RecordID         string          `json:"record_id"`
	Revision         int64           `json:"revision"`
	ExpectedRevision int64           `json:"expected_revision"`
	OperationID      string          `json:"operation_id"`
	Actor            string          `json:"actor"`
}

type CanonicalImport struct {
	ProjectID      string                  `json:"project_id"`
	GenerationID   string                  `json:"generation_id"`
	SourceRevision string                  `json:"source_revision"`
	Records        []CanonicalImportRecord `json:"records"`
	OperationID    string                  `json:"operation_id"`
	Actor          string                  `json:"actor"`
}

type CanonicalImportRecord struct {
	Family CanonicalFamily `json:"family"`
	Record CanonicalRecord `json:"record"`
}

type CanonicalManifest struct {
	ProjectID    string                    `json:"project_id"`
	GenerationID string                    `json:"generation_id,omitempty"`
	Active       bool                      `json:"active"`
	State        string                    `json:"state"`
	Teach        string                    `json:"teach,omitempty"`
	TeachDetail  *CanonicalTeachDiagnostic `json:"teach_detail,omitempty"`
	Documents    []CanonicalManifestEntry  `json:"documents"`
}

type CanonicalManifestEntry struct {
	Family        CanonicalFamily `json:"family"`
	ID            string          `json:"id"`
	Revision      int64           `json:"revision"`
	ContentDigest string          `json:"content_digest"`
}

type CanonicalHistoryPage struct {
	Events []CanonicalEvent `json:"events"`
}

type CanonicalTeachDiagnostic struct {
	Code           string   `json:"code"`
	FieldPath      string   `json:"field_path"`
	RecordID       string   `json:"record_id,omitempty"`
	AcceptedValues []string `json:"accepted_values,omitempty"`
	NextAction     string   `json:"next_action"`
}
type canonicalTeachError struct {
	Diagnostic CanonicalTeachDiagnostic
	Cause      error
}

func (e *canonicalTeachError) Error() string {
	return "TEACH: " + e.Diagnostic.FieldPath + " — " + e.Diagnostic.NextAction
}
func (e *canonicalTeachError) Unwrap() error { return e.Cause }

type CanonicalRule struct {
	ID        string   `json:"id"`
	Status    string   `json:"status,omitempty"`
	Statement string   `json:"statement"`
	AppliesTo []string `json:"applies_to,omitempty"`
}
type CanonicalDecision struct {
	ID             string               `json:"id"`
	Status         string               `json:"status,omitempty"`
	Statement      string               `json:"statement"`
	SemanticStatus string               `json:"semantic_status,omitempty"`
	Rationale      string               `json:"rationale,omitempty"`
	Evidence       []CanonicalReference `json:"evidence,omitempty"`
	Replaces       []string             `json:"replaces,omitempty"`
	CleanupTrigger string               `json:"cleanup_trigger,omitempty"`
}
type CanonicalEntity struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	Description       string `json:"description"`
	AuthorityBoundary string `json:"authority_boundary,omitempty"`
}
type CanonicalRelation struct {
	ID          string `json:"id"`
	From        string `json:"from"`
	To          string `json:"to"`
	Kind        string `json:"kind"`
	Statement   string `json:"statement,omitempty"`
	Cardinality string `json:"cardinality,omitempty"`
}
type CanonicalDistinction struct {
	ID     string   `json:"id"`
	Terms  []string `json:"terms"`
	Reason string   `json:"reason"`
}
type CanonicalOwnership struct {
	OwnerKind  string `json:"owner_kind"`
	OwnerID    string `json:"owner_id"`
	DecisionID string `json:"decision_id"`
}
type CanonicalField struct {
	Name         string               `json:"name"`
	Type         string               `json:"type"`
	Required     bool                 `json:"required"`
	Description  string               `json:"description,omitempty"`
	Nullable     bool                 `json:"nullable,omitempty"`
	Enum         []string             `json:"enum,omitempty"`
	Format       string               `json:"format,omitempty"`
	Pattern      string               `json:"pattern,omitempty"`
	Minimum      *float64             `json:"minimum,omitempty"`
	Maximum      *float64             `json:"maximum,omitempty"`
	MinLength    *int                 `json:"min_length,omitempty"`
	MaxLength    *int                 `json:"max_length,omitempty"`
	MinItems     *int                 `json:"min_items,omitempty"`
	MaxItems     *int                 `json:"max_items,omitempty"`
	Location     string               `json:"location,omitempty"`
	Fields       []CanonicalField     `json:"fields,omitempty"`
	Items        *CanonicalField      `json:"items,omitempty"`
	SchemaRef    string               `json:"schema_ref,omitempty"`
	OneOf        []CanonicalField     `json:"one_of,omitempty"`
	RequiredWhen []CanonicalCondition `json:"required_when,omitempty"`
}
type CanonicalStage struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	Status           string   `json:"status,omitempty"`
	Purpose          string   `json:"purpose,omitempty"`
	Context          string   `json:"context,omitempty"`
	ExecutiveValue   string   `json:"executive_value,omitempty"`
	FirstViewerSlice string   `json:"first_viewer_slice,omitempty"`
	OpenFronts       []string `json:"open_fronts,omitempty"`
	Capabilities     []string `json:"capabilities,omitempty"`
	Outcomes         []string `json:"outcomes,omitempty"`
}
type CanonicalEndpointPayload struct {
	CanonicalKnowledgeState
	Purpose     string                `json:"purpose,omitempty"`
	Method      string                `json:"method"`
	Route       string                `json:"route"`
	HostScope   string                `json:"host_scope"`
	ModuleID    string                `json:"module_id"`
	Request     []CanonicalField      `json:"request"`
	Response    []CanonicalField      `json:"response"`
	Errors      []CanonicalRule       `json:"errors,omitempty"`
	Security    *CanonicalSecurity    `json:"security,omitempty"`
	Pagination  *CanonicalPagination  `json:"pagination,omitempty"`
	Idempotency *CanonicalIdempotency `json:"idempotency,omitempty"`
	Patch       *CanonicalPatch       `json:"patch,omitempty"`
	Responses   []CanonicalResponse   `json:"responses,omitempty"`
	PolicyIDs   []string              `json:"policy_ids,omitempty"`
	EntityIDs   []string              `json:"entity_ids,omitempty"`
}
type MandatePayload struct {
	CanonicalKnowledgeState
	Purpose                string   `json:"purpose"`
	WorkingName            string   `json:"working_name,omitempty"`
	WorkingNameState       string   `json:"working_name_state,omitempty"`
	WorkingNamePolicy      string   `json:"working_name_policy,omitempty"`
	BusinessThesis         string   `json:"business_thesis,omitempty"`
	DesiredOutcomes        []string `json:"desired_outcomes,omitempty"`
	Scope                  []string `json:"scope"`
	ScopeContext           string   `json:"scope_context,omitempty"`
	Principles             []string `json:"principles,omitempty"`
	Exclusions             []string `json:"exclusions,omitempty"`
	FutureBoundary         string   `json:"future_boundary,omitempty"`
	SuccessMeasures        []string `json:"success_measures,omitempty"`
	SuccessMeasuresContext string   `json:"success_measures_context,omitempty"`
}
type DomainCatalogPayload struct {
	CanonicalKnowledgeState
	Entities            []CanonicalEntity         `json:"entities"`
	Relations           []CanonicalRelation       `json:"relations"`
	RelationshipRules   []CanonicalRule           `json:"relationship_rules,omitempty"`
	Distinctions        []CanonicalDistinction    `json:"distinctions"`
	QAValidationOutline *CanonicalQATargetOutline `json:"qa_validation_outline,omitempty"`
	LifecycleBoundary   string                    `json:"lifecycle_boundary,omitempty"`
}
type ConstitutionPayload struct {
	CanonicalKnowledgeState
	PromotionMode                string               `json:"promotion_mode"`
	CapabilityNamespaces         []string             `json:"capability_namespaces"`
	AuthorityBoundaries          []CanonicalNamedText `json:"authority_boundaries,omitempty"`
	ControlPlaneResponsibilities []CanonicalNamedText `json:"control_plane_responsibilities,omitempty"`
	PublicationFlow              []CanonicalProcess   `json:"publication_flow,omitempty"`
	SynchronizationOutcomes      []CanonicalNamedText `json:"synchronization_outcomes,omitempty"`
	FreshnessStates              []CanonicalNamedText `json:"freshness_states,omitempty"`
	ModuleBoundaryRefs           []CanonicalReference `json:"module_boundary_refs,omitempty"`
	Rules                        []CanonicalRule      `json:"rules"`
	Decisions                    []CanonicalDecision  `json:"decisions,omitempty"`
	MaintenanceRule              string               `json:"maintenance_rule,omitempty"`
}
type StrategicRoadmapPayload struct {
	CanonicalKnowledgeState
	StrategicObjective  string               `json:"strategic_objective,omitempty"`
	EpicCandidates      []CanonicalNamedText `json:"epic_candidates,omitempty"`
	EpicSequenceContext string               `json:"epic_sequence_context,omitempty"`
	Stages              []CanonicalStage     `json:"stages"`
	DeferredOptions     []CanonicalNamedText `json:"deferred_options,omitempty"`
	AdmissionRule       string               `json:"admission_rule,omitempty"`
}
type ModulePayload struct {
	CanonicalKnowledgeState
	Purpose         string                    `json:"purpose"`
	Coverage        []CanonicalNamedText      `json:"coverage,omitempty"`
	Processes       []CanonicalProcess        `json:"processes,omitempty"`
	Decisions       []CanonicalDecision       `json:"decisions,omitempty"`
	Interfaces      []CanonicalField          `json:"interfaces,omitempty"`
	Workflows       []CanonicalRule           `json:"workflows,omitempty"`
	Scope           []string                  `json:"scope,omitempty"`
	Exclusions      []string                  `json:"exclusions,omitempty"`
	Dependencies    []CanonicalDependency     `json:"dependencies,omitempty"`
	Routes          []CanonicalRouteOwnership `json:"routes,omitempty"`
	Schemas         []CanonicalSchema         `json:"schemas,omitempty"`
	Invariants      []CanonicalRule           `json:"invariants,omitempty"`
	LocalOperations []CanonicalLocalOperation `json:"local_operations,omitempty"`
	Events          []CanonicalEventContract  `json:"events,omitempty"`
	Jobs            []CanonicalJob            `json:"jobs,omitempty"`
	Observability   []CanonicalObservation    `json:"observability,omitempty"`
	Verification    []CanonicalVerification   `json:"verification,omitempty"`
}
type PolicyPayload struct {
	CanonicalKnowledgeState
	Purpose                   string                        `json:"purpose,omitempty"`
	Scope                     []string                      `json:"scope,omitempty"`
	Exclusions                []string                      `json:"exclusions,omitempty"`
	ScopeConcepts             []CanonicalNamedText          `json:"scope_concepts,omitempty"`
	ProhibitedInferences      []string                      `json:"prohibited_inferences,omitempty"`
	FutureSurfaceDeclarations []CanonicalNamedText          `json:"future_surface_declarations,omitempty"`
	FutureSurfaceCondition    string                        `json:"future_surface_condition,omitempty"`
	EnvironmentTypeState      string                        `json:"environment_type_state,omitempty"`
	MaintenanceRule           string                        `json:"maintenance_rule,omitempty"`
	Rules                     []CanonicalRule               `json:"rules"`
	Definitions               []CanonicalField              `json:"definitions,omitempty"`
	Ownership                 []CanonicalOwnership          `json:"ownership"`
	Decisions                 []CanonicalDecision           `json:"decisions,omitempty"`
	Routes                    []CanonicalRouteOwnership     `json:"routes,omitempty"`
	EndpointConventions       *CanonicalEndpointConventions `json:"endpoint_conventions,omitempty"`
}

func canonicalCollection(f CanonicalFamily) (string, error) {
	switch f {
	case FamilyMandate:
		return canonicalMandates, nil
	case FamilyDomain:
		return canonicalDomains, nil
	case FamilyConstitution:
		return canonicalConstitutions, nil
	case FamilyRoadmap:
		return canonicalRoadmaps, nil
	case FamilyModule:
		return canonicalModules, nil
	case FamilyEndpoint:
		return canonicalEndpoints, nil
	case FamilyPolicy:
		return canonicalPolicies, nil
	default:
		return "", ErrInvalid
	}
}

func validateCanonicalRecord(projectID, generationID string, family CanonicalFamily, record CanonicalRecord) error {
	if !validID(projectID) {
		return teachCanonical(record.ID, "project_id", "provide the exact registered Project ID", ErrInvalid)
	}
	if !canonicalID.MatchString(record.ID) {
		return teachCanonical(record.ID, "record.id", "provide a stable lowercase record ID", ErrInvalid)
	}
	if record.ProjectID != projectID {
		return teachCanonical(record.ID, "record.project_id", "match the request Project ID", ErrInvalid)
	}
	if !canonicalID.MatchString(generationID) || record.GenerationID != generationID {
		return teachCanonical(record.ID, "record.generation_id", "match the exact draft generation_id in the request", ErrInvalid)
	}
	if record.Revision < 1 {
		return teachCanonical(record.ID, "record.revision", "set the initial content revision to 1", ErrInvalid)
	}
	if len(record.Payload) == 0 || len(record.Payload) > maxCanonicalPayload {
		return teachCanonical(record.ID, "record.payload", "provide one typed payload object within the 4 MiB record limit", ErrInvalid)
	}
	if _, err := canonicalCollection(family); err != nil {
		return teachCanonical(record.ID, "family", "use a family listed by knowledge-template", ErrInvalid)
	}
	var target any
	switch family {
	case FamilyMandate:
		target = &MandatePayload{}
	case FamilyDomain:
		target = &DomainCatalogPayload{}
	case FamilyConstitution:
		target = &ConstitutionPayload{}
	case FamilyRoadmap:
		target = &StrategicRoadmapPayload{}
	case FamilyModule:
		target = &ModulePayload{}
	case FamilyEndpoint:
		target = &CanonicalEndpointPayload{}
	case FamilyPolicy:
		target = &PolicyPayload{}
	}
	if err := decodeCanonicalPayload(record.ID, record.Payload, target); err != nil {
		return err
	}
	return validateCanonicalPayload(record.ID, target)
}

func canonicalDigest(payload json.RawMessage) string {
	var compact any
	if json.Unmarshal(payload, &compact) != nil {
		return ""
	}
	data, _ := json.Marshal(compact)
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func teachCanonical(recordID, field, action string, err error) error {
	values := []string(nil)
	if strings.HasSuffix(field, ".status") {
		values = []string{"current", "planned", "deprecated", "historical"}
	}
	return &canonicalTeachError{Diagnostic: CanonicalTeachDiagnostic{Code: "canonical_validation", FieldPath: field, RecordID: recordID, AcceptedValues: values, NextAction: action}, Cause: err}
}

func normalizeCanonicalPayload(family CanonicalFamily, recordID string, payload json.RawMessage) (json.RawMessage, error) {
	if family != FamilyEndpoint {
		return payload, nil
	}
	var endpoint CanonicalEndpointPayload
	if err := decodeCanonicalPayload(recordID, payload, &endpoint); err != nil {
		return nil, err
	}
	endpoint.Method = strings.ToUpper(strings.TrimSpace(endpoint.Method))
	endpoint.HostScope = strings.ToLower(strings.TrimSpace(endpoint.HostScope))
	endpoint.Route = strings.TrimSpace(endpoint.Route)
	if !strings.HasPrefix(endpoint.Route, "/") || strings.ContainsAny(endpoint.Route, "?#") {
		return nil, teachCanonical(recordID, "payload.route", "provide a path beginning with /; move query parameters and fragments into the typed request contract", ErrInvalid)
	}
	endpoint.Route = path.Clean("/" + strings.TrimLeft(endpoint.Route, "/"))
	if endpoint.Route == "." {
		endpoint.Route = "/"
	}
	encoded, err := json.Marshal(endpoint)
	if err != nil {
		return nil, teachCanonical(recordID, "payload", "provide an encodable endpoint contract", ErrInvalid)
	}
	return encoded, nil
}

func CanonicalTemplate() map[string]any {
	formats := map[string]any{}
	definitions := map[string]map[string]string{}
	for family, payload := range map[CanonicalFamily]any{
		FamilyMandate: MandatePayload{}, FamilyDomain: DomainCatalogPayload{}, FamilyConstitution: ConstitutionPayload{},
		FamilyRoadmap: StrategicRoadmapPayload{}, FamilyModule: ModulePayload{}, FamilyEndpoint: CanonicalEndpointPayload{}, FamilyPolicy: PolicyPayload{},
	} {
		kind := reflect.TypeOf(payload)
		formats[string(family)] = kind.Name()
		canonicalTemplateFields(kind, definitions)
	}
	return map[string]any{
		"values":        map[string]any{"status": []string{"current", "planned", "deprecated", "historical"}, "decision_status": []string{"current", "planned", "deprecated", "historical"}, "owner_kind": []string{"module", "project_constitution", "policy"}},
		"identity":      map[string]string{"record_id": "lowercase slug", "semantic_id": "case-sensitive owner-local ID; preserve uppercase", "decision_ref": "owner_kind/owner_id/decision_id"},
		"reads":         []string{"entities/{entityID}", "endpoints/{endpointID}", "{family}/{recordID}/decisions/{decisionID}", "{family}/{recordID}/history/{revision}", "{family}/{recordID}/history/compare?from=1&to=2"},
		"families":      []string{string(FamilyMandate), string(FamilyDomain), string(FamilyConstitution), string(FamilyRoadmap), string(FamilyModule), string(FamilyEndpoint), string(FamilyPolicy)},
		"record":        map[string]any{"id": "stable-id", "project_id": "project", "generation_id": "generation", "revision": 1, "content_digest": "sha256:<digest>", "updated_at": "<BSON Date/RFC3339>", "payload": "<family-specific object>"},
		"payload_types": formats,
		"type_fields":   definitions,
		"migration_guidance": []string{
			"Draft import validates record structure but does not prove source-content completeness.",
			"Map every normative source statement to the matching typed field; do not summarize away rules, decisions, routes, schemas, operations, events or verification contracts.",
			"Compare imported records field by field against the fixed source revision before activation. Activation verifies structure and references, not editorial parity.",
			"Preserve explicit source IDs verbatim. For a source unit without an ID, use only a technical mig- ID derived by the published migration_id_policy; do not infer a semantic ID.",
			"Keep ambiguous source meanings or duplicate explicit IDs unresolved and request a decision; never silently merge distinct statements.",
		},
		"migration_id_policy": map[string]string{
			"applies_to": "source units requiring an ID but having no explicit source ID; never rewrite an explicit ID",
			"input":      "source_revision + newline + family + newline + record_id + newline + source_path + newline + heading_outline + newline + zero-based source_position + newline + exact source statement; normalize CRLF to LF and trim only outer whitespace",
			"output":     "mig- followed by the first 20 lowercase hexadecimal characters of SHA-256 over UTF-8 input; reject duplicate output IDs and ask for a decision",
			"lifecycle":  "persist the resulting ID in the imported record; do not recompute it after activation",
		},
		"payload_examples": map[string]any{
			string(FamilyMandate):      MandatePayload{Purpose: "Product purpose", Scope: []string{"audience"}},
			string(FamilyDomain):       DomainCatalogPayload{Entities: []CanonicalEntity{{ID: "project", Name: "Project"}}, Relations: []CanonicalRelation{}, Distinctions: []CanonicalDistinction{}},
			string(FamilyConstitution): ConstitutionPayload{PromotionMode: "mvp", CapabilityNamespaces: []string{"go"}, Rules: []CanonicalRule{{ID: "project-access", Status: "current", Statement: "Scope access by Project"}}},
			string(FamilyRoadmap):      StrategicRoadmapPayload{Stages: []CanonicalStage{{ID: "launch", Name: "Launch", Status: "planned"}}},
			string(FamilyModule):       ModulePayload{Purpose: "Owning module purpose", Decisions: []CanonicalDecision{{ID: "DEC-01", Status: "current", Statement: "The module owns its behavior"}}},
			string(FamilyEndpoint):     CanonicalEndpointPayload{Method: "GET", Route: "/projects", HostScope: "api", ModuleID: "projects", Request: []CanonicalField{}, Response: []CanonicalField{}},
			string(FamilyPolicy):       PolicyPayload{Rules: []CanonicalRule{{ID: "retention", Status: "current", Statement: "Retain governed events"}}, Ownership: []CanonicalOwnership{{OwnerKind: "module", OwnerID: "projects", DecisionID: "DEC-01"}}},
		},
		"limits": map[string]int{"max_record_bytes": maxCanonicalPayload, "max_records_per_import": 500},
	}
}

// The template describes the actual JSON tags on the typed payloads, including
// nested contracts. A reference by Go type name avoids recursive Field loops.
func canonicalTemplateFields(kind reflect.Type, definitions map[string]map[string]string) {
	for kind.Kind() == reflect.Pointer || kind.Kind() == reflect.Slice {
		kind = kind.Elem()
	}
	if kind.Kind() != reflect.Struct || kind.PkgPath() != reflect.TypeOf(MandatePayload{}).PkgPath() || kind.Name() == "" {
		return
	}
	if _, exists := definitions[kind.Name()]; exists {
		return
	}
	fields := map[string]string{}
	definitions[kind.Name()] = fields
	for i := 0; i < kind.NumField(); i++ {
		field := kind.Field(i)
		if field.Anonymous {
			canonicalTemplateFields(field.Type, definitions)
			if embedded := definitions[field.Type.Name()]; embedded != nil {
				for name, format := range embedded {
					fields[name] = format
				}
			}
			continue
		}
		name, optional, _ := strings.Cut(field.Tag.Get("json"), ",")
		if name == "" || name == "-" {
			continue
		}
		typeName := field.Type.String()
		if optional == "omitempty" {
			typeName += " (optional)"
		}
		fields[name] = typeName
		canonicalTemplateFields(field.Type, definitions)
	}
}

func (m *MongoStore) canonicalDB(ctx context.Context, companyID, projectID string) (*mongo.Database, Project, error) {
	if !validID(companyID) || !validID(projectID) {
		return nil, Project{}, ErrInvalid
	}
	company, err := m.company(ctx, companyID)
	if err != nil {
		return nil, Project{}, err
	}
	project, err := m.readProject(ctx, company, projectID)
	if err != nil {
		return nil, Project{}, err
	}
	return m.client.Database(company.DatabaseName), project, nil
}

func canonicalTime() time.Time { return time.Now().UTC().Truncate(time.Millisecond) }

func (m *MongoStore) ensureCanonicalCollections(ctx context.Context, database string) error {
	db := m.client.Database(database)
	families := []struct {
		family CanonicalFamily
		name   string
	}{
		{FamilyMandate, canonicalMandates}, {FamilyDomain, canonicalDomains}, {FamilyConstitution, canonicalConstitutions},
		{FamilyRoadmap, canonicalRoadmaps}, {FamilyModule, canonicalModules}, {FamilyEndpoint, canonicalEndpoints}, {FamilyPolicy, canonicalPolicies},
	}
	for _, item := range families {
		exists, err := db.ListCollectionNames(ctx, bson.D{{Key: "name", Value: item.name}})
		if err != nil {
			return err
		}
		validator := bson.M{"$jsonSchema": bson.M{"bsonType": "object", "required": bson.A{"_id", "id", "project_id", "generation_id", "revision", "content_digest", "updated_at", "payload"}, "properties": bson.M{
			"_id": bson.M{"bsonType": "objectId"}, "id": bson.M{"bsonType": "string", "minLength": 1, "maxLength": 128},
			"project_id": bson.M{"bsonType": "string"}, "generation_id": bson.M{"bsonType": "string"}, "revision": bson.M{"bsonType": "long", "minimum": 1},
			"content_digest": bson.M{"bsonType": "string", "pattern": "^sha256:[a-f0-9]{64}$"}, "updated_at": bson.M{"bsonType": "date"}, "payload": bson.M{"bsonType": "object"},
		}}}
		if len(exists) == 0 {
			if err := db.CreateCollection(ctx, item.name, options.CreateCollection().SetValidator(validator)); err != nil && !namespaceExists(err) {
				return err
			}
		} else {
			if err := db.RunCommand(ctx, bson.D{{Key: "collMod", Value: item.name}, {Key: "validator", Value: validator}, {Key: "validationLevel", Value: "strict"}, {Key: "validationAction", Value: "error"}}).Err(); err != nil {
				return err
			}
		}
		idx := []mongo.IndexModel{
			{Keys: bson.D{{Key: "project_id", Value: 1}, {Key: "generation_id", Value: 1}, {Key: "id", Value: 1}}, Options: options.Index().SetName(item.name + "_identity_unique").SetUnique(true)},
		}
		if item.family == FamilyEndpoint {
			idx = append(idx, mongo.IndexModel{Keys: bson.D{{Key: "project_id", Value: 1}, {Key: "generation_id", Value: 1}, {Key: "payload.method", Value: 1}, {Key: "payload.route", Value: 1}, {Key: "payload.host_scope", Value: 1}}, Options: options.Index().SetName("endpoint_method_route_host_unique").SetUnique(true)})
		}
		if _, err := db.Collection(item.name).Indexes().CreateMany(ctx, idx); err != nil {
			return err
		}
	}
	for _, name := range []string{canonicalVersions, canonicalEvents} {
		if _, err := db.ListCollectionNames(ctx, bson.D{{Key: "name", Value: name}}); err != nil {
			return err
		}
	}
	for _, model := range []struct {
		name   string
		index  string
		keys   bson.D
		unique bool
	}{
		{canonicalVersions, "canonical_versions_lookup", bson.D{{Key: "project_id", Value: 1}, {Key: "generation_id", Value: 1}, {Key: "family", Value: 1}, {Key: "record_id", Value: 1}, {Key: "revision", Value: 1}}, true},
		{canonicalEvents, "canonical_events_timeline", bson.D{{Key: "project_id", Value: 1}, {Key: "generation_id", Value: 1}, {Key: "occurred_at", Value: 1}, {Key: "_id", Value: 1}}, false},
		{canonicalEvents, "canonical_events_operation_unique", bson.D{{Key: "project_id", Value: 1}, {Key: "operation_id", Value: 1}, {Key: "family", Value: 1}, {Key: "record_id", Value: 1}, {Key: "kind", Value: 1}}, true},
	} {
		idxOptions := options.Index().SetName(model.index)
		if model.unique {
			idxOptions.SetUnique(true)
		}
		if _, err := db.Collection(model.name).Indexes().CreateOne(ctx, mongo.IndexModel{Keys: model.keys, Options: idxOptions}); err != nil {
			return err
		}
	}
	return nil
}

func (m *MongoStore) ImportCanonicalDraft(ctx context.Context, companyID string, input CanonicalImport) (CanonicalManifest, error) {
	if len(input.Records) == 0 || len(input.Records) > 500 {
		return CanonicalManifest{}, teachCanonical("", "records", "provide between 1 and 500 typed records in this draft import", ErrInvalid)
	}
	if !canonicalID.MatchString(input.GenerationID) {
		return CanonicalManifest{}, teachCanonical("", "generation_id", "provide a lowercase stable generation ID", ErrInvalid)
	}
	if !canonicalID.MatchString(input.OperationID) {
		return CanonicalManifest{}, teachCanonical("", "operation_id", "provide a lowercase stable operation ID", ErrInvalid)
	}
	if !canonicalSourceRevision.MatchString(input.SourceRevision) {
		return CanonicalManifest{}, teachCanonical("", "source_revision", "provide the exact 40-character lowercase Git SHA of the source revision", ErrInvalid)
	}
	if strings.TrimSpace(input.Actor) == "" || len(input.Actor) > 200 {
		return CanonicalManifest{}, teachCanonical("", "actor", "provide a non-empty actor identifier of at most 200 characters", ErrInvalid)
	}
	db, _, err := m.canonicalDB(ctx, companyID, input.ProjectID)
	if err != nil {
		return CanonicalManifest{}, err
	}
	seen := map[string]bool{}
	for i := range input.Records {
		item := &input.Records[i]
		item.Record.Payload, err = normalizeCanonicalPayload(item.Family, item.Record.ID, item.Record.Payload)
		if err != nil {
			return CanonicalManifest{}, err
		}
		if err := validateCanonicalRecord(input.ProjectID, input.GenerationID, item.Family, item.Record); err != nil {
			return CanonicalManifest{}, err
		}
		key := string(item.Family) + "/" + item.Record.ID
		if seen[key] {
			return CanonicalManifest{}, teachCanonical(item.Record.ID, "records", "avoid duplicate family/id pairs", ErrConflict)
		}
		seen[key] = true
		item.Record.Revision = 1
		item.Record.ContentDigest = canonicalDigest(item.Record.Payload)
		item.Record.UpdatedAt = canonicalTime()
	}
	var priorEvents []CanonicalEvent
	eventCursor, err := db.Collection(canonicalEvents).Find(ctx, bson.D{{Key: "project_id", Value: input.ProjectID}, {Key: "generation_id", Value: input.GenerationID}, {Key: "operation_id", Value: input.OperationID}, {Key: "kind", Value: "draft_imported"}})
	if err != nil {
		return CanonicalManifest{}, ErrUnavailable
	}
	for eventCursor.Next(ctx) {
		var event CanonicalEvent
		if eventCursor.Decode(&event) != nil {
			eventCursor.Close(ctx)
			return CanonicalManifest{}, ErrUnavailable
		}
		priorEvents = append(priorEvents, event)
	}
	eventCursor.Close(ctx)
	if len(priorEvents) > 0 {
		if len(priorEvents) != len(input.Records) {
			return CanonicalManifest{}, ErrConflict
		}
		prior := map[string]bool{}
		for _, event := range priorEvents {
			if event.SourceRevision != input.SourceRevision {
				return CanonicalManifest{}, ErrConflict
			}
			prior[string(event.Family)+"/"+event.RecordID] = true
		}
		for _, item := range input.Records {
			key := string(item.Family) + "/" + item.Record.ID
			if !prior[key] {
				return CanonicalManifest{}, ErrConflict
			}
			name, _ := canonicalCollection(item.Family)
			var stored canonicalStored
			if e := db.Collection(name).FindOne(ctx, bson.D{{Key: "project_id", Value: input.ProjectID}, {Key: "generation_id", Value: input.GenerationID}, {Key: "id", Value: item.Record.ID}}).Decode(&stored); e != nil || stored.ContentDigest != item.Record.ContentDigest {
				return CanonicalManifest{}, ErrConflict
			}
		}
		return m.CanonicalManifest(ctx, companyID, input.ProjectID, input.GenerationID)
	}
	// Drafts are invisible through active reads; the generation ID is part of every key.
	session, err := db.Client().StartSession()
	if err != nil {
		return CanonicalManifest{}, ErrUnavailable
	}
	defer session.EndSession(ctx)
	_, err = session.WithTransaction(ctx, func(sc context.Context) (any, error) {
		for _, item := range input.Records {
			collectionName, _ := canonicalCollection(item.Family)
			coll := db.Collection(collectionName)
			var existing bson.M
			e := coll.FindOne(sc, bson.D{{Key: "project_id", Value: input.ProjectID}, {Key: "generation_id", Value: input.GenerationID}, {Key: "id", Value: item.Record.ID}}, options.FindOne().SetProjection(bson.D{{Key: "_id", Value: 1}})).Decode(&existing)
			if e == nil {
				return nil, teachCanonical(item.Record.ID, "records", "this family/id already exists in the draft; create a new generation for corrected import, or use the same operation ID only for an identical retry", ErrConflict)
			}
			if !errors.Is(e, mongo.ErrNoDocuments) {
				return nil, ErrUnavailable
			}
			if _, e := coll.InsertOne(sc, canonicalStoredRecord(item.Family, item.Record)); e != nil {
				return nil, normalizeMongoError(e)
			}
			if _, e := db.Collection(canonicalVersions).InsertOne(sc, canonicalVersion(input.ProjectID, input.GenerationID, item.Family, item.Record)); e != nil {
				return nil, normalizeMongoError(e)
			}
			if _, e := db.Collection(canonicalEvents).InsertOne(sc, CanonicalEvent{ObjectID: bson.NewObjectID(), ProjectID: input.ProjectID, GenerationID: input.GenerationID, Family: item.Family, RecordID: item.Record.ID, Kind: "draft_imported", Revision: 1, OperationID: input.OperationID, Actor: input.Actor, SourceRevision: input.SourceRevision, OccurredAt: item.Record.UpdatedAt}); e != nil {
				return nil, normalizeMongoError(e)
			}
		}
		return nil, nil
	})
	if err != nil {
		normalized := normalizeMongoError(err)
		var teach *canonicalTeachError
		if errors.As(normalized, &teach) {
			return CanonicalManifest{}, normalized
		}
		if errors.Is(normalized, ErrConflict) {
			return CanonicalManifest{}, teachCanonical("", "records", "a family/id or operation already exists in this draft; use an identical operation ID only for a retry, otherwise import a clean generation", ErrConflict)
		}
		return CanonicalManifest{}, normalized
	}
	return m.CanonicalManifest(ctx, companyID, input.ProjectID, input.GenerationID)
}

type canonicalStored struct {
	ObjectID      bson.ObjectID   `bson:"_id,omitempty"`
	ID            string          `bson:"id"`
	ProjectID     string          `bson:"project_id"`
	GenerationID  string          `bson:"generation_id"`
	Revision      int64           `bson:"revision"`
	ContentDigest string          `bson:"content_digest"`
	UpdatedAt     time.Time       `bson:"updated_at"`
	Payload       bson.M          `bson:"payload"`
	Family        CanonicalFamily `bson:"family"`
}

func canonicalStoredRecord(f CanonicalFamily, r CanonicalRecord) canonicalStored {
	var payload bson.M
	if json.Unmarshal(r.Payload, &payload) != nil {
		payload = bson.M{}
	}
	return canonicalStored{ObjectID: bson.NewObjectID(), ID: r.ID, ProjectID: r.ProjectID, GenerationID: r.GenerationID, Revision: r.Revision, ContentDigest: r.ContentDigest, UpdatedAt: r.UpdatedAt, Payload: payload, Family: f}
}
func (r canonicalStored) toRecord() CanonicalRecord {
	payload, _ := json.Marshal(r.Payload)
	return CanonicalRecord{ID: r.ID, ProjectID: r.ProjectID, GenerationID: r.GenerationID, Revision: r.Revision, ContentDigest: r.ContentDigest, UpdatedAt: r.UpdatedAt, Payload: payload}
}
func canonicalVersion(project, generation string, f CanonicalFamily, r CanonicalRecord) bson.M {
	return bson.M{"_id": bson.NewObjectID(), "project_id": project, "generation_id": generation, "family": f, "record_id": r.ID, "revision": r.Revision, "content_digest": r.ContentDigest, "occurred_at": r.UpdatedAt, "record": canonicalStoredRecord(f, r)}
}

func (m *MongoStore) CanonicalManifest(ctx context.Context, companyID, projectID, generationID string) (CanonicalManifest, error) {
	db, project, err := m.canonicalDB(ctx, companyID, projectID)
	if err != nil {
		return CanonicalManifest{}, err
	}
	if generationID == "" {
		generationID = project.KnowledgeGenerationID
	}
	manifest := CanonicalManifest{ProjectID: projectID, GenerationID: generationID, Active: generationID != "" && generationID == project.KnowledgeGenerationID, Documents: []CanonicalManifestEntry{}}
	manifest.State = "draft"
	if manifest.Active {
		manifest.State = "active"
	}
	if generationID == "" {
		pending := canonicalPending()
		manifest.State, manifest.Teach, manifest.TeachDetail = "pending", pending.Error(), &pending.Diagnostic
		return manifest, nil
	}
	for _, family := range []CanonicalFamily{FamilyMandate, FamilyDomain, FamilyConstitution, FamilyRoadmap, FamilyModule, FamilyEndpoint, FamilyPolicy} {
		name, _ := canonicalCollection(family)
		cursor, e := db.Collection(name).Find(ctx, bson.D{{Key: "project_id", Value: projectID}, {Key: "generation_id", Value: generationID}}, options.Find().SetSort(bson.D{{Key: "id", Value: 1}}))
		if e != nil {
			return CanonicalManifest{}, ErrUnavailable
		}
		for cursor.Next(ctx) {
			var record canonicalStored
			if cursor.Decode(&record) != nil {
				cursor.Close(ctx)
				return CanonicalManifest{}, ErrUnavailable
			}
			manifest.Documents = append(manifest.Documents, CanonicalManifestEntry{Family: family, ID: record.ID, Revision: record.Revision, ContentDigest: record.ContentDigest})
		}
		if cursor.Err() != nil {
			cursor.Close(ctx)
			return CanonicalManifest{}, ErrUnavailable
		}
		cursor.Close(ctx)
	}
	return manifest, nil
}

func (m *MongoStore) ReadCanonical(ctx context.Context, companyID, projectID string, family CanonicalFamily, id string) (CanonicalRecord, error) {
	if !canonicalID.MatchString(id) {
		return CanonicalRecord{}, teachCanonical(id, "record_id", "use a stable lowercase record ID", ErrInvalid)
	}
	db, project, err := m.canonicalDB(ctx, companyID, projectID)
	if err != nil {
		return CanonicalRecord{}, err
	}
	if project.KnowledgeGenerationID == "" {
		return CanonicalRecord{}, canonicalPending()
	}
	name, err := canonicalCollection(family)
	if err != nil {
		return CanonicalRecord{}, err
	}
	var value canonicalStored
	err = db.Collection(name).FindOne(ctx, bson.D{{Key: "project_id", Value: projectID}, {Key: "generation_id", Value: project.KnowledgeGenerationID}, {Key: "id", Value: id}}).Decode(&value)
	if err == mongo.ErrNoDocuments {
		return CanonicalRecord{}, ErrNotFound
	}
	if err != nil {
		return CanonicalRecord{}, ErrUnavailable
	}
	return value.toRecord(), nil
}

// ReadCanonicalDraft is a private pre-activation readback for editorial parity.
// The explicit generation never changes the active authority pointer.
func (m *MongoStore) ReadCanonicalDraft(ctx context.Context, companyID, projectID, generationID string, family CanonicalFamily, id string) (CanonicalRecord, error) {
	if !canonicalID.MatchString(generationID) || !canonicalID.MatchString(id) {
		return CanonicalRecord{}, teachCanonical(id, "generation_id/record_id", "use exact IDs from this Project's draft knowledge-manifest", ErrInvalid)
	}
	db, project, err := m.canonicalDB(ctx, companyID, projectID)
	if err != nil {
		return CanonicalRecord{}, err
	}
	if project.KnowledgeGenerationID == generationID {
		return CanonicalRecord{}, teachCanonical(generationID, "generation_id", "this generation is active; omit generation_id to read the current canonical record", ErrInvalid)
	}
	name, err := canonicalCollection(family)
	if err != nil {
		return CanonicalRecord{}, err
	}
	var value canonicalStored
	err = db.Collection(name).FindOne(ctx, bson.D{{Key: "project_id", Value: projectID}, {Key: "generation_id", Value: generationID}, {Key: "id", Value: id}}).Decode(&value)
	if err == mongo.ErrNoDocuments {
		return CanonicalRecord{}, teachCanonical(id, "generation_id/family/record_id", "import this record into the exact draft generation, then read it back before activation", ErrNotFound)
	}
	if err != nil {
		return CanonicalRecord{}, ErrUnavailable
	}
	return value.toRecord(), nil
}

func (m *MongoStore) EditCanonical(ctx context.Context, companyID string, input CanonicalMutation) (CanonicalRecord, error) {
	return m.editCanonical(ctx, companyID, input, "content_changed")
}

func (m *MongoStore) editCanonical(ctx context.Context, companyID string, input CanonicalMutation, eventKind string) (CanonicalRecord, error) {
	if !canonicalID.MatchString(input.OperationID) || strings.TrimSpace(input.Actor) == "" || len(input.Actor) > 200 {
		return CanonicalRecord{}, ErrInvalid
	}
	db, project, err := m.canonicalDB(ctx, companyID, input.ProjectID)
	if err != nil {
		return CanonicalRecord{}, err
	}
	generation := project.KnowledgeGenerationID
	if generation == "" {
		return CanonicalRecord{}, ErrNotFound
	}
	input.Record.Payload, err = normalizeCanonicalPayload(input.Family, input.Record.ID, input.Record.Payload)
	if err != nil {
		return CanonicalRecord{}, err
	}
	var prior CanonicalEvent
	if e := db.Collection(canonicalEvents).FindOne(ctx, bson.D{{Key: "project_id", Value: input.ProjectID}, {Key: "generation_id", Value: generation}, {Key: "family", Value: input.Family}, {Key: "record_id", Value: input.Record.ID}, {Key: "operation_id", Value: input.OperationID}, {Key: "kind", Value: eventKind}}).Decode(&prior); e == nil {
		var version struct {
			Record canonicalStored `bson:"record"`
		}
		if e = db.Collection(canonicalVersions).FindOne(ctx, bson.D{{Key: "project_id", Value: input.ProjectID}, {Key: "generation_id", Value: generation}, {Key: "family", Value: input.Family}, {Key: "record_id", Value: input.Record.ID}, {Key: "revision", Value: prior.Revision}}).Decode(&version); e != nil {
			return CanonicalRecord{}, ErrUnavailable
		}
		if version.Record.ContentDigest != canonicalDigest(input.Record.Payload) || prior.Actor != input.Actor {
			return CanonicalRecord{}, ErrConflict
		}
		return version.Record.toRecord(), nil
	} else if e != mongo.ErrNoDocuments {
		return CanonicalRecord{}, ErrUnavailable
	}
	current, err := m.ReadCanonical(ctx, companyID, input.ProjectID, input.Family, input.Record.ID)
	if err != nil {
		return CanonicalRecord{}, err
	}
	if current.Revision != input.ExpectedRevision {
		return CanonicalRecord{}, ErrConflict
	}
	input.Record.ProjectID = input.ProjectID
	input.Record.GenerationID = generation
	input.Record.Revision = current.Revision + 1
	if err = validateCanonicalRecord(input.ProjectID, generation, input.Family, input.Record); err != nil {
		return CanonicalRecord{}, err
	}
	input.Record.ContentDigest = canonicalDigest(input.Record.Payload)
	input.Record.UpdatedAt = canonicalTime()
	name, _ := canonicalCollection(input.Family)
	filter := bson.D{{Key: "project_id", Value: input.ProjectID}, {Key: "generation_id", Value: generation}, {Key: "id", Value: input.Record.ID}, {Key: "revision", Value: input.ExpectedRevision}}
	session, err := db.Client().StartSession()
	if err != nil {
		return CanonicalRecord{}, ErrUnavailable
	}
	defer session.EndSession(ctx)
	_, err = session.WithTransaction(ctx, func(sc context.Context) (any, error) {
		stored := canonicalStoredRecord(input.Family, input.Record)
		encoded, e := bson.Marshal(stored)
		if e != nil {
			return nil, e
		}
		var fields bson.M
		if e = bson.Unmarshal(encoded, &fields); e != nil {
			return nil, e
		}
		delete(fields, "_id")
		result, e := db.Collection(name).UpdateOne(sc, filter, bson.M{"$set": fields})
		if e != nil {
			return nil, e
		}
		if result.MatchedCount != 1 {
			return nil, ErrConflict
		}
		if _, e = db.Collection(canonicalVersions).InsertOne(sc, canonicalVersion(input.ProjectID, generation, input.Family, input.Record)); e != nil {
			return nil, e
		}
		if _, e = db.Collection(canonicalEvents).InsertOne(sc, CanonicalEvent{ObjectID: bson.NewObjectID(), ProjectID: input.ProjectID, GenerationID: generation, Family: input.Family, RecordID: input.Record.ID, Kind: eventKind, Revision: input.Record.Revision, OperationID: input.OperationID, Actor: input.Actor, OccurredAt: input.Record.UpdatedAt}); e != nil {
			return nil, e
		}
		return nil, nil
	})
	if err != nil {
		return CanonicalRecord{}, normalizeMongoError(err)
	}
	return input.Record, nil
}

func (m *MongoStore) CreateCanonical(ctx context.Context, companyID string, input CanonicalMutation) (CanonicalRecord, error) {
	if input.ExpectedRevision != 0 || !canonicalID.MatchString(input.OperationID) || strings.TrimSpace(input.Actor) == "" || len(input.Actor) > 200 {
		return CanonicalRecord{}, ErrInvalid
	}
	db, project, err := m.canonicalDB(ctx, companyID, input.ProjectID)
	if err != nil {
		return CanonicalRecord{}, err
	}
	generation := project.KnowledgeGenerationID
	if generation == "" {
		return CanonicalRecord{}, ErrNotFound
	}
	input.Record.ProjectID, input.Record.GenerationID, input.Record.Revision = input.ProjectID, generation, 1
	input.Record.Payload, err = normalizeCanonicalPayload(input.Family, input.Record.ID, input.Record.Payload)
	if err != nil {
		return CanonicalRecord{}, err
	}
	if err = validateCanonicalRecord(input.ProjectID, generation, input.Family, input.Record); err != nil {
		return CanonicalRecord{}, err
	}
	input.Record.ContentDigest, input.Record.UpdatedAt = canonicalDigest(input.Record.Payload), canonicalTime()
	name, err := canonicalCollection(input.Family)
	if err != nil {
		return CanonicalRecord{}, err
	}
	var prior CanonicalEvent
	if err = db.Collection(canonicalEvents).FindOne(ctx, bson.D{{Key: "project_id", Value: input.ProjectID}, {Key: "generation_id", Value: generation}, {Key: "family", Value: input.Family}, {Key: "record_id", Value: input.Record.ID}, {Key: "operation_id", Value: input.OperationID}, {Key: "kind", Value: "record_created"}}).Decode(&prior); err == nil {
		var stored canonicalStored
		if err = db.Collection(name).FindOne(ctx, bson.D{{Key: "project_id", Value: input.ProjectID}, {Key: "generation_id", Value: generation}, {Key: "id", Value: input.Record.ID}}).Decode(&stored); err != nil || stored.ContentDigest != input.Record.ContentDigest || prior.Actor != input.Actor {
			return CanonicalRecord{}, ErrConflict
		}
		return stored.toRecord(), nil
	} else if err != mongo.ErrNoDocuments {
		return CanonicalRecord{}, ErrUnavailable
	}
	session, err := db.Client().StartSession()
	if err != nil {
		return CanonicalRecord{}, ErrUnavailable
	}
	defer session.EndSession(ctx)
	_, err = session.WithTransaction(ctx, func(tx context.Context) (any, error) {
		if _, e := db.Collection(name).InsertOne(tx, canonicalStoredRecord(input.Family, input.Record)); e != nil {
			return nil, e
		}
		if _, e := db.Collection(canonicalVersions).InsertOne(tx, canonicalVersion(input.ProjectID, generation, input.Family, input.Record)); e != nil {
			return nil, e
		}
		if _, e := db.Collection(canonicalEvents).InsertOne(tx, CanonicalEvent{ObjectID: bson.NewObjectID(), ProjectID: input.ProjectID, GenerationID: generation, Family: input.Family, RecordID: input.Record.ID, Kind: "record_created", Revision: 1, OperationID: input.OperationID, Actor: input.Actor, OccurredAt: input.Record.UpdatedAt}); e != nil {
			return nil, e
		}
		return nil, nil
	})
	if err != nil {
		return CanonicalRecord{}, normalizeMongoError(err)
	}
	return input.Record, nil
}

func (m *MongoStore) RestoreCanonical(ctx context.Context, companyID string, input CanonicalRestoreRequest) (CanonicalRecord, error) {
	if input.Revision < 1 || input.ExpectedRevision < 1 || !canonicalID.MatchString(input.OperationID) || strings.TrimSpace(input.Actor) == "" {
		return CanonicalRecord{}, ErrInvalid
	}
	db, project, err := m.canonicalDB(ctx, companyID, input.ProjectID)
	if err != nil {
		return CanonicalRecord{}, err
	}
	if project.KnowledgeGenerationID == "" {
		return CanonicalRecord{}, ErrNotFound
	}
	var version struct {
		Record canonicalStored `bson:"record"`
	}
	err = db.Collection(canonicalVersions).FindOne(ctx, bson.D{{Key: "project_id", Value: input.ProjectID}, {Key: "generation_id", Value: project.KnowledgeGenerationID}, {Key: "family", Value: input.Family}, {Key: "record_id", Value: input.RecordID}, {Key: "revision", Value: input.Revision}}).Decode(&version)
	if err == mongo.ErrNoDocuments {
		return CanonicalRecord{}, ErrNotFound
	}
	if err != nil {
		return CanonicalRecord{}, ErrUnavailable
	}
	return m.editCanonical(ctx, companyID, CanonicalMutation{ProjectID: input.ProjectID, Family: input.Family, Record: version.Record.toRecord(), ExpectedRevision: input.ExpectedRevision, OperationID: input.OperationID, Actor: input.Actor}, "content_restored")
}

func validateCanonicalGeneration(ctx context.Context, db *mongo.Database, projectID, generationID string, manifest CanonicalManifest) error {
	modules := map[string]bool{}
	for _, entry := range manifest.Documents {
		if entry.Family == FamilyModule {
			modules[entry.ID] = true
		}
	}
	for _, entry := range manifest.Documents {
		if entry.Family != FamilyEndpoint && entry.Family != FamilyDomain && entry.Family != FamilyPolicy {
			continue
		}
		name, _ := canonicalCollection(entry.Family)
		var stored canonicalStored
		if err := db.Collection(name).FindOne(ctx, bson.D{{Key: "project_id", Value: projectID}, {Key: "generation_id", Value: generationID}, {Key: "id", Value: entry.ID}}).Decode(&stored); err != nil {
			return ErrUnavailable
		}
		switch entry.Family {
		case FamilyEndpoint:
			var endpoint CanonicalEndpointPayload
			if json.Unmarshal(stored.toRecord().Payload, &endpoint) != nil || !modules[endpoint.ModuleID] {
				return teachCanonical(entry.ID, "payload.module_id", "reference an existing module owner", ErrInvalid)
			}
		case FamilyDomain:
			var domain DomainCatalogPayload
			if json.Unmarshal(stored.toRecord().Payload, &domain) != nil {
				return ErrInvalid
			}
			entities := map[string]bool{}
			for _, entity := range domain.Entities {
				if !canonicalID.MatchString(entity.ID) || strings.TrimSpace(entity.Name) == "" || entities[entity.ID] {
					return teachCanonical(entry.ID, "payload.entities", "use unique stable entity IDs and names", ErrInvalid)
				}
				entities[entity.ID] = true
			}
			for _, relation := range domain.Relations {
				if !canonicalID.MatchString(relation.ID) || !entities[relation.From] || !entities[relation.To] {
					return teachCanonical(entry.ID, "payload.relations", "reference declared entity IDs", ErrInvalid)
				}
			}
		case FamilyPolicy:
			var policy PolicyPayload
			if json.Unmarshal(stored.toRecord().Payload, &policy) != nil {
				return ErrInvalid
			}
			for _, owner := range policy.Ownership {
				if !canonicalOwnerHasDecision(ctx, db, projectID, generationID, owner) {
					return teachCanonical(entry.ID, "payload.ownership", "reference an existing owner and decision ID", ErrInvalid)
				}
			}
		}
	}
	return nil
}
func manifestCount(manifest CanonicalManifest, family CanonicalFamily) int {
	count := 0
	for _, entry := range manifest.Documents {
		if entry.Family == family {
			count++
		}
	}
	return count
}

func canonicalOwnerHasDecision(ctx context.Context, db *mongo.Database, projectID, generationID string, owner CanonicalOwnership) bool {
	var family CanonicalFamily
	switch owner.OwnerKind {
	case "module":
		family = FamilyModule
	case "project_constitution":
		family = FamilyConstitution
	case "policy":
		family = FamilyPolicy
	default:
		return false
	}
	name, _ := canonicalCollection(family)
	var stored canonicalStored
	if db.Collection(name).FindOne(ctx, bson.D{{Key: "project_id", Value: projectID}, {Key: "generation_id", Value: generationID}, {Key: "id", Value: owner.OwnerID}}).Decode(&stored) != nil {
		return false
	}
	if owner.DecisionID == "" {
		return true
	}
	var payload struct {
		Decisions []CanonicalDecision `json:"decisions"`
	}
	if json.Unmarshal(stored.toRecord().Payload, &payload) != nil {
		return false
	}
	for _, item := range payload.Decisions {
		if item.ID == owner.DecisionID {
			return true
		}
	}
	return false
}

func (m *MongoStore) ActivateCanonical(ctx context.Context, companyID, projectID, generationID, operationID, actor string) (CanonicalManifest, error) {
	if !canonicalID.MatchString(generationID) || !canonicalID.MatchString(operationID) || strings.TrimSpace(actor) == "" || len(actor) > 200 {
		return CanonicalManifest{}, ErrInvalid
	}
	db, project, err := m.canonicalDB(ctx, companyID, projectID)
	if err != nil {
		return CanonicalManifest{}, err
	}
	if project.KnowledgeGenerationID == generationID {
		return m.CanonicalManifest(ctx, companyID, projectID, generationID)
	}
	if project.KnowledgeGenerationID != "" {
		return CanonicalManifest{}, teachCanonical(projectID, "generation_id", "this Project already has an active generation; edit or restore its canonical records instead of replacing the generation", ErrConflict)
	}
	manifest, err := m.CanonicalManifest(ctx, companyID, projectID, generationID)
	if err != nil {
		return CanonicalManifest{}, err
	}
	present := map[CanonicalFamily]int{}
	seenRecords := map[string]bool{}
	for _, doc := range manifest.Documents {
		key := string(doc.Family) + "/" + doc.ID
		if seenRecords[key] {
			return CanonicalManifest{}, teachCanonical(doc.ID, "generation.documents", "remove duplicate family/id records from the draft and import a clean generation before activation", ErrConflict)
		}
		seenRecords[key] = true
		present[doc.Family]++
	}
	for _, family := range []CanonicalFamily{FamilyMandate, FamilyDomain, FamilyConstitution, FamilyRoadmap} {
		if present[family] != 1 {
			return CanonicalManifest{}, teachCanonical("", "generation."+string(family), "contain exactly one canonical singleton", ErrInvalid)
		}
	}
	if present[FamilyModule] == 0 || present[FamilyPolicy] == 0 {
		return CanonicalManifest{}, teachCanonical("", "generation", "contain at least one module and policy", ErrInvalid)
	}
	if err := validateCanonicalGeneration(ctx, db, projectID, generationID, manifest); err != nil {
		return CanonicalManifest{}, err
	}
	filter := bson.D{{Key: "id", Value: projectID}, {Key: "company_id", Value: companyID}}
	filter = append(filter, bson.E{Key: "$or", Value: bson.A{bson.M{"knowledge_generation_id": bson.M{"$exists": false}}, bson.M{"knowledge_generation_id": ""}}})
	session, err := db.Client().StartSession()
	if err != nil {
		return CanonicalManifest{}, ErrUnavailable
	}
	defer session.EndSession(ctx)
	_, err = session.WithTransaction(ctx, func(sc context.Context) (any, error) {
		result, e := db.Collection(projectsCollection).UpdateOne(sc, filter, bson.M{"$set": bson.M{"knowledge_generation_id": generationID}})
		if e != nil {
			return nil, e
		}
		if result.MatchedCount != 1 {
			return nil, ErrConflict
		}
		_, e = db.Collection(canonicalEvents).InsertOne(sc, CanonicalEvent{ObjectID: bson.NewObjectID(), ProjectID: projectID, GenerationID: generationID, Family: "project", RecordID: projectID, Kind: "generation_activated", Revision: 1, OperationID: operationID, Actor: actor, OccurredAt: canonicalTime()})
		return nil, e
	})
	if err != nil {
		return CanonicalManifest{}, normalizeMongoError(err)
	}
	manifest.Active, manifest.State = true, "active"
	return manifest, nil
}

func (m *MongoStore) CanonicalHistory(ctx context.Context, companyID, projectID string, after time.Time, limit int) (CanonicalHistoryPage, error) {
	if limit < 1 || limit > 100 {
		return CanonicalHistoryPage{}, ErrInvalid
	}
	db, _, err := m.canonicalDB(ctx, companyID, projectID)
	if err != nil {
		return CanonicalHistoryPage{}, err
	}
	filter := bson.D{{Key: "project_id", Value: projectID}}
	if !after.IsZero() {
		filter = append(filter, bson.E{Key: "occurred_at", Value: bson.M{"$gt": after}})
	}
	cursor, err := db.Collection(canonicalEvents).Find(ctx, filter, options.Find().SetSort(bson.D{{Key: "occurred_at", Value: 1}, {Key: "_id", Value: 1}}).SetLimit(int64(limit)))
	if err != nil {
		return CanonicalHistoryPage{}, ErrUnavailable
	}
	defer cursor.Close(ctx)
	page := CanonicalHistoryPage{Events: []CanonicalEvent{}}
	for cursor.Next(ctx) {
		var item CanonicalEvent
		if cursor.Decode(&item) != nil {
			return CanonicalHistoryPage{}, ErrUnavailable
		}
		page.Events = append(page.Events, item)
	}
	if cursor.Err() != nil {
		return CanonicalHistoryPage{}, ErrUnavailable
	}
	return page, nil
}
