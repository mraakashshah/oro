# Runbook: migrate bd/Dolt projects to Oro tasks

This runbook is the per-project operator path for moving every remaining
bd/Dolt-backed Oro project onto the native SQLite task store and the `oro task`
CLI.

Run it once for each project. Do not batch multiple repositories through one
shared shell environment unless every project has its own recorded inventory,
source snapshot, SQLite backup, validation output, and cutover log.

The command blocks assume one shell per project so variables such as
`repo_root`, `oro_bin`, `oro_home`, `project`, `state_db`, `migration_dir`, and
`bd_export` remain available. If a shell exits, rerun the inventory and source
snapshot blocks before continuing.

## Authority model

After migration, the project's `state.db` is the source of truth for task data.
bd/Dolt is retained only as:

- an import source for the first migration,
- an audit source for explaining where imported rows came from,
- a rollback reference when a reviewed historical snapshot is needed.

Do not run production dispatcher or worker paths with `ORO_BEADSOURCE_MODE=cli`
or `ORO_BEADSOURCE_MODE=shadow`. Normal task operations must use `oro task`.
The only migration-only command in this runbook that still uses the legacy
subtree is `oro bead migrate-from-dolt`.

## Per-project inventory

Create an operator log before touching data. Record this table for the target
project:

| Field | Value |
| --- | --- |
| Project name | |
| Repository root | |
| Oro binary path | |
| `ORO_HOME` | |
| `ORO_PROJECT` | |
| `ORO_DB_PATH` | |
| Resolved `state.db` | |
| Legacy task data path | |
| bd export path | |
| Pre-migration SQLite backup path | |
| Migration JSONL backup path | |
| Representative task IDs checked | |
| Cutover timestamp | |
| Archive path for legacy Dolt data | |

Resolve paths from inside the project root:

```bash
set -euo pipefail

repo_root=$(git rev-parse --show-toplevel)
cd "$repo_root"

oro_bin=${ORO_BIN:-"$(command -v oro)"}
case "$oro_bin" in
  /*) ;;
  *) oro_bin="$(cd "$(dirname "$oro_bin")" && pwd -P)/$(basename "$oro_bin")" ;;
esac
test -x "$oro_bin"

oro_home=${ORO_HOME:-"$HOME/.oro"}
project=${ORO_PROJECT:-}
if [ -z "$project" ] && [ -f .oro/config.yaml ]; then
  project=$(awk -F: '/^project:/ {gsub(/^[ \t]+|[ \t]+$/, "", $2); print $2; exit}' .oro/config.yaml)
fi
if [ -z "$project" ]; then
  root=$(pwd -P)
  hash=$(printf '%s' "$root" | shasum -a 256 | awk '{print substr($1, 1, 16)}')
  test ! -f "$oro_home/projects/s-$hash/config.yaml" || project="s-$hash"
fi

state_db=${ORO_DB_PATH:-}
if [ -z "$state_db" ]; then
  if [ -n "$project" ]; then
    state_db="$oro_home/projects/$project/state.db"
  else
    state_db="$oro_home/state.db"
  fi
fi

printf 'repo_root=%s\n' "$repo_root"
printf 'oro_bin=%s\n' "$oro_bin"
printf 'oro_home=%s\n' "$oro_home"
printf 'project=%s\n' "$project"
printf 'state_db=%s\n' "$state_db"

export ORO_DB_PATH="$state_db"
```

Inventory legacy data locations:

```bash
set -euo pipefail

find "$repo_root" "$oro_home/projects" -type d \( \
  -path '*/.beads/dolt' -o \
  -path '*/.beads/beads_*/.dolt' -o \
  -path '*/.beads/embeddeddolt/*/.dolt' -o \
  -path '*/beads/dolt' -o \
  -path '*/beads/beads_*/.dolt' -o \
  -path '*/beads/embeddeddolt/*/.dolt' \
\) 2>/dev/null | sort -u
```

If the project has no legacy data and `oro task status` already works against
the expected `state.db`, skip to the validation and archive sections.

## Preconditions

Run from the project root. Stop every dispatcher, worker, direct bd writer, and
direct native task writer before export, backup, dry-run, apply, reconcile, or
archive.

