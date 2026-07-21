---
name: brainstorming
description: Use when doing creative product, feature, component, functionality, or behavior design work
---

# Brainstorming Ideas Into Designs

Develop an evidence-based design through focused dialogue. Match the depth of the process to the decision's scope and reversibility.

## Workflow

### 1. Research Proportionally

Before proposing a design:

- Read the affected code, current project state, and relevant prior plans.
- Search `docs/decisions&discoveries.md`; use `oro recall` when institutional memory could help.
- Research external prior art only when the domain has relevant established solutions or current facts.
- Cite the sources and constraints that materially shape the proposal.

A small internal change may need two focused files. A new architecture may need broader internal and external research.

### 2. Frame the Decision

- Split multiple independent subsystems into separate design cycles.
- Resolve discoverable questions from code and documentation.
- Ask the user one question at a time only for material unknowns.
- State the purpose, constraints, success criteria, and load-bearing assumptions.

When asking, give your recommendation, confidence, and 2-3 concrete options with trade-offs.

### 3. Compare Approaches

- Propose 2-3 credible approaches.
- Lead with the recommended option and explain which constraints it satisfies.
- Prefer proven, reversible, low-blast-radius choices.
- Remove features and abstractions that do not earn their cost.

### 4. Premortem Load-Bearing Choices

Apply the premortem taxonomy privately to architectural or irreversible decisions:

- **Tiger** — a verified threat requiring mitigation.
- **Paper Tiger** — a concern already handled by an existing safeguard.
- **Elephant** — an important concern the design is avoiding.

Verify risks against the code and current mitigations. Incorporate material mitigations into the design; do not turn minor choices into user gates.

### 5. Present the Design

Present the design in digestible sections when useful. Cover architecture, components, data flow, error handling, testing, rollback, and explicit non-goals. Pause only when a material user decision remains.

Write `docs/plans/YYYY-MM-DD-<topic>-design.md` when the invoking workflow requires a design artifact. Include accepted risks and mitigations.

## Ownership Boundary

The invoking workflow owns adversarial review, task decomposition, commits, and execution. Brainstorming returns the chosen design plus unresolved material decisions; it does not spawn reviewers, create task graphs, commit files, or start implementation unless explicitly asked.

## Red Flags

- Proposing without evidence from relevant files or sources
- Asking questions answerable from the codebase
- Treating every minor choice as a user decision
- Choosing an approach without checking its load-bearing risks
- Continuing after an unresolved material decision
