"""Tests for session_start_extras hook."""

import importlib.util
import json
import os
import re
from datetime import UTC, datetime, timedelta
from pathlib import Path

# Load the hook module from source (assets/hooks/) for testing
_repo_root = Path(__file__).parent.parent
_spec = importlib.util.spec_from_file_location(
    "session_start_extras",
    _repo_root / "assets" / "hooks" / "session_start_extras.py",
)
_mod = importlib.util.module_from_spec(_spec)  # type: ignore[arg-type]
_spec.loader.exec_module(_mod)  # type: ignore[union-attr]

# Also keep reference to ORO_HOME for real file tests
_oro_home = Path(os.environ.get("ORO_HOME", Path.home() / ".oro"))

find_stale_beads = _mod.find_stale_beads
find_merged_worktrees = _mod.find_merged_worktrees
recent_memories_db = _mod.recent_memories_db
session_banner = _mod.session_banner
role_beacon = _mod.role_beacon
pane_handoff = _mod.pane_handoff
latest_handoff = _mod.latest_handoff


def test_superpowers_commit_instruction_is_non_interactive():
    assert "git commit -m" in _mod._SUPERPOWERS
    assert "git add` → `git commit`" not in _mod._SUPERPOWERS


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

        manager = role_beacon("manager", beacons_dir=str(beacons_dir))
        assert len(manager) > 500, "manager beacon should be substantial"
        assert "## Role" in manager
        assert "manager" in manager.lower()


class TestRoleBeaconTaskTerminology:
    PRIMARY_BEAD_COMMAND = re.compile(r"\boro\s+bead\s+(ready|create|show|close|dep|status|blocked|list|closed)\b")

    def test_checked_in_beacons_are_task_primary(self):
        beacon_paths = [
            _repo_root / "assets" / "beacons" / "manager.md",
            _repo_root / ".claude" / "hooks" / "beacons" / "manager.md",
        ]

        for path in beacon_paths:
            text = path.read_text()
            assert "oro task" in text, f"{path} should teach task-primary commands"
            assert not self.PRIMARY_BEAD_COMMAND.search(text), f"{path} should not teach primary oro bead commands"

        manager = (_repo_root / "assets" / "beacons" / "manager.md").read_text()
        assert "oro worker launch --bead <task-id>" in manager
        assert "legacy flag" in manager.lower()

    def test_manager_beacons_require_autonomous_routine_operations(self):
        beacon_paths = [
            _repo_root / "assets" / "beacons" / "manager.md",
            _repo_root / ".claude" / "hooks" / "beacons" / "manager.md",
        ]

        for path in beacon_paths:
            text = path.read_text()
            assert "Proceed autonomously" in text
            assert "Do not ask whether to claim" in text
            assert "whether to let the dispatcher assign" in text
            assert "Do not announce or enter long sleeps" in text
            assert "Do not create memory files" in text

    def test_superpowers_context_uses_task_commands(self):
        assert "oro task ready" in _mod._SUPERPOWERS
        assert "oro task close" in _mod._SUPERPOWERS
        assert "create tasks for remaining work" in _mod._SUPERPOWERS
        assert "oro bead ready" not in _mod._SUPERPOWERS
        assert "oro bead close" not in _mod._SUPERPOWERS
        assert "create beads" not in _mod._SUPERPOWERS
        assert "beads for remaining work" not in _mod._SUPERPOWERS

    def test_session_start_queries_native_task_alias(self, monkeypatch):
        calls = []

        class Result:
            returncode = 0

            def __init__(self, stdout):
                self.stdout = stdout

        def fake_run(cmd, **_kwargs):
            calls.append(cmd)
            if cmd[:3] == ["oro", "task", "closed"]:
                return Result('[{"id":"oro-done","title":"Done"}]')
            if cmd[:3] == ["oro", "task", "ready"]:
                return Result('[{"id":"oro-ready","title":"Ready"}]')
            msg = f"unexpected command: {cmd!r}"
            raise AssertionError(msg)

        monkeypatch.setattr(_mod.subprocess, "run", fake_run)

        assert _mod.recently_closed_beads(limit=1) == [{"id": "oro-done", "title": "Done"}]
        assert _mod.ready_beads(limit=1) == [{"id": "oro-ready", "title": "Ready"}]

        assert ["oro", "task", "closed", "--limit=1", "--json"] in calls
        assert ["oro", "task", "ready", "--json"] in calls

    def test_session_start_source_has_no_primary_bead_subprocesses(self):
        source = (_repo_root / "assets" / "hooks" / "session_start_extras.py").read_text()
        assert '["oro", "task", "list", "--status=in_progress", "--json"]' in source
        assert '["oro", "bead"' not in source
        assert not self.PRIMARY_BEAD_COMMAND.search(source)

    def test_stale_task_output_is_task_primary(self):
        output = _mod._format_output([{"id": "oro-old", "title": "Old", "days_stale": 4}], [], [])
        assert "## Stale Tasks" in output
        assert "Stale Beads" not in output


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


