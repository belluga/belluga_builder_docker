package main

import (
	"net/http"
)

type catalogAPIProject struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	CompanyID string `json:"company_id"`
}

type catalogAPIResponse struct {
	SchemaVersion string              `json:"schema_version"`
	Companies     []registeredCompany `json:"companies"`
	Projects      []catalogAPIProject `json:"projects"`
}

func (p *registeredPreview) catalogAPIHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet || r.URL.Path != "/api/catalog" || r.ContentLength != 0 || len(r.TransferEncoding) != 0 || r.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "catalog_request_invalid")
		return
	}
	catalog, err := p.knowledgeCatalog(r)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "catalog_unavailable")
		return
	}
	response := catalogAPIResponse{
		SchemaVersion: "1",
		Companies:     catalog.Companies,
		Projects:      make([]catalogAPIProject, 0, len(catalog.Projects)),
	}
	for _, project := range catalog.Projects {
		response.Projects = append(response.Projects, catalogAPIProject{ID: project.ID, Name: project.Name, CompanyID: project.CompanyID})
	}
	writeJSON(w, http.StatusOK, response)
}
