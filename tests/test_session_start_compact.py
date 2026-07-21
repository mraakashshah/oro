"""Tests for session_start_compact.py hook."""
# pylint: disable=import-error

from __future__ import annotations

import io
import json
import sys
from pathlib import Path
from unittest.mock import MagicMock, patch

sys.path.insert(0, str(Path(__file__).parent.parent / ".claude" / "hooks"))

from session_start_compact import main as session_start_main


def _context(output: dict) -> str:
    """Extract SessionStart additional context from a hook result."""
    return output["hookSpecificOutput"]["additionalContext"]


class TestSessionStartCompact:
    """Tests for the SessionStart(compact) hook."""

    def test_emits_codex_session_start_schema(self, tmp_path: Path, capsys) -> None:
        """Continuation context uses Codex's strict SessionStart JSON envelope."""

        state_dir = tmp_path / ".oro" / "compaction-state"
        state_dir.mkdir(parents=True)
        (state_dir / "session-schema.json").write_text(json.dumps({"bead_id": "oro-test"}))
        sys.stdin = io.StringIO(json.dumps({"session_id": "session-schema"}))

        with patch("session_start_compact.Path.home", return_value=tmp_path):
            session_start_main()

        output = json.loads(capsys.readouterr().out)
        assert set(output) == {"hookSpecificOutput"}
        assert output["hookSpecificOutput"]["hookEventName"] == "SessionStart"
        assert "oro-test" in output["hookSpecificOutput"]["additionalContext"]

    def test_reads_saved_state_and_injects_context(
        self,
        tmp_path: Path,
        capsys,
    ) -> None:
        state = {
            "bead_id": "oro-test",
            "files_modified": ["src/main.go", "pkg/worker/worker.go"],
            "errors": ["FAIL pkg/worker"],
            "last_assistant_message": "Fixing the test",
            "last_tool_calls": [{"name": "Edit"}, {"name": "Bash"}],
        }
        state_dir = tmp_path / ".oro" / "compaction-state"
        state_dir.mkdir(parents=True)
        state_path = state_dir / "session-123.json"
        state_path.write_text(json.dumps(state))

        import io

        sys.stdin = io.StringIO(json.dumps({"session_id": "session-123"}))

        with patch("session_start_compact.Path.home", return_value=tmp_path):
            session_start_main()

        output = json.loads(capsys.readouterr().out)
        ctx = _context(output)
        assert "oro-test" in ctx
        assert "src/main.go" in ctx
        assert "FAIL" in ctx
        assert "Fixing the test" in ctx

        # State file should be cleaned up
        assert not state_path.exists()

    def test_missing_state_file_is_noop(self, capsys) -> None:
        import io

        sys.stdin = io.StringIO(json.dumps({"session_id": "nonexistent"}))

        with patch(
            "session_start_compact.Path.home",
            return_value=Path("/tmp/test-no-state"),
        ):
            session_start_main()

        output = json.loads(capsys.readouterr().out)
        assert output == {}

    def test_missing_session_id_is_noop(self, capsys) -> None:
        import io

        sys.stdin = io.StringIO(json.dumps({}))
        session_start_main()
        output = json.loads(capsys.readouterr().out)
        assert output == {}


