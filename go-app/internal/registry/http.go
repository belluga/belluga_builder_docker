package registry

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
)

const maxRequestBody = maxSnapshotReferenceBytes
const maxTODOImportRequestBytes = 4 << 20

type Handler struct{ service Service }

type selectedTODOImporter interface {
	ImportSelectedTODOs(context.Context, SelectedTODOImportRequest) (SelectedTODOImportReport, error)
}

type additionalTODOAdmitter interface {
	AdmitAdditionalTODOs(context.Context, SelectedTODOImportRequest) (SelectedTODOImportReport, error)
}

type individualTODOAdmitter interface {
	AdmitTODO(context.Context, string, string, TODOAdmissionRequest) (TODOAdmissionReport, error)
}

type historicalTODOStageAdopter interface {
	AdoptHistoricalTODOStages(context.Context, string, HistoricalTODOAdoptionRequest) (HistoricalTODOAdoptionResponse, error)
}

type todoContentMigrator interface {
	MigrateTODOContent(context.Context, string, TODOContentMigrationRequest) (TODOContentEditResponse, error)
}

type todoCreationTemplateReader interface {
	TODOCreationTemplate(context.Context, string, string) (TODOCreationTemplate, error)
}

type selectedTODOLifecycle interface {
	ReadTODO(context.Context, string, string, string, *int64) (TODOReadResponse, error)
	TransitionTODO(context.Context, string, TODOActionRequest) (TODOActionResponse, error)
	VerifyCurrentTODOStage(context.Context, string, VerifyCurrentTODOStageRequest) (TODOActionResponse, error)
}

type todoHistoryService interface {
	ReadTODOHistory(context.Context, string, string, string, int64, int) (TODOHistoryPage, error)
	ReadTODOAsOf(context.Context, string, string, string, int64) (TODOAsOfResponse, error)
	CompareTODOHistory(context.Context, string, string, string, int64, int64) (TODOContentComparison, error)
	EditTODOContent(context.Context, string, TODOContentEditRequest) (TODOContentEditResponse, error)
	RestoreTODOContent(context.Context, string, TODOContentRestoreRequest) (TODOContentEditResponse, error)
	EditTODOEvidence(context.Context, string, TODOEvidenceEditRequest) (TODOContentEditResponse, error)
	EditTODOPlacement(context.Context, string, TODOPlacementEditRequest) (TODOContentEditResponse, error)
	NormalizeTODOSectionOwnership(context.Context, string, TODOSectionOwnershipRequest) (TODOContentEditResponse, error)
}

type todoRecordMutationService interface {
	EditTODOEvidence(context.Context, string, TODOEvidenceEditRequest) (TODOContentEditResponse, error)
	EditTODOPlacement(context.Context, string, TODOPlacementEditRequest) (TODOContentEditResponse, error)
	NormalizeTODOSectionOwnership(context.Context, string, TODOSectionOwnershipRequest) (TODOContentEditResponse, error)
}

