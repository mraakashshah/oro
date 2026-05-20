# Autonomous Factory Reliability v2 Design

Date: 2026-05-20

## Goal

Create the next hardening epic for Oro's autonomous factory. This phase should
remove the known sources of avoidable operator intervention before running more
managerless dogfood:

1. Branch cleanup must use the branch's active target, not always `main`.
2. Codex/Claude ops-review spawn/config/runtime failures must become explicit,
   recoverable incidents.
3. No-op merges must close tasks cleanly when the target already contains the
   work.
4. `oro monitor --act` dogfood must be repeatable from a scripted seed/run/assert
   harness.
5. Long subprocess phases must keep proving progress with focused regression
   tests.

The desired end state is that Oro can run these beads with max workers 2, and
when it cannot heal something, `oro health`, `oro status`, `oro monitor`, and
the task/ops records name the exact blocked condition and action.

## Non-Goals

- Do not reintroduce a resident manager pane as a correctness dependency.
- Do not make `oro monitor --act` auto-resolve failed/stale ops runs.
- Do not force-delete agent branches during startup, retry, stale branch cleanup,
  or dogfood cleanup.
- Do not expand Harness v6 or unrelated schema work in this epic.
- Do not make the dogfood harness depend on external network access or
  long-running production factory state.

## Source Research

Files and behavior checked before writing this design:

- `pkg/dispatcher/worktree_manager.go`
  - `Remove` removes worktrees without auto-committing.
  - `DeleteBranch` uses safe `git branch -d`.
  - `DeleteBranchMergedInto(ctx, branch, targetBranch)` already proves
    `branch` is an ancestor of `targetBranch` before safe deletion.
- `pkg/dispatcher/dispatcher.go`
  - `removeWorktreeAndClearTracking` already accepts a `targetBranch` and calls
    `DeleteBranchMergedInto` after successful merge/no-op cleanup.
  - `filterAlreadyMergedBranches` calls `isBranchMerged`, whose proof path still
    uses `d.cfg.DefaultBranch`.
  - `deleteStaleAgentBranch` calls plain `DeleteBranch` before assignment. That
    can quarantine an agent branch already merged into `epic/<parent>` but not
    yet merged to `main`.
  - `handleNoopMerge` closes beads and does not escalate, but tests do not yet
    assert assignment cleanup, target-aware branch cleanup, or no reopen across
    all target cases.
- `pkg/dispatcher/epic_branch.go`
  - `ResolveEpicBranch` already resolves the active target from bead metadata
    and the parent epic chain.
- `pkg/dispatcher/stale_branch_test.go`
  - Existing stale-branch tests cover checked-out branch quarantine and branch
    mismatch preservation, but not "merged to epic target, not main".
- `pkg/dispatcher/dispatcher_test.go`
  - `TestMergeAndCompleteNoopMergeClosesBeadWithoutEscalation` pins the basic
    no-op close behavior.
  - `TestIsBranchMerged_DefaultBranch` pins default-branch proof, which is now
    too narrow for epic-targeted work.
- `pkg/dispatcher/ops_runs.go`, `pkg/dispatcher/dispatcher.go`,
  `pkg/factoryhealth/health.go`, `cmd/oro/cmd_ops.go`, and
  `cmd/oro/cmd_monitor.go`
  - Durable `ops_runs` already exist.
  - `oro health` and `oro monitor --act` already surface failed/stale ops runs
    and refuse to mutate them automatically.
  - The gap for this phase is ensuring every ops review/spawn/config failure is
    guaranteed to land in those records with runtime/model/error detail.
- `pkg/ops/ops.go` and `pkg/ops/exec_spawner.go`
  - Spawn errors are returned as failed `ops.Result` values.
  - Runtime router errors include unknown runtime and unconfigured runtime
    cases.
- `pkg/worker/worker.go` and `pkg/worker/worker_progress_test.go`
  - Workers emit `running_progress` while a subprocess is active.
  - Writes use bounded deadlines so blocked progress writes do not wedge the
    context watcher.
  - Current tests cover a generic long subprocess and blocked writes, but not
    the named go test, quality gate, and Codex/plain-text subprocess phases.
- `cmd/oro/cmd_monitor.go`
  - Hidden `--iterations` gives a useful hook for finite dogfood assertions.

