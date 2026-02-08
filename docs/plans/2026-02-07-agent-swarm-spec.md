# Agent Swarm Patterns: Parallel Agents with Git Worktrees, Commits, and Coordination

**Date:** 2026-02-07
**Bead:** oro-on7
**Status:** Spec

---

## Problem Statement

The current `dispatching-parallel-agents` skill is a "fire and forget" pattern: the main session spawns subagents via the Task tool, they edit files in the shared working directory, and the main session reviews and commits everything. This works for 2-3 agents editing disjoint files but breaks down at scale:

1. **Git index contention.** All subagents share one working directory with one `.git/index`. Two agents running `git add` or `git commit` simultaneously corrupt the index or create race conditions. The current skill explicitly warns: "Don't use when agents would edit same files (conflicts)."

2. **Commit bottleneck.** Agents cannot commit their own work. The main session must review every agent's output, stage files, and commit -- serializing all integration through one context window. With 5+ agents this becomes the critical path.

3. **Push races.** Even if agents could commit, pushing to the same remote branch from multiple processes creates fast-forward failures and force-push risks.

4. **Context pollution.** The main session must hold enough context to review all agent output. Reading agent results (even summaries) scales linearly with agent count.

5. **No isolation guarantee.** Two agents that happen to touch the same file (even different functions) will silently overwrite each other's changes -- whoever writes last wins.

**Bottom line:** The current pattern is a single-workspace, no-commit, manager-reviews-everything model. It works for small parallelism but is architecturally limited. We need a model where agents work in isolation, commit independently, and merge through a controlled protocol.

---

## Prior Art Summary

### Oro's Own Worker Model (IPC Comparison + Arch Review)

Oro's full orchestration design (documented in `2026-02-07-orchestrator-ipc-comparison.md`) uses:
- **Worktree per worker.** Each Worker Go binary operates in its own git worktree.
- **UDS + SQLite coordination.** Real-time signals over Unix domain sockets; durable state in SQLite.
- **Merge coordinator.** Manager acquires a merge lock, rebases the worker's branch onto main, and fast-forward merges. Conflicts trigger worker-side resolution with retry.
- **Ralph handoff.** Workers detect context exhaustion and hand off to fresh workers in the same worktree via `.oro/handoff.yaml`.
- **Two-stage review.** Self-review then spec-compliance review before merge.

This is the "heavy" tier -- full daemon-based orchestration for sustained multi-agent work. Overkill for a session where you just need 3 agents to fix 3 test files.

### CC-v3 (Continuous Claude v3)

Three relevant skills:
- **`parallel-agents`**: Background agents write completion status to `.claude/cache/<batch>-status.txt`. No TaskOutput. Max 15 agents per batch. File-based confirmation only.
- **`parallel-agent-contracts`**: Type ownership map prevents duplicate type definitions. Grep-before-create pattern. Type checker (tsc) as the integration contract.
- **`agent-orchestration`**: Main session spawns agents to preserve its own context. Agents read their own files. Main gets ~200 token summary.

**Key insight from CC-v3:** File-based status reporting and type ownership maps are lightweight coordination mechanisms that work without worktrees. The contract pattern (canonical type map + verification command) prevents the most common parallel agent failure mode (conflicting definitions).

### Compound Engineering Plugin

- **`resolve_parallel`**: Analyzes TODOs, builds a dependency graph (mermaid), spawns one agent per independent TODO. Dependency-aware -- sequential when needed, parallel when safe.
- **13+ parallel review agents** synthesized into consolidated findings.

**Key insight from Compound:** Dependency analysis before dispatch. Not all tasks are independent -- some must be sequenced. A mermaid/graph-based planner prevents wasted parallel work.

### Superpowers

- **`dispatching-parallel-agents`**: Similar to Oro's current skill but with a decision-tree (graphviz) for when to use. Emphasizes self-contained prompts with all context embedded. No shared memory between agents.
- **Two-stage review** pattern: self-review then spec-compliance review then code-quality review.

**Key insight from Superpowers:** The review pattern catches integration issues post-agent. Agents are optimistic workers; review is the pessimistic gate.

### Industry Patterns (Web Research)

Git worktrees for parallel AI agents is now a well-established pattern:
- **Cursor Parallel Agents** (2026): Each agent gets its own worktree, branch, chat history, and terminal environment. Changes merge only after tests pass.
- **Claude Code Swarm Mode** (2026): Multi-agent orchestrator using worktrees. Each agent modifies its own copy; changes merge into main after passing tests.
- **Common pattern**: `git worktree add .worktrees/<branch> -b <branch>` per agent, agent commits freely in its worktree, merge back to main via PR or ff-only merge.

