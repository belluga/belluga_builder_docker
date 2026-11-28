package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"unicode/utf8"
)

type failingRoadmapService struct {
	*testService
	err error
}

type ambiguousTransitionService struct {
	testService
	err error
}

func (s *ambiguousTransitionService) TransitionTODO(context.Context, string, TODOActionRequest) (TODOActionResponse, error) {
	return TODOActionResponse{}, s.err
}

func TestTeachSchemaV1IsValidAndMatchesDiagnosticBounds(t *testing.T) {
	schema := loadTeachSchema(t)
	if schema["additionalProperties"] != false {
		t.Fatalf("schema must reject unrecognized fields: %#v", schema)
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok || properties["category"] != nil {
		t.Fatalf("unexpected TEACH v1 properties: %#v", properties)
	}
	if !readTeach("schema_probe", "field", "safe action").withinFieldBounds() {
		t.Fatal("normal owner-created diagnostic rejected its contract bounds")
	}
	instances := []TeachDiagnostic{
		*readTeach("canonical_pending", "generation_id", "read the manifest and import the missing record"),
		mutationErrorDiagnostic(canonicalPending(), "ambiguous-op"),
		mutationErrorDiagnostic(canonicalPrewriteError(teachCanonical("record-a", "payload.name", "provide a valid name", ErrInvalid), "prewrite-op"), "prewrite-op"),
	}
	for i, detail := range instances {
		encoded, marshalErr := json.Marshal(detail.normalizedForResponse())
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		var instance map[string]any
		if json.Unmarshal(encoded, &instance) != nil || validateTeachSchemaInstance(schema, instance) != nil {
			t.Fatalf("emitted diagnostic %d does not satisfy the declared schema: %s", i, encoded)
		}
	}
	roadmapResponses := []*httptest.ResponseRecorder{
		serveRoadmapFailure(todoTeach("request.name", "provide a name", ErrInvalid)),
		serveRoadmapFailure(&todoRevisionConflict{Revision: 9}),
	}
	pending := httptest.NewRecorder()
	NewHandler(&pendingCanonicalHTTPService{}).ServeHTTP(pending, httptest.NewRequest("GET", "/internal/v1/companies/company-a/projects/builder/knowledge/project_mandate/builder", nil))
	roadmapResponses = append(roadmapResponses, pending)
	session := httptest.NewRecorder()
	NewHandler(&sessionHTTPService{}).ServeHTTP(session, httptest.NewRequest(http.MethodPost, "/internal/v1/sessions/bind", strings.NewReader(`{"type":"codex","external_id":"conversation","company_id":"company-a","project_id":"project-a","expected_revision":0,"operation_id":"session-op","owner_reference":"owner"}`)))
	roadmapResponses = append(roadmapResponses, session)
	mutation := httptest.NewRecorder()
	NewHandler(&ambiguousTransitionService{testService: testService{}, err: ErrInvalid}).ServeHTTP(mutation, httptest.NewRequest(http.MethodPost, "/internal/v1/companies/company-a/projects/project-a/todos/todo-a/transition", strings.NewReader(`{"project_id":"project-a","todo_id":"todo-a","operation_id":"ambiguous-op","requested_stage":"Planning"}`)))
	roadmapResponses = append(roadmapResponses, mutation)
	for i, response := range roadmapResponses {
		var body errorResponse
		if json.Unmarshal(response.Body.Bytes(), &body) != nil || body.TeachDetail == nil {
			t.Fatalf("real HTTP error path %d omitted TEACH detail: %s", i, response.Body.String())
		}
		assertTeachDetailMatchesSchema(t, schema, *body.TeachDetail)
	}
	for _, invalid := range []map[string]any{
		{"schema_version": "1", "code": strings.Repeat("x", 2049), "next_action": "safe"},
		{"schema_version": "1", "code": "safe"},
		{"schema_version": "1", "code": "safe", "next_action": "safe", "effects": "success-ish"},
		{"schema_version": "1", "code": "safe", "next_action": "safe", "unexpected": true},
		{"schema_version": "1", "code": "safe", "next_action": "safe", "accepted_values": make([]any, 65)},
		{"schema_version": "1", "code": "safe", "next_action": 42},
	} {
		if err := validateTeachSchemaInstance(schema, invalid); err == nil {
			t.Fatalf("schema instance oracle accepted invalid instance: %#v", invalid)
		}
	}
}

func loadTeachSchema(t *testing.T) map[string]any {
	t.Helper()
	data, err := os.ReadFile("teach_diagnostic.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatalf("invalid schema JSON: %v", err)
	}
	return schema
}

func assertTeachDetailMatchesSchema(t *testing.T, schema map[string]any, detail TeachDiagnostic) {
	t.Helper()
	encoded, err := json.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	var instance map[string]any
	if err := json.Unmarshal(encoded, &instance); err != nil {
		t.Fatal(err)
	}
	if err := validateTeachSchemaInstance(schema, instance); err != nil {
		t.Fatalf("real HTTP teach_detail violates schema: %s: %v", encoded, err)
	}
}

func validateTeachSchemaInstance(schema, instance map[string]any) error {
	properties, ok := schema["properties"].(map[string]any)
	if !ok || schema["additionalProperties"] != false {
		return errors.New("schema properties or additionalProperties contract is invalid")
	}
	for _, requiredValue := range schema["required"].([]any) {
		required := requiredValue.(string)
		if _, exists := instance[required]; !exists {
			return fmt.Errorf("required property %s is absent", required)
		}
	}
	for name, value := range instance {
		rawProperty, exists := properties[name]
		if !exists {
			return fmt.Errorf("property %s is not declared", name)
		}
		property := rawProperty.(map[string]any)
		if constant, exists := property["const"]; exists && constant != value {
			return fmt.Errorf("property %s differs from const", name)
		}
		if choices, exists := property["enum"]; exists {
			matched := false
			for _, choice := range choices.([]any) {
				matched = matched || choice == value
			}
			if !matched {
				return fmt.Errorf("property %s is outside enum", name)
			}
		}
		switch property["type"] {
		case "string":
			text, ok := value.(string)
			if !ok {
				return fmt.Errorf("property %s is not a string", name)
			}
			runes := utf8.RuneCountInString(text)
			if min, exists := property["minLength"]; exists && float64(runes) < min.(float64) {
				return fmt.Errorf("property %s violates minLength", name)
			}
			if max, exists := property["maxLength"]; exists && float64(runes) > max.(float64) {
				return fmt.Errorf("property %s violates string length bounds", name)
			}
		case "array":
			items, ok := value.([]any)
			if !ok {
				return fmt.Errorf("property %s is not an array", name)
			}
			if max, exists := property["maxItems"]; exists && float64(len(items)) > max.(float64) {
				return fmt.Errorf("property %s exceeds maxItems", name)
			}
			itemSchema, _ := property["items"].(map[string]any)
			for _, item := range items {
				text, ok := item.(string)
				if !ok || itemSchema["type"] != "string" {
					return fmt.Errorf("property %s has an invalid item", name)
				}
				if max, exists := itemSchema["maxLength"]; exists && float64(utf8.RuneCountInString(text)) > max.(float64) {
					return fmt.Errorf("property %s item exceeds maxLength", name)
				}
			}
		case "integer":
			value, ok := value.(float64)
			if !ok || value < property["minimum"].(float64) || value != float64(int64(value)) {
				return fmt.Errorf("property %s is not a valid integer", name)
			}
		}
	}
	return nil
}

func TestMutationContextPreservesCanonicalCauseAndAddsOnlyKnownOperationFacts(t *testing.T) {
	want := canonicalPending().Diagnostic
	detail := mutationErrorDiagnostic(canonicalPending(), "knowledge-op-7")
	if detail.Code != want.Code || detail.FieldPath != want.FieldPath || detail.NextAction != want.NextAction || detail.OperationID != "knowledge-op-7" || detail.Effects != TeachEffectsUnknown {
		t.Fatalf("mutation context replaced canonical cause: got=%+v want=%+v", detail, want)
	}
	ambiguous := mutationErrorDiagnostic(teachCanonical("record-a", "payload.name", "provide a valid name", ErrInvalid), "ambiguous-op")
	if ambiguous.Effects != TeachEffectsUnknown || ambiguous.OperationID != "ambiguous-op" {
		t.Fatalf("error class alone proved an effect for an ambiguous mutation: %+v", ambiguous)
	}
	ownerFact := canonicalPrewriteError(teachCanonical("record-a", "payload.name", "provide a valid name", ErrInvalid), "prewrite-op")
	proven := mutationErrorDiagnostic(ownerFact, "prewrite-op")
	if proven.Effects != TeachEffectsNone || proven.OperationID != "prewrite-op" || proven.FieldPath != "payload.name" {
		t.Fatalf("explicit prewrite owner fact was not preserved: %+v", proven)
	}
}

func (s *failingRoadmapService) Roadmap(context.Context, string, string) (json.RawMessage, error) {
	return nil, s.err
}

func serveRoadmapFailure(err error) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	handler := NewHandler(&failingRoadmapService{testService: &testService{}, err: err})
	handler.ServeHTTP(response, httptest.NewRequest("GET", "/internal/v1/companies/company-a/projects/project-a/roadmap", nil))
	return response
}

