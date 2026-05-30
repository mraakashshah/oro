# Factory Breakdown: stale recovery skip after completed workers

Timestamp: 2026-05-30T18:17:57-0400

## Symptom

The dispatcher remained healthy according to `oro health --json`, but the queue stopped shrinking after two workers had produced completion summaries. `oro logs` continued emitting `recovery_quarantined_bead_skipped` for ready tasks even though `oro recovery list` reported no open recovery quarantines.

## Affected Tasks

- `oro-v6k5` on `agent/oro-v6k5`
  - Commit present: `15d3c727 fix(oro): hide unsupported task stubs`
  - Worker output reported focused tests and `./quality_gate.sh` passed.
  - Dispatcher event: `ready_for_review` at 2026-05-30 22:15:21 UTC.
- `oro-vnhr` on `agent/oro-vnhr`
  - Commit present: `f3083e39 chore(deps): tidy unused Go modules`
  - Worker output reported `go mod tidy -diff && go test ./...` and `./quality_gate.sh` passed.

## Evidence

- `oro status --json` showed both tasks still assigned after worker completion output.
- `oro ops list --json` showed no unresolved ops runs.
- `oro recovery list` showed `No open recovery quarantines.`
- `oro logs --tail 160` still showed repeated `recovery_quarantined_bead_skipped` events with reason `open_recovery_quarantine`.
- `git worktree list` showed preserved worktrees for both affected branches.
- `git -C .worktrees/oro-v6k5 status --short --branch` and `git -C .worktrees/oro-vnhr status --short --branch` showed only untracked `quality_gate.sh`, a worktree-local generated script.

## Suspected Root Cause

The dispatcher retained stale in-memory recovery-quarantine skip state after quarantine resolution. Because health and recovery commands read persistent state as clear, a controlled dispatcher restart should clear the stale in-memory skip state while preserving committed worker branches.

## Corrective Action

Perform a controlled factory restart:

1. Pause new assignments.
2. Stop the running dispatcher gracefully.
3. Start with `--web --detach --workers 2 --max-workers 2` so the dashboard is available.
4. Verify dashboard reachability at `http://127.0.0.1:4444`.
5. Verify health, task assignment, and that ready tasks are no longer skipped for a nonexistent quarantine.

## Verification

After installing the dashboard flag-forwarding fix and restarting the factory with `oro start --web --detach --workers 2 --max-workers 2`:

- `oro recovery list` reported no open recovery quarantines before restart.
- `oro status --json` reported PID `18131`, healthy posture, two active managed workers, and zero open QG incidents.
- `curl -i -sS http://127.0.0.1:4444/healthz` returned `HTTP/1.1 200 OK`.
- `oro dashboard` returned `http://127.0.0.1:4444`.
- `agent-browser open http://127.0.0.1:4444` opened the Oro Dashboard.

## Prevention

Every factory start should include `--web` and a dashboard reachability check, so operator state is visible immediately. If recovery skip events persist while `oro recovery list` is empty, restart the dispatcher after preserving branch/worktree evidence.
