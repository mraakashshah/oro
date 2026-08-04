# Durable Review Checkpoint Design

Date: 2026-07-16

Status: Validated by adversarial Gate 8; beadcraft graph `drc-factory`
created with 10 work-package epics and 53 leaf tasks

## 1. Goal

Make Oro's quality-gate and review boundary durable, typed, resumable, and
bounded.

Today, a reviewer can discover a real defect, but a keyword found anywhere in
the review transcript can misclassify the result as an environment or
infrastructure failure. The dispatcher then reopens the bead, completes the
assignment, releases the worker, and drops the actual findings. Unchanged code
can be assigned again, run the full quality gate again, and spend another
roughly seven minutes in review before rediscovering the same defect.

The desired end state is:

1. Review decisions are derived from typed fields, never substring searches
   over prompts, cards, tool logs, or raw transcripts.
2. Review findings are durable and are delivered to whichever worker resumes
   the bead.
3. A passing quality gate and a review decision form a durable checkpoint tied
   to an exact code state.
4. Worker or dispatcher death cannot lose an approved result, a rejected
   result, the QG proof, or the recovery action.
5. An unchanged rejected head cannot repeat QG and review.
6. Review-discovered contract gaps are repaired into executable acceptance
   requirements before another full gate.
7. Raw review transcripts are stored as bounded artifacts by reference. Events,
   ops runs, worker messages, and bead history carry compact structured data.
8. Review failure recovery is autonomous. Oro selects, executes, and records
   bounded recovery strategies without requiring an operator to babysit the
   bead.

### Primary beneficiary and protected scenario

The direct beneficiary is Oro's dispatcher while shepherding any implementation
bead across the QG-to-review boundary. The operator benefits indirectly from a
self-healing factory, but operator monitoring is not the mechanism's primary
consumer.

For every implementation bead that reaches `READY_FOR_REVIEW`, the checkpoint
must preserve enough state to continue correctly when any of these occur:

- review rejects the code and the same or a replacement worker must receive the
  exact structured findings;
- the reviewer process fails, times out, or reports a typed blocker;
- the implementation worker dies after QG passes;
- the dispatcher restarts while review or recovery is active;
- review approves the code but integration has not completed.

The success criterion is phase-local continuation: resume review, recovery,
implementation correction, or integration from the durable checkpoint without
restarting already-proven stages.

## 2. Source Research

The design is grounded in the current production paths and prior Oro designs.

### Current source paths

- `pkg/dispatcher/dispatcher.go`
  - `handleReadyForReview` changes the in-memory worker state to
    `WorkerReviewing` and starts `ops.Review`, but does not create a durable
    review checkpoint.
  - `handleReviewResult` routes a single `ops.Result`.
  - `classifyReviewFailure`, `reviewEnvBlocked`, and `reviewInfraBlocked` scan
    the full lower-cased feedback string. Keywords including `TaskOutput`,
    `tail -f`, permission errors, and timeouts can originate in the prompt,
    cards, recalled task text, or tool transcript rather than the failure.
  - `handleReviewBlocked` reopens the bead, completes the assignment, clears
    tracking, and idles the worker without sending findings.
  - `handleReviewRejection` does send feedback through a new `ASSIGN`, but the
    feedback is an untyped string and durable recovery depends on
    `rejection_history`.
  - startup recovery restores only assignment counters and worktree ownership.
    It does not restore a QG-passed or review-pending phase.
- `pkg/ops/ops.go`
  - `Result` exposes only `{Type, BeadID, Verdict, Feedback, Err}`.
  - `Spawner.Review` still uses the legacy single-pass prose path unless
    `ReviewOpts.MultiPersona` is explicitly enabled.
  - stream-json extraction and an idle watchdog already exist, but failed runs
    can still return a large raw output string as `Feedback`.
- `pkg/ops/finding.go`, `pkg/ops/review_parse.go`,
  `pkg/ops/review_merge.go`, and `pkg/ops/personas.go`
  - Oro already has structured `Finding`, `ReviewReport`, evidence validation,
    deterministic merge, finding IDs, multi-persona review, and persistent
    finding support.
  - The dispatcher does not enable or consume this structured spine.
- `pkg/worker/worker.go`
  - after QG passes, the worker stores only `pendingQGOutput` in memory and
    sends `READY_FOR_REVIEW`.
  - after approval, the worker echoes `DONE(QualityGatePassed=true)`.
  - if the worker dies between QG pass and approval, the QG proof disappears.
  - if the dispatcher dies, startup recovery has no durable review phase to
    resume.
- `pkg/protocol/message.go`
  - `ReadyForReviewPayload` carries only bead and worker IDs.
  - `DonePayload` carries a boolean plus raw QG output, with no tested HEAD,
    script hash, mode, or evidence ID.
  - `MaxMessageSize` is 1 MiB, which oversized retry context can approach.
- `pkg/protocol/schema.go`
  - `assignments`, `ops_runs`, `rejection_history`, QG incidents, and recovery
    quarantines exist.
  - there is no review-checkpoint table.
- `pkg/dispatcher/ops_runs.go`
  - durable ops runs can be failed, retried, superseded, and rerouted.
  - review routing exists, but the normal `READY_FOR_REVIEW` path does not
    consistently create and complete a linked review ops run.
- `pkg/dispatcher/assign_payload.go`
  - all retry paths share `buildAssignPayload`, which is the correct place to
    add compact structured recovery context.
- `pkg/dispatcher/missing_ac_test.go` and
  `pkg/dispatcher/dispatcher.go:checkBeadReady`
  - dispatcher admission checks missing AC, a minimal `Test:` marker, and an
    approximate module count.
  - it does not validate the full bead anatomy or reject vacuous commands.

### Prior designs incorporated

- `docs/plans/2026-05-31-review-depth-deepspec.md`
  - supplied the structured-finding spine, evidence validation, deterministic
    merge, persistent triage, and optional multi-persona review. Much of this is
    now implemented in `pkg/ops`.
- `docs/plans/2026-05-29-robust-claude-reviews-design.md`
  - supplied review-only streaming, final-result extraction, and idle-based
    process supervision.
- `docs/plans/2026-05-06-qg-semaphore-evidence-design.md`
  - supplied the QG evidence contract and the rule that evidence must be tied
    to an exact HEAD and QG script.
- `docs/plans/2026-05-06-qg-failure-handling-design.md`
  - established compact structured evidence, conservative classification,
    preserved work, and assignability tests after reopen.
- `docs/plans/2026-05-11-swarm-throughput-recovery-design.md`
  - established pre-review git hygiene, environment-aware review, and
    throughput as a correctness concern.
- `docs/plans/done/2026-02-12-worker-recovery-and-progress-timeout.md`
  - established that worker cleanup alone is insufficient if retry context is
    not durable.

No external research is required: the failure is internal to Oro's state and
protocol boundaries, and the repository already contains the relevant
mechanisms.

## 3. Root Cause

This is one architectural failure with four manifestations.

### 3.1 Review result loses type information

The review pipeline may produce structured findings internally, but
`ops.Result` collapses the outcome into `Verdict`, `Feedback string`, and
`Err`. The dispatcher then tries to reconstruct semantics by searching the
feedback text.

The bad data flow is:

```text
review prompt + project rules + cards + tool transcript + final answer
  -> one large Feedback string
  -> lower-case substring search
  -> env/infra classification overrides actual rejected findings
```

### 3.2 Review and QG progress are process-local

The worker owns the only pending QG output. The dispatcher owns the only
in-memory review state. SQLite knows that an assignment exists but not that:

- an exact HEAD passed QG;
- review started for that HEAD;
- review rejected it with specific findings;
- review approved it and merge should resume;
- review infrastructure blocked and ordinary assignment must stop.

### 3.3 Recovery destroys the useful phase distinction

The ordinary rejection path retries with feedback. The blocked path completes
the assignment and reopens the bead. Startup recovery similarly reduces state
to open/requeued assignment plus worktree. Neither path says what must happen
next. As a result, recovery restarts the entire pipeline.

### 3.4 The task contract is weaker than the review contract

Dispatcher admission checks syntax-level markers, while review checks missing
requirements, test-as-spec quality, scope drift, error paths, and boundary
behavior. When review identifies a genuine acceptance-contract gap, the gap is
not converted into a durable pre-gate requirement. The next worker receives
prose but the task remains formally unchanged.

## 4. Design Invariants

1. **Typed decision authority.** Review classification uses typed decision,
   finding, blocker, verification, and execution fields. Raw text is never a
   classifier input.
2. **Findings outrank blockers.** A valid Critical or Important finding means
   `rejected` even when the same run also reports an environment limitation.
3. **Approval fails closed.** An approved report from a failed/nonzero process
   does not authorize merge. A complete rejected report may still be used,
   because rejection cannot authorize unsafe code.
4. **Checkpoint identity is immutable.** A checkpoint is keyed to the bead,
   assignment, worker-tested HEAD, target HEAD, acceptance hash, QG script
   hash/mode, and review-policy hash.
5. **One review per checkpoint key.** The same immutable code and contract do
   not pay for review twice.
6. **No silent reopen.** Every non-approved outcome leaves either actionable
   findings, an autonomous recovery run, or an explicit recovery quarantine.
7. **Preserve work before releasing ownership.** Worktree and branch state are
   retained until merge proof or a durable recovery decision.
8. **Compact hot-path data.** Worker messages, events, `ops_runs`, and bead
   journey entries contain bounded summaries and structured findings, never
   raw stream transcripts.
9. **Restart-safe completion.** An approved checkpoint can resume integration
   without the original worker. A rejected checkpoint can resume implementation
   with a different worker.
10. **Contract gaps become contracts.** Review findings classified as
    acceptance gaps must pass a separate contract-repair and acceptance
    validation step before code retry.
11. **Commit before acknowledgement.** READY is acknowledged only after its
    canonical checkpoint and linked review ops run commit; lost ACKs replay
    idempotently.
12. **External effects carry proof.** Merge, recovery repair, artifact, and
    reminder side effects have durable intent, idempotency identity, and
    machine-verifiable completion proof.
13. **Incomplete coverage cannot approve.** Missing or failed required review
    personas fail approval closed.
14. **Pipeline ownership excludes ordinary assignment.** Any non-terminal
    checkpoint state blocks the bead from ordinary ready/assign paths without
    blocking unrelated beads.

## 5. Rejected Alternatives

### 5.1 Add more keyword exclusions

This is the narrowest patch, but every new prompt, card, task title, or tool can
introduce another misleading word. It also does nothing for restart recovery or
lost findings.

### 5.2 Move full review before QG

This catches semantic issues earlier but makes the expensive reviewer inspect
unformatted, uncompilable, or mechanically failing work. The factory would
trade QG waste for review waste.

### 5.3 Persist only the feedback string

`rejection_history` already approximates this. It cannot distinguish findings
from tool noise, cannot safely classify blockers, cannot resume approval, and
cannot prove the code state to which feedback applied.

### 5.4 Use bead status alone as the checkpoint

`open`, `in_progress`, and `blocked` are user-visible work states, not a
sufficient pipeline journal. They cannot encode QG evidence, immutable HEAD
identity, review attempt ownership, or approved-but-not-integrated recovery.

## 6. Typed Review Outcome

`ops.Result` remains the generic subprocess result for compatibility, but
review results gain an optional typed payload.

### Dependency-neutral finding contract

Introduce `pkg/reviewcontract`, which imports neither `pkg/ops` nor
`pkg/protocol`, as the single owner of `Severity`, `Evidence`, `ContractImpact`,
`Finding`, and `FindingHistoryEntry`. `pkg/ops` uses or aliases those types for
source compatibility; `pkg/protocol` imports `pkg/reviewcontract` directly.
There are no parallel ops and wire finding structs and therefore no lossy
dispatcher conversion.

```go
package reviewcontract

type Finding struct {
    ID             string                `json:"id"`
    Severity       Severity              `json:"severity"`
    Category       string                `json:"category"`
    Title          string                `json:"title"`
    Detail         string                `json:"detail"`
    Evidence       []Evidence            `json:"evidence"`
    Confidence     int                   `json:"confidence"`
    Sources        []string              `json:"sources"`
    SourceFamilies []string              `json:"source_families,omitempty"`
    Origin         string                `json:"origin"`
    Status         string                `json:"status,omitempty"`
    History        []FindingHistoryEntry `json:"history,omitempty"`
    ContractImpact ContractImpact        `json:"contract_impact"`
    RequiredAction string                `json:"required_action"`
}
```

