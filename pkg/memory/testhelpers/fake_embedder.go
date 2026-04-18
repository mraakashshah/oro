// Package testhelpers provides test doubles for the memory package.
package testhelpers

import (
	"hash/fnv"
	"math"
	"strings"
	"unicode"
)

const defaultDim = 128

// FakeEmbedder produces deterministic fixed-dimension embeddings via FNV-32
// hash-trick projection. Cosine similarity between two embeddings approximates
// the Jaccard similarity of their token sets (Ochiai coefficient). Deterministic
// across runs — no RNG, no external deps.
//
// Intentionally does NOT implement VocabPersister; exercises the Store
// type-assertion no-op path from oro-ot51.
type FakeEmbedder struct {
	dim int
}

// NewFakeEmbedder creates a FakeEmbedder. If dim is 0, defaults to 128.
//
//oro:testonly
func NewFakeEmbedder(dim int) *FakeEmbedder {
	if dim == 0 {
		dim = defaultDim
	}
	return &FakeEmbedder{dim: dim}
}

// Embed computes a hash-trick projection vector for text.
// Each token is hashed with FNV-32a; its bucket (hash % Dim) is incremented.
// The result is L2-normalized. Empty input returns nil.
func (f *FakeEmbedder) Embed(text string) []float32 {
	tokens := tokenize(text)
	if len(tokens) == 0 {
		return nil
	}

	vec := make([]float32, f.dim)
	for _, tok := range tokens {
		h := fnv.New32a()
		_, _ = h.Write([]byte(tok))
		idx := int(h.Sum32()) % f.dim
		vec[idx]++
	}

	normalize32(vec)
	return vec
}

// Dim returns the embedding dimension.
func (f *FakeEmbedder) Dim() int { return f.dim }

// Name returns the embedder identifier.
func (f *FakeEmbedder) Name() string { return "fake-jaccard" }

// tokenize splits text into lowercase alphanumeric tokens.
func tokenize(text string) []string {
	lower := strings.ToLower(text)
	return strings.FieldsFunc(lower, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

// normalize32 normalizes a float32 vector to unit length in place.
func normalize32(v []float32) {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	norm := math.Sqrt(sum)
	if norm == 0 {
		return
	}
	for i := range v {
		v[i] = float32(float64(v[i]) / norm)
	}
}
