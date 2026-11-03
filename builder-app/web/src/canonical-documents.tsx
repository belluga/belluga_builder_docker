import { useEffect, useState } from "react";
import type { MouseEvent } from "react";
import "./canonical-documents.css";

type Status = "current" | "planned" | "deprecated" | "historical";
type Reference = { kind: string; ref: string };
type Rule = { id: string; status: Status; statement: string; applies_to?: string[] };
type Decision = Rule & { semantic_status?: string; rationale?: string; evidence?: Reference[]; replaces?: string[]; cleanup_trigger?: string };
type Question = { id: string; question: string; status: string; owner?: string; evidence?: Reference[] };
type KnowledgeState = { status?: Status; semantic_status?: string; open_questions?: Question[] };
type Condition = { field: string; operator: string; values?: string[] };
type Field = { name: string; type: string; required: boolean; description?: string; nullable?: boolean; enum?: string[]; format?: string; pattern?: string; minimum?: number; maximum?: number; min_length?: number; max_length?: number; min_items?: number; max_items?: number; location?: string; fields?: Field[]; items?: Field; schema_ref?: string; required_when?: Condition[] };
type IndexKey = { field: string; order: string };
type Security = { level?: string; authentication?: string; abilities?: string[]; principals?: string[]; trusted_proxy_required?: boolean; direct_origin_blocked?: boolean; observe_mode?: boolean; rejection_codes?: string[]; response_headers?: string[]; requirements?: Rule[] };
type Pagination = { mode: string; request_fields?: Field[]; response_fields?: Field[]; order?: IndexKey[]; default_size?: number; max_size?: number };
type Idempotency = { required: boolean; header?: string; replay_window_seconds?: number; scope?: string[]; conflict_status_code?: number; requirements?: Rule[] };
type Patch = { omitted_fields: string; null_fields: string; atomic: boolean };
type RouteOwnership = { route: string; host_scope: string; environment_type: string | null; main_scope: string | null; subscope: string | null; guard: string; screen_id?: string };
type Payloads = {
  project_mandate: KnowledgeState & { purpose: string; scope: string[]; principles?: string[]; exclusions?: string[] };
  domain_catalog: KnowledgeState & { entities: { id: string; name: string; description: string }[]; relations: { id: string; from: string; to: string; kind: string }[]; distinctions: { id: string; left: string; right: string; reason: string }[] };
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
type StructuredValue = string | number | boolean | null | undefined | StructuredValue[] | { [key: string]: StructuredValue };

const families: Record<Family, string> = { project_mandate: "Project mandate", domain_catalog: "Domain catalog", project_constitution: "Project constitution", strategic_roadmap: "Strategic roadmap", module: "Module", endpoint_contract: "Endpoint contract", policy: "Policy" };
const idPattern = /^[a-z0-9][a-z0-9._-]{0,127}$/;
const digestPattern = /^sha256:[a-f0-9]{64}$/;
const validRevision = (revision: number) => Number.isSafeInteger(revision) && revision > 0;
const isFamily = (value: string): value is Family => Object.hasOwn(families, value);
const fieldLabel = (key: string) => key === "id" ? "ID" : key === "module_id" ? "Module ID" : key.replaceAll("_", " ").replace(/^./, (letter) => letter.toUpperCase());
const present = (value: StructuredValue) => value !== undefined && (!Array.isArray(value) || value.length > 0);

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
  // The family union keeps distinct contracts; common sections can share one
  // visual presentation without a second generic document/schema authority.
  return <article className="canonical-document" aria-label="Canonical document">
    <header><span className="project-page-tag">{families[record.family]}</span><h2>{record.id}</h2><p>Project {record.project_id} · Generation {record.generation_id} · Revision {record.revision}</p><code>{record.content_digest}</code><p>Updated {record.updated_at}</p></header>
    {Object.entries(payload).filter(([, value]) => present(value)).map(([key, value]) => <section className="canonical-section" key={key}><h3>{fieldLabel(key)}</h3><StructuredFields value={value} /></section>)}
  </article>;
}

export function CanonicalDocuments({ companyID, projectID, family, recordID, onSelect }: { companyID: string; projectID: string; family: string; recordID: string; onSelect: (family: string, id: string) => void }) {
  const [attempt, setAttempt] = useState(0);
  const [manifestRead, setManifestRead] = useState<Read<Manifest>>({ key: "", state: "loading" });
  const [recordRead, setRecordRead] = useState<Read<CanonicalRecord>>({ key: "", state: "loading" });
  const projectKey = `${companyID}/${projectID}`;
  const selectionKey = `${projectKey}/${family}/${recordID}`;
  const manifest = manifestRead.key === projectKey && manifestRead.state === "ready" ? manifestRead.value : undefined;
  const validSelection = Boolean(family && recordID && isFamily(family) && idPattern.test(recordID));
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
  if (manifestRead.key !== projectKey || manifestRead.state === "loading") return <p role="status">Loading canonical Documents…</p>;
  if (!manifest) return <section className="project-notice"><p role="alert">TEACH: Conhecimento canônico indisponível. Verifique a API do Project e recarregue Documents.</p>{retry}</section>;
  if (!manifest.active) return <section className="project-notice"><h2>Conhecimento canônico pending</h2><p role="status">{manifest.teach || "TEACH: Importe e valide uma geração tipada e solicite a ativação explícita deste Project."}</p>{retry}</section>;
  const recordState = recordRead.key === selectionKey ? recordRead.state : "loading";
  const select = (event: MouseEvent<HTMLAnchorElement>, entry: Entry) => { if (event.button === 0 && !event.ctrlKey && !event.metaKey && !event.shiftKey && !event.altKey) { event.preventDefault(); onSelect(entry.family, entry.id); } };
  return <div className="documents-page canonical-documents">
    <p className="documents-page-intro">Conhecimento canônico do Project {projectID} · Generation {manifest.generation_id}. Selecione um documento para ler seu contrato tipado.</p>
    <section className="document-list" aria-label="Canonical Documents">{manifest.documents.map((entry) => {
      const query = new URLSearchParams({ company_id: companyID, project_id: projectID, page: "documents", doc_family: entry.family, doc_id: entry.id });
      return <article className="document-row" key={`${entry.family}/${entry.id}`}><div><h2>{families[entry.family]} · {entry.id}</h2><p>Revision {entry.revision}</p><code>{entry.content_digest}</code></div><a href={`/?${query}`} aria-current={entry.family === family && entry.id === recordID ? "true" : undefined} onClick={(event) => select(event, entry)}>Read document <span aria-hidden="true">→</span></a></article>;
    })}{!manifest.documents.length && <p>Nenhum documento canônico nesta geração.</p>}</section>
    {(family || recordID) && (!validSelection ? <p className="project-notice" role="alert">TEACH: Link de documento inválido. Selecione uma família e um ID na lista atual.</p> : recordState === "loading" ? <p role="status">Loading document…</p> : recordState === "missing" ? <p className="project-notice" role="alert">TEACH: Documento não encontrado neste Project (404). Selecione um ID na lista atual.</p> : recordState === "error" ? <section className="project-notice"><p role="alert">TEACH: Não foi possível ler a revisão deste documento. Recarregue o manifesto e tente novamente.</p>{retry}</section> : recordRead.value && <TypedDocument record={recordRead.value} />)}
    <section className="documents-note"><h2>Reading is not review</h2><p>Identidade, revisão e digest identificam o conteúdo lido. Disponibilidade não indica aprovação humana.</p></section>
  </div>;
}
