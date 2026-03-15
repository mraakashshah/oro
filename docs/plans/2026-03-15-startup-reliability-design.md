# Startup Reliability: Stale Socket Fix + Persistent Dolt

**Date:** 2026-03-15
**Beads:** oro-7v9j (stale socket), oro-jthg (dolt after init)

## Problem

Three reliability issues in oro startup:

1. **Stale socket** (oro-7v9j): When a daemon dies, the UDS socket file persists. On next `oro start`, `pollForSocket` uses `os.Stat` (file exists → success), short-circuits before the new daemon creates its own socket, and `sendStartDirective` fails with "connection refused."

2. **Dolt dies between sessions**: `oro stop` kills dolt → next standalone `bd` command triggers bd's auto-start → 10s timeout (upstream, not ours) → failure. Root cause of recurring "dolt server unreachable" errors.

3. **Dolt not started after init** (oro-jthg): `oro init` writes dolt metadata but doesn't start the server, so the first `bd` command after init hits cold-start.

## Design

### Fix 1: Stale Socket Cleanup

**Two changes, belt-and-suspenders:**

#### 1a. Remove stale socket in StatusStale branch

In `preflightAndCheckRunning` (cmd_start.go:275):

```go
case StatusStale:
    _ = RemovePIDFile(pidPath)
    _ = os.Remove(sockPath)  // NEW: remove stale socket
```

#### 1b. Connect-check in pollForSocket

Replace `os.Stat` with actual UDS connect attempt:

```go
func pollForSocket(log *startupLog, sockPath string, socketTimeout time.Duration) error {
    socketSpinner := log.StartSpinner("Waiting for dispatcher socket...")
    deadline := time.Now().Add(socketTimeout)
    for time.Now().Before(deadline) {
        conn, err := net.DialTimeout("unix", sockPath, 200*time.Millisecond)
        if err == nil {
            _ = conn.Close()
            break
        }
        time.Sleep(socketPollInterval)
    }
    // Final check: must be connectable
    conn, err := net.DialTimeout("unix", sockPath, 200*time.Millisecond)
    if err != nil {
        socketSpinner()
        return fmt.Errorf("dispatcher socket not ready at %s: %w", sockPath, err)
    }
    _ = conn.Close()
    socketSpinner()
    log.Step("Dispatcher socket ready")
    return nil
}
```

**Premortem:**
- *Tiger*: New daemon creates socket but isn't ready for directives yet → `sendStartDirective` fails. Mitigated: the connect-check proves the socket is accepting connections, which means the listener is live.
- *Paper tiger*: Removing a socket file that another process needs → impossible, socket paths are per-project.

### Fix 2: Keep Dolt Alive Across Sessions

**Principle:** oro ensures dolt is running but never kills it. Dolt persists indefinitely.

#### 2a. Adopt running dolt in startDoltServer

In `dolt.go:startDoltServer` (line 94-97), change reject to adopt:

```go
// Before (rejects):
if isDoltServerRunning(port) {
    return 0, fmt.Errorf("port %d is already in use by another process", port)
}

// After (adopts):
if isDoltServerRunning(port) {
    return 0, nil  // already running, adopt it
}
```

#### 2b. Remove dolt stop from oro stop

In `cmd_stop.go`, remove `stopDoltFn` invocation from `runStopSequence`. Remove dolt stop from `runStopAll`. The `stopDoltFn` field can remain in `stopConfig` for `oro dolt stop` (future Phase 2 CLI surface) but is no longer called during normal shutdown.

#### 2c. Remove dolt cleanup from signal handler

In `daemon.go:SetupSignalHandler`, remove the dolt stop call from the cleanup closure. The `beadsDir` parameter becomes unused and can be removed.

#### 2d. Simplify startDoltIfNeeded cleanup

In `cmd_start.go:startDoltIfNeeded`, the cleanup closure should be a no-op. We never stop dolt, even on error paths (it was either already running or we want it to persist).

