# Builder MCP

Builder MCP exposes 70 typed tools over private Streamable HTTP. It uses the existing Registry for Project authority, conversation bindings, revisions, and TEACH diagnostics. The Go SDK is pinned to v1.8.0 in this separate Go module. The supported protocol versions are 2025-11-25 and 2026-07-28.

## Local configuration

Prepare the name and ID discovery indexes for the existing Companies. Use the Registry deployment's normal MongoDB environment and invoke its binary with the exact Company IDs.

```sh
registry-api discovery-indexes company-one company-two
```

A successful command returns `verified: true` and the selected Company IDs. Repeat preparation when adding a Company to discovery. Both the Company and tenant Project name indexes are created and verified by this explicit command.

Set `BUILDER_MCP_COMPANY_IDS` to the comma-separated Company IDs and inject `BUILDER_MCP_BEARER_TOKEN` from your local secret mechanism. The adapter requires a distinct bearer of at least 32 visible ASCII bytes. Launch only the optional MCP service from the project root once the private Registry is reachable.

```sh
docker compose -f compose.builder.yaml -f compose.builder.mcp.yaml --profile mcp up -d --no-deps builder-mcp
```

The overlay publishes only `127.0.0.1:8098/mcp` and uses the private Registry network. Standalone runs use `BUILDER_MCP_REGISTRY_URL` to set the fixed Registry origin. `BUILDER_MCP_LISTEN_ADDR` defaults to `127.0.0.1:8098`. `BUILDER_MCP_ALLOWED_HOSTS` and `BUILDER_MCP_ALLOWED_ORIGINS` accept comma-separated exact values. With no allowed origins supplied, requests without an Origin header are accepted. The adapter has a 32 MiB request ceiling and two concurrent HTTP requests. JSON-RPC control fields and metadata also share a 16 KiB limit outside tool arguments. Its container budget is 1 GiB with `GOMEMLIMIT=768MiB`.

Large Registry results retain the exact JSON TextContent contract. Both upstream calls may finish concurrently, while responses above 1 MiB are materialized and written one at a time. The next large response waits within the caller's existing request deadline; this bounds the SDK's expanded JSON buffers without lowering either Registry or MCP response limits.

## App Server API

Start the pinned local Codex App Server over stdio, call `initialize`, send `initialized`, and use `thread/start` or `thread/read` to obtain the API's native `thread.sessionId`. Attach the MCP server in the thread-scoped host configuration. The placeholder is replaced with the exact ID returned by the API.

```json
{
  "mcp_servers": {
    "builder": {
      "url": "http://127.0.0.1:8098/mcp",
      "bearer_token_env_var": "BUILDER_MCP_BEARER_TOKEN",
      "http_headers": {
        "X-Delphi-Session-Type": "codex",
        "X-Delphi-Session-ID": "<native thread.sessionId>"
      },
      "startup_timeout_sec": 20,
      "tool_timeout_sec": 300
    }
  }
}
```

Find the Company by exact ID or name. Ask the user for an exact ID if the result is ambiguous. Use the canonical Company ID and name returned by the search. Search Projects inside that Company. Once the user confirms the exact Company and Project, encode the following UTF-8 object as unpadded base64url and add it to `http_headers` under `X-Delphi-Confirmed-Selection`. The encoded header may contain at most 2,048 bytes. `project_name` is required for registration.

```json
{
  "session_id": "<native thread.sessionId>",
  "company_id": "company-one",
  "project_id": "project-one",
  "project_name": "Project One",
  "owner_reference": "<host record of explicit user confirmation>"
}
```

Apply the selected headers on an idle thread via a controlled cold resume and verify the effective context before issuing a write. A loaded thread can retain its previous configuration. Use `builder_project_register` for a new Project, then `builder_session_bind` with expected revision zero and a stable operation ID. Existing Projects can be bound directly after confirmation. The bound ID grants access only to that exact Company and Project. The same native ID preserves its binding on resume. A fork with a new ID needs its own confirmation and explicit binding.

Every tool returns one JSON TextContent item. Successful responses carry `http_status` and `data`. Registry errors preserve their status and bounded TEACH v1 fields. If a dispatched registration loses its response, call `builder_project_search` with the Company and Project IDs and compare the exact returned tuple before binding. An inconclusive search leaves the outcome unknown. For other uncertain writes, use the existing operation ID and the owning Registry readback or recovery contract.

## Verification

From this directory, run `go vet ./...` and `go test ./... -count=1`. The isolated App Server fixture requires the pinned binary, the fixture MCP URL and a JSON file declaring `company_id`, `company_name`, and a fresh `project_id_prefix`. Seed the test Company in a disposable Registry and run discovery index preparation for that Company before the fixture.

`testdata/catalog-contracts.json` freezes the input-schema digests for all 70 tools and 103 shared schema definitions. `registry_contract_test.go` compares the schema property names and JSON types against the Registry Go DTOs, then checks the frozen schema digests so required fields, enums, patterns, bounds, and object-closure rules cannot drift silently. A schema change requires reviewing the Registry route and validation sources first; the oracle is never regenerated by tests.

```sh
python3 tests/app_server_smoke.py --codex-bin /path/to/pinned/codex \
  --mcp-url http://127.0.0.1:8098/mcp \
  --fixture-context /path/to/disposable-fixture.json \
  --evidence /path/to/disposable-evidence.json
```
