# Startup cache sweep outlasted daemon readiness

**Date:** 2026-08-07
**Component:** Oro startup readiness and developer-cache maintenance
**Severity:** critical

## Symptom

`oro start --workers 3 --detach` reported a daemon PID, then failed with the
following error after 15 seconds (the socket path is normalized here):

```text
dispatcher socket not ready at <ORO_HOME>/projects/<project>/oro.sock: dial unix ...: connect: no such file or directory
```

The daemon was no longer running, no dispatcher socket or workers existed,
and the storage catalog retained a `pause_requested` epoch. Factory health
could therefore appear unsafe after a startup command that had already
reported `Daemon started`.

## Investigation

Phase 1 was read-only. It established all of the following before any retry or
catalog repair:

- The parent process killed the daemon when its 15-second socket wait expired.
- The daemon synchronously entered the weekly developer-cache sweep before
  opening the dispatcher state database and socket.
- The sweep completed the Go and uv providers, freeing 1,996,477,408 bytes in
  total, but its golangci-lint provider remained recorded as `running`.
- The catalog contained one `pause_requested` epoch and no live controller,
  acknowledgement, or active runtime lease that could finish or release it.
- The daemon log ended at startup; it did not contain an application panic or
  a dispatcher error.

The evidence ruled out SQLite lock contention, a stale socket, and worker
admission as the first failure. The daemon was killed while still performing
pre-socket maintenance.

## Root cause

Two independently bounded operations had incompatible deadlines. The startup
cache sweep had a 20-second budget in `cmd/oro/db.go:42`, while the parent
waited only 15 seconds for readiness in `cmd/oro/cmd_start.go`. Because the
sweep ran synchronously before the socket was created, a valid cleanup could
outlive the parent deadline. The parent's normal orphan cleanup then killed
the daemon.

Killing the process also exposed a second durability gap. The weekly sweep had
already acquired a global pause and written provider evidence, but startup had
no recovery transaction for a `running` sweep whose controller disappeared.
The next run could observe a future due date and return early while leaving
factory admission paused indefinitely.

Several assumptions proved unsafe:

- Treating maintenance errors as warnings did not make synchronous maintenance
  non-blocking.
- A context deadline did not guarantee every external cleaner returned before
  the parent readiness deadline.
- Advancing the next-due schedule did not complete the sweep or release its
  admission pause.
- The newest pause epoch could not be assumed to belong to the interrupted
  cleanup; an operator may have requested a later, unrelated pause.

## Solution

Startup readiness now covers the entire pre-socket maintenance budget plus a
bounded five-second margin (`cmd/oro/cmd_start.go:263`).
`TestStartupReadinessCoversDevCacheSweep` exercises both sides of that
contract: a live delayed Unix socket beyond the old boundary must succeed, and
a never-ready daemon must still be killed exactly once.

`RunWeeklyDevCacheSweep` now reconciles interrupted work under the maintenance
lock before checking whether the next sweep is due
(`pkg/storage/dev_schedule.go:73`). The reconciliation transaction:

1. Selects unfinished `weekly-dev-cache-*` provider sweeps.
2. Correlates each sweep to exactly one `pause_requested` epoch using the
   shared sweep start and pause creation timestamp, failing closed if the
   relationship is missing or ambiguous (`pkg/storage/dev_schedule.go:253`).
3. Leaves state unchanged while an identity-matched controller or active
   runtime lease exists.
4. Uses compare-and-swap updates to mark each `running` sweep `failed`, records
   durable interruption evidence, and opens only the correlated pause epoch in
   the same transaction (`pkg/storage/dev_schedule.go:134`).

The tests cover missing and ambiguous correlation, later unrelated pause
epochs, live owners, leases, failed compare-and-swap updates, evidence
collisions, rollback, cancellation, and idempotent subsequent calls.

## Prevention

- Keep every parent readiness deadline strictly greater than the sum of all
  synchronous pre-readiness budgets, with a tested margin.
- Any operation that acquires durable admission state must define and test its
  recovery path before it can run during startup.
- Correlate ownership with durable identity; never repair the latest record by
  chronology alone.
- Test process death between each durable state transition, including an
  already-advanced schedule.
- Keep strict mutation ownership one function per shard so fail-closed checks,
  compare-and-swap guards, and rollback paths cannot silently lose coverage.

## Related

- Tasks: `oro-438k` (incident epic), `oro-eqmf` (readiness budget),
  `oro-2mwc` (interrupted-sweep reconciliation), and `oro-krpw` (strict
  mutation routing).
- PR #6 and CI runs `31132181030` and `31134624366` established the exact
  pre-incident base at `dc35746c`; both were green, showing that ordinary CI
  did not reproduce a long real cache cleanup.
- Mutation coverage: `pkg/storage/dev_schedule_mutation_test.go` and the
  startup-maintenance owner/sharding contracts in
  `scripts/quality_gate/mutation_remote_test.sh`.
- [Cache lifecycle discoveries](2026-07-29-cache-lifecycle-discoveries.md)
- [Storage lifecycle and shared-cache design](../plans/2026-07-19-storage-lifecycle-and-shared-caches-design.md)
