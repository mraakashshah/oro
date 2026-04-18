// Package memory provides cross-session project memory for Oro workers.
// It handles storage, extraction, retrieval with ranking, prompt injection,
// and consolidation of learnings across sessions.
package memory

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"

	"oro/pkg/protocol"
)

// Store manages the memories table in SQLite.
type Store struct {
	db       *sql.DB
	embedder Embedder
	project  string // current project scope for queries and inserts
}

// NewStore creates a new Store backed by the given SQLite database.
func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// SetEmbedder attaches an Embedder to the store. When set, Insert() computes
// and stores TF-IDF embeddings, and HybridSearch() uses them for RRF scoring.
func (s *Store) SetEmbedder(e Embedder) {
	s.embedder = e
}

// HasEmbedder reports whether an Embedder has been attached to the store.
//
//oro:testonly
func (s *Store) HasEmbedder() bool {
	return s.embedder != nil
}

// SetProject sets the project scope for subsequent Insert, Search, and
// ForPrompt operations. When set to a non-empty string, only memories
// matching that project are returned or inserted. When set to empty string,
// all memories are accessible (no project filtering).
func (s *Store) SetProject(project string) {
	s.project = project
}

// embeddingDenseModelKey is the kv_store key for the embedder model sentinel.
const embeddingDenseModelKey = "embedding_dense_model"

// resetEmbedderData clears embedding_dense, memory_chunks, and backfill state
// when the model changes, then updates the sentinel. Assumes a transaction.
func resetEmbedderData(ctx context.Context, tx *sql.Tx, newModel string) error {
	if _, err := tx.ExecContext(ctx,
		`UPDATE memories SET embedding_dense = NULL`,
	); err != nil {
		return fmt.Errorf("clear embedding_dense: %w", err)
	}

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM memory_chunks`,
	); err != nil {
		return fmt.Errorf("delete memory_chunks: %w", err)
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE backfill_semantic_memory_state SET state = 'pending' WHERE id = 1`,
	); err != nil {
		return fmt.Errorf("reset backfill state: %w", err)
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE kv_store SET value = ?, updated_at = datetime('now') WHERE key = ?`,
		newModel, embeddingDenseModelKey,
	); err != nil {
		return fmt.Errorf("update sentinel: %w", err)
	}

	return nil
}

// checkEmbedderModelMatch verifies that the stored embedder model matches the
// current model. On mismatch (or missing sentinel on first run):
//   - First run (no sentinel): writes sentinel with currentModel, returns.
//   - Mismatch: clears embedding_dense column (set to NULL), deletes all
//     memory_chunks rows, flips backfill_semantic_memory_state to 'pending',
//     and rewrites sentinel to currentModel.
//   - Match: returns (no-op).
//
// All state changes occur in a single transaction to prevent partial updates.
func (s *Store) checkEmbedderModelMatch(ctx context.Context, currentModel string) error {
	if currentModel == "" {
		return fmt.Errorf("embedder model name required")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	var sentinel string
	err = tx.QueryRowContext(ctx,
		`SELECT value FROM kv_store WHERE key = ?`, embeddingDenseModelKey,
	).Scan(&sentinel)
	if errors.Is(err, sql.ErrNoRows) {
		// First run: write sentinel
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO kv_store (key, value, updated_at) VALUES (?, ?, datetime('now'))`,
			embeddingDenseModelKey, currentModel,
		); err != nil {
			return fmt.Errorf("write sentinel: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit first-run: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("read sentinel: %w", err)
	}

	// If sentinel matches, no-op
	if sentinel == currentModel {
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit noop: %w", err)
		}
		return nil
	}

	// Mismatch: clear state and update sentinel
	if err := resetEmbedderData(ctx, tx, currentModel); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit reset: %w", err)
	}

	return nil
}

// vocabKVKey is the kv_store key used to persist the embedder vocabulary.
const vocabKVKey = "embedder_vocab"

