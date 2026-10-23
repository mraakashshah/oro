# Oro implementation audit — Codex Astra

Date: 2026-10-23. Reviewer: `gpt-6-astra`, reasoning effort `max`.

Method: use the [staff engineer evaluation guide](../staff-engineer-evaluation-guide.md) as audit instructions to assess Oro’s implementation. The report below is the reviewer’s assessment; source links were made repository-relative for portability.

**Recommendation: permit a supervised pilot with manual integration; do not approve unattended integration at this snapshot.**

Oro contains substantial, useful engineering: deterministic orchestration, post-rebase validation, durable recovery quarantine, work preservation, and explicit failure handling. Those mechanisms justify continuing development. However, the implementation does not consistently enforce its central promise that only current, independently reviewed and validated work can become completed software.

The highest-value change is to consolidate authorization, validation, integration, and completion around one durable candidate record. Adding more agents or process stages before fixing that boundary would increase complexity without establishing trust.

This audit covers source baseline `772f4b94297a97dc6f6249ad5f5bf6976805b080`, with documentation commit `08a19549e154603302f3447262413761e14c34c9`. It excludes the main checkout’s uncommitted changes. “Observed” below means an executed focused test; “source-traced” means a reachable implementation path; inferred consequences have not been reproduced with a live factory.

**Prioritized findings**

**1. Critical — Integration does not require a current, candidate-bound review approval.**

