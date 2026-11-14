package registry

import (
	"net/http"
	"strconv"
	"strings"
)

func (h *Handler) readPrototypes(w http.ResponseWriter, r *http.Request) {
	service, ok := h.service.(prototypeReader)
	if !ok {
		h.writeError(w, ErrUnavailable)
		return
	}
	value, err := service.ReadPrototypes(r.Context(), strings.TrimSpace(r.PathValue("companyID")), strings.TrimSpace(r.PathValue("projectID")))
	h.respond(w, value, err)
}

func (h *Handler) readPrototype(w http.ResponseWriter, r *http.Request) {
	service, ok := h.service.(prototypeReader)
	if !ok {
		h.writeError(w, ErrUnavailable)
		return
	}
	value, err := service.ReadPrototype(r.Context(), strings.TrimSpace(r.PathValue("companyID")), strings.TrimSpace(r.PathValue("projectID")), strings.TrimSpace(r.PathValue("revision")), strings.TrimSpace(r.PathValue("prototypeID")))
	h.respond(w, value, err)
}

func (h *Handler) readPrototypeFile(w http.ResponseWriter, r *http.Request) {
	service, ok := h.service.(prototypeReader)
	if !ok {
		h.writeError(w, ErrUnavailable)
		return
	}
	value, err := service.ReadPrototypeFile(r.Context(), strings.TrimSpace(r.PathValue("companyID")), strings.TrimSpace(r.PathValue("projectID")), strings.TrimSpace(r.PathValue("revision")), r.PathValue("path"))
	if err != nil {
		h.writeError(w, err)
		return
	}
	w.Header().Set("Content-Type", value.ContentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(value.Data)))
	w.Header().Set("ETag", value.ETag)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Prototype-Revision", value.Revision)
	w.Header().Set("X-Prototype-Inventory-Digest", value.InventoryDigest)
	w.Header().Set("Cache-Control", "private, max-age=0, must-revalidate")
	if r.Header.Get("If-None-Match") == value.ETag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(value.Data)
	}
}