---

## Design Decisions

### Decision 1: Isolation Model

| Option | Description | Pros | Cons |
|--------|-------------|------|------|
| **A: Git worktrees** | `git worktree add` per agent. Shared `.git`, separate working dirs. | Lightweight (shared objects), instant branch switching, proven pattern | Worktree creation takes ~1-3s, `.worktrees/` must be gitignored, shared lock file can block concurrent `git` operations |
| **B: Separate branches (no worktree)** | Agents work on branches in the same working dir, switching via `git stash`/`checkout` | No setup cost | Impossible for true parallelism -- only one branch checked out at a time. Sequential, not parallel. |
| **C: Full clones** | `git clone` per agent | Complete isolation, no shared locks | Expensive (full copy), no shared objects, divergent history requires fetch+merge |

**Recommendation: A (Git worktrees).** This is the industry standard for parallel AI agents. Branches alone (B) don't give parallel filesystem isolation. Full clones (C) are wasteful. Worktrees give each agent an independent working directory while sharing the object store.

### Decision 2: Commit Policy

| Option | Description | Pros | Cons |
|--------|-------------|------|------|
| **A: Agent commits in worktree, manager merges** | Agent commits freely to its worktree branch. Manager rebases onto main and ff-merges. | Agent is self-contained, can run tests against its own commits. Clean git history per agent. | Merge step required. Rebase can fail. |
| **B: Agent writes files, manager commits** | Agent only edits files. Manager reviews diff and commits. (Current model.) | Manager has full control. No merge needed. | Doesn't scale. Manager becomes bottleneck. Agent can't test its own committed state. |
| **C: Agent commits to main directly (sequential lock)** | Agents take turns committing to main via a lock. | Simple. No merge. | Serializes all commits. Defeats purpose of parallelism. Lock contention with 5+ agents. |

**Recommendation: A (Agent commits in worktree, manager merges).** This is the only option that scales. The agent owns its branch, commits as needed, runs tests against committed state, and signals completion. The merge step is the coordination point.

### Decision 3: Merge Strategy

| Option | Description | Pros | Cons |
|--------|-------------|------|------|
| **A: Fast-forward only (rebase first)** | Rebase agent branch onto main, then `git merge --ff-only`. | Linear history. Easy to bisect. No merge commits. | Rebase can conflict. Must serialize merges (only one rebase at a time). |
| **B: Merge commits** | `git merge --no-ff agent-branch`. | Preserves branch topology. Parallelizable merges. | Noisy history. Harder to bisect. |
| **C: Squash merge** | `git merge --squash agent-branch`. | One commit per agent. Clean history. | Loses per-commit granularity. Agent's intermediate commits invisible on main. |

**Recommendation: C (Squash merge) for simple tier, A (FF-only) for medium/heavy tiers.** Simple tasks (one logical change) benefit from squash -- one clean commit per agent. Complex tasks with meaningful intermediate commits benefit from ff-only to preserve history. The merge coordinator should support both, selected per task.

### Decision 4: Conflict Resolution

| Option | Description | Pros | Cons |
|--------|-------------|------|------|
| **A: Fail-fast (abort + escalate)** | If rebase/merge conflicts, abort and notify the main session. | Simple. No risk of bad auto-resolution. | Main session becomes the conflict resolution bottleneck. |
| **B: Auto-resolve (simple conflicts)** | Attempt `git rerere` or simple heuristic resolution (import ordering, adjacent non-overlapping lines). Escalate true conflicts. | Handles 80% of mechanical conflicts automatically. | Risk of silent bad merges. Requires test verification after resolution. |
| **C: Worker resolves** | Send conflict back to the agent that produced the change. Agent has the most context to resolve. | Best resolution quality. Agent knows its own intent. | Consumes agent context. May require ralph if agent is near context limit. |

**Recommendation: A (Fail-fast) for simple tier, B+C hybrid for medium/heavy tiers.** In the simple tier, conflicts are rare (agents edit disjoint files by design) and escalation is fast. In the heavy tier, Oro's existing design (R1 in IPC comparison) already specifies: attempt rebase, auto-resolve if trivial, worker resolves if semantic, escalate to Architect only if tests fail after resolution.

### Decision 5: Coordination / Completion Signaling

