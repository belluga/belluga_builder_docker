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

const maxPrototypeJSONBytes = 4 << 20

func (p *registeredPreview) prototypeAPIHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if strings.HasPrefix(r.URL.Path, "/api/prototypes/file/") {
		p.prototypeRelativeFile(w, r)
		return
	}
	companyID, companyOK := oneQueryValue(r.URL.Query(), "company_id")
	projectID, projectOK := oneQueryValue(r.URL.Query(), "project_id")
	if r.Method != http.MethodGet && r.Method != http.MethodHead || r.ContentLength != 0 || len(r.TransferEncoding) != 0 || len(r.URL.RawQuery) > 4096 || !companyOK || !projectOK || !validRegistrationID(companyID) || !validRegistrationID(projectID) {
		prototypeError(w, http.StatusBadRequest, "prototype_request_invalid", "TEACH: Informe Company e Project uma única vez e use uma revisão Git exata para conteúdo.")
		return
	}
	if !p.prototypeProjectRegistered(companyID, projectID) {
		prototypeError(w, http.StatusNotFound, "prototype_not_found", "TEACH: O Project selecionado não possui um Prototype publicado nesta revisão.")
		return
	}
	path := strings.TrimSuffix(r.URL.Path, "/")
	if path == "/api/prototypes" {
		if r.Method != http.MethodGet || len(r.URL.Query()) != 2 {
			prototypeError(w, http.StatusMethodNotAllowed, "prototype_method_invalid", "TEACH: O catálogo de Prototypes aceita somente leitura GET.")
			return
		}
		p.proxyPrototypeCatalog(w, r, companyID, projectID)
		return
	}
	parts := strings.Split(strings.TrimPrefix(path, "/api/prototypes/"), "/")
	if len(parts) >= 3 && parts[1] == "files" {
		revision, filePath := parts[0], strings.Join(parts[2:], "/")
		digest, digestOK := oneQueryValue(r.URL.Query(), "inventory_digest")
		queryValid := len(r.URL.Query()) == 3 && digestOK && validDigest(digest)
		if len(r.URL.Query()) == 4 {
			state, stateOK := oneQueryValue(r.URL.Query(), "prototype_state")
			queryValid = digestOK && validDigest(digest) && stateOK && safePrototypeID(state) && strings.EqualFold(filepath.Ext(filePath), ".html") && len(r.URL.Query()["prototype_state"]) == 1
		}
		if !queryValid || !designSystemSHA.MatchString(revision) || ac.ValidateRelativePath(filePath) != nil || len([]byte(filePath)) > ac.MaxPathBytes || strings.Contains(r.URL.RawPath, "\\") {
			prototypeError(w, http.StatusBadRequest, "prototype_file_invalid", "TEACH: Use um caminho relativo declarado no Prototype e sua revisão Git completa.")
			return
		}
		p.proxyPrototypeFile(w, r, companyID, projectID, revision, digest, filePath)
		return
	}
	if len(r.URL.Query()) != 2 || len(parts) != 2 || !designSystemSHA.MatchString(parts[0]) || !safePrototypeID(parts[1]) || r.Method != http.MethodGet {
		prototypeError(w, http.StatusNotFound, "prototype_not_found", "TEACH: O Prototype ou a revisão selecionada não está disponível.")
		return
	}
	p.proxyPrototypeDetail(w, r, companyID, projectID, parts[0], parts[1])
}

func (p *registeredPreview) prototypeRelativeFile(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/prototypes/file/"), "/")
	if len(parts) < 6 || r.Method != http.MethodGet && r.Method != http.MethodHead || r.ContentLength != 0 || len(r.TransferEncoding) != 0 || len(r.URL.RawQuery) > 4096 {
		prototypeError(w, http.StatusBadRequest, "prototype_file_invalid", "TEACH: Use a URL de arquivo emitida pelo detalhe do Prototype.")
		return
	}
	companyID, projectID, revision, digestHex := parts[0], parts[1], parts[2], parts[3]
	if parts[4] != "files" {
		prototypeError(w, http.StatusBadRequest, "prototype_file_invalid", "TEACH: Use a URL de arquivo emitida pelo detalhe do Prototype.")
		return
	}
	filePath := strings.Join(parts[5:], "/")
	state, hasState := oneQueryValue(r.URL.Query(), "prototype_state")
	if !validRegistrationID(companyID) || !validRegistrationID(projectID) || !designSystemSHA.MatchString(revision) || !validDigest("sha256:"+digestHex) || ac.ValidateRelativePath(filePath) != nil || len([]byte(filePath)) > ac.MaxPathBytes || strings.Contains(r.URL.RawPath, "\\") || len(r.URL.Query()) > 1 || len(r.URL.Query()) == 1 && !hasState || hasState && (len(r.URL.Query()["prototype_state"]) != 1 || !safePrototypeID(state) || !strings.EqualFold(filepath.Ext(filePath), ".html")) {
		prototypeError(w, http.StatusBadRequest, "prototype_file_invalid", "TEACH: Use um arquivo declarado na revisão Git exata do Prototype.")
		return
	}
	if !p.prototypeProjectRegistered(companyID, projectID) {
		prototypeError(w, http.StatusNotFound, "prototype_not_found", "TEACH: O Project selecionado não possui um Prototype publicado nesta revisão.")
		return
	}
	p.proxyPrototypeFile(w, r, companyID, projectID, revision, "sha256:"+digestHex, filePath)
}

