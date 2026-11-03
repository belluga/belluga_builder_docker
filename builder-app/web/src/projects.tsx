import { useEffect, useLayoutEffect, useRef, useState } from "react";
import type { MouseEvent as ReactMouseEvent, ReactNode } from "react";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import { LocalArtifactPreview } from "./local-artifact-preview";
import type { PrototypeSelection } from "./local-artifact-preview";
import { resolveProjectTheme } from "./project-theme";
import type { ProjectTheme } from "./project-theme";
import { CanonicalDocuments } from "./canonical-documents";
import "./projects.css";
import "./release-detail.css";
import "./todo-detail.css";

type Company = { id: string; name: string };
type Artifact = { id: string; kind: string; name: string; state: string; entry_point?: string; files?: string[]; diagnostic?: string };
type Project = { id: string; name: string; company_id: string };
type Catalog = { companies: Company[]; projects: Project[] };
type DeliveryStage = "Planning" | "Implementation-Approval-Pending" | "Ready-for-Implementation" | "In-Progress" | "Local-Implemented" | "Promotion-Lane" | "Completed";
type RoadmapTodo = { id: string; title: string; path?: string; kind: "executable" | "planning"; todo_revision?: number; missing?: boolean; record_state: string; objective?: string; objective_state: "absent" | "complete" | "too_long"; next_step?: string; next_step_state: "absent" | "complete" | "too_long"; authored_delivery_stage: string; delivery_stage?: DeliveryStage; delivery_stage_state: "verified" | "unverified"; delivery_stage_teach?: string; historical_disposition?: "Superseded" | "Discarded"; disposition_reason?: string; successor_todo?: string; disposition_state?: "valid" | "invalid"; blocked: boolean; qualifier_state: "verified" | "unverified"; qualifier_teach?: string; work_state?: "blocked" | "unknown" };
type RoadmapRelease = { id: string; name: string; description: string; todos: RoadmapTodo[]; investigation?: { title: string } };
type ReleaseInvestigationRead = { company_id: string; project_id: string; release_id: string; state: "available" | "absent"; document?: { title: string; introduction: string; materials: { id: string; title: string; body_markdown: string; source_refs?: string[] }[] } };
type RoadmapEpic = { id: string; name: string; description: string; releases: RoadmapRelease[] };
type BacklogFolder = { name: string; path: string; description?: string; todos: RoadmapTodo[]; folders: BacklogFolder[] };
type RoadmapDiagnostic = { code: string; path?: string; message: string };
type RoadmapProjection = { schema_version: string; project_id: string; focus_release_id: string; epics: RoadmapEpic[]; backlog: { todos: RoadmapTodo[]; folders: BacklogFolder[] }; unclassified: { path: string; reason: string }[]; diagnostics: RoadmapDiagnostic[] };
type MongoTODOContentItem = { id: string; text: string };
type MongoTODORead = { company_id: string; project_id: string; todo_revision: number; content_revision?: number; content_digest?: string; todo: { id: string; project_id: string; revision: number; title: string; objective: string; next_exact_step: string; stage: string; content?: { schema_version?: number; context?: string; scope?: { included: string[]; excluded: string[] }; criteria?: MongoTODOContentItem[]; validation_scenarios?: { id: string; criterion_ids: string[]; method: string; expected: string }[]; implementation_plan?: MongoTODOContentItem[]; decisions?: MongoTODOContentItem[]; risks?: { id: string; text: string; mitigation: string }[]; references?: { kind: string; ref: string }[]; legacy_context?: { source_revision: string; section_position: number; ownership: string; title: string; body: string; digest: string }[]; architecture?: { decision: string; rationale: string; protection: string } }; sections: { key: string; title: string; body_markdown: string; position: number }[] } };
type ReleaseStatus = "delivered" | "blocked" | "in-progress" | "planned" | "unknown";
const releaseStatusLabel: Record<ReleaseStatus, string> = { delivered: "Entregue", blocked: "Bloqueada", "in-progress": "Em andamento", planned: "Planejada", unknown: "Status indisponível" };
const deliveryStageLabels: Record<DeliveryStage, string> = {
  Planning: "Planejamento",
  "Implementation-Approval-Pending": "Aguardando aprovação",
  "Ready-for-Implementation": "Pronto para implementar",
  "In-Progress": "Em andamento",
  "Local-Implemented": "Implementado localmente",
  "Promotion-Lane": "Em promoção",
  Completed: "Concluído",
};
const deliveryStageTone: Record<DeliveryStage, string> = {
  Planning: "planned",
  "Implementation-Approval-Pending": "approval",
  "Ready-for-Implementation": "ready",
  "In-Progress": "active",
  "Local-Implemented": "implemented",
  "Promotion-Lane": "validation",
  Completed: "done",
};
function deliveryStageLabel(todo: RoadmapTodo) {
  if (todo.disposition_state === "valid") return todo.historical_disposition === "Superseded" ? "Substituído" : "Descartado";
  return todo.delivery_stage_state === "verified" && todo.delivery_stage ? deliveryStageLabels[todo.delivery_stage] : "Status indisponível";
}
function deliveryStageClass(todo: RoadmapTodo) {
  if (todo.disposition_state === "valid") return "historical";
  return todo.delivery_stage_state === "verified" && todo.delivery_stage ? deliveryStageTone[todo.delivery_stage] : "unknown";
}
function todoStatusTeach(todo: RoadmapTodo) {
  return [todo.delivery_stage_state === "unverified" ? todo.delivery_stage_teach : "", todo.qualifier_state === "unverified" ? todo.qualifier_teach : ""].filter(Boolean).join(" ");
}
function isTODOReadable(todo: RoadmapTodo) {
  return todo.kind === "executable" || todo.todo_revision !== undefined;
}
function TodoStageStatus({ todo, className = "roadmap-todo-stage" }: { todo: RoadmapTodo; className?: string }) {
  const label = deliveryStageLabel(todo);
  const teach = todoStatusTeach(todo);
  return <><span className={`${className} ${deliveryStageClass(todo)}${todo.blocked ? " blocked" : ""}`}><i aria-hidden="true" />{todo.blocked ? `Bloqueado · ${label}` : label}</span>{teach && <small className="todo-stage-teach" role="note">{teach}</small>}</>;
}
function releaseStatus(release: RoadmapRelease): ReleaseStatus {
  const todos = release.todos.filter((todo) => todo.kind === "executable" && todo.disposition_state !== "valid");
  if (todos.length === 0) return "unknown";
  if (todos.some((todo) => todo.delivery_stage_state !== "verified" || !todo.delivery_stage || todo.qualifier_state !== "verified")) return "unknown";
  if (todos.some((todo) => todo.blocked)) return "blocked";
  if (todos.every((todo) => todo.delivery_stage === "Completed")) return "delivered";
  if (todos.some((todo) => ["In-Progress", "Local-Implemented", "Promotion-Lane", "Completed"].includes(todo.delivery_stage!))) return "in-progress";
  return "planned";
}
function initialRelease(epic?: RoadmapEpic): RoadmapRelease | undefined {
  return epic?.releases.find((release) => releaseStatus(release) !== "delivered") || epic?.releases.at(-1);
}
function TypedTODOContent({ content, legacySections }: { content: NonNullable<MongoTODORead["todo"]["content"]>; legacySections: MongoTODORead["todo"]["sections"] }) {
  const panels: ReactNode[] = [];
  if (content.context) panels.push(<section className="todo-panel" key="context"><h2>Context</h2><div className="todo-markdown"><ReactMarkdown remarkPlugins={[remarkGfm]}>{content.context}</ReactMarkdown></div></section>);
  if (content.scope) panels.push(<section className="todo-panel" key="scope"><h2>Scope</h2><h3>Included</h3><ul>{content.scope.included.map((value, index) => <li key={`included:${index}`}>{value}</li>)}</ul><h3>Excluded</h3><ul>{content.scope.excluded.map((value, index) => <li key={`excluded:${index}`}>{value}</li>)}</ul></section>);
  if (content.criteria?.length) panels.push(<section className="todo-panel" key="criteria"><h2>Acceptance criteria</h2><ul>{content.criteria.map((item) => <li key={item.id}><ReactMarkdown remarkPlugins={[remarkGfm]}>{item.text}</ReactMarkdown></li>)}</ul></section>);
  if (content.validation_scenarios?.length) panels.push(<section className="todo-panel" key="validation"><h2>Validation scenarios</h2>{content.validation_scenarios.map((scenario) => <div key={scenario.id}><h3>{scenario.id}</h3><p><strong>Method:</strong> {scenario.method}</p><p><strong>Expected:</strong> {scenario.expected}</p></div>)}</section>);
  if (content.implementation_plan?.length) panels.push(<section className="todo-panel" key="plan"><h2>Implementation plan</h2><ol>{content.implementation_plan.map((item) => <li key={item.id}>{item.text}</li>)}</ol></section>);
  if (content.decisions?.length) panels.push(<section className="todo-panel" key="decisions"><h2>Decisions</h2><ul>{content.decisions.map((item) => <li key={item.id}>{item.text}</li>)}</ul></section>);
  if (content.risks?.length) panels.push(<section className="todo-panel" key="risks"><h2>Risks</h2><ul>{content.risks.map((risk) => <li key={risk.id}><strong>{risk.text}</strong><p>{risk.mitigation}</p></li>)}</ul></section>);
  if (content.references?.length) panels.push(<section className="todo-panel" key="references"><h2>References</h2><ul>{content.references.map((reference, index) => <li key={`${reference.kind}:${index}`}><strong>{reference.kind}:</strong> {reference.ref}</li>)}</ul></section>);
  if (content.architecture) panels.push(<section className="todo-panel" key="architecture"><h2>Architecture decision</h2><p><strong>Decision:</strong> {content.architecture.decision}</p><p><strong>Rationale:</strong> {content.architecture.rationale}</p><p><strong>Protection:</strong> {content.architecture.protection}</p></section>);
  if (legacySections.length) panels.push(<section className="todo-panel" key="legacy-source"><h2>Historical v1 source</h2><details><summary>Show preserved v1 sections</summary>{legacySections.map((section) => <section key={`${section.position}:${section.key}`}><h3>{section.title}</h3><div className="todo-markdown"><ReactMarkdown remarkPlugins={[remarkGfm]}>{section.body_markdown}</ReactMarkdown></div></section>)}</details></section>);
  if (content.legacy_context?.length) panels.push(<section className="todo-panel" key="legacy"><h2>Preserved legacy sections</h2>{content.legacy_context.map((section) => <details key={`${section.source_revision}:${section.section_position}`}><summary>{section.title} · {section.ownership}</summary><div className="todo-markdown"><ReactMarkdown remarkPlugins={[remarkGfm]}>{section.body}</ReactMarkdown></div></details>)}</section>);
  return <>{panels}</>;
}
type OverviewSection = { heading: string; body: string; points: string[]; image_alt?: string };
type OverviewSource = { schema_version: string; tagline: string; summary: string; desktop_alt?: string; mobile_alt?: string; sections: Record<"problem" | "proposal" | "impact" | "audience", OverviewSection>; media?: Record<string, string> };
type TargetObservation = { status?: unknown; reason_code?: unknown };
type KnowledgeObservation = { schema_version?: string; landing?: TargetObservation; overview?: TargetObservation; roadmap?: TargetObservation };
type VisualOriginObservation = { state?: unknown; source_authority?: unknown; knowledge_source_state?: unknown; overview_source_state?: unknown; roadmap_source_state?: unknown; design_system_state?: unknown };
type SnapshotObservations = { knowledge_status?: KnowledgeObservation; visual_origin?: VisualOriginObservation };
type Detail = { consumer_readiness: string; company_id?: string; project_id?: string; snapshot_fingerprint?: string; project_sha?: string; external_source_id?: string; external_source_revision?: string; snapshot_observed_at?: string; artifacts: Artifact[]; overview?: OverviewSource; roadmap?: RoadmapProjection; snapshot_observations?: SnapshotObservations; portfolio_summary?: string; portfolio_cover_url?: string; documents?: { path: string; url: string }[]; diagnostic?: string };
type Page = "overview" | "documents" | "design-system" | "prototypes" | "roadmap" | "backlog" | "release" | "todo";
type Route = { companyID: string; projectID: string; page: string; pageExplicit: boolean; epicID: string; releaseID: string; todoID: string; documentFamily: string; documentID: string; prototype: PrototypeSelection };
type ProjectView = { key: string; detail: Detail; theme?: ProjectTheme; themeDiagnostic?: string };

