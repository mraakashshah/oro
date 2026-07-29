package codesearch

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"oro/pkg/protocol"

	_ "modernc.org/sqlite" // SQLite driver for database/sql
)

// CodeIndex is a SQLite-backed code search index.
type CodeIndex struct {
	db       *sql.DB
	reranker *Reranker
	embedder Embedder
}

// dbExecer abstracts database operations to support both *sql.DB and *sql.Tx.
type dbExecer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// BuildStats reports what happened during an index build.
type BuildStats struct {
	FilesProcessed int
	ChunksIndexed  int
	Duration       time.Duration
}

// SearchResult pairs a chunk with its relevance score and optional rerank reason.
type SearchResult struct {
	Chunk  Chunk
	Score  float64
	Reason string
}

// Result is the public result shape returned by routed code search handlers.
type Result = SearchResult

// Embedder computes dense embedding vectors for semantic code search.
type Embedder interface {
	Embed(text string) []float32
	Dim() int
	Name() string
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
	embedding BLOB,
	embedding_model TEXT,
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
// By default Search uses FTS5-only results. Call SetReranker to enable
// FTS5 pre-filter + Claude reranking.
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
	if err := ensureIndexColumn(ctx, db, "chunks", "embedding", "ALTER TABLE chunks ADD COLUMN embedding BLOB"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ensure chunk embedding: %w", err)
	}
	if err := ensureIndexColumn(ctx, db, "chunks", "embedding_model", "ALTER TABLE chunks ADD COLUMN embedding_model TEXT"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ensure chunk embedding model: %w", err)
	}

	return &CodeIndex{db: db}, nil
}

// SetReranker configures the reranker used by Search. If non-nil, Search
// uses FTS5 pre-filter + Claude reranking. If nil (default), Search returns
// FTS5-only results.
func (ci *CodeIndex) SetReranker(r *Reranker) {
	ci.reranker = r
}

// SetEmbedder configures the embedder used by Build and SearchSemantic. If nil,
// semantic search fails open to the existing FTS5 search path.
func (ci *CodeIndex) SetEmbedder(embedder Embedder) {
	ci.embedder = embedder
}

// Close closes the underlying database connection.
func (ci *CodeIndex) Close() error {
	if err := ci.db.Close(); err != nil {
		return fmt.Errorf("close code index db: %w", err)
	}
	return nil
}

// IsPopulated reports whether the index contains at least one chunk.
func (ci *CodeIndex) IsPopulated(ctx context.Context) (bool, error) {
	if err := contextError(ctx); err != nil {
		return false, err
	}

	var populated bool
	if err := ci.db.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM chunks)").Scan(&populated); err != nil {
		return false, fmt.Errorf("check code index population: %w", err)
	}
	return populated, nil
}

// EnsureCodeIndexReady builds an empty index before it is searched.
//
//oro:testonly
func EnsureCodeIndexReady(ctx context.Context, idx *CodeIndex, root string) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if idx == nil {
		return errors.New("code index is nil")
	}

	populated, err := idx.IsPopulated(ctx)
	if err != nil {
		return fmt.Errorf("check code index readiness: %w", err)
	}
	if populated {
		return nil
	}
	if _, err := idx.Build(ctx, root); err != nil {
		return fmt.Errorf("build code index: %w", err)
	}
	return nil
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("context error: %w", err)
	}
	return nil
}

// Build walks rootDir, chunks all Go files, embeds them, and stores in SQLite.
// This is a full rebuild: existing data is cleared first.
// Uses a transaction to ensure atomicity - if build fails, old data is preserved.
func (ci *CodeIndex) Build(ctx context.Context, rootDir string) (BuildStats, error) {
	start := time.Now()

	// Begin transaction for atomic rebuild.
	tx, err := ci.db.BeginTx(ctx, nil)
	if err != nil {
		return BuildStats{}, fmt.Errorf("begin transaction: %w", err)
	}

	// Ensure rollback on error, commit on success.
	var committed bool
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	// Clear existing data for full rebuild.
	if _, err := tx.ExecContext(ctx, "DELETE FROM chunks"); err != nil {
		return BuildStats{}, fmt.Errorf("clear chunks: %w", err)
	}

	// Rebuild FTS5 index (triggers don't fire on external content tables).
	if _, err := tx.ExecContext(ctx, "INSERT INTO chunks_fts(chunks_fts) VALUES('rebuild')"); err != nil {
		return BuildStats{}, fmt.Errorf("rebuild fts5 index: %w", err)
	}

	var stats BuildStats

	err = filepath.Walk(rootDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("walk error at %s: %w", path, walkErr)
		}

		if info.IsDir() {
			return ci.shouldSkipDir(path, rootDir)
		}

		return ci.indexFile(ctx, tx, path, rootDir, &stats)
	})
	if err != nil {
		return stats, fmt.Errorf("walk %s: %w", rootDir, err)
	}

	// Commit transaction.
	if err := tx.Commit(); err != nil {
		return stats, fmt.Errorf("commit transaction: %w", err)
	}
	committed = true

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
func (ci *CodeIndex) indexFile(ctx context.Context, exec dbExecer, path, rootDir string, stats *BuildStats) error {
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

		if err := ci.insertChunk(ctx, exec, chunk); err != nil {
			return fmt.Errorf("insert chunk %s/%s: %w", chunk.FilePath, chunk.Name, err)
		}

		stats.ChunksIndexed++
	}

	return nil
}

