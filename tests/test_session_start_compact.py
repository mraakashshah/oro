"""Tests for session_start_compact.py hook."""

from __future__ import annotations

import json
import sys
from pathlib import Path
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).parent.parent / ".claude" / "hooks"))

from session_start_compact import main as session_start_main


class TestSessionStartCompact:
    """Tests for the SessionStart(compact) hook."""

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
        ctx = output["additionalContext"]
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
