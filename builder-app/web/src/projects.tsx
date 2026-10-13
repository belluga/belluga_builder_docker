import { useEffect, useRef, useState } from "react";
import type { ReactNode } from "react";
import ReactMarkdown from "react-markdown";
import { LocalArtifactPreview } from "./local-artifact-preview";
import type { PrototypeSelection } from "./local-artifact-preview";
import { resolveProjectTheme } from "./project-theme";
import type { ProjectTheme } from "./project-theme";
import "./projects.css";

type Company = { id: string; name: string };
type Artifact = { id: string; kind: string; name: string; state: string; entry_point?: string; files?: string[]; diagnostic?: string };
type Project = { id: string; name: string; company_id: string; preparation_state: string; preparation_diagnostic?: string; snapshot_fingerprint?: string; artifacts: Artifact[] };
type Catalog = { companies: Company[]; projects: Project[] };
type RoadmapTodo = { id: string; title: string; path: string; kind: "executable" | "planning"; record_state: string };
type RoadmapRelease = { id: string; name: string; description: string; todos: RoadmapTodo[] };
type RoadmapEpic = { id: string; name: string; description: string; releases: RoadmapRelease[] };
type RoadmapProjection = { schema_version: string; project_id: string; focus_release_id: string; delivery_verification: "unverified"; epics: RoadmapEpic[] };
type Detail = { consumer_readiness: string; company_id?: string; project_id?: string; snapshot_fingerprint?: string; artifacts: Artifact[]; roadmap?: RoadmapProjection; snapshot_observations?: Record<string, unknown>; portfolio_summary?: string; portfolio_cover_url?: string; documents?: { path: string; url: string }[]; diagnostic?: string };
type Page = "overview" | "documents" | "design-system" | "prototypes" | "roadmap";
type Route = { companyID: string; projectID: string; page: string; pageExplicit: boolean; prototype: PrototypeSelection };
type ProjectView = { key: string; detail: Detail; theme?: ProjectTheme; themeDiagnostic?: string };

const pages: { id: Page; label: string }[] = [
  { id: "overview", label: "Overview" }, { id: "documents", label: "Documents / decisions" },
  { id: "design-system", label: "Design System" }, { id: "prototypes", label: "Prototype" },
  { id: "roadmap", label: "Roadmap" },
];
const prototypeKeys = ["artifact", "prototype_id", "screen_id", "state_id", "scenario_id", "scenario_step", "fingerprint", "device", "scale", "mode"];

function route(): Route {
  const q = new URLSearchParams(location.search);
  return {
    companyID: q.get("company_id") || "", projectID: q.get("project_id") || "",
    page: q.get("page") || "overview", pageExplicit: q.has("page"),
    prototype: {
      mode: q.get("mode") || "", artifact: q.get("artifact") || "", prototypeID: q.get("prototype_id") || "",
      screenID: q.get("screen_id") || "", stateID: q.get("state_id") || "", scenarioID: q.get("scenario_id") || "",
      scenarioStep: q.get("scenario_step") || "", fingerprint: q.get("fingerprint") || "",
      device: q.get("device") || "", scale: q.get("scale") || "",
    },
  };
}

function navigate(changes: Record<string, string | null>, replace = false) {
  const url = new URL(location.href);
  const contextChanged = (changes.company_id !== undefined && changes.company_id !== url.searchParams.get("company_id")) ||
    (changes.project_id !== undefined && changes.project_id !== url.searchParams.get("project_id"));
  const pageChanged = changes.page !== undefined && changes.page !== url.searchParams.get("page");
  if (contextChanged) { url.searchParams.delete("page"); prototypeKeys.forEach((key) => url.searchParams.delete(key)); }
  else if (pageChanged) prototypeKeys.forEach((key) => url.searchParams.delete(key));
  Object.entries(changes).forEach(([key, value]) => value ? url.searchParams.set(key, value) : url.searchParams.delete(key));
  if (replace) history.replaceState({}, "", url); else history.pushState({}, "", url);
  window.dispatchEvent(new PopStateEvent("popstate"));
}