```go
func startDoltIfNeeded(doltStartFn func() (int, error), doltStopFn func() error) (cleanup func(), err error) {
    noop := func() {}
    if doltStartFn == nil {
        return noop, nil
    }
    if _, err := doltStartFn(); err != nil {
        return noop, fmt.Errorf("start dolt: %w", err)
    }
    return noop, nil  // never clean up dolt
}
```

#### 2e. Keep cleanupDolt but only for stale PIDs

In `cmd_cleanup.go:cleanupDolt`, only kill dolt processes whose PID file points to a dead process (stale PID cleanup). Don't kill healthy running dolt servers. Remove the orphan-scan-and-kill for live processes.

**Premortem:**
- *Tiger*: Dolt accumulates memory over days/weeks without restart → potential resource leak. Mitigation: dolt is a database server designed for long-running operation. Users can manually restart via `bd dolt stop && bd dolt start` if needed.
- *Elephant*: Multiple oro projects sharing port 13486 (hash collision) → one project's dolt blocks another's start. Mitigation: `DerivePort` uses FNV-32a over absolute paths; collision probability ~1/1000 per pair. Low risk for typical usage (1-3 projects).
- *Paper tiger*: "What if the user wants to stop dolt?" → they can use `bd dolt stop`. Future `oro dolt stop` (Phase 2) would also work.

### Fix 3: Start Dolt After Init

In `cmd_init.go:bootstrapProject`, after `ensureDoltMetadata` (line 519-521), add:

```go
// 4d. Start dolt server if not already running.
// Fail-open: warn but continue. Dolt can be started later via bd or oro start.
if meta, _ := readDoltMeta(beadsPath); meta != nil {
    if _, startErr := startDoltServer(beadsPath, port); startErr != nil {
        fmt.Fprintf(os.Stderr, "warning: dolt server start failed: %v\n", startErr)
    }
}
```

With Fix 2a (adopt if running), this is idempotent. If dolt is already running, it returns `(0, nil)`.

**Premortem:**
- *Tiger*: `oro init` runs before dolt is installed → `startDoltServer` returns `exec.ErrNotFound`. Mitigated: fail-open pattern, warning printed, init continues.
- *Paper tiger*: Starting dolt during init slows down init → dolt spawns async, `startDoltServer` returns after `cmd.Start()`, not after server is ready. Minimal latency added.

#### 2f. cleanupDolt: remove orphan-kill pgrep scan

The current `cleanupDolt` (cmd_cleanup.go:316-329) runs `pgrep -f "dolt sql-server.*\.beads/dolt"` and kills ALL matching processes, including healthy running servers. With persistent dolt, this orphan scan must be removed entirely. Only the PID-file-based cleanup (stale PID pointing to dead process) should remain.

```go
func cleanupDolt(cfg *cleanupConfig) bool {
    if cfg.beadsDir == "" {
        return false
    }
    // Only clean up stale PID files (dead process). Do NOT scan/kill live dolt processes.
    pidPath := filepath.Join(cfg.beadsDir, "dolt-server.pid")
    data, err := os.ReadFile(pidPath)
    if err != nil {
        return false // no PID file, nothing to clean
    }
    pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
    if err != nil {
        removeDoltServerFiles(cfg.beadsDir)
        return true
    }
    if !IsProcessAlive(pid) {
        removeDoltServerFiles(cfg.beadsDir)
        fmt.Fprintf(cfg.w, "removed stale dolt PID file (process %d dead)\n", pid)
        return true
    }
    return false // dolt is healthy, leave it alone
}
```

### Fix 1c: Refactor runDispatcherStart to use pollForSocket

`runDispatcherStart` (cmd_dispatcher.go:178-186) has an inline copy of the same `os.Stat`-based socket polling that Fix 1b fixes in `pollForSocket`. Refactor to reuse `pollForSocket`:

```go
func runDispatcherStart(w io.Writer, workers int, spawner DaemonSpawner, socketTimeout time.Duration) error {
    // ... existing path resolution and spawn ...

    // Wait for the dispatcher socket to be connectable (not just file-exists).
    // Reuse pollForSocket which does a UDS connect-check.
    if err := pollForSocket(nil, sockPath, socketTimeout); err != nil {
        return err
    }
    // ... sendStartDirective ...
}
```

