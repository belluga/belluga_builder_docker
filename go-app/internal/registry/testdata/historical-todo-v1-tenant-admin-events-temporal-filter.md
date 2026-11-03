# TODO (V1): Tenant Admin Events Temporal Filter

**Status legend:** `- [ ] ⚪ Pending` · `- [ ] 🟡 Provisional` · `- [x] ✅ Production-Ready`.
**Status:** Active
- **Current delivery stage:** `Local-Implemented`
**Owners:** Flutter Team, Laravel Team
**Objective:** Add an independent temporal filter to tenant-admin event management so operators can view past, happening-now, and future events without mixing that concern into editorial status or archived visibility.
**Promotion lane path:** `dev -> stage -> main`
**Complexity:** `medium`
**Next exact step:** Run a final tenant-admin manual smoke across `status + archived + temporal` combinations, then checkpoint/promotion handling.

---

## Module Anchors
- **Primary:** `foundation_documentation/modules/events_module.md`
- **Secondary:** `foundation_documentation/modules/tenant_admin_module.md`
- **Related visual-only lane:** `foundation_documentation/todos/completed/TODO-v1-screen-events-polish.md`

## Scope
- Introduce a new tenant-admin events filter dimension for temporal buckets:
  - `past`
  - `now`
  - `future`
- Make temporal buckets multi-select with OR semantics.
- Keep temporal filtering independent from:
  - editorial status (`draft/published/scheduled/ended/todos`)
  - visibility (`ativos/arquivados`)
- Default the screen to `now + future`, excluding past events until the operator explicitly opts in.
- Back the filter with canonical backend query support and deterministic tests.

## Out of Scope
- Public events screen filtering changes.
- Event card visual redesign beyond whatever small filter UI additions are required.
- Replacing editorial `status` or archived visibility filters.

## Decision Baseline (Frozen)
- `D-01`: Temporal filtering is an additional query dimension, not a reinterpretation of editorial status or archived visibility.
- `D-02`: Temporal bucket semantics are based on effective event timing:
  - `past` when the effective end is before `now`
  - `now` when the event has started and the effective end has not passed
  - `future` when the event has not started yet
- `D-03`: Temporal filter selection is multi-select with inclusive OR semantics across the chosen buckets.
- `D-04`: The default tenant-admin view opens with `now + future` selected and `past` unselected so completed history does not pollute the main management list.
- `D-05`: The temporal filter must compose deterministically with existing `status` and `archived` filters instead of overriding them implicitly.

## Ordered Steps
1. Freeze the backend query contract for temporal buckets and document the accepted request shape.
2. Add fail-first Laravel tests covering temporal bucket semantics and filter composition with `status` plus `archived`.
3. Add fail-first Flutter repository/controller tests for default selection, multi-select persistence, and contract encoding.
4. Add the temporal multi-select control to tenant-admin events with the default `now + future` state.
5. Validate the combined filter matrix with focused backend and Flutter tests plus manual smoke on the tenant-admin events screen.

## Acceptance Criteria
- [ ] 🟧 Tenant-admin events exposes a distinct temporal filter with `past`, `now`, and `future`.
- [ ] 🟧 Operators can select more than one temporal bucket at the same time.
- [ ] 🟧 The default screen state uses `now + future` and hides past events until explicitly requested.
- [ ] 🟧 Temporal filtering combines correctly with `status` and `archived` without contradictory results.
- [ ] 🟧 Backend and Flutter tests prove the temporal contract and default behavior.

## Validation Steps
- [ ] 🟧 Laravel tests for temporal bucket calculation and filter composition.
- [ ] 🟧 Flutter tests for request encoding, controller default state, and multi-select changes.
- [ ] ⚪ Manual smoke: default load shows `now + future`.
- [ ] ⚪ Manual smoke: selecting `past` includes completed history without breaking `status`/`archived` filters.
