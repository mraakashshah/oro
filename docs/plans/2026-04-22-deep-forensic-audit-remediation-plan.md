# Deep Forensic Audit Remediation Plan

Date: 2026-04-22
Source audit: [docs/audits/2026-04-22-deep-forensic-audit-replacement.md](/Users/as21/codehouse/oro/docs/audits/2026-04-22-deep-forensic-audit-replacement.md)
Repo: `oro`

## Purpose

This document converts the forensic audit findings into an execution-ready remediation plan for a small team operating under time pressure.

This is a delivery plan, not a restatement of findings.

Optimization order:

1. Correctness
2. Rollout safety
3. Speed of risk reduction

## Release Readiness

Current state: not safe to release if the release may include dispatcher restarts, timeout/retry churn, or operator-driven bead closure while work is active.

Release blockers:

1. `FU-1` Assignment lifecycle integrity
2. `FU-2` Restart and ownership-safe recovery
3. `FU-3` External close/cancel semantics

Temporary mitigations if a release cannot be delayed:

- Disable destructive startup branch pruning immediately.
- Avoid dispatcher restarts except under manual supervision.
- Treat external close as operationally unsafe while a worker is active.
- If needed, temporarily disable handoff-heavy workflows until `FU-4` lands.

## Phase 1: Normalized Triage

### Deduplicated fix units

| Fix Unit | Included Findings | Why grouped |
| --- | --- | --- |
| `FU-1` Assignment lifecycle integrity | `DFA-001`, `DFA-005` | Same invariant: one durable active assignment attempt per bead. |
| `FU-2` Restart and ownership-safe recovery | `DFA-002`, `DFA-003`, `DFA-010` | Same failure surface: startup/restart reconciliation and ownership semantics. |
| `FU-3` External close/cancel semantics | `DFA-004` | Isolated semantic bug with high blast radius in merge path. |
| `FU-4` Handoff durability under registration races | `DFA-009` | Localized race in worker registration and handoff delivery. |
| `FU-5` Semantic-memory schema/runtime/test alignment | `DFA-006`, `DFA-007` | Same root issue: runtime SQL, migrations, and tests disagree. |
| `FU-6` Empty-scope vector search correctness | `DFA-008` | Independent search semantics bug. |

### Re-evaluated priority

| Tier | Fix Units | Rationale |
| --- | --- | --- |
| Must-fix immediately | `FU-1`, `FU-2`, `FU-3` | Silent corruption, work loss, or unintended merge risk. |
| High priority | `FU-4`, `FU-5` | Intermittent continuation loss and production/test mismatch. |
| Important but deferrable | `FU-6` | Search correctness issue with lower operational blast radius. |
| Structural / long-term | Recovery ledger and explicit claim ownership model | Needed to make restart safety enforceable rather than conventional. |

### Severity rubric applied

- User impact: accidental merges, duplicate work, lost work, or silent state corruption rank highest.
- Likelihood: timeout/retry and restart flows are normal operational paths, not edge cases.
- Detectability: issues that corrupt relational state or delete branches are low-detectability.
- Reversibility: branch deletion and merged canceled work are hard to reverse safely.

## Phase 2: Fix Units

### `FU-1` Assignment lifecycle integrity

- Findings: `DFA-001`, `DFA-005`
- Affected areas:
  - [pkg/protocol/schema.go](/Users/as21/codehouse/oro/pkg/protocol/schema.go)
  - [pkg/dispatcher/dispatcher.go](/Users/as21/codehouse/oro/pkg/dispatcher/dispatcher.go)
- Core invariant:
  - There is exactly one durable active assignment attempt per bead.
  - No worker starts work unless the active attempt has been durably persisted.
- Risks of partial implementation:
  - DB uniqueness without code changes will cause assignment failures in retry paths.
  - Code-only changes without DB enforcement leave silent corruption possible.
  - Continuing to key updates by `bead_id` will keep history and counters incorrect.

### `FU-2` Restart and ownership-safe recovery

- Findings: `DFA-002`, `DFA-003`, `DFA-010`
- Affected areas:
  - [pkg/dispatcher/dispatcher.go](/Users/as21/codehouse/oro/pkg/dispatcher/dispatcher.go)
  - worktree/branch lifecycle
