//go:build !cgo

package embeddings

// Embed returns a deterministic 384-dimensional stub embedding for text.
func (e *ONNXEmbedder) Embed(text string) []float32 {
	return deterministicVector(text, e.Dim())
}

// Dim returns the bge-small-en-v1.5 embedding dimension.
func (e *ONNXEmbedder) Dim() int {
	return bgeSmallDim
}

// Name returns the backing embedding model name.
func (e *ONNXEmbedder) Name() string {
	return bgeSmallName
}
