# Factory core: autonomous delivery and recovery, first increment

Status: design for implementation. Baseline: `79f6cad693fdcbf83c9f19f96a785b80e6ae5ff7`.

## Goal and success boundary

Build a small replacement lifecycle alongside the existing dispatcher. Given an objective, fixed worker/acceptance/review programs, a repository and an integration branch, it produces an independently accepted Git candidate, integrates that exact candidate, automatically retries repairable failures within a persistent budget, and reconciles interrupted integration on restart. There is no human approval step in this lifecycle.

This increment is an experimental execution path for trusted command adapters in disposable or externally isolated repositories. It proves lifecycle correctness with real Git, SQLite, subprocesses, independent acceptance and failure injection. It does not establish hostile-code containment, general model competence, autonomous deployment, or recovery of the host itself. Those capabilities are prerequisites for a future live-factory cutover, not claims made by this increment. The existing factory is neither migrated nor replaced automatically.

The demonstration must include an actual runnable artifact: a first attempt produces the wrong behavior, an external acceptance program detects it, the next attempt consumes the feedback and repairs it, an independent reviewer approves the exact candidate, and promotion occurs without operator intervention. Execute the promoted artifact again from a fresh target checkout. Include an ignored worker build output that would mask incorrect committed source if the worker directory were reused. Tests also stop execution after promotion but before durable completion, reopen the state, and establish idempotent recovery.

## Current-source revalidation

The earlier audit used `772f4b94`. On the newer baseline:

- `pkg/dispatcher/ready_evidence.go` now creates durable review checkpoints during READY admission. Startup recovery uses pinned source/target identities. These improvements should inform the new contract.
- `pkg/dispatcher/review.go:handleReviewApproved` still does not persist the corresponding Approved transition. `completion.go:handleDone` still accepts an unbound completion; normal `mergeAndComplete` uses the unpinned path.
- `pkg/merge/merge.go:updatePinnedTargetRef` uses expected-old-SHA compare-and-swap. Normal non-main advancement and epic advancement retain unchecked updates. Reuse the CAS algorithm, without the existing coordinator's coupled cleanup semantics.
- Epic acceptance still lacks an explicit candidate directory and falls back to completion for missing/unreadable acceptance. QG exhaustion still reopens work and clears local retry counters.
- `pkg/dbutil.OpenDB` is reusable. Runtime adapters import old worker/ops lifecycle packages; `processenv.ForWorkdir` creates shared cache/storage state. The replacement must not depend on those orchestration packages.

Eleven focused existing tests passed during revalidation, including the direct ASSIGN-to-DONE merge, READY evidence creation, pinned recovery, and exhaustion/reopen cases. Passing these tests does not mean their current semantics are the desired replacement contract.

## Chosen structure

Add `pkg/factory` and `oro factory run/status`. The public package surface is `Run(ctx, stateDir, Spec) (Status, error)` and `ReadStatus(ctx, stateDir, id) (Status, error)`. `cmd/oro/root.go:rootSubcommands` registers the command. Command constructors perform no I/O.

The package owns a separate SQLite database, immutable admitted specification, policy copies, evidence, and attempt worktrees beneath the explicit state directory. It never opens the live Oro task database or emits legacy DONE/READY messages. Dependencies are the standard library and existing `pkg/dbutil`; no dispatcher, worker, ops, storage, or processenv imports. A structural test enforces that boundary.

One exclusive OS file lock per objective guards the lifecycle; a second runner fails clearly without modifying state. SQLite uses full synchronous durability and transactional state/event updates. Configure and verify FULL on the actual connection used for transitions, rather than assuming the shared opener supplies it. The durable snapshot has a revision; updates require the expected revision. File-lock ownership is released by the OS on process death. Test two controller processes and release after controller death. This coordinates cooperating controllers, not hostile same-user programs.

### Alternatives

1. Patch each legacy lifecycle: smaller individual changes, but preserves separate authority and completion rules.
2. **Independent replacement lifecycle with reusable leaf primitives:** chosen; small enough to prove while the old system remains available.
3. Rewrite runtime adapters, indexing, knowledge, UI, and storage together: rejected for this increment because none is needed to prove the core guarantees.

## Admission contract

`Spec` JSON fields:

```json
{
  "id": "calculator",
  "objective": "Implement integer addition, including negative inputs",
  "repository": "/absolute/path/to/repo",
  "target_branch": "factory/calculator",
  "worker": {"path": "/absolute/policies/worker", "args": []},
  "acceptance": {"path": "/absolute/policies/acceptance", "args": []},
  "review": {"path": "/absolute/policies/review", "args": []},
  "required_checks": ["positive-addition", "negative-addition"],
  "max_attempts": 3,
  "timeout_seconds": 300,
  "step_timeout_seconds": 60
}
```

IDs match `[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}`. Objective and checks are nonempty; check names are unique. Limits are positive; step timeout is no greater than total timeout. Reject unknown JSON fields, trailing documents, missing/non-executable programs, non-Git repositories, invalid branch names, and a target checked out in any linked worktree. The target must already exist; setup may create a dedicated delivery branch once before submitting work. The state directory and policy programs must be outside the source repository, resolving symlinks before containment checks.

At first admission, copy the three executable policy files into the objective directory, preserving only executable permissions. Compute a SHA-256 over canonical spec JSON and the three program contents. Persist both the immutable spec and this policy identity before starting work. Resume must match the admitted spec and source program identities exactly; changing objective, programs, checks or budget under the same ID is rejected. Validate the copied programs against their admitted digests before every execution and before promotion. Script interpreters/toolchain dependencies remain part of the declared trusted execution environment; copying a file is not OS isolation.

The absolute deadline is computed and persisted once at admission. It does not move on restart. There is no automatic budget extension, child-objective creation or counter reset.

## Process protocol

Programs receive one JSON document on stdin, never an interpolated shell command. The worker runs in its attempt worktree. Acceptance and review each run in a separate, newly created pristine detached checkout of the recorded candidate SHA; neither reuses the worker directory or the other checker's directory. Input fields are `objective`, `attempt`, `worktree`, `candidate_sha`, `base_sha`, and `feedback`. For the worker, `candidate_sha` is empty. The worker edits files and must not commit; the core commits its changes after successful exit. Arguments are literal argv from the admitted policy.

Strip inherited `ORO_*` and `GIT_*` identity/override variables. Give each attempt private temporary/cache paths, disable interactive Git editors, and cap captured stdout and stderr at 1 MiB each. Exceeding the cap fails the step; do not parse truncated output as success. Apply the smaller of the remaining objective deadline and step timeout. Cancellation/timeout kills the subprocess group and waits for termination. Retain bounded diagnostics in durable feedback.

The acceptance program must exit zero and return exactly one JSON receipt:

```json
{"candidate_sha":"<exact commit>","passed":true,"checks":["positive-addition","negative-addition"],"feedback":""}
```

The reviewer must exit zero and return:

```json
{"candidate_sha":"<exact commit>","approved":true,"feedback":""}
```

Unknown fields, trailing output, missing/wrong candidate identity, missing/duplicate checks, process error, negative verdict, timeout or truncation fail closed. Acceptance must name every required check. A nonzero exit wins over a positive receipt. Failed receipts/diagnostics become feedback for the next worker attempt. Worker output is diagnostic only and cannot approve or close work.

The programs are separate processes with separate admitted policy identities. This does not assert that their judgments are statistically independent or correct; fixture acceptance explicitly runs the delivered program and compares observed results against fixed expected values. Real-agent adapters can implement the same protocol without adding an alternate completion route.

## Durable lifecycle

States: `pending`, `running`, `candidate`, `accepted`, `approved`, `promoting`, `completed`, `failed`.

The snapshot records immutable spec/policy identity, created/deadline timestamps, attempts consumed, revision, current state, base and candidate SHA, attempt worktree, acceptance/review evidence, feedback and terminal reason. Every transition and its new snapshot commit in one SQLite transaction. Evidence records bind stage, candidate SHA, base SHA, policy identity, exit status and output digest.

