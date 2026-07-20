# Autonomous Work Intake Integrity

**Date:** 2026-07-20

**Status:** Draft — adversarial R3 fixes applied, re-review pending

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
    Generation   int64
    ActorRole    string
    Capability   string
    Project      string
    SocketPath   string
    Worktree     string
    TargetBranch string
}

func WorkerEnv(base []string, ctx WorkerExecutionContext) ([]string, error)
```

The production transport is explicit and end-to-end:

```text
assignBead/createAssignment
  -> issueAssignmentCapability
  -> buildAssignPayload (AssignmentID, Generation, ActorRole, Capability)
  -> protocol.AssignPayload
  -> worker.handleAssign/resetForNewAssignment
  -> RuntimeStreamingSpawner.Spawn(..., WorkerExecutionContext)
  -> ClaudeSpawner or Codex WorkerSpawner
  -> WorkerEnv
```

`cmd/oro/cmd_worker_launch.go:ExecWorkerSpawner.SpawnWorker` and
`cmd/oro/cmd_worker.go:runWorker` are part of this chain. `runWorker` copies its
authoritative `--socket` value into worker state, and every assignment builds
`WorkerExecutionContext.SocketPath` from that state. `WorkerEnv` sets the exact
`ORO_SOCKET_PATH`; neither managed nor externally launched workers discover a
socket by scanning the filesystem.

This migration owns every initial, retry, review-recovery, and handoff assignment
payload call site; mocks cannot retain the old spawner signature. Capability
issuance happens only after the assignment row is durably active. Failure to
build or send the payload revokes the capability before reopening the bead.

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

The dispatcher issues a random 256-bit capability bound to assignment ID,
assignment generation, actor role, project, and allowed actions. Only its hash
is stored in `assignment_capabilities`. `AssignPayload` carries the initial raw
value to the worker, which writes a per-assignment credential document to a
mode-0600 file by atomic rename. The stable file path, not the bearer token, is
injected into the agent environment as `ORO_CAPABILITY_FILE`; every later
`oro evidence` or proposal CLI invocation opens and rereads the file. The file
contains assignment ID, generation, capability ID, raw token, and expiry and is
removed on assignment termination. It expires after 20 minutes or when the
assignment generation becomes terminal/replaced, whichever occurs first.

The capability is reusable only for its narrow action set, while every request
has a caller-generated nonce. `(capability_id, nonce)` is unique and stores the
completed response, making retries idempotent and replay with different content
an error. Dispatcher restart reloads capability hashes and consumed nonces.
Completion, requeue, disconnect recovery, or worker replacement revokes every
capability for the old generation.

Capability refresh has an explicit fake-clock-driven protocol. Five minutes
before expiry, the dispatcher persists a pending replacement hash and sends
`MsgCapabilityRefresh` containing assignment ID, generation, replacement raw
token, and expiry. `worker.handleMessage` verifies the assignment, atomically
replaces the credential file, rereads it, and returns
`MsgCapabilityRefreshACK` with the replacement capability ID. The dispatcher
durably records the ACK, then revokes the predecessor.

Raw pending tokens are never persisted. On restart before ACK, the dispatcher
atomically marks the unreachable pending token superseded, mints and persists a
new pending hash, and sends the new raw token; the predecessor remains valid
until the new ACK or its original expiry. Restart after ACK observes the durable
revocation. Crash-point tests cover before pending-hash commit, after commit,
after send/file replace, and after ACK. If downtime crosses predecessor expiry,
the worker installs the newly minted pending credential before issuing another
request. This protocol never reconstructs a raw value from a hash and never
revokes both usable tokens simultaneously.

Worker mutations go through dispatcher IPC; the CLI does not write the bead
store directly when any worker identity field is present. Capabilities are
checked against live assignment state. Environment variables transport the
opaque value but are not authority by themselves.

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
    ScopeHint        string // advisory; controller derives canonical ScopeKey
    Kind             string // task_local, prerequisite, systemic, external
    Summary          string
    SuggestedTitle   string
    SuggestedType    string
    SuggestedPriority int
}
```

