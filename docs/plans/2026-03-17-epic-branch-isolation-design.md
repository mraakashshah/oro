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

**`types.go`**: add `Tags []string` field to `Bead` struct (line ~16-34). Required for `tag:rebase` identification.

**`message.go`**: add `TargetBranch string` field to `AssignPayload` (line ~68-81)

### BeadSource (`pkg/dispatcher/beadsource.go`)

**New method on `BeadSource` interface** (dispatcher.go:64-74):

```go
// FindByParentAndTag returns beads matching parent + tag.
FindByParentAndTag(ctx context.Context, parentID, tag string) ([]Bead, error)
```

**`CLIBeadSource` implementation**: runs `bd list --parent=<parentID> --tag=<tag> --json`, parses result.

**`BeadDetail` struct** (types.go:37-52): add `Epic string` field so `cmd_work.go` can resolve baseBranch from bead context.

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
- Line ~159: `git merge --ff-only <branch>` → on `<targetBranch>`
- Line ~194: `git rev-list main..<branch>` → `git rev-list <targetBranch>..<branch>`
- Line ~202: `git diff main..<branch>` → `git diff <targetBranch>..<branch>` (in `isBranchMerged`)
- Line ~210: `git rev-parse main` → `git rev-parse <targetBranch>`

**Two-level locking** (replaces previous per-branch-only proposal):

The rebase step runs in an isolated worktree and does not touch the primary repo — it needs no primary repo lock. The FF merge step operates on HEAD in the primary repo and must be globally serialized.

```go
type Coordinator struct {
    rebaseLocks sync.Map   // targetBranch → *sync.Mutex (serializes rebases to same target)
    ffLock      sync.Mutex // global lock for FF merge step only (touches primary repo HEAD)
    // ...
}
```

Flow per `Merge()` call:
1. Acquire per-target-branch rebase lock
2. Rebase in worktree (slow, isolated — no primary repo contention)
3. Release rebase lock
4. Remove worktree
5. Acquire global `ffLock`
6. `git merge --ff-only` in primary repo (instant)
7. Release `ffLock`

This allows multiple concurrent rebases (children of different epics) while preventing HEAD races during FF merge. Two children of the same epic serialize on the rebase lock (same target branch).

**Abort redesign**: `activeWorktrees sync.Map` of `targetBranch → worktreePath`. `AbortAll()` iterates all entries for shutdown. `Abort(targetBranch)` aborts a single branch's merge.

### Dispatcher (`pkg/dispatcher/dispatcher.go`)

**`trackedWorker` struct** (line ~149-168): add `baseBranch string` and `targetBranch string` fields.

**`pendingHandoff` struct** (line ~172): add `epicID string`, `baseBranch string`, and `targetBranch string` fields to preserve epic context across worker respawns.

**`assignBead` changes:**
- Resolves `baseBranch`: if `bead.Epic != ""` → `epic/<bead.Epic>`, else `main`
- Resolves `targetBranch`: same logic as `baseBranch` (children merge to epic branch)
- Passes `baseBranch` to `worktrees.Create(ctx, beadID, baseBranch)`
- Before creating worktree for epic child: `d.worktrees.BranchExists(ctx, epicBranch)` — if false, escalate
- Stores `baseBranch` and `targetBranch` in `trackedWorker`
- Passes `targetBranch` in `ASSIGN` message via `AssignPayload.TargetBranch`

**`handleDone` fix**: Currently clears `w.epicID` at line ~821 before `autoCloseEpicIfComplete` at line ~1110. Fix: capture `epicID := w.epicID` before clearing, pass it explicitly to `autoCloseEpicIfComplete(epicID)`.

**`handleHandoff` fix**: Same pattern — `handleHandoff` (line ~1338) clears `w.epicID` before `respawnWorker` (line ~1353). Fix: capture `epicID`, `baseBranch`, `targetBranch` before clearing, populate `pendingHandoff` with these values. Extend `respawnWorker` signature to accept epic context.

**`mergeAndComplete` changes:**
- Constructs `merge.Opts` with `TargetBranch: w.targetBranch` (from trackedWorker)

**`tryCloseEpic` changes (new epic→main merge):**
1. All children closed
2. Check `d.worktrees.BranchExists(ctx, epicBranch)` — if no epic branch, fall through to existing count-based close
3. Gate: `d.beads.HasChildren(ctx, epicID)` must be true — prevents vacuous close when decomposition created branch but no children
4. Guard against concurrent invocation: `d.mergingBeads` check (or similar mutex) — two children completing simultaneously could trigger two `tryCloseEpic` calls; only one should proceed with FF merge
5. `d.worktrees.MergeFFOnly(ctx, epicBranch, "main")` — FF merge epic to main
6. If FF fails: find rebase bead via `d.beads.FindByParentAndTag(ctx, epicID, "rebase")`, re-open it with note "main advanced after rebase, re-rebase needed"
7. On success: delete epic branch via `d.worktrees.DeleteBranch(ctx, epicBranch)`
8. Run acceptance test (existing flow)
9. Close epic (existing flow)

**Ops review** (line ~1511): `BaseBranch` in review context must use `w.targetBranch` instead of hardcoded `"main"` for epic children.

