package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	ac "belluga-builder-viewer/artifact_catalog"
)

const maxLandingJSONBytes = 1 << 20
const maxLandingFileBytes = 2 << 20

type landingProxyDocument struct {
	Family        string `json:"family"`
	ID            string `json:"id"`
	Revision      int64  `json:"revision"`
	ContentDigest string `json:"content_digest"`
}

type landingProxyFile struct {
	Path string `json:"path"`
	URL  string `json:"url"`
}

type landingProxyCatalog struct {
	State             string                `json:"state"`
	CompanyID         string                `json:"company_id,omitempty"`
	ProjectID         string                `json:"project_id,omitempty"`
	RepositoryID      string                `json:"repository_id,omitempty"`
	PublicationBranch string                `json:"publication_branch,omitempty"`
	BindingRevision   int64                 `json:"binding_revision,omitempty"`
	Revision          string                `json:"revision,omitempty"`
	InventoryDigest   string                `json:"inventory_digest,omitempty"`
	ManifestVersion   string                `json:"manifest_version,omitempty"`
	EntryPoint        string                `json:"entry_point,omitempty"`
	EntryURL          string                `json:"entry_url,omitempty"`
	KnowledgeOrigin   landingProxyKnowledge `json:"knowledge_origin,omitempty"`
	Files             []landingProxyFile    `json:"files,omitempty"`
	Teach             string                `json:"teach,omitempty"`
}

type landingProxyKnowledge struct {
	ProjectID    string                 `json:"project_id"`
	GenerationID string                 `json:"generation_id"`
	Documents    []landingProxyDocument `json:"documents"`
}

func (p *registeredPreview) landingAPIHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if strings.HasPrefix(r.URL.Path, "/api/landing/file/") {
		p.landingRelativeFile(w, r)
		return
	}
	companyID, companyOK := oneQueryValue(r.URL.Query(), "company_id")
	projectID, projectOK := oneQueryValue(r.URL.Query(), "project_id")
	if r.Method != http.MethodGet || r.ContentLength != 0 || len(r.TransferEncoding) != 0 || len(r.URL.RawQuery) > 2048 || len(r.URL.Query()) != 2 || !companyOK || !projectOK || !validRegistrationID(companyID) || !validRegistrationID(projectID) {
		landingProxyError(w, http.StatusBadRequest, "landing_request_invalid", "TEACH: Informe uma única identidade Company/Project válida para ler o Landing revisionado.")
		return
	}
	base, err := p.landingRegistryURL()
	if err != nil {
		landingProxyError(w, http.StatusServiceUnavailable, "landing_unavailable", "TEACH: A Registry de artefatos não está configurada para leitura Landing.")
		return
	}
	endpoint := "/internal/v1/companies/" + url.PathEscape(companyID) + "/projects/" + url.PathEscape(projectID) + "/landing"
	response, err := p.landingGet(r, http.MethodGet, base, endpoint)
	if err != nil {
		landingProxyError(w, http.StatusServiceUnavailable, "landing_unavailable", "TEACH: A Registry de artefatos está indisponível; nenhuma snapshot ou Markdown fallback foi consultado.")
		return
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		landingProxyNotAuthored(w, companyID, projectID, "TEACH: Este Project ainda não publicou um manifesto Landing v2 válido no Git vinculado.")
		return
	}
	if response.StatusCode == http.StatusBadRequest {
		teach := boundedRegistryDesignSystemTeach(response.Body)
		if teach == "" {
			teach = "TEACH: O manifesto Landing publicado não satisfaz o contrato v2; atualize o artefato fonte."
		}
		writeJSON(w, http.StatusOK, landingProxyCatalog{State: "invalid", CompanyID: companyID, ProjectID: projectID, Teach: teach})
		return
	}
	if response.StatusCode != http.StatusOK {
		landingProxyRegistryFailure(w, response)
		return
	}
	data, err := readBoundedLandingJSON(response.Body)
	if err != nil {
		landingProxyError(w, http.StatusServiceUnavailable, "landing_response_invalid", "TEACH: A Registry retornou um catálogo Landing inválido ou acima do limite.")
		return
	}
	var catalog landingProxyCatalog
	if json.Unmarshal(data, &catalog) != nil || !validLandingCatalog(catalog, companyID, projectID) {
		landingProxyError(w, http.StatusServiceUnavailable, "landing_response_invalid", "TEACH: Identidade, SHA, inventário ou origem Knowledge do catálogo Landing não validou.")
		return
	}
	for i := range catalog.Files {
		catalog.Files[i].URL = landingProxyFileURL(companyID, projectID, catalog.BindingRevision, catalog.Revision, catalog.InventoryDigest, catalog.Files[i].Path)
	}
	catalog.EntryURL = landingProxyFileURL(companyID, projectID, catalog.BindingRevision, catalog.Revision, catalog.InventoryDigest, catalog.EntryPoint)
	catalog.State = "available"
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(catalog)
}

