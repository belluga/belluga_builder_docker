import { useEffect, useRef, useState } from "react";
import "./prototype-viewer.css";

type Artifact = { id: string; kind: string; name: string; state: string; entry_point?: string; diagnostic?: string };
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
};
export type PrototypeDetail = {
  consumer_readiness: string; snapshot_fingerprint?: string; artifacts: Artifact[];
  snapshot_observations?: Record<string, unknown>; diagnostic?: string;
};
export type PrototypeSelection = {
  mode: string; artifact: string; prototypeID: string; screenID: string; stateID: string;
  scenarioID: string; scenarioStep: string; fingerprint: string; device: string; scale: string;
};
type Props = {
  companyID: string; projectID: string; companyName: string; projectName: string;
  detail: PrototypeDetail | null; selection: PrototypeSelection;
  onNavigate: (changes: Record<string, string | null>, replace?: boolean) => void;
};

function snapshotURL(companyID: string, projectID: string, fingerprint: string, path: string) {
  return `/snapshot/${encodeURIComponent(companyID)}/${encodeURIComponent(projectID)}/${fingerprint}/${path.split("/").map(encodeURIComponent).join("/")}`;
}

function admittedPrototypes(detail: PrototypeDetail | null): Prototype[] {
  if (detail?.consumer_readiness !== "ready" || !detail.snapshot_fingerprint) return [];
  const status = detail.snapshot_observations?.prototype_status as { outcome?: string; items?: Prototype[] } | undefined;
  if (status?.outcome !== "go" || !Array.isArray(status.items)) return [];
  const available = new Set(detail.artifacts.filter((item) => item.kind === "prototype" && item.state === "available" && item.entry_point).map((item) => item.id));
  return status.items.filter((item) => available.has(item.id));
}

function presentationURL(props: Props, prototype: Prototype, screen: Screen, state: State) {
  const query = new URLSearchParams({ company_id: props.companyID, project_id: props.projectID, page: "prototypes", mode: "presentation",
    artifact: `prototype:${prototype.id}`, prototype_id: prototype.id, screen_id: screen.id, state_id: state.id,
    fingerprint: props.detail?.snapshot_fingerprint || "", device: "mobile", scale: "fit" });
  return `/?${query}`;
}

function PrototypePresentation({ props, prototypes }: { props: Props; prototypes: Prototype[] }) {
  const { selection, detail, companyID, projectID, onNavigate } = props;
  const prototype = prototypes.find((item) => item.id === selection.prototypeID && selection.artifact === `prototype:${item.id}`);
  const screen = prototype?.screens.find((item) => item.id === selection.screenID);
  const state = screen?.states.find((item) => item.id === selection.stateID);
  const scenario = prototype?.scenarios.find((item) => item.id === selection.scenarioID);
  const stepIndex = /^\d+$/.test(selection.scenarioStep) ? Number(selection.scenarioStep) : -1;
  const step = scenario?.steps[stepIndex];
  const scenarioValid = (!selection.scenarioID && !selection.scenarioStep) || Boolean(scenario && step && step.screen_id === screen?.id && step.state_id === state?.id);
  const valid = Boolean(prototype && screen && state && /^[a-f0-9]{64}$/.test(selection.fingerprint) &&
    ["mobile", "desktop"].includes(selection.device) && ["fit", "100"].includes(selection.scale) && scenarioValid);
  const currentFingerprint = detail?.snapshot_fingerprint || "";
  const stale = Boolean(selection.fingerprint && currentFingerprint && selection.fingerprint !== currentFingerprint);
  const entry = valid && prototype && screen && state
    ? `${snapshotURL(companyID, projectID, selection.fingerprint, `${prototype.root}/${screen.path}`)}?prototype_state=${encodeURIComponent(state.id)}` : "";
  const [check, setCheck] = useState<{ key: string; state: "checking" | "ready" | "error"; message?: string } | null>(null);
  const entryKey = `${entry}|${currentFingerprint}`;
  useEffect(() => {
    if (!entry || stale || !currentFingerprint || detail?.consumer_readiness !== "ready") return;
    const controller = new AbortController();
    setCheck({ key: entryKey, state: "checking" });
    fetch(entry, { method: "HEAD", signal: controller.signal }).then((response) => {
      if (!response.ok || response.headers.get("X-Builder-Snapshot-Fingerprint") !== selection.fingerprint) throw new Error("The selected HTML is unavailable in this snapshot.");
      if (!controller.signal.aborted) setCheck({ key: entryKey, state: "ready" });
    }).catch((error: unknown) => {
      if (!controller.signal.aborted) setCheck({ key: entryKey, state: "error", message: error instanceof Error ? error.message : "Snapshot verification failed." });
    });
    return () => controller.abort();
  }, [currentFingerprint, detail?.consumer_readiness, entry, entryKey, selection.fingerprint, stale]);
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
  return <main className="prototype-presentation">
    <header className="prototype-presentation-bar">
      <a className="prototype-presentation-exit" href={`/?${inspector}`}>← Sair</a>
      <span className="prototype-presentation-context">{props.companyName} / {props.projectName}</span>
      <label>Protótipo<select aria-label="Protótipo" value={prototype?.id || ""} onChange={(event) => {
        const next = prototypes.find((item) => item.id === event.target.value);
        const nextScreen = next?.screens.find((item) => item.path === next.entry_point) || next?.screens[0];
        const nextState = nextScreen?.states.find((item) => item.id === nextScreen.default_state_id) || nextScreen?.states[0];
        choose({ artifact: next ? `prototype:${next.id}` : null, prototype_id: next?.id || null, screen_id: nextScreen?.id || null, state_id: nextState?.id || null, scenario_id: null, scenario_step: null });
      }}>{prototypes.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}</select></label>
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
      {!detail ? <p role="status">Carregando snapshot…</p>
        : stale ? <p role="alert">Snapshot alterado; reabra a apresentação pelo <a href={`/?${inspector}`}>inspetor</a>.</p>
          : !valid ? <p role="alert">Seleção inválida. Este link não identifica um Protótipo, Tela, Estado ou Cenário disponível.</p>
            : check?.key !== entryKey || check.state === "checking" ? <p role="status">Verificando entrada do snapshot…</p>
              : check.state === "error" ? <p role="alert">{check.message}</p>
                : <div className="prototype-presentation-frame" style={{ width: dimensions.width * factor, height: dimensions.height * factor }}><iframe key={entry} title={`${prototype?.name} · ${screen?.name} · ${state?.name}`} src={entry} sandbox="allow-scripts" referrerPolicy="no-referrer" style={{ width: dimensions.width, height: dimensions.height, transform: `scale(${factor})`, transformOrigin: "top left" }} /></div>}
    </section>
  </main>;
}

