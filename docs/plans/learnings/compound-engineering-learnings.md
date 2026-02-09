# Compound Engineering Plugin Learnings

## Core Philosophy: Compounding Engineering

Each unit of engineering work should make subsequent units easier, not harder.

**Traditional:** Plan → Code → Review (20% time on non-execution)
**Compound:** Plan → Code → Review → Document (80% time on planning, review, knowledge capture)

## The Compound Loop

```
Plan → Work → Review → Compound → Repeat
```

### 1. `/workflows:plan` - Strategic Planning
- Transforms ideas into structured markdown plans
- Runs parallel research agents:
  - `repo-research-analyst` - project conventions
  - `learnings-researcher` - documented solutions
  - `best-practices-researcher` - external research
  - `framework-docs-researcher` - framework guidance
- Produces dated plan files in `docs/plans/`

### 2. `/workflows:work` - Systematic Execution
- Creates breakable todo lists with dependencies
- Supports parallel development with git worktrees
- Includes task execution loop with pattern matching

### 3. `/workflows:review` - Multi-Agent Code Review
- 13+ agents running in parallel:
  - Framework reviewers (Rails, Python, TypeScript)
  - Domain reviewers (Security, Performance, Architecture)
  - Specialized reviewers (Frontend races, Agent-native)
- Synthesizes findings into consolidated review

### 4. `/workflows:compound` - Knowledge Codification
- Documents recently solved problems
- Uses parallel subagents (Context Analyzer, Solution Extractor, etc.)
- Creates YAML frontmatter for searchability
- Stores in `docs/solutions/` for future reference

## Agent Architecture (27 Total)

**Review Agents (14):**
- Language-specific: `kieran-rails-reviewer`, `kieran-python-reviewer`, `kieran-typescript-reviewer`
- Domain: `security-sentinel`, `performance-oracle`, `architecture-strategist`
- Specialized: `agent-native-reviewer`, `data-integrity-guardian`

**Research Agents (4):**
- Invoked in parallel during planning
- Combine local context + external knowledge

**Design Agents (3):**
- Figma sync, design implementation review

## Agent-Native Architecture Patterns

### Parity Principle
Whatever users can do through UI, agents must achieve through tools.

### Granularity Principle
Prefer atomic primitives (read, write, move, list, bash). Features are outcomes, not functions.

### Composability Principle
Atomic tools + parity = new features via prompts only.

### Common Anti-Patterns
- **Context Starvation** - Agent doesn't know what resources exist
- **Orphan Features** - UI action with no agent equivalent
- **Workflow Tools** - Tools that encode business logic instead of primitives

## Multi-Agent Parallel Execution

```
Task repo-research-analyst(input)
Task learnings-researcher(input)
Task best-practices-researcher(input)  ← All run in parallel
Task framework-docs-researcher(input)
```

**Synthesis Pattern:**
1. Consolidate findings from all agents
2. De-duplicate and prioritize
3. Call out conflicts
4. Produce unified summary

## Knowledge Management

**docs/plans/** - Living documents with checkboxes (protected from cleanup)
**docs/solutions/** - YAML frontmatter, categorized, searchable
**docs/decisions-and-discoveries/** - Architectural pivots and learnings

## Skill Structure

```markdown
---
name: skill-name
description: What it does (third person)
---

# Skill Title
## Quick Start
## Instructions
## Examples
```

**Key Practices:**
- Under 500 lines (progressive disclosure)
- Split details into `references/`, `scripts/`
- Naming: gerund form (`processing-pdfs`)

## Directory Structure

```
plugins/compound-engineering/
├── agents/
│   ├── review/        # 14 agents
│   ├── research/      # 4 agents
│   ├── design/        # 3 agents
│   └── workflow/      # 5 agents
├── commands/
│   └── workflows/     # Core workflow commands
├── skills/            # 15 skills
└── .claude-plugin/
    └── plugin.json
```

## Key Learnings

1. **Count Accuracy** - Component counts must match in plugin.json, marketplace.json, README.md
2. **Versioning Discipline** - Every change triggers version bump + docs update
3. **Protection Directive** - Certain files protected from review suggestions
4. **Progressive Disclosure** - Skills guide users without overwhelming
