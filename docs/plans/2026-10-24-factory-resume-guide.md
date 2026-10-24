# Autonomous factory: start here after the pause

Date: 2026-10-24. Status: implementation paused at the user's request.
This is the durable entry point for a session returning after an extended break.
Documentation preservation does not resume the implementation goal.

## Objective and working agreement

Build a trustworthy, fully autonomous, self-healing software factory. Routine
human coordination, approval, restarts, cleanup, and repair must not be required
for qualified workloads and failures. Keep unsupported failure modes explicit;
bounded failure is useful containment, but is not successful self-healing.

The user clarified the intended roles:

- **Astra is the architect:** owns system boundaries, contracts, consequential
  design decisions, architectural review, and architectural escalations.
- **Terra is the engineer:** owns implementation, debugging, tests, integration,
  and repair through delivery. Give Terra context and engineering authority;
  do not reduce the role to generating patches under constant supervision.
- **Oro is the execution system:** owns durable work state, scheduling,
  isolation, feedback routing, verification, delivery, and automatic recovery.

The recent work did not establish a limitation in Terra's engineering ability.
Its concrete failures concerned integration, fixture fidelity, and continued
manual coordination. The proposed experiment tests whether Oro can carry the
models' work through to dependable delivery. Record actual provider/model IDs,
versions, prompts, and settings before trials; these role names do not establish
an executable model configuration.

No blanket teardown/rebuild has been decided. Keep proven primitives and be
willing to simplify or replace orchestration. Prioritize a complete operating
loop over further general infrastructure expansion. Resolve prerequisites that
the real path requires; do not weaken existing isolation or acceptance contracts
to obtain a demonstration.

## Recover the workspace

- Repository: `/Users/as21/codehouse/oro`.
- Implementation worktree: `/Users/as21/codehouse/oro/.worktrees/factory-core`.
- Branch: `feature/factory-core`, pushed to `origin/feature/factory-core`.
- Implementation checkpoint: `f95030ca`; unfinished work, not a release.
- The worktree was clean at that checkpoint. Later documentation commits may
  follow it; inspect current Git state before editing.
- The primary checkout had user changes to `pkg/dispatcher/assignment.go` and
  `pkg/dispatcher/assignment.go.tmp`. Do not overwrite or clean them.
- At the pause, agents were completed/frozen and no test or lint session remained
  active. Recheck current processes and ownership rather than trusting stale PIDs.
- Do not launch the legacy swarm, resume the goal, or dispatch workers until the
  user explicitly resumes implementation.

Read the current `AGENTS.md` and invoke `using-skills` before taking action.
Initial read-only checks, from the implementation worktree:

```sh
git status --short --branch
git log -8 --oneline
git worktree list
git show --stat f95030ca
```

## Read these documents in order

1. This guide, including the proposed acceptance experiment below.
2. [Pause checkpoint](2026-10-24-factory-pause-checkpoint.md): unfinished repair
   and verification status.
3. [Unattended design](2026-10-23-unattended-factory-design.md) and
   [task contracts](2026-10-23-unattended-factory-tasks.md): detailed ownership,
   admission, budgets, execution, supervision, and qualification requirements.
4. [Core design](2026-10-23-factory-core-design.md),
   [usage](../runbooks/factory-core.md), and
   [first increment proof](../proofs/2026-10-23-factory-core.md): established
   trusted-adapter lifecycle and its narrower evidence boundary.
5. [Full Oro check-runtime inventory](../proofs/2026-10-24-factory-check-runtime-inventory.md)
   and [VM-owner research](../proofs/2026-10-24-factory-owned-vm-research.md).
   Both are preserved research snapshots with unresolved work, not completed
   provisioning or recovery qualifications.
6. [Preserved native evidence](../proofs/2026-10-24-factory-native/README.md).

The older local handoff is
`docs/handoffs/2026-10-24_14-42_paused-factory.yaml`. Handoffs are ignored and may
be missing on another machine. This tracked guide and linked tracked documents
must remain sufficient to resume without that file or the chat transcript.

## What the code actually does

`cmd/oro/cmd_factory.go` exposes experimental `factory run/status` and hidden
host/Docker helpers. `factory.Run` in `pkg/factory/engine.go` constructs
`newLegacyDeliveryBackend`; the actual controller uses an explicit backend for
effects, but public managed start/supervision/delivery is not yet integrated.
The legacy adapter executes trusted host commands and is not the managed sandbox.

The private managed source path exists in `execution_docker_source.go` and
`managed_docker_source.go`. It composes source-volume creation, keeper creation,
READY, running inspection, one-shot stdin-tar seeding, and continuity inspection.
Journal phases preserve original deadlines and deny uncertain start/copy replay.
Component tests use recording runtimes; they are not a public real-model proof.

The design's `scripts/qualify_factory.sh` was **not created** at the pause.
The intended command is a future acceptance target, not an available passing run:

```sh
./scripts/qualify_factory.sh --docker-context colima-oro-factory --iterations 10
```

## Evidence boundary and immediate unfinished repair

- `bc4dca68` qualifies the actual production image builder and resulting watchdog
  on the dedicated Docker backend: UID1000 writes, denied watchdog attacks,
  detached-writer progress, original-deadline exit, unchanged sentinel, and exact
  owned-resource removal. Root independently checked absence. This does not
  qualify managed source preparation, controller integration, or the full factory.
- `fb10eb4a` records the native proof and the integration mismatch it exposed.
- `f95030ca` saves source preparation and the unfinished inherited-label repair.
- Source preparation passed its affected recording-runtime race suite in
  86.339 seconds before the label repair; independent verification also passed.
- The narrow label race regression passed in 5.410 seconds. The final focused
  non-race checkpoint regression passed in 1.410 seconds; package lint reported
  zero issues. Combined post-repair regression, coverage, and independent repair
  review remain outstanding. These are historical results, not fresh guarantees.

**Known integration failure:** Docker inherits the image's
`dev.getoro.factory.guard-build` label. Real containers therefore carried seven
labels, while the production keeper expected exactly six ownership labels.
The native fixture copied inherited labels, but recording fixtures omitted them;
both could pass while the production consumer would reject the real container.

The saved repair preserves six mandatory ownership values, then atomically binds
the complete bounded observed label map and CreatedAt after the first acknowledged
exact-ID/image stopped inspection. Later inspection, replay, and cleanup compare
the whole bound map exactly. Do not replace exact comparison with a subset check
or allow adoption of an unknown create result.

After implementation is resumed, first:

1. Check malformed raw label values, including JSON `null`; decoding into
   `map[string]string` may normalize null into an empty value. Test behavior before
   changing it. Preserve full-map exactness, required labels, and atomic rollback.
2. Independently review the repair and run combined source/keeper/volume/cleanup
   regressions with race detection and coverage. Expand coverage to the changed
   journal and replay boundaries; do not accept only the narrow reproducer.
3. Verify actual zero/default HostConfig fields for the real no-scratch keeper.
   Omitted Tmpfs is an **unverified possibility**, not a confirmed bug. The native
   image fixture had scratch configured; inspect real evidence before editing.
4. Exercise production source preparation through the trusted HostRuntime and
   actual managed consumer, then integrate candidate execution, stopped export,
   managed supervision/controller, and independent acceptance. A public test-only
   caller is not production integration.

Useful existing test entry points are `TestManagedKeeperInheritedLabels`,
`TestManagedSourcePrepare`, `TestManagedSourceKeeperCreate`,
`TestManagedDockerVolumeCreate`, `TestManagedDockerVolumeIntent`,
`TestManagedCleanupAuthority`, and `TestManagedCleanupScopeIntegrity` in
`pkg/factory`. The broader package remains the regression boundary. The native
image test is `cmd/oro-factory-image/native_test.go:TestGuardImageNative`; it is
opt-in and requires reviewed, pinned inputs. Missing prerequisites or a skipped
native test must never be counted as qualification.

## Other prerequisites that must not be rediscovered

The Go/watchdog image is not the full Oro check environment. The preserved
inventory identifies offline Linux dependencies/tools, a frozen vulnerability
database, incomplete Python locking, synthetic Git metadata, generated assets,
bounded writable gate state, and mandatory checks that can otherwise silently
skip. Candidate-owned scripts and tests cannot be the only acceptance authority.
Full repository checks require a trusted frozen policy and independent inputs.
Payload, inode, memory, and time requirements remain unmeasured.

Unknown Docker/VM outcomes also remain a recovery problem. Existing external
Colima state is not an owned VM epoch. VM-owner research proposes a narrow direct
Virtualization.framework helper experiment; it has not been implemented or run.
Successful force-stop completion and Stopped on the retained object are distinct
from a stop request, missing process, heartbeat, lock, or disk reopen. Owner-helper
death and host failure are unresolved. Keeping uncertain resources charged is
honest containment; it is not completed automatic recovery. Do not expand into a
general VM framework without showing which required recovery path needs it.

Historical task inventory, to reconcile against the tracker after resumption:

| Task | Pause state / relevance |
| --- | --- |
| `oro-mf-keeper-image-labels` | In progress; repair saved, final acceptance pending |
| `oro-mf-source-prepare` | In progress; recording tests, native production path pending |
| `oro-mf-guard-image-native` | In-progress tracker entry; accepted scoped native evidence exists |
| `oro-mf-docker-storage` | In progress at pause |
| `oro-mf-ready-helper-config` | In progress at pause |
| `oro-mf-backend-seam` | In progress at pause; legacy controller seam wired |
| `oro-mf-docker-scope` | Remaining integration/recovery work |
| `oro-mf-managed-delivery` | Remaining managed delivery work |
| `oro-mf-cli-start` | Remaining public managed start/supervision work |
| `oro-mf-qualification` | Remaining end-to-end qualification |
| `oro-factory-autonomy` | Parent objective remains unachieved |