## Invariants

1. **Target branch is authoritative.** If an assignment targets `epic/<id>`,
   cleanup and "already merged" checks must prove against `epic/<id>`, not
   `main`.
2. **Ambiguous branch state is preserved.** If Oro cannot prove an agent branch
   is merged into the active target, it must not delete it. Recovery quarantine
   is acceptable only when the branch/worktree state is genuinely unsafe or
   inconsistent.
3. **No-op merge is terminal success.** If the merge layer proves the target
   already contains the branch work, the assignment completes, the bead closes,
   ops agents are cancelled, and no retry/reopen loop starts.
4. **Ops failures are durable incidents.** A Codex/Claude ops review,
   decompose, or one-shot escalation spawn/config/runtime failure must create or
   complete an `ops_runs` row in `failed` status with enough detail for
   `oro ops retry` or `oro ops resolve`.
5. **Monitor does not guess.** `oro monitor --act` may maintain worker count,
   resume paused dispatch, and restart stalled daemons, but it must stop at
   recovery quarantines and failed/stale ops runs.
6. **Progress evidence must be bounded.** Long worker subprocess phases emit
   periodic `running_progress` messages and never block indefinitely on a
   dispatcher socket write.
7. **Dogfood is reproducible.** A scripted harness can seed known work, run a
   finite `monitor --act` loop against isolated state, and assert post-run
   invariants.

## State Transitions

### Stale Agent Branch Before Assignment

Current risk:

```text
open bead -> assign -> agent/<bead> exists
  -> DeleteBranch(agent/<bead>) checks default Git safety/upstream
  -> fails because branch is not merged to main
  -> unsafe_stale_branch quarantine, even if merged into epic/<parent>
```

Required transition:

```text
open bead -> resolve target branch
  -> agent/<bead> exists
  -> if branch is merged into target: safe-delete with git branch -d
  -> if branch is not merged into target: preserve/quarantine only if unsafe
  -> create or reuse worktree from target
```

### Already-Merged Candidate Filtering

Current risk:

```text
ready child bead, agent/<bead> ancestor of epic/<parent> but not main
  -> isBranchMerged(default branch) returns false
  -> bead can be reassigned or stale branch path can quarantine it
```

Required transition:

```text
ready child bead -> resolve assignment target -> prove agent/<bead> ancestor of target
  -> close bead as already present on target
  -> do not reassign
```

The empty-branch guard remains: if the branch tip equals its merge-base with the
target, the branch has no work to prove and must not be treated as done.

### Ops Review Spawn Failure

Required transition:

```text
worker READY_FOR_REVIEW
  -> dispatcher creates/runs ops review
  -> runtime unknown/unconfigured, spawn failure, timeout, or non-zero exit
  -> assignment/work is preserved
  -> ops_runs row status=failed, type=review, bead_id set, runtime/model/error set
  -> health/status/monitor show ops_run_failed with retry/resolve action
  -> monitor --act performs no mutation while blocked
```

Approved, rejected, and intentionally blocked review outcomes must also complete
the associated review ops row:

```text
review approved -> ops_runs status=resolved -> merge/no-op pipeline continues
review rejected -> ops_runs status=resolved -> assignment is requeued with feedback
review env/infra blocked -> ops_runs status=failed -> health/status/monitor expose action
```

No review path may leave a `running` ops row after the review result is handled.

### No-Op Merge

Required transition:

```text
review accepted -> merge layer returns Noop=true
  -> complete active assignment
  -> close bead once
  -> release worker
  -> cancel active ops agents for bead
  -> log merge_noop with branch,target,sha
  -> cleanup worktree and branch only via target-aware proof
  -> maybe auto-close epic and trigger memory/dream
```

No step may call `updateBeadStatus(..., "open")` after no-op proof.

### Dogfood Harness

Required transition:

```text
empty isolated state -> seed known docs/test-only beads
  -> build isolated oro binary
  -> start factory with workers=2 max-workers=2
  -> run oro monitor --target 2 --max-workers 2 --interval <short> --act --iterations N
  -> stop factory
  -> assert no active assignments, no open recovery quarantines, no open QG incidents,
     no failed/stale ops runs, and no ready seeded work remains
```

The harness must print paths to the isolated state directory and monitor log.