- Core invariant:
  - Restart preserves recoverable in-flight work.
  - Startup only reconciles dispatcher-owned claims, not human-owned `in_progress`.
- Risks of partial implementation:
  - Preserving branches without owner-aware reset still allows human work theft.
  - Owner-aware reset without branch/worktree preservation still loses in-flight code.
  - Leaving startup behavior implicit will let future changes regress restart safety.

### `FU-3` External close/cancel semantics

- Findings: `DFA-004`
- Affected areas:
  - [pkg/dispatcher/dispatcher.go](/Users/as21/codehouse/oro/pkg/dispatcher/dispatcher.go)
- Core invariant:
  - External close means cancel or abandon by default.
  - Close never implies merge unless explicitly marked as merged elsewhere.
- Risks of partial implementation:
  - Canceling the worker without cleaning up assignment/worktree state leaks durable state.
  - Leaving reopen-on-QG behavior intact can override operator intent after close.

### `FU-4` Handoff durability under registration races

- Findings: `DFA-009`
- Affected areas:
  - [pkg/dispatcher/worker_pool.go](/Users/as21/codehouse/oro/pkg/dispatcher/worker_pool.go)
- Core invariant:
  - Pending handoff context remains recoverable until a replacement worker actually accepts it.
- Risks of partial implementation:
  - Moving deletion later without handling send failure still loses handoff state.
  - Remaining purely in-memory preserves restart loss even if the narrow race is fixed.

### `FU-5` Semantic-memory schema/runtime/test alignment

- Findings: `DFA-006`, `DFA-007`
- Affected areas:
  - [pkg/protocol/schema.go](/Users/as21/codehouse/oro/pkg/protocol/schema.go)
  - [pkg/memory/memory.go](/Users/as21/codehouse/oro/pkg/memory/memory.go)
  - [pkg/memory/backfill.go](/Users/as21/codehouse/oro/pkg/memory/backfill.go)
  - semantic-memory tests
- Core invariant:
  - Production migrations, runtime SQL, and tests all target the same schema.
- Risks of partial implementation:
  - Runtime fixes without test fixes will regress later.
  - Test fixes without runtime fixes preserve production breakage.

### `FU-6` Empty-scope vector search correctness

- Findings: `DFA-008`
- Affected areas:
  - [pkg/memory/memory.go](/Users/as21/codehouse/oro/pkg/memory/memory.go)
  - memory search tests
- Core invariant:
  - Empty project scope means all projects across every search backend.
- Risks of partial implementation:
  - ANN-only fixes without contract tests can introduce hidden ranking regressions.

## Phase 3: Dependency and Sequencing

### Dependency graph

- `FU-1` precedes `FU-2`
- `FU-1` precedes `FU-3`
- `FU-1` should precede the durable version of `FU-4`
- `FU-5` precedes `FU-6`
- Structural recovery-ledger work depends on `FU-1` and `FU-2`

### Linearized execution order

1. `FU-1` Assignment lifecycle integrity
2. `FU-2` Restart and ownership-safe recovery
3. `FU-3` External close/cancel semantics
4. `FU-4` Handoff durability under registration races
5. `FU-5` Semantic-memory schema/runtime/test alignment
6. `FU-6` Empty-scope vector search correctness
7. Structural recovery-ledger refactor

### Parallelization guidance

- Engineer A owns `FU-1` end-to-end.
- Engineer B owns `FU-2`, but should not merge startup changes until `FU-1` assignment-attempt semantics are stable.
- Engineer C can implement `FU-3` in parallel after `FU-1` API shape is known.
- Engineer D can execute `FU-5` and `FU-6` independently of dispatcher work.
- `FU-4` can start in parallel, but any durable coupling to assignment identity should reuse `FU-1` primitives.

### Feature flags and staged rollout

- `FU-3` may use a short-lived feature flag if operators currently rely on close-implies-merge semantics.
- `FU-6` may ship behind a memory-search toggle if empty-scope ANN behavior changes materially.
- `FU-1` and `FU-2` should not be hidden behind flags; they restore core correctness.

## Phase 4: Implementation Plan By Fix Unit

### `FU-1` Assignment lifecycle integrity

#### Objective

