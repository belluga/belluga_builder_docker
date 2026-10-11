import { describe, expect, it } from "vitest";
import { resolveProjectTheme } from "./project-theme";

const builder = [
  ["color-canvas", "#061421"], ["color-surface", "#091D2B"], ["color-text-strong", "#EAF5FB"], ["color-text-secondary", "#A8C1D1"],
  ["color-accent", "#55DFF2"], ["color-on-accent", "#06202B"], ["color-border", "#89BFD8"], ["color-focus", "#CCEAF1"],
];
const boora = [
  ["surface", "#121116"], ["surface-container", "#211F27"], ["on-surface", "#F2EDF7"], ["on-surface-variant", "#CCC4D5"],
  ["primary", "#C7B8FF"], ["on-primary", "#2D1E53"], ["outline", "#9D93A9"],
];
function definition(colors: string[][], cardRadius: number, controlRadius: number, other: { font: string; body: string; display: string; field: string }) {
  return { tokens: [
    ...colors.map(([id, value]) => ({ id, type: "color", value })),
    { id: other.font, type: "string", value: 'system-ui, -apple-system, "Segoe UI", sans-serif' },
    { id: other.body, type: "number", value: 16 }, { id: other.display, type: "number", value: 40 },
    { id: "line-body", type: "number", value: 1.5 }, { id: "radius-card", type: "number", value: cardRadius },
    { id: other.field, type: "number", value: controlRadius },
  ] };
}
describe("Project Design System role adapter", () => {
  it("resolves Builder and Bóora vocabularies without Project identity branches", () => {
    const b = resolveProjectTheme(definition(builder, 7, 5, { font: "font-family-sans", body: "text-body", display: "text-display", field: "radius-small" }));
    const o = resolveProjectTheme(definition(boora, 24, 16, { font: "font-family", body: "font-body", display: "font-display", field: "radius-field" }));
    expect(b.style?.["--project-cardRadius"]).toBe("7px");
    expect(o.style?.["--project-cardRadius"]).toBe("24px");
    expect(b.style?.["--project-accent"]).toBe("#55DFF2");
    expect(o.style?.["--project-accent"]).toBe("#C7B8FF");
  });
  it("diagnoses an unknown or wrongly typed required role", () => {
    const source = definition(builder, 7, 5, { font: "font-family-sans", body: "text-body", display: "text-display", field: "radius-small" });
    source.tokens = source.tokens.filter((item) => item.id !== "color-canvas");
    expect(resolveProjectTheme(source).diagnostic).toMatch(/canvas token/);
    const wrongType = definition(builder, 7, 5, { font: "font-family-sans", body: "text-body", display: "text-display", field: "radius-small" });
    wrongType.tokens.find((item) => item.id === "color-canvas")!.type = "string";
    expect(resolveProjectTheme(wrongType).diagnostic).toMatch(/canvas token/);
  });
});
