"""Tests for context_pruner.py PostToolUse hook."""
# pylint: disable=import-error

from __future__ import annotations

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

        import io

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

        import io

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

        import io

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
