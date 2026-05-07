# Premortem Excision Design

Date: 2026-05-07
Status: draft (Stage 1 — Brainstorm complete; Stage 2 Consultation pending)

## Problem

Oro's beadstore carries a "premortem gate" subsystem: `pkg/dispatcher/retroactive_gate.go` auto-spawned a `type=premortem` child bead whenever an epic crossed five children, and `CheckPremortemGate` blocked sibling execution until that child closed. The runtime was disabled today (commits `d63c54ea`, `e2bc4ea5`, `bd52b1f0`; tasks oro-2vma, oro-c4yf, oro-k2k2 closed) but the codebase still carries the carcass: ~35 Go files, three Store API methods, two beads-table columns, two CHECK enum values, multiple worker-prompt branches, three CLI subcommands, asset patterns, and design-doc references.

The signal that the design is wrong is direct: when four epics crossed the threshold the night of 2026-05-06, the operator batch-closed all four auto-spawned premortem beads with `verdict=replan` and reason "Deleted per user request". A gate whose output is uniformly discarded produces friction without value. The premortem-as-a-bead-type concept conflates "task to execute" with "checkpoint to consider"; the latter belongs in the planning skill (`.claude/skills/premortem`), which already exists and is preserved.

The 2026-05-06 task-delete-and-no-premortem-beads design explicitly punted this excision as the next-phase elephant: "The premortem subsystem is now legacy code, but fully deleting it is larger than the requested fix and risks migration churn." This spec redeems that punt.

## Goals

- Remove all dead premortem-as-bead-type code: dispatcher, beadstore Store API, pkg/cards heuristic, pkg/lint closecheck, worker prompt branch, CLI subcommands.
- Drop `gate_state` and `premortem_cycle_count` columns from the beads table.
- Tighten the `type` CHECK constraint to remove `'premortem'` and the `pipeline_stage` CHECK to remove `'premortem'`.
- Scrub `bead_metadata` rows with keys `premortem_verdict`/`premortem_reason` and `bead_journey` rows with `actor='premortem'`.
- Defensive handling of any pre-existing `type='premortem'` rows (none observed in current DB): auto-soft-delete with a migration audit event.
- Land as a single PR with a passing acceptance test.

## Non-goals

- Do not remove the SKILL-level premortem (planning exercise) at `.claude/skills/premortem/`, `assets/skills/premortem/`, or its references in `brainstorming/`, `workflow-routing/`, `oro/` skill docs, README. The bead-type and the skill share a name but are distinct concepts.
- Do not change the soft-delete CLI shipped today (oro-jv6p chain) or its store contract.
- Do not change the dispatcher stability fixes shipped today (HeartbeatTimeout, AssignBead, etc.).
- Do not introduce a new state machine or replacement gating concept; this is pure removal.

## Affected surface (cited)

### Dispatcher
- `pkg/dispatcher/retroactive_gate.go` — `CheckPremortemGate` (`//oro:testonly`), `PremortemGateError`, `blockerHitAlreadyRecorded`. **Whole file removable** (CreateBeadGraph stays — moves to a small helper file).
- `pkg/dispatcher/retroactive_gate_test.go` — whole file removable.
- `pkg/dispatcher/premortem_routing_test.go` — whole file removable.
- `pkg/dispatcher/premortem_verdict_test.go` — whole file removable.
- `pkg/dispatcher/pipeline_state.go` — remove `StagePremortem` from the `PipelineStage` enum + any internal premortem-stage routing.
- `pkg/dispatcher/router.go`, `pkg/dispatcher/router_test.go` — remove premortem routing branches.
- `pkg/dispatcher/sweep.go`, `pkg/dispatcher/sweepers_test.go` — remove gate-state-driven sweep paths.
- `pkg/dispatcher/dispatcher.go:1974` — `CreateBeadGraph` callsite stays (no longer triggers gate).
- `pkg/dispatcher/dispatcher_test.go` — remove gate-related tests.

