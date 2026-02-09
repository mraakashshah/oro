# Cross-Reference Notes: Agent Patterns Across Implementations

## 1. How to Keep Context Small with Agents

### Continuous Claude v3
- **TLDR Stack**: 5-layer code analysis (AST → Call Graph → Control Flow → Data Flow → Program Dep) = 95% token savings (~1,200 vs 23,000 tokens)
- **Hook Interception**: `tldr-read-enforcer` hook intercepts Read tool, returns L1+L2+L3 analysis instead of raw files
- **Handoffs over Context**: YAML handoffs are 5-10x more token-efficient than maintaining conversation context
- **Auto-Compaction Anticipation**: Detect context >80%, auto-create checkpoint before compaction hits
- **Fresh Subagents**: Each subagent starts with clean context; no pollution from previous tasks

### Superpowers
- **Skill Token Efficiency**: Target <200 words for frequently-loaded skills
- **Cross-References**: Use `@skill-name` references instead of duplicating content
- **Progressive Disclosure**: Move heavy reference to separate files, load only when needed
- **Fresh Subagent Per Task**: Eliminates context pollution; each task gets clean slate

### Compound Engineering
- **Progressive Disclosure**: Skills under 500 lines; split details into `references/`, `scripts/`
- **Parallel Research**: Multiple agents work simultaneously, synthesize results (avoids one agent accumulating context)

### OpenClaw
- **Prompt Modes**: "full" for main agent, "minimal" for subagents (reduces context)
- **Session Compaction**: Auto-compacts when context window fills
- **Workspace Isolation**: Per-agent workspaces prevent context bleeding

---

## 2. How to Create and Query Memory

### Continuous Claude v3 (Most Comprehensive)

**Storage**: PostgreSQL + pgvector

**Tables**:
```sql
CREATE TABLE archival_memory (
  id UUID PRIMARY KEY,
  content TEXT,
  embedding vector(1024),  -- BGE embeddings
  metadata JSONB,          -- type, confidence, tags
  created_at TIMESTAMP
);
CREATE INDEX idx_archival_embedding ON archival_memory USING hnsw(embedding);
CREATE INDEX idx_archival_content_fts ON archival_memory USING gin(to_tsvector('english', content));
```

**Learning Types**: WORKING_SOLUTION, FAILED_APPROACH, ARCHITECTURAL_DECISION

**Hybrid Search (RRF)**:
```
Score = 1/(60 + text_rank) + 1/(60 + vector_rank)
```
Combines BM25 (lexical) + vector (semantic) for robustness.

**Commands**:
- `/recall "auth patterns"` - query semantic memory
- `remember "JWT patterns"` - store new learning

**Auto-Extraction Daemon**:
- Session ends → stale heartbeat detected
- Daemon spawns headless Claude (Sonnet)
- Extracts learnings from thinking blocks
- Stores with BGE embeddings

### OpenClaw
- **Memory Tools**: `memory_search` + `memory_get` in system prompt
- **Backend**: LanceDB (vector search) or embedded helpers
- **Per-Agent Memory**: Isolated to agent's workspace

### Compound Engineering
- **Knowledge Store**: `docs/solutions/` with YAML frontmatter for searchability
- **Learnings Researcher**: Agent queries institutional knowledge during planning
- **Compound Workflow**: `/workflows:compound` documents solved problems while context is fresh

---

## 3. Workflows and Skills Used/Suggested

### Continuous Claude v3 (109 Skills, 32 Agents)

**Meta-Workflows**:
| Command | Chain |
|---------|-------|
| `/build greenfield` | discovery → plan → validate → implement → commit → PR |
| `/build brownfield` | onboard → research → plan → validate → implement |
| `/fix bug` | sleuth → premortem → kraken → test → commit |
| `/tdd` | plan → arbiter (tests) → kraken (code) → arbiter (verify) |
| `/refactor` | phoenix → warden → kraken → judge |
| `/explore` | scout (quick/deep/architecture) |

**Skill Activation**: Natural language triggers via `skill-rules.json` with regex patterns:

- Keywords: "fix", "debug", "build"
- Intent patterns: `"fix.*?(bug|error|issue)"`
- Priority: CRITICAL, RECOMMENDED, SUGGESTED, OPTIONAL

### Superpowers (15+ Skills)

**Process Skills**: `brainstorming`, `writing-plans`, `executing-plans`, `using-git-worktrees`
**Implementation Skills**: `test-driven-development`, `subagent-driven-development`
**Quality Skills**: `systematic-debugging`, `verification-before-completion`

**Skill Triggering**: Description field contains CONDITIONS only (not workflow summary) for Claude search optimization.

### Compound Engineering (24 Commands, 15 Skills)

**Core Workflow Commands**:
- `/workflows:plan` - strategic planning with parallel research
- `/workflows:work` - systematic execution
- `/workflows:review` - multi-agent code review (13+ parallel agents)
- `/workflows:compound` - knowledge codification

**Agent Categories** (27 total):

- Review (14), Research (4), Design (3), Workflow (5), Docs (1)

### OpenClaw (50+ Skills)

**Skill-Based Guidance**: Skills are instructions, not tools. Agent chooses skill, reads it, follows recipe.

**Categories**: Integration, Coding, Media, Utilities

---

## 4. How Propulsion is Kept (Avoiding User Input)

### Continuous Claude v3

**Continuity Loop**:
1. Session start loads handoff + learnings automatically
2. Skill activation hook suggests relevant skills without user asking
3. Pre-compact hook auto-saves state before context fills
4. Session end daemon extracts learnings automatically

