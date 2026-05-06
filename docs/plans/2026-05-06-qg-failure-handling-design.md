# Quality Gate Failure Handling Design

Date: 2026-05-06

## Summary

Oro should stop treating every exhausted quality gate as a brand-new P0 bug.
That behavior creates noisy beads such as `P0: QG exhausted for <bead>` even
when the real problem is a flaky test, an environment problem, an impossible
acceptance criterion, or a deterministic failure that belongs on the original
bead.

The better design is a QG failure classifier plus a deduped incident tracker:

1. Worker-caused deterministic failures stay on the original bead.
2. Systemic, flaky, and environment failures create or reuse one infra bug keyed
   by a failure fingerprint.
3. Transient failures retry with backoff and do not create beads.
4. Impossible or underspecified beads are bumped or replanned on the original
   bead, not converted into random P0 children.
5. Repeated identical QG output remains a stop/triage signal, but creation of
   new work happens only after classification.

This spec complements `docs/plans/2026-05-06-qg-semaphore-evidence-design.md`.
The semaphore/evidence spec decides how QG runs are limited and trusted. This
spec decides what Oro should do when QG fails.

## Research Summary

Files and sources read:

- `pkg/dispatcher/dispatcher.go:1535` - `handleDone` routes
  `QualityGatePassed=false` into `handleQGFailure`.
- `pkg/dispatcher/dispatcher.go:1662` - `handleQGFailure` logs failure,
  detects repeated output, increments retry counts, and reassigns the same
  worker with QG feedback until retry exhaustion.
- `pkg/dispatcher/dispatcher.go:6712` - `handleQGExhausted` completes the
  assignment, marks the bead exhausted, invokes ops decompose, then falls back
  to P0 creation when decompose fails.
- `pkg/dispatcher/dispatcher.go:6792` - `handleQGExhaustedFallback` currently
  creates `P0: QG exhausted for <bead>` unconditionally.
- `pkg/dispatcher/dispatcher.go:1890` - dispatcher pre-merge QG failure reopens
  the bead and removes the worktree.
- `pkg/dispatcher/dispatcher.go:1933` - epic QG failure creates a P0 fix task
  directly under the epic.
- `pkg/dispatcher/qg_retry_test.go` - current tests assert retry, stuck
  detection, retry exhaustion, and P0 creation.
- `pkg/dispatcher/epic_qg_test.go` - current tests assert epic QG failure
  creates a fix bead.
- `pkg/protocol/errors.go` - `QualityGateError` contains only bead, worker,
  output, and attempt.
- `docs/decisions&discoveries.md` - records the historical decision to create
  fix beads on epic QG failure so no code lands on main without QG.
- `docs/audits/2026-04-22-technical-audit.md` - flags QG failure cleanup as a
  high-risk place to preserve rejected work instead of mutating/deleting it.
- `oro recall "QG failure exhausted quality gate P0 flaky"` - returned recent
  gotchas that dispatcher/beadstore failures under full QG can be flaky and may
  pass when run directly.

Observed trade-off:

- The current behavior is conservative about not merging bad work, but it is
  imprecise about what work to create next.
- Automatically creating a P0 guarantees a trail, but creates duplicate and
  misleading work when the same flaky/systemic failure affects many beads.
- Keeping every failure on the original bead preserves ownership, but can hide
  factory-wide infra failures unless the system groups and surfaces them.

## Goals

- Replace default P0-per-exhaustion behavior with classification-based policy.
- Preserve merge safety: failed QG never authorizes merge.
- Preserve worker feedback loops for fixable deterministic failures.
- Deduplicate systemic/flaky/env failures by fingerprint and link affected
  beads as evidence.
- Keep original bead state honest and resumable.
- Make QG failure handling observable in events, status, and bead notes.
- Keep backward compatibility while `DonePayload` still sends only boolean plus
  output.

## Non-Goals

- Removing QG.
- Removing worker-side retry.
- Solving QG concurrency; that belongs to the semaphore/evidence spec.
- Building a perfect ML classifier.
- Automatically editing failing test output or source code.
- Automatically closing old noisy P0 beads; operators can clean those after the
  new policy lands.

## Current Behavior

Worker-side failure path:

1. Worker sends `DONE` with `QualityGatePassed=false` and `QGOutput`.
2. Dispatcher logs `quality_gate_rejected`.
3. Dispatcher hashes output for identical-output stuck detection.
4. Dispatcher increments `attemptCounts`.
5. If below `maxQGRetries`, the worker receives another `ASSIGN` with QG output
   as feedback and model escalated to Opus.
6. If retry count is exhausted, dispatcher completes the assignment, marks the
   bead exhausted, spawns a decompose ops agent, and may create a P0 bug.

Dispatcher pre-merge failure path:

1. Dispatcher runs QG before merge.
2. If QG fails, dispatcher logs `qg_failed`, reopens the bead if it is not
   closed, completes assignment, removes worktree, and returns.
3. No classification or deduped infra tracking occurs.

Epic QG failure path:

1. Dispatcher runs QG on a temporary epic worktree.
2. If QG fails, dispatcher logs `epic_qg_failed`.
3. Dispatcher creates a P0 fix task under the epic using the raw output.

## Design

### 1. Failure Event Model

Introduce a structured QG failure record, produced for worker, pre-merge, epic,
and `oro work` QG failures.

```go
type QGFailureRecord struct {
    ID             string `json:"id"`
    BeadID         string `json:"bead_id"`
    WorkerID       string `json:"worker_id,omitempty"`
    AssignmentID   int64  `json:"assignment_id,omitempty"`
    Component      string `json:"component"` // worker, dispatcher-pre-merge, dispatcher-epic, oro-work
    Scope          string `json:"scope"`     // task, epic, global, unknown
    Attempt        int    `json:"attempt"`
    OutputHash     string `json:"output_hash"`
    Fingerprint    string `json:"fingerprint"`
    Class          string `json:"class"`     // worker_deterministic, systemic, flaky, transient, impossible, unknown
    Confidence     string `json:"confidence"`// high, medium, low
    Summary        string `json:"summary"`
    RawOutput      string `json:"raw_output,omitempty"`
    CreatedAt      string `json:"created_at"`
}
```

Raw QG output can remain in existing retry feedback and bead notes. Events and
status should use summaries and hashes to avoid huge payloads.

### 2. Fingerprinting

Add deterministic fingerprinting before classification.

Fingerprint inputs:

- Failing command/test names parsed from output.
- File paths and line numbers, with line numbers normalized away when possible.
- Tool/lane markers such as `go test`, `golangci-lint`, `staticcheck`,
  `shellcheck`, `biome`, or `quality_gate.sh`.
- Error class markers such as timeout, OOM, signal killed, database locked,
  package loader failure, missing script, network failure.
- Component: worker/pre-merge/epic/oro-work.
- Optional QG script hash from the evidence spec when available.

Normalization rules:

- Strip timestamps, temporary worktree paths, worker IDs, PIDs, elapsed times,
  random ports, and line numbers that are not part of the semantic failure.
- Keep test/function names, package names, command names, exit signal, and
  short diagnostic strings.
- If parsing fails, fall back to `sha256(normalizedOutput)`.

The fingerprint should be stable enough that the same systemic failure across
many beads reuses one incident, but specific enough that unrelated failures do
not collapse into a single unusable bug.

### 3. Classification Policy

Classification returns one of these decisions:

```go
type QGFailureDecision string

const (
    RetryOriginal      QGFailureDecision = "retry_original"
    ReopenOriginal     QGFailureDecision = "reopen_original"
    CreateOrReuseInfra QGFailureDecision = "create_or_reuse_infra"
    BackoffRetry       QGFailureDecision = "backoff_retry"
    BumpOriginal       QGFailureDecision = "bump_original"
    StopForTriage      QGFailureDecision = "stop_for_triage"
)
```

Classification table:

| Class | Indicators | Decision | Bead policy |
| --- | --- | --- | --- |
| `worker_deterministic` | failing acceptance test tied to files changed by bead; compile/lint error in worker diff; dispatcher pre-merge fails only on worker branch | `RetryOriginal` until retry cap, then `ReopenOriginal` with evidence | No new bead by default |
| `systemic` | same fingerprint affects multiple unrelated beads; failure reproduces on main/epic baseline; missing tooling; package loader failure; QG script bug; OOM; DB panic | `CreateOrReuseInfra` | Reuse one infra bug keyed by fingerprint |
| `flaky` | rerun passes; known flaky fingerprint; timeout/race under parallel load; recall/docs mark it flaky | `BackoffRetry` first, then `CreateOrReuseInfra` if repeated | No per-bead bug; link affected beads |
| `transient` | network hiccup, temporary lock, canceled run, signal from shutdown, one-off infrastructure error | `BackoffRetry` | No bead unless threshold exceeded |
| `impossible` | missing acceptance, impossible command, contradictory task state, required dependency absent from repo | `BumpOriginal` | Update/bump original bead, no QG bug |
| `unknown` | low-confidence parse | `StopForTriage` after repeated failure | Create triage note or infra bug only after human/ops classification |

