---
name: create-executive-landing
description: Create or review an executive Landing with evidence-linked decisions, clear visual direction, and truthful project status.
---

# Create an Executive Landing

Create a concise, relevance-led Landing for executives and other non-specialist readers. Explain why the work matters, the confirmed decisions behind it, its actual state, and the next meaningful direction. Keep facts, plans, and open questions distinct; link material claims to their canonical owner. A Landing is a navigational summary, not a second source of truth.

## Establish the owner before using evidence

Classify the request as a Builder Project Landing or a Company portfolio/proposal before selecting sources, tools, a design system, or a visual concept. Keep the branches separate throughout research, composition, status reporting, and output. If a request mixes owners, produce separately scoped outputs rather than blending their evidence.

- **Builder Project Landing:** the local `knowledge-status` and `artifact-status design-system` operations are bound to Project `builder` and its dedicated Foundation checkout. Use their results only for this Project.
- **Company portfolio or proposal:** use only Company evidence explicitly supplied for that request. Keep the result a manual proposal/draft; identify unavailable Company authority, status, or design capabilities as gaps. Do not invoke Project knowledge-status, review-packet, record-review, or artifact-status to support a Company claim. Do not reuse Project documents, status, revision, effective Design System, or selected concept C as Company evidence. A Design System response with `selected_level: "company"` is still emitted by the Project-bound operation and is not a Company-provider attestation.

Never fill missing Company evidence with Project or default output. An all-null or absent Project source map is a Project diagnostic, not permission to invent a Design System or to route a Company request through the Project adapter.

## Project evidence and review state

Read the current canonical Project sources from the dedicated Foundation checkout: mandate, domain entities, constitution, roadmap, and relevant module and policy documents. Do not infer facts, dates, metrics, approvals, or delivery from a saved snapshot, an image, or a structurally valid artifact.

The local review and preview boundary is defined in `belluga_builder_foundation_documentation/modules/project_knowledge_viewer_module.md`. The shared Prototype and Design System contract is defined in `belluga_builder_foundation_documentation/modules/design_artifact_authoring_module.md`; the Project's Landing claims and revision posture are in `belluga_builder_foundation_documentation/project_landing.md` and `project_landing.manifest.json`.

When current review state is needed, run from the Builder repository root:

```sh
local-api/knowledge-status status
```

Use only that invocation's fresh stdout. It reports Project-local review status (`current`, `review_required`, or `unverifiable`) for Landing and Roadmap; the saved `local-api/v1/projects/builder/knowledge-status.json` is a disposable last-writer-wins snapshot, not fresh evidence. This status is not preview integrity, publication, authentication, or remote freshness attestation. After authorized source or Landing changes, run status again. Changed source evidence requests review; it does not automatically rewrite accurate prose.

If Landing is `review_required` or has no review record, prepare the exact committed packet for human inspection. Also obtain the committed Landing packet whenever you need to pin a Project Foundation SHA for Design System evidence; fresh status alone does not select that revision:

```sh
local-api/knowledge-status review-packet --target landing
```

Inspect the packet's `source_revision`, source and output digests, and `packet_digest` against the named committed Landing and sources. Keep the Landing aligned to that Git revision. If accurate, say so; do not change content merely to clear review status. `record-review` is a separate human action: only after explicit approval of that exact packet, use its exact digest and the approved reviewer/reference values. Never infer approval from a reference string, a chosen image, or a concept decision; never record review automatically. Do not run `record-review` during a skill walkthrough or validation.

The real command is:

```sh
local-api/knowledge-status record-review --target landing --expected-packet-digest 'sha256:<approved-digest>' --reviewed-by 'reviewer-label' --review-reference 'approval-message-reference'
```

## Project Design System and Prototype references

For every Project Landing, resolve its effective Design System with the actual local operation and consume only its fresh stdout:

```sh
local-api/artifact-status design-system --mode committed
```

Check the full response identity before using it: `schema_version`, `project_id`, `company_id`, `authority_scope: "local_structure_only"`, `target`, `mode`, `revision`, `outcome`, `selected_level`, exactly one item, `design_system_validation`, the item's stable ID, `inventory_digest`, and the redacted `source` repository identity, owner, definition path, and revision. Require `outcome: "go"`, `design_system_validation: "valid"`, and matching item/source owner data for the selected slot. In committed mode, the selected Project source must be the exact intended Foundation SHA used by the Landing packet; do not accept an older but structurally valid Project definition as current. Company/default fallback sources retain separate explicit full-SHA pins and must not be equated with the Project Landing SHA. The selection is whole-source Project → Company → default, not a merge; a broken selected source does not authorize fallback. The CLI's default map path is `local-api/source-bindings.json`; an override is for an explicitly authorized local binding, not a reason to broaden its authority.

Working-tree mode is an explicitly mutable exploration only. Do not use it as committed evidence. An absent map or `source_binding_missing` is a gap: do not manufacture a map, choose an unbound source, or claim a fallback.

When referring to a Prototype's declared Design System, compare its manifest `design_system_ref.id` with the provider item ID and `content_digest` with the provider's complete `inventory_digest`, at the required revision. A match establishes version binding only. Prototype output's `design_system_validation: "not_evaluated"` means it does not prove CSS/component use, visual consistency, accessibility conformance, QA, preview, publication, or review approval. A stale digest, wrong ID, null/missing reference, no-go provider, wrong source revision, or absent provider means no conformance claim; state the gap and leave any corrective artifact as an explicitly proposed next action.

These operations are local transitional Project adapters, not Company services or authorization. Replace them only after a separately authorized, Project-bound API/MCP exists and this consumer is migrated to its equivalent versioned contract.

## Visual exploration and composition

For a substantive new or redesigned Landing, explore materially different compositions as images with the available external image-generation capability before implementation. Compare hierarchy, section flow, emphasis, and executive scanability; record the selected direction and meaningful rejected alternatives. Reuse selected concept C only when it belongs to this Project and still fits the audience and current evidence. Company proposals require a separately supplied/selected Company direction; Project concept C never defines Company branding. Minor copy or source corrections do not require fresh image exploration.

Exploration images communicate composition, not documentary evidence or finished production assets. Check any text or figures shown in them against canonical sources. A visual choice does not approve a review packet. If image generation is unavailable, return a clearly labeled textual composition draft and capability gap; do not claim image exploration occurred.

Compose an actual executive experience, not a dump of Foundation documents: lead with relevance and outcome, then explain the decisions, current status, meaningful progress, next direction, and unresolved questions. Keep implementation detail subordinate to executive consequence. Preserve caveats, readable hierarchy, responsive reading order, keyboard/focus semantics, and text alternatives in downstream requirements. Cite each material claim to the correct owner's source.

## Output and stopping rules

For a **Project Landing**, summarize the Project owner and Foundation revision, canonical sources, fresh local review state, effective Design System source/revision and full digest when available, chosen composition evidence, proposed canonical path, and remaining gaps. Distinguish local review, preview integrity, and remote attestation. Do not publish, broaden the anonymous catalog, or present unavailable capabilities as delivered.

For a **Company proposal**, include only explicitly supplied Company sources and manual design references, the separately supplied/selected Company direction, and capability gaps. Do not include Project-derived status, revision, Design System, concept C, or freshness claims. Do not claim an automated Company save, review, or publication.

When a source, binding, image capability, status, or authority is missing, conflicting, or stale, stop the affected claim and present a draft/gap plus the next evidence needed. Never turn uncertainty into a green status or mutate review evidence to make the presentation appear complete.
