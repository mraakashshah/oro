# Managerless Orchestration Design

Date: 2026-05-19

## Goal

Remove the resident oro manager pane from the factory's correctness path. Rare
judgment work should be handled by dispatcher-owned, durable, bounded ops runs
using the existing Claude/Codex batch spawner. A missing, stale, or busy tmux
manager pane must not prevent task assignment, decomposition, escalation
handling, health reporting, or recovery.

The first visible failure this design must fix is the live oversized-bead stall:
the dispatcher repeatedly emits `OVERSIZED_BEAD` to `oro:manager`, the manager is
interrupted by repeated prompts, workers remain idle, and the ready queue never
makes progress.

## Problem Statement

Oro has already moved most routine execution into the dispatcher and workers,
but the manager pane is still treated as both a user interface and a recovery
actor. That creates a fragile mixed contract:

- `oro start` creates a tmux `manager` window and nudges an interactive runtime.
- The dispatcher writes persistent escalation rows, then also pastes escalation
  text into `oro:manager`.
- Factory health reports `manager_pane_unhealthy` when the pane is absent or
  stale.
- The escalation retry loop re-delivers pending messages through tmux.
- Some escalation types spawn one-shot ops agents, but `OVERSIZED_BEAD` does
  not, even though the ops package already has decomposition support.

The result is that the manager pane can be the only actor capable of making
progress on a ready task, while also being the least durable and least
machine-verifiable part of the system.

## Source Research

Key files read before this design:

- `cmd/oro/tmux.go`
  - `TmuxSession.Create` creates a single `manager` window and launches an
    interactive runtime with `ORO_ROLE=manager`.
  - `launchAndNudgeManagerOnly` waits for the pane and sends `ManagerNudge`.
  - `bootstrapRoleConfigs` is manager-specific.
- `cmd/oro/cmd_start.go`
  - `buildDispatcherWithReviewTimeouts` wires
    `dispatcher.NewTmuxEscalator(..., TmuxPaneTarget(..., "manager"), ...)`.
  - `wireDependencies` installs a tmux pane restarter using
    `execEnvCmd("manager", project)` except in daemon-only mode.
- `cmd/oro/start_full_test.go` and `cmd/oro/cmd_start_test.go`
  - Tests assert startup creates the manager window, sends manager nudges, and
    wires a manager pane restarter.
- `cmd/oro/manager.go` and `assets/beacons/manager.md`
  - The manager role owns decomposition, escalations, status, and handoffs.
- `pkg/dispatcher/dispatcher.go`
  - `escalate` always persists an escalation and sends it to tmux.
  - `parseEscalationType` only one-shot routes `STUCK_WORKER`,
    `MERGE_CONFLICT`, `PRIORITY_CONTENTION`, and `MISSING_AC`.
  - `OVERSIZED_BEAD` is stored and retried, but not one-shot handled.
  - `handleEscalationResult` escalates failed one-shot agents back to the
    persistent manager.
  - `retryPendingEscalations` re-pastes unresolved pending escalations.
- `pkg/dispatcher/escalation_precheck.go`
  - `retryOversizedBead` already knows how to detect successful decomposition:
    parent closed, parent converted to epic, child tasks exist, or module count
    dropped below the threshold.
- `pkg/ops/ops.go` and `pkg/ops/decompose_prompt.go`
  - `OpsDecompose` and `Spawner.Decompose` already exist.
  - The prompt creates child tasks, wires parent dependencies, and converts the
    parent to an epic, but it is described as a retry-exhaustion path.
- `pkg/ops/escalation_prompt.go`
  - Generic one-shot escalation prompt has an `OVERSIZED_BEAD` playbook, but
    dispatcher never routes that type through `parseEscalationType`.
- `pkg/dispatcher/health.go` and `pkg/factoryhealth/health.go`
  - Health includes `ManagerPaneAlive` and emits `manager_pane_unhealthy`.
- `pkg/protocol/schema.go` and `pkg/protocol/tables.go`
  - `pane_activity` is manager-pane oriented.
  - `escalations` is documented as "dispatcher writes, manager acks".
- `assets/skills/watching-oro/SKILL.md` and
  `assets/skills/watching-oro/references/deep-observation.md`
  - Operator workflows inspect a manager pane and `pane_activity`.
- `docs/plans/2026-03-17-epic-branch-isolation-design.md`
  - Prior epic decomposition design established childless epic behavior and
    post-decomposition assignment expectations.
- `docs/plans/2026-05-06-codex-harness-parity-design.md`
  - Prior runtime work established dispatcher-spawned Codex/Cli parity.

