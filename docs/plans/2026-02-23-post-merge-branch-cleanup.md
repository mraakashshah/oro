# Post-Merge Agent Branch Cleanup

**Date:** 2026-02-23
**Status:** Draft
**Bead:** TBD

## Problem

After a successful merge, agent worktree directories are removed but agent branches (`agent/<beadID>`) are never deleted. These accumulate over time, polluting `git branch` output and causing `git worktree list` to show stale entries.

Currently, branch cleanup only happens in two places:
1. **`oro cleanup`** (manual command) — deletes all `agent/*` branches
2. **`pruneStale()`** in `worktree_manager.go` — deletes a branch during `Create()` crash recovery when the branch "already exists"

Neither runs as part of the normal post-merge lifecycle.

## Root Cause

Two merge paths exist, both missing branch deletion:

1. **Normal merge** (`merge.go:worktreeRemoveAndFFMerge`): removes worktree, ff-merges branch, gets SHA — but never deletes the branch afterward.
2. **Already-merged early return** (`merge.go:104-107`): detects branch is fully merged, returns immediately. Worktree removal happens later in the dispatcher, but branch is never deleted.

## Design

### Approach: Add `DeleteBranch` to `WorktreeManager` interface

Add branch deletion to the dispatcher's post-merge cleanup, not to `merge.go`. Rationale:

- **Single change point** — the dispatcher's `removeWorktreeAndClearTracking()` runs after *both* merge paths (normal and already-merged), so one addition covers both.
- **Separation of concerns** — `merge.go` is a library focused on rebase+merge. Lifecycle cleanup (worktree removal, branch deletion) is the dispatcher's job.
- **The dispatcher already knows the beadID** — branch name is `protocol.BranchPrefix + beadID`, trivially derivable.

### Changes

#### 1. Extend `WorktreeManager` interface (`pkg/dispatcher/dispatcher.go`)

```go
type WorktreeManager interface {
    Create(ctx context.Context, beadID string) (path string, branch string, err error)
    Remove(ctx context.Context, path string) error
    Prune(ctx context.Context) error
    DeleteBranch(ctx context.Context, branch string) error  // NEW
}
```

#### 2. Implement `DeleteBranch` on `GitWorktreeManager` (`pkg/dispatcher/worktree_manager.go`)

```go
func (g *GitWorktreeManager) DeleteBranch(ctx context.Context, branch string) error {
    _, err := g.runner.Run(ctx, "git", "-C", g.repoRoot, "branch", "-d", branch)
    if err != nil {
        return fmt.Errorf("branch delete %s: %w", branch, err)
    }
    return nil
}
```

Uses `-d` (lowercase) not `-D`: the branch is guaranteed to be merged at this point. If it's somehow not merged, `-d` will refuse — a safety net.

#### 3. Call `DeleteBranch` in `removeWorktreeAndClearTracking` (`pkg/dispatcher/dispatcher.go`)

```go
func (d *Dispatcher) removeWorktreeAndClearTracking(ctx context.Context, beadID, workerID, worktree string) {
    if err := d.worktrees.Remove(ctx, worktree); err != nil {
        _ = d.logEvent(ctx, "worktree_cleanup_failed", "dispatcher", beadID, workerID, err.Error())
    } else {
        d.mu.Lock()
        delete(d.worktreeByBead, beadID)
        d.mu.Unlock()
    }

    // Best-effort branch cleanup — branch was merged, safe to delete.
    branch := protocol.BranchPrefix + beadID
    if err := d.worktrees.DeleteBranch(ctx, branch); err != nil {
        _ = d.logEvent(ctx, "branch_cleanup_failed", "dispatcher", beadID, workerID, err.Error())
    }
}
```

Branch deletion runs regardless of whether worktree removal succeeded — even if the worktree is stuck, we can still try to delete the branch (will fail if worktree still references it, which is fine — best-effort).

### Edge Cases

| Scenario | Behavior |
|---|---|
| Branch already deleted (double call) | `git branch -d` returns error, swallowed by best-effort logging |
| Branch not merged (shouldn't happen) | `-d` refuses to delete, error logged — safety net |
| Worktree still references branch | `git branch -d` fails because branch is checked out, error logged |
| Already-merged early-return path | Dispatcher calls `removeWorktreeAndClearTracking` → worktree removed → branch deleted |
| Merge conflict (no merge happens) | `removeWorktreeAndClearTracking` is not called, branch preserved for retry |

### What This Does NOT Change

- **`merge.go`** — no changes. Stays focused on rebase+merge.
- **`oro cleanup`** — still works as a manual nuclear option.
- **`pruneStale()`** — still handles crash recovery during Create().
- **`oro work`** (standalone worker) — uses the same merge coordinator but has its own cleanup path; out of scope for this change.

### Test Plan

1. **Unit test for `GitWorktreeManager.DeleteBranch`** — mock runner, verify `git branch -d <branch>` is called with correct args.
2. **Unit test for `removeWorktreeAndClearTracking` with branch cleanup** — verify:
   - Both `Remove` and `DeleteBranch` are called, in that order.
   - Remove fails + DeleteBranch succeeds: both logged independently.
   - Both Remove and DeleteBranch fail: both failures logged independently.
   - Branch deletion failure doesn't affect worktree cleanup.
3. **Update all 3 mock `WorktreeManager` implementations** to add no-op `DeleteBranch`.
4. **Existing tests continue to pass** — the new method is additive.

### Follow-Up (out of scope)

- **`oro work` branch leak** — `cmd/oro/cmd_work.go` calls `Remove()` after merge but never deletes the branch. Same bug, different code path. File a follow-up bead.

## Files Modified

- `pkg/dispatcher/dispatcher.go` — extend interface + update `removeWorktreeAndClearTracking`
- `pkg/dispatcher/worktree_manager.go` — add `DeleteBranch` method
- `pkg/dispatcher/worktree_manager_test.go` — test `DeleteBranch`
- `pkg/dispatcher/dispatcher_test.go` — update mock, test branch cleanup in `removeWorktreeAndClearTracking`
- `cmd/oro/cmd_work_execute_test.go` — update mock with no-op `DeleteBranch`
- `pkg/integration/dispatcher_worker_test.go` — update mock with no-op `DeleteBranch`
