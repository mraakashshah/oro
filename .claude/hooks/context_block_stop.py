#!/usr/bin/env python3
"""Stop hook helpers for deterministic context handoff blocking."""

import json
import os
import sys
from pathlib import Path

OVERRIDE_ENV = "ORO_CONTEXT_BLOCK_PCT"


def _read_pct(path: Path) -> int | None:
    try:
        return int(path.read_text().strip())
    except (OSError, ValueError):
        return None


def read_context_pct() -> int | None:
    override = os.getenv(OVERRIDE_ENV)
    if override:
        try:
            return int(override)
        except ValueError:
            return None

    if os.getenv("ORO_WORKER") == "1":
        pct = _read_pct(Path.cwd() / ".oro" / "context_pct")
        if pct is not None:
            return pct

    role = os.getenv("ORO_ROLE")
    if role:
        pane_pct = Path.home() / ".oro" / "panes" / role / "context_pct"
        return _read_pct(pane_pct)

    return None


def handoff_exists() -> bool:
    return (Path.cwd() / ".oro" / "handoff_done").exists()


def decide(stdin: dict) -> dict:
    if stdin.get("stop_hook_active"):
        return {}
    pct = read_context_pct()
    if pct is None:
        return {}
    if handoff_exists():
        return {}
    return {}


def main() -> None:
    try:
        hook_input = json.loads(sys.stdin.read())
    except (json.JSONDecodeError, EOFError):
        return
    out = decide(hook_input)
    if out:
        json.dump(out, sys.stdout)


if __name__ == "__main__":
    main()
