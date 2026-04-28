#!/usr/bin/env python3
"""Tests for architect_router.py hook."""

import os
import subprocess
from pathlib import Path
from unittest.mock import Mock, patch

# Import the module under test
import architect_router


class TestRouteCommand:
    """Test the routing decision logic."""

    def test_bd_commands_stay_local(self):
        assert architect_router.route_command("bd stats") == "architect"
        assert architect_router.route_command("bd ready") == "architect"
        assert architect_router.route_command("bd create --title='test'") == "architect"
        assert architect_router.route_command("  bd list") == "architect"
        assert architect_router.route_command("bd sync --from-main") == "architect"

    def test_oro_commands_stay_local(self):
        """route_command always returns architect — no manager forwarding."""
        assert architect_router.route_command("oro start") == "architect"
        assert architect_router.route_command("oro stop") == "architect"
        assert architect_router.route_command("oro directive status") == "architect"
        assert architect_router.route_command("oro directive scale 3") == "architect"
        assert architect_router.route_command("  oro directive pause") == "architect"

    def test_git_readonly_commands_stay_local(self):
        """git status and other read-only commands now stay with architect."""
        assert architect_router.route_command("git status") == "architect"
        assert architect_router.route_command("git log") == "architect"
        assert architect_router.route_command("git diff") == "architect"

    def test_build_commands_stay_local(self):
        """route_command always returns architect — build commands stay local (blocked by build_decision)."""
        assert architect_router.route_command("make test") == "architect"
        assert architect_router.route_command("go build") == "architect"
        assert architect_router.route_command("go test ./...") == "architect"

    def test_empty_commands_stay_local(self):
        """Empty commands now stay with architect (safe default)."""
        assert architect_router.route_command("") == "architect"
        assert architect_router.route_command("   ") == "architect"

    def test_unknown_commands_stay_local(self):
        """Unknown commands now stay with architect (safe default)."""
        assert architect_router.route_command("echo bd stats") == "architect"
        assert architect_router.route_command("ls -la") == "architect"
        assert architect_router.route_command("some-random-command") == "architect"


class TestFormatForwardMessage:
    """Test the feedback message formatting."""

    def test_oro_commands_get_specific_message(self):
        assert (
            architect_router.format_forward_message("oro directive scale 3")
            == "[forwarded to manager] oro directive scale 3"
        )
        assert architect_router.format_forward_message("oro status") == "[forwarded to manager] oro status"

    def test_other_commands_get_generic_message(self):
        assert architect_router.format_forward_message("make test") == "[forwarded] make test"

    def test_blocked_message_format(self):
        """Test blocked message format."""
        result = architect_router.format_forward_message(
            "git add main.go", blocked_reason="Cannot add code files from architect pane"
        )
        assert "[BLOCKED]" in result
        assert "Cannot add code files" in result


class TestBuildDecision:
    """Test the full hook decision logic."""

    @patch.dict(os.environ, {"ORO_ROLE": "manager"})
    def test_passthrough_when_not_architect(self):
        hook_input = {
            "tool_name": "Bash",
            "tool_input": {"command": "oro status"},
        }
        assert architect_router.build_decision(hook_input) is None

    @patch.dict(os.environ, {"ORO_ROLE": "architect"})
    def test_passthrough_when_not_bash_tool(self):
        hook_input = {
            "tool_name": "Read",
            "tool_input": {"file_path": "test.txt"},
        }
        assert architect_router.build_decision(hook_input) is None

    @patch.dict(os.environ, {"ORO_ROLE": "architect"})
    def test_passthrough_for_bd_commands(self):
        hook_input = {
            "tool_name": "Bash",
            "tool_input": {"command": "bd stats"},
        }
        assert architect_router.build_decision(hook_input) is None

    @patch.dict(os.environ, {"ORO_ROLE": "architect"})
    def test_passthrough_for_git_readonly(self):
        """git status and other read-only commands now pass through."""
        hook_input = {
            "tool_name": "Bash",
            "tool_input": {"command": "git status"},
        }
        assert architect_router.build_decision(hook_input) is None

    @patch.dict(os.environ, {"ORO_ROLE": "architect"})
    def test_blocks_oro_commands(self):
        """oro commands are blocked without forwarding."""
        hook_input = {
            "tool_name": "Bash",
            "tool_input": {"command": "oro directive scale 3"},
        }
        result = architect_router.build_decision(hook_input)

        assert result is not None
        assert result["permissionDecision"] == "deny"
        assert "[BLOCKED]" in result["message"]

    @patch.dict(os.environ, {"ORO_ROLE": "architect"})
    def test_blocks_build_commands(self):
        """Build commands are blocked without forwarding."""
        hook_input = {
            "tool_name": "Bash",
            "tool_input": {"command": "make test"},
        }
        result = architect_router.build_decision(hook_input)

        assert result is not None
        assert result["permissionDecision"] == "deny"
        assert "[BLOCKED]" in result["message"]

    @patch.dict(os.environ, {"ORO_ROLE": "architect"})
    @patch("architect_router.send_to_manager_pane")
    def test_oro_blocked_regardless_of_tmux(self, mock_send):
        """oro commands are blocked even when tmux is unavailable."""
        hook_input = {
            "tool_name": "Bash",
            "tool_input": {"command": "oro status"},
        }
        result = architect_router.build_decision(hook_input)

        assert result is not None
        assert result["permissionDecision"] == "deny"
        mock_send.assert_not_called()