**Staggered Warnings** (proactive):
| Context % | Action |
|-----------|--------|
| 70-79% | Notice |
| 80-89% | Warning |
| 90%+ | Force handoff |

**Background Processes**: Hooks run in bash/TypeScript outside Claude's context.

### Superpowers

**Subagent-Driven Development**:
```
For each task:
1. Dispatch implementer subagent
2. Answer questions upfront (before implementation)
3. Implementer implements, tests, self-reviews, commits
4. Dispatch spec-reviewer (loop until pass)
5. Dispatch code-quality-reviewer (loop until pass)
6. Mark complete → next task
```

**Batched Execution**: Execute 3 tasks, then checkpoint for human review (only pause point).

### Loom

**Event-Driven State Machine**:
```
CallingLlm → ProcessingLlmResponse → ExecutingTools → PostToolsHook → CallingLlm (loop)
```

**Post-Tools Hook**: Auto-runs infrastructure operations (auto-commit), then continues to next LLM call without user intervention.

**Background Job Scheduler**:
```rust
loop {
    tokio::select! {
        _ = interval_timer.tick() => execute_with_retry(&job).await,
        _ = shutdown_rx.recv() => break,
    }
}
```

### Compound Engineering

**Parallel Agent Dispatch**: Multiple research agents run simultaneously during planning, synthesize results, continue to next phase.

**Progressive Questions**: AskUserQuestion only when truly needed; otherwise agents proceed autonomously.

---

## 5. If tmux is Used, and Why

### Findings

**None of the four implementations explicitly use tmux** for agent coordination.

**Alternatives Used**:

| System | Coordination Method |
|--------|---------------------|
| Continuous Claude v3 | PostgreSQL sessions table + file claims for cross-terminal awareness |
| Loom | Tokio async tasks + broadcast channels |
| OpenClaw | Single gateway daemon (WebSocket) managing all connections |
| Superpowers | Git worktrees for isolation |

**Why Not tmux**:
- **Database coordination** is more robust for multi-agent state sharing
- **WebSocket/async** allows event-driven communication without polling
- **Git worktrees** provide filesystem isolation without terminal multiplexing
- **Hooks** intercept at tool level, not terminal level

**When tmux Would Make Sense**:

- Long-running processes that need to survive terminal disconnect
- Visual monitoring of multiple agents in split panes
- Manual debugging of agent interactions

---

## 6. How Agent Orchestration is Managed with Tasks

### Continuous Claude v3

**Agent Spawning via Task Tool**:
```python
# Parallel research
Task(subagent_type="scout", prompt="Find auth patterns")
Task(subagent_type="oracle", prompt="Research OAuth best practices")
# Both run in parallel

# Hierarchical (maestro pattern)
Task(subagent_type="maestro", prompt="""
Coordinate these agents:
- architect: design
- kraken: implement
- arbiter: validate
""")
```

**Agent Categories**:
- Orchestrators (maestro, kraken)
- Planners (architect, phoenix)
- Explorers (scout, oracle)
- Implementers (kraken, spark)
- Reviewers (critic, judge)

### Superpowers

**Subagent-Driven Task Execution**:
1. Plan broken into 2-5 minute tasks
2. Fresh subagent dispatched per task
3. Two-stage review: spec-reviewer → code-quality-reviewer
4. Review loops until pass
5. Next task dispatched

**Parallel Agent Dispatch** (for independent investigations):
- Identify 2+ independent problem domains
- Create focused agent tasks per domain
- Dispatch all in parallel
- Review and integrate results

### Compound Engineering

**Parallel Research Agents**:
```
Task repo-research-analyst(input)
Task learnings-researcher(input)
Task best-practices-researcher(input)  ← All parallel
Task framework-docs-researcher(input)
```

**Synthesis Pattern**:
1. Consolidate findings from all agents
2. De-duplicate and prioritize
3. Call out conflicts
4. Produce unified summary

**Review Orchestration**: 13+ agents in parallel for code review, each produces isolated findings → synthesized.

### Loom

**Background Job Scheduler**:
```rust
async fn run_periodic_job(job: Arc<dyn Job>, interval: Duration) {
    let mut interval_timer = tokio::time::interval(interval);
    loop {
        tokio::select! {
            _ = interval_timer.tick() => execute_with_retry(&job).await,
            _ = shutdown_rx.recv() => break,
        }
    }
}
```

**Built-in Jobs**:
- Weaver Cleanup (30 min)
- Token Refresh (5 min)
- Session Cleanup (1 hour)
- Missed Run Detection (1 min)

### OpenClaw

**Subagent Spawning**:
- `openclaw_tools.sessions_spawn` for child agents
- Child agents inherit parent's workspace unless overridden
- Session keys format: `agent:<id>:<sessionName>`

---

## Summary Table

| Topic | Best Implementation | Key Pattern |
|-------|---------------------|-------------|
| Context Size | Continuous Claude v3 | TLDR stack (95% savings) + handoffs |
| Memory | Continuous Claude v3 | PostgreSQL + pgvector + hybrid RRF search |
| Workflows/Skills | Compound Engineering | 27 agents + 24 commands + `/workflows:*` |
| Propulsion | Superpowers | Subagent-driven with two-stage review loops |
| Terminal Coordination | Continuous Claude v3 | DB-based (not tmux) |
| Task Orchestration | Compound Engineering | Parallel dispatch + synthesis |
