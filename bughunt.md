# Oro Bug Hunt Report

**Date**: 2026-04-12
**Scope**: Full codebase audit — critical paths, concurrency, data integrity, security, edge cases, state management, config, web/API
**Findings**: 26 unique bugs (1 critical, 5 high, 10 medium, 10 low)

---

## Critical

### BUG-01: `merge.Coordinator` ff-only merges into HEAD instead of target branch

- **File**: `pkg/merge/merge.go:196`
- **Severity**: critical
- **Description**: `worktreeRemoveAndFFMerge` runs `git merge --ff-only opts.Branch` in the primary repository, which merges into whatever branch HEAD points to (typically `main`). When `TargetBranch` is an epic branch (e.g., `epic/xyz`), the rebase correctly targets the epic branch (line 200), but the ff-only merge puts the commits onto `main` instead. There is no `git checkout target` before the merge, and `effectiveTarget` (line 195) is only used for the rebase — not for the merge destination.
- **Impact**: Epic child bead changes land on `main` directly, bypassing epic branch isolation. If `main` has advanced, the ff-only merge fails with a confusing error. If `main` hasn't advanced, changes silently land on the wrong branch, corrupting the merge workflow.
- **Suggested fix**: For non-default targets, use `git update-ref refs/heads/<target> <commit>` or the existing `UpdateBranchRef` pattern (already used in `ffMergeEpicBranch`). Alternatively, `git checkout <target>` before the ff-only merge.

---

## High

### BUG-02: `applyRestartWorker` leaves bead permanently stuck in `in_progress`

- **File**: `pkg/dispatcher/dispatcher.go:3786-3791`
- **Severity**: high
- **Description**: When the `restart-worker` directive fires, the function calls `completeAssignment(ctx, beadID)` which marks the SQLite assignment record as "completed", but never resets the bead status to "open" via `d.beads.Update()` and never calls `d.clearBeadTracking(beadID)`. Compare with `applyKillWorker` (line 3690-3696) which correctly does both.
- **Impact**: After a restart-worker directive, the bead stays `in_progress` in the bead source. It won't appear in `bd ready`, can never be reassigned, and requires manual intervention to recover.
- **Suggested fix**: Add `_ = d.beads.Update(ctx, beadID, "open")` and `d.clearBeadTracking(beadID)` to the `beadID != ""` branch, matching `applyKillWorker`.

### BUG-03: `handleReviewRejection` ignores failed reservation, leaking bead state

- **File**: `pkg/dispatcher/dispatcher.go:2237`
- **Severity**: high
- **Description**: `handleReviewRejection` calls `d.withReservation(workerID, ...)` but discards the return value. If the reservation fails (worker disconnected during I/O), the function returns silently with no cleanup. Compare with `qgRetryWithReservation` (lines 1306-1310) which checks the return and calls `d.clearBeadTracking(beadID)` on failure.
- **Impact**: If the worker disconnects during the review rejection re-assignment window, the bead stays `in_progress` with no worker assigned. It's permanently stuck until hourly `pruneStaleTracking` runs, but even then the bead source status is never reset.
- **Suggested fix**: Check the return value of `withReservation`. On failure, call `d.clearBeadTracking(beadID)` and `d.beads.Update(ctx, beadID, "open")`.

### BUG-04: `handleMergeConflictResult` leaks bead state on conflict resolution failure

- **File**: `pkg/dispatcher/dispatcher.go:1885-1890`
- **Severity**: high
- **Description**: When ops merge-conflict resolution fails (non-`VerdictResolved`), the handler only escalates. It does not reset the bead to "open", call `clearBeadTracking`, `completeAssignment`, or clean up the worktree. The `guardMerge` defer already ran before this goroutine started, so `mergingBeads[beadID]` is cleared — but all other state leaks.
- **Impact**: After a failed merge conflict resolution, the bead is permanently stuck in `in_progress` with no worker. The worktree (with rebase in progress) persists on disk. Tracking map entries leak until the hourly prune.
- **Suggested fix**: In the default (failure) case, reset the bead to "open", complete the assignment, clear bead tracking, and remove the worktree — mirroring the non-conflict merge failure path.

