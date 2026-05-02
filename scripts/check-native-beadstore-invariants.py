#!/usr/bin/env python3
"""Fail-closed invariant checks for native SQLite beadstore cutover."""

from __future__ import annotations

import argparse
import os
import sqlite3
import sys
from pathlib import Path

CHECKS: tuple[tuple[str, str, object | None], ...] = (
    (
        "integrity_check",
        "PRAGMA integrity_check;",
        "ok",
    ),
    (
        "legacy_foreign_key_violations",
        "SELECT COUNT(*) FROM pragma_foreign_key_check;",
        None,
    ),
    (
        "invalid_status_rows",
        "SELECT COUNT(*) FROM beads WHERE status NOT IN ('open','in_progress','blocked','closed');",
        0,
    ),
    (
        "ready_view_mismatches",
        """
        WITH expected AS (
          SELECT b.id
          FROM beads b
          WHERE b.deleted = 0
            AND b.status = 'open'
            AND (
              b.deferred_until IS NULL
              OR b.deferred_until = ''
              OR julianday(b.deferred_until) <= julianday('now')
            )
            AND NOT EXISTS (
              SELECT 1 FROM assignments a
              WHERE a.bead_id = b.id
                AND a.status = 'active'
            )
            AND NOT EXISTS (
              SELECT 1 FROM bead_deps d
              LEFT JOIN beads parent
                ON parent.id = d.depends_on_id
               AND parent.deleted = 0
              WHERE d.bead_id = b.id
                AND d.type IN ('blocks','conditional-blocks')
                AND (parent.id IS NULL OR parent.status != 'closed')
            )
        ),
        actual AS (
          SELECT id FROM beads_ready
        ),
        diff AS (
          SELECT id FROM expected EXCEPT SELECT id FROM actual
          UNION ALL
          SELECT id FROM actual EXCEPT SELECT id FROM expected
        )
        SELECT COUNT(*) FROM diff;
        """,
        0,
    ),
    (
        "blocked_view_mismatches",
        """
        WITH expected AS (
          SELECT b.id
          FROM beads b
          WHERE b.deleted = 0
            AND b.status IN ('open','blocked')
            AND (
              b.status = 'blocked'
              OR b.deferred_until IS NULL
              OR b.deferred_until = ''
              OR julianday(b.deferred_until) <= julianday('now')
              OR EXISTS (
                SELECT 1 FROM bead_deps d
                LEFT JOIN beads parent
                  ON parent.id = d.depends_on_id
                 AND parent.deleted = 0
                WHERE d.bead_id = b.id
                  AND d.type IN ('blocks','conditional-blocks')
                  AND (parent.id IS NULL OR parent.status != 'closed')
              )
            )
            AND NOT EXISTS (
              SELECT 1 FROM assignments a
              WHERE a.bead_id = b.id
                AND a.status = 'active'
            )
            AND (
              b.status = 'blocked'
              OR EXISTS (
                SELECT 1 FROM bead_deps d
                LEFT JOIN beads parent
                  ON parent.id = d.depends_on_id
                 AND parent.deleted = 0
                WHERE d.bead_id = b.id
                  AND d.type IN ('blocks','conditional-blocks')
                  AND (parent.id IS NULL OR parent.status != 'closed')
              )
            )
        ),
        actual AS (
          SELECT id FROM beads_blocked
        ),
        diff AS (
          SELECT id FROM expected EXCEPT SELECT id FROM actual
          UNION ALL
          SELECT id FROM actual EXCEPT SELECT id FROM expected
        )
        SELECT COUNT(*) FROM diff;
        """,
        0,
    ),
    (
        "ready_blocked_overlap",
        """
        SELECT COUNT(*)
        FROM beads_ready r
        JOIN beads_blocked b ON b.id = r.id;
        """,
        0,
    ),
    (
        "active_assignment_in_ready_or_blocked",
        """
        SELECT COUNT(*)
        FROM assignments a
        WHERE a.status = 'active'
          AND (
            EXISTS (SELECT 1 FROM beads_ready r WHERE r.id = a.bead_id)
            OR EXISTS (SELECT 1 FROM beads_blocked b WHERE b.id = a.bead_id)
          );
        """,
        0,
    ),
    (
        "ready_with_unclosed_hard_blocker",
        """
        SELECT COUNT(*)
        FROM beads_ready r
        JOIN bead_deps d ON d.bead_id = r.id
        LEFT JOIN beads parent ON parent.id = d.depends_on_id AND parent.deleted = 0
        WHERE d.type IN ('blocks','conditional-blocks')
          AND (parent.id IS NULL OR parent.status != 'closed');
        """,
        0,
    ),
)


def scalar(conn: sqlite3.Connection, sql: str) -> object:
    row = conn.execute(sql).fetchone()
    if row is None:
        raise RuntimeError("query returned no rows")
    return row[0]


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "--db",
        default=os.environ.get("ORO_DB_PATH"),
        help="path to state.db; defaults to ORO_DB_PATH",
    )
    args = parser.parse_args()
    if not args.db:
        print("missing --db or ORO_DB_PATH", file=sys.stderr)
        return 2

    db_path = Path(args.db)
    if not db_path.is_file():
        print(f"database not found: {db_path}", file=sys.stderr)
        return 2

    ok = True
    conn = sqlite3.connect(f"file:{db_path}?mode=ro", uri=True)
    try:
        for name, sql, expected in CHECKS:
            actual = scalar(conn, sql)
            print(f"{name}={actual}")
            if expected is not None and actual != expected:
                ok = False
                print(f"FAIL {name}: expected {expected!r}, got {actual!r}", file=sys.stderr)
    finally:
        conn.close()

    return 0 if ok else 1


if __name__ == "__main__":
    raise SystemExit(main())
