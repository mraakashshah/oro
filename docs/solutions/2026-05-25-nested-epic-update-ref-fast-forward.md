# Nested Epic Ref Updates Must Be Fast-Forward Only

**Date:** 2026-05-25
**Component:** dispatcher epic branch integration
**Severity:** high

## Symptom

While monitoring the factory, task `oro-czec` closed with
`Merged: b41bb5b59161d0729f4c54bbb7034a127d583a01`, but `main` remained at
`b1ed580ab47cd8e5f248bdb475bd2feefcfaf553`. The commit was only reachable
from `epic/oro-tvza`.

`git diff main..epic/oro-tvza` showed that merging that stale epic branch would
remove the current mutation-testing opt-in fix. The branch graph showed
`epic/oro-tvza` had child commits on top of an older base while `main` had
advanced independently.

## Investigation

The child task was under nested epic `oro-tvza`, so merging to `epic/oro-tvza`
was expected. The unexpected part was how nested epic branches advance their
parent branch.

`pkg/dispatcher/dispatcher.go:2826` routed non-HEAD target branches through
`GitWorktreeManager.UpdateBranchRef`. That method used `git update-ref`
directly, without first proving that the target branch was an ancestor of the
source branch.

## Root Cause

`git update-ref` is not a merge operation. It will move `refs/heads/<target>` to
the source commit even when the source does not contain the target branch's
current commits. In nested epic integration, that can discard parent epic branch
history or carry stale child-epic history forward until a later merge failure.

## Solution

`GitWorktreeManager.UpdateBranchRef` now runs:

```bash
git merge-base --is-ancestor <targetBranch> <sourceBranch>
```

before calling `git update-ref`. Non-fast-forward updates fail before the ref is
moved, allowing the existing dispatcher path to create a rebase child instead of
silently replacing the parent branch.

Regression coverage lives in
`pkg/dispatcher/worktree_manager_test.go:TestUpdateBranchRefRequiresFastForward`.

## Prevention

Keep all branch advancement APIs fast-forward guarded. Any code that moves a ref
without checking ancestry must prove why it cannot discard integration branch
history.
