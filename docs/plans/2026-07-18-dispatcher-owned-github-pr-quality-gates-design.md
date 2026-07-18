# Dispatcher-Owned GitHub PR Quality Gates Design

Date: 2026-07-18

## Summary

Oro should move every quality-gate computation that does not fundamentally
require the local device off the Mac and onto GitHub Actions. Local QG compute
is a factory throughput limiter, so remote execution coverage—not PR visibility
or replacing Oro's merge model—is the primary product outcome. This must not
turn the operator into a CI coordinator. In the
selected design, the dispatcher owns the complete remote-gate lifecycle: it
rebases the bead branch, publishes the exact candidate state, creates or
updates a draft pull request, waits for an aggregate GitHub check, validates
the returned evidence against the exact head and target SHAs, routes failures
back to the worker, retries transient infrastructure failures, performs the
existing ops review, fast-forwards the target, pushes it, and reconciles the
pull request.

The operator is an observer. Remote-gate backlog, recovery, and degraded mode
must be surfaced in status, logs, the dashboard, and every progress response,
but routine progress must never require an operator action.

This design complements
`docs/plans/2026-05-06-qg-semaphore-evidence-design.md`. It uses the same
principle—only exact, durable evidence can authorize a merge—but replaces a
portable local full-QG execution with evidence from a GitHub PR merge commit.
It does not reuse the older design's assumption that worker and dispatcher
should both run full local gates.

## Problem

Oro currently asks the coding agent to run the full QG and then has the worker
harness run an authoritative full QG after the coding subprocess exits. The
worker harness rebases first, so the second gate is the only one that tests the
actual merge candidate. The root QG script serializes separate full gates, but
a single gate still fans out multiple lanes and Go tools. The combination
causes three harms:

1. Duplicate gates spend time without producing new evidence when the agent's
   branch was not changed by the rebase.
2. A single local full gate can still put enough pressure on the Mac to cause
   swapping or OOM termination.
3. Workers remain resident while waiting for local gate capacity, reducing
   useful coding throughput.

The repository already has `.github/workflows/ci.yml`, triggered for pull
requests to `main`, with Go, CGO-free, shell, docs/config, and Python jobs.
Oro also already has GitHub CLI probing in `pkg/janitor/detect.go`. The missing
capability is an authoritative, restart-safe dispatcher state machine that can
turn those checks into merge evidence.

## Goals

- Offload all portable full-QG computation to GitHub Actions for configured
  projects; retain locally only checks that require macOS, installed-machine
  state, or an explicitly entered outage fallback.
- Make remote-compute coverage measurable by reporting which QG stages ran
  remotely, which ran locally, why each local stage could not run remotely,
  and the local wall-clock/CPU time avoided.
- Make the dispatcher, not the operator or coding worker, own all PR and CI
  orchestration.
- Preserve the invariant that only a gate for the exact candidate head and
  exact target state can authorize review and merge.
- Preserve Oro's linear, fast-forward merge model.
- Route deterministic CI findings back to the assigned worker as structured,
  bounded feedback.
- Recover idempotently after dispatcher, worker, `gh`, network, or GitHub
  interruption.
- Cancel obsolete runs when a bead is retried, preempted, quarantined, or
  superseded.
- Keep macOS-specific verification local and memory bounded.
- Surface active, waiting, retrying, degraded, and quarantined remote gates in
  `oro status`, events, health, monitor output, and the dashboard.
- Apply the same remote evidence model to final epic promotion before the
  existing local build/install/restart boundary.

## Non-Goals

- Replacing GitHub Actions with a generic multi-provider CI framework.
- Letting GitHub merge or rebase Oro branches in the first version.
- Requiring an organization-owned repository or GitHub merge queue.
- Running agent/model credentials or Oro's private runtime state in CI.
- Sending arbitrary worker prompts, cards, or local state database contents to
  GitHub.
- Making branch protection the dispatcher correctness boundary.
- Eliminating focused acceptance tests from the coding worker.
- Offloading macOS-only smoke tests, local binary installation, dispatcher
  restart, or post-install health checks.
- Keeping every superseded draft PR forever.
- Using PRs primarily as a collaboration UI; they are the transport and audit
  identity for remote compute.
