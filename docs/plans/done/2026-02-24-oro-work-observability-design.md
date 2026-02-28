# `oro work` Observability — Reuse Worker Log Infrastructure

**Date:** 2026-02-24
**Status:** Ready

## Problem

When `oro work` runs in background, there's no way to see what Claude is doing. The only output is `logStep()` calls to stderr captured in the task output file — 5-6 lines total across the entire lifecycle. The Claude subprocess stdout is piped through `DrainOutput()` to stderr but lost when backgrounded.

Meanwhile, dispatcher workers already have per-worker log files at `~/.oro/workers/<id>/output.log` with `oro logs --raw --follow` to tail them. `oro work` doesn't use this infrastructure.

## Goal

Make `oro work` write Claude output to the same log file location that dispatcher workers use, so `oro logs` works for both.

## Non-Goals

- Dashboard integration (dispatcher-only feature)
- Progress file with structured JSON metadata
- Changing `oro work`'s stderr output format

## Design

### Log File Location

```
~/.oro/workers/work-<bead-id>/output.log
```

Prefix `work-` distinguishes from dispatcher worker IDs (`worker-<timestamp>-<n>`).

### Writing

`spawnAndWait()` in `cmd_work.go` currently calls:

```go
worker.DrainOutput(ctx, stdout, nil, cfg.beadID, os.Stderr)
```

Change: `executeWork()` resolves `oroHome` (it already has access via `deps`) and opens the log file. It passes the file handle down to `spawnAndWait()` which feeds it to DrainOutput.

```go
// In executeWork(), before the attempt loop:
oroHome, _ := oro.Home()
logDir := filepath.Join(oroHome, "workers", "work-"+cfg.beadID)
os.MkdirAll(logDir, 0o755)
logFile, err := os.OpenFile(filepath.Join(logDir, "output.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
if err == nil {
    defer logFile.Close()
}

// In spawnAndWait() — only include logFile when non-nil:
writers := []io.Writer{os.Stderr}
if logFile != nil {
    writers = append(writers, logFile)
}
worker.DrainOutput(ctx, stdout, nil, cfg.beadID, writers...)
```

### Attempt Separators

On each retry (QG failure, model escalation), write a separator to the log:

```
--- attempt 1 (sonnet) ---
[claude output...]
--- attempt 2 (opus) ---
[claude output...]
```

This preserves all attempts for debugging. No truncation between attempts (unlike dispatcher which truncates on ASSIGN — different use case, `oro work` retries are debugging-relevant).

### Phase Markers

`logStep()` currently writes to `os.Stderr` only. Add a package-level `var logOut io.Writer = os.Stderr` in `cmd_work.go`. `executeWork()` sets it to `io.MultiWriter(os.Stderr, logFile)` when logFile is non-nil. `logStep()` writes to `logOut` instead of `os.Stderr`. This interleaves phase transitions with Claude output:

```
=== Spawning claude (sonnet, attempt 0)... ===
[claude output lines...]
=== Claude completed ===
=== Running quality gate... ===
[qg output...]
```

### DrainOutput Change

Current signature:
```go
func DrainOutput(ctx context.Context, stdout io.ReadCloser, store *memory.Store, beadID string, echo io.Writer)
```

New signature — add variadic writers:
```go
func DrainOutput(ctx context.Context, stdout io.ReadCloser, store *memory.Store, beadID string, writers ...io.Writer)
```

Internally filter nil writers, then use `io.MultiWriter(writers...)` to fan out. Existing callers that pass `os.Stderr` unchanged. `oro work` passes its pre-filtered writers slice.

### Reading

`oro logs <id> --raw` already reads from `~/.oro/workers/<id>/output.log`. It accepts any ID — no validation against dispatcher worker IDs. So `oro logs work-<bead-id> --raw --follow` works immediately with zero changes to `cmd_logs.go`.

**Verify:** Need to confirm `oro logs` doesn't filter by dispatcher worker ID format.

### Cleanup

Log files persist after `oro work` exits. This is intentional — they're useful for post-mortem debugging. Dispatcher wipes `~/.oro/workers/` on startup, which cleans up `oro work` logs too.

No automatic cleanup in `oro work` itself.

## Premortem

### Tigers

| Risk | Severity | Mitigation |
|------|----------|------------|
| `oro logs` may validate worker ID format | Medium | Verify in cmd_logs.go. If it does, add `work-*` to accepted patterns. |
| DrainOutput signature change breaks callers | Medium | Variadic `...io.Writer` is backwards compatible — existing single-writer calls unchanged. |

### Elephants

| Risk | Status |
|------|--------|
| Multiple attempts append to same log — could get large | Accepted. QG output can be verbose. Bounded by max 6 attempts (3 sonnet + 3 opus). |

### Paper Tigers

| Risk | Why it's fine |
|------|---------------|
| Two `oro work` on same bead write to same log | Worktree guard prevents this — can't run two on same bead. |
| Log files accumulate forever | Dispatcher wipes `~/.oro/workers/` on startup. |

## Files to Change

| File | Change |
|------|--------|
| `cmd/oro/cmd_work.go` | Open log file in `executeWork`, pass handle to `spawnAndWait`. Add `logOut` package var, set in `executeWork`. Write attempt separators and phase markers via `logOut`. |
| `pkg/worker/drain.go` | Change `DrainOutput` echo param from `io.Writer` to `...io.Writer`, use `io.MultiWriter`. |
| `pkg/worker/drain_test.go` | Test multi-writer fan-out. |
| `cmd/oro/cmd_work_test.go` | Test log file created and contains output. |

## Acceptance Criteria

1. `oro work <bead-id>` writes Claude output to `~/.oro/workers/work-<bead-id>/output.log`
2. `oro logs work-<bead-id> --raw` displays the log content
3. Multiple attempts are separated by `--- attempt N (model) ---` markers
4. Phase transitions appear in the log interleaved with Claude output
5. DrainOutput signature change doesn't break existing callers
6. `go test ./cmd/oro/... ./pkg/worker/...` passes
