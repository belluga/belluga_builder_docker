package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReadProxyTeachBoundsCompleteEnvelopeAndProjectsSafeFields(t *testing.T) {
	base := `{"error":"upstream_conflict","state":"blocked","todo_revision":9,"teach":"TEACH: Reload the current item.","teach_detail":{"schema_version":"1","code":"todo_conflict","field_path":"expected_revision","next_action":"Reload the current item before editing.","accepted_values":["Planning","Doing"],"effects":"unknown","retry":"read_reassess"}`
	for _, tc := range []struct {
		name  string
		body  []byte
		valid bool
	}{
		{"exact envelope bound including newline", []byte(base + `,"padding":"` + strings.Repeat("x", (64<<10)-len(base)-len(`,"padding":""}`)-1) + `"}` + "\n"), true},
		{"oversize envelope", []byte(base + `,"padding":"` + strings.Repeat("x", 64<<10) + `"}`), false},
		{"trailing JSON", append([]byte(base+`}`), []byte(` {}`)...), false},
		{"duplicate key", []byte(strings.Replace(base+`}`, `"code":"todo_conflict"`, `"code":"todo_conflict","code":"other_code"`, 1)), false},
		{"invalid UTF8", append([]byte(base+`}`), 0xff), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := readProxyTeach(bytes.NewReader(tc.body))
			if ok != tc.valid {
				t.Fatalf("valid=%t want=%t", ok, tc.valid)
			}
			if ok && (got.TeachDetail == nil || got.TeachDetail.FieldPath != "expected_revision" || got.TODORevision == nil || *got.TODORevision != 9) {
				t.Fatalf("projected envelope = %#v", got)
			}
		})
	}
}

func TestProxyTeachFiltersEveryTextFieldWithoutDroppingSafeDetails(t *testing.T) {
	body := `{"state":"invalid","teach":"TEACH: Review the published artifact.","teach_detail":{"schema_version":"1","code":"artifact_invalid","field_path":"payload.schemas[0].fields","record_id":"record-1","next_action":"Review the published artifact and re-submit it.","object_purpose":"public document","accepted_values":["safe-value","https://private.example/token=secret"],"required_fields":["schema_version","payload"],"accepted_fields":["title","sections"],"not_persisted_fields":["draft"],"operation_id":"op-1","effects":"unknown","retry":"read_reassess"}}`
	got, ok := readProxyTeach(strings.NewReader(body))
	if !ok || got.TeachDetail == nil {
		t.Fatalf("TEACH detail missing: %#v %t", got, ok)
	}
	if got.TeachDetail.FieldPath != "payload.schemas[0].fields" || len(got.TeachDetail.AcceptedValues) != 0 {
		t.Fatalf("unknown accepted value escaped finite public enum allowlist: %#v", got.TeachDetail)
	}
	var out bytes.Buffer
	w := httptest.NewRecorder()
	writeProxyTeachError(w, http.StatusConflict, "public_conflict", "TEACH: Reload current state.", got)
	if w.Code != http.StatusConflict || !json.Valid(w.Body.Bytes()) || w.Body.Len() > maxProxyTeachEnvelope || strings.Contains(w.Body.String(), "private.example") || strings.Contains(w.Body.String(), "secret") {
		t.Fatalf("unsafe proxy error: %d %s", w.Code, w.Body.String())
	}
	_, _ = io.Copy(&out, w.Body)
	var envelope proxyTeachEnvelope
	if json.Unmarshal(out.Bytes(), &envelope) != nil || envelope.Error != "public_conflict" || envelope.TeachDetail == nil || envelope.TeachDetail.FieldPath != "payload.schemas[0].fields" {
		t.Fatalf("compatibility projection lost detail: %s", out.String())
	}
}

func TestProxyTeachDetailPublicDTODropsPrivateFieldsOnSuccessPayloads(t *testing.T) {
	raw := `{"schema_version":"1","code":"portfolio_invalid","field_path":"payload.schemas[0].fields","record_id":"private-record","operation_id":"private-operation","object_purpose":"private purpose","next_action":"Review the public artifact and reload it.","accepted_values":["safe-value","https://private.example/path"]}`
	var detail proxyTeachDetail
	if err := json.Unmarshal([]byte(raw), &detail); err != nil {
		t.Fatal(err)
	}
	projected := sanitizeProxyTeachDetail(&detail, "portfolio_invalid", "")
	body, err := json.Marshal(projected)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "private-record") || strings.Contains(string(body), "private-operation") || strings.Contains(string(body), "private purpose") || strings.Contains(string(body), "private.example") {
		t.Fatalf("private fields escaped the 2xx projection: %s", body)
	}
	if !strings.Contains(string(body), `"schema_version":"1"`) || !strings.Contains(string(body), `"field_path":"payload.schemas[0].fields"`) || !strings.Contains(string(body), `"next_action":"Review the public artifact and reload it."`) {
		t.Fatalf("valid public detail was not preserved: %s", body)
	}
}

