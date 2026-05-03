# Beadstore Migration Safety and Recovery Runbook

This runbook preserves recovery procedures and the legacy shadow-mode Phase 8
path for reference. Do not use it as the current migration-day command source of
truth.

For the current native-first cutover decision, use
`docs/runbooks/beadstore-native-cutover.md` as the migration-day command source
of truth. bd/Dolt is now an import source, audit trail, and rollback reference;
it is not a long-running authority that can veto cutover when divergence is due
to bd/Dolt failure, stale bd state, or bd unavailability. The 24-hour shadow
monitor gate below is retained as legacy recovery context, not as the current
Phase 8 cutover gate.

## Historical Dry-Run State

This section records the migration blockers encountered before the native-first
cutover decision. It is historical context, not the current Phase 8 go/no-go
gate. For current commands, use
`docs/runbooks/beadstore-native-cutover.md`.

The 2026-04-29 real-data dry-run blocker was:

```text
bd export count: 1718
dolt internal count error: ... no database selected
Aborting.
```

That blocker was resolved by selecting the Dolt database from
`.beads/metadata.json`, counting the `issues` table, reporting matched
`bd export` and Dolt counts, and preserving bd `deferred` rows as native `open`
rows with `defer_until`.

A later 2026-04-30 real apply exposed a separate blocker: the target `state.db`
already contained stale native bead row `oro-cdb3`, so post-apply SQLite had one
row more than `bd export`. The migration guard now fails closed on a non-empty
native bead table, and the live target was recovered through a reviewed
backup/clear path before the successful migration retry. Do not treat that
resolved incident as a current Phase 8 blocker.

Before relying on any dry-run gate, verify `bd export` can read the source and
the externally managed Dolt server on `127.0.0.1:13310` is reachable. A dry-run
that cannot run `bd export`, needs `--force-recover`, cannot query Dolt's
internal count, or reports a count mismatch is a no-go for initial import.

Rollback is also not yet fully executable from the shipped CLI. `oro task
import` is still a stub, and `migrate-from-dolt --from-jsonl` is an initial
import path, not an in-place restore command for a populated or corrupted
SQLite beadstore. Real migration or rollback must have an operator-taken
`state.db` SQLite backup snapshot that was integrity-checked and recorded in
the operator log, unless a native restore primitive exists.

## Legacy Shadow Phase 8 Gate Sequence

This section is the old bd-primary shadow path. The current native-first
migration-day path is `docs/runbooks/beadstore-native-cutover.md`.
After Phase 10 native dispatcher/worker startup begins, production `oro start`
and `oro work` fail closed for `ORO_BEADSOURCE_MODE=cli` and `shadow`; the
commands below are retained only for pre-cutover legacy recovery.

Stop the dispatcher and every worker before the first dry-run. Keep them stopped until the real migration completes and the shadow-mode restart begins. Run these commands from the repo root before any real migration:

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

Expected results:

- `scripts/check-bd-version.sh` passes. If a waiver is required, use `scripts/check-bd-version.sh --ignore-version-drift` and record the waiver in the operator log. Although `--ignore-version-drift` appears in `migrate-from-dolt --help`, it is not implemented for initial migration.
- Run the gate block as one script, with `set -euo pipefail` active, so any failed `test` aborts before dry-run.
- Dispatcher count is `0` across both `oro start` and `oro dispatcher start` invocation paths. Stop all dispatchers before migration.
- Worker/writer process scan prints `active_writer_count=0`. If any worker, any direct `bd` process, direct native `oro task` mutator, legacy `oro bead` compatibility mutator, or another migration command is active, stop it before dry-run. The scanner inspects process command names and argv tokens instead of substring-matching the whole shell command, so macOS daemons such as `sbd` and `donotdisturbd` and the shell running this gate do not trip the gate. This intentionally treats read-only `bd` commands as stop-the-world conflicts so the gate cannot miss newly added bd mutators; otherwise `bd export`, `state.db` snapshotting, or migration apply can race an active writer.
- `ORO_BEADSOURCE_MODE` is empty or `cli`; the command block exits non-zero otherwise. Do not migrate while already in `shadow` or `sqlite`.
- Help output matches the actual migration flags listed below; do not add unimplemented backup toggles.
- Dry-run exits successfully without `--force-recover` and does not report `native bead table is not empty`. Initial migration is not a repair command for an existing native bead table; any pre-existing row, including a soft-deleted row, means the operator must restore or clear `state.db` through a reviewed rollback path before retrying.
- A `state.db` SQLite backup snapshot is created and integrity-checked before the real apply command. The migration JSONL backup is mandatory source/audit data, but it is not the rollback mechanism for a failed or partially populated SQLite beadstore.

