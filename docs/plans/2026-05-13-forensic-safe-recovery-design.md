# Forensic-Safe Recovery Design

## Goal

Make Oro recovery preserve inspectable work by default. Restart, shutdown,
quality-gate failure, stale branch, and reassignment paths must not delete,
force-delete, or auto-commit ambiguous worker state. When Oro cannot prove a
recovery state is safe, it must create a durable recovery quarantine that is
visible through `oro health`, `oro status`, and `oro monitor`.

## Non-Goals

- This phase does not add broad self-healing for ambiguous recovery state.
- This phase does not change project-name sanitization, bead foreign-key
  enforcement, or unrelated audit findings.
- This phase does not make `oro monitor --act` resolve quarantines or take any
  other action while a recovery quarantine is open. Quarantines are
  operator-owned.

## Recovery Ownership Model

Dispatcher-owned recovery state includes:

- assignment rows with `status IN ('active', 'requeued', 'quarantined')`
- `agent/<bead>` branches
- `.worktrees/<bead>` worktrees
- in-memory maps restored from durable assignment rows

The system must treat these as evidence, not disposable cleanup.

## Durable State

The runtime schema adds `recovery_quarantines`:

```sql
CREATE TABLE IF NOT EXISTS recovery_quarantines (
    id INTEGER PRIMARY KEY,
    bead_id TEXT NOT NULL,
    assignment_id INTEGER,
    worker_id TEXT,
    worktree TEXT,
    branch TEXT,
    reason TEXT NOT NULL,
    details TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'open',
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    resolved_at TEXT
);
```

Open quarantines are idempotent per `(bead_id, reason, status='open')`.
Creating an already-open quarantine updates inspectable fields and returns the
existing row. Resolving a quarantine marks it `resolved` and sets
`resolved_at`.

Assignment rows use `status='quarantined'` when recovery cannot prove the
worktree/branch pair is safe. Quarantined rows must not be hidden by marking
them `completed`.

Dispatcher helpers and `oro recovery list|resolve` list open quarantines and
resolve a quarantine idempotently after an operator has preserved, merged, or
intentionally discarded the work. The operational resolution path is documented
in `docs/runbooks/forensic-safe-recovery.md`.

## Invariants

1. `Remove(path)` removes only. It must not run `git add`, `git commit`, or any
   other mutation inside the worker worktree before removal.
2. Normal branch deletion uses `git branch -d <branch>`.
3. Force branch deletion is only available through an explicit API and may only
   be used by tested cleanup after a successful merge proof.
4. `oro stop` changes active assignments to `requeued`, resets active beads to
   `open`, and preserves their worktrees and branches.
5. Startup restores active/requeued assignments only when both expected branch
   and worktree exist.
6. Startup creates open recovery quarantines for missing worktree, missing
   branch, branch/worktree mismatch, unmerged orphan branch, and unsafe stale
   branch conditions.
7. A quarantined bead must not be assigned by automatic paths until the
   quarantine is resolved.
8. Any path without positive merge proof preserves or quarantines the
   worktree/branch instead of deleting evidence. This includes QG/pre-merge
   failures, closed-bead merge aborts, failed merge-conflict recovery, mid-run
   task-to-epic type changes, and failed external-close recovery.
9. `oro monitor --act` must refuse all actions while
   `recovery_quarantine_open` is present.
10. Health/status output must include a finding with a concrete recommended
    action whenever open recovery quarantines exist.
11. `git worktree prune` must only prune Git metadata. Oro must not remove
    unregistered `.worktrees/<bead>` directories because they may contain
    recovery-owned work after a crash.

## State Transitions

### Startup

1. Load assignment rows with `status IN ('active', 'requeued')`.
2. For each row, compute expected branch `agent/<bead_id>`.
3. If worktree is empty or missing, mark assignment `quarantined`, create an
   open quarantine, and leave the bead visible only through health/status.
4. If branch is missing or branch lookup fails, mark assignment `quarantined`
   and create an open quarantine.
