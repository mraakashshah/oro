#!/usr/bin/env python3
"""Tests for architect_router.py hook."""

import os
import subprocess
from unittest.mock import Mock, patch

# Import the module under test
import architect_router


class TestRouteCommand:
    """Test the routing decision logic."""

    def test_bead_commands_stay_local(self):
        assert architect_router.route_command("oro bead stats") == "architect"
        assert architect_router.route_command("oro bead ready") == "architect"
        assert architect_router.route_command("oro bead create --title='test'") == "architect"
        assert architect_router.route_command("  oro bead list") == "architect"
        assert architect_router.route_command("oro bead sync --from-main") == "architect"

    def test_oro_commands_stay_with_architect_router(self):
        assert architect_router.route_command("oro start") == "architect"
        assert architect_router.route_command("oro stop") == "architect"
        assert architect_router.route_command("oro directive status") == "architect"
        assert architect_router.route_command("oro directive scale 3") == "architect"
        assert architect_router.route_command("  oro directive pause") == "architect"

    def test_git_readonly_commands_stay_with_architect_router(self):
        assert architect_router.route_command("git status") == "architect"
        assert architect_router.route_command("git log") == "architect"
        assert architect_router.route_command("git diff") == "architect"

    def test_build_commands_stay_with_architect_router(self):
        assert architect_router.route_command("make test") == "architect"
        assert architect_router.route_command("go build") == "architect"
        assert architect_router.route_command("go test ./...") == "architect"

    def test_empty_commands_stay_with_architect_router(self):
        assert architect_router.route_command("") == "architect"
        assert architect_router.route_command("   ") == "architect"

    def test_unknown_commands_stay_with_architect_router(self):
        assert architect_router.route_command("echo oro bead stats") == "architect"
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
    def test_passthrough_for_bead_commands(self):
        hook_input = {
            "tool_name": "Bash",
            "tool_input": {"command": "oro bead stats"},
        }
        assert architect_router.build_decision(hook_input) is None

    @patch.dict(os.environ, {"ORO_ROLE": "architect"})
    @patch("architect_router.send_to_manager_pane", return_value=True)
    def test_allows_git_readonly_commands(self, mock_send):
        hook_input = {
            "tool_name": "Bash",
            "tool_input": {"command": "git status"},
        }
        result = architect_router.build_decision(hook_input)

        assert result is None
        mock_send.assert_not_called()

    @patch.dict(os.environ, {"ORO_ROLE": "architect"})
    @patch("architect_router.send_to_manager_pane", return_value=True)
    def test_blocks_oro_commands(self, mock_send):
        hook_input = {
            "tool_name": "Bash",
            "tool_input": {"command": "oro directive scale 3"},
        }
        result = architect_router.build_decision(hook_input)

        assert result is not None
        assert result["permissionDecision"] == "deny"
        assert result["message"] == "[BLOCKED] oro commands not allowed in architect pane: oro directive scale 3"
        mock_send.assert_not_called()

    @patch.dict(os.environ, {"ORO_ROLE": "architect"})
    @patch("architect_router.send_to_manager_pane", return_value=True)
    def test_blocks_build_commands(self, mock_send):
        hook_input = {
            "tool_name": "Bash",
            "tool_input": {"command": "make test"},
        }
        result = architect_router.build_decision(hook_input)

        assert result is not None
        assert result["permissionDecision"] == "deny"
        mock_send.assert_not_called()

    @patch.dict(os.environ, {"ORO_ROLE": "architect"})
    @patch("architect_router.send_to_manager_pane", return_value=False)
    def test_passthrough_when_tmux_send_fails(self, mock_send):
        # If tmux send-keys fails, don't block the command
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


class TestNotifyOnBeadCreate:
    """Test PostToolUse notification when architect creates tasks."""

    @patch.dict(os.environ, {"ORO_ROLE": "architect"})
    @patch("architect_router.send_to_manager_pane", return_value=True)
    def test_notifies_manager_on_bead_create(self, mock_send):
        """When architect creates a task, manager pane gets notification."""
        hook_input = {
            "tool_name": "Bash",
            "tool_input": {"command": "oro bead create --title='test task' --type=task"},
            "tool_output": "Created issue: oro-xyz123",
        }
        result = architect_router.notify_on_bead_create(hook_input)

        assert result is not None
        assert "additionalContext" in result
        mock_send.assert_called_once()

        # Verify the notification message content
        call_args = mock_send.call_args[0]
        assert "[NEW WORK]" in call_args[0]
        assert "Check oro task ready" in call_args[0]

    @patch.dict(os.environ, {"ORO_ROLE": "architect"})
    @patch("architect_router.send_to_manager_pane", return_value=True)
    def test_no_notification_for_non_bead_create_commands(self, mock_send):
        """Only bead create triggers notification, not other bead commands."""
        hook_input = {
            "tool_name": "Bash",
            "tool_input": {"command": "oro bead ready"},
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
            "tool_input": {"command": "oro bead create --title='test'"},
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
            "tool_input": {"command": "oro bead create --title='test'"},
            "tool_output": "Created issue: oro-xyz",
        }
        result = architect_router.notify_on_bead_create(hook_input)

        # Should return None (fail open) when tmux fails
        assert result is None
        mock_send.assert_called_once()
