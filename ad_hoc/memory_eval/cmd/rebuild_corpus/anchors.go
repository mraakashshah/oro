//go:build cgo && darwin

package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"unicode"
)

// CorpusAnchor is one memory entry selected as a retrieval ground-truth anchor.
type CorpusAnchor struct {
	ID      int64  `json:"id"`
	Type    string `json:"type"`
	Content string `json:"content"`
}

// countTokens returns the number of letter/digit token runs in s, matching the
// tokenizer in pkg/memory/embed.go (splits on non-letter and non-digit runes).
func countTokens(s string) int {
	count := 0
	inToken := false
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if !inToken {
				count++
				inToken = true
			}
		} else {
			inToken = false
		}
	}
	return count
}

// filterByTokenCount returns anchors where countTokens(content) <= max.
func filterByTokenCount(anchors []CorpusAnchor, max int) []CorpusAnchor {
	filtered := make([]CorpusAnchor, 0, len(anchors))
	for _, a := range anchors {
		if countTokens(a.Content) <= max {
			filtered = append(filtered, a)
		}
	}
	return filtered
}

// SelectAnchors returns count anchor memories from db, selected deterministically
// by seed. Rows are ordered by (id * 2654435761 + seed) % (1<<31). SQL filters
// content length to [50, 400]; Go filters token count to <=512. Returns an error
// if fewer than count candidates pass all filters or if db is empty.
func SelectAnchors(db *sql.DB, seed int64, count int) ([]CorpusAnchor, error) {
	const query = `
		SELECT id, content, type
		FROM memories
		WHERE length(content) BETWEEN 50 AND 400
		ORDER BY (id * 2654435761 + ?) % 2147483648
	`
	rows, err := db.Query(query, seed)
	if err != nil {
		return nil, fmt.Errorf("select anchors: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var raw []CorpusAnchor
	for rows.Next() {
		var a CorpusAnchor
		if err := rows.Scan(&a.ID, &a.Content, &a.Type); err != nil {
			return nil, fmt.Errorf("scan anchor: %w", err)
		}
		raw = append(raw, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate anchors: %w", err)
	}

	candidates := filterByTokenCount(raw, 512)
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no anchor candidates in DB")
	}
	if len(candidates) < count {
		return nil, fmt.Errorf("insufficient anchor candidates: got %d, need %d", len(candidates), count)
	}

	return candidates[:count], nil
}

// WriteCorpusAnchors atomically writes anchors as JSONL to path by writing a
// temp file then renaming it. Each line is one JSON-encoded CorpusAnchor.
func WriteCorpusAnchors(path string, anchors []CorpusAnchor) error {
	tmp := path + ".tmp"
	f, err := os.Create(tmp) //nolint:gosec // path is caller-supplied, not user input
	if err != nil {
		return fmt.Errorf("create tmp: %w", err)
	}

	enc := json.NewEncoder(f)
	for _, a := range anchors {
		if err := enc.Encode(a); err != nil {
			_ = f.Close()
			_ = os.Remove(tmp)
			return fmt.Errorf("encode anchor %d: %w", a.ID, err)
		}
	}

	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}
