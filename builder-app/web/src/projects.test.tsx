import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import userEvent from "@testing-library/user-event";
import { Projects } from "./projects";

const fingerprint = "a".repeat(64);
const designRevision = "e".repeat(40);
const originalScrollIntoView = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "scrollIntoView");
const companies = [{ id: "belluga", name: "Belluga" }, { id: "second", name: "Second Company" }];
const projects = [{ id: "builder", name: "Builder", company_id: "belluga", preparation_state: "ready", snapshot_fingerprint: fingerprint, artifacts: [] },
  { id: "boora", name: "Bóora!", company_id: "belluga", preparation_state: "ready", snapshot_fingerprint: fingerprint, artifacts: [] },
  { id: "other", name: "Other", company_id: "second", preparation_state: "ready", snapshot_fingerprint: fingerprint, artifacts: [] }];
const theme = { tokens: [
  ["color-canvas", "color", "#101820"], ["color-surface", "color", "#192838"], ["color-text-strong", "color", "#eeeeee"],
  ["color-text-secondary", "color", "#aaaaaa"], ["color-accent", "color", "#32bbdd"], ["color-on-accent", "color", "#111111"],
  ["color-border", "color", "#355577"], ["color-focus", "color", "#ccaa33"], ["color-success", "color", "#eeeeee"], ["font-family-sans", "string", "Arial"],
  ["text-body", "number", 16], ["text-display", "number", 40], ["line-body", "number", 1.5], ["radius-card", "number", 12], ["radius-small", "number", 6],
].map(([id, type, value]) => ({ id, type, value })) };
const todoMarkdown = "# Project Roadmap connection\n\n## Objective\nExact objective from Foundation.\n\n## Scope\n1. Exact authored step.\n\n## Definition of Done\n- [ ] Exact acceptance criterion.\n\n## Delivery Status Canon\n- **Next exact step:** Review the source.\n- **Primary module:** `modules/roadmap.md`.\n";
function designSystemRead(companyID: string, projectID: string, name = "Builder System", revision = designRevision) { return { selection: { state: "selected", repository_id: "belluga/builder", publication_branch: "main", sha: revision, inventory_digest: `sha256:${"b".repeat(64)}`, binding_revision: 3 }, evaluation: { schema_version: "1", project_id: projectID, company_id: companyID, target: "design-system", authority_scope: "local_structure_only", mode: "committed", revision, outcome: "go", inventory_digest: `sha256:${"b".repeat(64)}`, items: [{ schema_version: "2", id: "system", name, owner: { level: "project", id: projectID }, tokens: theme.tokens, presentation: { default_context: "main", contexts: [{ id: "main", roles: { canvas: "color-canvas", surface: "color-surface", text: "color-text-strong", muted_text: "color-text-secondary", border: "color-border", accent: "color-accent", accent_text: "color-text-strong", on_accent: "color-on-accent", focus: "color-focus", success_text: "color-success", font_family: "font-family-sans", body_size: "text-body", display_size: "text-display", card_radius: "radius-card", control_radius: "radius-small", line_height: "line-body" } }] }, components: [{ id: "button", name: "Button", token_ids: ["color-accent"], states: [], variants: [], examples: ["examples.html"] }], assets: ["catalog.css"] }], diagnostics: [], selected_level: "project", design_system_validation: "valid", presentation_validation: "ready" } }; }
function verifiedStage(stage: "Planning" | "Implementation-Approval-Pending" | "Ready-for-Implementation" | "In-Progress" | "Local-Implemented" | "Promotion-Lane" | "Completed", authored: string = stage, blocked = false) {
  return { authored_delivery_stage: authored, delivery_stage: stage, delivery_stage_state: "verified", blocked, qualifier_state: "verified", work_state: "unknown" } as const;
}
function unverifiedStage(authored = "unknown", teach = "TEACH: stage requires review.") {
  return { authored_delivery_stage: authored, delivery_stage_state: "unverified", delivery_stage_teach: teach, blocked: false, qualifier_state: "verified", work_state: "unknown" } as const;
}
function roadmap(projectID: string) { return { schema_version: "builder-roadmap-projection-v3", project_id: projectID, focus_release_id: "r-04", backlog: { todos: [{ id: "backlog:todos/active/backlog-item.md", title: "Unassigned active task", path: "todos/active/backlog-item.md", kind: "executable", record_state: "active", ...verifiedStage("Planning", "Pending"), objective_state: "absent", next_step_state: "absent" }, { id: "backlog:todos/active/backlog-item-two.md", title: "Second unassigned task", path: "todos/active/backlog-item-two.md", kind: "executable", record_state: "active", ...verifiedStage("Planning", "Pending"), objective_state: "absent", next_step_state: "absent" }], folders: [] }, unclassified: [], diagnostics: [], epics: [
  { id: "e-01", name: `${projectID} Local projects`, description: "Local Project view", releases: [
    { id: "r-01", name: "First local viewer", description: "Local knowledge", todos: [
      { id: "viewer-ledger", title: "Builder genesis ledger", path: "todos/completed/builder-genesis.md", kind: "planning", record_state: "completed", ...unverifiedStage() },
      { id: "architecture-ledger", title: "Architecture ledger", path: "todos/active/architecture.md", kind: "planning", record_state: "active", ...unverifiedStage() },
      { id: "completed-todo", title: "Published MVP task", path: "todos/completed/published.md", kind: "executable", record_state: "completed", ...verifiedStage("Completed", "Production-Ready") },
      { id: "second-done", title: "Second published task", path: "todos/completed/second.md", kind: "executable", record_state: "completed", ...verifiedStage("Completed", "Production-Ready") },
      { id: "third-done", title: "Third published task", path: "todos/completed/third.md", kind: "executable", record_state: "completed", ...verifiedStage("Completed", "Production-Ready") },
    ] },
    { id: "r-04", name: "Project UI and real Roadmap", description: "Project pages", todos: [
      { id: "roadmap-runtime", title: "Project Roadmap connection", path: "todos/active/roadmap_runtime_connection.md", kind: "executable", record_state: "active", ...verifiedStage("Local-Implemented", "Local-Implemented", true), work_state: "blocked", objective: "Exact objective from Foundation.", objective_state: "complete", next_step: "Exact authored step.", next_step_state: "complete" },
      { id: "promoted-todo", title: "Promotion pending task", path: "todos/promotion_lane/promoted.md", kind: "executable", record_state: "promotion_lane", ...verifiedStage("Promotion-Lane", "Lane-Promoted") },
    ] },
  ] },
  { id: "e-02", name: `${projectID} Other developers`, description: "Local projects for other developers", releases: [] },
  { id: "e-03", name: `${projectID} Remote APIs`, description: "Remote Delphi APIs", releases: [] },
  { id: "e-04", name: `${projectID} Internal service`, description: "Internal company service", releases: [
    { id: "r-05", name: "Company and Project Schema Study", description: "Initial planning record", todos: [{ id: "company-schema", title: "Company and Project Schema Study", path: "todos/active/company_project_schema_study.md", kind: "planning", record_state: "active", ...verifiedStage("Planning", "Pending") }] },
  ] },
  { id: "e-05", name: `${projectID} External service`, description: "External service", releases: [] },
] }; }
function detail(companyID: string, projectID: string, documents: { path: string; url: string }[] = []) { return { consumer_readiness: "ready", company_id: companyID, project_id: projectID, snapshot_fingerprint: fingerprint, project_sha: "c".repeat(40),
  artifacts: [{ id: "system", kind: "design_system", name: "Design System", state: "available", entry_point: "design/system/examples.html", files: ["design/system/design-system.json"] },
    { id: "landing", kind: "landing", name: "Landing", state: "available", entry_point: "design/landing/index.html" },
    { id: "roadmap", kind: "roadmap", name: "Roadmap", state: "available", entry_point: "roadmap/projection.json", files: ["roadmap/projection.json"] }], roadmap: roadmap(projectID),
  overview: { schema_version: "1", tagline: "Product direction", summary: `Overview for ${projectID}`, desktop_alt: "Desktop Project view", mobile_alt: "Mobile Project view", media: { desktop: `/snapshot/${companyID}/${projectID}/${fingerprint}/project_overview/desktop.png`, mobile: `/snapshot/${companyID}/${projectID}/${fingerprint}/project_overview/mobile.png`, proposal: `/snapshot/${companyID}/${projectID}/${fingerprint}/project_overview/proposal.png` }, sections: {
    problem: { heading: "The problem", body: "Problem evidence", points: [] },
    proposal: { heading: "The proposal", body: "Proposal evidence", points: [], image_alt: "Admitted Builder document view" },
    impact: { heading: "The impact", body: "Impact hypothesis", points: [] },
    audience: { heading: "For whom", body: "Audience evidence", points: [] },
  } }, portfolio_summary: `Summary for ${projectID}`, documents, snapshot_observed_at: "2026-10-15T12:00:00Z", snapshot_observations: {
    knowledge_status: { landing: { status: "current", reason_code: "match" }, roadmap: { status: "current", reason_code: "match" } },
    visual_origin: { state: "matching", knowledge_source_state: "matching", roadmap_source_state: "matching", design_system_state: "matching" },
  } }; }
function overviewRead(companyID: string, projectID: string) { const source = detail(companyID, projectID).overview; return { state: "available", company_id: companyID, project_id: projectID, revision: 1, content: { schema_version: "1", tagline: source.tagline, summary: source.summary, desktop_alt: source.desktop_alt, mobile_alt: source.mobile_alt, sections: source.sections }, media: Object.entries(source.media).map(([slot]) => ({ slot, digest: `sha256:${"a".repeat(64)}`, href: `/api/overview/media/${companyID}/${projectID}/1/${slot}` })) }; }
function mockFetch(documents: { path: string; url: string }[] = [], documentResponse?: { ok: boolean; body?: string; fingerprint?: string }, todoResponse = todoMarkdown) { vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
  const url = new URL(String(input), location.origin);
  if (url.pathname === "/api/overview") return { ok: true, json: async () => overviewRead(url.searchParams.get("company_id")!, url.searchParams.get("project_id")!) };
  if (url.pathname === "/api/knowledge") return { ok: true, json: async () => ({ project_id: url.searchParams.get("project_id"), active: false, state: "pending", documents: [] }) };
  if (url.pathname === "/api/design-system") return { ok: true, json: async () => designSystemRead(url.searchParams.get("company_id")!, url.searchParams.get("project_id")!) };
  if (url.pathname === "/api/roadmap") return { ok: true, json: async () => roadmap(url.searchParams.get("project_id") || "builder") };
  if (url.pathname === "/api/local-todo-source") return { ok: true, json: async () => ({ todo_id: url.searchParams.get("todo_id"), fingerprint, markdown: todoResponse }) };
  if (documents.some((item) => item.url === String(input))) return { ok: documentResponse?.ok ?? false, headers: new Headers({ "X-Builder-Snapshot-Fingerprint": documentResponse?.fingerprint || fingerprint }), text: async (): Promise<string> => documentResponse?.body || "" };
  if (url.pathname.startsWith("/snapshot/") && url.pathname.includes("/todos/")) return { ok: true, headers: new Headers({ "X-Builder-Snapshot-Fingerprint": fingerprint }), text: async (): Promise<string> => todoResponse };
  if (url.pathname.startsWith("/snapshot/")) return { ok: true, headers: new Headers({ "X-Builder-Snapshot-Fingerprint": fingerprint }), json: async () => theme };
  return { ok: true, json: async () => url.searchParams.has("project_id") ? detail(url.searchParams.get("company_id")!, url.searchParams.get("project_id")!, documents) : { companies, projects } };
})); }
function mockProjectDetail(saved: ReturnType<typeof detail>, todoResponse = todoMarkdown) { vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
  const url = new URL(String(input), location.origin);
  if (url.pathname === "/api/overview") return { ok: true, json: async () => overviewRead(url.searchParams.get("company_id")!, url.searchParams.get("project_id")!) };
  if (url.pathname === "/api/roadmap") return { ok: true, json: async () => saved.roadmap };
  if (url.pathname === "/api/local-todo-source") return { ok: true, json: async () => ({ todo_id: url.searchParams.get("todo_id"), fingerprint, markdown: todoResponse }) };
  if (url.pathname === "/api/local-preview") return { ok: true, json: async () => url.searchParams.has("project_id") ? saved : { companies, projects } };
  if (url.pathname === "/api/design-system") return { ok: true, json: async () => designSystemRead(url.searchParams.get("company_id")!, url.searchParams.get("project_id")!) };
  if (url.pathname.startsWith("/snapshot/") && url.pathname.includes("/todos/")) return { ok: true, headers: new Headers({ "X-Builder-Snapshot-Fingerprint": fingerprint }), text: async () => todoResponse };
  return { ok: true, headers: new Headers({ "X-Builder-Snapshot-Fingerprint": fingerprint }), json: async () => theme };
})); }
beforeEach(() => { history.replaceState({}, "", "/"); mockFetch(); });
afterEach(() => { cleanup(); vi.unstubAllGlobals(); if (originalScrollIntoView) Object.defineProperty(HTMLElement.prototype, "scrollIntoView", originalScrollIntoView); else delete (HTMLElement.prototype as Partial<HTMLElement>).scrollIntoView; });

