# Shared Dolt Server with launchd Auto-Start

**Date:** 2026-03-17
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

## Architecture

```
~/.oro/
  dolt/                      # shared data directory
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
  "dolt_server_port": 13307
}
```

`bd dolt set` values stored in metadata.json:
- `host`: 127.0.0.1
- `port`: 13307
- `data-dir`: ~/.oro/dolt

## Commands

### `oro dolt setup`

One-time setup. Idempotent — safe to re-run.

1. **Create shared data directory:** `mkdir -p ~/.oro/dolt`
2. **Discover dolt-backend projects:** scan `~/.oro/projects/*/project.root`, read each project's `.beads/metadata.json`, filter `backend == "dolt"`
3. **Migrate each project:**
   a. Copy `.beads/dolt/<database>/` to `~/.oro/dolt/<database>/` (skip if already exists)
   b. Run `bd dolt set port 13307` in project dir
   c. Run `bd dolt set data-dir ~/.oro/dolt` in project dir
   d. Verify: `bd dolt test` succeeds
4. **Kill orphan per-project dolt servers:** any `dolt sql-server` process NOT on port 13307
5. **Start shared server:** `dolt sql-server --host 127.0.0.1 --port 13307 --data-dir ~/.oro/dolt`
6. **Install LaunchAgent:**
   a. Generate plist to `~/Library/LaunchAgents/com.anthropic.oro.dolt.plist`
   b. `launchctl bootout gui/<uid> <plist>` (remove old if exists)
   c. `launchctl bootstrap gui/<uid> <plist>` (load new)
7. **Verify:** TCP connect to 127.0.0.1:13307

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
  oro          ~/.oro/dolt/beads_oro
  sp-ai-gist   ~/.oro/dolt/beads_sp_ai_gist
```

### `oro dolt stop`

Stop the shared server. Warns if any oro dispatcher is running.
`--force` skips the warning.

### `oro dolt start`

Start the shared server manually (if launchd is disabled or for debugging).
Idempotent — adopts if already running.

### `oro dolt teardown`

Reverse of setup: unload launchd plist, stop server, migrate databases back to per-project `.beads/dolt/` dirs. For users who want to go back.

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

## Migration Details

### Per-project migration steps

For each project with `backend == "dolt"` in `.beads/metadata.json`:

1. Read `dolt_database` from metadata.json (e.g., `beads_oro`)
2. Source: `<project-root>/.beads/dolt/<database>/`
3. Dest: `~/.oro/dolt/<database>/`
4. If dest exists and source exists: skip (already migrated)
5. If dest doesn't exist and source exists: `cp -a source dest`
6. If source doesn't exist: skip (no data to migrate — fresh project)
7. Update metadata.json: set `dolt_server_port: 13307`
8. Run `bd dolt set data-dir <abs-path-to-~/.oro/dolt>` in project dir
9. Verify: start shared server if not running, `bd dolt test` in project dir

### Rollback

Old `.beads/dolt/` directories are preserved. To revert a project:
```bash
cd <project>
bd dolt set port 0        # back to derived port
bd dolt set data-dir ""   # back to default .beads/dolt
bd dolt start             # spawn per-project server
```

## Integration with `oro start`

After setup, `oro start` changes behavior:

1. `makeDoltLifecycle()` checks metadata.json
2. If `dolt_server_port == 13307` and data-dir points to `~/.oro/dolt/`:
   - Skip spawning per-project server
   - Just verify shared server is reachable (TCP dial)
   - If not reachable: attempt `launchctl kickstart gui/<uid>/com.anthropic.oro.dolt`
3. If per-project config (old behavior): spawn as before

This is backwards-compatible — projects that haven't run `oro dolt setup` keep working.

## Change Surface

| File | Change |
|------|--------|
| `cmd/oro/cmd_dolt.go` (new) | `oro dolt` subcommand: setup, status, start, stop, teardown |
| `cmd/oro/cmd_dolt_test.go` (new) | Tests for setup flow, migration, plist generation |
| `cmd/oro/dolt.go` | Add `isSharedServer()` check, shared server constants |
| `cmd/oro/cmd_start.go` | Skip per-project spawn when shared server configured |
| `cmd/oro/root.go` | Register `newDoltCmd()` |
| `cmd/oro/launchd.go` (new) | Plist generation, launchctl load/unload helpers |
| `cmd/oro/launchd_test.go` (new) | Plist template tests |

## Out of Scope

- Linux/systemd support (macOS only for now)
- Shared server across machines (always localhost)
- Automatic `oro dolt setup` during `oro init` (explicit opt-in)
- Changing bd itself
- Database-level access control (all databases accessible to all projects)

## Test Plan

1. `oro dolt setup` on fresh machine: creates dir, starts server, installs plist
2. `oro dolt setup` idempotent: re-run doesn't duplicate data or plists
3. Migration: copies database, preserves backup, bd dolt test passes
4. `oro start` with shared server: skips spawn, verifies connectivity
5. `oro start` without shared server: falls back to per-project (backwards compat)
6. `oro dolt status`: shows databases, projects, launchd state
7. `oro dolt teardown`: unloads plist, stops server, restores per-project config
8. LaunchAgent: server restarts after kill -9
9. Port conflict: setup detects and kills orphan per-project server on 13307
10. Database name collision: two projects can't share a database name (error)
