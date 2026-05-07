# Premortem Excision Design

Date: 2026-05-07
Status: draft (Stage 1 — Brainstorm; revised post-adversarial-review v3)

## Problem

Oro's beadstore carries a "premortem gate" subsystem: `pkg/dispatcher/retroactive_gate.go` auto-spawned a `type=premortem` child bead whenever an epic crossed five children, and `CheckPremortemGate` blocked sibling execution until that child closed. The runtime was disabled today (commits `d63c54ea`, `e2bc4ea5`, `bd52b1f0`; tasks oro-2vma, oro-c4yf, oro-k2k2 closed) but the codebase still carries the carcass: ~35 Go files, three Store API methods, two beads-table columns, two CHECK enum values, multiple worker-prompt functions, three CLI subcommands, asset patterns, and design-doc references.

The signal that the design is wrong is direct: when four epics crossed the threshold the night of 2026-05-06, the operator batch-closed all four auto-spawned premortem beads with `verdict=replan` and reason "Deleted per user request". A gate whose output is uniformly discarded produces friction without value. The premortem-as-a-bead-type concept conflates "task to execute" with "checkpoint to consider"; the latter belongs in the planning skill (`.claude/skills/premortem`), which already exists and is preserved.

The 2026-05-06 task-delete-and-no-premortem-beads design explicitly punted this excision as the next-phase elephant: "The premortem subsystem is now legacy code, but fully deleting it is larger than the requested fix and risks migration churn." This spec redeems that punt.

## Goals

- Remove all dead premortem-as-bead-type code: dispatcher (retroactive gate, router, sweep, hot-path notifier), beadstore Store API, pkg/cards heuristic, pkg/lint closecheck, worker prompt premortem function family, CLI subcommands.
- Drop `gate_state` and `premortem_cycle_count` columns from the beads table.
- Tighten the `type` CHECK constraint to remove `'premortem'` and the `pipeline_stage` CHECK to remove `'premortem'`.
- Scrub `bead_metadata` rows with keys `premortem_verdict`/`premortem_reason` and `bead_journey` rows with `actor='premortem'`.
- Defensive handling of any pre-existing `type='premortem'` rows (none observed): auto-soft-delete with a `migration_type_converted` audit event preserving the original type.
- Land as a single PR with a passing acceptance test that includes a build-graph completion check.

## Non-goals

- Do not remove the SKILL-level premortem (planning exercise) at `.claude/skills/premortem/`, `assets/skills/premortem/`, or its references in `brainstorming/`, `workflow-routing/`, `oro/` skill docs, README. The bead-type and the skill share a name but are distinct concepts.
- Do not change the soft-delete CLI shipped today (oro-jv6p chain) or its store contract.
- Do not change the dispatcher stability fixes shipped today (HeartbeatTimeout, AssignBead, etc.).
- Do not introduce a replacement gating concept; this is pure removal.

## Affected surface (cited, by symbol)

### Dispatcher

- `pkg/dispatcher/retroactive_gate.go` — **delete file**. Symbols removed: `CheckPremortemGate` (`//oro:testonly`), `PremortemGateError`, `blockerHitAlreadyRecorded`. `CreateBeadGraph` migrates to a small new helper file `pkg/dispatcher/bead_graph.go` (no gate logic).
- `pkg/dispatcher/retroactive_gate_test.go` — **delete file**.
- `pkg/dispatcher/premortem_routing_test.go` — **delete file**.
- `pkg/dispatcher/premortem_verdict_test.go` — **delete file**.
- `pkg/dispatcher/pipeline_state.go` — remove `StagePremortem` from `PipelineStage` enum; remove any internal premortem-stage routing.
- `pkg/dispatcher/router.go` — delete:
  - `premortemVerdictPayload` struct (line 18)
  - `isValidPremortemVerdict` (line 25)
  - `parsePremortemVerdict` (line 39)
  - `case "premortem":` branch in `BuildPrompt` (line 65)
  - `(*Dispatcher).ClosePremortemBead` (line 131)
  - `(*Dispatcher).applyPremortemVerdict` (line 149)
  - `ApplyPremortemVerdict` (line 155)
  - `ClosePremortemBeadWithStore` (line 195)
  - `(*Dispatcher).warnInvalidPremortemVerdict` (line 214)
  - Estimated ~150 of 221 lines deleted.
- `pkg/dispatcher/router_test.go` — delete all test functions exercising the symbols above.
- `pkg/dispatcher/sweep.go` — delete:
  - `nopPremortCounter` struct + `SetPremortCycleCount` method (lines 31-37)
  - `parseReplanCycleNum` (line 40)
  - `PremortCounter` interface (line 60)
  - `OnReplanChildrenClosed` (line 189)
  - `ErrReplanLoopExhausted` sentinel
  - `defaultMaxPremortemCycles` const
- `pkg/dispatcher/sweepers_test.go` — delete only the `TestSweepers` subtests targeting `OnReplanChildrenClosed/*` (4 subtests). **Preserve** the 8 subtests for `PromoteClosedParentChildren`, `ReapDeletedParentChildren`, and `ExpireReviewQueueSLA` — these test sweepers we keep.
- `pkg/dispatcher/dispatcher.go` — at lines **2072 (caller)** and **2078-2094 (definition)**: delete `d.safeGo(func() { d.notifyReplanChildClosed(ctx, beadID) })` from `finalizeSuccessfulMerge` AND delete the `notifyReplanChildClosed` method itself. **This is a production hot-path on every successful merge.** `CreateBeadGraph` callsite at line 1974 stays (no longer triggers gate).
- `pkg/dispatcher/dispatcher_test.go` — delete tests for `notifyReplanChildClosed`, replan-cycle handling, and gate-state filtering.

