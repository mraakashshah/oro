from __future__ import annotations

import importlib.util
import sqlite3
from pathlib import Path


def load_invariants_module():
    path = Path(__file__).with_name("check-native-beadstore-invariants.py")
    spec = importlib.util.spec_from_file_location("check_native_beadstore_invariants", path)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"could not load {path}")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def insert_bead(
    conn: sqlite3.Connection,
    bead_id: str,
    *,
    bead_type: str = "task",
    status: str = "open",
    parent_id: str | None = None,
    deleted: int = 0,
) -> None:
    conn.execute(
        """
        INSERT INTO beads (id, title, description, status, type, parent_id, deleted)
        VALUES (?, ?, '', ?, ?, ?, ?)
        """,
        (bead_id, bead_id, status, bead_type, parent_id, deleted),
    )


def test_epic_child_blocker_query_flags_open_children_without_edges() -> None:
    module = load_invariants_module()
    conn = sqlite3.connect(":memory:")
    conn.executescript(
        """
        CREATE TABLE beads (
            id TEXT PRIMARY KEY,
            title TEXT NOT NULL,
            description TEXT NOT NULL DEFAULT '',
            status TEXT NOT NULL,
            type TEXT NOT NULL DEFAULT 'task',
            parent_id TEXT,
            deleted INTEGER NOT NULL DEFAULT 0
        );
        CREATE TABLE bead_deps (
            bead_id TEXT NOT NULL,
            depends_on_id TEXT NOT NULL,
            type TEXT NOT NULL DEFAULT 'blocks'
        );
        """
    )

    insert_bead(conn, "epic-open", bead_type="epic")
    insert_bead(conn, "child-open", parent_id="epic-open")
    insert_bead(conn, "child-closed", status="closed", parent_id="epic-open")
    insert_bead(conn, "epic-deleted", bead_type="epic", deleted=1)
    insert_bead(conn, "child-deleted-parent", parent_id="epic-deleted")
    insert_bead(conn, "epic-with-deleted-child", bead_type="epic")
    insert_bead(conn, "child-deleted", parent_id="epic-with-deleted-child", deleted=1)
    insert_bead(conn, "epic-closed", bead_type="epic", status="closed")
    insert_bead(conn, "child-open-closed-epic", parent_id="epic-closed")
    conn.execute(
        "INSERT INTO bead_deps (bead_id, depends_on_id, type) VALUES (?, ?, ?)",
        ("child-open-closed-epic", "epic-closed", "blocks"),
    )

    messages = module.check_epic_child_blocker_edges(conn)

    assert messages == [
        "epic child blocker edge missing: epic=epic-open child=child-open",
    ]

    conn.execute(
        "INSERT INTO bead_deps (bead_id, depends_on_id, type) VALUES (?, ?, ?)",
        ("child-open", "epic-open", "blocks"),
    )

    assert module.check_epic_child_blocker_edges(conn) == []

    insert_bead(conn, "epic-parent-dep-only", bead_type="epic")
    insert_bead(conn, "child-parent-dep-only", parent_id="epic-parent-dep-only")
    conn.execute(
        "INSERT INTO bead_deps (bead_id, depends_on_id, type) VALUES (?, ?, ?)",
        ("epic-parent-dep-only", "child-parent-dep-only", "blocks"),
    )

    assert module.check_epic_child_blocker_edges(conn) == [
        "epic child blocker edge missing: epic=epic-parent-dep-only child=child-parent-dep-only"
    ]

    conn.execute(
        "INSERT INTO bead_deps (bead_id, depends_on_id, type) VALUES (?, ?, ?)",
        ("child-parent-dep-only", "epic-parent-dep-only", "conditional-blocks"),
    )
    assert module.check_epic_child_blocker_edges(conn) == []