Restore the guarantee that there is one durable active assignment attempt per bead, and that retry, handoff, timeout, and completion mutate the correct attempt only.

#### Root cause

The current lifecycle is modeled around `bead_id` rather than an explicit assignment attempt. Persistence is best-effort, uniqueness is unenforced, and updates target every active row for a bead.

#### Implementation steps

1. Introduce an explicit current assignment-attempt identity in dispatcher flow.
2. Update `createAssignment()` in [pkg/dispatcher/dispatcher.go](/Users/as21/codehouse/oro/pkg/dispatcher/dispatcher.go) to return the created row identity or a durable assignment token.
3. Thread that identity through worker state so completion, retry, and handoff paths update one attempt rather than all active rows for a bead.
4. Add a migration in [pkg/protocol/schema.go](/Users/as21/codehouse/oro/pkg/protocol/schema.go) for a partial unique index enforcing one active row per `bead_id`.
5. Before adding the unique index, add a one-time cleanup step that finds duplicate active rows and marks older rows superseded or completed.
6. Change timeout/reassignment flows in [pkg/dispatcher/worker_pool.go](/Users/as21/codehouse/oro/pkg/dispatcher/worker_pool.go) and [pkg/dispatcher/dispatcher.go](/Users/as21/codehouse/oro/pkg/dispatcher/dispatcher.go) so the prior active attempt is explicitly closed before a new one is inserted.
7. Change `persistBeadCount()` and `completeAssignment()` to target assignment identity, not `bead_id`.
8. Make `assignBead()` fail closed if durable assignment creation fails:
   - revert bead status
   - remove fresh worktree if created
   - clear in-memory assignment tracking
   - do not send `ASSIGN`
9. Add invariant checks and error logs around duplicate active rows at assignment time and startup.

#### Safety mechanisms

- Make migration additive first, then enable strict uniqueness after cleanup succeeds.
- Log but quarantine duplicate rows found during migration; do not auto-delete history.
- Use explicit superseded/completed terminal states if history clarity matters.

#### Data considerations

- Migration required for unique partial index.
- Backfill required to clean existing duplicate active rows before index creation.
- Existing corrupted attempt history may need a best-effort normalization pass.
- Rollback implication: once duplicates are normalized, rollback is low-risk if code still tolerates explicit terminal states.

#### Tests to add before fixing

- Reproduce timeout then reassignment and assert duplicate active rows are possible today.
- Inject DB failure on `createAssignment()` and show worker still receives work today.

#### Tests to add after fixing

- Unit test: reassignment closes old attempt and creates exactly one new active row.
- Integration test: timeout -> retry -> completion preserves historical attribution per attempt.
- Failure-mode test: SQLite write failure prevents assignment.
- Recovery test: restart sees one active attempt only.
- Concurrency test: parallel assignment attempts cannot create two active rows.

#### Validation plan

- In staging, force worker timeout and reassignment.
- Query `assignments` and verify one active row per bead.
- Confirm retry/handoff counters attach only to the latest attempt.
- Verify no worker starts when DB write injection fails.

#### Rollout plan

- Deploy to canary dispatcher first.
- Monitor duplicate-active-row invariant and assignment failure rate.
- Expand rollout only after forced timeout/retry staging checks pass.

#### Abort conditions

- Any bead with more than one active assignment row after deploy.
- Assignment create failures rising without rollback behavior.
- Worktree leaks rising due to failed assignment cleanup.

#### Blast radius

- Assignment, retry, handoff, completion, restart recovery, auditability.

#### Effort estimate

- Large

### `FU-2` Restart and ownership-safe recovery

#### Objective

Restore restart behavior so that dispatcher restarts preserve recoverable work and do not auto-claim human-owned `in_progress` beads.

#### Root cause

Startup is implemented as destructive cleanup followed by blanket reopening of `in_progress` beads, with no durable ownership model.

#### Implementation steps

1. Remove unconditional startup deletion of `agent/*` branches in `Run()` and `pruneStaleAgentBranches()` in [pkg/dispatcher/dispatcher.go](/Users/as21/codehouse/oro/pkg/dispatcher/dispatcher.go).
2. Narrow `resetOrphanedBeads()` so it only reopens beads with active dispatcher-owned assignments.
3. Add durable ownership metadata for dispatcher claims if not already derivable from assignment state.
4. Replace destructive startup assumptions with reconciliation rules:
   - active assignment + existing branch/worktree: recover or reuse
   - active assignment + missing worktree + surviving branch: recreate worktree from branch
   - external `in_progress` without dispatcher ownership: leave untouched
   - inconsistent state: quarantine and log instead of deleting