// SaveVocab serializes the attached embedder's vocabulary to the kv_store
// table. Subsequent calls overwrite the previous value (UPSERT). Returns an
// error if no embedder has been set.
func (s *Store) SaveVocab(ctx context.Context) error {
	if s.embedder == nil {
		return fmt.Errorf("save vocab: no embedder set")
	}
	vp, ok := s.embedder.(VocabPersister)
	if !ok {
		return nil
	}
	vocab := vp.ExportVocab()
	data, err := json.Marshal(vocab)
	if err != nil {
		return fmt.Errorf("save vocab marshal: %w", err)
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO kv_store (key, value, updated_at) VALUES (?, ?, datetime('now'))`,
		vocabKVKey, string(data),
	)
	if err != nil {
		return fmt.Errorf("save vocab upsert: %w", err)
	}
	return nil
}

// LoadVocab restores the embedder's vocabulary from the kv_store table. If no
// saved vocab exists this is a no-op (the embedder keeps its current, possibly
// empty, vocabulary). Returns an error if no embedder has been set.
func (s *Store) LoadVocab(ctx context.Context) error {
	if s.embedder == nil {
		return fmt.Errorf("load vocab: no embedder set")
	}
	vp, ok := s.embedder.(VocabPersister)
	if !ok {
		return nil
	}
	var value string
	err := s.db.QueryRowContext(ctx,
		`SELECT value FROM kv_store WHERE key = ?`, vocabKVKey,
	).Scan(&value)
	if err == sql.ErrNoRows {
		return nil // no persisted vocab — fresh start is valid
	}
	if err != nil {
		return fmt.Errorf("load vocab query: %w", err)
	}
	var vocab map[string]int
	if err := json.Unmarshal([]byte(value), &vocab); err != nil {
		return fmt.Errorf("load vocab unmarshal: %w", err)
	}
	vp.ImportVocab(vocab)
	return nil
}

// InsertParams holds parameters for inserting a new memory.
type InsertParams struct {
	Content       string
	Type          string // lesson | decision | gotcha | pattern | preference | summary | self_report
	Tags          []string
	Source        string // self_report | daemon_extracted
	BeadID        string
	WorkerID      string
	Confidence    float64
	FilesRead     []string
	FilesModified []string
	Pinned        bool
}

// SearchOpts configures a FTS5 search query.
type SearchOpts struct {
	Limit    int      // default 10
	Type     string   // optional filter
	Tags     []string // optional tag filter (any match)
	MinScore float64  // minimum combined score threshold
	FilePath string   // optional: filter memories touching this file path
}

// ScoredMemory is a Memory with an associated relevance score.
type ScoredMemory struct {
	protocol.Memory
	Score float64
}

// ListOpts configures a list query.
type ListOpts struct {
	Type   string
	Tag    string
	Limit  int
	Offset int
}

// ConsolidateOpts configures the consolidation process.
type ConsolidateOpts struct {
	SimilarityThreshold float64 // BM25 score threshold for "similar" (default 0.8)
	MinDecayedScore     float64 // minimum decayed score to keep (default 0.1)
	DryRun              bool    // if true, don't actually modify, just count
}

// tagsToJSON converts a string slice to a JSON array string.
func tagsToJSON(tags []string) string {
	if len(tags) == 0 {
		return "[]"
	}
	b, err := json.Marshal(tags)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// tagsFromJSON parses a JSON array string into a string slice.
func tagsFromJSON(s string) []string {
	if s == "" {
		return nil
	}
	var tags []string
	if err := json.Unmarshal([]byte(s), &tags); err != nil {
		return nil
	}
	return tags
}

// dedupJaccardThreshold is the minimum Jaccard similarity of terms above which
// a new memory is considered a duplicate of an existing one. Per the search
// spec, 0.7 is the day-one threshold for FTS5 overlap dedup.
const dedupJaccardThreshold = 0.7

// validMemoryTypes is the set of allowed memory type values for Insert.
//
//nolint:gochecknoglobals // compile-once lookup table, safe as package-level var
var validMemoryTypes = map[string]struct{}{
	"lesson":      {},
	"decision":    {},
	"gotcha":      {},
	"pattern":     {},
	"preference":  {},
	"summary":     {},
	"self_report": {},
}

// preparedFields holds the validated and computed values ready for INSERT.
type preparedFields struct {
	conf          float64
	tags          string
	filesRead     string
	filesModified string
	embeddingBlob []byte
	pinnedInt     int
	project       string
}

// prepareInsert validates InsertParams and computes derived fields (confidence
// default, embedding, JSON tags, pinned int, project). Callers supply a prefix
// for error messages (e.g. "memory insert" vs "memory merge insert").
func (s *Store) prepareInsert(params InsertParams, errPrefix string) (preparedFields, error) {
	if len(params.Content) < 10 {
		return preparedFields{}, fmt.Errorf("%s: content too short (min 10 chars, got %d)", errPrefix, len(params.Content))
	}
	if len(params.Content) > 2048 {
		return preparedFields{}, fmt.Errorf("%s: content too long (max 2048 chars, got %d)", errPrefix, len(params.Content))
	}
	if _, ok := validMemoryTypes[params.Type]; !ok {
		return preparedFields{}, fmt.Errorf("%s: invalid type %q", errPrefix, params.Type)
	}

	conf := params.Confidence
	if conf == 0 {
		conf = 0.8
	}

	var embeddingBlob []byte
	if s.embedder != nil {
		if vec := s.embedder.Embed(params.Content); vec != nil {
			embeddingBlob = MarshalEmbedding(vec)
		}
	}

	pinnedInt := 0
	if params.Pinned {
		pinnedInt = 1
	}

	project := s.project
	if project == "" {
		project = "oro"
	}

	return preparedFields{
		conf:          conf,
		tags:          tagsToJSON(params.Tags),
		filesRead:     tagsToJSON(params.FilesRead),
		filesModified: tagsToJSON(params.FilesModified),
		embeddingBlob: embeddingBlob,
		pinnedInt:     pinnedInt,
		project:       project,
	}, nil
}

// Insert adds a new memory with write-time dedup. Before inserting, it checks
// FTS5 for existing memories with high term overlap (Jaccard similarity).
// If a near-duplicate exists:
//   - If the existing memory has lower confidence, update it to max of both
//   - Return the existing ID (no new row created)
//
// Returns the inserted (or existing duplicate) ID.
func (s *Store) Insert(ctx context.Context, m InsertParams) (int64, error) {
	pf, err := s.prepareInsert(m, "memory insert")
	if err != nil {
		return 0, err
	}

	// Write-time dedup: check for near-duplicates via FTS5 + Jaccard.
	dupID, err := s.checkDuplicate(ctx, m.Content, pf.conf)
	if err != nil {
		// Dedup check failed -- proceed with insert rather than blocking writes.
		_ = err
	} else if dupID > 0 {
		return dupID, nil
	}

	res, err := s.db.ExecContext(ctx, //nolint:gosec // G701 false positive: parameterized query with ? placeholders, no string concatenation
		`INSERT INTO memories (content, type, tags, source, bead_id, worker_id, confidence, embedding, files_read, files_modified, pinned, project)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.Content, m.Type, pf.tags, m.Source, m.BeadID, m.WorkerID, pf.conf, pf.embeddingBlob, pf.filesRead, pf.filesModified, pf.pinnedInt, pf.project,
	)
	if err != nil {
		return 0, fmt.Errorf("memory insert: %w", err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("memory last insert id: %w", err)
	}
	return id, nil
}

// checkDuplicate searches for an existing memory with high term overlap.
// Uses FTS5 to find candidates, then Jaccard similarity of lowercased terms
// to confirm near-duplication. Returns the duplicate's ID if found
// (and updates its confidence if needed), or 0 if no duplicate exists.
func (s *Store) checkDuplicate(ctx context.Context, content string, newConf float64) (int64, error) {
	results, err := s.Search(ctx, content, SearchOpts{Limit: 3})
	if err != nil {
		return 0, fmt.Errorf("dedup search: %w", err)
	}

	newTerms := termSet(content)
	for _, r := range results {
		existTerms := termSet(r.Content)
		if jaccardSimilarity(newTerms, existTerms) < dedupJaccardThreshold {
			continue
		}
		// Near-duplicate found. Update confidence to max of both if needed.
		if newConf > r.Confidence {
			if err := s.UpdateConfidence(ctx, r.ID, newConf); err != nil {
				return 0, fmt.Errorf("dedup update confidence: %w", err)
			}
		}
		return r.ID, nil
	}

	return 0, nil
}

// termSet returns the set of lowercased words in s.
func termSet(s string) map[string]struct{} {
	words := strings.Fields(strings.ToLower(s))
	set := make(map[string]struct{}, len(words))
	for _, w := range words {
		set[w] = struct{}{}
	}
	return set
}

// jaccardSimilarity computes |A ∩ B| / |A ∪ B| for two term sets.
func jaccardSimilarity(a, b map[string]struct{}) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 1.0
	}
	intersection := 0
	for w := range a {
		if _, ok := b[w]; ok {
			intersection++
		}
	}
	union := len(a) + len(b) - intersection
	return float64(intersection) / float64(union)
}

