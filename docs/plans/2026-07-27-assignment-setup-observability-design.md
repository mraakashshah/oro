# Assignment Setup Observability — Design

**Date:** 2026-07-27
**Incident:** oro-qg-incident-465 (fingerprint `qg:7c10d57242653660ab37f6d9`), plus follow-on QG incidents 468, 471, 473
**Status:** proposed — revised after adversarial review
**Affected package:** `pkg/dispatcher`

## Problem

`c629e33e fix(dispatcher): release assignment loop after reservation` moved
post-reservation assignment setup off the scheduling pass. `launchAssignment`
(`pkg/dispatcher/dispatcher.go:6403`) now returns as soon as
`assignBeadWithClaim` reports the reservation, and everything after that —
`updateBeadStatus`, worktree resolution and creation, the `assignments` row
insert, the ASSIGN send to the worker — runs on a `safeGo` goroutine that
nobody awaits.

The production intent is sound and is not in dispute. What broke is that 13
tests call `d.tryAssign(ctx)` and then immediately read state that setup has
not written yet.

Because the failure depends on goroutine scheduling, **the failing subset
differs on every run**. That is why the quality gate keeps minting new
fingerprints for what is one defect: incidents 465, 468, 471 and 473 all name
different tests. Incident 473 reached `reopen_original` after exhausting its
retry budget. Every worker's pre-merge gate now fails on code it did not touch.

### Verified failure inventory

Full `go test ./pkg/dispatcher/` at `f44f9717`. 13 affected tests, two classes.

**Class A — completeness (10).** The test observes state the background
goroutine has not written yet.

| Test | Site | Symptom |
|---|---|---|
| `TestPriorityContention` | `dispatcher_test.go:14693` | worker received no messages |
| `TestAssignment_SkipsClosedBeads` | `dispatcher_test.go:21767` | worktree not created for open bead |
| `TestTryAssign_DeadSocketRemovesWorker` | `dispatcher_test.go:22534` | dead worker still in `d.workers` |
| `TestTryAssignNotFrozenByEmptySafeQuarantine` | `dispatcher_test.go:24746` | assigned beads = `[]` |
| `TestTryAssign_EpicPriorityBeatsEpicAge` | `dispatcher_test.go:24775`, `:24777` | assigned beads = `[]`; ASSIGN messages = 0 |
| `TestTryAssign_UnassignableEpicUnitDoesNotBlockNextEpic` | `dispatcher_test.go:24805` | assigned beads = `[]` |
| `TestTryAssign_ReservedEpicUnitDoesNotIdleOtherWorkers` | `dispatcher_test.go:24827` | assigned beads = `[]` |
| `TestPreservedWorktreeAutoRedeploysFreshWorker` | `recovery_quarantine_test.go:1002` | state `reserved`, wants `busy` |
| `TestOfflineRequeuePreservedRedeploy` | `recovery_quarantine_test.go:1128` | state `reserved`, wants `busy` |
| `TestSpawnFor_StalePendingTargetDoesNotReserveBeadForever` | `spawn_for_test.go:409` | idle worker received no assignment |

**Class B — ordering (3).** These already received `d.wg.Wait()` in `c629e33e`
and still fail, because waiting fixes completeness but not order.

| Test | Site | Observed across runs |
|---|---|---|
| `TestTryAssign_FillsIdleWorkersAcrossEpicUnitsByPriority` | `dispatcher_test.go:24553` | `[a-fast b-fast a-slow]`, `[a-slow b-fast a-fast]`, want `[a-fast a-slow b-fast]` |
| `TestTryAssign_ConcentratesWorkersOnTopEpic` | `dispatcher_test.go:24580` | intermittent |
| `TestTryAssign_IndependentBeforeEpicUnits` | `dispatcher_test.go:24698` | `[independent-p0 epic-child independent-p1]`, want `[independent-p0 independent-p1 epic-child]` |

The shared helper `assignedBeadIDsByCreation` (`dispatcher_test.go:24934`) reads
`SELECT bead_id FROM assignments ORDER BY id`. With concurrent setup goroutines,
insert order is the order goroutines happen to reach `createAssignment`
(`dispatcher.go:10176`) — unrelated to scheduling order.

### Why `d.wg.Wait()` is the wrong barrier

`d.wg` is the WaitGroup `safeGo` uses for *every* tracked goroutine
(`dispatcher.go:1413`), including the long-lived `assignLoop`, heartbeat and
janitor loops (`dispatcher.go:1679-1686`, `:1730-1742`). In these tests the
dispatcher is never started, so `wg.Wait()` happens to return. Applied to any
test that does start the dispatcher, it deadlocks until the package timeout —
a worse failure than the one it fixes. The idiom must not spread further.

## Decision

Three changes, all additive. **`c629e33e` is not reverted.**