```bash
set -euo pipefail

"$oro_bin" stop --force || true

dispatcher_matches=$(ps ax -o pid=,command= | rg '([o]ro start|[o]ro dispatcher start)' || true)
dispatcher_count=$(printf '%s\n' "$dispatcher_matches" | awk 'NF { n++ } END { print n + 0 }')
printf 'dispatcher_count=%s\n' "$dispatcher_count"
test "$dispatcher_count" = 0

scripts/check-phase8-no-writers.py

mode=${ORO_BEADSOURCE_MODE-}
printf 'ORO_BEADSOURCE_MODE=%s\n' "$mode"
case "$mode" in
  "" | sqlite) ;;
  *) echo "unset ORO_BEADSOURCE_MODE or set it to sqlite before this runbook" >&2; exit 1 ;;
esac

"$oro_bin" task status >/tmp/oro-task-status.preflight.txt
"$oro_bin" bead migrate-from-dolt --help >/tmp/oro-migrate-help.txt
```

If `scripts/check-phase8-no-writers.py` reports active writers, stop them and
restart this section. Do not use `--allow-running-dispatcher` for planned
migrations.

## Source snapshot

Prefer an explicit, operator-recorded JSONL export so every later command uses
the same source. This avoids accidentally importing a different bd/Dolt state
between dry-run and apply.

```bash
set -euo pipefail

migration_dir="$oro_home/migrations/$(date -u +%Y%m%dT%H%M%SZ)-${project:-global}-bd-dolt-to-oro-task"
mkdir -m 0700 -p "$migration_dir"

bd_export=${BD_EXPORT:-"$migration_dir/bd-export.jsonl"}
if [ -n "${BD_EXPORT:-}" ]; then
  test -s "$bd_export"
  if [ "$bd_export" != "$migration_dir/reviewed-source.jsonl" ]; then
    cp "$bd_export" "$migration_dir/reviewed-source.jsonl"
  fi
  bd_export="$migration_dir/reviewed-source.jsonl"
  wc -l "$bd_export" | tee "$migration_dir/bd-export.count.txt"
elif command -v bd >/dev/null 2>&1; then
  bd export > "$bd_export"
  test -s "$bd_export"
  wc -l "$bd_export" | tee "$migration_dir/bd-export.count.txt"
else
  echo "bd is not on PATH; set BD_EXPORT to a reviewed JSONL source snapshot" >&2
  exit 1
fi

printf 'bd_export=%s\n' "$bd_export"
```

If bd/Dolt is damaged but a reviewed JSONL export already exists, set this
before running the source snapshot block:

```bash
export BD_EXPORT=/absolute/path/to/reviewed-export.jsonl
```

Record why this snapshot is the best available source. Do not use
`--force-recover` for planned cutover approval.

## SQLite backup

Back up the target `state.db` before any real mutation, even if the native task
table is expected to be empty.

```bash
set -euo pipefail

mkdir -p "$(dirname "$state_db")"
touch "$state_db"

snapshot_dir="$migration_dir/pre-migration-state-db"
mkdir -m 0700 -p "$snapshot_dir"

sqlite3 "$state_db" 'PRAGMA wal_checkpoint(FULL);'
sqlite3 "$state_db" ".backup '$snapshot_dir/state.db'"
integrity=$(sqlite3 "$snapshot_dir/state.db" 'PRAGMA integrity_check;')
test "$integrity" = ok

printf 'pre_migration_state_db_snapshot=%s\n' "$snapshot_dir"
```

If this fails, stop. Fix path resolution or SQLite health before continuing.

## Dry-run

Initial migration is allowed only when the native task table is empty. A
non-empty target means the project was already imported or partially mutated;
use reconcile or restore a reviewed backup instead of forcing initial apply.

```bash
set -euo pipefail

scripts/check-phase8-no-writers.py

"$oro_bin" bead migrate-from-dolt --dry-run --from-jsonl "$bd_export" \
  | tee "$migration_dir/dry-run.txt"
```

Dry-run must satisfy all of these:

- exit code is `0`;
- no `--force-recover` is needed;
- no validation errors are reported;
- source row count matches the operator's expected export count;
- target is not reported as already populated.

If dry-run fails because target SQLite already contains imported task rows, do
not delete rows casually. Decide whether this project needs the reconcile path
or a restore from the backup captured above.

## Apply initial import

Run apply against exactly the JSONL source that passed dry-run.

```bash
set -euo pipefail

scripts/check-phase8-no-writers.py

"$oro_bin" bead migrate-from-dolt --from-jsonl "$bd_export" \
  | tee "$migration_dir/apply.txt"

sqlite3 "$state_db" 'PRAGMA integrity_check;' | tee "$migration_dir/integrity-after-apply.txt"
scripts/check-native-beadstore-invariants.py --db "$state_db" \
  | tee "$migration_dir/native-invariants-after-apply.txt"
```

The migration command writes its own source JSONL backup under
`$oro_home/migrations/<timestamp>-pre-migration.jsonl`. Record that path from
the apply output. That JSONL file is audit data; rollback depends on the
SQLite backup unless a future native import command is explicitly implemented.

## Reconcile late writes

Use reconcile only when a project was initially imported and then a reviewed
source snapshot shows legitimate bd/Dolt-side changes that must be copied into
SQLite. Reconcile is per project; never reconcile one repository with another
repository's export.

Preview:

```bash
set -euo pipefail

scripts/check-phase8-no-writers.py

"$oro_bin" bead migrate-from-dolt --reconcile --from-jsonl "$bd_export" \
  | tee "$migration_dir/reconcile-preview.txt"
```

Apply only when the preview has zero conflicts and the change counts are
expected:

```bash
set -euo pipefail

reconcile_snapshot="$migration_dir/pre-reconcile-state-db"
mkdir -m 0700 -p "$reconcile_snapshot"
sqlite3 "$state_db" 'PRAGMA wal_checkpoint(FULL);'
sqlite3 "$state_db" ".backup '$reconcile_snapshot/state.db'"
test "$(sqlite3 "$reconcile_snapshot/state.db" 'PRAGMA integrity_check;')" = ok

scripts/check-phase8-no-writers.py

"$oro_bin" bead migrate-from-dolt --reconcile --apply --from-jsonl "$bd_export" \
  | tee "$migration_dir/reconcile-apply.txt"

sqlite3 "$state_db" 'PRAGMA integrity_check;'
scripts/check-native-beadstore-invariants.py --db "$state_db"
```

## Native validation

Validation must prove read paths, write paths, dependency derivation, and JSON
shape from the native store. Run this before starting workers.

```bash
set -euo pipefail

export ORO_DB_PATH="$state_db"
export ORO_BEADSOURCE_MODE=sqlite

scripts/check-phase8-no-writers.py

"$oro_bin" task status | tee "$migration_dir/task-status.txt"
"$oro_bin" task ready --json > "$migration_dir/task-ready.json"
"$oro_bin" task blocked --json > "$migration_dir/task-blocked.json"
"$oro_bin" task closed --limit 25 --json > "$migration_dir/task-closed.json"

jq -e 'type == "array"' "$migration_dir/task-ready.json"
jq -e 'type == "array"' "$migration_dir/task-blocked.json"
jq -e 'type == "array"' "$migration_dir/task-closed.json"

representative_id=${ORO_NATIVE_REPRESENTATIVE_ID:?set to a migrated task id}
"$oro_bin" task show "$representative_id" --json \
  | tee "$migration_dir/task-show-representative.json" \
  | jq -e --arg id "$representative_id" '.id == $id and (.status | type == "string")'

"$oro_bin" task dep list "$representative_id" --json \
  > "$migration_dir/task-deps-representative.json"
jq -e 'type == "array"' "$migration_dir/task-deps-representative.json"

scripts/check-native-beadstore-invariants.py --db "$state_db" \
  | tee "$migration_dir/native-invariants.txt"
sqlite3 "$state_db" 'PRAGMA integrity_check;' \
  | tee "$migration_dir/integrity.txt"
sqlite3 "$state_db" 'SELECT COUNT(*) FROM beads WHERE deleted = 0;' \
  | tee "$migration_dir/native-task-count.txt"
```