type roadmapChangeService interface {
	ChangeRoadmap(context.Context, string, string, RoadmapChange) (RoadmapChangeResult, error)
}

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
	mux.HandleFunc("POST /internal/v1/companies/{companyID}/projects/{projectID}/roadmap/epics", h.roadmapChange("create-epic"))
	mux.HandleFunc("PATCH /internal/v1/companies/{companyID}/projects/{projectID}/roadmap/epics/{epicID}", h.roadmapChange("edit-epic"))
	mux.HandleFunc("PUT /internal/v1/companies/{companyID}/projects/{projectID}/roadmap/epics/order", h.roadmapChange("order-epics"))
	mux.HandleFunc("DELETE /internal/v1/companies/{companyID}/projects/{projectID}/roadmap/epics/{epicID}", h.roadmapChange("delete-epic"))
	mux.HandleFunc("POST /internal/v1/companies/{companyID}/projects/{projectID}/roadmap/epics/{epicID}/releases", h.roadmapChange("create-release"))
	mux.HandleFunc("PATCH /internal/v1/companies/{companyID}/projects/{projectID}/roadmap/releases/{releaseID}", h.roadmapChange("edit-release"))
	mux.HandleFunc("PUT /internal/v1/companies/{companyID}/projects/{projectID}/roadmap/epics/{epicID}/releases/order", h.roadmapChange("order-releases"))
	mux.HandleFunc("DELETE /internal/v1/companies/{companyID}/projects/{projectID}/roadmap/releases/{releaseID}", h.roadmapChange("delete-release"))
	mux.HandleFunc("GET /internal/v1/tenants/resolve", h.resolveTenant)
	mux.HandleFunc("POST /internal/v1/todos/import", h.importSelectedTODOs)
	mux.HandleFunc("POST /internal/v1/todos/admit-builder-r08", h.admitAdditionalTODOs)
	mux.HandleFunc("POST /internal/v1/companies/{companyID}/projects/{projectID}/todos", h.admitTODO)
	mux.HandleFunc("POST /internal/v1/companies/{companyID}/projects/{projectID}/todos/historical-adoption", h.adoptHistoricalTODOStages)
	mux.HandleFunc("GET /internal/v1/companies/{companyID}/projects/{projectID}/todos/template", h.todoCreationTemplate)
	mux.HandleFunc("GET /internal/v1/companies/{companyID}/projects/{projectID}/todos/{todoID}", h.readTODO)
	mux.HandleFunc("POST /internal/v1/companies/{companyID}/projects/{projectID}/todos/{todoID}/transition", h.transitionTODO)
	mux.HandleFunc("POST /internal/v1/companies/{companyID}/projects/{projectID}/todos/{todoID}/verify-current-stage", h.verifyCurrentTODOStage)
	mux.HandleFunc("GET /internal/v1/companies/{companyID}/projects/{projectID}/todos/{todoID}/history/compare", h.compareTODOHistory)
	mux.HandleFunc("GET /internal/v1/companies/{companyID}/projects/{projectID}/todos/{todoID}/history/{revision}", h.readTODOAsOf)
	mux.HandleFunc("GET /internal/v1/companies/{companyID}/projects/{projectID}/todos/{todoID}/history", h.readTODOHistory)
	mux.HandleFunc("POST /internal/v1/companies/{companyID}/projects/{projectID}/todos/{todoID}/content", h.editTODOContent)
	mux.HandleFunc("POST /internal/v1/companies/{companyID}/projects/{projectID}/todos/{todoID}/content/migrate", h.migrateTODOContent)
	mux.HandleFunc("POST /internal/v1/companies/{companyID}/projects/{projectID}/todos/{todoID}/evidence", h.editTODOEvidence)
	mux.HandleFunc("POST /internal/v1/companies/{companyID}/projects/{projectID}/todos/{todoID}/placement", h.editTODOPlacement)
	mux.HandleFunc("POST /internal/v1/companies/{companyID}/projects/{projectID}/todos/{todoID}/sections/ownership", h.normalizeTODOSectionOwnership)
	mux.HandleFunc("POST /internal/v1/companies/{companyID}/projects/{projectID}/todos/{todoID}/history/restore", h.restoreTODOContent)
	return mux
}

func (h *Handler) roadmapChange(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		service, ok := h.service.(roadmapChangeService)
		if !ok {
			h.writeError(w, ErrUnavailable)
			return
		}
		var input RoadmapChange
		if !h.decodeLimit(w, r, &input, maxRequestBody) {
			return
		}
		projectID := strings.TrimSpace(r.PathValue("projectID"))
		if input.ProjectID != projectID {
			h.writeError(w, todoTeach("project_id", "match the exact Project path identity", ErrConflict))
			return
		}
		if epicID := r.PathValue("epicID"); epicID != "" {
			if action == "edit-epic" || action == "delete-epic" {
				if input.ID != "" && input.ID != epicID {
					h.writeError(w, todoTeach("id", "match the Epic path identity", ErrConflict))
					return
				}
				input.ID = epicID
			} else {
				if input.EpicID != "" && input.EpicID != epicID {
					h.writeError(w, todoTeach("epic_id", "match the Epic path identity", ErrConflict))
					return
				}
				input.EpicID = epicID
			}
		}
		if releaseID := r.PathValue("releaseID"); releaseID != "" {
			if input.ID != "" && input.ID != releaseID {
				h.writeError(w, todoTeach("id", "match the Release path identity", ErrConflict))
				return
			}
			input.ID = releaseID
		}
		value, err := service.ChangeRoadmap(r.Context(), strings.TrimSpace(r.PathValue("companyID")), action, input)
		h.respond(w, value, err)
	}
}

