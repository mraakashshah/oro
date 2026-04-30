# Native Beadstore Migration Cutover Runbook

This is the current Phase 8 operator runbook for completing the bd/Dolt to
native SQLite beadstore migration.

## Authority Model

bd/Dolt is no longer treated as a long-running authority for cutover approval.
Its useful roles are narrower:

- **Import source:** take the best available snapshot from bd/Dolt.
- **Audit trail:** explain where imported data came from.
- **Rollback reference:** preserve JSONL exports and SQLite backup snapshots.
- **Not a veto:** bd parity does not block cutover when the divergence is caused
  by bd/Dolt failure, stale bd state, or bd unavailability.

After the initial import is verified, native SQLite becomes the system being
validated. Post-cutover failures are native beadstore defects unless evidence
shows data corruption that requires restoring a recorded backup.

## Preconditions

Run from the repo root with dispatcher and workers stopped.

```bash
set -euo pipefail

scripts/check-bd-version.sh
dispatcher_matches=$(ps ax -o pid=,command= | rg '([o]ro start|[o]ro dispatcher start)' || true)
dispatcher_count=$(printf '%s\n' "$dispatcher_matches" | awk 'NF { n++ } END { print n + 0 }')
printf 'dispatcher_count=%s\n' "$dispatcher_count"
test "$dispatcher_count" = 0
scripts/check-phase8-no-writers.py
mode=${ORO_BEADSOURCE_MODE-}
printf 'ORO_BEADSOURCE_MODE=%s\n' "$mode"
case "$mode" in "" | cli) ;; *) echo "ORO_BEADSOURCE_MODE must be empty or cli before migration" >&2; exit 1 ;; esac
./oro bead migrate-from-dolt --help
./oro bead migrate-from-dolt --dry-run
```

The dry-run must exit 0 without `--force-recover` and without reporting a
non-empty native bead table. If Dolt is damaged but a reviewed JSONL source is
available, run the matching `--from-jsonl <path>` dry-run and record why that
snapshot is the best available source.

## Required Backups

Before any real mutation, create a SQLite snapshot and verify it.

```bash
set -euo pipefail

oro_home=${ORO_HOME:-"$HOME/.oro"}
state_db=${ORO_DB_PATH:-}
if [ -z "$state_db" ]; then
  project=${ORO_PROJECT:-}
  if [ -z "$project" ] && [ -f .oro/config.yaml ]; then
    project=$(awk -F: '/^project:/ {gsub(/^[ \t]+|[ \t]+$/, "", $2); print $2; exit}' .oro/config.yaml)
  fi
  if [ -z "$project" ]; then
    root=$(pwd -P)
    hash=$(printf '%s' "$root" | shasum -a 256 | awk '{print substr($1, 1, 16)}')
    test ! -f "$oro_home/projects/s-$hash/config.yaml" || project="s-$hash"
  fi
  if [ -n "$project" ]; then
    state_db="$oro_home/projects/$project/state.db"
  else
    state_db="$oro_home/state.db"
  fi
fi
snapshot_dir="$oro_home/migrations/$(date -u +%Y%m%dT%H%M%SZ)-pre-native-cutover-state-db"
mkdir -m 0700 -p "$snapshot_dir"
sqlite3 "$state_db" 'PRAGMA wal_checkpoint(FULL);'
sqlite3 "$state_db" ".backup '$snapshot_dir/state.db'"
integrity=$(sqlite3 "$snapshot_dir/state.db" 'PRAGMA integrity_check;')
test "$integrity" = ok
printf 'pre-cutover state.db snapshot: %s\n' "$snapshot_dir"
```

Record the SQLite snapshot path. The migration command also writes a mandatory
JSONL source backup under `OroHome/migrations/<timestamp>-pre-migration.jsonl`;
record that path too. The JSONL backup is source/audit data, not an in-place
restore command for a populated SQLite database.

## Apply Import

Re-run the no-writer and mode gates immediately before real apply.

```bash
set -euo pipefail

dispatcher_matches=$(ps ax -o pid=,command= | rg '([o]ro start|[o]ro dispatcher start)' || true)
dispatcher_count=$(printf '%s\n' "$dispatcher_matches" | awk 'NF { n++ } END { print n + 0 }')
printf 'dispatcher_count=%s\n' "$dispatcher_count"
test "$dispatcher_count" = 0
scripts/check-phase8-no-writers.py
mode=${ORO_BEADSOURCE_MODE-}
printf 'ORO_BEADSOURCE_MODE=%s\n' "$mode"
case "$mode" in "" | cli) ;; *) echo "ORO_BEADSOURCE_MODE must be empty or cli before migration" >&2; exit 1 ;; esac

./oro bead migrate-from-dolt
sqlite3 "$state_db" 'PRAGMA integrity_check;'
```

