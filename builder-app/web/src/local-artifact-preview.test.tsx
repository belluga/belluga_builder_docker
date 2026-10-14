import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { LocalArtifactPreview } from "./local-artifact-preview";
import type { PrototypeSelection } from "./local-artifact-preview";

const fingerprint = "a".repeat(64);
const selection: PrototypeSelection = { mode: "", artifact: "", prototypeID: "", screenID: "", stateID: "", scenarioID: "", scenarioStep: "", fingerprint: "", device: "", scale: "" };
const prototype = { id: "demo", name: "Demo", root: "prototypes/demo", status: "active" as const, authoring_mode: "image_first" as const, entry_point: "index.html",
  screens: [{ id: "home", name: "Home", path: "index.html", default_state_id: "default", states: [{ id: "default", name: "Default", identifier_image: "home.png", approved_references: [] }] }],
  scenarios: [{ id: "tour", name: "Tour", steps: [{ screen_id: "home", state_id: "default" }] }], transitions: [] };
const detail = { consumer_readiness: "ready", snapshot_fingerprint: fingerprint, artifacts: [{ id: "demo", kind: "prototype", name: "Demo", state: "available", entry_point: "prototypes/demo/index.html" }], snapshot_observations: { prototype_status: { outcome: "go", items: [prototype] } } };
const base = { companyID: "company", projectID: "project", companyName: "Company", projectName: "Project", detail, selection, onNavigate: vi.fn() };
afterEach(() => { cleanup(); vi.restoreAllMocks(); });

