# Phase 9 Native Cutover Evidence Report

Date: 2026-05-02

This report replaces the old 30-day/passive-shadow Phase 9 gate. bd/Dolt is
retained as import source, audit trail, and rollback reference. It is not a
cutover veto after native SQLite validation passes.

## Inputs

- Repo: `/Users/as21/codehouse/oro`
- Binary: `/tmp/oro-current-phase10`
- Binary version: `oro v0.1.0-660-g91ca3a87-dirty`
- State DB: `/Users/as21/.oro/projects/oro/state.db`
- Baseline: `docs/plans/notes/baseline-metrics.md`
- Baseline bd ready average: `106.79 ms`

## Native Validation

Commands run with `ORO_DB_PATH=/Users/as21/.oro/projects/oro/state.db` and
`ORO_BEADSOURCE_MODE=sqlite`.

```text
scripts/check-phase8-no-writers.py
active_writer_count=0

/tmp/oro-current-phase10 bead status
open        91
in_progress 1
closed      1680

/tmp/oro-current-phase10 bead ready --json
result: valid JSON array

/tmp/oro-current-phase10 bead blocked --json
result: valid JSON array

/tmp/oro-current-phase10 bead show oro-eyym --json
result: id == oro-eyym and status is a string

scripts/check-native-beadstore-invariants.py --db /Users/as21/.oro/projects/oro/state.db
integrity_check=ok
legacy_foreign_key_violations=30
invalid_status_rows=0
ready_view_mismatches=0
blocked_view_mismatches=0
ready_blocked_overlap=0
active_assignment_in_ready_or_blocked=0
ready_with_unclosed_hard_blocker=0

sqlite3 /Users/as21/.oro/projects/oro/state.db 'PRAGMA integrity_check;'
ok

scripts/check-phase8-no-writers.py
active_writer_count=0
```

The single `in_progress` bead was `oro-cpv0`, the Phase 9 epic itself.

## Latency

Ten native `oro bead ready --json` calls were measured through the reviewed
binary and parsed as JSON.

```text
sqlite_ready_ms=22.66,22.55,22.02,20.59,20.53,21.05,22.11,22.08,20.45,20.49
sqlite_ready_avg_ms=21.45
sqlite_ready_min_ms=20.45
sqlite_ready_max_ms=22.66
bd_baseline_avg_ms=106.79
latency_improved=True
```

Native ready latency is improved versus the Phase 0 bd subprocess baseline.

## Dispatcher And Worker Proof

Preflight:

```text
scripts/check-phase8-no-writers.py
active_writer_count=0

dispatcher_count=0
ORO_BEADSOURCE_MODE=
PATH=/tmp/oro-phase10-nobd-arm-bin.tfMC5N:/usr/bin:/bin:/usr/sbin:/sbin
command -v bd: no match
command -v oro: /tmp/oro-phase10-nobd-arm-bin.tfMC5N/oro
```

The dispatcher was started with sqlite mode and zero initial workers:

```text
PATH="$phase10_path" ORO_DB_PATH=/Users/as21/.oro/projects/oro/state.db \
  ORO_BEADSOURCE_MODE=sqlite /tmp/oro-current-phase10 dispatcher start --workers 0

dispatcher started (PID 84852, workers=0)
```

The controlled worker smoke bead was
`oro-phase10-smoke-074350`. It used TDD-form acceptance criteria so the
dispatcher would treat it as executable.

```text
/tmp/oro-current-phase10 worker launch --bead oro-phase10-smoke-074350
```

Native event evidence:

```text
797516 spawn_for dispatcher oro-phase10-smoke-074350 worker-spawnfor-1777722230644608000
797524 assign dispatcher oro-phase10-smoke-074350 worker-spawnfor-1777722230644608000
       {"worktree":"/Users/as21/codehouse/oro/.worktrees/oro-phase10-smoke-074350",
        "branch":"agent/oro-phase10-smoke-074350"}
```

`oro status` reported exactly that active worker assignment:

```text
workers: 1 active, 1 idle (target: 0)
in_progress beads:
  worker-spawnfor-1777722230644608000 -> oro-phase10-smoke-074350
```

The dispatcher and workers were stopped with:

```text
ORO_HUMAN_CONFIRMED=1 /tmp/oro-current-phase10 stop --force
```

After shutdown:

```text
ps ax ... oro dispatcher/worker/start: no matches
scripts/check-phase8-no-writers.py
active_writer_count=0

sqlite3 /Users/as21/.oro/projects/oro/state.db 'PRAGMA integrity_check;'
ok
```

The smoke assignment was then quarantined by a zero-worker dispatcher restart
because the synthetic worktree was already removed:

```text
797562 startup_recovery_quarantined dispatcher oro-phase10-smoke-074350
       {"assignment_id":3086,"reason":"missing_worktree_path"}
```

Both controlled smoke beads are closed:

- `oro-phase10-smoke-074149`: closed as a setup error after
  `non_tdd_acceptance`; not used as proof.
- `oro-phase10-smoke-074350`: closed as the sqlite/no-bd dispatcher-worker
  proof bead.

## Conclusion

Phase 9's old passive observation gate is superseded. Native SQLite validation,
latency, dispatcher startup, and targeted worker assignment all pass with `bd`
absent from `PATH`. Remaining Phase 10 work should treat post-cutover failures
as native bugs unless SQLite data corruption is shown and a recorded backup must
be restored.