## Design Principles

1. Dispatcher-owned state is authoritative.
   Tmux panes can observe or assist, but cannot be required for forward
   progress or for acknowledging durable work.

2. Rare judgment is a bounded job, not a resident actor.
   Use existing `pkg/ops` batch spawners for one-shot Claude/Codex runs.

3. Every autonomous mutation must be validated after the agent exits.
   The dispatcher should not trust `VERDICT: resolved` unless the database and
   task graph prove the condition is resolved.

4. Failed judgment must become visible durable state.
   It must not fall back to unstructured tmux paste or disappear as an acked
   escalation.

5. Managerless does not mean invisible.
   `oro health`, `oro status`, and `oro monitor` must show open ops incidents,
   active ops runs, and stale judgment work.

## Target Architecture

### Dispatcher

The dispatcher becomes the owner of escalation routing:

- It persists an escalation row.
- It decides whether the escalation is automatically resolvable.
- It creates at most one blocking ops run per `(type, bead_id)`.
- It validates the post-condition after the ops run exits.
- It acks the escalation only when the condition is gone or deliberately
  converted to an explicit durable incident.

The dispatcher no longer needs to paste routine escalations into `oro:manager`.

### Ops Runs

Add durable ops-run records so one-shot judgment has inspectable state:

```sql
CREATE TABLE IF NOT EXISTS ops_runs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    escalation_id INTEGER,
    type TEXT NOT NULL,
    bead_id TEXT,
    worker_id TEXT,
    dispatcher_pid INTEGER,
    process_pid INTEGER,
    runtime TEXT,
    model TEXT,
    status TEXT NOT NULL DEFAULT 'running',
    verdict TEXT,
    feedback TEXT,
    error TEXT,
    started_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    completed_at DATETIME
);

CREATE INDEX IF NOT EXISTS idx_ops_runs_open
    ON ops_runs(status, type, bead_id);

CREATE UNIQUE INDEX IF NOT EXISTS idx_ops_runs_blocking_key
    ON ops_runs(type, bead_id)
    WHERE status IN ('running', 'failed', 'stale');
```

Statuses:

- `running`: subprocess was started and is expected to produce a result.
- `resolved`: subprocess completed and dispatcher post-validation passed.
- `failed`: subprocess failed, timed out, returned failed verdict, or
  post-validation failed.
- `stale`: dispatcher restarted or lost ownership while the subprocess was
  still recorded as running.
- `superseded`: the original escalation condition disappeared before the run
  finished.

The `escalations` table remains the durable queue, but comments and code should
move away from "manager acks" toward "dispatcher/ops resolves".

Escalation statuses should become explicit enough to keep the retry loop from
feeding the old manager path:

- `pending`: unresolved and not yet routed.
- `routed`: an ops run owns the condition.
- `acked`: post-condition is resolved or superseded.
- `failed`: routed handling failed and is visible in health/status.

The retry loop may only inspect `pending` rows. It must not paste by default:
routable pending rows create ops runs, unroutable pending rows surface health
findings, and `routed`, `failed`, or `acked` rows are ignored by retry.

Blocking-run rule:

- Before inserting a new escalation row, `escalate` checks for an open blocking
  `ops_runs` row matching `(type, bead_id)` where status is `running`, `failed`,
  or `stale`.
- If one exists, `escalate` does not insert another escalation row, does not
  paste to tmux, and logs one `ops_run_blocked_assignment` event containing the
  blocking run ID and status.
- A failed decompose run records assignment-failure cooldown for the parent
  task. `oro ops retry` and successful `oro ops resolve` clear that cooldown.

### Ops Spawner

Reuse `pkg/ops.Spawner` and `RuntimeBatchSpawner`:

- `OpsDecompose` handles oversized task decomposition.
- `OpsWriteAC` continues to handle missing acceptance criteria.
- `OpsEscalation` remains a generic fallback only for escalation types with a
  specific autonomous playbook.

No new direct `claude -p` or `codex exec` call site should be added in the
dispatcher. Runtime selection should continue through existing `agentmodel` and
`pkg/ops` routing.

### Optional UI Pane

An interactive manager pane may remain as an optional operator console, but it
is no longer started by default and no health finding depends on it.

If retained, it should be explicitly named as an observer console:

- Possible flag: `oro start --manager-pane`
- Possible command: `oro attach manager`
- Health finding only applies when that optional pane is configured.

This phase does not require deleting all manager docs and code immediately. It
does require removing the pane from the default correctness path.

