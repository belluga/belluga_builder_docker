package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var knowledgeID = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)

// Family-specific payload validation belongs to the typed Go Registry API. The
// read-only BFF owns selectors, tenant identity and the public response envelope.
var knowledgeFamilies = map[string]bool{
	"project_mandate": true, "domain_catalog": true, "project_constitution": true,
	"strategic_roadmap": true, "module": true, "endpoint_contract": true, "policy": true,
}

type knowledgeManifestEntry struct {
	Family        string `json:"family"`
	ID            string `json:"id"`
	Revision      int64  `json:"revision"`
	ContentDigest string `json:"content_digest"`
}

type knowledgeManifest struct {
	ProjectID    string                   `json:"project_id"`
	GenerationID string                   `json:"generation_id,omitempty"`
	Active       bool                     `json:"active"`
	State        string                   `json:"state"`
	Teach        string                   `json:"teach,omitempty"`
	Documents    []knowledgeManifestEntry `json:"documents"`
}

type knowledgeRecord struct {
	ID            string          `json:"id"`
	ProjectID     string          `json:"project_id"`
	GenerationID  string          `json:"generation_id"`
	Revision      int64           `json:"revision"`
	ContentDigest string          `json:"content_digest"`
	UpdatedAt     time.Time       `json:"updated_at"`
	Payload       json.RawMessage `json:"payload"`
}

const knowledgePendingTeach = "TEACH: O conhecimento canônico deste Project está pending. Importe e valide uma geração tipada pela API privada e solicite sua ativação explícita."

func knowledgeError(w http.ResponseWriter, status int, code, teach string) {
	writeJSON(w, status, map[string]string{"error": code, "teach": teach})
}

func (p *registeredPreview) knowledgeBase() string {
	if strings.TrimSpace(p.registryURL) != "" {
		return strings.TrimRight(strings.TrimSpace(p.registryURL), "/")
	}
	return strings.TrimRight(env("DELPHI_BUILDER_REGISTRY_URL", "http://builder-registry:8080"), "/")
}

func (p *registeredPreview) knowledgeGet(r *http.Request, path string) ([]byte, int, error) {
	u, err := url.Parse(p.knowledgeBase())
	if err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return nil, 0, errors.New("Registry URL invalid")
	}
	parts := strings.SplitN(path, "?", 2)
	u.Path = parts[0]
	if len(parts) == 2 {
		u.RawQuery = parts[1]
	}
	request, err := http.NewRequestWithContext(r.Context(), http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, 0, err
	}
	client := p.client
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	// The configured private Registry is the only destination. Never follow a
	// redirect into another trust boundary or forward browser headers/credentials.
	boundedClient := *client
	boundedClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := boundedClient.Do(request)
	if err != nil {
		return nil, 0, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, (4<<20)+1))
	if err != nil || len(data) > 4<<20 || rejectDuplicateJSONKeys(data) != nil {
		return nil, 0, errors.New("Registry response invalid")
	}
	return data, response.StatusCode, nil
}

// Catalog identity does not require reading any Foundation artifact. Other
// Project-detail and snapshot consumers keep their existing reader contracts.
func (p *registeredPreview) knowledgeCatalog(r *http.Request) (registrationCatalog, error) {
	data, status, err := p.knowledgeGet(r, "/internal/v1/catalog")
	var source struct {
		SchemaVersion string              `json:"schema_version"`
		Companies     []registeredCompany `json:"companies"`
		Projects      []struct {
			ID        string `json:"id"`
			Name      string `json:"name"`
			CompanyID string `json:"company_id"`
			Binding   *struct {
				FoundationRoot                 string `json:"foundation_root"`
				RepositoryID                   string `json:"repository_id"`
				PublicationBranch              string `json:"publication_branch"`
				LegacyFoundationSourceDisabled bool   `json:"legacy_foundation_source_disabled"`
			} `json:"binding"`
		} `json:"projects"`
	}
	if err != nil || status != http.StatusOK || json.Unmarshal(data, &source) != nil || source.SchemaVersion != "1" || source.Companies == nil || source.Projects == nil {
		return registrationCatalog{}, errors.New("Registry catalog unavailable")
	}
	result := registrationCatalog{SchemaVersion: "1", Companies: source.Companies, Projects: []catalogProject{}}
	companies, projects := map[string]bool{}, map[string]bool{}
	for _, company := range source.Companies {
		if !validRegistrationID(company.ID) || !validRegistrationName(company.Name) || companies[company.ID] {
			return registrationCatalog{}, errors.New("Registry Company invalid")
		}
		companies[company.ID] = true
	}
	for _, project := range source.Projects {
		key := project.CompanyID + "/" + project.ID
		if !validRegistrationID(project.ID) || !validRegistrationName(project.Name) || !companies[project.CompanyID] || projects[key] {
			return registrationCatalog{}, errors.New("Registry Project invalid")
		}
		projects[key] = true
		sourceBound, repositoryBound := false, false
		if project.Binding != nil {
			sourceBound = project.Binding.FoundationRoot != "" && legacyFoundationSourceEligibleValues(project.Binding.FoundationRoot, project.Binding.RepositoryID, project.Binding.PublicationBranch, project.Binding.LegacyFoundationSourceDisabled)
			repositoryBound = project.Binding.RepositoryID != "" && project.Binding.PublicationBranch != ""
		}
		result.Projects = append(result.Projects, catalogProject{ID: project.ID, Name: project.Name, CompanyID: project.CompanyID, SourceBound: sourceBound, RepositoryBound: repositoryBound, LegacySourceDisabled: project.Binding != nil && project.Binding.LegacyFoundationSourceDisabled})
	}
	return result, nil
}

