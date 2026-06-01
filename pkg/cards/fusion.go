package cards

import (
	"math"
	"sort"
)

// FusionConfig controls rank/cosine card recall fusion.
type FusionConfig struct {
	RRFK         int
	RRFWeight    float64
	CosineWeight float64
	FloorRatio   float64
}

// ScoredCard carries a card plus the ranking signals used by fusion.
type ScoredCard struct {
	Card           Card
	EffectiveScore float64
	Score          float64
	Cosine         float64
}

type cardReranker interface {
	rerankCards(query string, candidates []ScoredCard) ([]float64, error)
}

// TODO(oro-s08-p3c): wire the cross-encoder reranker into semantic card recall.
type cardRerankerFunc func(query string, candidates []ScoredCard) ([]float64, error)

func (f cardRerankerFunc) rerankCards(query string, candidates []ScoredCard) ([]float64, error) {
	return f(query, candidates)
}

type rerankConfig struct {
	Enabled bool
	TopN    int
}

type fusionAccumulator struct {
	card           Card
	effectiveScore float64
	cosine         float64
	rrf            float64
}

func fuse(keyword, vector []ScoredCard, cfg FusionConfig) []ScoredCard {
	if len(vector) == 0 {
		return keyword
	}
	cfg = cfg.withDefaults()
	byID := make(map[string]*fusionAccumulator, len(keyword)+len(vector))
	order := make([]string, 0, len(keyword)+len(vector))
	addRankedCards(byID, &order, keyword, cfg.RRFK)
	addRankedCards(byID, &order, vector, cfg.RRFK)

	out := make([]ScoredCard, 0, len(byID))
	topKeywordScore, floor := keywordFloor(keyword, cfg)
	for _, id := range order {
		acc, ok := byID[id]
		if !ok {
			continue
		}
		score := acc.effectiveScore * (cfg.RRFWeight*acc.rrf + cfg.CosineWeight*acc.cosine)
		if acc.effectiveScore < floor && score >= topKeywordScore {
			score = math.Nextafter(topKeywordScore, 0)
		}
		out = append(out, ScoredCard{
			Card:           acc.card,
			EffectiveScore: acc.effectiveScore,
			Score:          score,
			Cosine:         acc.cosine,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Score > out[j].Score
	})
	return out
}

func rerankTopCandidates(query string, candidates []ScoredCard, cfg rerankConfig, reranker cardReranker) []ScoredCard {
	if !cfg.Enabled || reranker == nil || len(candidates) == 0 {
		return candidates
	}
	topN := cfg.TopN
	if topN <= 0 {
		return candidates
	}
	if topN > len(candidates) {
		topN = len(candidates)
	}
	head := append([]ScoredCard(nil), candidates[:topN]...)
	scores, err := reranker.rerankCards(query, head)
	if err != nil || len(scores) != len(head) {
		return candidates
	}
	for i := range head {
		head[i].Score = scores[i]
	}
	sort.SliceStable(head, func(i, j int) bool {
		return head[i].Score > head[j].Score
	})
	out := make([]ScoredCard, 0, len(candidates))
	out = append(out, head...)
	out = append(out, candidates[topN:]...)
	return out
}

func (cfg FusionConfig) withDefaults() FusionConfig {
	if cfg.RRFK <= 0 {
		cfg.RRFK = 60
	}
	if cfg.RRFWeight == 0 && cfg.CosineWeight == 0 {
		cfg.RRFWeight = 0.7
		cfg.CosineWeight = 0.3
	}
	if cfg.FloorRatio < 0 {
		cfg.FloorRatio = 0
	}
	return cfg
}

func addRankedCards(
	byID map[string]*fusionAccumulator,
	order *[]string,
	cards []ScoredCard,
	rrfK int,
) {
	for i, scored := range cards {
		acc := accumulatorFor(byID, order, scored)
		rrf := 1 / float64(rrfK+i+1)
		acc.rrf += rrf
		if scored.Cosine > acc.cosine {
			acc.cosine = scored.Cosine
		}
	}
}

func accumulatorFor(
	byID map[string]*fusionAccumulator,
	order *[]string,
	scored ScoredCard,
) *fusionAccumulator {
	id := scored.Card.ID
	if acc, ok := byID[id]; ok {
		if acc.effectiveScore == 0 {
			acc.effectiveScore = scored.EffectiveScore
		}
		return acc
	}
	acc := &fusionAccumulator{
		card:           scored.Card,
		effectiveScore: scored.EffectiveScore,
	}
	byID[id] = acc
	*order = append(*order, id)
	return acc
}

func keywordFloor(keyword []ScoredCard, cfg FusionConfig) (topScore, floor float64) {
	if len(keyword) == 0 || cfg.FloorRatio == 0 {
		return 0, 0
	}
	topEffective := keyword[0].EffectiveScore
	topScore = keyword[0].EffectiveScore * cfg.RRFWeight / float64(cfg.RRFK+1)
	for _, scored := range keyword[1:] {
		if scored.EffectiveScore > topEffective {
			topEffective = scored.EffectiveScore
		}
	}
	return topScore, topEffective * cfg.FloorRatio
}
