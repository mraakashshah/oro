# Keep Epic Branches Current With Main — Stop Ancestry-Rejection Loops

Date: 2026-07-21
Epic: (to be created) — factory-throughput fix
Scope: Tier 0 (operational bridge) + Tier 1 (dispatcher self-heal), ONE epic.

## Summary

The #1 cause of review rejections is epic branches diverging from `main` faster
than they are healed. `main` advances ~75–171 commits/hour; long-lived epic
branches do not track it, so within ~1 hour every epic has `main` as a
non-ancestor. Child beads built on a diverged epic fail the `--is-ancestor`
acceptance/merge check → REJECTED (or `epic_branch_prepare_failed` → bounce to
`open`). The dispatcher's existing auto-recovery (`ensureEpicRebaseChild`) closes
"Merged" without advancing the epic ref, so divergence never clears.

Fix in two tiers, one epic:
- **Tier 0 (bridge, no dispatcher code):** a committed, guarded script that
  merges `main` into every active epic, scheduled to run only while the factory
  is quiescent. Stops today's bleed without a rebuild.
- **Tier 1 (real fix, dispatcher code):** when an epic is diverged, the
  dispatcher attempts a clean in-memory merge of `main` into the epic during
  assignment prep. Clean → proceed; conflict → **fall through to the existing
  rebase-child/reject path, unchanged**. Because it runs continuously, epics
  never drift far, so merges stay clean and conflicts stay rare.

Conflicts are explicitly kept on the current behavior. No agent-driven conflict
resolution is added.

## Verified facts (root-cause investigation, 2026-07-21)

- `pkg/dispatcher/worktree_manager.go:PrepareBaseBranchForAssignment` only
  ref-fast-forwards (`git branch -f`) when the epic is `branchStrictlyBehind`;
  on `branchDiverged`/`branchContainsBase` it returns `(false, nil)` — no-op.
- `pkg/dispatcher/dispatcher.go:prepareEpicBranchForAssignment` (line ~6582)
  then detects divergence, calls `ensureEpicRebaseChild`, and
  `rejectEpicBranchPreparation` (bead → `open`, `recordAssignmentFailure`).
- The rebase-child closes `Merged` but the epic ref does not gain `main`
  (confirmed: `oro-t34e` closed Merged; `epic/oro-runtime-storage` stayed 75
  behind; the merged commit did not have `main` as an ancestor).
- `git merge-tree --write-tree main <epic>` is available (git 2.39.3) and is a
  perfect probe: rc=0 + prints the merged tree OID when clean; rc≠0 + prints
  conflict info when conflicting. It touches no working tree and no ref.
  Verified live: rc=0 on an already-merged epic, rc=1 on `epic/drc-factory`.

## Tier 0 — Operational bridge (no dispatcher code)

A committed script (`scripts/merge_main_into_epics.sh`, hardened from the
throwaway used during the 2026-07-21 manual pass) that, for each `epic/*`:
- skips dead epics (closed + 0 open children), reports them as delete candidates;
- fast-forwards `ahead==0` epics (`git branch -f epic/X main`);
- merges `main` into `ahead>0` epics; on conflict **aborts and reports**, never
  auto-resolves;
- **refuses to run unless the dispatcher is stopped** (guard: `oro status`
  reports `dispatcher: stopped`, and no live `quality_gate.sh`/worker procs),
  because merging epic refs under a live dispatcher risks the
  `branch_worktree_mismatch` corruption class.
- snapshots all epic tips to a file before any mutation (reversal insurance).

Scheduled during quiescence (host `launchd`/cron, or a Make target run at
factory-stop). Tier 0 is a bridge and is retired once Tier 1 is deployed.

