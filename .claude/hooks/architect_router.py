#!/usr/bin/env python3
"""PreToolUse Bash hook: route architect commands to manager pane.

When ORO_ROLE=architect, intercepts Bash commands and routes them:
  - Commands starting with "bd " stay with the architect (passthrough)
  - Commands starting with "oro " forward to manager pane via tmux send-keys
  - Git read-only commands (status, log, diff) stay with architect
  - Git add of .md/.yaml/.yml files allowed; code files (.go/.py/.sh/.js/.ts) blocked
  - Git commit allowed only when staged files are all markdown/YAML
  - Git push allowed
  - Build commands (make, go build/test/install) forwarded to manager
  - All other commands (ls, cat, etc.) stay with architect (safe default)

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

    Routing logic:
      - Commands starting with "bd " stay with architect
      - Commands starting with "oro " go to manager
      - Build commands (make, go build/test/install) go to manager
      - All other commands stay with architect (safe default)
    """
    trimmed = command.strip()
    if not trimmed:
        return "architect"

    if trimmed.startswith("bd "):
        return "architect"

    if trimmed.startswith("oro "):
        return "manager"

    # Build commands forward to manager
    if trimmed.startswith("make ") or trimmed == "make":
        return "manager"

    if trimmed.startswith(("go build", "go test", "go install")):
        return "manager"

    # Safe default: keep read-only and unknown commands with architect
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
    """
    trimmed = command.strip()

    # Read-only git commands — always allowed
    for prefix in ("git status", "git log", "git diff", "git branch", "git show"):
        if trimmed.startswith(prefix):
            return None

    # git push — allowed
    if trimmed.startswith("git push"):
        return None

    # git pull — allowed
    if trimmed.startswith("git pull"):
        return None

    # git add — check file extensions
    if trimmed.startswith("git add"):
        args = trimmed[len("git add") :].strip()
        if not args:
            return None  # bare "git add" with no args — passthrough

        # Parse file paths from the arguments (skip flags like -A, -u, etc.)
        paths = [a for a in args.split() if not a.startswith("-")]
        if not paths:
            return None

        # Block if any path is a code file
        if any(_is_code_file(p) for p in paths):
            blocked_files = [p for p in paths if _is_code_file(p)]
            return {
                "permissionDecision": "deny",
                "message": format_forward_message(
                    trimmed,
                    blocked_reason=f"Cannot add code files from architect pane ({', '.join(blocked_files)})",
                ),
            }

        # All files are allowed (markdown/yaml) or unrecognized (allow)
        return None

    # git commit — check staged files
    if trimmed.startswith("git commit"):
        if verify_staged_files():
            return None
        return {
            "permissionDecision": "deny",
            "message": format_forward_message(
                trimmed,
                blocked_reason="Cannot commit — code files are staged",
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

    trimmed = command.strip()

    # Check git commands for markdown/code restrictions
    if trimmed.startswith("git "):
        git_decision = _check_git_command(command)
        if git_decision is not None:
            return git_decision
        # If _check_git_command returns None, it's allowed — passthrough
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
