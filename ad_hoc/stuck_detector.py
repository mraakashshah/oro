"""Helpers for reconstructing worker progress from formatted Oro event logs."""

import argparse
from collections.abc import Iterable
from datetime import UTC, datetime
from typing import Literal

PROGRESS_TIMEOUT_MINUTES = 15


def progress_timestamps(
    event_lines: Iterable[str], bead_id: str, worker_id: str
) -> tuple[datetime | None, datetime | None, datetime | None]:
    """Return the newest assign, review-ready, and progress event timestamps."""
    latest: dict[str, datetime | None] = {
        "assign": None,
        "ready_for_review": None,
        "worker_progress": None,
    }

    for line in event_lines:
        event = _parse_event_line(line)
        if event is None:
            continue

        timestamp, row_worker_id, event_type, row_bead_id = event
        if row_worker_id != worker_id or row_bead_id != bead_id or event_type not in latest:
            continue

        prior = latest[event_type]
        if prior is None or timestamp > prior:
            latest[event_type] = timestamp

    return (
        latest["assign"],
        latest["ready_for_review"],
        latest["worker_progress"],
    )


def commit_age_min(last_commit_ts: datetime | None, now: datetime) -> int | None:
    """Return whole commit age minutes for diagnostics, if a commit is known."""
    if last_commit_ts is None:
        return None
    return max(0, int((now - last_commit_ts).total_seconds() // 60))


def classify_worker(
    assign_ts: datetime | None,
    ready_for_review_ts: datetime | None,
    last_progress_ts: datetime | None,
    last_commit_ts: datetime | None,
    process_alive: bool,
    now: datetime,
) -> Literal["ok", "stale", "STUCK"]:
    """Classify lifecycle health without treating worktree commit age as progress."""
    _ = last_commit_ts
    if not process_alive:
        return "stale"

    lifecycle_timestamps = (assign_ts, ready_for_review_ts, last_progress_ts)
    latest_progress = max((ts for ts in lifecycle_timestamps if ts is not None), default=None)
    if latest_progress is None:
        return "ok"
    if (now - latest_progress).total_seconds() > PROGRESS_TIMEOUT_MINUTES * 60:
        return "STUCK"
    return "ok"


def main() -> int:
    """Print the lifecycle verdict and return its shell-compatible status."""
    parser = argparse.ArgumentParser()
    parser.add_argument("--assigned-at", type=_parse_timestamp)
    parser.add_argument("--ready-for-review-at", type=_parse_timestamp)
    parser.add_argument("--last-progress-at", type=_parse_timestamp)
    parser.add_argument("--last-commit-at", type=_parse_timestamp)
    parser.add_argument("--process-alive", action="store_true")
    parser.add_argument("--now", type=_parse_timestamp)
    args = parser.parse_args()

    verdict = classify_worker(
        args.assigned_at,
        args.ready_for_review_at,
        args.last_progress_at,
        args.last_commit_at,
        args.process_alive,
        args.now or datetime.now(),
    )
    print(verdict)
    return 2 if verdict == "STUCK" else 0


def _parse_event_line(line: str) -> tuple[datetime, str, str, str] | None:
    fields = line.split("|", maxsplit=5)
    if len(fields) != 6:
        return None

    timestamp = _parse_timestamp(fields[0].strip())
    if timestamp is None:
        return None

    return timestamp, fields[1].strip(), fields[2].strip(), fields[3].strip()


def _parse_timestamp(value: str) -> datetime | None:
    try:
        timestamp = datetime.fromisoformat(value)
    except ValueError:
        return None

    if timestamp.tzinfo is None:
        return timestamp
    return timestamp.astimezone(UTC).replace(tzinfo=None)


if __name__ == "__main__":
    raise SystemExit(main())
