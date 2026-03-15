# Dolt Lifecycle v2: Reliable Shutdown & CLI Surface

**Date:** 2026-03-15
**Supersedes:** `2026-03-14-dolt-lifecycle-design.md` (initial implementation)
**Problem:** `oro stop` leaves dolt processes running. No CLI surface for manual dolt management.

## Background

The v1 implementation (2026-03-14) wired dolt start/stop into `oro start`, `oro stop`, `daemon.go` signal handler, and `oro cleanup`. All paths call `stopDoltServer(beadsDir)`, which reads `.beads/dolt-server.pid` and sends SIGTERM.

**What went wrong:** After `oro stop`, both dolt processes were still alive:
- PID 401: `dolt sql-server --port 3307` (bd's global server)
- PID 87826: `dolt sql-server --port 13486 --data-dir .beads/dolt` (oro's project server)

## Root Causes

### Bug 1: No port-based fallback in `stopDoltServer`

`stopDoltServer` (dolt.go:132) only uses the PID file. If the PID file is missing (not written, crash, manual deletion), it silently returns nil:
```go
if errors.Is(err, os.ErrNotExist) {
    return nil  // ← silently succeeds, dolt keeps running
}
```

The port is known from `metadata.json` but never consulted as a fallback.

### Bug 2: `proc.Wait()` broken for non-child processes

`stopDoltServer` (dolt.go:152-167) uses `proc.Wait()` to detect process exit after SIGTERM. On Unix/macOS, `Wait()` only works for child processes. For adopted or previous-session dolt processes, `Wait()` returns immediately with an error, causing the goroutine to close the `done` channel before the process actually exits. The SIGKILL fallback never triggers.

### Bug 3: `runStopAll` derives wrong beadsDir

`runStopAll` (cmd_stop.go:186) computes beadsDir from the PID path:
```go
beadsDir := strings.TrimSuffix(d.PIDPath, "oro.pid") + ".beads"
```

For PID path `~/.oro/projects/oro/oro.pid`, this produces `~/.oro/projects/oro/.beads` — which **doesn't exist**. The actual `.beads` directory is at the project working directory (e.g., `/Users/as21/codehouse/oro/.beads`), not under `~/.oro/`. No project root → project dir mapping exists.

### Bug 4: Duplicate stop interfaces in stopConfig

`stopConfig` has both `doltStopFn func() error` (old, no args) and `stopDoltFn func(string) error` (new, takes beadsDir). Both are called in sequence (cmd_stop.go:337-348). In `runStopAll`, both interfaces receive the wrong beadsDir — the bug compounds.

### Bug 5: `cleanupDolt` gates orphan scan on PID file

`cleanupDolt` (cmd_cleanup.go:304-306) returns false when PID file is missing:
```go
if _, err := os.Stat(pidPath); errors.Is(err, os.ErrNotExist) {
    return false  // ← skips orphan scan too!
}
```

The orphan scan (line 315) only runs if the PID file existed. Orphans exist precisely when PID files are missing.

### Bug 6: Relative path in daemon signal handler

`SetupSignalHandler` (daemon.go:485) receives `".beads"` as a relative path. If the daemon's CWD changes, cleanup looks in the wrong place. Should use an absolute path.

### Non-bug: Port 3307 (bd's global server)

Port 3307 is bd's default server, started independently. oro only manages project-specific ports (13307-14306). This is by design. `oro dolt status` should report it for visibility.

## Design

### Phase 1: Bug Fixes (Critical Path)

These 4 changes fix the shutdown bug with ~50 lines of production code.

#### 1a. Add `discoverPIDByPort` helper

New function in `dolt.go`:
```go
func discoverPIDByPort(port int) (int, error)
```

Uses `lsof -ti :<port> -sTCP:LISTEN` to find the listening process PID. The `-sTCP:LISTEN` filter ensures only the listener is returned (not connected clients).

**Edge cases:**
- `lsof` not installed: return `exec.ErrNotFound` (callers degrade gracefully)
- Multiple PIDs returned: take the first (listener)
- Empty output: return error (no process on port)

#### 1b. Fix `stopDoltServer` with port-based fallback and poll-based wait

Two fixes in one:

1. **Port fallback:** When PID file is missing, read port from `metadata.json`, use `discoverPIDByPort` to find PID.

2. **Poll-based wait:** Replace `proc.Wait()` with `IsProcessAlive(pid)` poll loop (same pattern as `waitForExit` in cmd_stop.go). This works for any process, not just children.

```go
func stopDoltServer(beadsDir string) error {
    pid := readPIDFromFile(beadsDir)  // returns 0 if missing
    if pid == 0 {
        pid = discoverPIDFromPort(beadsDir)  // fallback
    }
    if pid == 0 {
        return nil  // nothing to stop
    }
    return killAndWait(pid, beadsDir)
}

func killAndWait(pid int, beadsDir string) error {
    proc, err := os.FindProcess(pid)
    if err != nil {
        return nil
    }
    _ = proc.Signal(syscall.SIGTERM)

    // Poll for exit (works for non-child processes).
    deadline := time.After(5 * time.Second)
    ticker := time.NewTicker(100 * time.Millisecond)
    defer ticker.Stop()
    for {
        select {
        case <-deadline:
            _ = proc.Signal(syscall.SIGKILL)
            goto cleanup
        case <-ticker.C:
            if !IsProcessAlive(pid) {
                goto cleanup
            }
        }
    }
cleanup:
    _ = os.Remove(filepath.Join(beadsDir, "dolt-server.pid"))
    _ = os.Remove(filepath.Join(beadsDir, "dolt-server.port"))
    return nil
}
```

#### 1c. Fix `runStopAll` beadsDir + consolidate stop interfaces

**beadsDir fix:** Write `project.root` file during `bootstrapProject` in cmd_init.go:
```go
rootFile := filepath.Join(projectDir, "project.root")
os.WriteFile(rootFile, []byte(projectRoot+"\n"), 0o600)
```

In `runStopAll`, read it back to derive the correct beadsDir:
```go
projectDir := filepath.Dir(d.PIDPath)
rootBytes, _ := os.ReadFile(filepath.Join(projectDir, "project.root"))
beadsDir := filepath.Join(strings.TrimSpace(string(rootBytes)), ".beads")
```

**Interface consolidation:** Remove `doltStopFn func() error` from `stopConfig`. Keep only `stopDoltFn func(string) error` with `beadsDir`. Remove the `makeDoltLifecycle` call from `newStopCmd` (the `stopDoltFn: stopDoltServer` + `beadsDir: ".beads"` combo already covers it). Update `runStopSequence` step 7 to a single call.

**Affected tests:** `TestStopSequenceCleansDolt`, `TestStopAll_CleansDolt` in cmd_stop_test.go reference `doltStopFn` — update to use `stopDoltFn`.

#### 1d. Fix `cleanupDolt` to always scan for orphans

Restructure to always run the orphan scan:
```go
func cleanupDolt(cfg *cleanupConfig) bool {
    cleaned := false
    // Try PID-based stop first (via stopDoltServer with port fallback).
    if err := stopDoltServer(cfg.beadsDir); err != nil {
        fmt.Fprintf(cfg.w, "warning: stop dolt: %v\n", err)
    }
    // Always scan for orphans.
    out, err := cfg.runner.Run("pgrep", "-f", "dolt sql-server.*\\.beads/dolt")
    if err == nil {
        pids := parseWorkerPIDs(out)
        for _, pid := range pids {
            cfg.signalFn(pid)
            cleaned = true
        }
    }
    return cleaned
}
```

#### 1e. Use absolute path in daemon signal handler

In `runDaemonOnly` (cmd_start.go:485), resolve `.beads` to an absolute path before passing to `SetupSignalHandler`:
```go
beadsAbs, _ := filepath.Abs(".beads")
shutdownCtx, cleanup := SetupSignalHandler(ctx, pidPath, d.ShutdownAuthorized(), beadsAbs)
```

### Phase 2: CLI Surface (Feature, can be deferred)

#### 2a. `oro dolt` subcommand group

New file `cmd/oro/cmd_dolt.go`. Follows `oro worker` registration pattern.

```
oro dolt status    Show dolt server status for current project
oro dolt start     Manually start dolt server (idempotent, adopts if running)
oro dolt stop      Manually stop dolt server (with dispatcher-running guard)
```

**`oro dolt status`** — Reads metadata.json for port. Reports PID file, process alive, port listening. Also scans for any dolt processes via `pgrep -f "dolt sql-server"` to show bd's servers.

**`oro dolt start`** — Calls `startDoltServer` with adoption. Idempotent.

**`oro dolt stop`** — Checks if dispatcher is running first. If running, prints warning: "dispatcher is running — use 'oro stop' instead, or pass --force". With `--force`, calls `stopDoltServer` + orphan scan.

Register in `root.go` with `cmd.AddCommand(newDoltCmd())`.

### Phase 3: Adoption Path (Enhancement, deferred)

Not needed for the shutdown fix — the port-based fallback in Phase 1 handles the case where PID file is missing. Adoption tracking can be added later if needed for `oro dolt status` accuracy.

**Deferred decisions:**
- Whether adopted servers should be killed on `oro stop` (currently: yes, via port fallback)
- Whether to verify data-dir matches before killing (prevents cross-project hash collision issues)
- Whether to write a `.dolt-adopted` sentinel to distinguish spawned vs adopted

## Change Surface

| File | Change | Phase |
|------|--------|-------|
| `cmd/oro/dolt.go` | Add `discoverPIDByPort`. Rewrite `stopDoltServer` with port fallback + poll wait. | 1 |
| `cmd/oro/dolt_test.go` | Tests for port fallback, poll wait, PID discovery | 1 |
| `cmd/oro/cmd_stop.go` | Remove `doltStopFn` from `stopConfig`. Fix `runStopAll` beadsDir via `project.root`. | 1 |
| `cmd/oro/cmd_stop_test.go` | Update tests for consolidated stop interface | 1 |
| `cmd/oro/cmd_cleanup.go` | Fix `cleanupDolt` to always scan for orphans | 1 |
| `cmd/oro/cmd_cleanup_test.go` | Test orphan scan without PID file | 1 |
| `cmd/oro/cmd_init.go` | Write `project.root` file during bootstrap | 1 |
| `cmd/oro/cmd_start.go` | Use absolute path for beadsDir in `runDaemonOnly` | 1 |
| `cmd/oro/cmd_dolt.go` (new) | `oro dolt` subcommand group: status, start, stop | 2 |
| `cmd/oro/cmd_dolt_test.go` (new) | Tests for dolt CLI subcommands | 2 |
| `cmd/oro/root.go` | Register `newDoltCmd()` | 2 |

## Dependency Order

```
Phase 1 (bug fixes):
  1a. discoverPIDByPort helper               (no deps)
  1b. stopDoltServer port fallback + poll     (depends on 1a)
  1c. runStopAll beadsDir + consolidate       (no deps on 1a/1b, but test with 1b)
  1d. cleanupDolt always scan orphans         (no deps)
  1e. Absolute beadsDir in daemon handler     (no deps)

Phase 2 (feature):
  2a. oro dolt CLI subcommand                 (depends on Phase 1)
```

## Test Plan

### Phase 1 tests
1. `discoverPIDByPort`: port with listener returns PID
2. `discoverPIDByPort`: port with no listener returns error
3. `discoverPIDByPort`: `lsof` not in PATH returns exec.ErrNotFound
4. `stopDoltServer`: PID file present, process alive → killed
5. `stopDoltServer`: PID file missing, port listening → killed via fallback
6. `stopDoltServer`: PID file missing, port not listening → no-op (nil)
7. `stopDoltServer`: PID file missing, metadata.json missing → no-op (nil)
8. `stopDoltServer`: poll wait actually waits (not instant proc.Wait)
9. `runStopAll`: project.root file present → correct beadsDir
10. `runStopAll`: project.root file missing → graceful degradation
11. `cleanupDolt`: no PID file, orphan dolt running → killed
12. `cleanupDolt`: no PID file, no orphans → no-op

### Phase 2 tests

1. `oro dolt status`: shows port, PID, listening state
2. `oro dolt stop`: refuses when dispatcher running (no --force)
3. `oro dolt stop --force`: kills dolt even with dispatcher running

## Out of Scope

- Managing bd's port 3307 server (bd's concern)
- Auto-restart on dolt crash (bd's `EnsureRunning` handles it)
- Adding dolt to `oro setup` (assume installed)
- Cross-project hash collision detection (probability ~1/1000 per pair)
- Unix socket support for dolt
