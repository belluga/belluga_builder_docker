package registry

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

type discoveryService interface {
	SearchCompanies(context.Context, discoveryQuery) (discoveryPage, error)
	SearchProjects(context.Context, discoveryQuery) (discoveryPage, error)
}

func (h *Handler) searchCompanies(w http.ResponseWriter, r *http.Request) {
	h.searchDiscovery(w, r, "")
}

func (h *Handler) searchProjects(w http.ResponseWriter, r *http.Request) {
	company := r.PathValue("companyID")
	if !validID(company) {
		h.writeError(w, discoveryInputError("company_id", "Supply a valid Company ID."))
		return
	}
	h.searchDiscovery(w, r, company)
}

func (h *Handler) searchDiscovery(w http.ResponseWriter, r *http.Request, company string) {
	query, cursor, err := parseDiscoveryQuery(r.URL.RawQuery, company)
	if err != nil {
		h.writeError(w, err)
		return
	}
	if r.Header.Get(sessionLaneHeader) == "conversation" && !h.scopeDiscovery(w, r, &query) {
		return
	}
	if cursor != "" {
		query.After, err = query.decodeCursor(cursor)
		if err != nil {
			h.writeError(w, err)
			return
		}
	}
	service, ok := h.service.(discoveryService)
	if !ok {
		h.writeError(w, ErrUnavailable)
		return
	}
	var page discoveryPage
	if company == "" {
		page, err = service.SearchCompanies(r.Context(), query)
	} else {
		page, err = service.SearchProjects(r.Context(), query)
	}
	if err != nil {
		h.writeError(w, err)
		return
	}
	h.respond(w, page, nil)
}

func discoveryInputError(field, action string) error {
	return withTeachDiagnostic(ErrInvalid, *readTeach("discovery_invalid_query", field, action))
}

func parseDiscoveryQuery(raw, company string) (discoveryQuery, string, error) {
	query := discoveryQuery{Company: company, Limit: 20}
	values, err := url.ParseQuery(raw)
	if err != nil {
		return query, "", discoveryInputError("query", "Supply valid URL-encoded search parameters.")
	}
	for key, occurrences := range values {
		switch key {
		case "name", "id", "limit", "cursor":
			if len(occurrences) == 1 && occurrences[0] != "" {
				continue
			}
		case "company_ids":
			if company == "" && len(occurrences) == 1 && occurrences[0] != "" {
				continue
			}
		}
		return query, "", discoveryInputError("query", "Supply one value for each supported search parameter.")
	}
	if values.Has("id") == values.Has("name") {
		return query, "", discoveryInputError("query", "Supply exactly one of name and id.")
	}
	if values.Has("id") {
		query.Field, query.Value = "id", values.Get("id")
		if !validID(query.Value) {
			return query, "", discoveryInputError("id", "Supply a valid Registry ID.")
		}
	} else {
		query.Field, query.Value = "name", strings.TrimSpace(values.Get("name"))
		if len(query.Value) == 0 || len(query.Value) > 200 || !utf8.ValidString(query.Value) {
			return query, "", discoveryInputError("name", "Supply a valid UTF-8 name of 1 to 200 bytes after trimming.")
		}
	}
	if values.Has("limit") {
		query.Limit, err = strconv.Atoi(values.Get("limit"))
		if err != nil || query.Limit < 1 || query.Limit > 50 {
			return query, "", discoveryInputError("limit", "Supply an integer between 1 and 50.")
		}
	}
	if values.Has("company_ids") {
		query.Scope, err = discoveryCompanyIDs(strings.Split(values.Get("company_ids"), ","))
		if err != nil {
			return query, "", err
		}
	}
	return query, values.Get("cursor"), nil
}

func (h *Handler) scopeDiscovery(w http.ResponseWriter, r *http.Request, query *discoveryQuery) bool {
	sessions, ok := h.service.(sessionService)
	if !ok {
		h.writeError(w, ErrUnavailable)
		return false
	}
	provider, externalID := r.Header.Get(sessionTypeHeader), r.Header.Get(sessionExternalIDHeader)
	if _, _, err := normalizeSessionIdentity(provider, externalID); err != nil {
		writeSessionGuardError(w, http.StatusUnauthorized, "session_required", "Supply the native conversation identity.")
		return false
	}
	session, err := sessions.ReadSession(r.Context(), provider, externalID)
	if errors.Is(err, ErrNotFound) {
		return true // Discovery is explicitly available before the initial bind.
	}
	if err != nil {
		h.writeError(w, err)
		return false
	}
	binding := session.Binding
	if query.Company != "" {
		if query.Company != binding.CompanyID {
			writeSessionGuardError(w, http.StatusForbidden, "session_project_mismatch", "Use the Company bound to this conversation for Project discovery.")
			return false
		}
		query.BoundID = binding.ProjectID
	} else {
		if len(query.Scope) > 0 && !slices.Contains(query.Scope, binding.CompanyID) {
			writeSessionGuardError(w, http.StatusForbidden, "session_project_mismatch", "The bound Company is outside the authorized discovery scope.")
			return false
		}
		query.BoundID = binding.CompanyID
	}
	return true
}