- Replacing Oro's task database with GitHub Issues, mirroring every epic as an
  issue, or representing child beads as GitHub sub-issues. Oro's task store
  remains authoritative; issue synchronization is a separate possible feature.

## Required Invariants

1. **Dispatcher ownership:** no routine remote-gate transition emits a manual
   integration request or waits for operator input.
2. **Exact-state evidence:** a passing check is usable only when it identifies
   the configured workflow, aggregate check, PR number, workflow run ID,
   tested merge SHA, candidate head SHA, target branch, and target SHA.
3. **No stale merge:** immediately before fast-forwarding, the dispatcher
   proves that local target, remote target, candidate head, and recorded
   evidence still match. Any mismatch invalidates evidence and re-enters the
   rebase/publish/gate loop.
4. **One authoritative portable gate:** in `github-pr` mode the coding agent
   runs acceptance/focused tests, not the portable full QG; GitHub supplies the
   authoritative portable result. Oro must not then rerun the same portable
   full gate locally after success.
5. **Fail closed:** missing, malformed, ambiguous, or unpersisted passing
   evidence never authorizes review or merge.
6. **Bounded local fallback:** a configured fallback may run only through the
   serialized, memory-safe local gate profile. Remote failure must never cause
   an unbounded local QG stampede.
7. **Durable recovery:** every external side effect has a persisted idempotency
   key and a reconciliation path.
8. **Worker feedback:** a deterministic remote failure must reach the next
   worker turn without requiring the operator to copy logs.
9. **No secret-bearing untrusted workflow:** use `pull_request`, never execute
   PR code through `pull_request_target`, and default workflow permissions to
   read-only.
10. **Quarantine visibility:** remote-gate ambiguity that preserves unmerged
    work follows the normal quarantine contract and is surfaced on every
    status/progress request.

## Current Call Chain

### Bead completion

```text
pkg/worker/worker.go:awaitSubprocessAndReport
  -> runQGAndReport
     -> rebaseOntoTarget
     -> runQualityGateWithProgress
     -> READY_FOR_REVIEW

pkg/dispatcher/dispatcher.go:handleReadyForReview
  -> spawn ops review
  -> handleReviewResult
  -> worker receives REVIEW_RESULT
  -> worker sends DONE(QualityGatePassed=true)
  -> dispatcher.handleDone
  -> dispatcher.mergeAndComplete
  -> merge.Coordinator.Merge
  -> fast-forward target and close bead
```

The new remote gate belongs between coding subprocess completion and ops
review, but its owner must be the dispatcher. The worker reports a committed
candidate instead of independently owning a long-lived GitHub operation.

### GitHub precedent

`pkg/janitor/detect.go:ciDetector` already establishes these local project
conventions:

- discover `gh` with `exec.LookPath`;
- derive the host from the repository's `origin` remote;
- verify active authentication;
- execute `gh` with the worktree-specific environment;
- parse JSON rather than terminal text;
- retain failing run URLs and failed-job evidence.

The remote-gate implementation should reuse those conventions, but must not
reuse the janitor detector directly. Janitor asks whether the latest branch CI
failed; merge authorization requires exact SHA identity and durable state.

## Selected Architecture

### 1. Opt-In Project Configuration

Add a project configuration section:

```yaml
factory:
  quality_gate:
    mode: github-pr             # local | github-pr
    github:
      remote: origin
      workflow: ci.yml
      aggregate_check: oro-portable-qg
      max_in_flight: 3
      poll_interval: 10s
      run_timeout: 35m
      outage_fallback_after: 15m
      close_superseded_prs: true
    local:
      profile: memory-safe
```

Rules:

- Existing projects default to `local`; setup never silently publishes code.
- `github-pr` is valid only when the remote resolves to GitHub, `gh` exists,
  authentication is active for that host, the workflow is visible, and the
  aggregate check contract can be found.
- Invalid explicit configuration fails startup with a configuration error. It
  does not silently switch modes.
- A runtime GitHub outage uses the separately configured degraded-mode policy;
  configuration errors are not outages.
- `max_in_flight` limits dispatcher-owned remote candidates, preventing an
  accidental PR/run explosion even though the computation is no longer local.
- The effective mode and all limits appear in status and startup events.