// insertChunk stores a single chunk in the database.
func (ci *CodeIndex) insertChunk(ctx context.Context, exec dbExecer, chunk Chunk) error {
	var embedding []byte
	var embeddingModel *string
	if ci.embedder != nil {
		embedding = encodeFloat32Vector(ci.embedder.Embed(chunkEmbeddingText(chunk)))
		model := ci.embedder.Name()
		embeddingModel = &model
	}

	_, err := exec.ExecContext(ctx,
		`INSERT INTO chunks (file_path, name, kind, start_line, end_line, content, embedding, embedding_model, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		chunk.FilePath, chunk.Name, string(chunk.Kind),
		chunk.StartLine, chunk.EndLine, chunk.Content, embedding, embeddingModel,
		time.Now().UTC().Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("exec insert chunk: %w", err)
	}
	return nil
}

// Search performs two-phase search: FTS5 pre-filter (limit=30) then optional
// Claude reranking. If reranker is nil, returns FTS5-only results scored by
// rank position. FTS5 returns 0 candidates → returns empty without calling reranker.
func (ci *CodeIndex) Search(ctx context.Context, query string, topK int) ([]SearchResult, error) {
	return ci.SearchInWorkdir(ctx, query, topK, "")
}

// SearchSemantic ranks indexed chunks by embedding similarity. It fails open to
// FTS5 search when no embedder is configured or no embedded chunks are present.
func (ci *CodeIndex) SearchSemantic(ctx context.Context, query string) ([]Result, error) {
	const defaultTopK = 10

	if ci.embedder == nil {
		return ci.Search(ctx, query, defaultTopK)
	}

	queryVec := ci.embedder.Embed(query)
	if len(queryVec) == 0 {
		return ci.Search(ctx, query, defaultTopK)
	}

	results, err := ci.searchSemanticVectors(ctx, queryVec, defaultTopK)
	if err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return ci.Search(ctx, query, defaultTopK)
	}
	return results, nil
}

// SearchInWorkdir performs Search with reranker subprocesses bound to workdir.
func (ci *CodeIndex) SearchInWorkdir(ctx context.Context, query string, topK int, workdir string) ([]SearchResult, error) {
	candidates, err := ci.FTS5Search(ctx, query, 30)
	if err != nil {
		return nil, fmt.Errorf("search fts5 phase: %w", err)
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	// If no reranker, return FTS5 results with positional scores.
	if ci.reranker == nil {
		results := make([]SearchResult, 0, min(len(candidates), topK))
		for i, c := range candidates {
			if i >= topK {
				break
			}
			results = append(results, SearchResult{
				Chunk: c,
				Score: 1.0 / float64(i+1),
			})
		}
		return results, nil
	}

	// Rerank candidates.
	scored, err := ci.reranker.RerankInWorkdir(ctx, query, candidates, topK, workdir)
	if err != nil {
		return nil, fmt.Errorf("search rerank phase: %w", err)
	}

	results := make([]SearchResult, len(scored))
	for i, sc := range scored {
		results[i] = SearchResult{
			Chunk:  sc.Chunk,
			Score:  1.0 / float64(sc.Rank),
			Reason: sc.Reason,
		}
	}
	return results, nil
}

func (ci *CodeIndex) searchSemanticVectors(ctx context.Context, queryVec []float32, limit int) ([]Result, error) {
	rows, err := ci.db.QueryContext(ctx, `
		SELECT id, file_path, name, kind, start_line, end_line, content, embedding
		FROM chunks
		WHERE embedding IS NOT NULL
	`)
	if err != nil {
		return nil, fmt.Errorf("semantic vector query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var results []Result
	for rows.Next() {
		var c Chunk
		var id int64
		var kind string
		var raw []byte
		if err := rows.Scan(&id, &c.FilePath, &c.Name, &kind, &c.StartLine, &c.EndLine, &c.Content, &raw); err != nil {
			return nil, fmt.Errorf("scan semantic chunk: %w", err)
		}
		c.Kind = ChunkKind(kind)
		score := cosineSimilarity(queryVec, decodeFloat32Vector(raw))
		results = append(results, Result{Chunk: c, Score: score})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("semantic vector rows: %w", err)
	}

	sort.SliceStable(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})
	if len(results) > limit {
		results = results[:limit]
	}
	return results, nil
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
	sanitized := protocol.SanitizeFTS5Query(query)

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

func chunkEmbeddingText(chunk Chunk) string {
	return chunk.Name + "\n" + chunk.Content
}

func encodeFloat32Vector(vec []float32) []byte {
	if len(vec) == 0 {
		return nil
	}
	buf := make([]byte, len(vec)*4)
	for i, v := range vec {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(v))
	}
	return buf
}

func decodeFloat32Vector(raw []byte) []float32 {
	if len(raw) == 0 || len(raw)%4 != 0 {
		return nil
	}
	vec := make([]float32, len(raw)/4)
	for i := range vec {
		vec[i] = math.Float32frombits(binary.LittleEndian.Uint32(raw[i*4:]))
	}
	return vec
}

func cosineSimilarity(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		av := float64(a[i])
		bv := float64(b[i])
		dot += av * bv
		normA += av * av
		normB += bv * bv
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}

func ensureIndexColumn(ctx context.Context, db *sql.DB, table, column, ddl string) error {
	var name string
	err := db.QueryRowContext(ctx, "SELECT name FROM pragma_table_info(?) WHERE name = ?", table, column).Scan(&name)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("inspect column %s.%s: %w", table, column, err)
	}
	if _, err := db.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("add column %s.%s: %w", table, column, err)
	}
	return nil
}
