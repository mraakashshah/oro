# Deep Forensic Audit Replacement

Date: 2026-04-22
Repo: `oro`

## Phase 1: Independent System Reconstruction

### What the system does

Oro is a local multi-agent software-engineering orchestrator.

- `cmd/oro` is the primary CLI and process supervisor.
- A background dispatcher (`pkg/dispatcher`) listens on a Unix domain socket, polls bead state from an external bead system, assigns work to workers, tracks runtime state in SQLite, runs merge/review/escalation flows, and optionally serves an HTTP dashboard.
- Workers (`pkg/worker`) connect over UDS, execute an agent runtime in a git worktree, emit heartbeats/status/handoff/done events, run quality gates locally, and hand results back to the dispatcher.
- State is split across:
  - external bead source (`bd`-style API)
  - git/worktrees/branches
  - SQLite runtime DB
  - in-memory dispatcher maps
  - tmux/session files for architect/manager panes

### Primary execution path

1. `oro start` launches the dispatcher daemon and tmux panes.
2. `Dispatcher.Run()` initializes schema, prunes worktrees/branches, restores some DB state, resets `in_progress` beads, opens the UDS listener, and starts assignment/heartbeat/pane/escalation loops.
3. Workers connect and register via heartbeat.
4. `assignLoop` calls `assignBead`, which:
   - marks the bead `in_progress`
   - creates or reuses a git worktree
   - inserts an `assignments` row
   - sends `ASSIGN` to the worker
5. Worker runs the runtime subprocess, streams output, runs QG, then sends either:
   - `DONE` with QG failure
   - `READY_FOR_REVIEW`
   - `HANDOFF`
6. Dispatcher retries, respawns, reviews, merges, closes the bead, and removes the worktree.

### State model and mutation points

- External bead status:
  - mutated in `assignBead`, timeout handling, external-close handling, startup reset, merge completion
- SQLite `assignments`:
  - created in `createAssignment`
  - updated by `persistBeadCount`
  - completed by `completeAssignment`
- In-memory dispatcher maps:
  - `workers`
  - `assigningBeads`
  - `worktreeByBead`
  - `attemptCounts`
  - `handoffCounts`
  - `pendingHandoffs`
  - `mergingBeads`
  - `processedExternalClose`
- Git state:
  - worktrees created/removed through `WorktreeManager`
  - `agent/*` and `epic/*` branches created, merged, deleted
- Memory DB:
  - stores learnings, summaries, rejection history, search telemetry, semantic artifacts

### Implicit invariants the runtime depends on

- At most one active assignment exists per bead.
- A bead marked `in_progress` is actually owned by the dispatcher, not a human.
- External bead closure is safe to interpret as “merge or finish cleanup”.
- Crash recovery can safely discard prior `agent/*` branches/worktrees.
- Assignment persistence is durable enough to reconstruct state after restart.
- Semantic-memory schema in tests matches runtime schema.
- Empty project scope means “all projects” consistently across FTS and vector paths.

### External boundaries

- bead system via `BeadSource`
- git/worktree/branch operations
- tmux
- local Unix socket
- optional HTTP dashboard
- local model/runtime subprocesses
- SQLite via `modernc.org/sqlite`

### Concurrency model

- Dispatcher is a shared-state multi-goroutine process guarded mostly by one mutex plus a few atomics/channels.
- Workers are separate processes plus goroutines for socket reads, subprocess monitoring, stdout processing, and context watching.
- Failure handling is mostly best-effort and often non-transactional across:
  - external bead state
  - SQLite
  - in-memory maps
  - git state

### Ambiguities

- The code treats `in_progress` as both a human-owned state and a dispatcher-owned transient state.
- Semantic-memory backfill/model-mismatch code exists, but production wiring is incomplete and tests use a different schema than production migrations.
- Crash recovery is documented as preserving continuity, but startup code aggressively deletes branches and reopens beads.

## Phase 2-6: Deep Findings

### DFA-001
- Title: Active assignment uniqueness is unenforced, and timeout/retry flows create duplicate active rows
- Severity: critical
- Confidence: confirmed
- Category: data integrity
- Exact location: `pkg/protocol/schema.go`, `pkg/dispatcher/worker_pool.go:401-465`, `pkg/dispatcher/dispatcher.go:3382`, `pkg/dispatcher/dispatcher.go:4547-4657`
- Evidence from code:
  - `assignments` has no uniqueness constraint on active rows for `bead_id`.
  - `checkHeartbeats()` removes dead/stuck workers and reopens beads but never completes the existing active assignment row.
  - `assignBead()` always calls `INSERT INTO assignments ...` for a new assignment.
  - `persistBeadCount()` and `completeAssignment()` update rows by `bead_id` only, affecting every active row for that bead.
