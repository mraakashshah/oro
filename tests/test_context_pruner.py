"""Tests for context_pruner.py PostToolUse hook."""
# pylint: disable=import-error

from __future__ import annotations

import io
import json
import sys
from pathlib import Path
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).parent.parent / ".claude" / "hooks"))

from context_pruner import load_config
from context_pruner import main as pruner_main


class TestLoadConfig:
    """Tests for config loading."""

    def test_loads_from_pruning_json(self, tmp_path: Path) -> None:
        config = {"Read": 5000, "Bash": 3000, "debounce_calls": 5}
        (tmp_path / "pruning.json").write_text(json.dumps(config))

        thresholds, debounce = load_config(str(tmp_path))
        assert thresholds["Read"] == 5000
        assert thresholds["Bash"] == 3000
        assert debounce == 5

    def test_missing_config_uses_defaults(self, tmp_path: Path) -> None:
        thresholds, debounce = load_config(str(tmp_path))
        assert thresholds["Read"] == 8000
        assert thresholds["Bash"] == 4000
        assert debounce == 3

    def test_invalid_json_uses_defaults(self, tmp_path: Path) -> None:
        (tmp_path / "pruning.json").write_text("not json")
        thresholds, debounce = load_config(str(tmp_path))
        assert thresholds["Read"] == 8000
        assert debounce == 3


class TestPrunerMain:
    """Tests for the main hook."""

    def test_large_output_triggers_nudge(self, tmp_path: Path, capsys) -> None:
        config = {"Read": 100, "debounce_calls": 3}
        (tmp_path / "pruning.json").write_text(json.dumps(config))

        input_data = json.dumps(
            {
                "tool_name": "Read",
                "tool_result": "x" * 200,
            }
        )

        with (
            patch.dict("os.environ", {"CLAUDE_PROJECT_DIR": str(tmp_path)}),
            patch("context_pruner.DEBOUNCE_FILE", str(tmp_path / "debounce")),
            patch("sys.stdin", io.StringIO(input_data)),
        ):
            pruner_main()

        output = json.loads(capsys.readouterr().out)
        assert "additionalContext" in output
        assert "200 chars" in output["additionalContext"]

    def test_small_output_no_nudge(self, tmp_path: Path, capsys) -> None:
        config = {"Read": 8000, "debounce_calls": 3}
        (tmp_path / "pruning.json").write_text(json.dumps(config))

        input_data = json.dumps(
            {
                "tool_name": "Read",
                "tool_result": "short output",
            }
        )

        with (
            patch.dict("os.environ", {"CLAUDE_PROJECT_DIR": str(tmp_path)}),
            patch("context_pruner.DEBOUNCE_FILE", str(tmp_path / "debounce")),
            patch("sys.stdin", io.StringIO(input_data)),
        ):
            pruner_main()

        output = json.loads(capsys.readouterr().out)
        assert output == {}

    def test_unknown_tool_no_nudge(self, tmp_path: Path, capsys) -> None:
        config = {"Read": 100, "debounce_calls": 3}
        (tmp_path / "pruning.json").write_text(json.dumps(config))

        input_data = json.dumps(
            {
                "tool_name": "UnknownTool",
                "tool_result": "x" * 200,
            }
        )

        with (
            patch.dict("os.environ", {"CLAUDE_PROJECT_DIR": str(tmp_path)}),
            patch("context_pruner.DEBOUNCE_FILE", str(tmp_path / "debounce")),
            patch("sys.stdin", io.StringIO(input_data)),
        ):
            pruner_main()

        output = json.loads(capsys.readouterr().out)
        assert output == {}


