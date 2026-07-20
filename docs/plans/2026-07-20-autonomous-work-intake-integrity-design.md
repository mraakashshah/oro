# Autonomous Work Intake Integrity

**Date:** 2026-07-20

**Status:** Draft — consultation pending

**Scope:** Worker discoveries, blocker proposals, task admission, dependency
authorization, quality-gate evidence, and autonomous recovery

## 1. Problem

An execution worker assigned to `oro-bym3` inspected a stale tmux pane belonging
to `.worktrees/oro-home-logs`, treated that pane's old dead-export failure as
current evidence, created malformed P0 bug `oro-8kex`, and attached it as a
blocking dependency of its own assignment. The actual dispatcher-owned quality
gate completed 31 seconds later, reported `dead exports` passing, and found only
task-local lint failures.

The incident exposed five independent defects:

1. `pkg/worker/prompt.go:appendFailureSection` teaches workers to create bugs as
   P0, shows task creation without acceptance criteria, and tells a worker to
   attach a blocker directly to its current task.
2. `pkg/agentruntime/codex/codex.go:WorkerSpawner.SpawnWithReasoning` does not
   add `ORO_WORKER=1`; the Claude-specific environment does. The existing
   `guardWorkerDepAddSelf` therefore fails open for Codex workers.
3. `cmd/oro/cmd_bead.go:runBeadCreate` accepts an open executable bead with no
   task contract. The native store also accepts that representation.
4. Worker task mutations are authorized by ambient environment variables at a
   CLI edge rather than by dispatcher-owned assignment state.
5. Unbound terminal output can be asserted as evidence without proving its
   assignment, worktree, branch, HEAD, command, or freshness.

The current dispatcher does prevent a bead with empty acceptance criteria from
being assigned (`checkBeadReady`), but that late guard does not prevent queue
pollution, false dependency edges, wasted review/retry work, or priority
inflation. Existing tasks `oro-vcw9` and `oro-emz2` address review behavior after
a dependency appears; they do not establish whether the dependency was valid.

## 2. Goal

Make work discovery an autonomous, evidence-bound pipeline:

- workers report observations rather than directly mutating executable work;
- Oro validates observation provenance against live assignment state;
- deterministic task-local failures return to the same task;
- genuine blockers become deduplicated, beadcraft-complete executable tasks;
- false or stale observations are rejected automatically;
- bounded repair and triage retries resolve normal cases without an operator;
- exhaustion quarantines only the affected task while unrelated throughput
  continues;
- human intervention is reserved for ambiguous, destructive, or policy-level
  decisions after autonomous options are exhausted.

## 3. Non-goals

- Defending against a malicious worker with arbitrary filesystem and process
  access. The controls prevent accidental, stale, confused-deputy, and
  cross-assignment mutations; they are not a hostile sandbox.
- Replacing the existing QG fingerprint/classification store.
- Preventing humans or trusted ops roles from creating draft tasks.
- Making all historical beads satisfy the new contract in one migration.
- Treating every defect as P0.
- Globally pausing the factory for a single malformed proposal.

## 4. Safety and Autonomy Invariants

1. An execution worker cannot create an executable bead or attach a dependency
   to its assigned bead.
2. Only a validated executable bead can enter the ready queue or become a
   blocking dependency.
3. Evidence used for automated mutation is bound to one project, assignment,
   worker, bead, worktree, branch, HEAD, command, and bounded time interval.
4. Dispatcher-owned QG output is authoritative over worker-observed terminal
   output.
5. A repeated observation creates at most one proposal and one executable bead
   per stable fingerprint and scope.
6. Rejected proposals do not change task dependencies or priority.
7. Accepted proposals are created with complete `Test`, `Cmd`, `Assert`,
   `Read`, estimate, scope, provenance, and severity rationale.
8. Proposal triage and repair have bounded attempts and durable state, and
   survive dispatcher restart.
