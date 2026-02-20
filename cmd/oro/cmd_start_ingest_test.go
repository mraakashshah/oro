package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"oro/pkg/memory"
	"oro/pkg/protocol"

	_ "modernc.org/sqlite"
)

// newTestDBWithSchema creates a file-based SQLite DB with the full Oro schema.
func newTestDBWithSchema(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(protocol.SchemaDDL); err != nil {
		t.Fatalf("exec schema: %v", err)
	}
	return db
}

func TestIngestKnowledgeOnStartup(t *testing.T) {
	t.Run("no_file_returns_silently", func(t *testing.T) {
		// Point ORO_KNOWLEDGE_FILE to a non-existent path so resolveKnowledgeFile
		// returns an error and ingestKnowledgeOnStartup returns silently.
		t.Setenv("ORO_KNOWLEDGE_FILE", filepath.Join(t.TempDir(), "no-such.jsonl"))

		db := newTestDBWithSchema(t)
		ingestKnowledgeOnStartup(db) // must not panic

		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM memories").Scan(&count); err != nil {
			t.Fatalf("count memories: %v", err)
		}
		if count != 0 {
			t.Errorf("expected 0 memories after no-file path, got %d", count)
		}
	})

	t.Run("valid_file_ingests_entries", func(t *testing.T) {
		tmpDir := t.TempDir()
		knowledgeFile := filepath.Join(tmpDir, "knowledge.jsonl")
		content := `{"key":"k1","type":"lesson","content":"Write tests first","bead":"test","tags":["tdd"],"ts":"2024-01-15T10:00:00Z"}
{"key":"k2","type":"decision","content":"Use early returns","bead":"test","tags":["go"],"ts":"2024-01-15T11:00:00Z"}
`
		if err := os.WriteFile(knowledgeFile, []byte(content), 0o600); err != nil {
			t.Fatalf("write knowledge file: %v", err)
		}
		t.Setenv("ORO_KNOWLEDGE_FILE", knowledgeFile)

		db := newTestDBWithSchema(t)
		ingestKnowledgeOnStartup(db)

		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM memories").Scan(&count); err != nil {
			t.Fatalf("count memories: %v", err)
		}
		if count != 2 {
			t.Errorf("expected 2 memories after valid ingest, got %d", count)
		}

		// Spot-check one entry via store search.
		store := memory.NewStore(db)
		ctx := context.Background()
		results, err := store.Search(ctx, "Write tests first", memory.SearchOpts{Limit: 5})
		if err != nil {
			t.Fatalf("search: %v", err)
		}
		if len(results) == 0 {
			t.Error("expected to find ingested entry 'Write tests first'")
		}
	})

	t.Run("malformed_json_does_not_panic", func(t *testing.T) {
		tmpDir := t.TempDir()
		knowledgeFile := filepath.Join(tmpDir, "mixed.jsonl")
		// Mix of malformed and one valid line.
		content := `not valid json at all
{"key":"good","type":"lesson","content":"Still works","bead":"test","tags":[],"ts":"2024-01-15T10:00:00Z"}
{broken json}
`
		if err := os.WriteFile(knowledgeFile, []byte(content), 0o600); err != nil {
			t.Fatalf("write knowledge file: %v", err)
		}
		t.Setenv("ORO_KNOWLEDGE_FILE", knowledgeFile)

		db := newTestDBWithSchema(t)
		ingestKnowledgeOnStartup(db) // must not panic

		// The single valid entry should have been ingested.
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM memories").Scan(&count); err != nil {
			t.Fatalf("count memories: %v", err)
		}
		if count != 1 {
			t.Errorf("expected 1 memory from mixed file, got %d", count)
		}
	})
}
