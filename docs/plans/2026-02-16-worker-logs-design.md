# Design: Per-Worker Log Files

Date: 2026-02-16

## Problem

When a worker is stuck, the operator has no way to see what its claude -p subprocess is doing. The only diagnostic path is:
1. `oro directive status` — tells you it's stuck, not why
2. `ps aux | grep` — tells you the process is alive
3. Check worktree existence — tells you if setup failed

Worker stdout currently goes to the shared `/tmp/oro-daemon.log` (interleaved across all workers) and is accumulated in-memory in `sessionText` (not accessible externally). The events table has structured lifecycle events but not raw output.

## Decision: Per-worker log files (Approach A)

**Chosen over:** Ring buffer in dispatcher memory (B), SQLite output table (C).

**Rationale:** Most Unix-native, requires zero protocol changes, survives crashes, supports `tail -f`, and the existing `oro logs` CLI can be extended to read these files.

## Design

### Convention

```
~/.oro/workers/<worker-id>/output.log
```

Worker IDs are timestamp-based (e.g., `worker-1739634000000000000-0`) and never reused, so each directory is unique.

### 1. Log file writing (worker.go)

In `processOutput()` at `worker.go:517`, tee each stdout line to the log file alongside the existing `sessionText` accumulation.

```
processOutput(ctx, stdout):
  open ~/.oro/workers/<worker-id>/output.log (create dirs, append mode)
  wrap in bufio.Writer for buffered I/O
  for each line from scanner:
    sessionText.WriteString(line)     // existing
    bufferedWriter.WriteString(line)  // new
    bufferedWriter.WriteString("\n")  // new
    [existing memory marker extraction]
  flush and close on exit
```

**Truncation on ASSIGN:** In `handleAssign()` at `worker.go:342`, when `sessionText.Reset()` is called (line 358), also truncate the log file. This scopes each file to the current bead assignment and bounds growth.

**Log path derivation:** The worker derives its log path from `os.UserHomeDir()` + `/.oro/workers/` + worker ID. No new CLI flags needed.

**Buffered I/O:** Use `bufio.Writer` to prevent file I/O from blocking the subprocess pipe. Best-effort writes — if file I/O fails, log a warning but don't block `processOutput`.

### 2. Cleanup on dispatcher startup (cmd_start.go)

When the daemon starts, remove `~/.oro/workers/` entirely (like `/tmp/oro-daemon.log` gets `O_TRUNC`). This prevents stale worker log directories from accumulating across daemon restarts.

Location: `cmd_start.go`, just before or after the daemon process is spawned. Use `os.RemoveAll(workersLogDir)` followed by `os.MkdirAll(workersLogDir)`.

### 3. `worker-logs` directive (dispatcher)

Add a new directive `worker-logs` that reads the last N lines of a worker's output log and returns them in the ACK detail.

**Protocol addition:**
```go
// pkg/protocol/directive.go
DirectiveWorkerLogs Directive = "worker-logs"
```

**Args format:** `<worker-id> [lines]` (default 20 lines).

**Handler:** `applyWorkerLogs(args)` in dispatcher.go:
1. Parse worker ID and optional line count from args
2. Validate worker ID exists in `d.workers` (or allow reading logs for recently-dead workers)
3. Derive log file path: `~/.oro/workers/<worker-id>/output.log`
4. Read last N lines from the file (seek from end or read all + tail)
5. Return lines as ACK detail string

**Security:** Validate the worker ID against `protocol.ValidateBeadID`-style pattern to prevent path traversal. The worker ID must match an alphanumeric+hyphen pattern.

### 4. Extend `oro logs` CLI (cmd_logs.go)

Add a `--raw` flag to `oro logs <worker-id>` that reads from the per-worker output file instead of the events table.

```
oro logs worker-123 --raw --tail 20     # last 20 lines of claude output
oro logs worker-123 --raw --follow      # tail -f the output file
oro logs worker-123                     # existing: structured events from SQLite
```

The `--follow` mode with `--raw` uses file polling (check file size every 500ms, read new bytes) rather than fsnotify, for simplicity.

### 5. Dashboard integration (oro-dash)

Add a "Output" tab to the worker detail drilldown (currently has Status/History/Stats tabs). This tab fetches the last N lines via the `worker-logs` directive and displays them in a scrollable view.

Uses the existing `sendDirective` → `readACK` pattern from `fetch.go`. Refreshes on a timer (every 2s) while the tab is active.

## Premortem Mitigations (Accepted)

| Risk | Mitigation |
|------|-----------|
| Unbounded log growth | Truncate on ASSIGN — scopes to current bead |
| File I/O blocking subprocess pipe | Buffered writer, best-effort writes |
| Stale output from previous bead | Truncate on ASSIGN clears old output |
| Dead worker dirs accumulate | Wipe `~/.oro/workers/` on daemon startup |
| claude -p hangs produce empty logs | Accepted limitation — still narrows diagnosis (startup hang vs mid-execution stuck) |
| Worker needs log path | Derive from `os.UserHomeDir()`, no new flags |

## Files to Change

| File | Change |
|------|--------|
| `pkg/worker/worker.go` | Add log file writing in `processOutput()`, truncate in `handleAssign()` |
| `pkg/worker/worker.go` | Add `logDir` field to Worker struct, initialize from `os.UserHomeDir()` |
| `cmd/oro/cmd_start.go` | Wipe `~/.oro/workers/` on daemon startup |
| `pkg/protocol/directive.go` | Add `DirectiveWorkerLogs` constant, update `Valid()` |
| `pkg/dispatcher/dispatcher.go` | Add `applyWorkerLogs(args)` handler in `applyDirective()` |
| `cmd/oro/cmd_logs.go` | Add `--raw` flag, file-based log reading, `--follow` with file polling |
| `cmd/oro/cmd_directive.go` | Update help text with `worker-logs` |
| `cmd/oro-dash/detail.go` | Add "Output" tab to worker detail drilldown |
| `cmd/oro-dash/fetch.go` | Add fetch function for worker-logs directive |

## Acceptance Criteria

1. Running worker's claude -p output is written to `~/.oro/workers/<worker-id>/output.log`
2. `oro logs <worker-id> --raw --tail 20` shows last 20 lines of claude output
3. `oro logs <worker-id> --raw --follow` tails the output file in real-time
4. `oro directive worker-logs <worker-id>` returns recent output lines in ACK
5. Log file is truncated when worker receives a new ASSIGN
6. `~/.oro/workers/` is wiped on daemon startup
7. Dashboard worker detail shows "Output" tab with recent claude output
8. All existing tests pass