9. Exhaustion quarantines the affected assignment only; unrelated ready work
   continues.
10. Every autonomous transition is idempotent and evented.

## 5. Existing Mechanisms to Reuse

### 5.1 Dispatcher admission

`pkg/dispatcher/dispatcher.go:checkBeadReady` already skips empty acceptance,
non-TDD operational acceptance, and oversized leaf tasks. This becomes the last
line of defense using the same central validator as creation and dependency
attachment.

### 5.2 QG incidents

`evaluateQGFailure`, `recordQGFailureIncident`, and
`ensureQGIncidentBead` already normalize fingerprints, deduplicate incidents,
and create structured infrastructure beads. Worker-reported QG failures route
to this pipeline only after the dispatcher runs the gate; workers do not create
parallel QG blocker tasks.

### 5.3 Durable quarantine and bounded recovery

Recovery quarantines and durable review checkpoints already demonstrate
restart-safe state, bounded attempts, reminders, and scoped freezing. Work
proposal exhaustion reuses those policies but must not make global health
unsafe merely because one proposal is awaiting repair.

### 5.4 Review blocker checks

`oro-vcw9` and `oro-emz2` remain required: once an accepted blocker edge is
installed, review admission, approval, and retry must re-read readiness before
continuing. This design depends on those tasks rather than duplicating them.

## 6. Architecture

### 6.1 Uniform worker execution context

Replace runtime-specific worker environment assembly with one helper:

```go
type WorkerExecutionContext struct {
    WorkerID     string
    BeadID       string
    AssignmentID int64
    Project      string
    Worktree     string
    TargetBranch string
}

func WorkerEnv(base []string, ctx WorkerExecutionContext) ([]string, error)
```

Both Claude and Codex spawners receive the resulting explicit environment.
The helper sets `ORO_WORKER=1`, identity fields, normalized `PWD`, isolated temp
roots, and shared cache variables. It rejects missing assignment identity.
Spawner APIs must carry `WorkerExecutionContext`; they must not read a
process-global `ORO_WORKER_BEAD_ID`, which is vulnerable to assignment changes
and runtime parity drift.

The immediate CLI guard fails closed when any worker identity field is present,
even if `ORO_WORKER` is absent. This protects mixed-version launches while the
typed spawner boundary lands.

### 6.2 Actor capabilities

Worker task mutation authority is role-scoped:

| Role | Create executable bead | Add blocking edge | Submit proposal |
|---|---:|---:|---:|
| execution worker | no | no | yes |
| epic decomposition worker | validated children only | parent-to-child only | yes |
| dispatcher | generated templates only | accepted proposal/QG/recovery only | yes |
| ops taskcraft reviewer | validated task only | accepted proposal only | yes |
| human CLI | yes, through contract validation | yes | yes |

The dispatcher issues a short-lived capability bound to assignment ID, actor
role, project, and allowed actions. Worker mutations go through dispatcher IPC;
the CLI does not write the bead store directly when worker identity is present.
Capabilities are checked against live assignment state and consumed
idempotently. Environment variables carry the opaque capability but are not
the authority by themselves.

### 6.3 Durable work proposals

A worker reports a discovery through `MsgWorkProposal`:

```go
type WorkProposalPayload struct {
    ProposalID       string
    AssignmentID     int64
    WorkerID         string
    BeadID           string
    EvidenceRunID    string
    Fingerprint      string
    Kind             string // task_local, prerequisite, systemic, external
    Summary          string
    SuggestedTitle   string
    SuggestedType    string
    SuggestedPriority int
}
```

Durable `work_proposals` state records identity, evidence, decision, repair
attempts, executable bead ID, and timestamps. A unique key on
`(assignment_id, fingerprint)` prevents duplicates. States are:

```text
pending -> validating -> rejected
                      -> repairing -> accepted -> materialized
                                  -> quarantined
```

No proposal is returned by `Store.Ready`, and no proposal blocks a bead.

### 6.4 Evidence runs