Default safety rule:

- When classification confidence is low, do not merge and do not create a P0
  automatically. Reopen or keep the original bead with QG evidence and raise a
  triage escalation.

### 4. Original Bead State Policy

The original bead remains the primary unit of work unless the failure is proven
systemic.

Worker deterministic failure:

- Retry the same bead with feedback while attempts remain.
- On exhaustion, complete the active assignment and set the original bead to
  `open`, not permanently exhausted.
- Add a note/comment summarizing attempts, fingerprint, latest output hash, and
  preserved branch/worktree.
- Preserve the agent branch/worktree for resumption when possible.
- Do not create `P0: QG exhausted for <bead>` by default.

Pre-merge deterministic failure:

- Reopen the original bead.
- Preserve the worker branch or archive the worktree according to existing
  rejected-work preservation rules.
- Record a failure event linked to the branch/head that failed.

Impossible bead:

- Change original bead state to `blocked`/`open` according to existing supported
  statuses.
- Add notes that explain missing acceptance or impossible command.
- If no `blocked` status exists in the current bead store, keep it `open` with
  explicit notes and priority escalation.

### 5. Deduped Infra Bug Policy

Create or reuse an infra bug only for systemic/flaky/env classes.

Bug key:

```text
qg:<class>:<fingerprint>
```

Bug title:

```text
P0: QG infrastructure failure - <summary>
```

Bug body includes:

- Fingerprint.
- Class and confidence.
- First seen and last seen timestamps.
- Components affected: worker, pre-merge, epic, oro-work.
- Affected beads list with status and assignment/worktree metadata.
- Representative output excerpt and output hash.
- Reproduction command if known.
- Baseline result: whether it reproduces on `main`, epic branch, or only worker
  branch.

Reuse behavior:

- If an open infra bug with the same key exists, update its notes/evidence
  rather than creating a new bead.
- If a closed infra bug with the same key exists and the failure recurs, reopen
  it or create a recurrence child only if the root cause was previously marked
  fixed and the output materially changed.
- Link affected beads as evidence without changing their ownership unless they
  are blocked by the infra failure.

Priority:

- P0 when failure blocks multiple beads, blocks main/epic QG, or affects factory
  throughput.
- P1 when isolated to one bead but classified as infra with medium confidence.
- Never promote to P0 solely because `maxQGRetries` was reached.

### 6. Retry and Backoff

Retry should distinguish fix attempts from environmental reruns.

Worker deterministic retries:

- Keep existing max retry cap and model escalation.
- The worker receives full QG output plus failure class/fingerprint.

Transient/flaky retries:

- Retry with backoff and jitter before reassigning coding work.
- Do not burn all worker-fix attempts on known transient infrastructure.
- Mark retry events as `qg_transient_retry` or `qg_flaky_rerun` so operators can
  distinguish them from worker attempts.

Repeated identical output:

- Keep current identical-output detector.
- On the third identical output, stop automatic worker retry and classify the
  fingerprint.
- If worker deterministic, reopen original with evidence.
- If systemic/flaky, create or reuse infra bug.
- If unknown, raise triage without creating a random P0 child.

### 7. Epic QG Failure Policy

Epic QG validates combined child work, so it deserves separate handling.

- Epic QG failure is not automatically a child worker's fault.
- Classify epic QG failures using the same fingerprinting path.
- If the fingerprint points to a deterministic integration failure caused by
  merged child work, create a targeted epic fix task under the epic.
- If it reproduces on main or appears across multiple epics, create/reuse the
  infra bug and link the epic.
- If the output is flaky/transient, rerun with backoff under the QG semaphore
  before creating work.
- The epic remains open/in_progress until a passing epic QG authorizes close.

This replaces the current direct P0 fix-task creation for all epic QG failures.

### 8. Storage

Add a `qg_failure_incidents` table to state DB:

```sql
CREATE TABLE IF NOT EXISTS qg_failure_incidents (
    fingerprint TEXT PRIMARY KEY,
    class TEXT NOT NULL,
    confidence TEXT NOT NULL,
    status TEXT NOT NULL,
    infra_bead_id TEXT,
    first_seen TEXT NOT NULL,
    last_seen TEXT NOT NULL,
    occurrence_count INTEGER NOT NULL DEFAULT 0,
    representative_output_hash TEXT,
    summary TEXT
);
```

Add a `qg_failure_occurrences` table:

```sql
CREATE TABLE IF NOT EXISTS qg_failure_occurrences (
    id TEXT PRIMARY KEY,
    fingerprint TEXT NOT NULL,
    bead_id TEXT NOT NULL,
    worker_id TEXT,
    assignment_id INTEGER,
    component TEXT NOT NULL,
    attempt INTEGER,
    output_hash TEXT NOT NULL,
    class TEXT NOT NULL,
    confidence TEXT NOT NULL,
    decision TEXT NOT NULL,
    created_at TEXT NOT NULL,
    FOREIGN KEY(fingerprint) REFERENCES qg_failure_incidents(fingerprint)
);
```

Indexes:

- `qg_failure_occurrences(bead_id, created_at)`
- `qg_failure_occurrences(fingerprint, created_at)`
- `qg_failure_incidents(status, last_seen)`

Events remain the operational feed. Tables provide dedupe and restart-safe
state.

### 9. Status and Events

New events:

- `qg_failure_classified`
- `qg_failure_incident_created`
- `qg_failure_incident_reused`
- `qg_failure_linked_bead`
- `qg_original_reopened`
- `qg_original_bumped`
- `qg_transient_retry`
- `qg_flaky_rerun`
- `qg_failure_triage_required`

Status additions:

```text
  qg failures: 2 open incidents, 5 affected beads in last 30m
```

JSON status additions:

```json
{
  "qg_failure_incidents_open": 2,
  "qg_failure_occurrences_30m": 5,
  "qg_failure_top_fingerprints": [
    {"fingerprint": "qg:flaky:go-test-dispatcher-heartbeat", "count": 3, "infra_bead_id": "oro-xxxx"}
  ]
}
```

Operator command follow-up:

```bash
oro qg incidents
oro qg incident show <fingerprint>
```

These commands are useful but not required for the first implementation if
events and bead notes expose the same evidence.

### 10. Interaction With QG Evidence Spec

When QG evidence exists:

- Failure record references `QGEvidence.RunID`.
- Fingerprint includes QG script hash and tested `HeadSHA`.
- Classifier can compare worker branch failure against dispatcher/main/epic
  baseline failures.

When QG evidence is not yet implemented:

- Classifier uses available fields: bead ID, worker ID, component, QG output,
  attempt, worktree path when known, and current branch head when cheap to read.
- Missing evidence must not block the conservative behavior: no merge, reopen
  original or create/reuse infra bug based on output and recurrence.

This lets the failure-handling work ship before or after QG evidence.

## Decision Premortems

Decision: classify before creating work.

```yaml
premortem:
  mode: quick
  context: "classification before QG failure bead creation"
  tigers:
    - risk: "Classifier mislabels a real worker bug as flaky and stops useful retries."
      severity: high
      mitigation_checked: "Spec requires low-confidence results to fall back to original-bead reopen plus triage, not silent ignore."
    - risk: "Systemic QG failure keeps reopening original beads and never creates infra work."
      severity: high
      mitigation_checked: "Spec requires cross-bead fingerprint recurrence to create/reuse an infra bug."
  elephants:
    - risk: "Heuristic classification will never be perfect."
  paper_tigers:
    - risk: "Not creating a P0 on every exhaustion hides problems."
      reason: "Every failure still creates an occurrence, event, note, or triage escalation; only duplicate bead creation is removed."
```

Decision: dedupe systemic failures by fingerprint.

```yaml
premortem:
  mode: quick
  context: "QG failure fingerprint dedupe"
  tigers:
    - risk: "Fingerprint is too broad and groups unrelated failures."
      severity: high
      mitigation_checked: "Spec keeps tool/test/package/error markers and requires representative output plus affected bead evidence."
    - risk: "Fingerprint is too narrow and still creates duplicate infra bugs."
      severity: medium
      mitigation_checked: "Spec normalizes volatile paths, timestamps, PIDs, elapsed time, and worker IDs."
  elephants:
    - risk: "The first version may need tuning from real factory output."
  paper_tigers:
    - risk: "SQLite incident tables add storage complexity."
      reason: "Events alone are not enough for restart-safe dedupe; schema is small and append-oriented."
```

