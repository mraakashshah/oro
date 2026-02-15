# Oro

**Autonomous agent swarm orchestrator for software engineering.**

Oro (from *ouroboros* — the serpent that eats its own tail) is a self-managing multi-agent system that coordinates AI workers to execute software engineering tasks. An architect designs, a manager judges, a dispatcher orchestrates, and workers write code — all running concurrently in isolated git worktrees with TDD, quality gates, and code review baked into every cycle.

## Philosophy

Oro exists because single-agent coding sessions don't scale. One agent hits context limits, loses track of prior decisions, and can't parallelize. Oro solves this with a swarm: multiple workers execute beads (tracked work items) simultaneously, each in an isolated worktree, each with access to cross-session memory. When a worker exhausts its context window, it writes a handoff and a fresh worker picks up where it left off — the serpent eats its tail.

Quality is not optional. Every bead goes through TDD (red-green-refactor), an automated quality gate (tests + lint + format), and ops-agent code review before merging to main. Failed reviews get feedback and retry. Merge conflicts get an ops agent. Stuck workers get diagnosed. The system is opinionated about correctness because autonomous agents must earn trust through process, not promises.

Memory persists across sessions. Workers emit learnings during execution, the dispatcher extracts patterns from logs, and a FTS5-backed memory store surfaces relevant context to future workers. Decisions, gotchas, and patterns accumulate over time — the swarm gets smarter as it works.

## Architecture

```
 ┌─────────────────────────────────────────────────────────┐
 │                    tmux session "oro"                    │
 │  ┌──────────────────────┐  ┌──────────────────────────┐ │
 │  │  Architect (pane 0)  │  │  Manager (pane 1)        │ │
 │  │  Designs specs,      │  │  Judgment calls,          │ │
 │  │  creates beads,      │  │  merge conflicts,         │ │
 │  │  sets priorities     │  │  stuck workers,           │ │
 │  │                      │  │  scales swarm             │ │
 │  └──────────────────────┘  └──────────────────────────┘ │
 └─────────────────────────────────────────────────────────┘
                          │
                    beads (bd CLI)
                          │
                          ▼
 ┌─────────────────────────────────────────────────────────┐
 │              Dispatcher (background daemon)              │
 │  Polls ready beads → assigns to idle workers → merges   │
 │  UDS socket · SQLite state · heartbeat monitoring       │
 └────────┬──────────┬──────────┬──────────┬───────────────┘
          │          │          │          │
          ▼          ▼          ▼          ▼
      ┌────────┐ ┌────────┐ ┌────────┐ ┌────────┐
      │ w-01   │ │ w-02   │ │ w-03   │ │ w-04   │
      │ claude │ │ claude │ │ claude │ │ claude │
      │   -p   │ │   -p   │ │   -p   │ │   -p   │
      └────┬───┘ └────┬───┘ └────┬───┘ └────┬───┘
           │          │          │          │
           ▼          ▼          ▼          ▼
       worktree   worktree   worktree   worktree
      bead/abc   bead/def   bead/ghi   bead/jkl
```

**Dispatcher states:** `inert` → `running` → `paused` / `stopping`

**Worker lifecycle:** spawn → connect (UDS) → receive ASSIGN → execute bead via `claude -p` → run quality gate → send DONE → dispatcher merges to main → next bead

**Context exhaustion (ralph loop):** When a worker hits its context threshold, it writes a handoff file and signals the dispatcher. A fresh worker spawns in the same worktree, reads the handoff, and continues — no work lost.

### Memory System

Three layers of persistent memory:

| Layer | Storage | Scope | Access |
|-------|---------|-------|--------|
| **Bead annotations** | Beads DB | Per-work-item notes, acceptance criteria | `bd show <id>` |
| **Handoffs** | YAML files in worktree | Immediate task context for continuation | Auto-read by next worker |
| **Project memory** | SQLite FTS5 | Cross-session learnings, patterns, decisions | `oro remember` / `oro recall` |

Workers emit `[MEMORY]` markers during execution. The dispatcher also extracts learnings from logs post-session. Before assigning a bead, the dispatcher queries the top relevant memories and injects them into the worker's prompt.

## Quick Start

### Prerequisites

```bash
# Go 1.23+
go version

# Claude Code CLI
claude --version

# Beads issue tracker
brew install beads

# Dolt (beads backend)
brew install dolt

# Linter
brew install golangci-lint
```

### Install

```bash
git clone https://github.com/yourusername/oro.git
cd oro
make build
make install    # installs to $GOPATH/bin
```

### Launch

```bash
# Bootstrap config and verify dependencies
oro init

# Start the swarm (opens tmux session)
oro start

# Or start detached
oro start --detach
```

This creates a tmux session with an architect pane and a manager pane, starts the dispatcher daemon, and spawns the worker pool. The architect begins designing work, the manager monitors execution, and workers pick up beads as they become ready.

### Basic Operations

```bash
# Check swarm status
oro status

# Scale workers up
oro directive scale 4

# Pause new assignments (workers finish current beads)
oro directive pause

# Resume
oro directive resume

# Store a learning
oro remember "lesson: always validate input at system boundaries"

# Search memories
oro recall "input validation"

# Graceful shutdown
oro stop
```

## CLI Reference

### Lifecycle

| Command | Description | Example |
|---------|-------------|---------|
| `oro init` | Bootstrap dependencies and generate config | `oro init --check` |
| `oro start` | Launch the swarm (tmux + dispatcher + workers) | `oro start -w 4 --detach` |
| `oro stop` | Graceful shutdown | `oro stop` |
| `oro cleanup` | Clean stale state after a crash | `oro cleanup` |