5. Emit explicit startup reconciliation events for recovered, reopened, quarantined, and skipped beads.
6. Document recovery rules in code comments next to startup flow so future changes have an explicit contract.

#### Safety mechanisms

- Prefer quarantine over deletion when state is inconsistent.
- Add a recovery mode flag only as an emergency escape hatch, not the primary path.
- Keep startup idempotent; a second restart should not further damage state.

#### Data considerations

- Minimal fix may not need a migration if ownership can be inferred from active assignments.
- Robust fix likely benefits from ownership/session columns.
- Rollback is straightforward if destructive startup pruning remains disabled.

#### Tests to add before fixing

- Crash/restart with committed but unmerged `agent/*` branch.
- Startup with one human-owned `in_progress` bead and one dispatcher-owned active bead.

#### Tests to add after fixing

- Integration test: committed branch survives restart.
- Integration test: human-owned `in_progress` remains non-assignable after restart.
- Recovery test: missing worktree but surviving branch is reused.
- Failure-mode test: inconsistent state is quarantined, not deleted.

#### Validation plan

- In staging, create an active assignment with committed work on `agent/<bead>`, restart dispatcher, verify branch survives and work is recoverable.
- Manually mark a bead `in_progress` outside dispatcher, restart, verify it remains untouched.
- Inspect startup event logs for recovery counts and quarantine events.

#### Rollout plan

- Restart a canary dispatcher under controlled conditions before broad rollout.
- Monitor reopened-bead count, quarantined-bead count, and branch/worktree leak metrics.

#### Abort conditions

- Any restart causes branch deletion or worktree loss for active work.
- Human-owned `in_progress` beads become assignable after restart.

#### Blast radius

- Startup, restart safety, assignment eligibility, branch/worktree continuity, operator workflow.

#### Effort estimate

- Large

### `FU-3` External close/cancel semantics

#### Objective

Ensure that externally closed beads are canceled and cleaned up, not merged by inference.

#### Root cause

`handleClosedAssignment()` currently treats closed or missing beads as a signal to merge existing worker output if a worktree exists.

#### Implementation steps

1. Change `handleClosedAssignment()` in [pkg/dispatcher/dispatcher.go](/Users/as21/codehouse/oro/pkg/dispatcher/dispatcher.go) so external close defaults to cancellation semantics.
2. Stop launching `mergeAndComplete()` from the external-close path unless explicit metadata indicates merged elsewhere.
3. Ensure cancellation path:
   - shuts down worker
   - completes or supersedes the active assignment
   - clears tracking
   - removes or quarantines worktree depending on recovery policy
4. Audit `checkPreMergeQG()` and related reopen logic so QG failure cannot reopen a bead that an operator explicitly closed.
5. Add event types that distinguish `external_close_cancelled` from `merged_elsewhere`.

#### Safety mechanisms

- If operator expectations are unclear, gate the new behavior behind a short-lived config flag with default set to cancel.
- Preserve branch/worktree only if recovery policy explicitly wants quarantine; do not merge by default.

#### Data considerations

- No migration required for the minimal fix.
- Robust fix may add explicit close reason/source metadata.
- Rollback is safe if behavior is isolated behind config.

#### Tests to add before fixing

- External close with unmerged commits currently merges.
- External close followed by QG failure currently reopens.

#### Tests to add after fixing

- Integration test: external close with local commits does not merge.
- Integration test: explicitly closed bead does not reopen on QG failure.
- Failure-mode test: worker disconnect during external close still closes assignment and clears tracking.

#### Validation plan

- In staging, close an active bead with unmerged worker commits and verify no merge commit lands.
- Confirm bead remains closed.
- Confirm assignment is terminal and worktree cleanup/quarantine follows policy.

#### Rollout plan

- Ship with targeted monitoring on external-close event counts.
- Have operators exercise the flow in staging before production rollout.

#### Abort conditions

