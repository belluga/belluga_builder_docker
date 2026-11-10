import { useEffect, useRef, useState } from "react";
import type { MouseEvent as ReactMouseEvent } from "react";
import "./prototype-viewer.css";

type Reference = { id: string; image: string; approval_evidence: string };
type State = { id: string; name: string; identifier_image: string; approved_references: Reference[] };
type Screen = { id: string; name: string; path: string; default_state_id: string; states: State[] };
type Step = { screen_id: string; state_id: string };
type Scenario = { id: string; name: string; steps: Step[] };
type Prototype = {
  id: string; name: string; root: string; status: "active" | "archived";
  authoring_mode: "image_first" | "design_system_first"; entry_point: string;
  screens: Screen[]; scenarios: Scenario[];
  transitions: { from_screen_id: string; from_state_id: string; action: string; to_screen_id: string; to_state_id: string }[];
  file_urls: { path: string; url: string }[];
};
type PrototypeRead = { revision: string; inventory_digest: string; item: Omit<Prototype, "file_urls">; file_urls: { path: string; url: string }[] };
type PrototypeCatalog = { revision: string; inventory_digest: string; items: { id: string; name: string; status: "active" | "archived"; detail_url: string }[] };
type PrototypeSummary = PrototypeCatalog["items"][number];
type ReaderState = { key: string; state: "loading" | "ready" | "error"; revision?: string; inventoryDigest?: string; summaries: PrototypeSummary[]; message?: string };
export type PrototypeSelection = {
  mode: string; artifact: string; prototypeID: string; screenID: string; stateID: string;
  scenarioID: string; scenarioStep: string; revision: string; device: string; scale: string;
};
type Props = {
  companyID: string; projectID: string; companyName: string; projectName: string;
  selection: PrototypeSelection;
  onNavigate: (changes: Record<string, string | null>, replace?: boolean) => void;
};

function fileURL(prototype: Prototype | undefined, path: string) {
  return prototype?.file_urls.find((item) => item.path === path)?.url || "";
}

function presentationURL(props: Props, prototype: Prototype, screen: Screen, state: State, revision: string) {
  const query = new URLSearchParams({ company_id: props.companyID, project_id: props.projectID, page: "prototypes", mode: "presentation",
    artifact: `prototype:${prototype.id}`, prototype_id: prototype.id, screen_id: screen.id, state_id: state.id,
    revision, device: "mobile", scale: "fit" });
  return `/?${query}`;
}

