# Epic Branch Lazy Creation Design

**Date:** 2026-04-02
**Status:** Draft — needs adversarial review.

## Goal

Create `epic/<epicID>` branches lazily on first child assignment, so epic branch isolation works regardless of how beads are created (worker decomposition, beadcraft, bd create, architect).

## Context: The Gap

Epic branch isolation requires an `epic/<epicID>` branch to exist before children can branch from it. Currently, this branch is **never explicitly created** anywhere in the codebase:

- When an epic is assigned for decomposition (`isEpicDecomp=true`), the worker gets a worktree on `agent/<epicID>` branched from `main` — no `epic/<epicID>` branch is created.
- When children are later assigned, `resolveEpicBranch()` returns `("epic/<epicID>", epicID, nil)` and `BranchExists()` returns false.
- `handleEpicBranchMissing()` either retries (epic still open) or escalates (epic in_progress) — but never creates the branch.
- Children fall back to branching from `main`, bypassing isolation entirely.

This works accidentally when workers decompose epics (the worktree implicitly creates branches), but fails for manually-created beads.

## Design

### What Changes

In `assignBead`, between the `BranchExists` check (line ~3000) and the `worktrees.Create` call (line ~3029), add lazy branch creation:

```go
// After resolveEpicBranch returns baseBranch = "epic/<epicID>"
// and BranchExists returns false:

if !exists && baseBranch != d.cfg.DefaultBranch {
    if err := d.worktrees.CreateBranch(ctx, baseBranch, d.cfg.DefaultBranch); err != nil {
        // Branch may already exist (race with another child) — check
        if exists2, _ := d.worktrees.BranchExists(ctx, baseBranch); exists2 {
            // Race resolved — branch was created by concurrent assignment
        } else {
            // Genuine failure
            d.logEvent(ctx, "epic_branch_create_failed", "dispatcher", beadID, "",
                fmt.Sprintf(`{"branch":%q,"error":%q}`, baseBranch, err.Error()))
            d.escalate(ctx, ..., beadID, "")
            return
        }
    }
    d.logEvent(ctx, "epic_branch_created", "dispatcher", beadID, "",
        fmt.Sprintf(`{"branch":%q,"from":%q}`, baseBranch, d.cfg.DefaultBranch))
}

// Proceed with worktrees.Create(ctx, beadID, baseBranch) as before
```

### WorktreeManager Interface Extension

Add one method:

```go
type WorktreeManager interface {
    // ...existing methods...
    CreateBranch(ctx context.Context, name string, from string) error
}
```

**GitWorktreeManager implementation:**

```go
func (g *GitWorktreeManager) CreateBranch(ctx context.Context, name, from string) error {
    cmd := exec.CommandContext(ctx, "git", "branch", name, from)
    cmd.Dir = g.repoRoot
    if out, err := cmd.CombinedOutput(); err != nil {
        return fmt.Errorf("git branch %s %s: %s: %w", name, from, out, err)
    }
    return nil
}
```

### Race Condition Handling

Two children assigned simultaneously for the same epic with no branch:

1. Both call `BranchExists` → both get `false`
2. Both call `CreateBranch("epic/X", "main")`
3. First succeeds, second gets `git branch: fatal: a branch named 'epic/X' already exists`
4. Second checks `BranchExists` again → `true` → proceeds normally

This is idempotent. The error from `git branch` when branch already exists is expected and handled by the re-check. No mutex needed.

### Mock Updates

`mockWorktreeManager` in test files needs `CreateBranch` method. Pattern: add `createBranchErr error` field, return it.

### Files Modified

- `pkg/dispatcher/dispatcher.go` — `assignBead` (~line 3000-3029): add lazy creation between BranchExists check and worktrees.Create. Remove escalation from handleEpicBranchMissing for the "branch missing but can be created" case.
- `pkg/dispatcher/worktree_manager.go` — add `CreateBranch(ctx, name, from string) error` to `GitWorktreeManager`
- `pkg/dispatcher/dispatcher.go` — `WorktreeManager` interface (~line 86): add `CreateBranch`

### Files Created

- `pkg/dispatcher/epic_branch_lazy_test.go` — tests for lazy creation

### Test Files Needing Mock Update

All files with `mockWorktreeManager` need `CreateBranch` method added:
- `pkg/dispatcher/dispatcher_test.go`
- `pkg/dispatcher/merge_test.go`
- Any other test files with mock (grep for `mockWorktreeManager`)

### What We're NOT Doing

- Not creating epic branches eagerly on epic creation (unnecessary until a child is assigned)
- Not changing the merge path (children already merge to `targetBranch` which is the epic branch)
- Not adding a mutex for branch creation (git's own error + re-check is sufficient)
- Not changing `resolveEpicBranch` (it correctly returns the epic branch name)
- Not changing `ffMergeEpicBranch` (it already handles branch-exists check)

## Risk

| Risk | Severity | Mitigation |
|------|----------|------------|
| `git branch` fails for non-race reason (permissions, disk) | Low | Escalate as stuck, don't assign child |
| Mock update touches many test files | Low | Mechanical — add no-op method to mock struct |
| CreateBranch called every assignment cycle until branch exists | Low | BranchExists is checked first — CreateBranch only called when false |