type errorResponse struct {
	Error        string `json:"error"`
	Teach        string `json:"teach,omitempty"`
	TODORevision *int64 `json:"todo_revision,omitempty"`
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

func (h *Handler) importSelectedTODOs(w http.ResponseWriter, r *http.Request) {
	importer, ok := h.service.(selectedTODOImporter)
	if !ok {
		h.writeError(w, ErrUnavailable)
		return
	}
	var input SelectedTODOImportRequest
	if !h.decodeLimit(w, r, &input, maxTODOImportRequestBytes) {
		return
	}
	report, err := importer.ImportSelectedTODOs(r.Context(), input)
	h.respond(w, report, err)
}

func (h *Handler) admitAdditionalTODOs(w http.ResponseWriter, r *http.Request) {
	admitter, ok := h.service.(additionalTODOAdmitter)
	if !ok {
		h.writeError(w, ErrUnavailable)
		return
	}
	var input SelectedTODOImportRequest
	if !h.decodeLimit(w, r, &input, maxTODOImportRequestBytes) {
		return
	}
	report, err := admitter.AdmitAdditionalTODOs(r.Context(), input)
	h.respond(w, report, err)
}

func (h *Handler) admitTODO(w http.ResponseWriter, r *http.Request) {
	admitter, ok := h.service.(individualTODOAdmitter)
	if !ok {
		h.writeError(w, ErrUnavailable)
		return
	}
	var input TODOAdmissionRequest
	if !h.decodeLimit(w, r, &input, maxTODOImportRequestBytes) {
		return
	}
	value, err := admitter.AdmitTODO(r.Context(), strings.TrimSpace(r.PathValue("companyID")), strings.TrimSpace(r.PathValue("projectID")), input)
	h.respond(w, value, err)
}

func (h *Handler) adoptHistoricalTODOStages(w http.ResponseWriter, r *http.Request) {
	adopter, ok := h.service.(historicalTODOStageAdopter)
	if !ok {
		h.writeError(w, ErrUnavailable)
		return
	}
	var input HistoricalTODOAdoptionRequest
	if !h.decodeLimit(w, r, &input, maxRequestBody) {
		return
	}
	projectID := strings.TrimSpace(r.PathValue("projectID"))
	if input.ProjectID != projectID {
		h.writeError(w, todoTeach("project_id", "match the exact Project path identity", ErrConflict))
		return
	}
	value, err := adopter.AdoptHistoricalTODOStages(r.Context(), strings.TrimSpace(r.PathValue("companyID")), input)
	h.respond(w, value, err)
}

func (h *Handler) todoCreationTemplate(w http.ResponseWriter, r *http.Request) {
	reader, ok := h.service.(todoCreationTemplateReader)
	if !ok {
		h.writeError(w, ErrUnavailable)
		return
	}
	value, err := reader.TODOCreationTemplate(r.Context(), strings.TrimSpace(r.PathValue("companyID")), strings.TrimSpace(r.PathValue("projectID")))
	h.respond(w, value, err)
}

func (h *Handler) readTODO(w http.ResponseWriter, r *http.Request) {
	lifecycle, ok := h.service.(selectedTODOLifecycle)
	if !ok {
		h.writeError(w, ErrUnavailable)
		return
	}
	var expectedRevision *int64
	query := r.URL.Query()["todo_revision"]
	if len(query) > 1 {
		h.writeError(w, todoTeach("todo_revision", "appear at most once", ErrInvalid))
		return
	}
	if len(query) == 1 && query[0] != "" {
		value := query[0]
		revision, err := strconv.ParseInt(value, 10, 64)
		if err != nil || revision < 0 {
			h.writeError(w, todoTeach("todo_revision", "be a nonnegative integer from the Roadmap summary", ErrInvalid))
			return
		}
		expectedRevision = &revision
	}
	value, err := lifecycle.ReadTODO(r.Context(), strings.TrimSpace(r.PathValue("companyID")), strings.TrimSpace(r.PathValue("projectID")), strings.TrimSpace(r.PathValue("todoID")), expectedRevision)
	h.respond(w, value, err)
}

func (h *Handler) transitionTODO(w http.ResponseWriter, r *http.Request) {
	lifecycle, ok := h.service.(selectedTODOLifecycle)
	if !ok {
		h.writeError(w, ErrUnavailable)
		return
	}
	var input TODOActionRequest
	if !h.decode(w, r, &input) {
		return
	}
	if input.ProjectID != strings.TrimSpace(r.PathValue("projectID")) || input.TODOID != strings.TrimSpace(r.PathValue("todoID")) {
		h.writeError(w, todoTeach("project_id/todo_id", "match the exact Project and TODO path identity", ErrConflict))
		return
	}
	value, err := lifecycle.TransitionTODO(r.Context(), strings.TrimSpace(r.PathValue("companyID")), input)
	h.respond(w, value, err)
}

func (h *Handler) verifyCurrentTODOStage(w http.ResponseWriter, r *http.Request) {
	lifecycle, ok := h.service.(selectedTODOLifecycle)
	if !ok {
		h.writeError(w, ErrUnavailable)
		return
	}
	var input VerifyCurrentTODOStageRequest
	if !h.decode(w, r, &input) {
		return
	}
	if input.ProjectID != strings.TrimSpace(r.PathValue("projectID")) || input.TODOID != strings.TrimSpace(r.PathValue("todoID")) {
		h.writeError(w, todoTeach("project_id/todo_id", "match the exact Project and TODO path identity", ErrConflict))
		return
	}
	value, err := lifecycle.VerifyCurrentTODOStage(r.Context(), strings.TrimSpace(r.PathValue("companyID")), input)
	h.respond(w, value, err)
}

func (h *Handler) readTODOHistory(w http.ResponseWriter, r *http.Request) {
	service, ok := h.service.(todoHistoryService)
	if !ok {
		h.writeError(w, ErrUnavailable)
		return
	}
	after := int64(-1)
	if raw := r.URL.Query().Get("after_revision"); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			h.writeError(w, todoTeach("after_revision", "be an integer aggregate revision cursor", ErrInvalid))
			return
		}
		after = value
	}
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil {
			h.writeError(w, todoTeach("limit", "be an integer between 1 and 100", ErrInvalid))
			return
		}
		limit = value
	}
	value, err := service.ReadTODOHistory(r.Context(), strings.TrimSpace(r.PathValue("companyID")), strings.TrimSpace(r.PathValue("projectID")), strings.TrimSpace(r.PathValue("todoID")), after, limit)
	h.respond(w, value, err)
}

