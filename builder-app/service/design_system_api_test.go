package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func designSystemResponseFixture(companyID, projectID, revision string) []byte {
	digest := "sha256:" + strings.Repeat("b", 64)
	return []byte(fmt.Sprintf(`{"selection":{"state":"selected","repository_id":"belluga/builder","publication_branch":"main","sha":%q,"inventory_digest":%q,"binding_revision":3},"evaluation":{"schema_version":"1","project_id":%q,"company_id":%q,"target":"design-system","authority_scope":"local_structure_only","mode":"committed","revision":%q,"outcome":"go","inventory_digest":%q,"items":[{"id":"system","name":"System","assets":["design/system/assets/mark.svg"]}],"diagnostics":[],"selected_level":"project","design_system_validation":"valid"}}`, revision, digest, projectID, companyID, revision, digest))
}

func TestDesignSystemAPIWorksWhenLocalPreviewIsPendingAndForwardsExactSHAAssets(t *testing.T) {
	const companyID, projectID = "belluga", "builder"
	const revision = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	detail := designSystemResponseFixture(companyID, projectID, revision)
	var registryPaths []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		registryPaths = append(registryPaths, r.URL.EscapedPath())
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Errorf("public credentials were forwarded to Registry: headers=%v", r.Header)
		}
		switch r.URL.Path {
		case "/internal/v1/catalog":
			writeJSON(w, http.StatusOK, map[string]any{
				"schema_version": "1", "companies": []any{map[string]string{"id": companyID, "name": "Belluga"}},
				"projects": []any{map[string]any{"id": projectID, "name": "Builder", "company_id": companyID, "binding": map[string]any{"repository_id": "belluga/builder", "publication_branch": "main"}}},
			})
		case "/internal/v1/companies/belluga/projects/builder/design-system":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(detail)
		case "/internal/v1/companies/belluga/projects/builder/design-system/" + revision + "/assets/design/system/assets/mark.svg":
			if r.Header.Get("If-None-Match") == `"`+revision+`-fixture` {
				w.Header().Set("ETag", `"`+revision+`-fixture`)
				w.Header().Set("X-Design-System-Revision", revision)
				w.Header().Set("X-Design-System-Inventory-Digest", "sha256:"+strings.Repeat("b", 64))
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("Content-Type", "image/svg+xml")
			w.Header().Set("ETag", `"`+revision+`-fixture`)
			w.Header().Set("X-Design-System-Revision", revision)
			w.Header().Set("X-Design-System-Inventory-Digest", "sha256:"+strings.Repeat("b", 64))
			_, _ = w.Write([]byte("<svg>selected</svg>"))
		default:
			t.Errorf("unexpected Registry path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	t.Setenv("DELPHI_BUILDER_REGISTRY_CUTOVER", "true")
	t.Setenv("DELPHI_BUILDER_REGISTRY_URL", upstream.URL)
	handler := registeredHandler(t.TempDir(), t.TempDir(), "")
	read := func(target string, headers map[string]string) *httptest.ResponseRecorder {
		t.Helper()
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, target, nil)
		request.Host = "localhost"
		for key, value := range headers {
			request.Header.Set(key, value)
		}
		handler.ServeHTTP(response, request)
		return response
	}
	local := read("/api/local-preview?company_id=belluga&project_id=builder", nil)
	if local.Code != http.StatusOK || !strings.Contains(local.Body.String(), `"consumer_readiness":"pending"`) {
		t.Fatalf("local preview should remain pending independently: %d %s", local.Code, local.Body.String())
	}
	selected := read("/api/design-system?company_id=belluga&project_id=builder", map[string]string{"Authorization": "Bearer private", "Cookie": "session=private"})
	if selected.Code != http.StatusOK || !strings.Contains(selected.Body.String(), revision) || !strings.Contains(selected.Body.String(), `"outcome":"go"`) {
		t.Fatalf("independent Design System read: %d %s", selected.Code, selected.Body.String())
	}
	asset := read("/api/design-system/assets?company_id=belluga&project_id=builder&revision="+revision+"&path=design%2Fsystem%2Fassets%2Fmark.svg", map[string]string{"If-None-Match": `"` + revision + `-fixture`})
	if asset.Code != http.StatusNotModified || asset.Header().Get("ETag") != `"`+revision+`-fixture` || asset.Header().Get("X-Design-System-Revision") != revision || asset.Header().Get("Content-Security-Policy") != "default-src 'none'; sandbox" {
		t.Fatalf("asset freshness metadata was not forwarded: %d headers=%v body=%s", asset.Code, asset.Header(), asset.Body.String())
	}
	activeAsset := read("/api/design-system/assets?company_id=belluga&project_id=builder&revision="+revision+"&path=design%2Fsystem%2Fassets%2Fmark.svg", nil)
	if activeAsset.Code != http.StatusOK || activeAsset.Body.String() != "<svg>selected</svg>" || activeAsset.Header().Get("Content-Length") != fmt.Sprint(len("<svg>selected</svg>")) || activeAsset.Header().Get("X-Content-Type-Options") != "nosniff" || activeAsset.Header().Get("Content-Security-Policy") != "default-src 'none'; sandbox" {
		t.Fatalf("asset response not safely bounded: %d headers=%v body=%q", activeAsset.Code, activeAsset.Header(), activeAsset.Body.String())
	}
	if !strings.Contains(strings.Join(registryPaths, "\n"), "/internal/v1/companies/belluga/projects/builder/design-system/"+revision+"/assets/design/system/assets/mark.svg") {
		t.Fatalf("asset did not use exact selected SHA path: %v", registryPaths)
	}
}

func TestDesignSystemAPIForwardsOnlySafeBoundedRegistryTeach(t *testing.T) {
	for _, test := range []struct {
		name, teach string
		want        string
	}{
		{"field teach", "TEACH: revision must be the exact selected SHA", "TEACH: revision must be the exact selected SHA"},
		{"private path is redacted", "TEACH: /private/registry/config is unavailable", "TEACH: A leitura atual do Design System está indisponível. Verifique a Registry e tente novamente."},
	} {
		t.Run(test.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "unavailable", "teach": test.teach})
			}))
			defer upstream.Close()
			consumer := &registeredPreview{registryURL: upstream.URL, client: upstream.Client()}
			response := httptest.NewRecorder()
			consumer.designSystemAPIHandler(response, httptest.NewRequest(http.MethodGet, "/api/design-system?company_id=belluga&project_id=builder", nil))
			if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), test.want) || strings.Contains(response.Body.String(), "/private/") {
				t.Fatalf("unsafe or missing Registry TEACH: %d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestDesignSystemAPIRejectsAmbiguousIdentityRevisionAndAssetPath(t *testing.T) {
	var calls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }))
	defer upstream.Close()
	consumer := &registeredPreview{registryURL: upstream.URL, client: upstream.Client()}
	handler := http.HandlerFunc(consumer.designSystemAPIHandler)
	for _, target := range []string{
		"/api/design-system?company_id=belluga&project_id=builder&project_id=other",
		"/api/design-system?company_id=belluga&project_id=builder&revision=HEAD",
		"/api/design-system/assets?company_id=belluga&project_id=builder&revision=" + strings.Repeat("a", 40) + "&path=..%2Fsecret",
		"/api/design-system/assets?company_id=belluga&project_id=builder&revision=" + strings.Repeat("a", 40) + "&path=x&path=y",
	} {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, target, nil)
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Errorf("unsafe selector accepted %s: %d %s", target, response.Code, response.Body.String())
		}
	}
	if calls != 0 {
		t.Fatalf("invalid requests reached Registry %d times", calls)
	}
}

