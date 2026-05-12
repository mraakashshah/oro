# Monitoring Report — Oro Software Factory

Started: 2026-05-05

## Cycle 1 — Launch

- Built via `make build` + `make install`
- Cleaned worktrees (none existed)
- Started dispatcher at PID 32775 with 2 workers via `/Users/as21/go/bin/oro start --workers 2 --detach`
- Status: running, 0 active workers (spinning up), queue=21 ready
- Note: `./oro start` from repo dir is blocked by the re-exec guard — using `$GOPATH/bin/oro` instead. Expected for dev installs (binary protection).

## Cycle 2 — First merge (~26min after launch)

- **Merged**: oro-uak3 → 7749b1bf `feat(beadstore): add BackfillJourneyEvents for §4.6.f COALESCE fix`
- Worker-0 immediately picked up oro-gec8 (vB-3 call-graph walker)
- Worker-1 still on oro-mzdl (V3Methods)
- Queue 18 ready, no QG/crash events
- Decision: not rebuilding — oro-uak3 added a new beadstore method; dispatcher/worker runtime unaffected for now.

## Cycle 3 — mzdl merge with conflict-resolution path

- mzdl finished, review_approved (ops verified pkg/beadstore + protocol + dispatcher all green with -race)
- Initial merge attempt → MERGE_CONFLICT on `pkg/protocol/schema.go` (mzdl's gocyclo refactor of runBeadStatusRebuildTx vs main's pyr2 sql.Tx wrap of same fn)
- Ops agent auto-rebased mzdl branch onto current main (squashed 2 commits → be077e61), `merge_conflict_resolved` at 05:48:54
- Retry merge succeeded → bead_closed at 05:50:56 (be077e61)
- Worker-1 had already moved to oro-wcdn during the conflict-resolution window
- **No defect** — handleMergeConflictResult retries merge after VerdictResolved, working as designed

## Cycle 4 — QG_FAILED pattern, no rebuild

- gec8 review_rejected → retry succeeded → review_approved → qg_failed (2 attempts) → reset to open
- wcdn review_rejected → retry succeeded → review_approved → qg_failed (2 attempts) → reset to open
- Both QG outputs show `go test + coverage ✗ FAIL`; output truncates after `oro/pkg/agentruntime/codex` with `-test.shuffle <seed>` line — actual failing test not captured
- Reproduced locally on main: TestTaskTerminologyGuard fails because of dirty `assets/review-patterns.md:86` ("per-bead bookkeeping") — but that file is clean in workers' worktrees, so workers' failure is a different test (likely flaky/shuffle-order)
- **Filed**: oro-q9wa (P1 bug) — capture full go test output on QG failure
- Decision: continuing observation, not rebuilding. Factory regrouping with 4p6c + a7wd. Per skill rules, single QG_FAILED on a bead is normal retry; loss of two beads is unfortunate but not a stop trigger.

## Cycle 5 — Stop, fix, relaunch

User asked me to stop and fix the defect. Two runtime bugs landed via work-bead skill (TDD, worktree, ff-merge):

- **oro-q9wa** → `ecc585b7` `fix(qg): emit full cmd output on parallel_checks failure`. Replaced `head -20` with `cat` in the FAIL branch of `scripts/quality_gate.sh::parallel_checks`. Workers and dispatcher events will now see the actual `--- FAIL: TestX` line in QG output instead of just the first 20 lines (macOS linker warnings).
- **oro-2p2m** → `5e375360` `fix(preflight): hard-fail oro start if oro-search-hook is missing and unbuildable`. `ensureSearchHook` was fail-open; now hard-fails when both binary and source are missing, and surfaces an actionable "run make install" error from `oro start`. Soft-fail preserved when an existing binary tolerates a transient rebuild failure or absent srcDir.

Rebuilt via `make build && make install`, verified search hook at `/Users/as21/.oro/hooks/oro-search-hook` (9.6MB, 02:57:27). Relaunched at PID 73656 with 2 workers and 23 ready in queue.

## Cycle 20 — Defect: external_close strands worktree (oro-ohlro)