**Shutdown** (line ~3543): replace `d.merger.Abort()` with `d.merger.AbortAll()`.

### Worker Prompt (`pkg/worker/prompt.go`)

**Epic decomposition prompt** (line ~88-137): add instructions to:
1. Create `epic/<epicID>` branch from main: `git branch epic/<epicID> main`
2. Create child beads (existing)
3. Create rebase bead as final child: `bd create "rebase epic/<epicID> onto main" --tag rebase`, parent wired to epic, dependencies on all siblings

**Rebase bead prompt**: new template section — "Rebase `epic/<epicID>` onto main, resolve any conflicts, run the epic's test suite, commit resolution"

### Worker (`pkg/worker/worker.go`)

`BuildAssignPrompt` (line ~435): read `AssignPayload.TargetBranch` and include in prompt context so the worker knows its merge target.

### Cleanup (`cmd/oro/cmd_cleanup.go`)

`cleanupAgentBranches()` (line ~265): extend to also match `epic/*` branches alongside `agent/*`.

### Work Command (`cmd/oro/cmd_work.go`)

`setupWorktree()` (line ~353): pass `baseBranch` to `WorktreeManager.Create`. Resolve baseBranch from `BeadDetail.Epic` field (if set).

`hasCommitsAhead()` (line ~362-369): parameterize hardcoded `"main.."` to use the resolved target branch.

`mergeToMain()` (line ~549): construct `merge.Opts` with `TargetBranch` resolved from bead's epic.

`reviewLoop()` (line ~451): pass resolved `BaseBranch` (target branch, not hardcoded `"main"`).

### Worker Pool (`pkg/dispatcher/worker_pool.go`)

`registerWorker` (line ~125-133): when sending `ASSIGN` from `pendingHandoff`, include `TargetBranch` in the payload. Store full `AssignPayload` context in `pendingHandoff` rather than individual fields.

## Edge Cases

