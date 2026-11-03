import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Projects } from "./projects";

const digest = `sha256:${"a".repeat(64)}`;
const entry = { family: "module", id: "knowledge-viewer", revision: 2, content_digest: digest };
const manifest = { project_id: "builder", generation_id: "launch", active: true, state: "active", documents: [entry] };
const record = { id: entry.id, project_id: "builder", generation_id: "launch", revision: 2, content_digest: digest, updated_at: "2026-11-03T12:00:00Z", payload: { purpose: "Knowledge is owned by the typed API.", decisions: [{ id: "DEC-01", status: "planned", statement: "An explicitly planned decision", rationale: "Owner review needed" }], open_questions: [{ id: "q-01", question: "Which environment?", status: "open" }] } };
const catalog = { companies: [{ id: "belluga", name: "Belluga" }], projects: [{ id: "builder", name: "Builder", company_id: "belluga" }, { id: "boora", name: "Bóora", company_id: "belluga" }] };
type Reply = { ok: boolean; status: number; json: () => Promise<unknown> };
const reply = (value: unknown, status = 200): Reply => ({ ok: status === 200, status, json: async () => value });
function setup(knowledge: (url: URL) => Reply | Promise<Reply> = (url) => reply(url.searchParams.has("record_id") ? record : manifest)) {
  const fetcher = vi.fn(async (input: RequestInfo | URL) => {
    const url = new URL(String(input), location.origin);
    if (url.pathname === "/api/knowledge") return knowledge(url);
    if (url.pathname === "/api/local-preview" && !url.search) return reply(catalog);
    return reply({ diagnostic: "Foundation reader unavailable" }, 503);
  });
  vi.stubGlobal("fetch", fetcher);
  return fetcher;
}
function open(query = "") { history.replaceState({}, "", `/?company_id=belluga&project_id=builder&page=documents${query}`); return render(<Projects />); }
beforeEach(() => { history.replaceState({}, "", "/"); setup(); });
afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