- **Observed**: bead_closed_externally on oro-ohlro at 12:37:19. Worker called `oro task close` directly (skill-leak from work-bead) instead of letting the dispatcher merge. Dispatcher emitted `external_close_cancelled`, sent SHUTDOWN. Commit `099cc7a6` (§18.3 checkpoint E2E test + `oro harness verify-checkpoint` CLI) was dangling — branch GC'd with worktree, never merged to main.
- **Filed**: oro-0xqv (P0 bug) — dispatcher should either reject external close on in-progress beads or recover the worktree branch via ff-merge if QG passes.
- **Recovered manually**: cherry-picked 099cc7a6 → `4aa8e992` in a fresh worktree; conflict on `cmd/oro/root.go` was additive (kept both `newTestContextSafetyCmd` and `newHarnessCmd`); tests `TestVerifyCheckpointCLIWraps` + `TestCheckpointE2EFromHighContext` both pass; pushed.
- Decision: not stopping for rebuild. The recovered code is tests + CLI wrapper, no running-dispatcher behavior change. Workers are productive on cz8mv + 1cem.

## Cycle 21 — Stop, fix oro-0xqv, relaunch

User asked me to stop and fix the runtime bug.

- **oro-0xqv** → `eac752fd` `fix(dispatcher): recover or escalate worker work on external_close`. `finalizeExternalClose` now calls `merger.Merge` on the agent branch before cleanup. Success → `external_close_recovered` event with SHA. Conflict / missing branch / error → `external_close_recovery_failed` + `EscMergeConflict` escalation with worktree path so the manager can recover manually. Cleanup still runs in both branches; the merger removes the worktree on its success path so `removeWorktreeAndClearTracking` becomes a no-op.
- TDD: `TestExternalCloseRecoversWorktree` (mockGitRunner returns fake SHA → recovery event), `TestExternalCloseEscalatesOnMergeConflict` (mockGitRunner.conflict=true → recovery_failed + escalation). Existing `TestExternalCloseDoesNotMergeWorkerBranch` and 4 sibling external-close tests all still pass.
- Rebuilt + reinstalled. Search hook present (9.6MB, 09:09:05). Relaunched at PID 53679 with 2 workers, queue=20.

## Cycle 7 — Post-fix throughput

- **Merged**: oro-4p6c → d750d6ef `test(beadstore): add §4.5 journey hot-path bench gate`
- Worker-0 still on oro-a7wd (retry of last-cycle fail), worker-1 picked up oro-gec8 (retry of last-cycle fail)
- Both at 9-11% ctx, healthy
- Queue 20. No QG_FAILED, no crashes. Workers are now retrying the previously-bumped beads against the freshly built binary with the QG fix in place.

## Cycle 23 — CI fix #2: hermetic daemon-skip preflight (oro-7jjt)

- CI run 25378988003 from oro-5879 failed: `TestStartPreflightAndCheckRunning_DaemonOnlyBypass` blew up with `build oro-search-hook failed and no existing binary at /home/runner/.oro/hooks/oro-search-hook: exec: "go": executable file not found in $PATH`. The oro-2p2m hard-fail was right; the test's PATH override (claude+git only) hits the same code path in CI.
- Local repro initially passed because `~/.oro/hooks/oro-search-hook` already exists in dev — the "preserved binary" warning silently swallowed the build failure. Fixed by setting `ORO_HOME=tmpDir/orohome` in the test, which reproduced the CI failure 1:1.
- **oro-7jjt** → `fff9316c` `fix(cmd): skip repo preflight in hermetic daemon-skip mode`. Added `runRepoChecks bool` parameter to `preflightAndCheckRunningWith`; daemon-skip path passes `false` (hermetic mode = no Go toolchain, no source on disk, building a search hook is impossible). Full path passes `true`.
- Rebuilt + reinstalled, search hook present (9.6MB, 09:35:00). Dispatcher PID 85593 still running cleanly — no relaunch needed since the bug was tests-only in cmd/oro.
- CI run 25379616028 watching via monitor `bnhagetxp`.

## Cycle 24 — Stop, fix focused epic descendant scheduling (oro-bck8)