func (p *registeredPreview) knowledgeAPIHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	q := r.URL.Query()
	companyID, companyOK := oneQueryValue(q, "company_id")
	projectID, projectOK := oneQueryValue(q, "project_id")
	family, familyOK := oneQueryValue(q, "family")
	recordID, recordOK := oneQueryValue(q, "record_id")
	detail := len(q) == 4 && familyOK && recordOK && knowledgeFamilies[family] && knowledgeID.MatchString(recordID)
	if r.Method != http.MethodGet || r.ContentLength != 0 || len(r.URL.RawQuery) > 2048 || !companyOK || !projectOK || !validRegistrationID(companyID) || !validRegistrationID(projectID) || len(q) != 2 && !detail {
		knowledgeError(w, 400, "canonical_request_invalid", "TEACH: Selecione Company e Project uma única vez; para um documento, informe a família e o ID exatos.")
		return
	}
	catalog, err := p.knowledgeCatalog(r)
	if err != nil {
		knowledgeError(w, 503, "canonical_unavailable", "TEACH: Verifique a disponibilidade da Registry API e tente novamente.")
		return
	}
	registered := false
	for _, project := range catalog.Projects {
		if project.ID == projectID && project.CompanyID == companyID {
			registered = true
			break
		}
	}
	if !registered {
		knowledgeError(w, 404, "project_not_found", "TEACH: Selecione um Project registrado nesta Company.")
		return
	}
	path := "/internal/v1/companies/" + companyID + "/projects/" + projectID + "/knowledge/manifest"
	if detail {
		path = "/internal/v1/companies/" + companyID + "/projects/" + projectID + "/knowledge/" + family + "/" + recordID
	}
	body, status, err := p.knowledgeGet(r, path)
	if err != nil || status != 200 && status != 404 {
		knowledgeError(w, 503, "canonical_unavailable", "TEACH: A leitura canônica está indisponível. Verifique a Registry API e recarregue Documents.")
		return
	}
	if status == 404 {
		var failure struct {
			State string `json:"state"`
		}
		_ = json.Unmarshal(body, &failure)
		if failure.State == "pending" {
			writeJSON(w, 404, map[string]string{"state": "pending", "teach": knowledgePendingTeach})
			return
		}
		knowledgeError(w, 404, "canonical_not_found", "TEACH: Documento não encontrado neste Project. Abra a lista atual de Documents e selecione um ID existente.")
		return
	}
	invalid := func() {
		knowledgeError(w, 503, "canonical_response_invalid", "TEACH: A API retornou identidade ou revisão canônica inválida. Verifique o contrato do Project e recarregue.")
	}
	if detail {
		var record knowledgeRecord
		var payload map[string]json.RawMessage
		if json.Unmarshal(body, &record) != nil || record.ID != recordID || record.ProjectID != projectID || !knowledgeID.MatchString(record.GenerationID) || record.Revision < 1 || !validKnowledgeDigest(record.ContentDigest) || record.UpdatedAt.IsZero() || json.Unmarshal(record.Payload, &payload) != nil || payload == nil {
			invalid()
			return
		}
		writeJSON(w, 200, record)
		return
	}
	var manifest knowledgeManifest
	if json.Unmarshal(body, &manifest) != nil || manifest.ProjectID != projectID || manifest.Documents == nil {
		invalid()
		return
	}
	if !manifest.Active {
		if manifest.State != "pending" || manifest.GenerationID != "" || len(manifest.Documents) != 0 {
			invalid()
			return
		}
		manifest.Teach = knowledgePendingTeach
	} else {
		if manifest.State != "active" || !knowledgeID.MatchString(manifest.GenerationID) {
			invalid()
			return
		}
		manifest.Teach = ""
		seen := map[string]bool{}
		for _, entry := range manifest.Documents {
			key := entry.Family + "/" + entry.ID
			if !knowledgeFamilies[entry.Family] || !knowledgeID.MatchString(entry.ID) || entry.Revision < 1 || !validKnowledgeDigest(entry.ContentDigest) || seen[key] {
				invalid()
				return
			}
			seen[key] = true
		}
	}
	writeJSON(w, 200, manifest)
}

