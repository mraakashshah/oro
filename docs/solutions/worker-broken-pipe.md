# Worker Broken Pipe During Assignment

**Date:** 2026-05-29
**Component:** worker, dispatcher worker pool
**Severity:** high

## Symptom

Workers could be declared unreachable during the first assignment window even though
the runtime process eventually started and emitted `STATUS running`.

The observed chain was approximately 36s:

1. Dispatcher sent `ASSIGN`.
2. Worker entered `handleAssign` and blocked inside runtime spawn.
3. No heartbeat was sent during the blocked spawn window.
4. Dispatcher hit its heartbeat timeout path and treated the worker as dead.
5. Worker later reconnected or sent worker-originated `STATUS`.
6. Dispatcher-to-worker writes could still hit a stale connection and return a
   broken pipe / `WorkerUnreachableError`.

The important distinction is directionality. A worker-originated `STATUS running`
only proves the worker can write to the dispatcher. It does not prove the
dispatcher can still write to that worker. The regression must prove
dispatcher-to-worker write reachability after the assignment and reconnect window.

## Investigation

The failure crossed both worker and dispatcher boundaries:

- `pkg/worker/worker.go:Run` previously restarted itself recursively after
  reconnect, creating nested run loops and extra initial-heartbeat behavior.
- `pkg/worker/worker.go:handleAssign` performed runtime spawn synchronously, so a
  slow spawn starved periodic heartbeat delivery before `watchContext` started.
- `pkg/dispatcher/dispatcher.go:connCloseCleanup` needed to ignore cleanup for a
  stale connection if the same worker ID had already registered on a newer conn.
- `pkg/dispatcher/worker_pool.go:sendToWorker` is the critical reachability proof:
  a successful worker `STATUS` is not enough; a dispatcher-originated write must
  reach the current conn with `pendingMsgs` still empty.

## Root Cause

Three smaller liveness bugs combined into the broken-pipe symptom:

1. **Spawn heartbeat starvation:** before the subprocess existed, normal
   `watchContext` heartbeats had not started. Slow runtime spawn could exceed the
   dispatcher heartbeat timeout, so the dispatcher considered the worker dead
   while the worker was still starting the assignment.
2. **Reconnect loop nesting:** reconnect success called `Run` recursively instead
   of replacing read channels in the existing event loop. That leaked event-loop
   structure and produced extra reconnect-time heartbeat behavior.
3. **Stale conn cleanup race:** when conn1 cleanup ran after conn2 registration,
   cleanup had to compare connection identity before deleting `d.workers[workerID]`.
   Without that guard, the live worker entry could be removed or writes could
   target stale state.

## Solution

The P0 child fixes covered the full chain:

- `oro-55q5`: added a spawn-window heartbeat loop so `handleAssign` keeps
  liveness updates flowing until runtime spawn returns.
- `oro-cann`: replaced recursive reconnect restart with read-channel replacement
  inside the existing `Run` event loop.
- `oro-4vhg`: added regression coverage for stale `connCloseCleanup` preserving a
  live reconnected worker.
- `oro-xmm3`: added dispatcher reachability coverage proving a dispatcher write
  reaches the reconnected worker after assignment, with `pendingMsgs` length 0.

## Regression Commands

Run the focused regressions:

```bash
go test ./pkg/worker/... -run TestWorkerHeartbeatDuringSlowAssignSpawn -count=1 -timeout 180s
go test ./pkg/worker/... -run TestReconnectDoesNotNestRunLoops -count=1 -timeout 180s
go test ./pkg/dispatcher/... -run TestReconnectStaleConnCleanupPreservesLiveWorker -count=1 -timeout 180s
go test ./pkg/dispatcher/... -run TestWorkerReachableThroughAssignment -count=1 -timeout 180s
```

Run the broader package checks:

```bash
go test ./pkg/worker/... -count=1 -timeout 180s
go test ./pkg/dispatcher/... -count=1 -timeout 180s
./scripts/quality_gate.sh
```

## Prevention

- Treat liveness during long blocking setup as a separate phase from subprocess
  monitoring. `STATUS running` starts only after spawn; heartbeats must cover the
  gap before that.
- Keep reconnect inside one event loop. Reconnect should swap the active conn and
  read channels, not call `Run` recursively.
- In cleanup paths, always compare the connection being cleaned up to the
  currently registered worker conn before deleting worker state.
- For reachability bugs, require a dispatcher-originated write assertion.
  Worker-originated messages can mask stale dispatcher write paths.

## Related

- `pkg/worker/worker.go:Run`
- `pkg/worker/worker.go:handleAssign`
- `pkg/dispatcher/dispatcher.go:connCloseCleanup`
- `pkg/dispatcher/worker_pool.go:sendToWorker`