func (p *registeredPreview) prototypeProjectRegistered(companyID, projectID string) bool {
	data, err := p.catalog()
	if err != nil {
		return false
	}
	for _, project := range data.Projects {
		if project.CompanyID == companyID && project.ID == projectID {
			return true
		}
	}
	return false
}

func (p *registeredPreview) proxyPrototypeCatalog(w http.ResponseWriter, r *http.Request, companyID, projectID string) {
	endpoint := "/internal/v1/companies/" + url.PathEscape(companyID) + "/projects/" + url.PathEscape(projectID) + "/prototypes"
	response, err := p.prototypeGet(r, http.MethodGet, endpoint)
	if err != nil {
		prototypeError(w, http.StatusServiceUnavailable, "prototype_unavailable", "TEACH: A leitura atual dos Prototypes está indisponível; verifique a Registry e tente novamente.")
		return
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		p.forwardPrototypeFailure(w, response)
		return
	}
	data, err := readBoundedPrototypeJSON(response.Body)
	if err != nil {
		prototypeError(w, http.StatusServiceUnavailable, "prototype_response_invalid", "TEACH: O catálogo da Registry excedeu o limite ou não contém JSON válido.")
		return
	}
	var catalog struct {
		RepositoryID        string `json:"repository_id"`
		PublicationBranch   string `json:"publication_branch"`
		BindingRevision     int64  `json:"binding_revision"`
		Revision            string `json:"revision"`
		EvaluatorVersion    string `json:"evaluator_version"`
		InventoryDigest     string `json:"inventory_digest"`
		KnowledgeGeneration string `json:"knowledge_generation_id,omitempty"`
		KnowledgeDigest     string `json:"knowledge_digest"`
		Items               []struct {
			ID          string  `json:"id"`
			Name        string  `json:"name"`
			Description *string `json:"description"`
			Root        string  `json:"root"`
			Status      string  `json:"status"`
			DetailURL   string  `json:"detail_url"`
		} `json:"items"`
	}
	if json.Unmarshal(data, &catalog) != nil || !designSystemSHA.MatchString(catalog.Revision) || !validDigest(catalog.InventoryDigest) || !validDigest("sha256:"+catalog.KnowledgeDigest) || catalog.Items == nil {
		prototypeError(w, http.StatusServiceUnavailable, "prototype_response_invalid", "TEACH: A identidade, revisão ou digest do catálogo retornado pela Registry é inválido.")
		return
	}
	for i := range catalog.Items {
		item := &catalog.Items[i]
		if !safePrototypeID(item.ID) || item.Name == "" || len(item.Name) > 256 || (item.Status != "active" && item.Status != "archived") {
			prototypeError(w, http.StatusServiceUnavailable, "prototype_response_invalid", "TEACH: Um item do catálogo da Registry não atende ao contrato público.")
			return
		}
		item.DetailURL = prototypePublicURL(companyID, projectID, catalog.Revision, item.ID)
	}
	writeJSON(w, http.StatusOK, catalog)
}

