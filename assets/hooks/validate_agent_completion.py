#!/usr/bin/env python3
"""PostToolUse hook: validate agent completion (SubagentStop).

When a Task-tool agent finishes, checks:
1. If a worktree path was used, verify it exists
2. Check for uncommitted changes in that worktree
3. Check that the branch was pushed to remote
4. Check that a bead was closed (bd close in output)

Fails open: missing data -> approve. Warnings via additionalContext, not blocks.

Input: JSON on stdin with tool_name, tool_input, tool_output fields.
Output: JSON with hookSpecificOutput.additionalContext on warnings, nothing otherwise.
"""

import json
import re
import subprocess
import sys
from pathlib import Path

# Pattern: /some/path/.worktrees/name (optionally followed by / or whitespace/EOL)
_WORKTREE_RE = re.compile(r"(/\S+/\.worktrees/[\w.-]+)/?")

# Pattern: bd close <id> (with flexible whitespace)
_BD_CLOSE_RE = re.compile(r"bd\s+close\s+\S+")


def extract_worktree_path(agent_output: str) -> str | None:
    """Extract the first .worktrees/<name> path from agent output.

    Returns the path without trailing slash, or None if not found.
    """
    if not agent_output:
        return None
    m = _WORKTREE_RE.search(agent_output)
    if not m:
        return None
    return m.group(1).rstrip("/")


def check_dirty_worktree(cwd: str) -> str | None:
    """Check for uncommitted changes via git status --porcelain.

    Returns a warning string if dirty, None if clean.
    Returns a warning if the path does not exist.
    """
    if not Path(cwd).exists():
        return f"Worktree path does not exist: {cwd}"
    try:
        result = subprocess.run(
            ["git", "status", "--porcelain"],
            cwd=cwd,
            capture_output=True,
            text=True,
            timeout=10,
        )
        if result.returncode != 0:
            return None  # fail open
        output = result.stdout.strip()
        if output:
            return f"Uncommitted changes in worktree {cwd}:\n{output}"
        return None
    except (subprocess.TimeoutExpired, OSError):
        return None  # fail open


def check_unpushed(cwd: str) -> str | None:
    """Check for unpushed commits via git log @{u}.. --oneline.

    Returns a warning string if unpushed commits exist, None if clean.
    Fails open if no upstream or path issues.
    """
    if not Path(cwd).exists():
        return None  # fail open
    try:
        result = subprocess.run(
            ["git", "log", "@{u}..", "--oneline"],
            cwd=cwd,
            capture_output=True,
            text=True,
            timeout=10,
        )
        if result.returncode != 0:
            return None  # fail open (no upstream configured, etc.)
        output = result.stdout.strip()
        if output:
            return f"Unpushed commits in worktree {cwd}:\n{output}"
        return None
    except (subprocess.TimeoutExpired, OSError):
        return None  # fail open


def check_bead_closed(agent_output: str) -> str | None:
    """Check that the agent ran 'bd close' in its output.

    Returns a warning string if not found, None if found.
    """
    if _BD_CLOSE_RE.search(agent_output):
        return None
    return "Agent did not run `bd close` to close its bead. Ensure the bead is closed."


def build_warnings(agent_output: str) -> list[str]:
    """Run all validation checks and return a list of warning strings.

    If no worktree path is detected, only checks for bd close when
    the output contains a worktree reference. Fails open otherwise.
    """
    warnings: list[str] = []

    worktree = extract_worktree_path(agent_output)
    if not worktree:
        # No worktree detected -> fail open, nothing to validate
        return warnings

    # Worktree was detected: run all checks
    dirty = check_dirty_worktree(worktree)
    if dirty:
        warnings.append(dirty)

    unpushed = check_unpushed(worktree)
    if unpushed:
        warnings.append(unpushed)

    bead = check_bead_closed(agent_output)
    if bead:
        warnings.append(bead)

    return warnings


def main() -> None:
    try:
        hook_input = json.loads(sys.stdin.read())
    except (json.JSONDecodeError, EOFError):
        return

    if hook_input.get("tool_name") != "Task":
        return

    # Combine prompt and output to search for worktree paths and bd close
    tool_input = hook_input.get("tool_input", {})
    tool_output = hook_input.get("tool_output", "")
    prompt = tool_input.get("prompt", "") if isinstance(tool_input, dict) else ""
    agent_output = f"{prompt}\n{tool_output}"

    warnings = build_warnings(agent_output)
    if not warnings:
        return

    context = "SubagentStop validation warnings:\n" + "\n".join(f"- {w}" for w in warnings)

    output = {
        "hookSpecificOutput": {
            "hookEventName": "PostToolUse",
            "additionalContext": context,
        }
    }
    json.dump(output, sys.stdout)


if __name__ == "__main__":
    main()