Note: `pollForSocket` takes a `*startupLog` which can be nil (no spinner in dispatcher-only mode). The function must tolerate a nil log — add a nil guard on the spinner calls.

### Fix 1d: Add socket cleanup to all StatusStale branches

Three other `StatusStale` handlers only remove the PID file, not the socket:

1. `cmd_stop.go:runStopSequence` (line 292-294)
2. `cmd_dispatcher.go:runDispatcherStopSequence` (line 83-85)
3. `cmd_attach.go` (line 32)

Add `_ = os.Remove(sockPath)` to each. This is belt-and-suspenders — `cleanStaleSocket` in `pkg/dispatcher/stale_socket.go` already handles stale sockets on the dispatcher bind path, but cleaning up early is better.

**Note:** `pkg/dispatcher/stale_socket.go:cleanStaleSocket` already handles stale sockets when the dispatcher binds its listener (connect-check, remove if stale, error if active). This is the secondary defense. Fix 1d adds cleanup at the oro CLI layer as the primary defense.

## Change Surface

| File | Change | Fix |
|------|--------|-----|
| `cmd/oro/cmd_start.go` | Remove stale socket in StatusStale; connect-check in pollForSocket; simplify startDoltIfNeeded cleanup; nil-guard startupLog in pollForSocket | 1a, 1b, 2d |
| `cmd/oro/cmd_dispatcher.go` | Refactor runDispatcherStart to use pollForSocket; add socket cleanup to StatusStale in runDispatcherStopSequence | 1c, 1d |
| `cmd/oro/cmd_stop.go` | Remove stopDoltFn call from runStopSequence and runStopAll; add socket cleanup to StatusStale | 1d, 2b |
| `cmd/oro/cmd_attach.go` | Add socket cleanup to StatusStale branch | 1d |
| `cmd/oro/dolt.go` | Adopt running dolt in startDoltServer (return nil instead of error) | 2a |
| `cmd/oro/daemon.go` | Remove dolt cleanup from SetupSignalHandler; drop beadsDir param | 2c |
| `cmd/oro/cmd_cleanup.go` | Rewrite cleanupDolt: PID-file-only cleanup, remove pgrep orphan scan | 2f |
| `cmd/oro/cmd_init.go` | Start dolt server after ensureDoltMetadata in bootstrapProject | 3 |

## Test Plan

### Fix 1 tests
1. `preflightAndCheckRunning`: StatusStale removes both PID file and socket file
2. `pollForSocket`: stale socket file present → waits for new connectable socket (not short-circuit)
3. `pollForSocket`: no socket file → waits and succeeds when socket appears
4. `pollForSocket`: timeout with no socket → returns error
5. `pollForSocket`: nil startupLog → no panic (nil guard)
6. `runDispatcherStart`: uses connect-check polling (no os.Stat short-circuit)
7. `runStopSequence` StatusStale: removes both PID file and socket file
8. `runDispatcherStopSequence` StatusStale: removes both PID file and socket file
9. `runAttach` StatusStale: removes socket file before returning error

### Fix 2 tests

1. `startDoltServer`: port already in use → returns (0, nil) not error
2. `runStopSequence`: does NOT call stopDoltServer
3. `SetupSignalHandler`: cleanup does NOT stop dolt; beadsDir param removed
4. `startDoltIfNeeded`: cleanup closure is no-op even when dolt was started
5. `cleanupDolt`: healthy dolt server (PID alive) → not killed, no pgrep scan
6. `cleanupDolt`: stale PID file with dead process → PID file removed
7. `cleanupDolt`: no PID file → returns false (no pgrep scan)
8. `runStopAll`: does NOT call stopDoltServer per daemon

### Fix 3 tests

1. `bootstrapProject`: starts dolt server after metadata setup
2. `bootstrapProject`: dolt already running → adopts (no error)
3. `bootstrapProject`: dolt binary missing → warns, continues

## Out of Scope

- Changing beads/bd upstream (10s auto-start timeout)
- `oro dolt stop/start/status` CLI subcommands (Phase 2, deferred)
- Project name collision fix (deferred)
- Cross-project port collision detection
