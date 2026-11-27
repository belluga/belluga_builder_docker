package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"
)

const maxProxyTeachEnvelope = 64 << 10

var teachToken = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,511}$`)
var teachPublicCode = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
var teachPublicField = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}(\[(0|[1-9][0-9]{0,2})\])?(\.[A-Za-z][A-Za-z0-9_-]{0,63}(\[(0|[1-9][0-9]{0,2})\])?)*$`)
var teachPublicEnum = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)
var teachPublicValues = map[string]bool{
	"Planning": true, "Doing": true, "Review": true, "Done": true,
	"current": true, "planned": true, "draft": true, "deprecated": true, "historical": true,
	"open": true, "resolved": true, "deferred": true, "provisional": true,
	"eq": true, "ne": true, "in": true, "exists": true, "gt": true, "gte": true, "lt": true, "lte": true,
	"string": true, "integer": true, "number": true, "boolean": true, "object": true, "array": true, "object_id": true, "datetime": true, "binary": true, "null": true, "union": true,
	"body": true, "query": true, "path": true, "header": true, "flag": true,
	"page": true, "cursor": true, "window": true, "none": true, "L1": true, "L2": true, "L3": true,
	"unchanged": true, "rejected": true, "asc": true, "desc": true, "2dsphere": true, "text": true, "hashed": true,
	"mvp": true, "complete": true, "GET": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true, "HEAD": true, "OPTIONS": true,
	"undefined": true, "defined": true, "module": true, "project_constitution": true, "policy": true,
	"collection": true, "request": true, "response": true, "projection": true, "configuration": true, "artifact": true,
	"inbound": true, "outbound": true, "log": true, "metric": true, "trace": true, "alert": true, "telemetry": true,
}

type proxyTeachDetail struct {
	SchemaVersion  string   `json:"schema_version"`
	Code           string   `json:"code"`
	FieldPath      string   `json:"field_path,omitempty"`
	AcceptedValues []string `json:"accepted_values,omitempty"`
	NextAction     string   `json:"next_action"`
	RequiredFields []string `json:"required_fields,omitempty"`
	AcceptedFields []string `json:"accepted_fields,omitempty"`
	NotPersisted   []string `json:"not_persisted_fields,omitempty"`
	Revision       *int64   `json:"revision,omitempty"`
	Effects        string   `json:"effects,omitempty"`
	Retry          string   `json:"retry,omitempty"`
}

type proxyTeachEnvelope struct {
	Error        string            `json:"error,omitempty"`
	Teach        string            `json:"teach,omitempty"`
	State        string            `json:"state,omitempty"`
	TODORevision *int64            `json:"todo_revision,omitempty"`
	TeachDetail  *proxyTeachDetail `json:"teach_detail,omitempty"`
}

