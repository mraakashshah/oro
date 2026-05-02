# Port Registry for Collision-Free Dolt Port Allocation

**Date:** 2026-04-21
**Status:** SUPERSEDED after Phase 10 native beadstore cleanup. Historical
analysis only; do not use this as future implementation guidance. The referenced
runtime Dolt helpers were removed or retired when normal Oro operation moved to
SQLite.
**Tags:** #dolt #init #multi-project

## Problem

Multiple oro/beads projects on the same machine clash on dolt TCP ports:

1. **bd default collision**: Projects initialized with `bd` directly (not `oro init`) default to port 13307 — the same as oro's `SharedDoltPort`.
2. **Hash collision**: `DerivePort` hashes beads-dir paths to ports in [13307, 14306]. Two different paths can hash to the same port. No detection or resolution.
3. **No registry**: Nothing tracks which ports are actually in use. `oro init` and `bd init` both assign ports in isolation.

Real-world impact: aipkm, wyndly-gh, personal-finance-analysis, and oro all ended up on 13307. Manual port reassignment was required.

## Constraints

- Cannot modify `bd` (beads CLI) — it's an external dependency.
- Must work for both standard and stealth project modes.
- Must not break existing projects or the shared-server mode.
- Port allocation must be deterministic across `oro init` re-runs for the same project (idempotent).
- Shared server mode (`oro dolt setup`) is macOS-only (launchd dependency). On Linux, all projects use per-project dolt servers. The registry design is platform-independent.

## Decision: File-based port registry at `~/.oro/port-registry.json`

### Rejected alternatives

**Default to shared server mode**: `oro dolt setup` eliminates collisions entirely (one server on 13307, multiple databases). But it requires launchd (macOS-only), database migration, and forces a single architecture on all users. Too heavy for `oro init`.

**Probe-before-assign (TCP dial)**: Check if a candidate port is in use before assigning. No state file needed. But this races between concurrent `oro init` invocations and doesn't detect ports reserved but not currently running.

### Design

#### Registry file: `~/.oro/port-registry.json`

```json
{
  "version": 1,
  "allocations": {
    "/Users/x/codehouse/oro/.beads": {
      "port": 13308,
      "project": "oro",
      "allocated_at": "2026-04-21T00:00:00Z"
    },
    "/Users/x/codehouse/me/aipkm/.beads": {
      "port": 13309,
      "project": "aipkm",
      "allocated_at": "2026-04-21T00:00:00Z"
    }
  }
}
```

Keyed by **beadsDir** (absolute path), not project name. beadsDir is the canonical unique identifier — project names are ambiguous (stealth projects use `s-<hash>`, two standard projects can share a basename like "api" in different directories).

The `project` field is informational only (for human readability and diagnostics).

#### Port 13307 is unconditionally reserved

Port 13307 (`SharedDoltPort`) is **never** allocated to per-project servers, regardless of whether the shared server is active. Rationale: `bd` defaults to 13307 for projects not initialized through oro, and oro cannot control `bd`. Reserving 13307 prevents the entire class of oro-vs-bd collisions.

When shared server mode IS active, all projects connect to 13307 via the shared server (existing behavior, unchanged). The registry is not consulted in shared mode.

When shared server mode is NOT active, per-project ports are allocated from [13308, 14306] — a range of 999 ports.

#### Allocation algorithm: `AllocatePort(beadsDir, projectName string) int`

```
1. Read ~/.oro/port-registry.json (create if missing; treat corrupt JSON as empty)
2. If beadsDir already has an allocation → return existing port (idempotent)
3. Run pruneRegistry (remove stale entries)
4. Compute candidate = DerivePort(beadsDir)
5. If candidate == 13307 → candidate = 13308  (unconditional reservation)
6. Collect all allocated ports from registry
7. If candidate is not in allocated set → use it
8. Else: scan [13308, doltPortBase + doltPortRange) skipping allocated ports, pick first free
9. Write allocation to registry (atomic write)
10. Return port
```

Step 2 preserves idempotency. Step 4 preserves backward compatibility: existing projects keep their hash-derived port unless it collides. Step 5 enforces the 13307 reservation.

#### Atomic writes and file locking

The registry file is protected against corruption by two mechanisms:

1. **Atomic write**: Always write to `port-registry.json.tmp`, then `os.Rename` to `port-registry.json`. This prevents partial writes from corrupting the registry. This is the PRIMARY defense.

2. **File locking**: `flock` on `~/.oro/port-registry.lock` during the read-modify-write cycle. **Fail-closed**: if lock acquisition fails after 3 retries (100ms, 200ms, 500ms backoff), return an error — do not proceed without the lock.

If the registry file is corrupted (invalid JSON), treat it as empty and rebuild via the mandatory migration scan.

#### Pruning stale entries

`pruneRegistry` runs lazily during `AllocatePort`:

- For each allocation, resolve the project root. For entries under `~/.oro/projects/<name>/beads/`, check if `~/.oro/projects/<name>/` exists. For entries under a repo's `.beads/`, check if the parent directory (project root) exists.
- Pattern: use the same approach as `discoverBreadsDirs` (cmd_dolt.go:140) which reads `project.root` to find the canonical project directory.
- Remove entries where the project root is gone.

Note: stealth beads dirs (`~/.oro/projects/s-<hash>/beads/`) always exist inside oroHome. The correct check is whether the project root (the actual repo directory) still exists, resolved by reading `<projectDir>/project.root` (same pattern as `discoverBreadsDirs` at cmd_dolt.go:132). Do NOT use config.yaml — it does not contain the project root path.