## Escalation Routing Matrix

| Escalation type | Current behavior | Managerless behavior |
| --- | --- | --- |
| `OVERSIZED_BEAD` | Persist row, paste to tmux, retry paste | Start `OpsDecompose`, validate parent is epic or has children, ack only after validation |
| `MISSING_AC` | Start `OpsWriteAC`, fallback to manager on failure | Start `OpsWriteAC`, validate AC is present and TDD-shaped, otherwise durable failed ops run |
| `MERGE_CONFLICT` | Start ops escalation or merge agent, fallback to manager on failure | Preserve worktree/branch, durable failed ops run or recovery quarantine; no manager fallback |
| `STUCK_WORKER` | Start generic escalation, may restart/preempt, fallback to manager | Start diagnosis/action ops run, validate assignment/worker state changed or record failed ops run |
| `PRIORITY_CONTENTION` | Start generic escalation, fallback to manager | Dispatcher-owned scheduling/preemption decision or durable failed ops run |
| `MERGE_COMPLETE` | Manager notification for push | Auto-ack; optional event/status notification. No manager needed |
| `MANUAL_INTEGRATION` | Manager notification | Durable integration incident with branch/worktree details |
| Unknown type | Paste to manager | Durable pending escalation with health finding; no repeated tmux paste |

## Production Routing Chain

`OVERSIZED_BEAD` must use the existing dispatcher escalation entry point. The
implementation task is not complete unless the actual production call chain is
wired end to end:

1. `checkBeadReady` calls `escalate` with an `OVERSIZED_BEAD` message.
2. `parseEscalationType` recognizes `protocol.EscOversizedBead` as a routable
   one-shot type.
3. `spawnEscalationOneShot` has an explicit `EscOversizedBead` branch that calls
   `d.ops.Decompose`, not `d.ops.Escalate` and not `d.ops.WriteAC`.
4. `ops.Spawner.Decompose` passes `DecomposeOpts.Workdir` as the workdir
   argument to `s.run`.
5. `handleEscalationResult` branches on `EscOversizedBead` and calls the
   decomposition post-validator before acking.
6. `retryPendingEscalations` routes legacy `pending` routable rows through the
   same helper, instead of calling `d.escalator.Escalate`.

The acceptance tests must exercise this chain from `escalate(...)` or
`retryPendingEscalations(...)`, not only isolated helper methods.

## Oversized-Bead Flow

1. `checkBeadReady` detects `CountDistinctModules(acceptance) > 2`, non-epic
   task, and no children.
2. Dispatcher inserts an `OVERSIZED_BEAD` escalation row.
3. Dispatcher inserts or reuses an `ops_runs` row for
   `("decompose", bead_id)`. The blocking unique index prevents a second
   running, failed, or stale decompose run for the same task even if repeated
   assignment checks create more escalation rows.
4. Dispatcher calls:

   ```go
   d.ops.Decompose(ctx, ops.DecomposeOpts{
       BeadID: beadID,
       Tier:   bead.Tier,
       Workdir: d.cfg.RepoRoot,
   })
   ```

5. The escalation row is marked `routed`. While a blocking `ops_runs` row
   exists, retry logic does not paste or launch another run.
6. When the run exits, dispatcher validates the state. All clauses are required:
   - Parent task exists.
   - Parent is type `epic` OR parent has open children.
   - Each open child task has acceptance criteria with Test/Cmd/Assert shape.
   - Parent depends on each child task in the correct direction.
   - Parent is now type `epic`, OR `CountDistinctModules(parent.Acceptance)`
     is less than or equal to 2.
   - The parent itself is no longer assignable as a ready oversized task.
7. If validation passes:
   - Mark ops run `resolved`.
   - Ack escalation.
   - Clear any assignment failure cooldown for the parent if one exists.
8. If validation fails:
   - Mark ops run `failed` with the validation reason.
   - Mark escalation `failed`, not `pending`.
   - Record assignment-failure cooldown for the parent task.
   - Surface an `ops_run_failed` health finding.
   - Do not paste to manager and do not assign the parent.

### Restart Reconciliation

Ops subprocess ownership is in memory, but `ops_runs` is durable. On dispatcher
startup:

1. Load `ops_runs` rows with `status='running'`.
2. If no in-process agent owns the row, inspect `process_pid` when available.
3. If the process is still alive, mark the row `stale` and surface
   `ops_run_stale`; do not launch a replacement because duplicate task mutation
   is unsafe.
4. If the process is absent, dead, or older than the configured stale grace
   period, mark the row `superseded` and re-route the still-true escalation
   condition through a fresh ops run.
