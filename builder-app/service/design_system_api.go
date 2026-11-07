package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	ac "belluga-builder-viewer/artifact_catalog"
)

const maxDesignSystemJSONBytes = 4 << 20

var designSystemSHA = regexp.MustCompile(`^[a-f0-9]{40}$`)

func designSystemError(w http.ResponseWriter, status int, code, teach string) {
	writeJSON(w, status, map[string]string{"error": code, "teach": teach})
}

func (p *registeredPreview) designSystemAPIHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	query := r.URL.Query()
	companyID, companyOK := oneQueryValue(query, "company_id")
	projectID, projectOK := oneQueryValue(query, "project_id")
	revision, revisionOK := oneQueryValue(query, "revision")
	assetPath, assetOK := oneQueryValue(query, "path")
	presentation := strings.HasPrefix(r.URL.Path, "/api/design-system/presentation/")
	if presentation {
		parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/api/design-system/presentation/"), "/", 4)
		if len(parts) == 4 {
			companyID, projectID, revision, assetPath = parts[0], parts[1], parts[2], parts[3]
			companyOK, projectOK, revisionOK, assetOK = true, true, true, true
		}
	}
	assetRequest := r.URL.Path == "/api/design-system/assets" || presentation
	validQuery := !assetRequest && len(query) == 2 || !assetRequest && len(query) == 3 && revisionOK
	if assetRequest {
		validQuery = (presentation && len(query) == 0 || !presentation && len(query) == 4) && revisionOK && assetOK && designSystemSHA.MatchString(revision) && ac.ValidateRelativePath(assetPath) == nil && len([]byte(assetPath)) <= ac.MaxPathBytes
	}
	if r.Method != http.MethodGet || r.ContentLength != 0 || len(r.TransferEncoding) != 0 || len(r.URL.RawQuery) > 4096 || !companyOK || !projectOK || !validRegistrationID(companyID) || !validRegistrationID(projectID) || !validQuery || revisionOK && (revision == "" || !designSystemSHA.MatchString(revision)) {
		designSystemError(w, http.StatusBadRequest, "design_system_request_invalid", "TEACH: Informe Company e Project uma única vez e, para conteúdo ou assets, uma SHA Git exata.")
		return
	}
	endpoint := "/internal/v1/companies/" + url.PathEscape(companyID) + "/projects/" + url.PathEscape(projectID) + "/design-system"
	if assetRequest {
		endpoint += "/" + revision + "/assets/" + assetPath
	} else if revisionOK {
		endpoint += "/" + url.PathEscape(revision)
	}
	response, err := p.designSystemGet(r, endpoint)
	if err != nil {
		designSystemError(w, http.StatusServiceUnavailable, "design_system_unavailable", "TEACH: A leitura atual do Design System está indisponível. Verifique a Registry e tente novamente.")
		return
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK && !(assetRequest && response.StatusCode == http.StatusNotModified) {
		status, code, fallback := http.StatusServiceUnavailable, "design_system_unavailable", "TEACH: A leitura atual do Design System está indisponível. Verifique a Registry e tente novamente."
		if response.StatusCode == http.StatusNotFound {
			status, code, fallback = http.StatusNotFound, "design_system_not_found", "TEACH: O Project ou a revisão selecionada não possui um Design System disponível."
		} else if response.StatusCode == http.StatusConflict {
			status, code, fallback = http.StatusConflict, "design_system_selection_changed", "TEACH: A seleção mudou durante a leitura. Recarregue o Design System atual."
		}
		teach := boundedRegistryDesignSystemTeach(response.Body)
		if teach != "" {
			fallback = teach
		}
		designSystemError(w, status, code, fallback)
		return
	}
	if assetRequest {
		p.forwardDesignSystemAsset(w, response, revision, presentation)
		return
	}
	p.forwardDesignSystemDetail(w, response, companyID, projectID, revision)
}

func (p *registeredPreview) designSystemGet(r *http.Request, endpoint string) (*http.Response, error) {
	base := strings.TrimRight(strings.TrimSpace(p.registryURL), "/")
	if base == "" {
		base = strings.TrimRight(strings.TrimSpace(env("DELPHI_BUILDER_REGISTRY_URL", "http://builder-registry:8080")), "/")
	}
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return nil, errors.New("Registry URL invalid")
	}
	u.Path = endpoint
	request, err := http.NewRequestWithContext(r.Context(), http.MethodGet, u.String(), nil)
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
	boundedClient := *client
	boundedClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return boundedClient.Do(request)
}

