# Oro Hardening Spec

**Date:** 2026-02-10
**Status:** Draft — awaiting review
**Scope:** Every structural, security, concurrency, lifecycle, error-handling, and testing deficiency found in a full codebase audit of 550K LOC across 45 production Go files.

---

## Motivation

Oro is a multi-agent orchestration system where bugs in the dispatcher silently lose work, leak goroutines, or leave the system in an unrecoverable state. The codebase passes tests and `go vet`, but a deep audit reveals systemic issues across 6 categories that compound under load, crashes, and adversarial conditions. This spec addresses all of them.

---

## Table of Contents

1. [Epic 1: Concurrency Hardening](#epic-1-concurrency-hardening)
2. [Epic 2: Security Hardening](#epic-2-security-hardening)
3. [Epic 3: Lifecycle & Recovery](#epic-3-lifecycle--recovery)
4. [Epic 4: Dispatcher Decomposition](#epic-4-dispatcher-decomposition)
5. [Epic 5: Error Handling & Observability](#epic-5-error-handling--observability)
6. [Epic 6: Test Infrastructure](#epic-6-test-infrastructure)
7. [Epic 7: Code Hygiene](#epic-7-code-hygiene)
8. [Dependency Graph](#dependency-graph)
9. [Risk Assessment](#risk-assessment)

---

## Epic 1: Concurrency Hardening

Priority: **P0** — these are data-loss and deadlock bugs.

### 1.1 Replace lock-unlock-pray with two-phase commit

**Files:** `pkg/dispatcher/dispatcher.go` (registerWorker lines 388-450, handleQGFailure lines 548-607, handleDone lines 492-542, handleReviewResult lines 860-904)

**Problem:** The dispatcher acquires a mutex, reads state, releases the mutex to do I/O (memory lookup, network send), re-acquires the mutex, and hopes nothing changed. During the unlock window, `checkHeartbeats` can delete the worker, losing bead assignments silently. The `testUnlockHook` exists specifically to reproduce this — the team knows it's broken.

**Design:**

Replace the lock-release-relock pattern with a reservation system:

```go
// Phase 1: Reserve under lock
d.mu.Lock()
w, ok := d.workers[id]
if !ok {
    d.mu.Unlock()
    return
}
w.state = WorkerReserved  // New state — heartbeat checker skips reserved workers
reservation := w.beadID
d.mu.Unlock()

// Phase 2: Do I/O without lock (worker is protected by Reserved state)
memCtx, _ := memory.ForPrompt(ctx, d.memories, nil, reservation, 0)

// Phase 3: Commit under lock
d.mu.Lock()
defer d.mu.Unlock()
w2, ok := d.workers[id]
if !ok || w2.beadID != reservation {
    // Reservation was invalidated (shouldn't happen with Reserved state)
    return
}
w2.state = WorkerBusy
// ... send ASSIGN ...
```

Key invariant: `checkHeartbeats` must skip workers in `WorkerReserved` state (but still track time — reserved workers that exceed timeout are bugs, not crashes).

**Acceptance criteria:**
- [ ] New `WorkerReserved` state added to state machine
- [ ] `checkHeartbeats` skips `WorkerReserved` workers but logs warning if reserved > 30s
- [ ] All 4 lock-release-relock sites converted to reservation pattern
- [ ] `testUnlockHook` still works for testing the reservation path
- [ ] Race detector passes: `go test -race -count=5 ./pkg/dispatcher/...`
- [ ] Existing tests pass without modification (behavior preserved)

**Blocked by:** Nothing
**Blocks:** 1.3 (goroutine lifecycle needs clean state transitions)

---

### 1.2 Add defer to merge abortMu

**Files:** `pkg/merge/merge.go` (lines 72-83)

**Problem:** `abortMu.Lock()` without `defer`. If anything panics between lock and unlock, the mutex is locked forever, deadlocking all future `Abort()` calls. In a merge coordinator handling rebase conflicts, panics are not hypothetical.

**Design:**

```go
func (c *Coordinator) Merge(ctx context.Context, opts Opts) (*Result, error) {
    c.mu.Lock()
    defer c.mu.Unlock()

    c.abortMu.Lock()
    c.activeWorktree = opts.Worktree
    c.abortMu.Unlock()
    // ^^^ Replace above 3 lines with:
    func() {
        c.abortMu.Lock()
        defer c.abortMu.Unlock()
        c.activeWorktree = opts.Worktree
    }()

    defer func() {
        c.abortMu.Lock()
        defer c.abortMu.Unlock()
        c.activeWorktree = ""
    }()
    // ...
}
```

**Acceptance criteria:**
- [ ] All `abortMu.Lock()` calls paired with `defer abortMu.Unlock()` in same scope
- [ ] Test: inject panic between lock/unlock, verify mutex released
- [ ] `go test -race ./pkg/merge/...` passes

**Blocked by:** Nothing
**Blocks:** Nothing

---

### 1.3 Add goroutine lifecycle tracking with sync.WaitGroup

**Files:** `pkg/dispatcher/dispatcher.go` (Run, acceptLoop, assignLoop, heartbeatLoop, mergeAndComplete, handleMergeConflictResult, handleDiagnosisResult, handleReviewResult, GracefulShutdownWorker)

**Problem:** `Run()` spawns 3+ long-lived goroutines and N event-driven goroutines, tracks none of them, and returns while they're still running. This causes: test flakes (goroutines outlive test), resource leaks on shutdown (merge results lost), and makes it impossible to verify clean shutdown.

**Design:**

Add a `wg sync.WaitGroup` field to `Dispatcher`. Every `go` call increments it:

```go
d.wg.Add(1)
go func() {
    defer d.wg.Done()
    d.acceptLoop(ctx, ln)
}()
```

At shutdown, after context cancellation:

```go
<-ctx.Done()
d.shutdownSequence()
_ = ln.Close()

// Wait for all goroutines with timeout
done := make(chan struct{})
go func() { d.wg.Wait(); close(done) }()
select {
case <-done:
    // Clean shutdown
case <-time.After(5 * time.Second):
    // Log warning: goroutines leaked
}
```

Also add a bounded semaphore for connection handlers to prevent unbounded goroutine spawning:

```go
// In acceptLoop:
sem := make(chan struct{}, 100) // Max 100 concurrent connections
for {
    conn, err := ln.Accept()
    // ...
    sem <- struct{}{}
    d.wg.Add(1)
    go func() {
        defer func() { <-sem }()
        defer d.wg.Done()
        d.handleConn(ctx, conn)
    }()
}
```

**Acceptance criteria:**
- [ ] All `go` calls in dispatcher wrapped with `wg.Add(1)` / `defer wg.Done()`
- [ ] `Run()` waits for `wg.Wait()` before returning (with 5s timeout)
- [ ] Connection handler limited to 100 concurrent goroutines
- [ ] Test: verify `Run()` does not return until all goroutines exit
- [ ] Test: verify 101st connection blocks until a slot opens
- [ ] Race detector passes

**Blocked by:** 1.1 (state machine changes affect goroutine behavior)
**Blocks:** Nothing

---

### 1.4 Fix timer leak in worker reconnect

**Files:** `pkg/worker/worker.go` (reconnect, line 601)

**Problem:** `time.After(wait)` creates a timer that leaks if context is cancelled during the sleep. Each cancelled reconnect attempt leaks a timer goroutine.

**Design:**

```go
// Replace:
case <-time.After(wait):

// With:
timer := time.NewTimer(wait)
select {
case <-ctx.Done():
    timer.Stop()
    return fmt.Errorf("worker reconnect: %w", ctx.Err())
case <-timer.C:
}
```

**Acceptance criteria:**
- [ ] No `time.After` in production code (grep confirms zero instances)
- [ ] Test: cancel context during reconnect wait, verify no goroutine leak (runtime.NumGoroutine)
- [ ] Existing reconnect tests pass

**Blocked by:** Nothing
**Blocks:** Nothing

---

### 1.5 Add cancellation to GracefulShutdownWorker goroutine

**Files:** `pkg/dispatcher/dispatcher.go` (GracefulShutdownWorker, lines 1027-1075)

**Problem:** Each call spawns a polling goroutine that runs for up to `timeout` duration with no cancellation mechanism. Repeated shutdown attempts accumulate goroutines.

**Design:**

```go
func (d *Dispatcher) GracefulShutdownWorker(workerID string, timeout time.Duration) {
    // ... send PREPARE_SHUTDOWN ...

    ctx, cancel := context.WithTimeout(context.Background(), timeout)
    d.shutdownCancels.Store(workerID, cancel) // New field: sync.Map

    d.wg.Add(1)
    go func() {
        defer d.wg.Done()
        defer cancel()
        defer d.shutdownCancels.Delete(workerID)

        ticker := time.NewTicker(50 * time.Millisecond)
        defer ticker.Stop()

        for {
            select {
            case <-ctx.Done():
                // Timeout or explicit cancellation
                d.forceShutdownWorker(workerID)
                return
            case <-ticker.C:
                if d.isWorkerGone(workerID) {
                    return
                }
            }
        }
    }()
}
```

**Acceptance criteria:**
- [ ] GracefulShutdownWorker goroutine cancellable via context
- [ ] Tracked by WaitGroup (from 1.3)
- [ ] Duplicate shutdown for same worker cancels previous goroutine
- [ ] Test: call shutdown twice for same worker, verify only 1 goroutine active

**Blocked by:** 1.3 (WaitGroup integration)
**Blocks:** Nothing

---

## Epic 2: Security Hardening

Priority: **P1** — local-only attack surface but defense in depth matters.

### 2.1 Add message size limits to protocol

**Files:** `pkg/dispatcher/dispatcher.go` (handleConn, line 325), `pkg/worker/worker.go` (readMessages, line 206)

**Problem:** Both dispatcher and worker use `bufio.Scanner` with default 64KB max token size. No explicit limit set. The `ReconnectPayload.BufferedEvents` field can contain up to 100 messages, each potentially 64KB.

**Design:**

```go
const MaxMessageSize = 1 << 20 // 1 MB — generous for JSON messages

scanner := bufio.NewScanner(conn)
scanner.Buffer(make([]byte, 0, MaxMessageSize), MaxMessageSize)
```

Add validation on reconnect payload:

```go
func (p *ReconnectPayload) Validate() error {
    if len(p.BufferedEvents) > maxBufferedMessages {
        return fmt.Errorf("reconnect payload exceeds max buffered events: %d > %d",
            len(p.BufferedEvents), maxBufferedMessages)
    }
    return nil
}
```

Define `MaxMessageSize` in `pkg/protocol/message.go` as a shared constant.

**Acceptance criteria:**
- [ ] `MaxMessageSize` constant in `pkg/protocol`
- [ ] Both dispatcher and worker scanners configured with explicit buffer size
- [ ] `ReconnectPayload.Validate()` enforces `maxBufferedMessages` on receive
- [ ] Test: send message > MaxMessageSize, verify clean error (not crash)
- [ ] Test: send reconnect with 200 buffered events, verify rejection

**Blocked by:** Nothing
**Blocks:** Nothing

---

### 2.2 Restrict Unix socket file permissions

**Files:** `pkg/dispatcher/dispatcher.go` (Run, line 281)

**Problem:** Socket created with default umask permissions. Any local user in the same group can connect, send directives, impersonate workers, and extract status information.

**Design:**

After `net.Listen("unix", path)`, explicitly restrict:

```go
ln, err := net.Listen("unix", d.cfg.SocketPath)
if err != nil {
    return fmt.Errorf("listen: %w", err)
}
if err := os.Chmod(d.cfg.SocketPath, 0600); err != nil {
    _ = ln.Close()
    return fmt.Errorf("chmod socket: %w", err)
}
```

**Acceptance criteria:**
- [ ] Socket file created with 0600 permissions
- [ ] Test: verify socket permissions after dispatcher start
- [ ] Existing connection tests pass (same user)

**Blocked by:** Nothing
**Blocks:** Nothing

---

### 2.3 Validate bead IDs for path safety

**Files:** `pkg/dispatcher/worktree_manager.go` (Create, line 29), `pkg/dispatcher/dispatcher.go` (anywhere beadID used in paths)

**Problem:** `beadID` is directly interpolated into file paths (`filepath.Join(repoRoot, ".worktrees", beadID)`) and git branch names (`"agent/" + beadID`) without validation. A crafted bead ID like `../../etc` would escape the worktree directory.

**Design:**

Add validation function in `pkg/protocol`:

```go
var validBeadID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,62}$`)

func ValidateBeadID(id string) error {
    if !validBeadID.MatchString(id) {
        return fmt.Errorf("invalid bead ID %q: must be lowercase alphanumeric with hyphens, 2-63 chars", id)
    }
    return nil
}
```

Call at every entry point where a bead ID arrives from external input:
- `handleMessage` when processing HEARTBEAT, DONE, HANDOFF, READY_FOR_REVIEW, RECONNECT
- `WorktreeManager.Create`
- `assignBead`

**Acceptance criteria:**
- [ ] `ValidateBeadID` in `pkg/protocol`
- [ ] Called at all 3 entry points listed
- [ ] Test: bead IDs with `../`, absolute paths, special characters rejected
- [ ] Test: valid bead IDs (`oro-abc`, `oro-z9x`) accepted
- [ ] `WorktreeManager.Create` returns error for invalid IDs

**Blocked by:** Nothing
**Blocks:** Nothing

---

### 2.4 Harden tmux escalation against injection

**Files:** `pkg/dispatcher/escalator.go` (sanitizeForTmux, line 63)

**Problem:** `sanitizeForTmux` only removes `\n` and `\r`. Tmux `send-keys` interprets shell metacharacters (`$()`, backticks, `;`, `&`, `|`). An attacker controlling escalation message content (via QG output or bead titles) could inject commands into the manager's tmux pane.

**Design:**

Replace `send-keys` with `load-buffer` + `paste-buffer` pattern, which treats content as literal text:

```go
func (e *TmuxEscalator) Escalate(ctx context.Context, typ EscalationType, msg string) error {
    // Write to temp file, load into tmux buffer, paste as literal text
    f, err := os.CreateTemp("", "oro-esc-")
    if err != nil {
        return fmt.Errorf("escalate: create temp: %w", err)
    }
    defer os.Remove(f.Name())

    if _, err := f.WriteString(msg + "\n"); err != nil {
        f.Close()
        return fmt.Errorf("escalate: write: %w", err)
    }
    f.Close()

    // load-buffer treats content as opaque bytes
    if _, err := e.runner.Run(ctx, "tmux", "load-buffer", f.Name()); err != nil {
        return fmt.Errorf("escalate: load-buffer: %w", err)
    }
    if _, err := e.runner.Run(ctx, "tmux", "paste-buffer", "-t", e.pane); err != nil {
        return fmt.Errorf("escalate: paste-buffer: %w", err)
    }
    return nil
}
```

If `load-buffer`/`paste-buffer` is not viable (e.g., tmux version compatibility), keep `send-keys` but escape aggressively:

```go
func sanitizeForTmux(msg string) string {
    // Remove all control characters and shell metacharacters
    var b strings.Builder
    for _, r := range msg {
        if r >= 32 && r < 127 && !strings.ContainsRune("`;|&$(){}[]<>\\!#~", r) {
            b.WriteRune(r)
        } else if r == '\n' || r == '\r' {
            b.WriteRune(' ')
        }
        // Drop everything else
    }
    return b.String()
}
```

**Acceptance criteria:**
- [ ] Escalation messages cannot execute shell commands in manager pane
- [ ] Test: escalation with `$(rm -rf /)` in message — verify literal text, no execution
- [ ] Test: escalation with backticks, pipes, semicolons — all neutralized
- [ ] Normal escalation messages still readable in manager pane

**Blocked by:** Nothing
**Blocks:** Nothing

---

### 2.5 Add periodic cleanup for tracking maps

**Files:** `pkg/dispatcher/dispatcher.go` (rejectionCounts, handoffCounts, attemptCounts, pendingHandoffs, qgStuckTracker)

**Problem:** Five tracking maps grow unbounded. `clearBeadTracking()` only fires on success or escalation. Worker crashes before escalation leave entries forever. Over days/weeks of operation, these leak memory.

**Design:**

Add a cleanup sweep to `heartbeatLoop` (already runs periodically):

```go
func (d *Dispatcher) heartbeatLoop(ctx context.Context) {
    ticker := time.NewTicker(d.cfg.HeartbeatTimeout / 3)
    defer ticker.Stop()

    cleanupTicker := time.NewTicker(1 * time.Hour)
    defer cleanupTicker.Stop()

    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            d.checkHeartbeats(ctx)
        case <-cleanupTicker.C:
            d.pruneStaleTracking(ctx)
        }
    }
}

func (d *Dispatcher) pruneStaleTracking(ctx context.Context) {
    d.mu.Lock()
    defer d.mu.Unlock()

    // Collect active bead IDs
    active := make(map[string]bool)
    for _, w := range d.workers {
        if w.beadID != "" {
            active[w.beadID] = true
        }
    }

    // Prune entries not associated with any active worker
    for id := range d.rejectionCounts {
        if !active[id] { delete(d.rejectionCounts, id) }
    }
    // ... same for all 5 maps
}
```

Also cap `qgHistory.hashes` to 10 entries (sliding window):

```go
// In qg_stuck.go:
const maxQGHistoryEntries = 10

hist.hashes = append(hist.hashes, hash)
if len(hist.hashes) > maxQGHistoryEntries {
    hist.hashes = hist.hashes[len(hist.hashes)-maxQGHistoryEntries:]
}
```

**Acceptance criteria:**
- [ ] Tracking maps pruned hourly for entries not associated with active workers
- [ ] `qgHistory.hashes` capped at 10 entries
- [ ] Test: add 100 tracking entries, kill workers, verify cleanup within one sweep
- [ ] Test: add 20 QG hashes, verify only latest 10 retained
- [ ] No functional change to active bead tracking

**Blocked by:** Nothing
**Blocks:** Nothing

---

### 2.6 Validate configuration values

**Files:** `pkg/dispatcher/dispatcher.go` (Config.withDefaults, lines 170-187)

**Problem:** Config defaults are applied but never validated. Negative `HeartbeatTimeout` would panic on `time.NewTicker`. Zero `PollInterval` would busy-loop. Negative `MaxWorkers` would panic on slice allocation.

**Design:**

```go
func (c *Config) validate() error {
    if c.HeartbeatTimeout <= 0 {
        return fmt.Errorf("HeartbeatTimeout must be positive, got %v", c.HeartbeatTimeout)
    }
    if c.PollInterval <= 0 {
        return fmt.Errorf("PollInterval must be positive, got %v", c.PollInterval)
    }
    if c.FallbackPollInterval <= 0 {
        return fmt.Errorf("FallbackPollInterval must be positive, got %v", c.FallbackPollInterval)
    }
    if c.ShutdownTimeout <= 0 {
        return fmt.Errorf("ShutdownTimeout must be positive, got %v", c.ShutdownTimeout)
    }
    if c.MaxWorkers < 0 {
        return fmt.Errorf("MaxWorkers must be non-negative, got %d", c.MaxWorkers)
    }
    return nil
}
```

Call from `New()` after `withDefaults()`.

**Acceptance criteria:**
- [ ] All duration and count fields validated for positive/non-negative
- [ ] `New()` returns error for invalid config
- [ ] Test: each invalid config value produces descriptive error
- [ ] Test: zero-value config (all defaults) passes validation

**Blocked by:** Nothing
**Blocks:** Nothing

---

### 2.7 Restrict runtime directory permissions

**Files:** `pkg/worker/worker.go` (line 543), `cmd/oro/cmd_start.go` (bootstrapOroDir)

**Problem:** Runtime directories created with `0750` (group-readable). `.oro/` can contain context percentages, handoff files, and session text — all potentially sensitive.

**Design:** Change all `os.MkdirAll(path, 0o750)` to `os.MkdirAll(path, 0o700)` for runtime directories.

**Acceptance criteria:**
- [ ] All `os.MkdirAll` for `.oro/` and runtime dirs use `0o700`
- [ ] Test: verify directory permissions after creation
- [ ] `grep -r '0o750' pkg/ cmd/` returns zero results for runtime dirs

**Blocked by:** Nothing
**Blocks:** Nothing

---

## Epic 3: Lifecycle & Recovery

Priority: **P0** — these cause silent data loss on crash.

### 3.1 Add orphan worktree cleanup on startup

**Files:** `pkg/dispatcher/dispatcher.go` (Run), `pkg/dispatcher/worktree_manager.go`

**Problem:** If the dispatcher crashes, `.worktrees/` directory accumulates orphaned worktrees with potentially uncommitted work. On restart, these are never cleaned up. Over time, disk fills. Stale branches pollute git.

**Design:**

Add `Prune(ctx context.Context) error` to `WorktreeManager`:

```go
func (g *GitWorktreeManager) Prune(ctx context.Context) error {
    // 1. Run git worktree prune to clean invalid entries
    if _, err := g.runner.Run(ctx, "git", "-C", g.repoRoot, "worktree", "prune"); err != nil {
        return fmt.Errorf("worktree prune: %w", err)
    }

    // 2. List remaining worktrees in .worktrees/
    entries, err := os.ReadDir(filepath.Join(g.repoRoot, ".worktrees"))
    if err != nil {
        if os.IsNotExist(err) { return nil }
        return fmt.Errorf("list worktrees: %w", err)
    }

    // 3. Remove each (they're from a previous session)
    for _, e := range entries {
        if !e.IsDir() { continue }
        wt := filepath.Join(g.repoRoot, ".worktrees", e.Name())
        if _, err := g.runner.Run(ctx, "git", "-C", g.repoRoot, "worktree", "remove", "--force", wt); err != nil {
            // Log but don't fail — best effort
            _ = d.logEvent(ctx, "worktree_prune_failed", "", e.Name(), "", err.Error())
        }
    }
    return nil
}
```

Call from `Dispatcher.Run()` before starting accept/assign loops.

**Acceptance criteria:**
- [ ] `WorktreeManager.Prune()` cleans orphaned worktrees on startup
- [ ] Runs `git worktree prune` first (handles broken symlinks)
- [ ] Removes all directories in `.worktrees/`
- [ ] Errors logged but don't prevent startup
- [ ] Test: create orphan worktree, restart dispatcher, verify cleanup
- [ ] Test: no `.worktrees/` directory — no error

**Blocked by:** Nothing
**Blocks:** Nothing

---

### 3.2 Enforce WAL mode in production

**Files:** `cmd/oro/cmd_start.go` (buildDispatcher, around line 227)

**Problem:** SQLite WAL mode is configured in tests but not enforced in production. Without WAL, concurrent readers and writers can deadlock.

**Design:**

After `sql.Open("sqlite", dbPath)`:

```go
db, err := sql.Open("sqlite", dbPath)
if err != nil {
    return nil, nil, fmt.Errorf("open state db: %w", err)
}
if err := db.Ping(); err != nil {
    return nil, nil, fmt.Errorf("ping state db: %w", err)
}
if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
    return nil, nil, fmt.Errorf("set WAL mode: %w", err)
}
if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
    return nil, nil, fmt.Errorf("set busy timeout: %w", err)
}
```

**Acceptance criteria:**
- [ ] WAL mode set on production database open
- [ ] `db.Ping()` called after open
- [ ] `busy_timeout` set to 5000ms
- [ ] Test: verify PRAGMA values after startup

**Blocked by:** Nothing
**Blocks:** Nothing

---

### 3.3 Add stale socket detection before bind

**Files:** `pkg/dispatcher/dispatcher.go` (Run, line 281)

**Problem:** If a previous dispatcher crashed without cleaning up, `net.Listen("unix", path)` fails with "address already in use". The user must manually delete the socket.

**Design:**

Before listen, check for stale socket:

```go
func (d *Dispatcher) cleanStaleSocket() error {
    _, err := os.Stat(d.cfg.SocketPath)
    if os.IsNotExist(err) {
        return nil // No socket, nothing to do
    }
    if err != nil {
        return fmt.Errorf("stat socket: %w", err)
    }

    // Socket exists — try connecting to see if it's alive
    conn, err := net.DialTimeout("unix", d.cfg.SocketPath, 1*time.Second)
    if err == nil {
        conn.Close()
        return fmt.Errorf("another dispatcher is already running on %s", d.cfg.SocketPath)
    }

    // Socket is stale — remove it
    if err := os.Remove(d.cfg.SocketPath); err != nil {
        return fmt.Errorf("remove stale socket: %w", err)
    }
    return nil
}
```

**Acceptance criteria:**
- [ ] Stale socket detected and removed automatically
- [ ] Active socket detected and returns error (no double-start)
- [ ] Test: create stale socket file, start dispatcher, verify it starts
- [ ] Test: start two dispatchers on same socket, verify error

**Blocked by:** Nothing
**Blocks:** Nothing

---

### 3.4 Add subprocess health monitoring to workers

**Files:** `pkg/worker/worker.go`

**Problem:** Workers send heartbeats on connection but don't monitor whether the `claude -p` subprocess is alive between heartbeats. A hung subprocess (see oro-gzi: "claude -p hangs silently") leaves the worker appearing alive while doing nothing.

**Design:**

Add periodic subprocess liveness check to `watchContext`:

```go
func (w *Worker) watchContext(ctx context.Context) {
    ticker := time.NewTicker(w.contextPollInterval)
    defer ticker.Stop()

    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            w.mu.Lock()
            proc := w.proc
            w.mu.Unlock()

            if proc != nil && !w.isProcessAlive(proc) {
                // Subprocess died without sending DONE
                _ = w.SendDone(ctx, false, "subprocess exited unexpectedly")
                return
            }

            // ... existing context percentage check ...
        }
    }
}

func (w *Worker) isProcessAlive(proc Process) bool {
    // Check if process is still running (non-blocking)
    // Implementation depends on Process interface — add method or use OS-level check
}
```

**Note:** This requires adding an `Alive() bool` method to the `Process` interface or using an OS-level process check. Consider whether the `Process` interface should be extended or if a wrapper handles it.

**Acceptance criteria:**
- [ ] Worker detects hung subprocess within `contextPollInterval`
- [ ] Sends `DONE(false)` with error message when subprocess dies unexpectedly
- [ ] Dispatcher re-assigns bead after receiving failed DONE
- [ ] Test: kill subprocess mid-execution, verify worker reports failure within 10s

**Blocked by:** Nothing
**Blocks:** Nothing

---

### 3.5 Fix shutdown hang — enforce hard timeout

**Files:** `pkg/dispatcher/dispatcher.go` (shutdownWaitForWorkers)

**Problem:** If a worker never responds to `PREPARE_SHUTDOWN` and the polling goroutine's timer expires but force-shutdown fails, the dispatcher can hang indefinitely.

**Design:**

Wrap entire shutdown sequence in a hard context timeout:

```go
func (d *Dispatcher) shutdownSequence() {
    // Hard deadline — shutdown MUST complete within 2x ShutdownTimeout
    hardCtx, cancel := context.WithTimeout(context.Background(), 2*d.cfg.ShutdownTimeout)
    defer cancel()

    d.shutdownCancelOps(hardCtx)
    d.shutdownWaitForWorkers(hardCtx)

    if hardCtx.Err() != nil {
        // Hard timeout — force kill everything
        d.mu.Lock()
        for _, w := range d.workers {
            _ = w.conn.Close()
        }
        d.workers = make(map[string]*trackedWorker)
        d.mu.Unlock()
    }

    d.shutdownRemoveWorktrees(hardCtx)
}
```

**Acceptance criteria:**
- [ ] Shutdown completes within `2 * ShutdownTimeout` guaranteed
- [ ] Force-kill all workers if hard timeout exceeded
- [ ] Test: worker that never responds to PREPARE_SHUTDOWN — verify shutdown completes
- [ ] `Run()` returns within bounded time

**Blocked by:** 1.3 (WaitGroup for tracking outstanding goroutines)
**Blocks:** Nothing

---

### 3.6 Handle dispatcher-side message loss during worker disconnect

**Files:** `pkg/dispatcher/dispatcher.go`, `pkg/worker/worker.go`

**Problem:** Workers buffer messages during disconnect (up to 100), but the dispatcher has no buffering. If the dispatcher needs to send ASSIGN or PREPARE_SHUTDOWN while a worker is reconnecting, the message is lost (write to closed conn → error → ignored).

**Design:**

Add pending-message queue to `trackedWorker`:

```go
type trackedWorker struct {
    // ... existing fields ...
    pendingMsgs []protocol.Message // Messages to send on reconnect
}

func (d *Dispatcher) sendToWorker(w *trackedWorker, msg protocol.Message) error {
    if w.disconnected {
        w.pendingMsgs = append(w.pendingMsgs, msg)
        if len(w.pendingMsgs) > 10 {
            // Too many pending — worker is probably dead
            return fmt.Errorf("worker %s: too many pending messages", w.id)
        }
        return nil
    }
    return d.writeMessage(w.conn, msg)
}

// In handleReconnect:
for _, msg := range w.pendingMsgs {
    if err := d.writeMessage(w.conn, msg); err != nil {
        break
    }
}
w.pendingMsgs = nil
```

**Acceptance criteria:**
- [ ] Dispatcher buffers up to 10 messages per disconnected worker
- [ ] Messages replayed on reconnect in FIFO order
- [ ] Worker with >10 pending messages treated as dead
- [ ] Test: send ASSIGN during disconnect, verify worker receives after reconnect
- [ ] Test: send 11 messages during disconnect, verify worker removed

**Blocked by:** Nothing
**Blocks:** Nothing

---

### 3.7 Verify escalation delivery

**Files:** `pkg/dispatcher/escalator.go`

**Problem:** Escalations via `tmux send-keys` fail silently if the tmux session is dead. The manager never receives the escalation, and the stuck bead sits forever.

**Design:**

After sending, verify the tmux pane exists:

```go
func (e *TmuxEscalator) Escalate(ctx context.Context, typ EscalationType, msg string) error {
    // Verify pane exists before sending
    if _, err := e.runner.Run(ctx, "tmux", "has-session", "-t", e.session); err != nil {
        return fmt.Errorf("escalate: tmux session %q not found: %w", e.session, err)
    }

    // ... send message ...

    // Log escalation for audit
    return nil
}
```

If escalation delivery fails, fall back to logging the event and incrementing a "missed escalations" counter that `oro status` can surface.

**Acceptance criteria:**
- [ ] Escalation verifies tmux session exists before sending
- [ ] Failed escalations logged to SQLite events table
- [ ] `oro status` reports count of undelivered escalations
- [ ] Test: escalate with dead tmux session — verify error returned and logged

**Blocked by:** Nothing
**Blocks:** Nothing

---

## Epic 4: Dispatcher Decomposition

Priority: **P2** — structural improvement, not a bug. Do after P0/P1 work stabilizes.

### 4.1 Extract WorkerPool from Dispatcher

**Files:** New file `pkg/dispatcher/worker_pool.go`, refactor `dispatcher.go`

**Problem:** The Dispatcher struct has 23 fields and 43 mutex lock sites because it manages worker lifecycle, bead tracking, merge coordination, ops agent spawning, and directive processing in one struct. The worker registry alone accounts for ~40% of the mutex operations.

**Design:**

Extract a `WorkerPool` struct that owns:
- `workers map[string]*trackedWorker`
- `mu sync.Mutex` (its own lock, not shared with bead logic)
- Worker lifecycle: register, deregister, heartbeat, state transitions
- Connection handling: send messages, track disconnects

The dispatcher becomes a composition of:
```go
type Dispatcher struct {
    cfg      Config
    pool     *WorkerPool       // Worker lifecycle
    tracker  *BeadTracker      // Bead assignment + retry tracking
    ops      OpsSpawner        // Ops agent lifecycle
    merger   *merge.Coordinator
    // ...
}
```

**Scope:** This is a large refactor. Decompose into sub-beads:
1. Extract `WorkerPool` with register/deregister/heartbeat
2. Move state transitions to `WorkerPool`
3. Extract `BeadTracker` with tracking maps + clearBeadTracking
4. Wire dispatcher as composition of pool + tracker

**Acceptance criteria:**
- [ ] `WorkerPool` has its own mutex (not shared with bead tracking)
- [ ] `BeadTracker` owns all 5 tracking maps
- [ ] Dispatcher delegates to pool and tracker — no direct worker map access
- [ ] All existing tests pass without modification
- [ ] Race detector passes
- [ ] Lock contention reduced (fewer lock sites per mutex)

**Blocked by:** Epic 1 (concurrency fixes must land first — refactoring while races exist is dangerous)
**Blocks:** Nothing

---

### 4.2 Unify SubprocessSpawner interfaces

**Files:** `pkg/worker/worker.go` (lines 26-34), `pkg/ops/ops.go` (lines 19-28)

**Problem:** Both packages define `SubprocessSpawner` and `Process` interfaces with different signatures. Worker's returns `(Process, io.ReadCloser, io.WriteCloser, error)`. Ops returns `(Process, error)`. Cannot share implementations.

**Design:**

Keep separate — the semantics genuinely differ. Worker needs stdio pipes for streaming; ops agents run to completion. But rename to clarify:

```go
// pkg/worker/worker.go
type StreamingSpawner interface {
    Spawn(ctx context.Context, model, prompt, workdir string) (Process, io.ReadCloser, io.WriteCloser, error)
}

// pkg/ops/ops.go
type BatchSpawner interface {
    Spawn(ctx context.Context, model, prompt, workdir string) (Process, error)
}
```

**Acceptance criteria:**
- [ ] Interfaces renamed to reflect usage pattern
- [ ] No code sharing assumed between them
- [ ] All tests pass

**Blocked by:** Nothing
**Blocks:** Nothing

---

### 4.3 Move wire types to protocol package

**Files:** `pkg/dispatcher/dispatcher.go` (Bead, BeadDetail, WorkerState, EscalationType), `pkg/protocol/`

**Problem:** Wire types like `Bead`, `BeadDetail`, and `WorkerState` are defined in the dispatcher package but conceptually belong to the protocol layer. External tools (CLI, TUI) that need these types must import the entire dispatcher.

**Design:**

Move to `pkg/protocol`:
- `Bead` struct
- `BeadDetail` struct
- `WorkerState` type and constants
- `EscalationType` type and constants

Keep dispatcher-internal types (like `trackedWorker`, `pendingHandoff`) in dispatcher.

**Acceptance criteria:**
- [ ] Wire types in `pkg/protocol`
- [ ] No import cycle introduced
- [ ] Dispatcher imports protocol for types (already does)
- [ ] All tests pass

**Blocked by:** Nothing
**Blocks:** Nothing

---

## Epic 5: Error Handling & Observability

Priority: **P2** — doesn't cause bugs but makes debugging production issues miserable.

### 5.1 Wrap all bare `return err` with context

**Files:** `cmd/oro/cmd_start.go` (10), `cmd/oro/cmd_stop.go` (6), `cmd/oro/cmd_directive.go` (5), `cmd/oro/tmux.go` (2), `cmd/oro/cmd_status.go` (2), `pkg/worker/worker.go` (3)

**Problem:** ~40 instances of bare `return err` without wrapping. When something fails, the error message has zero context about which operation, component, or step failed.

**Design:**

Mechanical find-and-replace. Every `return err` becomes `return fmt.Errorf("<operation>: %w", err)`. Examples:

```go
// Before:
return err  // line 56 cmd_start.go

// After:
return fmt.Errorf("get oro path: %w", err)
```

**Acceptance criteria:**
- [ ] `grep -rn 'return err$' cmd/ pkg/` returns zero non-test results
- [ ] Every error wrapping includes operation name
- [ ] Error strings start lowercase (Go convention)
- [ ] Fix uppercase error string at `worker.go:316`

**Blocked by:** Nothing
**Blocks:** Nothing

---

### 5.2 Add custom error types for key failure modes

**Files:** `pkg/dispatcher/`, `pkg/worker/`, `pkg/protocol/`

**Problem:** Error discrimination is done by string matching or boolean flags. `DonePayload.QualityGatePassed` is a boolean instead of a typed result. Worker unreachable errors are indistinguishable from protocol errors.

**Design:**

Add to `pkg/protocol`:

```go
type QualityGateError struct {
    BeadID string
    Output string // QG output for debugging
}

type WorkerUnreachableError struct {
    WorkerID string
    Reason   string
}

type BeadNotFoundError struct {
    BeadID string
}
```

These enable `errors.As` matching in handlers instead of boolean flags and string checks.

**Acceptance criteria:**
- [ ] 3 error types defined in `pkg/protocol`
- [ ] Used in at least dispatcher QG handling and worker send paths
- [ ] Tests use `errors.As` for error discrimination
- [ ] Existing boolean flags in payloads remain for wire compatibility

**Blocked by:** Nothing
**Blocks:** Nothing

---

### 5.3 Define named constants for magic numbers

**Files:** `pkg/dispatcher/dispatcher.go`, `pkg/worker/worker.go`, `pkg/merge/merge.go`, `pkg/codesearch/bypass.go`, `pkg/memory/memory.go`

**Problem:** Hardcoded durations, sizes, and thresholds scattered across files:

| Value | File | Line | Should be |
|-------|------|------|-----------|
| `45 * time.Second` | dispatcher.go | 176 | `DefaultHeartbeatTimeout` |
| `10 * time.Second` | dispatcher.go | 179 | `DefaultPollInterval` |
| `60 * time.Second` | dispatcher.go | 181 | `DefaultFallbackPollInterval` |
| `10 * time.Second` | dispatcher.go | 184 | `DefaultShutdownTimeout` |
| `50 * time.Millisecond` | dispatcher.go | 1047 | `shutdownPollInterval` |
| `2 * time.Second` | worker.go | 70 | Already named |
| `500 * time.Millisecond` | worker.go | 73 | Already named |
| `100` | worker.go | 76 | Already named |
| `5 * time.Second` | merge.go | 144 | `abortTimeout` |
| `3072` | bypass.go | 12 | `maxBypassFileSize` |
| `3` | dispatcher.go | ~maxQGRetries | Already named |
| `2` | dispatcher.go | ~maxHandoffsBeforeDiagnosis | Already named |

**Design:** Define constants in the file where they're used. Use `Default` prefix for configurable values, no prefix for internal constants.

**Acceptance criteria:**
- [ ] All hardcoded durations and sizes have named constants
- [ ] `grep -rn 'time\.\(Second\|Millisecond\|Minute\)' pkg/ --include='*.go' | grep -v _test.go | grep -v const` returns only config defaults
- [ ] Comments on constants explain the choice (e.g., why 45s not 30s)

**Blocked by:** Nothing
**Blocks:** Nothing

---

## Epic 6: Test Infrastructure

Priority: **P1** — flaky tests erode trust in the entire test suite.

### 6.1 Replace time.Sleep with synchronization primitives

**Files:** `pkg/dispatcher/dispatcher_test.go` (39 instances), `pkg/worker/worker_test.go` (18 instances), `pkg/dispatcher/qg_retry_test.go` (5 instances), `pkg/dispatcher/tracking_leak_test.go` (4 instances), `pkg/merge/merge_test.go` (3 instances), `cmd/oro/start_test.go` (2 instances) — **77 total**

**Problem:** Every `time.Sleep` in a test is a flaky test waiting to happen. Under CI load, the goroutine might not finish before the sleep expires. Under light load, the sleep wastes CI time.

**Design:**

Replace each sleep with one of:

**Pattern A: Channel signal** (for "wait for goroutine to do X"):
```go
// Before:
go doThing()
time.Sleep(100 * time.Millisecond)
assert(thingDone)

// After:
done := make(chan struct{})
go func() { doThing(); close(done) }()
select {
case <-done:
case <-time.After(5 * time.Second):
    t.Fatal("timed out waiting for thing")
}
assert(thingDone)
```

**Pattern B: Polling with deadline** (for "wait for state change"):
```go
// Before:
time.Sleep(200 * time.Millisecond)
assert(state == expected)

// After:
deadline := time.After(5 * time.Second)
ticker := time.NewTicker(10 * time.Millisecond)
defer ticker.Stop()
for {
    select {
    case <-deadline:
        t.Fatalf("state never reached %v, got %v", expected, state)
    case <-ticker.C:
        if state == expected { goto done }
    }
}
done:
```

**Pattern C: Test helper** (standardize the polling pattern):
```go
func waitFor(t *testing.T, timeout time.Duration, check func() bool, msg string) {
    t.Helper()
    deadline := time.After(timeout)
    ticker := time.NewTicker(10 * time.Millisecond)
    defer ticker.Stop()
    for {
        select {
        case <-deadline:
            t.Fatalf("timeout: %s", msg)
        case <-ticker.C:
            if check() { return }
        }
    }
}
```

**Scope:** This is the largest individual bead. Decompose by file:
1. `dispatcher_test.go` (39 sleeps) — largest, do first
2. `worker_test.go` (18 sleeps)
3. `qg_retry_test.go` + `tracking_leak_test.go` (9 sleeps)
4. `merge_test.go` + `start_test.go` (5 sleeps)

**Acceptance criteria:**
- [ ] `grep -rn 'time\.Sleep' *_test.go` returns zero results
- [ ] `waitFor` helper defined in a shared test helper file
- [ ] All tests pass with `-count=10` (stress test for flakiness)
- [ ] Test suite runtime does not increase (sleeps were waste)
- [ ] Race detector passes with `-count=5`

**Blocked by:** Nothing
**Blocks:** Nothing

---

### 6.2 Add missing test coverage

**Files:** `pkg/codesearch/lang.go`, `pkg/dispatcher/exec_runner.go`, `pkg/dispatcher/qg_stuck.go`

**Problem:**
- `LangFromPath()` — public API, zero dedicated tests
- `exec_runner.go` — no test file at all (693 bytes of untested code)
- `hashQGOutput()` — core hashing function, zero unit tests

**Design:**

**LangFromPath tests:**
```go
func TestLangFromPath(t *testing.T) {
    tests := []struct {
        path string
        want Language
        ok   bool
    }{
        {"foo.go", LangGo, true},
        {"bar.py", LangPython, true},
        {"baz.rs", LangRust, true},
        {"no-ext", Language(""), false},
        {"", Language(""), false},
        {".hidden", Language(""), false},
        {"FOO.GO", LangGo, true},    // case sensitivity
        {"dir/file.ts", LangTypeScript, true},
    }
    // ...
}
```

**hashQGOutput tests:**
```go
func TestHashQGOutput(t *testing.T) {
    tests := []struct {
        input string
        name  string
    }{
        {"", "empty"},
        {"hello", "simple"},
        {strings.Repeat("x", 10000), "large"},
    }
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            h1 := hashQGOutput(tt.input)
            h2 := hashQGOutput(tt.input)
            assert.Equal(t, h1, h2, "deterministic")
            assert.NotEmpty(t, h1)
        })
    }
    // Different inputs produce different hashes
    assert.NotEqual(t, hashQGOutput("a"), hashQGOutput("b"))
}
```

**exec_runner.go tests:** Test the `GitRunner` concrete implementation with a real git repo (using `t.TempDir()`).

**Acceptance criteria:**
- [ ] `LangFromPath` has dedicated test with 8+ cases including edge cases
- [ ] `hashQGOutput` has unit test covering empty, simple, large inputs + determinism
- [ ] `exec_runner.go` has test file with at least 3 test cases
- [ ] `go test -cover ./pkg/codesearch/...` shows lang.go covered
- [ ] `go test -cover ./pkg/dispatcher/...` shows qg_stuck.go and exec_runner.go covered

**Blocked by:** Nothing
**Blocks:** Nothing

---

## Epic 7: Code Hygiene

Priority: **P3** — cleanup that reduces cognitive load but doesn't fix bugs.

### 7.1 Unexport internal types

**Files:** `pkg/dispatcher/dispatcher.go`, `pkg/worker/worker.go`, `pkg/memory/memory.go`

**Problem:** Internal implementation details are exported:
- `WorkerState` and its constants (only used within dispatcher)
- `Thresholds` struct and `LoadThresholds` (only used within worker)
- `DedupJaccardThreshold` (implementation constant)

**Design:** Audit all exported symbols. Unexport anything used only within its package. Mark test-only exports with build tags if needed.

**Acceptance criteria:**
- [ ] `WorkerState` unexported (or moved to protocol if external consumers exist)
- [ ] `Thresholds` and `LoadThresholds` unexported
- [ ] `DedupJaccardThreshold` unexported
- [ ] All tests pass (may need `_test.go` in same package for access)

**Blocked by:** 4.3 (move wire types first, then unexport internals)
**Blocks:** Nothing

---

### 7.2 Consolidate magic strings into constants

**Files:** All packages

**Problem:** Repeated string literals: `"claude-opus-4-6"`, `"agent/"`, `".oro"`, `".worktrees"`, `".beads"`, `"worker-"`.

**Design:** Define in `pkg/protocol` or the consuming package:

```go
const (
    WorktreeDir = ".worktrees"
    OroDir      = ".oro"
    BeadsDir    = ".beads"
    BranchPrefix = "agent/"
)
```

Model strings already have constants (`ModelOpus`, `ModelSonnet`) — ensure they're used everywhere.

**Acceptance criteria:**
- [ ] `grep -rn '\.worktrees' pkg/ cmd/ --include='*.go' | grep -v const | grep -v _test.go` returns zero results
- [ ] Same for `.oro`, `.beads`, `agent/`
- [ ] Model string constants used everywhere (no hardcoded model IDs)

**Blocked by:** Nothing
**Blocks:** Nothing

---

## Dependency Graph

```
Epic 1: Concurrency (P0)
  1.1 Reservation pattern ──────────────┐
  1.2 Merge defer (independent)         │
  1.3 WaitGroup ◄───────────────────────┤
  1.4 Timer leak (independent)          │
  1.5 Shutdown goroutine ◄──────────────┘ (needs 1.3)