### BUG-05: Worker reconnection race — old `handleConn` defer deletes re-registered worker

- **File**: `pkg/dispatcher/dispatcher.go:949-967`
- **Severity**: high
- **Description**: When a worker reconnects, the new `handleConn` goroutine calls `registerWorker(id, newConn)` which upserts the worker entry. The old `handleConn` goroutine's deferred cleanup then fires and unconditionally executes `delete(d.workers, workerID)` (line 958) without checking if the connection has changed. It also resets the bead to "open" (line 964).
- **Impact**: After reconnection, the worker is deleted from the dispatcher's tracking. The bead is reset to "open" and can be double-assigned. The reconnected worker continues working on a bead the dispatcher no longer tracks — orphaned work and potential duplicate assignments.
- **Suggested fix**: In the defer, check that `d.workers[workerID].conn == conn` (the connection this goroutine owns) before deleting. Only clean up if the stored connection matches.

### BUG-06: HTTP `WriteTimeout` kills SSE connections after 30 seconds

- **File**: `pkg/dispatcher/dispatcher.go:761`
- **Severity**: high
- **Description**: The HTTP server is configured with `WriteTimeout: 30 * time.Second`. Go's `WriteTimeout` sets a deadline on the entire response write lifecycle. SSE connections in `pkg/web/server.go:217` are long-lived by design — 30 seconds kills them.
- **Impact**: Every SSE client (the web dashboard via htmx `sse-connect="/events"`) disconnects after 30 seconds, creating a reconnect storm. Users see the dashboard flicker and miss real-time updates.
- **Suggested fix**: Remove `WriteTimeout` from `http.Server`. Use `http.TimeoutHandler` for non-SSE handlers. For SSE, manage write deadlines per-flush via `http.ResponseController` (Go 1.20+).

---

## Medium

### BUG-07: `handleQGExhausted` race window can destroy a new assignment

- **File**: `pkg/dispatcher/dispatcher.go:4611-4645`
- **Severity**: medium
- **Description**: The function releases the worker (sets state to Idle, clears beadID) under lock at line 4611-4619, then unlocks. It calls `cancelOpsAgents` at line 4622, re-acquires the lock at 4627, and sets the worker to Idle + clears beadID again. Between the first unlock (4619) and second lock (4627), `tryAssign` could assign a new bead to the now-idle worker. The second lock block then overwrites the new assignment.
- **Impact**: In a narrow race window, a freshly assigned bead has its worker assignment silently wiped. The bead ends up `in_progress` with no worker.
- **Suggested fix**: Remove the first lock/unlock block (4611-4619). The second block (4627-4645) already does the same release plus tracking cleanup atomically.

### BUG-08: `logEventLocked` blocks on SQLite I/O while holding `d.mu`

- **File**: `pkg/dispatcher/dispatcher.go:4003-4016`
- **Severity**: medium
- **Description**: The comment says it "runs the SQL in a goroutine to avoid blocking while holding the lock" but the implementation executes `d.db.ExecContext` synchronously. Called from `checkHeartbeats` (worker_pool.go:397, 409) while `d.mu` is held.
- **Impact**: Under SQLite contention (many writers, disk I/O spikes), all operations waiting on `d.mu` stall — worker registration, message handling, state queries. Workers could timeout waiting for responses.
- **Suggested fix**: Run the SQL in a goroutine as the comment states, or restructure callers to log events after releasing the lock.

### BUG-09: `maybeAutoScale` TOCTOU race overwrites explicit scale directives

- **File**: `pkg/dispatcher/dispatcher.go:3855-3877`
- **Severity**: medium
- **Description**: Reads `currentTarget` under lock (3855-3858), computes `newTarget` outside the lock (3860-3868), then writes `d.targetWorkers = newTarget` under a new lock (3871-3873). Between the two locks, a `scale` directive could have set a higher target. The auto-scaler unconditionally overwrites it.
- **Impact**: An operator's explicit `scale 10` can be overwritten by the auto-scaler back to a lower value.
- **Suggested fix**: In the second lock block, re-check `d.targetWorkers >= newTarget` before writing. Auto-scale should only increase, never decrease below an explicit target.