## Deep Premortem

```yaml
premortem:
  mode: deep
  context: "QG failure classification, deduped infra incidents, and original-bead state policy"
  tigers:
    - risk: "Every task passes but dispatcher still calls the old P0 creation path."
      severity: high
      mitigation_checked: "Task graph includes replacing handleQGExhaustedFallback and epic QG failure creation call sites."
    - risk: "Pre-merge and epic QG failures bypass classifier."
      severity: high
      mitigation_checked: "Task graph includes worker, pre-merge, epic, and oro-work classification coverage."
    - risk: "Infra incident is created but affected beads are left stuck in exhausted/in_progress state."
      severity: high
      mitigation_checked: "Spec requires original bead reopen/block policy and occurrence links."
    - risk: "Classification result is not persisted, so restart creates duplicate bugs."
      severity: high
      mitigation_checked: "Spec adds incident and occurrence tables keyed by fingerprint."
    - risk: "A flaky failure is retried forever and consumes worker/QG capacity."
      severity: high
      mitigation_checked: "Spec caps backoff retries and escalates repeated flaky fingerprints to infra incident."
    - risk: "The identical-output stuck detector bypasses the classifier because it runs before retry exhaustion."
      severity: high
      mitigation_checked: "Task graph includes a repeated-identical-output test that must classify before escalation."
  elephants:
    - risk: "This is more dispatcher policy surface area around already complex retry code."
    - risk: "The real fix for many failures may be reducing QG flakiness, not smarter triage."
  paper_tigers:
    - risk: "Old workers lack structured evidence."
      reason: "Classifier can operate on legacy QGOutput and becomes more accurate when QGEvidence lands."
    - risk: "Operators want immediate P0 visibility."
      reason: "Systemic/flaky infra incidents are still P0 when they block throughput; worker-local failures remain visible on the original bead."
```

## Task Graph

Epic: Implement classified QG failure handling.

1. Define QG failure record, classifier types, and fingerprint helper
   - Test: `pkg/dispatcher/qg_failure_classifier_test.go:TestQGFailureFingerprintNormalizesVolatileOutput`
   - Cmd: `go test ./pkg/dispatcher -run 'TestQGFailureFingerprintNormalizesVolatileOutput|TestClassifyQGFailureDecisionMatrix' -count=1 -v`
   - Assert: volatile paths/timestamps/PIDs normalize away; decision matrix covers deterministic, systemic, flaky, transient, impossible, and unknown outputs.
   - Read: `pkg/dispatcher/dispatcher.go:handleQGFailure`, `pkg/protocol/errors.go`, `pkg/dispatcher/qg_stuck.go`
   - Signature: `func ClassifyQGFailure(record QGFailureRecord, history QGFailureHistory) QGFailureClassification`
   - Edges: empty output, huge output, unparsable output, known flaky fingerprint, repeated cross-bead fingerprint.

2. Persist QG failure incidents and occurrences
   - Test: `pkg/dispatcher/qg_failure_store_test.go:TestQGFailureStoreDedupesByFingerprint`
   - Cmd: `go test ./pkg/dispatcher -run TestQGFailureStoreDedupesByFingerprint -count=1 -v`
   - Assert: repeated same fingerprint updates one incident, records multiple occurrences, and survives dispatcher restart.
   - Read: `pkg/protocol/schema.go:SchemaDDL`, `cmd/oro/db.go:migrateStateDB`, `pkg/dispatcher/dispatcher.go:New`
   - Signature: `func RecordQGFailureOccurrence(ctx context.Context, db *sql.DB, rec QGFailureRecord, cls QGFailureClassification) (QGIncident, error)`
   - Edges: duplicate occurrence ID, DB locked, missing infra bead ID, closed incident recurrence.

