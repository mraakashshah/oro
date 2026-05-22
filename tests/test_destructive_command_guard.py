"""Tests for destructive_command_guard hook."""

import json
import subprocess
import sys

from destructive_command_guard import build_decision


def _claude_pre_tool_use(tool_name: str = "Read", file_path: str = "/nonexistent/file.go") -> dict:
    return {
        "hook_type": "PreToolUse",
        "tool_name": tool_name,
        "tool_input": {"file_path": file_path},
    }


def _codex_pre_tool_use(tool_name: str = "Read", file_path: str = "/nonexistent/file.go") -> dict:
    return {
        **_claude_pre_tool_use(tool_name, file_path),
        "tool_use_id": "tu_abc123",
        "turn_id": "turn-001",
        "transcript_path": "/nonexistent/transcript.jsonl",
    }


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


def test_destructive_command_guard_blocks_dangerous_commands() -> None:
    dangerous_commands = [
        "rm tmp.txt",
        "rmdir old-dir",
        "unlink stale-link",
        "git reset HEAD~1",
        "git checkout -- src/main.py",
        "git clean -fd",
        "git rebase main",
        "git merge feature",
        "git commit --amend",
        "git branch -D stale-branch",
        "git push --force origin HEAD",
        "git push -f origin HEAD",
        "git status && rm tmp.txt",
        "rm 'two words.txt'",
        "rm",
        "git reset --",
    ]
    malformed_dangerous_commands = [
        "rm 'unterminated",
        "git reset 'unterminated",
    ]

    for command in dangerous_commands + malformed_dangerous_commands:
        result = build_decision(_bash_input(command, codex=True))

        assert result is not None, command
        assert result["hookSpecificOutput"]["hookEventName"] == "PreToolUse"
        assert result["hookSpecificOutput"]["permissionDecision"] == "deny"

    for command in [
        "",
        "git status",
        "git log --oneline",
        "git diff --stat",
        "git branch -d stale-branch",
        'printf "git reset --hard HEAD"',
        'printf "rm tmp.txt"',
    ]:
        assert build_decision(_bash_input(command, codex=True)) is None, command


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
