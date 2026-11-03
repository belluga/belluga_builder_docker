package registry

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"sort"
	"strings"
)

// Record slugs and owner-local decision identities are intentionally different.
// Uppercase IDs already exist in the canonical corpus and must survive verbatim.
var canonicalSemanticID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
var canonicalFieldName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]{0,255}$`)

func decodeCanonicalPayload(recordID string, payload json.RawMessage, target any) error {
	if len(bytes.TrimSpace(payload)) == 0 || bytes.TrimSpace(payload)[0] != '{' {
		return teachCanonical(recordID, "payload", "provide one JSON object matching the family schema", ErrInvalid)
	}
	if err := rejectCanonicalDuplicateKeys(recordID, payload, "payload"); err != nil {
		return err
	}
	if err := checkCanonicalUnknownFields(recordID, payload, reflect.TypeOf(target), "payload", 0); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		field := "payload"
		if name, ok := strings.CutPrefix(err.Error(), `json: unknown field "`); ok {
			unknown := strings.TrimSuffix(name, `"`)
			field += "." + unknown
			teach := teachCanonical(recordID, field, "remove the unknown field, use the accepted typed fields for this object and resubmit; unaccepted fields are not persisted", ErrInvalid).(*canonicalTeachError)
			kind := reflect.TypeOf(target)
			for kind.Kind() == reflect.Pointer {
				kind = kind.Elem()
			}
			guide := canonicalObjectGuide(kind)
			teach.Diagnostic.ObjectPurpose = guide["purpose"].(string)
			teach.Diagnostic.RequiredFields = guide["required_fields"].([]string)
			for accepted := range guide["accepted_fields"].(map[string]string) {
				teach.Diagnostic.AcceptedFields = append(teach.Diagnostic.AcceptedFields, accepted)
			}
			sort.Strings(teach.Diagnostic.AcceptedFields)
			teach.Diagnostic.NotPersisted = []string{unknown}
			return teach
		}
		var typeError *json.UnmarshalTypeError
		if errors.As(err, &typeError) && typeError.Field != "" {
			field += "." + typeError.Field
		}
		return teachCanonical(recordID, field, "use the declared JSON field type in the family schema", ErrInvalid)
	}
	if decoder.Decode(new(any)) != io.EOF {
		return teachCanonical(recordID, "payload", "provide exactly one JSON object without trailing values", ErrInvalid)
	}
	return nil
}

