"""Tests for the handoff schema linter CLI."""

import subprocess
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[1]
SCRIPT = REPO_ROOT / "scripts" / "check-handoff-schema.py"


def test_check_handoff_schema_cli_exit_codes(tmp_path: Path) -> None:
    invalid = tmp_path / "invalid.yaml"
    invalid.write_text(
        """
goal: continue work
tasks:
  done: []
""".lstrip(),
        encoding="utf-8",
    )

    valid = tmp_path / "valid.yaml"
    valid.write_text(
        """
goal: continue work
tasks:
  completed:
    - wrote tests
  in_progress: []
  remaining:
    - run quality gate
""".lstrip(),
        encoding="utf-8",
    )

    invalid_result = subprocess.run(
        [sys.executable, str(SCRIPT), str(invalid)],
        check=False,
        capture_output=True,
        text=True,
    )
    assert invalid_result.returncode != 0
    assert "missing required key: tasks.completed" in invalid_result.stderr
    assert "missing required key: tasks.in_progress" in invalid_result.stderr
    assert "missing required key: tasks.remaining" in invalid_result.stderr

    valid_result = subprocess.run(
        [sys.executable, str(SCRIPT), str(valid)],
        check=False,
        capture_output=True,
        text=True,
    )
    assert valid_result.returncode == 0
    assert valid_result.stderr == ""


def test_check_handoff_schema_cli_rejects_missing_file(tmp_path: Path) -> None:
    missing = tmp_path / "missing.yaml"

    result = subprocess.run(
        [sys.executable, str(SCRIPT), str(missing)],
        check=False,
        capture_output=True,
        text=True,
    )

    assert result.returncode != 0
    assert "file not found" in result.stderr


def test_check_handoff_schema_cli_rejects_malformed_yaml(tmp_path: Path) -> None:
    malformed = tmp_path / "malformed.yaml"
    malformed.write_text("tasks: [not closed\n", encoding="utf-8")

    result = subprocess.run(
        [sys.executable, str(SCRIPT), str(malformed)],
        check=False,
        capture_output=True,
        text=True,
    )

    assert result.returncode != 0
    assert "malformed YAML" in result.stderr


def test_check_handoff_schema_cli_rejects_absent_tasks_key(tmp_path: Path) -> None:
    handoff = tmp_path / "handoff.yaml"
    handoff.write_text("goal: continue work\n", encoding="utf-8")

    result = subprocess.run(
        [sys.executable, str(SCRIPT), str(handoff)],
        check=False,
        capture_output=True,
        text=True,
    )

    assert result.returncode != 0
    assert "missing required key: tasks" in result.stderr


def test_check_handoff_schema_cli_allows_empty_in_progress(tmp_path: Path) -> None:
    handoff = tmp_path / "handoff.yaml"
    handoff.write_text(
        """
tasks:
  completed: []
  in_progress: []
  remaining: []
""".lstrip(),
        encoding="utf-8",
    )

    result = subprocess.run(
        [sys.executable, str(SCRIPT), str(handoff)],
        check=False,
        capture_output=True,
        text=True,
    )

    assert result.returncode == 0
    assert result.stderr == ""
