import { useEffect, useState } from "react";

type DesignToken = { id: string; type: string; value?: unknown; ref?: string };
type DesignComponent = { id: string; name: string; documentation?: string; token_ids: string[]; states: string[]; variants: { id: string; name: string; token_ids: string[] }[]; examples: string[] };
type Definition = { id: string; name: string; owner: { level: string; id: string }; tokens: DesignToken[]; components: DesignComponent[]; assets: string[] };
type ReadResponse = {
  selection: { state: string; sha?: string; inventory_digest?: string; binding_revision: number };
  evaluation: { project_id: string; company_id: string; mode: string; revision: string | null; outcome: string; inventory_digest: string | null; items: Definition[]; design_system_validation: string; selected_level?: string };
};
type ReadState = { key: string; state: "loading" | "ready" | "error"; value?: ReadResponse; teach?: string };

async function responseTeach(response: Response): Promise<string | undefined> {
  try {
    const payload = await response.json() as { teach?: unknown };
    const teach = payload?.teach;
    if (typeof teach !== "string" || teach.length > 512 || !teach.startsWith("TEACH: ") || /[\u0000-\u001f\u007f/\\]|:\/\//.test(teach)) return undefined;
    return teach;
  } catch {
    return undefined;
  }
}

function assetURL(companyID: string, projectID: string, revision: string, asset: string) {
  const query = new URLSearchParams({ company_id: companyID, project_id: projectID, revision, path: asset });
  return `/api/design-system/assets?${query}`;
}

function displayValue(value: unknown): string {
  if (typeof value === "string") return value;
  if (typeof value === "number" || typeof value === "boolean") return String(value);
  return value === undefined ? "—" : JSON.stringify(value);
}

export function DesignSystemViewer({ companyID, projectID }: { companyID: string; projectID: string }) {
  const key = `${companyID}/${projectID}`;
  const [read, setRead] = useState<ReadState>({ key: "", state: "loading" });
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    const controller = new AbortController();
    setRead({ key, state: "loading" });
    const query = new URLSearchParams({ company_id: companyID, project_id: projectID });
    fetch(`/api/design-system?${query}`, { signal: controller.signal }).then(async (response) => {
      if (!response.ok) throw new Error(await responseTeach(response) || "TEACH: O Design System selecionado está indisponível. Recarregue a seleção atual da Registry.");
      const value = await response.json() as ReadResponse;
      const evaluation = value.evaluation;
      if (evaluation.company_id !== companyID || evaluation.project_id !== projectID || evaluation.mode !== "committed" || evaluation.outcome !== "go" || evaluation.design_system_validation !== "valid" || !evaluation.inventory_digest || !evaluation.revision || !/^[a-f0-9]{40}$/.test(evaluation.revision) || !Array.isArray(evaluation.items) || evaluation.items.length !== 1 || value.selection.state === "selected" && (value.selection.sha !== evaluation.revision || value.selection.inventory_digest !== evaluation.inventory_digest)) {
        throw new Error("Design System identity or selected revision changed");
      }
      const definition = evaluation.items[0];
      if (!definition || typeof definition.name !== "string" || !definition.owner || typeof definition.owner.level !== "string" || !Array.isArray(definition.tokens) || !Array.isArray(definition.components) || !Array.isArray(definition.assets) || definition.components.some((component) => !component || typeof component.id !== "string" || typeof component.name !== "string" || !Array.isArray(component.states))) {
        throw new Error("Design System response shape is invalid");
      }
      return value;
    }).then((value) => { if (!controller.signal.aborted) setRead({ key, state: "ready", value }); })
      .catch((error: unknown) => { if (!controller.signal.aborted) setRead({ key, state: "error", teach: error instanceof Error && error.message.startsWith("TEACH: ") ? error.message : undefined }); });
    return () => controller.abort();
  }, [companyID, projectID, key, attempt]);
  const current = read.key === key ? read : { key, state: "loading" as const };
  if (current.state === "loading") return <section className="design-system-viewer" aria-label="Design System"><p role="status">Carregando Design System selecionado…</p></section>;
  if (current.state === "error" || !current.value) return <section className="design-system-viewer" aria-label="Design System"><p role="alert">{current.teach || "TEACH: O Design System selecionado está indisponível. Recarregue a seleção atual da Registry."}</p><button onClick={() => setAttempt((value) => value + 1)}>Tentar novamente</button></section>;
  const { evaluation } = current.value;
  const definition = evaluation.items[0];
  const revision = evaluation.revision!;
  return <section className="design-system-viewer" aria-label="Design System">
    <div className="design-system-source"><div><h2>{definition.name}</h2><p>Fonte {evaluation.selected_level || definition.owner.level} · revisão <code>{revision}</code></p><p>Inventário <code>{evaluation.inventory_digest}</code></p></div><span className="project-page-tag">ESTRUTURALMENTE VÁLIDO</span></div>
    <section aria-label="Tokens"><h3>Tokens</h3><ul className="design-system-token-list">{definition.tokens.map((token) => <li key={token.id}>{token.type === "color" && typeof token.value === "string" && /^#[0-9a-fA-F]{6}$/.test(token.value) && <span aria-hidden="true" className="design-system-token-swatch" style={{ backgroundColor: token.value }} />}<strong>{token.id}</strong><code>{token.ref ? `↗ ${token.ref}` : displayValue(token.value)}</code></li>)}</ul></section>
    <section aria-label="Components"><h3>Components</h3>{definition.components.length ? <ul>{definition.components.map((component) => <li key={component.id}><strong>{component.name}</strong> <code>{component.id}</code>{component.states.length > 0 && <span> · {component.states.length} states</span>}</li>)}</ul> : <p>No components are registered.</p>}</section>
    <section aria-label="Assets"><h3>Assets</h3>{definition.assets.length ? <ul>{definition.assets.map((asset) => <li key={asset}><a href={assetURL(companyID, projectID, revision, asset)} target="_blank" rel="noreferrer">{asset} ↗</a></li>)}</ul> : <p>No assets are registered.</p>}</section>
  </section>;
}
