# Configurable Base Branch

**Date:** 2026-03-24
**Status:** Draft
**Problem:** Oro hardcodes `"main"` as the default merge target. Users working on feature branches before submitting PRs — or who can't push to main (protected branches) — need the swarm to operate against arbitrary branches.

## Use Cases

1. **Long-lived feature branch** — sustained work on `feature/auth` before opening a PR to main.
2. **Protected main** — repo requires PRs; direct push to main is blocked.
3. **Multi-branch** — working on 2-3 features simultaneously, different beads target different branches.

## Design

### Two-layer branch resolution

1. **Dispatcher default** — captured at `oro start` from `git symbolic-ref --short HEAD`. Stored in `Config.DefaultBranch`. Overridable with `--base-branch <branch>`.
2. **Per-bead override** — `bead.Metadata["branch"]`. Dispatcher reads at assignment time:

```
resolved_branch = bead.Metadata["branch"] || config.DefaultBranch
```

Usage:
```bash
# Start swarm on current branch (e.g. lively/linting)
git checkout lively/linting
oro start --workers 3

# Most beads inherit the default
bd create "fix linting rules"

# Override for a bead targeting a different branch
bd create "add auth flow" --metadata branch=feature/auth
```

### End-to-end flow

**Startup:**
```
oro start
  → git symbolic-ref --short HEAD → "lively/linting"
  → Config.DefaultBranch = "lively/linting"
```

**Assignment:**
```
Dispatcher picks bead from queue
  → branch = bead.Metadata["branch"] || d.cfg.DefaultBranch
  → validate branch exists locally (git branch --list)
  → for epic children: resolveEpicBranch returns epic/<id> (unchanged)
  → for standalone: branch is the resolved value above
  → worktree.Create(beadID, branch)
  → worker gets AssignPayload{TargetBranch: branch}
```

**Worker lifecycle** (unchanged):
```
Worker commits to agent/<beadID> in worktree
Worker prompt references TargetBranch (already parameterized)
QG runs in worktree
Worker sends DONE
```

**Merge** (rebase unchanged; FF merge strategy changes for non-HEAD targets):
```
merge.Opts{TargetBranch: branch}
  → rebase agent/<beadID> onto <branch>              (unchanged)
  → if <branch> == HEAD in primary repo:
      git merge --ff-only agent/<beadID>              (existing path)
  → else:
      git fetch . agent/<beadID>:refs/heads/<branch>  (new: non-HEAD FF update)
  → cleanup worktree + agent branch
```

**Push:**
```
Dispatcher escalates: MERGE_COMPLETE <bead_id> — merged to <branch>. <sha>.
Manager runs: git push origin <branch>
```

**Epic interaction:**
```
Epic bead has Metadata["branch"] = "feature/auth"
  → epic/<epicID> created FROM "feature/auth" (not main)
  → children merge to epic/<epicID> (unchanged)
  → final rebase bead rebases epic/<epicID> onto "feature/auth"
  → FF-merge epic/<epicID> → "feature/auth"
```

### Non-HEAD fast-forward merge

The primary repo's HEAD is on `DefaultBranch` (the branch checked out at `oro start` time). For beads that target DefaultBranch, the existing `git merge --ff-only` works (merges into HEAD). For per-bead overrides targeting a different branch, `git merge --ff-only` would fail because it can only merge into HEAD.

**Fix:** `git fetch . <branch>:refs/heads/<target>` atomically updates the target branch ref as a fast-forward without requiring checkout. Non-force fetch refuses if it's not a fast-forward (same safety as `--ff-only`).

This applies to two code paths:

1. **`merge.go:worktreeRemoveAndFFMerge`** — standalone bead merge. Detect whether `effectiveTarget(opts)` matches HEAD in `primaryRepo`. If yes, use `git merge --ff-only` (existing). If no, use `git fetch . <branch>:refs/heads/<target>`. Retry logic works the same for both paths (re-rebase, then retry). After success, get SHA from `git rev-parse refs/heads/<target>` (not HEAD) for non-HEAD targets.

2. **`dispatcher.go:ffMergeEpicBranch`** — epic close. Currently calls `MergeFFOnly(epicBranch, d.repoRoot)` which merges into HEAD. Add `targetBranch` parameter. If target != HEAD, use the new `UpdateBranchRef` method on WorktreeManager.