3. Replace worker QG exhaustion P0 creation with classified policy
   - Test: `pkg/dispatcher/qg_retry_test.go:TestQGExhaustion_ReopensOriginalForDeterministicFailure`
   - Cmd: `go test ./pkg/dispatcher -run 'TestQGExhaustion_ReopensOriginalForDeterministicFailure|TestQGExhaustion_ReusesInfraIncidentForSystemicFailure' -count=1 -v`
   - Assert: deterministic exhaustion reopens original bead with notes and creates no P0 child; systemic exhaustion creates or reuses one infra bug and links the original bead.
   - Read: `pkg/dispatcher/dispatcher.go:handleQGExhausted`, `pkg/dispatcher/dispatcher.go:handleQGExhaustedFallback`, `pkg/dispatcher/qg_retry_test.go:TestQGExhaustion_CreatesP0Bead`
   - Signature: `func (d *Dispatcher) handleClassifiedQGExhaustion(...)`
   - Edges: decompose ops resolved, decompose ops failed, no active assignment, worker disconnected, original bead already closed.

4. Route worker retry/backoff by classification
   - Test: `pkg/dispatcher/qg_retry_test.go:TestTransientQGFailureBacksOffWithoutBurningWorkerAttempt`
   - Cmd: `go test ./pkg/dispatcher -run 'TestTransientQGFailureBacksOffWithoutBurningWorkerAttempt|TestFlakyQGFailureRerunThenCreatesIncidentAtThreshold|TestRepeatedIdenticalQGOutputClassifiedBeforeEscalation' -count=1 -v`
   - Assert: transient/flaky failures use backoff/rerun events and do not consume all worker-fix attempts before recurrence threshold; repeated identical output is classified before escalation or incident creation.
   - Read: `pkg/dispatcher/dispatcher.go:handleQGFailure`, `pkg/dispatcher/dispatcher.go:qgRetryWithReservation`, `pkg/dispatcher/qg_stuck.go`, `pkg/dispatcher/persist_counts_test.go`
   - Edges: context cancellation during backoff, dispatcher shutdown, worker disconnect, recurrence after prior pass.

5. Classify dispatcher pre-merge QG failures
   - Test: `pkg/dispatcher/pre_merge_qg_lifecycle_test.go:TestPreMergeQGFailureClassifiedBeforeReopen`
   - Cmd: `go test ./pkg/dispatcher -run TestPreMergeQGFailureClassifiedBeforeReopen -count=1 -v`
   - Assert: pre-merge QG failure records an occurrence, preserves/reopens the original bead for deterministic failure, and reuses infra incident for systemic failure.
   - Read: `pkg/dispatcher/dispatcher.go:checkPreMergeQG`, `pkg/dispatcher/pre_merge_qg_lifecycle_test.go`
   - Edges: bead externally closed, dirty/rejected worktree preservation, QG script error, missing bead detail.

6. Classify epic QG failures before creating fix work
   - Test: `pkg/dispatcher/epic_qg_test.go:TestEpicQGFailureClassifiedBeforeFixBeadCreation`
   - Cmd: `go test ./pkg/dispatcher -run TestEpicQGFailureClassifiedBeforeFixBeadCreation -count=1 -v`
   - Assert: deterministic integration failure creates a targeted epic fix task; systemic/flaky failure reuses infra incident and does not create duplicate epic fix tasks.
   - Read: `pkg/dispatcher/dispatcher.go:checkEpicQG`, `pkg/dispatcher/epic_qg_test.go`, `pkg/ops/epic_fix_prompt.go`
   - Edges: QG worktree create failure, QG error, repeated same epic fingerprint, multiple epics with same fingerprint.

7. Add bead note/link updates for original beads and infra incidents
   - Test: `pkg/dispatcher/qg_failure_notes_test.go:TestQGFailureNotesLinkAffectedBeadsToIncident`
   - Cmd: `go test ./pkg/dispatcher -run TestQGFailureNotesLinkAffectedBeadsToIncident -count=1 -v`
   - Assert: original bead receives latest class/fingerprint/output hash note; infra bug receives affected bead evidence without duplicate notes.
   - Read: `pkg/beadstore`, `pkg/dispatcher/dispatcher.go:updateBeadStatus`, existing fake bead store note/update helpers.
   - Edges: note/comment API unavailable, note update failure, output too large, affected bead already closed.

