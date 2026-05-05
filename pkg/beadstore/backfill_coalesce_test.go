package beadstore_test

import (
	"context"
	"database/sql"
	"testing"

	"oro/pkg/beadstore"

	_ "modernc.org/sqlite"
)

// openLegacyDB returns an in-memory SQLite DB whose beads table intentionally
// omits NOT NULL on created_at / closed_at to simulate pre-migration databases
// where those timestamps could be NULL. bead_journey is created with the
// current schema (ts TEXT NOT NULL) so the COALESCE backfill is the only thing
// that can satisfy the constraint.
func openLegacyDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.ExecContext(context.Background(), `
CREATE TABLE IF NOT EXISTS beads (
    id         TEXT PRIMARY KEY,
    title      TEXT NOT NULL,
    status     TEXT NOT NULL DEFAULT 'open',
    deleted    INTEGER NOT NULL DEFAULT 0,
    created_at TEXT,
    updated_at TEXT,
    closed_at  TEXT
);
CREATE TABLE IF NOT EXISTS bead_journey (
    id      INTEGER PRIMARY KEY AUTOINCREMENT,
    bead_id TEXT NOT NULL REFERENCES beads(id) ON DELETE CASCADE,
    ts      TEXT NOT NULL,
    actor   TEXT NOT NULL,
    event   TEXT NOT NULL,
    payload TEXT
);
`); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	return db
}

// TestBackfillCoalesce verifies that BackfillJourneyEvents:
//   - emits an 'imported' journey event for every bead, including those with NULL created_at
//   - emits a 'closed' journey event for closed beads, including those with NULL closed_at
//   - always writes a non-NULL ts (satisfying bead_journey.ts NOT NULL)
//   - is idempotent: a second call does not duplicate events
func TestBackfillCoalesce(t *testing.T) {
	ctx := context.Background()
	db := openLegacyDB(t)

	exec := func(stmt string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, stmt, args...); err != nil {
			t.Fatalf("exec: %v", err)
		}
	}

	// Normal open bead — both timestamps present.
	exec(`INSERT INTO beads (id, title, status, created_at, updated_at) VALUES ('b-normal', 'Normal', 'open', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)
	// Legacy open bead — NULL created_at.
	exec(`INSERT INTO beads (id, title, status, created_at, updated_at) VALUES ('b-null-ca', 'NullCA', 'open', NULL, NULL)`)
	// Closed bead — all timestamps present.
	exec(`INSERT INTO beads (id, title, status, created_at, updated_at, closed_at) VALUES ('b-closed', 'Closed', 'closed', '2026-02-01T00:00:00Z', '2026-02-02T00:00:00Z', '2026-02-02T12:00:00Z')`)
	// Closed bead — NULL closed_at (fall back to updated_at).
	exec(`INSERT INTO beads (id, title, status, created_at, updated_at, closed_at) VALUES ('b-null-clat', 'NullClAt', 'closed', '2026-03-01T00:00:00Z', '2026-03-02T00:00:00Z', NULL)`)
	// Worst-case closed bead — all timestamps NULL.
	exec(`INSERT INTO beads (id, title, status, created_at, updated_at, closed_at) VALUES ('b-all-null', 'AllNull', 'closed', NULL, NULL, NULL)`)

	if err := beadstore.BackfillJourneyEvents(ctx, db); err != nil {
		t.Fatalf("BackfillJourneyEvents: %v", err)
	}

	allIDs := []string{"b-normal", "b-null-ca", "b-closed", "b-null-clat", "b-all-null"}
	closedIDs := []string{"b-closed", "b-null-clat", "b-all-null"}
	openIDs := []string{"b-normal", "b-null-ca"}

	// Every bead must have exactly one 'imported' event with non-empty ts.
	for _, id := range allIDs {
		var ts string
		err := db.QueryRowContext(ctx,
			`SELECT ts FROM bead_journey WHERE bead_id=? AND event='imported' LIMIT 1`, id,
		).Scan(&ts)
		if err != nil {
			t.Errorf("bead %q: missing imported event: %v", id, err)
			continue
		}
		if ts == "" {
			t.Errorf("bead %q: imported event has empty ts", id)
		}
	}

	// Closed beads must have a 'closed' event with non-empty ts.
	for _, id := range closedIDs {
		var ts string
		err := db.QueryRowContext(ctx,
			`SELECT ts FROM bead_journey WHERE bead_id=? AND event='closed' LIMIT 1`, id,
		).Scan(&ts)
		if err != nil {
			t.Errorf("bead %q: missing closed event: %v", id, err)
			continue
		}
		if ts == "" {
			t.Errorf("bead %q: closed event has empty ts", id)
		}
	}

	// Open beads must NOT have a 'closed' event.
	for _, id := range openIDs {
		var n int
		if err := db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM bead_journey WHERE bead_id=? AND event='closed'`, id,
		).Scan(&n); err != nil {
			t.Fatalf("count closed events for %q: %v", id, err)
		}
		if n != 0 {
			t.Errorf("bead %q (open): expected 0 closed events, got %d", id, n)
		}
	}

	// ts NOT NULL constraint: no NULL ts values anywhere in bead_journey.
	var nullCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM bead_journey WHERE ts IS NULL`).Scan(&nullCount); err != nil {
		t.Fatalf("count NULL ts: %v", err)
	}
	if nullCount != 0 {
		t.Errorf("bead_journey has %d row(s) with NULL ts, want 0", nullCount)
	}

	// Idempotency: second call must not add duplicate events.
	if err := beadstore.BackfillJourneyEvents(ctx, db); err != nil {
		t.Fatalf("BackfillJourneyEvents (second call): %v", err)
	}
	for _, id := range allIDs {
		var n int
		if err := db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM bead_journey WHERE bead_id=? AND event='imported'`, id,
		).Scan(&n); err != nil {
			t.Fatalf("idempotency count for %q: %v", id, err)
		}
		if n != 1 {
			t.Errorf("bead %q: expected 1 imported event after second call, got %d", id, n)
		}
	}
}
