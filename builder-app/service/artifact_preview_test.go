package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRegisteredHandlerRetiresSnapshotRoutes(t *testing.T) {
	for _, route := range []struct {
		path string
		body string
	}{
		{path: "/api/local-preview?company_id=company-a&project_id=project-a", body: "project_snapshot_retired"},
		{path: "/snapshot/company-a/project-a/" + strings.Repeat("a", 64) + "/design/landing/index.html", body: "project_snapshot_retired"},
		{path: "/api/local-todo-source?company_id=company-a&project_id=project-a", body: "todo_source_retired"},
	} {
		t.Run(route.path, func(t *testing.T) {
			handler := registeredHandler("/unused", t.TempDir(), "")
			request := httptest.NewRequest(http.MethodGet, route.path, nil)
			request.Host = "localhost"
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusGone || !strings.Contains(response.Body.String(), route.body) {
				t.Fatalf("retired route status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestRoadmapHandlerProjectsNestedTODOTeachDetailsAndPreservesLegacyPayload(t *testing.T) {
	const detailPrivate = `,"record_id":"private-record","operation_id":"private-operation","object_purpose":"private purpose"`
	todo := func(id, legacyTeach, detail string) string {
		return fmt.Sprintf(`{"id":%q,"todo_revision":7,"title":"Title %s","path":"todos/%s.md","kind":"delivery","record_state":"authored","authored_delivery_stage":"In-Progress","delivery_stage_state":"unverified","delivery_stage_teach":%s,"teach_detail":%s,"qualifier_state":"none","legacy_extension":"keep-%s"}`, id, id, id, mustJSONString(legacyTeach), detail, id)
	}
	validDetail := `{"schema_version":"1","code":"todo_stage_unverified","field_path":"delivery_stage","accepted_values":["planned","identifier-shaped-secret"],"next_action":"Reload the current Roadmap before scheduling.","effects":"not_applicable"` + detailPrivate + `}`
	unsafeAction := `{"schema_version":"1","code":"todo_stage_unverified","field_path":"delivery_stage","next_action":"https://private.example/token=secret"` + detailPrivate + `}`
	oversizedAction := `{"schema_version":"1","code":"todo_stage_unverified","field_path":"delivery_stage","next_action":"` + strings.Repeat("x", 513) + `"` + detailPrivate + `}`
	oversizedValues := make([]string, 65)
	for i := range oversizedValues {
		oversizedValues[i] = fmt.Sprintf("identifier-shaped-secret-%d", i)
	}
	oversizedListJSON, err := json.Marshal(oversizedValues)
	if err != nil {
		t.Fatal(err)
	}
	oversizedList := `{"schema_version":"1","code":"todo_stage_unverified","field_path":"delivery_stage","accepted_values":` + string(oversizedListJSON) + `,"next_action":"Review the current Roadmap before scheduling."` + detailPrivate + `}`
	unsafeLegacy := `TEACH: /private/config token=secret`
	roadmap := `{"schema_version":"builder-roadmap-projection-v3","project_id":"project-a","focus_release_id":"","epics":[{"id":"epic-a","name":"Epic A","releases":[{"id":"release-a","name":"Release A","todos":[` + todo("todo-epic", "TEACH: Refresh the Roadmap.", unsafeAction) + `]}]}],"backlog":{"todos":[` + todo("todo-backlog", "TEACH: Review the current TODO.", oversizedAction) + `],"folders":[{"name":"Root","path":"todos/root","folders":[{"name":"Nested","path":"todos/root/nested","folders":[],"todos":[` + todo("todo-nested", unsafeLegacy, unsafeAction) + `]}],"todos":[` + todo("todo-folder", "TEACH: Check the published stage.", validDetail) + `,` + todo("todo-list", "TEACH: Review the current TODO.", string(oversizedList)) + `]}]},"unclassified":[],"diagnostics":[]}`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/internal/v1/catalog":
			_, _ = w.Write([]byte(`{"schema_version":"1","companies":[{"id":"company-a","name":"Company A"}],"projects":[{"id":"project-a","name":"Project A","company_id":"company-a"}]}`))
		case "/internal/v1/companies/company-a/projects/project-a/roadmap":
			_, _ = w.Write([]byte(roadmap))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	p := &registeredPreview{registryURL: upstream.URL, client: upstream.Client()}
	w := httptest.NewRecorder()
	p.roadmapAPIHandler(w, httptest.NewRequest(http.MethodGet, "/api/roadmap?company_id=company-a&project_id=project-a", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("roadmap status=%d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, secret := range []string{"private-record", "private-operation", "private purpose", "private.example", "token=secret", strings.Repeat("x", 513), "identifier-shaped-secret"} {
		if strings.Contains(body, secret) {
			t.Fatalf("unsafe Roadmap detail escaped public projection (%q): %s", secret, body)
		}
	}
	for _, preserved := range []string{`"epic-a"`, `"release-a"`, `"todo-epic"`, `"todo-backlog"`, `"todo-folder"`, `"todo-nested"`, `"legacy_extension":"keep-todo-epic"`, `"delivery_stage_state":"unverified"`, `"next_action":"Reload the current Roadmap before scheduling."`} {
		if !strings.Contains(body, preserved) {
			t.Fatalf("Roadmap projection dropped legacy/safe field %s: %s", preserved, body)
		}
	}
	var projected map[string]any
	if json.Unmarshal(w.Body.Bytes(), &projected) != nil || projected["schema_version"] != "builder-roadmap-projection-v3" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Roadmap envelope semantics changed: status=%d headers=%v body=%s", w.Code, w.Header(), body)
	}
	type roadmapTODO struct {
		ID          string            `json:"id"`
		TeachDetail *proxyTeachDetail `json:"teach_detail"`
	}
	type roadmapFolder struct {
		Todos   []roadmapTODO   `json:"todos"`
		Folders []roadmapFolder `json:"folders"`
	}
	var emitted struct {
		Epics []struct {
			Releases []struct {
				Todos []roadmapTODO `json:"todos"`
			} `json:"releases"`
		} `json:"epics"`
		Backlog struct {
			Todos   []roadmapTODO   `json:"todos"`
			Folders []roadmapFolder `json:"folders"`
		} `json:"backlog"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &emitted); err != nil {
		t.Fatal(err)
	}
	byID := map[string]roadmapTODO{}
	var visitFolder func(roadmapFolder)
	visitFolder = func(folder roadmapFolder) {
		for _, item := range folder.Todos {
			byID[item.ID] = item
		}
		for _, nested := range folder.Folders {
			visitFolder(nested)
		}
	}
	for _, epic := range emitted.Epics {
		for _, release := range epic.Releases {
			for _, item := range release.Todos {
				byID[item.ID] = item
			}
		}
	}
	for _, item := range emitted.Backlog.Todos {
		byID[item.ID] = item
	}
	for _, folder := range emitted.Backlog.Folders {
		visitFolder(folder)
	}
	for _, id := range []string{"todo-epic", "todo-backlog", "todo-folder", "todo-nested", "todo-list"} {
		item, found := byID[id]
		if !found || item.TeachDetail == nil || !safeProxyTeachDetail(*item.TeachDetail) {
			t.Fatalf("Roadmap TODO %s lost its individually valid projected diagnostic: %#v", id, item)
		}
	}
}

func TestRoadmapHandlerAddsTEACHFallbackOnlyForExistingStageGuidance(t *testing.T) {
	legacyTODO := func(id, state, deliveryStage, guidance, detail string) string {
		teachField := ""
		if guidance != "" {
			teachField = `,"delivery_stage_teach":` + mustJSONString(guidance)
		}
		return fmt.Sprintf(`{"id":%q,"todo_revision":3,"title":%q,"path":%q,"kind":"delivery","record_state":"authored","authored_delivery_stage":"Planning","delivery_stage":%q,"delivery_stage_state":%q%s,"qualifier_state":"none"%s}`, id, id, "todos/"+id+".md", deliveryStage, state, teachField, detail)
	}
	roadmap := `{"schema_version":"builder-roadmap-projection-v3","project_id":"project-a","focus_release_id":"","epics":[{"id":"epic-a","name":"Epic A","releases":[{"id":"release-a","name":"Release A","todos":[` + legacyTODO("todo-release-missing", "unverified", "", "TEACH: Review the released TODO evidence.", "") + `]}]}],"backlog":{"todos":[` + legacyTODO("todo-backlog-null", "unverified", "", "TEACH: Review the backlog TODO evidence.", `,"teach_detail":null`) + `],"folders":[{"name":"Root","path":"todos/root","todos":[` + legacyTODO("todo-healthy", "verified", "Planning", "", "") + `],"folders":[{"name":"Nested","path":"todos/root/nested","todos":[` + legacyTODO("todo-nested-missing", "unverified", "", "TEACH: Review the nested TODO evidence.", "") + `],"folders":[]}]}]},"unclassified":[],"diagnostics":[]}`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/internal/v1/catalog":
			_, _ = w.Write([]byte(`{"schema_version":"1","companies":[{"id":"company-a","name":"Company A"}],"projects":[{"id":"project-a","name":"Project A","company_id":"company-a"}]}`))
		case "/internal/v1/companies/company-a/projects/project-a/roadmap":
			_, _ = io.WriteString(w, roadmap)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	p := &registeredPreview{registryURL: upstream.URL, client: upstream.Client()}
	w := httptest.NewRecorder()
	p.roadmapAPIHandler(w, httptest.NewRequest(http.MethodGet, "/api/roadmap?company_id=company-a&project_id=project-a", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("Roadmap legacy diagnostic fixture was not accepted: status=%d body=%s", w.Code, w.Body.String())
	}
	type item struct {
		ID     string            `json:"id"`
		Legacy string            `json:"delivery_stage_teach"`
		Detail *proxyTeachDetail `json:"teach_detail"`
	}
	type folder struct {
		TODOs   []item   `json:"todos"`
		Folders []folder `json:"folders"`
	}
	var got struct {
		Epics []struct {
			Releases []struct {
				TODOs []item `json:"todos"`
			} `json:"releases"`
		} `json:"epics"`
		Backlog struct {
			TODOs   []item   `json:"todos"`
			Folders []folder `json:"folders"`
		} `json:"backlog"`
	}
	if json.Unmarshal(w.Body.Bytes(), &got) != nil {
		t.Fatalf("Roadmap response failed to decode: %s", w.Body.String())
	}
	items := map[string]item{}
	for _, epic := range got.Epics {
		for _, release := range epic.Releases {
			for _, row := range release.TODOs {
				items[row.ID] = row
			}
		}
	}
	for _, row := range got.Backlog.TODOs {
		items[row.ID] = row
	}
	var visit func([]folder)
	visit = func(folders []folder) {
		for _, f := range folders {
			for _, row := range f.TODOs {
				items[row.ID] = row
			}
			visit(f.Folders)
		}
	}
	visit(got.Backlog.Folders)
	for id, guidance := range map[string]string{"todo-release-missing": "TEACH: Review the released TODO evidence.", "todo-backlog-null": "TEACH: Review the backlog TODO evidence.", "todo-nested-missing": "TEACH: Review the nested TODO evidence."} {
		row, found := items[id]
		if !found || row.Legacy != guidance || row.Detail == nil || !safeProxyTeachDetail(*row.Detail) || row.Detail.Code != "roadmap_todo_diagnostic" || strings.TrimSpace(row.Detail.NextAction) == "" || row.Detail.Effects != "" || row.Detail.Retry != "" || row.Detail.Revision != nil {
			t.Errorf("Roadmap TODO %s failed the legacy-guidance fallback contract: %#v", id, row)
		}
	}
	healthy, found := items["todo-healthy"]
	if !found || healthy.Detail != nil || healthy.Legacy != "" {
		t.Errorf("ordinary healthy TODO gained an unsolicited diagnostic: %#v", healthy)
	}
}

func TestRoadmapProjectionPreservesHTMLRichSuccessBodyBudget(t *testing.T) {
	roadmap := `{"schema_version":"builder-roadmap-projection-v3","project_id":"project-a","focus_release_id":"","epics":[],"backlog":{"todos":[],"folders":[]},"unclassified":[],"diagnostics":[],"extension":"` + strings.Repeat("<", 3<<20) + `"}`
	if len(roadmap) >= 4<<20 {
		t.Fatalf("fixture must remain within original Roadmap success bound: %d", len(roadmap))
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/internal/v1/catalog":
			_, _ = w.Write([]byte(`{"schema_version":"1","companies":[{"id":"company-a","name":"Company A"}],"projects":[{"id":"project-a","name":"Project A","company_id":"company-a"}]}`))
		case "/internal/v1/companies/company-a/projects/project-a/roadmap":
			_, _ = w.Write([]byte(roadmap))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	p := &registeredPreview{registryURL: upstream.URL, client: upstream.Client()}
	w := httptest.NewRecorder()
	p.roadmapAPIHandler(w, httptest.NewRequest(http.MethodGet, "/api/roadmap?company_id=company-a&project_id=project-a", nil))
	if w.Code != http.StatusOK || len(w.Body.Bytes()) >= 4<<20 || !strings.Contains(w.Body.String(), `"extension":"`+strings.Repeat("<", 32)) {
		t.Fatalf("Roadmap projection changed a valid HTML-rich success response: status=%d body_size=%d", w.Code, w.Body.Len())
	}
}

func TestRoadmapProjectionPreservesDeepExtensionsAndLargeIntegerPrecision(t *testing.T) {
	var makeFolder func(int, string) map[string]any
	makeFolder = func(level int, parent string) map[string]any {
		folderPath := "todos/root"
		name := "Root"
		if level > 0 {
			folderPath = parent + "/nested"
			name = fmt.Sprintf("Nested %d", level)
		}
		children := []any{}
		if level < 128 {
			children = append(children, makeFolder(level+1, folderPath))
		}
		return map[string]any{"name": name, "path": folderPath, "todos": []any{}, "folders": children, "extension": map[string]any{"large_integer": json.Number("9007199254740993"), "label": "<& preserved"}}
	}
	roadmapBytes, err := json.Marshal(map[string]any{
		"schema_version": "builder-roadmap-projection-v3", "project_id": "project-a", "focus_release_id": "", "epics": []any{},
		"backlog": map[string]any{"todos": []any{}, "folders": []any{makeFolder(0, "")}}, "unclassified": []any{}, "diagnostics": []any{},
	})
	if err != nil || len(roadmapBytes) >= 4<<20 {
		t.Fatalf("fixture must be valid and below the existing Roadmap bound: size=%d err=%v", len(roadmapBytes), err)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/internal/v1/catalog":
			_, _ = w.Write([]byte(`{"schema_version":"1","companies":[{"id":"company-a","name":"Company A"}],"projects":[{"id":"project-a","name":"Project A","company_id":"company-a"}]}`))
		case "/internal/v1/companies/company-a/projects/project-a/roadmap":
			_, _ = w.Write(roadmapBytes)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	p := &registeredPreview{registryURL: upstream.URL, client: upstream.Client()}
	w := httptest.NewRecorder()
	p.roadmapAPIHandler(w, httptest.NewRequest(http.MethodGet, "/api/roadmap?company_id=company-a&project_id=project-a", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"large_integer":9007199254740993`) || !strings.Contains(w.Body.String(), `"label":"<& preserved"`) {
		t.Fatalf("nested extension or integer precision changed: status=%d body_size=%d", w.Code, w.Body.Len())
	}
}

func TestRoadmapProjectionKeepsRawUnicodeExtensionWithinAcceptedInputBudget(t *testing.T) {
	extension := strings.Repeat("\u2028", 1<<20)
	roadmap := `{"schema_version":"builder-roadmap-projection-v3","project_id":"project-a","focus_release_id":"","epics":[],"backlog":{"todos":[],"folders":[]},"unclassified":[],"diagnostics":[],"extension":"` + extension + `"}`
	if len(roadmap) >= 4<<20 {
		t.Fatalf("raw Unicode fixture must remain within the existing input bound: %d", len(roadmap))
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/internal/v1/catalog":
			_, _ = w.Write([]byte(`{"schema_version":"1","companies":[{"id":"company-a","name":"Company A"}],"projects":[{"id":"project-a","name":"Project A","company_id":"company-a"}]}`))
		case "/internal/v1/companies/company-a/projects/project-a/roadmap":
			_, _ = io.WriteString(w, roadmap)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	p := &registeredPreview{registryURL: upstream.URL, client: upstream.Client()}
	w := httptest.NewRecorder()
	p.roadmapAPIHandler(w, httptest.NewRequest(http.MethodGet, "/api/roadmap?company_id=company-a&project_id=project-a", nil))
	var projected struct {
		Extension string `json:"extension"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &projected) != nil || projected.Extension != extension {
		t.Fatalf("valid raw Unicode extension was rejected or changed: status=%d body_size=%d", w.Code, w.Body.Len())
	}
}

func mustJSONString(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func TestRegisteredHandlerPreservesRoadmapAndFamilyRoutes(t *testing.T) {
	roadmap := `{"schema_version":"builder-roadmap-projection-v3","project_id":"project-a","focus_release_id":"","epics":[],"backlog":{"todos":[],"folders":[]},"unclassified":[],"diagnostics":[]}`
	registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/internal/v1/catalog":
			_, _ = w.Write([]byte(`{"schema_version":"1","companies":[{"id":"company-a","name":"Company A"}],"projects":[{"id":"project-a","name":"Project A","company_id":"company-a"}]}`))
		case "/internal/v1/companies/company-a/projects/project-a/roadmap":
			_, _ = w.Write([]byte(roadmap))
		default:
			http.NotFound(w, r)
		}
	}))
	defer registry.Close()
	t.Setenv("DELPHI_BUILDER_REGISTRY_URL", registry.URL)

	handler := registeredHandler("/unused", t.TempDir(), "")
	for _, route := range []struct {
		path       string
		wantStatus int
		wantBody   string
	}{
		{path: "/api/catalog", wantStatus: http.StatusOK, wantBody: `"project-a"`},
		{path: "/api/roadmap?company_id=company-a&project_id=project-a", wantStatus: http.StatusOK, wantBody: `"project_id":"project-a"`},
		{path: "/api/prototypes", wantStatus: http.StatusBadRequest, wantBody: "prototype_request_invalid"},
		{path: "/api/design-system", wantStatus: http.StatusBadRequest, wantBody: "design_system_request_invalid"},
	} {
		t.Run(route.path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, route.path, nil)
			request.Host = "localhost"
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != route.wantStatus || !strings.Contains(response.Body.String(), route.wantBody) {
				t.Fatalf("route status=%d body=%s; wanted status=%d containing %q", response.Code, response.Body.String(), route.wantStatus, route.wantBody)
			}
		})
	}
}
