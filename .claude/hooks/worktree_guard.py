#!/usr/bin/env python3
"""PreToolUse hook: block git worktree remove when cwd is inside the worktree.

Prevents the known Claude Code bug where removing a worktree that contains
the Bash tool's cwd kills the shell permanently (every subsequent command
returns exit code 1 with no output). See: github.com/anthropics/claude-code/issues/9190

Intercepts Bash commands containing `git worktree remove`. If the current
working directory is inside the worktree being removed, blocks the command
and instructs the agent to cd to the project root first.

Input: JSON on stdin with tool_name, tool_input, etc.
Output: JSON with decision=block if dangerous, nothing otherwise (passthrough).
"""

import json
import os
import re
import sys
from pathlib import Path

# Pattern: git worktree remove [--force|-f] <path>
# Captures the path argument, handling optional --force/-f flag
# Works inside chained commands (&&, ;, ||)
_REMOVE_RE = re.compile(
    r"git\s+worktree\s+remove\s+"
    r"(?:--force\s+|-f\s+)?"
    r"""(?:["']([^"']+)["']|(\S+))"""
)


def parse_worktree_remove(command: str) -> str | None:
    """Extract the worktree path from a git worktree remove command.

    Returns the path argument, or None if this isn't a worktree remove command.
    """
    if not command:
        return None
    m = _REMOVE_RE.search(command)
    if not m:
        return None
    return (m.group(1) or m.group(2)).rstrip("/")


def is_cwd_inside(cwd: str, worktree_path: str) -> bool:
    """Check if cwd is equal to or a subdirectory of worktree_path.

    Uses Path.resolve() for consistent comparison. Handles the case where
    a path like /foo/bar-extra is NOT inside /foo/bar (prefix check trap).
    """
    try:
        cwd_resolved = Path(cwd).resolve()
        wt_resolved = Path(worktree_path).resolve()
        return cwd_resolved == wt_resolved or wt_resolved in cwd_resolved.parents
    except (OSError, ValueError):
        return False


def _get_cwd(hook_input: dict) -> str:
    """Get the Bash tool's working directory from hook input.

    Uses the 'cwd' field from the hook input (the Bash tool's actual CWD),
    NOT os.getcwd() which returns the hook process's CWD (always project root).
    Falls back to os.getcwd() if 'cwd' is missing (shouldn't happen).
    """
    return hook_input.get("cwd", os.getcwd())


def _resolve_worktree_path(path: str, cwd: str) -> str:
    """Resolve a possibly-relative worktree path to absolute.

    Uses the provided cwd for relative path resolution, not the hook's cwd.
    """
    p = Path(path)
    if not p.is_absolute():
        p = Path(cwd) / p
    return str(p.resolve())


def build_decision(hook_input: dict) -> dict | None:
    """Decide whether to block a worktree remove command.

    Returns a dict with {"decision": "block", "reason": "..."} if the command
    is dangerous, or None to passthrough (approve).
    """
    if hook_input.get("tool_name") != "Bash":
        return None

    tool_input = hook_input.get("tool_input")
    if not isinstance(tool_input, dict):
        return None

    command = tool_input.get("command", "")
    if not command:
        return None

    wt_path = parse_worktree_remove(command)
    if wt_path is None:
        return None

    cwd = _get_cwd(hook_input)
    resolved_wt = _resolve_worktree_path(wt_path, cwd)

    if not is_cwd_inside(cwd, resolved_wt):
        return None

    project_root = str(Path(__file__).resolve().parent.parent.parent)
    return {
        "decision": "block",
        "reason": (
            f"BLOCKED: Your shell cwd ({cwd}) is inside the worktree you're removing ({resolved_wt}). "
            f"This will permanently kill the Bash tool (Claude Code bug #9190). "
            f"Run `cd {project_root}` first to move to the project root, "
            f"then retry the worktree remove command."
        ),
    }


def main() -> None:
    try:
        hook_input = json.loads(sys.stdin.read())
    except (json.JSONDecodeError, EOFError):
        return

    result = build_decision(hook_input)
    if result is None:
        return

    output = {
        "hookSpecificOutput": {
            "hookEventName": "PreToolUse",
            **result,
        }
    }
    json.dump(output, sys.stdout)


if __name__ == "__main__":
    main()
