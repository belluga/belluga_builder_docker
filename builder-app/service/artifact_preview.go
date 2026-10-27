package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

type registeredPreview struct {
	root        string
	readerURL   string
	readerToken string
	client      *http.Client
}

var errPathOnlyReader = errors.New("Project registration must be recreated")

type registeredCompany struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type registeredArtifact struct {
	ID         string   `json:"id"`
	Kind       string   `json:"kind"`
	Name       string   `json:"name"`
	State      string   `json:"state"`
	Entry      string   `json:"entry_point,omitempty"`
	Files      []string `json:"files,omitempty"`
	Diagnostic string   `json:"diagnostic,omitempty"`
}
type registeredProject struct {
	ID                    string               `json:"id"`
	Name                  string               `json:"name"`
	CompanyID             string               `json:"company_id"`
	RegisteredAt          string               `json:"registered_at"`
	PreparationState      string               `json:"preparation_state"`
	PreparationDiagnostic string               `json:"preparation_diagnostic,omitempty"`
	SnapshotFingerprint   string               `json:"snapshot_fingerprint,omitempty"`
	Artifacts             []registeredArtifact `json:"artifacts"`
}
type registrationCatalog struct {
	SchemaVersion string              `json:"schema_version"`
	Companies     []registeredCompany `json:"companies"`
	Projects      []catalogProject    `json:"projects"`
}
type catalogProject struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	CompanyID string `json:"company_id"`
}
type persistedRegistration struct {
	SchemaVersion string                         `json:"schema_version"`
	Companies     map[string]registeredCompany   `json:"companies"`
	Projects      map[string]persistedProject    `json:"projects"`
	Preferences   map[string]persistedPreference `json:"preferences"`
}
type persistedPreference struct {
	ProjectID  string            `json:"project_id"`
	Binding    *persistedBinding `json:"binding"`
	DeclinedAt string            `json:"declined_at"`
}
type persistedBinding struct {
	WorkspaceRoot        string `json:"workspace_root"`
	ProjectRoot          string `json:"project_root"`
	FoundationRoot       string `json:"foundation_root"`
	RepositoryID         string `json:"repository_id,omitempty"`
	PublicationBranch    string `json:"publication_branch,omitempty"`
	SourceBindingsPath   string `json:"source_bindings_path,omitempty"`
	SourceBindingsDigest string `json:"source_bindings_digest,omitempty"`
	DefaultOwnerID       string `json:"default_owner_id,omitempty"`
}
type persistedProject struct {
	registeredProject
	Binding *persistedBinding `json:"binding"`
}
type registeredSnapshot struct {
	SchemaVersion          string                              `json:"schema_version"`
	ProjectID              string                              `json:"project_id"`
	CompanyID              string                              `json:"company_id"`
	ObservedAt             string                              `json:"observed_at"`
	ProjectSHA             string                              `json:"project_sha,omitempty"`
	ExternalSourceID       string                              `json:"external_source_id,omitempty"`
	ExternalSourceRevision string                              `json:"external_source_revision,omitempty"`
	Files                  map[string][]byte                   `json:"files"`
	Assets                 map[string]registeredAssetReference `json:"assets,omitempty"`
	Artifacts              []registeredArtifact                `json:"artifacts"`
	Observations           map[string]json.RawMessage          `json:"observations"`
	todoSources            todoSourceMap
	todoSourcesParsed      bool
}

type registeredAssetReference struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type roadmapCatalog struct {
	SchemaVersion string               `json:"schema_version"`
	ProjectID     string               `json:"project_id"`
	FocusRelease  string               `json:"focus_release_id"`
	Epics         []roadmapCatalogEpic `json:"epics"`
}

type roadmapCatalogEpic struct {
	ID          string                  `json:"id"`
	Name        string                  `json:"name"`
	Description string                  `json:"description"`
	Releases    []roadmapCatalogRelease `json:"releases"`
}

type roadmapCatalogRelease struct {
	ID          string               `json:"id"`
	Name        string               `json:"name"`
	Description string               `json:"description"`
	TODOs       []roadmapCatalogTODO `json:"todos"`
}

type roadmapCatalogTODO struct {
	ID   string `json:"id"`
	Path string `json:"path"`
	Kind string `json:"kind"`
}

type roadmapProjection struct {
	SchemaVersion string                `json:"schema_version"`
	ProjectID     string                `json:"project_id"`
	FocusRelease  string                `json:"focus_release_id"`
	Epics         []roadmapEpic         `json:"epics"`
	Backlog       roadmapBacklog        `json:"backlog"`
	Unclassified  []roadmapUnclassified `json:"unclassified"`
	Diagnostics   []roadmapDiagnostic   `json:"diagnostics"`
}

type roadmapBacklog struct {
	TODOs   []roadmapTODO          `json:"todos"`
	Folders []roadmapBacklogFolder `json:"folders"`
}

type roadmapBacklogFolder struct {
	Name        string                 `json:"name"`
	Path        string                 `json:"path"`
	Description string                 `json:"description,omitempty"`
	Folders     []roadmapBacklogFolder `json:"folders"`
	TODOs       []roadmapTODO          `json:"todos"`
}

type roadmapUnclassified struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

type roadmapDiagnostic struct {
	Code    string `json:"code"`
	Path    string `json:"path,omitempty"`
	Message string `json:"message"`
}

type roadmapEpic struct {
	ID          string           `json:"id"`
	Name        string           `json:"name"`
	Description string           `json:"description"`
	Releases    []roadmapRelease `json:"releases"`
}

type roadmapRelease struct {
	ID          string        `json:"id"`
	Name        string        `json:"name"`
	Description string        `json:"description"`
	TODOs       []roadmapTODO `json:"todos"`
}

type roadmapTODO struct {
	ID                    string `json:"id"`
	Title                 string `json:"title"`
	Path                  string `json:"path"`
	Kind                  string `json:"kind"`
	Missing               bool   `json:"missing,omitempty"`
	Objective             string `json:"objective,omitempty"`
	ObjectiveState        string `json:"objective_state"`
	NextStep              string `json:"next_step,omitempty"`
	NextStepState         string `json:"next_step_state"`
	RecordState           string `json:"record_state"`
	DeliveryStage         string `json:"delivery_stage,omitempty"`
	AuthoredDeliveryStage string `json:"authored_delivery_stage"`
	DeliveryStageState    string `json:"delivery_stage_state"`
	DeliveryStageTeach    string `json:"delivery_stage_teach,omitempty"`
	HistoricalDisposition string `json:"historical_disposition,omitempty"`
	DispositionReason     string `json:"disposition_reason,omitempty"`
	SuccessorTODO         string `json:"successor_todo,omitempty"`
	DispositionState      string `json:"disposition_state,omitempty"`
	Blocked               bool   `json:"blocked"`
	QualifierState        string `json:"qualifier_state"`
	QualifierTeach        string `json:"qualifier_teach,omitempty"`
	WorkState             string `json:"work_state"`
	InvalidQualifiers     bool   `json:"invalid_qualifiers,omitempty"`
	ExplicitNoCode        bool   `json:"explicit_no_code,omitempty"`
	Executable            bool   `json:"executable,omitempty"`
}

