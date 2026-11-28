package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const teachVersion = "1"

var publicCode = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)

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

var diagnosticRepositoryID = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,255}/[A-Za-z0-9_.-]{1,255}$`)
var diagnosticCommit = regexp.MustCompile(`^[a-f0-9]{40}$`)
var diagnosticBranch = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,127}$`)
var diagnosticNodeID = regexp.MustCompile(`^[A-Za-z0-9_=-]{1,128}$`)

type teachDiagnostic struct {
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

type errorEnvelope struct {
	HTTPStatus   int              `json:"http_status"`
	Error        string           `json:"error"`
	Teach        string           `json:"teach,omitempty"`
	State        string           `json:"state,omitempty"`
	TODORevision *int64           `json:"todo_revision,omitempty"`
	TeachDetail  *teachDiagnostic `json:"teach_detail,omitempty"`
}

func textResult(text string, failed bool) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}, IsError: failed}
}

func successResult(status int, data []byte) *mcp.CallToolResult {
	var text strings.Builder
	text.Grow(len(data) + 32)
	text.WriteString(`{"http_status":`)
	text.WriteString(strconv.Itoa(status))
	text.WriteString(`,"data":`)
	text.Write(data)
	text.WriteByte('}')
	// A valid JSON input no larger than 18 MiB can expand at most sixfold
	// when it becomes JSON string content. The enclosing MCP overhead is small
	// and remains strictly within the 128 MiB response contract.
	return textResult(text.String(), false)
}

func localFailure(status int, code, field, message, effects, operation, retry string) *mcp.CallToolResult {
	return errorResult(errorEnvelope{
		HTTPStatus: status, Error: code, Teach: message,
		TeachDetail: &teachDiagnostic{SchemaVersion: teachVersion, Code: code, FieldPath: field, NextAction: message, OperationID: operation, Effects: effects, Retry: retry},
	})
}

func registryFailure(response registryResponse, tool toolSpec, operation string) *mcp.CallToolResult {
	var envelope errorEnvelope
	err := json.Unmarshal(response.body, &envelope)
	if err != nil || !publicCode.MatchString(envelope.Error) || !boundedText(envelope.Teach) ||
		envelope.State != "" && !publicCode.MatchString(envelope.State) || !validTeach(envelope.TeachDetail) {
		return unavailable(tool, operation, http.StatusBadGateway, tool.Method != http.MethodGet)
	}
	envelope.HTTPStatus = response.status
	return errorResult(envelope)
}

func validTeach(d *teachDiagnostic) bool {
	if d == nil {
		return true
	}
	if d.SchemaVersion != teachVersion || !publicCode.MatchString(d.Code) || !validPromotionDiagnostic(d.Promotion) {
		return false
	}
	for _, text := range []string{d.FieldPath, d.RecordID, d.NextAction, d.ObjectPurpose, d.OperationID, d.Effects, d.Retry} {
		if !boundedText(text) {
			return false
		}
	}
	for _, list := range [][]string{d.AcceptedValues, d.RequiredFields, d.AcceptedFields, d.NotPersisted} {
		if len(list) > 64 {
			return false
		}
		for _, value := range list {
			if !boundedText(value) {
				return false
			}
		}
	}
	return true
}

func validPromotionDiagnostic(detail *promotionDiagnostic) bool {
	if detail == nil {
		return true
	}
	if detail.RequestedStage != "" && detail.RequestedStage != "Promotion-Lane" && detail.RequestedStage != "Completed" {
		return false
	}
	encoded, err := json.Marshal(detail)
	if err != nil || len(encoded) > 24<<10 || len(detail.Repositories) < 1 || len(detail.Repositories) > 32 {
		return false
	}
	for _, row := range detail.Repositories {
		if !diagnosticRepositoryID.MatchString(row.RepositoryID) || row.CommitSHA != "" && !diagnosticCommit.MatchString(row.CommitSHA) ||
			row.TargetBranch != "" && !diagnosticBranch.MatchString(row.TargetBranch) || row.SourceBranch != "" && !diagnosticBranch.MatchString(row.SourceBranch) ||
			row.ExpectedNodeID != "" && !diagnosticNodeID.MatchString(row.ExpectedNodeID) || !boundedText(row.NextAction) ||
			len(row.Alternatives) > 1 {
			return false
		}
		switch row.Status {
		case "verified", "commit_not_reachable", "branch_missing", "observation_unavailable", "unchecked", "connection_changed", "connection_unavailable":
		default:
			return false
		}
		for _, alternative := range row.Alternatives {
			if alternative.Branch != "dev" && alternative.Branch != "stage" || !alternative.OwnerConfirmationRequired || alternative.CommitReachable != nil && (alternative.Exists == nil || !*alternative.Exists) {
				return false
			}
		}
	}
	return true
}

func boundedText(value string) bool {
	return len(value) <= 2<<10 && utf8.ValidString(value)
}

func errorResult(envelope errorEnvelope) *mcp.CallToolResult {
	encoded, err := json.Marshal(envelope)
	if err == nil {
		// Quote through the same encoder as the wire transport to include
		// TextContent's extra expansion. The remaining JSON-RPC envelope and
		// bounded SDK metadata fit comfortably in the reserved space.
		quoted, quoteErr := json.Marshal(string(encoded))
		if quoteErr == nil && len(quoted)+4096 <= maxMCPError {
			return textResult(string(encoded), true)
		}
	}
	detail := envelope.TeachDetail
	operation, effects, retry := "", "", ""
	if detail != nil {
		operation, effects, retry = detail.OperationID, detail.Effects, detail.Retry
	}
	fallback := errorEnvelope{
		HTTPStatus: envelope.HTTPStatus, Error: "mcp_error_too_large",
		Teach:        "Read back the exact Registry record and consult the owning operation before continuing.",
		TODORevision: envelope.TODORevision,
		TeachDetail:  &teachDiagnostic{SchemaVersion: teachVersion, Code: "mcp_error_too_large", FieldPath: "response", NextAction: "Read current Registry state before another write.", OperationID: operation, Effects: effects, Retry: retry},
	}
	encoded, _ = json.Marshal(fallback)
	return textResult(string(encoded), true)
}

func unavailable(tool toolSpec, operation string, status int, writeDispatched bool) *mcp.CallToolResult {
	if !writeDispatched {
		return localFailure(status, "registry_unavailable", "registry", "Restore the private Registry connection and read the current record before proceeding.", "not_applicable", "", "")
	}
	var message, retry string
	switch tool.Name {
	case "builder_project_register":
		message, retry = "Registration outcome is unknown. Use builder_project_search with the exact ID and Company. Compare Company, Project and canonical name before binding. An inconclusive read keeps the outcome unknown.", "read_reassess"
	case "builder_design_system_begin", "builder_design_system_recover":
		message, retry = "Design System outcome is unknown. Read builder_design_system_state and use the existing exact operation's recovery path.", "recover_operation"
	case "builder_todo_transition":
		message, retry = "The transition outcome is unknown. Read the exact TODO with builder_todo_get. Stop if the requested stage is verified. If the outcome remains unknown, stop automatic retries and ask the operator to check this operation ID.", "read_reassess"
	case "builder_session_bind":
		message, retry = "Binding outcome is unknown. Read builder_session_get for this native session and compare the exact Company and Project before proceeding.", "read_reassess"
	default:
		message, retry = fmt.Sprintf("The %s outcome is unknown. Read back the exact record and consult the Registry's operation contract before any further submission.", tool.Name), "read_reassess"
	}
	return localFailure(status, "registry_outcome_unknown", "registry", message, "unknown", operation, retry)
}