| Option | Description | Pros | Cons |
|--------|-------------|------|------|
| **A: Task notifications (current)** | Claude Code's built-in Task tool notifications. Agent completes, main gets a system reminder. | Zero setup. Built-in. | No structured data. Main must parse notification text. |
| **B: File-based** | Agent writes status to a known file (e.g., `.worktrees/<name>/STATUS`). Main polls or watches. | Simple. Inspectable. Works across processes. | Polling is wasteful. File format must be agreed. |
| **C: UDS/IPC** | Structured messages over Unix domain sockets. (Oro's full model.) | True push. Typed messages. Bidirectional. | Requires daemon infrastructure. Overkill for Task-based agents. |

**Recommendation: A (Task notifications) for simple tier, B (file-based) for medium tier, C (UDS) for heavy tier.** The simple tier uses Claude Code's built-in Task tool -- no extra infrastructure. The medium tier adds a status file per worktree for structured completion data. The heavy tier uses Oro's full UDS protocol.

### Decision 6: Worktree Lifecycle

| Option | Description | Pros | Cons |
|--------|-------------|------|------|
| **A: Manager creates and destroys** | Main session creates worktrees before dispatch, removes after merge. | Centralized control. Clean lifecycle. | Main session does setup work. Sequential creation. |
| **B: Agent creates and destroys** | Agent prompt includes worktree creation. Agent cleans up when done. | Decentralized. Agent is self-contained. | Agent might fail before cleanup. Orphan worktrees. |
| **C: Pre-provisioned pool** | Pool of N worktrees created at session start. Assigned to agents on demand. Recycled. | No per-task creation cost. Fast dispatch. | Wastes resources if not all used. Pool sizing. |

**Recommendation: A (Manager creates and destroys) for simple/medium tiers, C (pre-provisioned pool) for heavy tier.** In the simple tier, creating 2-3 worktrees takes seconds and the main session has full control. In the heavy tier (Oro's daemon model), the Dispatcher maintains a pool of ready worktrees to minimize dispatch latency.

### Decision 7: Two Tiers (Not Three)

Two tiers, not three. All agents always commit. The only difference is coordination.

| Tier | When | Isolation | Commits | Merge | Coordination |
|------|------|-----------|---------|-------|-------------|
| **Claude agents** | Task tool subagents in Claude Code sessions | Worktree per agent | Agent commits on branch | Manager merges ff-only to main | Task notifications |
| **Oro agents** | Dispatcher + Worker binaries (full orchestration) | Worktree per worker + daemon | Agent commits on branch | Dispatcher merges ff-only via merge coordinator | UDS + SQLite |

**Core principle:** Every agent gets a worktree. Every agent commits. No agent merges or pushes. The orchestrator (manager or Dispatcher) handles merge and push.

**Selection:** If you're in a Claude Code session using the Task tool → Claude tier. If you're running the Oro daemon → Oro tier.

---

## Architecture: The Swarm Lifecycle

### Claude Agents (Task Tool Subagents)

```
┌─────────────────────────────────────────────────────┐
│ Main Session (Orchestrator)                         │
│                                                     │
│ 1. SPAWN: Create worktrees + branches               │
│    git worktree add .worktrees/task-1 -b task-1     │
│    git worktree add .worktrees/task-2 -b task-2     │
│                                                     │
│ 2. DISPATCH: Send agents to worktrees               │
│    Task("Work in .worktrees/task-1: ...")            │
│    Task("Work in .worktrees/task-2: ...")            │
│                                                     │
│ 3. WAIT: Monitor completion via notifications        │
│    (agents commit in their worktrees)               │
│                                                     │
│ 4. MERGE: Sequentially merge each branch             │
│    git checkout main                                 │
│    git merge --squash task-1 && git commit           │
│    git merge --squash task-2 && git commit           │
│    (or: rebase + ff-only for linear history)         │
│                                                     │
│ 5. CLEANUP: Remove worktrees                         │
│    git worktree remove .worktrees/task-1             │
│    git worktree remove .worktrees/task-2             │
│                                                     │
│ 6. VERIFY: Run full test suite on main               │
└─────────────────────────────────────────────────────┘
```

**Agent prompt template (medium tier):**

```markdown
You are working in an isolated git worktree at: {worktree_path}
Branch: {branch_name}

## Task
{task_description}

## Rules
- ONLY modify files within your worktree ({worktree_path})
- Commit your work before signaling completion
- Run tests in your worktree: cd {worktree_path} && {test_command}
- Do NOT push to remote
- Do NOT merge to main
- Write completion status: echo "DONE: {task_id}" >> {worktree_path}/.status

## Quality Gate
Before marking complete:
1. Run tests: {test_command}
2. Run lint: {lint_command}
3. Commit all changes with a descriptive message
4. Verify clean working tree: git status
```

### Oro Agents (Dispatcher + Workers)

This is the daemon-based orchestration described in `2026-02-07-orchestrator-ipc-comparison.md`. The lifecycle:

```
Dispatcher (Go daemon)
  │
  ├── Provision worktree pool (.worktrees/worker-{1..5})
  │
  ├── Pull work from priority queue (beads)
  │
  ├── ASSIGN: Send bead to Worker via UDS
  │     Worker Go binary receives ASSIGN
  │     Worker spawns `claude -p` in worktree
  │     Worker sends HEARTBEAT every N seconds
  │
  ├── WORK: Claude agent works in worktree
  │     Agent commits freely
  │     Agent runs tests
  │     Agent signals READY_FOR_REVIEW
  │
  ├── REVIEW: Dispatcher spawns reviewer in same worktree
  │     Reviewer checks spec compliance
  │     APPROVED → proceed to merge
  │     REJECTED → feedback to worker → fix → re-review
  │
  ├── MERGE: Dispatcher acquires merge lock
  │     Rebase onto main
  │     FF-only merge
  │     Conflict? → Worker resolves → retry
  │     Tests fail? → Escalate to Architect
  │
  ├── CLEANUP: Recycle worktree for next bead
  │
  └── RALPH: If context exhaustion detected
        Worker writes .oro/handoff.yaml
        Worker annotates bead
        Dispatcher spawns fresh Worker in same worktree
```

---

## Mapping to the `dispatching-parallel-agents` Skill

The skill should be updated to use worktrees by default:

1. **Manager creates worktrees + branches** before dispatch
2. **Agent prompt template** includes worktree path, commit instructions, quality gate
3. **Agent commits on its branch** (never merges or pushes)
4. **Manager merges ff-only** after all agents complete
5. **Manager cleans up worktrees** after merge and test verification
6. **Conflict handling**: fail-fast + escalate to user

For Oro's full daemon-based orchestration, reference `docs/plans/2026-02-07-orchestrator-ipc-comparison.md`.

---

## Accepted Trade-offs and Risks

### Accepted Trade-offs

| Trade-off | Why Accepted |
|-----------|--------------|
| **Worktree creation overhead (1-3s per agent)** | Negligible compared to agent runtime (minutes). Pre-provisioned pool eliminates this for Oro tier. |
| **Sequential merge serialization** | Necessary to prevent merge races. Merge time is small (~seconds) compared to agent work time. |
| **Manager as merge bottleneck (Claude tier)** | Acceptable for 2-10 agents. Oro tier moves merge to Dispatcher daemon. |
| **Fail-fast conflict resolution (Claude tier)** | Conflicts are rare with worktree isolation. Escalation to user is simpler and safer than auto-resolve. |

### Risks

| Risk | Severity | Mitigation |
|------|----------|------------|
| **Orphan worktrees on agent crash** | LOW | Manager cleanup step. Also: `git worktree prune` as periodic hygiene. |
| **Git lock contention** | LOW | Worktrees share `.git` but lock contention is rare for read-heavy workloads. Write operations (commit, rebase) are serialized by design. |
| **Agent edits files outside its worktree** | MEDIUM | Prompt instructions + path verification. Could add a pre-commit hook that rejects changes outside the worktree root, but prompt-level enforcement is sufficient for medium tier. |
| **Merge conflicts between agent branches** | MEDIUM | Tier-dependent. Simple: shouldn't happen (disjoint files). Medium: manager resolves. Heavy: worker resolves with retry. |
| **Stale worktree state (main moved during agent work)** | LOW | Rebase-before-merge handles this. Agent works on a snapshot; rebase brings it up to date. |
| **Context exhaustion during merge resolution** | MEDIUM | Heavy tier has ralph handoff. Medium tier: if merge is too complex, escalate to user. |

---

## Summary

| Dimension | Claude Agents | Oro Agents |
|-----------|--------------|------------|
| **Runtime** | Task tool subagents | Dispatcher + Worker binaries |
| **Isolation** | Worktree per agent | Worktree per worker + daemon |
| **Who commits** | Agent (on branch) | Agent (on branch) |
| **Who merges** | Manager (ff-only) | Dispatcher (ff-only via merge coordinator) |
| **Coordination** | Task notifications | UDS + SQLite |
| **Conflict resolution** | Fail-fast + escalate | Worker resolves, escalate to Architect |
| **Infrastructure** | Git worktrees | Full Oro stack |
| **Skill reference** | `dispatching-parallel-agents` | Oro Worker model docs |
