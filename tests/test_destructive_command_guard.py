"""Tests for destructive_command_guard hook."""

import json
import subprocess
import sys

from destructive_command_guard import build_decision
from test_hook_schemas import _claude_pre_tool_use, _codex_pre_tool_use


def _bash_input(command: str, *, codex: bool = False) -> dict:
    fixture = _codex_pre_tool_use("Bash") if codex else _claude_pre_tool_use("Bash")
    fixture["tool_input"]["command"] = command
    fixture["tool_input"].pop("file_path", None)
    return fixture


def test_destructive_command_guard_blocks_pretooluse_bash_payloads() -> None:
    for hook_input in [
        _bash_input("rm -rf build"),
        _bash_input("git reset --hard HEAD", codex=True),
        _bash_input("git status && rm -rf .pytest_cache", codex=True),
        _bash_input("git status; git branch -D stale-branch"),
        _bash_input("git status || rm -rf tmp\nls -la"),
    ]:
        result = build_decision(hook_input)

        assert result is not None
        assert result["hookSpecificOutput"]["hookEventName"] == "PreToolUse"
        assert result["hookSpecificOutput"]["permissionDecision"] == "deny"
        assert "BLOCKED" in result["systemMessage"]
        assert "destructive" in result["systemMessage"]


def test_destructive_command_guard_allows_non_bash_missing_empty_and_read_only() -> None:
    for hook_input in [
        {"tool_name": "Read", "tool_input": {"command": "rm -rf build"}},
        {"tool_name": "Bash"},
        {"tool_name": "Bash", "tool_input": None},
        {"tool_name": "Bash", "tool_input": []},
        _bash_input(""),
        _bash_input("   "),
        _bash_input("git status && ls -la; pwd || git log --oneline\nfind . -maxdepth 1 -type f"),
    ]:
        assert build_decision(hook_input) is None


def test_destructive_command_guard_writes_no_stdout_for_allowed_commands() -> None:
    hook_input = _bash_input("git status && ls -la")
    completed = subprocess.run(
        [sys.executable, "assets/hooks/destructive_command_guard.py"],
        input=json.dumps(hook_input).encode(),
        capture_output=True,
        check=False,
    )

    assert completed.returncode == 0
    assert completed.stdout == b""