5. If worktree and branch both exist but the worktree is not checked out on
   the expected `agent/<bead>` branch, mark assignment `quarantined` with
   reason `branch_worktree_mismatch`.
6. If worktree and branch both exist and match, restore `worktreeByBead`,
   `attemptCounts`, and `handoffCounts`.
7. Reopen dispatcher-owned in-progress beads only for restored assignments.

### Assignment

Before creating a fresh worktree:

1. If `agent/<bead>` does not exist, create from the resolved base branch.
2. If the branch exists and an existing worktree is tracked and present, reuse
   it only when the worktree is checked out on the expected `agent/<bead>`
   branch. Otherwise quarantine with reason `branch_worktree_mismatch`.
3. If the branch exists and is already merged into the base branch, safe-delete
   with `git branch -d`, then create a fresh worktree.
4. If the branch exists and is unmerged or ambiguous, create an open recovery
   quarantine and do not assign the bead.
5. If creating a fresh worktree fails because Git reports an already-existing
   branch/worktree, cleanup may run `git worktree remove --force` only in the
   `pruneStale` retry path after assignment recovery has ruled out a tracked
   resumable worktree for that bead.

### Shutdown

1. Ask active workers for graceful shutdown.
2. Requeue active assignments and reset their beads to `open`.
3. Do not remove requeued assignment worktrees.
4. Continue to cancel ops agents and abort in-flight merges, preserving worker
   work where merge proof is absent.

### QG Failure

Worker-level retry keeps the assignment active and reuses the same worktree.
When automation releases a worker because retry cannot continue, it must
preserve the worktree/branch. Pre-merge QG failures mark the assignment
`requeued`, reset the bead to `open`, and make the next assignment reuse the
stored worktree.

### Reassignment

When a worker is reassigned from one bead to another without a clean `DONE`,
the previous assignment is requeued if the bead is still open. The previous
worktree and branch remain tracked by bead ID so a later assignment can resume
or inspect the work. If the bead was externally closed, the assignment may be
completed, but cleanup still must not delete ambiguous work without a merge
proof.

### Stale Active Assignment

When a connected dispatcher finds an active assignment whose worker is no
longer connected, it creates a `stale_active_assignment` recovery quarantine
instead of marking the assignment abandoned. This surfaces the branch/worktree
through health even if the bead is never picked again.

### Merge Success Cleanup

After the dispatcher proves the branch has landed on the target branch, cleanup
may remove the worktree and safe-delete the merged branch. If safe deletion
fails because git cannot prove the branch is merged, cleanup must log the
failure and preserve the branch.

## Health and CLI Contract

`factoryhealth.Metrics` includes:

- `recovery_quarantines_open`

`factoryhealth.Finding` includes:

- `code: recovery_quarantine_open`
- `severity: critical`
- `component: recovery`
- recommended action naming `oro health --json`, assignment/quarantine
  inspection, and manual resolution after preserving or merging work

Human `oro status` prints a recovery summary when health includes open
quarantines. `oro health --json` exposes the metric and finding. `oro recovery
list|resolve` provides the operator resolution surface. `oro monitor` observe
mode logs the finding and action. `oro monitor --act` performs no mutation
beyond printing the finding while the recovery quarantine finding is present.
This contract applies to both local CLI fallback health and the running
dispatcher health path in `pkg/dispatcher/health.go`.

## Acceptance Tests

Epic verification command:

```sh
go test ./cmd/oro ./pkg/dispatcher/... ./pkg/factoryhealth -count=1
```

Expected result: all tests pass, including recovery tests that prove worktree
removal does not auto-commit, normal branch deletion is safe, startup quarantine
is durable and visible, shutdown preserves requeued worktrees, pre-merge QG
failure preserves work, and monitor refuses automation when recovery quarantine
state is open.

## Task Tree

Epic: Forensic-safe recovery

