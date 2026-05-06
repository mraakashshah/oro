# Quality Gate Semaphore and Evidence Design

Date: 2026-05-06

## Summary

Oro should decouple worker concurrency from quality-gate concurrency. The factory can benefit from more workers, but quality gates are expensive enough that three concurrent runs can OOM the host. The best version is not only a semaphore. It is a global QG resource limiter plus verifiable QG evidence so Oro can avoid rerunning the same expensive gate when it creates no new information.

The design has two core capabilities:

1. A project-local QG lease service that limits concurrent QG executions across workers, dispatcher pre-merge checks, epic checks, and `oro work`.
2. A QG evidence record that lets the dispatcher decide whether worker-side QG results are sufficient for merge, or whether a dispatcher-side QG must run because the tested state changed.

## Goals

- Allow `workers=4` while preventing 3+ simultaneous QG executions.
- Preserve the trust boundary: no merge based only on a worker's unverifiable boolean.
- Avoid duplicate full QG runs when the worker already tested the exact commit under the same relevant conditions.
- Make QG backpressure observable in `oro status` and event logs.
- Ensure pass, fail, error, timeout, cancellation, and process death release QG capacity.
- Keep default behavior safe for current machines: default global QG concurrency should be `2`.

## Non-Goals

- Full CPU/RAM resource scheduling.
- Per-language QG scheduling.
- Replacing `scripts/quality_gate.sh`.
- Removing worker-side QG.
- Removing epic QG.
- Changing review semantics except where QG evidence is required before merge.

## Current State

Worker QG path:

- `pkg/worker/worker.go:awaitSubprocessAndReport` waits for the coding subprocess to finish.
- `pkg/worker/worker.go:runQGAndReport` calls `RunQualityGate(ctx, wt, false)`.
- On failure, worker sends `DONE` with `QualityGatePassed=false`.
- On pass, worker stores QG output and sends `READY_FOR_REVIEW`.
- On review approval, worker sends `DONE` with `QualityGatePassed=true` and QG output.

Dispatcher QG path:

- `pkg/dispatcher/dispatcher.go:handleDone` accepts a passing `DONE`.
- `mergeAndComplete` calls `checkPreMergeQG`.
- `checkPreMergeQG` calls `d.qgRunner.Run(ctx, worktree, false)` before merge.
- `checkEpicQG` creates an epic worktree and calls `d.qgRunner.Run(ctx, worktree, false)`.

Protocol state:

- `pkg/protocol/message.go:DonePayload` carries only `QualityGatePassed bool` and `QGOutput string`.
- There is no commit SHA, base SHA, script hash, run ID, mode, or evidence status.

Risk:

- QG calls are unconstrained and duplicated.
- A dispatcher-only semaphore would not control worker-side QGs.
- Removing dispatcher QG entirely would trust a boolean and miss changed-state cases.

## Design

### 1. QG Lease Service

Introduce a project-local QG lease service in the dispatcher process. All long-running Oro QG invocations acquire a lease before spawning `quality_gate.sh`.

Lease scope:

- Worker-side QG in `pkg/worker`.
- Dispatcher pre-merge QG in `pkg/dispatcher`.
- Dispatcher epic QG in `pkg/dispatcher`.
- Lightweight `oro work` QG in `cmd/oro/cmd_work.go`.

Default concurrency:

- `MaxQGConcurrency=2`.
- `0` is invalid for production start; use `1` for serialized gates.
- Tests may explicitly set high values or use a no-op limiter.

Acquisition semantics:

- FIFO fairness is preferred, but bounded correctness matters more than strict ordering.
- Acquire observes context cancellation.
- Release is idempotent or guarded by a single-use lease object.
- Every caller uses `defer lease.Release()` immediately after a successful acquire.

Timeout semantics:

- The limiter does not impose a QG timeout by itself.
- The existing operation context remains authoritative.
- If the caller context is canceled while waiting, no lease is consumed.
- If the caller context is canceled during QG execution, `exec.CommandContext` kills the script and release still runs.

Events:

- `qg_wait_start`: component, bead_id, worker_id, mode, max_concurrency.
- `qg_wait_done`: wait duration, granted=true.
- `qg_wait_cancelled`: wait duration, error.
- `qg_run_start`: lease_id, component, mode, active_count, waiting_count.
- `qg_run_done`: lease_id, passed, duration, output_hash, error_kind.

