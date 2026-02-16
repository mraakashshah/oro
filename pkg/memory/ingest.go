package memory

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// knowledgeEntry represents one line from knowledge.jsonl.
type knowledgeEntry struct {
	Key     string   `json:"key"`
	Type    string   `json:"type"`
	Content string   `json:"content"`
	Bead    string   `json:"bead"`
	Tags    []string `json:"tags"`
	TS      string   `json:"ts"`
}

// IngestKnowledge reads JSONL entries from r, maps them to InsertParams,
// and inserts into the store. Deduplicates by key prefix stored in source field.
// Returns the count of newly inserted entries (skipping duplicates).
//
//oro:testonly — wired into production by oro-o84l (oro ingest CLI command)
func IngestKnowledge(ctx context.Context, store *Store, r io.Reader) (int, error) {
	scanner := bufio.NewScanner(r)
	newCount := 0

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var entry knowledgeEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			// Skip malformed lines
			continue
		}

		// Validate required fields
		if entry.Content == "" || entry.Key == "" {
			// Skip entries missing required fields
			continue
		}

		// Check for duplicate by key prefix in source field (best-effort)
		isDuplicate, _ := checkKeyDuplicate(ctx, store, entry.Key)
		if isDuplicate {
			// Skip duplicate entry
			continue
		}

		// Map JSONL entry to InsertParams
		params := InsertParams{
			Content:    entry.Content,
			Type:       mapType(entry.Type),
			Tags:       entry.Tags,
			Source:     "knowledge_import:" + entry.Key,
			BeadID:     entry.Bead,
			Confidence: 0.9,
			Pinned:     false,
		}

		// Parse timestamp and set created_at if valid
		// (Store.Insert uses current time by default, but we could add CreatedAt to InsertParams later)

		if _, err := store.Insert(ctx, params); err != nil {
			// Insert failed - skip this entry but continue processing others
			continue
		}

		newCount++
	}

	if err := scanner.Err(); err != nil {
		return newCount, fmt.Errorf("scan jsonl: %w", err)
	}

	return newCount, nil
}

// checkKeyDuplicate queries for existing entries with source matching the key.
// Returns true if a duplicate exists.
func checkKeyDuplicate(ctx context.Context, store *Store, key string) (bool, error) {
	sourcePrefix := "knowledge_import:" + key

	// Query for existing entries with this source
	rows, err := store.db.QueryContext(ctx,
		`SELECT id FROM memories WHERE source = ? LIMIT 1`,
		sourcePrefix,
	)
	if err != nil {
		return false, fmt.Errorf("query duplicate check: %w", err)
	}
	defer rows.Close()

	return rows.Next(), nil
}

// mapType maps JSONL type to memory store type.
// "learned" → "lesson", others pass through.
func mapType(jsonlType string) string {
	if jsonlType == "learned" {
		return "lesson"
	}
	return jsonlType
}