# --- auto_load_skills ---


auto_load_skills = _mod.auto_load_skills


class TestAutoLoadSkills:
    def test_valid_file_returns_formatted_content(self, tmp_path):
        """Valid SKILL.md should return formatted content with skill name from parent dir."""
        skill_dir = tmp_path / "using-skills"
        skill_dir.mkdir()
        skills_file = skill_dir / "SKILL.md"
        skills_file.write_text("# Skill Content\n\nThis is skill documentation.")

        result = auto_load_skills(str(skills_file))

        assert result != ""
        assert "# Skill Content" in result
        assert "This is skill documentation." in result
        # Skill name should come from parent directory, not file stem
        assert "# Auto-loaded Skill: using-skills" in result

    def test_flat_md_file_uses_stem_as_name(self, tmp_path):
        """Non-SKILL.md file should use file stem as skill name (backward compat)."""
        skills_file = tmp_path / "my-skill.md"
        skills_file.write_text("# My Skill")

        result = auto_load_skills(str(skills_file))

        assert "# Auto-loaded Skill: my-skill" in result

    def test_missing_file_returns_empty_and_logs_warning(self, tmp_path, capfd):
        """Missing file should return empty string and log warning."""
        nonexistent = tmp_path / "nonexistent.md"

        result = auto_load_skills(str(nonexistent))

        assert result == ""
        captured = capfd.readouterr()
        assert "warning" in captured.err.lower() or "not found" in captured.err.lower()

    def test_empty_file_returns_empty(self, tmp_path):
        """Empty file should return empty string."""
        empty_file = tmp_path / "empty.md"
        empty_file.write_text("")

        result = auto_load_skills(str(empty_file))

        assert result == ""


# --- main() integration ---


