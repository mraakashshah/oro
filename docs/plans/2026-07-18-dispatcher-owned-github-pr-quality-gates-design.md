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
back to durable correction work, retries transient infrastructure failures,
performs the existing ops review, authorizes a protected GitHub squash merge,
synchronizes local target state, and reconciles the pull request.

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
- Preserve linear target history with one GitHub squash-merged commit per bead.
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
- Letting a worker or operator decide or perform routine merges.
- Requiring an organization-owned repository or GitHub merge queue.
- Running agent/model credentials or Oro's private runtime state in CI.
- Sending arbitrary worker prompts, cards, or local state database contents to
  GitHub.
- Assuming a human will configure or preserve repository merge protection.
  GitHub mode requires a dispatcher-preflighted ruleset/branch policy that
  makes the aggregate check strict and up to date for every supported target.
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
3. **No stale merge:** immediately before authorizing GitHub's squash merge, the dispatcher
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
11. **Atomic target protection:** a remote provider may merge only if it can
    prove that the tested base is still current at the provider's atomic merge
    boundary. For GitHub v1 this requires a verified strict required-check
    ruleset covering `main`, configured custom targets, and `epic/**`; a
    pre-merge read followed by an expected-head request is not sufficient.
12. **Single authoritative completion path:** in `github-pr` mode neither
    `DONE(QualityGatePassed=true)`, the legacy local merge coordinator, nor
    `checkPreMergeQG` can bypass the durable remote-gate record.

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

### Presubmit Model Reframe

This is a high-scale presubmit system, not merely a way to replace one shell
command with a GitHub workflow. The integration target must stay green while
local feedback remains fast enough that obvious failures do not consume remote
queue time.

The intended gate layers are:

```text
worker implementation
  -> fast local presubmit
  -> normalized candidate commit/range
  -> dispatcher rebase onto exact target
  -> ops review of the exact post-rebase diff
  -> publish draft PR
  -> comprehensive remote QG on the PR merge commit
  -> exact evidence revalidation
  -> dispatcher-authorized GitHub squash merge
```

Each layer has a distinct job:

- Worker TDD and acceptance commands provide immediate change-specific
  feedback during implementation.
- Local presubmit is a completion-based set of independently scheduled checks,
  not a smaller monolithic QG and not a wall-clock budget. It rejects invalid,
  unformatted, uncompilable, acceptance-failing, or statically unsound work
  before publication. Project configuration controls local check concurrency;
  lightweight checks from many candidates may run together while heavyweight
  repository-wide suites execute remotely.
- Ops review protects design, behavior, acceptance completeness, code health,
  and maintainability. It reviews the exact post-rebase diff before publication
  to GitHub.
- Remote QG owns comprehensive compilation, tests, race checks, lint,
  architecture, coverage, security, documentation, and supported-platform
  matrices against the exact PR merge commit.
- Merge revalidation proves neither the candidate, target, workflow, QG
  evidence, nor ops-reviewed diff changed.

Candidate commits may exist on an isolated local worker branch before they are
validated; GitHub and CI need a commit object to identify what they test. The
hard guarantee applies to commits integrated into a target branch. The design
must explicitly choose whether internal worker commit ranges are normalized to
one validated candidate commit or whether every commit in the range is tested.
Testing only the range tip is insufficient if the requirement is literally
that every commit in target history compiles.

#### Local presubmit contract

Local presubmit keeps the broad static and compile-time protection of the
current pre-review QG, but decomposes it into independently scheduled actions.
It has no arbitrary wall-clock target. For each detected language/profile it
runs:

- the bead's exact acceptance command;
- asset staging, generated-file validation, and Git hygiene;
- every configured formatter;
- static lint and type analysis, including Go golangci-lint, NilAway, dead
  exports, import boundaries, architecture lint, build, and vet;
- changed-package tests plus configured direct-dependent tests;
- Python ruff format/check, pylint, pyright, and changed-scope pytest;
- shell formatting/lint and documentation/YAML/JSON validation.

The local scheduler admits checks rather than whole QGs. Many candidates and
many independent checks may progress together. Configuration exposes total
concurrent actions plus per-resource-class capacity so one memory-heavy linter
cannot multiply without serializing unrelated formatting, acceptance, docs,
or type checks. A check completes, fails, or is cancelled; elapsed time is an
observation, not a pass/fail policy.