The first version supports one GitHub remote and one workflow/check contract
per project. Provider-general abstractions are intentionally deferred.

### 2. Aggregate Workflow Contract

The existing CI jobs remain independently diagnosable. Add one final job with
a globally unique name:

```yaml
oro-portable-qg:
  if: ${{ always() }}
  needs: [go, cgo-free, shell, docs, python]
  runs-on: ubuntu-latest
  steps:
    - name: Require every portable gate
      env:
        NEEDS_JSON: ${{ toJson(needs) }}
      run: scripts/ci/require-needs-success.sh "$NEEDS_JSON"
```

The helper fails unless every required job conclusion is `success`. A skipped,
cancelled, timed-out, action-required, stale, or missing dependency is not a
pass. Tests verify every current portable job is named in `needs` and that the
aggregate job itself is unique across workflows.

Workflow-level concurrency cancels obsolete runs for the same PR head:

```yaml
concurrency:
  group: oro-pr-${{ github.event.pull_request.number }}
  cancel-in-progress: true
```

CI uses the `pull_request` event, explicit read-only permissions, no project or
model secrets, and SHA-pinned third-party actions. The workflow must run the
portable gate against GitHub's PR merge commit, not merely the head branch.

### 3. Dispatcher-Owned Remote Gate State Machine

Introduce a persisted state machine:

```text
candidate_committed
  -> rebasing
  -> publishing
  -> awaiting_run
  -> running
  -> passed
  -> awaiting_review
  -> merging
  -> reconciled

Failure branches:
  deterministic_failed -> worker_retry
  transient_failed     -> backoff -> awaiting_run/re-publish
  target_moved         -> rebasing
  outage_degraded      -> local_memory_safe_gate
  preserved_ambiguity  -> quarantine
```

The worker-to-dispatcher protocol gains `CANDIDATE_READY`. It contains the
bead ID, assignment ID, worktree, branch, target branch, and local head SHA.
It does not claim a QG pass. The worker remains assigned and receives periodic
remote-gate progress, failure feedback, or the existing review result.

On `CANDIDATE_READY`, the dispatcher:

1. verifies the assignment, branch, clean worktree, and committed head;
2. serializes target-changing Git operations using the existing merge/branch
   coordination boundary;
3. fetches the configured remote and rebases the candidate onto the exact
   local target;
4. ensures a non-main epic target exists on the remote before creating a PR;
5. publishes the candidate with `--force-with-lease=<remote-ref>:<observed-sha>`
   when a prior dispatcher-owned ref exists, never with an unconditional force;
6. creates or updates a draft PR whose base is the actual target branch;
7. persists PR identity before waiting for checks;
8. releases local Git coordination while CI runs;
9. polls exact check-run data with bounded jittered backoff;
10. validates and persists passing or failing evidence;
11. on pass, starts the existing ops review;
12. after approval, re-acquires merge coordination and revalidates evidence;
13. fast-forwards locally, pushes the target with a lease, closes the bead,
    and reconciles the PR and remote candidate ref.

The dispatcher never holds a repository lock while waiting for GitHub.

### 4. GitHub Client Boundary

Define a narrow dispatcher dependency so behavior is testable without network
access:

```go
type RemoteGateClient interface {
    Preflight(ctx context.Context, repoRoot string, cfg GitHubGateConfig) error
    Publish(ctx context.Context, req PublishRequest) (PublishedCandidate, error)
    EnsureDraftPR(ctx context.Context, req EnsurePRRequest) (PullRequest, error)
    Observe(ctx context.Context, req ObserveGateRequest) (RemoteGateObservation, error)
    Cancel(ctx context.Context, req CancelGateRequest) error
    Reconcile(ctx context.Context, req ReconcilePRRequest) error
}
```

The production implementation shells out to `git` and `gh` using argument
arrays and JSON output. It reuses `processenv.ForWorkdir`, authenticates against
the actual remote host, and applies per-call contexts. No shell command is
constructed from PR titles, branch names, URLs, or remote output.

The dispatcher owns policy, persistence, retry classification, and state
transitions. The client owns only GitHub/git side effects and normalized
observations.

### 5. Branch and Pull Request Identity

Candidate remote refs are deterministic and dispatcher-owned:

```text
oro/beads/<project-prefix>/<bead-id>
oro/epics/<project-prefix>/<epic-id>
```

