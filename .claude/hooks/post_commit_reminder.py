#!/usr/bin/env python3
"""PostToolUse hook: remind agent to log decisions after feat/fix commits.

Detects git commit commands with conventional commit messages starting with
`feat` or `fix`, and reminds the agent to log the decision/discovery to
docs/decisions-and-discoveries.md -- unless the output already mentions it.

Input: JSON on stdin with tool_name, tool_input, tool_output.
Output: JSON with additionalContext reminder, or nothing.
"""

import json
import re
import sys

# Matches git commit where the message starts with feat or fix
# Handles: -m "feat(...): ...", -m "feat: ...", -m "feat!: ...",
# and heredoc patterns containing feat/fix
_FEAT_FIX_RE = re.compile(
    r"git\s+commit\b.*\b(feat|fix)[(!:]",
    re.DOTALL,
)

_DECISIONS_DOC = "decisions-and-discoveries.md"


def is_feat_or_fix_commit(command: str) -> bool:
    """Return True if command is a git commit with a feat or fix type."""
    if not command:
        return False
    return _FEAT_FIX_RE.search(command) is not None


def mentions_decisions_doc(text: str | None) -> bool:
    """Return True if text mentions the decisions-and-discoveries doc."""
    if not text:
        return False
    return _DECISIONS_DOC in text


def build_reminder() -> str:
    """Build the gentle reminder text."""
    return (
        "This feat/fix commit may involve a decision or discovery worth logging. "
        "Consider adding an entry to docs/decisions-and-discoveries.md "
        "(YYYY-MM-DD format with Tags, Context, Decision/Discovery, Implications)."
    )


def process_hook(hook_input: dict | None) -> dict | None:
    """Process hook input and return reminder output or None.

    Fails open: returns None on any error rather than crashing.
    """
    try:
        if not hook_input or not isinstance(hook_input, dict):
            return None

        if hook_input.get("tool_name") != "Bash":
            return None

        tool_input = hook_input.get("tool_input")
        if not tool_input or not isinstance(tool_input, dict):
            return None

        command = tool_input.get("command", "")
        if not is_feat_or_fix_commit(command):
            return None

        tool_output = hook_input.get("tool_output", "")
        if mentions_decisions_doc(tool_output):
            return None

        return {
            "hookSpecificOutput": {
                "hookEventName": "PostToolUse",
                "additionalContext": build_reminder(),
            }
        }
    except Exception:
        return None


def main() -> None:
    try:
        hook_input = json.loads(sys.stdin.read())
        result = process_hook(hook_input)
        if result:
            json.dump(result, sys.stdout)
    except Exception:
        pass  # Fail open


if __name__ == "__main__":
    main()
