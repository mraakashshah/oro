# Oro

**Autonomous agent swarm orchestrator for software engineering.**

Oro is a self-managing multi-agent system that coordinates AI workers to execute software engineering tasks. A dispatcher orchestrates and workers write code — all running concurrently in isolated git worktrees with TDD, quality gates, and code review baked into every cycle.

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
  - [Ops / Recovery](#ops--recovery)
  - [Search](#search)
  - [Single-Worker Mode](#single-worker-mode)
  - [Maintenance](#maintenance)
  - [Internal](#internal)
- [Key Concepts](#key-concepts)
  - [Tasks](#tasks)
  - [Epics](#epics)
  - [Quality Gate](#quality-gate)
  - [Worktrees](#worktrees)
  - [Context Transfer](#context-transfer)
  - [Ops Agents](#ops-agents)
  - [Escalations](#escalations)
- [Development](#development)
  - [Build](#build)
  - [Project Structure](#project-structure)
- [Runtime Compatibility](#runtime-compatibility)

## Why "Oro"?

*Oro* is Spanish for **gold** — because that's what we're doing: mining. Sifting through the infinite possibility space of code, specs, and designs to extract the nuggets that actually work. Every task is a dig site. Every memory is a vein worth returning to.

It's also the heart of ***ouro*boros** — the serpent that eats its own tail. Workers consume their own context, checkpoint the state that matters, and a fresh worker picks up where they left off. The loop never ends. The serpent never stops eating. Context is finite; the work is not.

Also, our cute mascot is Oro, the *oro* ouroboros !
![Oro, the *oro* ouroboros](assets/oro-mascot.png)

## Philosophy

Oro exists because single-agent coding sessions don't scale. One agent hits context limits, loses track of prior decisions, and can't parallelize. Oro solves this with a swarm: multiple workers execute tasks (tracked work items) simultaneously, each in an isolated worktree, each with relevant knowledge cards. When a worker exhausts its context window, Oro carries the task/worktree forward with continuation context — the serpent eats its tail.

Quality is not optional. Every task goes through TDD (red-green-refactor), an automated quality gate (tests + lint + format + language-specific checks), and ops-agent code review before merging to main. Failed reviews get feedback and retry. Merge conflicts get an ops agent. Stuck workers get diagnosed. The system is opinionated about correctness because autonomous agents must earn trust through process, not promises.

Knowledge persists across sessions. Workers emit learnings during execution, context renders preserve immediate task state, and durable cards surface relevant rules, patterns, decisions, facts, and taste to future workers. The swarm gets smarter as it works.

## Principles

### 1. Less Context, Better Work

Agents produce better output when they see less. A worker that receives a tightly scoped task — clear acceptance criteria, relevant cards, no noise — outperforms one drowning in an entire codebase. Oro decomposes work into atomic tasks, assigns each to a worker in a clean worktree, and injects only the cards that match. Context is a budget: spend it on signal, not surface area.

### 2. Compound Learnings

Every session leaves the system smarter. Workers emit learnings during execution. The dispatcher turns useful patterns into card candidates, and reviewed cards become durable rules, patterns, decisions, facts, or taste. High-frequency patterns get proposed for codification — a recurring workaround becomes a rule, a repeated sequence becomes a skill, a solved problem becomes a documented decision. Knowledge compounds; the swarm never re-learns the same lesson.

### 3. Loop Until Done

The ouroboros isn't a metaphor — it's the architecture. When a worker exhausts its context window, Oro preserves enough context for a fresh worker to continue. When a task fails review, it gets feedback and retries. When a merge conflicts, an ops agent resolves it. The system loops — continuation loops, review loops, retry loops — until the work is done or explicitly abandoned. No work is lost to context limits, flaky failures, or transient state.

### 4. Better Specs, Better Outcomes

The most leveraged investment is upstream. Oro spends tokens on brainstorming alternatives, stress-testing designs with premortems, and writing validated specs — before a single line of production code is written. A spec that resolves ambiguity, handles edge cases, and includes a testing strategy produces better code on the first pass. The pipeline is front-loaded by design: cheap tokens early prevent expensive rework later.

### 5. Guards Over Trust

Autonomous agents earn trust through mechanism, not promises. Oro wraps every task in guards: TDD (failing test before code), a lane-based quality gate (tests, lint, format, builds, vulnerability checks, and language-specific checks), ops-agent code review, and evidence-based verification. These guards aren't overhead — they're what let the system execute fearlessly. When correctness is enforced mechanically, you stop worrying about whether the agent "did the right thing" and start compounding velocity.

## How Oro Creates Software

Oro enforces a disciplined pipeline from idea to merged code. Every phase has a specific purpose, and no phase can be skipped — the system is designed so that cutting corners is harder than doing it right.

```text
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
 │  8. QUALITY GATE                                        │
 │  Lane-based checks: format, lint, tests, builds, vet,   │
 │  vulnerability scan, shell/docs/config, and optional    │
 │  mutation testing. All required lanes must pass.        │
 └─────────────────────────┬───────────────────────────────┘
                           │
                           ▼
 ┌─────────────────────────────────────────────────────────┐
 │  9. CODE REVIEW                                         │
 │  Ops agent reviews against acceptance criteria and      │
 │  spec. Feedback triaged as Critical / Important /       │
 │  Minor. Critical blocks merge. Up to 2 review cycles    │
 │  before dispatcher escalation.                          │
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

```text
 ┌─────────────────────────────────────────────────────────┐
 │                    tmux session "oro"                   │
 │  Attach surface for monitoring and operator control     │
 └─────────────────────────────────────────────────────────┘
                          │
                    oro CLI / directives
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

**Escalation path:** Escalation is dispatcher-managed, not a separate supervising pane. The dispatcher persists the event, routes supported cases to short-lived ops agents, and leaves anything unrouted visible through health and recovery commands. Missing acceptance criteria can spawn an AC writer; oversized tasks can spawn decomposition; stuck workers, merge conflicts, and priority contention can spawn one-shot triage.

**Context exhaustion (ralph loop):** When a worker hits its context threshold, it sends structured continuation context to the dispatcher. A fresh worker can spawn in the same worktree with relevant context and cards, then continue — no work lost.

### Memory System

Four layers of persistent and operational memory:

| Layer | Storage | Scope | Access |
|-------|---------|-------|--------|
| **Task annotations** | Native task store | Per-work-item notes, acceptance criteria | `oro task show <id>` |
| **Context render** | Current task/event/card state | Immediate task context for continuation | `oro current`, `oro handoff --since 4h` |
| **Knowledge cards** | Card store | Durable rules, patterns, decisions, facts, and taste | `oro cards ...` |
| **Event log** | SQLite event store | Dispatcher and worker history | `oro logs`, `oro events` |

Workers and continuation flows can queue learning candidates during execution. Review commands promote the useful candidates into cards, and assignment prompts use relevant cards instead of the retired prompt-memory path.

**Cards:** Cards carry type, summary, full body, tags, score, contradiction state, retirement metadata, and lineage. They are the long-lived knowledge layer that workers receive in prompt context and operators maintain through the `oro cards` command group.

## Quick Start

### Prerequisites

Runtime requirements for release installs:

```bash
# macOS release archives are built for darwin amd64/arm64.

# Required by oro setup
claude --version
git --version
brew --version

# Optional runtime when using Codex workers
codex --version

# tmux (for swarm sessions)
brew install tmux
```

### Install

```bash
curl -fsSL https://raw.githubusercontent.com/mraakashshah/oro/main/scripts/install.sh | bash
```

The installer downloads the latest macOS release archive, verifies its SHA-256 checksum, installs `oro` into `/usr/local/bin` or `~/.local/bin`, installs `oro-search-hook` under `~/.oro/hooks`, and installs bundled dylibs under `~/.oro/lib` when they are present. Installer options include `--dry-run`, `--version VERSION`, and `--prefix DIR`.

Then in your project:

```bash
cd your-project
oro setup
```

`oro setup` checks prerequisites, detects project languages, installs missing dev tools, writes in-repo `.oro/config.yaml`, extracts assets to `~/.oro`, installs Oro git hooks, generates `scripts/quality_gate.sh`, and runs a health check.

For zero-footprint mode, use `oro init` without `--local`. Use `oro init --local` when you want in-repo config.

For Codex workers, install and authenticate the Codex CLI before selecting a Codex runtime:

```bash
codex --version
oro agent-assets --runtime codex
```

Codex assets are installed under `$CODEX_HOME` when set, otherwise `~/.codex`. Oro links portable skills into `$CODEX_HOME/skills`, writes command-permission rules and managed hooks directly, and generates project `AGENTS.md` instructions. No plugin registration or interactive installation is required. See [docs/runbooks/codex-setup.md](docs/runbooks/codex-setup.md) for the direct discovery path and startup contract.

### Uninstall

```bash
oro uninstall
```

Removes the binary, `~/.oro/`, launchd agents, `.oro/` anchor dirs, oro-managed git hooks (restores `.user` backups), and oro entries from the global gitignore. Use `--force` to skip the confirmation prompt, or `--keep-data` to preserve `~/.oro/` (databases, task history).

### Build from Source

For development or contributing:

```bash
# Prerequisites: Go 1.26.4, Node.js/npm, Python 3.13+, and uv
git clone https://github.com/mraakashshah/oro.git
cd oro
make setup      # git hooks, npm deps, golangci-lint, NilAway, Python deps
make install    # builds and installs oro, oro-search-hook, and runtime assets
```

### Launch

Once `oro setup` or `oro init` has completed in your project:

```bash
# Start the swarm
oro start

# Or start detached
oro start --detach

# Or start dashboard/health HTTP endpoints
oro start --web
```

This starts the dispatcher daemon, creates a plain tmux attach surface, and spawns the worker pool. The dispatcher monitors execution, and workers pick up tasks as they become ready.

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

# Pause new assignments (workers finish current tasks; monitor --act preserves this hold)
oro directive pause

# Resume
oro directive resume

# Render live context and transfer material
oro current
oro handoff --since 4h

# Review queued learning candidates
oro cards review-queue

# Inspect a knowledge card
oro cards show card-abc123

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

**`oro setup`** flags: `--project-root <dir>`, `--dev` (install dev-only tools), `--dry-run`, `--skip-tools`, `--force`

**`oro init`** flags: `--check` (verify only), `--force` (overwrite regenerated Oro assets and quality gate files), `--project-root <dir>`, `--quiet`, `--local` (in-repo mode: create `.oro/` in project root), `--skip-wizard`. Default is stealth mode — zero footprint, config stored under `~/.oro/projects/s-<hash>/`.

**`oro start`** flags: `--workers, -w` (default: 2), `--max-workers` (default: same as `--workers`), `--model` (tier name or provider model name — default: `balanced`), `--detach, -D`, `--daemon-only, -d`, `--manual-integration`, `--base-branch`, `--mutation-testing`, `--web`, `--web-addr`, `--progress-timeout`, `--ops-review-timeout`, `--review-stall-timeout`

Janitor and audit cleanliness roles are enabled by default. Their `oro start` controls are `--janitor-enabled` (default: `true`), `--audit-enabled` (default: `true`; requires janitor), `--janitor-interval` (default: every 50 completed merges; `0` disables janitor), `--janitor-idle-threshold` (default: `0`, so janitor waits unless the task queue is empty), `--audit-every-n-janitors` (default: every 5 janitor cycles; `0` disables periodic audits), and `--janitor-top-k` (default: `5`). By default, each janitor cycle files its top five findings; set `--janitor-top-k=0` to use the janitor's natural detector limit instead.

**`oro dispatcher start`** flags: `--workers, -w` (default: 0), `--force, -f`, `--manual-integration`, `--mutation-testing`

**`oro stop`** flags: `--force` (skip confirmation, requires `ORO_HUMAN_CONFIRMED=1`), `--all`

**`oro uninstall`** flags: `--force` (skip confirmation prompt), `--keep-data` (preserve `~/.oro/` — databases and task history)

### Monitoring

| Command | Description | Example |
|---------|-------------|---------|
| `oro status` | Show current swarm state | `oro status` |
| `oro health` | Show factory health findings | `oro health --json` |
| `oro monitor` | Observe health and optionally perform bounded recovery | `oro monitor --target 2 --max-workers 2 --interval 60s` |
| `oro throughput` | Report swarm throughput health | `oro throughput --window 2h` |
| `oro logs` | Query and tail dispatcher event logs | `oro logs --tail 50 -f` |
| `oro events` | Query structured event history | `oro events --type WORKER_DONE --limit 20` |
| `oro dashboard` | Show the local web dashboard | `oro dashboard` |

**`oro logs`** flags: `--tail <n>` (default: 20), `-f, --follow` (poll for new events), `--raw`

**`oro logs`** with worker filter: `oro logs w-01 --follow`

### Memory

| Command | Description | Example |
|---------|-------------|---------|
| `oro current` | Render current task context and relevant cards | `oro current --format json` |
| `oro handoff` | Render recent context for transfer | `oro handoff --since 4h` |
| `oro cards create` | Create a manual knowledge card | `oro cards create pattern "Validate inputs at boundaries" --summary "Check inputs before crossing system boundaries"` |
| `oro cards list` | List active card summaries | `oro cards list --type pattern --limit 10` |
| `oro cards show` | Show a full card | `oro cards show card-abc123 --json` |
| `oro cards review-queue` | List queued learning candidates | `oro cards review-queue` |
| `oro cards promote` | Promote a reviewed candidate to a card | `oro cards promote 42` |
| `oro cards reject` | Reject a queued candidate | `oro cards reject 42` |
| `oro cards retire` | Retire a stale or superseded card | `oro cards retire card-abc123 --reason "superseded"` |
| `oro cards import-from-memory` | Import legacy markdown memory files into cards | `oro cards import-from-memory ~/.oro/memory --dry-run` |
| `oro cards check-drift` | Check legacy migration mirror drift | `oro cards check-drift --backfill --dry-run` |
| `oro cards memory-retirement-check` | Verify legacy memory retirement readiness | `oro cards memory-retirement-check` |

**`oro cards create`** flags: `--summary <text>` (required), `--body <text>`, `--tag <tag>` (repeatable), `--confidence <float>`

**`oro cards list`** flags: `--type <rule|pattern|taste|decision|fact>`, `--include-retired`, `--limit <n>`

**`oro cards show`** flags: `--json`

**`oro cards retire`** flags: `--reason <text>` (required), `--superseded-by <card-id>`

### Control

| Command | Description | Example |
|---------|-------------|---------|
| `oro directive` | Send a directive to the dispatcher | `oro directive scale 4` |

**Operations:** `start`, `stop`, `pause`, `resume`, `scale <n>`, `focus <epic>`, `status`, `restart-worker`, `preempt`, `worker-logs`, `max-workers`, `pending-escalations`, `ack-escalation`

### Ops / Recovery

| Command | Description | Example |
|---------|-------------|---------|
| `oro ops list` | List durable ops agent runs | `oro ops list --json` |
| `oro ops retry` | Supersede and retry a failed or stale ops run | `oro ops retry 42` |
| `oro ops resolve` | Mark an ops run resolved after validating the condition | `oro ops resolve 42 --reason "operator checked"` |
| `oro directive pending-escalations` | List pending escalations that have not been acknowledged | `oro directive pending-escalations` |
| `oro directive ack-escalation` | Acknowledge an obsolete pending escalation | `oro directive ack-escalation 7` |

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

**`oro work`** flags: `--model` (tier name or provider model name — default: `balanced`), `--runtime claude|codex`, `--timeout`, `--review-timeout`, `--skip-review`, `--dry-run`, `--dry-run-spawn`, `--auto`, `--base-branch`, `--mutation-testing`

### Maintenance

| Command | Description | Example |
|---------|-------------|---------|
| `oro doctor` | Diagnose oro installation issues | `oro doctor` |
| `oro models` | Manage semantic model artifacts | `oro models list` |
| `oro leakscan` | Scan stdin, diffs, or files for credential-looking material | `oro leakscan --diff HEAD~1..HEAD` |

### Internal

| Command | Description | Example |
|---------|-------------|---------|
| `oro worker` | Run a worker process (used by dispatcher) | `oro worker --socket /tmp/oro.sock --id w-01` |
| `oro agent-assets` | Install Claude/Codex runtime assets | `oro agent-assets --runtime all` |

## Key Concepts

### Tasks

Work items tracked by the native `oro task` CLI. Each task has a title, description, acceptance criteria, numeric priority, type, dependencies, parent, owner, notes, tags, and metadata. Priority `0` is highest; default create priority is `2`. The dispatcher assigns ready tasks (no unresolved blockers) to idle workers.

### Task Terminology

- **Task:** an Oro work item.
- **Task type:** the `type` field. Dispatcher routing currently treats `task`, `bug`, and `chore` as executable; `research` is a stubbed oracle path; `epic` and `review` are non-executable.

### Epics

Parent tasks that group related work. The dispatcher can `focus` on an epic to prioritize its children. When an epic is first assigned, a worker decomposes it into child tasks. Child tasks merge to an isolated `epic/<epicID>` branch (not main). When all children complete, the epic branch can run an acceptance command, pass a quality gate check, then fast-forward merge to the target branch. If that fast-forward merge fails, Oro creates a rebase child task and leaves the epic open.

### Quality Gate

An automated lane-based pipeline (`scripts/quality_gate.sh`) that every automatically merged task must pass before merging. The repository gate includes Go formatting, `golangci-lint`, NilAway, dead-export checks, beadstore import checks, Go tests with coverage, Go builds, `go vet`, CGO-free builds, `govulncheck`, Python checks when Python files exist, shell checks, markdownlint, yamllint, Biome JSON checks, and optional mutation testing.

The quality gate is generated during `oro init` / `oro setup` based on detected project languages. For projects with no recognized languages, a shell/docs/config gate is still generated so that tasks always have a gate to pass.

Common environment controls include `ORO_QG_GOMAXPROCS`, `ORO_SKIP_MUTATION`,
`ORO_MUTATION_BASE`, and `ORO_QG_CONTEXT`. For repository pushes, GitHub
Actions is the authoritative full gate; ordinary pre-push hooks perform only
fast ref-safety checks. Run `scripts/quality_gate.sh` explicitly for a full
local check.

### Dead Pane Detection

When the dispatcher sends commands to worker tmux panes via `SendKeysVerified`, it first checks whether the target pane is still alive. If the pane has exited or been killed, the command fails fast with an actionable error message instead of hanging or silently dropping input. This prevents the dispatcher from getting stuck waiting on a dead worker.

### Worktrees

Each worker operates in an isolated git worktree on an `agent/<id>` branch. This prevents conflicts between concurrent workers. On completion, the dispatcher rebases onto the base branch and fast-forward merges when automatic integration is enabled — maintaining linear history. Worker worktrees receive a dispatcher-managed `quality_gate.sh` snapshot so stale branches cannot bypass current quality gate fixes.

### Context Transfer

Use `oro current` to inspect live task context and relevant cards. Use `oro handoff --since 4h` to render transfer context for continuation. This is a render, not the old stored YAML file workflow. Separately, the worker/dispatcher still has an internal `HANDOFF` protocol message for context-limit continuation.

### Ops Agents

Short-lived runtime subprocesses spawned by the dispatcher for judgment-heavy tasks: code review (post-completion), merge conflict resolution, stuck-worker diagnosis, acceptance criteria writing, and memory dreaming (cross-session synthesis). Routing depends on active agent configuration. Without an `agent` block, Oro uses its built-in Codex-coding/Claude-review route; initialized projects write an agent provider mode.

### Escalations

Escalations are exceptional dispatcher states. Every escalation is recorded before delivery or routing. Supported types are handled by ops agents when possible: `MISSING_AC` writes acceptance criteria, `OVERSIZED_BEAD` decomposes large tasks, and `STUCK_WORKER`, `MERGE_CONFLICT`, and `PRIORITY_CONTENTION` can run one-shot triage. Other pending escalations remain operator-visible through `oro health`, `oro ops list`, and `oro directive pending-escalations` until retried, resolved, or acknowledged.

## Development

### Build

```bash
make setup           # Install dev tooling (npm deps, golangci-lint, NilAway, Python deps)
make build           # Build oro + oro-search-hook
make install         # Build and install oro + oro-search-hook + runtime assets
make test            # Run tests with race detector and shuffle
make lint            # Run golangci-lint plus NilAway
make fmt             # Format Go and shell files
make gate            # Full quality gate
make release V=x.y.z # Tag and push vX.Y.Z
```

### Project Structure

```text
oro/
├── cmd/
│   ├── oro/              # Main binary — CLI commands + dispatcher
│   ├── oro-capture-hook/ # Capture hook binary
│   ├── oro-dash/         # Headless dashboard snapshot/diff-test command
│   └── oro-search-hook/  # Code search integration
├── internal/
│   └── appversion/       # Build/version metadata
├── pkg/
│   ├── agentassets/      # Claude/Codex runtime asset generation
│   ├── agentmodel/       # Role/tier/model resolution
│   ├── agentruntime/     # Claude/Codex runtime adapters
│   ├── beadstore/        # Native SQLite task store
│   ├── cards/            # Knowledge cards — durable rules, patterns, decisions
│   ├── codesearch/       # Semantic code search
│   ├── codestruct/       # Code structure helpers
│   ├── config/           # Agent runtime configuration
│   ├── dashboard/        # Dashboard data and headless view helpers
│   ├── dbutil/           # SQLite helpers
│   ├── dispatcher/       # Core orchestrator — state machine, worker pool, task tracking
│   ├── edit/             # Edit helpers
│   ├── embed/            # Embedding/reranking factories
│   ├── eventlog/         # Queryable event log
│   ├── factoryhealth/    # Health findings
│   ├── langprofile/      # Language detection for quality gate generation
│   ├── leakscan/         # Credential leak scanning
│   ├── lint/             # Lint helpers
│   ├── merge/            # Merge coordinator — serialized rebase + ff-only
│   ├── modelartifacts/   # ONNX model artifact specs/downloads
│   ├── ops/              # Ops agent spawner — review, merge resolution, diagnosis
│   ├── processenv/       # Subprocess cache/temp env isolation
│   ├── protocol/         # Shared types, UDS messages, task constants
│   ├── testutil/         # Test utilities
│   ├── web/              # Dashboard/health HTTP server
│   └── worker/           # Worker agent — UDS connection, prompt assembly, subprocess
├── assets/               # Embedded skills, hooks, rules, commands, beacons
├── scripts/
│   ├── README.md         # Developer and operator script catalog
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
├── git/hooks/            # Canonical repository git hooks
├── .goreleaser.yml       # Release build config
├── Makefile
└── go.mod
```

## Runtime Compatibility

Oro is runtime-aware. Workers dispatch tasks to the configured AI agent CLI. Claude (`claude`) and Codex (`codex`) are supported.

The `--model` flag and task `model` field accept **tier names** (preferred) or provider-specific model names (accepted for explicit overrides):

| Tier | Typical use |
|------|-------------|
| `fast` | Quick lookups, formatting, lightweight tasks |
| `balanced` | Standard implementation tasks |
| `deep` | Complex architecture, multi-file refactors, review-heavy tasks |
| `background` | Memory extraction, dreaming, ops subtasks |

Tier names are the stable interface. Concrete model names are resolved from the active agent config. Without an `agent` block, fast/background work uses Codex Luna at low reasoning, ordinary work uses Terra at medium, deep/escalated work uses Sol at high, and reviews use Claude Fable. `oro init` writes an `agent.provider_mode` by default; the default provider mode is `codex-coding-claude-review`.

Provider modes:

```text
codex-only
claude-only
codex-coding-claude-review
claude-coding-codex-review
```

Useful runtime environment variables:

```text
ORO_AGENT_RUNTIME=claude|codex
ORO_HOME
ORO_PROJECT
ORO_DB_PATH
ORO_SOCKET_PATH
ORO_PID_PATH
CODEX_HOME
CLAUDE_CONFIG_DIR
ORO_SQLITE_VEC_LIB
ANTHROPIC_API_KEY
```

## References

Oro builds on foundational work and ideas from the AI coding agent community:

- **[Garry Tan - Gstack](https://github.com/garrytan/gstack)** — An open source software factory
- **[Teresa Torres - Context Rot](https://www.producttalk.org/context-rot/)** — Why AI gets worse the longer you chat
- **[Every's Compound Engineering](https://every.to/guides/compound-engineering)** — Making each unit of work compound into the next through systematic learning loops
- **[Continuous Claude v3](https://github.com/parcadei/Continuous-Claude-v3)** — Context management pattern for persistent agent workflows using ledgers
- **[Continuous Claude V4.7](https://github.com/parcadei/ContinuousClaudeV4.7)** — Updated context-management lineage for continuous Claude workflows
- **[Obra - Superpowers](https://github.com/obra/superpowers)** — Agent skill framework for building disciplined AI coding workflows
