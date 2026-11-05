import { useEffect, useRef, useState } from "react";
import type { MouseEvent } from "react";
import "./canonical-documents.css";

type Status = "current" | "planned" | "draft" | "deprecated" | "historical";
type Reference = { kind: string; ref: string };
type Rule = { id: string; status: Status; statement: string; applies_to?: string[] };
type Decision = Rule & { semantic_status?: string; rationale?: string; evidence?: Reference[]; replaces?: string[]; cleanup_trigger?: string };
type Question = { id: string; question: string; status: string; owner?: string; evidence?: Reference[] };
type KnowledgeState = { status?: Status; semantic_status?: string; open_questions?: Question[] };
type Condition = { field: string; operator: string; values?: string[] };
type Field = { name: string; type: string; required: boolean; description?: string; nullable?: boolean; enum?: string[]; format?: string; pattern?: string; minimum?: number; maximum?: number; min_length?: number; max_length?: number; min_items?: number; max_items?: number; location?: string; fields?: Field[]; items?: Field; schema_ref?: string; one_of?: Field[]; required_when?: Condition[] };
type IndexKey = { field: string; order: string };
type Security = { level?: string; authentication?: string; abilities?: string[]; principals?: string[]; trusted_proxy_required?: boolean; direct_origin_blocked?: boolean; observe_mode?: boolean; rejection_codes?: string[]; response_headers?: string[]; requirements?: Rule[] };
type Pagination = { mode: string; request_fields?: Field[]; response_fields?: Field[]; order?: IndexKey[]; default_size?: number; max_size?: number };
type Idempotency = { required: boolean; header?: string; replay_window_seconds?: number; scope?: string[]; conflict_status_code?: number; requirements?: Rule[] };
type Patch = { omitted_fields: string; null_fields: string; atomic: boolean };
type RouteOwnership = { route: string; host_scope: string; environment_type: string | null; main_scope: string | null; subscope: string | null; guard: string; screen_id?: string };
type Payloads = {
  project_mandate: KnowledgeState & { purpose: string; scope: string[]; principles?: string[]; exclusions?: string[] };
  domain_catalog: KnowledgeState & { entities: { id: string; name: string; description: string }[]; relations: { id: string; from: string; to: string; kind: string }[]; distinctions: { id: string; terms: string[]; reason: string }[] };
  project_constitution: KnowledgeState & { promotion_mode: string; capability_namespaces: string[]; rules: Rule[]; decisions?: Decision[] };
  strategic_roadmap: KnowledgeState & { stages: { id: string; name: string; status: Status; outcomes?: string[] }[] };
  module: KnowledgeState & {
    purpose: string; decisions?: Decision[]; interfaces?: Field[]; workflows?: Rule[]; scope?: string[]; exclusions?: string[];
    dependencies?: { kind: string; ref: string; responsibility: string }[]; routes?: RouteOwnership[];
    schemas?: { id: string; name: string; kind: string; entity_id?: string; fields: Field[]; indexes?: { id: string; keys: IndexKey[]; unique?: boolean; sparse?: boolean; expire_after_seconds?: number; partial_filter?: Condition[] }[]; invariants?: Rule[] }[];
    invariants?: Rule[]; local_operations?: { id: string; command: string; flags?: Field[]; exit_codes: { code: number; outcome: string; stdout?: string; stderr?: string; consumer_action: string }[]; response?: Field[]; digest?: { algorithm: string; fields: string[]; exclusions?: string[] }; side_effects?: string[]; limits?: Field[]; requirements?: Rule[] }[];
    events?: { name: string; direction: string; transport?: string; payload?: Field[]; requirements?: Rule[] }[];
    jobs?: { id: string; handler: string; schedule: string; timezone?: string; lifecycle_requirements?: string[] }[];
    observability?: { id: string; kind: string; name: string; fields?: Field[]; target?: string; comparison?: string; threshold?: number; unit?: string }[];
    verification?: { id: string; layer: string; requirement: string; commands?: string[] }[];
  };
  endpoint_contract: KnowledgeState & { purpose?: string; method: string; route: string; host_scope: string; module_id: string; request: Field[]; response: Field[]; errors?: Rule[]; security?: Security; pagination?: Pagination; idempotency?: Idempotency; patch?: Patch; responses?: { status_code: number; description: string; fields?: Field[]; headers?: Field[] }[]; policy_ids?: string[]; entity_ids?: string[] };
  policy: KnowledgeState & { purpose?: string; scope?: string[]; exclusions?: string[]; rules: Rule[]; definitions?: Field[]; ownership: { owner_kind: string; owner_id: string; decision_id: string }[]; decisions?: Decision[]; routes?: RouteOwnership[]; endpoint_conventions?: { base_path: string; id_format?: string; datetime_format?: string; tenant_id_required?: boolean; pagination?: Pagination; security?: Security; idempotency?: Idempotency; patch?: Patch } };
};
type Family = keyof Payloads;
type Entry = { family: Family; id: string; revision: number; content_digest: string };
type Manifest = { project_id: string; generation_id?: string; active: boolean; state: "active" | "pending"; teach?: string; documents: Entry[] };
type CanonicalRecord = { [F in Family]: { family: F; id: string; project_id: string; generation_id: string; revision: number; content_digest: string; updated_at: string; payload: Payloads[F] } }[Family];
type Read<T> = { key: string; state: "loading" | "ready" | "error" | "missing"; value?: T };
type IndexItem = Entry & { purpose?: string; method?: string; route?: string; host_scope?: string; module_id?: string; decisions?: { id: string; statement: string }[] };
type IndexPage = { project_id: string; generation_id: string; write_epoch: number; items: IndexItem[]; next_cursor?: string };
type Tab = "default" | "foundation" | "module" | "endpoint" | "decision" | "policy";
type StructuredValue = string | number | boolean | null | undefined | StructuredValue[] | { [key: string]: StructuredValue };