class TestSendToManagerPane:
    """Test the tmux send-keys integration."""

    @patch("subprocess.run")
    def test_sends_command_via_tmux(self, mock_run):
        mock_run.return_value = Mock(returncode=0)

        result = architect_router.send_to_manager_pane("oro status")

        assert result is True
        assert mock_run.call_count == 2

        # First call: send-keys with literal text
        first_call = mock_run.call_args_list[0]
        assert first_call[0][0] == ["tmux", "send-keys", "-t", "oro:manager", "-l", "oro status"]

        # Second call: send Enter
        second_call = mock_run.call_args_list[1]
        assert second_call[0][0] == ["tmux", "send-keys", "-t", "oro:manager", "Enter"]

    @patch("subprocess.run", side_effect=subprocess.CalledProcessError(1, "tmux"))
    def test_returns_false_on_tmux_error(self, _mock_run):
        result = architect_router.send_to_manager_pane("oro status")
        assert result is False

    @patch("subprocess.run", side_effect=FileNotFoundError)
    def test_returns_false_when_tmux_not_found(self, _mock_run):
        result = architect_router.send_to_manager_pane("oro status")
        assert result is False


class TestArchitectRouterPolicy:
    """Policy tests: block mutating commands, allow read-only, never forward."""

    @patch.dict(os.environ, {"ORO_ROLE": "architect"})
    @patch("architect_router.send_to_manager_pane")
    def test_oro_commands_blocked_not_forwarded(self, mock_send):
        """oro start/work/status are blocked but NOT forwarded to manager."""
        for cmd in ["oro start", "oro work", "oro status", "oro directive scale 3"]:
            hook_input = {
                "tool_name": "Bash",
                "tool_input": {"command": cmd},
            }
            result = architect_router.build_decision(hook_input)
            assert result is not None, f"Expected block for: {cmd}"
            assert result["permissionDecision"] == "deny", f"Expected deny for: {cmd}"
            mock_send.assert_not_called()

    @patch.dict(os.environ, {"ORO_ROLE": "architect"})
    @patch("architect_router.send_to_manager_pane")
    def test_go_commands_blocked(self, mock_send):
        """go test/build/install are blocked."""
        for cmd in ["go test ./...", "go build ./...", "go install ./..."]:
            hook_input = {
                "tool_name": "Bash",
                "tool_input": {"command": cmd},
            }
            result = architect_router.build_decision(hook_input)
            assert result is not None, f"Expected block for: {cmd}"
            assert result["permissionDecision"] == "deny", f"Expected deny for: {cmd}"
            mock_send.assert_not_called()

    @patch.dict(os.environ, {"ORO_ROLE": "architect"})
    @patch("architect_router.send_to_manager_pane")
    def test_make_commands_blocked(self, mock_send):
        """make <anything> is blocked."""
        for cmd in ["make test", "make build", "make install", "make"]:
            hook_input = {
                "tool_name": "Bash",
                "tool_input": {"command": cmd},
            }
            result = architect_router.build_decision(hook_input)
            assert result is not None, f"Expected block for: {cmd}"
            assert result["permissionDecision"] == "deny", f"Expected deny for: {cmd}"
            mock_send.assert_not_called()

    @patch.dict(os.environ, {"ORO_ROLE": "architect"})
    @patch("architect_router.send_to_manager_pane")
    def test_git_mutating_commands_blocked(self, mock_send):
        """git add/commit/push are all blocked regardless of file types."""
        for cmd in ["git add .", "git add main.go", "git add README.md", "git commit -m 'test'", "git push"]:
            hook_input = {
                "tool_name": "Bash",
                "tool_input": {"command": cmd},
            }
            result = architect_router.build_decision(hook_input)
            assert result is not None, f"Expected block for: {cmd}"
            assert result["permissionDecision"] == "deny", f"Expected deny for: {cmd}"
            mock_send.assert_not_called()

    @patch.dict(os.environ, {"ORO_ROLE": "architect"})
    def test_git_readonly_commands_allowed(self):
        """git status/log/diff/branch/show pass through."""
        for cmd in ["git status", "git log", "git log --oneline", "git diff", "git branch", "git show HEAD"]:
            hook_input = {
                "tool_name": "Bash",
                "tool_input": {"command": cmd},
            }
            result = architect_router.build_decision(hook_input)
            assert result is None, f"Expected passthrough for: {cmd}"

    @patch.dict(os.environ, {"ORO_ROLE": "architect"})
    def test_bd_commands_allowed(self):
        """bd create/update/show pass through."""
        for cmd in ["bd create --title='test'", "bd update bd-123", "bd show bd-456", "bd ready", "bd stats"]:
            hook_input = {
                "tool_name": "Bash",
                "tool_input": {"command": cmd},
            }
            result = architect_router.build_decision(hook_input)
            assert result is None, f"Expected passthrough for: {cmd}"

    @patch.dict(os.environ, {"ORO_ROLE": "architect"})
    @patch("architect_router.send_to_manager_pane")
    def test_build_decision_never_calls_send_to_manager(self, mock_send):
        """build_decision() never calls send_to_manager_pane for any command."""
        commands = [
            "oro start",
            "oro work",
            "oro status",
            "go test ./...",
            "go build ./...",
            "go install",
            "make test",
            "make build",
            "git add main.go",
            "git commit -m 'test'",
            "git push",
            "bd create --title='test'",
            "bd update bd-123",
            "git status",
            "git log",
            "ls -la",
            "echo hello",
        ]
        for cmd in commands:
            hook_input = {
                "tool_name": "Bash",
                "tool_input": {"command": cmd},
            }
            architect_router.build_decision(hook_input)
        mock_send.assert_not_called()