The import report must show zero validation errors. Record source count,
imported count, verification result, SQLite snapshot path, and JSONL backup
path.

## Native Validation Gate

This gate replaces the old 24-hour shadow soak. It validates the native store
directly instead of letting bd/Dolt veto cutover.

Run against the target `state.db`:

```bash
set -euo pipefail

export ORO_DB_PATH="$state_db"
ORO_BEADSOURCE_MODE=sqlite ./oro bead status
ORO_BEADSOURCE_MODE=sqlite ./oro bead ready --json > /tmp/oro-native-ready.json
ORO_BEADSOURCE_MODE=sqlite ./oro bead blocked --json > /tmp/oro-native-blocked.json
jq -e 'type == "array"' /tmp/oro-native-ready.json
jq -e 'type == "array"' /tmp/oro-native-blocked.json
sqlite3 "$state_db" 'PRAGMA integrity_check;'
sqlite3 "$state_db" 'SELECT COUNT(*) FROM beads WHERE deleted = 0;'
```

Then prove a controlled native write path:

```bash
set -euo pipefail

test_id="native-cutover-smoke-$(date -u +%Y%m%dT%H%M%SZ)"
ORO_BEADSOURCE_MODE=sqlite ./oro bead create --id "$test_id" --title "Native cutover smoke" --type task --priority 4
ORO_BEADSOURCE_MODE=sqlite ./oro bead show "$test_id" --json | jq -e '.id == "'"$test_id"'"'
ORO_BEADSOURCE_MODE=sqlite ./oro bead close "$test_id" --reason "Native cutover smoke passed"
ORO_BEADSOURCE_MODE=sqlite ./oro bead show "$test_id" --json | jq -e '.status == "closed"'
sqlite3 "$state_db" 'PRAGMA integrity_check;'
```

If these commands fail, stop and fix the native beadstore. Do not fall back to bd
unless a recorded SQLite backup must be restored because of data corruption.

## Cut Over

Cut over only after the native validation gate passes and the no-writer gate is
clean.

```bash
set -euo pipefail

dispatcher_matches=$(ps ax -o pid=,command= | rg '([o]ro start|[o]ro dispatcher start)' || true)
dispatcher_count=$(printf '%s\n' "$dispatcher_matches" | awk 'NF { n++ } END { print n + 0 }')
printf 'dispatcher_count=%s\n' "$dispatcher_count"
test "$dispatcher_count" = 0
scripts/check-phase8-no-writers.py

export ORO_BEADSOURCE_MODE=sqlite
printf 'ORO_BEADSOURCE_MODE=%s\n' "$ORO_BEADSOURCE_MODE"
```

Restart dispatcher and workers from this environment. Every restarted worker
must have `bd` absent from `PATH` and `oro` present.

```bash
state_dir=$(dirname "$state_db")
pid_path=${ORO_PID_PATH:-"$state_dir/oro.pid"}
old_dispatcher_pid=$(cat "$pid_path" 2>/dev/null || true)
worker_count=<operator-selected restarted worker count>
test -n "$worker_count"
ORO_HUMAN_CONFIRMED=1 ./oro stop --force
ORO_BEADSOURCE_MODE=sqlite ./oro dispatcher start --workers "$worker_count"

test -r "$pid_path"
dispatcher_pid=$(cat "$pid_path")
test -z "$old_dispatcher_pid" || test "$dispatcher_pid" != "$old_dispatcher_pid"
ps eww -p "$dispatcher_pid" | rg 'ORO_BEADSOURCE_MODE=sqlite'
```

For each restarted worker, record PID, PATH check output, log path, and a
post-offset log segment from a controlled test bead showing native `oro bead`
commands and no `bd` invocation.

## Rollback Stance

Rollback to bd is not the normal answer after cutover. Use this order:

1. Stop dispatcher and workers.
2. Preserve the current `state.db`, WAL/SHM files, command transcript, and logs.
3. If SQLite data corruption is proven, restore the recorded SQLite backup
   snapshot and rerun native validation.
4. If bd must be used temporarily, first export SQLite with `oro bead export` and
   import that into bd so bd contains native-side writes made after cutover.
5. Set `ORO_BEADSOURCE_MODE=cli` only after bd has been refreshed from SQLite or
   after an explicit data-loss decision is recorded.

## Operator Log Checklist

Record:

- `scripts/check-bd-version.sh` output or waiver.
- No-writer gate output before dry-run, apply, validation, and restart.
- `ORO_BEADSOURCE_MODE` before migration and at cutover.
- Dry-run command and result.
- SQLite snapshot path and integrity result.
- JSONL migration backup path.
- Import report counts and verification result.
- Native validation command outputs.
- Controlled native smoke bead ID.
- Dispatcher PID before and after restart.
- Worker PATH and post-offset log evidence.
- Any bd/Dolt divergence treated as non-veto, with the reason.
