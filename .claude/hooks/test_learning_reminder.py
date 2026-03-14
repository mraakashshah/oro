"""Tests for learning_reminder.py — PostToolUse hook for memory reminders on git commit."""

import json
import os
import subprocess
import sys
from pathlib import Path

HOOK_SCRIPT = str(Path(__file__).parent / "learning_reminder.py")

SAMPLE_MEMORIES = [
    {"id": 1, "type": "gotcha", "content": "Use absolute paths for worktrees"},
    {"id": 2, "type": "lesson", "content": "Always run quality gate before commit"},
]


def _make_fake_oro(tmp_path: Path, memories: list[dict]) -> str:
    """Create a fake `oro` script that outputs given memories as JSON."""
    fake_oro = tmp_path / "oro"
    fake_oro.write_text(f"#!/bin/sh\necho '{json.dumps(memories)}'\n")
    fake_oro.chmod(0o755)
    return str(tmp_path)


def _run_hook(hook_input: dict, fake_bin_dir: str | None = None) -> dict | None:
    """Run the learning_reminder.py hook with given input, return parsed JSON output or None."""
    env = {**os.environ}
    if fake_bin_dir:
        env["PATH"] = fake_bin_dir + ":" + env.get("PATH", "")

    result = subprocess.run(
        [sys.executable, HOOK_SCRIPT],
        input=json.dumps(hook_input),
        capture_output=True,
        text=True,
        env=env,
    )

    stdout = result.stdout.strip()
    if not stdout:
        return None
    return json.loads(stdout)


# --- Tests ---


def test_git_commit_surfaces_memories(tmp_path: Path) -> None:
    """Git commit injects reminder when recent memories exist."""
    fake_bin = _make_fake_oro(tmp_path, SAMPLE_MEMORIES)

    hook_input = {
        "tool_name": "Bash",
        "tool_input": {"command": 'git commit -m "feat(hooks): update hook"'},
    }

    output = _run_hook(hook_input, fake_bin)

    assert output is not None
    context = output["hookSpecificOutput"]["additionalContext"]
    assert "2 recent memories" in context
    assert "absolute paths" in context
    assert "quality gate" in context


def test_git_commit_no_memories_produces_no_output() -> None:
    """Git commit with no memories (oro not on PATH) produces no output."""
    hook_input = {
        "tool_name": "Bash",
        "tool_input": {"command": 'git commit -m "feat: something"'},
    }

    # Remove oro from PATH entirely by using an empty temp dir
    env = {**os.environ, "PATH": "/nonexistent"}
    result = subprocess.run(
        [sys.executable, HOOK_SCRIPT],
        input=json.dumps(hook_input),
        capture_output=True,
        text=True,
        env=env,
    )
    assert result.stdout.strip() == ""


def test_non_commit_command_no_output(tmp_path: Path) -> None:
    """Non-commit bash commands produce no output."""
    fake_bin = _make_fake_oro(tmp_path, SAMPLE_MEMORIES)

    hook_input = {
        "tool_name": "Bash",
        "tool_input": {"command": "ls -la"},
    }

    output = _run_hook(hook_input, fake_bin)
    assert output is None


def test_non_bash_tool_no_output(tmp_path: Path) -> None:
    """Non-Bash tool_name produces no output."""
    fake_bin = _make_fake_oro(tmp_path, SAMPLE_MEMORIES)

    hook_input = {
        "tool_name": "Read",
        "tool_input": {"file_path": "/some/file.py"},
    }

    output = _run_hook(hook_input, fake_bin)
    assert output is None


def test_single_memory_uses_singular_form(tmp_path: Path) -> None:
    """Single memory uses singular 'memory' in the message."""
    fake_bin = _make_fake_oro(tmp_path, [SAMPLE_MEMORIES[0]])

    hook_input = {
        "tool_name": "Bash",
        "tool_input": {"command": 'git commit -m "fix: something"'},
    }

    output = _run_hook(hook_input, fake_bin)

    assert output is not None
    context = output["hookSpecificOutput"]["additionalContext"]
    assert "1 recent memory" in context


def test_empty_memories_list_produces_no_output(tmp_path: Path) -> None:
    """Oro returning empty list produces no output."""
    fake_bin = _make_fake_oro(tmp_path, [])

    hook_input = {
        "tool_name": "Bash",
        "tool_input": {"command": 'git commit -m "feat: something"'},
    }

    output = _run_hook(hook_input, fake_bin)
    assert output is None


def test_git_commit_amend_detected(tmp_path: Path) -> None:
    """git commit --amend is also detected as a commit command."""
    fake_bin = _make_fake_oro(tmp_path, [SAMPLE_MEMORIES[0]])

    hook_input = {
        "tool_name": "Bash",
        "tool_input": {"command": "git commit --amend --no-edit"},
    }

    output = _run_hook(hook_input, fake_bin)
    assert output is not None
