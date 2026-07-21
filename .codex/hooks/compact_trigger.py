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

DEFAULT_THRESHOLD = 40
PANES_DIR = os.path.expanduser("~/.oro/panes")
THRESHOLDS_FILE = Path(os.path.expanduser("~/.oro")) / "thresholds.json"

_KNOWN_TIERS = frozenset({"fast", "balanced", "deep", "background"})
_MODEL_FAMILIES = ("opus", "sonnet", "haiku")


def _load_thresholds(thresholds_file: Path | None = None) -> dict:
    if thresholds_file is None:
        thresholds_file = THRESHOLDS_FILE
    return json.loads(thresholds_file.read_text())


def _model_family(model: str) -> str:
    model = model.lower()
    for family in _MODEL_FAMILIES:
        if family in model:
            return family
    return "balanced"


def _threshold_value(thresholds: dict, key: str) -> int:
    value = thresholds.get(key, DEFAULT_THRESHOLD)
    if isinstance(value, int):
        return value
    return DEFAULT_THRESHOLD


def resolve_tier_threshold(thresholds_file: Path | None = None) -> int:
    try:
        thresholds = _load_thresholds(thresholds_file)
    except (OSError, json.JSONDecodeError):
        return DEFAULT_THRESHOLD

    role = os.getenv("ORO_ROLE", "")
    if role in _KNOWN_TIERS and role in thresholds:
        return _threshold_value(thresholds, role)

    model = os.getenv("ORO_MODEL", "")
    return _threshold_value(thresholds, _model_family(model))


def hard_threshold(thresholds_file: Path | None = None) -> int:
    return resolve_tier_threshold(thresholds_file) + 10


def load_threshold(
    model_key: str,
    thresholds_file: Path | None = None,
    *,
    tier: str = "",
) -> int:
    """Load compact threshold, preferring tier key over legacy model key.

    Args:
        model_key: Model identifier key (e.g., "opus", "sonnet", "haiku").
        thresholds_file: Path to thresholds JSON. Defaults to THRESHOLDS_FILE.
        tier: Bead routing tier (e.g., "fast", "balanced", "deep", "background").
            When set and the tier key exists in thresholds, it wins over model_key.

    Returns:
        Threshold percentage. Priority: known tier key > model_key > DEFAULT_THRESHOLD.
    """
    if thresholds_file is None:
        thresholds_file = THRESHOLDS_FILE
    try:
        thresholds = _load_thresholds(thresholds_file)
        if tier in _KNOWN_TIERS and tier in thresholds:
            return _threshold_value(thresholds, tier)
        return _threshold_value(thresholds, model_key)
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
    tier = hook_input.get("tier", "")

    # Load threshold: tier key preferred when bead routing tier is known
    threshold = load_threshold(model_key, tier=tier)

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
