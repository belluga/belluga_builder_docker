package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOverviewProxyRequiresRegistryIdentityAndValidatesInvalidState(t *testing.T) {
	const company, project = "belluga", "builder"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/internal/v1/catalog" {
			writeJSON(w, 200, map[string]any{"schema_version": "1", "companies": []any{map[string]string{"id": company}}, "projects": []any{map[string]string{"id": project, "company_id": company}}})
			return
		}
		if r.URL.Path == "/internal/v1/companies/belluga/projects/builder/overview" {
			writeJSON(w, 200, overviewProxyResponse{State: "invalid", CompanyID: company, ProjectID: project, Teach: "TEACH: Fix the active Overview contract."})
			return
		}
		http.NotFound(w, r)
	}))
	defer upstream.Close()
	p := &registeredPreview{registryURL: upstream.URL, client: upstream.Client()}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest("GET", "/api/overview?company_id=belluga&project_id=builder", nil)
	p.overviewAPIHandler(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var got overviewProxyResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil || got.State != "invalid" || got.Teach == "" {
		t.Fatalf("invalid state was not preserved: %#v err=%v", got, err)
	}
}

func TestOverviewMediaProxyPinsExactRegistryRevisionAndDigest(t *testing.T) {
	const company, project = "belluga", "builder"
	pngBytes, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/sWQAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(pngBytes)
	digest := fmt.Sprintf("sha256:%x", sum)
	content := json.RawMessage(`{"schema_version":"1","tagline":"Purpose","summary":"Summary","desktop_alt":"Desktop","mobile_alt":"Mobile","sections":{"problem":{"heading":"Problem","body":"Body","points":[]},"proposal":{"heading":"Proposal","body":"Body","points":[]},"impact":{"heading":"Impact","body":"Body","points":[]},"audience":{"heading":"Audience","body":"Body","points":[]}}}`)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/internal/v1/catalog" {
			writeJSON(w, 200, map[string]any{"schema_version": "1", "companies": []any{map[string]string{"id": company}}, "projects": []any{map[string]string{"id": project, "company_id": company}}})
			return
		}
		if r.URL.Path == "/internal/v1/companies/belluga/projects/builder/overview/4" {
			writeJSON(w, 200, overviewProxyResponse{State: "available", CompanyID: company, ProjectID: project, Revision: 4, Content: content, Media: []overviewProxyMedia{{Slot: "desktop", Digest: digest}, {Slot: "mobile", Digest: digest}}, KnowledgeOrigin: json.RawMessage(`{"project_id":"builder"}`), Source: json.RawMessage(`{"origin_absent":true}`)})
			return
		}
		if r.URL.Path == "/internal/v1/companies/belluga/projects/builder/overview/4/media/desktop" {
			w.Header().Set("Content-Type", "image/png")
			w.Header().Set("ETag", `"`+digest+`"`)
			_, _ = w.Write(pngBytes)
			return
		}
		http.NotFound(w, r)
	}))
	defer upstream.Close()
	p := &registeredPreview{registryURL: upstream.URL, client: upstream.Client()}
	w := httptest.NewRecorder()
	p.overviewAPIHandler(w, httptest.NewRequest("GET", "/api/overview/media/belluga/builder/4/desktop", nil))
	if w.Code != 200 || w.Header().Get("Content-Type") != "image/png" || !strings.EqualFold(fmt.Sprintf("%x", sha256.Sum256(w.Body.Bytes())), strings.TrimPrefix(digest, "sha256:")) {
		t.Fatalf("media proxy response status=%d headers=%v", w.Code, w.Header())
	}
}

func TestOverviewProxyValidatesCurrentIdentityAndRequestShape(t *testing.T) {
	for _, tc := range []struct {
		path   string
		status int
	}{{"/api/overview?company_id=belluga&project_id=builder", http.StatusOK}, {"/api/overview?company_id=belluga&company_id=belluga&project_id=builder", http.StatusBadRequest}} {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/catalog") {
				writeJSON(w, 200, map[string]any{"schema_version": "1", "companies": []any{map[string]string{"id": "belluga"}}, "projects": []any{map[string]string{"id": "builder", "company_id": "belluga"}}})
				return
			}
			writeJSON(w, 200, overviewProxyResponse{State: "not_authored", CompanyID: "belluga", ProjectID: "builder", Teach: "TEACH: Author Overview."})
		}))
		p := &registeredPreview{registryURL: upstream.URL, client: upstream.Client()}
		w := httptest.NewRecorder()
		p.overviewAPIHandler(w, httptest.NewRequest("GET", tc.path, nil))
		if w.Code != tc.status {
			t.Errorf("%s status=%d want %d: %s", tc.path, w.Code, tc.status, w.Body.String())
		}
		upstream.Close()
	}
}
