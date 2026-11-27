package registry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
)

type githubAdminService interface {
	roles(context.Context, string, string, githubOperator) (githubConnectionRoles, error)
	start(context.Context, string, string, githubOperator, githubConnectionStartRequest) (githubConnectionStatus, error)
	status(context.Context, string, string, githubOperator, string) (githubConnectionStatus, error)
	repositories(context.Context, string, string, githubOperator, string, int) (githubConnectionRepositoryPage, error)
	finalize(context.Context, string, string, githubOperator, string, githubConnectionFinalizeRequest) (githubConnectionStatus, error)
	abandon(context.Context, string, string, githubOperator, string, bool) (githubConnectionStatus, error)
	setupCallback(context.Context, githubOperator, string, int64) (githubConnectionStatus, error)
	requestInstallationCallback(context.Context, githubOperator, string) (githubConnectionStatus, error)
	pendingInstallation(context.Context, string, string, githubOperator) (githubPendingInstallationResponse, error)
	oauthCallback(context.Context, githubOperator, string, string) (githubConnectionStatus, error)
}

type githubAdminHTTP struct {
	admin                     githubAdminService
	access                    *githubAccessVerifier
	origin                    string
	store                     *MongoStore
	companyCreators           map[string]bool
	sharedAuthenticatedAccess bool
}

type githubAdminTarget struct {
	company, project, operation string
	operator                    githubOperator
}

type githubAdminAction func(*http.Request, githubAdminTarget) (any, error)

func (h *githubAdminHTTP) register(mux *http.ServeMux) {
	const prefix = "/internal/v1/companies/{companyID}/projects/{projectID}/github-connections"
	const operations = prefix + "/operations"
	mux.HandleFunc("GET "+prefix, h.protect(false, h.roles))
	mux.HandleFunc("GET "+prefix+"/pending-installation", h.protect(false, h.pendingInstallation))
	mux.HandleFunc("POST "+operations, h.protect(false, h.start))
	mux.HandleFunc("GET "+operations+"/{operationID}", h.protect(false, h.status))
	mux.HandleFunc("GET "+operations+"/{operationID}/repositories", h.protect(false, h.repositories))
	mux.HandleFunc("POST "+operations+"/{operationID}/finalize", h.protect(false, h.finalize))
	mux.HandleFunc("POST "+operations+"/{operationID}/abandon", h.protect(false, h.abandon))
	mux.HandleFunc("GET /internal/v1/admin/github/callback", h.protect(true, h.setup))
	mux.HandleFunc("GET /internal/v1/admin/github/oauth/callback", h.protect(true, h.oauth))
	if h.store != nil {
		h.registerOnboarding(mux)
	}
}

func (h *githubAdminHTTP) protect(callback bool, action githubAdminAction) http.HandlerFunc {
	return h.protectMode(callback, "github", action)
}

func (h *githubAdminHTTP) protectMode(callback bool, mode string, action githubAdminAction) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		var writer Handler
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			w.Header().Set("Allow", "GET, POST")
			writeErrorEnvelope(w, http.StatusMethodNotAllowed, errorResponse{Error: "github_method_not_allowed"})
			return
		}
		assertions := r.Header.Values(githubAccessAssertionHeader)
		if len(assertions) != 1 {
			githubAdminUnauthenticated(w)
			return
		}
		operator, err := h.access.verify(r.Context(), assertions[0])
		if err != nil {
			if errors.Is(err, ErrUnavailable) {
				writer.writeError(w, ErrUnavailable)
			} else {
				githubAdminUnauthenticated(w)
			}
			return
		}
		if len(r.URL.RawQuery) > 4096 {
			writer.writeError(w, ErrInvalid)
			return
		}
		t := githubAdminTarget{company: r.PathValue("companyID"), project: r.PathValue("projectID"), operation: r.PathValue("operationID"), operator: operator}
		if callback {
			state, err := parseGitHubState(githubQueryParameter(r, "state"))
			if err != nil {
				writer.writeError(w, ErrInvalid)
				return
			}
			t.company, t.project, t.operation = state.CompanyID, state.ProjectID, state.OperationID
		}
		if t.company != "" && !validID(t.company) || t.project != "" && !validID(t.project) || (t.operation != "" && !validID(t.operation)) {
			writer.writeError(w, ErrInvalid)
			return
		}
		allowed, err := h.onboardingAccess(r.Context(), mode, t)
		if err != nil {
			writer.writeError(w, err)
			return
		}
		if !allowed {
			githubAdminForbidden(w, "github_company_access_denied", "This operator is not authorized for the requested Company.")
			return
		}
		if r.Method == http.MethodPost {
			origins := r.Header.Values("Origin")
			if len(origins) != 1 || origins[0] != h.origin {
				githubAdminForbidden(w, "github_origin_denied", "Open the authorized Builder admin origin and retry.")
				return
			}
			contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			expectedType, limit := "application/json", int64(8<<10)
			if mode == "company-create" {
				expectedType, limit = "multipart/form-data", maxCompanyForm
			}
			if len(r.Header.Values("Content-Type")) != 1 || err != nil || contentType != expectedType {
				writeErrorEnvelope(w, http.StatusUnsupportedMediaType, errorResponse{Error: "github_content_type_required", Teach: "Submit " + expectedType + " for this operation."})
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, limit)
		}
		result, err := action(r, t)
		if err != nil {
			writer.writeError(w, err)
			return
		}
		if image, ok := result.(companyAdminImage); ok {
			w.Header().Set("Content-Type", image.MIME)
			w.Header().Set("X-Content-Type-Options", "nosniff")
			_, _ = w.Write(image.Bytes)
			return
		}
		writeSessionJSON(w, http.StatusOK, result)
	}
}