func (p *registeredPreview) knowledgeIndexAPIHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	q := r.URL.Query()
	companyID, companyOK := oneQueryValue(q, "company_id")
	projectID, projectOK := oneQueryValue(q, "project_id")
	if r.Method != http.MethodGet || r.ContentLength != 0 || len(r.URL.RawQuery) > 3500 || !companyOK || !projectOK || !validRegistrationID(companyID) || !validRegistrationID(projectID) || len(q) > 9 {
		knowledgeError(w, 400, "canonical_request_invalid", "TEACH: Informe Company e Project válidos uma única vez.")
		return
	}
	for key, values := range q {
		if len(values) != 1 || key != "company_id" && key != "project_id" && key != "family" && key != "module_id" && key != "method" && key != "q" && key != "limit" && key != "cursor" && key != "decisions_only" {
			knowledgeError(w, 400, "canonical_request_invalid", "TEACH: Use apenas filtros únicos do índice canônico.")
			return
		}
	}
	if family := q.Get("family"); family != "" && !knowledgeFamilies[family] || q.Get("module_id") != "" && !knowledgeID.MatchString(q.Get("module_id")) || len(q.Get("q")) > 120 || len(q.Get("cursor")) > 2048 {
		knowledgeError(w, 400, "canonical_request_invalid", "TEACH: Corrija família, módulo, busca ou cursor do índice.")
		return
	}
	if value := q.Get("decisions_only"); value != "" && value != "true" {
		knowledgeError(w, 400, "canonical_request_invalid", "TEACH: decisions_only aceita apenas true.")
		return
	}
	catalog, err := p.knowledgeCatalog(r)
	if err != nil {
		knowledgeError(w, 503, "canonical_unavailable", "TEACH: Verifique a Registry API e tente novamente.")
		return
	}
	registered := false
	for _, project := range catalog.Projects {
		if project.ID == projectID && project.CompanyID == companyID {
			registered = true
			break
		}
	}
	if !registered {
		knowledgeError(w, 404, "project_not_found", "TEACH: Selecione um Project registrado nesta Company.")
		return
	}
	internal := url.Values{}
	for _, key := range []string{"family", "module_id", "method", "q", "limit", "cursor", "decisions_only"} {
		if value := q.Get(key); value != "" {
			internal.Set(key, value)
		}
	}
	path := "/internal/v1/companies/" + companyID + "/projects/" + projectID + "/knowledge/index"
	if len(internal) != 0 {
		path += "?" + internal.Encode()
	}
	// knowledgeGet constructs the private URL from an approved path. Query is
	// copied only from the bounded, validated filter set above.
	body, status, err := p.knowledgeGet(r, path)
	if err != nil {
		knowledgeError(w, 503, "canonical_unavailable", "TEACH: O índice canônico está indisponível.")
		return
	}
	if status != 200 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(body)
		return
	}
	var page struct {
		ProjectID    string            `json:"project_id"`
		GenerationID string            `json:"generation_id"`
		WriteEpoch   int64             `json:"write_epoch"`
		Items        []json.RawMessage `json:"items"`
		NextCursor   string            `json:"next_cursor"`
	}
	if json.Unmarshal(body, &page) != nil || page.ProjectID != projectID || !knowledgeID.MatchString(page.GenerationID) || page.Items == nil || len(page.Items) > 100 || len(page.NextCursor) > 2048 {
		knowledgeError(w, 503, "canonical_response_invalid", "TEACH: O índice retornou identidade ou página inválida; recarregue Documents.")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	_, _ = w.Write(body)
}

func validKnowledgeDigest(digest string) bool {
	return strings.HasPrefix(digest, "sha256:") && validSHA256(strings.TrimPrefix(digest, "sha256:"))
}
