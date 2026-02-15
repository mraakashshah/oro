# Tmux Hardening Spec

**Date:** 2026-02-14
**Status:** Draft
**Epic:** oro-tmux-hardening (P0)

## Problem Statement

`oro start` and `oro start --detach` fail because `execEnvCmd()` passes invalid CLI flags to Claude Code. The tmux session either never starts or starts in a broken state where the `❯` prompt never appears.

Beyond the startup failure, the tmux UX is missing scrollback, status labels, mouse support, and process lifecycle resilience — all features that the Gastown reference implementation handles well.

## Current Architecture

```
┌─────────────────────────────────────┐
│         TMux Session "oro"          │
├──────────────────┬──────────────────┤
│ Window 0:        │ Window 1:        │
│ "architect"      │ "manager"        │
│                  │                  │
│ exec env         │ exec env         │
│   ORO_ROLE=arch  │   ORO_ROLE=mgr   │
│   claude         │   claude         │
│   --session-id   │   --session-id   │
│     oro-architect│     oro-manager  │  ← INVALID: not UUID
│   --ide          │   --ide          │  ← WRONG: no IDE in tmux
└──────────────────┴──────────────────┘
  Status bar: color only (green/orange)
  No scrollback | No mouse | No labels
```

**Startup sequence:** `oro start` → spawn daemon → wait for socket → send start directive → `TmuxSession.Create()` → `WaitForPrompt()` (polls for `❯`) → `SendKeysVerified()` (inject nudge) → `VerifyBeaconReceived()` → `RegisterPaneDiedHooks()`.

## Root Causes

### 1. `--session-id` requires a valid UUID (CRITICAL)