- Any merged commit produced from an externally closed bead without explicit override.
- Explicitly closed beads reopening.

#### Blast radius

- Merge path, operator cancellation path, worker shutdown cleanup.

#### Effort estimate

- Medium

### `FU-4` Handoff durability under registration races

#### Objective

Ensure continuation context is not lost when worker registration races with disconnects or heartbeat cleanup.

#### Root cause

`registerWorker()` removes the pending handoff before delivery is successful and ignores `sendToWorker()` result in the handoff path.

#### Implementation steps

1. In [pkg/dispatcher/worker_pool.go](/Users/as21/codehouse/oro/pkg/dispatcher/worker_pool.go), stop deleting `pendingHandoffs` before delivery succeeds.
2. Reserve the worker, compute memory context outside the lock, then re-check worker validity.
3. Attempt `ASSIGN`; only after successful send should the pending handoff be removed.
4. If worker disappears or send fails, restore or retain the pending handoff entry.
5. If `FU-1` exposes assignment identity, bind pending handoff to that active attempt for easier cleanup.
6. Later, persist pending handoffs in SQLite if restart durability is required.

#### Safety mechanisms

- Keep the change localized and non-destructive.
- Prefer retaining a handoff over dropping it; duplicate delivery is easier to detect than lost continuation.

#### Data considerations

- No migration for the minimal fix.
- Durable persistence would require schema extension later.

#### Tests to add before fixing

- Reproduce disconnect during `registerWorker()` unlock window and show handoff is lost.

#### Tests to add after fixing

- Race test: worker deleted during unlock window leaves pending handoff intact.
- Send failure test: handoff remains available for next worker.
- Integration test: replacement worker receives original continuation state.

#### Validation plan

- Run reconnect-churn soak test in staging with forced worker disconnects during registration.
- Verify handoff queue depth returns to zero only after successful reassignment.

#### Rollout plan

- Safe to ship after dispatcher blockers or in the same train if well isolated.

#### Abort conditions

- Handoff queue drains without successful reassignment.
- Replacement workers restart work without expected continuation state.

#### Blast radius

- Handoff continuity and worker failover quality.

#### Effort estimate

- Medium

### `FU-5` Semantic-memory schema/runtime/test alignment

#### Objective

Restore truthfulness between production migrations, runtime SQL, and semantic-memory tests.

#### Root cause

Runtime code still references a nonexistent backfill table and incorrect chunk columns, while tests construct fabricated schemas that hide the mismatch.

#### Implementation steps

1. Update `resetEmbedderData()` and `checkEmbedderModelMatch()` in [pkg/memory/memory.go](/Users/as21/codehouse/oro/pkg/memory/memory.go) to use `kv_store` for backfill state.
2. Fix `chunkWriter` in [pkg/memory/backfill.go](/Users/as21/codehouse/oro/pkg/memory/backfill.go) to write the actual `memory_chunks` columns defined by production migrations.
3. Create a shared semantic test helper that provisions DBs exclusively via production migrations in [pkg/protocol/schema.go](/Users/as21/codehouse/oro/pkg/protocol/schema.go).
4. Rewrite [pkg/memory/model_match_test.go](/Users/as21/codehouse/oro/pkg/memory/model_match_test.go) and [pkg/memory/backfill_test.go](/Users/as21/codehouse/oro/pkg/memory/backfill_test.go) to use that helper.
5. Add a schema smoke test that asserts runtime SQL references only migrated tables/columns.

#### Safety mechanisms

- Keep changes in the memory package isolated from dispatcher hot paths.
- Fail tests hard on schema mismatch instead of logging and suppressing.

#### Data considerations

- Likely no migration required because the production schema already exists.
- Verify behavior on existing DBs that have partial semantic rollout state.

#### Tests to add before fixing

- Run model-reset and backfill logic against a DB created only from production migrations.

#### Tests to add after fixing

- Unit test: embedder model mismatch resets `kv_store` state correctly.
- Integration test: backfill writes `memory_chunks` rows with production schema.
- Schema-smoke test: runtime SQL names match migrated tables and columns.

#### Validation plan

- In staging, trigger semantic model mismatch and verify backfill state becomes `pending`.
- Trigger backfill and verify chunk rows populate.
- Monitor memory logs for schema-related errors.