const families: Record<Family, string> = { project_mandate: "Project mandate", domain_catalog: "Domain catalog", project_constitution: "Project constitution", strategic_roadmap: "Strategic roadmap", module: "Module", endpoint_contract: "Endpoint contract", policy: "Policy" };
const idPattern = /^[a-z0-9][a-z0-9._-]{0,127}$/;
const digestPattern = /^sha256:[a-f0-9]{64}$/;
const validRevision = (revision: number) => Number.isSafeInteger(revision) && revision > 0;
const isFamily = (value: string): value is Family => Object.hasOwn(families, value);
const fieldLabel = (key: string) => key === "id" ? "ID" : key === "module_id" ? "Module ID" : key.replaceAll("_", " ").replace(/^./, (letter) => letter.toUpperCase());
const present = (value: StructuredValue) => value !== undefined;

// Typed family payloads are presented as semantic fields, never interpreted as
// Markdown or HTML. Nested contracts retain all authored values, including false,
// zero, null, open questions and explicit planned/historical status.
function StructuredFields({ value }: { value: StructuredValue }) {
  if (value === undefined) return null;
  if (value === null) return <span>Unspecified</span>;
  if (typeof value !== "object") return <span className="canonical-value">{String(value)}</span>;
  if (Array.isArray(value)) return value.length ? <ul className="canonical-values">{value.map((item, index) => <li key={typeof item === "object" && item && !Array.isArray(item) && typeof item.id === "string" ? item.id : index}><StructuredFields value={item} /></li>)}</ul> : <span>None declared</span>;
  return <dl className="canonical-fields">{Object.entries(value).filter(([, item]) => present(item)).map(([key, item]) => <div key={key}><dt>{fieldLabel(key)}</dt><dd><StructuredFields value={item} /></dd></div>)}</dl>;
}

