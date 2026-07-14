# Deep Forensic Audit Remediation Beads

**Date:** 2026-04-22
**Plan:** [2026-04-22-deep-forensic-audit-remediation-plan.md](./2026-04-22-deep-forensic-audit-remediation-plan.md)
**Audit:** Historical source: `archive/audits/2026-04-22-deep-forensic-audit-replacement.md` (not retained)
**Epic Slug:** `forensic-audit-remediation`
**Intent:** Decompose the forensic remediation plan into executable Oro beads with explicit dependencies, acceptance criteria, and rollout order.

## Epic

### `epic(forensic-audit-remediation): restore dispatcher correctness and restart safety`

- Type: `epic`
- Priority: `P1`
- Estimate: `220`
- Labels: `dispatcher`, `reliability`, `recovery`, `sqlite`, `memory`
- Description:
  Restore the core dispatcher invariants identified in the forensic audit so assignment lifecycle, restart recovery, operator cancellation, and semantic-memory behavior are safe to ship under real operational conditions.
- Acceptance:
  `All child beads closed. No bead can have more than one active assignment attempt. Restart preserves recoverable work and does not reopen human-owned in_progress beads. External close cancels by default and does not merge implicitly. Semantic-memory runtime SQL matches production schema and tests. Plan: docs/plans/2026-04-22-deep-forensic-audit-remediation-plan.md`

## Child Beads

### 1. `fix(dispatcher): add assignment-attempt identity and single-active-row enforcement`

- Type: `task`
- Priority: `P1`
- Estimate: `45`
- Depends on: none
- Why first:
  This is the base invariant for the rest of the dispatcher fixes. Without a durable attempt identity and one-active-row enforcement, restart, timeout, handoff, and completion semantics remain ambiguous and unsafe.
- Findings:
  `DFA-001`, `DFA-005`
- Acceptance:
  `Test: pkg/dispatcher/dispatcher_test.go:TestAssignmentReassignmentLeavesSingleActiveRow, pkg/dispatcher/dispatcher_test.go:TestCompleteAssignmentTargetsSpecificAttempt, pkg/dispatcher/dispatcher_test.go:TestPersistBeadCountTargetsSpecificAttempt | Cmd: go test ./pkg/dispatcher/... -run 'TestAssignmentReassignmentLeavesSingleActiveRow|TestCompleteAssignmentTargetsSpecificAttempt|TestPersistBeadCountTargetsSpecificAttempt' -count=1 | Assert: assignments are identified by attempt identity, not bead_id alone; timeout/reassignment leaves exactly one active row; completion and counter updates affect only the current attempt
Read: pkg/dispatcher/dispatcher.go, pkg/dispatcher/worker_pool.go, pkg/protocol/schema.go, docs/plans/2026-04-22-deep-forensic-audit-remediation-plan.md
Signature: introduce current assignment-attempt identity in dispatcher flow and enforce one active assignment row per bead in schema and code
Edges: existing history rows remain readable; duplicate active rows are normalized before uniqueness is enforced; retry and handoff bookkeeping stays attached to the latest attempt`

### 2. `fix(dispatcher): fail assignment closed on persistence errors and clean up safely`

- Type: `task`
- Priority: `P1`
- Estimate: `25`
- Depends on: bead 1
- Findings:
  `DFA-005`
- Acceptance:
  `Test: pkg/dispatcher/dispatcher_test.go:TestAssignBeadDoesNotSendWhenCreateAssignmentFails, pkg/dispatcher/dispatcher_test.go:TestAssignBeadRollsBackStatusAndWorktreeOnPersistenceFailure | Cmd: go test ./pkg/dispatcher/... -run 'TestAssignBeadDoesNotSendWhenCreateAssignmentFails|TestAssignBeadRollsBackStatusAndWorktreeOnPersistenceFailure' -count=1 | Assert: assignBead does not send ASSIGN when durable persistence fails; bead status and worktree state are rolled back or cleaned up; no orphaned live work exists after DB failure
Read: pkg/dispatcher/dispatcher.go, docs/plans/2026-04-22-deep-forensic-audit-remediation-plan.md
Signature: make assignment persistence invariant-bearing, not best-effort; add explicit rollback/cleanup on createAssignment failure
Edges: transient SQLite lock failures surface as assignment failure rather than silent orphaning; cleanup does not delete reused pre-existing worktrees`

