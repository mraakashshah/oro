#!/usr/bin/env python3
"""Stop hook: block worker exit at hard context until handoff is complete."""

from __future__ import annotations

import json
import sys
from pathlib import Path

HARD_THRESHOLD = 50
CONTEXT_PCT_FILE = Path(".oro") / "context_pct"
HANDOFF_DONE_FILE = Path(".oro") / "handoff_done"


def hard_threshold() -> int:
    """Return the hard stop context threshold percentage."""
    return HARD_THRESHOLD


def read_context_pct() -> int | None:
    """Read the latest worker context percentage from the runtime file."""
    try:
        return int(CONTEXT_PCT_FILE.read_text().strip())
    except (OSError, ValueError):
        return None


def handoff_exists() -> bool:
    """Return true when the worker has marked handoff completion."""
    return HANDOFF_DONE_FILE.exists()


def decide(stdin: dict) -> dict:
    """Return a Stop hook block decision when context is too full."""
    if not isinstance(stdin, dict) or not stdin:
        return {}
    if stdin.get("stop_hook_active") is True:
        return {}
    if handoff_exists():
        return {}

    pct = read_context_pct()
    if pct is None or pct < hard_threshold():
        return {}

    return {
        "decision": "block",
        "reason": (
            f"Context is at {pct}% (hard threshold {hard_threshold()}%). "
            "Invoke the create-handoff skill, complete the handoff, then create "
            ".oro/handoff_done before stopping so the next worker can re-enter cleanly."
        ),
    }


def main() -> None:
    """Read hook stdin and write a Stop hook decision."""
    try:
        hook_input = json.loads(sys.stdin.read() or "{}")
    except (json.JSONDecodeError, EOFError):
        return
    if not isinstance(hook_input, dict):
        return

    decision = decide(hook_input)
    if decision:
        json.dump(decision, sys.stdout)


if __name__ == "__main__":
    main()
