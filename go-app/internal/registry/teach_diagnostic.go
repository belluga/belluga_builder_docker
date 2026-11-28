package registry

import (
	"encoding/json"
	"errors"
	"unicode/utf8"
)

type teachDiagnosticError struct {
	err        error
	diagnostic TeachDiagnostic
}

func (e *teachDiagnosticError) Error() string { return e.err.Error() }
func (e *teachDiagnosticError) Unwrap() error { return e.err }

func withTeachDiagnostic(err error, diagnostic TeachDiagnostic) error {
	if err == nil {
		return nil
	}
	return &teachDiagnosticError{err: err, diagnostic: diagnostic}
}

func explicitTeachDiagnostic(err error) *TeachDiagnostic {
	var wrapped *teachDiagnosticError
	if errors.As(err, &wrapped) {
		diagnostic := wrapped.diagnostic
		return &diagnostic
	}
	return nil
}

func readTeach(code, field, action string) *TeachDiagnostic {
	return &TeachDiagnostic{SchemaVersion: teachDiagnosticSchemaVersion, Code: code, FieldPath: field, NextAction: action, Effects: TeachEffectsNotApplicable}
}

func registryErrorDiagnostic(err error, publicCode string) *TeachDiagnostic {
	var validation *TODOValidationError
	if errors.As(err, &validation) {
		return &TeachDiagnostic{SchemaVersion: teachDiagnosticSchemaVersion, Code: "registry_validation", FieldPath: validation.Field, NextAction: validation.Expected}
	}
	var canonical *canonicalTeachError
	if errors.As(err, &canonical) {
		diagnostic := canonical.Diagnostic
		diagnostic.SchemaVersion = teachDiagnosticSchemaVersion
		return &diagnostic
	}
	var revisionConflict *todoRevisionConflict
	if errors.As(err, &revisionConflict) {
		revision := revisionConflict.Revision
		return &TeachDiagnostic{SchemaVersion: teachDiagnosticSchemaVersion, Code: "todo_revision_conflict", FieldPath: "todo_revision", NextAction: "read the TODO at the reported revision and reassess the request before submitting again", Revision: &revision, Retry: TeachRetryReadReassess}
	}
	return defaultRegistryDiagnostic(publicCode)
}

func designSystemErrorDiagnostic(err error, operationID string, recovery bool) TeachDiagnostic {
	publicCode := ErrUnavailable.Error()
	switch {
	case errors.Is(err, ErrInvalid):
		publicCode = ErrInvalid.Error()
	case errors.Is(err, ErrNotFound):
		publicCode = ErrNotFound.Error()
	case errors.Is(err, ErrConflict):
		publicCode = ErrConflict.Error()
	}
	base := registryErrorDiagnostic(err, publicCode)
	detail := *base
	if recovery {
		if detail.Code == "registry_unavailable" {
			detail.Code = "design_system_recovery_uncertain"
			detail.FieldPath = "operation_id"
			detail.NextAction = "Keep this operation ID; read current state and recover it again if this operation remains pending. Do not start a replacement mutation."
		}
	} else if detail.Code == "registry_unavailable" {
		detail.Code = "design_system_mutation_uncertain"
		detail.FieldPath = "operation"
		detail.NextAction = "Read current Design System state; if this operation ID is pending, recover it. Do not start a replacement mutation while the outcome is uncertain."
	}
	if operationID != "" && detail.NextAction != "" && detail.Code != "design_system_mutation_uncertain" && detail.Code != "design_system_recovery_uncertain" {
		if recovery {
			detail.NextAction += "; read current state and recover this operation ID if it remains pending."
		} else {
			detail.NextAction += "; read current Design System state and recover this ID only if it is the pending operation."
		}
	}
	detail.SchemaVersion = teachDiagnosticSchemaVersion
	detail.OperationID = operationID
	detail.Effects = TeachEffectsUnknown
	if recovery {
		detail.Retry = TeachRetryRecoverOperation
	} else {
		detail.Retry = TeachRetryReadReassess
	}
	return detail
}

func mutationErrorDiagnostic(err error, operationID string) TeachDiagnostic {
	if explicit := explicitTeachDiagnostic(err); explicit != nil {
		return *explicit
	}
	publicCode := ErrUnavailable.Error()
	switch {
	case errors.Is(err, ErrInvalid):
		publicCode = ErrInvalid.Error()
	case errors.Is(err, ErrNotFound):
		publicCode = ErrNotFound.Error()
	case errors.Is(err, ErrConflict):
		publicCode = ErrConflict.Error()
	}
	detail := registryErrorDiagnostic(err, publicCode)
	if detail.Code == "registry_unavailable" {
		detail.Code = "registry_mutation_uncertain"
		detail.FieldPath = "operation"
		detail.NextAction = "Read current Registry state and reassess before retrying; the operation outcome is not confirmed."
	}
	detail.SchemaVersion = teachDiagnosticSchemaVersion
	detail.Effects = TeachEffectsUnknown
	if operationID != "" {
		detail.OperationID = operationID
	}
	return *detail
}

