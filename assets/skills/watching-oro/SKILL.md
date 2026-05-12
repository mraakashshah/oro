---
name: watching-oro
description: Use when running oro as a software factory and continuously observing, detecting defects, filing bug tasks, fixing via workers, and relaunching
user-invocable: true
---

# Watching Oro

Operate oro as a software factory. Run the swarm, observe behavior, detect defects, spec/task them, fix them (via workers when possible, manually when not), rebuild, relaunch, repeat.

## The Loop

```
LAUNCH → OBSERVE → DETECT → SPEC/TASK → FIX → REBUILD → RELAUNCH
   ↑                                                         |
   └─────────────────────────────────────────────────────────┘
```

Execute this loop continuously until the swarm runs clean or context runs low.

## Phase 1: Launch

```bash
make build && ./oro start --workers 3 --detach
./oro status   # confirm dispatcher running
```

## Phase 2: Observe

**No sleep loops.** Use event-driven techniques:

### Outcome-based autopilot

For unattended monitoring, start the installed autopilot monitor. It treats
fresh heartbeats as liveness only; throughput is healthy only when tasks close
or the queue shrinks within a bounded window.

```bash
tmux new-session -d -s oro-autopilot \
  'python3 ~/.oro/.claude/skills/watching-oro/scripts/oro_autopilot_monitor.py \
     --oro "$(command -v oro)" \
     --repo "$PWD" \
     --target 2 \
     --max-workers 2'
```

The monitor logs `THROUGHPUT_STALL` when workers stay busy without productive
closures for repeated checks, logs `QG_INCIDENT_INCREASE` when new QG incidents
appear, captures ready/closed/log snapshots, scales back to the target worker
count, and restarts the factory if throughput stalls persist.

### Tail-based observation loop

The primary observation pattern is a poll-on-demand loop using `./oro logs --tail` and `./oro directive status`:

```bash
# Snapshot key events (filter out noise)
./oro logs --tail 300 | grep -v heartbeat | grep -v directive | grep -v missing_accept | tail -20

# Worker context % and state (JSON — pipe through python or jq)
./oro directive status | python3 -c "
import json,sys
d=json.load(sys.stdin)
for w in d['workers']:
    task = w.get('bead_id', 'idle')
    ctx = w.get('context_pct', 0)
    print(f'{task:15s} ctx={ctx:3d}% state={w[\"state\"]}')
"

# Check worktree progress (commits + diffs)
for wt in .worktrees/oro-*/; do
  echo "=== $(basename $wt) ==="
  git -C "$wt" log --oneline -3
  git -C "$wt" diff --stat | tail -3
done

# Manager pane
tmux capture-pane -t oro:1 -p -S -30   # manager
```

### When to poll

- After each review/merge cycle — check what unblocked
- When a worker reaches >50% context — watch for degradation
- After `awaiting_review` events — do the review immediately
- After rebuild+relaunch — verify workers picked up new work

**What to watch**: See [deep-observation.md](references/deep-observation.md) for per-component techniques, DB queries, and failure pattern signatures. For operator cadence and incident policy, follow `docs/runbooks/oro-monitoring.md`.

### Key failure signatures

| Signal | Meaning |
|--------|---------|
| `goroutine_panic` / panic stack | Active incident — 60s cadence, file/fix bug, rebuild/restart after merge |
| Same event repeating >5x in 30s | Loop bug — spec immediately |
| `STUCK_WORKER` | Progress timeout — check worker context % |
| `WORKER_CRASH` with empty task ID | Auto-ack path — verify dispatcher handles it |
| `QG_FAILED` repeating for same task | Worker can't pass QG — check prompt or test |
| `MERGE_CONFLICT` without later `MERGED` | Stale worktree — needs manual rebase |
| Heartbeat `context_pct > 80` | Worker degrading — will likely fail |
| Pane activity stale >10min | Manager crashed — check pane |
| Assignment spam (same task >3x) | Rejection loop — check AC or worker prompt |

## Phase 3: Detect + Spec

When you observe a defect:

1. **Characterize**: What's the symptom? What component? Is it reproducible?
2. **Check if known**: `oro task list --status=open | grep -i "<keyword>"` — don't duplicate
3. **Spec it**: Use `spec` skill for systemic issues, or create a bug task directly:

```bash
oro task create --title="Bug: <symptom>" --type=bug --priority=1
oro task update <id> --description="..." --notes="Observed: <evidence>"
```

Set clear acceptance criteria so a worker (or you) can verify the fix.

## Phase 4: Fix

**Prefer workers** for isolated, well-scoped bugs:
```bash
# Workers pick up ready tasks automatically
# Verify it was assigned:
./oro directive status
```

**Fix manually** when:
- The bug is in oro itself (workers can't rebuild their own runtime)
- The fix requires rebuilding `oro` binary
- The bug blocks worker operation (dispatch loop, socket protocol, merge)
- Context/prompt issues that affect all workers

For manual fixes: use `work-bead` skill (TDD, worktree, merge to main).

## Phase 5: Rebuild + Relaunch

After merging fixes to main:

If the fix changes Oro runtime behavior, do not continue monitoring the old
dispatcher. Stop, rebuild/install, restart, then verify no fresh occurrence for
two 60s windows.

```bash
# 1. Graceful shutdown (non-interactive — ./oro stop requires TTY)
ORO_HUMAN_CONFIRMED=1 ./oro stop --force

# 2. Kill zombie workers (old workers reconnect to new dispatcher)
pkill -f "oro work" 2>/dev/null

# 3. Clean worktrees
for wt in .worktrees/oro-*/; do
  [ -d "$wt" ] && git worktree remove --force "$wt" 2>/dev/null
done

# 4. Rebuild
make build

# 5. Relaunch
./oro start --workers 3 --detach
./oro status
```

**Note:** `./oro stop` requires an interactive terminal. In agent/non-TTY contexts, use `ORO_HUMAN_CONFIRMED=1 ./oro stop --force`.

## Native Beadstore Errors During Monitoring

**NEVER run `force-initialization commands`.** It destroys all task history. This has happened 3 times.

When native beadstore errors occur during observation:

1. Inspect `./oro status`, logs, and event output for the failing component.
2. Verify the active SQLite state path and run `./oro task ready`,
   `./oro task blocked`, and `./oro task show <id>` directly.
3. If the store is damaged, follow `docs/runbooks/beadstore-recovery.md` and
   restore only from reviewed JSONL or SQLite backups.
4. If still broken: **ask the user**

Do not restart or repair Dolt from Oro. Don't panic. Don't nuke.

## Context Management

- At 40% context: switch to summary-only observation (report changes only)
- At 50%: create a handoff with current defect list and observation state
- File all unresolved observations as tasks before exiting

## Anti-Patterns

- Sleep-polling instead of `tail -f` / `--follow` / `fswatch`
- Fixing bugs without tasks (no tracking = no verification)
- Fixing oro runtime bugs via workers (they can't rebuild themselves)
- Continuing to observe after 3+ cycles with no new defects (declare clean)
- Skipping rebuild after merging a fix (stale binary = stale bugs)
