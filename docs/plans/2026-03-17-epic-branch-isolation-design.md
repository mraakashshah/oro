# Epic Branch Isolation

**Date:** 2026-03-17
**Status:** Draft

## Problem

When multiple epics run concurrently, all child beads merge directly to main. This causes:

1. **Pollution**: Epic A's half-done work lands on main before the epic is validated
2. **No revert**: Epic commits interleave with other work on main — no clean revert path
3. **Cross-epic interference**: Epic B's children rebase onto main that includes A's partial work
4. **No integration testing**: No staging ground to test an epic's beads together before they hit main

## Design

### Branch Topology

```
main ────A──B──C────────────────────────────────X──Y──Z── (epic commits land as contiguous block)
              │                                  ↑
              └─ epic/oro-5bsn ──D──E──F──G──H──┘ rebase + FF
                    │            ↑  ↑  ↑     ↑
                    │            │  │  │     rebase bead resolves conflicts,
                    │            │  │  │     runs integration tests
                    │            │  │  │
                    │            │  │  └─ agent/oro-4zky merges to epic branch
                    │            │  └─ agent/oro-891k merges to epic branch
                    │            └─ agent/oro-xn92 merges to epic branch
                    │
                    └─ created at decomposition
```

### Lifecycle

1. **Epic created** → `bd create` with type=epic
2. **Epic assigned to decomposition worker** → worker creates `epic/<epicID>` branch from `main`, decomposes into child beads + rebase bead
3. **Children execute** → each gets worktree branched from `epic/<epicID>`, merges back to `epic/<epicID>` via rebase+FF
4. **Rebase bead unblocks** → all siblings closed, worker rebases `epic/<epicID>` onto `main`, resolves conflicts, runs tests, merges back to epic branch like any other child
5. **Dispatcher merges** → detects all children done, FF merges epic branch to main, deletes epic branch
6. **Epic auto-close** → acceptance test runs, epic closes

**Standalone beads** (no parent epic) still branch from and merge to `main` directly. Zero change for non-epic work.

### Key Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| When does epic branch rebase onto main? | Only at epic completion (via rebase bead) | Maximum isolation. Big conflict risk mitigated by dedicated rebase bead worker. |
| Rebase onto main is its own bead? | Yes — pre-created at decomposition, depends on all siblings | Gets a worker, conflict resolution, ops review, the whole pipeline. No special-case dispatcher logic. |
| Non-epic beads? | Unchanged — merge directly to main | Epic branches are overhead that only pays off with multiple related beads. |
| When is epic branch created? | At decomposition (not lazily) | Avoids race conditions if two children assigned simultaneously. Deterministic. |
| How does dispatcher know to FF merge epic→main? | It doesn't special-case the rebase bead. Rebase bead merges to epic branch like any child. Auto-close detects all children done, then FF merges epic branch to main. | Zero special-casing for rebase bead. |
| How is the rebase bead identified? | `tags: [rebase]` metadata on the bead. Dispatcher checks tag to re-open on FF failure. | Explicit, queryable, no naming-convention fragility. |

## Component Changes

### Protocol (`pkg/protocol/`)

**`constants.go`**: add `EpicBranchPrefix = "epic/"`

**`message.go`**: add `TargetBranch string` field to `AssignPayload` (line ~68-81)

### Worktree Manager (`pkg/dispatcher/worktree_manager.go`)

`Create` signature changes to accept `baseBranch`:

```go
// Before:
Create(ctx context.Context, beadID string) (path, branch string, err error)

// After:
Create(ctx context.Context, beadID, baseBranch string) (path, branch string, err error)
```

Where `baseBranch` is:
- `epic/<epicID>` for children of an epic
- `main` for standalone beads and the epic decomposition itself

**New methods:**

```go
// BranchExists checks if a git branch exists.
BranchExists(ctx context.Context, branch string) (bool, error)

// MergeFFOnly performs a fast-forward-only merge of branch onto target in the primary repo.
MergeFFOnly(ctx context.Context, branch, target string) (commitSHA string, err error)
```

`MergeFFOnly` gives `tryCloseEpic` git access without adding a separate git runner dependency. The WorktreeManager already holds a git runner.

### Merge Coordinator (`pkg/merge/merge.go`)

**`Opts.TargetBranch`** field (default `"main"`). All hardcoded `"main"` references must be parameterized:
- Line ~110: `git rebase main <branch>` → `git rebase <targetBranch> <branch>`
- Line ~159: `git merge --ff-only <branch>` → checked out on `<targetBranch>` first
- Line ~194: `git rev-list main..<branch>` → `git rev-list <targetBranch>..<branch>`
- Line ~210: `git rev-parse main` → `git rev-parse <targetBranch>`

**Per-branch locking**: Replace single `sync.Mutex` with a `branchLocks sync.Map` of `branch → *sync.Mutex`. Each `Merge()` call acquires the lock for its `TargetBranch`.

**Abort redesign**: `activeWorktree string` becomes `activeWorktrees sync.Map` of `targetBranch → worktreePath`. `Abort()` takes a `targetBranch` parameter to abort only that branch's in-flight merge.

