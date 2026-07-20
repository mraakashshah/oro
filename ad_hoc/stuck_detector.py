"""Helpers for reconstructing worker progress from formatted Oro event logs."""

from collections.abc import Iterable
from datetime import UTC, datetime


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
