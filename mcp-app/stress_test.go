package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestTwoMaximumHTTPExchanges(t *testing.T) {
	binary := os.Getenv("BUILDER_MCP_STRESS_BINARY")
	if binary == "" {
		t.Skip("run the container memory fixture with BUILDER_MCP_STRESS_BINARY")
	}
	var arrived atomic.Int32
	both, send := make(chan struct{}), make(chan struct{})
	registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if arrived.Add(1) == 2 {
			close(both)
		}
		select {
		case <-r.Context().Done():
			return
		case <-send:
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", strconv.Itoa(maxRegistrySuccess))
		fmt.Fprint(w, `{"state":"selected","data":"`)
		const edges = len(`{"state":"selected","data":""}`)
		// This character takes six bytes in the Go SDK's JSON encoding.
		_, _ = io.CopyN(w, repeatedByte('&'), int64(maxRegistrySuccess-edges))
		fmt.Fprint(w, `"}`)
	}))
	defer registry.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	expectedResponseSize, expectedResponseDigest := maximumHTTPExchangeIntegrity(t)
	address, pid := startStressAdapter(t, ctx, binary, registry.URL)
	prefix, suffix := maximumOverviewEnvelope()
	payload := io.MultiReader(strings.NewReader(prefix), io.LimitReader(repeatedByte(' '), int64(maxMCPRequest-len(prefix)-len(suffix))), strings.NewReader(suffix))
	makeRequest := func(body io.Reader, size int64) *http.Request {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+address+"/mcp", body)
		if err != nil {
			t.Fatal(err)
		}
		req.ContentLength = size
		req.Header.Set("Authorization", "Bearer "+fixtureToken)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("MCP-Protocol-Version", "2025-11-25")
		req.Header.Set(headerProvider, provider)
		req.Header.Set(headerSession, fixtureSession)
		return req
	}
	type outcome struct {
		err    error
		status int
		size   int64
		digest string
	}
	outcomes := make(chan outcome, 2)
	start := time.Now()
	for i := 0; i < 2; i++ {
		reader := payload
		if i == 1 {
			reader = io.MultiReader(strings.NewReader(prefix), io.LimitReader(repeatedByte(' '), int64(maxMCPRequest-len(prefix)-len(suffix))), strings.NewReader(suffix))
		}
		req := makeRequest(reader, maxMCPRequest)
		if i == 1 {
			req.ContentLength = -1 // Force a real Transfer-Encoding: chunked request.
		}
		go func() {
			response, err := http.DefaultClient.Do(req)
			if err != nil {
				outcomes <- outcome{err: err}
				return
			}
			defer response.Body.Close()
			digest := sha256.New()
			size, err := io.Copy(digest, response.Body)
			outcomes <- outcome{err: err, status: response.StatusCode, size: size, digest: hex.EncodeToString(digest.Sum(nil))}
		}()
	}
	select {
	case <-both:
	case result := <-outcomes:
		t.Fatalf("first call finished before simultaneous Registry dispatch: %+v", result)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	rejected, err := http.DefaultClient.Do(makeRequest(strings.NewReader("{}"), 2))
	if err != nil {
		t.Fatal(err)
	}
	rejected.Body.Close()
	if rejected.StatusCode != http.StatusTooManyRequests || arrived.Load() != 2 {
		t.Fatalf("overload reached Registry: status=%d calls=%d", rejected.StatusCode, arrived.Load())
	}
	close(send)
	for i := 0; i < 2; i++ {
		select {
		case result := <-outcomes:
			if result.err != nil || result.status != http.StatusOK || result.size != expectedResponseSize || result.size > maxMCPResponse || result.digest != expectedResponseDigest {
				t.Fatalf("bad maximum exchange: err=%v status=%d size=%d want_size=%d digest=%s want_digest=%s", result.err, result.status, result.size, expectedResponseSize, result.digest, expectedResponseDigest)
			}
			t.Logf("exchange request=%d registry_response=%d mcp_response=%d duration=%s", maxMCPRequest, maxRegistrySuccess, result.size, time.Since(start))
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	oversize := makeRequest(io.LimitReader(repeatedByte(' '), maxMCPRequest+1), maxMCPRequest+1)
	bad, err := http.DefaultClient.Do(oversize)
	if err != nil {
		t.Fatal(err)
	}
	bad.Body.Close()
	if bad.StatusCode != http.StatusRequestEntityTooLarge || arrived.Load() != 2 {
		t.Fatalf("oversize status=%d Registry invocations=%d", bad.StatusCode, arrived.Load())
	}
	rss, err := peakRSS(pid)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("adapter process peak RSS=%d KiB", rss)
	if rss >= 896*1024 {
		t.Fatalf("adapter peak RSS above budget: %d KiB", rss)
	}
}

func maximumHTTPExchangeIntegrity(t *testing.T) (int64, string) {
	t.Helper()
	const marker = "MCP_MAXIMUM_RESPONSE_MARKER"
	const textPrefix = `{"http_status":200,"data":{"state":"selected","data":"`
	const textSuffix = `"}}`
	result, err := json.Marshal(&mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: textPrefix + marker + textSuffix}},
	})
	if err != nil {
		t.Fatal(err)
	}
	id, err := jsonrpc.MakeID(float64(1))
	if err != nil {
		t.Fatal(err)
	}
	wire, err := jsonrpc.EncodeMessage(&jsonrpc.Response{ID: id, Result: result})
	if err != nil {
		t.Fatal(err)
	}
	markerAt := bytes.Index(wire, []byte(marker))
	if markerAt < 0 || bytes.Index(wire[markerAt+len(marker):], []byte(marker)) >= 0 {
		t.Fatal("maximum response marker was not unique in the encoded MCP response")
	}
	const registryEdges = len(`{"state":"selected","data":""}`)
	const escapedAmpersand = `\u0026`
	repetitions := maxRegistrySuccess - registryEdges
	digest := sha256.New()
	_, _ = digest.Write(wire[:markerAt])
	const repetitionsPerChunk = 16 * 1024
	chunk := bytes.Repeat([]byte(escapedAmpersand), repetitionsPerChunk)
	for remaining := repetitions; remaining > 0; {
		count := min(remaining, repetitionsPerChunk)
		_, _ = digest.Write(chunk[:count*len(escapedAmpersand)])
		remaining -= count
	}
	_, _ = digest.Write(wire[markerAt+len(marker):])
	responseSize := len(wire) - len(marker) + repetitions*len(escapedAmpersand)
	return int64(responseSize), hex.EncodeToString(digest.Sum(nil))
}

