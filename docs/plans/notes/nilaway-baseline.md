# NilAway Baseline

Date: 2026-05-16

Design context: `docs/plans/2026-05-09-nilaway-go-lint-design.md`, Phase 2 task graph item 2.

## Command

```bash
nilaway -pretty-print=false -exclude-test-files -include-pkgs=oro ./cmd/... ./internal/... ./pkg/... > /tmp/nilaway.out 2>&1
```

Exact task command exit code in this worktree: `3`

Finding count: 59 `Potential nil panic detected` diagnostics.

During the `oro work oro-6hjb` attempt, the same exact command exited `1` before analysis because the worker sandbox resolved the default Go build cache under `/Users/as21/Library/Caches/oro/subprocess/...`, which was not writable from the worker:

```text
pattern ./cmd/...: open /Users/as21/Library/Caches/oro/subprocess/66cba0bab26bf2bf/go-build/...: operation not permitted
```

That worker reran the same pinned production-only NilAway command with only `GOCACHE` redirected to writable temporary storage:

```bash
GOCACHE=/tmp/oro-nilaway-gocache nilaway -pretty-print=false -exclude-test-files -include-pkgs=oro ./cmd/... ./internal/... ./pkg/... > /tmp/nilaway.out 2>&1
```

Baseline analysis exit code: `3`

## cmd/oro

Package decision: fix straightforward true positives. These are local nil map / nil pointer risks and should not need suppressions.

| Finding | NilAway flow summary | Decision |
| --- | --- | --- |
| `cmd/oro/cmd_bead_migrate.go:385:3` | `planBeadMigration()` result is passed to `checkInitialMigrationTargetForPlan(ctx, &plan)`; NilAway cannot prove the pointer argument is non-nil before `plan.Errors` is appended. Same nil source also reaches `cmd_bead_migrate.go:385:24`, `389:3`, and `389:24`. | Fix. Make `checkInitialMigrationTargetForPlan` tolerate a nil plan or avoid pointer passing if mutation can stay local. |
| `cmd/oro/cmd_global_oro_approach.go:319:2` | `settings` is a `map[string]json.RawMessage` populated by `json.Unmarshal`; NilAway treats it as possibly nil before `settings["hooks"] = hooksRaw`. | Fix. Initialize `settings` after unmarshal when nil, or reject non-object JSON explicitly before indexing. |
| `cmd/oro/tmux.go:232:3` | `config` is a `map[string]any`; after successful unmarshal NilAway treats it as possibly nil before `config["projects"] = projects`. | Fix. Initialize `config` after unmarshal when nil, or validate the decoded root object before indexing. |

## pkg/beadstore

Package decision: fix by returning empty slices for zero-row success paths.

| Finding | NilAway flow summary | Decision |
| --- | --- | --- |
| `pkg/beadstore/v3methods.go:139:3` | `scanJourneyEvents()` returns unassigned `events`; `LatestJourney()` passes it to `reverseEvents()`, which slices and swaps `events[i]`. Same nil source also reaches `139:14`, `139:26`, and `139:37`. | Fix. Initialize `events := make([]JourneyEvent, 0)` in `scanJourneyEvents()` so successful empty results are non-nil. |

## pkg/dispatcher

Package decision: fix by guarding store results and worker map lookups before dereference. Suppression is not selected for the current baseline because each dispatcher diagnostic can be addressed with a narrow guard while preserving the state-machine invariants.

### Store Result Guards

| Finding | NilAway flow summary | Decision |
| --- | --- | --- |
| `pkg/dispatcher/bead_graph.go:21:30` | `Store.Create()` may return nil because `SQLiteStore.Create()` returns `Show()` result; `CreateBeadGraph()` dereferences `*bead`. | Fix. Guard nil `bead` after `Create()` and return an internal error if it happens. |
| `pkg/dispatcher/dispatcher.go:2710:11` | `d.beads.Show()` may return nil from `SQLiteStore.Show()` before `detail.AcceptanceCriteria` is read in epic acceptance failure handling. | Fix. Guard nil `detail` and fall back to the existing fetch-failed path. |
| `pkg/dispatcher/dispatcher.go:2710:11` | Same sink as above, but through `FakeStore.Show()`, which can also return nil. | Fix. Same guard covers both concrete implementations. |