Status:

- `qg_running`
- `qg_waiting`
- `qg_max_concurrency`
- optional per-entry bead IDs and modes for verbose/JSON status.
- Status throttling must either include QG state in the cache invalidation path or document bounded staleness; stale QG backlog numbers must not hide an active deadlock.

### 2. Shared Access From Worker Processes

Workers are separate processes, so an in-memory dispatcher semaphore is insufficient.

Preferred implementation:

- Dispatcher owns the limiter.
- Workers request leases over the existing UDS using a new protocol message:
  - `QG_LEASE_REQUEST`
  - `QG_LEASE_GRANTED`
  - `QG_LEASE_RELEASE`
- Worker wraps `RunQualityGate` with a lease-aware runner.
- If the dispatcher connection is unavailable, the worker must fail closed for factory mode: QG is not run unbounded.

Dispatcher protocol routing:

- New QG lease messages must be first-class `protocol.Message` variants.
- `pkg/dispatcher/dispatcher.go:extractWorkerID`, `extractBeadID`, and `handleMessage` must route lease request/release messages.
- Lease grant responses must be written over the worker connection without blocking unrelated dispatcher message handling.
- `connCloseCleanup` must release all leases owned by the worker/session whose connection closed.
- Lease ownership must include both worker ID and dispatcher session/connection identity so reconnects cannot release another process's lease accidentally.

Fallback for `oro work`:

- If `oro work` is connected to a dispatcher, use the dispatcher lease service.
- If no dispatcher is running, use a local file lock under the project Oro home so multiple `oro work` processes still do not stampede.
- The local lock path must be project-scoped, not global across all repos.
- All `cmd/oro/cmd_work.go` QG call sites must go through the same limiter abstraction, not only the first post-coding gate.

Why not only SQLite leases:

- SQLite can represent leases, but process death and stale lease cleanup become the hard part.
- The dispatcher already has liveness, worker identity, event logging, and status; it is the right primary owner.
- A file lock remains useful for dispatcher-less `oro work`.

### 3. QG Evidence

Add a structured evidence object to `DonePayload`, preserving the existing boolean for wire compatibility.

```go
type QGEvidence struct {
    RunID          string `json:"run_id"`
    BeadID         string `json:"bead_id"`
    WorkerID       string `json:"worker_id,omitempty"`
    Component      string `json:"component"` // worker, dispatcher-pre-merge, dispatcher-epic, oro-work
    Mode           string `json:"mode"`      // local-no-mutation, full, epic
    Worktree       string `json:"worktree,omitempty"`
    HeadSHA        string `json:"head_sha"`
    BaseSHA        string `json:"base_sha,omitempty"`
    TargetBranch   string `json:"target_branch,omitempty"`
    QGScriptHash   string `json:"qg_script_hash"`
    OroBinarySHA   string `json:"oro_binary_sha,omitempty"`
    Passed         bool   `json:"passed"`
    OutputHash     string `json:"output_hash,omitempty"`
    StartedAt      string `json:"started_at"`
    FinishedAt     string `json:"finished_at"`
}
```

Evidence must be generated by the code that actually runs QG, not by the caller after the fact.

Required values:

- `HeadSHA`: `git rev-parse HEAD` in the tested worktree after QG completes.
- `BaseSHA`: merge base between `HEAD` and target branch when available.
- `QGScriptHash`: SHA-256 of the exact script file executed.
- `Mode`: distinguishes skipped mutation/local gate from full gate.
- `OutputHash`: SHA-256 of combined output; keep full output in existing retry feedback paths, not in status.

Worker review handoff:

- Worker QG passes before the worker sends `READY_FOR_REVIEW`.
- `DONE` is sent later from `pkg/worker/worker.go:handleReviewResult` after approval.
- Therefore the worker must persist pending QG evidence alongside `pendingQGOutput`.
- `handleAssign` must clear pending evidence exactly as it clears stale pending QG output.
- Review rejection must discard pending evidence because the next attempt must produce fresh evidence for a new tested state.

Persistence:

- Store evidence in SQLite, keyed by `run_id`.
- Index by `bead_id`, `head_sha`, `component`, `passed`, and `finished_at`.
- Store the latest passing evidence per bead/head for quick lookup.
- Keep event-log summaries small; full output remains in existing QG failure handling.
- Add schema/migration coverage through the same state DB path used by dispatcher startup.
- Evidence writes are best-effort for failed runs, but passing evidence required for a merge decision must be persisted or the dispatcher must run its own QG.

### 4. Dispatcher Evidence Policy

Dispatcher pre-merge should no longer blindly rerun QG on every passing worker `DONE`.

Policy function:

```go
func ShouldRunPreMergeQG(doneEvidence QGEvidence, current MergeContext) Decision
```

Return values:

- `AcceptEvidence`: skip dispatcher pre-merge QG and proceed to merge.
- `RunPreMergeQG`: acquire a lease and run dispatcher QG.
- `RejectEvidence`: treat as failed or malformed completion and retry/escalate.

Accept worker evidence only when all are true:

- `QualityGatePassed=true`.
- Evidence is present and `Passed=true`.
- `HeadSHA` equals current worktree `HEAD`.
- `QGScriptHash` equals the current tested script hash in the worktree.
- `Mode` is compatible with the required merge gate mode.
- The worker identity in evidence matches the assigned worker or an accepted reconnect identity.
- The bead has not been changed into an epic mid-flight.
- The branch has not been modified by ops after the evidence was produced.

Run dispatcher QG when any are true:

- Evidence missing.
- Evidence malformed.
- Current worktree HEAD differs.
- QG script hash differs.
- Ops/rebase/conflict repair changed the branch after worker QG.
- Target branch moved in a way that policy marks as requiring retest.
- Manual override requests full pre-merge gate.

Reject evidence when:

- Evidence claims pass but `HeadSHA` cannot be verified.
- Evidence bead/worker IDs do not match assignment.
- Evidence timestamp is impossible or stale relative to assignment lifecycle.

Target branch movement:

- For the first implementation, target branch movement should force dispatcher QG. This is conservative.
- Later optimization can use affected-file analysis or merge-base stability to accept evidence when target moved in unrelated files.

### 5. Epic QG Policy

Epic QG almost always creates new information because it validates combined child work.

Rules:

- Epic QG always acquires a global QG lease.
- Epic QG records evidence with `Component=dispatcher-epic` and `Mode=epic`.
- Epic close requires passing epic evidence for the epic branch head.
- Worker evidence for child tasks cannot substitute for epic QG.
- Epic evidence must be persisted before the epic is closed so restart/audit can prove why the epic was accepted.

### 6. Configuration

Startup flags:

- `oro start --qg-concurrency N`
- `oro dispatcher start --qg-concurrency N`

Startup propagation:

- Foreground `oro start` must pass `--qg-concurrency` through `ExecDaemonSpawner.buildArgs` into the daemon-only child.
- `oro dispatcher start --qg-concurrency` must pass the same value through its dispatcher-only start path.
- `runDaemonOnly` and `buildDispatcherWithReviewTimeouts` must set `dispatcher.Config.MaxQGConcurrency`.
- A daemon started with an existing dispatcher must reject impossible values and report the active value in status.

Directive:

- `oro directive qg-concurrency N`

Status JSON additions:

```json
{
  "qg_running": 1,
  "qg_waiting": 2,
  "qg_max_concurrency": 2,
  "qg_queue": [
    {"bead_id": "oro-123", "component": "worker", "worker_id": "worker-1", "wait_secs": 14}
  ]
}
```

Human status:

```text
  qg:          1 running, 2 waiting (max: 2)
```

Default:

- Production default: `2`.
- Test default: no-op or `runtime.NumCPU()` only when tests explicitly bypass production config.
- Existing tests that inject `mockQGRunner` should not hang on leases.

### 7. Failure Handling

Lease request fails:

- Worker sends `DONE(false)` with QG output explaining lease acquisition failure.
- Dispatcher logs `qg_lease_failed`.
- This is safer than running unbounded.

Worker dies while holding lease:

- Dispatcher releases leases owned by a disconnected/dead worker.
- Lease records include worker ID and connection/session ID.

Dispatcher dies while worker waits:

- Worker context eventually cancels or connection read fails.
- Worker reports failure if possible; otherwise startup recovery handles assignment.