func startStressAdapter(t *testing.T, ctx context.Context, binary, upstream string) (string, int) {
	t.Helper()
	address, err := ephemeralTCPAddress()
	if err != nil {
		t.Fatal(err)
	}
	server := exec.CommandContext(ctx, binary)
	server.Env = []string{
		"BUILDER_MCP_COMPANY_IDS=" + fixtureCompany,
		"BUILDER_MCP_BEARER_TOKEN=" + fixtureToken,
		"BUILDER_MCP_ALLOWED_HOSTS=" + address,
		"BUILDER_MCP_LISTEN_ADDR=" + address,
		"BUILDER_MCP_REGISTRY_URL=" + upstream,
		"GOMEMLIMIT=768MiB", "GOMAXPROCS=2",
	}
	server.Stderr = os.Stderr
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = server.Process.Kill()
		_ = server.Wait()
	})
	waitTCP(t, ctx, address)
	return address, server.Process.Pid
}

func stressRequest(ctx context.Context, address string, body io.Reader, size int64) *http.Request {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+address+"/mcp", body)
	if err != nil {
		panic(err) // Test-owned URL is supplied by ephemeralTCPAddress.
	}
	req.ContentLength = size
	req.Header.Set("Authorization", "Bearer "+fixtureToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", "2025-11-25")
	req.Header.Set(headerProvider, provider)
	req.Header.Set(headerSession, fixtureSession)
	return req
}

