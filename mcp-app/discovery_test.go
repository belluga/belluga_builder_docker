package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestDiscoveryWithoutConversationHeaders(t *testing.T) {
	const host = "127.0.0.1:8098"
	const companies = "/internal/v1/companies/search"
	const projects = "/internal/v1/companies/company-id/projects"
	var upstreamCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		number := upstreamCalls.Add(1)
		query := r.URL.Query()
		var wantCursor string
		switch number {
		case 3, 4:
			wantCursor = "fixture-cursor"
		case 5, 6:
			wantCursor = "bound-cursor"
		}
		if query.Get("cursor") != wantCursor {
			t.Errorf("discovery request %d cursor: got %q, want %q", number, query.Get("cursor"), wantCursor)
		}
		if r.Method != http.MethodGet || r.URL.Path != companies && r.URL.Path != projects || query.Get("name") != "Example" || query.Get("limit") != "1" {
			t.Errorf("unexpected discovery request: %s %s", r.Method, r.URL)
		}
		if r.URL.Path == companies && query.Get("company_ids") != fixtureCompany+",other-company" {
			t.Error("company discovery did not preserve its configured scope")
		}
		if number <= 4 {
			for _, name := range []string{headerProvider, headerSession, headerLane} {
				if len(r.Header.Values(name)) != 0 {
					t.Errorf("unscoped discovery forwarded %s", name)
				}
			}
		} else if r.Header.Get(headerLane) != "conversation" || r.Header.Get(headerProvider) != provider || r.Header.Get(headerSession) != fixtureSession {
			t.Error("scoped discovery lost the conversation identity")
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"match_state":"ambiguous","items":[{"id":"company-id","name":"Example"}],"next_cursor":"fixture-cursor"}`)
	}))
	defer upstream.Close()

	handler, err := newHandler(serviceConfig{
		origin: upstream.URL, companies: []string{fixtureCompany, "other-company"},
		transport: transportConfig{bearer: fixtureToken, hosts: map[string]bool{host: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	connect := func(t *testing.T, headers http.Header) *mcp.ClientSession {
		t.Helper()
		headers.Set("Authorization", "Bearer "+fixtureToken)
		client := mcp.NewClient(&mcp.Implementation{Name: "discovery-test", Version: "1"}, nil)
		session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
			Endpoint: server.URL + "/mcp", DisableStandaloneSSE: true,
			HTTPClient: &http.Client{Transport: authTransport{host: host, headers: headers}},
		}, &mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = session.Close() })
		return session
	}
	call := func(t *testing.T, client *mcp.ClientSession, tool string, args map[string]any, wantError bool) {
		t.Helper()
		result, err := client.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		body, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		if result.IsError != wantError || wantError && !strings.Contains(string(body), "session_required") ||
			!wantError && !strings.Contains(string(body), "fixture-cursor") {
			t.Fatalf("%s: %s", tool, body)
		}
	}
	withoutSession := connect(t, http.Header{})
	call(t, withoutSession, "builder_company_search", map[string]any{"name": "Example", "limit": 1}, false)
	projectArgs := map[string]any{"company_id": fixtureCompany, "name": "Example", "limit": 1}
	call(t, withoutSession, "builder_project_search", projectArgs, false)
	call(t, withoutSession, "builder_company_search", map[string]any{"name": "Example", "limit": 1, "cursor": "fixture-cursor"}, false)
	call(t, withoutSession, "builder_project_search", map[string]any{"company_id": fixtureCompany, "name": "Example", "limit": 1, "cursor": "fixture-cursor"}, false)

	catalog, err := loadCatalog()
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range catalog {
		if tool.Name != "builder_company_search" && tool.Name != "builder_project_search" {
			call(t, withoutSession, tool.Name, map[string]any{}, true)
		}
	}
	for name, headers := range map[string]http.Header{
		"missing type":   {headerSession: {fixtureSession}},
		"missing id":     {headerProvider: {provider}},
		"empty type":     {headerProvider: {""}},
		"empty id":       {headerSession: {""}},
		"duplicate id":   {headerProvider: {provider}, headerSession: {fixtureSession, fixtureSession}},
		"duplicate type": {headerProvider: {provider, "other"}, headerSession: {fixtureSession}},
		"invalid id":     {headerProvider: {provider}, headerSession: {"invalid"}},
		"invalid type":   {headerProvider: {"other"}, headerSession: {fixtureSession}},
	} {
		t.Run(name, func(t *testing.T) {
			client := connect(t, headers)
			call(t, client, "builder_company_search", map[string]any{"name": "Example", "limit": 1}, true)
			call(t, client, "builder_project_search", projectArgs, true)
		})
	}
	if got := upstreamCalls.Load(); got != 4 {
		t.Fatalf("expected only four reads before valid-session tests, got %d", got)
	}

	withSession := connect(t, http.Header{headerProvider: {provider}, headerSession: {fixtureSession}})
	call(t, withSession, "builder_company_search", map[string]any{"name": "Example", "limit": 1, "cursor": "bound-cursor"}, false)
	call(t, withSession, "builder_project_search", map[string]any{"company_id": fixtureCompany, "name": "Example", "limit": 1, "cursor": "bound-cursor"}, false)
	if got := upstreamCalls.Load(); got != 6 {
		t.Fatalf("expected six discovery calls, got %d", got)
	}
}