type todoSourceMap struct {
	SchemaVersion string                `json:"schema_version"`
	Sources       map[string]todoSource `json:"sources"`
	byPath        map[string]string
}
type todoSource struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type portfolioSummary struct {
	SchemaVersion string `json:"schema_version"`
	Summary       string `json:"summary"`
}

type overviewSection struct {
	Heading  string   `json:"heading"`
	Body     string   `json:"body"`
	Points   []string `json:"points"`
	ImageAlt string   `json:"image_alt,omitempty"`
}

type overviewSource struct {
	SchemaVersion string                     `json:"schema_version"`
	Tagline       string                     `json:"tagline"`
	Summary       string                     `json:"summary"`
	DesktopAlt    string                     `json:"desktop_alt,omitempty"`
	MobileAlt     string                     `json:"mobile_alt,omitempty"`
	Sections      map[string]overviewSection `json:"sections"`
	Media         map[string]string          `json:"media,omitempty"`
}

func (p *registeredPreview) catalog() (registrationCatalog, error) {
	if !filepath.IsAbs(p.root) || filepath.Clean(p.root) == string(filepath.Separator) {
		return registrationCatalog{}, errors.New("state root must be absolute")
	}
	root, err := os.Lstat(p.root)
	if err != nil || !root.IsDir() || root.Mode()&os.ModeSymlink != 0 {
		return registrationCatalog{}, errors.New("state root unavailable or unsafe")
	}
	registryPath := filepath.Join(p.root, "registry.json")
	if _, err := os.Lstat(registryPath); errors.Is(err, os.ErrNotExist) {
		return registrationCatalog{SchemaVersion: "builder-project-registry-v1", Companies: []registeredCompany{}, Projects: []catalogProject{}}, nil
	}
	data, err := readRegisteredFile(registryPath, 128<<10)
	var stored persistedRegistration
	if err != nil || rejectDuplicateJSONKeys(data) != nil || json.Unmarshal(data, &stored) != nil || stored.SchemaVersion != "builder-project-registry-v1" || stored.Companies == nil || stored.Projects == nil || stored.Preferences == nil {
		return registrationCatalog{}, errors.New("registration registry unavailable or invalid")
	}
	result := registrationCatalog{SchemaVersion: stored.SchemaVersion, Companies: []registeredCompany{}, Projects: []catalogProject{}}
	for key, c := range stored.Companies {
		if !validRegistrationID(c.ID) || key != c.ID || !validRegistrationName(c.Name) {
			return registrationCatalog{}, errors.New("registration company identity invalid")
		}
		result.Companies = append(result.Companies, c)
	}
	for key, storedProject := range stored.Projects {
		p := storedProject.registeredProject
		if !validRegistrationID(p.ID) || key != p.ID || !validRegistrationName(p.Name) || !hasCompany(stored.Companies, p.CompanyID) || !validStoredBinding(storedProject.Binding) {
			return registrationCatalog{}, errors.New("registration project identity invalid")
		}
		result.Projects = append(result.Projects, catalogProject{ID: p.ID, Name: p.Name, CompanyID: p.CompanyID})
	}
	for key, preference := range stored.Preferences {
		if !validRegistrationID(key) || key != preference.ProjectID || !validStoredBinding(preference.Binding) || preference.DeclinedAt == "" {
			return registrationCatalog{}, errors.New("registration preference invalid")
		}
	}
	sort.Slice(result.Companies, func(i, j int) bool { return result.Companies[i].ID < result.Companies[j].ID })
	sort.Slice(result.Projects, func(i, j int) bool { return result.Projects[i].ID < result.Projects[j].ID })
	return result, nil
}

func (p *registeredPreview) currentFingerprint(projectID, companyID string) (string, error) {
	if _, err := p.catalog(); err != nil {
		return "", err
	}
	data, err := readRegisteredFile(filepath.Join(p.root, "registry.json"), 128<<10)
	if err != nil || rejectDuplicateJSONKeys(data) != nil {
		return "", errors.New("registration registry unavailable")
	}
	var stored persistedRegistration
	if json.Unmarshal(data, &stored) != nil {
		return "", errors.New("registration registry invalid")
	}
	project, ok := stored.Projects[projectID]
	if !ok || project.registeredProject.CompanyID != companyID || project.registeredProject.ID != projectID || project.registeredProject.SnapshotFingerprint == "" || project.Binding == nil || project.Binding.RepositoryID == "" || project.Binding.PublicationBranch == "" {
		return "", errors.New("current Project pointer unavailable")
	}
	return project.registeredProject.SnapshotFingerprint, nil
}

func rejectDuplicateJSONKeys(data []byte) error {
	d := json.NewDecoder(strings.NewReader(string(data)))
	var value func() error
	value = func() error {
		t, err := d.Token()
		if err != nil {
			return err
		}
		open, ok := t.(json.Delim)
		if !ok {
			return nil
		}
		if open == '{' {
			seen := map[string]bool{}
			for d.More() {
				keyToken, e := d.Token()
				if e != nil {
					return e
				}
				key, ok := keyToken.(string)
				if !ok || seen[key] {
					return errors.New("duplicate or invalid JSON key")
				}
				seen[key] = true
				if e = value(); e != nil {
					return e
				}
			}
			_, err = d.Token()
			return err
		}
		if open == '[' {
			for d.More() {
				if err := value(); err != nil {
					return err
				}
			}
			_, err = d.Token()
			return err
		}
		return errors.New("unexpected JSON delimiter")
	}
	if err := value(); err != nil {
		return err
	}
	if _, err := d.Token(); err == nil {
		return errors.New("trailing JSON value")
	}
	return nil
}

func validRegistrationName(value string) bool {
	return strings.TrimSpace(value) != "" && utf8.ValidString(value) && utf8.RuneCountInString(value) <= 160
}
func validStoredBinding(binding *persistedBinding) bool {
	if binding == nil || !filepath.IsAbs(binding.WorkspaceRoot) || binding.ProjectRoot == "" || binding.FoundationRoot == "" {
		return false
	}
	validContextPath := func(value string, allowDot bool) bool {
		if allowDot && value == "." {
			return true
		}
		if len(value) > 1024 || !safeSnapshotPath(value) {
			return false
		}
		for _, segment := range strings.Split(value, "/") {
			if segment == ".git" || segment == "." || segment == ".." {
				return false
			}
		}
		return true
	}
	if !validContextPath(binding.ProjectRoot, true) || !validContextPath(binding.FoundationRoot, false) || binding.SourceBindingsPath == "" && binding.SourceBindingsDigest != "" {
		return false
	}
	if binding.SourceBindingsPath != "" {
		digest := strings.TrimPrefix(binding.SourceBindingsDigest, "sha256:")
		if !validContextPath(binding.SourceBindingsPath, false) || len(binding.SourceBindingsDigest) != 71 || !strings.HasPrefix(binding.SourceBindingsDigest, "sha256:") || strings.ToLower(digest) != digest {
			return false
		}
		if _, err := hex.DecodeString(digest); err != nil {
			return false
		}
	}
	return binding.DefaultOwnerID == "" || validRegistrationID(binding.DefaultOwnerID)
}

