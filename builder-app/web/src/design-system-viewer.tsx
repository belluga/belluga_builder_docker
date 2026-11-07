import { useEffect, useState } from "react";
import { resolveProjectTheme } from "./project-theme";

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
  const theme = resolveProjectTheme(definition).style;
  const value = (role: string) => theme?.[`--project-${role}`] || "—";
  const palette = ["canvas", "surface", "border", "accent", "focus"] as const;
  const example = definition.components.flatMap((component) => component.examples).find((path) => path.endsWith(".html"));
  const presentationURL = example ? `/api/design-system/presentation/${encodeURIComponent(companyID)}/${encodeURIComponent(projectID)}/${revision}/${example.split("/").map(encodeURIComponent).join("/")}` : undefined;
  return <section className="design-system-view design-system-viewer" aria-label="Design System" style={theme}>
    <section className="design-system-palette"><small>PALETA ESTRUTURAL</small><h2>Paleta estrutural</h2><p>Tokens visuais do Design System selecionado de {definition.name}.</p><div className="design-system-swatches">{palette.map((role) => <div className="design-system-swatch" key={role} style={{ backgroundColor: value(role), color: role === "accent" || role === "focus" || role === "border" ? value("canvas") : value("text") }}><strong>{role[0].toUpperCase() + role.slice(1)}</strong><code>{value(role)}</code></div>)}</div></section>
    <div className="design-system-cards">
      <article><h2>Hierarquia</h2><p className="design-system-type-sample">Clareza para decidir.</p><p>Títulos e conteúdo seguem os tokens deste projeto: {value("displaySize")} / {value("bodySize")}.</p></article>
      <article><h2>Componentes</h2><div className="design-system-controls"><button disabled>Primário</button><button disabled>Secundário</button><span className="project-page-tag">{definition.components.length} COMPONENTES</span></div></article>
      <article><h2>Princípio</h2><p>Cor de destaque orienta ação e foco; superfícies organizam a informação sem competir com ela.</p><p>Raio dos cartões: {value("cardRadius")}.</p></article>
    </div>
    <section className="design-system-source"><div><small>FONTE AUTORADA</small><h2>{definition.name}</h2><p>Revisão selecionada <code>{revision.slice(0, 12)}</code> · validação estrutural concluída. Esta tela apresenta o catálogo, mas não o edita.</p></div>{presentationURL && <a href={presentationURL} target="_blank" rel="noreferrer">Abrir Design System ↗</a>}</section>
  </section>;
}
