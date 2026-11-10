package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func prototypeTestConsumer(t *testing.T, upstream *httptest.Server) *registeredPreview {
	t.Helper()
	root := t.TempDir()
	registry := persistedRegistration{
		SchemaVersion: "builder-project-registry-v1",
		Companies:     map[string]registeredCompany{"company": {ID: "company", Name: "Company"}},
		Projects:      map[string]persistedProject{"project": {registeredProject: registeredProject{ID: "project", Name: "Project", CompanyID: "company"}, Binding: &persistedBinding{WorkspaceRoot: "/tmp/workspace", ProjectRoot: "project", FoundationRoot: "project/foundation", RepositoryID: "example/project", PublicationBranch: "main"}}},
		Preferences:   map[string]persistedPreference{},
	}
	data, err := json.Marshal(registry)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "registry.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	return &registeredPreview{root: root, registryURL: upstream.URL, client: upstream.Client()}
}

func TestPrototypeAPIProxiesRevisionBoundCatalogDetailAndFiles(t *testing.T) {
	const revision = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const digest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	const item = `{"id":"demo","name":"Demo","root":"prototypes/demo","status":"active","authoring_mode":"image_first","entry_point":"index.html","screens":[],"scenarios":[],"transitions":[]}`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/internal/v1/companies/company/projects/project/prototypes") {
			t.Errorf("unexpected Registry path %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		switch r.URL.Path {
		case "/internal/v1/companies/company/projects/project/prototypes":
			_ = json.NewEncoder(w).Encode(map[string]any{"repository_id": "example/project", "publication_branch": "main", "binding_revision": 3, "revision": revision, "evaluator_version": "prototype-v3", "inventory_digest": digest, "knowledge_digest": strings.TrimPrefix(digest, "sha256:"), "items": []any{map[string]any{"id": "demo", "name": "Demo", "root": "prototypes/demo", "status": "active", "detail_url": "/internal/v1/detail"}}})
		case "/internal/v1/companies/company/projects/project/prototypes/" + revision + "/demo":
			_ = json.NewEncoder(w).Encode(map[string]any{"repository_id": "example/project", "publication_branch": "main", "binding_revision": 3, "revision": revision, "evaluator_version": "prototype-v3", "inventory_digest": digest, "knowledge_digest": strings.TrimPrefix(digest, "sha256:"), "item": json.RawMessage(item), "file_urls": []any{map[string]string{"path": "prototypes/demo/index.html", "url": "/internal/v1/file"}}})
		case "/internal/v1/companies/company/projects/project/prototypes/" + revision + "/files/prototypes/demo/index.html":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("ETag", `"`+revision+`-body"`)
			w.Header().Set("X-Prototype-Revision", revision)
			w.Header().Set("X-Prototype-Inventory-Digest", digest)
			if r.Header.Get("If-None-Match") == `"`+revision+`-body"` {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			_, _ = w.Write([]byte("<main>Prototype</main>"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	consumer := prototypeTestConsumer(t, upstream)
	request := func(method, target string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, target, nil)
		r.Host = "127.0.0.1:8080"
		response := httptest.NewRecorder()
		consumer.prototypeAPIHandler(response, r)
		return response
	}
	catalog := request(http.MethodGet, "/api/prototypes?company_id=company&project_id=project")
	if catalog.Code != http.StatusOK || !strings.Contains(catalog.Body.String(), "/api/prototypes/"+revision+"/demo?company_id=company\\u0026project_id=project") {
		t.Fatalf("catalog proxy status=%d body=%s", catalog.Code, catalog.Body.String())
	}
	detail := request(http.MethodGet, "/api/prototypes/"+revision+"/demo?company_id=company&project_id=project")
	fileURL := "/api/prototypes/file/company/project/" + revision + "/" + strings.TrimPrefix(digest, "sha256:") + "/files/prototypes/demo/index.html"
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), fileURL) {
		t.Fatalf("detail proxy status=%d body=%s", detail.Code, detail.Body.String())
	}
	var parsedDetail struct {
		EntryURL string `json:"entry_url"`
	}
	if err := json.Unmarshal(detail.Body.Bytes(), &parsedDetail); err != nil || parsedDetail.EntryURL != fileURL {
		t.Fatalf("detail must expose its revision-bound entry URL: %q, %v", parsedDetail.EntryURL, err)
	}
	file := request(http.MethodGet, fileURL)
	if file.Code != http.StatusOK || file.Body.String() != "<main>Prototype</main>" || file.Header().Get("X-Prototype-Revision") != revision || file.Header().Get("X-Prototype-Inventory-Digest") != digest || !strings.Contains(file.Header().Get("Content-Security-Policy"), "sandbox allow-scripts") {
		t.Fatalf("file proxy status=%d headers=%v body=%s", file.Code, file.Header(), file.Body.String())
	}
	head := httptest.NewRequest(http.MethodGet, fileURL, nil)
	head.Header.Set("If-None-Match", `"`+revision+`-body"`)
	headResponse := httptest.NewRecorder()
	consumer.prototypeAPIHandler(headResponse, head)
	if headResponse.Code != http.StatusNotModified || headResponse.Header().Get("X-Prototype-Revision") != revision {
		t.Fatalf("conditional file response status=%d headers=%v body=%s", headResponse.Code, headResponse.Header(), headResponse.Body.String())
	}
	base, _ := url.Parse(fileURL)
	relative, _ := url.Parse("app.css")
	if got := base.ResolveReference(relative).String(); got != strings.TrimSuffix(fileURL, "index.html")+"app.css" {
		t.Fatalf("relative HTML asset escaped its revision-bound file route: %s", got)
	}
}

func TestPrototypeAPIRejectsUnregisteredIdentityAndInvalidRevision(t *testing.T) {
	upstream := httptest.NewServer(http.NotFoundHandler())
	defer upstream.Close()
	consumer := prototypeTestConsumer(t, upstream)
	for _, target := range []string{"/api/prototypes?company_id=outside&project_id=project", "/api/prototypes/not-a-sha/demo?company_id=company&project_id=project", "/api/prototypes/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/files/../secret?company_id=company&project_id=project"} {
		response := httptest.NewRecorder()
		consumer.prototypeAPIHandler(response, httptest.NewRequest(http.MethodGet, target, nil))
		if response.Code < 400 {
			t.Errorf("unsafe selection %s returned %d", target, response.Code)
		}
	}
}
