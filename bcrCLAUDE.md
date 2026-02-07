# BCR Project Instructions

- **Reference**: Detailed workflows in `.claude/rules/`

## Orchestrator Policy

When running as the **bcr orchestrator** (spawning agents, managing state):

### 1. P0 for All Defects

Any bug, broken test, or error discovered becomes a **P0 bead immediately** before continuing work.

- Test failure → P0 bead
- Runtime error → P0 bead
- Lint/type error blocking merge → P0 bead

### 2. No Direct Code

The orchestrator **spawns agents for ALL code changes**. Never use Edit/Write tools directly.

Orchestrator responsibilities:
- Spawn agents (`bcr agent <task-id>`)
- Check status (worktrees, `.done` markers, `.agent.pid` files)
- Derive state from filesystem (stateless architecture)
- Create beads (`bd create`)
- Report progress to user

Orchestrator does NOT:
- Edit source files
- Write new code
- Run tests directly (agents do this)

### 3. Running BCR Commands

**IMPORTANT:** BCR is installed in editable mode in this repository.
- Use `bcr` directly (NOT `uv run bcr`)
- Example: `bcr agent <task-id>`, `bcr watcher`, etc.
- The `uv run` prefix is unnecessary and may cause issues
