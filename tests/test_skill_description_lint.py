"""Tests for skill description linting."""
# pylint: disable=import-error

from __future__ import annotations

import importlib.util
import subprocess
import sys
from pathlib import Path

import pytest  # type: ignore[import-not-found]

WORKFLOW_SUMMARY_ERROR = "description must contain triggering conditions only, not a workflow summary"


def _repo_root() -> Path:
    return Path(__file__).resolve().parents[1]


def _load_checker():
    script_path = _repo_root() / "scripts" / "check-skill-descriptions.py"
    spec = importlib.util.spec_from_file_location("check_skill_descriptions", script_path)
    assert spec is not None
    assert spec.loader is not None
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def _run_skill_description_lint(skill_path: Path) -> subprocess.CompletedProcess[str]:
    script_path = _repo_root() / "scripts" / "check-skill-descriptions.py"
    return subprocess.run(
        [sys.executable, str(script_path), str(skill_path)],
        check=False,
        capture_output=True,
        text=True,
    )


def _read_skill_description(skill_path: Path) -> str | None:
    checker = _load_checker()
    frontmatter = checker._frontmatter(skill_path.read_text(encoding="utf-8"))
    if frontmatter is None:
        return None

    description = frontmatter.get("description")
    if not isinstance(description, str):
        return None

    description = description.strip()
    return description or None


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


def test_check_skill_description_accepts_writing_skills_trigger_description_via_api() -> None:
    checker = _load_checker()
    skill_path = _repo_root() / "assets/skills/writing-skills/SKILL.md"

    assert checker.check_skill_description(skill_path) == []


def test_check_skill_description_accepts_writing_skills_asset() -> None:
    checker = _load_checker()
    skill_path = _repo_root() / "assets/skills/writing-skills/SKILL.md"

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
    skill_path = _repo_root() / "assets" / "skills" / "writing-skills" / "SKILL.md"

    result = _run_skill_description_lint(skill_path)

    assert result.returncode == 0
    assert result.stdout == ""
    assert result.stderr == ""


def test_skill_description_lint_accepts_writing_skills_cli() -> None:
    skill_path = _repo_root() / "assets/skills/writing-skills/SKILL.md"

    result = _run_skill_description_lint(skill_path)

    assert result.returncode == 0
    assert result.stdout == ""
    assert result.stderr == ""


def test_skill_description_lint_accepts_writing_skills_trigger_description_cli() -> None:
    skill_path = _repo_root() / "assets/skills/writing-skills/SKILL.md"

    result = _run_skill_description_lint(skill_path)

    assert result.returncode == 0
    assert result.stdout == ""
    assert result.stderr == ""


def test_skill_description_lint_cli_accepts_writing_skills_trigger_description() -> None:
    skill_path = _repo_root() / "assets/skills/writing-skills/SKILL.md"

    result = _run_skill_description_lint(skill_path)

    assert result.returncode == 0
    assert result.stdout == ""
    assert result.stderr == ""


def test_check_skill_descriptions_quiet_success_for_valid_writing_skills_asset() -> None:
    skill_path = _repo_root() / "assets/skills/writing-skills/SKILL.md"

    result = _run_skill_description_lint(skill_path)

    assert result.returncode == 0
    assert result.stdout == ""
    assert result.stderr == ""


def test_read_skill_description_extracts_frontmatter_description(tmp_path: Path) -> None:
    skill_path = _write_skill(
        tmp_path / "agent-browser-trigger",
        "  Use when the user needs to interact with websites  ",
    )
    missing_frontmatter = _write_skill(tmp_path / "missing-frontmatter", None)
    missing_description = tmp_path / "missing-description" / "SKILL.md"
    missing_description.parent.mkdir(parents=True)
    missing_description.write_text("---\nname: missing-description\n---\n\n# Skill\n", encoding="utf-8")

    assert _read_skill_description(skill_path) == "Use when the user needs to interact with websites"
    assert _read_skill_description(missing_frontmatter) is None
    assert _read_skill_description(missing_description) is None


def test_writing_skills_description_is_normalized_trigger_only() -> None:
    checker = _load_checker()
    skill_path = _repo_root() / "assets/skills/writing-skills/SKILL.md"
    skill_text = skill_path.read_text(encoding="utf-8")
    opening_marker, _, remainder = skill_text.partition("---\n")
    assert opening_marker == ""

    frontmatter_text, closing_marker, markdown_body = remainder.partition("\n---\n")
    frontmatter = frontmatter_text.splitlines()
    normalized_description = "Use when creating or editing Codex skills"

    assert closing_marker == "\n---\n"
    assert "---" not in frontmatter
    assert f"description: {normalized_description}" in frontmatter
    assert normalized_description not in markdown_body
    assert checker.check_skill_description(skill_path) == []


def test_writing_skills_asset_is_lint_clean() -> None:
    checker = _load_checker()
    skill_path = _repo_root() / "assets/skills/writing-skills/SKILL.md"

    assert checker.check_skill_description(skill_path) == []