Durable `work_proposals` state records identity, evidence, decision, repair
attempts, canonical scope key, executable bead ID, and timestamps. Provisional
submission is idempotent on `(assignment_id, client_proposal_id)` and does not
collapse by fingerprint before scope is known. The controller may merge
provisional rows only after canonical scope derivation. Materialization is
globally deduplicated on `(project, target_branch, scope_key, fingerprint)`.
The controller derives `scope_key` from authoritative task ancestry plus the
taskcraft review's canonical prerequisite identity; the worker's `ScopeHint`
is never used as a key without validation. QG proposals use the existing QG
incident scope and fingerprint. States are:

```text
pending -> validating -> rejected
                      -> repairing -> accepted -> materialized
                                  -> quarantined
```

No proposal is returned by `Store.Ready`, and no proposal blocks a bead.

### 6.4 Agent-facing evidence and proposal commands

The real producer entry points are:

```text
oro evidence run --kind diagnostic --timeout 2m -- <argv...>
oro task propose-blocker --evidence-run <run-id> --kind <kind> \
  --summary <text> --title <advisory-title> --priority <advisory-priority>
```

Both commands require `ORO_SOCKET_PATH`, assignment identity, and the opaque
capability. They use the existing UDS directive client framing and add exact
protocol messages:

```go
MsgEvidenceRunRequest  // capability, nonce, kind, argv, timeout
MsgEvidenceRunResult   // run ID, exit, bounded output, manifest hash, error
MsgWorkProposal        // capability, nonce, WorkProposalPayload
MsgWorkProposalResult  // proposal ID, state, decision/retry feedback, error
```

`cmd/oro` registers both commands. `protocol.Message` gains typed cases. In
`dispatcher.handleConn`, evidence and proposal requests are recognized and
handled as short-lived request/response traffic before `registerWorker`, just
like directives: validate the claimed identity/capability against the tracked
worker, send the result on the short-lived connection, then close only that
connection. They never call `registerWorker`, replace `trackedWorker.conn`, or
run `connCloseCleanup` for the assignment worker. The CLI
discovers the project socket from the injected exact path; it never scans `/tmp`
or connects to another project's socket. A missing socket, mismatched response
identity, timeout, or dispatcher disconnect fails closed and prints structured
JSON suitable for the agent prompt.

### 6.5 Evidence runs

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

The dispatcher, not the agent subprocess, executes the requested argv in the
assignment's stored worktree after capability validation. Caller cwd is ignored.
The wrapper resolves the worktree and Git identity before execution, records
the bounded result in dispatcher state, and returns `RunID`. Proposal validation
re-resolves the live assignment and rejects mismatched project, worker, bead,
worktree, branch, HEAD, future timestamp, expired evidence, absent run, or hash.
Default timeout is two minutes and the maximum is ten minutes. Argv encoding is
bounded to 4 KiB, retained output to 32 KiB, and the event excerpt to 1200 bytes;
the full streamed output goes to the assignment log and is addressed by hash.
Cancellation records a terminal canceled run. Startup marks abandoned
`running` evidence as interrupted; interrupted or unfinished evidence never
validates and may be rerun with a new nonce.

tmux panes and arbitrary files are never mutation evidence. A worker may inspect
them diagnostically, but a proposal citing them without a matching evidence run
is rejected. Dispatcher-run QG creates evidence directly and remains
authoritative.

### 6.6 Central executable task contract

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

Contract state is durable, not inferred after reload. Add
`beads.contract_version INTEGER NOT NULL DEFAULT 0` and
`beads.draft INTEGER NOT NULL DEFAULT 0`, carry both through `protocol.Bead`,
`BeadDetail`, `CreateParams`, `UpdateParams`, SQLite, shadow store, read
transactions, export, and compatibility migrations. `Store.Ready` and its
read-transaction equivalent exclude `draft=1` before dispatcher filtering.
Every validator call derives its version from the persisted field.

