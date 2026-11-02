package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testBundle() *previewBundle {
	return &previewBundle{
		commit: approvedCommit, fingerprint: approvedFingerprint, localState: "matching",
		files: map[string][]byte{"project_landing.manifest.json": []byte(`{"landing":{"document_version":"1.4"}}`), "project_landing.md": []byte("# Landing\n")},
		byID:  map[string]approvedFile{"landing": {ID: "landing", Path: "project_landing.md", Digest: "unused"}},
	}
}

func TestPreviewReturnsPinnedCatalogAndSeparatedVerification(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/api/preview", nil)
	response := httptest.NewRecorder()
	testBundle().apiHandler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	var body landingResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Commit != approvedCommit || body.RemoteVerification != "unverifiable" || body.LocalIntegrity != "matching" || body.AccessMode != "public_preview" {
		t.Fatalf("unexpected preview trust fields: %+v", body)
	}
	if len(body.Documents) != 7 || body.Documents[0].ID != "landing" || body.Documents[6].ID != "genesis" {
		t.Fatalf("unexpected catalog order: %+v", body.Documents)
	}
}

func TestDocumentEndpointUsesExactIDAndNeverAcceptsPath(t *testing.T) {
	bundle := testBundle()
	handler := bundle.documentHandler()
	for _, path := range []string{"/api/preview/documents/unknown", "/api/preview/documents/../project_landing.md", "/api/preview/documents/%2e%2e%2fproject_landing.md"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusNotFound {
			t.Errorf("%s status = %d", path, response.Code)
		}
		if response.Body.String() == "" || response.Body.String()[0] != '{' {
			t.Errorf("%s did not return a bounded error", path)
		}
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/preview/documents/landing", nil))
	if response.Code != http.StatusOK || !contains(response.Body.String(), "# Landing") {
		t.Fatalf("approved ID response = %d %s", response.Code, response.Body.String())
	}
}

func TestServeMuxCannotTurnTraversalOrWriteRoutesIntoDocuments(t *testing.T) {
	mux := http.NewServeMux()
	mux.Handle("/api/preview", testBundle().apiHandler())
	mux.Handle("/api/preview/documents/", testBundle().documentHandler())
	mux.Handle("/", staticHandler(t.TempDir()))
	for _, path := range []string{"/api/preview/documents/../project_landing.md", "/api/preview/documents/%2e%2e%2fproject_landing.md", "/api/unknown"} {
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusNotFound && response.Code != http.StatusMovedPermanently {
			t.Errorf("%s status = %d", path, response.Code)
		}
		if contains(response.Body.String(), "# Landing") {
			t.Errorf("%s disclosed a source document", path)
		}
	}
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/preview/documents/landing", nil))
	if response.Code != http.StatusNotFound || contains(response.Body.String(), "# Landing") {
		t.Fatalf("write method reached document content: %d", response.Code)
	}
}

func TestUnknownAPIAndMethodsNeverFallBackToHTML(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("<main>viewer app</main>"), 0o444); err != nil {
		t.Fatal(err)
	}
	handler := staticHandler(root)
	for _, request := range []*http.Request{httptest.NewRequest(http.MethodGet, "/api/unknown", nil), httptest.NewRequest(http.MethodPost, "/", nil), httptest.NewRequest(http.MethodGet, "/repository/project_landing.md", nil), httptest.NewRequest(http.MethodGet, "/foundation_documentation", nil)} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusNotFound {
			t.Errorf("%s %s status = %d", request.Method, request.URL.Path, response.Code)
		}
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/documents/not-approved", nil))
	if response.Code != http.StatusOK || response.Body.String() != "<main>viewer app</main>" {
		t.Fatalf("safe document route did not reach UI fallback: %d %s", response.Code, response.Body.String())
	}
}

func TestListenerDefaultsRejectExternalBind(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "::1", "localhost"} {
		if !isLoopback(host) {
			t.Errorf("%q must be loopback", host)
		}
	}
	for _, host := range []string{"0.0.0.0", "192.0.2.1", ""} {
		if isLoopback(host) {
			t.Errorf("%q must not be loopback", host)
		}
	}
}

func TestLocalViewerRejectsNonLoopbackHost(t *testing.T) {
	handler := localHostOnly(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	for _, host := range []string{"127.0.0.1:8089", "localhost:8089", "[::1]:8089"} {
		request := httptest.NewRequest(http.MethodGet, "/api/local-preview", nil)
		request.Host = host
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusNoContent {
			t.Errorf("local host %q rejected with %d", host, response.Code)
		}
	}
	for _, host := range []string{"attacker.invalid:8089", "localhost.attacker.invalid:8089", "", "[::1"} {
		request := httptest.NewRequest(http.MethodGet, "/snapshot/company/project/fingerprint/file", nil)
		request.Host = host
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusForbidden {
			t.Errorf("nonlocal host %q reached local viewer: %d", host, response.Code)
		}
	}
}

func TestPublishedViewerAcceptsOnlyConfiguredHost(t *testing.T) {
	handler := previewHostOnly(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }), "builder.belluga.online")
	for _, host := range []string{"builder.belluga.online", "builder.belluga.online:443", "BUILDER.BELLUGA.ONLINE"} {
		request := httptest.NewRequest(http.MethodGet, "/api/local-preview", nil)
		request.Host = host
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusNoContent {
			t.Errorf("published host %q rejected with %d", host, response.Code)
		}
	}
	for _, host := range []string{"attacker.invalid", "builder.belluga.online.attacker.invalid", "", "[::1"} {
		request := httptest.NewRequest(http.MethodGet, "/api/local-preview", nil)
		request.Host = host
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusForbidden {
			t.Errorf("unconfigured host %q accepted with %d", host, response.Code)
		}
	}
}

func TestPublishedViewerRootOpensProjectDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("project directory"), 0o444); err != nil {
		t.Fatal(err)
	}
	handler := registeredHandler(t.TempDir(), root, "builder.belluga.online")
	for _, tc := range []struct {
		path, host string
		status     int
	}{
		{path: "/", host: "builder.belluga.online", status: http.StatusOK},
		{path: "/local", host: "builder.belluga.online", status: http.StatusNotFound},
		{path: "/", host: "untrusted.example", status: http.StatusForbidden},
	} {
		request := httptest.NewRequest(http.MethodGet, tc.path, nil)
		request.Host = tc.host
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != tc.status {
			t.Errorf("published %s on %s = %d, want %d", tc.path, tc.host, response.Code, tc.status)
		}
		if tc.status == http.StatusOK && response.Body.String() != "project directory" {
			t.Errorf("published root body = %q", response.Body.String())
		}
	}
}

func TestRegisteredHandlerRoutesExactTODOReadsToConsumer(t *testing.T) {
	handler := registeredHandler(t.TempDir(), t.TempDir(), "")
	request := httptest.NewRequest(http.MethodGet, "/api/local-todo-source", nil)
	request.Host = "127.0.0.1"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "todo_source_request_invalid") {
		t.Fatalf("exact TODO route did not reach the registered consumer: status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestBundleRejectsMissingOrExtraFiles(t *testing.T) {
	root := filepath.Join(t.TempDir(), "bundle")
	foundation := filepath.Join(root, "foundation")
	if err := os.MkdirAll(foundation, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := rejectExtraFiles(foundation); err == nil {
		t.Fatal("missing files must fail closed")
	}
	for _, file := range approvedFiles {
		path := filepath.Join(foundation, filepath.FromSlash(file.Path))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("fixture"), 0o444); err != nil {
			t.Fatal(err)
		}
	}
	if err := rejectExtraFiles(foundation); err != nil {
		t.Fatalf("exact inventory rejected: %v", err)
	}
	if err := os.WriteFile(filepath.Join(foundation, "private.md"), []byte("private"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := rejectExtraFiles(foundation); err == nil {
		t.Fatal("extra file must fail closed")
	}
}

func TestBundleFileSizeLimit(t *testing.T) {
	if maxDocumentBytes != 1<<20 {
		t.Fatalf("unexpected catalog size cap: %d", maxDocumentBytes)
	}
	root := t.TempDir()
	path := filepath.Join(root, "foundation", filepath.FromSlash(approvedFiles[0].Path))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, maxDocumentBytes+1), 0o400); err != nil {
		t.Fatal(err)
	}
	if _, err := readBoundedFile(path, maxDocumentBytes); err == nil {
		t.Fatal("oversized document must be rejected before loading")
	}
}

func TestExporterRejectsUnapprovedRevision(t *testing.T) {
	if err := exportCommit(t.TempDir(), "deadbeef", filepath.Join(t.TempDir(), "bundle")); err == nil {
		t.Fatal("an unapproved commit must not be exportable")
	}
}

func TestInvalidManifestInputFailsClosed(t *testing.T) {
	b := testBundle()
	b.files["project_landing.manifest.json"] = []byte(`{"landing":{"document_version":"1.4"},"source_snapshot":{"inputs":[{"path":"../../secret","content_digest":"sha256:0000000000000000000000000000000000000000000000000000000000000000"}]}}`)
	if err := b.evaluateManifest(); err == nil {
		t.Fatal("manifest path outside allowlist must fail")
	}
}

func TestManifestSourceDriftRemainsReadableButStale(t *testing.T) {
	inputs := make([]map[string]string, 0, 6)
	approvedSources := make([]approvedFile, 0, 6)
	b := &previewBundle{files: make(map[string][]byte), localState: "matching"}
	for _, file := range approvedFiles {
		if file.ID == "" || file.ID == "landing" {
			continue
		}
		inputs = append(inputs, map[string]string{"path": file.Path, "content_digest": "sha256:" + file.Digest})
		approvedSources = append(approvedSources, file)
		b.files[file.Path] = []byte("different document bytes")
	}
	manifest := map[string]any{
		"landing":         map[string]string{"document_version": "1.4"},
		"source_snapshot": map[string]any{"digest": snapshotDigest(approvedSources), "inputs": inputs},
		"asset_snapshot":  map[string]any{"digest": "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", "inputs": []any{}},
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	b.files["project_landing.manifest.json"] = encoded
	if err := b.evaluateManifest(); err != nil {
		t.Fatalf("declared drift should remain readable: %v", err)
	}
	if b.localState != "stale" {
		t.Fatalf("drift state = %q, want stale", b.localState)
	}
}

func TestBundleLoaderRejectsContentOutsideApprovedDigests(t *testing.T) {
	root := t.TempDir()
	foundation := filepath.Join(root, "foundation")
	for _, file := range approvedFiles {
		path := filepath.Join(foundation, filepath.FromSlash(file.Path))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("wrong bytes"), 0o444); err != nil {
			t.Fatal(err)
		}
	}
	lock, err := json.Marshal(bundleLock{Commit: approvedCommit, Fingerprint: approvedFingerprint, Files: approvedFiles})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "bundle.json"), lock, 0o444); err != nil {
		t.Fatal(err)
	}
	if _, err := loadBundle(root); err == nil {
		t.Fatal("a complete-looking bundle with wrong document bytes must not load")
	}
}

func contains(value, part string) bool {
	return len(value) >= len(part) && (value == part || len(value) > len(part) && (value[:len(part)] == part || contains(value[1:], part)))
}
