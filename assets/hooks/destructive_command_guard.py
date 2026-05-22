#!/usr/bin/env python3
"""PreToolUse hook: block known destructive Bash commands."""

from __future__ import annotations

import json
import re
import shlex
import sys

_CHAIN_RE = re.compile(r"\s*(?:&&|\|\||;|\n)+\s*")

_DESTRUCTIVE_PATTERNS: tuple[tuple[re.Pattern[str], str], ...] = (
    (re.compile(r"^git\s+reset\s+--hard(?:\s|$)"), "git reset --hard"),
    (re.compile(r"^git\s+clean\s+.*(?:^|\s)-[^\s]*[fF][^\s]*(?:\s|$)"), "git clean -f"),
    (re.compile(r"^git\s+branch\s+-D(?:\s|$)"), "git branch -D"),
    (re.compile(r"^git\s+checkout\s+.*(?:^|\s)-[^\s]*[fF][^\s]*(?:\s|$)"), "git checkout -f"),
    (re.compile(r"^git\s+restore\s+.*(?:^|\s)--(?:source|staged|worktree)\b"), "git restore destructive"),
)


def _rm_label(tokens: list[str]) -> str | None:
    if not tokens or tokens[0] != "rm":
        return None

    flags = "".join(token[1:] for token in tokens[1:] if token.startswith("-") and token != "--")
    if "r" in flags.lower() and "f" in flags.lower():
        return "rm -rf"
    return None


def _classify_part(command_part: str) -> str | None:
    try:
        tokens = shlex.split(command_part)
    except ValueError:
        tokens = command_part.split()

    label = _rm_label(tokens)
    if label is not None:
        return label

    normalized = command_part.strip()
    for pattern, label in _DESTRUCTIVE_PATTERNS:
        if pattern.search(normalized):
            return label
    return None


def classify_command(command: str) -> str | None:
    """Return a destructive command label, or None when the command is allowed."""
    if not command.strip():
        return None

    for part in _CHAIN_RE.split(command):
        if not part.strip():
            continue
        label = _classify_part(part)
        if label is not None:
            return label
    return None


def build_decision(hook_input: dict) -> dict | None:
    """Return a PreToolUse deny decision for destructive Bash payloads."""
    if hook_input.get("tool_name") != "Bash":
        return None

    tool_input = hook_input.get("tool_input")
    if not isinstance(tool_input, dict):
        return None

    command = tool_input.get("command", "")
    if not isinstance(command, str) or not command.strip():
        return None

    label = classify_command(command)
    if label is None:
        return None

    return {
        "hookSpecificOutput": {
            "hookEventName": "PreToolUse",
            "permissionDecision": "deny",
        },
        "systemMessage": (
            f"BLOCKED: Bash command classified as destructive ({label}). "
            "Use a safer command or ask the user for explicit destructive-command approval."
        ),
    }


def main() -> None:
    try:
        hook_input = json.loads(sys.stdin.read())
    except (json.JSONDecodeError, EOFError):
        return

    result = build_decision(hook_input)
    if result is None:
        return

    json.dump(result, sys.stdout)


if __name__ == "__main__":
    main()
