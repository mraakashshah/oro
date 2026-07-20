"""Behavioral contracts for high-leverage workflow skills."""

from pathlib import Path

REPO_ROOT = Path(__file__).parent.parent
SKILL_NAMES = (
    "spec",
    "brainstorming",
    "create-handoff",
    "dispatching-parallel-agents",
)


def _skill(name: str) -> str:
    return (REPO_ROOT / "assets" / "skills" / name / "SKILL.md").read_text()


def _section(text: str, start: str, end: str) -> str:
    return text.split(start, 1)[1].split(end, 1)[0]


def test_spec_runs_private_tiger_premortem_in_both_modes() -> None:
    skill = _skill("spec")
    quick = _section(skill, "## Quick Mode", "## Full Mode")
    full = _section(skill, "## Full Mode", "## Red Flags")

    for category in ("Tiger", "Paper Tiger", "Elephant"):
        assert category in skill
    assert "Internal Premortem" in quick
    assert "Internal Premortem" in full
    assert "do not run a second premortem pass" in full
    assert (
        "Compare approaches → Internal Leverage Pass → brainstorming's single Internal Premortem → finalize"
        in full
    )
    assert "Keep the premortem private" in skill
    assert "verified material risks" in skill


def test_brainstorming_leaves_adversarial_review_to_its_caller() -> None:
    skill = _skill("brainstorming")

    assert "The invoking workflow owns adversarial review" in skill
    assert "Apply the premortem taxonomy privately" in skill
    assert "Use the `premortem` skill" not in skill
    assert "adversarial-spec-review" not in skill
    assert "Spawn a fresh-context subagent" not in skill
    assert "The audit proved" not in skill
    assert len(skill.split()) < 650


def test_handoff_only_signals_dispatcher_for_an_oro_worker() -> None:
    skill = _skill("create-handoff")

    assert "only when `ORO_WORKER=1`" in skill
    assert "For every other handoff, stop after writing the handoff document" in skill
    assert "touch .oro/handoff_done" in skill
    assert skill.count("## Principles") == 1
    assert len(skill.split()) < 550


def test_parallel_dispatch_delegates_integration_to_oro_work() -> None:
    skill = _skill("dispatching-parallel-agents")

    assert "`oro work` owns integration" in skill
    assert "preserve the reported worktree and branch" in skill
    assert "Preserve suspected abandoned worktrees until integration is proven" in skill
    assert "explicit cleanup approval" in skill
    for unsafe_default in (
        "git stash",
        "git worktree remove --force",
        "git rebase",
        "git checkout",
        "git branch -D",
        "Rebase + fast-forward merge. Always.",
    ):
        assert unsafe_default not in skill
    assert len(skill.split()) < 700


def test_revised_skill_bodies_match_agent_mirrors() -> None:
    for name in SKILL_NAMES:
        canonical = _skill(name)
        for root in (".agents", ".claude"):
            mirror = REPO_ROOT / root / "skills" / name / "SKILL.md"
            assert mirror.read_text() == canonical
