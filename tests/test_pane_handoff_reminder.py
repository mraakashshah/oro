"""Tests for the pane_handoff_reminder PreToolUse hook."""
# pylint: disable=import-error

import importlib.util
from pathlib import Path

import pytest  # type: ignore[import-not-found]

# Load the hook module from source (assets/hooks/) for testing
_repo_root = Path(__file__).parent.parent
_spec = importlib.util.spec_from_file_location(
    "pane_handoff_reminder",
    _repo_root / "assets" / "hooks" / "pane_handoff_reminder.py",
)
_mod = importlib.util.module_from_spec(_spec)  # type: ignore[arg-type]
_spec.loader.exec_module(_mod)  # type: ignore[union-attr]

_check_signal = _mod._check_signal
main = _mod.main


@pytest.fixture
def temp_panes_dir(tmp_path, monkeypatch):
    """Create a temporary panes directory and set ORO_PANES_DIR."""
    panes_dir = tmp_path / "panes"
    panes_dir.mkdir()
    monkeypatch.setenv("ORO_PANES_DIR", str(panes_dir))
    return panes_dir


@pytest.fixture
def temp_role(monkeypatch):
    """Set a test ORO_ROLE."""
    monkeypatch.setenv("ORO_ROLE", "test-worker")
    return "test-worker"


class TestCheckSignal:
    """Test the _check_signal function."""

    def test_signal_exists(self, temp_panes_dir, temp_role):
        """Should return True when signal file exists."""
        role_dir = temp_panes_dir / temp_role
        role_dir.mkdir()
        signal_file = role_dir / "handoff_requested"
        signal_file.touch()

        assert _check_signal(temp_role) is True

    def test_signal_missing(self, temp_panes_dir, temp_role):
        """Should return False when signal file missing."""
        role_dir = temp_panes_dir / temp_role
        role_dir.mkdir()

        assert _check_signal(temp_role) is False


class TestMainWithContextGuard:
    """Test main() respects context_pct guard (defense in depth)."""

    def test_stale_signal_low_context_no_output(self, temp_panes_dir, temp_role, capsys):
        """Signal exists + context_pct=19 → no output."""
        # Setup: create signal file
        role_dir = temp_panes_dir / temp_role
        role_dir.mkdir()
        signal_file = role_dir / "handoff_requested"
        signal_file.touch()

        # Setup: create context_pct file with low value
        context_pct_file = role_dir / "context_pct"
        context_pct_file.write_text("19")

        # Act: run the hook
        main()

        # Assert: no output because context is still fresh (< 40%)
        captured = capsys.readouterr()
        assert captured.out == ""

    def test_valid_signal_high_context_outputs_warning(self, temp_panes_dir, temp_role, capsys):
        """Signal exists + context_pct=55 → critical warning."""
        # Setup: create signal file
        role_dir = temp_panes_dir / temp_role
        role_dir.mkdir()
        signal_file = role_dir / "handoff_requested"
        signal_file.touch()

        # Setup: create context_pct file with high value
        context_pct_file = role_dir / "context_pct"
        context_pct_file.write_text("55")

        # Act: run the hook
        main()

        # Assert: outputs critical warning because context is high (>= 40%)
        captured = capsys.readouterr()
        assert "CRITICAL: Context threshold reached" in captured.out
        assert "handoff.yaml" in captured.out

    def test_signal_exists_no_context_pct_file_no_output(self, temp_panes_dir, temp_role, capsys):
        """Signal exists but context_pct file missing → no output (defensive)."""
        # Setup: create signal file only
        role_dir = temp_panes_dir / temp_role
        role_dir.mkdir()
        signal_file = role_dir / "handoff_requested"
        signal_file.touch()

        # Act: run the hook (context_pct file doesn't exist)
        main()

        # Assert: no output (can't verify context is high, so don't fire)
        captured = capsys.readouterr()
        assert captured.out == ""

    def test_signal_exists_malformed_context_pct_no_output(self, temp_panes_dir, temp_role, capsys):
        """Signal exists + malformed context_pct → no output (defensive)."""
        # Setup: create signal file
        role_dir = temp_panes_dir / temp_role
        role_dir.mkdir()
        signal_file = role_dir / "handoff_requested"
        signal_file.touch()

        # Setup: create malformed context_pct file
        context_pct_file = role_dir / "context_pct"
        context_pct_file.write_text("not-a-number")

        # Act: run the hook
        main()

        # Assert: no output (can't parse pct, so don't fire)
        captured = capsys.readouterr()
        assert captured.out == ""