## Premortem

### Tigers

- **Wrong branch proof deletes or closes real work.** Target-aware proof must be
  explicit in tests: `agent/<child>` merged into `epic/<parent>` but not `main`
  should be clean; `agent/<child>` diverged from `epic/<parent>` should be
  preserved.
- **Ops failure disappears before health sees it.** Review spawn/config failure
  must be tested at the dispatcher boundary, not only in `pkg/ops`, because the
  dispatcher owns assignment visibility and ops-run persistence.
- **Dogfood mutates the developer's live factory.** The harness must default to
  isolated state and an isolated binary. A live two-hour run can still be manual,
  but the regression harness must be finite and disposable.

### Paper Cuts

- Existing no-op tests prove the happy path but do not verify cleanup target or
  assignment row state.
- Monitor output already blocks on failed ops runs, but a missing ops-run row
  would make the failure look like a generic stall.
- Worker progress tests use synthetic processes. They need named phase tests so
  future refactors do not accidentally remove progress from QG or Codex paths.

### Elephants

- This phase does not solve all open deferred work in the repo.
- This phase does not guarantee that Codex/Claude can always fix a failed ops
  incident; it guarantees the incident is visible, retryable, and non-ambiguous.
- This phase does not replace the full two-hour dogfood. It adds a repeatable
  smoke dogfood so the two-hour run has a baseline.

## Design

### 1. Target-Aware Branch Cleanup

Add a target parameter to stale branch and already-merged proof paths:

- `filterAlreadyMergedBranches` resolves each candidate's active target by using
  `bead.MetaBranch`, `ResolveEpicBranch`, and `d.cfg.DefaultBranch`.
- Replace `isBranchMerged(ctx, beadID)` with `isBranchMergedInto(ctx, beadID,
  targetBranch)`.
- `isBranchMergedInto` preserves the existing empty-branch guard, but computes
  merge-base and ancestry against `targetBranch`.
- `deleteStaleAgentBranch` accepts `targetBranch` and calls
  `DeleteBranchMergedInto(ctx, branch, targetBranch)`.
- If `DeleteBranchMergedInto` fails because the branch is not an ancestor of the
  target, preserve the branch and create a recovery quarantine only when the
  assignment cannot safely proceed. Do not force-delete.

Logging requirements:

- `stale_agent_branch_deleted` includes `branch` and `target`.
- `bead_branch_already_merged` includes `branch`, `target`, and close reason.
- `stale_agent_branch_quarantined` includes `branch`, `target`, and reason.
- The `CloseBead` reason must name the actual target branch, for example
  `branch already merged to epic/oro-parent`, not `main` unless `main` was the
  proof target.

### 2. No-Op Merge Closure

No-op merge is already present, but the v2 work should harden its boundaries:

- Assert the active assignment row is completed before `CloseBead`.
- Assert no `updateBeadStatus(..., "open")` occurs.
- Assert no escalation is emitted.
- Assert `removeWorktreeAndClearTracking` uses the merge target.
- Assert target branch defaults to `d.cfg.DefaultBranch` only when no target was
  provided.

This should be a test-hardening and bug-fix task, not a rewrite of the merge
pipeline.

### 3. Managerless Escalation Hardening

Extend ops-run coverage to review/spawn failure paths:

- Before running an ops review, create or associate a durable `ops_runs` row
  with `type=review` and the bead/worker/runtime/model context.
- On spawn/config/runtime failure, complete that row as `failed`.
- On approved or rejected review verdicts, complete that row as `resolved`.
- On env-blocked or infra-blocked review failures, complete that row as
  `failed`.
- Preserve the assignment/worktree for inspection or retry.
- Health/status/monitor should already surface `ops_run_failed`; add tests that
  fail if the failed review path only produces logs or a generic stuck worker.
- `oro ops retry <id>` should supersede the failed row and route the same review
  work again where possible.

`routeOpsRun` must support `OpsReview`. A retry of a failed review row must
reconstruct the review context from durable dispatcher state: bead ID, worker ID
where available, assignment/worktree/branch/target branch where available, and
review runtime/model. If the assignment context is no longer available, retry
must fail explicitly with a replacement failed row whose error says the review
context is unavailable; it must not silently report `routed=false` with no
action.

