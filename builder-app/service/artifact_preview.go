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
	"unicode/utf8"
)

type registeredPreview struct{ root string }

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
	Projects      []registeredProject `json:"projects"`
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
	SourceBindingsPath   string `json:"source_bindings_path,omitempty"`
	SourceBindingsDigest string `json:"source_bindings_digest,omitempty"`
	DefaultOwnerID       string `json:"default_owner_id,omitempty"`
}
type persistedProject struct {
	registeredProject
	Binding *persistedBinding `json:"binding"`
}
type registeredSnapshot struct {
	SchemaVersion string                     `json:"schema_version"`
	ProjectID     string                     `json:"project_id"`
	CompanyID     string                     `json:"company_id"`
	ObservedAt    string                     `json:"observed_at"`
	Files         map[string][]byte          `json:"files"`
	Artifacts     []registeredArtifact       `json:"artifacts"`
	Observations  map[string]json.RawMessage `json:"observations"`
}

type roadmapCatalog struct {
	SchemaVersion string        `json:"schema_version"`
	ProjectID     string        `json:"project_id"`
	FocusRelease  string        `json:"focus_release_id"`
	Epics         []roadmapCatalogEpic `json:"epics"`
}

type roadmapCatalogEpic struct {
	ID          string                  `json:"id"`
	Name        string                  `json:"name"`
	Description string                  `json:"description"`
	Releases    []roadmapCatalogRelease `json:"releases"`
}

type roadmapCatalogRelease struct {
	ID          string              `json:"id"`
	Name        string              `json:"name"`
	Description string              `json:"description"`
	TODOs       []roadmapCatalogTODO `json:"todos"`
}

type roadmapCatalogTODO struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Path  string `json:"path"`
	Kind  string `json:"kind"`
}

type roadmapProjection struct {
	SchemaVersion        string        `json:"schema_version"`
	ProjectID            string        `json:"project_id"`
	FocusRelease         string        `json:"focus_release_id"`
	DeliveryVerification string        `json:"delivery_verification"`
	Epics                []roadmapEpic `json:"epics"`
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
	ID          string `json:"id"`
	Title       string `json:"title"`
	Path        string `json:"path"`
	Kind        string `json:"kind"`
	RecordState string `json:"record_state"`
}

type portfolioSummary struct {
	SchemaVersion string `json:"schema_version"`
	Summary       string `json:"summary"`
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
		return registrationCatalog{SchemaVersion: "builder-project-registry-v1", Companies: []registeredCompany{}, Projects: []registeredProject{}}, nil
	}
	data, err := readRegisteredFile(registryPath, 128<<10)
	var stored persistedRegistration
	if err != nil || rejectDuplicateJSONKeys(data) != nil || json.Unmarshal(data, &stored) != nil || stored.SchemaVersion != "builder-project-registry-v1" || stored.Companies == nil || stored.Projects == nil || stored.Preferences == nil {
		return registrationCatalog{}, errors.New("registration registry unavailable or invalid")
	}
	result := registrationCatalog{SchemaVersion: stored.SchemaVersion, Companies: []registeredCompany{}, Projects: []registeredProject{}}
	for key, c := range stored.Companies {
		if !validRegistrationID(c.ID) || key != c.ID || !validRegistrationName(c.Name) {
			return registrationCatalog{}, errors.New("registration company identity invalid")
		}
		result.Companies = append(result.Companies, c)
	}
	for key, storedProject := range stored.Projects {
		p := storedProject.registeredProject
		validFingerprint := len(p.SnapshotFingerprint) == 64
		if validFingerprint {
			_, decodeErr := hex.DecodeString(p.SnapshotFingerprint)
			validFingerprint = decodeErr == nil && strings.ToLower(p.SnapshotFingerprint) == p.SnapshotFingerprint
		}
		stateValid := (p.PreparationState == "ready" && validFingerprint) || (p.PreparationState == "not_ready" && (p.SnapshotFingerprint == "" || validFingerprint))
		if !validRegistrationID(p.ID) || key != p.ID || !validRegistrationName(p.Name) || !hasCompany(stored.Companies, p.CompanyID) || !validStoredBinding(storedProject.Binding) || !stateValid {
			return registrationCatalog{}, errors.New("registration project identity invalid")
		}
		result.Projects = append(result.Projects, p)
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
		if project.PreparationState != "ready" || len(project.SnapshotFingerprint) != 64 {
			writeJSON(w, 200, map[string]any{"schema_version": "1", "company_id": companyID, "project_id": projectID, "registration_state": "registered", "consumer_readiness": "not_ready", "artifacts": project.Artifacts, "diagnostic": project.PreparationDiagnostic})
			return
		}
		snapshot, fingerprint, e := p.snapshot(project.ID, project.CompanyID, project.SnapshotFingerprint)
		if e != nil {
			writeError(w, 503, "snapshot_integrity_failure")
			return
		}
		writeJSON(w, 200, registeredProjectDetail(snapshot, fingerprint))
		return
	}
	writeError(w, 404, "project_not_found")
}