func (p *registeredPreview) forwardDesignSystemDetail(w http.ResponseWriter, response *http.Response, companyID, projectID, requestedRevision string) {
	data, err := io.ReadAll(io.LimitReader(response.Body, maxDesignSystemJSONBytes+1))
	if err != nil || len(data) > maxDesignSystemJSONBytes || rejectDuplicateJSONKeys(data) != nil {
		designSystemError(w, http.StatusServiceUnavailable, "design_system_response_invalid", "TEACH: A Registry retornou um Design System inválido ou acima do limite de leitura.")
		return
	}
	var result struct {
		Selection struct {
			State string `json:"state"`
			SHA   string `json:"sha"`
		} `json:"selection"`
		Evaluation struct {
			ProjectID              string            `json:"project_id"`
			CompanyID              string            `json:"company_id"`
			Mode                   string            `json:"mode"`
			Revision               *string           `json:"revision"`
			Outcome                string            `json:"outcome"`
			InventoryDigest        *string           `json:"inventory_digest"`
			Items                  []json.RawMessage `json:"items"`
			DesignSystemValidation string            `json:"design_system_validation"`
		} `json:"evaluation"`
	}
	if json.Unmarshal(data, &result) != nil || result.Evaluation.ProjectID != projectID || result.Evaluation.CompanyID != companyID || result.Evaluation.Mode != "committed" || result.Evaluation.Outcome != "go" || result.Evaluation.DesignSystemValidation != "valid" || result.Evaluation.InventoryDigest == nil || len(result.Evaluation.Items) != 1 || result.Evaluation.Revision == nil || !designSystemSHA.MatchString(*result.Evaluation.Revision) || requestedRevision != "" && *result.Evaluation.Revision != requestedRevision || result.Selection.State == "selected" && result.Selection.SHA != *result.Evaluation.Revision {
		designSystemError(w, http.StatusServiceUnavailable, "design_system_response_invalid", "TEACH: Registry identity, exact revision, or semantic evaluation did not match this request.")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func boundedRegistryDesignSystemTeach(body io.Reader) string {
	data, err := io.ReadAll(io.LimitReader(body, (8<<10)+1))
	if err != nil || len(data) > 8<<10 || rejectDuplicateJSONKeys(data) != nil {
		return ""
	}
	var response struct {
		Teach string `json:"teach"`
	}
	if json.Unmarshal(data, &response) != nil || len(response.Teach) > 512 || !utf8.ValidString(response.Teach) || !strings.HasPrefix(response.Teach, "TEACH: ") || strings.ContainsAny(response.Teach, "\r\n\\") || strings.Contains(response.Teach, "://") || strings.Contains(response.Teach, "/") {
		return ""
	}
	for _, marker := range []string{"token=", "password", "secret", "authorization", "mongodb"} {
		if strings.Contains(strings.ToLower(response.Teach), marker) {
			return ""
		}
	}
	for _, char := range response.Teach {
		if char < 0x20 || char == 0x7f {
			return ""
		}
	}
	return response.Teach
}

func (p *registeredPreview) forwardDesignSystemAsset(w http.ResponseWriter, response *http.Response, requestedRevision string, presentation bool) {
	if response.StatusCode == http.StatusNotModified {
		etag := response.Header.Get("ETag")
		digest := response.Header.Get("X-Design-System-Inventory-Digest")
		if response.Header.Get("X-Design-System-Revision") != requestedRevision || etag == "" || len(etag) > 256 || strings.ContainsAny(etag, "\r\n") || !strings.HasPrefix(digest, "sha256:") || len(digest) != 71 || !validSHA256(strings.TrimPrefix(digest, "sha256:")) {
			designSystemError(w, http.StatusServiceUnavailable, "design_system_asset_invalid", "TEACH: Registry asset revision metadata is invalid.")
			return
		}
		for _, name := range []string{"ETag", "X-Design-System-Revision", "X-Design-System-Inventory-Digest", "Cache-Control"} {
			if value := response.Header.Get(name); value != "" && !strings.ContainsAny(value, "\r\n") {
				w.Header().Set(name, value)
			}
		}
		w.Header().Set("Content-Security-Policy", designSystemFilePolicy(presentation, ""))
		w.WriteHeader(http.StatusNotModified)
		return
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, ac.MaxFileBytes+1))
	if err != nil || int64(len(data)) > ac.MaxFileBytes {
		designSystemError(w, http.StatusServiceUnavailable, "design_system_asset_invalid", "TEACH: O asset selecionado excedeu o limite de leitura.")
		return
	}
	contentType := response.Header.Get("Content-Type")
	etag := response.Header.Get("ETag")
	revision := response.Header.Get("X-Design-System-Revision")
	digest := response.Header.Get("X-Design-System-Inventory-Digest")
	if contentType == "" || len(contentType) > 128 || strings.ContainsAny(contentType, "\r\n") || etag == "" || len(etag) > 256 || strings.ContainsAny(etag, "\r\n") || revision != requestedRevision || !designSystemSHA.MatchString(revision) || !strings.HasPrefix(digest, "sha256:") || len(digest) != 71 || !validSHA256(strings.TrimPrefix(digest, "sha256:")) {
		designSystemError(w, http.StatusServiceUnavailable, "design_system_asset_invalid", "TEACH: Registry asset revision metadata is invalid.")
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Header().Set("ETag", etag)
	w.Header().Set("X-Design-System-Revision", revision)
	w.Header().Set("X-Design-System-Inventory-Digest", digest)
	w.Header().Set("Cache-Control", "private, max-age=0, must-revalidate")
	w.Header().Set("Content-Security-Policy", designSystemFilePolicy(presentation, contentType))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func designSystemFilePolicy(presentation bool, contentType string) string {
	if presentation && strings.HasPrefix(contentType, "text/html") {
		return "default-src 'none'; style-src 'self'; img-src 'self' data:; sandbox; frame-ancestors 'self'"
	}
	return "default-src 'none'; sandbox"
}
