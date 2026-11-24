package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	fixtureSession = "11111111-1111-4111-8111-111111111111"
	fixtureToken   = "fixture-token-never-for-production-001"
	fixtureCompany = "company-id"
	fixtureProject = "project-id"
)

type authTransport struct {
	host    string
	headers http.Header
}

func (a authTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Host = a.host
	for key, values := range a.headers {
		r.Header[key] = slices.Clone(values)
	}
	return http.DefaultTransport.RoundTrip(r)
}

func sdkFixture(t *testing.T, upstream, version, choice string) (*mcp.ClientSession, string) {
	t.Helper()
	return sdkFixtureWithNegotiatedVersion(t, upstream, version, choice, version)
}

func sdkFixtureWithNegotiatedVersion(t *testing.T, upstream, version, choice, want string) (*mcp.ClientSession, string) {
	t.Helper()
	host := "127.0.0.1:8098"
	handler, err := newHandler(serviceConfig{
		origin: upstream, companies: []string{fixtureCompany},
		transport: transportConfig{bearer: fixtureToken, hosts: map[string]bool{host: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	headers := make(http.Header)
	headers.Set("Authorization", "Bearer "+fixtureToken)
	headers.Set(headerProvider, provider)
	headers.Set(headerSession, fixtureSession)
	if choice != "" {
		headers.Set(headerSelection, choice)
	}
	std := &http.Client{Transport: authTransport{host: host, headers: headers}}
	client := mcp.NewClient(&mcp.Implementation{Name: "builder-test", Version: "1"}, &mcp.ClientOptions{})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint: server.URL + "/mcp", HTTPClient: std, DisableStandaloneSSE: true,
	}, &mcp.ClientSessionOptions{ProtocolVersion: version})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	if got := session.InitializeResult().ProtocolVersion; got != want {
		t.Fatalf("negotiated %s, want %s", got, want)
	}
	return session, server.URL
}

func TestSDKRetriesHighestSupportedProtocolVersion(t *testing.T) {
	sdkFixtureWithNegotiatedVersion(t, "http://127.0.0.1:1", "2099-12-31", "", "2026-07-28")
}

func fixtureSelection() string {
	choice, _ := json.Marshal(map[string]string{
		"session_id": fixtureSession, "company_id": fixtureCompany, "project_id": fixtureProject,
		"project_name": "Fixture Project", "owner_reference": "fixture-user-selection",
	})
	return base64.RawURLEncoding.EncodeToString(choice)
}

type fixtureCall struct {
	Name          string          `json:"name"`
	Template      string          `json:"template"`
	Route         string          `json:"route"`
	Arguments     json.RawMessage `json:"arguments"`
	ExpectedQuery url.Values      `json:"expected_query"`
}

type registryCall struct {
	Route string
	Query url.Values
	Body  string
}

func TestSDKCatalogAndAllClosedRoutes(t *testing.T) {
	fixture, err := os.ReadFile("testdata/calls.json")
	if err != nil {
		t.Fatal(err)
	}
	var calls []fixtureCall
	if err := json.Unmarshal(fixture, &calls); err != nil {
		t.Fatal(err)
	}
	baseline, err := os.ReadFile("testdata/registry-matrix.json")
	if err != nil {
		t.Fatal(err)
	}
	var matrix struct {
		Rows []struct {
			Tool  string `json:"tool"`
			Route string `json:"route"`
		} `json:"rows"`
		NewRoutes []struct {
			Tool  string `json:"tool"`
			Route string `json:"route"`
		} `json:"new_routes"`
	}
	if err := json.Unmarshal(baseline, &matrix); err != nil {
		t.Fatal(err)
	}
	if len(matrix.Rows) != 94 || len(matrix.NewRoutes) != 2 {
		t.Fatalf("route freeze incomplete: %d + %d", len(matrix.Rows), len(matrix.NewRoutes))
	}
	wantNames := make(map[string]string)
	missing := make(map[string]bool)
	for _, row := range append(matrix.Rows, matrix.NewRoutes...) {
		if row.Tool != "" {
			wantNames[row.Tool] = strings.Fields(row.Route)[0]
			missing[row.Tool+" "+row.Route] = true
		}
	}
	if len(calls) != 73 || len(wantNames) != 70 {
		t.Fatalf("%d route cases, %d catalog names", len(calls), len(wantNames))
	}
	for _, fixture := range calls {
		entry := fixture.Name + " " + fixture.Template
		if !missing[entry] {
			t.Fatalf("unexpected or duplicate matrix fixture: %s", entry)
		}
		delete(missing, entry)
	}
	if len(missing) != 0 {
		t.Fatalf("uncovered approved routes: %v", missing)
	}
	specs, err := loadCatalog()
	if err != nil {
		t.Fatal(err)
	}
	published := make(map[string]any, len(specs))
	evaluators := make(map[string]*jsonschema.Resolved, len(specs))
	for _, spec := range specs {
		var advertised any
		if err := json.Unmarshal(spec.Schema, &advertised); err != nil {
			t.Fatal(err)
		}
		published[spec.Name] = advertised
		evaluator, err := spec.wire.root.Resolve(nil)
		if err != nil {
			t.Fatal(err)
		}
		evaluators[spec.Name] = evaluator
	}

	var state struct {
		sync.Mutex
		expectedRoute string
		upstreamCode  int
		forwarded     []registryCall
	}
	registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		state.Lock()
		defer state.Unlock()
		if r.Header.Get("Authorization") != "" || r.Header.Get(headerSelection) != "" ||
			r.Header.Get(headerSession) != fixtureSession || r.Header.Get(headerLane) != "conversation" ||
			r.Header.Get(headerProvider) != provider {
			t.Error("invalid Registry network trust headers")
		}
		w.Header().Set("Content-Type", "application/json")
		actualRoute := r.Method + " " + r.URL.EscapedPath()
		body, _ := io.ReadAll(r.Body)
		state.forwarded = append(state.forwarded, registryCall{actualRoute, r.URL.Query(), string(body)})
		if state.expectedRoute == "POST /internal/v1/companies/company-id/projects" && strings.HasPrefix(actualRoute, "GET ") {
			if strings.Contains(actualRoute, "/sessions/") {
				w.WriteHeader(http.StatusNotFound)
				fmt.Fprint(w, `{"error":"not_found"}`)
			} else {
				fmt.Fprint(w, `{"match_state":"unique","items":[{"id":"company-id","name":"Fixture Company"}]}`)
			}
			return
		}
		if state.upstreamCode != 0 {
			w.WriteHeader(state.upstreamCode)
			fmt.Fprint(w, `{"error":"conflict","state":"pending","teach":"Read current owner record.","todo_revision":9007199254740993,"teach_detail":{"schema_version":"1","code":"registry_conflict","field_path":"expected_revision","next_action":"Read current owner record.","revision":9223372036854775807,"effects":"none","retry":"read_reassess","internal_diagnostic":"PRIVATE"},"binding":{"workspace_root":"PRIVATE"},"stderr":"PRIVATE"}`)
			return
		}
		switch {
		case strings.Contains(actualRoute, "/internal/v1/sessions/"):
			if r.Method == http.MethodPost {
				w.WriteHeader(http.StatusCreated)
			}
			fmt.Fprint(w, `{"type":"codex","external_id":"`+fixtureSession+`","binding":{"company_id":"company-id","project_id":"project-id"},"owner_reference":"fixture-user-selection"}`)
		case actualRoute == "GET /internal/v1/companies/company-id/projects/project-id" || actualRoute == "POST /internal/v1/companies/company-id/projects":
			fmt.Fprint(w, `{"id":"project-id","company_id":"company-id","name":"Fixture Project","binding":{"workspace_root":"PRIVATE"},"preparation_diagnostic":"PRIVATE","roadmap_revision":9223372036854775807}`)
		default:
			fmt.Fprint(w, `{"state":"pending","revision":9007199254740993,"maximum":9223372036854775807}`)
		}
	}))
	defer registry.Close()

	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		t.Run(version, func(t *testing.T) {
			session, _ := sdkFixture(t, registry.URL, version, fixtureSelection())
			listed, err := session.ListTools(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(listed.Tools) != len(wantNames) || listed.NextCursor != "" {
				t.Fatalf("want 70 tools in one page, got %d and cursor %q", len(listed.Tools), listed.NextCursor)
			}
			auditedDestructive := map[string]bool{
				"builder_repository_binding_change": true, "builder_implementation_repository_change": true,
				"builder_todo_remove": true, "builder_design_system_begin": true, "builder_design_system_recover": true,
			}
			for i, tool := range listed.Tools {
				wantDestructive := wantNames[tool.Name] == http.MethodDelete || auditedDestructive[tool.Name]
				if wantNames[tool.Name] == "" || (i > 0 && listed.Tools[i-1].Name >= tool.Name) || tool.OutputSchema != nil ||
					tool.Annotations == nil || tool.Annotations.ReadOnlyHint != (wantNames[tool.Name] == http.MethodGet) ||
					tool.Annotations.DestructiveHint == nil || *tool.Annotations.DestructiveHint != wantDestructive ||
					!reflect.DeepEqual(tool.InputSchema, published[tool.Name]) {
					t.Fatalf("advertised schema or metadata differs for %s", tool.Name)
				}
				schema := tool.InputSchema.(map[string]any)
				if schema["type"] != "object" || schema["additionalProperties"] != false ||
					schema["$schema"] != "https://json-schema.org/draft/2020-12/schema" {
					t.Fatalf("advertised input is not a closed object for %s", tool.Name)
				}
			}
			for _, fixture := range calls {
				t.Run(fixture.Name+" "+fixture.Route, func(t *testing.T) {
					args, err := decodeObject(fixture.Arguments)
					if err != nil {
						t.Fatal(err)
					}
					if err := evaluators[fixture.Name].Validate(args); err != nil {
						t.Fatalf("independent JSON Schema evaluator rejected fixture: %v", err)
					}
					for _, status := range []int{0, http.StatusConflict} {
						state.Lock()
						state.expectedRoute, state.upstreamCode, state.forwarded = fixture.Route, status, nil
						state.Unlock()
						result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: fixture.Name, Arguments: fixture.Arguments})
						if err != nil {
							t.Fatal(err)
						}
						message := resultText(t, result)
						if strings.Contains(message, "PRIVATE") || result.IsError != (status != 0) {
							t.Fatalf("unexpected result: %s", message)
						}
						wantStatus := status
						if wantStatus == 0 {
							wantStatus = http.StatusOK
							if fixture.Name == "builder_session_bind" {
								wantStatus = http.StatusCreated
							}
						}
						if !strings.Contains(message, `"http_status":`+strconv.Itoa(wantStatus)) {
							t.Fatalf("lost owner HTTP status %d: %s", wantStatus, message)
						}
						if status == 0 && !strings.HasPrefix(fixture.Name, "builder_session_") && fixture.Name != "builder_project_get" && fixture.Name != "builder_project_register" &&
							(!strings.Contains(message, `"state":"pending"`) || !strings.Contains(message, `"revision":9007199254740993`) || !strings.Contains(message, `"maximum":9223372036854775807`)) {
							t.Fatalf("lost owner successful state or precision: %s", message)
						}
						if status != 0 && (!strings.Contains(message, `"todo_revision":9007199254740993`) || !strings.Contains(message, `"revision":9223372036854775807`)) {
							t.Fatalf("lost owner error precision: %s", message)
						}
						state.Lock()
						forwarded := slices.Clone(state.forwarded)
						state.Unlock()
						wantCalls := 1
						if fixture.Name == "builder_project_register" {
							wantCalls = 3
						}
						if len(forwarded) != wantCalls || forwarded[len(forwarded)-1].Route != fixture.Route {
							t.Fatalf("Registry wire route mismatch: %+v, want %s and %d calls", forwarded, fixture.Route, wantCalls)
						}
						wire := forwarded[len(forwarded)-1]
						if expected := args.child("body"); expected != nil {
							got, err := decodeObject([]byte(wire.Body))
							if err != nil || !reflect.DeepEqual(got, expected) {
								t.Fatalf("Registry payload mismatch: %s, want %v", wire.Body, expected)
							}
						} else if fixture.Name == "builder_session_bind" {
							if !strings.Contains(wire.Body, `"owner_reference":"fixture-user-selection"`) || !strings.Contains(wire.Body, fixtureSession) {
								t.Fatalf("missing trusted bind metadata: %s", wire.Body)
							}
						} else if wire.Body != "" {
							t.Fatalf("unexpected Registry body: %s", wire.Body)
						}
						if !reflect.DeepEqual(wire.Query, fixture.ExpectedQuery) {
							t.Fatalf("Registry query=%v, want exactly %v", wire.Query, fixture.ExpectedQuery)
						}
					}
				})
			}
		})
	}
}