describe("single root Project experience", () => {
  it("renders Mongo Overview for an unbound registered Project without reading snapshots", async () => {
    const unbound = { id: "accountzz", name: "Accountzz", company_id: "belluga", source_bound: false };
    const fetcher = vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), location.origin);
      if (url.pathname === "/api/roadmap") return { ok: true, json: async () => roadmap("accountzz") };
      if (url.pathname === "/api/overview") return { ok: true, json: async () => overviewRead("belluga", "accountzz") };
      if (url.pathname === "/api/local-preview" && url.searchParams.has("project_id")) throw new Error("unbound Project requested a Reader detail");
      if (url.pathname.startsWith("/snapshot/")) throw new Error("unbound Project requested a snapshot");
      return { ok: true, json: async () => ({ companies, projects: [...projects, unbound] }) };
    });
    vi.stubGlobal("fetch", fetcher);
    history.replaceState({}, "", "/?company_id=belluga&project_id=accountzz&page=overview");
    render(<Projects />);
    expect(await screen.findByRole("heading", { name: "The problem" })).toBeInTheDocument();
    expect(screen.getByRole("navigation", { name: "Project pages" })).toBeInTheDocument();
    expect(fetcher.mock.calls.some(([input]) => String(input).includes("/api/local-preview?company_id=") || String(input).startsWith("/snapshot/"))).toBe(false);
  });

  it("loads Mongo Roadmap when the Foundation Reader returns unavailable", async () => {
    const registryRoadmap = roadmap("builder");
    const fetcher = vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), location.origin);
      if (url.pathname === "/api/local-preview" && url.searchParams.has("project_id")) return { ok: false, status: 503, json: async () => ({ diagnostic: "Reader unavailable" }) };
      if (url.pathname === "/api/roadmap") return { ok: true, json: async () => registryRoadmap };
      return { ok: true, json: async () => ({ companies, projects }) };
    });
    vi.stubGlobal("fetch", fetcher);
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=roadmap");
    render(<Projects />);
    expect(await screen.findByRole("button", { name: /Project UI and real Roadmap/ }, { timeout: 5000 })).toBeInTheDocument();
    expect(screen.queryByText(/Reader unavailable/)).not.toBeInTheDocument();
    expect(fetcher.mock.calls.some(([input]) => String(input).startsWith("/api/roadmap?"))).toBe(true);
  });

  it("distinguishes administrative completion and non-delivery dispositions in a Release", async () => {
    const registryRoadmap = roadmap("builder");
    registryRoadmap.epics[0].releases[0].todos.push({ id: "accepted-complete", title: "Accepted historical delivery", kind: "executable", todo_revision: 0, record_state: "active", objective_state: "complete", next_step_state: "complete", authored_delivery_stage: "Completed", delivery_stage: "Completed", delivery_stage_state: "administrative", delivery_stage_basis: "administrative-acceptance", administrative_acceptance: { accepted_stage: "Completed", owner_reference: "APROVADO", reason: "Already delivered", permission_decision: "pre-iam-private-api-allow-all", accepted_at: "2026-11-07T00:00:00Z", content_digest: "sha256:old" }, blocked: false, qualifier_state: "verified", work_state: "unknown" } as never);
    registryRoadmap.epics[0].releases[0].todos.push({ id: "discarded", title: "Discarded historical obligation", kind: "non-delivery", todo_revision: 0, record_state: "active", objective_state: "complete", next_step_state: "complete", authored_delivery_stage: "Planning", delivery_stage_state: "unverified", disposition: { kind: "Discarded", reason: "No longer required" }, disposition_state: "valid", blocked: false, qualifier_state: "verified", work_state: "unknown" } as never);
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), location.origin);
      if (url.pathname === "/api/roadmap") return { ok: true, json: async () => registryRoadmap };
      return { ok: true, json: async () => ({ companies, projects }) };
    }));
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=release&epic_id=e-01&release_id=r-01");
    render(<Projects />);
    expect(await screen.findByText("Concluída por aceite administrativo")).toBeInTheDocument();
    expect(screen.getByText("Concluído · aceite administrativo")).toBeInTheDocument();
    expect(screen.getByText("Histórico não entregue")).toBeInTheDocument();
    expect(screen.getByText("Descartado")).toBeInTheDocument();
  });

  it("loads the Mongo Overview independently when the Foundation Reader fails", async () => {
    const fetcher = vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), location.origin);
      if (url.pathname === "/api/local-preview" && url.searchParams.has("project_id")) return { ok: false, status: 503, json: async () => ({ diagnostic: "Reader unavailable" }) };
      if (url.pathname === "/api/overview") return { ok: true, json: async () => overviewRead("belluga", "builder") };
      return { ok: true, json: async () => ({ companies, projects }) };
    });
    vi.stubGlobal("fetch", fetcher);
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=overview");
    render(<Projects />);
    expect(await screen.findByRole("heading", { name: "The problem" })).toBeInTheDocument();
    expect(fetcher.mock.calls.some(([input]) => String(input).startsWith("/api/overview?"))).toBe(true);
    expect(screen.queryByText(/Reader unavailable/)).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: /Ver landing independente/ })).not.toBeInTheDocument();
  });

  it("shows Registry TEACH without falling back to an available Foundation Roadmap", async () => {
    const fetcher = vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), location.origin);
      if (url.pathname === "/api/roadmap") return { ok: false, status: 503, json: async () => ({ error: "roadmap_api_unavailable" }) };
      if (url.pathname === "/api/local-preview" && url.searchParams.has("project_id")) return { ok: true, json: async () => detail("belluga", "builder") };
      return { ok: true, json: async () => ({ companies, projects }) };
    });
    vi.stubGlobal("fetch", fetcher);
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=roadmap");
    render(<Projects />);
    expect(await screen.findByText("TEACH: Mongo Roadmap indisponível. Verifique a Registry API e recarregue; não é usado snapshot Foundation como fallback.")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Project UI and real Roadmap/ })).not.toBeInTheDocument();
  });

  it("rejects an invalid successful Roadmap response without rendering Foundation data", async () => {
    const fetcher = vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), location.origin);
      if (url.pathname === "/api/roadmap") return { ok: true, status: 200, json: async () => ({ schema_version: "builder-roadmap-projection-v3", project_id: "builder", epics: null, backlog: null }) };
      if (url.pathname === "/api/local-preview" && url.searchParams.has("project_id")) return { ok: true, json: async () => detail("belluga", "builder") };
      return { ok: true, json: async () => ({ companies, projects }) };
    });
    vi.stubGlobal("fetch", fetcher);
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=roadmap");
    render(<Projects />);
    expect(await screen.findByText(/TEACH: Mongo Roadmap indisponível/)).toHaveAttribute("role", "alert");
    expect(screen.queryByRole("button", { name: /Project UI and real Roadmap/ })).not.toBeInTheDocument();
    expect(fetcher.mock.calls.some(([input]) => String(input).includes("local-todo-source"))).toBe(false);
  });

  it.each(["release", "backlog"])("rejects an invalid nested %s TODO without a crash or Foundation fallback", async (placement) => {
    const invalid = roadmap("builder");
    if (placement === "release") invalid.epics[0].releases[0].todos = [null] as never;
    else invalid.backlog.todos = [null] as never;
    const fetcher = vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), location.origin);
      if (url.pathname === "/api/roadmap") return { ok: true, json: async () => invalid };
      if (url.pathname === "/api/local-preview" && url.searchParams.has("project_id")) return { ok: true, json: async () => detail("belluga", "builder") };
      return { ok: true, json: async () => ({ companies, projects }) };
    });
    vi.stubGlobal("fetch", fetcher);
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=roadmap");
    render(<Projects />);
    expect(await screen.findByText(/TEACH: Mongo Roadmap indisponível/)).toHaveAttribute("role", "alert");
    expect(screen.queryByRole("button", { name: /Project UI and real Roadmap/ })).not.toBeInTheDocument();
    expect(fetcher.mock.calls.some(([input]) => String(input).includes("local-todo-source"))).toBe(false);
  });

  it("reads a Planning Backlog TODO from its exact Mongo revision without approval or Reader", async () => {
    const registryRoadmap = roadmap("builder");
    registryRoadmap.backlog.todos.push({ id: "schema-study", title: "Schema study", path: "", kind: "executable", todo_revision: 0, record_state: "active", objective: "Inspect schema", objective_state: "complete", next_step: "Review fields", next_step_state: "complete", ...verifiedStage("Planning") } as typeof registryRoadmap.backlog.todos[number]);
    const exact = { company_id: "belluga", project_id: "builder", todo_revision: 0, content_revision: 0, content_digest: "a".repeat(64), todo: { id: "schema-study", project_id: "builder", revision: 0, title: "Schema study", objective: "Inspect schema", next_exact_step: "Review fields", stage: "Planning", content: { schema_version: 2, context: "Typed contract context", scope: { included: ["typed fields"], excluded: [] }, criteria: [{ id: "criterion-one", text: "Typed criterion body" }], validation_scenarios: [], implementation_plan: [], decisions: [], risks: [], references: [], architecture: { decision: "Keep schema validation at the registry boundary.", rationale: "One canonical contract owner.", protection: "Reject malformed versions before persistence." } }, sections: [{ key: "objective", title: "Legacy", body_markdown: "Do not use the legacy section when typed content is available.", position: 0 }] } };
    const fetcher = vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), location.origin);
      if (url.pathname === "/api/local-preview" && url.searchParams.has("project_id")) return { ok: false, status: 503, json: async () => ({ diagnostic: "Reader unavailable" }) };
      if (url.pathname === "/api/roadmap") return { ok: true, json: async () => url.searchParams.has("todo_id") ? exact : registryRoadmap };
      return { ok: true, json: async () => ({ companies, projects }) };
    });
    vi.stubGlobal("fetch", fetcher);
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=todo&todo_id=schema-study");
    render(<Projects />);
    expect(await screen.findByText("Typed contract context")).toBeInTheDocument();
    expect(await screen.findByText("Keep schema validation at the registry boundary.")).toBeInTheDocument();
    expect(screen.getByText("Typed criterion body")).toBeInTheDocument();
    expect(screen.getByText("Do not use the legacy section when typed content is available.")).not.toBeVisible();
    fireEvent.click(screen.getByText("Show preserved v1 sections"));
    expect(screen.getByText("Do not use the legacy section when typed content is available.")).toBeInTheDocument();
    const exactCall = fetcher.mock.calls.map(([input]) => new URL(String(input), location.origin)).find((url) => url.searchParams.has("todo_id"));
    expect(exactCall?.searchParams.get("todo_revision")).toBe("0");
    expect(fetcher.mock.calls.some(([input]) => String(input).includes("local-todo-source"))).toBe(false);
  });

  it("opens a Mongo Backlog TODO whose Roadmap summary has no Foundation path", async () => {
    vi.stubGlobal("matchMedia", vi.fn(() => ({ matches: false, media: "(max-width: 1200px)", onchange: null, addListener: vi.fn(), removeListener: vi.fn(), addEventListener: vi.fn(), removeEventListener: vi.fn(), dispatchEvent: vi.fn() })));
    const registryRoadmap = roadmap("builder");
    registryRoadmap.backlog.todos.push({ id: "schema-study", title: "Schema study", kind: "executable", todo_revision: 0, record_state: "active", objective: "Inspect schema", objective_state: "complete", next_step: "Review fields", next_step_state: "complete", ...verifiedStage("Planning") } as unknown as typeof registryRoadmap.backlog.todos[number]);
    const exact = { company_id: "belluga", project_id: "builder", todo_revision: 0, content_revision: 0, content_digest: `sha256:${"a".repeat(64)}`, todo: { id: "schema-study", project_id: "builder", revision: 0, title: "Schema study", objective: "Typed schema objective", next_exact_step: "Review fields", stage: "Planning", sections: [] } };
    const fetcher = vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), location.origin);
      if (url.pathname === "/api/roadmap") return { ok: true, json: async () => url.searchParams.has("todo_id") ? exact : registryRoadmap };
      if (url.pathname === "/api/local-preview" && url.searchParams.has("project_id")) return { ok: false, status: 503, json: async () => ({ diagnostic: "Reader unavailable" }) };
      return { ok: true, json: async () => ({ companies, projects }) };
    });
    vi.stubGlobal("fetch", fetcher);
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=backlog&todo_id=schema-study");
    render(<Projects />);
    await userEvent.setup().click(await screen.findByRole("button", { name: /Abrir TODO completo/ }));
    expect(await screen.findByText("Typed schema objective")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Schema study" })).toBeInTheDocument();
    expect(fetcher.mock.calls.some(([input]) => String(input).includes("local-todo-source"))).toBe(false);
  });

  it("does not start another detail request on tab return or elapsed time", async () => {
    const interval = vi.spyOn(window, "setInterval");
    let resolveDetail!: (value: unknown) => void;
    const pendingDetail = new Promise<unknown>((resolve) => { resolveDetail = resolve; });
    const detailCalls = vi.fn();
    const fetcher = vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), location.origin);
      if (url.pathname === "/api/overview") return { ok: true, json: async () => overviewRead("belluga", url.searchParams.get("project_id")!) };
      if (url.pathname === "/api/roadmap") return { ok: true, json: async () => roadmap(url.searchParams.get("project_id") || "builder") };
      if (url.pathname.startsWith("/snapshot/")) return { ok: true, headers: new Headers({ "X-Builder-Snapshot-Fingerprint": fingerprint }), json: async () => theme };
      if (url.pathname === "/api/local-preview" && url.searchParams.has("project_id")) { detailCalls(); return pendingDetail as Promise<never>; }
      return { ok: true, json: async () => ({ companies, projects }) };
    });
    vi.stubGlobal("fetch", fetcher);
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=overview");
    render(<Projects />);
    await waitFor(() => expect(detailCalls).toHaveBeenCalledTimes(1));
    expect(await screen.findByRole("heading", { name: "The problem" })).toBeInTheDocument();
    for (let index = 0; index < 10; index++) fireEvent(document, new Event("visibilitychange"));
    expect(detailCalls).toHaveBeenCalledTimes(1);
    resolveDetail({ ok: true, json: async () => detail("belluga", "builder") });
    expect(await screen.findByRole("heading", { name: "The problem" })).toBeInTheDocument();
    fireEvent(document, new Event("visibilitychange"));
    expect(detailCalls).toHaveBeenCalledTimes(1);
    expect(interval.mock.calls.some(([, delay]) => delay === 30_000)).toBe(false);
    interval.mockRestore();
  });
  it("opens Prototype independently of the Project detail reader", async () => {
    const fetcher = vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), location.origin);
      if (url.pathname === "/api/prototypes") return new Response(JSON.stringify({ revision: "a".repeat(40), inventory_digest: `sha256:${"b".repeat(64)}`, items: [] }), { status: 200 });
      if (url.pathname === "/api/local-preview" && url.searchParams.has("project_id")) throw new Error("Prototype requested Project detail");
      return { ok: true, json: async () => ({ companies, projects }) };
    });
    vi.stubGlobal("fetch", fetcher);
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=prototypes");
    render(<Projects />);
    await waitFor(() => expect(fetcher.mock.calls.some(([input]) => String(input).startsWith("/api/prototypes?company_id=belluga&project_id=builder"))).toBe(true));
    expect(fetcher.mock.calls.some(([input]) => String(input).startsWith("/api/local-preview?company_id=belluga&project_id=builder"))).toBe(false);
  });
  it("keeps the loaded Project detail through page, brand and invalid-link recovery navigation", async () => {
    const fetcher = vi.mocked(fetch);
    const detailCalls = () => fetcher.mock.calls.filter(([input]) => String(input).startsWith("/api/local-preview?company_id=")).length;
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=roadmap");
    render(<Projects />);
    expect(await screen.findByRole("button", { name: /First local viewer/ })).toBeInTheDocument();
    expect(detailCalls()).toBe(1);
    fireEvent.click(screen.getByRole("button", { name: "Documents / decisions" }));
    expect(await screen.findByRole("heading", { name: "Documentação" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("link", { name: "Project Builder" }));
    expect(await screen.findByRole("heading", { name: "The problem" })).toBeInTheDocument();
    expect(detailCalls()).toBe(1);
    act(() => { history.pushState({}, "", "/?company_id=belluga&project_id=builder&page=missing"); window.dispatchEvent(new PopStateEvent("popstate")); });
    fireEvent.click(screen.getByRole("link", { name: "Back to Overview" }));
    expect(await screen.findByRole("heading", { name: "The problem" })).toBeInTheDocument();
    expect(detailCalls()).toBe(1);
    act(() => { history.pushState({}, "", "/?company_id=belluga&project_id=builder&artifact=prototype%3Ademo"); window.dispatchEvent(new PopStateEvent("popstate")); });
    fireEvent.click(screen.getByRole("link", { name: "Open Prototype" }));
    expect(await screen.findByRole("heading", { name: "Prototypes" })).toBeInTheDocument();
    expect(detailCalls()).toBe(1);
  });
  it("does not treat Foundation snapshot observations as current Overview state", async () => {
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=overview");
    render(<Projects />);
    expect(await screen.findByRole("heading", { name: "The problem" })).toBeInTheDocument();
    expect(screen.queryByRole("region", { name: "Snapshot status" })).not.toBeInTheDocument();
  });
  it("does not use a Foundation Markdown TODO as current Roadmap content", async () => {
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=release&epic_id=e-01&release_id=r-04");
    render(<Projects />);
    const invokingTODO = await screen.findByRole("button", { name: /Project Roadmap connection/ });
    fireEvent.click(invokingTODO);
    expect(location.search).toContain("todo_id=roadmap-runtime");
    const dialog = await screen.findByRole("dialog", { name: "Prévia do TODO" });
    const closePreview = within(dialog).getByRole("button", { name: "Fechar prévia" });
    const openFull = within(dialog).getByRole("button", { name: /Abrir TODO completo/ });
    expect(closePreview).toHaveFocus();
    invokingTODO.focus();
    expect(closePreview).toHaveFocus();
    fireEvent.click(openFull);
    expect(await screen.findByRole("alert")).toHaveTextContent("TEACH: Este item não possui uma revisão exata no Mongo Roadmap");
    expect(screen.queryByText("Exact objective from Foundation.")).not.toBeInTheDocument();
    expect(vi.mocked(fetch).mock.calls.some(([input]) => String(input).includes("local-todo-source"))).toBe(false);
  });
  it("uses the Roadmap revision for a migrated exact read and surfaces stale TEACH", async () => {
    const saved = detail("belluga", "builder");
    const selected = { id: "builder-company-tenant-mongo-registry", title: "Mongo registry", path: "todos/active/registry.md", kind: "executable" as const, record_state: "active", todo_revision: 0, objective: "Registry objective", objective_state: "complete" as const, next_step: "Next", next_step_state: "complete" as const, ...verifiedStage("In-Progress") };
    saved.roadmap!.epics[0].releases[0].todos.push(selected);
    const calls: URL[] = [];
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), location.origin);
      if (url.pathname === "/api/design-system") return { ok: true, json: async () => designSystemRead("belluga", "builder") };
      if (url.pathname === "/api/roadmap" && url.searchParams.has("todo_id")) {
        calls.push(url);
        return { ok: false, json: async () => ({ teach: "TEACH: TODO revision is now 1; reload the Project Roadmap." }) };
      }
      if (url.pathname === "/api/roadmap") return { ok: true, json: async () => saved.roadmap };
      if (url.pathname === "/api/local-preview") return { ok: true, json: async () => url.searchParams.has("project_id") ? saved : { companies, projects } };
      return { ok: true, headers: new Headers({ "X-Builder-Snapshot-Fingerprint": fingerprint }), json: async () => theme };
    }));
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=todo&epic_id=e-01&release_id=r-01&todo_id=builder-company-tenant-mongo-registry");
    render(<Projects />);
    expect(await screen.findByRole("alert")).toHaveTextContent("TEACH: TODO revision is now 1");
    expect(calls).toHaveLength(1);
    expect(calls[0].searchParams.get("todo_revision")).toBe("0");
  });
  it("rejects a TODO ID outside its Release before requesting source", async () => {
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=todo&epic_id=e-01&release_id=r-01&todo_id=backlog%3Atodos%2Factive%2Fevil.md&path=todos%2Factive%2Fbacklog-item.md");
    render(<Projects />);
    expect(await screen.findByRole("alert")).toHaveTextContent("TODO indisponível");
    expect(screen.queryByText("Exact objective from Foundation.")).not.toBeInTheDocument();
    expect(vi.mocked(fetch).mock.calls.some(([input]) => String(input).includes("backlog-item.md"))).toBe(false);
  });
  it("requires exact Mongo revision before reading a direct TODO link", async () => {
    mockFetch([], undefined, "# Project Roadmap connection\n\n## Objective\nOnly authored objective.\n");
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=todo&todo_id=roadmap-runtime");
    render(<Projects />);
    expect(await screen.findByRole("alert")).toHaveTextContent("TEACH: Este item não possui uma revisão exata no Mongo Roadmap");
    expect(screen.queryByText("Only authored objective.")).not.toBeInTheDocument();
    expect(vi.mocked(fetch).mock.calls.some(([input]) => String(input).includes("local-todo-source"))).toBe(false);
  });
  it("does not read a Foundation-only Primary module for current Roadmap TODO detail", async () => {
    mockFetch([], undefined, "# Project Roadmap connection\n\n## Delivery Status Canon\n- **Primary module:** `modules/roadmap.md`.\n");
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=todo&todo_id=roadmap-runtime");
    render(<Projects />);
    expect(await screen.findByRole("alert")).toHaveTextContent("TEACH: Este item não possui uma revisão exata no Mongo Roadmap");
    expect(screen.queryByRole("heading", { name: "Primary module" })).not.toBeInTheDocument();
  });
  it("ignores an exact Mongo TODO read that resolves after switching Projects", async () => {
    let resolveTODO!: (response: unknown) => void;
    const mongoRoadmap = roadmap("builder");
    mongoRoadmap.epics[0].releases[0].todos.push({ id: "mongo-late", title: "Delayed Mongo TODO", path: "", kind: "executable", todo_revision: 0, record_state: "active", objective: "Delayed objective", objective_state: "complete", next_step: "Review", next_step_state: "complete", ...verifiedStage("Planning") } as typeof mongoRoadmap.backlog.todos[number]);
    const fetcher = vi.fn((input: RequestInfo | URL) => {
      const url = new URL(String(input), location.origin);
      if (url.pathname === "/api/overview") return Promise.resolve({ ok: true, json: async () => overviewRead("belluga", url.searchParams.get("project_id")!) });
      if (url.pathname === "/api/roadmap" && url.searchParams.has("todo_id")) return new Promise((resolve) => { resolveTODO = resolve; });
      if (url.pathname === "/api/roadmap") return Promise.resolve({ ok: true, json: async () => mongoRoadmap });
      if (url.pathname === "/api/local-preview") return Promise.resolve({ ok: true, json: async () => url.searchParams.has("project_id") ? detail("belluga", url.searchParams.get("project_id")!) : { companies, projects } });
      return Promise.resolve({ ok: true, json: async () => theme });
    });
    vi.stubGlobal("fetch", fetcher);
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=todo&todo_id=mongo-late");
    render(<Projects />);
    await waitFor(() => expect(resolveTODO).toBeTypeOf("function"));
    act(() => { history.pushState({}, "", "/?company_id=belluga&project_id=boora&page=overview"); window.dispatchEvent(new PopStateEvent("popstate")); });
    expect(await screen.findByText("Overview for boora")).toBeInTheDocument();
    await act(async () => { resolveTODO({ ok: true, json: async () => ({ company_id: "belluga", project_id: "builder", todo_revision: 0, todo: { id: "mongo-late", revision: 0, title: "Delayed Mongo TODO", objective: "Delayed objective", next_exact_step: "Review", stage: "Planning", sections: [] } }) }); });
    expect(screen.queryByText("Delayed objective")).not.toBeInTheDocument();
    expect(fetcher.mock.calls.some(([input]) => String(input).includes("local-todo-source"))).toBe(false);
  });
  it("opens a path-bound Backlog preview by keyboard but requires Mongo revision for full detail", async () => {
    vi.stubGlobal("matchMedia", vi.fn(() => ({ matches: true, media: "(max-width: 1200px)", onchange: null, addListener: vi.fn(), removeListener: vi.fn(), addEventListener: vi.fn(), removeEventListener: vi.fn(), dispatchEvent: vi.fn() })));
    HTMLElement.prototype.scrollIntoView = vi.fn();
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=backlog");
    const fetchSpy = vi.fn((input: RequestInfo | URL) => {
      const url = new URL(String(input), location.origin);
      if (url.pathname === "/api/design-system") return Promise.resolve({ ok: true, json: async () => designSystemRead("belluga", "builder") });
      if (url.pathname === "/api/roadmap") return Promise.resolve({ ok: true, json: async () => detail("belluga", url.searchParams.get("project_id") || "builder").roadmap });
      if (url.pathname === "/api/local-preview" && !url.searchParams.has("project_id")) return Promise.resolve({ ok: true, json: async () => ({ companies, projects }) });
      if (url.pathname === "/api/local-preview") return Promise.resolve({ ok: true, json: async () => detail("belluga", "builder") });
      if (url.pathname.endsWith("/design/system/design-system.json")) return Promise.resolve({ ok: true, headers: new Headers({ "X-Builder-Snapshot-Fingerprint": fingerprint }), json: async () => theme });
      return Promise.resolve({ ok: true, json: async () => theme });
    });
    vi.stubGlobal("fetch", fetchSpy);
    const user = userEvent.setup();
    render(<Projects />);
    const first = await screen.findByRole("button", { name: /Unassigned active task/ });
    first.focus();
    await user.keyboard("{Enter}");
    fireEvent.click(screen.getByRole("button", { name: /Second unassigned task/ }));
    expect(screen.getByLabelText("Prévia do TODO")).toHaveFocus();
    expect(fetchSpy.mock.calls.filter(([input]) => new URL(String(input), location.origin).searchParams.has("todo_id"))).toHaveLength(0);
    expect(location.search).toContain("page=backlog");
    expect(location.search).toContain("backlog%3Atodos%2Factive%2Fbacklog-item-two.md");
    await user.click(screen.getByRole("button", { name: /Abrir TODO completo/ }));
    expect(await screen.findByRole("alert")).toHaveTextContent("TEACH: Este item não possui uma revisão exata no Mongo Roadmap");
    expect(screen.queryByText("Current backlog objective")).not.toBeInTheDocument();
    expect(location.search).toContain("backlog%3Atodos%2Factive%2Fbacklog-item-two.md");
    expect(location.search).toContain("page=todo");
    expect(fetchSpy.mock.calls.some(([input]) => String(input).includes("local-todo-source"))).toBe(false);
    await user.click(within(screen.getByRole("navigation", { name: "Project pages" })).getByRole("button", { name: "Backlog" }));
    expect(location.search).toContain("page=backlog");
    expect(screen.getByRole("region", { name: "Project Backlog" })).toBeInTheDocument();
  });
  it("does not render a Foundation-only detached TODO as current Roadmap content", async () => {
    const saved = detail("belluga", "builder");
    saved.roadmap.backlog.todos[0].id = "t-preserved-history";
    mockProjectDetail(saved);
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=todo&todo_id=t-preserved-history");
    render(<Projects />);
    expect(await screen.findByRole("alert")).toHaveTextContent("TEACH: Este item não possui uma revisão exata no Mongo Roadmap");
    expect(screen.queryByRole("heading", { name: "Unassigned active task" })).not.toBeInTheDocument();
    expect(vi.mocked(fetch).mock.calls.some(([input]) => String(input).includes("local-todo-source"))).toBe(false);
  });
  it("opens nested Backlog folders and keeps authored README guidance in the preview without Foundation TODO reads", async () => {
    vi.stubGlobal("matchMedia", vi.fn(() => ({ matches: true, media: "(max-width: 1200px)", onchange: null, addListener: vi.fn(), removeListener: vi.fn(), addEventListener: vi.fn(), removeEventListener: vi.fn(), dispatchEvent: vi.fn() })));
    const saved = detail("belluga", "builder");
    const nestedPath = "todos/active/platform/runtime/cache.md";
    saved.roadmap.backlog.todos = [];
    saved.roadmap.backlog.folders = [{
      name: "Platform", path: "todos/active/platform", description: "README: platform-wide authored guidance.", todos: [], folders: [{
        name: "Runtime", path: "todos/active/platform/runtime", description: "README: runtime-specific authored guidance.",
        todos: [{ id: `backlog:${nestedPath}`, title: "Cache runtime task", path: nestedPath, kind: "executable" as const, record_state: "active", ...verifiedStage("Planning", "Pending") }], folders: [],
      }],
    }] as unknown as typeof saved.roadmap.backlog.folders;
    const roadmapArtifact = saved.artifacts.find((artifact) => artifact.kind === "roadmap")!;
    roadmapArtifact.files = [...(roadmapArtifact.files || []), "todos/active/platform/README.md", "todos/active/platform/runtime/README.md", nestedPath];
    mockProjectDetail(saved, "# Cache runtime task\n\n## Objective\nAuthored nested objective.\n");
    const user = userEvent.setup();
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=backlog");
    render(<Projects />);
    await screen.findByRole("heading", { name: "Backlog", level: 1 });

    const rootDetails = screen.getByText("Platform").closest("details")!;
    const rootSummary = within(rootDetails).getByText("Platform").closest("summary")!;
    rootSummary.focus();
    expect(rootSummary).toHaveFocus();
    if (!rootDetails.open) await user.keyboard("{Enter}");
    // jsdom does not implement the browser's native summary Enter activation.
    if (!rootDetails.open) fireEvent.click(rootSummary);
    expect(rootDetails).toHaveAttribute("open");
    expect(within(rootDetails).getByText("README: platform-wide authored guidance.")).toBeInTheDocument();
    const nestedDetails = within(rootDetails).getByText("Runtime").closest("details")!;
    const nestedSummary = within(nestedDetails).getByText("Runtime").closest("summary")!;
    nestedSummary.focus();
    expect(nestedSummary).toHaveFocus();
    await user.keyboard("{Enter}");
    if (!nestedDetails.open) fireEvent.click(nestedSummary);
    expect(nestedDetails).toHaveAttribute("open");
    expect(within(nestedDetails).getByText("README: runtime-specific authored guidance.")).toBeInTheDocument();
    await user.click(within(nestedDetails).getByRole("button", { name: /Cache runtime task/ }));
    expect(screen.queryByText("Authored nested objective.")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /Abrir TODO completo/ }));
    expect(await screen.findByRole("alert")).toHaveTextContent("TEACH: Este item não possui uma revisão exata no Mongo Roadmap");
    expect(screen.queryByText("Authored nested objective.")).not.toBeInTheDocument();
    expect(vi.mocked(fetch).mock.calls.some(([input]) => String(input).includes("local-todo-source"))).toBe(false);
    expect(location.search).toContain(encodeURIComponent(`backlog:${nestedPath}`));
  });
  it("keeps an available empty plan separate from executable Backlog work", async () => {
    const saved = detail("belluga", "builder");
    saved.roadmap.epics = [];
    saved.roadmap.focus_release_id = "";
    mockProjectDetail(saved);
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=roadmap");
    render(<Projects />);

    expect(await screen.findByRole("heading", { name: "Roadmap" })).toBeInTheDocument();
    expect(screen.getByText("Nenhum Epic ou Release planejado.")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Sem plano de releases" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Unassigned active task/ })).not.toBeInTheDocument();
    expect(screen.getByRole("group", { name: "Releases for selected Epic" })).not.toHaveTextContent("Unassigned active task");
    fireEvent.click(screen.getByRole("button", { name: "Backlog" }));
    expect(await screen.findByRole("button", { name: /Unassigned active task/ })).toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });
  it("lists every admitted unclassified path for an invalid plan without assigning membership", async () => {
    const saved = detail("belluga", "builder");
    const admittedPaths = ["todos/active/uncategorized-one.md", "todos/active/uncategorized-two.md", "todos/active/nested/uncategorized-three.md"];
    saved.roadmap.epics = [];
    saved.roadmap.focus_release_id = "";
    saved.roadmap.backlog.todos = [];
    saved.roadmap.backlog.folders = [];
    saved.roadmap.unclassified = admittedPaths.map((path, index) => ({ path, reason: `Invalid plan assignment ${index + 1}.` })) as unknown as typeof saved.roadmap.unclassified;
    saved.roadmap.diagnostics = [{ code: "invalid_plan", path: "todos/active/roadmap.md", message: "Planning metadata is inconsistent." }] as unknown as typeof saved.roadmap.diagnostics;
    const roadmapArtifact = saved.artifacts.find((artifact) => artifact.kind === "roadmap")!;
    roadmapArtifact.state = "available";
    roadmapArtifact.files = [...(roadmapArtifact.files || []), ...admittedPaths, "todos/active/roadmap.md"];
    mockProjectDetail(saved);
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=roadmap");
    render(<Projects />);

    expect(await screen.findByRole("heading", { name: "Roadmap" })).toBeInTheDocument();
    const unclassified = screen.getByRole("region", { name: "TODOs sem classificação" });
    admittedPaths.forEach((path, index) => {
      expect(within(unclassified).getByText(path)).toBeInTheDocument();
      expect(within(unclassified).getByText(`Invalid plan assignment ${index + 1}.`)).toBeInTheDocument();
    });
    expect(within(unclassified).getAllByRole("listitem")).toHaveLength(admittedPaths.length);
    expect(screen.getByRole("region", { name: "Roadmap diagnostics" })).toHaveTextContent("Planning metadata is inconsistent.");
    expect(screen.getByRole("group", { name: "Releases for selected Epic" })).not.toHaveTextContent("uncategorized");
    expect(screen.queryByRole("button", { name: /uncategorized/ })).not.toBeInTheDocument();
    expect(document.querySelectorAll(".backlog-todo")).toHaveLength(0);
  });
  it("opens a Project-bound Release from Roadmap and returns to the same focus", async () => {
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=roadmap");
    render(<Projects />);
    const release = await screen.findByRole("button", { name: /Project UI and real Roadmap/ });
    const releaseCard = release.closest("section")!;
    expect(releaseCard).not.toHaveTextContent("•••");
    const openRelease = within(releaseCard).getByRole("button", { name: "Abrir Release" });
    expect(openRelease).toHaveTextContent("›");
    fireEvent.click(openRelease);
    expect(location.search).toContain("page=release");
    expect(location.search).toContain("epic_id=e-01");
    expect(location.search).toContain("release_id=r-04");
    expect(await screen.findByRole("heading", { name: "Project UI and real Roadmap", level: 1 })).toBeInTheDocument();
    expect(screen.getByRole("region", { name: "Contexto da release" })).toHaveTextContent("builder Local projects");
    expect(screen.getByRole("region", { name: "Contexto da release" })).not.toHaveTextContent("Caminho de publicação");
    expect(screen.getByRole("region", { name: "Contexto da release" })).not.toHaveTextContent("Evidência de publicação");
    expect(screen.getByRole("region", { name: "TODOs executáveis" })).toHaveTextContent("Project Roadmap connection");
    expect(screen.getByRole("region", { name: "TODOs executáveis" })).not.toHaveTextContent("Planning ledger");
    fireEvent.click(screen.getByRole("button", { name: /Voltar ao Roadmap/ }));
    expect(location.search).toContain("page=roadmap");
    expect(location.search).toContain("release_id=r-04");
  });
  it("loads Release studies on demand without treating them as TODOs", async () => {
    const baseFetch = globalThis.fetch;
    const fetcher = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = new URL(String(input), location.origin);
      if (url.pathname === "/api/roadmap" && url.searchParams.has("release_id")) return Promise.resolve({ ok: true, json: async () => ({ company_id: "belluga", project_id: "builder", release_id: "r-04", state: "available", document: { title: "Release research", introduction: "Decision context", materials: [{ id: "study-1", title: "Architecture study", body_markdown: "A **human-readable** finding.", source_refs: ["source:study-1"] }] } }) } as Response);
      return baseFetch(input, init);
    });
    vi.stubGlobal("fetch", fetcher);
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=release&epic_id=e-01&release_id=r-04");
    render(<Projects />);
    expect(await screen.findByRole("heading", { name: "Release research" })).toBeInTheDocument();
    expect(screen.getByText("Architecture study")).toBeInTheDocument();
    fireEvent.click(screen.getByText("Architecture study"));
    expect(screen.getByRole("list", { name: "Fontes do estudo" })).toHaveTextContent("source:study-1");
    expect(screen.getByRole("region", { name: "TODOs executáveis" })).not.toHaveTextContent("Architecture study");
    expect(fetcher.mock.calls.some(([input]) => new URL(String(input), location.origin).searchParams.has("release_id"))).toBe(true);
  });
  it("shows truthful delivered and invalid Release states without planning-ledger arithmetic", async () => {
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=release&epic_id=e-01&release_id=r-01");
    render(<Projects />);
    expect(await screen.findByRole("heading", { name: "First local viewer", level: 1 }, { timeout: 5000 })).toBeInTheDocument();
    expect(document.querySelector(".release-badge")).toHaveTextContent("Entregue");
    expect(screen.getByRole("region", { name: "TODOs executáveis" })).not.toHaveTextContent("Builder genesis ledger");
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=release&epic_id=e-02&release_id=r-01");
    window.dispatchEvent(new PopStateEvent("popstate"));
    expect(await screen.findByText("Release indisponível", {}, { timeout: 5000 })).toBeInTheDocument();
  });
  it("keeps superseded history visible without counting it as delivered work", async () => {
    const saved = detail("belluga", "builder");
    const historical = { ...saved.roadmap.epics[0].releases[0].todos[2], id: "old-plan", title: "Replaced historical plan", path: "todos/completed/old.md", ...unverifiedStage("unknown", "") };
    Object.assign(historical, { historical_disposition: "Superseded", disposition_reason: "Replaced by a narrower task.", disposition_state: "valid" });
    saved.roadmap.epics[0].releases[0].todos.push(historical as never);
    mockProjectDetail(saved);
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=release&epic_id=e-01&release_id=r-01");
    render(<Projects />);
    expect(await screen.findByRole("heading", { name: "First local viewer", level: 1 })).toBeInTheDocument();
    expect(document.querySelector(".release-badge")).toHaveTextContent("Entregue");
    expect(screen.getByRole("region", { name: "Histórico não entregue" })).toHaveTextContent("Replaced historical plan");
    expect(screen.getByRole("region", { name: "TODOs executáveis" })).not.toHaveTextContent("Replaced historical plan");
  });
  it("selects Company and Project before entering its dedicated page", async () => {
    render(<Projects />);
    expect(await screen.findByRole("heading", { name: "Choose a company to explore its projects." })).toBeInTheDocument();
    const companyCard = screen.getByRole("button", { name: /Belluga/ });
    expect(companyCard).toHaveTextContent("2 registered Projects");
    expect(companyCard).toHaveTextContent("Open company portfolio");
    fireEvent.click(companyCard);
    expect(await screen.findByRole("heading", { name: "Explore registered Projects for Belluga." })).toBeInTheDocument();
    expect(document.querySelector(".portfolio-hero aside")).toHaveTextContent("Registered Projects2");
    fireEvent.click((await screen.findAllByRole("button", { name: /Explore Project/ }))[0]);
    expect(await screen.findByLabelText("Switch Project")).toHaveValue("builder");
    expect(location.pathname).toBe("/");
    expect(screen.getByRole("navigation", { name: "Project pages" })).toBeInTheDocument();
  });
  it("applies Project Design System roles to the whole Project page", async () => {
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=overview");
    render(<Projects />);
    await screen.findByText("Overview for builder");
    expect((document.querySelector(".projects-project") as HTMLElement).style.getPropertyValue("--project-accent")).toBe("#32bbdd");
    expect(screen.getByText("Overview for builder")).toBeInTheDocument();
  });
  it("keeps a selected v1 system structurally readable without inventing a Project theme", async () => {
    const selected = designSystemRead("belluga", "builder");
    selected.evaluation.items[0].schema_version = "1";
    selected.evaluation.presentation_validation = "not_ready";
    const fallback = globalThis.fetch;
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => new URL(String(input), location.origin).pathname === "/api/design-system"
      ? Promise.resolve({ ok: true, json: async () => selected }) : fallback(input)));
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=roadmap");
    render(<Projects />);
    expect(await screen.findByText(/Design System indisponível:/)).toHaveTextContent("presentation is not ready");
    expect((document.querySelector(".projects-project") as HTMLElement).style.getPropertyValue("--project-accent")).toBe("");
    expect(await screen.findByRole("heading", { name: "Roadmap" })).toBeInTheDocument();
  });
  it("keeps Roadmap and Documents navigable when the Design System is invalid", async () => {
    const saved = detail("belluga", "builder", [{ path: "project_mandate.md", url: `/snapshot/belluga/builder/${fingerprint}/project_mandate.md` }]);
    saved.artifacts[0] = { ...saved.artifacts[0], state: "invalid" };
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), location.origin);
      if (url.pathname === "/api/roadmap") return { ok: true, json: async () => saved.roadmap };
      return { ok: true, json: async () => url.searchParams.has("project_id") ? saved : { companies, projects } };
    }));
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=roadmap");
    render(<Projects />);
    expect(await screen.findByRole("heading", { name: "Roadmap" })).toBeInTheDocument();
    expect(screen.getByRole("status")).toHaveTextContent("Design System");
    fireEvent.click(screen.getByRole("button", { name: "Documents / decisions" }));
    expect(await screen.findByRole("heading", { name: "Documentação" })).toBeInTheDocument();
  });
  it("renders the Registry Overview narrative and revision-pinned media", async () => {
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=overview");
    render(<Projects />);
    expect(await screen.findByRole("heading", { name: "The problem" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "The proposal" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "The impact" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "For whom" })).toBeInTheDocument();

    const narrative = document.querySelector(".overview-editorial");
    expect(narrative).toBeInTheDocument();
    expect(document.querySelector(".overview-status")).not.toBeInTheDocument();
    expect(screen.queryByText(/Saved snapshot:/)).not.toBeInTheDocument();
    expect(await screen.findByRole("link", { name: /Ver landing independente/ })).toHaveAttribute("target", "_blank");
    expect(screen.queryByRole("region", { name: "Executive Landing artifact" })).not.toBeInTheDocument();
    expect(screen.queryByText("MVP")).not.toBeInTheDocument();
    const hero = document.querySelector(".project-hero");
    expect(hero?.querySelector("h1")).toHaveTextContent("Builder");
    expect(hero?.querySelector(".overview-hero-tagline")).toHaveTextContent("Product direction");
    expect(screen.getByAltText("Desktop Project view")).toBeInTheDocument();
    expect(screen.getByAltText("Mobile Project view")).toBeInTheDocument();
    expect(screen.getByText("CONCEITO · DADOS ILUSTRATIVOS")).toBeInTheDocument();
    expect(screen.getByAltText("Admitted Builder document view")).toHaveAttribute("src", "/api/overview/media/belluga/builder/1/proposal");
  });
  it("keeps Overview editorial separate from an empty Documents state", async () => {
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=overview");
    render(<Projects />);
    expect(await screen.findByText("Problem evidence")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Documents / decisions" }));
    expect(await screen.findByText("Documentação ainda não iniciada")).toBeInTheDocument();
  });
  it("does not substitute portfolio text for a missing admitted Overview", async () => {
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=overview");
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), location.origin);
      if (url.pathname === "/api/overview") return { ok: true, json: async () => ({ state: "not_authored", company_id: "belluga", project_id: "builder", teach: "TEACH: request targeted Overview status." }) };
      if (url.pathname.startsWith("/snapshot/")) return { ok: true, headers: new Headers({ "X-Builder-Snapshot-Fingerprint": fingerprint }), json: async () => theme };
      if (url.searchParams.has("project_id")) { const result = detail("belluga", "builder"); return { ok: true, json: async () => ({ ...result, artifacts: [...result.artifacts, { id: "overview", kind: "overview", name: "Project Overview", state: "pending", diagnostic: "Project Overview is not available in the published Foundation snapshot. TEACH: request targeted Overview status." }], overview: undefined }) }; }
      return { ok: true, json: async () => ({ companies, projects }) };
    }));
    render(<Projects />);
    expect(await screen.findByRole("heading", { name: "Project Overview not authored yet" })).toBeInTheDocument();
    expect(screen.queryByText("Summary for builder")).not.toBeInTheDocument();
    expect(screen.getByText(/TEACH: request targeted Overview status/)).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: /Ver landing independente/ })).not.toBeInTheDocument();
  });
  it("labels missing hero media against the loaded Overview, not a continuously current source", async () => {
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=overview");
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), location.origin);
      if (url.pathname === "/api/overview") return { ok: true, json: async () => ({ state: "invalid", company_id: "belluga", project_id: "builder", teach: "TEACH: Registry omitted the required hero media." }) };
      if (url.pathname.startsWith("/snapshot/")) return { ok: true, headers: new Headers({ "X-Builder-Snapshot-Fingerprint": fingerprint }), json: async () => theme };
      if (url.searchParams.has("project_id")) { const result = detail("belluga", "builder"); return { ok: true, json: async () => ({ ...result, overview: { ...result.overview, media: {} } }) }; }
      return { ok: true, json: async () => ({ companies, projects }) };
    }));
    render(<Projects />);
    expect(await screen.findByRole("heading", { name: "Project Overview is invalid" })).toBeInTheDocument();
  });
  it("renders stale Mongo Overview TEACH without Foundation review metadata", async () => {
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), location.origin);
      if (url.pathname === "/api/overview") return { ok: true, json: async () => ({ ...overviewRead("belluga", "builder"), state: "stale", teach: "TEACH: Review the current source parity." }) };
      if (url.pathname === "/api/design-system") return { ok: true, json: async () => designSystemRead("belluga", "builder") };
      if (url.pathname === "/api/roadmap") return { ok: true, json: async () => roadmap("builder") };
      if (url.pathname === "/api/local-preview" && !url.searchParams.has("project_id")) return { ok: true, json: async () => ({ companies, projects }) };
      if (url.pathname === "/api/local-preview") return { ok: true, json: async () => detail("belluga", "builder") };
      return { ok: true, json: async () => theme };
    }));
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=overview");
    render(<Projects />);
    expect(await screen.findByText("TEACH: Review the current source parity.")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "The problem" })).toBeInTheDocument();
    expect(screen.queryByRole("region", { name: "Snapshot status" })).not.toBeInTheDocument();
  });
  it("shows one status column and derives Release status without counting ledgers", async () => {
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=roadmap");
    render(<Projects />);
    expect(await screen.findByRole("heading", { name: "Roadmap" })).toBeInTheDocument();
    expect(document.querySelector(".roadmap-release-panel")).toBeInTheDocument();
    expect(document.querySelector(".roadmap-context")).not.toHaveTextContent("Não verificada");
    expect(screen.getByRole("navigation", { name: "Roadmap useful links" })).toHaveTextContent("Documentação");
    expect(screen.getByRole("group", { name: "Select Epic" })).toHaveTextContent("builder Local projects");
    expect(document.querySelector(".roadmap-context")).toHaveTextContent("Project UI and real Roadmap");
    expect(screen.getByRole("button", { name: /First local viewer/ })).toHaveAttribute("aria-expanded", "false");
    expect(screen.getByRole("button", { name: /First local viewer/ })).toHaveTextContent("Entregue");
    expect(screen.getByRole("button", { name: /Project UI and real Roadmap/ })).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByRole("button", { name: /Project UI and real Roadmap/ })).toHaveTextContent("Bloqueada");
    expect(within(screen.getByRole("group", { name: "Releases for selected Epic" })).getByText("Project UI and real Roadmap")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /First local viewer/ })).toBeInTheDocument();
    expect(screen.queryByRole("columnheader", { name: "ENTREGA" })).not.toBeInTheDocument();
    expect(screen.getByRole("columnheader", { name: "STATUS" })).toBeInTheDocument();
    expect(screen.queryByText(/Delivery not verified|Entrega não verificada/)).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /First local viewer/ }));
    fireEvent.click(screen.getByRole("button", { name: /First local viewer/ }));
    expect(screen.getAllByText("Planning ledger")).toHaveLength(2);
    const completedTodo = screen.getByText(/Builder genesis ledger/).closest("tr");
    expect(completedTodo).not.toBeNull();
    expect(within(completedTodo!).getByText("Local record: completed")).toBeInTheDocument();
    expect(within(completedTodo!).getByText("—")).toBeInTheDocument();
    const readyTodo = screen.getByText(/Published MVP task/).closest("tr");
    expect(within(readyTodo!).getByText("Concluído")).toHaveClass("done");
    fireEvent.click(screen.getByRole("button", { name: /Project UI and real Roadmap/ }));
    const localTodo = screen.getByText(/Project Roadmap connection/).closest("tr");
    expect(localTodo).not.toBeNull();
    expect(within(localTodo!).getByText("Bloqueado · Implementado localmente")).toHaveClass("blocked");
    const promotedTodo = screen.getByText(/Promotion pending task/).closest("tr");
    expect(within(promotedTodo!).getByText("Em promoção")).toHaveClass("validation");
    fireEvent.click(screen.getByRole("button", { name: /builder Internal service/ }));
    await waitFor(() => expect(screen.getByRole("group", { name: "Releases for selected Epic" })).toHaveTextContent("Company and Project Schema Study"));
    expect(within(screen.getByRole("group", { name: "Releases for selected Epic" })).getByText("Company and Project Schema Study")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Company and Project Schema Study/ })).toHaveTextContent("Status indisponível");
    expect(document.querySelector(".roadmap-context")).toHaveTextContent("Company and Project Schema Study");
    expect(screen.queryByText("Project Roadmap connection")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /builder Other developers/ }));
    expect(screen.getByText("No Releases defined yet.")).toBeInTheDocument();
    expect(document.querySelector(".roadmap-context")).toHaveTextContent("Sem Release neste épico");
  }, 15000);
  it("uses one canonical label map and shows per-TODO TEACH from Roadmap", async () => {
    const saved = detail("belluga", "builder");
    const stages = [
      ["Planning", "Planejamento", "planned"],
      ["Implementation-Approval-Pending", "Aguardando aprovação", "approval"],
      ["Ready-for-Implementation", "Pronto para implementar", "ready"],
      ["In-Progress", "Em andamento", "active"],
      ["Local-Implemented", "Implementado localmente", "implemented"],
      ["Promotion-Lane", "Em promoção", "validation"],
      ["Completed", "Concluído", "done"],
    ] as const;
    const projected: unknown[] = stages.map(([stage], index) => ({
      id: `canonical-${index}`, title: `Canonical stage ${index}`, path: `todos/active/canonical-${index}.md`,
      kind: "executable" as const, record_state: "active", ...verifiedStage(stage), objective_state: "absent" as const, next_step_state: "absent" as const,
    }));
    projected.push({
      id: "needs-teach", title: "Unverified status TODO", path: "todos/active/unverified.md", kind: "executable", record_state: "active",
      ...unverifiedStage("Production-Ready", "TEACH: review this stage before use."), objective_state: "absent", next_step_state: "absent",
    });
    (saved.roadmap.epics[0].releases[1] as unknown as { todos: unknown[] }).todos = projected;
    mockProjectDetail(saved);
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=release&epic_id=e-01&release_id=r-04");
    render(<Projects />);
    await screen.findByRole("heading", { name: "Project UI and real Roadmap", level: 1 });
    await screen.findByText("Canonical stage 0");
    for (const [stage, label, tone] of stages) {
      const row = screen.getByText(`Canonical stage ${stages.findIndex(([id]) => id === stage)}`).closest<HTMLElement>(".release-work-row")!;
      expect(within(row).getByText(label)).toHaveClass(tone);
    }
    const unverified = screen.getByText("Unverified status TODO").closest<HTMLElement>(".release-work-row")!;
    expect(within(unverified).getByText("Status indisponível")).toHaveClass("unknown");
    expect(within(unverified).getByText("TEACH: review this stage before use.")).toBeInTheDocument();
    fireEvent.click(within(unverified).getByRole("button", { name: /Unverified status TODO/ }));
    const preview = within(await screen.findByRole("dialog", { name: "Prévia do TODO" })).getByRole("complementary", { name: "Prévia do TODO" });
    expect(within(preview).getByText("TEACH: review this stage before use.")).toBeInTheDocument();
  });
  it("uses the same admitted Epic and Release projection in Overview", async () => {
    const scrollIntoView = vi.fn();
    Object.defineProperty(HTMLElement.prototype, "scrollIntoView", { configurable: true, value: scrollIntoView });
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=overview");
    render(<Projects />);
    const overview = await screen.findByRole("region", { name: "Project Roadmap overview" });
    expect(within(overview).getByRole("group", { name: "Select Epic" })).toHaveTextContent("builder Local projects");
    expect(within(overview).getByRole("group", { name: "Releases for selected Epic" })).toHaveTextContent("Project UI and real Roadmap");
    const releaseRail = within(overview).getByRole("group", { name: "Releases for selected Epic" });
    expect(within(releaseRail).getByRole("button", { name: /First local viewer/ })).toHaveTextContent("Entregue");
    expect(within(releaseRail).getByRole("button", { name: /Project UI and real Roadmap/ })).toHaveTextContent("Bloqueada");
    expect(within(overview).queryByText("1 / 5")).not.toBeInTheDocument();
    fireEvent.click(within(overview).getByRole("button", { name: "Next Epic" }));
    expect(within(overview).getByRole("group", { name: "Select Epic" }).querySelector("button.selected")).toHaveTextContent("Other developers");
    expect(within(overview).getByRole("group", { name: "Select Epic" }).querySelector("button.selected")).toHaveAttribute("aria-pressed", "true");
    fireEvent.click(within(overview).getByRole("button", { name: "Previous Epic" }));
    expect(within(overview).getByRole("group", { name: "Select Epic" }).querySelector("button.selected")).toHaveTextContent("Local projects");
    expect(scrollIntoView).not.toHaveBeenCalled();
    fireEvent.click(within(overview).getByRole("button", { name: /builder Internal service/ }));
    expect(within(overview).getByRole("group", { name: "Releases for selected Epic" })).toHaveTextContent("Company and Project Schema Study");
    fireEvent.click(within(overview).getByRole("button", { name: /builder Other developers/ }));
    expect(within(overview).getByRole("group", { name: "Releases for selected Epic" })).toHaveTextContent("No Releases defined yet.");
  });
  it.each([
    { name: "planned", todos: [{ kind: "executable", ...verifiedStage("Planning") }], expected: "Planejada" },
    { name: "approval pending is not started", todos: [{ kind: "executable", ...verifiedStage("Implementation-Approval-Pending") }], expected: "Planejada" },
    { name: "ready is not started", todos: [{ kind: "executable", ...verifiedStage("Ready-for-Implementation") }], expected: "Planejada" },
    { name: "first work is started", todos: [{ kind: "executable", ...verifiedStage("In-Progress") }], expected: "Em andamento" },
    { name: "mixed", todos: [{ kind: "executable", ...verifiedStage("Completed") }, { kind: "executable", ...verifiedStage("Planning") }], expected: "Em andamento" },
    { name: "unknown", todos: [{ kind: "executable", ...unverifiedStage() }, { kind: "executable", ...verifiedStage("Completed") }], expected: "Status indisponível" },
    { name: "invalid qualifier is not delivered", todos: [{ kind: "executable", ...verifiedStage("Completed"), qualifier_state: "unverified", qualifier_teach: "TEACH: correct the qualifier" }], expected: "Status indisponível" },
    { name: "planning-only", todos: [{ kind: "planning", ...verifiedStage("Completed") }], expected: "Status indisponível" },
    { name: "blocked ready", todos: [{ kind: "executable", ...verifiedStage("Completed", "Completed", true) }], expected: "Bloqueada" },
    { name: "invalid blocked qualifier remains unknown", todos: [{ kind: "executable", ...verifiedStage("Completed", "Completed", true), qualifier_state: "unverified", qualifier_teach: "TEACH: correct the qualifier" }], expected: "Status indisponível" },
    { name: "work note does not impersonate blocked qualifier", todos: [{ kind: "executable", ...verifiedStage("Completed"), work_state: "blocked" }], expected: "Entregue" },
  ])("derives $name Release status without a second delivery field", async ({ todos, expected }) => {
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), location.origin);
      if (url.pathname === "/api/roadmap") {
        const saved = detail("belluga", "builder");
        const members = todos.map((todo, index) => ({ id: `status-${index}`, title: `Status TODO ${index}`, path: `todos/active/status-${index}.md`, record_state: "active", ...todo }));
        return { ok: true, json: async () => ({ ...saved.roadmap, epics: saved.roadmap.epics.map((epic) => ({ ...epic, releases: epic.releases.map((release) => release.id === "r-01" ? { ...release, todos: members } : release) })) }) };
      }
      if (url.pathname.startsWith("/snapshot/")) return { ok: true, headers: new Headers({ "X-Builder-Snapshot-Fingerprint": fingerprint }), json: async () => theme };
      if (!url.searchParams.has("project_id")) return { ok: true, json: async () => ({ companies, projects }) };
      const saved = detail("belluga", "builder");
      const members = todos.map((todo, index) => ({ id: `status-${index}`, title: `Status TODO ${index}`, path: `todos/active/status-${index}.md`, record_state: "active", ...todo }));
      return { ok: true, json: async () => ({ ...saved, roadmap: { ...saved.roadmap, epics: saved.roadmap.epics.map((epic) => ({ ...epic, releases: epic.releases.map((release) => release.id === "r-01" ? { ...release, todos: members } : release) })) } }) };
    }));
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=roadmap");
    render(<Projects />);
    const release = await screen.findByRole("button", { name: /First local viewer/ });
    expect(release).toHaveTextContent(expected);
    expect(screen.queryByRole("columnheader", { name: "ENTREGA" })).not.toBeInTheDocument();
  });
  it("updates the derived Release status only after a manual page reload", async () => {
    let roadmapReads = 0;
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), location.origin);
      if (url.pathname === "/api/roadmap") {
        const saved = detail("belluga", "builder");
        roadmapReads++;
        saved.roadmap.epics[0].releases[0].todos = [{ id: "work", title: "Published work", path: "todos/active/work.md", kind: "executable", record_state: "active", ...(roadmapReads > 1 ? verifiedStage("Local-Implemented") : verifiedStage("Planning", "Pending")) }];
        return { ok: true, json: async () => saved.roadmap };
      }
      if (url.pathname.startsWith("/snapshot/")) return { ok: true, headers: new Headers({ "X-Builder-Snapshot-Fingerprint": fingerprint }), json: async () => theme };
      if (!url.searchParams.has("project_id")) return { ok: true, json: async () => ({ companies, projects }) };
      const saved = detail("belluga", "builder");
      return { ok: true, json: async () => saved };
    }));
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=roadmap");
    const mounted = render(<Projects />);
    expect(await screen.findByRole("button", { name: /First local viewer/ })).toHaveTextContent("Planejada");
    fireEvent(document, new Event("visibilitychange"));
    expect(roadmapReads).toBe(1);
    expect(screen.getByRole("button", { name: /First local viewer/ })).toHaveTextContent("Planejada");
    mounted.unmount();
    render(<Projects />);
    await waitFor(() => expect(screen.getByRole("button", { name: /First local viewer/ })).toHaveTextContent("Em andamento"));
    expect(roadmapReads).toBe(2);
  });
  it("focuses the first incomplete Release and shares manual Roadmap focus by URL", async () => {
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=roadmap");
    render(<Projects />);
    expect(await screen.findByRole("button", { name: /Project UI and real Roadmap/ })).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByRole("button", { name: /First local viewer/ })).toHaveAttribute("aria-expanded", "false");
    expect(location.search).not.toContain("release_id");
    fireEvent.click(screen.getByRole("button", { name: /First local viewer/ }));
    expect(location.search).toContain("release_id=r-01");
    fireEvent.click(screen.getByRole("button", { name: /Project UI and real Roadmap/ }));
    expect(location.search).toContain("epic_id=e-01");
    expect(location.search).toContain("release_id=r-04");
    expect(screen.getByRole("button", { name: /Project UI and real Roadmap/ })).toHaveAttribute("aria-expanded", "true");
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=roadmap&release_id=r-05");
    window.dispatchEvent(new PopStateEvent("popstate"));
    await waitFor(() => expect(screen.getByRole("group", { name: "Releases for selected Epic" })).toHaveTextContent("Company and Project Schema Study"));
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=roadmap&epic_id=e-02&release_id=r-05");
    window.dispatchEvent(new PopStateEvent("popstate"));
    await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("link is invalid"));
  });
  it("drags the Overview rails without turning a drag into an Epic selection", async () => {
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=overview");
    render(<Projects />);
    const overview = await screen.findByRole("region", { name: "Project Roadmap overview" });
    const epics = within(overview).getByRole("group", { name: "Select Epic" });
    epics.scrollLeft = 100;
    fireEvent.mouseDown(epics, { button: 0, clientX: 100 });
    fireEvent.mouseMove(epics, { buttons: 1, clientX: 55 });
    fireEvent.mouseUp(epics);
    expect(epics.scrollLeft).toBe(145);
    fireEvent.click(within(epics).getByRole("button", { name: /builder Other developers/ }));
    expect(epics.querySelector("button.selected")).toHaveTextContent("Local projects");
    fireEvent.click(within(epics).getByRole("button", { name: /builder Other developers/ }));
    expect(epics.querySelector("button.selected")).toHaveTextContent("Other developers");

    fireEvent.click(within(epics).getByRole("button", { name: /builder Local projects/ }));
    const releases = within(overview).getByRole("group", { name: "Releases for selected Epic" });
    releases.scrollLeft = 20;
    fireEvent.mouseDown(releases, { button: 0, clientX: 100 });
    fireEvent.mouseMove(releases, { buttons: 1, clientX: 60 });
    fireEvent.mouseUp(releases);
    expect(releases.scrollLeft).toBe(60);
  });
  it("shows an honest Roadmap state when catalog data is absent", async () => {
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), location.origin);
      if (url.pathname === "/api/roadmap") return { ok: false, status: 503, json: async () => ({ error: "roadmap_api_unavailable" }) };
      if (url.pathname.startsWith("/snapshot/")) return { ok: true, headers: new Headers({ "X-Builder-Snapshot-Fingerprint": fingerprint }), json: async () => theme };
      if (!url.searchParams.has("project_id")) return { ok: true, json: async () => ({ companies, projects }) };
      const saved = detail("belluga", "builder");
      return { ok: true, json: async () => ({ ...saved, roadmap: undefined, artifacts: saved.artifacts.map((item) => item.kind === "roadmap" ? { ...item, state: "pending", diagnostic: "Roadmap catalog is not available yet. Register after adding the catalog." } : item) }) };
    }));
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=roadmap");
    render(<Projects />);
    expect(await screen.findByText(/TEACH: Mongo Roadmap indisponível/)).toBeInTheDocument();
    expect(screen.queryByText(/Local projects/)).not.toBeInTheDocument();
    expect(screen.queryByText(/Project UI and real Roadmap/)).not.toBeInTheDocument();
  });
  it("hard-cuts over v2 Roadmap and keeps the Project Overview navigable", async () => {
    const saved = detail("belluga", "builder");
    saved.roadmap.schema_version = "builder-roadmap-projection-v2";
    mockProjectDetail(saved);
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=roadmap");
    render(<Projects />);
    expect(await screen.findByText("Project Roadmap data is not available in this saved snapshot.")).toBeInTheDocument();
    fireEvent.click(within(screen.getByRole("navigation", { name: "Project pages" })).getByRole("button", { name: "Overview" }));
    expect(await screen.findByText("Product direction")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /Ver landing independente/ })).toHaveAttribute("target", "_blank");
  });
  it("keeps the Overview honest when its Roadmap catalog is absent", async () => {
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), location.origin);
      if (url.pathname === "/api/overview") return { ok: true, json: async () => overviewRead("belluga", "builder") };
      if (url.pathname === "/api/design-system") return { ok: true, json: async () => designSystemRead("belluga", "builder") };
      if (url.pathname === "/api/roadmap") return { ok: false, status: 503, json: async () => ({ error: "roadmap_api_unavailable" }) };
      if (url.pathname.startsWith("/snapshot/")) return { ok: true, headers: new Headers({ "X-Builder-Snapshot-Fingerprint": fingerprint }), json: async () => theme };
      if (!url.searchParams.has("project_id")) return { ok: true, json: async () => ({ companies, projects }) };
      const saved = detail("belluga", "builder");
      return { ok: true, json: async () => ({ ...saved, roadmap: undefined, artifacts: saved.artifacts.map((item) => item.kind === "roadmap" ? { ...item, state: "pending", diagnostic: "Roadmap catalog is not available yet. Register after adding the catalog." } : item) }) };
    }));
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=overview");
    render(<Projects />);
    expect(await screen.findByText(/TEACH: Mongo Roadmap indisponível/)).toBeInTheDocument();
    expect(screen.queryByText(/Local projects|Project UI and real Roadmap/)).not.toBeInTheDocument();
    expect(screen.queryByText(/demo|delivered|shipped/i)).not.toBeInTheDocument();
  });
  it("resets selected Epic and Project-bound Roadmap data when the Project changes", async () => {
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), location.origin);
      if (url.pathname === "/api/roadmap") {
        const projectID = url.searchParams.get("project_id") || "builder";
        const saved = detail("belluga", projectID);
        return { ok: true, json: async () => projectID === "boora" ? { ...saved.roadmap, epics: [] } : saved.roadmap };
      }
      if (url.pathname.startsWith("/snapshot/")) return { ok: true, headers: new Headers({ "X-Builder-Snapshot-Fingerprint": fingerprint }), json: async () => theme };
      if (!url.searchParams.has("project_id")) return { ok: true, json: async () => ({ companies, projects }) };
      const projectID = url.searchParams.get("project_id")!;
      const saved = detail("belluga", projectID);
      if (projectID !== "boora") return { ok: true, json: async () => saved };
      return { ok: true, json: async () => ({ ...saved, roadmap: undefined, artifacts: saved.artifacts.map((item) => item.kind === "roadmap" ? { ...item, state: "pending", diagnostic: "Roadmap catalog is not available for this Project." } : item) }) };
    }));
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=roadmap");
    render(<Projects />);
    expect(await screen.findByRole("button", { name: /Project UI and real Roadmap/ })).toHaveAttribute("aria-expanded", "true");
    fireEvent.click(screen.getByRole("button", { name: /builder Internal service/ }));
    expect(within(screen.getByRole("group", { name: "Releases for selected Epic" })).getByText("Company and Project Schema Study")).toBeInTheDocument();
    history.pushState({}, "", "/?company_id=belluga&project_id=boora&page=roadmap");
    window.dispatchEvent(new PopStateEvent("popstate"));
    expect(await screen.findByText("Nenhum Epic ou Release planejado.")).toBeInTheDocument();
    expect(screen.getByRole("group", { name: "Select Epic" })).toHaveTextContent("Nenhum Epic ou Release planejado.");
    expect(screen.queryByText(/builder Local projects|builder Internal service|Company and Project Schema Study|Project UI and real Roadmap|Project Roadmap connection/)).not.toBeInTheDocument();
  });
  it("does not expose admitted Markdown when canonical knowledge is pending", async () => {
    const legacyURL = "/snapshot/example/modules/legacy.md";
    mockFetch([{ path: "modules/legacy.md", url: legacyURL }], { ok: true, body: "# Legacy content" });
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=documents");
    render(<Projects />);
    expect(await screen.findByText("Documentação ainda não iniciada")).toBeInTheDocument();
    expect(screen.queryByText("Legacy content")).not.toBeInTheDocument();
    expect(vi.mocked(fetch).mock.calls.some(([url]) => String(url) === legacyURL)).toBe(false);
  });
  it("teaches fresh registration when the private reader rejects an old path-only binding", async () => {
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), location.origin);
      if (url.pathname === "/api/overview") return { ok: true, json: async () => overviewRead("belluga", "builder") };
      if (url.searchParams.has("project_id")) return { ok: false, status: 503, json: async () => ({ company_id: "belluga", project_id: "builder", diagnostic: "TEACH: This Project has an invalid path-only registration." }) };
      return { ok: true, json: async () => ({ companies, projects }) };
    }));
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=overview");
    render(<Projects />);
    expect(await screen.findByRole("heading", { name: "The problem" })).toBeInTheDocument();
    expect(screen.getByRole("navigation", { name: "Project pages" })).toBeInTheDocument();
  });
  it("ignores a late Project detail after switching to another Project", async () => {
    let releaseBuilder!: (value: unknown) => void;
    const lateBuilder = new Promise<unknown>((resolve) => { releaseBuilder = resolve; });
    const booraFingerprint = "b".repeat(64);
    const booraProjects = projects.map((item) => item.id === "boora" ? { ...item, snapshot_fingerprint: booraFingerprint } : item);
    const booraTheme = { tokens: theme.tokens.map((item) => item.id === "color-accent" ? { ...item, value: "#cc99ee" } : item) };
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), location.origin);
      if (url.pathname === "/api/overview") return { ok: true, json: async () => overviewRead("belluga", url.searchParams.get("project_id") || "builder") };
      if (url.pathname === "/api/design-system") {
        const selected = designSystemRead("belluga", url.searchParams.get("project_id") || "builder");
        selected.evaluation.items[0].tokens = booraTheme.tokens;
        return { ok: true, json: async () => selected };
      }
      if (url.pathname.startsWith("/snapshot/")) return { ok: true, headers: new Headers({ "X-Builder-Snapshot-Fingerprint": booraFingerprint }), json: async () => booraTheme };
      if (!url.searchParams.has("project_id")) return { ok: true, json: async () => ({ companies, projects: booraProjects }) };
      if (url.searchParams.get("project_id") === "builder") return lateBuilder;
      const selected = detail("belluga", "boora");
      selected.snapshot_observations.knowledge_status.landing = { status: "review_required", reason_code: "source_changed" };
      selected.snapshot_observed_at = "2026-10-16T12:00:00Z";
      return { ok: true, json: async () => ({ ...selected, snapshot_fingerprint: booraFingerprint }) };
    }));
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=overview");
    render(<Projects />);
    await waitFor(() => expect(fetch).toHaveBeenCalledWith(expect.stringContaining("project_id=builder"), expect.anything()));
    history.pushState({}, "", "/?company_id=belluga&project_id=boora&page=overview");
    window.dispatchEvent(new PopStateEvent("popstate"));
    expect(await screen.findByText("Overview for boora")).toBeInTheDocument();
    expect(screen.queryByRole("region", { name: "Snapshot status" })).not.toBeInTheDocument();
    await act(async () => { releaseBuilder({ ok: true, json: async () => detail("belluga", "builder") }); });
    expect(screen.getByText("Overview for boora")).toBeInTheDocument();
    expect(screen.queryByText("Overview for builder")).not.toBeInTheDocument();
    expect(screen.queryByRole("region", { name: "Snapshot status" })).not.toBeInTheDocument();
    expect((document.querySelector(".projects-project") as HTMLElement).style.getPropertyValue("--project-accent")).toBe("#cc99ee");
  });
  it("replaces Prototype evidence and presentation identity when switching Projects", async () => {
    const revision = "b".repeat(40);
    const digest = `sha256:${"c".repeat(64)}`;
    const fetcher = vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), location.origin);
      if (url.pathname === "/api/design-system") return { ok: true, json: async () => designSystemRead("belluga", url.searchParams.get("project_id") || "builder") };
      if (url.pathname === "/api/prototypes") {
        const id = url.searchParams.get("project_id")!;
        return new Response(JSON.stringify({ revision, inventory_digest: digest, items: [{ id: `${id}-prototype`, name: `${id} Prototype`, status: "active", detail_url: `/api/prototypes/${revision}/${id}-prototype?company_id=belluga&project_id=${id}` }] }), { status: 200 });
      }
      if (url.pathname.startsWith(`/api/prototypes/${revision}/`)) {
        const id = url.searchParams.get("project_id")!;
        const prototype = { id: `${id}-prototype`, name: `${id} Prototype`, root: `prototypes/${id}`, status: "active", authoring_mode: "design_system_first", entry_point: "home.html",
        screens: [{ id: `${id}-screen`, name: `${id} Screen`, path: "home.html", default_state_id: "default", states: [{ id: "default", name: "Default", identifier_image: "home.png", approved_references: [] }] }],
        scenarios: [], transitions: [] };
        return new Response(JSON.stringify({ revision, inventory_digest: digest, item: prototype, file_urls: [{ path: `${prototype.root}/home.html`, url: `/api/prototypes/file/belluga/${id}/${revision}/${digest.slice(7)}/files/${prototype.root}/home.html` }] }), { status: 200 });
      }
      if (!url.searchParams.has("project_id")) return { ok: true, json: async () => ({ companies, projects }) };
      return { ok: true, json: async () => detail("belluga", url.searchParams.get("project_id")!) };
    });
    vi.stubGlobal("fetch", fetcher);
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=prototypes");
    render(<Projects />);
    expect(await screen.findByRole("heading", { name: "builder Screen / Default" })).toBeInTheDocument();
    expect(screen.getAllByLabelText("Prototype inspector")).toHaveLength(1);
    fireEvent.change(screen.getByLabelText("Switch Project"), { target: { value: "boora" } });
    fireEvent.click(within(await screen.findByRole("navigation", { name: "Project pages" })).getByRole("button", { name: "Prototype" }));
    expect(await screen.findByRole("heading", { name: "boora Screen / Default" })).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "builder Screen / Default" })).not.toBeInTheDocument();
    expect(screen.getAllByLabelText("Prototype inspector")).toHaveLength(1);
    expect(screen.getByRole("link", { name: /Abrir apresentação/ }).getAttribute("href")).toContain(revision);
    expect(fetcher.mock.calls.some(([input]) => String(input).startsWith("/snapshot/"))).toBe(false);
    expect(fetcher.mock.calls.some(([input]) => new URL(String(input), location.origin).pathname === "/api/design-system")).toBe(true);
  });
  it("restores the visual catalogue and opens the rendered presentation at the selected SHA", async () => {
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=design-system");
    render(<Projects />);
    expect(await screen.findByRole("heading", { name: "Builder System" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Paleta estrutural" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Hierarquia" })).toBeInTheDocument();
    const presentation = screen.getByRole("link", { name: "Abrir Design System ↗" });
    expect(presentation).toHaveAttribute("href", `/api/design-system/presentation/belluga/builder/${designRevision}/examples.html`);
    expect(presentation).toHaveAttribute("target", "_blank");
  });
  it("does not apply the Foundation snapshot theme as the Design System page theme", async () => {
    const fetcher = vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), location.origin);
      if (url.pathname === "/api/design-system") return { ok: true, json: async () => designSystemRead("belluga", "builder") };
      if (url.pathname === "/api/local-preview" && !url.searchParams.has("project_id")) return { ok: true, json: async () => ({ companies, projects }) };
      if (url.pathname === "/api/local-preview") return { ok: true, json: async () => detail("belluga", "builder") };
      if (url.pathname.endsWith("/design/system/design-system.json")) return { ok: true, headers: new Headers({ "X-Builder-Snapshot-Fingerprint": fingerprint }), json: async () => theme };
      return { ok: true, json: async () => theme };
    });
    vi.stubGlobal("fetch", fetcher);
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=design-system");
    render(<Projects />);
    expect(await screen.findByRole("heading", { name: "Builder System" })).toBeInTheDocument();
    expect(screen.getByRole("main")).toHaveStyle({ "--project-canvas": "#101820" });
  });
  it("uses the selected Design System for Project theming when raw snapshot reads fail", async () => {
    const fetcher = vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), location.origin);
      if (url.pathname === "/api/overview") return { ok: true, json: async () => overviewRead("belluga", "builder") };
      if (url.pathname === "/api/design-system") return { ok: true, json: async () => designSystemRead(url.searchParams.get("company_id") || "", url.searchParams.get("project_id") || "") };
      if (url.pathname === "/api/local-preview" && !url.searchParams.has("project_id")) return { ok: true, json: async () => ({ companies, projects }) };
      if (url.pathname === "/api/local-preview") return { ok: true, json: async () => detail("belluga", "builder") };
      if (url.pathname.endsWith("/design/system/design-system.json")) return { ok: false, status: 404, headers: new Headers(), json: async () => ({ error: "snapshot_unavailable" }) };
      return { ok: true, json: async () => theme };
    });
    vi.stubGlobal("fetch", fetcher);
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=overview");
    render(<Projects />);
    expect(await screen.findByText("Overview for builder")).toBeInTheDocument();
    await waitFor(() => expect(screen.queryByText(/Design System indisponível:/)).not.toBeInTheDocument());
    expect(screen.getByRole("main")).toHaveStyle({ "--project-accent": "#32bbdd" });
    expect(fetcher.mock.calls.some(([input]) => {
      const url = new URL(String(input), location.origin);
      return url.pathname === "/api/design-system" && url.searchParams.get("company_id") === "belluga" && url.searchParams.get("project_id") === "builder";
    })).toBe(true);
    expect(fetcher.mock.calls.some(([input]) => String(input).includes("/design/system/design-system.json"))).toBe(false);
  });
  it("uses a valid inherited Company Design System without a Project selection", async () => {
    const fetcher = vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), location.origin);
      if (url.pathname === "/api/overview") return { ok: true, json: async () => overviewRead("belluga", "builder") };
      if (url.pathname === "/api/design-system") {
        const inherited = designSystemRead("belluga", "builder");
        inherited.selection.state = "never_bound";
        inherited.evaluation.selected_level = "company";
        inherited.evaluation.items[0].owner = { level: "company", id: "belluga" };
        return { ok: true, json: async () => inherited };
      }
      if (url.pathname === "/api/local-preview" && !url.searchParams.has("project_id")) return { ok: true, json: async () => ({ companies, projects }) };
      if (url.pathname === "/api/local-preview") return { ok: true, json: async () => detail("belluga", "builder") };
      return { ok: true, json: async () => theme };
    });
    vi.stubGlobal("fetch", fetcher);
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=overview");
    render(<Projects />);
    expect(await screen.findByText("Overview for builder")).toBeInTheDocument();
    await waitFor(() => expect(screen.queryByText(/Design System indisponível:/)).not.toBeInTheDocument());
    expect(screen.getByRole("main")).toHaveStyle({ "--project-accent": "#32bbdd" });
  });
  it("refuses a Design System response from a different selected revision", async () => {
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), location.origin);
      if (url.pathname === "/api/overview") return { ok: true, json: async () => overviewRead("belluga", "builder") };
      if (url.pathname === "/api/design-system") {
        const selected = designSystemRead("belluga", "builder");
        selected.selection.sha = "f".repeat(40);
        return { ok: true, json: async () => selected };
      }
      if (url.pathname === "/api/local-preview" && !url.searchParams.has("project_id")) return { ok: true, json: async () => ({ companies, projects }) };
      if (url.pathname === "/api/local-preview") return { ok: true, json: async () => detail("belluga", "builder") };
      return { ok: true, json: async () => theme };
    }));
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=overview");
    render(<Projects />);
    expect(await screen.findByText("Overview for builder")).toBeInTheDocument();
    expect(await screen.findByText(/Design System indisponível:/)).toBeInTheDocument();
    expect(screen.getByRole("main").style.getPropertyValue("--project-accent")).toBe("");
  });
  it("loads Design System independently for a source-unbound Project", async () => {
    const unbound = { id: "builder", name: "Builder", company_id: "belluga", source_bound: false, repository_bound: true };
    const fetcher = vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), location.origin);
      if (url.pathname === "/api/design-system") return { ok: true, json: async () => designSystemRead("belluga", "builder") };
      if (url.pathname === "/api/local-preview" && url.searchParams.has("project_id")) throw new Error("Legacy snapshot must not gate Design System");
      if (url.pathname === "/api/local-preview") return { ok: true, json: async () => ({ companies, projects: [unbound] }) };
      return { ok: true, json: async () => ({ companies, projects: [unbound] }) };
    });
    vi.stubGlobal("fetch", fetcher);
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=design-system");
    render(<Projects />);
    expect(await screen.findByRole("heading", { name: "Builder System" })).toBeInTheDocument();
    expect(screen.queryByText(/artefatos publicados estão pendentes/)).not.toBeInTheDocument();
    expect(fetcher.mock.calls.some(([input]) => String(input).includes("/api/local-preview?company_id="))).toBe(false);
  });
  it("keeps Design System readable when Foundation local-preview fails", async () => {
    const fetcher = vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), location.origin);
      if (url.pathname === "/api/design-system") return { ok: true, json: async () => designSystemRead("belluga", "builder") };
      if (url.pathname === "/api/local-preview" && url.searchParams.has("project_id")) return { ok: false, status: 503, json: async () => ({ diagnostic: "Reader unavailable" }) };
      return { ok: true, json: async () => ({ companies, projects }) };
    });
    vi.stubGlobal("fetch", fetcher);
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=design-system");
    render(<Projects />);
    expect(await screen.findByRole("heading", { name: "Builder System" })).toBeInTheDocument();
    expect(screen.queryByText(/não foi possível ler os dados publicados/)).not.toBeInTheDocument();
    expect(fetcher.mock.calls.some(([input]) => String(input).startsWith("/api/design-system?"))).toBe(true);
  });
  it("shows bounded Registry TEACH within the Design System section", async () => {
    const fetcher = vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), location.origin);
      if (url.pathname === "/api/design-system") return { ok: false, status: 409, json: async () => ({ teach: "TEACH: A seleção mudou; recarregue a revisão." }) };
      return { ok: true, json: async () => ({ companies, projects }) };
    });
    vi.stubGlobal("fetch", fetcher);
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=design-system");
    render(<Projects />);
    expect(await screen.findByRole("alert")).toHaveTextContent("TEACH: A seleção mudou; recarregue a revisão.");
    expect(screen.getByRole("button", { name: "Tentar novamente" })).toBeInTheDocument();
  });
  it("retries a failed Design System read and renders the successful retry", async () => {
    let designSystemCalls = 0;
    const fetcher = vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), location.origin);
      if (url.pathname === "/api/design-system") {
        designSystemCalls += 1;
        if (designSystemCalls === 1) return { ok: false, status: 503, json: async () => ({ teach: "TEACH: Registry unavailable" }) };
        return { ok: true, json: async () => designSystemRead("belluga", "builder") };
      }
      return { ok: true, json: async () => ({ companies, projects }) };
    });
    vi.stubGlobal("fetch", fetcher);
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=design-system");
    render(<Projects />);
    expect(await screen.findByRole("alert")).toHaveTextContent("TEACH: Registry unavailable");
    fireEvent.click(screen.getByRole("button", { name: "Tentar novamente" }));
    expect(await screen.findByRole("heading", { name: "Builder System" })).toBeInTheDocument();
    expect(designSystemCalls).toBe(2);
  });
  it("ignores a late Design System response from the previous Project", async () => {
    let releaseBuilder: ((response: { ok: boolean; json: () => Promise<unknown> }) => void) | undefined;
    const builderPending = new Promise<{ ok: boolean; json: () => Promise<unknown> }>((resolve) => { releaseBuilder = resolve; });
    const fetcher = vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), location.origin);
      if (url.pathname === "/api/local-preview" && !url.searchParams.has("project_id")) return { ok: true, json: async () => ({ companies, projects }) };
      if (url.pathname === "/api/design-system") {
        const projectID = url.searchParams.get("project_id");
        if (projectID === "builder") return builderPending;
        return { ok: true, json: async () => designSystemRead("belluga", "boora", "Bóora System", "f".repeat(40)) };
      }
      if (url.pathname === "/api/local-preview" && url.searchParams.has("project_id")) return { ok: true, json: async () => detail("belluga", url.searchParams.get("project_id")!) };
      return { ok: true, json: async () => ({ companies, projects }) };
    });
    vi.stubGlobal("fetch", fetcher);
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=design-system");
    render(<Projects />);
    await waitFor(() => expect(fetcher.mock.calls.some(([input]) => new URL(String(input), location.origin).pathname === "/api/design-system")).toBe(true));
    fireEvent.change(screen.getByLabelText("Switch Project"), { target: { value: "boora" } });
    fireEvent.click(within(await screen.findByRole("navigation", { name: "Project pages" })).getByRole("button", { name: "Design System" }));
    expect(await screen.findByRole("heading", { name: "Bóora System" })).toBeInTheDocument();
    await act(async () => { releaseBuilder?.({ ok: true, json: async () => designSystemRead("belluga", "builder") }); });
    expect(screen.getByRole("heading", { name: "Bóora System" })).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Builder System" })).not.toBeInTheDocument();
  });
  it("does not render mismatched Reader data as Overview content", async () => {
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => ({ ok: true, json: async () => String(input).includes("project_id=") ? detail("second", "builder") : { companies, projects } })));
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder");
    render(<Projects />);
    expect(await screen.findByRole("heading", { name: "Overview indisponível" })).toBeInTheDocument();
    expect(screen.getByRole("navigation", { name: "Project pages" })).toBeInTheDocument();
  });
  it("uses the live reader fingerprint without comparing it to the identity catalog", async () => {
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), location.origin);
      if (url.pathname === "/api/roadmap") return { ok: true, json: async () => roadmap(url.searchParams.get("project_id") || "builder") };
      if (url.pathname.startsWith("/snapshot/")) return { ok: true, headers: new Headers({ "X-Builder-Snapshot-Fingerprint": "b".repeat(64) }), json: async () => theme };
      return { ok: true, json: async () => url.searchParams.has("project_id")
        ? { ...detail("belluga", "builder"), snapshot_fingerprint: "b".repeat(64) }
        : { companies, projects } };
    }));
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=roadmap");
    render(<Projects />);
    expect(await screen.findByRole("region", { name: "Project Roadmap" })).toBeInTheDocument();
  });
  it("rejects the artifact-only legacy link explicitly", async () => {
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&artifact=prototype:demo");
    render(<Projects />);
    expect(await screen.findByRole("alert")).toHaveTextContent(/artifact-only link is incomplete/);
  });
});