### Beadstore
- `pkg/beadstore/v3types.go` — remove `GateState` type, six gate constants, `StagePremortem`, `ErrStaleGate`. Update `Actor` field comment to drop `premortem`.
- `pkg/beadstore/v3methods.go` — remove `SetGateState`, `GateState`, `HasClosedPremortemChild`, `SetPremortemVerdict`, `IncrPremortCycleCount`, `ResetPremortCycleCount`.
- `pkg/beadstore/store.go` — remove the corresponding interface methods.
- `pkg/beadstore/sqlite.go` — remove implementations + the deprecated comment at line 45.
- `pkg/beadstore/shadow.go` — remove gate-state shadow paths.
- `pkg/beadstore/testfake.go` — remove fake-store gate-state methods.
- `pkg/beadstore/v3_methods_test.go`, `pkg/beadstore/store_test.go`, `pkg/beadstore/read_tx_parity_test.go` — remove gate-state tests.

### Migrations
- `pkg/beadstore/migrations/migrate_v3.go` — leave intact (additive, already shipped).
- `pkg/beadstore/migrations/migrate_v4.go` — **NEW**. See Migration Design below.
- `pkg/beadstore/migrations/migrate_v3_test.go`, `bead_type_check_test.go` — update expectations to reflect post-v4 schema.

### Schema
- `pkg/protocol/schema.go:185` — remove `'premortem'` from the type CHECK in `beadTableDDL`.
- `pkg/protocol/schema.go:629-706` — `EnsureBeadTypeCheckConstraint` already detects "old DDL with premortem" and triggers rebuild; this function continues to work because new DDL omits 'premortem' and the function detects mismatch by string presence. The detection comment at line 650 stays accurate.

### CLI
- `cmd/oro/cmd_bead.go` — remove `gate-reset`, `gate-state`, `premortem-close` subcommands. Keep the `--type=premortem` rejection (still needed defensively).
- `cmd/oro/cmd_task.go` — remove the corresponding alias registrations.
- `cmd/oro/cmd_bead_test.go`, `cmd_task_test.go`, `cmd_work_test.go`, `cmd_work_execute_test.go` — remove premortem-related test cases. Keep `TestTaskCreateRejectsPremortemType` (still wanted).

### Worker
- `pkg/worker/prompt.go` — remove the premortem-prompt branch in `AssemblePrompt`.
- `pkg/worker/premortem_prompt_test.go` — whole file removable.
- `pkg/worker/prompt_test.go` — remove premortem-branch test cases.

### Cards / lint / integration
- `pkg/cards/store.go:454` — remove `|| beadType == "premortem"` (one-liner).
- `pkg/lint/closecheck/closecheck.go` — remove premortem references.
- `tests/integration/verify_retroactive_gate_test.go` — whole file removable.

### Assets / docs
- `assets/review-patterns.md:119` — remove the `gate-self-block` pattern entry (refers exclusively to premortem self-block).
- `docs/plans/2026-05-06-task-delete-no-premortem-beads-design.md` — leave intact (historical record).
- README.md, CLAUDE.md, `.claude/skills/oro/SKILL.md`, `.claude/skills/brainstorming/SKILL.md`, `.claude/skills/workflow-routing/SKILL.md`, `assets/skills/oro/SKILL.md`, `assets/skills/brainstorming/SKILL.md`, `assets/skills/workflow-routing/SKILL.md`, `.claude/skills/premortem/SKILL.md`, `assets/skills/premortem/SKILL.md` — **PRESERVE**. These reference the planning skill, not the bead type.

## Migration Design (`migrate_v4.go`)

Single atomic table-rebuild migration following the established pattern from `pkg/protocol/schema.go:runBeadsTypeRebuild`. All steps in one transaction.

### Steps (in order, single tx)

1. **Inventory pass.** `SELECT id, type, status, deleted FROM beads WHERE type='premortem'`. Log count + IDs to migration log.

2. **Defensive convert** of any legacy `type='premortem'` rows:
   - For each row, INSERT into `bead_journey` (actor='migration', event='migration_type_converted', payload=`{"original_type":"premortem","reason":"premortem-excision"}`).
   - `UPDATE beads SET deleted=1, type='task', close_reason='premortem-excision: auto-soft-deleted by migrate_v4', updated_at=now() WHERE type='premortem'`.
   - This makes the upcoming CHECK tighten safe (no rows are 'premortem' after this step).
   - In current DB this is a no-op (zero rows).

