"""E2E integration test for Layer 3 context management.

Tests the full pre_compact → session_start_compact hook chain with a
realistic high-context transcript simulating a worker session that
exceeds compaction thresholds.

Covers oro-vejk: Test Layer 3 context management with forced high-context scenario.
"""
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
from session_start_compact import main as session_start_main


def _build_realistic_transcript(tmp_path: Path, *, num_tool_calls: int = 50) -> Path:
    """Build a realistic transcript JSONL simulating a high-context worker session.

    Generates a sequence of assistant messages, tool_use calls (Edit, Bash, Read),
    and tool_result entries that mirror what a real oro worker session produces.
    """
    transcript = tmp_path / "transcript.jsonl"
    lines: list[str] = []

    # Worker claims a bead
    lines.append(json.dumps({"role": "assistant", "content": "Starting work on oro-test-bead."}))
    lines.append(
        json.dumps(
            {
                "type": "tool_use",
                "name": "Bash",
                "tool_input": {"command": "bd update oro-test-bead --status in_progress"},
            }
        )
    )
    lines.append(json.dumps({"type": "tool_result", "tool_result": {"exit_code": 0, "stdout": "Updated"}}))

    # Simulate many file reads, edits, and test runs
    files_touched = [
        "/src/pkg/worker/handler.go",
        "/src/pkg/worker/handler_test.go",
        "/src/cmd/oro/main.go",
        "/src/pkg/dispatcher/dispatch.go",
    ]

    for i in range(num_tool_calls):
        file_path = files_touched[i % len(files_touched)]

        # Read
        lines.append(
            json.dumps(
                {
                    "type": "tool_use",
                    "name": "Read",
                    "tool_input": {"file_path": file_path},
                }
            )
        )
        # Simulate large read output (high context consumption)
        lines.append(
            json.dumps(
                {
                    "type": "tool_result",
                    "tool_result": {
                        "exit_code": 0,
                        "stdout": f"// file content chunk {i}\n" * 200,
                    },
                }
            )
        )

        # Assistant thinking
        lines.append(
            json.dumps(
                {
                    "role": "assistant",
                    "content": f"Analyzing {file_path}, applying fix for iteration {i}.",
                }
            )
        )

        # Edit
        lines.append(
            json.dumps(
                {
                    "type": "tool_use",
                    "name": "Edit",
                    "tool_input": {
                        "file_path": file_path,
                        "old_string": f"old code {i}",
                        "new_string": f"new code {i}",
                    },
                }
            )
        )
        lines.append(json.dumps({"type": "tool_result", "tool_result": {"exit_code": 0}}))

        # Periodic test runs (some failing)
        if i % 5 == 4:
            exit_code = 1 if i % 10 == 4 else 0
            lines.append(
                json.dumps(
                    {
                        "type": "tool_use",
                        "name": "Bash",
                        "tool_input": {"command": "go test ./pkg/worker/... -v -count=1"},
                    }
                )
            )
            stderr = f"FAIL pkg/worker iteration {i}" if exit_code else ""
            lines.append(
                json.dumps(
                    {
                        "type": "tool_result",
                        "tool_result": {"exit_code": exit_code, "stderr": stderr},
                    }
                )
            )

    # Final assistant message before compaction
    lines.append(
        json.dumps(
            {
                "role": "assistant",
                "content": "Tests are passing now. Committing the fix and running lint.",
            }
        )
    )

    transcript.write_text("\n".join(lines))
    return transcript