class TestNotifyOnBeadCreate:
    """Test PostToolUse notification when architect creates beads."""

    @patch.dict(os.environ, {"ORO_ROLE": "architect"})
    @patch("architect_router.send_to_manager_pane", return_value=True)
    def test_notifies_manager_on_bd_create(self, mock_send):
        """When architect runs bd create, manager pane gets notification."""
        hook_input = {
            "tool_name": "Bash",
            "tool_input": {"command": "bd create --title='test task' --type=task"},
            "tool_output": "Created issue: oro-xyz123",
        }
        result = architect_router.notify_on_bead_create(hook_input)

        assert result is not None
        assert "additionalContext" in result
        mock_send.assert_called_once()

        # Verify the notification message content
        call_args = mock_send.call_args[0]
        assert "[NEW WORK]" in call_args[0]
        assert "Check oro bead ready" in call_args[0]

    @patch.dict(os.environ, {"ORO_ROLE": "architect"})
    @patch("architect_router.send_to_manager_pane", return_value=True)
    def test_no_notification_for_non_bd_create_commands(self, mock_send):
        """Only bd create triggers notification, not other bd commands."""
        hook_input = {
            "tool_name": "Bash",
            "tool_input": {"command": "bd ready"},
            "tool_output": "No beads ready",
        }
        result = architect_router.notify_on_bead_create(hook_input)

        assert result is None
        mock_send.assert_not_called()

    @patch.dict(os.environ, {"ORO_ROLE": "manager"})
    @patch("architect_router.send_to_manager_pane", return_value=True)
    def test_no_notification_when_not_architect(self, mock_send):
        """Manager doesn't notify itself."""
        hook_input = {
            "tool_name": "Bash",
            "tool_input": {"command": "bd create --title='test'"},
            "tool_output": "Created issue: oro-xyz",
        }
        result = architect_router.notify_on_bead_create(hook_input)

        assert result is None
        mock_send.assert_not_called()

    @patch.dict(os.environ, {"ORO_ROLE": "architect"})
    @patch("architect_router.send_to_manager_pane", return_value=False)
    def test_notification_fails_gracefully_on_tmux_error(self, mock_send):
        """If tmux send-keys fails, don't block or error."""
        hook_input = {
            "tool_name": "Bash",
            "tool_input": {"command": "bd create --title='test'"},
            "tool_output": "Created issue: oro-xyz",
        }
        result = architect_router.notify_on_bead_create(hook_input)

        # Should return None (fail open) when tmux fails
        assert result is None
        mock_send.assert_called_once()


class TestAssetSync:
    """Verify .claude/hooks/ and assets/hooks/ are kept in sync."""

    def test_hooks_and_assets_copies_identical(self):
        """Every hook in assets/hooks/ must have an identical copy in .claude/hooks/."""
        repo_root = Path(__file__).parent.parent
        assets_hooks = repo_root / "assets" / "hooks"
        claude_hooks = repo_root / ".claude" / "hooks"

        hook_files = [
            p
            for p in assets_hooks.iterdir()
            if p.is_file() and not p.name.startswith("test_") and p.name != "__pycache__"
        ]
        assert hook_files, "No hook files found in assets/hooks/"

        for assets_file in sorted(hook_files):
            claude_file = claude_hooks / assets_file.name
            assert claude_file.exists(), f"Missing in .claude/hooks/: {assets_file.name}"

            assets_content = assets_file.read_text()
            claude_content = claude_file.read_text()
            assert assets_content == claude_content, (
                f"Content mismatch: {assets_file.name}\nassets/hooks/ and .claude/hooks/ copies differ"
            )