### 3. `fix(recovery): preserve recoverable branches and reconcile dispatcher-owned work on startup`

- Type: `task`
- Priority: `P1`
- Estimate: `40`
- Depends on: bead 1
- Findings:
  `DFA-002`, `DFA-010`
- Acceptance:
  `Test: pkg/dispatcher/dispatcher_test.go:TestStartupDoesNotPruneRecoverableAgentBranch, pkg/dispatcher/dispatcher_test.go:TestStartupRecoversFromActiveAssignmentBranchState, pkg/dispatcher/dispatcher_test.go:TestStartupQuarantinesInconsistentRecoveryState | Cmd: go test ./pkg/dispatcher/... -run 'TestStartupDoesNotPruneRecoverableAgentBranch|TestStartupRecoversFromActiveAssignmentBranchState|TestStartupQuarantinesInconsistentRecoveryState' -count=1 | Assert: startup no longer deletes agent/* branches by default; active dispatcher-owned work can be recovered from surviving branch/worktree state; inconsistent state is quarantined and logged rather than destroyed
Read: pkg/dispatcher/dispatcher.go, docs/plans/2026-04-22-deep-forensic-audit-remediation-plan.md
Signature: replace destructive startup pruning with ownership-aware reconciliation and quarantine rules
Edges: a second restart remains idempotent; missing worktree with surviving branch is recoverable; branch preservation does not make unrelated stale branches assignable`

### 4. `fix(recovery): stop reopening human-owned in_progress beads on startup`

- Type: `task`
- Priority: `P1`
- Estimate: `20`
- Depends on: bead 1
- Findings:
  `DFA-003`
- Acceptance:
  `Test: pkg/dispatcher/dispatcher_test.go:TestResetOrphanedBeadsOnlyReopensDispatcherOwnedClaims, pkg/dispatcher/dispatcher_test.go:TestHumanOwnedInProgressBeadRemainsNonAssignableAfterRestart | Cmd: go test ./pkg/dispatcher/... -run 'TestResetOrphanedBeadsOnlyReopensDispatcherOwnedClaims|TestHumanOwnedInProgressBeadRemainsNonAssignableAfterRestart' -count=1 | Assert: startup reopens only dispatcher-owned in_progress beads backed by durable assignment state; human-owned in_progress beads remain untouched and non-assignable
Read: pkg/dispatcher/dispatcher.go, docs/plans/2026-04-22-deep-forensic-audit-remediation-plan.md
Signature: narrow resetOrphanedBeads to dispatcher-owned claims only and document the ownership rule
Edges: dispatcher-owned abandoned beads remain recoverable; human-owned beads are not rewritten to open; assignability logic stays consistent with startup reset semantics`

### 5. `fix(dispatcher): treat external close as cancellation, not implicit merge`

- Type: `task`
- Priority: `P1`
- Estimate: `25`
- Depends on: beads 1, 3
- Findings:
  `DFA-004`
- Acceptance:
  `Test: pkg/dispatcher/dispatcher_test.go:TestExternalCloseDoesNotMergeWorkerBranch, pkg/dispatcher/dispatcher_test.go:TestExternalCloseDoesNotReopenAfterQGFailure, pkg/dispatcher/dispatcher_test.go:TestExternalCloseCleansUpAssignmentAndTracking | Cmd: go test ./pkg/dispatcher/... -run 'TestExternalCloseDoesNotMergeWorkerBranch|TestExternalCloseDoesNotReopenAfterQGFailure|TestExternalCloseCleansUpAssignmentAndTracking' -count=1 | Assert: externally closed or removed beads are canceled by default; mergeAndComplete is not invoked implicitly from external close; assignment state and tracking are cleaned up without reopening the bead
Read: pkg/dispatcher/dispatcher.go, docs/plans/2026-04-22-deep-forensic-audit-remediation-plan.md
Signature: rewrite external-close handling to default to cancellation semantics and isolate any merged-elsewhere behavior behind explicit metadata or config
Edges: worker disconnect during external close still leaves durable state terminal; explicit operator close is not overridden by pre-merge QG behavior`

### 6. `fix(dispatcher): make pending handoff consumption atomic across registration races`

- Type: `task`
- Priority: `P2`
- Estimate: `20`
- Depends on: bead 1
- Findings:
  `DFA-009`
