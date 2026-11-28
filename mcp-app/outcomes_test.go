package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
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

func TestPromotionDiagnosticReachesMCPWithoutOpaqueFields(t *testing.T) {
	payload := `{"error":"conflict","teach":"Review target","teach_detail":{"schema_version":"1","code":"promotion_evidence_incomplete","field_path":"remote_proof","next_action":"Read the TODO.","effects":"none","retry":"read_reassess","promotion":{"requested_stage":"Promotion-Lane","repositories":[{"repository_id":"belluga/push","sha":"3333333333333333333333333333333333333333","target_branch":"stage","status":"branch_missing","alternatives":[{"branch":"dev","exists":true,"commit_reachable":true,"owner_confirmation_required":true}],"secret_token":"PRIVATE"}]}}}`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/todos/todo-id/transition") {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusConflict)
		fmt.Fprint(w, payload)
	}))
	defer upstream.Close()
	client, _ := sdkFixture(t, upstream.URL, "2026-07-28", fixtureSelection())
	result, err := client.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "builder_todo_transition",
		Arguments: json.RawMessage(`{"company_id":"company-id","project_id":"project-id","todo_id":"todo-id","body":{"project_id":"project-id","todo_id":"todo-id","expected_revision":6,"operation_id":"promote-fixture","requested_stage":"Promotion-Lane","promotion_targets":{"belluga/push":"stage"}}}`),
	})
	if err != nil || !result.IsError {
		t.Fatalf("expected structured MCP conflict: result=%+v err=%v", result, err)
	}
	text := resultText(t, result)
	var data errorEnvelope
	if json.Unmarshal([]byte(text), &data) != nil || data.HTTPStatus != 409 || data.TeachDetail == nil ||
		data.TeachDetail.Promotion == nil || len(data.TeachDetail.Promotion.Repositories) != 1 ||
		data.TeachDetail.Promotion.Repositories[0].Status != "branch_missing" || strings.Contains(text, "PRIVATE") {
		t.Fatalf("unexpected MCP diagnostic: %s", text)
	}
	if output := os.Getenv("BUILDER_TEACH_MCP_FIXTURE"); output != "" {
		if err := os.WriteFile(output, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPromotionRegistryHTTPDiagnosticsRoundTrip(t *testing.T) {
	input := os.Getenv("BUILDER_TEACH_EVIDENCE_DIRECTORY")
	if input == "" {
		t.Skip("run with the disposable-Mongo Registry HTTP evidence directory")
	}
	transitions := []string{"registry-http.json", "registry-relink.json", ""}
	var attempts atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		index := int(attempts.Add(1)) - 1
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/todos/todo-id/transition") || index >= len(transitions) {
			t.Errorf("unexpected Registry request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(500)
			return
		}
		if transitions[index] == "" {
			w.WriteHeader(http.StatusBadGateway)
			fmt.Fprint(w, "upstream response lost")
			return
		}
		body, err := os.ReadFile(filepath.Join(input, transitions[index]))
		if err != nil {
			t.Error(err)
			w.WriteHeader(500)
			return
		}
		w.WriteHeader(http.StatusConflict)
		w.Write(body)
	}))
	defer upstream.Close()
	client, _ := sdkFixture(t, upstream.URL, "2026-07-28", fixtureSelection())
	for _, name := range transitions {
		result, err := client.CallTool(context.Background(), &mcp.CallToolParams{
			Name:      "builder_todo_transition",
			Arguments: json.RawMessage(`{"company_id":"company-id","project_id":"project-id","todo_id":"todo-id","body":{"project_id":"project-id","todo_id":"todo-id","expected_revision":6,"operation_id":"promote-fixture","requested_stage":"Promotion-Lane","promotion_targets":{"owner/repo":"stage","owner/secondary":"stage"}}}`),
		})
		if err != nil || !result.IsError {
			t.Fatalf("missing transition error: %+v %v", result, err)
		}
		text := resultText(t, result)
		var got errorEnvelope
		if json.Unmarshal([]byte(text), &got) != nil || got.TeachDetail == nil {
			t.Fatalf("invalid MCP diagnostic: %s", text)
		}
		if name == "" {
			if got.TeachDetail.Effects != "unknown" || got.TeachDetail.OperationID != "promote-fixture" || !strings.Contains(got.TeachDetail.NextAction, "builder_todo_get") {
				t.Fatalf("unknown-effect guidance was lost: %s", text)
			}
			name = "unknown.json"
		} else {
			raw, err := os.ReadFile(filepath.Join(input, name))
			var want errorEnvelope
			if err != nil || json.Unmarshal(raw, &want) != nil || got.HTTPStatus != 409 || !reflect.DeepEqual(got.TeachDetail, want.TeachDetail) {
				t.Fatalf("MCP and Registry diagnostics differ: %s vs %s (%v)", text, raw, err)
			}
		}
		if err := os.WriteFile(filepath.Join(input, "mcp-"+name), []byte(text), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if attempts.Load() != 3 {
		t.Fatalf("unexpected retry count: %d", attempts.Load())
	}
}

func TestPromotionDiagnosticMCPBoundsEscapedRows(t *testing.T) {
	detail := promotionDiagnostic{RequestedStage: "Promotion-Lane", Repositories: make([]promotionRepositoryDiagnostic, 32)}
	for i := range detail.Repositories {
		detail.Repositories[i] = promotionRepositoryDiagnostic{
			RepositoryID: "example/repo", CommitSHA: fmt.Sprintf("%040x", i+1), TargetBranch: "stage", Status: "observation_unavailable",
			NextAction: strings.Repeat("<", 80),
		}
	}
	envelope := errorEnvelope{HTTPStatus: 409, Error: "conflict", TeachDetail: &teachDiagnostic{SchemaVersion: teachVersion, Code: "promotion_evidence_incomplete",
		FieldPath: "remote_proof", NextAction: "Read the exact TODO.", Effects: "none", Retry: "read_reassess", Promotion: &detail}}
	if !validTeach(envelope.TeachDetail) {
		t.Fatal("thirty-two bounded rows were refused")
	}
	result := errorResult(envelope)
	if !result.IsError {
		t.Fatal("expected MCP error")
	}
	var decoded errorEnvelope
	text := resultText(t, result)
	if json.Unmarshal([]byte(text), &decoded) != nil || decoded.TeachDetail == nil || decoded.TeachDetail.Promotion == nil ||
		len(decoded.TeachDetail.Promotion.Repositories) != 32 {
		t.Fatalf("MCP lost bounded diagnostic: %d bytes", len(text))
	}
	for i := range detail.Repositories {
		detail.Repositories[i].NextAction = strings.Repeat("<", 200)
	}
	if validTeach(envelope.TeachDetail) {
		t.Fatal("escaped diagnostic exceeded the budget but was accepted")
	}
}