func TestRoadmapProxyDoesNotForwardRawRegistryClientErrors(t *testing.T) {
	const company, project = "company-a", "project-a"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/internal/v1/catalog":
			_, _ = w.Write([]byte(`{"schema_version":"1","companies":[{"id":"company-a","name":"Company A"}],"projects":[{"id":"project-a","name":"Project A","company_id":"company-a"}]}`))
		case "/internal/v1/companies/company-a/projects/project-a/roadmap":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"path=/private/config","teach":"TEACH: invalid request","teach_detail":{"schema_version":"1","code":"bad_request","field_path":"request.query","next_action":"Correct the request and reload."},"secret":"never-forward"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	p := &registeredPreview{registryURL: upstream.URL, client: upstream.Client()}
	w := httptest.NewRecorder()
	p.roadmapAPIHandler(w, httptest.NewRequest(http.MethodGet, "/api/roadmap?company_id="+company+"&project_id="+project, nil))
	if w.Code != http.StatusBadRequest || strings.Contains(w.Body.String(), "/private/config") || strings.Contains(w.Body.String(), "never-forward") || !strings.Contains(w.Body.String(), `"teach_detail"`) || !strings.Contains(w.Body.String(), `"error":"roadmap_request_rejected"`) {
		t.Fatalf("Registry error was not projected safely: %d %s", w.Code, w.Body.String())
	}
}

func TestRoadmapProxyPreservesSafeRegistryErrorCodesAndFallsBackForHostileCode(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		status     int
		want       string
	}{
		{name: "invalid", code: "invalid", status: http.StatusBadRequest, want: "invalid"},
		{name: "not found", code: "not_found", status: http.StatusNotFound, want: "not_found"},
		{name: "conflict", code: "conflict", status: http.StatusConflict, want: "conflict"},
		{name: "hostile", code: "path=/private/config", status: http.StatusBadRequest, want: "roadmap_request_rejected"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/internal/v1/catalog":
					_, _ = io.WriteString(w, `{"schema_version":"1","companies":[{"id":"company-a","name":"Company A"}],"projects":[{"id":"project-a","name":"Project A","company_id":"company-a"}]}`)
				case "/internal/v1/companies/company-a/projects/project-a/roadmap":
					w.WriteHeader(tc.status)
					_, _ = io.WriteString(w, `{"error":`+mustJSONString(tc.code)+`,"teach":"TEACH: Review the current Roadmap filters.","teach_detail":{"schema_version":"1","code":"roadmap_invalid","field_path":"filters","next_action":"Review the current Roadmap filters."},"private":"never-forward"}`)
				default:
					http.NotFound(w, r)
				}
			}))
			defer upstream.Close()
			p := &registeredPreview{registryURL: upstream.URL, client: upstream.Client()}
			w := httptest.NewRecorder()
			p.roadmapAPIHandler(w, httptest.NewRequest(http.MethodGet, "/api/roadmap?company_id=company-a&project_id=project-a", nil))
			var got proxyTeachEnvelope
			if json.Unmarshal(w.Body.Bytes(), &got) != nil || w.Code != tc.status || got.Error != tc.want || got.TeachDetail == nil || got.TeachDetail.Code != "roadmap_invalid" || got.TeachDetail.NextAction == "" || strings.Contains(w.Body.String(), "private") {
				t.Fatalf("Roadmap Registry error compatibility: status=%d error=%q body=%s", w.Code, got.Error, w.Body.String())
			}
		})
	}
}

