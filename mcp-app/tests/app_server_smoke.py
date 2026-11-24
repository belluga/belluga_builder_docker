#!/usr/bin/env python3
"""Exercise the pinned local App Server against an isolated Builder MCP fixture."""

import argparse
import base64
import contextlib
import hashlib
import http.client
import http.server
import itertools
import json
import os
from pathlib import Path
import queue
import random
import struct
import subprocess
import tempfile
import threading
import time
from urllib.parse import urlsplit
import zlib


SERVER = "builder_fixture"
MODEL = "builder-local-scripted-fixture"
VERSION = "0.171.0-alpha.13"
TOKEN_VARIABLE = "BUILDER_MCP_BEARER_TOKEN"
INTERRUPT_MARKER = "Interrupt this owned Builder fixture turn."
TOOL_NAMES = [tool["name"] for tool in json.loads(
    (Path(__file__).resolve().parents[1] / "catalog.json").read_text()
)["tools"]]
SESSION_FIELDS = ("id", "sessionId", "ephemeral", "forkedFromId", "attachedToThreadId")


def identity(thread):
    return {key: thread.get(key) for key in SESSION_FIELDS}


def canonical_json(value):
    return json.dumps(value, ensure_ascii=False, separators=(",", ":"))


class Scenario:
    def __init__(self, args):
        self.args = args
        self.mcp = urlsplit(args.mcp_url)
        if (self.mcp.scheme != "http" or self.mcp.hostname not in {"127.0.0.1", "localhost", "::1"}
                or self.mcp.path != "/mcp" or self.mcp.query or self.mcp.fragment or self.mcp.username):
            raise ValueError("the validation target must be a disposable loopback HTTP /mcp listener")
        if not self.mcp.port:
            raise ValueError("provide the disposable port explicitly")
        self.context = json.loads(args.fixture_context.read_text())
        self.token = os.environ.get(TOKEN_VARIABLE, "")
        if len(self.token) < 32:
            raise ValueError(f"set {TOKEN_VARIABLE} to the isolated fixture bearer")
        self.model_counter = itertools.count(1)
        self.actions = {}
        self.interrupt_arrived = threading.Event()
        self.interrupt_release = threading.Event()
        self.evidence = {"checks": [], "http": [], "rpc": [], "turns": []}

    def require(self, value, message):
        self.evidence["checks"].append({"condition": message, "passed": bool(value)})
        if not value:
            raise AssertionError(message)

    def action(self, rpc, thread, tool, arguments, policy="accept"):
        marker = "Builder fixture action " + str(next(self.model_counter))
        self.actions[marker] = (tool, arguments, "call-" + marker.rsplit(" ", 1)[-1])
        rpc.policy = policy
        turn_id, items = rpc.turn(thread["id"], marker)
        row = {"tool": tool, "session_id": thread["sessionId"], "thread_id": thread["id"],
               "turn_id": turn_id, "declared_policy": policy,
               "operation_id": arguments.get("operation_id", arguments.get("body", {}).get("operation_id")),
               "calls": items}
        self.evidence["turns"].append(row)
        return items

    def write_action(self, rpc, thread, tool, arguments, policy="accept", error=False):
        offset = len(self.evidence["http"])
        items = self.action(rpc, thread, tool, arguments, policy)
        calls = [item for item in self.evidence["http"][offset:] if item["method"] == "tools/call"]
        if policy in {"decline", "cancel"}:
            self.require(not calls, f"{policy} produced no HTTP tool call")
            self.require(any(row.get("decision") == policy for row in self.evidence["rpc"]),
                         f"App Server emitted a native prompt handled as {policy}")
            return
        self.require(len(calls) == 1 and calls[0]["tool"] == tool and calls[0]["session_id"] == thread["sessionId"],
                     f"{tool} reached MCP exactly once with the active native ID")
        self.require(len(items) == 1 and items[0]["tool"] == tool, f"{tool} emitted a native agent mcpToolCall item")
        result = calls[0]["response"]
        self.require(error == bool(result.get("isError")), f"{tool} preserved the expected outcome")
        return json.loads(result["content"][0]["text"])

    def run(self, host, home, cwd):
        company = self.context["company_id"]
        company_name = self.context["company_name"]
        prefix = self.context["project_id_prefix"]
        threads = []
        with RPC(self, home, cwd, "seed") as rpc:
            for label in ("root-one", "root-two"):
                raw = rpc.call("thread/start", {"cwd": str(cwd), "ephemeral": False,
                                               "historyMode": "paginated"})["thread"]
                thread = identity(raw) | {"label": label}
                seed_id, _ = rpc.turn(thread["id"], "Persist this isolated fixture.")
                self.require(rpc.call("thread/read", {"threadId": thread["id"]})["thread"]["sessionId"] ==
                             thread["sessionId"], f"{label} has a readable native identity")
                threads.append(thread)
                if label == "root-one":
                    for fork_label, options in (
                        ("ordinary", {}), ("last-turn", {"lastTurnId": seed_id}),
                        ("attached", {"attachToSource": True}),
                    ):
                        fork = rpc.call("thread/fork", {"threadId": thread["id"], "ephemeral": False,
                                                       "excludeTurns": True} | options)["thread"]
                        child = identity(fork) | {"label": fork_label}
                        self.require(child["sessionId"] != thread["sessionId"] and child["forkedFromId"] == thread["id"],
                                     f"{fork_label} has its own native ID and source lineage")
                        rpc.turn(child["id"], "Persist this isolated fork.")
                        threads.append(child)
            self.require(not self.evidence["http"], "the isolated threads were seeded with MCP off")
            for thread in threads:
                rpc.call("thread/unsubscribe", {"threadId": thread["id"]})

        for phase in ("discovery", "confirmed", "resumed"):
            with RPC(self, home, cwd, phase) as rpc:
                for thread in threads:
                    label = thread["label"]
                    project = prefix + "-" + label
                    project_name = "Fixture " + project
                    choice = None if phase == "discovery" else base64.urlsafe_b64encode(canonical_json({
                        "session_id": thread["sessionId"], "company_id": company, "project_id": project,
                        "project_name": project_name, "owner_reference": "fixture host confirmation " + label,
                    }).encode()).decode().rstrip("=")
                    headers = {"X-Delphi-Session-Type": "codex", "X-Delphi-Session-ID": thread["sessionId"]}
                    if choice is not None:
                        headers["X-Delphi-Confirmed-Selection"] = choice
                    config = {"mcp_servers": {SERVER: {
                        "url": host + "/mcp", "http_headers": headers, "bearer_token_env_var": TOKEN_VARIABLE,
                        "startup_timeout_sec": 20, "tool_timeout_sec": 300, "required": True,
                    }}}
                    native = rpc.call("thread/resume", {"threadId": thread["id"], "config": config,
                                                       "excludeTurns": True})["thread"]
                    self.require(identity(native) == {key: thread[key] for key in SESSION_FIELDS},
                                 f"{phase}/{label} preserved native identity")
                    status = rpc.call("mcpServerStatus/list", {"threadId": thread["id"], "serverName": SERVER})
                    names = sorted(status["data"][0]["tools"]) if status["data"] else []
                    self.require(names == TOOL_NAMES and not status.get("nextCursor"),
                                 f"{phase}/{label} sees all 70 tools in one list")
                    offset = len(self.evidence["http"])
                    found = rpc.read_tool(thread, "builder_company_search", {"id": company})
                    calls = [item for item in self.evidence["http"][offset:] if item["method"] == "tools/call"]
                    self.require(len(calls) == 1 and calls[0]["session_id"] == thread["sessionId"] and
                                 calls[0]["session_type"] == "codex" and calls[0]["token_matches"] and
                                 calls[0]["selection"] == choice and calls[0]["protocol"] == "2025-11-25",
                                 f"{phase}/{label} sent exact effective native and confirmation headers")
                    self.require(found["data"]["items"] == [{"id": company, "name": company_name}],
                                 f"{phase}/{label} read the exact canonical Company")

                    route_ids = {"company_id": company, "project_id": project}
                    register = {"company_id": company, "body": route_ids | {
                        "company_name": company_name, "project_name": project_name,
                    }}
                    bind = route_ids | {"expected_revision": 0, "operation_id": "bind-" + label}
                    if phase == "discovery":
                        denied = rpc.read_tool(thread, "builder_project_get", route_ids, error=True)
                        self.require(denied["teach_detail"]["code"] == "session_unavailable_or_unknown",
                                     f"{label} cannot read a Project before its own binding")
                        if label == "root-one":
                            bad = self.write_action(rpc, thread, "builder_project_register", register, error=True)
                            self.require(bad["teach_detail"]["code"] == "selection_required_or_mismatched",
                                         "missing host confirmation rejects registration")
                            for query_name, expected in self.context.get("company_name_cases", {}).items():
                                result = rpc.read_tool(thread, "builder_company_search", {"name": query_name, "limit": 1})
                                self.require(result["data"]["match_state"] == expected, f"Company name result {expected}")
                    elif phase == "confirmed":
                        if label == "root-one":
                            self.write_action(rpc, thread, "builder_project_register", register, policy="decline")
                            self.write_action(rpc, thread, "builder_project_register", register, policy="cancel")
                            wrong = {"company_id": company, "body": register["body"] | {"project_name": "Wrong selection"}}
                            denied = self.write_action(rpc, thread, "builder_project_register", wrong, error=True)
                            self.require(denied["teach_detail"]["code"] == "selection_required_or_mismatched",
                                         "mismatched host selection rejects registration")
                        created = self.write_action(rpc, thread, "builder_project_register", register)
                        self.require(created["data"]["id"] == project and created["data"]["name"] == project_name,
                                     f"{label} registered the selected canonical tuple")
                        lookup = rpc.read_tool(thread, "builder_project_search", {"company_id": company, "id": project})
                        self.require(lookup["data"]["items"] == [{"id": project, "name": project_name, "company_id": company}],
                                     f"{label} read the exact Project tuple before binding")
                        bound = self.write_action(rpc, thread, "builder_session_bind", bind)
                        self.require(bound["data"]["binding"] == route_ids and bound["data"]["external_id"] == thread["sessionId"],
                                     f"{label} bound only its own native ID")
                    if phase != "discovery":
                        project_record = rpc.read_tool(thread, "builder_project_get", route_ids)
                        self.require(project_record["data"]["id"] == project, f"{phase}/{label} uses its exact bound Project")
                        if phase == "confirmed" and label == "root-one":
                            self.owner_journeys(rpc, thread, route_ids)
                        if phase == "resumed" and label == "root-one":
                            rpc.interrupt_fixture_turn(thread["id"])
                            repeated = rpc.read_tool(thread, "builder_project_get", route_ids)
                            self.require(repeated["data"]["id"] == project, "a read after turn interruption kept the binding")
                        if label in {"ordinary", "last-turn", "attached"}:
                            root_ids = route_ids | {"project_id": prefix + "-root-one"}
                            denied = rpc.read_tool(thread, "builder_project_get", root_ids, error=True)
                            self.require(denied["teach_detail"]["code"] == "session_project_mismatch",
                                         f"{label} lacks authorization to use its source's Project")
                for thread in threads:
                    rpc.call("thread/unsubscribe", {"threadId": thread["id"]})


    def authenticated_readonly(self, host, cwd):
        """Use native local authentication internally on one owned fixture thread."""
        company = self.context["company_id"]
        read = "builder_company_search"

        def configuration(session=None, disabled=()):
            config = {"features": {"apps": False, "plugins": False},
                      "mcp_servers": {server: {"enabled": False} for server in disabled}}
            if session is not None:
                headers = {"X-Delphi-Session-Type": "codex", "X-Delphi-Session-ID": session}
                config["mcp_servers"][SERVER] = {
                    "url": host + "/mcp", "http_headers": headers, "bearer_token_env_var": TOKEN_VARIABLE,
                    "enabled_tools": [read], "startup_timeout_sec": 20, "tool_timeout_sec": 300, "required": True,
                }
            return config

        thread = None
        try:
            with RPC(self, None, cwd, "authenticated-seed") as rpc:
                thread = rpc.call("thread/start", {"cwd": str(cwd), "sandbox": "read-only", "ephemeral": False,
                                                   "historyMode": "paginated", "config": configuration(),
                                                   "developerInstructions":
                                                   "This is an isolated read-only Builder MCP fixture. Do not read files,"
                                                   " run commands, or access external data. Only use the single read-only"
                                                   " Builder fixture tool if the prompt explicitly requests it."})["thread"]
                seed, items = rpc.turn(thread["id"], "Disposable Builder MCP fixture. Reply with the single word ready. Use no tools.")
                self.require(not items, "authenticated host created the owned fixture without Builder tools")
                self.evidence["seed_turn"] = seed
                rpc.call("thread/unsubscribe", {"threadId": thread["id"]})
            with RPC(self, None, cwd, "authenticated-inventory") as rpc:
                rpc.call("thread/resume", {"threadId": thread["id"], "excludeTurns": True,
                                           "config": configuration(thread["sessionId"])})
                inventory = rpc.call("mcpServerStatus/list", {"threadId": thread["id"], "detail": "toolsAndAuthOnly"})
                disabled = {entry["name"] for entry in inventory["data"] if entry["name"] != SERVER}
                self.require(not inventory.get("nextCursor"), "all inherited server status entries were inspected")
                rpc.call("thread/unsubscribe", {"threadId": thread["id"]})
            with RPC(self, None, cwd, "authenticated-cold-resume") as rpc:
                native = rpc.call("thread/resume", {"threadId": thread["id"], "excludeTurns": True,
                                                   "config": configuration(thread["sessionId"], disabled)})["thread"]
                self.require(identity(native) == identity(thread), "authenticated resumed fixture uses the original native ID")
                status = rpc.call("mcpServerStatus/list", {"threadId": thread["id"], "detail": "toolsAndAuthOnly"})
                selected = [entry for entry in status["data"] if entry["name"] == SERVER]
                others = [entry for entry in status["data"] if entry["name"] != SERVER]
                self.evidence["authenticated_inventory"] = {
                    "fixture_tool_counts": [len(entry["tools"]) for entry in selected],
                    "other_statuses": [{"status": entry.get("runtimeStatus"), "count": len(entry["tools"])} for entry in others],
                }
                self.require(not status.get("nextCursor") and all(
                    entry.get("runtimeStatus") == "disabled" and not entry["tools"] for entry in others
                ), "all inherited registrations are disabled with zero tools")
                self.require(len(selected) == 1 and list(selected[0]["tools"]) == [read],
                             "authenticated fixture offers only the synthetic read-only MCP tool")
                offset = len(self.evidence["http"])
                found = rpc.read_tool(thread, read, {"id": company})
                calls = [event for event in self.evidence["http"][offset:] if event["method"] == "tools/call"]
                self.require(len(calls) == 1 and calls[0]["session_id"] == thread["sessionId"],
                             "authenticated cold resume applied its exact native identity before model use")
                self.require(found["data"]["items"] == [{"id": company, "name": self.context["company_name"]}],
                             "authenticated fixture request returned only the synthetic Company")
                offset = len(self.evidence["http"])
                turn_id, items = rpc.turn(thread["id"], f"Call the Builder fixture Company search by exact id {company}.")
                calls = [event for event in self.evidence["http"][offset:] if event["method"] == "tools/call"]
                self.require(len(calls) == 1 and calls[0]["tool"] == read and calls[0]["session_id"] == thread["sessionId"],
                             "native authenticated model dispatched the exact synthetic read")
                self.require(len(items) == 1 and items[0]["tool"] == read and items[0]["status"] == "completed",
                             "native authenticated model completed the one read-only tool")
                self.evidence["turns"].append({"turn_id": turn_id, "thread_id": thread["id"],
                                               "session_id": thread["sessionId"], "tool": read})
                rpc.call("thread/unsubscribe", {"threadId": thread["id"]})
        finally:
            if thread is not None:
                self.evidence["owned_fixture_thread"] = thread["id"]
                try:
                    with RPC(self, None, cwd, "archive-owned-fixture") as rpc:
                        rpc.call("thread/archive", {"threadId": thread["id"]})
                    self.evidence["owned_fixture_archived"] = True
                except (RuntimeError, queue.Empty) as exc:
                    self.evidence["owned_fixture_archive_error"] = str(exc)

    def owner_journeys(self, rpc, thread, ids):
        project = ids["project_id"]
        generation = "fixture-knowledge"
        actor = "builder-mcp-fixture"
        payloads = {
            "project_mandate": (project, {"purpose": "Own fixture work", "scope": ["fixture"]}),
            "domain_catalog": (project, {
                "entities": [{"id": "Project", "name": "Project", "description": ""}],
                "relations": [{"id": "OWNS", "from": "Project", "to": "Project", "kind": "owns"}],
                "distinctions": [],
            }),
            "project_constitution": (project, {"promotion_mode": "mvp", "capability_namespaces": ["go"],
                                               "rules": [{"id": "tenant-scope", "status": "current", "statement": "Scope reads"}]}),
            "strategic_roadmap": (project, {"stages": [{"id": "launch", "name": "Launch", "status": "planned"}]}),
            "module": ("projects", {"purpose": "Owns fixture projects",
                                    "decisions": [{"id": "DEC-FIXTURE", "status": "current",
                                                   "statement": "Projects own routes."}]}),
            "policy": ("access-policy", {"rules": [{"id": "retention", "status": "current", "statement": "Keep audit records"}]}),
        }
        records = [{"family": family, "record": {"id": name, "project_id": project, "generation_id": generation,
                                                  "revision": 1, "payload": payload}}
                   for family, (name, payload) in payloads.items()]
        pending = rpc.read_tool(thread, "builder_knowledge_manifest", ids)["data"]
        self.require(pending["state"] == "pending", "2xx pending manifest remains a successful read")
        draft_body = {"project_id": project, "generation_id": generation, "actor": actor,
                      "operation_id": "fixture-draft", "records": records}
        invalid = records[0] | {"record": records[0]["record"] | {"payload": {"scope": ["fixture"]}}}
        invalid_body = draft_body | {"records": [invalid], "operation_id": "fixture-draft-invalid"}
        invalid_result = self.write_action(rpc, thread, "builder_knowledge_draft_create",
                                           ids | {"body": invalid_body}, error=True)
        teach = invalid_result["teach_detail"]
        self.require(teach["field_path"] == "payload.purpose" and teach["effects"] == "none" and
                     teach["operation_id"] == "fixture-draft-invalid", "draft invalid preserved owner TEACH and prewrite facts")
        result = self.write_action(rpc, thread, "builder_knowledge_draft_create", ids | {"body": draft_body})
        self.require(result["data"]["state"] == "draft" and len(result["data"]["documents"]) == len(records),
                     "API-born draft recorded all expected families")
        replay = self.write_action(rpc, thread, "builder_knowledge_draft_create", ids | {"body": draft_body})
        self.require(replay["data"]["documents"] == result["data"]["documents"], "exact draft replay preserved the records")
        divergent = records[0] | {"record": records[0]["record"] | {
            "payload": records[0]["record"]["payload"] | {"purpose": "Divergent content under the same operation ID"},
        }}
        refused = self.write_action(rpc, thread, "builder_knowledge_draft_create",
                                    ids | {"body": draft_body | {"records": [divergent, *records[1:]]}}, error=True)
        self.require(refused["http_status"] == 409 and refused["teach_detail"]["operation_id"] == "fixture-draft",
                     "same Knowledge operation ID rejected divergent typed content")
        record = rpc.read_tool(thread, "builder_knowledge_get", ids | {
            "family": "module", "record_id": "projects", "generation_id": generation,
        })["data"]
        self.require(record["project_id"] == project and record["revision"] == 1, "read back exact draft module")
        activated = self.write_action(rpc, thread, "builder_knowledge_activate", ids | {"body": {
            "project_id": project, "generation_id": generation, "operation_id": "fixture-activate", "actor": actor,
        }})["data"]
        self.require(activated["state"] == "active" and activated["generation_id"] == generation,
                     "separate Registry activation published the exact draft generation")
        entity = rpc.read_tool(thread, "builder_knowledge_entity_get", ids | {"entity_id": "Project"})["data"]
        decision = rpc.read_tool(thread, "builder_knowledge_decision_get", ids | {
            "family": "module", "record_id": "projects", "decision_id": "DEC-FIXTURE",
        })["data"]
        self.require(entity["entity"]["id"] == "Project" and decision["decision"]["id"] == "DEC-FIXTURE",
                     "case-sensitive Registry projections retained their exact identities")
        history = rpc.read_tool(thread, "builder_knowledge_history", ids)["data"]
        count = sum(event.get("operation_id") == "fixture-draft" for event in history["events"])
        self.require(count == len(records), "draft replay created one history event per record")

        todo_id = "fixture-mcp-todo"
        todo = {
            "id": todo_id, "project_id": project, "title": "TODO: prove Builder MCP",
            "objective": "Prove the Registry TODO contract through the pinned client.",
            "next_exact_step": "Read current evidence.", "artifact_type": "tactical_execution_contract",
            "stage": "Planning", "qualifiers": [], "work_state": "planning", "sections": [],
            "proof_refs": [], "waiver_refs": [], "diff_expectation": [], "implementation_commits": [],
            "affected_files": [], "content": {"schema_version": 2},
        }
        created = self.write_action(rpc, thread, "builder_todo_create", ids | {"body": {
            "schema_version": "builder-todo-creation-v2", "operation_id": "fixture-create-todo", "todo": todo,
        }})["data"]
        revision = created["todo_revision"]
        todo_path = ids | {"todo_id": todo_id}
        todo_edit = {"project_id": project, "todo_id": todo_id, "expected_revision": revision,
                     "operation_id": "fixture-edit-todo", "reason": "Validate owner CAS and content edit.",
                     "content": {"schema_version": 2, "title": "TODO: verify Builder MCP",
                                 "objective": todo["objective"], "diff_expectation": [], "affected_files": []}}
        stale = self.write_action(rpc, thread, "builder_todo_update", todo_path | {"body":
                                  todo_edit | {"expected_revision": revision + 1, "operation_id": "fixture-stale-todo"}},
                                  error=True)
        self.require(stale["todo_revision"] == revision and stale["teach_detail"]["retry"] == "read_reassess",
                     "stale write returned exact owner revision and retry guidance")
        current = rpc.read_tool(thread, "builder_todo_get", todo_path)["data"]
        self.require(current["todo_revision"] == revision, "stale write created no new TODO revision")
        edited = self.write_action(rpc, thread, "builder_todo_update", todo_path | {"body": todo_edit})["data"]
        self.require(edited["todo_revision"] == revision + 1, "exact content edit advanced once")
        self.write_action(rpc, thread, "builder_todo_transition", todo_path | {"body": {
            "project_id": project, "todo_id": todo_id, "expected_revision": edited["todo_revision"],
            "operation_id": "fixture-transition", "requested_stage": "Implementation-Approval-Pending",
            "proof_refs": [], "waiver_refs": [],
        }}, error=True)
        post_gate = rpc.read_tool(thread, "builder_todo_get", todo_path)["data"]
        self.require(post_gate["todo_revision"] == edited["todo_revision"] and post_gate["todo"]["stage"] == "Planning",
                     "Registry gate rejection preserved current Planning revision")

        ds = rpc.read_tool(thread, "builder_design_system_state", ids)["data"]
        self.require("selection" in ds, "Design System returns owner state through the bound session")

        # A deterministic, incompressible PNG exercises the existing inline Overview
        # exception close to the 2 MiB slot constraint without filesystem access.
        overview = {"schema_version": "1", "tagline": "Fixture Overview", "summary": "Bound project fixture.",
                    "desktop_alt": "Fixture desktop", "mobile_alt": "Fixture mobile",
                    "sections": {name: {"heading": name, "body": "Fixture " + name, "points": []}
                                 for name in ("problem", "proposal", "impact", "audience")}}
        media = [{"slot": "desktop", "data": overview_png(1)}, {"slot": "mobile", "data": overview_png(2)}]
        overview_args = {"project_id": project, "expected_revision": 0, "operation_id": "fixture-overview",
                         "actor": actor, "content": overview, "media": media, "knowledge_origin": {
                             "project_id": project, "generation_id": generation, "documents": activated["documents"],
                         }, "source": {"origin_absent": True}}
        # Media is sent through the real MCP transport without injecting multi-MB
        # scripted model response items into the App Server fixture.
        response = self.direct_tool(thread, "builder_overview_write", ids | {"body": overview_args})
        self.require(not response.get("isError"), "two near-limit valid PNGs survived typed Overview write")
        read = rpc.read_tool(thread, "builder_overview_get", ids)["data"]
        self.require(read["revision"] == 1 and len(read["media"]) == 2, "Overview read returned the committed media references")
        too_large = base64.b64encode(bytes((2 << 20) + 1)).decode()
        oversized = overview_args | {"operation_id": "fixture-overview-oversize", "expected_revision": 1,
                                    "media": [media[0] | {"data": too_large}, media[1]]}
        failed = self.direct_tool(thread, "builder_overview_write", ids | {"body": oversized})
        self.require(failed["isError"] and json.loads(failed["content"][0]["text"])["error"] == "mcp_invalid_media",
                     "MCP rejects a PNG slot over 2 MiB before dispatch")
        current = rpc.read_tool(thread, "builder_overview_get", ids)["data"]
        self.require(current["revision"] == 1, "oversized Overview did not advance the Registry revision")

    def direct_tool(self, thread, name, args):
        # Use independently negotiated legacy JSON-RPC for the large body.
        payload = canonical_json({"jsonrpc": "2.0", "id": 1, "method": "tools/call",
                                  "params": {"name": name, "arguments": args}}).encode()
        conn = http.client.HTTPConnection(self.mcp.hostname, self.mcp.port, timeout=205)
        try:
            conn.request("POST", "/mcp", payload, {
                "Content-Type": "application/json", "Accept": "application/json, text/event-stream",
                "Authorization": "Bearer " + self.token, "MCP-Protocol-Version": "2025-11-25",
                "X-Delphi-Session-Type": "codex", "X-Delphi-Session-ID": thread["sessionId"],
            })
            reply = conn.getresponse()
            return json.loads(reply.read())["result"]
        finally:
            conn.close()


