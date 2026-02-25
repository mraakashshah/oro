"""Tests for pre_compact.py and session_start_compact.py hooks."""
# pylint: disable=import-error

from __future__ import annotations

import json
import sys
from pathlib import Path
from unittest.mock import patch

import pytest  # type: ignore[import-not-found]

# Add hooks directory to path
sys.path.insert(0, str(Path(__file__).parent.parent / ".claude" / "hooks"))

from pre_compact import main as pre_compact_main
from pre_compact import parse_transcript, save_state


class TestParseTranscript:
    """Tests for transcript parsing."""

    def test_extracts_state_from_transcript(self, tmp_path: Path) -> None:
        """Core acceptance test: parses transcript JSONL and extracts structured state."""
        transcript = tmp_path / "transcript.jsonl"
        lines = [
            json.dumps({"role": "assistant", "content": "Working on the fix now"}),
            json.dumps(
                {
                    "type": "tool_use",
                    "name": "Edit",
                    "tool_input": {"file_path": "/src/main.go"},
                }
            ),
            json.dumps(
                {
                    "type": "tool_use",
                    "name": "Bash",
                    "tool_input": {"command": "bd update oro-abc --status in_progress"},
                }
            ),
            json.dumps(
                {
                    "type": "tool_use",
                    "name": "Bash",
                    "tool_input": {"command": "go test ./..."},
                }
            ),
            json.dumps(
                {
                    "type": "tool_result",
                    "tool_result": {"exit_code": 1, "stderr": "FAIL pkg/worker"},
                }
            ),
        ]
        transcript.write_text("\n".join(lines))

        state = parse_transcript(transcript)

        assert "/src/main.go" in state["files_modified"]
        assert state["bead_id"] == "oro-abc"
        assert len(state["errors"]) == 1
        assert "FAIL" in state["errors"][0]
        assert state["last_assistant_message"] == "Working on the fix now"
        assert len(state["last_tool_calls"]) >= 2

    def test_missing_transcript_returns_empty_state(self, tmp_path: Path) -> None:
        state = parse_transcript(tmp_path / "nonexistent.jsonl")
        assert state["files_modified"] == []
        assert state["bead_id"] is None
        assert state["errors"] == []

    def test_empty_transcript_returns_empty_state(self, tmp_path: Path) -> None:
        transcript = tmp_path / "transcript.jsonl"
        transcript.write_text("")
        state = parse_transcript(transcript)
        assert state["files_modified"] == []

    def test_last_5_tool_calls_only(self, tmp_path: Path) -> None:
        transcript = tmp_path / "transcript.jsonl"
        lines = [json.dumps({"type": "tool_use", "name": f"Tool{i}", "tool_input": {}}) for i in range(10)]
        transcript.write_text("\n".join(lines))
        state = parse_transcript(transcript)
        assert len(state["last_tool_calls"]) == 5
        assert state["last_tool_calls"][0]["name"] == "Tool5"


class TestSaveState:
    """Tests for state persistence."""

    def test_saves_to_compaction_state_dir(self, tmp_path: Path) -> None:
        with patch("pre_compact.Path.home", return_value=tmp_path):
            state = {"bead_id": "oro-test", "files_modified": ["a.go"]}
            path = save_state("session-123", state)

            assert path.exists()
            loaded = json.loads(path.read_text())
            assert loaded["bead_id"] == "oro-test"
            assert path.parent.name == "compaction-state"


class TestPreCompactMain:
    """Tests for the main hook entry point."""

    def test_outputs_continue_true(self, tmp_path: Path, capsys: pytest.CaptureFixture[str]) -> None:
        transcript = tmp_path / "transcript.jsonl"
        transcript.write_text(json.dumps({"role": "assistant", "content": "hello"}))

        input_data = json.dumps(
            {
                "session_id": "test-session",
                "transcript_path": str(transcript),
            }
        )

        import io

        with patch("pre_compact.Path.home", return_value=tmp_path), patch("sys.stdin", io.StringIO(input_data)):
            pre_compact_main()

        output = json.loads(capsys.readouterr().out)
        assert output["continue"] is True
        assert "systemMessage" in output

    def test_missing_session_id_is_noop(self, capsys: pytest.CaptureFixture[str]) -> None:
        import io

        sys.stdin = io.StringIO(json.dumps({}))
        pre_compact_main()
        output = json.loads(capsys.readouterr().out)
        assert output["continue"] is True
        assert "systemMessage" not in output
