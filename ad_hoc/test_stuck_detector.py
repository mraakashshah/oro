"""Regression tests for worker-progress event history parsing."""

from datetime import datetime

from ad_hoc.stuck_detector import progress_timestamps


def _event(timestamp: str, worker_id: str, event_type: str, bead_id: str) -> str:
    """Render one event in the format produced by ``oro logs``."""
    return f"{timestamp} | {worker_id} | {event_type} | {bead_id} | source | "


def test_progress_timestamps_from_event_history() -> None:
    """Newest matching event timestamps win despite input ordering and noise."""
    timestamps = progress_timestamps(
        [
            _event("2026-07-20 09:03:00", "worker-1", "worker_progress", "oro-vrmb"),
            _event("2026-07-20 09:01:00", "worker-1", "assign", "oro-vrmb"),
            _event("2026-07-20 09:02:00", "worker-1", "ready_for_review", "oro-vrmb"),
            _event("2026-07-20 09:04:00", "worker-1", "worktree_reused", "oro-vrmb"),
            "not an event row",
            "not-a-timestamp | worker-1 | worker_progress | oro-vrmb | assign | ",
            _event("2026-07-20 09:05:00", "worker-2", "worker_progress", "oro-vrmb"),
            _event("2026-07-20 09:06:00", "worker-1", "worker_progress", "oro-other"),
            _event("2026-07-20 09:07:00", "worker-1", "assign", "oro-vrmb"),
            _event("2026-07-20 09:08:00", "worker-1", "ready_for_review", "oro-vrmb"),
            _event("2026-07-20 09:09:00", "worker-1", "worker_progress", "oro-vrmb"),
        ],
        bead_id="oro-vrmb",
        worker_id="worker-1",
    )

    assert timestamps == (
        datetime(2026, 7, 20, 9, 7),
        datetime(2026, 7, 20, 9, 8),
        datetime(2026, 7, 20, 9, 9),
    )


def test_progress_timestamps_returns_none_for_missing_event_kinds() -> None:
    """Missing event kinds remain absent in the returned tuple."""
    timestamps = progress_timestamps(
        [_event("2026-07-20T09:00:00Z", "worker-1", "assign", "oro-vrmb")],
        bead_id="oro-vrmb",
        worker_id="worker-1",
    )

    assert timestamps == (datetime(2026, 7, 20, 9), None, None)
