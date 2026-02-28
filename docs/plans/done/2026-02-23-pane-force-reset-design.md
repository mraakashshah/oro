# Manager Pane Force-Reset with Running Handoff

**Date:** 2026-02-23
**Status:** Draft (post-adversarial review)
**Builds on:** frg2 epic implementation (force restart infra already landed)

## Problem

The manager pane's context lifecycle has two mechanisms:

1. **Advisory hooks** (`inject_context_usage.py`) — inject warning messages hoping the
   agent voluntarily compacts or hands off. Agents often ignore these.
2. **Dispatcher force restart** (`checkManagerPane` in `paneMonitorLoop`) — kills and
   respawns the manager pane when context exceeds threshold. **Already implemented** in
   the frg2 epic.

The force restart mechanism is **fully built but non-functional** because its input is
broken: `context_pct_writer.py` was never wired as a PostToolUse hook. The dispatcher
polls `~/.oro/panes/manager/context_pct` every 5 seconds, but the file is never updated.
The entire restart pipeline is dead.

Additionally, when the force restart does fire, there is no mechanism for the manager to
preserve state across the kill. The manager starts fresh with no memory of what it was
doing.

## What Already Works (frg2 Infrastructure)

Verified in code — these components are implemented and tested:

| Component | Location | Status |
|-----------|----------|--------|
| `TmuxPaneRestarter` | `pkg/dispatcher/pane_restarter.go` | Implemented, tested |
| `checkManagerPane` force restart | `pkg/dispatcher/pane_monitor.go:110-159` | Implemented, tested |
| `managerRestartNeeded` (threshold + inactivity) | `pane_monitor.go:161-178` | Implemented, tested |
| `paneState` (cooldown, restart count) | `pane_monitor.go` | Implemented |
| `PaneRestartCooldown` (default 2min) | `dispatcher.go:186,219-221` | Implemented |
| `PaneInactivityTimeout` (default 10min) | `dispatcher.go:187,222-224` | Implemented |
| Manager-only gating | `pane_monitor.go:63` | Implemented |
| `wireDependencies` passes `execEnvCmd` | `cmd_start.go:453-456` | Implemented |
| `buildPaneDiedHook` uses `execEnvCmd` | `tmux.go:804` | Implemented |
| Double-respawn file guard | `tmux.go:819-821` | Implemented |
| SessionStart reads `~/.oro/panes/<role>/handoff.yaml` | `session_start_extras.py:311-340` | Implemented |

## What's Broken/Missing

### Gap 1: context_pct_writer.py not wired (CRITICAL — blocks everything)

`context_pct_writer.py` exists at `.claude/hooks/context_pct_writer.py` with tests.
It is NOT registered in:
- `.claude/settings.json` (project-local hooks)
- `~/.oro/projects/*/settings.json` (oro init generated hooks)
- `cmd/oro/cmd_init.go` `buildHookConfig()` (template for new projects)

Without this, `context_pct` is never updated, so `checkManagerPane` never fires.

**Files to change:**
- `.claude/settings.json` — add PostToolUse empty-matcher entry
- `cmd/oro/cmd_init.go` — add to `buildHookConfig()` PostToolUse section
- `assets/hooks/` → `.claude/hooks/` sync is already handled by `oro init`

### Gap 2: Double-respawn coordination (BUG)

When `checkManagerPane` calls `paneRestarter.Restart()`, it uses `tmux respawn-pane -k`
which kills the pane process. This triggers the pane-died hook, which ALSO tries to
respawn. The pane-died hook guards against this with:
```bash
test ! -f ~/.oro/panes/<role>/restarting && tmux respawn-pane -k ...
```

But `checkManagerPane` only sets `state.restarting = true` **in memory** — it never
writes the file. The pane-died hook doesn't see it and fires, causing a double respawn.

**Fix:** `checkManagerPane` must write `~/.oro/panes/manager/restarting` before calling
`Restart()` and delete it after. The pane-died hook's file check then works correctly.

**Files to change:**
- `pkg/dispatcher/pane_monitor.go` — write/delete restarting file in `checkManagerPane`

### Gap 3: Restart failure still triggers cooldown (BUG)

`checkManagerPane` unconditionally updates `state.lastRestartAt` and `state.restartCount`
after calling `Restart()`, even when `Restart()` returns an error. A failed restart burns
the 2-minute cooldown, preventing retry.

**Fix:** Only update cooldown state on successful restart.

**Files to change:**
- `pkg/dispatcher/pane_monitor.go` — conditional update in `checkManagerPane`

### Gap 4: Running handoff mechanism (NEW — the main design contribution)

No mechanism exists for the manager to persist state continuously. When force-killed,
the new manager starts fresh with no context about what the previous instance was doing.

**Design:** The manager maintains a running handoff at `~/.oro/panes/manager/handoff.yaml`.
This is a behavioral instruction in the manager's role beacon, not a hook. The manager
writes it using the Write tool as part of its normal workflow.

**Update cadence:**
- After claiming or completing a bead operation
- After making a strategic decision
- After receiving and acting on an escalation
- Minimum: every ~10 tool calls

**Format:** Same handoff YAML already used by create-handoff skill:
```yaml
---
date: 2026-02-23
status: partial
---
goal: Current session objective
now: What to do next (for recovery)
done_this_session:
  - task: Description
    details: What happened
decisions:
  key_name: rationale
beads:
  in_progress: [oro-xxx]
  completed: [oro-yyy]
```

**Atomic safety:** Claude Code's Write tool is atomic (write-tmp-then-rename). Kill
mid-write leaves the previous complete file intact. Worst case: handoff is one update
behind.