Dispatcher dies while worker runs QG:

- Worker process may continue, but the dispatcher is down.
- On dispatcher restart, lease state is reconstructed from live worker connections or reset after stale timeout.
- The QG process is not trusted unless it reconnects with valid evidence and matching assignment.

QG fail:

- Lease releases.
- Existing retry/stuck logic remains.
- Evidence records failed runs for diagnostics but never authorizes merge.

QG error:

- Lease releases.
- Existing escalation paths remain for script missing/start errors.

QG backlog:

- This is intended backpressure.
- Backlog is visible via status/events.
- If backlog dominates merge throughput, lower worker count or split QG tiers; do not remove the limiter.

### 8. Testing Strategy

Unit tests:

- Limiter never allows active count above cap.
- Acquire respects context cancellation.
- Release on pass/fail/error/cancel.
- Worker `runQGAndReport` uses a lease-aware runner.
- Dispatcher pre-merge and epic QG use the same limiter.
- `DonePayload` round-trips evidence while preserving old fields.
- Evidence policy accepts exact matching worker evidence.
- Evidence policy forces dispatcher QG on missing, stale, mismatched, or changed-head evidence.
- Status includes QG running/waiting/max.

Integration tests:

- Four workers reach QG together; only two QG scripts run concurrently.
- Worker-side QG and dispatcher-side QG contend for the same cap.
- Failed QG releases a slot and the next waiter starts.
- Dispatcher restart does not preserve bogus stale leases.
- Accepted worker evidence skips duplicate dispatcher pre-merge QG.
- Ops-modified branch forces dispatcher pre-merge QG.
- Epic close always runs epic QG under the limiter.

Manual verification:

- Start with `--workers 4 --max-workers 4 --qg-concurrency 2`.
- Confirm `oro status` shows target/managed 4 and QG max 2.
- Use artificial sleep QG script in a temporary repo to prove only two run at once.
- Restore real QG and monitor one 25-minute factory window.

## Premortem

```yaml
premortem:
  mode: deep
  context: "global QG limiter plus evidence-based dispatcher QG skipping"
  tigers:
    - risk: "Only dispatcher QG is limited, worker QGs still OOM the host."
      severity: high
      mitigation_checked: "Spec requires worker, dispatcher, epic, and oro-work paths to use the same lease."
    - risk: "Evidence lets bad work merge because it records a boolean without verifying the tested commit."
      severity: high
      mitigation_checked: "Spec requires HeadSHA, script hash, mode, assignment identity, and policy checks before accepting evidence."
    - risk: "A leaked lease deadlocks QG throughput."
      severity: high
      mitigation_checked: "Spec requires defer release, owner/session IDs, disconnect cleanup, cancellation tests, and stale recovery."
    - risk: "Skipping pre-merge QG misses target-branch drift."
      severity: high
      mitigation_checked: "Initial policy forces dispatcher QG when target branch movement is considered relevant; later optimization is explicit follow-up."
  elephants:
    - risk: "Full QG may be too expensive to run per bead at all; the limiter makes the bottleneck visible instead of solving QG cost."
    - risk: "A UDS lease protocol is more plumbing than a local semaphore, but process boundaries make local-only semaphores dishonest."
  paper_tigers:
    - risk: "QG backlog reduces apparent worker utilization."
      reason: "Backpressure is safer than OOM; status exposes backlog so worker count can be tuned."
    - risk: "Old workers do not send evidence."
      reason: "Wire compatibility keeps QualityGatePassed; missing evidence forces dispatcher QG instead of merge rejection during rollout."
```

## Task Graph

Epic: Implement global QG limiting and evidence-based QG reuse.

1. Define QG evidence protocol
   - Test: `pkg/protocol/message_test.go:TestDonePayload_QGEvidenceRoundTrip`
   - Cmd: `go test ./pkg/protocol -run TestDonePayload_QGEvidenceRoundTrip -count=1 -v`
   - Assert: `DonePayload` preserves `QualityGatePassed`/`QGOutput` and round-trips structured evidence.
   - Read: `pkg/protocol/message.go:DonePayload`, `pkg/protocol/message_test.go:TestDonePayload_QualityGatePassed_RoundTrip`
   - Signature: `type QGEvidence struct {...}`
   - Edges: missing evidence remains valid wire input; malformed IDs fail validation where consumed.

