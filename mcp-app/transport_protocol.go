package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	fastjson "github.com/segmentio/encoding/json"
)

// Protocol control is small on both supported SDK versions. This fence is
// separate from the 32 MiB request envelope and the Registry body limits.
const maxRPCControl = 16 << 10
const maxRPCNestingDepth = 1000
const maxRPCWireNestingDepth = maxRPCNestingDepth + 1

var (
	errRPCControlTooLarge = errors.New("MCP protocol control exceeds 16 KiB")
	errRPCNestingTooDeep  = errors.New("MCP request exceeds the supported JSON nesting depth")
	errRPCInvalid         = errors.New("Supply one supported JSON-RPC 2.0 request")
)

func inspectRPCRequest(raw []byte) (json.RawMessage, error) {
	raw = bytes.TrimSpace(raw)
	// Segmentio recursively parses RawMessage values. Bound nesting before it
	// sees the request; the extra level is the outer JSON-RPC envelope, while
	// the pinned SDK applies the 1000-level limit to params alone.
	if exceedsJSONDepth(raw, maxRPCWireNestingDepth) {
		return nil, errRPCNestingTooDeep
	}
	var envelope struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}
	if rest, err := fastjson.Parse(raw, &envelope, fastjson.ZeroCopy); err != nil || len(rest) != 0 ||
		envelope.JSONRPC != "2.0" || !validRPCID(envelope.ID) ||
		len(envelope.Method) == 0 || len(envelope.Method) > 128 {
		return nil, errRPCInvalid
	}
	var meta json.RawMessage
	if envelope.Method == "tools/call" {
		var params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
			Meta      json.RawMessage `json:"_meta"`
		}
		if err := parseRPCParams(envelope.Params, &params); err != nil {
			return nil, err
		}
		if len(raw)-len(params.Arguments) > maxRPCControl {
			return nil, errRPCControlTooLarge
		}
		if len(envelope.ID) == 0 || bytes.Equal(envelope.ID, []byte("null")) || len(params.Name) == 0 ||
			len(params.Name) > 128 || len(params.Arguments) != 0 && params.Arguments[0] != '{' ||
			!exactRPCKeys(envelope.Params, []string{"name", "arguments", "_meta"}) {
			return nil, errRPCInvalid
		}
		meta = params.Meta
	} else {
		if len(raw) > maxRPCControl {
			return nil, errRPCControlTooLarge
		}
		if len(envelope.Params) != 0 {
			var params struct {
				Meta json.RawMessage `json:"_meta"`
			}
			if err := parseRPCParams(envelope.Params, &params); err != nil {
				return nil, err
			}
			meta = params.Meta
		}
	}
	if !exactRPCKeys(raw, []string{"jsonrpc", "id", "method", "params"}) {
		return nil, errRPCInvalid
	}
	if len(meta) != 0 {
		// Match the SDK's metadata conversion after the independent control
		// fence, before a rejected conversion can involve the full argument body.
		var sdkMeta map[string]any
		if meta[0] != '{' || json.Unmarshal(meta, &sdkMeta) != nil {
			return nil, errRPCInvalid
		}
	}
	return envelope.ID, nil
}

func exceedsJSONDepth(raw []byte, limit int) bool {
	var (
		depth    int
		inString bool
		escaped  bool
	)
	for _, b := range raw {
		if inString {
			switch {
			case escaped:
				escaped = false
			case b == '\\':
				escaped = true
			case b == '"':
				inString = false
			}
			continue
		}
		switch b {
		case '"':
			inString = true
		case '{', '[':
			depth++
			if depth > limit {
				return true
			}
		case '}', ']':
			if depth > 0 {
				depth--
			}
		}
	}
	return false
}

func validRPCID(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return true
	}
	if len(raw) > 512 {
		return false
	}
	switch raw[0] {
	case '"':
		return true // The envelope parser already checked the JSON string.
	case 'n':
		return bytes.Equal(raw, []byte("null"))
	default:
		_, err := strconv.ParseInt(string(raw), 10, 64)
		return err == nil
	}
}