function snapshotFileURL(detail: Detail, path: string) {
  if (!detail.company_id || !detail.project_id || !detail.snapshot_fingerprint) return "";
  return `/snapshot/${encodeURIComponent(detail.company_id)}/${encodeURIComponent(detail.project_id)}/${detail.snapshot_fingerprint}/${path.split("/").map(encodeURIComponent).join("/")}`;
}

function artifactURL(detail: Detail | null, artifact: Artifact) {
  return detail && artifact.state === "available" && artifact.entry_point ? snapshotFileURL(detail, artifact.entry_point) : "";
}

function stateLabel(state: string) {
  return state === "available" ? "Available" : state === "invalid" ? "Needs attention" : "Not available yet";
}

function documentLabel(path: string) {
  const filename = path.split("/").pop() || path;
  const words = filename.replace(/\.md$/i, "").replace(/[_-]+/g, " ");
  return words.charAt(0).toUpperCase() + words.slice(1);
}

function RoadmapView({ roadmap, detail, projectID, compact = false }: { roadmap?: RoadmapProjection; detail: Detail; projectID: string; compact?: boolean }) {
  const roadmapArtifact = detail.artifacts.find((item) => item.kind === "roadmap");
  const epics = roadmap?.epics || [];
  const focusedRelease = roadmap?.focus_release_id || "";
  const initialEpic = epics.find((epic) => epic.releases.some((release) => release.id === focusedRelease))?.id || epics[0]?.id || "";
  const [selectedEpicID, setSelectedEpicID] = useState(initialEpic);
  const [expandedReleaseIDs, setExpandedReleaseIDs] = useState<string[]>(focusedRelease ? [focusedRelease] : []);
  const epicRail = useRef<HTMLDivElement>(null);
  useEffect(() => {
    epicRail.current?.querySelector<HTMLButtonElement>(".selected")?.scrollIntoView?.({ block: "nearest", inline: "start" });
  }, [selectedEpicID]);
  if (!roadmap || roadmap.project_id !== projectID || roadmap.schema_version !== "builder-roadmap-v1" || roadmap.delivery_verification !== "unverified") {
    return <p className="project-notice roadmap-unavailable" role={roadmapArtifact?.state === "invalid" ? "alert" : "status"}>
      {roadmapArtifact?.diagnostic || "Project Roadmap data is not available in this saved snapshot. Delivery is not verified."}
    </p>;
  }
  const selectedEpic = epics.find((epic) => epic.id === selectedEpicID) || epics.find((epic) => epic.id === initialEpic);
  const selectedEpicIndex = Math.max(0, epics.findIndex((epic) => epic.id === selectedEpic?.id));
  const releases = selectedEpic?.releases || [];
  const focusName = selectedEpic?.releases.find((release) => release.id === focusedRelease)?.name;
  return <section className={`roadmap-view${compact ? " roadmap-view-compact" : ""}`} aria-label={compact ? "Project Roadmap overview" : "Project Roadmap"}>
    {!compact && <header className="roadmap-heading"><div><span>PROJECT ROADMAP</span><h2>Planned work</h2></div><p>Delivery not verified</p></header>}
    <div className="roadmap-rail-group">
      <h3>Epics <span className="roadmap-epic-navigation"><button type="button" aria-label="Previous Epic" disabled={selectedEpicIndex === 0} onClick={() => { const epic = epics[selectedEpicIndex - 1]; if (epic) { setSelectedEpicID(epic.id); setExpandedReleaseIDs(epic.releases.some((release) => release.id === focusedRelease) ? [focusedRelease] : []); } }}>←</button><button type="button" aria-label="Next Epic" disabled={selectedEpicIndex >= epics.length - 1} onClick={() => { const epic = epics[selectedEpicIndex + 1]; if (epic) { setSelectedEpicID(epic.id); setExpandedReleaseIDs(epic.releases.some((release) => release.id === focusedRelease) ? [focusedRelease] : []); } }}>→</button></span></h3>
      <div className="roadmap-rail roadmap-epic-rail" ref={epicRail} role="group" aria-label="Select Epic">
        {epics.map((epic) => <button key={epic.id} className={epic.id === selectedEpic?.id ? "selected" : ""} aria-pressed={epic.id === selectedEpic?.id} onClick={() => { setSelectedEpicID(epic.id); setExpandedReleaseIDs(epic.releases.some((release) => release.id === focusedRelease) ? [focusedRelease] : []); }}>
          <span>{epic.id.toUpperCase()}</span><strong>{epic.name}</strong><small>{epic.description}</small>
        </button>)}
      </div>
    </div>
    {compact && <div className="roadmap-rail-group roadmap-releases-rail">
      <h3>Releases {selectedEpic && <span>· {selectedEpic.name}</span>}</h3>
      <div className="roadmap-rail" role="group" aria-label="Releases for selected Epic">
        {releases.map((release) => <div key={release.id} className={`roadmap-release-chip${release.id === focusedRelease ? " focused" : ""}`}>
          <strong>{release.name}</strong><span>{release.description}</span><span>Delivery not verified</span>
          {release.id === focusedRelease && <small>Current focus</small>}
        </div>)}
        {releases.length === 0 && <p className="roadmap-no-releases">No Releases defined yet.</p>}
      </div>
    </div>}
    {!compact && <div className="roadmap-release-list" role="group" aria-label="Releases for selected Epic">
      {focusName && <p className="roadmap-focus-note">Catalog focus: {focusName}. Focus is a navigation choice and does not indicate delivery.</p>}
      {releases.map((release) => <section className={`roadmap-release${release.id === focusedRelease ? " focused" : ""}`} key={release.id}>
        <button className="roadmap-release-toggle" aria-expanded={expandedReleaseIDs.includes(release.id)} onClick={() => setExpandedReleaseIDs((current) => current.includes(release.id) ? current.filter((id) => id !== release.id) : [...current, release.id])}>
          <span className="roadmap-release-name"><span>{release.id.toUpperCase()}</span><strong>{release.name}</strong><small>{release.description}</small></span>
          <span className="roadmap-delivery-status">Delivery not verified</span><span className="roadmap-release-chevron" aria-hidden="true">{expandedReleaseIDs.includes(release.id) ? "⌄" : "›"}</span>
        </button>
        {expandedReleaseIDs.includes(release.id) && <ul>{release.todos.map((todo) => <li key={todo.id}>
          <div><strong>{todo.title}</strong><span>{todo.path}</span></div>
          <p><span>{todo.kind === "planning" ? "Planning ledger" : "Executable TODO"}</span><span>{todo.record_state === "completed" ? "Local record: completed" : todo.record_state === "active" ? "Local record: active" : "Local record: other"}</span><span>Delivery not verified</span></p>
        </li>)}</ul>}
      </section>)}
      {releases.length === 0 && <p className="project-notice">No Releases defined yet.</p>}
    </div>}
  </section>;
}