- Acceptance:
  `Test: pkg/dispatcher/register_race_test.go:TestRegisterWorkerRetainsPendingHandoffOnConcurrentDeletion, pkg/dispatcher/worker_pool_test.go:TestRegisterWorkerRetainsPendingHandoffOnSendFailure | Cmd: go test ./pkg/dispatcher/... -run 'TestRegisterWorkerRetainsPendingHandoffOnConcurrentDeletion|TestRegisterWorkerRetainsPendingHandoffOnSendFailure' -count=1 | Assert: pending handoff entries are removed only after successful ASSIGN delivery; disconnects during the unlock/send window leave the handoff recoverable for the next worker
Read: pkg/dispatcher/worker_pool.go, pkg/dispatcher/register_race_test.go, docs/plans/2026-04-22-deep-forensic-audit-remediation-plan.md
Signature: defer pendingHandoff deletion until successful handoff delivery and restore state on send failure or reservation invalidation
Edges: duplicate delivery is avoided; losing the reserved worker does not drop continuation state; minimal fix remains in-memory but race-safe`

### 7. `fix(memory): align semantic-memory runtime SQL with production migrations`

- Type: `task`
- Priority: `P2`
- Estimate: `25`
- Depends on: none
- Findings:
  `DFA-006`
- Acceptance:
  `Test: pkg/memory/model_match_test.go:TestCheckEmbedderModelMatchAgainstProductionSchema, pkg/memory/backfill_test.go:TestBackfillWritesChunksAgainstProductionSchema | Cmd: go test ./pkg/memory/... -run 'TestCheckEmbedderModelMatchAgainstProductionSchema|TestBackfillWritesChunksAgainstProductionSchema' -count=1 | Assert: embedder model mismatch updates kv_store-based backfill state correctly; backfill chunk writes target the actual memory_chunks schema used in production migrations
Read: pkg/memory/memory.go, pkg/memory/backfill.go, pkg/protocol/schema.go, docs/plans/2026-04-22-deep-forensic-audit-remediation-plan.md
Signature: replace nonexistent semantic-memory table references with production-schema SQL and correct chunk insert columns
Edges: production DBs created only through runtime migrations are sufficient for tests; model-reset and chunk-write paths succeed without ad hoc schema setup`

### 8. `test(memory): replace fabricated semantic schemas with migration-backed test setup`

- Type: `task`
- Priority: `P2`
- Estimate: `20`
- Depends on: bead 7
- Findings:
  `DFA-007`
- Acceptance:
  `Test: pkg/memory/model_match_test.go:TestSemanticTestsUseProductionMigrations, pkg/memory/backfill_test.go:TestSemanticSchemaSmokeMatchesRuntimeSQL | Cmd: go test ./pkg/memory/... -run 'TestSemanticTestsUseProductionMigrations|TestSemanticSchemaSmokeMatchesRuntimeSQL' -count=1 | Assert: semantic-memory tests build databases only through production migrations; no test defines fabricated backfill_semantic_memory_state or incompatible memory_chunks columns; schema smoke test catches future runtime-schema drift
Read: pkg/memory/model_match_test.go, pkg/memory/backfill_test.go, pkg/protocol/schema.go
Signature: add shared migration-backed test helper and remove custom semantic schema fixtures
Edges: future semantic tests fail fast on schema drift; helper is reused instead of reintroducing hand-written semantic tables`

### 9. `fix(memory): make empty project scope consistent across vector and FTS search`

- Type: `task`
- Priority: `P3`
- Estimate: `15`
- Depends on: bead 8
- Findings:
  `DFA-008`
- Acceptance:
  `Test: pkg/memory/memory_test.go:TestEmptyProjectScopeSearchesAllProjectsWithVecIndex, pkg/memory/hybrid_integration_test.go:TestHybridSearchEmptyScopeMatchesAllProjectContract | Cmd: go test ./pkg/memory/... -run 'TestEmptyProjectScopeSearchesAllProjectsWithVecIndex|TestHybridSearchEmptyScopeMatchesAllProjectContract' -count=1 | Assert: empty project scope no longer collapses vec-index search to project oro; results under empty scope follow the all-project contract consistently across FTS and vector-enabled paths
Read: pkg/memory/memory.go, pkg/memory/memory_test.go, pkg/memory/hybrid_integration_test.go, docs/plans/2026-04-22-deep-forensic-audit-remediation-plan.md
Signature: implement the low-risk empty-scope fix by bypassing or fan-outing vec-index search so empty scope means all projects everywhere
Edges: correctness is preferred over ANN performance for empty scope; explicit per-project scope behavior remains unchanged`

