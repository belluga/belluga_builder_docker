package main

import (
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

type todoImageFileURL struct {
	Path string `json:"path"`
	URL  string `json:"url"`
}

func validTODOImageAttachmentPath(companyID, projectID, todoID, filePath, digest string) bool {
	if !validDigest(digest) {
		return false
	}
	prefix := "todo-reference-images/" + companyID + "/" + projectID + "/" + todoID + "/" + strings.TrimPrefix(digest, "sha256:")
	for _, extension := range []string{".png", ".jpg", ".gif"} {
		if filePath == prefix+extension {
			return true
		}
	}
	return false
}

func (p *registeredPreview) todoImageAPIHandler(w http.ResponseWriter, r *http.Request) {
	companyID, projectID, todoID, imageID := r.PathValue("companyID"), r.PathValue("projectID"), r.PathValue("todoID"), r.PathValue("imageID")
	if !validRegistrationID(companyID) || !validRegistrationID(projectID) || !validRegistrationID(todoID) || !validRegistrationID(imageID) || r.URL.RawQuery != "" || r.ContentLength > 0 {
		writeError(w, http.StatusBadRequest, "todo_image_request_invalid")
		return
	}
	endpoint := "/internal/v1/companies/" + url.PathEscape(companyID) + "/projects/" + url.PathEscape(projectID) + "/todos/" + url.PathEscape(todoID) + "/images/" + url.PathEscape(imageID)
	response, err := p.prototypeGet(r, r.Method, endpoint)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "todo_image_unavailable")
		return
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		if response.StatusCode == http.StatusNotFound {
			writeError(w, http.StatusNotFound, "todo_image_not_found")
		} else {
			writeError(w, http.StatusServiceUnavailable, "todo_image_unavailable")
		}
		return
	}
	contentType := response.Header.Get("Content-Type")
	if contentType != "image/png" && contentType != "image/jpeg" && contentType != "image/gif" {
		writeError(w, http.StatusServiceUnavailable, "todo_image_response_invalid")
		return
	}
	contentLength, err := strconv.ParseInt(response.Header.Get("Content-Length"), 10, 64)
	if err != nil || contentLength < 1 || contentLength > 2<<20 {
		writeError(w, http.StatusServiceUnavailable, "todo_image_response_invalid")
		return
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 2<<20+1))
	if r.Method == http.MethodGet && (err != nil || int64(len(data)) != contentLength) || r.Method == http.MethodHead && len(data) != 0 {
		writeError(w, http.StatusServiceUnavailable, "todo_image_response_invalid")
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.FormatInt(contentLength, 10))
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if etag := response.Header.Get("ETag"); strings.HasPrefix(etag, `"sha256:`) && len(etag) == len(`"sha256:`)+64+1 {
		w.Header().Set("ETag", etag)
	}
	if r.Method == http.MethodGet {
		_, _ = w.Write(data)
	}
}