- Violated invariant: one active assignment row per bead.
- Concrete failure mode:
  - Worker A times out.
  - Bead is reopened.
  - Worker B is assigned and inserts a second active assignment row.
  - Later retries/handoffs mutate both rows.
  - Completion marks all active rows completed, destroying historical attribution and corrupting retry/handoff accounting.
- Triggering conditions: worker timeout, stuck-worker reset, reconnect loss, or any reassignment before the prior row is completed.
- Blast radius: assignment history, restart recovery, retry counts, handoff counts, auditability.
- Failure type: silent, intermittent.
- Why it would likely escape detection: most tests assert behavior from in-memory maps or current worker state, not relational invariants across multiple active rows.
- Minimal fix:
  - Add a partial unique index enforcing one active assignment per bead.
  - Complete or supersede the prior active row before inserting a new one.
- Robust fix:
  - Model assignment lifecycle explicitly with immutable attempts and a current-assignment foreign key.
  - Make reassignment a transaction that atomically closes the old row and creates the new row.
- Regression test:
  - Simulate timeout then reassignment; assert exactly one `status='active'` row remains and counters attach only to the latest attempt.

### DFA-002
- Title: Startup recovery can silently discard in-flight work by pruning worktrees/branches before reopening beads
- Severity: critical
- Confidence: confirmed
- Category: correctness
- Exact location: `pkg/dispatcher/dispatcher.go:897-905`, `pkg/dispatcher/dispatcher.go:4566-4586`, `pkg/dispatcher/dispatcher.go:4604-4618`
- Evidence from code:
  - `Run()` calls `worktrees.Prune()` and then `pruneStaleAgentBranches()` before reset/reassignment.
  - `pruneStaleAgentBranches()` force-deletes every `agent/*` branch with `git branch -D`.
  - `resetOrphanedBeads()` then reopens all `in_progress` beads.
- Violated invariant: crash recovery should preserve or recover outstanding worker progress.
- Concrete failure mode:
  - Dispatcher crashes after a worker commits useful work to `agent/<bead>`, but before merge.
  - On restart, startup deletes the branch ref and reopens the bead.
  - The next assignment starts from main, not from recovered work.
  - Prior committed progress is orphaned or lost.
- Triggering conditions: dispatcher restart/crash while a bead has unmerged branch state.
- Blast radius: code loss, duplicate implementation, misleading bead history.
- Failure type: silent, catastrophic for affected bead.
- Why it would likely escape detection: recovery tests focus on same-session respawn reuse, not process crash + restart with committed but unmerged work.
- Minimal fix:
  - Stop deleting all `agent/*` branches on startup.
- Robust fix:
  - Persist branch/worktree ownership in SQLite and reconcile on restart.
  - Recover or explicitly quarantine prior in-flight attempts instead of deleting refs.
- Regression test:
  - Seed an active assignment with an unmerged `agent/*` branch, restart dispatcher, assert branch survives and reassignment can recover it.

### DFA-003
- Title: Startup reset reopens human-owned `in_progress` beads and makes them assignable
- Severity: high
- Confidence: confirmed
- Category: correctness
- Exact location: `pkg/dispatcher/dispatcher.go:4604-4618`, `pkg/dispatcher/dispatcher.go:3040-3043`
- Evidence from code:
  - `isBeadAssignable()` explicitly treats `in_progress` as human-owned/non-assignable.
  - `resetOrphanedBeads()` unconditionally changes every `in_progress` bead to `open` on startup.
- Violated invariant: human-owned `in_progress` work must not be auto-claimed by the dispatcher.
- Concrete failure mode:
  - Operator marks a bead `in_progress` manually.
  - Dispatcher restarts.
  - Startup rewrites it to `open`.
  - Assign loop now considers it eligible and hands it to a worker.
- Triggering conditions: any startup while human-owned beads are `in_progress`.
- Blast radius: duplicate work, unexpected branch creation, merge conflicts, human/agent contention.
- Failure type: silent.
- Why it would likely escape detection: startup logic does not distinguish dispatcher-owned vs externally-owned `in_progress`.
- Minimal fix:
  - Only reopen beads that have an active dispatcher-owned assignment row.
- Robust fix:
  - Add ownership metadata to bead claims and reconcile only claims owned by the crashed dispatcher instance.
