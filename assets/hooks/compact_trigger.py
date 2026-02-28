#!/usr/bin/env python3
"""PostToolUse hook: trigger /compact when context percentage reaches threshold.

Reads the context percentage written by context_pct_writer.py from the role's
pane file. When percentage meets or exceeds the model-specific threshold from
thresholds.json, sends /compact to the current tmux pane. A debounce file
prevents repeated triggers within the same session.

Input: JSON on stdin with model_key and other PostToolUse fields.
Output: Silent (no stdout). Best-effort.

Environment:
  TMUX_PANE: Current tmux pane ID (e.g., %0). Hook is a no-op if absent.
  ORO_WORKER: Set to "1" for worker processes — skips (workers don't self-compact).
  ORO_ROLE: Role name used to locate pane context_pct file. No-op if absent.
"""

import json
import os
import subprocess
import sys
from pathlib import Path

DEFAULT_THRESHOLD = 50
PANES_DIR = os.path.expanduser("~/.oro/panes")
THRESHOLDS_FILE = Path(os.path.expanduser("~/.oro")) / "thresholds.json"


def load_threshold(model_key: str, thresholds_file: Path | None = None) -> int:
    """Load compact threshold for model_key from thresholds.json.

    Args:
        model_key: Model identifier key (e.g., "opus", "sonnet", "haiku").
        thresholds_file: Path to thresholds JSON. Defaults to THRESHOLDS_FILE.

    Returns:
        Threshold percentage. Falls back to DEFAULT_THRESHOLD if key not found
        or file is missing/invalid.
    """
    if thresholds_file is None:
        thresholds_file = THRESHOLDS_FILE
    try:
        thresholds = json.loads(thresholds_file.read_text())
        return thresholds.get(model_key, DEFAULT_THRESHOLD)
    except (OSError, json.JSONDecodeError):
        return DEFAULT_THRESHOLD


def main() -> None:
    """Main entry point."""
    # Guard: must be running in a tmux pane
    pane = os.getenv("TMUX_PANE")
    if not pane:
        return

    # Guard: workers don't self-compact
    if os.getenv("ORO_WORKER") == "1":
        return

    # Guard: must have a role to locate pct_file
    role = os.getenv("ORO_ROLE")
    if not role:
        return

    # Read current context percentage from pane file
    pct_file = Path(PANES_DIR) / role / "context_pct"
    try:
        pct = int(pct_file.read_text().strip())
    except (OSError, ValueError):
        return

    # Read model_key from stdin hook input
    try:
        hook_input = json.loads(sys.stdin.read())
    except (json.JSONDecodeError, EOFError):
        hook_input = {}
    model_key = hook_input.get("model_key", "sonnet")

    # Load threshold for this model
    threshold = load_threshold(model_key)

    # Not yet at threshold — nothing to do
    if pct < threshold:
        return

    # Check debounce: avoid triggering multiple times
    debounce_file = Path(PANES_DIR) / role / "compact_debounce"
    if debounce_file.exists():
        return

    # Trigger /compact in the current pane via tmux send-keys
    result = subprocess.run(
        ["tmux", "send-keys", "-t", pane, "/compact", "Enter"],
        capture_output=True,
    )

    # Write debounce file only if tmux succeeded
    if result.returncode == 0:
        debounce_file.touch()


if __name__ == "__main__":
    main()
