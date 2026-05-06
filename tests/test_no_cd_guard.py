"""Tests for no_cd_guard hook."""

import subprocess
from unittest.mock import MagicMock, patch

from no_cd_guard import _git_repo_root, build_decision, check_git_command, find_cd_targets, is_outside_root


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

    def test_cd_to_subdir_is_blocked(self, tmp_path):
        sub = tmp_path / "src"
        sub.mkdir()
        assert is_outside_root(str(sub), str(tmp_path)) is True

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

    def test_git_repo_root_inside_worktree(self, tmp_path):
        """Returns real repo root when CWD is /repo/root/.worktrees/agent-xxx.

        Bug: git rev-parse --show-toplevel returns the worktree path when
        executed from inside a worktree, making the hook believe cd /repo/root
        is "outside the project" and blocking valid navigation.

        Fix: use --git-common-dir which always returns the shared .git dir,
        even from inside a worktree. Parent of .git dir is the real repo root.
        """
        repo_root = tmp_path / "repo"
        repo_root.mkdir()
        git_dir = repo_root / ".git"
        git_dir.mkdir()
        worktree = repo_root / ".worktrees" / "agent-xxx"
        worktree.mkdir(parents=True)

        def mock_run(cmd, **kwargs):
            result = MagicMock()
            if "--git-common-dir" in cmd:
                # Correct: --git-common-dir points to shared .git even from worktree
                result.stdout = str(git_dir) + "\n"
            elif "--show-toplevel" in cmd:
                # Bug: --show-toplevel returns worktree path from inside worktree
                result.stdout = str(worktree) + "\n"
            else:
                result.stdout = ""
            return result

        with patch("no_cd_guard.subprocess.run", side_effect=mock_run):
            result = _git_repo_root()

        assert result == str(repo_root.resolve())

    def test_git_repo_root_fallback_when_common_dir_not_dot_git(self, tmp_path):
        """Falls back to --show-toplevel when --git-common-dir dir is not named .git."""
        repo_root = tmp_path / "repo"
        repo_root.mkdir()
        # Unusual: common dir not named ".git" (e.g. bare repo or manual setup)
        common_dir = repo_root / ".git_common"
        common_dir.mkdir()

        def mock_run(cmd, **kwargs):
            result = MagicMock()
            if "--git-common-dir" in cmd:
                result.stdout = str(common_dir) + "\n"
            elif "--show-toplevel" in cmd:
                result.stdout = str(repo_root) + "\n"
            else:
                result.stdout = ""
            return result

        with patch("no_cd_guard.subprocess.run", side_effect=mock_run):
            result = _git_repo_root()

        assert result == str(repo_root)

    def test_git_repo_root_fallback_on_git_failure(self):
        """Falls back to cwd string when git commands fail."""
        with patch(
            "no_cd_guard.subprocess.run",
            side_effect=subprocess.CalledProcessError(128, "git"),
        ):
            result = _git_repo_root()

        assert isinstance(result, str)
        assert len(result) > 0


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
    def test_blocks_cd_to_subdir(self):
        result = build_decision(self._hook_input("cd /project/src"))
        assert result is not None
        assert result["permissionDecision"] == "deny"

    def test_ignores_non_bash(self):
        result = build_decision({"tool_name": "Read", "tool_input": {"command": "cd /tmp"}})
        assert result is None