Incomplete automated discoveries are proposals, not beads. New human CLI
creation defaults executable tasks to contract v2 and rejects invalid input. A
deliberate human `--draft` path persists `draft=1` without making the bead ready.
`oro task update` can edit every publication-required field while a bead remains
draft: title, description, acceptance, estimate, type, priority, parent, owner,
and notes. This extends `UpdateParams` and the SQLite, shadow, fake, protocol,
and export boundaries. A production CLI test creates an incomplete draft,
repairs fields over multiple updates, publishes it, and observes it in `Ready`.
Update-to-open, reopen, and dependency attachment revalidate the stored version.
Historical contract-v0 beads continue through the existing admission behavior
and are repaired lazily; there is no flag-day migration.

### 6.7 Autonomous triage controller

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
   complete task contract. `pkg/workproposal.ScopeKeyV1` is the typed canonical
   prerequisite identity: project plus normalized kind, package/component,
   external subject, and invariant. `NormalizeScopeV1` trims and case-folds
   enumerated components, canonicalizes repository-relative paths, rejects
   unknown fields, and serializes a versioned deterministic key. Reviewer prose
   is never itself a dedupe key; equivalence fixtures pin normalization.
4. **Revalidate.** Oro runs the central validator and scope/provenance checks on
   the returned contract. Invalid output is retried with exact validation
   errors; it is never stored as executable work.
5. **Materialize atomically.** `Store.MaterializeWorkProposal` owns one SQLite
   transaction across: compare live source assignment/generation; reserve the
   global materialization key; create or reload the v2 bead; add or verify the
   edge; transition proposal state; append the transition event; and mark the
   assignment blocked. SQLite, shadow, fake, and read-transaction test stores
   implement the boundary. A stale source assignment aborts before writes.
6. **Resume throughput.** Rejected/task-local proposals resume the original
   assignment. Accepted blockers preserve current work, reopen the task as
   blocked, and release the worker. Other ready tasks continue.
7. **Exhaustion.** After two repair attempts and one independent classification
   attempt, transition the proposal to `quarantined` and record its source bead
   in `proposal_quarantine_beads`. This is separate from
   `recovery_quarantines`: assignment filtering excludes only that bead, health
   reports a scoped warning, and monitor actions for unrelated beads remain
   enabled. A background janitor retries when evidence, branch, task graph, or
   relevant commits change. Operator escalation is emitted after the retry
   budget, but the factory keeps processing unrelated tasks.

### 6.8 Worker mutation gateway

Every mutable `oro task` entry point is inventoried. When worker identity is
present, create, update, reopen, close, delete, defer, undefer, note add,
dependency add, and dependency remove fail closed unless the dispatcher-issued capability
explicitly authorizes the operation through IPC. Execution workers have only
evidence/proposal actions. Epic decomposition capabilities allow only validated
child creation under the assigned epic and parent-to-child edges; they cannot
mutate unrelated beads or create cross-epic dependencies.

Human/ops CLI calls still use the store but pass central contract validation.
Generated QG, audit, janitor, recovery, and cleanliness producers use named
constructors declaring contract version and executable versus non-executable
classification; none bypasses the validator accidentally.

The gateway test derives the complete mutable leaf-command set by walking the
real `newTaskCmdWithStore` Cobra tree and compares it with an explicit policy
table. Adding a future mutable command without a deny-or-route policy fails the
test. Human draft publication uses `oro task publish <id>`, which validates the
stored v2 contract and atomically clears `draft=1`; invalid drafts remain
non-ready.

### 6.9 Severity policy

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
- Capability hashes, expiry, revocation, and consumed nonces reload before IPC
  admission opens. Exact request replay returns the stored response; the same
  nonce with different content is rejected.

### 7.4 Ops unavailable

