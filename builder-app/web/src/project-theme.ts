import type { CSSProperties } from "react";

type Token = { id: string; type: string; value?: unknown; ref?: string };
type Definition = { tokens?: Token[] };

const roles = {
  canvas: ["color", "color-canvas", "surface"],
  surface: ["color", "color-surface", "surface-container"],
  text: ["color", "color-text-strong", "on-surface"],
  muted: ["color", "color-text-secondary", "on-surface-variant"],
  accent: ["color", "color-accent", "primary"],
  onAccent: ["color", "color-on-accent", "on-primary"],
  border: ["color", "color-border", "outline"],
  focus: ["color", "color-focus", "primary"],
  font: ["string", "font-family-sans", "font-family"],
  bodySize: ["number", "text-body", "font-body"],
  displaySize: ["number", "text-display", "font-display"],
  lineHeight: ["number", "line-body"],
  cardRadius: ["number", "radius-card"],
  controlRadius: ["number", "radius-small", "radius-field"],
} as const;

export type ProjectTheme = CSSProperties & Record<`--project-${string}`, string>;

export function resolveProjectTheme(definition: unknown): { style?: ProjectTheme; diagnostic?: string } {
  if (!definition || typeof definition !== "object" || !Array.isArray((definition as Definition).tokens)) {
    return { diagnostic: "The admitted Design System definition is unavailable or malformed." };
  }
  const tokens = new Map<string, Token>();
  for (const token of (definition as Definition).tokens ?? []) {
    if (!token || typeof token.id !== "string" || tokens.has(token.id)) {
      return { diagnostic: "The admitted Design System contains an invalid or duplicate token." };
    }
    tokens.set(token.id, token);
  }
  function value(id: string, type: string, seen = new Set<string>()): string | undefined {
    const token = tokens.get(id);
    if (!token || token.type !== type || seen.has(id)) return undefined;
    if (token.ref) {
      seen.add(id);
      return value(token.ref, type, seen);
    }
    if (type === "color") return typeof token.value === "string" && /^#[0-9a-fA-F]{6}$/.test(token.value) ? token.value : undefined;
    if (type === "string") return typeof token.value === "string" && token.value.length <= 200 && /^[\w\s,.'"-]+$/.test(token.value) ? token.value : undefined;
    return typeof token.value === "number" && Number.isFinite(token.value) && token.value >= 0 && token.value <= 999 ? String(token.value) : undefined;
  }
  const style = {} as ProjectTheme;
  for (const [role, [type, ...aliases]] of Object.entries(roles)) {
    const resolved = aliases.map((alias) => value(alias, type)).find((candidate) => candidate !== undefined);
    if (resolved === undefined) return { diagnostic: `The admitted Design System cannot resolve required ${role} token (${aliases.join(" or ")}).` };
    const unit = type === "number" && role !== "lineHeight" ? "px" : "";
    style[`--project-${role}`] = `${resolved}${unit}`;
  }
  const success = value("color-status-positive", "color") ?? value("color-success", "color");
  if (success) style["--project-success"] = success;
  return { style };
}