## Actual Migration Flags

- `--dry-run`: preview only. It skips SQLite writes and migration locks.
- `--reconcile`: preview reconcile by default.
- `--reconcile --apply`: apply reconcile changes after review.
- `--dry-run --reconcile --apply`: still does not apply; `--dry-run` wins.
- `--from-jsonl <path>`: import from an operator-selected JSONL snapshot when Dolt is unrecoverable.
- `--from-fixture <path>`: test-fixture source for migration tests and drills, not the production migration source.
- `--ignore-version-drift`: present in help but unsupported by the initial migration path. Use `scripts/check-bd-version.sh --ignore-version-drift` for the approved version-check waiver and record the waiver in the operator log.
- `--allow-running-dispatcher`: emergency override for the dispatcher PID-lock gate. Do not use during real migration approval; stop the dispatcher instead.
- `--force-recover`: emergency partial-recovery acknowledgment. Do not use for real migration approval; a dry-run that requires it blocks Phase 8.

Initial apply refuses to run when the native `beads` table contains any rows. After that guard passes, it writes a mandatory source JSONL backup under `OroHome/migrations/<timestamp>-pre-migration.jsonl` before importing.

## Legacy Shadow Apply Path

This section continues the old bd-primary shadow path. For the current
native-first cutover, use `docs/runbooks/beadstore-native-cutover.md`.

Only after all gates pass:

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
snapshot_dir="$oro_home/migrations/$(date -u +%Y%m%dT%H%M%SZ)-pre-migration-state-db"
mkdir -m 0700 -p "$snapshot_dir"
sqlite3 "$state_db" 'PRAGMA wal_checkpoint(FULL);'
sqlite3 "$state_db" ".backup '$snapshot_dir/state.db'"
integrity=$(sqlite3 "$snapshot_dir/state.db" 'PRAGMA integrity_check;')
test "$integrity" = ok
printf 'pre-migration state.db snapshot: %s\n' "$snapshot_dir"

dispatcher_matches=$(ps ax -o pid=,command= | rg '([o]ro start|[o]ro dispatcher start)' || true)
dispatcher_count=$(printf '%s\n' "$dispatcher_matches" | awk 'NF { n++ } END { print n + 0 }')
printf 'dispatcher_count=%s\n' "$dispatcher_count"
test "$dispatcher_count" = 0
scripts/check-phase8-no-writers.py
mode=${ORO_BEADSOURCE_MODE-}
printf 'ORO_BEADSOURCE_MODE=%s\n' "$mode"
case "$mode" in "" | cli) ;; *) echo "ORO_BEADSOURCE_MODE must be empty or cli before migration" >&2; exit 1 ;; esac

./oro bead migrate-from-dolt
export ORO_BEADSOURCE_MODE=shadow
printf '%s\n' "${ORO_BEADSOURCE_MODE:?}"
```

After apply:

```bash
./oro task status
```

The integrity check must print `ok`. Record the snapshot path and the migration JSONL backup path in the operator log. Then restart the dispatcher from the environment where `ORO_BEADSOURCE_MODE=shadow` is exported, with the worker count explicitly chosen for the migration restart:

```bash
state_dir=$(dirname "$state_db")
pid_path=${ORO_PID_PATH:-"$state_dir/oro.pid"}
old_dispatcher_pid=$(cat "$pid_path" 2>/dev/null || true)
worker_count=<operator-selected restarted worker count>
test -n "$worker_count"
ORO_HUMAN_CONFIRMED=1 ./oro stop --force
bd dolt start
bd dolt status
ORO_BEADSOURCE_MODE=shadow ./oro dispatcher start --workers "$worker_count"

