# Design: Swarm Health Dashboard & Manual Daemon Restart

Date: 2026-02-16

## Problem

Manager needs visibility into the health of all oro components (daemon, architect pane, manager pane, workers) and the ability to manually restart the daemon when issues arise. Currently:
- No centralized health view
- No way to restart dispatcher without `oro stop && oro start` (kills tmux sessions)
- Manager relies on indirect signals (stuck workers, missing ACKs) to detect daemon issues

## Decision: Dashboard Health Panel + Manual Restart

**Chosen over:** Automated supervisor-based restart (complex, uncertain failure modes)

**Rationale:** Human-in-the-loop is simpler, more reliable for rare restarts. Dashboard provides observability; manual restart provides control.

## Design

### 1. Health Status Data Model

Add status query to dispatcher that returns:

```go
type SwarmHealth struct {
    Daemon       DaemonStatus
    ArchitectPane PaneStatus
    ManagerPane   PaneStatus
    Workers      []WorkerStatus // existing
}

type DaemonStatus struct {
    PID      int
    Uptime   time.Duration
    State    string // "running", "paused", etc.
}

type PaneStatus struct {
    Name         string // "architect" | "manager"
    Alive        bool   // tmux pane exists
    LastActivity time.Time // last SessionStart or command
}
```

**Implementation:**
- Add `health` directive that returns SwarmHealth as JSON
- Dispatcher queries tmux for pane existence: `tmux list-panes -t oro -F '#{pane_id} #{pane_title}'`
- Track pane activity via SessionStart hook writes to SQLite
- Existing WorkerStatus already has health data (heartbeat, state)

### 2. Dashboard Health View

Add "Health" tab (or top-level panel) to oro-dash showing:

```
╭─ Swarm Health ────────────────────────────────╮
│ Daemon:     ● Running (PID 12345, 2h 34m)    │
│ Architect:  ● Active (last seen 5m ago)      │
│ Manager:    ● Active (last seen 1m ago)      │
│ Workers:    3/3 healthy                       │
│                                                │
│ [Restart Daemon]  [Restart Panes]             │
╰────────────────────────────────────────────────╯
```

**Indicators:**
- ● Green = healthy (daemon running, pane alive, worker heartbeat <30s)
- ◐ Yellow = degraded (pane alive but no activity >5min)
- ○ Red = dead (pane missing, worker timeout)

**Refresh:** Poll `health` directive every 2s while Health tab active.

### 3. Manual Daemon Restart

Add `restart-daemon` directive (manager → dispatcher):

**Flow:**
1. Manager sends `restart-daemon` directive via oro CLI or dashboard button
2. Dispatcher ACKs with "restarting in 5s"
3. Dispatcher broadcasts PREPARE_SHUTDOWN to workers (drain in-progress beads)
4. Wait up to 60s for workers to finish or save progress
5. Close DB, close socket, exit with code 0
6. Manager detects disconnect (ACK received, then connection drops)
7. Manager spawns new daemon: `oro start --daemon-only --workers <N>`
8. Dashboard reconnects and shows new PID

**Edge cases:**
- Restart during active bead execution → PREPARE_SHUTDOWN triggers save-progress
- Restart with stuck worker → timeout after 60s, kill worker, exit anyway
- Manager loses connection before ACK → retry restart after timeout
- Socket file lingers → cleanStaleSocket removes it on new daemon start

### 4. Pane Activity Tracking

**Problem:** How does dispatcher know when architect/manager panes last had activity?

**Solution:** SessionStart hook writes to `pane_activity` table:

```sql
CREATE TABLE IF NOT EXISTS pane_activity (
    pane TEXT PRIMARY KEY,  -- "architect" | "manager"
    last_seen INTEGER       -- unix timestamp
);
```

**Hook modification:**
- `.claude/hooks/session_start_extras.py` writes to SQLite on every session start
- Dispatcher reads from pane_activity to populate PaneStatus.LastActivity

**Fallback:** If no pane_activity record, query tmux directly for pane existence.

### 5. Restart Panes (Future)

For completeness, add `restart-pane <architect|manager>` directive:
- Kills tmux pane (tmux kill-pane -t oro:<id>)
- Respawns pane with same nudge
- Triggers fresh SessionStart

**Note:** Not critical for MVP - included for symmetry.

## Premortem Mitigations (Accepted)

| Risk | Mitigation |
|------|-----------|
| Daemon restart fails, no auto-recovery | Human monitors dashboard, retries manually |
| Manager disconnects before ACK | ACK explicitly says "restarting in 5s", manager waits |
| Workers lose state during restart | PREPARE_SHUTDOWN drains or saves progress |
| Pane activity stale (hook doesn't run) | Fallback to tmux list-panes query |
| Dashboard shows wrong status (polling lag) | 2s refresh rate, status includes timestamp |

## Files to Change

| File | Change |
|------|--------|
| `pkg/protocol/directive.go` | Add `DirectiveHealth`, `DirectiveRestartDaemon` |
| `pkg/dispatcher/dispatcher.go` | Add `applyHealth()`, `applyRestartDaemon()` handlers |
| `pkg/dispatcher/health.go` | New file: SwarmHealth, DaemonStatus, PaneStatus types |
| `cmd/oro/cmd_start.go` | Export buildDispatcher or add `oro restart-daemon` CLI |
| `cmd/oro-dash/model.go` | Add Health view state |
| `cmd/oro-dash/health.go` | New file: Health view rendering |
| `cmd/oro-dash/fetch.go` | Add fetchHealth() using health directive |
| `.claude/hooks/session_start_extras.py` | Write to pane_activity table on SessionStart |
| `pkg/protocol/schema.sql` | Add pane_activity table DDL |

## Acceptance Criteria

1. Dashboard shows daemon PID, uptime, state
2. Dashboard shows architect/manager pane alive status and last activity
3. Dashboard shows worker health (existing feature)
4. `oro directive restart-daemon` triggers graceful restart
5. New daemon process starts with updated PID
6. Workers reconnect automatically after restart
7. Dashboard Health tab refreshes every 2s
8. SessionStart hook updates pane_activity table