### Dispatcher (`pkg/dispatcher/dispatcher.go`)

**`trackedWorker` struct** (line ~149-168): add `baseBranch string` and `targetBranch string` fields.

**`pendingHandoff` struct** (line ~172): add `epicID string` and `baseBranch string` fields to preserve epic context across worker respawns.

**`assignBead` changes:**
- Resolves `baseBranch`: if `bead.Epic != ""` → `epic/<bead.Epic>`, else `main`
- Resolves `targetBranch`: same logic as `baseBranch` (children merge to epic branch)
- Passes `baseBranch` to `worktrees.Create(ctx, beadID, baseBranch)`
- Before creating worktree for epic child: `d.worktrees.BranchExists(ctx, epicBranch)` — if false, escalate
- Stores `baseBranch` and `targetBranch` in `trackedWorker`
- Passes `targetBranch` in `ASSIGN` message via `AssignPayload.TargetBranch`

**`handleDone` fix (B1)**: Currently clears `w.epicID` at line ~821 before `autoCloseEpicIfComplete` at line ~1110. Fix: capture `epicID := w.epicID` before clearing, pass it explicitly to `autoCloseEpicIfComplete(epicID)`.

**`mergeAndComplete` changes:**
- Constructs `merge.Opts` with `TargetBranch: w.targetBranch` (from trackedWorker)

**`tryCloseEpic` changes (new epic→main merge):**
1. All children closed
2. Check `d.worktrees.BranchExists(ctx, epicBranch)` — if no epic branch, fall through to existing count-based close
3. Gate: `d.beads.HasChildren(ctx, epicID)` must be true — prevents vacuous close when decomposition created branch but no children
4. `d.worktrees.MergeFFOnly(ctx, epicBranch, "main")` — FF merge epic to main
5. If FF fails: find rebase bead via `bd list --parent=<epicID> --tag=rebase`, re-open it with note "main advanced after rebase, re-rebase needed"
6. On success: delete epic branch via `d.worktrees.DeleteBranch(ctx, epicBranch)`
7. Run acceptance test (existing flow)
8. Close epic (existing flow)

### Worker Prompt (`pkg/worker/prompt.go`)

**Epic decomposition prompt** (line ~88-137): add instructions to:
1. Create `epic/<epicID>` branch from main: `git branch epic/<epicID> main`
2. Create child beads (existing)
3. Create rebase bead as final child: `bd create "rebase epic/<epicID> onto main"` with `--tag rebase`, parent wired to epic, dependencies on all siblings

**Rebase bead prompt**: new template section — "Rebase `epic/<epicID>` onto main, resolve any conflicts, run the epic's test suite, commit resolution"

### Worker (`pkg/worker/worker.go`)

`BuildAssignPrompt` (line ~435): read `AssignPayload.TargetBranch` and include in prompt context so the worker knows its merge target.

### Cleanup (`cmd/oro/cmd_cleanup.go`)

`cleanupAgentBranches()` (line ~265): extend to also match `epic/*` branches alongside `agent/*`.

### Work Command (`cmd/oro/cmd_work.go`)

`setupWorktree()` (line ~353): pass `baseBranch` to `WorktreeManager.Create`. For `oro work`, resolve baseBranch from the bead's epic field (if set).

`mergeToMain()` (line ~549): construct `merge.Opts` with `TargetBranch` resolved from bead's epic.

## Edge Cases

| Scenario | Behavior |
|----------|----------|
| Decomposition worker fails to create branch | Dispatcher checks `BranchExists` when first child is assigned. If missing, escalate. |
| Child merge conflict on epic branch | Existing flow — ops agent resolves or escalates. Target is epic branch instead of main. |
| Rebase bead hits conflicts | Worker's job to resolve. Goes through ralph loop / ops review like any bead. |
| Main moves after rebase bead completes but before FF | FF fails. Dispatcher finds rebase bead by `tag:rebase`, re-opens it. Worker re-rebases. |
| Epic has no children (branch created, decomp crashed) | `HasChildren` gate in `tryCloseEpic` prevents vacuous close. Epic stays open for manual intervention. |
| Manual `bd close` before all children done | No epic→main merge triggered. Branch orphaned. `oro cleanup` handles stale epic branches. |
| Two epics touch same files | Isolated until one lands. Second epic's rebase bead surfaces conflicts. |
| Standalone bead merges while epic in flight | No impact. Epic branch isolated. Rebase bead handles divergence. |
| Worker timeout/kill during child work | Existing respawn logic. Worktree preserved. `pendingHandoff` now includes `epicID` + `baseBranch`. |
| Epic branch cleanup | Deleted after FF merge. Failed/abandoned epics cleaned by `oro cleanup`. |
| Epic branch deleted manually mid-execution | Child merge fails (rebase target missing). Merge coordinator returns error. Dispatcher escalates to manager. |
| Child bead moved to different epic | `trackedWorker` retains original `epicID`/`baseBranch` for in-flight work. Next assignment picks up new epic. Split children across branches is a manual error — ops review catches it. |
| Two children merge to epic branch concurrently | Per-branch lock serializes merges to the same epic branch. Second child's rebase base is stale — retry handles this (same as current main-merge races). |
| Rebase bead cannot resolve conflicts (divergence too large) | Normal bead failure path — ralph loop, ops review, eventual escalation to manager. Manual mid-epic rebase is escape hatch (documented below). |

