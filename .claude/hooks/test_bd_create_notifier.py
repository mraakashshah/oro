#!/usr/bin/env python3
"""Tests for bd_create_notifier.py hook."""

import os
import subprocess
import sys
import unittest
from unittest.mock import MagicMock, patch

# Add the hooks directory to the path
sys.path.insert(0, os.path.dirname(__file__))

import bd_create_notifier


class TestBdCreateNotifier(unittest.TestCase):
    """Test suite for bd_create_notifier hook."""

    def test_get_oro_role_returns_architect(self):
        """Test that get_oro_role() reads ORO_ROLE from environment."""
        with patch.dict(os.environ, {"ORO_ROLE": "architect"}):
            self.assertEqual(bd_create_notifier.get_oro_role(), "architect")

    def test_get_oro_role_returns_empty_when_not_set(self):
        """Test that get_oro_role() returns empty string when ORO_ROLE is not set."""
        with patch.dict(os.environ, {}, clear=True):
            self.assertEqual(bd_create_notifier.get_oro_role(), "")

    def test_should_notify_returns_true_for_bd_create_as_architect(self):
        """Test that should_notify returns True for bd create commands as architect."""
        with patch.dict(os.environ, {"ORO_ROLE": "architect"}):
            hook_input = {
                "tool_name": "Bash",
                "tool_input": {"command": "bd create --title='Test' --type=task"},
            }
            self.assertTrue(bd_create_notifier.should_notify(hook_input))

    def test_should_notify_returns_false_when_not_architect(self):
        """Test that should_notify returns False when not architect role."""
        with patch.dict(os.environ, {"ORO_ROLE": "manager"}):
            hook_input = {
                "tool_name": "Bash",
                "tool_input": {"command": "bd create --title='Test' --type=task"},
            }
            self.assertFalse(bd_create_notifier.should_notify(hook_input))

    def test_should_notify_returns_false_for_non_bash_tools(self):
        """Test that should_notify returns False for non-Bash tools."""
        with patch.dict(os.environ, {"ORO_ROLE": "architect"}):
            hook_input = {
                "tool_name": "Read",
                "tool_input": {"file_path": "/some/file"},
            }
            self.assertFalse(bd_create_notifier.should_notify(hook_input))

    def test_should_notify_returns_false_for_non_bd_create_commands(self):
        """Test that should_notify returns False for non-bd-create commands."""
        with patch.dict(os.environ, {"ORO_ROLE": "architect"}):
            hook_input = {
                "tool_name": "Bash",
                "tool_input": {"command": "bd list --status=open"},
            }
            self.assertFalse(bd_create_notifier.should_notify(hook_input))

    def test_should_notify_returns_false_for_bd_create_substring(self):
        """Test that should_notify correctly identifies bd create at start."""
        with patch.dict(os.environ, {"ORO_ROLE": "architect"}):
            hook_input = {
                "tool_name": "Bash",
                "tool_input": {"command": "echo 'bd create' is the command"},
            }
            self.assertFalse(bd_create_notifier.should_notify(hook_input))

    @patch("subprocess.run")
    def test_notify_manager_sends_tmux_send_keys(self, mock_run):
        """Test that notify_manager calls tmux send-keys."""
        mock_run.return_value = MagicMock(returncode=0)
        result = bd_create_notifier.notify_manager("Test message", "oro")

        self.assertTrue(result)
        # send-keys makes 2 calls: one for text (-l flag), one for Enter
        self.assertEqual(mock_run.call_count, 2)

        # First call: send text with literal flag
        first_call = mock_run.call_args_list[0][0][0]
        self.assertEqual(first_call, ["tmux", "send-keys", "-t", "oro:manager", "-l", "Test message"])

        # Second call: send Enter
        second_call = mock_run.call_args_list[1][0][0]
        self.assertEqual(second_call, ["tmux", "send-keys", "-t", "oro:manager", "Enter"])

    @patch("subprocess.run")
    def test_notify_manager_returns_false_on_error(self, mock_run):
        """Test that notify_manager returns False when tmux command fails."""
        mock_run.side_effect = subprocess.CalledProcessError(1, "tmux")
        result = bd_create_notifier.notify_manager("Test message")
        self.assertFalse(result)

    @patch("subprocess.run")
    def test_notify_manager_returns_false_when_tmux_not_found(self, mock_run):
        """Test that notify_manager returns False when tmux is not installed."""
        mock_run.side_effect = FileNotFoundError()
        result = bd_create_notifier.notify_manager("Test message")
        self.assertFalse(result)


if __name__ == "__main__":
    unittest.main()