The remote PR workflow reruns all local checks and additionally owns full
repository tests, race/shuffle suites, coverage enforcement, full pytest,
security scans, CGO-free and supported-platform builds, and complete build
matrices. It also runs incremental mutation against files/functions changed by
the PR. A below-policy score is deterministic failure; tool crash, missing base,
or timeout is infrastructure failure eligible for dispatcher retry, never a
warning-pass. Thus local results accelerate feedback but never substitute for
the comprehensive exact post-rebase remote gate.

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
      poll_min_interval: 5s
      poll_max_interval: 60s
      run_timeout: 35m
      outage_fallback_after: 15m
      close_superseded_prs: true
    local:
      profile: memory-safe
      max_actions: 6
      resource_capacity:
        cpu_light: 4
        cpu_heavy: 1
        memory_heavy: 1
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
- Configuration is loaded into a typed project-config model, passed by
  `cmd/oro/cmd_start.go:newProductionDispatcher` into `dispatcher.Config`, and
  validated before the daemon socket becomes available. File values have the
  normal project-config precedence; explicit CLI overrides, if introduced,
  win and are reported as the effective value. Unknown or malformed remote
  gate keys fail closed.
- GitHub preflight verifies workflow visibility and trigger eligibility for
  the project's actual target patterns, squash-merge availability, and a
  strict required-check ruleset covering `main`, configured targets, and
  `epic/**`. When the authenticated identity has repository-administration
  permission, setup may reconcile the documented Oro-owned ruleset
  idempotently; otherwise startup reports an unhealthy configuration and does
  not publish candidates. Routine beads never wait for operator setup.

The first version supports one GitHub remote and one workflow/check contract
per project. Candidate, evidence, correction, state-machine, and merge-policy
types remain provider-neutral; only the v1 adapter and configuration are
GitHub-specific. No second provider implementation or speculative generalized
framework is built.

### 2. Aggregate Workflow Contract

The `pull_request` trigger must not retain the current `branches: [main]`
filter. It covers every PR base the dispatcher supports, including configured
custom targets and ephemeral `epic/**` branches. Push CI may remain limited to
protected integration targets. Preflight rejects an actual target for which
the workflow is ineligible before publishing the candidate.

The existing CI jobs remain independently diagnosable. Add one final job with
a globally unique name:

```yaml
oro-portable-qg:
  if: ${{ always() }}
  needs: [go, cgo-free, shell, docs, python, incremental-mutation]
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
model secrets, and full-commit-SHA-pinned third-party actions. The workflow
must run the portable gate against GitHub's PR synthetic merge commit, not
merely the head branch. Static workflow-contract tests prove trigger
eligibility for main/custom/epic bases, one unique aggregate name, complete
`needs`, `always()` failure handling, read-only permissions, absence of
`pull_request_target` and secrets, pinned actions, and merge-commit checkout.

The `incremental-mutation` job invokes a new strict, machine-readable remote
mode, not the existing best-effort `--mutation-testing` behavior. Missing base,
tool crash, timeout, malformed/absent output, artifact loss, and zero mutants
when mutants were expected are non-success infrastructure conclusions. A valid
score below policy is deterministic failure. Fixtures cover each conclusion.

### 3. Dispatcher-Owned Remote Gate State Machine

Introduce a persisted state machine:

```text
candidate_adopted
  -> local_presubmit
  -> rebasing
  -> local_presubmit_post_rebase
  -> ops_review
  -> publishing
  -> awaiting_run
  -> running
  -> passed
  -> merge_authorizing
  -> github_merging
  -> local_sync
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
It does not claim a QG pass. Once the dispatcher durably adopts the candidate,
the worker is released to the pool and may move to another bead. Review and CI
therefore never rely on the original worker process remaining reserved.

Adoption validates message size, assignment ownership and freshness, clean
worktree, exact branch/ref, target, and that the SHA is committed and reachable
from that branch. Missing, dirty, mismatched, stale, or oversized messages fail
before adoption. Duplicate delivery is idempotent both before and after the
acknowledgement: the dispatcher persists the candidate row, dispatcher-owned
remote ref, and candidate-ref lease before acknowledging worker release.

The durable candidate row is independent of the active worker assignment. On
adoption, ownership of the candidate ref transfers from the assignment to the
dispatcher; the worker worktree may then be deleted or reused. Corrections
materialize from the persisted dispatcher-owned remote ref at the exact
candidate SHA into a fresh worktree. A missing remote ref, deleted original
worktree, stale `agent/<bead>` branch, or dead original worker is handled by
that source-of-truth order and never by worker affinity.

On `CANDIDATE_READY`, the dispatcher:

1. verifies the assignment, branch, clean worktree, and committed head;
2. serializes target-changing Git operations using the existing merge/branch
   coordination boundary;
3. fetches the configured remote and rebases the candidate onto the exact
   local target;
4. runs ops review against the exact post-rebase tree and persists its verdict;
5. ensures a non-main epic target exists on the remote before creating a PR;
6. publishes the candidate with `--force-with-lease=<remote-ref>:<observed-sha>`
   when a prior dispatcher-owned ref exists, never with an unconditional force;
7. creates or updates a draft PR whose base is the actual target branch;
8. persists PR identity before waiting for checks;
9. releases local Git coordination while CI runs;
10. observes exact check-run data through the adaptive pull scheduler;
11. validates and persists passing or failing evidence;
12. after pass, revalidates ops and CI evidence and requests a GitHub squash
    merge with the expected PR head SHA;
13. observes the merged SHA, verifies the merged tree, synchronizes the local
    target, closes the bead, and reconciles the PR and remote candidate ref.

The dispatcher never holds a repository lock while waiting for GitHub.

#### Adaptive GitHub observation

The dispatcher does not create one polling goroutine per PR. One project-local
scheduler owns a priority queue keyed by `next_observation_at` and batches API
queries where GitHub supports it. An observation is scheduled immediately when:

- a PR is created or its head/base is updated;
- ops review changes candidate eligibility;
- a prior observation reaches its next due time;
- a transient API retry becomes due;
- the dispatcher starts or resumes and finds a nonterminal remote gate;
- a status/progress request asks for freshness (the request returns cached
  state with `observed_at`; refresh happens asynchronously);
- the safety sweep finds a nonterminal record with no live timer.

Active queued/new runs begin at `poll_min_interval`. Stable running jobs back
off toward `poll_max_interval`; API errors use jittered exponential backoff.
Every state change resets to the minimum. Terminal records stop polling. ETags,
rate-limit headers, and a 60-second safety sweep prevent both API hammering and
lost wakeups. Webhooks are an optional future accelerator, never a correctness
dependency for a dispatcher behind NAT.

### 4. GitHub Client Boundary

Define provider-neutral policy types and a narrow dispatcher dependency so
behavior is testable without network access. The core names a change request,
target, candidate, evidence, and merge capability; it does not expose `gh`
JSON or GitHub PR types:

```go
type RemoteGateClient interface {
    Preflight(ctx context.Context, req PreflightRequest) (Capabilities, error)
    Publish(ctx context.Context, req PublishRequest) (PublishedCandidate, error)
    EnsureChange(ctx context.Context, req EnsureChangeRequest) (RemoteChange, error)
    Observe(ctx context.Context, req ObserveGateRequest) (RemoteGateObservation, error)
    AuthorizeSquashMerge(ctx context.Context, req MergeAuthorizationRequest) (MergeResult, error)
    Cancel(ctx context.Context, req CancelGateRequest) error
    Reconcile(ctx context.Context, req ReconcileChangeRequest) error
}
```

`MergeAuthorizationRequest` contains repository identity, remote change ID,
expected candidate head SHA, expected target ref and SHA, exact evidence ID,
and reviewed tree. `Capabilities` must attest that the provider's merge
boundary atomically rejects a changed tested base. The GitHub adapter satisfies
this through a verified strict required-check ruleset and sends the expected
PR head SHA to the squash endpoint. Ambiguous timeout-after-success responses
are reconciled by observing the persisted change before retrying.

The production implementation shells out to `git` and `gh` using argument
arrays and JSON output. It reuses `processenv.ForWorkdir`, authenticates against
the actual remote host, and applies per-call contexts. No shell command is
constructed from PR titles, branch names, URLs, or remote output.

The dispatcher owns policy, persistence, retry classification, and state
transitions. The client owns only provider/git side effects and normalized
observations. Compile-time boundary tests prevent dispatcher policy packages
from importing GitHub/`gh` transport representations or shelling out to `gh`.

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

On restart, the GitHub adapter's `EnsureChange` searches first by persisted PR number and then by
exact head/base refs. It may adopt only a PR whose repository, head ref, base
ref, and bead metadata all match. Ambiguous matches fail closed and preserve
the work in quarantine.

PR titles and bodies are generated by the dispatcher and contain no prompt or
card content beyond the bead ID, title, target, commit SHA, and a short factory
status marker.