Review rows are not skipped for docs-only auto-approvals. If the ops layer
auto-approves a docs-only diff, the dispatcher still resolves the review ops row
immediately. This keeps the lifecycle uniform and avoids heuristic drift.

The same durable failure rule applies to routed decompose and generic one-shot
escalation runs. Existing decompose/escalation ops-run paths may be reused, but
Task B must add tests proving unknown/unconfigured runtime failures complete
the relevant row as `failed` and surface through health/status/monitor.

The existing `ops_runs` semantics remain:

- `running`, `failed`, and `stale` are blocking.
- `resolved` and `superseded` are not blocking.
- `monitor --act` logs `blocked_by_ops_runs` and does not mutate those rows.

### 4. Long Subprocess Progress Regression Tests

Keep the current production mechanism unless tests reveal a real gap:

- `recordSpawnedProc` starts timing any worker subprocess.
- `watchContext` sends `running_progress` every context poll interval while the
  process is alive.
- `trySendSubprocessProgress` uses a short write deadline.

Add focused tests for:

- worker assignment subprocess using a go-test-like command,
- QG subprocess after assignment exit,
- Codex/plain-text runtime subprocess,
- blocked progress writes during those phases.

These tests should run quickly with fake processes or helper commands. They
must not call the real network-backed Codex or Claude CLIs.

### 5. Repeatable Dogfood Harness

Add a script and tests around a finite run:

- Script name: `scripts/oro-dogfood-smoke.sh`.
- Defaults:
  - isolated temp state root,
  - isolated temp binary,
  - workers=2,
  - max-workers=2,
  - monitor iterations=3,
  - short interval suitable for CI/manual smoke.
- Scenario flag:
  - `--scenario smoke` seeds normal liveness work.
  - `--scenario reliability-v2` additionally seeds deterministic fixtures for
    target-aware cleanup, no-op merge closure, and ops-review failure visibility.
- The script seeds docs/test-only tasks that can complete without external
  services when the installed runtimes are available; tests may stub the command
  layer for determinism.
- The script always stops the factory before exiting.
- Assertions:
  - no active assignments remain,
  - no open `recovery_quarantines`,
  - no open QG incidents,
  - no failed/stale ops runs,
  - seeded work is closed or explicitly reported as unresolved with a named
    finding.

The default smoke scenario is a liveness check. The `reliability-v2` scenario is
the integration check for this epic and must prove that the target-aware cleanup,
no-op merge, and ops-run failure surfaces were exercised by inspecting event
logs, task close reasons, and health/ops output in the isolated state.

The full two-hour live dogfood remains:

```bash
go build -o "$(mktemp -d)/oro" ./cmd/oro
oro start --workers 2 --max-workers 2 --detach
oro monitor --target 2 --max-workers 2 --interval 60s --act 2>&1 | tee /tmp/oro-reliability-v2-dogfood.log
oro health --json
oro status --json
oro stop
```

## Acceptance Test Matrix

1. Branch merged into epic target, not main, is treated as merged:
   - `agent/oro-child` has commits.
   - `agent/oro-child` is an ancestor of `epic/oro-parent`.
   - `agent/oro-child` is not an ancestor of `main`.
   - Candidate filtering closes or skips the task as already on target and does
     not create recovery quarantine noise.

2. Stale branch cleanup uses target proof:
   - Existing `agent/oro-child` is merged into `epic/oro-parent`.
   - Fresh assignment cleanup calls `DeleteBranchMergedInto(agent/oro-child,
     epic/oro-parent)`.
   - Safe branch deletion uses `git branch -d`.

3. Diverged stale branch is preserved:
   - Existing `agent/oro-child` is not merged into the resolved target.
   - Assignment does not force-delete or overwrite it.
   - The state is visible as a named recovery quarantine only if reuse cannot be
     made safe.

4. No-op merge closes cleanly:
   - Merge result `Noop=true`.
   - Assignment is completed before close.
   - Bead is closed once.
   - Bead is not reopened.
   - No escalation is emitted.
   - Cleanup uses the active target branch.

5. Ops review config failure is explicit:
   - Runtime router returns unknown/unconfigured runtime for review.
   - A failed `ops_runs` row exists with `type=review`.
   - `oro health --json` includes `ops_run_failed`.
   - `oro status` summarizes failed ops runs.
   - `oro monitor --act` prints `blocked_by_ops_runs` and performs no mutation.

