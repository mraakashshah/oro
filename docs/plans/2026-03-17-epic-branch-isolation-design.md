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

## Component Changes

### Protocol (`pkg/protocol/`)

- `constants.go`: add `EpicBranchPrefix = "epic/"`
- Message types: `ASSIGN` message includes `TargetBranch` field

### Worktree Manager (`pkg/dispatcher/worktree_manager.go`)

`Create` signature changes to accept `baseBranch`:

```go
// Before:
git worktree add .worktrees/<beadID> -b agent/<beadID> main

// After:
git worktree add .worktrees/<beadID> -b agent/<beadID> <baseBranch>
```

Where `baseBranch` is:
- `epic/<epicID>` for children of an epic
- `main` for standalone beads and the epic decomposition itself

### Merge Coordinator (`pkg/merge/merge.go`)

`Opts` gets a `TargetBranch` field (default `"main"`):
- Epic children: `TargetBranch = "epic/<epicID>"`
- Standalone beads: `TargetBranch = "main"`

Lock becomes **per-target-branch** instead of global: `sync.Map` of `branch → *sync.Mutex`, so children of different epics merge concurrently.

### Dispatcher (`pkg/dispatcher/dispatcher.go`)

**Assignment:**
- `assignBead` resolves `baseBranch`: if `bead.Epic != ""` → `epic/<bead.Epic>`, else `main`
- Passes `baseBranch` to `worktrees.Create`, stores in `trackedWorker`
- Passes `targetBranch` in `ASSIGN` message

**Epic decomposition:**
- After decomposition worker completes, dispatcher verifies `epic/<epicID>` branch exists

**Epic-to-main merge (new, in `tryCloseEpic`):**
1. All children closed → detect `epic/<epicID>` branch exists
2. Acquire main merge lock
3. `git merge --ff-only epic/<epicID>` on main
4. If FF fails: re-open rebase bead ("main advanced, re-rebase needed")
5. On success: delete `epic/<epicID>` branch
6. Run acceptance test (existing)
7. Close epic (existing)

### Worker Prompt (`pkg/worker/prompt.go`)

- Epic decomposition prompt: new instruction to create `epic/<epicID>` branch from main, and create a rebase bead as final child with dependencies on all siblings
- Rebase bead prompt: new template — "rebase `epic/<epicID>` onto main, resolve conflicts, run tests"

## Edge Cases

| Scenario | Behavior |
|----------|----------|
| Decomposition worker fails to create branch | Dispatcher detects no `epic/<epicID>` branch when first child assigned. Escalate. |
| Child merge conflict on epic branch | Existing flow — ops agent resolves or escalates. Target is epic branch instead of main. |
| Rebase bead hits conflicts | Worker's job to resolve. Goes through ralph loop / ops review like any bead. |
| Main moves after rebase bead completes but before FF | FF fails. Dispatcher re-opens rebase bead with note. Worker re-rebases. |
| Epic has no children | Existing behavior — auto-closes with count-based fallback. No epic branch. |
| Manual `bd close` before all children done | No epic→main merge. Branch orphaned. `oro cleanup` handles stale epic branches. |
| Two epics touch same files | Isolated until one lands. Second epic's rebase bead surfaces conflicts. |
| Standalone bead merges while epic in flight | No impact. Epic branch isolated. Rebase bead handles divergence. |
| Worker timeout/kill during child work | Existing respawn logic. Worktree preserved. Base branch unchanged. |
| Epic branch cleanup | Deleted after FF merge. Failed/abandoned epics cleaned by `oro cleanup` (extend to cover `epic/*`). |

## Testing Strategy

### Unit Tests

| Test | Location |
|------|----------|
| `WorktreeManager.Create` with custom baseBranch | `worktree_manager_test.go` |
| Merge coordinator per-branch locking | `merge_test.go` |
| `Opts.TargetBranch` flows through rebase+FF | `merge_test.go` |
| `assignBead` resolves baseBranch from epic | `dispatcher_test.go` |
| `tryCloseEpic` with epic branch FF merge | `dispatcher_test.go` |
| FF merge failure re-opens rebase bead | `dispatcher_test.go` |

### Integration Tests

| Test | Verifies |
|------|----------|
| Full epic lifecycle | Decompose → children → rebase bead → FF to main → acceptance → close |
| Two concurrent epics | Each on own branch, merge independently, no cross-contamination |
| Rebase bead conflict resolution | Main diverges, rebase bead resolves, FF succeeds |

## Files Modified

- `pkg/protocol/constants.go` — add `EpicBranchPrefix`
- `pkg/protocol/types.go` or message types — `TargetBranch` in ASSIGN
- `pkg/dispatcher/worktree_manager.go` — `Create` accepts `baseBranch`
- `pkg/dispatcher/worktree_manager_test.go` — tests for baseBranch
- `pkg/merge/merge.go` — `TargetBranch` in Opts, per-branch locking
- `pkg/merge/merge_test.go` — per-branch lock tests, target branch tests
- `pkg/dispatcher/dispatcher.go` — baseBranch resolution, epic→main FF merge in tryCloseEpic
- `pkg/dispatcher/dispatcher_test.go` — assignment tests, FF merge tests
- `pkg/worker/prompt.go` — decomposition and rebase bead prompt templates
- All mock `WorktreeManager` implementations — update `Create` signature