Mode changes cannot create two completion paths. Switching to `local` while a
remote record is publishing, running, passed, or merging stops new remote
adoptions but leaves that record under remote reconciliation until it reaches
a proven terminal state or is durably cancelled and requeued. Best-effort PR
cancellation never releases candidate refs or enables legacy `DONE` merge.

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
- the passing evidence row commits successfully before merge authorization;
- no later candidate, target, or workflow change supersedes it.

The adapter obtains run identity from GitHub's workflow-runs/check-runs APIs,
then independently fetches the immutable workflow file at the run commit and
the PR head/base objects. It persists workflow database ID, workflow path,
workflow blob SHA, run attempt, check-suite/check-run IDs, head SHA, recorded
base SHA, and synthetic merge SHA before transitioning to `passed`. It verifies
that the synthetic merge commit has the expected candidate and target parents
and expected tree. A mutable current `pull_request.merge_commit_sha`, a check
name alone, or the latest run for a branch is never sufficient. Reruns,
same-name checks, workflow edits, base movement, and stale merge SHAs are
explicit negative fixtures.

Status checks named the same by another workflow are rejected. The dispatcher
uses workflow identity plus check name, not check name alone.

### 7. Target Movement and Merge

Target movement is normal under parallel workers. It is not an operator event.

Before ops review, before publication, and again immediately before merge
authorization, the dispatcher compares:

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

After exact validation, the dispatcher re-verifies that the actual target is
covered by the preflighted strict required-check ruleset, then calls the
adapter's `AuthorizeSquashMerge` with `merge_method=squash`, exact expected PR
head SHA, target ref/SHA, and evidence ID. GitHub's endpoint directly guards
the head while the required-up-to-date ruleset provides the atomic base guard:
if the base advances, the required aggregate becomes stale and GitHub rejects
the merge. A target without this provider-enforced guarantee is unsupported in
`github-pr` mode. A 405/409, changed protection, or changed head/base means the
bead is not closed and re-enters preflight or the rebase/review/gate loop.

After GitHub reports the merged SHA, the dispatcher fetches the remote and
proves the three-way invariant: reviewed post-rebase candidate tree equals the
tested synthetic merge tree equals the resulting squash-commit tree. Git tree
identity covers modes, symlinks, submodule gitlinks, and LFS pointer blobs. An
empty/no-op candidate is rejected before publication. An unexpected mismatch
after a provider merge is a durable P0 recovery state: it never closes the bead
or deletes refs even though the external target has already changed. After a
valid proof, the dispatcher updates the local target by fast-forward only. In the normal clean
factory checkout this is `git merge --ff-only origin/<target>`; a non-checked-
out target ref may be compare-and-swap updated. Tracked local edits or divergent
local commits are preserved and routed to dispatcher recovery—never reset,
overwritten, or silently discarded.

### 8. Failure Classification and Worker Recovery

Remote observations are classified before action:

| Class | Examples | Dispatcher action |
|---|---|---|
| deterministic | test, lint, build, coverage, or aggregate dependency failure | Fetch bounded failed-step evidence, persist a correction checkpoint on the original bead, and enqueue a fresh correction assignment for any available worker |
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

The original worker is not reserved while ops review or GitHub runs. The
dispatcher owns a candidate worktree/ref after `CANDIDATE_READY`; the worker
may immediately accept another bead. Any review or CI rejection reopens the
original bead in a correction stage and creates a normal pool assignment. The
same process may receive it if idle, but correctness never depends on affinity.
The correction worker starts from the preserved remote candidate ref or a fresh
worktree at that exact SHA and receives the durable checkpoint before coding.
This preserves the requirement that unchanged code cannot repeat a rejection
cycle without seeing the actual findings.

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
target SHA. It may advance the durable record to `local_passed_waiting_remote`,
release local compute, and preserve the candidate, but it cannot perform a
GitHub squash merge while the merge API or its protection proof is unavailable.
After GitHub recovers, the dispatcher revalidates target/review/local evidence,
authorizes the protected merge, and returns new candidates automatically to
remote mode. Thus coding and local validation may continue during a long
outage, while integration throughput correctly waits for the authoritative
remote merge boundary. Outages are tested separately during publish, observe,
and merge authorization.

The concrete fallback interface is
`scripts/quality_gate.sh --profile=memory-safe`. It acquires one project-global
lease, runs outer lanes sequentially, exports `GOMAXPROCS=2`, propagates
`go test -p 1`, limits every inner scheduler to one, forwards cancellation, and
always releases the lease. Behavioral subprocess tests assert those commands
and limits rather than trusting environment variables by convention.

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

