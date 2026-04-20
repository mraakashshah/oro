# oro shared dolt — lifecycle coordination

**Date:** 2026-04-20
**Status:** Design
**Owner:** as21
**Reproduction:**
- *This session*: 3 oro projects running simultaneously; a fall-through `startSharedDoltServer` call from one instance overrode the dolt on :13307 with a different `--data-dir`, making `beads_oro` invisible to this project (`bd ready` → `database "beads_oro" not found`).
- *aipkm*: `oro stop --force` killed the shared dolt; bd refused to auto-restart due to `auto-start: false`. Manual `bd dolt start` recovered.
- *scriptwriter*: shared dolt restarted on a random port (:61341) pointing at `.beads/dolt/`; bd silently fell back to `embeddeddolt/scriptwriter/` snapshot. 30 V2 beads disappeared.

---

## Problem

Lifecycle ownership of the machine-wide dolt server is **undefined**. Three actors believe they may spawn it:

1. **launchd** (`oro dolt setup` installs the agent) — the intended owner.
2. **`oro start`** (`ensureSharedDoltRunning` falls back to `startSharedDoltServer` if launchd kickstart fails or returns no listener within 3s).
3. **bd** (`bd dolt start`, manual operator commands) — bd's `auto-start: false` opts out, but humans still type the command.