- **Observed during relaunch**: after `oro directive focus oro-gpex`, dispatcher assigned unrelated ready tasks (`oro-jv74`, `oro-fro8`, `oro-9nfh`) while focused epic descendant `oro-q53e` was ready under `oro-sgge -> oro-gpex`.
- **Filed**: `oro-bck8` P0 bug, "Focused epic scheduling ignores nested descendants".
- **Root cause**: scheduler focus grouping only checked `bead.Epic == focusedEpic`. `Epic` is the immediate parent, so grandchildren of the focused epic were sorted as unfocused work.
- **Fix**: `e348338e` `fix(dispatcher): include nested descendants in epic focus (oro-bck8)`. `sortBeadsByPriority` now walks parent chains via `Show` with cycle protection and treats all descendants of the focused epic as focused.
- **Verified**: RED regression `TestSortBeadsByPriority_EpicFinishing/focused_epic_includes_nested_descendants`; `go test ./pkg/dispatcher -count=1`; `./scripts/quality_gate.sh`.
- Rebuilt + reinstalled, search hook present (9.6MB, 09:34). Relaunching with `--workers 3 --max-workers 3` and focus `oro-gpex`.

## Cycle 25 — Stop, fix immediate-focus preemption races

- **Clarified semantics**: standard `focus` keeps priority/backfill behavior; `focus --immediate` preempts current non-focused workers, then still uses normal focus priority with backfill.
- **Invalidated**: `oro-sfpa` was closed without code after the strict no-backfill interpretation was rejected.
- **Fixed**:
  - `58ea6e3b` `fix(dispatcher): abort stale assignments on focus change (oro-771g)` guards assignments when focus changes during worktree setup.
  - `896dea8c` `fix(dispatcher): abort post-persist assignments on focus change (oro-rkhr)` covers the post-persist/pre-send window.
  - `b5cd0e97` `fix(dispatcher): guard focus at worker state commit (oro-gj1z)` checks `focusVersion` under `d.mu` immediately before committing worker state.
- **Verified**: focused dispatcher regressions plus full `./scripts/quality_gate.sh` passed for each fix. Rebuilt + reinstalled, hook present at `/Users/as21/.oro/hooks/oro-search-hook`.
- **Relaunch result**: dispatcher PID 24303, target=3, managed=3, focus=`oro-gpex`. `focus --immediate oro-gpex` preempted 3 non-focused workers; `oro-q53e` is active as focused work and remaining capacity is backfilled (`oro-fro8`, `oro-jv74`), as intended.

## Cycle 26 — Stop, fix v4 FTS trigger stall, relaunch

- **Launch attempt**: `/Users/as21/go/bin/oro start --workers 2 --max-workers 2 --detach` after `make build`/`make install`.
- **Observed stall**: dispatcher started but had 0 registered workers and 43 ready tasks. Logs repeated `update_status_failed` on ready tasks with `SQL logic error: table beads_fts has no column named status`.
- **Root cause**: v4 migration installed legacy `beads_ai/beads_ad/beads_au` triggers that wrote `status/type/parent_id/owner` into `beads_fts`, while the canonical FTS table only contains `title/description/acceptance_criteria`.
- **Filed**: `oro-db6n` P1 bug, "Bug: v4 migration installs bad beads_fts triggers".
- **Fixed**: `bd56b386` `fix(beadstore): repair v4 fts triggers`. Future v4 migrations now install canonical `beads_fts_*` triggers only; already-migrated `user_version=4` DBs are repaired through normal DB open/startup; repair skips the FTS rebuild when trigger state is already healthy.
- **Verified**: focused regression tests passed, full `./scripts/quality_gate.sh` passed, and `claude -p` ops review returned PASS with no Critical/Important blockers.
- **DB repair confirmed**: live state DB now has only `beads_fts_ai`, `beads_fts_ad`, `beads_fts_au`.
- **Relaunch result**: dispatcher PID 36168, target=2, max=2, managed=2. Workers active on `oro-aers` and `oro-f4pq`; queue=41. Fresh logs show `assign` and `bead_updated` to `in_progress`; no new trigger errors after 20:05.
- **Follow-up filed**: `oro-nft6` P1 for a recovered `goroutine_panic` in `retryOversizedBead` at 20:07:19. The panic did not repeat in the next monitoring check and the swarm remained healthy, so the run was not stopped for this separate defect.

