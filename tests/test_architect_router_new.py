#!/usr/bin/env python3
"""New tests for architect_router.py - markdown/yaml git workflow."""

import os
from unittest.mock import Mock, patch

# Import the module under test
import architect_router


class TestGitMarkdownWorkflow:
    """Test git commands for markdown/yaml workflow."""

    @patch.dict(os.environ, {"ORO_ROLE": "architect"})
    def test_git_status_allowed(self):
        """git status should be allowed (read-only)."""
        hook_input = {
            "tool_name": "Bash",
            "tool_input": {"command": "git status"},
        }
        result = architect_router.build_decision(hook_input)
        assert result is None  # Passthrough = allowed

    @patch.dict(os.environ, {"ORO_ROLE": "architect"})
    def test_git_log_allowed(self):
        """git log should be allowed (read-only)."""
        hook_input = {
            "tool_name": "Bash",
            "tool_input": {"command": "git log --oneline"},
        }
        result = architect_router.build_decision(hook_input)
        assert result is None

    @patch.dict(os.environ, {"ORO_ROLE": "architect"})
    def test_git_diff_allowed(self):
        """git diff should be allowed (read-only)."""
        hook_input = {
            "tool_name": "Bash",
            "tool_input": {"command": "git diff"},
        }
        result = architect_router.build_decision(hook_input)
        assert result is None

        hook_input = {
            "tool_name": "Bash",
            "tool_input": {"command": "git diff --cached --name-only"},
        }
        result = architect_router.build_decision(hook_input)
        assert result is None

    @patch.dict(os.environ, {"ORO_ROLE": "architect"})
    def test_git_add_markdown_allowed(self):
        """git add with .md files should be allowed."""
        hook_input = {
            "tool_name": "Bash",
            "tool_input": {"command": "git add docs/plans/spec.md"},
        }
        result = architect_router.build_decision(hook_input)
        assert result is None

    @patch.dict(os.environ, {"ORO_ROLE": "architect"})
    def test_git_add_yaml_allowed(self):
        """git add with .yaml/.yml files should be allowed."""
        hook_input = {
            "tool_name": "Bash",
            "tool_input": {"command": "git add handoff.yaml"},
        }
        result = architect_router.build_decision(hook_input)
        assert result is None

        hook_input = {
            "tool_name": "Bash",
            "tool_input": {"command": "git add config.yml"},
        }
        result = architect_router.build_decision(hook_input)
        assert result is None

    @patch.dict(os.environ, {"ORO_ROLE": "architect"})
    def test_git_add_code_blocked(self):
        """git add with code files (.go, .py, .sh, .js, .ts) should be blocked."""
        test_cases = [
            "git add cmd/oro/main.go",
            "git add script.py",
            "git add build.sh",
            "git add app.js",
            "git add component.ts",
        ]

        for command in test_cases:
            hook_input = {
                "tool_name": "Bash",
                "tool_input": {"command": command},
            }
            result = architect_router.build_decision(hook_input)
            assert result is not None, f"Expected {command} to be blocked"
            assert result["permissionDecision"] == "deny"
            assert "[BLOCKED]" in result["message"]

    @patch.dict(os.environ, {"ORO_ROLE": "architect"})
    @patch("architect_router.verify_staged_files", return_value=True)
    def test_git_commit_allowed_when_markdown_staged(self, mock_verify):
        """git commit should be allowed when only markdown/yaml is staged."""
        hook_input = {
            "tool_name": "Bash",
            "tool_input": {"command": "git commit -m 'docs: add spec'"},
        }
        result = architect_router.build_decision(hook_input)
        assert result is None
        mock_verify.assert_called_once()

    @patch.dict(os.environ, {"ORO_ROLE": "architect"})
    @patch("architect_router.verify_staged_files", return_value=False)
    def test_git_commit_blocked_when_code_staged(self, mock_verify):
        """git commit should be blocked when code files are staged."""
        hook_input = {
            "tool_name": "Bash",
            "tool_input": {"command": "git commit -m 'feat: add feature'"},
        }
        result = architect_router.build_decision(hook_input)
        assert result is not None
        assert result["permissionDecision"] == "deny"
        assert "[BLOCKED]" in result["message"]
        assert "code" in result["message"].lower()
        mock_verify.assert_called_once()

    @patch.dict(os.environ, {"ORO_ROLE": "architect"})
    def test_git_push_allowed(self):
        """git push should be allowed."""
        hook_input = {
            "tool_name": "Bash",
            "tool_input": {"command": "git push"},
        }
        result = architect_router.build_decision(hook_input)
        assert result is None

    @patch.dict(os.environ, {"ORO_ROLE": "architect"})
    def test_bd_sync_allowed(self):
        """bd sync should be allowed."""
        hook_input = {
            "tool_name": "Bash",
            "tool_input": {"command": "bd sync --from-main"},
        }
        result = architect_router.build_decision(hook_input)
        assert result is None