#### Rollout plan

- Ship after tests pass; low coupling to dispatcher fixes.

#### Abort conditions

- Runtime schema errors in memory logs after deploy.
- Backfill remains pending without chunk population.

#### Blast radius

- Semantic search and backfill only.

#### Effort estimate

- Medium

### `FU-6` Empty-scope vector search correctness

#### Objective

Make empty project scope consistently mean all projects across FTS and vector search paths.

#### Root cause

`vectorSearchViaIndex()` normalizes empty scope to `"oro"` while higher-level API semantics and FTS treat it as global.

#### Implementation steps

1. In [pkg/memory/memory.go](/Users/as21/codehouse/oro/pkg/memory/memory.go), implement the lowest-risk fix first:
   - if project scope is empty, bypass vec-index and use a global non-ANN path
2. Add contract tests for multi-project empty-scope search.
3. If needed later, implement explicit all-project ANN fan-out with ranking merge behind a separate design.

#### Safety mechanisms

- Prefer correctness over ANN performance when scope is empty.
- Keep the fallback path explicit and observable in logs or counters.

#### Data considerations

- None.

#### Tests to add before fixing

- Multi-project search with empty scope and vec-index enabled currently under-returns non-`oro` results.

#### Tests to add after fixing

- Integration test: empty-scope results include cross-project memories.
- Contract test: FTS and vector-enabled paths are scope-consistent for empty project.

#### Validation plan

- Compare empty-scope results in staging with vec-index enabled and disabled.
- Verify no cross-project loss when ANN path is enabled.

#### Rollout plan

- Low-risk; can ship behind a config toggle if performance sensitivity exists.

#### Abort conditions

- Significant search correctness mismatch remains under empty scope.

#### Blast radius

- Memory search relevance and performance under empty-scope queries.

#### Effort estimate

- Small to Medium

## Phase 5: Global Risk Mitigation

### Logging and observability to add

- Startup reconciliation summary:
  - recovered attempts
  - reopened dispatcher-owned beads
  - skipped human-owned `in_progress`
  - quarantined inconsistent states
- Assignment lifecycle counters:
  - assignment create failures
  - duplicate-active-row detections
  - superseded attempts
  - completion failures
- External close counters:
  - canceled by external close
  - merged elsewhere
  - attempted reopen after explicit close
- Handoff counters:
  - pending handoffs created
  - delivered
  - restored after send failure

### Assertions and invariant checks

- On startup and periodically:
  - no bead has more than one active assignment row
  - no active assignment references a missing worker and missing recoverable branch without quarantine
  - no human-owned `in_progress` bead is auto-reopened
- In dispatcher hot paths:
  - assignment creation must succeed before `ASSIGN`
  - assignment completion must target exactly one attempt

### Error handling standardization

- Invariant-bearing writes must not be best-effort.
- Telemetry and advisory logs may remain best-effort.
- Use explicit cleanup/rollback helpers for:
  - assignment creation failure
  - worker send failure
  - external close cancellation
  - startup reconciliation

### Test infrastructure improvements

- Add SQLite fault-injection coverage for dispatcher assignment lifecycle.
- Add crash-recovery integration harness spanning:
  - SQLite state
  - git branches/worktrees
  - bead source status
- Add production-migration-only DB helper for semantic-memory tests.

### CI/CD safeguards

- Fail CI if invariant audit tests fail.
- Require restart-recovery integration suite before merging dispatcher lifecycle changes.
- Require semantic schema smoke test before merging memory schema/runtime changes.

## Phase 6: Execution Roadmap

### Immediate (blockers)

1. `FU-1` Assignment lifecycle integrity
   - Rationale: stops ongoing silent corruption and makes downstream fixes reliable.
2. `FU-2` Restart and ownership-safe recovery
   - Rationale: current restarts can lose code and steal human-owned work.
3. `FU-3` External close/cancel semantics
   - Rationale: current operator cancellation can merge invalid or canceled work.

### Next (high ROI)

1. `FU-4` Handoff durability under registration races
   - Rationale: contained change, good reliability payoff, low blast radius.
2. `FU-5` Semantic-memory schema/runtime/test alignment
   - Rationale: removes false confidence and restores production correctness for memory features.

### Parallelizable workstreams