8. Add status/events observability
   - Test: `cmd/oro/cmd_status_test.go:TestStatusShowsQGFailureIncidents`
   - Cmd: `go test ./cmd/oro -run 'TestStatusShowsQGFailureIncidents|TestEventsShowQGFailureClassification' -count=1 -v`
   - Assert: status reports open QG incidents and recent occurrence count; events include class, fingerprint, decision, and affected bead.
   - Read: `cmd/oro/cmd_status.go`, `cmd/oro/cmd_events.go`, `pkg/dispatcher/dispatcher.go:logEvent`
   - Edges: no incidents, many incidents, status cache staleness, JSON and human output.

9. Integrate with `oro work` QG exhaustion
   - Test: `cmd/oro/cmd_work_execute_test.go:TestExecuteWorkQGExhaustionUsesClassifiedPolicy`
   - Cmd: `go test ./cmd/oro -run TestExecuteWorkQGExhaustionUsesClassifiedPolicy -count=1 -v`
   - Assert: standalone `oro work` does not create noisy QG exhaustion beads; deterministic failure resets original bead and systemic failure creates/reuses infra incident when dispatcher state DB is available.
   - Read: `cmd/oro/cmd_work.go`, `cmd/oro/cmd_work_execute_test.go:TestExecuteWork_QGExhaustion_ResetsBead`
   - Edges: no dispatcher running, no state DB, no bead store mutation available, existing agent branch.

10. Document operator policy and cleanup of legacy noisy QG beads
    - Test: docs review plus quality gate
    - Cmd: `./scripts/quality_gate.sh`
    - Assert: factory monitoring docs explain classification, when P0 infra bugs are created, how to inspect affected beads, and how to close legacy `P0: QG exhausted for <bead>` duplicates after confirming original beads/infra incident links.
    - Read: `docs/plans/2026-05-06-qg-failure-handling-design.md`, existing monitoring docs/runbooks.
    - Edges: legacy open P0 QG beads, closed recurrence, operator manual triage.

## Acceptance Test For Epic

Primary machine check:

```bash
go test ./pkg/dispatcher -run 'TestQGFailureFingerprintNormalizesVolatileOutput|TestClassifyQGFailureDecisionMatrix|TestQGFailureStoreDedupesByFingerprint|TestQGExhaustion_ReopensOriginalForDeterministicFailure|TestQGExhaustion_ReusesInfraIncidentForSystemicFailure|TestTransientQGFailureBacksOffWithoutBurningWorkerAttempt|TestFlakyQGFailureRerunThenCreatesIncidentAtThreshold|TestRepeatedIdenticalQGOutputClassifiedBeforeEscalation|TestPreMergeQGFailureClassifiedBeforeReopen|TestEpicQGFailureClassifiedBeforeFixBeadCreation|TestQGFailureNotesLinkAffectedBeadsToIncident' -count=1
go test ./cmd/oro -run 'TestStatusShowsQGFailureIncidents|TestEventsShowQGFailureClassification|TestExecuteWorkQGExhaustionUsesClassifiedPolicy' -count=1
```

Final gate:

```bash
./scripts/quality_gate.sh
```

Operational acceptance:

- A deterministic worker QG exhaustion reopens the original bead and creates no
  `P0: QG exhausted for <bead>` child.
- The same systemic/flaky QG fingerprint across two unrelated beads creates or
  reuses exactly one infra bug and links both beads.
- Epic QG failure creates a targeted epic fix task only when classified as a
  deterministic integration failure.
- `oro status` and `oro events` expose incident counts, fingerprint, class, and
  decision.

## Adversarial Review