Then prove native writes with a controlled smoke task:

```bash
set -euo pipefail

smoke_id="native-task-smoke-$(date -u +%Y%m%dT%H%M%SZ)"

"$oro_bin" task create \
  --id "$smoke_id" \
  --title "Native task smoke" \
  --type task \
  --priority 4 \
  --description "Created by bd/Dolt migration runbook validation" \
  --acceptance-criteria "Cmd: oro task show $smoke_id --json | jq -e '.status == \"closed\"'"

"$oro_bin" task show "$smoke_id" --json | jq -e --arg id "$smoke_id" '.id == $id'
"$oro_bin" task close "$smoke_id" --reason "Native task smoke passed"
"$oro_bin" task show "$smoke_id" --json | jq -e '.status == "closed"'

scripts/check-native-beadstore-invariants.py --db "$state_db"
sqlite3 "$state_db" 'PRAGMA integrity_check;'

printf 'smoke_id=%s\n' "$smoke_id" | tee "$migration_dir/native-smoke-id.txt"
```

## Cutover

Start the dispatcher from an environment that resolves `oro` and normal worker
tools but does not resolve `bd`.

```bash
set -euo pipefail

scripts/check-phase8-no-writers.py

state_dir=$(dirname "$state_db")
pid_path=${ORO_PID_PATH:-"$state_dir/oro.pid"}
old_dispatcher_pid=$(cat "$pid_path" 2>/dev/null || true)

worker_count=${ORO_CUTOVER_WORKERS:?set restarted worker count}
test -n "$worker_count"

cutover_bin_dir=$(mktemp -d /tmp/oro-task-cutover-bin.XXXXXX)
ln -s "$oro_bin" "$cutover_bin_dir/oro"

old_ifs=$IFS
IFS=:
for dir in $PATH; do
  IFS=$old_ifs
  test -d "$dir" || { IFS=:; continue; }
  for tool_path in "$dir"/*; do
    test -e "$tool_path" || continue
    test -x "$tool_path" || continue
    tool_name=$(basename "$tool_path")
    test "$tool_name" != bd || continue
    test ! -e "$cutover_bin_dir/$tool_name" || continue
    ln -s "$tool_path" "$cutover_bin_dir/$tool_name"
  done
  IFS=:
done
IFS=$old_ifs

cutover_path="$cutover_bin_dir:/usr/bin:/bin:/usr/sbin:/sbin"
PATH="$cutover_path" command -v oro
PATH="$cutover_path" command -v git
PATH="$cutover_path" command -v bash
! PATH="$cutover_path" command -v bd

ORO_HUMAN_CONFIRMED=1 "$oro_bin" dispatcher stop --force || true
PATH="$cutover_path" \
  ORO_DB_PATH="$state_db" \
  ORO_BEADSOURCE_MODE=sqlite \
  "$oro_bin" dispatcher start --force --workers "$worker_count"

test -r "$pid_path"
dispatcher_pid=$(cat "$pid_path")
test -z "$old_dispatcher_pid" || test "$dispatcher_pid" != "$old_dispatcher_pid"
ps eww -p "$dispatcher_pid" | rg 'ORO_BEADSOURCE_MODE=sqlite'
dispatcher_path=$(ps eww -p "$dispatcher_pid" | tr ' ' '\n' | awk -F= '$1=="PATH" { print substr($0, 6); exit }')
test "$dispatcher_path" = "$cutover_path"
```

Verify every restarted worker inherited a PATH that cannot resolve `bd`.
Record each PID and PATH check output in the operator log.

```bash
set -euo pipefail

worker_pids=${ORO_CUTOVER_WORKER_PIDS:?set space-separated restarted worker PIDs}
for worker_pid in $worker_pids; do
  ps -p "$worker_pid" >/dev/null
  worker_path=$(ps eww -p "$worker_pid" | tr ' ' '\n' | awk -F= '$1=="PATH" { print substr($0, 6); exit }')
  test -n "$worker_path"
  PATH="$worker_path" command -v oro
  PATH="$worker_path" command -v git
  ! PATH="$worker_path" command -v bd
  printf 'worker_pid=%s path_ok_no_bd=1\n' "$worker_pid" \
    | tee -a "$migration_dir/worker-path-checks.txt"
done
```