class TestLayer3E2E:
    """End-to-end integration tests for Layer 3 context management chain."""

    def test_full_chain_preserves_state_across_compaction(
        self,
        tmp_path: Path,
        capsys: pytest.CaptureFixture[str],
    ) -> None:
        """Full chain: pre_compact extracts state → session_start_compact restores it."""
        import io

        session_id = "e2e-test-session"
        transcript = _build_realistic_transcript(tmp_path, num_tool_calls=50)

        # --- Phase 1: PreCompact fires ---
        input_data = json.dumps({"session_id": session_id, "transcript_path": str(transcript)})

        with (
            patch("pre_compact.Path.home", return_value=tmp_path),
            patch("sys.stdin", io.StringIO(input_data)),
        ):
            pre_compact_main()

        pre_output = json.loads(capsys.readouterr().out)
        assert pre_output["continue"] is True
        assert "oro-test-bead" in pre_output["systemMessage"]

        # Verify state file was persisted
        state_path = tmp_path / ".oro" / "compaction-state" / f"{session_id}.json"
        assert state_path.exists()
        saved_state = json.loads(state_path.read_text())
        assert saved_state["bead_id"] == "oro-test-bead"
        assert len(saved_state["files_modified"]) >= 2
        assert len(saved_state["last_tool_calls"]) == 5
        assert saved_state["last_assistant_message"].startswith("Tests are passing")

        # --- Phase 2: SessionStart(compact) fires ---
        sys.stdin = io.StringIO(json.dumps({"session_id": session_id}))

        with patch("session_start_compact.Path.home", return_value=tmp_path):
            session_start_main()

        post_output = json.loads(capsys.readouterr().out)
        ctx = post_output["additionalContext"]

        # Verify continuation context contains key state
        assert "oro-test-bead" in ctx
        assert "handler.go" in ctx or "main.go" in ctx  # files modified
        assert "Tests are passing" in ctx  # last assistant message
        assert "Edit" in ctx or "Bash" in ctx  # recent tools

        # Verify state file was cleaned up
        assert not state_path.exists()

    def test_high_context_transcript_extracts_errors(self, tmp_path: Path) -> None:
        """Verify errors from failing test runs are captured in state."""
        transcript = _build_realistic_transcript(tmp_path, num_tool_calls=50)
        state = parse_transcript(transcript)

        assert len(state["errors"]) > 0
        assert any("FAIL" in e for e in state["errors"])

    def test_high_context_files_modified_deduped(self, tmp_path: Path) -> None:
        """Verify files_modified is deduplicated (same file edited many times)."""
        transcript = _build_realistic_transcript(tmp_path, num_tool_calls=50)
        state = parse_transcript(transcript)

        # 4 unique files touched across 50 iterations
        assert len(state["files_modified"]) == 4
        assert all(f.startswith("/src/") for f in state["files_modified"])

    def test_last_tool_calls_limited_to_five(self, tmp_path: Path) -> None:
        """Verify only last 5 tool calls kept regardless of transcript size."""
        transcript = _build_realistic_transcript(tmp_path, num_tool_calls=100)
        state = parse_transcript(transcript)
        assert len(state["last_tool_calls"]) == 5

    def test_worker_mode_creates_continuation_bead(
        self,
        tmp_path: Path,
        capsys: pytest.CaptureFixture[str],
    ) -> None:
        """When ORO_WORKER=1, session_start_compact creates a continuation bead."""
        import io

        state = {
            "bead_id": "oro-high-ctx",
            "files_modified": ["src/main.go"],
            "errors": [],
            "last_assistant_message": "Working on feature",
            "last_tool_calls": [{"name": "Edit"}],
        }
        state_dir = tmp_path / ".oro" / "compaction-state"
        state_dir.mkdir(parents=True)
        state_path = state_dir / "worker-session.json"
        state_path.write_text(json.dumps(state))

        sys.stdin = io.StringIO(json.dumps({"session_id": "worker-session"}))

        with (
            patch("session_start_compact.Path.home", return_value=tmp_path),
            patch.dict("os.environ", {"ORO_WORKER": "1"}),
            patch("session_start_compact.subprocess.run") as mock_run,
        ):
            session_start_main()

        # Verify oro bead create was called with continuation bead args
        mock_run.assert_called_once()
        call_args = mock_run.call_args[0][0]
        assert call_args[0] == "oro"
        assert call_args[1] == "bead"
        assert "create" in call_args
        assert any("Continue: oro-high-ctx" in a for a in call_args)
        assert any("--parent=oro-high-ctx" in a for a in call_args)

        output = json.loads(capsys.readouterr().out)
        assert "additionalContext" in output
        assert "oro-high-ctx" in output["additionalContext"]

    def test_non_worker_mode_skips_continuation_bead(
        self,
        tmp_path: Path,
        capsys: pytest.CaptureFixture[str],
    ) -> None:
        """Without ORO_WORKER=1, no continuation bead is created."""
        import io

        state = {
            "bead_id": "oro-standalone",
            "files_modified": [],
            "errors": [],
            "last_assistant_message": "",
            "last_tool_calls": [],
        }
        state_dir = tmp_path / ".oro" / "compaction-state"
        state_dir.mkdir(parents=True)
        state_path = state_dir / "standalone-session.json"
        state_path.write_text(json.dumps(state))

        sys.stdin = io.StringIO(json.dumps({"session_id": "standalone-session"}))

        with (
            patch("session_start_compact.Path.home", return_value=tmp_path),
            patch.dict("os.environ", {"ORO_WORKER": ""}, clear=False),
            patch("session_start_compact.subprocess.run") as mock_run,
        ):
            session_start_main()

        # bd create should NOT be called
        mock_run.assert_not_called()

        output = json.loads(capsys.readouterr().out)
        assert "additionalContext" in output

    def test_state_persistence_survives_save_and_load(self, tmp_path: Path) -> None:
        """Verify save_state → load roundtrip preserves all fields."""
        state = {
            "bead_id": "oro-roundtrip",
            "files_modified": ["a.go", "b.go", "c.go"],
            "errors": ["error 1", "error 2"],
            "last_assistant_message": "Almost done with the refactor",
            "last_tool_calls": [
                {"name": "Edit", "input": "/src/a.go"},
                {"name": "Bash", "input": "go test ./..."},
            ],
        }

        with patch("pre_compact.Path.home", return_value=tmp_path):
            path = save_state("roundtrip-session", state)

        loaded = json.loads(path.read_text())
        assert loaded == state
