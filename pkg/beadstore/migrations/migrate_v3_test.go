package migrations_test

import (
	"context"
	"database/sql"
	"testing"

	"oro/pkg/beadstore/migrations"
	"oro/pkg/dbutil"
	"oro/pkg/protocol"
)

// openV20DB opens an in-memory SQLite DB with the full v20 bead schema applied,
// representing the state before the v3 migration runs.
func openV20DB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := dbutil.OpenDB(":memory:")
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, protocol.SchemaDDL); err != nil {
		t.Fatalf("apply state schema DDL: %v", err)
	}
	if err := protocol.MigrateBeadSchema(ctx, db); err != nil {
		t.Fatalf("apply v20 bead schema: %v", err)
	}
	return db
}

// TestMigrateToV3Idempotent verifies that MigrateToV3:
//   - adds the 10 new ALTER columns to the beads table
//   - creates the 4 new tables and their indexes
//   - is safe to run twice (idempotent, no-op on second call)
//   - leaves all v20 acceptance tests passing (columns/tables present)
func TestMigrateToV3Idempotent(t *testing.T) {
	ctx := context.Background()
	db := openV20DB(t)

	// First application must succeed and add all schema elements.
	if err := migrations.MigrateToV3(ctx, db); err != nil {
		t.Fatalf("MigrateToV3 first call: %v", err)
	}

	// Assert all 10 ALTER TABLE ADD COLUMN additions are present.
	wantCols := []string{
		// §4.6.a
		"next_action", "blockers", "linked_artifacts", "worker_state",
		// §4.6.b
		"gate_state", "premortem_cycle_count", "pipeline_stage",
		"sandbox_session", "allowed_external_fns", "context_thresholds",
	}
	for _, col := range wantCols {
		assertBeadsColumn(t, db, col)
	}

	// Assert all 4 new tables exist.
	wantTables := []string{
		"bead_journey",
		"bead_learnings_pending",
		"cards",
		"card_events",
	}
	for _, tbl := range wantTables {
		assertTableExists(t, db, tbl)
	}

	// Assert key indexes were created.
	wantIndexes := []string{
		"idx_journey_bead_ts",
		"idx_journey_ts",
		"idx_learnings_bead",
		"idx_learnings_pending",
		"idx_cards_type_score",
		"idx_card_events_card_ts",
	}
	for _, idx := range wantIndexes {
		assertIndexExists(t, db, idx)
	}

	// Second application must be a no-op (idempotent).
	if err := migrations.MigrateToV3(ctx, db); err != nil {
		t.Fatalf("MigrateToV3 second call (idempotency): %v", err)
	}

	// After second call, all columns and tables must still be present.
	for _, col := range wantCols {
		assertBeadsColumn(t, db, col)
	}
	for _, tbl := range wantTables {
		assertTableExists(t, db, tbl)
	}
}

func assertBeadsColumn(t *testing.T, db *sql.DB, col string) {
	t.Helper()
	var name string
	err := db.QueryRowContext(context.Background(), "SELECT name FROM pragma_table_info('beads') WHERE name=?", col).Scan(&name)
	if err != nil {
		t.Errorf("beads table missing column %q: %v", col, err)
	}
}

func assertTableExists(t *testing.T, db *sql.DB, tbl string) {
	t.Helper()
	var name string
	err := db.QueryRowContext(context.Background(), "SELECT name FROM sqlite_schema WHERE type='table' AND name=?", tbl).Scan(&name)
	if err != nil {
		t.Errorf("table %q not found: %v", tbl, err)
	}
}

func assertIndexExists(t *testing.T, db *sql.DB, idx string) {
	t.Helper()
	var name string
	err := db.QueryRowContext(context.Background(), "SELECT name FROM sqlite_schema WHERE type='index' AND name=?", idx).Scan(&name)
	if err != nil {
		t.Errorf("index %q not found: %v", idx, err)
	}
}
