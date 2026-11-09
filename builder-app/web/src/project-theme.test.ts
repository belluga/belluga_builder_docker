import { describe, expect, it } from "vitest";
import { resolveProjectTheme } from "./project-theme";

function definition(prefix: string, accent: string, radius: number) {
  const colors = { canvas: "#101319", surface: "#1A1D23", text: "#FFFFFF", muted_text: "#D0D0D0", border: "#AAAAAA", accent, accent_text: "#FFFFFF", on_accent: "#101319", focus: "#FFFFFF", success_text: "#D0D0D0" };
  const numbers = { body_size: 16, display_size: 36, card_radius: radius, control_radius: 8, line_height: 1.5 };
  const roles = Object.fromEntries([...Object.keys(colors), ...Object.keys(numbers), "font_family"].map((name) => [name, `${prefix}-${name}`]));
  return {
    schema_version: "2",
    presentation: { default_context: "main", contexts: [{ id: "main", roles }] },
    tokens: [
      ...Object.entries(colors).map(([name, value]) => ({ id: `${prefix}-${name}`, type: "color", value })),
      ...Object.entries(numbers).map(([name, value]) => ({ id: `${prefix}-${name}`, type: "number", value })),
      { id: `${prefix}-font_family`, type: "string", value: "Inter, Arial" },
    ],
  };
}

describe("Project Design System semantic presentation", () => {
  it("uses declared roles for unrelated token vocabularies", () => {
    const first = resolveProjectTheme(definition("builder", "#66DDF2", 7));
    const second = resolveProjectTheme(definition("other", "#C7B8FF", 24));
    expect(first.style?.["--project-cardRadius"]).toBe("7px");
    expect(second.style?.["--project-cardRadius"]).toBe("24px");
    expect(first.style?.["--project-accent"]).toBe("#66DDF2");
    expect(first.style?.["--project-accentText"]).toBe("#FFFFFF");
    expect(second.style?.["--project-accent"]).toBe("#C7B8FF");
    expect(first.style?.["--project-lineHeight"]).toBe("1.5");
  });
  it("does not guess a v1 theme or an undeclared role", () => {
    const legacy = definition("old", "#66DDF2", 7);
    legacy.schema_version = "1";
    expect(resolveProjectTheme(legacy).diagnostic).toMatch(/presentation is not ready/);
    const invalid = definition("broken", "#66DDF2", 7);
    delete invalid.presentation.contexts[0].roles.canvas;
    expect(resolveProjectTheme(invalid).diagnostic).toMatch(/roles.canvas/);
  });
});
