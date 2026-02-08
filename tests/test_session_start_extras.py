"""Tests for session_start_extras hook."""

import importlib.util
import json
from datetime import UTC, datetime, timedelta
from pathlib import Path

# Load the hook module directly from .claude/hooks/ (not a package)
_spec = importlib.util.spec_from_file_location(
    "session_start_extras",
    Path(__file__).resolve().parent.parent / ".claude" / "hooks" / "session_start_extras.py",
)
_mod = importlib.util.module_from_spec(_spec)  # type: ignore[arg-type]
_spec.loader.exec_module(_mod)  # type: ignore[union-attr]

find_stale_beads = _mod.find_stale_beads
find_merged_worktrees = _mod.find_merged_worktrees
recent_learnings = _mod.recent_learnings


# --- find_stale_beads ---


class TestFindStaleBeads:
    def test_no_beads(self):
        assert find_stale_beads("", days_threshold=3) == []

    def test_fresh_bead_not_stale(self):
        today = datetime.now(UTC).strftime("%Y-%m-%d")
        bd_output = f"◐ oro-a9r [● P2] [feature] - Some task\n  Updated: {today}\n"
        assert find_stale_beads(bd_output, days_threshold=3) == []

    def test_stale_bead_detected(self):
        old_date = (datetime.now(UTC) - timedelta(days=5)).strftime("%Y-%m-%d")
        bd_output = f"◐ oro-xyz [● P2] [feature] - Old task\n  Updated: {old_date}\n"
        result = find_stale_beads(bd_output, days_threshold=3)
        assert len(result) == 1
        assert result[0]["id"] == "oro-xyz"
        assert result[0]["days_stale"] >= 5

    def test_mixed_fresh_and_stale(self):
        today = datetime.now(UTC).strftime("%Y-%m-%d")
        old_date = (datetime.now(UTC) - timedelta(days=10)).strftime("%Y-%m-%d")
        bd_output = (
            f"◐ oro-aaa [● P2] [feature] - Fresh\n"
            f"  Updated: {today}\n"
            f"◐ oro-bbb [● P1] [bug] - Stale\n"
            f"  Updated: {old_date}\n"
        )
        result = find_stale_beads(bd_output, days_threshold=3)
        assert len(result) == 1
        assert result[0]["id"] == "oro-bbb"

    def test_exactly_at_threshold_not_stale(self):
        boundary_date = (datetime.now(UTC) - timedelta(days=3)).strftime("%Y-%m-%d")
        bd_output = f"◐ oro-edge [● P2] [feature] - Boundary\n  Updated: {boundary_date}\n"
        # Exactly 3 days is NOT stale (need >3)
        result = find_stale_beads(bd_output, days_threshold=3)
        assert len(result) == 0

    def test_custom_threshold(self):
        old_date = (datetime.now(UTC) - timedelta(days=2)).strftime("%Y-%m-%d")
        bd_output = f"◐ oro-ccc [● P2] [feature] - Task\n  Updated: {old_date}\n"
        assert find_stale_beads(bd_output, days_threshold=1) != []
        assert find_stale_beads(bd_output, days_threshold=5) == []


# --- find_merged_worktrees ---


class TestFindMergedWorktrees:
    def test_no_worktrees_dir(self, tmp_path):
        nonexistent = tmp_path / "nope"
        assert find_merged_worktrees(str(nonexistent)) == []

    def test_empty_worktrees_dir(self, tmp_path):
        wt_dir = tmp_path / ".worktrees"
        wt_dir.mkdir()
        assert find_merged_worktrees(str(wt_dir)) == []

    def test_finds_merged_worktree(self, tmp_path):
        """Use a real git repo to test merged branch detection."""
        import subprocess

        # Create a git repo with main branch
        repo = tmp_path / "repo"
        repo.mkdir()
        subprocess.run(["git", "init", "-b", "main"], cwd=repo, capture_output=True)
        subprocess.run(["git", "config", "user.email", "test@test.com"], cwd=repo, capture_output=True)
        subprocess.run(["git", "config", "user.name", "Test"], cwd=repo, capture_output=True)
        (repo / "file.txt").write_text("hello")
        subprocess.run(["git", "add", "."], cwd=repo, capture_output=True)
        subprocess.run(["git", "commit", "-m", "init"], cwd=repo, capture_output=True)

        # Create a feature branch and merge it
        subprocess.run(["git", "checkout", "-b", "agent/test-merged"], cwd=repo, capture_output=True)
        (repo / "feature.txt").write_text("feature")
        subprocess.run(["git", "add", "."], cwd=repo, capture_output=True)
        subprocess.run(["git", "commit", "-m", "feature"], cwd=repo, capture_output=True)
        subprocess.run(["git", "checkout", "main"], cwd=repo, capture_output=True)
        subprocess.run(["git", "merge", "agent/test-merged"], cwd=repo, capture_output=True)

        # Create worktrees dir with a symlink-like structure
        wt_dir = tmp_path / ".worktrees"
        wt_dir.mkdir()
        # Add a worktree
        subprocess.run(
            ["git", "worktree", "add", str(wt_dir / "bead-test"), "agent/test-merged"],
            cwd=repo,
            capture_output=True,
        )

        result = find_merged_worktrees(str(wt_dir), main_branch="main")
        assert len(result) == 1
        assert result[0]["branch"] == "agent/test-merged"
        assert "bead-test" in result[0]["path"]

    def test_unmerged_worktree_excluded(self, tmp_path):
        """Unmerged branches should not appear."""
        import subprocess

        repo = tmp_path / "repo"
        repo.mkdir()
        subprocess.run(["git", "init", "-b", "main"], cwd=repo, capture_output=True)
        subprocess.run(["git", "config", "user.email", "test@test.com"], cwd=repo, capture_output=True)
        subprocess.run(["git", "config", "user.name", "Test"], cwd=repo, capture_output=True)
        (repo / "file.txt").write_text("hello")
        subprocess.run(["git", "add", "."], cwd=repo, capture_output=True)
        subprocess.run(["git", "commit", "-m", "init"], cwd=repo, capture_output=True)

        # Create feature branch but do NOT merge
        subprocess.run(["git", "checkout", "-b", "agent/unmerged"], cwd=repo, capture_output=True)
        (repo / "feature.txt").write_text("wip")
        subprocess.run(["git", "add", "."], cwd=repo, capture_output=True)
        subprocess.run(["git", "commit", "-m", "wip"], cwd=repo, capture_output=True)
        subprocess.run(["git", "checkout", "main"], cwd=repo, capture_output=True)

        wt_dir = tmp_path / ".worktrees"
        wt_dir.mkdir()
        subprocess.run(
            ["git", "worktree", "add", str(wt_dir / "bead-unmerged"), "agent/unmerged"],
            cwd=repo,
            capture_output=True,
        )

        result = find_merged_worktrees(str(wt_dir), main_branch="main")
        assert result == []