## Cycle 27 — Stop, fix duplicate worker-process leak

- **Observed during resumed monitoring**: `oro status` reported PID 60674 with 2 managed active workers (`oro-c761`, `oro-e5ts`) and 43 ready tasks, but `ps` showed 6 `oro worker` OS processes under the dispatcher. Three processes shared worker ID `worker-1778279429892676000-0`; two stale workers were still heartbeating with no bead. Logs also showed stale-base workers filing duplicate govulncheck bugs after the fix had already merged.
- **Action**: stopped dispatcher with `ORO_HUMAN_CONFIRMED=1 oro stop --force`, then killed leftover worker processes for the project socket. Verified `oro status` reported stopped and no matching worker processes remained.
- **Filed**: `oro-xmzh` P0 bug, "restart-worker leaves duplicate worker OS processes alive".
- **Root cause**: `applyRestartWorker` closed the worker socket and spawned the same worker ID without first calling `procMgr.Kill(workerID)`. `ExecProcessManager.Spawn` then overwrote `procs[id]`, so older same-ID processes were no longer reachable by process-manager kills.
- **Fix**: `applyRestartWorker` now kills old managed worker processes before respawning the same ID; `ExecProcessManager.Spawn` now terminates any previously tracked process for a duplicate ID after successfully starting the replacement.
- **Verification**: RED regressions failed as expected, then passed after the fix:
  - `TestApplyRestartWorker_KillsManagedProcessBeforeSameIDRespawn`
  - `TestExecProcessManager_Spawn_DuplicateIDKillsExistingProcess`
- Full package verification: `go test ./pkg/dispatcher -count=1 -timeout 180s` passed.
- Quality gate: `./scripts/quality_gate.sh` passed after applying `shfmt` to `scripts/check_no_claude_workers_phrasing.sh`.
- Ops review: `claude -p` first pass returned PASS with two Important tighten-ups; second pass returned PASS after fixing both.
- **Committed**: `b1910b85` `fix(dispatcher): kill duplicate worker processes`.
- **Relaunch result**: started via `/Users/as21/go/bin/oro start --workers 2 --max-workers 2 --detach`, dispatcher PID 22512. Startup quarantined interrupted assignments for `oro-e5ts` and `oro-c761` due to missing worktree paths, reopened them, and then reassigned both.
- **Post-relaunch monitoring**: two consecutive clean cadence checks show 2 active workers, 0 idle, 0 pending, queue=43, exactly two worker OS processes, active tasks `oro-e5ts` and `oro-c761`, and no fresh panic or duplicate-worker recurrence.

## Cycle 28 — Stop, fix merge recovery regressions, relaunch

