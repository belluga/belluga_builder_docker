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
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// registeredPreview is the shared read-only API client used by the Builder
// service. Project snapshot storage and rendering are retired.
type registeredPreview struct {
	registryURL string
	client      *http.Client
}

type registeredCompany struct {
	ID   string `json:"id"`
	Name string `json:"name"`
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

type roadmapProjection struct {
	SchemaVersion string            `json:"schema_version"`
	ProjectID     string            `json:"project_id"`
	FocusRelease  string            `json:"focus_release_id"`
	Epics         []roadmapEpic     `json:"epics"`
	Backlog       roadmapBacklog    `json:"backlog"`
	Unclassified  []json.RawMessage `json:"unclassified"`
	Diagnostics   []json.RawMessage `json:"diagnostics"`
}

type roadmapBacklog struct {
	TODOs   []roadmapTODO          `json:"todos"`
	Folders []roadmapBacklogFolder `json:"folders"`
}

type roadmapBacklogFolder struct {
	Name    string                 `json:"name"`
	Path    string                 `json:"path"`
	Folders []roadmapBacklogFolder `json:"folders"`
	TODOs   []roadmapTODO          `json:"todos"`
}

type roadmapEpic struct {
	ID       string           `json:"id"`
	Name     string           `json:"name"`
	Releases []roadmapRelease `json:"releases"`
}

type roadmapRelease struct {
	ID    string        `json:"id"`
	Name  string        `json:"name"`
	TODOs []roadmapTODO `json:"todos"`
}

type roadmapTODO struct {
	ID                       string `json:"id"`
	TODORevision             *int64 `json:"todo_revision,omitempty"`
	Title                    string `json:"title"`
	Path                     string `json:"path"`
	Kind                     string `json:"kind"`
	RecordState              string `json:"record_state"`
	DeliveryStage            string `json:"delivery_stage,omitempty"`
	AuthoredDeliveryStage    string `json:"authored_delivery_stage"`
	DeliveryStageState       string `json:"delivery_stage_state"`
	DeliveryStageBasis       string `json:"delivery_stage_basis,omitempty"`
	DeliveryStageTeach       string `json:"delivery_stage_teach,omitempty"`
	AdministrativeAcceptance *struct {
		AcceptedStage      string `json:"accepted_stage"`
		OwnerReference     string `json:"owner_reference"`
		Reason             string `json:"reason"`
		PermissionDecision string `json:"permission_decision"`
		ContentDigest      string `json:"content_digest"`
	} `json:"administrative_acceptance,omitempty"`
	Disposition *struct {
		Kind            string `json:"kind"`
		Reason          string `json:"reason"`
		SuccessorTODOID string `json:"successor_todo_id,omitempty"`
	} `json:"disposition,omitempty"`
	DispositionState string `json:"disposition_state,omitempty"`
	QualifierState   string `json:"qualifier_state"`
}

func validRegistrationName(value string) bool {
	return strings.TrimSpace(value) != "" && utf8.ValidString(value) && utf8.RuneCountInString(value) <= 160
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

func canonicalTODOStage(stage string) bool {
	switch stage {
	case "Planning", "Implementation-Approval-Pending", "Ready-for-Implementation", "In-Progress", "Local-Implemented", "Promotion-Lane", "Completed":
		return true
	default:
		return false
	}
}

func oneQueryValue(q url.Values, key string) (string, bool) {
	values, ok := q[key]
	if !ok || len(values) != 1 {
		return "", false
	}
	return values[0], true
}

func rejectDuplicateJSONKeys(data []byte) error {
	d := json.NewDecoder(strings.NewReader(string(data)))
	var walk func() error
	walk = func() error {
		token, err := d.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				keyToken, err := d.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok || seen[key] {
					return errors.New("duplicate or invalid JSON key")
				}
				seen[key] = true
				if err := walk(); err != nil {
					return err
				}
			}
			_, err := d.Token()
			return err
		case '[':
			for d.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			_, err := d.Token()
			return err
		default:
			return errors.New("unexpected JSON delimiter")
		}
	}
	if err := walk(); err != nil {
		return err
	}
	if _, err := d.Token(); err == nil {
		return errors.New("trailing JSON value")
	}
	return nil
}