Before releasing normal work, assign one controlled task through the restarted
dispatcher and verify it moves through the native store. Record the task ID,
worker PID, worker PATH, and dispatcher events in the operator log.

## Archive legacy Dolt data

Archive only after native validation passes and the dispatcher has restarted
from a PATH where `bd` is unavailable.

For each legacy Dolt directory found in the inventory:

```bash
set -euo pipefail

legacy_dolt_path=${LEGACY_DOLT_PATH:?set one legacy Dolt directory}

case "$legacy_dolt_path" in
  */.beads/dolt)
    legacy_root=$(dirname "$legacy_dolt_path")
    archive_item_rel="dolt"
    ;;
  */.beads/beads_*/.dolt|*/.beads/embeddeddolt/*/.dolt)
    legacy_root=${legacy_dolt_path%%/.beads/*}/.beads
    db_dir=${legacy_dolt_path%/.dolt}
    archive_item_rel=${db_dir#"$legacy_root"/}
    ;;
  */beads/dolt)
    legacy_root=$(dirname "$legacy_dolt_path")
    archive_item_rel="dolt"
    ;;
  */beads/beads_*/.dolt|*/beads/embeddeddolt/*/.dolt)
    legacy_root=${legacy_dolt_path%%/beads/*}/beads
    db_dir=${legacy_dolt_path%/.dolt}
    archive_item_rel=${db_dir#"$legacy_root"/}
    ;;
  *)
    echo "unexpected legacy Dolt path: $legacy_dolt_path" >&2
    exit 1
    ;;
esac

safe_name=$(printf '%s' "$archive_item_rel" | tr '/.' '__')
archive="$migration_dir/legacy-dolt-${safe_name}-archive-$(date -u +%Y%m%dT%H%M%SZ).tar.gz"

test ! -e "$archive"
tar -czf "$archive" -C "$legacy_root" "$archive_item_rel"
tar -tzf "$archive" | head
rm -rf "$legacy_root/$archive_item_rel"

printf 'legacy_dolt_archive=%s\n' "$archive"
```

Keep archives for at least 30 days, or until the Phase 11 legacy-migration
survey says the project no longer needs rollback evidence.

## Rollback stance

Rollback to bd/Dolt is not the normal answer after cutover.

Use this order:

1. Stop dispatcher and workers.
2. Preserve the current `state.db`, `state.db-wal`, `state.db-shm`, worker logs,
   dispatcher logs, and the migration operator log.
3. If SQLite corruption is proven, restore the recorded SQLite backup and rerun
   native validation.
4. If imported content is wrong but SQLite is healthy, preserve a fresh
   `oro task export --out "$migration_dir/native-before-repair.jsonl"` snapshot
   and create native repair tasks.
5. Use bd/Dolt only for forensic comparison against the recorded JSONL export
   or archived Dolt tarball. Do not restart production dispatcher/workers in
   `cli` or `shadow` mode.

## Completion gate

A project is fully migrated only when all of these are true:

- `oro task status`, `ready`, `blocked`, `closed`, `show`, `dep list`,
  `create`, and `close` passed with `ORO_BEADSOURCE_MODE=sqlite`.
- `scripts/check-native-beadstore-invariants.py --db "$state_db"` passed after
  import and after the smoke write.
- `sqlite3 "$state_db" 'PRAGMA integrity_check;'` printed `ok`.
- Dispatcher was restarted with `ORO_BEADSOURCE_MODE=sqlite`.
- Dispatcher and worker PATH checks prove `bd` is not resolvable.
- Legacy Dolt directories were archived or explicitly waived in the operator
  log.
- The operator log records every path and command output listed in the
  inventory table.

After every project passes this gate, run the install-level proof once from a
clean checkout:

```bash
scripts/check-phase10-no-bd-install.sh
```

That script proves a normal build/install and native task lifecycle work in a
controlled PATH where `bd` cannot be resolved.
