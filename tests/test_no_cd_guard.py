"""Tests for no_cd_guard hook."""

from unittest.mock import patch

from no_cd_guard import _git_repo_root, build_decision, find_cd_targets, is_outside_root


class TestFindCdTargets:
    def test_simple_cd(self):
        assert find_cd_targets("cd /tmp") == ["/tmp"]

    def test_chained_cd(self):
        assert find_cd_targets("git stash && cd /foo") == ["/foo"]

    def test_no_cd(self):
        assert find_cd_targets("git status") == []

    def test_empty(self):
        assert find_cd_targets("") == []


class TestIsOutsideRoot:
    def test_cd_to_root(self, tmp_path):
        assert is_outside_root(str(tmp_path), str(tmp_path)) is False

    def test_cd_to_subdir(self, tmp_path):
        sub = tmp_path / "src"
        sub.mkdir()
        assert is_outside_root(str(sub), str(tmp_path)) is False

    def test_cd_to_worktree(self, tmp_path):
        wt = tmp_path / ".worktrees" / "agent-123"
        wt.mkdir(parents=True)
        assert is_outside_root(str(wt), str(tmp_path)) is True

    def test_cd_outside(self, tmp_path):
        assert is_outside_root("/completely/elsewhere", str(tmp_path)) is True


class TestGitRepoRoot:
    def test_returns_real_repo_root(self):
        """_git_repo_root returns the actual repo root, not a worktree root."""
        root = _git_repo_root()
        # Must not contain .worktrees or .claude/worktrees
        assert ".worktrees" not in root
        assert root.endswith("/oro") or root.endswith("/oro/")


class TestBuildDecision:
    def _hook_input(self, command: str, cwd: str = "/project") -> dict:
        return {"tool_name": "Bash", "tool_input": {"command": command}, "cwd": cwd}

    @patch("no_cd_guard._PROJECT_ROOT", "/project")
    def test_allows_cd_to_project_root(self):
        result = build_decision(self._hook_input("cd /project"))
        assert result is None

    @patch("no_cd_guard._PROJECT_ROOT", "/project")
    def test_blocks_cd_to_worktree(self):
        result = build_decision(self._hook_input("cd /project/.worktrees/agent-123"))
        assert result is not None
        assert result["permissionDecision"] == "deny"

    @patch("no_cd_guard._PROJECT_ROOT", "/project")
    def test_blocks_cd_outside(self):
        result = build_decision(self._hook_input("cd /tmp"))
        assert result is not None
        assert result["permissionDecision"] == "deny"

    @patch("no_cd_guard._PROJECT_ROOT", "/project")
    def test_allows_cd_to_subdir(self):
        result = build_decision(self._hook_input("cd /project/src"))
        assert result is None

    def test_ignores_non_bash(self):
        result = build_decision({"tool_name": "Read", "tool_input": {"command": "cd /tmp"}})
        assert result is None
