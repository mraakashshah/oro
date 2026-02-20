---
name: tmux
description: Use when you need an interactive TTY for REPLs, long-running processes, or orchestrating multiple coding agents in parallel
---

# tmux

## Overview

Remote-control tmux sessions for interactive CLIs by sending keystrokes and scraping pane output. Use tmux only when you need an interactive TTY — prefer background execution for non-interactive tasks.

## Quickstart (isolated socket)

```bash
SOCKET_DIR="${TMPDIR:-/tmp}/oro-tmux-sockets"
mkdir -p "$SOCKET_DIR"
SOCKET="$SOCKET_DIR/oro.sock"
SESSION=oro-python

tmux -S "$SOCKET" new -d -s "$SESSION" -n shell
tmux -S "$SOCKET" send-keys -t "$SESSION":0.0 -- 'PYTHON_BASIC_REPL=1 python3 -q' Enter
tmux -S "$SOCKET" capture-pane -p -J -t "$SESSION":0.0 -S -200
```

After starting a session, always print monitor commands:

```
To monitor:
  tmux -S "$SOCKET" attach -t "$SESSION"
  tmux -S "$SOCKET" capture-pane -p -J -t "$SESSION":0.0 -S -200
```

## Socket Convention

- Default socket path: `${TMPDIR:-/tmp}/oro-tmux-sockets/oro.sock`
- Target format: `session:window.pane` (defaults to `:0.0`)
- Keep names short; avoid spaces

## Sending Input Safely

- Literal sends: `tmux -S "$SOCKET" send-keys -t target -l -- "$cmd"`
- Control keys: `tmux -S "$SOCKET" send-keys -t target C-c`
- **For TUI apps** (Claude Code, Codex): split text and Enter with a delay — fast text+Enter may be treated as paste:

```bash
tmux -S "$SOCKET" send-keys -t target -l -- "$cmd" && sleep 0.1 && tmux -S "$SOCKET" send-keys -t target Enter
```

## Watching Output

- Capture recent history: `tmux -S "$SOCKET" capture-pane -p -J -t target -S -200`
- Wait for patterns: `.claude/skills/tmux/scripts/wait-for-text.sh -t session:0.0 -p 'pattern'`
- Attaching is OK; detach with `Ctrl+b d`

## Spawning Processes

- For Python REPLs, set `PYTHON_BASIC_REPL=1` (non-basic REPL breaks send-keys flows)
- For Go: run `go run .` or test commands directly

## Orchestrating Coding Agents

tmux excels at running multiple coding agents in parallel:

```bash
SOCKET="${TMPDIR:-/tmp}/agent-army.sock"

# Create multiple sessions
for i in 1 2 3 4 5; do
  tmux -S "$SOCKET" new-session -d -s "agent-$i"
done

# Launch agents in different worktrees
tmux -S "$SOCKET" send-keys -t agent-1 "cd .worktrees/feature-a && claude" Enter
tmux -S "$SOCKET" send-keys -t agent-2 "cd .worktrees/feature-b && claude" Enter

# Poll for completion
for sess in agent-1 agent-2; do
  if tmux -S "$SOCKET" capture-pane -p -t "$sess" -S -3 | grep -q '❯'; then
    echo "$sess: DONE"
  else
    echo "$sess: Running..."
  fi
done
```

**Tips:**
- Use separate git worktrees for parallel work (no branch conflicts)
- Check for shell prompt (`❯` or `$`) to detect completion

## Cleanup

- Kill a session: `tmux -S "$SOCKET" kill-session -t "$SESSION"`
- Kill all on socket: `tmux -S "$SOCKET" kill-server`

## Helper: wait-for-text.sh

Polls a pane for a regex with timeout:

```bash
.claude/skills/tmux/scripts/wait-for-text.sh -t session:0.0 -p 'pattern' [-F] [-T 20] [-i 0.5] [-l 2000]
```

| Flag | Purpose | Default |
|------|---------|---------|
| `-t` | Pane target (required) | — |
| `-p` | Regex pattern (required) | — |
| `-F` | Fixed string match | regex |
| `-T` | Timeout seconds | 15 |
| `-i` | Poll interval | 0.5 |
| `-l` | History lines | 1000 |

## Red Flags

- Running interactive sessions when background exec would suffice
- Forgetting to print monitor commands after session creation
- Sending text+Enter in one `send-keys` call to TUI apps
- Leaving orphan sessions (always clean up)
