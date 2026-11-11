package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCatalogRouteUsesOnlyRegistryCatalogAndRetiresSnapshotRoutes(t *testing.T) {
	var registryCalls int
	registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		registryCalls++
		if r.Method != http.MethodGet || r.URL.Path != "/internal/v1/catalog" || r.URL.RawQuery != "" {
			t.Errorf("unexpected Registry request: %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"schema_version": "1",
			"companies":      []any{map[string]any{"id": "belluga-solutions", "name": "Belluga Solutions", "private": "strip"}},
			"projects": []any{map[string]any{
				"id": "builder", "name": "Builder", "company_id": "belluga-solutions",
				"binding": map[string]any{"repository_id": "belluga/private", "publication_branch": "r-12", "foundation_root": "private"},
			}},
		})
	}))
	defer registry.Close()
	t.Setenv("DELPHI_BUILDER_REGISTRY_URL", registry.URL)
	t.Setenv("DELPHI_PROJECT_READER_URL", "http://127.0.0.1:1")

	handler := registeredHandler(t.TempDir(), t.TempDir(), "")
	read := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Host = "localhost"
		handler.ServeHTTP(recorder, request)
		return recorder
	}

	catalog := read("/api/catalog")
	if catalog.Code != http.StatusOK || !strings.Contains(catalog.Body.String(), `"id":"builder"`) || strings.Contains(catalog.Body.String(), "private") || strings.Contains(catalog.Body.String(), "binding") {
		t.Fatalf("catalog response must expose only public Company/Project identity: status=%d body=%s", catalog.Code, catalog.Body.String())
	}
	if catalog.Header().Get("Cache-Control") != "no-store" || registryCalls != 1 {
		t.Fatalf("catalog cache or Registry call contract changed: cache=%q calls=%d", catalog.Header().Get("Cache-Control"), registryCalls)
	}
	if invalid := read("/api/catalog?project_id=builder"); invalid.Code != http.StatusBadRequest || registryCalls != 1 {
		t.Fatalf("catalog selector was not rejected before Registry: status=%d calls=%d", invalid.Code, registryCalls)
	}
	for _, path := range []string{
		"/api/local-preview",
		"/api/local-preview?company_id=belluga-solutions&project_id=builder",
		"/snapshot/belluga-solutions/builder/" + strings.Repeat("a", 64) + "/project_landing/index.html",
	} {
		response := read(path)
		if response.Code != http.StatusGone || !strings.Contains(response.Body.String(), "project_snapshot_retired") {
			t.Errorf("retired route %q status=%d body=%s", path, response.Code, response.Body.String())
		}
	}
	if registryCalls != 1 {
		t.Fatalf("retired routes unexpectedly reached Registry: calls=%d", registryCalls)
	}
}