class TestVerifyStagedFiles:
    """Test verification of staged files before commit."""

    @patch("subprocess.run")
    def test_all_markdown_yaml_allowed(self, mock_run):
        """When all staged files are .md/.yaml/.yml, return True."""
        mock_run.return_value = Mock(
            returncode=0,
            stdout="docs/plans/spec.md\nhandoff.yaml\nconfig.yml\n",
            stderr="",
        )
        assert architect_router.verify_staged_files() is True

    @patch("subprocess.run")
    def test_code_files_blocked(self, mock_run):
        """When code files are staged, return False."""
        mock_run.return_value = Mock(
            returncode=0,
            stdout="cmd/oro/main.go\ndocs/spec.md\n",
            stderr="",
        )
        assert architect_router.verify_staged_files() is False

    @patch("subprocess.run")
    def test_python_files_blocked(self, mock_run):
        """Python files should be blocked."""
        mock_run.return_value = Mock(
            returncode=0,
            stdout="script.py\nREADME.md\n",
            stderr="",
        )
        assert architect_router.verify_staged_files() is False

    @patch("subprocess.run")
    def test_shell_files_blocked(self, mock_run):
        """Shell scripts should be blocked."""
        mock_run.return_value = Mock(
            returncode=0,
            stdout="build.sh\n",
            stderr="",
        )
        assert architect_router.verify_staged_files() is False

    @patch("subprocess.run")
    def test_no_staged_files_allowed(self, mock_run):
        """When no files are staged, return True (empty commit is allowed)."""
        mock_run.return_value = Mock(
            returncode=0,
            stdout="",
            stderr="",
        )
        assert architect_router.verify_staged_files() is True

    @patch("subprocess.run")
    def test_git_command_failure_blocks(self, mock_run):
        """If git diff --cached fails, return False (safe default)."""
        mock_run.return_value = Mock(
            returncode=1,
            stdout="",
            stderr="fatal: not a git repository",
        )
        assert architect_router.verify_staged_files() is False


class TestBuildCommandsBlocked:
    """Test that build/test commands are forwarded to manager."""

    @patch.dict(os.environ, {"ORO_ROLE": "architect"})
    @patch("architect_router.send_to_manager_pane", return_value=True)
    def test_make_commands_forwarded(self, mock_send):
        """make commands should be forwarded to manager."""
        hook_input = {
            "tool_name": "Bash",
            "tool_input": {"command": "make test"},
        }
        result = architect_router.build_decision(hook_input)
        assert result is not None
        assert result["permissionDecision"] == "deny"
        mock_send.assert_called_once()

    @patch.dict(os.environ, {"ORO_ROLE": "architect"})
    @patch("architect_router.send_to_manager_pane", return_value=True)
    def test_go_build_commands_forwarded(self, mock_send):
        """go build/test/install should be forwarded."""
        test_cases = ["go build", "go test ./...", "go install"]

        for command in test_cases:
            mock_send.reset_mock()
            hook_input = {
                "tool_name": "Bash",
                "tool_input": {"command": command},
            }
            result = architect_router.build_decision(hook_input)
            assert result is not None, f"Expected {command} to be forwarded"
            assert result["permissionDecision"] == "deny"
            mock_send.assert_called_once()
