package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLandingAPIProxiesExactRevisionInventoryAndFiles(t *testing.T) {
	const revision = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const digest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	const filePath = "design/landing/index.html"
	entryURL := "/api/landing/file/company/project/3/" + revision + "/" + strings.TrimPrefix(digest, "sha256:") + "/files/" + filePath
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/internal/v1/catalog":
			t.Error("Landing request fetched the full Project catalog before its scoped Registry route")
			serveLandingTestCatalog(w)
		case "/internal/v1/companies/company/projects/project/landing":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"company_id": "company", "project_id": "project", "repository_id": "example/project", "publication_branch": "main",
				"binding_revision": 3, "revision": revision, "inventory_digest": digest, "manifest_version": "2", "entry_point": filePath,
				"knowledge_origin": map[string]any{"project_id": "project", "generation_id": "generation-1", "documents": []any{map[string]any{"family": "project_constitution", "id": "builder", "revision": 2, "content_digest": digest}}},
				"files":            []any{map[string]string{"path": filePath, "url": "/internal/private/ignored"}, map[string]string{"path": "design/landing/site.css", "url": "/internal/private/ignored"}, map[string]string{"path": "design/landing/app.mjs", "url": "/internal/private/ignored"}},
			})
		case "/internal/v1/companies/company/projects/project/landing/3/" + revision + "/files/" + filePath:
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("ETag", `"landing-v1"`)
			w.Header().Set("X-Landing-Revision", revision)
			w.Header().Set("X-Landing-Inventory-Digest", digest)
			_, _ = w.Write([]byte(`<!doctype html><link rel="stylesheet" href="site.css"><main>Rendered fixture</main>`))
		case "/internal/v1/companies/company/projects/project/landing/3/" + revision + "/files/design/landing/site.css":
			w.Header().Set("Content-Type", "text/css; charset=utf-8")
			w.Header().Set("ETag", `"landing-css"`)
			w.Header().Set("X-Landing-Revision", revision)
			w.Header().Set("X-Landing-Inventory-Digest", digest)
			_, _ = w.Write([]byte("main{color:teal}"))
		case "/internal/v1/companies/company/projects/project/landing/3/" + revision + "/files/design/landing/app.mjs":
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			w.Header().Set("ETag", `"landing-js"`)
			w.Header().Set("X-Landing-Revision", revision)
			w.Header().Set("X-Landing-Inventory-Digest", digest)
			_, _ = w.Write([]byte("document.body.dataset.loaded = 'yes'"))
		case "/internal/v1/companies/company/projects/project/landing/4/" + revision + "/files/" + filePath:
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":"conflict","teach":"TEACH: Binding changed; reload Landing catalog."}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	consumer := prototypeTestConsumer(t, upstream)
	request := func(target string) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		consumer.landingAPIHandler(response, httptest.NewRequest(http.MethodGet, target, nil))
		return response
	}
	catalog := request("/api/landing?company_id=company&project_id=project")
	if catalog.Code != http.StatusOK || !strings.Contains(catalog.Body.String(), `"entry_url":"`+entryURL+`"`) || strings.Contains(catalog.Body.String(), "/internal/private") {
		t.Fatalf("Landing catalog must expose only exact-revision local links: status=%d body=%s", catalog.Code, catalog.Body.String())
	}
	page := request(entryURL)
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "Rendered fixture") || page.Header().Get("X-Landing-Revision") != revision || page.Header().Get("X-Landing-Inventory-Digest") != digest {
		t.Fatalf("Landing HTML must remain pinned to its catalog revision and inventory: status=%d headers=%v body=%s", page.Code, page.Header(), page.Body.String())
	}
	asset := request("/api/landing/file/company/project/3/" + revision + "/" + strings.TrimPrefix(digest, "sha256:") + "/files/design/landing/site.css")
	if asset.Code != http.StatusOK || asset.Header().Get("Content-Type") != "text/css; charset=utf-8" || asset.Header().Get("X-Landing-Revision") != revision {
		t.Fatalf("relative Landing asset must preserve revision and MIME: status=%d headers=%v body=%s", asset.Code, asset.Header(), asset.Body.String())
	}
	javascript := request("/api/landing/file/company/project/3/" + revision + "/" + strings.TrimPrefix(digest, "sha256:") + "/files/design/landing/app.mjs")
	if javascript.Code != http.StatusOK || javascript.Header().Get("Content-Type") != "text/javascript; charset=utf-8" || !strings.Contains(javascript.Body.String(), "dataset.loaded") {
		t.Fatalf("Registry JavaScript MIME must pass through without policy drift: status=%d headers=%v body=%s", javascript.Code, javascript.Header(), javascript.Body.String())
	}
	staleBinding := request("/api/landing/file/company/project/4/" + revision + "/" + strings.TrimPrefix(digest, "sha256:") + "/files/" + filePath)
	if staleBinding.Code != http.StatusConflict || !strings.Contains(staleBinding.Body.String(), "landing_selection_changed") {
		t.Fatalf("rebind must invalidate a previously pinned Landing URL: status=%d body=%s", staleBinding.Code, staleBinding.Body.String())
	}
	for _, target := range []string{
		"/api/landing/file/company/project/3/" + revision + "/" + strings.TrimPrefix(digest, "sha256:") + "/files/design/landing/private.txt",
		"/api/landing/file/company/project/3/" + strings.Repeat("c", 40) + "/" + strings.TrimPrefix(digest, "sha256:") + "/files/" + filePath,
	} {
		if response := request(target); response.Code < http.StatusBadRequest {
			t.Fatalf("invalid or stale Landing URL was served: %s -> %d %s", target, response.Code, response.Body.String())
		}
	}
}

