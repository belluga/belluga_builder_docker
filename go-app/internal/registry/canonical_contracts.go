package registry

// These are documentary contracts, not executable validators for the described
// product. Their typed fields preserve ownership and machine-readable semantics.
type CanonicalReference struct {
	Kind string `json:"kind"`
	Ref  string `json:"ref"`
}

type CanonicalKnowledgeState struct {
	Status         string              `json:"status,omitempty"`
	SemanticStatus string              `json:"semantic_status,omitempty"`
	OpenQuestions  []CanonicalQuestion `json:"open_questions,omitempty"`
}

type CanonicalQuestion struct {
	ID       string               `json:"id"`
	Question string               `json:"question"`
	Status   string               `json:"status"`
	Owner    string               `json:"owner,omitempty"`
	Evidence []CanonicalReference `json:"evidence,omitempty"`
}

// NamedText preserves a named contract entry without treating a whole source
// document as an unstructured section.
type CanonicalNamedText struct {
	Name string `json:"name"`
	Text string `json:"text"`
}

type CanonicalProcess struct {
	Name  string               `json:"name"`
	Steps []CanonicalNamedText `json:"steps"`
}

type CanonicalQATargetOutline struct {
	Applicability string               `json:"applicability"`
	Fields        []CanonicalNamedText `json:"fields"`
	Definitions   []CanonicalNamedText `json:"definitions,omitempty"`
}

type CanonicalCondition struct {
	Field    string   `json:"field"`
	Operator string   `json:"operator"`
	Values   []string `json:"values,omitempty"`
}

type CanonicalSchema struct {
	ID         string           `json:"id"`
	Name       string           `json:"name"`
	Kind       string           `json:"kind"`
	EntityID   string           `json:"entity_id,omitempty"`
	Fields     []CanonicalField `json:"fields"`
	Indexes    []CanonicalIndex `json:"indexes,omitempty"`
	Invariants []CanonicalRule  `json:"invariants,omitempty"`
}

type CanonicalIndex struct {
	ID                 string               `json:"id"`
	Keys               []CanonicalIndexKey  `json:"keys"`
	Unique             bool                 `json:"unique,omitempty"`
	Sparse             bool                 `json:"sparse,omitempty"`
	ExpireAfterSeconds *int64               `json:"expire_after_seconds,omitempty"`
	PartialFilter      []CanonicalCondition `json:"partial_filter,omitempty"`
}

type CanonicalIndexKey struct {
	Field string `json:"field"`
	Order string `json:"order"`
}

type CanonicalDependency struct {
	Kind           string `json:"kind"`
	Ref            string `json:"ref"`
	Responsibility string `json:"responsibility"`
}

type CanonicalRouteOwnership struct {
	Route           string  `json:"route"`
	HostScope       string  `json:"host_scope"`
	EnvironmentType *string `json:"environment_type"`
	MainScope       *string `json:"main_scope"`
	Subscope        *string `json:"subscope"`
	Guard           string  `json:"guard"`
	ScreenID        string  `json:"screen_id,omitempty"`
}

type CanonicalLocalOperation struct {
	ID           string                   `json:"id"`
	Command      string                   `json:"command"`
	Flags        []CanonicalField         `json:"flags,omitempty"`
	ExitCodes    []CanonicalExitCode      `json:"exit_codes"`
	Response     []CanonicalField         `json:"response,omitempty"`
	Digest       *CanonicalDigestContract `json:"digest,omitempty"`
	SideEffects  []string                 `json:"side_effects,omitempty"`
	Limits       []CanonicalField         `json:"limits,omitempty"`
	Requirements []CanonicalRule          `json:"requirements,omitempty"`
}

type CanonicalExitCode struct {
	Code           int    `json:"code"`
	Outcome        string `json:"outcome"`
	Stdout         string `json:"stdout,omitempty"`
	Stderr         string `json:"stderr,omitempty"`
	ConsumerAction string `json:"consumer_action"`
}