`TestReviewFindingWireRoundTrip` marshals a fully populated finding through
`protocol.AssignPayload.ReviewRecovery`, unmarshals it at the worker boundary,
and requires deep equality for ID, severity, evidence, contract impact,
required action, and every other field. This test also protects the import
graph: `pkg/reviewcontract` may use only the standard library.

```go
type ReviewDecision string

const (
    ReviewApproved ReviewDecision = "approved"
    ReviewRejected ReviewDecision = "rejected"
    ReviewBlocked  ReviewDecision = "blocked"
    ReviewFailed   ReviewDecision = "failed"
)

type ReviewExecutionKind string

const (
    ReviewExecSucceeded  ReviewExecutionKind = "succeeded"
    ReviewExecSpawnError ReviewExecutionKind = "spawn_error"
    ReviewExecExitError  ReviewExecutionKind = "exit_error"
    ReviewExecTimeout    ReviewExecutionKind = "timeout"
    ReviewExecIdle       ReviewExecutionKind = "idle_timeout"
    ReviewExecCancelled  ReviewExecutionKind = "cancelled"
)

type ReviewBlocker struct {
    Class      string `json:"class"` // environment | infrastructure
    Scope      string `json:"scope"` // acceptance | broader_verification | runtime
    Command    string `json:"command,omitempty"`
    ErrorCode  string `json:"error_code,omitempty"`
    Summary    string `json:"summary"`
}

type ReviewVerification struct {
    AcceptanceCommand string `json:"acceptance_command,omitempty"`
    AcceptanceStatus  string `json:"acceptance_status"` // passed|failed|not_run|blocked
    AcceptanceExit    int    `json:"acceptance_exit,omitempty"`
}

type ReviewArtifactRef struct {
    Path      string `json:"path,omitempty"`
    SHA256    string `json:"sha256"`
    Bytes     int64  `json:"bytes"`
    Truncated bool   `json:"truncated,omitempty"`
}

type ReviewPersonaExecution struct {
    Persona   string              `json:"persona"`
    Required  bool                `json:"required"`
    Kind      ReviewExecutionKind `json:"kind"`
    ErrorCode string              `json:"error_code,omitempty"`
}

type ReviewExecution struct {
    Kind              ReviewExecutionKind      `json:"kind"`
    ExitCode          int                      `json:"exit_code,omitempty"`
    ErrorCode         string                   `json:"error_code,omitempty"`
    Complete          bool                     `json:"complete"`
    RequiredPersonas  []string                 `json:"required_personas,omitempty"`
    CompletedPersonas []string                 `json:"completed_personas,omitempty"`
    PersonaExecutions []ReviewPersonaExecution `json:"persona_executions,omitempty"`
}

type ReviewOutcome struct {
    Decision     ReviewDecision          `json:"decision"`
    Findings     []reviewcontract.Finding `json:"findings,omitempty"`
    Blockers     []ReviewBlocker         `json:"blockers,omitempty"`
    Verification ReviewVerification      `json:"verification"`
    Execution    ReviewExecution         `json:"execution"`
    Summary      string                  `json:"summary"`
    Artifact     ReviewArtifactRef       `json:"artifact"`
}
```

The shared `Finding` uses a narrowly-scoped contract impact:

```go
type ContractImpact string

const (
    ContractImplementationFix ContractImpact = "implementation_fix"
    ContractAcceptanceGap     ContractImpact = "acceptance_gap"
)
```

The dispatcher consumes `ReviewOutcome`. A legacy prose response is parsed into
a conservative synthesized outcome:

- exact terminal rejected verdict -> rejected with compact legacy feedback;
- exact terminal approved verdict plus zero process error -> approved;
- any process error or ambiguous output -> failed;
- legacy text never becomes env/infra blocked through keyword matching.

### Decision precedence

The outcome reducer applies these rules in order:

1. Any validated gating finding -> `rejected`.
2. Otherwise, valid approval plus execution error -> `failed`.
3. Otherwise, typed blocker -> `blocked`.
4. Otherwise, exact valid approval -> `approved`.
5. Otherwise -> `failed`.

This explicitly prevents a blocker or transcript keyword from swallowing real
findings.

### Required review coverage

The review-policy hash includes the selected personas, which personas are
required, fallback policy, prompt/schema version, reviewer configuration, and
triage revision. Approval is legal only when:

1. every required persona has one successful, schema-valid report;
2. `ReviewExecution.Complete` is true;
3. required and completed persona sets match;
4. no validated gating finding exists;
5. no Critical or Important candidate was dropped by validation without
   producing a fail-closed rejected/failed outcome.

A failed or missing required persona makes the aggregate `failed`, unless
another successful report contains a valid gating finding, in which case the
safe terminal decision remains `rejected`. A fallback single-pass review is
allowed only when the hashed policy explicitly selects it; the fallback itself
becomes required coverage and cannot silently replace one failed persona.

The docs-only shortcut must emit a complete typed `ReviewOutcome` with
`Execution.Kind=succeeded`, an explicit docs-only policy hash, and no raw prose
classification. If the shortcut cannot construct that outcome, it runs the
ordinary typed review path instead of auto-approving.

## 7. Bounded Review Artifacts

The ops process separates raw transport from the result contract.

- Stream raw NDJSON/tool output to a project-scoped artifact under the resolved
  Oro state directory, not the worker worktree.
- Create artifacts with mode `0600`.
- Compute SHA-256 and byte count while streaming.
- Keep only the final assistant result plus a bounded diagnostic tail in memory.
- Cap compact `Result.Feedback` at 64 KiB.
- Cap checkpoint findings JSON at 128 KiB.
- Cap event and `ops_runs` summaries at 16 KiB.
- Keep `ASSIGN` review recovery context below 192 KiB and assert the complete
  protocol message remains below `protocol.MaxMessageSize`.
- Apply a configurable artifact byte cap and mark truncation explicitly.
- Retain artifacts long enough for incident diagnosis, then prune them through
  an explicit janitor policy; never delete them as part of assignment cleanup.

The artifact reference is diagnostic only. Classification and recovery remain
possible if the raw artifact is missing.

`cmd/oro:buildDispatcherWithReviewTimeoutsAndCleanliness` resolves
`<project-state-dir>/review-artifacts` through `ResolveDaemonPaths` and passes
it, the byte cap, and retention duration through dispatcher config into
`ReviewOpts`. `pkg/ops/exec_spawner.go` streams to a temporary `0600` file,
fsyncs it, atomically renames it, and returns its hash and size.

Artifact create, write, fsync, or hash failure is recorded in
`ReviewExecution.ErrorCode`. It fails approval closed. A complete structured
rejection may still reject safely because it cannot authorize integration.
Janitor selection is database-driven: it may delete only artifacts referenced
exclusively by terminal checkpoints older than retention, never an artifact
referenced by a non-terminal checkpoint or recovery run.

For retention, only `integrated` and `superseded` are terminal. `approved`,
`rejected`, `blocked`, `failed`, `quarantined`,
`manual_integration_pending`, and every active phase remain non-terminal because
recovery, correction, integration, triage, or operator inspection can still
need their artifacts.

Full findings are stored as capped rows in `review_checkpoint_findings`, not as
one lossy JSON blob. Inline recovery preserves every gating finding when the
message fits. If it does not fit, `ASSIGN` carries a local
`ReviewRecoveryArtifactRef {Path, SHA256, FindingCount}`; the worker loads and
hash-verifies the atomically written recovery artifact before starting. Missing,
oversized, or hash-mismatched recovery data fails closed into typed recovery.
Compaction may truncate excerpts and diagnostics deterministically, but never
finding IDs, severity, file/line, contract impact, or required action.

Before correction becomes routable, the canonical lossless recovery artifact is
fsynced and its path, SHA-256, byte count, and finding count are committed on
the checkpoint in the same transaction as `rejected`. Restart reconstructs
`ReviewRecoveryArtifactRef` from those columns, not compact rows or process
memory. Because compact rows may truncate diagnostics, they are never treated
as a lossless regeneration source. A missing persisted artifact enters typed
checkpoint recovery/quarantine and never sends partial findings or silently
reruns review.

## 8. QG Evidence at READY_FOR_REVIEW

The worker must send proof of the QG-passed state before review starts.

`ResolveDaemonPaths` exposes `<project-state-dir>/review-evidence`, and
`buildDispatcherWithReviewTimeoutsAndCleanliness` passes it as
`Config.ReviewEvidenceDir`. Every `AssignPayload` carries that absolute
project-state path as `QGEvidenceDir`. The worker writes
`<QGEvidenceDir>/<bead-id>/<assignment-id>/<ready-attempt>.json` with directory
mode `0700` and file mode `0600`, then fsyncs the file and parent directory.
The writer rejects non-absolute paths and any clean/join result outside the
assigned evidence directory. The dispatcher independently canonicalizes every
`QGEvidenceRef.Path`, requires it to remain beneath its own configured
`ReviewEvidenceDir`, rejects symlink escape, and hash-verifies before use.

Evidence is deliberately outside the worktree. Oro does not add a broad
`.oro` hygiene exemption, so arbitrary untracked project files remain dirty.
`TestReadyEvidenceDoesNotDirtyWorktree` creates an otherwise unconfigured
temporary Git repository, writes evidence through the production worker helper,
and proves `checkPreReviewGitHygiene` still reports clean without a repository
ignore rule.

`TestReadyEvidenceProductionAssignPath` starts the real dispatcher/worker socket
assignment path, receives an `ASSIGN` built only by
`pkg/dispatcher/assign_payload.go:buildAssignPayload`, and requires its
`QGEvidenceDir` to equal the canonical absolute configured directory. The
receiving worker writes evidence through the production helper, sends READY,
and the dispatcher accepts that exact reference while the temporary worktree
remains clean. Tests may not seed `QGEvidenceDir` directly.

`ReadyForReviewPayload` gains:

```go
type QGEvidence struct {
    RunID        string `json:"run_id"`
    AssignmentID int64  `json:"assignment_id"`
    BeadID       string `json:"bead_id"`
    WorkerID     string `json:"worker_id,omitempty"`
    HeadSHA      string `json:"head_sha"`
    TargetBranch string `json:"target_branch"`
    TargetSHA    string `json:"target_sha"`
    ScriptHash   string `json:"script_hash"`
    Mode         string `json:"mode"`
    Passed       bool   `json:"passed"`
    OutputHash   string `json:"output_hash,omitempty"`
    StartedAt    string `json:"started_at"`
    FinishedAt   string `json:"finished_at"`
}
```

The code that runs QG creates the evidence. `READY_FOR_REVIEW` is invalid for
the durable path unless:

- evidence says passed;
- evidence bead and worker match the assignment;
- current worktree HEAD equals `HeadSHA`;
- current QG script hash equals `ScriptHash`;
- the worktree passes pre-review git hygiene.

Old workers remain wire-compatible. Missing evidence takes the legacy path:
review may run, but approval requires dispatcher-owned QG before integration.

The new path no longer relies on `worker.pendingQGOutput` as authoritative
state.

### READY acknowledgement and replay

`READY_FOR_REVIEW` is an at-least-once phase transition, not a fire-and-forget
event.

```go
type ReadyForReviewPayload struct {
    BeadID        string        `json:"bead_id"`
    WorkerID      string        `json:"worker_id"`
    ReadyAttempt  string        `json:"ready_attempt"`
    QGEvidence    QGEvidence    `json:"qg_evidence"`
    QGEvidenceRef QGEvidenceRef `json:"qg_evidence_ref"`
}

type ReadyForReviewAckPayload struct {
    BeadID        string `json:"bead_id"`
    ReadyAttempt  string `json:"ready_attempt"`
    CheckpointID  int64  `json:"checkpoint_id"`
    CheckpointKey string `json:"checkpoint_key"`
}

type QGEvidenceRef struct {
    RunID  string `json:"run_id"`
    Path   string `json:"path"`
    SHA256 string `json:"sha256"`
}

type ReconnectPayload struct {
    WorkerID       string         `json:"worker_id"`
    BeadID         string         `json:"bead_id"`
    State          string         `json:"state"`
    Phase          string         `json:"phase,omitempty"`
    ReadyAttempt   string         `json:"ready_attempt,omitempty"`
    QGEvidenceRef  *QGEvidenceRef `json:"qg_evidence_ref,omitempty"`
    ContextPct     int            `json:"context_pct"`
    BufferedEvents []Message      `json:"buffered_events"`
}
```