func githubAdminUnauthenticated(w http.ResponseWriter) {
	writeErrorEnvelope(w, http.StatusUnauthorized, errorResponse{Error: "github_operator_authentication_required", Teach: "Sign in through the protected Builder administration host."})
}

func githubAdminForbidden(w http.ResponseWriter, code, teach string) {
	writeErrorEnvelope(w, http.StatusForbidden, errorResponse{Error: code, Teach: teach})
}

func githubAdminDecode(r *http.Request, target any) error {
	data, err := io.ReadAll(r.Body)
	if err != nil {
		return ErrInvalid
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return ErrInvalid
	}
	if rejectCanonicalDuplicateKeys("", data, "request") != nil {
		return ErrInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil || decoder.Decode(new(any)) != io.EOF {
		return ErrInvalid
	}
	return nil
}

func githubPositiveID(value string) (int64, error) {
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id <= 0 || strconv.FormatInt(id, 10) != value {
		return 0, ErrInvalid
	}
	return id, nil
}

func githubQueryParameter(r *http.Request, key string) string {
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(q[key]) != 1 {
		return ""
	}
	return q[key][0]
}

func (h *githubAdminHTTP) start(r *http.Request, t githubAdminTarget) (any, error) {
	var input githubConnectionStartRequest
	if err := githubAdminDecode(r, &input); err != nil {
		return nil, err
	}
	if t.project != "" && input.DiscoveryOperation != "" {
		allowed, err := h.onboardingAccess(r.Context(), "company", t)
		if err != nil {
			return nil, err
		}
		input.allowCompanyDiscovery = allowed
	}
	return h.admin.start(r.Context(), t.company, t.project, t.operator, input)
}

func (h *githubAdminHTTP) status(r *http.Request, t githubAdminTarget) (any, error) {
	return h.admin.status(r.Context(), t.company, t.project, t.operator, t.operation)
}

func (h *githubAdminHTTP) repositories(r *http.Request, t githubAdminTarget) (any, error) {
	page := int64(1)
	if r.URL.RawQuery != "" {
		var err error
		page, err = githubPositiveID(githubQueryParameter(r, "page"))
		if err != nil || page > 100 {
			return nil, ErrInvalid
		}
	}
	return h.admin.repositories(r.Context(), t.company, t.project, t.operator, t.operation, int(page))
}

func (h *githubAdminHTTP) finalize(r *http.Request, t githubAdminTarget) (any, error) {
	var input githubConnectionFinalizeRequest
	if err := githubAdminDecode(r, &input); err != nil {
		return nil, err
	}
	return h.admin.finalize(r.Context(), t.company, t.project, t.operator, t.operation, input)
}

func (h *githubAdminHTTP) abandon(r *http.Request, t githubAdminTarget) (any, error) {
	var input struct {
		Acknowledge bool `json:"acknowledge_uncertain_result"`
	}
	if err := githubAdminDecode(r, &input); err != nil {
		return nil, err
	}
	return h.admin.abandon(r.Context(), t.company, t.project, t.operator, t.operation, input.Acknowledge)
}

func (h *githubAdminHTTP) setup(r *http.Request, t githubAdminTarget) (any, error) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(query["setup_action"]) > 1 {
		return nil, ErrInvalid
	}
	switch query.Get("setup_action") {
	case "request":
		if len(query) != 2 || query.Has("installation_id") || githubQueryParameter(r, "state") == "" {
			return nil, ErrInvalid
		}
		return h.admin.requestInstallationCallback(r.Context(), t.operator, githubQueryParameter(r, "state"))
	case "", "install", "update":
	default:
		return nil, ErrInvalid
	}
	id, err := githubPositiveID(githubQueryParameter(r, "installation_id"))
	if err != nil {
		return nil, err
	}
	return h.admin.setupCallback(r.Context(), t.operator, githubQueryParameter(r, "state"), id)
}

func (h *githubAdminHTTP) pendingInstallation(r *http.Request, t githubAdminTarget) (any, error) {
	if r.URL.RawQuery != "" {
		return nil, ErrInvalid
	}
	return h.admin.pendingInstallation(r.Context(), t.company, t.project, t.operator)
}

func (h *githubAdminHTTP) oauth(r *http.Request, t githubAdminTarget) (any, error) {
	code := githubQueryParameter(r, "code")
	if !validSummaryField(code, 512) {
		return nil, ErrInvalid
	}
	return h.admin.oauthCallback(r.Context(), t.operator, githubQueryParameter(r, "state"), code)
}

func (h *githubAdminHTTP) roles(r *http.Request, t githubAdminTarget) (any, error) {
	return h.admin.roles(r.Context(), t.company, t.project, t.operator)
}
