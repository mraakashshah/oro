# Disable Dispatcher Pane Restart

**Date:** 2026-02-16
**Status:** Implementing
**Fixes:** Recurring oro/tmux crash (dispatcher + tmux dying after 3-10 minutes)

## Problem

Two independent pane restart mechanisms create a feedback loop:

1. **Tmux pane-died hooks** (`cmd/oro/tmux.go:733-785`) — instant, tmux-native
2. **Dispatcher pane restart** (`pkg/dispatcher/pane_monitor.go:112-221`) — 5s poll, added Feb 15 (fc581b9)

Race sequence: pane dies → pane-died hook respawns instantly → dispatcher poll finds `handoff_complete` → kills just-respawned pane → pane-died fires again → cascade.

## Decision

**Disable dispatcher pane restart entirely (Approach A).**

Rationale:
- Pane-died hooks already cover all restart scenarios (any process exit triggers them via `remain-on-exit=on`)
- Dispatcher restart is redundant — added Feb 15, crashes started Feb 16
- Gastown reference uses only pane-died hooks, no dispatcher restart
- Removing newest code preserves battle-tested crash recovery
- Also serves as diagnostic: if crashes persist, root cause is elsewhere

## Changes

### Remove dispatcher pane restart

1. `pkg/dispatcher/pane_monitor.go` — Remove `checkHandoffComplete()` call from `checkPaneContexts()`, remove dead functions (`checkHandoffComplete`, `restartPane`, `buildExecEnvCmd`)
2. `pkg/dispatcher/dispatcher.go` — Remove `TmuxSession` interface and `tmuxSession` field
3. `pkg/dispatcher/process_manager.go` — Remove `SetTmuxSession` method
4. `pkg/dispatcher/pane_restart_test.go` — Delete (tests removed feature)
5. `cmd/oro/cmd_start.go` — Remove `SetTmuxSession` wiring

### Extend signal file cleanup

1. `assets/hooks/session_start_extras.py` — Also delete stale `handoff_complete` on SessionStart
2. `tests/test_session_start_extras.py` — Add test for `handoff_complete` cleanup

### Keep unchanged

- `cmd/oro/tmux.go` — `KillPane` and `RespawnPane` methods stay (independently tested utilities)
- Pane-died hooks — untouched, remain the sole restart mechanism
- Context monitoring and handoff signaling — untouched, dispatcher still writes `handoff_requested`

## Risk

| Risk | Mitigation |
|------|-----------|
| Stale `handoff_complete` files accumulate | SessionStart cleanup extended to delete them |
| Handoff restart no longer automated by dispatcher | Pane-died hook handles it (process exits → hook fires → respawn) |