### 1. A per-invocation batch barrier for post-reservation setup

`tryAssign` keeps its signature and behavior. Internally it delegates to a
variant that returns the completion handles of the setups *that invocation*
launched:

```go
func (d *Dispatcher) tryAssign(ctx context.Context) { _ = d.tryAssignBatch(ctx) }

// tryAssignBatch runs one scheduling pass and returns a handle per assignment
// setup this pass launched. Production callers discard it; safeGo remains the
// lifecycle owner for shutdown.
func (d *Dispatcher) tryAssignBatch(ctx context.Context) []<-chan struct{}
```

`launchAssignment` returns its done-channel again (as it did before
`c629e33e`), and `assignGeneralSchedulingUnit` accumulates the channels — but
**nothing awaits them inside the pass**, so the cold-start property
`c629e33e` introduced is preserved exactly.

Tests use a bounded helper:

```go
func tryAssignAndWait(t *testing.T, d *Dispatcher, ctx context.Context) {
    t.Helper()
    waitForSetup(t, d.tryAssignBatch(ctx))
}
```

**Why per-invocation rather than a `Dispatcher`-level `sync.WaitGroup`:** a
shared WaitGroup has a genuine reuse hazard — `Wait` may observe zero and
return just as another `tryAssign` calls `Add(1)`, and a positive `Add`
concurrent with a zero-count `Wait` is prohibited use. A per-pass batch cannot
include dispatcher loops, cannot include a later pass, and needs no argument
about which goroutine drives `tryAssign`. This replaces the shared-WaitGroup
approach in the first draft of this design, which review found unsafe.

The helper must `t.Fatal` on a deadline rather than block forever; note that
its waiter goroutine outlives a timed-out test.

### 2. Assert the selected bead set, not the insert order

Replace `assignedBeadIDsByCreation` with a sorted / order-independent variant.
The three Class B `want` lists are sets — the tests assert *which* beads got
scheduled, and `ORDER BY id` was only ever a proxy for that.

**Plan ordering is already covered synchronously and keeps its exact-order
assertion:** `buildSchedulingPlan` is tested directly via
`schedulingPlanBeadIDs` (`dispatcher_test.go:24526-24531`), which is where
priority order actually lives. Relaxing the `tryAssign` tests to set equality
therefore loses no coverage.

**Rejected: assert bead-by-worker-slot.** The first draft of this design
proposed this and claimed the pairing was deterministic. It is not. `idle` is
built by ranging `d.workers`, a Go map, with no sort (`dispatcher.go:6022-6032`),
so bead→worker-name pairing is a random rotation per run. Both reviewers
independently caught this; one measured 3 distinct orderings in 30 runs versus
1 distinct sorted set. Implementing it would have converted a deterministic
failure into a ~30%-per-run flake — strictly worse for the fingerprint churn
this design exists to stop. Pinning the pairing would require sorting `idle`
inside `tryAssign`, a production change this design does not propose.

### 3. Migrate all 13 sites, not just the three patched ones

Replace `d.tryAssign(ctx)` with `tryAssignAndWait(t, d, ctx)` at every Class A
site listed above — including the three outside `dispatcher_test.go`
(`recovery_quarantine_test.go:997`, `:1123`, `spawn_for_test.go:405`) — and
drop the three `d.wg.Wait()` calls `c629e33e` added (`dispatcher_test.go:24548`,
`:24574`, `:24693`). `TestPriorityContention` calls `tryAssign` more than once;
every call needs the barrier.

Pre-existing `d.wg.Wait()` uses elsewhere (`dream_test.go:466`,
`dispatcher_test.go:17317`, `:22324`, `:22390`, `:26796`) are out of scope —
they predate this regression.

## Rejected alternatives

**Revert `c629e33e`.** Would make all 13 tests pass untouched, and is the
tempting move under QG pressure. Rejected: the change is production-sound (see
risks) and reverting reintroduces the cold-start stall it fixed. Note the
original bug report is *latency*, not signal loss — `workerReadyCh` is a
buffered cap-1 channel (`dispatcher.go:1374`, `:1227`), so worker B's ready
signal was never dropped, only delayed by one worktree setup. Both reviewers
confirmed the commit improves latency, not correctness; that is still worth
keeping.

**Test-side condition polling.** Zero production diff, but this is a flake
incident and deadline-based waits under quality-gate load are themselves a
flake source. Retained only as fallback where the barrier does not fit.

**Insert the `assignments` row synchronously at reservation.** Would restore a
deterministic `ORDER BY id`. Rejected: `createAssignment` requires the worktree
path (`dispatcher.go:10176`), the slow output, so this needs a
placeholder-insert-then-update against a table with an active-row uniqueness
rule (`assignment_attempt_test.go:13-30`). Large blast radius to restore an
ordering no consumer needs.

