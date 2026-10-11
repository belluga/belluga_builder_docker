import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Projects } from "./projects";

const fingerprint = "a".repeat(64);
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
function detail(companyID: string, projectID: string, documents: { path: string; url: string }[] = []) { return { consumer_readiness: "ready", company_id: companyID, project_id: projectID, snapshot_fingerprint: fingerprint,
  artifacts: [{ id: "system", kind: "design_system", name: "Design System", state: "available", entry_point: "design/system/examples.html", files: ["design/system/design-system.json"] },
    { id: "landing", kind: "landing", name: "Landing", state: "available", entry_point: "design/landing/index.html" }],
  portfolio_summary: `Summary for ${projectID}`, documents, snapshot_observations: {} }; }
function mockFetch(documents: { path: string; url: string }[] = [], documentResponse?: { ok: boolean; body?: string }) { vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => {
  const url = new URL(String(input), location.origin);
  if (documents.some((item) => item.url === String(input))) return { ok: documentResponse?.ok ?? false, text: async () => documentResponse?.body || "" };
  if (url.pathname.startsWith("/snapshot/")) return { ok: true, headers: new Headers({ "X-Builder-Snapshot-Fingerprint": fingerprint }), json: async () => theme };
  return { ok: true, json: async () => url.searchParams.has("project_id") ? detail(url.searchParams.get("company_id")!, url.searchParams.get("project_id")!, documents) : { companies, projects } };
})); }
beforeEach(() => { history.replaceState({}, "", "/local"); mockFetch(); });
afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