function PortalHeader({ children }: { children: ReactNode }) {
  return <header className="projects-mast"><a href="/local">Builder <span>Project directory</span></a>{children}</header>;
}

function ProjectCard({ project, onOpen }: { project: Project; onOpen: () => void }) {
  const [detail, setDetail] = useState<Detail | null>(null);
  const [coverError, setCoverError] = useState(false);
  useEffect(() => {
    if (project.preparation_state !== "ready") return;
    const controller = new AbortController();
    fetch(`/api/local-preview?company_id=${encodeURIComponent(project.company_id)}&project_id=${encodeURIComponent(project.id)}`, { signal: controller.signal })
      .then((response) => response.ok ? response.json() as Promise<Detail> : Promise.reject())
      .then((value) => {
        if (!controller.signal.aborted && value.company_id === project.company_id && value.project_id === project.id && value.snapshot_fingerprint === project.snapshot_fingerprint) setDetail(value);
      }).catch(() => undefined);
    return () => controller.abort();
  }, [project.company_id, project.id, project.preparation_state, project.snapshot_fingerprint]);
  const verified = detail?.company_id === project.company_id && detail.project_id === project.id && detail.snapshot_fingerprint === project.snapshot_fingerprint ? detail : null;
  return <button className="project-card" onClick={onOpen}>
    {verified?.portfolio_cover_url && !coverError ? <img src={verified.portfolio_cover_url} alt="" onError={() => setCoverError(true)} /> : <span className="project-card-mark">{project.name.slice(0, 1)}</span>}
    <small>{project.preparation_state === "ready" ? "SAVED SNAPSHOT READY" : "REGISTERED · NOT READY"}</small>
    <strong>{project.name}</strong>
    <span>{verified?.portfolio_summary || verified?.artifacts.find((item) => item.kind === "portfolio" && item.state === "invalid")?.diagnostic || project.preparation_diagnostic || "Open this registered Project for its available details."}</span>
    <i><span>Explore Project</span><b aria-hidden="true">↗</b></i>
  </button>;
}

