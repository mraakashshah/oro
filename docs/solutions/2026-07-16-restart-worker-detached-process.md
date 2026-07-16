# Restart Worker Left a Detached Worker Process Running

**Date:** 2026-07-16
**Component:** `pkg/dispatcher`
**Severity:** critical

## Symptom

Restarting a managed worker terminated its tracked process group but left a
worker-owned tmux server running. The server had created a new session, become
reparented, and continued a serialized quality gate after the replacement
worker started.

## Investigation

The existing process manager isolated each worker in a process group and killed
that group before replacing a duplicate worker ID. That contract covered normal
descendants but could not reach a child that called `setsid` and moved into a
new group.

The regression fixture also needed to prove ownership on the detached process
itself. A system `sleep` helper did not expose the inherited environment in the
Darwin process snapshot, so checking only its parent could make the safety
assertions vacuous. The final fixture re-executes the Go test helper, detaches
it, and has each owned and foreign control process report its own exact markers.

## Root Cause

Process-group membership is transient ownership evidence. A daemonized session
leader can escape that group while still belonging to the same Oro project and
worker. Without a second exact ownership identity, restart had no safe way to
distinguish that residual process from another project's worker.

## Solution

Production worker commands now receive normalized `ORO_SOCKET_PATH` and
`ORO_WORKER_ID` environment entries. `ExecProcessManager` serializes worker
lifecycle changes, terminates the tracked group, scans one bounded
environment-inclusive process snapshot for both exact markers, and performs a
bounded TERM-then-KILL cleanup before a same-ID spawn can proceed.

The stop command uses the same all-marker matcher. Bare roles, tool names, and
worktree substrings are not sufficient ownership evidence when scoped markers
are available.

## Prevention

Lifecycle tests for detached processes should make every positive and negative
control self-report its runtime ownership tuple. Cleanup code should require the
complete worker-and-project identity and retain the tracked process-group kill
as the fast first step.

## Related

- Task `oro-xsmn`
- [Ops Timeout Orphaned the Runtime Grandchild](2026-07-13-ops-timeout-orphaned-grandchild.md)