func (h *Handler) readTODOAsOf(w http.ResponseWriter, r *http.Request) {
	service, ok := h.service.(todoHistoryService)
	if !ok {
		h.writeError(w, ErrUnavailable)
		return
	}
	revision, err := strconv.ParseInt(strings.TrimSpace(r.PathValue("revision")), 10, 64)
	if err != nil {
		h.writeError(w, todoTeach("revision", "be a nonnegative aggregate revision", ErrInvalid))
		return
	}
	value, err := service.ReadTODOAsOf(r.Context(), strings.TrimSpace(r.PathValue("companyID")), strings.TrimSpace(r.PathValue("projectID")), strings.TrimSpace(r.PathValue("todoID")), revision)
	h.respond(w, value, err)
}

func (h *Handler) compareTODOHistory(w http.ResponseWriter, r *http.Request) {
	service, ok := h.service.(todoHistoryService)
	if !ok {
		h.writeError(w, ErrUnavailable)
		return
	}
	from, fromErr := strconv.ParseInt(r.URL.Query().Get("from"), 10, 64)
	to, toErr := strconv.ParseInt(r.URL.Query().Get("to"), 10, 64)
	if fromErr != nil || toErr != nil {
		h.writeError(w, todoTeach("from/to", "be integer aggregate revisions", ErrInvalid))
		return
	}
	value, err := service.CompareTODOHistory(r.Context(), strings.TrimSpace(r.PathValue("companyID")), strings.TrimSpace(r.PathValue("projectID")), strings.TrimSpace(r.PathValue("todoID")), from, to)
	h.respond(w, value, err)
}

func (h *Handler) editTODOContent(w http.ResponseWriter, r *http.Request) {
	service, ok := h.service.(todoHistoryService)
	if !ok {
		h.writeError(w, ErrUnavailable)
		return
	}
	var input TODOContentEditRequest
	if !h.decodeLimit(w, r, &input, maxTODOImportRequestBytes) {
		return
	}
	if input.ProjectID != strings.TrimSpace(r.PathValue("projectID")) || input.TODOID != strings.TrimSpace(r.PathValue("todoID")) {
		h.writeError(w, todoTeach("project_id/todo_id", "match the exact Project and TODO path identity", ErrConflict))
		return
	}
	value, err := service.EditTODOContent(r.Context(), strings.TrimSpace(r.PathValue("companyID")), input)
	h.respond(w, value, err)
}

