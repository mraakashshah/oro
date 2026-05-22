"""Tests for the resume-handoff skill instructions."""

from __future__ import annotations

from pathlib import Path


def test_resume_handoff_skill_reads_task_state() -> None:
    """resume-handoff must preserve tracked task state from handoffs."""
    skill_path = Path("assets/skills/resume-handoff/SKILL.md")
    content = skill_path.read_text(encoding="utf-8")

    required_phrases = [
        "tasks.completed",
        "tasks.in_progress",
        "tasks.remaining",
        "tasks.epic",
        "Verify tracked task state before continuing",
        "Handoffs without a `tasks:` section are incomplete state",
        "Complete `tasks.in_progress` entries before starting `tasks.remaining`",
    ]

    for phrase in required_phrases:
        assert phrase in content