1. Worktree manager safety
   - Test: `pkg/dispatcher/worktree_manager_test.go:TestRemoveDoesNotAutoCommitBeforeWorktreeRemoval`
   - Cmd: `go test ./pkg/dispatcher -run 'TestRemoveDoesNotAutoCommitBeforeWorktreeRemoval|TestDeleteBranchUsesSafeDelete|TestForceDeleteBranchUsesExplicitAPI|TestGitWorktreeManager_Prune' -count=1`
   - Assert: `Remove` invokes only `git worktree remove`; normal branch delete
     uses `-d`; force delete is isolated behind an explicit method; `Prune`
     does not remove unregistered worktree directories.
   - Read: `pkg/dispatcher/worktree_manager.go:GitWorktreeManager`,
     `pkg/dispatcher/stale_branch_test.go`
   - Signature: `func (g *GitWorktreeManager) ForceDeleteBranch(ctx context.Context, branch string) error`
   - Edges: missing worktree path is idempotent; unmerged branch must make
     `DeleteBranch` fail through git.

2. Durable recovery quarantine helpers
   - Test: `pkg/dispatcher/recovery_quarantine_test.go:TestCreateRecoveryQuarantineIdempotent`
   - Cmd: `go test ./pkg/dispatcher -run 'TestCreateRecoveryQuarantineIdempotent|TestListAndResolveRecoveryQuarantine|TestSchemaApplyAddsRecoveryQuarantinesTable' -count=1`
   - Assert: repeated quarantine creation for the same bead/reason returns one
     open row and marks the assignment `quarantined`; list/resolve helpers
     expose open rows and resolve them idempotently.
   - Read: `pkg/protocol/schema.go:SchemaDDL`,
     `pkg/dispatcher/dispatcher.go:processQuarantined`
   - Signature: `func (d *Dispatcher) createRecoveryQuarantine(ctx context.Context, q recoveryQuarantine) (int64, error)`
   - Edges: nil assignment id is allowed; resolved quarantines do not block a
     new open row.

3. Startup and assignment recovery
   - Test: `pkg/dispatcher/recovery_quarantine_test.go:TestRestoreStateQuarantinesMissingWorktreeDurably`
   - Cmd: `go test ./pkg/dispatcher -run 'TestRestoreStateQuarantinesMissingWorktreeDurably|TestRestoreStateQuarantinesBranchWorktreeMismatch|TestProcessQuarantinedContinuesAfterRowFailure|TestDeleteStaleAgentBranchQuarantinesUnmergedBranch|TestFilterRecoveryQuarantinedBeadsFailsClosedOnQueryError' -count=1`
   - Assert: inconsistent startup state creates an open quarantine and leaves
     the assignment `quarantined`; stale unmerged branch is not deleted and is
     not assigned; quarantine filtering fails closed on DB errors.
   - Read: `pkg/dispatcher/dispatcher.go:restoreState`,
     `pkg/dispatcher/dispatcher.go:classifyAssignment`,
     `pkg/dispatcher/dispatcher.go:processQuarantined`,
     `pkg/dispatcher/dispatcher.go:deleteStaleAgentBranch`,
     `pkg/dispatcher/worktree_manager.go:DeleteBranch`
   - Edges: missing branch, branch check failure, missing worktree path, and
     unsafe stale branch each produce named reasons.

4. Closed-bead GC preservation
   - Test: `pkg/dispatcher/recovery_quarantine_test.go:TestGCClosedWorktreesSkipsBeadsWithOpenRecoveryQuarantine`
   - Cmd: `go test ./pkg/dispatcher -run TestGCClosedWorktreesSkipsBeadsWithOpenRecoveryQuarantine -count=1`
   - Assert: closed-bead worktree GC skips beads with open recovery quarantines
     and logs `gc_skipped_recovery_quarantined`.
   - Read: `pkg/dispatcher/bead_tracker.go:gcWorktrees`,
     `pkg/dispatcher/worktree_manager.go:GCClosedWorktrees`
   - Edges: DB query failure must fail closed and skip GC.

