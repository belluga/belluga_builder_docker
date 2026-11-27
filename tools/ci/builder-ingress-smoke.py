#!/usr/bin/env python3
"""Assertions for the disposable Builder Compose ingress rehearsal."""

import base64
from concurrent.futures import ThreadPoolExecutor
import http.client
import http.server
import json
import os
import socket
import ssl
import subprocess
import sys
from threading import Event
import time
import uuid


HOST = "builder-ci.test"
COMPANY = "ingress-fixture"
PROJECT = "release-smoke"
TODO = "https-ingress-smoke"
TITLE = "Disposable Builder ingress TODO"
TOKEN_VAR = "BUILDER_MCP_BEARER_TOKEN"
PROTOCOL = "2025-11-25"
CURL = "curlimages/curl:8.15.0@sha256:4026b29997dc7c823b51c164b71e2b51e0fd95cce4601f78202c513d97da2922"
RPC_CONTENT = {"Content-Type": "application/json", "Accept": "application/json, text/event-stream"}
PEERS = {
    "builder-ingress": {"ingress-egress", "ingress-viewer", "ingress-mcp"},
    "builder-viewer": {"ingress-viewer", "viewer-registry"},
    "builder-mcp": {"ingress-mcp", "mcp-registry"},
    "builder-registry": {"viewer-registry", "mcp-registry", "registry-reader"},
    "project-reader": {"registry-reader"},
    "registry-migrate": {"migration-egress"},
}


def require(condition, label):
    if not condition:
        raise AssertionError(label)


def topology(tunnel):
    config = json.load(sys.stdin)
    services = config["services"]
    require(set(services) == set(PEERS), "unexpected service in base Compose")
    for name, networks in PEERS.items():
        service = services[name]
        wanted = networks | ({"tunnel-ingress"} if tunnel and name == "builder-ingress" else set())
        require(set(service["networks"]) == wanted, f"incorrect networks for {name}")
        ports = service.get("ports", [])
        require({(entry["target"], entry["published"], entry["protocol"]) for entry in ports} ==
                ({(80, "80", "tcp"), (443, "443", "tcp")} if name == "builder-ingress" else set()),
                f"incorrect published ports for {name}")
        require("build" not in service and "@sha256:" in service["image"],
                f"{name} is not configured to run a digest-pinned image")
    for name in ("ingress-viewer", "ingress-mcp", "viewer-registry", "mcp-registry"):
        require(config["networks"][name].get("internal"), f"{name} must be internal")
    require({name for name, net in config["networks"].items() if net.get("external")} ==
            ({"tunnel-ingress"} if tunnel else set()), "unexpected external network")
    require(any(mount.get("type") == "volume" and mount.get("target") == "/data"
                for mount in services["builder-ingress"]["volumes"]), "missing persistent TLS storage")
    print(f"PASS Compose topology ({'optional tunnel' if tunnel else 'direct'})", flush=True)


def connection(port, context=None):
    if context is None:
        conn = http.client.HTTPConnection(HOST, port, timeout=5)
    else:
        conn = http.client.HTTPSConnection(HOST, port, timeout=5, context=context)
    conn._create_connection = lambda address, timeout, source: socket.create_connection(
        ("127.0.0.1", address[1]), timeout, source)
    return conn


def request(port, context, path, method="GET", headers=None, payload=None):
    conn = connection(port, context)
    try:
        conn.request(method, path, None if payload is None else json.dumps(payload),
                     {"Host": HOST} | (headers or {}))
        response = conn.getresponse()
        return response.status, dict((key.lower(), value) for key, value in response.getheaders()), response.read()
    finally:
        conn.close()


def viewer_ready(port, context, source_sha):
    for attempt in range(45):
        try:
            status, _, body = request(port, context, "/api/build")
        except OSError:
            status = None
        if status == 200:
            require(json.loads(body)["source_sha"] == source_sha, "Viewer source SHA mismatch")
            return
        if status not in (None, 502, 503):
            raise AssertionError(f"unexpected Viewer status {status}")
        if attempt < 44:
            time.sleep(1)
    raise AssertionError("Viewer did not become ready through trusted HTTPS")


def docker_output(*args):
    return subprocess.check_output(["docker", *args], text=True)


