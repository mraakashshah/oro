package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	"oro/pkg/memory"
	"oro/pkg/protocol"

	_ "modernc.org/sqlite"
)

// defaultMemoryStore opens (or creates) the default SQLite memory store at
// ~/.oro/memories.db and ensures the schema is applied.
func defaultMemoryStore() (*memory.Store, error) {
	dbPath := os.Getenv("ORO_MEMORY_DB")
	if dbPath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("resolve home dir: %w", err)
		}
		dir := filepath.Join(home, ".oro")
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, fmt.Errorf("create .oro dir: %w", err)
		}
		dbPath = filepath.Join(dir, "memories.db")
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open memory db: %w", err)
	}

	if _, err := db.ExecContext(context.Background(), protocol.SchemaDDL); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}

	return memory.NewStore(db), nil
}
