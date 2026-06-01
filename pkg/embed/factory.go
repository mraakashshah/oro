package embeddings

// Embedder computes dense embedding vectors for semantic recall.
type Embedder interface {
	Embed(text string) []float32
	Dim() int
	Name() string
}

// Reranker re-scores candidate documents against a query.
type Reranker interface {
	Rerank(query string, docs []string) []float64
}

// NewEmbedder returns the bge-small-en-v1.5 embedder.
func NewEmbedder(modelDir string) (Embedder, error) {
	return &ONNXEmbedder{modelDir: modelDir}, nil
}

// NewReranker returns the bge-reranker-base reranker.
func NewReranker(modelDir string) (Reranker, error) {
	return &BGEReranker{modelDir: modelDir}, nil
}
