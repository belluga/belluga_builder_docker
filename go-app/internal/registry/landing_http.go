package registry

import (
	"net/http"
	"strconv"
	"strings"
)

func (h *Handler) readLanding(w http.ResponseWriter, r *http.Request) {
	service, ok := h.service.(landingService)
	if !ok {
		h.writeError(w, ErrUnavailable)
		return
	}
	value, err := service.ReadLanding(r.Context(), strings.TrimSpace(r.PathValue("companyID")), strings.TrimSpace(r.PathValue("projectID")))
	h.respond(w, value, err)
}

func (h *Handler) readLandingFile(w http.ResponseWriter, r *http.Request) {
	service, ok := h.service.(landingService)
	if !ok {
		h.writeError(w, ErrUnavailable)
		return
	}
	bindingRevision, parseErr := strconv.ParseInt(strings.TrimSpace(r.PathValue("bindingRevision")), 10, 64)
	if parseErr != nil || bindingRevision < 0 {
		h.writeError(w, ErrInvalid)
		return
	}
	value, err := service.ReadLandingFile(r.Context(), strings.TrimSpace(r.PathValue("companyID")), strings.TrimSpace(r.PathValue("projectID")), bindingRevision, strings.TrimSpace(r.PathValue("revision")), r.PathValue("path"))
	if err != nil {
		h.writeError(w, err)
		return
	}
	if len(value.Data) > landingMaxFileBytes || !validLandingResponseMIME(value.ContentType) || value.ETag == "" || len(value.ETag) > 256 || strings.ContainsAny(value.ETag, "\r\n") || !validFullGitSHA(value.Revision) || !validSourceDigest(value.InventoryDigest) {
		h.writeError(w, ErrUnavailable)
		return
	}
	w.Header().Set("Content-Type", value.ContentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(value.Data)))
	w.Header().Set("ETag", value.ETag)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Landing-Revision", value.Revision)
	w.Header().Set("X-Landing-Inventory-Digest", value.InventoryDigest)
	w.Header().Set("Cache-Control", "private, max-age=0, must-revalidate")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if strings.HasPrefix(value.ContentType, "text/html") || value.ContentType == "image/svg+xml" {
		w.Header().Set("Content-Security-Policy", landingCSP)
	}
	if value.ETag != "" && r.Header.Get("If-None-Match") == value.ETag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(value.Data)
	}
}

func validLandingResponseMIME(contentType string) bool {
	for _, extension := range []string{".html", ".css", ".js", ".json", ".svg", ".png", ".jpg", ".gif", ".webp", ".avif", ".woff", ".woff2", ".ttf", ".otf"} {
		if landingMIMEType("design/landing/file"+extension) == contentType {
			return true
		}
	}
	return false
}
