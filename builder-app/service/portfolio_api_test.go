package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPortfolioAPIReadsOneCompanyCatalogAndPinnedCover(t *testing.T) {
	const revision = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const digest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	const coverDigest = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	catalogCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/internal/v1/companies/company/portfolios":
			catalogCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{"company_id": "company", "projects": []any{
				map[string]any{"company_id": "company", "project_id": "project", "state": "available", "binding_revision": 3, "revision": revision, "inventory_digest": digest, "schema_version": "1", "summary": "Portfolio summary", "files": []any{map[string]any{"path": "project_portfolio/cover.png", "content_digest": coverDigest, "content_type": "image/png", "url": "/internal/hidden"}}},
				map[string]any{"company_id": "company", "project_id": "other", "state": "not_authored"},
			}})
		case "/internal/v1/companies/company/projects/project/portfolio/3/" + revision + "/" + strings.TrimPrefix(digest, "sha256:") + "/files/project_portfolio/cover.png":
			w.Header().Set("Content-Type", "image/png")
			w.Header().Set("X-Portfolio-Revision", revision)
			w.Header().Set("X-Portfolio-Inventory-Digest", digest)
			w.Header().Set("ETag", `"cover"`)
			_, _ = w.Write([]byte("PNG fixture"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	consumer := prototypeTestConsumer(t, upstream)
	request := func(method, target string) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		consumer.portfolioAPIHandler(response, httptest.NewRequest(method, target, nil))
		return response
	}
	read := request(http.MethodGet, "/api/portfolios?company_id=company")
	coverURL := "/api/portfolios/file/company/project/3/" + revision + "/" + strings.TrimPrefix(digest, "sha256:") + "/files/project_portfolio/cover.png"
	if read.Code != http.StatusOK || !strings.Contains(read.Body.String(), coverURL) || strings.Contains(read.Body.String(), "/internal/hidden") || catalogCalls != 1 {
		t.Fatalf("Portfolio catalog must be one Company call with public pinned URLs: status=%d calls=%d body=%s", read.Code, catalogCalls, read.Body.String())
	}
	cover := request(http.MethodGet, coverURL)
	if cover.Code != http.StatusOK || cover.Header().Get("Content-Type") != "image/png" || cover.Body.String() != "PNG fixture" {
		t.Fatalf("pinned cover must proxy exact Registry bytes: status=%d headers=%v body=%s", cover.Code, cover.Header(), cover.Body.String())
	}
	for _, target := range []string{
		"/api/portfolios?company_id=company&company_id=other",
		strings.Replace(coverURL, "/files/", "/other/", 1),
		strings.Replace(coverURL, "/project/3/", "/other/3/", 1),
	} {
		if response := request(http.MethodGet, target); response.Code < http.StatusBadRequest {
			t.Fatalf("invalid or foreign Portfolio URL succeeded: %s -> %d %s", target, response.Code, response.Body.String())
		}
	}
}