### BUG-10: `applyPreempt` bypasses `sendToWorker`, missing disconnect handling

- **File**: `pkg/dispatcher/dispatcher.go:3831`
- **Severity**: medium
- **Description**: Sends the PREEMPT message via `w.encoder.Encode(msg)` directly instead of `d.sendToWorker(w, msg)`. This bypasses message buffering for disconnected workers, dead-worker cleanup, and connection liveness checks. On failure, the worker stays stuck in `WorkerPreempting` state.
- **Impact**: If the connection is dead at preempt time, the worker gets stuck in `WorkerPreempting` state indefinitely until heartbeat timeout reaps it.
- **Suggested fix**: Replace `w.encoder.Encode(msg)` with `d.sendToWorker(w, msg)`. On error, reset `w.state` to its previous value.

### BUG-11: Path traversal in `oro logs --raw` via unsanitized worker ID

- **File**: `cmd/oro/cmd_logs.go:447`
- **Severity**: medium
- **Description**: `getWorkerLogPath` constructs a path via `oroHome + "/workers/" + workerID + "/output.log"` with no validation of `workerID`. A crafted ID like `../../etc/passwd` probes the filesystem. The same unsanitized ID flows into `os.ReadFile` (line 287) and `os.Open` (line 409).
- **Impact**: Local path traversal. Limited by trailing `/output.log` segment, but can probe for directory existence. Higher risk if `oro logs` is wrapped by a web API.
- **Suggested fix**: Validate with `protocol.ValidateBeadID()` or use `filepath.Base(workerID)` to strip path components.

### BUG-12: Internal error messages exposed to HTTP clients

- **File**: `pkg/web/server.go:135,147,159,171,184,189,196,201,208,213`
- **Severity**: medium
- **Description**: Every HTTP handler passes raw `err.Error()` to `http.Error()`. These errors contain file paths, SQLite details, subprocess output, and system state.
- **Impact**: Information disclosure. Attacker with dashboard access can enumerate paths, schema details, and CLI structure. No auth on any endpoint.
- **Suggested fix**: Log full error server-side via `slog.Error`. Return generic "internal server error" to client.

### BUG-13: `MergeMemories` and `mergePair` not wrapped in transactions

- **File**: `pkg/memory/memory.go:831-859` (MergeMemories), `pkg/memory/memory.go:1128-1146` (mergePair)
- **Severity**: medium
- **Description**: `MergeMemories` does a SELECT to verify `keepID`, then separate DELETEs for `deleteIDs` — no transaction. `mergePair` calls `UpdateConfidence` then `Delete` as two separate SQL operations. A crash between steps leaves data in an inconsistent state.
- **Impact**: During `Consolidate()` or `MergeMemories()`, a crash can permanently delete memories without preserving the "keep" record. The codebase already uses transactions correctly in `pkg/codesearch/index.go:139`.
- **Suggested fix**: Wrap multi-step operations in `db.BeginTx()` / `tx.Commit()`.

### BUG-14: Template execution errors produce garbled HTTP responses

- **File**: `pkg/web/server.go:143-148` (and all similar handler patterns)
- **Severity**: medium
- **Description**: Handlers set `Content-Type: text/html`, then call `ExecuteTemplate` which writes directly to `w`. If the template partially renders then errors, the 200 status is already committed. The subsequent `http.Error()` can't change the status and appends plaintext into partial HTML.
- **Impact**: Clients receive corrupted responses: partial HTML + plaintext errors. 200 status means clients won't retry.
- **Suggested fix**: Execute templates into a `bytes.Buffer` first. Write to `w` only on success.

### BUG-15: No read deadline on UDS connections allows goroutine/semaphore exhaustion