func TestLandingInvalidManifestKeepsSafeOwnerFactsAndNormalizesUnsafeAction(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/v1/companies/company/projects/project/landing" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"invalid","teach":"TEACH: Update the published Landing manifest and retry.","teach_detail":{"schema_version":"1","code":"landing_invalid","field_path":"manifest_version","next_action":"Correct design/landing/manifest.json and retry.","revision":7,"effects":"not_applicable","retry":"read_reassess"}}`)
	}))
	defer upstream.Close()
	p := &registeredPreview{registryURL: upstream.URL, client: upstream.Client()}
	w := httptest.NewRecorder()
	p.landingAPIHandler(w, httptest.NewRequest(http.MethodGet, "/api/landing?company_id=company&project_id=project", nil))
	var got struct {
		State       string            `json:"state"`
		Teach       string            `json:"teach"`
		TeachDetail *proxyTeachDetail `json:"teach_detail"`
	}
	if json.Unmarshal(w.Body.Bytes(), &got) != nil || w.Code != http.StatusOK || got.State != "invalid" || got.TeachDetail == nil || !safeProxyTeachDetail(*got.TeachDetail) || got.TeachDetail.Code != "landing_invalid" || got.TeachDetail.FieldPath != "manifest_version" || got.TeachDetail.Revision == nil || *got.TeachDetail.Revision != 7 || got.TeachDetail.Effects != "not_applicable" || got.TeachDetail.Retry != "read_reassess" || strings.Contains(got.TeachDetail.NextAction, "/") || !strings.Contains(got.Teach, "Update the published Landing manifest") {
		t.Fatalf("invalid Landing response lost safe owner facts or emitted unsafe action: status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestLandingPortfolioAndPrototypeProjectTEACHDetails(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":"selection_changed","teach":"TEACH: Reload the current artifact.","teach_detail":{"schema_version":"1","code":"artifact_conflict","field_path":"expected_revision","next_action":"Reload the current artifact before opening it.","operation_id":"private-op","object_purpose":"private record","accepted_values":["safe","https://private.example/path"]},"authorization":"secret"}`))
	}))
	defer upstream.Close()
	p := &registeredPreview{registryURL: upstream.URL, client: upstream.Client()}
	for _, tc := range []struct {
		name, path string
		handler    func(http.ResponseWriter, *http.Request)
		code       string
	}{
		{"landing", "/api/landing?company_id=company-a&project_id=project-a", p.landingAPIHandler, "landing_selection_changed"},
		{"portfolio", "/api/portfolios?company_id=company-a", p.portfolioAPIHandler, "portfolio_selection_changed"},
		{"prototype", "/api/prototypes?company_id=company-a&project_id=project-a", p.prototypeAPIHandler, "prototype_selection_changed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			tc.handler(w, httptest.NewRequest(http.MethodGet, tc.path, nil))
			if w.Code != http.StatusConflict || strings.Contains(w.Body.String(), "private-op") || strings.Contains(w.Body.String(), "private record") || strings.Contains(w.Body.String(), "private.example") || strings.Contains(w.Body.String(), "secret") || !strings.Contains(w.Body.String(), `"error":"`+tc.code+`"`) || !strings.Contains(w.Body.String(), `"teach_detail"`) {
				t.Fatalf("%s projection: %d %s", tc.name, w.Code, w.Body.String())
			}
		})
	}
}

func TestPrototypeHandlersReplaceBlankOptionalTEACHActionsWithValidV1Fallback(t *testing.T) {
	const revision = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const digest = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	for _, tc := range []struct {
		name   string
		status int
	}{
		{"success", http.StatusOK},
		{"public error", http.StatusConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.status != http.StatusOK {
					w.WriteHeader(tc.status)
					_, _ = io.WriteString(w, `{"teach_detail":{"schema_version":"1","code":"prototype_selection_changed","next_action":"   ","operation_id":"private-operation"}}`)
					return
				}
				writeJSON(w, http.StatusOK, map[string]any{
					"repository_id": "example/project", "publication_branch": "main", "binding_revision": 3,
					"revision": revision, "evaluator_version": "prototype-v3", "inventory_digest": "sha256:" + digest,
					"knowledge_digest": digest, "items": []any{},
					"teach_detail": map[string]any{"schema_version": "1", "code": "prototype_diagnostic", "next_action": "   ", "operation_id": "private-operation"},
				})
			}))
			defer upstream.Close()
			p := prototypeTestConsumer(t, upstream)
			w := httptest.NewRecorder()
			p.prototypeAPIHandler(w, httptest.NewRequest(http.MethodGet, "/api/prototypes?company_id=company&project_id=project", nil))
			var got struct {
				TeachDetail *proxyTeachDetail `json:"teach_detail"`
			}
			if json.Unmarshal(w.Body.Bytes(), &got) != nil || w.Code != tc.status || got.TeachDetail == nil || !safeProxyTeachDetail(*got.TeachDetail) || strings.TrimSpace(got.TeachDetail.NextAction) == "" || strings.Contains(w.Body.String(), "private-operation") {
				t.Fatalf("blank optional detail escaped a real %s handler: status=%d body=%s", tc.name, w.Code, w.Body.String())
			}
		})
	}
}