### Worker Registration And Handoff

| Finding | NilAway flow summary | Decision |
| --- | --- | --- |
| `pkg/dispatcher/dispatcher.go:375:2` | `registerWorker()` reads `d.workers[id]` in `worker_pool.go:116` and passes it as receiver to `markShuttingDownWithoutAssignment()`, which reads `targetBeadID`. | Fix. Store `w, ok := d.workers[id]` after `upsertWorker()` and guard before receiver calls. |
| `pkg/dispatcher/dispatcher.go:375:2` | Duplicate flow from the `worker_pool.go:122` `d.workers[id]` read to the same receiver. | Fix. Same guarded local worker variable should remove both diagnostics. |
| `pkg/dispatcher/dispatcher.go:390:9` | `registerWorker()` reads `d.workers[id]` in `worker_pool.go:116`, passes it to `sendShutdownWithoutBuffering()`, then to `sendToWorkerWithoutBuffering()`, which reads `w.conn`. | Fix. Same guarded local worker variable before send. |
| `pkg/dispatcher/dispatcher.go:390:9` | Duplicate flow from the `worker_pool.go:122` `d.workers[id]` read to `sendShutdownWithoutBuffering()`. | Fix. Same guarded local worker variable before send. |
| `pkg/dispatcher/worker_pool.go:110:3` | `registerWorker()` writes `d.workers[id].spawnFor` after `upsertWorker()` without a map lookup guard. | Fix. Use the guarded local worker returned/read after upsert. |
| `pkg/dispatcher/worker_pool.go:113:3` | `registerWorker()` writes `d.workers[id].targetBeadID` without a map lookup guard. Same nil source also reaches `worker_pool.go:128:18`. | Fix. Same guarded local worker. |
| `pkg/dispatcher/worker_pool.go:122:25` | Inline `w := d.workers[id]` then reads `w.spawnFor` without checking `w != nil`. | Fix. Guard `w != nil` before state checks. |
| `pkg/dispatcher/worker_pool.go:122:39` | Same inline worker read then reads `w.state` without checking `w != nil`. | Fix. Same guard as previous finding. |
| `pkg/dispatcher/worker_pool.go:170:2` | `assignHandoffToWorker()` reads `d.workers[id]` then writes `w.state`. | Fix. Guard `w != nil` before mutation and log/drop the handoff if the worker disappeared. |
| `pkg/dispatcher/worker_pool.go:171:2` | Same `assignHandoffToWorker()` worker read then writes `w.assignmentID`. | Fix. Same guarded worker lookup as line 170. |
| `pkg/dispatcher/worker_pool.go:172:2` | Same worker read then writes `w.beadID`. | Fix. Same guarded worker lookup as line 170. |
| `pkg/dispatcher/worker_pool.go:173:2` | Same worker read then writes `w.worktree`. | Fix. Same guarded worker lookup as line 170. |
| `pkg/dispatcher/worker_pool.go:174:2` | Same worker read then writes `w.runtime`. | Fix. Same guarded worker lookup as line 170. |
| `pkg/dispatcher/worker_pool.go:175:2` | Same worker read then writes `w.model`. | Fix. Same guarded worker lookup as line 170. |
| `pkg/dispatcher/worker_pool.go:176:2` | Same worker read then writes `w.reasoning`. | Fix. Same guarded worker lookup as line 170. |
| `pkg/dispatcher/worker_pool.go:177:2` | Same worker read then writes `w.epicID`. | Fix. Same guarded worker lookup as line 170. |
| `pkg/dispatcher/worker_pool.go:178:2` | Same worker read then writes `w.baseBranch`. | Fix. Same guarded worker lookup as line 170. |
| `pkg/dispatcher/worker_pool.go:179:2` | Same worker read then writes `w.targetBranch`. | Fix. Same guarded worker lookup as line 170. |
| `pkg/dispatcher/worker_pool.go:180:2` | Same worker read then writes `w.lastProgress`. | Fix. Same guarded worker lookup as line 170. |