const teachDiagnosticSchemaVersion = "1"

const (
	TeachEffectsNone          = "none"
	TeachEffectsCommitted     = "committed"
	TeachEffectsUnknown       = "unknown"
	TeachEffectsNotApplicable = "not_applicable"

	TeachRetryNone             = "none"
	TeachRetrySameOperation    = "same_operation"
	TeachRetryReadReassess     = "read_reassess"
	TeachRetryRestoreService   = "restore_service"
	TeachRetryRecoverOperation = "recover_operation"
)

type promotionAlternative struct {
	Branch                    string `json:"branch"`
	Exists                    *bool  `json:"exists"`
	CommitReachable           *bool  `json:"commit_reachable"`
	OwnerConfirmationRequired bool   `json:"owner_confirmation_required"`
}

type promotionRepositoryDiagnostic struct {
	RepositoryID   string                 `json:"repository_id"`
	CommitSHA      string                 `json:"sha,omitempty"`
	TargetBranch   string                 `json:"target_branch,omitempty"`
	Status         string                 `json:"status"`
	SourceBranch   string                 `json:"source_branch,omitempty"`
	ExpectedNodeID string                 `json:"expected_node_id,omitempty"`
	Alternatives   []promotionAlternative `json:"alternatives,omitempty"`
	NextAction     string                 `json:"next_action,omitempty"`
}

type promotionDiagnostic struct {
	RequestedStage string                          `json:"requested_stage,omitempty"`
	Repositories   []promotionRepositoryDiagnostic `json:"repositories"`
}

// TeachDiagnostic is the shared, additive diagnostic carried by eligible JSON
// responses. Effects and retry remain empty unless the owning operation can
// prove them; this transport layer does not infer either from an error code.
type TeachDiagnostic struct {
	Promotion      *promotionDiagnostic `json:"promotion,omitempty"`
	SchemaVersion  string               `json:"schema_version"`
	Code           string               `json:"code"`
	FieldPath      string               `json:"field_path"`
	RecordID       string               `json:"record_id,omitempty"`
	AcceptedValues []string             `json:"accepted_values,omitempty"`
	NextAction     string               `json:"next_action"`
	ObjectPurpose  string               `json:"object_purpose,omitempty"`
	RequiredFields []string             `json:"required_fields,omitempty"`
	AcceptedFields []string             `json:"accepted_fields,omitempty"`
	NotPersisted   []string             `json:"not_persisted_fields,omitempty"`
	OperationID    string               `json:"operation_id,omitempty"`
	Revision       *int64               `json:"revision,omitempty"`
	Effects        string               `json:"effects,omitempty"`
	Retry          string               `json:"retry,omitempty"`
}

// normalizedForResponse enforces the diagnostic text/list bounds. Envelope
// size is checked separately after all legacy and v1 fields are serialized.
func (d TeachDiagnostic) normalizedForResponse() TeachDiagnostic {
	d.SchemaVersion = teachDiagnosticSchemaVersion
	if d.Code == "" {
		d.Code = "registry_diagnostic"
	}
	if d.NextAction == "" {
		d.NextAction = "Review the request and consult the owning Registry operation before proceeding."
	}
	return d
}

func safeTeachText(value, fallback string) string {
	if len(value) <= 2<<10 && utf8.ValidString(value) {
		return value
	}
	return fallback
}

func (d TeachDiagnostic) withinFieldBounds() bool {
	if d.Promotion != nil {
		serialized, err := json.Marshal(d.Promotion)
		if err != nil || len(serialized) > 24<<10 || len(d.Promotion.Repositories) > 32 {
			return false
		}
	}
	for _, value := range []string{d.Code, d.FieldPath, d.RecordID, d.NextAction, d.ObjectPurpose, d.OperationID} {
		if len(value) > 2<<10 || !utf8.ValidString(value) {
			return false
		}
	}
	for _, values := range [][]string{d.AcceptedValues, d.RequiredFields, d.AcceptedFields, d.NotPersisted} {
		if len(values) > 64 {
			return false
		}
		for _, value := range values {
			if len(value) > 2<<10 || !utf8.ValidString(value) {
				return false
			}
		}
	}
	return (d.Effects == "" || validTeachEffect(d.Effects)) && (d.Retry == "" || validTeachRetry(d.Retry))
}