def running_topology(project):
    ids = docker_output("ps", "-aq", "--filter", f"label=com.docker.compose.project={project}").split()
    rows = json.loads(docker_output("inspect", *ids))
    names = set()
    for row in rows:
        name = row["Config"]["Labels"]["com.docker.compose.service"]
        names.add(name)
        networks = PEERS.get(name, set()) | (
            {"fixture-mongo"} if name in {"mongo", "builder-registry", "registry-migrate"} else set()
        )
        require(set(row["NetworkSettings"]["Networks"]) == {f"{project}_{network}" for network in networks},
                f"incorrect runtime network membership for {name}")
        if name == "registry-migrate":
            require(row["State"]["Status"] == "exited" and row["State"]["ExitCode"] == 0,
                    "disposable Registry migration did not complete")
        else:
            require(row["State"]["Status"] == "running", f"{name} is not running in the disposable Compose")
        ports = row["HostConfig"]["PortBindings"] or {}
        if name == "builder-ingress":
            require(set(ports) == {"80/tcp", "443/tcp"} and all(
                binding["HostIp"] == "127.0.0.1" for entries in ports.values() for binding in entries
            ), "ingress smoke ports must bind to loopback")
        else:
            require(not ports, f"running application unexpectedly publishes a port: {name}")
    require(names == (set(PEERS) | {"mongo"}), "unexpected runtime Compose services")
    print("PASS running Compose network membership, migration and ingress-only loopback ports", flush=True)


def seed(project):
    private = f"http://builder-registry:8080/internal/v1/companies/{COMPANY}"

    def api(path, body=None):
        args = ["run", "--rm", "--network", f"{project}_mcp-registry", CURL,
                "--silent", "--show-error", "--fail-with-body", "--max-time", "20"]
        if body is not None:
            args.extend(["-H", "Content-Type: application/json", "--data", json.dumps(body)])
        output = docker_output(*args, private + path)
        return json.loads(output)

    api("/projects", {"company_id": COMPANY, "company_name": "Disposable Company",
                      "project_id": PROJECT, "project_name": "Ingress test Project"})
    route = f"/projects/{PROJECT}/todos"
    fixture = api(route + "/template")["example"]
    fixture["operation_id"] = "create-ingress-smoke"
    fixture["todo"].update(id=TODO, title=TITLE)
    return api(route, fixture)["content_digest"]


def encoded(value):
    return base64.urlsafe_b64encode(json.dumps(value, separators=(",", ":")).encode()).decode().rstrip("=")