// searchSQL builds the FTS5 search SQL and args for the given query and opts.
// project parameter filters by project column; empty string means no filtering.
func searchSQL(query string, opts SearchOpts, project string) (stmt string, args []any) {
	limit := opts.Limit
	if limit <= 0 {
		limit = 10
	}

	conditions := []string{"memories_fts MATCH ?"}
	args = []any{protocol.SanitizeFTS5Query(query)}

	if opts.Type != "" {
		conditions = append(conditions, "m.type = ?")
		args = append(args, opts.Type)
	}
	if opts.FilePath != "" {
		conditions = append(conditions, "(m.files_read LIKE ? OR m.files_modified LIKE ?)")
		pattern := "%" + opts.FilePath + "%"
		args = append(args, pattern, pattern)
	}
	if project != "" {
		conditions = append(conditions, "m.project = ?")
		args = append(args, project)
	}

	// Use FTS5 rank for relevance ordering; compute score in Go.
	q := fmt.Sprintf(`
		SELECT m.id, m.content, m.type, m.tags, m.source,
		       COALESCE(m.bead_id, '') AS bead_id,
		       COALESCE(m.worker_id, '') AS worker_id,
		       m.confidence, m.created_at, m.embedding,
		       COALESCE(m.files_read, '[]') AS files_read,
		       COALESCE(m.files_modified, '[]') AS files_modified,
		       (julianday('now') - julianday(m.created_at)) AS age_days,
		       COALESCE(m.pinned, 0) AS pinned
		FROM memories_fts
		JOIN memories m ON memories_fts.rowid = m.id
		WHERE %s
		ORDER BY rank
		LIMIT ?
	`, strings.Join(conditions, " AND "))

	args = append(args, limit)
	return q, args
}