After a successful GitHub squash merge, reconciliation proves the merged target
tree equals the reviewed and tested candidate tree. It then:

1. records GitHub's merged SHA and synchronizes the local target by
   fast-forward only;
2. verifies GitHub marked the PR merged;
3. deletes the remote candidate ref with a lease;
4. archives or safely deletes the non-ancestor worker branch only after
   tree-equivalence and durable PR evidence prove no unique work remains;
5. runs local worktree cleanup and emits `remote_gate_reconciled`.

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
- restore the candidate pipeline status independently of worker lifetime;
- resume backoff from persisted attempt time rather than stampeding GitHub.

All transitions use compare-and-swap on the record version. Only one dispatcher
instance may advance a record. Reconciliation is idempotent: repeating it after
any individual side effect produces the same live PR/run or a conservative
quarantine, never a duplicate merge.

If the original worker process is absent, a deterministic failure opens a
normal correction assignment with its checkpoint. A passing gate may proceed
to merge authorization only after current branch, review, and evidence state
is reverified.

### 12. Epic Promotion

Child beads targeting an epic branch use PRs whose base is that epic branch.
The dispatcher publishes and advances the remote epic branch as children merge.

When all children and acceptance criteria are closed:

1. create/update an epic promotion PR from the epic branch to its actual target;
2. run the aggregate portable gate against the combined epic merge commit;
3. persist exact epic evidence;
4. run final epic review/acceptance;
5. authorize GitHub to squash merge through the normal exact-state path;
6. reconcile the promotion PR and remote epic ref;
7. perform the existing local `make build install`, installed/repo binary hash
   match, controlled Oro restart, and healthy-dispatch verification.

The dispatcher owns steps 1–6. Local factory lifecycle automation owns step 7;
it must be a durable post-epic operation rather than an informal operator
reminder. Failure in step 7 keeps the epic completion operation visible and
retryable without undoing a proven merge.

The durable epic states distinguish `promotion_gate`, `remote_merged`,
`local_install_pending`, `restart_pending`, `health_verification`, and
`complete`. `tryCloseEpic`, `completeEpicClose`, and `ffMergeEpicBranch` must
dispatch by gate mode; the legacy local-QG/FF path cannot close an epic in
`github-pr` mode. The epic closes only after installed/repository binary hashes
match and healthy dispatch is observed.

### 13. Remote Auditor Mutation Campaign

Every periodic whole-repository auditor cycle triggers exactly one distinct
GitHub Actions workflow against the audit's exact current target SHA. The audit
cycle is not complete until that mutation workflow reaches a valid terminal
result and its artifacts are durably incorporated. This is separate from both the
per-PR portable gate and `quality_gate.sh --mutation-testing` because the
existing flag is incremental: Go targets changed files/touched functions, has
an eight-minute cap, and treats timeout as a warning.

The remote audit workflow:

- uses `workflow_dispatch` with audit run ID, repository target, and exact SHA;
- checks out an ephemeral workspace at that SHA;
- installs pinned Go tools from `go.mod` and Python tools from the lock/project
  configuration;
- shards full Go mutation across configured packages/files instead of mutating
  one checkout concurrently;
- runs the full Python `cosmic-ray-full.toml` campaign when configured;
- never mutates a shared or dispatcher worktree;
- uploads machine-readable per-shard results, surviving mutants, killed/total
  counts, score, tool versions, and logs;
- aggregates only after every required shard reaches a terminal conclusion;
- distinguishes infrastructure failure from a valid campaign whose mutation
  score is below policy;
- persists exact workflow/run/job/artifact/SHA evidence in the audit journey;
- lets the auditor translate surviving mutants and policy regressions into
  deduplicated, evidence-backed beads without operator intervention.

The dispatcher uses the same adaptive GitHub observation scheduler and restart
reconciliation as PR gates. Mutation campaigns have their own remote
concurrency group. Auditor cycles do not overlap for the same project: a new
cycle adopts an already-running campaign for the same target SHA or waits for
the prior different-SHA cycle to finish; it never silently cancels evidence the
auditor is required to consume. Local mutation remains only an explicit
fallback for projects whose configured mutation command genuinely requires
local hardware or services.

The full campaign gates completion of its auditor cycle and always produces
auditor evidence or an explicit audit infrastructure failure. It does not block
ordinary bead PRs or epic promotion. Surviving mutants and policy regressions
become prioritized repair beads through the normal auditor finding path. Full
mutation must never be inserted into every bead PR gate because that would
create a new critical path at a much higher compute cost.

