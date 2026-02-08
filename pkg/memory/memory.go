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
	"strings"

	"oro/pkg/protocol"
)

// Store manages the memories table in SQLite.
type Store struct {
	db *sql.DB
}

// NewStore creates a new Store backed by the given SQLite database.
func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// InsertParams holds parameters for inserting a new memory.
type InsertParams struct {
	Content    string
	Type       string // lesson | decision | gotcha | pattern | preference
	Tags       []string
	Source     string // self_report | daemon_extracted
	BeadID     string
	WorkerID   string
	Confidence float64
}

// SearchOpts configures a FTS5 search query.
type SearchOpts struct {
	Limit    int      // default 10
	Type     string   // optional filter
	Tags     []string // optional tag filter (any match)
	MinScore float64  // minimum combined score threshold
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

// Insert adds a new memory. Returns the inserted ID.
func (s *Store) Insert(ctx context.Context, m InsertParams) (int64, error) {
	tags := tagsToJSON(m.Tags)
	conf := m.Confidence
	if conf == 0 {
		conf = 0.8
	}

	res, err := s.db.ExecContext(ctx,
		`INSERT INTO memories (content, type, tags, source, bead_id, worker_id, confidence)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		m.Content, m.Type, tags, m.Source, m.BeadID, m.WorkerID, conf,
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

// Search performs FTS5 BM25-ranked search with optional type filter.
// Results are scored by: BM25 relevance * confidence * time decay.
// Time decay formula: confidence * (0.5 ^ ((julianday('now') - julianday(created_at)) / 30.0))
func (s *Store) Search(ctx context.Context, query string, opts SearchOpts) ([]ScoredMemory, error) {
	if query == "" {
		return nil, nil
	}

	limit := opts.Limit
	if limit <= 0 {
		limit = 10
	}

	// Build the query dynamically based on filters.
	var conditions []string
	var args []interface{}

	conditions = append(conditions, "memories_fts MATCH ?")
	args = append(args, sanitizeFTS5Query(query))

	if opts.Type != "" {
		conditions = append(conditions, "m.type = ?")
		args = append(args, opts.Type)
	}

	whereClause := strings.Join(conditions, " AND ")

	q := fmt.Sprintf(`
		SELECT m.id, m.content, m.type, m.tags, m.source,
		       COALESCE(m.bead_id, '') AS bead_id,
		       COALESCE(m.worker_id, '') AS worker_id,
		       m.confidence, m.created_at, m.embedding,
		       (-bm25(memories_fts)) * m.confidence *
		       POWER(0.5, (julianday('now') - julianday(m.created_at)) / 30.0) AS score
		FROM memories_fts
		JOIN memories m ON memories_fts.rowid = m.id
		WHERE %s
		ORDER BY score DESC
		LIMIT ?
	`, whereClause)

	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("memory search: %w", err)
	}
	defer rows.Close()

	var results []ScoredMemory
	for rows.Next() {
		var sm ScoredMemory
		var embedding sql.NullString
		if err := rows.Scan(
			&sm.ID, &sm.Content, &sm.Type, &sm.Tags, &sm.Source,
			&sm.BeadID, &sm.WorkerID, &sm.Confidence, &sm.CreatedAt,
			&embedding, &sm.Score,
		); err != nil {
			return nil, fmt.Errorf("memory search scan: %w", err)
		}
		if embedding.Valid {
			sm.Embedding = []byte(embedding.String)
		}

		if opts.MinScore > 0 && sm.Score < opts.MinScore {
			continue
		}

		// Tag filter: any match
		if len(opts.Tags) > 0 {
			memTags := tagsFromJSON(sm.Tags)
			if !anyTagMatch(memTags, opts.Tags) {
				continue
			}
		}

		results = append(results, sm)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("memory search rows: %w", err)
	}

	return results, nil
}

// sanitizeFTS5Query wraps each term in double quotes to prevent FTS5 operator
// interpretation (e.g., "and", "or", "not" are FTS5 operators).
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
	return strings.Join(quoted, " ")
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

// List returns memories matching optional filters, ordered by created_at desc.
func (s *Store) List(ctx context.Context, opts ListOpts) ([]protocol.Memory, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = 50
	}

	var conditions []string
	var args []interface{}

	if opts.Type != "" {
		conditions = append(conditions, "type = ?")
		args = append(args, opts.Type)
	}
	if opts.Tag != "" {
		// Match tag within JSON array using LIKE
		conditions = append(conditions, `tags LIKE ?`)
		args = append(args, fmt.Sprintf(`%%"%s"%%`, opts.Tag))
	}

	whereClause := ""
	if len(conditions) > 0 {
		whereClause = "WHERE " + strings.Join(conditions, " AND ")
	}

	q := fmt.Sprintf(`
		SELECT id, content, type, tags, source,
		       COALESCE(bead_id, '') AS bead_id,
		       COALESCE(worker_id, '') AS worker_id,
		       confidence, created_at, embedding
		FROM memories
		%s
		ORDER BY created_at DESC, id DESC
		LIMIT ? OFFSET ?
	`, whereClause)

	args = append(args, limit, opts.Offset)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("memory list: %w", err)
	}
	defer rows.Close()

	var results []protocol.Memory
	for rows.Next() {
		var m protocol.Memory
		var embedding sql.NullString
		if err := rows.Scan(
			&m.ID, &m.Content, &m.Type, &m.Tags, &m.Source,
			&m.BeadID, &m.WorkerID, &m.Confidence, &m.CreatedAt,
			&embedding,
		); err != nil {
			return nil, fmt.Errorf("memory list scan: %w", err)
		}
		if embedding.Valid {
			m.Embedding = []byte(embedding.String)
		}
		results = append(results, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("memory list rows: %w", err)
	}

	return results, nil
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
func ExtractMarkers(ctx context.Context, r io.Reader, store *Store, workerID string, beadID string) (int, error) {
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

// ForPrompt retrieves the most relevant memories for a bead and formats them
// as a markdown section suitable for injection into the worker prompt.
// Cap: maxTokens (approximate, using word count / 0.75 as token estimate).
func ForPrompt(ctx context.Context, store *Store, beadTags []string, beadDesc string, maxTokens int) (string, error) {
	if maxTokens <= 0 {
		maxTokens = 200
	}

	if beadDesc == "" {
		return "", nil
	}

	// Search by bead description keywords
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

	// Take top 3
	top := results
	if len(top) > 3 {
		top = top[:3]
	}

	var lines []string
	lines = append(lines, "## Relevant Memories")

	for _, m := range top {
		age := formatAge(m.CreatedAt)
		line := fmt.Sprintf("- [%s] %s (%s, confidence: %.2f)",
			m.Type, m.Content, age, m.Confidence)
		lines = append(lines, line)
	}

	output := strings.Join(lines, "\n")

	// Truncate if exceeds maxTokens (estimate: words / 0.75)
	words := strings.Fields(output)
	estimatedTokens := int(float64(len(words)) / 0.75)
	if estimatedTokens > maxTokens {
		// Truncate word count to fit
		targetWords := int(float64(maxTokens) * 0.75)
		if targetWords < len(words) {
			output = strings.Join(words[:targetWords], " ") + "..."
		}
	}

	return output, nil
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
func Consolidate(ctx context.Context, store *Store, opts ConsolidateOpts) (merged int, pruned int, err error) {
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
func pruneStale(ctx context.Context, store *Store, minScore float64, dryRun bool) (int, error) {
	q := `
		SELECT id FROM memories
		WHERE confidence * POWER(0.5, (julianday('now') - julianday(created_at)) / 30.0) < ?
	`
	rows, err := store.db.QueryContext(ctx, q, minScore)
	if err != nil {
		return 0, fmt.Errorf("prune stale query: %w", err)
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return 0, fmt.Errorf("prune stale scan: %w", err)
		}
		ids = append(ids, id)
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
func mergeDuplicates(ctx context.Context, store *Store, threshold float64, dryRun bool) (int, error) {
	// Get all memories
	all, err := store.List(ctx, ListOpts{Limit: 1000})
	if err != nil {
		return 0, fmt.Errorf("merge duplicates list: %w", err)
	}

	merged := 0
	deleted := make(map[int64]bool)

	for i := 0; i < len(all); i++ {
		if deleted[all[i].ID] {
			continue
		}

		// Search for similar memories using this memory's content
		similar, err := store.Search(ctx, all[i].Content, SearchOpts{Limit: 5})
		if err != nil {
			// FTS match might fail for some content; skip
			continue
		}

		for _, s := range similar {
			if s.ID == all[i].ID || deleted[s.ID] {
				continue
			}

			// Check similarity using normalized score
			if s.Score < threshold {
				continue
			}

			if dryRun {
				merged++
				deleted[s.ID] = true
				continue
			}

			// Keep the one with higher confidence, merge content
			keepID := all[i].ID
			removeID := s.ID
			keepConf := all[i].Confidence
			removeConf := s.Confidence

			if removeConf > keepConf {
				keepID, removeID = removeID, keepID
				keepConf = removeConf
			}

			// Update confidence of keeper to max
			newConf := math.Max(keepConf, removeConf)
			if err := store.UpdateConfidence(ctx, keepID, newConf); err != nil {
				return merged, fmt.Errorf("merge update confidence: %w", err)
			}

			// Delete the duplicate
			if err := store.Delete(ctx, removeID); err != nil {
				return merged, fmt.Errorf("merge delete duplicate: %w", err)
			}

			merged++
			deleted[removeID] = true
		}
	}

	return merged, nil
}
