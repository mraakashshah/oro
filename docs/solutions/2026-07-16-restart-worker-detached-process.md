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

The first bounded-scan follow-up still failed with
`inspect process environments: context deadline exceeded`. It listed candidate
PIDs first, then launched `ps axeww -p <pid>` concurrently for each candidate.
On Darwin, the BSD `a` and `x` selectors expand the selection to the full live
process table despite `-p`, so every nominally PID-filtered inspection repeated
the complete environment scan.

## Root Cause

Process-group membership is transient ownership evidence. A daemonized session
leader can escape that group while still belonging to the same Oro project and
worker. Without a second exact ownership identity, restart had no safe way to
distinguish that residual process from another project's worker.

The acceptance regression persisted because the scanner combined mutually
counterproductive `ps` selectors. Concurrency did not bound the work: it
multiplied a full environment-inclusive table scan by the number of live
candidate PIDs until the shared four-second cleanup context expired.

## Solution

Production worker commands now receive normalized `ORO_SOCKET_PATH` and
`ORO_WORKER_ID` environment entries. `ExecProcessManager` serializes worker
lifecycle changes, terminates the tracked group, scans one bounded
set of environment-inclusive process snapshots for both exact markers, and
performs a bounded TERM-then-KILL cleanup before a same-ID spawn can proceed.

The bounded scanner takes one lightweight current-user PID/PGID snapshot, then
reads each candidate's environment with a delimiter-preserving OS source:
`/proc/<pid>/environ` on Linux and Darwin `kern.procargs2` when cgo is
available. Unsupported or unavailable readers fail closed instead of
reconstructing environment entries from whitespace-delimited process output.
Argument text stays separate from environment entries, so a foreign process
cannot spoof ownership through command-line strings. Processes that exit
between snapshots are skipped only after their absence is verified, canceled
contexts return a bounded error, and incomplete socket/worker marker tuples are
rejected before either scan.

The stop command uses the same all-marker matcher. Bare roles, tool names, and
worktree substrings are not sufficient ownership evidence when scoped markers
are available.

## Prevention

Lifecycle tests for detached processes should make every positive and negative
control self-report its runtime ownership tuple. Cleanup code should require the
complete worker-and-project identity and retain the tracked process-group kill
as the fast first step.

When changing BSD-style `ps` selectors, verify the number of returned rows as
well as the presence of expected fields. A command can expose the right marker
while silently ignoring its intended PID filter.

## Related

- Task `oro-xsmn`
- [Ops Timeout Orphaned the Runtime Grandchild](2026-07-13-ops-timeout-orphaned-grandchild.md)