test -r "$pid_path"
dispatcher_pid=$(cat "$pid_path")
test -z "$old_dispatcher_pid" || test "$dispatcher_pid" != "$old_dispatcher_pid"
ps eww -p "$dispatcher_pid" | rg 'ORO_BEADSOURCE_MODE=shadow'
```

`./oro stop --force` can stop or flush the Dolt server that bd uses as the
shadow-mode primary. Restart and verify bd's Dolt server before starting the
shadow dispatcher, especially for a manual monitor start with `--workers 0`.
If `bd dolt status` does not show a reachable server, do not start shadow mode.

Every restarted worker subprocess must no longer have `bd` on `PATH`, and a controlled test task per restarted worker must prove workers emit native `oro task` commands rather than `bd` commands before normal work resumes. Worker logs are append-only, so record each worker log byte offset before assigning the controlled task and inspect only the new log segment:

```bash
worker_ids="<space-separated restarted dispatcher worker ids>"
for worker_id in $worker_ids; do
  worker_pid=<pid for worker_id>
  worker_path=$(ps eww -p "$worker_pid" | tr ' ' '\n' | awk -F= '$1=="PATH"{print substr($0, 6); exit}')
  test -n "$worker_path"
  if PATH="$worker_path" command -v bd >/dev/null 2>&1; then
    echo "bd still visible to worker PATH for $worker_id" >&2
    exit 1
  fi
  PATH="$worker_path" command -v oro >/dev/null
done

for worker_id in $worker_ids; do
  test_worker_log="$oro_home/workers/$worker_id/output.log"
  if [ -r "$test_worker_log" ]; then
    before_bytes=$(wc -c < "$test_worker_log" | tr -d ' ')
  else
    before_bytes=0
  fi
  test_task=<operator-created test task assigned to worker_id>
  test -n "$test_task"
  test -r "$test_worker_log"
  after_log=$(mktemp)
  tail -c +"$((before_bytes + 1))" "$test_worker_log" > "$after_log"
  rg --fixed-strings "$test_task" "$after_log"
  rg 'oro task (create|update|close|reopen|dep|deps|tag|defer|undefer|comment|note|meta)' "$after_log"
  ! rg 'oro bead (create|update|close|reopen|dep|deps|tag|defer|undefer|comment|note|meta)' "$after_log"
  ! rg '(^|[;&|[:space:]])bd([[:space:]]|$)' "$after_log"
  rm -f "$after_log"
done
```

## Legacy 24h Shadow Monitor Gate

The current Phase 8 gate is the native validation gate in
`docs/runbooks/beadstore-native-cutover.md`, not a 24-hour shadow soak. This
legacy gate is useful only if an operator deliberately chooses to keep bd as the
temporary primary for additional observation.

Run this gate only after the dispatcher and workers have restarted in `ORO_BEADSOURCE_MODE=shadow`. Set `ORO_DB_PATH` to the same `state.db` path recorded before initial apply so `./oro events` and the SQLite shadow-start check read the same database:

```bash
ORO_DB_PATH=<recorded Phase 8 state.db> ORO_BEADSOURCE_MODE=shadow scripts/check-beadstore-shadow-monitor.sh
```

The script runs with `set -euo pipefail`, reads `kv_store.beadstore_shadow_started_at`, requires the shadow window to be at least 24 hours old, then runs a minimal `./oro events --type=beadstore_divergence --since=24h --limit=1` smoke check so unreadable event logs fail closed. The blocking count is an uncapped SQLite query over the same `events` table using `json_extract(payload, '$.kind')` for `"real"` and `"drift"` payloads. It must fail if `./oro events` cannot open the state database, if the persisted shadow window is missing or younger than 24 hours, or if any real divergence payload is present anywhere in the last 24 hours. Drift payloads are counted for visibility only.

## Reconcile Path

During shadow, SQLite intentionally drifts because bd remains authoritative for writes. Preview reconcile first:

```bash
set -euo pipefail

