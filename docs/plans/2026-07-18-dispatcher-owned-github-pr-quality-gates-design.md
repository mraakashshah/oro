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
performs the existing ops review, authorizes an atomic GitHub-hosted squash
integration,
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
- Preserve linear target history with one squash commit per bead, atomically
  accepted by the GitHub target ref only while its tested base is still exact.
- Route deterministic CI findings back to the assigned worker as structured,
  bounded feedback.
- Recover idempotently after dispatcher, worker, API client, network, or GitHub
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
3. **No stale merge:** immediately before authorizing the GitHub-hosted squash integration, the dispatcher
   proves that local target, remote target, candidate head, and recorded
   evidence still match. Any mismatch invalidates evidence and re-enters the
   rebase/publish/gate loop.
   GitHub cannot atomically condition a ref update on a previously observed
   ruleset hash; the narrower policy-drift contract in section 4 governs that
   final provider race and never misreports an already-integrated commit.
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
    boundary. GitHub v1 creates one commit whose parent is the exact tested base
    and whose tree is the exact reviewed/tested tree, then updates the target
    ref with an exact `--force-with-lease=<target>:<tested-base>` transaction.
    GitHub accepts the ref update only when the current ref value is exactly the
    expected base; forward, backward, or rewritten movement rejects atomically.
    Mutable policy observations or an expected-head-only PR merge request are
    not correctness boundaries.
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

`pkg/janitor/detect.go:ciDetector` establishes useful evidence conventions:

- discover `gh` with `exec.LookPath`;
- derive the host from the repository's `origin` remote;
- verify active authentication;
- execute `gh` with the worktree-specific environment;
- parse JSON rather than terminal text;
- retain failing run URLs and failed-job evidence.

The remote-gate implementation standardizes on `gh` for GitHub APIs and reuses
host derivation, structured JSON, and evidence concepts, but not janitor's
ambient executable/environment lookup. Janitor asks whether the latest branch
CI failed; merge authorization requires an installation-managed, setup-attested
`gh` executable, minimal credential environment, exact SHA identity, and durable
state.

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
  -> dispatcher-authorized GitHub target-ref CAS of one squash commit
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
  lifecycle:
    auto_install_after_epic: true
    supervisor: managed-monitor
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
      cli:
        executable: managed
        install_if_missing: true
      api:
        base_url: https://api.github.com
        ca_bundle_ref: system
        proxy: none
        api_version: "2022-11-28"
      runtime_identity:
        type: github-app
        app_id: 123456
        installation_id: 789012
        private_key_ref: keychain:oro/github-app
      policy_reconciliation:
        enabled: true
        owned_ruleset_key: oro-target-policy-018f...
        owned_ruleset_name: oro:project-identity:target-policy
        desired_template_hash: sha256:...
        maintenance_identity:
          type: github-app
          app_id: 654321
          installation_id: 789012
          private_key_ref: keychain:oro/github-maintenance-app
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
- `gh` is an Oro installation dependency. On macOS, `make install` and
  `oro setup` run the idempotent equivalent of `command -v gh >/dev/null || brew
  install gh`; installation fails actionably if Homebrew is unavailable or the
  supported `gh` version/identity cannot be verified. Packaged Linux installs
  declare `gh` as a dependency; an unpackaged Linux setup fails with the exact
  supported package-manager command rather than silently disabling the feature.
  Rebuild/reinstall after an epic rechecks but does not reinstall an already
  valid CLI.
- `github-pr` is valid only when the remote resolves to GitHub, the required
  setup-attested `gh` CLI and API host validate,
  the configured runtime credential provider resolves the expected GitHub App
  installation for that host/repository, the workflow is visible, and the
  aggregate check contract can be found. Ambient unvalidated `gh`, SSH-agent, Git
  credential-helper, or developer tokens never satisfy this requirement.
- Invalid explicit configuration fails startup with a configuration error. It
  does not silently switch modes.
- `--manual-integration` is incompatible with effective `github-pr` mode. The
  remote factory is an autonomous contract; it does not create a hidden manual-
  approval state. Both `oro start`/`ExecDaemonSpawner` and
  `oro dispatcher start`/`runDaemonOnly` reject the combination before daemon
  socket availability, worker spawn, candidate publication, or remote recovery
  mutation, and daemon-side typed validation repeats the check so a handcrafted
  child invocation cannot bypass it. Local mode retains existing manual
  integration behavior.
- GitHub PR mode requires typed policy-reconciliation configuration for the
  one Oro-owned repository ruleset. Configuration and the supervisor descriptor
  carry a stable logical ownership key/name/template hash, never an immutable
  provider ruleset ID; the active provider ID is versioned durable state because
  recreation returns a new ID. The maintenance identity is a distinct
  GitHub App installation credential minted only with Metadata-read and
  Administration-write; every Contents, Pull requests, Actions, Checks,
  Workflows, secrets, organization, and account permission is forbidden. The
  runtime App never receives Administration. Missing/malformed identity,
  repository/host mismatch, permission drift, or inaccessible secret reference
  fails startup or recovery as auth/config rather than falling back to ambient
  administration.
- A runtime GitHub outage uses the separately configured degraded-mode policy;
  configuration errors are not outages.
- `max_in_flight` limits dispatcher-owned remote candidates, preventing an
  accidental PR/run explosion even though the computation is no longer local.
- The effective mode and all limits appear in status and startup events.
- Every `github-pr` project requires a live, version-compatible external
  supervisor with a recent durable heartbeat, independently of
  `auto_install_after_epic`, because the isolated monitor is the only claimant
  allowed to use maintenance authority for policy self-healing. In v1 this is a
  stable per-project supervisor shim installed and enabled by setup (launchd on
  macOS, systemd user service on Linux) which launches a versioned
  `oro monitor --act` child. Startup fails before dispatch when the descriptor,
  service enablement, protocol, heartbeat, or maintenance capability attestation
  is absent or stale. Local gate mode may remain monitorless when epic auto-
  installation is disabled.
- Setup atomically writes a versioned per-project supervisor descriptor under
  the project-scoped Oro state directory. It contains canonical real repository
  root, immutable project identity, absolute `ORO_HOME`, state DB/PID/socket/
  lifecycle-ledger paths, installed executable path, worker/start configuration,
  schema version, runtime credential-provider reference, expected App/
  installation/host/repository identity, the distinct maintenance credential-
  provider reference and expected identity/scope, network transport policy, and
  descriptor hash. It contains no credentials. Managed
  monitor, health, restart, and `startFreshSwarm` accept this descriptor
  explicitly and never rediscover project context from CWD, `ORO_PROJECT`, or
  ambient environment.
- Dispatcher health continuously leases the supervisor heartbeat. A stale or
  incompatible claimant sets durable `maintenance_unavailable`, blocks new
  integration intent and target CAS, and is not classified as a GitHub outage
  eligible for local fallback. The OS service restarts the stable shim, which
  restarts the monitor; any already-durable reconciliation request is then
  reclaimed. The live heartbeat/capability generation is rechecked immediately
  before integration authorization. No in-process dispatcher fallback may mint
  the Administration token.
- Configuration is loaded into a typed project-config model, passed by
  `cmd/oro/cmd_start.go:newProductionDispatcher` into `dispatcher.Config`, and
  validated before the daemon socket becomes available. File values have the
  normal project-config precedence; explicit CLI overrides, if introduced,
  win and are reported as the effective value. Unknown or malformed remote
  gate keys fail closed.
- GitHub preflight verifies workflow visibility and trigger eligibility for
  the project's actual target patterns and a
  strict required-check ruleset covering `main`, configured targets, and
  `epic/**`. A dedicated least-privilege integration identity may bypass the
  PR/required-check rule only for the final target-ref CAS; it has contents/PR
  plus the exact Actions/check/workflow permissions enumerated below, but no
  repository administration or ruleset-write permission.
  No human or general worker identity is a bypass actor. The same
  evaluation resolves the complete effective repository and organization
  policy for each target. V1 rejects any overlapping rule that requires human
  approving/CODEOWNER reviews, conversation resolution, deployments, merge
  queue, signed-commit behavior the provider cannot satisfy, or another
  unsupported actor/restriction/lock/read-only condition. It also proves the
  dispatcher can update candidate refs, create the exact squash commit, and
  perform the exact-SHA leased target update through that dedicated identity. The
  evaluated rule IDs, versions, enforcement modes, bypass actors, and canonical
  policy hash become capability evidence. The complete policy is re-read
  immediately before authorization; removal, mutation, newly effective policy,
  unexpected bypass eligibility, or ambiguous enforcement observed before the
  target mutation quarantines the candidate instead of merging. Repository
  administration used by setup is a
  separate credential from the runtime integration identity. Setup may
  reconcile the documented Oro-owned ruleset idempotently; otherwise startup
  reports an unhealthy configuration and does not publish candidates. Routine
  beads never wait for operator setup.
- Preflight discovers and persists the repository default branch separately
  from the configured integration/audit target. The full-mutation workflow path
  must exist on the current default branch and declare `workflow_dispatch`, even
  when the audited snapshot belongs to a custom/release branch. Absence,
  disablement, or invalid trigger is auth/config failure.
- Preflight also evaluates repository and organization rules against a
  prospective exact `oro/audits/<project-prefix>/**` ref, independently of
  candidate and target policy. It proves the runtime App may create, adopt by
  exact SHA, and delete that namespace. Setup's capability canary uses a unique
  ref inside that exact namespace, creates it with an expected-absent lease,
  observes it, and deletes it with an exact-SHA lease; a generic probe ref is
  insufficient. Applicable rule IDs, patterns, creation/update/deletion
  restrictions, enforcement modes, bypass actors, and policy hash are persisted.
  A failed cleanup remains a visible retryable setup defect rather than being
  hidden or deleted with the administration credential.
- Preflight separately evaluates create/update/delete rules and runtime-App
  bypass for prospective `epic/**` refs. The capability canary creates,
  observes, advances by exact old SHA, and lease-deletes a unique ref in that
  exact namespace. Candidate or audit-ref capability is not evidence for an
  epic PR base. The effective policy is re-attested for each concrete epic ref
  immediately before create/recreate and delete; drift fails before mutation or
  enters durable cleanup pending after retirement.
- `runtime_identity` is typed and separate from setup administration. GitHub v1
  uses a GitHub App installation credential provider. `private_key_ref` points
  to an OS secret store or an approved credential-command provider; secret
  material is never stored in project YAML. The provider returns a redacted
  credential handle, expiration, App/installation actor IDs, host, and allowed
  repository. It refreshes with safety skew before expiry and persists only
  nonsecret actor/expiry metadata. Refresh failure is classified as transient
  when the source is temporarily unavailable and auth/config when identity or
  scope is invalid.

The runtime installation token is minted with an exact allowlist, not every
permission the App might possess. V1's canonical repository permission matrix
is:

| Permission | Level | Used for |
|---|---|---|
| Metadata | read | repository identity and effective branch rules |
| Contents | write | authenticated Git fetch/publish and exact target CAS |
| Pull requests | write | create/update/ready/close the CI PR |
| Actions | write | dispatch/cancel audit workflows; read runs, jobs, logs, and artifacts |
| Checks | read | observe the exact aggregate check/run evidence |
| Workflows | write | publish candidate commits that legitimately change workflow files |

All other repository/organization/account permissions—including
Administration, secrets, deployments, environments, webhooks, members, and
ruleset mutation—must be absent from the minted runtime token. The adapter uses
the effective-rules-for-branch read endpoint available through Metadata rather
than requiring Administration. If a configured host reports a different
endpoint permission contract, capability probing decides support; the adapter
does not silently broaden the token.

`Capabilities` persists the required, granted, and forbidden permission sets,
canonical permission hash, host/API version, endpoint-probe evidence, and typed
provider execution limits including `max_matrix_entries`. GitHub.com's adapter
reports 256; the core never hardcodes that value, and a host whose limit cannot
be established fails full-mutation preflight rather than guessing. Token
issuance response permissions/repository scope must equal the allowlist. Setup
then runs an isolated capability canary: push/delete an Oro probe ref, create/
ready/close a probe PR, dispatch and observe a no-op workflow that uploads a
small digest-checked artifact, and dispatch/cancel a second run. Probe refs,
PRs, runs, and artifacts are namespaced and reconciled automatically. Explicit
`github-pr` startup requires recent successful evidence; it never discovers a
missing write permission on a production bead.

Before dispatch, cancellation, artifact ingestion, PR mutation, or target CAS,
the provider refreshes and re-attests the permission hash. Revocation or
permission drift is auth/config failure, not a transient outage. Permission-
aware fakes enforce each endpoint independently, test every one-permission-
missing case and every forbidden extra permission, model the
`X-Accepted-GitHub-Permissions` response, and cover GitHub Enterprise hosts.

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
  -> ensuring_ephemeral_target (non-main epic base only)
  -> publishing
  -> awaiting_run
  -> running
  -> passed
  -> readying_change
  -> ready_for_merge
  -> integration_preparing
  -> integration_prepared
  -> integration_intent
  -> merge_authorizing
  -> github_merging
  -> integrated_policy_drift (only when post-CAS policy differs)
  -> local_sync
  -> reconciled

Failure branches:
  deterministic_failed -> worker_retry
  transient_failed     -> backoff -> awaiting_run/re-publish
  target_moved         -> rebasing
  outage_degraded      -> local_memory_safe_gate
  preserved_ambiguity  -> quarantine
  cancel_pending       -> reconcile integration outcome -> reconciled/cancelled
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
local adoption ref, and candidate-ref lease before acknowledging worker
release. Remote-ref identity may be reserved at adoption, but it is not treated
as durable storage until publication and exact remote observation succeed.

