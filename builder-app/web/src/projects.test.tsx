import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import userEvent from "@testing-library/user-event";
import { Projects } from "./projects";

const fingerprint = "a".repeat(64);
const originalScrollIntoView = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "scrollIntoView");
const companies = [{ id: "belluga", name: "Belluga" }, { id: "second", name: "Second Company" }];
const projects = [{ id: "builder", name: "Builder", company_id: "belluga", preparation_state: "ready", snapshot_fingerprint: fingerprint, artifacts: [] },
  { id: "boora", name: "Bóora!", company_id: "belluga", preparation_state: "ready", snapshot_fingerprint: fingerprint, artifacts: [] },
  { id: "other", name: "Other", company_id: "second", preparation_state: "ready", snapshot_fingerprint: fingerprint, artifacts: [] }];
const theme = { tokens: [
  ["color-canvas", "color", "#101820"], ["color-surface", "color", "#192838"], ["color-text-strong", "color", "#eeeeee"],
  ["color-text-secondary", "color", "#aaaaaa"], ["color-accent", "color", "#32bbdd"], ["color-on-accent", "color", "#111111"],
  ["color-border", "color", "#355577"], ["color-focus", "color", "#ccaa33"], ["font-family-sans", "string", "Arial"],
  ["text-body", "number", 16], ["text-display", "number", 40], ["line-body", "number", 1.5], ["radius-card", "number", 12], ["radius-small", "number", 6],
].map(([id, type, value]) => ({ id, type, value })) };
const todoMarkdown = "# Project Roadmap connection\n\n## Objective\nExact objective from Foundation.\n\n## Scope\n1. Exact authored step.\n\n## Definition of Done\n- [ ] Exact acceptance criterion.\n\n## Delivery Status Canon\n- **Next exact step:** Review the source.\n- **Primary module:** `modules/roadmap.md`\n";
function roadmap(projectID: string) { return { schema_version: "builder-roadmap-projection-v1", project_id: projectID, focus_release_id: "r-04", backlog: { todos: [{ id: "backlog:todos/active/backlog-item.md", title: "Unassigned active task", path: "todos/active/backlog-item.md", kind: "executable", record_state: "active", local_stage: "Pending" }, { id: "backlog:todos/active/backlog-item-two.md", title: "Second unassigned task", path: "todos/active/backlog-item-two.md", kind: "executable", record_state: "active", local_stage: "Pending" }], folders: [] }, unclassified: [], diagnostics: [], epics: [
  { id: "e-01", name: `${projectID} Local projects`, description: "Local Project view", releases: [
    { id: "r-01", name: "First local viewer", description: "Local knowledge", todos: [
      { id: "viewer-ledger", title: "Builder genesis ledger", path: "todos/completed/builder-genesis.md", kind: "planning", record_state: "completed", local_stage: "unknown" },
      { id: "architecture-ledger", title: "Architecture ledger", path: "todos/active/architecture.md", kind: "planning", record_state: "active", local_stage: "unknown" },
      { id: "completed-todo", title: "Published MVP task", path: "todos/completed/published.md", kind: "executable", record_state: "completed", local_stage: "Production-Ready" },
      { id: "second-done", title: "Second published task", path: "todos/completed/second.md", kind: "executable", record_state: "completed", local_stage: "Production-Ready" },
      { id: "third-done", title: "Third published task", path: "todos/completed/third.md", kind: "executable", record_state: "completed", local_stage: "Production-Ready" },
    ] },
    { id: "r-04", name: "Project UI and real Roadmap", description: "Project pages", todos: [
      { id: "roadmap-runtime", title: "Project Roadmap connection", path: "todos/active/roadmap_runtime_connection.md", kind: "executable", record_state: "active", local_stage: "Local-Implemented", work_state: "blocked" },
      { id: "promoted-todo", title: "Promotion pending task", path: "todos/promotion_lane/promoted.md", kind: "executable", record_state: "promotion_lane", local_stage: "Lane-Promoted" },
    ] },
  ] },
  { id: "e-02", name: `${projectID} Other developers`, description: "Local projects for other developers", releases: [] },
  { id: "e-03", name: `${projectID} Remote APIs`, description: "Remote Delphi APIs", releases: [] },
  { id: "e-04", name: `${projectID} Internal service`, description: "Internal company service", releases: [
    { id: "r-05", name: "Company and Project Schema Study", description: "Initial planning record", todos: [{ id: "company-schema", title: "Company and Project Schema Study", path: "todos/active/company_project_schema_study.md", kind: "planning", record_state: "active", local_stage: "Pending" }] },
  ] },
  { id: "e-05", name: `${projectID} External service`, description: "External service", releases: [] },
] }; }
function detail(companyID: string, projectID: string, documents: { path: string; url: string }[] = []) { return { consumer_readiness: "ready", company_id: companyID, project_id: projectID, snapshot_fingerprint: fingerprint, project_sha: "c".repeat(40),
  artifacts: [{ id: "system", kind: "design_system", name: "Design System", state: "available", entry_point: "design/system/examples.html", files: ["design/system/design-system.json"] },
    { id: "landing", kind: "landing", name: "Landing", state: "available", entry_point: "design/landing/index.html" },
    { id: "roadmap", kind: "roadmap", name: "Roadmap", state: "available", entry_point: "roadmap/projection.json", files: ["roadmap/projection.json", "roadmap/todo-status.json", "todos/active/roadmap_runtime_connection.md", "todos/active/backlog-item.md", "todos/active/backlog-item-two.md", "todos/active/architecture.md", "todos/active/company_project_schema_study.md", "todos/completed/builder-genesis.md", "todos/completed/published.md", "todos/completed/second.md", "todos/completed/third.md", "todos/promotion_lane/promoted.md"] }], roadmap: roadmap(projectID),
  overview: { schema_version: "1", tagline: "Product direction", summary: `Overview for ${projectID}`, desktop_alt: "Desktop Project view", mobile_alt: "Mobile Project view", media: { desktop: `/snapshot/${companyID}/${projectID}/${fingerprint}/project_overview/desktop.png`, mobile: `/snapshot/${companyID}/${projectID}/${fingerprint}/project_overview/mobile.png`, proposal: `/snapshot/${companyID}/${projectID}/${fingerprint}/project_overview/proposal.png` }, sections: {
    problem: { heading: "The problem", body: "Problem evidence", points: [] },
    proposal: { heading: "The proposal", body: "Proposal evidence", points: [], image_alt: "Admitted Builder document view" },
    impact: { heading: "The impact", body: "Impact hypothesis", points: [] },
    audience: { heading: "For whom", body: "Audience evidence", points: [] },
  } }, portfolio_summary: `Summary for ${projectID}`, documents, snapshot_observed_at: "2026-10-15T12:00:00Z", snapshot_observations: {
    knowledge_status: { landing: { status: "current", reason_code: "match" }, roadmap: { status: "current", reason_code: "match" } },
    visual_origin: { state: "matching", knowledge_source_state: "matching", roadmap_source_state: "matching", design_system_state: "matching" },
  } }; }
