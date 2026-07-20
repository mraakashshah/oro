"""Behavioral contracts for high-leverage workflow skills."""

from pathlib import Path

REPO_ROOT = Path(__file__).parent.parent
SKILL_NAMES = (
    "spec",
    "brainstorming",
    "create-handoff",
    "dispatching-parallel-agents",
    "using-skills",
    "finishing-work",
    "workflow-routing",
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
    assert "Compare approaches → Internal Leverage Pass → brainstorming's single Internal Premortem → finalize" in full
    assert "Keep the premortem private" in skill
    assert "verified material risks" in skill


def test_spec_uses_beadcraft_only_for_existing_oro_projects() -> None:
    skill = _skill("spec")

    assert "only when the current project is already Oro-managed" in skill
    assert "Outside an Oro-managed project" in skill
    assert "Do not initialize Oro" in skill
    assert "native tracker or implementation plan" in skill


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


def test_using_skills_keeps_one_percent_rule_with_material_matching() -> None:
    skill = _skill("using-skills")

    assert "1% chance" in skill
    assert "materially matches" in skill
    assert "Topical adjacency is not a material match" in skill
    assert "already Oro-managed" in skill
    assert "Do not initialize Oro just to invoke `beadcraft`" in skill
    assert "Outside an Oro-managed project" in skill


def test_workflow_routing_respects_oro_only_beadcraft_boundary() -> None:
    skill = _skill("workflow-routing")

    assert "only when the current project is already Oro-managed" in skill
    assert "Outside an Oro-managed project" in skill
    assert "Do not initialize Oro" in skill
    assert "When Encode produced Oro tasks" in skill
    assert "When Encode produced a native" in skill
    assert "`executing-beads` only for Oro tasks; native execution otherwise" in skill


def test_finishing_work_uses_conditional_documentation_and_single_landing_owner() -> None:
    skill = _skill("finishing-work")

    assert "Invoke `review-docs` only when" in skill
    assert "Invoke `documenting-solutions` only when" in skill
    assert "The selected integration option owns commit and push" in skill
    assert "Before executing Option 1 or 2" in skill
    assert "invoke `git-commits`" in skill
    assert "Then invoke `review-docs`" not in skill
    assert "This is not optional" not in skill
    assert "### Step 7: Landing the Plane" not in skill
    assert "an active tool's working directory" in skill
    assert "Claude Code bug #9190" not in skill
    assert "Codex bug #9190" not in skill
    assert 'echo "bash ok"' not in skill
    assert "verify bash" not in skill.lower()
    assert skill.index("### Step 3: Reflect and Document") < skill.index("### Step 5: Execute Choice")
    assert "explicitly set its working directory" in skill


def test_worktree_removal_is_tool_neutral_and_uses_explicit_working_directory() -> None:
    paths = [REPO_ROOT / "assets" / "skills" / "using-git-worktrees" / "SKILL.md"]
    paths.extend(REPO_ROOT / root / "skills" / "using-git-worktrees" / "SKILL.md" for root in (".agents", ".claude"))

    for path in paths:
        skill = path.read_text()
        assert "an active tool's working directory" in skill
        assert "explicitly set its working directory" in skill
        assert "Claude Code bug #9190" not in skill
        assert "Codex bug #9190" not in skill
        assert 'echo "bash ok"' not in skill
        assert "verify bash" not in skill.lower()


def test_revised_skill_bodies_match_agent_mirrors() -> None:
    for name in SKILL_NAMES:
        canonical = _skill(name)
        for root in (".agents", ".claude"):
            mirror = REPO_ROOT / root / "skills" / name / "SKILL.md"
            assert mirror.read_text() == canonical