**New method on WorktreeManager interface:**
```go
// UpdateBranchRef atomically fast-forward updates targetBranch to point to
// srcBranch's tip. Uses `git fetch . <src>:refs/heads/<target>` which refuses
// non-fast-forward updates. For non-HEAD branch merges.
UpdateBranchRef(ctx context.Context, srcBranch, targetBranch string) (commitSHA string, err error)
```

### Concurrency

Per-target rebase locks (already in `merge.Coordinator`) serialize merges to the same branch. Different branches merge in parallel. The global `ffLock` serializes all FF operations (both HEAD merges and non-HEAD ref updates) — kept for simplicity, overhead is minimal since FF ops are fast.

### Detached HEAD

`git symbolic-ref --short HEAD` fails on detached HEAD. In that case:
- If `--base-branch` flag provided → use it.
- Otherwise → error: `"detached HEAD: specify --base-branch or checkout a branch"`.

## Changes

### Layer 1 — Config & startup

| File | Line | Change |
|------|------|--------|
| `pkg/dispatcher/dispatcher.go` | 246 | Add `DefaultBranch string` to `Config`. `withDefaults()` sets to `"main"` if empty (backward compat). |
| `cmd/oro/cmd_start.go` | 387 | Add `--base-branch` flag. Default: `git symbolic-ref --short HEAD`. Store in `Config.DefaultBranch`. |

### Layer 2 — Branch resolution

| File | Line | Change |
|------|------|--------|
| `pkg/protocol/types.go` | — | Add `const MetaBranch = "branch"` |
| `pkg/dispatcher/epic_branch.go` | 26, 35, 41, 51 | `resolveEpicBranch` takes `defaultBranch string` param, returns it instead of `"main"` in all 4 non-epic return paths. |

### Layer 3 — Assignment

| File | Line | Change |
|------|------|--------|
| `pkg/dispatcher/dispatcher.go` | ~2715 | In `assignBead`: read `bead.Metadata[protocol.MetaBranch]`, fall back to `d.cfg.DefaultBranch`. Pass to `resolveEpicBranch` and `worktree.Create`. |

### Layer 4 — FF merge for non-HEAD targets

| File | Line | Change |
|------|------|--------|
| `pkg/merge/merge.go` | 177-221 | `worktreeRemoveAndFFMerge`: detect if `target == HEAD` in primary repo. If HEAD: existing `git merge --ff-only`. If non-HEAD: `git fetch . <branch>:refs/heads/<target>`. After success, `rev-parse refs/heads/<target>` (not HEAD) for non-HEAD. Retry logic unchanged (re-rebase, then retry). |
| `pkg/dispatcher/dispatcher.go` | 1477-1515 | `ffMergeEpicBranch`: add `targetBranch string` param. Resolve epic's target branch from `bead.Metadata[MetaBranch]` or `DefaultBranch`. If target == HEAD: use `MergeFFOnly` as today. If non-HEAD: use new `UpdateBranchRef`. Fix hardcoded `"main"` in error msg (line 1495) and rebase bead title (line 1500). |
| `pkg/dispatcher/worktree_manager.go` | — | Add `UpdateBranchRef(ctx, srcBranch, targetBranch string) (commitSHA string, err error)` to `GitWorktreeManager`. Uses `git fetch . <src>:refs/heads/<target>`. |
| `pkg/dispatcher/dispatcher.go` | 80-92 | Add `UpdateBranchRef` to `WorktreeManager` interface. |

### Layer 5 — Merge state & escalation

| File | Line | Change |
|------|------|--------|
| `pkg/dispatcher/dispatcher.go` | 2542 | `isBranchMerged`: replace hardcoded `"main"` with `d.cfg.DefaultBranch`. See design note below. |
| `pkg/dispatcher/dispatcher.go` | 1345 | `mergeAndComplete`: interpolate `targetBranch` into MERGE_COMPLETE escalation summary instead of hardcoded `"merged to main"`. Format: `"merged to <branch>"`. |
| `pkg/dispatcher/dispatcher.go` | 1318 | `mergeAndComplete` conflict path: add `TargetBranch: targetBranch` to `ops.MergeOpts` construction. |
| `pkg/dispatcher/dispatcher.go` | 1416-1471 | `tryCloseEpic`: after `d.beads.Show(epicID)` at line 1428, extract epic's target branch from `detail.Metadata[MetaBranch]` or `d.cfg.DefaultBranch`. Pass through `completeEpicClose` → `ffMergeEpicBranch`. |
| `pkg/dispatcher/dispatcher.go` | 1521 | `completeEpicClose`: add `targetBranch string` param, pass to `ffMergeEpicBranch`. |

