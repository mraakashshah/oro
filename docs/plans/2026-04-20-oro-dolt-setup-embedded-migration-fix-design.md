# oro dolt setup — embedded-mode migration fix

**Date:** 2026-04-20
**Status:** Design
**Owner:** as21
**Reproduction:** scriptwriter session, 2026-04-19. `oro dolt setup` reported success migrating 8 projects but scriptwriter's shared DB was empty; recovery required manual `dolt dump` from `.beads/embeddeddolt/scriptwriter/` and metadata patch.

---

## Problem

`oro dolt setup` silently produces empty shared databases for projects that were originally created in embedded bd mode. Three independent layers conspire:

1. **Wrong source path.** `cmd/oro/cmd_dolt.go:198` builds `srcDir := filepath.Join(p.beadsDir, "dolt", p.dbName)`. For embedded-mode projects, the live data lives at `.beads/embeddeddolt/<dolt_database>/`. The path the code reads is empty (or absent).
2. **Silent skip on missing source.** `atomicCopyDir` (`cmd_dolt.go:341-369`) returns `nil` when source doesn't exist. Setup reports success.
3. **Metadata not flipped.** `setDoltPort` writes `dolt_server_port` only. Neither `dolt_mode: "server"` (in `metadata.json`) nor a per-project `.beads/dolt-server.port` file is written by the migration path. `startSharedDoltServer` writes a *shared* `~/.oro/dolt-server.port`, not per-project. Even when migration *does* succeed, individual projects can still appear unconfigured to bd.

A test (`TestAtomicCopyDir_SrcNotExists` at `cmd_dolt_test.go:1087`) cements layer 2 as expected behavior — it asserts `atomicCopyDir` returns `nil` when source is absent.

**bd-mode confirmation (added 2026-04-20):** The shipped Homebrew bd 1.0.0 binary contains the `embeddeddolt` mode compiled in (verifiable via `strings $(which bd) | grep embeddeddolt` — yields `embeddeddolt: begin tx`, `embeddeddolt: init schema`, `.beads/embeddeddolt/`). bd selects mode at runtime based on `metadata.json`. Stale upstream archive at `archive/yap/reference/beads-upstream/` shows a build-tagged variant; the shipped binary does not behave that way.

## Goals

- Migrate embedded-mode projects correctly when `oro dolt setup` runs.
- Detect already-broken state on a registered machine (your other 7 projects may also be silently empty).
- Block `oro start` from launching against a known-broken project.
- Lock the canonical name resolution: `dolt_database` field in `metadata.json` is the source of truth for both embedded and shared paths.

## Non-Goals

- Modifying bd. bd ships from Homebrew; oro must work with stock `bd 1.0.0+`.
- Retiring `.beads/dolt-server.port`. Permanent dual-write — bd reads from it, oro writes to it.
- Auto-cleaning stale `.beads/dolt/<dbName>/` artifacts beyond the migration boundary. Optional cleanup is in scope; aggressive housekeeping is not.

## Decisions

### D1 — Detection: mode hint + probe verify

Read `dolt_mode` from `metadata.json` to pick the *expected* source path:

| `dolt_mode` value | Expected source                                  |
|-------------------|--------------------------------------------------|
| `"embedded"`      | `.beads/embeddeddolt/<dolt_database>/`           |
| missing/legacy    | same as `embedded` (legacy projects predate the field) |
| `"server"`        | already shared — skip migration, validate only   |

**Verify:** before migrating, confirm the expected path contains `.dolt/noms/` with at least one chunk file. If expected path is empty *and* the alternative path has data, fail loudly with a "metadata mismatch" error pointing at the discrepancy. Never silently fall back.

**Alternatives considered:**
- A (read mode only): trusts metadata that may be stale.
- B (probe both, ignore mode): defensive but requires fully resolving ambiguity at every call.
- **C (mode + probe)**: chosen. Fast happy path, loud failure on drift.