- **Observed during monitoring**: `oro-8dtm` recovered from a stuck-worker escalation, but later external-close recovery / merge-conflict handling produced killed ops agents. `oro-r1e0`, `oro-krng`, and `oro-zdqd` reached review approval but their merge path failed and left beads reopened or needing operator attention. No fresh panic signal appeared; the recurring failure was merge recovery, not worker health.
- **Filed**: `oro-gyvy` P0, "merge-conflict recovery times out and strands approved tasks". Fixed dispatcher non-conflict merge failure handling so assignments complete, beads reopen, escalation/logging happens, and worktree/branch/tracking are preserved for recovery instead of deleting the only context.
- **Verified**: `go test ./pkg/dispatcher -run TestMergeAndComplete_NonConflictMergeFailurePreservesRecoveryContext -count=1 -v`; `go test ./pkg/dispatcher -run 'TestMergeAndComplete|TestMergeConflict|TestExternalClose' -count=1`; `go test ./pkg/... -count=1`; `make build`; `make install`.
- **Follow-up observed**: `oro-krng` exposed a second merge bug: clean approved branches targeting an epic branch failed `ff-only merge ... after rebase retry` because the coordinator tried `git merge --ff-only <agent>` in the primary repo instead of advancing the non-checked-out target ref.
- **Filed**: `oro-hnec` P0, "approved branches reopen after ff-only merge failure despite clean rebased branch". Fixed `pkg/merge/merge.go` so non-main targets verify ancestry with `merge-base --is-ancestor <target> <branch>`, then advance `refs/heads/<target>` with `update-ref` and remove the worktree. Main-target behavior still uses the checked-out ff-only merge/retry path.
- **Verified**: `go test ./pkg/merge -count=1`; `go test ./pkg/dispatcher -run 'TestMergeAndComplete|TestMergeConflict|TestExternalClose|TestEpicMerge|TestMergeFFOnly' -count=1`; `go test ./pkg/... -count=1`; `make build && make install`.
- **Relaunch result**: stopped with `ORO_HUMAN_CONFIRMED=1 /Users/as21/go/bin/oro stop --force`, rebuilt/installed, restarted with `/Users/as21/go/bin/oro start --workers 2 --detach`. New dispatcher PID 37303. Startup quarantined stale assignments 3798 (`oro-zdqd`) and 3797 (`oro-zc58`) for missing worktree paths, then assigned `oro-hnec` and `oro-zc58`.
- **Post-relaunch monitoring**: two clean 60s windows after 13:32:31 EDT show PID 37303 running, 2 active workers, queue=31, fresh heartbeats, and no new `panic`, `merge_failed`, `MERGE_CONFLICT`, `qg_failed`, stuck-worker, or ops-escalation timeout events. Stable monitoring continues while `oro-hnec` and `oro-zc58` run.

## Cycle 29 — Stop, fix stale active assignment reuse, relaunch

- **Observed during monitoring**: `oro-zc58` and `oro-hnec` reached review approval, then the same workers were assigned fresh beads (`oro-acqj`, `oro-zdqd`) while the approved assignments remained active. SQLite showed two active assignments per worker and no corresponding `merge_failed` event.
- **Filed**: `oro-k96v` P0, "approved review can leave stale active assignment while worker receives new bead".
- **Root cause**: `handleDone` moved workers directly to `idle` and cleared assignment fields before async merge completion. The assign loop could reuse the worker while the old assignment still needed terminal merge/cleanup, and later recovery could not complete the stale row because the worker no longer carried the old assignment metadata.
- **Fix**: workers now move to `reserved` after DONE and keep bead/assignment metadata until a terminal merge/QG/manual-integration path calls `releaseWorkerAfterDoneTerminal`. Successful merge releases the worker immediately after close + assignment completion, before slower cleanup/memory hooks.
- **Regression**: `TestReleaseWorkerAfterDoneReservesUntilTerminalCleanup` covers the DONE-to-terminal lifecycle and active-assignment invariant.
- **Verification**: focused dispatcher lifecycle tests passed; `go test ./pkg/dispatcher -count=1` passed; `go test ./pkg/... -count=1` passed; `make build && make install` passed.
- **State cleanup before restart**: backed up `~/.oro/projects/oro/state.db`, completed stale active assignments 3799-3802, and left tasks open/closed according to current fix state. Closed `oro-k96v` and re-closed `oro-hnec` with fix notes.
- **Relaunch result**: `/Users/as21/go/bin/oro start --workers 2 --detach`, dispatcher PID 38478, target/max 2.
- **Post-relaunch monitoring**: two clean 60s windows show PID 38478 running, 2 busy workers (`oro-acqj`, `oro-zc58`), queue=31, exactly one active assignment per worker, and no fresh `panic`, `merge_failed`, `merge_conflict`, or `assignment_invariant` event.

## Cycle 30 — Stop, fix no-verdict review stall, relaunch