// Search performs FTS5-ranked search with optional type filter.
// Results are scored by: confidence * time_decay, ordered by FTS5 relevance.
// Note: bm25() returns negligible values with modernc.org/sqlite (pure-Go),
// so we use FTS5 rank for ordering and compute scores in Go.
func (s *Store) Search(ctx context.Context, query string, opts SearchOpts) ([]ScoredMemory, error) {
	if query == "" {
		return nil, nil
	}

	q, args := searchSQL(query, opts, s.project)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("memory search: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var results []ScoredMemory
	for rows.Next() {
		sm, err := scanScoredMemory(rows)
		if err != nil {
			return nil, err
		}
		if opts.MinScore > 0 && sm.Score < opts.MinScore {
			continue
		}
		if len(opts.Tags) > 0 && !anyTagMatch(tagsFromJSON(sm.Tags), opts.Tags) {
			continue
		}
		results = append(results, sm)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("memory search rows: %w", err)
	}

	sortByScoreDesc(results)
	return results, nil
}

// rrfK is the smoothing constant for Reciprocal Rank Fusion.
// k=60 is the standard starting point from the RRF paper (Cormack et al. 2009).
const rrfK = 60.0

// HybridSearch combines FTS5 text search with vector cosine similarity using
// Reciprocal Rank Fusion (RRF). The combined score for each memory is:
//
//	RRF = 1/(k + textRank) + 1/(k + vectorRank)
//
// where textRank and vectorRank are 1-based positions in the FTS5 and cosine
// similarity result lists respectively. Items appearing in only one list
// receive a partial RRF score from that list alone.
//
// If no embedder is set, falls back to plain FTS5 Search().
func (s *Store) HybridSearch(ctx context.Context, query string, opts SearchOpts) ([]ScoredMemory, error) {
	if query == "" {
		return nil, nil
	}

	// Phase 1: FTS5 text search (always available).
	ftsResults, err := s.Search(ctx, query, SearchOpts{
		Limit: maxHybridCandidates(opts.Limit),
		Type:  opts.Type,
	})
	if err != nil {
		return nil, fmt.Errorf("hybrid fts search: %w", err)
	}

	// If no embedder, fall back to FTS5-only with original filtering.
	if s.embedder == nil {
		return applyFilters(ftsResults, opts), nil
	}

	// Phase 2: Vector similarity search.
	queryVec := s.embedder.Embed(query)
	vectorResults, vecErr := s.vectorSearch(ctx, queryVec, maxHybridCandidates(opts.Limit), opts.Type)
	if vecErr != nil {
		// Vector search failure is non-fatal; degrade gracefully to FTS-only.
		return applyFilters(ftsResults, opts), nil //nolint:nilerr // intentional graceful degradation
	}

	// Phase 3: Fuse with RRF.
	fused := fuseRRF(ftsResults, vectorResults)

	return applyFilters(fused, opts), nil
}

// maxHybridCandidates returns the candidate pool size for each search phase.
// We fetch more candidates than the final limit to give RRF a richer pool.
func maxHybridCandidates(limit int) int {
	if limit <= 0 {
		limit = 10
	}
	n := limit * 3
	if n < 20 {
		n = 20
	}
	return n
}

// maxVectorCandidates is the maximum number of candidate rows loaded from the
// database for vector similarity scoring, to bound memory usage.
const maxVectorCandidates = 1000

// vectorSearch retrieves memories and ranks them by cosine similarity to queryVec.
//
//nolint:funlen // extra lines from file tracking columns in SELECT
func (s *Store) vectorSearch(ctx context.Context, queryVec []float32, limit int, typeFilter string) ([]ScoredMemory, error) {
	if len(queryVec) == 0 {
		return nil, nil
	}

	// Fetch recent memories that have embeddings, bounded by maxVectorCandidates.
	q := `SELECT id, content, type, tags, source,
	       COALESCE(bead_id, '') AS bead_id,
	       COALESCE(worker_id, '') AS worker_id,
	       confidence, created_at, embedding,
	       COALESCE(files_read, '[]') AS files_read, COALESCE(files_modified, '[]') AS files_modified,
	       (julianday('now') - julianday(created_at)) AS age_days,
	       COALESCE(pinned, 0) AS pinned
	FROM memories
	WHERE embedding IS NOT NULL`

	var args []any
	if typeFilter != "" {
		q += " AND type = ?"
		args = append(args, typeFilter)
	}
	if s.project != "" {
		q += " AND project = ?"
		args = append(args, s.project)
	}

	q += " ORDER BY created_at DESC LIMIT ?"
	args = append(args, maxVectorCandidates)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("vector search query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	type scored struct {
		sm  ScoredMemory
		cos float64
	}

	var candidates []scored
	for rows.Next() {
		sm, err := scanScoredMemory(rows)
		if err != nil {
			return nil, err
		}
		vec := UnmarshalEmbedding(sm.Embedding)
		if len(vec) == 0 {
			continue
		}
		cos := CosineSimilarity(queryVec, vec)
		candidates = append(candidates, scored{sm: sm, cos: cos})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("vector search rows: %w", err)
	}

	// Sort by cosine similarity descending.
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].cos > candidates[j].cos
	})

	// Truncate to limit.
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}

	results := make([]ScoredMemory, len(candidates))
	for i, c := range candidates {
		results[i] = c.sm
		// Overwrite Score with cosine for later RRF ranking.
		results[i].Score = c.cos
	}
	return results, nil
}

// fuseRRF merges FTS5 and vector result lists using Reciprocal Rank Fusion.
// Each item's final score is: 1/(k+textRank) + 1/(k+vectorRank).
// Items in only one list get a partial score.
func fuseRRF(ftsResults, vectorResults []ScoredMemory) []ScoredMemory {
	type entry struct {
		sm         ScoredMemory
		textRank   int
		vectorRank int
	}

	byID := make(map[int64]*entry)

	for rank, sm := range ftsResults {
		byID[sm.ID] = &entry{sm: sm, textRank: rank + 1}
	}

	for rank, sm := range vectorResults {
		if e, ok := byID[sm.ID]; ok {
			e.vectorRank = rank + 1
		} else {
			byID[sm.ID] = &entry{sm: sm, vectorRank: rank + 1}
		}
	}

	results := make([]ScoredMemory, 0, len(byID))
	for _, e := range byID {
		e.sm.Score = RRFScore(e.textRank, e.vectorRank, rrfK)
		results = append(results, e.sm)
	}

	sortByScoreDesc(results)
	return results
}

// applyFilters applies MinScore, Tags, and Limit filters to results.
func applyFilters(results []ScoredMemory, opts SearchOpts) []ScoredMemory {
	limit := opts.Limit
	if limit <= 0 {
		limit = 10
	}

	var filtered []ScoredMemory
	for _, r := range results {
		if opts.MinScore > 0 && r.Score < opts.MinScore {
			continue
		}
		if len(opts.Tags) > 0 && !anyTagMatch(tagsFromJSON(r.Tags), opts.Tags) {
			continue
		}
		filtered = append(filtered, r)
		if len(filtered) >= limit {
			break
		}
	}
	return filtered
}

