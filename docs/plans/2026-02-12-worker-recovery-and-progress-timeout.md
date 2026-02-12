# Spec: Worker Recovery After Review Rejection & Progress Timeout

Date: 2026-02-12

## Problem Statement

Workers produce code but never land it on main. Two root causes:

### 1. Workers become zombies after review rejection escalation

After `maxReviewRejections` (2), `handleReviewRejection` calls `clearBeadTracking(beadID)` which removes the bead from all dispatcher tracking maps. But the worker still has `w.beadID` set and remains in `WorkerBusy` state. Result:

- Worker heartbeats forever with a beadID the dispatcher no longer tracks
- `tryAssign()` won't assign new work because worker isn't `WorkerIdle`
- `checkHeartbeats()` will eventually kill it (45s timeout after last heartbeat), but if heartbeats keep flowing, it lives forever
- Worker burns API tokens doing nothing

**Secondary bugs in the rejection retry path (pre-escalation):**
- `pendingQGOutput` on worker is never cleared on rejection reassign (`worker.go:354` resets `sessionText` but not `pendingQGOutput`)
- No `MemoryContext` sent with rejection reassign — worker retries blind without knowing what the reviewer said previously
- No `Attempt` counter on rejection reassign — worker doesn't know it's on retry 2 of 2

### 2. No progress timeout — only liveness timeout

The dispatcher has a 45s heartbeat timeout (`checkHeartbeats` in `worker_pool.go:156-188`), but:

- It only fires when heartbeats STOP (liveness)
- Workers heartbeat every 10s from `watchContext()` regardless of whether they're doing useful work
- `checkHeartbeats` explicitly skips `WorkerIdle` and `WorkerReserved` states (line 162)
- There is no tracking of "last meaningful progress" (last commit, last state transition, last QG attempt)
- A worker can heartbeat for 10 hours after its last commit and the dispatcher thinks it's healthy

The only "stuck" detection is `qg_stuck.go` which catches 3 identical QG outputs — not general staleness.

## Design

### Fix 1: Clean up worker state on review escalation

In `handleReviewRejection` (dispatcher.go:1066-1106), when `count > maxReviewRejections`:

1. **Before** calling `clearBeadTracking()`, transition the worker to `WorkerIdle` and clear its `beadID`
2. This makes the worker eligible for new assignment via `tryAssign()`
3. The worker process should be killed and restarted fresh (not left running with stale context)

Implementation:
```
// After escalation, release the worker so it can take new work.
d.mu.Lock()
if w, ok := d.workers[workerID]; ok {
    w.state = protocol.WorkerIdle
    w.beadID = ""
    w.worktree = ""
}
d.mu.Unlock()
```

Then send a KILL or RESET message to the worker so it stops its current subprocess.

### Fix 2: Clear pendingQGOutput on rejection reassign

In `worker.go handleAssign()` (line 342), add:
```
w.pendingQGOutput = ""
```

This prevents stale QG output from being sent on the next approval cycle.

### Fix 3: Include MemoryContext and Attempt in rejection reassign

In `handleReviewRejection` (dispatcher.go:1096-1103), the ASSIGN message sent back to the worker should include:
- `MemoryContext` with the reviewer's feedback from previous rejections
- `Attempt` counter so the worker knows which retry it's on

### Fix 4: Add progress timeout to dispatcher

Add a `lastProgress` timestamp to the worker struct in the dispatcher, updated on meaningful state transitions:
- Worker sends `DONE` or `READY_FOR_REVIEW`
- Worker sends a `STATUS` with `state: "running"` (first one after assignment)
- QG pass or fail (means worker at least ran QG)

Add a `ProgressTimeout` config (default 15 minutes). In `checkHeartbeats`, add a second check:

```
// Existing: liveness timeout (no heartbeats at all)
if w.state != WorkerIdle && w.state != WorkerReserved &&
    now.Sub(w.lastSeen) > d.cfg.HeartbeatTimeout {
    dead = append(dead, id)
}

// NEW: progress timeout (heartbeating but no progress)
if w.state == WorkerBusy &&
    now.Sub(w.lastProgress) > d.cfg.ProgressTimeout {
    stalled = append(stalled, id)
}
```

For stalled workers: escalate to manager with `STUCK_WORKER` and kill the worker subprocess so the bead can be reassigned.

## Files to Change

| File | Change |
|------|--------|
| `pkg/dispatcher/dispatcher.go` | Fix `handleReviewRejection` escalation path: reset worker state to Idle, clear beadID. Add `MemoryContext` and `Attempt` to rejection ASSIGN. |
| `pkg/dispatcher/worker_pool.go` | Add `lastProgress` field to worker struct. Add progress timeout check in `checkHeartbeats`. |
| `pkg/dispatcher/dispatcher.go` | Update `handleReadyForReview`, `handleQGResult`, `handleStatus` to bump `lastProgress`. |
| `pkg/dispatcher/dispatcher.go` | Add `ProgressTimeout` to Config with 15m default. |
| `pkg/worker/worker.go` | Clear `pendingQGOutput` in `handleAssign`. |
| `pkg/protocol/types.go` | No changes needed (states are fine). |

## Acceptance Criteria

1. After review escalation (3rd rejection), worker transitions to Idle and gets new work within one assign cycle
2. `pendingQGOutput` is empty after rejection reassign
3. Rejection reassign includes reviewer feedback in `MemoryContext`
4. Workers idle for >15 minutes with no progress (no DONE, READY_FOR_REVIEW, or QG result) trigger STUCK_WORKER escalation
5. All existing tests pass
6. New tests cover: escalation cleanup, progress timeout, stale pendingQGOutput