- **Observed during monitoring**: after `oro-acqj` reached `ready_for_review`, the review subprocess exited/disappeared without a parseable `VERDICT`, but the dispatcher left worker `worker-1778437384623020000-0` in `reviewing` with no live review path. This matched the user's monitoring concern: no fresh panic, but a worker-health failure from a review lifecycle hole.
- **Filed**: `oro-khcv` P0, "review subprocess exit without verdict leaves worker stuck reviewing".
- **Root cause**: failed/no-verdict review results logged `review_failed` and escalated, but a clean failed result with no subprocess error did not actively transition the reviewing worker out of `WorkerReviewing`.
- **Fix**: `handleReviewResult` now treats failed/no-verdict clean review completions as review rejections and reassigns the bead with feedback. Subprocess errors still use the existing dead-review cleanup/reopen path so ops process failures do not get converted into ordinary review feedback.
- **Regression**: `TestHandleReviewResult_FailedVerdictReassignsReviewingWorker` asserts a no-verdict failure emits `review_failed`, emits `review_rejected`, records escalation, and leaves the worker out of `WorkerReviewing`.
- **Verification**: focused dispatcher regression passed; `go test ./pkg/ops -count=1` passed; `go test ./pkg/dispatcher -count=1` passed; `go test ./pkg/... -count=1` passed; `make build && make install` passed.
- **State cleanup before restart**: stopped with `ORO_HUMAN_CONFIRMED=1 /Users/as21/go/bin/oro stop --force`, backed up `~/.oro/projects/oro/state.db`, completed stale active assignments 3805 (`oro-zdqd`) and 3806 (`oro-acqj`), and closed `oro-khcv`.
- **Relaunch result**: `/Users/as21/go/bin/oro start --workers 2 --detach`, dispatcher PID 53456, target/max 2. Startup warned that pane-died hook registration failed, so the relaunch was monitored on the tighter error cadence.
- **Post-relaunch monitoring**: two clean 60s windows show PID 53456 running, 2 busy workers (`oro-acqj`, `oro-zdqd`), queue=30, exactly one active assignment per worker, fresh heartbeats under 1s, and no fresh `panic`, `timeout`, `merge_conflict`, `review_failed`, `review_rejected`, stuck-worker, duplicate-assignment, or oneshot-escalation event.
- **Follow-up monitoring**: both active beads later entered review. `oro-acqj` produced `review_approved` and `done`, then hit a `merge_conflict` in `pkg/dispatcher/dispatcher.go`; the resolver completed successfully with `merge_conflict_resolved` at 20:45:57, avoiding the previous 5m timeout path. Its post-conflict QG then failed on pytest and was classified `transient/backoff_retry`, so assignment 3807 completed and the bead was reassigned as 3810. `oro-zdqd` produced `review_approved`, `done`, and `merged` at 20:47:02; assignment 3808 completed and the worker was reassigned to `oro-19j6`. Elevated monitoring after the retry showed one active assignment per worker and live child agents for both `oro-acqj` and `oro-19j6`.

## Cycle 31 — Stop, fix clean merge-resolver output misclassified as failure

- **Observed during monitoring**: `oro-acqj` reached review approval again and entered merge-conflict recovery at 21:10:20. The resolver output reported a clean rebase / clean working tree, but the dispatcher still emitted `merge_conflict_failed` at 21:13:21, completed assignment 3810, and retried the task as assignment 3811. This was a P0 because successful recovery output was being treated as failure.
- **Filed**: `oro-14zr` P0, "successful merge-conflict resolver output is treated as failure and retries task".
- **Root cause**: `ops.parseResult` returned `VerdictFailed` immediately on any subprocess wait error before parsing merge stdout, and `parseMergeOutput` only accepted explicit `RESOLVED`. The live resolver produced success phrases such as `Rebase completed cleanly` and `Working tree clean` without the literal sentinel.
- **Fix**: merge ops now parse stdout before honoring a non-zero wait status, and clean rebase / clean working-tree summaries are accepted as `VerdictResolved` with the resolver output preserved as feedback.
- **Regression**: added coverage for clean rebase summaries and for non-zero merge subprocess exit with clean resolver output resolving successfully. Non-merge non-zero exits still fail.
- **Verification**: focused ops/dispatcher merge-conflict tests passed; `go test ./pkg/ops -count=1` passed; `go test ./pkg/dispatcher -count=1` passed. Two full `go test ./pkg/... -count=1` runs hit only `TestPriorityContention_StableUnderLoad` timing out under full-suite load; focused reruns of that same test passed twice. `make build && make install` passed.
- **State cleanup before restart**: stopped with `ORO_HUMAN_CONFIRMED=1 /Users/as21/go/bin/oro stop --force`, backed up `~/.oro/projects/oro/state.db` to `state.db.bak-20260510-172140`, completed stale stopped-run assignments 3809 (`oro-19j6`) and 3811 (`oro-acqj`), and closed `oro-14zr`.
- **Relaunch result**: `/Users/as21/go/bin/oro start --workers 2 --detach`, dispatcher PID 41911, target/max 2. Startup warned that pane-died hook registration failed, so monitoring stays on the tighter error cadence until the merge-recovery path stays clean.
- **Initial post-relaunch monitoring**: first checks show PID 41911 running with two managed worker processes, active assignments 3812 (`oro-acqj`) and 3813 (`oro-19j6`), no duplicate active assignments, and no fresh `panic`, `timeout`, `merge`, `review`, or duplicate-assignment event since relaunch.

