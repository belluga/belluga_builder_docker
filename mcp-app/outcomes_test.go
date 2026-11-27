package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestRegistryFailurePreservesWriteUncertaintyWithoutReplay(t *testing.T) {
	const write = "builder_todo_next_step_update"
	for _, scenario := range []string{"malformed", "oversize", "redirect", "redirect-no-body"} {
		t.Run(scenario, func(t *testing.T) {
			var attempts atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempts.Add(1)
				raw, _ := io.ReadAll(r.Body)
				if scenario != "redirect-no-body" && (!strings.Contains(string(raw), "9007199254740993") || !strings.Contains(string(raw), "fixture-step")) {
					t.Error("request did not preserve exact operation ID and integer")
				}
				switch scenario {
				case "malformed":
					fmt.Fprint(w, `{"broken":"/private/path"`)
				case "oversize":
					w.WriteHeader(http.StatusServiceUnavailable)
					fmt.Fprint(w, strings.Repeat("x", maxRegistryError+1))
				default:
					http.Redirect(w, r, "/unapproved", http.StatusTemporaryRedirect)
				}
			}))
			defer upstream.Close()
			mcpClient, _ := sdkFixture(t, upstream.URL, "2026-07-28", fixtureSelection())
			tool := write
			args := json.RawMessage(`{"company_id":"company-id","project_id":"project-id","todo_id":"todo-id","body":{"project_id":"project-id","todo_id":"todo-id","expected_revision":9007199254740993,"operation_id":"fixture-step","reason":"fixture","next_exact_step":"read current state"}}`)
			if scenario == "redirect-no-body" {
				tool = "builder_design_system_recover"
				args = json.RawMessage(`{"company_id":"company-id","project_id":"project-id","operation_id":"fixture-step"}`)
			}
			result, err := mcpClient.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
			if err != nil {
				t.Fatal(err)
			}
			text := resultText(t, result)
			if !result.IsError || !strings.Contains(text, `"effects":"unknown"`) ||
				!strings.Contains(text, `"operation_id":"fixture-step"`) || strings.Contains(text, "private/path") || attempts.Load() != 1 {
				t.Fatalf("invalid upstream failure response or replay: %s attempts=%d", text, attempts.Load())
			}
		})
	}
}

func TestCancelPropagatesAfterMutationDispatch(t *testing.T) {
	for _, version := range []string{"2025-11-25", "2026-07-28"} {
		t.Run(version, func(t *testing.T) {
			dispatched, canceled := make(chan struct{}), make(chan struct{})
			var attempts atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				if attempts.Add(1) == 1 {
					close(dispatched)
					<-r.Context().Done()
					close(canceled)
				}
				fmt.Fprint(w, `{"outcome":"should never reach the canceled consumer"}`)
			}))
			defer upstream.Close()
			client, _ := sdkFixture(t, upstream.URL, version, fixtureSelection())
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			completed := make(chan error, 1)
			go func() {
				_, err := client.CallTool(ctx, &mcp.CallToolParams{
					Name:      "builder_design_system_recover",
					Arguments: json.RawMessage(`{"company_id":"company-id","project_id":"project-id","operation_id":"ds-recovery"}`),
				})
				completed <- err
			}()
			waitSignal(t, dispatched, "Registry received mutation")
			cancel()
			waitSignal(t, canceled, "Registry observed HTTP cancellation")
			select {
			case err := <-completed:
				if err == nil {
					t.Fatal("canceled MCP request reported success")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("MCP caller did not return after cancellation")
			}
			if attempts.Load() != 1 {
				t.Fatalf("mutation attempted %d times after cancel", attempts.Load())
			}
		})
	}
}