## Risks

**Paper tiger — double assignment under concurrent setup.** `assignBeadWithClaim`
holds `d.mu` across the `assigningBeads` check, an all-worker sweep for an
existing Busy/Reserved claim on the bead, and the `w.state == WorkerIdle`
revalidation (`dispatcher.go:7244-7285`). Three post-reservation re-checks guard
the slow tail — `assignmentReservationHeld` (`:7366`), `focusChangedSince`
(`:7387`), `attachAssignmentToReservation` (`:7476`) — each falling into
`abortAssignmentReservationLost` (`:7732`). Both reviewers verified this
independently; no rollback hole is unique to `c629e33e`.

**Paper tiger — a production consumer depending on `assignments` insert order.**
Narrowly: no consumer depends on *within-pass scheduling order*. Production does
use descending `id`, but always scoped to a single bead — newest requeued row
(`dispatcher.go:5475-5495`), newest active row (`:10869-10880`),
`assigned_at DESC, id DESC` for the active worker (`beadstore/sqlite.go:726-749`,
`:915-926`), `NOT EXISTS (... newer.id > a.id)` (`recovery_quarantine.go:190-194`),
`id NOT IN (SELECT MAX(id) ... GROUP BY bead_id)` (`protocol/schema.go:46-52`).
A pass inserts at most one row per bead, so cross-bead interleaving is
unobservable to all of them.

**Resolved (was an Elephant) — code reading a Reserved worker's empty worktree.**
Investigated and found safe; recorded here so it is not re-investigated.
`collectTimedOutWorkersLocked` opens with `if w.state == protocol.WorkerReserved
{ continue }` (`worker_pool.go:578-581`), and `protocol/types.go:172` documents
Reserved as "transient: I/O in progress, heartbeat checker must skip" — so
heartbeat, progress-timeout, review-timeout and dead-process reaping never see
one. The single real reader is `shutdownWorkerForClose` (`dispatcher.go:6529`,
via `checkClosedBeadAssignments` at `:6456`), and reading `""` there is correct:
a Reserved worker has never been sent an ASSIGN, so it has no branch commits,
and `finalizeExternalClose`'s `if worktree != ""` guard skips ff-merge recovery.

## Found during review — separate defects, not fixed here

Both are pre-existing and independent of the test failures, but `c629e33e`
widened the window in which they are observable. Each gets its own task.

1. **Reserved worker can be sent ASSIGN after graceful shutdown begins.**
   `gracefulShutdownWorker` only sets `WorkerShuttingDown` when
   `reason == shutdownReasonScaleDown || w.spawnFor` (`worker_pool.go:750-785`),
   so a normal Reserved worker stays Reserved; detached setup can then pass the
   reservation check and send ASSIGN (`dispatcher.go:7470-7496`), racing
   shutdown's active-row reset (`:10945-11032`).
2. **A hung worktree operation reserves a worker indefinitely.** Reserved is
   heartbeat-immune by design, and the detached setup has no timeout, so a hung
   git operation strands the worker with no reaper.

Also filed separately: `TestConcurrentGatesNoTimingFlakeSerialLaneCatchesRegression`
(`quality_gate_concurrency_test.go:191`) fails under full-package parallel load
and passes standalone — unrelated to assignment, but it breaks any
full-package `-count=N` acceptance.

## Out of scope

- The staged-but-uncommitted grade-ladder revert in the primary checkout (171
  deletions across `pkg/config/agent.go`, `.oro/config.yaml`,
  `scripts/quality_gate.sh` and others). Committing it would undo
  `1a662348`/`3029deaa`/`f44f9717`. Needs its own decision.
- The `TestGradeRoleLadder` half of incident 465, already fixed by `f44f9717`
  and verified passing at HEAD.
- Pre-existing `d.wg.Wait()` sites listed in change 3.

## Acceptance

Deliberately scoped away from full-package `-count=5`, which cannot pass at
HEAD for an unrelated reason (see above).

- `go test ./pkg/dispatcher/ -count=10 -timeout 600s -run 'TestTryAssign|TestPriorityContention|TestAssignment_SkipsClosedBeads|TestPreservedWorktree|TestOfflineRequeue|TestSpawnFor_StalePending'` green — `-count=10` because the defect is a flake and a single green run proves nothing.
- `go test ./pkg/dispatcher/ -count=1 -timeout 600s` green apart from the
  separately-filed load flake.
- No `d.wg.Wait()` remains in any test that calls `tryAssign`.
- `TestTryAssignReturnsAfterReservingSingleWorkerWithSlowWorktreeSetup`
  (`dispatcher_test.go:24637`) still passes — the cold-start property
  `c629e33e` added is preserved.
- Bead `oro-jv92` retries without reproducing fingerprint
  `qg:7c10d57242653660ab37f6d9`.
