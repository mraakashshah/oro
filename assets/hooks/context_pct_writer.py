#!/usr/bin/env python3
"""PostToolUse hook: write context percentage to pane file.

Reads the Claude Code transcript to find the most recent token usage,
calculates context consumption as a percentage, and writes it to
~/.oro/panes/<role>/context_pct for dispatcher polling.

Input: JSON on stdin with transcript_path, tool_name, tool_input, etc.
Output: Silent (no stdout). Best-effort file write.

Environment:
  ORO_ROLE: Role name (architect/manager). No-op if unset.

Performance: <10ms (no network, no subprocess spawning).
"""

import contextlib
import json
import os
import sys
from pathlib import Path

CONTEXT_WINDOW = 200_000
PANES_DIR = os.path.expanduser("~/.oro/panes")


def get_last_usage(transcript_path: str) -> dict | None:
    """Read transcript JSONL and return the usage dict from the last assistant message."""
    last_usage = None
    try:
        with open(transcript_path) as f:
            for line in f:
                line = line.strip()
                if not line:
                    continue
                try:
                    entry = json.loads(line)
                except json.JSONDecodeError:
                    continue
                msg = entry.get("message")
                if isinstance(msg, dict) and msg.get("usage"):
                    last_usage = msg["usage"]
    except OSError:
        return None
    return last_usage


def calculate_context_pct(usage: dict) -> int:
    """Return context percentage (0-100) from a usage dict."""
    used = (
        usage.get("input_tokens", 0)
        + usage.get("cache_creation_input_tokens", 0)
        + usage.get("cache_read_input_tokens", 0)
    )
    pct = int((used / CONTEXT_WINDOW) * 100)
    return min(max(pct, 0), 100)  # clamp to 0-100


def main() -> None:
    """Main entry point."""
    # No-op if ORO_ROLE is not set
    role = os.getenv("ORO_ROLE")
    if not role:
        return

    hook_input = json.loads(sys.stdin.read())
    transcript_path = hook_input.get("transcript_path", "")

    if not transcript_path:
        return

    usage = get_last_usage(transcript_path)
    if not usage:
        return

    pct = calculate_context_pct(usage)

    # Best-effort write to ~/.oro/panes/<role>/context_pct
    pane_dir = Path(PANES_DIR) / role
    context_file = pane_dir / "context_pct"

    with contextlib.suppress(OSError):
        pane_dir.mkdir(parents=True, exist_ok=True)
        context_file.write_text(f"{pct}\n")


if __name__ == "__main__":
    main()
