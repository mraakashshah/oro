package migrations

import (
	"context"
	"database/sql"
	"reflect"
	"strings"
	"testing"

	"oro/pkg/dbutil"
	"oro/pkg/protocol"
)

func TestMigrateToV3RollsBackViewRefreshOnLaterDDLFailure(t *testing.T) {
	ctx := context.Background()
	db, err := dbutil.OpenDB(":memory:")
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.ExecContext(ctx, protocol.SchemaDDL); err != nil {
		t.Fatalf("apply state schema DDL: %v", err)
	}
	if err := protocol.MigrateBeadSchema(ctx, db); err != nil {
		t.Fatalf("apply bead schema: %v", err)
	}

	const legacyViewsDDL = `
DROP VIEW beads_ready;
DROP VIEW beads_blocked;
DROP VIEW review_checkpoints_blocking_assignment;
CREATE VIEW review_checkpoints_blocking_assignment AS
SELECT id, bead_id FROM review_checkpoints WHERE state = 'review_running';
CREATE VIEW beads_ready AS
SELECT b.* FROM beads b WHERE b.id LIKE 'legacy-ready-%';
CREATE VIEW beads_blocked AS
SELECT b.* FROM beads b WHERE b.id LIKE 'legacy-blocked-%';
`
	if _, err := db.ExecContext(ctx, legacyViewsDDL); err != nil {
		t.Fatalf("install legacy views: %v", err)
	}

	before := viewDefinitions(ctx, t, db)
	lateFailureDDL := protocol.BeadQueueViewsDDL + `
CREATE VIEW beads_ready AS SELECT b.* FROM beads b;
`
	err = migrateToV3WithViewsDDL(ctx, db, lateFailureDDL)
	if err == nil {
		t.Fatal("migrate with late duplicate view creation: got nil error, want failure")
	}

	after := viewDefinitions(ctx, t, db)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("view definitions changed after failed migration\nbefore: %#v\n after: %#v", before, after)
	}
}

func viewDefinitions(ctx context.Context, t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	rows, err := db.QueryContext(ctx, `
SELECT name, sql
FROM sqlite_schema
WHERE type = 'view'
  AND name IN ('beads_ready', 'beads_blocked', 'review_checkpoints_blocking_assignment')
ORDER BY name`)
	if err != nil {
		t.Fatalf("query view definitions: %v", err)
	}
	defer rows.Close()

	definitions := make(map[string]string)
	for rows.Next() {
		var name, definition string
		if err := rows.Scan(&name, &definition); err != nil {
			t.Fatalf("scan view definition: %v", err)
		}
		definitions[name] = definition
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate view definitions: %v", err)
	}
	return definitions
}

func TestV3ViewsDDLHandlesConcurrentBlockedViewRecreate(t *testing.T) {
	ctx := context.Background()
	db, err := dbutil.OpenDB(":memory:")
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.ExecContext(ctx, protocol.SchemaDDL); err != nil {
		t.Fatalf("apply state schema DDL: %v", err)
	}
	if err := protocol.MigrateBeadSchema(ctx, db); err != nil {
		t.Fatalf("apply bead schema: %v", err)
	}

	for _, stmt := range splitSQLStatements(protocol.BeadQueueViewsDDL) {
		if isCreateBlockedViewStatement(stmt) {
			createConcurrentBlockedView(ctx, t, db)
		}
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("exec v3 view statement %q: %v", stmt, err)
		}
	}
}

func isCreateBlockedViewStatement(stmt string) bool {
	normalized := strings.ToUpper(stmt)
	return strings.HasPrefix(normalized, "CREATE VIEW") &&
		strings.Contains(normalized, "BEADS_BLOCKED AS")
}

func createConcurrentBlockedView(ctx context.Context, t *testing.T, db *sql.DB) {
	t.Helper()
	_, err := db.ExecContext(ctx, `CREATE VIEW beads_blocked AS SELECT b.* FROM beads b WHERE 0`)
	if err != nil {
		t.Fatalf("create concurrent beads_blocked view: %v", err)
	}
}

func splitSQLStatements(sqlText string) []string {
	parts := strings.Split(sqlText, ";")
	stmts := make([]string, 0, len(parts))
	for _, part := range parts {
		stmt := strings.TrimSpace(part)
		if stmt != "" {
			stmts = append(stmts, stmt)
		}
	}
	return stmts
}