const pages: { id: Exclude<Page, "release" | "todo">; label: string }[] = [
  { id: "overview", label: "Overview" }, { id: "roadmap", label: "Roadmap" }, { id: "backlog", label: "Backlog" }, { id: "documents", label: "Documents / decisions" },
  { id: "design-system", label: "Design System" }, { id: "prototypes", label: "Prototype" },
];
const pageDisplayNames: Record<Page, string> = { overview: "Overview", roadmap: "Roadmap", backlog: "Backlog", release: "Release", todo: "TODO", documents: "Documents", "design-system": "Design System", prototypes: "Prototypes" };
const prototypeKeys = ["artifact", "prototype_id", "screen_id", "state_id", "scenario_id", "scenario_step", "fingerprint", "device", "scale", "mode"];
const roadmapKeys = ["epic_id", "release_id", "todo_id"];
const documentKeys = ["doc_family", "doc_id"];

function route(): Route {
  const q = new URLSearchParams(location.search);
  return {
    companyID: q.get("company_id") || "", projectID: q.get("project_id") || "",
    page: q.get("page") || "overview", pageExplicit: q.has("page"), epicID: q.get("epic_id") || "", releaseID: q.get("release_id") || "", todoID: q.get("todo_id") || "",
    documentFamily: q.get("doc_family") || "", documentID: q.get("doc_id") || "",
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
  if (contextChanged) { url.searchParams.delete("page"); [...prototypeKeys, ...roadmapKeys, ...documentKeys].forEach((key) => url.searchParams.delete(key)); }
  else if (pageChanged) [...prototypeKeys, ...roadmapKeys, ...documentKeys].forEach((key) => url.searchParams.delete(key));
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

function todoStatusText(todo: RoadmapTodo) {
  if (todo.missing) return "TODO indisponível na revisão publicada";
  return todo.blocked ? `Bloqueado · ${deliveryStageLabel(todo)}` : deliveryStageLabel(todo);
}

function TodoPreview({ todo, context, epicID = "", releaseID = "", onClose, focusOnMobile = false }: { todo: RoadmapTodo; context: string; epicID?: string; releaseID?: string; onClose: () => void; focusOnMobile?: boolean }) {
  const previewRef = useRef<HTMLElement | null>(null);
  const error = Boolean(todo.missing);
  const objective = todo.objective_state === "complete" ? todo.objective || "" : "";
  const nextStep = todo.next_step_state === "complete" ? todo.next_step || "" : "";
  const openFullTODO = () => navigate({ page: "todo", epic_id: epicID || null, release_id: releaseID || null, todo_id: todo.id });
  useLayoutEffect(() => {
    if (!focusOnMobile || !window.matchMedia("(max-width: 1200px)").matches) return;
    const preview = previewRef.current;
    if (!preview) return;
    preview.scrollIntoView?.({ behavior: "smooth", block: "start" });
    preview.focus();
  }, [focusOnMobile, todo.id]);
  return <aside ref={previewRef} tabIndex={-1} className="todo-preview roadmap-todo-preview" aria-label="Prévia do TODO">
    <div className="todo-preview-top"><span>{todo.todo_revision === undefined ? "TODO · LEITURA DA FOUNDATION" : `TODO · REGISTRO MONGO · revisão ${todo.todo_revision}`}</span><button type="button" aria-label="Fechar prévia" onClick={onClose}>×</button></div>
    <p className="todo-preview-context">{context}</p><h2>{todo.title}</h2><p className={`todo-preview-status ${todo.blocked ? "blocked" : deliveryStageClass(todo)}`}>{todoStatusText(todo)}</p>{todoStatusTeach(todo) && <small className="todo-stage-teach" role="note">{todoStatusTeach(todo)}</small>}
    {error && <p className="project-notice" role="alert">Este TODO não está disponível na revisão publicada do projeto.</p>}
    <div className="todo-preview-body">{objective && <section><h3>Objetivo</h3><div className="todo-markdown"><ReactMarkdown remarkPlugins={[remarkGfm]} components={{ img: () => null }}>{objective}</ReactMarkdown></div></section>}{todo.objective_state === "too_long" && <p>O objetivo completo é extenso para a prévia. Abra o TODO para ler o texto original.</p>}{nextStep && <section><h3>Próximo passo</h3><div className="todo-markdown"><ReactMarkdown remarkPlugins={[remarkGfm]} components={{ img: () => null }}>{nextStep}</ReactMarkdown></div></section>}{todo.next_step_state === "too_long" && <p>O próximo passo completo é extenso para a prévia. Abra o TODO para ler o texto original.</p>}</div>
    <button className="todo-preview-open" type="button" disabled={error} onClick={openFullTODO}>Abrir TODO completo <span aria-hidden="true">↗</span></button>
  </aside>;
}

function BacklogFolderTree({ folders, onSelect, selectedID = "", root = false }: { folders: BacklogFolder[]; onSelect: (todo: RoadmapTodo) => void; selectedID?: string; root?: boolean }) {
  return <>{folders.map((folder, index) => <details className="backlog-folder" key={folder.path} ref={(element) => {
    if (element && root && index === 0 && !element.dataset.initialOpen) {
      element.open = true;
      element.dataset.initialOpen = "true";
    }
  }}>
    <summary><span aria-hidden="true">▸</span><strong>{folder.name}</strong><small>{backlogFolderTodoCount(folder)} TODOs</small></summary>
    {folder.description && <p className="folder-description">{folder.description}</p>}
    {folder.todos.map((todo) => <button className={`backlog-todo${todo.id === selectedID ? " selected" : ""}`} type="button" key={todo.id} aria-pressed={todo.id === selectedID} onClick={() => onSelect(todo)}>▤ <span><strong>{todo.title}</strong><small>{todoStatusText(todo)} · {folder.name}</small>{todoStatusTeach(todo) && <small className="todo-stage-teach">{todoStatusTeach(todo)}</small>}</span><span aria-hidden="true">›</span></button>)}
    <BacklogFolderTree folders={folder.folders} onSelect={onSelect} selectedID={selectedID} />
  </details>)}</>;
}

function flattenBacklogTodos(folder: BacklogFolder): RoadmapTodo[] {
  return [...folder.todos, ...folder.folders.flatMap(flattenBacklogTodos)];
}

function backlogFolderTodoCount(folder: BacklogFolder): number {
  return folder.todos.length + folder.folders.reduce((total, child) => total + backlogFolderTodoCount(child), 0);
}

function ReleaseDetailView({ roadmap, companyID, projectID, epicID, releaseID, todoID }: { roadmap?: RoadmapProjection; companyID: string; projectID: string; epicID: string; releaseID: string; todoID: string }) {
  const previewOpenerRef = useRef<HTMLButtonElement | null>(null);
  const releaseBackRef = useRef<HTMLButtonElement | null>(null);
  const previewDialogRef = useRef<HTMLDivElement | null>(null);
  const epic = roadmap?.project_id === projectID && roadmap.schema_version === "builder-roadmap-projection-v3"
    ? roadmap.epics.find((item) => item.id === epicID && item.releases.some((release) => release.id === releaseID)) : undefined;
  const release = epic?.releases.find((item) => item.id === releaseID);
  const selectedTodo = release?.todos.find((todo) => todo.id === todoID && isTODOReadable(todo));
  const investigationKey = `${companyID}/${projectID}/${releaseID}`;
  const [investigationRead, setInvestigationRead] = useState<{ key: string; state: "loading" | "ready" | "error"; value?: ReleaseInvestigationRead }>({ key: "", state: "loading" });
  useEffect(() => {
    if (!release) return;
    const controller = new AbortController();
    setInvestigationRead({ key: investigationKey, state: "loading" });
    const query = new URLSearchParams({ company_id: companyID, project_id: projectID, release_id: releaseID });
    fetch(`/api/roadmap?${query}`, { signal: controller.signal }).then(async (response) => {
      if (!response.ok) throw new Error("Release investigation unavailable");
      const value = await response.json() as ReleaseInvestigationRead;
      if (value.company_id !== companyID || value.project_id !== projectID || value.release_id !== releaseID || value.state !== "available" && value.state !== "absent" || value.state === "available" && !value.document) throw new Error("Release investigation identity mismatch");
      if (!controller.signal.aborted) setInvestigationRead({ key: investigationKey, state: "ready", value });
    }).catch(() => { if (!controller.signal.aborted) setInvestigationRead({ key: investigationKey, state: "error" }); });
    return () => controller.abort();
  }, [companyID, projectID, releaseID, release?.id, investigationKey]);
  useLayoutEffect(() => {
    if (!selectedTodo) return;
    const dialog = previewDialogRef.current;
    dialog?.querySelector<HTMLButtonElement>('button[aria-label="Fechar prévia"]')?.focus();
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        event.preventDefault();
        navigate({ todo_id: null });
      } else if (event.key === "Tab" && dialog) {
        const focusable = Array.from(dialog.querySelectorAll<HTMLElement>('button:not(:disabled), a[href], [tabindex]:not([tabindex="-1"])'));
        if (!focusable.length) return;
        const first = focusable[0];
        const last = focusable[focusable.length - 1];
        if (event.shiftKey && (document.activeElement === first || !dialog.contains(document.activeElement))) {
          event.preventDefault();
          last.focus();
        } else if (!event.shiftKey && (document.activeElement === last || !dialog.contains(document.activeElement))) {
          event.preventDefault();
          first.focus();
        }
      }
    };
    const handleFocusIn = (event: FocusEvent) => {
      if (dialog && event.target instanceof Node && !dialog.contains(event.target)) {
        dialog.querySelector<HTMLButtonElement>('button[aria-label="Fechar prévia"]')?.focus();
      }
    };
    document.addEventListener("keydown", handleKeyDown);
    document.addEventListener("focusin", handleFocusIn);
    return () => {
      document.removeEventListener("keydown", handleKeyDown);
      document.removeEventListener("focusin", handleFocusIn);
      (previewOpenerRef.current || releaseBackRef.current)?.focus();
    };
  }, [selectedTodo?.id]);
  if (!epic || !release) return <section className="project-notice" role="alert"><h1>Release indisponível</h1><p>Esta Release não existe no Roadmap salvo deste projeto.</p><button onClick={() => navigate({ page: "roadmap", epic_id: null, release_id: null })}>Voltar ao Roadmap</button></section>;
  const executable = release.todos.filter((todo) => todo.kind === "executable" && todo.disposition_state !== "valid");
  const historical = release.todos.filter((todo) => todo.disposition_state === "valid");
  const status = releaseStatus(release);
  const back = () => navigate({ page: "roadmap", epic_id: epic.id, release_id: release.id });
  return <article className="release-page" aria-label="Detalhe da Release">
    <button ref={releaseBackRef} className="release-back" onClick={back}><span aria-hidden="true">←</span> Voltar ao Roadmap</button>
    <nav className="release-breadcrumb" aria-label="Localização"><button onClick={back}>Roadmap</button><span aria-hidden="true">›</span><span>{epic.name}</span><span aria-hidden="true">›</span><span aria-current="page">{release.name}</span></nav>
    <header className="release-hero"><div><span className="release-kicker">RELEASE · {release.id}</span><h1>{release.name}</h1>{release.description && <p>{release.description}</p>}</div><span className={`release-badge ${status}`}><span aria-hidden="true">◉</span> {releaseStatusLabel[status]}</span></header>
    <section className="release-context-strip without-investigation" aria-label="Contexto da release">
      <div className="release-context-epic"><span className="release-context-icon" aria-hidden="true">◉</span><div><small>Épico</small><strong>{epic.name}</strong>{epic.description && <span>{epic.description}</span>}</div></div>
    </section>
    {investigationRead.key === investigationKey && investigationRead.state === "loading" && <p role="status">Carregando estudos da Release…</p>}
    {investigationRead.key === investigationKey && investigationRead.state === "error" && <p className="project-notice" role="alert">Os estudos desta Release não puderam ser carregados. Os TODOs continuam disponíveis.</p>}
    {investigationRead.key === investigationKey && investigationRead.state === "ready" && investigationRead.value?.state === "available" && investigationRead.value.document && <section className="release-investigation" aria-labelledby="release-investigation-title">
      <div className="release-work-heading"><div><h2 id="release-investigation-title">{investigationRead.value.document.title}</h2>{investigationRead.value.document.introduction && <p>{investigationRead.value.document.introduction}</p>}</div></div>
      {investigationRead.value.document.materials.map((material) => <details key={material.id} className="release-study"><summary>{material.title}<span aria-hidden="true">›</span></summary><div className="todo-markdown"><ReactMarkdown remarkPlugins={[remarkGfm]} components={{ img: () => null }}>{material.body_markdown}</ReactMarkdown></div>{material.source_refs && material.source_refs.length > 0 && <ul className="release-study-refs" aria-label="Fontes do estudo">{material.source_refs.map((ref) => <li key={ref}>{ref}</li>)}</ul>}</details>)}
    </section>}
    <section className="release-work" aria-labelledby="release-work-title"><div className="release-work-heading"><div><h2 id="release-work-title">TODOs executáveis</h2><p>Trabalho agrupado nesta release.</p></div></div>
      {executable.length ? <div className="release-work-list" role="list">{executable.map((todo) => {
        return <article className="release-work-row" role="listitem" key={todo.id}><span className="release-work-icon" aria-hidden="true">▤</span><h3><button className="release-todo-link" onClick={(event) => { previewOpenerRef.current = event.currentTarget; navigate({ todo_id: todo.id }); }}>{todo.title} <span aria-hidden="true">›</span></button></h3><span className="release-work-id">{todo.id}</span><span className="release-work-status-wrap"><TodoStageStatus todo={todo} className="release-work-status" /></span></article>;
      })}</div> : <p className="project-notice">Nenhum TODO executável nesta Release.</p>}
    </section>
    {historical.length > 0 && <section className="release-work" aria-label="Histórico não entregue"><div className="release-work-heading"><div><h2>Histórico não entregue</h2><p>Itens substituídos ou descartados não contam como entrega.</p></div></div><div className="release-work-list" role="list">{historical.map((todo) => <article className="release-work-row" role="listitem" key={todo.id}><span className="release-work-icon" aria-hidden="true">▤</span><h3><button className="release-todo-link" onClick={() => navigate({ todo_id: todo.id })}>{todo.title} <span aria-hidden="true">›</span></button></h3><span className="release-work-id">{todo.disposition_reason}</span><span className="release-work-status-wrap"><TodoStageStatus todo={todo} className="release-work-status" /></span></article>)}</div></section>}
    {selectedTodo && <div ref={previewDialogRef} className="release-todo-overlay" role="dialog" aria-modal="true" aria-label="Prévia do TODO"><TodoPreview todo={selectedTodo} context={`${epic.name} / ${release.name}`} epicID={epic.id} releaseID={release.id} onClose={() => navigate({ todo_id: null })} /></div>}
  </article>;
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
  const canonical = knowledge?.schema_version === "2" && visual?.source_authority === "canonical_api";
  const reviewLabels = { current: "Current against reviewed source", review_required: "Review required", unverifiable: "Unverifiable" };
  const originLabels = { matching: "Matches saved source observations", mismatch: "Mismatch", unverifiable: "Unverifiable" };
  const observedAt = savedObservationTime(detail.snapshot_observed_at);
  const timestampAvailable = observedAt !== "Observation time unavailable";
  const landing = timestampAvailable ? savedObservationStatus(knowledge?.landing?.status, reviewLabels) : "Unavailable";
  const overview = timestampAvailable ? savedObservationStatus(knowledge?.overview?.status, reviewLabels) : "Unavailable";
  const roadmap = timestampAvailable ? savedObservationStatus(knowledge?.roadmap?.status, reviewLabels) : "Unavailable";
  const origin = timestampAvailable ? savedObservationStatus(visual?.state, originLabels) : "Unavailable";
  const needsAttention = canonical ? landing !== reviewLabels.current || overview !== reviewLabels.current : landing !== reviewLabels.current || roadmap !== reviewLabels.current || origin !== originLabels.matching;
  const reason = (value?: unknown) => typeof value === "string" && value ? ` (${value.replaceAll("_", " ")})` : "";
  return <section className="overview-status" aria-labelledby="snapshot-status-title">
    <div className="overview-status-heading"><div><span className="project-page-tag">{canonical ? "Canonical API · loaded revision" : "Published source · loaded revision"}</span><h2 id="snapshot-status-title">Snapshot status</h2></div><span>{canonical ? `Observations saved ${observedAt}` : `Observations saved ${observedAt} · Foundation ${detail.project_sha?.slice(0, 12) || "revision unavailable"}`}</span></div>
    <dl className="overview-status-grid">
      <div><dt>Landing review</dt><dd>{landing}{landing !== "Unavailable" && reason(knowledge?.landing?.reason_code)}</dd></div>
      {canonical ? <div><dt>Overview review</dt><dd>{overview}{overview !== "Unavailable" && reason(knowledge?.overview?.reason_code)}</dd></div> : <><div><dt>Roadmap review</dt><dd>{roadmap}{roadmap !== "Unavailable" && reason(knowledge?.roadmap?.reason_code)}</dd></div><div><dt>Visual origin</dt><dd>{origin}</dd></div></>}
    </dl>
    <p className="overview-status-note">{canonical ? "These observations compare visual artifacts with canonical API documents. They do not establish human approval." : "These observations were extracted from the published Foundation revision shown above. They do not establish human approval."}</p>
    {needsAttention && <p className="overview-status-next" role="status">{canonical ? "TEACH: Review the selected Project’s canonical API documents and update the affected visual artifact through Delphi; then reload this Project." : "TEACH: Check the selected Project’s published Foundation branch through Delphi. Correct the source, publish the change, then reload this Project to read the current branch."}</p>}
  </section>;
}

