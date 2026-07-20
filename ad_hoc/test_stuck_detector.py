"""Regression tests for worker-progress event history parsing."""

from datetime import datetime, timedelta

from ad_hoc.stuck_detector import classify_worker, main, progress_timestamps


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


def test_worktree_reuse_not_stuck(capsys, monkeypatch) -> None:
    """Lifecycle progress, not an old worktree commit, determines a live verdict."""
    now = datetime(2026, 7, 20, 12)
    old_commit = now - timedelta(minutes=243)
    assigned = now - timedelta(minutes=29)
    review_ready = now - timedelta(minutes=14)

    assert classify_worker(assigned, review_ready, None, old_commit, True, now) == "ok"
    assert classify_worker(None, None, None, old_commit, False, now) == "stale"
    assert classify_worker(None, None, None, old_commit, True, now) == "ok"
    assert (
        classify_worker(
            assigned,
            None,
            now - timedelta(minutes=16),
            old_commit,
            True,
            now,
        )
        == "STUCK"
    )

    monkeypatch.setattr(
        "sys.argv",
        [
            "stuck_detector.py",
            "--assigned-at",
            assigned.isoformat(),
            "--ready-for-review-at",
            review_ready.isoformat(),
            "--last-commit-at",
            old_commit.isoformat(),
            "--process-alive",
            "--now",
            now.isoformat(),
        ],
    )
    assert main() == 0
    assert capsys.readouterr().out.strip() == "ok"

    monkeypatch.setattr(
        "sys.argv",
        [
            "stuck_detector.py",
            "--assigned-at",
            assigned.isoformat(),
            "--last-progress-at",
            (now - timedelta(minutes=16)).isoformat(),
            "--last-commit-at",
            old_commit.isoformat(),
            "--process-alive",
            "--now",
            now.isoformat(),
        ],
    )
    assert main() == 2
    assert capsys.readouterr().out.strip() == "STUCK"

    monkeypatch.setattr(
        "sys.argv",
        [
            "stuck_detector.py",
            "--assigned-at",
            "2020-01-01T00:00:00",
            "--process-alive",
        ],
    )
    assert main() == 2
    assert capsys.readouterr().out.strip() == "STUCK"