describe("controlled Prototype inspector", () => {
  it("lists admitted screens and states without owning Project navigation", () => {
    render(<LocalArtifactPreview {...base} />);
    expect(screen.getByRole("navigation", { name: "Telas e estados" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Home / Default" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /Abrir apresentação/ })).toHaveAttribute("href", expect.stringContaining("/?"));
    expect(screen.queryByText("Project directory")).not.toBeInTheDocument();
  });
  it("does not expose a Prototype without an admitted available artifact", () => {
    render(<LocalArtifactPreview {...base} detail={{ ...detail, artifacts: [] }} />);
    expect(screen.getByText(/No admitted Prototype/)).toBeInTheDocument();
  });
  it("rejects a stale presentation fingerprint before loading HTML", () => {
    const fetcher = vi.fn(); vi.stubGlobal("fetch", fetcher);
    render(<LocalArtifactPreview {...base} selection={{ ...selection, mode: "presentation", artifact: "prototype:demo", prototypeID: "demo", screenID: "home", stateID: "default", fingerprint: "b".repeat(64), device: "mobile", scale: "fit" }} />);
    expect(screen.getByRole("alert")).toHaveTextContent(/Snapshot alterado/);
    expect(fetcher).not.toHaveBeenCalled();
    vi.unstubAllGlobals();
  });
  it("verifies selected HTML before the presentation iframe appears", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => ({ ok: true, headers: new Headers({ "X-Builder-Snapshot-Fingerprint": fingerprint }) })));
    render(<LocalArtifactPreview {...base} selection={{ ...selection, mode: "presentation", artifact: "prototype:demo", prototypeID: "demo", screenID: "home", stateID: "default", fingerprint, device: "mobile", scale: "fit" }} />);
    await waitFor(() => expect(screen.getByTitle("Demo · Home · Default")).toBeInTheDocument());
    vi.unstubAllGlobals();
  });
  it("uses the owning Project callback for screen selection", () => {
    const onNavigate = vi.fn(); render(<LocalArtifactPreview {...base} onNavigate={onNavigate} />);
    fireEvent.click(screen.getByRole("button", { name: "Default" }));
    expect(onNavigate).toHaveBeenCalledWith(expect.objectContaining({ screen_id: "home", state_id: "default" }));
  });
  it("does not substitute different evidence for invalid inspector IDs", () => {
    for (const change of [{ artifact: "prototype:missing" }, { screenID: "missing" }, { stateID: "missing" }]) {
      const onNavigate = vi.fn();
      render(<LocalArtifactPreview {...base} selection={{ ...selection, ...change }} onNavigate={onNavigate} />);
      expect(screen.getByRole("alert")).toHaveTextContent(/selection is unavailable/);
      expect(screen.queryByRole("heading", { name: "Home / Default" })).not.toBeInTheDocument();
      fireEvent.click(screen.getByRole("button", { name: "Show available Prototypes" }));
      expect(onNavigate).toHaveBeenCalledWith(expect.objectContaining({ artifact: null, screen_id: null, state_id: null }));
      cleanup();
    }
  });
  it("shows an archived direct-link selection in the archived inventory", () => {
    const archived = { ...prototype, id: "old", name: "Archived Demo", status: "archived" as const };
    const onNavigate = vi.fn();
    render(<LocalArtifactPreview {...base} detail={{ ...detail, artifacts: [...detail.artifacts, { id: "old", kind: "prototype", name: "Archived Demo", state: "available", entry_point: "prototypes/demo/index.html" }], snapshot_observations: { prototype_status: { outcome: "go", items: [prototype, archived] } } }} selection={{ ...selection, artifact: "prototype:old", screenID: "home", stateID: "default" }} onNavigate={onNavigate} />);
    expect(screen.getByLabelText("Status do protótipo")).toHaveValue("archived");
    expect(screen.getByRole("button", { name: /Archived Demo/ })).toHaveClass("selected");
    fireEvent.change(screen.getByLabelText("Status do protótipo"), { target: { value: "active" } });
    expect(onNavigate).toHaveBeenCalledWith(expect.objectContaining({ artifact: null, screen_id: null, state_id: null }));
  });
  it("enters a scenario and advances to its next Screen and State", () => {
    const nextScreen = { id: "details", name: "Details", path: "details.html", default_state_id: "default", states: [{ id: "default", name: "Default", identifier_image: "details.png", approved_references: [] }] };
    const twoStep = { ...prototype, screens: [...prototype.screens, nextScreen], scenarios: [{ id: "tour", name: "Tour", steps: [{ screen_id: "home", state_id: "default" }, { screen_id: "details", state_id: "default" }] }] };
    const scenarioDetail = { ...detail, snapshot_observations: { prototype_status: { outcome: "go", items: [twoStep] } } };
    const onNavigate = vi.fn();
    const presentation = { ...selection, mode: "presentation", artifact: "prototype:demo", prototypeID: "demo", screenID: "home", stateID: "default", fingerprint, device: "mobile", scale: "fit" };
    const { rerender } = render(<LocalArtifactPreview {...base} detail={scenarioDetail} selection={presentation} onNavigate={onNavigate} />);
    fireEvent.change(screen.getByLabelText("Cenário"), { target: { value: "tour" } });
    expect(onNavigate).toHaveBeenCalledWith(expect.objectContaining({ scenario_id: "tour", scenario_step: "0", screen_id: "home" }), true);
    rerender(<LocalArtifactPreview {...base} detail={scenarioDetail} selection={{ ...presentation, scenarioID: "tour", scenarioStep: "0" }} onNavigate={onNavigate} />);
    fireEvent.click(screen.getByRole("button", { name: "Próxima" }));
    expect(onNavigate).toHaveBeenCalledWith(expect.objectContaining({ scenario_step: "1", screen_id: "details", state_id: "default" }), true);
  });
  it("rejects an invalid pinned scenario without fetching HTML", () => {
    const fetcher = vi.fn(); vi.stubGlobal("fetch", fetcher);
    render(<LocalArtifactPreview {...base} selection={{ ...selection, mode: "presentation", artifact: "prototype:demo", prototypeID: "demo", screenID: "home", stateID: "default", scenarioID: "missing", scenarioStep: "0", fingerprint, device: "mobile", scale: "fit" }} />);
    expect(screen.getByRole("alert")).toHaveTextContent(/Seleção inválida/);
    expect(fetcher).not.toHaveBeenCalled();
    vi.unstubAllGlobals();
  });
  it("ignores an old HTML verification response after presentation selection changes", async () => {
    let releaseOld!: (value: unknown) => void;
    const oldResponse = new Promise<unknown>((resolve) => { releaseOld = resolve; });
    vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => String(input).includes("index.html") ? oldResponse : Promise.resolve({ ok: false, headers: new Headers() })));
    const presentation = { ...selection, mode: "presentation", artifact: "prototype:demo", prototypeID: "demo", screenID: "home", stateID: "default", fingerprint, device: "mobile", scale: "fit" };
    const { rerender } = render(<LocalArtifactPreview {...base} selection={presentation} />);
    await waitFor(() => expect(fetch).toHaveBeenCalled());
    rerender(<LocalArtifactPreview {...base} selection={{ ...presentation, stateID: "missing" }} />);
    expect(screen.getByRole("alert")).toHaveTextContent(/Seleção inválida/);
    await act(async () => { releaseOld({ ok: true, headers: new Headers({ "X-Builder-Snapshot-Fingerprint": fingerprint }) }); });
    expect(screen.queryByTitle("Demo · Home · Default")).not.toBeInTheDocument();
    vi.unstubAllGlobals();
  });
});