### Worker Map Lookup Guards

| Finding | NilAway flow summary | Decision |
| --- | --- | --- |
| `pkg/dispatcher/dispatcher.go:2955:20` | `suppressScaleDownHandoff()` checks `ok` from `d.workers[workerID]` but reads `w.shutdownReason` without proving `w != nil`. | Fix. Change condition to `ok && w != nil && ...`. |
| `pkg/dispatcher/dispatcher.go:3429:15` | `reviewingWorkerMatches()` checks `ok` but reads `w.beadID` without proving `w != nil`. | Fix. Add `w != nil` to the return condition. |
| `pkg/dispatcher/dispatcher.go:3429:37` | Same worker lookup then reads `w.state`. | Fix. Same `w != nil` guard. |
| `pkg/dispatcher/dispatcher.go:5509:15` | Worker lookup reads `w.state` without proving `w != nil`. | Fix. Guard `w != nil` before comparing worker state. |
| `pkg/dispatcher/dispatcher.go:5509:53` | Same worker lookup reads `w.beadID` without proving `w != nil`. | Fix. Same guard as line 5509. |
| `pkg/dispatcher/dispatcher.go:5519:2` | Worker lookup at `5518` then writes `w.assignmentID`. | Fix. Guard `w != nil` before resetting assignment fields. |
| `pkg/dispatcher/dispatcher.go:5520:2` | Same worker read then writes `w.worktree`. | Fix. Same guard as line 5519. |
| `pkg/dispatcher/dispatcher.go:5521:2` | Same worker read then writes `w.baseBranch`. | Fix. Same guard as line 5519. |
| `pkg/dispatcher/dispatcher.go:5522:2` | Same worker read then writes `w.targetBranch`. | Fix. Same guard as line 5519. |
| `pkg/dispatcher/dispatcher.go:5523:2` | Same worker read then writes `w.epicID`. | Fix. Same guard as line 5519. |
| `pkg/dispatcher/dispatcher.go:5524:2` | Same worker read then writes `w.isEpicDecomp`. | Fix. Same guard as line 5519. |
| `pkg/dispatcher/duplicate_guard.go:86:18` | `releaseDuplicateWorkersLocked()` reads `d.workers[c.workerID]` then copies `w.encoder`. | Fix. Guard `w != nil`; skip or log stale candidates. |
| `pkg/dispatcher/duplicate_guard.go:88:3` | Same worker read then clears `w.beadID`. | Fix. Same guard. |
| `pkg/dispatcher/duplicate_guard.go:89:3` | Same worker read then sets `w.state`. | Fix. Same guard. |
| `pkg/dispatcher/duplicate_guard.go:90:3` | Same worker read then clears `w.assignmentID`. | Fix. Same guard. |
| `pkg/dispatcher/duplicate_guard.go:91:3` | Same worker read then clears `w.epicID`. | Fix. Same guard. |
| `pkg/dispatcher/worker_pool.go:641:74` | `removeDeadWorkersLocked()` reads `d.workers[id]` then copies `w.beadID`. Same source also reaches `worker_pool.go:645:64`. | Fix. Guard `w != nil` and skip missing workers because the dead list is derived separately from the map. |
| `pkg/dispatcher/worker_pool.go:641:98` | Same dead-worker read then copies `w.assignmentID`. | Fix. Same `w != nil` guard as line 641. |
| `pkg/dispatcher/worker_pool.go:641:127` | Same dead-worker read then copies `w.prevSession`. | Fix. Same `w != nil` guard as line 641. |
| `pkg/dispatcher/worker_pool.go:641:151` | Same dead-worker read then copies `w.managed`. Same source also reaches `worker_pool.go:642:6`. | Fix. Same `w != nil` guard as line 641. |
| `pkg/dispatcher/worker_pool.go:642:20` | Same dead-worker read then reads `w.spawnFor`. | Fix. Same `w != nil` guard as line 641. |
| `pkg/dispatcher/worker_pool.go:646:7` | Same dead-worker read then closes `w.conn`. | Fix. Same `w != nil` guard as line 641. |
| `pkg/dispatcher/worker_pool.go:656:7` | `removeStoppedSpawnForWorkersLocked()` reads `d.workers[id]` then closes `w.conn`. | Fix. Guard missing or nil worker before close/delete. |
| `pkg/dispatcher/worker_pool.go:665:76` | `removeStuckWorkersLocked()` reads `d.workers[id]` then copies `w.beadID`. Same source also reaches `worker_pool.go:669:63`. | Fix. Guard `w != nil` and skip missing workers. |
| `pkg/dispatcher/worker_pool.go:665:100` | Same stuck-worker read then copies `w.assignmentID`. | Fix. Same `w != nil` guard as line 665. |
| `pkg/dispatcher/worker_pool.go:665:125` | Same stuck-worker read then copies `w.managed`. Same source also reaches `worker_pool.go:666:6`. | Fix. Same `w != nil` guard as line 665. |
| `pkg/dispatcher/worker_pool.go:666:20` | Same stuck-worker read then reads `w.spawnFor`. | Fix. Same `w != nil` guard as line 665. |
| `pkg/dispatcher/worker_pool.go:670:52` | Same stuck-worker read then reads `w.lastProgress`. | Fix. Same `w != nil` guard as line 665. |
| `pkg/dispatcher/worker_pool.go:671:7` | Same stuck-worker read then closes `w.conn`. | Fix. Same `w != nil` guard as line 665. |