func TestRegistryValidationErrorEmitsCommonDiagnosticAndLegacyFields(t *testing.T) {
	response := httptest.NewRecorder()
	(&Handler{}).writeError(response, todoTeach("request.operation_id", "provide a stable operation ID", ErrInvalid))
	var body errorResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if response.Code != 400 || body.Error != "invalid" || !strings.Contains(body.Teach, "request.operation_id") {
		t.Fatalf("legacy response changed: status=%d body=%s", response.Code, response.Body)
	}
	detail := body.TeachDetail
	if detail == nil || detail.SchemaVersion != "1" || detail.Code != "registry_validation" || detail.FieldPath != "request.operation_id" || detail.NextAction != "provide a stable operation ID" || detail.Effects != "" || detail.Retry != "" {
		t.Fatalf("unexpected common diagnostic: %+v", detail)
	}
}

func TestCanonicalDiagnosticKeepsLegacyKnowledgeFieldsAndCode(t *testing.T) {
	response := httptest.NewRecorder()
	(&Handler{}).writeError(response, teachCanonical("module-a", "payload.decisions.status", "use an accepted decision status", ErrInvalid))
	var body errorResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if response.Code != 400 || body.Error != "invalid" || body.TeachDetail == nil {
		t.Fatalf("response status/code/detail changed: %d %s", response.Code, response.Body)
	}
	if body.TeachDetail.SchemaVersion != "1" || body.TeachDetail.Code != "canonical_validation" || body.TeachDetail.FieldPath != "payload.decisions.status" || body.TeachDetail.RecordID != "module-a" || body.TeachDetail.NextAction == "" {
		t.Fatalf("canonical diagnostic lost legacy fields: %+v", body.TeachDetail)
	}
	encoded, err := json.Marshal(body.TeachDetail)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"code", "field_path", "record_id", "accepted_values", "next_action"} {
		if _, ok := fields[field]; !ok {
			t.Errorf("legacy Knowledge field %q omitted: %s", field, encoded)
		}
	}
}

