package registry

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
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
	GrantTODOReviewWaiver(context.Context, string, TODOReviewWaiverRequest) (TODOContentEditResponse, error)
}

type todoRecordMutationService interface {
	EditTODOEvidence(context.Context, string, TODOEvidenceEditRequest) (TODOContentEditResponse, error)
	EditTODOPlacement(context.Context, string, TODOPlacementEditRequest) (TODOContentEditResponse, error)
	EditTODONextStep(context.Context, string, TODONextStepEditRequest) (TODOContentEditResponse, error)
	NormalizeTODOSectionOwnership(context.Context, string, TODOSectionOwnershipRequest) (TODOContentEditResponse, error)
}

type roadmapChangeService interface {
	ChangeRoadmap(context.Context, string, string, RoadmapChange) (RoadmapChangeResult, error)
}

type releaseInvestigationService interface {
	ReadReleaseInvestigation(context.Context, string, string, string) (ReleaseInvestigationRead, error)
}

type canonicalKnowledgeService interface {
	ImportCanonicalDraft(context.Context, string, CanonicalImport) (CanonicalManifest, error)
	CanonicalManifest(context.Context, string, string, string) (CanonicalManifest, error)
	ReadCanonical(context.Context, string, string, CanonicalFamily, string) (CanonicalRecord, error)
	ReadCanonicalDraft(context.Context, string, string, string, CanonicalFamily, string) (CanonicalRecord, error)
	CreateCanonical(context.Context, string, CanonicalMutation) (CanonicalRecord, error)
	EditCanonical(context.Context, string, CanonicalMutation) (CanonicalRecord, error)
	RestoreCanonical(context.Context, string, CanonicalRestoreRequest) (CanonicalRecord, error)
	DeleteCanonical(context.Context, string, CanonicalDeleteRequest) (CanonicalRecord, error)
	ActivateCanonical(context.Context, string, string, string, string, string) (CanonicalManifest, error)
	CanonicalHistory(context.Context, string, string, time.Time, int) (CanonicalHistoryPage, error)
}

type canonicalKnowledgeReader interface {
	ReadCanonicalEntity(context.Context, string, string, string) (CanonicalProjection, error)
	ReadCanonicalDecision(context.Context, string, string, CanonicalFamily, string, string) (CanonicalProjection, error)
	ReadCanonicalAsOf(context.Context, string, string, CanonicalFamily, string, int64) (CanonicalRecord, error)
	CompareCanonicalHistory(context.Context, string, string, CanonicalFamily, string, int64, int64) (CanonicalComparison, error)
}

type canonicalIndexReader interface {
	CanonicalIndex(context.Context, string, string, CanonicalIndexQuery) (CanonicalIndexPage, error)
}

