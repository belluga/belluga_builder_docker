package registry

import (
	"net/http"
	"strconv"
	"strings"
)

func (h *Handler) readPortfolioCatalog(w http.ResponseWriter, r *http.Request) {
	service, ok := h.service.(portfolioReader)
	if !ok {
		h.writeError(w, ErrUnavailable)
		return
	}
	value, err := service.ReadPortfolioCatalog(r.Context(), strings.TrimSpace(r.PathValue("companyID")))
	h.respond(w, value, err)
}

func (h *Handler) readPortfolioFile(w http.ResponseWriter, r *http.Request) {
	service, ok := h.service.(portfolioReader)
	if !ok {
		h.writeError(w, ErrUnavailable)
		return
	}
	rev, err := strconv.ParseInt(strings.TrimSpace(r.PathValue("bindingRevision")), 10, 64)
	if err != nil || rev < 0 {
		h.writeError(w, ErrInvalid)
		return
	}
	value, err := service.ReadProjectPortfolioFile(r.Context(), strings.TrimSpace(r.PathValue("companyID")), strings.TrimSpace(r.PathValue("projectID")), rev, strings.TrimSpace(r.PathValue("revision")), "sha256:"+strings.TrimSpace(r.PathValue("inventoryDigest")), r.PathValue("path"))
	if err != nil {
		h.writeError(w, err)
		return
	}
	if len(value.Data) > portfolioMaxFileBytes || value.ContentType != portfolioMIMEType(r.PathValue("path")) || value.ETag == "" || !validFullGitSHA(value.Revision) || !validSourceDigest(value.InventoryDigest) || !validSourceDigest(value.Digest) {
		h.writeError(w, ErrUnavailable)
		return
	}
	w.Header().Set("Content-Type", value.ContentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(value.Data)))
	w.Header().Set("ETag", value.ETag)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Portfolio-Revision", value.Revision)
	w.Header().Set("X-Portfolio-Inventory-Digest", value.InventoryDigest)
	w.Header().Set("X-Portfolio-Content-Digest", value.Digest)
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