### Manual Mid-Epic Rebase (escape hatch)

If an epic branch has diverged too far from main for the rebase bead to handle automatically:

```bash
git checkout epic/<epicID>
git rebase main
# resolve conflicts manually
git rebase --continue
```

This is a manual intervention, not an automated path. Document in ops runbook.

## Testing Strategy

### Unit Tests

| Test | Location |
|------|----------|
| `WorktreeManager.Create` with custom baseBranch | `worktree_manager_test.go` |
| `WorktreeManager.BranchExists` | `worktree_manager_test.go` |
| `WorktreeManager.MergeFFOnly` | `worktree_manager_test.go` |
| Merge coordinator per-branch locking (concurrent different-branch merges) | `merge_test.go` |
| Merge coordinator serialization (concurrent same-branch merges) | `merge_test.go` |
| `Opts.TargetBranch` parameterizes rebase, rev-list, rev-parse, ff-merge | `merge_test.go` |
| `Abort()` with multiple active worktrees | `merge_test.go` |
| `assignBead` resolves baseBranch from epic (mock captures baseBranch arg) | `dispatcher_test.go` |
| `assignBead` checks `BranchExists` for epic children, escalates on missing | `dispatcher_test.go` |
| `tryCloseEpic` with epic branch: FF merge → branch delete → acceptance | `dispatcher_test.go` |
| `tryCloseEpic` FF failure: finds rebase bead by tag, re-opens it | `dispatcher_test.go` |
| `tryCloseEpic` with zero children: `HasChildren` gate prevents vacuous close | `dispatcher_test.go` |
| `handleDone` passes epicID to `autoCloseEpicIfComplete` (not cleared) | `dispatcher_test.go` |
| `cmd_work.go` passes baseBranch to Create, targetBranch to merge.Opts | `cmd_work_test.go` |
| `cmd_cleanup.go` cleans `epic/*` branches | `cmd_cleanup_test.go` |

### Integration Tests

| Test | Verifies |
|------|----------|
| Full epic lifecycle | Decompose → children → rebase bead → FF to main → acceptance → close |
| Two concurrent epics | Each on own branch, merge independently, no cross-contamination |
| Rebase bead conflict resolution | Main diverges, rebase bead resolves, FF succeeds |
| FF merge failure + rebase bead re-open | Main moves post-rebase, FF fails, rebase bead re-opens, re-rebases, succeeds |

## Files Modified (exhaustive)

### Production
- `pkg/protocol/constants.go` — add `EpicBranchPrefix`
- `pkg/protocol/message.go` — add `TargetBranch` to `AssignPayload`
- `pkg/dispatcher/worktree_manager.go` — `Create(ctx, beadID, baseBranch)`, `BranchExists`, `MergeFFOnly`
- `pkg/merge/merge.go` — `TargetBranch` in Opts, per-branch locking, `Abort(targetBranch)`, parameterize all hardcoded `"main"`
- `pkg/dispatcher/dispatcher.go` — `trackedWorker` fields, `pendingHandoff` fields, `assignBead` baseBranch resolution, `handleDone` epicID capture, `mergeAndComplete` targetBranch, `tryCloseEpic` epic→main FF merge
- `pkg/worker/prompt.go` — decomposition prompt (epic branch + rebase bead), rebase bead prompt template
- `pkg/worker/worker.go` — `BuildAssignPrompt` reads `TargetBranch`
- `cmd/oro/cmd_cleanup.go` — extend to clean `epic/*` branches
- `cmd/oro/cmd_work.go` — `setupWorktree` + `mergeToMain` pass baseBranch/targetBranch

### Mocks (all `WorktreeManager.Create` signature updates)
- `pkg/dispatcher/dispatcher_test.go` — `mockWorktreeManager` (line ~213)
- `cmd/oro/cmd_work_execute_test.go` — `mockWorktreeManager` (line ~70)
- `cmd/oro/cmd_work_test.go` — `mockWorktreeManager` (line ~156)
- `cmd/oro/cmd_work_test.go` — `envCapturingWorktreeManager` (line ~539)

### Tests
- `pkg/dispatcher/worktree_manager_test.go` — baseBranch, BranchExists, MergeFFOnly tests + all existing Create calls updated
- `pkg/merge/merge_test.go` — per-branch locking, TargetBranch parameterization, Abort redesign
- `pkg/dispatcher/dispatcher_test.go` — assignment, tryCloseEpic, FF failure, handleDone epicID capture
- `cmd/oro/cmd_work_test.go` — baseBranch/targetBranch flow
- `cmd/oro/cmd_cleanup_test.go` — epic branch cleanup