function TodoDetailView({ roadmap, companyID, projectID, todoID }: { roadmap?: RoadmapProjection; companyID: string; projectID: string; todoID: string }) {
  const [mongoRead, setMongoRead] = useState<{ key: string; state: "loading" | "ready" | "error"; value?: MongoTODORead; message?: string }>({ key: "", state: "loading" });
  const mongoSummary = [...(roadmap?.epics.flatMap((epic) => epic.releases.flatMap((release) => release.todos)) || []), ...(roadmap?.backlog.todos || []), ...(roadmap?.backlog.folders.flatMap(flattenBacklogTodos) || [])].find((todo) => todo.id === todoID && todo.todo_revision !== undefined);
  const mongoInBacklog = Boolean(mongoSummary && roadmap?.backlog.todos.concat(roadmap.backlog.folders.flatMap(flattenBacklogTodos)).some((todo) => todo.id === mongoSummary.id));
  const mongoKey = mongoSummary ? `${companyID}/${projectID}/${mongoSummary.id}/${mongoSummary.todo_revision}` : "";
  useEffect(() => {
    if (!mongoKey || !mongoSummary) { setMongoRead({ key: mongoKey, state: "error" }); return; }
    const controller = new AbortController();
    setMongoRead({ key: mongoKey, state: "loading" });
    const query = new URLSearchParams({ company_id: companyID, project_id: projectID, todo_id: mongoSummary.id, todo_revision: String(mongoSummary.todo_revision) });
    fetch(`/api/roadmap?${query}`, { signal: controller.signal }).then(async (response) => {
      const value = await response.json() as MongoTODORead & { error?: string; teach?: string };
      if (!response.ok) throw new Error(value.teach || "TEACH: The current Mongo TODO revision is unavailable.");
      if (value.company_id !== companyID || value.project_id !== projectID || value.todo?.id !== mongoSummary.id || value.todo_revision !== mongoSummary.todo_revision || value.todo.revision !== mongoSummary.todo_revision) throw new Error("TEACH: Mongo TODO identity or revision changed. Reload Roadmap.");
      return value;
    }).then((value) => { if (!controller.signal.aborted) setMongoRead({ key: mongoKey, state: "ready", value }); })
      .catch((error: unknown) => { if (!controller.signal.aborted) setMongoRead({ key: mongoKey, state: "error", message: error instanceof Error ? error.message : "TEACH: Mongo TODO unavailable." }); });
    return () => controller.abort();
  }, [mongoKey, companyID, projectID, mongoSummary?.todo_revision]);
  if (mongoSummary) {
    const loadedMongo = mongoRead.key === mongoKey ? mongoRead : { key: mongoKey, state: "loading" as const };
    const item = loadedMongo.value?.todo;
    return <article className="todo-page" aria-label="Mongo TODO detail"><nav className="todo-breadcrumb"><button onClick={() => navigate({ page: mongoInBacklog ? "backlog" : "roadmap" })}>{mongoInBacklog ? "Backlog" : "Roadmap"}</button><span aria-hidden="true">›</span><span aria-current="page">TODO</span></nav><header className="todo-hero"><h1>{mongoSummary.title}</h1><div className="todo-hero-meta"><TodoStageStatus todo={mongoSummary} className="todo-stage"/><span className="todo-source-label">Fonte: Mongo · revisão {mongoSummary.todo_revision}</span></div></header>{loadedMongo.state === "loading" && <p role="status">Carregando contrato Mongo…</p>}{loadedMongo.state === "error" && <p className="project-notice" role="alert">{loadedMongo.message || "TEACH: Mongo TODO readback failed. Reload Roadmap and retry."}</p>}{item && <><section className="todo-objective"><div><h2>Objective</h2><ReactMarkdown remarkPlugins={[remarkGfm]}>{item.objective}</ReactMarkdown></div></section><section className="todo-facts"><div><span><small>Next exact step</small><strong>{item.next_exact_step}</strong></span></div><div><span><small>Stage</small><strong>{item.stage}</strong></span></div></section>{item.content ? <TypedTODOContent content={item.content} legacySections={item.sections}/> : item.sections.map((section) => <section className="todo-panel" key={`${section.position}:${section.key}`}><h2>{section.title}</h2><div className="todo-markdown"><ReactMarkdown remarkPlugins={[remarkGfm]}>{section.body_markdown}</ReactMarkdown></div></section>)}</>}</article>;
  }
  return <section className="project-notice" role="alert"><h1>TODO indisponível</h1><p>TEACH: Este item não possui uma revisão exata no Mongo Roadmap. Markdown Foundation é proveniência de migração e não será usado como conteúdo atual.</p><button onClick={() => navigate({ page: "roadmap" })}>Voltar ao Roadmap</button></section>;
}

