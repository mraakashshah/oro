#!/usr/bin/env python3
"""PostToolUse Bash hook: notify manager when architect creates tasks.

When ORO_ROLE=architect and an 'oro task create' command is executed, sends a
notification to the manager pane via tmux send-keys to alert them
that new work is available.

This is a PostToolUse hook — it runs AFTER the command completes, so the
task is already created and visible in oro task ready.

Input: JSON on stdin with tool_name, tool_input, tool_output, etc.
Output: None (hook doesn't modify behavior, just sends notification)
"""

import json
import os
import shlex
import sys

# Import send_to_manager_pane from architect_router
from architect_router import send_to_manager_pane

_SHELL_CONTROL_TOKENS = frozenset({";", "&&", "||", "|", "&", ">", ">>", "<", "<<"})
_SHELL_OPERATOR_CHARS = frozenset(";&|<>")


def _shell_tokens(command: str) -> list[str]:
    """Tokenize a shell command enough for conservative hook policy checks."""
    lexer = shlex.shlex(command, posix=True, punctuation_chars=True)
    lexer.whitespace_split = True
    return list(lexer)


def _has_shell_control_operator(command: str) -> bool:
    """Return True when command contains shell control flow or separators."""
    if "\n" in command or "$(" in command or "`" in command or "<(" in command or ">(" in command:
        return True
    try:
        tokens = _shell_tokens(command)
    except ValueError:
        return True
    return any(token in _SHELL_CONTROL_TOKENS or all(ch in _SHELL_OPERATOR_CHARS for ch in token) for token in tokens)


def _is_bead_create_command(command: str) -> bool:
    """Return True when command creates a task through a supported CLI path."""
    if _has_shell_control_operator(command):
        return False
    try:
        tokens = _shell_tokens(command)
    except ValueError:
        return False
    return tokens[:3] in (["oro", "task", "create"], ["oro", "bead", "create"])


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
    - Command starts with current task-create or legacy bead-create CLI path
    """
    if get_oro_role() != "architect":
        return False

    if hook_input.get("tool_name") != "Bash":
        return False

    tool_input = hook_input.get("tool_input")
    if not isinstance(tool_input, dict):
        return False

    command = tool_input.get("command", "").strip()
    return _is_bead_create_command(command)


def main() -> None:
    try:
        hook_input = json.loads(sys.stdin.read())
    except (json.JSONDecodeError, EOFError):
        return

    if not should_notify(hook_input):
        return

    # Send notification to manager pane
    notify_manager("[NEW WORK] Architect created a task. Check 'oro task ready'.")


if __name__ == "__main__":
    main()