func hasCompany(companies map[string]registeredCompany, id string) bool {
	_, ok := companies[id]
	return ok
}
func validRegistrationID(value string) bool {
	if len(value) < 1 || len(value) > 64 || value[0] < 'a' || value[0] > 'z' {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}
func readRegisteredFile(name string, limit int64) ([]byte, error) {
	fd, err := syscall.Open(name, syscall.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > limit {
		return nil, errors.New("registration file is not regular or exceeds limit")
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, errors.New("registration file exceeds limit")
	}
	return data, nil
}

func (p *registeredPreview) apiHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && r.URL.Path == "/api/local-todo-source" {
		p.todoSourceHandler(w, r)
		return
	}
	if r.Method != http.MethodGet || r.URL.Path != "/api/local-preview" {
		writeError(w, 404, "not_found")
		return
	}
	catalog, err := p.catalog()
	if err != nil {
		writeError(w, 503, "registration_state_unavailable")
		return
	}
	companyID, projectID := r.URL.Query().Get("company_id"), r.URL.Query().Get("project_id")
	if companyID == "" && projectID == "" {
		writeJSON(w, 200, map[string]any{"schema_version": "1", "companies": catalog.Companies, "projects": catalog.Projects})
		return
	}
	if companyID == "" || projectID == "" {
		writeError(w, 400, "company_and_project_required")
		return
	}
	for _, project := range catalog.Projects {
		if project.ID != projectID || project.CompanyID != companyID {
			continue
		}
		snapshot, fingerprint, e := p.readCurrent(project.ID, project.CompanyID)
		if e != nil {
			if errors.Is(e, errPathOnlyReader) {
				writeJSON(w, http.StatusServiceUnavailable, map[string]any{"schema_version": "1", "company_id": companyID, "project_id": projectID, "registration_state": "registered", "consumer_readiness": "unavailable", "diagnostic": "TEACH: This Project has an invalid path-only registration. Verify its published Foundation Git remote and branch, then register the Project again. No migration or compatibility reader is available."})
				return
			}
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"schema_version": "1", "company_id": companyID, "project_id": projectID, "registration_state": "registered", "consumer_readiness": "unavailable", "diagnostic": "TEACH: Check the governed published Foundation branch and private Delphi reader access, then retry. No older snapshot is shown as current."})
			return
		}
		writeJSON(w, 200, registeredProjectDetail(snapshot, fingerprint))
		return
	}
	writeError(w, 404, "project_not_found")
}

func (p *registeredPreview) todoSourceHandler(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	companyID, companyOK := oneQueryValue(q, "company_id")
	projectID, projectOK := oneQueryValue(q, "project_id")
	todoID, todoOK := oneQueryValue(q, "todo_id")
	fingerprintArg, fingerprintOK := oneQueryValue(q, "fingerprint")
	if r.Method != http.MethodGet || r.ContentLength > 0 || len(r.URL.RawQuery) > 8192 || len(q) != 4 || !companyOK || !projectOK || !todoOK || !fingerprintOK || !validRegistrationID(companyID) || !validRegistrationID(projectID) || todoID == "" || len(todoID) > 1200 || !utf8.ValidString(todoID) || !validSHA256(fingerprintArg) {
		writeError(w, http.StatusBadRequest, "todo_source_request_invalid")
		return
	}
	base, token := strings.TrimRight(strings.TrimSpace(p.readerURL), "/"), p.readerToken
	if base == "" {
		base = strings.TrimRight(strings.TrimSpace(os.Getenv("DELPHI_PROJECT_READER_URL")), "/")
	}
	if token == "" {
		token = os.Getenv("DELPHI_PROJECT_READER_TOKEN")
	}
	query := url.Values{"company_id": {companyID}, "fingerprint": {fingerprintArg}, "todo_id": {todoID}}
	endpoint, err := url.Parse(base + "/internal/v1/projects/" + url.PathEscape(projectID))
	if err != nil || endpoint.Scheme != "http" || endpoint.Host == "" || len(token) < 32 {
		writeError(w, http.StatusServiceUnavailable, "todo_source_unavailable")
		return
	}
	endpoint.RawQuery = query.Encode()
	client := p.client
	if client == nil {
		client = &http.Client{Timeout: 100 * time.Second}
	}
	request, err := http.NewRequestWithContext(r.Context(), http.MethodGet, endpoint.String(), nil)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "todo_source_unavailable")
		return
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := client.Do(request)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "todo_source_unavailable")
		return
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		writeError(w, http.StatusNotFound, "todo_source_not_found")
		return
	}
	if response.StatusCode == http.StatusConflict {
		writeError(w, http.StatusConflict, "todo_source_stale_reload_project")
		return
	}
	if response.StatusCode != http.StatusOK {
		writeError(w, http.StatusServiceUnavailable, "todo_source_unavailable")
		return
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, (512<<10)+2048))
	var result struct {
		TODOID      string `json:"todo_id"`
		Fingerprint string `json:"fingerprint"`
		Markdown    string `json:"markdown"`
	}
	if err != nil || len(body) > (512<<10)+2048 || rejectDuplicateJSONKeys(body) != nil || json.Unmarshal(body, &result) != nil || result.TODOID != todoID || result.Fingerprint != fingerprintArg || len([]byte(result.Markdown)) > 512<<10 || !utf8.ValidString(result.Markdown) {
		writeError(w, http.StatusServiceUnavailable, "todo_source_unavailable")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func oneQueryValue(q url.Values, key string) (string, bool) {
	values, ok := q[key]
	if !ok || len(values) != 1 {
		return "", false
	}
	return values[0], true
}

func validTodoSourceEntry(id string, item todoSource) bool {
	ext := strings.ToLower(filepath.Ext(item.Path))
	if id == "" || len(id) > 1200 || !utf8.ValidString(id) || len(item.Path) > 1024 || !strings.HasPrefix(item.Path, "todos/") || !safeSnapshotPath(item.Path) || (ext != ".md" && ext != ".markdown") || len(item.SHA256) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(item.SHA256)
	return err == nil && hex.EncodeToString(decoded) == item.SHA256
}

func validSHA256(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32
}

type privateReaderResponse struct {
	ProjectID              string                              `json:"project_id"`
	ProjectName            string                              `json:"project_name"`
	CompanyID              string                              `json:"company_id"`
	ProjectSHA             string                              `json:"project_sha"`
	ObservedAt             string                              `json:"observed_at"`
	Fingerprint            string                              `json:"snapshot_fingerprint"`
	Artifacts              []registeredArtifact                `json:"artifacts"`
	Files                  map[string][]byte                   `json:"files"`
	Assets                 map[string]registeredAssetReference `json:"assets"`
	Observations           map[string]json.RawMessage          `json:"observations"`
	ExternalSourceID       string                              `json:"external_source_id"`
	ExternalSourceRevision string                              `json:"external_source_revision"`
}

func (p *registeredPreview) readCurrent(projectID, companyID string) (registeredSnapshot, string, error) {
	base := strings.TrimRight(strings.TrimSpace(p.readerURL), "/")
	token := p.readerToken
	if base == "" {
		base = strings.TrimRight(strings.TrimSpace(os.Getenv("DELPHI_PROJECT_READER_URL")), "/")
	}
	if token == "" {
		token = os.Getenv("DELPHI_PROJECT_READER_TOKEN")
	}
	if base == "" || token == "" || len(token) < 32 {
		return registeredSnapshot{}, "", errors.New("private Project reader is not configured")
	}
	u, err := url.Parse(base + "/internal/v1/projects/" + url.PathEscape(projectID))
	if err != nil || u.Scheme != "http" || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
		return registeredSnapshot{}, "", errors.New("private Project reader endpoint is invalid")
	}
	client := p.client
	if client == nil {
		client = &http.Client{Timeout: 100 * time.Second}
	}
	request, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return registeredSnapshot{}, "", err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := client.Do(request)
	if err != nil {
		return registeredSnapshot{}, "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		if response.StatusCode == http.StatusServiceUnavailable {
			var diagnostic struct {
				State string `json:"state"`
				Teach string `json:"teach"`
			}
			if json.NewDecoder(io.LimitReader(response.Body, 1024)).Decode(&diagnostic) == nil && diagnostic.State == "unavailable" && strings.Contains(diagnostic.Teach, "invalid path-only registration") && strings.Contains(diagnostic.Teach, "re-register") {
				return registeredSnapshot{}, "", errPathOnlyReader
			}
		}
		return registeredSnapshot{}, "", errors.New("private Project reader returned unavailable")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 64<<20+1))
	if err != nil || len(data) > 64<<20 || rejectDuplicateJSONKeys(data) != nil {
		return registeredSnapshot{}, "", errors.New("private Project response is invalid")
	}
	var source privateReaderResponse
	if json.Unmarshal(data, &source) != nil || source.ProjectID != projectID || source.CompanyID != companyID || len(source.Fingerprint) != 64 || !validProjectSHA(source.ProjectSHA) || source.Files == nil || source.Artifacts == nil || source.Observations == nil {
		return registeredSnapshot{}, "", errors.New("private Project response identity is invalid")
	}
	if _, err := hex.DecodeString(source.Fingerprint); err != nil || strings.ToLower(source.Fingerprint) != source.Fingerprint {
		return registeredSnapshot{}, "", errors.New("private Project fingerprint is invalid")
	}
	snapshot := registeredSnapshot{SchemaVersion: "builder-project-snapshot-v3", ProjectID: source.ProjectID, CompanyID: source.CompanyID, ObservedAt: source.ObservedAt, ProjectSHA: source.ProjectSHA, ExternalSourceID: source.ExternalSourceID, ExternalSourceRevision: source.ExternalSourceRevision, Files: source.Files, Assets: source.Assets, Artifacts: source.Artifacts, Observations: source.Observations}
	snapshot.todoSources = parseTodoSourceMap(snapshot)
	snapshot.todoSourcesParsed = true
	return snapshot, source.Fingerprint, nil
}