- Retry with exponential backoff within the proposal budget.
- Preserve the source assignment without occupying a worker process.
- Continue unrelated assignment.
- Quarantine only after budget exhaustion.

### 7.5 Store or transaction failure

- Fail closed: no blocking edge may exist without a valid target bead.
- `MaterializeWorkProposal` rolls back bead, edge, proposal, assignment, and
  event writes together.
- Commit uncertainty leaves the proposal in `validating`; startup reconciliation
  queries the global materialization key and either completes the same
  transition or retries it.
- Concurrent assignment replacement fails the generation comparison before
  any mutation.
- Emit a repairable reconciliation event for commit uncertainty.

### 7.6 Transition and event atomicity

Each proposal transition uses a monotonically increasing generation and a
unique event key `(proposal_id, generation, event_type)`. State update and event
inserts occur in the same transaction. The required event set is explicit:

| Transition | Exact event set |
|---|---|
| receive | `work_proposal_received` |
| evidence rejection | `work_proposal_evidence_rejected` |
| classify | `work_proposal_classified` |
| start/reject repair | `work_proposal_repair_started` or `work_proposal_repair_rejected` |
| materialize and attach | `work_proposal_materialized`, `work_proposal_dependency_added` |
| resume original | `work_proposal_original_resumed` |
| quarantine | `work_proposal_quarantined` |
| reconcile uncertain commit | `work_proposal_reconciled` plus any missing materialize/attach event |

Each listed event occurs exactly once for its transition generation. Replaying
a completed request returns the stored result without emitting another event;
invalid transitions change neither state nor events.

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
not globally disable monitor actions unrelated to that bead. Proposal
quarantines have separate metrics and findings from recovery quarantines and
never enter `recoveryQuarantineAssignmentScope`.

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
- Initial, retry, review-recovery, and handoff payloads preserve assignment ID,
  generation, role, and capability through a real subprocess environment.
- Managed and external `oro worker launch` paths inject the exact socket into
  the spawned agent, which successfully performs an evidence request without
  changing the tracked worker connection.
- Missing assignment identity fails closed.
- Expired, revoked, wrong-role, stale-generation, replayed-different-content,
  and consumed capability requests fail closed; exact replay is idempotent.
- A fake-clock assignment crossing 20 minutes refreshes its mode-0600 credential
  file; the same already-running real agent shim launches a CLI after expiry and
  succeeds with the replacement. Restart at every refresh crash point safely
  supersedes pending tokens or observes the durable ACK as specified.
- v2 task validation rejects every missing field and invalid estimate.
- Contract version and draft state round-trip through create, reload, update,
  reopen, dependency attach, Ready, dispatcher admission, shadow store, and
  export; drafts never appear in Ready.
- dependency authorization rejects execution-worker self/cross-epic edges.
- A Cobra-tree table test proves every mutable task command—including defer,
  undefer, and note add—is denied or capability-routed for execution workers.
- evidence validation rejects every identity mismatch and stale timestamp.
- Evidence timeout, cancellation, oversized argv/output, dispatcher crash, and
  unfinished runs are terminal and never validate.
- Fingerprint plus canonical scope deduplicates across assignments.
- Two distinct canonical prerequisite scopes sharing one fingerprint within one
  assignment remain distinct; same-scope repeats collapse at materialization.
- Every proposal transition emits exactly its table-defined durable event set
  once under replay.
- A draft created without publication-required fields is edited in stages via
  the real CLI, published atomically, and then appears in `Ready`.

### 10.2 State-machine tests

- reject -> resume original;
- task-local -> retry original without new bead;
- accepted -> valid bead plus edge plus worker release;
- crash between bead create and edge -> reconcile exactly once;
- injected failure after every materialization write -> full rollback or one
  idempotent startup completion;
- ops timeout -> bounded retry -> scoped quarantine;
- scoped proposal quarantine -> source excluded, unrelated assignment continues,
  recovery global-freeze metrics unchanged;