func checkCanonicalUnknownFields(recordID string, raw json.RawMessage, kind reflect.Type, path string, depth int) error {
	if depth > 32 {
		return teachCanonical(recordID, path, "limit nested canonical objects to 32 levels", ErrInvalid)
	}
	for kind.Kind() == reflect.Pointer {
		kind = kind.Elem()
	}
	if kind.Kind() == reflect.Slice {
		var items []json.RawMessage
		if json.Unmarshal(raw, &items) != nil {
			return nil
		}
		for i, item := range items {
			if err := checkCanonicalUnknownFields(recordID, item, kind.Elem(), fmt.Sprintf("%s[%d]", path, i), depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if kind.Kind() != reflect.Struct || len(raw) == 0 || raw[0] != '{' {
		return nil
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return nil
	}
	allowed := map[string]reflect.Type{}
	var gather func(reflect.Type)
	gather = func(t reflect.Type) {
		for i := 0; i < t.NumField(); i++ {
			field := t.Field(i)
			if field.Anonymous {
				gather(field.Type)
				continue
			}
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if name != "" && name != "-" {
				allowed[name] = field.Type
			}
		}
	}
	gather(kind)
	keys := make([]string, 0, len(fields))
	for name := range fields {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	for _, name := range keys {
		fieldType, ok := allowed[name]
		if !ok {
			teach := teachCanonical(recordID, path+"."+name, "remove the unknown field, use the accepted typed fields for this object and resubmit; unaccepted fields are not persisted", ErrInvalid).(*canonicalTeachError)
			guide := canonicalObjectGuide(kind)
			teach.Diagnostic.ObjectPurpose = guide["purpose"].(string)
			teach.Diagnostic.RequiredFields = guide["required_fields"].([]string)
			for accepted := range guide["accepted_fields"].(map[string]string) {
				teach.Diagnostic.AcceptedFields = append(teach.Diagnostic.AcceptedFields, accepted)
			}
			sort.Strings(teach.Diagnostic.AcceptedFields)
			teach.Diagnostic.NotPersisted = []string{name}
			return teach
		}
		if err := checkCanonicalUnknownFields(recordID, fields[name], fieldType, path+"."+name, depth+1); err != nil {
			return err
		}
	}
	return nil
}

// encoding/json accepts repeated object keys by keeping the last value. Reject
// them before typed decoding so migrations cannot silently lose authored data.
func rejectCanonicalDuplicateKeys(recordID string, raw []byte, root string) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var visit func(string) error
	visit = func(field string) error {
		token, err := decoder.Token()
		if err != nil {
			return teachCanonical(recordID, field, "provide valid JSON", ErrInvalid)
		}
		switch token {
		case json.Delim('{'):
			seen := map[string]bool{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return teachCanonical(recordID, field, "provide a JSON object with string keys", ErrInvalid)
				}
				key, ok := keyToken.(string)
				if !ok {
					return teachCanonical(recordID, field, "provide string object keys", ErrInvalid)
				}
				if seen[key] {
					return teachCanonical(recordID, field+"."+key, "remove the duplicate JSON key; provide each field exactly once", ErrInvalid)
				}
				seen[key] = true
				if err := visit(field + "." + key); err != nil {
					return err
				}
			}
			if _, err := decoder.Token(); err != nil {
				return teachCanonical(recordID, field, "close the JSON object", ErrInvalid)
			}
		case json.Delim('['):
			for index := 0; decoder.More(); index++ {
				if err := visit(fmt.Sprintf("%s[%d]", field, index)); err != nil {
					return err
				}
			}
			if _, err := decoder.Token(); err != nil {
				return teachCanonical(recordID, field, "close the JSON array", ErrInvalid)
			}
		}
		return nil
	}
	if err := visit(root); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return teachCanonical(recordID, root, "provide exactly one JSON value", ErrInvalid)
	}
	return nil
}

type canonicalValidator struct {
	recordID string
	err      error
}

func (v *canonicalValidator) check(ok bool, field, action string) {
	if !ok && v.err == nil {
		v.err = teachCanonical(v.recordID, field, action, ErrInvalid)
	}
}
func (v *canonicalValidator) text(value, field string) {
	v.check(strings.TrimSpace(value) != "", field, "provide a non-empty value")
}
func (v *canonicalValidator) choice(value, field string, choices ...string) {
	for _, choice := range choices {
		if value == choice {
			return
		}
	}
	if v.err == nil {
		v.err = &canonicalTeachError{Diagnostic: CanonicalTeachDiagnostic{Code: "canonical_validation", RecordID: v.recordID, FieldPath: field, AcceptedValues: choices, NextAction: "use one of: " + strings.Join(choices, ", ")}, Cause: ErrInvalid}
	}
}
func (v *canonicalValidator) identity(id, field string, seen map[string]int) {
	v.check(canonicalSemanticID.MatchString(id), field, "preserve the stable semantic ID (including uppercase); use letters, digits, dot, underscore or hyphen")
	if previous, ok := seen[id]; ok {
		v.check(false, field, fmt.Sprintf("duplicate identity %s also occurs at index %d; obtain an explicit owner correction before import; do not overwrite or suffix it", id, previous))
	}
	seen[id] = len(seen)
}
func (v *canonicalValidator) state(value CanonicalKnowledgeState) {
	if value.Status != "" {
		v.choice(value.Status, "payload.status", "current", "planned", "draft", "deprecated", "historical")
	}
	seen := map[string]int{}
	for i, item := range value.OpenQuestions {
		p := fmt.Sprintf("payload.open_questions[%d]", i)
		v.identity(item.ID, p+".id", seen)
		v.text(item.Question, p+".question")
		v.choice(item.Status, p+".status", "open", "resolved", "deferred")
		v.references(item.Evidence, p+".evidence")
	}
}
func (v *canonicalValidator) references(items []CanonicalReference, field string) {
	for i, item := range items {
		p := fmt.Sprintf("%s[%d]", field, i)
		v.text(item.Kind, p+".kind")
		v.text(item.Ref, p+".ref")
	}
}
func (v *canonicalValidator) decisions(items []CanonicalDecision) {
	seen := map[string]int{}
	for i, item := range items {
		p := fmt.Sprintf("payload.decisions[%d]", i)
		v.identity(item.ID, p+".id", seen)
		v.text(item.Statement, p+".statement")
		if item.Status != "" {
			v.choice(item.Status, p+".status", "current", "planned", "draft", "deprecated", "historical")
		}
		v.references(item.Evidence, p+".evidence")
	}
}
func (v *canonicalValidator) rules(items []CanonicalRule, field string, required bool) {
	v.check(!required || len(items) > 0, field, "provide at least one typed rule")
	seen := map[string]int{}
	for i, item := range items {
		p := fmt.Sprintf("%s[%d]", field, i)
		v.identity(item.ID, p+".id", seen)
		v.text(item.Statement, p+".statement")
		if item.Status != "" {
			v.choice(item.Status, p+".status", "current", "planned", "draft", "deprecated", "historical")
		}
	}
}
func (v *canonicalValidator) namedTexts(items []CanonicalNamedText, field string) {
	for i, item := range items {
		p := fmt.Sprintf("%s[%d]", field, i)
		v.text(item.Name, p+".name")
		v.text(item.Text, p+".text")
	}
}
func (v *canonicalValidator) processes(items []CanonicalProcess, field string) {
	for i, item := range items {
		p := fmt.Sprintf("%s[%d]", field, i)
		v.text(item.Name, p+".name")
		v.check(len(item.Steps) > 0, p+".steps", "provide ordered process steps")
		v.namedTexts(item.Steps, p+".steps")
	}
}
func (v *canonicalValidator) conditions(items []CanonicalCondition, field string) {
	for i, item := range items {
		p := fmt.Sprintf("%s[%d]", field, i)
		v.text(item.Field, p+".field")
		v.choice(item.Operator, p+".operator", "eq", "ne", "in", "exists", "gt", "gte", "lt", "lte")
		v.check(item.Operator == "exists" || len(item.Values) > 0, p+".values", "provide comparison values")
	}
}
func (v *canonicalValidator) fields(items []CanonicalField, field string, depth int, flags bool) {
	v.check(depth <= 12, field, "limit recursive schema nesting to 12 levels")
	if v.err != nil {
		return
	}
	seen := map[string]int{}
	for i, item := range items {
		p := fmt.Sprintf("%s[%d]", field, i)
		name := item.Name
		if flags {
			name = strings.TrimPrefix(name, "--")
		}
		v.check(canonicalFieldName.MatchString(name), p+".name", "use a declared field name (CLI flags may start with --)")
		if _, ok := seen[item.Name]; ok {
			v.check(false, p+".name", "remove the duplicate field name; do not silently overwrite it")
		}
		seen[item.Name] = i
		v.choice(item.Type, p+".type", "string", "integer", "number", "boolean", "object", "array", "object_id", "datetime", "binary", "null", "union")
		v.check(len(item.Fields) == 0 || item.Type == "object", p+".fields", "nested fields require type object")
		v.check(item.Items == nil || item.Type == "array", p+".items", "array items require type array")
		v.check(item.Type != "array" || item.Items != nil || item.SchemaRef != "", p+".items", "provide the array item type or an explicit schema_ref")
		v.check(item.SchemaRef == "" || (len(item.Fields) == 0 && item.Items == nil), p+".schema_ref", "use either an explicit schema reference or an inline schema")
		v.check(item.Type == "union" && len(item.OneOf) >= 2 || item.Type != "union" && len(item.OneOf) == 0, p+".one_of", "declare at least two alternatives only for a union field")
		for j, alternative := range item.OneOf {
			v.fields([]CanonicalField{alternative}, fmt.Sprintf("%s.one_of[%d]", p, j), depth+1, false)
		}
		if item.Pattern != "" {
			_, err := regexp.Compile(item.Pattern)
			v.check(err == nil, p+".pattern", "provide a valid regular expression")
		}
		if item.Minimum != nil && item.Maximum != nil {
			v.check(*item.Minimum <= *item.Maximum, p+".maximum", "maximum must be at least minimum")
		}
		for _, bounds := range []struct {
			min, max *int
			name     string
		}{{item.MinLength, item.MaxLength, "length"}, {item.MinItems, item.MaxItems, "items"}} {
			v.check(bounds.min == nil || *bounds.min >= 0, p+".min_"+bounds.name, "provide a non-negative bound")
			v.check(bounds.max == nil || *bounds.max >= 0, p+".max_"+bounds.name, "provide a non-negative bound")
			v.check(bounds.min == nil || bounds.max == nil || *bounds.min <= *bounds.max, p+".max_"+bounds.name, "maximum must be at least minimum")
		}
		if item.Location != "" {
			if flags {
				v.choice(item.Location, p+".location", "flag")
			} else {
				v.choice(item.Location, p+".location", "body", "query", "path", "header")
			}
		}
		v.conditions(item.RequiredWhen, p+".required_when")
		v.fields(item.Fields, p+".fields", depth+1, false)
		if item.Items != nil {
			v.fields([]CanonicalField{*item.Items}, p+".items", depth+1, false)
		}
	}
}
func (v *canonicalValidator) routes(items []CanonicalRouteOwnership, field string) {
	for i, item := range items {
		p := fmt.Sprintf("%s[%d]", field, i)
		v.check(strings.HasPrefix(item.Route, "/"), p+".route", "provide an absolute product route")
		v.text(item.HostScope, p+".host_scope")
		v.text(item.Guard, p+".guard")
	}
}
func (v *canonicalValidator) pagination(value *CanonicalPagination, field string) {
	if value == nil {
		return
	}
	v.choice(value.Mode, field+".mode", "page", "cursor", "window", "none")
	v.fields(value.RequestFields, field+".request_fields", 0, false)
	v.fields(value.ResponseFields, field+".response_fields", 0, false)
	v.check(value.DefaultSize == nil || *value.DefaultSize > 0, field+".default_size", "provide a positive page size")
	v.check(value.MaxSize == nil || *value.MaxSize > 0, field+".max_size", "provide a positive page size")
	v.check(value.DefaultSize == nil || value.MaxSize == nil || *value.DefaultSize <= *value.MaxSize, field+".max_size", "maximum must be at least default_size")
	v.indexKeys(value.Order, field+".order")
}
func (v *canonicalValidator) security(value *CanonicalSecurity, field string) {
	if value == nil {
		return
	}
	if value.Level != "" {
		v.choice(value.Level, field+".level", "L1", "L2", "L3")
	}
	v.rules(value.Requirements, field+".requirements", false)
}
func (v *canonicalValidator) idempotency(value *CanonicalIdempotency, field string) {
	if value == nil {
		return
	}
	v.check(!value.Required || value.Header != "", field+".header", "provide the required idempotency header")
	v.check(value.ReplayWindowSeconds == nil || *value.ReplayWindowSeconds > 0, field+".replay_window_seconds", "provide a positive replay window")
	v.check(value.ConflictStatusCode == 0 || (value.ConflictStatusCode >= 400 && value.ConflictStatusCode <= 599), field+".conflict_status_code", "provide an HTTP rejection status")
	v.rules(value.Requirements, field+".requirements", false)
}
func (v *canonicalValidator) patch(value *CanonicalPatch, field string) {
	if value == nil {
		return
	}
	v.choice(value.OmittedFields, field+".omitted_fields", "unchanged", "rejected")
	v.choice(value.NullFields, field+".null_fields", "clear_nullable", "rejected")
}
func (v *canonicalValidator) indexKeys(items []CanonicalIndexKey, field string) {
	seen := map[string]bool{}
	for i, item := range items {
		p := fmt.Sprintf("%s[%d]", field, i)
		v.text(item.Field, p+".field")
		v.check(!seen[item.Field], p+".field", "remove duplicate index keys")
		seen[item.Field] = true
		v.choice(item.Order, p+".order", "asc", "desc", "2dsphere", "text", "hashed")
	}
}

func validateCanonicalPayload(recordID string, target any) error {
	v := &canonicalValidator{recordID: recordID}
	switch value := target.(type) {
	case *MandatePayload:
		v.state(value.CanonicalKnowledgeState)
		v.text(value.Purpose, "payload.purpose")
		v.check(len(value.Scope) > 0, "payload.scope", "provide at least one scope item")
		if value.WorkingNameState != "" {
			v.choice(value.WorkingNameState, "payload.working_name_state", "provisional", "current", "historical")
		}
	case *DomainCatalogPayload:
		v.state(value.CanonicalKnowledgeState)
		v.check(len(value.Entities) > 0, "payload.entities", "provide at least one typed entity")
		seen := map[string]int{}
		for i, item := range value.Entities {
			p := fmt.Sprintf("payload.entities[%d]", i)
			v.identity(item.ID, p+".id", seen)
			v.text(item.Name, p+".name")
		}
		relations := map[string]int{}
		for i, item := range value.Relations {
			p := fmt.Sprintf("payload.relations[%d]", i)
			v.identity(item.ID, p+".id", relations)
			_, from := seen[item.From]
			_, to := seen[item.To]
			v.check(from && to, p, "reference declared entity IDs")
			v.text(item.Kind, p+".kind")
		}
		v.rules(value.RelationshipRules, "payload.relationship_rules", false)
		distinctions := map[string]int{}
		for i, item := range value.Distinctions {
			p := fmt.Sprintf("payload.distinctions[%d]", i)
			v.identity(item.ID, p+".id", distinctions)
			v.check(len(item.Terms) >= 2, p+".terms", "name at least two terms in this distinction")
			for j, term := range item.Terms {
				v.text(term, fmt.Sprintf("%s.terms[%d]", p, j))
			}
			v.text(item.Reason, p+".reason")
		}
		if value.QAValidationOutline != nil {
			v.text(value.QAValidationOutline.Applicability, "payload.qa_validation_outline.applicability")
			v.namedTexts(value.QAValidationOutline.Fields, "payload.qa_validation_outline.fields")
			v.namedTexts(value.QAValidationOutline.Definitions, "payload.qa_validation_outline.definitions")
		}
	case *ConstitutionPayload:
		v.state(value.CanonicalKnowledgeState)
		v.choice(value.PromotionMode, "payload.promotion_mode", "mvp", "complete")
		v.check(len(value.CapabilityNamespaces) > 0, "payload.capability_namespaces", "declare active project capability namespaces")
		seen := map[string]bool{}
		for i, item := range value.CapabilityNamespaces {
			p := fmt.Sprintf("payload.capability_namespaces[%d]", i)
			v.check(canonicalID.MatchString(item) && !seen[item], p, "use unique lowercase capability namespace identifiers")
			seen[item] = true
		}
		v.rules(value.Rules, "payload.rules", true)
		v.decisions(value.Decisions)
		v.namedTexts(value.AuthorityBoundaries, "payload.authority_boundaries")
		v.namedTexts(value.ControlPlaneResponsibilities, "payload.control_plane_responsibilities")
		v.processes(value.PublicationFlow, "payload.publication_flow")
		v.namedTexts(value.SynchronizationOutcomes, "payload.synchronization_outcomes")
		v.namedTexts(value.FreshnessStates, "payload.freshness_states")
		v.references(value.ModuleBoundaryRefs, "payload.module_boundary_refs")
	case *StrategicRoadmapPayload:
		v.state(value.CanonicalKnowledgeState)
		v.check(len(value.Stages) > 0, "payload.stages", "provide at least one strategic stage")
		seen := map[string]int{}
		for i, item := range value.Stages {
			p := fmt.Sprintf("payload.stages[%d]", i)
			v.identity(item.ID, p+".id", seen)
			v.text(item.Name, p+".name")
			if item.Status != "" {
				v.choice(item.Status, p+".status", "current", "planned", "draft", "deprecated", "historical")
			}
		}
		v.namedTexts(value.EpicCandidates, "payload.epic_candidates")
		v.namedTexts(value.DeferredOptions, "payload.deferred_options")
	case *ModulePayload:
		v.state(value.CanonicalKnowledgeState)
		v.text(value.Purpose, "payload.purpose")
		v.decisions(value.Decisions)
		v.namedTexts(value.Coverage, "payload.coverage")
		v.processes(value.Processes, "payload.processes")
		v.fields(value.Interfaces, "payload.interfaces", 0, false)
		v.rules(value.Workflows, "payload.workflows", false)
		v.rules(value.Invariants, "payload.invariants", false)
		v.routes(value.Routes, "payload.routes")
		for i, item := range value.Dependencies {
			p := fmt.Sprintf("payload.dependencies[%d]", i)
			v.text(item.Kind, p+".kind")
			v.text(item.Ref, p+".ref")
			v.text(item.Responsibility, p+".responsibility")
		}
		v.module(value)
	case *CanonicalEndpointPayload:
		v.state(value.CanonicalKnowledgeState)
		v.choice(value.Method, "payload.method", "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS")
		v.check(strings.HasPrefix(value.Route, "/") && !strings.ContainsAny(value.Route, "?#"), "payload.route", "provide a normalized absolute HTTP route without query or fragment")
		v.text(value.HostScope, "payload.host_scope")
		v.check(canonicalID.MatchString(value.ModuleID), "payload.module_id", "provide the owning module record ID")
		v.fields(value.Request, "payload.request", 0, false)
		v.fields(value.Response, "payload.response", 0, false)
		v.rules(value.BehaviorRules, "payload.behavior_rules", false)
		v.rules(value.Errors, "payload.errors", false)
		v.security(value.Security, "payload.security")
		v.pagination(value.Pagination, "payload.pagination")
		v.idempotency(value.Idempotency, "payload.idempotency")
		v.patch(value.Patch, "payload.patch")
		for i, item := range value.Responses {
			p := fmt.Sprintf("payload.responses[%d]", i)
			v.check(item.StatusCode >= 100 && item.StatusCode <= 599, p+".status_code", "provide an HTTP response status")
			v.text(item.Description, p+".description")
			v.fields(item.Fields, p+".fields", 0, false)
			v.fields(item.Headers, p+".headers", 0, false)
		}
	case *PolicyPayload:
		v.state(value.CanonicalKnowledgeState)
		v.rules(value.Rules, "payload.rules", true)
		v.decisions(value.Decisions)
		v.namedTexts(value.ScopeConcepts, "payload.scope_concepts")
		v.namedTexts(value.FutureSurfaceDeclarations, "payload.future_surface_declarations")
		if value.EnvironmentTypeState != "" {
			v.choice(value.EnvironmentTypeState, "payload.environment_type_state", "undefined", "defined")
		}
		v.fields(value.Definitions, "payload.definitions", 0, false)
		v.routes(value.Routes, "payload.routes")
		for i, item := range value.Ownership {
			p := fmt.Sprintf("payload.ownership[%d]", i)
			v.choice(item.OwnerKind, p+".owner_kind", "module", "project_constitution", "policy")
			v.check(canonicalID.MatchString(item.OwnerID), p+".owner_id", "provide the stable owner record ID")
			v.check(item.DecisionID == "" || canonicalSemanticID.MatchString(item.DecisionID), p+".decision_id", "preserve the owner decision ID verbatim")
		}
		if c := value.EndpointConventions; c != nil {
			v.check(strings.HasPrefix(c.BasePath, "/"), "payload.endpoint_conventions.base_path", "provide an absolute API prefix")
			v.pagination(c.Pagination, "payload.endpoint_conventions.pagination")
			v.security(c.Security, "payload.endpoint_conventions.security")
			v.idempotency(c.Idempotency, "payload.endpoint_conventions.idempotency")
			v.patch(c.Patch, "payload.endpoint_conventions.patch")
		}
	}
	return v.err
}

func (v *canonicalValidator) module(value *ModulePayload) {
	schemas := map[string]int{}
	for i, item := range value.Schemas {
		p := fmt.Sprintf("payload.schemas[%d]", i)
		v.identity(item.ID, p+".id", schemas)
		v.text(item.Name, p+".name")
		v.choice(item.Kind, p+".kind", "collection", "request", "response", "projection", "configuration", "artifact")
		v.check(len(item.Fields) > 0, p+".fields", "provide typed schema fields")
		v.fields(item.Fields, p+".fields", 0, false)
		v.rules(item.Invariants, p+".invariants", false)
		indexes := map[string]int{}
		for j, index := range item.Indexes {
			q := fmt.Sprintf("%s.indexes[%d]", p, j)
			v.identity(index.ID, q+".id", indexes)
			v.check(len(index.Keys) > 0, q+".keys", "provide ordered index keys")
			v.indexKeys(index.Keys, q+".keys")
			v.check(index.ExpireAfterSeconds == nil || *index.ExpireAfterSeconds >= 0, q+".expire_after_seconds", "provide a non-negative TTL")
			v.conditions(index.PartialFilter, q+".partial_filter")
		}
	}
	operations := map[string]int{}
	for i, item := range value.LocalOperations {
		p := fmt.Sprintf("payload.local_operations[%d]", i)
		v.identity(item.ID, p+".id", operations)
		v.text(item.Command, p+".command")
		v.fields(item.Flags, p+".flags", 0, true)
		v.fields(item.Response, p+".response", 0, false)
		v.fields(item.Limits, p+".limits", 0, false)
		v.rules(item.Requirements, p+".requirements", false)
		v.check(len(item.ExitCodes) > 0, p+".exit_codes", "provide explicit exit-code outcomes")
		exits := map[int]bool{}
		for j, exit := range item.ExitCodes {
			q := fmt.Sprintf("%s.exit_codes[%d]", p, j)
			v.check(exit.Code >= 0 && exit.Code <= 255 && !exits[exit.Code], q+".code", "use unique exit codes in 0..255")
			exits[exit.Code] = true
			v.text(exit.Outcome, q+".outcome")
			v.text(exit.ConsumerAction, q+".consumer_action")
		}
		if item.Digest != nil {
			v.text(item.Digest.Algorithm, p+".digest.algorithm")
			v.check(len(item.Digest.Fields) > 0, p+".digest.fields", "declare the digest input fields")
		}
	}
	events := map[string]int{}
	for i, item := range value.Events {
		p := fmt.Sprintf("payload.events[%d]", i)
		v.identity(item.Name, p+".name", events)
		v.choice(item.Direction, p+".direction", "inbound", "outbound")
		v.fields(item.Payload, p+".payload", 0, false)
		v.rules(item.Requirements, p+".requirements", false)
	}
	jobs := map[string]int{}
	for i, item := range value.Jobs {
		p := fmt.Sprintf("payload.jobs[%d]", i)
		v.identity(item.ID, p+".id", jobs)
		v.text(item.Handler, p+".handler")
		v.text(item.Schedule, p+".schedule")
	}
	observations := map[string]int{}
	for i, item := range value.Observability {
		p := fmt.Sprintf("payload.observability[%d]", i)
		v.identity(item.ID, p+".id", observations)
		v.choice(item.Kind, p+".kind", "log", "metric", "trace", "alert", "telemetry")
		v.text(item.Name, p+".name")
		v.fields(item.Fields, p+".fields", 0, false)
		if item.Comparison != "" {
			v.choice(item.Comparison, p+".comparison", "eq", "gt", "gte", "lt", "lte")
			v.check(item.Threshold != nil, p+".threshold", "provide a numeric threshold for the comparison")
		}
	}
	verification := map[string]int{}
	for i, item := range value.Verification {
		p := fmt.Sprintf("payload.verification[%d]", i)
		v.identity(item.ID, p+".id", verification)
		v.text(item.Layer, p+".layer")
		v.text(item.Requirement, p+".requirement")
	}
}