func TestSessionGuardErrorAddsDiagnosticWithoutChangingStatusOrCode(t *testing.T) {
	response := httptest.NewRecorder()
	writeSessionGuardError(response, 403, "session_project_mismatch", "use a conversation bound to this Project")
	var body errorResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if response.Code != 403 || body.Error != "session_project_mismatch" || body.Teach != "use a conversation bound to this Project" {
		t.Fatalf("legacy session guard response changed: %d %s", response.Code, response.Body)
	}
	if body.TeachDetail == nil || body.TeachDetail.SchemaVersion != "1" || body.TeachDetail.Code != body.Error || body.TeachDetail.NextAction != body.Teach {
		t.Fatalf("session detail missing or inconsistent: %+v", body.TeachDetail)
	}
}

func TestTeachErrorEnvelopeBoundIncludesLegacyAndDiagnosticFields(t *testing.T) {
	values := make([]string, 64)
	for i := range values {
		values[i] = strings.Repeat("v", 2048)
	}
	revision := int64(41)
	response := httptest.NewRecorder()
	writeErrorEnvelope(response, 400, errorResponse{
		Error: "invalid", State: "pending", TODORevision: &revision, Teach: strings.Repeat("legacy", 1000),
		TeachDetail: &TeachDiagnostic{Code: "oversized", FieldPath: "request.field", NextAction: "correct the request", AcceptedValues: values, RequiredFields: values, AcceptedFields: values, NotPersisted: values},
	})
	if len(response.Body.Bytes()) > maxTeachErrorEnvelopeBytes {
		t.Fatalf("serialized error envelope exceeds bound: %d", len(response.Body.Bytes()))
	}
	var body errorResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("bounded fallback is not valid JSON: %v", err)
	}
	if body.Error != "invalid" || body.State != "pending" || body.TODORevision == nil || *body.TODORevision != revision || body.TeachDetail == nil || body.TeachDetail.SchemaVersion != "1" {
		t.Fatalf("bounded envelope lost safe required fields: %+v", body)
	}
}

