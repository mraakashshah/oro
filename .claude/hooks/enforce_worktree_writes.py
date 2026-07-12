#!/usr/bin/env python3
"""PreToolUse hook: block Write/Edit to the primary git checkout.

Enforces the "all code in worktrees" policy: file mutations (Write, Edit,
NotebookEdit) whose target resolves inside a git PRIMARY working tree are
denied, so that concurrent agents never edit the shared main checkout. A write
is ALLOWED when the target is:
  - inside a linked git worktree (the intended isolation), or
  - outside any git repository (e.g. an OS temp/scratch path), or
  - under an allow-listed prefix of the checkout (docs/, .worktrees/, .claude/),
    where .claude/ stays writable so this hook can always be disabled, or
  - any path, when the ORO_ALLOW_MAIN_WRITES escape hatch is set.

Detection follows the ce-worktree method: compare the resolved --absolute-git-dir
against the resolved --git-common-dir. Equal => primary checkout; differ (and not
a submodule) => linked worktree.

Input: JSON on stdin with tool_name, tool_input, cwd.
Output: deny JSON if blocked, nothing otherwise (passthrough).
"""

import json
import os
import subprocess
import sys
from pathlib import Path

# Tools that mutate files on disk.
_WRITE_TOOLS = frozenset({"Write", "Edit", "NotebookEdit"})

# Environment escape hatch: set to a truthy value to permit primary-checkout
# writes for the whole session (e.g. ORO_ALLOW_MAIN_WRITES=1).
_ESCAPE_ENV = "ORO_ALLOW_MAIN_WRITES"

# Paths (relative to the checkout root) that stay writable in the primary
# checkout. .claude/ is included so the control surface — settings.json, this
# hook — can always be edited to disable the policy without a lockout.
ALLOWLIST_PREFIXES = ("docs/", ".worktrees/", ".claude/")


def is_escape_hatch_set(env) -> bool:
    """True when the escape-hatch env var is set to a truthy value."""
    val = env.get(_ESCAPE_ENV, "")
    return val.strip().lower() not in ("", "0", "false", "no", "off")


def target_path_for(tool_name: str, tool_input: dict) -> str | None:
    """Extract the file path a write-tool call targets."""
    if tool_name == "NotebookEdit":
        return tool_input.get("notebook_path") or tool_input.get("file_path")
    return tool_input.get("file_path")


def nearest_existing_dir(path) -> Path:
    """Walk up from path to the nearest directory that exists on disk.

    Write can target a file whose parent directories do not exist yet, so we
    probe git from the closest existing ancestor.
    """
    d = Path(path)
    while not d.exists() and d != d.parent:
        d = d.parent
    return d


def _git(cwd, *args) -> str | None:
    """Run a git command in cwd; return stripped stdout, or None on failure."""
    try:
        result = subprocess.run(
            ["git", "-C", str(cwd), *args],
            capture_output=True,
            text=True,
            check=True,
        )
        return result.stdout.strip()
    except (subprocess.CalledProcessError, FileNotFoundError):
        return None


def classify_checkout(target_dir) -> str:
    """Classify target_dir as 'primary', 'linked', 'submodule', or 'none'.

    'none' means the directory is not inside a git repository.
    """
    abs_git_dir = _git(target_dir, "rev-parse", "--absolute-git-dir")
    if abs_git_dir is None:
        return "none"
    common = _git(target_dir, "rev-parse", "--git-common-dir")
    if common is None:
        return "none"
    common_path = Path(common)
    if not common_path.is_absolute():
        common_path = Path(target_dir) / common_path
    if Path(abs_git_dir).resolve() == common_path.resolve():
        return "primary"
    # A submodule's git dir also differs from its common dir; treat it as a
    # shared checkout (not the isolation we want), same as ce-worktree.
    if _git(target_dir, "rev-parse", "--show-superproject-working-tree"):
        return "submodule"
    return "linked"


def checkout_root(target_dir) -> Path | None:
    """Resolve the working-tree root for target_dir, or None if not a repo."""
    top = _git(target_dir, "rev-parse", "--show-toplevel")
    return Path(top).resolve() if top else None


def is_allowlisted(target, root) -> bool:
    """True when target sits under an allow-listed prefix of the checkout root."""
    try:
        rel = Path(target).resolve().relative_to(Path(root).resolve())
    except ValueError:
        return False
    rel_str = rel.as_posix()
    return any(rel_str == prefix.rstrip("/") or rel_str.startswith(prefix) for prefix in ALLOWLIST_PREFIXES)


def build_decision(hook_input: dict) -> dict | None:
    """Decide whether to block a write. Returns a deny dict or None (allow)."""
    tool_name = hook_input.get("tool_name", "")
    if tool_name not in _WRITE_TOOLS:
        return None

    tool_input = hook_input.get("tool_input")
    if not isinstance(tool_input, dict):
        return None

    target = target_path_for(tool_name, tool_input)
    if not target:
        return None

    if is_escape_hatch_set(os.environ):
        return None

    cwd = hook_input.get("cwd") or os.getcwd()
    abs_target = Path(target)
    if not abs_target.is_absolute():
        abs_target = Path(cwd) / abs_target
    abs_target = abs_target.resolve()

    status = classify_checkout(nearest_existing_dir(abs_target.parent))
    if status in ("none", "linked"):
        return None

    root = checkout_root(nearest_existing_dir(abs_target.parent))
    if root is not None and is_allowlisted(abs_target, root):
        return None

    return {
        "permissionDecision": "deny",
        "message": (
            f"BLOCKED: writing to {abs_target} edits the primary git checkout "
            f"({root}), where a concurrent agent may be working. Per the "
            f"all-code-in-worktrees policy, create/enter a linked worktree first: "
            f"`git worktree add .worktrees/<branch> -b <branch>` then edit under "
            f".worktrees/<branch>/. To override for this session, start it with "
            f"{_ESCAPE_ENV}=1."
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
