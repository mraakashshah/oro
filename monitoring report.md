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
