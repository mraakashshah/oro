# Deep Observation Guide

Per-component techniques for observing an oro swarm in depth. All methods are event-driven (tail/follow/fswatch) — avoid sleep-poll loops.

## 1. Dispatcher

The dispatcher is the central orchestrator. Observe via its event log and UDS socket.

### Event stream (primary)
```bash
# Real-time event stream — never exits, no sleep
./oro logs --follow

# Filter to specific event types
./oro logs --follow 2>&1 | grep -E 'ASSIGN|DONE|MERGE|ESCALATION'

# Per-worker events
./oro logs worker-1 --follow
```

### Status snapshot
```bash
./oro status
# Shows: state, worker count, queue depth, assignments, alerts

./oro directive status
# Richer: sent via UDS, includes focus, health
```

### Daemon process log
```bash
# Stderr from the daemon process (panics, fatal errors surface here)
tail -f /tmp/oro-daemon.log
```

### Database forensics
```bash
DB="$HOME/.oro/state.db"

# Recent events with payloads
sqlite3 "$DB" "SELECT type, bead_id, worker_id, substr(payload,1,80), created_at
  FROM events ORDER BY created_at DESC LIMIT 30;"

# Assignment history
sqlite3 "$DB" "SELECT bead_id, worker_id, status, attempt_count, assigned_at
  FROM assignments ORDER BY assigned_at DESC LIMIT 20;"

# Escalation queue (unacked)
sqlite3 "$DB" "SELECT id, type, bead_id, worker_id, message, created_at
  FROM escalations WHERE status='pending' ORDER BY created_at;"

# KV store (dispatcher runtime state)
sqlite3 "$DB" "SELECT * FROM kv_store;"

# Watch DB for changes (macOS)
fswatch -r "$DB" | while read -r _; do
  echo "=== $(date +%H:%M:%S) ==="
  sqlite3 "$DB" "SELECT type, bead_id, worker_id FROM events ORDER BY created_at DESC LIMIT 5;"
done
```

## 2. Workers

Workers are `oro worker` processes. Each writes to `~/.oro/workers/<id>/output.log`.

### Tail all workers (interleaved)
```bash
# Tail all existing worker logs
tail -f ~/.oro/workers/*/output.log

# With filename headers (GNU tail)
tail -f --verbose ~/.oro/workers/*/output.log

# Follow a specific worker
./oro logs worker-1 --raw --follow
```

### Worker health signals
```bash
# Heartbeats include context percentage — extract from events
sqlite3 ~/.oro/state.db \
  "SELECT worker_id, json_extract(payload,'$.context_pct'), created_at
   FROM events WHERE type='HEARTBEAT' ORDER BY created_at DESC LIMIT 20;"

# Workers with no heartbeat in 45s = dead
sqlite3 ~/.oro/state.db \
  "SELECT worker_id, max(created_at) as last_hb
   FROM events WHERE type='HEARTBEAT'
   GROUP BY worker_id
   HAVING (strftime('%s','now') - strftime('%s',last_hb)) > 45;"

# Context burn rate (context % over time for a worker)
sqlite3 ~/.oro/state.db \
  "SELECT created_at, json_extract(payload,'$.context_pct')
   FROM events WHERE type='HEARTBEAT' AND worker_id='worker-1'
   ORDER BY created_at DESC LIMIT 20;"
```

### Worker lifecycle events
```bash
# Track a worker through its lifecycle
sqlite3 ~/.oro/state.db \
  "SELECT type, bead_id, substr(payload,1,60), created_at
   FROM events WHERE worker_id='worker-1' ORDER BY created_at;"
```

### Worktree state
```bash
# List active worktrees
git worktree list

# Check worktree contents
ls .worktrees/

# Watch for worktree creation/deletion (macOS)
fswatch .worktrees/ 2>/dev/null | while read -r path; do
  echo "Worktree change: $path"
  git worktree list
done
```

## 3. Manager Pane

The manager is a Claude session in `oro:1` (window 1). It receives `[ORO-DISPATCH]` messages from the dispatcher and can issue directives.

### Scrape pane content
```bash
# Last 200 lines
tmux capture-pane -t oro:1 -p -S -200

# Watch for dispatch messages
tmux capture-pane -t oro:1 -p -S -200 | grep '\[ORO-DISPATCH\]'

# Continuous mirror (refreshes on DB changes)
while true; do
  tmux capture-pane -t oro:1 -p -S -40 2>/dev/null
  echo "--- $(date +%H:%M:%S) ---"
  fswatch -1 ~/.oro/state.db 2>/dev/null || sleep 5
done
```

### Manager activity tracking
```bash
# When was manager last active?
sqlite3 ~/.oro/state.db "SELECT * FROM pane_activity WHERE pane='manager';"
```

## 4. Architect Pane

The architect is a Claude session in `oro:0` (window 0). It creates beads, plans work, and manages the dependency graph.

### Scrape pane content
```bash
tmux capture-pane -t oro:0 -p -S -200

# Watch for bead creation
tmux capture-pane -t oro:0 -p -S -200 | grep -E 'bd create|bd dep'
```