function TypedDocument({ record }: { record: CanonicalRecord }) {
  const payload = record.payload;
  const endpoint = record.family === "endpoint_contract" ? payload as Payloads["endpoint_contract"] : null;
  const decisions = "decisions" in payload && Array.isArray(payload.decisions) ? payload.decisions as Decision[] : [];
  const companyID = new URLSearchParams(location.search).get("company_id") || "";
  return <article className="canonical-document" aria-label="Canonical document">
    <header className="doc-detail-head"><span className="doc-kicker">{families[record.family]}</span><h2>{endpoint ? `${endpoint.method} ${endpoint.route}` : record.id}</h2>{"purpose" in payload && payload.purpose && <p>{payload.purpose}</p>}<div className="doc-record-meta"><span>ID <strong>{record.id}</strong></span><span>Revisão <strong>{record.revision}</strong></span><span>Digest <strong>{record.content_digest}</strong></span><span>Geração <strong>{record.generation_id}</strong></span></div></header>
    <div className="doc-reading">{Object.entries(payload).filter(([key, value]) => key !== "purpose" && present(value)).map(([key, value]) => <section className="canonical-section" id={`field-${key}`} key={key}><h3>{fieldLabel(key)}</h3><StructuredFields value={value} /></section>)}{decisions.length > 0 && <section className="canonical-section"><h3>Decisões deste registro</h3>{decisions.map((decision) => { const query = new URLSearchParams({company_id:companyID,project_id:record.project_id,page:"documents",doc_family:record.family,doc_id:record.id,decision_id:decision.id}); return <a className="doc-row" href={`/?${query}`} key={decision.id}><span>◇</span><span><strong>Abrir decisão {decision.id}</strong><small>Owner {record.family}/{record.id}</small></span><b>→</b></a>; })}</section>}</div>
  </article>;
}

const foundationFamilies: Family[] = ["project_mandate", "domain_catalog", "project_constitution", "strategic_roadmap"];
const foundationNames: Record<string, string> = { project_mandate: "Mandato do projeto", domain_catalog: "Catálogo de domínio", project_constitution: "Constituição", strategic_roadmap: "Roadmap estratégico" };
const foundationNotes: Record<string, string> = { project_mandate: "Propósito, público e fronteiras.", domain_catalog: "Entidades e linguagem compartilhada.", project_constitution: "Invariantes e autoridade.", strategic_roadmap: "Direção de produto, não entregas." };
const tabForFamily = (value: string, decisionID: string): Tab => decisionID ? "decision" : foundationFamilies.includes(value as Family) ? "foundation" : value === "module" ? "module" : value === "endpoint_contract" ? "endpoint" : value === "policy" ? "policy" : "default";

