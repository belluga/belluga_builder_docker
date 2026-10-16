package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrivateReaderPathOnlyRegistrationRequiresFreshRegistration(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]string{"state": "unavailable", "teach": "This Project has an invalid path-only registration. TEACH: re-register the Project."})
	}))
	defer server.Close()
	consumer := &registeredPreview{readerURL: server.URL, readerToken: strings.Repeat("t", 32), client: server.Client()}
	_, _, err := consumer.readCurrent("builder", "belluga-solutions")
	if !errors.Is(err, errPathOnlyReader) {
		t.Fatalf("path-only registration did not preserve its bounded TEACH classification: %v", err)
	}
}

func testReaderConsumer(t *testing.T, root string) *registeredPreview {
	t.Helper()
	base := &registeredPreview{root: root}
	catalog, err := base.catalog()
	if err != nil || len(catalog.Projects) == 0 {
		t.Fatalf("test catalog unavailable: %v", err)
	}
	project := catalog.Projects[0]
	registryData, err := os.ReadFile(filepath.Join(root, "registry.json"))
	if err != nil {
		t.Fatal(err)
	}
	var registry persistedRegistration
	if err := json.Unmarshal(registryData, &registry); err != nil {
		t.Fatal(err)
	}
	stored := registry.Projects[project.ID]
	_, fingerprint, err := base.snapshot(project.ID, project.CompanyID, stored.SnapshotFingerprint)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, _, err := base.snapshot(project.ID, project.CompanyID, fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/internal/v1/projects/"+project.ID {
			http.Error(w, "wrong private reader route", http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("t", 32) {
			http.Error(w, "unauthorized", 401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(privateReaderResponse{ProjectID: project.ID, ProjectName: project.Name, CompanyID: project.CompanyID, ProjectSHA: strings.Repeat("a", 40), ObservedAt: snapshot.ObservedAt, Fingerprint: fingerprint, Artifacts: snapshot.Artifacts, Files: snapshot.Files, Assets: snapshot.Assets, Observations: snapshot.Observations, ExternalSourceID: "design-system", ExternalSourceRevision: "r-123"})
	}))
	t.Cleanup(server.Close)
	return &registeredPreview{root: root, readerURL: server.URL, readerToken: strings.Repeat("t", 32), client: server.Client()}
}

func TestRegisteredProjectDetailExposesOnlyProjectBoundRoadmapStages(t *testing.T) {
	catalog := []byte(`{"schema_version":"builder-roadmap-v1","project_id":"project-a","focus_release_id":"r-01","epics":[{"id":"e-01","name":"Epic","description":"Strategic checkpoint","releases":[{"id":"r-01","name":"Release","description":"Selected grouping","todos":[{"id":"done","path":"todos/completed/done.md","kind":"planning"},{"id":"active","path":"todos/active/work.md","kind":"executable"}]}]}]}`)
	status := roadmapStatusForTest(catalog, roadmapLocalStage{ID: "done", Stage: "unknown"}, roadmapLocalStage{ID: "active", Stage: "Local-Implemented", WorkState: "blocked"})
	snapshot := registeredSnapshot{
		SchemaVersion: "builder-project-snapshot-v1", ProjectID: "project-a", CompanyID: "company-a",
		Files:     map[string][]byte{"roadmap/catalog.json": catalog, "roadmap/todo-status.json": status, "todos/completed/done.md": []byte("# Planning record\n"), "todos/active/work.md": []byte("# Work item\n")},
		Artifacts: []registeredArtifact{{ID: "roadmap", Kind: "roadmap", Name: "Roadmap", State: "available", Files: []string{"roadmap/catalog.json", "roadmap/todo-status.json", "todos/completed/done.md", "todos/active/work.md"}}},
	}
	detail := registeredProjectDetail(snapshot, "fingerprint-a")
	projection, ok := detail["roadmap"].(roadmapProjection)
	if !ok || projection.ProjectID != snapshot.ProjectID || projection.FocusRelease != "r-01" || len(projection.Epics) != 1 {
		t.Fatalf("roadmap projection is missing or unbound: %#v", detail["roadmap"])
	}
	encoded, err := json.Marshal(projection)
	if err != nil || strings.Contains(string(encoded), "delivery_verification") {
		t.Fatalf("roadmap still exposes a duplicate delivery status: %s, %v", encoded, err)
	}
	release := projection.Epics[0].Releases[0]
	if release.TODOs[0].RecordState != "completed" || release.TODOs[0].Kind != "planning" || release.TODOs[0].LocalStage != "unknown" || release.TODOs[1].RecordState != "active" || release.TODOs[1].LocalStage != "Local-Implemented" || release.TODOs[1].WorkState != "blocked" {
		t.Fatalf("roadmap claims are not truthful: %+v", release)
	}
	if release.TODOs[1].Title != "Work item" {
		t.Fatal("Roadmap title was not derived from the admitted TODO H1")
	}

	snapshot.ProjectID = "project-b"
	if _, ok := registeredProjectDetail(snapshot, "fingerprint-b")["roadmap"]; ok {
		t.Fatal("catalog for a different Project was exposed")
	}
}

func TestRegisteredProjectDetailFailsClosedForContradictoryRoadmapStageProjection(t *testing.T) {
	catalog := []byte(`{"schema_version":"builder-roadmap-v1","project_id":"project-a","focus_release_id":"r-01","epics":[{"id":"e-01","name":"Epic","description":"Strategic checkpoint","releases":[{"id":"r-01","name":"Release","description":"Selected grouping","todos":[{"id":"todo-a","path":"todos/active/work.md","kind":"executable"}]}]}]}`)
	valid := roadmapStatusForTest(catalog, roadmapLocalStage{ID: "todo-a", Stage: "Pending"})
	invalid := [][]byte{
		nil,
		[]byte(`not-json`),
		[]byte(`{"schema_version":"builder-roadmap-status-v1","catalog_sha256":"wrong","todos":[{"id":"todo-a","local_stage":"Pending"}]}`),
		roadmapStatusForTest(catalog, roadmapLocalStage{ID: "todo-a", Stage: "Pending"}, roadmapLocalStage{ID: "todo-a", Stage: "Pending"}),
		roadmapStatusForTest(catalog, roadmapLocalStage{ID: "different-id", Stage: "Pending"}),
		roadmapStatusForTest(catalog, roadmapLocalStage{ID: "todo-a", Stage: "delivered"}),
		roadmapStatusForTest(catalog, roadmapLocalStage{ID: "todo-a", Stage: "Pending", WorkState: "delivered"}),
	}
	for index, status := range invalid {
		snapshot := registeredSnapshot{ProjectID: "project-a", Files: map[string][]byte{"roadmap/catalog.json": catalog, "roadmap/todo-status.json": status, "todos/active/work.md": []byte("# Work item\n")}, Artifacts: []registeredArtifact{{ID: "roadmap", Kind: "roadmap", State: "available", Files: []string{"roadmap/catalog.json", "roadmap/todo-status.json", "todos/active/work.md"}}}}
		if _, ok := registeredProjectDetail(snapshot, "fp")["roadmap"]; ok {
			t.Errorf("contradictory stage projection %d was exposed", index)
		}
	}
	snapshot := registeredSnapshot{ProjectID: "project-a", Files: map[string][]byte{"roadmap/catalog.json": catalog, "roadmap/todo-status.json": valid}, Artifacts: []registeredArtifact{{ID: "roadmap", Kind: "roadmap", State: "available", Files: []string{"roadmap/catalog.json"}}}}
	if _, ok := registeredProjectDetail(snapshot, "fp")["roadmap"]; ok {
		t.Fatal("projection was exposed when derived file was not admitted by the artifact")
	}
}

func roadmapStatusForTest(catalog []byte, todos ...roadmapLocalStage) []byte {
	digest := sha256.Sum256(catalog)
	status, _ := json.Marshal(roadmapStatusProjection{SchemaVersion: "builder-roadmap-status-v1", CatalogSHA256: hex.EncodeToString(digest[:]), TODOs: todos})
	return status
}

func TestRegisteredProjectDetailExposesAdmittedOverviewOnly(t *testing.T) {
	data := []byte(`{"schema_version":"1","tagline":"Purpose","summary":"Summary","desktop_alt":"Desktop preview","mobile_alt":"Mobile preview","sections":{"problem":{"heading":"Problem","body":"Body","points":[]},"proposal":{"heading":"Proposal","body":"Body","points":[]},"impact":{"heading":"Impact","body":"Body","points":[]},"audience":{"heading":"Audience","body":"Body","points":[]}}}`)
	snapshot := registeredSnapshot{ProjectID: "project-a", CompanyID: "company-a", Files: map[string][]byte{"project_overview.json": data, "project_overview/desktop.png": []byte("png"), "project_overview/mobile.png": []byte("png")}, Artifacts: []registeredArtifact{{ID: "overview", Kind: "overview", State: "available", Entry: "project_overview.json", Files: []string{"project_overview.json", "project_overview/desktop.png", "project_overview/mobile.png"}}}}
	detail := registeredProjectDetail(snapshot, "fingerprint-a")
	overview, ok := detail["overview"].(overviewSource)
	if !ok || overview.Summary != "Summary" || !strings.Contains(overview.Media["desktop"], "fingerprint-a") || !strings.Contains(overview.Media["mobile"], "fingerprint-a") {
		t.Fatalf("Overview not bound to snapshot: %#v", detail["overview"])
	}
	delete(snapshot.Files, "project_overview/mobile.png")
	if _, ok := registeredProjectDetail(snapshot, "fingerprint-a")["overview"]; ok {
		t.Fatal("Overview without mobile hero image was exposed")
	}
	snapshot.Artifacts[0].State = "invalid"
	if _, ok := registeredProjectDetail(snapshot, "fingerprint-a")["overview"]; ok {
		t.Fatal("invalid Overview was exposed")
	}
}

func TestAdmittedRoadmapProjectionRejectsDuplicateAndMismatchedCatalog(t *testing.T) {
	for _, value := range []string{
		`{"schema_version":"builder-roadmap-v1","schema_version":"builder-roadmap-v1","project_id":"project-a","focus_release_id":"r-01","epics":[{"id":"e-01","name":"Epic","description":"Strategic checkpoint","releases":[{"id":"r-01","name":"Release","description":"Selected grouping","todos":[{"id":"todo-a","path":"todos/active/work.md","kind":"executable"}]}]}]}`,
		`{"schema_version":"builder-roadmap-v1","project_id":"project-b","focus_release_id":"r-01","epics":[{"id":"e-01","name":"Epic","description":"Strategic checkpoint","releases":[{"id":"r-01","name":"Release","description":"Selected grouping","todos":[{"id":"todo-a","path":"todos/active/work.md","kind":"executable"}]}]}]}`,
	} {
		catalog := []byte(value)
		if _, ok := admittedRoadmapProjection("project-a", catalog, roadmapStatusForTest(catalog, roadmapLocalStage{ID: "todo-a", Stage: "Pending"}), map[string][]byte{"todos/active/work.md": []byte("# Work item\n")}, []string{"todos/active/work.md"}); ok {
			t.Fatalf("invalid or foreign catalog admitted: %s", value)
		}
	}
}

func TestAdmittedRoadmapProjectionRequiresAdmittedTODOBody(t *testing.T) {
	catalog := []byte(`{"schema_version":"builder-roadmap-v1","project_id":"project-a","focus_release_id":"r-01","epics":[{"id":"e-01","name":"Epic","description":"Strategic checkpoint","releases":[{"id":"r-01","name":"Release","description":"Selected grouping","todos":[{"id":"todo-a","path":"todos/active/work.md","kind":"executable"}]}]}]}`)
	status := roadmapStatusForTest(catalog, roadmapLocalStage{ID: "todo-a", Stage: "Pending"})
	files := map[string][]byte{"roadmap/catalog.json": catalog, "roadmap/todo-status.json": status, "todos/active/work.md": []byte("# Authored title\n")}
	admitted := []string{"roadmap/catalog.json", "roadmap/todo-status.json"}
	if _, ok := admittedRoadmapProjection("project-a", catalog, status, files, admitted); ok {
		t.Fatal("unlisted TODO body was exposed")
	}
	admitted = append(admitted, "todos/active/work.md")
	projection, ok := admittedRoadmapProjection("project-a", catalog, status, files, admitted)
	if !ok || projection.Epics[0].Releases[0].TODOs[0].Title != "Authored title" {
		t.Fatal("admitted source H1 was not used")
	}
}

func TestRegisteredPreviewUsesPrivateRegistryAndImmutableAdmittedSnapshot(t *testing.T) {
	if _, err := (&registeredPreview{root: string(filepath.Separator)}).catalog(); err == nil {
		t.Fatal("filesystem root was accepted as local Builder state")
	}
	root := t.TempDir()
	imagePaths := []string{
		"design/landing/assets/lowercase.avif",
		"design/landing/assets/uppercase.AVIF",
		"design/landing/assets/marker.PNG",
		"design/landing/assets/photo.JPG",
		"design/landing/assets/photo.JPEG",
		"design/landing/assets/photo.WEBP",
	}
	files := map[string][]byte{"design/landing/index.html": []byte("<main>trial</main>"), "design/landing/landing.css": []byte("main{color:teal}"), "design/landing/assets/marker.svg": []byte("<svg xmlns=\"http://www.w3.org/2000/svg\"></svg>"), "design/landing/assets/icons.ttf": []byte("fixture-font"), "project_mandate.md": []byte("# Trial mandate"), "private.txt": []byte("not admitted by artifact record")}
	for _, name := range imagePaths {
		files[name] = []byte("fixture-image")
	}
	artifactFiles := []string{"design/landing/index.html", "design/landing/landing.css", "design/landing/assets/marker.svg", "design/landing/assets/icons.ttf", "project_mandate.md"}
	artifactFiles = append(artifactFiles, imagePaths...)
	snapshot := registeredSnapshot{SchemaVersion: "builder-project-snapshot-v1", ProjectID: "trial-project", CompanyID: "trial-company", ObservedAt: "2026-10-07T00:00:00Z", Files: files, Artifacts: []registeredArtifact{{ID: "landing", Kind: "landing", Name: "Landing", State: "available", Entry: "design/landing/index.html", Files: artifactFiles}}, Observations: map[string]json.RawMessage{}}
	snapshotBytes, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(snapshotBytes)
	fingerprint := hex.EncodeToString(hash[:])
	if err := os.MkdirAll(filepath.Join(root, "snapshots"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "snapshots", fingerprint+".json"), snapshotBytes, 0o444); err != nil {
		t.Fatal(err)
	}
	registry := persistedRegistration{SchemaVersion: "builder-project-registry-v1", Companies: map[string]registeredCompany{"trial-company": {ID: "trial-company", Name: "Trial Company"}}, Projects: map[string]persistedProject{"trial-project": {registeredProject: registeredProject{ID: "trial-project", Name: "Trial Project", CompanyID: "trial-company", PreparationState: "ready", SnapshotFingerprint: fingerprint, Artifacts: snapshot.Artifacts}, Binding: &persistedBinding{WorkspaceRoot: "/tmp/trial", ProjectRoot: "project", FoundationRoot: "project/foundation"}}}, Preferences: map[string]persistedPreference{}}
	registryBytes, err := json.Marshal(registry)
	if err != nil {
		t.Fatal(err)
	}
	registryPath := filepath.Join(root, "registry.json")
	if err := os.WriteFile(registryPath, registryBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	consumer := testReaderConsumer(t, root)
	api := httptest.NewRecorder()
	consumer.apiHandler(api, httptest.NewRequest("GET", "/api/local-preview?company_id=trial-company&project_id=trial-project", nil))
	if api.Code != 200 || !strings.Contains(api.Body.String(), fingerprint) || strings.Contains(api.Body.String(), root) {
		t.Fatalf("API response=%d %s", api.Code, api.Body.String())
	}
	before, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(registryBytes) {
		t.Fatal("read-only API changed registry")
	}
	var currentDetail map[string]any
	if json.Unmarshal(api.Body.Bytes(), &currentDetail) != nil || currentDetail["project_sha"] != strings.Repeat("a", 40) || currentDetail["external_source_id"] != "design-system" || currentDetail["external_source_revision"] != "r-123" {
		t.Fatalf("live revision metadata missing from detail: %s", api.Body.String())
	}
	failedReader := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"state":"unavailable"}`, http.StatusServiceUnavailable)
	}))
	defer failedReader.Close()
	unavailable := &registeredPreview{root: root, readerURL: failedReader.URL, readerToken: strings.Repeat("t", 32), client: failedReader.Client()}
	failedDetail := httptest.NewRecorder()
	unavailable.apiHandler(failedDetail, httptest.NewRequest("GET", "/api/local-preview?company_id=trial-company&project_id=trial-project", nil))
	if failedDetail.Code != http.StatusServiceUnavailable || !strings.Contains(failedDetail.Body.String(), `"consumer_readiness":"unavailable"`) || strings.Contains(failedDetail.Body.String(), fingerprint) {
		t.Fatalf("reader failure exposed cached data as current: %d %s", failedDetail.Code, failedDetail.Body.String())
	}
	badRegistry := registry
	badRegistry.Projects = map[string]persistedProject{"wrong-key": registry.Projects["trial-project"]}
	badBytes, err := json.Marshal(badRegistry)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(registryPath, badBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	corrupt := httptest.NewRecorder()
	consumer.apiHandler(corrupt, httptest.NewRequest("GET", "/api/local-preview", nil))
	if corrupt.Code != 503 {
		t.Fatalf("mismatched registry map key was selectable: %d %s", corrupt.Code, corrupt.Body.String())
	}
	duplicateRegistry := []byte(`{"schema_version":"builder-project-registry-v1","schema_version":"builder-project-registry-v1","companies":{},"projects":{}}`)
	if err := os.WriteFile(registryPath, duplicateRegistry, 0o600); err != nil {
		t.Fatal(err)
	}
	duplicate := httptest.NewRecorder()
	consumer.apiHandler(duplicate, httptest.NewRequest("GET", "/api/local-preview", nil))
	if duplicate.Code != 503 {
		t.Fatalf("duplicate registry keys status=%d", duplicate.Code)
	}
	if err := os.WriteFile(registryPath, registryBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	page := httptest.NewRecorder()
	consumer.artifactHandler(page, httptest.NewRequest("GET", "/snapshot/trial-company/trial-project/"+fingerprint+"/design/landing/index.html", nil))
	if page.Code != 200 || page.Body.String() != "<main>trial</main>" || page.Header().Get("X-Builder-Snapshot-Fingerprint") != fingerprint {
		t.Fatalf("artifact response=%d %s", page.Code, page.Body.String())
	}
	asset := httptest.NewRecorder()
	consumer.artifactHandler(asset, httptest.NewRequest("GET", "/snapshot/trial-company/trial-project/"+fingerprint+"/design/landing/landing.css", nil))
	if asset.Code != 200 || asset.Header().Get("Content-Type") != "text/css; charset=utf-8" {
		t.Fatalf("relative asset response=%d type=%s", asset.Code, asset.Header().Get("Content-Type"))
	}
	font := httptest.NewRecorder()
	consumer.artifactHandler(font, httptest.NewRequest("GET", "/snapshot/trial-company/trial-project/"+fingerprint+"/design/landing/assets/icons.ttf", nil))
	if font.Code != 200 || font.Header().Get("Content-Type") != "font/ttf" {
		t.Fatalf("admitted font response=%d type=%s", font.Code, font.Header().Get("Content-Type"))
	}
	svg := httptest.NewRecorder()
	consumer.artifactHandler(svg, httptest.NewRequest("GET", "/snapshot/trial-company/trial-project/"+fingerprint+"/design/landing/assets/marker.svg", nil))
	if svg.Code != 200 || svg.Header().Get("Content-Type") != "image/svg+xml" || !strings.Contains(svg.Header().Get("Content-Security-Policy"), "sandbox") {
		t.Fatalf("SVG response=%d type=%s csp=%s", svg.Code, svg.Header().Get("Content-Type"), svg.Header().Get("Content-Security-Policy"))
	}
	for _, image := range []struct{ path, contentType string }{
		{"design/landing/assets/lowercase.avif", "image/avif"},
		{"design/landing/assets/uppercase.AVIF", "image/avif"},
		{"design/landing/assets/marker.PNG", "image/png"},
		{"design/landing/assets/photo.JPG", "image/jpeg"},
		{"design/landing/assets/photo.JPEG", "image/jpeg"},
		{"design/landing/assets/photo.WEBP", "image/webp"},
	} {
		response := httptest.NewRecorder()
		consumer.artifactHandler(response, httptest.NewRequest("GET", "/snapshot/trial-company/trial-project/"+fingerprint+"/"+image.path, nil))
		if response.Code != 200 || response.Header().Get("Content-Type") != image.contentType {
			t.Errorf("image %s response=%d content-type=%s; want %s", image.path, response.Code, response.Header().Get("Content-Type"), image.contentType)
		}
	}
	evidence := httptest.NewRecorder()
	consumer.artifactHandler(evidence, httptest.NewRequest("GET", "/snapshot/trial-company/trial-project/"+fingerprint+"/project_mandate.md", nil))
	if evidence.Code != 200 || !strings.Contains(evidence.Header().Get("Content-Security-Policy"), "frame-ancestors 'self'") || !strings.Contains(evidence.Header().Get("Content-Security-Policy"), "sandbox") {
		t.Fatalf("relative evidence response=%d csp=%s", evidence.Code, evidence.Header().Get("Content-Security-Policy"))
	}
	for _, request := range []struct{ name, method, path string }{
		{"traversal", "GET", "/snapshot/trial-company/trial-project/" + fingerprint + "/../../etc/passwd"},
		{"mismatched company", "GET", "/snapshot/other-company/trial-project/" + fingerprint + "/design/landing/index.html"},
		{"unadmitted file present in snapshot", "GET", "/snapshot/trial-company/trial-project/" + fingerprint + "/private.txt"},
		{"write method", "POST", "/snapshot/trial-company/trial-project/" + fingerprint + "/design/landing/index.html"},
	} {
		denied := httptest.NewRecorder()
		consumer.artifactHandler(denied, httptest.NewRequest(request.method, request.path, nil))
		if denied.Code != 404 {
			t.Fatalf("%s status=%d body=%s", request.name, denied.Code, denied.Body.String())
		}
	}
	for _, state := range []struct {
		name, readiness, fingerprint string
		wantStatus                   int
	}{
		{"superseded fingerprint", "ready", strings.Repeat("b", 64), http.StatusNotFound},
		{"retained immutable pointer remains addressable", "not_ready", fingerprint, http.StatusOK},
	} {
		t.Run(state.name, func(t *testing.T) {
			changed := registry
			changed.Projects = map[string]persistedProject{}
			project := registry.Projects["trial-project"]
			project.PreparationState = state.readiness
			project.SnapshotFingerprint = state.fingerprint
			changed.Projects["trial-project"] = project
			data, err := json.Marshal(changed)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(registryPath, data, 0o600); err != nil {
				t.Fatal(err)
			}
			response := httptest.NewRecorder()
			consumer.artifactHandler(response, httptest.NewRequest("GET", "/snapshot/trial-company/trial-project/"+fingerprint+"/design/landing/index.html", nil))
			if response.Code != state.wantStatus {
				t.Fatalf("snapshot pointer response=%d body=%s want=%d", response.Code, response.Body.String(), state.wantStatus)
			}
		})
	}
	if err := os.WriteFile(registryPath, registryBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	// A digest-mismatched immutable payload must fail closed.
	if err := os.Chmod(filepath.Join(root, "snapshots", fingerprint+".json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "snapshots", fingerprint+".json"), append(snapshotBytes, ' '), 0o600); err != nil {
		t.Fatal(err)
	}
	tampered := httptest.NewRecorder()
	consumer.artifactHandler(tampered, httptest.NewRequest("GET", "/snapshot/trial-company/trial-project/"+fingerprint+"/design/landing/index.html", nil))
	if tampered.Code != 404 {
		t.Fatalf("tampered snapshot status=%d", tampered.Code)
	}
	if err := os.WriteFile(filepath.Join(root, "snapshots", fingerprint+".json"), snapshotBytes, 0o444); err != nil {
		t.Fatal(err)
	}
	// A live detail may advance before the atomic cache pointer; assets remain denied until that pointer advances.
	snapshot.Files["design/landing/index.html"] = []byte("<main>published B</main>")
	bSnapshot, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	bDigest := sha256.Sum256(bSnapshot)
	bFingerprint := hex.EncodeToString(bDigest[:])
	if err := os.WriteFile(filepath.Join(root, "snapshots", bFingerprint+".json"), bSnapshot, 0o444); err != nil {
		t.Fatal(err)
	}
	readerCalls := 0
	liveReader := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		readerCalls++
		if r.URL.Path != "/internal/v1/projects/trial-project" {
			http.Error(w, "wrong route", 404)
			return
		}
		_ = json.NewEncoder(w).Encode(privateReaderResponse{ProjectID: "trial-project", ProjectName: "Trial Project", CompanyID: "trial-company", ProjectSHA: strings.Repeat("b", 40), ObservedAt: snapshot.ObservedAt, Fingerprint: bFingerprint, Artifacts: snapshot.Artifacts, Files: snapshot.Files, Assets: snapshot.Assets, Observations: snapshot.Observations})
	}))
	defer liveReader.Close()
	current := &registeredPreview{root: root, readerURL: liveReader.URL, readerToken: strings.Repeat("t", 32), client: liveReader.Client()}
	liveDetail := httptest.NewRecorder()
	current.apiHandler(liveDetail, httptest.NewRequest("GET", "/api/local-preview?company_id=trial-company&project_id=trial-project", nil))
	if liveDetail.Code != 200 || !strings.Contains(liveDetail.Body.String(), bFingerprint) || !strings.Contains(liveDetail.Body.String(), strings.Repeat("b", 40)) {
		t.Fatalf("reader's current B detail was not returned: %d %s", liveDetail.Code, liveDetail.Body.String())
	}
	deniedB := httptest.NewRecorder()
	current.artifactHandler(deniedB, httptest.NewRequest("GET", "/snapshot/trial-company/trial-project/"+bFingerprint+"/design/landing/index.html", nil))
	if deniedB.Code != 404 {
		t.Fatalf("asset B served before registry pointer publication: %d", deniedB.Code)
	}
	registry.Projects["trial-project"] = persistedProject{registeredProject: registeredProject{ID: "trial-project", Name: "Trial Project", CompanyID: "trial-company", PreparationState: "ready", SnapshotFingerprint: bFingerprint}, Binding: registry.Projects["trial-project"].Binding}
	registryBytes, err = json.Marshal(registry)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(registryPath, registryBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	servedB := httptest.NewRecorder()
	current.artifactHandler(servedB, httptest.NewRequest("GET", "/snapshot/trial-company/trial-project/"+bFingerprint+"/design/landing/index.html", nil))
	if servedB.Code != 200 || servedB.Body.String() != "<main>published B</main>" || readerCalls != 1 {
		t.Fatalf("published B asset mismatch or reader was queried per asset: code=%d body=%s reader_calls=%d", servedB.Code, servedB.Body.String(), readerCalls)
	}
	if err := os.Rename(filepath.Join(root, "snapshots"), filepath.Join(root, "snapshots-real")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "snapshots-real"), filepath.Join(root, "snapshots")); err != nil {
		t.Fatal(err)
	}
	symlinked := httptest.NewRecorder()
	consumer.artifactHandler(symlinked, httptest.NewRequest("GET", "/snapshot/trial-company/trial-project/"+fingerprint+"/design/landing/index.html", nil))
	if symlinked.Code != 404 {
		t.Fatalf("symlinked snapshot component status=%d", symlinked.Code)
	}
}

func TestDetailProjectsOnlyAdmittedPortfolioAndDocuments(t *testing.T) {
	snapshot := registeredSnapshot{
		ProjectID: "trial-project", CompanyID: "trial-company",
		Files: map[string][]byte{
			"project_portfolio.json":      []byte(`{"schema_version":"1","summary":"A bounded summary"}`),
			"project_portfolio/cover.png": []byte("png"),
			"modules/overview.md":         []byte("# Overview"),
			"private.md":                  []byte("not admitted"),
		},
		Artifacts: []registeredArtifact{
			{ID: "portfolio", Kind: "portfolio", State: "available", Files: []string{"project_portfolio.json", "project_portfolio/cover.png"}},
			{ID: "documents", Kind: "documents", State: "available", Files: []string{"modules/overview.md"}},
		},
	}
	body, err := json.Marshal(registeredProjectDetail(snapshot, strings.Repeat("a", 64)))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got["portfolio_summary"] != "A bounded summary" {
		t.Fatalf("summary missing: %s", body)
	}
	if got["portfolio_cover_url"] != "/snapshot/trial-company/trial-project/"+strings.Repeat("a", 64)+"/project_portfolio/cover.png" {
		t.Fatalf("cover URL not snapshot-bound: %v", got["portfolio_cover_url"])
	}
	docs, ok := got["documents"].([]any)
	if !ok || len(docs) != 1 || !strings.Contains(docs[0].(map[string]any)["url"].(string), "/modules/overview.md") {
		t.Fatalf("documents projection invalid: %v", got["documents"])
	}
	if strings.Contains(string(body), "private.md") {
		t.Fatalf("unadmitted file leaked into projection: %s", body)
	}
}

func TestProjectExperienceHasOneRoute(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("local app"), 0o444); err != nil {
		t.Fatal(err)
	}
	local := httptest.NewRecorder()
	localStaticHandler(root).ServeHTTP(local, httptest.NewRequest("GET", "/", nil))
	if local.Code != 200 || local.Body.String() != "local app" {
		t.Fatalf("root / = %d %q", local.Code, local.Body.String())
	}
	legacy := httptest.NewRecorder()
	localStaticHandler(root).ServeHTTP(legacy, httptest.NewRequest("GET", "/local", nil))
	if legacy.Code != 404 {
		t.Fatalf("retired /local route = %d", legacy.Code)
	}
	retired := httptest.NewRecorder()
	localStaticHandler(root).ServeHTTP(retired, httptest.NewRequest("GET", "/projects", nil))
	if retired.Code != 404 {
		t.Fatalf("retired /projects route = %d", retired.Code)
	}
	public := httptest.NewRecorder()
	staticHandler(root).ServeHTTP(public, httptest.NewRequest("GET", "/projects", nil))
	if public.Code != 404 {
		t.Fatalf("public /projects must remain unavailable, got %d", public.Code)
	}
}

func TestRegisteredPreviewRejectsPrivatePrototypeRelatedFromSnapshot(t *testing.T) {
	root := t.TempDir()
	secret := []byte("PRIVATE_SHOULD_NOT_BE_SERVED")
	manifest := []byte(`{"schema_version":"1","related":[{"kind":"documentation","path":".env"}]}`)
	snapshot := registeredSnapshot{SchemaVersion: "builder-project-snapshot-v1", ProjectID: "trial-project", CompanyID: "trial-company", ObservedAt: "2026-10-07T00:00:00Z", Files: map[string][]byte{"prototypes/demo/prototype.json": manifest, "prototypes/demo/index.html": []byte("<main>fixture</main>"), ".env": secret}, Artifacts: []registeredArtifact{{ID: "demo", Kind: "prototype", Name: "Disposable Prototype", State: "available", Entry: "prototypes/demo/index.html", Files: []string{"prototypes/demo/prototype.json", "prototypes/demo/index.html", ".env"}}}, Observations: map[string]json.RawMessage{}}
	snapshotBytes, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(snapshotBytes)
	fingerprint := hex.EncodeToString(digest[:])
	if err := os.MkdirAll(filepath.Join(root, "snapshots"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "snapshots", fingerprint+".json"), snapshotBytes, 0o444); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	(&registeredPreview{root: root}).artifactHandler(response, httptest.NewRequest("GET", "/snapshot/trial-company/trial-project/"+fingerprint+"/.env", nil))
	if response.Code != 404 || strings.Contains(response.Body.String(), string(secret)) {
		t.Fatalf("private Related bytes were served: status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestRegisteredPreviewHidesAvailablePrototypeWithLegacyStatusButKeepsOtherArtifacts(t *testing.T) {
	root := t.TempDir()
	manifest := []byte(`{"schema_version":"2","id":"demo","entry_point":"index.html","related":[]}`)
	snapshot := registeredSnapshot{
		SchemaVersion: "builder-project-snapshot-v1",
		ProjectID:     "trial-project",
		CompanyID:     "trial-company",
		ObservedAt:    "2026-10-07T00:00:00Z",
		Files: map[string][]byte{
			"prototypes/demo/prototype.json": manifest,
			"prototypes/demo/index.html":     []byte("<main>legacy prototype</main>"),
			"design/landing/index.html":      []byte("<main>Landing</main>"),
		},
		Artifacts:    []registeredArtifact{{ID: "demo", Kind: "prototype", Name: "Demo", State: "available", Entry: "prototypes/demo/index.html", Files: []string{"prototypes/demo/prototype.json", "prototypes/demo/index.html"}}, {ID: "landing", Kind: "landing", Name: "Landing", State: "available", Entry: "design/landing/index.html", Files: []string{"design/landing/index.html"}}},
		Observations: map[string]json.RawMessage{"prototype_status": json.RawMessage(`{"schema_version":"2","project_id":"trial-project","target":"prototypes","outcome":"go","items":[{"id":"demo","screens":[{"id":"start","path":"index.html"}]}]}`)},
	}
	snapshotBytes, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(snapshotBytes)
	fingerprint := hex.EncodeToString(hash[:])
	if err := os.MkdirAll(filepath.Join(root, "snapshots"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "snapshots", fingerprint+".json"), snapshotBytes, 0o444); err != nil {
		t.Fatal(err)
	}
	registry := persistedRegistration{SchemaVersion: "builder-project-registry-v1", Companies: map[string]registeredCompany{"trial-company": {ID: "trial-company", Name: "Trial Company"}}, Projects: map[string]persistedProject{"trial-project": {registeredProject: registeredProject{ID: "trial-project", Name: "Trial Project", CompanyID: "trial-company", PreparationState: "ready", SnapshotFingerprint: fingerprint, Artifacts: snapshot.Artifacts}, Binding: &persistedBinding{WorkspaceRoot: "/tmp/trial", ProjectRoot: "project", FoundationRoot: "project/foundation"}}}, Preferences: map[string]persistedPreference{}}
	registryBytes, err := json.Marshal(registry)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "registry.json"), registryBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	response := httptest.NewRecorder()
	testReaderConsumer(t, root).apiHandler(response, httptest.NewRequest("GET", "/api/local-preview?company_id=trial-company&project_id=trial-project", nil))
	var body struct {
		ConsumerReadiness   string               `json:"consumer_readiness"`
		SnapshotFingerprint string               `json:"snapshot_fingerprint"`
		Artifacts           []registeredArtifact `json:"artifacts"`
	}
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &body) != nil || body.ConsumerReadiness != "ready" || body.SnapshotFingerprint != fingerprint {
		t.Fatalf("legacy Prototype blocked unrelated snapshot content: status=%d body=%s", response.Code, response.Body.String())
	}
	var prototype registeredArtifact
	for _, artifact := range body.Artifacts {
		if artifact.Kind == "prototype" {
			prototype = artifact
		}
	}
	if prototype.State != "invalid" || !strings.Contains(prototype.Diagnostic, "schema v2") || !strings.Contains(prototype.Diagnostic, "TEACH") || !strings.Contains(prototype.Diagnostic, "fresh Prototype status") {
		t.Fatalf("legacy Prototype did not receive migration TEACH: %#v", prototype)
	}
	raw := httptest.NewRecorder()
	testReaderConsumer(t, root).artifactHandler(raw, httptest.NewRequest("GET", "/snapshot/trial-company/trial-project/"+fingerprint+"/prototypes/demo/index.html", nil))
	if raw.Code != 404 {
		t.Fatalf("unsupported Prototype HTML remained available: status=%d body=%s", raw.Code, raw.Body.String())
	}
	landing := httptest.NewRecorder()
	testReaderConsumer(t, root).artifactHandler(landing, httptest.NewRequest("GET", "/snapshot/trial-company/trial-project/"+fingerprint+"/design/landing/index.html", nil))
	if landing.Code != 200 {
		t.Fatalf("unrelated Landing was blocked: status=%d body=%s", landing.Code, landing.Body.String())
	}

	// A v2 observation is harmless when this snapshot has no available Prototype.
	snapshot.Artifacts = []registeredArtifact{{ID: "landing", Kind: "landing", Name: "Landing", State: "available", Entry: "design/landing/index.html", Files: []string{"design/landing/index.html"}}}
	snapshotBytes, err = json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	hash = sha256.Sum256(snapshotBytes)
	noPrototypeFingerprint := hex.EncodeToString(hash[:])
	if err := os.WriteFile(filepath.Join(root, "snapshots", noPrototypeFingerprint+".json"), snapshotBytes, 0o444); err != nil {
		t.Fatal(err)
	}
	registry.Projects["trial-project"] = persistedProject{registeredProject: registeredProject{ID: "trial-project", Name: "Trial Project", CompanyID: "trial-company", PreparationState: "ready", SnapshotFingerprint: noPrototypeFingerprint, Artifacts: snapshot.Artifacts}, Binding: &persistedBinding{WorkspaceRoot: "/tmp/trial", ProjectRoot: "project", FoundationRoot: "project/foundation"}}
	registryBytes, err = json.Marshal(registry)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "registry.json"), registryBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	withoutPrototype := httptest.NewRecorder()
	testReaderConsumer(t, root).apiHandler(withoutPrototype, httptest.NewRequest("GET", "/api/local-preview?company_id=trial-company&project_id=trial-project", nil))
	if withoutPrototype.Code != 200 || !strings.Contains(withoutPrototype.Body.String(), `"consumer_readiness":"ready"`) || strings.Contains(withoutPrototype.Body.String(), `"kind":"prototype"`) {
		t.Fatalf("v2 observation without an available Prototype blocked unrelated views or exposed Prototype: status=%d body=%s", withoutPrototype.Code, withoutPrototype.Body.String())
	}
}

func TestPrototypeStatusRequiresViewerArrays(t *testing.T) {
	base := registeredSnapshot{
		ProjectID: "trial-project",
		Files: map[string][]byte{
			"prototypes/demo/prototype.json": []byte(`{"schema_version":"3","id":"demo","scenarios":[]}`),
		},
		Artifacts: []registeredArtifact{{ID: "demo", Kind: "prototype", State: "available", Entry: "prototypes/demo/start.html"}},
	}
	observation := `{"schema_version":"3","project_id":"trial-project","target":"prototypes","authority_scope":"local_structure_only","outcome":"go","items":[{"id":"demo","root":"prototypes/demo","entry_point":"start.html","screens":[{"id":"start","path":"start.html","default_state_id":"default","states":[{"id":"default","approved_references":[]}]}],"transitions":[],"scenarios":[]}]}`
	base.Observations = map[string]json.RawMessage{"prototype_status": json.RawMessage(observation)}
	if !validPrototypeStatusObservation(base) {
		t.Fatal("complete v3 status was rejected")
	}
	for _, tc := range []struct {
		name string
		old  string
		new  string
	}{
		{"missing transitions", `,"transitions":[]`, ""},
		{"missing approved references", `,"approved_references":[]`, ""},
		{"missing scenarios", `,"scenarios":[]`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := base
			changed.Observations = map[string]json.RawMessage{"prototype_status": json.RawMessage(strings.Replace(observation, tc.old, tc.new, 1))}
			if validPrototypeStatusObservation(changed) {
				t.Fatal("viewer-required array was accepted when absent")
			}
		})
	}
	manifestMismatch := base
	manifestMismatch.Files = map[string][]byte{"prototypes/demo/prototype.json": []byte(`{"schema_version":"3","id":"demo","scenarios":[{"id":"other","name":"Other","steps":[{"screen_id":"start","state_id":"default"}]}]}`)}
	if validPrototypeStatusObservation(manifestMismatch) {
		t.Fatal("saved scenario projection mismatch was accepted")
	}
}

func TestRegisteredPreviewServesMatchingNonemptyScenarioProjectionAndRejectsMalformedRows(t *testing.T) {
	serve := func(t *testing.T, variant string) (*httptest.ResponseRecorder, *httptest.ResponseRecorder) {
		t.Helper()
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, "snapshots"), 0o700); err != nil {
			t.Fatal(err)
		}
		scenario := `[{"id":"review","name":"Review path","steps":[{"screen_id":"start","state_id":"default"},{"screen_id":"start","state_id":"default"}]}]`
		statusScenario := scenario
		if variant == "mismatch" {
			scenario = `[{"id":"other","name":"Different path","steps":[{"screen_id":"start","state_id":"default"}]}]`
		} else if variant == "malformed" {
			scenario = `[{"id":"review","name":"Review path","steps":[]}]`
			statusScenario = scenario
		}
		manifest := []byte(`{"schema_version":"3","id":"demo","related":[],"scenarios":` + scenario + `}`)
		entry := []byte("<main>Scenario entry</main>")
		status := json.RawMessage(`{"schema_version":"3","project_id":"trial-project","target":"prototypes","authority_scope":"local_structure_only","outcome":"go","items":[{"id":"demo","root":"prototypes/demo","entry_point":"start.html","screens":[{"id":"start","path":"start.html","default_state_id":"default","states":[{"id":"default","approved_references":[]}]}],"transitions":[],"scenarios":` + statusScenario + `}]}`)
		snapshot := registeredSnapshot{
			SchemaVersion: "builder-project-snapshot-v1", ProjectID: "trial-project", CompanyID: "trial-company", ObservedAt: "2026-10-08T00:00:00Z",
			Files:        map[string][]byte{"prototypes/demo/prototype.json": manifest, "prototypes/demo/start.html": entry},
			Artifacts:    []registeredArtifact{{ID: "demo", Kind: "prototype", Name: "Demo", State: "available", Entry: "prototypes/demo/start.html", Files: []string{"prototypes/demo/prototype.json", "prototypes/demo/start.html"}}},
			Observations: map[string]json.RawMessage{"prototype_status": status},
		}
		snapshotBytes, err := json.Marshal(snapshot)
		if err != nil {
			t.Fatal(err)
		}
		hash := sha256.Sum256(snapshotBytes)
		fingerprint := hex.EncodeToString(hash[:])
		if err := os.WriteFile(filepath.Join(root, "snapshots", fingerprint+".json"), snapshotBytes, 0o444); err != nil {
			t.Fatal(err)
		}
		registry := persistedRegistration{
			SchemaVersion: "builder-project-registry-v1",
			Companies:     map[string]registeredCompany{"trial-company": {ID: "trial-company", Name: "Trial Company"}},
			Projects:      map[string]persistedProject{"trial-project": {registeredProject: registeredProject{ID: "trial-project", Name: "Trial Project", CompanyID: "trial-company", PreparationState: "ready", SnapshotFingerprint: fingerprint, Artifacts: snapshot.Artifacts}, Binding: &persistedBinding{WorkspaceRoot: "/tmp/trial", ProjectRoot: "project", FoundationRoot: "project/foundation"}}},
			Preferences:   map[string]persistedPreference{},
		}
		registryBytes, err := json.Marshal(registry)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "registry.json"), registryBytes, 0o600); err != nil {
			t.Fatal(err)
		}
		consumer := testReaderConsumer(t, root)
		api := httptest.NewRecorder()
		consumer.apiHandler(api, httptest.NewRequest("GET", "/api/local-preview?company_id=trial-company&project_id=trial-project", nil))
		raw := httptest.NewRecorder()
		consumer.artifactHandler(raw, httptest.NewRequest("GET", "/snapshot/trial-company/trial-project/"+fingerprint+"/prototypes/demo/start.html", nil))
		return api, raw
	}
	t.Run("matching ordered repeated pairs", func(t *testing.T) {
		api, raw := serve(t, "matching")
		var apiBody struct {
			SnapshotObservations map[string]json.RawMessage `json:"snapshot_observations"`
		}
		var statusBody struct {
			Items []struct {
				Scenarios []savedScenario `json:"scenarios"`
			} `json:"items"`
		}
		if api.Code != 200 || !strings.Contains(api.Body.String(), `"consumer_readiness":"ready"`) || json.Unmarshal(api.Body.Bytes(), &apiBody) != nil || json.Unmarshal(apiBody.SnapshotObservations["prototype_status"], &statusBody) != nil || len(statusBody.Items) != 1 || !sameSavedScenarios(statusBody.Items[0].Scenarios, []savedScenario{{ID: "review", Name: "Review path", Steps: []savedScenarioStep{{ScreenID: "start", StateID: "default"}, {ScreenID: "start", StateID: "default"}}}}) || raw.Code != 200 || raw.Body.String() != "<main>Scenario entry</main>" {
			t.Fatalf("matching scenario snapshot was not usable: API=%d %s raw=%d %s", api.Code, api.Body.String(), raw.Code, raw.Body.String())
		}
	})
	t.Run("scenario mismatch fails closed", func(t *testing.T) {
		api, raw := serve(t, "mismatch")
		if api.Code != 200 || !strings.Contains(api.Body.String(), `"state":"invalid"`) || !strings.Contains(api.Body.String(), "projection does not match") || raw.Code != 404 {
			t.Fatalf("scenario mismatch was not rejected: API=%d %s raw=%d %s", api.Code, api.Body.String(), raw.Code, raw.Body.String())
		}
	})
	t.Run("matching but empty steps fail closed", func(t *testing.T) {
		api, raw := serve(t, "malformed")
		if api.Code != 200 || !strings.Contains(api.Body.String(), `"state":"invalid"`) || raw.Code != 404 {
			t.Fatalf("malformed matching scenario was not rejected: API=%d %s raw=%d %s", api.Code, api.Body.String(), raw.Code, raw.Body.String())
		}
	})
}

func TestPrototypeHTMLAllowsOnlyAdmittedSameOriginFonts(t *testing.T) {
	w := httptest.NewRecorder()
	registeredArtifactHeaders(w, "prototype")
	policy := w.Header().Get("Content-Security-Policy")
	if !strings.Contains(policy, "font-src 'self'") || !strings.Contains(policy, "default-src 'none'") || !strings.Contains(policy, "sandbox allow-scripts") {
		t.Fatalf("prototype font policy does not preserve isolation: %s", policy)
	}
}

func TestRegisteredPreviewLazilyServesVerifiedV2PNG(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "snapshots"), 0o700); err != nil {
		t.Fatal(err)
	}
	png := []byte("authored-png-bytes")
	sum := sha256.Sum256(png)
	digest := hex.EncodeToString(sum[:])
	assetName := "design/landing/assets/logo.png"
	snapshot := registeredSnapshot{
		SchemaVersion: "builder-project-snapshot-v2", ProjectID: "trial-project", CompanyID: "trial-company",
		Files:        map[string][]byte{"design/landing/index.html": []byte("<img src=\"assets/logo.png\">")},
		Assets:       map[string]registeredAssetReference{assetName: {SHA256: digest, Size: int64(len(png))}},
		Artifacts:    []registeredArtifact{{ID: "landing", Kind: "landing", State: "available", Entry: "design/landing/index.html", Files: []string{"design/landing/index.html", assetName}}},
		Observations: map[string]json.RawMessage{},
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	fingerprintHash := sha256.Sum256(data)
	fingerprint := hex.EncodeToString(fingerprintHash[:])
	if err := os.WriteFile(filepath.Join(root, "snapshots", fingerprint+".json"), data, 0o444); err != nil {
		t.Fatal(err)
	}
	registry := persistedRegistration{
		SchemaVersion: "builder-project-registry-v1",
		Companies:     map[string]registeredCompany{"trial-company": {ID: "trial-company", Name: "Trial Company"}},
		Projects:      map[string]persistedProject{"trial-project": {registeredProject: registeredProject{ID: "trial-project", Name: "Trial Project", CompanyID: "trial-company", PreparationState: "ready", SnapshotFingerprint: fingerprint, Artifacts: snapshot.Artifacts}, Binding: &persistedBinding{WorkspaceRoot: "/tmp/trial", ProjectRoot: "project", FoundationRoot: "project/foundation"}}},
		Preferences:   map[string]persistedPreference{},
	}
	registryData, err := json.Marshal(registry)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "registry.json"), registryData, 0o600); err != nil {
		t.Fatal(err)
	}
	consumer := testReaderConsumer(t, root)
	if _, _, err := consumer.snapshot("trial-project", "trial-company", fingerprint); err != nil {
		t.Fatalf("manifest read eagerly required the blob: %v", err)
	}
	requestPath := "/snapshot/trial-company/trial-project/" + fingerprint + "/" + assetName
	missing := httptest.NewRecorder()
	consumer.artifactHandler(missing, httptest.NewRequest("GET", requestPath, nil))
	if missing.Code != 404 {
		t.Fatalf("missing blob returned %d %q", missing.Code, missing.Body.String())
	}
	if err := os.MkdirAll(filepath.Join(root, "assets"), 0o700); err != nil {
		t.Fatal(err)
	}
	blobPath := filepath.Join(root, "assets", digest)
	if err := os.WriteFile(blobPath, png, 0o444); err != nil {
		t.Fatal(err)
	}
	served := httptest.NewRecorder()
	consumer.artifactHandler(served, httptest.NewRequest("GET", requestPath, nil))
	if served.Code != 200 || served.Header().Get("Content-Type") != "image/png" || served.Body.String() != string(png) {
		t.Fatalf("PNG response=%d type=%s bytes=%q", served.Code, served.Header().Get("Content-Type"), served.Body.Bytes())
	}
	for _, path := range []string{
		"/snapshot/other-company/trial-project/" + fingerprint + "/" + assetName,
		"/snapshot/trial-company/other-project/" + fingerprint + "/" + assetName,
		"/snapshot/trial-company/trial-project/" + strings.Repeat("0", 64) + "/" + assetName,
		"/snapshot/trial-company/trial-project/" + fingerprint + "/design/landing/assets/unlisted.png",
		"/snapshot/trial-company/trial-project/" + fingerprint + "/design/landing/../landing/assets/logo.png",
	} {
		response := httptest.NewRecorder()
		consumer.artifactHandler(response, httptest.NewRequest("GET", path, nil))
		if response.Code != 404 || strings.Contains(response.Body.String(), string(png)) {
			t.Fatalf("v2 identity/path bypass at %q: %d %q", path, response.Code, response.Body.String())
		}
	}
	if err := os.Chmod(blobPath, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(blobPath, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	tampered := httptest.NewRecorder()
	consumer.artifactHandler(tampered, httptest.NewRequest("GET", requestPath, nil))
	if tampered.Code != 404 || strings.Contains(tampered.Body.String(), string(png)) {
		t.Fatalf("tampered blob was served: %d %q", tampered.Code, tampered.Body.String())
	}
}

func TestRegisteredPreviewRejectsInvalidV2AssetReferences(t *testing.T) {
	const assetName = "design/landing/assets/logo.png"
	png := []byte("authored-png-bytes")
	sum := sha256.Sum256(png)
	valid := registeredSnapshot{
		SchemaVersion: "builder-project-snapshot-v2", ProjectID: "trial-project", CompanyID: "trial-company",
		Files:        map[string][]byte{"design/landing/index.html": []byte("landing")},
		Assets:       map[string]registeredAssetReference{assetName: {SHA256: hex.EncodeToString(sum[:]), Size: int64(len(png))}},
		Artifacts:    []registeredArtifact{{ID: "landing", Kind: "landing", State: "available", Entry: "design/landing/index.html", Files: []string{"design/landing/index.html", assetName}}},
		Observations: map[string]json.RawMessage{},
	}
	for name, change := range map[string]func(*registeredSnapshot){
		"inline-and-blob-collision": func(s *registeredSnapshot) { s.Files[assetName] = png },
		"invalid-digest": func(s *registeredSnapshot) {
			s.Assets[assetName] = registeredAssetReference{SHA256: "wrong", Size: int64(len(png))}
		},
		"invalid-size": func(s *registeredSnapshot) {
			s.Assets[assetName] = registeredAssetReference{SHA256: hex.EncodeToString(sum[:]), Size: 2<<20 + 1}
		},
		"aggregate-over-limit": func(s *registeredSnapshot) {
			for i := 0; i < 17; i++ {
				name := "design/landing/assets/" + strings.Repeat("x", i+1) + ".png"
				s.Assets[name] = registeredAssetReference{SHA256: hex.EncodeToString(sum[:]), Size: 2 << 20}
				s.Artifacts[0].Files = append(s.Artifacts[0].Files, name)
			}
		},
		"unadmitted-asset": func(s *registeredSnapshot) { s.Artifacts[0].Files = []string{"design/landing/index.html"} },
		"unsafe-asset-path": func(s *registeredSnapshot) {
			s.Assets["../logo.png"] = s.Assets[assetName]
			delete(s.Assets, assetName)
		},
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.MkdirAll(filepath.Join(root, "snapshots"), 0o700); err != nil {
				t.Fatal(err)
			}
			snapshot := valid
			snapshot.Files = map[string][]byte{"design/landing/index.html": []byte("landing")}
			snapshot.Assets = map[string]registeredAssetReference{assetName: valid.Assets[assetName]}
			snapshot.Artifacts = []registeredArtifact{{ID: "landing", Kind: "landing", State: "available", Entry: "design/landing/index.html", Files: []string{"design/landing/index.html", assetName}}}
			change(&snapshot)
			data, err := json.Marshal(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256(data)
			fingerprint := hex.EncodeToString(digest[:])
			if err := os.WriteFile(filepath.Join(root, "snapshots", fingerprint+".json"), data, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, _, err := (&registeredPreview{root: root}).snapshot("trial-project", "trial-company", fingerprint); err == nil {
				t.Fatal("invalid v2 asset reference was accepted")
			}
		})
	}
}

func TestV2AssetReferencesFeedOverviewAndPrototypeProjection(t *testing.T) {
	image := registeredAssetReference{SHA256: strings.Repeat("a", 64), Size: 42}
	overview := overviewSource{SchemaVersion: "1", DesktopAlt: "desktop", MobileAlt: "mobile", Sections: map[string]overviewSection{
		"problem": {}, "proposal": {}, "impact": {}, "audience": {},
	}}
	data, err := json.Marshal(overview)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := registeredSnapshot{
		SchemaVersion: "builder-project-snapshot-v2", ProjectID: "trial-project", CompanyID: "trial-company",
		Files: map[string][]byte{"project_overview.json": data, "prototypes/example/index.html": []byte("prototype"), "prototypes/example/prototype.json": []byte(`{"related":[]}`)},
		Assets: map[string]registeredAssetReference{
			"project_overview/desktop.png": image, "project_overview/mobile.png": image,
			"prototypes/example/screen.png": image,
		},
		Artifacts: []registeredArtifact{
			{ID: "overview", Kind: "overview", State: "available", Entry: "project_overview.json", Files: []string{"project_overview.json", "project_overview/desktop.png", "project_overview/mobile.png"}},
			{ID: "example", Kind: "prototype", State: "available", Entry: "prototypes/example/index.html", Files: []string{"prototypes/example/index.html", "prototypes/example/prototype.json", "prototypes/example/screen.png"}},
		},
	}
	detail := registeredProjectDetail(snapshot, "fingerprint")
	projected, ok := detail["overview"].(overviewSource)
	if !ok || projected.Media["desktop"] == "" || projected.Media["mobile"] == "" {
		t.Fatalf("v2 Overview image references were not projected: %#v", detail["overview"])
	}
	if !validPrototypeRelatedFiles(snapshot, snapshot.Artifacts[1]) {
		t.Fatal("v2 Prototype image reference was not admitted")
	}
}
