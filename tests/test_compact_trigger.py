#!/usr/bin/env python3
"""Tests for compact_trigger.py hook.

Tests that the hook correctly triggers /compact when context percentage reaches
the model-specific threshold, with debounce to prevent repeated triggers.
"""

import json
import sys
from io import StringIO
from pathlib import Path
from unittest.mock import MagicMock, patch

HOOKS_DIR = str(Path(__file__).resolve().parent.parent / "assets" / "hooks")


def _run_main(
    monkeypatch,
    *,
    env: dict,
    panes_dir: Path,
    thresholds_file: Path,
    hook_input: dict | None = None,
    returncode: int = 0,
) -> MagicMock:
    """Re-import compact_trigger fresh and run main() with patched state.

    Uses del sys.modules + re-import to avoid module caching between tests.
    Patches compact_trigger.PANES_DIR and compact_trigger.THRESHOLDS_FILE
    (never patches os.path.expanduser).

    Returns the mock for subprocess.run.
    """
    # Reset relevant env vars, then set provided ones
    for key in ("TMUX_PANE", "ORO_WORKER", "ORO_ROLE"):
        monkeypatch.delenv(key, raising=False)
    for key, val in env.items():
        monkeypatch.setenv(key, val)

    # Fresh import — per bead notes: del + re-import avoids module caching
    if "compact_trigger" in sys.modules:
        del sys.modules["compact_trigger"]
    if HOOKS_DIR not in sys.path:
        sys.path.insert(0, HOOKS_DIR)

    import compact_trigger

    compact_trigger.PANES_DIR = str(panes_dir)
    compact_trigger.THRESHOLDS_FILE = thresholds_file

    mock_run = MagicMock()
    mock_run.return_value.returncode = returncode

    stdin_data = json.dumps(hook_input or {})
    with patch("sys.stdin", StringIO(stdin_data)), patch("compact_trigger.subprocess.run", mock_run):
        compact_trigger.main()

    return mock_run