func (p *registeredPreview) landingRelativeFile(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/landing/file/"), "/")
	if len(parts) < 7 || r.Method != http.MethodGet && r.Method != http.MethodHead || r.ContentLength != 0 || len(r.TransferEncoding) != 0 || len(r.URL.RawQuery) != 0 {
		landingProxyError(w, http.StatusBadRequest, "landing_file_invalid", "TEACH: Use um URL de arquivo emitido pelo catálogo Landing exato.")
		return
	}
	companyID, projectID := parts[0], parts[1]
	bindingRevision, err := strconv.ParseInt(parts[2], 10, 64)
	revision, digestHex := parts[3], parts[4]
	if !validRegistrationID(companyID) || !validRegistrationID(projectID) || err != nil || bindingRevision < 0 || !designSystemSHA.MatchString(revision) || !validDigest("sha256:"+digestHex) || parts[5] != "files" {
		landingProxyError(w, http.StatusBadRequest, "landing_file_invalid", "TEACH: Use a revisão Git, binding e digest completos emitidos pelo catálogo Landing.")
		return
	}
	filePath := strings.Join(parts[6:], "/")
	if ac.ValidateRelativePath(filePath) != nil || !strings.HasPrefix(filePath, "design/landing/") || !landingProxyMIMEType(filePath) || strings.Contains(r.URL.RawPath, "\\") || len(r.URL.RawQuery) != 0 {
		landingProxyError(w, http.StatusBadRequest, "landing_file_invalid", "TEACH: O arquivo deve pertencer ao inventário seguro design/landing da revisão selecionada.")
		return
	}
	base, err := p.landingRegistryURL()
	if err != nil {
		landingProxyError(w, http.StatusServiceUnavailable, "landing_unavailable", "TEACH: A Registry de artefatos está indisponível.")
		return
	}
	endpoint := "/internal/v1/companies/" + url.PathEscape(companyID) + "/projects/" + url.PathEscape(projectID) + "/landing/" + strconv.FormatInt(bindingRevision, 10) + "/" + revision + "/files/" + escapePrototypePath(filePath)
	response, err := p.landingGet(r, r.Method, base, endpoint)
	if err != nil {
		landingProxyError(w, http.StatusServiceUnavailable, "landing_unavailable", "TEACH: A Registry de artefatos está indisponível.")
		return
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusNotModified {
		landingProxyRegistryFailure(w, response)
		return
	}
	if response.Header.Get("X-Landing-Revision") != revision || response.Header.Get("X-Landing-Inventory-Digest") != "sha256:"+digestHex || !landingProxyMIMEType(filePath) {
		landingProxyError(w, http.StatusConflict, "landing_selection_changed", "TEACH: Binding, Knowledge ou inventário mudou; recarregue o catálogo Landing.")
		return
	}
	contentType := response.Header.Get("Content-Type")
	if contentType == "" || !landingProxyContentTypeAllowed(filePath, contentType) {
		landingProxyError(w, http.StatusServiceUnavailable, "landing_response_invalid", "TEACH: MIME do arquivo não corresponde ao inventário Landing.")
		return
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxLandingFileBytes+1))
	if err != nil || len(body) > maxLandingFileBytes {
		landingProxyError(w, http.StatusServiceUnavailable, "landing_response_invalid", "TEACH: O arquivo Landing excedeu o limite de 2 MiB.")
		return
	}
	for _, name := range []string{"Content-Type", "ETag", "X-Content-Type-Options", "X-Landing-Revision", "X-Landing-Inventory-Digest", "Cache-Control", "Referrer-Policy", "Content-Security-Policy"} {
		if value := response.Header.Get(name); value != "" && !strings.ContainsAny(value, "\r\n") {
			w.Header().Set(name, value)
		}
	}
	w.WriteHeader(response.StatusCode)
	if r.Method != http.MethodHead && response.StatusCode != http.StatusNotModified {
		_, _ = w.Write(body)
	}
}

func (p *registeredPreview) landingRegistryURL() (string, error) {
	base := strings.TrimRight(strings.TrimSpace(p.registryURL), "/")
	if base == "" {
		base = strings.TrimRight(strings.TrimSpace(env("DELPHI_BUILDER_REGISTRY_URL", "http://builder-registry:8080")), "/")
	}
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return "", errors.New("Registry URL invalid")
	}
	return base, nil
}

func (p *registeredPreview) landingGet(r *http.Request, method, base, endpoint string) (*http.Response, error) {
	u, _ := url.Parse(base)
	u.Path = endpoint
	request, err := http.NewRequestWithContext(r.Context(), method, u.String(), nil)
	if err != nil {
		return nil, err
	}
	if etag := r.Header.Get("If-None-Match"); etag != "" && len(etag) <= 512 && !strings.ContainsAny(etag, "\r\n") {
		request.Header.Set("If-None-Match", etag)
	}
	client := p.client
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	bounded := *client
	bounded.Timeout = 90 * time.Second
	bounded.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return bounded.Do(request)
}

