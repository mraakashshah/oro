#!/usr/bin/env python3
"""PostToolUse Bash hook: notify manager when architect creates beads.

When ORO_ROLE=architect and a 'bd create' command is executed, sends a
notification to the manager pane via tmux send-keys to alert them
that new work is available.

This is a PostToolUse hook — it runs AFTER the command completes, so the
bead is already created and visible in bd ready.

Input: JSON on stdin with tool_name, tool_input, tool_output, etc.
Output: None (hook doesn't modify behavior, just sends notification)
"""

import json
import os
import sys

# Import send_to_manager_pane from architect_router
from architect_router import send_to_manager_pane


def get_oro_role() -> str:
    """Get the current ORO_ROLE from environment."""
    return os.environ.get("ORO_ROLE", "")


def notify_manager(message: str, session_name: str = "oro") -> bool:
    """Send a notification to the manager pane via tmux send-keys.

    Uses send_to_manager_pane from architect_router which sends the message
    as actual keystrokes to the manager pane.

    Returns True if successful, False otherwise.
    """
    return send_to_manager_pane(message, session_name)


def should_notify(hook_input: dict) -> bool:
    """Determine if this command warrants a notification.

    Returns True if:
    - Role is architect
    - Tool is Bash
    - Command starts with 'bd create'
    """
    if get_oro_role() != "architect":
        return False

    if hook_input.get("tool_name") != "Bash":
        return False

    tool_input = hook_input.get("tool_input")
    if not isinstance(tool_input, dict):
        return False

    command = tool_input.get("command", "").strip()
    return command.startswith("bd create")


def main() -> None:
    try:
        hook_input = json.loads(sys.stdin.read())
    except (json.JSONDecodeError, EOFError):
        return

    if not should_notify(hook_input):
        return

    # Send notification to manager pane
    notify_manager("[NEW WORK] Architect created a bead. Check 'bd ready'.")


if __name__ == "__main__":
    main()