**Files to change:**
- Manager role beacon (`.claude/roles/manager/` or session injection) — add running
  handoff instruction
- No Go code changes needed

### Gap 5: SessionStart handoff archiving (NEW)

When a new manager session starts with an existing `handoff.yaml`:

1. **Read** the handoff and inject into context (already implemented)
2. **Archive** to `~/.oro/panes/<role>/handoff-<timestamp>.yaml` (new — prevents clobbering)
3. **Persist learnings** to memory store (new — mirrors worker `persistHandoffContext`)
4. **Clean up** stale signal files: `handoff_requested`, `compacted` (partially done —
   `session_start_extras.py:527` already cleans `handoff_requested` and `handoff_complete`)

**Files to change:**
- `.claude/hooks/session_start_extras.py` — add archive + memory persist steps
- `assets/hooks/session_start_extras.py` — keep in sync

### Gap 6: Hook manager-skip divergence (SYNC)

`inject_context_usage.py` needs early return when `ORO_ROLE == "manager"` since the
dispatcher handles manager lifecycle. The `assets/hooks/` version already has this skip
(line 156-158). The `.claude/hooks/` version does not.

**Files to change:**
- `.claude/hooks/inject_context_usage.py` — add manager skip (match assets/ version)

### Gap 7: Per-role debounce (MINOR)

`inject_context_usage.py` shares `/tmp/oro-context-warn-ts` across all roles. If the
architect triggers a warning, it silences the manager for 60 seconds. Fix:
```python
DEBOUNCE_FILE = f"/tmp/oro-context-warn-{os.getenv('ORO_ROLE', 'default')}-ts"
```

With Gap 6 (manager skip), this mostly doesn't matter. But fixes the issue for any
future multi-pane advisory scenarios.

**Files to change:**
- `.claude/hooks/inject_context_usage.py` — per-role debounce path
- `assets/hooks/inject_context_usage.py` — keep in sync

## Architecture (Corrected)

```
PostToolUse hook                    Dispatcher (ALREADY BUILT)
┌─────────────────┐       ┌──────────────────────────────┐
│context_pct_writer│──────>│ paneMonitorLoop (5s poll)     │
│ writes pane file │       │  checkManagerPane():          │
│ (NOT WIRED YET)  │       │    reads context_pct          │
└─────────────────┘       │    if >= 60% OR inactive 10m: │
                          │      write restarting file     │
                          │      paneRestarter.Restart()   │
                          │      delete restarting file    │
                          └──────────┬───────────────────┘
                                     │ tmux respawn-pane -k
                                     v
                          ┌──────────────────────────────┐
Agent (manager)           │ pane-died hook (ALREADY BUILT)│
┌─────────────────┐       │  checks restarting file guard │
│ Running handoff  │       │  respawns with execEnvCmd     │
│ ~/.oro/panes/    │       └──────────────────────────────┘
│   manager/       │                 │
│   handoff.yaml   │                 v
│ (NEW — agent     │      ┌──────────────────────────────┐
│  maintained)     │      │ SessionStart hook              │
└────────┬────────┘       │  reads handoff.yaml (EXISTS)  │
         └──────────────> │  archives old handoff (NEW)   │
                          │  persists learnings (NEW)     │
                          │  cleans stale signals (EXISTS) │
                          └──────────────────────────────┘
```

## Scope

**In scope:**
- Wire context_pct_writer.py (Gap 1 — unblocks everything)
- Fix double-respawn coordination (Gap 2)
- Fix restart failure cooldown (Gap 3)
- Running handoff mechanism via beacon instruction (Gap 4)
- SessionStart handoff archiving (Gap 5)
- Sync hook manager-skip (Gap 6)
- Per-role debounce (Gap 7)

**Out of scope:**
- Worker changes (ralph handoff already works)
- Architect pane lifecycle (human-controlled, stays advisory)
- Handoff quality validation (future work)

## Testing

Existing tests to verify still pass:
- `go test ./pkg/dispatcher/... -run TestPaneMonitor` — force restart on threshold
- `go test ./pkg/dispatcher/... -run TestTmuxPaneRestarter` — restart mechanics
- `go test ./cmd/oro/... -run TestWireDependencies` — correct execEnvCmd passed

New tests needed:
- `go test ./pkg/dispatcher/... -run TestCheckManagerPane_WritesRestartingFile` — Gap 2
- `go test ./pkg/dispatcher/... -run TestCheckManagerPane_NoUpdateOnFailure` — Gap 3
- `pytest tests/test_context_pct_writer.py` — already exists, just needs hook wiring
- `pytest tests/test_inject_context_usage.py` — add manager skip test
- `pytest tests/test_session_start_extras.py` — add archive + cleanup tests
- Integration: manually verify running handoff → kill → respawn → recovery cycle

## Risks

1. **Running handoff quality degrades.** If the manager doesn't update frequently enough,
   recovery state is stale. Mitigation: even a stale handoff is vastly better than nothing.

2. **Cooldown vs restart loop.** If SessionStart injection immediately pushes context_pct
   above threshold, the pane gets killed again. Mitigation: 2-minute cooldown. The stale
   context_pct file from the dead process will be overwritten once the new process starts
   and context_pct_writer fires.

3. **context_pct_writer failure = silent death.** If the hook fails (permissions, missing
   dir), context_pct is never updated, and the force restart never fires. The hook uses
   `contextlib.suppress(OSError)` which swallows errors silently. Mitigation: the hook
   creates the directory if missing (`mkdir parents=True`). Could add a health check
   to the dispatcher that logs if context_pct hasn't been updated in >60s.
