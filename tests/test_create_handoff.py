"""Tests for the create-handoff skill's context summary writing behaviour.

Verifies that write_context_summary() correctly writes .oro/context_summary.txt
before .oro/handoff_done is touched, so that continuation beads receive context.
"""

from __future__ import annotations

from pathlib import Path

# write_context_summary is in assets/hooks/ — added to sys.path by conftest.py
from write_context_summary import write_context_summary


class TestCreateHandoffWritesContextSummary:
    """Acceptance tests: write_context_summary writes .oro/context_summary.txt."""

    def test_writes_context_summary_with_non_empty_content(self, tmp_path: Path) -> None:
        """(1) context_summary.txt is written with non-empty content."""
        write_context_summary(
            goal="implement context summary for continuation beads",
            now="run quality gate and commit",
            worktree_root=tmp_path,
        )
        summary_path = tmp_path / ".oro" / "context_summary.txt"
        assert summary_path.exists(), "context_summary.txt must be written"
        assert summary_path.read_text(encoding="utf-8").strip() != "", (
            "context_summary.txt must contain non-empty content"
        )

    def test_does_not_touch_handoff_done(self, tmp_path: Path) -> None:
        """(2) write_context_summary does NOT create handoff_done — ordering preserved.

        The dispatcher reads context_summary.txt during handoff processing.
        handoff_done must be touched by the caller AFTER this function returns,
        ensuring the summary is available before the dispatcher acts.
        """
        write_context_summary(
            goal="some goal",
            now="some next action",
            worktree_root=tmp_path,
        )
        handoff_done = tmp_path / ".oro" / "handoff_done"
        assert not handoff_done.exists(), (
            "write_context_summary must not touch handoff_done; "
            "caller is responsible for touching it AFTER summary is written"
        )

    def test_summary_includes_goal_field(self, tmp_path: Path) -> None:
        """(3a) The summary includes the goal field."""
        goal = "fix handoff context summary bug in create-handoff skill"
        write_context_summary(goal=goal, now="run tests", worktree_root=tmp_path)
        content = (tmp_path / ".oro" / "context_summary.txt").read_text(encoding="utf-8")
        assert goal in content, f"Expected goal text in summary, got: {content!r}"

    def test_summary_includes_now_field(self, tmp_path: Path) -> None:
        """(3b) The summary includes the current state / next-step field."""
        now = "run uv run pytest tests/test_create_handoff.py -v"
        write_context_summary(goal="some goal", now=now, worktree_root=tmp_path)
        content = (tmp_path / ".oro" / "context_summary.txt").read_text(encoding="utf-8")
        assert now in content, f"Expected 'now' text in summary, got: {content!r}"

    def test_creates_oro_dir_if_missing(self, tmp_path: Path) -> None:
        """Creates .oro/ directory when it does not exist."""
        assert not (tmp_path / ".oro").exists()
        write_context_summary(goal="goal", now="next", worktree_root=tmp_path)
        assert (tmp_path / ".oro").is_dir()

    def test_overwrites_existing_context_summary(self, tmp_path: Path) -> None:
        """Overwrites an existing context_summary.txt (edge: file already exists)."""
        oro_dir = tmp_path / ".oro"
        oro_dir.mkdir()
        (oro_dir / "context_summary.txt").write_text("stale content from prior run", encoding="utf-8")

        write_context_summary(goal="new goal", now="new next", worktree_root=tmp_path)

        content = (oro_dir / "context_summary.txt").read_text(encoding="utf-8")
        assert "new goal" in content
        assert "stale content" not in content