dispatcher_matches=$(ps ax -o pid=,command= | rg '([o]ro start|[o]ro dispatcher start)' || true)
dispatcher_count=$(printf '%s\n' "$dispatcher_matches" | awk 'NF { n++ } END { print n + 0 }')
printf 'dispatcher_count=%s\n' "$dispatcher_count"
test "$dispatcher_count" = 0
scripts/check-phase8-no-writers.py
mode=${ORO_BEADSOURCE_MODE-}
printf 'ORO_BEADSOURCE_MODE=%s\n' "$mode"
test "$mode" = shadow
./oro bead migrate-from-dolt --reconcile
```

Dispatcher count must be `0` across both dispatcher invocation paths, `ORO_BEADSOURCE_MODE` must be exactly `shadow`, and the worker/writer process scan must print `active_writer_count=0`. Apply only after the preview is reviewed and conflict-free, with the dispatcher, workers, direct `bd` processes, direct native `oro task` mutators, legacy `oro bead` compatibility mutators, and other migration commands still stopped:

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
snapshot_dir="$oro_home/migrations/$(date -u +%Y%m%dT%H%M%SZ)-pre-reconcile-state-db"
mkdir -m 0700 -p "$snapshot_dir"
sqlite3 "$state_db" 'PRAGMA wal_checkpoint(FULL);'
sqlite3 "$state_db" ".backup '$snapshot_dir/state.db'"
integrity=$(sqlite3 "$snapshot_dir/state.db" 'PRAGMA integrity_check;')
test "$integrity" = ok
printf 'pre-reconcile state.db snapshot: %s\n' "$snapshot_dir"

dispatcher_matches=$(ps ax -o pid=,command= | rg '([o]ro start|[o]ro dispatcher start)' || true)
dispatcher_count=$(printf '%s\n' "$dispatcher_matches" | awk 'NF { n++ } END { print n + 0 }')
printf 'dispatcher_count=%s\n' "$dispatcher_count"
test "$dispatcher_count" = 0
scripts/check-phase8-no-writers.py
mode=${ORO_BEADSOURCE_MODE-}
printf 'ORO_BEADSOURCE_MODE=%s\n' "$mode"
test "$mode" = shadow

./oro bead migrate-from-dolt --reconcile --apply
```

Re-run the dispatcher/writer/mode gate immediately before initial apply and immediately before reconcile apply, even if the same gate already passed before dry-run or preview. A writer that appears after preview is a stop-the-world failure.

Reconcile apply writes a pre-reconcile backup under `OroHome/migrations/<timestamp>-pre-reconcile-sqlite.jsonl`.
The integrity check must print `ok`; record both the SQLite backup snapshot path and the JSONL pre-reconcile backup path.

## Rollback and Recovery

Migration aborted before import:

- Dry-run or preflight failure leaves SQLite unchanged.
- Do not run real migration until the dry-run passes without `--force-recover`.

Migration aborted mid-import:

- Legacy-only: leave `ORO_BEADSOURCE_MODE=cli`. After Phase 10 native
  dispatcher/worker startup begins, keep production stopped instead of trying to
  restart in `cli`.
- Keep dispatcher, workers, direct `bd` processes, direct native `oro task` mutators, legacy `oro bead` compatibility mutators, and other migration commands stopped until the failed `state.db` is restored or moved aside and integrity-checked.
- Preserve the command transcript, the `OroHome/migrations/<timestamp>-pre-migration.jsonl` backup, and the failed `state.db`.
- The migration validates rows before opening SQLite, but row-level insert failures can be collected and committed for other rows before the command returns a validation error. Treat any non-zero migration error count as a partial import unless proven otherwise.
- Do not rerun real migration against the same failed `state.db`. Restore a known-good pre-migration `state.db` SQLite backup snapshot or move the failed DB aside before retrying the full gate sequence.

Bad initial import:

- Legacy-only: set `ORO_BEADSOURCE_MODE=cli`. After Phase 10 native
  dispatcher/worker startup begins, keep production stopped and use the
  recorded SQLite backup path instead.