func TestWideArgumentsRejectWithoutDispatchOrMemoryExhaustion(t *testing.T) {
	binary := os.Getenv("BUILDER_MCP_STRESS_BINARY")
	if binary == "" {
		t.Skip("run the container memory fixture with BUILDER_MCP_STRESS_BINARY")
	}
	var dispatched atomic.Int32
	registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		dispatched.Add(1)
		fmt.Fprint(w, `{"unexpected":true}`)
	}))
	defer registry.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	address, pid := startStressAdapter(t, ctx, binary, registry.URL)
	const prefix = `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"builder_overview_write","arguments":{"company_id":"company-id","project_id":"project-id","body":{"media":[`
	const suffix = `0]}}}}`
	for _, payload := range []struct {
		size int64
		code string
	}{
		{(32 << 20) - 16, "mcp_body_too_large"},
		{(18 << 20) - 1024, "mcp_invalid_arguments"},
	} {
		results := make(chan error, 2)
		for i := 0; i < 2; i++ {
			reader := io.MultiReader(strings.NewReader(prefix), io.LimitReader(&jsonZeros{}, (payload.size-int64(len(prefix)+len(suffix)))&^1), strings.NewReader(suffix))
			req := stressRequest(ctx, address, reader, -1)
			go func() {
				res, err := http.DefaultClient.Do(req)
				if err != nil {
					results <- err
					return
				}
				defer res.Body.Close()
				body, err := io.ReadAll(io.LimitReader(res.Body, 10000))
				if err != nil || res.StatusCode != http.StatusOK || !bytes.Contains(body, []byte(payload.code)) {
					results <- fmt.Errorf("wide request response status=%d body=%s err=%v", res.StatusCode, body, err)
					return
				}
				results <- nil
			}()
		}
		for i := 0; i < 2; i++ {
			if err := <-results; err != nil {
				t.Fatal(err)
			}
		}
		t.Logf("two wide chunked input bodies of %d bytes rejected with %s", payload.size, payload.code)
	}
	rss, err := peakRSS(pid)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("wide arguments peak RSS=%d KiB Registry dispatch=%d", rss, dispatched.Load())
	if rss >= 896*1024 || dispatched.Load() != 0 {
		t.Fatalf("wide malformed calls exceeded a boundary: RSS=%d KiB Registry dispatch=%d", rss, dispatched.Load())
	}
}

type jsonZeros struct{ comma bool }

func (r *jsonZeros) Read(data []byte) (int, error) {
	for i := range data {
		data[i] = '0'
		if r.comma {
			data[i] = ','
		}
		r.comma = !r.comma
	}
	return len(data), nil
}

func TestSlowChunkedClientCancellationReleasesAdmission(t *testing.T) {
	binary := os.Getenv("BUILDER_MCP_STRESS_BINARY")
	if binary == "" {
		t.Skip("run the container slow-client fixture with BUILDER_MCP_STRESS_BINARY")
	}
	var dispatched atomic.Int32
	registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		dispatched.Add(1)
		fmt.Fprint(w, `{"items":[],"match_state":"none"}`)
	}))
	defer registry.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	address, pid := startStressAdapter(t, ctx, binary, registry.URL)

	var channels []net.Conn
	for i := 0; i < 2; i++ {
		conn, err := net.DialTimeout("tcp", address, 5*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		channels = append(channels, conn)
		// Send one incomplete chunk, then stall without completing the body.
		if _, err := fmt.Fprintf(conn, "POST /mcp HTTP/1.1\r\nHost: %s\r\nAuthorization: Bearer %s\r\nContent-Type: application/json\r\nTransfer-Encoding: chunked\r\n\r\n1\r\n{\r\n", address, fixtureToken); err != nil {
			t.Fatal(err)
		}
	}
	poll := func(body string, want int) {
		t.Helper()
		for {
			req := stressRequest(ctx, address, strings.NewReader(body), int64(len(body)))
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = io.Copy(io.Discard, res.Body)
			res.Body.Close()
			if res.StatusCode == want {
				return
			}
			select {
			case <-ctx.Done():
				t.Fatalf("never saw status %d, last was %d", want, res.StatusCode)
			case <-time.After(15 * time.Millisecond):
			}
		}
	}
	poll("{}", http.StatusTooManyRequests)
	if dispatched.Load() != 0 {
		t.Fatal("stalled inputs reached Registry")
	}
	start := time.Now()
	for _, conn := range channels {
		conn.Close()
	}
	const ordinary = `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"builder_company_search","arguments":{"id":"company-id"}}}`
	poll(ordinary, http.StatusOK)
	released := time.Since(start)
	req := stressRequest(ctx, address, io.LimitReader(repeatedByte(' '), maxMCPRequest+1), -1)
	oversize, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	oversize.Body.Close()
	if oversize.StatusCode != http.StatusRequestEntityTooLarge || dispatched.Load() != 1 {
		t.Fatalf("chunked limit status=%d Registry dispatch=%d", oversize.StatusCode, dispatched.Load())
	}
	rss, err := peakRSS(pid)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("stalled chunked slots recovered in %s, oversize chunked blocked, peak RSS=%d KiB", released, rss)
	if rss >= 896*1024 {
		t.Fatal("slow-client memory budget exceeded")
	}
}