1. **Reserve attempt:** before spawning or creating a worktree, atomically increment attempts and enter `running`. If attempts are exhausted or the deadline has elapsed, persist `failed` and spawn nothing.
2. **Prepare:** resolve the current target SHA and create a unique detached worktree for this attempt. Never reuse an interrupted attempt's writable worktree. Preserve previous worktrees for diagnosis.
3. **Implement:** execute worker. On success, stage its edits and create a core-owned commit if there are changes. A no-change candidate is valid if acceptance proves the objective already holds. Persist `candidate` and immutable SHA.
4. **Accept:** create a pristine detached checkout of that SHA and run the frozen acceptance policy there. Check tracked content and HEAD against the candidate before and after execution. Persist valid evidence and `accepted` only on success. Ignored/untracked worker artifacts must never be present in this checkout; checker-created outputs are never promoted or reused by another checker.
5. **Review:** create another pristine detached checkout of the same SHA, run review, require exact identity, and persist `approved` with both evidence records present. Any tracked mutation or HEAD movement invalidates the attempt. Checker-generated files cannot carry into another stage.
6. **Prepare promotion:** in one transaction verify candidate/base/policy/evidence identities and persist `promoting` with expected old target SHA and proposed new SHA.
7. **Promote:** recheck policy identity, object identity, ancestry and that the target is not checked out. Run `git update-ref refs/heads/<target> <candidate> <expected-old>`. No blind ref update, rebase after approval, checkout of the primary tree, push, or cleanup in this operation.
8. **Complete:** after proving the target equals the candidate, persist `completed`. Return the accepted commit and attempt count. Re-running completed work validates the recorded artifact exists and returns its stored receipt; it does not execute work again.

A failed worker/acceptance/review step or target contention persists its explanation, clears current evidence, and loops to a new bounded attempt from the current target. Persistence errors return an error; they never become a terminal-success message. An exhausted/deadline-expired objective becomes durably `failed`, retaining all work and diagnostics.

### Restart reconciliation

- `pending`: start normally.
- `running`, `candidate`, `accepted`: treat the interrupted attempt as spent, retain its artifacts, and retry within the original budget/deadline. No stale subprocess result is consumed by the new process.
- `approved`: recheck persisted evidence and policy, then prepare promotion; no new worker is needed if identities still match and the deadline remains valid.
- `promoting`: inspect actual Git state. If target equals candidate, finalize completion idempotently even if the deadline has since elapsed: the authorized effect already happened. If target equals expected old SHA, retry promotion only while evidence and deadline remain valid. If target moved elsewhere, invalidate this attempt and retry from the new target within budget. Never overwrite an intervening advance. A checked-out target blocks mutation and terminates with an actionable failure.
- `completed` or `failed`: return the durable terminal result without spending another attempt.

The core handles worker failures automatically. Restarting a killed controller is the external supervisor's responsibility in this increment; resume itself requires no human reconciliation. A later supervisor/cutover increment must demonstrate restart without operator commands. Old subprocess/descendant containment across controller death is likewise part of that production execution boundary; unique worktrees prevent a late cooperative worker from changing a new candidate, but do not establish a hostile-code boundary.

## Public API and implementation seams

```go
type Command struct { Path string `json:"path"`; Args []string `json:"args"` }
type Spec struct {
    ID string `json:"id"`
    Objective string `json:"objective"`
    Repository string `json:"repository"`
    TargetBranch string `json:"target_branch"`
    Worker Command `json:"worker"`
    Acceptance Command `json:"acceptance"`
    Review Command `json:"review"`
    RequiredChecks []string `json:"required_checks"`
    MaxAttempts int `json:"max_attempts"`
    TimeoutSeconds int `json:"timeout_seconds"`
    StepTimeoutSeconds int `json:"step_timeout_seconds"`
}
type Status struct {
    ID string `json:"id"`
    State string `json:"state"`
    Attempts int `json:"attempts"`
    CandidateSHA string `json:"candidate_sha,omitempty"`
    TargetBranch string `json:"target_branch"`
    Reason string `json:"reason,omitempty"`
}
func Run(ctx context.Context, stateDir string, spec Spec) (Status, error)
func ReadStatus(ctx context.Context, stateDir, id string) (Status, error)
```

Private seams: durable store/snapshot transitions; Git candidate/promotion adapter; bounded command runner; receipt validation; lifecycle driver. Keep private implementation types within `pkg/factory`; the CLI consumes only the public surface above. Production failpoint flags are prohibited; tests inject hooks at internal boundaries to simulate process interruption without adding a bypass to the shipped CLI.

## CLI behavior

