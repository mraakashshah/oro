#!/usr/bin/env python3
"""PostToolUse hook: surface undocumented learnings on git commit with bead reference.

Intercepts `git commit` commands run via Bash tool. Parses the commit message
for a bead reference (bd-xyz or (bd-xyz)). If found, loads the knowledge base
and checks for undocumented learnings for that bead. If entries exist, injects
an additionalContext reminder to document them.

Input: JSON on stdin with tool_name, tool_input, etc.
Output: JSON with additionalContext reminder, or nothing.
"""

import json
import os
import re
import sys
from pathlib import Path

# Allow override via env var for testing; default to the standard location.
KNOWLEDGE_FILE = os.environ.get("ORO_KNOWLEDGE_FILE", ".beads/memory/knowledge.jsonl")

# Match "git commit" commands — must start with git commit (possibly with flags)
_GIT_COMMIT_RE = re.compile(r"\bgit\s+commit\b")

# Extract bead reference from commit message: bd-xyz, (bd-xyz), bd-zw5.4, etc.
# The bead ID part after "bd-" can contain word chars and dots.
_BEAD_REF_RE = re.compile(r"\bbd-([\w.]+)")


def _parse_bead_ref(command: str) -> str | None:
    """Extract bead ID from a git commit command, returning 'oro-<id>' or None."""
    if not _GIT_COMMIT_RE.search(command):
        return None
    m = _BEAD_REF_RE.search(command)
    if not m:
        return None
    return f"oro-{m.group(1)}"


def _load_knowledge(path: Path) -> list[dict]:
    """Read knowledge.jsonl, deduplicate by key (latest wins)."""
    try:
        with open(path) as f:
            lines = f.readlines()
    except OSError:
        return []

    by_key: dict[str, dict] = {}
    for line in lines:
        line = line.strip()
        if not line:
            continue
        try:
            entry = json.loads(line)
        except json.JSONDecodeError:
            continue
        by_key[entry["key"]] = entry

    return list(by_key.values())


def _filter_by_bead(entries: list[dict], bead_id: str) -> list[dict]:
    """Filter entries by bead field."""
    return [e for e in entries if e.get("bead") == bead_id]


def main() -> None:
    hook_input = json.loads(sys.stdin.read())

    if hook_input.get("tool_name") != "Bash":
        return

    command = hook_input.get("tool_input", {}).get("command", "")
    bead_id = _parse_bead_ref(command)
    if not bead_id:
        return

    entries = _load_knowledge(Path(KNOWLEDGE_FILE))
    matched = _filter_by_bead(entries, bead_id)
    if not matched:
        return

    count = len(matched)
    s = "s" if count != 1 else ""
    output = {
        "hookSpecificOutput": {
            "hookEventName": "PostToolUse",
            "additionalContext": (
                f"{count} undocumented learning{s} for bead {bead_id}. "
                f"Consider adding to docs/decisions-and-discoveries.md or running oro remember."
            ),
        }
    }
    json.dump(output, sys.stdout)


if __name__ == "__main__":
    main()
