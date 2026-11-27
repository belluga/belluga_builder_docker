package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	provider        = "codex"
	headerProvider  = "X-Delphi-Session-Type"
	headerSession   = "X-Delphi-Session-ID"
	headerLane      = "X-Delphi-Request-Lane"
	headerSelection = "X-Delphi-Confirmed-Selection"
)

type adapter struct {
	registry    registryClient
	companies   []string
	companyList string
}

type confirmedSelection struct {
	SessionID      string
	CompanyID      string
	ProjectID      string
	ProjectName    string
	OwnerReference string
}

func selectionFromHeader(value string) (confirmedSelection, bool) {
	if value == "" || len(value) > 2048 {
		return confirmedSelection{}, false
	}
	data, err := base64.RawURLEncoding.Strict().DecodeString(value)
	if err != nil {
		return confirmedSelection{}, false
	}
	fields, err := decodeObject(data)
	if err != nil {
		return confirmedSelection{}, false
	}
	for key, value := range fields {
		if !slices.Contains([]string{"session_id", "company_id", "project_id", "project_name", "owner_reference"}, key) {
			return confirmedSelection{}, false
		}
		if _, ok := value.(string); !ok {
			return confirmedSelection{}, false
		}
	}
	choice := confirmedSelection{
		SessionID: fields.text("session_id"), CompanyID: fields.text("company_id"), ProjectID: fields.text("project_id"),
		ProjectName: fields.text("project_name"), OwnerReference: fields.text("owner_reference"),
	}
	if !nativeSessionID.MatchString(choice.SessionID) || !identityID.MatchString(choice.CompanyID) ||
		!identityID.MatchString(choice.ProjectID) || utf8.RuneCountInString(choice.ProjectName) > 160 ||
		strings.TrimSpace(choice.OwnerReference) == "" || len(choice.OwnerReference) > 512 {
		return confirmedSelection{}, false
	}
	return choice, true
}

func (a adapter) handler(tool toolSpec) mcp.ToolHandler {
	return func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		carrier, ok := ctx.Value(carrierKey{}).(transportCarrier)
		sessionValid := carrier.provider == provider && nativeSessionID.MatchString(carrier.session)
		withoutSession := carrier.sessionHeadersAbsent && tool.Method == http.MethodGet &&
			(tool.Name == "builder_company_search" || tool.Name == "builder_project_search")
		if !ok || !sessionValid && !withoutSession {
			return localFailure(http.StatusUnauthorized, "session_required", "session_id",
				"Use the native Codex thread.sessionId in the host-provided conversation headers.", "none", "", ""), nil
		}
		// SDK legacy Streamable HTTP handlers may detach their context from the
		// HTTP request. Tie the single upstream call to both cancellation paths.
		toolCtx, cancel := context.WithTimeout(carrier.context, 180*time.Second)
		defer cancel()
		stop := context.AfterFunc(ctx, cancel)
		defer stop()
		return a.call(toolCtx, carrier, tool, request.Params), nil
	}
}

