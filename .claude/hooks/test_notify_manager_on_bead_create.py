#!/usr/bin/env python3
"""Tests for notify_manager_on_bead_create.py batching/debounce logic."""

import json
import os
from pathlib import Path
from unittest import mock

from notify_manager_on_bead_create import (
    extract_bead_id_from_output,
    format_batch_notification,
    handle_post_tool_use,
    load_state,
    record_bead_create,
    save_state,
    should_notify,
)

# ---------------------------------------------------------------------------
# Pure function tests
# ---------------------------------------------------------------------------


class TestExtractBeadIdFromOutput:
    def test_extracts_id_from_standard_output(self):
        output = "✓ Created issue: oro-abc\n  Title: Test bead\n"
        assert extract_bead_id_from_output(output) == "oro-abc"

    def test_extracts_id_with_dot_separated_id(self):
        output = "Created issue: oro-18c5.3"
        assert extract_bead_id_from_output(output) == "oro-18c5.3"

    def test_returns_empty_string_when_not_found(self):
        assert extract_bead_id_from_output("some other output") == ""

    def test_returns_empty_string_on_empty_output(self):
        assert extract_bead_id_from_output("") == ""


class TestLoadState:
    def test_returns_empty_state_when_file_missing(self, tmp_path):
        state = load_state(str(tmp_path / "nonexistent.json"))
        assert state == {"beads": [], "last_create_ts": 0.0}

    def test_returns_empty_state_when_file_corrupt(self, tmp_path):
        p = tmp_path / "state.json"
        p.write_text("not json")
        state = load_state(str(p))
        assert state == {"beads": [], "last_create_ts": 0.0}

    def test_returns_saved_state_when_file_valid(self, tmp_path):
        p = tmp_path / "state.json"
        data = {"beads": [{"id": "oro-abc", "title": "Test bead"}], "last_create_ts": 1234.5}
        p.write_text(json.dumps(data))
        state = load_state(str(p))
        assert state == data


class TestSaveState:
    def test_writes_json_to_file(self, tmp_path):
        p = tmp_path / "state.json"
        state = {"beads": [{"id": "oro-abc", "title": "A bead"}], "last_create_ts": 100.0}
        save_state(state, str(p))
        written = json.loads(p.read_text())
        assert written == state

    def test_creates_parent_dirs(self, tmp_path):
        p = tmp_path / "nested" / "dir" / "state.json"
        state = {"beads": [], "last_create_ts": 0.0}
        save_state(state, str(p))
        assert p.exists()


class TestRecordBeadCreate:
    def test_appends_bead_to_empty_state(self):
        state = {"beads": [], "last_create_ts": 0.0}
        now = 1000.0
        new_state = record_bead_create("oro-abc", "My Bead", now, state)
        assert new_state["beads"] == [{"id": "oro-abc", "title": "My Bead"}]
        assert new_state["last_create_ts"] == 1000.0

    def test_appends_bead_to_existing_state(self):
        state = {
            "beads": [{"id": "oro-111", "title": "First"}],
            "last_create_ts": 900.0,
        }
        now = 1000.0
        new_state = record_bead_create("oro-222", "Second", now, state)
        assert len(new_state["beads"]) == 2
        assert new_state["beads"][1] == {"id": "oro-222", "title": "Second"}
        assert new_state["last_create_ts"] == 1000.0

    def test_does_not_mutate_input_state(self):
        state = {"beads": [], "last_create_ts": 0.0}
        record_bead_create("oro-abc", "Test", 100.0, state)
        assert state["beads"] == []

    def test_updates_last_create_ts(self):
        state = {"beads": [], "last_create_ts": 500.0}
        new_state = record_bead_create("oro-x", "X", 999.0, state)
        assert new_state["last_create_ts"] == 999.0


class TestShouldNotify:
    def test_returns_false_when_no_beads(self):
        state = {"beads": [], "last_create_ts": 0.0}
        assert should_notify(state, now=1000.0, window_secs=30) is False

    def test_returns_false_within_debounce_window(self):
        # last create was 10s ago, window is 30s → still within window
        state = {"beads": [{"id": "oro-1", "title": "T"}], "last_create_ts": 990.0}
        assert should_notify(state, now=1000.0, window_secs=30) is False

    def test_returns_true_after_debounce_window_expires(self):
        # last create was 31s ago, window is 30s → window has expired
        state = {"beads": [{"id": "oro-1", "title": "T"}], "last_create_ts": 969.0}
        assert should_notify(state, now=1000.0, window_secs=30) is True

    def test_returns_true_exactly_at_window_boundary(self):
        # last create was exactly 30s ago → boundary is inclusive
        state = {"beads": [{"id": "oro-1", "title": "T"}], "last_create_ts": 970.0}
        assert should_notify(state, now=1000.0, window_secs=30) is True


