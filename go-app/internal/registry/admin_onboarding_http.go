package registry

import (
	"context"
	"errors"
	"io"
	"net/http"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

func (h *githubAdminHTTP) onboardingAccess(ctx context.Context, mode string, t githubAdminTarget) (bool, error) {
	if mode == "company-create" {
		return h.canCreateCompany(t.operator), nil
	}
	if mode == "directory" {
		return true, nil
	}
	if t.company == "" {
		return false, nil
	}
	if (h.sharedAuthenticatedAccess && (mode == "company" || t.project == "")) || h.access.permits(t.operator, t.company) {
		return true, nil
	}
	if h.store == nil {
		return false, nil
	}
	company, err := h.store.company(ctx, t.company)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if (mode != "github" || t.project == "") && company.CreatedBy.permits(t.operator) {
		return true, nil
	}
	if mode != "github" || t.project == "" {
		return false, nil
	}
	project, err := h.store.readProjectDocument(ctx, company, t.project)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return project.CreatedBy.permits(t.operator), nil
}

func (h *githubAdminHTTP) canCreateCompany(operator githubOperator) bool {
	return h.sharedAuthenticatedAccess || h.companyCreators[operator.Subject]
}

func (h *githubAdminHTTP) registerOnboarding(mux *http.ServeMux) {
	const prefix = "/internal/v1/admin/companies"
	mux.HandleFunc("GET /internal/v1/admin/projects", h.protectMode(false, "directory", h.adminProjects))
	mux.HandleFunc("GET "+prefix, h.protectMode(false, "directory", h.adminDirectory))
	mux.HandleFunc("POST "+prefix, h.protectMode(false, "company-create", h.createCompany))
	mux.HandleFunc("GET "+prefix+"/{companyID}/github-pending-installation", h.protectMode(false, "company", h.pendingInstallation))
	const discovery = prefix + "/{companyID}/github-discovery"
	mux.HandleFunc("POST "+discovery, h.protectMode(false, "company", h.start))
	mux.HandleFunc("GET "+discovery+"/{operationID}", h.protectMode(false, "company", h.status))
	mux.HandleFunc("GET "+discovery+"/{operationID}/repositories", h.protectMode(false, "company", h.repositories))

	mux.HandleFunc("GET "+prefix+"/{companyID}/image", h.protectMode(false, "company", func(r *http.Request, t githubAdminTarget) (any, error) {
		return h.store.readAdminCompanyImage(r.Context(), t.company)
	}))
	mux.HandleFunc("POST "+prefix+"/{companyID}/projects", h.protectMode(false, "company", func(r *http.Request, t githubAdminTarget) (any, error) {
		var req adminRegistration
		if err := githubAdminDecode(r, &req); err != nil {
			return nil, err
		}
		return h.store.createAdminProject(r.Context(), t.company, t.operator, req)
	}))
}

func (h *githubAdminHTTP) adminDirectory(r *http.Request, t githubAdminTarget) (any, error) {
	type projectRow struct {
		ID              string `json:"id" bson:"id"`
		Name            string `json:"name" bson:"name"`
		BindingRevision int64  `json:"binding_revision" bson:"binding_revision"`
		RoadmapRevision int64  `json:"roadmap_revision" bson:"roadmap_revision"`
	}
	type row struct {
		ID       string       `json:"id"`
		Name     string       `json:"name"`
		Projects []projectRow `json:"projects"`
		HasImage bool         `json:"has_image"`
	}
	selected := githubQueryParameter(r, "company_id")
	if r.URL.RawQuery != "" && (!validID(selected) || len(r.URL.Query()) != 1) {
		return nil, ErrInvalid
	}
	var companies []Company
	if selected == "" {
		all, err := h.store.readCompanies(r.Context())
		if err != nil {
			return nil, err
		}
		companies = all
	} else {
		company, err := h.store.rawCompany(r.Context(), selected)
		if err == nil {
			companies = []Company{company}
		} else if !errors.Is(err, ErrNotFound) {
			return nil, err
		}
	}
	rows := make([]row, 0)
	for _, company := range companies {
		if !h.canReadAdminCompany(company, t.operator) {
			continue
		}
		entry := row{ID: company.ID, Name: company.Name, Projects: []projectRow{}, HasImage: company.HasAdminImage}
		if company.ID == selected {
			collection := h.store.client.Database(company.DatabaseName).Collection(projectsCollection)
			projection := options.Find().SetProjection(bson.M{"id": 1, "name": 1, "binding_revision": 1, "roadmap_revision": 1, "_id": 0})
			cursor, err := collection.Find(r.Context(), bson.M{"company_id": company.ID}, projection)
			if err != nil {
				return nil, normalizeMongoError(err)
			}
			if err := cursor.All(r.Context(), &entry.Projects); err != nil {
				return nil, normalizeMongoError(err)
			}
		}
		rows = append(rows, entry)
	}
	return struct {
		Companies []row `json:"companies"`
		CanCreate bool  `json:"can_create_company"`
	}{rows, h.canCreateCompany(t.operator)}, nil
}

func (h *githubAdminHTTP) createCompany(r *http.Request, t githubAdminTarget) (any, error) {
	reader, err := r.MultipartReader()
	if err != nil {
		return nil, ErrInvalid
	}
	seen := make(map[string]bool, 4)
	var request adminRegistration
	var image []byte
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, ErrInvalid
		}
		name := part.FormName()
		if seen[name] {
			return nil, ErrInvalid
		}
		seen[name] = true
		limit := int64(512)
		if name == "image" {
			limit = maxCompanyImage
		}
		content, err := io.ReadAll(io.LimitReader(part, limit+1))
		if err != nil || int64(len(content)) > limit {
			return nil, ErrInvalid
		}
		if err := part.Close(); err != nil {
			return nil, ErrInvalid
		}
		switch name {
		case "name":
			request.Name = string(content)
		case "slug":
			request.Slug = string(content)
		case "operation_id":
			request.Operation = string(content)
		case "image":
			if len(content) == 0 || part.FileName() == "" {
				return nil, ErrInvalid
			}
			image = content
		default:
			return nil, ErrInvalid
		}
	}
	return h.store.createAdminCompany(r.Context(), t.operator, request, image)
}
