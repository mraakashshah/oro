# Epic Bead Management Fixes

**Date:** 2026-04-01
**Status:** Draft — needs adversarial review before decomposition.

## Goal

Fix 6 failure modes in the dispatcher's epic bead management that cause stuck workers, zombie beads, infinite retry loops, and false escalations. All discovered during a live swarm session.

## Failure Modes (Observed)

### FM1: Type Promotion Breaks handleDone (CRITICAL)

**Location:** `dispatcher.go:handleDone` (line ~1062)

**Problem:** Worker promotes a task to epic (`bd update --type epic`) mid-flight. `isEpicDecomp` is captured once at assignment time and never revalidated. When the worker finishes, `handleDone` sees `isEpicDecomp=false` and merges the bead like a task. The epic stays open forever because `autoCloseEpicIfComplete` expects children but there was no decomposition.

**Fix:** In `handleDone`, re-fetch `bead.Type` from BeadSource before deciding merge-vs-decomp. If type changed from task→epic, skip merge, clean up worktree, and let the dispatcher re-assign the epic for decomposition.

```go
// In handleDone, after capturing isEpicDecomp from worker state:
freshDetail, err := d.beads.Show(ctx, beadID)
if err == nil && freshDetail.Type == "epic" && !isEpicDecomp {
    // Type changed mid-flight — skip merge, let dispatcher re-assign
    d.logEvent(ctx, "type_changed_to_epic", workerID, beadID, ...)
    d.removeWorktreeAndClearTracking(ctx, beadID, workerID, worktree)
    return
}
```

### FM2: handleClosedAssignment Race (MEDIUM)

**Location:** `dispatcher.go:handleClosedAssignment` (line ~2620)

**Problem:** `mergingBeads` guard prevents duplicate merges while in-flight. But after merge completes, the guard clears. If `checkClosedBeadAssignments` runs again before the bead is reassigned, it calls `mergeAndComplete` a second time.

**Fix:** Add a `processedExternalClose` map on the Dispatcher struct. In `handleClosedAssignment`, check this map before processing. Add bead to map after processing. Clear entry when bead is reassigned or worktree removed.

```go
type Dispatcher struct {
    // ...existing fields...
    processedExternalClose map[string]bool // beadID → already handled
}
```

### FM3: Assignment DB Record Orphaned on Worker Delete (MEDIUM)

**Location:** `dispatcher.go:handleClosedAssignment` (line ~2651-2655)

**Problem:** If `sendToWorker` fails (broken pipe), the worker is deleted from `d.workers` but the assignment DB record is not cleaned up. Next worker connection sees stale assignment.

**Fix:** Call `d.completeAssignment(ctx, beadID)` in the `sendToWorker` error path:

```go
if err := d.sendToWorker(w, protocol.Message{Type: protocol.MsgShutdown}); err != nil {
    _ = w.conn.Close()
    delete(d.workers, w.id)
    d.completeAssignment(ctx, beadID) // Clean up assignment DB
}
```

### FM4: Epic FF-Merge Failure Not Escalated (CRITICAL)

**Location:** `dispatcher.go:completeEpicClose` (line ~1711-1712)

**Problem:** If `ffMergeEpicBranch` fails, `completeEpicClose` returns without closing the epic AND without escalating. The epic stays open, all children are already closed, so `autoCloseEpicIfComplete` retries every cycle → infinite retry loop with no visibility.

**Fix:** Add escalation and set epic to a blocked-like state to prevent infinite retry:

```go
if err := d.ffMergeEpicBranch(ctx, epicID, workerID, targetBranch); err != nil {
    d.escalate(ctx, protocol.FormatEscalation(protocol.EscStuck, epicID,
        "epic ff-merge failed", err.Error()), epicID, workerID)
    // Track failed epic to prevent retry until fix bead lands
    d.mu.Lock()
    d.epicMergeFailed[epicID] = true
    d.mu.Unlock()
    return
}
```

In `autoCloseEpicIfComplete`, check `epicMergeFailed[epicID]` and skip if true. Clear the flag when a child bead (the rebase fix bead) completes.