6. Review ops-run lifecycle is complete:
   - Approved reviews complete their `type=review` row as `resolved`.
   - Rejected reviews complete their `type=review` row as `resolved` before the
     bead is reassigned with feedback.
   - Env/infra-blocked reviews complete their `type=review` row as `failed`.
   - A second review on the same bead is not blocked by an old `running` row.

7. Ops retry is recoverable:
   - `oro ops retry <failed-review-id>` marks the old row `superseded`.
   - A replacement review row is created and routed, or fails explicitly with
     context-unavailable detail.
   - Assignment remains inspectable.

8. Decompose/escalation runtime failures are explicit:
   - Unknown/unconfigured runtime for decompose creates or completes a
     `type=decompose` row as `failed`.
   - Unknown/unconfigured runtime for generic one-shot escalation creates or
     completes the relevant row as `failed`.
   - Health/status/monitor show the failed ops run with retry/resolve action.

9. Long subprocess progress:
   - Assignment, QG, and Codex/plain-text subprocess phases emit
     `running_progress`.
   - Progress result contains bounded `command_age_ms` and
     `last_output_age_ms`.
   - Blocked progress writes do not block heartbeat/context watching.

10. Dogfood smoke:
    - Script seeds isolated work, runs finite `monitor --act`, stops factory,
      and asserts post-run invariants.
    - Failing invariant prints the log and the recommended health action.
    - `--scenario reliability-v2` proves target-aware cleanup, no-op merge, and
      ops failure visibility were actually exercised.

## Task Decomposition Draft

### Epic: Autonomous Factory Reliability v2

Type: `epic`

Priority: `0`

Acceptance:

```text
Test: all child beads pass their focused tests and the dogfood smoke harness.
Cmd: go test ./cmd/oro ./pkg/dispatcher/... ./pkg/factoryhealth ./pkg/ops ./pkg/worker -count=1 && scripts/oro-dogfood-smoke.sh --iterations 3 --workers 2
Assert: target-aware cleanup creates no main-only quarantine noise, managerless ops failures are explicit failed ops incidents, no-op merges close terminally, long subprocesses emit bounded progress, and the smoke dogfood exits with no active assignments/quarantines/QG incidents/failed ops runs.
Read: docs/plans/2026-05-20-autonomous-factory-reliability-v2-design.md
Edges: do not force-delete branches; do not auto-resolve ops incidents; do not rely on a resident manager pane; keep dogfood isolated from the developer's live factory.
```

### Task A: Target-Aware Branch Cleanup

Acceptance:

```text
Test: pkg/dispatcher/stale_branch_test.go adds coverage for agent branch merged into epic target but not main, plus diverged branch preservation.
Cmd: go test ./pkg/dispatcher -run 'TestFilterAlreadyMergedBranchesUsesResolvedTargetBranch|TestDeleteStaleAgentBranchUsesAssignmentTargetBranch|TestDeleteStaleAgentBranch_DivergedFromTargetQuarantines' -count=1
Assert: branch cleanup and already-merged filtering prove against the resolved target branch; merged-to-epic child branches do not create unsafe_stale_branch quarantine noise; CloseBead reason names the resolved target; diverged branches are preserved and surfaced.
Read: pkg/dispatcher/dispatcher.go filterAlreadyMergedBranches/isBranchMerged/deleteStaleAgentBranch/prepareAssignmentWorktree; pkg/dispatcher/epic_branch.go ResolveEpicBranch; pkg/dispatcher/worktree_manager.go DeleteBranchMergedInto.
Signature: replace isBranchMerged(ctx, beadID) with isBranchMergedInto(ctx, beadID, targetBranch); pass targetBranch into deleteStaleAgentBranch.
Edges: empty agent branches must not be treated as done; missing target branch must not cause force deletion; errors must preserve inspectable branch/worktree state.
```

### Task B: Managerless Ops-Review Failure Incidents

Acceptance:

```text
Test: dispatcher/health/monitor tests cover review runtime config failure becoming a failed ops run.
Cmd: go test ./pkg/dispatcher ./pkg/factoryhealth ./cmd/oro -run 'TestOpsReviewSpawnFailureCreatesFailedOpsRun|TestReviewOpsRunResolvedOnApproved|TestReviewOpsRunResolvedOnRejected|TestReviewOpsRunFailedOnBlocked|TestOpsReviewRetryRoutesReplacementRun|TestDecomposeOpsRunSpawnFailureCreatesFailedIncident|TestEscalationOpsRunSpawnFailureCreatesFailedIncident|TestHealthJSONIncludesOpsRunMetrics|TestMonitorActDoesNotResolveFailedOpsRuns|TestStatusShowsFailedOpsRuns' -count=1
Assert: Codex/Claude review spawn/config/runtime failures create or complete a blocking ops_runs row with type=review, bead_id, worker_id, runtime/model/error; approved/rejected reviews resolve the row; env/infra-blocked reviews fail the row; decompose and generic escalation runtime failures also land as failed ops incidents; health/status/monitor show ops_run_failed with oro ops retry/resolve action; monitor --act performs no mutation.
Read: pkg/dispatcher/dispatcher.go handleReadyForReview/handleReviewApproved/handleReviewRejection/handleReviewBlocked/handleReviewFailed and ops run helpers; pkg/dispatcher/ops_runs.go routeOpsRun/supersedeOpsRunForRetry; pkg/ops/ops.go; pkg/ops/exec_spawner.go; pkg/factoryhealth/health.go; cmd/oro/cmd_ops.go; cmd/oro/cmd_monitor.go; cmd/oro/cmd_status.go.
Signature: add or reuse dispatcher helper to create/complete review ops runs around the ops review path; add OpsReview routing support to routeOpsRun or explicit context-unavailable failure.
Edges: docs-only auto-approval still resolves the review ops row; failed rows remain blocking until retry/resolve; retry must not lose preserved assignment/worktree context or silently return routed=false.
```

### Task C: No-Op Merge Closure Hardening

Acceptance:

```text
Test: pkg/dispatcher/dispatcher_test.go or merge_closed_bead_test.go asserts no-op merge completes assignment, closes once, does not reopen, emits no escalation, and cleans branch against target.
Cmd: go test ./pkg/dispatcher -run 'TestMergeAndCompleteNoopMergeClosesBeadWithoutEscalation|TestNoopMergeCompletesAssignmentBeforeClose|TestNoopMergeCleanupUsesTargetBranch' -count=1
Assert: Noop merge is a terminal success path; active assignment row is completed before CloseBead; updateBeadStatus(open) is not called; removeWorktreeAndClearTracking receives the active target branch.
Read: pkg/dispatcher/dispatcher.go mergeAndComplete/handleNoopMerge/finalizeSuccessfulMerge/removeWorktreeAndClearTracking; pkg/merge.
Signature: keep handleNoopMerge target parameter and target defaulting explicit.
Edges: CloseBead failure is still escalated as stuck; cleanup failure must not reopen the task; empty target falls back to default branch.
```

### Task D: Long Subprocess Progress Regression Tests

Acceptance:

```text
Test: pkg/worker/worker_progress_test.go adds named tests for assignment/go-test-like, quality gate, and Codex/plain-text subprocess phases.
Cmd: go test ./pkg/worker -run 'TestWorkerEmitsProgressWhileSubprocessRuns|TestProgressTickWriteDoesNotBlockContextWatcher|TestWorkerEmitsProgressDuringQualityGate|TestWorkerEmitsProgressDuringCodexPlainTextSubprocess' -count=1
Assert: each long subprocess phase emits running_progress with command_age_ms and last_output_age_ms before timeout; the QG test drives worker.runQGAndReport with a fake long quality gate; the Codex/plain-text test drives the worker assignment spawn path using the Codex/plain-text stream format rather than a generic unnamed subprocess; blocked progress writes use bounded deadlines and do not prevent later heartbeat/context watcher activity.
Read: pkg/worker/worker.go recordSpawnedProc/watchContext/runQGAndReport/trySendSubprocessProgress; pkg/worker/worker_progress_test.go; pkg/worker/worker_test.go test helpers.
Signature: no production signature change expected unless tests expose a real gap; tests must prove recordSpawnedProc is reached from the named QG and Codex/plain-text entry points.
Edges: tests must not invoke real network-backed Codex/Claude; timing should use short fake intervals and deterministic fake processes.
```