The local `agent/<bead-id>` branch remains unchanged for compatibility.
Persisted identity includes repository node/name, remote, remote ref, PR
number, PR URL, PR base ref, assignment ID, and latest local/remote SHAs.

Idempotency key:

```text
<repository>|<bead-id>|<assignment-id>|<candidate-head-sha>|<target-ref>
```

On restart, `EnsureDraftPR` searches first by persisted PR number and then by
exact head/base refs. It may adopt only a PR whose repository, head ref, base
ref, and bead metadata all match. Ambiguous matches fail closed and preserve
the work in quarantine.

PR titles and bodies are generated by the dispatcher and contain no prompt or
card content beyond the bead ID, title, target, commit SHA, and a short factory
status marker.

### 6. Exact Remote Evidence

Persist one evidence row per observed aggregate run:

```go
type RemoteQGEvidence struct {
    ID              string
    BeadID          string
    AssignmentID    int64
    Repository      string
    Remote          string
    PullRequest     int
    PullRequestURL  string
    WorkflowFile    string
    WorkflowRunID   int64
    WorkflowRunURL  string
    AggregateCheck  string
    CandidateHeadSHA string
    TargetBranch    string
    TargetSHA       string
    MergeSHA        string
    WorkflowBlobSHA string
    Conclusion      string
    StartedAt       time.Time
    FinishedAt      time.Time
    ObservedAt      time.Time
}
```

A remote pass is acceptable only when all of these are true:

- the configured workflow file and aggregate check match exactly;
- the conclusion is `success`;
- GitHub associates the run with the persisted PR;
- PR head SHA equals the currently published candidate SHA;
- PR base ref and base SHA equal the intended target and recorded target SHA;
- the run head/merge SHA equals the PR merge SHA for that head/base pair;
- the workflow definition blob SHA is recorded;
- the passing evidence row commits successfully before review starts;
- no later candidate, target, or workflow change supersedes it.

Status checks named the same by another workflow are rejected. The dispatcher
uses workflow identity plus check name, not check name alone.

### 7. Target Movement and Merge

Target movement is normal under parallel workers. It is not an operator event.

Before review, and again immediately before merge, the dispatcher compares:

- local target SHA;
- remote target SHA;
- evidence target SHA;
- local candidate SHA;
- remote candidate SHA;
- evidence candidate SHA;
- current workflow blob SHA;
- evidence workflow blob SHA.

If the target moved, the dispatcher invalidates the evidence, rebases the
candidate, publishes with force-with-lease, and waits for a new run. Review
approval for the old diff is also invalidated because the diff changed.

If only observational metadata changed, the dispatcher may continue. The first
version must not attempt affected-file reasoning.

After exact validation, the dispatcher uses the existing fast-forward merge
path locally. It then pushes the target with a lease against the target SHA it
validated. A lease failure means another writer won the race: the merge is not
closed, the local target is reconciled to the remote, and the bead re-enters
the rebase/gate loop.

GitHub is not asked to synthesize a merge, squash, or rebase commit. This keeps
the tested candidate SHA and Oro's local linear history aligned.

### 8. Failure Classification and Worker Recovery

Remote observations are classified before action:

| Class | Examples | Dispatcher action |
|---|---|---|
| deterministic | test, lint, build, coverage, or aggregate dependency failure | Fetch bounded failed-step evidence, persist it, send retry to the same worker when alive, otherwise requeue with the evidence checkpoint |
| superseded | candidate or base changed; run cancelled by newer push | Ignore old run and await the replacement |
| transient | GitHub 5xx, network timeout, runner startup failure, `gh` temporary error | Retry with jittered exponential backoff within the run timeout |
| auth/config | missing auth, missing workflow, missing aggregate check | Mark factory configuration unhealthy and pause new remote-gate assignments; do not ask the operator to advance individual beads |
| ambiguous | multiple matching PRs, mismatched repository identity, unverifiable SHA | Preserve branch/ref/evidence and quarantine for dispatcher recovery logic |

Deterministic feedback has a durable checkpoint containing:

- workflow/run/check identity and URL;
- failed job and step names;
- normalized failure fingerprint;
- a bounded excerpt selected around failure markers;
- full log artifact path or remote URL;
- candidate and target SHAs.

