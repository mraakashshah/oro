#!/usr/bin/env python3
"""PostToolUse Bash hook: notify manager when architect creates beads.

When ORO_ROLE=architect and a Bash command starting with "bd create" executes,
send a notification to the manager pane via tmux display-message.

Input: JSON on stdin with tool_name, tool_input, etc.
Output: No output needed for PostToolUse hooks (they don't affect execution).
"""

import json
import os
import subprocess
import sys


def get_oro_role() -> str:
    """Get the current ORO_ROLE from environment."""
    return os.environ.get("ORO_ROLE", "")


def notify_manager(message: str, session_name: str = "oro") -> bool:
    """Send a notification message to the manager pane via tmux display-message.

    Returns True if successful, False otherwise.
    """
    manager_pane = f"{session_name}:manager"

    try:
        subprocess.run(
            ["tmux", "display-message", "-t", manager_pane, message],
            check=True,
            capture_output=True,
            text=True,
        )
        return True
    except (subprocess.CalledProcessError, FileNotFoundError):
        return False


def handle_post_tool_use(hook_input: dict) -> None:
    """Check if this was a 'bd create' command and notify manager if so."""
    # Only act when running as architect
    if get_oro_role() != "architect":
        return

    if hook_input.get("tool_name") != "Bash":
        return

    tool_input = hook_input.get("tool_input")
    if not isinstance(tool_input, dict):
        return

    command = tool_input.get("command", "")
    if not command:
        return

    trimmed = command.strip()

    # Check if this is a "bd create" command
    if trimmed.startswith("bd create"):
        notify_manager("[NEW WORK] Architect created a bead. Check bd ready.")


def main() -> None:
    try:
        hook_input = json.loads(sys.stdin.read())
    except (json.JSONDecodeError, EOFError):
        return

    handle_post_tool_use(hook_input)
    # PostToolUse hooks don't output anything - they run for side effects only


if __name__ == "__main__":
    main()
