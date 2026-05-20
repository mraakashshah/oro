# Oro

**Autonomous agent swarm orchestrator for software engineering.**

Oro is a self-managing multi-agent system that coordinates AI workers to execute software engineering tasks. A manager judges, a dispatcher orchestrates, and workers write code — all running concurrently in isolated git worktrees with TDD, quality gates, and code review baked into every cycle.

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
  - [Uninstall](#uninstall)
  - [Build from Source](#build-from-source)
  - [Launch](#launch)
  - [Basic Operations](#basic-operations)
- [CLI Reference](#cli-reference)
  - [Lifecycle](#lifecycle)
  - [Monitoring](#monitoring)
  - [Memory](#memory)
  - [Control](#control)
  - [Search](#search)
  - [Single-Worker Mode](#single-worker-mode)
  - [Maintenance](#maintenance)
  - [Internal](#internal)
- [Key Concepts](#key-concepts)
  - [Tasks](#tasks)
  - [Epics](#epics)
  - [Quality Gate](#quality-gate)
  - [Worktrees](#worktrees)
  - [Handoffs](#handoffs)
  - [Ops Agents](#ops-agents)
- [Development](#development)
  - [Build](#build)
  - [Project Structure](#project-structure)
- [Claude Runtime Compatibility](#claude-runtime-compatibility)

## Why "Oro"?

*Oro* is Spanish for **gold** — because that's what we're doing: mining. Sifting through the infinite possibility space of code, specs, and designs to extract the nuggets that actually work. Every task is a dig site. Every memory is a vein worth returning to.

It's also the heart of ***ouro*boros** — the serpent that eats its own tail. Workers consume their own context, write a handoff, and a fresh worker picks up where they left off. The loop never ends. The serpent never stops eating. Context is finite; the work is not.

Also, our cute mascot is Oro, the *oro* ouroboros !
![Oro, the *oro* ouroboros](assets/oro-mascot.png)

## Philosophy

Oro exists because single-agent coding sessions don't scale. One agent hits context limits, loses track of prior decisions, and can't parallelize. Oro solves this with a swarm: multiple workers execute tasks (tracked work items) simultaneously, each in an isolated worktree, each with access to cross-session memory. When a worker exhausts its context window, it writes a handoff and a fresh worker picks up where it left off — the serpent eats its tail.

Quality is not optional. Every task goes through TDD (red-green-refactor), an automated quality gate (tests + lint + format), and ops-agent code review before merging to main. Failed reviews get feedback and retry. Merge conflicts get an ops agent. Stuck workers get diagnosed. The system is opinionated about correctness because autonomous agents must earn trust through process, not promises.

Memory persists across sessions. Workers emit learnings during execution, the dispatcher extracts patterns from logs, and a FTS5-backed memory store surfaces relevant context to future workers. Decisions, gotchas, and patterns accumulate over time — the swarm gets smarter as it works.

## Principles

### 1. Less Context, Better Work

Agents produce better output when they see less. A worker that receives a tightly scoped task — clear acceptance criteria, relevant memories, no noise — outperforms one drowning in an entire codebase. Oro decomposes work into atomic tasks, assigns each to a worker in a clean worktree, and injects only the memories that match. Context is a budget: spend it on signal, not surface area.

### 2. Compound Learnings

Every session leaves the system smarter. Workers emit learnings during execution. The dispatcher extracts patterns from logs. Memory consolidation deduplicates and scores entries over time. High-frequency patterns get proposed for codification — a recurring workaround becomes a rule, a repeated sequence becomes a skill, a solved problem becomes a documented decision. Knowledge compounds; the swarm never re-learns the same lesson.

### 3. Loop Until Done

The ouroboros isn't a metaphor — it's the architecture. When a worker exhausts its context window, it writes a handoff and a fresh worker continues. When a task fails review, it gets feedback and retries. When a merge conflicts, an ops agent resolves it. The system loops — handoff loops, review loops, retry loops — until the work is done or explicitly abandoned. No work is lost to context limits, flaky failures, or transient state.

### 4. Better Specs, Better Outcomes

The most leveraged investment is upstream. Oro spends tokens on brainstorming alternatives, stress-testing designs with premortems, and writing validated specs — before a single line of production code is written. A spec that resolves ambiguity, handles edge cases, and includes a testing strategy produces better code on the first pass. The pipeline is front-loaded by design: cheap tokens early prevent expensive rework later.

### 5. Guards Over Trust

Autonomous agents earn trust through mechanism, not promises. Oro wraps every task in guards: TDD (failing test before code), a 19-check quality gate (tests, lint, format, type-check, vulnerability scan), ops-agent code review, and evidence-based verification. These guards aren't overhead — they're what let the system execute fearlessly. When correctness is enforced mechanically, you stop worrying about whether the agent "did the right thing" and start compounding velocity.

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
 │  5. TASK CRAFT                                          │
 │  Decompose plan into tasks — atomic work items with     │
 │  testable acceptance criteria, dependencies, and        │
 │  priority. Each task answers: "how do I know this is    │
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

This pipeline is encoded as skills — reusable process definitions that agents follow. Workers execute each phase (observe through merge) autonomously. The dispatcher enforces the quality gate and review gates mechanically — a worker cannot merge without passing both.

The key insight: autonomous agents are only as trustworthy as their process. Oro doesn't trust agents to "do the right thing" — it structures the work so that the right thing is the only path forward.

## Architecture

```
 ┌─────────────────────────────────────────────────────────┐
 │                    tmux session "oro"                   │
 │  ┌─────────────────────────────────────────────────┐   │
 │  │  Manager (pane 0)                               │   │
 │  │  Judgment calls, merge conflicts,               │   │
 │  │  stuck workers, scales swarm                    │   │
 │  └─────────────────────────────────────────────────┘   │
 └─────────────────────────────────────────────────────────┘
                          │
                    oro task CLI
                          │
                          ▼
 ┌─────────────────────────────────────────────────────────┐
 │              Dispatcher (background daemon)             │
 │  Polls ready tasks → assigns to idle workers → merges   │
 │  UDS socket · SQLite state · heartbeat monitoring       │
 └────────┬──────────┬──────────┬──────────┬───────────────┘
          │          │          │          │
          ▼          ▼          ▼          ▼
      ┌────────┐ ┌────────┐ ┌────────┐ ┌────────┐
      │ w-01   │ │ w-02   │ │ w-03   │ │ w-04   │
      │ agent  │ │ agent  │ │ agent  │ │ agent  │
      │   -p   │ │   -p   │ │   -p   │ │   -p   │
      └────┬───┘ └────┬───┘ └────┬───┘ └────┬───┘
           │          │          │          │
           ▼          ▼          ▼          ▼
       worktree   worktree   worktree   worktree
      agent/abc  agent/def  agent/ghi  agent/jkl
```

**Dispatcher states:** `inert` → `running` → `paused` / `stopping`

**Worker lifecycle:** spawn → connect (UDS) → receive ASSIGN → execute task via the configured runtime adapter → run quality gate → send DONE → dispatcher merges to main → next task

**Context exhaustion (ralph loop):** When a worker hits its context threshold, it writes a handoff file and signals the dispatcher. A fresh worker spawns in the same worktree, reads the handoff, and continues — no work lost.

### Memory System

Three layers of persistent memory:

| Layer | Storage | Scope | Access |
|-------|---------|-------|--------|
| **Task annotations** | Native task store | Per-work-item notes, acceptance criteria | `oro task show <id>` |
| **Handoffs** | YAML files in worktree | Immediate task context for continuation | Auto-read by next worker |
| **Project memory** | SQLite FTS5 | Cross-session learnings, patterns, decisions | `oro remember` / `oro recall` |

Workers emit `[MEMORY]` markers during execution. The dispatcher also runs LLM-based extraction (background tier) on session output to catch patterns workers didn't explicitly tag. Before assigning a task, the dispatcher queries the top relevant memories and injects them into the worker's prompt — annotated with age so workers verify stale claims (>7 days) against current code.

**Dreaming:** Every 10 completed tasks (or when an epic closes), the dispatcher spawns a dreaming ops agent that reads the entire memories table, synthesizes cross-session patterns, resolves contradictions, merges duplicates, and prunes obsolete entries. The swarm gets smarter over time without human curation.

## Quick Start

### Prerequisites

Runtime requirements (macOS only):

```bash
# Supported agent runtime CLIs
claude --version
codex --version

# tmux (for swarm sessions)
brew install tmux
```

### Install

```bash
curl -fsSL https://raw.githubusercontent.com/mraakashshah/oro/main/scripts/install.sh | bash
```

The installer downloads the latest `oro` binary (pre-built for macOS amd64/arm64), verifies its SHA-256 checksum, and places it in `/usr/local/bin` (or `~/.local/bin` if `/usr/local/bin` isn't writable). The binary is self-contained — assets (hooks, skills, beacons) auto-extract to `~/.oro/` on first run.

Then in your project:

```bash
cd your-project
oro setup
```

`oro setup` checks prerequisites, detects project languages, installs missing dev tools, bootstraps `.oro/` config, and runs a health check. By default it uses stealth mode — zero footprint in the project directory.

For Codex workers, install and authenticate the Codex CLI before setup:

```bash
codex --version
```

Codex assets are installed under `$CODEX_HOME` when set, otherwise `~/.codex`.
Oro writes portable Codex skills, Codex `prefix_rule` command-permission rules,
an Oro local marketplace package, and project `AGENTS.md` instructions. See
[docs/runbooks/codex-setup.md](docs/runbooks/codex-setup.md) for the plugin
discovery path, `.codex-plugin/plugin.json` layout, marketplace registration,
and current limitations. Case B (interactive Codex sessions) is deferred to a future spec; the current parity scope is dispatcher-spawned Codex workers.

### Uninstall

```bash
oro uninstall
```

Removes the binary, `~/.oro/`, launchd agents, legacy `.beads` symlinks in known projects, `.oro/` anchor dirs, oro-managed git hooks (restores `.user` backups), and oro entries from the global gitignore. Use `--force` to skip the confirmation prompt, or `--keep-data` to preserve `~/.oro/` (databases, task history).

### Build from Source

For development or contributing:

```bash
# Prerequisites: Go 1.26, Node.js (for npm), Python (for uv)
git clone https://github.com/mraakashshah/oro.git
cd oro
make setup      # npm deps, golangci-lint, git hooks
make install    # builds and installs oro, oro-search-hook, and runtime assets
```

### Launch

Once `oro setup` has completed in your project:

```bash
# Start the swarm (opens tmux session)
oro start

# Or start detached
oro start --detach
```

This creates a tmux session with a manager pane, starts the dispatcher daemon, and spawns the worker pool. The manager monitors execution, and workers pick up tasks as they become ready.

### Basic Operations

```bash
# Check swarm status
oro status

# Inspect health findings
oro health

# Observe health continuously
oro monitor --target 2 --max-workers 2 --interval 60s

# Scale workers up
oro directive scale 4

# Pause new assignments (workers finish current tasks)
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
| `oro setup` | User-friendly project setup (prereq check, language detect, tools, bootstrap, health check) | `oro setup my-project` |
| `oro init` | Lower-level bootstrap (config + assets), used by `oro setup` | `oro init --check` |
| `oro start` | Launch the swarm (tmux + dispatcher + workers) | `oro start -w 4 --detach` |
| `oro stop` | Graceful shutdown | `oro stop` |
| `oro cleanup` | Clean stale state after a crash | `oro cleanup` |
| `oro uninstall` | Remove oro and all its artifacts from this machine | `oro uninstall --force` |

**`oro setup`** flags: `--project-root <dir>`, `--dev` (install dev-only tools), `--dry-run`, `--skip-tools`, `--force` (overwrite existing config)

**`oro init`** flags: `--check` (verify only), `--force` (overwrite config), `--project-root <dir>`, `--quiet`, `--local` (in-repo mode: create `.oro/` in project root). Default is stealth mode — zero footprint, config stored under `~/.oro/projects/s-<hash>/`.

**`oro start`** flags: `--workers, -w` (default: 2), `--max-workers` (hard ceiling for autoscale, scale directives, spawn-for, and manual `oro worker launch` reservations), `--model` (tier name or provider model name — default: `balanced`), `--detach, -D`, `--daemon-only, -d`, `--manual-integration` (leave completed worker branches/worktrees for coordinator review instead of auto-merging)

**`oro dispatcher start`** flags: `--workers, -w` (default: 0), `--force, -f`, `--manual-integration` (leave completed worker branches/worktrees for coordinator review instead of auto-merging)

**`oro stop`** flags: `--force` (skip confirmation, requires `ORO_HUMAN_CONFIRMED=1`)

**`oro uninstall`** flags: `--force` (skip confirmation prompt), `--keep-data` (preserve `~/.oro/` — databases and task history)

### Monitoring

| Command | Description | Example |
|---------|-------------|---------|
| `oro status` | Show current swarm state | `oro status` |
| `oro health` | Show factory health findings | `oro health --json` |
| `oro monitor` | Observe health and optionally perform bounded recovery | `oro monitor --target 2 --max-workers 2 --interval 60s` |
| `oro throughput` | Report swarm throughput health | `oro throughput --window 2h` |
| `oro logs` | Query and tail dispatcher event logs | `oro logs --tail 50 -f` |
| `oro dashboard` | Show the local web dashboard | `oro dashboard` |

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

### Single-Worker Mode

| Command | Description | Example |
|---------|-------------|---------|
| `oro work` | Execute a single task interactively (no dispatcher) | `oro work oro-abc1 --model deep` |

**`oro work`** flags: `--model` (tier name or provider model name — default: `balanced`), `--timeout` (default: 15m), `--skip-review`, `--base-branch`

### Maintenance

| Command | Description | Example |
|---------|-------------|---------|
| `oro doctor` | Diagnose oro installation issues | `oro doctor` |

### Internal

| Command | Description | Example |
|---------|-------------|---------|
| `oro worker` | Run a worker process (used by dispatcher) | `oro worker --socket /tmp/oro.sock --id w-01` |

## Key Concepts

### Tasks

Work items tracked by the native `oro task` CLI. Each task has a title, description, acceptance criteria, priority (P0-P4), type (task/feature/bug/epic), and optional dependencies. The dispatcher assigns ready tasks (no unresolved blockers) to idle workers.

### Task Terminology

- **Task:** preferred public term for an Oro work item.
- **Bead:** legacy/internal term still visible in storage, historical docs, compatibility CLI, and migration artifacts.
- **Task type:** the `type` field, whose values include `task`, `bug`, `epic`, `research`, and `chore`.

### Epics

Parent tasks that group related work. The dispatcher can `focus` on an epic to prioritize its children. When an epic is first assigned, a worker decomposes it into child tasks. Child tasks merge to an isolated `epic/<epicID>` branch (not main). When all children complete, the epic branch passes a quality gate check, then fast-forward merges to main.

### Quality Gate

An automated pipeline (`scripts/quality_gate.sh`) that every task must pass before merging: `go test ./... -race` + `golangci-lint` + `gofumpt` + `goimports`. Workers run the gate after implementation. Failed gates mean the task is not done.

The quality gate is generated during `oro init` / `oro setup` based on detected project languages. For projects with no recognized languages, a shell-only quality gate is still generated (shellcheck + markdownlint) so that tasks always have a gate to pass.

### Dead Pane Detection

When the dispatcher sends commands to worker tmux panes via `SendKeysVerified`, it first checks whether the target pane is still alive. If the pane has exited or been killed, the command fails fast with an actionable error message instead of hanging or silently dropping input. This prevents the dispatcher from getting stuck waiting on a dead worker.

### Worktrees

Each worker operates in an isolated git worktree on an `agent/<id>` branch. This prevents conflicts between concurrent workers. On completion, the dispatcher rebases onto main and fast-forward merges — maintaining linear history.

### Handoffs

When a worker exhausts its context window, it writes a YAML handoff file capturing current progress, remaining work, and learnings. A fresh worker spawns in the same worktree and continues. This "ralph loop" means no task is limited by a single context window.

### Ops Agents

Short-lived runtime subprocesses spawned by the dispatcher for judgment-heavy tasks: code review (post-completion), merge conflict resolution, stuck-worker diagnosis, acceptance criteria writing, and memory dreaming (cross-session synthesis). Current default routing still maps review and diagnosis to the deep tier and dreaming to the background tier.

## Development

### Build

```bash
make setup          # Install dev tooling (npm deps, golangci-lint, git hooks)
make build          # Build oro + oro-search-hook
make install        # Build and install oro + oro-search-hook + runtime assets
make test           # Run tests with race detector
make lint           # Run golangci-lint
make fmt            # Format Go files (gofumpt + goimports)
make gate           # Full quality gate
make release V=x.y.z # Tag and push (triggers GitHub Actions release)
```

### Project Structure

```
oro/
├── cmd/
│   ├── oro/              # Main binary — CLI commands + dispatcher
│   ├── oro-dash/         # TUI dashboard helpers (library package)
│   └── oro-search-hook/  # Code search integration
├── pkg/
│   ├── dispatcher/       # Core orchestrator — state machine, worker pool, task tracking
│   ├── worker/           # Worker agent — UDS connection, prompt assembly, subprocess
│   ├── memory/           # FTS5 memory store — insert, search, consolidate
│   ├── ops/              # Ops agent spawner — review, merge resolution, diagnosis
│   ├── merge/            # Merge coordinator — serialized rebase + ff-only
│   ├── protocol/         # Shared types, UDS messages, SQLite schema, constants
│   ├── dashboard/        # Dashboard data and headless view helpers
│   ├── codesearch/       # Semantic code search
│   ├── eventlog/         # Queryable event log
│   ├── langprofile/      # Language detection for quality gate generation
│   └── integration/      # End-to-end test harness
├── scripts/
│   ├── install.sh        # curl installer
│   └── quality_gate.sh   # Automated quality gate runner
├── docs/
│   ├── plans/            # Active specs, design docs, and plan notes
│   ├── runbooks/         # Operator procedures, logs, incidents, drills
│   ├── learnings/        # Synthesized learnings and prior-art studies
│   ├── audits/           # Technical audits and pressure reviews
│   ├── research/         # Comparative research
│   ├── solutions/        # Documented solved problems
│   └── archive/          # Superseded or compatibility-only docs
├── .goreleaser.yml       # Release build config
├── Makefile
└── go.mod
```

## Claude Runtime Compatibility

Oro is runtime-agnostic — workers dispatch tasks to whatever AI agent CLI is configured. Claude (`claude`) is the current primary runtime; Codex (`codex`) is also supported.

The `--model` flag and the bead `model` field accept **tier names** (preferred) or provider-specific model names (accepted for explicit overrides):

| Tier | Claude model | Typical use |
|------|-------------|-------------|
| `fast` | `haiku` | Quick lookups, formatting, lightweight tasks |
| `balanced` | `sonnet` | Standard implementation tasks (default) |
| `deep` | `opus` | Complex architecture, multi-file refactors |
| `background` | `haiku` | Memory extraction, dreaming, ops subtasks |

Tier names are the stable interface — they remain valid across Claude model generations. When the configured runtime is not Claude, tier names route to the runtime's equivalent capability level.

**Prerequisite for Claude runtime:**

```bash
claude --version   # must be installed and authenticated
```

## References

Oro builds on foundational work and ideas from the AI coding agent community:

- **[Garry Tan - Gstack](https://github.com/garrytan/gstack)** — An open source software factory
- **[Teresa Torres - Context Rot](https://www.producttalk.org/context-rot/)** — Why AI gets worse the longer you chat
- **[Every's Compound Engineering](https://every.to/guides/compound-engineering)** — Making each unit of work compound into the next through systematic learning loops
- **[Teresa Torres - Context Rot](https://www.producttalk.org/context-rot/)** — Why institutional knowledge decays and how to prevent it
- **[Continuous Claude v3](https://github.com/parcadei/Continuous-Claude-v3)** — Context management pattern for persistent agent workflows using ledgers and handoffs
- **[Steve Yegge - Beads](https://github.com/steveyegge/beads)** — Git-backed issue tracker designed as external memory for coding agents
- **[Obra - Superpowers](https://github.com/obra/superpowers)** — Agent skill framework for building disciplined AI coding workflows