func validProjectSHA(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded)*2 == len(value) && strings.ToLower(value) == value
}

func registeredProjectDetail(snapshot registeredSnapshot, fingerprint string) map[string]any {
	result := map[string]any{
		"schema_version": "1", "company_id": snapshot.CompanyID, "project_id": snapshot.ProjectID,
		"registration_state": "registered", "consumer_readiness": "ready", "snapshot_fingerprint": fingerprint,
		"snapshot_observed_at": snapshot.ObservedAt, "snapshot_observations": snapshot.Observations, "artifacts": snapshot.Artifacts,
		"project_sha": snapshot.ProjectSHA,
	}
	todoSources := snapshot.todoSources
	if !snapshot.todoSourcesParsed {
		todoSources = parseTodoSourceMap(snapshot)
		snapshot.todoSourcesParsed = true
	}
	snapshot.todoSources = todoSources
	prototypeTODOs := []map[string]string{}
	for _, artifact := range snapshot.Artifacts {
		if artifact.Kind != "prototype" || artifact.State != "available" {
			continue
		}
		parts := strings.Split(artifact.Entry, "/")
		if len(parts) < 3 {
			continue
		}
		manifest := snapshot.Files[path.Join("prototypes", parts[1], "prototype.json")]
		var evidence struct {
			Related []struct {
				Kind string `json:"kind"`
				Path string `json:"path"`
			} `json:"related"`
		}
		if json.Unmarshal(manifest, &evidence) != nil {
			continue
		}
		for _, related := range evidence.Related {
			if !strings.HasPrefix(related.Path, "todos/") {
				continue
			}
			if id := todoSources.byPath[related.Path]; id != "" {
				prototypeTODOs = append(prototypeTODOs, map[string]string{"prototype_id": artifact.ID, "todo_id": id, "label": path.Base(related.Path), "path": related.Path})
			}
		}
	}
	result["prototype_todo_sources"] = prototypeTODOs
	if snapshot.ExternalSourceID != "" {
		result["external_source_id"] = snapshot.ExternalSourceID
	}
	if snapshot.ExternalSourceRevision != "" {
		result["external_source_revision"] = snapshot.ExternalSourceRevision
	}
	for _, artifact := range snapshot.Artifacts {
		if artifact.Kind == "overview" && artifact.State == "available" && artifact.Entry == "project_overview.json" {
			if data, ok := snapshot.Files[artifact.Entry]; ok {
				var overview overviewSource
				decoder := json.NewDecoder(strings.NewReader(string(data)))
				decoder.DisallowUnknownFields()
				if rejectDuplicateJSONKeys(data) == nil && decoder.Decode(&overview) == nil && overview.SchemaVersion == "1" && len(overview.Sections) == 4 && overview.Media == nil {
					media := map[string]string{}
					for _, name := range artifact.Files {
						if name == "project_overview/desktop.png" || name == "project_overview/mobile.png" || name == "project_overview/problem.png" || name == "project_overview/proposal.png" || name == "project_overview/impact.png" || name == "project_overview/audience.png" {
							if snapshotHasFile(snapshot, name) {
								media[strings.TrimSuffix(path.Base(name), ".png")] = snapshotFileURL(snapshot, fingerprint, name)
							}
						}
					}
					if media["desktop"] != "" && media["mobile"] != "" && strings.TrimSpace(overview.DesktopAlt) != "" && strings.TrimSpace(overview.MobileAlt) != "" {
						overview.Media = media
						result["overview"] = overview
					}
				}
			}
		}
		if artifact.Kind == "roadmap" && artifact.State == "available" {
			for _, name := range artifact.Files {
				if name == "roadmap/projection.json" {
					if projection, ok := admittedRoadmapProjection(snapshot.ProjectID, snapshot.Files[name], snapshot.Files, artifact.Files, snapshot.todoSources); ok {
						result["roadmap"] = projection
					}
				}
			}
		}
		if artifact.Kind == "portfolio" && artifact.State == "available" {
			for _, name := range artifact.Files {
				if name == "project_portfolio.json" {
					var source portfolioSummary
					if rejectDuplicateJSONKeys(snapshot.Files[name]) == nil && json.Unmarshal(snapshot.Files[name], &source) == nil && source.SchemaVersion == "1" && strings.TrimSpace(source.Summary) != "" && utf8.ValidString(source.Summary) && utf8.RuneCountInString(source.Summary) <= 240 {
						result["portfolio_summary"] = source.Summary
					}
				}
				if name == "project_portfolio/cover.png" {
					result["portfolio_cover_url"] = snapshotFileURL(snapshot, fingerprint, name)
				}
			}
		}
		if artifact.Kind == "documents" && artifact.State == "available" {
			files := make([]map[string]string, 0, len(artifact.Files))
			for _, name := range artifact.Files {
				files = append(files, map[string]string{"path": name, "url": snapshotFileURL(snapshot, fingerprint, name)})
			}
			result["documents"] = files
		}
	}
	return result
}

