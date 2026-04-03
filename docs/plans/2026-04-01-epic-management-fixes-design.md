# Epic Bead Management Fixes

**Date:** 2026-04-01
**Status:** Revised — adversarial review FAIL, 3 critical issues fixed below.

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

**Map lifecycle (from adversarial review):** `processedExternalClose` must be added to `BeadTracker` struct, included in `deleteOrphanedTracking` and `allTrackingKeys`, and initialized in `New()`. Without this, the map grows without bound in long-running dispatchers.

### FM3: Assignment DB Record Orphaned on Worker Delete (MEDIUM)

**Location:** `dispatcher.go:assignBead` (line ~3035-3050)

**Problem:** In `assignBead`, `createAssignment()` creates a DB record at line ~2984. If `sendToWorker` fails at line ~3021, the worker is deleted and worktree removed, but `completeAssignment()` is never called. The DB record is orphaned. Next worker for this bead sees a stale active assignment.

**Note (from adversarial review):** The original spec targeted `handleClosedAssignment`, which already handles this correctly — `mergeAndComplete` calls `completeAssignment`. The real orphan is in `assignBead`'s sendToWorker failure path.

**Fix:** Call `d.completeAssignment(ctx, beadID)` in `assignBead`'s `sendToWorker` error path (line ~3035-3050):

```go
// In assignBead, after sendToWorker fails:
d.completeAssignment(ctx, beadID) // Clean up orphaned assignment DB record
```

### FM4: Epic FF-Merge Failure Not Escalated (CRITICAL)

**Location:** `dispatcher.go:completeEpicClose` (line ~1711-1712), `mergeAndComplete` (line ~1427), `checkEpicAssignable` (line ~3076-3081)

**Problem:** If `ffMergeEpicBranch` fails, `completeEpicClose` returns without closing the epic AND without escalating. The epic stays open, all children are already closed, so `autoCloseEpicIfComplete` retries every cycle → infinite retry loop with no visibility.

**Additional problem (from adversarial review):** `checkEpicAssignable` at line ~3076-3081 calls `beads.Close()` directly WITHOUT running `ffMergeEpicBranch`. If this path fires before `autoCloseEpicIfComplete`, the epic is closed without merging its branch — commits are silently lost.

**Fix (3 parts):**

1. In `completeEpicClose`, escalate on merge failure and set flag:
```go
if err := d.ffMergeEpicBranch(ctx, epicID, workerID, targetBranch); err != nil {
    d.escalate(ctx, protocol.FormatEscalation(protocol.EscStuck, epicID,
        "epic ff-merge failed", err.Error()), epicID, workerID)
    d.mu.Lock()
    d.epicMergeFailed[epicID] = true
    d.mu.Unlock()
    return
}
```

1. **CRITICAL:** Clear `epicMergeFailed[epicID]` in `mergeAndComplete` BEFORE calling `autoCloseEpicIfComplete`, not inside it. Otherwise, the rebase fix bead's completion is blocked by the very flag it should clear (deadlock).
```go
// In mergeAndComplete, before autoCloseEpicIfComplete:
d.mu.Lock()
delete(d.epicMergeFailed, epicID)
d.mu.Unlock()
d.autoCloseEpicIfComplete(ctx, epicID)
```

1. In `checkEpicAssignable`, replace the direct `beads.Close()` call with delegation to `completeEpicClose` (or at minimum `ffMergeEpicBranch` + close). The current direct-close path loses commits.

**Map lifecycle:** `epicMergeFailed` must be added to `BeadTracker` struct, included in `deleteOrphanedTracking` and `allTrackingKeys`, and initialized in `New()`.

### FM5: Epic Branch Missing Escalates Prematurely (LOW-MEDIUM)

**Location:** `dispatcher.go:assignBead` (line ~2928-2942)

**Problem:** If a child bead is assigned before its parent epic has been assigned (and created the epic branch), the dispatcher escalates as STUCK_WORKER. But the epic is just waiting in the ready queue — not actually stuck.

