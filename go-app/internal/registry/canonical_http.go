package registry

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
)

func (h *Handler) canonicalEntity(w http.ResponseWriter, r *http.Request) {
	service, ok := h.service.(canonicalKnowledgeReader)
	if !ok {
		h.writeError(w, ErrUnavailable)
		return
	}
	value, err := service.ReadCanonicalEntity(r.Context(), r.PathValue("companyID"), r.PathValue("projectID"), r.PathValue("entityID"))
	h.respond(w, value, err)
}

func (h *Handler) canonicalEndpoint(w http.ResponseWriter, r *http.Request) {
	service, ok := h.service.(canonicalKnowledgeService)
	if !ok {
		h.writeError(w, ErrUnavailable)
		return
	}
	value, err := service.ReadCanonical(r.Context(), r.PathValue("companyID"), r.PathValue("projectID"), FamilyEndpoint, r.PathValue("endpointID"))
	h.respond(w, value, err)
}

func (h *Handler) canonicalDecision(w http.ResponseWriter, r *http.Request) {
	service, ok := h.service.(canonicalKnowledgeReader)
	if !ok {
		h.writeError(w, ErrUnavailable)
		return
	}
	value, err := service.ReadCanonicalDecision(r.Context(), r.PathValue("companyID"), r.PathValue("projectID"), CanonicalFamily(r.PathValue("family")), r.PathValue("recordID"), r.PathValue("decisionID"))
	h.respond(w, value, err)
}

func (h *Handler) canonicalAsOf(w http.ResponseWriter, r *http.Request) {
	service, ok := h.service.(canonicalKnowledgeReader)
	if !ok {
		h.writeError(w, ErrUnavailable)
		return
	}
	revision, err := strconv.ParseInt(r.PathValue("revision"), 10, 64)
	if err != nil || revision < 1 {
		h.writeError(w, teachCanonical(r.PathValue("recordID"), "revision", "use a positive content revision", ErrInvalid))
		return
	}
	value, err := service.ReadCanonicalAsOf(r.Context(), r.PathValue("companyID"), r.PathValue("projectID"), CanonicalFamily(r.PathValue("family")), r.PathValue("recordID"), revision)
	h.respond(w, value, err)
}

func (h *Handler) canonicalCompare(w http.ResponseWriter, r *http.Request) {
	service, ok := h.service.(canonicalKnowledgeReader)
	if !ok {
		h.writeError(w, ErrUnavailable)
		return
	}
	query := r.URL.Query()
	from, fromErr := strconv.ParseInt(query.Get("from"), 10, 64)
	to, toErr := strconv.ParseInt(query.Get("to"), 10, 64)
	if len(query["from"]) != 1 || len(query["to"]) != 1 || fromErr != nil || toErr != nil || from < 1 || to < 1 {
		h.writeError(w, teachCanonical(r.PathValue("recordID"), "from/to", "provide each positive content revision exactly once", ErrInvalid))
		return
	}
	value, err := service.CompareCanonicalHistory(r.Context(), r.PathValue("companyID"), r.PathValue("projectID"), CanonicalFamily(r.PathValue("family")), r.PathValue("recordID"), from, to)
	h.respond(w, value, err)
}

func (h *Handler) decodeCanonicalRequest(w http.ResponseWriter, r *http.Request, target any) bool {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxCanonicalPayload))
	if err != nil {
		h.writeError(w, teachCanonical("", "request", "provide a JSON object within the template's byte limit", ErrInvalid))
		return false
	}
	if err := rejectCanonicalDuplicateKeys("", body, "request"); err != nil {
		h.writeError(w, err)
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		field := "request"
		if name, ok := strings.CutPrefix(err.Error(), `json: unknown field "`); ok {
			field += "." + strings.TrimSuffix(name, `"`)
		}
		var typeError *json.UnmarshalTypeError
		if errors.As(err, &typeError) && typeError.Field != "" {
			field += "." + typeError.Field
		}
		h.writeError(w, teachCanonical("", field, "use the typed request shape returned by knowledge-template", ErrInvalid))
		return false
	}
	if decoder.Decode(new(any)) != io.EOF {
		h.writeError(w, teachCanonical("", "request", "provide exactly one JSON object without trailing values", ErrInvalid))
		return false
	}
	return true
}