The worker prompt receives the bounded findings, never an entire oversized CI
transcript. The default limit is 24 KiB of normalized feedback with per-step
and total caps. Repeated identical findings increment an occurrence count
instead of duplicating text. Review/QG stuck detection fingerprints normalized
findings, not the full transcript.

If the worker dies, the next assignment receives the durable checkpoint before
coding begins. This preserves the existing requirement that unchanged code
must not repeat a rejection cycle without seeing the actual findings.

### 9. Automatic Degraded Mode

The operator should not have to choose between waiting forever and OOMing the
Mac during a GitHub outage.

After `outage_fallback_after` of continuously classified transient failure, the
dispatcher may execute the candidate through a local `memory-safe` profile:

- one local full gate globally;
- no coding-agent full gate;
- outer language lanes sequential;
- bounded internal check fan-out of one;
- Go package parallelism `-p 1`;
- existing `GOMAXPROCS=2` default retained;
- cancellation releases the local gate lease.

The local result produces exact local evidence for the same candidate and
target SHA. It can authorize review and merge, but the dispatcher records that
the remote gate was bypassed due to degraded mode. When GitHub recovers, new
candidates return automatically to remote mode.

Projects may set `outage_fallback_after: 0` to fail closed and wait remotely,
but the Oro project's recommended default is `15m` so the factory continues
without operator intervention.

Auth/config failures do not trigger local fallback because they are stable
misconfiguration, not an outage. The factory pauses new work whose completion
requires the unavailable contract while continuing unrelated safe work.

### 10. Cancellation, Cleanup, and Quarantine

When a candidate is superseded, preempted, requeued, closed externally, or
quarantined, the dispatcher cancels its current workflow run best-effort and
marks the durable run obsolete. A failed cancellation cannot make stale
evidence usable.

After a successful target push, reconciliation proves the candidate head is an
ancestor of both local and remote target. It then:

1. observes whether GitHub marked the PR merged;
2. if still open, closes it with a dispatcher-generated reconciliation note;
3. deletes the remote candidate ref with a lease;
4. runs existing local worktree/branch cleanup;
5. emits `remote_gate_reconciled`.

Cleanup is retryable and cannot reopen or fail the already proven merge. Remote
refs with unmerged commits are never deleted. Ambiguous ownership, unexpected
commits, or mismatched PR identity creates a recovery quarantine that includes
the local branch, remote ref, PR, SHAs, and evidence. Dispatcher recovery owns
the next action; the operator sees it but is not the routine assignee.

### 11. Dispatcher Restart Recovery

Remote gate state is stored in SQLite, not only in worker memory. On startup,
the dispatcher reconciles every nonterminal record:

- verify the bead and assignment still exist;
- verify local branch/worktree and remote ref identities;
- query the persisted PR and workflow run;
- adopt an exact active run, observe a completed run, or publish a replacement;
- cancel obsolete runs;
- restore the worker-visible status;
- resume backoff from persisted attempt time rather than stampeding GitHub.

All transitions use compare-and-swap on the record version. Only one dispatcher
instance may advance a record. Reconciliation is idempotent: repeating it after
any individual side effect produces the same live PR/run or a conservative
quarantine, never a duplicate merge.

If the worker process is absent, a deterministic failure reopens/requeues the
bead with its checkpoint. A passing gate may proceed to a fresh ops review only
after current branch/worktree state is reverified.

### 12. Epic Promotion

Child beads targeting an epic branch use PRs whose base is that epic branch.
The dispatcher publishes and advances the remote epic branch as children merge.

When all children and acceptance criteria are closed:

1. create/update an epic promotion PR from the epic branch to its actual target;
2. run the aggregate portable gate against the combined epic merge commit;
3. persist exact epic evidence;
4. run final epic review/acceptance;
5. fast-forward and push through the normal exact-state merge path;
6. reconcile the promotion PR and remote epic ref;
7. perform the existing local `make build install`, installed/repo binary hash
   match, controlled Oro restart, and healthy-dispatch verification.

The dispatcher owns steps 1–6. Local factory lifecycle automation owns step 7;
it must be a durable post-epic operation rather than an informal operator
reminder. Failure in step 7 keeps the epic completion operation visible and
retryable without undoing a proven merge.