### Task E: Repeatable Monitor Dogfood Harness

Acceptance:

```text
Test: cmd/oro tests cover finite dogfood seed/run/assert command behavior, and shell script smoke can run against an isolated temp state.
Cmd: go test ./cmd/oro -run 'TestDogfoodHarnessSeedsRunsAndAssertsInvariants|TestDogfoodHarnessReliabilityV2ScenarioExercisesHardeningPaths|TestMonitorIterationsActKeepsHealthyFactory' -count=1 && scripts/oro-dogfood-smoke.sh --iterations 3 --workers 2 --scenario reliability-v2
Assert: the harness builds/uses an isolated oro binary, seeds known work, starts workers=2 max-workers=2, runs monitor --act for finite iterations, stops the factory, and fails with health/status/log detail if active assignments, recovery quarantines, QG incidents, failed/stale ops runs, or ready seeded work remain; reliability-v2 scenario proves target-aware cleanup, no-op merge closure, and ops failure visibility were exercised.
Read: cmd/oro/cmd_monitor.go; cmd/oro/cmd_start.go; cmd/oro/cmd_stop.go; cmd/oro/cmd_task.go; cmd/oro/monitor_actions.go; scripts/quality_gate.sh.
Signature: add scripts/oro-dogfood-smoke.sh with flags --iterations, --workers, --interval, --state-dir, --oro-bin, --scenario.
Edges: never mutate the developer's live state by default; always stop the factory on exit; print artifact paths; support short finite smoke and longer manual dogfood.
```

Dependencies:

- Epic depends on A, B, C, D, and E.
- E depends on A, B, C, and D because the harness should assert the hardened
  invariants.
- A, B, C, and D are independent enough for max workers 2.

## Verification

Focused verification:

```bash
go test ./pkg/dispatcher -run 'TestFilterAlreadyMergedBranchesUsesResolvedTargetBranch|TestDeleteStaleAgentBranchUsesAssignmentTargetBranch|TestDeleteStaleAgentBranch_DivergedFromTargetQuarantines|TestMergeAndCompleteNoopMergeClosesBeadWithoutEscalation|TestNoopMergeCompletesAssignmentBeforeClose|TestNoopMergeCleanupUsesTargetBranch|TestOpsReviewSpawnFailureCreatesFailedOpsRun|TestReviewOpsRunResolvedOnApproved|TestReviewOpsRunResolvedOnRejected|TestReviewOpsRunFailedOnBlocked|TestOpsReviewRetryRoutesReplacementRun|TestDecomposeOpsRunSpawnFailureCreatesFailedIncident|TestEscalationOpsRunSpawnFailureCreatesFailedIncident' -count=1
go test ./pkg/factoryhealth ./cmd/oro -run 'TestHealthJSONIncludesOpsRunMetrics|TestMonitorActDoesNotResolveFailedOpsRuns|TestStatusShowsFailedOpsRuns|TestDogfoodHarnessSeedsRunsAndAssertsInvariants|TestDogfoodHarnessReliabilityV2ScenarioExercisesHardeningPaths|TestMonitorIterationsActKeepsHealthyFactory' -count=1
go test ./pkg/worker -run 'TestWorkerEmitsProgressWhileSubprocessRuns|TestProgressTickWriteDoesNotBlockContextWatcher|TestWorkerEmitsProgressDuringQualityGate|TestWorkerEmitsProgressDuringCodexPlainTextSubprocess' -count=1
```

Full phase verification:

```bash
go test ./cmd/oro ./pkg/dispatcher/... ./pkg/factoryhealth ./pkg/ops ./pkg/worker -count=1
scripts/oro-dogfood-smoke.sh --iterations 3 --workers 2 --scenario reliability-v2
./scripts/quality_gate.sh
```

Live dogfood after child beads pass:

```bash
oro start --workers 2 --max-workers 2 --detach
oro monitor --target 2 --max-workers 2 --interval 60s --act 2>&1 | tee /tmp/oro-reliability-v2-dogfood.log
oro health --json
oro status --json
oro stop
```

The live run should continue for two hours unless `oro health` reports a
non-healable finding. In that case, the operator fixes the named incident,
rebuilds/restarts, and resumes the run.
