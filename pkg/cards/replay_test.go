package cards_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"oro/pkg/cards"
)

type recallReplayFixture struct {
	Cards           []recallReplayCard `json:"cards"`
	Query           string             `json:"query"`
	ExpectedTopKIDs []string           `json:"expected_top_k_ids"`
}

type recallReplayCard struct {
	ID          string   `json:"id"`
	Type        string   `json:"type"`
	Title       string   `json:"title"`
	BodySummary string   `json:"body_summary"`
	BodyFull    string   `json:"body_full"`
	Tags        []string `json:"tags"`
}

func TestRecallReplay_HitRate(t *testing.T) {
	fixtures := loadRecallReplayFixtures(t)
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			ctx := context.Background()
			store := newTestStore(t)
			for _, card := range fixture.data.Cards {
				mustCreate(t, store, cards.CardCreateParams{
					ID:          card.ID,
					Type:        cards.CardType(card.Type),
					Title:       card.Title,
					BodySummary: card.BodySummary,
					BodyFull:    card.BodyFull,
					Tags:        card.Tags,
				})
			}

			baseline := replayRelevantIDs(ctx, t, store, fixture.data.Query, 0)
			phase1 := replayRelevantIDs(ctx, t, store, fixture.data.Query, 0.25)
			k := len(fixture.data.ExpectedTopKIDs)

			baselineRecall := recallAtK(baseline, fixture.data.ExpectedTopKIDs, k)
			phase1Recall := recallAtK(phase1, fixture.data.ExpectedTopKIDs, k)
			if phase1Recall < baselineRecall {
				t.Fatalf("phase1 recall@%d = %.3f, baseline = %.3f\nbaseline=%v\nphase1=%v\nexpected=%v",
					k, phase1Recall, baselineRecall, baseline, phase1, fixture.data.ExpectedTopKIDs)
			}
			if phase1Recall == 0 {
				t.Fatalf("phase1 recall@%d = 0, expected at least one replay hit\nphase1=%v\nexpected=%v",
					k, phase1, fixture.data.ExpectedTopKIDs)
			}

			baselineMRR := mrr(baseline, fixture.data.ExpectedTopKIDs)
			phase1MRR := mrr(phase1, fixture.data.ExpectedTopKIDs)
			if phase1MRR < baselineMRR {
				t.Fatalf("phase1 MRR = %.3f, baseline = %.3f\nbaseline=%v\nphase1=%v\nexpected=%v",
					phase1MRR, baselineMRR, baseline, phase1, fixture.data.ExpectedTopKIDs)
			}
		})
	}
}

type namedRecallReplayFixture struct {
	name string
	data recallReplayFixture
}

func loadRecallReplayFixtures(t *testing.T) []namedRecallReplayFixture {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("testdata", "recall_replay", "*.json"))
	if err != nil {
		t.Fatalf("glob recall replay fixtures: %v", err)
	}
	if len(paths) == 0 {
		t.Fatal("no recall replay fixtures found")
	}

	fixtures := make([]namedRecallReplayFixture, 0, len(paths))
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read recall replay fixture %s: %v", path, err)
		}
		var fixture recallReplayFixture
		if err := json.Unmarshal(raw, &fixture); err != nil {
			t.Fatalf("parse recall replay fixture %s: %v", path, err)
		}
		if len(fixture.Cards) == 0 || fixture.Query == "" || len(fixture.ExpectedTopKIDs) == 0 {
			t.Fatalf("fixture %s must define cards, query, and expected_top_k_ids", path)
		}
		fixtures = append(fixtures, namedRecallReplayFixture{
			name: filepath.Base(path),
			data: fixture,
		})
	}
	return fixtures
}

func replayRelevantIDs(
	ctx context.Context,
	t *testing.T,
	store *cards.SQLiteCardStore,
	query string,
	wSeeAlso float64,
) []string {
	t.Helper()
	got, err := store.Relevant(ctx, cards.RelevanceQuery{
		BeadDescription: query,
		MaxTokens:       1000,
		IncludeLowScore: true,
		WSeeAlso:        wSeeAlso,
	})
	if err != nil {
		t.Fatalf("Relevant(%q): %v", query, err)
	}
	ids := make([]string, 0, len(got.Deck))
	for _, card := range got.Deck {
		ids = append(ids, card.ID)
	}
	return ids
}

func recallAtK(rankedIDs, expectedIDs []string, k int) float64 {
	if k <= 0 || len(expectedIDs) == 0 {
		return 0
	}
	expected := make(map[string]bool, len(expectedIDs))
	for _, id := range expectedIDs {
		expected[id] = true
	}
	limit := k
	if len(rankedIDs) < limit {
		limit = len(rankedIDs)
	}
	hits := 0
	for _, id := range rankedIDs[:limit] {
		if expected[id] {
			hits++
		}
	}
	return float64(hits) / float64(len(expectedIDs))
}

func mrr(rankedIDs, expectedIDs []string) float64 {
	expected := make(map[string]bool, len(expectedIDs))
	for _, id := range expectedIDs {
		expected[id] = true
	}
	for rank, id := range rankedIDs {
		if expected[id] {
			return 1.0 / float64(rank+1)
		}
	}
	return 0
}