// readProxyTeach reads an entire bounded public error envelope and projects
// only known TEACH fields. Unknown/private data is never copied to the viewer.
func readProxyTeach(reader io.Reader) (proxyTeachEnvelope, bool) {
	data, err := io.ReadAll(io.LimitReader(reader, maxProxyTeachEnvelope+1))
	if err != nil || len(data) > maxProxyTeachEnvelope || !utf8.Valid(data) || rejectDuplicateJSONKeys(data) != nil || !json.Valid(data) {
		return proxyTeachEnvelope{}, false
	}
	var raw struct {
		Error        string          `json:"error"`
		Teach        string          `json:"teach"`
		State        string          `json:"state"`
		TODORevision *int64          `json:"todo_revision"`
		TeachDetail  json.RawMessage `json:"teach_detail"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if decoder.Decode(&raw) != nil {
		return proxyTeachEnvelope{}, false
	}
	out := proxyTeachEnvelope{}
	if teachPublicCode.MatchString(raw.Error) {
		out.Error = raw.Error
	}
	if safeProxyTeachText(raw.Teach) {
		out.Teach = raw.Teach
	}
	if raw.State == "pending" || raw.State == "blocked" || raw.State == "invalid" {
		out.State = raw.State
	}
	if raw.TODORevision != nil && *raw.TODORevision >= 0 {
		revision := *raw.TODORevision
		out.TODORevision = &revision
	}
	if len(raw.TeachDetail) != 0 && string(raw.TeachDetail) != "null" {
		var detail proxyTeachDetail
		if json.Unmarshal(raw.TeachDetail, &detail) == nil {
			out.TeachDetail = projectProxyTeachDetail(detail)
		}
	}
	return out, true
}

func safeProxyTeachText(value string) bool {
	if len(value) == 0 || len(value) > 512 || !utf8.ValidString(value) || strings.ContainsAny(value, "\\\r\n") || strings.Contains(value, "://") || strings.Contains(value, "/") {
		return false
	}
	text := strings.TrimPrefix(value, "TEACH: ")
	if strings.TrimSpace(text) == "" {
		return false
	}
	lower := strings.ToLower(value)
	for _, marker := range []string{"token=", "password", "secret", "authorization", "mongodb", "bearer ", "localhost", "127.0.0.1", "10.0.", "192.168.", "172.16."} {
		if strings.Contains(lower, marker) {
			return false
		}
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func proxyTeachDetailFromLegacy(code, teach string) *proxyTeachDetail {
	if !teachToken.MatchString(code) {
		code = "registry_diagnostic"
	}
	action := strings.TrimPrefix(teach, "TEACH: ")
	if !safeProxyTeachText(action) {
		action = "Review the current Registry state and follow the owning operation before proceeding."
	}
	return &proxyTeachDetail{SchemaVersion: "1", Code: code, NextAction: action}
}

func safeProxyTeachDetail(d proxyTeachDetail) bool {
	return d.SchemaVersion == "1" && teachPublicCode.MatchString(d.Code) && safeProxyTeachText(d.NextAction) && (d.FieldPath == "" || teachPublicField.MatchString(d.FieldPath)) && validProxyTeachLists(d) && (d.Effects == "" || d.Effects == "none" || d.Effects == "committed" || d.Effects == "unknown" || d.Effects == "not_applicable") && (d.Retry == "" || d.Retry == "none" || d.Retry == "same_operation" || d.Retry == "read_reassess" || d.Retry == "restore_service" || d.Retry == "recover_operation") && (d.Revision == nil || *d.Revision >= 0)
}

func validProxyTeachLists(d proxyTeachDetail) bool {
	for _, list := range [][]string{d.AcceptedValues, d.RequiredFields, d.AcceptedFields, d.NotPersisted} {
		if len(list) > 64 {
			return false
		}
		for _, item := range list {
			if !teachPublicField.MatchString(item) && !teachPublicEnum.MatchString(item) {
				return false
			}
		}
	}
	return true
}

func projectProxyTeachDetail(d proxyTeachDetail) *proxyTeachDetail {
	if d.SchemaVersion != "1" || !teachPublicCode.MatchString(d.Code) {
		return nil
	}
	out := proxyTeachDetail{SchemaVersion: "1", Code: d.Code}
	if teachPublicField.MatchString(d.FieldPath) {
		out.FieldPath = d.FieldPath
	}
	if safeProxyTeachText(d.NextAction) {
		out.NextAction = d.NextAction
	}
	out.AcceptedValues = safeProxyTeachList(d.AcceptedValues, true)
	out.RequiredFields = safeProxyTeachList(d.RequiredFields, false)
	out.AcceptedFields = safeProxyTeachList(d.AcceptedFields, false)
	out.NotPersisted = safeProxyTeachList(d.NotPersisted, false)
	if d.Revision != nil && *d.Revision >= 0 {
		revision := *d.Revision
		out.Revision = &revision
	}
	if d.Effects == "none" || d.Effects == "committed" || d.Effects == "unknown" || d.Effects == "not_applicable" {
		out.Effects = d.Effects
	}
	if d.Retry == "none" || d.Retry == "same_operation" || d.Retry == "read_reassess" || d.Retry == "restore_service" || d.Retry == "recover_operation" {
		out.Retry = d.Retry
	}
	return &out
}

// sanitizeProxyTeachDetail applies the public allowlist to 2xx Registry payloads
// and supplies a small valid diagnostic when an optional detail is malformed.
func sanitizeProxyTeachDetail(detail *proxyTeachDetail, fallbackCode, teach string) *proxyTeachDetail {
	if detail == nil {
		return nil
	}
	projected := projectProxyTeachDetail(*detail)
	if projected == nil {
		return proxyTeachDetailFromLegacy(fallbackCode, teach)
	}
	if projected.NextAction == "" {
		fallback := proxyTeachDetailFromLegacy(projected.Code, teach)
		projected.NextAction = fallback.NextAction
	}
	return projected
}

func safeProxyTeachList(values []string, enums bool) []string {
	if len(values) > 64 {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if enums && teachPublicEnum.MatchString(value) && teachPublicValues[value] || !enums && teachPublicField.MatchString(value) {
			out = append(out, value)
		}
	}
	return out
}

func writeProxyTeachError(w http.ResponseWriter, status int, code, fallback string, envelope proxyTeachEnvelope) {
	if code != "" {
		envelope.Error = code
	} else if !teachPublicCode.MatchString(envelope.Error) {
		envelope.Error = ""
	}
	if !safeProxyTeachText(envelope.Teach) {
		envelope.Teach = fallback
	}
	detailCode := envelope.Error
	if detailCode == "" {
		detailCode = "registry_diagnostic"
	}
	if envelope.TeachDetail != nil {
		// Keep safe causal fields when only optional public text is filtered. The
		// sanitizer supplies a valid action without discarding owner facts.
		envelope.TeachDetail = sanitizeProxyTeachDetail(envelope.TeachDetail, detailCode, envelope.Teach)
	}
	if envelope.TeachDetail == nil || !safeProxyTeachDetail(*envelope.TeachDetail) {
		action := strings.TrimPrefix(envelope.Teach, "TEACH: ")
		if action == "" {
			action = "Review the request and consult the current Registry state before proceeding."
		}
		envelope.TeachDetail = &proxyTeachDetail{SchemaVersion: "1", Code: detailCode, NextAction: action}
	}
	body, err := json.Marshal(envelope)
	if err != nil || len(body)+1 > maxProxyTeachEnvelope {
		fallbackError := code
		if fallbackError == "" && teachPublicCode.MatchString(envelope.Error) {
			fallbackError = envelope.Error
		}
		fallbackDetailCode := fallbackError
		if fallbackDetailCode == "" {
			fallbackDetailCode = "registry_diagnostic"
		}
		nextAction := strings.TrimPrefix(fallback, "TEACH: ")
		if !safeProxyTeachText(nextAction) {
			nextAction = "Review current Registry state before proceeding."
		}
		state := envelope.State
		if state == "" {
			state = "blocked"
		}
		envelope = proxyTeachEnvelope{Error: fallbackError, Teach: fallback, State: state, TODORevision: envelope.TODORevision, TeachDetail: &proxyTeachDetail{SchemaVersion: "1", Code: fallbackDetailCode, NextAction: nextAction}}
		body, _ = json.Marshal(envelope)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(append(body, '\n'))
}