export function LocalArtifactPreview(props: Props) {
  const { detail, selection, companyID, projectID, onNavigate } = props;
  const prototypes = admittedPrototypes(detail);
  const requestedID = selection.artifact.startsWith("prototype:") ? selection.artifact.slice(10) : selection.prototypeID;
  const requested = prototypes.find((item) => item.id === requestedID);
  const [filter, setFilter] = useState<"active" | "archived" | "all">(requested?.status || "active");
  const [expanded, setExpanded] = useState("");
  const [imageError, setImageError] = useState(false);
  useEffect(() => { if (requested) setFilter(requested.status); }, [requested?.id, requested?.status]);
  const visible = prototypes.filter((item) => filter === "all" || item.status === filter);
  const prototype = requestedID ? requested : visible[0];
  const screen = selection.screenID ? prototype?.screens.find((item) => item.id === selection.screenID) : prototype?.screens.find((item) => item.path === prototype.entry_point) || prototype?.screens[0];
  const state = selection.stateID ? screen?.states.find((item) => item.id === selection.stateID) : screen?.states.find((item) => item.id === screen.default_state_id) || screen?.states[0];
  const invalidSelection = Boolean((selection.artifact && (!requestedID || selection.artifact !== `prototype:${requestedID}`)) ||
    (selection.prototypeID && selection.prototypeID !== requestedID) || (requestedID && !requested) ||
    (selection.screenID && !screen) || (selection.stateID && !state));
  const fingerprint = detail?.snapshot_fingerprint || "";
  const file = (path: string) => snapshotURL(companyID, projectID, fingerprint, path);
  const selectedImage = prototype && state ? file(`${prototype.root}/${state.identifier_image}`) : "";
  useEffect(() => { setImageError(false); }, [selectedImage]);
  if (selection.mode === "presentation") return <PrototypePresentation props={props} prototypes={prototypes} />;
  if (!detail) return <p role="status">Loading the saved Project snapshot…</p>;
  if (detail.consumer_readiness !== "ready") return <p role="alert">{detail.diagnostic || "The saved Project snapshot is not ready."}</p>;
  if (invalidSelection) return <section className="prototype-review" aria-label="Prototype inspector"><p role="alert">This Prototype, Screen or State selection is unavailable in the saved snapshot.</p><button onClick={() => onNavigate({ artifact: null, prototype_id: null, screen_id: null, state_id: null, scenario_id: null, scenario_step: null })}>Show available Prototypes</button></section>;
  const unavailable = detail.artifacts.find((item) => ["prototype", "prototype_collection"].includes(item.kind) && item.state === "invalid");
  const choosePrototype = (item: Prototype) => {
    setFilter(item.status); setExpanded("");
    onNavigate({ artifact: `prototype:${item.id}`, prototype_id: null, screen_id: null, state_id: null, scenario_id: null, scenario_step: null });
  };
  return <section className="prototype-review" aria-label="Prototype inspector">
    {unavailable && <p className="project-notice" role="alert">{unavailable.diagnostic || "The Prototype inventory is invalid."}</p>}
    <div className="prototype-review-layout">
      <aside className="prototype-review-list">
        <label>Status do protótipo<select aria-label="Status do protótipo" value={filter} onChange={(event) => { const next = event.target.value as "active" | "archived" | "all"; setFilter(next); if (requested && next !== "all" && requested.status !== next) onNavigate({ artifact: null, prototype_id: null, screen_id: null, state_id: null, scenario_id: null, scenario_step: null }); }}><option value="active">Ativos</option><option value="archived">Arquivados</option><option value="all">Todos</option></select></label>
        {visible.map((item) => <button key={item.id} className={prototype?.id === item.id ? "selected" : ""} onClick={() => choosePrototype(item)}>{item.name}<small>{item.status === "active" ? "Ativo" : "Arquivado"}</small></button>)}
        {!visible.length && <p>Nenhum protótipo {filter === "archived" ? "arquivado" : filter === "active" ? "ativo" : ""} disponível.</p>}
      </aside>
      <div className="prototype-review-body">
        {prototype && screen && state ? <>
          <div className="prototype-review-actions"><div><small>PROTÓTIPO · {prototype.status === "active" ? "ATIVO" : "ARQUIVADO"}</small><h3>{prototype.name}</h3><span>{prototype.authoring_mode === "image_first" ? "Exploração visual" : "Design System direto"} · snapshot {fingerprint.slice(0, 12)}</span></div><a href={presentationURL(props, prototype, screen, state)} target="_blank" rel="noopener noreferrer">Abrir apresentação do protótipo ↗</a></div>
          <div className="prototype-review-columns"><nav className="prototype-review-tree" aria-label="Telas e estados"><div className="prototype-review-tree-heading"><small>PROTÓTIPO · {prototype.status === "active" ? "ATIVO" : "ARQUIVADO"}</small><strong>Telas e estados</strong></div>{prototype.screens.map((item) => {
            const open = (expanded || screen.id) === item.id;
            return <div key={item.id}><button aria-expanded={open} className={screen.id === item.id ? "selected" : ""} onClick={() => {
              if (open) setExpanded("closed"); else { setExpanded(item.id); onNavigate({ artifact: `prototype:${prototype.id}`, screen_id: item.id, state_id: item.default_state_id }); }
            }}>{item.name}</button>{open && <div className="prototype-review-states">{item.states.map((candidate) => <button key={candidate.id} aria-pressed={screen.id === item.id && state.id === candidate.id} onClick={() => onNavigate({ artifact: `prototype:${prototype.id}`, screen_id: item.id, state_id: candidate.id })}>{candidate.name}</button>)}</div>}</div>;
          })}</nav>
          <section className="prototype-review-evidence" aria-label="State evidence"><div className="prototype-review-evidence-heading"><div><small>TELA · {screen.id}</small><h3>{screen.name} / {state.name}</h3></div><span>Estado padrão: {screen.default_state_id === state.id ? "sim" : "não"}</span></div>
            <figure><img key={selectedImage} src={selectedImage} alt={`Imagem identificadora · ${screen.name} · ${state.name}`} onError={() => setImageError(true)} /><figcaption>Imagem identificadora deste estado</figcaption></figure>
            {imageError && <p role="alert">Não foi possível carregar a imagem identificadora deste estado.</p>}
            {state.approved_references.map((reference) => <figure key={reference.id}><img src={file(`${prototype.root}/${reference.image}`)} alt={`Referência visual aprovada · ${reference.id}`} /><figcaption>{reference.id} · <a href={file(reference.approval_evidence)} target="_blank" rel="noopener noreferrer">Abrir evidência ↗</a></figcaption></figure>)}
            {!state.approved_references.length && <p>Nenhuma referência visual aprovada foi registrada para este estado.</p>}
            <h4>Transições deste estado</h4><ul>{prototype.transitions.filter((item) => item.from_screen_id === screen.id && item.from_state_id === state.id).map((item) => <li key={`${item.action}/${item.to_screen_id}/${item.to_state_id}`}>{item.action} → {item.to_screen_id} / {item.to_state_id}</li>)}</ul>
          </section></div>
        </> : <p role="status">No admitted Prototype is available in this saved snapshot.</p>}
      </div>
    </div>
  </section>;
}