### Architect activity tracking
```bash
sqlite3 ~/.oro/state.db "SELECT * FROM pane_activity WHERE pane='architect';"
```

## 5. Dashboard

The TUI dashboard is a separate binary `oro-dash`.

### Launch dashboard
```bash
# In a separate terminal or tmux pane
oro-dash

# Or in the monitoring session
tmux split-window -t oro-watch "oro-dash"
```

### Dashboard data sources
The dashboard reads from the same `state.db` and `bd` CLI — so all observation techniques above feed it too.

## 6. Bead Behavior

### Watch bead state transitions
```bash
# Current in-progress beads
bd list --status=in_progress

# Detailed bead view
bd show <bead-id>

```

### Bead assignment tracking
```bash
# Which bead is assigned to which worker?
sqlite3 ~/.oro/state.db \
  "SELECT bead_id, worker_id, status, attempt_count
   FROM assignments WHERE status != 'completed';"

# Beads that have been assigned more than once (retries)
sqlite3 ~/.oro/state.db \
  "SELECT bead_id, count(*) as assigns, max(attempt_count) as attempts
   FROM assignments GROUP BY bead_id HAVING assigns > 1;"

# Bead completion timeline
sqlite3 ~/.oro/state.db \
  "SELECT bead_id, type, created_at FROM events
   WHERE type IN ('ASSIGN','DONE','QG_FAILED','MERGE_CONFLICT','MERGED')
   ORDER BY created_at DESC LIMIT 30;"
```

### Quality gate tracking
```bash
# QG results
sqlite3 ~/.oro/state.db \
  "SELECT bead_id, worker_id, json_extract(payload,'$.qg_result'), created_at
   FROM events WHERE type='DONE' ORDER BY created_at DESC LIMIT 10;"

# QG failures and retries
sqlite3 ~/.oro/state.db \
  "SELECT bead_id, worker_id, substr(payload,1,80), created_at
   FROM events WHERE type='QG_FAILED' ORDER BY created_at DESC;"
```

## 7. Failure Pattern Recognition

### Event-driven alert stream
```bash
# Tail events and highlight problems
./oro logs --follow 2>&1 | grep --color=always -E \
  'ESCALATION|STUCK|CRASH|CONFLICT|FAILED|PANIC|fatal|error'
```

### Key failure signatures

| What to grep | Means |
|-------------|-------|
| `STUCK_WORKER` | No progress for 10min — kill or investigate |
| `WORKER_CRASH` | Process died — check worker output.log |
| `QG_FAILED` repeating for same bead | Worker can't pass quality gate |
| `MERGE_CONFLICT` without `MERGED` | Unresolved conflict — needs manual intervention |
| `escalation_failed` | Manager pane unreachable |
| `ASSIGN` spam (same bead) | Assignment loop — check rejection counts |
| `context_pct > 80` in heartbeats | Worker running low on context |

### Quick health check (one-shot)
```bash
echo "=== Dispatcher ===" && ./oro status
echo "=== Escalations ===" && ./oro directive pending-escalations
echo "=== Stuck Workers ===" && sqlite3 ~/.oro/state.db \
  "SELECT worker_id, bead_id, created_at FROM events
   WHERE type='STUCK_WORKER' AND created_at > datetime('now','-1 hour');"
echo "=== Recent Failures ===" && sqlite3 ~/.oro/state.db \
  "SELECT type, bead_id, worker_id, created_at FROM events
   WHERE type IN ('QG_FAILED','WORKER_CRASH','MERGE_CONFLICT','ESCALATION')
   ORDER BY created_at DESC LIMIT 10;"
```

## 8. UDS Socket Observation

The dispatcher communicates with workers via `~/.oro/oro.sock` (Unix domain socket, line-delimited JSON).

### Probe socket liveness
```bash
# Quick liveness check
echo '{"type":"DIRECTIVE","directive":{"action":"status"}}' | socat - UNIX-CONNECT:~/.oro/oro.sock

# Or via the CLI
./oro directive health
```

### Intercept socket traffic (read-only, for debugging)
```bash
# WARNING: This replaces the socket — only for debugging stopped swarms
# Move original socket, create proxy that logs traffic
mv ~/.oro/oro.sock ~/.oro/oro.sock.real
socat -v UNIX-LISTEN:~/.oro/oro.sock,fork UNIX-CONNECT:~/.oro/oro.sock.real 2>&1 | tee /tmp/oro-socket-trace.log
# Remember to restore: mv ~/.oro/oro.sock.real ~/.oro/oro.sock
```

**Safer alternative**: All socket messages are logged as events in `state.db`. Use the database queries above instead.

## 9. Memory System

```bash
# Recent memories
sqlite3 ~/.oro/state.db \
  "SELECT id, type, substr(content,1,80), source, created_at
   FROM memories ORDER BY created_at DESC LIMIT 20;"

# Memories from a specific worker/bead
sqlite3 ~/.oro/state.db \
  "SELECT substr(content,1,100), type, confidence
   FROM memories WHERE bead_id='<bead-id>';"

# Search memories
sqlite3 ~/.oro/state.db \
  "SELECT substr(content,1,100), rank FROM memories_fts
   WHERE memories_fts MATCH 'search term' LIMIT 10;"
```
