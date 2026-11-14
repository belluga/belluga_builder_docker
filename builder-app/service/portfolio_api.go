package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	ac "belluga-builder-viewer/artifact_catalog"
)

type portfolioProxyFile struct {
	Path        string `json:"path"`
	Digest      string `json:"content_digest"`
	URL         string `json:"url"`
	ContentType string `json:"content_type"`
}
type portfolioProxyProject struct {
	CompanyID         string               `json:"company_id"`
	ProjectID         string               `json:"project_id"`
	State             string               `json:"state"`
	Teach             string               `json:"teach,omitempty"`
	RepositoryID      string               `json:"repository_id,omitempty"`
	PublicationBranch string               `json:"publication_branch,omitempty"`
	BindingRevision   int64                `json:"binding_revision,omitempty"`
	Revision          string               `json:"revision,omitempty"`
	InventoryDigest   string               `json:"inventory_digest,omitempty"`
	SchemaVersion     string               `json:"schema_version,omitempty"`
	Summary           string               `json:"summary,omitempty"`
	Files             []portfolioProxyFile `json:"files,omitempty"`
}
type portfolioProxyCatalog struct {
	CompanyID string                  `json:"company_id"`
	Projects  []portfolioProxyProject `json:"projects"`
}

func (p *registeredPreview) portfolioAPIHandler(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/portfolios/file/") {
		p.portfolioRelativeFile(w, r)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	query := r.URL.Query()
	companyID := strings.TrimSpace(query.Get("company_id"))
	if len(query) != 1 || len(query["company_id"]) != 1 || !validRegistrationID(companyID) {
		landingProxyError(w, http.StatusBadRequest, "portfolio_request_invalid", "TEACH: Informe uma única identidade Company válida para ler o catálogo de portfólios.")
		return
	}
	base, err := p.landingRegistryURL()
	if err != nil {
		landingProxyError(w, http.StatusServiceUnavailable, "portfolio_unavailable", "TEACH: Registry indisponível; nenhum snapshot ou Markdown foi consultado.")
		return
	}
	response, err := p.landingGet(r, http.MethodGet, base, "/internal/v1/companies/"+url.PathEscape(companyID)+"/portfolios")
	if err != nil {
		landingProxyError(w, http.StatusServiceUnavailable, "portfolio_unavailable", "TEACH: Registry indisponível; nenhum snapshot ou Markdown foi consultado.")
		return
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		portfolioRegistryFailure(w, response)
		return
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (2<<20)+1))
	var catalog portfolioProxyCatalog
	if err != nil || len(data) > 2<<20 || rejectDuplicateJSONKeys(data) != nil || json.Unmarshal(data, &catalog) != nil || catalog.CompanyID != companyID || catalog.Projects == nil || len(catalog.Projects) > 1000 {
		landingProxyError(w, http.StatusServiceUnavailable, "portfolio_response_invalid", "TEACH: A Registry retornou catálogo de portfólios inválido ou acima do limite.")
		return
	}
	seen := map[string]bool{}
	for i := range catalog.Projects {
		item := &catalog.Projects[i]
		if !validRegistrationID(item.ProjectID) || item.CompanyID != companyID || seen[item.ProjectID] {
			landingProxyError(w, http.StatusServiceUnavailable, "portfolio_response_invalid", "TEACH: Identidade Company/Project do catálogo de portfólios não validou.")
			return
		}
		seen[item.ProjectID] = true
		if item.State != "available" {
			if item.State != "not_authored" && item.State != "invalid" && item.State != "unavailable" {
				landingProxyError(w, http.StatusServiceUnavailable, "portfolio_response_invalid", "TEACH: Estado do portfólio não reconhecido.")
				return
			}
			continue
		}
		if item.BindingRevision < 0 || !designSystemSHA.MatchString(item.Revision) || !validDigest(item.InventoryDigest) || len(item.Files) == 0 || len(item.Files) > 2 {
			landingProxyError(w, http.StatusServiceUnavailable, "portfolio_response_invalid", "TEACH: A seleção do portfólio não contém SHA e inventário válidos.")
			return
		}
		hasSummary := false
		for j := range item.Files {
			file := &item.Files[j]
			if (file.Path != "project_portfolio.json" && file.Path != "project_portfolio/cover.png") || ac.ValidateRelativePath(file.Path) != nil || !validDigest(file.Digest) || file.ContentType != portfolioProxyMIMEType(file.Path) {
				landingProxyError(w, http.StatusServiceUnavailable, "portfolio_response_invalid", "TEACH: O inventário do portfólio contém caminho, MIME ou digest inválido.")
				return
			}
			if file.Path == "project_portfolio.json" {
				hasSummary = true
			}
			file.URL = portfolioProxyFileURL(companyID, item.ProjectID, item.BindingRevision, item.Revision, strings.TrimPrefix(item.InventoryDigest, "sha256:"), file.Path)
		}
		if (hasSummary && (item.SchemaVersion != "1" || strings.TrimSpace(item.Summary) == "")) || (!hasSummary && (item.SchemaVersion != "" || item.Summary != "")) {
			landingProxyError(w, http.StatusServiceUnavailable, "portfolio_response_invalid", "TEACH: A versão e o resumo do portfólio devem corresponder ao arquivo de resumo publicado.")
			return
		}
	}
	writeJSON(w, http.StatusOK, catalog)
}