def test_debug_logging_writes_to_file(tmp_path: Path, capsys) -> None:
    """When ORO_DEBUG=1, log entries are written with tool_name, result_len, threshold, action."""
    config = {"Read": 100, "debounce_calls": 3}
    (tmp_path / "pruning.json").write_text(json.dumps(config))
    log_file = tmp_path / "hooks" / "context_pruner.log"

    input_data = json.dumps({"tool_name": "Read", "tool_result": "x" * 200})

    with (
        patch.dict("os.environ", {"CLAUDE_PROJECT_DIR": str(tmp_path), "ORO_DEBUG": "1"}),
        patch("context_pruner.DEBOUNCE_FILE", str(tmp_path / "debounce")),
        patch("context_pruner.LOG_FILE", str(log_file)),
        patch("sys.stdin", io.StringIO(input_data)),
    ):
        pruner_main()

    assert log_file.exists(), "Log file should be created when ORO_DEBUG=1"
    log_entry = json.loads(log_file.read_text().strip())
    assert log_entry["tool_name"] == "Read"
    assert log_entry["result_len"] == 200
    assert log_entry["threshold"] == 100
    assert log_entry["action"] == "nudge_fired"


def test_debug_logging_debounced(tmp_path: Path, capsys) -> None:
    """When ORO_DEBUG=1 and nudge is debounced, log records debounced action."""
    import time

    config = {"Read": 100, "debounce_calls": 3}
    (tmp_path / "pruning.json").write_text(json.dumps(config))
    log_file = tmp_path / "hooks" / "context_pruner.log"

    # Pre-populate debounce file to trigger debounce (recent fire, count < limit)
    debounce_file = tmp_path / "debounce"
    debounce_file.write_text(f"{time.time():.0f}:0")

    input_data = json.dumps({"tool_name": "Read", "tool_result": "x" * 200})

    with (
        patch.dict("os.environ", {"CLAUDE_PROJECT_DIR": str(tmp_path), "ORO_DEBUG": "1"}),
        patch("context_pruner.DEBOUNCE_FILE", str(debounce_file)),
        patch("context_pruner.LOG_FILE", str(log_file)),
        patch("sys.stdin", io.StringIO(input_data)),
    ):
        pruner_main()

    assert log_file.exists()
    log_entry = json.loads(log_file.read_text().strip())
    assert log_entry["action"] == "debounced"


def test_debug_logging_disabled_when_oro_debug_unset(tmp_path: Path, capsys) -> None:
    """When ORO_DEBUG is unset, no log file is created."""
    import os

    config = {"Read": 100, "debounce_calls": 3}
    (tmp_path / "pruning.json").write_text(json.dumps(config))
    log_file = tmp_path / "hooks" / "context_pruner.log"

    input_data = json.dumps({"tool_name": "Read", "tool_result": "x" * 200})

    # Remove ORO_DEBUG if present in real env, don't add it back
    saved = os.environ.pop("ORO_DEBUG", None)
    try:
        with (
            patch.dict("os.environ", {"CLAUDE_PROJECT_DIR": str(tmp_path)}, clear=False),
            patch("context_pruner.DEBOUNCE_FILE", str(tmp_path / "debounce")),
            patch("context_pruner.LOG_FILE", str(log_file)),
            patch("sys.stdin", io.StringIO(input_data)),
        ):
            pruner_main()
    finally:
        if saved is not None:
            os.environ["ORO_DEBUG"] = saved

    assert not log_file.exists(), "Log file should NOT be created when ORO_DEBUG is unset"


def test_debug_logging_disabled_when_oro_debug_zero(tmp_path: Path, capsys) -> None:
    """When ORO_DEBUG=0, no log file is created."""
    config = {"Read": 100, "debounce_calls": 3}
    (tmp_path / "pruning.json").write_text(json.dumps(config))
    log_file = tmp_path / "hooks" / "context_pruner.log"

    input_data = json.dumps({"tool_name": "Read", "tool_result": "x" * 200})

    with (
        patch.dict("os.environ", {"CLAUDE_PROJECT_DIR": str(tmp_path), "ORO_DEBUG": "0"}),
        patch("context_pruner.DEBOUNCE_FILE", str(tmp_path / "debounce")),
        patch("context_pruner.LOG_FILE", str(log_file)),
        patch("sys.stdin", io.StringIO(input_data)),
    ):
        pruner_main()

    assert not log_file.exists(), "Log file should NOT be created when ORO_DEBUG=0"
