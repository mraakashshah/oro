# Parallel Dispatcher Tests Share Assignment State

**Date:** 2026-07-20
**Component:** `pkg/dispatcher` test infrastructure
**Severity:** high

## Symptom

Under nested quality-gate load,
`TestNoMultipleAssignmentsToSameBead` intermittently left `oro-test1` open with
no busy worker. Failure-only event diagnostics exposed:

```text
assignment_persist_failed: create assignment: constraint failed:
UNIQUE constraint failed: assignments.bead_id (2067)
```

The same stress run could also fail dispatcher startup with:

```text
stale socket check /tmp/oro-test-<timestamp>.sock:
another dispatcher is already running
```

Both tests passed immediately in isolation.

## Investigation

The nested serial-lane regression first failed once in ten runs. Repeating the
canonical guarded test set under concurrent load reproduced the assignment
failure four times in fifty runs. The event stream contained two startup
reconciliation summaries and assignments from separate tests in what should
have been one isolated in-memory database.

After database isolation was fixed, the same stress command exposed a Unix
socket collision. This ruled out the production assignment lock and worker
heartbeat paths: both resources were named from `time.Now().UnixNano()` and
parallel tests could observe the same host-clock value.

## Root Cause

`newTestDB` and `newTestDispatcher` used clock-derived identifiers for a
shared-cache SQLite DSN and a Unix socket path. `UnixNano` reports nanoseconds
but does not guarantee that concurrent calls receive unique values. On macOS
under load, separate dispatcher fixtures occasionally shared the same database
or socket.

When `TestReconnectDoesNotStealBead` and
`TestNoMultipleAssignmentsToSameBead` shared a database, both used bead ID
`oro-test1`. The first active assignment satisfied the database uniqueness
constraint; the second insert failed and correctly rolled its bead back to
`open`, making the test look like an assignment race.

## Solution

`pkg/dispatcher/dispatcher_test.go:newTestDB` and `newTestDispatcher` now name
resources with the process ID plus a package-level `atomic.Uint64` sequence.
This guarantees process-local uniqueness without weakening assignment or
liveness assertions. `pkg/dispatcher/concurrent_assign_test.go` also includes
the dispatcher event stream in final-state failures.

## Prevention

- Do not use timestamps as uniqueness guarantees for parallel test resources.
- Use a process ID plus an atomic sequence for shared-cache DSNs, sockets, and
  other process-local fixture names.
- When an isolated SQLite test reports unexpected rows, inspect startup-event
  multiplicity before changing production concurrency logic.
- Reproduce load-only dispatcher failures with the entire canonical serial-lane
  filter, because its `t.Parallel` tests expose cross-fixture collisions that a
  single-test run cannot.

## Related

- Task `oro-5sr4`
- `pkg/dispatcher/quality_gate_concurrency_test.go`
- `pkg/dispatcher/testdata/serial_lane_tests.txt`
