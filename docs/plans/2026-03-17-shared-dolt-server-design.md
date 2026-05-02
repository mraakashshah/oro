# Shared Dolt Server with launchd Auto-Start

**Date:** 2026-03-17
**Status:** SUPERSEDED after Phase 10 native beadstore cleanup. Historical analysis only; do not use this as future implementation guidance. Normal Oro operation now uses the native SQLite beadstore; runtime bd/Dolt lifecycle helpers were removed.

> Phase 10 retained this document only as historical context for pre-migration failures. Do not reintroduce `oro dolt` setup/start/stop/teardown or launchd-managed Dolt from this plan.

**Problem:** Dolt servers die on sleep/reboot. Each project spawns its own process. No auto-restart. Users must manually `bd dolt start` after every wake.
**Solution:** Single shared dolt server at `~/.oro/dolt/` on port 13307, managed by macOS LaunchAgent.

## Decisions

| Decision | Choice | Why |
|----------|--------|-----|
| Server count | One shared | Fewer processes, single launchd plist, no port conflicts |
| Data directory | `~/.oro/dolt/` | oro owns lifecycle, independent of repo location |
| Port | 13307 (fixed) | Base of existing range, avoids MySQL 3306/3307 |
| Migration | Copy + backup | Copy `.beads/dolt/<db>/` to `~/.oro/dolt/<db>/`, leave old dir as backup |
| Auto-start | macOS LaunchAgent | `KeepAlive: true` restarts on crash/reboot/sleep |
| Client config | `bd dolt set port 13307` only | `data-dir` is server-side only — bd does NOT support `set data-dir` in server mode |

## Architecture

```
~/.oro/
  dolt/                      # shared data directory (--data-dir for dolt sql-server)
    beads_oro/               # project database (migrated from .beads/dolt/)
    beads_myproj/            # another project database
    .doltcfg/                # shared dolt config
  dolt-server.pid            # shared PID file
  dolt-server.port           # always 13307

~/Library/LaunchAgents/
  com.anthropic.oro.dolt.plist   # KeepAlive launchd agent
```

Each project's `.beads/metadata.json` points to the shared server:
```json
{
  "backend": "dolt",
  "dolt_database": "beads_oro",
  "dolt_server_port": 13307,
  "dolt_mode": "server"
}
```

Client-side config is port + database name only. The server-side `--data-dir`
is set in the launchd plist and the `startSharedDoltServer()` function.

### bd PID file compatibility

bd looks for `.beads/dolt-server.pid` and `.beads/dolt-server.port` to detect
a running server. During setup, `oro dolt setup` writes these files in each
project's `.beads/` directory pointing to the shared server's PID and port.
This prevents bd from spawning its own competing server.

On teardown, these files are removed so bd falls back to its own auto-start.

## Commands

### `oro dolt setup`

One-time setup. Idempotent — safe to re-run. **Requires no running dispatchers** (guards against killing per-project servers mid-session).

1. **Guard:** check for running oro dispatchers. Abort with error if any are active.
2. **Create shared data directory:** `mkdir -p ~/.oro/dolt`
3. **Discover dolt-backend projects:** scan `~/.oro/projects/*/project.root`, read each project's `.beads/metadata.json`, filter `backend == "dolt"`
4. **Validate database names are unique:** if two projects share the same `dolt_database`, error with actionable message (user must rename via `bd dolt set database <unique-name>`)
5. **Migrate each project:**
   a. Read `dolt_database` from metadata.json (e.g., `beads_oro`)
   b. Source: `<project-root>/.beads/dolt/<database>/`
   c. Dest: `~/.oro/dolt/<database>/`
   d. If dest exists and source exists with same name: skip (already migrated)
   e. If dest doesn't exist and source exists: copy to temp dir first (`~/.oro/dolt/.<database>.migrating`), then rename to final path (atomic — survives disk-full/crash)
   f. If source doesn't exist: skip (no data to migrate)
   g. Run `bd dolt set port 13307` in project dir
   h. Write `.beads/dolt-server.port` with `13307` (bd compatibility)
6. **Kill orphan per-project dolt servers:** any `dolt sql-server` process NOT on port 13307
7. **Start shared server:** `dolt sql-server --host 127.0.0.1 --port 13307 --data-dir ~/.oro/dolt`
8. **Write shared PID/port files:**
   a. `~/.oro/dolt-server.pid` with shared server PID
   b. `~/.oro/dolt-server.port` with `13307`
   c. Per-project `.beads/dolt-server.pid` with shared server PID (bd compat)
9. **Install LaunchAgent:**
   a. Generate plist to `~/Library/LaunchAgents/com.anthropic.oro.dolt.plist`
   b. `launchctl bootout gui/<uid> <plist>` (remove old if exists)
   c. `launchctl bootstrap gui/<uid> <plist>` (load new)
10. **Verify:** `bd dolt test` in each migrated project

Output: summary of projects migrated, server status, launchd status.

### `oro dolt status`

Show shared server health:
```
Shared Dolt Server
  Status:    running (PID 12345)
  Port:      13307
  Data:      ~/.oro/dolt
  Databases: beads_oro, beads_myproj
  LaunchAgent: loaded (com.anthropic.oro.dolt)

Projects using shared server:
  oro          beads_oro
  sp-ai-gist  beads_sp_ai_gist
```

### `oro dolt stop`

Stop the shared server. Warns if any oro dispatcher is running.
`--force` skips the warning.

### `oro dolt start`

Start the shared server manually (if launchd is disabled or for debugging).
Idempotent — adopts if already running.

### `oro dolt teardown`