function OverviewEditorial({ detail, projectName }: { detail: Detail; projectName: string }) {
  const overview = detail.overview;
  const landing = detail.artifacts.find((item) => item.kind === "landing" && item.state === "available");
  const landingHref = landing ? artifactURL(detail, landing) : "";
  const landingLink = landingHref ? <a className="overview-landing-link" href={landingHref} target="_blank" rel="noreferrer">Ver landing independente <span aria-hidden="true">→</span></a> : null;
  if (!overview || overview.schema_version !== "1") return <section className="project-notice" role="status">
    <h2>Project Overview not available yet</h2><p>{detail.artifacts.find((item) => item.kind === "overview")?.diagnostic || "Create project_overview.json in this Project's Foundation and validate it before publishing the branch."}</p>{landingLink}
  </section>;
  const sections = ["problem", "proposal", "impact", "audience"] as const;
  const labels = { problem: "Problema", proposal: "Proposta", impact: "Impacto", audience: "Para quem" };
  return <>
    <section className="project-hero overview-hero" aria-label="Project introduction">
      <div className="project-hero-copy"><span className="overview-eyebrow">VISÃO DO PROJETO</span><h1>{projectName}</h1><h2 className="overview-hero-tagline">{overview.tagline}</h2><p className="overview-hero-summary">{overview.summary}</p>{landingLink}</div>
      {(overview.media?.desktop && overview.media?.mobile) ? <div className="overview-device-pair" aria-label="Project preview images">
        <span className="overview-visual-note">CONCEITO · DADOS ILUSTRATIVOS</span>
        {overview.media.desktop && <img className="overview-desktop" src={overview.media.desktop} alt={overview.desktop_alt || ""} />}
        {overview.media.mobile && <img className="overview-mobile" src={overview.media.mobile} alt={overview.mobile_alt || ""} />}
      </div> : <p className="project-notice" role="alert">Hero images are missing from the loaded Overview. Create desktop.png and mobile.png, then publish the branch.</p>}
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

function RoadmapView({ roadmap, detail, projectID, epicID, releaseID, compact = false }: { roadmap?: RoadmapProjection; detail?: Detail | null; projectID: string; epicID: string; releaseID: string; compact?: boolean }) {
  const roadmapArtifact = detail?.artifacts.find((item) => item.kind === "roadmap");
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
  if (!roadmap || roadmap.project_id !== projectID || roadmap.schema_version !== "builder-roadmap-projection-v3") {
    return <p className="project-notice roadmap-unavailable" role={roadmapArtifact?.state === "invalid" ? "alert" : "status"}>
      {roadmapArtifact?.diagnostic || "Project Roadmap data is not available in this saved snapshot."}
    </p>;
  }
  if (invalidFocus) return <p className="project-notice roadmap-unavailable" role="alert">This Epic or Release link is invalid for this Project. Remove epic_id and release_id to return to the current Roadmap focus.</p>;
  const selectedEpicIndex = Math.max(0, epics.findIndex((epic) => epic.id === selectedEpic?.id));
  const releases = selectedEpic?.releases || [];
  const focusName = selectedEpic?.releases.find((release) => release.id === focusedRelease)?.name;
  const selectEpic = (epic?: RoadmapEpic) => { if (epic) navigate({ epic_id: epic.id, release_id: initialRelease(epic)?.id || null, todo_id: null }); };
  const selectRelease = (release: RoadmapRelease) => navigate({ epic_id: selectedEpicID, release_id: release.id, todo_id: null });
  const visibleRelease = selectedEpic?.releases.find((release) => release.id === focusedRelease);
  const selectedReleaseTodo = visibleRelease?.todos.find((todo) => todo.id === route().todoID);
  const previewContext = selectedEpic && visibleRelease ? `${selectedEpic.name} / ${visibleRelease.name}` : "";
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
        {epics.length === 0 && <p className="roadmap-no-releases">Nenhum Epic ou Release planejado.</p>}
        </div>
        {compact && <button className="roadmap-rail-arrow" type="button" aria-label="Next Epic" disabled={selectedEpicIndex >= epics.length - 1} onClick={() => selectEpic(epics[selectedEpicIndex + 1])}>›</button>}
      </div>
    </div>
    {compact && <div className="roadmap-rail-group roadmap-releases-rail">
      <h3>{selectedEpic ? `RELEASES DO ÉPICO ${selectedEpic.name.toLocaleUpperCase("pt-BR")}` : "PLANO DE RELEASES"}</h3><p>{selectedEpic ? "Marcos deste épico." : "Nenhum Epic ou Release planejado."}</p>
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
    {!compact && <div className="roadmap-full-body"><div className="roadmap-release-panel release-panel"><header className="roadmap-release-panel-heading release-heading"><div><span>{selectedEpic ? "RELEASES DO ÉPICO" : "PLANEJAMENTO"}</span><h2>{selectedEpic ? `Releases de ${selectedEpic.name}` : "Sem plano de releases"}</h2><p>{selectedEpic?.description || (!selectedEpic ? "TODOs ativos identificados pela Foundation aparecem no Backlog; nenhum agrupamento foi inferido." : "")}</p></div></header><div className="roadmap-release-list release-list" role="group" aria-label="Releases for selected Epic">
      {releases.map((release) => <section className={`roadmap-release release-card${release.id === focusedRelease ? " focused" : ""}${expandedReleaseIDs.includes(release.id) ? " open" : ""}`} key={release.id}>
        <button className="roadmap-release-toggle release-toggle" aria-expanded={expandedReleaseIDs.includes(release.id)} onClick={() => { if (release.id !== focusedRelease) selectRelease(release); else setExpandedReleaseIDs((current) => current.includes(release.id) ? current.filter((id) => id !== release.id) : [...current, release.id]); }}>
          <span className="roadmap-release-chevron chevron" aria-hidden="true">›</span><span className="roadmap-release-name release-name">{release.name}<span className="release-description">{release.description}</span></span>
          <span className={`roadmap-release-status release-status ${releaseStatus(release)}`}>{releaseStatusLabel[releaseStatus(release)]}</span>
        </button>
        <button className="roadmap-release-open" type="button" aria-label="Abrir Release" title={`Abrir ${release.name}`} onClick={() => navigate({ page: "release", epic_id: selectedEpicID, release_id: release.id })}><span aria-hidden="true">›</span></button>
        {expandedReleaseIDs.includes(release.id) && <div className="roadmap-release-details release-details"><div className="roadmap-table-scroll"><table className="roadmap-todo-table todo-table"><thead><tr><th>#</th><th>TODO</th><th>TIPO · REGISTRO</th><th>STATUS</th></tr></thead><tbody>{release.todos.map((todo, index) => <tr key={todo.id}>
          <td>{String(index + 1).padStart(2, "0")}</td><td className="todo-name" title={todo.path}>{isTODOReadable(todo) ? <button className="roadmap-todo-link" onClick={() => navigate({ todo_id: todo.id })}>▤ &nbsp;{todo.title}</button> : <>▤ &nbsp;{todo.title}</>}</td><td><span className="roadmap-todo-meta">{todo.kind === "planning" ? "Planning ledger" : "Executable TODO"}<small>{todo.record_state === "completed" ? "Local record: completed" : todo.record_state === "active" ? "Local record: active" : "Local record: other"}</small></span></td><td>{todo.kind === "planning" ? <span className="roadmap-todo-stage">—</span> : <TodoStageStatus todo={todo} className="roadmap-todo-stage todo-status" />}</td>
        </tr>)}</tbody></table></div></div>}
      </section>)}
      {releases.length === 0 && <p className="project-notice">No Releases defined yet.</p>}
    </div></div>
    {!compact && (roadmap.unclassified.length > 0 || roadmap.diagnostics.length > 0) && <section className="roadmap-planning-diagnostics">
      {roadmap.unclassified.length > 0 && <section className="roadmap-unclassified" aria-label="TODOs sem classificação"><h3>Planejamento inválido · documentos admitidos sem agrupamento</h3><ul>{roadmap.unclassified.map((item) => <li key={item.path}><span>{item.path}</span><small>{item.reason}</small></li>)}</ul></section>}
      {roadmap.diagnostics.length > 0 && <section className="roadmap-diagnostics" aria-label="Roadmap diagnostics"><h3>Avisos do Roadmap</h3><ul>{roadmap.diagnostics.map((item, index) => <li key={`${item.code}:${item.path || index}`}><strong>{item.path || item.code}</strong><span>{item.message}</span></li>)}</ul></section>}
    </section>}
    </div>}
    {!compact && selectedReleaseTodo ? <TodoPreview todo={selectedReleaseTodo} context={previewContext} epicID={selectedEpicID} releaseID={focusedRelease} onClose={() => navigate({ todo_id: null })} focusOnMobile /> : !compact && <aside className="roadmap-context"><h2>Contexto do projeto</h2><div><span>▤</span><p><small>Release em foco</small><strong>{focusName || (selectedEpic ? "Sem Release neste épico" : "Sem Release planejada")}</strong>Seleção de navegação, não conclusão.</p></div><nav className="roadmap-context-links" aria-label="Roadmap useful links"><h3>Links úteis</h3><button type="button" onClick={() => navigate({ page: "overview" })}>◉ <span>Visão do produto</span><b aria-hidden="true">↗</b></button><button type="button" onClick={() => navigate({ page: "documents" })}>▤ <span>Documentação</span><b aria-hidden="true">↗</b></button></nav></aside>}
  </section>;
}