func (h *Handler) migrateTODOContent(w http.ResponseWriter, r *http.Request) {
	migrator, ok := h.service.(todoContentMigrator)
	if !ok {
		h.writeError(w, ErrUnavailable)
		return
	}
	var input TODOContentMigrationRequest
	if !h.decodeLimit(w, r, &input, maxRequestBody) {
		return
	}
	if input.ProjectID != strings.TrimSpace(r.PathValue("projectID")) || input.TODOID != strings.TrimSpace(r.PathValue("todoID")) {
		h.writeError(w, todoTeach("project_id/todo_id", "match the exact Project and TODO path identities", ErrConflict))
		return
	}
	value, err := migrator.MigrateTODOContent(r.Context(), strings.TrimSpace(r.PathValue("companyID")), input)
	h.respond(w, value, err)
}

func (h *Handler) restoreTODOContent(w http.ResponseWriter, r *http.Request) {
	service, ok := h.service.(todoHistoryService)
	if !ok {
		h.writeError(w, ErrUnavailable)
		return
	}
	var input TODOContentRestoreRequest
	if !h.decodeLimit(w, r, &input, maxTODOImportRequestBytes) {
		return
	}
	if input.ProjectID != strings.TrimSpace(r.PathValue("projectID")) || input.TODOID != strings.TrimSpace(r.PathValue("todoID")) {
		h.writeError(w, todoTeach("project_id/todo_id", "match the exact Project and TODO path identity", ErrConflict))
		return
	}
	value, err := service.RestoreTODOContent(r.Context(), strings.TrimSpace(r.PathValue("companyID")), input)
	h.respond(w, value, err)
}

func (h *Handler) editTODOEvidence(w http.ResponseWriter, r *http.Request) {
	service, ok := h.service.(todoRecordMutationService)
	if !ok {
		h.writeError(w, ErrUnavailable)
		return
	}
	var input TODOEvidenceEditRequest
	if !h.decodeLimit(w, r, &input, maxTODOImportRequestBytes) {
		return
	}
	if input.ProjectID != strings.TrimSpace(r.PathValue("projectID")) || input.TODOID != strings.TrimSpace(r.PathValue("todoID")) {
		h.writeError(w, todoTeach("project_id/todo_id", "match the exact Project and TODO path identity", ErrConflict))
		return
	}
	value, err := service.EditTODOEvidence(r.Context(), strings.TrimSpace(r.PathValue("companyID")), input)
	h.respond(w, value, err)
}

func (h *Handler) editTODOPlacement(w http.ResponseWriter, r *http.Request) {
	service, ok := h.service.(todoRecordMutationService)
	if !ok {
		h.writeError(w, ErrUnavailable)
		return
	}
	var input TODOPlacementEditRequest
	if !h.decodeLimit(w, r, &input, maxTODOImportRequestBytes) {
		return
	}
	if input.ProjectID != strings.TrimSpace(r.PathValue("projectID")) || input.TODOID != strings.TrimSpace(r.PathValue("todoID")) {
		h.writeError(w, todoTeach("project_id/todo_id", "match the exact Project and TODO path identity", ErrConflict))
		return
	}
	value, err := service.EditTODOPlacement(r.Context(), strings.TrimSpace(r.PathValue("companyID")), input)
	h.respond(w, value, err)
}

func (h *Handler) normalizeTODOSectionOwnership(w http.ResponseWriter, r *http.Request) {
	service, ok := h.service.(todoRecordMutationService)
	if !ok {
		h.writeError(w, ErrUnavailable)
		return
	}
	var input TODOSectionOwnershipRequest
	if !h.decodeLimit(w, r, &input, maxTODOImportRequestBytes) {
		return
	}
	if input.ProjectID != strings.TrimSpace(r.PathValue("projectID")) || input.TODOID != strings.TrimSpace(r.PathValue("todoID")) {
		h.writeError(w, todoTeach("project_id/todo_id", "match the exact Project and TODO path identity", ErrConflict))
		return
	}
	value, err := service.NormalizeTODOSectionOwnership(r.Context(), strings.TrimSpace(r.PathValue("companyID")), input)
	h.respond(w, value, err)
}

func (h *Handler) decode(w http.ResponseWriter, r *http.Request, target any) bool {
	return h.decodeLimit(w, r, target, maxRequestBody)
}

func (h *Handler) decodeLimit(w http.ResponseWriter, r *http.Request, target any, limit int64) bool {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
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
	response := errorResponse{Error: code}
	var teach *TODOValidationError
	if errors.As(err, &teach) {
		response.Teach = teach.Error()
	}
	var revisionConflict *todoRevisionConflict
	if errors.As(err, &revisionConflict) {
		response.TODORevision = &revisionConflict.Revision
		response.Teach = revisionConflict.Error()
	}
	_ = json.NewEncoder(w).Encode(response)
}
