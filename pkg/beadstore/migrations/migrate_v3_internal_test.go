package migrations

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"oro/pkg/dbutil"
	"oro/pkg/protocol"
)

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