function BacklogView({ roadmap, detail, projectID, todoID }: { roadmap?: RoadmapProjection; detail?: Detail | null; projectID: string; todoID: string }) {
  const roadmapArtifact = detail?.artifacts.find((item) => item.kind === "roadmap");
  if (!roadmap || roadmap.project_id !== projectID || roadmap.schema_version !== "builder-roadmap-projection-v3") {
    return <p className="project-notice" role={roadmapArtifact?.state === "invalid" ? "alert" : "status"}>{roadmapArtifact?.diagnostic || "Project Backlog data is not available in this snapshot."}</p>;
  }
  const selectedTodo = roadmap.backlog.todos.concat(roadmap.backlog.folders.flatMap(flattenBacklogTodos)).find((todo) => todo.id === todoID);
  const selectTodo = (todo: RoadmapTodo) => navigate({ todo_id: todo.id });
  const folderPath = selectedTodo?.path?.replace("todos/active/", "").split("/").slice(0, -1).join(" / ") || "Raiz";
  return <section className="backlog-view" aria-label="Project Backlog">
    <div className="backlog-main">
      <header className="backlog-page-heading"><p className="roadmap-eyebrow">TRABALHO AINDA NÃO PLANEJADO</p><h1>Backlog</h1><p className="subtitle">TODOs ativos ainda não atribuídos a uma Release. As pastas preservam a organização da Foundation.</p></header>
      <section className="backlog-panel" aria-labelledby="backlog-heading"><div className="backlog-heading"><div><p className="roadmap-eyebrow">EXPLORAR POR PASTA</p><h2 id="backlog-heading">O que está em aberto</h2><p className="subtitle">Expanda uma pasta para ver seus TODOs; selecione um item para ler a prévia.</p></div></div>
        {roadmap.backlog.folders.length > 0 && <div className="backlog-tree" role="group" aria-label="Pastas do Backlog"><BacklogFolderTree folders={roadmap.backlog.folders} root selectedID={selectedTodo?.id} onSelect={selectTodo} /></div>}
        {roadmap.backlog.todos.length > 0 && <div className="backlog-ungrouped"><h3>Sem pasta</h3>{roadmap.backlog.todos.map((todo) => <button className={`backlog-todo${todo.id === selectedTodo?.id ? " selected" : ""}`} type="button" key={todo.id} aria-pressed={todo.id === selectedTodo?.id} onClick={() => selectTodo(todo)}>▤ <span><strong>{todo.title}</strong><small>{todoStatusText(todo)} · Raiz do Backlog</small>{todoStatusTeach(todo) && <small className="todo-stage-teach">{todoStatusTeach(todo)}</small>}</span><span aria-hidden="true">›</span></button>)}</div>}
        {roadmap.backlog.folders.length === 0 && roadmap.backlog.todos.length === 0 && <p className="project-notice">Nenhum TODO ativo sem Release.</p>}
      </section>
    </div>
    {selectedTodo ? <TodoPreview todo={selectedTodo} context={`Backlog · ${folderPath}`} onClose={() => navigate({ todo_id: null })} focusOnMobile /> : <aside className="backlog-context" aria-label="Contexto do Backlog"><section className="backlog-context-card"><h2>Backlog</h2><p>Este trabalho ainda não pertence a um épico ou a uma Release. A organização por pastas pode ser aninhada e descrita por README.</p><div className="backlog-context-notice"><span aria-hidden="true">ⓘ</span><p>Selecionar um TODO abre a prévia sem perder a posição na árvore.</p></div><button type="button" onClick={() => navigate({ page: "roadmap" })}>Ver Roadmap <span aria-hidden="true">→</span></button></section></aside>}
  </section>;
}