class TestCheckGitCommand:
    def test_blocks_worktree_remove_when_role_set(self):
        with patch.dict("os.environ", {"ORO_ROLE": "worker"}):
            result = check_git_command("git worktree remove .worktrees/agent-123")
            assert result is not None
            assert result["permissionDecision"] == "deny"

    def test_blocks_worktree_add_when_role_set(self):
        with patch.dict("os.environ", {"ORO_ROLE": "worker"}):
            result = check_git_command("git worktree add .worktrees/new-branch new-branch")
            assert result is not None
            assert result["permissionDecision"] == "deny"

    def test_blocks_worktree_remove_when_manager(self):
        with patch.dict("os.environ", {"ORO_ROLE": "manager"}):
            result = check_git_command("git worktree remove .worktrees/agent-123")
            assert result is not None
            assert result["permissionDecision"] == "deny"

    def test_allows_worktree_remove_when_no_role(self):
        with patch.dict("os.environ", {}, clear=True):
            result = check_git_command("git worktree remove .worktrees/agent-123")
            assert result is None

    def test_allows_worktree_add_when_no_role(self):
        with patch.dict("os.environ", {}, clear=True):
            result = check_git_command("git worktree add .worktrees/new-branch new-branch")
            assert result is None

    def test_allows_git_status(self):
        with patch.dict("os.environ", {"ORO_ROLE": "worker"}):
            assert check_git_command("git status") is None

    def test_allows_git_log(self):
        with patch.dict("os.environ", {"ORO_ROLE": "worker"}):
            assert check_git_command("git log --oneline") is None

    def test_allows_bare_git_worktree(self):
        with patch.dict("os.environ", {"ORO_ROLE": "worker"}):
            assert check_git_command("git worktree") is None

    def test_allows_git_worktree_list(self):
        with patch.dict("os.environ", {"ORO_ROLE": "worker"}):
            assert check_git_command("git worktree list") is None

    def test_allows_commit_message_containing_worktree_text(self):
        # Commit messages that mention "git worktree remove" must not be blocked.
        cmd = 'git commit -m "fix: block git worktree remove in workers"'
        with patch.dict("os.environ", {"ORO_ROLE": "worker"}):
            assert check_git_command(cmd) is None

    def test_allows_heredoc_commit_with_worktree_text(self):
        # Multi-line heredoc commit messages must not be blocked.
        cmd = (
            "git commit -m \"$(cat <<'EOF'\n"
            "fix(no_cd_guard): block git worktree remove/add\n\n"
            "Workers could delete their own worktree via\n"
            "git worktree remove, causing quality gate failure.\n"
            'EOF\n)"'
        )
        with patch.dict("os.environ", {"ORO_ROLE": "worker"}):
            assert check_git_command(cmd) is None


class TestBlockWorktreeRemove:
    def _hook_input(self, command: str) -> dict:
        return {"tool_name": "Bash", "tool_input": {"command": command}}

    @patch("no_cd_guard._PROJECT_ROOT", "/project")
    def test_blocks_git_worktree_remove_when_role_set(self):
        with patch.dict("os.environ", {"ORO_ROLE": "worker"}):
            result = build_decision(self._hook_input("git worktree remove .worktrees/agent-123"))
            assert result is not None
            assert result["permissionDecision"] == "deny"

    @patch("no_cd_guard._PROJECT_ROOT", "/project")
    def test_blocks_git_worktree_add_when_role_set(self):
        with patch.dict("os.environ", {"ORO_ROLE": "manager"}):
            result = build_decision(self._hook_input("git worktree add .worktrees/new-branch new-branch"))
            assert result is not None
            assert result["permissionDecision"] == "deny"

    @patch("no_cd_guard._PROJECT_ROOT", "/project")
    def test_allows_worktree_remove_when_no_role(self):
        with patch.dict("os.environ", {}, clear=True):
            result = build_decision(self._hook_input("git worktree remove .worktrees/agent-123"))
            assert result is None

    @patch("no_cd_guard._PROJECT_ROOT", "/project")
    def test_allows_worktree_add_when_no_role(self):
        with patch.dict("os.environ", {}, clear=True):
            result = build_decision(self._hook_input("git worktree add .worktrees/new-branch new-branch"))
            assert result is None

    @patch("no_cd_guard._PROJECT_ROOT", "/project")
    def test_allows_git_status(self):
        with patch.dict("os.environ", {"ORO_ROLE": "worker"}):
            result = build_decision(self._hook_input("git status"))
            assert result is None

    @patch("no_cd_guard._PROJECT_ROOT", "/project")
    def test_allows_git_log(self):
        with patch.dict("os.environ", {"ORO_ROLE": "worker"}):
            result = build_decision(self._hook_input("git log --oneline"))
            assert result is None

    @patch("no_cd_guard._PROJECT_ROOT", "/project")
    def test_allows_bare_git_worktree(self):
        with patch.dict("os.environ", {"ORO_ROLE": "worker"}):
            result = build_decision(self._hook_input("git worktree"))
            assert result is None

    @patch("no_cd_guard._PROJECT_ROOT", "/project")
    def test_allows_git_worktree_list(self):
        with patch.dict("os.environ", {"ORO_ROLE": "worker"}):
            result = build_decision(self._hook_input("git worktree list"))
            assert result is None

    @patch("no_cd_guard._PROJECT_ROOT", "/project")
    def test_allows_commit_message_with_worktree_text(self):
        # Commit message body containing "git worktree remove" must not be blocked.
        cmd = (
            "git commit -m \"$(cat <<'EOF'\n"
            "fix(no_cd_guard): block git worktree remove/add\n\n"
            "git worktree remove, causing quality gate failure.\n"
            'EOF\n)"'
        )
        with patch.dict("os.environ", {"ORO_ROLE": "worker"}):
            result = build_decision(self._hook_input(cmd))
            assert result is None