3. **Scrub historical premortem rows** per Decision 4:
   - `DELETE FROM bead_metadata WHERE key IN ('premortem_verdict','premortem_reason')`.
   - `DELETE FROM bead_journey WHERE actor='premortem'`.
   - The `migration_type_converted` events from step 2 use `actor='migration'` and survive this scrub.

4. **Table rebuild for column drop + CHECK tighten** (legacy_alter_table=ON pattern, foreign_keys=OFF):
   - `DROP VIEW IF EXISTS beads_ready; DROP VIEW IF EXISTS beads_blocked;`
   - Drop schema-rebuild triggers (reuse `dropBeadSchemaRebuildTriggers`).
   - `ALTER TABLE beads RENAME TO beads_v4_rebuild_old`.
   - Create new `beads` table from updated `beadTableDDL`:
     - No `gate_state` column.
     - No `premortem_cycle_count` column.
     - `type` CHECK omits `'premortem'`.
     - `pipeline_stage` CHECK omits `'premortem'`.
     - All other v3 columns and constraints preserved.
   - `INSERT INTO beads (<column-list-without-dropped-columns>) SELECT <same> FROM beads_v4_rebuild_old`.
   - `DROP TABLE beads_v4_rebuild_old`.
   - Recreate `beads_ready` and `beads_blocked` views from `v3ViewsDDL` (verbatim — they don't reference dropped columns).
   - Recreate any FTS / parent-touch triggers.

5. **Commit transaction.** `PRAGMA foreign_key_check` to verify integrity. If violations, abort and roll back.

6. **Idempotency.** Migration detects "already applied" by `PRAGMA table_info(beads)` showing no `gate_state` column. If absent, no-op.

### Why one tx
- Atomicity: any failure rolls back to v3 state; no half-migrated tables.
- The legacy table rebuild and the metadata/journey scrub must commit together so an interrupted migration cannot leave behind orphaned `premortem_verdict` rows pointing to converted beads.

### Why no DROP COLUMN
- SQLite 3.35+ `ALTER TABLE DROP COLUMN` works but does not allow CHECK changes in the same step.
- A single rebuild does both column-drop and CHECK-tighten in one operation; matches the existing `runBeadsTypeRebuild` pattern in the codebase.
- Removes a SQLite-version dependency.

## Acceptance Test

```bash
go test ./cmd/oro ./pkg/beadstore ./pkg/beadstore/migrations ./pkg/dispatcher ./pkg/worker ./pkg/cards ./pkg/lint/closecheck ./pkg/protocol
```

Required assertions:

1. `pkg/beadstore/migrations/migrate_v4_test.go:TestMigrateV4DropsGateColumns` — given a v3-shaped fixture, after migrate_v4 the `beads` table has no `gate_state` and no `premortem_cycle_count` columns.
2. `:TestMigrateV4TightensTypeCheck` — `INSERT INTO beads (..., type, ...) VALUES (..., 'premortem', ...)` returns a CHECK constraint error after migration.
3. `:TestMigrateV4TightensPipelineStageCheck` — same for `pipeline_stage='premortem'`.
4. `:TestMigrateV4ConvertsLegacyPremortemRows` — fixture seeded with 2 rows of `type='premortem'`; after migration both rows have `type='task'`, `deleted=1`, `close_reason` containing 'premortem-excision', and a `bead_journey` event with `actor='migration'` and `event='migration_type_converted'` preserving `original_type='premortem'` in payload.
5. `:TestMigrateV4ScrubsBeadMetadata` — fixture seeded with 4 rows of `key='premortem_verdict'` + 4 rows of `key='premortem_reason'`; after migration, zero rows for both keys.
6. `:TestMigrateV4ScrubsJourneyPremortemActor` — fixture seeded with 3 rows of `actor='premortem'`; after migration, zero rows. The `migration_type_converted` events from step 4 remain.
7. `:TestMigrateV4Idempotent` — running migrate_v4 twice on the same DB produces the same output as running once; second invocation is a no-op.
8. `:TestMigrateV4PreservesNonPremortemData` — fixture has 100 normal beads of varied types/statuses + dependencies + assignments; counts and edges are preserved post-migration.
9. `:TestMigrateV4PreservesViews` — `beads_ready` and `beads_blocked` views return the same rows as a v3 baseline for the non-premortem fixture.
10. `:TestMigrateV4ForeignKeyIntegrity` — `PRAGMA foreign_key_check` returns no rows after migration.
11. Compile gate: `go build ./...` passes (no references to removed Store methods, types, or constants).
12. CLI gate: `oro task gate-reset`, `oro task gate-state`, `oro task premortem-close` all return "unknown command" with exit code != 0.
13. Worker prompt gate: `pkg/worker/prompt_test.go:TestAssemblePromptHasNoPremortemBranch` — assembling a prompt for a `type=task` bead never emits the premortem section header.
14. Dispatcher gate: `go vet ./pkg/dispatcher/...` is clean (no dead-import warnings from removed gate types).

## Out-of-band cleanup (same PR)

- Run `bd doctor --deep` (or `oro task doctor` equivalent) post-merge to confirm no orphaned premortem-related tracking.
- Pre-merge: confirm `oro.db` shows zero rows where `type='premortem'` (already true).

## Per-decision premortems

### D1 — New `migrate_v4.go` file
- **Tigers**:
  - "v4 file is more code than amend." Reality: 200 lines including tests, vs amending v3 which would also need ~200 lines and breaks idempotency invariant. Net zero.
  - "Migration ordering bugs if v3 and v4 race." Mitigation: migration runner applies in version order; v4 only runs after v3 is confirmed applied (existing infrastructure).
- **Elephants**:
  - "Future v5 migration adds another column premortem-related." Mitigation: by then no premortem concept exists in code; future is unaffected.
- **Paper tigers**:
  - "Bigger surface area." Reality: smaller atomic unit. Easier to review than amended v3.

### D2/D3 — Single rebuild, drop columns + tighten CHECKs
- **Tigers**:
  - "Rebuild is heavy on a 100k-row beads table." Mitigation: existing rebuild pattern handles this; SQLite copy is fast on indexed PK.
  - "Trigger drop/recreate misses one." Mitigation: reuse `dropBeadSchemaRebuildTriggers` + post-rebuild trigger validation.
- **Elephants**:
  - "View definitions reference dropped columns." Verified false: `v3ViewsDDL` references only `id, status, deferred_until, parent_id, deleted, deps, tags` — none of `gate_state`/`premortem_cycle_count`/`pipeline_stage`.
- **Paper tigers**:
  - "ALTER TABLE DROP COLUMN is more idiomatic." Reality: doesn't compose with CHECK changes; we'd still need a rebuild for those.

### D4 — Scrub bead_metadata + bead_journey premortem rows (USER OVERRODE recommendation)
- **Tigers**:
  - "Audit trail loss." Mitigation: the migration_type_converted journey events preserve the type-conversion narrative; the PR description preserves the broader narrative; commit history preserves the deletion narrative. `bead_journey` actor='migration' rows survive scrub.
  - "Downstream observability tools depend on actor='premortem' events." Mitigation pre-merge: grep `actor='premortem'` across all consumers (dashboards, telemetry exporters). If any found, contract negotiation; if none, proceed.
- **Elephants**:
  - "Future audit asks 'what was last night's batch close about?' and we have no record." Mitigation: the commit message of the runtime-disable trio + this PR description form the durable record.
- **Paper tigers**:
  - "Schema cleanliness benefits." Real benefit: the scrubbed columns don't hold meaningful data once their consumers are gone, but the rows would never bother anyone if left.

### D5 — Single big-bang PR
- **Tigers**:
  - "Hard to review ~30 files." Mitigation: structure commits within the PR by surface (worker → CLI → dispatcher → store API → schema → docs); reviewer can walk commit-by-commit.
  - "One bug rolls back everything." Mitigation: acceptance test runs full-suite gate; CI catches before merge.
- **Elephants**:
  - "Mid-PR rebase against main is painful with 30 files." Mitigation: rebase early and often; the only landed-today-touched paths are `cmd/oro/cmd_bead*.go`, `pkg/dispatcher/`, and `pkg/beadstore/` (soft-delete), all of which we're touching anyway — overlap is expected, not a surprise.
- **Paper tigers**:
  - "Phased multiplies review surface." Reality: phased adds review-context overhead per phase; the user judges single PR is the right tradeoff here.

### D6 — Auto-soft-delete legacy `type='premortem'` rows
- **Tigers**:
  - "Silently mutating user data." Mitigation: the migration logs the inventory before mutation; the journey event is durable; the close_reason names the migration explicitly.
  - "Operator wanted to inspect those rows first." Mitigation: in current DB this is a no-op; no rows exist. If future DBs hit this, the rows are still readable by `oro task list --include-deleted` (existing flag) and the journey event lets them reconstruct original type.
- **Elephants**:
  - "Ten thousand legacy rows in some user's DB get auto-converted." None observed; if discovered post-merge, rollback is manual SQL UPDATE on a backup. Out of scope for this spec.
- **Paper tigers**:
  - "Hard-fail is more conservative." Reality: hard-fail blocks `oro start` for any DB with stale rows, which is worse UX than auto-soft-delete with audit.

## Reversibility

- **Code excision**: reversible via `git revert`. Branch saved as `excise-premortem-2026-05-07`.
- **Schema migration**: irreversible without a backup-restore. Migration writes nothing destructive that isn't backed by the audit row in `bead_journey` (for type conversions) or recoverable via PR description (for scrubbed rows). **The migration must run with a fresh `.beads/oro.db` backup taken immediately before, written to `.beads/oro.db.pre-v4-<timestamp>`.** Add this to the migration's first step inside the application (not the migration tx) — copy file before opening connection.

## Implementation order (single PR, ordered commits)

1. `feat(worker): remove premortem prompt branch`.
2. `feat(cards): drop premortem heuristic from card scoring`.
3. `feat(lint): drop premortem closecheck reference`.
4. `feat(dispatcher): remove gate routing + premortem pipeline stage`.
5. `feat(dispatcher): remove retroactive_gate dead code + tests`.
6. `feat(beadstore): remove gate-state Store API + types + constants`.
7. `feat(cli): remove gate-reset/gate-state/premortem-close subcommands`.
8. `feat(beadstore): add migrate_v4 (drop columns, tighten CHECKs, scrub legacy rows)`.
9. `feat(schema): tighten beadTableDDL — drop 'premortem' from type and pipeline_stage CHECKs`.
10. `feat(assets): remove gate-self-block review pattern`.
11. `chore(docs): mark 2026-05-06 punt redeemed; cross-link this design`.

Each commit compiles + tests-green standalone (so reviewers can step through). The schema commit (8 + 9) lands together; commits 1–7 leave columns orphaned but harmless.

## Load-bearing assumptions (must verify in adversarial review)

1. **Zero non-test production callers** of `CheckPremortemGate`, `SetGateState`, `GateState`, `HasClosedPremortemChild`, `SetPremortemVerdict`, `IncrPremortCycleCount`, `ResetPremortCycleCount` outside the surfaces enumerated above. Verification: `rg` for each symbol; result must match the file list in "Affected surface".
2. **Zero rows where `type='premortem'`** in the live DB at migration time. Verification: pre-migration `SELECT COUNT(*) FROM beads WHERE type='premortem'` logged.
3. **No external consumer** (telemetry exporter, dashboard) reads `bead_journey` filtering on `actor='premortem'`. Verification: `rg "actor.*premortem"` across `cmd/`, `pkg/`, `assets/`, plus any out-of-tree integration documentation.
4. **`v3ViewsDDL` does not reference dropped columns.** Verified by inspection (line 137-231 of `migrate_v3.go`); the views select `b.*` but the existence check is on `deleted`, `status`, `deferred_until`, `parent_id`, `bead_deps`, `bead_tags`, `assignments`, `beads.parent_id`, `beads.deleted`, `beads.status`. None of `gate_state`/`premortem_cycle_count`/`pipeline_stage`.
5. **The user's planning-skill premortem references** (README, CLAUDE.md, skill SKILL.md files) describe a planning *practice*, not bead-type machinery. Verification: each reference reads as "premortem each decision," "decision-level premortem," etc., none reference `type=premortem` beads or gate states.

## Status checkpoints

- [x] Stage 1 — Brainstorm (this doc)
- [ ] Stage 2 — Consultation (six forcing questions)
- [ ] Stage 3 — Adversarial review (`adversarial-spec-review` subagent)
- [ ] Stage 4 — Beadcraft decompose