Commands used as mutation evidence run through an Oro-owned wrapper:

```go
type EvidenceManifest struct {
    RunID          string
    Project        string
    AssignmentID   int64
    WorkerID       string
    BeadID         string
    Worktree       string
    Branch         string
    HeadSHA        string
    Command        []string
    StartedAt      time.Time
    FinishedAt     time.Time
    ExitCode       int
    OutputHash     string
    OutputExcerpt  string
}
```

The wrapper resolves the worktree and Git identity before execution, records
the bounded result in dispatcher state, and returns `RunID`. Proposal validation
re-resolves the live assignment and rejects mismatched project, worker, bead,
worktree, branch, HEAD, future timestamp, expired evidence, absent run, or hash.
Output excerpts are bounded; full logs remain in the existing worker/QG log
surface.

tmux panes and arbitrary files are never mutation evidence. A worker may inspect
them diagnostically, but a proposal citing them without a matching evidence run
is rejected. Dispatcher-run QG creates evidence directly and remains
authoritative.

### 6.5 Central executable task contract

Add one pure validator shared by bead creation, update-to-open, dependency
attachment, dispatcher admission, generated QG tasks, and ops materialization:

```go
type ContractVersion int

type TaskContractResult struct {
    Valid  bool
    Code   string
    Fields ParsedAcceptance
}

func ValidateExecutableTask(bead protocol.BeadDetail, version ContractVersion) TaskContractResult
```

For contract version 2, executable task/bug beads require:

- non-empty, single-purpose title;
- `Test:`, `Cmd:`, `Assert:`, and `Read:`;
- estimate from 1 through 7 minutes;
- valid task/bug type and priority 0 through 4;
- provenance metadata for autonomously generated tasks;
- `Signature:` when the task adds a callable API;
- `Edges:` for non-trivial boundary or error behavior.

The last two are required by taskcraft review rather than inferred solely with
string matching. Epics use a separate contract requiring a main-branch
machine-verifiable `Cmd:` and `Assert:`.

Incomplete human or automated discoveries are proposals, not open beads. New
CLI creation defaults executable tasks to contract v2 and rejects invalid
input. A deliberate human `--draft` path can preserve incomplete notes without
making them ready. Historical contract-v0 beads continue through the existing
admission behavior and are repaired lazily; there is no flag-day migration.

### 6.6 Autonomous triage controller

The dispatcher runs a restart-safe proposal controller:

1. **Validate provenance.** Invalid or stale evidence is rejected and the
   originating worker receives structured feedback.
2. **Classify.** Reuse QG fingerprinting where applicable:
   - deterministic task-local failure -> retry original task;
   - authoritative repeated/systemic failure -> reuse QG incident;
   - missing prerequisite -> taskcraft repair;
   - unsupported or ambiguous -> bounded ops consultation.
3. **Taskcraft repair.** A one-shot ops reviewer receives the evidence manifest,
   current source task, related epic, current branch, and relevant code index.
   It returns a typed decision: reject, retry-original, or materialize, with a
   complete task contract.
4. **Revalidate.** Oro runs the central validator and scope/provenance checks on
   the returned contract. Invalid output is retried with exact validation
   errors; it is never stored as executable work.
5. **Materialize atomically.** Create/reuse the executable bead, add the
   dependency, transition the current assignment, and append events in one
   transaction or compensate without leaving a half-edge.
6. **Resume throughput.** Rejected/task-local proposals resume the original
   assignment. Accepted blockers preserve current work, reopen the task as
   blocked, and release the worker. Other ready tasks continue.
7. **Exhaustion.** After two repair attempts and one independent classification
   attempt, quarantine only the proposal and source assignment. A background
   janitor retries when evidence, branch, task graph, or relevant commits change.
   Operator escalation is emitted after the retry budget, but the factory keeps
   processing unrelated tasks.

### 6.7 Severity policy

Remove “bugs are always P0.” Default autonomous bug priority is P2.