- Workstream A: `FU-1`
  - Owner: senior dispatcher engineer
  - Coordination required with `FU-2` and `FU-4`
- Workstream B: `FU-2`
  - Owner: senior runtime/recovery engineer
  - Do not merge startup reconciliation until `FU-1` attempt semantics are stable
- Workstream C: `FU-3` then `FU-4`
  - Owner: dispatcher/worker lifecycle engineer
- Workstream D: `FU-5` then `FU-6`
  - Owner: memory/search engineer

### Later (structural)

- Introduce a durable recovery ledger tying together:
  - bead attempt
  - worker
  - branch
  - worktree
  - dispatcher session ownership
- Introduce explicit typed project-scope policy in memory search.
- Replace startup cleanup heuristics with a reconciler model backed by durable ownership.

## Phase 7: Validation Strategy

### Highest-value tests to add across the system

1. Timeout then reassignment leaves exactly one active assignment row.
2. Assignment create DB failure prevents worker assignment and rolls back bead/worktree state.
3. Crash restart with committed unmerged `agent/*` work preserves branch and recoverability.
4. Restart with mixed human-owned and dispatcher-owned `in_progress` beads only reopens dispatcher-owned work.
5. External close on active bead with local commits does not merge and does not reopen.
6. Handoff disconnect during worker registration keeps pending handoff recoverable.
7. Semantic model mismatch on production-migrated DB resets backfill state successfully.
8. Semantic backfill on production schema writes chunk rows successfully.
9. Empty project scope returns cross-project results consistently when vec index is enabled.
10. Startup invariant audit detects duplicate active assignments and missing recovery state.

### Scenarios most likely to expose hidden bugs

- Dispatcher crash between bead status update and assignment persistence.
- Dispatcher restart with partially cleaned git state.
- Worker disconnect during handoff reservation or send.
- External bead closure while QG or merge is in progress.
- SQLite write contention during assignment create and completion.
- Semantic model rotation on a DB created only from runtime migrations.

### Production signals to monitor post-fix

- duplicate active assignments
- assignment create failures
- assignment completion failures
- startup reopened-bead count
- startup quarantined-bead count
- branch/worktree leak count
- external-close cancel count
- external-close merge count
- pending handoff age
- semantic schema/backfill errors

## Phase 8: Release Strategy

### Release gate

Must be fixed before release:

1. `FU-1`
2. `FU-2`
3. `FU-3`

Can be mitigated temporarily:

- `FU-4` by reducing reliance on handoff-heavy flows
- `FU-5` by treating semantic-memory features as degraded but non-blocking
- `FU-6` by tolerating reduced empty-scope vector correctness or forcing fallback paths

### Rollout shape

1. Land additive schema and cleanup logic for `FU-1`
2. Validate on staging with forced timeout/retry and restart scenarios
3. Roll out canary dispatcher with enhanced invariant logging
4. Observe for one operational cycle with forced restart and operator close scenarios
5. Expand rollout after no invariant violations are observed

### Fallback and rollback strategy

- If `FU-1` causes assignment regressions:
  - stop new assignments
  - preserve DB and git state
  - revert code path, not data cleanup
- If `FU-2` causes recovery anomalies:
  - disable automated restart handling
  - preserve branches/worktrees
  - use manual reconciliation
- If `FU-3` causes operator workflow confusion:
  - temporarily enable compatibility flag while retaining explicit monitoring

## Execution Checklist

### Before coding

- Confirm owners for `FU-1` through `FU-5`
- Align on assignment-attempt identity design
- Align on startup reconciliation policy and quarantine rules

### Before merge

- Required tests for the affected fix unit are present
- Staging validation scenario has been executed
- Monitoring hooks for the new invariants are in place

### Before production rollout

- Canary deployment has completed
- Restart scenario has been exercised on the candidate build
- External close scenario has been exercised on the candidate build
- No duplicate-active-assignment invariant violations remain

## Acceptance Criteria Summary

- No bead can have more than one active assignment attempt.
- Restart preserves recoverable work and does not auto-claim human-owned work.
- External close cancels work by default and does not merge by inference.
- Pending handoff state survives registration/send races.
- Semantic-memory runtime and tests both use production schema.
- Empty project scope behaves consistently across search backends.