func admittedRoadmapProjection(projectID string, data []byte, files map[string][]byte, admitted []string, sources todoSourceMap) (roadmapProjection, bool) {
	admittedFiles := make(map[string]struct{}, len(admitted))
	for _, name := range admitted {
		admittedFiles[name] = struct{}{}
	}
	isAdmitted := func(target string) bool {
		_, ok := admittedFiles[target]
		return ok
	}

	var projection roadmapProjection
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if rejectDuplicateJSONKeys(data) != nil || decoder.Decode(&projection) != nil || decoder.Decode(new(any)) != io.EOF ||
		projection.SchemaVersion != "builder-roadmap-projection-v3" || projection.ProjectID != projectID || projection.Epics == nil ||
		projection.Backlog.TODOs == nil || projection.Backlog.Folders == nil || projection.Unclassified == nil ||
		projection.Diagnostics == nil || len(projection.Diagnostics) > 64 || len(projection.Unclassified) > 4096 || len(projection.Epics) > 32 {
		return roadmapProjection{}, false
	}
	if sources.SchemaVersion != "builder-todo-sources-v2" || sources.Sources == nil || len(sources.byPath) != len(sources.Sources) {
		return roadmapProjection{}, false
	}
	seenTODOIDs := make(map[string]bool)
	validateTODO := func(sourceTODO *roadmapTODO, allowMissing bool) bool {
		if sourceTODO.ID == "" || seenTODOIDs[sourceTODO.ID] || sourceTODO.Path == "" || len(sourceTODO.Path) > 1024 ||
			!strings.HasPrefix(sourceTODO.Path, "todos/") || !safeSnapshotPath(sourceTODO.Path) ||
			(sourceTODO.Kind != "executable" && sourceTODO.Kind != "planning") {
			return false
		}
		if sourceTODO.Missing {
			if !allowMissing || isAdmitted(sourceTODO.Path) || files[sourceTODO.Path] != nil {
				return false
			}
			sourceTODO.Title = sourceTODO.ID
			sourceTODO.RecordState = "missing"
		} else {
			mapped, ok := sources.Sources[sourceTODO.ID]
			if !ok || mapped.Path != sourceTODO.Path {
				return false
			}
			if sourceTODO.Title == "" || utf8.RuneCountInString(sourceTODO.Title) > 160 || !validRoadmapField(sourceTODO.Objective, sourceTODO.ObjectiveState, 1024) || !validRoadmapField(sourceTODO.NextStep, sourceTODO.NextStepState, 512) || !validRoadmapRecordState(sourceTODO.RecordState) || !validRoadmapLifecycleTODO(*sourceTODO) {
				return false
			}
		}
		if sourceTODO.Missing && (!validRoadmapLifecycleTODO(*sourceTODO) || sourceTODO.WorkState != "unknown") {
			return false
		}
		seenTODOIDs[sourceTODO.ID] = true
		return true
	}
	for e := range projection.Epics {
		if projection.Epics[e].ID == "" || projection.Epics[e].Releases == nil || len(projection.Epics[e].Releases) > 64 {
			return roadmapProjection{}, false
		}
		for r := range projection.Epics[e].Releases {
			if projection.Epics[e].Releases[r].ID == "" || projection.Epics[e].Releases[r].TODOs == nil {
				return roadmapProjection{}, false
			}
			for t := range projection.Epics[e].Releases[r].TODOs {
				if !validateTODO(&projection.Epics[e].Releases[r].TODOs[t], true) {
					return roadmapProjection{}, false
				}
			}
		}
	}
	var validateFolder func(*roadmapBacklogFolder, string) bool
	validateFolder = func(folder *roadmapBacklogFolder, parent string) bool {
		if folder.Name == "" || folder.Path != parent+"/"+folder.Name || !safeSnapshotPath(folder.Path) ||
			folder.Folders == nil || folder.TODOs == nil {
			return false
		}
		for i := range folder.TODOs {
			if folder.TODOs[i].Kind != "executable" || folder.TODOs[i].ID != "backlog:"+folder.TODOs[i].Path ||
				!strings.HasPrefix(folder.TODOs[i].Path, folder.Path+"/") || !validateTODO(&folder.TODOs[i], false) {
				return false
			}
		}
		for i := range folder.Folders {
			if !validateFolder(&folder.Folders[i], folder.Path) {
				return false
			}
		}
		return true
	}
	for i := range projection.Backlog.TODOs {
		todo := &projection.Backlog.TODOs[i]
		if todo.Kind != "executable" || todo.ID != "backlog:"+todo.Path || !strings.HasPrefix(todo.Path, "todos/active/") ||
			strings.Contains(strings.TrimPrefix(todo.Path, "todos/active/"), "/") || !validateTODO(todo, false) {
			return roadmapProjection{}, false
		}
	}
	for i := range projection.Backlog.Folders {
		if !validateFolder(&projection.Backlog.Folders[i], "todos/active") {
			return roadmapProjection{}, false
		}
	}
	seenUnclassified := make(map[string]bool, len(projection.Unclassified))
	for _, item := range projection.Unclassified {
		if item.Path == "" || len(item.Path) > 1024 || item.Reason == "" || len(item.Reason) > 512 ||
			!strings.HasPrefix(item.Path, "todos/") || !safeSnapshotPath(item.Path) || seenUnclassified[item.Path] {
			return roadmapProjection{}, false
		}
		seenUnclassified[item.Path] = true
	}
	for _, diagnostic := range projection.Diagnostics {
		if diagnostic.Code == "" || len(diagnostic.Code) > 64 || diagnostic.Message == "" || len(diagnostic.Message) > 512 ||
			(diagnostic.Path != "" && (len(diagnostic.Path) > 1024 || !safeSnapshotPath(diagnostic.Path))) {
			return roadmapProjection{}, false
		}
	}
	return projection, true
}

func validRoadmapField(value, state string, max int) bool {
	switch state {
	case "absent", "too_long":
		return value == ""
	case "complete":
		return value != "" && utf8.ValidString(value) && len([]byte(value)) <= max
	default:
		return false
	}
}
func validRoadmapRecordState(value string) bool {
	return value == "active" || value == "completed" || value == "promotion_lane" || value == "other"
}

func roadmapTODOHeading(content []byte) string {
	for _, line := range strings.Split(string(content), "\n") {
		if strings.HasPrefix(line, "# ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "# "))
		}
	}
	return ""
}