2. Add QG limiter core
   - Test: `pkg/dispatcher/qg_limiter_test.go:TestQGLimiterCapsConcurrentRuns`
   - Cmd: `go test ./pkg/dispatcher -run TestQGLimiterCapsConcurrentRuns -count=1 -v`
   - Assert: four concurrent lease holders never exceed cap two, and all complete after release.
   - Read: `pkg/dispatcher/dispatcher.go:Config`, `pkg/dispatcher/dispatcher.go:New`
   - Signature: `type QGLimiter interface { Acquire(ctx context.Context, req QGLeaseRequest) (*QGLease, error) }`
   - Edges: context cancellation before grant returns error without consuming capacity.

3. Wire dispatcher QG calls through limiter
   - Test: `pkg/dispatcher/dispatcher_test.go:TestPreMergeAndEpicQGShareLimiter`
   - Cmd: `go test ./pkg/dispatcher -run TestPreMergeAndEpicQGShareLimiter -count=1 -v`
   - Assert: concurrent pre-merge and epic QG calls share one cap and release on failure.
   - Read: `pkg/dispatcher/dispatcher.go:checkPreMergeQG`, `pkg/dispatcher/dispatcher.go:checkEpicQG`, `pkg/dispatcher/dispatcher.go:ShellQGRunner`
   - Edges: QG pass, fail, script error, and canceled context all release the lease.

4. Add dispatcher lease protocol routing
   - Test: `pkg/dispatcher/dispatcher_test.go:TestDispatcherQGLeaseProtocolRoutesRequests`
   - Cmd: `go test ./pkg/dispatcher -run TestDispatcherQGLeaseProtocolRoutesRequests -count=1 -v`
   - Assert: dispatcher grants/releases worker QG leases over UDS and releases worker-owned leases on connection cleanup.
   - Read: `pkg/dispatcher/dispatcher.go:handleMessage`, `pkg/dispatcher/dispatcher.go:extractWorkerID`, `pkg/dispatcher/dispatcher.go:extractBeadID`, `pkg/dispatcher/dispatcher.go:connCloseCleanup`, `pkg/protocol/message.go:Message`
   - Signature: `type QGLeaseRequestPayload struct {...}`, `type QGLeaseGrantedPayload struct {...}`, `type QGLeaseReleasePayload struct {...}`
   - Edges: unknown worker, canceled waiter, worker disconnect while waiting, worker disconnect while holding lease.

5. Add worker lease client
   - Test: `pkg/worker/worker_test.go:TestWorkerQGUsesDispatcherLease`
   - Cmd: `go test ./pkg/worker -run TestWorkerQGUsesDispatcherLease -count=1 -v`
   - Assert: worker requests a lease before running QG and releases it after pass/fail/error.
   - Read: `pkg/worker/worker.go:runQGAndReport`, `pkg/worker/worker.go:RunQualityGate`, `pkg/protocol/message.go:Message`
   - Signature: `func (w *Worker) acquireQGLease(ctx context.Context, req protocol.QGLeaseRequestPayload) (*QGLease, error)`
   - Edges: dispatcher unavailable -> fail closed with `DONE(false)`.

6. Add `oro work` QG limiting
   - Test: `cmd/oro/cmd_work_test.go:TestWorkQGUsesProjectLimiter`
   - Cmd: `go test ./cmd/oro -run TestWorkQGUsesProjectLimiter -count=1 -v`
   - Assert: every `cmd_work` QG call site acquires a dispatcher lease when available and a project-scoped file lock when no dispatcher is running.
   - Read: `cmd/oro/cmd_work.go:newWorkDeps`, `cmd/oro/cmd_work.go:runWork`, `cmd/oro/cmd_work.go:handleReviewRejection`, `cmd/oro/cmd_work_execute_test.go`
   - Signature: `func limitedWorkQGRunner(base runQGFunc, limiter QGLimiter) runQGFunc`
   - Edges: first post-coding QG, mutation/final QG, QG after review rejection, dispatcher unavailable, two concurrent `oro work` processes.

