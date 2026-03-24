#!/usr/bin/env python3
"""PreToolUse hook: block Bash commands that cd outside the project root or modify worktrees.

Agents (architect, manager, dispatcher, subagents doing merges) must always
operate from the project root using absolute paths. Changing directory into
worktrees or other locations causes shell state corruption when combined
with git operations (worktree remove, rebase chains, etc.).

Blocks:
  - `cd <path>` where path resolves outside the project root
  - `git worktree remove` and `git worktree add` (workers must not alter worktree structure)

Allows:
  - `cd /project/root` (returning home)
  - `git worktree list` and bare `git worktree` (read-only)

Input: JSON on stdin with tool_name, tool_input, etc.
Output: JSON with permissionDecision=deny if dangerous, nothing otherwise (passthrough).
"""

import json
import os
import re
import subprocess
import sys
from pathlib import Path

# Match cd commands, including inside chains (&& cd, ; cd, || cd)
# and after newlines in multiline commands.
# Captures the target path
_CD_RE = re.compile(
    r"(?:^|\n\s*|&&\s*|;\s*|\|\|\s*)cd\s+"
    r"""(?:["']([^"']+)["']|(\S+))"""
)

# Match `git worktree <subcommand>` at command boundaries only.
# Uses same boundary markers as _CD_RE so it won't fire on commit messages or
# heredoc content that happens to contain the text "git worktree remove".
_WORKTREE_RE = re.compile(r"(?:^|&&\s*|;\s*|\|\|\s*)git\s+worktree\s+(\w+)")

# Subcommands that mutate the worktree list — workers must never run these
_BLOCKED_WORKTREE_SUBCMDS = frozenset({"remove", "add", "prune", "lock", "unlock", "repair", "move"})


def _git_repo_root() -> str:
    """Get the real git repo root, even when CWD is inside a worktree.

    git rev-parse --show-toplevel returns the worktree root when CWD is
    inside a worktree, which breaks cd-to-project-root detection. We use
    --git-common-dir to find the shared .git directory, then resolve the
    actual repo root from that.
    """
    try:
        # --git-common-dir returns the shared .git dir (e.g. /repo/.git)
        # even from inside a worktree, unlike --show-toplevel.
        result = subprocess.run(
            ["git", "rev-parse", "--git-common-dir"],
            capture_output=True,
            text=True,
            check=True,
        )
        git_common = Path(result.stdout.strip()).resolve()
        # .git dir is at repo_root/.git → parent is the real root
        if git_common.name == ".git":
            return str(git_common.parent)
        # Fallback: if structure is unexpected, use --show-toplevel
        result = subprocess.run(
            ["git", "rev-parse", "--show-toplevel"],
            capture_output=True,
            text=True,
            check=True,
        )
        return result.stdout.strip()
    except (subprocess.CalledProcessError, FileNotFoundError):
        return str(Path.cwd())


# Project root — the repo root where .git lives
_PROJECT_ROOT = _git_repo_root()


def check_git_command(command: str) -> dict | None:
    """Detect and block dangerous git worktree subcommands.

    Blocks 'git worktree remove', 'git worktree add', and other mutating
    subcommands when ORO_ROLE is set (worker, architect, manager). The main
    session (no ORO_ROLE) needs worktree commands for cleanup and maintenance.

    Allows bare 'git worktree' and 'git worktree list' (read-only) regardless.
    """
    if not command:
        return None
    # Only block worktree mutations for swarm roles (worker, architect, manager).
    # The main session (ORO_ROLE unset) needs worktree commands for cleanup.
    if not os.environ.get("ORO_ROLE"):
        return None
    match = _WORKTREE_RE.search(command)
    if match is None:
        return None
    subcmd = match.group(1)
    if subcmd not in _BLOCKED_WORKTREE_SUBCMDS:
        return None
    return {
        "permissionDecision": "deny",
        "message": (
            f"BLOCKED: `git worktree {subcmd}` is not allowed in workers. "
            f"Workers must not modify the worktree structure. "
            f"Only read-only git worktree commands (e.g. git worktree list) are permitted."
        ),
    }


def find_cd_targets(command: str) -> list[str]:
    """Extract all cd target paths from a command string."""
    if not command:
        return []
    return [m.group(1) or m.group(2) for m in _CD_RE.finditer(command)]


def is_outside_root(target: str, root: str) -> bool:
    """Check if a cd target is anything other than the project root.

    Only allows cd to the exact project root itself. ALL other cd
    commands are blocked — changing CWD to any subdirectory breaks
    hook scripts that use relative paths to .claude/hooks/.
    """
    try:
        resolved = Path(target).resolve()
        root_path = Path(root).resolve()

        # Only allow cd to root itself — block everything else
        return resolved != root_path
    except (OSError, ValueError):
        return True


def build_decision(hook_input: dict) -> dict | None:
    if hook_input.get("tool_name") != "Bash":
        return None

    tool_input = hook_input.get("tool_input")
    if not isinstance(tool_input, dict):
        return None

    command = tool_input.get("command", "")
    if not command:
        return None

    result = check_git_command(command)
    if result is not None:
        return result

    if "cd " not in command:
        return None

    targets = find_cd_targets(command)
    for target in targets:
        if is_outside_root(target, _PROJECT_ROOT):
            return {
                "permissionDecision": "deny",
                "message": (
                    f"BLOCKED: `cd {target}` changes CWD away from project root ({_PROJECT_ROOT}). "
                    f"ALL cd commands are blocked except `cd {_PROJECT_ROOT}`. "
                    f"Changing CWD breaks hook scripts that use relative paths. "
                    f"Use absolute paths or tool-specific path parameters instead."
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