- **File**: `pkg/dispatcher/dispatcher.go:942`
- **Severity**: medium
- **Description**: `handleConn` reads from UDS via `bufio.Scanner` with no read deadline. A connected client that sends nothing holds a goroutine and `acceptSem` slot indefinitely. Enough stuck connections prevent new workers from connecting.
- **Impact**: Misbehaving or crashed workers that connect but never send data permanently consume resources. Socket is protected by 0600, but workers are local processes.
- **Suggested fix**: Set `conn.SetReadDeadline(time.Now().Add(heartbeatTimeout))` and refresh on each successful message.

### BUG-16: `pruneStale` deletes pinned memories

- **File**: `pkg/memory/memory.go:1083-1124`
- **Severity**: medium
- **Description**: `pruneStale` selects all memories and checks `confidence * 0.5^(age_days/30) < minScore`. It never reads or checks the `pinned` column. The `scanScoredMemory` function correctly sets `decayFactor = 1.0` for pinned memories, but `pruneStale` recalculates decay independently and ignores the flag.
- **Impact**: Memories explicitly pinned by users (meant to be preserved indefinitely) are deleted by the consolidation process once old enough (~7+ months at default confidence). Silently destroys important institutional knowledge.
- **Suggested fix**: Add `WHERE pinned = 0` to the pruneStale query, or check `pinned` in the loop.

---

## Low

### BUG-17: Memory tag filter uses `%q` producing incorrect LIKE pattern

