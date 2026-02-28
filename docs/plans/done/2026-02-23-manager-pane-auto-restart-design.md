# Manager Pane Auto-Restart

**Date:** 2026-02-23
**Status:** Draft
**Epic:** TBD

## Problem

The architect and manager panes rely on advisory hooks (`inject_context_usage.py`, `pane_handoff_reminder.py`) to warn agents when context is exhausted. The agents ignore these warnings. Unlike workers — which the dispatcher kills and respawns via `handleHandoff` → `respawnWorker` — panes have no automated lifecycle management for context exhaustion or staleness.

The result: panes accumulate stale context, degrade in quality, and eventually hit Claude Code's auto-compaction, which produces worse results than a clean restart.

## Scope

**In scope:** Manager pane only. Two triggers: context threshold and inactivity.

**Out of scope:** Architect pane (human-controlled — user manages their own session via `/clear`).

## Design

### Triggers

| Trigger | Condition | Response |
|---------|-----------|----------|
| Context threshold | `context_pct >= PaneContextThreshold` | Create handoff → restart |
| Inactivity | `context_pct` file mtime > 15 minutes AND process alive | Direct restart (no handoff) |

### Restart Flow

```
paneMonitorLoop (every 5s, manager only)
  │
  ├─ context_pct >= threshold?
  │    ├─ YES: spawn claude -p handoff agent (best-effort, 30s timeout)
  │    │         ↓
  │    │       write ~/.oro/panes/manager/restarting flag
  │    │         ↓
  │    │       tmux respawn-pane -k -t oro:manager <execEnvCmd>
  │    │         ↓
  │    │       clear restarting flag, context_pct, handoff_requested
  │    │       set cooldown timer (5 min)
  │    │         ↓
  │    │       log pane_restarted event
  │    └─ NO: continue
  │
  ├─ context_pct mtime > 15 min AND process alive AND no cooldown?
  │    ├─ YES: write restarting flag → respawn-pane → clear flag → cooldown
  │    └─ NO: continue
  │
  └─ cooldown active? skip all checks
```

### Components

#### 1. PaneRestarter Interface (pkg/dispatcher/)

```go
type PaneRestarter interface {
    Restart(role string) error
}
```

Production implementation: `TmuxPaneRestarter` wraps `tmux respawn-pane -k -t <session>:<role> <execEnvCmd>`. Uses a `CommandRunner` (same interface as `TmuxEscalator`).

Constructed in `wireDependencies()` with session name ("oro"), project name, and runner.

#### 2. Extended paneMonitorLoop (pkg/dispatcher/pane_monitor.go)

New per-pane state tracked in dispatcher:

```go
type paneState struct {
    lastRestartAt time.Time     // cooldown tracking
    restartCount  int           // total restarts this session
}
```

The loop runs for `"manager"` only (hardcoded; architect excluded). On each tick:

1. **Cooldown check**: if `time.Since(lastRestartAt) < PaneRestartCooldown`, skip.
2. **Context check**: read `context_pct` file. If `>= threshold`, trigger restart with handoff.
3. **Inactivity check**: stat `context_pct` file mtime. If stale > `PaneInactivityTimeout` AND pane process alive (via `tmux display-message -p -t oro:manager "#{pane_pid}"` + `kill -0`), trigger restart without handoff.

#### 3. Handoff Agent (best-effort)

Before restart on context threshold (not inactivity), spawn:

```bash
claude -p "Read the manager's current state. Run: bd list --status=in_progress --assignee=manager. Run: git status. Run: git log --oneline -5. Write a handoff YAML to ~/.oro/panes/manager/handoff.yaml with fields: date, status: auto, goal, now, done_this_session."
```

- 30-second timeout. If it fails or times out, proceed with restart anyway.
- Uses haiku model to minimize cost.
- Before spawning: reset any manager-owned `in_progress` beads to `open` via `bd update <id> --status=open`.

#### 4. Double-Respawn Guard

Before calling `respawn-pane -k`:
1. Write `~/.oro/panes/manager/restarting` flag file.
2. The existing `pane-died` hook (`buildPaneDiedHook`) must check for this flag. If present, skip auto-respawn (the dispatcher is handling it).
3. After successful respawn, delete the flag file.

#### 5. After Restart

The new Claude process starts → SessionStart hooks fire automatically:
- `session_start_extras.py` detects `ORO_ROLE=manager`, loads role beacon, handoff.yaml, bd ready, git state.
- Manager is reprimed with fresh context and latest project state.

### Configuration

New fields in `dispatcher.Config`:

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `PaneRestartCooldown` | `time.Duration` | `5 * time.Minute` | Min time between restarts |
| `PaneInactivityTimeout` | `time.Duration` | `15 * time.Minute` | Idle time before restart |
| `PaneHandoffTimeout` | `time.Duration` | `30 * time.Second` | claude -p timeout |

Existing field `PaneContextThreshold` (default 60) is reused.

### Hook Cleanup

| Hook | Change |
|------|--------|
| `pane_handoff_reminder.py` | Add early return when `ORO_ROLE == "manager"` (dispatcher handles lifecycle) |
| `inject_context_usage.py` | Add early return when `ORO_ROLE == "manager"` (dispatcher handles lifecycle) |
| `context_pct_writer.py` | No change (dispatcher still reads the file) |
| `buildPaneDiedHook` | Check `restarting` flag; skip auto-respawn if present |

### Mitigations (from premortem)

| Risk | Severity | Mitigation |
|------|----------|------------|
| Restart loop | HIGH | 5-minute cooldown after each restart; skip monitoring during cooldown |
| Double respawn | MEDIUM | `restarting` flag file checked by pane-died hook |
| Inactivity false positive | MEDIUM | Check process alive before treating mtime as stale |
| claude -p cost | ELEPHANT | Best-effort with 30s timeout; use haiku; skip on failure |
| Orphaned beads | ELEPHANT | Reset manager in_progress beads to open before restart |

## Files Affected

| File | Change |
|------|--------|
| `pkg/dispatcher/pane_monitor.go` | Extend with restart logic, state tracking, inactivity detection |
| `pkg/dispatcher/pane_monitor_test.go` | New tests for restart flow, cooldown, inactivity, double-respawn guard |
| `pkg/dispatcher/dispatcher.go` | Add `PaneRestarter` field, config fields, `paneState` map |
| `pkg/dispatcher/pane_restarter.go` | New file: `TmuxPaneRestarter` implementation |
| `pkg/dispatcher/pane_restarter_test.go` | New file: tests for TmuxPaneRestarter |
| `cmd/oro/cmd_start.go` | Wire `PaneRestarter` in `wireDependencies()` |
| `cmd/oro/tmux.go` | Update `buildPaneDiedHook` to check `restarting` flag |
| `assets/hooks/pane_handoff_reminder.py` | Skip for manager role |
| `assets/hooks/inject_context_usage.py` | Skip for manager role |

## Non-Goals

- Architect pane auto-restart (human-controlled)
- Worker context management changes (already works via handleHandoff)
- Changes to context_pct calculation or threshold logic
- Handoff quality improvements beyond basic beads+git state capture
