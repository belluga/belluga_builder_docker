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
				map[string]any{"company_id": "company", "project_id": "project", "state": "available", "binding_revision": 3, "revision": revision, "inventory_digest": digest, "schema_version": "1", "summary": "Portfolio summary", "teach_detail": map[string]any{"schema_version": "1", "code": "portfolio_available", "next_action": "Review public portfolio details.", "operation_id": "private-op", "object_purpose": "secret purpose"}, "files": []any{
					map[string]any{"path": "project_portfolio.json", "content_digest": coverDigest, "content_type": "application/json", "url": "/internal/hidden"},
					map[string]any{"path": "project_portfolio/cover.png", "content_digest": coverDigest, "content_type": "image/png", "url": "/internal/hidden"},
				}},
				map[string]any{"company_id": "company", "project_id": "other", "state": "not_authored", "teach": "TEACH: Portfolio is not authored.", "teach_detail": map[string]any{"schema_version": "1", "code": "portfolio_not_authored", "next_action": "Create the portfolio source.", "record_id": "private-record", "accepted_values": []string{"safe", "https://private.example/token=secret"}}},
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
	if read.Code != http.StatusOK || !strings.Contains(read.Body.String(), coverURL) || strings.Contains(read.Body.String(), "/internal/hidden") || strings.Contains(read.Body.String(), "private-op") || strings.Contains(read.Body.String(), "secret purpose") || strings.Contains(read.Body.String(), "private-record") || strings.Contains(read.Body.String(), "private.example") || catalogCalls != 1 {
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

func TestPortfolioSuccessfulRowsProjectLegacyTeachAndFiniteAcceptedValues(t *testing.T) {
	const revision = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const inventory = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	const cover = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"company_id": "company", "projects": []any{
			map[string]any{"company_id": "company", "project_id": "project", "state": "available", "binding_revision": 3, "revision": revision, "inventory_digest": inventory, "teach": "TEACH: https://private.example/token=leak", "teach_detail": map[string]any{"schema_version": "1", "code": "portfolio_invalid", "next_action": "Review the published Portfolio details.", "accepted_values": []string{"planned", "id-shaped-secret"}}, "files": []any{map[string]any{"path": "project_portfolio/cover.png", "content_digest": cover, "content_type": "image/png"}}},
			map[string]any{"company_id": "company", "project_id": "other", "state": "not_authored", "teach": "TEACH: This portfolio is awaiting publication."},
			map[string]any{"company_id": "company", "project_id": "invalid-item", "state": "invalid", "teach": "TEACH: Correct the published Portfolio manifest."},
			map[string]any{"company_id": "company", "project_id": "unavailable-item", "state": "unavailable", "teach": "TEACH: Check the Portfolio Registry connection."},
			map[string]any{"company_id": "company", "project_id": "malformed-detail", "state": "invalid", "teach": "TEACH: Correct the published Portfolio metadata.", "teach_detail": map[string]any{"schema_version": "2", "code": "private_code", "next_action": "/private/config", "operation_id": "private-operation"}},
		}})
	}))
	defer upstream.Close()
	consumer := prototypeTestConsumer(t, upstream)
	response := httptest.NewRecorder()
	consumer.portfolioAPIHandler(response, httptest.NewRequest(http.MethodGet, "/api/portfolios?company_id=company", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("portfolio catalog failed: %d %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	if strings.Contains(body, "private.example") || strings.Contains(body, "token=leak") || strings.Contains(body, "id-shaped-secret") || strings.Contains(body, "private-operation") || strings.Contains(body, "/private/config") || !strings.Contains(body, `"accepted_values":["planned"]`) || !strings.Contains(body, "TEACH: Revise a publicação Portfolio atual antes de continuar.") {
		t.Fatalf("Portfolio success projection exposed unsafe TEACH values or removed the finite public enum: %s", body)
	}
	var got portfolioProxyCatalog
	if json.Unmarshal(response.Body.Bytes(), &got) != nil {
		t.Fatalf("Portfolio response did not decode: %s", body)
	}
	wantStates := map[string]struct{ state, teach string }{
		"other":            {"not_authored", "TEACH: This portfolio is awaiting publication."},
		"invalid-item":     {"invalid", "TEACH: Correct the published Portfolio manifest."},
		"unavailable-item": {"unavailable", "TEACH: Check the Portfolio Registry connection."},
		"malformed-detail": {"invalid", "TEACH: Correct the published Portfolio metadata."},
	}
	for _, item := range got.Projects {
		want, expected := wantStates[item.ProjectID]
		if !expected {
			continue
		}
		if item.State != want.state || item.Teach != want.teach || item.TeachDetail == nil || !safeProxyTeachDetail(*item.TeachDetail) || item.TeachDetail.Code != "portfolio_"+want.state || strings.TrimSpace(item.TeachDetail.NextAction) == "" || item.TeachDetail.Effects != "" || item.TeachDetail.Retry != "" || item.TeachDetail.Revision != nil {
			t.Errorf("Portfolio %s %q lacked valid state-specific fallback: %#v", item.ProjectID, want.state, item.TeachDetail)
		}
		delete(wantStates, item.ProjectID)
	}
	if len(wantStates) != 0 {
		t.Fatalf("Portfolio handler dropped expected diagnostic rows: %#v", wantStates)
	}
}

func TestPortfolioAPIAcceptsCoverOnlyButRejectsMalformedSummaryEntry(t *testing.T) {
	const revision = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const inventory = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	const coverDigest = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	var catalog any = map[string]any{"company_id": "company", "projects": []any{
		map[string]any{"company_id": "company", "project_id": "project", "state": "available", "binding_revision": 3, "revision": revision, "inventory_digest": inventory, "files": []any{
			map[string]any{"path": "project_portfolio/cover.png", "content_digest": coverDigest, "content_type": "image/png"},
		}},
	}}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(catalog)
	}))
	defer upstream.Close()
	consumer := prototypeTestConsumer(t, upstream)
	request := func() *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		consumer.portfolioAPIHandler(response, httptest.NewRequest(http.MethodGet, "/api/portfolios?company_id=company", nil))
		return response
	}
	if response := request(); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "/api/portfolios/file/company/project/3/") {
		t.Fatalf("cover-only portfolio without summary schema must be admitted: status=%d body=%s", response.Code, response.Body.String())
	}
	// A non-cover summary entry still requires its own v1 metadata; accepting the
	// cover must not suppress malformed entries in the same catalog item.
	catalog = map[string]any{"company_id": "company", "projects": []any{
		map[string]any{"company_id": "company", "project_id": "project", "state": "available", "binding_revision": 3, "revision": revision, "inventory_digest": inventory, "files": []any{
			map[string]any{"path": "project_portfolio.json", "content_digest": coverDigest, "content_type": "application/json"},
			map[string]any{"path": "project_portfolio/cover.png", "content_digest": coverDigest, "content_type": "image/png"},
		}},
	}}
	if response := request(); response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "portfolio_response_invalid") {
		t.Fatalf("malformed summary entry must invalidate catalog: status=%d body=%s", response.Code, response.Body.String())
	}
}