**Risks accepted:**
- *Tigers:* mode-mismatch error becomes the primary new error class operators will see; remediation message must be sharp.
- *Elephants:* legacy projects without `dolt_mode` are treated as embedded — correct for every project bd has ever created, but encoded as an assumption.

### D2 — Verification: defense in depth

After migration, verify the destination DB before declaring success:

1. **Stat probe (gate):** `~/.oro/dolt/<dolt_database>/.dolt/noms/` exists and contains at least one non-zero file.
2. **SQL probe (truth):** `SELECT COUNT(*) FROM issues` against source and destination must return the same number. Connect via dolt SQL on each side.

If either probe fails, the migration is reverted (destination directory removed) and metadata is *not* flipped. User sees the failure with both row counts in the error message.

**Cost:** ~hundreds of ms per project for the SQL connect. Acceptable.

**Lock semantics:** Embedded dolt is single-writer (the actual lock is `noms/LOCK` + an `embeddeddolt` advisory flock taken at process start — there is no `sql-server.lock` in embedded mode). Rather than probing the lock file (path is internal and may change), rely on:

1. The existing dispatcher-running guard (covers the common case — workers don't run when setup runs).
2. Dolt's own error surface: when `dolt sql` runs against a locked directory, the embedded mode emits "another process holds the exclusive lock on %s" (verified in bd 1.0.0 binary strings). The probe must detect this error string and surface a typed `ErrEmbeddedLockHeld` with remediation: "stop active bd processes and re-run."

This is intentionally less defensive than a lock-file probe but more robust to bd internal layout changes.

**PATH dependency:** SQL probe requires the `dolt` binary in PATH (already required by `startSharedDoltServer`). If absent, MIGRATION fails hard with "install dolt CLI." START-time validation (D4) degrades to stat-only with a warning — the stricter SQL probe is migration-only.

**Risks accepted:**
- *Tigers:* schema coupling — `issues` table is bd's, not oro's. If bd renames the table, this code breaks. Mitigation: integration test (deferred per D5) catches format drift.
- *Tigers:* lock-acquisition failure mode is a new error class for users with active bd sessions. Remediation message must be clear.
- *Paper tiger:* "dolt rewrites chunks during compaction so byte size diverges" — handled because we use row count, not bytes.

### D3 — Metadata atomicity: per-file atomic, no cross-file transaction

Both writes use write-temp-then-rename:

1. Write updated `.beads/metadata.json` to `.beads/metadata.json.tmp`, fsync, rename.
2. Write `.beads/dolt-server.port` to `.beads/dolt-server.port.tmp`, fsync, rename.

**Order matters:** metadata.json first, port file second. If step 2 fails, bd will error on next invocation looking for the port file — visible failure rather than silent drift.

No two-phase commit, no rollback. Cross-file inconsistency is possible only if the process is killed between the two renames; the doctor/heal logic (D4) detects and corrects.

**Port-file 4-state enumeration** — `HealProjects` must distinguish:

| State | per-project `.beads/dolt-server.port` | metadata `dolt_mode` | Per-project pid alive? | Action |
|-------|---------------------------------------|----------------------|-----------------------|--------|
| (a)   | present + content matches metadata port | `server`           | n/a                   | healthy — no-op |
| (b)   | present + content mismatches metadata   | `server`           | n/a                   | rewrite port file (file is stale from old per-project server mode) |
| (c)   | absent                                  | `server`           | n/a                   | the migration's missing write — write port file |
| (d)   | present + per-project `.beads/dolt-server.pid` exists AND process is alive | any | yes | **REFUSE TO TOUCH** — a legacy per-project dolt server is actively running. Hard fail with "stop legacy server first." |

State (d) is the abort condition that protects users mid-migration from a legacy per-project server. Must be checked before any write.

**Alternatives considered:**
- D (single source of truth in metadata, retire port file): rejected — requires bd PR + brew release, never going to happen for downstream users.

### D4 — Heal at setup, detect at start

Detection-and-heal logic lives in **one shared function** consumed by two callers:

| Caller            | Behavior on detected drift                          |
|-------------------|------------------------------------------------------|
| `oro dolt setup`  | Re-validate every project, re-migrate any broken one. Setup is idempotent self-heal. |
| `oro start`       | Hard fail before launching dispatcher. If TTY: prompt `Heal now? [y/N]`. If non-TTY: refuse and exit non-zero with remediation pointer. |

**Why one function:** if heal logic in setup and detection logic at start drift apart, the system gets stuck in "start says broken, setup says fine" loops.

**Risks accepted:**
- *Tigers:* `oro start` becomes slower (validation cost on every launch). Mitigated: validation is `~/.oro/dolt/` stat + SQL count per project — small constant work.
- *Tigers:* TTY heuristic could be wrong in tmux sessions where stdin is connected but the user doesn't expect a prompt. Mitigation: prompt is a single y/N with explicit default N.

### D5 — Test fixture: synthetic `.beads/` directory

Hand-roll fixtures in `cmd/oro/testdata/`:
- A directory tree mimicking embedded-mode bd: `metadata.json` with `dolt_mode: "embedded"`, `.beads/embeddeddolt/<dbName>/.dolt/noms/` with a tiny pre-built dolt DB committed as testdata.
- A second fixture for already-shared mode.
- A third for the broken state (empty `.beads/dolt/<dbName>/`, full `.beads/embeddeddolt/<dbName>/`).

Unit tests run hermetically against these fixtures.

**Risks accepted:**
- *Elephant:* fixture rot if bd's on-disk format changes. Mitigation deferred — an integration test that shells out to real `bd init` is filed as a follow-up bead, gated behind a build tag, run pre-release rather than per-commit.

### D6 — Project-name resolution: `dolt_database` field is canonical

Confirmed from oro's own metadata: `dolt_database: "beads_oro"` matches both `.beads/embeddeddolt/beads_oro/` and `.beads/dolt/beads_oro/`.

- The `dolt_database` field in `.beads/metadata.json` names every per-project path.
- Missing `dolt_database` → fall back to `"beads"` (current behavior) with a warning logged.
- Project basename is **not** consulted.

### D8 — Pre-flight diagnostic (separate subcommand)

Pin to `oro dolt diagnose` (new subcommand), NOT `--dry-run` on setup. Reasons: setup's flag surface stays focused on the action; diagnose can be safely re-run without needing to know setup's flags; exit codes are diagnostic-specific.

**CLI:** `oro dolt diagnose [--project <path>]` — without `--project`, scans all `~/.oro/projects/*/project.root` registered projects.

**Output:** the table from §Pre-flight Diagnostic (one row per project) printed as both human-readable text and `--json` for scripting.

**Exit codes:**

| Code | Meaning                                                                              |
|------|---------------------------------------------------------------------------------------|
| 0    | All registered projects healthy. No action needed.                                    |
| 1    | Drift found, heal feasible (data exists in `.beads/embeddeddolt/<db>/` for every broken project). Run `oro dolt setup` to fix. |
| 2    | Drift found, **NOT** all recoverable from `embeddeddolt`. At least one project has zero issues across all 4 candidate paths — data loss. Spec must be revised before heal logic ships. |
| 3    | Lock conflict — at least one project's source DB is held by an active bd process. Stop bd and re-run. |
| 4    | Dispatcher running. Stop dispatcher and re-run.                                       |

**Concurrency:** respects the dispatcher-running guard. If the dispatcher is up, exits 4. Issue counts are obtained via `dolt sql -q "SELECT COUNT(*) FROM issues" --data-dir <path>` — SELECT-only statements don't write regardless of flags, so no `--readonly` is needed.

**Spec-revision threshold:** if exit code 2 fires for any project, this design doc must be revised — the heal pathway as designed cannot recover that project. Threshold is exact: every project must be heal-feasible from `.beads/embeddeddolt/<db>/`.

### D7 — Failure UX

Two messages standardized across setup and start:

```
ERROR: project "<dolt_database>" has empty shared DB
  expected source: .beads/embeddeddolt/<dolt_database>/  (X issues)
  shared dest:     ~/.oro/dolt/<dolt_database>/          (0 issues)
  fix: run `oro dolt setup` (idempotent — safe to re-run)
```

```
ERROR: project "<dolt_database>" metadata mismatch
  metadata.json says: dolt_mode=embedded
  but data found at:  ~/.oro/dolt/<dolt_database>/  (X issues)
  expected at:        .beads/embeddeddolt/<dolt_database>/  (empty)
  fix: ...
```

## Architecture

### New / changed code

| File                              | Change                                                                                  |
|-----------------------------------|------------------------------------------------------------------------------------------|
| `cmd/oro/cmd_dolt.go`             | Replace `srcDir` line; add `resolveSourcePath`, `verifyMigration`, `flipMetadataAtomic`, `writeFileAtomic` (co-located in `cmd_dolt.go`, not a new file — only callers are migration helpers). |
| `cmd/oro/cmd_dolt.go`             | New exported `HealProjects(oroHome string, beadsDirs []string, w io.Writer, opts HealOptions) error` consumed by setup's Cobra `RunE` and by `cmd_start.go`. `HealOptions{Interactive bool, DryRun bool}` controls TTY-prompt vs hard-fail vs report-only. |
| `cmd/oro/cmd_dolt.go`             | New `DiagnoseProjects(oroHome string, beadsDirs []string, w io.Writer) (DiagnosticReport, error)` powering `oro dolt diagnose`. |
| `cmd/oro/cmd_start.go`            | Call `HealProjects` from BOTH `startFreshSwarm` (with `HealOptions{Interactive: isatty(stdin)}`) AND `runDaemonOnly` (with `HealOptions{Interactive: false}` — hard-fail mode, no prompt). The daemon must still detect drift and refuse to launch; only the prompting differs. Wire point in startFreshSwarm: between `makeDoltLifecycle` and `runFullStart` (`cmd_start.go:444-469`). |
| `cmd/oro/dolt.go`                 | Extend `doltMeta` struct (`dolt.go:36`) with `DoltMode string \`json:"dolt_mode,omitempty"\`` so D1's mode-hint detection can read it.   |
| `cmd/oro/cmd_dolt_test.go`        | Update `TestAtomicCopyDir_SrcNotExists` (`cmd_dolt_test.go:1087`) — it currently asserts silent-nil-on-missing-source, which is the cemented bug. New assertion: `atomicCopyDir` returns a typed error when source missing AND the caller asked it to migrate (or callers wrap with explicit pre-check). Add new tests below. |
| `cmd/oro/testdata/`               | Three new fixture trees (embedded, shared, broken).                                      |

### Data flow (happy path, embedded → shared)

```
oro dolt setup
  └─ discoverBreadsDirs → list of .beads paths
       └─ for each:
            readMetadata
            resolveSourcePath(mode, dbName) → .beads/embeddeddolt/<dbName>/
            probeNoms → ok (chunks present)
            atomicCopyDir → ~/.oro/dolt/<dbName>/
            verifyMigration:
              probeNoms(dest)        → ok
              SQL count(src) == count(dest) → ok
            flipMetadataAtomic:
              write metadata.json {dolt_mode: "server", dolt_server_port: 13307}
              write .beads/dolt-server.port "13307\n"
            ok
```

### Error path (mismatch detected)

```
oro start
  └─ HealProjects (read-only check)
       └─ for each project:
            expected = resolveSourcePath
            if expected empty AND alternative non-empty:
              ERROR + remediation
       (any error)
  └─ if TTY: prompt "Heal now? [y/N]"
       if y: invoke setup heal path; on success, continue launch
  └─ if non-TTY or N: exit 1
```

## Testing Strategy

| Layer            | Coverage                                                                          |
|------------------|------------------------------------------------------------------------------------|
| Unit             | `resolveSourcePath` for all `dolt_mode` values + missing field.                    |
| Unit             | `verifyMigration` row-count match, mismatch, dest-empty, schema-missing.           |
| Unit             | `flipMetadataAtomic` survives partial failures (simulated rename failure mid-write). |
| Integration      | Full setup against synthetic embedded fixture: produces correct shared DB + flipped metadata. |
| Integration      | Full setup against pre-broken fixture (empty `.beads/dolt/`, full `.beads/embeddeddolt/`): heals correctly. |
| Integration      | `oro start` against broken project: hard-fails with correct message; with TTY+y: heals and proceeds. |
| Deferred         | Real-`bd init` integration test, build-tagged, run pre-release.                    |

## Pre-flight Diagnostic (must run BEFORE shipping heal logic)

Before writing the heal pathway, run `oro dolt setup --dry-run` (or a one-off script `scripts/dolt-diagnose.sh`) against the 8 currently-registered projects on this machine. Report per project:

| Project | metadata.dolt_mode | `.beads/embeddeddolt/<db>/` issues | `.beads/dolt/<db>/` issues | `~/.oro/dolt/<db>/` issues | Heal feasible? |
|---------|--------------------|------------------------------------|------------------------------|------------------------------|----------------|

If any project has zero issues in *all four* candidate locations, the heal cannot recover it — file a separate bead for that project and document the data loss. **If the diagnostic shows different recovery patterns across the 8 projects, this spec must be revised before decomposition.**

## Out of Scope

- Modifying bd or shimming for new bd versions.
- Cleaning up stale `.beads/dolt/<dbName>/` after migration (logged as warning; manual cleanup acceptable).
- Doctor as a separate subcommand — heal lives in setup, detection in start.
- Per-project port assignment — single shared port (13307) is the existing design.

## Acceptance Criteria

A single shell-runnable acceptance script `scripts/test-dolt-migration.sh` covers the end-to-end happy path:

1. Builds the synthetic broken fixture (empty `.beads/dolt/<db>/`, populated `.beads/embeddeddolt/<db>/` with N issues), confirms `dolt sql -q 'SELECT COUNT(*) FROM issues'` against the embedded path returns N.
2. Runs `oro dolt setup`, exits 0.
3. Asserts `~/.oro/dolt/<db>/.dolt/noms/` non-empty.
4. Asserts `dolt sql --use-db <db> -q 'SELECT COUNT(*) FROM issues'` against the shared server returns N.
5. Asserts `.beads/metadata.json` contains `"dolt_mode": "server"` and `"dolt_server_port": 13307`.
6. Asserts `.beads/dolt-server.port` exists and contains `13307\n`.

Additional criteria:

7. **Pre-flight diagnostic** (new, added per review): `oro dolt diagnose` (subcommand defined in §D8) reports per-registered-project: which mode metadata says, which paths actually have data, and what the heal action would be — without executing. Run this first against the user's 8 registered projects to confirm recoverability before shipping the heal.
8. Running `oro start` against a project flagged broken by the diagnostic hard-fails with the standardized error message and refuses to launch the dispatcher.
9. **Start-time validation budget**: `HealProjects` (in stat-only mode, the happy-path used by `oro start`) completes in under 500ms wall clock for 10 healthy projects, measured by `BenchmarkHealProjectsStatOnly`. The SQL probe (D2) runs only during MIGRATION/HEAL execution, not during start-time validation, so per-start cost is bounded by a directory stat + JSON parse per project.
10. `oro dolt setup` is idempotent — re-running on a healthy machine performs validation only and exits 0 with no metadata writes.
11. `TestAtomicCopyDir_SrcNotExists` is updated to assert the new error-on-missing-source behavior (or, if `atomicCopyDir`'s contract is preserved, callers are explicitly tested to pre-check existence).