class TestCompactTrigger:
    def test_no_tmux_pane_returns(self, monkeypatch, tmp_path):
        """TMUX_PANE absent → hook returns immediately without any action."""
        panes_dir = tmp_path / "panes"
        thresholds_file = tmp_path / "thresholds.json"
        thresholds_file.write_text('{"sonnet": 50}')

        mock_run = _run_main(
            monkeypatch,
            env={"ORO_ROLE": "worker"},  # No TMUX_PANE
            panes_dir=panes_dir,
            thresholds_file=thresholds_file,
        )

        mock_run.assert_not_called()

    def test_oro_worker_returns(self, monkeypatch, tmp_path):
        """ORO_WORKER=1 → hook returns without triggering compact."""
        panes_dir = tmp_path / "panes"
        role_dir = panes_dir / "worker"
        role_dir.mkdir(parents=True)
        (role_dir / "context_pct").write_text("60\n")

        thresholds_file = tmp_path / "thresholds.json"
        thresholds_file.write_text('{"sonnet": 50}')

        mock_run = _run_main(
            monkeypatch,
            env={"TMUX_PANE": "%0", "ORO_WORKER": "1", "ORO_ROLE": "worker"},
            panes_dir=panes_dir,
            thresholds_file=thresholds_file,
            hook_input={"model_key": "sonnet"},
        )

        mock_run.assert_not_called()

    def test_no_oro_role_returns(self, monkeypatch, tmp_path):
        """ORO_ROLE absent → hook returns without triggering compact."""
        panes_dir = tmp_path / "panes"
        thresholds_file = tmp_path / "thresholds.json"
        thresholds_file.write_text('{"sonnet": 50}')

        mock_run = _run_main(
            monkeypatch,
            env={"TMUX_PANE": "%0"},  # No ORO_ROLE
            panes_dir=panes_dir,
            thresholds_file=thresholds_file,
        )

        mock_run.assert_not_called()

    def test_pct_file_absent_returns(self, monkeypatch, tmp_path):
        """pct_file absent → hook returns without triggering compact."""
        panes_dir = tmp_path / "panes"
        panes_dir.mkdir()
        # No context_pct file exists for the role

        thresholds_file = tmp_path / "thresholds.json"
        thresholds_file.write_text('{"sonnet": 50}')

        mock_run = _run_main(
            monkeypatch,
            env={"TMUX_PANE": "%0", "ORO_ROLE": "worker"},
            panes_dir=panes_dir,
            thresholds_file=thresholds_file,
            hook_input={"model_key": "sonnet"},
        )

        mock_run.assert_not_called()

    def test_pct_below_threshold_no_compact(self, monkeypatch, tmp_path):
        """pct below threshold → no compact triggered."""
        panes_dir = tmp_path / "panes"
        role_dir = panes_dir / "worker"
        role_dir.mkdir(parents=True)
        (role_dir / "context_pct").write_text("40\n")  # Below sonnet threshold of 50

        thresholds_file = tmp_path / "thresholds.json"
        thresholds_file.write_text('{"sonnet": 50}')

        mock_run = _run_main(
            monkeypatch,
            env={"TMUX_PANE": "%0", "ORO_ROLE": "worker"},
            panes_dir=panes_dir,
            thresholds_file=thresholds_file,
            hook_input={"model_key": "sonnet"},
        )

        mock_run.assert_not_called()

    def test_debounce_file_exists_returns(self, monkeypatch, tmp_path):
        """debounce file exists → hook returns without triggering compact again."""
        panes_dir = tmp_path / "panes"
        role_dir = panes_dir / "worker"
        role_dir.mkdir(parents=True)
        (role_dir / "context_pct").write_text("55\n")  # Above threshold
        (role_dir / "compact_debounce").touch()  # Already debounced

        thresholds_file = tmp_path / "thresholds.json"
        thresholds_file.write_text('{"sonnet": 50}')

        mock_run = _run_main(
            monkeypatch,
            env={"TMUX_PANE": "%0", "ORO_ROLE": "worker"},
            panes_dir=panes_dir,
            thresholds_file=thresholds_file,
            hook_input={"model_key": "sonnet"},
        )

        mock_run.assert_not_called()

    def test_tmux_failure_skips_debounce_write(self, monkeypatch, tmp_path):
        """tmux returncode != 0 → compact attempted but debounce NOT written."""
        panes_dir = tmp_path / "panes"
        role_dir = panes_dir / "worker"
        role_dir.mkdir(parents=True)
        (role_dir / "context_pct").write_text("55\n")  # Above threshold

        thresholds_file = tmp_path / "thresholds.json"
        thresholds_file.write_text('{"sonnet": 50}')

        mock_run = _run_main(
            monkeypatch,
            env={"TMUX_PANE": "%0", "ORO_ROLE": "worker"},
            panes_dir=panes_dir,
            thresholds_file=thresholds_file,
            hook_input={"model_key": "sonnet"},
            returncode=1,  # tmux fails
        )

        # tmux was called
        mock_run.assert_called_once()
        # But debounce file was NOT written
        assert not (role_dir / "compact_debounce").exists()

    def test_compact_triggered_writes_debounce(self, monkeypatch, tmp_path):
        """pct >= threshold, tmux succeeds → compact triggered AND debounce written."""
        panes_dir = tmp_path / "panes"
        role_dir = panes_dir / "worker"
        role_dir.mkdir(parents=True)
        (role_dir / "context_pct").write_text("55\n")  # Above sonnet threshold of 50

        thresholds_file = tmp_path / "thresholds.json"
        thresholds_file.write_text('{"sonnet": 50}')

        mock_run = _run_main(
            monkeypatch,
            env={"TMUX_PANE": "%0", "ORO_ROLE": "worker"},
            panes_dir=panes_dir,
            thresholds_file=thresholds_file,
            hook_input={"model_key": "sonnet"},
            returncode=0,
        )

        # tmux was called with compact command targeting the pane
        mock_run.assert_called_once()
        call_args = mock_run.call_args[0][0]  # First positional arg = cmd list
        assert "tmux" in call_args
        assert "/compact" in call_args
        assert "%0" in call_args  # The TMUX_PANE value

        # Debounce file was written after successful compact
        assert (role_dir / "compact_debounce").exists()