- Regression test:
  - Seed one human-only `in_progress` bead and one dispatcher-owned `in_progress` bead; restart and assert only the dispatcher-owned bead is reopened.

### DFA-004
- Title: External bead closure is treated as “merge whatever exists”, which can land canceled or partial work
- Severity: high
- Confidence: confirmed
- Category: correctness
- Exact location: `pkg/dispatcher/dispatcher.go:2884-2951`, `pkg/dispatcher/dispatcher.go:1589-1648`
- Evidence from code:
  - `handleClosedAssignment()` interprets bead missing/closed as external closure.
  - If a worktree exists, it launches `mergeAndComplete()` instead of canceling/abandoning work.
  - `checkPreMergeQG()` can also reopen the bead to `open` on QG failure.
- Violated invariant: external closure/cancellation should not implicitly merge in-flight worker output.
- Concrete failure mode:
  - Operator closes a bead because scope changed, work is invalid, or they want to abandon it.
  - Dispatcher shuts down the worker and merges any existing branch commits.
  - If QG fails, the code reopens the bead the operator explicitly closed.
- Triggering conditions: manual closure/removal of a bead while a worker is assigned.
- Blast radius: unwanted production code changes, reopened abandoned work, hard-to-explain state flips.
- Failure type: silent or intermittent.
- Why it would likely escape detection: happy-path tests for external close focus on cleanup, not the semantic meaning of closure.
- Minimal fix:
  - Do not merge on external close; treat it as cancellation by default.
- Robust fix:
  - Differentiate “closed because merged elsewhere” from “closed/canceled” with explicit source/reason metadata.
- Regression test:
  - Close a bead externally with unmerged worker commits; assert no merge occurs and bead does not silently reopen.

### DFA-005
- Title: Assignment persistence is best-effort, so DB write failure can orphan live work without recoverability
- Severity: high
- Confidence: confirmed
- Category: reliability
- Exact location: `pkg/dispatcher/dispatcher.go:3382-3449`, `pkg/dispatcher/dispatcher.go:1625-1628`
- Evidence from code:
  - `assignBead()` ignores `createAssignment()` errors.
  - Merge success ignores both `beads.Close()` and `completeAssignment()` errors.
  - The runtime relies on SQLite rows for restart recovery and counter persistence.
- Violated invariant: durable assignment state must be recorded before work proceeds.
- Concrete failure mode:
  - DB is locked or unavailable during assignment creation.
  - Worker still receives the assignment and proceeds.
  - Dispatcher later crashes.
  - Restart cannot reconstruct or clean up the attempt because the DB row never existed.
- Triggering conditions: transient SQLite write failure during assign/complete.
- Blast radius: restart recovery, stuck `in_progress` beads, leaked worktrees, lost attempt history.
- Failure type: silent.
- Why it would likely escape detection: failure is only visible under DB contention/fault injection; normal tests run with healthy local SQLite.
- Minimal fix:
  - Fail assignment if `createAssignment()` fails.
- Robust fix:
  - Make bead status update, assignment-row creation, and worker state transition an atomic claim protocol with rollback.
- Regression test:
  - Inject DB failure on `createAssignment`; assert worker is not assigned and bead status/worktree are rolled back.

### DFA-006
- Title: Semantic-memory runtime code is written against a schema that production never migrates to
- Severity: medium
- Confidence: confirmed
- Category: correctness
- Exact location: `pkg/protocol/schema.go:195-197`, `pkg/protocol/schema.go:225-235`, `pkg/memory/memory.go:153-156`, `pkg/memory/backfill.go:191-209`
- Evidence from code:
  - Production migration stores backfill state in `kv_store`, not in a table named `backfill_semantic_memory_state`.
  - `checkEmbedderModelMatch()` updates `backfill_semantic_memory_state`, which does not exist in production migrations.
  - Production `memory_chunks` schema is `(memory_id, chunk_idx, text, embedding)`.
  - `backfill.go` writes `INSERT ... (memory_id, embedding_dense)`, which does not match that schema.
- Violated invariant: semantic-memory code and schema must agree.
- Concrete failure mode:
  - Model-mismatch reset fails at runtime when it tries to update a nonexistent table.
  - Backfill cannot write chunk rows and logs/suppresses the failure.
- Triggering conditions: semantic model change, backfill execution.
- Blast radius: semantic search quality, backfill completeness, model-rotation safety.
- Failure type: mostly silent.
- Why it would likely escape detection: tests create a custom schema that masks the production mismatch.
- Minimal fix:
  - Align runtime SQL with the migrated schema.
