from __future__ import annotations

import importlib.util
from pathlib import Path

import pytest

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


def test_check_skill_description_reports_missing_frontmatter(tmp_path: Path) -> None:
    checker = _load_checker()
    skill_path = _write_skill(tmp_path / "missing-frontmatter", None)

    assert checker.check_skill_description(skill_path) == ["missing YAML frontmatter"]


def test_check_skill_description_accepts_trigger_only_description(tmp_path: Path) -> None:
    checker = _load_checker()
    skill_path = _write_skill(
        tmp_path / "trigger-only",
        "Use when you have a written implementation plan to execute",
    )

    assert checker.check_skill_description(skill_path) == []


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
