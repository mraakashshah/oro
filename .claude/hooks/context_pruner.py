#!/usr/bin/env python3
# /// script
# requires-python = ">=3.10"
# dependencies = []
# ///
"""PostToolUse hook: nudge model to summarize large tool outputs.

Advisory hook — fires when a tool result exceeds a configurable character
threshold. Injects a gentle reminder to summarize key findings rather than
relying on verbatim content. Debounced to max once per N tool calls.

Reads thresholds from pruning.json at the project root.
"""

from __future__ import annotations

import contextlib
import json
import os
import sys
import time
from pathlib import Path

# Debounce state file
DEBOUNCE_FILE = "/tmp/oro-context-pruner-state"

# Defaults when pruning.json is missing
DEFAULT_THRESHOLDS: dict[str, int] = {"Read": 8000, "Bash": 4000}
DEFAULT_DEBOUNCE_CALLS = 3


def load_config(project_dir: str) -> tuple[dict[str, int], int]:
    """Load pruning config from pruning.json."""
    config_path = Path(project_dir) / "pruning.json"
    if not config_path.exists():
        return DEFAULT_THRESHOLDS, DEFAULT_DEBOUNCE_CALLS

    try:
        config = json.loads(config_path.read_text())
    except (json.JSONDecodeError, OSError):
        return DEFAULT_THRESHOLDS, DEFAULT_DEBOUNCE_CALLS

    debounce = config.pop("debounce_calls", DEFAULT_DEBOUNCE_CALLS)
    # Remaining keys are tool-name → threshold mappings
    thresholds = {k: v for k, v in config.items() if isinstance(v, int)}
    return thresholds or DEFAULT_THRESHOLDS, debounce


def should_debounce(debounce_calls: int) -> bool:
    """Check if we should suppress this nudge (fired too recently)."""
    try:
        data = Path(DEBOUNCE_FILE).read_text().strip()
        parts = data.split(":")
        if len(parts) == 2:
            last_time = float(parts[0])
            call_count = int(parts[1])
            # Reset if more than 60s old
            if time.time() - last_time > 60:
                return False
            return call_count < debounce_calls
    except (OSError, ValueError):
        pass
    return False


def record_fire() -> None:
    """Record that we fired a nudge."""
    with contextlib.suppress(OSError):
        Path(DEBOUNCE_FILE).write_text(f"{time.time():.0f}:0")


def increment_counter() -> None:
    """Increment the call counter (called when we suppress)."""
    with contextlib.suppress(OSError, ValueError):
        data = Path(DEBOUNCE_FILE).read_text().strip()
        parts = data.split(":")
        if len(parts) == 2:
            ts = parts[0]
            count = int(parts[1]) + 1
            Path(DEBOUNCE_FILE).write_text(f"{ts}:{count}")


def main() -> None:
    """Main hook entry point."""
    try:
        input_data = json.load(sys.stdin)
    except (json.JSONDecodeError, EOFError):
        print(json.dumps({}))
        return

    tool_name = input_data.get("tool_name", "")
    tool_result = input_data.get("tool_result", "")

    # Get result length
    if isinstance(tool_result, str):
        result_len = len(tool_result)
    elif isinstance(tool_result, dict):
        result_len = len(json.dumps(tool_result))
    else:
        print(json.dumps({}))
        return

    # Load config
    project_dir = os.environ.get("CLAUDE_PROJECT_DIR", os.getcwd())
    thresholds, debounce_calls = load_config(project_dir)

    # Check if this tool's output exceeds threshold
    threshold = thresholds.get(tool_name, 0)
    if threshold == 0 or result_len <= threshold:
        increment_counter()
        print(json.dumps({}))
        return

    # Check debounce
    if should_debounce(debounce_calls):
        increment_counter()
        print(json.dumps({}))
        return

    # Fire the nudge
    record_fire()
    msg = (
        f"Large tool output ({result_len} chars). "
        f"Summarize key findings in your response rather than "
        f"relying on verbatim content — keeps context lean for future steps."
    )
    print(json.dumps({"additionalContext": msg}))


if __name__ == "__main__":
    main()
