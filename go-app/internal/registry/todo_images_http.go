package registry

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
)

type todoImageService interface {
	AttachTODOImage(context.Context, string, TODOImageUploadRequest) (TODOImageUploadResponse, error)
	ReadTODOImage(context.Context, string, string, string, string) (todoImageFile, error)
}

func (h *Handler) uploadTODOImage(w http.ResponseWriter, r *http.Request) {
	service, ok := h.service.(todoImageService)
	if !ok {
		h.writeError(w, ErrUnavailable)
		return
	}
	if r.Header.Get(sessionLaneHeader) != "conversation" {
		h.writeError(w, todoTeach("session", "use a bound conversation session to upload a TODO image", ErrInvalid))
		return
	}
	if r.ContentLength > maxTODOImageBytes+8192 || r.ContentLength < 0 {
		h.writeError(w, todoTeach("image", "send a bounded multipart upload up to 2 MiB", ErrInvalid))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxTODOImageBytes+8192)
	if err := r.ParseMultipartForm(maxTODOImageBytes + 8192); err != nil {
		h.writeError(w, todoTeach("image", "send a multipart image field with revision and operation_id", ErrInvalid))
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	if len(r.MultipartForm.File["image"]) != 1 || len(r.MultipartForm.Value["expected_revision"]) != 1 || len(r.MultipartForm.Value["operation_id"]) != 1 || len(r.MultipartForm.Value["description"]) > 1 {
		h.writeError(w, todoTeach("image", "provide one image, expected_revision and operation_id", ErrInvalid))
		return
	}
	revision, err := strconv.ParseInt(r.FormValue("expected_revision"), 10, 64)
	if err != nil || revision < 0 {
		h.writeError(w, todoTeach("expected_revision", "provide the current TODO revision", ErrInvalid))
		return
	}
	file, err := r.MultipartForm.File["image"][0].Open()
	if err != nil {
		h.writeError(w, ErrInvalid)
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxTODOImageBytes+1))
	if err != nil || len(data) > maxTODOImageBytes {
		h.writeError(w, todoTeach("image", "provide an image no larger than 2 MiB", ErrInvalid))
		return
	}
	value, err := service.AttachTODOImage(r.Context(), strings.TrimSpace(r.PathValue("companyID")), TODOImageUploadRequest{
		ProjectID: strings.TrimSpace(r.PathValue("projectID")), TODOID: strings.TrimSpace(r.PathValue("todoID")),
		ExpectedRevision: revision, OperationID: r.FormValue("operation_id"), Description: r.FormValue("description"), Data: data,
	})
	h.respond(w, value, err)
}

func (h *Handler) readTODOImage(w http.ResponseWriter, r *http.Request) {
	service, ok := h.service.(todoImageService)
	if !ok {
		h.writeError(w, ErrUnavailable)
		return
	}
	file, err := service.ReadTODOImage(r.Context(), strings.TrimSpace(r.PathValue("companyID")), strings.TrimSpace(r.PathValue("projectID")), strings.TrimSpace(r.PathValue("todoID")), strings.TrimSpace(r.PathValue("imageID")))
	if err != nil {
		h.writeError(w, err)
		return
	}
	w.Header().Set("Content-Type", file.ContentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(file.Data)))
	w.Header().Set("ETag", `"`+file.Digest+`"`)
	w.Header().Set("Cache-Control", "public, max-age=30")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method == http.MethodGet {
		_, _ = w.Write(file.Data)
	}
}