func NewHandler(service Service) http.Handler {
	h := &Handler{service: service}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /internal/v1/catalog", h.catalog)
	mux.HandleFunc("GET /internal/v1/companies/{companyID}/projects/{projectID}/knowledge/template", h.canonicalTemplate)
	mux.HandleFunc("GET /internal/v1/companies/{companyID}/projects/{projectID}/knowledge/manifest", h.canonicalManifest)
	mux.HandleFunc("GET /internal/v1/companies/{companyID}/projects/{projectID}/knowledge/index", h.canonicalIndex)
	mux.HandleFunc("GET /internal/v1/companies/{companyID}/projects/{projectID}/knowledge/entities/{entityID}", h.canonicalEntity)
	mux.HandleFunc("GET /internal/v1/companies/{companyID}/projects/{projectID}/knowledge/endpoints/{endpointID}", h.canonicalEndpoint)
	mux.HandleFunc("GET /internal/v1/companies/{companyID}/projects/{projectID}/knowledge/{family}/{recordID}/decisions/{decisionID}", h.canonicalDecision)
	mux.HandleFunc("GET /internal/v1/companies/{companyID}/projects/{projectID}/knowledge/{family}/{recordID}/history/compare", h.canonicalCompare)
	mux.HandleFunc("GET /internal/v1/companies/{companyID}/projects/{projectID}/knowledge/{family}/{recordID}/history/{revision}", h.canonicalAsOf)
	mux.HandleFunc("POST /internal/v1/companies/{companyID}/projects/{projectID}/knowledge/drafts", h.canonicalImport)
	mux.HandleFunc("POST /internal/v1/companies/{companyID}/projects/{projectID}/knowledge/activate", h.canonicalActivate)
	mux.HandleFunc("GET /internal/v1/companies/{companyID}/projects/{projectID}/knowledge/{family}/{recordID}", h.canonicalRead)
	mux.HandleFunc("PUT /internal/v1/companies/{companyID}/projects/{projectID}/knowledge/{family}/{recordID}", h.canonicalEdit)
	mux.HandleFunc("DELETE /internal/v1/companies/{companyID}/projects/{projectID}/knowledge/{family}/{recordID}", h.canonicalDelete)
	mux.HandleFunc("POST /internal/v1/companies/{companyID}/projects/{projectID}/knowledge/{family}", h.canonicalCreate)
	mux.HandleFunc("POST /internal/v1/companies/{companyID}/projects/{projectID}/knowledge/{family}/{recordID}/history/restore", h.canonicalRestore)
	mux.HandleFunc("GET /internal/v1/companies/{companyID}/projects/{projectID}/knowledge/history", h.canonicalHistory)
	mux.HandleFunc("GET /internal/v1/companies/{companyID}/projects/{projectID}", h.project)
	mux.HandleFunc("POST /internal/v1/companies/{companyID}/projects", h.register)
	mux.HandleFunc("POST /internal/v1/companies/{companyID}/projects/{projectID}/binding/first", h.bindFirst)
	mux.HandleFunc("POST /internal/v1/companies/{companyID}/projects/{projectID}/decline", h.decline)
	mux.HandleFunc("GET /internal/v1/companies/{companyID}/projects/{projectID}/preference", h.preference)
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
	mux.HandleFunc("GET /internal/v1/companies/{companyID}/projects/{projectID}/roadmap/releases/{releaseID}/investigation/template", h.releaseInvestigationTemplate)
	mux.HandleFunc("GET /internal/v1/companies/{companyID}/projects/{projectID}/roadmap/releases/{releaseID}/investigation", h.releaseInvestigation)
	mux.HandleFunc("PUT /internal/v1/companies/{companyID}/projects/{projectID}/roadmap/releases/{releaseID}/investigation", h.roadmapChange("set-investigation"))
	mux.HandleFunc("DELETE /internal/v1/companies/{companyID}/projects/{projectID}/roadmap/releases/{releaseID}/investigation", h.roadmapChange("clear-investigation"))
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
	mux.HandleFunc("POST /internal/v1/companies/{companyID}/projects/{projectID}/todos/{todoID}/review-waivers", h.grantTODOReviewWaiver)
	mux.HandleFunc("POST /internal/v1/companies/{companyID}/projects/{projectID}/todos/{todoID}/placement", h.editTODOPlacement)
	mux.HandleFunc("POST /internal/v1/companies/{companyID}/projects/{projectID}/todos/{todoID}/next-step", h.editTODONextStep)
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
		limit := int64(maxRequestBody)
		if action == "set-investigation" {
			limit = maxInvestigationBytes + 4<<10
		}
		if !h.decodeLimit(w, r, &input, limit) {
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

func (h *Handler) releaseInvestigation(w http.ResponseWriter, r *http.Request) {
	service, ok := h.service.(releaseInvestigationService)
	if !ok {
		h.writeError(w, ErrUnavailable)
		return
	}
	value, err := service.ReadReleaseInvestigation(r.Context(), strings.TrimSpace(r.PathValue("companyID")), strings.TrimSpace(r.PathValue("projectID")), strings.TrimSpace(r.PathValue("releaseID")))
	h.respond(w, value, err)
}

func (h *Handler) releaseInvestigationTemplate(w http.ResponseWriter, r *http.Request) {
	service, ok := h.service.(releaseInvestigationService)
	if !ok {
		h.writeError(w, ErrUnavailable)
		return
	}
	read, err := service.ReadReleaseInvestigation(r.Context(), strings.TrimSpace(r.PathValue("companyID")), strings.TrimSpace(r.PathValue("projectID")), strings.TrimSpace(r.PathValue("releaseID")))
	if err != nil {
		h.writeError(w, err)
		return
	}
	template := ReleaseInvestigationTemplate()
	write := template["write"].(map[string]any)
	write["project_id"], write["id"], write["expected_roadmap_revision"] = read.ProjectID, read.ReleaseID, read.RoadmapRevision
	h.respond(w, template, nil)
}

type errorResponse struct {
	Error        string                    `json:"error"`
	State        string                    `json:"state,omitempty"`
	Teach        string                    `json:"teach,omitempty"`
	TeachDetail  *CanonicalTeachDiagnostic `json:"teach_detail,omitempty"`
	TODORevision *int64                    `json:"todo_revision,omitempty"`
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
func (h *Handler) bindFirst(w http.ResponseWriter, r *http.Request) {
	var input FirstBindingRequest
	if !h.decode(w, r, &input) {
		return
	}
	value, err := h.service.BindFirst(r.Context(), strings.TrimSpace(r.PathValue("companyID")), strings.TrimSpace(r.PathValue("projectID")), input)
	h.respond(w, value, err)
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
func (h *Handler) preference(w http.ResponseWriter, r *http.Request) {
	value, err := h.service.Preference(r.Context(), strings.TrimSpace(r.PathValue("companyID")), strings.TrimSpace(r.PathValue("projectID")))
	h.respond(w, value, err)
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
	h.writeError(w, todoTeach("route", "use typed Mongo v2 TODO content admission; this Foundation selected-ID import route is disabled and performs no writes", ErrConflict))
}

func (h *Handler) admitAdditionalTODOs(w http.ResponseWriter, r *http.Request) {
	h.writeError(w, todoTeach("route", "use typed Mongo v2 TODO content admission; this selected-ID import route is disabled and performs no writes", ErrConflict))
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

func (h *Handler) grantTODOReviewWaiver(w http.ResponseWriter, r *http.Request) {
	service, ok := h.service.(todoHistoryService)
	if !ok {
		h.writeError(w, ErrUnavailable)
		return
	}
	var input TODOReviewWaiverRequest
	if !h.decodeLimit(w, r, &input, maxTODOImportRequestBytes) {
		return
	}
	if input.ProjectID != strings.TrimSpace(r.PathValue("projectID")) || input.TODOID != strings.TrimSpace(r.PathValue("todoID")) {
		h.writeError(w, todoTeach("project_id/todo_id", "match the exact Project and TODO path identities", ErrConflict))
		return
	}
	value, err := service.GrantTODOReviewWaiver(r.Context(), strings.TrimSpace(r.PathValue("companyID")), input)
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

func (h *Handler) editTODONextStep(w http.ResponseWriter, r *http.Request) {
	service, ok := h.service.(todoRecordMutationService)
	if !ok {
		h.writeError(w, ErrUnavailable)
		return
	}
	var input TODONextStepEditRequest
	if !h.decodeLimit(w, r, &input, maxTODOImportRequestBytes) {
		return
	}
	if input.ProjectID != strings.TrimSpace(r.PathValue("projectID")) || input.TODOID != strings.TrimSpace(r.PathValue("todoID")) {
		h.writeError(w, todoTeach("project_id/todo_id", "match the exact Project and TODO path identity", ErrConflict))
		return
	}
	value, err := service.EditTODONextStep(r.Context(), strings.TrimSpace(r.PathValue("companyID")), input)
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

func (h *Handler) canonicalTemplate(w http.ResponseWriter, r *http.Request) {
	h.respond(w, CanonicalTemplate(), nil)
}

func (h *Handler) canonicalManifest(w http.ResponseWriter, r *http.Request) {
	service, ok := h.service.(canonicalKnowledgeService)
	if !ok {
		h.writeError(w, ErrUnavailable)
		return
	}
	value, err := service.CanonicalManifest(r.Context(), r.PathValue("companyID"), r.PathValue("projectID"), r.URL.Query().Get("generation_id"))
	h.respond(w, value, err)
}

func (h *Handler) canonicalIndex(w http.ResponseWriter, r *http.Request) {
	service, ok := h.service.(canonicalIndexReader)
	if !ok {
		h.writeError(w, ErrUnavailable)
		return
	}
	q := r.URL.Query()
	if len(q) > 7 {
		h.writeError(w, ErrInvalid)
		return
	}
	for key, values := range q {
		if len(values) != 1 || key != "family" && key != "module_id" && key != "method" && key != "q" && key != "limit" && key != "cursor" && key != "decisions_only" {
			h.writeError(w, ErrInvalid)
			return
		}
	}
	query := CanonicalIndexQuery{Family: CanonicalFamily(q.Get("family")), ModuleID: q.Get("module_id"), Method: q.Get("method"), Q: q.Get("q"), Cursor: q.Get("cursor")}
	if value := q.Get("decisions_only"); value != "" {
		if value != "true" {
			h.writeError(w, ErrInvalid)
			return
		}
		query.DecisionsOnly = true
	}
	if value := q.Get("limit"); value != "" {
		var err error
		query.Limit, err = strconv.Atoi(value)
		if err != nil {
			h.writeError(w, ErrInvalid)
			return
		}
	}
	page, err := service.CanonicalIndex(r.Context(), r.PathValue("companyID"), r.PathValue("projectID"), query)
	h.respond(w, page, err)
}

func (h *Handler) canonicalImport(w http.ResponseWriter, r *http.Request) {
	service, ok := h.service.(canonicalKnowledgeService)
	if !ok {
		h.writeError(w, ErrUnavailable)
		return
	}
	var input CanonicalImport
	if !h.decodeCanonicalRequest(w, r, &input) {
		return
	}
	if input.ProjectID != r.PathValue("projectID") {
		h.writeError(w, todoTeach("project_id", "match the Project path identity", ErrConflict))
		return
	}
	value, err := service.ImportCanonicalDraft(r.Context(), r.PathValue("companyID"), input)
	h.respond(w, value, err)
}

func (h *Handler) canonicalCreate(w http.ResponseWriter, r *http.Request) {
	service, ok := h.service.(canonicalKnowledgeService)
	if !ok {
		h.writeError(w, ErrUnavailable)
		return
	}
	var input CanonicalMutation
	if !h.decodeCanonicalRequest(w, r, &input) {
		return
	}
	if input.ProjectID != r.PathValue("projectID") || input.Family != CanonicalFamily(r.PathValue("family")) {
		h.writeError(w, todoTeach("project_id/family", "match the resource path", ErrConflict))
		return
	}
	value, err := service.CreateCanonical(r.Context(), r.PathValue("companyID"), input)
	h.respond(w, value, err)
}

func (h *Handler) canonicalActivate(w http.ResponseWriter, r *http.Request) {
	service, ok := h.service.(canonicalKnowledgeService)
	if !ok {
		h.writeError(w, ErrUnavailable)
		return
	}
	var input struct {
		ProjectID    string `json:"project_id"`
		GenerationID string `json:"generation_id"`
		OperationID  string `json:"operation_id"`
		Actor        string `json:"actor"`
	}
	if !h.decodeCanonicalRequest(w, r, &input) {
		return
	}
	if input.ProjectID != r.PathValue("projectID") {
		h.writeError(w, todoTeach("project_id", "match the Project path identity", ErrConflict))
		return
	}
	value, err := service.ActivateCanonical(r.Context(), r.PathValue("companyID"), input.ProjectID, input.GenerationID, input.OperationID, input.Actor)
	h.respond(w, value, err)
}

func (h *Handler) canonicalRead(w http.ResponseWriter, r *http.Request) {
	service, ok := h.service.(canonicalKnowledgeService)
	if !ok {
		h.writeError(w, ErrUnavailable)
		return
	}
	query := r.URL.Query()
	if len(query["generation_id"]) > 1 || len(query["generation_id"]) == 1 && query.Get("generation_id") == "" {
		h.writeError(w, teachCanonical("", "generation_id", "provide one exact draft generation ID or omit this parameter for active knowledge", ErrInvalid))
		return
	}
	var value CanonicalRecord
	var err error
	if generationID := query.Get("generation_id"); generationID != "" {
		value, err = service.ReadCanonicalDraft(r.Context(), r.PathValue("companyID"), r.PathValue("projectID"), generationID, CanonicalFamily(r.PathValue("family")), r.PathValue("recordID"))
	} else {
		value, err = service.ReadCanonical(r.Context(), r.PathValue("companyID"), r.PathValue("projectID"), CanonicalFamily(r.PathValue("family")), r.PathValue("recordID"))
	}
	h.respond(w, value, err)
}

func (h *Handler) canonicalEdit(w http.ResponseWriter, r *http.Request) {
	service, ok := h.service.(canonicalKnowledgeService)
	if !ok {
		h.writeError(w, ErrUnavailable)
		return
	}
	var input CanonicalMutation
	if !h.decodeCanonicalRequest(w, r, &input) {
		return
	}
	if input.ProjectID != r.PathValue("projectID") || input.Family != CanonicalFamily(r.PathValue("family")) || input.Record.ID != r.PathValue("recordID") {
		h.writeError(w, todoTeach("project_id/family/record.id", "match the resource path", ErrConflict))
		return
	}
	value, err := service.EditCanonical(r.Context(), r.PathValue("companyID"), input)
	h.respond(w, value, err)
}

func (h *Handler) canonicalDelete(w http.ResponseWriter, r *http.Request) {
	service, ok := h.service.(canonicalKnowledgeService)
	if !ok {
		h.writeError(w, ErrUnavailable)
		return
	}
	var input CanonicalDeleteRequest
	if !h.decodeCanonicalRequest(w, r, &input) {
		return
	}
	if input.ProjectID != r.PathValue("projectID") || input.Family != CanonicalFamily(r.PathValue("family")) || input.RecordID != r.PathValue("recordID") {
		h.writeError(w, todoTeach("project_id/family/record_id", "match the resource path", ErrConflict))
		return
	}
	value, err := service.DeleteCanonical(r.Context(), r.PathValue("companyID"), input)
	h.respond(w, value, err)
}

func (h *Handler) canonicalRestore(w http.ResponseWriter, r *http.Request) {
	service, ok := h.service.(canonicalKnowledgeService)
	if !ok {
		h.writeError(w, ErrUnavailable)
		return
	}
	var input CanonicalRestoreRequest
	if !h.decodeCanonicalRequest(w, r, &input) {
		return
	}
	if input.ProjectID != r.PathValue("projectID") || input.Family != CanonicalFamily(r.PathValue("family")) || input.RecordID != r.PathValue("recordID") {
		h.writeError(w, todoTeach("project_id/family/record_id", "match the resource path", ErrConflict))
		return
	}
	value, err := service.RestoreCanonical(r.Context(), r.PathValue("companyID"), input)
	h.respond(w, value, err)
}

func (h *Handler) canonicalHistory(w http.ResponseWriter, r *http.Request) {
	service, ok := h.service.(canonicalKnowledgeService)
	if !ok {
		h.writeError(w, ErrUnavailable)
		return
	}
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			h.writeError(w, todoTeach("limit", "be an integer between 1 and 100", ErrInvalid))
			return
		}
		limit = parsed
	}
	var after time.Time
	if raw := r.URL.Query().Get("after"); raw != "" {
		parsed, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			h.writeError(w, todoTeach("after", "be an RFC3339 timestamp", ErrInvalid))
			return
		}
		after = parsed
	}
	value, err := service.CanonicalHistory(r.Context(), r.PathValue("companyID"), r.PathValue("projectID"), after, limit)
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
	var canonicalTeach *canonicalTeachError
	if errors.As(err, &canonicalTeach) {
		response.Teach = canonicalTeach.Error()
		response.TeachDetail = &canonicalTeach.Diagnostic
		if canonicalTeach.Diagnostic.Code == "canonical_pending" {
			response.State = "pending"
		}
	}
	var revisionConflict *todoRevisionConflict
	if errors.As(err, &revisionConflict) {
		response.TODORevision = &revisionConflict.Revision
		response.Teach = revisionConflict.Error()
	}
	_ = json.NewEncoder(w).Encode(response)
}
