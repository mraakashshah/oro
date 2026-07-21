# pkg/memory Retirement — Readiness-Signal Decision

**Date:** 2026-07-21
**Epic:** oro-9t95 · Decision task: oro-0dfs
**Decision owner:** aakash (approved retirement 2026-07-21)

## Context

`oro cards memory-retirement-check` reports `BLOCKED: no memory read telemetry
found` and cannot clear, because the `memory_read_events` (schema table `n`)
emitter was never wired: production has only the schema migration
(`cmd/oro/db.go`) and the read-side `COUNT(*)` gate
(`cmd/oro/cmd_cards_retirement.go`) — there is no `INSERT` anywhere. The gate
fails closed on an empty telemetry table, so the 14-day-zero-reads window can
never be satisfied.

## Discovery (verified 2026-07-21)

The code-level retirement has **already happened**. Verified via `go list`
(authoritative, immune to grep issues):

- `pkg/memory` — directory removed; `go list ./pkg/memory` → *directory not found*.
- Importers of `oro/pkg/memory` — **zero**, production and test.
- `pkg/cards/legacy_writer.go` (dual-write shim) — removed.
- `ad_hoc/memory_eval` harness — removed.
- Remaining string mentions of `pkg/memory` are comments/doc references in
  `pkg/cards/cards.go`, `pkg/beadstore/sqlite.go`, `cmd/oro/cmd_cards.go`, and
  the retirement command's own logic — not imports.
- The 3 `check-drift` entries (memory 3611–3613) are the notes *recording* that
  removal; their source (`pkg/memory`) no longer exists to mirror from.

## Decision

Retirement is complete in code. **Do not** wire read telemetry (option b) — it
would instrument a package that no longer exists — and **do not** re-gate on a
readiness check. Close out by **decommissioning the vestigial ceremony**:

1. Remove the `oro cards memory-retirement-check` readiness gate and the
   `memory_read_events` / table `n` migration; they guard nothing.
2. Replace the readiness gate with a lightweight **stays-retired guard test**
   (per note 3613): a command/test that fails if `oro/pkg/memory` is ever
   re-imported in production, so retirement cannot silently regress.
3. Reconcile the 3 orphan `check-drift` entries (import as cards or mark
   resolved), then retire or narrow `check-drift` since the dual-write source
   is gone.
4. Strip the residual `pkg/memory` comment/string references.

Rationale: the guarantee we still want is "pkg/memory does not come back,"
which is a static import invariant, not a runtime read-count. A guard test
expresses that directly; the telemetry gate never can.

## Implications

- Epic oro-9t95 is reframed from "remove the package" to "decommission dead
  retirement scaffolding + reconcile orphan drift + add a stays-retired guard."
- `oro-kf51` (remove package + harness) is already satisfied and is closed.
- No dual-write window, no 14-day telemetry wait — close-out is mechanical.
