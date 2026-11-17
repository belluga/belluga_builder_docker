package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTODODetailProjectsRelativeImageFileURLs(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	filePath := "todo-reference-images/company-a/project-a/todo-a/" + strings.Repeat("a", 64) + ".png"
	registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/internal/v1/catalog" {
			_, _ = w.Write([]byte(`{"schema_version":"1","companies":[{"id":"company-a"}],"projects":[{"id":"project-a","company_id":"company-a"}]}`))
			return
		}
		if r.URL.Path != "/internal/v1/companies/company-a/projects/project-a/todos/todo-a" || r.URL.Query().Get("todo_revision") != "2" {
			http.NotFound(w, r)
			return
		}
		response := map[string]any{"company_id": "company-a", "project_id": "project-a", "todo_revision": 2, "content_revision": 2, "content_digest": digest, "todo": map[string]any{"id": "todo-a", "project_id": "project-a", "revision": 2, "title": "TODO image", "objective": "Study image", "next_exact_step": "Review", "sections": []any{}, "content": map[string]any{"image_attachments": []any{map[string]any{"id": "image-a", "path": filePath, "digest": digest, "verified_revision": strings.Repeat("b", 40)}}}}}
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer registry.Close()
	p := &registeredPreview{registryURL: registry.URL}
	response := httptest.NewRecorder()
	p.roadmapAPIHandler(response, httptest.NewRequest(http.MethodGet, "/api/roadmap?company_id=company-a&project_id=project-a&todo_id=todo-a&todo_revision=2", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var projected struct {
		FileURLs []todoImageFileURL `json:"file_urls"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &projected); err != nil || len(projected.FileURLs) != 1 || projected.FileURLs[0].Path != filePath || projected.FileURLs[0].URL != "/api/todo-images/company-a/project-a/todo-a/image-a" {
		t.Fatalf("wrong file URLs: %+v %v", projected.FileURLs, err)
	}
}

func TestTODOImageProxySupportsGetAndHead(t *testing.T) {
	registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/v1/companies/company-a/projects/project-a/todos/todo-a/images/image-a" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Content-Length", "4")
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte("test"))
		}
	}))
	defer registry.Close()
	p := &registeredPreview{registryURL: registry.URL}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/todo-images/{companyID}/{projectID}/{todoID}/{imageID}", p.todoImageAPIHandler)
	mux.HandleFunc("HEAD /api/todo-images/{companyID}/{projectID}/{todoID}/{imageID}", p.todoImageAPIHandler)
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, httptest.NewRequest(method, "/api/todo-images/company-a/project-a/todo-a/image-a", nil))
		if recorder.Code != http.StatusOK || recorder.Header().Get("Content-Type") != "image/png" || recorder.Header().Get("Content-Length") != "4" {
			t.Fatalf("%s: unexpected response %d %v", method, recorder.Code, recorder.Header())
		}
		if method == http.MethodGet && recorder.Body.String() != "test" || method == http.MethodHead && recorder.Body.Len() != 0 {
			t.Fatalf("%s: unexpected body %q", method, recorder.Body.String())
		}
	}
	post := httptest.NewRecorder()
	mux.ServeHTTP(post, httptest.NewRequest(http.MethodPost, "/api/todo-images/company-a/project-a/todo-a/image-a", nil))
	if post.Code != http.StatusMethodNotAllowed {
		t.Fatalf("public upload status=%d", post.Code)
	}
}

func TestTODOImageProxyRejectsInvalidUpstreamAndRequest(t *testing.T) {
	for _, tc := range []struct {
		name, mime, length, body string
		status                   int
		want                     int
	}{
		{"missing", "image/png", "0", "", http.StatusNotFound, http.StatusNotFound},
		{"wrong mime", "text/html", "4", "test", http.StatusOK, http.StatusServiceUnavailable},
		{"wrong length", "image/png", "8", "test", http.StatusOK, http.StatusServiceUnavailable},
		{"too large", "image/png", "2097153", "test", http.StatusOK, http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", tc.mime)
				w.Header().Set("Content-Length", tc.length)
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer registry.Close()
			p := &registeredPreview{registryURL: registry.URL}
			mux := http.NewServeMux()
			mux.HandleFunc("GET /api/todo-images/{companyID}/{projectID}/{todoID}/{imageID}", p.todoImageAPIHandler)
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/todo-images/company-a/project-a/todo-a/image-a", nil))
			if response.Code != tc.want {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
	p := &registeredPreview{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/todo-images/{companyID}/{projectID}/{todoID}/{imageID}", p.todoImageAPIHandler)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/todo-images/invalid!/project-a/todo-a/image-a", nil))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("malformed identity status=%d", response.Code)
	}
}
