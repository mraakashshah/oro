#!/usr/bin/env python3
"""PreToolUse hook: inject skills reminder at task boundaries.

Fires before Edit, Write, Agent, Task tool calls. Suppressed for workers
(ORO_WORKER=1). Uses a per-session call counter: fires at 0, then every
WINDOW calls after that — approximates "once per task cluster".
"""

import json
import os
import sys
from pathlib import Path

QUALIFYING_TOOLS = frozenset({"Edit", "Write", "Agent", "Task"})
WINDOW = 12


def state_file(ppid: int) -> Path:
    return Path("/tmp") / f"enforce-skills-{ppid}"


def read_counter(path: Path) -> int:
    try:
        return int(path.read_text().strip())
    except (OSError, ValueError):
        return 0


def write_counter(path: Path, count: int) -> None:
    try:
        path.write_text(str(count))
    except OSError:
        pass


def should_remind(counter: int, window: int = WINDOW) -> bool:
    return counter == 0 or counter % window == 0


def build_reminder() -> dict:
    return {
        "hookSpecificOutput": {
            "hookEventName": "PreToolUse",
            "additionalContext": (
                "SKILLS GATE: Before this action, confirm: have you invoked `using-skills`? "
                "If not, do that first. Even a 1% chance a skill applies means you must check. "
                "Red flags: 'this is simple', 'I know this', 'just continuing' — these are rationalizations."
            ),
        }
    }


def build_decision(hook_input: dict, ppid: int, window: int = WINDOW) -> dict | None:
    if os.environ.get("ORO_WORKER") == "1":
        return None
    if hook_input.get("tool_name", "") not in QUALIFYING_TOOLS:
        return None
    path = state_file(ppid)
    counter = read_counter(path)
    write_counter(path, counter + 1)
    return build_reminder() if should_remind(counter, window) else None


def main() -> None:
    try:
        hook_input = json.loads(sys.stdin.read())
    except (json.JSONDecodeError, EOFError):
        return
    result = build_decision(hook_input, ppid=os.getppid())
    if result:
        json.dump(result, sys.stdout)


if __name__ == "__main__":
    main()
