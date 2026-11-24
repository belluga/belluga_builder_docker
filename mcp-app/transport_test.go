package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTransportRejectsUnauthenticatedCallsBeforeDispatch(t *testing.T) {
	dispatched := false
	handler := protectTransport(transportConfig{
		bearer: "fixture-secret-at-least-32-bytes",
		hosts:  map[string]bool{"localhost:8098": true},
	}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		dispatched = true
		w.WriteHeader(http.StatusAccepted)
	}))
	request := httptest.NewRequest(http.MethodPost, "http://localhost:8098/mcp", strings.NewReader("{}"))
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, request)
	if res.Code != http.StatusUnauthorized || dispatched {
		t.Fatalf("unauthenticated HTTP request reached the tool protocol: status=%d dispatch=%t", res.Code, dispatched)
	}
}

func TestTransportRejectsUntrustedHostOriginAndBearer(t *testing.T) {
	for _, tc := range []struct {
		name, target, origin string
		headers              []string
		status               int
	}{
		{"wrong credential", "http://localhost:8098/mcp", "", []string{"Bearer not-the-fixture-bearer"}, http.StatusUnauthorized},
		{"duplicate credential", "http://localhost:8098/mcp", "", []string{"Bearer " + fixtureToken, "Bearer " + fixtureToken}, http.StatusUnauthorized},
		{"untrusted host", "http://unrelated.invalid:8098/mcp", "", []string{"Bearer " + fixtureToken}, http.StatusForbidden},
		{"untrusted origin", "http://localhost:8098/mcp", "http://unrelated.invalid", []string{"Bearer " + fixtureToken}, http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				t.Error("untrusted request passed the transport guard")
				w.WriteHeader(http.StatusAccepted)
			})
			handler := protectTransport(transportConfig{
				bearer: fixtureToken, hosts: map[string]bool{"localhost:8098": true},
				origins: map[string]bool{"http://localhost:8098": true},
			}, next)
			request := httptest.NewRequest(http.MethodPost, tc.target, strings.NewReader("{}"))
			for _, header := range tc.headers {
				request.Header.Add("Authorization", header)
			}
			if tc.origin != "" {
				request.Header.Set("Origin", tc.origin)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != tc.status {
				t.Fatalf("status=%d want=%d", response.Code, tc.status)
			}
		})
	}
}

