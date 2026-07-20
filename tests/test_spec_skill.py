"""Contract tests for the spec skill workflow."""

from pathlib import Path

REPO_ROOT = Path(__file__).parent.parent
SPEC_SKILL = REPO_ROOT / "assets" / "skills" / "spec" / "SKILL.md"


def _section(text: str, start: str, end: str) -> str:
    return text.split(start, 1)[1].split(end, 1)[0]


def _body(text: str) -> str:
    return text.split("---", 2)[2].lstrip()


def test_quick_and_full_modes_run_the_internal_leverage_pass() -> None:
    skill = SPEC_SKILL.read_text()
    quick = _section(skill, "## Quick Mode", "## Full Mode")
    full = _section(skill, "## Full Mode", "## Red Flags")

    assert "Internal Leverage Pass" in quick
    assert "Internal Leverage Pass" in full


def test_internal_leverage_pass_covers_direction_simplicity_impact_and_scale() -> None:
    skill = SPEC_SKILL.read_text().lower()

    for phrase in (
        "most useful",
        "assumptions became stale",
        "should not exist",
        "radically simpler",
        "theoretically best",
        "half the timeline",
        "double impact",
        "money is less constrained than talent",
        "dream in years",
        "plan in months",
        "evaluate in weeks",
        "ship daily",
        "1x",
        "10x",
        "100x",
    ):
        assert phrase in skill


def test_leverage_review_is_internal_and_only_surfaces_material_decisions() -> None:
    skill = SPEC_SKILL.read_text().lower()

    assert "run this privately" in skill
    assert "material" in skill
    assert "ask the user to decide, one material decision at a time" in skill
    assert "do not proceed until it is decided" in skill
    assert "when no material decisions remain" in skill
    assert "not a user questionnaire" in skill


def test_consultation_ceremony_was_removed() -> None:
    skill = SPEC_SKILL.read_text()

    for legacy in (
        "The six forcing questions",
        "Assumption ledger",
        "LEDGER",
        "Consultation ← GATE",
        "Stage 2 Consultation",
    ):
        assert legacy not in skill


def test_spec_skill_remains_concise_and_has_matching_agent_bodies() -> None:
    canonical = SPEC_SKILL.read_text()
    assert len(canonical.split()) < 900

    for mirror in (
        REPO_ROOT / ".agents" / "skills" / "spec" / "SKILL.md",
        REPO_ROOT / ".claude" / "skills" / "spec" / "SKILL.md",
    ):
        assert _body(mirror.read_text()) == _body(canonical)
