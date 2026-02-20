#!/usr/bin/env python3
"""Tests for context_pct_writer.py PostToolUse hook."""

import json
import os
import tempfile
from pathlib import Path
from unittest.mock import patch

import pytest
from context_pct_writer import main


@pytest.fixture
def temp_dirs():
    """Create temporary directories for testing."""
    with tempfile.TemporaryDirectory() as tmpdir:
        panes_dir = Path(tmpdir) / "panes"
        panes_dir.mkdir()
        yield {"panes": panes_dir, "root": Path(tmpdir)}


@pytest.fixture
def mock_transcript(tmp_path):
    """Create a mock transcript file with usage data."""
    transcript = tmp_path / "transcript.jsonl"
    # Simulate a transcript with token usage
    lines = [
        json.dumps(
            {
                "message": {
                    "model": "claude-opus-4",
                    "usage": {
                        "input_tokens": 50000,
                        "cache_creation_input_tokens": 10000,
                        "cache_read_input_tokens": 5000,
                    },
                }
            }
        ),
        json.dumps(
            {
                "message": {
                    "model": "claude-opus-4",
                    "usage": {
                        "input_tokens": 80000,
                        "cache_creation_input_tokens": 15000,
                        "cache_read_input_tokens": 5000,
                    },
                }
            }
        ),
    ]
    transcript.write_text("\n".join(lines) + "\n")
    return str(transcript)


def test_no_op_when_oro_role_unset(temp_dirs, mock_transcript, monkeypatch, capsys):
    """When ORO_ROLE is unset, hook should no-op."""
    monkeypatch.delenv("ORO_ROLE", raising=False)

    hook_input = {
        "transcript_path": mock_transcript,
        "tool_name": "Write",
        "tool_input": {"file_path": "/tmp/test.txt"},
    }

    with patch("sys.stdin.read", return_value=json.dumps(hook_input)):
        main()

    # Should produce no output and write no files
    captured = capsys.readouterr()
    assert captured.out == ""
    assert not list(temp_dirs["panes"].iterdir())


def test_writes_context_pct_to_pane_file(temp_dirs, mock_transcript, monkeypatch, capsys):
    """When ORO_ROLE is set, writes context_pct to ~/.oro/panes/<role>/context_pct."""
    panes_dir = temp_dirs["panes"]
    monkeypatch.setenv("ORO_ROLE", "architect")

    hook_input = {
        "transcript_path": mock_transcript,
        "tool_name": "Write",
        "tool_input": {"file_path": "/tmp/test.txt"},
    }

    with (
        patch("sys.stdin.read", return_value=json.dumps(hook_input)),
        patch("context_pct_writer.PANES_DIR", str(panes_dir)),
    ):
        main()

    # Should write integer 0-100 to panes/architect/context_pct
    pane_file = panes_dir / "architect" / "context_pct"
    assert pane_file.exists()
    content = pane_file.read_text().strip()
    assert content.isdigit()
    pct = int(content)
    assert 0 <= pct <= 100
    # 100000 / 200000 = 50%
    assert pct == 50


def test_creates_directory_if_missing(temp_dirs, mock_transcript, monkeypatch):
    """Creates ~/.oro/panes/<role>/ if it doesn't exist."""
    panes_dir = temp_dirs["panes"]
    monkeypatch.setenv("ORO_ROLE", "manager")

    hook_input = {
        "transcript_path": mock_transcript,
        "tool_name": "Bash",
        "tool_input": {"command": "echo test"},
    }

    with (
        patch("sys.stdin.read", return_value=json.dumps(hook_input)),
        patch("context_pct_writer.PANES_DIR", str(panes_dir)),
    ):
        main()

    pane_file = panes_dir / "manager" / "context_pct"
    assert pane_file.exists()


def test_no_op_when_transcript_missing(temp_dirs, monkeypatch, capsys):
    """No-op when transcript_path is not in input."""
    monkeypatch.setenv("ORO_ROLE", "architect")

    hook_input = {
        "tool_name": "Write",
        "tool_input": {"file_path": "/tmp/test.txt"},
    }

    with (
        patch("sys.stdin.read", return_value=json.dumps(hook_input)),
        patch("context_pct_writer.PANES_DIR", str(temp_dirs["panes"])),
    ):
        main()

    captured = capsys.readouterr()
    assert captured.out == ""
    assert not list(temp_dirs["panes"].iterdir())


def test_no_op_when_usage_unavailable(temp_dirs, tmp_path, monkeypatch, capsys):
    """No-op when transcript has no usage data."""
    monkeypatch.setenv("ORO_ROLE", "architect")

    transcript = tmp_path / "empty.jsonl"
    transcript.write_text("{}\n")

    hook_input = {
        "transcript_path": str(transcript),
        "tool_name": "Read",
        "tool_input": {"file_path": "/tmp/test.txt"},
    }

    with (
        patch("sys.stdin.read", return_value=json.dumps(hook_input)),
        patch("context_pct_writer.PANES_DIR", str(temp_dirs["panes"])),
    ):
        main()

    captured = capsys.readouterr()
    assert captured.out == ""
    assert not list(temp_dirs["panes"].iterdir())


def test_silent_on_write_errors(temp_dirs, mock_transcript, monkeypatch, capsys):
    """Silent failure when directory is not writable."""
    panes_dir = temp_dirs["panes"]
    monkeypatch.setenv("ORO_ROLE", "architect")

    # Make directory read-only
    os.chmod(panes_dir, 0o444)

    hook_input = {
        "transcript_path": mock_transcript,
        "tool_name": "Write",
        "tool_input": {"file_path": "/tmp/test.txt"},
    }

    try:
        with (
            patch("sys.stdin.read", return_value=json.dumps(hook_input)),
            patch("context_pct_writer.PANES_DIR", str(panes_dir)),
        ):
            main()

        # Should not raise, produce no output
        captured = capsys.readouterr()
        assert captured.out == ""
    finally:
        os.chmod(panes_dir, 0o755)


def test_overwrites_previous_value(temp_dirs, mock_transcript, monkeypatch):
    """Overwrites previous context_pct value."""
    panes_dir = temp_dirs["panes"]
    monkeypatch.setenv("ORO_ROLE", "architect")

    # Pre-create file with old value
    pane_dir = panes_dir / "architect"
    pane_dir.mkdir()
    pane_file = pane_dir / "context_pct"
    pane_file.write_text("25")

    hook_input = {
        "transcript_path": mock_transcript,
        "tool_name": "Edit",
        "tool_input": {"file_path": "/tmp/test.txt"},
    }

    with (
        patch("sys.stdin.read", return_value=json.dumps(hook_input)),
        patch("context_pct_writer.PANES_DIR", str(panes_dir)),
    ):
        main()

    # Should overwrite with new value
    assert pane_file.read_text().strip() == "50"
