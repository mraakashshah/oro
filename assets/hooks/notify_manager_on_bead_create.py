#!/usr/bin/env python3
"""PostToolUse Bash hook: batch notify manager when architect creates beads.

When ORO_ROLE=architect and a Bash command starting with "bd create" executes,
accumulate the bead into a debounce state file. After a debounce window of no
new creates (default 30s), send a single grouped notification to the manager
pane listing all new beads.

State is persisted across process invocations in a JSON file at
$ORO_HOME/bead_notify_state.json.

Input: JSON on stdin with tool_name, tool_input, etc.
Output: No output needed for PostToolUse hooks (they don't affect execution).

Pure functions (testable with no I/O):
  - load_state(state_file) -> dict
  - save_state(state, state_file) -> None
  - record_bead_create(bead_id, bead_title, now, state) -> dict
  - should_notify(state, now, window_secs) -> bool
  - format_batch_notification(state) -> str

Impure edges:
  - notify_manager(message, session_name) -> bool
  - handle_post_tool_use(hook_input, state_file, now, window_secs) -> None
  - main() -> None
"""

import json
import os
import subprocess
import sys
import time
from pathlib import Path

_DEBOUNCE_WINDOW_SECS = 30
_EMPTY_STATE: dict = {"beads": [], "last_create_ts": 0.0}


# ---------------------------------------------------------------------------
# Pure functions
# ---------------------------------------------------------------------------


def load_state(state_file: str) -> dict:
    """Load batching state from state_file.

    Returns empty state if file is missing or corrupt.
    """
    try:
        content = Path(state_file).read_text()
        data = json.loads(content)
        # Validate expected structure
        if not isinstance(data, dict):
            return dict(_EMPTY_STATE)
        return {
            "beads": data.get("beads", []),
            "last_create_ts": float(data.get("last_create_ts", 0.0)),
        }
    except (OSError, json.JSONDecodeError, ValueError):
        return dict(_EMPTY_STATE)


def save_state(state: dict, state_file: str) -> None:
    """Persist batching state to state_file, creating parent dirs as needed."""
    path = Path(state_file)
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(state))


def record_bead_create(bead_id: str, bead_title: str, now: float, state: dict) -> dict:
    """Return new state with bead appended and last_create_ts updated.

    Does not mutate the input state dict.
    """
    new_beads = [*state.get("beads", []), {"id": bead_id, "title": bead_title}]
    return {"beads": new_beads, "last_create_ts": now}


def should_notify(state: dict, now: float, window_secs: int) -> bool:
    """Return True if the debounce window has expired and there are pending beads.

    The window expires when `now - last_create_ts >= window_secs`.
    """
    if not state.get("beads"):
        return False
    elapsed = now - state.get("last_create_ts", 0.0)
    return elapsed >= window_secs


def format_batch_notification(state: dict) -> str:
    """Format a grouped notification message for all pending beads."""
    beads = state.get("beads", [])
    count = len(beads)
    lines = [f"[NEW WORK] {count} bead(s) ready — check bd ready:"]
    for b in beads:
        lines.append(f"  • {b['id']}: {b['title']}")
    return "\n".join(lines)


# ---------------------------------------------------------------------------
# Impure edges
# ---------------------------------------------------------------------------


def _default_state_file() -> str:
    """Return the default state file path: $ORO_HOME/bead_notify_state.json."""
    oro_home = os.environ.get("ORO_HOME", os.path.expanduser("~/.oro"))
    return os.path.join(oro_home, "bead_notify_state.json")


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


def handle_post_tool_use(
    hook_input: dict,
    state_file: str | None = None,
    now: float | None = None,
    window_secs: int = _DEBOUNCE_WINDOW_SECS,
) -> None:
    """Handle a PostToolUse hook event with batched notification logic.

    1. If role != architect or tool != Bash or command != bd create → skip.
    2. Load state, check if window expired (should_notify).
       - If yes: flush accumulated beads as a single grouped notification,
         clear state, then record the new bead.
       - If no: record the new bead (accumulate).
    3. Save updated state.

    Args:
        hook_input: Parsed hook JSON from stdin.
        state_file: Path to the state JSON file (defaults to $ORO_HOME/bead_notify_state.json).
        now: Current timestamp in seconds (defaults to time.time()).
        window_secs: Debounce window in seconds (default 30).
    """
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
    if not trimmed.startswith("bd create"):
        return

    if state_file is None:
        state_file = _default_state_file()
    if now is None:
        now = time.time()

    state = load_state(state_file)

    # If window expired and there are pending beads → flush them first
    if should_notify(state, now, window_secs):
        message = format_batch_notification(state)
        notify_manager(message)
        # Clear accumulated beads, start fresh
        state = dict(_EMPTY_STATE)

    # Extract bead id from tool output (PostToolUse includes tool_output).
    # Fall back to extracting title from command if output is not available.
    tool_output = hook_input.get("tool_response", hook_input.get("tool_output", ""))
    if isinstance(tool_output, dict):
        tool_output = tool_output.get("output", "")
    bead_id = extract_bead_id_from_output(str(tool_output))
    bead_title = _extract_title_from_command(trimmed)

    state = record_bead_create(bead_id, bead_title, now, state)
    save_state(state, state_file)


def _extract_title_from_command(command: str) -> str:
    """Extract --title value from a bd create command string.

    Returns empty string if not found.
    """
    import re

    # Match --title='...' or --title="..." or --title=...
    m = re.search(r"--title[= ]'([^']*)'|--title[= ]\"([^\"]*)\"|--title[= ](\S+)", command)
    if not m:
        return ""
    return (m.group(1) or m.group(2) or m.group(3) or "").strip()


def extract_bead_id_from_output(tool_output: str) -> str:
    """Extract bead ID from bd create tool output.

    Parses lines like '✓ Created issue: oro-xyz' or 'Created issue: oro-xyz'.
    Returns empty string if not found.
    """
    import re

    m = re.search(r"Created issue:\s+([\w.-]+)", tool_output)
    if m:
        return m.group(1).strip()
    return ""


def main() -> None:
    try:
        hook_input = json.loads(sys.stdin.read())
    except (json.JSONDecodeError, EOFError):
        return

    handle_post_tool_use(hook_input)
    # PostToolUse hooks don't output anything - they run for side effects only


if __name__ == "__main__":
    main()
