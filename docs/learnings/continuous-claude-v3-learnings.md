# Continuous Claude v3 Learnings

## Overview

Persistent, learning, multi-agent development environment that transforms Claude Code into an autonomous system capable of:
- Maintaining state across sessions via handoffs
- Delegating to 32+ specialized agents
- Token-efficient code analysis (95% savings via TLDR)
- Semantic memory with PostgreSQL+pgvector
- Natural language skill activation (109 skills)
- Multi-terminal coordination

## The Continuity Loop (4 Phases)

```
PHASE 1: SESSION START
├─ Load continuity ledger
├─ Restore handoff context
├─ Recall relevant learnings
└─ Warm TLDR cache

PHASE 2: WORKING
├─ Track file claims (cross-terminal locking)
├─ Auto-index on edits
├─ Detect skill activation
└─ Create handoffs on demand

PHASE 3: PRE-COMPACT (Auto-Save)
├─ Detect when context >80%
├─ Auto-create continuity checkpoint
├─ Force handoff if >90%
└─ Re-index TLDR if needed

PHASE 4: SESSION END
├─ Detect stale heartbeat (>5 min)
├─ Daemon spawns headless Claude
├─ Extract learnings from thinking blocks
└─ Store in archival_memory
```

**Key Insight:** Don't fight compaction—anticipate it. Auto-handoffs preserve state before compaction happens.

## Handoff System

**Location:** `thoughts/shared/handoffs/<session-name>/YYYY-MM-DD_HH-MM_*.yaml`

```yaml
session_name: feature-auth-123
date: 2026-01-08T15:26:01+0000
status: in_progress

## Goal
What this session aims to accomplish

## Now
What the next session should do first

## Ledger
- [x] Completed tasks
- [ ] In progress
```

**Why YAML:** 5-10x more token-efficient for parsing.

## Meta-Skills (Workflow Routers)

| Meta-Skill | Chain |
|------------|-------|
| `/build greenfield` | discovery → plan → validate → implement → commit → PR |
| `/build brownfield` | onboard → research → plan → validate → implement |
| `/fix bug` | sleuth → premortem → kraken → test → commit |
| `/tdd` | plan → arbiter (tests) → kraken (code) → arbiter (verify) |
| `/refactor` | phoenix → warden → kraken → judge |
| `/explore` | scout (quick/deep/architecture) |

## Skill Activation (Natural Language)

Users describe intent naturally → hook suggests relevant skills:

```
User: "Fix the login bug"
↓
System suggests: /fix workflow, debug-agent, scout
↓
Claude decides which to use
```

**How:** `skill-rules.json` contains regex patterns for intent matching with priority levels (CRITICAL, RECOMMENDED, SUGGESTED).

## The Hook Layer

**SessionStart:**
- `session-start-continuity` - restore ledger, load handoff
- `session-register` - register in coordination layer
- `memory-awareness` - surface relevant learnings

**PreToolUse:**
- `tldr-read-enforcer` - return L1+L2+L3 instead of raw files
- `smart-search-router` - route to AST-grep when appropriate
- `file-claims` - prevent cross-terminal conflicts

**PostToolUse:**
- `post-edit-diagnostics` - pyright + ruff after edits
- `compiler-in-the-loop` - validate TypeScript compiles

**SessionEnd:**
- `session-end-cleanup` - extract learnings

**Key:** Hooks run in bash/TypeScript, not in Claude's context (don't burn tokens).

## TLDR Code Analysis Stack (95% Savings)

```
L1: AST (~500 tokens)     - Functions, classes, signatures
L2: Call Graph (+440)     - Cross-file dependencies
L3: Control Flow (+110)   - Cyclomatic complexity
L4: Data Flow (+130)      - Variable origins/uses
L5: Program Dep (+150)    - Control + data dependencies
```

**Total:** ~1,200 tokens vs 23,000 raw = 95% savings

## Memory System (PostgreSQL + pgvector)

**4 Tables:**
1. `sessions` - cross-terminal awareness
2. `file_claims` - cross-terminal file locking
3. `archival_memory` - long-term learnings with embeddings
4. `handoffs` - session handoffs with semantic search

**Hybrid Search (RRF):**
```
Score = 1/(60 + text_rank) + 1/(60 + vector_rank)
```
Combines BM25 + vector similarity for robustness.

## Agent Categories (32 Total)

- **Orchestrators:** maestro, kraken
- **Planners:** architect, phoenix
- **Explorers:** scout, oracle, pathfinder
- **Implementers:** kraken, spark
- **Debuggers:** sleuth, debug-agent, profiler
- **Reviewers:** critic, judge, surveyor
- **Specialized:** aegis (security), herald (release)

## Staggered Warnings

| Context % | Action |
|-----------|--------|
| 70-79% | Notice: "Consider handoff" |
| 80-89% | Warning: "Recommend `/create_handoff`" |
| 90%+ | CRITICAL: Blocks until handoff created |

## Key Commands

```bash
/workflow           # Goal-based router
/create_handoff     # Save state for next session
/resume_handoff     # Load latest handoff
/recall "query"     # Search learnings
remember "learning" # Store learning
```

## Opinionated Choices

| Choice | Why |
|--------|-----|
| Handoffs > Context | YAML is token-efficient; recover from compaction |
| Hooks > Plugins | Hooks don't burn tokens |
| Learning > Accumulation | Extract learnings; start fresh with context |
| Agents > Single Claude | Delegate to specialists in parallel |
| TLDR > Raw Files | 95% token savings |
| Hybrid Search > Vector Only | Robust to typos, faster cold starts |

## Key Files

```
.claude/
├── agents/            # 32 specialized agents
├── hooks/src/         # TypeScript hook sources
├── skills/            # 109 modular capabilities
└── settings.json      # Hook registration
opc/
├── scripts/core/
│   ├── recall_learnings.py
│   ├── store_learning.py
│   └── memory_daemon.py
└── docker/
    └── init-schema.sql
```
