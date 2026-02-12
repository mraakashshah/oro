# Restart Oro

Nuke all oro processes, clean stale state, rebuild, relaunch, and monitor until healthy.

Execute these phases in order. Do NOT skip steps. Report status after each phase.

## Phase 1: Kill Everything

```bash
# Kill tmux session (swarm UI)
tmux kill-session -t oro 2>/dev/null || true

# Kill dispatcher and workers
./oro cleanup 2>&1 || true

# Belt and suspenders: kill any strays
pkill -f "oro start" 2>/dev/null || true
pkill -f "oro worker" 2>/dev/null || true
```

Verify nothing remains:
```bash
ps aux | grep -E '[o]ro (start|worker|dispatch)' | head -5
tmux list-sessions 2>&1
```

## Phase 2: Clean Stale State

```bash
# Remove stale worktree references
git worktree prune

# Delete ALL agent/* branches (safe — they're ephemeral worker branches)
for b in $(git branch | grep 'agent/' | tr -d ' +'); do
  git branch -D "$b" 2>&1
done

# Remove leftover worktree directories
rm -rf .worktrees/

# Verify clean
git worktree list
git branch | grep agent/ || echo "No stale agent branches"
```

## Phase 3: Build

```bash
make build 2>&1
```

If build fails, STOP and report the error. Do not proceed to Phase 4.

## Phase 4: Launch

```bash
./oro start 2>&1
```

The "open terminal failed: not a terminal" error is expected (no TTY in Bash tool). The dispatcher should still start. Verify:

```bash
sleep 5 && ./oro status 2>&1
```

Confirm dispatcher is running before proceeding.

## Phase 5: Monitor (3 observation cycles)

Run 3 observation cycles, 20 seconds apart. In each cycle, collect:

```bash
./oro status 2>&1
./oro logs 2>&1 | tail -20
tmux capture-pane -t oro:0 -p 2>&1 | tail -15
tmux capture-pane -t oro:1 -p 2>&1 | tail -15
```

**Watch for these failure patterns:**
- `worktree_error` repeating in logs (infinite retry bug)
- `missing_acceptance` repeating in logs (assignment spam)
- Workers stuck at 0 after 60s
- Dispatcher state not `running`
- Any panic or fatal error in tmux panes

After 3 cycles, report:
1. Dispatcher state and worker count
2. Any errors or warnings observed
3. What beads are assigned and making progress
4. Overall verdict: HEALTHY or NEEDS ATTENTION (with details)