func parseRPCParams(raw json.RawMessage, target any) error {
	if len(raw) == 0 || raw[0] != '{' {
		return errRPCInvalid
	}
	if rest, err := fastjson.Parse(raw, target, fastjson.ZeroCopy); err != nil || len(rest) != 0 {
		return errRPCInvalid
	}
	return nil
}

func exactRPCKeys(value []byte, allowed []string) bool {
	if len(value) == 0 || value[0] != '{' {
		return false
	}
	tokens := fastjson.NewTokenizer(value)
	defer tokens.Reset(nil)
	var seen uint8
	for tokens.Next() {
		if tokens.Depth != 1 || !tokens.IsKey {
			continue
		}
		index := slices.Index(allowed, string(tokens.String()))
		if index < 0 || seen&(1<<index) != 0 {
			return false
		}
		seen |= 1 << index
	}
	return tokens.Err == nil
}

// sdkProtocolError identifies JSON-RPC protocol errors. Tool results carry an
// entirely different result envelope. Their factual TEACH content is preserved.
func sdkProtocolError(raw []byte) bool {
	// The SDK's single JSON-RPC response writes put the result/error key
	// beside its version and ID. Allow for worst-case escaping of the ID.
	prefix := raw[:min(len(raw), 4<<10)]
	tokens := fastjson.NewTokenizer(prefix)
	defer tokens.Reset(nil)
	for tokens.Next() {
		if !tokens.IsKey || tokens.Depth != 1 {
			continue
		}
		switch string(tokens.String()) {
		case "result":
			return false
		case "error":
			return true
		}
	}
	return false
}

type limitedWriter struct {
	http.ResponseWriter
	requestID json.RawMessage
	status    int
	written   int
	started   bool
	sanitized bool
}

func (w *limitedWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *limitedWriter) WriteHeader(status int) {
	if !w.started && w.status == 0 {
		w.status = status
	}
}

func (w *limitedWriter) finish() {
	if w.started {
		return
	}
	w.started = true
	if w.status != 0 {
		w.ResponseWriter.WriteHeader(w.status)
	}
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	if w.sanitized {
		return len(p), nil
	}
	if !w.started {
		switch {
		case sdkProtocolError(p):
			return w.writeSafeProtocolError(p)
		case w.status >= http.StatusBadRequest:
			return w.writeSafeError(p, []byte("MCP protocol rejected the request.\n"), "text/plain; charset=utf-8")
		}
		w.finish()
	}
	if len(p) > maxMCPResponse-w.written {
		return 0, errors.New("MCP response exceeds configured bound")
	}
	n, err := w.ResponseWriter.Write(p)
	w.written += n
	return n, err
}

func (w *limitedWriter) writeSafeProtocolError(original []byte) (int, error) {
	var sdk struct {
		Error struct {
			Code int64 `json:"code"`
		} `json:"error"`
	}
	if len(original) <= maxMCPError {
		_, _ = fastjson.Parse(original, &sdk, fastjson.ZeroCopy)
	}
	if sdk.Error.Code == 0 {
		sdk.Error.Code = -32600 // JSON-RPC Invalid Request.
	}
	id := w.requestID
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	type versionNegotiation struct {
		Supported []string `json:"supported"`
	}
	type protocolError struct {
		Code    int64               `json:"code"`
		Message string              `json:"message"`
		Data    *versionNegotiation `json:"data,omitempty"`
	}
	response := protocolError{Code: sdk.Error.Code, Message: "MCP protocol rejected the request."}
	if sdk.Error.Code == mcp.CodeUnsupportedProtocolVersion {
		// Advertise only server-owned versions so newer clients can negotiate.
		response.Data = &versionNegotiation{Supported: builderProtocolVersions}
	}
	safe, _ := json.Marshal(struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Error   protocolError   `json:"error"`
	}{
		JSONRPC: "2.0", ID: id,
		Error: response,
	})
	return w.writeSafeError(original, safe, "application/json")
}

func (w *limitedWriter) writeSafeError(original, safe []byte, contentType string) (int, error) {
	w.sanitized = true
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(safe)))
	w.finish()
	if _, err := w.ResponseWriter.Write(safe); err != nil {
		return 0, err
	}
	return len(original), nil
}