7. Record QG evidence from actual runs
   - Test: `pkg/worker/worker_test.go:TestWorkerDoneIncludesQGEvidenceForTestedHead`
   - Cmd: `go test ./pkg/worker -run TestWorkerDoneIncludesQGEvidenceForTestedHead -count=1 -v`
   - Assert: passing worker QG sends evidence containing tested HEAD and QG script hash.
   - Read: `pkg/worker/worker.go:runQGAndReport`, `pkg/worker/worker.go:handleReviewResult`, `pkg/worker/worker.go:SendDone`, `pkg/worker/worker.go:RunQualityGate`, `pkg/worker/pending_qg_clear_test.go:TestHandleAssignClearsPendingQGOutput`
   - Signature: `func BuildQGEvidence(ctx context.Context, worktree string, opts QGEvidenceOptions) (*protocol.QGEvidence, error)`
   - Edges: detached HEAD, missing target branch, missing script hash, review rejection clears pending evidence, reassignment clears stale pending evidence.

8. Persist QG evidence
   - Test: `pkg/dispatcher/qg_evidence_store_test.go:TestQGEvidenceStoreLatestPassingByBeadHead`
   - Cmd: `go test ./pkg/dispatcher -run TestQGEvidenceStoreLatestPassingByBeadHead -count=1 -v`
   - Assert: state DB migration creates `qg_evidence`, inserts pass/fail records, and returns latest passing evidence for a bead/head.
   - Read: `pkg/protocol/schema.go:SchemaDDL`, `cmd/oro/db.go:migrateStateDB`, `pkg/dispatcher/dispatcher.go:New`
   - Signature: `func RecordQGEvidence(ctx context.Context, db *sql.DB, evidence protocol.QGEvidence) error`
   - Edges: duplicate run ID, failed evidence write on passing merge evidence, restart reads existing evidence.

9. Implement dispatcher evidence policy
   - Test: `pkg/dispatcher/qg_evidence_policy_test.go:TestShouldRunPreMergeQGDecisionMatrix`
   - Cmd: `go test ./pkg/dispatcher -run TestShouldRunPreMergeQGDecisionMatrix -count=1 -v`
   - Assert: exact matching evidence is accepted; missing/mismatched/stale evidence forces or rejects as specified.
   - Read: `pkg/dispatcher/dispatcher.go:handleDone`, `pkg/dispatcher/dispatcher.go:mergeAndComplete`, `pkg/protocol/message.go:DonePayload`
   - Signature: `func ShouldRunPreMergeQG(e *protocol.QGEvidence, ctx MergeQGContext) QGDecision`
   - Edges: changed HEAD, changed script hash, changed target branch, wrong worker, wrong bead.

10. Skip duplicate dispatcher QG when evidence is valid
   - Test: `pkg/dispatcher/dispatcher_test.go:TestPreMergeAcceptsMatchingWorkerQGEvidence`
   - Cmd: `go test ./pkg/dispatcher -run TestPreMergeAcceptsMatchingWorkerQGEvidence -count=1 -v`
   - Assert: dispatcher does not invoke `qgRunner.Run` when worker evidence exactly matches current merge context.
   - Read: `pkg/dispatcher/dispatcher.go:handleDone`, `pkg/dispatcher/dispatcher.go:checkPreMergeQG`, `pkg/dispatcher/dispatcher_test.go:mockQGRunner`
   - Edges: missing evidence remains backward compatible by running dispatcher QG.

11. Record dispatcher pre-merge QG evidence
   - Test: `pkg/dispatcher/dispatcher_test.go:TestPreMergeQGPersistsDispatcherEvidenceBeforeMerge`
   - Cmd: `go test ./pkg/dispatcher -run TestPreMergeQGPersistsDispatcherEvidenceBeforeMerge -count=1 -v`
   - Assert: when worker evidence is missing/stale and dispatcher runs fallback pre-merge QG, it persists `Component=dispatcher-pre-merge` evidence before authorizing merge.
   - Read: `pkg/dispatcher/dispatcher.go:checkPreMergeQG`, `pkg/dispatcher/dispatcher.go:mergeAndComplete`, `pkg/dispatcher/qg_evidence_store_test.go:TestQGEvidenceStoreLatestPassingByBeadHead`
   - Signature: `func BuildDispatcherQGEvidence(ctx context.Context, worktree string, opts QGEvidenceOptions) (*protocol.QGEvidence, error)`
   - Edges: evidence persist failure prevents evidence-based merge authorization, QG failure records failed evidence best-effort, script error does not authorize merge.