func validRoadmapDeliveryStage(value string) bool {
	switch value {
	case "Planning", "Implementation-Approval-Pending", "Ready-for-Implementation", "In-Progress", "Local-Implemented", "Promotion-Lane", "Completed":
		return true
	default:
		return false
	}
}

func validRoadmapLifecycleTODO(todo roadmapTODO) bool {
	if len(todo.AuthoredDeliveryStage) > 256 || !utf8.ValidString(todo.AuthoredDeliveryStage) || (todo.WorkState != "unknown" && todo.WorkState != "blocked") {
		return false
	}
	if len(todo.DispositionReason) > 512 || len(todo.SuccessorTODO) > 1200 || !utf8.ValidString(todo.DispositionReason) || !utf8.ValidString(todo.SuccessorTODO) {
		return false
	}
	switch todo.DispositionState {
	case "":
		if todo.HistoricalDisposition != "" || todo.DispositionReason != "" || todo.SuccessorTODO != "" {
			return false
		}
	case "valid":
		if (todo.HistoricalDisposition != "Superseded" && todo.HistoricalDisposition != "Discarded") || todo.DispositionReason == "" {
			return false
		}
	case "invalid":
		// Keep one invalid TODO visible with its TEACH instead of dropping the Roadmap.
	default:
		return false
	}
	switch todo.DeliveryStageState {
	case "verified":
		if !validRoadmapDeliveryStage(todo.DeliveryStage) || todo.DeliveryStageTeach != "" {
			return false
		}
	case "unverified":
		if todo.DeliveryStage != "" || !strings.HasPrefix(todo.DeliveryStageTeach, "TEACH:") || len(todo.DeliveryStageTeach) > 1024 {
			return false
		}
	default:
		return false
	}
	switch todo.QualifierState {
	case "verified":
		if todo.QualifierTeach != "" {
			return false
		}
	case "unverified":
		if todo.Blocked || !strings.HasPrefix(todo.QualifierTeach, "TEACH:") || len(todo.QualifierTeach) > 1024 {
			return false
		}
	default:
		return false
	}
	return true
}

func containsRoadmapFile(files []string, target string) bool {
	for _, name := range files {
		if name == target {
			return true
		}
	}
	return false
}

