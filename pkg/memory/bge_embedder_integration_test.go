//go:build integration && cgo && darwin

package memory

import (
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBGEEmbedder384Dim requires ~/.oro/models/bge-small-en-v1.5/model.onnx and tokenizer.json.
func TestBGEEmbedder384Dim(t *testing.T) {
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	modelDir := filepath.Join(home, ".oro", "models", "bge-small-en-v1.5")
	emb, err := NewBGEEmbedder(modelDir)
	require.NoError(t, err, "NewBGEEmbedder must succeed when model files are present")
	defer emb.Close()

	assert.Equal(t, 384, emb.Dim())
	assert.Equal(t, "bge-small-en-v1.5", emb.Name())

	vec := emb.Embed("test sentence")

	require.Len(t, vec, 384, "Embed must return 384-dimensional vector")
	assert.InDelta(t, 1.0, l2Norm(vec), 1e-5, "embedding must be L2-normalized")

	// Empty string must return zero vector without panic.
	zero := emb.Embed("")
	require.Len(t, zero, 384)
	for _, v := range zero {
		assert.Equal(t, float32(0), v)
	}
}

// TestBGEEmbedderSemanticCosine is a regression test for the token_type_ids bug
// (bge_ort_real.go 2026-04-19). When the ORT session was created with only
// [input_ids, attention_mask] and the model declared a third input, ONNX logged
// "Missing Input: token_type_ids" and silently returned degenerate embeddings
// for which the cosine relationships below do not hold. Asserts that embeddings
// actually capture semantic similarity, not just L2-norm to 1.
func TestBGEEmbedderSemanticCosine(t *testing.T) {
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	emb, err := NewBGEEmbedder(filepath.Join(home, ".oro", "models", "bge-small-en-v1.5"))
	require.NoError(t, err)
	defer emb.Close()

	a := emb.Embed("How do I use semantic search in this codebase?")
	b := emb.Embed("What's the retrieval pipeline for memory lookups?")
	c := emb.Embed("apple banana orange")

	cosAB := cosine32(a, b)
	cosAC := cosine32(a, c)
	cosBC := cosine32(b, c)

	// Both A and B are about retrieval; C is about fruit. The semantically
	// aligned pair must outrank either cross-topic pair. This gap was near-zero
	// under the token_type_ids bug.
	assert.Greater(t, cosAB, cosAC, "retrieval-topic pair must outrank retrieval-vs-fruit")
	assert.Greater(t, cosAB, cosBC, "retrieval-topic pair must outrank retrieval-vs-fruit")
	// And BGE's same-topic cosine is typically > 0.7 on unit-norm vectors.
	assert.Greater(t, cosAB, 0.60, "same-topic cosine should exceed 0.60, got %.4f", cosAB)
}

// cosine32 computes cosine similarity of two float32 vectors.
func cosine32(a, b []float32) float64 {
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// l2Norm returns the L2 norm of a float32 vector.
func l2Norm(v []float32) float64 {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	return math.Sqrt(sum)
}