def live(project, http_port, https_port, context, source_sha):
    viewer_ready(https_port, context, source_sha)
    running_topology(project)
    state = encoded({"company_id": COMPANY, "project_id": PROJECT, "operation_id": "fixture-callback",
                     "nonce": base64.urlsafe_b64encode(os.urandom(32)).decode().rstrip("=")})
    callback = f"/admin/github/callback?setup_action=request&state={state}"
    status, headers, _ = request(http_port, None, callback)
    require(status == 308 and headers.get("location") == f"https://{HOST}{callback}",
            "HTTP redirect must preserve callback scheme, host, path and query")
    status, headers, body = request(https_port, context, "/")
    require(status == 200 and "text/html" in headers.get("content-type", "") and b"<html" in body.lower(),
            "anonymous Viewer HTML unavailable")
    for path in ("/admin/github", callback, "/admin/github/oauth/callback?state=fixture&code=fixture"):
        status, _, body = request(https_port, context, path)
        require(status == 401 and json.loads(body)["error"] == "github_operator_authentication_required",
                f"Viewer must require Access identity for {path}")
    status, headers, body = request(https_port, context, callback, headers={"Cf-Access-Jwt-Assertion": "invalid"})
    require(status == 401 and headers.get("referrer-policy") == "no-referrer"
            and json.loads(body)["error"] == "github_callback_rejected",
            f"callback should reach Registry identity validation; observed status {status} and body {body!r}")
    print("PASS trusted HTTPS Viewer, HTTP redirect, callback routing and Access rejection", flush=True)

    init = {"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {
        "protocolVersion": PROTOCOL, "capabilities": {}, "clientInfo": {"name": "CI smoke", "version": "1"},
    }}
    auth = {"Authorization": "Bearer " + os.environ[TOKEN_VAR]}
    origin = {"Origin": f"https://{HOST}"}
    status, headers, _ = request(https_port, context, "/mcp", "POST", RPC_CONTENT, init)
    require(status == 401 and headers.get("www-authenticate") == 'Bearer realm="builder-mcp"',
            "exact /mcp must require bearer")
    status, _, body = request(https_port, context, "/mcp", "POST", RPC_CONTENT | auth | {"Origin": "https://outside.test"}, init)
    require(status == 403 and b"Origin" in body and b"allowlist" in body, "foreign Origin reached MCP")
    status, _, body = request(https_port, context, "/mcp", "POST", RPC_CONTENT | auth | {"Host": "outside.test"}, init)
    require(status == 421 and b'"protocolVersion"' not in body, "foreign Host reached MCP")
    status, _, body = request(https_port, context, "/mcp?fixture=1", "POST", RPC_CONTENT | auth, init)
    require(status == 404 and body == b"404 page not found\n", "MCP must receive original query and reject it")
    for path in ("/mcp/", "/mcp/subpath", "/mcp-extra", "/MCP", "/m%63p"):
        status, headers, _ = request(https_port, context, path)
        require(status in (200, 404) and headers.get("content-security-policy", "").startswith("default-src 'self'"),
                f"non-MCP path {path} did not reach Viewer (status {status})")

    digest = seed(project)
    session = str(uuid.uuid4())
    identity = {"company_id": COMPANY, "project_id": PROJECT}
    selection = encoded(identity | {"session_id": session, "owner_reference": "Disposable CI fixture"})
    carrier = {"X-Delphi-Session-Type": "codex", "X-Delphi-Session-ID": session}
    status, session_headers, body = request(https_port, context, "/mcp", "POST", RPC_CONTENT | auth | origin | carrier, init)
    require(status == 200, f"MCP initialize failed with {status}")
    initialized = json.loads(body)
    require(initialized["result"]["protocolVersion"] == PROTOCOL
            and initialized["result"]["serverInfo"]["name"] == "builder", "unexpected MCP initialization identity")
    protocol = {"MCP-Protocol-Version": PROTOCOL}
    if session_headers.get("mcp-session-id"):
        protocol["Mcp-Session-Id"] = session_headers["mcp-session-id"]

    def mcp(method, params=None, extra=None):
        payload = {"jsonrpc": "2.0", "method": method}
        if params is not None:
            payload["params"] = params
        if method != "notifications/initialized":
            payload["id"] = 2
        status, _, body = request(https_port, context, "/mcp", "POST",
                                  RPC_CONTENT | auth | origin | carrier | protocol | (extra or {}), payload)
        if method == "notifications/initialized":
            require(status == 202 and not body, "MCP initialization notification failed")
            return None
        require(status == 200, f"MCP {method} returned {status}")
        return json.loads(body)["result"]

    def call(name, args, extra=None, error=False):
        result = mcp("tools/call", {"name": name, "arguments": args}, extra)
        require(result.get("isError", False) == error, f"unexpected MCP result for {name}: {result}")
        return json.loads(result["content"][0]["text"])

    mcp("notifications/initialized")
    tools = mcp("tools/list")["tools"]
    require(any(tool["name"] == "builder_todo_get" and tool["annotations"]["readOnlyHint"] for tool in tools),
            "tools/list did not expose the read-only TODO tool")
    read_args = identity | {"todo_id": TODO}
    unknown = call("builder_todo_get", read_args, error=True)
    require(unknown["teach_detail"]["code"] == "session_unavailable_or_unknown", "unbound session read succeeded")
    bound = call("builder_session_bind", identity | {"operation_id": "bind-ingress-smoke", "expected_revision": 0},
                 {"X-Delphi-Confirmed-Selection": selection})
    require(bound["data"]["binding"] == identity and bound["data"]["external_id"] == session,
            "MCP session bound to the wrong Project or native identity")

    def todo_read():
        data = call("builder_todo_get", read_args)["data"]
        require(data["todo"]["id"] == TODO and data["todo"]["title"] == TITLE
                and data["content_digest"] == digest, "readback must match the Registry fixture")
        return data

    todo_read()
    with ThreadPoolExecutor(max_workers=3) as pool:
        check_viewer = pool.submit(viewer_ready, https_port, context, source_sha)
        reads = [pool.submit(todo_read) for _ in range(2)]
        check_viewer.result()
        require(reads[0].result() == reads[1].result(), "concurrent exact TODO reads disagree")
    print("PASS exact MCP routing, Host/Origin/bearer rejection, initialize, tools/list and concurrent bound TODO reads",
          flush=True)


