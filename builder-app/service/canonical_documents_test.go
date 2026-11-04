package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCanonicalDocumentsReadBoundary(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	manifest := map[string]any{"project_id": "builder", "generation_id": "launch", "active": true, "state": "active", "documents": []any{map[string]any{"family": "project_mandate", "id": "mandate", "revision": 2, "content_digest": digest}}}
	record := map[string]any{"project_id": "builder", "generation_id": "launch", "id": "mandate", "revision": 2, "content_digest": digest, "updated_at": "2026-11-03T12:00:00Z", "payload": map[string]any{"purpose": "Canonical product purpose", "scope": []string{"Projects"}}}
	status := http.StatusOK
	var value any = manifest
	var paths []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "" {
			t.Error("public credentials or mutation reached Registry")
		}
		if r.URL.Path == "/internal/v1/catalog" {
			writeJSON(w, 200, map[string]any{"schema_version": "1", "companies": []any{map[string]string{"id": "belluga", "name": "Belluga", "atlas_uri": "private"}}, "projects": []any{map[string]string{"id": "builder", "name": "Builder", "company_id": "belluga", "token": "private"}}})
			return
		}
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(value)
	}))
	defer upstream.Close()
	t.Setenv("DELPHI_BUILDER_REGISTRY_URL", upstream.URL)
	t.Setenv("DELPHI_BUILDER_REGISTRY_CUTOVER", "true")
	t.Setenv("DELPHI_PROJECT_READER_URL", "http://127.0.0.1:1")
	consumer := registeredHandler(t.TempDir(), t.TempDir(), "")
	read := func(target string) *httptest.ResponseRecorder {
		t.Helper()
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, target, nil)
		request.Host = "localhost"
		consumer.ServeHTTP(response, request)
		return response
	}
	t.Run("catalog remains usable without Foundation reader and strips private fields", func(t *testing.T) {
		response := read("/api/local-preview")
		if response.Code != 200 || !strings.Contains(response.Body.String(), `"name":"Builder"`) || strings.Contains(response.Body.String(), "private") {
			t.Fatalf("catalog: %d %s", response.Code, response.Body.String())
		}
	})
	t.Run("unbound Project detail is pending without reader or snapshot", func(t *testing.T) {
		response := read("/api/local-preview?company_id=belluga&project_id=builder")
		if response.Code != 200 || !strings.Contains(response.Body.String(), `"consumer_readiness":"pending"`) || strings.Contains(response.Body.String(), "snapshot_fingerprint") || paths[len(paths)-1] != "/internal/v1/catalog" {
			t.Fatalf("unbound detail: %d %s", response.Code, response.Body.String())
		}
	})
	t.Run("active manifest is read from Registry without snapshot", func(t *testing.T) {
		response := read("/api/knowledge?company_id=belluga&project_id=builder")
		if response.Code != 200 || !strings.Contains(response.Body.String(), digest) || response.Header().Get("Cache-Control") != "no-store" || paths[len(paths)-1] != "/internal/v1/companies/belluga/projects/builder/knowledge/manifest" {
			t.Fatalf("manifest: %d %s", response.Code, response.Body.String())
		}
	})
	t.Run("detail preserves exact record identity revision and payload", func(t *testing.T) {
		value = record
		response := read("/api/knowledge?company_id=belluga&project_id=builder&family=project_mandate&record_id=mandate")
		if response.Code != 200 || !strings.Contains(response.Body.String(), "Canonical product purpose") || !strings.Contains(response.Body.String(), `"revision":2`) || paths[len(paths)-1] != "/internal/v1/companies/belluga/projects/builder/knowledge/project_mandate/mandate" {
			t.Fatalf("record: %d %s", response.Code, response.Body.String())
		}
	})
	t.Run("pending is explicit and cannot reveal inactive draft", func(t *testing.T) {
		value = map[string]any{"project_id": "builder", "active": false, "state": "pending", "documents": []any{}, "teach": "TEACH: import and activate this Project"}
		response := read("/api/knowledge?company_id=belluga&project_id=builder")
		if response.Code != 200 || !strings.Contains(response.Body.String(), `"state":"pending"`) || !strings.Contains(response.Body.String(), "TEACH:") {
			t.Fatalf("pending: %d %s", response.Code, response.Body.String())
		}
	})
	t.Run("cross tenant or mismatched identity is rejected", func(t *testing.T) {
		response := read("/api/knowledge?company_id=other&project_id=builder")
		if response.Code != 404 {
			t.Fatalf("cross tenant: %d", response.Code)
		}
		value = map[string]any{"project_id": "other", "active": true, "state": "active", "generation_id": "launch", "documents": []any{}}
		response = read("/api/knowledge?company_id=belluga&project_id=builder")
		if response.Code != 503 {
			t.Fatalf("mismatched project: %d %s", response.Code, response.Body.String())
		}
	})
	t.Run("unknown record and upstream errors are sanitized", func(t *testing.T) {
		value = map[string]any{"error": "mongodb+srv://secret", "teach": "token=secret"}
		for _, code := range []int{404, 500} {
			status = code
			response := read("/api/knowledge?company_id=belluga&project_id=builder&family=module&record_id=missing")
			want := code
			if code == 500 {
				want = 503
			}
			if response.Code != want || strings.Contains(response.Body.String(), "secret") || !strings.Contains(response.Body.String(), "TEACH:") {
				t.Fatalf("error: %d %s", response.Code, response.Body.String())
			}
		}
		status = 200
	})
	t.Run("invalid selectors and mutations never reach Registry", func(t *testing.T) {
		before := len(paths)
		for _, query := range []string{
			"company_id=belluga&project_id=builder&generation_id=draft",
			"company_id=belluga&project_id=builder&project_id=other",
			"company_id=belluga&project_id=builder&family=module",
			"company_id=belluga&project_id=builder&family=activate&record_id=x",
			"company_id=belluga&project_id=builder&family=module&record_id=..%2Fsecret",
		} {
			if response := read("/api/knowledge?" + query); response.Code != 400 {
				t.Errorf("invalid selector accepted: %s %d", query, response.Code)
			}
		}
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/api/knowledge?company_id=belluga&project_id=builder", strings.NewReader(`{}`))
		request.Host = "localhost"
		consumer.ServeHTTP(response, request)
		if response.Code < 400 || len(paths) != before {
			t.Fatalf("invalid request reached Registry: status=%d calls=%d", response.Code, len(paths)-before)
		}
	})
}