func snapshotFileURL(snapshot registeredSnapshot, fingerprint, name string) string {
	parts := strings.Split(name, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return "/snapshot/" + url.PathEscape(snapshot.CompanyID) + "/" + url.PathEscape(snapshot.ProjectID) + "/" + fingerprint + "/" + strings.Join(parts, "/")
}

func (p *registeredPreview) snapshot(projectID, companyID, fingerprint string) (registeredSnapshot, string, error) {
	if len(fingerprint) != 64 {
		return registeredSnapshot{}, "", errors.New("fingerprint invalid")
	}
	if _, err := hex.DecodeString(fingerprint); err != nil {
		return registeredSnapshot{}, "", err
	}
	file := filepath.Join(p.root, "snapshots", fingerprint+".json")
	if err := rejectLocalPathComponents(p.root, filepath.ToSlash(filepath.Join("snapshots", fingerprint+".json"))); err != nil {
		return registeredSnapshot{}, "", err
	}
	data, err := readRegisteredFile(file, 64<<20)
	if err != nil {
		return registeredSnapshot{}, "", err
	}
	sum := sha256.Sum256(data)
	actual := hex.EncodeToString(sum[:])
	if actual != fingerprint {
		return registeredSnapshot{}, "", errors.New("snapshot fingerprint mismatch")
	}
	var snapshot registeredSnapshot
	if rejectDuplicateJSONKeys(data) != nil || json.Unmarshal(data, &snapshot) != nil || snapshot.SchemaVersion != "builder-project-snapshot-v3" || snapshot.ProjectID != projectID || snapshot.CompanyID != companyID || snapshot.Files == nil || snapshot.Artifacts == nil || snapshot.Observations == nil {
		return registeredSnapshot{}, "", errors.New("snapshot identity invalid")
	}
	if len(data) > 4<<20 {
		return registeredSnapshot{}, "", errors.New("snapshot storage bound or version invalid")
	}
	snapshot.todoSources = parseTodoSourceMap(snapshot)
	snapshot.todoSourcesParsed = true
	var total int64
	for name, content := range snapshot.Files {
		if !safeSnapshotPath(name) || len(content) > 2<<20 {
			return registeredSnapshot{}, "", errors.New("snapshot file is unsafe or too large")
		}
		total += int64(len(content))
		if total > 48<<20 || strings.EqualFold(filepath.Ext(name), ".png") {
			return registeredSnapshot{}, "", errors.New("snapshot decoded content limit exceeded")
		}
	}
	var assetTotal int64
	for name, ref := range snapshot.Assets {
		if !safeSnapshotPath(name) || !strings.EqualFold(filepath.Ext(name), ".png") || len(ref.SHA256) != 64 || strings.ToLower(ref.SHA256) != ref.SHA256 || ref.Size <= 0 || ref.Size > 2<<20 {
			return registeredSnapshot{}, "", errors.New("snapshot asset reference invalid")
		}
		if _, err := hex.DecodeString(ref.SHA256); err != nil {
			return registeredSnapshot{}, "", errors.New("snapshot asset digest invalid")
		}
		assetTotal += ref.Size
		if assetTotal > 32<<20 {
			return registeredSnapshot{}, "", errors.New("snapshot asset aggregate limit exceeded")
		}
	}
	admitted := map[string]bool{}
	for _, artifact := range snapshot.Artifacts {
		if artifact.ID == "" || artifact.Kind == "" || (artifact.State != "available" && artifact.State != "pending" && artifact.State != "invalid") {
			return registeredSnapshot{}, "", errors.New("snapshot artifact state invalid")
		}
		seen := map[string]bool{}
		for _, name := range artifact.Files {
			if !safeSnapshotPath(name) || seen[name] {
				return registeredSnapshot{}, "", errors.New("snapshot artifact file list invalid")
			}
			if !snapshotHasFile(snapshot, name) {
				return registeredSnapshot{}, "", errors.New("snapshot artifact file missing")
			}
			if _, inline := snapshot.Files[name]; inline {
				if _, asset := snapshot.Assets[name]; asset {
					return registeredSnapshot{}, "", errors.New("snapshot path has multiple representations")
				}
			}
			admitted[name] = true
			seen[name] = true
		}
		if artifact.State == "available" && (!safeSnapshotPath(artifact.Entry) || !seen[artifact.Entry]) {
			return registeredSnapshot{}, "", errors.New("snapshot available entry is not admitted")
		}
		if artifact.Kind == "prototype" && artifact.State == "available" && !validPrototypeRelatedFiles(snapshot, artifact, snapshot.todoSources) {
			return registeredSnapshot{}, "", errors.New("snapshot Prototype evidence is not admitted")
		}
	}
	for name := range snapshot.Assets {
		if !admitted[name] {
			return registeredSnapshot{}, "", errors.New("snapshot asset is not admitted by an artifact")
		}
	}
	if hasAvailablePrototype(snapshot.Artifacts) && !validPrototypeStatusObservation(snapshot) {
		diagnostic := "Current Prototype projection does not match its admitted v3 manifest; correct the source and publish the Foundation branch."
		if savedPrototypeSchema(snapshot) == "2" {
			diagnostic = "Prototype schema v2 is not available after the v3 cutover. TEACH: run fresh Prototype status; adapt every offending v2 catalog/manifest, including archived candidates, to schema_version 3; confirm the complete collection is go; then publish the Foundation branch and reload this Project."
		}
		for i := range snapshot.Artifacts {
			artifact := &snapshot.Artifacts[i]
			if artifact.Kind == "prototype" && artifact.State == "available" {
				artifact.State = "invalid"
				artifact.Entry = ""
				artifact.Files = nil
				artifact.Diagnostic = diagnostic
			}
		}
	}
	return snapshot, actual, nil
}

func hasAvailablePrototype(artifacts []registeredArtifact) bool {
	for _, artifact := range artifacts {
		if artifact.Kind == "prototype" && artifact.State == "available" {
			return true
		}
	}
	return false
}

func savedPrototypeSchema(snapshot registeredSnapshot) string {
	var status struct {
		SchemaVersion string `json:"schema_version"`
	}
	_ = json.Unmarshal(snapshot.Observations["prototype_status"], &status)
	return status.SchemaVersion
}

func validPrototypeStatusObservation(snapshot registeredSnapshot) bool {
	data, ok := snapshot.Observations["prototype_status"]
	if !ok || rejectDuplicateJSONKeys(data) != nil {
		return false
	}
	var status struct {
		SchemaVersion string `json:"schema_version"`
		ProjectID     string `json:"project_id"`
		Target        string `json:"target"`
		Authority     string `json:"authority_scope"`
		Outcome       string `json:"outcome"`
		Items         []struct {
			ID         string `json:"id"`
			Root       string `json:"root"`
			EntryPoint string `json:"entry_point"`
			Screens    []struct {
				ID             string `json:"id"`
				Path           string `json:"path"`
				DefaultStateID string `json:"default_state_id"`
				States         []struct {
					ID                 string            `json:"id"`
					ApprovedReferences []json.RawMessage `json:"approved_references"`
				} `json:"states"`
			} `json:"screens"`
			Transitions []json.RawMessage `json:"transitions"`
			Scenarios   []savedScenario   `json:"scenarios"`
		} `json:"items"`
	}
	if json.Unmarshal(data, &status) != nil || status.SchemaVersion != "3" || status.ProjectID != snapshot.ProjectID || status.Target != "prototypes" || status.Authority != "local_structure_only" || status.Outcome != "go" || status.Items == nil {
		return false
	}
	for _, artifact := range snapshot.Artifacts {
		if artifact.Kind != "prototype" || artifact.State != "available" {
			continue
		}
		var item *struct {
			ID         string `json:"id"`
			Root       string `json:"root"`
			EntryPoint string `json:"entry_point"`
			Screens    []struct {
				ID             string `json:"id"`
				Path           string `json:"path"`
				DefaultStateID string `json:"default_state_id"`
				States         []struct {
					ID                 string            `json:"id"`
					ApprovedReferences []json.RawMessage `json:"approved_references"`
				} `json:"states"`
			} `json:"screens"`
			Transitions []json.RawMessage `json:"transitions"`
			Scenarios   []savedScenario   `json:"scenarios"`
		}
		for i := range status.Items {
			if status.Items[i].ID == artifact.ID {
				item = &status.Items[i]
				break
			}
		}
		if item == nil || item.Root == "" || item.EntryPoint == "" || len(item.Screens) == 0 || item.Transitions == nil || item.Scenarios == nil {
			return false
		}
		manifestPath := path.Join(item.Root, "prototype.json")
		manifestBytes, exists := snapshot.Files[manifestPath]
		if !exists || rejectDuplicateJSONKeys(manifestBytes) != nil {
			return false
		}
		var manifest struct {
			SchemaVersion string          `json:"schema_version"`
			ID            string          `json:"id"`
			Scenarios     []savedScenario `json:"scenarios"`
		}
		if json.Unmarshal(manifestBytes, &manifest) != nil || manifest.SchemaVersion != "3" || manifest.ID != artifact.ID || manifest.Scenarios == nil || !sameSavedScenarios(manifest.Scenarios, item.Scenarios) {
			return false
		}
		entryMatches := false
		declaredPairs := make(map[string]bool)
		for _, screen := range item.Screens {
			if !validRegistrationID(screen.ID) || screen.Path == "" || !validRegistrationID(screen.DefaultStateID) || len(screen.States) == 0 {
				return false
			}
			defaultFound := false
			for _, state := range screen.States {
				if !validRegistrationID(state.ID) || state.ApprovedReferences == nil {
					return false
				}
				declaredPairs[screen.ID+"\x00"+state.ID] = true
				if state.ID == screen.DefaultStateID {
					defaultFound = true
				}
			}
			if !defaultFound {
				return false
			}
			if screen.Path == item.EntryPoint {
				entryMatches = true
			}
		}
		if !entryMatches || artifact.Entry != path.Join(item.Root, item.EntryPoint) {
			return false
		}
		seenScenarioIDs := make(map[string]bool, len(item.Scenarios))
		for _, scenario := range item.Scenarios {
			if !validRegistrationID(scenario.ID) || seenScenarioIDs[scenario.ID] || !validRegistrationName(scenario.Name) || len(scenario.Steps) == 0 {
				return false
			}
			seenScenarioIDs[scenario.ID] = true
			for _, step := range scenario.Steps {
				if !validRegistrationID(step.ScreenID) || !validRegistrationID(step.StateID) || !declaredPairs[step.ScreenID+"\x00"+step.StateID] {
					return false
				}
			}
		}
	}
	return true
}

type savedScenario struct {
	ID    string              `json:"id"`
	Name  string              `json:"name"`
	Steps []savedScenarioStep `json:"steps"`
}

type savedScenarioStep struct {
	ScreenID string `json:"screen_id"`
	StateID  string `json:"state_id"`
}

func sameSavedScenarios(left, right []savedScenario) bool {
	leftBytes, leftErr := json.Marshal(left)
	rightBytes, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && string(leftBytes) == string(rightBytes)
}

func safeSnapshotPath(value string) bool {
	if value == "" || strings.Contains(value, "\\") || strings.HasPrefix(value, "/") {
		return false
	}
	clean := path.Clean(value)
	return clean == value && clean != "." && clean != ".." && !strings.HasPrefix(clean, "../")
}

func snapshotHasFile(snapshot registeredSnapshot, name string) bool {
	if _, ok := snapshot.Files[name]; ok {
		return true
	}
	_, ok := snapshot.Assets[name]
	return ok
}

func validPrototypeRelatedFiles(snapshot registeredSnapshot, artifact registeredArtifact, todoSources todoSourceMap) bool {
	files := snapshot.Files
	entryParts := strings.Split(artifact.Entry, "/")
	if len(entryParts) < 3 || entryParts[0] != "prototypes" {
		return false
	}
	prototypeRoot := path.Join("prototypes", entryParts[1])
	manifestPath := prototypeRoot + "/prototype.json"
	var listed = make(map[string]bool, len(artifact.Files))
	for _, name := range artifact.Files {
		listed[name] = true
	}
	manifestBytes, ok := files[manifestPath]
	if !ok || !listed[manifestPath] || rejectDuplicateJSONKeys(manifestBytes) != nil {
		return false
	}
	var manifest struct {
		Related []struct {
			Kind string `json:"kind"`
			Path string `json:"path"`
		} `json:"related"`
	}
	if json.Unmarshal(manifestBytes, &manifest) != nil || manifest.Related == nil {
		return false
	}
	relatedPaths := make(map[string]bool, len(manifest.Related))
	for _, related := range manifest.Related {
		if !approvedPrototypeRelatedPath(related.Kind, related.Path) || relatedPaths[related.Path] {
			return false
		}
		if strings.HasPrefix(related.Path, "todos/") {
			if _, ok := todoSources.byPath[related.Path]; !ok {
				return false
			}
		} else if !listed[related.Path] || !snapshotHasFile(snapshot, related.Path) {
			return false
		}
		relatedPaths[related.Path] = true
	}
	for _, name := range artifact.Files {
		if !strings.HasPrefix(name, prototypeRoot+"/") && !relatedPaths[name] {
			return false
		}
	}
	return true
}

func parseTodoSourceMap(snapshot registeredSnapshot) todoSourceMap {
	data := snapshot.Files["roadmap/todo-sources.json"]
	var mapping todoSourceMap
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if len(data) == 0 || len(data) > 2<<20 || rejectDuplicateJSONKeys(data) != nil || decoder.Decode(&mapping) != nil || decoder.Decode(new(any)) != io.EOF || mapping.SchemaVersion != "builder-todo-sources-v2" || mapping.Sources == nil {
		return todoSourceMap{}
	}
	mapping.byPath = make(map[string]string, len(mapping.Sources))
	for id, item := range mapping.Sources {
		if !validTodoSourceEntry(id, item) {
			return todoSourceMap{}
		}
		if _, exists := mapping.byPath[item.Path]; exists {
			return todoSourceMap{}
		}
		mapping.byPath[item.Path] = id
	}
	return mapping
}

func approvedPrototypeRelatedPath(kind, value string) bool {
	if (kind != "todo" && kind != "decision" && kind != "documentation") || !safeSnapshotPath(value) {
		return false
	}
	segments := strings.Split(value, "/")
	for _, segment := range segments {
		lower := strings.ToLower(segment)
		if strings.HasPrefix(segment, ".") || lower == "tmp" || lower == "temp" || lower == "generated" || lower == "ephemeral" {
			return false
		}
	}
	ext := strings.ToLower(path.Ext(value))
	if ext != ".md" && ext != ".markdown" {
		return false
	}
	if len(segments) == 1 {
		base := strings.TrimSuffix(segments[0], path.Ext(segments[0]))
		switch base {
		case "domain_entities", "project_constitution", "project_landing", "project_mandate", "system_roadmap":
			return true
		default:
			return false
		}
	}
	switch segments[0] {
	case "modules", "policies", "todos":
		return true
	default:
		return false
	}
}

func (p *registeredPreview) artifactHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeError(w, 404, "not_found")
		return
	}
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/snapshot/"), "/", 4)
	if len(parts) != 4 || !safeSnapshotPath(parts[3]) {
		writeError(w, 404, "not_found")
		return
	}
	currentFingerprint, err := p.currentFingerprint(parts[1], parts[0])
	if err != nil || currentFingerprint != parts[2] {
		writeError(w, 404, "snapshot_unavailable")
		return
	}
	snapshot, fingerprint, err := p.snapshot(parts[1], parts[0], parts[2])
	if err != nil {
		writeError(w, 404, "snapshot_unavailable")
		return
	}
	content, ok := snapshot.Files[parts[3]]
	asset, assetBacked := snapshot.Assets[parts[3]]
	if !ok && !assetBacked {
		writeError(w, 404, "artifact_not_admitted")
		return
	}
	kind := ""
	for _, artifact := range snapshot.Artifacts {
		if artifact.State != "available" {
			continue
		}
		for _, name := range artifact.Files {
			if name == parts[3] {
				kind = artifact.Kind
				break
			}
		}
	}
	if kind == "" {
		writeError(w, 404, "artifact_not_admitted")
		return
	}
	if assetBacked {
		assetPath := filepath.Join(p.root, "assets", asset.SHA256)
		if err := rejectLocalPathComponents(p.root, filepath.ToSlash(filepath.Join("assets", asset.SHA256))); err != nil {
			writeError(w, 404, "snapshot_unavailable")
			return
		}
		content, err = readRegisteredFile(assetPath, 2<<20)
		if err != nil || int64(len(content)) != asset.Size {
			writeError(w, 404, "snapshot_unavailable")
			return
		}
		sum := sha256.Sum256(content)
		if hex.EncodeToString(sum[:]) != asset.SHA256 {
			writeError(w, 404, "snapshot_unavailable")
			return
		}
	}
	switch strings.ToLower(filepath.Ext(parts[3])) {
	case ".css":
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
	case ".ttf":
		w.Header().Set("Content-Type", "font/ttf")
	case ".js":
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	case ".png":
		w.Header().Set("Content-Type", "image/png")
	case ".svg":
		w.Header().Set("Content-Type", "image/svg+xml")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	case ".jpg", ".jpeg":
		w.Header().Set("Content-Type", "image/jpeg")
	case ".webp":
		w.Header().Set("Content-Type", "image/webp")
	case ".avif":
		w.Header().Set("Content-Type", "image/avif")
	case ".json":
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
	case ".md":
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox; frame-ancestors 'self'")
	default:
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		registeredArtifactHeaders(w, kind)
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Builder-Snapshot-Fingerprint", fingerprint)
	_, _ = w.Write(content)
}

