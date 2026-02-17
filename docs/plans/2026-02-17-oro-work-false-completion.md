# Plan: Fix `oro work` false completion bug

## Problem

When `oro work` spawns claude and claude dies (SIGKILL, nesting error, timeout, etc.) with **zero work done**, the process continues through QG → review → merge → bead close. The QG passes vacuously on an unchanged worktree, merge is a no-op (returns main HEAD), and the bead gets falsely closed.

**Desired behavior:**
1. Detect that nothing changed (no commits on the bead branch)
2. Reset the bead to `open` (unassign it)
3. Exit early — do NOT continue to QG/merge

## Root Cause Analysis

Three bugs in `cmd/oro/cmd_work.go`:

### Bug A: No "did work happen?" guard after claude exits
- `spawnAndWait()` returns `nil` regardless of exit code, output, or commits
- Line 175 proceeds straight to QG with no branch-diff check
- The `hasCommitsAhead()` function exists but is only used for `--resume`

### Bug B: Bead stays `in_progress` on all failure paths
- Line 146: `_ = deps.beadSrc.Update(ctx, cfg.beadID, "in_progress")`
- No code path resets it to `open` on failure
- `os.Exit()` calls at lines 201-203, 219, 331 skip all deferred cleanup

### Bug C: Merge phase "failed to get primary repo path" (separate but related)
- `merge.go:120`: runs `git rev-parse --show-toplevel` with `cmd.Dir = commonDir` (the `.git/` dir)
- `.git/` is not a working tree — git fails with exit 128
- Fix: derive primary repo from `commonDir` by stripping `.git` suffix

## Implementation Plan

### Task 1: Add `hasNewWork()` guard after claude exits (cmd_work.go)

**Where:** After line 175 (`logStep("Claude completed")`), before line 179 (`Running quality gate...`)

**What:** Check if the bead branch has commits ahead of main. If not, this means claude produced no commits — bail out.

```go
// After claude exits, check if any work was actually done.
if !hasCommitsAhead(deps.repoRoot, branch) {
    logStep("No commits on branch — claude produced no work")
    _ = deps.beadSrc.Update(ctx, cfg.beadID, "open")
    _ = deps.wtMgr.Remove(ctx, worktree)
    return fmt.Errorf("claude exited without producing commits on bead %s", cfg.beadID)
}
```

**Test:** `TestExecuteWork_NoCommits_BailsOut` — mock spawner that exits cleanly but makes no commits. Assert:
- bead updated to "open"
- worktree removed
- returns error (does not proceed to QG)

### Task 2: Reset bead to `open` on all failure paths (cmd_work.go)

**Where:** Add a deferred cleanup near line 146 that resets the bead if we haven't reached the merge phase.

**What:** Track whether merge succeeded with a bool. In a deferred function, if merge didn't succeed, reset bead to `open`.

```go
_ = deps.beadSrc.Update(ctx, cfg.beadID, "in_progress")
var merged bool
defer func() {
    if !merged {
        _ = deps.beadSrc.Update(context.Background(), cfg.beadID, "open")
    }
}()
```

Set `merged = true` after line 221 (`logStep("Merged (commit %s)"...)`).

**Why `context.Background()`:** The parent ctx may be cancelled (signal, timeout) — we still want the bead reset to go through.

**Also:** Replace `os.Exit()` calls (lines 201, 219, 331) with `return fmt.Errorf(...)` so deferred cleanup actually runs. The exit codes can be handled by the caller (`runWork`) if needed.

**Test:** `TestExecuteWork_QGFails_ResetsBead` — mock QG to fail maxRetries times. Assert bead reset to "open".

### Task 3: Fix merge `cherryPickToMain` primary repo resolution (merge.go)

**Where:** `pkg/merge/merge.go:112-124`

**What:** Instead of running `git rev-parse --show-toplevel` inside `commonDir` (which is `.git/`), derive the primary repo path by trimming the `.git` suffix:

```go
commonDir, _, err := c.git.Run(ctx, opts.Worktree, "rev-parse", "--git-common-dir")
if err != nil {
    return nil, fmt.Errorf("failed to get git common dir: %w", err)
}
commonDir = strings.TrimSpace(commonDir)

// commonDir is like "/path/to/repo/.git" — derive primary repo from it.
primaryRepo := strings.TrimSuffix(strings.TrimRight(commonDir, "/"), "/.git")
if primaryRepo == commonDir {
    // Fallback: commonDir didn't end with /.git (unexpected), try --show-toplevel from worktree
    primaryRepo, _, err = c.git.Run(ctx, opts.Worktree, "rev-parse", "--show-toplevel")
    if err != nil {
        return nil, fmt.Errorf("failed to get primary repo path: %w", err)
    }
    primaryRepo = strings.TrimSpace(primaryRepo)
}
```

**Test:** Update existing merge tests to verify cherry-pick works with worktree-based `commonDir`. Add test case where `commonDir` returns a `.git` path.

### Task 4: Replace `os.Exit()` with returned errors (cmd_work.go)

**Where:** Lines 129, 135, 201-203, 219, 331, 352

**What:** Replace `os.Exit(N)` with `return &exitError{code: N, msg: "..."}` or simply `return fmt.Errorf(...)`. Add an exit code wrapper in `runWork` so the process still exits with the correct code, but deferred cleanup runs first.

```go
type exitError struct {
    code int
    msg  string
}

func (e *exitError) Error() string { return e.msg }
```

In `runWork`:
```go
err := executeWork(ctx, cfg, deps)
if err != nil {
    var ee *exitError
    if errors.As(err, &ee) {
        os.Exit(ee.code)
    }
    return err
}
```

**Test:** Verify deferred cleanup runs even on QG exhaustion and merge failure.

## Execution Order

1. **Task 1** (no-work guard) — highest impact, prevents false completions
2. **Task 2** (bead reset on failure) — prevents orphaned in_progress beads
3. **Task 4** (os.Exit → return) — required for Task 2's defer to work on all paths
4. **Task 3** (merge fix) — separate bug, can be done in parallel

Tasks 1+2+4 are tightly coupled (same file, same flow). Task 3 is independent.

## Files Changed

- `cmd/oro/cmd_work.go` — Tasks 1, 2, 4
- `cmd/oro/cmd_work_test.go` — Tests for Tasks 1, 2, 4
- `pkg/merge/merge.go` — Task 3
- `pkg/merge/merge_test.go` — Tests for Task 3

## Bead Tracking

These fixes directly address the false completion bug that caused oro-bu85 and oro-18c5.1 to be falsely closed. Should be filed as a P0 bead before implementation.
