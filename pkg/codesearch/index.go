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

-- FTS5 full-text index over chunks for BM25-ranked search
CREATE VIRTUAL TABLE IF NOT EXISTS chunks_fts USING fts5(
	content,
	content=chunks,
	content_rowid=id
);

-- Triggers to keep FTS index in sync with chunks table
CREATE TRIGGER IF NOT EXISTS chunks_ai AFTER INSERT ON chunks BEGIN
	INSERT INTO chunks_fts(rowid, content) VALUES (new.id, new.content);
END;

CREATE TRIGGER IF NOT EXISTS chunks_ad AFTER DELETE ON chunks BEGIN
	INSERT INTO chunks_fts(chunks_fts, rowid, content) VALUES ('delete', old.id, old.content);
END;

CREATE TRIGGER IF NOT EXISTS chunks_au AFTER UPDATE ON chunks BEGIN
	INSERT INTO chunks_fts(chunks_fts, rowid, content) VALUES ('delete', old.id, old.content);
	INSERT INTO chunks_fts(rowid, content) VALUES (new.id, new.content);
END;
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

	// Rebuild FTS5 index (triggers don't fire on external content tables).
	if _, err := ci.db.ExecContext(ctx, "INSERT INTO chunks_fts(chunks_fts) VALUES('rebuild')"); err != nil {
		return BuildStats{}, fmt.Errorf("rebuild fts5 index: %w", err)
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

// FTS5Search performs full-text search over indexed chunks using SQLite FTS5.
// Returns chunks ranked by BM25 relevance. Empty query returns nil, nil.
//
//oro:testonly
func (ci *CodeIndex) FTS5Search(ctx context.Context, query string, limit int) ([]Chunk, error) {
	// Empty query: return empty results per acceptance criteria.
	if query == "" {
		return nil, nil
	}

	// Sanitize query to prevent FTS5 operator interpretation.
	sanitized := sanitizeFTS5Query(query)

	// Query FTS5 virtual table and join with chunks table for full data.
	q := `
		SELECT c.id, c.file_path, c.name, c.kind, c.start_line, c.end_line, c.content
		FROM chunks_fts
		JOIN chunks c ON chunks_fts.rowid = c.id
		WHERE chunks_fts MATCH ?
		ORDER BY rank
		LIMIT ?
	`

	rows, err := ci.db.QueryContext(ctx, q, sanitized, limit)
	if err != nil {
		return nil, fmt.Errorf("fts5 search query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var results []Chunk
	for rows.Next() {
		var c Chunk
		var id int64
		var kind string
		if err := rows.Scan(&id, &c.FilePath, &c.Name, &kind, &c.StartLine, &c.EndLine, &c.Content); err != nil {
			return nil, fmt.Errorf("scan chunk: %w", err)
		}
		c.Kind = ChunkKind(kind)
		results = append(results, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("fts5 search rows: %w", err)
	}

	return results, nil
}

// sanitizeFTS5Query wraps each term in double quotes to prevent FTS5 operator
// interpretation (e.g., "and", "or", "not" are FTS5 operators) and joins them
// with OR for broader recall. Pattern from pkg/memory/memory.go.
func sanitizeFTS5Query(query string) string {
	words := strings.Fields(query)
	if len(words) == 0 {
		return query
	}
	quoted := make([]string, 0, len(words))
	for _, w := range words {
		// Strip non-alphanumeric characters that break FTS5 quoting
		clean := strings.Map(func(r rune) rune {
			if r == '"' {
				return -1
			}
			return r
		}, w)
		if clean != "" {
			quoted = append(quoted, `"`+clean+`"`)
		}
	}
	return strings.Join(quoted, " OR ")
}
