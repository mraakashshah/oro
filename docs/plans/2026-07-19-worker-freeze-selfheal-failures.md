# Spec: Wedged workers never self-heal — reaper blind spot + phantom quarantine

**Date:** 2026-07-19 · **Priority:** P0 (factory reliability) · **Author:** operator + Claude

## Incident

Both managed workers (`oro-vawk`, `oro-k8zp`) sat frozen for **~2.5 hours** occupying
both worker slots, producing **zero merges**. Signature per worker:

- `codex exec` process alive the whole time (2h07m / 2h28m elapsed).
- Work **committed** (coding done — e.g. vawk had 10 commits) but **never merged/closed**;
  wedged in the QG / ops-review / merge phase.
- **No new commit and no file change** for 2.5h; bead `last_heartbeat=None`,
  `updated_at` frozen.
- **No `progress_timeout` / `STUCK_WORKER` event** ever fired.
- Dispatcher logged `gc_skipped_recovery_quarantined` for the bead, yet
  `oro recovery list` reported **"No open recovery quarantines."**

Net effect: a wedged worker **never self-heals** — it is neither reaped nor visible
for manual recovery. Only a manual `oro directive restart-worker` cleared it. This
makes any single-worker hang a permanent slot loss until a human notices.

## Root cause 1 — progress-timeout reaper has a phase blind spot

`workerProgressTimedOut` (`pkg/dispatcher/worker_pool.go`):

```go
func workerProgressTimedOut(w *trackedWorker, now time.Time, timeout time.Duration) bool {
    return w.state == protocol.WorkerBusy && !w.lastProgress.IsZero() && now.Sub(w.lastProgress) > timeout
}
```

Two defects combine so it never fired:

1. **State-gated to `WorkerBusy`.** The frozen workers had left `WorkerBusy` (coding
   done, committed) and were in the review/QG/merge phase (`WorkerReviewing`). The
   predicate returns `false` for any non-`WorkerBusy` state, so a worker wedged in
   review/QG/merge is **outside the reaper's coverage entirely**.
2. **Context-creep defeats the flat-context guard.** `handleHeartbeat`
   (`dispatcher.go:1857`) only treats a heartbeat as progress when `ContextPct`
   changed, to avoid flat-context liveness resetting `lastProgress` (fix for oro-16yy).
   But a spinning codex session's `ContextPct` **creeps upward every heartbeat**, so
   `lastProgress` keeps getting bumped even with zero real work — the guard assumes
   "context moved ⇒ progress," which is false for a hung-but-churning session.

**Fix invariant:** a worker with no *real* progress (STATUS/DONE/READY_FOR_REVIEW/QG
transition — not bare heartbeats, not context drift) for `ProgressTimeout` must be
detected and reaped **regardless of phase** (busy, reviewing, QG, merging). Consider
a separate/absolute wall-clock cap for the post-`WorkerBusy` phases so review/QG/merge
hangs are bounded too.

## Root cause 2 — GC and `oro recovery list` disagree on "open"

GC / assignment-blocking query (`dispatcher.go:openRecoveryQuarantineBeads`, ~5794):

```sql
SELECT DISTINCT q.bead_id FROM recovery_quarantines q
LEFT JOIN assignments a ON a.id=q.assignment_id
WHERE q.status IN ('open','human_owned') OR (q.status='resolved' AND a.status='requeued')
```

Operator-facing query (`recovery_quarantine.go:listOpenRecoveryQuarantines`, ~268),
which backs `oro recovery list`:

```sql
SELECT ... FROM recovery_quarantines WHERE status='open' ORDER BY id
```

A quarantine in `human_owned` **or** `resolved`-with-`requeued`-assignment therefore
**blocks GC/reaping** (`gc_skipped_recovery_quarantined`) while being **invisible** to
the operator. The operator sees "No open recovery quarantines," cannot resolve what
they cannot see, and the slot stays wedged.

**Fix invariant:** `oro recovery list` must surface **every** quarantine that blocks
GC/assignment (reconcile the two predicates, or share one), with a state label so the
operator knows *why* it blocks and can clear it.

Relates to `oro-br5g` (recovery quarantines silently freezing assignment) and
`oro-sr6w` (auto-resolve empty-safe quarantines).

## Acceptance (per bug task)

See the two P0 bug tasks filed against this spec; each carries a machine-verifiable
`Test:`/`Cmd:`/`Assert:` derived from the fix invariants above.