This table is a handoff snapshot, not live task status or an instruction to close
tasks. Scope accepted component evidence separately from full task acceptance.

## Proposed Astra/Terra delivery experiment

This experiment was discussed after the pause. It has not been implemented or
run. It is a diagnostic milestone within the full goal, not permission to replace
the existing qualification suite with three easy tasks.

### Trial contract and roles

Choose three actual Oro changes: a reproducible bug, a small feature, and a change
crossing module boundaries. Exact task IDs are not selected yet. Use pinned clean
base revisions, private delivery branches, and disposable copies of the real
repository. Freeze objective, independently validated behavioral expectations,
required repository checks, forbidden modifications, attempt/time/spend limits,
and expected outcomes before running models. Validate the acceptance oracle
against known broken and working behavior. Keep some cases held out while giving
Terra actionable failure diagnostics. Record and diagnose baseline failures;
do not silently waive required checks during a trial.

Astra produces the architecture and implementation contract. Terra implements,
tests, debugs, integrates, and repairs. Oro routes implementation failures to
Terra and architectural uncertainty to Astra automatically. A fresh review context
and independent frozen acceptance evaluate the exact candidate. Neither role may
weaken the frozen evaluation criteria. Record exact model settings and every
automatic escalation.

Required loop:

```text
objective -> Astra architecture -> Terra engineering -> independent acceptance
    -> actionable repair / architectural escalation -> recheck -> exact delivery
```

### Clean and faulted trials

First deliver one real change with no fault. Restore the original base for each
subsequent trial; rerunning an already completed objective is not recovery proof.
Then replay with one failure at a time. A separate driver injects faults at
observed lifecycle boundaries, records the event, and only observes afterward.

| Fault | Required result |
| --- | --- |
| Kill Terra while working | Retire old execution; resume/retry automatically within original budgets |
| Kill controller during an active invocation | Supervisor restores it; reconcile durable state; retire old work before replacement |
| Kill supervisor | External service manager restores it; the same ownership rules hold |
| Temporarily interrupt model service | Retry after recovery without lost work or budget/deadline resets |
| Reject a genuinely incorrect candidate | Terra repairs using feedback and resubmits without human instruction |
| Inject a recoverable environment failure | Automatically diagnose and repair/recreate environment, then deliver |
| Kill after actual Git promotion before durable completion | Observe and record the original exact delivered commit; no duplicate delivery |
| Exhaust budget / use impossible acceptance | Bounded truthful failure; no false success or unauthorized branch change |

The driver must not restart Oro, repair files, provide missing instructions, or
clean resources on Oro's behalf. Every human intervention is recorded as a failed
autonomous trial, including intervention by the coordinating coding assistant.
Impossible tasks and deliberately permanent outages have expected failure
outcomes; keep their containment results separate from recoverable-fault delivery.

### Grade and diagnose

Successful delivery requires fresh-checkout behavioral acceptance of the exact
delivered SHA, every mandatory check observed passing, no human rescue, no old
execution or conflicting work left behind, and original budgets respected.
Record all trials, including failures: task/base/candidate/delivered SHA, harness
revision/binary digest, frozen policy/runtime identities, model settings, fault
boundary, transitions, attempts, escalations, checks/skips, elapsed time, spending,
cleanup evidence, and interventions. Preserve bounded evidence outside temporary
storage. A green transcript or reviewer statement alone is not acceptance.

Keep a small matched reference using Astra and Terra directly with automated
grading, the same frozen tasks and comparable budgets. It isolates factory
overhead and orchestration failures; it does not replace production-path trials.
Report delivery and fault-recovery rates separately, plus false success, human
interventions, elapsed time, retries/escalations, and total cost per accepted
change. Model capability is not presumed to be the cause of an Oro failure.

First decision gate: one real change delivered unattended, then the same starting
task delivered through a controller crash with no manual restart or cleanup.
Expand to the other changes, repeated trials, the full existing ten-scenario
qualification, sustained queues, automatic intake/decomposition/repair, and Oro
delivering and qualifying changes to itself. Pin the running factory during each
trial; qualify a newly delivered factory version separately before using it as
the next runner. Passing a small pilot is not evidence for two-week reliability,
host/logout recovery, or live dispatcher cutover.

## Suggested return-session prompt

> Read AGENTS.md and use the required skills. Read
> docs/plans/2026-10-24-factory-resume-guide.md and its linked checkpoint before
> editing. Report current Git/task state and distinguish proven behavior from
> unqualified integration. Implementation remains paused unless I explicitly
> resume it. On resumption, retain Astra as architect and Terra as engineer;
> prioritize one real unattended delivery and controller-crash recovery through
> the production managed path. Do not substitute recording tests, toy fixtures,
> manual rescue, or bounded containment for autonomous delivery/self-healing.