**Guard is necessary but not sufficient — restart hazard.** "Dispatcher stopped"
prevents concurrent-mutation corruption, but moving an epic ref leaves any
`.worktrees/` pinned to an `agent/*` branch built on the OLD epic tip stale →
`branch_worktree_mismatch` on the next start. The runbook MUST require, after any
Tier 0 run, a `git worktree prune` + validation of `.worktrees/` (or that the run
happen only at a clean-worktree quiescence point) before the factory restarts.
Tip snapshots are taken before mutation for reversal.

## Tier 1 — Dispatcher self-heal (the real fix)

### New worktree-free clean-merge helper (`GitWorktreeManager`)

Add `MergeBaseIntoBranchIfClean(ctx, branch, baseBranch) (merged bool, err error)`:
1. If `base` is already an ancestor of `branch` → return `(false, nil)` (nothing
   to do; caller treats as clean/no-op).
2. Run `git merge-tree --write-tree <base> <branch>` (or `<branch> <base>`; base
   first so the merge is "base into branch").
   - **Conflict (rc≠0):** return `(false, nil)` — DO NOT mutate. The caller
     falls through to the existing divergence path.
   - **Clean (rc=0):** parse the written tree OID, create the merge commit with
     `git commit-tree <tree> -p <branch> -p <base> -m "merge main into <branch>"`,
     then `git update-ref refs/heads/<branch> <newcommit> <oldbranchsha>`
     (compare-and-swap on the old sha so a concurrent mutation fails safely).
   - Return `(true, nil)`.
- Entirely ref-level; no worktree checkout, no partial state. Deterministic and
  restart-safe (a crash before `update-ref` leaves the epic untouched).