func (p *registeredPreview) portfolioRelativeFile(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/portfolios/file/"), "/")
	if (r.Method != http.MethodGet && r.Method != http.MethodHead) || len(parts) < 7 {
		landingProxyError(w, http.StatusBadRequest, "portfolio_file_invalid", "TEACH: Use somente arquivo emitido pelo catálogo de portfólios.")
		return
	}
	companyID, projectID := parts[0], parts[1]
	bindingRevision, err := strconv.ParseInt(parts[2], 10, 64)
	revision, inventory := parts[3], parts[4]
	filePath := strings.Join(parts[6:], "/")
	if err != nil || bindingRevision < 0 || parts[5] != "files" || !validRegistrationID(companyID) || !validRegistrationID(projectID) || !designSystemSHA.MatchString(revision) || !validDigest("sha256:"+inventory) || ac.ValidateRelativePath(filePath) != nil || portfolioProxyMIMEType(filePath) == "" || len(r.URL.Query()) != 0 || strings.Contains(r.URL.RawPath, "\\") {
		landingProxyError(w, http.StatusBadRequest, "portfolio_file_invalid", "TEACH: Arquivo, revisão e inventário devem corresponder ao catálogo fixado.")
		return
	}
	base, err := p.landingRegistryURL()
	if err != nil {
		landingProxyError(w, http.StatusServiceUnavailable, "portfolio_unavailable", "TEACH: Registry de portfólios indisponível.")
		return
	}
	endpoint := "/internal/v1/companies/" + companyID + "/projects/" + projectID + "/portfolio/" + strconv.FormatInt(bindingRevision, 10) + "/" + revision + "/" + inventory + "/files/" + filePath
	response, err := p.landingGet(r, r.Method, base, endpoint)
	if err != nil {
		landingProxyError(w, http.StatusServiceUnavailable, "portfolio_unavailable", "TEACH: Registry de portfólios indisponível.")
		return
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusNotModified {
		if response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusBadRequest {
			landingProxyError(w, response.StatusCode, "portfolio_file_invalid", "TEACH: O arquivo não pertence à seleção Portfolio atual; recarregue o catálogo.")
			return
		}
		portfolioRegistryFailure(w, response)
		return
	}
	contentType := response.Header.Get("Content-Type")
	if contentType != portfolioProxyMIMEType(filePath) || response.Header.Get("X-Portfolio-Revision") != revision || response.Header.Get("X-Portfolio-Inventory-Digest") != "sha256:"+inventory {
		landingProxyError(w, http.StatusConflict, "portfolio_selection_changed", "TEACH: Binding ou inventário mudou; recarregue o catálogo.")
		return
	}
	var data []byte
	if r.Method == http.MethodGet && response.StatusCode == http.StatusOK {
		data, err = io.ReadAll(io.LimitReader(response.Body, (2<<20)+1))
		if err != nil || len(data) > 2<<20 {
			landingProxyError(w, http.StatusServiceUnavailable, "portfolio_file_invalid", "TEACH: Arquivo excedeu o limite de 2 MiB.")
			return
		}
	}
	w.Header().Set("Content-Type", contentType)
	if response.StatusCode == http.StatusOK {
		w.Header().Set("Content-Length", response.Header.Get("Content-Length"))
	}
	w.Header().Set("ETag", response.Header.Get("ETag"))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=0, must-revalidate")
	w.WriteHeader(response.StatusCode)
	if r.Method != http.MethodHead && response.StatusCode != http.StatusNotModified {
		_, _ = w.Write(data)
	}
}

func portfolioProxyFileURL(companyID, projectID string, bindingRevision int64, revision, inventory, path string) string {
	return "/api/portfolios/file/" + url.PathEscape(companyID) + "/" + url.PathEscape(projectID) + "/" + strconv.FormatInt(bindingRevision, 10) + "/" + revision + "/" + inventory + "/files/" + escapePrototypePath(path)
}
func portfolioProxyMIMEType(path string) string {
	switch path {
	case "project_portfolio.json":
		return "application/json"
	case "project_portfolio/cover.png":
		return "image/png"
	default:
		return ""
	}
}
func portfolioRegistryFailure(w http.ResponseWriter, response *http.Response) {
	status, code, teach := http.StatusServiceUnavailable, "portfolio_unavailable", "TEACH: Registry não conseguiu ler a publicação Portfolio atual; nenhum fallback foi consultado."
	switch response.StatusCode {
	case http.StatusNotFound:
		status, code, teach = http.StatusOK, "portfolio_not_authored", "TEACH: Este Project ainda não publicou os arquivos Portfolio v1 no Git vinculado."
	case http.StatusBadRequest:
		status, code, teach = http.StatusOK, "portfolio_invalid", "TEACH: Os arquivos Portfolio publicados não satisfazem o contrato v1."
	case http.StatusConflict:
		status, code, teach = http.StatusConflict, "portfolio_selection_changed", "TEACH: Binding ou revisão mudou; recarregue o catálogo."
	}
	landingProxyError(w, status, code, teach)
}
