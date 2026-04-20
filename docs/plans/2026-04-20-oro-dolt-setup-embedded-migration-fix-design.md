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
3. **Metadata not flipped.** `setDoltPort` writes `dolt_server_port` only. Neither `dolt_mode: "server"` (in `metadata.json`) nor `.beads/dolt-server.port` (the file bd reads at startup) is written. Even when migration *does* succeed, bd still treats the project as embedded.

A test (`TestRunDoltSetup_SkipsEmptyProjectWithoutError`) cements layer 2 as expected behavior.

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

**Risks accepted:**
- *Tigers:* schema coupling — `issues` table is bd's, not oro's. If bd renames the table, this code breaks. Mitigation: integration test (deferred per D5) catches format drift.
- *Paper tiger:* "dolt rewrites chunks during compaction so byte size diverges" — handled because we use row count, not bytes.

### D3 — Metadata atomicity: per-file atomic, no cross-file transaction

Both writes use write-temp-then-rename:

1. Write updated `.beads/metadata.json` to `.beads/metadata.json.tmp`, fsync, rename.
2. Write `.beads/dolt-server.port` to `.beads/dolt-server.port.tmp`, fsync, rename.

**Order matters:** metadata.json first, port file second. If step 2 fails, bd will error on next invocation looking for the port file — visible failure rather than silent drift.

No two-phase commit, no rollback. Cross-file inconsistency is possible only if the process is killed between the two renames; the doctor/heal logic (D4) detects and corrects.

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
| `cmd/oro/cmd_dolt.go`             | Replace `srcDir` line; add `resolveSourcePath`, `verifyMigration`, `flipMetadataAtomic`. |
| `cmd/oro/cmd_dolt.go`             | New shared `validateAndHealProjects` consumed by setup and `oro start`.                  |
| `cmd/oro/cmd_start.go`            | Call `validateAndHealProjects` before dispatcher launch; TTY-prompt on drift.            |
| `cmd/oro/cmd_dolt_test.go`        | Drop `TestRunDoltSetup_SkipsEmptyProjectWithoutError` (false-positive). Add new tests.   |
| `cmd/oro/testdata/`               | Three new fixture trees (embedded, shared, broken).                                      |
| `cmd/oro/atomic.go` (new or co-located) | `writeFileAtomic(path, content)` helper — write-temp + rename.                     |

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
  └─ validateAndHealProjects (read-only check)
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

## Out of Scope

- Modifying bd or shimming for new bd versions.
- Cleaning up stale `.beads/dolt/<dbName>/` after migration (logged as warning; manual cleanup acceptable).
- Doctor as a separate subcommand — heal lives in setup, detection in start.
- Per-project port assignment — single shared port (13307) is the existing design.

## Acceptance Criteria

1. Running `oro dolt setup` against a fresh embedded-mode project migrates the data, with row-count match verified, and flips both `metadata.json` and `dolt-server.port`.
2. Running `oro dolt setup` against the scriptwriter-style broken state heals it: pre-existing empty shared DB is replaced with a correctly-populated one.
3. Running `oro start` against a project with a known-broken shared DB hard-fails with the standardized error message and refuses to launch the dispatcher.
4. Running `oro start` against a healthy project incurs no perceptible startup delay (validation is sub-second per project).
5. New unit + integration tests pass; the false-positive `TestRunDoltSetup_SkipsEmptyProjectWithoutError` is removed.
6. `oro dolt setup` is idempotent — re-running on a healthy machine is a no-op (validation only).
