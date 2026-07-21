"""Tests for rebase_worktree_guard hook."""

from unittest.mock import patch

from rebase_worktree_guard import build_decision, get_worktree_branches, parse_rebase_branch


class TestParseRebaseBranch:
    def test_simple_rebase(self):
        assert parse_rebase_branch("git rebase main bead/oro-by8") == "bead/oro-by8"

    def test_rebase_with_flags(self):
        assert parse_rebase_branch("git rebase --onto main bead/xyz") == "bead/xyz"

    def test_rebase_no_branch(self):
        """git rebase main (no target branch) should not match."""
        assert parse_rebase_branch("git rebase main") is None

    def test_not_rebase(self):
        assert parse_rebase_branch("git merge --ff-only bead/xyz") is None

    def test_empty(self):
        assert parse_rebase_branch("") is None

    def test_none(self):
        assert parse_rebase_branch(None) is None

    def test_chained_command(self):
        assert parse_rebase_branch("git stash && git rebase main bead/foo") == "bead/foo"

    def test_rebase_abort(self):
        """git rebase --abort should not match (no target branch)."""
        assert parse_rebase_branch("git rebase --abort") is None


class TestBuildDecision:
    def _hook_input(self, command: str) -> dict:
        return {"tool_name": "Bash", "tool_input": {"command": command}}

    @patch("rebase_worktree_guard.get_worktree_branches")
    def test_blocks_rebase_on_worktree_branch(self, mock_branches):
        mock_branches.return_value = {"bead/oro-by8"}
        result = build_decision(self._hook_input("git rebase main bead/oro-by8"))
        assert result is not None
        assert result["permissionDecision"] == "deny"
        assert "bead/oro-by8" in result["message"]
        assert "worktree remove" in result["message"]

    @patch("rebase_worktree_guard.get_worktree_branches")
    def test_allows_rebase_on_non_worktree_branch(self, mock_branches):
        mock_branches.return_value = {"bead/other"}
        result = build_decision(self._hook_input("git rebase main bead/oro-by8"))
        assert result is None

    @patch("rebase_worktree_guard.get_worktree_branches")
    def test_allows_rebase_no_worktrees(self, mock_branches):
        mock_branches.return_value = set()
        result = build_decision(self._hook_input("git rebase main bead/xyz"))
        assert result is None

    def test_ignores_non_bash(self):
        result = build_decision({"tool_name": "Read", "tool_input": {}})
        assert result is None

    def test_ignores_non_rebase(self):
        result = build_decision(self._hook_input("git status"))
        assert result is None

    def test_ignores_empty_command(self):
        result = build_decision(self._hook_input(""))
        assert result is None

    def test_ignores_missing_tool_input(self):
        result = build_decision({"tool_name": "Bash"})
        assert result is None


class TestGetWorktreeBranches:
    @patch("subprocess.run")
    def test_parses_porcelain_output(self, mock_run):
        mock_run.return_value = type(
            "Result",
            (),
            {
                "returncode": 0,
                "stdout": (
                    "worktree /Users/me/project\n"
                    "HEAD abc123\n"
                    "branch refs/heads/main\n"
                    "\n"
                    "worktree /Users/me/project/.worktrees/bead-xyz\n"
                    "HEAD def456\n"
                    "branch refs/heads/bead/xyz\n"
                    "\n"
                ),
            },
        )()
        branches = get_worktree_branches()
        assert branches == {"main", "bead/xyz"}

    @patch("subprocess.run")
    def test_handles_git_failure(self, mock_run):
        mock_run.return_value = type(
            "Result",
            (),
            {
                "returncode": 1,
                "stdout": "",
            },
        )()
        assert get_worktree_branches() == set()

    @patch("subprocess.run")
    def test_handles_no_worktrees(self, mock_run):
        mock_run.return_value = type(
            "Result",
            (),
            {
                "returncode": 0,
                "stdout": ("worktree /Users/me/project\nHEAD abc123\nbranch refs/heads/main\n\n"),
            },
        )()
        branches = get_worktree_branches()
        assert branches == {"main"}