Reverse of setup:
1. Unload launchd plist
2. Stop shared server
3. For each project: copy `~/.oro/dolt/<database>/` back to `.beads/dolt/<database>/`
4. Run `bd dolt set port 0` per project (revert to derived port)
5. Remove per-project `.beads/dolt-server.{pid,port}` files
6. `bd dolt start` per project (spawn per-project servers)

## LaunchAgent Plist

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
  "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>com.anthropic.oro.dolt</string>

  <key>ProgramArguments</key>
  <array>
    <string>/opt/homebrew/bin/dolt</string>
    <string>sql-server</string>
    <string>--host</string>
    <string>127.0.0.1</string>
    <string>--port</string>
    <string>13307</string>
    <string>--data-dir</string>
    <string>/Users/USERNAME/.oro/dolt</string>
  </array>

  <key>RunAtLoad</key>
  <true/>

  <key>KeepAlive</key>
  <true/>

  <key>StandardOutPath</key>
  <string>/Users/USERNAME/.oro/dolt-server.log</string>

  <key>StandardErrorPath</key>
  <string>/Users/USERNAME/.oro/dolt-server.err.log</string>

  <key>WorkingDirectory</key>
  <string>/Users/USERNAME/.oro/dolt</string>
</dict>
</plist>
```

Paths resolved at generation time via `os.UserHomeDir()` and `exec.LookPath("dolt")`.

## Integration with `oro start`

After setup, `oro start` changes behavior:

1. `makeDoltLifecycle()` reads metadata.json
2. If `dolt_server_port == 13307` (shared server port):
   - Skip spawning per-project server
   - Verify shared server is reachable (TCP dial to 127.0.0.1:13307)
   - If not reachable: attempt `launchctl kickstart gui/<uid>/com.anthropic.oro.dolt`
   - If still not reachable: fall back to `startDoltServer` with shared data-dir
3. If per-project config (any other port): spawn per-project server as before

Detection uses port alone — no `data-dir` field needed in `doltMeta`. Port 13307 is reserved for the shared server; `DerivePort()` range starts at 13307 but collisions are resolved by the setup process.

## Integration with `oro init`

After setup, `oro init` for new dolt-backend projects:

1. Check if shared server exists (`~/.oro/dolt-server.port` file present)
2. If yes: configure new project to use shared server (port 13307, create database on shared server)
3. If no: fall back to per-project server (existing behavior)

## Integration with `oro cleanup`

`cleanupDolt` must also check `~/.oro/dolt-server.pid` for stale shared server PID files, in addition to per-project `.beads/dolt-server.pid`.

## Change Surface

| File | Change |
|------|--------|
| `cmd/oro/cmd_dolt.go` (new) | `oro dolt` subcommand: setup, status, start, stop, teardown |
| `cmd/oro/cmd_dolt_test.go` (new) | Tests for setup flow, migration, plist generation |
| `cmd/oro/dolt.go` | Add `isSharedServer()` (checks port == 13307), `startSharedDoltServer()`, shared constants |
| `cmd/oro/cmd_start.go` | Skip per-project spawn when shared server detected via port check |
| `cmd/oro/cmd_init.go` | Auto-detect shared server for new dolt projects |
| `cmd/oro/cmd_cleanup.go` | Check `~/.oro/dolt-server.pid` in addition to `.beads/` |
| `cmd/oro/root.go` | Register `newDoltCmd()` |
| `cmd/oro/launchd.go` (new) | Plist generation, launchctl load/unload helpers |
| `cmd/oro/launchd_test.go` (new) | Plist template tests |

## Premortems

| Risk | Severity | Mitigation |
|------|----------|------------|
| Port 13307 already in use by per-project server | Medium | Setup kills orphan per-project servers first |
| Database name collision (two projects both named `beads`) | High | Setup validates uniqueness before migrating; errors with actionable fix |
| Shared server crash takes down all projects | Low | launchd KeepAlive restarts in seconds |
| Migration corrupts data (disk full mid-copy) | High | Atomic copy via temp dir + rename; old `.beads/dolt/` preserved as backup |
| bd spawns competing server (doesn't see shared PID) | High | Write `.beads/dolt-server.{pid,port}` per project during setup |
| Setup runs while dispatcher active | High | Guard check at step 1 — abort if any dispatcher running |
| Concurrent setup from two terminals | Medium | File lock on `~/.oro/dolt.setup.lock` |
| `oro init` after setup creates per-project server | Medium | cmd_init.go detects shared server and configures accordingly |

## Out of Scope

- Linux/systemd support (macOS only for now)
- Shared server across machines (always localhost)
- Changing bd itself
- Database-level access control (all databases accessible to all projects)

## Test Plan

1. `oro dolt setup` on fresh machine: creates dir, starts server, installs plist
2. `oro dolt setup` idempotent: re-run doesn't duplicate data or plists
3. Migration: atomic copy, preserves backup, bd dolt test passes
4. Migration with database name collision: errors before copying anything
5. `oro start` with shared server: skips spawn, verifies connectivity
6. `oro start` with shared server down: kickstarts via launchctl
7. `oro start` without shared server: falls back to per-project (backwards compat)
8. `oro dolt status`: shows databases, projects, launchd state
9. `oro dolt teardown`: unloads plist, stops server, copies data back, restores per-project config
10. LaunchAgent: server restarts after kill -9
11. Port conflict: setup detects and kills orphan per-project server on 13307
12. Setup guard: refuses when dispatcher is running
13. `oro init` after setup: new project auto-configures for shared server
14. `oro cleanup`: finds stale shared server PID files
15. bd compatibility: bd dolt test passes, bd doesn't spawn competing server