class TestMainIntegration:
    def _run_main_for_role(self, tmp_path, monkeypatch, role):
        import io
        import subprocess
        import sys

        oro_home = tmp_path / ".oro"
        monkeypatch.chdir(tmp_path)
        monkeypatch.setenv("ORO_HOME", str(oro_home))
        if role is None:
            monkeypatch.delenv("ORO_ROLE", raising=False)
        else:
            monkeypatch.setenv("ORO_ROLE", role)
        monkeypatch.delenv("ORO_WORKER", raising=False)

        calls: dict[str, list] = {"update": [], "beacon": [], "subprocess": []}

        def fake_update_pane_activity(pane_role):
            calls["update"].append(pane_role)

        def fake_role_beacon(pane_role):
            calls["beacon"].append(pane_role)
            return f"{pane_role} beacon" if pane_role else ""

        def fake_run(cmd, *args, **kwargs):
            calls["subprocess"].append(cmd)
            return subprocess.CompletedProcess(cmd, 0, stdout="[]\n", stderr="")

        monkeypatch.setattr(_mod, "update_pane_activity", fake_update_pane_activity)
        monkeypatch.setattr(_mod, "role_beacon", fake_role_beacon)
        monkeypatch.setattr(_mod, "pane_handoff", lambda _role: "")
        monkeypatch.setattr(_mod, "latest_handoff", lambda _dir: "handoff context")
        monkeypatch.setattr(_mod, "project_state", lambda: "state context")
        monkeypatch.setattr(_mod, "find_merged_worktrees", lambda _dir: [])
        monkeypatch.setattr(_mod, "recent_memories_db", lambda n=5: [])
        monkeypatch.setattr(_mod, "recently_closed_beads", lambda limit=3: [])
        monkeypatch.setattr(_mod, "ready_beads", lambda limit=4: [])
        monkeypatch.setattr(
            _mod, "auto_load_skills", lambda _path: "# Auto-loaded Skill: using-skills\n\nskill context"
        )
        monkeypatch.setattr(_mod.subprocess, "run", fake_run)
        monkeypatch.setattr(sys, "stdin", io.StringIO("{}"))

        mock_stdout = io.StringIO()
        monkeypatch.setattr(sys, "stdout", mock_stdout)

        _mod.main()
        output = json.loads(mock_stdout.getvalue())
        context = output["hookSpecificOutput"]["additionalContext"]
        return calls, context

    def test_main_architect_role_warns_and_skips_role_side_effects(self, tmp_path, monkeypatch, capsys):
        calls, context = self._run_main_for_role(tmp_path, monkeypatch, "architect")

        captured = capsys.readouterr()
        assert (
            "[oro] ORO_ROLE=architect is no longer supported — this value was removed. See release notes."
            in captured.err
        )
        assert calls["update"] == []
        assert calls["beacon"] == []
        assert "# Superpowers" in context
        assert "# Auto-loaded Skill: using-skills" in context
        assert "handoff context" in context
        assert "state context" in context

    def test_main_manager_role_updates_activity_and_injects_beacon(self, tmp_path, monkeypatch, capsys):
        calls, context = self._run_main_for_role(tmp_path, monkeypatch, "manager")

        captured = capsys.readouterr()
        assert "ORO_ROLE=architect is no longer supported" not in captured.err
        assert calls["update"] == ["manager"]
        assert calls["beacon"] == ["manager"]
        assert "# Role Beacon (manager)" in context
        assert "manager beacon" in context

    def test_main_unset_role_skips_role_side_effects(self, tmp_path, monkeypatch, capsys):
        calls, context = self._run_main_for_role(tmp_path, monkeypatch, None)

        captured = capsys.readouterr()
        assert "ORO_ROLE=architect is no longer supported" not in captured.err
        assert calls["update"] == []
        assert calls["beacon"] == []
        assert "# Superpowers" in context
        assert "# Auto-loaded Skill: using-skills" in context

    def test_auto_load_skills_injected_into_additional_context(self, tmp_path, monkeypatch):
        """Verify main() calls auto_load_skills and injects content into additionalContext."""
        import io
        import sys

        # Set up a fake skills file under ORO_HOME/.claude/skills/using-skills/SKILL.md
        oro_home = tmp_path / ".oro"
        skills_dir = oro_home / ".claude" / "skills" / "using-skills"
        skills_dir.mkdir(parents=True)
        using_skills_file = skills_dir / "SKILL.md"
        using_skills_file.write_text("# Using Skills\n\nAlways check for skills first.")

        # Monkeypatch to inject the skills directory into main()
        # We'll need to mock the call or set environment so main() uses our test skills
        monkeypatch.chdir(tmp_path)
        monkeypatch.setenv("ORO_HOME", str(tmp_path / ".oro"))

        # Create empty stdin
        mock_stdin = io.StringIO("{}")
        monkeypatch.setattr(sys, "stdin", mock_stdin)

        # Capture stdout
        mock_stdout = io.StringIO()
        monkeypatch.setattr(sys, "stdout", mock_stdout)

        # Run main()
        _mod.main()

        # Parse JSON output
        output = json.loads(mock_stdout.getvalue())
        additional_context = output["hookSpecificOutput"]["additionalContext"]

        # Verify auto-loaded skills content appears
        assert "# Auto-loaded Skill: using-skills" in additional_context
        assert "Always check for skills first." in additional_context

        # Verify order: Superpowers should come before auto-loaded skills
        superpowers_idx = additional_context.find("# Superpowers")
        skills_idx = additional_context.find("# Auto-loaded Skill: using-skills")
        assert superpowers_idx < skills_idx, "Superpowers should come before auto-loaded skills"

    def test_main_cleans_up_handoff_signals(self, tmp_path, monkeypatch):
        """Verify main() deletes stale handoff_requested and handoff_complete signals on SessionStart."""
        import io
        import sys

        # Set up panes directory with a role
        oro_home = tmp_path / ".oro"
        panes_dir = oro_home / "panes"
        role_dir = panes_dir / "test-worker"
        role_dir.mkdir(parents=True)

        # Create stale handoff signals
        requested_file = role_dir / "handoff_requested"
        complete_file = role_dir / "handoff_complete"
        requested_file.touch()
        complete_file.touch()
        assert requested_file.exists(), "handoff_requested should exist before main()"
        assert complete_file.exists(), "handoff_complete should exist before main()"

        # Set environment — clear ORO_WORKER to ensure the full non-worker path runs
        monkeypatch.chdir(tmp_path)
        monkeypatch.setenv("ORO_HOME", str(oro_home))
        monkeypatch.setenv("ORO_ROLE", "test-worker")
        monkeypatch.delenv("ORO_WORKER", raising=False)

        # Create empty stdin
        mock_stdin = io.StringIO("{}")
        monkeypatch.setattr(sys, "stdin", mock_stdin)

        # Capture stdout
        mock_stdout = io.StringIO()
        monkeypatch.setattr(sys, "stdout", mock_stdout)

        # Run main()
        _mod.main()

        # Verify both signals were deleted
        assert not requested_file.exists(), "handoff_requested should be deleted after main()"
        assert not complete_file.exists(), "handoff_complete should be deleted after main()"