From [Claude Code CLI reference](https://code.claude.com/docs/en/cli-usage):
> `--session-id`: Use a specific session ID (**must be a valid UUID**)
> Example: `claude --session-id "550e8400-e29b-41d4-a716-446655440000"`

Current code passes `oro-architect` and `oro-manager` — not valid UUIDs. Claude Code either rejects the flag or enters an undefined state where the Ink TUI never renders, causing `WaitForPrompt()` to timeout after 60s.

**Location:** `cmd/oro/tmux.go:87` (`execEnvCmd`)

### 2. `--ide` flag is inappropriate

From CLI reference:
> `--ide`: Automatically connect to IDE on startup if exactly one valid IDE is available

In a tmux pane, there's no IDE. At best this is a no-op; at worst it causes Claude to hang looking for an IDE connection.

**Location:** `cmd/oro/tmux.go:87` (`execEnvCmd`)

### 3. No startup failure cleanup

If `WaitForPrompt()` fails after 60s, the error propagates but the half-created tmux session isn't cleaned up. The architect window may exist but the manager window was never created, leaving a zombie session.

**Location:** `cmd/oro/tmux.go:96-160` (`Create`)

## Design

### Phase 1: Fix Startup (P0 — System Broken)

#### 1a. Fix `execEnvCmd()` — drop invalid flags

```go
func execEnvCmd(role string) string {
    return fmt.Sprintf(
        "exec env ORO_ROLE=%s BD_ACTOR=%s GIT_AUTHOR_NAME=%s claude",
        role, role, role,
    )
}
```

**Changes:**
- Drop `--session-id oro-<role>` — invalid UUID format, and fresh sessions are fine (SessionStart hook provides all context)
- Drop `--ide` — no IDE in tmux

**Decision: Drop `--session-id` entirely vs deterministic UUIDs**

| Option | Pro | Con |
|--------|-----|-----|
| Drop entirely (recommended) | Simple, no UUID dependency | Loses conversation history across restarts |
| Hardcode deterministic UUIDs | Preserves history | Stale context from prior sessions may confuse agents; adds complexity |

**Premortem:** Dropping `--session-id` means each `oro start` creates a fresh Claude session. This is actually *better* — the SessionStart hook injects full role context, and stale conversation history from a crashed session could actively mislead the agent.

#### 1b. Add startup failure cleanup

```go
func (s *TmuxSession) Create(architectNudge, managerNudge string) error {
    if s.Exists() {
        if s.isHealthy() { return nil }
        _ = s.Kill()
    }

    if _, err := s.Runner.Run("tmux", "new-session", ...); err != nil {
        return fmt.Errorf("tmux new-session: %w", err)
    }

    if _, err := s.Runner.Run("tmux", "new-window", ...); err != nil {
        _ = s.Kill()  // ← NEW: cleanup on partial creation failure
        return fmt.Errorf("tmux new-window: %w", err)
    }

    // ... rest of setup with cleanup on failure ...
}
```

#### 1c. Verify end-to-end with integration test

Write a test that exercises the full `Create()` flow with the fixed `execEnvCmd()` and verifies Claude starts. Use the existing `fakeCmd` test infrastructure.

### Phase 2: UX Hardening (P1)

#### 2a. Enable scrollback

```go
// In Create(), after status bar setup:
s.Runner.Run("tmux", "set-option", "-t", s.Name, "alternate-screen", "off")
s.Runner.Run("tmux", "set-option", "-t", s.Name, "history-limit", "50000")
```

**`alternate-screen off`** causes Claude Code's Ink TUI output to flow into tmux's scrollback buffer instead of using the alternate screen. Mouse scroll and `C-b [` (copy-mode) will then show actual Claude output.

**Premortem:** Display artifacts when Claude exits/restarts — acceptable since Claude IS the pane process and rarely exits cleanly.

#### 2b. Status bar labels

```go
// status-left: show role name
statusLeft := `#[bold] #{window_name} `
s.Runner.Run("tmux", "set-option", "-t", s.Name, "status-left", statusLeft)
s.Runner.Run("tmux", "set-option", "-t", s.Name, "status-left-length", "20")

// status-right: session name + time
statusRight := `#[default] oro | %H:%M`
s.Runner.Run("tmux", "set-option", "-t", s.Name, "status-right", statusRight)
```

The `after-select-window` hook already changes background color, so the label auto-updates.

#### 2c. Enable mouse mode

```go
// Following Gastown pattern (EnableMouseMode):
s.Runner.Run("tmux", "set-option", "-t", s.Name, "mouse", "on")
s.Runner.Run("tmux", "set-option", "-t", s.Name, "set-clipboard", "on")
```

### Phase 3: Resilience (P2)

#### 3a. remain-on-exit + respawn-pane for crash recovery

Instead of killing and recreating the entire session on crash, use Gastown's `remain-on-exit` + `respawn-pane` pattern:

```go
// During Create():
s.Runner.Run("tmux", "set-option", "-t", s.Name, "remain-on-exit", "on")

// On crash recovery (pane-died hook or health check):
s.Runner.Run("tmux", "respawn-pane", "-k", "-t", pane, execEnvCmd(role))
```

This preserves the session, scrollback, and status bar configuration.

#### 3b. Nudge serialization

Add per-pane mutex to prevent interleaved send-keys when multiple goroutines nudge concurrently (from Gastown's `sessionNudgeLocks` pattern):

```go
var sessionNudgeLocks sync.Map // map[string]*sync.Mutex

func getSessionNudgeLock(target string) *sync.Mutex {
    actual, _ := sessionNudgeLocks.LoadOrStore(target, &sync.Mutex{})
    return actual.(*sync.Mutex)
}
```

#### 3c. Process tree cleanup on Kill

Adopt Gastown's `KillSessionWithProcesses()` — walk process group and descendants before killing tmux session to prevent orphaned Claude/node processes.

## Premortem

```yaml
premortem:
  mode: deep
  context: "Tmux hardening epic — fixing startup, UX, and resilience"

  tigers:
    - risk: "alternate-screen=off may break Claude Code's Ink TUI rendering"
      severity: medium
      mitigation_checked: >
        Gastown does NOT use alternate-screen=off. Ink TUI expects alternate
        screen for cursor positioning, colors, etc. Need to test manually
        before committing. If broken, fall back to Option B from oro-cm5q:
        mouse scroll enters copy-mode with if-shell #{alternate_on}.
      location: "Phase 2a"

    - risk: "Removing --session-id breaks conversation continuity for user"
      severity: medium
      mitigation_checked: >
        Currently sessions crash and restart frequently, so continuity
        is already broken. SessionStart hook provides full context.
        The user (architect) rarely needs to review prior conversation
        history. If needed later, can add deterministic UUIDs (UUID v5
        with oro namespace).
      location: "Phase 1a"

  elephants:
    - risk: >
        The fundamental tmux approach — launching interactive Claude Code
        in a tmux pane and sending keystrokes via send-keys — is inherently
        fragile. Gastown has 1679 lines of tmux code and still has bugs.
        Every Claude Code update could break prompt detection, Ink rendering,
        or keystroke handling. The real question is whether claude -p
        (non-interactive SDK mode) could replace interactive sessions for
        the manager role, leaving tmux only for the architect (human-facing).

  paper_tigers:
    - risk: "Mouse mode conflicts with user's ~/.tmux.conf settings"
      reason: >
        tmux set-option -t scopes to the session. User's global mouse
        setting is not affected. Session-scoped settings override globals.

    - risk: "remain-on-exit leaves dead panes visible to user"
      reason: >
        respawn-pane -k immediately replaces the dead pane content.
        The user only sees a brief flash (if attached). Gastown uses
        this pattern in production.

    - risk: "Dropping --ide breaks something for attached users"
      reason: >
        --ide only connects to IDE if one is available. In tmux, there's
        never an IDE available, so the flag was always a no-op at best.
```

## Existing Beads to Subsume

| Bead | Title | Disposition |
|------|-------|-------------|
| oro-7tso | Bug: oro start --detach fails with prompt timeout | → Phase 1 (fix execEnvCmd) |
| oro-8ah2 | Add role labels and status info to tmux status bar | → Phase 2b (status labels) |
| oro-cm5q | Fix tmux scroll: enable scrollback past Claude Code TUI | → Phase 2a (scrollback) |

## Files to Modify

- `cmd/oro/tmux.go` — execEnvCmd, Create, scrollback, status, mouse, remain-on-exit
- `cmd/oro/tmux_test.go` — update tests for all changes

## References

- **Gastown tmux:** `yap/reference/gastown/internal/tmux/tmux.go` (1679 lines)
- **Claude Code CLI:** https://code.claude.com/docs/en/cli-usage
- **Closed related:** oro-r50m (tmux server dies), oro-eoqk (cleanup CLI), oro-0g8y (SIGINT bypass)
