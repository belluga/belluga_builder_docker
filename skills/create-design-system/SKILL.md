---
name: create-design-system
description: Create or update a Git-backed Belluga Design System with explicit ownership, registered files, and deterministic local structural validation.
---

# Create a Design System

Use this skill when a Builder Project needs a coherent, versioned set of design tokens, components, states, variants, examples, and assets. Work in the source repository explicitly selected for the Project; do not infer ownership from a folder name or merge token keys across levels.

## Establish the effective source

The canonical field, limit, diagnostic, and digest contract is [the Design Artifact Authoring module](../../belluga_builder_foundation_documentation/modules/design_artifact_authoring_module.md#6-design-system-provider-extension-skill-ds-01); consult it rather than guessing schema details. The first-version Builder adapter reads the explicitly supplied local map at `local-api/source-bindings.json`. Its redacted shape is in `local-api/source-bindings.example.json`. Project, Company, then default is whole-source precedence. A selected but broken source is a no-go; never fall back to a less-specific source. Only the selected source content is loaded. The Project slot must resolve to the exact `belluga_builder_foundation_documentation/` checkout. This local map is a trusted operator assertion, not IAM authority; never print it or include credentials in a response.

Before writing, confirm explicit intent and authorization to modify the selected owner level. Effective Company/default fallback is read/conformance evidence, not permission for a Project-scoped request to mutate its parent. Do not invent a Project binding or definition to bypass that boundary; use an authorized source or return a draft/gap for the owner.

Run the real provider from the Builder repository root:

```sh
local-api/artifact-status design-system --mode working-tree
local-api/artifact-status design-system --mode committed
```

Working-tree mode requires null source revisions and is a mutable local observation. Committed mode requires an explicit full SHA in every non-null source binding; it never infers HEAD. The shared runner requires Docker, not a host Go installation. It prints fresh JSON and updates a fixed disposable snapshot. Use that invocation's stdout only: snapshots are last-writer-wins and are not fresh evidence. Exit 0 is structural `go`, exit 2 is structured `no_go`, 64 is usage error, and 70 is infrastructure error.

If the map/source is missing, do not create a real system or invent bindings. Return a clearly labeled draft proposal only, identify the missing source, and make no conformance claim. Keep source root paths, map contents, and credentials private.

## Author the selected system

Start with purpose, audience, owning level, and existing material. Preserve stable IDs when updating; use unique IDs for tokens and components, and unique state/variant IDs within each component. Use explicit supported token values only: opaque `#RRGGBB` colors, finite numbers, or strings of 1–2000 characters. A token may instead reference another token in this same definition; references must resolve iteratively, without cycles or type changes. Do not use expressions, CSS evaluation, or values inherited from another source.

Register each navigable component and its token IDs, states, variants, documentation, examples, and assets in the exact definition. File references are relative to the Design System root (the parent directory of `definition_path`); they are not Foundation-root paths. Include every managed regular file and no extras or orphan directories. Never execute examples or assets.

Declare only contrast pairs the system intends to check. The provider evaluates opaque sRGB WCAG relative luminance and compares the unrounded ratio to each declared minimum. Empty pairs mean zero checks, not an accessibility pass. A structural `go` does not approve aesthetics, usability, completeness, or accessibility beyond the declared pairs checked.

After authoring, run the provider against the exact source and mode intended for handoff. Resolve every diagnostic; do not weaken the schema or accept partial items/digests from a no-go. Report source owner and revision, selected system ID, validation result, registered paths, checked contrast-pair count, visual decisions, and remaining questions. Distinguish mutable working-tree evidence from a committed revision.

## Consumer references

Before a conformance/reference claim, check the fresh response's schema version, `project_id`, `company_id`, `target: design-system`, requested `mode`, selected `source` identity/owner, and exact revision; require `outcome: go`, `design_system_validation: valid`, and exactly one item. Consumers match that item's ID and full `inventory_digest`. In committed mode, a Project Design System reference must use the same intended downstream Foundation SHA, while Company/default revisions are independently pinned. A Prototype's `design_system_ref.content_digest` binds the full managed inventory, including supporting files; it is not `definition_digest`. A changed support file makes a previous reference stale. This skill does not edit or automatically approve downstream Prototypes or Landing artifacts; their owners must consume the provider response through their own approved workflows.

## Available tools

- `local-api/artifact-status design-system` — bounded, local structural validation for the explicitly bound source.
- `local-api/artifact-status prototypes` — separate Prototype structural operation; it reports `design_system_validation: not_evaluated` and does not establish conformance.
- Skill Creator validation — run `python3 /home/elton/.codex/skills/.system/skill-creator/scripts/quick_validate.py skills/create-design-system` when changing this skill.

Do not claim that this operation renders, publishes, authenticates, provisions a Company source, or changes the preview/API surface.