`protocol.AssignPayload` gains immutable `AssignmentID int64` and
`TargetSHA string`. `assignBead` resolves the live target SHA, stores the durable
assignment ID and target SHA on the tracked worker before `buildAssignPayload`;
correction routing and `assignHandoffToWorker` do the same.
`worker.handleAssign` rejects a missing/nonpositive identity, empty target
branch/SHA, or malformed SHA and retains them for QG, READY, reconnect, and
handoff. The evidence path's assignment component, decoded
`QGEvidence.AssignmentID`, active/requeued assignment row, bead, worktree, and
READY identity must all agree. A correction worker's next checkpoint uses its
current correction assignment ID and freshly captured target SHA; prior
evidence remains bound to the checkpoint's origin assignment.

Worker pre-QG target preparation is mandatory:

```go
type RebaseProof struct {
    TargetBranch string
    TargetSHA    string
    HeadSHA      string
}

func rebaseOntoTarget(
    ctx context.Context,
    worktree, targetBranch, assignedTargetSHA string,
) (RebaseProof, error)
```

The worker rebases onto the exact assigned commit, not a moving branch name,
then proves that commit is an ancestor of the resulting HEAD before QG. Any
rebase/ancestry error blocks QG and READY; the current ignored-error call is
removed. QG evidence copies the returned target identity and never samples a
new target after QG. At READY the dispatcher requires nonempty target fields,
equality with the assignment, equality with the live target ref, and ancestry
from evidence target SHA to evidence HEAD. A target move during QG or before
READY enters `RecoveryRefreshTarget` and requires new target-bound QG/review.

Before sending READY, the worker atomically writes and fsyncs QG evidence under
the assigned project-state evidence directory. It enters an
`awaiting_review_ack` phase and retains
`{ReadyAttempt,QGEvidence,QGEvidenceRef}` independently of the evicting
`MessageBuffer`.

Initial READY carries both the inline evidence and its file reference. The
dispatcher canonicalizes and hash-verifies the referenced file, decodes it, and
requires byte-for-byte canonical JSON equality with the inline evidence before
creating a checkpoint. The canonical path must equal the exact path derived from
configured directory, bead, assignment, and ready attempt—not merely remain
beneath the directory—and reference `RunID` must equal decoded and inline
`RunID`. Missing reference, mismatched bytes/hash/identity, or unsafe path
selects the legacy-unverified path and forces dispatcher QG; it can never
authorize integration. Reconnect reuses the same `QGEvidenceRef`.

`TestReadyEvidenceIdentityValidation` submits matching evidence, different
inline canonical content against the same file, wrong reference hash, wrong
run ID, wrong assignment ID, empty/stale target identity, target movement during
QG, non-ancestor target/head, and path/symlink escape. Every mismatch must avoid
trusted checkpoint creation and force target refresh or the dispatcher-owned
legacy QG path.

While the socket remains connected, awaiting-ACK state starts a five-second
timer. Timeout retransmits the identical READY/attempt/reference, doubles the
interval up to a 30-second cap, and never creates a new attempt or reruns QG.
ACK clears the timer only after identity validation; reconnect restarts it from
five seconds. `TestReadyForReviewAckLiveRetransmit` drops ACK on a live socket
and proves one canonical checkpoint and one review.

The dispatcher validates READY, transactionally creates or reuses the
checkpoint and linked review ops run, commits, and only then sends ACK.
Duplicate READY messages with the same attempt or canonical checkpoint key
return the same ACK and never spawn a second review. If ACK is lost, the worker
resends READY. On reconnect, `ReconnectPayload` carries the explicit phase,
ready attempt, and QG evidence reference; `idle` is not legal while awaiting
ACK or review.

`protocol.Message` gains `MsgReadyForReviewAck` and a
`ReadyForReviewAck` envelope field. `worker.Run -> handleMessage` consumes it
through `handleReadyForReviewAck`, verifies bead/attempt/key, clears only the
awaiting-ACK retry state, and retains evidence until the checkpoint becomes
terminal. Dispatcher `handleReconnect -> processReconnectUnderLock` validates
the new phase/reference fields and invokes the same idempotent READY transaction
rather than reducing the worker to `idle`.

If the worker dies before ACK, startup recovery scans the active or requeued
assignment's preserved evidence file, verifies its hash and checkpoint identity,
and performs the same idempotent transaction. Evidence files are reconstructable
phase state and therefore need not be non-evictable buffer entries. They are
removed only after checkpoint terminal state and integration or supersession
are durable.

## 9. Durable Review Checkpoint

Add a state-DB table dedicated to immutable review phases.

```sql
CREATE TABLE review_checkpoints (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    checkpoint_key TEXT NOT NULL,
    bead_id TEXT NOT NULL,
    origin_assignment_id INTEGER NOT NULL,
    current_assignment_id INTEGER,
    worker_id TEXT,
    worktree TEXT NOT NULL,
    branch TEXT NOT NULL,
    target_branch TEXT NOT NULL,
    head_sha TEXT NOT NULL,
    target_sha TEXT NOT NULL,
    acceptance_hash TEXT NOT NULL,
    qg_run_id TEXT,
    qg_script_hash TEXT NOT NULL,
    qg_mode TEXT NOT NULL,
    qg_output_hash TEXT,
    qg_evidence_path TEXT,
    qg_evidence_sha256 TEXT,
    review_policy_hash TEXT NOT NULL,
    triage_revision TEXT NOT NULL,
    ready_attempt TEXT NOT NULL,
    state TEXT NOT NULL,
    review_attempt INTEGER NOT NULL DEFAULT 0,
    recovery_attempt INTEGER NOT NULL DEFAULT 0,
    recovery_strategy TEXT,
    failure_fingerprint TEXT,
    next_recovery_at TEXT,
    quarantined_at TEXT,
    next_quarantine_reminder_at TEXT,
    quarantine_reminded_at TEXT,
    quarantine_reminder_count INTEGER NOT NULL DEFAULT 0,
    blockers_json TEXT NOT NULL DEFAULT '[]',
    verification_json TEXT NOT NULL DEFAULT '{}',
    summary TEXT NOT NULL DEFAULT '',
    artifact_path TEXT,
    artifact_sha256 TEXT,
    artifact_bytes INTEGER NOT NULL DEFAULT 0,
    recovery_artifact_path TEXT,
    recovery_artifact_sha256 TEXT,
    recovery_artifact_bytes INTEGER NOT NULL DEFAULT 0,
    recovery_artifact_finding_count INTEGER NOT NULL DEFAULT 0,
    ops_run_id INTEGER,
    integration_target_before_sha TEXT,
    integration_approved_head_sha TEXT,
    integration_observed_target_sha TEXT,
    integration_step TEXT,
    override_kind TEXT,
    override_source TEXT,
    overridden_at TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now')),
    completed_at TEXT
);

CREATE UNIQUE INDEX idx_review_checkpoints_active_key
ON review_checkpoints(checkpoint_key)
WHERE state <> 'superseded';

CREATE TABLE review_checkpoint_findings (
    checkpoint_id INTEGER NOT NULL,
    finding_id TEXT NOT NULL,
    severity TEXT NOT NULL,
    file TEXT NOT NULL,
    line INTEGER,
    contract_impact TEXT NOT NULL,
    required_action TEXT NOT NULL,
    compact_json TEXT NOT NULL,
    PRIMARY KEY(checkpoint_id, finding_id)
);

CREATE TABLE review_recovery_attempts (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    checkpoint_id INTEGER NOT NULL,
    failure_fingerprint TEXT NOT NULL,
    idempotency_key TEXT NOT NULL UNIQUE,
    strategy TEXT NOT NULL,
    action_json TEXT NOT NULL,
    status TEXT NOT NULL,
    proof_json TEXT NOT NULL DEFAULT '{}',
    started_at TEXT NOT NULL,
    completed_at TEXT
);

CREATE TABLE review_quarantine_deliveries (
    checkpoint_id INTEGER NOT NULL,
    scheduled_at TEXT NOT NULL,
    delivered_at TEXT,
    sink TEXT NOT NULL,
    PRIMARY KEY(checkpoint_id, scheduled_at, sink)
);
```

Allowed states:

```text
qg_passed
review_running
rejected
correction_assigning
correction_assigned
contract_repair_running
blocked
failed
recovery_running
quarantined
approved
manual_integration_pending
integrating
integrated
superseded
```

`checkpoint_key` is the SHA-256 of a versioned, length-prefixed canonical
encoding of:

```text
bead_id
head_sha
target_sha
acceptance_hash
qg_script_hash
qg_mode
review_policy_hash
triage_revision
```

Missing legacy values use explicit non-null sentinels such as
`legacy-unverified`; nullable SQLite columns are never part of uniqueness.
Assignment and worker ownership may move during recovery without changing the
code/contract checkpoint identity. There may be only one non-superseded
checkpoint for a canonical key.

The current checkpoint is authoritative for pipeline recovery. The bead journey
receives compact `review_finding`, `review_checkpoint_changed`, and triage
events for user-visible audit, but recovery does not depend on scanning an
unbounded journey.

## 10. Review Ops-Run Lifecycle

Before spawning review, the dispatcher transactionally:

1. verifies the active or requeued assignment owns the worktree;
2. creates or reuses the checkpoint by canonical key;
3. creates a linked `ops_runs(type=review, status=running)` row;
4. transitions the checkpoint to `review_running`;
5. commits before acknowledging READY or spawning the subprocess.

The spawned goroutine carries an immutable
`ReviewAttemptContext {CheckpointID, CheckpointKey, AssignmentID, OpsRunID,
ReviewAttempt}`. Every result handler accepts this context instead of
reconstructing identity from worker and bead IDs.

Terminal handling:

- approved -> ops run `resolved`, checkpoint `approved`;
- rejected -> ops run `resolved`, checkpoint `rejected`;
- typed environment/infrastructure blocker -> ops run `failed`, checkpoint
  `blocked`;
- spawn, timeout, cancellation, malformed result, or unsafe approval -> ops run
  `failed`, checkpoint `failed`.

No review path may leave a `running` ops run after terminal handling. A blocked
or failed result automatically enters the recovery controller. Manual
`oro ops retry` remains an administrative override, but it is not part of the
normal recovery contract.

Checkpoint transition and ops-run completion occur in one SQLite transaction.
When startup supersedes a dead run, failure to route its replacement completes
the replacement as `failed` in the same recovery cycle; it may not remain
`running` without a process or durable scheduled retry.

### Autonomous recovery controller

The controller chooses the next safe action from typed outcome fields and
durable attempt history. It never derives a strategy from raw transcript text.

```go
type RecoveryStrategy string

const (
    RecoveryRerouteReview       RecoveryStrategy = "reroute_review"
    RecoveryProbeCapability     RecoveryStrategy = "probe_capability"
    RecoveryRepairDependency    RecoveryStrategy = "repair_dependency"
    RecoveryRepairContract      RecoveryStrategy = "repair_contract"
    RecoveryRefreshTarget       RecoveryStrategy = "refresh_target"
    RecoveryResolveIntegration  RecoveryStrategy = "resolve_integration_conflict"
    RecoveryRetryIntegration    RecoveryStrategy = "retry_integration"
    RecoveryQuarantine          RecoveryStrategy = "quarantine"
)

type RecoveryAction struct {
    IdempotencyKey    string           `json:"idempotency_key"`
    CheckpointID      int64            `json:"checkpoint_id"`
    FailureFingerprint string         `json:"failure_fingerprint"`
    Strategy          RecoveryStrategy `json:"strategy"`
    ActionID          string           `json:"action_id"`
    Arguments         map[string]string `json:"arguments,omitempty"`
    ExpectedProof     string           `json:"expected_proof"`
}

type RecoveryResult struct {
    IdempotencyKey string `json:"idempotency_key"`
    Status         string `json:"status"` // succeeded|failed|blocked
    ProofHash      string `json:"proof_hash,omitempty"`
    ErrorCode      string `json:"error_code,omitempty"`
}
```

The policy maps typed `{Execution.Kind, Blocker.Class, Blocker.Scope,
Blocker.ErrorCode}` tuples to an allowlisted `ActionID`. Reviewer-supplied
commands and recovery-planner prose are never executed directly. The planner
may select only an allowlisted action and bounded arguments, which a dedicated
executor validates. Each side effect is guarded by the persisted idempotency key
and must return machine-verifiable proof before the checkpoint advances.

1. Compute a stable failure fingerprint from checkpoint identity, execution
   kind, blocker class/scope/error code, and failed verification command.
2. If the process died or timed out before a trustworthy outcome, reroute review
   from the same QG checkpoint after bounded backoff.
