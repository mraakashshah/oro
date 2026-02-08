# Oro Architecture Spec

**Date:** 2026-02-08
**Status:** Draft — in progress with human review

## Roles

### Architect (interactive Claude session, tmux pane 0)

- **Human's primary interface** to the system
- Helps the human understand the codebase, create specs, shape work
- **Only output: beads.** Does not write code. Ever.
- Can spawn Claude subagents for research/analysis (never coding)
- Communicates priority to the manager by creating beads
- Runs as bare `claude` with project CLAUDE.md — no special role prompt needed

### Manager (interactive Claude session, tmux pane 1)

- **Gets stuff done** via the dispatcher and workers
- Does not write code except as absolute last resort (broken merges)
- Creates beads (decomposition, new work discovered)
- Spawns oro workers by telling the dispatcher swarm size
- Sends directives to dispatcher via UDS (start, stop, pause, scale, focus)
- Can spawn Claude subagents for non-coding tasks
- Two input sources: human (interactive) and dispatcher (tmux send-keys escalations)
- Needs `oro` CLI commands to send directives

### Workers (Go binary + claude -p subprocess, isolated worktrees)

- Execute beads — this is where code gets written
- Work in isolated git worktrees
- Create beads when:
  - Merge fails → P0 bead
  - Bead too big → decompose into smaller beads
  - Context limit hit → decompose remaining work into new beads
- Can spawn Claude subagents for coding subtasks
- Communicate with dispatcher via UDS (heartbeat, status, done, handoff)

### Dispatcher (Go binary, background daemon)

- **Purely mechanical** — no judgment, no AI
- Manages worker lifecycle (UDS connections, heartbeats, crash detection)
- Assigns beads to idle workers (reacts to bead changes via fsnotify)
- Executes merges (rebase + ff-only onto main)
- Spawns ops agents (review, merge conflict resolution, diagnosis)
- Escalates to manager via tmux send-keys
- Processes directives from manager via UDS
- Starts with swarm size 0 — inert until manager says go

### Ops Agents (short-lived claude -p processes)

- Spawned by dispatcher for specific tasks
- Review, merge conflict resolution, stuck-worker diagnosis
- Disposable — run task, report result, exit

## Communication

### Universal language: Beads

Everyone reads and writes beads. Beads are the primary coordination mechanism.

- Architect → creates beads (human's intent)
- Manager → creates beads (decomposition, discovered work)
- Workers → create beads (failures, decomposition, context handoff)
- Dispatcher → reads beads (assignment), closes beads (completion)

### Push-based, not polling

| Channel | Mechanism | Direction |
|---------|-----------|-----------|
| Manager → Dispatcher | UDS directives | Push (manager connects to dispatcher socket) |
| Dispatcher → Workers | UDS messages | Push (ASSIGN, SHUTDOWN, etc.) |
| Workers → Dispatcher | UDS messages | Push (HEARTBEAT, DONE, HANDOFF, etc.) |
| Dispatcher → Manager | tmux send-keys | Push (escalations) |
| Bead changes → Dispatcher | fsnotify on .beads/ | Push (react to new/changed beads) |

### Directives (Manager → Dispatcher via UDS)

| Directive | Effect |
|-----------|--------|
| `start` | Inert → Running (begin assigning beads) |
| `stop` | → Stopping (finish current, no new assignments) |
| `pause` | → Paused (workers continue, no new assignments) |
| `scale N` | Set target swarm size to N workers |
| `focus <epic>` | Prioritize beads from specific epic |

### Escalations (Dispatcher → Manager via tmux send-keys)

| Escalation | Trigger |
|------------|---------|
| Semantic merge conflict | Tests fail after ops agent resolution |
| Stuck worker | Bead fails after 2 context cycles |
| Priority contention | All slots busy + new P0 arrives |

## Lifecycle

### Session Start

1. User runs `oro start`
2. Dispatcher Go binary launches (background, **inert**, swarm size 0)
3. Tmux session created: architect (pane 0) + manager (pane 1)
4. Architect: bare `claude` — human starts interacting
5. Manager: `claude -p '<manager_prompt>'` — autonomous but interactable
6. Manager sends `start` + `scale N` directives via UDS when ready

### Steady State

1. Dispatcher watches .beads/ via fsnotify for new ready work
2. When bead becomes ready + idle worker available → create worktree, send ASSIGN
3. Worker executes bead (TDD, quality gate, commit)
4. Worker signals DONE → dispatcher merges → dispatcher closes bead
5. On failure/conflict → ops agent or escalation
6. Loop continues until `stop` directive

### Session End

1. User runs `oro stop` (or tells manager to stop)
2. Manager sends `stop` directive via UDS
3. Dispatcher: graceful shutdown (finish merges, drain workers, remove worktrees)
4. `bd sync`, git commit, git push

## Open Questions

- [ ] Manager prompt: what should it actually contain? (current prompt is outdated)
- [ ] Worker prompt template: assembled by Go binary — what sections?
- [ ] How does `scale N` work mechanically? Dispatcher spawns worker processes? Or manager spawns them and they connect?
- [ ] Should the manager be `-p` (autonomous) or interactive `claude`? User said interactive, but it also runs autonomously. Clarify: is it `claude '<initial_message>'` (interactive with first message)?
- [ ] fsnotify: watch .beads/issues.jsonl? Or individual bead files?
- [ ] SQLite commands table: remove in favor of UDS-only? Or keep as audit log?