12. Record epic QG evidence before epic close
   - Test: `pkg/dispatcher/epic_qg_test.go:TestEpicQGPersistsEvidenceBeforeClose`
   - Cmd: `go test ./pkg/dispatcher -run TestEpicQGPersistsEvidenceBeforeClose -count=1 -v`
   - Assert: epic QG acquires a lease, records dispatcher-epic evidence for the epic branch head, and closes only after evidence is persisted.
   - Read: `pkg/dispatcher/dispatcher.go:checkEpicQG`, `pkg/dispatcher/epic_qg_test.go`, `pkg/dispatcher/dispatcher.go:completeEpicClose`
   - Edges: evidence persist failure, QG failure, temporary worktree cleanup.

13. Add config, startup propagation, directive, status, and events
   - Test: `cmd/oro/cmd_status_test.go:TestStatusShowsQGCapacity`
   - Cmd: `go test ./cmd/oro -run 'TestStatusShowsQGCapacity|TestDirectiveQGConcurrency|TestStartQGConcurrencyPropagatesToDaemon|TestDispatcherStartQGConcurrencyPropagatesToDaemon' -count=1 -v`
   - Assert: status prints QG running/waiting/max, directive adjusts max at runtime, and both `oro start --qg-concurrency N` and `oro dispatcher start --qg-concurrency N` reach daemon-only dispatcher config.
   - Read: `cmd/oro/cmd_start.go:newStartCmd`, `cmd/oro/cmd_start.go:ExecDaemonSpawner.buildArgs`, `cmd/oro/cmd_start.go:runDaemonOnly`, `cmd/oro/cmd_start.go:buildDispatcherWithReviewTimeouts`, `cmd/oro/cmd_dispatcher.go:newDispatcherCmd`, `cmd/oro/cmd_dispatcher.go:runDispatcherStart`, `cmd/oro/cmd_directive.go:newDirectiveCmd`, `cmd/oro/cmd_status.go:formatStatusResponse`, `pkg/protocol/directive.go`
   - Edges: invalid values reject, lowering below active count prevents new leases until active drops, status cache does not hide QG deadlock, qg_wait/qg_run events are logged.

14. Add integration coverage for four workers and cap two
   - Test: `pkg/integration/dispatcher_worker_test.go:TestGlobalQGLimiterCapsWorkerAndDispatcherRuns`
   - Cmd: `go test ./pkg/integration -run TestGlobalQGLimiterCapsWorkerAndDispatcherRuns -count=1 -v`
   - Assert: four workers can run, but active QG process count never exceeds two across worker and dispatcher phases.
   - Read: `pkg/integration/dispatcher_worker_test.go`, `pkg/worker/worker.go:runQGAndReport`, `pkg/dispatcher/dispatcher.go:checkPreMergeQG`
   - Edges: one QG failure releases slot, next waiter starts, worker-side and dispatcher-side QG contend for same cap, `oro work` does not bypass cap.

15. Document operating policy
   - Test: `docs/runbooks/factory-monitoring.md` or equivalent doc lint/manual review
   - Cmd: `./scripts/quality_gate.sh`
   - Assert: monitoring docs explain `workers=4`, `qg_concurrency=2`, QG backlog interpretation, and stop triggers.
   - Read: `docs/plans/2026-05-06-qg-semaphore-evidence-design.md`, existing monitoring docs.
   - Edges: backlog vs stuck worker distinction.

## Acceptance Test For Epic

Primary machine check:

```bash
go test ./pkg/protocol -run TestDonePayload_QGEvidenceRoundTrip -count=1
go test ./pkg/dispatcher -run 'TestQGLimiterCapsConcurrentRuns|TestPreMergeAndEpicQGShareLimiter|TestDispatcherQGLeaseProtocolRoutesRequests|TestQGEvidenceStoreLatestPassingByBeadHead|TestShouldRunPreMergeQGDecisionMatrix|TestPreMergeAcceptsMatchingWorkerQGEvidence|TestPreMergeQGPersistsDispatcherEvidenceBeforeMerge|TestEpicQGPersistsEvidenceBeforeClose' -count=1
go test ./pkg/worker -run 'TestWorkerQGUsesDispatcherLease|TestWorkerDoneIncludesQGEvidenceForTestedHead' -count=1
go test ./cmd/oro -run 'TestWorkQGUsesProjectLimiter|TestStatusShowsQGCapacity|TestDirectiveQGConcurrency|TestStartQGConcurrencyPropagatesToDaemon|TestDispatcherStartQGConcurrencyPropagatesToDaemon' -count=1
go test ./pkg/integration -run TestGlobalQGLimiterCapsWorkerAndDispatcherRuns -count=1
```

