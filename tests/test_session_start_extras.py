"""Tests for session_start_extras hook."""

import importlib.util
import json
import os
from datetime import UTC, datetime, timedelta
from pathlib import Path

# Load the hook module from ORO_HOME/hooks/ (externalized config)
_oro_home = Path(os.environ.get("ORO_HOME", Path.home() / ".oro"))
_spec = importlib.util.spec_from_file_location(
    "session_start_extras",
    _oro_home / "hooks" / "session_start_extras.py",
)
_mod = importlib.util.module_from_spec(_spec)  # type: ignore[arg-type]
_spec.loader.exec_module(_mod)  # type: ignore[union-attr]

find_stale_beads = _mod.find_stale_beads
find_merged_worktrees = _mod.find_merged_worktrees
recent_learnings = _mod.recent_learnings
session_banner = _mod.session_banner
role_beacon = _mod.role_beacon
pane_handoff = _mod.pane_handoff
latest_handoff = _mod.latest_handoff


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


# --- session_banner ---


class TestSessionBanner:
    def test_empty_when_no_beads(self):
        assert session_banner([], []) == ""

    def test_just_finished_only(self):
        closed = [{"id": "oro-abc", "title": "Fix bug"}]
        result = session_banner(closed, [])
        assert "Just finished:" in result
        assert "✓ oro-abc: Fix bug" in result
        assert "Up next:" not in result

    def test_up_next_only(self):
        ready = [{"id": "oro-xyz", "title": "Add feature"}]
        result = session_banner([], ready)
        assert "Up next:" in result
        assert "→ oro-xyz: Add feature" in result
        assert "Just finished:" not in result

    def test_both_sections(self):
        closed = [{"id": "oro-aaa", "title": "Done task"}]
        ready = [{"id": "oro-bbb", "title": "Next task"}]
        result = session_banner(closed, ready)
        assert "Just finished:" in result
        assert "✓ oro-aaa: Done task" in result
        assert "Up next:" in result
        assert "→ oro-bbb: Next task" in result
        # "Just finished" appears before "Up next"
        assert result.index("Just finished:") < result.index("Up next:")

    def test_multiple_entries(self):
        closed = [
            {"id": "oro-1", "title": "First"},
            {"id": "oro-2", "title": "Second"},
        ]
        ready = [
            {"id": "oro-3", "title": "Third"},
            {"id": "oro-4", "title": "Fourth"},
        ]
        result = session_banner(closed, ready)
        assert result.count("✓") == 2
        assert result.count("→") == 2


# --- role_beacon ---


class TestRoleBeacon:
    def test_empty_role_returns_empty(self):
        assert role_beacon("") == ""

    def test_none_role_returns_empty(self):
        assert role_beacon("", beacons_dir="/nonexistent") == ""

    def test_unknown_role_returns_empty(self, tmp_path):
        beacons_dir = tmp_path / "beacons"
        beacons_dir.mkdir()
        assert role_beacon("unknown", beacons_dir=str(beacons_dir)) == ""

    def test_architect_beacon_loaded(self, tmp_path):
        beacons_dir = tmp_path / "beacons"
        beacons_dir.mkdir()
        (beacons_dir / "architect.md").write_text("## Role\nYou are the architect.")
        result = role_beacon("architect", beacons_dir=str(beacons_dir))
        assert "You are the architect" in result
        assert "## Role" in result

    def test_manager_beacon_loaded(self, tmp_path):
        beacons_dir = tmp_path / "beacons"
        beacons_dir.mkdir()
        (beacons_dir / "manager.md").write_text("# Manager\nYou coordinate work.")
        result = role_beacon("manager", beacons_dir=str(beacons_dir))
        assert "You coordinate work" in result

    def test_missing_beacons_dir_returns_empty(self):
        result = role_beacon("architect", beacons_dir="/nonexistent/path/beacons")
        assert result == ""

    def test_real_beacon_files_exist(self):
        """Verify the actual beacon files in ORO_HOME are loadable."""
        beacons_dir = _oro_home / "beacons"

        architect = role_beacon("architect", beacons_dir=str(beacons_dir))
        assert len(architect) > 500, "architect beacon should be substantial"
        assert "## Role" in architect
        assert "architect" in architect.lower()

        manager = role_beacon("manager", beacons_dir=str(beacons_dir))
        assert len(manager) > 500, "manager beacon should be substantial"
        assert "## Role" in manager
        assert "manager" in manager.lower()


# --- pane_handoff ---


class TestPaneHandoff:
    def test_empty_role_returns_empty(self, tmp_path):
        assert pane_handoff("", panes_dir=str(tmp_path)) == ""

    def test_no_panes_dir_returns_empty(self):
        assert pane_handoff("architect", panes_dir="/nonexistent/panes") == ""

    def test_missing_handoff_file_returns_empty(self, tmp_path):
        role_dir = tmp_path / "architect"
        role_dir.mkdir()
        # No handoff.yaml inside
        assert pane_handoff("architect", panes_dir=str(tmp_path)) == ""

    def test_valid_handoff_returned(self, tmp_path):
        role_dir = tmp_path / "manager"
        role_dir.mkdir()
        content = "---\ngoal: test handoff\nnow: doing stuff\n"
        (role_dir / "handoff.yaml").write_text(content)
        result = pane_handoff("manager", panes_dir=str(tmp_path))
        assert "## Latest Handoff (Auto-Recovery)" in result
        assert "goal: test handoff" in result
        assert "```yaml" in result

    def test_malformed_yaml_returns_empty_with_warning(self, tmp_path, capfd):
        role_dir = tmp_path / "testbad"
        role_dir.mkdir()
        (role_dir / "handoff.yaml").write_text("---\nthis is: not: valid: yaml:\n---")
        result = pane_handoff("testbad", panes_dir=str(tmp_path))
        assert result == ""
        captured = capfd.readouterr()
        assert "warning" in captured.err.lower() or "malformed" in captured.err.lower()

    def test_truncation_at_2000_chars(self, tmp_path):
        role_dir = tmp_path / "architect"
        role_dir.mkdir()
        content = "---\ngoal: " + "x" * 2100 + "\n"
        (role_dir / "handoff.yaml").write_text(content)
        result = pane_handoff("architect", panes_dir=str(tmp_path))
        assert "...(truncated)" in result

    def test_pane_handoff_takes_priority_over_dir(self, tmp_path):
        """When pane handoff exists, latest_handoff_with_role should prefer it."""
        # Set up pane handoff
        panes = tmp_path / "panes"
        role_dir = panes / "manager"
        role_dir.mkdir(parents=True)
        (role_dir / "handoff.yaml").write_text("---\ngoal: pane handoff\n")

        # Set up directory handoff
        handoffs = tmp_path / "handoffs"
        handoffs.mkdir()
        (handoffs / "2026-02-15.yaml").write_text("---\ngoal: dir handoff\n")

        # Pane handoff should win
        result = pane_handoff("manager", panes_dir=str(panes))
        assert "pane handoff" in result

    def test_real_pane_handoff_files(self):
        """Verify the actual pane handoff files in ORO_HOME are loadable."""
        panes_dir = _oro_home / "panes"
        if not panes_dir.is_dir():
            return  # Skip if no panes dir

        manager_handoff = pane_handoff("manager", panes_dir=str(panes_dir))
        if (panes_dir / "manager" / "handoff.yaml").is_file():
            assert len(manager_handoff) > 0, "manager pane handoff should be non-empty"
            assert "## Latest Handoff" in manager_handoff
