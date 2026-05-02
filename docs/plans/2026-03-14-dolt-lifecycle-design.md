# Dolt Server Lifecycle Management

**Date:** 2026-03-14
**Status:** SUPERSEDED after Phase 10 native beadstore cleanup. Historical analysis only; do not use this as future implementation guidance. Normal Oro operation now uses the native SQLite beadstore; runtime bd/Dolt lifecycle helpers were removed.

> Phase 10 retained this document only as historical context for pre-migration failures. Do not reintroduce `oro dolt` setup/start/stop/teardown or runtime Dolt lifecycle management from this plan.

**Goal:** Run 3 oro instances in 3 projects concurrently without port collisions.

## Problem

Dolt backend requires a running `dolt sql-server`. Currently users must start it manually. If two projects both use port 3307, the second one fails. oro should manage the dolt server lifecycle automatically.

## Design

### Port Assignment

Each project gets a deterministic port derived from its `.beads/` absolute path using FNV-32a hash, mapped to range 13307–14306. This is the same algorithm from the beads reference implementation (`archive/yap/reference/beads/internal/doltserver/doltserver.go`).

```go
func DerivePort(beadsDir string) int {
    abs, _ := filepath.Abs(beadsDir)
    h := fnv.New32a()
    h.Write([]byte(abs))
    return 13307 + int(h.Sum32()%1000)
}
```

**When to assign:** During `oro init`, after `bd init` creates `.beads/`. Write derived port to `.beads/metadata.json` field `dolt_server_port`. If metadata.json doesn't exist, create it with minimum fields (`backend`, `dolt_server_port`, `dolt_database`). If it already has a non-default port (not 3307), respect it.

### PID File Location: Use bd's convention

**Critical decision:** Write dolt PID to `.beads/dolt-server.pid` (not `~/.oro/projects/<project>/dolt.pid`). This is where bd's doltserver package looks for a running server. If we use our own location, bd won't find our server and will try to start another one on the same port.

Also write `.beads/dolt-server.port` with the port number (bd reads this too).

### oro start

Insert dolt server start **before** daemon spawn in `runFullStart`:

1. Read `.beads/metadata.json` — if `backend != "dolt"`, skip entirely
2. Check if dolt binary exists in PATH — fail with actionable error if missing
3. Read port from metadata.json `dolt_server_port` (fall back to `DerivePort` if not set)
4. Check if dolt server already running on the port (`net.DialTimeout`) — if yes, adopt it (skip spawn)
5. If not running, spawn: `dolt sql-server --host 127.0.0.1 --port <port> --data-dir .beads/dolt`
6. Poll for TCP connection (reuse `pollForSocket` pattern but for TCP)
7. Write PID to `.beads/dolt-server.pid` and port to `.beads/dolt-server.port`
8. Store dolt PID in a variable for the daemon signal handler (see below)
9. Continue with daemon spawn

**Error cleanup:** If `SpawnDaemon` or tmux `Create` fails after dolt is already started, call `stopDoltServer` alongside the existing `killFn(pid)` cleanup (see cmd_start.go:172-174 for the pattern).

### Daemon Signal Handler

**Critical:** The daemon's `SetupSignalHandler` in `daemon.go` must also kill dolt on exit. When the daemon catches SIGINT (e.g., user Ctrl+C, or `oro stop` sends SIGINT), the cleanup closure runs. This closure currently only removes the daemon PID file. It must also:

1. Read `.beads/dolt-server.pid`
2. Send SIGTERM to that PID
3. Wait up to 5s, then SIGKILL
4. Remove `.beads/dolt-server.pid` and `.beads/dolt-server.port`

This ensures dolt is cleaned up regardless of how the daemon exits (oro stop, SIGINT, SIGTERM).

### oro stop

Insert dolt server stop **after** dispatcher shutdown in `runStopSequence`:

1. Read dolt PID from `.beads/dolt-server.pid`
2. Send SIGTERM, wait up to 5s
3. SIGKILL if still alive
4. Remove PID and port files

