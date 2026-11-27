package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

const (
	maxMCPRequest             = 32 << 20
	maxMCPResponse            = 128 << 20
	maxInFlight               = 2
	largeMCPResponseThreshold = 1 << 20
)

type transportConfig struct {
	bearer  string
	hosts   map[string]bool
	origins map[string]bool
}

type carrierKey struct{}

type transportCarrier struct {
	context   context.Context
	provider  string
	session   string
	selection string
	response  *largeResponseLease
}

// largeResponseLease keeps expanded TextContent serialization from
// multiplying across both admitted MCP requests. Registry calls stay
// concurrent; only materializing a large response waits for the prior
// response to finish.
type largeResponseLease struct {
	gate chan struct{}
	held atomic.Bool
}

func (l *largeResponseLease) acquire(ctx context.Context) bool {
	if l == nil || l.gate == nil || !l.held.CompareAndSwap(false, true) {
		return false
	}
	select {
	case l.gate <- struct{}{}:
		return true
	case <-ctx.Done():
		l.held.Store(false)
		return false
	}
}

func (l *largeResponseLease) release() {
	if l != nil && l.held.CompareAndSwap(true, false) {
		<-l.gate
	}
}

func protectTransport(config transportConfig, next http.Handler) http.Handler {
	bearer := sha256.Sum256([]byte(config.bearer))
	inFlight := make(chan struct{}, maxInFlight)
	largeResponses := make(chan struct{}, 1)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != "/mcp" || r.URL.RawQuery != "" {
			http.NotFound(w, r)
			return
		}
		if !config.hosts[r.Host] {
			http.Error(w, "Host is outside the private MCP allowlist", http.StatusForbidden)
			return
		}
		if origins := r.Header.Values("Origin"); len(origins) > 0 && (len(origins) != 1 || !config.origins[origins[0]]) {
			http.Error(w, "Origin is outside the private MCP allowlist", http.StatusForbidden)
			return
		}
		token, schemeOK := strings.CutPrefix(singleHeader(r.Header, "Authorization"), "Bearer ")
		candidate := sha256.Sum256([]byte(token))
		matches := subtle.ConstantTimeCompare(candidate[:], bearer[:]) == 1
		if !schemeOK || !matches || len(config.bearer) < 32 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="builder-mcp"`)
			http.Error(w, "Builder MCP credential required", http.StatusUnauthorized)
			return
		}

		select {
		case inFlight <- struct{}{}:
			defer func() { <-inFlight }()
		default:
			http.Error(w, "Builder MCP is at its concurrent request limit", http.StatusTooManyRequests)
			return
		}
		var requestID json.RawMessage
		if r.Method == http.MethodPost {
			var accepted bool
			requestID, accepted = boundRPCRequest(w, r)
			if !accepted {
				return
			}
		}
		ctx, cancel := context.WithTimeout(r.Context(), 200*time.Second)
		defer cancel()
		response := &largeResponseLease{gate: largeResponses}
		defer response.release()
		carrier := transportCarrier{
			context: ctx, provider: singleHeader(r.Header, headerProvider),
			session: singleHeader(r.Header, headerSession), selection: singleHeader(r.Header, headerSelection), response: response,
		}
		guarded := &limitedWriter{ResponseWriter: w, requestID: requestID}
		next.ServeHTTP(guarded, r.WithContext(context.WithValue(ctx, carrierKey{}, carrier)))
		guarded.finish()
	})
}

func singleHeader(header http.Header, key string) string {
	values := header.Values(key)
	if len(values) != 1 {
		return ""
	}
	return values[0]
}

func boundRPCRequest(w http.ResponseWriter, r *http.Request) (json.RawMessage, bool) {
	if r.ContentLength > maxMCPRequest {
		http.Error(w, "MCP request exceeds 32 MiB", http.StatusRequestEntityTooLarge)
		return nil, false
	}
	limited := http.MaxBytesReader(w, r.Body, maxMCPRequest)
	defer limited.Close()
	var raw []byte
	var err error
	if r.ContentLength > 0 {
		raw = make([]byte, r.ContentLength)
		_, err = io.ReadFull(limited, raw)
	} else {
		raw, err = io.ReadAll(limited)
	}
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "MCP request exceeds 32 MiB", http.StatusRequestEntityTooLarge)
		} else {
			http.Error(w, "MCP request body is unavailable", http.StatusBadRequest)
		}
		return nil, false
	}
	requestID, err := inspectRPCRequest(raw)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, errRPCControlTooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		http.Error(w, err.Error(), status)
		return nil, false
	}
	// The SDK reads its own opaque request bytes. Release this first buffer
	// as soon as it has been drained, before the SDK builds a large response.
	r.Body = &drainingRPCBody{Reader: bytes.NewReader(raw)}
	return append(json.RawMessage(nil), requestID...), true
}

type drainingRPCBody struct{ *bytes.Reader }

func (b *drainingRPCBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	if b.Reader.Len() == 0 {
		b.Reader.Reset(nil)
	}
	return n, err
}

func (b *drainingRPCBody) Close() error { return nil }
