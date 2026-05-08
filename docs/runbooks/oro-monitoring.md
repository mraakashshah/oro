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
| Same task and no progress | 3 checks or 15 minutes | Inspect context, worker logs, and worktree diff |
| `QG_FAILED` same fingerprint | 3 repeats | Classify as deterministic, flaky, or infrastructure; file/fix if systemic |
| Merge conflict without resolution | Next check after conflict | Inspect branch/worktree; preserve work before cleanup |
| `update_status_failed` or DB write error | Any repeat | Stop swarm before store spam; inspect SQLite schema/state |
| `goroutine_panic` | Any occurrence | Follow Panic Policy |

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
4. Update `monitoring report.md` with the defect, bug ID, commit, verification,
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