func TestLandingAPIExposesInvalidLegacyManifestSeparatelyFromNotAuthored(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/internal/v1/catalog" {
			serveLandingTestCatalog(w)
			return
		}
		if r.URL.Path != "/internal/v1/companies/company/projects/project/landing" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_input","teach":"TEACH: Replace the legacy v1 manifest with the strict v2 contract."}`))
	}))
	defer upstream.Close()
	consumer := prototypeTestConsumer(t, upstream)
	response := httptest.NewRecorder()
	consumer.landingAPIHandler(response, httptest.NewRequest(http.MethodGet, "/api/landing?company_id=company&project_id=project", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"state":"invalid"`) || !strings.Contains(response.Body.String(), "legacy v1") || strings.Contains(response.Body.String(), `"state":"not_authored"`) {
		t.Fatalf("legacy invalid artifact must not be reported as absent: status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestLandingAPIExposesNotAuthoredTEACHWithoutSnapshotFallback(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/internal/v1/catalog" {
			serveLandingTestCatalog(w)
			return
		}
		if r.URL.Path != "/internal/v1/companies/company/projects/project/landing" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"not_found","teach":"TEACH: publish a version 2 Landing manifest at the bound Git revision"}`))
	}))
	defer upstream.Close()
	consumer := prototypeTestConsumer(t, upstream)
	response := httptest.NewRecorder()
	consumer.landingAPIHandler(response, httptest.NewRequest(http.MethodGet, "/api/landing?company_id=company&project_id=project", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"state":"not_authored"`) || !strings.Contains(response.Body.String(), "TEACH") {
		t.Fatalf("missing canonical Landing should be an explicit not-authored state: status=%d body=%s", response.Code, response.Body.String())
	}
}

func serveLandingTestCatalog(w http.ResponseWriter) {
	_ = json.NewEncoder(w).Encode(map[string]any{
		"schema_version": "1",
		"companies":      []any{map[string]any{"id": "company", "name": "Company"}},
		"projects":       []any{map[string]any{"id": "project", "name": "Project", "company_id": "company"}},
	})
}