class TestSessionStartCompactRole:
    """Tests for role-aware logic in SessionStart(compact) hook."""

    def test_clears_debounce(self, tmp_path: Path, capsys) -> None:
        """Worker role clears the debounce file before falling through."""
        import io

        panes_dir = tmp_path / "panes"
        panes_dir.mkdir(parents=True)
        worker_dir = panes_dir / "worker123"
        worker_dir.mkdir(parents=True)
        debounce_file = worker_dir / "compact_debounce"
        debounce_file.write_text("1")

        sys.stdin = io.StringIO(json.dumps({"session_id": "no-state", "role": "worker123"}))

        with (
            patch("session_start_compact.PANES_DIR", str(panes_dir)),
            patch("session_start_compact.Path.home", return_value=Path("/tmp/no-state")),
        ):
            session_start_main()

        assert not debounce_file.exists()

    def test_clears_debounce_non_worker_manager(self, tmp_path: Path, capsys) -> None:
        """Non-worker (manager) role clears debounce AND injects live swarm context."""
        import io

        panes_dir = tmp_path / "panes"
        manager_dir = panes_dir / "manager"
        manager_dir.mkdir(parents=True)
        debounce_file = manager_dir / "compact_debounce"
        debounce_file.write_text("1")

        sys.stdin = io.StringIO(json.dumps({"session_id": "s1", "role": "manager"}))

        mock_result = MagicMock()
        mock_result.stdout = "status output"
        mock_result.returncode = 0

        with (
            patch("session_start_compact.PANES_DIR", str(panes_dir)),
            patch("subprocess.run", return_value=mock_result),
        ):
            session_start_main()

        assert not debounce_file.exists()
        output = json.loads(capsys.readouterr().out)
        assert "additionalContext" in output["hookSpecificOutput"]

    def test_injects_live_state(self, capsys) -> None:
        """Non-worker role injects live swarm context from BOTH oro status and task list."""
        import io

        sys.stdin = io.StringIO(json.dumps({"session_id": "s1", "role": "manager"}))

        mock_status = MagicMock()
        mock_status.stdout = "status output"
        mock_status.returncode = 0

        mock_task = MagicMock()
        mock_task.stdout = "task list output"
        mock_task.returncode = 0

        with patch("subprocess.run", side_effect=[mock_status, mock_task]) as mock_run:
            session_start_main()

        output = json.loads(capsys.readouterr().out)
        assert "additionalContext" in output["hookSpecificOutput"]
        assert "status output" in _context(output)
        assert "task list output" in _context(output)
        assert mock_run.call_count == 2
        first_cmd = mock_run.call_args_list[0][0][0]
        second_cmd = mock_run.call_args_list[1][0][0]
        assert first_cmd[0] == "oro"
        assert second_cmd[0] == "oro"
        assert second_cmd[1] == "task"

    def test_oro_status_failure_suppressed(self, capsys) -> None:
        """OSError/TimeoutExpired from oro status is suppressed; output still valid."""
        import io

        sys.stdin = io.StringIO(json.dumps({"session_id": "s1", "role": "manager"}))

        mock_bd = MagicMock()
        mock_bd.stdout = "bd list output"
        mock_bd.returncode = 0

        with patch("subprocess.run", side_effect=[OSError("not found"), mock_bd]):
            session_start_main()

        output = json.loads(capsys.readouterr().out)
        assert "additionalContext" in output["hookSpecificOutput"]

    def test_oro_status_failure_still_injects_bd_list(self, capsys) -> None:
        """When oro status raises OSError, bd list output is still in additionalContext."""
        import io

        sys.stdin = io.StringIO(json.dumps({"session_id": "s1", "role": "manager"}))

        mock_bd = MagicMock()
        mock_bd.stdout = "bd list output"
        mock_bd.returncode = 0

        with patch("subprocess.run", side_effect=[OSError("not found"), mock_bd]):
            session_start_main()

        output = json.loads(capsys.readouterr().out)
        assert "additionalContext" in output["hookSpecificOutput"]
        assert "bd list output" in _context(output)

    def test_bd_list_failure_still_injects_oro_status(self, capsys) -> None:
        """When bd list raises OSError, oro status output is still in additionalContext."""
        import io

        sys.stdin = io.StringIO(json.dumps({"session_id": "s1", "role": "manager"}))

        mock_status = MagicMock()
        mock_status.stdout = "status output"
        mock_status.returncode = 0

        with patch("subprocess.run", side_effect=[mock_status, OSError("not found")]):
            session_start_main()

        output = json.loads(capsys.readouterr().out)
        assert "additionalContext" in output["hookSpecificOutput"]
        assert "status output" in _context(output)

    def test_worker_path_unchanged(self, tmp_path: Path, capsys) -> None:
        """Worker role falls through to transcript path after clearing debounce."""
        import io

        state = {
            "bead_id": "oro-test",
            "files_modified": ["src/main.go"],
        }
        state_dir = tmp_path / ".oro" / "compaction-state"
        state_dir.mkdir(parents=True)
        state_path = state_dir / "session-worker.json"
        state_path.write_text(json.dumps(state))

        panes_dir = tmp_path / "panes"
        panes_dir.mkdir(parents=True)

        sys.stdin = io.StringIO(json.dumps({"session_id": "session-worker", "role": "worker-abc"}))

        with (
            patch("session_start_compact.PANES_DIR", str(panes_dir)),
            patch("session_start_compact.Path.home", return_value=tmp_path),
        ):
            session_start_main()

        output = json.loads(capsys.readouterr().out)
        assert "additionalContext" in output["hookSpecificOutput"]
        assert "oro-test" in _context(output)