Final gate:

```bash
./scripts/quality_gate.sh
```

Operational acceptance:

- Launch with `--workers 4 --max-workers 4 --qg-concurrency 2`.
- `oro status` reports 4 managed workers and QG max 2.
- Artificial concurrent QG load never exceeds two active QG scripts.
- Valid worker evidence skips duplicate dispatcher QG.
- Changed worktree or script forces dispatcher QG.
- Epic QG still runs under the limiter.

## Adversarial Review

```yaml
verdict: CHALLENGE_FAIL_INCORPORATED
spec: docs/plans/2026-05-06-qg-semaphore-evidence-design.md
reviewer_note: "Fresh Codex challenge passes found missing coverage for dispatcher lease routing, oro work, persistence, startup propagation, dispatcher-only start, pre-merge evidence persistence, event observability, and review-handoff evidence. This revision incorporates those gaps into the design and task graph."
acceptance_test:
  cmd: "Run the explicit package/test list in Acceptance Test For Epic, then ./scripts/quality_gate.sh"
  assert: "QG concurrency is globally capped at 2, evidence-based skip works only for exact matching state, and full project gate passes."
  adequate: true
requirements_traceability:
  - criterion: "Global QG cap across worker and dispatcher"
    task: 2,3,4,5,14
    status: covered
  - criterion: "oro work QG uses dispatcher lease or project file lock"
    task: 6,14
    status: covered
  - criterion: "Evidence can replace duplicate dispatcher QG only when safe"
    task: 1,7,8,9,10,11
    status: covered
  - criterion: "Epic QG remains authoritative"
    task: 3,12,14
    status: covered
  - criterion: "Evidence persists for audit/restart"
    task: 8,11,12
    status: covered
  - criterion: "Startup flags and directives configure runtime limiter"
    task: 13
    status: covered
  - criterion: "Operators can observe and tune the limiter"
    task: 13,15
    status: covered
negative_space:
  - scenario: "Old worker sends no evidence"
    coverage: "Task 10 requires backward-compatible fallback to dispatcher QG."
  - scenario: "Worker QG evidence is lost between READY_FOR_REVIEW and DONE"
    coverage: "Task 7 requires pending evidence to be stored and cleared with pendingQGOutput."
  - scenario: "Worker dies while holding lease"
    coverage: "Task 4 requires lease release on disconnect/session death."
  - scenario: "Two oro work processes bypass dispatcher and OOM the host"
    coverage: "Task 6 requires project-scoped file locking when no dispatcher lease is available."
  - scenario: "Foreground start drops qg-concurrency when spawning daemon-only child"
    coverage: "Task 13 requires foreground and dispatcher-only start propagation tests."
  - scenario: "Dispatcher fallback pre-merge QG passes but leaves no audit evidence"
    coverage: "Task 11 requires dispatcher-pre-merge evidence persistence before merge authorization."
  - scenario: "Event observability is absent even though status looks fine"
    coverage: "Task 13 requires qg_wait/qg_run event logging."
  - scenario: "Target branch moves after worker QG"
    coverage: "Task 9 requires conservative retest."
integration_inventory:
  must_touch:
    - "pkg/protocol/message.go"
    - "pkg/protocol/schema.go"
    - "pkg/protocol/directive.go"
    - "pkg/worker/worker.go"
    - "pkg/dispatcher/dispatcher.go"
    - "cmd/oro/cmd_start.go"
    - "cmd/oro/cmd_dispatcher.go"
    - "cmd/oro/cmd_directive.go"
    - "cmd/oro/cmd_status.go"
    - "cmd/oro/cmd_work.go"
    - "cmd/oro/db.go"
    - "pkg/integration/dispatcher_worker_test.go"
```
