#!/usr/bin/env bash
# watch-loop.sh — Launch oro, observe, rebuild every 30 minutes.
# Run inside a tmux session. Ctrl+C to stop.
set -euo pipefail

REPO_ROOT="$(git rev-parse --show-toplevel)"
cd "$REPO_ROOT"

CYCLE=0
INTERVAL=1800 # 30 minutes

cleanup_and_stop() {
	echo ""
	echo "=== [watch-loop] Stopping... ==="
	ORO_HUMAN_CONFIRMED=1 ./oro stop --force 2>/dev/null || true
	pkill -f "oro work" 2>/dev/null || true

	# Remove worktrees and their tracking branches
	for wt in .worktrees/oro-*/; do
		if [ -d "$wt" ]; then
			bead_id="$(basename "$wt")"
			git worktree remove --force "$wt" 2>/dev/null || true
			git branch -D "agent/${bead_id}" 2>/dev/null || true
		fi
	done
}

trap 'cleanup_and_stop; exit 0' INT TERM

launch_cycle() {
	CYCLE=$((CYCLE + 1))
	echo ""
	echo "╔════════════════════════════════════════════╗"
	echo "║  CYCLE $CYCLE — $(date '+%Y-%m-%d %H:%M:%S')           ║"
	echo "╚════════════════════════════════════════════╝"

	# Stop if already running
	if ./oro status 2>/dev/null | grep -q "running"; then
		echo "[watch-loop] Stopping previous cycle..."
		ORO_HUMAN_CONFIRMED=1 ./oro stop --force 2>/dev/null || true
		pkill -f "oro work" 2>/dev/null || true
		sleep 2
	fi

	# Remove old worktrees and their tracking branches
	echo "[watch-loop] Cleaning worktrees..."
	for wt in .worktrees/oro-*/; do
		if [ -d "$wt" ]; then
			bead_id="$(basename "$wt")"
			if git worktree remove --force "$wt" 2>/dev/null; then echo "  removed $wt"; fi
			if git branch -D "agent/${bead_id}" 2>/dev/null; then echo "  deleted branch agent/${bead_id}"; fi
		fi
	done

	# Rebuild
	echo "[watch-loop] Building..."
	make build 2>&1 | tail -5

	# Launch — --detach may fail to send start directive due to socket race;
	# we detect "inert" state and send it manually.
	echo "[watch-loop] Launching oro with 3 workers..."
	./oro start --workers 3 --detach 2>&1 || true
	sleep 3

	# Ensure dispatcher transitions from inert to running
	STATUS=$(./oro status 2>/dev/null || echo "not running")
	if echo "$STATUS" | grep -q "inert"; then
		echo "[watch-loop] Dispatcher inert — sending start directive..."
		./oro directive start 2>/dev/null || true
		sleep 2
	fi
	./oro status

	echo "[watch-loop] Running for ${INTERVAL}s (until $(date -v +${INTERVAL}S '+%H:%M:%S'))..."
	echo "[watch-loop] Monitor: ./oro logs --tail 50 | grep -v heartbeat"
}

# Initial launch
launch_cycle

# 30-minute rebuild loop
while true; do
	# Sleep in small chunks to allow clean interruption
	ELAPSED=0
	while [ $ELAPSED -lt $INTERVAL ]; do
		sleep 30
		ELAPSED=$((ELAPSED + 30))
		# Lightweight status check (no polling spam)
		if [ $((ELAPSED % 300)) -eq 0 ]; then
			echo "[watch-loop] $(date '+%H:%M:%S') — $((INTERVAL - ELAPSED))s until next rebuild"
			./oro logs --tail 5 2>/dev/null | grep -v heartbeat | grep -v directive | grep -v missing_accept | tail -3 || true
		fi
	done

	echo "[watch-loop] 30 minutes elapsed — rebuilding..."
	launch_cycle
done