`pkg/dispatcher/audit.go:runAudit` persists the audit snapshot and remote
campaign key before dispatch, waits through the shared scheduler, and cannot
call the existing completion path until every shard artifact is validated and
incorporated. Restart adopts the same workflow for the same audit ID/SHA.
Infrastructure retries are bounded by configured campaign policy; exhausting
them completes the *attempt* as a durable audit-infrastructure failure, keeps
the audit cycle non-successful and visible, and schedules self-healing retry
work without blocking bead/epic gates. A valid campaign with surviving mutants
completes evidence ingestion and files deduplicated repair beads.

### 14. Observability

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

### 15. Implementation Coverage Plan

`beadcraft` must preserve these work packages and their named production call
chains; it may split them further but may not collapse away a boundary:

1. **Typed configuration and repository preflight.** Read and change the
   project config model, `cmd/oro/cmd_start.go:newProductionDispatcher`,
   `dispatcher.Config.validate`, and `Dispatcher.Run` before socket readiness.
   Cover defaults, precedence, malformed values, workflow eligibility, target
   rulesets, auth, squash support, startup events, and status.
2. **Provider-neutral core and GitHub adapter.** Implement normalized
   candidate/change/evidence/merge types, all `RemoteGateClient` operations
   including ambiguous `AuthorizeSquashMerge`, and GitHub transport isolated
   behind the adapter. Read `pkg/janitor/detect.go` for environment/host/auth
   conventions and add boundary/import tests.
3. **Protocol and worker handoff.** Wire `CANDIDATE_READY` through
   `pkg/protocol/message.go`, `pkg/worker/worker.go:awaitSubprocessAndReport`,
   `runQGAndReport`, `SendReadyForReview`, and `SendDone`. Update
   `pkg/worker/prompt.go:buildCodingSections`. Prove malformed, oversized,
   stale, and duplicate handoffs fail safely and GitHub mode cannot enter the
   legacy full-QG or `DONE` merge path.
4. **Durable remote state and dispatcher ownership.** Add normalized SQLite
   schema/types/indexes/migrations/CAS for candidate, run, evidence,
   correction, audit campaign, and post-install state. Wire `handleMessage`,
   `handleDone`, `handleReadyForReview`, `handleReviewResult`,
   `startupRecovery`, `restoreState`, and `spawnBackgroundLoops`. Test restart
   after worker death and worktree deletion.
5. **Local presubmit action scheduler.** Replace the worker monolithic gate in
   GitHub mode with completion-based actions, total/per-resource admission,
   cancellation, exact acceptance execution, and pre/post-rebase invalidation.
   Test concurrency across candidates and serialize only declared heavy
   resources.
6. **Workflow and strict incremental mutation.** Change
   `.github/workflows/ci.yml`, add `scripts/ci/require-needs-success.sh`, a
   strict machine-readable incremental mutation command, workflow fixtures,
   SHA-pinned actions, and aggregate membership/trigger tests for main,
   custom, and epic bases.
7. **Exact evidence, ops review, and protected merge.** Wire the remote state
   machine through `checkPreMergeQG`, `mergeAndComplete`, and
   `finalizeSuccessfulMerge`; bind the ops-reviewed tree, tested synthetic
   merge tree, strict target policy, and squash result. Test base races,
   same-name checks, reruns, changed workflow, ambiguous merge response,
   unexpected result tree, and local divergence without destructive reset.
8. **Correction and cleanup.** Persist bounded findings, create a normal pool
   correction assignment at the exact remote candidate SHA, and handle dead
   workers, deleted worktrees, missing refs, duplicate findings, non-ancestor
   squash cleanup, cancellation, rollback, and recovery quarantine.
9. **Epic promotion and local installation.** Replace the GitHub-mode path in
   `tryCloseEpic`, `completeEpicClose`, and `ffMergeEpicBranch` with promotion
   state, remote evidence/merge, local sync, durable build/install/restart,
   hash comparison, health proof, and retry.
10. **Remote full mutation audit.** Add the workflow-dispatch/shard/aggregate
    workflow and wire `pkg/dispatcher/audit.go:runAudit` to durable exact-SHA
    campaign observation, artifact ingestion, restart, infrastructure failure,
    and survivor bead creation.
11. **Observability and self-healing.** Extend dispatcher/status JSON,
    `cmd/oro/cmd_status.go`, health online/offline loaders, monitor defect
    rules, dashboard provider/templates, and progress responses for every
    required state and finding.