Epic 2: Security (P1)
  All items independent — can parallelize fully

Epic 3: Lifecycle (P0)
  3.1 Worktree cleanup (independent)
  3.2 WAL mode (independent)
  3.3 Stale socket (independent)
  3.4 Subprocess health (independent)
  3.5 Shutdown timeout ◄─── 1.3 (needs WaitGroup)
  3.6 Dispatcher buffering (independent)
  3.7 Escalation verification (independent)

Epic 4: Decomposition (P2) ◄─── Epic 1 (must land concurrency fixes first)
  4.1 WorkerPool extraction
  4.2 Interface rename (independent)
  4.3 Move wire types (independent)

Epic 5: Error Handling (P2)
  All items independent — can parallelize fully

Epic 6: Testing (P1)
  6.1 Replace sleeps ◄─── should be done AFTER Epic 1 (concurrency changes may invalidate test patterns)
  6.2 Missing coverage (independent)

Epic 7: Hygiene (P3) ◄─── 4.3 (move types before unexporting)
  7.1 Unexport types ◄─── 4.3
  7.2 Magic strings (independent)
```

---

## Execution Order

**Phase 1 — Stop the bleeding (P0):**
1. 1.1 Reservation pattern (unblocks 1.3, 1.5)
2. 1.2 Merge defer (parallel with 1.1)
3. 1.4 Timer leak (parallel with 1.1)
4. 3.1 Worktree cleanup (parallel with 1.1)
5. 3.2 WAL mode (parallel with 1.1)
6. 3.3 Stale socket (parallel with 1.1)
7. 3.4 Subprocess health (parallel with 1.1)
8. 1.3 WaitGroup (after 1.1)
9. 1.5 Shutdown goroutine (after 1.3)
10. 3.5 Shutdown timeout (after 1.3)

**Phase 2 — Defense in depth (P1):**
11. 2.1-2.7 Security items (all parallel)
12. 3.6 Dispatcher buffering
13. 3.7 Escalation verification
14. 6.1 Replace test sleeps (sub-beads by file)
15. 6.2 Missing test coverage

**Phase 3 — Structural improvement (P2):**
16. 5.1 Error wrapping
17. 5.2 Custom error types
18. 5.3 Named constants
19. 4.2 Interface rename
20. 4.3 Move wire types
21. 4.1 WorkerPool extraction (largest item — after everything else stabilizes)

**Phase 4 — Polish (P3):**
22. 7.1 Unexport types (after 4.3)
23. 7.2 Magic strings

---

## Risk Assessment

| Risk | Likelihood | Impact | Mitigation |
|------|-----------|--------|------------|
| Reservation pattern (1.1) introduces new state machine bugs | Medium | High | Extensive test coverage, race detector, staged rollout |
| WaitGroup (1.3) changes shutdown timing, breaks tests | Medium | Medium | Run tests with `-count=10` before merging |
| WorkerPool extraction (4.1) introduces regression | Medium | High | All existing tests must pass unchanged — no test modifications allowed during refactor |
| Sleep replacement (6.1) masks real timing bugs | Low | Medium | Review each replacement carefully — some sleeps exist to test actual timing behavior |
| Security hardening (Epic 2) breaks existing CLI/scripts | Low | Low | All changes backwards-compatible — only adds validation, doesn't change happy path |

---

## Metrics

Track these before/after hardening:

| Metric | Current | Target |
|--------|---------|--------|
| Race detector failures (`go test -race -count=5`) | 0 (believed) | 0 (proven) |
| `time.Sleep` in tests | 77 | 0 |
| Bare `return err` in production code | ~40 | 0 |
| Dispatcher struct fields | 23 | ≤12 (after decomposition) |
| Dispatcher mutex lock sites | 43 | ≤20 (after decomposition) |
| `go vet` + `staticcheck` warnings | Unknown (staticcheck broken) | 0 |
| Test coverage (line) | Unknown | ≥80% per package |
| Named constants for durations/sizes | ~8 | All |
| `time.After` in production code | 1 | 0 |