5. Keep related escalations in `routed` while a replacement is running,
   `acked` when the condition has disappeared, or `failed` when validation or
   reroute fails.

This prevents duplicate one-shot agents after a daemon crash while allowing
managerless recovery when the orphaned process is provably gone.

### Legacy Pending Escalations

Upgrade must handle live factories that already have pending manager-routed
escalations.

On startup and on each retry-loop tick, dispatcher calls a single helper,
`routePendingRoutableEscalations`, that:

1. Selects `pending` escalation rows with routable types such as
   `OVERSIZED_BEAD` and `MISSING_AC`.
2. Runs the existing precheck for that escalation type. If the condition is
   resolved, mark the row `acked`.
3. If the condition still holds and no blocking `ops_runs` row exists for
   `(type, bead_id)`, insert an `ops_runs` row and mark the escalation `routed`
   in one transaction.
4. If a blocking run exists, leave the escalation unchanged and log
   `ops_run_blocked_assignment`; do not paste to tmux.
5. If the type is not routable, keep it pending and expose
   `pending_escalation_unrouted` in health.

## Health And Status

`factoryhealth.Snapshot` gains ops-run metrics:

- `OpenOpsRuns`
- `FailedOpsRuns`
- `StaleOpsRuns`

`oro health --json` includes an `ops_runs` section with at least:

```json
{
  "ops_runs": {
    "running": 1,
    "failed": 0,
    "stale": 0,
    "by_type": {
      "decompose": {"running": 1, "failed": 0}
    }
  }
}
```

New findings:

- `ops_run_failed`
  - Severity: error
  - Component: `ops`
  - Recommended action: inspect the run feedback and task graph, then resolve
    or retry explicitly.
- `ops_run_stale`
  - Severity: warning or error depending on age.
  - Component: `ops`
  - Recommended action: inspect subprocess state and retry or mark failed.
- `pending_escalation_unrouted`
  - Severity: warning
  - Component: `dispatcher`
  - Recommended action: add an autonomous route or explicitly resolve the
    escalation.

Remove default `manager_pane_unhealthy` from factory health. If optional
manager pane mode remains, report it as an optional UI finding rather than a
factory progress finding.

Human `oro status` should summarize:

- active ops runs
- failed ops runs
- pending unrouted escalations
- recovery quarantines

`oro monitor --act` may restart daemons and workers, and may allow dispatcher
ops runs to proceed, but it must not mutate failed ops runs except through an
explicit retry/resolve command.

Add an explicit CLI recovery path for failed or stale ops runs:

- `oro ops list` shows running, failed, and stale ops runs.
- `oro ops retry <run-id>` marks the blocking run `superseded`, returns the
  related escalation to `pending`, clears the parent assignment-failure
  cooldown, and lets the dispatcher route a new run.
- `oro ops resolve <run-id> --reason=<text>` re-runs the relevant
  post-condition validation; it only marks the run `resolved` and acks the
  escalation when validation passes, and then clears the parent
  assignment-failure cooldown.

## Startup And Runtime Changes

Default `oro start` should:

- Start the daemon.
- Start workers as configured.
- Not create an interactive `manager` tmux pane.
- Not wire a manager pane restarter into the dispatcher.
- Not fail health because no manager pane activity exists.

Optional manager UI mode, if retained, must be explicitly requested and tested
as non-authoritative.

`--model` help should stop describing a provider-native model for a manager
session unless optional manager mode remains.

`cmd/oro/router.go` should stop saying commands are "forwarded to manager" for
generic `oro` commands.

`oro attach` should be coherent when no manager pane exists. Default behavior
should either attach to the optional manager pane when one was explicitly
started, or print a clear non-error explanation that this factory is running in
managerless mode and direct the operator to `oro status` / `oro monitor`.

## Data Migration

Schema migration must be additive:

1. Keep `pane_activity` for backward compatibility and optional UI panes.
2. Keep `escalations` existing rows.
3. Add `ops_runs`.
4. Add indexes for open failed/running lookups.
5. Add or tolerate new escalation statuses: `routed` and `failed`.
6. Update table comments in `pkg/protocol/tables.go` and
   `pkg/protocol/directive.go` so escalation ownership is dispatcher/ops, not
   manager-only.

Existing pending escalations after upgrade:

- Resolved conditions auto-ack through current precheck helpers.
- Routable unresolved conditions create ops runs through
  `routePendingRoutableEscalations` at startup or retry-loop time.
- Unroutable unresolved conditions remain pending and are shown in health as
  `pending_escalation_unrouted`.

