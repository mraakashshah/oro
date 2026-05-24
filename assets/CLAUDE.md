# Oro Project Instructions

- **Claude compatibility wrapper**: This file exists for Claude-specific installs. Shared Oro guidance lives in `ORO_AGENT.md`.
- **Reference**: Remember, you also have global ~/.claude/CLAUDE.md and any files in ~/.claude/rules/

## Oro Renders

- Use `oro current` to inspect the live task queue and active work state.
- Use `oro handoff --since 4h` when transferring session context; do not create handoff files.
- Use `oro resume <task-id>` to continue a tracked task from live Oro state.

## Skills

Always invoke `using-skills` before any action. This applies in all contexts: main sessions, sub-agents spawned via Task tool, and oro workers. No exceptions.
