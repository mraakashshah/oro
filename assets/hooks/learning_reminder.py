#!/usr/bin/env python3
"""PostToolUse hook: surface recent memories as a reminder on git commit.

Intercepts `git commit` commands run via Bash tool. After a commit,
fetches recent memories from memories.db (via `oro memories list`) and
injects an additionalContext reminder to review them.

Input: JSON on stdin with tool_name, tool_input, etc.
Output: JSON with additionalContext reminder, or nothing.
"""

import json
import re
import subprocess
import sys

# Match "git commit" commands — must start with git commit (possibly with flags)
_GIT_COMMIT_RE = re.compile(r"\bgit\s+commit\b")


def _is_git_commit(command: str) -> bool:
    """Return True if the command contains a git commit invocation."""
    return bool(_GIT_COMMIT_RE.search(command))


def _fetch_recent_memories(limit: int = 3) -> list[dict]:
    """Fetch recent memories from memories.db via `oro memories list`.

    Returns empty list when oro is not on PATH, exits non-zero, or outputs invalid JSON.
    """
    try:
        result = subprocess.run(
            ["oro", "memories", "list", "--format=json", f"--limit={limit}"],
            capture_output=True,
            text=True,
            timeout=10,
        )
        if result.returncode != 0:
            return []
    except (subprocess.TimeoutExpired, OSError):
        return []

    try:
        entries = json.loads(result.stdout)
    except (json.JSONDecodeError, ValueError):
        return []

    return entries if isinstance(entries, list) else []


def main() -> None:
    hook_input = json.loads(sys.stdin.read())

    if hook_input.get("tool_name") != "Bash":
        return

    command = hook_input.get("tool_input", {}).get("command", "")
    if not _is_git_commit(command):
        return

    memories = _fetch_recent_memories(limit=3)
    if not memories:
        return

    memory_lines = [f"- {m.get('content', '')}" for m in memories if m.get("content")]
    if not memory_lines:
        return

    count = len(memory_lines)
    noun = "memory" if count == 1 else "memories"
    context = f"{count} recent {noun} — review before next session:\n" + "\n".join(memory_lines)

    output = {
        "hookSpecificOutput": {
            "hookEventName": "PostToolUse",
            "additionalContext": context,
        }
    }
    json.dump(output, sys.stdout)


if __name__ == "__main__":
    main()
