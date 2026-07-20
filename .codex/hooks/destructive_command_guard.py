#!/usr/bin/env python3
"""PreToolUse hook: block known destructive Bash commands."""

from __future__ import annotations

import json
import re
import shlex
import sys

_CHAIN_RE = re.compile(r"\s*(?:&&|\|\||;|\n)+\s*")

_DANGEROUS_PREFIXES = (
    ("rm",),
    ("rmdir",),
    ("unlink",),
    ("git", "reset"),
    ("git", "checkout"),
    ("git", "clean"),
    ("git", "rebase"),
)

_NONINTERACTIVE_COMMIT_LONG_OPTS = (
    "--message",
    "--file",
    "--reuse-message",
    "--fixup",
)


def _has_option(tokens: list[str], *options: str) -> bool:
    return any(token in options for token in tokens)


def _is_force_push(tokens: list[str]) -> bool:
    if len(tokens) < 2 or tokens[:2] != ["git", "push"]:
        return False
    if _has_option(tokens[2:], "--force", "--force-with-lease"):
        return True
    return any(token.startswith("-") and "f" in token[1:] for token in tokens[2:] if token != "--")


def _commit_has_noninteractive_message(tokens: list[str]) -> bool:
    args = tokens[2:]
    for token in args:
        if token in {"--no-edit", "-m", "--message", "-F", "--file", "-C", "--reuse-message", "--fixup"}:
            return True
        if any(token.startswith(opt + "=") for opt in _NONINTERACTIVE_COMMIT_LONG_OPTS):
            return True
        if token.startswith("-") and not token.startswith("--"):
            flags = token[1:]
            if any(flag in flags for flag in ("m", "F", "C")):
                return True
    return False


def _is_interactive_commit(tokens: list[str]) -> bool:
    if len(tokens) < 2 or tokens[:2] != ["git", "commit"]:
        return False
    if _has_option(tokens[2:], "--edit", "-e"):
        return True
    return not _commit_has_noninteractive_message(tokens)


def _classify_tokens(tokens: list[str]) -> str | None:
    if not tokens:
        return None

    if tokens[0] in {"rm", "rmdir", "unlink"}:
        return tokens[0]

    if len(tokens) < 2 or tokens[0] != "git":
        return None

    subcommand = tokens[1]
    if subcommand in {"reset", "checkout", "clean", "rebase"}:
        return f"git {subcommand}"
    if subcommand == "commit" and _has_option(tokens[2:], "--amend"):
        return "git commit --amend"
    if _is_interactive_commit(tokens):
        return "interactive git commit"
    if subcommand == "branch" and _has_option(tokens[2:], "-D"):
        return "git branch -D"
    if _is_force_push(tokens):
        return "git push --force"
    return None


def _classify_malformed(command_part: str) -> str | None:
    words = command_part.strip().split()
    for prefix in _DANGEROUS_PREFIXES:
        if tuple(words[: len(prefix)]) == prefix:
            return " ".join(prefix)
    if len(words) >= 3 and words[:2] == ["git", "commit"] and "--amend" in words[2:]:
        return "git commit --amend"
    if len(words) >= 2 and words[:2] == ["git", "commit"] and _is_interactive_commit(words):
        return "interactive git commit"
    if len(words) >= 3 and words[:2] == ["git", "branch"] and "-D" in words[2:]:
        return "git branch -D"
    if _is_force_push(words):
        return "git push --force"
    return None


def _classify_part(command_part: str) -> str | None:
    try:
        tokens = shlex.split(command_part)
    except ValueError:
        return _classify_malformed(command_part)

    return _classify_tokens(tokens)


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

    reason = (
        f"BLOCKED: Bash command classified as destructive ({label}). "
        "Use a safer command or ask the user for explicit destructive-command approval."
    )
    return {
        "hookSpecificOutput": {
            "hookEventName": "PreToolUse",
            "permissionDecision": "deny",
            "permissionDecisionReason": reason,
        },
        "systemMessage": reason,
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