**`oro init`** flags: `--check` (verify only), `--force` (overwrite config), `--project-root <dir>`, `--quiet`

**`oro start`** flags: `--workers, -w` (default: 2), `--model` (default: sonnet), `--detach, -D`, `--daemon-only, -d`

**`oro stop`** flags: `--force` (skip confirmation, requires `ORO_HUMAN_CONFIRMED=1`)

### Monitoring

| Command | Description | Example |
|---------|-------------|---------|
| `oro status` | Show current swarm state | `oro status` |
| `oro logs` | Query and tail dispatcher event logs | `oro logs --tail 50 -f` |
| `oro dash` | Launch interactive TUI dashboard | `oro dash` |

**`oro logs`** flags: `--tail <n>` (default: 20), `-f, --follow` (poll for new events)

**`oro logs`** with worker filter: `oro logs w-01 --follow`

### Memory

| Command | Description | Example |
|---------|-------------|---------|
| `oro remember` | Store a memory | `oro remember "gotcha: FTS5 requires content sync triggers"` |
| `oro recall` | Search memories | `oro recall "testing patterns"` |
| `oro forget` | Delete memories by ID | `oro forget 1 2 3` |
| `oro memories list` | Browse memories with filters | `oro memories list --type lesson --limit 10` |
| `oro memories consolidate` | Deduplicate and prune stale entries | `oro memories consolidate --dry-run` |

**`oro remember`** flags: `--pin` (skip time decay). Supports type hints: `lesson:`, `decision:`, `gotcha:`, `pattern:`

**`oro recall`** flags: `--id <n>` (fetch by ID), `--file <path>` (filter by file)

**`oro memories list`** flags: `--type <type>`, `--tag <tag>`, `--limit <n>` (default: 20)

**`oro memories consolidate`** flags: `--min-score <f>` (default: 0.1), `--similarity <f>` (default: 0.8), `--dry-run`

### Control

| Command | Description | Example |
|---------|-------------|---------|
| `oro directive` | Send a directive to the dispatcher | `oro directive scale 4` |

**Operations:** `start`, `stop`, `pause`, `resume`, `scale <n>`, `focus <epic>`, `status`

### Search

| Command | Description | Example |
|---------|-------------|---------|
| `oro index build` | Build semantic code search index | `oro index build --dir .` |
| `oro index search` | Search the code index | `oro index search "authentication handler" --top 5` |

**`oro index build`** flags: `--dir <path>` (default: cwd)

**`oro index search`** flags: `--top <n>` (default: 10)

### Internal

| Command | Description | Example |
|---------|-------------|---------|
| `oro worker` | Run a worker process (used by dispatcher) | `oro worker --socket /tmp/oro.sock --id w-01` |

## Key Concepts

### Beads

Work items tracked by the `bd` CLI. Each bead has a title, description, acceptance criteria, priority (P0–P4), type (task/feature/bug/epic), and optional dependencies. The dispatcher assigns ready beads (no unresolved blockers) to idle workers.

### Epics

Parent beads that group related work. The dispatcher can `focus` on an epic to prioritize its children. Epics are never directly assigned to workers — they close when all children complete.

### Quality Gate

An automated pipeline (`quality_gate.sh`) that every bead must pass before merging: `go test ./... -race` + `golangci-lint` + `gofumpt` + `goimports`. Workers run the gate after implementation. Failed gates mean the bead is not done.

### Worktrees

Each worker operates in an isolated git worktree (`bead/<id>` branch). This prevents conflicts between concurrent workers. On completion, the dispatcher rebases onto main and fast-forward merges — maintaining linear history.

### Handoffs

When a worker exhausts its context window, it writes a YAML handoff file capturing current progress, remaining work, and learnings. A fresh worker spawns in the same worktree and continues. This "ralph loop" means no bead is limited by a single context window.

### Ops Agents

Short-lived `claude -p` processes spawned by the dispatcher for operational tasks: code review (post-completion), merge conflict resolution, and stuck-worker diagnosis. Ops agents use Opus for high-fidelity judgment.

## Development

### Build

```bash
make build          # Build oro binary
make build-dash     # Build TUI dashboard
make install        # Install to $GOPATH/bin
make test           # Run tests with race detector
make lint           # Run golangci-lint
make gate           # Full quality gate
```

### Project Structure

```
oro/
├── cmd/
│   ├── oro/              # Main binary — CLI commands + dispatcher
│   ├── oro-dash/         # TUI dashboard (bubbletea)
│   └── oro-search-hook/  # Code search integration
├── pkg/
│   ├── dispatcher/       # Core orchestrator — state machine, worker pool, bead tracking
│   ├── worker/           # Worker agent — UDS connection, prompt assembly, subprocess
│   ├── memory/           # FTS5 memory store — insert, search, consolidate
│   ├── ops/              # Ops agent spawner — review, merge resolution, diagnosis
│   ├── merge/            # Merge coordinator — serialized rebase + ff-only
│   ├── protocol/         # Shared types, UDS messages, SQLite schema, constants
│   ├── codesearch/       # Semantic code search
│   └── integration/      # End-to-end test harness
├── docs/
│   ├── plans/            # Architecture specs and design docs
│   └── solutions/        # Documented solved problems
├── Makefile
├── quality_gate.sh
└── go.mod
```
