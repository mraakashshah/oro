# Early Compact Trigger for Architect/Manager Panes

**Date:** 2026-02-28
**Status:** Validated, adversarially reviewed, ready for implementation

## Problem

Claude Code's built-in auto-compaction fires at ~59% context for architect/manager panes
(Sonnet hard stop = 60%). This is too late — the model is already degraded and the
compaction interrupts work at the worst moment.

Workers avoid this via the Go `handleContextThreshold` watcher (handoff at 60%), but
architect/manager are long-running interactive sessions that need a different solution.

## Goal

Trigger `/compact` proactively at the **Sonnet soft threshold (50%)** via tmux send-keys,
before CC's natural compaction fires. After compaction, recover context from the live
dispatcher state rather than the stale transcript.

## Non-Goals

- Changing pre_compact.py (it works fine as-is)
- Changing worker handoff threshold or Go hard-stop
- Supporting manual compaction instructions — model doesn't run /compact itself

## Design

### Component 1: `compact_trigger.py` (PostToolUse hook — already exists, verify/update)

Runs after `context_pct_writer.py` in the PostToolUse chain. Reads the already-written
pct file, checks threshold, injects `/compact` via tmux if threshold crossed.

**Trigger conditions (all must be true):**
- `ORO_ROLE` env var is set (architect or manager — not a worker session)
- `ORO_WORKER != 1`
- `TMUX_PANE` env var present (running inside tmux)
- `~/.oro/panes/<role>/context_pct` reads >= threshold
- `~/.oro/panes/<role>/compact_debounce` does NOT exist (debounce)

**On trigger:**
1. `tmux send-keys -t $TMUX_PANE "/compact" Enter`
2. On success (exit 0): write `~/.oro/panes/<role>/compact_debounce` (debounce flag)

**Debounce file path:** `~/.oro/panes/<role>/compact_debounce`
(subdirectory per role — same layout as `context_pct`)

**Threshold source:** `thresholds.json` keyed by `model_key` from hook input.
Fallback: 50 (Sonnet default).

**pct source:** Read `~/.oro/panes/<role>/context_pct` (written by preceding
`context_pct_writer.py` in the same PostToolUse event). If file absent or stale: skip.

### Component 2: Updated `session_start_compact.py` (SessionStart, matcher: compact)

After CC compacts and restarts the session:

**Non-worker path** (architect/manager — role set and not starting with "worker"):
1. **Clear debounce:** delete `~/.oro/panes/<role>/compact_debounce` (suppress OSError)
2. **Inject live state:**
   - Run `oro status` → worker/bead assignments
   - Run `bd list --status=in_progress` → active beads
   - Concatenate both outputs, inject as `additionalContext`
3. Return — do NOT fall through to transcript path

