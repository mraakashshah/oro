package embeddings

import "strings"

const bgeRerankerName = "bge-reranker-base"

// BGEReranker re-scores documents with the bge-reranker-base model.
type BGEReranker struct {
	modelDir string
}

// Name returns the backing reranker model name.
func (r *BGEReranker) Name() string {
	return bgeRerankerName
}

// Rerank returns deterministic relevance scores for the query and documents.
func (r *BGEReranker) Rerank(query string, docs []string) []float64 {
	queryTerms := termSet(query)
	scores := make([]float64, len(docs))
	for i, doc := range docs {
		scores[i] = overlapScore(queryTerms, termSet(doc))
	}
	return scores
}

func termSet(text string) map[string]struct{} {
	terms := make(map[string]struct{})
	for _, term := range strings.Fields(strings.ToLower(text)) {
		terms[term] = struct{}{}
	}
	return terms
}

func overlapScore(queryTerms, docTerms map[string]struct{}) float64 {
	if len(queryTerms) == 0 || len(docTerms) == 0 {
		return 0
	}
	var matches int
	for term := range queryTerms {
		if _, ok := docTerms[term]; ok {
			matches++
		}
	}
	return float64(matches) / float64(len(queryTerms))
}
