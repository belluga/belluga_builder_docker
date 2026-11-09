import type { ProjectTheme } from "./project-theme";

type DesignToken = { id: string; type: string; value?: unknown; ref?: string };
type DesignComponent = { id: string; name: string; documentation?: string; token_ids: string[]; states: string[]; variants: { id: string; name: string; token_ids: string[] }[]; examples: string[] };
type Definition = { id: string; name: string; owner: { level: string; id: string }; tokens: DesignToken[]; components: DesignComponent[]; assets: string[]; schema_version: string };
export type DesignSystemReadResponse = {
  selection: { state: string; sha?: string; inventory_digest?: string; binding_revision: number };
  evaluation: { project_id: string; company_id: string; mode: string; revision: string | null; outcome: string; inventory_digest: string | null; items: Definition[]; design_system_validation: string; presentation_validation?: string; presentation_teach?: { code: string; field_path: string; message: string; resolution: string }; selected_level?: string };
};
export function DesignSystemViewer({ companyID, projectID, read, theme, diagnostic, onRetry }: { companyID: string; projectID: string; read?: DesignSystemReadResponse; theme?: ProjectTheme; diagnostic?: string; onRetry: () => void }) {
  if (!read) return <section className="design-system-viewer" aria-label="Design System">{diagnostic ? <><p role="alert">{diagnostic}</p><button onClick={onRetry}>Tentar novamente</button></> : <p role="status">Carregando Design System selecionado…</p>}</section>;
  const { evaluation } = read;
  const definition = evaluation.items[0];
  const revision = evaluation.revision!;
  const example = definition.components.flatMap((component) => component.examples).find((path) => path.endsWith(".html"));
  const presentationURL = example ? `/api/design-system/presentation/${encodeURIComponent(companyID)}/${encodeURIComponent(projectID)}/${revision}/${example.split("/").map(encodeURIComponent).join("/")}` : undefined;
  if (!theme) return <section className="design-system-viewer" aria-label="Design System"><p role="alert">{diagnostic || "TEACH: A apresentação do Design System selecionado não está pronta."}</p>{presentationURL && <a href={presentationURL} target="_blank" rel="noreferrer">Abrir Design System ↗</a>}<button onClick={onRetry}>Tentar novamente</button></section>;
  const value = (role: string) => theme[`--project-${role}`];
  const palette = ["canvas", "surface", "border", "accent", "focus"] as const;
  return <section className="design-system-view design-system-viewer" aria-label="Design System" style={theme}>
    <section className="design-system-palette"><small>PALETA ESTRUTURAL</small><h2>Paleta estrutural</h2><p>Tokens visuais do Design System selecionado de {definition.name}.</p><div className="design-system-swatches">{palette.map((role) => <div className="design-system-swatch" key={role}><span className="design-system-swatch-chip" style={{ backgroundColor: value(role) }} aria-hidden="true"/><strong>{role[0].toUpperCase() + role.slice(1)}</strong><code>{value(role)}</code></div>)}</div></section>
    <div className="design-system-cards">
      <article><h2>Hierarquia</h2><p className="design-system-type-sample">Clareza para decidir.</p><p>Títulos e conteúdo seguem os tokens deste projeto: {value("displaySize")} / {value("bodySize")}.</p></article>
      <article><h2>Componentes</h2><div className="design-system-controls"><button disabled>Primário</button><button disabled>Secundário</button><span className="project-page-tag">{definition.components.length} COMPONENTES</span></div></article>
      <article><h2>Princípio</h2><p>Cor de destaque orienta ação e foco; superfícies organizam a informação sem competir com ela.</p><p>Raio dos cartões: {value("cardRadius")}.</p></article>
    </div>
    <section className="design-system-source"><div><small>FONTE AUTORADA</small><h2>{definition.name}</h2><p>Revisão selecionada <code>{revision.slice(0, 12)}</code> · validação estrutural concluída. Esta tela apresenta o catálogo, mas não o edita.</p></div>{presentationURL && <a href={presentationURL} target="_blank" rel="noreferrer">Abrir Design System ↗</a>}</section>
  </section>;
}
