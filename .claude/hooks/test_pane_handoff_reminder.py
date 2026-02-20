#!/usr/bin/env python3
"""Tests for pane_handoff_reminder.py PreToolUse hook."""

import json
import os
import tempfile
from pathlib import Path
from unittest.mock import patch

import pytest
from pane_handoff_reminder import main


@pytest.fixture
def temp_panes_dir():
    """Create temporary panes directory structure."""
    with tempfile.TemporaryDirectory() as tmpdir:
        panes = Path(tmpdir) / "panes"
        panes.mkdir()
        yield panes


@pytest.fixture
def mock_env_no_role():
    """Mock environment with no ORO_ROLE set."""
    with patch.dict(os.environ, {}, clear=True):
        yield


@pytest.fixture
def mock_env_architect(temp_panes_dir):
    """Mock environment with ORO_ROLE=architect."""
    architect_dir = temp_panes_dir / "architect"
    architect_dir.mkdir()
    with patch.dict(os.environ, {"ORO_ROLE": "architect", "ORO_PANES_DIR": str(temp_panes_dir)}):
        yield architect_dir


def test_no_output_when_role_not_set(mock_env_no_role, capsys):
    """Hook should no-op when ORO_ROLE is not set."""
    hook_input = {
        "hookEventName": "PreToolUse",
        "tool_name": "Read",
        "tool_input": {"file_path": "/some/file.txt"},
    }

    with patch("sys.stdin.read", return_value=json.dumps(hook_input)):
        main()

    captured = capsys.readouterr()
    assert captured.out == ""


def test_no_output_when_signal_file_missing(mock_env_architect, capsys):
    """Hook should no-op when handoff_requested signal file is missing."""
    hook_input = {
        "hookEventName": "PreToolUse",
        "tool_name": "Read",
        "tool_input": {"file_path": "/some/file.txt"},
    }

    with patch("sys.stdin.read", return_value=json.dumps(hook_input)):
        main()

    captured = capsys.readouterr()
    assert captured.out == ""


def test_injects_reminder_when_signal_present(mock_env_architect, capsys):
    """Hook should inject system reminder when handoff_requested exists."""
    # Create the signal file
    signal_file = mock_env_architect / "handoff_requested"
    signal_file.touch()

    hook_input = {
        "hookEventName": "PreToolUse",
        "tool_name": "Read",
        "tool_input": {"file_path": "/some/file.txt"},
    }

    with patch("sys.stdin.read", return_value=json.dumps(hook_input)):
        main()

    captured = capsys.readouterr()
    output = json.loads(captured.out)

    assert "hookSpecificOutput" in output
    assert output["hookSpecificOutput"]["hookEventName"] == "PreToolUse"
    assert "additionalContext" in output["hookSpecificOutput"]

    context = output["hookSpecificOutput"]["additionalContext"]
    assert "handoff" in context.lower()
    assert "architect/handoff.yaml" in context
    assert "CRITICAL" in context
    assert "Context threshold reached" in context


def test_does_not_delete_signal_file(mock_env_architect, capsys):
    """Hook should NOT delete the signal file (dispatcher manages lifecycle)."""
    signal_file = mock_env_architect / "handoff_requested"
    signal_file.touch()

    hook_input = {
        "hookEventName": "PreToolUse",
        "tool_name": "Read",
        "tool_input": {"file_path": "/some/file.txt"},
    }

    with patch("sys.stdin.read", return_value=json.dumps(hook_input)):
        main()

    # Signal file should still exist
    assert signal_file.exists()


def test_performance_when_signal_missing(mock_env_architect, tmp_path):
    """Hook should be fast (<5ms) when signal file is missing."""
    import time

    hook_input = {
        "hookEventName": "PreToolUse",
        "tool_name": "Read",
        "tool_input": {"file_path": "/some/file.txt"},
    }

    start = time.perf_counter()

    with patch("sys.stdin.read", return_value=json.dumps(hook_input)):
        main()

    elapsed = (time.perf_counter() - start) * 1000  # convert to ms

    # Should be very fast (file existence check only)
    assert elapsed < 5.0


def test_works_with_different_roles(temp_panes_dir, capsys):
    """Hook should work with different role names."""
    manager_dir = temp_panes_dir / "manager"
    manager_dir.mkdir()
    signal_file = manager_dir / "handoff_requested"
    signal_file.touch()

    with patch.dict(os.environ, {"ORO_ROLE": "manager", "ORO_PANES_DIR": str(temp_panes_dir)}):
        hook_input = {
            "hookEventName": "PreToolUse",
            "tool_name": "Read",
            "tool_input": {"file_path": "/some/file.txt"},
        }

        with patch("sys.stdin.read", return_value=json.dumps(hook_input)):
            main()

        captured = capsys.readouterr()
        output = json.loads(captured.out)

        context = output["hookSpecificOutput"]["additionalContext"]
        assert "manager/handoff.yaml" in context


def test_uses_default_panes_dir_when_env_not_set(tmp_path, capsys):
    """Hook should use ~/.oro/panes when ORO_PANES_DIR is not set."""
    # Create mock home directory structure
    home_oro = tmp_path / ".oro"
    home_oro.mkdir()
    panes = home_oro / "panes"
    panes.mkdir()
    worker_dir = panes / "worker"
    worker_dir.mkdir()
    signal_file = worker_dir / "handoff_requested"
    signal_file.touch()

    with patch.dict(os.environ, {"ORO_ROLE": "worker", "HOME": str(tmp_path)}):
        hook_input = {
            "hookEventName": "PreToolUse",
            "tool_name": "Read",
            "tool_input": {"file_path": "/some/file.txt"},
        }

        with patch("sys.stdin.read", return_value=json.dumps(hook_input)):
            main()

        captured = capsys.readouterr()
        output = json.loads(captured.out)

        context = output["hookSpecificOutput"]["additionalContext"]
        assert "handoff.yaml" in context
