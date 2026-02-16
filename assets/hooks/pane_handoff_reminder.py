#!/usr/bin/env python3
"""PreToolUse hook: inject handoff reminder when signaled by dispatcher.

Checks ~/.oro/panes/<role>/handoff_requested signal file. When found, injects
a system reminder forcing the agent to create a handoff at ~/.oro/panes/<role>/handoff.yaml.

Fast path: no-op when ORO_ROLE not set or signal file missing (<5ms).
Does NOT delete the signal file — dispatcher manages its lifecycle.

Input: JSON on stdin with hookEventName, tool_name, tool_input, etc.
Output: JSON with additionalContext reminder, or nothing.
"""

import json
import os
import sys
from pathlib import Path


def _get_panes_dir() -> Path:
    """Return the panes directory, defaulting to ~/.oro/panes."""
    if override := os.environ.get("ORO_PANES_DIR"):
        return Path(override)
    home = os.environ.get("HOME", "")
    return Path(home) / ".oro" / "panes"


def _check_signal(role: str) -> bool:
    """Check if handoff_requested signal exists for the given role."""
    panes_dir = _get_panes_dir()
    signal_file = panes_dir / role / "handoff_requested"
    return signal_file.exists()


def main() -> None:
    # Fast path: no-op if ORO_ROLE not set
    role = os.environ.get("ORO_ROLE")
    if not role:
        return

    # Fast path: no-op if signal file missing
    if not _check_signal(role):
        return

    # Signal present: inject system reminder
    panes_dir = _get_panes_dir()
    handoff_path = panes_dir / role / "handoff.yaml"

    context = (
        f"CRITICAL: Context threshold reached. You MUST create a handoff now. "
        f"Write your handoff to {handoff_path} immediately. "
        f"Include: goal (what you accomplished), now (next steps), "
        f"done_this_session (tasks + files + details), and decisions (any key choices made)."
    )

    output = {
        "hookSpecificOutput": {
            "hookEventName": "PreToolUse",
            "additionalContext": context,
        }
    }
    json.dump(output, sys.stdout)


if __name__ == "__main__":
    main()