## Invariants

1. A missing tmux manager pane cannot make `oro health` unhealthy by default.
2. A missing tmux manager pane cannot prevent assignment of otherwise valid
   ready tasks.
3. An oversized task that is ready, non-epic, and childless either gets an
   active decompose ops run or a durable failed ops-run finding.
4. No escalation retry loop may paste `OVERSIZED_BEAD` repeatedly into tmux.
5. One-shot ops failure must not be acked as success and must not fallback to
   unstructured manager paste.
6. The dispatcher validates task graph state after decomposition before
   acknowledging the escalation.
7. `oro monitor --act` does not mutate failed ops-run state without an explicit
   retry/resolve path.
8. Successful decomposition makes child tasks visible and assignable without
   requiring a human or manager pane.
9. A dispatcher restart cannot create duplicate ops runs for a task that already
   has a running, stale, or failed run.
10. Ops agents that execute `oro ...` commands run with an explicit project
    workdir, not the daemon's incidental current directory.
11. `parseEscalationType`, `spawnEscalationOneShot`,
    `handleEscalationResult`, and `retryPendingEscalations` are all part of
    the managerless routing contract and must be covered by production-path
    tests.
12. If a blocking ops run already exists for `(type, bead_id)`, the dispatcher
    does not insert duplicate escalation rows or paste to tmux.
13. Live factories with pre-existing pending `OVERSIZED_BEAD` rows are converted
    to routed ops runs without requiring a manager pane.

## Acceptance Tests

Epic-level verification:

```bash
./scripts/test_managerless_oversized_e2e.sh && go test ./cmd/oro ./pkg/dispatcher/... ./pkg/factoryhealth ./pkg/ops ./pkg/protocol -count=1 && ./scripts/quality_gate.sh
```

Binary assertions:

- `test_managerless_oversized_e2e.sh` seeds an oversized fixture, runs the
  dispatcher loop, and exits 0 only when:
  - an `ops_runs` row of type `decompose` reaches `resolved`
  - the parent is an epic or has child tasks
  - every child has Test/Cmd/Assert acceptance criteria
  - no tmux manager paste was attempted, proven by a counting fake escalator or
    by asserting no tmux-escalation event/call was recorded
  - no duplicate pending `OVERSIZED_BEAD` rows were inserted
- `go test` passes.
- `quality_gate.sh` exits 0.
- Health remains healthy when no manager pane exists and no ops/recovery/QG
  incidents are open.

Targeted tests:

1. `pkg/dispatcher`
   - `TestParseEscalationTypeRecognizesOversized`
   - `TestEscalateOversizedRoutesToDecomposeProductionPath`
   - `TestSpawnEscalationOneShotRoutesOversizedToDecompose`
   - `TestCheckBeadReady_OversizedBeadStartsDecomposeOpsRun`
   - `TestCheckBeadReady_OversizedBeadDoesNotEscalateToTmuxWhenOpsRouted`
   - `TestOversizedDecomposeResultAcksOnlyAfterValidation`
   - `TestOversizedDecomposeResultFailsWhenNoChildrenCreated`
   - `TestHandleEscalationResult_OversizedFailsOpsRunOnMissingChildren`
   - `TestDecomposeValidator_FailsWhenChildHasNoTDDAcceptance`
   - `TestDecomposeValidator_FailsWhenParentDoesNotDependOnChildren`
   - `TestRetryPendingEscalations_DoesNotRepasteRoutedOpsRun`
   - `TestRetryPendingEscalations_RoutesLegacyOversizedToOpsRun`
   - `TestStartupConvertsLegacyPendingOversizedToOpsRun`
   - `TestEscalateNoOpWhenBlockingOpsRunExists`
   - `TestFailedDecomposeOpsRunSetsAssignmentCooldown`
   - `TestConcurrentEscalateCreatesOneOpsRun`
   - `TestDispatcherStartupMarksOrphanedOpsRunsStale`
   - `TestOneShotFailureCreatesOpsRunFailureWithoutManagerFallback`
   - `TestPendingUnknownEscalationSurfacesUnroutedHealthFinding`

2. `pkg/ops`
   - `TestDecomposePromptSupportsOversizedReason`
   - `TestDecomposePromptRequiresParentEpicOrChildren`
   - `TestRuntimeSpawnerRoutesDecomposeThroughConfiguredRuntime`

3. `pkg/protocol`
   - `TestSchemaCreatesOpsRunsTable`
   - `TestOpsRunUniqueEscalationTypeBead`

