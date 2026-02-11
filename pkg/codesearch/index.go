package codesearch

import (
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite" // SQLite driver for database/sql
)

// CodeIndex is a SQLite-backed vector store for semantic code search.
type CodeIndex struct {
	db  *sql.DB
	emb Embedder
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
	embedding BLOB NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_chunks_file_path ON chunks(file_path);
`

// NewCodeIndex opens or creates a code index at the given database path.
func NewCodeIndex(dbPath string, emb Embedder) (*CodeIndex, error) {
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

	return &CodeIndex{db: db, emb: emb}, nil
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

		vec, err := ci.emb.Embed(ctx, chunk.Content)
		if err != nil {
			return fmt.Errorf("embed chunk %s/%s: %w", chunk.FilePath, chunk.Name, err)
		}

		if err := ci.insertChunk(ctx, chunk, vec); err != nil {
			return fmt.Errorf("insert chunk %s/%s: %w", chunk.FilePath, chunk.Name, err)
		}

		stats.ChunksIndexed++
	}

	return nil
}

// insertChunk stores a single chunk with its embedding in the database.
func (ci *CodeIndex) insertChunk(ctx context.Context, chunk Chunk, embedding []float64) error {
	_, err := ci.db.ExecContext(ctx,
		`INSERT INTO chunks (file_path, name, kind, start_line, end_line, content, embedding, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		chunk.FilePath, chunk.Name, string(chunk.Kind),
		chunk.StartLine, chunk.EndLine, chunk.Content,
		EncodeEmbedding(embedding), time.Now().UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("exec insert chunk: %w", err)
	}
	return nil
}

// Search embeds the query and returns the top-K most similar chunks by cosine similarity.
func (ci *CodeIndex) Search(ctx context.Context, query string, topK int) ([]SearchResult, error) {
	queryVec, err := ci.emb.Embed(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}

	rows, err := ci.db.QueryContext(ctx,
		`SELECT file_path, name, kind, start_line, end_line, content, embedding FROM chunks`)
	if err != nil {
		return nil, fmt.Errorf("query chunks: %w", err)
	}
	defer rows.Close()

	var results []SearchResult

	for rows.Next() {
		var (
			filePath     string
			name         string
			kind         string
			startLine    int
			endLine      int
			content      string
			embeddingBuf []byte
		)

		if err := rows.Scan(&filePath, &name, &kind, &startLine, &endLine, &content, &embeddingBuf); err != nil {
			return nil, fmt.Errorf("scan chunk row: %w", err)
		}

		chunkVec := DecodeEmbedding(embeddingBuf)
		score := CosineSimilarity(queryVec, chunkVec)

		results = append(results, SearchResult{
			Chunk: Chunk{
				FilePath:  filePath,
				Name:      name,
				Kind:      ChunkKind(kind),
				StartLine: startLine,
				EndLine:   endLine,
				Content:   content,
			},
			Score: score,
		})
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate chunk rows: %w", err)
	}

	// Sort by score descending.
	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})

	// Limit to topK.
	if len(results) > topK {
		results = results[:topK]
	}

	return results, nil
}

// EncodeEmbedding serializes a float64 slice to a binary blob (little-endian).
func EncodeEmbedding(vec []float64) []byte {
	buf := make([]byte, len(vec)*8)
	for i, v := range vec {
		binary.LittleEndian.PutUint64(buf[i*8:], math.Float64bits(v))
	}
	return buf
}

// DecodeEmbedding deserializes a binary blob back to a float64 slice.
func DecodeEmbedding(buf []byte) []float64 {
	n := len(buf) / 8
	vec := make([]float64, n)
	for i := range n {
		vec[i] = math.Float64frombits(binary.LittleEndian.Uint64(buf[i*8:]))
	}
	return vec
}

// CosineSimilarity computes the cosine similarity between two vectors.
// Returns 0 for zero-length or zero-magnitude vectors.
func CosineSimilarity(a, b []float64) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}

	n := len(a)
	if len(b) < n {
		n = len(b)
	}

	var dot, normA, normB float64
	for i := range n {
		dot += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}

	denom := math.Sqrt(normA) * math.Sqrt(normB)
	if denom == 0 {
		return 0
	}

	return dot / denom
}