function mockFetch(documents: { path: string; url: string }[] = [], documentResponse?: { ok: boolean; body?: string; fingerprint?: string }, todoResponse = todoMarkdown) { vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
  const url = new URL(String(input), location.origin);
  if (documents.some((item) => item.url === String(input))) return { ok: documentResponse?.ok ?? false, headers: new Headers({ "X-Builder-Snapshot-Fingerprint": documentResponse?.fingerprint || fingerprint }), text: async (): Promise<string> => documentResponse?.body || "" };
  if (url.pathname.startsWith("/snapshot/") && url.pathname.includes("/todos/")) return { ok: true, headers: new Headers({ "X-Builder-Snapshot-Fingerprint": fingerprint }), text: async (): Promise<string> => todoResponse };
  if (url.pathname.startsWith("/snapshot/")) return { ok: true, headers: new Headers({ "X-Builder-Snapshot-Fingerprint": fingerprint }), json: async () => theme };
  return { ok: true, json: async () => url.searchParams.has("project_id") ? detail(url.searchParams.get("company_id")!, url.searchParams.get("project_id")!, documents) : { companies, projects } };
})); }
function mockProjectDetail(saved: ReturnType<typeof detail>, todoResponse = todoMarkdown) { vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
  const url = new URL(String(input), location.origin);
  if (url.pathname === "/api/local-preview") return { ok: true, json: async () => url.searchParams.has("project_id") ? saved : { companies, projects } };
  if (url.pathname.startsWith("/snapshot/") && url.pathname.includes("/todos/")) return { ok: true, headers: new Headers({ "X-Builder-Snapshot-Fingerprint": fingerprint }), text: async () => todoResponse };
  return { ok: true, headers: new Headers({ "X-Builder-Snapshot-Fingerprint": fingerprint }), json: async () => theme };
})); }
beforeEach(() => { history.replaceState({}, "", "/"); mockFetch(); });
afterEach(() => { cleanup(); vi.unstubAllGlobals(); if (originalScrollIntoView) Object.defineProperty(HTMLElement.prototype, "scrollIntoView", originalScrollIntoView); else delete (HTMLElement.prototype as Partial<HTMLElement>).scrollIntoView; });