- P0: demonstrated data loss, security boundary failure, factory-wide safety
  failure, or total throughput stop across unrelated tasks.
- P1: blocks an epic or repeatedly affects more than one assignment.
- P2: blocks one task with a bounded workaround or repair path.
- P3/P4: non-blocking follow-up or cleanup.

Promotion requires machine-readable evidence and a recorded rationale. A
worker's suggested priority is advisory only.

## 7. State Transitions and Failure Handling

### 7.1 Proposal rejected

- Persist decision and reason.
- Add no bead and no dependency.
- If the worker remains connected, send retry feedback on the same assignment.
- Otherwise preserve committed work and requeue through normal handoff recovery.

### 7.2 Proposal accepted

- Deduplicate by scope and fingerprint.
- Materialize a v2-valid bead.
- Add the dependency only after materialization succeeds.
- Recheck the source task's active assignment before changing review/retry state.
- Preserve worktree/branch, complete the assignment as blocked, reopen source,
  and release the worker.
- Existing `oro-vcw9`/`oro-emz2` behavior prevents review or merge races.

### 7.3 Dispatcher crash

- Proposal and evidence are durable before any edge is added.
- Startup resumes `pending`, `validating`, and `repairing` proposals using
  idempotency keys.
- A `materialized` proposal reconciles bead existence and edge existence rather
  than creating duplicates.

### 7.4 Ops unavailable

- Retry with exponential backoff within the proposal budget.
- Preserve the source assignment without occupying a worker process.
- Continue unrelated assignment.
- Quarantine only after budget exhaustion.

### 7.5 Store or transaction failure

- Fail closed: no blocking edge may exist without a valid target bead.
- A created bead with no edge is safe and deduplicated on retry.
- Emit a repairable reconciliation event.

## 8. Observability

Events:

```text
work_proposal_received
work_proposal_evidence_rejected
work_proposal_classified
work_proposal_repair_started
work_proposal_repair_rejected
work_proposal_materialized
work_proposal_dependency_added
work_proposal_original_resumed
work_proposal_quarantined
work_proposal_reconciled
```

`oro status`, `oro health`, and `oro throughput` expose counts for pending,
repairing, accepted, rejected, and quarantined proposals. Pending proposals are
not health-critical. A repeatedly failing fingerprint is a warning. A
quarantined proposal is scoped warning/critical according to severity but does
not globally disable monitor actions unrelated to that bead.

## 9. Compatibility and Migration

1. Introduce contract parsing and validation without changing existing v0 task
   behavior.
2. Fix Codex/Claude worker environment parity and fail-closed guards.
3. Replace worker prompt examples with proposal commands.
4. Add proposal/evidence protocol and durable stores.
5. Route execution-worker mutations through the proposal controller.
6. Enable strict v2 validation for new executable CLI/dispatcher tasks.
7. Add lazy janitor repair for historical malformed open tasks.
8. Remove direct execution-worker task mutation after production evidence shows
   proposal routing is healthy.

Rollback disables proposal materialization while retaining records, restores
the previous prompt, and keeps dispatcher admission checks. Schema additions
are additive; no destructive migration is required.

## 10. Testing

### 10.1 Unit contracts

- Both runtime spawners receive identical worker identity fields.
- Missing assignment identity fails closed.
- v2 task validation rejects every missing field and invalid estimate.
- Draft and legacy-v0 behavior is explicit.
- dependency authorization rejects execution-worker self/cross-epic edges.
- evidence validation rejects every identity mismatch and stale timestamp.
- fingerprint deduplication is stable.

### 10.2 State-machine tests

- reject -> resume original;
- task-local -> retry original without new bead;
- accepted -> valid bead plus edge plus worker release;
- crash between bead create and edge -> reconcile exactly once;
- ops timeout -> bounded retry -> scoped quarantine;
- branch/HEAD change -> stale proposal revalidated or rejected;
- two workers report same systemic issue -> one incident bead.