describe("canonical Documents without Foundation", () => {
  it("lists typed identities and opens a document through a persistent URL", async () => {
    const fetcher = setup();
    open();
    fireEvent.click(await screen.findByRole("link", { name: /Read document/ }));
    expect(await screen.findByText(record.payload.purpose)).toBeInTheDocument();
    expect(screen.getByText("An explicitly planned decision")).toBeInTheDocument();
    expect(screen.getByText("Which environment?")).toBeInTheDocument();
    expect(screen.getAllByText(/Revision 2/).length).toBeGreaterThan(0);
    expect(screen.getAllByText(digest).length).toBeGreaterThan(0);
    expect(location.search).toContain("doc_family=module");
    expect(location.search).toContain("doc_id=knowledge-viewer");
    expect(fetcher.mock.calls.every(([url]) => !String(url).startsWith("/snapshot/"))).toBe(true);
  });

  it("restores a direct link on reload and reads the current API revision", async () => {
    let revision = 2;
    setup((url) => {
      const content_digest = `sha256:${(revision === 2 ? "a" : "b").repeat(64)}`;
      return reply(url.searchParams.has("record_id") ? { ...record, revision, content_digest, payload: { purpose: `Current revision ${revision}` } } : { ...manifest, documents: [{ ...entry, revision, content_digest }] });
    });
    const mounted = open("&doc_family=module&doc_id=knowledge-viewer");
    expect(await screen.findByText("Current revision 2")).toBeInTheDocument();
    mounted.unmount();
    revision = 3;
    render(<Projects />);
    expect(await screen.findByText("Current revision 3")).toBeInTheDocument();
    expect(screen.queryByText("Current revision 2")).not.toBeInTheDocument();
    expect(screen.getAllByText(`sha256:${"b".repeat(64)}`).length).toBeGreaterThan(0);
  });

  it.each([
    ["project_mandate", { purpose: "Mandate purpose", scope: ["Audience"], principles: ["Ownership"], exclusions: ["IAM"] }, "Mandate purpose"],
    ["domain_catalog", { entities: [{ id: "project", name: "Project", description: "A bounded Project" }], relations: [{ id: "owns", from: "company", to: "project", kind: "ownership" }], distinctions: [{ id: "scope", left: "Business domain", right: "Hostname", reason: "Different concepts" }] }, "Different concepts"],
    ["project_constitution", { promotion_mode: "mvp", capability_namespaces: ["go"], rules: [{ id: "access", status: "current", statement: "Project access is scoped" }], decisions: [] }, "Project access is scoped"],
    ["strategic_roadmap", { stages: [{ id: "launch", name: "Launch", status: "planned", outcomes: ["Canonical knowledge"] }] }, "Canonical knowledge"],
    ["module", { ...record.payload, schemas: [{ id: "schema", name: "Schema", kind: "object", fields: [{ name: "count", type: "number", required: false, minimum: 0 }] }], routes: [{ route: "/projects", host_scope: "api", environment_type: null, main_scope: null, subscope: null, guard: "Read guard" }] }, "Read guard"],
    ["endpoint_contract", { method: "GET", route: "/projects", host_scope: "api", module_id: "knowledge-viewer", request: [], response: [{ name: "name", type: "string", required: true }], security: { authentication: "Private service identity" }, pagination: { mode: "cursor", default_size: 20 }, idempotency: { required: false } }, "Private service identity"],
    ["policy", { rules: [{ id: "rule", status: "historical", statement: "Historical rule kept explicit" }], ownership: [{ owner_kind: "module", owner_id: "knowledge-viewer", decision_id: "DEC-01" }], endpoint_conventions: { base_path: "/api" } }, "Historical rule kept explicit"],
  ])("preserves the %s family as structured text", async (family, payload, expected) => {
    setup((url) => reply(url.searchParams.has("record_id") ? { ...record, payload } : { ...manifest, documents: [{ ...entry, family }] }));
    open(`&doc_family=${family}&doc_id=knowledge-viewer`);
    expect(await screen.findByText(String(expected))).toBeInTheDocument();
    if (family === "module") {
      expect(screen.getByText("false")).toBeInTheDocument();
      expect(screen.getByText("0")).toBeInTheDocument();
      expect(screen.getAllByText("Unspecified")).toHaveLength(3);
    }
  });

  it("renders source text without interpreting Markdown or HTML", async () => {
    const purpose = "# Literal **purpose** <img src=x onerror=alert(1)>";
    setup((url) => reply(url.searchParams.has("record_id") ? { ...record, payload: { purpose } } : manifest));
    open("&doc_family=module&doc_id=knowledge-viewer");
    expect(await screen.findByText(purpose)).toBeInTheDocument();
    expect(screen.queryByRole("img")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Overview" }));
    expect(location.search).not.toContain("doc_family");
    expect(location.search).not.toContain("doc_id");
  });

  it("shows pending and TEACH for an unactivated Project without requesting detail", async () => {
    const fetcher = setup(() => reply({ project_id: "builder", active: false, state: "pending", documents: [], teach: "TEACH: Activate this Project explicitly." }));
    open("&doc_family=module&doc_id=knowledge-viewer");
    expect(await screen.findByText("TEACH: Activate this Project explicitly.")).toBeInTheDocument();
    expect(screen.getByText(/Conhecimento canônico pending/)).toBeInTheDocument();
    expect(fetcher.mock.calls.filter(([url]) => String(url).includes("record_id="))).toHaveLength(0);
  });

  it("reports unknown IDs without replacing them with another document", async () => {
    setup((url) => reply(url.searchParams.has("record_id") ? { error: "canonical_not_found" } : manifest, url.searchParams.has("record_id") ? 404 : 200));
    open("&doc_family=module&doc_id=missing");
    expect(await screen.findByText(/Documento não encontrado/)).toBeInTheDocument();
    expect(screen.queryByText(record.payload.purpose)).not.toBeInTheDocument();
  });

  it("shows loading and ignores a slow response after switching Project", async () => {
    let resolveRecord!: (value: Reply) => void;
    const slow = new Promise<Reply>((resolve) => { resolveRecord = resolve; });
    setup((url) => url.searchParams.get("project_id") === "boora" ? reply({ project_id: "boora", active: false, state: "pending", documents: [], teach: "TEACH: Bóora not activated." }) : url.searchParams.has("record_id") ? slow : reply(manifest));
    open("&doc_family=module&doc_id=knowledge-viewer");
    expect(await screen.findByText("Loading document…")).toBeInTheDocument();
    await act(async () => { history.pushState({}, "", "/?company_id=belluga&project_id=boora&page=documents"); window.dispatchEvent(new PopStateEvent("popstate")); });
    expect(await screen.findByText("TEACH: Bóora not activated.")).toBeInTheDocument();
    await act(async () => resolveRecord(reply(record)));
    expect(screen.queryByText(record.payload.purpose)).not.toBeInTheDocument();
  });

  it("rejects mismatched identity, generation, revision and digest", async () => {
    for (const override of [{ project_id: "boora" }, { generation_id: "old" }, { revision: 1 }, { content_digest: `sha256:${"b".repeat(64)}` }]) {
      setup((url) => reply(url.searchParams.has("record_id") ? { ...record, ...override } : manifest));
      const mounted = open("&doc_family=module&doc_id=knowledge-viewer");
      expect(await screen.findByRole("alert")).toHaveTextContent("TEACH:");
      expect(screen.queryByText(record.payload.purpose)).not.toBeInTheDocument();
      mounted.unmount();
    }
  });

  it("recovers from an API failure with retry and preserves the selection", async () => {
    let failed = true;
    setup((url) => failed ? reply({}, 503) : reply(url.searchParams.has("record_id") ? record : manifest));
    open("&doc_family=module&doc_id=knowledge-viewer");
    expect(await screen.findByRole("alert")).toHaveTextContent("TEACH:");
    failed = false;
    fireEvent.click(screen.getByRole("button", { name: "Tentar novamente" }));
    expect(await screen.findByText(record.payload.purpose)).toBeInTheDocument();
    await waitFor(() => expect(location.search).toContain("doc_id=knowledge-viewer"));
  });
});