### 13. Observability

Human and JSON status expose:

- effective gate mode and degraded-mode state;
- configured remote/workflow/aggregate check;
- remote gates publishing, queued, running, retrying, passed, and failed;
- oldest wait and retry age;
- local memory-safe fallback active/waiting counts;
- bead, worker, PR, run URL, candidate SHA prefix, target SHA prefix;
- last deterministic failure fingerprint;
- open remote-gate quarantines;
- pending epic post-merge build/install operations.

Required events include:

```text
remote_gate_candidate_ready
remote_gate_rebase_started / completed / failed
remote_gate_published
remote_gate_pr_created / adopted
remote_gate_run_observed
remote_gate_passed / deterministic_failed / transient_failed
remote_gate_evidence_invalidated
remote_gate_worker_feedback_sent
remote_gate_degraded_started / recovered
remote_gate_cancelled
remote_gate_recovery_resumed
remote_gate_quarantined
remote_gate_reconciled
epic_postmerge_install_started / completed / failed
```

The monitor treats a remote gate with no observation beyond the configured
timeout, a repeated deterministic fingerprint without a worker-feedback event,
or a terminal run attached to a nonterminal state as a dispatcher defect and
files/deduplicates a P0 bead automatically.

### 14. Rollout

Rollout is reversible and staged:

1. Land the aggregate workflow contract and remote-gate client behind
   `mode: local`.
2. Land durable state, recovery, failure routing, and observability.
3. Enable `github-pr` for one canary bead at a time while local QG remains the
   default.
4. Publish the current local history to `origin/main`; this repository is
   currently materially ahead of the remote and cannot use remote evidence for
   unpublished base state.
5. Enable `github-pr` for the Oro project with `max_in_flight: 1`, verify three
   successful beads plus one deterministic failure/retry and one target-move
   cycle.
6. Raise `max_in_flight` to `3` while keeping local fallback concurrency `1`.
7. Remove the coding-agent full-QG instruction only after the dispatcher-owned
   remote path and fallback path are both proven.

Rollback sets `mode: local`. Durable remote records remain audit evidence;
active dispatcher-owned runs are cancelled and candidate branches are preserved
until their work is merged or safely requeued.

## Acceptance Criteria

1. In a hermetic integration test with two candidates targeting the same base,
   the dispatcher publishes both, accepts only the exact passing merge evidence,
   invalidates the second candidate when the first advances the base, reruns it,
   sends a deterministic failed run's bounded findings to the worker, and
   fast-forwards only after the replacement run and ops review pass.
2. Killing and restarting the dispatcher during `awaiting_run`, `running`, and
   `passed` resumes the same exact PR/run when valid and never duplicates a PR,
   loses findings, or merges stale evidence.
3. With GitHub transiently unavailable beyond the configured threshold, at
   most one local memory-safe gate runs, progress continues without operator
   action, and the dispatcher automatically returns to remote mode after
   recovery.
4. Epic promotion uses a remote PR gate for the combined branch, then performs
   and verifies the local build/install/restart operation durably.
5. `oro status --json`, health, monitor events, and the dashboard expose remote
   backlog, degraded mode, failure feedback delivery, quarantine count, and
   pending post-epic installation.

Epic verification command:

```text
Cmd: go test ./pkg/dispatcher ./pkg/worker ./pkg/protocol ./cmd/oro -run 'TestDispatcherRemoteGateEndToEnd|TestRemoteGateRestartRecovery|TestRemoteGateDegradedFallback|TestEpicRemoteGateAndInstall' -count=1 && ./scripts/test_quality_gate.sh && ./scripts/quality_gate.sh
Assert: all named integration tests execute (none report "no tests to run"), the script harness passes, and the full repository quality gate exits 0 on main.
```

## Deep Premortem