The durable candidate row is independent of the active worker assignment. On
adoption, ownership of the candidate ref transfers from the assignment to the
dispatcher; the worker worktree may then be deleted or reused. Corrections
materialize through the ordered candidate source resolver: verified remote ref
when one exists, otherwise the retained local adoption ref at the exact SHA.
A missing remote ref, deleted original worktree, stale `agent/<bead>` branch,
or dead original worker is handled by that source-of-truth order and never by
worker affinity.

Worker release has a crash-consistent durability boundary before the first
network publication. The dispatcher atomically creates and verifies a local
backup ref in the shared repository, for example
`refs/oro/adopted/<project>/<bead>/<assignment>`, pointing at the exact worker
SHA. It verifies the commit is reachable from that ref, then persists the ref
name/SHA and adopted candidate row before acknowledging `CANDIDATE_READY`.
Only after that acknowledgement may the worker worktree/branch be reused.
`git update-ref` and the SQLite transaction cannot be one atomic transaction,
so ordering and startup reconciliation are explicit: a crash after ref creation
but before the row leaves an identifiable orphan ref that recovery adopts or
conservatively retains; a crash after the row but before ACK makes the duplicate
message return the same ACK. The local adoption ref is retained until the
rebased candidate is verified under a durable remote ref or the work is
terminally preserved elsewhere. Garbage collection and cleanup must never
remove an adoption ref referenced by a nonterminal row.

#### Integration versus cancellation linearization

External close, deduplication, preemption, requeue, rollback, and shutdown
cancellation share one durable transition with remote integration. Immediately
before provider mutation, the dispatcher starts a SQLite write transaction that
reads the current bead status/generation and prepared-attempt version. If the
bead is already closed/cancelled/requeued or the attempt changed, integration
intent does not commit and the remote target cannot be mutated. Otherwise the
same transaction commits an irrevocable `integration_intent` tied to the exact
prepared squash and task generation.

Every dispatcher cancellation producer, including
`checkClosedBeadAssignments`, `handleClosedAssignment`, preemption, requeue,
rollback, and remote-record scanning, uses the same rule. Cancellation that
commits first prevents preparation/CAS. Once intent commits, later cancellation
is recorded as `cancel_pending`; it cannot mark evidence obsolete, delete refs,
or invoke a legacy recovery merge while the provider outcome is unknown. If
CAS or ancestry reconciliation proves integration, the close request has lost
the cancellation race and normal idempotent reconciliation completes the bead.
If CAS is proven not to have mutated the target, pending cancellation wins and
cleanup preserves all unique work. Ambiguous outcomes remain under recovery
until one of those facts is proven.

The external-close scanner examines nonterminal remote records independently of
worker assignments. `handleClosedAssignment` delegates to this state machine
and is forbidden from invoking its legacy branch recovery/merge path for any
remote-owned record. Because task status and integration intent live in the
same project SQLite database, `BEGIN IMMEDIATE` serializes a direct task close
with intent creation. A task close committed after intent is visible as pending
but cannot retroactively authorize cancellation of an already-issued CAS.

On `CANDIDATE_READY`, the dispatcher:

1. verifies the assignment, branch, clean worktree, and committed head;
2. creates/verifies the dispatcher-owned local adoption ref, persists its
   lease and candidate row, acknowledges adoption, and only then releases the
   worker;
3. serializes target-changing Git operations using the existing merge/branch
   coordination boundary;
4. fetches the configured remote and rebases the candidate onto the exact
   local target;
5. runs ops review against the exact post-rebase tree and persists its verdict;
6. ensures a non-main epic target exists on the remote before creating a PR;
7. publishes the candidate with `--force-with-lease=<remote-ref>:<observed-sha>`
   when a prior dispatcher-owned ref exists, never with an unconditional force;
8. creates or updates a draft PR whose base is the actual target branch;
9. persists PR identity before waiting for checks;
10. releases local Git coordination while CI runs;
11. observes exact check-run data through the adaptive pull scheduler;
12. validates and persists passing or failing evidence;
13. after pass, idempotently marks the draft change ready for review and
    observes that non-draft state;
14. revalidates ops and CI evidence and requests the GitHub-hosted squash CAS
    with the exact candidate head and target SHA;