function DesignSystemView({ artifacts, detail, theme }: { artifacts: Artifact[]; detail: Detail; theme: ProjectTheme }) {
  if (!artifacts.length) return <p className="project-notice">No Design System artifact is available in this snapshot.</p>;
  const palette = [
    { id: "canvas", label: "Canvas" },
    { id: "surface", label: "Surface" },
    { id: "accent", label: "Accent" },
    { id: "border", label: "Border" },
    { id: "focus", label: "Focus" },
  ] as const;
  const value = (role: string) => theme[`--project-${role}`];
  return <div className="design-system-view">{artifacts.map((artifact) => {
    const href = artifactURL(detail, artifact);
    if (!href) return <p className="project-notice" key={artifact.id}>{artifact.diagnostic || `${artifact.name} is ${stateLabel(artifact.state).toLowerCase()}.`}</p>;
    return <div key={artifact.id}>
      <section className="design-system-palette"><small>ADMITTED PROJECT DESIGN SYSTEM</small><h2>Applied color roles</h2><p>These values are resolved from this Project's saved Design System.</p><div className="design-system-swatches">{palette.map((item) => <div className="design-system-swatch" key={item.id}><div className="design-system-swatch-chip" aria-hidden="true" style={{ backgroundColor: value(item.id) }} /><strong>{item.label}</strong><code>{value(item.id)}</code></div>)}</div></section>
      <div className="design-system-cards">
        <article><h2>Typography</h2><p className="design-system-type-sample">Aa Bb Cc 123</p><p>Display {value("displaySize")} · body {value("bodySize")} · line height {value("lineHeight")}</p></article>
        <article><h2>Components</h2><div className="design-system-controls"><button disabled>Primary action</button><button disabled>Secondary action</button></div><p>Buttons use the selected Project's text, accent and border roles.</p></article>
        <article><h2>Shape & focus</h2><div className="design-system-shape-sample">Card radius {value("cardRadius")}</div><p>Focus color <code>{value("focus")}</code></p></article>
      </div>
      <section className="design-system-source"><div><small>{stateLabel(artifact.state).toUpperCase()} · SAVED SNAPSHOT</small><h2>{artifact.name}</h2><p>The complete token catalog and examples remain in the admitted Project artifact.</p></div><a href={href} target="_blank" rel="noreferrer">Open Design System separately ↗</a></section>
    </div>;
  })}</div>;
}

