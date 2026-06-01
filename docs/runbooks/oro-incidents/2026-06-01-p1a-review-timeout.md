# 2026-06-01 P1a Review Timeout Recovery

## Symptom

`oro-s08-p1a` repeatedly reached `ready_for_review`, then emitted
`escalation_failed` because the dispatcher could not notify the missing tmux
manager window, followed by `heartbeat_timeout`. The task remained
`in_progress` with no active assignment.

## Affected Task

- Task: `oro-s08-p1a`
- Branch: `agent/oro-s08-p1a`
- Worktree: `.worktrees/oro-s08-p1a`

## Evidence

- `oro logs` showed repeated `ready_for_review`, `escalation_failed`, and
  `heartbeat_timeout` events for `oro-s08-p1a`.
- `oro status --json` no longer listed `oro-s08-p1a` as an active assignment
  after timeout.
- Acceptance passed in the worker branch:
  `go test -C .worktrees/oro-s08-p1a ./pkg/cards/ -run '^TestSchema_AddsRelationTablesAndSessionID$' -count=1 -v`
- Package tests passed in the worker branch:
  `go test -C .worktrees/oro-s08-p1a ./pkg/cards/ -count=1`

## Suspected Root Cause

The implementation was reviewable, but the review/escalation path failed
operationally because the dispatcher attempted to notify `oro-oro:manager`,
which was not present. The worker then timed out before the dispatcher consumed
the review outcome and merge.

## Corrective Action

Supervisor recovery merges `agent/oro-s08-p1a` into `epic/oro-spec08` manually
after verifying the acceptance and package tests, then closes the task with the
merge commit evidence.

## Prevention

When an Oro worker repeatedly reaches review and times out with no active
assignment, verify the branch tests and merge state before re-queueing another
time. If the product branch is clean and the failure is only notification or
review consumption, perform a documented supervisor integration recovery.
