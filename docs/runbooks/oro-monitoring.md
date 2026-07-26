# Oro Monitoring Runbook

Use this runbook when actively operating an Oro swarm. The operator keeps the
factory moving, catches systemic failures quickly, files tracked bugs, and
rebuilds/restarts when a runtime fix lands.

## Baseline Cadence

Run an immediate snapshot after launch, restart, merge, QG failure, review
transition, user ping, or any unusual event.

For each snapshot:

```bash
oro status
oro directive status | jq '{state,pid,queue_depth,target_count,max_workers,worker_count,active_count,idle_count,pending_worker_count,workers}'
oro logs --tail 120 | grep -v heartbeat | grep -v directive | grep -v missing_accept | tail -50
```

Cadence:

| State | Cadence | Exit Criteria |
| --- | --- | --- |
| Fresh launch or restart | Immediate, then every 60s for 2 cycles | Workers active or queue empty; no new high-severity events |
| Active incident | Every 60s | Defect fixed, rebuilt, restarted, and clean for 2 cycles |
| Stable run | Every 2-5 minutes | Change in worker state, merge, QG failure, review, or user ping |
| Context pressure above 40% | Summary-only | Report changes and incidents only |

Do not let an active incident drift to stable cadence because workers still
heartbeat. A recovered dispatcher loop can keep workers alive while repeatedly
failing background work.

## Panic Policy

Any `goroutine_panic`, `panic:`, or fatal dispatcher/worker stack trace is an
active incident.

Required response:

1. Capture the full stack and `restart_count`.
2. Check whether the same stack or `restart_count` repeats in the next 60s.
3. File or find a bug task with exact stack top, affected loop, and acceptance
   criteria.
4. Keep 60s incident cadence until the fix merges.
5. After the fix merges, stop Oro, rebuild/install, restart, and verify the new
   dispatcher PID is running the fixed binary.
6. Confirm no fresh panic for 2 consecutive 60s windows before returning to
   stable cadence.

Important: safeGo recovery is not resolution. If `restart_count` increases, the
running binary is still defective and must be replaced after the fix lands.

## Stuck-State Policy

Escalate to active incident cadence when any of these occur:

| Signal | Threshold | Action |
| --- | --- | --- |
| Workers idle with ready queue | 2 consecutive checks | Inspect `oro task ready`; restart dispatcher if assignment is stuck |
| Workers busy but no productive closures and queue not shrinking | 4 consecutive checks | Treat as throughput stall; inspect ready/closed/log snapshots, then restart or intervene on the repeated task/QG blocker |
| Same assigned task set | 4 consecutive checks | Treat as retry churn even if heartbeats are fresh; inspect AC, QG notes, and worker output |
| Same task and no progress | 3 checks or 15 minutes | Inspect context, worker logs, and worktree diff |
| `QG_FAILED` same fingerprint | 3 repeats | Classify as deterministic, flaky, or infrastructure; file/fix if systemic |
| Merge conflict without resolution | Next check after conflict | Inspect branch/worktree; preserve work before cleanup |
| `update_status_failed` or DB write error | Any repeat | Stop swarm before store spam; inspect SQLite schema/state |
| `goroutine_panic` | Any occurrence | Follow Panic Policy |

For unattended runs, use the supported CLI monitor so this policy is enforced
continuously and survives daemon restarts:

```bash
oro monitor --target 2 --max-workers 2 --interval 60s
```

Default mode is observe-only: it prints health findings and recommended actions
without changing dispatcher state. Add `--act` only when the operator wants the
monitor to maintain the requested worker count and restart the daemon after
repeated stalled or unsafe findings. It resumes a paused ready queue only when
the durable monitor ledger proves this monitor issued the matching QG-recovery
pause; explicit operator pauses remain paused:

```bash
oro monitor --target 2 --max-workers 2 --interval 60s --act
```

The previous skill-local Python autopilot and
`scripts/oro-factory-watchdog.sh` are deprecated operator aids. Prefer
`oro health`, `oro status --json`, and `oro monitor` for supported health
policy and recovery actions.

## QG Failure Policy

QG failure handling is classification-first. A failed quality gate never
authorizes merge, but it also does not automatically mean "create a fresh P0
for this task." First identify whether the failure belongs to the original task
or to factory infrastructure.

Classification decisions:

| Class | Operator policy |
| --- | --- |
| `worker_deterministic` | Keep the work on the original task. Retry while attempts remain; after exhaustion, reopen the original task with the QG fingerprint, output hash, latest branch/worktree, and review state. Do not create a new P0 by default. |
| `systemic` | Create or reuse one infra bug keyed by the QG fingerprint. Use P0 only when it blocks multiple tasks, main/epic QG, or factory throughput. Link every affected task as evidence. |
| `flaky` | Back off and rerun before assigning more coding work. If the same flaky fingerprint recurs enough to block throughput, create or reuse the infra bug and link affected tasks. |
| `transient` | Back off and retry without burning all worker-fix attempts. Create tracked work only after recurrence or sustained throughput impact. |
| `impossible` | Update the original task: fix acceptance criteria, add missing dependency details, or block/replan it. Do not convert impossible AC into a random QG P0. |
| `unknown` | Stop for triage after repeated failure. Keep the original task visible with evidence; create infra work only after the failure is classified or recurrence shows factory impact. |

Create a P0 infra bug when the classified failure is infrastructure and at
least one of these is true:

- The same fingerprint affects unrelated tasks.
- The failure reproduces on `main`, an epic branch baseline, or a clean QG
  worktree.
- QG cannot run because tooling, scripts, the store, process control, or the
  runner environment is broken.
- Main/epic QG, merge safety, or overall factory throughput is blocked.

Do not promote a failure to P0 solely because retry attempts were exhausted. For
single-task deterministic failures, the operator should inspect and continue the
original task.

## Inspecting Affected QG Tasks

Until a dedicated `oro qg incidents` command exists, use status, events, task
metadata, and logs as the incident view.

```bash
oro status --json | jq '{qg_failure_incidents_open,qg_failure_occurrences_30m,qg_failure_top_fingerprints}'
oro logs --tail 300 | grep -E 'qg_failure_|quality_gate_|QG_FAILED|qg_failed'
oro task show <task-id> --json
oro task show <infra-bug-id> --json
```

If the JSON status fields are absent in an older binary, fall back to event/log
inspection and task notes. Capture:

- QG fingerprint or normalized failure summary.
- Class, confidence, and policy decision.
- Affected task IDs and statuses.
- Component: worker, dispatcher pre-merge, epic QG, or standalone `oro work`.
- Branch, worktree, assignment ID, and worker ID when available.
- Representative output hash or short excerpt.

For deterministic failures, inspect the original task's worktree/branch before
cleanup. Preserve rejected work whenever it contains useful fixes or review
feedback. For systemic/flaky failures, add the affected task list to the infra
bug instead of filing one bug per task.

## Legacy QG P0 Cleanup

Older Oro builds created tasks titled like `P0: QG exhausted for <task>` for
many retry-exhausted failures. Clean them up only after confirming whether they
are duplicates of a classified incident or actually contain unique work.

Cleanup procedure:

1. Find the legacy P0 and the original task named in its title/body.
2. Compare the QG output, fingerprint if present, affected branch/worktree, and
   close reason against any open infra incident.
3. If the legacy P0 is a duplicate of an open infra incident, add its evidence
   to that infra bug and close the duplicate as superseded.
4. If the failure is worker-deterministic and belongs to the original task,
   move any useful output or branch details back to the original task and close
   the legacy P0 as duplicate/no longer policy.
5. If the legacy P0 contains unique systemic evidence with no incident yet,
   convert or retitle it as the fingerprint-keyed infra bug instead of closing
   it.

Closed recurrence policy:

- If the same fingerprint recurs after its infra bug was closed as fixed,
  reopen the infra bug when the output is materially the same.
- Create a recurrence child only when the new output shows a changed root cause
  or the original fix no longer describes the failure.
- Do not reopen closed duplicate P0 tasks; link them from the active incident as
  historical evidence.

## Fix And Relaunch

When a runtime bug in Oro itself is fixed:

```bash
ORO_HUMAN_CONFIRMED=1 oro stop --force
make install
oro start --workers <previous-target> --max-workers <previous-max> --detach
oro status
```

Use the installed binary path if repo-local `./oro` hits the re-exec guard.

After relaunch:

1. Record old PID, new PID, worker count, queue depth, and active assignments.
2. Check logs from the new start time only.
3. Verify the original failure signature is absent for 2 watch windows.
4. Update `docs/monitoring-report.md` with the defect, bug ID, commit, verification,
   and relaunch status.

## Reporting Format

Normal cycle:

```text
[22:20] OK — PID 60674, 2 active, queue 40, tasks oro-zc58/oro-4rxk, no new panics
```

Incident cycle:

```text
[22:17] INCIDENT — goroutine_panic restart_count 66 in retryOversizedBead.
Action: bug oro-nft6 exists; keep 60s cadence until merged and restarted.
```

Post-fix restart:

```text
[22:20] RESTARTED — old PID 36168 replaced by 60674 from commit a9d91b35.
No panic in first 60s window; one more clean window required.
```