function PortalHeader({ children }: { children: ReactNode }) {
  return <header className="projects-mast"><a href="/">Builder <span>Project directory</span></a>{children}</header>;
}

function ProjectLoadingShell({ company, project, error, stage, onRetry }: { company: Company; project: Project; error: string; stage: "foundation" | "design" | "error"; onRetry: () => void }) {
  const [elapsed, setElapsed] = useState(0);
  useEffect(() => { const started = Date.now(); const timer = window.setInterval(() => setElapsed(Math.floor((Date.now() - started) / 1000)), 1000); return () => window.clearInterval(timer); }, []);
  const foundationDone = stage === "design" || stage === "error";
  const context = `${company.name} / ${project.name}`;
  return <main className="projects-page projects-project project-loading-shell">
    <header className="project-topbar"><a className="project-topbar-brand" href="/"><span className="project-topbar-mark" aria-hidden="true">◆</span><span>Project Builder</span></a><div className="project-topbar-meta"><span>Modo do projeto: <strong>MVP</strong></span><span>Exploração visual</span></div></header>
    <div className="project-layout"><nav className="project-nav" aria-label="Navegação do projeto"><div className="project-nav-links">{pages.map((item) => <button key={item.id} disabled aria-label={item.label}><span className="project-nav-icon" aria-hidden="true">{{overview:"⌂",roadmap:"▦",backlog:"☷",documents:"▤","design-system":"◈",prototypes:"▷"}[item.id]}</span>{pageDisplayNames[item.id]}</button>)}</div><span className="nav-footer">Exploração visual</span></nav>
      <section className="project-content project-loading-page" aria-label="Abrindo projeto"><p className="roadmap-eyebrow">BELLUGA SOLUTIONS / BUILDER</p><h1>Abrindo o projeto</h1><p className="subtitle">O painel está conferindo a revisão publicada antes de mostrar os dados.</p>
        <section className="loading-card" aria-labelledby="loading-title" role={error ? "alert" : "status"} aria-live="polite"><div className="loading-card-head"><span className="loading-symbol" aria-hidden="true">◌</span><div><p className="roadmap-eyebrow">LEITURA DO PROJETO</p><h2 id="loading-title">Extraindo dados dos seus TODOs</h2></div></div>
          <p id="loading-explanation">{error || "Empresa e projeto encontrados. A leitura da Foundation publicada está em andamento."}</p>
          <ol className="loading-steps"><li className="confirmed"><span aria-hidden="true">✓</span><span>Contexto encontrado<strong>{context}</strong></span></li><li className={error ? "failed" : foundationDone ? "confirmed" : "running"}><span aria-hidden="true">{error ? "!" : foundationDone ? "✓" : "◌"}</span><span>Foundation publicada<strong>{error ? "A leitura precisa ser tentada novamente" : foundationDone ? "Revisão lida" : "Aguardando resposta da API"}</strong></span></li><li className={stage === "design" ? "running" : "pending"}><span aria-hidden="true">{stage === "design" ? "◌" : "·"}</span><span>Design System<strong>{stage === "design" ? "Aplicação após a leitura" : "Aplicação após a leitura"}</strong></span></li></ol>
          <p className="loading-elapsed">Aguardando há {elapsed} s</p><div className="loading-actions">{error && <button id="retry-link" type="button" onClick={onRetry}>Tentar novamente</button>}<a href="/">Trocar de projeto</a></div>
        </section><p className="loading-note">O produto só mostrará contagens e etapas confirmadas pela API.</p>
      </section></div>
  </main>;
}

