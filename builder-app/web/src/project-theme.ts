import type { CSSProperties } from "react";

type Token = { id: string; type: string; value?: unknown; ref?: string };
type ThemeContext = { id: string; roles: Record<string, string> };
type Definition = { schema_version: string; tokens: Token[]; presentation?: { default_context: string; contexts: ThemeContext[] } };

const requiredRoles = {
  canvas: ["color", "canvas"], surface: ["color", "surface"], text: ["color", "text"],
  muted_text: ["color", "muted"], border: ["color", "border"], accent: ["color", "accent"],
  accent_text: ["color", "accentText"], on_accent: ["color", "onAccent"], focus: ["color", "focus"],
  success_text: ["color", "success"], font_family: ["string", "font"],
  body_size: ["number", "bodySize"], display_size: ["number", "displaySize"],
  card_radius: ["number", "cardRadius"], control_radius: ["number", "controlRadius"],
} as const;

export type ProjectTheme = CSSProperties & Record<`--project-${string}`, string>;

export function resolveProjectTheme(definition: unknown, contextID?: string): { style?: ProjectTheme; diagnostic?: string } {
  if (!definition || typeof definition !== "object") return { diagnostic: "The selected Design System definition is unavailable." };
  const source = definition as Definition;
  if (source.schema_version !== "2" || !source.presentation || !Array.isArray(source.presentation.contexts)) {
    return { diagnostic: "TEACH: The selected Design System is structurally available but its presentation is not ready. Publish schema_version 2 with presentation.default_context and contexts[].roles." };
  }
  const context = source.presentation.contexts.find((item) => item.id === (contextID ?? source.presentation!.default_context));
  if (!context || !context.roles || !Array.isArray(source.tokens)) return { diagnostic: "The selected Design System has no valid theme context or tokens." };
  const tokens = new Map<string, Token>();
  for (const token of source.tokens) {
    if (!token || typeof token.id !== "string" || tokens.has(token.id)) return { diagnostic: "The selected Design System contains an invalid or duplicate token." };
    tokens.set(token.id, token);
  }
  function value(id: string, type: string, seen = new Set<string>()): string | undefined {
    const token = tokens.get(id);
    if (!token || token.type !== type || seen.has(id)) return undefined;
    if (token.ref) { seen.add(id); return value(token.ref, type, seen); }
    if (type === "color") return typeof token.value === "string" && /^#[0-9a-fA-F]{6}$/.test(token.value) ? token.value : undefined;
    if (type === "string") return typeof token.value === "string" ? token.value : undefined;
    return typeof token.value === "number" && Number.isFinite(token.value) ? String(token.value) : undefined;
  }
  const style = {} as ProjectTheme;
  for (const [role, [kind, cssRole]] of Object.entries(requiredRoles)) {
    const tokenID = context.roles[role];
    const resolved = tokenID && value(tokenID, kind);
    if (resolved === undefined) return { diagnostic: `TEACH: presentation.contexts.${context.id}.roles.${role} must reference a valid ${kind} token.` };
    style[`--project-${cssRole}`] = kind === "number" ? `${resolved}px` : resolved;
  }
  const lineHeightID = context.roles.line_height;
  if (lineHeightID) {
    const lineHeight = value(lineHeightID, "number");
    if (lineHeight === undefined) return { diagnostic: `TEACH: presentation.contexts.${context.id}.roles.line_height must reference a valid number token.` };
    style["--project-lineHeight"] = lineHeight;
  }
  return { style };
}