describe("single root Project experience", () => {
  it("does not start another detail request on tab return or elapsed time", async () => {
    const interval = vi.spyOn(window, "setInterval");
    let resolveDetail!: (value: unknown) => void;
    const pendingDetail = new Promise<unknown>((resolve) => { resolveDetail = resolve; });
    const detailCalls = vi.fn();
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), location.origin);
      if (url.pathname.startsWith("/snapshot/")) return { ok: true, headers: new Headers({ "X-Builder-Snapshot-Fingerprint": fingerprint }), json: async () => theme };
      if (url.searchParams.has("project_id")) { detailCalls(); return pendingDetail as Promise<never>; }
      return { ok: true, json: async () => ({ companies, projects }) };
    }));
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=overview");
    render(<Projects />);
    await waitFor(() => expect(detailCalls).toHaveBeenCalledTimes(1));
    expect(screen.getByRole("status")).toHaveTextContent("A leitura da Foundation publicada está em andamento");
    for (let index = 0; index < 10; index++) fireEvent(document, new Event("visibilitychange"));
    expect(detailCalls).toHaveBeenCalledTimes(1);
    resolveDetail({ ok: true, json: async () => detail("belluga", "builder") });
    expect(await screen.findByRole("region", { name: "Snapshot status" })).toBeInTheDocument();
    fireEvent(document, new Event("visibilitychange"));
    expect(detailCalls).toHaveBeenCalledTimes(1);
    expect(interval.mock.calls.some(([, delay]) => delay === 30_000)).toBe(false);
    interval.mockRestore();
  });
  it("restarts elapsed time for each retry and Project selection", async () => {
    vi.useFakeTimers({ toFake: ["Date", "setInterval", "clearInterval"] });
    const requests = new Map<string, ((value: unknown) => void)[]>();
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => {
      const url = new URL(String(input), location.origin);
      if (url.pathname === "/api/local-preview" && !url.searchParams.has("project_id")) return Promise.resolve({ ok: true, json: async () => ({ companies, projects }) });
      if (url.pathname === "/api/local-preview") return new Promise((resolve) => {
        const projectID = url.searchParams.get("project_id")!;
        requests.set(projectID, [...(requests.get(projectID) || []), resolve]);
      });
      return Promise.resolve({ ok: true, headers: new Headers({ "X-Builder-Snapshot-Fingerprint": fingerprint }), json: async () => theme });
    }));
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=overview");
    render(<Projects />);
    await act(async () => { await Promise.resolve(); await Promise.resolve(); });
    expect(requests.get("builder")).toHaveLength(1);
    await act(async () => { vi.advanceTimersByTime(5_000); });
    expect(screen.getByText("Aguardando há 5 s")).toBeInTheDocument();
    await act(async () => { requests.get("builder")![0]({ ok: false, status: 500 }); await Promise.resolve(); });
    expect(screen.getByRole("alert")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Tentar novamente" }));
    await act(async () => { await Promise.resolve(); await Promise.resolve(); });
    expect(requests.get("builder")).toHaveLength(2);
    expect(screen.getByText("Aguardando há 0 s")).toBeInTheDocument();
    await act(async () => { vi.advanceTimersByTime(2_000); });
    expect(screen.getByText("Aguardando há 2 s")).toBeInTheDocument();
    act(() => { history.pushState({}, "", "/?company_id=belluga&project_id=boora&page=overview"); window.dispatchEvent(new PopStateEvent("popstate")); });
    await act(async () => { await Promise.resolve(); await Promise.resolve(); });
    expect(requests.get("boora")).toHaveLength(1);
    expect(screen.getByRole("region", { name: "Abrindo projeto" })).toHaveTextContent("Belluga / Bóora!");
    expect(screen.getByText("Aguardando há 0 s")).toBeInTheDocument();
    await act(async () => { vi.advanceTimersByTime(1_000); });
    expect(screen.getByText("Aguardando há 1 s")).toBeInTheDocument();
    expect(screen.queryByRole("progressbar")).not.toBeInTheDocument();
    expect(screen.queryByText(/\d+%/)).not.toBeInTheDocument();
    cleanup();
    vi.useRealTimers();
  });
  it("keeps the loaded Project detail through page, brand and invalid-link recovery navigation", async () => {
    const fetcher = vi.mocked(fetch);
    const detailCalls = () => fetcher.mock.calls.filter(([input]) => String(input).startsWith("/api/local-preview?company_id=")).length;
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=roadmap");
    render(<Projects />);
    expect(await screen.findByRole("button", { name: /First local viewer/ })).toBeInTheDocument();
    expect(detailCalls()).toBe(1);
    fireEvent.click(screen.getByRole("button", { name: "Documents / decisions" }));
    expect(await screen.findByRole("heading", { name: "Documents & decisions" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("link", { name: "Project Builder" }));
    expect(await screen.findByRole("region", { name: "Snapshot status" })).toBeInTheDocument();
    expect(detailCalls()).toBe(1);
    act(() => { history.pushState({}, "", "/?company_id=belluga&project_id=builder&page=missing"); window.dispatchEvent(new PopStateEvent("popstate")); });
    fireEvent.click(screen.getByRole("link", { name: "Back to Overview" }));
    expect(await screen.findByRole("region", { name: "Snapshot status" })).toBeInTheDocument();
    expect(detailCalls()).toBe(1);
    act(() => { history.pushState({}, "", "/?company_id=belluga&project_id=builder&artifact=prototype%3Ademo"); window.dispatchEvent(new PopStateEvent("popstate")); });
    fireEvent.click(screen.getByRole("link", { name: "Open Prototype" }));
    expect(await screen.findByRole("heading", { name: "Prototypes" })).toBeInTheDocument();
    expect(detailCalls()).toBe(1);
  });
  it("shows normal saved review and visual-origin observations with their snapshot time", async () => {
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=overview");
    render(<Projects />);
    const status = await screen.findByRole("region", { name: "Snapshot status" });
    expect(status).toHaveTextContent("Published source · loaded revision");
    expect(status).toHaveTextContent("Foundation cccccccccccc");
    expect(status).toHaveTextContent("Observations saved 2026-10-15T12:00:00Z");
    expect(status).toHaveTextContent("Landing reviewCurrent against reviewed source (match)");
    expect(status).toHaveTextContent("Roadmap reviewCurrent against reviewed source (match)");
    expect(status).toHaveTextContent("Visual originMatches saved source observations");
    expect(status).toHaveTextContent("extracted from the published Foundation revision shown above");
    expect(within(status).queryByText(/TEACH:/)).not.toBeInTheDocument();
  });
  it("warns for unverifiable review and mismatched visual origin", async () => {
    const saved = detail("belluga", "builder");
    saved.snapshot_observations = {
      knowledge_status: { landing: { status: "unverifiable", reason_code: "review_missing" }, roadmap: { status: "unverifiable", reason_code: "review_missing" } },
      visual_origin: { state: "mismatch", knowledge_source_state: "matching", roadmap_source_state: "mismatch", design_system_state: "matching" },
    };
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), location.origin);
      if (url.pathname.startsWith("/snapshot/")) return { ok: true, headers: new Headers({ "X-Builder-Snapshot-Fingerprint": fingerprint }), json: async () => theme };
      return { ok: true, json: async () => url.searchParams.has("project_id") ? saved : { companies, projects } };
    }));
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=overview");
    render(<Projects />);
    const status = await screen.findByRole("region", { name: "Snapshot status" });
    expect(status).toHaveTextContent("Landing reviewUnverifiable (review missing)");
    expect(status).toHaveTextContent("Roadmap reviewUnverifiable (review missing)");
    expect(status).toHaveTextContent("Visual originMismatch");
    expect(within(status).getByRole("status")).toHaveTextContent("published Foundation branch through Delphi");
    expect(within(status).getByRole("status")).toHaveTextContent("publish the change");
  });
  it("warns when a saved source change requires review", async () => {
    const saved = detail("belluga", "builder");
    saved.snapshot_observations.knowledge_status.landing = { status: "review_required", reason_code: "source_changed" };
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), location.origin);
      if (url.pathname.startsWith("/snapshot/")) return { ok: true, headers: new Headers({ "X-Builder-Snapshot-Fingerprint": fingerprint }), json: async () => theme };
      return { ok: true, json: async () => url.searchParams.has("project_id") ? saved : { companies, projects } };
    }));
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=overview");
    render(<Projects />);
    const status = await screen.findByRole("region", { name: "Snapshot status" });
    expect(status).toHaveTextContent("Landing reviewReview required (source changed)");
    expect(within(status).getByRole("status")).toHaveTextContent("published Foundation branch through Delphi");
  });
  it("renders missing observations as unavailable while retaining their valid saved time", async () => {
    const saved = detail("belluga", "builder");
    saved.snapshot_observations = undefined as unknown as typeof saved.snapshot_observations;
    saved.snapshot_observed_at = "2026-10-15T12:34:56.123456789Z";
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), location.origin);
      if (url.pathname.startsWith("/snapshot/")) return { ok: true, headers: new Headers({ "X-Builder-Snapshot-Fingerprint": fingerprint }), json: async () => theme };
      return { ok: true, json: async () => url.searchParams.has("project_id") ? saved : { companies, projects } };
    }));
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=overview");
    render(<Projects />);
    const status = await screen.findByRole("region", { name: "Snapshot status" });
    expect(status).toHaveTextContent("Observations saved 2026-10-15T12:34:56.123456789Z");
    expect(status).toHaveTextContent("Landing reviewUnavailable");
    expect(status).toHaveTextContent("Roadmap reviewUnavailable");
    expect(status).toHaveTextContent("Visual originUnavailable");
    expect(within(status).getByRole("status")).toBeInTheDocument();
  });
  it.each(["1", "2026-02-30T12:00:00Z"])("does not trust otherwise current statuses with malformed parseable timestamp %s", async (timestamp) => {
    const saved = detail("belluga", "builder");
    saved.snapshot_observed_at = timestamp;
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), location.origin);
      if (url.pathname.startsWith("/snapshot/")) return { ok: true, headers: new Headers({ "X-Builder-Snapshot-Fingerprint": fingerprint }), json: async () => theme };
      return { ok: true, json: async () => url.searchParams.has("project_id") ? saved : { companies, projects } };
    }));
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=overview");
    render(<Projects />);
    const status = await screen.findByRole("region", { name: "Snapshot status" });
    expect(status).toHaveTextContent("Observation time unavailable");
    expect(status).toHaveTextContent("Landing reviewUnavailable");
    expect(status).toHaveTextContent("Roadmap reviewUnavailable");
    expect(status).toHaveTextContent("Visual originUnavailable");
    expect(within(status).getByRole("status")).toBeInTheDocument();
  });
  it("opens the same admitted TODO from Release and renders only authored sections", async () => {
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=release&epic_id=e-01&release_id=r-04");
    render(<Projects />);
    const invokingTODO = await screen.findByRole("button", { name: /Project Roadmap connection/ });
    fireEvent.click(invokingTODO);
    expect(location.search).toContain("todo_id=roadmap-runtime");
    const dialog = await screen.findByRole("dialog", { name: "Prévia do TODO" });
    const closePreview = within(dialog).getByRole("button", { name: "Fechar prévia" });
    const openFull = within(dialog).getByRole("button", { name: /Abrir TODO completo/ });
    expect(closePreview).toHaveFocus();
    fireEvent.keyDown(document, { key: "Tab", shiftKey: true });
    expect(openFull).toHaveFocus();
    fireEvent.keyDown(document, { key: "Tab" });
    expect(closePreview).toHaveFocus();
    invokingTODO.focus();
    expect(closePreview).toHaveFocus();
    fireEvent.keyDown(document, { key: "Escape" });
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "Prévia do TODO" })).not.toBeInTheDocument());
    expect(invokingTODO).toHaveFocus();
    fireEvent.click(invokingTODO);
    expect(await screen.findByText("Exact objective from Foundation.")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /Abrir TODO completo/ }));
    expect(await screen.findByText("Exact authored step.")).toBeInTheDocument();
    expect(document.querySelector(".todo-work")).toHaveTextContent("Exact acceptance criterion.");
    expect(screen.getByText("Review the source.")).toBeInTheDocument();
    expect(screen.queryByText("Nenhum bloqueio declarado no arquivo.")).not.toBeInTheDocument();
    fireEvent.click(screen.getByText(/Contrato completo/));
    expect(document.querySelector(".todo-source pre")?.textContent).toBe(todoMarkdown);
  });
  it("rejects a TODO ID outside its Release before requesting source", async () => {
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=todo&epic_id=e-01&release_id=r-01&todo_id=backlog%3Atodos%2Factive%2Fevil.md&path=todos%2Factive%2Fbacklog-item.md");
    render(<Projects />);
    expect(await screen.findByRole("alert")).toHaveTextContent("TODO indisponível");
    expect(screen.queryByText("Exact objective from Foundation.")).not.toBeInTheDocument();
    expect(vi.mocked(fetch).mock.calls.some(([input]) => String(input).includes("backlog-item.md"))).toBe(false);
  });
  it("omits absent authored sections on a direct TODO link", async () => {
    mockFetch([], undefined, "# Project Roadmap connection\n\n## Objective\nOnly authored objective.\n");
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=todo&todo_id=roadmap-runtime");
    render(<Projects />);
    expect(await screen.findByText("Only authored objective.")).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Scope" })).not.toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Definition of Done" })).not.toBeInTheDocument();
    expect(document.querySelector(".todo-source pre")?.textContent).toBe("# Project Roadmap connection\n\n## Objective\nOnly authored objective.\n");
  });
  it("opens a path-bound Backlog TODO by keyboard, ignores a stale rapid selection, and opens its full page", async () => {
    vi.stubGlobal("matchMedia", vi.fn(() => ({ matches: true, media: "(max-width: 1200px)", onchange: null, addListener: vi.fn(), removeListener: vi.fn(), addEventListener: vi.fn(), removeEventListener: vi.fn(), dispatchEvent: vi.fn() })));
    HTMLElement.prototype.scrollIntoView = vi.fn();
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=roadmap");
    const pending = new Map<string, (response: unknown) => void>();
    const fetchSpy = vi.fn((input: RequestInfo | URL) => {
      const url = new URL(String(input), location.origin);
      if (url.pathname === "/api/local-preview" && !url.searchParams.has("project_id")) return Promise.resolve({ ok: true, json: async () => ({ companies, projects }) });
      if (url.pathname === "/api/local-preview") return Promise.resolve({ ok: true, json: async () => detail("belluga", "builder") });
      if (url.pathname.endsWith("/design/system/design-system.json")) return Promise.resolve({ ok: true, headers: new Headers({ "X-Builder-Snapshot-Fingerprint": fingerprint }), json: async () => theme });
      return new Promise((resolve) => pending.set(url.pathname, resolve));
    });
    vi.stubGlobal("fetch", fetchSpy);
    const user = userEvent.setup();
    render(<Projects />);
    const first = await screen.findByRole("button", { name: /Unassigned active task/ });
    first.focus();
    await user.keyboard("{Enter}");
    fireEvent.click(screen.getByRole("button", { name: /Second unassigned task/ }));
    const firstPath = `/snapshot/belluga/builder/${fingerprint}/todos/active/backlog-item.md`;
    const secondPath = `/snapshot/belluga/builder/${fingerprint}/todos/active/backlog-item-two.md`;
    await waitFor(() => expect(pending.has(firstPath) && pending.has(secondPath)).toBe(true));
    const response = (title: string, objective: string, nextStep: string) => ({ ok: true, headers: new Headers({ "X-Builder-Snapshot-Fingerprint": fingerprint }), text: async () => `# ${title}\n\n## Objective\n${objective}\n\n## Delivery Status Canon\n- **Next exact step:** ${nextStep}\n\n## Scope\nFull authored scope for ${title}.\n` });
    await act(async () => { pending.get(secondPath)?.(response("Second unassigned task", "Current backlog objective", "Current authored next step.")); });
    expect(await screen.findByText("Current backlog objective")).toBeInTheDocument();
    expect(screen.getByLabelText("Prévia do TODO")).toHaveFocus();
    expect(HTMLElement.prototype.scrollIntoView).toHaveBeenCalledWith({ behavior: "smooth", block: "start" });
    expect(screen.getByText("Current authored next step.")).toBeInTheDocument();
    expect(location.search).toContain("page=roadmap");
    expect(location.search).toContain("backlog%3Atodos%2Factive%2Fbacklog-item-two.md");
    await act(async () => { pending.get(firstPath)?.(response("Unassigned active task", "Stale objective", "Stale authored next step.")); });
    expect(screen.queryByText("Stale objective")).not.toBeInTheDocument();
    expect(screen.queryByText("Stale authored next step.")).not.toBeInTheDocument();
    expect(screen.getByText("Current authored next step.")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /Abrir TODO completo/ }));
    await waitFor(() => expect(fetchSpy.mock.calls.filter(([input]) => String(input).includes("todos/active/backlog-item-two.md"))).toHaveLength(2));
    await act(async () => { pending.get(secondPath)?.(response("Second unassigned task", "Current backlog objective", "Current authored next step.")); });
    expect(await screen.findByText("Current backlog objective")).toBeInTheDocument();
    expect(location.search).toContain("backlog%3Atodos%2Factive%2Fbacklog-item-two.md");
    expect(location.search).toContain("page=todo");
    expect(screen.getByText("Current authored next step.")).toBeInTheDocument();
    expect(document.querySelector(".todo-source pre")?.textContent).toBe("# Second unassigned task\n\n## Objective\nCurrent backlog objective\n\n## Delivery Status Canon\n- **Next exact step:** Current authored next step.\n\n## Scope\nFull authored scope for Second unassigned task.\n");
    expect(fetchSpy.mock.calls.some(([input]) => String(input).includes("todos/active/backlog-item-two.md"))).toBe(true);
  });
  it("opens nested Backlog folders by keyboard and previews the authored TODO", async () => {
    vi.stubGlobal("matchMedia", vi.fn(() => ({ matches: true, media: "(max-width: 1200px)", onchange: null, addListener: vi.fn(), removeListener: vi.fn(), addEventListener: vi.fn(), removeEventListener: vi.fn(), dispatchEvent: vi.fn() })));
    const saved = detail("belluga", "builder");
    const nestedPath = "todos/active/platform/runtime/cache.md";
    saved.roadmap.backlog.todos = [];
    saved.roadmap.backlog.folders = [{
      name: "Platform", path: "todos/active/platform", description: "README: platform-wide authored guidance.", todos: [], folders: [{
        name: "Runtime", path: "todos/active/platform/runtime", description: "README: runtime-specific authored guidance.",
        todos: [{ id: `backlog:${nestedPath}`, title: "Cache runtime task", path: nestedPath, kind: "executable" as const, record_state: "active", local_stage: "Pending" as const }], folders: [],
      }],
    }] as unknown as typeof saved.roadmap.backlog.folders;
    const roadmapArtifact = saved.artifacts.find((artifact) => artifact.kind === "roadmap")!;
    roadmapArtifact.files = [...(roadmapArtifact.files || []), "todos/active/platform/README.md", "todos/active/platform/runtime/README.md", nestedPath];
    mockProjectDetail(saved, "# Cache runtime task\n\n## Objective\nAuthored nested objective.\n");
    const user = userEvent.setup();
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=roadmap");
    render(<Projects />);
    await screen.findByRole("heading", { name: "Roadmap" });

    const rootDetails = screen.getByText("Platform").closest("details")!;
    const rootSummary = within(rootDetails).getByText("Platform").closest("summary")!;
    rootSummary.focus();
    expect(rootSummary).toHaveFocus();
    await user.keyboard("{Enter}");
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
    expect(await screen.findByText("Authored nested objective.")).toBeInTheDocument();
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
    expect(screen.getByRole("button", { name: /Unassigned active task/ })).toBeInTheDocument();
    expect(screen.getByRole("group", { name: "Releases for selected Epic" })).not.toHaveTextContent("Unassigned active task");
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
  it("shows truthful delivered and invalid Release states without planning-ledger arithmetic", async () => {
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=release&epic_id=e-01&release_id=r-01");
    render(<Projects />);
    expect(await screen.findByRole("heading", { name: "First local viewer", level: 1 })).toBeInTheDocument();
    expect(document.querySelector(".release-badge")).toHaveTextContent("Entregue");
    expect(screen.getByRole("region", { name: "TODOs executáveis" })).not.toHaveTextContent("Builder genesis ledger");
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=release&epic_id=e-02&release_id=r-01");
    window.dispatchEvent(new PopStateEvent("popstate"));
    expect(await screen.findByRole("alert")).toHaveTextContent("Release indisponível");
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
  it("keeps Roadmap and Documents navigable when the Design System is invalid", async () => {
    const saved = detail("belluga", "builder", [{ path: "project_mandate.md", url: `/snapshot/belluga/builder/${fingerprint}/project_mandate.md` }]);
    saved.artifacts[0] = { ...saved.artifacts[0], state: "invalid" };
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), location.origin);
      return { ok: true, json: async () => url.searchParams.has("project_id") ? saved : { companies, projects } };
    }));
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=roadmap");
    render(<Projects />);
    expect(await screen.findByRole("heading", { name: "Roadmap" })).toBeInTheDocument();
    expect(screen.getByRole("alert")).toHaveTextContent("Design System");
    fireEvent.click(screen.getByRole("button", { name: "Documents / decisions" }));
    expect(await screen.findByRole("heading", { name: "Documents & decisions" })).toBeInTheDocument();
  });
  it("keeps the approved Overview narrative primary and links the Landing separately", async () => {
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=overview");
    render(<Projects />);
    expect(await screen.findByRole("heading", { name: "The problem" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "The proposal" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "The impact" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "For whom" })).toBeInTheDocument();

    const narrative = document.querySelector(".overview-editorial");
    expect(narrative).toBeInTheDocument();
    expect(document.querySelector(".overview-status")).toBeInTheDocument();
    expect(screen.queryByText(/Saved snapshot:/)).not.toBeInTheDocument();
    expect(screen.getByRole("link", { name: /Ver landing independente/ })).toHaveAttribute("target", "_blank");
    expect(screen.queryByRole("region", { name: "Executive Landing artifact" })).not.toBeInTheDocument();
    expect(screen.queryByText("MVP")).not.toBeInTheDocument();
    const hero = document.querySelector(".project-hero");
    expect(hero?.querySelector("h1")).toHaveTextContent("Builder");
    expect(hero?.querySelector(".overview-hero-tagline")).toHaveTextContent("Product direction");
    expect(screen.getByAltText("Desktop Project view")).toBeInTheDocument();
    expect(screen.getByAltText("Mobile Project view")).toBeInTheDocument();
    expect(screen.getByText("CONCEITO · DADOS ILUSTRATIVOS")).toBeInTheDocument();
    expect(screen.getByAltText("Admitted Builder document view")).toHaveAttribute("src", `/snapshot/belluga/builder/${fingerprint}/project_overview/proposal.png`);
  });
  it("keeps Overview editorial separate from an empty Documents state", async () => {
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=overview");
    render(<Projects />);
    expect(await screen.findByText("Problem evidence")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Documents / decisions" }));
    expect(await screen.findByText("No canonical Markdown documents are admitted in this snapshot.")).toBeInTheDocument();
  });
  it("does not substitute portfolio text for a missing admitted Overview", async () => {
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=overview");
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), location.origin);
      if (url.pathname.startsWith("/snapshot/")) return { ok: true, headers: new Headers({ "X-Builder-Snapshot-Fingerprint": fingerprint }), json: async () => theme };
      if (url.searchParams.has("project_id")) { const result = detail("belluga", "builder"); return { ok: true, json: async () => ({ ...result, artifacts: [...result.artifacts, { id: "overview", kind: "overview", name: "Project Overview", state: "pending", diagnostic: "Project Overview is not available in the published Foundation snapshot. TEACH: request targeted Overview status." }], overview: undefined }) }; }
      return { ok: true, json: async () => ({ companies, projects }) };
    }));
    render(<Projects />);
    expect(await screen.findByRole("heading", { name: "Project Overview not available yet" })).toBeInTheDocument();
    expect(screen.queryByText("Summary for builder")).not.toBeInTheDocument();
    expect(screen.getByText(/TEACH: request targeted Overview status/)).toBeInTheDocument();
    const landingLink = screen.getByRole("link", { name: /Ver landing independente/ });
    expect(landingLink).toHaveAttribute("href", `/snapshot/belluga/builder/${fingerprint}/design/landing/index.html`);
    expect(landingLink).toHaveAttribute("target", "_blank");
  });
  it("labels missing hero media against the loaded Overview, not a continuously current source", async () => {
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=overview");
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), location.origin);
      if (url.pathname.startsWith("/snapshot/")) return { ok: true, headers: new Headers({ "X-Builder-Snapshot-Fingerprint": fingerprint }), json: async () => theme };
      if (url.searchParams.has("project_id")) { const result = detail("belluga", "builder"); return { ok: true, json: async () => ({ ...result, overview: { ...result.overview, media: {} } }) }; }
      return { ok: true, json: async () => ({ companies, projects }) };
    }));
    render(<Projects />);
    expect(await screen.findByRole("alert", { name: "" })).toHaveTextContent("loaded Overview");
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
    expect(within(readyTodo!).getByText("Production-Ready")).toHaveClass("done");
    fireEvent.click(screen.getByRole("button", { name: /Project UI and real Roadmap/ }));
    const localTodo = screen.getByText(/Project Roadmap connection/).closest("tr");
    expect(localTodo).not.toBeNull();
    expect(within(localTodo!).getByText("Blocked · Local-Implemented")).toHaveClass("blocked");
    const promotedTodo = screen.getByText(/Promotion pending task/).closest("tr");
    expect(within(promotedTodo!).getByText("Lane-Promoted")).toHaveClass("validation");
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
    { name: "planned", todos: [{ kind: "executable", local_stage: "Pending" }], expected: "Planejada" },
    { name: "mixed", todos: [{ kind: "executable", local_stage: "Production-Ready" }, { kind: "executable", local_stage: "Pending" }], expected: "Em andamento" },
    { name: "unknown", todos: [{ kind: "executable", local_stage: "unknown" }, { kind: "executable", local_stage: "Production-Ready" }], expected: "Status indisponível" },
    { name: "planning-only", todos: [{ kind: "planning", local_stage: "Production-Ready" }], expected: "Status indisponível" },
    { name: "blocked ready", todos: [{ kind: "executable", local_stage: "Production-Ready", work_state: "blocked" }], expected: "Bloqueada" },
  ])("derives $name Release status without a second delivery field", async ({ todos, expected }) => {
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), location.origin);
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
    expect(release).not.toHaveTextContent("Entregue");
    expect(screen.queryByRole("columnheader", { name: "ENTREGA" })).not.toBeInTheDocument();
  });
  it("updates the derived Release status only after a manual page reload", async () => {
    let detailReads = 0;
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), location.origin);
      if (url.pathname.startsWith("/snapshot/")) return { ok: true, headers: new Headers({ "X-Builder-Snapshot-Fingerprint": detailReads > 1 ? "b".repeat(64) : fingerprint }), json: async () => theme };
      if (!url.searchParams.has("project_id")) return { ok: true, json: async () => ({ companies, projects }) };
      detailReads++;
      const saved = detail("belluga", "builder");
      saved.snapshot_fingerprint = detailReads > 1 ? "b".repeat(64) : fingerprint;
      saved.project_sha = (detailReads > 1 ? "d" : "c").repeat(40);
      const first = saved.roadmap.epics[0].releases[0];
      first.todos = [{ id: "work", title: "Published work", path: "todos/active/work.md", kind: "executable", record_state: "active", local_stage: detailReads > 1 ? "Local-Implemented" : "Pending" }];
      return { ok: true, json: async () => saved };
    }));
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=roadmap");
    const mounted = render(<Projects />);
    expect(await screen.findByRole("button", { name: /First local viewer/ })).toHaveTextContent("Planejada");
    fireEvent(document, new Event("visibilitychange"));
    expect(detailReads).toBe(1);
    expect(screen.getByRole("button", { name: /First local viewer/ })).toHaveTextContent("Planejada");
    mounted.unmount();
    render(<Projects />);
    await waitFor(() => expect(screen.getByRole("button", { name: /First local viewer/ })).toHaveTextContent("Em andamento"));
    expect(detailReads).toBe(2);
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
      if (url.pathname.startsWith("/snapshot/")) return { ok: true, headers: new Headers({ "X-Builder-Snapshot-Fingerprint": fingerprint }), json: async () => theme };
      if (!url.searchParams.has("project_id")) return { ok: true, json: async () => ({ companies, projects }) };
      const saved = detail("belluga", "builder");
      return { ok: true, json: async () => ({ ...saved, roadmap: undefined, artifacts: saved.artifacts.map((item) => item.kind === "roadmap" ? { ...item, state: "pending", diagnostic: "Roadmap catalog is not available yet. Register after adding the catalog." } : item) }) };
    }));
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=roadmap");
    render(<Projects />);
    expect(await screen.findByText(/Roadmap catalog is not available yet/)).toBeInTheDocument();
    expect(screen.queryByText(/Local projects/)).not.toBeInTheDocument();
    expect(screen.queryByText(/Project UI and real Roadmap/)).not.toBeInTheDocument();
  });
  it("keeps the Overview honest when its Roadmap catalog is absent", async () => {
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), location.origin);
      if (url.pathname.startsWith("/snapshot/")) return { ok: true, headers: new Headers({ "X-Builder-Snapshot-Fingerprint": fingerprint }), json: async () => theme };
      if (!url.searchParams.has("project_id")) return { ok: true, json: async () => ({ companies, projects }) };
      const saved = detail("belluga", "builder");
      return { ok: true, json: async () => ({ ...saved, roadmap: undefined, artifacts: saved.artifacts.map((item) => item.kind === "roadmap" ? { ...item, state: "pending", diagnostic: "Roadmap catalog is not available yet. Register after adding the catalog." } : item) }) };
    }));
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=overview");
    render(<Projects />);
    await waitFor(() => expect(screen.getByRole("status")).toHaveTextContent("Roadmap catalog is not available yet"));
    const notice = screen.getByRole("status");
    expect(notice).toHaveClass("project-notice");
    expect(screen.queryByText(/Local projects|Project UI and real Roadmap/)).not.toBeInTheDocument();
    expect(screen.queryByText(/demo|delivered|shipped/i)).not.toBeInTheDocument();
  });
  it("resets selected Epic and Project-bound Roadmap data when the Project changes", async () => {
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), location.origin);
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
    expect(await screen.findByText("Roadmap catalog is not available for this Project.")).toBeInTheDocument();
    expect(screen.queryByRole("group", { name: "Select Epic" })).not.toBeInTheDocument();
    expect(screen.queryByText(/builder Local projects|builder Internal service|Company and Project Schema Study|Project UI and real Roadmap|Project Roadmap connection/)).not.toBeInTheDocument();
  });
  it("shows the honest empty Documents state without an empty index column", async () => {
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=documents");
    render(<Projects />);
    expect(await screen.findByText("No canonical Markdown documents are admitted in this snapshot.")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Reading is not review" })).toBeInTheDocument();
    expect(document.querySelector(".documents-layout")).toBeNull();
  });
  it("shows readable labels while retaining the admitted document path", async () => {
    mockFetch([{ path: "modules/project_knowledge_viewer_module.md", url: "/snapshot/example/modules/project_knowledge_viewer_module.md" }]);
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=documents");
    render(<Projects />);
    expect(await screen.findByRole("heading", { name: "Project knowledge viewer module" })).toBeInTheDocument();
    expect(screen.getByText("modules/project_knowledge_viewer_module.md")).toBeInTheDocument();
  });
  it("reads admitted Markdown with headings", async () => {
    const documentURL = "/snapshot/example/modules/project_knowledge_viewer_module.md";
    const admitted = [{ path: "modules/project_knowledge_viewer_module.md", url: documentURL }];
    mockFetch(admitted, { ok: true, body: "# Project knowledge\n\n## Decisions\n\nCurrent direction." });
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=documents");
    render(<Projects />);
    fireEvent.click(await screen.findByRole("button", { name: /Read document/ }));
    expect(await screen.findByRole("heading", { name: "Decisions" })).toBeInTheDocument();
    expect(screen.getByRole("navigation", { name: "Document headings" })).toHaveTextContent("Decisions");
  });
  it("replaces an open document only after a manual page reload reads the new Foundation revision", async () => {
    const nextFingerprint = "b".repeat(64);
    const path = "modules/project_knowledge_viewer_module.md";
    const firstURL = `/snapshot/belluga/builder/${fingerprint}/${path}`;
    const nextURL = `/snapshot/belluga/builder/${nextFingerprint}/${path}`;
    let detailReads = 0;
    let resolveNextDocument!: (value: unknown) => void;
    const nextDocument = new Promise<unknown>((resolve) => { resolveNextDocument = resolve; });
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), location.origin);
      if (String(input) === firstURL) return { ok: true, headers: new Headers({ "X-Builder-Snapshot-Fingerprint": fingerprint }), text: async () => "# Old Foundation content" };
      if (String(input) === nextURL) return nextDocument as Promise<never>;
      if (url.pathname.startsWith("/snapshot/")) return { ok: true, headers: new Headers({ "X-Builder-Snapshot-Fingerprint": detailReads > 1 ? nextFingerprint : fingerprint }), json: async () => theme };
      if (url.searchParams.has("project_id")) {
        detailReads++;
        const result = detail("belluga", "builder", [{ path, url: detailReads > 1 ? nextURL : firstURL }]);
        result.snapshot_fingerprint = detailReads > 1 ? nextFingerprint : fingerprint;
        return { ok: true, json: async () => result };
      }
      return { ok: true, json: async () => ({ companies, projects }) };
    }));
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=documents");
    const mounted = render(<Projects />);
    fireEvent.click(await screen.findByRole("button", { name: /Read document/ }));
    expect(await screen.findByRole("heading", { name: "Old Foundation content" })).toBeInTheDocument();
    fireEvent(document, new Event("visibilitychange"));
    expect(detailReads).toBe(1);
    expect(screen.getByRole("heading", { name: "Old Foundation content" })).toBeInTheDocument();
    mounted.unmount();
    render(<Projects />);
    await waitFor(() => expect(detailReads).toBe(2));
    expect(screen.queryByRole("heading", { name: "Old Foundation content" })).not.toBeInTheDocument();
    fireEvent.click(await screen.findByRole("button", { name: /Read document/ }));
    expect(screen.getByRole("status")).toHaveTextContent("Loading document");
    await act(async () => resolveNextDocument({ ok: true, headers: new Headers({ "X-Builder-Snapshot-Fingerprint": nextFingerprint }), text: async () => "# New Foundation content" }));
    expect(await screen.findByRole("heading", { name: "New Foundation content" })).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Old Foundation content" })).not.toBeInTheDocument();
  });
  it("reports a failed admitted document fetch without inventing content", async () => {
    const documentURL = "/snapshot/example/modules/missing.md";
    mockFetch([{ path: "modules/missing.md", url: documentURL }]);
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=documents");
    render(<Projects />);
    fireEvent.click(await screen.findByRole("button", { name: /Read document/ }));
    expect(await screen.findByRole("alert")).toHaveTextContent("could not be loaded");
    expect(screen.queryByText("Current direction.")).not.toBeInTheDocument();
  });
  it("rejects document bytes tagged with a different snapshot fingerprint", async () => {
    const documentURL = "/snapshot/example/modules/mismatched.md";
    mockFetch([{ path: "modules/mismatched.md", url: documentURL }], { ok: true, body: "# Wrong revision", fingerprint: "b".repeat(64) });
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=documents");
    render(<Projects />);
    fireEvent.click(await screen.findByRole("button", { name: /Read document/ }));
    expect(await screen.findByRole("alert")).toHaveTextContent("could not be loaded");
    expect(screen.queryByRole("heading", { name: "Wrong revision" })).not.toBeInTheDocument();
  });
  it("teaches fresh registration when the private reader rejects an old path-only binding", async () => {
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), location.origin);
      if (url.searchParams.has("project_id")) return { ok: false, status: 503, json: async () => ({ company_id: "belluga", project_id: "builder", diagnostic: "TEACH: This Project has an invalid path-only registration." }) };
      return { ok: true, json: async () => ({ companies, projects }) };
    }));
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=overview");
    render(<Projects />);
    expect(await screen.findByRole("alert")).toHaveTextContent("registre o Project novamente");
    expect(screen.getByRole("alert")).toHaveTextContent("Não há migração");
  });
  it("ignores a late Project detail after switching to another Project", async () => {
    let releaseBuilder!: (value: unknown) => void;
    const lateBuilder = new Promise<unknown>((resolve) => { releaseBuilder = resolve; });
    const booraFingerprint = "b".repeat(64);
    const booraProjects = projects.map((item) => item.id === "boora" ? { ...item, snapshot_fingerprint: booraFingerprint } : item);
    const booraTheme = { tokens: theme.tokens.map((item) => item.id === "color-accent" ? { ...item, value: "#cc99ee" } : item) };
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), location.origin);
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
    const status = screen.getByRole("region", { name: "Snapshot status" });
    expect(status).toHaveTextContent("Landing reviewReview required (source changed)");
    expect(status).toHaveTextContent("Observations saved 2026-10-16T12:00:00Z");
    await act(async () => { releaseBuilder({ ok: true, json: async () => detail("belluga", "builder") }); });
    expect(screen.getByText("Overview for boora")).toBeInTheDocument();
    expect(screen.queryByText("Overview for builder")).not.toBeInTheDocument();
    const visibleStatus = screen.getByRole("region", { name: "Snapshot status" });
    expect(visibleStatus).toHaveTextContent("Landing reviewReview required (source changed)");
    expect(visibleStatus).toHaveTextContent("Observations saved 2026-10-16T12:00:00Z");
    expect(visibleStatus).not.toHaveTextContent("Landing reviewCurrent against reviewed source");
    expect((document.querySelector(".projects-project") as HTMLElement).style.getPropertyValue("--project-accent")).toBe("#cc99ee");
  });
  it("replaces Prototype evidence and presentation identity when switching Projects", async () => {
    const booraFingerprint = "b".repeat(64);
    const registered = projects.map((item) => item.id === "boora" ? { ...item, snapshot_fingerprint: booraFingerprint } : item);
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), location.origin);
      if (url.pathname.startsWith("/snapshot/")) {
        const boora = url.pathname.includes("/boora/");
        return { ok: true, headers: new Headers({ "X-Builder-Snapshot-Fingerprint": boora ? booraFingerprint : fingerprint }), json: async () => theme };
      }
      if (!url.searchParams.has("project_id")) return { ok: true, json: async () => ({ companies, projects: registered }) };
      const id = url.searchParams.get("project_id")!;
      const saved = detail("belluga", id);
      const prototype = { id: `${id}-prototype`, name: `${id} Prototype`, root: `prototypes/${id}`, status: "active", authoring_mode: "design_system_first", entry_point: "home.html",
        screens: [{ id: `${id}-screen`, name: `${id} Screen`, path: "home.html", default_state_id: "default", states: [{ id: "default", name: "Default", identifier_image: "home.png", approved_references: [] }] }],
        scenarios: [], transitions: [] };
      return { ok: true, json: async () => ({ ...saved, snapshot_fingerprint: id === "boora" ? booraFingerprint : fingerprint,
        artifacts: [...saved.artifacts, { id: prototype.id, kind: "prototype", name: prototype.name, state: "available", entry_point: `${prototype.root}/home.html` }],
        snapshot_observations: { prototype_status: { outcome: "go", items: [prototype] } } }) };
    }));
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=prototypes");
    render(<Projects />);
    expect(await screen.findByRole("heading", { name: "builder Screen / Default" })).toBeInTheDocument();
    expect(screen.getAllByLabelText("Prototype inspector")).toHaveLength(1);
    fireEvent.change(screen.getByLabelText("Switch Project"), { target: { value: "boora" } });
    fireEvent.click(within(await screen.findByRole("navigation", { name: "Project pages" })).getByRole("button", { name: "Prototype" }));
    expect(await screen.findByRole("heading", { name: "boora Screen / Default" })).toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "builder Screen / Default" })).not.toBeInTheDocument();
    expect(screen.getAllByLabelText("Prototype inspector")).toHaveLength(1);
    expect(screen.getByRole("link", { name: /Abrir apresentação/ }).getAttribute("href")).toContain(booraFingerprint);
  });
  it("renders the admitted Design System roles without nesting its full catalog", async () => {
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder&page=design-system");
    render(<Projects />);
    expect(await screen.findByRole("heading", { name: "Paleta estrutural" })).toBeInTheDocument();
    expect(screen.getByText("#101820")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Hierarquia" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Abrir Design System ↗" })).toHaveAttribute("target", "_blank");
    expect(document.querySelector(".design-system-view iframe")).toBeNull();
  });
  it("does not render a Project with a mismatched saved identity", async () => {
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => ({ ok: true, json: async () => String(input).includes("project_id=") ? detail("second", "builder") : { companies, projects } })));
    history.replaceState({}, "", "/?company_id=belluga&project_id=builder");
    render(<Projects />);
    expect(await screen.findByRole("alert")).toHaveTextContent(/Não foi possível ler os dados publicados/);
    expect(screen.queryByRole("navigation", { name: "Project pages" })).not.toBeInTheDocument();
  });
  it("uses the live reader fingerprint without comparing it to the identity catalog", async () => {
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
      const url = new URL(String(input), location.origin);
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