func (a adapter) call(ctx context.Context, carrier transportCarrier, tool toolSpec, params *mcp.CallToolParamsRaw) *mcp.CallToolResult {
	args, body, identity, err := tool.readArguments(ctx, params.Arguments)
	if errors.Is(err, errArgumentBodyLimit) {
		return localFailure(http.StatusRequestEntityTooLarge, "mcp_body_too_large", "body",
			"Reduce the JSON body to fit the owning Registry endpoint's byte limit.", "none", "", "")
	}
	if errors.Is(err, errOverviewMedia) {
		return localFailure(http.StatusBadRequest, "mcp_invalid_media", "body.media",
			"Provide two to six typed Overview PNG slots of at most 2 MiB each.", "none", "", "")
	}
	if err != nil || params.RequestState != "" || len(params.InputResponses) != 0 {
		return localFailure(http.StatusBadRequest, "mcp_invalid_arguments", "arguments",
			"Supply one typed arguments object matching this Builder tool's input schema.", "none", "", "")
	}
	operation := args.text("operation_id")
	if operation == "" {
		operation = identity.OperationID
	}
	company, project := args.text("company_id"), args.text("project_id")
	if company != "" && !slices.Contains(a.companies, company) {
		return localFailure(http.StatusForbidden, "company_not_allowed", "company_id",
			"Select a Company authorized for this private MCP service.", "none", operation, "")
	}
	if field := validateIdentities(tool, args, identity); field != "" {
		return localFailure(http.StatusConflict, "mcp_identity_mismatch", field,
			"Match the exact Company, Project and resource identities in this tool's route and body.", "none", operation, "")
	}
	if tool.Name == "builder_company_search" || tool.Name == "builder_project_search" {
		if value := args.text("name"); value != "" && (strings.TrimSpace(value) == "" || len(strings.TrimSpace(value)) > 200) {
			return localFailure(http.StatusBadRequest, "mcp_invalid_name", "name",
				"Supply a UTF-8 name of 1 to 200 bytes after trimming.", "none", "", "")
		}
	}

	switch tool.Name {
	case "builder_project_register", "builder_session_bind":
		choice, confirmed := selectionFromHeader(carrier.selection)
		if tool.Name == "builder_project_register" {
			project = identity.ProjectID
		}
		if !confirmed || choice.SessionID != carrier.session || choice.CompanyID != company || choice.ProjectID != project ||
			tool.Name == "builder_project_register" && choice.ProjectName != identity.ProjectName {
			return localFailure(http.StatusForbidden, "selection_required_or_mismatched", "confirmed_selection",
				"Confirm the exact Company and Project with the host for this native conversation before accessing registration or binding.", "none", operation, "")
		}
		if tool.Name == "builder_session_bind" {
			body, err = json.Marshal(object{"type": provider, "external_id": carrier.session, "company_id": company, "project_id": project,
				"expected_revision": args["expected_revision"], "operation_id": operation, "owner_reference": choice.OwnerReference})
			if err != nil {
				return unavailable(tool, operation, http.StatusBadGateway, false)
			}
		} else if rejection := a.preflightRegistration(ctx, carrier.session, company, project, identity.CompanyName); rejection != nil {
			return rejection
		}
	}
	if ctx.Err() != nil {
		return unavailable(tool, operation, http.StatusGatewayTimeout, false)
	}
	query := tool.queryValues(args)
	if tool.Name == "builder_company_search" {
		query.Set("company_ids", a.companyList)
	}
	path := tool.path(args, carrier.session)
	var response registryResponse
	if carrier.sessionHeadersAbsent {
		response, err = a.registry.requestDiscovery(ctx, path, query, tool.timeout())
	} else {
		response, err = a.registry.request(ctx, carrier.session, tool.Method, path, query, body, tool.timeout())
	}
	if err != nil || ctx.Err() != nil {
		status := http.StatusBadGateway
		if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) {
			status = http.StatusGatewayTimeout
		}
		return unavailable(tool, operation, status, tool.Method != http.MethodGet)
	}
	if !response.ok() {
		return registryFailure(response, tool, operation)
	}
	output := response.body
	switch tool.Name {
	case "builder_session_get", "builder_session_bind":
		result, ok := readRegistrySession(output, carrier.session)
		if !ok || tool.Name == "builder_session_bind" && (company != result.Binding.CompanyID || project != result.Binding.ProjectID) {
			return unavailable(tool, operation, http.StatusBadGateway, tool.Method != http.MethodGet)
		}
		if !slices.Contains(a.companies, result.Binding.CompanyID) {
			return localFailure(http.StatusForbidden, "company_not_allowed", "company_id",
				"The bound Company is outside the scope of this private MCP service.", "not_applicable", "", "")
		}
	case "builder_project_get", "builder_project_register":
		output, err = projectProjection(output, company, project, identity.ProjectName)
		if err != nil {
			return unavailable(tool, operation, http.StatusBadGateway, tool.Method != http.MethodGet)
		}
	}
	if len(output) > largeMCPResponseThreshold && (carrier.response == nil || !carrier.response.acquire(ctx)) {
		return unavailable(tool, operation, http.StatusGatewayTimeout, tool.Method != http.MethodGet)
	}
	result := successResult(response.status, output)
	if ctx.Err() != nil {
		return unavailable(tool, operation, http.StatusGatewayTimeout, tool.Method != http.MethodGet)
	}
	return result
}

func (a adapter) preflightRegistration(ctx context.Context, session, company, project, name string) *mcp.CallToolResult {
	const sessionRoute = "/internal/v1/sessions/codex/"
	const companyRoute = "/internal/v1/companies/search"
	readTool := toolSpec{Method: http.MethodGet}
	sessionResult, err := a.registry.request(ctx, session, http.MethodGet, sessionRoute+url.PathEscape(session), nil, nil, 10*time.Second)
	if err != nil {
		return unavailable(readTool, "", http.StatusBadGateway, false)
	}
	if sessionResult.ok() {
		current, ok := readRegistrySession(sessionResult.body, session)
		if !ok {
			return unavailable(readTool, "", http.StatusBadGateway, false)
		}
		if current.Binding.CompanyID != company || current.Binding.ProjectID != project {
			return localFailure(http.StatusForbidden, "session_project_mismatch", "project_id",
				"This native conversation is bound to a different Company or Project. Select a separate conversation.", "none", "", "")
		}
	} else if sessionResult.status != http.StatusNotFound {
		return registryFailure(sessionResult, readTool, "")
	}

	query := url.Values{"id": {company}, "company_ids": {company}}
	result, err := a.registry.request(ctx, session, http.MethodGet, companyRoute, query, nil, 10*time.Second)
	if err != nil {
		return unavailable(readTool, "", http.StatusBadGateway, false)
	}
	if !result.ok() {
		return registryFailure(result, readTool, "")
	}
	var page struct {
		MatchState string `json:"match_state"`
		Items      []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"items"`
	}
	if err := json.Unmarshal(result.body, &page); err != nil {
		return unavailable(readTool, "", http.StatusBadGateway, false)
	}
	if page.MatchState != "unique" || len(page.Items) != 1 || page.Items[0].ID != company || page.Items[0].Name != name {
		return localFailure(http.StatusConflict, "company_not_confirmed", "body.company_name",
			"Find the pre-existing Company and use its exact canonical ID and name before registering this Project.", "none", "", "")
	}
	return nil
}
