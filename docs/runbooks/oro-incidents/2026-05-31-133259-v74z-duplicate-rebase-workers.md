# 2026-05-31 v74z duplicate rebase workers

## Symptom

The `oro-v74z` epic close created two duplicate rebase children,
`oro-7yq9` and `oro-k8sb`, both titled `Rebase epic/oro-v74z onto main`.
Both workers stayed assigned for more than 15 minutes with fresh heartbeats but
no task-state progress events.

## Affected State

- Epic: `oro-v74z`
- Duplicate rebase children: `oro-7yq9`, `oro-k8sb`
- Workers:
  - `worker-1780185474091435000-0` assigned `oro-7yq9`
  - `worker-1780230918935217000-0` assigned `oro-k8sb`
- At `2026-05-31T13:32:59Z`, `oro health --json` still reported healthy
  posture, but `last_event_age_secs` was about `1059` while both workers were
  busy.

## Evidence

- Open tasks: `oro-v74z`
- In progress tasks: `oro-k8sb`, `oro-7yq9`
- Ready queue: `0`
- QG incidents: `0`
- Active quality gate owner:
  - `pid=84370`
  - `repo=/Users/as21/codehouse/oro`
  - `created_at=2026-05-31T13:31:59Z`
- Branch topology observations:
  - `main...agent/oro-7yq9`: `0 26`
  - `main...agent/oro-k8sb`: `0 26`
  - Earlier branch tips had identical trees and both had `main` plus
    `epic/oro-v74z` as ancestors.
- Worktrees present:
  - `/Users/as21/codehouse/oro/.worktrees/oro-7yq9`
  - `/Users/as21/codehouse/oro/.worktrees/oro-k8sb`

## Suspected Root Cause

The dispatcher created duplicate action rebase children for the same epic
merge failure. The workers independently performed the same recovery shape and
then serialized full quality gates through the repo-scoped QG lock. Heartbeats
continued, so health stayed green even though the task graph was no longer
shrinking.

## Corrective Action

Stop the factory using Oro's own stop command, preserve the worker branches,
verify one recovered branch independently, advance `epic/oro-v74z` and `main`
only after verification, close the duplicate rebase child as superseded, then
close the epic after its acceptance command passes on `main`.

## Prevention

The dispatcher should avoid creating duplicate rebase children for the same
epic merge failure, or it should retire patch-equivalent duplicate rebase
children once one verified branch is integration-ready.