4. `pkg/factoryhealth`
   - `TestEvaluateNoManagerPaneFindingByDefault`
   - `TestEvaluateFailedOpsRunFinding`
   - `TestEvaluateStaleOpsRunFinding`

5. `cmd/oro`
   - `TestStartDoesNotCreateManagerPaneByDefault`
   - `TestWireDependenciesDoesNotSetManagerRestarterByDefault`
   - `TestReconnectRunningDaemonDoesNotNudgeManagerByDefault`
   - `TestAttachExplainsManagerlessModeWhenNoPaneExists`
   - `TestStatusShowsFailedOpsRuns`
   - `TestOpsRetrySupersedesBlockingRunAndClearsCooldown`
   - `TestOpsResolveValidatesBeforeAckAndClearsCooldown`
   - `TestMonitorActDoesNotResolveFailedOpsRuns`

Dogfood acceptance:

1. Seed or use an oversized ready task like the current `oro-1evm`.
2. Run isolated daemon and monitor:

   ```bash
   oro start --workers 2 --max-workers 2 --detach
   oro monitor --target 2 --max-workers 2 --interval 60s --act
   ```

3. Expected:
   - No repeated `[ORO-DISPATCH] OVERSIZED_BEAD` paste loop.
   - Either child tasks are created and assigned, or `oro health --json`
     contains an `ops_run_failed` finding with the failed run details.
   - Workers are not idle solely because the manager pane is absent or stale.

## Implementation Tasks

### Task 1: Durable ops-run schema and metrics

Read:

- `pkg/protocol/schema.go`
- `pkg/protocol/tables.go`
- `pkg/dispatcher/health.go`
- `pkg/factoryhealth/health.go`

Build:

- Add `ops_runs` schema and table model.
- Add helper functions to create, complete, fail, and list open ops runs
  idempotently.
- Store `dispatcher_pid` and subprocess `process_pid` when available.
- Add startup reconciliation that marks orphaned `running` rows `stale`.
- Add startup conversion for legacy pending routable escalations by calling the
  same `routePendingRoutableEscalations` helper used by retry.
- Add new escalation statuses or status helpers so routed/failed rows are not
  retried through tmux.
- Add health metrics and findings for failed/stale/running ops runs.

Tests:

- `TestSchemaCreatesOpsRunsTable`
- `TestOpsRunUniqueEscalationTypeBead`
- `TestDispatcherStartupMarksOrphanedOpsRunsStale`
- `TestStartupConvertsLegacyPendingOversizedToOpsRun`
- `TestEvaluateFailedOpsRunFinding`
- `TestEvaluateStaleOpsRunFinding`

### Task 2: Route oversized beads to decompose ops

Read:

- `pkg/dispatcher/dispatcher.go:escalate`
- `pkg/dispatcher/dispatcher.go:parseEscalationType`
- `pkg/dispatcher/dispatcher.go:spawnEscalationOneShot`
- `pkg/dispatcher/dispatcher.go:handleEscalationResult`
- `pkg/dispatcher/dispatcher.go:retryPendingEscalations`
- `pkg/dispatcher/escalation_precheck.go`
- `pkg/ops/ops.go`
- `pkg/ops/decompose_prompt.go`
- `pkg/ops/escalation_prompt.go`

Build:

- Route `OVERSIZED_BEAD` through `OpsDecompose`.
- Add `Workdir` to `ops.DecomposeOpts` and pass `d.cfg.RepoRoot`.
- Make `ops.Spawner.Decompose` pass `opts.Workdir` as the workdir argument to
  `s.run`.
- Deduplicate blocking decompose runs by type/bead, not only escalation ID.
- Validate the full post-condition predicate before acking:
  parent exists; parent is epic or has open children; every child has
  Test/Cmd/Assert acceptance criteria; parent depends on every child; parent is
  epic or has no more than two distinct acceptance modules; parent is no longer
  assignable as an oversized ready task.
- Mark ops runs failed on timeout, failed verdict, or validation failure.
- Set assignment-failure cooldown when decompose validation fails.
- Short-circuit `escalate` when a blocking ops run exists for `(type, bead_id)`.
- Route pre-upgrade pending `OVERSIZED_BEAD` rows through ops in the retry loop.
- Mark routed/failed escalation rows so retry does not paste to tmux.

Tests:

