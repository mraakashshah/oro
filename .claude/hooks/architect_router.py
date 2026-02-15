#!/usr/bin/env python3
"""PreToolUse Bash hook: route architect commands to manager pane.

When ORO_ROLE=architect, intercepts Bash commands and routes them:
  - Commands starting with "bd " stay with the architect (passthrough)
  - Commands starting with "oro " forward to manager pane via tmux send-keys
  - All other commands (git, ls, etc.) forward to manager pane

Forwarded commands are sent to the manager pane and blocked from execution
in the architect pane. The architect receives feedback like:
  "[forwarded to manager] oro directive scale 3"

Input: JSON on stdin with tool_name, tool_input, etc.
Output: JSON with permissionDecision=deny + additionalContext for forwarded commands.
"""

import json
import os
import subprocess
import sys


def get_oro_role() -> str:
    """Get the current ORO_ROLE from environment."""
    return os.environ.get("ORO_ROLE", "")


def route_command(command: str) -> str:
    """Determine routing destination.

    Returns "architect" or "manager".

    Routing logic mirrors the Go RouteCommand function:
      - Commands starting with "bd " stay with architect
      - Commands starting with "oro " go to manager
      - All other commands go to manager
    """
    trimmed = command.strip()
    if not trimmed:
        return "manager"

    if trimmed.startswith("bd "):
        return "architect"

    # All oro commands and unknown commands go to manager
    return "manager"


def format_forward_message(command: str) -> str:
    """Format the feedback message shown to the architect."""
    trimmed = command.strip()
    if trimmed.startswith("oro "):
        return f"[forwarded to manager] {trimmed}"
    return f"[forwarded] {trimmed}"


def send_to_manager_pane(command: str, session_name: str = "oro") -> bool:
    """Send command to manager pane via tmux send-keys.

    Returns True if successful, False otherwise.
    """
    manager_pane = f"{session_name}:manager"

    try:
        # Send text using literal mode (-l) to handle special chars
        subprocess.run(
            ["tmux", "send-keys", "-t", manager_pane, "-l", command],
            check=True,
            capture_output=True,
            text=True,
        )

        # Send Enter to submit the command
        subprocess.run(
            ["tmux", "send-keys", "-t", manager_pane, "Enter"],
            check=True,
            capture_output=True,
            text=True,
        )

        return True
    except (subprocess.CalledProcessError, FileNotFoundError):
        return False


def build_decision(hook_input: dict) -> dict | None:
    """Decide whether to block and forward a command.

    Returns a dict with permissionDecision=deny + additionalContext if the
    command should be forwarded, or None to passthrough (execute locally).
    """
    # Only intercept when running as architect
    if get_oro_role() != "architect":
        return None

    if hook_input.get("tool_name") != "Bash":
        return None

    tool_input = hook_input.get("tool_input")
    if not isinstance(tool_input, dict):
        return None

    command = tool_input.get("command", "")
    if not command:
        return None

    destination = route_command(command)

    # If staying with architect, passthrough
    if destination == "architect":
        return None

    # Forward to manager pane
    forward_msg = format_forward_message(command)
    send_success = send_to_manager_pane(command)

    if not send_success:
        # If tmux send-keys fails, don't block the command
        # (maybe we're not in a tmux session, or manager pane doesn't exist)
        return None

    # Block execution in architect pane and show feedback
    return {
        "permissionDecision": "deny",
        "message": forward_msg,
    }


def notify_on_bead_create(hook_input: dict) -> dict | None:
    """Send notification to manager when architect creates a bead.

    This is a PostToolUse hook that triggers after bd create commands.
    Returns additionalContext to notify the agent, or None if no notification needed.
    """
    # Only notify when running as architect
    if get_oro_role() != "architect":
        return None

    if hook_input.get("tool_name") != "Bash":
        return None

    tool_input = hook_input.get("tool_input")
    if not isinstance(tool_input, dict):
        return None

    command = tool_input.get("command", "").strip()
    if not command.startswith("bd create"):
        return None

    # Send notification to manager pane
    notification = "[NEW WORK] Architect created a bead. Check bd ready."
    send_success = send_to_manager_pane(notification)

    if not send_success:
        # Fail open: if tmux send-keys fails, don't block
        return None

    # Return additionalContext to inform the agent (but don't block)
    return {
        "hookSpecificOutput": {
            "hookEventName": "PostToolUse",
        },
        "additionalContext": "✓ Manager notified of new bead",
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
        "additionalContext": message,
    }
    json.dump(output, sys.stdout)


if __name__ == "__main__":
    main()