// scanScoredMemory scans a single row from the search query into a ScoredMemory.
func scanScoredMemory(rows *sql.Rows) (ScoredMemory, error) {
	var sm ScoredMemory
	var embedding sql.NullString
	var ageDays float64
	var pinnedInt int
	if err := rows.Scan(
		&sm.ID, &sm.Content, &sm.Type, &sm.Tags, &sm.Source,
		&sm.BeadID, &sm.WorkerID, &sm.Confidence, &sm.CreatedAt,
		&embedding, &sm.FilesRead, &sm.FilesModified, &ageDays, &pinnedInt,
	); err != nil {
		return sm, fmt.Errorf("memory search scan: %w", err)
	}
	if embedding.Valid {
		sm.Embedding = []byte(embedding.String)
	}
	sm.Pinned = pinnedInt != 0

	// Score: confidence * time_decay (halves every 30 days)
	// Pinned memories skip time decay (decay factor = 1.0)
	decayFactor := math.Pow(0.5, ageDays/30.0)
	if sm.Pinned {
		decayFactor = 1.0
	}
	sm.Score = sm.Confidence * decayFactor
	return sm, nil
}

// anyTagMatch returns true if any tag in a appears in b.
func anyTagMatch(a, b []string) bool {
	set := make(map[string]struct{}, len(b))
	for _, t := range b {
		set[t] = struct{}{}
	}
	for _, t := range a {
		if _, ok := set[t]; ok {
			return true
		}
	}
	return false
}

// sortByScoreDesc sorts ScoredMemory results by Score descending.
func sortByScoreDesc(results []ScoredMemory) {
	sort.Slice(results, func(i, j int) bool {
		return results[i].Score > results[j].Score
	})
}

// List returns memories matching optional filters, ordered by created_at desc.
func (s *Store) List(ctx context.Context, opts ListOpts) ([]protocol.Memory, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = 50
	}

	q, args := listSQL(opts, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("memory list: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var results []protocol.Memory
	for rows.Next() {
		m, err := scanMemory(rows)
		if err != nil {
			return nil, err
		}
		results = append(results, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("memory list rows: %w", err)
	}
	return results, nil
}

// escapeLike escapes %, _, and backslash in s so it is safe to use as a
// literal pattern in a SQL LIKE expression with backslash as the escape char.
func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, "%", `\%`)
	s = strings.ReplaceAll(s, "_", `\_`)
	return s
}

// listSQL builds the list query SQL and args.
func listSQL(opts ListOpts, limit int) (query string, args []any) {
	var conditions []string

	if opts.Type != "" {
		conditions = append(conditions, "type = ?")
		args = append(args, opts.Type)
	}
	if opts.Tag != "" {
		conditions = append(conditions, `tags LIKE ? ESCAPE '\'`)
		args = append(args, fmt.Sprintf(`%%"%s"%%`, escapeLike(opts.Tag))) //nolint:gocritic // %q changes escaping and breaks SQL LIKE
	}

	whereClause := ""
	if len(conditions) > 0 {
		whereClause = "WHERE " + strings.Join(conditions, " AND ")
	}

	q := fmt.Sprintf(`
		SELECT id, content, type, tags, source,
		       COALESCE(bead_id, '') AS bead_id,
		       COALESCE(worker_id, '') AS worker_id,
		       confidence, created_at, embedding,
		       COALESCE(files_read, '[]') AS files_read,
		       COALESCE(files_modified, '[]') AS files_modified,
		       COALESCE(pinned, 0) AS pinned
		FROM memories %s
		ORDER BY created_at DESC, id DESC
		LIMIT ? OFFSET ?
	`, whereClause)
	args = append(args, limit, opts.Offset)
	return q, args
}

// scanMemory scans a single row from the list query into a protocol.Memory.
func scanMemory(rows *sql.Rows) (protocol.Memory, error) {
	var m protocol.Memory
	var embedding sql.NullString
	var pinnedInt int
	if err := rows.Scan(
		&m.ID, &m.Content, &m.Type, &m.Tags, &m.Source,
		&m.BeadID, &m.WorkerID, &m.Confidence, &m.CreatedAt,
		&embedding, &m.FilesRead, &m.FilesModified, &pinnedInt,
	); err != nil {
		return m, fmt.Errorf("memory list scan: %w", err)
	}
	if embedding.Valid {
		m.Embedding = []byte(embedding.String)
	}
	m.Pinned = pinnedInt != 0
	return m, nil
}

// GetByID retrieves a single memory by its ID.
func (s *Store) GetByID(ctx context.Context, id int64) (protocol.Memory, error) {
	q := `SELECT id, content, type, tags, source,
	       COALESCE(bead_id, '') AS bead_id,
	       COALESCE(worker_id, '') AS worker_id,
	       confidence, created_at, embedding,
	       COALESCE(files_read, '[]') AS files_read,
	       COALESCE(files_modified, '[]') AS files_modified,
	       COALESCE(pinned, 0) AS pinned
	FROM memories WHERE id = ?`

	var m protocol.Memory
	var embedding sql.NullString
	var pinnedInt int
	err := s.db.QueryRowContext(ctx, q, id).Scan(
		&m.ID, &m.Content, &m.Type, &m.Tags, &m.Source,
		&m.BeadID, &m.WorkerID, &m.Confidence, &m.CreatedAt,
		&embedding, &m.FilesRead, &m.FilesModified, &pinnedInt,
	)
	if err == sql.ErrNoRows {
		return m, fmt.Errorf("memory %d not found", id)
	}
	if err != nil {
		return m, fmt.Errorf("memory get by id: %w", err)
	}
	if embedding.Valid {
		m.Embedding = []byte(embedding.String)
	}
	m.Pinned = pinnedInt != 0
	return m, nil
}

// Delete removes a memory by ID.
func (s *Store) Delete(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM memories WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("memory delete: %w", err)
	}
	return nil
}