describe("single /local Project experience", () => {
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
    expect(location.pathname).toBe("/local");
    expect(screen.getByRole("navigation", { name: "Project pages" })).toBeInTheDocument();
  });
  it("applies Project Design System roles to the whole Project page", async () => {
    history.replaceState({}, "", "/local?company_id=belluga&project_id=builder&page=overview");
    render(<Projects />);
    await waitFor(() => expect(document.querySelector(".projects-project")).toBeInTheDocument());
    expect((document.querySelector(".projects-project") as HTMLElement).style.getPropertyValue("--project-accent")).toBe("#32bbdd");
    expect(screen.getByText("Summary for builder")).toBeInTheDocument();
  });
  it("keeps the approved Overview narrative primary and links the Landing separately", async () => {
    history.replaceState({}, "", "/local?company_id=belluga&project_id=builder&page=overview");
    render(<Projects />);
    expect(await screen.findByRole("heading", { name: "Por que existe" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "O caminho" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Artefatos visíveis" })).toBeInTheDocument();

    const narrative = document.querySelector(".overview-cards");
    const status = document.querySelector(".overview-status");
    expect(narrative).toBeInTheDocument();
    expect(status).toBeInTheDocument();
    expect(Boolean(narrative!.compareDocumentPosition(status!) & Node.DOCUMENT_POSITION_FOLLOWING)).toBe(true);
    expect(screen.getByRole("link", { name: "Open Landing ↗" })).toHaveAttribute("target", "_blank");
    expect(document.querySelector(".project-landing-reference iframe")).toBeNull();
    expect(screen.queryByText("MVP")).not.toBeInTheDocument();
    const hero = document.querySelector(".project-hero");
    expect(hero?.querySelector("h1")).toHaveTextContent("Builder");
    expect(hero?.querySelector("aside")).toHaveTextContent("Project identity");
  });
  it("keeps empty-document Overview copy honest and its CTA opens the empty Documents state", async () => {
    history.replaceState({}, "", "/local?company_id=belluga&project_id=builder&page=overview");
    render(<Projects />);
    expect(await screen.findByText(/No admitted documents are available to substantiate/)).toBeInTheDocument();
    expect(screen.getByText(/There are no admitted documents to show recorded decisions or planning/)).toBeInTheDocument();
    expect(screen.queryByText("Use the admitted Project documents to review its purpose and operating context.")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /View Documents/ }));
    expect(await screen.findByText("No canonical Markdown documents are admitted in this snapshot.")).toBeInTheDocument();
  });
  it("shows the honest empty Documents state without an empty index column", async () => {
    history.replaceState({}, "", "/local?company_id=belluga&project_id=builder&page=documents");
    render(<Projects />);
    expect(await screen.findByText("No canonical Markdown documents are admitted in this snapshot.")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Reading is not review" })).toBeInTheDocument();
    expect(document.querySelector(".documents-layout")).toBeNull();
  });
  it("shows readable labels while retaining the admitted document path", async () => {
    mockFetch([{ path: "modules/project_knowledge_viewer_module.md", url: "/snapshot/example/modules/project_knowledge_viewer_module.md" }]);
    history.replaceState({}, "", "/local?company_id=belluga&project_id=builder&page=documents");
    render(<Projects />);
    expect(await screen.findByRole("heading", { name: "Project knowledge viewer module" })).toBeInTheDocument();
    expect(screen.getByText("modules/project_knowledge_viewer_module.md")).toBeInTheDocument();
  });
  it("reads admitted Markdown with headings", async () => {
    const documentURL = "/snapshot/example/modules/project_knowledge_viewer_module.md";
    const admitted = [{ path: "modules/project_knowledge_viewer_module.md", url: documentURL }];
    mockFetch(admitted, { ok: true, body: "# Project knowledge\n\n## Decisions\n\nCurrent direction." });
    history.replaceState({}, "", "/local?company_id=belluga&project_id=builder&page=documents");
    render(<Projects />);
    fireEvent.click(await screen.findByRole("button", { name: /Read document/ }));
    expect(await screen.findByRole("heading", { name: "Decisions" })).toBeInTheDocument();
    expect(screen.getByRole("navigation", { name: "Document headings" })).toHaveTextContent("Decisions");
  });
  it("reports a failed admitted document fetch without inventing content", async () => {
    const documentURL = "/snapshot/example/modules/missing.md";
    mockFetch([{ path: "modules/missing.md", url: documentURL }]);
    history.replaceState({}, "", "/local?company_id=belluga&project_id=builder&page=documents");
    render(<Projects />);
    fireEvent.click(await screen.findByRole("button", { name: /Read document/ }));
    expect(await screen.findByRole("alert")).toHaveTextContent("could not be loaded");
    expect(screen.queryByText("Current direction.")).not.toBeInTheDocument();
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
      return { ok: true, json: async () => ({ ...detail("belluga", "boora"), snapshot_fingerprint: booraFingerprint }) };
    }));
    history.replaceState({}, "", "/local?company_id=belluga&project_id=builder&page=overview");
    render(<Projects />);
    await waitFor(() => expect(fetch).toHaveBeenCalledWith(expect.stringContaining("project_id=builder"), expect.anything()));
    history.pushState({}, "", "/local?company_id=belluga&project_id=boora&page=overview");
    window.dispatchEvent(new PopStateEvent("popstate"));
    expect(await screen.findByText("Summary for boora")).toBeInTheDocument();
    await act(async () => { releaseBuilder({ ok: true, json: async () => detail("belluga", "builder") }); });
    expect(screen.getByText("Summary for boora")).toBeInTheDocument();
    expect(screen.queryByText("Summary for builder")).not.toBeInTheDocument();
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
    history.replaceState({}, "", "/local?company_id=belluga&project_id=builder&page=prototypes");
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
    history.replaceState({}, "", "/local?company_id=belluga&project_id=builder&page=design-system");
    render(<Projects />);
    expect(await screen.findByRole("heading", { name: "Applied color roles" })).toBeInTheDocument();
    expect(screen.getByText("#101820")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Typography" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Open Design System separately ↗" })).toHaveAttribute("target", "_blank");
    expect(document.querySelector(".design-system-view iframe")).toBeNull();
  });
  it("does not render a Project with a mismatched saved identity", async () => {
    vi.stubGlobal("fetch", vi.fn(async (input: RequestInfo | URL) => ({ ok: true, json: async () => String(input).includes("project_id=") ? detail("second", "builder") : { companies, projects } })));
    history.replaceState({}, "", "/local?company_id=belluga&project_id=builder");
    render(<Projects />);
    expect(await screen.findByRole("alert")).toHaveTextContent(/identity\/integrity/);
    expect(screen.queryByRole("navigation", { name: "Project pages" })).not.toBeInTheDocument();
  });
  it("rejects the artifact-only legacy link explicitly", async () => {
    history.replaceState({}, "", "/local?company_id=belluga&project_id=builder&artifact=prototype:demo");
    render(<Projects />);
    expect(await screen.findByRole("alert")).toHaveTextContent(/artifact-only link is incomplete/);
  });
});