func resultText(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()
	if result.StructuredContent != nil || len(result.Content) != 1 {
		t.Fatalf("unexpected tool result content: %+v", result)
	}
	content, ok := result.Content[0].(*mcp.TextContent)
	if !ok || !json.Valid([]byte(content.Text)) {
		t.Fatalf("expected exact JSON TextContent, got %+v", result)
	}
	return content.Text
}

func TestStreamingAndPublishedSchemaRejectInvalidShapes(t *testing.T) {
	specs, err := loadCatalog()
	if err != nil {
		t.Fatal(err)
	}
	tools := make(map[string]toolSpec)
	for _, spec := range specs {
		tools[spec.Name] = spec
	}
	for _, tc := range []struct{ tool, arguments string }{
		{"builder_company_search", `{"name":"a","id":"one"}`},
		{"builder_company_search", `{"name":19}`},
		{"builder_company_search", `{"id":"one","limit":51}`},
		{"builder_project_search", `{"id":"one"}`},
		{"builder_project_get", `{"company_id":"company","project_id":"../outside"}`},
		{"builder_project_get", `{"company_id":"company","project_id":"project","url":"http://example.invalid"}`},
		{"builder_session_bind", `{"company_id":"company","project_id":"project","operation_id":"bind","expected_revision":1}`},
		{"builder_knowledge_create", `{"company_id":"company","project_id":"project","family":"not-a-family","body":{}}`},
		{"builder_design_system_begin", `{"company_id":"company","project_id":"project","body":{"operation":"unexpected"}}`},
	} {
		t.Run(tc.tool+" "+tc.arguments, func(t *testing.T) {
			spec := tools[tc.tool]
			published, err := spec.wire.root.Resolve(nil)
			if err != nil {
				t.Fatal(err)
			}
			raw := json.RawMessage(tc.arguments)
			value, err := decodeObject(raw)
			if err != nil {
				t.Fatal(err)
			}
			if published.Validate(value) == nil {
				t.Fatal("advertised schema accepted an out-of-contract input")
			}
			if _, _, _, err := spec.readArguments(context.Background(), raw); err == nil {
				t.Fatal("streamed arguments accepted an out-of-contract input")
			}
		})
	}
	for _, raw := range []json.RawMessage{
		json.RawMessage(`{"id":"one","id":"one"}`),
		json.RawMessage(`{"id":"one"} {"id":"two"}`),
	} {
		if _, _, _, err := tools["builder_company_search"].readArguments(context.Background(), raw); err == nil {
			t.Fatalf("ambiguous wire JSON accepted: %v", raw)
		}
	}
}

func TestWireSchemaRejectsUnsupportedAssertionCombinations(t *testing.T) {
	for _, raw := range []string{
		`{"$defs":{"thing":{"type":"string"}},"$ref":"#/$defs/thing","pattern":"^only$"}`,
		`{"anyOf":[{"type":"string"}],"minLength":7}`,
		`{"not":{"type":"integer"}}`,
		`{"oneOf":[{"required":["first"],"additionalProperties":false}]}`,
	} {
		var schema jsonschema.Schema
		if err := json.Unmarshal([]byte(raw), &schema); err != nil {
			t.Fatal(err)
		}
		if _, err := compileWireSchema(&schema); err == nil {
			t.Fatalf("accepted unsupported schema assertion composition: %s", raw)
		}
	}
}