- `TestCheckBeadReady_OversizedBeadStartsDecomposeOpsRun`
- `TestParseEscalationTypeRecognizesOversized`
- `TestEscalateOversizedRoutesToDecomposeProductionPath`
- `TestSpawnEscalationOneShotRoutesOversizedToDecompose`
- `TestOversizedDecomposeResultAcksOnlyAfterValidation`
- `TestOversizedDecomposeResultFailsWhenNoChildrenCreated`
- `TestHandleEscalationResult_OversizedFailsOpsRunOnMissingChildren`
- `TestDecomposeValidator_FailsWhenChildHasNoTDDAcceptance`
- `TestDecomposeValidator_FailsWhenParentDoesNotDependOnChildren`
- `TestRetryPendingEscalations_DoesNotRepasteRoutedOpsRun`
- `TestRetryPendingEscalations_RoutesLegacyOversizedToOpsRun`
- `TestEscalateNoOpWhenBlockingOpsRunExists`
- `TestFailedDecomposeOpsRunSetsAssignmentCooldown`
- `TestConcurrentEscalateCreatesOneOpsRun`
- `TestDecomposeOpsUsesRepoRootWorkdir`

### Task 3: Remove manager fallback from autonomous escalation handling

Read:

- `pkg/dispatcher/dispatcher.go:handleEscalationResult`
- `pkg/dispatcher/dispatcher.go:retryPendingEscalations`
- `pkg/ops/escalation_prompt.go`
- `pkg/dispatcher/escalator.go`
- `pkg/dispatcher/escalator_test.go`

Build:

- Replace failed one-shot fallback-to-manager with durable failed ops-run state.
- Keep tmux escalator only for optional/manual notifications or behind an
  explicit manager mode.
- Surface unrouted pending escalations through health.

Tests:

- `TestOneShotFailureCreatesOpsRunFailureWithoutManagerFallback`
- `TestPendingUnknownEscalationSurfacesUnroutedHealthFinding`
- Update tests that expect failed one-shots to paste `ONESHOT_FAILED`.

### Task 4: Start without a manager pane by default

Read:

- `cmd/oro/tmux.go:TmuxSession.Create`
- `cmd/oro/tmux.go:ForwardCommandToManager`
- `cmd/oro/cmd_start.go`
- `cmd/oro/cmd_start.go:reconnectTmux`
- `cmd/oro/cmd_attach.go`
- `cmd/oro/start_full_test.go`
- `cmd/oro/cmd_start_test.go`
- `cmd/oro/manager.go`

Build:

- Change default `oro start` to daemon/workers without interactive manager
  pane.
- Remove default pane restarter wiring.
- If optional manager mode remains, guard tmux session creation behind a flag
  and mark it non-authoritative.
- Make `oro attach` managerless-aware.
- Make reconnect to an already-running daemon avoid `ManagerNudge()` by default.
- Update CLI help for `--model` and startup output.

Tests:

- `TestStartDoesNotCreateManagerPaneByDefault`
- `TestWireDependenciesDoesNotSetManagerRestarterByDefault`
- `TestReconnectRunningDaemonDoesNotNudgeManagerByDefault`
- `TestAttachExplainsManagerlessModeWhenNoPaneExists`
- Optional manager-mode tests if a flag is retained.

### Task 5: Health, status, and monitor integration

Read:

- `pkg/factoryhealth/health.go`
- `pkg/dispatcher/health.go`
- `cmd/oro/cmd_status.go`
- `cmd/oro/cmd_health.go`
- `cmd/oro/cmd_monitor.go`
- `cmd/oro/main.go`

Build:

- Remove default `manager_pane_unhealthy`.
- Add ops-run metrics to JSON health.
- Add human status summary for active/failed ops runs.
- Add `oro ops list`, `oro ops retry <run-id>`, and
  `oro ops resolve <run-id> --reason=<text>`.
- Ensure `oro ops resolve` performs the same post-condition validation used by
  the dispatcher before it acks an escalation.
- Ensure `monitor --act` does not auto-resolve failed ops runs.

Tests:

- `TestEvaluateNoManagerPaneFindingByDefault`
- `TestStatusShowsFailedOpsRuns`
- `TestOpsRetrySupersedesBlockingRunAndClearsCooldown`
- `TestOpsResolveValidatesBeforeAckAndClearsCooldown`
- `TestMonitorActDoesNotResolveFailedOpsRuns`

### Task 6: Documentation and asset cleanup

Read:

- `assets/beacons/manager.md`
- `cmd/oro/manager.go`
- `assets/skills/watching-oro/SKILL.md`
- `assets/skills/watching-oro/references/deep-observation.md`
- `cmd/oro/router.go`
- `cmd/oro/tmux.go:ForwardCommandToManager`
- `docs/decisions&discoveries.md`