function ProjectCard({ project, onOpen }: { project: Project; onOpen: () => void }) {
  const [detail, setDetail] = useState<Detail | null>(null);
  const [coverError, setCoverError] = useState(false);
  useEffect(() => {
    const controller = new AbortController();
    fetch(`/api/local-preview?company_id=${encodeURIComponent(project.company_id)}&project_id=${encodeURIComponent(project.id)}`, { signal: controller.signal })
      .then((response) => response.ok ? response.json() as Promise<Detail> : Promise.reject())
      .then((value) => { if (!controller.signal.aborted && value.consumer_readiness === "ready" && value.company_id === project.company_id && value.project_id === project.id) setDetail(value); })
      .catch(() => undefined);
    return () => controller.abort();
  }, [project.company_id, project.id]);
  return <button className="project-card" onClick={onOpen}>
    {detail?.portfolio_cover_url && !coverError ? <img src={detail.portfolio_cover_url} alt="" onError={() => setCoverError(true)} /> : <span className="project-card-mark">{project.name.slice(0, 1)}</span>}
    <small>REGISTERED PROJECT</small>
    <strong>{project.name}</strong>
    <span>{detail?.portfolio_summary || "Open this registered Project to read its currently published Foundation data."}</span>
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
  const [roadmapRead, setRoadmapRead] = useState<{ key: string; state: "loading" | "ready" | "error"; value?: RoadmapProjection }>({ key: "", state: "loading" });
  const [viewState, setViewState] = useState<"loading" | "ready" | "error">("loading");
  const [viewError, setViewError] = useState<"unavailable" | "registration">("unavailable");
  const [loadStage, setLoadStage] = useState<"foundation" | "design" | "error">("foundation");
  const [reloadCounter, setReloadCounter] = useState(0);
  const [pendingCompanyID, setPendingCompanyID] = useState("");
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
  const viewKey = project ? `${project.company_id}/${project.id}` : "";
  useEffect(() => {
    if (!project || !["overview", "roadmap", "backlog", "release", "todo"].includes(selection.page)) { setRoadmapRead({ key: "", state: "loading" }); return; }
    const controller = new AbortController();
    const key = `${project.company_id}/${project.id}`;
    setRoadmapRead({ key, state: "loading" });
    const query = new URLSearchParams({ company_id: project.company_id, project_id: project.id });
    fetch(`/api/roadmap?${query}`, { signal: controller.signal }).then(async (response) => {
      const value = await response.json() as RoadmapProjection;
      if (!response.ok || value.project_id !== project.id || value.schema_version !== "builder-roadmap-projection-v3") throw new Error("Roadmap unavailable");
      return value;
    }).then((value) => { if (!controller.signal.aborted) setRoadmapRead({ key, state: "ready", value }); })
      .catch(() => { if (!controller.signal.aborted) setRoadmapRead({ key, state: "error" }); });
    return () => controller.abort();
  }, [viewKey, reloadCounter, selection.page]);
  useEffect(() => {
    setView(null); setViewState("loading"); setViewError("unavailable"); setLoadStage("foundation");
    if (!project) return;
    const controller = new AbortController();
    async function load() {
      try {
        const response = await fetch(`/api/local-preview?company_id=${encodeURIComponent(project!.company_id)}&project_id=${encodeURIComponent(project!.id)}`, { signal: controller.signal });
        if (!response.ok) {
          if (response.status === 503) {
            const failure = await response.json().catch(() => null) as { company_id?: string; project_id?: string; diagnostic?: string } | null;
            if (failure?.company_id === project!.company_id && failure.project_id === project!.id && failure.diagnostic?.includes("invalid path-only registration")) throw new Error("registration");
          }
          throw new Error("Snapshot unavailable");
        }
        const detail = await response.json() as Detail;
        if (detail.company_id !== project!.company_id || detail.project_id !== project!.id || detail.consumer_readiness !== "ready" || !detail.snapshot_fingerprint) throw new Error("Published Project is unavailable");
        if (!controller.signal.aborted) setLoadStage("design");
        let theme: ProjectTheme | undefined;
        let themeDiagnostic: string | undefined;
        if (detail.consumer_readiness === "ready") {
          const design = detail.artifacts.find((item) => item.kind === "design_system" && item.state === "available" && item.files?.includes("design/system/design-system.json"));
          if (!design || !detail.snapshot_fingerprint) themeDiagnostic = "The loaded Design System definition is unavailable.";
          else {
            try {
              const definition = await fetch(snapshotFileURL(detail, "design/system/design-system.json"), { signal: controller.signal });
              if (!definition.ok || definition.headers.get("X-Builder-Snapshot-Fingerprint") !== detail.snapshot_fingerprint) throw new Error("Design System snapshot mismatch");
              const resolved = resolveProjectTheme(await definition.json());
              theme = resolved.style; themeDiagnostic = resolved.diagnostic;
            } catch {
              themeDiagnostic = "The loaded Design System definition could not be read.";
            }
          }
        }
        if (!controller.signal.aborted) { setView({ key: viewKey, detail, theme, themeDiagnostic }); setViewState("ready"); }
      } catch (error) {
        if (!controller.signal.aborted) { setViewError(error instanceof Error && error.message === "registration" ? "registration" : "unavailable"); setLoadStage("error"); setViewState("error"); }
      }
    }
    void load();
    return () => controller.abort();
  }, [viewKey, reloadCounter]);
  const activeView = view?.key === viewKey ? view : null;
  const roadFamilyPage = ["roadmap", "backlog", "release", "todo"].includes(selection.page);
  const roadmapPage = roadFamilyPage || selection.page === "overview";
  const roadData = roadmapRead.key === viewKey && roadmapRead.state === "ready" ? roadmapRead.value : undefined;
  const readerIndependentRoadPage = (roadFamilyPage || selection.page === "documents") && (!activeView || viewState !== "ready");
  const detail = activeView?.detail || (readerIndependentRoadPage ? { consumer_readiness: "ready", company_id: project?.company_id, project_id: project?.id, artifacts: [], roadmap: roadData } as Detail : null);
  const page = selection.page === "release" || selection.page === "todo" ? selection.page : pages.find((item) => item.id === selection.page)?.id;
  const available = detail?.artifacts || [];
  const invalidArtifacts = available.filter((item) => item.state === "invalid");

  if (catalogState === "loading") return <main className="projects-page"><p role="status">Loading registered Projects…</p></main>;
  if (catalogState === "error") return <main className="projects-page"><p role="alert">The local Project catalog is unavailable.</p></main>;
  if (selection.projectID && (!company || !project)) return <main className="projects-page"><p role="alert">This Company and Project selection is unavailable.</p><a href="/">Back to Companies</a></main>;
  if (selection.companyID && !company) return <main className="projects-page"><p role="alert">This Company is unavailable.</p><a href="/">Back to Companies</a></main>;
  if (selection.projectID && !page) return <main className="projects-page"><p role="alert">This Project page is unavailable.</p><a href={`/?company_id=${encodeURIComponent(selection.companyID)}&project_id=${encodeURIComponent(selection.projectID)}`} onClick={(event) => { if (!event.ctrlKey && !event.metaKey && !event.shiftKey && event.button === 0) { event.preventDefault(); navigate({ page: "overview" }); } }}>Back to Overview</a></main>;
  if (selection.projectID && selection.prototype.artifact && !selection.pageExplicit && !selection.prototype.mode) return <main className="projects-page"><p role="alert">This artifact-only link is incomplete. Open the Prototype page.</p><a href={`/?company_id=${encodeURIComponent(selection.companyID)}&project_id=${encodeURIComponent(selection.projectID)}&page=prototypes`} onClick={(event) => { if (!event.ctrlKey && !event.metaKey && !event.shiftKey && event.button === 0) { event.preventDefault(); navigate({ page: "prototypes" }); } }}>Open Prototype</a></main>;
  if (selection.prototype.mode && (selection.prototype.mode !== "presentation" || page !== "prototypes" || !selection.projectID)) return <main className="projects-page"><p role="alert">This presentation link is invalid.</p><a href="/">Back to Companies</a></main>;

  if (!selection.companyID) return <main className="projects-page projects-portal"><PortalHeader><p>Registered Companies and their Projects</p></PortalHeader><div className="portal-main"><p className="projects-eyebrow">01 / Project directory</p><h1>Choose a company to explore its projects.</h1><p className="portal-intro">Select a registered company to view its Projects and their available snapshot information.</p><section className="company-grid">{catalog.companies.map((item) => { const count = catalog.projects.filter((p) => p.company_id === item.id).length; return <button key={item.id} className="company-card" onClick={() => navigate({ company_id: item.id, project_id: null, page: null })}><span className="company-card-mark" aria-hidden="true">{item.name.slice(0, 1)}</span><small>COMPANY</small><strong>{item.name}</strong><span className="company-card-count">{count} registered {count === 1 ? "Project" : "Projects"}</span><span className="company-card-cta">Open company portfolio <b aria-hidden="true">↗</b></span></button>; })}</section>{!catalog.companies.length && <p>No Companies are registered yet.</p>}</div></main>;
  if (!selection.projectID) return <main className="projects-page projects-portal"><PortalHeader><p>Company portfolio · registered Projects</p></PortalHeader><div className="portal-main"><a className="portal-breadcrumb" href="/">← All Companies</a><div className="portfolio-hero"><div><p className="projects-eyebrow">02 / {company?.name}</p><h1>Explore registered Projects for {company?.name}.</h1><p className="portal-intro">Open a registered Project to review its available artifacts and snapshot information.</p></div><aside>Registered Projects<strong>{projects.length}</strong></aside></div><section className="project-cards" aria-label={`${company?.name} Projects`}>{projects.map((item) => <ProjectCard key={`${item.company_id}/${item.id}`} project={item} onOpen={() => navigate({ project_id: item.id, page: "overview" })} />)}</section>{!projects.length && <p>No Projects are registered under this Company yet.</p>}</div></main>;

  if ((!readerIndependentRoadPage && (viewState !== "ready" || !detail || !activeView)) || !detail) return <ProjectLoadingShell key={`${viewKey}/${reloadCounter}`} company={company!} project={project!} stage={loadStage} error={viewState === "error" ? (viewError === "registration" ? "Este Project tem um registro antigo inválido. Confirme o repositório e a branch Foundation publicados e registre o Project novamente. Não há migração nem leitura por compatibilidade." : "Não foi possível ler os dados publicados deste Project. Verifique a branch Foundation vinculada e o acesso do reader Delphi, depois tente novamente. Nenhum snapshot antigo é exibido como atual.") : ""} onRetry={() => setReloadCounter((value) => value + 1)} />;
  const previewProps = { companyID: selection.companyID, projectID: selection.projectID, companyName: company?.name || "Company", projectName: project?.name || "Project", detail, selection: selection.prototype, onNavigate: navigate };
  if (selection.prototype.mode === "presentation") return <div className="project-theme" style={activeView?.theme}><LocalArtifactPreview {...previewProps} /></div>;
  const tentativeProjects = catalog.projects.filter((item) => item.company_id === (pendingCompanyID || selection.companyID));
  const selectedTODOInBacklog = (roadData?.backlog?.todos || []).concat((roadData?.backlog?.folders || []).flatMap(flattenBacklogTodos)).some((todo) => todo.id === selection.todoID);
  const roadmapPending = roadmapPage && (roadmapRead.key !== viewKey || roadmapRead.state === "loading");
  return <main className="projects-page projects-project" style={activeView?.theme}>
    <div className="project-bar"><span className="project-bar-context">CONTEXTO</span>
      <label>COMPANY<select aria-label="Switch Company" value={pendingCompanyID || selection.companyID} onChange={(event) => setPendingCompanyID(event.target.value)}><option value={selection.companyID}>{company?.name}</option>{catalog.companies.filter((item) => item.id !== selection.companyID).map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}</select></label>
      <label>PROJECT<select aria-label="Switch Project" value={pendingCompanyID ? "" : project?.id} onChange={(event) => { if (event.target.value) navigate({ company_id: pendingCompanyID || selection.companyID, project_id: event.target.value, page: "overview" }); }}>{pendingCompanyID && <option value="">Choose a Project</option>}{tentativeProjects.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}</select></label>
      {pendingCompanyID && <button className="project-switch-cancel" onClick={() => setPendingCompanyID("")}>Cancel</button>}
    </div>
    <header className="project-topbar"><a className="project-topbar-brand" href={`/?company_id=${encodeURIComponent(selection.companyID)}&project_id=${encodeURIComponent(selection.projectID)}&page=overview`} onClick={(event) => { if (!event.ctrlKey && !event.metaKey && !event.shiftKey && event.button === 0) { event.preventDefault(); navigate({ page: "overview" }); } }}><span className="project-topbar-mark" aria-hidden="true">◆</span><span>Project {project?.name}</span></a></header>
    <div className="project-layout"><nav className="project-nav" aria-label="Project pages"><div className="project-nav-links">{pages.map((item) => <button key={item.id} className={page === item.id || (page === "release" && item.id === "roadmap") || (page === "todo" && item.id === (selectedTODOInBacklog ? "backlog" : "roadmap")) ? "active" : ""} aria-label={item.label} aria-current={page === item.id ? "page" : undefined} onClick={() => navigate({ page: item.id })}><span className="project-nav-icon" aria-hidden="true">{{overview:"⌂",roadmap:"▦",backlog:"☷",documents:"▤","design-system":"◈",prototypes:"▷"}[item.id]}</span>{pageDisplayNames[item.id]}</button>)}</div></nav>
      <section className="project-content">{activeView?.themeDiagnostic && <p className="project-theme-warning" role="alert">Design System indisponível: {activeView.themeDiagnostic} Os demais artefatos publicados continuam navegáveis.</p>}{page !== "overview" && page !== "roadmap" && page !== "backlog" && page !== "release" && page !== "todo" && <header className="project-page-heading"><span className="project-page-tag">{page === "documents" ? "PROJECT KNOWLEDGE · CANONICAL API" : page === "design-system" ? "BUILDER · IDENTIDADE DO PROJETO" : "BUILDER · PROTÓTIPOS DO PROJETO"}</span><h1>{page === "documents" ? "Documents & decisions" : pageDisplayNames[page as Page]}</h1>{page === "design-system" && <p>Uma linguagem visual aplicada a este projeto: cores, hierarquia e componentes derivados do seu Design System.</p>}{page === "prototypes" && <><p>Explore as telas e os estados de um protótipo registrado. A inspeção e a apresentação interativa são momentos distintos.</p><span className="prototype-page-note">Snapshot salvo · aprovação humana não inferida</span></>}</header>}
        {page === "overview" && <div className="project-overview"><OverviewEditorial detail={detail} projectName={project?.name || "Project"} />
          <SnapshotStatus detail={detail} />
          {roadData && <RoadmapView key={viewKey} roadmap={roadData} detail={detail} projectID={project!.id} epicID={selection.epicID} releaseID={selection.releaseID} compact />}
          {invalidArtifacts.length > 0 && <footer className="overview-status" aria-label="Unavailable artifacts">{invalidArtifacts.map((item) => <p className="project-note" key={`${item.kind}:${item.id}`}>{item.name}: {stateLabel(item.state)}{item.diagnostic ? ` · ${item.diagnostic}` : ""}</p>)}</footer>}</div>}
        {page === "documents" && <CanonicalDocuments key={viewKey} companyID={selection.companyID} projectID={selection.projectID} family={selection.documentFamily} recordID={selection.documentID} onSelect={(family, id) => navigate({ doc_family: family, doc_id: id })} />}
        {page === "design-system" && activeView?.theme && <DesignSystemView artifacts={available.filter((item) => item.kind === "design_system")} detail={detail} theme={activeView.theme} />}
        {page === "prototypes" && <LocalArtifactPreview {...previewProps} />}
        {roadFamilyPage && roadmapRead.key === viewKey && roadmapRead.state === "error" && <p className="project-notice roadmap-unavailable" role="alert">TEACH: Mongo Roadmap indisponível. Verifique a Registry API e recarregue; não é usado snapshot Foundation como fallback.</p>}
        {roadmapPending && <p className="project-notice" role="status">Carregando Roadmap atual da Registry…</p>}
        {page === "overview" && roadmapRead.key === viewKey && roadmapRead.state === "error" && <p className="project-notice roadmap-unavailable" role="alert">TEACH: Mongo Roadmap indisponível. O Overview permanece disponível, sem fallback para dados Foundation.</p>}
        {!roadmapPending && page === "roadmap" && <RoadmapView key={viewKey} roadmap={roadData} detail={detail} projectID={project!.id} epicID={selection.epicID} releaseID={selection.releaseID} />}
        {!roadmapPending && page === "backlog" && <BacklogView roadmap={roadData} detail={detail} projectID={project!.id} todoID={selection.todoID} />}
        {!roadmapPending && page === "release" && <ReleaseDetailView roadmap={roadData} companyID={selection.companyID} projectID={project!.id} epicID={selection.epicID} releaseID={selection.releaseID} todoID={selection.todoID} />}
        {!roadmapPending && page === "todo" && <TodoDetailView key={`${viewKey}/${selection.todoID}`} roadmap={roadData} companyID={selection.companyID} projectID={project!.id} todoID={selection.todoID} />}
      </section></div>
  </main>;
}
