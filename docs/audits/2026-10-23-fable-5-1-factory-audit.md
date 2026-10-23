# Oro implementation audit — Claude Fable 5.1

Date: 2026-10-23. Reviewer: `claude-fable-5-1` via Claude Code. The runtime result confirmed this exact model and reported no permission denials.

Method: use the [staff engineer evaluation guide](../staff-engineer-evaluation-guide.md) as audit instructions to assess Oro’s implementation. This reviewer had source-reading and search tools only and ran no tests. The report below preserves the reviewer’s assessment; coordinator qualifications follow it.

**Decision: do not enable unattended automatic integration into a protected branch.** Oro is fit for supervised use, where a human treats "merged by Oro" as a candidate rather than an accepted change. The core loop is real and several safeguards are well built, but three of the guide's non-negotiable invariants fail on source inspection: completion authority, evidence binding, and bounded retry. I ran no tests or experiments in this session, so nothing below is Observed. Every finding is Implemented (traced through a reachable production path), Hypothesis (needs an experiment), or Unknown.

Scope was the code at 772f4b94 with the guide from 08a19549, read along the path a change travels: task admission, assignment, worker execution, review, merge, recovery, epics, and the standalone `oro work` path.

## Findings

**Critical 1. The dispatcher accepts DONE on a worker ID alone and never requires review.** (Implemented)

- `handleDone` in `pkg/dispatcher/completion.go:9-70` releases the worker and launches the merge after checking only that the payload exists and `QualityGatePassed` is true.
- `releaseWorkerAfterDoneLocked` at `pkg/dispatcher/startup_recovery.go:691-718` does not compare the message's bead to the worker's tracked bead, does not check the worker state, and builds the branch name from the message's bead ID while taking the worktree from the tracked worker.
- The review requirement lives only in the worker binary: `pkg/worker/worker.go:929-970` sends READY_FOR_REVIEW after a QG pass and `worker.go:565-582` sends DONE on approval. Nothing dispatcher-side records that approval happened.
- Connections are bound by the first message's worker ID at `startup_recovery.go:513-518`, and `upsertWorker` at `pkg/dispatcher/worker_pool.go:168-191` replaces the live connection for an existing ID. There is no authentication of worker messages; `handleMessage` at `startup_recovery.go:583-616` checks only bead ID syntax.
- The protocol's `DonePayload` and `ReadyForReviewPayload` at `pkg/protocol/message.go:216-249` carry no assignment ID, generation, or HEAD SHA, so the dispatcher could not bind them even if it tried. Capability tokens are issued at assignment but are consumed only by the work-proposal CLI path.
- The agent subprocess receives `ORO_SOCKET_PATH`, `ORO_WORKER_ID`, and `ORO_WORKER_BEAD_ID` in its environment at `pkg/worker/worker_context.go:96-110`, and Codex workers run with `danger-full-access` at `pkg/agentruntime/codex/codex.go:162`.
- The test suite codifies the review-less path: `TestDispatcher_WorkerDone_MergesClean` at `pkg/dispatcher/dispatcher_test.go:2709-2753` sends DONE directly after ASSIGN and asserts a merge.

Failure scenarios (Hypothesis, unrun): an agent, or an injected instruction it follows, opens the socket and sends a forged DONE, skipping review entirely and leaving only the pre-merge QG. Benign variant: a stale DONE replayed through `BufferedEvents` on reconnect (`pkg/dispatcher/reconnect.go:155-157`) names an old bead, and `git rebase <target> agent/<old>` runs inside the worker's current worktree, switching its checked-out branch mid-task. Impact: the README's "a worker cannot merge without passing both" gates is a cooperative-worker promise, not a mechanism. Change: add assignment ID, generation, and HEAD SHA to READY_FOR_REVIEW and DONE; have the dispatcher persist "approved at HEAD X for assignment N" and require `w.beadID`, `w.assignmentID`, and an approved state in `handleDone`; verify the branch HEAD equals the approved HEAD under the merge lock; authenticate the first message of each connection with the assignment capability or the spawned process identity, and refuse to rebind a live connection.

**Critical 2. The final pre-merge gate executes the candidate's own gate script.** (Implemented)

