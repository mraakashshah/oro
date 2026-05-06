//nolint:testpackage // Uses SQLiteStore internals (store.db) to seed fixtures via direct SQL.
package beadstore

import (
	"context"
	"testing"

	"oro/pkg/beadstore/migrations"
)

// TestReadyBlockedPartition is the §10.4 acceptance test for the amended
// beads_ready and beads_blocked views.  It seeds one child bead per parent
// state and asserts:
//
//   - alive-open:        parent exists, deleted=0, status='open'        → child blocked (not ready)
//   - alive-in_progress: parent exists, deleted=0, status='in_progress'  → child blocked (not ready)
//   - alive-closed:      parent exists, deleted=0, status='closed'       → child ready  (not blocked)
//   - deleted:           parent exists, deleted=1                        → child blocked (not ready)
//   - missing:           parent_id set but no row in beads               → child blocked (not ready)
//
// Partition invariant: no child bead appears in both views simultaneously.
func TestReadyBlockedPartition(t *testing.T) {
	ctx := context.Background()

	// newDB opens an in-memory store with both the v20 bead schema and v3
	// migration applied so beads_ready/beads_blocked include the
	// awaits_parent_close clause.
	store := newTestSQLiteStore(t)
	if err := migrations.MigrateToV3(ctx, store.db); err != nil {
		t.Fatalf("MigrateToV3: %v", err)
	}

	inView := func(view, beadID string) bool {
		t.Helper()
		var n int
		if err := store.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM `+view+` WHERE id=?`, beadID,
		).Scan(&n); err != nil {
			t.Fatalf("query %s for %q: %v", view, beadID, err)
		}
		return n > 0
	}

	// assertPartition verifies that childID appears in exactly the expected
	// views and never in both simultaneously.
	assertPartition := func(t *testing.T, childID string, wantReady, wantBlocked bool) {
		t.Helper()
		gotReady := inView("beads_ready", childID)
		gotBlocked := inView("beads_blocked", childID)

		if gotReady && gotBlocked {
			t.Errorf("%s: partition violated — bead appears in BOTH beads_ready and beads_blocked", childID)
		}
		if gotReady != wantReady {
			t.Errorf("%s: beads_ready = %v, want %v", childID, gotReady, wantReady)
		}
		if gotBlocked != wantBlocked {
			t.Errorf("%s: beads_blocked = %v, want %v", childID, gotBlocked, wantBlocked)
		}
	}

	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := store.db.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("exec %q args=%v: %v", q, args, err)
		}
	}

	t.Run("alive-open_parent", func(t *testing.T) {
		mustCreate(t, store, CreateParams{ID: "p-open", Title: "parent open"})
		mustCreate(t, store, CreateParams{
			ID:       "c-open",
			Title:    "child of open parent",
			ParentID: "p-open",
			Tags:     []string{"awaits_parent_close"},
		})
		// parent status='open', deleted=0 → child must be blocked, not ready
		assertPartition(t, "c-open", false, true)
	})

	t.Run("alive-in_progress_parent", func(t *testing.T) {
		mustCreate(t, store, CreateParams{ID: "p-ip", Title: "parent in_progress"})
		mustCreate(t, store, CreateParams{
			ID:       "c-ip",
			Title:    "child of in_progress parent",
			ParentID: "p-ip",
			Tags:     []string{"awaits_parent_close"},
		})
		status := "in_progress"
		mustUpdate(t, store, "p-ip", UpdateParams{Status: &status})
		// parent status='in_progress', deleted=0 → child must be blocked, not ready
		assertPartition(t, "c-ip", false, true)
	})

	t.Run("alive-closed_parent", func(t *testing.T) {
		mustCreate(t, store, CreateParams{ID: "p-closed", Title: "parent closed"})
		mustCreate(t, store, CreateParams{
			ID:       "c-closed",
			Title:    "child of closed parent",
			ParentID: "p-closed",
			Tags:     []string{"awaits_parent_close"},
		})
		mustClose(t, store, "p-closed", "done")
		// parent status='closed', deleted=0 → child must be ready, not blocked
		assertPartition(t, "c-closed", true, false)
	})

	t.Run("deleted_parent", func(t *testing.T) {
		mustCreate(t, store, CreateParams{ID: "p-del", Title: "parent to delete"})
		mustCreate(t, store, CreateParams{
			ID:       "c-del",
			Title:    "child of deleted parent",
			ParentID: "p-del",
			Tags:     []string{"awaits_parent_close"},
		})
		exec(`UPDATE beads SET deleted=1 WHERE id='p-del'`)
		// parent deleted=1 → child must be blocked, not ready
		assertPartition(t, "c-del", false, true)
	})

	t.Run("missing_parent", func(t *testing.T) {
		// Insert child directly to bypass Store validation; parent_id points to
		// a bead that has no row in the beads table.
		exec(`INSERT INTO beads (id, title, status, parent_id, priority, created_at, updated_at)
		      VALUES ('c-missing', 'child of missing parent', 'open', 'nonexistent-parent-id', 2, '2026-01-01', '2026-01-01')`)
		exec(`INSERT INTO bead_tags (bead_id, tag) VALUES ('c-missing', 'awaits_parent_close')`)
		// parent_id set but no row → child must be blocked, not ready
		assertPartition(t, "c-missing", false, true)
	})
}
