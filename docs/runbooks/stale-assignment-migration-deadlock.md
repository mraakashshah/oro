# Stale-Assignment Migration Deadlock Runbook

Use this when `oro start` refuses to launch with an error like:

```
migrate_v4: cannot migrate while N active assignments exist; run 'oro stop' first then re-run 'oro start'
```

## The deadlock

Stale `status='active'` assignment rows — left behind when a dispatcher (or an
entire swarm) died without closing them — block the v3→v4 schema migration:
`ensureNoActiveAssignments` hard-errors while any active assignment exists. The
migration runs at DB-open, *before* the dispatcher's own `startupRecovery` can
quarantine stale rows. So:

```
stale 'active' assignments → v4 migration refuses → oro start aborts
    → dispatcher never starts → it can never clean the rows blocking it
```

This is **stale bookkeeping, not corruption**: `PRAGMA integrity_check` and
`PRAGMA foreign_key_check` pass. The normal online cleanup path also cannot
resolve **orphan** rows — active assignments whose `beads` row no longer exists
(e.g. lost in an old bd/Dolt destruction before the SQLite replatform) — because
it keys on existing beads.

## Confirm the diagnosis

Read-only inspection of the affected project's store
(`~/.oro/projects/<name>/state.db`):

```sh
sqlite3 -readonly state.db "PRAGMA user_version;"          # typically 0 (unmigrated)
sqlite3 -readonly state.db "PRAGMA integrity_check;"       # expect: ok
sqlite3 -readonly state.db "PRAGMA foreign_key_check;"     # expect: no rows
sqlite3 -readonly state.db "SELECT COUNT(*) FROM assignments WHERE status='active';"
```

Break down active assignments into bead-backed vs orphan:

```sh
sqlite3 -readonly state.db "
SELECT CASE WHEN b.id IS NULL THEN 'orphan (bead missing)'
            ELSE 'bead status=' || b.status END AS category, COUNT(*)
FROM assignments a LEFT JOIN beads b ON b.id=a.bead_id
WHERE a.status='active' GROUP BY category;"
```

To root-cause an orphan, read its event trail (the assignment history survives
even when the bead row is gone):

```sh
sqlite3 state.db "SELECT created_at, type, source, substr(payload,1,400)
FROM events WHERE bead_id='<orphan-id>' ORDER BY id;"
```

A `shutdown_reset_bead_failed` event naming a missing Dolt database
(`database "<project>" not found on Dolt server`) means the bead was lost at the
legacy Dolt layer while its assignment persisted in SQLite — a permanent orphan
with nothing to recover.

## Break the deadlock

Confirm no dispatcher is live for the project (stale `oro.pid` with no `oro.sock`
is safe), then run the guarded offline recovery. It refuses if a dispatcher is
running, backs up `state.db` (+ `-wal`/`-shm`) to a timestamped copy before any
write, opens the store **without** running the migration, and quarantines every
active assignment in one transaction. It only touches `assignments` and
`recovery_quarantines` — never bead/task rows.

```sh
# Target a specific project's store via env overrides (no cd required):
ORO_DB_PATH=~/.oro/projects/<name>/state.db \
ORO_PID_PATH=~/.oro/projects/<name>/oro.pid \
ORO_SOCKET_PATH=~/.oro/projects/<name>/oro.sock \
ORO_HUMAN_CONFIRMED=1 oro recovery abandon-stale --force
```

Without `--force` the command prompts for an interactive `YES`. Bead-backed rows
are quarantined with reason `stale_active_assignment`; orphans get
`orphan_bead_assignment` so they triage separately.

After it runs, `active` count is 0, the v4 migration is unblocked, and the
quarantined rows are visible to `oro recovery list`.

## Finish the quarantines

For a safe dry run first, copy the store to a scratch dir and run the command
against the copy (all three `ORO_*` paths pointed at the copy) before touching
the real store.

Resolve each quarantine after preserving, merging, or intentionally discarding
its work — see [forensic-safe-recovery.md](forensic-safe-recovery.md) for the
per-row preservation flow and the full `--mode` reference
(`requeue-preserved`, `resolved-after-merge`, `human-owned`, `discard-empty-safe`).

When the branch **and** worktree are both gone (common for old stranded rows),
there is no work to lose:

```sh
oro recovery resolve <id> --mode discard-empty-safe
```

`discard-empty-safe` refuses if the branch still exists and is ahead, so it
cannot silently drop unmerged work. Orphan rows (missing bead, gone branch, gone
worktree) are always safe to discard once root-caused.

Verify clean:

```sh
oro recovery list                                          # No open recovery quarantines.
sqlite3 state.db "SELECT COUNT(*) FROM assignments WHERE status='active';"   # 0
```

The dispatcher pauses assignment while an open recovery quarantine still has a
branch or worktree to preserve. Empty-safe rows with neither do not freeze new
work, but should still be resolved so health reporting returns clean.

## Prevention

The failure mode that strands orphans (bead state in Dolt, assignment state in
SQLite, no transaction across the boundary) is retired: the native SQLite
beadstore keeps beads and assignments in one transactional DB. New stale-active
rows are quarantined automatically by the running dispatcher's `startupRecovery`
/ stale-assignment sweep; this runbook is for stores that cannot reach that path
because they are still unmigrated (`user_version < 4`).
