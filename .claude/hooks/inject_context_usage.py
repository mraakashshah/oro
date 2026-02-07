#!/usr/bin/env python3
"""PreToolUse hook: monitor context usage and inject decomposition warnings.

Reads the Claude Code transcript to find the most recent token usage,
calculates context consumption as a percentage, and injects warnings
when the agent should decompose remaining work into sub-beads.

Input: JSON on stdin with transcript_path, tool_name, tool_input, etc.
Output: JSON with additionalContext when threshold exceeded, nothing otherwise.
"""

import json
import sys

CONTEXT_WINDOW = 200_000
WARN_THRESHOLD = 0.30  # 30%
CRITICAL_THRESHOLD = 0.40  # 40%

WARN_MESSAGE = (
    "<IMPORTANT>\n"
    "Context usage is at {pct}% ({used:,}/{total:,} tokens). "
    "You are approaching context limits.\n\n"
    "ACTION REQUIRED:\n"
    "1. Identify remaining work not yet completed\n"
    "2. Create bd issues (beads) for each remaining item\n"
    "3. Add dependencies between them\n"
    "4. Complete your current task, commit, and hand off\n\n"
    "Do NOT start new multi-step work. Finish what you're doing and decompose the rest.\n"
    "</IMPORTANT>"
)

CRITICAL_MESSAGE = (
    "<EXTREMELY_IMPORTANT>\n"
    "Context usage is at {pct}% ({used:,}/{total:,} tokens). "
    "Context exhaustion is imminent.\n\n"
    "STOP starting new work. You MUST:\n"
    "1. Commit any in-progress changes NOW\n"
    "2. Create bd issues for ALL remaining work\n"
    "3. Run `bd sync --flush-only`\n"
    "4. Create a handoff (invoke `create-handoff` skill)\n\n"
    "Continuing without decomposing will lose context and waste work.\n"
    "</EXTREMELY_IMPORTANT>"
)


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
    except (OSError, IOError):
        return None
    return last_usage


def calculate_context_pct(usage: dict) -> tuple[int, int, float]:
    """Return (used_tokens, total_window, percentage) from a usage dict."""
    used = (
        usage.get("input_tokens", 0)
        + usage.get("cache_creation_input_tokens", 0)
        + usage.get("cache_read_input_tokens", 0)
    )
    return used, CONTEXT_WINDOW, used / CONTEXT_WINDOW


def main() -> None:
    hook_input = json.loads(sys.stdin.read())
    transcript_path = hook_input.get("transcript_path", "")

    if not transcript_path:
        return

    usage = get_last_usage(transcript_path)
    if not usage:
        return

    used, total, pct = calculate_context_pct(usage)

    if pct >= CRITICAL_THRESHOLD:
        message = CRITICAL_MESSAGE.format(pct=int(pct * 100), used=used, total=total)
    elif pct >= WARN_THRESHOLD:
        message = WARN_MESSAGE.format(pct=int(pct * 100), used=used, total=total)
    else:
        return

    output = {
        "hookSpecificOutput": {
            "hookEventName": "PreToolUse",
            "additionalContext": message,
        }
    }
    json.dump(output, sys.stdout)


if __name__ == "__main__":
    main()