Note: the daemon signal handler also kills dolt, so by the time `runStopSequence` reaches this step, dolt may already be dead. `stopDoltServer` must be idempotent (no-op if PID file missing or process already dead).

### oro stop --all

`runStopAll` iterates project daemons. After calling `runStopSequence` for each, the dolt cleanup happens automatically because:
- The daemon signal handler kills dolt when the dispatcher exits
- `runStopSequence` also calls `stopDoltServer` as belt-and-suspenders

The `stopConfig` struct needs a `beadsDir` field so `runStopSequence` can find `.beads/dolt-server.pid`. Derive it from the project's working directory.

### oro cleanup

Add `cleanupDolt` step after `cleanupDispatcher`:

1. Read `.beads/dolt-server.pid`, kill if alive
2. Remove stale PID and port files
3. Also scan for orphaned dolt processes: `pgrep -f "dolt sql-server.*\.beads/dolt"`

The `cleanupConfig` struct needs the beads dir path.

### New file: `cmd/oro/dolt.go`

Contains:
- `DerivePort(beadsDir string) int`
- `readDoltMeta(beadsDir string) (*doltMeta, error)` — reads metadata.json, returns nil if not dolt backend
- `startDoltServer(beadsDir string, port int) (pid int, err error)` — spawns process, writes PID/port files
- `stopDoltServer(beadsDir string) error` — reads PID file, SIGTERM → wait → SIGKILL, removes files. Idempotent.
- `isDoltServerRunning(port int) bool` — TCP dial check
- `ensureDoltMetadata(beadsDir string, port int) error` — creates or updates metadata.json with port

### Error Paths

| Scenario | Behavior |
|----------|----------|
| `backend != "dolt"` in metadata.json | Skip all dolt logic |
| No `.beads/` directory | Skip (not a beads project) |
| No `.beads/metadata.json` | Skip (beads not initialized, or SQLite) |
| `dolt` binary not in PATH | Fail `oro start` with: "dolt is required but not found in PATH. Install: brew install dolt" |
| Port already in use (by dolt) | Adopt — skip spawn, write PID file |
| Port already in use (by non-dolt) | Fail with: "port <N> in use by another process" |
| `.beads/dolt/` doesn't exist | Fail with: "dolt database not initialized. Run: bd migrate dolt" |
| dolt server crashes mid-session | bd's `EnsureRunning` handles restart on next bd command. Not oro's concern. |
| metadata.json malformed | Fail with parse error, suggest manual inspection |

## Change Surface

| File | Change |
|------|--------|
| `cmd/oro/dolt.go` (new) | All dolt lifecycle helpers |
| `cmd/oro/dolt_test.go` (new) | Tests for port derivation, metadata parsing, start/stop |
| `cmd/oro/cmd_start.go` | Call `startDoltServer` before daemon spawn in `runFullStart` |
| `cmd/oro/cmd_stop.go` | Add `beadsDir` to `stopConfig`, call `stopDoltServer` in `runStopSequence` |
| `cmd/oro/cmd_cleanup.go` | Add `beadsDir` to `cleanupConfig`, add `cleanupDolt` step |
| `cmd/oro/cmd_init.go` | Call `ensureDoltMetadata` in `bootstrapProject` after beads setup |
| `cmd/oro/daemon.go` | Add dolt cleanup to `SetupSignalHandler` cleanup closure |
| `cmd/oro/cmd_start_test.go` | Test dolt start integration |
| `cmd/oro/cmd_stop_test.go` | Test dolt stop in `runStopSequence` and `runStopAll` |
| `cmd/oro/cmd_cleanup_test.go` | Test `cleanupDolt` |

## Out of Scope

- Changing bd itself
- Adding dolt to `oro setup` (assume already installed)
- Unix socket support (bd doesn't support it)
- Dolt auto-restart on crash (bd's EnsureRunning handles it)