## pkg/edit

Package decision: fix by normalizing nil slices to empty slices on success paths. Suppression is not needed.

| Finding | NilAway flow summary | Decision |
| --- | --- | --- |
| `pkg/edit/splice.go:168:21` | `splitByAnchors()` can return nil `pre`; `processSegment(pre, ...)` slices `seg[contIdx+1:]`. | Fix. Return empty slices for empty `pre`, `inter`, and `post`, or make `processSegment` explicitly handle nil segments before slicing. |
| `pkg/edit/splice.go:168:21` | `splitByAnchors()` can return nil `post`; `processSegment(post, ...)` slices `seg[contIdx+1:]`. | Fix. Same nil-to-empty normalization or `processSegment` guard. |
| `pkg/edit/splice.go:199:14` | `findAnchorPositions()` returns unassigned `positions`; `Splice()` indexes `anchorPositions[0]`. Same source also reaches `splice.go:200:13`. | Fix. Initialize `positions := make([]int, 0)` or rely on the existing eligibility error after making the non-nil contract explicit. |
| `pkg/edit/splice.go:212:43` | `splitByAnchors()` can return nil `inter`; `Splice()` indexes `inter[i]`. | Fix. Initialize `inter := make([][]classifiedLine, 0)` and ensure inter length matches the validated anchor gaps. |

## Suggested Cleanup Order

1. Fix nil map initialization in `cmd/oro` and empty slice returns in `pkg/beadstore` / `pkg/edit`.
2. Add nil guards for dispatcher map lookups where `ok` is already available.
3. Revisit dispatcher state-machine paths that derive worker IDs from internal lists. Prefer guards that skip stale IDs; use narrow suppression only after tests prove the lock-held invariant.
4. Re-run the same NilAway command and require the finding count to decrease before wiring NilAway into blocking lint.