func TestTeachFallbackBoundsEscapedEnvelopeAndPreservesOperationContext(t *testing.T) {
	repeated := strings.Repeat("<", 2<<10)
	revision := int64(19)
	response := httptest.NewRecorder()
	writeErrorEnvelope(response, 409, errorResponse{
		Error: repeated, State: repeated, Teach: repeated, TODORevision: &revision,
		TeachDetail: &TeachDiagnostic{SchemaVersion: "1", Code: repeated, FieldPath: repeated, NextAction: repeated, OperationID: repeated, Effects: TeachEffectsUnknown, Retry: TeachRetryReadReassess},
	})
	if response.Code != 409 || len(response.Body.Bytes()) > maxTeachErrorEnvelopeBytes || !json.Valid(response.Body.Bytes()) {
		t.Fatalf("escaped fallback is not bounded valid JSON: status=%d bytes=%d body-prefix=%q", response.Code, response.Body.Len(), response.Body.String()[:min(response.Body.Len(), 128)])
	}
	var body errorResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error != repeated || body.State != repeated || body.TODORevision == nil || *body.TODORevision != revision || body.TeachDetail == nil || body.TeachDetail.SchemaVersion != "1" || body.TeachDetail.Code == "" || body.TeachDetail.NextAction == "" || body.TeachDetail.OperationID != repeated || body.TeachDetail.Effects != TeachEffectsUnknown || body.TeachDetail.Retry != TeachRetryReadReassess {
		t.Fatalf("fallback lost required legacy or operation context: %+v", body)
	}
}

func TestTeachDiagnosticOptionalFieldsStayAbsentWhenUnknown(t *testing.T) {
	diagnostic := TeachDiagnostic{Code: "registry_validation", NextAction: "review the typed request"}.normalizedForResponse()
	encoded, err := json.Marshal(diagnostic)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	if diagnostic.Effects != "" || diagnostic.Retry != "" {
		t.Fatalf("unknown effects/retry were invented: %+v", diagnostic)
	}
	for _, field := range []string{"effects", "retry", "operation_id", "revision", "category"} {
		if _, ok := fields[field]; ok {
			t.Errorf("unexpected optional/category field %q: %s", field, encoded)
		}
	}
}

func TestTeachEnvelopeRejectsUnknownOptionalEnumValues(t *testing.T) {
	response := httptest.NewRecorder()
	writeErrorEnvelope(response, 400, errorResponse{Error: "invalid", TeachDetail: &TeachDiagnostic{
		Code: "registry_invalid", NextAction: "correct the typed request", Effects: "maybe", Retry: "blind_retry",
	}})
	var body errorResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.TeachDetail == nil || body.TeachDetail.Effects != "" || body.TeachDetail.Retry != "" || body.TeachDetail.SchemaVersion != "1" {
		t.Fatalf("invalid optional enum values escaped fallback: %s", response.Body)
	}
}