- Robust fix:
  - Centralize semantic schema constants and generate all test fixtures from them.
- Regression test:
  - Run semantic-memory paths against a DB created only via `openStateDB()`/production migrations and assert chunk writes and model-reset succeed.

### DFA-007
- Title: Semantic-memory tests are materially untruthful and validate a schema that production does not use
- Severity: medium
- Confidence: confirmed
- Category: test gap
- Exact location: `pkg/memory/model_match_test.go:38-60`, `pkg/memory/backfill_test.go:22-37`
- Evidence from code:
  - `model_match_test` invents a `backfill_semantic_memory_state` table and a different `memory_chunks` shape (`chunk_index`, `content`, `embedding_dense`).
  - `backfill_test` never creates `memory_chunks`, so chunk-write failures are silently suppressed and unasserted.
- Violated invariant: tests should exercise production schema and failure modes.
- Concrete failure mode: runtime semantic bugs pass tests because the tests run against a different database shape.
- Triggering conditions: any semantic-memory change relying on these tests.
- Blast radius: semantic-memory reliability, future refactors, operator trust in tests.
- Failure type: silent.
- Why it would likely escape detection: the tests are white-box and self-consistent, but not production-consistent.
- Minimal fix:
  - Replace ad hoc semantic test schemas with production migration setup.
- Robust fix:
  - Add a shared helper that builds DBs exclusively through runtime migrations and forbid custom schema definitions in semantic tests.
- Regression test:
  - A smoke test that diffs semantic table/column names used in runtime SQL against the migrated schema.

### DFA-008
- Title: Empty project scope means “all projects” for FTS, but only `"oro"` for vec-index search
- Severity: medium
- Confidence: confirmed
- Category: correctness
- Exact location: `pkg/memory/memory.go:421-423`, `pkg/memory/memory.go:791-798`
- Evidence from code:
  - Inserts normalize empty project to `"oro"`.
  - `SetProject("")` is documented to expose all memories.
  - `vectorSearchViaIndex()` also normalizes empty scope to `"oro"` instead of querying all partitions.
- Violated invariant: empty project scope should be semantically consistent across search backends.
- Concrete failure mode:
  - FTS sees all projects.
  - ANN search sees only `"oro"`.
  - Hybrid search silently under-ranks or misses non-`oro` memories when project scope is empty.
- Triggering conditions: empty project scope with vec index enabled and multi-project data present.
- Blast radius: search relevance, memory retrieval correctness.
- Failure type: silent.
- Why it would likely escape detection: tests currently codify the `"oro"` normalization behavior rather than the higher-level API contract.
- Minimal fix:
  - Disable vec-index path when project scope is empty, or explicitly merge per-project ANN results.
- Robust fix:
  - Make project scoping an explicit typed policy (`single-project`, `all-projects`) carried through every search layer.
- Regression test:
  - Seed multiple projects, search with empty scope and vec index enabled, assert results include cross-project ANN hits consistent with FTS scope.

### DFA-009
- Title: Pending handoff consumption is non-atomic and can drop continuation state during worker registration races
- Severity: medium
- Confidence: high
- Category: concurrency
- Exact location: `pkg/dispatcher/worker_pool.go:103-149`
- Evidence from code:
  - `registerWorker()` deletes a pending handoff from the map before assignment is durably transferred.
  - It then unlocks for memory lookup.
  - On re-lock, if the worker disappeared or state changed, it returns without restoring the handoff.
  - `sendToWorker()` result is ignored in the handoff path.
- Violated invariant: a handoff should remain recoverable until a new worker has actually accepted it.
- Concrete failure mode:
  - Old worker handoffs.
  - New worker connects but disconnects during the unlock/send window.
  - Pending handoff entry is gone, yet the new assignment was never durably delivered.
- Triggering conditions: disconnect/reconnect timing during handoff registration.
- Blast radius: lost continuation context, stalled or restarted work from weaker context, confusing operator state.
- Failure type: intermittent.
- Why it would likely escape detection: requires a narrow race between registration, I/O outside the lock, and worker disconnect.
- Minimal fix:
  - Do not remove the pending handoff until `ASSIGN` is successfully sent.
- Robust fix:
  - Persist pending handoffs in SQLite and transition them through explicit states.
- Regression test:
  - Force disconnect during `registerWorker()`’s unlocked window; assert the pending handoff remains available for the next worker.

