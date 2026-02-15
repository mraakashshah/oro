"""Tests for the worktree removal guard PreToolUse hook."""

import importlib.util
import os
from pathlib import Path
from unittest.mock import patch

# Load the hook module from ORO_HOME/hooks/ (externalized config)
_oro_home = Path(os.environ.get("ORO_HOME", Path.home() / ".oro"))
_spec = importlib.util.spec_from_file_location(
    "worktree_guard",
    _oro_home / "hooks" / "worktree_guard.py",
)
_mod = importlib.util.module_from_spec(_spec)  # type: ignore[arg-type]
_spec.loader.exec_module(_mod)  # type: ignore[union-attr]

parse_worktree_remove = _mod.parse_worktree_remove
is_cwd_inside = _mod.is_cwd_inside
build_decision = _mod.build_decision


class TestParseWorktreeRemove:
    """Extract the worktree path from git worktree remove commands."""

    def test_basic_remove(self):
        assert parse_worktree_remove("git worktree remove .worktrees/feat-x") == ".worktrees/feat-x"

    def test_remove_with_force(self):
        assert parse_worktree_remove("git worktree remove --force .worktrees/feat-x") == ".worktrees/feat-x"

    def test_remove_force_short(self):
        assert parse_worktree_remove("git worktree remove -f .worktrees/my-branch") == ".worktrees/my-branch"

    def test_absolute_path(self):
        assert parse_worktree_remove("git worktree remove /tmp/wt/branch") == "/tmp/wt/branch"

    def test_remove_with_force_after_path(self):
        # --force before path is common; after is not valid git syntax but test parsing
        assert parse_worktree_remove("git worktree remove --force /tmp/wt") == "/tmp/wt"

    def test_not_worktree_command(self):
        assert parse_worktree_remove("git status") is None

    def test_worktree_add(self):
        assert parse_worktree_remove("git worktree add .worktrees/new -b new") is None

    def test_worktree_list(self):
        assert parse_worktree_remove("git worktree list") is None

    def test_empty_command(self):
        assert parse_worktree_remove("") is None

    def test_chained_command_with_remove(self):
        assert parse_worktree_remove("cd /tmp && git worktree remove .worktrees/x") == ".worktrees/x"

    def test_semicolon_chained(self):
        assert parse_worktree_remove("echo hi; git worktree remove --force .worktrees/y") == ".worktrees/y"

    def test_quoted_path(self):
        assert parse_worktree_remove('git worktree remove ".worktrees/my branch"') == ".worktrees/my branch"

    def test_single_quoted_path(self):
        assert parse_worktree_remove("git worktree remove '.worktrees/my branch'") == ".worktrees/my branch"


class TestIsCwdInside:
    """Check if cwd is inside a worktree path."""

    def test_cwd_is_worktree(self):
        assert is_cwd_inside("/repo/.worktrees/feat", "/repo/.worktrees/feat") is True

    def test_cwd_is_subdirectory(self):
        assert is_cwd_inside("/repo/.worktrees/feat/src", "/repo/.worktrees/feat") is True

    def test_cwd_is_outside(self):
        assert is_cwd_inside("/repo", "/repo/.worktrees/feat") is False

    def test_cwd_is_sibling_worktree(self):
        assert is_cwd_inside("/repo/.worktrees/other", "/repo/.worktrees/feat") is False

    def test_relative_paths_resolved(self, tmp_path):
        wt = tmp_path / ".worktrees" / "feat"
        wt.mkdir(parents=True)
        sub = wt / "src"
        sub.mkdir()
        assert is_cwd_inside(str(sub), str(wt)) is True

    def test_prefix_not_parent(self):
        # /repo/.worktrees/feat-extra is NOT inside /repo/.worktrees/feat
        assert is_cwd_inside("/repo/.worktrees/feat-extra", "/repo/.worktrees/feat") is False


class TestBuildDecision:
    """Integration: build_decision returns block or passthrough."""

    def test_non_bash_passthrough(self):
        hook_input = {"tool_name": "Read", "tool_input": {"file_path": "/foo"}}
        assert build_decision(hook_input) is None

    def test_non_worktree_bash_passthrough(self):
        hook_input = {"tool_name": "Bash", "tool_input": {"command": "git status"}}
        assert build_decision(hook_input) is None

    def test_safe_removal_passthrough(self):
        """CWD is the project root, not inside the worktree being removed."""
        hook_input = {
            "tool_name": "Bash",
            "tool_input": {"command": "git worktree remove .worktrees/feat-x"},
        }
        with (
            patch.object(_mod, "_get_cwd", return_value="/repo"),
            patch.object(_mod, "_resolve_worktree_path", return_value="/repo/.worktrees/feat-x"),
        ):
            assert build_decision(hook_input) is None

    def test_dangerous_removal_blocks(self):
        """CWD is inside the worktree being removed — must block."""
        hook_input = {
            "tool_name": "Bash",
            "tool_input": {"command": "git worktree remove --force .worktrees/feat-x"},
        }
        with (
            patch.object(_mod, "_get_cwd", return_value="/repo/.worktrees/feat-x"),
            patch.object(_mod, "_resolve_worktree_path", return_value="/repo/.worktrees/feat-x"),
        ):
            result = build_decision(hook_input)
            assert result is not None
            assert result["permissionDecision"] == "deny"
            assert "cd" in result["message"].lower() or "cwd" in result["message"].lower()

    def test_dangerous_removal_from_subdirectory_blocks(self):
        """CWD is in a subdirectory of the worktree — must block."""
        hook_input = {
            "tool_name": "Bash",
            "tool_input": {"command": "git worktree remove .worktrees/feat-x"},
        }
        with (
            patch.object(_mod, "_get_cwd", return_value="/repo/.worktrees/feat-x/src/pkg"),
            patch.object(_mod, "_resolve_worktree_path", return_value="/repo/.worktrees/feat-x"),
        ):
            result = build_decision(hook_input)
            assert result is not None
            assert result["permissionDecision"] == "deny"

    def test_block_message_includes_cd_instruction(self):
        """Block message must tell the agent HOW to fix it."""
        hook_input = {
            "tool_name": "Bash",
            "tool_input": {"command": "git worktree remove .worktrees/feat-x"},
        }
        with (
            patch.object(_mod, "_get_cwd", return_value="/repo/.worktrees/feat-x"),
            patch.object(_mod, "_resolve_worktree_path", return_value="/repo/.worktrees/feat-x"),
        ):
            result = build_decision(hook_input)
            assert "cd" in result["message"]

    def test_malformed_json_passthrough(self):
        """Bad input should not crash — fail open."""
        assert build_decision({}) is None
        assert build_decision({"tool_name": "Bash"}) is None
        assert build_decision({"tool_name": "Bash", "tool_input": {}}) is None