func landingProxyFileURL(companyID, projectID string, bindingRevision int64, revision, digest, filePath string) string {
	return "/api/landing/file/" + url.PathEscape(companyID) + "/" + url.PathEscape(projectID) + "/" + strconv.FormatInt(bindingRevision, 10) + "/" + revision + "/" + strings.TrimPrefix(digest, "sha256:") + "/files/" + escapePrototypePath(filePath)
}

func validLandingCatalog(c landingProxyCatalog, companyID, projectID string) bool {
	if c.CompanyID != companyID || c.ProjectID != projectID || c.RepositoryID == "" || c.PublicationBranch == "" || c.BindingRevision < 0 || !designSystemSHA.MatchString(c.Revision) || !validDigest(c.InventoryDigest) || c.ManifestVersion != "2" || c.EntryPoint != "design/landing/index.html" || c.KnowledgeOrigin.ProjectID != projectID || c.KnowledgeOrigin.GenerationID == "" || len(c.KnowledgeOrigin.Documents) == 0 || len(c.Files) == 0 || len(c.Files) > 128 {
		return false
	}
	seen := make(map[string]struct{}, len(c.Files))
	entryFound := false
	for _, file := range c.Files {
		if ac.ValidateRelativePath(file.Path) != nil || !strings.HasPrefix(file.Path, "design/landing/") || !landingProxyMIMEType(file.Path) {
			return false
		}
		if _, duplicate := seen[file.Path]; duplicate {
			return false
		}
		seen[file.Path] = struct{}{}
		entryFound = entryFound || file.Path == c.EntryPoint
	}
	if !entryFound {
		return false
	}
	for _, document := range c.KnowledgeOrigin.Documents {
		if document.Family == "" || document.ID == "" || document.Revision < 1 || !validDigest(document.ContentDigest) {
			return false
		}
	}
	return true
}

func landingProxyMIMEType(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".html", ".htm", ".css", ".js", ".mjs", ".json", ".svg", ".png", ".jpg", ".jpeg", ".gif", ".webp", ".avif", ".woff", ".woff2", ".ttf", ".otf":
		return true
	default:
		return false
	}
}

func landingProxyContentTypeAllowed(path, contentType string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	mediaType := strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0])
	expected := extToLandingMIME(ext)
	switch expected {
	case "text/html", "text/css", "text/javascript", "application/json", "image/svg+xml", "image/png", "image/jpeg", "image/gif", "image/webp", "image/avif", "font/woff", "font/woff2", "font/ttf", "font/otf":
		return mediaType == expected
	default:
		return false
	}
}

func extToLandingMIME(ext string) string {
	switch ext {
	case ".html", ".htm":
		return "text/html"
	case ".css":
		return "text/css"
	case ".js", ".mjs":
		return "text/javascript"
	case ".json":
		return "application/json"
	case ".svg":
		return "image/svg+xml"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".avif":
		return "image/avif"
	case ".woff":
		return "font/woff"
	case ".woff2":
		return "font/woff2"
	case ".ttf":
		return "font/ttf"
	case ".otf":
		return "font/otf"
	default:
		return ""
	}
}

func readBoundedLandingJSON(body io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, maxLandingJSONBytes+1))
	if err != nil || len(data) > maxLandingJSONBytes || rejectDuplicateJSONKeys(data) != nil || !json.Valid(data) {
		return nil, errors.New("invalid bounded Landing JSON")
	}
	return data, nil
}

func landingProxyNotAuthored(w http.ResponseWriter, companyID, projectID, teach string) {
	writeJSON(w, http.StatusOK, landingProxyCatalog{State: "not_authored", CompanyID: companyID, ProjectID: projectID, Teach: teach})
}

func landingProxyError(w http.ResponseWriter, status int, code, teach string) {
	writeJSON(w, status, map[string]string{"error": code, "teach": teach})
}

func landingProxyRegistryFailure(w http.ResponseWriter, response *http.Response) {
	status, code, teach := http.StatusServiceUnavailable, "landing_unavailable", "TEACH: Registry não conseguiu ler o Landing atual. Nenhum fallback foi consultado."
	if response.StatusCode == http.StatusConflict {
		status, code, teach = http.StatusConflict, "landing_selection_changed", "TEACH: Binding ou Knowledge mudou; recarregue o catálogo Landing."
	} else if response.StatusCode == http.StatusNotFound {
		status, code, teach = http.StatusNotFound, "landing_file_not_found", "TEACH: O arquivo não está no inventário da revisão Landing selecionada."
	} else if response.StatusCode == http.StatusBadRequest {
		status, code, teach = http.StatusBadRequest, "landing_invalid", "TEACH: O manifesto Landing publicado não satisfaz o contrato v2; corrija o artefato fonte."
	}
	if message := boundedRegistryDesignSystemTeach(response.Body); message != "" {
		teach = message
	}
	landingProxyError(w, status, code, teach)
}
