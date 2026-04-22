package memory //nolint:testpackage // shared semantic-memory test helpers

import (
	"database/sql"
	"testing"

	"oro/pkg/dbutil"
	"oro/pkg/protocol"
)

func setupSemanticProductionDB(t *testing.T) *sql.DB {
	t.Helper()

	db, err := dbutil.OpenDB(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(protocol.SchemaDDL); err != nil {
		t.Fatalf("exec schema: %v", err)
	}
	if _, err := db.Exec(protocol.MigrateSemanticMemoryDense); err != nil {
		t.Fatalf("exec dense migration: %v", err)
	}
	if _, err := db.Exec(protocol.MigrateSemanticMemoryBackfillState); err != nil {
		t.Fatalf("exec backfill-state migration: %v", err)
	}
	if _, err := db.Exec(protocol.MigrateSemanticMemoryChunks); err != nil {
		t.Fatalf("exec chunk migration: %v", err)
	}
	return db
}