**Design note — `isBranchMerged` and per-bead branches:**
`isBranchMerged` is called during assignment filtering before the bead's `Metadata["branch"]` is resolved. Resolving metadata per bead per poll cycle adds I/O overhead. Instead, check against `d.cfg.DefaultBranch` (correct for ~90% of beads). For the rare per-bead override case, a duplicate assignment attempt is caught by the merge coordinator's "already merged" check during the actual rebase — no silent data corruption, just a wasted assignment that self-corrects.

Note: `merge.go:effectiveTarget()` already returns `Opts.TargetBranch` when set. No change to `effectiveTarget` needed.

### Layer 6 — Prompts

| File | Line | Change |
|------|------|--------|
| `pkg/worker/prompt.go` | 220-234 | Fix `"main"` default at line 223. Update constraint at line 234: `"Do not modify the <targetBranch> branch"`. |
| `pkg/ops/review_prompt.go` | 14 | Replace `base = "main"` with parameterized value from review context (already has `BaseBranch` field). |
| `pkg/ops/ops.go` | 490 | Replace hardcoded `"git rebase main"` with interpolated target branch. Requires adding `TargetBranch string` to `MergeOpts` struct (line 122). |

### Layer 7 — Manager push

| File | Line | Change |
|------|------|--------|
| `cmd/oro/manager.go` | 87, 137 | MERGE_COMPLETE playbook: `git push origin <branch>`. Manager parses branch name from the escalation summary (`"merged to <branch>"`). |

### Layer 8 — Standalone worker

| File | Line | Change |
|------|------|--------|
| `cmd/oro/cmd_work.go` | 143, 246 | Read default branch from config or `--base-branch` flag. Thread to `resolveEpicBranch`. |

### Not changed

- `WorktreeManager.Create()` — already takes `baseBranch` param.
- `merge.Opts` / `effectiveTarget()` — already parameterized via `TargetBranch`.
- `AssignPayload` — already has `TargetBranch` field.
- `trackedWorker` — already has `baseBranch` and `targetBranch` fields.
- `ReviewOpts` — already has `BaseBranch` field (just needs callers to populate it).

### Changed (updated from adversarial review)

- `WorktreeManager` interface — gains `UpdateBranchRef` method for non-HEAD FF merges.
- `merge.go:worktreeRemoveAndFFMerge` — dual-path: HEAD merge vs non-HEAD fetch.
- `ffMergeEpicBranch`, `completeEpicClose`, `tryCloseEpic` — gain `targetBranch` param, resolve from epic metadata.

## Backward compatibility

- `DefaultBranch` defaults to `"main"` in `withDefaults()` — zero-config existing behavior preserved.
- Beads without `Metadata["branch"]` inherit the dispatcher default.
- Epic branch resolution still returns `epic/<id>` for epic children — only the non-epic fallback changes.

## Risks and mitigations