def test_agent_browser_description_lint_accepts_canonical_file() -> None:
    checker = _load_checker()
    skill_path = _repo_root() / "assets/skills/agent-browser/SKILL.md"
    description = _read_skill_description(skill_path)

    assert description is not None
    assert description.startswith("Use when the user needs to interact with websites")
    assert not description.startswith("Browser automation CLI for AI agents.")
    assert checker.check_skill_description(skill_path) == []


def test_agent_browser_canonical_description_is_normalized_trigger_only() -> None:
    checker = _load_checker()
    skill_path = _repo_root() / "assets/skills/agent-browser/SKILL.md"
    skill_text = skill_path.read_text(encoding="utf-8")
    description = _read_skill_description(skill_path)
    _frontmatter_text, closing_marker, markdown_body = skill_text[4:].partition("\n---\n")

    assert closing_marker == "\n---\n"
    assert description is not None
    assert description.startswith("Use when the user needs to interact with websites")
    assert "Browser automation CLI for AI agents." not in skill_text
    assert not description.startswith("Browser automation CLI for AI agents.")
    assert description not in markdown_body
    assert checker.check_skill_description(skill_path) == []


def test_agent_browser_nested_description_is_normalized_trigger_only() -> None:
    checker = _load_checker()
    skill_path = _repo_root() / "assets/skills/agent-browser/agent-browser/SKILL.md"
    skill_text = skill_path.read_text(encoding="utf-8")
    description = _read_skill_description(skill_path)
    _frontmatter_text, closing_marker, _markdown_body = skill_text[4:].partition("\n---\n")

    assert closing_marker == "\n---\n"
    assert description is not None
    assert description.startswith("Use when the user needs to interact with websites")
    assert "Browser automation CLI for AI agents." not in skill_text
    assert checker.check_skill_description(skill_path) == []


def test_agent_browser_descriptions_are_normalized_trigger_only() -> None:
    checker = _load_checker()
    repo_root = _repo_root()
    skill_paths = [
        repo_root / "assets/skills/agent-browser/SKILL.md",
        repo_root / "assets/skills/agent-browser/agent-browser/SKILL.md",
    ]

    for skill_path in skill_paths:
        skill_text = skill_path.read_text(encoding="utf-8")
        description = _read_skill_description(skill_path)

        assert description is not None
        assert description.startswith("Use when the user needs to interact with websites")
        assert "Browser automation CLI for AI agents." not in skill_text
        assert checker.check_skill_description(skill_path) == []


def test_agent_browser_descriptions_are_identical() -> None:
    repo_root = _repo_root()
    canonical_description = _read_skill_description(repo_root / "assets/skills/agent-browser/SKILL.md")
    nested_description = _read_skill_description(repo_root / "assets/skills/agent-browser/agent-browser/SKILL.md")

    assert canonical_description is not None
    assert nested_description is not None
    assert canonical_description == nested_description


def test_using_skills_description_is_normalized_trigger_only(tmp_path: Path) -> None:
    checker = _load_checker()
    skill_path = _repo_root() / "assets/skills/using-skills/SKILL.md"
    skill_text = skill_path.read_text(encoding="utf-8")
    frontmatter = skill_text.split("\n---\n", maxsplit=1)[0].splitlines()

    assert "description: Use when checking which skills apply before starting a task" in frontmatter
    assert checker.check_skill_description(skill_path) == []

    workflow_summary = _write_skill(
        tmp_path / "using-skills-workflow-summary",
        "Use at the start of any task - check which skills apply before acting",
    )
    mandatory_rule = _write_skill(
        tmp_path / "using-skills-mandatory-rule",
        "Use at the start of any task and you MUST invoke relevant skills before any action",
    )

    assert checker.check_skill_description(workflow_summary) == [WORKFLOW_SUMMARY_ERROR]
    assert checker.check_skill_description(mandatory_rule) == [WORKFLOW_SUMMARY_ERROR]


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


def test_using_skills_description_rejects_non_trigger_descriptions(
    tmp_path: Path,
) -> None:
    checker = _load_checker()
    valid_skill_path = _write_skill(
        tmp_path / "trigger-only",
        "Use when checking which skills apply before starting a task",
    )
    workflow_summary_path = _write_skill(
        tmp_path / "workflow-summary",
        "Use when checking skills - always invoke applicable skills before action",
    )
    ordinary_prose_path = _write_skill(
        tmp_path / "ordinary-prose",
        "Checks which skills apply before starting a task.",
    )
    mandatory_rule_paths = [
        _write_skill(
            tmp_path / "must-rule",
            "You must invoke using-skills before any action.",
        ),
        _write_skill(
            tmp_path / "always-rule",
            "Always invoke using-skills before any action.",
        ),
        _write_skill(
            tmp_path / "no-exceptions-rule",
            "Invoke using-skills before any action. No exceptions.",
        ),
    ]

    assert checker.check_skill_description(valid_skill_path) == []
    assert checker.check_skill_description(workflow_summary_path) == [WORKFLOW_SUMMARY_ERROR]
    assert checker.check_skill_description(ordinary_prose_path) == [WORKFLOW_SUMMARY_ERROR]
    for mandatory_rule_path in mandatory_rule_paths:
        assert checker.check_skill_description(mandatory_rule_path) == [WORKFLOW_SUMMARY_ERROR]
