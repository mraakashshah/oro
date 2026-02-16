#!/usr/bin/env python3
"""Tests for session_start_extras.py pane activity tracking."""

import os
import sqlite3
import tempfile
import time
from pathlib import Path
from unittest import mock

import pytest
from session_start_extras import main


@pytest.fixture
def temp_state_db():
    """Create a temporary SQLite database with pane_activity table."""
    with tempfile.NamedTemporaryFile(suffix=".db", delete=False) as f:
        db_path = f.name

    conn = sqlite3.connect(db_path)
    conn.execute("""
        CREATE TABLE IF NOT EXISTS pane_activity (
            pane TEXT PRIMARY KEY,
            last_seen INTEGER
        )
    """)
    conn.commit()
    conn.close()

    yield db_path

    # Cleanup
    Path(db_path).unlink(missing_ok=True)


def test_pane_activity_written_when_oro_role_set(temp_state_db):
    """Test that pane_activity is written when ORO_ROLE is set."""
    # Setup environment
    oro_home = Path(temp_state_db).parent

    # Mock update_pane_activity to use our test db
    original_update = __import__("session_start_extras").update_pane_activity

    def mock_update(role: str, state_db: str | None = None):
        _ = state_db  # Unused in mock
        return original_update(role, temp_state_db)

    with (
        mock.patch.dict(
            os.environ,
            {
                "ORO_ROLE": "architect",
                "ORO_HOME": str(oro_home),
            },
        ),
        mock.patch("session_start_extras.oro_home", return_value=str(oro_home)),
        mock.patch("session_start_extras.update_pane_activity", side_effect=mock_update),
        mock.patch("sys.stdin", mock.MagicMock(read=lambda: "{}")),
        mock.patch("sys.stdout", mock.MagicMock()),
        mock.patch("subprocess.run"),
    ):
        before_ts = int(time.time())
        main()
        after_ts = int(time.time())

    # Verify database was updated
    conn = sqlite3.connect(temp_state_db)
    cursor = conn.execute("SELECT pane, last_seen FROM pane_activity WHERE pane = ?", ("architect",))
    row = cursor.fetchone()
    conn.close()

    assert row is not None, "Expected pane_activity row for 'architect'"
    pane, last_seen = row
    assert pane == "architect"
    assert before_ts <= last_seen <= after_ts, f"Timestamp {last_seen} not in range [{before_ts}, {after_ts}]"


def test_pane_activity_skipped_when_oro_role_not_set(temp_state_db):
    """Test that pane_activity is skipped when ORO_ROLE is not set."""
    oro_home = Path(temp_state_db).parent

    # Mock update_pane_activity to use our test db
    original_update = __import__("session_start_extras").update_pane_activity

    def mock_update(role: str, state_db: str | None = None):
        _ = state_db  # Unused in mock
        return original_update(role, temp_state_db)

    with (
        mock.patch.dict(os.environ, {}, clear=True),
        mock.patch("session_start_extras.oro_home", return_value=str(oro_home)),
        mock.patch("session_start_extras.update_pane_activity", side_effect=mock_update),
        mock.patch("sys.stdin", mock.MagicMock(read=lambda: "{}")),
        mock.patch("sys.stdout", mock.MagicMock()),
        mock.patch("subprocess.run"),
    ):
        main()

    # Verify no row was inserted
    conn = sqlite3.connect(temp_state_db)
    cursor = conn.execute("SELECT COUNT(*) FROM pane_activity")
    count = cursor.fetchone()[0]
    conn.close()

    assert count == 0, "Expected no pane_activity rows when ORO_ROLE not set"


def test_pane_activity_updates_existing_row(temp_state_db):
    """Test that pane_activity updates existing row (INSERT OR REPLACE)."""
    oro_home = Path(temp_state_db).parent

    # Insert initial row
    conn = sqlite3.connect(temp_state_db)
    initial_ts = int(time.time()) - 100
    conn.execute("INSERT INTO pane_activity (pane, last_seen) VALUES (?, ?)", ("manager", initial_ts))
    conn.commit()
    conn.close()

    # Mock update_pane_activity to use our test db
    original_update = __import__("session_start_extras").update_pane_activity

    def mock_update(role: str, state_db: str | None = None):
        _ = state_db  # Unused in mock
        return original_update(role, temp_state_db)

    # Run hook
    with (
        mock.patch.dict(
            os.environ,
            {
                "ORO_ROLE": "manager",
                "ORO_HOME": str(oro_home),
            },
        ),
        mock.patch("session_start_extras.oro_home", return_value=str(oro_home)),
        mock.patch("session_start_extras.update_pane_activity", side_effect=mock_update),
        mock.patch("sys.stdin", mock.MagicMock(read=lambda: "{}")),
        mock.patch("sys.stdout", mock.MagicMock()),
        mock.patch("subprocess.run"),
    ):
        before_ts = int(time.time())
        main()
        after_ts = int(time.time())

    # Verify row was updated
    conn = sqlite3.connect(temp_state_db)
    cursor = conn.execute("SELECT last_seen FROM pane_activity WHERE pane = ?", ("manager",))
    row = cursor.fetchone()
    count_cursor = conn.execute("SELECT COUNT(*) FROM pane_activity WHERE pane = ?", ("manager",))
    count = count_cursor.fetchone()[0]
    conn.close()

    assert count == 1, "Expected exactly one row for 'manager'"
    assert row is not None
    last_seen = row[0]
    assert last_seen > initial_ts, f"Timestamp should be updated from {initial_ts} to {last_seen}"
    assert before_ts <= last_seen <= after_ts
