#!/usr/bin/env python3
"""PreToolUse hook: warn when Task tool dispatches agents without worktree isolation.

Checks if the Task tool prompt includes worktree path and commit instructions.
Emits a warning (not a block) if missing.
"""

import json
import sys


def main():
    try:
        data = json.load(sys.stdin)
    except (json.JSONDecodeError, EOFError):
        return

    tool_name = data.get("tool_name", "")
    if tool_name != "Task":
        return

    tool_input = data.get("tool_input", {})
    prompt = tool_input.get("prompt", "")
    run_in_background = tool_input.get("run_in_background", False)

    if not run_in_background:
        return

    prompt_lower = prompt.lower()
    has_worktree = ".worktrees/" in prompt or "worktree" in prompt_lower
    has_commit = "commit" in prompt_lower

    missing = []
    if not has_worktree:
        missing.append("worktree path (.worktrees/<name>)")
    if not has_commit:
        missing.append("commit instructions")

    if missing:
        warning = (
            "Agent prompt is missing: "
            + ", ".join(missing)
            + ". Per swarm spec, every background agent should get a worktree "
            "and commit on its branch. See dispatching-parallel-agents skill."
        )
        json.dump({"additionalContext": warning}, sys.stdout)


if __name__ == "__main__":
    main()
