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

Lifecycle ownership of the machine-wide dolt server is **undefined**. Four actors believe they may spawn it:

1. **launchd** (`oro dolt setup` installs the agent under label `dev.getoro.dolt`) — the intended owner.
2. **`oro start`** (`ensureSharedDoltRunning` falls back to `startSharedDoltServer` if launchd kickstart fails or returns no listener within 3s).
3. **`oro dolt start`** (`cmd_dolt.go:614`) — operator-invokable shared-server spawn.
4. **`oro init` / `initDoltForProject`** (`cmd_init.go:722`) — per-project `startDoltServer`; collides with shared port if `DerivePort` hashes to 13307.

Plus bd (`bd dolt start`, manual operator commands) — bd's `auto-start: false` opts out, but humans still type the command.

**Root cause of today's failure — label mismatch in kickstart path:** `tryLaunchctlKickstart` at `cmd/oro/dolt.go:457` hard-codes the service target `gui/<uid>/com.oro.dolt-server`, but the installed plist label (`cmd/oro/launchd.go:17`) is `dev.getoro.dolt`. `launchctl kickstart` against a non-existent label returns 0 (no-op), so `ensureSharedDoltRunning` silently falls through to `startSharedDoltServer` every single time. This has been happening since the label rename. When multiple oros race, each one fall-throughs into direct spawn. First-comer writes the PID file; second-comer's spawn produces an orphan on :13307 with its own `--data-dir` (whichever `startSharedDoltServer` derived — `~/.oro/dolt` for legitimate paths but it also catches older worktrees or CI paths).

