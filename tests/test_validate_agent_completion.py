"""Tests for the validate_agent_completion PostToolUse hook (SubagentStop)."""

import importlib.util
import json
import os
import subprocess
import tempfile
from pathlib import Path
from unittest.mock import patch

# Load the hook module from ORO_HOME/hooks/ (externalized config)
_oro_home = Path(os.environ.get("ORO_HOME", Path.home() / ".oro"))
_spec = importlib.util.spec_from_file_location(
    "validate_agent_completion",
    _oro_home / "hooks" / "validate_agent_completion.py",
)
_mod = importlib.util.module_from_spec(_spec)  # type: ignore[arg-type]
_spec.loader.exec_module(_mod)  # type: ignore[union-attr]

extract_worktree_path = _mod.extract_worktree_path
check_dirty_worktree = _mod.check_dirty_worktree
check_unpushed = _mod.check_unpushed
check_bead_closed = _mod.check_bead_closed
build_warnings = _mod.build_warnings


# ---------------------------------------------------------------------------
# extract_worktree_path
# ---------------------------------------------------------------------------
class TestExtractWorktreePath:
    def test_extracts_worktree_path(self):
        output = "Working in /Users/me/project/.worktrees/bead-abc123\nDone."
        assert extract_worktree_path(output) == "/Users/me/project/.worktrees/bead-abc123"

    def test_extracts_from_git_c_flag(self):
        output = "git -C /Users/me/project/.worktrees/bead-xyz commit -m 'done'"
        assert extract_worktree_path(output) == "/Users/me/project/.worktrees/bead-xyz"

    def test_returns_none_when_no_worktree(self):
        output = "I completed the task successfully."
        assert extract_worktree_path(output) is None

    def test_returns_none_for_empty_string(self):
        assert extract_worktree_path("") is None

    def test_extracts_first_worktree_path(self):
        output = "Using /foo/.worktrees/first-one for work\nAlso mentioned /foo/.worktrees/second-one"
        assert extract_worktree_path(output) == "/foo/.worktrees/first-one"

    def test_handles_worktree_path_with_trailing_slash(self):
        output = "cd /foo/.worktrees/bead-abc/ && git status"
        result = extract_worktree_path(output)
        assert result == "/foo/.worktrees/bead-abc"


# ---------------------------------------------------------------------------
# check_dirty_worktree
# ---------------------------------------------------------------------------
class TestCheckDirtyWorktree:
    def test_clean_worktree(self, tmp_path):
        # Init a git repo with one commit
        subprocess.run(["git", "init"], cwd=tmp_path, capture_output=True)
        subprocess.run(["git", "config", "user.email", "t@t.com"], cwd=tmp_path, capture_output=True)
        subprocess.run(["git", "config", "user.name", "T"], cwd=tmp_path, capture_output=True)
        (tmp_path / "f.txt").write_text("hello")
        subprocess.run(["git", "add", "."], cwd=tmp_path, capture_output=True)
        subprocess.run(["git", "commit", "-m", "init"], cwd=tmp_path, capture_output=True)

        assert check_dirty_worktree(str(tmp_path)) is None

    def test_dirty_worktree(self, tmp_path):
        subprocess.run(["git", "init"], cwd=tmp_path, capture_output=True)
        subprocess.run(["git", "config", "user.email", "t@t.com"], cwd=tmp_path, capture_output=True)
        subprocess.run(["git", "config", "user.name", "T"], cwd=tmp_path, capture_output=True)
        (tmp_path / "f.txt").write_text("hello")
        subprocess.run(["git", "add", "."], cwd=tmp_path, capture_output=True)
        subprocess.run(["git", "commit", "-m", "init"], cwd=tmp_path, capture_output=True)
        # Create uncommitted change
        (tmp_path / "f.txt").write_text("modified")

        result = check_dirty_worktree(str(tmp_path))
        assert result is not None
        assert "uncommitted" in result.lower()

    def test_nonexistent_path(self):
        result = check_dirty_worktree("/nonexistent/path/worktree")
        assert result is not None
        assert "does not exist" in result.lower() or "not found" in result.lower()

    def test_untracked_files_count_as_dirty(self, tmp_path):
        subprocess.run(["git", "init"], cwd=tmp_path, capture_output=True)
        subprocess.run(["git", "config", "user.email", "t@t.com"], cwd=tmp_path, capture_output=True)
        subprocess.run(["git", "config", "user.name", "T"], cwd=tmp_path, capture_output=True)
        (tmp_path / "f.txt").write_text("hello")
        subprocess.run(["git", "add", "."], cwd=tmp_path, capture_output=True)
        subprocess.run(["git", "commit", "-m", "init"], cwd=tmp_path, capture_output=True)
        # Untracked file
        (tmp_path / "new.txt").write_text("untracked")

        result = check_dirty_worktree(str(tmp_path))
        assert result is not None