func TestLocalToolRejectionsDoNotDispatch(t *testing.T) {
	var attempts atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		fmt.Fprint(w, `{"unexpected":true}`)
	}))
	defer upstream.Close()
	client, _ := sdkFixture(t, upstream.URL, "2025-11-25", "")
	for _, test := range []struct{ tool, raw, code string }{
		{"builder_project_get", `{"company_id":"other","project_id":"project-id"}`, "company_not_allowed"},
		{"builder_project_get", `{"company_id":"company-id","project_id":"project-id","url":"http://example.invalid"}`, "mcp_invalid_arguments"},
		{"builder_project_get", `{"company_id":"company-id","project_id":"../other"}`, "mcp_invalid_arguments"},
		{"builder_project_search", `{"company_id":"company-id","id":"project-id","name":"Some name"}`, "mcp_invalid_arguments"},
		{"builder_project_search", `{"company_id":"company-id","id":"project-id","company_ids":"other"}`, "mcp_invalid_arguments"},
		{"builder_company_search", `{"name":"one","name":"two"}`, "mcp_invalid_arguments"},
		{"builder_todo_next_step_update", `{"company_id":"company-id","project_id":"project-id","todo_id":"todo-id","body":{"project_id":"project-id","todo_id":"another","expected_revision":9223372036854775807,"operation_id":"one","reason":"one","next_exact_step":"one"}}`, "mcp_identity_mismatch"},
		{"builder_todo_next_step_update", `{"company_id":"company-id","project_id":"project-id","todo_id":"todo-id","body":{"project_id":"project-id","todo_id":"todo-id","expected_revision":9223372036854775808,"operation_id":"one","reason":"one","next_exact_step":"one"}}`, "mcp_invalid_arguments"},
		{"builder_session_bind", `{"company_id":"company-id","project_id":"project-id","expected_revision":0,"operation_id":"bind-fixture"}`, "selection_required_or_mismatched"},
		{"builder_session_bind", `{"company_id":"company-id","project_id":"project-id","expected_revision":0,"operation_id":"bind-fixture","owner_reference":"forged"}`, "mcp_invalid_arguments"},
	} {
		result, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: test.tool, Arguments: json.RawMessage(test.raw)})
		if err != nil {
			t.Fatal(err)
		}
		text := resultText(t, result)
		if !result.IsError || !strings.Contains(text, test.code) {
			t.Errorf("%s did not reject %s: %s", test.tool, test.code, text)
		}
	}
	if attempts.Load() != 0 {
		t.Fatal("rejected calls reached Registry")
	}
}

func waitSignal(t *testing.T, signal <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(15 * time.Second):
		t.Fatal(description)
	}
}

func TestUnknownRegistrationUsesExactCollectionReadbackBeforeBind(t *testing.T) {
	var writes, searches atomic.Int32
	owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /internal/v1/sessions/codex/" + fixtureSession:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"error":"not_found"}`)
		case "GET /internal/v1/companies/search":
			fmt.Fprint(w, `{"match_state":"unique","items":[{"id":"company-id","name":"Fixture Company"}]}`)
		case "POST /internal/v1/companies/company-id/projects":
			writes.Add(1)
			fmt.Fprint(w, `{"partial":`)
		case "GET /internal/v1/companies/company-id/projects":
			if r.URL.RawQuery != "id=project-id" || writes.Load() != 1 {
				t.Errorf("registration readback was not exact and sequenced: %s", r.URL)
			}
			if searches.Add(1) == 1 {
				fmt.Fprint(w, `{"match_state":"none","items":[]}`)
			} else {
				fmt.Fprint(w, `{"match_state":"unique","items":[{"id":"project-id","company_id":"company-id","name":"Fixture Project"}]}`)
			}
		case "POST /internal/v1/sessions/bind":
			if searches.Load() != 2 {
				t.Error("bind attempted before conclusive Project readback")
			}
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"type":"codex","external_id":"`+fixtureSession+`","binding":{"company_id":"company-id","project_id":"project-id"}}`)
		default:
			t.Errorf("unexpected prebind route %s %s", r.Method, r.URL)
		}
	}))
	defer owner.Close()
	client, _ := sdkFixture(t, owner.URL, "2025-11-25", fixtureSelection())
	call := func(tool, args string) (*mcp.CallToolResult, string) {
		t.Helper()
		result, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: json.RawMessage(args)})
		if err != nil {
			t.Fatal(err)
		}
		return result, resultText(t, result)
	}
	outcome, text := call("builder_project_register", `{"company_id":"company-id","body":{"company_id":"company-id","project_id":"project-id","company_name":"Fixture Company","project_name":"Fixture Project"}}`)
	if !outcome.IsError || !strings.Contains(text, `"effects":"unknown"`) || strings.Contains(text, `"operation_id":`) || !strings.Contains(text, "builder_project_search") {
		t.Fatalf("unknown registration: %s", text)
	}
	_, text = call("builder_project_search", `{"company_id":"company-id","id":"project-id"}`)
	if !strings.Contains(text, `"match_state":"none"`) || writes.Load() != 1 {
		t.Fatalf("first inconclusive readback attempted another write: %s", text)
	}
	read, text := call("builder_project_search", `{"company_id":"company-id","id":"project-id"}`)
	if read.IsError || !strings.Contains(text, `{"id":"project-id","company_id":"company-id","name":"Fixture Project"}`) {
		t.Fatalf("exact Project tuple not preserved: %s", text)
	}
	bind, text := call("builder_session_bind", `{"company_id":"company-id","project_id":"project-id","expected_revision":0,"operation_id":"bind-fixture"}`)
	if bind.IsError || !strings.Contains(text, `"http_status":201`) || writes.Load() != 1 {
		t.Fatalf("bind after exact recovery: %s", text)
	}
}

func TestDesignSystemRecoveryLeavesPendingOutcomeUntilOwnerReadback(t *testing.T) {
	var recovery atomic.Int32
	owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /internal/v1/companies/company-id/projects/project-id/design-system/state":
			fmt.Fprint(w, `{"selection":{"state":"unselected","binding_revision":1},"pending_operation":{"operation_id":"ds-op"}}`)
		case "POST /internal/v1/companies/company-id/projects/project-id/design-system/mutations/ds-op/recover":
			recovery.Add(1)
			w.WriteHeader(503)
			fmt.Fprint(w, `{"error":"unavailable","state":"pending","teach_detail":{"schema_version":"1","code":"design_system_recovery_unavailable","field_path":"operation_id","operation_id":"ds-op","effects":"unknown","retry":"recover_operation","next_action":"Read state and recover the same operation."}}`)
		default:
			t.Errorf("unexpected route: %s %s", r.Method, r.URL)
		}
	}))
	defer owner.Close()
	client, _ := sdkFixture(t, owner.URL, "2026-07-28", fixtureSelection())
	const ids = `{"company_id":"company-id","project_id":"project-id"}`
	result, err := client.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "builder_design_system_state", Arguments: json.RawMessage(ids),
	})
	if err != nil || result.IsError || !strings.Contains(resultText(t, result), `"pending_operation":{"operation_id":"ds-op"}`) {
		t.Fatalf("pending state was not preserved: result=%+v err=%v", result, err)
	}
	result, err = client.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "builder_design_system_recover", Arguments: json.RawMessage(`{"company_id":"company-id","project_id":"project-id","operation_id":"ds-op"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	text := resultText(t, result)
	if !result.IsError || !strings.Contains(text, `"http_status":503`) || !strings.Contains(text, `"state":"pending"`) ||
		!strings.Contains(text, `"retry":"recover_operation"`) || !strings.Contains(text, `"operation_id":"ds-op"`) || recovery.Load() != 1 {
		t.Fatalf("owner recovery uncertainty changed: %s calls=%d", text, recovery.Load())
	}
}