# --- handoff_archive ---


class TestHandoffArchive:
    def test_pane_handoff_archives_old_file(self, tmp_path):
        """Verify pane_handoff archives old handoff to timestamped backup."""
        import re

        # Set up pane directory with handoff
        panes = tmp_path / "panes"
        role_dir = panes / "manager"
        role_dir.mkdir(parents=True)
        handoff_file = role_dir / "handoff.yaml"
        content = "---\ngoal: test\nnow: working\n"
        handoff_file.write_text(content)

        # Call pane_handoff to read the file
        result = pane_handoff("manager", panes_dir=str(panes))

        # Verify the result is readable
        assert "goal: test" in result
        assert "## Latest Handoff (Auto-Recovery)" in result

        # Verify original handoff.yaml still exists and is readable
        assert handoff_file.is_file()
        assert handoff_file.read_text() == content

        # Verify an archive was created with timestamp pattern
        # Pattern: handoff.YYYY-MM-DD_HH-MM-SS.yaml or similar
        archive_pattern = re.compile(r"handoff\.\d{4}-\d{2}-\d{2}_\d{2}-\d{2}-\d{2}\.yaml")
        archives = [f for f in role_dir.iterdir() if archive_pattern.match(f.name)]
        assert len(archives) >= 1, f"No archived handoff found in {role_dir}"

        # Verify archive contains the old content
        archive = archives[0]
        archive_content = archive.read_text()
        assert archive_content == content, f"Archive content differs: {archive_content}"

    def test_latest_handoff_archives_old_file(self, tmp_path):
        """Verify latest_handoff archives old handoff to timestamped backup."""
        import re

        # Set up handoffs directory
        handoffs_dir = tmp_path / "handoffs"
        handoffs_dir.mkdir()
        handoff_file = handoffs_dir / "2026-02-15.yaml"
        content = "---\ngoal: latest test\nnow: working on latest\n"
        handoff_file.write_text(content)

        # Call latest_handoff to read the file
        result = _mod.latest_handoff(str(handoffs_dir))

        # Verify the result is readable
        assert "goal: latest test" in result
        assert "## Latest Handoff" in result

        # Verify original handoff file still exists
        assert handoff_file.is_file()
        assert handoff_file.read_text() == content

        # Verify an archive was created
        archive_pattern = re.compile(r"2026-02-15\.\d{4}-\d{2}-\d{2}_\d{2}-\d{2}-\d{2}\.yaml")
        archives = [f for f in handoffs_dir.iterdir() if archive_pattern.match(f.name)]
        assert len(archives) >= 1, f"No archived handoff found in {handoffs_dir}"

        # Verify archive contains the old content
        archive = archives[0]
        archive_content = archive.read_text()
        assert archive_content == content, f"Archive content differs: {archive_content}"

    def test_multiple_reads_create_multiple_archives(self, tmp_path):
        """Verify multiple reads create multiple timestamped archives."""
        import re
        import time

        # Set up pane directory
        panes = tmp_path / "panes"
        role_dir = panes / "manager"
        role_dir.mkdir(parents=True)
        handoff_file = role_dir / "handoff.yaml"
        content = "---\ngoal: multi-read test\n"
        handoff_file.write_text(content)

        # First read
        pane_handoff("manager", panes_dir=str(panes))
        time.sleep(0.1)  # Small delay to ensure different timestamp

        # Modify the handoff
        new_content = "---\ngoal: multi-read test updated\n"
        handoff_file.write_text(new_content)

        # Second read
        pane_handoff("manager", panes_dir=str(panes))

        # Verify multiple archives exist (pattern includes optional counter suffix)
        # Matches: handoff.YYYY-MM-DD_HH-MM-SS.yaml or handoff.YYYY-MM-DD_HH-MM-SS.N.yaml
        archive_pattern = re.compile(r"handoff\.\d{4}-\d{2}-\d{2}_\d{2}-\d{2}-\d{2}(?:\.\d+)?\.yaml")
        archives = [f for f in role_dir.iterdir() if archive_pattern.match(f.name)]
        assert len(archives) >= 2, f"Should have at least 2 archives, got {len(archives)}"


# --- recent_memories_db ---