### 10. `test(dispatcher): add crash-recovery and invariant audit coverage for release gating`

- Type: `task`
- Priority: `P1`
- Estimate: `25`
- Depends on: beads 2, 3, 4, 5, 6
- Why near the end:
  This bead turns the restored invariants into a regression barrier for future changes. It should land after the core dispatcher semantics are stable enough to encode.
- Acceptance:
  `Test: go test ./pkg/dispatcher/... -run 'TestAssignmentReassignmentLeavesSingleActiveRow|TestStartupDoesNotPruneRecoverableAgentBranch|TestResetOrphanedBeadsOnlyReopensDispatcherOwnedClaims|TestExternalCloseDoesNotMergeWorkerBranch|TestRegisterWorkerRetainsPendingHandoffOnConcurrentDeletion' -count=1 | Cmd: go test ./pkg/dispatcher/... -run 'TestAssignmentReassignmentLeavesSingleActiveRow|TestStartupDoesNotPruneRecoverableAgentBranch|TestResetOrphanedBeadsOnlyReopensDispatcherOwnedClaims|TestExternalCloseDoesNotMergeWorkerBranch|TestRegisterWorkerRetainsPendingHandoffOnConcurrentDeletion' -count=1 | Assert: release-gating dispatcher coverage exists for the highest-risk regressions identified by the forensic audit; staging validation steps in the remediation plan are reflected in deterministic tests
Read: docs/plans/2026-04-22-deep-forensic-audit-remediation-plan.md, pkg/dispatcher/...
Signature: assemble a release-gating dispatcher correctness suite covering assignment uniqueness, restart recovery, ownership-safe reset, external close semantics, and handoff race safety
Edges: tests remain deterministic without relying on flaky timing; failures map directly to named forensic invariants`

### 11. `obs(dispatcher): add startup reconciliation and assignment invariant telemetry`

- Type: `task`
- Priority: `P2`
- Estimate: `15`
- Depends on: beads 1, 3, 4, 5
- Acceptance:
  `Test: pkg/dispatcher/dispatcher_test.go:TestStartupReconciliationEmitsRecoverySummary, pkg/dispatcher/dispatcher_test.go:TestAssignmentInvariantViolationIsLogged | Cmd: go test ./pkg/dispatcher/... -run 'TestStartupReconciliationEmitsRecoverySummary|TestAssignmentInvariantViolationIsLogged' -count=1 | Assert: dispatcher emits structured events or logs for recovered attempts, reopened dispatcher-owned beads, skipped human-owned in_progress beads, quarantined inconsistent states, and duplicate-active-assignment detection
Read: pkg/dispatcher/dispatcher.go, docs/plans/2026-04-22-deep-forensic-audit-remediation-plan.md
Signature: add observability for the restored startup and assignment invariants so canary rollout has actionable signals
Edges: telemetry is advisory and must not become another source of assignment failure; events are stable enough to monitor during rollout`

## Dependency Shape

- Critical path:
  1 -> 2
  1 -> 3, 4, 6
  3 -> 5
  1, 3 -> 5
  7 -> 8 -> 9
  2, 3, 4, 5, 6 -> 10
  1, 3, 4, 5 -> 11

- Parallel tracks:
  - Dispatcher core: 1, 2, 3, 4, 5, 6, 10, 11
  - Memory correctness: 7, 8, 9

## Recommended Creation Order

1. Epic
2. Beads 1, 7
3. Beads 2, 3, 4
4. Beads 5, 6, 8
5. Bead 9
6. Beads 10, 11

## Team Split

- Engineer A:
  - Beads 1, 2
- Engineer B:
  - Beads 3, 4, 11
- Engineer C:
  - Beads 5, 6, 10
- Engineer D:
  - Beads 7, 8, 9

## Tracker Note

This file is the beadcraft source of truth for creating execution beads from the forensic remediation plan. If tracker automation is unavailable or unhealthy, create beads manually from this decomposition rather than re-deriving the work from the audit.
