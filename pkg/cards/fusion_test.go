package cards //nolint:testpackage // white-box tests pin the unexported fuse contract from the Phase 2 spec.

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"testing"
)

func TestRRFFusion_K60(t *testing.T) {
	keyword := []ScoredCard{
		fusionCard("b", 1, 1, 0),
		fusionCard("a", 1, 0.5, 0),
	}
	vector := []ScoredCard{
		fusionCard("b", 1, 0, 0.8),
		fusionCard("c", 1, 0, 0.7),
	}

	got := fuse(keyword, vector, FusionConfig{RRFK: 60, RRFWeight: 1})

	wantB := 1.0/61.0 + 1.0/61.0
	wantA := 1.0 / 62.0
	wantC := 1.0 / 62.0
	assertScore(t, got, "b", wantB)
	assertScore(t, got, "a", wantA)
	assertScore(t, got, "c", wantC)
	assertOrder(t, got, []string{"b", "a", "c"})
}

func TestBlend_0703(t *testing.T) {
	keyword := []ScoredCard{
		fusionCard("a", 1, 1, 0),
	}
	vector := []ScoredCard{
		fusionCard("a", 1, 0, 0.9),
	}

	got := fuse(keyword, vector, FusionConfig{RRFK: 4, RRFWeight: 0.7, CosineWeight: 0.3})

	assertScore(t, got, "a", 0.7*0.4+0.3*0.9)
}

func TestFloorGate_CosineCannotVaultBelowFloor(t *testing.T) {
	keyword := []ScoredCard{
		fusionCard("keyword", 1, 0.2, 0),
		fusionCard("semantic", 0.05, 0.05, 0),
	}
	vector := []ScoredCard{
		fusionCard("semantic", 0.05, 0, 0.99),
	}

	got := fuse(keyword, vector, FusionConfig{
		RRFK:         60,
		RRFWeight:    0.7,
		CosineWeight: 0.3,
		FloorRatio:   0.8,
	})

	assertOrder(t, got, []string{"keyword", "semantic"})
	if scoreFor(got, "semantic") >= scoreFor(got, "keyword") {
		t.Fatalf("semantic score vaulted keyword floor: semantic=%f keyword=%f", scoreFor(got, "semantic"), scoreFor(got, "keyword"))
	}
}

func TestFusion_NilEmbedderEqualsPhase1(t *testing.T) {
	keyword := []ScoredCard{
		fusionCard("a", 1, 0.9, 0),
		fusionCard("b", 1, 0.4, 0),
	}

	got := fuse(keyword, nil, FusionConfig{})

	if !reflect.DeepEqual(got, keyword) {
		t.Fatalf("nil vector fusion changed keyword results:\ngot  %#v\nwant %#v", got, keyword)
	}
}

func TestFusion_DefaultsAndFloorOff(t *testing.T) {
	keyword := []ScoredCard{
		fusionCard("keyword", 1, 0.2, 0),
		fusionCard("semantic", 0, 0.05, 0),
	}
	vector := []ScoredCard{
		fusionCard("semantic", 0.05, 0, 0.99),
	}

	got := fuse(keyword, vector, FusionConfig{FloorRatio: -1})

	assertOrder(t, got, []string{"semantic", "keyword"})
	assertScore(t, got, "keyword", 0.7/61.0)
	assertScore(t, got, "semantic", 0.05*(0.7*(1.0/62.0+1.0/61.0)+0.3*0.99))
}

func TestRerank_FailOpenPreservesTail(t *testing.T) {
	candidates := make([]ScoredCard, 35)
	for i := range candidates {
		candidates[i] = fusionCard(fmt.Sprintf("card-%02d", i+1), 1, float64(35-i), 0)
	}
	reranker := cardRerankerFunc(func(_ string, cards []ScoredCard) ([]float64, error) {
		if len(cards) != 30 {
			t.Fatalf("reranker saw %d candidates, want 30", len(cards))
		}
		scores := make([]float64, len(cards))
		for i := range scores {
			scores[i] = float64(i)
		}
		return scores, nil
	})

	got := rerankTopCandidates("query", candidates, rerankConfig{
		Enabled: true,
		TopN:    30,
	}, reranker)

	wantTop := reverseIDs(candidates[:30])
	wantTail := idsOf(candidates[30:])
	assertOrder(t, got[:30], wantTop)
	assertOrder(t, got[30:], wantTail)

	failed := rerankTopCandidates("query", candidates, rerankConfig{
		Enabled: true,
		TopN:    30,
	}, cardRerankerFunc(func(_ string, _ []ScoredCard) ([]float64, error) {
		return nil, errors.New("reranker unavailable")
	}))

	if !reflect.DeepEqual(failed, candidates) {
		t.Fatalf("reranker error changed candidates:\ngot  %#v\nwant %#v", failed, candidates)
	}
}

func TestRerank_TopNZeroNoop(t *testing.T) {
	candidates := []ScoredCard{
		fusionCard("first", 1, 3, 0),
		fusionCard("second", 1, 2, 0),
		fusionCard("third", 1, 1, 0),
	}
	called := false
	got := rerankTopCandidates("query", candidates, rerankConfig{
		Enabled: true,
		TopN:    0,
	}, cardRerankerFunc(func(_ string, _ []ScoredCard) ([]float64, error) {
		called = true
		return []float64{1, 2, 3}, nil
	}))

	if called {
		t.Fatal("reranker should not be called when TopN is zero")
	}
	if !reflect.DeepEqual(got, candidates) {
		t.Fatalf("TopN zero changed candidates:\ngot  %#v\nwant %#v", got, candidates)
	}
}

func fusionCard(id string, effectiveScore, score, cosine float64) ScoredCard {
	return ScoredCard{
		Card: Card{
			ID: id,
		},
		EffectiveScore: effectiveScore,
		Score:          score,
		Cosine:         cosine,
	}
}

func idsOf(cards []ScoredCard) []string {
	ids := make([]string, len(cards))
	for i := range cards {
		ids[i] = cards[i].Card.ID
	}
	return ids
}

func reverseIDs(cards []ScoredCard) []string {
	ids := idsOf(cards)
	for i, j := 0, len(ids)-1; i < j; i, j = i+1, j-1 {
		ids[i], ids[j] = ids[j], ids[i]
	}
	return ids
}

func assertScore(t *testing.T, cards []ScoredCard, id string, want float64) {
	t.Helper()
	got := scoreFor(cards, id)
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("%s score = %.12f, want %.12f", id, got, want)
	}
}

func assertOrder(t *testing.T, cards []ScoredCard, want []string) {
	t.Helper()
	got := make([]string, len(cards))
	for i := range cards {
		got[i] = cards[i].Card.ID
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

func scoreFor(cards []ScoredCard, id string) float64 {
	for _, card := range cards {
		if card.Card.ID == id {
			return card.Score
		}
	}
	return math.NaN()
}
