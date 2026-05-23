"""Tests for skill description linting."""
# pylint: disable=import-error

from __future__ import annotations

import importlib.util
import subprocess
import sys
from pathlib import Path

import pytest  # type: ignore[import-not-found]

WORKFLOW_SUMMARY_ERROR = "description must contain triggering conditions only, not a workflow summary"


def _load_checker():
    script_path = Path(__file__).resolve().parents[1] / "scripts" / "check-skill-descriptions.py"
    spec = importlib.util.spec_from_file_location("check_skill_descriptions", script_path)
    assert spec is not None
    assert spec.loader is not None
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def _write_skill(path: Path, description: str | None) -> Path:
    path.mkdir(parents=True)
    skill_path = path / "SKILL.md"
    if description is None:
        skill_path.write_text("# Skill\n", encoding="utf-8")
        return skill_path

    skill_path.write_text(
        f"---\nname: test-skill\ndescription: {description}\n---\n\n# Skill\n",
        encoding="utf-8",
    )
    return skill_path


def _cso_description_examples() -> dict[str, str]:
    return {
        "bad": "Use when executing plans - dispatches subagent per task with code review",
        "good": "Use when you have a written implementation plan to execute",
    }


def test_check_skill_description_reports_missing_frontmatter(tmp_path: Path) -> None:
    checker = _load_checker()
    skill_path = _write_skill(tmp_path / "missing-frontmatter", None)

    assert checker.check_skill_description(skill_path) == ["missing YAML frontmatter"]


def test_check_skill_description_accepts_trigger_only_description(tmp_path: Path) -> None:
    checker = _load_checker()
    skill_path = _write_skill(
        tmp_path / "trigger-only",
        _cso_description_examples()["good"],
    )

    assert checker.check_skill_description(skill_path) == []


def test_check_skill_description_accepts_writing_skills_trigger_description() -> None:
    checker = _load_checker()
    skill_path = Path("assets/skills/writing-skills/SKILL.md")

    assert checker.check_skill_description(skill_path) == []


def test_skill_description_lint_accepts_documented_cso_good_description(
    tmp_path: Path,
) -> None:
    checker = _load_checker()
    skill_path = _write_skill(
        tmp_path / "documented-cso-good",
        _cso_description_examples()["good"],
    )
    assert checker.__file__ is not None

    result = subprocess.run(
        [sys.executable, str(Path(checker.__file__)), str(skill_path)],
        check=False,
        capture_output=True,
        text=True,
    )

    assert result.returncode == 0
    assert result.stdout == ""
    assert result.stderr == ""


def test_skill_description_lint_rejects_documented_cso_bad_description(
    tmp_path: Path,
) -> None:
    checker = _load_checker()
    skill_path = _write_skill(
        tmp_path / "documented-cso-bad",
        _cso_description_examples()["bad"],
    )

    assert checker.check_skill_description(skill_path) == [WORKFLOW_SUMMARY_ERROR]


def test_skill_description_lint_cli_reports_documented_cso_bad_description_on_stderr(
    tmp_path: Path,
    capsys: pytest.CaptureFixture[str],
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    checker = _load_checker()
    skill_path = _write_skill(
        tmp_path / "documented-cso-bad",
        _cso_description_examples()["bad"],
    )

    monkeypatch.setattr("sys.argv", ["check-skill-descriptions.py", str(skill_path)])

    assert checker.main() == 1
    captured = capsys.readouterr()
    assert captured.out == ""
    assert "workflow-summary" in captured.err
    assert str(skill_path) in captured.err


def test_skill_description_lint_builds_documented_cso_good_fixture(tmp_path: Path) -> None:
    examples = _cso_description_examples()
    good, bad = examples["good"], examples["bad"]
    skill_path = _write_skill(tmp_path / "documented-good", good)

    skill_text = skill_path.read_text(encoding="utf-8")

    assert f"description: {good}" in skill_text
    assert f"description: {bad}" not in skill_text


def test_skill_description_lint_accepts_writing_skills_trigger_description() -> None:
    repo_root = Path(__file__).resolve().parents[1]
    script_path = repo_root / "scripts" / "check-skill-descriptions.py"
    skill_path = repo_root / "assets" / "skills" / "writing-skills" / "SKILL.md"

    result = subprocess.run(
        [sys.executable, str(script_path), str(skill_path)],
        check=False,
        capture_output=True,
        text=True,
    )

    assert result.returncode == 0
    assert result.stdout == ""
    assert result.stderr == ""


@pytest.mark.parametrize("dash", ["-", "\u2013", "\u2014"])
def test_check_skill_description_rejects_dash_separated_workflow_summary(
    dash: str,
    tmp_path: Path,
) -> None:
    checker = _load_checker()
    skill_path = _write_skill(
        tmp_path / "workflow-summary",
        f"Use when executing plans {dash} dispatches subagent per task with code review",
    )

    assert checker.check_skill_description(skill_path) == [WORKFLOW_SUMMARY_ERROR]
