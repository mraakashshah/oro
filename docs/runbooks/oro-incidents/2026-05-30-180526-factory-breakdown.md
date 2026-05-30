# Factory Breakdown: recovery quarantine 83

Timestamp: 2026-05-30T18:05:26-0400

## Symptom

Oro dispatcher was running but health was unsafe because one recovery quarantine was open. Assignment was repeatedly blocked with `assignment_blocked_by_recovery_quarantine`, leaving eight ready tasks and zero active or idle workers.

## Affected Task

- Task: `oro-0wmt`
- Title: `Rebase epic/oro-6wxx onto main`
- Quarantine: `#83`
- Branch: `agent/oro-0wmt`
- Worktree: `.worktrees/oro-0wmt`

## Evidence

- `oro health --json` reported `recovery_quarantine_open` with recommended operator action.
- `oro recovery list` reported `#83 oro-0wmt stale_active_assignment`.
- `oro recovery inspect 83` reported a disconnected worker assignment and branch/worktree preservation state.
- `git -C .worktrees/oro-0wmt status --short --branch` showed only `?? quality_gate.sh`.
- `cmp -s scripts/quality_gate.sh .worktrees/oro-0wmt/quality_gate.sh` returned `0`, so the untracked file is identical to the tracked quality gate script and contains no unique source work.
- `git worktree list` showed preserved worktrees for `oro-0wmt`, `oro-d2nm`, `oro-p444`, `oro-txb6`, and `oro-v6k5`.

## Suspected Root Cause

The worker assigned to `oro-0wmt` disconnected after preserving its branch/worktree, leaving an active assignment quarantine. The dispatcher correctly blocked new assignments until an operator inspected preservation state.

## Corrective Action

Resolve quarantine `#83` with preserved requeue semantics after inspection, then verify Oro health and restart or resume workers only after the unsafe finding clears.

## Verification

After resolving quarantine `#83` with `oro recovery resolve 83 --mode requeue-preserved`:

- `oro recovery list` reported no open recovery quarantines.
- `oro health --json` reported healthy posture with no findings.
- `oro status` reported the dispatcher healthy before the later controlled restart.

## Prevention

When `oro health` reports `recovery_quarantine_open`, inspect the quarantine and worktree dirtiness first. If dirty files are generated duplicates with no unique content, record that evidence before resolving and requeueing.
