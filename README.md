# Oro

**Autonomous agent swarm orchestrator for software engineering.**

Oro is a self-managing multi-agent system that coordinates AI workers to execute software engineering tasks. An architect designs, a manager judges, a dispatcher orchestrates, and workers write code — all running concurrently in isolated git worktrees with TDD, quality gates, and code review baked into every cycle.

## Table of Contents

- [Philosophy](#philosophy)
- [Principles](#principles)
  - [1. Less Context, Better Work](#1-less-context-better-work)
  - [2. Compound Learnings](#2-compound-learnings)
  - [3. Loop Until Done](#3-loop-until-done)
  - [4. Better Specs, Better Outcomes](#4-better-specs-better-outcomes)
  - [5. Guards Over Trust](#5-guards-over-trust)
- [How Oro Creates Software](#how-oro-creates-software)
- [Architecture](#architecture)
  - [Memory System](#memory-system)
- [Quick Start](#quick-start)
  - [Prerequisites](#prerequisites)
  - [Install](#install)
  - [Launch](#launch)
  - [Basic Operations](#basic-operations)
- [CLI Reference](#cli-reference)
  - [Lifecycle](#lifecycle)
  - [Monitoring](#monitoring)
  - [Memory](#memory)
  - [Control](#control)
  - [Search](#search)
  - [Internal](#internal)
- [Key Concepts](#key-concepts)
  - [Beads](#beads)
  - [Epics](#epics)
  - [Quality Gate](#quality-gate)
  - [Worktrees](#worktrees)
  - [Handoffs](#handoffs)
  - [Ops Agents](#ops-agents)
- [Development](#development)
  - [Build](#build)
  - [Project Structure](#project-structure)

## Why "Oro"?

*Oro* is Spanish for **gold** — because that's what we're doing: mining. Sifting through the infinite possibility space of code, specs, and designs to extract the nuggets that actually work. Every bead is a dig site. Every memory is a vein worth returning to.

It's also the heart of ***ouro*boros** — the serpent that eats its own tail. Workers consume their own context, write a handoff, and a fresh worker picks up where they left off. The loop never ends. The serpent never stops eating. Context is finite; the work is not.

Also, our cute mascot is Oro, the *oro* ouroboros !
![Oro, the *oro* ouroboros](oro-mascot.png)

## Philosophy

Oro exists because single-agent coding sessions don't scale. One agent hits context limits, loses track of prior decisions, and can't parallelize. Oro solves this with a swarm: multiple workers execute beads (tracked work items) simultaneously, each in an isolated worktree, each with access to cross-session memory. When a worker exhausts its context window, it writes a handoff and a fresh worker picks up where it left off — the serpent eats its tail.

Quality is not optional. Every bead goes through TDD (red-green-refactor), an automated quality gate (tests + lint + format), and ops-agent code review before merging to main. Failed reviews get feedback and retry. Merge conflicts get an ops agent. Stuck workers get diagnosed. The system is opinionated about correctness because autonomous agents must earn trust through process, not promises.

Memory persists across sessions. Workers emit learnings during execution, the dispatcher extracts patterns from logs, and a FTS5-backed memory store surfaces relevant context to future workers. Decisions, gotchas, and patterns accumulate over time — the swarm gets smarter as it works.

## Principles

### 1. Less Context, Better Work

Agents produce better output when they see less. A worker that receives a tightly scoped bead — clear acceptance criteria, relevant memories, no noise — outperforms one drowning in an entire codebase. Oro decomposes work into atomic beads, assigns each to a worker in a clean worktree, and injects only the memories that match. Context is a budget: spend it on signal, not surface area.

### 2. Compound Learnings

Every session leaves the system smarter. Workers emit learnings during execution. The dispatcher extracts patterns from logs. Memory consolidation deduplicates and scores entries over time. High-frequency patterns get proposed for codification — a recurring workaround becomes a rule, a repeated sequence becomes a skill, a solved problem becomes a documented decision. Knowledge compounds; the swarm never re-learns the same lesson.

### 3. Loop Until Done

The ouroboros isn't a metaphor — it's the architecture. When a worker exhausts its context window, it writes a handoff and a fresh worker continues. When a bead fails review, it gets feedback and retries. When a merge conflicts, an ops agent resolves it. The system loops — handoff loops, review loops, retry loops — until the work is done or explicitly abandoned. No work is lost to context limits, flaky failures, or transient state.

### 4. Better Specs, Better Outcomes

The most leveraged investment is upstream. Oro spends tokens on brainstorming alternatives, stress-testing designs with premortems, and writing validated specs — before a single line of production code is written. A spec that resolves ambiguity, handles edge cases, and includes a testing strategy produces better code on the first pass. The pipeline is front-loaded by design: cheap tokens early prevent expensive rework later.

### 5. Guards Over Trust

Autonomous agents earn trust through mechanism, not promises. Oro wraps every bead in guards: TDD (failing test before code), a 19-check quality gate (tests, lint, format, type-check, vulnerability scan), ops-agent code review, and evidence-based verification. These guards aren't overhead — they're what let the system execute fearlessly. When correctness is enforced mechanically, you stop worrying about whether the agent "did the right thing" and start compounding velocity.

## How Oro Creates Software

Oro enforces a disciplined pipeline from idea to merged code. Every phase has a specific purpose, and no phase can be skipped — the system is designed so that cutting corners is harder than doing it right.

```
 Idea
  │
  ▼
 ┌─────────────────────────────────────────────────────────┐
 │  1. BRAINSTORM                                          │
 │  Research prior art in codebase and docs. Explore 2-3   │
 │  approaches with trade-offs. One question at a time.    │
 └─────────────────────────┬───────────────────────────────┘
                           │
                           ▼
 ┌─────────────────────────────────────────────────────────┐
 │  2. PREMORTEM                                           │
 │  Stress-test every design decision before committing.   │
 │  Tigers (likely + severe), elephants (ignored obvious   │
 │  problems), paper tigers (fears that aren't real).      │
 └─────────────────────────┬───────────────────────────────┘
                           │
                           ▼
 ┌─────────────────────────────────────────────────────────┐
 │  3. SPEC                                                │
 │  Write validated design to docs/plans/. Includes        │
 │  resolved premortems, architecture, data flow, error    │
 │  handling, and testing strategy.                        │
 └─────────────────────────┬───────────────────────────────┘
                           │
                           ▼
 ┌─────────────────────────────────────────────────────────┐
 │  4. PLAN                                                │
 │  Break spec into bite-sized implementation steps        │
 │  (2-5 min each). Exact file paths, code snippets,       │
 │  review checkpoints between tasks.                      │
 └─────────────────────────┬───────────────────────────────┘
                           │
                           ▼
 ┌─────────────────────────────────────────────────────────┐
 │  5. BEAD CRAFT                                          │
 │  Decompose plan into beads — atomic work items with     │
 │  testable acceptance criteria, dependencies, and        │
 │  priority. Each bead answers: "how do I know this is    │
 │  done?" with a runnable test command.                   │
 └─────────────────────────┬───────────────────────────────┘
                           │
                           ▼
 ┌─────────────────────────────────────────────────────────┐
 │  6. OBSERVE                                             │
 │  Before touching code, check actual system state.       │
 │  Read real outputs, run real commands. Mark confidence: │
 │  VERIFIED / INFERRED / UNCERTAIN. No assumptions.       │
 └─────────────────────────┬───────────────────────────────┘
                           │
                           ▼
 ┌─────────────────────────────────────────────────────────┐
 │  7. TDD (Red → Green → Refactor)                        │
 │  Write failing test from acceptance criteria. Watch it  │
 │  fail for the right reason. Write minimal code to pass. │
 │  Refactor while green. No production code without a     │
 │  failing test first.                                    │
 └─────────────────────────┬───────────────────────────────┘
                           │
                           ▼
 ┌─────────────────────────────────────────────────────────┐
 │  8. QUALITY GATE (19 checks)                            │
 │  go test -race · golangci-lint · gofumpt · goimports ·  │
 │  go vet · govulncheck · shellcheck · ruff · pyright ·   │
 │  markdownlint · yamllint · biome. All must pass.        │
 └─────────────────────────┬───────────────────────────────┘
                           │
                           ▼
 ┌─────────────────────────────────────────────────────────┐
 │  9. CODE REVIEW                                         │
 │  Ops agent reviews against acceptance criteria and      │
 │  spec. Feedback triaged as Critical / Important /       │
 │  Minor. Critical blocks merge. Up to 2 review cycles    │
 │  before escalation to Manager.                          │
 └─────────────────────────┬───────────────────────────────┘
                           │
                           ▼
 ┌─────────────────────────────────────────────────────────┐
 │  10. VERIFY                                             │
 │  Run verification fresh. Read output. Check exit code.  │
 │  Evidence-based assertions only — no "should work."     │
 └─────────────────────────┬───────────────────────────────┘
                           │
                           ▼
 ┌─────────────────────────────────────────────────────────┐
 │  11. MERGE + PUSH                                       │
 │  Rebase onto main. Fast-forward merge (linear history). │
 │  Push. Clean up worktree and branch. Document learnings.│
 └─────────────────────────────────────────────────────────┘
                           │
                           ▼
                    Code on main
```

This pipeline is encoded as skills — reusable process definitions that agents follow. The architect orchestrates the early phases (brainstorm through bead craft), while workers execute the later phases (observe through merge) autonomously. The dispatcher enforces the quality gate and review gates mechanically — a worker cannot merge without passing both.

The key insight: autonomous agents are only as trustworthy as their process. Oro doesn't trust agents to "do the right thing" — it structures the work so that the right thing is the only path forward.

## Architecture

```
 ┌─────────────────────────────────────────────────────────┐
 │                    tmux session "oro"                   │
 │  ┌──────────────────────┐  ┌──────────────────────────┐ │
 │  │  Architect (pane 0)  │  │  Manager (pane 1)        │ │
 │  │  Designs specs,      │  │  Judgment calls,         │ │
 │  │  creates beads,      │  │  merge conflicts,        │ │
 │  │  sets priorities     │  │  stuck workers,          │ │
 │  │                      │  │  scales swarm            │ │
 │  └──────────────────────┘  └──────────────────────────┘ │
 └─────────────────────────────────────────────────────────┘
                          │
                    beads (bd CLI)
                          │
                          ▼
 ┌─────────────────────────────────────────────────────────┐
 │              Dispatcher (background daemon)             │
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
# Go 1.24+
go version

# Claude Code CLI
claude --version

# Beads issue tracker
brew install beads
```

### Install

```bash
git clone https://github.com/yourusername/oro.git
cd oro
make setup      # npm deps, golangci-lint, git hooks
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
make setup          # Install dev tooling (npm deps, golangci-lint, git hooks)
make build          # Build oro binary
make build-dash     # Build TUI dashboard
make install        # Install to $GOPATH/bin
make test           # Run tests with race detector
make lint           # Run golangci-lint
make fmt            # Format Go files (gofumpt + goimports)
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

## References

Oro builds on foundational work and ideas from the AI coding agent community:

- **[Continuous Claude v3](https://github.com/parcadei/Continuous-Claude-v3)** — Context management pattern for persistent agent workflows using ledgers and handoffs
- **[Steve Yegge - Beads](https://github.com/steveyegge/beads)** — Git-backed issue tracker designed as external memory for coding agents
- **[Steve Yegge - Gastown](https://github.com/steveyegge/gastown)** — Multi-agent orchestration system with hierarchical roles and parallel execution
- **[Obra - Superpowers](https://github.com/obra/superpowers)** — Agentic skills framework and software development methodology with composable workflows
- **[Teresa Torres - Context Rot](https://www.producttalk.org/context-rot/)** — Why institutional knowledge decays and how to prevent it
- **[Every's Compound Engineering](https://every.to/guides/compound-engineering)** — Making each unit of work compound into the next through systematic learning loops