export function Projects() {
  const [selection, setSelection] = useState(route);
  const [catalog, setCatalog] = useState<Catalog>({ companies: [], projects: [] });
  const [catalogState, setCatalogState] = useState<"loading" | "ready" | "error">("loading");
  const [view, setView] = useState<ProjectView | null>(null);
  const [viewState, setViewState] = useState<"loading" | "ready" | "error">("loading");
  const [pendingCompanyID, setPendingCompanyID] = useState("");
  const [selectedDoc, setSelectedDoc] = useState("");
  const [markdown, setMarkdown] = useState("");
  const [docState, setDocState] = useState<"idle" | "loading" | "ready" | "error">("idle");
  useEffect(() => {
    const update = () => { setSelection(route()); setPendingCompanyID(""); };
    addEventListener("popstate", update);
    return () => removeEventListener("popstate", update);
  }, []);
  useEffect(() => {
    const controller = new AbortController();
    fetch("/api/local-preview", { signal: controller.signal }).then((response) => {
      if (!response.ok) throw new Error("Catalog unavailable");
      return response.json() as Promise<Catalog>;
    }).then((value) => { if (!controller.signal.aborted) { setCatalog(value); setCatalogState("ready"); } })
      .catch(() => { if (!controller.signal.aborted) setCatalogState("error"); });
    return () => controller.abort();
  }, []);
  const company = catalog.companies.find((item) => item.id === selection.companyID);
  const project = catalog.projects.find((item) => item.company_id === selection.companyID && item.id === selection.projectID);
  const projects = catalog.projects.filter((item) => item.company_id === selection.companyID);
  const viewKey = project ? `${project.company_id}/${project.id}/${project.snapshot_fingerprint || ""}` : "";
  useEffect(() => {
    setView(null); setViewState("loading"); setSelectedDoc(""); setMarkdown(""); setDocState("idle");
    if (!project) return;
    const controller = new AbortController();
    async function load() {
      try {
        const response = await fetch(`/api/local-preview?company_id=${encodeURIComponent(project!.company_id)}&project_id=${encodeURIComponent(project!.id)}`, { signal: controller.signal });
        if (!response.ok) throw new Error("Snapshot unavailable");
        const detail = await response.json() as Detail;
        if (detail.company_id !== project!.company_id || detail.project_id !== project!.id ||
          (project!.snapshot_fingerprint && detail.snapshot_fingerprint !== project!.snapshot_fingerprint)) throw new Error("Snapshot identity mismatch");
        let theme: ProjectTheme | undefined;
        let themeDiagnostic: string | undefined;
        if (detail.consumer_readiness === "ready") {
          const design = detail.artifacts.find((item) => item.kind === "design_system" && item.state === "available" && item.files?.includes("design/system/design-system.json"));
          if (!design || !detail.snapshot_fingerprint) themeDiagnostic = "The admitted Design System definition is unavailable. Refresh registration after correcting the artifact.";
          else {
            const definition = await fetch(snapshotFileURL(detail, "design/system/design-system.json"), { signal: controller.signal });
            if (!definition.ok || definition.headers.get("X-Builder-Snapshot-Fingerprint") !== detail.snapshot_fingerprint) throw new Error("Design System snapshot mismatch");
            const resolved = resolveProjectTheme(await definition.json());
            theme = resolved.style; themeDiagnostic = resolved.diagnostic;
          }
        }
        if (!controller.signal.aborted) { setView({ key: viewKey, detail, theme, themeDiagnostic }); setViewState("ready"); }
      } catch {
        if (!controller.signal.aborted) setViewState("error");
      }
    }
    void load();
    return () => controller.abort();
  }, [viewKey]);
  const activeView = view?.key === viewKey ? view : null;
  const detail = activeView?.detail || null;
  const documents = detail?.documents || [];
  useEffect(() => {
    if (!selectedDoc || !documents.some((item) => item.url === selectedDoc)) return;
    const controller = new AbortController();
    setDocState("loading");
    fetch(selectedDoc, { signal: controller.signal }).then((response) => {
      if (!response.ok) throw new Error("Document unavailable");
      return response.text();
    }).then((value) => { if (!controller.signal.aborted) { setMarkdown(value); setDocState("ready"); } })
      .catch(() => { if (!controller.signal.aborted) setDocState("error"); });
    return () => controller.abort();
  }, [selectedDoc, viewKey]);
  const headings = [...markdown.matchAll(/^#{1,3}\s+(.+)$/gm)].map((match) => match[1]);
  const page = pages.find((item) => item.id === selection.page)?.id;
  const available = detail?.artifacts || [];

  if (catalogState === "loading") return <main className="projects-page"><p role="status">Loading registered Projects…</p></main>;
  if (catalogState === "error") return <main className="projects-page"><p role="alert">The local Project catalog is unavailable.</p></main>;
  if (selection.projectID && (!company || !project)) return <main className="projects-page"><p role="alert">This Company and Project selection is unavailable.</p><a href="/local">Back to Companies</a></main>;
  if (selection.companyID && !company) return <main className="projects-page"><p role="alert">This Company is unavailable.</p><a href="/local">Back to Companies</a></main>;
  if (selection.projectID && !page) return <main className="projects-page"><p role="alert">This Project page is unavailable.</p><a href={`/local?company_id=${encodeURIComponent(selection.companyID)}&project_id=${encodeURIComponent(selection.projectID)}`}>Back to Overview</a></main>;
  if (selection.projectID && selection.prototype.artifact && !selection.pageExplicit && !selection.prototype.mode) return <main className="projects-page"><p role="alert">This artifact-only link is incomplete. Open the Prototype page.</p><a href={`/local?company_id=${encodeURIComponent(selection.companyID)}&project_id=${encodeURIComponent(selection.projectID)}&page=prototypes`}>Open Prototype</a></main>;
  if (selection.prototype.mode && (selection.prototype.mode !== "presentation" || page !== "prototypes" || !selection.projectID)) return <main className="projects-page"><p role="alert">This presentation link is invalid.</p><a href="/local">Back to Companies</a></main>;

  if (!selection.companyID) return <main className="projects-page projects-portal"><PortalHeader><p>Registered Companies and their Projects</p></PortalHeader><div className="portal-main"><p className="projects-eyebrow">01 / Project directory</p><h1>Choose a company to explore its projects.</h1><p className="portal-intro">Select a registered company to view its Projects and their available snapshot information.</p><section className="company-grid">{catalog.companies.map((item) => { const count = catalog.projects.filter((p) => p.company_id === item.id).length; return <button key={item.id} className="company-card" onClick={() => navigate({ company_id: item.id, project_id: null, page: null })}><span className="company-card-mark" aria-hidden="true">{item.name.slice(0, 1)}</span><small>COMPANY</small><strong>{item.name}</strong><span className="company-card-count">{count} registered {count === 1 ? "Project" : "Projects"}</span><span className="company-card-cta">Open company portfolio <b aria-hidden="true">↗</b></span></button>; })}</section>{!catalog.companies.length && <p>No Companies are registered yet.</p>}</div></main>;
  if (!selection.projectID) return <main className="projects-page projects-portal"><PortalHeader><p>Company portfolio · registered Projects</p></PortalHeader><div className="portal-main"><a className="portal-breadcrumb" href="/local">← All Companies</a><div className="portfolio-hero"><div><p className="projects-eyebrow">02 / {company?.name}</p><h1>Explore registered Projects for {company?.name}.</h1><p className="portal-intro">Open a registered Project to review its available artifacts and snapshot information.</p></div><aside>Registered Projects<strong>{projects.length}</strong></aside></div><section className="project-cards" aria-label={`${company?.name} Projects`}>{projects.map((item) => <ProjectCard key={`${item.company_id}/${item.id}`} project={item} onOpen={() => navigate({ project_id: item.id, page: "overview" })} />)}</section>{!projects.length && <p>No Projects are registered under this Company yet.</p>}</div></main>;

  if (!activeView && viewState !== "error") return <main className="projects-page"><p role="status">Loading the saved Project snapshot and Design System…</p></main>;
  if (viewState === "error" || !detail) return <main className="projects-page"><p role="alert">The saved Project snapshot is unavailable or failed identity/integrity verification.</p><a href="/local">Back to Companies</a></main>;
  if (detail.consumer_readiness !== "ready") return <main className="projects-page"><p role="alert">This Project is registered but its saved snapshot is not ready. {detail.diagnostic}</p><a href="/local">Back to Companies</a></main>;
  if (!activeView?.theme) return <main className="projects-page"><p role="alert">{activeView?.themeDiagnostic || "The Project Design System cannot be applied."}</p><p>Project: {project?.name} · saved snapshot {detail.snapshot_fingerprint?.slice(0, 12)}</p><a href="/local">Back to Companies</a></main>;

  const previewProps = { companyID: selection.companyID, projectID: selection.projectID, companyName: company?.name || "Company", projectName: project?.name || "Project", detail, selection: selection.prototype, onNavigate: navigate };
  if (selection.prototype.mode === "presentation") return <div className="project-theme" style={activeView.theme}><LocalArtifactPreview {...previewProps} /></div>;
  const tentativeProjects = catalog.projects.filter((item) => item.company_id === (pendingCompanyID || selection.companyID));
  return <main className="projects-page projects-project" style={activeView.theme}>
    <header className="project-bar"><a className="project-bar-brand" href="/local" aria-label="Project directory">B</a>
      <label>COMPANY<select aria-label="Switch Company" value={pendingCompanyID || selection.companyID} onChange={(event) => setPendingCompanyID(event.target.value)}><option value={selection.companyID}>{company?.name}</option>{catalog.companies.filter((item) => item.id !== selection.companyID).map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}</select></label>
      <label>PROJECT<select aria-label="Switch Project" value={pendingCompanyID ? "" : project?.id} onChange={(event) => { if (event.target.value) navigate({ company_id: pendingCompanyID || selection.companyID, project_id: event.target.value, page: "overview" }); }}>{pendingCompanyID && <option value="">Choose a Project</option>}{tentativeProjects.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}</select></label>
      {pendingCompanyID && <button className="project-switch-cancel" onClick={() => setPendingCompanyID("")}>Cancel</button>}
      <span className="project-bar-title">{project?.name}</span>
    </header>
    <div className="project-layout"><nav className="project-nav" aria-label="Project pages"><p>{company?.name}</p><h2>{project?.name}</h2>{pages.map((item) => <button key={item.id} className={page === item.id ? "active" : ""} onClick={() => navigate({ page: item.id })}>{item.label}</button>)}</nav>
      <section className="project-content">{page !== "overview" && <><p className="projects-eyebrow">{company?.name} / {project?.id}</p><h1>{pages.find((item) => item.id === page)?.label}</h1></>}
        {page === "overview" && <div className="project-overview"><section className="project-hero"><div className="project-hero-copy"><span className="project-source-tag">PROJECT OVERVIEW</span><h1>{project?.name}</h1><p>{detail.portfolio_summary || (documents.length ? `Consult the admitted documents to review the purpose and direction of ${project?.name}.` : `No admitted summary or canonical documents are available for ${project?.name} yet.`)}</p></div><aside>Project identity<strong>{company?.name}</strong></aside></section>
          <div className="overview-cards"><article className="overview-card"><span aria-hidden="true">↗</span><h2>Por que existe</h2><p>{documents.length ? "Review the admitted Project documents for its purpose and operating context." : "No admitted documents are available to substantiate the Project's purpose yet."}</p><button onClick={() => navigate({ page: "documents" })}>{documents.length ? "Explore context" : "View Documents"} <b aria-hidden="true">→</b></button></article><article className="overview-card"><span aria-hidden="true">▦</span><h2>O caminho</h2><p>{documents.length ? "Consult admitted documents for recorded decisions and planning." : "There are no admitted documents to show recorded decisions or planning yet."}</p><button onClick={() => navigate({ page: "documents" })}>{documents.length ? "Read documents" : "Open Documents"} <b aria-hidden="true">→</b></button></article><article className="overview-card"><span aria-hidden="true">◇</span><h2>Artefatos visíveis</h2><p>Open the artifacts admitted to this saved Project snapshot.</p><div className="overview-card-links"><button onClick={() => navigate({ page: "design-system" })}>Design System</button><button onClick={() => navigate({ page: "prototypes" })}>Prototype</button></div></article></div>
          <RoadmapView key={viewKey} roadmap={detail.roadmap} detail={detail} projectID={project!.id} compact />
          <section className="project-landing-reference" aria-label="Executive Landing artifact"><div><small>SEPARATE PROJECT ARTIFACT</small><h2>Executive Landing</h2><p>The Landing is a separate artifact and is not embedded in this overview.</p></div>{(() => { const landing = available.find((item) => item.kind === "landing"); const src = landing ? artifactURL(detail, landing) : ""; return src ? <a href={src} target="_blank" rel="noreferrer">Open Landing ↗</a> : <span>{landing?.diagnostic || "Not available in this saved snapshot"}</span>; })()}</section>
          <section className="overview-status" aria-label="Saved artifact status"><div className="project-metrics"><div><strong>{available.filter((item) => item.state === "available").length}</strong><span>Available artifacts</span></div><div><strong>{available.filter((item) => item.state === "invalid").length}</strong><span>Artifacts needing attention</span></div><div><strong>{documents.length}</strong><span>Admitted documents</span></div></div><p className="project-status">Saved snapshot: {detail.snapshot_fingerprint?.slice(0, 12)} · availability does not indicate human approval or current source freshness.</p>{available.filter((item) => item.state !== "available").map((item) => <p className="project-note" key={`${item.kind}:${item.id}`}>{item.name}: {stateLabel(item.state)}{item.diagnostic ? ` · ${item.diagnostic}` : ""}</p>)}</section></div>}
        {page === "documents" && <div className="documents-page"><p className="documents-page-intro">Read the canonical Markdown files admitted to this Project's saved snapshot.</p>
          {documents.length ? <section className="document-list" aria-label="Admitted Documents">{documents.map((item) => <article className="document-row" key={item.path}><div><h2>{documentLabel(item.path)}</h2><p>{item.path}</p></div><button aria-pressed={selectedDoc === item.url} onClick={() => setSelectedDoc(item.url)}>{selectedDoc === item.url ? "Selected" : "Read document"} <span aria-hidden="true">→</span></button></article>)}</section> : <p className="project-notice documents-empty">{available.find((item) => item.kind === "documents")?.diagnostic || "No canonical Markdown documents are admitted in this snapshot."}</p>}
          {selectedDoc && <article className="project-markdown document-reader">{docState === "loading" && <p role="status">Loading document…</p>}{docState === "error" && <p role="alert">This admitted document could not be loaded.</p>}{docState === "ready" && <>{headings.length > 0 && <nav className="document-headings" aria-label="Document headings">{headings.map((heading, index) => <button key={`${heading}-${index}`} onClick={() => document.querySelectorAll(".project-markdown h1, .project-markdown h2, .project-markdown h3")[index]?.scrollIntoView({ behavior: "smooth" })}>{heading}</button>)}</nav>}<ReactMarkdown components={{ img: () => null }}>{markdown}</ReactMarkdown></>}</article>}
          <section className="documents-note"><h2>Reading is not review</h2><p>This view reads files from the saved snapshot. Availability does not indicate human approval or current source freshness.</p></section>
        </div>}
        {page === "design-system" && activeView?.theme && <DesignSystemView artifacts={available.filter((item) => item.kind === "design_system")} detail={detail} theme={activeView.theme} />}
        {page === "prototypes" && <LocalArtifactPreview {...previewProps} />}
        {page === "roadmap" && <RoadmapView key={viewKey} roadmap={detail.roadmap} detail={detail} projectID={project!.id} />}
      </section></div>
  </main>;
}
