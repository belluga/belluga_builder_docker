package registry

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

const maxRequestBody = maxSnapshotReferenceBytes

type Handler struct{ service Service }

func NewHandler(service Service) http.Handler {
	h := &Handler{service: service}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /internal/v1/catalog", h.catalog)
	mux.HandleFunc("GET /internal/v1/companies/{companyID}/projects/{projectID}", h.project)
	mux.HandleFunc("POST /internal/v1/companies/{companyID}/projects", h.register)
	mux.HandleFunc("POST /internal/v1/companies/{companyID}/projects/{projectID}/decline", h.decline)
	mux.HandleFunc("PUT /internal/v1/companies/{companyID}/projects/{projectID}/snapshot-reference", h.snapshotReference)
	mux.HandleFunc("GET /internal/v1/lifecycle-control", h.lifecycleControl)
	mux.HandleFunc("PUT /internal/v1/lifecycle-control", h.updateLifecycleControl)
	mux.HandleFunc("GET /internal/v1/companies/{companyID}/projects/{projectID}/roadmap", h.roadmap)
	mux.HandleFunc("GET /internal/v1/tenants/resolve", h.resolveTenant)
	return mux
}

type errorResponse struct {
	Error string `json:"error"`
}

func (h *Handler) catalog(w http.ResponseWriter, r *http.Request) {
	value, err := h.service.Catalog(r.Context())
	h.respond(w, value, err)
}
func (h *Handler) project(w http.ResponseWriter, r *http.Request) {
	value, err := h.service.Project(r.Context(), strings.TrimSpace(r.PathValue("companyID")), strings.TrimSpace(r.PathValue("projectID")))
	h.respond(w, value, err)
}
func (h *Handler) register(w http.ResponseWriter, r *http.Request) {
	var input Registration
	if h.decode(w, r, &input) {
		if input.CompanyID != strings.TrimSpace(r.PathValue("companyID")) {
			h.writeError(w, ErrInvalid)
			return
		}
		value, err := h.service.Register(r.Context(), input)
		h.respond(w, value, err)
	}
}
func (h *Handler) decline(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Binding Binding `json:"binding"`
	}
	if !h.decode(w, r, &input) {
		return
	}
	if err := h.service.Decline(r.Context(), strings.TrimSpace(r.PathValue("companyID")), strings.TrimSpace(r.PathValue("projectID")), input.Binding); err != nil {
		h.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (h *Handler) snapshotReference(w http.ResponseWriter, r *http.Request) {
	var input SnapshotReference
	if !h.decode(w, r, &input) {
		return
	}
	value, err := h.service.UpdateSnapshotReference(r.Context(), strings.TrimSpace(r.PathValue("companyID")), strings.TrimSpace(r.PathValue("projectID")), input)
	h.respond(w, value, err)
}
func (h *Handler) lifecycleControl(w http.ResponseWriter, r *http.Request) {
	value, err := h.service.LifecycleControl(r.Context())
	h.respond(w, value, err)
}
func (h *Handler) updateLifecycleControl(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ExpectedRevision int64           `json:"expected_revision"`
		Value            json.RawMessage `json:"value"`
	}
	if !h.decode(w, r, &input) {
		return
	}
	value, err := h.service.UpdateLifecycleControl(r.Context(), input.ExpectedRevision, input.Value)
	h.respond(w, value, err)
}
func (h *Handler) roadmap(w http.ResponseWriter, r *http.Request) {
	companyID := strings.TrimSpace(r.PathValue("companyID"))
	value, err := h.service.Roadmap(r.Context(), strings.TrimSpace(r.PathValue("projectID")), companyID)
	h.respondRaw(w, value, err)
}
func (h *Handler) resolveTenant(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	value, err := h.service.ResolveTenant(r.Context(), TenantRequest{Type: q.Get("type"), Identifier: q.Get("identifier"), CompanyID: q.Get("company_id")})
	h.respond(w, value, err)
}

func (h *Handler) decode(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		h.writeError(w, ErrInvalid)
		return false
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		h.writeError(w, ErrInvalid)
		return false
	}
	return true
}
func (h *Handler) respond(w http.ResponseWriter, value any, err error) {
	if err != nil {
		h.writeError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(value)
}
func (h *Handler) respondRaw(w http.ResponseWriter, value json.RawMessage, err error) {
	if err != nil {
		h.writeError(w, err)
		return
	}
	if len(value) == 0 || !json.Valid(value) {
		h.writeError(w, ErrUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(value)
}
func (h *Handler) writeError(w http.ResponseWriter, err error) {
	status, code := http.StatusServiceUnavailable, ErrUnavailable.Error()
	switch {
	case errors.Is(err, ErrInvalid):
		status, code = http.StatusBadRequest, ErrInvalid.Error()
	case errors.Is(err, ErrNotFound):
		status, code = http.StatusNotFound, ErrNotFound.Error()
	case errors.Is(err, ErrConflict):
		status, code = http.StatusConflict, ErrConflict.Error()
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorResponse{Error: code})
}