Build:

- Rename manager docs to legacy/optional observer language or remove them
  from default workflows.
- Update watching docs to inspect dispatcher health, ops runs, workers, and
  events instead of manager pane activity.
- Replace "forwarded to manager" user-facing text.
- Remove `ForwardCommandToManager` and `FormatForwardMessage` if they have no
  production caller, or explicitly mark them legacy with tests proving default
  flows do not call them.
- Record the architecture decision.

Tests:

- Existing init/router/doc tests updated.
- Search check: default docs should not instruct operators to depend on
  `oro:manager` for routine progress.
- Router/help tests prove "forwarded to manager" is absent from default user
  output.

### Task 7: End-to-end oversized routing fixture

Read:

- `scripts/quality_gate.sh`
- `cmd/oro/cmd_start.go`
- `pkg/dispatcher/dispatcher.go:checkBeadReady`
- `pkg/dispatcher/dispatcher.go:escalate`
- `pkg/ops/ops.go:Spawner.Decompose`

Build:

- Add `scripts/test_managerless_oversized_e2e.sh`.
- The script creates an isolated state directory, seeds one oversized ready task,
  starts a dispatcher with a fake or controlled ops spawner, advances the
  dispatcher loop, and asserts the task graph and `ops_runs` state.
- The script fails if any tmux manager paste is attempted, measured by a
  counting fake escalator or an event/call assertion, or if duplicate pending
  `OVERSIZED_BEAD` escalation rows appear.

Tests:

- The script itself is the epic acceptance command's first gate.
- `./scripts/test_managerless_oversized_e2e.sh`

## Premortem

Critical risks:

- The decompose agent creates invalid child tasks and prints success.
  Mitigation: dispatcher validates task graph and AC shape before ack.

- A hung one-shot blocks decomposition forever.
  Mitigation: durable `running` ops run records subprocess ownership, startup
  marks orphaned rows stale, dead stale runs can be superseded and rerouted, and
  failed runs have explicit retry/resolve commands.

- Removing manager health hides a broken operator UI.
  Mitigation: optional UI mode can report UI-specific health without making
  factory progress unhealthy.

- Unknown escalation types silently stop being handled.
  Mitigation: `pending_escalation_unrouted` finding and no destructive fallback.

- Two dispatcher loops launch duplicate ops runs for the same task.
  Mitigation: unique blocking `(type, bead_id)` index for running, failed, and
  stale rows plus active-run check.

- A failed ops run is acked and disappears.
  Mitigation: ack only after validation or explicit supersession; failed runs
  remain visible in health/status.

Important risks:

- Existing tests encode manager-pane assumptions deeply.
  Mitigation: change tests around default behavior first, preserve optional
  manager tests only if optional mode remains.

- `pane_activity` comments and docs mislead future implementers.
  Mitigation: update protocol comments and watching docs in the same phase.

- Live factories with pending manager escalations upgrade mid-run.
  Mitigation: migration keeps escalations, prechecks auto-ack resolved rows, and
  unresolved routable rows create ops runs through the startup/retry
  `routePendingRoutableEscalations` helper.

- Stale live orphan processes may still mutate tasks after dispatcher restart.
  Mitigation: startup only auto-reroutes when the stored process is absent,
  dead, or past the stale grace period; otherwise health reports a stale run and
  suppresses duplicate escalation churn.

Non-goals:

- Removing every manager-related file in one change.
- Redesigning worker assignment.
- Changing the quality gate lifecycle.
- Replacing the existing ops runtime router.
- Solving project-name tmux command sanitization.

## Open Decisions

Recommended defaults:

1. Keep optional manager pane support only if it is cheap after default removal.
   Otherwise delete the pane startup path in a follow-up.
2. Use `ops_runs` rather than overloading `escalations.status`; this preserves
   the original durable escalation and allows multiple attempts later.
3. Do not make `oro monitor --act` automatically retry failed ops runs in this
   phase. Stale runs whose process is provably gone may be superseded and
   rerouted; failed runs require explicit `oro ops retry` or `oro ops resolve`.

## Rollout Order

1. Add `ops_runs` schema, helpers, and health metrics.
2. Route `OVERSIZED_BEAD` to `OpsDecompose` with validation and no tmux retry.
3. Remove manager fallback from one-shot failure handling.
4. Make manager pane optional or absent by default at startup.
5. Update health/status/monitor surfaces.
6. Add the managerless oversized end-to-end fixture.
7. Update docs/assets and run dogfood against the current oversized-ready-task
   failure mode.