3. If a typed environment or infrastructure blocker names a repairable
   precondition, create a linked `ops_runs(type=review_recovery)` run to repair
   or provision that precondition, then retry only the blocked verification or
   review phase.
4. If the failure is an acceptance-contract gap, use the contract-repair path
   rather than infrastructure recovery.
5. If the same fingerprint survives the allowed strategies, invoke one bounded
   recovery-planning run that must return a typed, policy-approved action.
6. If no safe action remains, transition only this checkpoint to
   `quarantined`, preserve all work, and continue dispatching unrelated beads.
7. While quarantined, publish a deduplicated recurring operator reminder and
   automatically reactivate recovery when a relevant input fingerprint changes.

Retry budgets are per failure fingerprint and survive dispatcher restarts.
Changing process IDs, workers, or timestamps cannot reset the budget. Recovery
actions are idempotent or guarded by compare-and-swap state transitions.
Success clears the active fingerprint but preserves attempt history for audit.

The factory does not wait for an operator between these steps. Status and CLI
commands expose the strategy, attempts, next retry, and quarantine reason for
diagnosis or optional override. Reminder timestamps and counts are durable so a
dispatcher restart neither suppresses a due reminder nor repeats one early.

The default quarantine reminder schedule is:

1. immediately when the checkpoint enters quarantine;
2. every 15 minutes during its first hour in quarantine;
3. hourly after the first hour until it leaves quarantine.

`quarantined_at` anchors the schedule and `next_quarantine_reminder_at` makes
delivery restart-safe. Configuration may make reminders more frequent, but may
not disable the immediate reminder, the recurring hourly reminder, or inclusion
in progress/status output.

Relevant reactivation inputs include:

- toolchain version plus sorted dependency-lock-file hashes;
- `.oro/config.yaml` hash and review-policy hash;
- reviewer/provider health epoch maintained by the dispatcher;
- target HEAD;
- acceptance-contract hash and triage revision;
- an allowlisted capability probe result for the previously blocked command.

An input change resets only the recovery budget made obsolete by that change.
It does not discard the checkpoint, worktree, findings, or prior audit history.
The dispatcher computes the canonical environment fingerprint at startup, after
each recovery action, and once per minute for quarantined checkpoints. Status
and progress queries read the latest sample but do not themselves execute side
effects. A changed component reactivates only strategies that declare that
component as a precondition.

### Production maintenance scheduler

`spawnBackgroundLoops` starts one `reviewMaintenanceLoop`. On a one-minute
base tick it:

1. routes due rejected checkpoints to correction workers;
2. runs due recovery attempts and fingerprint sampling;
3. reconciles `manual_integration_pending` ancestry proof;
4. claims and emits due quarantine reminders;
5. invokes artifact pruning when the configured retention sweep is due.

Each operation claims durable work before side effects, so overlapping ticks or
dispatcher restart are harmless. Config exposes the base tick, artifact
retention, and artifact sweep interval; defaults are one minute, seven days, and
one hour. `TestReviewArtifactJanitorScheduled` proves a terminal due artifact is
deleted by the loop while active references remain.
`TestReviewQuarantineReminderScheduler` uses a fake clock to prove immediate,
15-minute, hourly, restart, and duplicate-delivery behavior through the actual
loop.

## 11. State Machine

```text
worker implementation
  -> QG pass + evidence
  -> checkpoint qg_passed
  -> review_running

review_running
  -> approved
       -> integrating
       -> integrated
       -> bead closed

  -> rejected (implementation_fix)
       -> preserve assignment/worktree
       -> correction_assigning
       -> reserve same or replacement worker
       -> correction_assigned with compact findings
       -> worker changes HEAD
       -> acceptance preflight
       -> full QG
       -> new checkpoint key

  -> rejected (acceptance_gap)
       -> contract_repair_running
       -> validated acceptance revision
       -> preserve/requeue worktree
       -> worker retry with revised contract

  -> blocked or failed
       -> preserve/requeue assignment
       -> release worker capacity
       -> failed ops run blocks ordinary reassignment
       -> recovery_running
       -> repair/reroute/retry from the durable checkpoint
       -> resume the appropriate phase on success
       -> quarantined only when no policy-approved action remains
```

No transition from `blocked` or `failed` directly reopens the bead into the
ordinary ready queue.

## 12. Approval Without Worker Echo

The dispatcher becomes completion owner for evidence-backed checkpoints.

`beginCheckpointIntegration` reads the live target ref immediately before
integration intent and compare-and-swaps only when it equals the checkpoint's
`target_sha`. `pkg/merge/merge.go:Opts` gains `ExpectedTargetSHA`, and
`Coordinator.Merge` performs the expected-ref comparison at its final target
update; a ref change between intent and merge returns typed `ErrTargetMoved`
before any semantic merge. Either mismatch transitions the checkpoint to
`recovery_running` with `RecoveryRefreshTarget`, preserves the
assignment/worktree/approval, and keeps the bead out of ordinary queues. Target
refresh or rebase must run QG and review again under a new target-bound
checkpoint key; only after the replacement is durable does the stale checkpoint
become `superseded`.

`TestReviewIntegrationTargetInvalidation` advances the target after approval and
again at the merge compare point. Both cases must avoid merge, assignment
completion, and bead reopen, then produce new target-bound QG/review evidence.

On approval:

1. compare-and-swap checkpoint `review_running -> approved`;
2. verify assignment identity, worktree, HEAD, and QG evidence again;
3. record `integration_target_before_sha`, `integration_approved_head_sha`, and
   `integration_step=intent`, then transition `approved -> integrating`;
4. execute the existing passing-DONE merge path;
5. read the target ref and durably record
   `integration_observed_target_sha` plus `integration_step=merge_observed`;
6. complete the assignment idempotently and record
   `integration_step=assignment_completed`;
7. close the bead idempotently and record `integration_step=bead_closed`;
8. transition to `integrated` only after all proof is durable.

Refactor the successful half of `handleDone` into a shared integration function
used by:

- legacy worker `DONE(QualityGatePassed=true)`;
- durable approved checkpoint recovery.

`checkPreMergeQG` runs for legacy or untrusted evidence only. A durable
checkpoint revalidates its immutable evidence and live target but does not run a
redundant dispatcher QG for the same key. `handleNoopMerge` and
`completeEpicRebaseChild` must call the same checkpoint proof/finalization
primitive as `finalizeSuccessfulMerge`:

- no-op requires ancestry proof that the approved head is already reachable
  from the expected target, records the observed target, and advances every
  integration step;
- epic-rebase-child updates the epic target with an expected-ref compare,
  records its observed ref, and advances the same assignment/bead/checkpoint
  steps;
- crashes between either ref side effect and DB proof reconcile from ancestry
  and never rerun QG, review, or semantic merge.

`TestReviewIntegrationSpecialBranches` drives durable approval through
`checkPreMergeQG`, `handleNoopMerge`, and `completeEpicRebaseChild`, proving QG
is skipped only for trusted evidence and both special branches become
`integrated` idempotently.

The expected-ref compare is a concrete interface contract:

```go
type WorktreeManager interface {
    UpdateBranchRef(
        ctx context.Context,
        targetBranch, sourceBranch, expectedOldSHA string,
    ) (observedSHA string, err error)
}
```

`GitWorktreeManager.UpdateBranchRef` resolves the source SHA and executes atomic
`git update-ref <target-ref> <source-sha> <expected-old-sha>`. Compare failure
returns typed `ErrTargetMoved` and never overwrites the moved ref.
`completeEpicRebaseChild` passes the checkpoint's expected target SHA and
persists the returned observed SHA. All interface implementations, mocks, and
legacy callers are migrated. `TestReviewIntegrationSpecialBranches` injects a
target move at the `UpdateBranchRef` operation and requires target-refresh
recovery with no assignment completion, bead reopen, or checkpoint
finalization.

`ReviewResultPayload` gains a completion-owner marker. New workers clear their
pending local QG state and do not echo `DONE` when the dispatcher owns
completion. Old workers may still echo `DONE`; assignment/checkpoint CAS plus
the existing merge guard must make the duplicate a no-op.

This removes the requirement that the original worker survive until approval.

When existing `ManualIntegration` mode is enabled, dispatcher ownership does
not bypass it. Approval transitions to `manual_integration_pending`, preserves
the worktree and assignment, and emits the existing manual-integration signal.
After an explicit/manual merge, reconciliation verifies that the approved head
is an ancestor of the target before completing assignment, bead, and checkpoint
state. While the same dispatcher remains running,
`reviewMaintenanceLoop -> reconcileManualIntegrationCheckpoint` scans each
pending checkpoint at the base tick. When ancestry is proven it compare-and-swaps
to `integrating`, records the observed target proof, and invokes the same
idempotent assignment/bead finalizer. No proof leaves the checkpoint pending
without side effects. `TestReviewApprovedManualIntegrationPreserved` performs
the manual merge while one dispatcher instance remains alive, advances a fake
clock, and observes automatic finalization through the production loop without
restart or direct helper invocation.

### Crash-consistent integration reconciliation

Startup reconciles every `integrating` checkpoint before ordinary assignment:

- target ref still equals `integration_target_before_sha` -> merge did not
  happen; revalidate the checkpoint and retry merge;
- approved head is an ancestor of the current target and the recorded target
  before SHA is also an ancestor -> merge happened; record the observed target
  SHA if missing and resume assignment/bead finalization;
- recorded observed target SHA equals current target -> resume the next
  idempotent DB/bead step;
- target moved and ancestry cannot prove the approved head was integrated ->
  fail closed into checkpoint-scoped recovery or quarantine.

Tests inject death before merge, after the git merge side effect, after observed
SHA persistence, after assignment completion, and after bead close. No restart
may issue a duplicate semantic merge, reopen an integrated bead, or run QG or
review again.

### Checkpoint-owned merge failure

The shared integration function does not reuse the legacy reopen behavior when
called for a durable checkpoint. A conflict transitions `integrating` to
checkpoint-scoped recovery with typed strategy
`resolve_integration_conflict`; a non-conflict merge error uses
`retry_integration` only when target/intent proof makes retry safe. Both paths
preserve the assignment, worktree, approved head, target-before SHA, and merge
proof, and neither completes the assignment nor reopens the bead. Exhausted or
unsafe recovery quarantines only this checkpoint.

`TestReviewIntegrationFailureRecovery` injects both conflict and non-conflict
errors through the production integration function, proves the bead never
enters ordinary ready queues, then proves a safe retry finalizes without QG or
review replay.

## 13. Rejection Recovery

`AssignPayload` gains structured recovery context:

```go
type ReviewRecoveryArtifactRef struct {
    Path         string `json:"path"`
    SHA256       string `json:"sha256"`
    FindingCount int    `json:"finding_count"`
}

type ReviewRecovery struct {
    CheckpointID    int64                        `json:"checkpoint_id"`
    RejectedHeadSHA string                       `json:"rejected_head_sha"`
    Findings        []reviewcontract.Finding     `json:"findings,omitempty"`
    FindingsRef     *ReviewRecoveryArtifactRef   `json:"findings_ref,omitempty"`
    Attempt         int                          `json:"attempt"`
    AcceptanceHash  string                       `json:"acceptance_hash"`
}
```

The existing string `Feedback` remains as a compact rendered compatibility
view, not the source of truth.

### Rejected-checkpoint correction scheduler

Rejected checkpoints never depend on ordinary `beads_ready` assignment.
`reviewMaintenanceLoop` invokes `routeRejectedCheckpoint`:

1. select a due `rejected` checkpoint with no live correction worker;
2. compare-and-swap `rejected -> correction_assigning`;
3. requeue and reuse its checkpoint-owned assignment, or create a replacement
   assignment bound to the preserved worktree when none remains;
4. reserve an idle eligible worker through the worker-pool reservation seam;
5. update `current_assignment_id` and worker ownership transactionally;
6. build the structured payload through `buildAssignPayload`;
7. send `ASSIGN` and transition to `correction_assigned`.

Send or reservation failure releases the worker, requeues the assignment, and
returns the checkpoint to `rejected` with bounded backoff. Heartbeat,
connection-loss, graceful-shutdown, and scale-down paths requeue rather than
complete a checkpoint-owned assignment, then wake this scheduler. The same
worker is an optimization; killing the original worker must still result in a
real replacement assignment carrying identical finding IDs and content.

Before running another full QG, the worker performs a recovery preflight:

1. current HEAD must differ from `RejectedHeadSHA`;
2. current acceptance hash must match the assigned contract;
3. the primary acceptance command must pass;
4. any explicit contract-repair regression command must pass.