class TestFormatBatchNotification:
    def test_single_bead_message(self):
        state = {"beads": [{"id": "oro-abc", "title": "Do the thing"}], "last_create_ts": 0.0}
        msg = format_batch_notification(state)
        assert "oro-abc" in msg
        assert "Do the thing" in msg

    def test_multiple_beads_message(self):
        state = {
            "beads": [
                {"id": "oro-1", "title": "First"},
                {"id": "oro-2", "title": "Second"},
                {"id": "oro-3", "title": "Third"},
            ],
            "last_create_ts": 0.0,
        }
        msg = format_batch_notification(state)
        assert "oro-1" in msg
        assert "oro-2" in msg
        assert "oro-3" in msg
        assert "First" in msg
        assert "Second" in msg
        assert "Third" in msg

    def test_message_indicates_new_work_available(self):
        state = {"beads": [{"id": "oro-x", "title": "X"}], "last_create_ts": 0.0}
        msg = format_batch_notification(state)
        # Should contain some indication of new work
        assert any(kw in msg.upper() for kw in ("NEW WORK", "READY", "BEAD"))


# ---------------------------------------------------------------------------
# Integration tests: handle_post_tool_use with batching
# ---------------------------------------------------------------------------


class TestHandlePostToolUseBatching:
    """Test that handle_post_tool_use batches notifications correctly."""

    def _make_hook_input(self, command: str) -> dict:
        return {
            "tool_name": "Bash",
            "tool_input": {"command": command},
        }

    def test_5_rapid_beads_produce_1_notification(self, tmp_path):
        """Creating 5 beads in rapid succession produces 1 grouped notification."""
        state_file = str(tmp_path / "state.json")
        sent_notifications = []

        def mock_notify(message, session_name="oro"):
            sent_notifications.append(message)
            return True

        # Simulate 5 rapid bd create calls at t=0,1,2,3,4 (all within 30s window)
        base_ts = 1000.0
        with (
            mock.patch.dict(os.environ, {"ORO_ROLE": "architect"}),
            mock.patch("notify_manager_on_bead_create.notify_manager", side_effect=mock_notify),
        ):
            for i in range(5):
                now = base_ts + i  # 1s apart, all within 30s window
                handle_post_tool_use(
                    self._make_hook_input(f"bd create --title='Bead {i}' --type=task"),
                    state_file=state_file,
                    now=now,
                    window_secs=30,
                )

        # No notifications yet — all within debounce window
        assert len(sent_notifications) == 0, (
            f"Expected 0 notifications during window, got {len(sent_notifications)}: {sent_notifications}"
        )

        # Now simulate the debounce window expiring (31s after last create)
        # In real usage, the next bd create (or a timer) would trigger flush.
        # We simulate this by calling handle_post_tool_use with no new bd create
        # but after the window — but since the hook only fires on bd create,
        # we need a flush trigger. Let's call it with now = base_ts + 4 + 31.
        flush_ts = base_ts + 4 + 31  # 31s after last create
        with (
            mock.patch.dict(os.environ, {"ORO_ROLE": "architect"}),
            mock.patch("notify_manager_on_bead_create.notify_manager", side_effect=mock_notify),
        ):
            # A new bd create after window → triggers flush of accumulated beads
            # and starts a new batch with this bead
            handle_post_tool_use(
                self._make_hook_input("bd create --title='Bead 5' --type=task"),
                state_file=state_file,
                now=flush_ts,
                window_secs=30,
            )

        # Should have sent exactly 1 grouped notification for the first 5 beads
        assert len(sent_notifications) == 1, (
            f"Expected 1 grouped notification, got {len(sent_notifications)}: {sent_notifications}"
        )
        # The notification should mention multiple bead IDs or a count
        msg = sent_notifications[0]
        assert "5" in msg or "Bead 0" in msg or "Bead 1" in msg or "[NEW WORK]" in msg

    def test_single_bead_notifies_after_window(self, tmp_path):
        """Single bead creation still sends notification after debounce window."""
        state_file = str(tmp_path / "state.json")
        sent_notifications = []

        def mock_notify(message, session_name="oro"):
            sent_notifications.append(message)
            return True

        base_ts = 1000.0
        with (
            mock.patch.dict(os.environ, {"ORO_ROLE": "architect"}),
            mock.patch("notify_manager_on_bead_create.notify_manager", side_effect=mock_notify),
        ):
            # First bead created
            handle_post_tool_use(
                self._make_hook_input("bd create --title='Solo Bead' --type=task"),
                state_file=state_file,
                now=base_ts,
                window_secs=30,
            )

        # No notification yet (within window)
        assert len(sent_notifications) == 0

        # Second invocation after window expires (simulated via next bd create)
        with (
            mock.patch.dict(os.environ, {"ORO_ROLE": "architect"}),
            mock.patch("notify_manager_on_bead_create.notify_manager", side_effect=mock_notify),
        ):
            handle_post_tool_use(
                self._make_hook_input("bd create --title='Another Bead' --type=task"),
                state_file=state_file,
                now=base_ts + 31,
                window_secs=30,
            )

        # 1 notification for the first batch (1 bead)
        assert len(sent_notifications) == 1
        assert "Solo Bead" in sent_notifications[0]

    def test_no_new_beads_no_notification(self, tmp_path):
        """No new beads → no notification."""
        state_file = str(tmp_path / "state.json")
        sent_notifications = []

        def mock_notify(message, session_name="oro"):
            sent_notifications.append(message)
            return True

        with (
            mock.patch.dict(os.environ, {"ORO_ROLE": "architect"}),
            mock.patch("notify_manager_on_bead_create.notify_manager", side_effect=mock_notify),
        ):
            # Non-bd-create command
            handle_post_tool_use(
                self._make_hook_input("git status"),
                state_file=state_file,
                now=1000.0,
                window_secs=30,
            )

        assert len(sent_notifications) == 0

    def test_non_architect_role_no_notification(self, tmp_path):
        """Non-architect roles do not send notifications."""
        state_file = str(tmp_path / "state.json")
        sent_notifications = []

        def mock_notify(message, session_name="oro"):
            sent_notifications.append(message)
            return True

        with (
            mock.patch.dict(os.environ, {"ORO_ROLE": "worker"}),
            mock.patch("notify_manager_on_bead_create.notify_manager", side_effect=mock_notify),
        ):
            handle_post_tool_use(
                self._make_hook_input("bd create --title='Test' --type=task"),
                state_file=state_file,
                now=1000.0,
                window_secs=30,
            )

        assert len(sent_notifications) == 0

    def test_grouped_notification_lists_bead_ids_and_titles(self, tmp_path):
        """The grouped notification lists newly-ready bead IDs and titles."""
        state_file = str(tmp_path / "state.json")
        sent_notifications = []

        def mock_notify(message, session_name="oro"):
            sent_notifications.append(message)
            return True

        # Simulate bd create commands that return bead IDs in output
        # We need to test that the notification includes id+title info
        # Pre-load state with known beads (simulating previous calls)
        pre_state = {
            "beads": [
                {"id": "oro-aaa", "title": "Alpha feature"},
                {"id": "oro-bbb", "title": "Beta fix"},
            ],
            "last_create_ts": 1000.0,
        }
        state_file_path = Path(state_file)
        state_file_path.parent.mkdir(parents=True, exist_ok=True)
        state_file_path.write_text(json.dumps(pre_state))

        # Trigger flush by calling after window expires
        with (
            mock.patch.dict(os.environ, {"ORO_ROLE": "architect"}),
            mock.patch("notify_manager_on_bead_create.notify_manager", side_effect=mock_notify),
        ):
            handle_post_tool_use(
                self._make_hook_input("bd create --title='New bead' --type=task"),
                state_file=state_file,
                now=1000.0 + 31,  # after 30s window
                window_secs=30,
            )

        assert len(sent_notifications) == 1
        msg = sent_notifications[0]
        assert "oro-aaa" in msg, f"Expected oro-aaa in: {msg}"
        assert "oro-bbb" in msg, f"Expected oro-bbb in: {msg}"
        assert "Alpha feature" in msg, f"Expected 'Alpha feature' in: {msg}"
        assert "Beta fix" in msg, f"Expected 'Beta fix' in: {msg}"