- branch/HEAD change -> stale proposal revalidated or rejected;
- two workers report same systemic issue -> one incident bead.
- two assignments report the same prerequisite -> one blocker bead, one
  materialization key, two source edges, and both source beads blocked.
- one assignment reports two different prerequisite scopes with the same
  fingerprint -> two materializations.
- proposal/evidence CLI disconnect -> short-lived connection closes while the
  tracked worker connection, assignment, and bead status remain unchanged.

### 10.3 Production-path regression

`cmd/oro/autonomous_work_intake_integrity_e2e_test.go` owns
`TestAutonomousWorkIntakeIntegrityEndToEnd`. It starts the real dispatcher with
a real `cmd/oro` CLI and Codex subprocess shim for bead A, plus stale terminal
evidence from worktree B. The production-path acceptance epic owns all fixtures
and the full CLI -> UDS -> protocol -> dispatcher -> controller chain. It proves:

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
10. each durable transition event occurs exactly once.
11. the supported external `oro worker launch` path carries socket and capability
    through a real evidence/proposal round trip.
12. every mutable task CLI leaf is denied or routed under execution-worker
    identity.
13. a fake clock crosses capability expiry and the same live agent shim uses
    the atomically refreshed credential file for a successful proposal.
14. same-fingerprint proposals with two canonical scopes both materialize.
15. two sources sharing one global blocker receive two edges and both become
    blocked.
16. an incomplete draft is repaired through real CLI updates, published, and
    becomes ready.

Epic acceptance runs against `main`:

```text
Cmd: bash -euo pipefail -c 'test "$(git branch --show-current)" = main && test "$(go test ./cmd/oro -list "^TestAutonomousWorkIntakeIntegrityEndToEnd$" | grep -c "^TestAutonomousWorkIntakeIntegrityEndToEnd$")" -eq 1 && go test ./cmd/oro -run "^TestAutonomousWorkIntakeIntegrityEndToEnd$" -count=1 -timeout=180s && ./scripts/quality_gate.sh'
Assert: exactly one test in cmd/oro lists under that name, it passes on main, and the full quality gate exits 0
```

## 11. Delivery Structure

The work decomposes into independently mergeable epics:

1. **Worker identity parity** — typed execution context and fail-closed runtime
   propagation, live credential-file transport, capability issuance/lifecycle,
   crash-safe refresh, and every assignment payload path.
2. **Executable task contracts** — shared validator, draft path, creation/update/
   admission enforcement, persisted contract version, and producer inventory.
3. **Evidence-bound proposals** — exact CLI/UDS protocol, evidence execution,
   manifests, durable proposal store, canonical scope, and validation.
4. **Autonomous proposal controller** — classification, taskcraft repair,
   atomic materialization, retry, scoped quarantine, and reconciliation.
5. **Dependency authority** — dispatcher-only worker edges and integration with
   `oro-vcw9`/`oro-emz2` review readiness.
6. **Observability and janitor repair** — status/health/throughput plus legacy
   contract repair.
7. **Production-path acceptance** — real Codex stale-worktree regression and
   restart/idempotency proof.

Epics 1 and 2 can land first and prevent recurrence before the proposal
controller is complete. Epic 3 must land before direct execution-worker mutation
instructions are removed, so workers retain a valid reporting path. Epics 3
through 6 may merge behind a disabled materialization flag. Epic 7 enables the
production path, removes legacy prompt instructions, and proves real command
composition.

## 12. Integration Point Ownership