- Preserve `OroHome/migrations/<timestamp>-pre-migration.jsonl` and the command transcript.
- Keep all writers stopped. Restore the recorded pre-migration `state.db` SQLite backup snapshot, moving the failed DB aside first:

```bash
set -euo pipefail

state_db=<same state_db path recorded before apply>
snapshot_dir=<recorded pre-migration-state-db snapshot dir>
failed_dir="$(dirname "$state_db")/failed-migration-$(date -u +%Y%m%dT%H%M%SZ)"
mkdir -m 0700 -p "$failed_dir"
for suffix in "" -wal -shm; do
  test ! -e "$state_db$suffix" || mv "$state_db$suffix" "$failed_dir/state.db$suffix"
done
cp -p "$snapshot_dir/state.db" "$state_db"
integrity=$(sqlite3 "$state_db" 'PRAGMA integrity_check;')
test "$integrity" = ok
printf 'restored state.db from %s; failed DB moved to %s\n' "$snapshot_dir" "$failed_dir"
```

- Restart dispatcher and workers only after the restored `state.db` passes integrity check.
- Fix the importer or source data, then rerun dry-run before any apply.

If the recorded pre-migration SQLite snapshot is itself non-empty, as happened
on 2026-04-30 with stale row `oro-cdb3`, restoring that snapshot alone is not
enough. The initial migration guard will still fail. In that case, preserve the
failed database, keep all writers stopped, and clear only the native beadstore
tables before retrying the full gate sequence:

```bash
set -euo pipefail

state_db=<same state_db path recorded before apply>
snapshot_dir=<recorded pre-migration-state-db snapshot dir>
recovery_dir="$(dirname "$state_db")/failed-migration-clear-$(date -u +%Y%m%dT%H%M%SZ)"
mkdir -m 0700 -p "$recovery_dir"

dispatcher_matches=$(ps ax -o pid=,command= | rg '([o]ro start|[o]ro dispatcher start)' || true)
dispatcher_count=$(printf '%s\n' "$dispatcher_matches" | awk 'NF { n++ } END { print n + 0 }')
printf 'dispatcher_count=%s\n' "$dispatcher_count"
test "$dispatcher_count" = 0
scripts/check-phase8-no-writers.py
mode=${ORO_BEADSOURCE_MODE-}
printf 'ORO_BEADSOURCE_MODE=%s\n' "$mode"
case "$mode" in "" | cli) ;; *) echo "ORO_BEADSOURCE_MODE must be empty or cli before clearing target" >&2; exit 1 ;; esac

sqlite3 -bail "$state_db" 'PRAGMA wal_checkpoint(FULL);'
sqlite3 -bail "$state_db" ".backup '$recovery_dir/failed-state.db'"
integrity=$(sqlite3 -bail "$recovery_dir/failed-state.db" 'PRAGMA integrity_check;')
test "$integrity" = ok
snapshot_count=$(sqlite3 -bail "$snapshot_dir/state.db" 'SELECT COUNT(*) FROM beads;')
printf 'recorded pre-migration snapshot bead count: %s\n' "$snapshot_count"

sqlite3 -bail "$state_db" <<'SQL'
PRAGMA trusted_schema=ON;
PRAGMA foreign_keys=ON;
BEGIN IMMEDIATE;
DELETE FROM bead_notes;
DELETE FROM bead_metadata;
DELETE FROM bead_labels;
DELETE FROM bead_tags;
DELETE FROM bead_deps;
DELETE FROM beads;
INSERT INTO beads_fts(beads_fts) VALUES('rebuild');
DELETE FROM kv_store WHERE key = 'beadstore_shadow_started_at';
COMMIT;
PRAGMA integrity_check;
SELECT 'beads', COUNT(*) FROM beads;
SELECT 'bead_deps', COUNT(*) FROM bead_deps;
SELECT 'bead_tags', COUNT(*) FROM bead_tags;
SELECT 'bead_labels', COUNT(*) FROM bead_labels;
SELECT 'bead_metadata', COUNT(*) FROM bead_metadata;
SELECT 'bead_notes', COUNT(*) FROM bead_notes;
SQL
```