- Version guard: if `merge-tree --write-tree` is unsupported, return
  `(false, nil)` (degrade to today's behavior), logged once.

### Wire into `prepareEpicBranchForAssignment`

The merge attempt is inserted AFTER the `isEpicRebaseChildForBase` short-circuit
(dispatcher.go ~6610) — so a rebase-child bead assigned on its own epic is still
short-circuited and never triggers a self-merge of the epic it exists to repair —
and BEFORE `ensureEpicRebaseChild`/reject. The type assertion and the config gate
are both explicit; on any failure/conflict, execution falls through to the exact
current path:
```
if !diverged { return true }
if d.isEpicRebaseChildForBase(ctx, beadID, baseBranch) {
    log epic_rebase_child_prepare_diverged; return true         // UNCHANGED
}
if d.cfg.AutoMergeEpicBase {
    if merger, ok := d.worktrees.(assignmentBaseBranchMerger); ok {
        merged, err := merger.MergeBaseIntoBranchIfClean(ctx, baseBranch, d.cfg.DefaultBranch)
        if err == nil && merged {
            log epic_branch_merged_main
            return true            // assignment proceeds on the now-current epic
        }
        // err != nil OR conflict (merged==false): fall through UNCHANGED
    }
}
// ... existing ensureEpicRebaseChild + rejectEpicBranchPreparation (byte-for-byte today) ...
```
- Conflict/error path is byte-for-byte the current behavior. No new conflict handling.
- Helper params are named `(branch, incomingBase)` to avoid the transposition trap:
  the dispatcher calls the EPIC `baseBranch` while the helper's incoming side is
  `main` (`d.cfg.DefaultBranch`). Call maps (epic, main) → merge main INTO epic.

### Config / enable — sequenced in two commits

- Add `AutoMergeEpicBase bool` to `dispatcher.Config` and declare the new
  `assignmentBaseBranchMerger` interface beside `assignmentBaseBranchPreparer`/
  `assignmentBaseBranchSafetyChecker` (dispatcher.go ~375-381).
- **Commit A (helper + wiring):** default **FALSE** in `withDefaults`
  (`boolDefault(out.AutoMergeEpicBase, false)`). Behavior is identical to today;
  all existing tests stay green. `withDefaults` is the single source of the
  default — `cmd_start.go` does NOT set it (avoids the double-enable-point trap).
- **Commit B (flip, LAST):** change the `withDefaults` default to TRUE **in the
  same commit that updates the existing divergence test** (below). Only then can
  a cleanly-diverged epic self-heal, and the test asserting the old contract is
  corrected atomically with the behavior change.

### Existing test that MUST change with the flip (blocking)

`pkg/dispatcher/dispatcher_test.go` `TestEpicRebaseChildAssignableOnDivergedBranch/"ordinary child remains rejected with cooldown"` (~lines 765-781) wires a REAL
`GitWorktreeManager` over a **cleanly** diverged repo and asserts the ordinary
child is **rejected + cooldown recorded**. Tier 1 auto-merge inverts that — a
clean divergence now self-heals and the child becomes assignable. This test must
be updated in Commit B to assert: ordinary child on a cleanly-diverged epic →
**assignable, no cooldown, and `main` is an ancestor of the epic after prep**;
plus a NEW subtest for a genuinely **conflicting** divergence that still →
**rejected + cooldown** (proving the fall-through is intact). The
`"rebase child remains assignable"` subtest (~748-763) must stay green (guards the
short-circuit ordering).

## Epic acceptance test (machine-verifiable)

```
Cmd: go test ./pkg/dispatcher -run 'TestEpicBranchCurrency' -count=1
Assert: on a cleanly-diverged epic, prepareEpicBranchForAssignment merges main
into the epic and returns assignable; git merge-base --is-ancestor main <epic>
holds afterward; a conflicting-divergence epic still falls through to
rejectEpicBranchPreparation with cooldown (fall-through unchanged).
```

## Risks

- **Epic→main integration with merge commits (Tiger — verified mechanically OK,
  still needs a test).** Merging `main` INTO an epic adds merge commits to the
  epic. Code review confirms `ffMergeEpicBranch` uses `git merge --ff-only` for a
  `main` target and `update-ref` (after `--is-ancestor`) for other targets — both
  replay any-shaped history when the target is an ancestor; there is **no squash**
  in this path (earlier "squash" wording was wrong). So merge commits in the epic
  do not break epic-close. Required regression test:
  `go test ./pkg/dispatcher -run 'TestEpicCloseWithMergeCommitInEpic'` — build an
  epic carrying a "merge main into epic" commit, make it is-ancestor of the close
  target, assert `completeEpicClose` ff-merges to `main` (exit 0); and when `main`
  advanced, assert it creates/retries the rebase child instead (no regression).
- **Concurrent ref mutation (Paper Tiger).** The `update-ref <new> <old>` CAS
  rejects if the epic moved under us (another child merged, or a worker pushed);
  on CAS failure return `(false,nil)` and fall through. No lost updates.
- **`merge-tree` clean but semantically wrong (Paper Tiger).** A textually-clean
  merge can still be semantically off. Mitigation: this is exactly what the
  child bead's own acceptance test + QG already validate downstream; the merge
  only makes `main` an ancestor, it does not bypass any gate.
- **Elephant — this is still not the merge-model rewrite.** `oro-0352` remains
  the durable fix (dispatcher-owned rebase-onto-exact-target + CAS). Tier 1 keeps
  the current model alive and healthy until then; it is not a replacement.

## Out of scope

- Agent-driven conflict resolution (explicitly declined — conflicts keep the
  current rebase-child/reject behavior).
- Changing the epic→main integration/close model.
- Deleting dead epic branches (handled operationally).

## Affected code

- `pkg/dispatcher/worktree_manager.go` — `MergeBaseIntoBranchIfClean` + a
  `assignmentBaseBranchMerger` interface.
- `pkg/dispatcher/dispatcher.go` — wire into `prepareEpicBranchForAssignment`;
  `AutoMergeEpicBase` config + default.
- `cmd/oro/cmd_start.go` — NO change (flag is defaulted in `withDefaults` only;
  cmd_start must not set it, to keep a single enable point).
- `scripts/merge_main_into_epics.sh` + `docs/runbooks/` — Tier 0.
