# SQLite Assignment Blocker - 2026-04-30

This note records the root cause and verification plan for Phase 8 blocker
`oro-ect4.6`.

## Symptom

During the live P8-4 sqlite restart proof, the dispatcher and worker both
started from a stripped `PATH` without `bd`, and both inherited
`ORO_BEADSOURCE_MODE=sqlite`. A controlled native smoke task stayed ready while
the worker heartbeated as idle. A targeted `spawn-for` worker also heartbeated,
but no `assign` event was recorded.

## Root Cause

The dispatcher heartbeat loop still ran the legacy bd/Dolt health probe in
sqlite mode. With `bd` intentionally absent from `PATH`, the probe failed and
entered Dolt recovery:

```text
dolt_recovery_started
dolt_recovery_failed step=dolt_start error="exec: \"bd\": executable file not found in $PATH"
```

While `doltRecovering` is true, `tryAssign` returns before polling ready tasks
or idle workers. That made the worker proof look like an assignment bug even
though the native ready queue was valid.

## Fix

Dispatcher startup now captures the normalized `ORO_BEADSOURCE_MODE`. The
heartbeat loop skips legacy bd/Dolt health recovery only when the captured mode
is `sqlite`. CLI and shadow modes keep the legacy health path.

Regression test:

```bash
go test ./pkg/dispatcher -run TestSQLiteModeSkipsDoltRecoveryAndAssignsReadyBead -count=1
```

The test proves that sqlite mode with `bd` unavailable does not invoke
`bd dolt ...`, does not enter Dolt recovery, and still assigns a native SQLite
ready task to an idle worker.

## Live Retry Gate

Before retrying P8-4, run:

```bash
scripts/check-phase8-no-writers.py
scripts/check-native-beadstore-invariants.py --db /Users/as21/.oro/projects/oro/state.db
sqlite3 /Users/as21/.oro/projects/oro/state.db 'PRAGMA integrity_check;'
```

Then rebuild the reviewed binary, restart dispatcher/workers from the stripped
sqlite cutover `PATH`, and rerun the controlled worker task proof from
`docs/runbooks/beadstore-native-cutover.md`.