def overview_png(seed):
    def chunk(tag, contents):
        return struct.pack(">I", len(contents)) + tag + contents + struct.pack(">I", zlib.crc32(tag + contents))
    width, height = 1024, 680
    pixels = random.Random(seed)
    raw = b"".join(b"\0" + pixels.randbytes(width * 3) for _ in range(height))
    data = (b"\x89PNG\r\n\x1a\n" +
            chunk(b"IHDR", struct.pack(">IIBBBBB", width, height, 8, 2, 0, 0, 0)) +
            chunk(b"IDAT", zlib.compress(raw, level=1)) + chunk(b"IEND", b""))
    if not (2 << 20) - (16 << 10) < len(data) <= 2 << 20:
        raise ValueError("test PNG must be near the documented payload ceiling")
    return base64.b64encode(data).decode()


class Fixture(http.server.BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def send_value(self, status, data, media):
        self.send_response(status)
        self.send_header("Content-Type", media)
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        if data:
            self.wfile.write(data)

    def do_GET(self):
        self.send_value(405, b"", "text/plain")

    def do_POST(self):
        data = self.rfile.read(int(self.headers["Content-Length"]))
        request = json.loads(data)
        scenario = self.server.scenario
        if self.path == "/v1/responses" and not getattr(scenario.args, "authenticated_readonly", False):
            return self.respond_model(request)
        if self.path != "/mcp":
            return self.send_value(404, b"", "text/plain")
        if getattr(scenario.args, "authenticated_readonly", False) and request.get("method") == "tools/call":
            params = request.get("params", {})
            if params.get("name") != "builder_company_search" or params.get("arguments") != {"id": scenario.context["company_id"]}:
                return self.send_value(403, b"only the exact synthetic Company read is admitted", "text/plain")
        # This loopback recorder is fixture-only. It forwards the bytes to the
        # actual supplied MCP listener and records no Authorization value.
        event = {
            "method": request.get("method"), "tool": request.get("params", {}).get("name"),
            "token_matches": self.headers.get("Authorization") == f"Bearer {scenario.token}",
            "session_type": self.headers.get("X-Delphi-Session-Type"),
            "session_id": self.headers.get("X-Delphi-Session-ID"),
            "selection": self.headers.get("X-Delphi-Confirmed-Selection"),
            "protocol": self.headers.get("MCP-Protocol-Version"),
        }
        if event["method"] == "initialize":
            event["requested_protocol"] = request["params"]["protocolVersion"]
        headers = {key: value for key, value in self.headers.items()
                   if key.lower() not in {"host", "connection", "accept-encoding"}}
        conn = http.client.HTTPConnection(scenario.mcp.hostname, scenario.mcp.port, timeout=205)
        try:
            conn.request("POST", "/mcp", data, headers)
            response = conn.getresponse()
            result = response.read()
            if result and event["method"] in {"initialize", "tools/call"} and response.status == 200:
                event["response"] = json.loads(result).get("result")
            event["status"] = response.status
            scenario.evidence["http"].append(event)
            self.send_value(response.status, result, response.getheader("Content-Type", "text/plain"))
        finally:
            conn.close()

    def respond_model(self, request):
        scenario = self.server.scenario
        inputs = request.get("input", [])
        namespace = next((tool for tool in request.get("tools", []) if tool.get("name") == "mcp__" + SERVER), None)
        action = None
        interrupted = False
        for item in reversed(inputs):
            if item.get("role") != "user":
                continue
            for content in item.get("content", []):
                text = content.get("text")
                if text == INTERRUPT_MARKER:
                    scenario.interrupt_arrived.set()
                    scenario.interrupt_release.wait(30)
                    interrupted = True
                action = scenario.actions.get(text)
                if action:
                    break
            break
        emitted = []
        if action:
            name, arguments, call_id = action
            if not any(item.get("call_id") == call_id and item.get("type") == "function_call_output" for item in inputs):
                if not namespace or name not in {nested["name"] for nested in namespace["tools"]}:
                    return self.send_value(400, b'{"error":{"message":"Expected tool in Builder namespace"}}', "application/json")
                emitted.append({"type": "function_call", "id": "fc-" + call_id, "name": name,
                                "namespace": namespace["name"], "call_id": call_id, "status": "completed",
                                "arguments": canonical_json(arguments)})
        response = {"id": "resp-builder-" + str(next(scenario.model_counter)), "object": "response",
                    "created_at": int(time.time()), "model": MODEL, "status": "completed", "output": emitted,
                    "usage": {"input_tokens": 10, "output_tokens": 1, "total_tokens": 11}}
        events = [{"type": "response.created", "response": dict(response, status="in_progress", output=[])}]
        for item in emitted:
            events.append({"type": "response.output_item.added", "output_index": 0,
                           "item": dict(item, arguments="", status="in_progress")})
            events.append({"type": "response.output_item.done", "output_index": 0, "item": item})
        events.append({"type": "response.completed", "response": response})
        try:
            self.send_value(200, "".join(f"data: {canonical_json(event)}\n\n" for event in events).encode(),
                            "text/event-stream")
        except (BrokenPipeError, ConnectionResetError):
            if not interrupted:
                raise


class RPC:
    def __init__(self, scenario, home, cwd, phase):
        self.scenario, self.home, self.phase = scenario, home, phase
        self.policy = "cancel"
        self.pending = queue.Queue()
        self.events = []
        self.ids = itertools.count(1)
        env = {"PATH": os.environ["PATH"], TOKEN_VARIABLE: scenario.token,
               "NO_PROXY": "127.0.0.1,localhost", "RUST_LOG": "warn"}
        command = [str(scenario.args.codex_bin)]
        if home is None:
            env["HOME"] = os.environ["HOME"]
            command.extend(["-c", "mcp_servers={}"])
        else:
            env["CODEX_HOME"] = str(home)
            for key in ("CONFIG", "DATA", "CACHE", "STATE"):
                env[f"XDG_{key}_HOME"] = str(home / ("xdg-" + key.lower()))
        self.process = subprocess.Popen(
            command + ["app-server", "--stdio"], cwd=cwd, env=env,
            stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True, bufsize=1,
        )
        threading.Thread(target=self.receive, daemon=True).start()

    def __enter__(self):
        try:
            self.call("initialize", {"clientInfo": {"name": "builder-disposable", "version": "1"},
                                     "capabilities": {"experimentalApi": True}})
            self.send({"method": "initialized"})
            return self
        except BaseException:
            self.__exit__()
            raise

    def __exit__(self, *_):
        self.process.stdin.close()
        try:
            self.process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            self.process.terminate()
            try:
                self.process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                self.process.kill()
                self.process.wait(timeout=5)

    def receive(self):
        try:
            for line in self.process.stdout:
                self.pending.put(json.loads(line))
        finally:
            self.pending.put(None)

    def send(self, message):
        self.process.stdin.write(canonical_json(message) + "\n")
        self.process.stdin.flush()

    def next(self, deadline):
        message = self.pending.get(timeout=max(0, deadline - time.monotonic()))
        if message is None:
            raise RuntimeError(f"{self.phase}: App Server closed the fixture connection")
        if "method" in message:
            if "id" in message:
                method = message["method"]
                if method == "mcpServer/elicitation/request":
                    self.send({"id": message["id"], "result": {"action": self.policy,
                               "content": {"approve": True} if self.policy == "accept" else None}})
                    self.scenario.evidence["rpc"].append({"phase": self.phase, "callback": method, "decision": self.policy})
                else:
                    self.send({"id": message["id"], "error": {"code": -32601, "message": "unsupported fixture callback"}})
            else:
                self.events.append(message)
        return message

    def call(self, method, params):
        identifier = next(self.ids)
        self.send({"id": identifier, "method": method, "params": params})
        deadline = time.monotonic() + 45
        while True:
            response = self.next(deadline)
            if response.get("id") == identifier and "method" not in response:
                if "error" in response:
                    raise RuntimeError(f"{self.phase}/{method}: {response['error']}")
                return response["result"]

    def turn(self, thread_id, text):
        offset = len(self.events)
        turn_id = self.call("turn/start", {"threadId": thread_id,
                                          "input": [{"type": "text", "text": text, "text_elements": []}]})["turn"]["id"]
        deadline = time.monotonic() + 60
        while True:
            new_events = self.events[offset:]
            for event in new_events:
                if event["method"] == "turn/completed" and event["params"]["turn"]["id"] == turn_id:
                    current = event["params"]["turn"]
                    status = current["status"]
                    if status != "completed":
                        error = current.get("error", {})
                        self.scenario.evidence.setdefault("native_turn_errors", []).append({
                            "phase": self.phase, "status": status, "info": error.get("codexErrorInfo"),
                        })
                    self.scenario.require(status == "completed", f"{self.phase} turn status {status}")
                    items = [row["params"]["item"] for row in new_events if row["method"] == "item/completed" and
                             row["params"]["item"]["type"] == "mcpToolCall"]
                    return turn_id, items
            self.next(deadline)

    def interrupt_fixture_turn(self, thread_id):
        offset = len(self.events)
        started = self.call("turn/start", {"threadId": thread_id, "input": [
            {"type": "text", "text": INTERRUPT_MARKER, "text_elements": []},
        ]})["turn"]
        turn_id = started["id"]
        try:
            self.scenario.require(self.scenario.interrupt_arrived.wait(15), "owned turn reached its loopback model fixture")
            self.call("turn/interrupt", {"threadId": thread_id, "turnId": turn_id})
            deadline = time.monotonic() + 25
            while True:
                for event in self.events[offset:]:
                    if event["method"] == "turn/completed" and event["params"]["turn"]["id"] == turn_id:
                        status = event["params"]["turn"]["status"]
                        self.scenario.require(status == "interrupted", "App Server completed the requested turn interruption")
                        self.scenario.evidence["turns"].append({"thread_id": thread_id, "turn_id": turn_id, "status": status})
                        return
                self.next(deadline)
        finally:
            self.scenario.interrupt_release.set()

    def read_tool(self, thread, name, args, error=False):
        result = self.call("mcpServer/tool/call", {"threadId": thread["id"], "server": SERVER,
                                                 "tool": name, "arguments": args})
        self.scenario.require(bool(result.get("isError")) == error, f"{self.phase}/{name} returned its expected result")
        return json.loads(result["content"][0]["text"])


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--codex-bin", type=Path, required=True)
    parser.add_argument("--mcp-url", required=True)
    parser.add_argument("--fixture-context", type=Path, required=True)
    parser.add_argument("--evidence", type=Path, required=True)
    parser.add_argument("--authenticated-readonly", action="store_true",
                        help="attempt one native authenticated read-only turn and archive the owned fixture")
    args = parser.parse_args()
    scenario = Scenario(args)
    evidence = scenario.evidence
    try:
        code_version = subprocess.check_output([str(args.codex_bin), "--version"], text=True).strip()
        with args.codex_bin.open("rb") as binary:
            evidence.update(binary_version=code_version, binary_sha256=hashlib.file_digest(binary, "sha256").hexdigest())
        scenario.require(code_version == "codex-cli " + VERSION, "pinned App Server executable")
        with contextlib.ExitStack() as stack:
            fixture = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Fixture)
            fixture.scenario = scenario
            threading.Thread(target=fixture.serve_forever, daemon=True).start()
            stack.callback(fixture.server_close)
            stack.callback(fixture.shutdown)
            host = f"http://127.0.0.1:{fixture.server_port}"
            temp = Path(stack.enter_context(tempfile.TemporaryDirectory(prefix="builder-mcp-fixture-")))
            home, cwd = temp / "codex", temp / "cwd"
            home.mkdir(mode=0o700)
            cwd.mkdir(mode=0o700)
            (home / "config.toml").write_text(f'''
model = "{MODEL}"
model_provider = "builder-fixture"
sandbox_mode = "read-only"
[analytics]
enabled = false
[model_providers.builder-fixture]
name = "Builder scripted loopback fixture"
base_url = "{host}/v1"
wire_api = "responses"
requires_openai_auth = false
request_max_retries = 0
stream_max_retries = 0
''')
            if args.authenticated_readonly:
                scenario.authenticated_readonly(host, cwd)
            else:
                scenario.run(host, home, cwd)
        evidence["status"] = "passed"
    except BaseException as exc:
        evidence.update(status="failed", failure=str(exc))
        raise
    finally:
        args.evidence.write_text(json.dumps(evidence, indent=2) + "\n")


if __name__ == "__main__":
    main()
