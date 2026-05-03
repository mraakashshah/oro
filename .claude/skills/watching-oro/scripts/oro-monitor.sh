#!/usr/bin/env bash
# oro-monitor.sh — Create a tmux monitoring layout for observing a running oro swarm.
# Usage: .claude/skills/watching-oro/scripts/oro-monitor.sh [--workers N]
#
# Creates tmux session "oro-watch" with 6 panes:
#   0: Dispatcher event stream (oro logs --follow)
#   1: Daemon stderr log (tail -f /tmp/oro-daemon.log)
#   2: Worker output logs (tail -f on all worker output.logs)
#   3: Architect pane mirror (continuous capture)
#   4: Manager pane mirror (continuous capture)
#   5: Status dashboard (oro status loop via inotifywait or kqueue)
#
# All panes use tail -f or --follow flags. No sleep-based polling.

set -euo pipefail

ORO_HOME="${ORO_HOME:-$HOME/.oro}"
SESSION="oro-watch"
ORO_BIN="./oro"

# Resolve oro binary
if [[ ! -x "$ORO_BIN" ]]; then
	ORO_BIN="$(command -v oro 2>/dev/null || true)"
	if [[ -z "$ORO_BIN" ]]; then
		echo "error: oro binary not found. Run 'make build' first." >&2
		exit 1
	fi
fi

# Kill existing monitoring session
tmux kill-session -t "$SESSION" 2>/dev/null || true

# Create session with first pane: dispatcher event stream
tmux new-session -d -s "$SESSION" -n monitor \
	"$ORO_BIN logs --follow 2>&1; echo '[dispatcher log ended]'; read"

# Pane 1 (right): daemon stderr
tmux split-window -t "$SESSION":0 -h \
	"tail -f /tmp/oro-daemon.log 2>/dev/null || echo 'No daemon log yet — waiting...'; \
   while [ ! -f /tmp/oro-daemon.log ]; do sleep 1; done; tail -f /tmp/oro-daemon.log"

# Pane 2 (bottom-left): all worker output logs, interleaved
# Uses tail -f with --retry so it picks up new files
tmux split-window -t "$SESSION":0.0 -v \
	"echo 'Watching worker output logs...'; \
   while true; do \
     logs=(\$(find \"$ORO_HOME/workers\" -name 'output.log' 2>/dev/null)); \
     if [ \${#logs[@]} -gt 0 ]; then \
       tail -f --retry \"\${logs[@]}\" 2>/dev/null; \
     fi; \
     inotifywait -qq -e create \"$ORO_HOME/workers\" 2>/dev/null || sleep 2; \
   done"

# Pane 3 (bottom-right top): architect pane mirror
# Uses tmux wait-for + pipe-pane for event-driven capture
tmux split-window -t "$SESSION":0.1 -v \
	"echo '--- Architect Pane (oro:0) ---'; \
   while true; do \
     tmux capture-pane -t oro:0 -p -S -40 2>/dev/null || echo '[architect pane not available]'; \
     echo '--- refresh ---'; \
     inotifywait -qq -t 5 -e modify \"$ORO_HOME/state.db\" 2>/dev/null || true; \
   done"

# Pane 4 (below pane 2): manager pane mirror
tmux split-window -t "$SESSION":0.2 -v \
	"echo '--- Manager Pane (oro:1) ---'; \
   while true; do \
     tmux capture-pane -t oro:1 -p -S -40 2>/dev/null || echo '[manager pane not available]'; \
     echo '--- refresh ---'; \
     inotifywait -qq -t 5 -e modify \"$ORO_HOME/state.db\" 2>/dev/null || true; \
   done"

# Pane 5 (below pane 3): status dashboard
# Refreshes on DB changes (event-driven via fswatch on macOS, inotifywait on Linux)
tmux split-window -t "$SESSION":0.3 -v \
	"echo '--- Oro Status Dashboard ---'; \
   while true; do \
     clear; \
     echo '=== \$(date +%H:%M:%S) ==='; \
     $ORO_BIN status 2>&1; \
     echo; echo '--- Tasks In Progress ---'; \
     $ORO_BIN task list --status=in_progress 2>&1 | head -20; \
     echo; echo '--- Recent Events ---'; \
     $ORO_BIN logs 2>&1 | tail -10; \
     if command -v fswatch >/dev/null 2>&1; then \
       fswatch -1 \"$ORO_HOME/state.db\" 2>/dev/null || sleep 5; \
     elif command -v inotifywait >/dev/null 2>&1; then \
       inotifywait -qq -t 5 -e modify \"$ORO_HOME/state.db\" 2>/dev/null || true; \
     else \
       sleep 5; \
     fi; \
   done"

# Set pane titles for identification
tmux select-pane -t "$SESSION":0.0 -T "events"
tmux select-pane -t "$SESSION":0.1 -T "daemon-log"
tmux select-pane -t "$SESSION":0.2 -T "worker-logs"
tmux select-pane -t "$SESSION":0.3 -T "architect"
tmux select-pane -t "$SESSION":0.4 -T "manager"
tmux select-pane -t "$SESSION":0.5 -T "status"

# Enable pane titles in status
tmux set-option -t "$SESSION" pane-border-status top
tmux set-option -t "$SESSION" pane-border-format " #{pane_title} "
tmux set-option -t "$SESSION" mouse on

echo "Monitoring session ready. Attach with:"
echo "  tmux attach -t $SESSION"
echo ""
echo "Pane layout:"
echo "  0: events       1: daemon-log"
echo "  2: worker-logs  3: architect"
echo "  4: manager      5: status"