12. **Hermetic epic verification and canary.** Build
    `scripts/test_remote_gate_epic.sh` around a real local Git remote and a
    deterministic GitHub API/`gh` fake. Parse `go test -json` and require a
    test-level pass event for every exact named integration test. Statically
    validate both PR and full-mutation workflows, then run the controlled Oro
    GitHub canary only after current local history and the workflow are
    published.

### 16. Rollout

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

Rollback sets `mode: local` only for new candidates. Durable remote records
remain audit evidence; active dispatcher-owned runs are reconciled to terminal
or durably cancelled before their candidates are requeued, and candidate
branches/refs are preserved. A legacy `DONE` or local merge can never race a
nonterminal remote record.

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
6. Workflow contract fixtures prove PR eligibility for main, a configured
   custom target, and an `epic/**` target; prove the aggregate includes every
   portable job including strict incremental mutation; and reject mutable
   action tags, write permissions, secrets, `pull_request_target`, head-only
   checkout, missing/skipped needs, and a non-strict target ruleset.
7. GitHub mode runs the configured local presubmit actions with bounded
   total/resource concurrency, then replaces the production
   `READY_FOR_REVIEW`/`DONE` local-QG merge path with durable candidate
   adoption. Malformed and duplicate handoffs, worker death, deleted worktree,
   missing remote ref, and correction by a different worker are covered.
8. Exact evidence tests reject same-name checks, changed workflow blobs,
   reruns for another attempt, stale synthetic merge SHAs, target movement at
   the merge boundary, ambiguous merge responses, and any inequality among
   reviewed, tested, and squash-result trees.
9. Every auditor cycle dispatches or adopts exactly one full mutation campaign
   for its audit ID and SHA, survives restart, validates every shard artifact,
   distinguishes infrastructure failure from surviving mutants, and creates
   deduplicated repair beads before a successful audit completion.
10. Provider-neutral core packages compile without GitHub transport imports or
    direct `gh` execution; a deterministic GitHub adapter fake exercises every
    side effect including protected squash authorization and reconciliation.

Epic verification command:

```text
Cmd: test "$(git branch --show-current)" = main && ./scripts/test_remote_gate_epic.sh && ./scripts/test_quality_gate.sh && ./scripts/quality_gate.sh
Assert: exit 0. The remote-gate harness uses `go test -json` and fails unless
every exact integration test emits a test-level `pass` event; exercises a real
local Git remote and deterministic GitHub API/`gh` fake; validates PR and full
mutation workflow contracts, status/health/monitor/dashboard surfaces, strict
incremental and full mutation outcomes, and provider boundaries. The existing
QG harness and full repository gate also pass on main.
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
    - risk: "The auditor labels the incremental, timeout-tolerant mutation command as a full mutation pass."
      severity: high
      mitigation_checked: "The design requires a separate sharded full-repository workflow, exact target SHA, terminal evidence from every shard, and distinct infrastructure-failure versus mutation-score outcomes."

  elephants:
    - risk: "PR-per-bead increases remote noise and consumes hosted CI capacity."
      mitigation: "Draft PRs, deterministic refs, max_in_flight, cancellation, and automatic reconciliation bound the noise; the audit trail is part of the desired capability."
    - risk: "The dispatcher requests a squash merge after its evidence becomes stale."
      mitigation: "A dispatcher-preflighted strict required-check ruleset provides the provider-side atomic base guard, the merge request supplies expected head SHA, and Oro revalidates head, base, workflow, review, and QG evidence immediately before authorization."
    - risk: "Automatic local fallback can still be slower than remote CI and one tool can individually exhaust memory."
      mitigation: "Fallback is serialized and memory-safe, but absolute memory safety needs OS-level resource control as a future capability."

  paper_tigers:
    - risk: "GitHub merge queue is unavailable for this user-owned public repository."
      reason: "The selected design does not depend on merge queue; a verified strict ruleset makes the aggregate stale on base movement, expected-head squash merge rejects head races, and the dispatcher automatically rebases/retries."
    - risk: "Squash merge makes the candidate branch a non-ancestor of target."
      reason: "Reconciliation binds the tested candidate tree to GitHub's merged tree before cleaning the preserved worker and remote refs."
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
- [x] DECISION: Which projects can use remote gates?
      ANSWER: Any Oro-managed GitHub repository can opt in through project
      configuration. The Oro repository is the first canary; implementation
      contains no Oro-repository or developer-account special cases.
- [x] DECISION: What quality model should the design emulate?
      ANSWER: A large-engineering-team presubmit model: fast local feedback,
      exact post-rebase review, comprehensive remote validation, and no target
      integration until every required independent gate passes.
- [x] DECISION: Which checks are definitely remote?
      ANSWER: The comprehensive post-rebase QG, including full compilation,
      tests, lint, architecture, coverage, security, docs, and portable build
      matrices, runs on GitHub against the PR merge commit.
- [x] DECISION: Does ops review remain a required independent gate?
      ANSWER: Yes. It checks design and code health beyond mechanical QG, and
      reviews the exact post-rebase diff before that candidate is published.
- [x] DECISION: How is the guarantee that every integrated commit compiles
      enforced when a worker creates multiple internal commits?
      ANSWER: GitHub performs a protected squash merge after exact review and
      remote QG evidence pass. Worker/WIP commits remain on preserved candidate
      refs but do not enter target history.
- [x] DECISION: Who owns PR creation, CI waiting, retry, merge, and cleanup?
      ANSWER: The dispatcher; the operator only observes surfaced state.
- [x] DECISION: Is GitHub or the local worker authoritative for portable QG in
      remote mode?
      ANSWER: GitHub exact PR merge evidence is authoritative; local runs only
      macOS checks or the explicitly memory-safe outage fallback.
- [x] DECISION: Who creates the final Git commit?
      ANSWER: The worker creates candidate commits; the dispatcher authorizes;
      GitHub creates one squash commit on the target; the dispatcher fetches
      and fast-forwards local state after verifying tree equivalence.
- [x] DECISION: Must the original worker remain occupied during review and CI?
      ANSWER: No. Candidate adoption releases it. Rejections become durable
      correction assignments consumable by any available worker, with exact
      candidate state and bounded findings restored.
- [x] DECISION: Is local presubmit governed by a time target?
      ANSWER: No. It is governed by an explicit check set and configurable
      concurrent scheduling. Many local presubmit actions may run at once;
      heavyweight comprehensive work remains remote.
- [x] DECISION: Which existing QG checks remain in local presubmit?
      ANSWER: Broad formatting, static analysis, type analysis, compilation,
      vet, architecture/import checks, changed-scope tests, exact acceptance,
      shell/docs/config validation, staging, and Git hygiene remain local as
      independently scheduled actions. Remote CI reruns them and owns the full
      dynamic suites, coverage, security, and platform matrices.
- [x] DECISION: Where does the auditor execute a full mutation campaign?
      ANSWER: On GitHub in a separate sharded workflow at an exact target SHA.
      The existing incremental mutation QG is not misrepresented as full.
- [x] DECISION: When does incremental mutation run?
      ANSWER: Every remote PR QG runs changed-scope incremental mutation on
      GitHub. Mutation timeout/tool error is retryable infrastructure failure,
      not a pass; below-policy score is deterministic failure.
- [x] DECISION: When does full mutation run and what does it gate?
      ANSWER: Every auditor cycle triggers one full GitHub mutation campaign at
      the exact audit SHA and cannot complete until it incorporates a valid
      terminal result. It files repair beads but does not block ordinary bead
      or epic merges.
- [x] DECISION: What happens when the target moves?
      ANSWER: Dispatcher automatically invalidates evidence/review, rebases,
      republishes, and reruns.
- [x] DECISION: What happens when GitHub is transiently unavailable?
      ANSWER: After 15 minutes the dispatcher uses one memory-safe local gate
      and automatically returns to remote mode.
- [x] DECISION: Is replacing normal local full-QG execution necessary?
      ANSWER: Yes. The serialized local gate is a dominant throughput defect,
      not an optional optimization opportunity. Remote execution is the normal
      architecture; local full QG survives only as controlled degraded fallback.
- [x] DECISION: How should the design accommodate future CI providers?
      ANSWER: Candidate lifecycle, evidence, correction, recovery, and merge
      authorization contracts are provider-neutral. V1 implements only a
      GitHub adapter; no speculative multi-provider framework is built.
- [x] DECISION: Is a visible draft PR for every bead an acceptable permanent
      factory audit trail, or should successful bead PRs be collapsed later?
      DEPENDS_ON: None.
      RECOMMENDATION: Keep one draft PR per bead and auto-reconcile it; this is
      the narrowest implementation and gives exact CI/audit identity.
      ASK: Confirm PR-per-bead as the v1 unit of remote work.
      ANSWER: Yes. One dispatcher-owned draft PR per bead is the v1 remote work
      and evidence unit; the dispatcher updates, squash-merges, and reconciles
      it automatically.
