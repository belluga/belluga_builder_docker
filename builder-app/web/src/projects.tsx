import { useEffect, useLayoutEffect, useRef, useState } from "react";
import type { MouseEvent as ReactMouseEvent, ReactNode } from "react";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import { LocalArtifactPreview } from "./local-artifact-preview";
import type { PrototypeSelection } from "./local-artifact-preview";
import { resolveProjectTheme } from "./project-theme";
import type { ProjectTheme } from "./project-theme";
import "./projects.css";
import "./release-detail.css";
import "./todo-detail.css";

type Company = { id: string; name: string };
type Artifact = { id: string; kind: string; name: string; state: string; entry_point?: string; files?: string[]; diagnostic?: string };
type Project = { id: string; name: string; company_id: string; preparation_state: string; preparation_diagnostic?: string; snapshot_fingerprint?: string; artifacts: Artifact[] };
type Catalog = { companies: Company[]; projects: Project[] };
type RoadmapTodo = { id: string; title: string; path: string; kind: "executable" | "planning"; record_state: string; local_stage: "Pending" | "Local-Implemented" | "Lane-Promoted" | "Production-Ready" | "unknown"; work_state?: "blocked" | "unknown" };
type RoadmapRelease = { id: string; name: string; description: string; todos: RoadmapTodo[] };
type RoadmapEpic = { id: string; name: string; description: string; releases: RoadmapRelease[] };
type RoadmapProjection = { schema_version: string; project_id: string; focus_release_id: string; epics: RoadmapEpic[] };
type ReleaseStatus = "delivered" | "blocked" | "in-progress" | "planned" | "unknown";
const releaseStatusLabel: Record<ReleaseStatus, string> = { delivered: "Entregue", blocked: "Bloqueada", "in-progress": "Em andamento", planned: "Planejada", unknown: "Status indisponível" };
function releaseStatus(release: RoadmapRelease): ReleaseStatus {
  const todos = release.todos.filter((todo) => todo.kind === "executable");
  if (todos.length === 0) return "unknown";
  if (todos.some((todo) => todo.work_state === "blocked")) return "blocked";
  if (todos.some((todo) => todo.local_stage === "unknown")) return "unknown";
  if (todos.every((todo) => todo.local_stage === "Production-Ready")) return "delivered";
  if (todos.some((todo) => todo.local_stage !== "Pending")) return "in-progress";
  return "planned";
}
function initialRelease(epic?: RoadmapEpic): RoadmapRelease | undefined {
  return epic?.releases.find((release) => releaseStatus(release) !== "delivered") || epic?.releases.at(-1);
}
type OverviewSection = { heading: string; body: string; points: string[]; image_alt?: string };
type OverviewSource = { schema_version: string; tagline: string; summary: string; desktop_alt?: string; mobile_alt?: string; sections: Record<"problem" | "proposal" | "impact" | "audience", OverviewSection>; media?: Record<string, string> };
type TargetObservation = { status?: unknown; reason_code?: unknown };
type KnowledgeObservation = { landing?: TargetObservation; roadmap?: TargetObservation };
type VisualOriginObservation = { state?: unknown; knowledge_source_state?: unknown; roadmap_source_state?: unknown; design_system_state?: unknown };
type SnapshotObservations = { knowledge_status?: KnowledgeObservation; visual_origin?: VisualOriginObservation };
type Detail = { consumer_readiness: string; company_id?: string; project_id?: string; snapshot_fingerprint?: string; snapshot_observed_at?: string; artifacts: Artifact[]; overview?: OverviewSource; roadmap?: RoadmapProjection; snapshot_observations?: SnapshotObservations; portfolio_summary?: string; portfolio_cover_url?: string; documents?: { path: string; url: string }[]; diagnostic?: string };
type Page = "overview" | "documents" | "design-system" | "prototypes" | "roadmap" | "release" | "todo";
type Route = { companyID: string; projectID: string; page: string; pageExplicit: boolean; epicID: string; releaseID: string; todoID: string; prototype: PrototypeSelection };
type ProjectView = { key: string; detail: Detail; theme?: ProjectTheme; themeDiagnostic?: string };

const pages: { id: Exclude<Page, "release" | "todo">; label: string }[] = [
  { id: "overview", label: "Overview" }, { id: "roadmap", label: "Roadmap" }, { id: "documents", label: "Documents / decisions" },
  { id: "design-system", label: "Design System" }, { id: "prototypes", label: "Prototype" },
];
const pageDisplayNames: Record<Page, string> = { overview: "Overview", roadmap: "Roadmap", release: "Release", todo: "TODO", documents: "Documents", "design-system": "Design System", prototypes: "Prototypes" };
const prototypeKeys = ["artifact", "prototype_id", "screen_id", "state_id", "scenario_id", "scenario_step", "fingerprint", "device", "scale", "mode"];
const roadmapKeys = ["epic_id", "release_id", "todo_id"];