func maximumOverviewEnvelope() (string, string) {
	media := make([]string, 0, 6)
	image := strings.Repeat("A", 2796203) + "="
	for _, slot := range []string{"desktop", "mobile", "problem", "proposal", "impact", "audience"} {
		media = append(media, `{"slot":"`+slot+`","data":"`+image+`"}`)
	}
	const prefix = `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"builder_overview_write","arguments":{"company_id":"company-id","project_id":"project-id","body":{"project_id":"project-id","expected_revision":0,"operation_id":"bounded-overview","actor":"fixture","content":{"schema_version":"1","tagline":"fixture","summary":"fixture","desktop_alt":"desktop","mobile_alt":"mobile","sections":{"problem":{"heading":"problem","body":"fixture","points":[]},"proposal":{"heading":"proposal","body":"fixture","points":[]},"impact":{"heading":"impact","body":"fixture","points":[]},"audience":{"heading":"audience","body":"fixture","points":[]}}},"knowledge_origin":{"project_id":"project-id","generation_id":"generation","documents":[]},"source":{"origin_absent":true},"media":[`
	return prefix + strings.Join(media, ",") + `]}}}}`, ""
}

type repeatedByte byte

func (c repeatedByte) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(c)
	}
	return len(p), nil
}

func ephemeralTCPAddress() (string, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer listener.Close()
	return listener.Addr().String(), nil
}

func waitTCP(t *testing.T, ctx context.Context, address string) {
	t.Helper()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		conn, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
		if err == nil {
			conn.Close()
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-tick.C:
		}
	}
}

func peakRSS(pid int) (int, error) {
	info, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0, err
	}
	for _, line := range bytes.Split(info, []byte{'\n'}) {
		fields := bytes.Fields(line)
		if len(fields) == 3 && string(fields[0]) == "VmHWM:" && string(fields[2]) == "kB" {
			return strconv.Atoi(string(fields[1]))
		}
	}
	return 0, errors.New("process peak RSS missing")
}

func TestWideProtocolControlRejectsBeforeSDK(t *testing.T) {
	binary := os.Getenv("BUILDER_MCP_STRESS_BINARY")
	if binary == "" {
		t.Skip("run the pre-SDK memory fixture with BUILDER_MCP_STRESS_BINARY")
	}
	var dispatched atomic.Int32
	owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		dispatched.Add(1)
		fmt.Fprint(w, `{"unexpected":true}`)
	}))
	defer owner.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	address, pid := startStressAdapter(t, ctx, binary, owner.URL)
	const prefix = `{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"builder_company_search","arguments":{"id":"company-id"},"_meta":{"synthetic-control-marker":[`
	const suffix = `0]}}}`
	const payload = (maxMCPRequest - len(prefix) - len(suffix) - 16) &^ 1
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		data := io.MultiReader(strings.NewReader(prefix), io.LimitReader(&jsonZeros{}, int64(payload)), strings.NewReader(suffix))
		req := stressRequest(ctx, address, data, -1)
		go func() {
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				results <- err
				return
			}
			defer res.Body.Close()
			message, err := io.ReadAll(io.LimitReader(res.Body, maxMCPError+1))
			if err != nil || res.StatusCode != http.StatusRequestEntityTooLarge || len(message) > maxMCPError ||
				bytes.Contains(message, []byte("synthetic-control-marker")) ||
				!bytes.Contains(message, []byte("protocol control exceeds")) {
				results <- fmt.Errorf("oversized control leaked to protocol: status=%d size=%d err=%v", res.StatusCode, len(message), err)
				return
			}
			results <- nil
		}()
	}
	for i := 0; i < 2; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	rss, err := peakRSS(pid)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("two %d-byte protocol-control requests rejected before SDK, process peak RSS=%d KiB, Registry dispatch=%d",
		len(prefix)+payload+len(suffix), rss, dispatched.Load())
	if rss >= 896*1024 || dispatched.Load() != 0 {
		t.Fatalf("SDK control gate missed a runtime boundary: RSS=%d KiB owner dispatch=%d", rss, dispatched.Load())
	}
}