| Risk | Severity | Mitigation |
|------|----------|------------|
| Branch doesn't exist locally → worktree creation fails | High | Validate on assignment, escalate to manager |
| Typo in `Metadata["branch"]` → merges to wrong branch | Medium | `const MetaBranch` prevents key typos; branch existence validation catches value typos |
| Detached HEAD at startup, no `--base-branch` | Low | Clear error message with instructions |
| Manager pushes wrong branch | Medium | MERGE_COMPLETE escalation includes explicit branch name in summary |
| `resolveEpicBranch` callers forget to pass `defaultBranch` | Medium | Compiler enforces — new required param |
| Existing tests hardcode `"main"` expectations | Low | Update test expectations; tests that don't set `DefaultBranch` get `"main"` via `withDefaults()` |
| `isBranchMerged` uses DefaultBranch not per-bead branch | Low | Worst case: one wasted assignment attempt for per-bead override beads. Merge coordinator catches "already merged" during actual rebase. No data corruption. |
| `MergeOpts` missing `TargetBranch` → conflict prompt says `git rebase main` | High | Add `TargetBranch string` field to `MergeOpts`, interpolate in conflict resolution prompt |
| MERGE_COMPLETE says "merged to main" regardless of actual target | High | Interpolate `targetBranch` into escalation summary at `dispatcher.go:1345` |
| FF merge into HEAD fails for per-bead overrides | Critical | Dual-path merge: `git merge --ff-only` for HEAD targets, `git fetch . <src>:refs/heads/<target>` for non-HEAD targets. See "Non-HEAD fast-forward merge" section. |
| `ffMergeEpicBranch` merges into HEAD, not epic's target branch | Critical | Add `targetBranch` param, resolve from epic metadata. Use `UpdateBranchRef` for non-HEAD targets. |
| `ffMergeEpicBranch` hardcodes "main" in error msg and rebase bead title | Medium | Interpolate `targetBranch` at lines 1495 and 1500 |
| `mergeAndComplete` conflict path missing TargetBranch in MergeOpts | High | Add `TargetBranch: targetBranch` to `ops.MergeOpts` at `dispatcher.go:1318` |
| Primary repo HEAD changed during swarm run | Medium | Document as precondition. Merge coordinator detects mismatch (HEAD vs target) and picks correct path. |

## Premortem findings (resolved)

### Tiger: MERGE_COMPLETE escalation hardcodes "merged to main"
**Location:** `dispatcher.go:1345`
**Fix:** Interpolate `targetBranch` into summary string. Manager parses branch name from escalation to determine which branch to push.

### Tiger: MergeOpts lacks TargetBranch for conflict resolution prompt
**Location:** `ops.go:490`, `ops.go:122`
**Fix:** Add `TargetBranch string` to `MergeOpts`. Interpolate in conflict prompt instead of hardcoded `"git rebase main"`.

### Tiger: FF merge always targets HEAD — breaks per-bead overrides
**Location:** `merge.go:196` (`worktreeRemoveAndFFMerge`), `dispatcher.go:1493` (`ffMergeEpicBranch`)
**Fix:** Dual-path merge strategy. Detect if target == HEAD in primary repo. HEAD targets: `git merge --ff-only` (existing). Non-HEAD targets: `git fetch . <branch>:refs/heads/<target>` (atomic fast-forward ref update, no checkout needed). Add `UpdateBranchRef` to `WorktreeManager` interface for the dispatcher's epic close path.

### Tiger: ffMergeEpicBranch has no target branch context
**Location:** `dispatcher.go:1477-1515`, called from `completeEpicClose:1521` via `tryCloseEpic:1416`
**Fix:** `tryCloseEpic` already calls `d.beads.Show(epicID)` at line 1428. Extract `detail.Metadata[MetaBranch]` or fall back to `d.cfg.DefaultBranch`. Thread through `completeEpicClose` → `ffMergeEpicBranch`.

### Tiger: mergeAndComplete conflict path omits TargetBranch
**Location:** `dispatcher.go:1318`
**Fix:** Add `TargetBranch: targetBranch` to `ops.MergeOpts` construction.

### Elephant: isBranchMerged called before per-bead metadata resolution
**Location:** `dispatcher.go:2542`
**Decision:** Use `d.cfg.DefaultBranch` instead of `"main"`. Accept the edge case where per-bead override beads may get one wasted assignment attempt — self-corrects at merge time. Adding per-bead I/O to the polling loop is not worth the complexity.

### Elephant: Metadata["branch"] is stringly typed
**Decision:** Accept for now. `const MetaBranch` prevents Go-side typos. Branch existence validation catches value typos at assignment time. Follow-up: promote to first-class `bd create --branch` field when the pattern is validated.

## Out of scope

- **Remote tracking setup** — assumes branches already exist locally and have upstream configured. User runs `git push -u origin <branch>` before starting the swarm.
- **Auto-creating branches** — if a bead specifies a branch that doesn't exist, the dispatcher escalates rather than creating it.
- **Cross-branch dependencies** — beads on different branches can depend on each other (ordering only). No special merge coordination across branches.
- **First-class `--branch` flag on `bd create`** — use `--metadata branch=X` for now. Promote to first-class field in a follow-up if the pattern proves useful.