**Fix:** When epic branch is missing, check if the epic is still in "open" status (not yet assigned). If so, skip this child silently (return without escalating) and let the epic be assigned first. Only escalate if the epic is in_progress (branch should exist but doesn't).

**Note (from adversarial review):** Handle `Show` errors by retrying (return without escalating), not by falling through to escalation. Transient DB errors should not cause false STUCK_WORKER escalation — same class of bug as FM6.

```go
if !exists {
    epicDetail, err := d.beads.Show(ctx, resolvedEpicID)
    if err != nil {
        // Transient error — skip, retry next cycle (don't escalate)
        d.logEvent(ctx, "epic_show_error", ...)
        return
    }
    if epicDetail.Status == "open" {
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

**Scope:** `dispatcher.go:handleClosedAssignment` (~line 2620), `bead_tracker.go` (add to BeadTracker, deleteOrphanedTracking, allTrackingKeys). Add `processedExternalClose` map, check before processing, clear on reassignment. Initialize in `New()`.

**Test:** `TestExternalClose_NoReEntry` — close bead externally, verify handleClosedAssignment runs once, not twice on subsequent cycles.

### Bead 3: Clean assignment DB on worker delete (FM3)

**Scope:** `dispatcher.go:assignBead` (~line 3035-3050). Add `completeAssignment` call in sendToWorker error path within `assignBead` (NOT handleClosedAssignment — that path already works).

**Test:** `TestAssignmentCleanedOnWorkerDelete` — simulate sendToWorker failure in assignBead, verify assignment DB record cleared.

### Bead 4: Escalate on epic FF-merge failure + prevent infinite retry (FM4)

**Scope:** `dispatcher.go:completeEpicClose` (~line 1711), `mergeAndComplete` (~line 1427), `checkEpicAssignable` (~line 3076-3081), `bead_tracker.go`. Three parts: (a) escalation + epicMergeFailed flag in completeEpicClose, (b) clear flag in mergeAndComplete BEFORE autoCloseEpicIfComplete (prevents deadlock), (c) replace direct beads.Close() in checkEpicAssignable with delegation to completeEpicClose.

**Test:** `TestEpicFFMergeFailure_EscalatesAndBlocks` — mock merge failure, verify escalation sent, epic not retried. Also: `TestEpicMergeFailedClearedOnChildComplete` — verify rebase fix bead completion clears the flag and epic proceeds.

### Bead 5: Skip child assignment when epic branch pending (FM5)

**Scope:** `dispatcher.go:assignBead` (~line 2928-2942). Check epic status before escalating. Handle Show() errors by skipping (retry next cycle), not escalating.

**Test:** `TestChildAssignment_SkipsWhenEpicNotAssigned` — epic in "open" status, child tries to assign, verify no escalation, child skipped silently. Also: `TestChildAssignment_ShowError_NoEscalation` — transient Show error, verify no false STUCK_WORKER escalation.

### Bead 6: Retry on transient error in checkEpicAssignable (FM6)

**Scope:** `dispatcher.go:checkEpicAssignable` (~line 3062-3074). Change error return to `(false, false)`.

**Test:** `TestCheckEpicAssignable_RetriesOnError` — mock HasChildren error, verify returns (false, false) not (false, true).

## Dependencies

Beads 1-6 are mostly independent. Minor note: Bead 4 touches `bead_tracker.go` for the `epicMergeFailed` map, and Bead 2 touches `bead_tracker.go` for `processedExternalClose`. If both run in parallel, the second to merge will need a trivial conflict resolution on that file. Otherwise, no cross-bead dependencies.

## What We're NOT Doing

- No refactor of the epic assignment model (would require rearchitecting the assignment loop)
- No priority boost for epics over children (correctness fix, not scheduling optimization)
- No distributed locking (single-dispatcher model means mutex is sufficient)
- No changes to bd CLI or BeadSource interface