When more than one actor spawns dolt, the second-comer wins the port (or grabs an ephemeral port and writes its PID file over the first's). The losing project's database becomes invisible. bd's silent fallback to `.beads/embeddeddolt/<db>/` masks the failure until a `bd ready` returns stale data.

A second class of failure: `oro stop` (intended for per-project lifecycle) currently terminates the shared dolt under `--force`. This violates the documented contract that "the shared server persists across sessions."

A third class: bd 1.0.2+ deprecates `dolt_server_port` in `metadata.json` (warning text: "can cause cross-project data leakage"). The canonical source is `.beads/dolt-server.port`. Oro still writes the deprecated field on every `oro init`/`oro dolt setup`.

## Goals

- **Single owner**: launchd is the only actor that spawns the shared dolt. Other actors adopt or fail loudly.
- **Verified adoption**: every adoption is preceded by a process probe + SQL probe that proves the running dolt is serving the expected `--data-dir` and contains the expected database.
- **Repair-only intervention**: a single `oro dolt repair` command is the only legitimate way to kill + relaunch a misconfigured dolt. It holds a flock so two oros cannot race.
- **Stop respects shared lifetime**: `oro stop` (any flags) never touches the shared dolt.
- **Port file is canonical**: `.beads/dolt-server.port` is the only place oro writes the port. `metadata.json:dolt_server_port` is removed during `oro start` if present (one-way migration).
- **Detect-at-start**: every `oro start` runs the probe. If verification fails, the dispatcher does not launch — user sees the exact mismatch and the suggested repair command.

## Non-Goals

- Modifying bd. bd ships from Homebrew; oro must work with stock `bd 1.0.0+` (and degrade gracefully on bd 1.0.2+ which emits the deprecation warning).
- Linux/Docker support. macOS launchd only for first ship; design hooks for a portable supervisor are noted.
- Multi-host dolt clustering. Single-machine only.
- Eliminating bd's `embeddeddolt` fallback. That's bd's behavior.

---

## Decisions

### D1 — Single-owner architecture: launchd-only

`oro start` (and any path that previously spawned dolt) must not call `startSharedDoltServer` directly. The new flow:

1. Resolve `dolt_server_port` from `.beads/dolt-server.port` (sole source).
2. If port is not the shared port (13307), use existing per-project lifecycle (unchanged).
3. If port IS the shared port:
   - Run **identity probe** (D2) on the running dolt.
   - On success → adopt.
   - On failure → exit non-zero with `oro dolt repair` hint and the specific mismatch.
4. If port is shared and **nothing is listening** (no dolt up):
   - Try `launchctl kickstart -k gui/<uid>/dev.getoro.dolt` once; wait 3s.
   - On success → re-run identity probe.
   - On failure → exit non-zero with `oro dolt setup` hint.

**Test/CI escape hatch:** `ORO_DOLT_DIRECT_SPAWN=1` env var allows the legacy direct-spawn path. Documented as test-only; emits a `WARN: direct spawn — not for production` line on every use. Default unset.

**Risks accepted:**
- *Tigers:* onboarding pain — first `oro start` after install requires `oro dolt setup` to have run. Mitigation: `oro setup` (existing) wires `oro dolt setup` into its prereq sequence.
- *Elephants:* if launchctl kickstart silently no-ops (e.g., agent disabled by user), the failure surface shifts to "kickstart succeeded, port still down." Mitigation: post-kickstart wait + identity-probe retry, then exit hint.

### D2 — Identity probe: process arg + SQL probe (defense in depth)

On every adoption attempt:

**Step 1 — process probe (~5ms):**
- Read PID from `~/.oro/dolt-server.pid` if present.
- If absent, discover via `lsof -i :13307 -sTCP:LISTEN -t` (single PID expected).
- Read process args via `ps -p <pid> -o args=` — no privileges needed on macOS.
- Parse for `--data-dir <path>`. Compare to `~/.oro/dolt`.
- Mismatch → identity probe fails with `process_data_dir_mismatch` and the observed path.

**Step 2 — SQL probe (~50ms):**
- Run `dolt sql -h 127.0.0.1 -P 13307 -q "SHOW DATABASES;" --result-format=tabular` (or equivalent via Go MySQL driver).
- Assert `dolt_database` from `.beads/metadata.json` is in the result.
- Missing → fails with `database_not_found` and the list of databases actually present.

**Step 3 — write `.server-identity` cookie:**
- On successful probe, oro writes `~/.oro/dolt/.server-identity.json` (PID, start_time, data_dir, observed_at).
- Subsequent probes within 60s can short-circuit on cookie freshness (PID still alive + cookie age <60s) → ~1ms total.

**Risks accepted:**
- *Tigers:* `lsof` may be missing on minimal macOS images. Mitigation: PID file is primary; lsof is fallback only. If both missing, probe fails closed with `cannot identify dolt owner` and repair hint.
- *Tigers:* SQL probe needs `dolt` CLI in PATH or a Go MySQL driver. Decision: use Go MySQL driver (`github.com/go-sql-driver/mysql` already used elsewhere) — no new dependency, no PATH coupling.
- *Elephants:* race between cookie write and a launchd restart. Mitigation: cookie includes `start_time` (parse from `ps -o lstart=`); mismatched start_time invalidates cookie.

### D3 — `oro dolt repair` subcommand (single repair pathway)

New Cobra command. Behavior:

1. Acquire flock on `~/.oro/dolt/.spawn.lock` (timeout 30s; on timeout assume another oro is repairing and re-probe).
2. Run identity probe on current :13307 listener.
3. If probe **passes** → no repair needed; print state + exit 0.
4. If probe **fails** with `process_data_dir_mismatch`:
   - Verify the rogue PID's `--data-dir` is genuinely wrong (not just a path normalization quirk).
   - SIGTERM the rogue. Wait up to 5s. SIGKILL if still alive.
   - Re-run `launchctl kickstart -k gui/<uid>/dev.getoro.dolt`.
   - Wait up to 10s for :13307. Re-run identity probe.
   - On success → exit 0 with `repaired` line.
   - On failure → exit 3 with full mismatch trace.
5. If probe **fails** with `database_not_found` → do NOT kill (data dir is right, db is missing — that's an operator problem; suggest `bd doctor`). Exit 4.
6. If port is **down** → kickstart, wait, probe. Same as D1 cold-start path.

Exit codes: 0 ok, 2 unidentified rogue (lsof/PID both unavailable), 3 repair attempted but verification still fails, 4 data-dir-correct but database-missing.

**Flock semantics:** `syscall.Flock` (LOCK_EX | LOCK_NB). On `EWOULDBLOCK`, wait 100ms and retry up to 30s. After timeout, downgrade to read-only probe (don't repair) and exit 5 with `another oro is repairing` hint.

**Risks accepted:**
- *Tigers:* SIGTERM of a foreign dolt that another user (different UID) owns. Mitigation: `repair` exits with permission-denied if the rogue PID is not owned by current UID. Cross-user repair is out of scope.
- *Elephants:* SIGTERM of a dolt mid-write loses uncommitted journal data. Acceptable: dolt's journal is recoverable on next launch; the alternative (silently using the wrong dolt) loses *all* writes anyway.

### D4 — Migrate `dolt_server_port` out of `metadata.json`

`metadata.json` becomes the source of truth for `dolt_database`, `dolt_mode`, `backend`, `database`. The `dolt_server_port` field is removed.

`.beads/dolt-server.port` is the canonical port file (already exists; bd 1.0.2+ explicitly prefers it).

Migration runs at every `oro start`:
1. If `.beads/metadata.json` contains `dolt_server_port`:
   - If `.beads/dolt-server.port` is missing → write it from the metadata value, then strip the field.
   - If both exist and agree → strip the field silently.
   - If both exist and disagree → port file wins; strip metadata; emit `WARN: metadata.json had stale dolt_server_port=N, port file has M; port file kept`.
2. Use `writeFileAtomic` from cmd_dolt.go (oro-kn25 just landed) for the metadata rewrite.

**Risks accepted:**
- *Tigers:* concurrent `oro start` from two projects could both try to migrate. Mitigation: each project has its own `.beads/` — no cross-project lock needed.
- *Paper tigers:* "what if user reverts bd to 1.0.0" — bd 1.0.0 ignores the field's absence (port file is already the new contract). No regression.

### D5 — Fix `oro stop` to never touch shared dolt

`oro stop` (with or without `--force`) must call `stopDoltServer(beadsDir)` only when the project's port ≠ `SharedDoltPort`.

Current `cmd_stop.go` (audit needed):
- If it currently invokes a dolt-stop path on shared-port projects, gate that with `if !isSharedServer(port)`.
- Add a regression test: `TestOroStopPreservesSharedDolt`.

`--force` only affects dispatcher shutdown urgency, never widens the dolt termination scope.

### D6 — Remove direct-spawn from start path

Delete (or gate behind `ORO_DOLT_DIRECT_SPAWN`) the fall-back to `startSharedDoltServer` inside `ensureSharedDoltRunning` (`cmd/oro/dolt.go:443-444`). Replace with:

```go
return 0, fmt.Errorf("shared dolt server unavailable on port %d; run 'oro dolt repair' or 'oro dolt setup'", SharedDoltPort)
```

`startSharedDoltServer` itself stays — used by `oro dolt setup` (the legitimate one-time bring-up path) and the repair command. The change is only in the start path.

---

## Architecture

```
┌──────────────────────────────────────────────────────────┐
│ Owner (singleton): launchd dev.getoro.dolt              │
│   plist: --data-dir ~/.oro/dolt -P 13307                │
│   PID file: ~/.oro/dolt-server.pid                      │
│   Identity cookie: ~/.oro/dolt/.server-identity.json    │
└──────────────────────────────────────────────────────────┘
                        │
            ┌───────────┴────────────┐
            │                        │
   ┌────────▼────────┐      ┌────────▼────────┐
   │ oro start (A)   │      │ oro start (B)   │
   │ project A       │      │ project B       │
   │                 │      │                 │
   │ probe → adopt   │      │ probe → adopt   │
   │ no spawn        │      │ no spawn        │
   └────────┬────────┘      └────────┬────────┘
            │                        │
            └──── never spawn ───────┘
                        │
                        ▼
            ┌──────────────────────────┐
            │ oro dolt repair          │
            │  (only path that kills)  │
            │  flock: ~/.oro/dolt/     │
            │         .spawn.lock      │
            └──────────────────────────┘
```

## Components

| File | Change |
|------|--------|
| `cmd/oro/dolt.go` | `ensureSharedDoltRunning`: replace `startSharedDoltServer` fallback with error return (D6). Add `runIdentityProbe(oroHome, dbName) (probeResult, error)`. Add `writeServerIdentity` / `readServerIdentity` for cookie. |
| `cmd/oro/cmd_dolt_repair.go` | NEW. `oro dolt repair` Cobra subcommand (D3). Flock helper. Exit codes 0/2/3/4/5. |
| `cmd/oro/cmd_start.go` | `makeDoltLifecycle`: probe before adopt (D1). `runDaemonOnly`: gate ORO_DOLT_DIRECT_SPAWN escape hatch. |
| `cmd/oro/cmd_dolt.go` | `newDoltCmd()`: register `repair` subcommand. |
| `cmd/oro/cmd_init.go` | Stop writing `dolt_server_port` to `metadata.json` (D4). |
| `cmd/oro/dolt_meta.go` | NEW (or extend existing meta.go). `MigrateMetadataPort(beadsDir)` runs at start (D4). |
| `cmd/oro/cmd_stop.go` | Gate dolt-stop calls behind `!isSharedServer(port)` (D5). |
| `cmd/oro/launchd.go` | No code change — plist already correct. Test reaffirms `--data-dir <homeDir>/.oro/dolt`. |

New test files:
- `cmd/oro/cmd_dolt_repair_test.go`
- `cmd/oro/identity_probe_test.go`
- `cmd/oro/migrate_metadata_port_test.go`
- `cmd/oro/cmd_stop_shared_dolt_test.go`

---

## Data flows

### Cold start (no dolt running)

```
oro start
  → makeDoltLifecycle → port=13307 (shared)
  → ensureSharedDoltRunning
    → isPortUp(13307)? NO
    → tryLaunchctlKickstart() → wait 3s → port up?
      → YES: runIdentityProbe → adopt
      → NO:  return ERR "run oro dolt setup"
```

### Warm start (correct dolt running)

```
oro start
  → ensureSharedDoltRunning
  → port up → runIdentityProbe
    → process probe: --data-dir == ~/.oro/dolt? YES
    → SQL probe: SHOW DATABASES contains beads_oro? YES
    → write/refresh .server-identity.json
    → return 0 (adopt)
```

### Warm start (rogue dolt running — today's failure)

```
oro start
  → ensureSharedDoltRunning
  → port up → runIdentityProbe
    → process probe: --data-dir == ~/.oro/dolt? NO (observed: .beads/dolt/)
    → return ERR process_data_dir_mismatch
  → exit non-zero with: "shared dolt is serving wrong data dir
                          (observed: .beads/dolt/, expected: ~/.oro/dolt/);
                          run 'oro dolt repair' to recover"
```

### Repair flow

```
oro dolt repair
  → flock(~/.oro/dolt/.spawn.lock, 30s timeout)
  → identityProbe → fail (process_data_dir_mismatch, rogue PID 12345)
  → uid_owns_pid(12345)? YES
  → SIGTERM 12345; wait 5s; alive? SIGKILL
  → tryLaunchctlKickstart; wait 10s
  → identityProbe → pass
  → write .server-identity.json
  → release flock; exit 0 "repaired"
```

### Migration flow

```
oro start (every invocation)
  → MigrateMetadataPort(beadsDir)
    → metadata.dolt_server_port present?
      → YES: read .beads/dolt-server.port; resolve conflicts (D4); strip field
      → NO:  no-op
  → continue with start
```

---

## Error handling

| Failure | Exit | User-facing message | Suggested action |
|---------|------|---------------------|------------------|
| Port up, process probe `--data-dir` mismatch | start: 1; repair: 3 (after attempt) | `shared dolt is serving wrong data dir (observed: X, expected: Y)` | `oro dolt repair` |
| Port up, SQL probe `database_not_found` | start: 1; repair: 4 | `dolt server is correct but database 'beads_oro' missing` | `bd doctor` (likely needs project re-init or migration) |
| Port up, can't identify owner (no PID file, no lsof) | start: 1; repair: 2 | `cannot identify dolt server owner on port 13307` | `oro dolt repair` (or manual investigation) |
| Port down, launchctl kickstart fails | start: 1 | `shared dolt is not running and launchctl kickstart failed` | `oro dolt setup` |
| Repair flock contended | repair: 5 | `another oro process is repairing dolt; re-probe in 30s` | wait or retry |
| Direct-spawn requested without env | start: 1 | (kept for legacy paths only) | unset `ORO_DOLT_DIRECT_SPAWN` if intentional |

All error messages include the offending paths/PIDs/observed values, never just "failed."

---

## Testing strategy

### Unit tests

- `TestRunIdentityProbe_*`: matrix of (process probe pass/fail) × (SQL probe pass/fail), with PID-file present/absent and lsof present/absent.
- `TestMigrateMetadataPort_*`: matrix of (metadata field present/absent) × (port file present/absent) × (values agree/disagree).
- `TestRunDoltRepair_*`: matrix of probe outcomes and rogue-PID UID match.
- `TestEnsureSharedDoltRunning_NoDirectSpawn`: assert direct spawn path is unreachable without env var.
- `TestOroStopPreservesSharedDolt`: with shared port, `oro stop --force` does not call any dolt-stop function.

### Integration test — synthetic rogue

`scripts/test-dolt-coordination.sh`:
1. Set up two synthetic projects (A and B) using existing oro-tjq1 fixture machinery (oro-p5el).
2. Start dolt with the *wrong* `--data-dir` on :13307 (simulates today's rogue).
3. Run `oro start` from project A → expect non-zero exit with `process_data_dir_mismatch`.
4. Run `oro dolt repair` → expect exit 0 + relaunched with correct data dir.
5. Re-run `oro start` from project A → expect adopt success.
6. Run `oro start` from project B simultaneously → expect both succeed (no double repair).

### Real-data gate

Run `oro dolt repair --dry-run` against this machine's current state (after manual reset). Capture output in `docs/plans/2026-04-20-dolt-coordination-real-gate.md`. Surface any cases the design missed.

---

## Out of scope (follow-ups)

- Linux/Docker portable supervisor (replace launchd with a foreground supervised dolt subprocess + exclusive lock).
- bd integration: propose to bd that `database not found` errors include the actual `--data-dir` of the running server, not just the database name.
- `oro dolt diagnose` (already in oro-tjq1) — no overlap; that command surveys per-project metadata health, this command operates on the shared server lifecycle.
- Cross-user dolt repair (different UID owns rogue PID).
- Identity cookie format upgrade (currently JSON; future signed cookie).

---

## Adversarial review checklist (for Stage 2 subagent)

1. Does D1 actually eliminate the random-port-dolt class? Audit every code path that calls `dolt sql-server`.
2. D2 process probe: confirm `ps -p <pid> -o args=` works without privileges on macOS 14+. Test on a foreign-UID PID.
3. D3 flock: confirm `syscall.Flock` semantics on macOS APFS. Race test: two concurrent repairs.
4. D4 migration: what if `oro start` runs on a brand-new project with no metadata? Verify migration is no-op.
5. D5: audit `cmd_stop.go` — does anything call dolt-stop unconditionally?
6. D6: confirm `startSharedDoltServer` is only reachable from `oro dolt setup` and `oro dolt repair` after the change. Grep all call sites.
7. Identity cookie: what happens on launchd restart (start_time changes)? Verify cookie invalidates.
8. SQL probe via Go MySQL driver: confirm we're not adding a new dependency that changes the binary surface.
9. Error message audit: every failure mode in the table above must have a corresponding test asserting the exact message text.
10. Onboarding: trace `oro setup` (existing, not `oro dolt setup`) end-to-end. Does it now require `oro dolt setup` to have run? If not, first `oro start` will fail.