func TestDeepJSONWithMaximumBodyRejectsBeforeSDK(t *testing.T) {
	binary := os.Getenv("BUILDER_MCP_STRESS_BINARY")
	if binary == "" {
		t.Skip("run the container JSON-depth fixture with BUILDER_MCP_STRESS_BINARY")
	}
	var dispatched atomic.Int32
	owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		dispatched.Add(1)
		fmt.Fprint(w, `{"unexpected":true}`)
	}))
	defer owner.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	address, pid := startStressAdapter(t, ctx, binary, owner.URL)
	depth := maxMCPRequest / 4
	depthBytes := int64(depth*2 + 1)
	depthReader := func(value byte) io.Reader {
		return io.LimitReader(repeatedByte(value), int64(depth))
	}
	metaPrefix := `{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"builder_session_get","_meta":{"deep":`
	metaMiddle := `},"arguments":{"padding":"`
	metaSuffix := `"}}}`
	metaPadding := maxMCPRequest - len(metaPrefix) - int(depthBytes) - len(metaMiddle) - len(metaSuffix)
	argsPrefix := `{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"builder_session_get","arguments":{"padding":"`
	argsMiddle := `","deep":`
	argsSuffix := `}}}`
	argsPadding := maxMCPRequest - len(argsPrefix) - len(argsMiddle) - int(depthBytes) - len(argsSuffix)
	if metaPadding < 0 || argsPadding < 0 {
		t.Fatal("deep JSON fixtures do not fit in the bounded request body")
	}
	start := make(chan struct{})
	type result struct {
		name   string
		status int
		body   []byte
		err    error
	}
	results := make(chan result, maxInFlight)
	for _, fixture := range []struct {
		name string
		body io.Reader
	}{
		{
			name: "metadata",
			body: io.MultiReader(
				strings.NewReader(metaPrefix), depthReader('['), strings.NewReader("0"), depthReader(']'),
				strings.NewReader(metaMiddle), io.LimitReader(repeatedByte('x'), int64(metaPadding)), strings.NewReader(metaSuffix),
			),
		},
		{
			name: "arguments after a large string",
			body: io.MultiReader(
				strings.NewReader(argsPrefix), io.LimitReader(repeatedByte('x'), int64(argsPadding)), strings.NewReader(argsMiddle),
				depthReader('['), strings.NewReader("0"), depthReader(']'), strings.NewReader(argsSuffix),
			),
		},
	} {
		request := stressRequest(ctx, address, fixture.body, maxMCPRequest)
		name := fixture.name
		go func() {
			<-start
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				results <- result{name: name, err: err}
				return
			}
			defer response.Body.Close()
			message, err := io.ReadAll(io.LimitReader(response.Body, maxMCPError+1))
			results <- result{name: name, status: response.StatusCode, body: message, err: err}
		}()
	}
	close(start)
	for i := 0; i < maxInFlight; i++ {
		response := <-results
		if response.err != nil || response.status != http.StatusBadRequest ||
			len(response.body) > maxMCPError ||
			!bytes.Contains(response.body, []byte(errRPCNestingTooDeep.Error())) {
			t.Fatalf("%s deep maximum-body request did not stop at the bounded pre-SDK guard: status=%d bytes=%d err=%v body=%q",
				response.name, response.status, len(response.body), response.err, response.body)
		}
	}
	rss, err := peakRSS(pid)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("two concurrent %d-byte requests with %d-level metadata/arguments rejected before decoder; peak RSS=%d KiB, Registry dispatch=%d",
		maxMCPRequest, depth, rss, dispatched.Load())
	if rss >= 896*1024 || dispatched.Load() != 0 {
		t.Fatalf("nested request crossed the pre-SDK resource boundary: RSS=%d KiB owner dispatch=%d", rss, dispatched.Load())
	}
}