The integrity check must print `ok`, and every native beadstore table count must
be `0`. `PRAGMA trusted_schema=ON` is required for this one-shot operator
cleanup because the native bead schema includes FTS triggers on `beads`; without
it, the SQLite CLI can reject the delete with `unsafe use of virtual table
"beads_fts"`. Record `recovery_dir`, `snapshot_count`, and the table-count
output in the operator log. Then rerun the complete Phase 8 gate sequence from
the top, starting with `scripts/check-bd-version.sh` and a fresh dry-run. Do not
set `ORO_BEADSOURCE_MODE=shadow`, run reconcile, or restart dispatcher/workers
until the real migration has subsequently completed cleanly.

Bad reconcile apply:

- Preserve `OroHome/migrations/<timestamp>-pre-reconcile-sqlite.jsonl`; it is a JSONL snapshot for audit/recovery tooling, not a shipped in-place restore command.
- Restore `state.db` from the operator's pre-reconcile SQLite backup snapshot. If no such snapshot exists, stop and implement/review a restore procedure before continuing:

```bash
set -euo pipefail

state_db=<same state_db path recorded before reconcile apply>
snapshot_dir=<recorded pre-reconcile-state-db snapshot dir>
failed_dir="$(dirname "$state_db")/failed-reconcile-$(date -u +%Y%m%dT%H%M%SZ)"
mkdir -m 0700 -p "$failed_dir"
for suffix in "" -wal -shm; do
  test ! -e "$state_db$suffix" || mv "$state_db$suffix" "$failed_dir/state.db$suffix"
done
cp -p "$snapshot_dir/state.db" "$state_db"
integrity=$(sqlite3 "$state_db" 'PRAGMA integrity_check;')
test "$integrity" = ok
printf 'restored state.db from %s; failed DB moved to %s\n' "$snapshot_dir" "$failed_dir"
```

- Legacy-only: keep `ORO_BEADSOURCE_MODE=shadow` or revert to `cli`. After
  Phase 10 native dispatcher/worker startup begins, those modes are forensic
  reference modes only and production start/work reject them.
- Fix reconcile, rerun `--reconcile`, then rerun `--reconcile --apply`.

Critical bug after sqlite cutover:

- Stop dispatcher and workers.
- Export the current SQLite beadstore with `oro task export` and reconcile bd from that export using bd's import path before switching authority back:

```bash
set -euo pipefail

rollback_export=".oro/exports/sqlite-rollback-$(date -u +%Y%m%dT%H%M%SZ).jsonl"
mkdir -p "$(dirname "$rollback_export")"
oro task export --out="$rollback_export"
bd import --dry-run "$rollback_export"
bd import "$rollback_export"
```

- Do not restart production dispatcher/workers with `ORO_BEADSOURCE_MODE=cli`
  after Phase 10 native startup begins; production start/work reject legacy
  modes. If bd must be used temporarily, run a reviewed pre-cutover binary or
  branch outside the production dispatcher/worker path after bd has the
  SQLite-side writes that occurred during Phase 9.
- Keep bd installed and Dolt data intact until Phase 10 cleanup is complete.

Dolt destroyed during shadow:

- Stop dispatcher and workers.
- There is no SQLite-only reconcile promotion path.
- Restore Dolt through the Dolt/bd recovery path or choose an operator-reviewed JSONL snapshot with `--from-jsonl <path>`.
- Rerun the gate with the same source you intend to apply. For a JSONL fallback, use `./oro bead migrate-from-dolt --dry-run --from-jsonl <path>`.
- Apply `./oro bead migrate-from-dolt --from-jsonl <path>` only against a clean target DB after the JSONL dry-run passes; it is not a repair command for an already-populated corrupted SQLite store.

## Operator Log

Capture:

- command transcript for every gate command;
- `bd --version` and `scripts/check-bd-version.sh` result;
- dispatcher count result;
- `ORO_BEADSOURCE_MODE` value;
- dry-run report;
- backup paths written under `OroHome/migrations/`;
- pre-migration and pre-reconcile `state.db` SQLite backup snapshot paths;
- any version-drift waiver, since durable waiver event rows are not implemented.
