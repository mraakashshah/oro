package migrations_test

import (
	"context"
	"database/sql"
	"strings"
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

func TestMigrateToV3TreatsVersionFourAsAlreadyMigrated(t *testing.T) {
	ctx := context.Background()
	db, err := dbutil.OpenDB(":memory:")
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.ExecContext(ctx, `PRAGMA user_version = 4`); err != nil {
		t.Fatalf("set user_version: %v", err)
	}

	if err := migrations.MigrateToV3(ctx, db); err != nil {
		t.Fatalf("MigrateToV3 at newer schema version: %v", err)
	}
	var beadsTables int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_schema WHERE type='table' AND name='beads'`,
	).Scan(&beadsTables); err != nil {
		t.Fatalf("inspect schema after no-op migration: %v", err)
	}
	if beadsTables != 0 {
		t.Fatalf("beads table count = %d, want no schema mutation at version 4", beadsTables)
	}
}

// TestMigrateToV3CheckConstraints verifies that the three spec-mandated CHECK
// constraints are actually enforced by SQLite — not just that the columns are
// present. The previous review (oro-vye0 attempt #0) caught this gap because
// pragma_table_info reports column names but not CHECK clauses.
//
// References: spec §4.6.b lines 539-540 (gate_state), 546-547 (pipeline_stage),
// and §5.3 line 747 (cards.type).
func TestMigrateToV3CheckConstraints(t *testing.T) {
	ctx := context.Background()
	db := openV20DB(t)
	if err := migrations.MigrateToV3(ctx, db); err != nil {
		t.Fatalf("MigrateToV3: %v", err)
	}

	// gate_state: only the five enum values are allowed.
	insertBead := func(id, gateState string) error {
		_, err := db.ExecContext(ctx,
			`INSERT INTO beads (id, title, status, gate_state) VALUES (?, ?, 'open', ?)`,
			id, "t-"+id, gateState)
		return err
	}
	for _, ok := range []string{"none", "eligible", "satisfied", "blocked", "replan"} {
		if err := insertBead("gs-ok-"+ok, ok); err != nil {
			t.Errorf("gate_state=%q: expected accept, got error %v", ok, err)
		}
	}
	if err := insertBead("gs-bogus", "bogus"); err == nil {
		t.Errorf("gate_state='bogus': expected CHECK constraint failure, got nil")
	} else if !strings.Contains(err.Error(), "CHECK") && !strings.Contains(err.Error(), "constraint") {
		t.Errorf("gate_state='bogus': expected CHECK constraint error, got %v", err)
	}

	// pipeline_stage: only the eight enum values are allowed.
	insertWithStage := func(id, stage string) error {
		_, err := db.ExecContext(ctx,
			`INSERT INTO beads (id, title, status, pipeline_stage) VALUES (?, ?, 'open', ?)`,
			id, "t-"+id, stage)
		return err
	}
	for _, ok := range []string{"assess", "plan", "premortem", "prepare", "execute", "validate", "evolve", "none"} {
		if err := insertWithStage("ps-ok-"+ok, ok); err != nil {
			t.Errorf("pipeline_stage=%q: expected accept, got error %v", ok, err)
		}
	}
	if err := insertWithStage("ps-bogus", "shipping"); err == nil {
		t.Errorf("pipeline_stage='shipping': expected CHECK constraint failure, got nil")
	} else if !strings.Contains(err.Error(), "CHECK") && !strings.Contains(err.Error(), "constraint") {
		t.Errorf("pipeline_stage='shipping': expected CHECK constraint error, got %v", err)
	}
	// pipeline_stage column also accepts NULL (closed beads, per §4.6.f.3).
	if _, err := db.ExecContext(ctx,
		`INSERT INTO beads (id, title, status) VALUES ('ps-null', 't', 'closed')`); err != nil {
		t.Errorf("pipeline_stage NULL: expected accept (column is nullable), got %v", err)
	}

	// cards.type: only the five enum values are allowed.
	insertCard := func(id, cardType string) error {
		_, err := db.ExecContext(ctx, `
			INSERT INTO cards (id, type, title, body_summary, body_full,
			                   decay_anchor, created_at, updated_at)
			VALUES (?, ?, 't', 's', 'f', '2026-01-01', '2026-01-01', '2026-01-01')`,
			id, cardType)
		return err
	}
	for _, ok := range []string{"rule", "taste", "pattern", "decision", "fact"} {
		if err := insertCard("ct-ok-"+ok, ok); err != nil {
			t.Errorf("cards.type=%q: expected accept, got error %v", ok, err)
		}
	}
	if err := insertCard("ct-bogus", "not-a-type"); err == nil {
		t.Errorf("cards.type='not-a-type': expected CHECK constraint failure, got nil")
	} else if !strings.Contains(err.Error(), "CHECK") && !strings.Contains(err.Error(), "constraint") {
		t.Errorf("cards.type='not-a-type': expected CHECK constraint error, got %v", err)
	}
}

// TestMigrateToV3ViewsContainAwaitsParentClose reads the rewritten view
// definitions from sqlite_schema and asserts that the awaits_parent_close
// clause (§10.4) was preserved in both views.
func TestMigrateToV3ViewsContainAwaitsParentClose(t *testing.T) {
	ctx := context.Background()
	db := openV20DB(t)
	if err := migrations.MigrateToV3(ctx, db); err != nil {
		t.Fatalf("MigrateToV3: %v", err)
	}

	for _, name := range []string{"beads_ready", "beads_blocked"} {
		var sqlText string
		err := db.QueryRowContext(ctx,
			`SELECT sql FROM sqlite_schema WHERE type='view' AND name=?`, name,
		).Scan(&sqlText)
		if err != nil {
			t.Fatalf("read view %q: %v", name, err)
		}
		if !strings.Contains(sqlText, "awaits_parent_close") {
			t.Errorf("view %q missing awaits_parent_close clause; sql=%s", name, sqlText)
		}
		// Both views must also still join through bead_tags for the awaits clause.
		if !strings.Contains(sqlText, "bead_tags") {
			t.Errorf("view %q missing bead_tags reference; sql=%s", name, sqlText)
		}
	}
}

// TestMigrateToV3PreservesV20ReadyBlockedSemantics seeds a representative set
// of beads exercising the v20 ready/blocked classification rules (open vs.
// blocked status, deferred_until, blocking deps, active assignments) and
// asserts that the rewritten v3 views return the same membership for cases
// that don't involve the new awaits_parent_close clause. This is the AC's
// "v20 acceptance still passes" check, made behavioral.
func TestMigrateToV3PreservesV20ReadyBlockedSemantics(t *testing.T) {
	ctx := context.Background()
	db := openV20DB(t)
	if err := migrations.MigrateToV3(ctx, db); err != nil {
		t.Fatalf("MigrateToV3: %v", err)
	}

	exec := func(stmt string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, stmt, args...); err != nil {
			t.Fatalf("exec %q: %v", stmt, err)
		}
	}

	// Seed beads:
	//   ready-1: open, no deps, no assignment   -> ready
	//   ready-2: open, future-dated deferred=NULL, no deps         -> ready
	//   def-1:   open, deferred_until in future                    -> not ready, blocked
	//   blocked: status=blocked                                    -> not ready, blocked
	//   active:  open, has active assignment                       -> not ready, not blocked
	//   blocker: closed parent dep                                 -> see below
	//   dep-on-open: open, blocks-dep on a still-open parent       -> not ready, blocked
	//   dep-on-closed: open, blocks-dep on a closed parent         -> ready
	exec(`INSERT INTO beads (id, title, status) VALUES ('ready-1', 't', 'open')`)
	exec(`INSERT INTO beads (id, title, status) VALUES ('ready-2', 't', 'open')`)
	exec(`INSERT INTO beads (id, title, status, deferred_until) VALUES ('def-1', 't', 'open', '2099-01-01')`)
	exec(`INSERT INTO beads (id, title, status) VALUES ('blocked', 't', 'blocked')`)
	exec(`INSERT INTO beads (id, title, status) VALUES ('active', 't', 'open')`)
	exec(`INSERT INTO assignments (bead_id, worker_id, worktree, status) VALUES ('active', 'w', 'wt', 'active')`)

	exec(`INSERT INTO beads (id, title, status) VALUES ('parent-open', 't', 'open')`)
	exec(`INSERT INTO beads (id, title, status, closed_at) VALUES ('parent-closed', 't', 'closed', '2026-01-01')`)
	exec(`INSERT INTO beads (id, title, status) VALUES ('dep-on-open', 't', 'open')`)
	exec(`INSERT INTO beads (id, title, status) VALUES ('dep-on-closed', 't', 'open')`)
	exec(`INSERT INTO bead_deps (bead_id, depends_on_id, type) VALUES ('dep-on-open', 'parent-open', 'blocks')`)
	exec(`INSERT INTO bead_deps (bead_id, depends_on_id, type) VALUES ('dep-on-closed', 'parent-closed', 'blocks')`)

	// Ready: open, not deferred, no active assignment, no open blockers,
	// no awaits_parent_close tag.
	wantReady := map[string]bool{
		"ready-1":       true,
		"ready-2":       true,
		"dep-on-closed": true,
		"parent-open":   true,
	}
	// Blocked: status=blocked, or has open blocker dependency. Future-deferred
	// beads are NOT in beads_blocked under v20 — they're in their own
	// "deferred" bucket (neither ready nor blocked). The migration must
	// preserve that.
	wantBlocked := map[string]bool{
		"blocked":     true,
		"dep-on-open": true,
	}
	// Neither ready nor blocked under v20.
	wantNeitherReadyNorBlocked := []string{"def-1", "active"}

	gotReady := collectIDs(ctx, t, db, `SELECT id FROM beads_ready`)
	for id := range wantReady {
		if !gotReady[id] {
			t.Errorf("beads_ready: expected %q present, got %v", id, sortedKeys(gotReady))
		}
	}
	for _, id := range []string{"def-1", "blocked", "active", "dep-on-open"} {
		if gotReady[id] {
			t.Errorf("beads_ready: expected %q absent, but it was returned", id)
		}
	}

	gotBlocked := collectIDs(ctx, t, db, `SELECT id FROM beads_blocked`)
	for id := range wantBlocked {
		if !gotBlocked[id] {
			t.Errorf("beads_blocked: expected %q present, got %v", id, sortedKeys(gotBlocked))
		}
	}
	if gotBlocked["dep-on-closed"] {
		t.Errorf("beads_blocked: 'dep-on-closed' (parent closed, no blocker) must be excluded")
	}
	for _, id := range wantNeitherReadyNorBlocked {
		if gotReady[id] {
			t.Errorf("classification: %q must not be in beads_ready", id)
		}
		if gotBlocked[id] {
			t.Errorf("classification: %q must not be in beads_blocked", id)
		}
	}
}

func collectIDs(ctx context.Context, t *testing.T, db *sql.DB, query string) map[string]bool {
	t.Helper()
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	defer rows.Close()
	ids := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan: %v", err)
		}
		ids[id] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	return ids
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
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
