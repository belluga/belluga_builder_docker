package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func prototypeTestConsumer(t *testing.T, upstream *httptest.Server) *registeredPreview {
	t.Helper()
	return &registeredPreview{registryURL: upstream.URL, client: upstream.Client()}
}

func TestPrototypeAPIProxiesRevisionBoundCatalogDetailAndFiles(t *testing.T) {
	const revision = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const digest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	const item = `{"id":"demo","name":"Demo","root":"prototypes/demo","status":"active","authoring_mode":"image_first","entry_point":"index.html","screens":[],"scenarios":[],"transitions":[]}`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/internal/v1/catalog" {
			t.Error("Prototype request fetched the full Project catalog before its scoped Registry route")
			_ = json.NewEncoder(w).Encode(map[string]any{"schema_version": "1", "companies": []any{map[string]string{"id": "company", "name": "Company"}}, "projects": []any{map[string]string{"id": "project", "name": "Project", "company_id": "company"}}})
			return
		}
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
			_, _ = w.Write([]byte(`<main>Prototype <i style="--fill:80%"></i></main>`))
		case "/internal/v1/companies/company/projects/project/prototypes/" + revision + "/files/prototypes/demo/experience.js":
			w.Header().Set("Content-Type", "application/javascript")
			w.Header().Set("ETag", `"`+revision+`-script"`)
			w.Header().Set("X-Prototype-Revision", revision)
			w.Header().Set("X-Prototype-Inventory-Digest", digest)
			_, _ = w.Write([]byte(`document.body.innerHTML = "<main>Rendered</main>";`))
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
	policy := file.Header().Get("Content-Security-Policy")
	if file.Code != http.StatusOK || file.Body.String() != `<main>Prototype <i style="--fill:80%"></i></main>` || file.Header().Get("X-Prototype-Revision") != revision || file.Header().Get("X-Prototype-Inventory-Digest") != digest || !strings.Contains(policy, "sandbox allow-scripts") {
		t.Fatalf("file proxy status=%d headers=%v body=%s", file.Code, file.Header(), file.Body.String())
	}
	if cspDirective(policy, "style-src-attr") != "'unsafe-inline'" || cspDirective(policy, "style-src-elem") != "'self'" {
		t.Fatalf("HTML must allow only inline style attributes while keeping stylesheet elements self-hosted: %q", policy)
	}
	if cspDirective(policy, "script-src") != "'self'" || strings.Contains(cspDirective(policy, "script-src"), "unsafe-inline") {
		t.Fatalf("HTML must not relax executable script policy: %q", policy)
	}
	assetURL := strings.TrimSuffix(fileURL, "index.html") + "experience.js"
	versionedAsset := request(http.MethodGet, assetURL+"?v=project-entry-loading-2")
	if versionedAsset.Code != http.StatusOK || !strings.Contains(versionedAsset.Body.String(), "Rendered") || versionedAsset.Header().Get("X-Prototype-Revision") != revision {
		t.Fatalf("versioned script proxy status=%d headers=%v body=%s", versionedAsset.Code, versionedAsset.Header(), versionedAsset.Body.String())
	}
	for _, invalid := range []string{"?unknown=1", "?v=", "?v=one&v=two", "?v=../../secret", "?prototype_state=default"} {
		response := request(http.MethodGet, assetURL+invalid)
		if response.Code != http.StatusBadRequest {
			t.Errorf("invalid script query %q status=%d body=%s", invalid, response.Code, response.Body.String())
		}
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

func cspDirective(policy, name string) string {
	for _, directive := range strings.Split(policy, ";") {
		fields := strings.Fields(strings.TrimSpace(directive))
		if len(fields) > 0 && fields[0] == name {
			return strings.Join(fields[1:], " ")
		}
	}
	return ""
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