If HEAD is unchanged, the worker reports
`FailureReason=review_findings_unaddressed` without running QG. The dispatcher
does not burn a QG attempt or spawn review. It re-delivers the same checkpoint
or escalates after the bounded unchanged-retry threshold.

Generic semantic findings are not automatically converted into shell commands.
They remain structured obligations in the worker prompt and review checkpoint.

## 14. Acceptance-Contract Repair

Findings explicitly marked `acceptance_gap` do not go directly back to a coding
worker. `handleReviewResult` calls
`routeAcceptanceGap(ctx, checkpointID, findings)`. That coordinator:

1. persists the findings;
2. releases worker capacity while preserving the assignment worktree;
3. compare-and-swaps the checkpoint to `contract_repair_running` and creates one
   linked blocking contract-repair ops run in the same transaction;
4. asks the contract agent to produce a complete replacement acceptance
   contract;
5. routes its result to
   `handleContractRepairResult(ctx, checkpointID, opsRunID, result)`;
6. parses and validates the replacement through `pkg/acceptance`;
7. transactionally updates bead acceptance, completes the ops run, marks the old
   checkpoint superseded because `acceptance_hash` changed, and requeues the
   preserved implementation with the revised contract.

Invalid, empty, or failed repair output marks the linked ops run failed and
leaves the checkpoint in a durable failed/recovery state; it never falls through
to coding correction. Startup reconciliation resumes or safely reroutes a
processless `contract_repair_running` run before ordinary assignment.

The contract agent cannot directly close, merge, or silently waive a finding.
False-positive and wont-fix handling continues through durable finding triage.

This is the prevention layer: requirements discovered as missing from the
original bead become executable requirements before another full QG.

## 15. Acceptance Admission

Introduce dependency-neutral `pkg/acceptance` with:

```go
type Contract struct {
    Test       string
    Command    string
    Assert     string
    Read       []string
    Signature string
    Edges      []string
}

func Parse(raw string) (Contract, error)
func ValidateWorkerContract(contract Contract, repoRoot string) error
```

This is the one line-aware parser and validator shared by:

- `oro task create`;
- `oro task update --acceptance`;
- `oro work` no-commit preflight through `acAlreadySatisfied`;
- dispatcher `checkBeadReady`;
- contract repair;
- review prompt command extraction.

The parser must preserve shell pipes and quoted expressions inside `Cmd:`. It
must not split acceptance text on every `|`.

`cmd/oro:runBeadCreate`, `newBeadUpdateCmd`, dispatcher `checkBeadReady`,
`cmd/oro:acAlreadySatisfied`, contract repair, and
`pkg/ops:buildReviewPrompt`/`acceptanceCommand` must call this package.
`parseACCmd` and `parseACTestFile` are removed. No consumer keeps a private
parser or raw pipe split.

Worker-executable tasks require:

- non-empty `Test:`;
- non-empty `Cmd:`;
- non-empty `Assert:`;
- non-empty `Read:`;
- a non-vacuous command;
- a test reference and assertion specific enough to distinguish pass from
  no-op;
- a parseable command field;
- valid repository-relative `Read:` paths.

`Signature:` and `Edges:` remain conditionally required by beadcraft and
contract review rather than guessed by a syntax parser.

Legacy beads that fail the stricter contract are routed to the existing AC
repair path once, not repeatedly offered to workers.

## 16. Startup and Runtime Recovery

Startup recovery uses this mandatory order:

1. initialize and migrate schema;
2. validate persisted worktree and branch ownership;
3. restore assignments, `worktreeByBead`, target branches, checkpoint identity,
   READY attempts, and evidence references;
4. reconcile `integrating` checkpoints and external merge proof;
5. reconcile review, recovery, and contract ops runs;
6. route pending ops/escalation work only after its context is available;
7. reset orphaned beads only when no non-terminal checkpoint owns the bead;
8. start ordinary assignment.

When an ops run is superseded but its replacement cannot route, the replacement
is completed `failed` transactionally and a scheduled recovery attempt is
created. Startup may not leave a processless `running` ops run.

`beads_ready`, `statusQueueBeads`, and `tryAssign` all exclude a bead with a
checkpoint in `qg_passed`, `review_running`, `rejected`,
`correction_assigning`, `correction_assigned`, `contract_repair_running`,
`blocked`, `failed`, `recovery_running`, `quarantined`, `approved`, or
`manual_integration_pending`, or `integrating`. The dispatcher rechecks this
condition immediately before assignment to close view/query races. Review
quarantine is checkpoint-scoped; it cannot trigger the existing global
recovery quarantine behavior, and unrelated ready beads continue dispatching.

### Graceful shutdown, scale-down, and heartbeat loss

One helper, `releaseCheckpointOwnedWorker`, is used by
`handleShutdownApproved`, `shutdownSequence`, `shutdownResetActiveBeads`,
`requeueAssignmentForShutdown`, `connCloseCleanup`, `sendToWorker` write-failure
removal, heartbeat timeout handling, scale-down, `shutdownWithTimeout`,
`applyKillWorker`, `applyRestartWorker`, and
`restartWorkerIfStillOnBead` (including focus `--immediate`). Every production
`delete(d.workers, ...)`, `clearBeadTracking`, or bead-to-open path must first
use this helper when a non-terminal checkpoint owns the bead. In that case the
helper:

- never completes the assignment or reopens the bead;
- requeues the current assignment while preserving worktree, branch, and
  evidence;
- clears only worker ownership and records the checkpoint's exact phase;
- wakes review, correction, or recovery routing for the next process or
  startup.

Shutdown reset is idempotent with the earlier shutdown-approved transition.
`TestReviewCheckpointGracefulShutdownRecovery` covers `awaiting_review_ack`,
`review_running`, `correction_assigned`, and `integrating`, followed by restart
and phase-local continuation. `TestReviewCheckpointSocketEOFRecovery` closes a
real `handleConn` socket during each phase and proves exact-phase requeue without
ordinary reopen. `TestReviewCheckpointSendFailureRecovery` forces a socket write
failure through `sendToWorker` and proves the same invariant.

Administrative kill, restart, focus-preemption, and hard shutdown may replace
or stop a process, but they never complete the checkpoint-owned assignment,
reopen the bead, or reset phase. They durably release ownership and wake the
phase-specific scheduler. `TestReviewCheckpointAdministrativeWorkerLifecycle`
drives the real directive, focus-immediate, and hard-timeout entry points in
`review_running`, `correction_assigned`, and `integrating`, then proves
phase-local recovery.

### Handoff and generic preempt

Worker context exhaustion and `oro directive preempt` enter
`worker.handleContextThreshold/handlePreempt -> SendHandoff ->
dispatcher.handleHandoff`. `applyPreempt`, `shutdownWorkerForHandoff`, and
`respawnWorker` inspect checkpoint ownership before creating an ordinary pending
handoff:

- `awaiting_review_ack` releases the process and replays the preserved evidence
  through READY recovery;
- `review_running` releases the implementation worker while the dispatcher-owned
  review continues;
- `correction_assigning` or `correction_assigned` requeues the
  checkpoint-owned correction assignment and wakes `routeRejectedCheckpoint`;
- `contract_repair_running`, `manual_integration_pending`, or `integrating`
  releases the process and wakes the owning dispatcher phase;
- no non-terminal checkpoint enters the generic `pendingHandoffs` map.

`worker_pool.assignHandoffToWorker` remains for ordinary implementation
handoffs, but it must call `buildAssignPayload` or the same canonical payload
builder rather than constructing a private `AssignPayload`. It therefore always
delivers `QGEvidenceDir`; any future checkpoint recovery fields added to the
canonical builder cannot drift. Checkpoint-owned correction uses the
phase-specific scheduler and carries exact `ReviewRecovery`.

`TestReviewCheckpointHandoffPreemptRecovery` drives both an organic context
handoff and the real generic preempt directive over dispatcher/worker sockets.
Its first case hands off during ordinary pre-QG implementation, traverses the
real `pendingHandoffs -> assignHandoffToWorker -> worker.handleAssign` path,
requires the preserved positive assignment ID, canonical evidence directory,
and target identity, then completes READY from the derived path. Its
`review_running` and `correction_assigned` cases kill the original process,
prove no ordinary reassignment or QG replay, and require a replacement
correction worker to receive identical finding IDs/content plus the canonical
evidence directory.

### External bead close

`checkClosedBeadAssignments -> shutdownWorkerForClose ->
finalizeExternalClose` must not use the current recovery merge path for a bead
owned by a non-terminal checkpoint. Its scan includes every checkpoint-owned
worker regardless of `WorkerBusy`, `WorkerReserved`, `WorkerReviewing`, or
phase-specific state; it cannot omit review ownership because of the current
busy/reserved filter.

- If an `approved` or `integrating` checkpoint already has ancestry proof that
  its approved head reached the target, normal integration reconciliation
  completes it idempotently.
- Otherwise, the observed external close is an explicit no-merge override:
  transactionally set the checkpoint to `superseded`, record
  `override_kind=external_close`, its source, and timestamp, complete or release
  the assignment without reopening, and preserve branch/worktree/evidence for
  retention and audit.
- An external close never invokes a semantic merge for a rejected, blocked,
  quarantined, review-running, correction, or unproven integrating checkpoint.

`TestReviewCheckpointExternalCloseOverride` drives all three production
functions, proves no unapproved merge occurs, and proves restart retains the
override audit. This is an optional operator override, not a recovery
requirement or routine intervention path.

For every non-terminal checkpoint:

### `qg_passed` or `review_running`

- verify worktree, branch, HEAD, acceptance hash, and QG evidence;
- reconcile the linked review ops run;
- reroute review exactly once if the prior process is dead;
- never run worker QG again for the same checkpoint key.

### `rejected`

- keep or reactivate the preserved assignment;
- wake `routeRejectedCheckpoint`;
- make the bead assignable only through the correction scheduler with structured
  review recovery context;
- if a worker reconnects on the same assignment, replay the compact findings.

### `correction_assigning` or `correction_assigned`

- reconcile reservation, assignment, and worker ownership;
- finish or retry an interrupted payload send idempotently;
- if the worker is gone, requeue the assignment and return to `rejected`;
- never expose the bead through the ordinary ready queue.

### `contract_repair_running`

- reconcile the linked contract ops run;
- ordinary worker assignment remains blocked.

### `approved`, `manual_integration_pending`, or `integrating`

- verify the immutable checkpoint;
- preserve manual-integration mode and wait for ancestry proof when configured;
- reconcile integration intent and merge proof, then resume without the
  original worker;
- run dispatcher QG if evidence cannot be trusted;
- do not rerun review.

### `blocked` or `failed`

- preserve the worktree and assignment;
- keep the bead out of the ordinary queue;
- reconcile or create the autonomous recovery run;
- apply persisted backoff and attempt budgets by failure fingerprint;
- expose the current automatic strategy and next retry.

### `recovery_running`

- reconcile the linked recovery ops run;
- resume or reroute it once if its process is dead;
- on success, return to the exact blocked phase rather than restarting the
  implementation pipeline;
- on exhaustion, invoke the bounded recovery planner or quarantine.

### `quarantined`

- preserve the worktree, assignment, checkpoint, and all structured evidence;
- keep the bead out of the ordinary queue;
- continue dispatching unrelated beads;
- mark project health degraded and include the bead in every status snapshot;
- emit a deduplicated reminder on a configurable recurring cadence until the
  checkpoint leaves quarantine;
- automatically transition back to recovery when a relevant input fingerprint
  changes;
- expose the terminal reason and optional administrative override without making
  operator action a prerequisite for factory health.

### Any identity mismatch

- mark the checkpoint superseded;
- require new QG/review for changed code or contract;
- recovery-quarantine ambiguous branch/worktree ownership.

## 17. Review Cache and Invalidation

A terminal checkpoint may be reused only when its full key matches.

Invalidators include:

- branch HEAD change;
- target HEAD change;
- acceptance criteria change;
- QG script change;
- QG mode change;
- review prompt/policy version change;
- reviewer configuration change when it affects the policy hash;
- triage state change that alters gating.

Effects:

- same rejected key -> re-deliver findings, no QG/review;
- same approved key -> resume integration, no review;
- changed key -> supersede and run the necessary pipeline stages.

## 18. Observability

Add compact events:

- `review_checkpoint_created`
- `review_checkpoint_reused`
- `review_checkpoint_superseded`
- `review_checkpoint_recovered`
- `review_findings_delivered`
- `review_unchanged_retry_blocked`
- `review_contract_gap`
- `review_contract_repaired`
- `review_integration_resumed`
- `review_artifact_truncated`
- `review_recovery_started`
- `review_recovery_retried`
- `review_recovery_succeeded`
- `review_recovery_exhausted`
- `review_checkpoint_quarantined`
- `review_quarantine_reminder`
- `review_quarantine_reactivated`

The durable state-DB event row is the delivery source of truth. A reminder
scheduler claims `review_quarantine_deliveries(checkpoint_id,scheduled_at,sink)`
with an insert-or-ignore transaction, appends the event, and marks delivery
complete. The built-in sinks are dispatcher event stream/daemon log and monitor
output; configured external sinks may subscribe later but are not required for
correctness. An ACK lost after event persistence is deduplicated by the delivery
primary key.

Status/health metrics:

- review checkpoints by state;
- oldest non-terminal checkpoint;
- review cache hits;
- unchanged retries prevented;
- review attempts per checkpoint;
- review execution duration;
- compact payload bytes vs raw artifact bytes;
- failed/stale review ops runs;
- approved-but-not-integrated checkpoints.

Every operator-facing progress or status response must include an active
quarantine summary even when no reminder is currently due. This applies to
`oro status` human/JSON, `oro health` online/offline human/JSON,
`oro throughput`, the daemon status response, and every `oro monitor` iteration.
Each summary includes bead ID, checkpoint state, compact reason, quarantine age,
reminder count, attempted strategies, last attempt, next automatic reactivation
condition, and whether unrelated work is still progressing. All online and
offline views load the same `factoryhealth` quarantine model from the state DB.

Throughput reporting should distinguish:

- review executions;
- checkpoint reuses;
- productive rejections delivered to a worker;
- blocked infra reviews;
- autonomous recovery attempts and success rate;
- quarantined checkpoints by stable failure fingerprint;
- repeated unchanged attempts prevented.

## 19. Backward Compatibility

- Existing `Verdict` values and `<-chan Result` remain.
- Existing `Feedback` remains as a bounded compatibility rendering.
- Missing `ReadyForReview.QGEvidence` or `QGEvidenceRef` selects the
  legacy-compatible path and forces dispatcher QG before merge.
- Legacy READY creates a provisional checkpoint using explicit
  `legacy-unverified` sentinels and a canonical key; duplicate legacy READY
  messages reuse it and cannot spawn concurrent reviews.
- Legacy rejection findings or bounded feedback are persisted before worker
  notification and survive dispatcher restart even though QG evidence is
  untrusted.
- Legacy exact prose verdicts synthesize a typed outcome conservatively.
- Existing finding journey and triage formats remain readable.
- Existing `rejection_history` may continue receiving compact summaries during
  rollout, but it is no longer authoritative.
- Old workers that echo passing `DONE` after approval cannot cause duplicate
  integration.
- Rolling upgrades are supported through explicit worker capability
  negotiation. Current workers advertise protocol version 1 and
  `ready-evidence-v1` in both heartbeat and reconnect messages.
- A legacy worker with a durable active or requeued assignment may reconnect
  and finish that assignment through the legacy-compatible path, but is marked
  for drain and cannot receive a handoff or a new assignment. Once it becomes
  idle, the dispatcher sends `SHUTDOWN`.
- A legacy or incompatible worker without an existing assignment receives
  `SHUTDOWN`, its connection is closed, and a durable
  `worker_protocol_drained` event records the required version and capability.
  Incompatible idle workers are never silently left connected.
- New ACK fields remain optional on the wire during the drain window. Assignment
  and checkpoint CAS make mixed-version duplicate completion harmless.

## 20. Testing Strategy

### Pure outcome tests

- A rejected report containing `TaskOutput`, `tail -f`, or permission-denied
  text remains rejected and retains findings.
- A typed blocker with no findings becomes blocked.
- A valid rejection plus nonzero process exit remains safely rejected.
- An approval plus nonzero exit fails closed.
- a failed or missing required persona prevents approval;
- a valid gating finding from a surviving persona still rejects when another
  required persona fails;
- docs-only approval emits a complete typed outcome and policy hash;
- dropped Critical/Important candidates cannot produce approval;
- Raw artifact text is never passed to the classifier.
- a fully populated shared finding survives `AssignPayload` JSON round-trip with
  exact contract impact and required action.

### Artifact tests

- multi-megabyte stream output produces a compact result and an artifact
  reference;
- event, ops-run, checkpoint, and `ASSIGN` payloads remain below their caps;
- truncation is explicit and does not change the decision;
- artifacts use project-scoped paths and restrictive permissions.
- create/write/fsync/hash failure fails approval closed;
- janitor never removes an artifact referenced by a non-terminal checkpoint;
- janitor treats only `integrated` and `superseded` as terminal and retains
  approved, rejected, failed, blocked, quarantined, and manual-pending artifacts
  in `TestReviewArtifactTerminalStateMatrix`;
- oversized findings switch to a hash-verified recovery artifact without
  dropping gating obligations.
- `TestReviewArtifactAndFindingOverflow` restarts before correction delivery and
  reconstructs the exact persisted recovery reference without compact-row
  regeneration;
- QG evidence is written outside an unconfigured temporary Git worktree and
  does not weaken dirty-file detection.
- `TestReadyEvidenceProductionAssignPath` proves the real assignment builder and
  socket deliver the configured directory without direct payload seeding, and
  initial READY inline/file evidence matches exactly.
- `TestReadyEvidenceIdentityValidation` rejects content, hash, run, assignment,
  target timing/ancestry, and path mismatches into target refresh or
  dispatcher-owned legacy QG.

### Checkpoint store tests

- duplicate immutable keys create one active checkpoint;
- duplicate legacy keys with sentinel values create one active checkpoint;
- compare-and-swap rejects stale state transitions;
- changed HEAD, acceptance, target, QG script, or policy supersedes the prior
  checkpoint;
- reviewer configuration and triage revision invalidate reuse;
- terminal rejected/approved data survives database reopen.

### Dispatcher lifecycle tests

- normal rejection sends exact structured findings;
- env/infra keyword pollution cannot route to blocked;
- blocked review preserves work, creates a failed review ops run, and schedules
  autonomous recovery without reopening into the ready queue;
- every review outcome completes the linked ops run;
- a replacement ops run that cannot route is failed and scheduled, never left
  processless and running;
- approval integrates without worker `DONE`;
- manual integration merges while one dispatcher stays alive and the production
  maintenance loop discovers ancestry and finalizes without restart;
- READY ACK is consumed through `worker.Run/handleMessage`, and phase-aware
  reconnect is consumed through dispatcher `handleReconnect`;
- live lost ACK retransmits the identical READY on a bounded timer through
  `TestReadyForReviewAckLiveRetransmit`;
- legacy READY without evidence forces dispatcher QG before merge in
  `TestLegacyReadyForReviewForcesDispatcherQG`;
- an old-worker duplicate `DONE` is harmless.

### Recovery tests

- dispatcher death during review reroutes one review and no QG;
- death after READY socket receipt but before checkpoint commit is repaired by
  READY resend or evidence-file recovery;
- lost READY ACK causes idempotent replay and one review;
- worker death during review does not lose QG evidence;
- worker death after rejection reassigns findings to a different worker;
- `TestReviewFindingsReplacementWorker` kills the original worker and observes
  the correction scheduler assign exact findings to a real replacement;
- `TestReviewCheckpointGracefulShutdownRecovery` proves stop, scale-down, and
  heartbeat loss requeue checkpoint-owned assignments without reopening;
- `TestReviewCheckpointSocketEOFRecovery` and
  `TestReviewCheckpointSendFailureRecovery` prove live teardown cannot reopen
  checkpoint-owned work;
- `TestReviewCheckpointAdministrativeWorkerLifecycle` proves kill, restart,
  focus-immediate, and hard shutdown preserve exact checkpoint phase;
- `TestReviewCheckpointHandoffPreemptRecovery` proves organic handoff and
  generic preempt preserve the canonical ordinary pre-QG assignment contract
  plus checkpoint phase, findings, and evidence over replacement sockets;
- `TestReviewCheckpointExternalCloseOverride` proves external close cannot merge
  unapproved work and persists its no-merge override;
- dispatcher death after approval resumes integration and does not rerun review;
- startup restores worktree/checkpoint context before ops-run reconciliation;
- non-terminal checkpoint states cannot enter `beads_ready`,
  `statusQueueBeads`, or `tryAssign`;
- death before merge, after merge, after merge-proof persistence, after
  assignment completion, and after bead close resumes idempotently;
- conflict and non-conflict integration failures remain checkpoint-scoped,
  preserve proof/work, and never reopen through
  `TestReviewIntegrationFailureRecovery`;
- target movement after approval and at merge compare forces target-bound
  QG/review through `TestReviewIntegrationTargetInvalidation`;
- no-op and epic-rebase-child outcomes finalize checkpoint proof through
  `TestReviewIntegrationSpecialBranches`, including atomic expected-ref failure;
- changed HEAD invalidates the checkpoint;
- recovery attempts and backoff survive dispatcher restart;
- changing worker/process identity does not reset a failure fingerprint budget;
- a successful repair resumes the blocked phase without rerunning worker QG;
- exhausted recovery quarantines one bead while unrelated beads continue;
- quarantine reminder cadence and deduplication survive dispatcher restart;
- quarantine emits immediately, every 15 minutes for the first hour, and hourly
  thereafter;
- every progress/status response includes active quarantine summaries even
  between scheduled reminders;
- relevant toolchain, config, provider-health, target, contract, or capability
  changes reactivate recovery automatically;
- unsafe worktree state recovery-quarantines instead of deleting work.
- `oro status`, `oro health` online/offline, `oro throughput`, daemon status,
  and `oro monitor` human/JSON surfaces agree on quarantine details.
- the scheduled artifact janitor and quarantine reminder loop run from
  `spawnBackgroundLoops` with restart-safe claims.

### Contract tests

- line-aware AC parsing preserves shell pipes;
- missing `Read`, vacuous `Cmd`, or missing `Assert` is rejected before
  assignment;
- `oro work` no-commit preflight executes a pipeline command intact through
  `acAlreadySatisfied` and has no private parser;
- `TestReviewAcceptanceGapContractRepair` sends an `acceptance_gap` outcome
  through `handleReviewResult`, proves coding correction remains blocked, proves
  a valid deterministic replacement updates acceptance, completes the ops run,
  supersedes the old checkpoint, and requeues preserved work, then proves
  invalid output leaves a failed ops run and no worker loop.

### End-to-end proof

The epic acceptance command is:

```bash
bash -euo pipefail -c 'test "$(git branch --show-current)" = main; tests="TestReviewFindingWireRoundTrip TestReviewOutcomeRequiredCoverage TestReviewArtifactAndFindingOverflow TestReviewArtifactJanitorScheduled TestReviewArtifactTerminalStateMatrix TestReadyForReviewAckReplay TestReadyForReviewAckLiveRetransmit TestReadyEvidenceDoesNotDirtyWorktree TestReadyEvidenceProductionAssignPath TestReadyEvidenceIdentityValidation TestLegacyReadyForReviewForcesDispatcherQG TestReviewCheckpointCanonicalKeyMigration TestReviewOpsRunCheckpointTransaction TestReviewCheckpointStartupOrdering TestReviewCheckpointGracefulShutdownRecovery TestReviewCheckpointSocketEOFRecovery TestReviewCheckpointSendFailureRecovery TestReviewCheckpointAdministrativeWorkerLifecycle TestReviewCheckpointExternalCloseOverride TestReviewCheckpointHandoffPreemptRecovery TestReviewIntegrationCrashRecovery TestReviewIntegrationFailureRecovery TestReviewIntegrationTargetInvalidation TestReviewIntegrationSpecialBranches TestReviewApprovedManualIntegrationPreserved TestReviewFindingsReplacementWorker TestReviewRecoveryBudgetAndReactivation TestReviewQuarantineReminderScheduler TestAcceptanceContractSharedParser TestReviewAcceptanceGapContractRepair TestReviewQuarantineSurfaceParity TestDurableReviewCheckpointEndToEnd TestDurableReviewCheckpointProductionComposition"; pattern="^($(tr " " "|" <<<"$tests"))$"; out=$(go test -v -count=1 -timeout=300s ./pkg/... ./cmd/oro -run "$pattern"); for name in $tests; do grep -Fq -- "--- PASS: $name" <<<"$out"; done'
```

Assert: exit code 0 and all 33 exact named tests emit PASS markers on `main`.

`TestDurableReviewCheckpointEndToEnd` exercises:

1. production-built `ASSIGN` carrying the canonical evidence directory,
   immutable assignment ID, exact assignment-scoped path, initial QG pass,
   fsynced evidence, accepted READY, and a clean worktree, followed by rejection
   of content/hash/run/assignment/path mismatches, stale/moving target identity,
   and non-ancestor target/head;
2. dispatcher death after READY receipt but before checkpoint commit;
3. lost ACK on a live socket, bounded identical retransmit, phase-aware
   reconnect through both production event loops, one canonical checkpoint,
   and one review;
4. structured rejection with misleading infrastructure keywords in the raw
   transcript;
5. original-worker death and exact inline or referenced findings delivered by
   the correction scheduler to a replacement worker;
6. unchanged retry prevented before QG;
7. changed retry producing new QG evidence and checkpoint key;
8. dispatcher restart during review with worktree restoration before ops
   reroute;
9. graceful stop during review requeues checkpoint ownership and resumes after
   restart;
10. live socket EOF, write failure, administrative kill/restart,
    focus-immediate, hard shutdown, organic handoff, generic preempt, and
    external close preserving or durably overriding exact checkpoint phase
    without unapproved merge; post-handoff correction `ASSIGN` must contain
    byte-identical findings and the canonical evidence directory, and ordinary
    pre-QG handoff must reach real `worker.handleAssign` with assignment/target
    identity before READY;
11. failed required persona preventing approval, typed docs-only approval, and
    legacy READY forcing dispatcher QG;
12. acceptance-gap contract repair blocking coding, accepting a valid revision,
    superseding the old checkpoint, executing its pipeline intact through
    `oro work` no-commit preflight, and failing invalid repair safely;
13. automatic approval and integration without the original worker;
14. manual-integration approval preserving the worktree, a manual merge while
    the same dispatcher remains alive, and maintenance-loop ancestry
    reconciliation;
15. dispatcher death after git merge but before merge-proof persistence, then
    idempotent assignment/bead finalization without QG or review replay, plus
    target invalidation, checkpoint-scoped conflict and non-conflict
    merge-failure recovery, no-op integration, and epic-rebase-child
    finalization with an injected atomic ref-compare race;
16. bounded autonomous failure recovery, checkpoint-scoped quarantine,
    unrelated bead dispatch, fake-clock reminder scheduling, input-change
    reactivation, and the full artifact terminal-state matrix before a due
    maintenance sweep;
17. zero active assignments, failed or processless running review ops runs,
    non-terminal review checkpoints, or unclosed target bead at completion.

`TestDurableReviewCheckpointProductionComposition` invokes
`newRootCmd -> newStartCmd -> runDaemonOnly/startFreshSwarm ->
buildDispatcherWithReviewTimeoutsAndCleanliness -> dispatcher.New -> Run`.
It must not construct `dispatcher.Config` or call the builder directly. It may
replace only external worker-process and listener lifecycle seams after the
production factory has constructed the dispatcher. With a temporary
`ORO_HOME`/project it proves `ResolveDaemonPaths` supplies distinct artifact and
evidence directories; `ReviewArtifactDir`, `ReviewEvidenceDir`,
`ReviewMaintenanceInterval`, `ReviewArtifactRetention`, and
`ReviewArtifactSweepInterval` reach the dispatcher; and the real startup path
starts the maintenance scheduler. The proof observes one scheduled reminder and
one due terminal-artifact deletion rather than merely inspecting fields.

`TestReviewQuarantineSurfaceParity` lives in `cmd/oro` and drives the real
online and offline status, health, throughput, daemon-status, and monitor
handlers against one state database between reminder deadlines. It requires
identical active quarantine identity, reason, cadence, and unrelated-progress
fields from every human/JSON model.

Beadcraft must place this command verbatim on the epic and assign the named
production-wiring tests to the final integration beads.

### Required beadcraft work packages

At the design-review gate these were task placeholders. Stage 4 beadcraft
assigned them under `drc-factory`, recursively split each to the Rule-of-Five
size limit, and preserved the listed production call sites in child `Read:`
fields.

| Work package | Required production wiring | Required named proof |
|---|---|---|
| Shared finding wire contract | new `pkg/reviewcontract`, `pkg/ops/finding.go`, `pkg/protocol/message.go:AssignPayload.ReviewRecovery`, worker decode | `TestReviewFindingWireRoundTrip` |
| Typed review contract | `pkg/ops/ops.go:Result/Review`, `review_parse.go`, `review_validation.go`, `review_merge.go`, `personas.go`, shared finding model | `TestReviewOutcomeRequiredCoverage` |
| Bounded artifacts and findings | `pkg/ops/exec_spawner.go`, `pkg/ops/finding.go`, checkpoint recovery-artifact reference, dispatcher config/path injection, `spawnBackgroundLoops/reviewMaintenanceLoop` | `TestReviewArtifactAndFindingOverflow`, `TestReviewArtifactJanitorScheduled`, `TestReviewArtifactTerminalStateMatrix` |
| READY evidence handshake | `pkg/worker/worker.go:handleAssign/awaitSubprocessAndReport/rebaseOntoTarget/runQGAndReport/reconnect/handleMessage/handleReadyForReviewAck/evidence writer/ACK timer`, `buffer.go`, `pkg/protocol/message.go:AssignPayload/QGEvidence`, `pkg/dispatcher/dispatcher.go:assignBead`, `assign_payload.go:buildAssignPayload`, `worker_pool.go:assignHandoffToWorker`, dispatcher `handleReadyForReview/handleReconnect/processReconnectUnderLock/checkPreReviewGitHygiene`, `cmd/oro:ResolveDaemonPaths/buildDispatcherWithReviewTimeoutsAndCleanliness` | `TestReadyForReviewAckReplay`, `TestReadyForReviewAckLiveRetransmit`, `TestReadyEvidenceDoesNotDirtyWorktree`, `TestReadyEvidenceProductionAssignPath`, `TestReadyEvidenceIdentityValidation`, `TestLegacyReadyForReviewForcesDispatcherQG` |
| Checkpoint schema/store | `pkg/protocol/schema.go:SchemaDDL/MigrateBeadSchema/beads_ready` plus checkpoint repository | `TestReviewCheckpointCanonicalKeyMigration` |
| Review ops lifecycle | `dispatcher.handleReadyForReview/handleReviewResult`, `ops_runs.go` | `TestReviewOpsRunCheckpointTransaction` |
| Startup and queue recovery | `dispatcher.Run/startupRecovery/restoreState/tryAssign/statusQueueBeads/connCloseCleanup/shutdownWithTimeout/applyKillWorker/applyRestartWorker/restartWorkerIfStillOnBead/checkClosedBeadAssignments/shutdownWorkerForClose/finalizeExternalClose`, `worker_pool.go:sendToWorker/heartbeat/scale-down`, `handleShutdownApproved/shutdownSequence/shutdownResetActiveBeads/requeueAssignmentForShutdown` | `TestReviewCheckpointStartupOrdering`, `TestReviewCheckpointGracefulShutdownRecovery`, `TestReviewCheckpointSocketEOFRecovery`, `TestReviewCheckpointSendFailureRecovery`, `TestReviewCheckpointAdministrativeWorkerLifecycle`, `TestReviewCheckpointExternalCloseOverride` |
| Checkpoint handoff and preempt | `pkg/worker/worker.go:handleContextThreshold/handlePreempt/SendHandoff`, dispatcher `applyPreempt/handleHandoff/shutdownWorkerForHandoff/respawnWorker`, `worker_pool.go:assignHandoffToWorker`, `releaseCheckpointOwnedWorker` and phase schedulers | `TestReviewCheckpointHandoffPreemptRecovery` |
| Dispatcher-owned integration | `handleDone/beginCheckpointIntegration/mergeAndComplete/checkPreMergeQG/handleNoopMerge/completeEpicRebaseChild/finalizeSuccessfulMerge`, `pkg/merge/merge.go:Opts/Coordinator.Merge/ErrTargetMoved`, `pkg/dispatcher/dispatcher.go:WorktreeManager`, `worktree_manager.go:GitWorktreeManager.UpdateBranchRef`, checkpoint merge-error routing, `spawnBackgroundLoops/reviewMaintenanceLoop/reconcileManualIntegrationCheckpoint` | `TestReviewIntegrationCrashRecovery`, `TestReviewIntegrationFailureRecovery`, `TestReviewIntegrationTargetInvalidation`, `TestReviewIntegrationSpecialBranches`, `TestReviewApprovedManualIntegrationPreserved` |
| Structured rejection delivery | `routeRejectedCheckpoint`, worker-pool reservation/heartbeat paths, `assign_payload.go:buildAssignPayload`, worker recovery preflight | `TestReviewFindingsReplacementWorker` |
| Autonomous recovery | recovery action/store/executor, checkpoint-scoped quarantine, fingerprint sampler, `spawnBackgroundLoops/reviewMaintenanceLoop` | `TestReviewRecoveryBudgetAndReactivation`, `TestReviewQuarantineReminderScheduler` |
| Acceptance repair/admission | new `pkg/acceptance`, task create/update, `cmd/oro/cmd_work.go:acAlreadySatisfied` with private parser removal, `checkBeadReady`, `handleReviewResult/routeAcceptanceGap/handleContractRepairResult`, contract ops/startup reconciliation, review prompt | `TestAcceptanceContractSharedParser`, `TestReviewAcceptanceGapContractRepair` |
| Quarantine observability | `factoryhealth`, dispatcher health/events, status/health/throughput/monitor online and offline paths | `TestReviewQuarantineSurfaceParity` |
| Dispatcher epic proof | production dispatcher/worker sockets, review/checkpoint/recovery/integration stores, CLI surface adapters | `TestDurableReviewCheckpointEndToEnd` |
| CLI production composition | `cmd/oro:main/newRootCmd/newStartCmd/runDaemonOnly/startFreshSwarm/buildDispatcherWithReviewTimeoutsAndCleanliness`, `paths.go:ResolveDaemonPaths`, real dispatcher `Run/spawnBackgroundLoops` | `TestDurableReviewCheckpointProductionComposition` |

Dependency order is: contracts/schema -> protocol/artifacts -> review and ops
lifecycle -> startup/integration/rejection -> recovery/contract/observability ->
production composition and epic proof. The epic depends on every leaf task.

## 21. Rollout

1. Add typed outcomes, required-persona aggregation, docs-only typing, and
   bounded artifacts while keeping existing dispatcher behavior.
2. Add canonical checkpoint schema, normalized findings, QG evidence files, and
   READY ACK/replay.
3. Switch dispatcher review routing to structured outcomes and transactionally
   linked ops runs.
4. Move evidence-backed approval completion to the dispatcher with durable
   integration intent and merge proof.
5. Reorder startup and add recovery/queue exclusion for each checkpoint state.
6. Add structured rejection recovery, overflow references, and unchanged-head
   preflight.
7. Add typed autonomous recovery, fingerprint reactivation, and
   checkpoint-scoped quarantine.
8. Add shared acceptance parsing, contract repair, and stricter admission.
9. Add health/status/throughput/reminder reporting.
10. Add the exact bounded dispatcher and CLI-composition crash/restart proofs.

Each phase is reversible until the schema/checkpoint path becomes authoritative.
During rollout, missing new fields select conservative legacy fallbacks.

## 22. Deep Premortem