### 10.3 Production-path regression

`TestAutonomousWorkIntakeIntegrityEndToEnd` starts the real dispatcher with a
Codex worker assignment for bead A and seeds stale terminal evidence from
worktree B. It proves:

1. worker identity reaches the Codex subprocess;
2. direct task creation and self-dependency mutation are denied;
3. a proposal using B's evidence is rejected;
4. no bead or dependency is created and A resumes;
5. a dispatcher-owned systemic QG failure for A creates one v2-valid incident;
6. an accepted prerequisite proposal materializes one v2-valid blocker and
   atomically blocks A;
7. review/merge does not proceed after the edge;
8. the worker is released and an unrelated ready bead is assigned;
9. restart does not duplicate proposals, beads, or edges.

Epic acceptance runs against `main`:

```text
Cmd: bash -euo pipefail -c 'test "$(git branch --show-current)" = main && go test ./pkg/... ./cmd/oro/... -run "^TestAutonomousWorkIntakeIntegrityEndToEnd$" -count=1 -timeout=180s && ./scripts/quality_gate.sh'
Assert: the named production-path test passes on main and the full quality gate exits 0
```

## 11. Delivery Structure

The work decomposes into independently mergeable epics:

1. **Worker identity parity** — typed execution context and fail-closed runtime
   propagation.
2. **Executable task contracts** — shared validator, draft path, creation/update/
   admission enforcement.
3. **Evidence-bound proposals** — protocol, evidence manifests, durable proposal
   store, validation.
4. **Autonomous proposal controller** — classification, taskcraft repair,
   materialization, retry, quarantine, reconciliation.
5. **Dependency authority** — dispatcher-only worker edges and integration with
   `oro-vcw9`/`oro-emz2` review readiness.
6. **Observability and janitor repair** — status/health/throughput plus legacy
   contract repair.
7. **Production-path acceptance** — real Codex stale-worktree regression and
   restart/idempotency proof.

Epics 1 and 2 can land first and prevent recurrence before the proposal
controller is complete. Epics 3 through 6 may merge independently behind a
disabled materialization flag. Epic 7 enables the production path and removes
the legacy worker prompt instructions.

## 12. Premortem

```yaml
premortem:
  mode: deep
  context: autonomous work intake integrity
  tigers:
    - risk: syntactically valid but factually false tasks are materialized
      severity: high
      mitigation_checked: contract validation alone is insufficient; evidence identity and independent taskcraft review are required
    - risk: proposal triage becomes an operator queue and stalls throughput
      severity: high
      mitigation_checked: bounded autonomous repair, reject/resume, scoped quarantine, janitor retry, and unrelated dispatch are specified
    - risk: direct SQLite CLI mutation bypasses actor policy
      severity: high
      mitigation_checked: worker mutations move through dispatcher IPC with assignment-bound capabilities; env guards are only compatibility defense
    - risk: crash leaves a blocker edge without a valid target or duplicates work
      severity: high
      mitigation_checked: materialization is transactional or compensating, fingerprinted, and startup-reconciled
    - risk: strict validation strands historical tasks
      severity: medium
      mitigation_checked: contract versioning preserves v0 and janitor repairs lazily
  elephants:
    - risk: workers currently have broad host authority, so this is integrity control rather than a hostile security boundary
  paper_tigers:
    - risk: requiring taskcraft makes genuine blocker handling too slow
      reason: taskcraft is a bounded one-shot only after evidence validation; task-local and QG failures use existing fast paths
```

## 13. Assumptions for Consultation

- A worker observation may be wrong; dispatcher-owned assignment and QG state
  are authoritative.
- Normal recovery must not require a human.
- It is acceptable to quarantine one task after bounded autonomous attempts as
  long as unrelated work continues.
- Human-created incomplete notes may exist, but not in the executable queue.
- Execution workers do not need direct task/dependency mutation once proposal
  submission exists.
