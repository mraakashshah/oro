---
name: watching-oro
description: Use when running oro as a software factory and continuously observing, detecting defects, filing bug beads, fixing via workers, and relaunching
user-invocable: true
---

# Watching Oro

Operate oro as a software factory. Run the swarm, observe behavior, detect defects, spec/bead them, fix them (via workers when possible, manually when not), rebuild, relaunch, repeat.

## The Loop

```
LAUNCH → OBSERVE → DETECT → SPEC/BEAD → FIX → REBUILD → RELAUNCH
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
    bead = w.get('bead_id', 'idle')
    ctx = w.get('context_pct', 0)
    print(f'{bead:15s} ctx={ctx:3d}% state={w[\"state\"]}')
"

# Check worktree progress (commits + diffs)
for wt in .worktrees/oro-*/; do
  echo "=== $(basename $wt) ==="
  git -C "$wt" log --oneline -3
  git -C "$wt" diff --stat | tail -3
done

# Architect/manager panes
tmux capture-pane -t oro:0 -p -S -30   # architect
tmux capture-pane -t oro:1 -p -S -30   # manager
```

### When to poll

- After each review/merge cycle — check what unblocked
- When a worker reaches >50% context — watch for degradation
- After `awaiting_review` events — do the review immediately
- After rebuild+relaunch — verify workers picked up new work

**What to watch**: See [deep-observation.md](references/deep-observation.md) for per-component techniques, DB queries, and failure pattern signatures.

### Key failure signatures

| Signal | Meaning |
|--------|---------|
| Same event repeating >5x in 30s | Loop bug — spec immediately |
| `STUCK_WORKER` | Progress timeout — check worker context % |
| `WORKER_CRASH` with empty bead ID | Auto-ack path — verify dispatcher handles it |
| `QG_FAILED` repeating for same bead | Worker can't pass QG — check prompt or test |
| `MERGE_CONFLICT` without later `MERGED` | Stale worktree — needs manual rebase |
| Heartbeat `context_pct > 80` | Worker degrading — will likely fail |
| Pane activity stale >10min | Manager/architect crashed — check pane |
| Assignment spam (same bead >3x) | Rejection loop — check AC or worker prompt |

## Phase 3: Detect + Spec

When you observe a defect:

1. **Characterize**: What's the symptom? What component? Is it reproducible?
2. **Check if known**: `oro bead list --status=open | grep -i "<keyword>"` — don't duplicate
3. **Spec it**: Use `spec` skill for systemic issues, or create a bug bead directly:

```bash
oro bead create --title="Bug: <symptom>" --type=bug --priority=1
oro bead update <id> --description="..." --notes="Observed: <evidence>"
```

Set clear acceptance criteria so a worker (or you) can verify the fix.

## Phase 4: Fix

**Prefer workers** for isolated, well-scoped bugs:
```bash
# Workers pick up ready beads automatically
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

## Dolt/Beads Errors During Monitoring

**NEVER run `force-initialization commands`.** It destroys all bead history. This has happened 3 times.

When bead database/Dolt errors occur during observation:

1. `check Dolt server status` → `restart the Dolt server` → `test Dolt connectivity`
2. If still broken: `run bead-store server diagnostics` → `run non-destructive bead-store repair`
3. If still broken: `rebuild from JSONL backup`
4. If still broken: **ask the user**

The dispatcher survives dolt outages — workers stay connected, only bead assignment pauses. Don't panic. Don't nuke.

## Context Management

- At 40% context: switch to summary-only observation (report changes only)
- At 50%: create a handoff with current defect list and observation state
- File all unresolved observations as beads before exiting

## Anti-Patterns

- Sleep-polling instead of `tail -f` / `--follow` / `fswatch`
- Fixing bugs without beads (no tracking = no verification)
- Fixing oro runtime bugs via workers (they can't rebuild themselves)
- Continuing to observe after 3+ cycles with no new defects (declare clean)
- Skipping rebuild after merging a fix (stale binary = stale bugs)
