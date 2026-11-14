package registry

import (
	"encoding/json"
	"io"
	"net/http"
)

func (h *Handler) readImplementationRepositories(w http.ResponseWriter, r *http.Request) {
	service, ok := h.service.(implementationRepositoryService)
	if !ok {
		h.writeError(w, ErrUnavailable)
		return
	}
	result, err := service.ReadImplementationRepositories(r.Context(), r.PathValue("companyID"), r.PathValue("projectID"))
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeSessionJSON(w, http.StatusOK, result)
}

func (h *Handler) changeImplementationRepository(w http.ResponseWriter, r *http.Request) {
	service, ok := h.service.(implementationRepositoryService)
	if !ok {
		h.writeError(w, ErrUnavailable)
		return
	}
	var request ImplementationRepositoryChange
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		h.writeError(w, todoTeach("request", "provide one implementation-repository change object within 8 KiB", ErrInvalid))
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		h.writeError(w, todoTeach("request", "contain exactly one JSON object", ErrInvalid))
		return
	}
	result, err := service.ChangeImplementationRepository(r.Context(), r.PathValue("companyID"), r.PathValue("projectID"), request)
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeSessionJSON(w, http.StatusOK, result)
}
