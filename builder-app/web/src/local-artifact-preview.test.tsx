import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { LocalArtifactPreview } from "./local-artifact-preview";
import type { PrototypeSelection } from "./local-artifact-preview";

const revision = "a".repeat(40);
const inventoryDigest = `sha256:${"b".repeat(64)}`;
const selection: PrototypeSelection = { mode: "", artifact: "", prototypeID: "", screenID: "", stateID: "", scenarioID: "", scenarioStep: "", revision: "", device: "", scale: "" };
type TestPrototype = { id: string; name: string; root: string; status: "active" | "archived"; authoring_mode: "image_first"; entry_point: string; screens: { id: string; name: string; path: string; default_state_id: string; states: { id: string; name: string; identifier_image: string; approved_references: { id: string; image: string; approval_evidence: string }[] }[] }[]; scenarios: { id: string; name: string; steps: { screen_id: string; state_id: string }[] }[]; transitions: never[] };
const prototype: TestPrototype = { id: "demo", name: "Demo", root: "prototypes/demo", status: "active", authoring_mode: "image_first", entry_point: "index.html",
  screens: [{ id: "home", name: "Home", path: "index.html", default_state_id: "default", states: [{ id: "default", name: "Default", identifier_image: "home.png", approved_references: [] }] }],
  scenarios: [{ id: "tour", name: "Tour", steps: [{ screen_id: "home", state_id: "default" }, { screen_id: "home", state_id: "default" }] }], transitions: [] };
const summaries = [{ id: "demo", name: "Demo", status: "active", detail_url: `/api/prototypes/${revision}/demo?company_id=company&project_id=project` },
  { id: "other", name: "Other", status: "archived", detail_url: `/api/prototypes/${revision}/other?company_id=company&project_id=project` }];
const detailResponse = (item: TestPrototype) => ({ revision, inventory_digest: inventoryDigest, item,
  file_urls: [`${item.root}/${item.entry_point}`, `${item.root}/home.png`].map((path) => ({ path, url: `/api/prototypes/file/company/project/${revision}/${inventoryDigest.slice(7)}/files/${path}` })) });
const catalog = { revision, inventory_digest: inventoryDigest, items: summaries };
const base = { companyID: "company", projectID: "project", companyName: "Company", projectName: "Project", selection, onNavigate: vi.fn() };
function mockAPI() {
  const fetcher = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    if (url === "/api/prototypes?company_id=company&project_id=project") return new Response(JSON.stringify(catalog), { status: 200 });
    const summary = summaries.find((item) => url === item.detail_url);
    if (summary) return new Response(JSON.stringify(detailResponse(summary.id === "demo" ? prototype : { ...prototype, id: "other", name: "Other", status: "archived" })), { status: 200 });
    if (url.includes("/files/") && init?.method === "HEAD") return new Response(null, { status: 200, headers: { "X-Prototype-Revision": revision, "X-Prototype-Inventory-Digest": inventoryDigest } });
    return new Response("not found", { status: 404 });
  });
  vi.stubGlobal("fetch", fetcher);
  return fetcher;
}
afterEach(() => { cleanup(); vi.unstubAllGlobals(); vi.restoreAllMocks(); });

describe("revision-bound Prototype viewer", () => {
  it("loads the API catalog and one detail independently of snapshots", async () => {
    const fetcher = mockAPI();
    render(<LocalArtifactPreview {...base} />);
    expect(await screen.findByRole("heading", { name: "Home / Default" })).toBeInTheDocument();
    expect(screen.getByText(/revisão Git/)).toHaveTextContent(revision.slice(0, 12));
    expect(fetcher.mock.calls.filter(([url]) => String(url).includes("/demo?")).length).toBe(1);
    expect(fetcher.mock.calls.some(([url]) => String(url).startsWith("/snapshot/"))).toBe(false);
  });

  it("fetches detail lazily when another catalog item is selected", async () => {
    const fetcher = mockAPI();
    render(<LocalArtifactPreview {...base} />);
    await screen.findByRole("heading", { name: "Home / Default" });
    expect(fetcher.mock.calls.some(([url]) => String(url).includes("/other?"))).toBe(false);
    fireEvent.change(screen.getByLabelText("Status do protótipo"), { target: { value: "all" } });
    fireEvent.click(screen.getByRole("button", { name: /Other/ }));
    await waitFor(() => expect(fetcher.mock.calls.some(([url]) => String(url).includes("/other?"))).toBe(true));
  });

  it("preserves repeated scenario steps and verifies presentation HTML against revision and digest", async () => {
    const fetcher = mockAPI();
    const onNavigate = vi.fn();
    render(<LocalArtifactPreview {...base} onNavigate={onNavigate} selection={{ ...selection, mode: "presentation", artifact: "prototype:demo", prototypeID: "demo", screenID: "home", stateID: "default", scenarioID: "tour", scenarioStep: "1", revision, device: "mobile", scale: "fit" }} />);
    expect(await screen.findByTitle("Demo · Home · Default")).toBeInTheDocument();
    const htmlURL = String(fetcher.mock.calls.find(([url, init]) => String(url).includes("/files/prototypes/demo/index.html") && (init as RequestInit)?.method === "HEAD")?.[0]);
    expect(htmlURL).toContain("/api/prototypes/");
    expect(htmlURL).toContain(`prototype_state=default`);
    expect(htmlURL).toContain(`/file/company/project/${revision}/${"b".repeat(64)}/files/`);
    expect(screen.getByTitle("Demo · Home · Default").closest("iframe")).toHaveAttribute("src", expect.stringContaining("/api/prototypes/"));
    expect(onNavigate).not.toHaveBeenCalled();
  });

  it("rejects a stale revision URL before requesting presentation HTML", async () => {
    const fetcher = mockAPI();
    render(<LocalArtifactPreview {...base} selection={{ ...selection, mode: "presentation", artifact: "prototype:demo", prototypeID: "demo", screenID: "home", stateID: "default", revision: "c".repeat(40), device: "mobile", scale: "fit" }} />);
    expect(await screen.findByRole("alert")).toHaveTextContent(/revisão Git mudou/);
    expect(fetcher.mock.calls.some(([url]) => String(url).includes("/files/"))).toBe(false);
  });
});