`oro factory run --spec <file> --state-dir <directory>` reads strict JSON and prints one status JSON object. Exit 0 only for completed; terminal failed and infrastructure errors return nonzero with truthful diagnostics. `oro factory status --id <id> --state-dir <directory>` prints durable status without starting work. Missing state is an error. Help describes the experimental/trusted-adapter execution envelope, not a production isolation claim. No implicit access to `~/.oro`.

## Acceptance and task coverage

The epic acceptance command on the integrated feature branch is `go test ./pkg/factory -run '^TestFactoryAutonomousDeliveryRecovery$' -v -count=1 -timeout=120s`. Repeat it on main after an eventual approved integration; this work does not claim deployment merely by passing on a feature branch.

| Task | Files | Named acceptance test and required result |
| --- | --- | --- |
| 1. Admit immutable objectives | `contract.go`, `contract_test.go` | `TestAdmissionContract`: reject invalid JSON/paths/limits/policies; changed spec cannot resume |
| 2. Persist bounded attempts | `store.go`, `store_test.go` | `TestDurableAttemptBudget`: reopen DB after failure; attempts/deadline unchanged; no fourth attempt; stale revision rejected; verify FULL durability and process-level objective exclusion/release |
| 3. Snapshot Git candidates | `git.go`, `git_test.go` | `TestCandidateIsolation`: real repository, detached unique worktrees, core commit, separate pristine evidence checkouts, ignored worker output excluded, checked-out target rejected |
| 4. Promote by compare-and-swap | `git.go`, `git_test.go` | `TestPromotionCAS`: external target advance is preserved; no automatic cleanup; no rebase after evidence |
| 5. Bound command execution | `runner.go`, `runner_test.go` | `TestBoundedExecution`: nonzero, timeout, cancellation, overflow and inherited identity cannot produce success |
| 6. Validate exact receipts | `receipts.go`, `receipts_test.go` | `TestCandidateReceipts`: reject missing/wrong SHA, missing checks, malformed/trailing JSON, errors with positive verdict |
| 7. Drive automatic repair | `engine.go`, `engine_test.go` | `TestFactoryAutonomousDeliveryRecovery`: wrong runnable artifact with misleading ignored worker output is independently rejected, repaired from feedback, reviewed and promoted; fresh checkout of delivered target executes correct behavior |
| 8. Reconcile interrupted promotion | `engine.go`, `recovery_test.go` | `TestRestartPromotion`: crash before/after CAS, reopen state, no duplicate effect, stale policy rejected, original budget enforced |
| 9. Expose CLI lifecycle | `cmd/oro/cmd_factory.go`, `root.go`, `cmd_factory_test.go` | `TestFactoryCLI`: real command dispatch invokes new core, reports completed/failed/status correctly; no legacy task-state mutation |

All tasks use tests first, real Git/SQLite where relevant, and narrow process fixtures. Final validation runs the whole new package with race detection and coverage, the explicit CLI tests after `make stage-assets`, formatting/lint/build and the repository gate. Existing baseline failures are recorded separately and must not be disguised by altering the policy.

## Design risks and safeguards

- **New parallel lifecycle could remain an unused library:** CLI registration plus a real command acceptance test is required in task 9.
- **A new abstraction could repeat old permissive semantics:** independent fixtures assert rejected early approval, immutable evidence, no false terminal status, and fixed lifetime budget.
- **Git succeeds while DB write fails:** durable promotion intent and actual-ref reconciliation close this window; cleanup is separate and omitted initially.
- **A test fixture could be mistaken for a production factory:** reports explicitly separate deterministic lifecycle proof, real-agent trials, sandbox/supervisor work, and cutover evidence.
- **Prototype expansion could recreate existing complexity:** no task graphs, memory, learning, model routing, UI, remote CI, migration, or automatic cleanup in this increment.

## Cutover prerequisites after this increment

Implement and demonstrate an isolated execution backend with protected state/refs/policies and reliable descendant cleanup; a minimal external supervisor with a known-working rollback version; autonomous intake/planning and generated acceptance evaluated against held-out product outcomes; representative real-model workloads and objective-level cost accounting; fault-injection soak tests; then migration and rollback. None is replaced by a human approval gate. They remain required before claiming the fully autonomous software factory is complete.