func registeredProjectDetail(snapshot registeredSnapshot, fingerprint string) map[string]any {
	result := map[string]any{
		"schema_version": "1", "company_id": snapshot.CompanyID, "project_id": snapshot.ProjectID,
		"registration_state": "registered", "consumer_readiness": "ready", "snapshot_fingerprint": fingerprint,
		"snapshot_observed_at": snapshot.ObservedAt, "snapshot_observations": snapshot.Observations, "artifacts": snapshot.Artifacts,
	}
	for _, artifact := range snapshot.Artifacts {
		if artifact.Kind == "roadmap" && artifact.State == "available" {
			for _, name := range artifact.Files {
				if name == "roadmap/catalog.json" {
					if projection, ok := admittedRoadmapProjection(snapshot.ProjectID, snapshot.Files[name]); ok {
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

func admittedRoadmapProjection(projectID string, data []byte) (roadmapProjection, bool) {
	var catalog roadmapCatalog
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if rejectDuplicateJSONKeys(data) != nil || decoder.Decode(&catalog) != nil || catalog.SchemaVersion != "builder-roadmap-v1" || catalog.ProjectID != projectID || len(catalog.Epics) == 0 {
		return roadmapProjection{}, false
	}
	epics := make([]roadmapEpic, 0, len(catalog.Epics))
	for _, sourceEpic := range catalog.Epics {
		epic := roadmapEpic{ID: sourceEpic.ID, Name: sourceEpic.Name, Description: sourceEpic.Description, Releases: []roadmapRelease{}}
		for _, sourceRelease := range sourceEpic.Releases {
			release := roadmapRelease{ID: sourceRelease.ID, Name: sourceRelease.Name, Description: sourceRelease.Description, TODOs: []roadmapTODO{}}
			for _, sourceTODO := range sourceRelease.TODOs {
				recordState := "other"
				if strings.HasPrefix(sourceTODO.Path, "todos/completed/") {
					recordState = "completed"
				} else if strings.HasPrefix(sourceTODO.Path, "todos/active/") {
					recordState = "active"
				}
				release.TODOs = append(release.TODOs, roadmapTODO{ID: sourceTODO.ID, Title: sourceTODO.Title, Path: sourceTODO.Path, Kind: sourceTODO.Kind, RecordState: recordState})
			}
			epic.Releases = append(epic.Releases, release)
		}
		epics = append(epics, epic)
	}
	return roadmapProjection{SchemaVersion: catalog.SchemaVersion, ProjectID: catalog.ProjectID, FocusRelease: catalog.FocusRelease, DeliveryVerification: "unverified", Epics: epics}, true
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
	data, err := readRegisteredFile(file, 32<<20)
	if err != nil {
		return registeredSnapshot{}, "", err
	}
	sum := sha256.Sum256(data)
	actual := hex.EncodeToString(sum[:])
	if actual != fingerprint {
		return registeredSnapshot{}, "", errors.New("snapshot fingerprint mismatch")
	}
	var snapshot registeredSnapshot
	if rejectDuplicateJSONKeys(data) != nil || json.Unmarshal(data, &snapshot) != nil || snapshot.SchemaVersion != "builder-project-snapshot-v1" || snapshot.ProjectID != projectID || snapshot.CompanyID != companyID || snapshot.Files == nil || snapshot.Artifacts == nil || snapshot.Observations == nil {
		return registeredSnapshot{}, "", errors.New("snapshot identity invalid")
	}
	var total int64
	for name, content := range snapshot.Files {
		if !safeSnapshotPath(name) || len(content) > 2<<20 {
			return registeredSnapshot{}, "", errors.New("snapshot file is unsafe or too large")
		}
		total += int64(len(content))
		if total > 24<<20 {
			return registeredSnapshot{}, "", errors.New("snapshot decoded content limit exceeded")
		}
	}
	for _, artifact := range snapshot.Artifacts {
		if artifact.ID == "" || artifact.Kind == "" || (artifact.State != "available" && artifact.State != "pending" && artifact.State != "invalid") {
			return registeredSnapshot{}, "", errors.New("snapshot artifact state invalid")
		}
		seen := map[string]bool{}
		for _, name := range artifact.Files {
			if !safeSnapshotPath(name) || seen[name] {
				return registeredSnapshot{}, "", errors.New("snapshot artifact file list invalid")
			}
			if _, exists := snapshot.Files[name]; !exists {
				return registeredSnapshot{}, "", errors.New("snapshot artifact file missing")
			}
			seen[name] = true
		}
		if artifact.State == "available" && (!safeSnapshotPath(artifact.Entry) || !seen[artifact.Entry]) {
			return registeredSnapshot{}, "", errors.New("snapshot available entry is not admitted")
		}
		if artifact.Kind == "prototype" && artifact.State == "available" && !validPrototypeRelatedFiles(snapshot.Files, artifact) {
			return registeredSnapshot{}, "", errors.New("snapshot Prototype evidence is not admitted")
		}
	}
	if hasAvailablePrototype(snapshot.Artifacts) && !validPrototypeStatusObservation(snapshot) {
		diagnostic := "Saved Prototype projection does not match its admitted v3 manifest; correct the source and explicitly refresh registration."
		if savedPrototypeSchema(snapshot) == "2" {
			diagnostic = "Saved Prototype schema v2 is not available after the v3 cutover. TEACH: run fresh Prototype status; adapt every offending v2 catalog/manifest, including archived candidates, to schema_version 3; confirm the complete collection is go; then explicitly refresh registration. Registration alone cannot repair source."
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

func validPrototypeRelatedFiles(files map[string][]byte, artifact registeredArtifact) bool {
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
		if !approvedPrototypeRelatedPath(related.Kind, related.Path) || relatedPaths[related.Path] || !listed[related.Path] {
			return false
		}
		if _, ok := files[related.Path]; !ok {
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
	catalog, err := p.catalog()
	if err != nil {
		writeError(w, 503, "registration_state_unavailable")
		return
	}
	current := false
	for _, project := range catalog.Projects {
		if project.CompanyID == parts[0] && project.ID == parts[1] && project.PreparationState == "ready" && project.SnapshotFingerprint == parts[2] {
			current = true
			break
		}
	}
	if !current {
		writeError(w, 404, "snapshot_unavailable")
		return
	}
	snapshot, fingerprint, err := p.snapshot(parts[1], parts[0], parts[2])
	if err != nil {
		writeError(w, 404, "snapshot_unavailable")
		return
	}
	content, ok := snapshot.Files[parts[3]]
	if !ok {
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
		if r.URL.Path == "/" || r.URL.Path == "/index.html" || r.URL.Path == "/local" {
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