func TestLargeOrUnsupportedRPCControlIsRejectedBeforeSDK(t *testing.T) {
	const largeMarker = "synthetic-private-control-marker"
	oversized := strings.Repeat(largeMarker, 900)
	begin := `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"builder_session_get",`
	for _, tc := range []struct{ name, body string }{
		{"oversized meta", begin + `"_meta":{"extra":"` + oversized + `"},"arguments":{}}}`},
		{"input responses", begin + `"inputResponses":{"fixture":{"action":"accept"}},"arguments":{}}}`},
		{"request state", begin + `"requestState":"unsupported","arguments":{}}}`},
		{"wrong meta type", begin + `"_meta":[1,2,3],"arguments":{}}}`},
		{"unknown call fields", begin + `"credentials":{"contents":"` + largeMarker + `"},"arguments":{}}}`},
		{"duplicate call fields", begin + `"name":"builder_session_get","arguments":{}}}`},
		{"oversized control method", `{"jsonrpc":"2.0","id":7,"method":"tools/list","params":{"cursor":"` + oversized + `"}}`},
		{"unexpected top level", `{"jsonrpc":"2.0","id":7,"method":"tools/list","params":{},"override":{}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dispatched := false
			handler := protectTransport(transportConfig{
				bearer: fixtureToken, hosts: map[string]bool{"localhost:8098": true},
			}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				dispatched = true
				w.WriteHeader(http.StatusAccepted)
			}))
			request := httptest.NewRequest(http.MethodPost, "http://localhost:8098/mcp", strings.NewReader(tc.body))
			request.Header.Set("Authorization", "Bearer "+fixtureToken)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest && response.Code != http.StatusRequestEntityTooLarge ||
				dispatched || strings.Contains(response.Body.String(), largeMarker) {
				t.Fatalf("RPC control escaped early validation: status=%d dispatched=%t", response.Code, dispatched)
			}
		})
	}
}

func TestUnsupportedTypedMetadataStopsBeforeSDKWithLargeArguments(t *testing.T) {
	dispatched := false
	handler := protectTransport(transportConfig{
		bearer: fixtureToken, hosts: map[string]bool{"localhost:8098": true},
	}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		dispatched = true
		w.WriteHeader(http.StatusAccepted)
	}))
	body := strings.NewReader(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{` +
		`"name":"builder_session_get","_meta":{"metric":1e400},"arguments":{` +
		strings.Repeat(" ", maxMCPRequest-512) + `}}}`)
	request := httptest.NewRequest(http.MethodPost, "http://localhost:8098/mcp", body)
	request.Header.Set("Authorization", "Bearer "+fixtureToken)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || dispatched || response.Body.Len() > maxRPCControl {
		t.Fatalf("unsupported typed metadata reached the SDK: status=%d dispatched=%t response=%d bytes",
			response.Code, dispatched, response.Body.Len())
	}
}

func TestRPCNestingLimitMatchesPinnedSDKBeforeHandoff(t *testing.T) {
	deepArray := func(depth int) string {
		return strings.Repeat("[", depth) + "0" + strings.Repeat("]", depth)
	}
	call := func(params string) string {
		return `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":` + params + `}`
	}
	for _, tc := range []struct {
		name       string
		params     string
		wantStatus int
	}{
		{
			name:       "exact SDK maximum in arguments",
			params:     `{"name":"builder_session_get","arguments":{"deep":` + deepArray(maxRPCNestingDepth-2) + `}}`,
			wantStatus: http.StatusAccepted,
		},
		{
			name:       "over SDK maximum in arguments",
			params:     `{"name":"builder_session_get","arguments":{"deep":` + deepArray(maxRPCNestingDepth-1) + `}}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "over SDK maximum in metadata",
			params:     `{"name":"builder_session_get","arguments":{},"_meta":{"deep":` + deepArray(maxRPCNestingDepth-1) + `}}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "very deep arguments stop before recursive decoding",
			params:     `{"name":"builder_session_get","arguments":{"deep":` + deepArray(100_000) + `}}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "delimiters inside strings do not count",
			params:     `{"name":"builder_session_get","arguments":{},"_meta":{"text":"` + strings.Repeat("[]{}", 1500) + `"}}`,
			wantStatus: http.StatusAccepted,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dispatched := false
			handler := protectTransport(transportConfig{
				bearer: fixtureToken, hosts: map[string]bool{"localhost:8098": true},
			}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				dispatched = true
				w.WriteHeader(http.StatusAccepted)
			}))
			request := httptest.NewRequest(http.MethodPost, "http://localhost:8098/mcp", strings.NewReader(call(tc.params)))
			request.Header.Set("Authorization", "Bearer "+fixtureToken)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != tc.wantStatus || dispatched != (tc.wantStatus == http.StatusAccepted) {
				t.Fatalf("nesting boundary status=%d dispatched=%t body=%q", response.Code, dispatched, response.Body.String())
			}
			if tc.wantStatus == http.StatusBadRequest &&
				(!strings.Contains(response.Body.String(), errRPCNestingTooDeep.Error()) || response.Body.Len() > maxMCPError) {
				t.Fatalf("deep input was not rejected with a bounded pre-SDK diagnostic: %q", response.Body.String())
			}
		})
	}
}

func TestSDKProtocolErrorCannotReflectRawControl(t *testing.T) {
	const marker = "SDK-reflected-synthetic-private-control-marker"
	rpc := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`
	for _, tc := range []struct {
		status int
		body   string
	}{
		{http.StatusBadRequest, marker},
		{http.StatusBadRequest, `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"` + marker + `"}}`},
		{http.StatusOK, `{"jsonrpc":"2.0","id":1,"error":{"code":-32022,"message":"` + marker +
			`","data":{"supported":["` + marker + `"],"requested":"` + marker + `"}}}`},
		{http.StatusOK, `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"` + strings.Repeat(marker, 9000) + `"}}`},
	} {
		handler := protectTransport(transportConfig{
			bearer: fixtureToken, hosts: map[string]bool{"localhost:8098": true},
		}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(tc.body))
		}))
		request := httptest.NewRequest(http.MethodPost, "http://localhost:8098/mcp", strings.NewReader(rpc))
		request.Header.Set("Authorization", "Bearer "+fixtureToken)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != tc.status || strings.Contains(response.Body.String(), marker) ||
			response.Body.Len() == 0 || response.Body.Len() > maxMCPError {
			t.Fatalf("unbounded/unsanitized SDK protocol error: status=%d, bytes=%d", response.Code, response.Body.Len())
		}
	}
}
