package memory

import (
	"context"
	"encoding/json"
	"fmt"
)

// SearchEvent records the inputs and outputs of a single hybrid-search call.
type SearchEvent struct {
	Project       string
	QueryHash     string
	TopKIDs       []int64
	TopKScores    []float64
	LatencyMs     int
	UsedRerank    bool
	UsedBGE       bool
	ANNCandidates int
}

// logSearchEvent inserts one row into memory_search_events.
// The caller is responsible for ensuring the table exists (migration run at store open).
func (s *Store) logSearchEvent(ctx context.Context, evt SearchEvent) error {
	ids, err := json.Marshal(evt.TopKIDs)
	if err != nil {
		return fmt.Errorf("marshal top_k_ids: %w", err)
	}
	scores, err := json.Marshal(evt.TopKScores)
	if err != nil {
		return fmt.Errorf("marshal top_k_scores: %w", err)
	}

	boolToInt := func(b bool) int {
		if b {
			return 1
		}
		return 0
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO memory_search_events
			(project, query_hash, top_k_ids, top_k_scores, latency_ms, used_rerank, used_bge, ann_candidates)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`,
		evt.Project,
		evt.QueryHash,
		string(ids),
		string(scores),
		evt.LatencyMs,
		boolToInt(evt.UsedRerank),
		boolToInt(evt.UsedBGE),
		evt.ANNCandidates,
	)
	if err != nil {
		return fmt.Errorf("insert memory_search_events: %w", err)
	}
	return nil
}