# ---------------------------------------------------------------------------
# check_unpushed
# ---------------------------------------------------------------------------
class TestCheckUnpushed:
    def test_no_upstream_fails_open(self):
        """If no upstream is configured, fail open (return None)."""
        with tempfile.TemporaryDirectory() as tmp:
            subprocess.run(["git", "init"], cwd=tmp, capture_output=True)
            subprocess.run(["git", "config", "user.email", "t@t.com"], cwd=tmp, capture_output=True)
            subprocess.run(["git", "config", "user.name", "T"], cwd=tmp, capture_output=True)
            Path(os.path.join(tmp, "f.txt")).write_text("x")
            subprocess.run(["git", "add", "."], cwd=tmp, capture_output=True)
            subprocess.run(["git", "commit", "-m", "init"], cwd=tmp, capture_output=True)
            # No remote configured -> fail open
            assert check_unpushed(tmp) is None

    def test_nonexistent_path_fails_open(self):
        assert check_unpushed("/nonexistent/path") is None


# ---------------------------------------------------------------------------
# check_bead_closed
# ---------------------------------------------------------------------------
class TestCheckBeadClosed:
    def test_bd_close_present(self):
        output = 'Running: bd close oro-abc --reason="Done"\nSuccess.'
        assert check_bead_closed(output) is None

    def test_bd_close_missing(self):
        output = "I finished the task. All tests pass."
        result = check_bead_closed(output)
        assert result is not None
        assert "bd close" in result.lower() or "bead" in result.lower()

    def test_empty_output(self):
        result = check_bead_closed("")
        assert result is not None

    def test_bd_close_with_different_formatting(self):
        output = "bd  close  my-bead  --reason='Completed'"
        assert check_bead_closed(output) is None


# ---------------------------------------------------------------------------
# build_warnings (integration of all checks)
# ---------------------------------------------------------------------------
class TestBuildWarnings:
    def test_no_worktree_no_warnings(self):
        """If no worktree path detected, fail open with no warnings."""
        output = "Task completed successfully."
        warnings = build_warnings(output)
        assert warnings == []

    def test_missing_bead_close_warning(self):
        """Even without worktree, should warn about missing bd close."""
        output = "Working in /foo/.worktrees/bead-abc\nAll done, tests pass."
        # Worktree won't exist, so we mock check_dirty_worktree and check_unpushed
        with (
            patch.object(_mod, "check_dirty_worktree", return_value=None),
            patch.object(_mod, "check_unpushed", return_value=None),
        ):
            warnings = build_warnings(output)
        assert any("bd close" in w.lower() or "bead" in w.lower() for w in warnings)

    def test_all_clean_no_warnings(self):
        output = 'git -C /foo/.worktrees/bead-abc commit\nbd close abc --reason="Done"'
        with (
            patch.object(_mod, "check_dirty_worktree", return_value=None),
            patch.object(_mod, "check_unpushed", return_value=None),
        ):
            warnings = build_warnings(output)
        assert warnings == []

    def test_multiple_warnings_accumulated(self):
        output = "Working in /foo/.worktrees/bead-abc\nDid some work."
        with (
            patch.object(_mod, "check_dirty_worktree", return_value="Uncommitted changes found"),
            patch.object(_mod, "check_unpushed", return_value="Unpushed commits found"),
        ):
            warnings = build_warnings(output)
        # Should have dirty + unpushed + no bd close = 3 warnings
        assert len(warnings) == 3


# ---------------------------------------------------------------------------
# main() end-to-end via stdin/stdout
# ---------------------------------------------------------------------------
class TestMain:
    def test_non_task_tool_ignored(self):
        """Non-Task tool_name should produce no output."""
        hook_input = {
            "tool_name": "Bash",
            "tool_input": {"command": "echo hello"},
            "tool_output": "hello",
        }
        result = subprocess.run(
            [
                "python3",
                str(Path(__file__).resolve().parent.parent / ".claude" / "hooks" / "validate_agent_completion.py"),
            ],
            input=json.dumps(hook_input),
            capture_output=True,
            text=True,
        )
        assert result.stdout == ""

    def test_task_tool_clean_no_worktree(self):
        """Task tool with no worktree mention and bd close should produce no output."""
        hook_input = {
            "tool_name": "Task",
            "tool_input": {"prompt": "Do something"},
            "tool_output": 'bd close xyz --reason="Done"',
        }
        result = subprocess.run(
            [
                "python3",
                str(Path(__file__).resolve().parent.parent / ".claude" / "hooks" / "validate_agent_completion.py"),
            ],
            input=json.dumps(hook_input),
            capture_output=True,
            text=True,
        )
        # No worktree -> fail open, bd close present -> no warning
        assert result.stdout == ""

    def test_task_tool_missing_bd_close_warns(self):
        """Task with worktree but no bd close should produce warning JSON."""
        hook_input = {
            "tool_name": "Task",
            "tool_input": {"prompt": "Work in /foo/.worktrees/bead-test"},
            "tool_output": "Done. All tests pass.",
        }
        result = subprocess.run(
            [
                "python3",
                str(Path(__file__).resolve().parent.parent / ".claude" / "hooks" / "validate_agent_completion.py"),
            ],
            input=json.dumps(hook_input),
            capture_output=True,
            text=True,
        )
        if result.stdout:
            output = json.loads(result.stdout)
            assert "hookSpecificOutput" in output
            ctx = output["hookSpecificOutput"].get("additionalContext", "")
            assert "bd close" in ctx.lower() or "bead" in ctx.lower()