5. Shutdown and QG preservation
   - Test: `pkg/dispatcher/pre_merge_qg_lifecycle_test.go:TestPreMergeQGFailurePreservesRejectedWork`
   - Cmd: `go test ./pkg/dispatcher -run 'TestShutdownPreservesRequeuedWorktrees|TestPreMergeQGFailurePreservesRejectedWork|TestReleasePriorAssignmentPreservesWorktreeAndBranch|TestAbandonStaleActiveAssignments_QuarantinesRecoveryState' -count=1`
   - Assert: graceful shutdown does not remove requeued worktrees; pre-merge QG
     failure requeues the assignment and preserves the stored worktree;
     reassignment requeues the prior assignment and preserves its branch.
   - Read: `pkg/dispatcher/dispatcher.go:shutdownSequence`,
     `pkg/dispatcher/dispatcher.go:handlePreMergeQGFailure`,
     `pkg/dispatcher/dispatcher.go:releasePriorAssignment`,
     `pkg/dispatcher/dispatcher.go:abandonStaleActiveAssignments`
   - Edges: closed-bead stale QG results still complete safely; successful
     merge cleanup remains allowed.

6. Non-merge cleanup preservation
   - Test: `pkg/dispatcher/merge_closed_bead_test.go:TestMergeAndCompleteAbortsOnClosedBead`
   - Cmd: `go test ./pkg/dispatcher -run 'TestMergeAndCompleteAbortsOnClosedBead|TestMergeConflictFailurePreservesWorktree|TestExternalCloseEscalatesOnMergeConflict|TestAssignBeadReusesWorktreeOnlyIfBranchMatches' -count=1`
   - Assert: closed-bead merge aborts, failed merge-conflict resolution,
     failed external-close recovery, and runtime worktree branch mismatch all
     preserve/quarantine work instead of removing it.
   - Read: `pkg/dispatcher/dispatcher.go:mergeAndComplete`,
     `pkg/dispatcher/dispatcher.go:handleMergeConflictResult`,
     `pkg/dispatcher/dispatcher.go:finalizeExternalClose`,
     `pkg/dispatcher/dispatcher.go:assignBead`
   - Edges: successful merge/external-close recovery cleanup remains allowed.

7. Health/status/monitor/recovery CLI integration
   - Test: `pkg/factoryhealth/health_test.go:TestEvaluateRecoveryQuarantineOpenIsUnsafe`
   - Cmd: `go test ./cmd/oro ./pkg/factoryhealth ./pkg/dispatcher -run 'RecoveryQuarantine|MonitorAct|Status|ApplyHealth' -count=1`
   - Assert: `oro health --json` exposes quarantine metrics/findings; human
     status summarizes unsafe recovery; monitor observe logs the finding and
     `--act` refuses recovery mutation while quarantine is open; `oro recovery
     list|resolve` provides the operator resolution surface.
   - Read: `pkg/factoryhealth/health.go:Evaluate`,
     `cmd/oro/cmd_health.go:loadLocalFactoryHealth`,
     `cmd/oro/cmd_status.go:formatStatusHealthSummary`,
     `cmd/oro/cmd_monitor.go:actOnMonitorHealth`,
     `cmd/oro/cmd_recovery.go`
   - Edges: daemon stopped with only open quarantines is unsafe, not cleanly
     stopped.

## Adversarial Review Notes

The structural risk is a task passing in isolation while the dispatcher still
auto-deletes state through a different path. The integration points that must
be touched are:

- `pkg/dispatcher/worktree_manager.go`
- `pkg/dispatcher/worker_pool.go`
- `pkg/dispatcher/dispatcher.go`
- `pkg/dispatcher/bead_tracker.go`
- `pkg/protocol/schema.go`
- `pkg/factoryhealth/health.go`
- `pkg/dispatcher/health.go`
- `cmd/oro/cmd_health.go`
- `cmd/oro/cmd_status.go`
- `cmd/oro/cmd_monitor.go`
- `cmd/oro/cmd_recovery.go`
- tests in `pkg/dispatcher`, `pkg/factoryhealth`, and `cmd/oro`