func (p *registeredPreview) proxyPrototypeDetail(w http.ResponseWriter, r *http.Request, companyID, projectID, revision, id string) {
	endpoint := "/internal/v1/companies/" + url.PathEscape(companyID) + "/projects/" + url.PathEscape(projectID) + "/prototypes/" + revision + "/" + url.PathEscape(id)
	response, err := p.prototypeGet(r, http.MethodGet, endpoint)
	if err != nil {
		prototypeError(w, http.StatusServiceUnavailable, "prototype_unavailable", "TEACH: A leitura atual do Prototype está indisponível.")
		return
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		p.forwardPrototypeFailure(w, response)
		return
	}
	data, err := readBoundedPrototypeJSON(response.Body)
	if err != nil {
		prototypeError(w, http.StatusServiceUnavailable, "prototype_response_invalid", "TEACH: A resposta de detalhe da Registry excedeu o limite ou não contém JSON válido.")
		return
	}
	var detail struct {
		RepositoryID        string          `json:"repository_id"`
		PublicationBranch   string          `json:"publication_branch"`
		BindingRevision     int64           `json:"binding_revision"`
		Revision            string          `json:"revision"`
		EvaluatorVersion    string          `json:"evaluator_version"`
		InventoryDigest     string          `json:"inventory_digest"`
		KnowledgeGeneration string          `json:"knowledge_generation_id,omitempty"`
		KnowledgeDigest     string          `json:"knowledge_digest"`
		Item                json.RawMessage `json:"item"`
		EntryURL            string          `json:"entry_url"`
		FileURLs            []struct {
			Path string `json:"path"`
			URL  string `json:"url"`
		} `json:"file_urls"`
	}
	if json.Unmarshal(data, &detail) != nil || detail.Revision != revision || !validDigest(detail.InventoryDigest) || !validDigest("sha256:"+detail.KnowledgeDigest) || len(detail.Item) == 0 || detail.FileURLs == nil {
		prototypeError(w, http.StatusServiceUnavailable, "prototype_response_invalid", "TEACH: Identidade e digest do Prototype não correspondem à revisão solicitada.")
		return
	}
	var item struct {
		ID         string `json:"id"`
		Root       string `json:"root"`
		EntryPoint string `json:"entry_point"`
	}
	if json.Unmarshal(detail.Item, &item) != nil || item.ID != id || ac.ValidateRelativePath(item.Root) != nil || ac.ValidateRelativePath(item.EntryPoint) != nil {
		prototypeError(w, http.StatusServiceUnavailable, "prototype_response_invalid", "TEACH: O Prototype retornado não contém uma tela inicial válida para esta identidade.")
		return
	}
	entryPath := strings.TrimSuffix(item.Root, "/") + "/" + item.EntryPoint
	for i := range detail.FileURLs {
		item := &detail.FileURLs[i]
		if ac.ValidateRelativePath(item.Path) != nil || len([]byte(item.Path)) > ac.MaxPathBytes {
			prototypeError(w, http.StatusServiceUnavailable, "prototype_response_invalid", "TEACH: A Registry retornou um caminho de arquivo inválido.")
			return
		}
		item.URL = prototypeFilePublicURL(companyID, projectID, revision, detail.InventoryDigest, item.Path)
		if item.Path == entryPath {
			detail.EntryURL = item.URL
		}
	}
	if detail.EntryURL == "" {
		prototypeError(w, http.StatusServiceUnavailable, "prototype_response_invalid", "TEACH: A tela inicial não foi declarada nos arquivos desta revisão do Prototype.")
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (p *registeredPreview) proxyPrototypeFile(w http.ResponseWriter, r *http.Request, companyID, projectID, revision, requestedDigest, filePath string) {
	endpoint := "/internal/v1/companies/" + url.PathEscape(companyID) + "/projects/" + url.PathEscape(projectID) + "/prototypes/" + revision + "/files/" + escapePrototypePath(filePath)
	response, err := p.prototypeGet(r, r.Method, endpoint)
	if err != nil {
		prototypeError(w, http.StatusServiceUnavailable, "prototype_unavailable", "TEACH: O arquivo do Prototype está indisponível nesta revisão.")
		return
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusNotModified {
		p.forwardPrototypeFailure(w, response)
		return
	}
	contentType, etag := response.Header.Get("Content-Type"), response.Header.Get("ETag")
	digest := response.Header.Get("X-Prototype-Inventory-Digest")
	if response.Header.Get("X-Prototype-Revision") != revision || digest != requestedDigest || etag == "" || len(etag) > 256 || strings.ContainsAny(etag, "\r\n") || response.StatusCode == http.StatusOK && (contentType == "" || len(contentType) > 128 || strings.ContainsAny(contentType, "\r\n")) || response.StatusCode == http.StatusNotModified && contentType != "" && (len(contentType) > 128 || strings.ContainsAny(contentType, "\r\n")) {
		prototypeError(w, http.StatusServiceUnavailable, "prototype_file_invalid", "TEACH: Metadados de revisão ou digest do arquivo são inválidos.")
		return
	}
	copyHeaderSafe(w, "ETag", etag)
	w.Header().Set("X-Prototype-Revision", revision)
	w.Header().Set("X-Prototype-Inventory-Digest", digest)
	w.Header().Set("Cache-Control", "private, max-age=0, must-revalidate")
	w.Header().Set("Content-Security-Policy", prototypeFilePolicy(filePath, contentType))
	if response.StatusCode == http.StatusNotModified {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if r.Method == http.MethodHead {
		length, parseErr := strconv.ParseInt(response.Header.Get("Content-Length"), 10, 64)
		if parseErr != nil || length < 0 || length > ac.MaxFileBytes {
			prototypeError(w, http.StatusServiceUnavailable, "prototype_file_invalid", "TEACH: O tamanho anunciado do arquivo selecionado excedeu o limite de leitura.")
			return
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Content-Length", strconv.FormatInt(length, 10))
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusOK)
		return
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, ac.MaxFileBytes+1))
	if err != nil || int64(len(data)) > ac.MaxFileBytes {
		prototypeError(w, http.StatusServiceUnavailable, "prototype_file_invalid", "TEACH: O arquivo selecionado excedeu o limite de leitura.")
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(data)
	}
}

func prototypePublicURL(companyID, projectID, revision, id string) string {
	return "/api/prototypes/" + revision + "/" + url.PathEscape(id) + "?company_id=" + url.QueryEscape(companyID) + "&project_id=" + url.QueryEscape(projectID)
}
func prototypeFilePublicURL(companyID, projectID, revision, digest, filePath string) string {
	return "/api/prototypes/file/" + url.PathEscape(companyID) + "/" + url.PathEscape(projectID) + "/" + revision + "/" + strings.TrimPrefix(digest, "sha256:") + "/files/" + escapePrototypePath(filePath)
}

func escapePrototypePath(value string) string {
	parts := strings.Split(value, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return strings.Join(parts, "/")
}
func safePrototypeID(value string) bool {
	if len(value) == 0 || len(value) > 64 || value[0] < 'a' || value[0] > 'z' {
		return false
	}
	for _, c := range value[1:] {
		if c < 'a' || c > 'z' {
			if c < '0' || c > '9' {
				if c != '-' {
					return false
				}
			}
		}
	}
	return true
}
func prototypeFilePolicy(filePath, contentType string) string {
	ext := ""
	if dot := strings.LastIndex(filePath, "."); dot >= 0 {
		ext = filePath[dot+1:]
	}
	if strings.EqualFold(ext, "html") || strings.HasPrefix(contentType, "text/html") {
		return "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self'; connect-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'self'; sandbox allow-scripts"
	}
	return "default-src 'none'; sandbox; frame-ancestors 'self'"
}
func prototypeError(w http.ResponseWriter, status int, code, teach string) {
	writeJSON(w, status, map[string]string{"error": code, "teach": teach})
}
func readBoundedPrototypeJSON(body io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, maxPrototypeJSONBytes+1))
	if err != nil || len(data) > maxPrototypeJSONBytes || rejectDuplicateJSONKeys(data) != nil || !json.Valid(data) {
		return nil, errors.New("invalid bounded JSON")
	}
	return data, nil
}
func (p *registeredPreview) forwardPrototypeFailure(w http.ResponseWriter, response *http.Response) {
	status, code, teach := http.StatusServiceUnavailable, "prototype_unavailable", "TEACH: A Registry não conseguiu ler o Prototype atual."
	if response.StatusCode == http.StatusNotFound {
		status, code, teach = http.StatusNotFound, "prototype_not_found", "TEACH: O Prototype ou arquivo não está disponível na revisão vinculada."
	} else if response.StatusCode == http.StatusConflict {
		status, code, teach = http.StatusConflict, "prototype_selection_changed", "TEACH: A revisão do Prototype mudou durante a leitura; recarregue o catálogo."
	}
	if message := boundedRegistryDesignSystemTeach(response.Body); message != "" {
		teach = message
	}
	prototypeError(w, status, code, teach)
}
func (p *registeredPreview) prototypeGet(r *http.Request, method, endpoint string) (*http.Response, error) {
	base := strings.TrimRight(strings.TrimSpace(p.registryURL), "/")
	if base == "" {
		base = strings.TrimRight(strings.TrimSpace(env("DELPHI_BUILDER_REGISTRY_URL", "http://builder-registry:8080")), "/")
	}
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return nil, errors.New("Registry URL invalid")
	}
	u.Path = endpoint
	request, err := http.NewRequestWithContext(r.Context(), method, u.String(), nil)
	if err != nil {
		return nil, err
	}
	if value := r.Header.Get("If-None-Match"); value != "" && len(value) <= 512 && !strings.ContainsAny(value, "\r\n") {
		request.Header.Set("If-None-Match", value)
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
func copyHeaderSafe(w http.ResponseWriter, name, value string) {
	if !strings.ContainsAny(value, "\r\n") {
		w.Header().Set(name, value)
	}
}