### Beadstore

- `pkg/beadstore/v3types.go` — remove `GateState` type + 6 gate constants (`GateNone`, `GateEligible`, `GateSatisfied`, `GateBlocked`, `GateReplan`, `GateEscalated`); remove `StagePremortem` from `PipelineStage` constants; remove `ErrStaleGate`. Update `JourneyEvent.Actor` field comment to drop `premortem` from the enumerated values list.
- `pkg/beadstore/v3methods.go` — remove `SetGateState`, `GateState`, `HasClosedPremortemChild`, `SetPremortemVerdict`, `IncrPremortCycleCount`, `ResetPremortCycleCount`.
- `pkg/beadstore/store.go` — remove the corresponding interface methods.
- `pkg/beadstore/sqlite.go` — remove implementations + the v3-migrations comment at line 45 (replace with neutral wording).
- `pkg/beadstore/shadow.go` — remove gate-state shadow paths.
- `pkg/beadstore/testfake.go` — remove fake-store gate-state methods.
- `pkg/beadstore/v3_methods_test.go`, `pkg/beadstore/store_test.go`, `pkg/beadstore/read_tx_parity_test.go` — remove gate-state and premortem-verdict tests; preserve all other v3 method tests.

### Migrations

- `pkg/beadstore/migrations/migrate_v3.go` — **modify minimally**: add a guard at function entry that reads `PRAGMA user_version` and returns nil if `>= 4`. This prevents `MigrateToV3` from re-adding `gate_state`/`premortem_cycle_count` columns on every plain `openStateDB` call after v4 has dropped them. Without this guard, the v2 architecture is broken in steady state: every `oro task list`/`oro logs`/`oro work` invocation would silently un-migrate part of v4 (because `tryAlterTableAddColumn` only silences `'duplicate column name'`, not the post-drop success path). The guard is one of the only acceptable in-place edits to a shipped migration; it is purely defensive (skips work, doesn't change shape). Rest of the file untouched.
- `pkg/beadstore/migrations/migrate_v4.go` — **NEW**. See Migration Design below.
- `pkg/beadstore/migrations/migrate_v4_test.go` — **NEW**. Test set in Acceptance Test below.
- `pkg/beadstore/migrations/migrate_v3_test.go`, `bead_type_check_test.go` — update expectations to the post-v4 schema (no gate_state, no premortem_cycle_count, type CHECK omits 'premortem').

### Schema

- `pkg/protocol/schema.go:184-185` — remove `'premortem'` from the `type` CHECK constraint in `beadTableDDL`. Result: `CHECK (type IN ('task','bug','epic','research','chore','review'))`.
- `pkg/protocol/schema.go:629-706` — `EnsureBeadTypeCheckConstraint` and `runBeadsTypeRebuild`. **Retirement decision**: replace `EnsureBeadTypeCheckConstraint` body with a no-op (returns `false, nil`) + deprecation comment ("retired 2026-05-07: migrate_v4 owns CHECK tightening; legacy DBs reach the new shape via migrate_v4"). **Delete `runBeadsTypeRebuild` outright** in the same commit — it is unexported (lowercase r, single call site at line 656) and becomes orphaned `unused` (golangci-lint U1000 fail) once its caller is no-op'd. `migrate_v4` inlines an equivalent rebuild pattern (Migration Design step 4) rather than calling the helper, because v4's rebuild also drops columns whereas `runBeadsTypeRebuild` only updates CHECKs. Justification: leaving the function as-is creates two competing CHECK-tightening paths that race on detection; retiring it makes v4 the single source of truth and removes the dead-code lint risk.

- `pkg/protocol/schema.go:498-510 (MigrateBeadSchema)` — caller of `EnsureBeadTypeCheckConstraint`. The bool return is stored in `rebuiltTypeConstraint` and used on line 506 (`rebuiltStatusConstraint || rebuiltTypeConstraint`) to gate `INSERT INTO beads_fts(beads_fts) VALUES('rebuild')`. Post-retirement the bool is always `false`, so FTS rebuild on this path fires only when status migration triggers it. This is safe because v4's own FTS rebuild (Migration Design step 5) covers the v3→v4 transition; subsequent type-CHECK changes don't exist (CHECK is stable post-v4). Caller wiring stays unchanged.

### CLI

- `cmd/oro/cmd_bead.go` — remove `gate-reset`, `gate-state`, `premortem-close` subcommand factories and their wiring in `newBeadCmdWithStore`. **Keep** the `--type=premortem` rejection guard (CHECK is double-protection but the guard returns a clearer error message). Update line 840-841 outdated comment ("v3 migrations (gate_state, premortem_cycle_count, etc.)") to reflect post-v4 shape.
- `cmd/oro/cmd_task.go` — remove the corresponding alias registrations on `newTaskCmdWithStore`.
- `cmd/oro/cmd_bead_test.go`, `cmd_task_test.go`, `cmd_work_test.go`, `cmd_work_execute_test.go` — remove premortem-gate / premortem-close test cases. Keep `TestTaskCreateRejectsPremortemType` (still wanted; guard remains).
- `cmd/oro/db.go:30 openStateDB` — **leave default migration path applying through v3 only** (do NOT auto-trigger v4 here). Add a sibling function `openStateDBWithV4Migration(ctx, path)` that wraps `openStateDB` and additionally runs the v3→v4 transition (backup + `migrations.MigrateToV4`). **Only `cmd_start.go:openStateDB callsite (line 761)` calls the v4-migration variant**; all other 13+ callsites continue using plain `openStateDB`. Rationale: `oro start` is the dispatcher entry point and is the natural single migration trigger; CLI commands like `oro task list`, `oro logs`, `oro task show` work read-only at v3 schema (none reference dropped columns), so the upgrade window is non-blocking for observability commands. The active-assignment guard inside `migrate_v4` then becomes defense-in-depth (a running dispatcher cannot start while assignments are active by existing pre-existing constraints).
- Pre-migration backup sequence (inside `openStateDBWithV4Migration`, after first connection-open + PRAGMA detection):
  1. Open SQL connection on `paths.StateDBPath`.
  2. `PRAGMA table_info(beads)` to detect v3 shape (`gate_state` column present).
  3. If v3 detected: close connection, `io.Copy` source file to `<path>.pre-v4-<RFC3339>` (open source, open dest with O_CREATE|O_WRONLY|O_EXCL, io.Copy, fsync dest, close both — no `os.Rename` because rename moves the file leaving no original).
  4. Reopen SQL connection, run `migrations.MigrateToV4(ctx, db)`.
  5. If v4 already applied (no `gate_state` column), skip backup, no-op migration.
- `cmd/oro/cmd_bead_migrate.go:339, 1393` — these callsites use plain `openStateDB`; document that running `oro task migrate-from-dolt` does NOT trigger v4. The intended upgrade order is: stop running dispatcher, then `oro start` (which triggers v4).

### Worker

- `pkg/worker/prompt.go` — delete (~85 lines):
  - `PremortemPromptParams` struct (line 119)
  - `AssemblePremortemPrompt` (line 132)
  - `premortemPromptHeader` (line 136)
  - `premortemPromptBody` (line 159)
  - `premortemPromptFooter` (line 193)
  - `AssemblePrompt` (line 77) is **untouched** — it has no premortem branch (corrects v0 spec error).
- `pkg/worker/premortem_prompt_test.go` — **delete file**.
- `pkg/worker/prompt_test.go` — remove any test importing or asserting on `AssemblePremortemPrompt`.

### Cards / lint / integration / scripts

- `pkg/cards/store.go:454` — remove `|| beadType == "premortem"` (one-liner).
- `pkg/lint/closecheck/closecheck.go` — remove premortem references in `isBlessedCloser` and lines 69, 110-111 godoc / switch case for `ClosePremortemBeadWithStore`.
- `tests/integration/verify_retroactive_gate_test.go` — **delete file**.
- `scripts/verify-retroactive-gate.sh` — **delete file**. Add a tombstone test (per the `dead-subpackage-deletion-needs-testonly` review-patterns idiom) at `tests/integration/forbidden_paths_test.go:TestVerifyRetroactiveGateScriptRemoved` asserting `os.Stat` returns `ErrNotExist`.

### Assets / docs

- `assets/review-patterns.md` — comprehensive audit + scrub of premortem-coupled entries:
  - Line 101 (`spec-deviation-with-rationale: ... router.go:18-23 is a clean example`): the cited router.go lines are the `premortemVerdictPayload` struct being deleted. Either rewrite citation to a different example or delete the entry. **Decision**: keep entry, replace citation with a non-premortem example (TBD during impl).
  - Line 119 (`gate-self-block: ... premortem-type beads short-circuit \`CheckPremortemGate\``): **delete entry**. Pattern is exclusively premortem-specific.
  - Line 123 (`post-reset-roundtrip-test: ... subsequent OnReplanChildrenClosed`): **delete entry** (cites a removed symbol).
  - Plus: re-grep `assets/review-patterns.md` for any other premortem/gate/OnReplan/CheckPremortemGate references and scrub.
- `docs/plans/2026-05-06-task-delete-no-premortem-beads-design.md` — leave intact (historical record); add a one-line note at top: "Status: punt redeemed by 2026-05-07-premortem-excision-design.md".
- `docs/plans/2026-04-28-oro-harness-architecture-spec.md` — historical architecture spec lists `premortem` in glossary and references gate state extensively. **Decision**: add a one-line "superseded by 2026-05-07-premortem-excision-design.md for premortem subsystem" note at the top of relevant sections; do not rewrite. Future readers should know the architecture spec is partially historical.
- README.md, CLAUDE.md, `.claude/skills/oro/SKILL.md`, `.claude/skills/brainstorming/SKILL.md`, `.claude/skills/workflow-routing/SKILL.md`, `assets/skills/oro/SKILL.md`, `assets/skills/brainstorming/SKILL.md`, `assets/skills/workflow-routing/SKILL.md`, `.claude/skills/premortem/SKILL.md`, `assets/skills/premortem/SKILL.md` — **PRESERVE**. These reference the planning skill, not the bead type.

## Migration Design (`migrate_v4.go`)

Single atomic table-rebuild migration. All steps in one transaction except the precondition check + pre-migration file backup (which run before opening the SQL connection).

### Pre-tx preconditions

**P1. Logically-consistent backup via SQLite Online Backup API (in `openStateDBWithV4Migration` only):**
- Open SQL connection.
- Read `PRAGMA user_version` — if `>= 4`, return early (no backup, no migration). DB is post-v4.
- `PRAGMA table_info(beads)` — if `gate_state` column absent, return early (fresh post-v4 DB; migrate_v4's idempotency-step bumps user_version).
- If `gate_state` present and `user_version < 4`: invoke SQLite's online backup via the mattn/go-sqlite3 driver: `db.Conn(ctx)` → `conn.Raw(func(driverConn any) error)` → cast to `*sqlite3.SQLiteConn` → call `srcConn.Backup(...)` writing to `<dbpath>.pre-v4-<RFC3339Nano>`. The online backup API is consistent under concurrent activity (handles WAL correctly) and produces a logically-coherent v3 snapshot. Filename uses `RFC3339Nano` (not `RFC3339`) to avoid second-resolution collisions.
- If the driver-cast or backup call fails, fall back to: connection-close → `flock(2)` exclusive lock on dbPath → `io.Copy` (open source O_RDONLY, open dest O_CREATE|O_WRONLY|O_EXCL mode 0600, copy bytes, `f.Sync()` dest, close both, release flock) → reopen connection. Do **not** use `os.Rename` (it moves, leaves no original). The flock fallback is a defense-in-depth measure since `oro start` already requires no concurrent dispatcher.
- Backup file is `os.Remove`'d if migration tx returns an error (defer pattern in the migration entry function), preventing accumulation on failed-run recovery. On successful migration, backup is retained for the runbook-documented `rm <dbpath>.pre-v4-*` cleanup step.

**P2. Active-assignment guard (defense-in-depth, first SQL of migrate_v4):**
- `SELECT COUNT(*) FROM assignments WHERE status='active'`. If count > 0, abort with: `migrate_v4: cannot migrate while N active assignments exist; run 'oro stop' first then re-run 'oro start'`. Because v4 is only triggered by `oro start` (which itself ensures no other dispatcher is running) and `oro start` boots before workers reattach, this guard is unlikely to fire in practice. It exists to catch a manual-invocation footgun (a developer calling `MigrateToV4` directly with a stale DB) and document the operator constraint.

### Steps inside single tx

1. **Inventory pass** — `SELECT id, type, status, deleted FROM beads WHERE type='premortem'`. Log count + IDs to migration log (slog Info level).

2. **Defensive convert** of any legacy `type='premortem'` rows:
   - For each row: `INSERT INTO bead_journey (bead_id, ts, actor, event, payload) VALUES (?, ?, 'migration', 'migration_type_converted', '{"original_type":"premortem","reason":"premortem-excision"}')`.
   - `UPDATE beads SET deleted=1, type='task', close_reason='premortem-excision: auto-soft-deleted by migrate_v4', updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE type='premortem'`.
   - In current DB this is a no-op (zero rows).

3. **Scrub historical premortem rows**:
   - `DELETE FROM bead_metadata WHERE key IN ('premortem_verdict','premortem_reason')`.
   - `DELETE FROM bead_journey WHERE actor='premortem'`.
   - The `migration_type_converted` events from step 2 use `actor='migration'` and survive this scrub.

4. **Table rebuild** (legacy_alter_table=ON pattern; foreign_keys=OFF; mirrors `runBeadsTypeRebuild` in `pkg/protocol/schema.go`):
   - `DROP VIEW IF EXISTS beads_ready; DROP VIEW IF EXISTS beads_blocked;`
   - Drop schema-rebuild triggers (reuse `dropBeadSchemaRebuildTriggers`).
   - `ALTER TABLE beads RENAME TO beads_v4_rebuild_old`.
   - Create new `beads` table from updated `beadTableDDL`:
     - No `gate_state` column.
     - No `premortem_cycle_count` column.
     - `type` CHECK omits `'premortem'`.
     - `pipeline_stage` CHECK omits `'premortem'`.
     - All other v3 columns and constraints preserved verbatim.
   - `INSERT INTO beads (<column-list-without-dropped>) SELECT <same-list> FROM beads_v4_rebuild_old`. Column list explicitly enumerates all post-v4 columns; `gate_state`/`premortem_cycle_count` are absent from both sides.
   - `DROP TABLE beads_v4_rebuild_old`.
   - Recreate `beads_ready` and `beads_blocked` views from `v3ViewsDDL` verbatim (these views use `SELECT b.*` which dynamically resolves to the post-rebuild column set; views must be CREATEd after the rebuild, which step ordering guarantees).
   - Recreate FTS triggers (reuse the trigger DDL from `MigrateBeadSchema`).

5. **FTS content rebuild**:
   - `INSERT INTO beads_fts(beads_fts) VALUES('rebuild')` to re-index the rebuilt beads table. (Pattern matches existing post-rebuild calls in `MigrateBeadSchema`.)

6. **Foreign-key integrity check INSIDE tx**:
   - `PRAGMA foreign_key_check`. If any rows returned, ROLLBACK and return error. **The check runs before commit so rollback is possible.**

7. **Commit**. Final tx step before COMMIT: `PRAGMA user_version = 4`. This is the sentinel `MigrateToV3` reads to short-circuit on post-v4 DBs.

### Idempotency

`migrate_v4` first reads `PRAGMA user_version`. If `>= 4`, return early (no-op). Otherwise, run `PRAGMA table_info(beads)`; if `gate_state` is absent (fresh DB on post-v4 schema before user_version is bumped), bump user_version to 4 and return. This double-check handles three states cleanly:
- v4 already applied (user_version=4): no-op.
- Fresh install on post-v4 `beadTableDDL` but v3 hasn't been re-run (gate_state absent): bump user_version, no-op.
- v3 shape (gate_state present): run full migration including the user_version=4 step at commit time.

### Why not ALTER TABLE DROP COLUMN

SQLite 3.35+ DROP COLUMN works for column removal but does not allow CHECK constraint changes in the same step. A single rebuild does both column-drop and CHECK-tighten atomically — matches the established `runBeadsTypeRebuild` pattern in this codebase and removes the SQLite-version dependency.

### Why one tx

- Atomicity: any failure rolls back to v3 state; no half-migrated tables.
- Concurrency: triggering only from `oro start` ensures no other dispatcher process is running; the active-assignment guard catches the manual-invocation footgun.

### Why migrate_v3 stays untouched

`migrate_v3.go:30-31` adds `pipeline_stage` with a CHECK that includes `'premortem'`. Spec leaves that file intact for two reasons: (a) hash-stable history — modifying a shipped migration breaks idempotency-detection across DBs with different application states; (b) safety — v4's table rebuild tightens the CHECK before any worker can insert `pipeline_stage='premortem'` (the only writer was `notifyReplanChildClosed`, deleted in commit 6). On fresh installs the sequence is: `beadTableDDL` (post-v4 shape) → `MigrateBeadSchema` (no-op) → `MigrateToV3` (re-adds gate_state etc., per its idempotent ALTER) → `MigrateToV4` (drops them via rebuild). Net: correct shape, one wasted rebuild cycle per fresh install. Acceptable tradeoff.

## Acceptance Test

```bash
go test ./cmd/oro ./pkg/beadstore ./pkg/beadstore/migrations ./pkg/dispatcher ./pkg/worker ./pkg/cards ./pkg/lint/closecheck ./pkg/protocol ./tests/integration && \
go build ./... && \
! grep -rl --include='*.go' \
  -e 'CheckPremortemGate' -e 'SetGateState' -e 'GateState' -e 'HasClosedPremortemChild' \
  -e 'SetPremortemVerdict' -e 'IncrPremortCycleCount' -e 'ResetPremortCycleCount' \
  -e 'StagePremortem' -e 'OnReplanChildrenClosed' -e 'ClosePremortemBead' -e 'ApplyPremortemVerdict' \
  -e 'AssemblePremortemPrompt' -e 'PremortemPromptParams' -e 'premortemVerdictPayload' \
  -e 'defaultMaxPremortemCycles' -e 'nopPremortCounter' -e 'PremortCounter' \
  -e 'parseReplanCycleNum' -e 'ErrReplanLoopExhausted' -e 'ErrStaleGate' -e 'notifyReplanChildClosed' \
  pkg/ cmd/ tests/ \
  | grep -vE '(/\.worktrees/|/migrations/migrate_v4|/forbidden_paths_test\.go|symbols_removed_test\.go)'
```

`grep` returns 0 matching files (excluding the migration file itself, the tombstone test, and any acceptance-guard test that intentionally references the removed names).

### Required test functions

1. `TestMigrateV4DropsGateColumns` — given a v3-shaped fixture, after migrate_v4 the `beads` table has no `gate_state` and no `premortem_cycle_count` columns (`PRAGMA table_info`).
2. `TestMigrateV4TightensTypeCheck` — `INSERT INTO beads (..., type, ...) VALUES (..., 'premortem', ...)` returns a CHECK constraint error after migration.
3. `TestMigrateV4TightensPipelineStageCheck` — same for `pipeline_stage='premortem'`.
4. `TestMigrateV4ConvertsLegacyPremortemRows` — fixture seeded with 2 rows of `type='premortem'`; after migration both rows have `type='task'`, `deleted=1`, `close_reason` containing 'premortem-excision', and a `bead_journey` event with `actor='migration'`, `event='migration_type_converted'`, payload containing `"original_type":"premortem"`.
5. `TestMigrateV4ScrubsBeadMetadata` — fixture seeded with 4 rows of `key='premortem_verdict'` + 4 rows of `key='premortem_reason'`; after migration, zero rows for both keys.
6. `TestMigrateV4ScrubsJourneyPremortemActor` — fixture seeded with 3 rows of `actor='premortem'`; after migration, zero rows. The `migration_type_converted` events from test 4 remain.
7. `TestMigrateV4Idempotent` — running migrate_v4 twice on the same DB produces the same output as running once; second invocation is a no-op (verified via PRAGMA table_info early return).
8. `TestMigrateV4PreservesNonPremortemData` — fixture has 100 normal beads of varied types/statuses + dependencies + assignments (status='inactive') + tags + metadata; counts and edges preserved post-migration.
9. `TestMigrateV4PreservesViews` — `beads_ready` and `beads_blocked` views return the same rows as a v3 baseline for the non-premortem fixture.
10. `TestMigrateV4PreservesFTS` — fixture has 50 beads with title + description text; after migration, `SELECT id FROM beads_fts WHERE beads_fts MATCH 'specific-token'` returns the same set as pre-migration.
11. `TestMigrateV4FKViolationRollsBack` — pre-seed an FK-violating row in `bead_deps` with `PRAGMA foreign_keys=OFF` (an orphan `depends_on_id` pointing to a non-existent bead), then invoke migrate_v4. Step 6's `PRAGMA foreign_key_check` detects the orphan and triggers ROLLBACK. Assert DB at original v3 shape (gate_state column still present, no migration_type_converted events emitted, original orphan still present in bead_deps).
12. `TestMigrateV4RejectsActiveAssignments` — fixture seeded with `INSERT INTO assignments (..., status='active', ...)`; migrate_v4 returns the documented error and does not mutate any rows.
13. `TestMigrateV4WritesPreMigrationBackup` — call `openStateDB` on a v3-shape file; assert a sibling file matching `<path>.pre-v4-*` exists and is byte-identical to the original.
14. `TestSymbolsRemoved` (build-graph completion) — runs the grep gate above as a Go test; fails if any production .go file references a removed symbol.
15. `TestVerifyRetroactiveGateScriptRemoved` — `os.Stat("scripts/verify-retroactive-gate.sh")` returns `ErrNotExist`.
16. `TestReviewPatternsScrubbed` — `assets/review-patterns.md` contains zero substring matches for `gate-self-block`, `OnReplanChildrenClosed`, `CheckPremortemGate`, or `premortem-type beads`.
17. `TestCLIPremortemSubcommandsRemoved` — `oro task gate-reset`, `oro task gate-state`, `oro task premortem-close`, `oro bead gate-reset`, `oro bead gate-state`, `oro bead premortem-close` all return exit code != 0 and stderr containing "unknown command".
18. `TestTaskCreateRejectsPremortemType` (existing, kept) — `oro task create --type premortem` fails and creates no bead.
19. `TestMigrateV3RespectsV4UserVersion` — seed a DB with `PRAGMA user_version=4` and post-v4 schema; call `MigrateToV3`; assert no ALTER fired (gate_state and premortem_cycle_count columns remain absent).
20. `TestPlainOpenStateDBDoesNotTriggerV4` — open a v3-shape DB with plain `openStateDB` (not the v4 variant) twice; assert gate_state column persists (v3 schema preserved), no backup file created, no migrate_v4 side effects.
21. `TestCreateBeadGraphCreatesChildren` (in bead_graph_test.go) — smoke test for the relocated function: creates 3 children under a parent, asserts each child's `ParentID` is forced to the parent regardless of input, asserts return slice shape matches input.

## Implementation order (single PR, ordered commits)

Each commit must compile + pass tests standalone — reviewers walk commit-by-commit and CI runs the full suite per commit. Bottom-up by dependency: callers first, then callees. Where caller and callee deletions are tightly coupled (and would otherwise cross a compile gap), they are bundled into one commit.

1. `feat(cards): drop premortem heuristic from card scoring` — pkg/cards/store.go:454 one-liner + test update. No dependencies.
2. `feat(dispatcher): remove router + worker premortem prompt + close path together` — bundles caller (router.go BuildPrompt 'premortem' branch + ApplyPremortemVerdict + ClosePremortemBeadWithStore + 6 other symbols) AND callees (worker.AssemblePremortemPrompt family in prompt.go + premortem_prompt_test.go). Also: pkg/lint/closecheck/closecheck.go isBlessedCloser entry (must drop in same commit since closecheck inspects the now-removed `ClosePremortemBeadWithStore`). Plus router_test.go premortem tests + prompt_test.go premortem branches deleted. Single atomic commit because the cross-package dependency cannot be split without breaking compile or lint.
3. `feat(dispatcher): remove sweep replan-cycle plumbing + finalizeSuccessfulMerge notifier together` — bundles caller (`dispatcher.go:2072` + `dispatcher.go:2078-2094 notifyReplanChildClosed`) AND callees (sweep.go: ErrReplanLoopExhausted, defaultMaxPremortemCycles, nopPremortCounter, parseReplanCycleNum, PremortCounter interface, OnReplanChildrenClosed). Plus sweepers_test.go OnReplanChildrenClosed/* subtests deleted (preserves all other sweepers tests — count varies by file state). Plus dispatcher_test.go replan-cycle tests. Single atomic commit because dispatcher.go references sweep.go symbols.
4. `feat(dispatcher): remove pipeline_state premortem stage and retroactive_gate` — pipeline_state.go StagePremortem + delete retroactive_gate*.go + premortem_routing_test.go + premortem_verdict_test.go + relocate CreateBeadGraph to bead_graph.go + add bead_graph_test.go (smoke test: creates child under parent, asserts ParentID overwrite + return shape).
5. `feat(beadstore): remove gate-state Store API + types + constants` — store.go interface + v3types.go + v3methods.go + sqlite.go + shadow.go + testfake.go + v3_methods_test.go + read_tx_parity_test.go + store_test.go.
6. `feat(cli): remove gate-reset/gate-state/premortem-close subcommands` — cmd_bead.go + cmd_task.go + tests; keep type=premortem rejection guard.
7. `feat(integration): remove verify_retroactive_gate test + script + add tombstone` — delete tests/integration/verify_retroactive_gate_test.go and scripts/verify-retroactive-gate.sh; add forbidden_paths_test.go with TestVerifyRetroactiveGateScriptRemoved.
8. `feat(schema): retire EnsureBeadTypeCheckConstraint, delete runBeadsTypeRebuild, tighten beadTableDDL` — pkg/protocol/schema.go updates; replace `EnsureBeadTypeCheckConstraint` body with `return false, nil` + deprecation comment; **delete `runBeadsTypeRebuild`** (avoids `unused` lint); remove `'premortem'` from `beadTableDDL` type CHECK. **Delete pkg/beadstore/migrations/bead_type_check_test.go** in this commit (the test seeds a legacy DDL and asserts that calling the function adds a CHECK; with the function as a no-op, the assertion deterministically fails — the test is fundamentally invalidated by the no-op refactor and cannot be salvaged with content edits).
9. `feat(beadstore): MigrateToV3 user_version guard + add migrate_v4` — pkg/beadstore/migrations/migrate_v3.go gains a `PRAGMA user_version >= 4 → return nil` guard at function entry. Add migrations/migrate_v4.go (table rebuild + scrub + FTS rebuild + FK check inside tx + `PRAGMA user_version=4` final step) + migrate_v4_test.go covering all required tests below.
10. `feat(cli): add openStateDBWithV4Migration and wire only into oro start` — cmd/oro/db.go gains `func openStateDBWithV4Migration(ctx context.Context, path string) (*sql.DB, error)` (open conn → user_version PRAGMA detect → online backup via SQLite backup API with flock fallback → MigrateToV4 with deferred backup-cleanup-on-error). cmd_start.go:761 switches to the new variant (passing `cmd.Context()`). All other 12 production openStateDB callsites stay on plain `openStateDB`. db_test.go gets `TestMigrateV4WritesPreMigrationBackup`, `TestPlainOpenStateDBDoesNotTriggerV4` (regression: open DB twice with plain function, assert columns unchanged), and `TestMigrateV3RespectsV4UserVersion` (post-v4 DB, call MigrateToV3, assert no ALTER fired).
11. `chore(assets): scrub premortem entries from review-patterns.md` — line 101 citation rewrite, line 119 + 123 deletions, audit pass.
12. `chore(docs): mark 2026-05-06 punt redeemed and 2026-04-28 architecture partially superseded` — header notes only.

The build-graph grep gate (acceptance test #14) runs in CI; any commit that introduces an orphan symbol fails the gate. CI must run `go build ./...` AND `golangci-lint run` per commit (verified locally before push).

## Per-decision premortems (tigers/elephants/paper_tigers)

### D1 — New `migrate_v4.go` file
- **Tigers**:
  - Migration ordering bugs if v3 and v4 race. Mitigation: existing migration runner applies in version order; v4 only runs after v3 confirmed applied.
  - Backup file accumulates indefinitely. Mitigation: backup is one-shot per v3→v4 transition; existing DBs migrate once.
- **Elephants**:
  - Future v5 adds another column premortem-related. By then no premortem concept exists in code; future is unaffected.
- **Paper tigers**:
  - "Bigger surface area." Reality: smaller atomic unit; easier review than amended v3.

### D2/D3 — Single rebuild, drop columns + tighten CHECKs
- **Tigers**:
  - Rebuild is heavy on a 100k-row beads table. Mitigation: existing rebuild pattern handles this; SQLite copy is fast on indexed PK.
  - Trigger drop/recreate misses one. Mitigation: reuse `dropBeadSchemaRebuildTriggers` + post-rebuild trigger validation.
  - **FTS index drift after rebuild.** Mitigation: explicit `INSERT INTO beads_fts(beads_fts) VALUES('rebuild')` step + `TestMigrateV4PreservesFTS` test.
- **Elephants**:
  - View definitions reference dropped columns. Verified via b.* semantics: views are dropped before rebuild and recreated after, so column resolution catches the post-rebuild shape.
- **Paper tigers**:
  - "ALTER TABLE DROP COLUMN is more idiomatic." Reality: doesn't compose with CHECK changes; we'd still need a rebuild for those.

### D4 — Scrub bead_metadata + bead_journey premortem rows (USER OVERRODE recommendation)
- **Tigers**:
  - Audit trail loss. Mitigation: the `migration_type_converted` journey events (actor='migration') preserve type-conversion narrative; PR description preserves broader narrative; commit history preserves deletion narrative.
  - Downstream observability tools depend on `actor='premortem'` events. Mitigation pre-merge: `rg "actor='premortem'"` across all consumers (dashboards, telemetry exporters); search returned zero non-test producers in this repo.
- **Elephants**:
  - "Future audit asks 'what was last night's batch close about?' and we have no record." Mitigation: commit message of the runtime-disable trio + this PR's description form the durable record.

### D5 — Single big-bang PR
- **Tigers**:
  - Hard to review ~30 files. Mitigation: 15 ordered commits within the PR (see Implementation order); reviewer walks commit-by-commit.
  - One bug rolls back everything. Mitigation: build-graph grep gate (test #14) catches missed surface; CI runs all migration tests.
- **Elephants**:
  - Mid-PR rebase against main is painful. Mitigation: rebase early and often; today's landed-touched paths (`cmd/oro/cmd_bead*.go`, `pkg/dispatcher/`, `pkg/beadstore/`) overlap with our changes — that's expected, not a surprise.

### D6 — Auto-soft-delete legacy `type='premortem'` rows
- **Tigers**:
  - Silently mutating user data. Mitigation: migration logs the inventory before mutation; journey event is durable; close_reason names the migration explicitly.
  - Operator wanted to inspect those rows first. Mitigation: in current DB this is a no-op; rows still readable post-migration via `oro task list --include-deleted` (existing flag) and the journey event preserves the original type.

### D7 — Retire `EnsureBeadTypeCheckConstraint`
- **Tigers**:
  - Existing call sites in `MigrateBeadSchema` may still expect the function to do work. Mitigation: replace body with no-op + deprecation comment; call sites preserve their structure but become no-ops.
  - A future `MigrateBeadSchema` change re-introduces a CHECK fixup elsewhere. Mitigation: deprecation comment names migrate_v4 as the owner; future migrations follow the v4 pattern.
- **Elephants**:
  - The function is not actually retirable because some test path depends on its rebuild side effects. Mitigation pre-impl: grep all call sites; verify each accepts no-op return without expectation of mutation.
- **Paper tigers**:
  - "Just delete the function." Reality: existing call sites would break; no-op preserves call-site structure with zero work.

## Reversibility

- **Code excision**: reversible via `git revert`. Branch saved as `excise-premortem-2026-05-07`.
- **Schema migration**: irreversible without backup-restore. Pre-migration backup at `<dbpath>.pre-v4-<RFC3339>` provides a recovery path. Migration writes nothing destructive that isn't backed by either an audit row in `bead_journey` (for type conversions) or the file backup (for column drops).
- **Backup retention**: backup files accumulate (one per v3→v4 transition; idempotent re-runs do not generate additional backups). Recommend in upgrade runbook: `rm <dbpath>.pre-v4-*` after verifying upstream behavior. Not deleted automatically.

## Pre-implementation audit checklist

These quick greps must run clean (or surface known hits) before commit 1:

```bash
# oro-dash binary should not query gate_state / premortem_cycle_count
grep -rn 'gate_state\|premortem_cycle_count\|StagePremortem' cmd/oro-dash/ || echo "clean"

# Other SQL string emitters
grep -rn "gate_state\|premortem_cycle_count" --include='*.go' . | grep -v 'pkg/beadstore\|migrate_v3\|migrate_v4\|docs/'

# External writers of actor='premortem' (assumption #3)
grep -rn "actor.*['\"]\?premortem['\"]\?" --include='*.go' --include='*.sql' . | grep -v 'pkg/beadstore/v3types.go\|migrate_v4'
```

Any non-empty output (other than the documented exclusions) must be addressed in the affected-surface list.

## Load-bearing assumptions (verified in adversarial review v1)

1. **Production callers of removed Store methods are confined to the surfaces enumerated in Affected surface (cited).** Specifically the chain to be cut:
   - `dispatcher.go:notifyReplanChildClosed` → `sweep.go:OnReplanChildrenClosed` → `store.GateState` + `store.SetGateState` + `counter.SetPremortCycleCount`.
   - `router.go:applyPremortemVerdict` + `ApplyPremortemVerdict` + `ClosePremortemBeadWithStore` → `store.SetGateState` + `store.IncrPremortCycleCount` + `store.SetPremortemVerdict`.
   - `cmd/oro/cmd_bead.go:premortem-close subcommand` → `router.ClosePremortemBeadWithStore`.
   - All three chains are removed in this spec; the build-graph grep gate (test #14) enforces no orphan callers.
2. **Zero rows where `type='premortem'`** in the live DB at migration time. Migration step 2 is the failsafe for any future-discovered legacy data.
3. **No external (out-of-tree) consumer reads `bead_journey` filtering on `actor='premortem'`.** Internal verification: zero in-repo writers of `actor='premortem'` (premortem agent's events used `actor='dispatcher'` per router.go:101).
4. **`v3ViewsDDL` uses `SELECT b.*` which dynamically resolves at view-creation time.** The migration drops views before the rebuild and recreates them after, so the post-v4 column set propagates cleanly. **Order is critical**: any future migration that recreates views before rebuild would silently include dropped columns and break — `migrate_v4` enforces drop-first / recreate-last ordering.
5. **SKILL-level premortem references** (README, CLAUDE.md, skill SKILL.md files) describe a planning *practice*, not bead-type machinery. Verified: each reference reads as Tiger/Elephant/Paper-Tiger framework or "premortem each decision," none reference `type=premortem` beads or gate states.
6. **`EnsureBeadTypeCheckConstraint` retirement is safe.** The single caller, `MigrateBeadSchema` at `schema.go:498-510`, captures the bool into `rebuiltTypeConstraint` and ORs it with `rebuiltStatusConstraint` to gate `INSERT INTO beads_fts(beads_fts) VALUES('rebuild')` (line 506). Replacing the function body with `return false, nil` causes that branch of the OR to be permanently false, which is **safe** because: (a) the status-rebuild branch retains its own FTS-rebuild trigger via `rebuiltStatusConstraint`, and (b) `migrate_v4` itself includes an explicit FTS rebuild (Migration Design step 5) that covers the v3→v4 transition. After v4 lands, no further type-CHECK changes are anticipated, so the false-return path is correct in steady state. **Note**: assumption #6 in v0 of this doc was factually wrong (claimed callers ignore the bool) — corrected here.

## Status checkpoints

- [x] Stage 1 — Brainstorm (this doc, v3 post-adversarial-review)
- [x] Stage 2 — Consultation (six forcing questions; ledger drained)
- [x] Stage 3 — Adversarial review v0/v1/v2 (Ralph Loop converging)
- [ ] Stage 3 — Adversarial review v3 (re-run after v3 fixes)
- [ ] Stage 4 — Beadcraft decompose