### FM5: Epic Branch Missing Escalates Prematurely (LOW-MEDIUM)

**Location:** `dispatcher.go:assignBead` (line ~2928-2942)

**Problem:** If a child bead is assigned before its parent epic has been assigned (and created the epic branch), the dispatcher escalates as STUCK_WORKER. But the epic is just waiting in the ready queue — not actually stuck.

**Fix:** When epic branch is missing, check if the epic is still in "open" status (not yet assigned). If so, skip this child silently (return without escalating) and let the epic be assigned first. Only escalate if the epic is in_progress (branch should exist but doesn't).

```go
if !exists {
    // Check if epic is just not assigned yet
    epicDetail, _ := d.beads.Show(ctx, resolvedEpicID)
    if epicDetail != nil && epicDetail.Status == "open" {
        // Epic not assigned yet — skip child, try later
        d.logEvent(ctx, "epic_branch_pending", ...)
        return
    }
    // Epic is in_progress but branch missing — genuine problem
    d.escalate(ctx, ...)
}
```

### FM6: checkEpicAssignable Auto-Closes on Transient Error (LOW)

**Location:** `dispatcher.go:checkEpicAssignable` (line ~3062-3074)

**Problem:** If `HasChildren` or `AllChildrenClosed` returns a transient error (db timeout, dolt hiccup), the function returns `(false, true)` — skip and don't retry. The epic is never assigned.

**Fix:** On error, return `(false, false)` to allow retry on next cycle instead of permanently skipping.

## Implementation Plan

### Bead 1: Re-check bead type in handleDone (FM1)

**Scope:** `dispatcher.go:handleDone` (~line 1062). Add `beads.Show()` call, check type changed, skip merge if epic.

**Test:** `TestHandleDone_TypeChangedToEpic` — worker assigned as task, bead type changed to epic mid-flight, verify merge skipped and worktree cleaned.

### Bead 2: Guard handleClosedAssignment against re-entry (FM2)

**Scope:** `dispatcher.go:handleClosedAssignment` (~line 2620). Add `processedExternalClose` map, check before processing, clear on reassignment.

**Test:** `TestExternalClose_NoReEntry` — close bead externally, verify handleClosedAssignment runs once, not twice on subsequent cycles.

### Bead 3: Clean assignment DB on worker delete (FM3)

**Scope:** `dispatcher.go:handleClosedAssignment` (~line 2651-2655). Add `completeAssignment` call in sendToWorker error path.

**Test:** `TestAssignmentCleanedOnWorkerDelete` — simulate sendToWorker failure, verify assignment DB record cleared.

### Bead 4: Escalate on epic FF-merge failure + prevent infinite retry (FM4)

**Scope:** `dispatcher.go:completeEpicClose` (~line 1711), `autoCloseEpicIfComplete`. Add escalation, add `epicMergeFailed` map, check in auto-close.

**Test:** `TestEpicFFMergeFailure_EscalatesAndBlocks` — mock merge failure, verify escalation sent and epic not retried until fix bead clears the flag.

### Bead 5: Skip child assignment when epic branch pending (FM5)

**Scope:** `dispatcher.go:assignBead` (~line 2928-2942). Check epic status before escalating.

**Test:** `TestChildAssignment_SkipsWhenEpicNotAssigned` — epic in "open" status, child tries to assign, verify no escalation, child skipped silently.

### Bead 6: Retry on transient error in checkEpicAssignable (FM6)

**Scope:** `dispatcher.go:checkEpicAssignable` (~line 3062-3074). Change error return to `(false, false)`.

**Test:** `TestCheckEpicAssignable_RetriesOnError` — mock HasChildren error, verify returns (false, false) not (false, true).

## Dependencies

Beads 1-6 are independent — each fixes a different code path. They can all be executed in parallel. No cross-bead dependencies.

## What We're NOT Doing

- No refactor of the epic assignment model (would require rearchitecting the assignment loop)
- No priority boost for epics over children (correctness fix, not scheduling optimization)
- No distributed locking (single-dispatcher model means mutex is sufficient)
- No changes to bd CLI or BeadSource interface