func registeredArtifactHeaders(w http.ResponseWriter, kind string) {
	policy := "default-src 'none'; style-src 'self'; img-src 'self' data:; font-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'self'; sandbox"
	if kind == "prototype" {
		policy = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'self'; sandbox allow-scripts"
	}
	w.Header().Set("Content-Security-Policy", policy)
}

func rejectLocalPathComponents(root, relative string) error {
	if filepath.IsAbs(relative) || strings.Contains(relative, "..") || filepath.Clean(filepath.FromSlash(relative)) != filepath.FromSlash(relative) {
		return errors.New("unsafe path")
	}
	current := root
	for i, part := range strings.Split(filepath.ToSlash(relative), "/") {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("unsafe path component")
		}
		if i < len(strings.Split(filepath.ToSlash(relative), "/"))-1 && !info.IsDir() {
			return errors.New("non-directory path component")
		}
	}
	return nil
}

func localStaticHandler(root string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			writeError(w, 404, "not_found")
			return
		}
		if r.URL.Path == "/" || r.URL.Path == "/index.html" {
			http.ServeFile(w, r, filepath.Join(root, "index.html"))
			return
		}
		if strings.HasPrefix(r.URL.Path, "/assets/") && !strings.Contains(r.URL.Path, "..") {
			staticHandler(root).ServeHTTP(w, r)
			return
		}
		writeError(w, 404, "not_found")
	})
}