function PrototypePresentation({ props, prototypes, summaries, reader }: { props: Props; prototypes: Prototype[]; summaries: PrototypeSummary[]; reader: ReaderState }) {
  const { selection, companyID, projectID, onNavigate } = props;
  const prototype = prototypes.find((item) => item.id === selection.prototypeID && selection.artifact === `prototype:${item.id}`);
  const screen = prototype?.screens.find((item) => item.id === selection.screenID);
  const state = screen?.states.find((item) => item.id === selection.stateID);
  const scenario = prototype?.scenarios.find((item) => item.id === selection.scenarioID);
  const stepIndex = /^\d+$/.test(selection.scenarioStep) ? Number(selection.scenarioStep) : -1;
  const step = scenario?.steps[stepIndex];
  const scenarioValid = (!selection.scenarioID && !selection.scenarioStep) || Boolean(scenario && step && step.screen_id === screen?.id && step.state_id === state?.id);
  const valid = Boolean(prototype && screen && state && /^[a-f0-9]{40}$/.test(selection.revision) &&
    ["mobile", "desktop"].includes(selection.device) && ["fit", "100"].includes(selection.scale) && scenarioValid);
  const currentRevision = reader.revision || "";
  const stale = Boolean(selection.revision && currentRevision && selection.revision !== currentRevision);
  const entry = valid && prototype && screen && state
    ? (() => { const href = fileURL(prototype, `${prototype.root}/${screen.path}`); if (!href) return ""; const url = new URL(href, location.origin); url.searchParams.set("prototype_state", state.id); return url.pathname + url.search; })() : "";
  const [check, setCheck] = useState<{ key: string; state: "checking" | "ready" | "error"; message?: string } | null>(null);
  const entryKey = `${entry}|${currentRevision}|${reader.inventoryDigest || ""}`;
  useEffect(() => {
    if (!entry || stale || !currentRevision || reader.state !== "ready") return;
    const controller = new AbortController();
    setCheck({ key: entryKey, state: "checking" });
    fetch(entry, { method: "HEAD", signal: controller.signal }).then((response) => {
      if (!response.ok || response.headers.get("X-Prototype-Revision") !== selection.revision || response.headers.get("X-Prototype-Inventory-Digest") !== reader.inventoryDigest) throw new Error("The selected HTML is unavailable in this Git revision.");
      if (!controller.signal.aborted) setCheck({ key: entryKey, state: "ready" });
    }).catch((error: unknown) => {
      if (!controller.signal.aborted) setCheck({ key: entryKey, state: "error", message: error instanceof Error ? error.message : "Snapshot verification failed." });
    });
    return () => controller.abort();
  }, [currentRevision, entry, entryKey, reader.inventoryDigest, reader.state, selection.revision, stale]);
  const canvasRef = useRef<HTMLElement | null>(null);
  const [size, setSize] = useState({ width: window.innerWidth - 32, height: window.innerHeight - 96 });
  useEffect(() => {
    if (!canvasRef.current || typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(([entry]) => setSize({ width: entry.contentRect.width, height: entry.contentRect.height }));
    observer.observe(canvasRef.current);
    return () => observer.disconnect();
  }, []);
  const dimensions = selection.device === "desktop" ? { width: 1440, height: 900 } : { width: 390, height: 844 };
  const factor = selection.scale === "fit" ? Math.max(.1, Math.min(1, size.width / dimensions.width, size.height / dimensions.height)) : 1;
  const inspector = new URLSearchParams({ company_id: companyID, project_id: projectID, page: "prototypes", artifact: selection.artifact, screen_id: selection.screenID, state_id: selection.stateID });
  const choose = (changes: Record<string, string | null>) => onNavigate(changes, true);
  const exitPresentation = (event: ReactMouseEvent<HTMLAnchorElement>) => {
    if (event.ctrlKey || event.metaKey || event.shiftKey || event.button !== 0) return;
    event.preventDefault();
    onNavigate({ page: "prototypes", mode: null, prototype_id: null, scenario_id: null, scenario_step: null, revision: null, device: null, scale: null });
  };
  return <main className="prototype-presentation">
    <header className="prototype-presentation-bar">
      <a className="prototype-presentation-exit" href={`/?${inspector}`} onClick={exitPresentation}>← Sair</a>
      <span className="prototype-presentation-context">{props.companyName} / {props.projectName}</span>
      <label>Protótipo<select aria-label="Protótipo" value={prototype?.id || ""} onChange={(event) => {
        const next = prototypes.find((item) => item.id === event.target.value);
        const nextScreen = next?.screens.find((item) => item.path === next.entry_point) || next?.screens[0];
        const nextState = nextScreen?.states.find((item) => item.id === nextScreen.default_state_id) || nextScreen?.states[0];
        choose({ artifact: next ? `prototype:${next.id}` : null, prototype_id: next?.id || null, screen_id: nextScreen?.id || null, state_id: nextState?.id || null, scenario_id: null, scenario_step: null });
      }}>{summaries.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}</select></label>
      <label>Cenário<select aria-label="Cenário" value={scenario?.id || ""} onChange={(event) => {
        const next = prototype?.scenarios.find((item) => item.id === event.target.value);
        const first = next?.steps[0];
        choose({ scenario_id: next?.id || null, scenario_step: first ? "0" : null, screen_id: first?.screen_id || screen?.id || null, state_id: first?.state_id || state?.id || null });
      }}><option value="">Exploração manual</option>{(prototype?.scenarios || []).map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}</select></label>
      {scenario && step && <div className="prototype-presentation-scenario" role="group" aria-label="Etapas do cenário">
        <button disabled={stepIndex <= 0} onClick={() => { const target = scenario.steps[stepIndex - 1]; if (target) choose({ scenario_step: String(stepIndex - 1), screen_id: target.screen_id, state_id: target.state_id }); }}>Anterior</button>
        <span aria-live="polite">Etapa {stepIndex + 1} de {scenario.steps.length}</span>
        <button disabled={stepIndex >= scenario.steps.length - 1} onClick={() => { const target = scenario.steps[stepIndex + 1]; if (target) choose({ scenario_step: String(stepIndex + 1), screen_id: target.screen_id, state_id: target.state_id }); }}>Próxima</button>
      </div>}
      <label>Ir para tela<select aria-label="Ir para tela" value={screen?.id || ""} onChange={(event) => { const target = prototype?.screens.find((item) => item.id === event.target.value); choose({ screen_id: target?.id || null, state_id: target?.default_state_id || null, scenario_id: null, scenario_step: null }); }}>{(prototype?.screens || []).map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}</select></label>
      <label>Estado<select aria-label="Estado" value={state?.id || ""} onChange={(event) => choose({ state_id: event.target.value, scenario_id: null, scenario_step: null })}>{(screen?.states || []).map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}</select></label>
      <div className="prototype-presentation-toggle" role="group" aria-label="Dispositivo"><button aria-pressed={selection.device === "mobile"} onClick={() => choose({ device: "mobile" })}>Mobile</button><button aria-pressed={selection.device === "desktop"} onClick={() => choose({ device: "desktop" })}>Desktop</button></div>
      <div className="prototype-presentation-toggle" role="group" aria-label="Escala"><button aria-pressed={selection.scale === "fit"} onClick={() => choose({ scale: "fit" })}>Ajustar</button><button aria-pressed={selection.scale === "100"} onClick={() => choose({ scale: "100" })}>100%</button></div>
      {check?.key === entryKey && check.state === "ready" && entry ? <a href={entry} target="_blank" rel="noopener noreferrer">Abrir HTML bruto ↗</a> : <span aria-disabled="true">Abrir HTML bruto indisponível</span>}
    </header>
    <section ref={canvasRef} className="prototype-presentation-canvas" aria-label={`${props.companyName} / ${props.projectName} · apresentação`}>
      {reader.state === "loading" ? <p role="status">Carregando catálogo Git…</p>
        : reader.state === "error" ? <p role="alert">{reader.message || "O catálogo atual de Prototypes está indisponível."}</p>
          : stale ? <p role="alert">A revisão Git mudou; reabra a apresentação pelo <a href={`/?${inspector}`} onClick={exitPresentation}>inspetor</a>.</p>
          : !valid ? <p role="alert">Seleção inválida. Este link não identifica um Protótipo, Tela, Estado ou Cenário disponível.</p>
            : check?.key !== entryKey || check.state === "checking" ? <p role="status">Verificando arquivo na revisão Git…</p>
              : check.state === "error" ? <p role="alert">{check.message}</p>
                : <div className="prototype-presentation-frame" style={{ width: dimensions.width * factor, height: dimensions.height * factor }}><iframe key={`${entry}|${selection.scenarioID}|${selection.scenarioStep}`} title={`${prototype?.name} · ${screen?.name} · ${state?.name}`} src={entry} sandbox="allow-scripts" referrerPolicy="no-referrer" style={{ width: dimensions.width, height: dimensions.height, transform: `scale(${factor})`, transformOrigin: "top left" }} /></div>}
    </section>
  </main>;
}

export function LocalArtifactPreview(props: Props) {
  const { selection, companyID, projectID, onNavigate } = props;
  const readerKey = `${companyID}/${projectID}`;
  const [reader, setReader] = useState<ReaderState>({ key: "", state: "loading", summaries: [] });
  const [details, setDetails] = useState<Record<string, Prototype>>({});
  const [retryCount, setRetryCount] = useState(0);
  useEffect(() => {
    if (!companyID || !projectID) { setReader({ key: readerKey, state: "error", summaries: [], message: "Company and Project are required." }); return; }
    const controller = new AbortController();
    setReader({ key: readerKey, state: "loading", summaries: [] });
    setDetails({});
    async function load() {
      try {
        const query = new URLSearchParams({ company_id: companyID, project_id: projectID });
        const response = await fetch(`/api/prototypes?${query}`, { signal: controller.signal });
        if (!response.ok) throw new Error("The current Prototype catalog is unavailable.");
        const catalog = await response.json() as PrototypeCatalog;
        if (!/^[a-f0-9]{40}$/.test(catalog.revision) || !/^sha256:[a-f0-9]{64}$/.test(catalog.inventory_digest) || !Array.isArray(catalog.items)) throw new Error("The Prototype catalog identity is invalid.");
        if (catalog.items.some((summary) => !summary || typeof summary.id !== "string" || !["active", "archived"].includes(summary.status) || typeof summary.detail_url !== "string" || !summary.detail_url.startsWith(`/api/prototypes/${catalog.revision}/`))) throw new Error("The Prototype catalog item is invalid.");
        if (!controller.signal.aborted) setReader({ key: readerKey, state: "ready", revision: catalog.revision, inventoryDigest: catalog.inventory_digest, summaries: catalog.items });
      } catch (error) {
        if (!controller.signal.aborted) setReader({ key: readerKey, state: "error", summaries: [], message: error instanceof Error ? error.message : "The current Prototype catalog could not be read." });
      }
    }
    void load();
    return () => controller.abort();
  }, [companyID, projectID, readerKey, retryCount]);
  const currentReader = reader.key === readerKey ? reader : { key: readerKey, state: "loading" as const, summaries: [] };
  const requestedID = selection.artifact.startsWith("prototype:") ? selection.artifact.slice(10) : selection.prototypeID;
  const [filter, setFilter] = useState<"active" | "archived" | "all">("active");
  const visibleSummaries = currentReader.summaries.filter((item) => filter === "all" || item.status === filter);
  const selectedSummary = requestedID ? currentReader.summaries.find((item) => item.id === requestedID) : visibleSummaries[0];
  useEffect(() => {
    if (currentReader.state !== "ready" || !selectedSummary || details[selectedSummary.id]) return;
    const controller = new AbortController();
    const expectedRevision = currentReader.revision;
    const expectedDigest = currentReader.inventoryDigest || "";
    async function loadDetail() {
      try {
        const response = await fetch(selectedSummary!.detail_url, { signal: controller.signal });
        if (!response.ok) throw new Error("The selected Prototype detail is unavailable.");
        const value = await response.json() as PrototypeRead;
		const filePrefix = `/api/prototypes/file/${encodeURIComponent(companyID)}/${encodeURIComponent(projectID)}/${expectedRevision}/${expectedDigest.slice(7)}/files/`;
		if (value.revision !== expectedRevision || value.inventory_digest !== expectedDigest || value.item?.id !== selectedSummary!.id || !Array.isArray(value.file_urls) || !value.file_urls.length || value.file_urls.some((file) => typeof file.path !== "string" || typeof file.url !== "string" || !file.url.startsWith(filePrefix))) throw new Error("Prototype detail revision or file links do not match the catalog.");
        if (!controller.signal.aborted) setDetails((old) => ({ ...old, [selectedSummary!.id]: { ...value.item, file_urls: value.file_urls } }));
      } catch (error) {
        if (!controller.signal.aborted) setReader((old) => ({ ...old, state: "error", message: error instanceof Error ? error.message : "The selected Prototype detail could not be read." }));
      }
    }
    void loadDetail();
    return () => controller.abort();
  }, [currentReader.inventoryDigest, currentReader.revision, currentReader.state, details, selectedSummary?.detail_url, selectedSummary?.id]);
  const prototypes = Object.values(details);
  const requested = requestedID ? details[requestedID] : undefined;
  const selectedPrototype = selectedSummary ? details[selectedSummary.id] : undefined;
  const [expanded, setExpanded] = useState("");
  const [imageError, setImageError] = useState(false);
  useEffect(() => { if (requested) setFilter(requested.status); }, [requested?.id, requested?.status]);
  const visible = prototypes.filter((item) => filter === "all" || item.status === filter);
  const prototype = requested || selectedPrototype || visible[0];
  const screen = selection.screenID ? prototype?.screens.find((item) => item.id === selection.screenID) : prototype?.screens.find((item) => item.path === prototype.entry_point) || prototype?.screens[0];
  const state = selection.stateID ? screen?.states.find((item) => item.id === selection.stateID) : screen?.states.find((item) => item.id === screen.default_state_id) || screen?.states[0];
  const invalidSelection = Boolean((selection.artifact && (!requestedID || selection.artifact !== `prototype:${requestedID}`)) ||
    (selection.prototypeID && selection.prototypeID !== requestedID) || (requestedID && !requested) ||
    (selection.screenID && !screen) || (selection.stateID && !state));
  const file = (path: string) => fileURL(prototype, path);
  const selectedImage = prototype && state ? file(`${prototype.root}/${state.identifier_image}`) : "";
  useEffect(() => { setImageError(false); }, [selectedImage]);
  if (currentReader.state === "loading") return <p role="status">Carregando o catálogo de Prototypes da revisão Git…</p>;
  if (currentReader.state === "error") return <section className="prototype-review" aria-label="Prototype inspector"><p role="alert">{currentReader.message || "O catálogo atual de Prototypes está indisponível."}</p><button onClick={() => setRetryCount((value) => value + 1)}>Tentar novamente</button></section>;
  if (selectedSummary && !details[selectedSummary.id] && currentReader.state === "ready") return <p role="status">Carregando detalhe do Prototype na revisão Git…</p>;
  if (selection.mode === "presentation") return <PrototypePresentation props={props} prototypes={prototypes} summaries={currentReader.summaries} reader={currentReader} />;
  if (invalidSelection) return <section className="prototype-review" aria-label="Prototype inspector"><p role="alert">This Prototype, Screen or State selection is unavailable in the selected Git revision.</p><button onClick={() => onNavigate({ artifact: null, prototype_id: null, screen_id: null, state_id: null, scenario_id: null, scenario_step: null, revision: null })}>Show available Prototypes</button></section>;
  const choosePrototype = (item: Pick<PrototypeSummary, "id" | "status">) => {
    setFilter(item.status); setExpanded("");
    onNavigate({ artifact: `prototype:${item.id}`, prototype_id: null, screen_id: null, state_id: null, scenario_id: null, scenario_step: null, revision: currentReader.revision || null });
  };
  return <section className="prototype-review" aria-label="Prototype inspector">
    <div className="prototype-review-layout">
      <aside className="prototype-review-list">
        <label>Status do protótipo<select aria-label="Status do protótipo" value={filter} onChange={(event) => { const next = event.target.value as "active" | "archived" | "all"; setFilter(next); if (requested && next !== "all" && requested.status !== next) onNavigate({ artifact: null, prototype_id: null, screen_id: null, state_id: null, scenario_id: null, scenario_step: null, revision: null }); }}><option value="active">Ativos</option><option value="archived">Arquivados</option><option value="all">Todos</option></select></label>
        {visibleSummaries.map((item) => <button key={item.id} className={prototype?.id === item.id ? "selected" : ""} onClick={() => choosePrototype(item)}>{item.name}<small>{item.status === "active" ? "Ativo" : "Arquivado"}</small></button>)}
        {!visible.length && <p>Nenhum protótipo {filter === "archived" ? "arquivado" : filter === "active" ? "ativo" : ""} disponível.</p>}
      </aside>
      <div className="prototype-review-body">
        {prototype && screen && state ? <>
          <div className="prototype-review-actions"><div><small>PROTÓTIPO · {prototype.status === "active" ? "ATIVO" : "ARQUIVADO"}</small><h3>{prototype.name}</h3><span>{prototype.authoring_mode === "image_first" ? "Exploração visual" : "Design System direto"} · revisão Git {(currentReader.revision || "").slice(0, 12)}</span></div><a href={presentationURL(props, prototype, screen, state, currentReader.revision || "")} target="_blank" rel="noopener noreferrer">Abrir apresentação do protótipo ↗</a></div>
          <div className="prototype-review-columns"><nav className="prototype-review-tree" aria-label="Telas e estados"><div className="prototype-review-tree-heading"><small>PROTÓTIPO · {prototype.status === "active" ? "ATIVO" : "ARQUIVADO"}</small><strong>Telas e estados</strong></div>{prototype.screens.map((item) => {
            const open = (expanded || screen.id) === item.id;
            return <div key={item.id}><button aria-expanded={open} className={screen.id === item.id ? "selected" : ""} onClick={() => {
              if (open) setExpanded("closed"); else { setExpanded(item.id); onNavigate({ artifact: `prototype:${prototype.id}`, screen_id: item.id, state_id: item.default_state_id }); }
            }}>{item.name}</button>{open && <div className="prototype-review-states">{item.states.map((candidate) => <button key={candidate.id} aria-pressed={screen.id === item.id && state.id === candidate.id} onClick={() => onNavigate({ artifact: `prototype:${prototype.id}`, screen_id: item.id, state_id: candidate.id })}>{candidate.name}</button>)}</div>}</div>;
          })}</nav>
          <section className="prototype-review-evidence" aria-label="State evidence"><div className="prototype-review-evidence-heading"><div><small>TELA · {screen.id}</small><h3>{screen.name} / {state.name}</h3></div><span>Estado padrão: {screen.default_state_id === state.id ? "sim" : "não"}</span></div>
            <figure><img key={selectedImage} src={selectedImage} alt={`Imagem identificadora · ${screen.name} · ${state.name}`} onError={() => setImageError(true)} /><figcaption>Imagem identificadora deste estado</figcaption></figure>
            {imageError && <p role="alert">Não foi possível carregar a imagem identificadora deste estado.</p>}
            {state.approved_references.map((reference) => {
              const isTODOPath = reference.approval_evidence.startsWith("todos/");
              return <figure key={reference.id}><img src={file(`${prototype.root}/${reference.image}`)} alt={`Referência visual aprovada · ${reference.id}`} /><figcaption>{reference.id} · {file(reference.approval_evidence) ? <a href={file(reference.approval_evidence)} target="_blank" rel="noopener noreferrer">{isTODOPath ? "Abrir evidência relacionada" : "Abrir evidência"} ↗</a> : <span aria-disabled="true">Evidência indisponível</span>}</figcaption></figure>;
            })}
            {!state.approved_references.length && <p>Nenhuma referência visual aprovada foi registrada para este estado.</p>}
            <h4>Transições deste estado</h4><ul>{prototype.transitions.filter((item) => item.from_screen_id === screen.id && item.from_state_id === state.id).map((item) => <li key={`${item.action}/${item.to_screen_id}/${item.to_state_id}`}>{item.action} → {item.to_screen_id} / {item.to_state_id}</li>)}</ul>
          </section></div>
        </> : selectedSummary ? <p role="status">Carregando telas e estados do Prototype selecionado…</p> : <p role="status">Nenhum Prototype está disponível na revisão Git publicada.</p>}
      </div>
    </div>
  </section>;
}
