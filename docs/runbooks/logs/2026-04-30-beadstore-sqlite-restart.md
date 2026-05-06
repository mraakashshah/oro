# Beadstore SQLite Restart Operator Log - 2026-04-30

This log tracks Phase 8 P8-4 (`oro-nqih`): restarting the dispatcher/workers in
native SQLite mode with `bd` stripped from worker `PATH`.

## Gate

- State database: `/Users/as21/.oro/projects/oro/state.db`
- Reviewed binary: `/tmp/oro-native-cutover`
- Worker count for this proof: `1`
- Pre-claim gate evidence:
  - `scripts/check-phase8-no-writers.py`: `active_writer_count=0`
  - dispatcher/worker process scan: `dispatcher_count=0`
  - `scripts/check-native-beadstore-invariants.py --db /Users/as21/.oro/projects/oro/state.db`: zero ready/blocked/status/assignment/blocker mismatches
  - `sqlite3 /Users/as21/.oro/projects/oro/state.db 'PRAGMA integrity_check;'`: `ok`
- Tracker state: `oro-nqih` claimed in both bd and native SQLite before restart.
- Native ready queue after claim: `0`

## Planned Restart Command

Workers inherit dispatcher daemon environment. The dispatcher is therefore
started from a generated `PATH` that contains a symlinked `oro` binary and
standard system directories, but not `bd`.

```bash
state_db=/Users/as21/.oro/projects/oro/state.db
state_dir=$(dirname "$state_db")
pid_path=${ORO_PID_PATH:-"$state_dir/oro.pid"}
oro_bin=/tmp/oro-native-cutover
worker_count=1

cutover_bin_dir=$(mktemp -d /tmp/oro-sqlite-cutover-bin.XXXXXX)
ln -s "$oro_bin" "$cutover_bin_dir/oro"
ln -s "$(command -v claude)" "$cutover_bin_dir/claude"
for tool in go jq rg node; do
  tool_path=$(command -v "$tool" || true)
  if test -n "$tool_path"; then ln -s "$tool_path" "$cutover_bin_dir/$tool"; fi
done
cutover_path="$cutover_bin_dir:/usr/bin:/bin:/usr/sbin:/sbin"
PATH="$cutover_path" command -v oro
PATH="$cutover_path" command -v claude
PATH="$cutover_path" command -v git
! PATH="$cutover_path" command -v bd

ORO_HUMAN_CONFIRMED=1 ./oro stop --force
PATH="$cutover_path" ORO_DB_PATH="$state_db" ORO_BEADSOURCE_MODE=sqlite "$oro_bin" dispatcher start --force --workers "$worker_count"
```

## Runtime Evidence

- First start attempt failed before daemon spawn because `dispatcher start`
  preflight required `bd` in `PATH`, which conflicts with worker PATH stripping.
  No dispatcher or worker remained running after the failed attempt; the
  no-writer gate still reported `active_writer_count=0`.
- Runbook correction: use `dispatcher start --force` only after explicit
  replacement checks prove `oro`, `claude`, and `git` resolve and `bd` does not.
- Disposable branch smoke for blocker `oro-nqih.1`: built `/tmp/oro-nqih1`,
  created a temporary state DB and Oro home, generated a stripped `PATH` with
  symlinks for `oro`, `claude`, and available helper tools, and verified
  `oro`, `claude`, and `git` resolved while `bd` did not. Started
  `ORO_BEADSOURCE_MODE=sqlite /tmp/oro-nqih1 dispatcher start --force
  --workers 0` against temporary PID/socket paths; the socket became ready,
  dispatcher PID `88728` inherited `ORO_BEADSOURCE_MODE=sqlite`, and its `PATH`
  exactly matched the generated stripped path. The dispatcher was then stopped
  with `dispatcher stop --force`. No live worker was spawned by this smoke.
- Main integration proof for blocker `oro-nqih.1`: commit `0c89975d` passed
  focused main tests and a disposable stripped-`PATH` smoke using
  `/tmp/oro-main-nqih1`. The smoke started sqlite dispatcher workers=0,
  verified socket readiness, `ORO_BEADSOURCE_MODE=sqlite`, exact stripped
  dispatcher `PATH`, `bd` absent from `PATH`, and clean dispatcher stop.
- Live P8-4 retry after `oro-nqih.1` closure: gates passed with
  `active_writer_count=0`, dispatcher count `0`, native invariants clean,
  SQLite `integrity_check=ok`, `ORO_BEADSOURCE_MODE` empty, and native ready
  length `0` before restart. Built reviewed binary `/tmp/oro-native-cutover`.
  Started dispatcher PID `74998` from stripped path
  `/tmp/oro-sqlite-cutover-bin.mfqtTh:/usr/bin:/bin:/usr/sbin:/sbin` with
  `ORO_BEADSOURCE_MODE=sqlite`, `--force`, and `--workers 1`; dispatcher env
  mode and path matched expectations.
- Worker proof blocked: worker `sqlite-smoke-20260430T2055` PID `77112`
  inherited the exact stripped path and log offset `0`, but remained idle.
  Controlled native bead `native-worker-smoke-20260430T2056` was created and
  stayed open/ready. Targeted `worker launch --bead` spawned
  `worker-spawnfor-1777582706091359000`, which heartbeated, but no `assign`
  event appeared after more than one assign interval. Dispatcher/workers were
  stopped, the smoke task was closed manually for cleanup, and
  `scripts/check-phase8-no-writers.py` returned `active_writer_count=0`.
  New blocker: `oro-ect4.6`.
- Root cause for `oro-ect4.6`: sqlite dispatcher startup still allowed the
  legacy bd/Dolt health loop. With `bd` intentionally stripped from `PATH`, the
  loop logged `dolt_recovery_started` and `dolt_recovery_failed` for
  `bd dolt start`, leaving `doltRecovering=true`; `tryAssign` exits early in
  that state. Fix branch `bead/oro-ect4.6-sqlite-assign` now captures
  `ORO_BEADSOURCE_MODE` at dispatcher startup and skips bd/Dolt health recovery
  only in sqlite mode. Detailed note:
  `docs/runbooks/incidents/2026-04-30-beadstore-sqlite-assignment-blocker.md`.