def emitter():
    release = Event()

    class Stream(http.server.BaseHTTPRequestHandler):
        def end_headers(self):
            self.send_header("X-Builder-Stream-Fixture", "true")
            super().end_headers()

        def do_GET(self):
            if self.path == "/mcp?release=1":
                release.set()
                self.send_response(204)
                self.end_headers()
                return
            if self.path != "/mcp":
                self.send_error(404)
                return
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.end_headers()
            self.wfile.write(b"data: first\n\n")
            self.wfile.flush()
            if release.wait(10):
                self.wfile.write(b"data: second\n\n")
                self.wfile.flush()

    http.server.ThreadingHTTPServer(("0.0.0.0", 8098), Stream).serve_forever()


def stream(port, context, source_sha):
    for attempt in range(20):
        conn = connection(port, context)
        conn.request("GET", "/mcp", headers={"Host": HOST, "Accept": "text/event-stream"})
        response = conn.getresponse()
        if response.status != 502:
            break
        conn.close()
        time.sleep(1)
    try:
        require(response.status == 200 and response.getheader("Content-Type") == "text/event-stream",
                "SSE fixture did not become available through Caddy")
        require(response.readline() == b"data: first\n" and response.readline() == b"\n",
                "Caddy did not flush the first event while the stream was open")
        viewer_ready(port, context, source_sha)
        for path in ("/mcp/", "/mcp/subpath", "/mcp-extra", "/MCP", "/m%63p"):
            _, headers, _ = request(port, context, path)
            require("x-builder-stream-fixture" not in headers, f"non-MCP path {path} reached the SSE fixture")
        require(request(port, context, "/mcp?release=1")[0] == 204, "stream query was not forwarded")
        require(response.read() == b"data: second\n\n", "Caddy did not complete the SSE response")
    finally:
        conn.close()
    print("PASS Caddy flushed SSE events and served Viewer while the stream was open", flush=True)


def log_redaction(port, context, ingress_id):
    path = "/admin/github/oauth/callback?code=fixture-oauth-code&state=fixture-oauth-state"
    status, _, _ = request(port, context, path, headers={"Cf-Access-Jwt-Assertion": "fixture-access-assertion"})
    require(status == 502, "unavailable Viewer did not produce the proxy error used to test redaction")
    output = subprocess.check_output(["docker", "logs", ingress_id], stderr=subprocess.STDOUT, text=True)
    entries = [json.loads(line) for line in output.splitlines() if line.startswith("{")]
    require(any(entry.get("status") == 502 and entry.get("logger") == "http.log.error" for entry in entries),
            "Caddy did not retain the upstream HTTP error")
    require(all(marker not in output for marker in ("fixture-oauth-code", "fixture-oauth-state", "fixture-access-assertion")),
            "Caddy logged disposable callback or Access credentials")
    print("PASS HTTP error logging retains status and redacts callback and Access credentials", flush=True)


def main():
    mode, *args = sys.argv[1:]
    if mode == "topology":
        topology(args == ["tunnel"])
    elif mode == "live":
        project, http_port, https_port, root, source_sha = args
        live(project, int(http_port), int(https_port), ssl.create_default_context(cafile=root), source_sha)
    elif mode == "emitter":
        emitter()
    elif mode == "stream":
        https_port, root, source_sha = args
        stream(int(https_port), ssl.create_default_context(cafile=root), source_sha)
    elif mode == "redaction":
        https_port, root, ingress_id = args
        log_redaction(int(https_port), ssl.create_default_context(cafile=root), ingress_id)
    elif mode == "restart":
        https_port, root, source_sha = args
        viewer_ready(int(https_port), ssl.create_default_context(cafile=root), source_sha)
        print("PASS ingress recreation preserved the TLS CA used by the same trusted client", flush=True)
    else:
        raise ValueError(f"unknown smoke phase {mode}")


if __name__ == "__main__":
    main()
