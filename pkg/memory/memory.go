// Package memory provides cross-session project memory for Oro workers.
// It handles storage, extraction, retrieval with ranking, prompt injection,
// and consolidation of learnings across sessions.
package memory

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"regexp"
	"sort"
	"strings"

	"oro/pkg/protocol"
)

// Store manages the memories table in SQLite.
type Store struct {
	db       *sql.DB
	embedder *Embedder
}

// NewStore creates a new Store backed by the given SQLite database.
func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// SetEmbedder attaches an Embedder to the store. When set, Insert() computes
// and stores TF-IDF embeddings, and HybridSearch() uses them for RRF scoring.
//
//oro:testonly
func (s *Store) SetEmbedder(e *Embedder) {
	s.embedder = e
}

// InsertParams holds parameters for inserting a new memory.
type InsertParams struct {
	Content       string
	Type          string // lesson | decision | gotcha | pattern | preference
	Tags          []string
	Source        string // self_report | daemon_extracted
	BeadID        string
	WorkerID      string
	Confidence    float64
	FilesRead     []string
	FilesModified []string
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

// DedupJaccardThreshold is the minimum Jaccard similarity of terms above which
// a new memory is considered a duplicate of an existing one. Per the search
// spec, 0.7 is the day-one threshold for FTS5 overlap dedup.
const DedupJaccardThreshold = 0.7

// Insert adds a new memory with write-time dedup. Before inserting, it checks
// FTS5 for existing memories with high term overlap (Jaccard similarity).
// If a near-duplicate exists:
//   - If the existing memory has lower confidence, update it to max of both
//   - Return the existing ID (no new row created)
//
// Returns the inserted (or existing duplicate) ID.
func (s *Store) Insert(ctx context.Context, m InsertParams) (int64, error) {
	conf := m.Confidence
	if conf == 0 {
		conf = 0.8
	}

	// Write-time dedup: check for near-duplicates via FTS5 + Jaccard.
	dupID, err := s.checkDuplicate(ctx, m.Content, conf)
	if err != nil {
		// Dedup check failed -- proceed with insert rather than blocking writes.
		_ = err
	} else if dupID > 0 {
		return dupID, nil
	}

	tags := tagsToJSON(m.Tags)
	filesRead := tagsToJSON(m.FilesRead)
	filesModified := tagsToJSON(m.FilesModified)

	// Compute embedding if embedder is attached.
	var embeddingBlob []byte
	if s.embedder != nil {
		if vec := s.embedder.Embed(m.Content); vec != nil {
			embeddingBlob = MarshalEmbedding(vec)
		}
	}

	res, err := s.db.ExecContext(ctx,
		`INSERT INTO memories (content, type, tags, source, bead_id, worker_id, confidence, embedding, files_read, files_modified)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.Content, m.Type, tags, m.Source, m.BeadID, m.WorkerID, conf, embeddingBlob, filesRead, filesModified,
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
		if jaccardSimilarity(newTerms, existTerms) < DedupJaccardThreshold {
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
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}

// searchSQL builds the FTS5 search SQL and args for the given query and opts.
func searchSQL(query string, opts SearchOpts) (stmt string, args []any) {
	limit := opts.Limit
	if limit <= 0 {
		limit = 10
	}

	conditions := []string{"memories_fts MATCH ?"}
	args = []any{sanitizeFTS5Query(query)}

	if opts.Type != "" {
		conditions = append(conditions, "m.type = ?")
		args = append(args, opts.Type)
	}
	if opts.FilePath != "" {
		conditions = append(conditions, "(m.files_read LIKE ? OR m.files_modified LIKE ?)")
		pattern := "%" + opts.FilePath + "%"
		args = append(args, pattern, pattern)
	}

	// Use FTS5 rank for relevance ordering; compute score in Go.
	q := fmt.Sprintf(`
		SELECT m.id, m.content, m.type, m.tags, m.source,
		       COALESCE(m.bead_id, '') AS bead_id,
		       COALESCE(m.worker_id, '') AS worker_id,
		       m.confidence, m.created_at, m.embedding,
		       COALESCE(m.files_read, '[]') AS files_read,
		       COALESCE(m.files_modified, '[]') AS files_modified,
		       (julianday('now') - julianday(m.created_at)) AS age_days
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

	q, args := searchSQL(query, opts)
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
//
//oro:testonly
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

// vectorSearch retrieves memories and ranks them by cosine similarity to queryVec.
//
//nolint:funlen // extra lines from file tracking columns in SELECT
func (s *Store) vectorSearch(ctx context.Context, queryVec []float32, limit int, typeFilter string) ([]ScoredMemory, error) {
	if len(queryVec) == 0 {
		return nil, nil
	}

	// Fetch all memories that have embeddings.
	q := `SELECT id, content, type, tags, source,
	       COALESCE(bead_id, '') AS bead_id,
	       COALESCE(worker_id, '') AS worker_id,
	       confidence, created_at, embedding,
	       COALESCE(files_read, '[]') AS files_read, COALESCE(files_modified, '[]') AS files_modified,
	       (julianday('now') - julianday(created_at)) AS age_days
	FROM memories
	WHERE embedding IS NOT NULL`

	var args []any
	if typeFilter != "" {
		q += " AND type = ?"
		args = append(args, typeFilter)
	}

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
	if err := rows.Scan(
		&sm.ID, &sm.Content, &sm.Type, &sm.Tags, &sm.Source,
		&sm.BeadID, &sm.WorkerID, &sm.Confidence, &sm.CreatedAt,
		&embedding, &sm.FilesRead, &sm.FilesModified, &ageDays,
	); err != nil {
		return sm, fmt.Errorf("memory search scan: %w", err)
	}
	if embedding.Valid {
		sm.Embedding = []byte(embedding.String)
	}
	// Score: confidence * time_decay (halves every 30 days)
	sm.Score = sm.Confidence * math.Pow(0.5, ageDays/30.0)
	return sm, nil
}

// sanitizeFTS5Query wraps each term in double quotes to prevent FTS5 operator
// interpretation (e.g., "and", "or", "not" are FTS5 operators) and joins them
// with OR for broader recall. FTS5 implicit AND requires all terms to appear,
// which is too restrictive for fuzzy memory search.
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

// listSQL builds the list query SQL and args.
func listSQL(opts ListOpts, limit int) (query string, args []any) {
	var conditions []string

	if opts.Type != "" {
		conditions = append(conditions, "type = ?")
		args = append(args, opts.Type)
	}
	if opts.Tag != "" {
		conditions = append(conditions, `tags LIKE ?`)
		args = append(args, fmt.Sprintf(`%%%q%%`, opts.Tag))
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
		       COALESCE(files_modified, '[]') AS files_modified
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
	if err := rows.Scan(
		&m.ID, &m.Content, &m.Type, &m.Tags, &m.Source,
		&m.BeadID, &m.WorkerID, &m.Confidence, &m.CreatedAt,
		&embedding, &m.FilesRead, &m.FilesModified,
	); err != nil {
		return m, fmt.Errorf("memory list scan: %w", err)
	}
	if embedding.Valid {
		m.Embedding = []byte(embedding.String)
	}
	return m, nil
}

// GetByID retrieves a single memory by its ID.
func (s *Store) GetByID(ctx context.Context, id int64) (protocol.Memory, error) {
	q := `SELECT id, content, type, tags, source,
	       COALESCE(bead_id, '') AS bead_id,
	       COALESCE(worker_id, '') AS worker_id,
	       confidence, created_at, embedding,
	       COALESCE(files_read, '[]') AS files_read,
	       COALESCE(files_modified, '[]') AS files_modified
	FROM memories WHERE id = ?`

	var m protocol.Memory
	var embedding sql.NullString
	err := s.db.QueryRowContext(ctx, q, id).Scan(
		&m.ID, &m.Content, &m.Type, &m.Tags, &m.Source,
		&m.BeadID, &m.WorkerID, &m.Confidence, &m.CreatedAt,
		&embedding, &m.FilesRead, &m.FilesModified,
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
	_, err := s.db.ExecContext(ctx,
		`UPDATE memories SET confidence = ? WHERE id = ?`,
		confidence, id,
	)
	if err != nil {
		return fmt.Errorf("memory update confidence: %w", err)
	}
	return nil
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

// implicitPatterns maps regex patterns to memory types for implicit extraction.
//
//nolint:gochecknoglobals // compile-once regex table, safe as package-level var
var implicitPatterns = []struct {
	re      *regexp.Regexp
	memType string
}{
	{regexp.MustCompile(`(?i)I learned\s+(.+?)\.?\s*$`), "lesson"},
	{regexp.MustCompile(`(?i)^Note:\s+(.+?)\.?\s*$`), "lesson"},
	{regexp.MustCompile(`(?i)^Gotcha:\s+(.+?)\.?\s*$`), "gotcha"},
	{regexp.MustCompile(`(?i)^Pattern:\s+(.+?)\.?\s*$`), "pattern"},
	{regexp.MustCompile(`(?i)^Decision:\s+(.+?)\.?\s*$`), "decision"},
	{regexp.MustCompile(`(?i)^Decided:\s+(.+?)\.?\s*$`), "decision"},
	{regexp.MustCompile(`(?i)^Important:\s+(.+?)\.?\s*$`), "lesson"},
}

// ExtractImplicit scans text for implicit learning patterns and returns
// InsertParams for each match. Does NOT insert — caller decides.
func ExtractImplicit(text string) []InsertParams {
	lines := strings.Split(text, "\n")
	var results []InsertParams

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		for _, p := range implicitPatterns {
			m := p.re.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			content := strings.TrimSpace(m[1])
			if content == "" {
				continue
			}
			results = append(results, InsertParams{
				Content:    content,
				Type:       p.memType,
				Source:     "daemon_extracted",
				Confidence: 0.6,
			})
			break // one match per line
		}
	}

	return results
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

	results, err := store.Search(ctx, beadDesc, SearchOpts{
		Limit: 10,
		Tags:  beadTags,
	})
	if err != nil {
		return "", fmt.Errorf("for prompt search: %w", err)
	}

	if len(results) == 0 {
		return "", nil
	}

	// Build compact table format.
	lines := []string{
		"## Relevant Memories",
		"| ID | Type | Title | Tokens |",
		"|----|------|-------|--------|",
	}

	count := 0
	for _, m := range results {
		if count >= maxInjectedMemories {
			break
		}

		// Truncate content to create a title (max 50 chars)
		title := m.Content
		if len(title) > 50 {
			title = title[:47] + "..."
		}

		// Estimate token count for full content
		tokens := estimateTokens(m.Content)

		line := fmt.Sprintf("| %d | %s | %s | ~%d |", m.ID, m.Type, title, tokens)
		lines = append(lines, line)
		count++
	}

	if count == 0 {
		return "", nil
	}

	lines = append(lines, "", "Use `oro recall --id=N` to fetch full memory content.")

	return strings.Join(lines, "\n"), nil
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
func formatAge(createdAt string) string {
	// Use SQL to calculate — but since we have the string, do a simple parse.
	// created_at is in "YYYY-MM-DD HH:MM:SS" format from SQLite datetime('now').
	// For simplicity, return the raw date. A production version would calculate days.
	if len(createdAt) >= 10 {
		return createdAt[:10]
	}
	return createdAt
}

// Consolidate deduplicates and prunes the memory store.
// - Finds pairs with high FTS5 similarity (BM25 score above threshold)
// - Merges content of duplicates, keeping higher confidence
// - Prunes memories with decayed score below minScore
// Returns count of merged and pruned memories.
//
//oro:testonly
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
func pruneStale(ctx context.Context, store *Store, minScore float64, dryRun bool) (int, error) {
	q := `
		SELECT id, confidence,
		       (julianday('now') - julianday(created_at)) AS age_days
		FROM memories
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
func mergePair(ctx context.Context, store *Store, a, b protocol.Memory) error {
	keepID, removeID := a.ID, b.ID
	keepConf, removeConf := a.Confidence, b.Confidence

	if removeConf > keepConf {
		keepID, removeID = removeID, keepID
		keepConf = removeConf
	}

	if err := store.UpdateConfidence(ctx, keepID, math.Max(keepConf, removeConf)); err != nil {
		return fmt.Errorf("merge update confidence: %w", err)
	}

	if err := store.Delete(ctx, removeID); err != nil {
		return fmt.Errorf("merge delete duplicate: %w", err)
	}

	return nil
}

func mergeDuplicates(ctx context.Context, store *Store, threshold float64, dryRun bool) (int, error) {
	all, err := store.List(ctx, ListOpts{Limit: 1000})
	if err != nil {
		return 0, fmt.Errorf("merge duplicates list: %w", err)
	}

	merged := 0
	deleted := make(map[int64]bool)

	for i := range all {
		if deleted[all[i].ID] {
			continue
		}

		similar, err := store.Search(ctx, all[i].Content, SearchOpts{Limit: 5})
		if err != nil {
			continue
		}

		for _, s := range similar {
			if s.ID == all[i].ID || deleted[s.ID] || s.Score < threshold {
				continue
			}

			if !dryRun {
				if err := mergePair(ctx, store, all[i], s.Memory); err != nil {
					return merged, err
				}
			}

			merged++
			deleted[s.ID] = true
		}
	}

	return merged, nil
}