15. observes the merged SHA, verifies the merged tree, synchronizes the local
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
    EnsureEphemeralTarget(ctx context.Context, req EnsureEphemeralTargetRequest) (EphemeralTarget, error)
    DeleteEphemeralTarget(ctx context.Context, req DeleteEphemeralTargetRequest) error
    Publish(ctx context.Context, req PublishRequest) (PublishedCandidate, error)
    EnsureChange(ctx context.Context, req EnsureChangeRequest) (RemoteChange, error)
    Observe(ctx context.Context, req ObserveGateRequest) (RemoteGateObservation, error)
    SetChangeReady(ctx context.Context, req ChangeReadyRequest) (RemoteChange, error)
    PrepareSquash(ctx context.Context, req PrepareSquashRequest) (PreparedSquash, error)
    IntegrateSquashCAS(ctx context.Context, prepared PreparedSquash) (MergeResult, error)
    Cancel(ctx context.Context, req CancelGateRequest) error
    Reconcile(ctx context.Context, req ReconcileChangeRequest) error
}
```

The GitHub package preserves its existing read-only startup surface as
`Client` and `NewClient(APIReader, string, CollectionReader,
CollectionLimits)`. The mutable remote-change adapter uses the distinct
`ChangeClient` and `NewChangeClient` names so adding lifecycle support does not
break current preflight callers.

`EnsureEphemeralTarget` is the provider-neutral lifecycle for PR bases such as
an Oro epic branch. Its request includes project/epic identity, exact ref name,
persisted seed SHA, target generation, ownership marker, and expected-absent or
exact-observed lease. GitHub creates `epic/<id>` at that SHA; an identical ref is
idempotently adopted after a concurrent creator, lost response, or restart. A
mismatched unowned/external ref is quarantined and never overwritten. Once the
target exists, normal `IntegrateSquashCAS` advances it as children complete.
`DeleteEphemeralTarget` accepts only a durably retired target plus exact final
SHA/generation and uses a leased Git deletion; it never deletes a moving or
unowned ref.

`PrepareSquashRequest` contains repository identity, remote change ID, expected
candidate head SHA, exact target ref/SHA, exact evidence ID, reviewed/tested
tree, and deterministic commit message/metadata. `PrepareSquash` is forbidden
from mutating a provider target. It creates one
squash commit with parent equal to the expected target SHA and tree equal to
the reviewed/tested tree, stores it under a dispatcher-owned local integration
ref, and returns exactly one `PreparedSquash` containing proposed SHA, parent,
tree, local ref, candidate/evidence/PR identity, target ref, and attempt key.
The dispatcher verifies that object/ref, commits the integration-attempt row,
and advances durable state to `integration_prepared`. Only after that
transaction acknowledges success may it call
`IntegrateSquashCAS(prepared)`. That operation re-verifies the prepared object
and local ref, then asks GitHub to advance the target ref using an
exact expected-old-SHA lease. Git's receive transaction accepts the update only
if the current target equals that tested base; because the new commit's sole
parent is also the expected target, the result is one fast-forward squash
commit. Any concurrent target movement rejects the update atomically.
The proposed commit is deterministic and its local integration ref survives
restart. On success, timeout, disconnect, or restart, reconciliation proves
the proposed commit has its persisted parent/tree and is on the target's
current first-parent history. It therefore recognizes success even if another
valid integration already advanced `proposed -> newer-tip`. Tree equality is
checked at the proposed commit, while local synchronization advances to the
current descendant tip. If the proposed commit is absent from current target
history, the dispatcher distinguishes unchanged expected base (safe retry),
different target (invalidate/rebase), and rewritten/deleted ambiguity
(quarantine). It never infers success from current-tip tree equality and never
recreates a second squash commit after an ambiguous response.

This two-phase boundary has one commit-identity producer. The dispatcher never
recomputes the squash SHA, and the adapter never writes Oro's database. Crash
injection covers before/after local preparation, before/after attempt-row
commit, immediately before remote mutation, and immediately after accepted
mutation but before the adapter returns. An orphan prepared ref is retained and
reconciled; remote mutation is illegal without a matching committed prepared
row and version.

`Capabilities` also contains the canonical effective-policy hash and an
enumerated result for every applicable repository/organization rule. GitHub v1
explicitly does not implement merge queue or autonomous satisfaction of human
review, conversation, or deployment gates; preflight fails before candidate
publication when any is effective. Pre-authorization must reproduce the same
compatible policy hash. The fake models every supported rejection category,
not only the desired required-check rule.

GitHub's ref-update primitive has no atomic ruleset-version precondition: its
request carries the new SHA and fast-forward/lease semantics, while the server
enforces whatever policy is current when it processes that update. Therefore
Oro guarantees atomic target-SHA equality and current server-side enforcement,
but does not claim that a policy read and ref mutation form one transaction.
Immediately after every successful or ambiguously reconciled CAS, the adapter
re-reads the complete effective policy. If its canonical hash differs from the
authorized hash, the candidate enters durable `integrated_policy_drift` with
before/after policy and rule-suite evidence; Oro freezes further target
integrations and does not falsely report that the target was unchanged. The
already-integrated commit is never destructively reverted. Automatic policy
reconciliation uses only the separately configured setup path, after which the
record is reconciled idempotently and integration resumes; unavailable setup
authority remains a surfaced P0 auth/config defect.

The deterministic fake has a barrier after the last policy read with an
unchanged target. Policy-only removal or a mutation still bypassed by the App
may allow the ref CAS and must produce `integrated_policy_drift`; a newly
restrictive rule enforced against the App must make GitHub reject the CAS.
Policy drift before the last read must prevent mutation. These cases explicitly
test the provider limitation rather than masking it with simultaneous target
movement.

Automatic recovery is production-wired through a second provider-neutral
boundary, not an injected test callback:

```go
type PolicyReconciler interface {
    PreflightMaintenance(ctx context.Context, req MaintenancePreflightRequest) (MaintenanceCapabilities, error)
    ReconcileOwnedPolicy(ctx context.Context, req ReconcileOwnedPolicyRequest) (PolicyReconcileResult, error)
}
```

The GitHub implementation is constructed from the typed maintenance identity
by setup and the managed monitor, never by a worker and never from the runtime
credential. The desired canonical template, logical ownership key encoded in
the exact ruleset name, repository identity, last observed provider ID/version/
hash, drift evidence, and reconciliation idempotency key are mandatory request
fields. It refuses organization rules, foreign or unmarked repository rulesets,
repository mismatch, ownership-name collision, unexpected creation actor, or a
desired template not matching project configuration. The adapter mints a short-
lived exact-scope token just in time, reconciles the Oro-owned ruleset, re-reads
complete effective policy, and returns before/after provider-ID/rule/version/
hash evidence. Secrets are excluded from argv, environment inheritance,
descriptors, database rows, and logs.

The durable policy-binding registry is authoritative for mutable provider
identity: logical key, active provider ID, binding generation, desired template
hash, create-attempt ID, observed IDs/versions, and state (`bound`, `missing`,
`create_ambiguous`, `deduplicating`). A 404 on the active ID first lists all
repository-source rulesets through the complete-collection primitive and
discovers exact ownership-name matches. One
matching ruleset is adopted only when its full template and ruleset-history
creation actor match the configured maintenance App. Zero matches persists a
create attempt before POSTing the full marked template. A response records the
new ID; a lost response enters `create_ambiguous` and reconciliation lists by
the same marker before any new attempt.

Because GitHub creation has no idempotency-key field, an ambiguous retry may
produce duplicate identical marked rulesets. Integration remains frozen while
the reconciler validates every match, deterministically retains the oldest
maintenance-App-created exact-template instance, lease-records each duplicate,
and deletes only the other identical marked instances. Any foreign creator or
template mismatch is quarantined, never deleted. Once exactly one instance
remains, one database transaction increments binding generation and replaces
the active provider ID. Static config and the supervisor descriptor need no
rewrite because they contain only the logical key/name/template; monitor and
dispatcher always resolve the current binding generation from the shared
ledger. Crashes or lost responses around create, discovery, duplicate deletion,
binding commit, and verification converge without duplicate active policy.

`integrated_policy_drift` atomically creates a durable reconciliation request,
attempt-owned blocker, and joins/creates the integration-barrier generation. The
external monitor claims the request with a renewable lease,
constructs `PolicyReconciler` from the descriptor, retries transient credential/
API failures with backoff, and commits the returned evidence. Dispatcher
`startupRecovery`, `restoreState`, and `spawnBackgroundLoops` reconcile requests
and resolve a blocker only when a fresh runtime-identity policy read exactly
matches the configured template and that integrated attempt is already
accounted for. Barrier closure uses the last-owner predicate below.
Crashes before/after claim, token mint, provider update/create/adopt/rebind,
verification, row commit, monitor restart, and dispatcher restart are
idempotent. Drift caused by
foreign or organization policy is never overwritten; it remains frozen and a
P0 auth/config defect, but it does not silently require a human interaction
inside a routine bead.

`SetChangeReady` is a distinct persisted, idempotent transition. The GitHub
adapter marks the draft PR ready, then observes `isDraft=false` before merge
authorization. A timeout after the provider accepted the transition is
reconciled by observation instead of repeating blindly. Restart and rollback
preserve this state, and the deterministic adapter fake rejects every attempt
to merge a draft PR.

The production implementation uses the installation-managed `gh` CLI for every
PR, workflow, check, artifact, cancellation, ruleset, and installation-token API
operation, and the pinned `git` transport for Git object/ref operations. Setup
resolves the absolute Homebrew/package-managed `gh`, verifies a supported
version/provenance, and persists path, file identity, hash, and capability
evidence; startup and immediately pre-spawn re-attest it. No `exec.LookPath`,
shell, alias, or ambient PATH lookup occurs after setup.

Every `gh` call uses argument arrays, JSON output, bounded stdout/stderr, a
context deadline, and request bodies over stdin. The token never appears in argv,
stdin payload, config files, logs, events, or durable rows. The child environment
starts from a minimal allowlist: fixed executable/helper paths; Oro-generated
`GH_TOKEN`, `GH_HOST`, `GH_CONFIG_DIR`, prompt/update/color controls; explicit
typed proxy/CA policy when configured; and locale/temp. It strips ambient
`GH_*`, `GITHUB_*`, `GIT_*`, credential, executable-search, dynamic-loader,
proxy, CA, shell-startup, and config injection. `GH_CONFIG_DIR` is an Oro-owned
empty/nonpersistent directory and cannot provide another identity. Setup and
doctor validate host/API/TLS policy through this exact runner. No shell command
is constructed from PR titles, branch names, URLs, or remote output.

Correctness-critical list operations use one typed complete-collection
primitive in the shared runner, never an endpoint caller's one-page shortcut.
For PRs, checks, workflow runs/jobs/artifacts, effective rules, repository
rulesets, and ruleset history, it invokes `gh api --paginate --slurp` (or the
host-version-equivalent Link traversal), validates every page before exposing
any item, normalizes the page arrays into one typed collection, and rejects
malformed pagination, cycles, host-changing links, repeated page tokens,
duplicate stable IDs, inconsistent repository/run identity, or a partial-page
failure. Endpoint-specific maximum page/item/byte limits are setup-attested;
hitting a limit without a proven terminal page fails closed as incomplete
rather than returning a prefix. Callers receive either a complete normalized
collection plus page/count evidence or no collection at all. Restart repeats
the idempotent read from page one; durable state never records a prefix as
complete.

Dispatcher-owned network ref mutation uses a dedicated internal Git transport,
not the repository's ordinary push path. Its unexported constructor requires
the runtime credential provider plus a durable operation/lease object for one
of candidate, ephemeral-epic, audit, or target-CAS ref operations. It invokes a
trusted absolute Git executable with the canonical HTTPS URL, controlled
environment/config, `-c core.hooksPath=/dev/null`, and `push --no-verify`; it
does not reuse `processenv.ForWorkdir` unchanged. Setup resolves the absolute Git
binary, `git --exec-path`, and real `git-remote-https` helper chain in a sanitized
environment, canonicalizes them, records binary/helper identities and hashes in
capability evidence, and startup re-attests them. Invocation pins that trusted
exec path and HTTPS-only protocol policy.

The subprocess environment is built from an empty/minimal allowlist. It removes
every ambient `GIT_*`, `GH_*`, dynamic-loader, executable-search, proxy, CA-
override, credential, SSH, object-directory/alternates, template, namespace,
and config-injection variable, then adds only Oro-generated values such as
`GIT_TERMINAL_PROMPT=0`, the one-shot credential provider, null system/global
config, explicit workdir, trusted exec path, locale/temp, and configured network
policy. Internally supplied config-count entries are constructed from typed
constants, never inherited. The helper executable directory and PATH are fixed
to setup-attested locations; neither `GIT_EXEC_PATH` nor a substituted
`git-remote-https` can come from the parent process. The operation still uses
exact expected-old/absent leases and provider policy checks. No worker, general
command runner, public CLI flag, or human push path can obtain this transport
capability.

This bypass is intentionally local and per subprocess. Oro does not delete,
rewrite, disable, or globally reconfigure `.git/hooks/pre-push`, its user-hook
chain, or `core.hooksPath`. Ordinary `git push` continues through the installed
`buildOroPrePushCheck` wrapper, rejects human `agent/*`/`epic/*` publication, and
runs the configured local guard. Internal pushes never execute that wrapper or
arbitrary user hooks, so they neither reject dispatcher-owned `epic/**` refs nor
rerun the full local QG. Command/audit evidence records `hooks_disabled=true`
and the internal operation ID without logging credentials.

Provider construction receives one `RuntimeCredentialProvider`. Before every
network side effect it resolves/refreshes a credential and verifies the same
expected App ID, installation ID, host, and repository stored in
`Capabilities`. The attested `gh` runner receives the scoped token only as its
Oro-generated `GH_TOKEN` and verifies the installation actor through the GitHub API. Git
network subprocesses use a canonical HTTPS repository URL plus a private
`GIT_ASKPASS`/credential-FD bridge backed by that same installation token;
tokens never appear in argv, config files, logs, events, or persisted rows.
The adapter clears ambient Git credential helpers, SSH commands/agents, and
interactive prompting for provider network operations. An SSH `origin` may be
used as local repository metadata, but publishing, fetching provider evidence,
candidate-ref mutation, and target CAS use only the verified App-owned HTTPS
transport.

The adapter re-attests actor identity immediately before target CAS. Token
expiry during a long CI wait triggers provider refresh and one idempotent
retry; an actor/installation/repository mismatch fails closed before mutation.
Production-construction tests use the real setup-attested `gh` executable
against a production-faithful API fixture plus a Git receive transport to expose
split actors, ambient administrator credentials, wrong host/repo, proxy/CA
abuse, executable replacement, credential-helper/SSH leakage, expiry/refresh,
body limits, and redaction.

The credential provider and canonical authenticated Git transport live in a
shared production package used by both the dispatcher GitHub adapter and the
managed lifecycle runner. The supervisor reconstructs that provider from the
descriptor's hash-bound nonsecret reference and expected actor scope after the
dispatcher is gone. It cannot substitute an anonymous, SSH, ambient helper, or
developer credential path.

The dispatcher owns policy, persistence, retry classification, and state
transitions. The client owns only provider/git side effects and normalized
observations. Compile-time boundary tests prevent dispatcher policy packages
from importing GitHub/`gh` transport representations or executing `gh`
directly; only the GitHub adapter's shared attested runner may do so.

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

After exact validation, the dispatcher re-verifies compatible policy, calls
`PrepareSquash`, persists and acknowledges the returned prepared object, then
commits the exact integration intent against current bead/attempt versions,
then calls `IntegrateSquashCAS` with that exact object. The adapter independently
re-verifies the PR head, prepared commit/ref, and performs the exact-SHA leased
target-ref update. This Git ref transaction—not the mutable policy reread—is
the atomic expected-base guard. A rejected ref update, changed policy,
or changed head/base observed before the mutation means the bead is not closed
and re-enters preflight or the rebase/review/gate loop. Deterministic fake
barriers between final policy read and ref mutation separately inject target
movement, policy-only removal/mutation, and both together. Target movement must
reject before mutation; policy-only outcomes follow the explicit provider-
limitation contract rather than borrowing safety from the target lease.

Deterministic barriers also inject close/preempt/requeue immediately before and
after integration intent. Before-intent cancellation prevents provider
mutation. After-intent cancellation becomes pending while the already-issued
CAS is reconciled; it never starts a second completion path.

After GitHub reports the integrated SHA, the dispatcher fetches the remote and
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

Before local sync, closure, cleanup, or epic installation, the dispatcher also
performs the post-CAS effective-policy read. A hash match permits normal
completion. Drift commits the exact `integrated_policy_drift` state and project-
wide integration freeze first; subsequent reconciliation knows the target
already contains the tested squash and must never republish or integrate it.

The integration freeze is gate-mode independent. Assignment eligibility and
every target-mutating entry point—including `handleDone`,
`completeManualIntegration`, `mergeAndComplete`, `ffMergeEpicBranch`, remote
integration intent/CAS, and epic promotion—must read the same durable barrier.
Workers may finish and have results durably adopted, but no local or remote
merge/FF/CAS occurs while it is set. A configuration rollback cannot clear or
bypass it.

The freeze is not one boolean. A monotonic `integration_barrier` generation owns
durable participant rows keyed by project, integration attempt, target ref, and
maintenance owner. Every issued CAS already has an attempt row before provider
mutation and remains in `integration_intent|cas_issued|post_cas_verification`
until its issuance/outcome, policy result, and local synchronization are durable.
The first drift transaction
increments the barrier generation, creates its drift blocker, and enrolls every
other intent/issued/post-CAS attempt as a participant before eligibility observes the
barrier. Later drift records join the same active generation; new integration
attempts are forbidden.
An enrolled `integration_intent` proven not yet issued is durably cancelled and
never sent; issued/ambiguous attempts must be observed to exact outcome.

Policy-reconciliation requests are deduplicated by desired policy identity but
do not collapse per-attempt synchronization: one repair may satisfy several
blockers, while each target/attempt separately proves its integrated result and
authoritative local descendant. Recovery may resolve participants in any order.
A resolution transaction marks only its owner complete, then performs a
generation-checked `NOT EXISTS` query over unresolved blockers and enrolled
post-CAS participants. Only the last-owner transaction may close the barrier and
restore eligibility. Lost responses, ambiguous CAS, restart, different main/
custom/epic targets, and duplicate recovery never decrement a counter or clear
another owner's blocker.

If mode becomes `local` while drift or deferred `local_sync` exists, startup is
recovery-only: status/health and the external supervisor run, but the assignment
loop and all integration paths remain ineligible. The previous runtime and
maintenance credential-provider references, supervisor descriptor, remote
identity, integration attempt, and expected target are retained until recovery;
configuration that removes them fails closed. After the supervisor repairs the
owned policy, the dispatcher re-verifies it with the runtime identity, fetches
the authoritative remote target, proves the persisted squash is on its current
first-parent history with the expected tree, and fast-forwards the local target
to that exact current descendant. Dirty/divergent local state is preserved and
quarantined, never reset. One transaction records that owner's remote/local
synchronization and closes its remote ownership record exactly once. Local
assignments or legacy integration start only when the atomic last-owner
predicate closes the barrier.

“GitHub reports” includes ancestry reconciliation of an ambiguous attempt. The
dispatcher evaluates the persisted proposed squash commit, not only the current
target tip. If the current tip is a first-parent descendant, the bead integrated
exactly once; the dispatcher verifies tree equality at the proposed commit,
records its integration, syncs to the newer descendant, and idempotently
reconciles the PR. A durable reconciliation marker prevents duplicate PR close,
comment, bead close, or cleanup events after repeated restart.

### 8. Failure Classification and Worker Recovery

Remote observations are classified before action:

| Class | Examples | Dispatcher action |
|---|---|---|
| deterministic | test, lint, build, coverage, or aggregate dependency failure | Fetch bounded failed-step evidence, persist a correction checkpoint on the original bead, and enqueue a fresh correction assignment for any available worker |
| superseded | candidate or base changed; run cancelled by newer push | Ignore old run and await the replacement |
| transient | GitHub 5xx, network timeout, runner startup failure, API transport error | Retry with jittered exponential backoff within the run timeout |
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
Candidate materialization uses one durable resolver: before verified remote
publication it selects the local adoption ref; afterward it prefers the exact
verified remote ref and retains the local ref as fallback until terminal
reconciliation. The correction worker gets a fresh worktree at that resolved
SHA and receives the durable checkpoint before coding. Local-presubmit or ops
rejection before first publication is a required correction fixture.
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
GitHub target-ref CAS while the remote API/transport is unavailable.
After GitHub recovers, the dispatcher revalidates target/review/local evidence,
authorizes the atomic squash CAS, and returns new candidates automatically to
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

After a successful GitHub-hosted squash CAS, reconciliation proves the target
tree equals the reviewed and tested candidate tree. It then:

1. records GitHub's integrated SHA and synchronizes the local target by
   fast-forward only;
2. verifies the exact target ref/commit, then closes/reconciles the CI PR with
   durable factory-integrated evidence (the PR merge endpoint is not the target
   mutation primitive);
3. deletes the remote candidate ref with a lease;
4. archives or safely deletes the non-ancestor worker branch only after
   tree-equivalence and durable PR evidence prove no unique work remains;
5. after the idempotent bead/PR reconciliation marker commits, deletes the
   local adoption and proposed-integration refs with exact leases;
6. runs local worktree cleanup and emits `remote_gate_reconciled`.

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
- restore every prepared integration attempt and test its persisted proposed
  squash commit against the current target's first-parent history before any
  target mutation retry;
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
The local epic creation transaction persists its exact seed SHA, ref, ownership
marker, and generation in an `epic_target` row. Before the first child candidate
can publish or create a PR, the dispatcher calls `EnsureEphemeralTarget` and
durably records the returned provider identity/SHA. Two concurrent first
children share that row: one expected-absent create wins and the other adopts
the exact ref. Publication remains ineligible until creation/adoption is
committed. A lost response, dispatcher death, or externally deleted active ref
reconciles by observation; an active owned target may be recreated only at its
exact last durably integrated SHA, while a retired target can never resurrect.

When all children and acceptance criteria are closed:

1. create/update an epic promotion PR from the epic branch to its actual target;
2. run the aggregate portable gate against the combined epic merge commit;
3. persist exact epic evidence;
4. run final epic review/acceptance;
5. authorize the GitHub-hosted squash CAS through the normal exact-state path;
6. reconcile the promotion PR and remote epic ref;
7. perform the existing local `make build install`, installed/repo binary hash
   match, controlled Oro restart, and state-dependent replacement-readiness
   verification.

The dispatcher owns steps 1–6, then writes a versioned post-epic operation and
signals the external lifecycle supervisor. The dispatcher never attempts to
restart itself. A managed `oro monitor --act` child owns step 7 across the
termination boundary under the stable supervisor shim. It durably claims the
operation with a renewable lease, then idempotently:

After step 6, the dispatcher transactionally marks the ephemeral epic target
retired and records its exact final SHA before requesting leased deletion.
Deletion happens only after the promotion integration and PR reconciliation are
durable. Absent-after-timeout is reconciled as success; still-present-at-exact-
SHA is retryable; a changed ref is quarantined. Policy or permission denial is
durable `epic_target_cleanup_pending`, exposed to self-healing and retried
without resurrecting the branch. Final epic completion requires cleanup
reconciliation as well as the post-install acknowledgement, though build/
restart work may proceed while cleanup retries.

1. verifies the exact synced target SHA and clean lifecycle worktree;
2. runs or resumes `make build install` and records repository/installed binary
   hashes before restart;
3. requests graceful shutdown, records the old PID, and waits for its exit;
4. invokes `cmd/oro/cmd_start.go:startFreshSwarm` through the newly installed
   binary with the persisted project worker limits/configuration;
5. proves the replacement PID differs, its executable hash is expected, its
   reported build hash matches the repository, and dispatcher health plus the
   typed state-dependent readiness proof below succeed;
6. atomically acknowledges the lifecycle operation for dispatcher startup
   recovery to close the epic.

The operation ledger stores stage, schema version, claim owner/lease, attempts,
target/install hashes, old/new PIDs, start configuration, health evidence, and
acknowledgement. Monitor restart resumes from the last proven stage: a crash
after install does not rebuild unnecessarily, and a crash after old-daemon exit
retries replacement start. The monitor action ledger and post-epic ledger use
idempotency keys so two monitor instances cannot start two swarms.

Lifecycle execution is serialized by one renewable project-global lease and a
monotonic lifecycle generation, not by independent per-operation leases. Each
post-epic operation records its configured install target, required target SHA,
generation, and artifact identity. On every claim/resume, the monitor loads all
pending operations for the project, fetches the authoritative configured target
under the normal repository coordination boundary, and orders requirements by
Git ancestry. It selects the newest required/current descendant as the desired
install SHA; row creation or claim timing cannot select an ancestor first.

A healthy installed descendant satisfies every pending operation whose required
SHA is its ancestor. Those operations are acknowledged individually with
durable `satisfied_by_sha`, ancestry proof, installed/build hash, PID, and health
evidence. An ancestor operation arriving after a descendant is already healthy
is coalesced without rebuilding or restarting. Divergent, rewritten, different-
artifact, or different-install-target requirements cannot be coalesced and fail
closed into lifecycle recovery rather than choosing arbitrarily.

The monitor re-fetches and revalidates desired generation/SHA immediately
before installation, before stopping the daemon, and before starting the
replacement. Target advancement during build supersedes the build and restarts
from the newest descendant before shutdown. Because only the live dispatcher
performs target integration and the monitor holds the project lifecycle barrier
through shutdown, no later integration can appear after the old dispatcher
exits and before replacement start. The installed/running SHA is monotonic by
ancestry: the coordinator never installs or starts an ancestor of the latest
proven healthy build. These rules prevent downgrade and restart storms while
allowing one newest build/restart to close several epics.

Normal epic installation is side-by-side, not in-place. The build produces a
versioned Oro executable path `B1` and retains the current `B0`. Before daemon
shutdown, old monitor `M0` invokes `B1` in non-mutating supervisor compatibility
mode to validate the stable descriptor envelope, lifecycle-ledger schema,
credential-provider access, and supported operation range. The shim descriptor
envelope and lifecycle journal use a stable append-only protocol that automatic
epic installation never incompatibly migrates. `B1` must read/write the active
protocol while `B0` remains valid. An incompatible or irreversible supervisor-
protocol change fails preflight and files a prioritized compatibility repair
while `M0` and old dispatcher `D0` continue running; such a change must ship
through a backward-compatible bridge release or an explicit setup-maintenance
flow, never this automatic handoff.

After successful preflight, `M0` persists `supervisor_upgrade_pending` with a
new fencing generation/token, expected `B1` hash/build/schema ranges, staged
compatibility proof, and rollback `B0` identity. Every monitor
side effect checks the current fencing generation. `M0` relinquishes its lease
and exits; it cannot resume mutation after the generation advances. The stable
shim atomically selects `B1`, starts distinct child `M1`, and requires a
heartbeat bound to M1 PID, executable-image hash, build SHA, descriptor schema,
ledger/operation-schema ranges, and fencing generation. `M1` performs the
preflighted compatibility probe, claims the unchanged stable ledger, and
acknowledges handoff.
Only then may `M1` stop `D0` and start/verify `D1`.

The shim is deliberately outside normal `make build install` and uses a small,
versioned, backward-compatible bootstrap protocol. It remains alive while
monitor children change. If `M1` exits or cannot produce the exact heartbeat
before timeout, the shim restores the `B0` selection, starts a fenced `M0`
replacement, proves B0 can read/write/claim the unchanged stable ledger,
records rollback evidence, and leaves `D0` running. Crashes before or after
lease relinquishment, child start, compatibility probe, heartbeat, and handoff
acknowledgement are idempotently recoverable; exactly one fencing generation
may stop/start a dispatcher.

Project state-database migration is a later, separate boundary. After M1 is
healthy, it gracefully quiesces and stops D0, creates and integrity-checks an
exact B0-readable SQLite backup plus external bootstrap-journal marker, then
runs B1 migration with no old readers/writers. Automatic install permits only
transactional/reversible migrations. If migration, D1 start, or D1 health fails,
M1 stops D1, restores the verified B0 preimage byte/logically exactly, starts
D0/B0, proves B0 database read/write and health, and records rollback. The
stable shim journal distinguishes backup, migration-started, activated, and
rollback-restored even when the project DB is unreadable. Irreversible
migrations fail preflight before D0 shutdown; no live D0 observes a B1 schema.

Restart-relevant operational control is durable, not inferred from descriptor
defaults. A versioned `runtime_control` row stores desired run state
(`paused|running`), focused epic/clear state, target worker count, max workers,
explicit runtime overrides, and a monotonic control generation. Every
`applyPause`, `applyResume`, `applyFocus`, `applyScaleDirective`, and
`applyMaxWorkersDirective` transactionally persists the new generation before
acknowledging the directive; status/events expose that generation.

Before D0 shutdown, M1 requests a durable restart freeze. D0 serializes it with
directive writes, records the exact frozen control generation, quiesces new
assignments, and thereafter rejects new control directives with a retryable
`restart-in-progress` result rather than acknowledging state that cannot join
the handoff. Thus a directive either commits before the frozen snapshot or is
explicitly not accepted. M1 revalidates target, project configuration, and the
frozen control generation before stop and before D1 start.

D1 starts in managed inert mode: socket/health may report bootstrapping, but
worker spawning and assignment loops remain ineligible. It loads and validates
the B1 project configuration, reapplies the exact durable runtime overrides,
restores pause/run, focus, target/max worker counts, and acknowledges the frozen
generation. Only then does it enter Running when the snapshot says Running; a
paused snapshot remains healthy and paused with zero new assignments. B1
configuration incompatibility with a persisted override fails preflight before
D0 shutdown rather than silently dropping or reviving a value.

Lifecycle acknowledgement never depends on ordinary queue work existing or
being dispatchable. After D1 acknowledges the frozen control generation, it
emits a typed, durable replacement-readiness proof containing project/build/
PID/restart generation, control generation, worker-generation fence result,
socket/database/background-loop health, queue snapshot generation, and one
planner-cycle result. The planner cycle is read-only: it may prove assignment
eligibility, but cannot lease, mutate, or send a production assignment. Its
state reason is exactly one of `paused`, `zero_capacity`, `no_eligible_work`,
`focus_filtered`, or `assignment_eligible`. A paused proof requires the paused
control generation and zero post-start `ASSIGN` messages; capacity/focus/empty
proofs require the matching durable controls and queue snapshot. When configured
capacity is positive, any expected managed workers must complete the B1
generation/build/protocol HELLO before readiness; zero configured capacity is a
valid healthy state. The supervisor accepts only this exact-generation proof,
never unpauses, scales, clears focus, injects a bead, or waits for a production
assignment to close an epic.

Rollback clears the freeze only after D0/B0 or a replacement B0 dispatcher has
loaded and acknowledged the same latest control generation. No rollback path
unpauses, clears focus, or restores descriptor-time capacity. Deterministic
barriers issue pause/resume, focus/clear, scale, and max-worker directives during
build, immediately before freeze, after freeze, and before D1 eligibility; only
acknowledged directives appear in the restored generation.

Workers are fenced across the same restart. The durable restart snapshot
allocates a new worker generation and records every known managed worker PID,
process group, process-start identity, executable-image/build hash, socket,
managed/external ownership, and prior generation. D0's graceful shutdown
returns explicit termination results rather than ignoring kill errors. M1
rescans project-owned process groups independently of D0's connected-worker map,
escalates TERM/KILL for every old managed generation, and keeps D1 assignment
eligibility closed until zero owned B0 worker processes remain or the lifecycle
operation is durably failed/quarantined.

Worker protocol begins with mandatory versioned `HELLO`/`HELLO_ACK` before
registration. HELLO contains immutable project identity, worker/restart
generation, executable-image hash, build SHA, supported protocol range,
managed/external type, and process identity. D1 accepts only B1 workers whose
project, generation, image/build, and negotiated protocol match the active
restart snapshot. A previous-session timestamp is not admission. Stale or
incompatible managed/external workers receive a shutdown/rejection and never
enter the idle pool; external workers that cannot be killed remain harmless
because reconnect cannot pass HELLO.

Assignment selection, target/max capacity, health, status, and dashboard counts
include only current-generation attested workers. Health reports expected versus
attested B1 workers plus stale-generation connections/residual process evidence.
The negotiated protocol versions assignment, shutdown, heartbeat,
`CANDIDATE_READY`, review, and completion messages; unsupported semantics fail
before any work is assigned. Rollback allocates another generation for B0 so
partially spawned B1 workers cannot join the restored factory.

The supervisor shim is supervised by the OS user service manager. Setup installs
and verifies that service; dispatcher startup requires a compatible monitor-child
heartbeat and supported operation-schema version before enabling automatic
epic installation. This ensures some process remains able to consume
`restart_pending` after the dispatcher exits. Failure keeps the epic operation
visible and retryable without undoing a proven remote integration.

The generated service invokes
`oro-supervisor-shim --managed-project-descriptor <absolute-path>` with an
absolute stable shim executable and an explicit canonical working directory.
The shim selects a versioned Oro executable and launches
`oro monitor --act --managed-project-descriptor <absolute-path>`; correctness
comes from the validated descriptor rather than the working directory. The
service starts from a clean, allowlisted environment. Shim and monitor startup verify
descriptor hash, canonical root, project ownership/identity, state paths, and
service instance identity before heartbeat or mutation; a moved repository or
changed identity fails closed until `oro setup` atomically unloads the old
instance and installs a new descriptor/service.

Service identities are stable and collision-resistant per canonical project
identity: for example `dev.getoro.oro.monitor.<project-hash>` and a matching
project-specific plist path on launchd, or an escaped/hash-addressed
`oro-monitor@<project-hash>.service` user unit on systemd. Setup, repair, and
uninstall act only on the exact descriptor hash/instance and cannot replace or
remove another project's supervisor. Verification launches the installed unit
from an unrelated working directory with ambient project variables removed,
then requires its heartbeat and lifecycle claim to appear only in the intended
project database.

Every supervisor fetch/re-fetch of the authoritative target uses the shared
credential-provider-backed canonical HTTPS transport. Before pre-install,
pre-shutdown, and pre-start network reads, it refreshes with safety skew and
attests the expected App, installation, host, and repository. Temporarily
locked/unavailable secret storage is a durable transient lifecycle failure;
invalid actor/scope is auth/config failure. Tokens are excluded from argv,
descriptor, service definition, stdout/stderr, monitor logs, events, and
lifecycle rows. An authenticated private-remote fixture rejects anonymous,
ambient, SSH/helper, expired, wrong-installation, and wrong-repository access
and expires credentials between lifecycle stages.

The durable epic states distinguish `promotion_gate`, `remote_merged`,
`local_install_pending`, `restart_pending`, `health_verification`, and
`complete`. `tryCloseEpic`, `completeEpicClose`, and `ffMergeEpicBranch` must
dispatch by gate mode; the legacy local-QG/FF path cannot close an epic in
`github-pr` mode. The epic closes only after installed/repository binary hashes
match, the external supervisor acknowledgement commits, and the replacement's
typed state-dependent readiness proof is verified.

### 13. Remote Auditor Mutation Campaign

Every periodic whole-repository auditor cycle triggers exactly one distinct
GitHub Actions workflow against the audit's exact current target SHA. The audit
cycle is not complete until that mutation workflow reaches a valid terminal
result and its artifacts are durably incorporated. This is separate from both the
per-PR portable gate and `quality_gate.sh --mutation-testing` because the
existing flag is incremental: Go targets changed files/touched functions, has
an eight-minute cap, and treats timeout as a warning.

Before workflow dispatch, a low-compute, read-only inventory builder walks the
exact audited Git tree and applies the versioned mutation inclusion/exclusion
policy. It emits a canonical sorted inventory of eligible Go and Python
mutation units, plus target SHA, policy/config hash, tool/version requirements,
unit count, and inventory hash. A unit is the stable language-specific source
identifier the mutation runner can independently claim and report; generated,
test-only, vendored, or explicitly excluded code appears only through the
hashed exclusion policy. The dispatcher persists the complete inventory and
hash before dispatch and passes the hash/count/policy hash as workflow inputs.

Immediately before creating or adopting the audit ref, the adapter re-fetches
the repository default branch and verifies that the configured full-mutation
workflow path still exists there, is enabled, and declares
`workflow_dispatch`. This registration check is distinct from the immutable
audit-ref workflow-blob proof below: GitHub authorizes a manual dispatch from
the workflow registered on the default branch, while Oro executes and attests
the workflow definition at the audited snapshot. Default-branch movement,
workflow deletion/disablement, or trigger removal after startup is an
auth/config failure with no audit ref or workflow run created.

At that same no-side-effect barrier, the adapter re-evaluates every effective
repository and organization rule for the concrete generated audit-ref name and
attests the runtime App's create/adopt capability against the startup policy
evidence. Any new matching pattern, restriction, enforcement change, lost
bypass, or ambiguous policy result fails auth/config before ref creation.

`workflow_dispatch` is issued only against an immutable dispatcher-owned branch
ref, never the moving target branch or a SHA-like input. For each audit the
dispatcher creates/adopts
`oro/audits/<project-prefix>/<audit-id>-<sha-prefix>` at the exact persisted
audit SHA using an expected-absent or exact-old-SHA lease. A matching ref is
idempotently adopted; a mismatched existing ref is quarantined and never
rewritten. Before dispatch, the dispatcher reads and persists the configured
full-mutation workflow path/blob from that exact commit plus the audit ref/SHA.
If the workflow is absent or differs from configuration, the campaign fails
auth/config before execution.

The adapter dispatches with the immutable branch name and exact audit identity
inputs. The run must report that ref and head SHA, and the workflow emits its
own path/blob hash computed from the checked-out audit commit. Dispatcher
observation independently verifies workflow database ID/path/blob, run attempt,
ref, head SHA, and input audit ID before trusting inventory or artifacts. Target
movement immediately before/after dispatch cannot change workflow code or
checkout state. Restart recovery adopts the exact ref/run idempotently. The
audit ref is deleted with an exact SHA lease only after terminal artifact
incorporation and durable campaign reconciliation. Immediately before deletion,
the adapter re-evaluates the effective rules for that exact name and proves the
same App's deletion capability; denial or ambiguity enters durable
`cleanup_pending`, reports auth/config health, and retries without losing the
incorporated result or using an administrative credential. Unincorporated
evidence always retains the ref.

Run, job, check, and artifact discovery for an audit is complete-collection
only. Artifact ingestion begins only after every validated page has been
normalized and the resulting stable IDs/count/page evidence are durably bound
to the audit attempt. A later-page timeout, malformed response, duplicate ID,
pagination cycle, restart between pages, or configured bound exhaustion leaves
the campaign retryable/infrastructure-failed with zero prefix incorporated; it
can never satisfy exact-union completion from the first page alone.

The workflow independently reconstructs the inventory from its exact checkout
and fails before planning if any hash/count/policy value differs. Its shard
planner partitions the canonical inventory deterministically; the planner's
own output is never the definition of completeness. Every shard artifact lists
its assigned unit IDs and one terminal outcome per unit, including killed,
survived, valid-no-mutant, or infrastructure failure, plus artifact digest and
exact SHA. The aggregate proves shard manifests are an exact, nonoverlapping
union of the persisted inventory: no missing, duplicate, unexpected, wrong-SHA,
or wrong-policy unit can pass. An empty plan succeeds only when the independently
persisted inventory is genuinely empty. Zero generated mutants with nonempty
eligible inventory is infrastructure failure unless every unit has a validated
`valid-no-mutant` outcome under the pinned tool contract.

The persisted audit plan records the requested shard count, provider-reported
matrix bound, effective shard count, packing algorithm/version, ordered unit-to-
shard map, and plan hash. The effective count is
`min(requested, max_matrix_entries, inventory_unit_count)` for nonempty
inventories. A stable weighted bin-packing algorithm balances estimated mutation
work while deterministically assigning every unit exactly once; natural package/
file partitions never become unbounded matrix dimensions. The workflow planner
reconstructs that map, asserts its matrix length is within the persisted provider
bound before expansion, and the aggregate still proves exact-union outcomes at
unit granularity. Invalid zero/negative bounds fail typed preflight. A configured
request above the provider limit is safely capped and reported, not retried as an
invalid workflow; an emitted plan above the bound is infrastructure failure
before dispatch in Oro and is independently rejected by the workflow and fake.

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
- records inventory, policy, deterministic plan, shard-manifest, and artifact
  hashes and requires exact-union proof before aggregation succeeds;
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

The durable audit completion record contains inventory hash, policy hash,
shard-plan hash, workflow path/blob/run attempt, artifact IDs/digests, eligible
and covered unit counts, and exact-union proof. Missing evidence or unequal
counts keeps the audit unsuccessful even when every produced shard itself
reported success.

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
- integrated-policy-drift evidence and whether target integration is frozen;
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
remote_gate_integrated_policy_drift / policy_reconciled
remote_gate_reconciled
epic_postmerge_install_started / completed / failed
```

The monitor treats a remote gate with no observation beyond the configured
timeout, a repeated deterministic fingerprint without a worker-feedback event,
a terminal run attached to a nonterminal state, or integrated policy drift as a
dispatcher defect and files/deduplicates a P0 bead automatically. Policy drift
also triggers the separately authorized setup-reconciliation path; runtime
credentials never gain ruleset-write permission.

### 15. Implementation Coverage Plan

`beadcraft` must preserve these work packages and their named production call
chains; it may split them further but may not collapse away a boundary:

1. **Typed configuration and repository preflight.** Read and change the
   project config model, `cmd/oro/cmd_start.go:newProductionDispatcher`,
   `dispatcher.Config.validate`, and `Dispatcher.Run` before socket readiness.
   Cover defaults, precedence, malformed values, workflow eligibility, target
   rulesets, complete effective repository/organization policy, unsupported
   human/deployment/merge-queue blockers, dedicated runtime integration
   identity provider/secret reference/refresh/permissions, auth, target CAS
   support, startup events, and status. Production construction must not fall
   back to ambient credentials. Setup/startup must inspect the exact permission
   allowlist/hash and recent isolated capability-canary evidence. Discover and
   persist the repository default branch independently of the configured target;
   require the full-mutation workflow to be present, enabled, and registered
   with `workflow_dispatch` on that branch. Evaluate effective repository and
   organization rules for the prospective `oro/audits/<project-prefix>/**`
   namespace, persist operation-specific create/adopt/delete capability and
   policy hashes, and run the capability canary inside that exact namespace.
   Persist the provider's typed matrix-entry limit and reject full-mutation
   enablement when that limit is absent or invalid. Add the distinct typed
   maintenance identity/owned-ruleset/template configuration, exact permission
   validation, secret-reference resolution, recent setup attestation, and
   production constructor inputs through `newProductionDispatcher` and the
   supervisor descriptor. Config/descriptor validation forbids a pinned provider
   ID and requires the stable logical ownership key/name instead. Require a live
   compatible supervisor/monitor heartbeat and maintenance attestation for every
   `github-pr` startup even when epic auto-install is false; monitorless explicit
   configuration fails before the dispatcher socket accepts work.
   Prove prospective `epic/**` create/update/delete capability and persist the
   exact-namespace canary evidence separately from candidate/audit namespaces.
   Construct all ref canaries through the same internal hook-free Git transport
   used in production; a REST-only or hookless-repository probe is insufficient.
   Setup/doctor persist and startup re-attests canonical Git binary/exec-path/
   HTTPS-helper identities and hashes; missing, moved, replaced, or untrusted
   helper evidence fails before any credential-bearing subprocess.
   Install/validate the supported package-managed `gh`, persist its absolute
   path/version/provenance/file identity/hash, and validate typed API host,
   TLS/CA/proxy policy, API version, and size/time limits through the exact
   minimal-environment runner. Missing or changed CLI fails readiness.
   Wire the dependency into `Makefile` install/reinstall, `oro setup`/doctor,
   and packaging metadata. macOS subprocess fixtures cover missing/present `gh`,
   `brew install gh`, absent/failing Homebrew, supported-version validation, and
   idempotent epic rebuild/install; uninstall never removes the shared Homebrew
   CLI. Linux packaging declares the dependency and unpackaged setup emits the
   exact remediation command.
   Thread effective `ManualIntegration` through both CLI parent/child start
   paths and reject it with `github-pr` before any startup side effect; local
   mode remains compatible.
   On local-mode startup, inspect durable maintenance/freeze/deferred-sync state
   before applying monitorless defaults; require preserved supervisor/runtime/
   maintenance identity and enter recovery-only until reconciliation completes.
2. **Provider-neutral core and GitHub adapter.** Implement normalized
   candidate/change/evidence/merge types, all `RemoteGateClient` operations
   including idempotent `SetChangeReady` and ambiguous
   `PrepareSquash`/`IntegrateSquashCAS`, and GitHub transport isolated behind
   the adapter. Preparation cannot mutate the provider; integration accepts
   only the one returned, durably acknowledged `PreparedSquash`.
   Read `pkg/janitor/detect.go` for environment/host/auth conventions and add
   boundary/import tests. The production-faithful fake must reject merge while
   a change remains draft and model target movement at the exact CAS barrier,
   policy mutation in that same interval, approving review, CODEOWNER, conversation,
   deployment, signed-commit, lock/read-only, actor restriction, and merge-
   queue policy blockers.
   Bind the attested `gh` API runner and Git network transport to one credential-
   provider actor; test SSH origins, ambient admin/credential helpers, mismatched actors,
   host/repository scope, expiry refresh, and secret redaction through the real
   adapter constructor. Expose the same service-safe credential and canonical
   HTTPS transport constructors to the managed lifecycle runner; no second Git
   network implementation is permitted.
   Model accepted-CAS/lost-response followed by a second target advance before
   observation; reconciliation returns the persisted proposed commit as
   integrated without requiring it to remain the current tip.
   Make the API fake permission-aware per endpoint and test every required
   permission removed individually, every forbidden extra permission, token
   permission drift/revocation, `X-Accepted-GitHub-Permissions`, and host/API
   differences. The setup canary exercises probe ref/PR, workflow dispatch/
   observation/cancellation, check/job/log reads, and artifact download.
   Model GitHub's branch/tag-only `workflow_dispatch` ref semantics; expose
   leased immutable audit-ref create/adopt/delete plus workflow path/blob/run-
   head attestation through the provider boundary. Model the repository default
   branch and dispatch ref as separate provider identities, and reject dispatch
   when the workflow is absent, disabled, or lacks `workflow_dispatch` on the
   current default branch even if the same workflow is valid at the audit ref.
   Make the deterministic fake apply repository/organization rules by ref
   pattern, operation type, enforcement mode, and App bypass actor. Candidate,
   generic probe, target, and audit namespaces must be independently configurable.
   Model provider execution limits and reject a planned matrix above the reported
   bound exactly as the production host does.
   Implement `EnsureEphemeralTarget`/`DeleteEphemeralTarget` with expected-
   absent/exact-SHA leases, ownership/generation checks, ambiguity observation,
   and pattern-specific policy enforcement. The fake independently denies epic
   creation, advancement, or deletion while other namespaces remain usable.
   Implement the provider-neutral `PolicyReconciler` boundary and GitHub adapter
   with discover/create/update/adopt/deduplicate/rebind plus owned-ruleset/
   identity/template/idempotency validation. Production
   constructor tests prove runtime credentials cannot construct it, maintenance
   credentials cannot perform Git/PR/Actions operations, and absent, malformed,
   expired, wrong-host/repository, overprivileged, or inaccessible maintenance
   identity fails closed without secret leakage.
   Add the unexported hook-free internal Git transport and route every
   dispatcher-owned push/delete/CAS through it. Tests reject missing leases,
   unsupported operation kinds, noncanonical/SSH/helper transports, inherited
   hook/config injection, absent `core.hooksPath=/dev/null` or `--no-verify`,
   and any worker/general-CLI construction path; ordinary Git remains unchanged.
   Poison `GIT_EXEC_PATH`, `PATH`, template/object/alternate/namespace/protocol/
   config variables, proxy/CA overrides, and dynamic-loader variables with
   credential-capturing/failing sentinels. Real Git internal operations must use
   only the setup-attested helper chain, while an ordinary user Git invocation
   still sees the poisoned environment.
   Implement all GitHub API operations through the one attested `gh` runner;
   adapter and reconciler cannot construct subprocesses independently. The
   production-faithful fixture covers every endpoint and rejects wrong host,
   unconfigured proxy/CA, oversized/malformed JSON, forged actor/scope, and token
   leakage. Poison PATH with a credential-capturing `gh`, plus `GH_TOKEN`,
   `GH_HOST`, `GH_CONFIG_DIR`, loader, and proxy variables; only the absolute
   attested executable may run with the generated minimal environment.
   Add the typed complete-collection operation and require it for every PR,
   check, workflow run/job/artifact, effective-policy, repository-ruleset, and
   ruleset-history enumeration. It uses bounded `--paginate --slurp`/Link
   traversal, validates all pages atomically, normalizes page shapes, rejects
   duplicate IDs/cycles/host changes/incomplete bounds, and returns page/count
   evidence. Split owned, foreign, and duplicate rulesets across pages; no
   repair, deduplication, readiness, integration, or unfreeze may proceed from
   an incomplete enumeration.
3. **Protocol and worker handoff.** Wire `CANDIDATE_READY` through
   `pkg/protocol/message.go`, `pkg/worker/worker.go:awaitSubprocessAndReport`,
   `runQGAndReport`, `SendReadyForReview`, and `SendDone`. Update
   `pkg/worker/prompt.go:buildCodingSections`. Prove malformed, oversized,
   stale, and duplicate handoffs fail safely and GitHub mode cannot enter the
   legacy full-QG or `DONE` merge path.
   Prove remote `CANDIDATE_READY` cannot enter `completeManualIntegration`, and
   that rejected startup sends no worker/protocol/remote messages.
   Add mandatory `HELLO`/`HELLO_ACK` negotiation carrying project, restart/
   worker generation, image/build, process identity, ownership type, and
   protocol range before `registerWorker`/`upsertWorker`. Version all assignment,
   shutdown, heartbeat, candidate, review, and completion messages; stale or
   incompatible workers never register.
4. **Durable remote state and dispatcher ownership.** Add normalized SQLite
   schema/types/indexes/migrations/CAS for candidate, run, evidence,
   correction, audit campaign, post-install state, and monotonic runtime-control
   generation, plus `integrated_policy_drift` evidence and the project-wide
   integration freeze/setup-reconciliation request, lease, attempt, and evidence
   state, including the logical-key policy-binding registry, mutable provider ID,
   binding generation, create-attempt ID, ambiguity, duplicate leases, and
   `maintenance_unavailable` heartbeat/capability generation. Model the freeze
   as monotonic barrier-generation plus attempt/target/maintenance-owner blocker
   and issued/post-CAS participant rows, never a boolean or decrementing counter. Wire
   `handleMessage`,
   `handleDone`, `handleReadyForReview`, `handleReviewResult`,
   `startupRecovery`, `restoreState`, `spawnBackgroundLoops`,
   `checkClosedBeadAssignments`, and `handleClosedAssignment`. Test restart
   after worker death and worktree deletion. Add dispatcher-owned local
   adoption-ref creation, reachability proof, lease persistence, orphan-ref
   startup recovery, and crash injection between ref creation, row commit, ACK,
   worktree deletion, rebase, and first remote publication.
   Persist the deterministic integration-attempt row and local proposed-squash
   ref before CAS; recover orphan/prepared attempts and idempotent
   reconciliation markers across restart. The dispatcher writer commits the
   adapter-returned `PreparedSquash` between the two typed operations and never
   recomputes its SHA.
   Add the same-database bead-generation/integration-intent transaction and a
   non-assignment remote-record close scan.
   Wire `startupRecovery`, `restoreState`, and `spawnBackgroundLoops` to adopt
   or observe monitor-owned reconciliation requests and unfreeze only after a
   fresh runtime-identity policy proof matches. Inject crashes at every claim/
   provider-update/evidence-commit/unfreeze boundary and prohibit duplicate
   integration or premature closure.
   Inject 404 deletion, lost create/update/delete responses, duplicate creation,
   crash-before/after active-ID binding, and stale descriptor/provider-ID cache;
   prove one active exact-template ruleset and monotonic binding generation.
   Continuously observe the claimant lease and block integration intent/CAS when
   stale; recovery must not enter memory-safe local merge or mint maintenance
   credentials inside the dispatcher.
   Add `epic_target` seed/provider/ownership/generation/active/retired/cleanup
   rows and CAS transitions. Serialize concurrent first-child ensure, child
   integration, epic close, external ref deletion, and cleanup so a retired ref
   cannot be recreated and a missing active ref is restored only at the last
   durably integrated SHA.
   Implement local-manual rollback reconciliation: cancel/requeue pre-intent
   remote records, resolve intent/ambiguous outcomes before socket readiness,
   and preserve one-owner/no-legacy-merge invariants across restart.
   Make the project-wide freeze a shared eligibility predicate for assignment
   and every local/remote integration producer. Persist recovery-only mode and
   require policy repair, authenticated authoritative fetch, first-parent/tree
   proof, exact descendant fast-forward, remote-record closure, and unfreeze in
   the specified order; inject crashes and dirty/divergent local targets.
   Inject two or more CAS attempts on main/custom/epic targets before the first
   drift becomes visible, deduplicated/shared and distinct policy repairs,
   out-of-order participant sync, lost/ambiguous outcomes, duplicate recovery,
   and restart. Prove only a generation-checked zero-unresolved last-owner
   transaction restores assignment/integration eligibility.
   Persist the worker generation/process inventory and make worker pool
   registration, idle selection, capacity, health, status, and assignment
   require active-generation attestation. `shutdownWaitForWorkers`,
   `killManagedWorkers`, and `cleanupResidualProcesses` return/persist verified
   outcomes and rescan project-owned process groups beyond connected IDs.
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
7. **Exact evidence, ops review, and atomic squash integration.** Wire the remote state
   machine through `checkPreMergeQG`, `mergeAndComplete`, and
   `finalizeSuccessfulMerge`; bind the ops-reviewed tree, tested synthetic
   merge tree, strict target policy, and squash result. Test base races,
   same-name checks, reruns, changed workflow, draft-to-ready transition and
   ambiguous response, ambiguous merge response, ruleset removal/bypass,
   compound policy mutation plus base movement at the final CAS barrier,
   policy-only removal and mutation after the last policy read with an unchanged
   target, post-CAS drift evidence/integration freeze/setup reconciliation,
   accepted-CAS/lost-response followed by descendant advancement and restart,
   crashes before/after preparation, row commit, remote mutation, and adapter
   return through the real two-phase production call chain,
   unexpected result tree, and local divergence without destructive reset.
   Before `Publish`/`EnsureChange` for a non-main epic base, wire local
   `PrepareBaseBranchForAssignment`/`CreateBranch` seed evidence through the
   durable `EnsureEphemeralTarget` transition; PR creation is impossible until
   exact remote-base adoption commits.
   Add real `oro start` and `oro dispatcher start` fixtures proving
   `github-pr + --manual-integration` fails before publication/CAS, including a
   passed remote record and restart; prove local-manual rollback follows the
   cancellation/reconciliation contract instead of silently auto-integrating.
   At the post-CAS/pre-local-sync policy-drift barrier, switch to ordinary local
   and local-manual modes and restart. Exercise `handleDone`,
   `completeManualIntegration`, `mergeAndComplete`, and `ffMergeEpicBranch`;
   each performs zero mutation until supervised repair and authoritative
   descendant local sync clear the freeze.
   Repeat with two drift/issued participants and resolve them in reverse order;
   the first resolution cannot admit any legacy or remote integration.
8. **Correction and cleanup.** Persist bounded findings, create a normal pool
   correction assignment at the exact remote candidate SHA, and handle dead
   workers, deleted worktrees, missing refs, duplicate findings, non-ancestor
   squash cleanup, adoption-ref retention/removal, cancellation, rollback, and
   recovery quarantine. Include
   `TestRemoteGateModeRollbackOwnership` across publishing, running, passed,
   and ambiguous-merge states after restart; new candidates use local mode,
   while every old record remains exclusively remote-owned until terminal or
   durably cancelled.
   The shared candidate source resolver is exercised for prepublication
   presubmit/ops rejection (local adoption ref), postpublication rejection
   (verified remote ref), startup recovery, and missing-source quarantine.
   Inventory every close, deduplication, preemption, requeue, rollback, and
   shutdown producer. All must use cancellation-before-intent or
   `cancel_pending`-after-intent semantics, and legacy recovery merge is
   forbidden for a remote-owned record. Deterministic barriers cover both
   orderings immediately around intent and a rejected, successful, and
   ambiguous CAS outcome.
9. **Epic promotion and local installation.** Replace the GitHub-mode path in
   `tryCloseEpic`, `completeEpicClose`, and `ffMergeEpicBranch` with promotion
   state, remote evidence/merge, local sync, durable build/install/restart,
   hash comparison, health proof, and retry. Wire
   `pkg/dispatcher/dispatcher.go:applyRestartDaemon`,
   `cmd/oro/cmd_monitor.go:cliMonitorRunner.RestartDaemon`, the monitor action
   ledger/claim loop, `cmd/oro/cmd_start.go:startFreshSwarm`, setup-managed
   stable supervisor shim/versioned-child protocol, launchd/systemd service
   installation, supervisor heartbeat/schema preflight,
   and post-epic operation store. The hermetic test uses real old-dispatcher and
   external-monitor processes, asserts old PID exit/new PID start, expected
   installed/repository hash and worker config, replacement health, durable
   acknowledgement, epic closure, and recovery after monitor crashes both
   after install and after old-daemon exit. An in-process lifecycle fake cannot
   satisfy this package.
   Start the bare remote without `epic/<id>`, race two first children, lose the
   winning create response, restart, and prove one exact seed ref plus two valid
   child PRs. Cover mismatched existing ref, operation-specific ruleset denial,
   external deletion/recreation while active, promotion retirement, leased
   deletion ambiguity, changed-ref quarantine, cleanup pending, and no post-
   retirement resurrection.
   Build that source repository through real Oro init/setup so its managed
   pre-push wrapper and a sentinel failing user hook are installed. Prove the
   internal epic/candidate/audit/target mutations reach the bare remote without
   invoking either hook or local QG, while ordinary human pushes still invoke
   the wrapper/user chain and `epic/*` is rejected. Assert hook files and Git
   configuration are byte-for-byte unchanged afterward.
   Add the project-global lifecycle generation/lease, ancestry-ordered desired
   SHA selection, descendant-satisfies-ancestor evidence, no-downgrade running
   SHA, and pre-install/pre-shutdown/pre-start revalidation. The same test runs
   two epic operations `S1 -> S2`, forces reverse row/claim order, target
   advancement during build, a late ancestor operation, and monitor crashes;
   it proves at most one necessary restart, both epics acknowledged/closed, and
   final running build equal to the newest authoritative descendant. Divergent
   requirements fail closed.
   Wire `cmd/oro/cmd_setup.go:runSetup`/phase 4/doctor, a versioned supervisor-
   descriptor generator, `cmd/oro/launchd.go` project labels/plist paths,
   systemd user-unit generation, `cmd/oro/cmd_monitor.go:newMonitorCmd`, and
   `cmd/oro/paths.go:ResolveDaemonPaths` so managed mode threads explicit
   project context end to end. The installed-service test starts with empty
   project environment/unrelated CWD, then installs projects A and B
   concurrently and proves distinct descriptors, service identities, ledgers,
   heartbeats, sockets, restart targets, and uninstall isolation. Repository
   relocation fails closed until setup repairs the exact instance.
   The supervisor descriptor carries the hash-bound nonsecret credential-
   provider reference and expected actor/repository scope. Extend the epic test
   with an authenticated private-remote-equivalent HTTP fixture, launch through
   the clean service environment, expire the App token between build and
   pre-shutdown/pre-start checks, and prove refresh, scope attestation, target
   revalidation, redaction from service/log/rows, restart, acknowledgement, and
   epic closure. Anonymous, SSH/helper, ambient, wrong-actor/repository, and
   unrefreshable credentials fail without mutation.
   Extend `cmd/oro/cmd_setup.go:runSetup`/doctor with creation or idempotent
   repair of the marked Oro-owned repository ruleset and a maintenance-
   capability attestation. Extend the stable descriptor, monitor claim loop,
   and supervisor protocol with typed policy-reconciliation operations. A real
   authenticated ruleset API fixture—not an injected callback—expires the
   maintenance token, restarts monitor/dispatcher at each boundary, proves
   exact-scope refresh and redaction, restores the template, verifies through
   the runtime identity, releases the integration freeze, and never grants the
   runtime App Administration. Delete the live ruleset so recreation returns a
   different provider ID, lose the create response, permit duplicate marked
   creation, and prove discovery/deduplication/atomic rebind plus final unfreeze
   without rewriting static config or the descriptor.
   The external monitor constructs the same attested `gh` runner from the
   descriptor; a poisoned PATH/GH environment cannot substitute the executable
   or intercept its maintenance token, and process inventory proves only the
   expected absolute binary is spawned.
   Run the same project with `auto_install_after_epic: false`: no supervisor
   fails startup, while an installed supervisor passes. Kill the monitor and
   stable shim, prove OS-service relaunch and durable request reclaim; while its
   heartbeat is stale, no new integration intent or CAS occurs.
   Then switch configuration to local at the integrated-policy-drift/pre-sync
   boundary: the supervisor remains required and claims repair, while the
   dispatcher stays recovery-only. Prove exact remote descendant sync and
   transactional unfreeze before any local worker assignment or merge.
   Share one policy-repair request across eligible blockers while retaining
   per-attempt target sync; a monitor crash or duplicate claim cannot resolve a
   blocker it does not own.
   Build distinct old/new Oro fixtures with both compatible and incompatible
   descriptor/operation-schema ranges. Exercise the normal no-crash
   `M0/B0 -> shim -> M1/B1 ->
   D0 -> D1` path and crashes before/after fencing, lease release, child start,
   compatibility probe, heartbeat, and handoff ACK. Assert distinct monitor PID, actual
   executable-image/build hash, compatible schemas/generation, old-monitor
   fencing, dispatcher restart only after M1 proof, and automatic B0 rollback
   with unchanged stable-ledger B0 read/write/claim and D0 left running when B1
   preflight or M1 heartbeat fails. Incompatible supervisor protocol is rejected
   before handoff. Separately test reversible project-DB migration only after D0
   quiescence, crashes before/after backup/migration/activation, verified B0
   preimage restoration plus D0 read/write/health, and pre-shutdown rejection of
   irreversible migration.
   Persist `applyPause`/`applyResume`, `applyFocus`, `applyScaleDirective`, and
   `applyMaxWorkersDirective` before ACK. Add restart freeze/unfreeze,
   `cmd/oro/cmd_start.go:runFullStart` managed-inert startup, and assignment-loop
   eligibility only after exact control-generation acknowledgement. Test
   directives at build/pre-freeze/post-freeze/pre-start barriers, paused no-
   dispatch health, focus/capacity restoration, retryable rejected directives,
   B1-config/override incompatibility, and B0 rollback without stale controls.
   Implement the typed read-only planner/readiness proof and compose lifecycle
   acknowledgement plus epic closure with each preserved state: paused, zero
   target/max workers, no eligible bead, focus excluding all ready beads, and
   assignment-eligible. The paused real-process case remains paused, emits zero
   production `ASSIGN` messages, acknowledges the lifecycle operation, and
   closes the epic without an operator action or synthetic production bead.
   Spawn stubborn managed/external B0 workers that disconnect before inventory,
   ignore graceful shutdown, and reconnect after D1 socket creation. Prove
   managed process-group escalation/rescan blocks D1 until zero residuals,
   external/stale HELLO rejection, no registration/assignment/capacity impact,
   attested B1 replacement workers, mixed-protocol rejection, and a newly
   rotated rollback generation excluding partial B1 workers.
10. **Remote full mutation audit.** Add the workflow-dispatch/shard/aggregate
    workflow and wire `pkg/dispatcher/audit.go:runAudit` to durable exact-SHA
    campaign observation, artifact ingestion, restart, infrastructure failure,
    and survivor bead creation. Implement the independent canonical eligible-
    unit inventory/policy hasher, remote reconstruction check, deterministic
    planner, per-unit shard manifests, artifact digests, and exact-union audit
    completion proof. Test new/unconfigured packages, remainder shards, missing/
    duplicate/unexpected/wrong-SHA units, stale policy, empty inventory, empty
    plan with nonempty inventory, valid-no-mutant, artifact loss, and restart.
    Make the provider-bound planner persist requested/bound/effective counts and
    a deterministic unit-to-shard packing map. Test 255, 256, 257, and much
    larger natural shard inventories, requested count above the host limit,
    absent/invalid capability, balance determinism, restart-stable plan hashes,
    workflow/fake rejection of an injected 257-entry matrix, and unit-level
    exact-union proof after packing.
    Use the permission-aware runtime App fixture for audit workflow dispatch,
    run/job observation, cancellation, logs, and artifact download; any missing
    Actions capability fails auth/config before campaign side effects.
    Force workflow runs, jobs, checks, and artifacts across multiple pages,
    including page sizes 1 and 30 and enough artifacts for the 256-shard case.
    Inject restart between pages, later-page timeout/malformed JSON, duplicate
    stable IDs, cyclic/foreign Link targets, and page/item bound exhaustion.
    Assert no prefix is incorporated, no exact-union proof passes, and retry
    restarts at page one until one complete normalized collection is durably
    bound to the attempt.
    Create/adopt the exact-SHA namespaced audit branch before dispatch, persist
    expected workflow blob, require run ref/head/workflow identity equality,
    retain the ref through restart/artifact incorporation, and lease-delete it
    only after reconciliation. Inject target movement and workflow change at
    the snapshot/dispatch barrier plus ref collision, stale run, and cleanup
    crash cases. Before audit-ref creation, re-fetch the repository default
    branch and repeat the workflow registration/enablement/trigger check. Test a
    configured custom or release audit target whose snapshot contains a valid
    workflow while the default branch lacks it, disables it, removes its trigger,
    or changes default branch after startup; each case fails before ref or run
    creation. The production-faithful fake enforces default-branch registration
    independently from dispatch-ref workflow identity. Re-attest all effective
    rules for the concrete audit-ref name immediately before create/adopt and
    delete. Fixtures independently deny audit-namespace creation, update/adopt,
    and deletion while candidate/target/generic-probe refs remain usable; cover
    startup drift, lost bypass, ambiguous policy reads, failed cleanup recovery,
    restart, and proof that neither setup administration nor another actor
    silently performs runtime cleanup.
11. **Observability and self-healing.** Extend dispatcher/status JSON,
    `cmd/oro/cmd_status.go`, health online/offline loaders, monitor defect
    rules, dashboard provider/templates, and progress responses for every
    required state and finding, including reconciliation request/lease/attempt,
    redacted maintenance identity status, before/after ruleset evidence, retry
    age, barrier generation, blocker/participant owner and unresolved counts,
    last-owner unfreeze proof, and integration freeze.
12. **Hermetic epic verification and canary.** Build
    `scripts/test_remote_gate_epic.sh` around a real local Git remote, the real
    package-managed `gh`, and a deterministic production-faithful GitHub API
    fixture. Parse `go test -json` and require a
    test-level pass event for every test in the fixed manifest below. The
    harness fails if the manifest is empty, has duplicates, names an absent
    test, or lacks a criterion mapping. Statically validate both PR and full-
    mutation workflows, including the dynamic-matrix bound assertion and the
    absence of unbounded Cartesian dimensions, then run the controlled Oro
    GitHub canary only after
    current local history and the workflow are published.
    Provider-boundary fixtures include the monitorless auto-install-disabled
    startup rejection and stale-heartbeat integration barrier so the always-
    supervised requirement cannot be hidden by the canary's normal monitor.
    The epic fixture asserts the bare remote initially lacks every `epic/**`
    ref and the source has the actual Oro-managed pre-push wrapper plus user-hook
    sentinel; pre-seeding the base or using an uninitialized hookless source is
    a harness failure.
    It also supplies a poisoned `GIT_EXEC_PATH` containing a sentinel
    `git-remote-https`, poisoned object/config/protocol variables, and executable/
    loader/proxy overrides. Every real-Git internal mutation must ignore them,
    and the sentinel must observe neither execution nor App credentials; the
    ordinary user-Git control proves the poison is otherwise effective.
    The API side uses a real TLS server and poisons PATH with a token-capturing
    `gh` plus ambient GH/proxy/CA/loader values. Endpoint coverage and process
    inventory prove every runtime and maintenance API call invokes only the
    attested absolute CLI with typed network policy and never feeds a sentinel.
    The fixture paginates every correctness-critical collection and fails any
    caller that omits traversal, requests an unbounded sequence, exposes a page
    prefix, or bypasses the shared normalizer. It places matching/foreign/
    duplicate rulesets and required run/job/check/artifact evidence beyond page
    one, and records page/count evidence in the fixed-manifest assertions.
    CLI fixtures cover both installed start entry points with manual integration
    on/off in local/GitHub modes and assert zero remote side effects on the
    invalid combination.
    The fixed manifest maps the rollback-at-integrated-policy-drift cross-product
    to both `TestRemoteGateModeRollbackOwnership` and
    `TestRemoteGateExactEvidenceAndProtectedMerge`; testing those states only in
    isolation does not satisfy the harness criterion.
    The same mapping requires concurrent drift blockers on different targets,
    reverse-order recovery across restart, and no eligibility until the final
    participant resolves.
13. **Automatic degraded mode and memory-safe full fallback.** Own the
    `transient_failed -> outage_degraded -> local_memory_safe_gate ->
    local_passed_waiting_remote` dispatcher transitions, outage timer, exact
    local evidence, recovery, and project-global lease. Add
    `scripts/quality_gate.sh --profile=memory-safe` with behavioral subprocess
    tests for sequential lanes, `GOMAXPROCS=2`, `go test -p 1`, cancellation,
    and lease release. Test outages during publish, observe, and merge
    separately and prove automatic return to remote mode.

The committed epic integration-test manifest is nonempty and maps one-to-one
to required behavior:

```text
TestRemoteGateConcurrentCandidates
TestRemoteGateRestartRecovery
TestRemoteGateDegradedPublishOutage
TestRemoteGateDegradedObserveOutage
TestRemoteGateDegradedMergeOutage
TestRemoteGateDraftReadiness
TestRemoteGateModeRollbackOwnership
TestRemoteGateIntegrationCancellationRace
TestRemoteGateWorkflowContract
TestRemoteGateWorkerHandoffAndCorrection
TestRemoteGateAdoptionCrashBoundary
TestRemoteGateExactEvidenceAndProtectedMerge
TestRemoteMutationAuditCampaign
TestEpicRemoteGateAndInstall
TestRemoteGateObservability
TestRemoteGateProviderBoundary
```

`scripts/test_remote_gate_epic.sh` owns this literal manifest (or reads an
equivalent committed data file), verifies exactly 16 unique entries, associates
each with acceptance criteria 1–10, and requires a test-level JSON `pass` event
for every entry.

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

Rollback does not make the supervisor optional while a project-wide integration
freeze, policy-reconciliation request, deferred remote local-sync, epic cleanup,
or other maintenance-owned record exists. Such a local-mode restart enters the
recovery-only state above and retains both credential references until all
barriers are reconciled. Only a clean local project with no maintenance-owned
state may return to the otherwise valid monitorless local configuration.

If rollback also enables local `--manual-integration`, startup recovery first
cancels every pre-intent remote record and requeues its preserved candidate into
the local-manual path. An integration-intent or ambiguous-CAS record is
reconciled to an exact integrated/not-integrated outcome before the socket opens;
an unintegrated candidate is requeued, while an already-integrated result is
reported exactly once. Local manual policy never causes a remote-owned record to
enter legacy merge, and no preexisting remote record may continue to a new CAS
under the manual session. Switching back to `github-pr` requires the manual flag
to be absent; existing local-manual records remain locally owned until completed
or explicitly cancelled.

## Acceptance Criteria

1. In a hermetic integration test with two candidates targeting the same base,
   the dispatcher publishes both, accepts only the exact passing merge evidence,
   invalidates the second candidate when the first advances the base, reruns it,
   sends a deterministic failed run's bounded findings to the worker, and
   fast-forwards only after the replacement run and ops review pass.
   A concurrent subcase issues CAS attempts on two different targets before
   post-CAS policy observation, makes both barrier participants, recovers them
   in reverse order across restart, and proves the first resolution cannot
   restore assignment or integration eligibility.
2. Killing and restarting the dispatcher during `awaiting_run`, `running`, and
   `passed` resumes the same exact PR/run when valid and never duplicates a PR,
   loses findings, or merges stale evidence.
3. With GitHub transiently unavailable beyond the configured threshold, at
   most one local memory-safe gate runs, progress continues without operator
   action, and the dispatcher automatically returns to remote mode after
   recovery.
4. Epic promotion uses a remote PR gate for the combined branch, then performs
   and verifies the local build/install/restart operation through a real
   externally supervised monitor process. The test crosses old-daemon exit/new-
   daemon start, checks PID/build/install hashes/config/health/acknowledgement,
   and resumes monitor crashes after install and after daemon exit. With two
   epic operations `S1 -> S2`, reverse claim order and target movement during
   build never downgrade or restart twice unnecessarily; the healthy `S2`
   installation durably satisfies both operations and closes both epics.
   The real bare remote begins with no `epic/**` ref. Two concurrent first
   children converge through expected-absent create/exact-SHA adoption despite
   lost response and restart, then create PRs against the one remote base.
   Promotion retires and lease-deletes that ref; mismatches, policy denial,
   ambiguity, cleanup pending, and post-retirement non-resurrection are proven.
   The source repository is actually Oro-initialized with the managed pre-push
   wrapper and a failing user-hook sentinel: internal leased ref operations
   bypass both without running local QG, ordinary pushes still execute them,
   and human `epic/*` publication remains blocked.
   The same test launches the actually generated launchd/systemd-equivalent
   unit from an unrelated CWD with empty ambient project environment, proves
   exact descriptor-bound database/socket/repository identity, and verifies
   simultaneous project A/B service identity plus uninstall isolation and
   relocation failure. A private-remote-equivalent authenticated fixture expires
   the App token between lifecycle stages and proves monitor-side refresh,
   actor/repository attestation, no ambient fallback, and secret redaction.
   It also proves normal and crash-boundary supervisor upgrade from old
   `M0/B0` to distinct `M1/B1` with fencing, compatibility proof, heartbeat
   PID/image/build/generation evidence, rollback to B0 before dispatcher
   shutdown on incompatibility, and `D0 -> D1` only after M1 acknowledgement.
   Stable supervisor protocol incompatibility is rejected without changing its
   ledger; rollback proves B0 read/write/claim. Project DB migration occurs only
   after D0 quiescence with a verified preimage, and failure restores B0 DB/D0
   health; irreversible migration never stops D0.
   Pause/run, focus, target workers, max workers, and explicit overrides use a
   durable control generation. Directives before freeze survive exactly;
   directives after freeze are retryably rejected. D1 cannot dispatch before
   acknowledging that generation, and rollback D0 never revives stale control.
   With the pre-freeze state paused, zero-capacity, empty, or fully focus-
   filtered, D1 emits the matching exact-generation read-only readiness proof;
   the supervisor acknowledges and closes the epic while preserving that state
   and without any production assignment. The paused subcase proves zero
   post-start `ASSIGN` messages. Assignment-eligible readiness is also covered
   without making ordinary queue availability a completion prerequisite.
   Stubborn/disconnected B0 managed workers are inventoried and terminated;
   surviving external/stale workers fail the mandatory generation/build/
   protocol HELLO and never affect idle capacity or assignments. D1 dispatches
   only through attested B1 workers, and rollback rotates generation again.
5. `oro status --json`, health, monitor events, and the dashboard expose remote
   backlog, degraded mode, failure feedback delivery, quarantine count, and
   pending post-epic installation, audit-ref cleanup pending with the blocking
   policy evidence, mutation requested/provider-bound/effective shard counts,
   integrated policy drift plus integration-freeze/setup-reconciliation state,
   ephemeral epic-target creation/adoption/retirement/cleanup state,
   logical policy key, active provider ID/binding generation, create ambiguity/
   deduplication, supervisor heartbeat/capability generation and maintenance-
   unavailable barrier, cross-mode recovery-only/deferred-sync state, lifecycle
   barrier generation and blocker/participant owners/unresolved count/last-owner
   proof, readiness reason/control/queue generations, expected/attested
   active-generation workers, stale worker connections, and residual old-
   generation processes.
6. Workflow contract fixtures prove PR eligibility for main, a configured
   custom target, and an `epic/**` target; prove the aggregate includes every
   portable job including strict incremental mutation; and reject mutable
   action tags, write permissions, secrets, `pull_request_target`, head-only
   checkout, missing/skipped needs, a non-strict target ruleset, any bypass
   actor other than the dedicated least-privilege integration identity,
   and every unsupported effective human/deployment/conversation/signature/
   lock/actor/merge-queue policy. Production transport fixtures prove API and
   Git network operations use the same configured App installation and reject
   ambient, split, expired-unrefreshable, wrong-host, or wrong-repository
   credentials without exposing secret material. This assertion covers both
   dispatcher and externally supervised lifecycle constructors/transports.
   On macOS, a clean Oro install/setup with Homebrew and no `gh` executes
   `brew install gh`, attests the resulting supported CLI, and enables the
   feature; an existing valid CLI is untouched on rebuild/reinstall. Missing or
   failed Homebrew and unsupported CLI versions fail with actionable output,
   and Oro uninstall does not remove the shared package.
   The granted token permission set exactly matches Metadata-read,
   Contents-write, Pull-requests-write, Actions-write, Checks-read, and
   Workflows-write. A permission-aware probe/fake rejects every missing member,
   every forbidden extra permission, drift/revocation, and endpoint-specific
   dispatch/cancel/observe/log/artifact denial before production work.
   The setup canary creates, observes, and exact-SHA lease-deletes its unique
   probe inside `oro/audits/<project-prefix>/**`; policy fixtures prove that a
   usable target/candidate/generic probe does not imply audit-namespace create,
   adopt, or delete capability.
   A separate unique `epic/**` canary proves create, exact-SHA advancement, and
   leased deletion under that namespace's effective repository/organization
   rules; other ref probes cannot satisfy this assertion.
   Production transport fixtures install the real wrapper plus arbitrary
   sentinel hook and prove every internal push kind uses the scoped hook-free
   constructor with lease evidence, while worker/user pushes cannot access the
   bypass and repository hooks/config remain unchanged.
   The same real-Git fixture poisons `GIT_EXEC_PATH` with a credential-capturing
   `git-remote-https` plus every ambient execution/object/config/protocol/helper/
   loader/proxy override. Internal transport pins the setup-attested binary,
   exec path, helper identities, HTTPS policy, and minimal environment so no
   sentinel runs or sees a token; the ordinary Git control still uses the poison.
   All PR/workflow/check/artifact/cancel/ruleset/token operations use the one
   setup-attested absolute `gh` runner. A poisoned PATH executable, ambient GH/
   proxy/CA/loader/config state, malformed/oversized response, and forged actor
   cannot substitute code, capture credentials, or produce accepted evidence.
   Process inventory and argv/environment capture prove only the expected CLI
   runs, request bodies use stdin, tokens are absent from argv/stdin/logs, and
   runtime versus maintenance scopes remain noninterchangeable.
   Every list endpoint proves complete bounded pagination: page sizes 1 and 30,
   required evidence beyond page one, and the 256-shard artifact set all
   normalize without omission. Later-page failure, duplicate IDs, cycles,
   foreign Link targets, and exhausted bounds atomically reject the collection;
   policy repair/unfreeze, merge authorization, and audit exact-union completion
   remain blocked with no durable prefix.
   A second production-constructor fixture requires the maintenance App token
   to contain exactly Metadata-read and Administration-write for the same host/
   repository and no runtime permission. It proves runtime/maintenance
   credential noninterchangeability, short-lived refresh, descriptor/row/log
   redaction, and failure for absent, malformed, expired, inaccessible,
   overprivileged, or wrong-scope maintenance configuration.
   Static config/descriptor identity is the logical ownership key/name/template,
   while the shared binding registry is the only authoritative active provider
   ID and generation; pinned provider IDs are rejected.
   With `auto_install_after_epic: false`, an absent/disabled/stale supervisor
   fails `github-pr` startup; local mode remains valid. Stale heartbeat after
   startup blocks integration without activating local-merge fallback, and the
   OS-managed shim/monitor relaunch restores the claimant generation.
7. GitHub mode runs the configured local presubmit actions with bounded
   total/resource concurrency, then replaces the production
   `READY_FOR_REVIEW`/`DONE` local-QG merge path with durable candidate
   adoption. Malformed and duplicate handoffs, worker death, deleted worktree,
   missing remote ref, correction by a different worker, and external close/
   preempt/requeue on both sides of integration intent are covered. Exactly one
   of cancellation or integration wins; no remote-owned record reaches legacy
   recovery merge.
   Both installed start commands reject effective `github-pr` plus
   `--manual-integration` before daemon/worker/remote side effects. Local manual
   mode remains functional; a GitHub-to-local-manual restart cancels/requeues
   pre-intent records and resolves issued/ambiguous CAS before accepting work,
   so no passed remote record silently auto-integrates under the manual session.
   If rollback occurs with `integrated_policy_drift` before local sync, both
   ordinary and manual local modes are recovery-only and every legacy merge/FF
   path proves zero mutation until the shared freeze is cleared.
8. Exact evidence tests reject same-name checks, changed workflow blobs,
   reruns for another attempt, stale synthetic merge SHAs, target movement at
   the merge boundary, ambiguous integration responses, simultaneous policy
   mutation plus base movement at the CAS barrier, accepted CAS with lost
   response followed by descendant advancement/restart, and any inequality
   among reviewed, tested, and squash-result trees. Prepublication rejection also
   materializes corrections from the retained local adoption ref.
   Separate unchanged-target barriers prove policy drift before the last read
   blocks integration; a newly enforced restriction makes the provider reject;
   and removal or bypassed mutation in the unavoidable post-read race produces
   an integrated commit plus exact `integrated_policy_drift` evidence, freezes
   later integrations, and reconciles only through the setup path. The test
   never claims that a nontransactional policy read prevented that mutation.
   The reconciliation case uses the real typed project config, supervisor
   descriptor, external monitor claim loop, GitHub ruleset adapter, runtime
   post-verification, startup recovery, and dispatcher unfreeze path. It
   restarts across provider update and evidence commit, rejects foreign/
   organization/unmarked rules, and proves no fake-only callback or manual
   operator step is needed for the marked Oro-owned ruleset.
   It deletes the active ruleset, requires GitHub to return a new ID, loses the
   create response, injects a duplicate exact marked instance and crashes around
   deduplication/binding commit. Recovery retains one maintenance-created exact-
   template instance, increments the binding generation atomically, resolves
   stale cached IDs through the ledger, verifies policy, and only then unfreezes.
   Killing monitor/shim during that request proves OS-service relaunch, lease
   reclaim, and eventual unfreeze; no dispatcher path can construct the
   Administration-scoped reconciler.
   At the exact post-CAS/pre-local-sync drift barrier, a restart in local mode
   retains supervisor and credential references, repairs policy, proves the
   integrated squash on the authoritative current descendant, fast-forwards
   clean local state, closes remote ownership once, and only then admits local
   work. Dirty/divergent local state remains frozen and preserved.
   A multi-owner variant has drift/issued participants on main and an epic or
   custom target, shares policy repair where valid, synchronizes each attempt
   separately in reverse order across restart, and proves only the final
   generation-checked `NOT EXISTS` transition unfreezes the project.
9. Every auditor cycle dispatches or adopts exactly one full mutation campaign
   for its audit ID and SHA, survives restart, validates every shard artifact,
   independently reconstructs the persisted eligible-unit inventory, proves
   shard manifests are its exact nonoverlapping union, distinguishes
   infrastructure failure from surviving/valid-no-mutant outcomes, and creates
   deduplicated repair beads before a successful audit completion.
   The provider capability supplies the matrix-entry bound; the deterministic
   planner packs 257-plus natural partitions into no more than that bound while
   preserving unit-level exact-union proof. Boundary fixtures cover 255/256/257,
   a much larger inventory, an overlarge configured request, restart-stable
   packing, and workflow/fake rejection of any injected over-bound plan.
   The exact runtime App proves Actions dispatch/cancel/read and artifact/log
   access through the isolated canary and permission-aware campaign fixture;
   correct actor with insufficient permission is auth/config failure.
   Startup and the immediate pre-dispatch barrier independently require the
   configured workflow to be present, enabled, and registered with
   `workflow_dispatch` on the repository's then-current default branch. Tests
   cover a custom/release audit target whose immutable snapshot is valid while
   the default branch is missing, disabled, trigger-ineligible, or changed, and
   prove failure creates neither an audit ref nor a workflow run.
   Effective repository and organization rules are evaluated for the concrete
   audit-ref name at startup, immediately before create/adopt, and immediately
   before deletion, including operation-specific restrictions and App bypass.
   Creation denial has no ref/run side effects; deletion denial durably retains
   the exact ref in visible `cleanup_pending` and self-heals on policy recovery.
   Dispatch uses a leased immutable audit branch at the exact snapshot SHA,
   binds the expected workflow path/blob and run ref/head/attempt, survives
   target/workflow movement and restart, and deletes the ref only after durable
   artifact incorporation.
10. Provider-neutral core packages compile without GitHub transport imports or
    direct `gh` execution; a deterministic GitHub adapter fake exercises every
    side effect including credential refresh/actor attestation, exact-old-SHA
    squash CAS, permission/capability/execution-limit enforcement, and
    reconciliation.

Epic verification command:

```text
Cmd: test "$(git branch --show-current)" = main && ./scripts/test_remote_gate_epic.sh && ./scripts/test_quality_gate.sh && ./scripts/quality_gate.sh
Assert: exit 0. The remote-gate harness uses `go test -json` and fails unless
every exact integration test emits a test-level `pass` event; exercises a real
local Git remote, attested real `gh`, and deterministic GitHub API fixture;
validates PR and full
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
    - risk: "The dispatcher requests squash integration after its evidence becomes stale."
      mitigation: "Oro revalidates head, base, workflow, review, and QG evidence, constructs a commit with the exact tested tree and base parent, and uses an exact-old-SHA leased GitHub ref transaction to reject any concurrent base movement atomically."
    - risk: "Automatic local fallback can still be slower than remote CI and one tool can individually exhaust memory."
      mitigation: "Fallback is serialized and memory-safe, but absolute memory safety needs OS-level resource control as a future capability."

  paper_tigers:
    - risk: "GitHub merge queue is unavailable for this user-owned public repository."
      reason: "The selected design does not depend on merge queue; preflight rejects any target whose effective policy requires it. For supported targets, exact evidence rejects head races and the exact-old-SHA squash ref transaction rejects base races atomically, after which the dispatcher automatically rebases/retries."
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
      ANSWER: After exact review and GitHub remote-QG evidence pass, the
      dispatcher supplies one exact-tree squash commit and GitHub atomically
      accepts it only if the target is still the tested base. Worker/WIP commits
      remain on preserved candidate refs but do not enter target history.
- [x] DECISION: Who owns PR creation, CI waiting, retry, merge, and cleanup?
      ANSWER: The dispatcher; the operator only observes surfaced state.
- [x] DECISION: Is GitHub or the local worker authoritative for portable QG in
      remote mode?
      ANSWER: GitHub exact PR merge evidence is authoritative; local runs only
      macOS checks or the explicitly memory-safe outage fallback.
- [x] DECISION: Who creates the final Git commit?
      ANSWER: The worker creates candidate commits. After exact GitHub CI and
      review evidence, the dispatcher constructs one commit with the tested
      tree and exact tested base as its sole parent; GitHub atomically accepts
      that commit through an exact-old-SHA leased target-ref compare-and-swap. The
      dispatcher fetches and fast-forwards local state after verifying tree
      equivalence. The PR remains the CI/audit object, but the PR merge endpoint
      is not used because it has no atomic expected-base parameter.
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