func TestDesignSystemAPIRejectsRegistryIdentityMismatch(t *testing.T) {
	body := designSystemResponseFixture("other-company", "builder", strings.Repeat("a", 40))
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(body) }))
	defer upstream.Close()
	consumer := &registeredPreview{registryURL: upstream.URL, client: upstream.Client()}
	response := httptest.NewRecorder()
	consumer.designSystemAPIHandler(response, httptest.NewRequest(http.MethodGet, "/api/design-system?company_id=belluga&project_id=builder", nil))
	if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), "other-company") {
		t.Fatalf("Registry identity mismatch was leaked or accepted: %d %s", response.Code, response.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil || payload["teach"] == nil {
		t.Fatalf("expected sanitized TEACH: %s", response.Body.String())
	}
}

func TestDesignSystemAPIRejectsAssetFromDifferentRevision(t *testing.T) {
	const requestedRevision = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/assets/") {
			t.Errorf("unexpected Registry path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "image/svg+xml")
		w.Header().Set("ETag", `"fixture"`)
		w.Header().Set("X-Design-System-Revision", strings.Repeat("b", 40))
		w.Header().Set("X-Design-System-Inventory-Digest", "sha256:"+strings.Repeat("b", 64))
		_, _ = w.Write([]byte("<svg>wrong revision</svg>"))
	}))
	defer upstream.Close()
	consumer := &registeredPreview{registryURL: upstream.URL, client: upstream.Client()}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/design-system/assets?company_id=belluga&project_id=builder&revision="+requestedRevision+"&path=design%2Fsystem%2Fassets%2Fmark.svg", nil)
	consumer.designSystemAPIHandler(response, request)
	if response.Code != http.StatusServiceUnavailable || strings.Contains(response.Body.String(), "wrong revision") {
		t.Fatalf("asset from another revision was accepted or leaked: %d headers=%v body=%q", response.Code, response.Header(), response.Body.String())
	}
}
