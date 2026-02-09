#!/usr/bin/env python3
"""PreToolUse hook: block Bash commands that cd outside the project root.

Agents (architect, manager, dispatcher, subagents doing merges) must always
operate from the project root using absolute paths. Changing directory into
worktrees or other locations causes shell state corruption when combined
with git operations (worktree remove, rebase chains, etc.).

Blocks commands containing `cd <path>` where path resolves outside the
project root. Allows `cd /project/root` (returning home) explicitly.

Input: JSON on stdin with tool_name, tool_input, etc.
Output: JSON with permissionDecision=deny if dangerous, nothing otherwise (passthrough).
"""

import json
import re
import sys
from pathlib import Path

# Match cd commands, including inside chains (&& cd, ; cd, || cd)
# Captures the target path
_CD_RE = re.compile(
    r"(?:^|&&\s*|;\s*|\|\|\s*)cd\s+"
    r"""(?:["']([^"']+)["']|(\S+))"""
)

# Project root — the repo root where .git lives
_PROJECT_ROOT = str(Path(__file__).resolve().parent.parent.parent)


def find_cd_targets(command: str) -> list[str]:
    """Extract all cd target paths from a command string."""
    if not command:
        return []
    return [m.group(1) or m.group(2) for m in _CD_RE.finditer(command)]


def is_outside_root(target: str, root: str) -> bool:
    """Check if a cd target resolves outside the project root.

    Allows cd to the root itself, or any subdirectory of root
    that is NOT inside .worktrees/.
    """
    try:
        resolved = Path(target).resolve()
        root_path = Path(root).resolve()

        # Allow cd to root itself
        if resolved == root_path:
            return False

        # Block if outside root entirely
        if root_path not in resolved.parents:
            return True

        # Block if going into .worktrees/
        rel = resolved.relative_to(root_path)
        return bool(str(rel).startswith(".worktrees"))
    except (OSError, ValueError):
        return True


def build_decision(hook_input: dict) -> dict | None:
    if hook_input.get("tool_name") != "Bash":
        return None

    tool_input = hook_input.get("tool_input")
    if not isinstance(tool_input, dict):
        return None

    command = tool_input.get("command", "")
    if not command or "cd " not in command:
        return None

    targets = find_cd_targets(command)
    for target in targets:
        if is_outside_root(target, _PROJECT_ROOT):
            return {
                "permissionDecision": "deny",
                "message": (
                    f"BLOCKED: `cd {target}` leaves the project root ({_PROJECT_ROOT}). "
                    f"Use absolute paths instead of cd. "
                    f"Never cd into worktrees — it causes shell corruption when combined "
                    f"with git operations. Use absolute paths in all commands."
                ),
            }

    return None


def main() -> None:
    try:
        hook_input = json.loads(sys.stdin.read())
    except (json.JSONDecodeError, EOFError):
        return

    result = build_decision(hook_input)
    if result is None:
        return

    message = result.pop("message", "")
    output = {
        "hookSpecificOutput": {
            "hookEventName": "PreToolUse",
            **result,
        },
        "systemMessage": message,
    }
    json.dump(output, sys.stdout)


if __name__ == "__main__":
    main()
