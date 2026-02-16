#!/usr/bin/env python3
"""PreToolUse hook: block git rebase on branches checked out in worktrees.

Git rebase fails if the target branch is checked out in any worktree.
This hook detects `git rebase main <branch>` commands, checks whether
<branch> is checked out in a worktree via `git worktree list`, and blocks
the command with instructions to remove the worktree first.

Safe sequence (from docs/solutions/2026-02-08-bash-dies-after-worktree-remove.md):
  1. git worktree remove .worktrees/bead-<id>
  2. git stash  (if dirty)
  3. git rebase main bead/<id>
  4. git merge --ff-only bead/<id>

Input: JSON on stdin with tool_name, tool_input, etc.
Output: JSON with permissionDecision=deny if dangerous, nothing otherwise (passthrough).
"""

import json
import re
import subprocess
import sys

# Pattern: git rebase <base> <branch>
# Captures the branch being rebased (the second positional arg).
# Handles flags like --onto, -i, etc. by skipping them.
# We only care about the form: git rebase main <branch>
_REBASE_RE = re.compile(
    r"git\s+rebase\s+"
    r"(?:--\S+\s+)*"  # skip flags
    r"(\S+)\s+"  # base branch (e.g., main)
    r"(\S+)"  # target branch being rebased
)


def parse_rebase_branch(command: str | None) -> str | None:
    """Extract the branch being rebased from a git rebase command.

    Only matches `git rebase <base> <branch>` form.
    Returns the branch name, or None if not a matching rebase command.
    """
    if not command:
        return None
    m = _REBASE_RE.search(command)
    if not m:
        return None
    return m.group(2)


def get_worktree_branches() -> set[str]:
    """Return set of branch names currently checked out in worktrees.

    Parses `git worktree list --porcelain` output. Each worktree entry
    has a `branch refs/heads/<name>` line.
    """
    try:
        result = subprocess.run(
            ["git", "worktree", "list", "--porcelain"],
            capture_output=True,
            text=True,
            timeout=5,
            check=False,
        )
        if result.returncode != 0:
            return set()

        branches = set()
        for line in result.stdout.splitlines():
            if line.startswith("branch refs/heads/"):
                branches.add(line.removeprefix("branch refs/heads/"))
        return branches
    except (subprocess.TimeoutExpired, FileNotFoundError, OSError):
        return set()


def build_decision(hook_input: dict) -> dict | None:
    """Decide whether to block a git rebase command.

    Returns a dict with {"permissionDecision": "deny", "reason": "..."} if the
    rebase target branch is checked out in a worktree, or None to
    passthrough (approve).
    """
    if hook_input.get("tool_name") != "Bash":
        return None

    tool_input = hook_input.get("tool_input")
    if not isinstance(tool_input, dict):
        return None

    command = tool_input.get("command", "")
    if not command or "rebase" not in command:
        return None

    branch = parse_rebase_branch(command)
    if branch is None:
        return None

    worktree_branches = get_worktree_branches()
    if branch not in worktree_branches:
        return None

    return {
        "permissionDecision": "deny",
        "message": (
            f"BLOCKED: Branch '{branch}' is checked out in a worktree. "
            f"git rebase will fail. Remove the worktree first:\n"
            f"  git worktree remove .worktrees/bead-{branch.removeprefix('bead/')}\n"
            f"Then retry the rebase."
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