**Worker path** (role starts with "worker"):
- Clear debounce (safety — workers shouldn't have one, but defensive)
- Fall through to existing transcript-state injection + continuation bead creation

**No-role path** (role absent):
- Existing transcript-state injection unchanged

**How `role` is sourced:** Read from hook `input_data.get("role", "")` (passed by CC in
SessionStart JSON), NOT from `ORO_ROLE` env var. `_clear_debounce` must use:
```python
debounce_path = Path(PANES_DIR) / role / "compact_debounce"
```
This matches the path written by `compact_trigger.py`.

### Component 3: `cmd_init.go` wiring

Add `compact_trigger.py` to PostToolUse hooks **after** `context_pct_writer.py`:

```go
"PostToolUse": {
    {Matcher: "", Hooks: []hookEntry{
        {Type: "command", Command: py("context_pct_writer.py")},
        {Type: "command", Command: py("compact_trigger.py")},  // ← new
        ...
    }},
```

## Data Flow

```
PostToolUse fires
  → context_pct_writer.py  writes ~/.oro/panes/<role>/context_pct
  → compact_trigger.py     reads file, checks threshold + debounce
      if triggered:
          tmux send-keys -t $TMUX_PANE "/compact" Enter
          on success: write ~/.oro/panes/<role>/compact_debounce

CC PreCompact fires
  → pre_compact.py  saves transcript state to ~/.oro/compaction-state/<session_id>.json

CC compacts conversation

CC SessionStart (source=compact) fires
  → session_start_compact.py:
      role = input_data["role"]
      if non-worker:
          delete ~/.oro/panes/<role>/compact_debounce   (clear debounce)
          oro status + bd list --status=in_progress → additionalContext
          return
      if worker:
          delete debounce (safety)
          existing transcript-state injection (unchanged)
```

## Key Decisions

### Debounce via file (not in-memory)
Hooks are subprocesses — no shared memory. File is the only option. Role dir created
with `mkdir -p`; delete wrapped in `suppress(OSError)`.

Debounce file: `~/.oro/panes/<role>/compact_debounce` (consistent with context_pct
layout, one subdirectory per role).

Premortem: two hooks racing on same pane → file write is atomic at this timescale
(hooks run sequentially per PostToolUse event). Acceptable.

### Clear debounce BEFORE injecting live state (non-worker)
The debounce clear must happen first. If live state injection fails, the debounce is
already cleared, allowing re-trigger on the next tool call.

### Read pct from file, not re-parse transcript
`context_pct_writer.py` runs first in the same event. File read is ~1µs vs re-parsing
a growing JSONL. Risk: if writer fails silently, file is stale → trigger misses one
cycle. Acceptable — CC will compact eventually at 59% anyway.

### `TMUX_PANE` from environment, not a pane map file
Hooks inherit the environment of their parent process (Claude Code), which runs in a
tmux pane and therefore has `TMUX_PANE` set. No session_id → pane mapping needed.
If absent (session started outside tmux): skip silently.

### Live state recovery, not transcript state
For architect/manager, the source of truth is the dispatcher DB and beads DB — not
the conversation. `oro status` + `bd list --status=in_progress` gives full swarm
context in two commands. Transcript state (bead_id, files_modified) is only meaningful
for workers doing code changes.

### `role` source in session_start_compact
`compact_trigger.py` guards on `ORO_ROLE` env var (set by the user's shell/launcher).
`session_start_compact.py` reads `role` from the CC-provided hook input JSON. Both
are correct for their context — `compact_trigger` fires during normal tool use,
`session_start_compact` fires after CC restarts the session.

## Edge Cases

| Scenario | Behavior |
|----------|----------|
| `TMUX_PANE` not set | Skip silently |
| `thresholds.json` missing | Fallback to 50 |
| `oro status` fails post-compact | Suppress, continue with `bd list` output only |
| `bd list` fails post-compact | Suppress, continue with `oro status` output only |
| Both fail | Inject empty string, don't crash |
| pct file absent/stale | Skip trigger |
| Rapid tool calls at 51%, 53%, 55% | First triggers, rest are no-ops (debounce) |
| Compact while worker merging | No interaction — client-side only |
| Debounce clear fails (OSError) | Suppress — CC will compact again at 59% |
| `ORO_ROLE` not set in architect pane | compact_trigger no-ops silently — ensure launcher sets it |

## Tests

**`compact_trigger.py`:**
- pct < threshold → no tmux call, no debounce file
- pct >= threshold, no debounce file → tmux called, debounce file written at `<role>/compact_debounce`
- pct >= threshold, debounce file exists → no-op (tmux not called)
- `TMUX_PANE` absent → no-op
- `ORO_WORKER=1` → no-op
- `ORO_ROLE` absent → no-op
- `thresholds.json` missing → uses 50 fallback
- tmux send-keys fails (exit 1) → debounce file NOT written (retry next cycle)

**`session_start_compact.py`:**
- non-worker role: debounce file `<role>/compact_debounce` deleted before live state injected
- non-worker role: `additionalContext` contains output of BOTH `oro status` AND `bd list --status=in_progress`
- non-worker role: `oro status` failure → suppress, still inject `bd list` output
- non-worker role: `bd list` failure → suppress, still inject `oro status` output
- worker role: debounce cleared, falls through to transcript-state path (existing behavior unchanged)
- `ORO_WORKER=1` → existing transcript-state path unchanged

**`cmd/oro/cmd_init_test.go`:**
- generated settings.json PostToolUse hook chain contains `compact_trigger.py` immediately after `context_pct_writer.py`

## Files Changed

| File | Change |
|------|--------|
| `assets/hooks/compact_trigger.py` | Update: verify debounce path is `<role>/compact_debounce` |
| `.claude/hooks/compact_trigger.py` | Sync from assets |
| `assets/hooks/session_start_compact.py` | Update: fix debounce path, add debounce clear for non-workers, add `bd list` call |
| `.claude/hooks/session_start_compact.py` | Sync from assets |
| `cmd/oro/cmd_init.go` | Wire `compact_trigger.py` in PostToolUse after `context_pct_writer.py` |
| `tests/test_compact_trigger.py` | New: unit tests for all guards + debounce path |
| `tests/test_session_start_compact.py` | Update: debounce clear path, bd list injection, error suppression |
| `cmd/oro/cmd_init_test.go` | Update: verify compact_trigger wired after context_pct_writer |