In [handleDone](../../pkg/dispatcher/completion.go#L18), `QualityGatePassed=true` is enough to proceed toward merge. [releaseWorkerAfterDoneLocked](../../pkg/dispatcher/startup_recovery.go#L691) checks that the worker exists, but does not compare the supplied task ID against its assignment, require an approved state, or validate assignment generation. It combines the supplied task’s branch name with the tracked worker’s worktree and assignment.

This is more than an absent negative test. **Observed:** [TestDispatcher_WorkerDone_MergesClean](../../pkg/dispatcher/dispatcher_test.go#L2709) sends `DONE` directly after assignment, without requesting review, and successfully reaches a merged event.

Approval callbacks have a related identity gap. [handleReviewResultForAssignment](../../pkg/dispatcher/review.go#L330) receives an assignment ID but drops it on the approved branch. The subsequent guard checks task/worker/state, rather than the originating assignment. By comparison, [blocked-review handling](../../pkg/dispatcher/review.go#L691) correctly checks the expected assignment ID.

Candidate provenance is also incomplete. Completion and review messages lack the assignment generation and candidate SHA; the production approval path does not persist and consume an approval bound to the final tree. The richer [review checkpoint store](../../pkg/dispatcher/review_checkpoint_store.go#L78) has identity fields, but its creation and findings-writing methods have no production callers in this snapshot.

**Consequence:** review is partly a cooperative protocol. A premature completion can skip it, and an old result can potentially affect a replacement attempt. A later rebase can also produce an integrated candidate different from the reviewed one.

**Important qualification:** this does **not** demonstrate a final quality-gate bypass. [mergeAndComplete](../../pkg/dispatcher/merge_flow.go#L45) supplies a real post-rebase gate.

**Change:** make integration consume a durable authorization record containing task, assignment/generation, candidate SHA, target/base SHA, acceptance hash, policy hash, and approval. Validate every terminal message and asynchronous callback against it. Reject early, duplicate, wrong-task, stale-generation, or changed-candidate completion. Reuse the existing identity/checkpoint work through one production admission path.

**2. Critical — Epic completion can turn missing evidence or infrastructure errors into success.**

The epic path explicitly fails open:

- [closeEpicAfterAcceptance](../../pkg/dispatcher/epic_close.go#L127) integrates and closes when no executable acceptance command exists, bypassing the normal epic QG branch.
- [fetchEpicCloseDetail](../../pkg/dispatcher/epic_close.go#L166) falls back to completion when acceptance-detail lookup fails or returns nil.
- [ffMergeEpicBranch](../../pkg/dispatcher/dispatcher.go#L862) treats a Git branch-existence error as absence and permits closure.
- [completeEpicClose](../../pkg/dispatcher/epic_close.go#L433) ignores the close error and then emits `epic_auto_closed`. Standalone [oro work](../../cmd/oro/cmd_work.go#L552) similarly ignores `Close` failure while printing that the task closed.

**Observed:** `TestEpicAutoCloseRunsAcceptanceTest` passes its explicit “without Cmd” count-based closure case. The fallback is established behavior.

Even the normal acceptance path is not bound to its candidate. [ShellAcceptanceRunner.Run](../../pkg/dispatcher/qg_runner.go#L29) receives only a command string and sets no working directory. Acceptance executes before the epic branch advances the target. Ordinary relative commands therefore run in the daemon’s inherited directory, unless the command itself arranges otherwise. The separate epic QG does create a candidate worktree, but that does not repair the acceptance oracle’s missing identity.

**Consequence:** “epic complete” does not reliably mean that the composed feature was tested, integrated, or durably closed. A transient read failure can weaken the completion standard.

**Change:** keep missing or unreadable acceptance in a distinct blocked/unverified state. Run acceptance and required gates on an immutable candidate worktree; record its identity and rerun relevant acceptance after integration changes. Treat closure as a durable, retryable finalization step. Never emit completion until that transition succeeds.

**3. Critical for concurrent integration — Non-main ref updates can overwrite an intervening target advance.**

[updateTargetRefAndRemove](../../pkg/merge/merge.go#L299) checks ancestry and subsequently executes `git update-ref` without an expected old SHA. [GitWorktreeManager.UpdateBranchRef](../../pkg/dispatcher/worktree_manager.go#L648) repeats this pattern.

**Source-traced:** these paths advance epic and other non-main targets. The merge coordinator’s locks serialize its own callers; they do not exclude another process or all other ref-writing paths.

**Inferred consequence:** if another writer advances the target after the ancestry check, the unchecked update can replace that newer tip. I did not reproduce an overwrite against real Git during this audit.

The project already contains the right primitive: [preserveEpicAncestry](../../pkg/dispatcher/worktree_manager.go#L730) uses compare-and-swap with captured immutable object IDs.

**Change:** use captured source and target OIDs throughout validation, then update the ref with the expected old target OID. On contention, preserve work and recompute the candidate and applicable checks. Consolidate ref advancement so ordinary merges, epic closure, and recovery use the same rule.

**4. Important — The candidate can influence its own enforcement policy, and worker permissions do not establish an independent boundary.**

The [documentation-only classifier](../../pkg/ops/ops.go#L965) includes Markdown under `assets/` and `.claude/`. [Review](../../pkg/ops/ops.go#L345) can approve these changes without spawning a reviewer. In Oro, these locations contain executable agent instructions and policy.

**Observed:** `TestReviewDocsOnlyDiffShortCircuits` confirms that this route approves without a reviewer.

Meanwhile, the final [ShellQGRunner](../../pkg/dispatcher/qg_runner.go#L58) executes the candidate worktree’s gate script. There is no independent authorization check in this path for changes that lower the required standard.

The [Codex adapter](../../pkg/agentruntime/codex/codex.go#L161) grants `danger-full-access`. The [task mutation guard](../../cmd/oro/worker_mutation_policy.go#L56) is useful against accidental CLI misuse, but recognizes worker context through removable environment variables. It is not filesystem, database, or Git-ref isolation.

**Consequence:** the safeguards depend on cooperative agents and intact policy. Worktrees alone do not prevent a mistaken or manipulated worker from changing shared state or its checker.

**Change:** classify policy-bearing paths separately from prose and require independent approval. Execute a pinned validation policy outside candidate control. For an unattended threat model, separate worker permissions from the principal that owns task state, integration refs, and credentials. A supervised pilot can use an externally isolated runtime while this boundary is developed.

**5. Important — A green local gate omits CLI behavior, and the remote-gate machinery does not yet close that gap.**

The active [local Go test lane](../../scripts/quality_gate.sh#L1290) runs `./internal/... ./pkg/...`, excluding `cmd`. It also disables race checking when `ORO_SKIP_MUTATION=1`; dispatcher mutation testing defaults off, so this coupling affects ordinary local runs beyond darwin/arm64.

[CI](../../.github/workflows/ci.yml#L102) includes CLI and race tests. However, the inspected merge path consults the local QG, rather than requiring those CI results.

Remote configuration and [startup preflight](../../cmd/oro/remote_capabilities.go#L264) are real. They should not be mistaken for operational candidate gating: the remote store’s [AdoptCandidate](../../pkg/dispatcher/store.go#L160) and dispatcher transition wrapper have no production callers in this snapshot. Similarly, review/evidence scaffolding does not substitute for an integration admission check.

**Consequence:** important command behavior can regress while the enforced gate remains green. Configuring a promising remote-gate surface is insufficient evidence that candidate-specific remote checks block integration.

**Change:** define one required-check manifest per supported mode. Make nested gate tests safe or give them an explicit independent lane; include CLI tests before integration. Separate race and mutation policy. Until the remote lifecycle is connected and exercised end to end, reject or clearly disable its unsupported operating mode rather than allowing it to imply stronger enforcement.

**6. Important — Retry limits bound episodes, not the original objective’s resource consumption.**

[handleClassifiedQGExhaustion](../../pkg/dispatcher/qg_exhaustion.go#L377) completes the assignment, clears its retry tracking, reopens the original task, and defers it. The [cooldown](../../pkg/dispatcher/dispatcher.go#L62) is one hour. [releaseWorkerAfterQGExhaustion](../../pkg/dispatcher/qg_exhaustion.go#L442) deletes the exhaustion and attempt counters.

**Observed:** `TestQGExhaustion_ReopensOriginalForDeterministicFailure` verifies reopening, clearing exhaustion, and scheduling a future retry. Separately, [handoff exhaustion](../../pkg/dispatcher/handoff.go#L146) creates a continuation task.

**Consequence:** cooldowns and per-assignment caps prevent rapid loops but do not establish a total limit across attempts, restarts, continuations, and recovery children. An unsatisfiable objective can consume repeated budgets.

**Change:** persist an objective-level budget and lineage. Charge implementation, review, diagnosis, continuation, and recovery to it. Exhaustion should require an explicit extension or changed acceptance contract; creating a new assignment or child must not reset the limit.

**What works and should stay**

The post-rebase gate is a meaningful safeguard. Its tests verify execution before fast-forward, repetition after another rebase, and execution before non-main advancement. Dispatcher tests also verify failure blocks integration.

Review parsing has useful discipline: exact final verdict syntax is required, and a nonzero reviewer exit with `APPROVED` fails. I would preserve this instead of attributing every review problem to the parser.

Recovery is one of the stronger areas. [Recovery quarantine](../../pkg/dispatcher/recovery_quarantine.go#L58) persists assignment quarantine and recovery records transactionally. Startup classifies inconsistent state rather than blindly resetting it. Focused tests passed for quarantine on inconsistent recovery, preservation after failed pre-merge QG, fail-closed quarantine lookup, and respecting human-owned claims. Target-specific branch-deletion proof is also preferable to indiscriminate cleanup.

The deterministic epic preserve-merge path captures immutable OIDs, uses a real merge tree, checks content, and advances with compare-and-swap. That is a useful pattern to generalize. Manual integration is implemented and its focused test confirms preservation of the worker branch/worktree for human action.

These are substantive controls, not merely prompts describing desired behavior.

**Product outcomes, learning, economics, and complexity**

The audit establishes useful mechanisms and significant correctness gaps. It does not establish that Oro produces better software or saves engineering time.

The [dashboard](../../pkg/dispatcher/dashboard.go#L288) measures merged events and leaves cost per hour as a placeholder. Those are operational signals, not accepted-outcome economics. A comparison must include specification, decomposition, supervision, rescue, review, failed attempts, and defects discovered afterward.

Knowledge reuse is wired into assignment context with a token budget. Its reliability needs separate validation. [Learning promotion](../../pkg/dispatcher/promotion.go#L111) derives a passing signal from a merge close reason; [card promotion](../../pkg/cards/promotion.go#L104) can promote rules and patterns from that signal. Merge success does not independently establish that the accompanying lesson is true. Stored grading verdicts likewise do not by themselves demonstrate calibration against external truth.

The complexity is **partly justified**: ownership, recovery, concurrency, and preserving interrupted work are difficult problems worth explicit machinery. The current cost comes from uneven enforcement across standalone work, worker completion, epic closure, recovery, and partially connected newer infrastructure. Some production machinery is maintained around concepts that are not yet used to authorize completion.

I would simplify by consolidating these paths around shared invariants, not by deleting recovery controls or replacing them with more prompt instructions.

**Implementation order**

1. **Fix:** reject premature/stale completion, require candidate-bound approval, fail closed on epic acceptance and finalization errors, and use compare-and-swap for every target-ref advance.
2. **Consolidate:** make standalone, swarm, epic, and recovery integration consume the same durable authorization and finalization logic.
3. **Harden:** pin enforcement policy, require independent review for policy changes, and complete the required-check matrix.
4. **Bound:** add persistent objective-level resource budgets and reliable stop conditions.
5. **Measure before expanding:** run representative comparative workloads and held-out acceptance checks. Defer additional autonomous maintenance, grading complexity, and concurrency expansion until their outcome benefit is demonstrated.

**Verification scope and remaining uncertainty**

I executed **17 selected top-level tests, plus their subtests**, across `pkg/merge` (3), `pkg/dispatcher` (11), and `pkg/ops` (3). All passed with `-v -count=1 -timeout=120s`, using Go `1.27.3` on darwin/arm64. Network module access was disabled and inherited Oro/Git identity variables were removed. No race run or full gate was performed.

The selection covered post-rebase gating, direct `DONE` completion, stale blocked-review rejection, reviewer process-error rejection, manual integration, docs-only approval, verdict parsing, epic acceptance/fallback, deterministic retry exhaustion, recovery quarantine, ownership on restart, and preservation of rejected work. Several are mocked lifecycle tests; passing them does not prove real-process safety.

I did not run live-model workloads, concurrent external ref mutation, crash-at-boundary drills, storage fault injection, remote GitHub gating, permission escape probes, upgrades/restoration, or economic comparisons. Those remain necessary before expanding autonomy. The source findings above are sufficient to withhold automatic-integration approval without pretending these unrun experiments already failed.

HEAD remained `08a19549…`; tracked working-tree and index diffs were empty at the end of the audit.