## Cycle 32 — Stop, fix ops close-unmerged-recovery guardrail

- **Observed during monitoring**: at 21:32 UTC, `oro-acqj` failed QG in its worktree, then a one-shot/manager closed the bead with reason: recovery preserved on `recovery/oro-acqj-rebased` (`b439b694`) but manual merge into `main` was blocked by dirty local main, closing to break a dispatcher merge-conflict loop. Dispatcher emitted `external_close_recovery_failed` and `external_close_cancelled`; the bead ended closed while recovered work was not merged.
- **Filed**: `oro-btfa` P0, "merge-conflict ops can close recovered work without merging".
- **Root cause**: the production MERGE_CONFLICT one-shot prompt told ops agents how to inspect/resolve/test, but did not explicitly forbid task closure as a loop escape when recovered work cannot be merged.
- **Fix**: `pkg/ops/escalation_prompt.go` now adds MERGE_CONFLICT safety rules: do not close the task unless recovered work is merged and verified; do not use task closure to break a merge-conflict loop or hide unmerged recovery work; if dirty repo state, missing worktree data, or semantic conflicts block integration, preserve branch/worktree and output `ESCALATE`.
- **Regression**: `TestWritePlaybook_MergeConflict_ForbidsClosingUnmergedRecovery` asserts the guardrails stay in the prompt.
- **Verification**: focused prompt regression passed; `go test ./pkg/ops -count=1` passed; focused dispatcher external-close / merge-conflict checks passed; `make build && make install` passed.
- **State cleanup before restart**: stopped with `ORO_HUMAN_CONFIRMED=1 /Users/as21/go/bin/oro stop --force`, backed up `~/.oro/projects/oro/state.db` to `state.db.bak-20260510-173525`, completed stale stopped-run assignments 3813 (`oro-19j6`) and 3814 (`oro-krng`), and closed `oro-btfa`.
- **Relaunch result**: `/Users/as21/go/bin/oro start --workers 2 --detach`, dispatcher PID 80660, target/max 2. Startup again warned that pane-died hook registration failed.
- **Initial post-relaunch monitoring**: first 60s check shows PID 80660 running, two busy workers (`oro-19j6`, `oro-krng`), queue=29, one active assignment per worker, exactly two worker OS processes, fresh heartbeats, and no fresh `panic`, `timeout`, `merge`, `review`, `external`, or duplicate-assignment event since relaunch.

## Cycle 33 — Switch swarm to Codex-only after Claude usage limits