#### Mandatory migration scan

On **first registry creation** (file doesn't exist yet), `AllocatePort` must scan all known beads directories and pre-populate the registry with their current port assignments:

1. Call `discoverBreadsDirs(oroHome)` (already exists at cmd_dolt.go:119)
2. For each discovered beads dir, read its port from `dolt-server.port` file (canonical), falling back to `DerivePort(beadsDir)` for projects that haven't run yet
3. Register each with collision resolution (if two existing projects already share a port, the second one gets bumped)

This prevents the regression where the first project to init claims a port that an unregistered existing project is already using.

#### Integration points

**Superseded after Phase 10:** `initDoltForProject` and `setDoltPort` were
removed during the native beadstore cleanup. This port-registry plan is
historical; do not use it as guidance for new work. Future legacy-Dolt recovery
must go through the beadstore recovery runbook or external bd/Dolt tooling, not
new oro runtime helpers.

**`makeDoltLifecycle` (cmd_start.go:419-443)**: Currently reads port from metadata or `DerivePort`. After this change, it should read from the registry (via `AllocatePort` which is idempotent) to ensure consistency. If the registry doesn't exist yet (pre-migration binary), fall back to existing behavior.

**`oro dolt setup` (cmd_dolt.go:228-275)**: Clear all per-project allocations from the registry **after** successful migration and **after** the shared server is confirmed running. If any step fails, the registry retains old per-project allocations (safe fallback).

**`oro dolt teardown` (cmd_dolt.go:restorePerProjectDBs)**: When reverting from shared mode:
1. Clear all allocations from registry
2. For each project, call `AllocatePort` to re-derive and register atomically
3. First project to register "wins" its hash-derived port; subsequent collisions get bumped

**`cmd_dolt_repair.go`**: Repair flow should read/validate registry state. Non-blocking — repair can warn about inconsistencies.

**`daemon.go` SetupSignalHandler**: The stop closure resolves port via `readDoltMeta`. This remains correct — it reads the runtime truth from metadata.json, which is kept in sync with the registry by `initDoltForProject`.

## Premortem

**Tiger: Registry file corruption (mitigated)**
Atomic write via tmp+rename makes partial writes impossible. File locking prevents concurrent writers. If the file is still corrupted (disk error, etc.), treat as empty and rebuild via migration scan.

**Tiger: Stale registry entries block ports (mitigated)**
`pruneRegistry` checks project root existence, not beads_dir. Stealth project roots are validated via config.yaml, not the always-present `~/.oro/projects/s-<hash>/` directory.

**Elephant: bd-only projects never register**
Projects initialized with `bd` alone won't be in the registry. Mitigation: the mandatory migration scan reads existing `dolt-server.port` files, which covers bd-only projects that have already started their dolt server at least once. Pure-bd projects that never started dolt are best-effort — but they'd have the same collision problem regardless.

**Paper tiger: Port exhaustion**
999 ports in the range (13307 reserved). Even with 25 projects, <3% utilization.

**Paper tiger: Registry adds latency to oro init**
File read + write of a small JSON file. Negligible.

## Test plan

1. `TestAllocatePort_NewProject` — fresh registry, assigns hash-derived port (never 13307)
2. `TestAllocatePort_Idempotent` — same beadsDir returns same port on re-call
3. `TestAllocatePort_Collision` — two projects hash to same port, second gets bumped
4. `TestAllocatePort_SharedPortReserved` — 13307 never allocated even when shared server inactive
5. `TestAllocatePort_PruneStale` — removed project root entries get cleaned; stealth entries with valid root survive
6. `TestAllocatePort_MigrationPopulates` — first call scans discoverBreadsDirs, reads dolt-server.port files, populates registry
7. `TestAllocatePort_ConcurrentLocking` — two goroutines init simultaneously, no duplicate ports
8. `TestAllocatePort_ConcurrentProcesses` — two child processes run oro init for different projects, no duplicate ports in resulting registry
9. `TestAllocatePort_MetadataSync` — when AllocatePort bumps a port, metadata.json is updated via setDoltPort (not ensureDoltMetadata)
10. `TestAllocatePort_CorruptRegistry` — invalid JSON in registry treated as empty, rebuilt via migration scan
11. `TestAllocatePort_DeriveReturns13307` — mock DerivePort to return 13307, verify AllocatePort returns 13308
12. `TestAllocatePort_PruneStealth` — stealth project whose repo root was deleted but `~/.oro/projects/s-<hash>/` still exists gets pruned

## Files to modify

- `cmd/oro/dolt.go` — add `AllocatePort`, `readRegistry`, `writeRegistryAtomic`, `pruneRegistry`, `migrateExistingPorts`
- `cmd/oro/dolt_test.go` — test cases 1-10 above
- `cmd/oro/cmd_init.go:719` — replace `deriveEffectivePort` with `AllocatePort` in non-shared path; use `setDoltPort` when port changes
- `cmd/oro/cmd_start.go:422-443` — `makeDoltLifecycle` reads port from registry (with fallback)
- `cmd/oro/cmd_dolt.go:228-275` — `runDoltSetup` clears registry after successful migration
- `cmd/oro/cmd_dolt.go:restorePerProjectDBs` — teardown uses `AllocatePort` instead of `DerivePort`
- `cmd/oro/cmd_dolt_repair.go` — in `runDoltRepair`, after identity probe passes, read registry and warn if registry port != metadata.json port for the current project
