#!/usr/bin/env python3
"""PostToolUse hook: surface undocumented learnings on git commit with bead reference.

Intercepts `git commit` commands run via Bash tool. Parses the commit message
for a bead reference (bd-xyz or (bd-xyz)). If found, loads the knowledge base
and checks for undocumented learnings for that bead. If entries exist, injects
an additionalContext reminder to document them.

Also runs frequency analysis across ALL knowledge entries. If any tag from the
matched bead's entries has reached 3+ frequency, appends a codification proposal
to the reminder (per the session-end learning synthesis spec).

Input: JSON on stdin with tool_name, tool_input, etc.
Output: JSON with additionalContext reminder, or nothing.
"""

import json
import os
import re
import sys
from pathlib import Path

from learning_analysis import frequency_level, load_knowledge, tag_frequency

# Allow override via env var for testing; default to the standard location.
KNOWLEDGE_FILE = os.environ.get("ORO_KNOWLEDGE_FILE", ".beads/memory/knowledge.jsonl")

# Match "git commit" commands — must start with git commit (possibly with flags)
_GIT_COMMIT_RE = re.compile(r"\bgit\s+commit\b")

# Extract bead reference from commit message: bd-xyz, (bd-xyz), bd-zw5.4, etc.
# The bead ID part after "bd-" can contain word chars and dots.
_BEAD_REF_RE = re.compile(r"\bbd-([\w.]+)")

# Decision tree mapping for codification proposals
_CODIFY_DECISION_TREE = (
    "Repeatable sequence -> skill (.claude/skills/), "
    "Event-triggered -> hook (.claude/hooks/), "
    "Heuristic/constraint -> rule (.claude/rules/), "
    "Solved problem -> solution doc (docs/decisions-and-discoveries.md)"
)


def _parse_bead_ref(command: str) -> str | None:
    """Extract bead ID from a git commit command, returning 'oro-<id>' or None."""
    if not _GIT_COMMIT_RE.search(command):
        return None
    m = _BEAD_REF_RE.search(command)
    if not m:
        return None
    return f"oro-{m.group(1)}"


def _filter_by_bead(entries: list[dict], bead_id: str) -> list[dict]:
    """Filter entries by bead field."""
    return [e for e in entries if e.get("bead") == bead_id]


def _codification_proposals(matched: list[dict], all_entries: list[dict]) -> list[str]:
    """Build codification proposal strings for tags that hit 3+ frequency.

    Checks tags from the matched (bead-specific) entries against global
    tag frequency across all entries. Returns a proposal line per qualifying tag.
    """
    global_freq = tag_frequency(all_entries)

    # Collect unique tags from matched entries
    bead_tags: set[str] = set()
    for entry in matched:
        for tag in entry.get("tags", []):
            bead_tags.add(tag)

    proposals: list[str] = []
    for tag in sorted(bead_tags):
        count = global_freq.get(tag, 0)
        if frequency_level(count) == "create":
            proposals.append(
                f"Tag '{tag}' has reached {count} occurrences — consider codification: {_CODIFY_DECISION_TREE}"
            )

    return proposals


def main() -> None:
    hook_input = json.loads(sys.stdin.read())

    if hook_input.get("tool_name") != "Bash":
        return

    command = hook_input.get("tool_input", {}).get("command", "")
    bead_id = _parse_bead_ref(command)
    if not bead_id:
        return

    all_entries = load_knowledge(Path(KNOWLEDGE_FILE))
    matched = _filter_by_bead(all_entries, bead_id)
    if not matched:
        return

    count = len(matched)
    s = "s" if count != 1 else ""
    context_parts = [
        f"{count} undocumented learning{s} for bead {bead_id}. "
        f"Consider adding to docs/decisions-and-discoveries.md or running oro remember."
    ]

    # Frequency analysis: check if any tag from this bead's entries hit 3+
    proposals = _codification_proposals(matched, all_entries)
    context_parts.extend(proposals)

    output = {
        "hookSpecificOutput": {
            "hookEventName": "PostToolUse",
            "additionalContext": " ".join(context_parts),
        }
    }
    json.dump(output, sys.stdout)


if __name__ == "__main__":
    main()
