//go:build integration

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

// l2Norm returns the L2 norm of a float32 vector.
func l2Norm(v []float32) float64 {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	return math.Sqrt(sum)
}