When more than one actor spawns dolt, the second-comer wins the port (or grabs an ephemeral port and writes its PID file over the first's). The losing project's database becomes invisible. bd's silent fallback to `.beads/embeddeddolt/<db>/` masks the failure until a `bd ready` returns stale data.

A second class of failure: aipkm report claims `oro stop --force` killed the shared dolt. Current `cmd_stop.go` (line 357) explicitly comments "dolt server is intentionally NOT stopped here" and does not call `stopDoltServer`. So the aipkm reproduction has a **different root cause** (likely daemon SIGTERM cascade-killing a dolt reparented to the dispatcher process group, or tmux pane death killing a foreground dolt). D5 below must investigate before asserting a fix.

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

### D0 — Fix kickstart label mismatch (P0, ships first)

Pre-requisite to every other decision. `tryLaunchctlKickstart` (`cmd/oro/dolt.go:457`) must use the `launchAgentLabel` constant, not the stale `com.oro.dolt-server` string:

```go
cmd := exec.CommandContext(ctx, launchctlPath, "kickstart", "-k",
    fmt.Sprintf("gui/%d/%s", os.Getuid(), launchAgentLabel))
```

Add regression test `TestKickstartLabelMatchesPlist` asserting the kickstart service target matches the installed plist's `Label`. This prevents drift.

**Why first:** without D0, every launchctl kickstart call in D1/D3 is a silent no-op. D0 is a one-line fix that eliminates the main reproducer of today's failure even before the rest of the spec lands. Ship D0 as a P0 bead that can hotfix production on its own.

**Risks accepted:** none — it's a factual typo fix.

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

**Test/CI escape hatch:** **build tag** `//go:build integration` gates the direct-spawn path. Production binaries (built without the tag) physically cannot take the direct-spawn path — a plaintext env var was rejected because CI/dev shell rc files could silently re-enable the bug. Tests that need direct spawn run under `go test -tags integration`. The released binary has no escape hatch.

**Risks accepted:**
- *Tigers:* onboarding pain — first `oro start` after install requires `oro dolt setup` to have run. Mitigation: `oro setup` (existing) wires `oro dolt setup` into its prereq sequence.
- *Elephants:* if launchctl kickstart silently no-ops (e.g., agent disabled by user), the failure surface shifts to "kickstart succeeded, port still down." Mitigation: post-kickstart wait + identity-probe retry, then exit hint.

### D2 — Identity probe: process arg + SQL probe (defense in depth)

On every adoption attempt:

**Step 1 — process probe (~5ms):**
- Read PID from `~/.oro/dolt-server.pid` if present.
- If absent, discover via `lsof -i :13307 -sTCP:LISTEN -t` (single PID expected).
- Read process args via `ps -p <pid> -o args=` — no privileges needed on macOS (verified: `ps -p 62939 -o args=` returned full arg vector from a different shell, no sudo, on this machine).
- **Flag-parse, not positional:** scan the arg vector for `--data-dir <path>` (or `--data-dir=<path>`) regardless of its position relative to the `sql-server` subcommand. Real dolt invocations put `--data-dir` before `sql-server` (observed live).
- Compare to `~/.oro/dolt`.
- Mismatch → identity probe fails with `process_data_dir_mismatch` and the observed path.

**Reconciliation with existing `checkSharedPortConflict`:** `cmd/oro/dolt.go:490-515` already has a partial adoption check that uses a process-name match (`isDoltProcess`) but does **not** verify `--data-dir`. `runIdentityProbe` replaces it. Remove `checkSharedPortConflict` or have it delegate to `runIdentityProbe` to avoid two parallel adoption mechanisms.

**Step 2 — SQL probe (~150ms):**
- **Mechanism: shell to `dolt sql`**, not a Go MySQL driver. Verified: `github.com/go-sql-driver/mysql` is NOT in `go.sum`; adding it expands the binary and couples oro to a specific wire protocol version. `dolt` CLI is already required by `startSharedDoltServer`, so shelling adds no new runtime dependency.
- Command: `dolt sql -h 127.0.0.1 -P 13307 --result-format json -q "SHOW DATABASES;"`.
- Parse JSON output; assert `dolt_database` from `.beads/metadata.json` is in the result.
- Missing → fails with `database_not_found` and the list of databases actually present.
- If `dolt` not in PATH → skip SQL probe, keep process probe only. Log `warn: dolt CLI absent; SQL probe skipped, process probe authoritative`.

**Step 3 — write `.server-identity` cookie:**
- On successful probe, oro writes `~/.oro/dolt/.server-identity.json` (PID, start_time, data_dir, observed_at).
- Subsequent probes within 60s can short-circuit on cookie freshness (PID still alive + cookie age <60s) → ~1ms total.

**Risks accepted:**
- *Tigers:* `lsof` may be missing on minimal macOS images. Mitigation: PID file is primary; lsof is fallback only. If both missing, probe fails closed with `cannot identify dolt owner` and repair hint.
- *Tigers:* `dolt` CLI may be absent in minimal macOS/CI images. Mitigation: process probe is authoritative; SQL probe logs a warn and skips when dolt is not in PATH (see D2 Step 2 last bullet). `dolt sql --result-format json` is confirmed supported by dolt 1.85.0+ (bd-shipped).
- *Elephants:* race between cookie write and a launchd restart. Mitigation: cookie includes `start_time` (parse from `ps -o lstart=`); mismatched start_time invalidates cookie.

### D3 — `oro dolt repair` subcommand (single repair pathway)

New Cobra command. Behavior:

1. Acquire flock on `~/.oro/.dolt-spawn.lock` (in `oroHome`, which the binary always creates — not `~/.oro/dolt/` which may not exist before `oro dolt setup` runs). Timeout 30s; on timeout assume another oro is repairing and re-probe. `os.MkdirAll(oroHome, 0o750)` before `os.OpenFile` for the lock — defensive against ENOENT on cold install.
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
2. Atomic write: **`writeFileAtomic` does not yet exist in source** (oro-kn25 was closed by ops escalation but code sits unmerged on `agent/oro-kn25`). **Decision: inline a private `atomicWriteFile` helper in `cmd/oro/dolt_migrate.go`** — unblocks this spec without waiting for oro-kn25 merge. When oro-kn25 eventually lands, switch call site and delete the private helper. Operation: write to `.tmp`, `fsync`, `rename` (POSIX atomic).
3. Migration is **idempotent** — read → modify → write is safe for a second-comer (same input → same output). Worktrees sharing the parent's `.beads/` (confirmed: `.worktrees/oro-cjpn` shares `/Users/as21/codehouse/oro/.beads/`) do not need cross-process locking for the migration itself; atomicity + idempotence is sufficient.

**Risks accepted:**
- *Tigers:* concurrent `oro start` from two projects could both try to migrate. Mitigation: each project has its own `.beads/` — no cross-project lock needed.
- *Paper tigers:* "what if user reverts bd to 1.0.0" — bd 1.0.0 ignores the field's absence (port file is already the new contract). No regression.

### D5 — Fix `SetupSignalHandler` + `makeDoltLifecycle` stop closures to respect shared lifetime (P0)

**Root cause (direct, not investigation):** `cmd/oro/daemon.go:182-194` — `SetupSignalHandler` accepts `beadsDir` and unconditionally calls `stopDoltServer(beadsDir)` on SIGTERM/SIGINT. In shared mode, `stopDoltServer` (`cmd/oro/dolt.go:229-278`) falls through the PID-file-absent branch, reads `metadata.json.dolt_server_port=13307` (written by `oro dolt setup` → `setDoltPort`), discovers the shared dolt PID via `lsof`, and kills it. Also: `cmd/oro/cmd_start.go:440-441` — `makeDoltLifecycle`'s non-shared branch returns `stopDoltServer` for per-project ports only, but has no defense if a shared-port project leaks into this code path.

**Fix (simple, guards at preserving call sites only — NOT in `stopDoltServer`):**

Guards must live at the callers that should preserve the shared dolt, not inside `stopDoltServer` itself. `stopDoltServer` is also called by `runDoltStop` (`oro dolt stop`) and `runDoltTeardown` (`oro dolt teardown`) — both legitimate user-invoked kill paths whose entire purpose is to stop the shared server. Adding a defensive guard inside `stopDoltServer` would silently break those commands.

1. `cmd/oro/daemon.go:SetupSignalHandler` — resolve port from `beadsDir`; if `port == SharedDoltPort`, stop closure is a no-op with log `"shared dolt server preserved across sessions"`.
2. `cmd/oro/cmd_start.go:makeDoltLifecycle` — shared-port branch already returns `nil` stop func (correct); add an assertion in the non-shared branch that asserts port != SharedDoltPort before wrapping `stopDoltServer` (belt-and-suspenders; this path should already never see shared port thanks to the existing `isSharedServer(port)` check).
3. `stopDoltServer` itself is **unchanged** — callers from `oro dolt stop` / `oro dolt teardown` rightly terminate the shared server.

**Regression test** `TestOroStopPreservesSharedDolt`:
- Spawn shared dolt on `SharedDoltPort`.
- Invoke `runStopSequence` with a shared-mode beadsDir.
- Assert dolt PID is still alive after `oro stop` returns.
- Also test `cmd/oro/daemon_test.go:TestSetupSignalHandlerNoStopsSharedDolt`.

**Risks accepted:**
- *Tigers:* defensive guard in `stopDoltServer` changes the error surface — a caller that expected the shared server to shut down on a test path now sees a no-op. Mitigation: test fixtures should use per-project mode.

### D6 — Enumerate and fence ALL direct-spawn pathways

Complete audit. Every code path that can call `dolt sql-server` (directly via `startSharedDoltServer` or per-project `startDoltServer`):

| # | Call site | Current behavior | Post-fix behavior |
|---|-----------|------------------|-------------------|
| 1 | `ensureSharedDoltRunning` fallback (`cmd/oro/dolt.go:444`) | Spawns `startSharedDoltServer` if kickstart fails | Return error, point at `oro dolt setup` / `oro dolt repair`. Gate direct spawn behind `integration build tag=1`. |
| 2 | `newDoltStartCmd` RunE (`cmd/oro/cmd_dolt.go:614`) | Operator-facing `oro dolt start` → direct `startSharedDoltServer` | Route through `ensureSharedDoltRunning` (same probe-then-kickstart pathway). Operator command uses same single-owner contract. |
| 3 | `newDoltSetupCmd` (`cmd/oro/cmd_dolt.go:68`) → `runDoltSetup` → `startFn` | First-time bring-up, legitimate | **Keep** — `oro dolt setup` is the legitimate bootstrap. Add comment calling out this is the ONLY legal direct spawn. |
| 4 | `oro dolt repair` (new in D3) | N/A | Legitimate post-failure repair. Explicit allowlist. |
| 5 | `initDoltForProject` (`cmd/oro/cmd_init.go:722`) | Per-project `startDoltServer` — collides if `DerivePort` hashes to 13307 | **Fence**: if derived port == `SharedDoltPort`, refuse to per-project-spawn; advise migration to shared mode via `oro dolt setup`. |
| 6 | `cmd_cleanup.go:368` pgrep pattern (`dolt sql-server.*\.beads/dolt`) | Legacy pattern for pre-shared-mode orphans | Keep — it's a discovery pattern, not a spawn site. |
| 7 | `daemon.go:190-194` `stopDolt` closure (NOT a spawn, but a STOP path that kills shared dolt today — the aipkm regression) | Unconditionally calls `stopDoltServer(beadsDir)` on SIGTERM/SIGINT → kills shared dolt in shared mode | **Guard** in D5: no-op when resolved port == `SharedDoltPort`. |
| 8 | `cmd_start.go:440-441` `makeDoltLifecycle` non-shared stop closure | Returns `stopDoltServer(beadsDir)` for per-project ports | **Guard**: assert port != `SharedDoltPort` before wrapping. Shared branch already returns nil stop (correct). |

**Allowlist enforcement:** add a `// startSharedDoltServer LEGAL CALLERS: oro dolt setup, oro dolt repair — DO NOT add more without updating D6.` comment at the function's definition. Add a test `TestStartSharedDoltServer_CallerAllowlist` using `go/parser + go/ast` to walk `cmd/oro/*.go` and assert the set of `CallExpr` nodes naming `startSharedDoltServer` is a subset of `{newDoltSetupCmd, newDoltRepairCmd}`. Go-native analysis — no shelling to rg, not a fragile regex.

**Risks accepted:**
- *Tigers:* `oro dolt start` users may expect idempotent direct spawn; routing through probe pathway changes the error surface. Mitigation: keep exit-code semantics; error messages suggest next command.
- *Paper tigers:* `--force-direct-spawn` flag for ops operators — deferred. Use `integration build tag=1` for now.

### D7 — Wire `oro dolt setup` into `oro setup` onboarding (prevents onboarding regression from D6)

Audit: `cmd/oro/cmd_setup.go` phases 1–5 are prereqs/language-detect/tools/bootstrap/doctor. None invoke `runDoltSetup`. After D6 removes direct-spawn fallback, the first-ever `oro start` on a clean machine exits with "shared dolt is not running and launchctl kickstart failed" because no plist is installed yet.

**Fix:** invoke `runDoltSetup` from `setupPhase4Bootstrap` **after** `executeBootstrap` returns success (`executeBootstrap` writes `~/.oro/projects/<name>/project.root`, which `runDoltSetup`'s `discoverBreadsDirs` needs). Guard: skip if backend is not dolt (detect via `resolveBackend(beadsDir)` or `metadata.json` presence); skip **only if** plist exists at `launchAgentPlistPath(homeDir)` AND `isLaunchAgentLoaded()` returns true (plist existence alone is not sufficient — `launchctl unload` leaves the file but no registered agent). When plist exists but not loaded, `runDoltSetup` re-bootstraps (idempotent). When agent loaded but plist path differs, log warn and continue.

**Acceptance:** on a fresh install, `oro setup && oro start` works end-to-end with no manual `oro dolt setup` call.

**Risks accepted:**
- *Tigers:* `oro dolt setup` requires the dispatcher to be stopped (aborts with error otherwise). Onboarding call must run before any dispatcher is started. Mitigation: phase ordering guarantees this.
- *Elephants:* users who opt into per-project dolt mode don't want shared setup. Mitigation: only run when backend is dolt AND mode is server (or absent — default).

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

| File | Line (current) | Change |
|------|----------------|--------|
| `cmd/oro/dolt.go` | 457 | D0: fix `tryLaunchctlKickstart` label from `com.oro.dolt-server` to `launchAgentLabel` (`dev.getoro.dolt`). |
| `cmd/oro/dolt.go` | 427-445 | D1, D6.1: `ensureSharedDoltRunning`: probe-before-adopt; remove direct-spawn fallback (gated by `integration build tag`). Add `runIdentityProbe(oroHome, dbName) (probeResult, error)` and cookie I/O. |
| `cmd/oro/dolt.go` | 490-515 | D2: retire `checkSharedPortConflict` or delegate to `runIdentityProbe`. |
| `cmd/oro/cmd_dolt_repair.go` | NEW | D3: `oro dolt repair` subcommand. Flock on `~/.oro/.dolt-spawn.lock`. Exit codes 0/2/3/4/5. |
| `cmd/oro/cmd_dolt.go` | 43-49 | D3: register `repair` subcommand in `newDoltCmd()`. |
| `cmd/oro/cmd_dolt.go` | 608-618 | D6.2: `newDoltStartCmd` routes through `ensureSharedDoltRunning` instead of calling `startSharedDoltServer` directly. |
| `cmd/oro/cmd_init.go` | 722 | D6.5: `initDoltForProject` refuses per-project spawn when derived port == `SharedDoltPort`. |
| `cmd/oro/cmd_init.go` | (port/metadata write) | D4: stop writing `dolt_server_port` to `metadata.json`. |
| `cmd/oro/dolt_migrate.go` | NEW | D4: `MigrateMetadataPort(beadsDir) error`. Idempotent. Atomic write via inline private `atomicWriteFile` helper (switch to shared helper if/when oro-kn25 lands). |
| `cmd/oro/cmd_start.go` | 419-442 | D1, D4: `makeDoltLifecycle` calls `MigrateMetadataPort` first, then probe-before-adopt. |
| `cmd/oro/daemon.go` | 182-194 | D5: `SetupSignalHandler` stop closure guards on shared port → no-op. |
| `cmd/oro/dolt.go` | 229-278 | D5: `stopDoltServer` UNCHANGED (legitimate callers `runDoltStop`/`runDoltTeardown` must still work). |
| `cmd/oro/cmd_start.go` | 440-441 | D5: `makeDoltLifecycle` non-shared branch asserts port != SharedDoltPort. |
| `cmd/oro/cmd_setup.go` | 188-204 | D7: insert `runDoltSetup` call inside `setupPhase4Bootstrap` AFTER `executeBootstrap` returns success. |
| `cmd/oro/launchd.go` | — | No code change. Regression test (D0) asserts plist Label == kickstart target. |
| `Makefile` | test target | Add `test-integration` target: `go test -tags integration ./cmd/oro/...`. CI invokes it. |

New test files:
- `cmd/oro/cmd_dolt_repair_test.go`
- `cmd/oro/identity_probe_test.go`
- `cmd/oro/dolt_migrate_test.go`
- `cmd/oro/cmd_stop_shared_dolt_test.go`
- `cmd/oro/kickstart_label_test.go` (D0 regression)
- `cmd/oro/cmd_setup_dolt_chain_test.go` (D7)

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
| Direct-spawn path reached in release binary | — | (unreachable by construction — compile-gated behind `//go:build integration`) | N/A |

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
0. Fixture preconditions: `HOME=$(mktemp -d)`, then `oro dolt setup` to install plist + bring up launchd agent against scratch HOME. All subsequent steps run in this isolated environment.
1. Set up two synthetic projects (A and B) using existing oro-tjq1 fixture machinery (oro-p5el).
2. Kill the legitimate dolt; start a rogue dolt with the *wrong* `--data-dir` on :13307 (simulates today's actual failure).
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
- **Audit of closed-but-unlanded beads**: oro-kn25, oro-mrtt, oro-p5el, oro-cjpn were closed by ops escalation but their code sits on `agent/*` branches unmerged to main. Separate bead to land them or reopen as needed.

---

## Adversarial review checklist (for Stage 2 subagent)

1. Does D1 actually eliminate the random-port-dolt class? Audit every code path that calls `dolt sql-server`.
2. D2 process probe: confirm `ps -p <pid> -o args=` works without privileges on macOS 14+. Test on a foreign-UID PID.
3. D3 flock: confirm `syscall.Flock` semantics on macOS APFS. Race test: two concurrent repairs.
4. D4 migration: what if `oro start` runs on a brand-new project with no metadata? Verify migration is no-op.
5. D5: audit `cmd_stop.go` — does anything call dolt-stop unconditionally?
6. D6: confirm `startSharedDoltServer` is only reachable from `oro dolt setup` and `oro dolt repair` after the change. Grep all call sites.
7. Identity cookie: what happens on launchd restart (start_time changes)? Verify cookie invalidates.
8. SQL probe mechanism: confirm D2 shells to `dolt sql --result-format json` and does NOT add `go-sql-driver/mysql` to go.sum.
9. Error message audit: every failure mode in the table above must have a corresponding test asserting the exact message text.
10. Onboarding: trace `oro setup` (existing, not `oro dolt setup`) end-to-end. Does it now require `oro dolt setup` to have run? If not, first `oro start` will fail.
