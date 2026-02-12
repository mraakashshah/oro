package codesearch

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // SQLite driver for database/sql
)

// CodeIndex is a SQLite-backed code search index.
type CodeIndex struct {
	db *sql.DB
}

// BuildStats reports what happened during an index build.
type BuildStats struct {
	FilesProcessed int
	ChunksIndexed  int
	Duration       time.Duration
}

// SearchResult pairs a chunk with its cosine similarity score.
type SearchResult struct {
	Chunk Chunk
	Score float64
}

// indexSchemaDDL creates the chunks table for the code index.
const indexSchemaDDL = `
CREATE TABLE IF NOT EXISTS chunks (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	file_path TEXT NOT NULL,
	name TEXT NOT NULL,
	kind TEXT NOT NULL,
	start_line INTEGER NOT NULL,
	end_line INTEGER NOT NULL,
	content TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_chunks_file_path ON chunks(file_path);
`

// NewCodeIndex opens or creates a code index at the given database path.
func NewCodeIndex(dbPath string) (*CodeIndex, error) {
	// Ensure parent directory exists.
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o750); err != nil {
		return nil, fmt.Errorf("create index dir: %w", err)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open code index db: %w", err)
	}

	ctx := context.Background()

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping code index db: %w", err)
	}

	if _, err := db.ExecContext(ctx, "PRAGMA journal_mode=WAL"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set WAL on code index: %w", err)
	}

	if _, err := db.ExecContext(ctx, "PRAGMA busy_timeout=5000"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set busy_timeout on code index: %w", err)
	}

	if _, err := db.ExecContext(ctx, indexSchemaDDL); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("apply code index schema: %w", err)
	}

	return &CodeIndex{db: db}, nil
}

// Close closes the underlying database connection.
func (ci *CodeIndex) Close() error {
	if err := ci.db.Close(); err != nil {
		return fmt.Errorf("close code index db: %w", err)
	}
	return nil
}

// Build walks rootDir, chunks all Go files, embeds them, and stores in SQLite.
// This is a full rebuild: existing data is cleared first.
func (ci *CodeIndex) Build(ctx context.Context, rootDir string) (BuildStats, error) {
	start := time.Now()

	// Clear existing data for full rebuild.
	if _, err := ci.db.ExecContext(ctx, "DELETE FROM chunks"); err != nil {
		return BuildStats{}, fmt.Errorf("clear chunks: %w", err)
	}

	var stats BuildStats

	err := filepath.Walk(rootDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("walk error at %s: %w", path, walkErr)
		}

		if info.IsDir() {
			return ci.shouldSkipDir(path, rootDir)
		}

		return ci.indexFile(ctx, path, rootDir, &stats)
	})
	if err != nil {
		return stats, fmt.Errorf("walk %s: %w", rootDir, err)
	}

	stats.Duration = time.Since(start)
	return stats, nil
}

// shouldSkipDir returns filepath.SkipDir for directories that should not be indexed.
func (ci *CodeIndex) shouldSkipDir(path, rootDir string) error {
	base := filepath.Base(path)
	if strings.HasPrefix(base, ".") && path != rootDir {
		return filepath.SkipDir
	}
	if base == "vendor" || base == "node_modules" || base == "testdata" {
		return filepath.SkipDir
	}
	return nil
}

// indexFile processes a single file: reads, chunks, embeds, and stores.
func (ci *CodeIndex) indexFile(ctx context.Context, path, rootDir string, stats *BuildStats) error {
	// Only process Go files for now.
	if !strings.HasSuffix(path, ".go") {
		return nil
	}

	// Skip test files.
	if strings.HasSuffix(path, "_test.go") {
		return nil
	}

	src, err := os.ReadFile(path) //nolint:gosec // path comes from filepath.Walk
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}

	relPath, err := filepath.Rel(rootDir, path)
	if err != nil {
		relPath = path
	}

	chunks, err := ChunkGoSource(relPath, string(src))
	if err != nil {
		// Skip files that fail to parse (e.g., generated code).
		return nil //nolint:nilerr // intentional skip
	}

	stats.FilesProcessed++

	for _, chunk := range chunks {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("context cancelled: %w", err)
		}

		if err := ci.insertChunk(ctx, chunk); err != nil {
			return fmt.Errorf("insert chunk %s/%s: %w", chunk.FilePath, chunk.Name, err)
		}

		stats.ChunksIndexed++
	}

	return nil
}

// insertChunk stores a single chunk in the database.
func (ci *CodeIndex) insertChunk(ctx context.Context, chunk Chunk) error {
	_, err := ci.db.ExecContext(ctx,
		`INSERT INTO chunks (file_path, name, kind, start_line, end_line, content, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		chunk.FilePath, chunk.Name, string(chunk.Kind),
		chunk.StartLine, chunk.EndLine, chunk.Content,
		time.Now().UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("exec insert chunk: %w", err)
	}
	return nil
}

// Search is a placeholder stub. Real search will be implemented in a later bead.
func (ci *CodeIndex) Search(ctx context.Context, query string, topK int) ([]SearchResult, error) {
	// Return empty results. This will be replaced with FTS5-based search.
	return []SearchResult{}, nil
}
