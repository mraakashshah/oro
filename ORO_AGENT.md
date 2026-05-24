# Oro Project Instructions

## Oro Renders

- Use `oro current` to inspect the live task queue and active work state.
- Use `oro handoff --since 4h` when transferring session context; handoffs are renders, not stored artifacts.
- Use `oro resume <task-id>` to continue a tracked task from live Oro state.

## Skills

Always invoke `using-skills` before any action. This applies in all contexts: main sessions, sub-agents spawned via Task tool, and oro workers. No exceptions.