- `ShellQGRunner.Run` at `pkg/dispatcher/qg_runner.go:58-72` prefers `scripts/quality_gate.sh` from the worktree over the dispatcher-managed root `quality_gate.sh` that `pkg/dispatcher/worktree_manager.go:284-318` installs. The worker-side lookup at `worker.go:1867-1876` uses the opposite order. The README's claim that stale branches cannot bypass gate fixes holds only for the worker's run, not the dispatcher's.
- Guard-tampering scenario (Hypothesis): a task edits the tracked gate script; the dispatcher's gate under the FF lock runs the edited script and passes. Review might catch it, but review is not independent authorization for policy files.
- Related scope gaps are acknowledged in the script itself: `scripts/quality_gate.sh:1299-1304` omits `./cmd/...` tests, and line 1295 drops `-race` on darwin/arm64 or when mutation is skipped. Optional tools produce SKIP lines rather than failures at lines 948 and 1060.

Change: run the managed snapshot or the target branch's version of the script for the final gate, record the script hash on the merged event, and treat diffs touching the gate script, hooks, settings, or allowlists as requiring independent authorization.

**Critical 3. Retry budgets reset on every new assignment, so there is no lifetime bound per objective.** (Implemented)

- Per-bead counters are in-memory maps. On exhaustion, `releaseWorkerAfterQGExhaustion` at `pkg/dispatcher/qg_exhaustion.go:442-463` deletes them, the bead is reopened and deferred for one hour (`dispatcher.go:64`, `qg_exhaustion.go:428-440`), and the next assignment starts at attempt zero.
- `restoreState` at `startup_recovery.go:880-895` restores counts only for still-active assignments. Review rejection and handoff caps at `review.go:298` and `handoff.go:17` behave the same way.
- Impact: the guide's "work terminates or escalates" invariant is not met. The July audit's hundreds of runs for single tasks is consistent with this design, though I did not verify that data.

Change: persist lifetime counters per bead (assignments, QG failures, rejections, handoffs, wall time, spawned children) and refuse admission above a declared budget until an operator reopens. Charge recovery children to the parent's budget.

**Important 4. Review approval is not bound to the reviewed tree, and policy markdown is auto-approved.** (Implemented)

- The durable checkpoint primitive with `head_sha`, `qg_script_hash`, and `acceptance_hash` exists at `pkg/dispatcher/review_checkpoint_store.go:39-58`, but its constructor and `CreateOrReuse` are annotated test-only. The only production caller is artifact pruning in `review_maintenance.go:39-57`. `handleReadyForReview` at `review.go:34-95` spawns the reviewer without creating a checkpoint.
- `Spawner.Review` at `pkg/ops/ops.go:346-362` approves without a reviewer when every changed path is markdown under `docs/`, `assets/`, `.claude/`, or `README.md` (`ops.go:965-975`). Under `assets/` live the skills, rules, and commands that instruct agents.

Change: wire the checkpoint store into ready-for-review, approval, and merge; narrow docs-only to `docs/` and README; re-review after any re-rebase that changes the tree.

**Important 5. Acceptance contracts are shape-checked at best, and the strict validator has no caller.** (Implemented)

- `taskcontract.Validate` has no production caller anywhere in the repository. Production admission is `checkBeadReady` at `pkg/dispatcher/assignment_reconcile.go:525-564`, which requires non-empty acceptance text and a `Test:` marker when `Cmd:` or `Assert:` appear.
- `workproposal.ValidateEvidence` is annotated test-only at `pkg/workproposal/evidence.go:15-48`.
- A `Cmd` that matches zero tests, or a test that asserts nothing, passes admission, the worker gate, and the final gate.

Change: call the validator at publish and admission with a declared mode; before closure, require proof that the acceptance command executed at least one named test.

**Important 6. Epic acceptance runs in the wrong tree and fails open.** (Implemented)