function route(): Route {
  const q = new URLSearchParams(location.search);
  return {
    companyID: q.get("company_id") || "", projectID: q.get("project_id") || "",
    page: q.get("page") || "overview", pageExplicit: q.has("page"), epicID: q.get("epic_id") || "", releaseID: q.get("release_id") || "", todoID: q.get("todo_id") || "",
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
  if (contextChanged) { url.searchParams.delete("page"); [...prototypeKeys, ...roadmapKeys].forEach((key) => url.searchParams.delete(key)); }
  else if (pageChanged) [...prototypeKeys, ...roadmapKeys].forEach((key) => url.searchParams.delete(key));
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

function ReleaseDetailView({ roadmap, projectID, epicID, releaseID }: { roadmap?: RoadmapProjection; projectID: string; epicID: string; releaseID: string }) {
  const epic = roadmap?.project_id === projectID && roadmap.schema_version === "builder-roadmap-v1"
    ? roadmap.epics.find((item) => item.id === epicID && item.releases.some((release) => release.id === releaseID)) : undefined;
  const release = epic?.releases.find((item) => item.id === releaseID);
  if (!epic || !release) return <section className="project-notice" role="alert"><h1>Release indisponível</h1><p>Esta Release não existe no Roadmap salvo deste projeto.</p><button onClick={() => navigate({ page: "roadmap", epic_id: null, release_id: null })}>Voltar ao Roadmap</button></section>;
  const executable = release.todos.filter((todo) => todo.kind === "executable");
  const status = releaseStatus(release);
  const back = () => navigate({ page: "roadmap", epic_id: epic.id, release_id: release.id });
  return <article className="release-page" aria-label="Detalhe da Release">
    <button className="release-back" onClick={back}><span aria-hidden="true">←</span> Voltar ao Roadmap</button>
    <nav className="release-breadcrumb" aria-label="Localização"><button onClick={back}>Roadmap</button><span aria-hidden="true">›</span><span>{epic.name}</span><span aria-hidden="true">›</span><span aria-current="page">{release.name}</span></nav>
    <header className="release-hero"><div><span className="release-kicker">RELEASE · {release.id}</span><h1>{release.name}</h1>{release.description && <p>{release.description}</p>}</div><span className={`release-badge ${status}`}><span aria-hidden="true">◉</span> {releaseStatusLabel[status]}</span></header>
    <section className="release-context-strip without-investigation" aria-label="Contexto da release">
      <div className="release-context-epic"><span className="release-context-icon" aria-hidden="true">◉</span><div><small>Épico</small><strong>{epic.name}</strong>{epic.description && <span>{epic.description}</span>}</div></div>
    </section>
    <section className="release-work" aria-labelledby="release-work-title"><div className="release-work-heading"><div><h2 id="release-work-title">TODOs executáveis</h2><p>Trabalho agrupado nesta release.</p></div></div>
      {executable.length ? <div className="release-work-list" role="list">{executable.map((todo) => {
        const stage = todo.work_state === "blocked" ? "blocked" : todo.local_stage === "Production-Ready" ? "delivered" : todo.local_stage === "Lane-Promoted" ? "review" : todo.local_stage === "Local-Implemented" ? "active" : todo.local_stage === "Pending" ? "planned" : "unknown";
        const label = todo.work_state === "blocked" ? "Bloqueado" : todo.local_stage === "Production-Ready" ? "Entregue" : todo.local_stage === "Lane-Promoted" ? "Em promoção" : todo.local_stage === "Local-Implemented" ? "Implementado localmente" : todo.local_stage === "Pending" ? "Pendente" : "Status indisponível";
        return <article className="release-work-row" role="listitem" key={todo.id}><span className="release-work-icon" aria-hidden="true">▤</span><h3><button className="release-todo-link" onClick={() => navigate({ page: "todo", epic_id: epic.id, release_id: release.id, todo_id: todo.id })}>{todo.title} <span aria-hidden="true">›</span></button></h3><span className="release-work-id">{todo.id}</span><span className={`release-work-status ${stage}`}><i aria-hidden="true" />{label}</span></article>;
      })}</div> : <p className="project-notice">Nenhum TODO executável nesta Release.</p>}
    </section>
  </article>;
}

function authoredSection(source: string, heading: string) {
  const start = source.search(new RegExp(`^## ${heading}\\s*$`, "m"));
  if (start < 0) return "";
  const body = source.slice(start).replace(/^## [^\n]*\n?/, "");
  const end = body.search(/^## /m);
  return (end < 0 ? body : body.slice(0, end)).trim();
}

function authoredField(source: string, label: string) {
  const match = source.match(new RegExp(`^- \\*\\*${label}:\\*\\*\\s*(.+)$`, "m"));
  return match?.[1]?.trim() || "";
}

function savedObservationStatus(value: unknown, known: Record<string, string>) {
  return typeof value === "string" && Object.hasOwn(known, value) ? known[value] : "Unavailable";
}

function savedObservationTime(value?: string) {
  const parts = value?.match(/^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.(\d{1,9}))?Z$/);
  if (!value || !parts) return "Observation time unavailable";
  const date = new Date(value);
  if (Number.isNaN(date.valueOf()) || date.getUTCFullYear() !== Number(parts[1]) || date.getUTCMonth() + 1 !== Number(parts[2]) ||
    date.getUTCDate() !== Number(parts[3]) || date.getUTCHours() !== Number(parts[4]) || date.getUTCMinutes() !== Number(parts[5]) ||
    date.getUTCSeconds() !== Number(parts[6])) return "Observation time unavailable";
  return value;
}

function SnapshotStatus({ detail }: { detail: Detail }) {
  const knowledge = detail.snapshot_observations?.knowledge_status;
  const visual = detail.snapshot_observations?.visual_origin;
  const reviewLabels = { current: "Current against reviewed source", review_required: "Review required", unverifiable: "Unverifiable" };
  const originLabels = { matching: "Matches saved source observations", mismatch: "Mismatch", unverifiable: "Unverifiable" };
  const observedAt = savedObservationTime(detail.snapshot_observed_at);
  const timestampAvailable = observedAt !== "Observation time unavailable";
  const landing = timestampAvailable ? savedObservationStatus(knowledge?.landing?.status, reviewLabels) : "Unavailable";
  const roadmap = timestampAvailable ? savedObservationStatus(knowledge?.roadmap?.status, reviewLabels) : "Unavailable";
  const origin = timestampAvailable ? savedObservationStatus(visual?.state, originLabels) : "Unavailable";
  const needsAttention = landing !== reviewLabels.current || roadmap !== reviewLabels.current || origin !== originLabels.matching;
  const reason = (value?: unknown) => typeof value === "string" && value ? ` (${value.replaceAll("_", " ")})` : "";
  return <section className="overview-status" aria-labelledby="snapshot-status-title">
    <div className="overview-status-heading"><div><span className="project-page-tag">Saved snapshot integrity: Verified</span><h2 id="snapshot-status-title">Snapshot status</h2></div><span>Observations saved {observedAt}</span></div>
    <dl className="overview-status-grid">
      <div><dt>Landing review</dt><dd>{landing}{landing !== "Unavailable" && reason(knowledge?.landing?.reason_code)}</dd></div>
      <div><dt>Roadmap review</dt><dd>{roadmap}{roadmap !== "Unavailable" && reason(knowledge?.roadmap?.reason_code)}</dd></div>
      <div><dt>Visual origin</dt><dd>{origin}</dd></div>
    </dl>
    <p className="overview-status-note">These are saved observations from the registration snapshot. They do not check current Foundation sources live or establish visual approval.</p>
    {needsAttention && <p className="overview-status-next" role="status">TEACH: Check the selected Project’s source through Delphi. After correcting it and receiving explicit authorization, refresh that Project’s matching registration to capture new observations.</p>}
  </section>;
}

function TodoDetailView({ detail, projectID, epicID, releaseID, todoID }: { detail: Detail; projectID: string; epicID: string; releaseID: string; todoID: string }) {
  const matches: { epic: RoadmapEpic; release: RoadmapRelease; todo: RoadmapTodo }[] = [];
  if (detail.roadmap?.project_id === projectID && detail.roadmap.schema_version === "builder-roadmap-v1") {
    for (const epic of detail.roadmap.epics) for (const release of epic.releases) for (const todo of release.todos) {
      if (todo.id === todoID && todo.kind === "executable") matches.push({ epic, release, todo });
    }
  }
  const selected = matches.length === 1 && (!epicID || matches[0].epic.id === epicID) && (!releaseID || matches[0].release.id === releaseID) ? matches[0] : null;
  const admitted = detail.artifacts.find((item) => item.kind === "roadmap" && item.state === "available")?.files?.includes(selected?.todo.path || "") || false;
  const [source, setSource] = useState("");
  const [state, setState] = useState<"loading" | "ready" | "error">("loading");
  useEffect(() => {
    setSource(""); setState("loading");
    if (!selected || !admitted || !detail.snapshot_fingerprint) { setState("error"); return; }
    const controller = new AbortController();
    fetch(snapshotFileURL(detail, selected.todo.path), { signal: controller.signal }).then(async (response) => {
      if (!response.ok || response.headers.get("X-Builder-Snapshot-Fingerprint") !== detail.snapshot_fingerprint) throw new Error("TODO snapshot mismatch");
      return response.text();
    }).then((value) => {
      const heading = value.match(/^# (.+)$/m)?.[1]?.trim();
      if (heading !== selected.todo.title) throw new Error("TODO title mismatch");
      if (!controller.signal.aborted) { setSource(value); setState("ready"); }
    }).catch(() => { if (!controller.signal.aborted) setState("error"); });
    return () => controller.abort();
  }, [detail, selected?.todo.path, selected?.todo.title, admitted]);
  if (!selected) return <section className="project-notice" role="alert"><h1>TODO indisponível</h1><p>Este TODO não pertence à Release executável deste projeto.</p><button onClick={() => navigate({ page: "roadmap" })}>Voltar ao Roadmap</button></section>;
  const back = () => navigate({ page: "release", epic_id: selected.epic.id, release_id: selected.release.id });
  const objective = authoredSection(source, "Objective");
  const scope = authoredSection(source, "Scope");
  const completion = authoredSection(source, "Definition of Done");
  const nextStep = authoredField(source, "Next exact step");
  const primaryModule = authoredField(source, "Primary module");
  return <article className="todo-page" aria-label="Detalhe do TODO">
    <nav className="todo-breadcrumb" aria-label="Localização"><button onClick={() => navigate({ page: "roadmap", epic_id: selected.epic.id, release_id: selected.release.id })}>Roadmap</button><span aria-hidden="true">›</span><span>{selected.epic.name}</span><span aria-hidden="true">›</span><button onClick={back}>{selected.release.name}</button><span aria-hidden="true">›</span><span aria-current="page">TODO</span></nav>
    <header className="todo-hero"><h1>{selected.todo.title}</h1><div className="todo-hero-meta"><span className={`todo-stage ${selected.todo.work_state === "blocked" ? "blocked" : selected.todo.local_stage === "Production-Ready" ? "delivered" : ""}`}><i aria-hidden="true" />{selected.todo.work_state === "blocked" ? "Blocked · " : ""}{selected.todo.local_stage}</span><span className="todo-source-label">Fonte: TODO Markdown</span></div></header>
    {state === "loading" && <p className="project-notice" role="status">Carregando TODO da Foundation…</p>}
    {state === "error" && <p className="project-notice" role="alert">O Markdown admitido deste TODO não está disponível ou não corresponde ao snapshot do projeto.</p>}
    {state === "ready" && <>
      {objective && <section className="todo-objective" aria-labelledby="todo-objective-title"><span className="todo-icon" aria-hidden="true">◎</span><div><h2 id="todo-objective-title">Objective</h2><div className="todo-markdown"><ReactMarkdown remarkPlugins={[remarkGfm]} components={{ img: () => null }}>{objective}</ReactMarkdown></div></div></section>}
      <section className="todo-facts" aria-label="Contexto do TODO"><div><span className="todo-icon" aria-hidden="true">⌁</span><span><small>Current delivery stage</small><strong>{selected.todo.local_stage}</strong></span></div>{nextStep && <div><span className="todo-icon" aria-hidden="true">➜</span><span><small>Next exact step</small><div className="todo-markdown"><ReactMarkdown remarkPlugins={[remarkGfm]} components={{ img: () => null }}>{nextStep}</ReactMarkdown></div></span></div>}<div><span className="todo-icon" aria-hidden="true">◇</span><span><small>Release</small><strong>{selected.release.name}</strong></span></div></section>
      {(scope || completion || primaryModule) && <div className="todo-columns"><section className="todo-panel todo-work" aria-label="Contrato de trabalho">{scope && <><h2><span className="todo-icon" aria-hidden="true">☷</span>Scope</h2><div className="todo-markdown"><ReactMarkdown remarkPlugins={[remarkGfm]} components={{ img: () => null }}>{scope}</ReactMarkdown></div></>}{scope && completion && <hr />}{completion && <><h2><span className="todo-icon" aria-hidden="true">✓</span>Definition of Done</h2><div className="todo-markdown"><ReactMarkdown remarkPlugins={[remarkGfm]} components={{ img: () => null }}>{completion}</ReactMarkdown></div></>}</section><aside className="todo-panel todo-relations" aria-label="Fonte e relações"><h2><span className="todo-icon" aria-hidden="true">▤</span>Source</h2><p><code>{selected.todo.path}</code></p>{primaryModule && <><h2><span className="todo-icon" aria-hidden="true">◇</span>Primary module</h2><p><code>{primaryModule.replace(/^`|`$/g, "")}</code></p></>}</aside></div>}
      <details className="todo-source"><summary><span className="todo-icon" aria-hidden="true">▤</span>Contrato completo · abrir Markdown original <span aria-hidden="true">⌄</span></summary><pre>{source}</pre></details>
    </>}
  </article>;
}

function OverviewEditorial({ detail, projectName }: { detail: Detail; projectName: string }) {
  const overview = detail.overview;
  if (!overview || overview.schema_version !== "1") return <section className="project-notice" role="status">
    <h2>Project Overview not available yet</h2><p>{detail.artifacts.find((item) => item.kind === "overview")?.diagnostic || "Create project_overview.json in this Project's Foundation, validate it, then refresh the saved registration."}</p>
  </section>;
  const sections = ["problem", "proposal", "impact", "audience"] as const;
  const labels = { problem: "Problema", proposal: "Proposta", impact: "Impacto", audience: "Para quem" };
  return <>
    <section className="project-hero overview-hero" aria-label="Project introduction">
      <div className="project-hero-copy"><span className="overview-eyebrow">VISÃO DO PROJETO</span><h1>{projectName}</h1><h2 className="overview-hero-tagline">{overview.tagline}</h2><p className="overview-hero-summary">{overview.summary}</p>{(() => { const landing = detail.artifacts.find((item) => item.kind === "landing" && item.state === "available"); const href = landing ? artifactURL(detail, landing) : ""; return href ? <a className="overview-landing-link" href={href} target="_blank" rel="noreferrer">Ver landing independente <span aria-hidden="true">→</span></a> : null; })()}</div>
      {(overview.media?.desktop && overview.media?.mobile) ? <div className="overview-device-pair" aria-label="Project preview images">
        <span className="overview-visual-note">CONCEITO · DADOS ILUSTRATIVOS</span>
        {overview.media.desktop && <img className="overview-desktop" src={overview.media.desktop} alt={overview.desktop_alt || ""} />}
        {overview.media.mobile && <img className="overview-mobile" src={overview.media.mobile} alt={overview.mobile_alt || ""} />}
      </div> : <p className="project-notice" role="alert">Hero images are missing from the saved Overview snapshot. Create desktop.png and mobile.png, then refresh this Project registration.</p>}
    </section>
    <div className="overview-editorial" aria-label="Project at a glance">{sections.map((key) => {
      const section = overview.sections[key];
      return <section className={`overview-editorial-section overview-editorial-${key}`} key={key}>
        <div className="overview-section-copy"><div className="overview-section-heading"><span>{labels[key].toUpperCase()}</span><h2>{section.heading}</h2></div><div className="overview-section-body"><p>{section.body}</p>{(key === "proposal" || key === "audience") && section.points.length > 0 && <ul>{section.points.map((point) => <li key={point}>{point}</li>)}</ul>}</div></div>
        {(key === "problem" || key === "impact") && section.points.length > 0 && <div className="overview-section-aside">{key === "problem" && <strong>Consequências no dia a dia:</strong>}<ul>{section.points.map((point) => <li key={point}>{point}</li>)}</ul></div>}
        {overview.media?.[key] && <img src={overview.media[key]} alt={section.image_alt || ""} />}
      </section>;
    })}</div>
  </>;
}

function useDraggableRail() {
  const drag = useRef({ startX: 0, startScroll: 0, active: false, moved: false });
  return {
    onMouseDown(event: ReactMouseEvent<HTMLDivElement>) {
      if (event.button !== 0) return;
      drag.current = { startX: event.clientX, startScroll: event.currentTarget.scrollLeft, active: true, moved: false };
    },
    onMouseMove(event: ReactMouseEvent<HTMLDivElement>) {
      if (!drag.current.active || event.buttons !== 1) return;
      const distance = event.clientX - drag.current.startX;
      if (Math.abs(distance) < 5 && !drag.current.moved) return;
      drag.current.moved = true;
      event.preventDefault();
      event.currentTarget.scrollLeft = drag.current.startScroll - distance;
    },
    onMouseUp() { drag.current.active = false; },
    onMouseLeave() { drag.current.active = false; },
    onClickCapture(event: ReactMouseEvent<HTMLDivElement>) {
      if (!drag.current.moved) return;
      event.preventDefault();
      event.stopPropagation();
      drag.current.moved = false;
    },
  };
}

function RoadmapView({ roadmap, detail, projectID, epicID, releaseID, compact = false }: { roadmap?: RoadmapProjection; detail: Detail; projectID: string; epicID: string; releaseID: string; compact?: boolean }) {
  const roadmapArtifact = detail.artifacts.find((item) => item.kind === "roadmap");
  const epics = roadmap?.epics || [];
  const requestedReleaseEpic = releaseID ? epics.find((epic) => epic.releases.some((release) => release.id === releaseID)) : undefined;
  const invalidFocus = (releaseID && !requestedReleaseEpic) || (epicID && !epics.some((epic) => epic.id === epicID)) || (epicID && requestedReleaseEpic && epicID !== requestedReleaseEpic.id);
  const selectedEpic = invalidFocus ? undefined : requestedReleaseEpic || epics.find((epic) => epic.id === epicID) || epics.find((epic) => epic.releases.some((release) => releaseStatus(release) !== "delivered")) || epics.find((epic) => epic.releases.length > 0) || epics[0];
  const focusedRelease = releaseID || initialRelease(selectedEpic)?.id || "";
  const selectedEpicID = selectedEpic?.id || "";
  const focusedReleaseStatus = selectedEpic?.releases.find((release) => release.id === focusedRelease);
  const initiallyExpanded = focusedRelease && focusedReleaseStatus && releaseStatus(focusedReleaseStatus) !== "delivered" ? [focusedRelease] : [];
  const [expandedReleaseIDs, setExpandedReleaseIDs] = useState<string[]>(initiallyExpanded);
  const epicRail = useRef<HTMLDivElement>(null);
  const releaseRail = useRef<HTMLDivElement>(null);
  const epicDrag = useDraggableRail();
  const releaseDrag = useDraggableRail();
  useEffect(() => { setExpandedReleaseIDs(initiallyExpanded); }, [focusedRelease, roadmap]);
  useEffect(() => {
    const rail = epicRail.current;
    const selected = rail?.querySelector<HTMLButtonElement>(".selected");
    if (!rail || !selected) return;
    const railBounds = rail.getBoundingClientRect();
    const selectedBounds = selected.getBoundingClientRect();
    if (selectedBounds.left < railBounds.left) {
      rail.scrollLeft -= railBounds.left - selectedBounds.left;
    } else if (selectedBounds.right > railBounds.right) {
      rail.scrollLeft += selectedBounds.right - railBounds.right;
    }
  }, [selectedEpicID]);
  useLayoutEffect(() => {
    if (!compact) return;
    const rail = releaseRail.current;
    const focused = rail?.querySelector<HTMLElement>(".focused");
    if (!rail || !focused) return;
    const railBounds = rail.getBoundingClientRect();
    const focusedBounds = focused.getBoundingClientRect();
    if (focusedBounds.left < railBounds.left || focusedBounds.right > railBounds.right) {
      rail.style.scrollBehavior = "auto";
      rail.scrollLeft += focusedBounds.left - railBounds.left;
      rail.style.scrollBehavior = "";
    }
  }, [compact, selectedEpicID, focusedRelease]);
  if (!roadmap || roadmap.project_id !== projectID || roadmap.schema_version !== "builder-roadmap-v1") {
    return <p className="project-notice roadmap-unavailable" role={roadmapArtifact?.state === "invalid" ? "alert" : "status"}>
      {roadmapArtifact?.diagnostic || "Project Roadmap data is not available in this saved snapshot."}
    </p>;
  }
  if (invalidFocus) return <p className="project-notice roadmap-unavailable" role="alert">This Epic or Release link is invalid for this Project. Remove epic_id and release_id to return to the current Roadmap focus.</p>;
  const selectedEpicIndex = Math.max(0, epics.findIndex((epic) => epic.id === selectedEpic?.id));
  const releases = selectedEpic?.releases || [];
  const focusName = selectedEpic?.releases.find((release) => release.id === focusedRelease)?.name;
  const selectEpic = (epic?: RoadmapEpic) => { if (epic) navigate({ epic_id: epic.id, release_id: initialRelease(epic)?.id || null }); };
  const selectRelease = (release: RoadmapRelease) => navigate({ epic_id: selectedEpicID, release_id: release.id });
  return <section className={`roadmap-view${compact ? " roadmap-view-compact" : " roadmap-screen"}`} aria-label={compact ? "Project Roadmap overview" : "Project Roadmap"}>
    {!compact && <header className="roadmap-heading"><div><p className="roadmap-eyebrow">PLANEJAMENTO DO PROJETO</p><h1>Roadmap</h1><p className="roadmap-subtitle">Visão estratégica do projeto em releases.</p></div><div className="roadmap-heading-aside"><span>Da ideia ao impacto, um plano.</span><div className="roadmap-progress-track"><i /></div><div className="roadmap-heading-controls"><button type="button" aria-label="Previous Epic" disabled={selectedEpicIndex === 0} onClick={() => selectEpic(epics[selectedEpicIndex - 1])}>‹</button><button type="button" aria-label="Next Epic" disabled={selectedEpicIndex >= epics.length - 1} onClick={() => selectEpic(epics[selectedEpicIndex + 1])}>›</button></div></div></header>}
    {compact && <header className="roadmap-journey-heading"><div><span>JORNADA ESTRATÉGICA DO PROJETO</span><p>Do plano aos próximos marcos do projeto.</p></div><small>Arraste para explorar ↔</small></header>}
    <div className="roadmap-rail-group">
      <div className={compact ? "roadmap-rail-frame" : "epic-viewport"}>
        {compact && <button className="roadmap-rail-arrow" type="button" aria-label="Previous Epic" disabled={selectedEpicIndex === 0} onClick={() => selectEpic(epics[selectedEpicIndex - 1])}>‹</button>}
        <div className={`roadmap-rail roadmap-epic-rail${compact ? "" : " epic-rail"}`} ref={epicRail} role="group" aria-label="Select Epic" {...epicDrag}>
        {epics.map((epic) => <button key={epic.id} className={`${compact ? "" : "epic-card "}${epic.id === selectedEpic?.id ? "selected" : ""}`} aria-pressed={epic.id === selectedEpic?.id} onClick={() => selectEpic(epic)}>
          {compact ? <><i className={`roadmap-journey-status${epic.id === selectedEpic?.id ? " active" : ""}`} aria-hidden="true" /><span>Épico</span><strong>{epic.name}</strong><small>{epic.releases.some((release) => release.id === focusedRelease) ? "Foco atual" : "No plano"}</small></> : <><span className="epic-top"><span className="epic-symbol" aria-hidden="true">{epic.releases.some((release) => release.id === focusedRelease) ? "◉" : "◷"}</span><span><span className="epic-title">{epic.name}</span><span className="epic-status">{epic.releases.some((release) => release.id === focusedRelease) ? "Foco atual" : "No plano"}</span></span></span><span className="epic-description">{epic.description}</span><span className="release-count">{epic.releases.length} {epic.releases.length === 1 ? "release" : "releases"}</span></>}
        </button>)}
        </div>
        {compact && <button className="roadmap-rail-arrow" type="button" aria-label="Next Epic" disabled={selectedEpicIndex >= epics.length - 1} onClick={() => selectEpic(epics[selectedEpicIndex + 1])}>›</button>}
      </div>
    </div>
    {compact && <div className="roadmap-rail-group roadmap-releases-rail">
      <h3>RELEASES DO ÉPICO {selectedEpic?.name.toLocaleUpperCase("pt-BR")}</h3><p>Marcos deste épico.</p>
      <div className="roadmap-rail-frame">
      <button className="roadmap-rail-arrow" type="button" aria-label="Previous Release" onClick={(event) => { const rail = event.currentTarget.nextElementSibling as HTMLDivElement; rail.scrollLeft -= Math.max(rail.clientWidth * .7, 180); }}>‹</button>
      <div className="roadmap-rail" ref={releaseRail} role="group" aria-label="Releases for selected Epic" {...releaseDrag}>
        {releases.map((release) => <button type="button" key={release.id} className={`roadmap-release-chip ${releaseStatus(release)}${release.id === focusedRelease ? " focused" : ""}`} aria-pressed={release.id === focusedRelease} onClick={() => selectRelease(release)}>
          <i className={`roadmap-journey-status ${releaseStatus(release)}${release.id === focusedRelease ? " active" : ""}`} aria-hidden="true" /><span>Release</span><strong>{release.name}</strong><small>{releaseStatusLabel[releaseStatus(release)]}</small>
        </button>)}
        {releases.length === 0 && <p className="roadmap-no-releases">No Releases defined yet.</p>}
      </div>
      <button className="roadmap-rail-arrow" type="button" aria-label="Next Release" onClick={(event) => { const rail = event.currentTarget.previousElementSibling as HTMLDivElement; rail.scrollLeft += Math.max(rail.clientWidth * .7, 180); }}>›</button>
      </div>
    </div>}
    {!compact && <div className="roadmap-full-body"><div className="roadmap-release-panel release-panel"><header className="roadmap-release-panel-heading release-heading"><div><span>RELEASES DO ÉPICO</span><h2>Releases de {selectedEpic?.name}</h2><p>{selectedEpic?.description}</p></div></header><div className="roadmap-release-list release-list" role="group" aria-label="Releases for selected Epic">
      {releases.map((release) => <section className={`roadmap-release release-card${release.id === focusedRelease ? " focused" : ""}${expandedReleaseIDs.includes(release.id) ? " open" : ""}`} key={release.id}>
        <button className="roadmap-release-toggle release-toggle" aria-expanded={expandedReleaseIDs.includes(release.id)} onClick={() => { if (release.id !== focusedRelease) selectRelease(release); else setExpandedReleaseIDs((current) => current.includes(release.id) ? current.filter((id) => id !== release.id) : [...current, release.id]); }}>
          <span className="roadmap-release-chevron chevron" aria-hidden="true">›</span><span className="roadmap-release-name release-name">{release.name}<span className="release-description">{release.description}</span></span>
          <span className={`roadmap-release-status release-status ${releaseStatus(release)}`}>{releaseStatusLabel[releaseStatus(release)]}</span>
        </button>
        <button className="roadmap-release-open" type="button" aria-label="Abrir Release" title={`Abrir ${release.name}`} onClick={() => navigate({ page: "release", epic_id: selectedEpicID, release_id: release.id })}><span aria-hidden="true">›</span></button>
        {expandedReleaseIDs.includes(release.id) && <div className="roadmap-release-details release-details"><div className="roadmap-table-scroll"><table className="roadmap-todo-table todo-table"><thead><tr><th>#</th><th>TODO</th><th>TIPO · REGISTRO</th><th>STATUS</th></tr></thead><tbody>{release.todos.map((todo, index) => <tr key={todo.id}>
          <td>{String(index + 1).padStart(2, "0")}</td><td className="todo-name" title={todo.path}>{todo.kind === "executable" ? <button className="roadmap-todo-link" onClick={() => navigate({ page: "todo", epic_id: selectedEpicID, release_id: release.id, todo_id: todo.id })}>▤ &nbsp;{todo.title}</button> : <>▤ &nbsp;{todo.title}</>}</td><td><span className="roadmap-todo-meta">{todo.kind === "planning" ? "Planning ledger" : "Executable TODO"}<small>{todo.record_state === "completed" ? "Local record: completed" : todo.record_state === "active" ? "Local record: active" : "Local record: other"}</small></span></td><td>{todo.kind === "planning" ? <span className="roadmap-todo-stage">—</span> : <span className={`roadmap-todo-stage todo-status${todo.work_state === "blocked" ? " blocked" : todo.local_stage === "Local-Implemented" ? " doing" : todo.local_stage === "Lane-Promoted" ? " validation" : todo.local_stage === "Production-Ready" ? " done" : ""}`}>{todo.work_state === "blocked" ? "Blocked · " : ""}{todo.local_stage || "unknown"}</span>}</td>
        </tr>)}</tbody></table></div></div>}
      </section>)}
      {releases.length === 0 && <p className="project-notice">No Releases defined yet.</p>}
    </div></div><aside className="roadmap-context"><h2>Contexto do projeto</h2><div><span>▤</span><p><small>Release em foco</small><strong>{focusName || "Sem Release neste épico"}</strong>Seleção de navegação, não conclusão.</p></div><nav className="roadmap-context-links" aria-label="Roadmap useful links"><h3>Links úteis</h3><button type="button" onClick={() => navigate({ page: "overview" })}>◉ <span>Visão do produto</span><b aria-hidden="true">↗</b></button><button type="button" onClick={() => navigate({ page: "documents" })}>▤ <span>Documentação</span><b aria-hidden="true">↗</b></button></nav></aside></div>}
  </section>;
}

function PortalHeader({ children }: { children: ReactNode }) {
  return <header className="projects-mast"><a href="/">Builder <span>Project directory</span></a>{children}</header>;
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
    { id: "border", label: "Border" },
    { id: "accent", label: "Accent" },
    { id: "focus", label: "Focus" },
  ] as const;
  const value = (role: string) => theme[`--project-${role}`];
  return <div className="design-system-view">{artifacts.map((artifact) => {
    const href = artifactURL(detail, artifact);
    if (!href) return <p className="project-notice" key={artifact.id}>{artifact.diagnostic || `${artifact.name} is ${stateLabel(artifact.state).toLowerCase()}.`}</p>;
    return <div key={artifact.id}>
      <section className="design-system-palette"><h2>Paleta estrutural</h2><p>Tokens visuais aplicados do Design System deste projeto.</p><div className="design-system-swatches">{palette.map((item) => <div className="design-system-swatch" key={item.id} style={{ backgroundColor: value(item.id), color: item.id === "accent" || item.id === "focus" || item.id === "border" ? value("canvas") : value("text") }}><strong>{item.label}</strong><code>{value(item.id)}</code></div>)}</div></section>
      <div className="design-system-cards">
        <article><h2>Hierarquia</h2><p className="design-system-type-sample">Clareza para decidir.</p><p>Títulos, contexto e conteúdo seguem os tokens tipográficos deste projeto: {value("displaySize")} / {value("bodySize")}.</p></article>
        <article><h2>Componentes</h2><div className="design-system-controls"><button disabled>Primário</button><button disabled>Secundário</button><span className="project-page-tag">STATUS · DISPONÍVEL</span></div></article>
        <article><h2>Princípio</h2><p>Cor de destaque orienta ação e foco; superfícies organizam a informação sem competir com ela.</p><p>Raio dos cartões: {value("cardRadius")}.</p></article>
      </div>
      <section className="design-system-source"><div><h2>Fonte autorada</h2><p>O catálogo completo de {artifact.name} está no artefato admitido deste projeto. Esta tela o apresenta, mas não o edita.</p></div><a href={href} target="_blank" rel="noreferrer">Abrir Design System ↗</a></section>
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
  const page = selection.page === "release" || selection.page === "todo" ? selection.page : pages.find((item) => item.id === selection.page)?.id;
  const available = detail?.artifacts || [];
  const invalidArtifacts = available.filter((item) => item.state === "invalid");

  if (catalogState === "loading") return <main className="projects-page"><p role="status">Loading registered Projects…</p></main>;
  if (catalogState === "error") return <main className="projects-page"><p role="alert">The local Project catalog is unavailable.</p></main>;
  if (selection.projectID && (!company || !project)) return <main className="projects-page"><p role="alert">This Company and Project selection is unavailable.</p><a href="/">Back to Companies</a></main>;
  if (selection.companyID && !company) return <main className="projects-page"><p role="alert">This Company is unavailable.</p><a href="/">Back to Companies</a></main>;
  if (selection.projectID && !page) return <main className="projects-page"><p role="alert">This Project page is unavailable.</p><a href={`/?company_id=${encodeURIComponent(selection.companyID)}&project_id=${encodeURIComponent(selection.projectID)}`}>Back to Overview</a></main>;
  if (selection.projectID && selection.prototype.artifact && !selection.pageExplicit && !selection.prototype.mode) return <main className="projects-page"><p role="alert">This artifact-only link is incomplete. Open the Prototype page.</p><a href={`/?company_id=${encodeURIComponent(selection.companyID)}&project_id=${encodeURIComponent(selection.projectID)}&page=prototypes`}>Open Prototype</a></main>;
  if (selection.prototype.mode && (selection.prototype.mode !== "presentation" || page !== "prototypes" || !selection.projectID)) return <main className="projects-page"><p role="alert">This presentation link is invalid.</p><a href="/">Back to Companies</a></main>;

  if (!selection.companyID) return <main className="projects-page projects-portal"><PortalHeader><p>Registered Companies and their Projects</p></PortalHeader><div className="portal-main"><p className="projects-eyebrow">01 / Project directory</p><h1>Choose a company to explore its projects.</h1><p className="portal-intro">Select a registered company to view its Projects and their available snapshot information.</p><section className="company-grid">{catalog.companies.map((item) => { const count = catalog.projects.filter((p) => p.company_id === item.id).length; return <button key={item.id} className="company-card" onClick={() => navigate({ company_id: item.id, project_id: null, page: null })}><span className="company-card-mark" aria-hidden="true">{item.name.slice(0, 1)}</span><small>COMPANY</small><strong>{item.name}</strong><span className="company-card-count">{count} registered {count === 1 ? "Project" : "Projects"}</span><span className="company-card-cta">Open company portfolio <b aria-hidden="true">↗</b></span></button>; })}</section>{!catalog.companies.length && <p>No Companies are registered yet.</p>}</div></main>;
  if (!selection.projectID) return <main className="projects-page projects-portal"><PortalHeader><p>Company portfolio · registered Projects</p></PortalHeader><div className="portal-main"><a className="portal-breadcrumb" href="/">← All Companies</a><div className="portfolio-hero"><div><p className="projects-eyebrow">02 / {company?.name}</p><h1>Explore registered Projects for {company?.name}.</h1><p className="portal-intro">Open a registered Project to review its available artifacts and snapshot information.</p></div><aside>Registered Projects<strong>{projects.length}</strong></aside></div><section className="project-cards" aria-label={`${company?.name} Projects`}>{projects.map((item) => <ProjectCard key={`${item.company_id}/${item.id}`} project={item} onOpen={() => navigate({ project_id: item.id, page: "overview" })} />)}</section>{!projects.length && <p>No Projects are registered under this Company yet.</p>}</div></main>;

  if (!activeView && viewState !== "error") return <main className="projects-page"><p role="status">Loading the saved Project snapshot and Design System…</p></main>;
  if (viewState === "error" || !detail) return <main className="projects-page"><p role="alert">The saved Project snapshot is unavailable or failed identity/integrity verification.</p><a href="/">Back to Companies</a></main>;
  if (detail.consumer_readiness !== "ready") return <main className="projects-page"><p role="alert">This Project is registered but its saved snapshot is not ready. {detail.diagnostic}</p><a href="/">Back to Companies</a></main>;
  if (!activeView?.theme) return <main className="projects-page"><p role="alert">{activeView?.themeDiagnostic || "The Project Design System cannot be applied."}</p><p>Project: {project?.name} · saved snapshot {detail.snapshot_fingerprint?.slice(0, 12)}</p><a href="/">Back to Companies</a></main>;

  const previewProps = { companyID: selection.companyID, projectID: selection.projectID, companyName: company?.name || "Company", projectName: project?.name || "Project", detail, selection: selection.prototype, onNavigate: navigate };
  if (selection.prototype.mode === "presentation") return <div className="project-theme" style={activeView.theme}><LocalArtifactPreview {...previewProps} /></div>;
  const tentativeProjects = catalog.projects.filter((item) => item.company_id === (pendingCompanyID || selection.companyID));
  return <main className="projects-page projects-project" style={activeView.theme}>
    <div className="project-bar"><span className="project-bar-context">CONTEXTO</span>
      <label>COMPANY<select aria-label="Switch Company" value={pendingCompanyID || selection.companyID} onChange={(event) => setPendingCompanyID(event.target.value)}><option value={selection.companyID}>{company?.name}</option>{catalog.companies.filter((item) => item.id !== selection.companyID).map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}</select></label>
      <label>PROJECT<select aria-label="Switch Project" value={pendingCompanyID ? "" : project?.id} onChange={(event) => { if (event.target.value) navigate({ company_id: pendingCompanyID || selection.companyID, project_id: event.target.value, page: "overview" }); }}>{pendingCompanyID && <option value="">Choose a Project</option>}{tentativeProjects.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}</select></label>
      {pendingCompanyID && <button className="project-switch-cancel" onClick={() => setPendingCompanyID("")}>Cancel</button>}
    </div>
    <header className="project-topbar"><a className="project-topbar-brand" href={`/?company_id=${encodeURIComponent(selection.companyID)}&project_id=${encodeURIComponent(selection.projectID)}&page=overview`}><span className="project-topbar-mark" aria-hidden="true">◆</span><span>Project {project?.name}</span></a></header>
    <div className="project-layout"><nav className="project-nav" aria-label="Project pages"><div className="project-nav-links">{pages.map((item) => <button key={item.id} className={page === item.id || ((page === "release" || page === "todo") && item.id === "roadmap") ? "active" : ""} aria-label={item.label} aria-current={page === item.id ? "page" : undefined} onClick={() => navigate({ page: item.id })}><span className="project-nav-icon" aria-hidden="true">{{overview:"⌂",roadmap:"▦",documents:"▤","design-system":"◈",prototypes:"▷"}[item.id]}</span>{pageDisplayNames[item.id]}</button>)}</div></nav>
      <section className="project-content">{page !== "overview" && page !== "roadmap" && page !== "release" && page !== "todo" && <header className="project-page-heading"><span className="project-page-tag">{page === "documents" ? "FOUNDATION DOCUMENTATION · SAVED SNAPSHOT" : page === "design-system" ? "BUILDER · IDENTIDADE DO PROJETO" : "BUILDER · PROTÓTIPOS DO PROJETO"}</span><h1>{page === "documents" ? "Documents & decisions" : pageDisplayNames[page as Page]}</h1>{page === "design-system" && <p>Uma linguagem visual aplicada a este projeto: cores, hierarquia e componentes derivados do seu Design System.</p>}{page === "prototypes" && <><p>Explore as telas e os estados de um protótipo registrado. A inspeção e a apresentação interativa são momentos distintos.</p><span className="prototype-page-note">Snapshot salvo · aprovação humana não inferida</span></>}</header>}
        {page === "overview" && <div className="project-overview"><OverviewEditorial detail={detail} projectName={project?.name || "Project"} />
          <SnapshotStatus detail={detail} />
          <RoadmapView key={viewKey} roadmap={detail.roadmap} detail={detail} projectID={project!.id} epicID={selection.epicID} releaseID={selection.releaseID} compact />
          {invalidArtifacts.length > 0 && <footer className="overview-status" aria-label="Unavailable artifacts">{invalidArtifacts.map((item) => <p className="project-note" key={`${item.kind}:${item.id}`}>{item.name}: {stateLabel(item.state)}{item.diagnostic ? ` · ${item.diagnostic}` : ""}</p>)}</footer>}</div>}
        {page === "documents" && <div className="documents-page"><p className="documents-page-intro">Read the canonical Markdown files admitted to this Project's saved snapshot.</p>
          {documents.length ? <section className="document-list" aria-label="Admitted Documents">{documents.map((item) => <article className="document-row" key={item.path}><div><h2>{documentLabel(item.path)}</h2><p>{item.path}</p></div><button aria-pressed={selectedDoc === item.url} onClick={() => setSelectedDoc(item.url)}>{selectedDoc === item.url ? "Selected" : "Read document"} <span aria-hidden="true">→</span></button></article>)}</section> : <p className="project-notice documents-empty">{available.find((item) => item.kind === "documents")?.diagnostic || "No canonical Markdown documents are admitted in this snapshot."}</p>}
          {selectedDoc && <article className="project-markdown document-reader">{docState === "loading" && <p role="status">Loading document…</p>}{docState === "error" && <p role="alert">This admitted document could not be loaded.</p>}{docState === "ready" && <>{headings.length > 0 && <nav className="document-headings" aria-label="Document headings">{headings.map((heading, index) => <button key={`${heading}-${index}`} onClick={() => document.querySelectorAll(".project-markdown h1, .project-markdown h2, .project-markdown h3")[index]?.scrollIntoView({ behavior: "smooth" })}>{heading}</button>)}</nav>}<ReactMarkdown components={{ img: () => null }}>{markdown}</ReactMarkdown></>}</article>}
          <section className="documents-note"><h2>Reading is not review</h2><p>This view reads files from the saved snapshot. Availability does not indicate human approval or current source freshness.</p></section>
        </div>}
        {page === "design-system" && activeView?.theme && <DesignSystemView artifacts={available.filter((item) => item.kind === "design_system")} detail={detail} theme={activeView.theme} />}
        {page === "prototypes" && <LocalArtifactPreview {...previewProps} />}
        {page === "roadmap" && <RoadmapView key={viewKey} roadmap={detail.roadmap} detail={detail} projectID={project!.id} epicID={selection.epicID} releaseID={selection.releaseID} />}
        {page === "release" && <ReleaseDetailView roadmap={detail.roadmap} projectID={project!.id} epicID={selection.epicID} releaseID={selection.releaseID} />}
        {page === "todo" && <TodoDetailView key={`${viewKey}/${selection.todoID}`} detail={detail} projectID={project!.id} epicID={selection.epicID} releaseID={selection.releaseID} todoID={selection.todoID} />}
      </section></div>
  </main>;
}