func TestWriteErrorStillMapsUnknownErrorsToUnavailable(t *testing.T) {
	response := serveRoadmapFailure(errors.New("unclassified internal cause"))
	var body errorResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if response.Code != 503 || body.Error != "unavailable" || body.TeachDetail == nil || body.TeachDetail.Code != "registry_unavailable" || body.TeachDetail.NextAction == "" || strings.Contains(response.Body.String(), "unclassified internal cause") {
		t.Fatalf("unknown error mapping changed: status=%d body=%+v", response.Code, body)
	}
}

func TestRevisionConflictDiagnosticRetainsKnownRevisionAndReassessment(t *testing.T) {
	response := serveRoadmapFailure(&todoRevisionConflict{Revision: 12})
	var body errorResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if response.Code != 409 || body.Error != "conflict" || body.TODORevision == nil || *body.TODORevision != 12 || body.TeachDetail == nil || body.TeachDetail.Code != "todo_revision_conflict" || body.TeachDetail.FieldPath != "todo_revision" || body.TeachDetail.Revision == nil || *body.TeachDetail.Revision != 12 || body.TeachDetail.Retry != TeachRetryReadReassess {
		t.Fatalf("revision conflict diagnostic lost known state: status=%d body=%+v", response.Code, body)
	}
}

func TestUnavailableDiagnosticIsEmittedByHandlerWithoutChangingWireCode(t *testing.T) {
	response := serveRoadmapFailure(ErrUnavailable)
	var body errorResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if response.Code != 503 || body.Error != "unavailable" || body.TeachDetail == nil || body.TeachDetail.SchemaVersion != "1" || body.TeachDetail.Code != "registry_unavailable" || body.TeachDetail.NextAction == "" || body.TeachDetail.Effects != "" {
		t.Fatalf("unavailable Handler response=%s status=%d", response.Body, response.Code)
	}
}

func TestPromotionDiagnosticEnvelopeRetains32RowsAndBoundsEscaping(t *testing.T) {
	detail := promotionDiagnostic{RequestedStage: "Promotion-Lane", Repositories: make([]promotionRepositoryDiagnostic, 32)}
	for i := range detail.Repositories {
		detail.Repositories[i] = promotionRepositoryDiagnostic{
			RepositoryID: "example/repo", CommitSHA: fmt.Sprintf("%040x", i+1), TargetBranch: "stage", Status: "observation_unavailable",
			NextAction: strings.Repeat("<", 80),
		}
	}
	response := errorResponse{Error: "conflict", TeachDetail: &TeachDiagnostic{
		Code: "promotion_evidence_incomplete", FieldPath: "remote_proof", NextAction: "Read the TODO.",
		Effects: TeachEffectsNone, Retry: TeachRetryReadReassess, Promotion: &detail,
	}}
	recorder := httptest.NewRecorder()
	writeErrorEnvelope(recorder, http.StatusConflict, response)
	var decoded errorResponse
	if json.Unmarshal(recorder.Body.Bytes(), &decoded) != nil || decoded.TeachDetail == nil || decoded.TeachDetail.Promotion == nil ||
		len(decoded.TeachDetail.Promotion.Repositories) != 32 || recorder.Body.Len() > maxTeachErrorEnvelopeBytes {
		t.Fatalf("bounded diagnostic did not keep all rows: %d bytes", recorder.Body.Len())
	}
	for i := range detail.Repositories {
		detail.Repositories[i].NextAction = strings.Repeat("<", 200)
	}
	recorder = httptest.NewRecorder()
	writeErrorEnvelope(recorder, http.StatusConflict, response)
	decoded = errorResponse{}
	if json.Unmarshal(recorder.Body.Bytes(), &decoded) != nil || decoded.TeachDetail == nil || decoded.TeachDetail.Promotion != nil ||
		decoded.TeachDetail.Effects != TeachEffectsNone {
		t.Fatalf("oversized diagnostic did not fail safely: %d bytes", recorder.Body.Len())
	}
}