```yaml
verdict: SELF_REVIEW_PASS_PENDING_FRESH_CHALLENGE
spec: docs/plans/2026-05-06-qg-failure-handling-design.md
reviewer_note: "In-context adversarial review found two high-risk gaps: old P0 creation paths could remain wired, and identical-output stuck detection could bypass classification. The task graph now explicitly covers worker exhaustion, repeated-output stuck detection, pre-merge, epic, and oro-work QG failure paths."

acceptance_test:
  cmd: "go test ./pkg/dispatcher -run 'TestQGFailureFingerprintNormalizesVolatileOutput|TestClassifyQGFailureDecisionMatrix|TestQGFailureStoreDedupesByFingerprint|TestQGExhaustion_ReopensOriginalForDeterministicFailure|TestQGExhaustion_ReusesInfraIncidentForSystemicFailure|TestTransientQGFailureBacksOffWithoutBurningWorkerAttempt|TestFlakyQGFailureRerunThenCreatesIncidentAtThreshold|TestRepeatedIdenticalQGOutputClassifiedBeforeEscalation|TestPreMergeQGFailureClassifiedBeforeReopen|TestEpicQGFailureClassifiedBeforeFixBeadCreation|TestQGFailureNotesLinkAffectedBeadsToIncident' -count=1 && go test ./cmd/oro -run 'TestStatusShowsQGFailureIncidents|TestEventsShowQGFailureClassification|TestExecuteWorkQGExhaustionUsesClassifiedPolicy' -count=1"
  assert: "No default P0-per-exhaustion path remains; deterministic failures stay on original beads; systemic/flaky failures dedupe into incidents."
  adequate: true

traceability:
  covered: 9
  gaps: 0
  matrix: |
    | # | Criterion | Task | Test | Status |
    | 1 | Classify QG failures | 1 | TestClassifyQGFailureDecisionMatrix | covered |
    | 2 | Dedupe systemic/flaky incidents | 2,3 | TestQGFailureStoreDedupesByFingerprint, TestQGExhaustion_ReusesInfraIncidentForSystemicFailure | covered |
    | 3 | Deterministic failures stay on original bead | 3,5 | TestQGExhaustion_ReopensOriginalForDeterministicFailure, TestPreMergeQGFailureClassifiedBeforeReopen | covered |
    | 4 | Transient/flaky retry does not burn worker-fix attempts | 4 | TestTransientQGFailureBacksOffWithoutBurningWorkerAttempt | covered |
    | 5 | Repeated identical QG output is classified | 4 | TestRepeatedIdenticalQGOutputClassifiedBeforeEscalation | covered |
    | 6 | Epic QG classification replaces direct fix creation | 6 | TestEpicQGFailureClassifiedBeforeFixBeadCreation | covered |
    | 7 | Evidence links affected beads | 7 | TestQGFailureNotesLinkAffectedBeadsToIncident | covered |
    | 8 | Operator observability | 8 | TestStatusShowsQGFailureIncidents, TestEventsShowQGFailureClassification | covered |
    | 9 | oro work path does not bypass policy | 9 | TestExecuteWorkQGExhaustionUsesClassifiedPolicy | covered |

wiring_gaps: []

negative_space:
  - area: "Fresh-context adversarial review"
    severity: minor
    fix: "Run a separate Codex/ops review before beadcraft if operator wants the full Ralph Loop gate."
  - area: "Classifier tuning from real logs"
    severity: minor
    fix: "Task 10 documents operator cleanup and legacy incidents; classifier tests should include real examples from current open QG P0s."

red_team_scenarios:
  - scenario: "Classifier package exists and tests pass, but handleQGExhaustedFallback still creates P0 bugs."
    beads_pass: false
    feature_works: false
    root_cause: "Worker exhaustion call site not replaced."
    fix: "Task 3 explicitly reads and replaces handleQGExhaustedFallback."
  - scenario: "Worker path is fixed, but epic QG still creates duplicate fix tasks."
    beads_pass: false
    feature_works: false
    root_cause: "Epic check has separate direct CreateBeadGraph path."
    fix: "Task 6 explicitly replaces checkEpicQG failure policy."
  - scenario: "Incident is created but original bead remains exhausted and invisible."
    beads_pass: false
    feature_works: false
    root_cause: "No original bead state policy."
    fix: "Tasks 3, 5, and 7 require reopen/link behavior."
  - scenario: "Repeated identical QG output triggers the existing stuck escalation and never records a classified incident."
    beads_pass: false
    feature_works: false
    root_cause: "The stuck detector runs before retry exhaustion."
    fix: "Task 4 requires TestRepeatedIdenticalQGOutputClassifiedBeforeEscalation."

integration_points:
  covered:
    - "pkg/dispatcher/dispatcher.go:handleQGFailure"
    - "pkg/dispatcher/qg_stuck.go"
    - "pkg/dispatcher/dispatcher.go:handleQGExhausted"
    - "pkg/dispatcher/dispatcher.go:handleQGExhaustedFallback"
    - "pkg/dispatcher/dispatcher.go:checkPreMergeQG"
    - "pkg/dispatcher/dispatcher.go:checkEpicQG"
    - "cmd/oro/cmd_work.go"
    - "cmd/oro/cmd_status.go"
    - "cmd/oro/cmd_events.go"
    - "pkg/protocol/schema.go"
    - "cmd/oro/db.go"
  uncovered: []
```
