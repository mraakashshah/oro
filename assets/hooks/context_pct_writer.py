#!/usr/bin/env python3
"""PostToolUse hook: write context percentage to pane file.

Reads the Claude Code transcript to find the most recent token usage,
calculates context consumption as a percentage, and writes it to:
- ~/.oro/panes/<role>/context_pct for dispatcher pane polling (when ORO_ROLE set)
- CWD/.oro/context_pct for Go worker hard-stop enforcement (when ORO_WORKER=1)

Input: JSON on stdin with transcript_path, tool_name, tool_input, etc.
Output: Silent (no stdout). Best-effort file write.

Environment:
  ORO_ROLE: Role name (architect/manager/main). Writes to pane file when set.
    Defaults to "main" when neither ORO_ROLE nor ORO_WORKER is set.
  ORO_WORKER: Set to "1" for worker processes. Writes to CWD/.oro/context_pct.

Performance: <10ms (no network, no subprocess spawning).
"""

import contextlib
import json
import os
import sys
from pathlib import Path

DEFAULT_CONTEXT_WINDOW = 1_000_000  # Fallback: assume largest context (Opus)
PANES_DIR = os.path.expanduser("~/.oro/panes")
ORO_HOME = Path(os.environ.get("ORO_HOME", os.path.expanduser("~/.oro")))
BUDGETS_FILE = ORO_HOME / "context_budgets.json"


def load_budget_from_config(model_key: str, config_path: Path | None = None) -> int:
    """Load token budget for model_key from context_budgets.json.

    Args:
        model_key: Model identifier key (e.g., "1m_beta", "default").
        config_path: Path to config JSON. Defaults to BUDGETS_FILE.

    Returns:
        Token budget. Falls back to config "default" if key not found,
        then to DEFAULT_CONTEXT_WINDOW if file is missing or invalid.
    """
    if config_path is None:
        config_path = BUDGETS_FILE
    try:
        budgets = json.loads(config_path.read_text())
        return budgets.get(model_key, budgets.get("default", DEFAULT_CONTEXT_WINDOW))
    except (OSError, json.JSONDecodeError):
        return DEFAULT_CONTEXT_WINDOW


# Known context windows by model family. Used when context_budgets.json is
# missing or doesn't have the model. Keys are substrings matched against the
# full model ID from the transcript (e.g. "claude-opus-4" matches
# "claude-opus-4-20260301").
MODEL_BUDGETS = {
    "opus": 1_000_000,
    "sonnet": 200_000,
    "haiku": 200_000,
}


def get_last_usage(transcript_path: str) -> tuple[dict | None, str]:
    """Read transcript JSONL and return (usage_dict, model_id) from the last assistant message."""
    last_usage = None
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
                if isinstance(msg, dict) and msg.get("usage"):
                    last_usage = msg["usage"]
                    last_model = msg.get("model", last_model)
    except OSError:
        return None, ""
    return last_usage, last_model


def budget_for_model(model_id: str) -> int:
    """Return context window budget for a model ID.

    Priority: exact key in context_budgets.json → MODEL_BUDGETS family match
    → DEFAULT_CONTEXT_WINDOW.
    """
    # Try exact match in config file (skip "default" fallback)
    try:
        budgets = json.loads(BUDGETS_FILE.read_text())
        if model_id in budgets:
            return budgets[model_id]
    except (OSError, json.JSONDecodeError):
        pass

    # Match by model family substring
    model_lower = model_id.lower()
    for family, budget in MODEL_BUDGETS.items():
        if family in model_lower:
            return budget

    return DEFAULT_CONTEXT_WINDOW


def calculate_context_pct(usage: dict, budget: int) -> int:
    """Return context percentage (0-100) from a usage dict.

    Args:
        usage: Usage dict from transcript with token counts
        budget: Token budget limit (e.g., 200_000 or 1_000_000)

    Returns:
        Context percentage clamped to 0-100
    """
    used = (
        usage.get("input_tokens", 0)
        + usage.get("cache_creation_input_tokens", 0)
        + usage.get("cache_read_input_tokens", 0)
    )
    pct = int((used / budget) * 100)
    return min(max(pct, 0), 100)  # clamp to 0-100


def main() -> None:
    """Main entry point."""
    role = os.getenv("ORO_ROLE")
    is_worker = os.getenv("ORO_WORKER") == "1"

    # Default to "main" role for interactive sessions (no ORO_ROLE, no ORO_WORKER)
    if not role and not is_worker:
        role = "main"

    hook_input = json.loads(sys.stdin.read())
    transcript_path = hook_input.get("transcript_path", "")

    if not transcript_path:
        return

    usage, model_id = get_last_usage(transcript_path)
    if not usage:
        return

    # Budget lookup: hook_input["budget"] overrides; otherwise detect from transcript model
    budget = hook_input.get("budget") or budget_for_model(model_id)

    pct = calculate_context_pct(usage, budget)

    # Best-effort write to ~/.oro/panes/<role>/context_pct (pane processes)
    if role:
        pane_dir = Path(PANES_DIR) / role
        context_file = pane_dir / "context_pct"
        with contextlib.suppress(OSError):
            pane_dir.mkdir(parents=True, exist_ok=True)
            context_file.write_text(f"{pct}\n")

    # Best-effort write to CWD/.oro/context_pct (worker processes)
    if is_worker:
        oro_dir = Path.cwd() / ".oro"
        with contextlib.suppress(OSError):
            oro_dir.mkdir(parents=True, exist_ok=True)
            (oro_dir / "context_pct").write_text(f"{pct}\n")


if __name__ == "__main__":
    main()