- **File**: `pkg/memory/memory.go:686`
- **Severity**: low
- **Description**: Tag filter uses `fmt.Sprintf("%%%q%%", opts.Tag)`. The `%q` verb produces Go-quoted strings. For tag `testing`, this generates `%"testing"%` which accidentally works because tags are stored as JSON arrays with quotes. Breaks for tags with special characters (`%q` double-escapes them). Tags containing `%` or `_` act as SQL wildcards.
- **Impact**: False negatives for special-character tags, false positives for tags containing `%` or `_`.
- **Suggested fix**: Use `fmt.Sprintf("%%\"%s\"%%", escapeLike(opts.Tag))` with proper LIKE escaping for `%`, `_`, and `\`.

### BUG-18: `dream.go` `ExecuteActions` MERGE deletes sources without transaction

- **File**: `pkg/memory/dream.go:104-114`
- **Severity**: low
- **Description**: MERGE action inserts the merged memory, then iterates over source IDs deleting each individually — no transaction. A crash after insert but before all deletes creates duplicates. A failed delete is logged and skipped, leaving inconsistent state.
- **Impact**: Memory duplication or partial deletion during dream consolidation. Not catastrophic but violates the MERGE atomicity invariant.
- **Suggested fix**: Wrap insert + deletes in a transaction.

### BUG-19: Eventlog DSN constructed without path escaping

- **File**: `pkg/eventlog/query.go:60`
- **Severity**: low
- **Description**: `NewReader` builds a DSN via `fmt.Sprintf("file:%s?mode=ro&_journal_mode=WAL", dbPath)`. If `dbPath` contains `?`, the portion after it is interpreted as DSN parameters, potentially changing connection mode.
- **Impact**: Narrow exploitation window — requires control of file path via `ORO_HOME` or symlinks.
- **Suggested fix**: URL-encode the path with `url.PathEscape(dbPath)`.

### BUG-20: Worker `handleConnectionError` uses recursion instead of iteration

- **File**: `pkg/worker/worker.go:297`
- **Severity**: low
- **Description**: `handleConnectionError` calls `w.Run(ctx)` recursively after reconnection. Each reconnection adds a stack frame. Many reconnections (flaky dispatcher) cause unbounded stack growth.
- **Impact**: Eventual stack overflow after thousands of reconnections. Unlikely in practice due to retry intervals.
- **Suggested fix**: Return a sentinel error from `handleConnectionError` and wrap `Run`'s main loop in an outer `for` that restarts on that sentinel.

### BUG-21: Dead code in `shutdownRemoveWorktrees` — no-op log statement

- **File**: `pkg/dispatcher/dispatcher.go:4541`
- **Severity**: low
- **Description**: `_, _, _ = d.logEvent, ctx, p` assigns function value, context, and path to blank identifiers without calling the function. Should be `_ = d.logEvent(ctx, "worktree_removed", "dispatcher", "", "", p)`.
- **Impact**: Successful worktree removals during shutdown are not logged. Hinders debugging.
- **Suggested fix**: Replace with an actual `logEvent` call.

### BUG-22: `escalationRetryLoop` does not listen for `shutdownCh`

- **File**: `pkg/dispatcher/dispatcher.go:4101-4133`
- **Severity**: low
- **Description**: Only selects on `ctx.Done()` and `ticker.C`. Unlike `assignLoop` and `heartbeatLoop`, it does not select on `d.shutdownCh`. During shutdown, it continues running until the `wg.Wait` timeout (5 seconds).
- **Impact**: Delays daemon restart by up to 5 seconds. May fire a stale retry escalation during shutdown.
- **Suggested fix**: Add `case <-d.shutdownCh: return true` to the select, matching other loops.

### BUG-23: No read deadline on `readACK` in `sendStartDirective`

- **File**: `cmd/oro/cmd_start.go:777-789`
- **Severity**: low
- **Description**: `sendStartDirective` connects to the dispatcher UDS and calls `readACK` which blocks on `Scanner.Scan()` with no deadline. If the dispatcher accepts but never responds, `oro start` hangs indefinitely.
- **Impact**: `oro start` can hang forever during dispatcher initialization race. User must Ctrl+C.
- **Suggested fix**: `conn.SetReadDeadline(time.Now().Add(10 * time.Second))` before `readACK`.

### BUG-24: Dead code — `Config.Estimator` field overridden unconditionally in constructor

- **File**: `pkg/dispatcher/dispatcher.go:559-562` vs. line 356
- **Severity**: low
- **Description**: `withDefaults()` sets `out.Estimator = NewBeadEstimator()` (haiku model). But `New()` unconditionally creates a separate `LLMEstimator` (opus model) at lines 559-562, ignoring `resolved.Estimator`. Two duplicate estimator implementations exist.
- **Impact**: Bead estimation uses opus instead of haiku — ~30x more expensive per call. `Config.Estimator` is dead configuration.
- **Suggested fix**: Remove the duplicate `LLMEstimator` from `dispatcher.go`. Use `resolved.Estimator` from `estimate.go`.

### BUG-25: `readLastNLines` loads entire file into memory

- **File**: `pkg/dispatcher/worker_logs.go:82-107`
- **Severity**: low
- **Description**: Comment claims "sliding window approach to avoid loading the entire file" but implementation reads every line into `[]string`. The `count` parameter has no upper bound.
- **Impact**: Large worker logs (hours of verbose output) can OOM the dispatcher when the manager requests logs.
- **Suggested fix**: Implement actual tail-from-end reading or cap `count` to a reasonable max (e.g., 1000). Fix the misleading comment.

### BUG-26: Duplicate `LLMEstimator` creates new `http.Client` per API call

- **File**: `pkg/dispatcher/dispatcher.go:4804`
- **Severity**: low
- **Description**: `LLMEstimator.callAPI` creates `http.Client{Timeout: 10*time.Second}` on every call. Each call pays the full TLS handshake cost to `api.anthropic.com` since connection pooling lives in the client's transport.
- **Impact**: ~200-400ms extra latency per bead estimation from repeated TLS handshakes.
- **Suggested fix**: Initialize `http.Client` once as a field on `LLMEstimator`.

---

## Summary

| Severity | Count | Key Themes |
|----------|-------|------------|
| Critical | 1     | Wrong merge target for epic branches |
| High     | 5     | Stuck beads (3), reconnection race (1), SSE killed by WriteTimeout (1) |
| Medium   | 10    | Concurrency races (3), missing transactions (2), security (3), pinned memory deletion (1), UDS exhaustion (1) |
| Low      | 10    | Dead code (2), missing deadlines (2), resource waste (3), edge cases (3) |

### Top 3 Priorities

1. **BUG-01** (critical): Epic merge lands on wrong branch — data corruption
2. **BUG-02/03/04** (high): Three independent paths leave beads permanently stuck with no recovery
3. **BUG-05** (high): Worker reconnection race can orphan work and cause double-assignments
