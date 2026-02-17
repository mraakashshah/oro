#!/usr/bin/env python3
"""PreToolUse hook: monitor context usage and inject decomposition warnings.

Reads the Claude Code transcript to find the most recent token usage,
calculates context consumption as a percentage, and injects warnings
when the agent should compact or hand off.

Thresholds are loaded from thresholds.json at the project root.
Each model has a single threshold (percentage). Default is 50%.

When the threshold is breached:
- If .oro/compacted does NOT exist: tell agent to run /compact
- If .oro/compacted EXISTS: tell agent to hand off

Input: JSON on stdin with transcript_path, tool_name, tool_input, etc.
Output: JSON with additionalContext when threshold exceeded, nothing otherwise.
"""

import contextlib
import json
import subprocess
import sys
import time
from pathlib import Path

DEFAULT_CONTEXT_WINDOW = 200_000  # Fallback if budget not provided
DEBOUNCE_FILE = "/tmp/oro-context-warn-ts"
DEBOUNCE_SECONDS = 60

DEFAULT_THRESHOLD = 0.50

COMPACT_MESSAGE = (
    "<IMPORTANT>\n"
    "Context usage is at {pct}% ({used:,}/{total:,} tokens). "
    "You are approaching context limits.\n\n"
    "ACTION REQUIRED: Run /compact to free up context space.\n"
    "Do NOT start new multi-step work until you have compacted.\n"
    "</IMPORTANT>"
)

HANDOFF_MESSAGE = (
    "<EXTREMELY_IMPORTANT>\n"
    "Context usage is at {pct}% ({used:,}/{total:,} tokens). "
    "Context exhaustion is imminent and compaction has already been done.\n\n"
    "STOP starting new work. You MUST:\n"
    "1. Commit any in-progress changes NOW (pre-commit hook auto-syncs beads)\n"
    "2. Create bd issues for ALL remaining work\n"
    "3. Create a handoff (invoke `create-handoff` skill)\n\n"
    "Continuing without handing off will lose context and waste work.\n"
    "</EXTREMELY_IMPORTANT>"
)


def _find_project_root() -> str:
    """Find the project root via git rev-parse, falling back to cwd."""
    try:
        result = subprocess.run(
            ["git", "rev-parse", "--show-toplevel"],
            capture_output=True,
            text=True,
            timeout=5,
        )
        if result.returncode == 0 and result.stdout.strip():
            return result.stdout.strip()
    except (OSError, subprocess.TimeoutExpired):
        pass
    return str(Path.cwd())


def load_thresholds(project_root: str) -> dict[str, float]:
    """Load per-model thresholds from thresholds.json at the project root.

    The file maps model family names to integer percentages (e.g. {"opus": 65}).
    Returns a dict mapping model names to float fractions (e.g. {"opus": 0.65}).
    Returns empty dict if file is missing or malformed.
    """
    thresholds_path = Path(project_root) / "thresholds.json"
    try:
        raw = json.loads(thresholds_path.read_text())
    except (OSError, json.JSONDecodeError):
        return {}
    if not isinstance(raw, dict):
        return {}
    return {k: v / 100.0 for k, v in raw.items() if isinstance(v, (int, float))}


def detect_model(transcript_path: str) -> str:
    """Detect model family from transcript. Returns 'opus', 'sonnet', or 'haiku'."""
    last_model = ""
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
                if isinstance(msg, dict) and msg.get("model"):
                    last_model = msg["model"]
    except OSError:
        pass
    lower = last_model.lower()
    if "opus" in lower:
        return "opus"
    if "sonnet" in lower:
        return "sonnet"
    if "haiku" in lower:
        return "haiku"
    return "opus"  # default assumption


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


def calculate_context_pct(usage: dict, budget: int) -> tuple[int, int, float]:
    """Return (used_tokens, total_window, percentage) from a usage dict.

    Args:
        usage: Usage dict from transcript with token counts
        budget: Token budget limit (e.g., 200_000 or 1_000_000)

    Returns:
        Tuple of (used_tokens, budget, percentage)
    """
    used = (
        usage.get("input_tokens", 0)
        + usage.get("cache_creation_input_tokens", 0)
        + usage.get("cache_read_input_tokens", 0)
    )
    return used, budget, used / budget


def main() -> None:
    hook_input = json.loads(sys.stdin.read())
    transcript_path = hook_input.get("transcript_path", "")

    if not transcript_path:
        return

    usage = get_last_usage(transcript_path)
    if not usage:
        return

    # Read budget from hook input, fallback to default if not provided
    budget = hook_input.get("budget", DEFAULT_CONTEXT_WINDOW)

    used, total, pct = calculate_context_pct(usage, budget)

    model = detect_model(transcript_path)
    project_root = _find_project_root()
    thresholds = load_thresholds(project_root)
    threshold = thresholds.get(model, DEFAULT_THRESHOLD)

    if pct < threshold:
        return

    # Check for .oro/compacted flag
    compacted_flag = Path(project_root) / ".oro" / "compacted"
    if compacted_flag.is_file():
        message = HANDOFF_MESSAGE.format(pct=int(pct * 100), used=used, total=total)
    else:
        message = COMPACT_MESSAGE.format(pct=int(pct * 100), used=used, total=total)

    # Debounce: skip if we warned less than DEBOUNCE_SECONDS ago
    debounce = Path(DEBOUNCE_FILE)
    try:
        if debounce.is_file():
            last = float(debounce.read_text().strip())
            if time.time() - last < DEBOUNCE_SECONDS:
                return
    except (OSError, ValueError):
        pass
    with contextlib.suppress(OSError):
        debounce.write_text(str(time.time()))

    output = {
        "hookSpecificOutput": {
            "hookEventName": "PreToolUse",
            "additionalContext": message,
        }
    }
    json.dump(output, sys.stdout)


if __name__ == "__main__":
    main()