type CanonicalDigestContract struct {
	Algorithm  string   `json:"algorithm"`
	Fields     []string `json:"fields"`
	Exclusions []string `json:"exclusions,omitempty"`
}

type CanonicalEventContract struct {
	Name         string           `json:"name"`
	Direction    string           `json:"direction"`
	Transport    string           `json:"transport,omitempty"`
	Payload      []CanonicalField `json:"payload,omitempty"`
	Requirements []CanonicalRule  `json:"requirements,omitempty"`
}

type CanonicalJob struct {
	ID                    string   `json:"id"`
	Handler               string   `json:"handler"`
	Schedule              string   `json:"schedule"`
	Timezone              string   `json:"timezone,omitempty"`
	LifecycleRequirements []string `json:"lifecycle_requirements,omitempty"`
}

type CanonicalObservation struct {
	ID         string           `json:"id"`
	Kind       string           `json:"kind"`
	Name       string           `json:"name"`
	Fields     []CanonicalField `json:"fields,omitempty"`
	Target     string           `json:"target,omitempty"`
	Comparison string           `json:"comparison,omitempty"`
	Threshold  *float64         `json:"threshold,omitempty"`
	Unit       string           `json:"unit,omitempty"`
}

type CanonicalVerification struct {
	ID          string   `json:"id"`
	Layer       string   `json:"layer"`
	Requirement string   `json:"requirement"`
	Commands    []string `json:"commands,omitempty"`
}

type CanonicalSecurity struct {
	Level                string          `json:"level,omitempty"`
	Authentication       string          `json:"authentication,omitempty"`
	Abilities            []string        `json:"abilities,omitempty"`
	Principals           []string        `json:"principals,omitempty"`
	TrustedProxyRequired bool            `json:"trusted_proxy_required,omitempty"`
	DirectOriginBlocked  bool            `json:"direct_origin_blocked,omitempty"`
	ObserveMode          *bool           `json:"observe_mode,omitempty"`
	RejectionCodes       []string        `json:"rejection_codes,omitempty"`
	ResponseHeaders      []string        `json:"response_headers,omitempty"`
	Requirements         []CanonicalRule `json:"requirements,omitempty"`
}

type CanonicalPagination struct {
	Mode           string              `json:"mode"`
	RequestFields  []CanonicalField    `json:"request_fields,omitempty"`
	ResponseFields []CanonicalField    `json:"response_fields,omitempty"`
	Order          []CanonicalIndexKey `json:"order,omitempty"`
	DefaultSize    *int                `json:"default_size,omitempty"`
	MaxSize        *int                `json:"max_size,omitempty"`
}

type CanonicalIdempotency struct {
	Required            bool            `json:"required"`
	Header              string          `json:"header,omitempty"`
	ReplayWindowSeconds *int64          `json:"replay_window_seconds,omitempty"`
	Scope               []string        `json:"scope,omitempty"`
	ConflictStatusCode  int             `json:"conflict_status_code,omitempty"`
	Requirements        []CanonicalRule `json:"requirements,omitempty"`
}

type CanonicalPatch struct {
	OmittedFields string `json:"omitted_fields"`
	NullFields    string `json:"null_fields"`
	Atomic        bool   `json:"atomic"`
}

type CanonicalResponse struct {
	StatusCode  int              `json:"status_code"`
	Description string           `json:"description"`
	Fields      []CanonicalField `json:"fields,omitempty"`
	Headers     []CanonicalField `json:"headers,omitempty"`
}

type CanonicalEndpointConventions struct {
	BasePath         string                `json:"base_path"`
	IDFormat         string                `json:"id_format,omitempty"`
	DatetimeFormat   string                `json:"datetime_format,omitempty"`
	TenantIDRequired bool                  `json:"tenant_id_required,omitempty"`
	Pagination       *CanonicalPagination  `json:"pagination,omitempty"`
	Security         *CanonicalSecurity    `json:"security,omitempty"`
	Idempotency      *CanonicalIdempotency `json:"idempotency,omitempty"`
	Patch            *CanonicalPatch       `json:"patch,omitempty"`
}