### DFA-010
- Title: Crash-time branch deletion and reassignment logic rely on developer discipline rather than enforced recovery semantics
- Severity: medium
- Confidence: confirmed
- Category: architecture
- Exact location: `pkg/dispatcher/dispatcher.go:897-914`, `pkg/dispatcher/dispatcher.go:4566-4618`
- Evidence from code:
  - Startup recovery is implemented as a sequence of best-effort destructive cleanups, not as a reconciler.
  - There is no durable owner/session record for worktrees, branches, or `in_progress` claims.
- Violated invariant: restart safety should be enforced by state model, not by assumption.
- Concrete failure mode: restart behavior depends on ad hoc branch naming and status conventions; subtle new features can easily violate recovery assumptions.
- Triggering conditions: crash, deploy, forced restart, partial DB/git divergence.
- Blast radius: future correctness regressions across assignment, merge, and recovery.
- Failure type: intermittent/systemic.
- Why it would likely escape detection: current tests validate many local cases but not end-to-end invariants across crash boundaries.
- Minimal fix:
  - Document startup destructive behavior clearly and gate it behind explicit recovery modes.
- Robust fix:
  - Introduce a durable recovery ledger tying bead attempt, worker, branch, worktree, and session ownership together.
- Regression test:
  - Crash-recovery integration suite covering committed/uncommitted work, human-owned beads, and restart under partial failures.

## Phase 7: Systemic Risks

- Cross-store state is mutated without transactional boundaries.
  - External bead state, SQLite, in-memory maps, and git are updated independently with many ignored errors.
- Correctness depends on conventions, not enforcement.
  - No unique constraint for active assignments.
  - No durable ownership model for `in_progress`.
  - No durable recovery model for worktree/branch state.
- Restart handling is destructive by default.
  - Startup cleanup assumes stale state is disposable, which is the opposite of a forensic-safe recovery posture.
- Tests overfit local helpers instead of production composition.
  - Semantic-memory tests are the clearest example, but the pattern is broader.
- Error handling is frequently best-effort on invariant-bearing writes.
  - This is acceptable for telemetry.
  - It is not acceptable for assignment creation/completion or recovery metadata.

## Phase 8: Priority and Execution

### Top 10 most dangerous issues

1. DFA-001 active-assignment duplication and counter corruption
2. DFA-002 restart discards in-flight branch/worktree progress
3. DFA-004 external closure merges canceled work
4. DFA-005 ignored assignment-persistence failures
5. DFA-003 startup reopens human-owned `in_progress` beads
6. DFA-009 non-atomic pending-handoff consumption
7. DFA-006 semantic-memory runtime/schema mismatch
8. DFA-008 empty-project vec-index inconsistency
9. DFA-010 recovery semantics are architectural guesswork
10. DFA-007 semantic-memory tests are untruthful

### Top 10 fixes by risk reduction per effort

1. Add a unique partial index for active assignments and close old rows before reassignment.
2. Stop unconditional startup deletion of `agent/*` branches.
3. Reopen only dispatcher-owned `in_progress` beads on startup.
4. Treat external close as cancellation unless explicitly marked as merged elsewhere.
5. Fail assignment when `createAssignment()` fails.
6. Make `completeAssignment()` target a specific assignment attempt, not every active row for the bead.
7. Align semantic-memory SQL with the migrated schema.
8. Replace semantic test schemas with production migration helpers.
9. Persist pending handoffs durably or at least delete them only after successful reassignment.
10. Add crash-recovery integration tests spanning DB/git/bead state.

### Most likely to cause silent data corruption

- DFA-001 duplicate active assignments
- DFA-002 startup branch/worktree discard
- DFA-004 external-close merge/reopen behavior
- DFA-005 ignored assignment write failures
- DFA-008 hybrid-search scope inconsistency

### Most likely to cause production incidents

- DFA-001 duplicate assignments under timeout/retry
- DFA-002 restart after crash losing active work
- DFA-003 human-owned bead reassigned after restart
- DFA-004 canceled bead merged anyway
- DFA-009 handoff race dropping continuation

### Most likely to cause intermittent or hard-to-reproduce bugs

- DFA-001 timeout/reassignment corruption
- DFA-009 registerWorker handoff race
- DFA-005 transient SQLite failure during assignment creation/completion
- DFA-008 empty-scope vec-index mismatch

## Test Suite Truthfulness Audit Summary

Highest-risk gaps:

- No end-to-end test for restart with committed-but-unmerged `agent/*` work.
- No test asserting active-assignment uniqueness across timeout/reassignment.
- No test for human-owned `in_progress` survival across startup.
- No test for “external close means cancel, not merge”.
- Semantic-memory tests validate a fabricated schema instead of production migrations.