func TestMaximumSignedIntegerIsPreservedInQueryAndResult(t *testing.T) {
	const maximum = "9223372036854775807"
	const precise = "9007199254740993"
	owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("from") != precise || r.URL.Query().Get("to") != maximum {
			t.Errorf("exact integers did not reach Registry: %s", r.URL.RawQuery)
		}
		fmt.Fprintf(w, `{"left":%s,"right":%s}`, precise, maximum)
	}))
	defer owner.Close()
	client, _ := sdkFixture(t, owner.URL, "2026-07-28", fixtureSelection())
	result, err := client.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "builder_todo_compare",
		Arguments: json.RawMessage(`{"company_id":"company-id","project_id":"project-id","todo_id":"todo-id","from":` + precise + `,"to":` + maximum + `}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if text := resultText(t, result); result.IsError || !strings.Contains(text, `"left":`+precise+`,"right":`+maximum) {
		t.Fatalf("exact result precision changed: %s", text)
	}
}

func TestFixedOwnerTODOBodyLimitBeforeRegistry(t *testing.T) {
	var invoked atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		invoked.Add(1)
		fmt.Fprint(w, `{"unexpected":true}`)
	}))
	defer upstream.Close()
	session, _ := sdkFixture(t, upstream.URL, "2025-11-25", fixtureSelection())
	large := strings.Repeat("a", 4<<20)
	payload := json.RawMessage(`{"company_id":"company-id","project_id":"project-id","todo_id":"todo-id","body":{"project_id":"project-id","todo_id":"todo-id","expected_revision":0,"operation_id":"large-argument","reason":"` +
		large + `","next_exact_step":"unchanged"}}`)
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "builder_todo_next_step_update", Arguments: payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || !strings.Contains(resultText(t, result), `"http_status":413`) || invoked.Load() != 0 {
		t.Fatalf("owning Registry body limit was not enforced: result=%v calls=%d", result.Content, invoked.Load())
	}
}