// UpdateConfidence updates the confidence score for a memory.
func (s *Store) UpdateConfidence(ctx context.Context, id int64, confidence float64) error {
	_, err := s.db.ExecContext(ctx, //nolint:gosec // G701: query uses parameterized placeholders, not string interpolation
		`UPDATE memories SET confidence = ? WHERE id = ?`,
		confidence, id,
	)
	if err != nil {
		return fmt.Errorf("memory update confidence: %w", err)
	}
	return nil
}

// DumpAll returns all memories for the current project. If the table is empty,
// returns nil. Respects the project scope set via SetProject().
//
//oro:testonly
func (s *Store) DumpAll(ctx context.Context) ([]protocol.Memory, error) {
	var whereClause string
	var args []any
	if s.project != "" {
		whereClause = "WHERE project = ?"
		args = append(args, s.project)
	}

	q := fmt.Sprintf( //nolint:gosec // G201 false positive: whereClause is safe (built from constants or project scope)
		`
		SELECT id, content, type, tags, source,
		       COALESCE(bead_id, '') AS bead_id,
		       COALESCE(worker_id, '') AS worker_id,
		       confidence, created_at, embedding,
		       COALESCE(files_read, '[]') AS files_read,
		       COALESCE(files_modified, '[]') AS files_modified,
		       COALESCE(pinned, 0) AS pinned
		FROM memories %s
		ORDER BY created_at DESC, id DESC
	`, whereClause)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("memory dump all: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var results []protocol.Memory
	for rows.Next() {
		m, err := scanMemory(rows)
		if err != nil {
			return nil, err
		}
		results = append(results, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("memory dump all rows: %w", err)
	}
	return results, nil
}

// MergeMemories keeps the memory with keepID and deletes memories with the given deleteIDs.
// Returns an error if keepID doesn't exist.
// The existence check and deletion are wrapped in a single transaction for atomicity.
//
//oro:testonly
func (s *Store) MergeMemories(ctx context.Context, keepID int64, deleteIDs []int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("memory merge begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Verify keepID exists
	var exists int
	if err := tx.QueryRowContext(ctx,
		`SELECT 1 FROM memories WHERE id = ?`,
		keepID,
	).Scan(&exists); err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("memory merge: keep memory ID %d not found", keepID)
		}
		return fmt.Errorf("memory merge check keep: %w", err)
	}

	// Delete the specified memories
	if len(deleteIDs) > 0 {
		placeholders := make([]string, len(deleteIDs))
		args := make([]any, len(deleteIDs))
		for i, id := range deleteIDs {
			placeholders[i] = "?"
			args[i] = id
		}
		q := fmt.Sprintf(`DELETE FROM memories WHERE id IN (%s)`, strings.Join(placeholders, ",")) //nolint:gosec // G201 false positive: placeholders are hardcoded "?" and args are parameterized
		if _, err := tx.ExecContext(ctx, q, args...); err != nil {
			return fmt.Errorf("memory merge delete: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("memory merge commit: %w", err)
	}
	return nil
}

// executeMergeAtomic inserts params as a new memory and deletes deleteIDs in a single
// transaction. If any delete fails the insert is rolled back. Unlike Insert, the
// write-time dedup check is skipped — the dreamer explicitly supplies the merged content.
func (s *Store) executeMergeAtomic(ctx context.Context, params InsertParams, deleteIDs []int64) (int64, error) {
	pf, err := s.prepareInsert(params, "memory merge insert")
	if err != nil {
		return 0, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("memory merge begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx, //nolint:gosec // G701 false positive: parameterized query with ? placeholders, no string concatenation
		`INSERT INTO memories (content, type, tags, source, bead_id, worker_id, confidence, embedding, files_read, files_modified, pinned, project)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		params.Content, params.Type, pf.tags, params.Source, params.BeadID, params.WorkerID, pf.conf,
		pf.embeddingBlob, pf.filesRead, pf.filesModified, pf.pinnedInt, pf.project,
	)
	if err != nil {
		return 0, fmt.Errorf("memory merge insert: %w", err)
	}

	newID, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("memory merge last insert id: %w", err)
	}

	for _, delID := range deleteIDs {
		if _, err := tx.ExecContext(ctx, `DELETE FROM memories WHERE id = ?`, delID); err != nil {
			return 0, fmt.Errorf("memory merge delete %d: %w", delID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("memory merge commit: %w", err)
	}
	return newID, nil
}

// markerRe matches [MEMORY] marker lines.
// Format: [MEMORY] type=<type>[ tags=<tag1,tag2>]: <content>
var markerRe = regexp.MustCompile(`^\[MEMORY\]\s+type=(\w+)(?:\s+tags=([^\s:]+))?:\s+(.+)$`)

// ParseMarker extracts a memory from a [MEMORY] marker line.
// Returns nil if the line doesn't contain a valid marker.
func ParseMarker(line string) *InsertParams {
	line = strings.TrimSpace(line)
	m := markerRe.FindStringSubmatch(line)
	if m == nil {
		return nil
	}

	memType := m[1]
	tagsStr := m[2]
	content := m[3]

	var tags []string
	if tagsStr != "" {
		tags = strings.Split(tagsStr, ",")
	}

	return &InsertParams{
		Content:    content,
		Type:       memType,
		Tags:       tags,
		Source:     "self_report",
		Confidence: 0.8,
	}
}

// ExtractMarkers scans an io.Reader for [MEMORY] markers and inserts them
// into the store. Returns the count of successfully extracted markers.
//
//oro:testonly
func ExtractMarkers(ctx context.Context, r io.Reader, store *Store, workerID, beadID string) (int, error) {
	scanner := bufio.NewScanner(r)
	count := 0

	for scanner.Scan() {
		line := scanner.Text()
		params := ParseMarker(line)
		if params == nil {
			continue
		}

		params.WorkerID = workerID
		params.BeadID = beadID

		if _, err := store.Insert(ctx, *params); err != nil {
			return count, fmt.Errorf("extract markers insert: %w", err)
		}
		count++
	}

	if err := scanner.Err(); err != nil {
		return count, fmt.Errorf("extract markers scan: %w", err)
	}

	return count, nil
}

// maxInjectedMemories is the maximum number of memories injected into a prompt.
// Per search spec: 5 memories max, but token budget is the binding constraint.
const maxInjectedMemories = 5

// ForPrompt retrieves the most relevant memories for a bead and formats them
// as a compact index table suitable for injection into the worker prompt.
// Returns a markdown table with ID, Type, and Title (truncated content).
// Workers can fetch full details with 'oro recall --id=N'.
// Token estimation uses len(content)/4 (~4 chars per token for English).
func ForPrompt(ctx context.Context, store *Store, beadTags []string, beadDesc string, maxTokens int) (string, error) {
	_ = maxTokens // reserved for future token budget enforcement in compact mode

	if beadDesc == "" {
		return "", nil
	}

	var results []ScoredMemory
	var err error
	if store.embedder != nil {
		results, err = store.HybridSearch(ctx, beadDesc, SearchOpts{
			Limit: 10,
			Tags:  beadTags,
		})
	} else {
		results, err = store.Search(ctx, beadDesc, SearchOpts{
			Limit: 10,
			Tags:  beadTags,
		})
	}
	if err != nil {
		return "", fmt.Errorf("for prompt search: %w", err)
	}

	if len(results) == 0 {
		return "", nil
	}

	rows := memoryTableRows(results)
	if len(rows) == 0 {
		return "", nil
	}

	lines := make([]string, 0, 3+len(rows)+2)
	lines = append(lines,
		"## Relevant Memories",
		"| ID | Type | Title | Age | Tokens |",
		"|----|------|-------|-----|--------|",
	)
	lines = append(lines, rows...)
	lines = append(lines, "", "Use `oro recall --id=N` to fetch full memory content.")

	return strings.Join(lines, "\n"), nil
}

// memoryTableRows builds the data rows for the ForPrompt compact table.
func memoryTableRows(results []ScoredMemory) []string {
	rows := make([]string, 0, maxInjectedMemories)
	for _, m := range results {
		if len(rows) >= maxInjectedMemories {
			break
		}
		title := m.Content
		if len(title) > 50 {
			title = title[:47] + "..."
		}
		age := formatAge(m.CreatedAt)
		if isStaleMemory(m.CreatedAt) && !m.Pinned {
			age += " ⚠"
		}
		rows = append(rows, fmt.Sprintf("| %d | %s | %s | %s | ~%d |",
			m.ID, m.Type, title, age, estimateTokens(m.Content)))
	}
	return rows
}

// parseCreatedAt parses a SQLite datetime string in "YYYY-MM-DD HH:MM:SS" or "YYYY-MM-DD" format.
func parseCreatedAt(createdAt string) (time.Time, error) {
	t, err := time.Parse("2006-01-02 15:04:05", createdAt)
	if err != nil {
		t, err = time.Parse("2006-01-02", createdAt)
		if err != nil {
			return time.Time{}, fmt.Errorf("parse created_at %q: %w", createdAt, err)
		}
	}
	return t, nil
}

// isStaleMemory returns true if the memory was created more than 7 days ago.
func isStaleMemory(createdAt string) bool {
	if createdAt == "" {
		return false
	}
	t, err := parseCreatedAt(createdAt)
	if err != nil {
		return false
	}
	return time.Since(t) > 7*24*time.Hour
}

// estimateTokens returns an approximate token count for text (~4 chars/token).
func estimateTokens(text string) int {
	n := len(text) / 4
	if n == 0 && text != "" {
		return 1
	}
	return n
}

// formatAge returns a human-readable age string from a datetime string.
// created_at is in "YYYY-MM-DD HH:MM:SS" format from SQLite datetime('now').
// Returns "<1m" for sub-minute, "Xm" for minutes, "Xh" for hours, "Xd" for days.
func formatAge(createdAt string) string {
	if createdAt == "" {
		return ""
	}
	t, err := parseCreatedAt(createdAt)
	if err != nil {
		return createdAt
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "<1m"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// Consolidate deduplicates and prunes the memory store.
// - Finds pairs with high FTS5 similarity (BM25 score above threshold)
// - Merges content of duplicates, keeping higher confidence
// - Prunes memories with decayed score below minScore
// Returns count of merged and pruned memories.
func Consolidate(ctx context.Context, store *Store, opts ConsolidateOpts) (merged, pruned int, err error) {
	if opts.SimilarityThreshold <= 0 {
		opts.SimilarityThreshold = 0.8
	}
	if opts.MinDecayedScore <= 0 {
		opts.MinDecayedScore = 0.1
	}

	// Phase 1: Prune stale memories with low decayed scores.
	pruned, err = pruneStale(ctx, store, opts.MinDecayedScore, opts.DryRun)
	if err != nil {
		return 0, 0, fmt.Errorf("consolidate prune: %w", err)
	}

	// Phase 2: Merge duplicates.
	merged, err = mergeDuplicates(ctx, store, opts.SimilarityThreshold, opts.DryRun)
	if err != nil {
		return merged, pruned, fmt.Errorf("consolidate merge: %w", err)
	}

	return merged, pruned, nil
}

// pruneStale removes memories whose decayed score is below minScore.
// Decayed score = confidence * 0.5^(age_days/30).
// Pinned memories are always excluded.
func pruneStale(ctx context.Context, store *Store, minScore float64, dryRun bool) (int, error) {
	q := `
		SELECT id, confidence,
		       (julianday('now') - julianday(created_at)) AS age_days
		FROM memories
		WHERE COALESCE(pinned, 0) = 0
	`
	rows, err := store.db.QueryContext(ctx, q)
	if err != nil {
		return 0, fmt.Errorf("prune stale query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var ids []int64
	for rows.Next() {
		var id int64
		var confidence, ageDays float64
		if err := rows.Scan(&id, &confidence, &ageDays); err != nil {
			return 0, fmt.Errorf("prune stale scan: %w", err)
		}
		decayedScore := confidence * math.Pow(0.5, ageDays/30.0)
		if decayedScore < minScore {
			ids = append(ids, id)
		}
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("prune stale rows: %w", err)
	}

	if dryRun {
		return len(ids), nil
	}

	for _, id := range ids {
		if err := store.Delete(ctx, id); err != nil {
			return 0, fmt.Errorf("prune stale delete: %w", err)
		}
	}

	return len(ids), nil
}

// mergeDuplicates finds pairs of similar memories and merges them.
// mergePair keeps the higher-confidence memory and deletes the other.
// The confidence update and deletion are wrapped in a transaction for atomicity.
func mergePair(ctx context.Context, store *Store, a, b protocol.Memory) error {
	keepID, removeID := a.ID, b.ID
	keepConf, removeConf := a.Confidence, b.Confidence

	if removeConf > keepConf {
		keepID, removeID = removeID, keepID
		keepConf = removeConf
	}

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("merge pair begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, //nolint:gosec // G701 false positive: parameterized query with ? placeholders
		`UPDATE memories SET confidence = ? WHERE id = ?`,
		math.Max(keepConf, removeConf), keepID,
	); err != nil {
		return fmt.Errorf("merge update confidence: %w", err)
	}

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM memories WHERE id = ?`, removeID,
	); err != nil {
		return fmt.Errorf("merge delete duplicate: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("merge pair commit: %w", err)
	}
	return nil
}

// processSimilarMemories merges similar memories and updates the deletion map.
// Returns the number of merges performed and any error encountered.
func processSimilarMemories(
	ctx context.Context,
	store *Store,
	current protocol.Memory,
	similar []ScoredMemory,
	threshold float64,
	dryRun bool,
	deleted map[int64]bool,
	maxMerges int,
	currentMerged int,
) (int, error) {
	merged := 0
	for _, s := range similar {
		if s.ID == current.ID || deleted[s.ID] || s.Score < threshold {
			continue
		}

		if !dryRun {
			if err := mergePair(ctx, store, current, s.Memory); err != nil {
				return merged, err
			}
		}

		merged++
		deleted[s.ID] = true

		// Early termination after reaching max merges
		if currentMerged+merged >= maxMerges {
			break
		}
	}
	return merged, nil
}

func mergeDuplicates(ctx context.Context, store *Store, threshold float64, dryRun bool) (int, error) {
	const (
		batchSize = 100
		maxMerges = 50
	)

	all, err := store.List(ctx, ListOpts{Limit: batchSize})
	if err != nil {
		return 0, fmt.Errorf("merge duplicates list: %w", err)
	}

	merged := 0
	deleted := make(map[int64]bool)

	for i := range all {
		if deleted[all[i].ID] || merged >= maxMerges {
			continue
		}

		similar, err := store.Search(ctx, all[i].Content, SearchOpts{Limit: 5})
		if err != nil {
			continue
		}

		count, err := processSimilarMemories(ctx, store, all[i], similar, threshold, dryRun, deleted, maxMerges, merged)
		if err != nil {
			return merged, err
		}
		merged += count

		if merged >= maxMerges {
			break
		}
	}

	return merged, nil
}

// Rejection holds a single reviewer rejection record from rejection_history.
type Rejection struct {
	ID        int64
	BeadID    string
	WorkerID  string
	Feedback  string
	CreatedAt string
}

// InsertRejection stores reviewer feedback in the rejection_history table.
// It never writes to the memories table, so rejections do not pollute memory
// search results.
func (s *Store) InsertRejection(ctx context.Context, beadID, workerID, feedback string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO rejection_history (bead_id, worker_id, feedback) VALUES (?, ?, ?)`,
		beadID, workerID, feedback,
	)
	if err != nil {
		return fmt.Errorf("insert rejection: %w", err)
	}
	return nil
}

// GetRejections returns all rejection_history entries for the given bead,
// ordered by created_at ascending (oldest first).
func (s *Store) GetRejections(ctx context.Context, beadID string) ([]Rejection, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, bead_id, COALESCE(worker_id, ''), feedback, created_at
		 FROM rejection_history WHERE bead_id = ? ORDER BY created_at ASC, id ASC`,
		beadID,
	)
	if err != nil {
		return nil, fmt.Errorf("get rejections: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var results []Rejection
	for rows.Next() {
		var r Rejection
		if err := rows.Scan(&r.ID, &r.BeadID, &r.WorkerID, &r.Feedback, &r.CreatedAt); err != nil {
			return nil, fmt.Errorf("get rejections scan: %w", err)
		}
		results = append(results, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("get rejections rows: %w", err)
	}
	return results, nil
}