func validSHA256(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func (p *registeredPreview) roadmapAPIHandler(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	companyID, companyOK := oneQueryValue(q, "company_id")
	projectID, projectOK := oneQueryValue(q, "project_id")
	todoID, todoOK := oneQueryValue(q, "todo_id")
	todoRevision, revisionOK := oneQueryValue(q, "todo_revision")
	releaseID, releaseOK := oneQueryValue(q, "release_id")
	if r.Method != http.MethodGet || r.ContentLength > 0 || len(r.URL.RawQuery) > 2048 || !companyOK || !projectOK || !validRegistrationID(companyID) || !validRegistrationID(projectID) || len(q) != 2 && len(q) != 3 && len(q) != 4 || len(q) == 3 && (!releaseOK || !validRegistrationID(releaseID)) {
		writeError(w, http.StatusBadRequest, "roadmap_request_invalid")
		return
	}
	endpointPath := "/internal/v1/companies/" + url.PathEscape(companyID) + "/projects/" + url.PathEscape(projectID) + "/roadmap"
	if len(q) == 3 {
		endpointPath += "/releases/" + url.PathEscape(releaseID) + "/investigation"
	} else if len(q) == 4 {
		revision, err := strconv.ParseInt(todoRevision, 10, 64)
		if !todoOK || todoID == "" || !revisionOK || err != nil || revision < 0 || !validRegistrationID(todoID) {
			writeError(w, http.StatusBadRequest, "roadmap_todo_request_invalid")
			return
		}
		endpointPath = "/internal/v1/companies/" + url.PathEscape(companyID) + "/projects/" + url.PathEscape(projectID) + "/todos/" + url.PathEscape(todoID)
		endpointPath += "?todo_revision=" + strconv.FormatInt(revision, 10)
	}
	base := strings.TrimRight(strings.TrimSpace(p.registryURL), "/")
	if base == "" {
		base = strings.TrimRight(strings.TrimSpace(os.Getenv("DELPHI_BUILDER_REGISTRY_URL")), "/")
	}
	if base == "" {
		base = "http://builder-registry:8080"
	}
	registered, err := p.registryProjectRegistered(r, base, companyID, projectID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "roadmap_registry_unavailable")
		return
	}
	if !registered {
		writeError(w, http.StatusNotFound, "project_not_found")
		return
	}
	u, err := url.Parse(base + endpointPath)
	if err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil || u.Fragment != "" {
		writeError(w, http.StatusServiceUnavailable, "roadmap_api_unavailable")
		return
	}
	request, err := http.NewRequestWithContext(r.Context(), http.MethodGet, u.String(), nil)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "roadmap_api_unavailable")
		return
	}
	client := p.client
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	response, err := client.Do(request)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "roadmap_api_unavailable")
		return
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 4<<20+1))
	if err != nil || len(body) > 4<<20 || rejectDuplicateJSONKeys(body) != nil {
		writeError(w, http.StatusServiceUnavailable, "roadmap_api_response_invalid")
		return
	}
	if response.StatusCode != http.StatusOK {
		if response.StatusCode >= 400 && response.StatusCode < 500 {
			writeJSON(w, response.StatusCode, json.RawMessage(body))
		} else {
			writeError(w, http.StatusServiceUnavailable, "roadmap_api_unavailable")
		}
		return
	}
	if len(q) == 2 {
		if !validRegistryRoadmap(body, projectID) {
			writeError(w, http.StatusServiceUnavailable, "roadmap_api_response_invalid")
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
		return
	}
	if len(q) == 3 {
		var result struct {
			CompanyID string          `json:"company_id"`
			ProjectID string          `json:"project_id"`
			ReleaseID string          `json:"release_id"`
			State     string          `json:"state"`
			Document  json.RawMessage `json:"document"`
		}
		if json.Unmarshal(body, &result) != nil || result.CompanyID != companyID || result.ProjectID != projectID || result.ReleaseID != releaseID || result.State != "available" && result.State != "absent" || result.State == "available" && (len(result.Document) == 0 || string(result.Document) == "null") || result.State == "absent" && len(result.Document) != 0 {
			writeError(w, http.StatusServiceUnavailable, "roadmap_api_response_invalid")
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
		return
	}
	var result struct {
		CompanyID       string `json:"company_id"`
		ProjectID       string `json:"project_id"`
		ContentRevision *int64 `json:"content_revision"`
		ContentDigest   string `json:"content_digest"`
		TODO            struct {
			ID            string `json:"id"`
			ProjectID     string `json:"project_id"`
			Revision      int64  `json:"revision"`
			Title         string `json:"title"`
			Objective     string `json:"objective"`
			NextExactStep string `json:"next_exact_step"`
			Sections      []struct {
				Key          string `json:"key"`
				Title        string `json:"title"`
				BodyMarkdown string `json:"body_markdown"`
			} `json:"sections"`
		} `json:"todo"`
		TODORevision int64 `json:"todo_revision"`
	}
	requestedRevision, _ := strconv.ParseInt(todoRevision, 10, 64)
	if json.Unmarshal(body, &result) != nil || result.CompanyID != companyID || result.ProjectID != projectID || result.TODO.ID != todoID || result.TODO.ProjectID != projectID || result.TODO.Revision != requestedRevision || result.TODORevision != requestedRevision || strings.TrimSpace(result.TODO.Title) == "" || result.TODO.Sections == nil || result.ContentRevision != nil && (*result.ContentRevision < 0 || !strings.HasPrefix(result.ContentDigest, "sha256:") || !validSHA256(strings.TrimPrefix(result.ContentDigest, "sha256:"))) || result.ContentRevision == nil && result.ContentDigest != "" {
		writeError(w, http.StatusServiceUnavailable, "roadmap_todo_response_invalid")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func (p *registeredPreview) registryProjectRegistered(r *http.Request, base, companyID, projectID string) (bool, error) {
	u, err := url.Parse(strings.TrimRight(base, "/") + "/internal/v1/catalog")
	if err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil || u.Fragment != "" {
		return false, errors.New("Registry catalog URL is invalid")
	}
	request, err := http.NewRequestWithContext(r.Context(), http.MethodGet, u.String(), nil)
	if err != nil {
		return false, err
	}
	client := p.client
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	response, err := client.Do(request)
	if err != nil {
		return false, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20+1))
	if err != nil || len(data) > 1<<20 || response.StatusCode != http.StatusOK || rejectDuplicateJSONKeys(data) != nil {
		return false, errors.New("Registry catalog is unavailable or invalid")
	}
	var catalog struct {
		SchemaVersion string `json:"schema_version"`
		Companies     []struct {
			ID string `json:"id"`
		} `json:"companies"`
		Projects []struct {
			ID        string `json:"id"`
			CompanyID string `json:"company_id"`
		} `json:"projects"`
	}
	if json.Unmarshal(data, &catalog) != nil || catalog.SchemaVersion != "1" || catalog.Companies == nil || catalog.Projects == nil {
		return false, errors.New("Registry catalog contract is invalid")
	}
	companies := make(map[string]bool, len(catalog.Companies))
	for _, company := range catalog.Companies {
		if !validRegistrationID(company.ID) || companies[company.ID] {
			return false, errors.New("Registry Company identity is invalid")
		}
		companies[company.ID] = true
	}
	projects := make(map[string]bool, len(catalog.Projects))
	registered := false
	for _, project := range catalog.Projects {
		key := project.CompanyID + "/" + project.ID
		if !validRegistrationID(project.ID) || !companies[project.CompanyID] || projects[key] {
			return false, errors.New("Registry Project identity is invalid")
		}
		projects[key] = true
		if project.CompanyID == companyID && project.ID == projectID {
			registered = true
		}
	}
	return registered, nil
}

func validRegistryRoadmap(data []byte, projectID string) bool {
	var roadmap roadmapProjection
	if len(data) == 0 || rejectDuplicateJSONKeys(data) != nil || json.Unmarshal(data, &roadmap) != nil || roadmap.SchemaVersion != "builder-roadmap-projection-v3" || roadmap.ProjectID != projectID || roadmap.Epics == nil || roadmap.Backlog.TODOs == nil || roadmap.Backlog.Folders == nil || roadmap.Unclassified == nil || roadmap.Diagnostics == nil || roadmap.FocusRelease != "" || len(roadmap.Unclassified) != 0 || len(roadmap.Diagnostics) != 0 {
		return false
	}
	seen := make(map[string]bool)
	validTODO := func(todo roadmapTODO) bool {
		if !validRegistrationID(todo.ID) || seen[todo.ID] || todo.TODORevision == nil || *todo.TODORevision < 0 || todo.Title == "" || todo.Path != "" && !validMongoTODOPath(todo.Path) || todo.Kind == "" || todo.RecordState == "" || !canonicalTODOStage(todo.AuthoredDeliveryStage) || todo.QualifierState == "" {
			return false
		}
		if todo.Disposition != nil {
			if todo.Kind != "non-delivery" || todo.DispositionState != "valid" || todo.Disposition.Kind != "Superseded" && todo.Disposition.Kind != "Discarded" || todo.Disposition.Reason == "" || todo.DeliveryStage != "" || todo.DeliveryStageTeach != "" || todo.AdministrativeAcceptance == nil || todo.AuthoredDeliveryStage != "Planning" {
				return false
			}
			seen[todo.ID] = true
			return true
		}
		switch todo.DeliveryStageState {
		case "verified":
			if !canonicalTODOStage(todo.DeliveryStage) || todo.DeliveryStage != todo.AuthoredDeliveryStage {
				return false
			}
		case "unverified":
			if todo.DeliveryStage != "" || todo.DeliveryStageTeach == "" {
				return false
			}
		case "administrative":
			if todo.DeliveryStage != todo.AuthoredDeliveryStage || todo.DeliveryStageBasis != "administrative-acceptance" || todo.AdministrativeAcceptance == nil || todo.AdministrativeAcceptance.AcceptedStage != todo.DeliveryStage || todo.AdministrativeAcceptance.OwnerReference == "" || todo.AdministrativeAcceptance.Reason == "" || todo.AdministrativeAcceptance.ContentDigest == "" || todo.Disposition != nil {
				return false
			}
		default:
			return false
		}
		seen[todo.ID] = true
		return true
	}
	var validFolders func([]roadmapBacklogFolder, string) bool
	folderPaths := make(map[string]bool)
	validFolders = func(folders []roadmapBacklogFolder, parent string) bool {
		for _, folder := range folders {
			if strings.TrimSpace(folder.Name) == "" || !validMongoTODOPath(folder.Path) || folder.Folders == nil || folder.TODOs == nil || folderPaths[folder.Path] || parent != "" && !strings.HasPrefix(folder.Path, strings.TrimSuffix(parent, "/")+"/") {
				return false
			}
			folderPaths[folder.Path] = true
			for _, todo := range folder.TODOs {
				if !validTODO(todo) {
					return false
				}
			}
			if !validFolders(folder.Folders, folder.Path) {
				return false
			}
		}
		return true
	}
	for _, epic := range roadmap.Epics {
		if !validRegistrationID(epic.ID) || !validRegistrationName(epic.Name) || epic.Releases == nil || seen[epic.ID] {
			return false
		}
		seen[epic.ID] = true
		for _, release := range epic.Releases {
			if !validRegistrationID(release.ID) || !validRegistrationName(release.Name) || release.TODOs == nil || seen[release.ID] {
				return false
			}
			seen[release.ID] = true
			for _, todo := range release.TODOs {
				if !validTODO(todo) {
					return false
				}
			}
		}
	}
	for _, todo := range roadmap.Backlog.TODOs {
		if !validTODO(todo) {
			return false
		}
	}
	return validFolders(roadmap.Backlog.Folders, "")
}

func validMongoTODOPath(value string) bool {
	if value == "" || !strings.HasPrefix(value, "todos/") || strings.Contains(value, "\\") || strings.HasPrefix(value, "/") {
		return false
	}
	clean := path.Clean(value)
	if clean != value || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == ".git" {
			return false
		}
	}
	return true
}

func localStaticHandler(root string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			writeError(w, http.StatusNotFound, "not_found")
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
		writeError(w, http.StatusNotFound, "not_found")
	})
}