# --- recent_learnings ---


class TestRecentLearnings:
    def _write_knowledge(self, path: Path, entries: list[dict]) -> str:
        path.parent.mkdir(parents=True, exist_ok=True)
        with open(path, "w") as f:
            for e in entries:
                f.write(json.dumps(e) + "\n")
        return str(path)

    def test_missing_file(self, tmp_path):
        assert recent_learnings(str(tmp_path / "nope.jsonl")) == []

    def test_empty_file(self, tmp_path):
        p = tmp_path / "knowledge.jsonl"
        p.write_text("")
        assert recent_learnings(str(p)) == []

    def test_returns_up_to_n(self, tmp_path):
        entries = [
            {
                "key": f"learned-{i}",
                "type": "learned",
                "content": f"fact {i}",
                "bead": "b1",
                "tags": [],
                "ts": f"2026-01-{10 + i:02d}T00:00:00+00:00",
            }
            for i in range(10)
        ]
        p = self._write_knowledge(tmp_path / "knowledge.jsonl", entries)
        result = recent_learnings(p, n=5)
        assert len(result) == 5

    def test_dedup_by_key_keeps_latest(self, tmp_path):
        entries = [
            {
                "key": "learned-pytest",
                "type": "learned",
                "content": "old fact",
                "bead": "b1",
                "tags": ["pytest"],
                "ts": "2026-01-01T00:00:00+00:00",
            },
            {
                "key": "learned-pytest",
                "type": "learned",
                "content": "new fact",
                "bead": "b2",
                "tags": ["pytest"],
                "ts": "2026-01-10T00:00:00+00:00",
            },
        ]
        p = self._write_knowledge(tmp_path / "knowledge.jsonl", entries)
        result = recent_learnings(p, n=5)
        assert len(result) == 1
        assert result[0]["content"] == "new fact"

    def test_only_learned_type(self, tmp_path):
        entries = [
            {
                "key": "learned-a",
                "type": "learned",
                "content": "a",
                "bead": "b1",
                "tags": [],
                "ts": "2026-01-01T00:00:00+00:00",
            },
            {
                "key": "other-b",
                "type": "context",
                "content": "b",
                "bead": "b1",
                "tags": [],
                "ts": "2026-01-02T00:00:00+00:00",
            },
        ]
        p = self._write_knowledge(tmp_path / "knowledge.jsonl", entries)
        result = recent_learnings(p, n=5)
        assert len(result) == 1
        assert result[0]["content"] == "a"

    def test_sorted_by_ts_descending(self, tmp_path):
        entries = [
            {
                "key": "learned-old",
                "type": "learned",
                "content": "old",
                "bead": "b1",
                "tags": [],
                "ts": "2026-01-01T00:00:00+00:00",
            },
            {
                "key": "learned-new",
                "type": "learned",
                "content": "new",
                "bead": "b2",
                "tags": ["git"],
                "ts": "2026-01-15T00:00:00+00:00",
            },
            {
                "key": "learned-mid",
                "type": "learned",
                "content": "mid",
                "bead": "b1",
                "tags": [],
                "ts": "2026-01-10T00:00:00+00:00",
            },
        ]
        p = self._write_knowledge(tmp_path / "knowledge.jsonl", entries)
        result = recent_learnings(p, n=5)
        assert [r["content"] for r in result] == ["new", "mid", "old"]

    def test_format_includes_bead_and_tags(self, tmp_path):
        entries = [
            {
                "key": "learned-x",
                "type": "learned",
                "content": "use ruff",
                "bead": "oro-abc",
                "tags": ["python", "lint"],
                "ts": "2026-01-05T00:00:00+00:00",
            },
        ]
        p = self._write_knowledge(tmp_path / "knowledge.jsonl", entries)
        result = recent_learnings(p, n=5)
        assert result[0]["bead"] == "oro-abc"
        assert result[0]["tags"] == ["python", "lint"]

    def test_malformed_lines_skipped(self, tmp_path):
        p = tmp_path / "knowledge.jsonl"
        p.write_text(
            "not json\n"
            + json.dumps(
                {
                    "key": "learned-ok",
                    "type": "learned",
                    "content": "ok",
                    "bead": "b1",
                    "tags": [],
                    "ts": "2026-01-01T00:00:00+00:00",
                }
            )
            + "\n"
        )
        result = recent_learnings(str(p), n=5)
        assert len(result) == 1