| Boundary | Required production points | Owning epic |
|---|---|---|
| Assignment context | `assignBead`, `createAssignment`, `buildAssignPayload`, every reassignment path, `AssignPayload`, `handleAssign`, `runWorker`, `ExecWorkerSpawner.SpawnWorker`, all spawner interfaces/mocks, Claude and Codex spawners | 1 |
| Capability refresh | dispatcher expiry scheduler, credential-file lifecycle, `MsgCapabilityRefresh`, worker handler, `MsgCapabilityRefreshACK`, pending-token supersession, durable ACK/revocation/restart | 1 |
| Contract persistence | protocol bead types, schema/migrations, Store/CreateParams/UpdateParams, SQLite/shadow/fake/read-tx/export, `Ready`, `checkBeadReady` | 2 |
| Human/task mutations | Cobra-tree policy for create, update, reopen, publish, close, delete, defer, undefer, note add, dep add/remove plus generated QG/audit/janitor/recovery/cleanliness producers | 2 and 5 |
| Agent proposal producer | command registration, `oro evidence run`, `oro task propose-blocker`, UDS client, protocol messages, pre-registration `handleConn` request/response path, connection-preservation checks | 3 |
| Proposal state | schema, proposal/evidence store, scope normalization, transition/event idempotency | 3 |
| Canonical scope | `pkg/workproposal.ScopeKeyV1`, `NormalizeScopeV1`, versioned serialization and equivalence fixtures | 3 |
| Materialization | `Store.MaterializeWorkProposal`, assignment generation compare, bead/edge/proposal/event transaction, startup reconciliation | 4 |
| Scoped quarantine | proposal-specific admission exclusion, janitor retry, factory health/status/throughput/monitor | 4 and 6 |
| Review readiness | accepted edge rechecks at admission, approval, and retry using `oro-emz2` and `oro-vcw9` | 5 |
| Production acceptance | `cmd/oro/autonomous_work_intake_integrity_e2e_test.go`, real CLI/UDS/Codex shim, stale foreign-worktree evidence, restart and unrelated-throughput assertions | 7 |

## 13. Premortem

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
      mitigation_checked: Store.MaterializeWorkProposal owns one transaction with a global key, generation compare, event write, and startup commit-uncertainty reconciliation
    - risk: strict validation strands historical tasks
      severity: medium
      mitigation_checked: contract versioning preserves v0 and janitor repairs lazily
  elephants:
    - risk: workers currently have broad host authority, so this is integrity control rather than a hostile security boundary
  paper_tigers:
    - risk: requiring taskcraft makes genuine blocker handling too slow
      reason: taskcraft is a bounded one-shot only after evidence validation; task-local and QG failures use existing fast paths
```

## 14. Resolved Adversarial Decisions

- Capability transport is part of `AssignPayload` and every assignment/retry
  call chain, not runtime-local construction.
- Contract version and draft status are first-class persisted bead fields.
- Proposal submission deduplicates per assignment; materialization deduplicates
  across assignments with canonical scope.
- Agent-facing commands and their exact CLI/UDS protocol are part of delivery.
- Materialization uses one named Store transaction, not an unspecified
  transaction-or-compensation choice.
- Proposal quarantine is separate from recovery quarantine and cannot trigger
  its global freeze.
- The production acceptance epic owns real CLI, protocol, subprocess, restart,
  event, and unrelated-throughput fixtures.
- External worker launch transports the exact socket through typed context.
- Capability refresh is a durable refresh/ACK protocol tested across expiry.
- Live workers receive refresh through an atomically replaced credential file;
  restart supersedes unrecoverable pending raw tokens instead of storing them.
- Short-lived proposal connections bypass worker registration and cleanup.
- Provisional proposal identity preserves distinct scopes until canonicalization.
- Mutable command coverage is generated from the real Cobra tree.
- Draft editing covers every field required for publication.
- Materialization reuse asserts one edge and blocked state per source.
- Acceptance first proves the named production test exists exactly once.

## 15. Assumptions

- A worker observation may be wrong; dispatcher-owned assignment and QG state
  are authoritative.
- Normal recovery must not require a human.
- It is acceptable to quarantine one task after bounded autonomous attempts as
  long as unrelated work continues.
- Human-created incomplete notes may exist, but not in the executable queue.
- Execution workers do not need direct task/dependency mutation once proposal
  submission exists.
