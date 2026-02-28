#!/usr/bin/env python3
"""PreToolUse Bash hook: block mutating commands in architect pane.

When ORO_ROLE=architect, intercepts Bash commands and enforces policy:
  - bd commands: allowed (bd create, bd update, bd show, etc.)
  - Read-only git (status, log, diff, branch, show): allowed
  - git pull: allowed
  - All other git (add, commit, push): BLOCKED
  - oro commands (oro start, oro work, etc.): BLOCKED
  - Build commands (make, go build/test/install): BLOCKED
  - All other commands (ls, cat, grep, etc.): allowed (safe default)

Blocked commands are denied with a [BLOCKED] message. No forwarding to
the manager pane occurs from build_decision(). The send_to_manager_pane
helper is used ONLY by notify_on_bead_create (PostToolUse notification).

Input: JSON on stdin with tool_name, tool_input, etc.
Output: JSON with permissionDecision=deny + additionalContext for blocked commands.
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

    Returns "architect" for all commands. Forwarding to manager has been
    removed; build_decision() handles blocking directly without forwarding.
    """
    return "architect"


# Extensions for code files that the architect must not stage or commit
_CODE_EXTENSIONS = frozenset({".go", ".py", ".sh", ".js", ".ts"})

# Extensions that the architect IS allowed to stage
_ALLOWED_EXTENSIONS = frozenset({".md", ".yaml", ".yml"})


def _is_code_file(path: str) -> bool:
    """Return True if path has a code-file extension."""
    return any(path.endswith(ext) for ext in _CODE_EXTENSIONS)


def _is_allowed_file(path: str) -> bool:
    """Return True if path has an allowed (markdown/YAML) extension."""
    return any(path.endswith(ext) for ext in _ALLOWED_EXTENSIONS)


def verify_staged_files() -> bool:
    """Check that all staged files are markdown or YAML.

    Runs ``git diff --cached --name-only`` and inspects each file extension.
    Returns True if every staged file is .md/.yaml/.yml (or nothing is staged).
    Returns False if any code file is staged, or if the git command fails.
    """
    try:
        result = subprocess.run(
            ["git", "diff", "--cached", "--name-only"],
            capture_output=True,
            text=True,
        )
        if result.returncode != 0:
            return False  # Safe default: block if we can't verify
    except (subprocess.CalledProcessError, FileNotFoundError):
        return False

    files = [f for f in result.stdout.strip().split("\n") if f]
    if not files:
        return True  # Nothing staged — allow (git will say "nothing to commit")

    return all(_is_allowed_file(f) for f in files)


def _check_git_command(command: str) -> dict | None:
    """Check a git command and return a block decision or None to allow.

    Returns a deny dict if the command should be blocked, or None to passthrough.
    Read-only commands are allowed; all mutating commands (add/commit/push) are blocked.
    """
    trimmed = command.strip()

    # Read-only git commands — always allowed
    for prefix in ("git status", "git log", "git diff", "git branch", "git show"):
        if trimmed.startswith(prefix):
            return None

    # git pull — allowed
    if trimmed.startswith("git pull"):
        return None

    # Block all mutating commands
    if trimmed.startswith("git add"):
        return {
            "permissionDecision": "deny",
            "message": format_forward_message(
                trimmed,
                blocked_reason="git add not allowed in architect pane",
            ),
        }

    if trimmed.startswith("git commit"):
        return {
            "permissionDecision": "deny",
            "message": format_forward_message(
                trimmed,
                blocked_reason="git commit not allowed in architect pane",
            ),
        }

    if trimmed.startswith("git push"):
        return {
            "permissionDecision": "deny",
            "message": format_forward_message(
                trimmed,
                blocked_reason="git push not allowed in architect pane",
            ),
        }

    # Other git commands (e.g. git stash, git checkout) — allow by default
    return None


def format_forward_message(command: str, blocked_reason: str = "") -> str:
    """Format the feedback message shown to the architect."""
    if blocked_reason:
        return f"[BLOCKED] {blocked_reason}: {command.strip()}"
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
    """Decide whether to block a command.

    Returns a dict with permissionDecision=deny + additionalContext if the
    command should be blocked, or None to passthrough (execute locally).
    Never calls send_to_manager_pane — no forwarding occurs here.
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

    trimmed = command.strip()

    # Check git commands (read-only allowed, mutating blocked)
    if trimmed.startswith("git "):
        return _check_git_command(command)

    # Block oro commands — architect must not run oro directly
    if trimmed.startswith("oro "):
        return {
            "permissionDecision": "deny",
            "message": format_forward_message(
                trimmed,
                blocked_reason="oro commands not allowed in architect pane",
            ),
        }

    # Block build commands
    if trimmed.startswith("make ") or trimmed == "make" or trimmed.startswith(("go build", "go test", "go install")):
        return {
            "permissionDecision": "deny",
            "message": format_forward_message(
                trimmed,
                blocked_reason="Build commands not allowed in architect pane",
            ),
        }

    # All other commands passthrough (bd, ls, cat, grep, etc.)
    return None


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