- **Trigger**: user reported Claude usage limits and asked to shift to Codex only.
- **Action**: stopped the running swarm with `ORO_HUMAN_CONFIRMED=1 /Users/as21/go/bin/oro stop --force` to prevent further `claude -p` subprocesses. Confirmed remaining Claude child processes were gone.
- **Config change**: added an explicit `agent:` block to `.oro/config.yaml` mapping all tiers and CLI roles (`worker`, `ops_review`, `ops_merge`, `ops_escalation`, `ops_diagnosis`, memory extraction, code-search reranker, etc.) to `runtime: codex`, `model: gpt-5.5-codex`, with role-appropriate reasoning.
- **Launch mode**: relaunched with `ORO_AGENT_RUNTIME=codex /Users/as21/go/bin/oro start --workers 2 --detach` so the dispatcher/worker runtime selector also inherits Codex mode.
- **Verification**: `codex --version` reports `codex-cli 0.130.0`; `go test ./pkg/config ./pkg/agentmodel ./pkg/agentruntime ./pkg/agentruntime/codex -count=1` passed; process scans after relaunch showed `codex exec --model gpt-5.5-codex` for code-search reranking and no `claude -p` processes.
- **State cleanup before restart**: backed up `~/.oro/projects/oro/state.db` and completed stale stopped-run assignments 3815/3816, then later 3819/3820 after an interrupted verification run.
- **Current monitoring state**: dispatcher PID 1153, two Codex-mode workers on `oro-19j6` and `oro-krng`, one active assignment per worker, no Claude processes. Both active tasks are in a deterministic QG retry loop under Codex; monitoring remains at 60s until they either pass, exhaust cleanly, or reveal a dispatcher loop defect.

## Cycle 34 — Stop, fix deterministic QG exhaustion reassignment loop

- **Observed during Codex-only monitoring**: `oro-19j6` and `oro-krng` repeatedly hit deterministic QG failures. After QG exhaustion the dispatcher logged `qg_original_reopened`, immediately reassigned the same reopened beads to the same workers/worktrees, and burned Codex/runtime cycles instead of cooling down or moving to other ready work.
- **Filed**: `oro-sm9x` P0, "deterministic QG exhaustion immediately reassigns same failing bead".
- **Root cause**: the deterministic QG exhaustion path reopened the original bead and `releaseWorkerAfterQGExhaustion` cleared the in-memory exhausted guard. Because the bead stayed `open` with no defer/block state, `oro task ready` surfaced it again on the next assign loop.
- **Fix**: `handleClassifiedQGExhaustion` now reopens the original bead and applies a one-hour defer via the native beadstore. If deferring fails, the dispatcher preserves an in-memory `exhaustedBeads` guard and logs `qg_original_defer_failed`.
- **Regression**: deterministic QG exhaustion and repeated identical deterministic QG tests now assert a cooldown defer is applied after reopening and that already-closed originals are not reopened/deferred.
- **Verification**: focused QG retry tests passed; `go test ./pkg/dispatcher -count=1 -timeout 180s` passed; `make build` and `make install` passed. Closed `oro-sm9x`.
- **Relaunch result**: `ORO_AGENT_RUNTIME=codex /Users/as21/go/bin/oro start --workers 2 --detach`, dispatcher PID 9045. Startup warned pane-died hook registration failed, so monitoring stayed on the tighter error cadence.
- **Post-fix monitoring**: `oro-19j6` and `oro-krng` each exhausted deterministic QG again, then emitted `qg_original_deferred` and `qg_original_reopened`; assignments 3825/3826 completed; both beads are open but deferred until `2026-05-11T00:52Z`. Scheduler moved on to `oro-r1e0` and `oro-g8ns` instead of reassigning the same failing beads. Process scans show no `claude -p`; active child processes are Oro workers and local quality gates.
- **Codex-only follow-up**: a quality-gate unit test exposed that fallback memory extraction could run `codex exec --model claude-haiku-4-5-20251001` when `ORO_AGENT_RUNTIME=codex` but a caller passed a legacy Claude model name. Patched `normalizeCodexModel` to map `claude-*` fallback models to `gpt-5-codex`, added a regression, and committed the code fixes as `29ed672f` so clean worker worktrees inherit them.
- **Final relaunch in this cycle**: stopped PID 85993, backed up state to `state.db.bak-20260510-200743`, completed stopped-run assignments 3831/3832, rebuilt/installed commit `29ed672f`, and relaunched `ORO_AGENT_RUNTIME=codex /Users/as21/go/bin/oro start --workers 2 --detach` as PID 84084. Initial monitoring shows two busy workers on `oro-q1pj` and `oro-5y08`, one active assignment per worker, fresh heartbeats, and no `claude -p` or Claude-model Codex process.