```yaml
premortem:
  mode: deep
  context: "durable review checkpoints and worker recovery"

  tigers:
    - risk: "READY is accepted by the socket but lost before checkpoint commit."
      location: "pkg/worker/worker.go:SendReadyForReview/reconnect; pkg/dispatcher/dispatcher.go:handleReadyForReview"
      severity: high
      mitigation_checked: "Current protocol has no READY ACK and reconnect reports idle when proc is nil. Design adds fsynced evidence with an explicit initial file reference, inline/file equality, awaiting_review_ack phase, commit-before-ACK, and idempotent replay."

    - risk: "Durable READY evidence makes every clean worktree appear dirty."
      location: "pkg/worker/worker.go:runQGAndReport; pkg/dispatcher/dispatcher.go:checkPreReviewGitHygiene"
      severity: high
      mitigation_checked: "Evidence is stored under the project state directory passed by production config, never under the worktree, and a temporary repository without ignore rules must remain clean."

    - risk: "Production config has an evidence directory but ASSIGN never delivers it."
      location: "pkg/dispatcher/assign_payload.go:buildAssignPayload; pkg/worker/worker.go:handleAssign"
      severity: high
      mitigation_checked: "A real dispatcher/worker socket test forbids direct payload seeding, asserts AssignmentID and canonical QGEvidenceDir on ASSIGN, and follows the exact assignment-scoped evidence path through READY acceptance."

    - risk: "Matching happy-path evidence hides ignored content, hash, run, assignment, or path mismatches."
      location: "pkg/dispatcher/dispatcher.go:handleReadyForReview"
      severity: high
      mitigation_checked: "Named negative proof submits every mismatch class plus stale/moving/non-ancestor target evidence and requires target refresh or untrusted legacy handling plus dispatcher-owned QG."

    - risk: "A dropped READY ACK on a live socket waits forever for reconnect."
      location: "pkg/worker/worker.go:runQGAndReport/handleReadyForReviewAck"
      severity: high
      mitigation_checked: "Non-evictable READY state retransmits the identical attempt on a bounded 5-to-30-second timer; live-socket proof requires one checkpoint and one review."

    - risk: "Worker ignores pre-QG rebase failure or records a target that moved after QG."
      location: "pkg/worker/worker.go:awaitSubprocessAndReport/rebaseOntoTarget/runQGAndReport"
      severity: high
      mitigation_checked: "Assignment captures target SHA; worker rebases onto that exact commit, proves ancestry before QG, never resamples after QG, and READY compares evidence to assignment and live target."

    - risk: "Ops and protocol use different finding models or create an import cycle."
      location: "pkg/ops/finding.go; pkg/ops/ops.go; pkg/protocol/message.go"
      severity: high
      mitigation_checked: "A dependency-neutral pkg/reviewcontract owns the only model, and a full JSON wire round-trip protects every recovery field."

    - risk: "Git merge succeeds but dispatcher death loses integration progress."
      location: "pkg/dispatcher/dispatcher.go:handleDone/mergeAndComplete"
      severity: high
      mitigation_checked: "Current integration has no persisted target proof. Design stores intent, target-before, approved head, observed target, and stepwise idempotent finalization."

    - risk: "Partial multi-persona failure still produces approval."
      location: "pkg/ops/ops.go:collectPersonaReviews; pkg/ops/review_merge.go:mergeReports"
      severity: high
      mitigation_checked: "Current merge flattens surviving reports. Design hashes required coverage and forbids approval unless every required persona succeeds."

    - risk: "Startup reroutes review before assignment/worktree context is restored."
      location: "pkg/dispatcher/dispatcher.go:startupRecovery; pkg/dispatcher/ops_runs.go:routeReviewOpsRun"
      severity: high
      mitigation_checked: "Current startup reconciles ops runs before restoreState. Design mandates restore and integration reconciliation before ops reroute, with failed rollback when routing cannot start."

    - risk: "Rejected findings are durable but no replacement worker is ever assigned."
      location: "pkg/dispatcher/dispatcher.go:handleReviewRejection; pkg/dispatcher/worker_pool.go:checkHeartbeats"
      severity: high
      mitigation_checked: "Current rejection resends only to the same worker, while ordinary ready assignment is excluded. Design adds a correction scheduler with CAS, assignment requeue/replacement, reservation, and real replacement-worker proof."

    - risk: "Graceful shutdown completes a checkpoint-owned assignment and breaks restart ownership."
      location: "pkg/dispatcher/dispatcher.go:handleShutdownApproved/shutdownSequence/shutdownResetActiveBeads"
      severity: high
      mitigation_checked: "Current shutdown can complete the assignment. Design centralizes checkpoint-aware release that requeues and preserves exact phase across stop, scale-down, heartbeat, and connection loss."

    - risk: "Unexpected socket EOF or write failure reopens checkpoint-owned work."
      location: "pkg/dispatcher/dispatcher.go:connCloseCleanup; pkg/dispatcher/worker_pool.go:sendToWorker"
      severity: high
      mitigation_checked: "Both teardown paths and every worker deletion/open transition use releaseCheckpointOwnedWorker, with real EOF and write-failure proofs."

    - risk: "Administrative or external-close paths bypass checkpoint ownership."
      location: "pkg/dispatcher/dispatcher.go:applyKillWorker/applyRestartWorker/restartWorkerIfStillOnBead/checkClosedBeadAssignments/finalizeExternalClose/shutdownWithTimeout"
      severity: high
      mitigation_checked: "Process controls preserve exact phase through checkpoint-aware release; external close records a durable no-merge override unless completed integration is already proven."

    - risk: "Context handoff or generic preempt drops structured findings and QG evidence."
      location: "pkg/worker/worker.go:handleContextThreshold/handlePreempt; pkg/dispatcher/dispatcher.go:handleHandoff; pkg/dispatcher/worker_pool.go:assignHandoffToWorker"
      severity: high
      mitigation_checked: "Checkpoint-owned handoff routes by durable phase, never through generic pendingHandoffs; an ordinary pre-QG replacement-socket case traverses the canonical payload builder and handleAssign before READY; checkpoint cases preserve findings and evidence."

    - risk: "Retention and recurring reminder logic exists but no production loop invokes it."
      location: "pkg/dispatcher/dispatcher.go:spawnBackgroundLoops"
      severity: high
      mitigation_checked: "Design assigns one reviewMaintenanceLoop and named scheduler tests for artifact pruning, correction routing, fingerprints, and reminder cadence."

    - risk: "Dispatcher and legacy worker both finalize the same approved bead."
      location: "pkg/worker/worker.go:handleReviewResult; pkg/dispatcher/dispatcher.go:handleDone"
      severity: high
      mitigation_checked: "Current protocol makes the worker echo DONE. Design requires a completion-owner marker, checkpoint CAS, assignment identity checks, and duplicate-DONE coverage."

    - risk: "Manual integration succeeds but remains pending until daemon restart."
      location: "pkg/dispatcher/dispatcher.go:spawnBackgroundLoops/reviewMaintenanceLoop"
      severity: high
      mitigation_checked: "The live maintenance loop scans manual_integration_pending, claims ancestry-proven work, and finalizes through the idempotent integration path; the proof forbids restart or direct helper invocation."

    - risk: "A checkpoint-owned merge failure falls through to legacy reopen."
      location: "pkg/dispatcher/dispatcher.go:mergeAndComplete"
      severity: high
      mitigation_checked: "Conflict and non-conflict failures retain checkpoint ownership and proof, enter typed integration recovery, and have a production failure-path test."

    - risk: "A stale approval is reused after code, target, acceptance, QG script, or policy changes."
      location: "pkg/dispatcher/dispatcher.go:handleReadyForReview; pkg/dispatcher/dispatcher.go:mergeAndComplete"
      severity: high
      mitigation_checked: "Current code has no immutable review key. Design keys reuse to all relevant hashes, compares the live target before intent and inside the merger, and requires target-bound QG/review on mismatch."

    - risk: "A reviewer false positive is automatically promoted into permanent acceptance criteria."
      location: "pkg/ops/finding.go; pkg/dispatcher/dispatcher.go:handleReviewRejection"
      severity: high
      mitigation_checked: "Current Finding has no contract-impact field and current retry uses prose. Design requires explicit acceptance_gap classification, separate contract repair, deterministic AC validation, and triage override."

    - risk: "Acceptance-gap parsing exists but no production coordinator runs repair."
      location: "pkg/dispatcher/dispatcher.go:handleReviewResult; pkg/dispatcher/ops_runs.go"
      severity: high
      mitigation_checked: "Design names routeAcceptanceGap and handleContractRepairResult, their transactional state changes, startup reconciliation, and a full lifecycle test."

    - risk: "Package-local tests pass while oro start omits evidence or maintenance configuration."
      location: "cmd/oro/cmd_start.go:buildDispatcherWithReviewTimeoutsAndCleanliness; cmd/oro/paths.go:ResolveDaemonPaths"
      severity: high
      mitigation_checked: "Epic acceptance includes a second named test that enters through the real root/start command chain and observes scheduled behavior, not just config fields."

    - risk: "Blocked review recovery becomes another automatic retry loop."
      location: "pkg/dispatcher/dispatcher.go:handleReviewBlocked; pkg/dispatcher/ops_runs.go"
      severity: high
      mitigation_checked: "Current blocked path reopens ordinary work. Design fingerprints failures, persists attempt budgets across restarts, uses typed bounded strategies, and quarantines one bead when no safe action remains."

    - risk: "Recurring quarantine warnings become noisy enough that operators ignore them."
      location: "pkg/dispatcher/health.go; pkg/dispatcher/events.go"
      severity: medium
      mitigation_checked: "Design persists quarantine/reminder schedule state, deduplicates by checkpoint/failure fingerprint, uses 15-minute reminders only for the first hour, backs off to hourly, and always keeps the bead visible in progress/status output."

    - risk: "Raw review artifacts consume disk or expose sensitive local context."
      location: "pkg/ops/ops.go:runWith; pkg/protocol/message.go:MaxMessageSize"
      severity: high
      mitigation_checked: "Current process accumulates output in memory. Design requires 0600 artifacts, byte caps, hashes, truncation markers, and retention policy."

    - risk: "Overflow findings survive only in process memory and disappear on restart."
      location: "review_checkpoints recovery artifact columns; pkg/dispatcher/assign_payload.go"
      severity: high
      mitigation_checked: "The lossless recovery-artifact reference is fsynced and committed with rejection; restart never regenerates it from compact rows."

    - risk: "No-op or epic-rebase-child success closes the bead but leaves its checkpoint integrating."
      location: "pkg/dispatcher/dispatcher.go:handleNoopMerge/completeEpicRebaseChild"
      severity: high
      mitigation_checked: "Both branches share observed-target proof and the idempotent checkpoint finalizer; epic ref update is an atomic expected-old-SHA compare with an injected race proof."

    - risk: "Contract validation blocks legitimate legacy operational beads."
      location: "pkg/dispatcher/dispatcher.go:checkBeadReady"
      severity: medium
      mitigation_checked: "Current admission accepts weak legacy AC. Design routes invalid legacy work through one repair path and retains explicit non-worker task types."

    - risk: "oro work truncates a repaired acceptance command at its first shell pipe."
      location: "cmd/oro/cmd_work.go:acAlreadySatisfied/parseACCmd"
      severity: high
      mitigation_checked: "The private cmd_work parsers are removed, acAlreadySatisfied uses pkg/acceptance, and the real no-commit consumer executes a pipeline intact."

  elephants:
    - risk: "Review latency cannot be solved solely by faster models; unchanged-state replay is the dominant avoidable cost."
    - risk: "QG evidence is part of review recovery correctness even though the immediate symptom appears to be feedback classification."

  paper_tigers:
    - risk: "A dedicated review_checkpoints table duplicates bead journey."
      reason: "Journey is an audit log; recovery needs indexed current state and compare-and-swap transitions."
    - risk: "Structured findings make worker prompts too large."
      reason: "The design caps recovery findings below 192 KiB and stores raw transport only by artifact reference."
```

## 23. Assumption Ledger

The following decisions are intentionally held for consultation:

- [x] Architecture: use a durable review checkpoint, not keyword patching.
- [x] Real problem framing: preserve trustworthy correctness state across
  process boundaries and restarts. Throughput loss is a measured consequence,
  not the primary problem.
- [x] Status quo and intervention: repeated work and dropped findings are not
  acceptable operating costs. Recovery must be automatic; operator commands
  are optional overrides, not normal pipeline steps.
- [x] Autonomous exhaustion policy: quarantine one bead after bounded safe
  strategies, continue unrelated work, reactivate automatically when relevant
  inputs change, and surface the quarantine to the operator on a recurring
  deduplicated cadence.
- [x] Quarantine reminder cadence and escalation: emit immediately, every 15
  minutes for the first hour, then hourly; also include active quarantine
  summaries whenever progress or status is requested.
- [x] Exact primary beneficiary/failure scenario: the dispatcher shepherding
  every implementation bead across QG into review, including rejection,
  reviewer/process failure, worker death, dispatcher restart, and
  approved-before-integration recovery.
- [x] Narrowest shippable wedge: do not cut the self-healing product contract.
  Use beadcraft to form dependency-ordered, independently verifiable beads; the
  first usable chain may land incrementally, but the epic remains open until
  all durable recovery behavior is integrated.
- [x] Consequence threshold for doing nothing: unacceptable P0 correctness and
  liveness failure. Lost findings, unchanged-state QG/review replay, and
  routine operator babysitting contradict the self-healing factory contract.
- [x] Future-fit and acceptance-contract repair: durable core capability.
  Review-classified acceptance gaps must enter a validated contract-repair path
  before coding retry; raw reviewer prose cannot directly mutate requirements.