- `closeEpicAfterAcceptance` at `pkg/dispatcher/epic_close.go:120-164` runs the acceptance command first, then the QG on the epic branch, then the fast-forward. `ShellAcceptanceRunner` at `qg_runner.go:29-41` sets no working directory, so the command runs in the dispatcher's cwd on whatever is checked out there, before the epic's commits reach the target.
- On a store lookup error, `fetchEpicCloseDetail` at `epic_close.go:166-183` falls back to a count-based close and merge. No `Cmd` also closes with a warning at lines 127-133.
- Impact: the compound-feature experiment in the guide would likely not be caught. The children's unit tests pass, the parent's composed check never runs against the integrated tree, and the epic closes.

Change: execute the acceptance command inside the epic QG worktree after rebase onto the target; treat store errors and missing commands as escalations, never as closure.

**Important 7. Non-main ref advances lack compare-and-swap.** (Implemented, cross-process risk is Hypothesis)

- `updateTargetRefAndRemove` at `pkg/merge/merge.go:299-314` and `UpdateBranchRef` at `worktree_manager.go:648-658` check ancestry and then call `update-ref` without an expected old value. The in-process FF lock does not protect against `oro work` or a second dispatcher on the same repository. The CAS primitive already exists at `worktree_manager.go:731-735` for preserve merges. Main is safer because `merge --ff-only` fails on movement and retries at `merge.go:255-283`.

Change: resolve the target OID before the ancestry check and pass it as the old value; retry on failure.

**Important 8. The permission boundary is cooperative, and the task store has no authorization.** (Implemented)

- Codex workers get full filesystem access; Claude workers run `-p` with a project settings file at `worker.go:1984-1993`. The worktree-write hook has an environment escape hatch and allowlists `.claude/`. The task CLI's self-close guard at `cmd/oro/cmd_bead.go:291-300` keys on environment variables the agent controls, and it only blocks closing the worker's own bead. A worker can close a peer's in-flight bead, which the closed-bead guard at `merge_flow.go:17-36` then quarantines, aborting that peer's merge.

Change: state the threat model plainly in the README; for unattended use, run workers under a separate OS user or container with access to only the worktree and socket; gate worker-role task mutations on the assignment capability.

**Minor.** Review failure classification is substring-based at `review.go:535-554`, so reviewer output can steer a verdict into "infra blocked" and a free retry. The dashboard's `CostPerHour` is a placeholder at `pkg/dispatcher/dashboard.go:303`, and throughput counts merged events, so no in-product signal measures accepted outcomes. Many test-only annotations mark built-but-unwired primitives, which is a maintenance smell the July audit also noted.

**What works and should remain.**

- The final QG runs on the rebased tree under the FF lock and re-runs after a re-rebase (`merge.go:255-283`), with real tests at `pkg/merge/merge_test.go:1290-1415`.
- Review fails closed when a process error accompanies an approval token (`review.go:361-370`), and verdict parsing demands an exact final line (`ops.go:1064-1081`).
- Terminal worker release checks both bead and assignment identity (`startup_recovery.go:720-743`), and assignment reservations carry generations (`assignment.go:491-550`).
- Startup recovery classifies assignments and quarantines rather than deleting (`startup_recovery.go:880-946`); the stale sweep quarantines orphaned rows; the recovery runbook gives a usable procedure.
- Epic preserve merges use CAS, verify with QG, and roll back on failure (`worktree_manager.go:677-735`, `epic_close.go:240-307`).
- Pre-review hygiene rejects dirty worktrees, pre-merge leak scanning exists, and dependency state is re-checked before approval and retry.

## Plan and open questions

Evidence maturity per the guide's scale, where 0 is demonstrated failure, 1 is implementation and isolated tests, and U is untested:

| Dimension | Score | Basis |
| --- | --- | --- |
| Authority and evidence | 1, with Critical 1 and Important 4 unresolved | No binding of DONE or approval to assignment or HEAD |
| Evaluation quality | 1 | Final gate runs candidate-controlled script; cmd tests omitted |
| Recovery and bounded execution | 1 for recovery, U for bounds | Quarantine design is sound; no lifetime budget |
| Intent and delivered behavior | U | Validator unwired; epic acceptance runs pre-integration |
| Operability | 1 | Runbooks exist; heavy state surface |
| Economics and learning | U | No accepted-outcome measurement in product |

Ordered work, each with a decisive check:

1. **Bind completion and authenticate connections** (Critical 1). Done when a second connection claiming a live worker ID is rejected and logged, DONE for a non-owned bead or an unapproved state is ignored, and a replayed buffered DONE has no effect, all under test and one real-process drill.
2. **Independent final gate** (Critical 2). Done when a candidate that rewrites the gate script to exit zero still fails the dispatcher's gate, and the merged event records the gate script hash.
3. **Lifetime budget** (Critical 3). Done when a bead that exhausts QG retries, defers, and is reassigned is refused at a declared total across a dispatcher restart.
4. **Wire review checkpoints and narrow docs-only** (Important 4). Done when approving commit A and then adding commit B blocks the merge, and a change under `assets/` spawns a reviewer.
5. **Wire the contract validator and move epic acceptance** (Important 5 and 6). Done when an epic whose children pass but whose composed command fails stays open, the command runs in the epic worktree after rebase, and a store error does not close the epic.
6. **CAS on ref advances** (Important 7). Done when an external advance injected between the ancestry check and the ref update is refused and retried with no commit lost.
7. **Close the cmd test gap or consult a remote gate** (Minor and gate matrix). Done when a seeded failing cmd test blocks a merge.
8. **Containment and authorization** (Important 8). Done when the README states the threat model and worker-role task mutations require the capability.
9. **Simplify** by wiring or deleting test-only primitives and replacing the dashboard placeholder with accepted-outcome counts. Defer multi-tenant storage work until a workload needs it.

Uncertainties and the experiments that would resolve them:

- **Live evidence of review-less merges.** Query the event log for merged events with no preceding approval for the same bead and assignment. Zero hits lowers the benign-path risk but not the adversarial one.
- **Codex command rules.** If the Codex approval policy denies non-allowlisted commands even under full access, forging a socket message is harder. The `oro` prefix is allowlisted, which may itself open a path. Test from a worker.
- **Claude worker settings.** I did not inspect the generated settings file. Its tool permissions decide whether a Claude worker can open the socket or run raw git.
- **Stale DONE replay.** A barrier test at reconnect with a buffered DONE for a prior bead would confirm or dismiss the worktree-switch hypothesis.
- **Epic command convention.** If authored commands check out or diff the epic branch themselves, Important 6 is a documentation issue rather than a correctness one. Sample existing epics.
- **CI status and required checks.** The guide and July audit say CI was red while merges proceeded. A current run history would show whether the gate matrix gap is live.

Evidence that would change the recommendation: a dispatcher-side authentication step I missed, a production caller of the review checkpoint store on the approval path, or drill results showing forged and replayed DONE messages are rejected. Absent those, Oro should run with manual integration or merge into an unprotected integration branch that a human promotes.

## Coordinator verification

The shared task-command mutation guard qualifies **Important 8**. [newTaskCmdWithStore](../../cmd/oro/cmd_task.go#L17) installs `guardTaskWorkerMutation` in `PersistentPreRunE`. [workerMutationPolicy](../../cmd/oro/worker_mutation_policy.go#L27) denies direct `task close` and other mutations whenever any recognized worker-identity variable is present, regardless of which task is targeted. The legacy self-close helper cited by the reviewer is not the only guard. Normal peer-task closure through the CLI with worker identity intact is therefore blocked by source-visible wiring. The remaining concern is bypassing environment-based identity or writing shared state directly under the worker’s OS privileges; that scenario still needs a controlled experiment.

For **Critical 2**, changing script lookup precedence is insufficient if the managed copy remains writable by the worker. The trusted validation policy must be outside candidate control, or its immutable identity and execution must be enforced by an independent principal. The Astra audit recommends that stronger boundary.

For **Important 5**, evidence that at least one named test ran is useful where the acceptance command selects tests. Other valid acceptance procedures, such as an executable smoke check, need an equivalent non-vacuous success condition rather than a universal named-test requirement.

Neither audit verified current CI run history. The guide treats historical audits as leads and does not assert that CI is currently red. The reviewer’s CI-history statement should therefore be treated as historical and unverified for the audited snapshot.

The reports agree on the main recommendation: preserve the useful orchestration and recovery controls, repair the completion and integration guarantees, and measure outcomes before expanding autonomy. These qualifications do not establish that any unrun attack or crash scenario has been reproduced.
