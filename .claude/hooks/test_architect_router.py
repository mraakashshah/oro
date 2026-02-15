#!/usr/bin/env python3
"""Tests for architect_router.py hook."""

import os
import subprocess
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

    def test_oro_commands_forward_to_manager(self):
        assert architect_router.route_command("oro start") == "manager"
        assert architect_router.route_command("oro stop") == "manager"
        assert architect_router.route_command("oro directive status") == "manager"
        assert architect_router.route_command("oro directive scale 3") == "manager"
        assert architect_router.route_command("  oro directive pause") == "manager"

    def test_unknown_commands_forward_to_manager(self):
        assert architect_router.route_command("git status") == "manager"
        assert architect_router.route_command("ls -la") == "manager"
        assert architect_router.route_command("make test") == "manager"
        assert architect_router.route_command("some-random-command") == "manager"

    def test_empty_commands_forward_to_manager(self):
        assert architect_router.route_command("") == "manager"
        assert architect_router.route_command("   ") == "manager"

    def test_bd_as_substring_forwards_to_manager(self):
        # "bd" must be at the start followed by a space
        assert architect_router.route_command("echo bd stats") == "manager"
        assert architect_router.route_command("notbd stats") == "manager"


class TestFormatForwardMessage:
    """Test the feedback message formatting."""

    def test_oro_commands_get_specific_message(self):
        assert (
            architect_router.format_forward_message("oro directive scale 3")
            == "[forwarded to manager] oro directive scale 3"
        )
        assert architect_router.format_forward_message("oro status") == "[forwarded to manager] oro status"

    def test_other_commands_get_generic_message(self):
        assert architect_router.format_forward_message("git status") == "[forwarded] git status"
        assert architect_router.format_forward_message("make test") == "[forwarded] make test"


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
    @patch("architect_router.send_to_manager_pane", return_value=True)
    def test_blocks_and_forwards_oro_commands(self, mock_send):
        hook_input = {
            "tool_name": "Bash",
            "tool_input": {"command": "oro directive scale 3"},
        }
        result = architect_router.build_decision(hook_input)

        assert result is not None
        assert result["permissionDecision"] == "deny"
        assert result["message"] == "[forwarded to manager] oro directive scale 3"
        mock_send.assert_called_once_with("oro directive scale 3")

    @patch.dict(os.environ, {"ORO_ROLE": "architect"})
    @patch("architect_router.send_to_manager_pane", return_value=True)
    def test_blocks_and_forwards_unknown_commands(self, mock_send):
        hook_input = {
            "tool_name": "Bash",
            "tool_input": {"command": "git status"},
        }
        result = architect_router.build_decision(hook_input)

        assert result is not None
        assert result["permissionDecision"] == "deny"
        assert result["message"] == "[forwarded] git status"
        mock_send.assert_called_once_with("git status")

    @patch.dict(os.environ, {"ORO_ROLE": "architect"})
    @patch("architect_router.send_to_manager_pane", return_value=False)
    def test_passthrough_when_tmux_send_fails(self, mock_send):
        # If tmux send-keys fails, don't block the command
        hook_input = {
            "tool_name": "Bash",
            "tool_input": {"command": "oro status"},
        }
        result = architect_router.build_decision(hook_input)

        assert result is None
        mock_send.assert_called_once()


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
        assert "Check bd ready" in call_args[0]

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