| Scenario | Behavior |
|----------|----------|
| Decomposition worker fails to create branch | Dispatcher checks `BranchExists` when first child is assigned. If missing, escalate. |
| Child merge conflict on epic branch | Existing flow — ops agent resolves or escalates. Target is epic branch instead of main. |
| Rebase bead hits conflicts | Worker's job to resolve. Goes through ralph loop / ops review like any bead. |
| Main moves after rebase bead completes but before FF | FF fails. Dispatcher finds rebase bead via `FindByParentAndTag`, re-opens it. Worker re-rebases. |
| Epic has no children (branch created, decomp crashed) | `HasChildren` gate in `tryCloseEpic` prevents vacuous close. Epic stays open for manual intervention. |
| Manual `bd close` before all children done | No epic→main merge triggered. Branch orphaned. `oro cleanup` handles stale epic branches. |
| Two epics touch same files | Isolated until one lands. Second epic's rebase bead surfaces conflicts. |
| Standalone bead merges while epic in flight | No impact. Epic branch isolated. Rebase bead handles divergence. |
| Worker timeout/kill during child work | Existing respawn logic. Worktree preserved. `pendingHandoff` includes `epicID`, `baseBranch`, `targetBranch`. |
| Epic branch cleanup | Deleted after FF merge. Failed/abandoned epics cleaned by `oro cleanup`. |
| Epic branch deleted manually mid-execution | Child merge fails (rebase target missing). Merge coordinator returns error. Dispatcher escalates to manager. |
| Child bead moved to different epic | `trackedWorker` retains original `epicID`/`baseBranch` for in-flight work. Next assignment picks up new epic. Split children across branches is a manual error — ops review catches it. |
| Two children merge to epic branch concurrently | Per-target-branch rebase lock serializes rebases to same epic branch. Global FF lock serializes the instant FF merge step. |
| Rebase bead cannot resolve conflicts (divergence too large) | Normal bead failure path — ralph loop, ops review, eventual escalation to manager. Manual mid-epic rebase is escape hatch (documented below). |
| Concurrent tryCloseEpic for same epic | `mergingBeads` guard ensures only one invocation proceeds with FF merge. Second invocation is a no-op. |
| Rebase bead re-opened but worktree cleaned up | Re-opened bead flows through normal `assignBead` path — gets a fresh worktree branched from epic branch. No special handling needed. |
| Respawned worker after handoff | `pendingHandoff` carries `epicID`, `baseBranch`, `targetBranch`. `registerWorker` includes `TargetBranch` in the re-sent ASSIGN message. |

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
| Merge coordinator two-level locking: concurrent rebases to different targets | `merge_test.go` |
| Merge coordinator: serialized rebases to same target | `merge_test.go` |
| Merge coordinator: global FF lock serializes all FF merges | `merge_test.go` |
| `Opts.TargetBranch` parameterizes rebase, rev-list, diff, rev-parse, ff-merge | `merge_test.go` |
| `AbortAll()` iterates all active worktrees | `merge_test.go` |
| `Abort(targetBranch)` aborts single branch merge | `merge_test.go` |
| `assignBead` resolves baseBranch from epic (mock captures baseBranch arg) | `dispatcher_test.go` |
| `assignBead` checks `BranchExists` for epic children, escalates on missing | `dispatcher_test.go` |
| `tryCloseEpic` with epic branch: FF merge → branch delete → acceptance | `dispatcher_test.go` |
| `tryCloseEpic` FF failure: finds rebase bead by `FindByParentAndTag`, re-opens it | `dispatcher_test.go` |
| `tryCloseEpic` with zero children: `HasChildren` gate prevents vacuous close | `dispatcher_test.go` |
| `tryCloseEpic` concurrent invocation: only one proceeds, other is no-op | `dispatcher_test.go` |
| `handleDone` passes epicID to `autoCloseEpicIfComplete` (not cleared) | `dispatcher_test.go` |
| `handleHandoff` captures epicID/baseBranch before clearing, populates pendingHandoff | `dispatcher_test.go` |
| `registerWorker` sends TargetBranch in ASSIGN from pendingHandoff | `worker_pool_test.go` |
| `cmd_work.go` passes baseBranch to Create, targetBranch to merge.Opts | `cmd_work_test.go` |
| `cmd_work.go` `hasCommitsAhead` uses targetBranch, not hardcoded main | `cmd_work_test.go` |
| `cmd_work.go` `reviewLoop` passes correct BaseBranch | `cmd_work_test.go` |
| `cmd_cleanup.go` cleans `epic/*` branches | `cmd_cleanup_test.go` |
| `CLIBeadSource.FindByParentAndTag` | `beadsource_test.go` |
| Ops review uses `w.targetBranch` for BaseBranch | `dispatcher_test.go` |

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
- `pkg/protocol/types.go` — add `Tags []string` to `Bead`, add `Epic string` to `BeadDetail`
- `pkg/protocol/message.go` — add `TargetBranch` to `AssignPayload`
- `pkg/dispatcher/beadsource.go` — add `FindByParentAndTag` to `BeadSource` interface + `CLIBeadSource` implementation
- `pkg/dispatcher/worktree_manager.go` — `Create(ctx, beadID, baseBranch)`, `BranchExists`, `MergeFFOnly`
- `pkg/merge/merge.go` — `TargetBranch` in Opts, two-level locking (per-target rebase lock + global FF lock), `Abort(targetBranch)`, `AbortAll()`, parameterize all hardcoded `"main"` (lines ~110, ~159, ~194, ~202, ~210)
- `pkg/dispatcher/dispatcher.go` — `trackedWorker` fields, `pendingHandoff` fields, `assignBead` baseBranch resolution, `handleDone` epicID capture, `handleHandoff` epicID capture, `mergeAndComplete` targetBranch, `tryCloseEpic` epic→main FF merge with concurrent invocation guard, ops review `BaseBranch`, shutdown `AbortAll()`
- `pkg/dispatcher/worker_pool.go` — `registerWorker` includes `TargetBranch` in ASSIGN from pendingHandoff
- `pkg/worker/prompt.go` — decomposition prompt (epic branch + rebase bead), rebase bead prompt template
- `pkg/worker/worker.go` — `BuildAssignPrompt` reads `TargetBranch`
- `cmd/oro/cmd_cleanup.go` — extend to clean `epic/*` branches
- `cmd/oro/cmd_work.go` — `setupWorktree` baseBranch, `hasCommitsAhead` targetBranch, `mergeToMain` targetBranch, `reviewLoop` BaseBranch

### Mocks — WorktreeManager (Create signature + BranchExists + MergeFFOnly)
- `pkg/dispatcher/dispatcher_test.go` — `mockWorktreeManager` (line ~213)
- `pkg/integration/dispatcher_worker_test.go` — `mockWorktreeManager` (line ~81)
- `cmd/oro/cmd_work_execute_test.go` — `mockWorktreeManager` (line ~70)
- `cmd/oro/cmd_work_test.go` — `mockWorktreeManager` (line ~156)
- `cmd/oro/cmd_work_test.go` — `envCapturingWorktreeManager` (line ~539)

### Mocks — BeadSource (FindByParentAndTag)
- `pkg/dispatcher/dispatcher_test.go` — `mockBeadSource`
- `pkg/dispatcher/beadsource_test.go` — test for `CLIBeadSource.FindByParentAndTag`

### Tests
- `pkg/dispatcher/worktree_manager_test.go` — baseBranch, BranchExists, MergeFFOnly tests + all existing Create calls updated
- `pkg/merge/merge_test.go` — two-level locking, TargetBranch parameterization, AbortAll, Abort(targetBranch)
- `pkg/dispatcher/dispatcher_test.go` — assignment, tryCloseEpic (all paths), handleDone epicID capture, handleHandoff epicID capture, concurrent tryCloseEpic, ops review BaseBranch
- `pkg/dispatcher/worker_pool_test.go` — registerWorker with TargetBranch in pendingHandoff
- `cmd/oro/cmd_work_test.go` — baseBranch/targetBranch flow, hasCommitsAhead, reviewLoop
- `cmd/oro/cmd_cleanup_test.go` — epic branch cleanup
- `pkg/dispatcher/beadsource_test.go` — FindByParentAndTag
