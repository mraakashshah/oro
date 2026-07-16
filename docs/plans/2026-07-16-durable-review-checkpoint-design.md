# Durable Review Checkpoint Design

Date: 2026-07-16

Status: Architecture selected; consultation pending

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
   findings, a blocking ops incident, or an explicit recovery quarantine.
7. **Preserve work before releasing ownership.** Worktree and branch state are
   retained until merge proof or explicit recovery resolution.
8. **Compact hot-path data.** Worker messages, events, `ops_runs`, and bead
   journey entries contain bounded summaries and structured findings, never
   raw stream transcripts.
9. **Restart-safe completion.** An approved checkpoint can resume integration
   without the original worker. A rejected checkpoint can resume implementation
   with a different worker.
10. **Contract gaps become contracts.** Review findings classified as
    acceptance gaps must pass a separate contract-repair and acceptance
    validation step before code retry.

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

type ReviewOutcome struct {
    Decision     ReviewDecision     `json:"decision"`
    Findings     []Finding          `json:"findings,omitempty"`
    Blockers     []ReviewBlocker    `json:"blockers,omitempty"`
    Verification ReviewVerification `json:"verification"`
    Execution    ReviewExecution    `json:"execution"`
    Summary      string             `json:"summary"`
    Artifact     ReviewArtifactRef  `json:"artifact"`
}
```

`Finding` gains a narrowly-scoped contract impact:

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

## 8. QG Evidence at READY_FOR_REVIEW

The worker must send proof of the QG-passed state before review starts.

`ReadyForReviewPayload` gains:

```go
type QGEvidence struct {
    RunID        string `json:"run_id"`
    BeadID       string `json:"bead_id"`
    WorkerID     string `json:"worker_id,omitempty"`
    HeadSHA      string `json:"head_sha"`
    TargetBranch string `json:"target_branch,omitempty"`
    TargetSHA    string `json:"target_sha,omitempty"`
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

## 9. Durable Review Checkpoint

Add a state-DB table dedicated to immutable review phases.

```sql
CREATE TABLE review_checkpoints (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    bead_id TEXT NOT NULL,
    assignment_id INTEGER NOT NULL,
    worker_id TEXT,
    worktree TEXT NOT NULL,
    branch TEXT NOT NULL,
    target_branch TEXT NOT NULL,
    head_sha TEXT NOT NULL,
    target_sha TEXT,
    acceptance_hash TEXT NOT NULL,
    qg_run_id TEXT,
    qg_script_hash TEXT,
    qg_mode TEXT,
    qg_output_hash TEXT,
    review_policy_hash TEXT NOT NULL,
    state TEXT NOT NULL,
    review_attempt INTEGER NOT NULL DEFAULT 0,
    findings_json TEXT NOT NULL DEFAULT '[]',
    blockers_json TEXT NOT NULL DEFAULT '[]',
    verification_json TEXT NOT NULL DEFAULT '{}',
    summary TEXT NOT NULL DEFAULT '',
    artifact_path TEXT,
    artifact_sha256 TEXT,
    artifact_bytes INTEGER NOT NULL DEFAULT 0,
    ops_run_id INTEGER,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now')),
    completed_at TEXT
);
```

Allowed states:

```text
qg_passed
review_running
rejected
contract_repair_running
blocked
failed
approved
integrating
integrated
superseded
```

Checkpoint uniqueness is based on:

```text
bead_id
assignment_id
head_sha
target_sha
acceptance_hash
qg_script_hash
qg_mode
review_policy_hash
```

There may be only one non-superseded checkpoint for a unique key.

The current checkpoint is authoritative for pipeline recovery. The bead journey
receives compact `review_finding`, `review_checkpoint_changed`, and triage
events for user-visible audit, but recovery does not depend on scanning an
unbounded journey.

## 10. Review Ops-Run Lifecycle

Before spawning review, the dispatcher transactionally:

1. verifies the active/requeued assignment owns the worktree;
2. creates or reuses the checkpoint;
3. creates a linked `ops_runs(type=review, status=running)` row;
4. transitions the checkpoint to `review_running`.

Terminal handling:

- approved -> ops run `resolved`, checkpoint `approved`;
- rejected -> ops run `resolved`, checkpoint `rejected`;
- typed environment/infrastructure blocker -> ops run `failed`, checkpoint
  `blocked`;
- spawn, timeout, cancellation, malformed result, or unsafe approval -> ops run
  `failed`, checkpoint `failed`.

No review path may leave a `running` ops run after terminal handling. Manual
`oro ops retry` supersedes the old run and resumes review from the checkpoint,
not from an inferred bead status.

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
       -> deliver compact findings
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
       -> oro ops retry/resolve is the recovery action
```

No transition from `blocked` or `failed` directly reopens the bead into the
ordinary ready queue.

## 12. Approval Without Worker Echo

The dispatcher becomes completion owner for evidence-backed checkpoints.

On approval:

1. compare-and-swap checkpoint `review_running -> approved`;
2. verify assignment identity, worktree, HEAD, and QG evidence again;
3. transition `approved -> integrating`;
4. execute the existing passing-DONE integration path;
5. transition to `integrated` only after assignment completion and merge/close.

Refactor the successful half of `handleDone` into a shared integration function
used by:

- legacy worker `DONE(QualityGatePassed=true)`;
- durable approved checkpoint recovery.

`ReviewResultPayload` gains a completion-owner marker. New workers clear their
pending local QG state and do not echo `DONE` when the dispatcher owns
completion. Old workers may still echo `DONE`; assignment/checkpoint CAS plus
the existing merge guard must make the duplicate a no-op.

This removes the requirement that the original worker survive until approval.

## 13. Rejection Recovery

`AssignPayload` gains structured recovery context:

```go
type ReviewRecovery struct {
    CheckpointID    int64     `json:"checkpoint_id"`
    RejectedHeadSHA string    `json:"rejected_head_sha"`
    Findings        []Finding `json:"findings"`
    Attempt         int       `json:"attempt"`
    AcceptanceHash  string    `json:"acceptance_hash"`
}
```

The existing string `Feedback` remains as a compact rendered compatibility
view, not the source of truth.

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
worker.

The dispatcher:

1. persists the findings;
2. releases worker capacity while preserving the assignment worktree;
3. creates a blocking contract-repair ops run;
4. asks the contract agent to produce a complete replacement acceptance
   contract;
5. validates the replacement deterministically;
6. updates the bead acceptance only after validation;
7. marks the old checkpoint superseded because `acceptance_hash` changed;
8. requeues the preserved implementation with the revised contract.

The contract agent cannot directly close, merge, or silently waive a finding.
False-positive and wont-fix handling continues through durable finding triage.

This is the prevention layer: requirements discovered as missing from the
original bead become executable requirements before another full QG.

## 15. Acceptance Admission

Introduce one line-aware acceptance parser and validator shared by:

- `oro task create`;
- `oro task update --acceptance`;
- dispatcher `checkBeadReady`;
- contract repair;
- review prompt command extraction.

The parser must preserve shell pipes and quoted expressions inside `Cmd:`. It
must not split acceptance text on every `|`.

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

Startup recovery runs after assignment/worktree validation.

For every non-terminal checkpoint:

### `qg_passed` or `review_running`

- verify worktree, branch, HEAD, acceptance hash, and QG evidence;
- reconcile the linked review ops run;
- reroute review exactly once if the prior process is dead;
- never run worker QG again for the same checkpoint key.

### `rejected`

- keep or reactivate the preserved assignment;
- make the bead assignable only with structured review recovery context;
- if a worker reconnects on the same assignment, replay the compact findings.

### `contract_repair_running`

- reconcile the linked contract ops run;
- ordinary worker assignment remains blocked.

### `approved` or `integrating`

- verify the immutable checkpoint;
- resume integration without the original worker;
- run dispatcher QG if evidence cannot be trusted;
- do not rerun review.

### `blocked` or `failed`

- preserve the worktree and assignment;
- keep the bead out of the ordinary queue;
- expose the failed ops run and exact `oro ops retry`/`resolve` action.

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

Throughput reporting should distinguish:

- review executions;
- checkpoint reuses;
- productive rejections delivered to a worker;
- blocked infra reviews;
- repeated unchanged attempts prevented.

## 19. Backward Compatibility

- Existing `Verdict` values and `<-chan Result` remain.
- Existing `Feedback` remains as a bounded compatibility rendering.
- Missing `ReadyForReview.QGEvidence` selects the legacy-compatible path and
  forces dispatcher QG before merge.
- Legacy exact prose verdicts synthesize a typed outcome conservatively.
- Existing finding journey and triage formats remain readable.
- Existing `rejection_history` may continue receiving compact summaries during
  rollout, but it is no longer authoritative.
- Old workers that echo passing `DONE` after approval cannot cause duplicate
  integration.

## 20. Testing Strategy

### Pure outcome tests

- A rejected report containing `TaskOutput`, `tail -f`, or permission-denied
  text remains rejected and retains findings.
- A typed blocker with no findings becomes blocked.
- A valid rejection plus nonzero process exit remains safely rejected.
- An approval plus nonzero exit fails closed.
- Raw artifact text is never passed to the classifier.

### Artifact tests

- multi-megabyte stream output produces a compact result and an artifact
  reference;
- event, ops-run, checkpoint, and `ASSIGN` payloads remain below their caps;
- truncation is explicit and does not change the decision;
- artifacts use project-scoped paths and restrictive permissions.

### Checkpoint store tests

- duplicate immutable keys create one active checkpoint;
- compare-and-swap rejects stale state transitions;
- changed HEAD, acceptance, target, QG script, or policy supersedes the prior
  checkpoint;
- terminal rejected/approved data survives database reopen.

### Dispatcher lifecycle tests

- normal rejection sends exact structured findings;
- env/infra keyword pollution cannot route to blocked;
- blocked review preserves work and creates a failed ops run without reopening
  into the ready queue;
- every review outcome completes the linked ops run;
- approval integrates without worker `DONE`;
- an old-worker duplicate `DONE` is harmless.

### Recovery tests

- dispatcher death during review reroutes one review and no QG;
- worker death during review does not lose QG evidence;
- worker death after rejection reassigns findings to a different worker;
- dispatcher death after approval resumes integration and does not rerun review;
- changed HEAD invalidates the checkpoint;
- unsafe worktree state recovery-quarantines instead of deleting work.

### Contract tests

- line-aware AC parsing preserves shell pipes;
- missing `Read`, vacuous `Cmd`, or missing `Assert` is rejected before
  assignment;
- an acceptance-gap finding blocks coding retry;
- valid contract repair updates AC and requeues preserved work;
- invalid contract repair leaves a failed ops run and no worker loop.

### End-to-end proof

A bounded harness must exercise:

1. initial QG pass;
2. structured rejection with misleading infrastructure keywords in the raw
   transcript;
3. exact findings delivered to a recovery worker;
4. unchanged retry prevented before QG;
5. changed retry produces new QG evidence;
6. dispatcher restart during review;
7. approval and integration without original worker;
8. zero active assignments, failed review ops runs, or non-terminal review
   checkpoints at completion.

## 21. Rollout

1. Add typed outcomes and bounded artifacts while keeping existing dispatcher
   behavior.
2. Add QG evidence and checkpoint persistence.
3. Switch dispatcher review routing to structured outcomes and linked ops runs.
4. Move evidence-backed approval completion to the dispatcher.
5. Add startup recovery for each checkpoint state.
6. Add structured rejection recovery and unchanged-head preflight.
7. Add acceptance-contract repair and stricter admission.
8. Add health/status/throughput reporting and the bounded restart proof.

Each phase is reversible until the schema/checkpoint path becomes authoritative.
During rollout, missing new fields select conservative legacy fallbacks.

## 22. Deep Premortem

```yaml
premortem:
  mode: deep
  context: "durable review checkpoints and worker recovery"

  tigers:
    - risk: "Dispatcher and legacy worker both finalize the same approved bead."
      location: "pkg/worker/worker.go:handleReviewResult; pkg/dispatcher/dispatcher.go:handleDone"
      severity: high
      mitigation_checked: "Current protocol makes the worker echo DONE. Design requires a completion-owner marker, checkpoint CAS, assignment identity checks, and duplicate-DONE coverage."

    - risk: "A stale approval is reused after code, target, acceptance, QG script, or policy changes."
      location: "pkg/dispatcher/dispatcher.go:handleReadyForReview; pkg/dispatcher/dispatcher.go:mergeAndComplete"
      severity: high
      mitigation_checked: "Current code has no immutable review key. Design keys reuse to all relevant hashes and requires verification again before integration."

    - risk: "A reviewer false positive is automatically promoted into permanent acceptance criteria."
      location: "pkg/ops/finding.go; pkg/dispatcher/dispatcher.go:handleReviewRejection"
      severity: high
      mitigation_checked: "Current Finding has no contract-impact field and current retry uses prose. Design requires explicit acceptance_gap classification, separate contract repair, deterministic AC validation, and triage override."

    - risk: "Blocked review recovery becomes another automatic retry loop."
      location: "pkg/dispatcher/dispatcher.go:handleReviewBlocked; pkg/dispatcher/ops_runs.go"
      severity: high
      mitigation_checked: "Current blocked path reopens ordinary work. Design keeps failed/blocked checkpoints behind a failed ops run and requires explicit retry/resolve."

    - risk: "Raw review artifacts consume disk or expose sensitive local context."
      location: "pkg/ops/ops.go:runWith; pkg/protocol/message.go:MaxMessageSize"
      severity: high
      mitigation_checked: "Current process accumulates output in memory. Design requires 0600 artifacts, byte caps, hashes, truncation markers, and retention policy."

    - risk: "Contract validation blocks legitimate legacy operational beads."
      location: "pkg/dispatcher/dispatcher.go:checkBeadReady"
      severity: medium
      mitigation_checked: "Current admission accepts weak legacy AC. Design routes invalid legacy work through one repair path and retains explicit non-worker task types."

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
- [ ] Status quo cost and acceptable operator intervention.
- [ ] Exact primary beneficiary/failure scenario.
- [ ] Narrowest shippable wedge within the architecture.
- [ ] Consequence threshold for doing nothing.
- [ ] Whether acceptance-contract repair is a durable core capability or a
  follow-up after checkpoint recovery.
