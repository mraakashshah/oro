# Native Task Store Recovery

Use this runbook when Oro's native SQLite task store is unreadable, inconsistent,
or suspected of losing task state.

## Invariants

- `oro task` is the canonical task interface.
- Do not recreate task history from a legacy tracker.
- Do not delete or replace `state.db` until a timestamped backup exists.
- Stop the dispatcher before mutating task-store files.
- Prefer preserving ambiguous state over making it disappear.

## 1. Stop Writers

```bash
ORO_HUMAN_CONFIRMED=1 oro stop --force
oro status --json
```

Proceed only when `active_count` is `0`.

## 2. Locate The Store

```bash
oro status --json | jq -r '.database.path // empty'
oro health --json
```

If status cannot open the database, resolve the expected path from
`ORO_DB_PATH` or the project entry under `~/.oro/projects/`.

## 3. Back Up Before Repair

```bash
db=<state.db path>
cp "$db" "$db.backup.$(date -u +%Y%m%dT%H%M%SZ)"
```

If the database has sidecar files, copy those too:

```bash
for suffix in -wal -shm; do
  [ -e "$db$suffix" ] && cp "$db$suffix" "$db$suffix.backup.$(date -u +%Y%m%dT%H%M%SZ)"
done
```

## 4. Inspect SQLite Integrity

```bash
sqlite3 "$db" 'PRAGMA integrity_check;'
sqlite3 "$db" 'PRAGMA foreign_key_check;'
```

`integrity_check` must print `ok`. Any other output is evidence to preserve in
the incident notes before attempting repair.

## 5. Inspect Task State

```bash
sqlite3 "$db" "select id,status,title from beads order by updated_at desc limit 20;"
sqlite3 "$db" "select bead_id,status,worker_id from assignments order by updated_at desc limit 20;"
oro recovery list --json
oro ops list --json | jq '[.[] | select(.status=="running" or .status=="failed")]'
```

The table names are internal storage names. Operators should still use
`oro task` for normal workflows.

## 6. Repair Conservatively

- For open recovery quarantines, inspect first with `oro recovery inspect <id>`.
- Resolve only when the branch/worktree state is understood.
- For failed ops runs, use `oro ops resolve <id> --reason '<evidence>'` only
  after verifying the work was completed or superseded.
- For task rows stuck `in_progress` with no active assignment and no preserved
  worktree, requeue through the Oro CLI instead of editing SQLite directly.

## 7. Verify

```bash
oro health --json
oro status --json
oro task ready --json
sqlite3 "$db" 'PRAGMA integrity_check;'
```

The store is ready for normal operation when health is not unsafe, integrity is
`ok`, no unexpected active assignments remain, and ready/blocked tasks match the
operator's expectation.