class TestRecentMemoriesDB:
    def test_parses_valid_json_from_oro(self, monkeypatch):
        """recent_memories_db returns parsed list from oro memories list --format=json."""
        import subprocess

        fake_json = json.dumps(
            [
                {"id": 1, "type": "lesson", "content": "use ruff", "confidence": 0.9, "created_at": "2026-01-01"},
                {"id": 2, "type": "gotcha", "content": "beware cd", "confidence": 0.8, "created_at": "2026-01-02"},
            ]
        )

        def fake_run(cmd, **kwargs):
            result = subprocess.CompletedProcess(cmd, 0, stdout=fake_json + "\n", stderr="")
            return result

        monkeypatch.setattr(subprocess, "run", fake_run)
        result = recent_memories_db(n=5)
        assert len(result) == 2
        assert result[0]["content"] == "use ruff"
        assert result[1]["type"] == "gotcha"

    def test_oro_not_on_path_returns_empty(self, monkeypatch):
        """When oro is not installed, returns empty list."""
        import subprocess

        def fake_run(cmd, **kwargs):
            raise OSError("No such file or directory: 'oro'")

        monkeypatch.setattr(subprocess, "run", fake_run)
        assert recent_memories_db(n=5) == []

    def test_oro_returns_nonzero_exit_returns_empty(self, monkeypatch):
        """When oro exits with error, returns empty list."""
        import subprocess

        def fake_run(cmd, **kwargs):
            return subprocess.CompletedProcess(cmd, 1, stdout="", stderr="error")

        monkeypatch.setattr(subprocess, "run", fake_run)
        assert recent_memories_db(n=5) == []

    def test_oro_returns_invalid_json_returns_empty(self, monkeypatch):
        """When oro outputs invalid JSON, returns empty list."""
        import subprocess

        def fake_run(cmd, **kwargs):
            return subprocess.CompletedProcess(cmd, 0, stdout="not json\n", stderr="")

        monkeypatch.setattr(subprocess, "run", fake_run)
        assert recent_memories_db(n=5) == []

    def test_passes_limit_to_oro(self, monkeypatch):
        """Limit parameter is forwarded to oro --limit flag."""
        import subprocess

        captured_cmd = []

        def fake_run(cmd, **kwargs):
            captured_cmd.extend(cmd)
            return subprocess.CompletedProcess(cmd, 0, stdout="[]\n", stderr="")

        monkeypatch.setattr(subprocess, "run", fake_run)
        recent_memories_db(n=3)
        assert "--limit=3" in captured_cmd

    def test_timeout_returns_empty(self, monkeypatch):
        """When oro times out, returns empty list."""
        import subprocess

        def fake_run(cmd, **kwargs):
            raise subprocess.TimeoutExpired(cmd, 5)

        monkeypatch.setattr(subprocess, "run", fake_run)
        assert recent_memories_db(n=5) == []


# --- TestWorkerShortCircuit ---


class TestWorkerShortCircuit:
    def test_worker_makes_no_subprocess_calls(self, tmp_path, monkeypatch):
        """When ORO_WORKER=1, main() makes zero subprocess.run calls and outputs superpowers + skills."""
        import io
        import subprocess
        import sys
        import time

        oro_home = tmp_path / ".oro"
        skills_dir = oro_home / ".claude" / "skills" / "using-skills"
        skills_dir.mkdir(parents=True)
        (skills_dir / "SKILL.md").write_text("# Using Skills\n\nAlways check for skills first.")

        monkeypatch.setenv("ORO_HOME", str(oro_home))
        monkeypatch.setenv("ORO_WORKER", "1")

        subprocess_calls: list = []

        def fake_run(cmd, **kwargs):
            subprocess_calls.append(cmd)
            return subprocess.CompletedProcess(cmd, 0, stdout="", stderr="")

        monkeypatch.setattr(subprocess, "run", fake_run)

        mock_stdin = io.StringIO("{}")
        monkeypatch.setattr(sys, "stdin", mock_stdin)
        mock_stdout = io.StringIO()
        monkeypatch.setattr(sys, "stdout", mock_stdout)

        start = time.monotonic()
        _mod.main()
        elapsed = time.monotonic() - start

        assert subprocess_calls == [], f"Expected zero subprocess.run calls, got: {subprocess_calls}"

        output = json.loads(mock_stdout.getvalue())
        additional_context = output["hookSpecificOutput"]["additionalContext"]
        assert "# Superpowers" in additional_context
        assert "# Auto-loaded Skill: using-skills" in additional_context

        assert elapsed < 2.0, f"Worker path took {elapsed:.2f}s (expected < 2s)"