```yaml
premortem:
  mode: deep
  context: "dispatcher-owned GitHub PR quality gates"

  tigers:
    - risk: "A green workflow is accepted after the target branch moves, so the merged state was never tested."
      severity: high
      mitigation_checked: "The design records head, base, merge, workflow blob, PR, run, and check identity; it revalidates local and remote target under merge coordination and invalidates both QG and review on movement."
    - risk: "Dispatcher restart creates duplicate PRs or loses a completed run and repeats expensive CI."
      severity: high
      mitigation_checked: "Durable idempotency keys, exact-ref adoption, versioned transitions, and startup reconciliation are required before rollout."
    - risk: "GitHub failure sends an oversized transcript to the worker, recreating latency and misclassification defects."
      severity: high
      mitigation_checked: "Feedback is normalized, fingerprinted, persisted, and capped at 24 KiB; full logs remain referenced evidence rather than prompt text."
    - risk: "Remote outage stops the factory until a human intervenes."
      severity: high
      mitigation_checked: "Transient outage automatically enters a single-slot memory-safe local fallback and returns to remote mode after recovery."
    - risk: "A rebase force-push overwrites a branch not owned by this dispatcher."
      severity: high
      mitigation_checked: "Deterministic namespace, persisted observed SHA, force-with-lease, ownership metadata, and ambiguity quarantine replace unconditional force."
    - risk: "The dispatcher trusts a check with the right name from the wrong workflow."
      severity: high
      mitigation_checked: "Evidence requires workflow file/blob identity plus unique aggregate check and exact PR merge SHA."

  elephants:
    - risk: "PR-per-bead increases remote noise and consumes hosted CI capacity."
      mitigation: "Draft PRs, deterministic refs, max_in_flight, cancellation, and automatic reconciliation bound the noise; the audit trail is part of the desired capability."
    - risk: "Direct target pushes mean GitHub branch protection is not the primary correctness boundary."
      mitigation: "Oro's exact-evidence merge state machine is the boundary in v1; rulesets can be added later without changing evidence semantics."
    - risk: "Automatic local fallback can still be slower than remote CI and one tool can individually exhaust memory."
      mitigation: "Fallback is serialized and memory-safe, but absolute memory safety needs OS-level resource control as a future capability."

  paper_tigers:
    - risk: "GitHub merge queue is unavailable for this user-owned public repository."
      reason: "The selected design does not depend on merge queue; Oro rebases, validates, fast-forwards, and pushes with leases itself."
    - risk: "A PR remains open after Oro directly pushes its head into the base."
      reason: "Reconciliation proves ancestry, then closes the PR and safely deletes the remote candidate ref without affecting merge correctness."
```

## Assumption Ledger

- [x] DECISION: What is the underlying problem?
      ANSWER: Local-device QG compute limits factory throughput. Move as much
      QG computation onto GitHub as possible; PR visibility is incidental and
      the dispatcher remains responsible for autonomous progress.
- [x] DECISION: How severe is the current status quo?
      ANSWER: QG serialization is the dominant current factory throughput
      limiter. GitHub CI exists but is not consumed by bead progression, while
      the local authoritative gate queues workers behind one serialized slot.
- [x] DECISION: Should GitHub also become the task database in this work?
      ANSWER: No. Epics and child beads remain in Oro's task store. A future
      GitHub Issues/sub-issues integration is explicitly out of scope.
- [x] DECISION: Who owns PR creation, CI waiting, retry, merge, and cleanup?
      ANSWER: The dispatcher; the operator only observes surfaced state.
- [x] DECISION: Is GitHub or the local worker authoritative for portable QG in
      remote mode?
      ANSWER: GitHub exact PR merge evidence is authoritative; local runs only
      macOS checks or the explicitly memory-safe outage fallback.
- [x] DECISION: Who creates the final Git commit?
      ANSWER: The worker creates the candidate commit; GitHub never rewrites it;
      the dispatcher fast-forwards the target.
- [x] DECISION: What happens when the target moves?
      ANSWER: Dispatcher automatically invalidates evidence/review, rebases,
      republishes, and reruns.
- [x] DECISION: What happens when GitHub is transiently unavailable?
      ANSWER: After 15 minutes the dispatcher uses one memory-safe local gate
      and automatically returns to remote mode.
- [ ] DECISION: Is a visible draft PR for every bead an acceptable permanent
      factory audit trail, or should successful bead PRs be collapsed later?
      DEPENDS_ON: None.
      RECOMMENDATION: Keep one draft PR per bead and auto-reconcile it; this is
      the narrowest implementation and gives exact CI/audit identity.
      ASK: Confirm PR-per-bead as the v1 unit of remote work.