export function CanonicalDocuments({ companyID, projectID, family, recordID, decisionID, onSelect, onOverview }: { companyID: string; projectID: string; family: string; recordID: string; decisionID: string; onSelect: (family: string, id: string, decisionID?: string) => void; onOverview: () => void }) {
  const [attempt, setAttempt] = useState(0);
  const [tab, setTab] = useState<Tab>(tabForFamily(family, decisionID));
  const [search, setSearch] = useState("");
  const [indexRead, setIndexRead] = useState<Read<IndexPage>>({ key: "", state: "loading" });
  const [loaded, setLoaded] = useState<IndexItem[]>([]);
  const [cursor, setCursor] = useState("");
  const pagination = useRef<AbortController | null>(null);
  const [indexEpoch, setIndexEpoch] = useState<number | null>(null);
  const [manifestRead, setManifestRead] = useState<Read<Manifest>>({ key: "", state: "loading" });
  const [recordRead, setRecordRead] = useState<Read<CanonicalRecord>>({ key: "", state: "loading" });
  const projectKey = `${companyID}/${projectID}`;
  const selectionKey = `${projectKey}/${family}/${recordID}`;
  const manifest = manifestRead.key === projectKey && manifestRead.state === "ready" ? manifestRead.value : undefined;
  const validSelection = Boolean(family && recordID && isFamily(family) && idPattern.test(recordID));
  useEffect(() => { if (recordID) setTab(tabForFamily(family, decisionID)); }, [family, recordID, decisionID]);
  useEffect(() => {
    const controller = new AbortController();
    setManifestRead({ key: projectKey, state: "loading" });
    const query = new URLSearchParams({ company_id: companyID, project_id: projectID });
    fetch(`/api/knowledge?${query}`, { signal: controller.signal }).then(async (response) => {
      if (!response.ok) throw new Error("Canonical manifest unavailable");
      const value = await response.json() as Manifest;
      if (value.project_id !== projectID || !Array.isArray(value.documents) || value.active && (value.state !== "active" || !value.generation_id || !idPattern.test(value.generation_id)) || !value.active && (value.state !== "pending" || value.generation_id || value.documents.length)) throw new Error("Canonical manifest identity invalid");
      const identities = new Set<string>();
      for (const entry of value.documents) {
        const key = `${entry.family}/${entry.id}`;
        if (!isFamily(entry.family) || !idPattern.test(entry.id) || !validRevision(entry.revision) || !digestPattern.test(entry.content_digest) || identities.has(key)) throw new Error("Canonical entry invalid");
        identities.add(key);
      }
      if (!controller.signal.aborted) setManifestRead({ key: projectKey, state: "ready", value });
    }).catch(() => { if (!controller.signal.aborted) setManifestRead({ key: projectKey, state: "error" }); });
    return () => controller.abort();
  }, [companyID, projectID, projectKey, attempt]);
  const moduleSelection = family === "module" && recordID ? recordID : "";
  const filterFamily = moduleSelection ? "endpoint_contract" : tab === "module" ? "module" : tab === "endpoint" ? "endpoint_contract" : tab === "policy" ? "policy" : "";
  const indexKey = `${projectKey}/${manifest?.generation_id || ""}/${tab}/${moduleSelection}/${search}/${attempt}`;
  useEffect(() => {
    pagination.current?.abort();
    pagination.current = null;
    if (!manifest?.active || tab === "default" && !search.trim() && !moduleSelection || recordID && !moduleSelection) return;
    const controller = new AbortController();
    setLoaded([]); setCursor(""); setIndexEpoch(null); setIndexRead({ key: indexKey, state: "loading" });
    const query = new URLSearchParams({ company_id: companyID, project_id: projectID, limit: "100" });
    if (filterFamily) query.set("family", filterFamily);
    if (moduleSelection) query.set("module_id", moduleSelection);
    if (tab === "decision") query.set("decisions_only", "true");
    if (search.trim()) query.set("q", search.trim());
    fetch(`/api/knowledge/index?${query}`, { signal: controller.signal }).then(async (response) => {
      if (!response.ok) throw new Error("Canonical index unavailable");
      const value = await response.json() as IndexPage;
      if (value.project_id !== projectID || value.generation_id !== manifest.generation_id || !Array.isArray(value.items) || value.items.length > 100 || !Number.isSafeInteger(value.write_epoch)) throw new Error("Canonical index identity invalid");
      for (const item of value.items) {
        const entry = manifest.documents.find((entry) => entry.family === item.family && entry.id === item.id);
        if (!entry || entry.revision !== item.revision || entry.content_digest !== item.content_digest) throw new Error("Canonical index revision changed");
      }
      if (!controller.signal.aborted) { setLoaded(value.items); setCursor(value.next_cursor || ""); setIndexEpoch(value.write_epoch); setIndexRead({ key: indexKey, state: "ready", value }); }
    }).catch(() => { if (!controller.signal.aborted) setIndexRead({ key: indexKey, state: "error" }); });
    return () => { controller.abort(); pagination.current?.abort(); pagination.current = null; };
  }, [companyID, projectID, manifest, filterFamily, moduleSelection, search, indexKey]);
  useEffect(() => {
    if (!manifest?.active || !validSelection) return;
    const controller = new AbortController();
    setRecordRead({ key: selectionKey, state: "loading" });
    const query = new URLSearchParams({ company_id: companyID, project_id: projectID, family, record_id: recordID });
    fetch(`/api/knowledge?${query}`, { signal: controller.signal }).then(async (response) => {
      if (response.status === 404) { if (!controller.signal.aborted) setRecordRead({ key: selectionKey, state: "missing" }); return; }
      if (!response.ok) throw new Error("Canonical document unavailable");
      const value = await response.json() as Omit<CanonicalRecord, "family">;
      const entry = manifest.documents.find((item) => item.family === family && item.id === recordID);
      if (!entry || value.id !== recordID || value.project_id !== projectID || value.generation_id !== manifest.generation_id || value.revision !== entry.revision || value.content_digest !== entry.content_digest || !value.payload || typeof value.payload !== "object" || Array.isArray(value.payload)) throw new Error("Canonical record identity/revision invalid");
      if (!controller.signal.aborted) setRecordRead({ key: selectionKey, state: "ready", value: { ...value, family } as CanonicalRecord });
    }).catch(() => { if (!controller.signal.aborted) setRecordRead({ key: selectionKey, state: "error" }); });
    return () => controller.abort();
  }, [companyID, projectID, family, recordID, selectionKey, validSelection, manifest]);
  const retry = <button type="button" onClick={() => setAttempt((current) => current + 1)}>Tentar novamente</button>;
  const heading = <header className="doc-heading"><div><span className="doc-eyebrow">CONHECIMENTO CANÔNICO · PROJECT {projectID.toUpperCase()}</span><h1>Documentação</h1><p>Definições, contratos e decisões no contexto em que foram criados.</p></div>{manifest?.active && <span className="doc-generation">● Geração ativa <small>Leitura por Project · {manifest.generation_id}</small></span>}</header>;
  const chooseTab = (next: Tab) => { pagination.current?.abort(); setTab(next); onOverview(); };
  const link = (entry: Entry, label: string, className = "doc-row", note = "") => {
    const query = new URLSearchParams({ company_id: companyID, project_id: projectID, page: "documents", doc_family: entry.family, doc_id: entry.id });
    const plainNote = note.trim().length <= 180 && !/[\r\n#*`]/.test(note) ? note.trim() : "";
    return <a className={className} key={`${entry.family}/${entry.id}`} href={`/?${query}`} onClick={(event) => select(event, entry)}><span aria-hidden="true">{entry.family === "policy" ? "⚑" : entry.family === "endpoint_contract" ? "⌁" : "▦"}</span><span><strong>{label}</strong><small>{plainNote || `${families[entry.family]} · revisão ${entry.revision}`}</small></span><b aria-hidden="true">→</b></a>;
  };
  const select = (event: MouseEvent<HTMLAnchorElement>, entry: Entry, selectedDecision?: string) => { if (event.button === 0 && !event.ctrlKey && !event.metaKey && !event.shiftKey && !event.altKey) { event.preventDefault(); onSelect(entry.family, entry.id, selectedDecision); } };
  const loadMore = async () => {
    if (!cursor || !manifest || indexRead.state !== "ready" || indexRead.key !== indexKey) return;
    const controller = new AbortController();
    pagination.current?.abort();
    pagination.current = controller;
    setIndexRead({ key: indexKey, state: "loading" });
    try {
      const query = new URLSearchParams({ company_id: companyID, project_id: projectID, limit: "100", cursor });
      if (filterFamily) query.set("family", filterFamily);
      if (moduleSelection) query.set("module_id", moduleSelection);
      if (tab === "decision") query.set("decisions_only", "true");
      if (search.trim()) query.set("q", search.trim());
      const response = await fetch(`/api/knowledge/index?${query}`, { signal: controller.signal });
      if (!response.ok) throw new Error("Index page unavailable");
      const value = await response.json() as IndexPage;
      if (value.project_id !== projectID || value.generation_id !== manifest.generation_id || value.write_epoch !== indexEpoch || !Array.isArray(value.items) || value.items.length > 100 || value.items.some((item) => !manifest.documents.some((entry) => entry.family === item.family && entry.id === item.id && entry.revision === item.revision && entry.content_digest === item.content_digest))) throw new Error("Index page changed");
      if (!controller.signal.aborted && pagination.current === controller) { setLoaded((current) => [...current, ...value.items]); setCursor(value.next_cursor || ""); setIndexRead({ key: indexKey, state: "ready", value }); }
    } catch { if (!controller.signal.aborted && pagination.current === controller) setIndexRead({ key: indexKey, state: "error" }); }
    finally { if (pagination.current === controller) pagination.current = null; }
  };
  if (manifestRead.key !== projectKey || manifestRead.state === "loading") return <div className="doc-experience">{heading}<p role="status">Carregando documentação canônica…</p></div>;
  if (!manifest) return <div className="doc-experience">{heading}<section className="doc-state-panel error"><h2>Não foi possível carregar a documentação</h2><p role="alert">TEACH: Verifique a API do Project e recarregue Documents. Nenhum conteúdo antigo será exibido.</p>{retry}</section></div>;
  if (!manifest.active) return <div className="doc-experience">{heading}<section className="doc-state-panel"><h2>Documentação ainda não iniciada</h2><p role="status">{manifest.teach || "TEACH: Importe e valide uma geração tipada e solicite a ativação explícita deste Project."}</p>{retry}</section></div>;
  const recordState = recordRead.key === selectionKey ? recordRead.state : "loading";
  const counts = (item: Family) => manifest.documents.filter((entry) => entry.family === item).length;
  const currentIndex = indexRead.key === indexKey;
  const visible: IndexItem[] = tab === "decision" ? loaded.filter((item) => item.decisions?.length) : tab === "foundation" ? (search.trim() ? currentIndex ? loaded : [] : manifest.documents).filter((item) => foundationFamilies.includes(item.family)) : loaded;
  const selected = validSelection && recordRead.value && recordState === "ready" ? recordRead.value : undefined;
  const selectedDecision = selected && decisionID && "decisions" in selected.payload ? selected.payload.decisions?.find((item) => item.id === decisionID) : undefined;
  return <div className="documents-page canonical-documents doc-experience">
    {heading}
    <div className="doc-toolbar"><label className="doc-search"><span aria-hidden="true">⌕</span><span className="sr-only">Buscar na documentação</span><input type="search" value={search} maxLength={120} onChange={(event) => { pagination.current?.abort(); setSearch(event.target.value); }} placeholder="Buscar módulo, rota ou decisão" /></label><span>Busque por ID, rota ou responsabilidade</span></div>
    <div className="doc-layout"><nav className="doc-families" aria-label="Famílias da documentação"><span className="doc-rail-label">EXPLORAR</span>{([ ["default", "⌂", "Visão geral"], ["foundation", "▤", "Fundamentos"], ["module", "▦", "Módulos"], ["endpoint", "⌁", "Endpoints"], ["decision", "◇", "Decisões"], ["policy", "⚑", "Políticas"] ] as [Tab,string,string][]).map(([id, icon, label]) => <button key={id} type="button" className={tab === id ? "selected" : ""} aria-current={tab === id ? "page" : undefined} onClick={() => chooseTab(id)}><span aria-hidden="true">{icon}</span>{label}</button>)}<p>O Roadmap estratégico é um documento. O Roadmap de entregas fica na navegação principal.</p></nav>
      <div className="doc-content" id="doc-content"><div className="doc-breadcrumb"><button type="button" onClick={() => chooseTab("default")}>{projectID} <span>›</span> Documentação</button>{tab !== "default" && <span>› {tab === "foundation" ? "Fundamentos" : tab === "module" ? "Módulos" : tab === "endpoint" ? "Endpoints" : tab === "decision" ? "Decisões" : "Políticas"}</span>}{recordID && <span>› {recordID}</span>}</div>
        {recordID ? (!validSelection ? <div className="doc-state-panel error" role="alert">TEACH: Link inválido. Selecione uma família e ID da lista atual.</div> : recordState === "loading" ? <p role="status">Loading document…</p> : recordState === "missing" ? <div className="doc-state-panel error" role="alert">TEACH: Documento não encontrado neste Project. Volte à lista atual.</div> : recordState === "error" ? <div className="doc-state-panel error" role="alert">TEACH: A revisão mudou ou a leitura falhou. Recarregue a documentação. {retry}</div> : selected ? <>{decisionID ? selectedDecision ? <article className="canonical-document"><header className="doc-detail-head"><span className="doc-kicker">DECISÃO · OWNER: {family}/{recordID}</span><h2>{selectedDecision.id}</h2><p>{selectedDecision.statement}</p><div className="doc-record-meta"><span>Revisão <strong>{selected.revision}</strong></span><span>Digest <strong>{selected.content_digest}</strong></span></div></header><div className="doc-reading"><StructuredFields value={selectedDecision as StructuredValue} /><button type="button" onClick={() => onSelect(family, recordID)}>← Voltar ao registro proprietário</button></div></article> : <div className="doc-state-panel error" role="alert">TEACH: Decisão não encontrada nesta revisão do owner.</div> : <TypedDocument record={selected} />}{family === "module" && !decisionID && <section className="doc-reading"><div className="doc-section-head"><h3>Endpoints deste módulo</h3></div>{loaded.filter((item) => item.family === "endpoint_contract" && item.module_id === recordID).map((item) => link(item, `${item.method} ${item.route}`))}{!loaded.some((item) => item.family === "endpoint_contract" && item.module_id === recordID) && <div className="doc-empty">Nenhum endpoint carregado para este módulo.</div>}{cursor && <button type="button" className="doc-more" onClick={loadMore}>Carregar mais endpoints</button>}</section>}</> : null) : tab === "default" && !search ? <section><div className="doc-intro"><span className="doc-kicker">VISÃO GERAL</span><h2>Conhecimento em contexto.</h2><p>Comece pelos fundamentos ou entre em um módulo para ver responsabilidade, decisões e endpoints vinculados.</p></div><div className="doc-metrics"><div><strong>{foundationFamilies.reduce((sum, item) => sum + counts(item), 0)}</strong><span>fundamentos</span></div><div><strong>{counts("module")}</strong><span>módulos</span></div><div><strong>{counts("endpoint_contract")}</strong><span>endpoints neste Project</span></div><div><strong>{counts("policy")}</strong><span>políticas</span></div></div><div className="doc-section-head"><h3>Fundamentos</h3><button onClick={() => chooseTab("foundation")}>Ver documentos →</button></div><div className="doc-foundations">{foundationFamilies.map((item, index) => { const entry = manifest.documents.find((value) => value.family === item); return entry && <a key={item} href={`/?${new URLSearchParams({company_id:companyID,project_id:projectID,page:"documents",doc_family:item,doc_id:entry.id})}`} onClick={(event) => select(event, entry)}><small>{String(index+1).padStart(2,"0")} / {families[item].toUpperCase()}</small><strong>{foundationNames[item]}</strong><span>{foundationNotes[item]}</span></a>; })}</div><div className="doc-section-head"><h3>Módulos</h3><button onClick={() => chooseTab("module")}>Explorar módulos →</button></div>{manifest.documents.filter((item) => item.family === "module").slice(0,2).map((entry) => link(entry, entry.id))}<div className="doc-section-head"><h3>Políticas e decisões</h3></div><div className="doc-split-links"><button onClick={() => chooseTab("decision")}>◇ Decisões no registro proprietário →</button><button onClick={() => chooseTab("policy")}>⚑ Explorar políticas →</button></div></section> : <section><div className="doc-intro"><span className="doc-kicker">{tab === "default" ? "RESULTADOS" : tab.toUpperCase()}</span><h2>{tab === "foundation" ? "Fundamentos do Project" : tab === "module" ? "Módulos e responsabilidades" : tab === "endpoint" ? "Contratos de endpoint" : tab === "decision" ? "Decisões no owner" : tab === "policy" ? "Políticas do Project" : "Resultados da busca"}</h2><p>Registros e vínculos derivados da API canônica, sem cópia de conteúdo.</p></div>{indexRead.state === "error" ? <div className="doc-state-panel error" role="alert">TEACH: O índice mudou ou está indisponível. Recarregue a página. {retry}</div> : <>{indexRead.state === "loading" && <p role="status">Buscando registros…</p>}{tab === "decision" ? visible.flatMap((owner) => (owner.decisions || []).map((decision) => { const query = new URLSearchParams({company_id:companyID, project_id:projectID,page:"documents",doc_family:owner.family,doc_id:owner.id,decision_id:decision.id}); return <a className="doc-row" key={`${owner.family}/${owner.id}/${decision.id}`} href={`/?${query}`} onClick={(event) => select(event, owner, decision.id)}><span>◇</span><span><strong>{decision.id}</strong><small>{owner.family}/{owner.id} · {decision.statement}</small></span><b>→</b></a>; })) : visible.map((entry) => link(entry, entry.family === "endpoint_contract" ? `${entry.method} ${entry.route}` : foundationNames[entry.family] || entry.id, "doc-row", entry.purpose || `${entry.family}/${entry.id} · revisão ${entry.revision}`))}{!visible.length && indexRead.state === "ready" && <div className="doc-empty">Nenhum registro encontrado.<small>Revise a busca ou selecione outra família.</small></div>}{cursor && indexRead.state === "ready" && <button type="button" className="doc-more" onClick={loadMore}>Carregar mais registros</button>}</>}</section>}
        <p className="doc-demo-note">Identidade, revisão e digest não significam aprovação humana.</p>
      </div><aside className="doc-on-page" aria-label="Nesta página"><span className="doc-rail-label">NESTA PÁGINA</span><a href="#doc-content">Visão geral</a>{selected && !decisionID && Object.keys(selected.payload).filter((key) => present((selected.payload as Record<string, StructuredValue>)[key])).slice(0,8).map((key) => <a key={key} href={`#field-${key}`}>{fieldLabel(key)}</a>)}<p>A localização acompanha o registro aberto e sua revisão.</p></aside></div>
  </div>;
}
