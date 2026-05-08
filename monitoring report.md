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
