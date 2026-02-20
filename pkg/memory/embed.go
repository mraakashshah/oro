// Package memory — embedding support for semantic search.
package memory

import (
	"encoding/binary"
	"math"
	"strings"
	"unicode"
)

// maxVocabSize caps the number of unique terms in the vocabulary to prevent
// embedding vectors from growing without bound. Once the cap is reached, new
// unseen terms are silently ignored (zero weight). This prevents OOM in
// vectorSearch which loads up to 1000 rows of embeddings into memory.
const maxVocabSize = 10000

// Embedder computes TF-IDF-style term-frequency vectors for text.
// It maintains a vocabulary (term -> dimension index) that grows as new terms
// are encountered. All embeddings share the same vector space defined by the
// vocabulary at the time of computation.
//
// Design note: we use a bag-of-words TF approach (not full TF-IDF with IDF
// weights) because IDF requires a corpus scan that would be expensive at insert
// time. The RRF hybrid scoring is the main innovation here — even simple TF
// vectors provide useful semantic signal when combined with FTS5 BM25 via RRF.
type Embedder struct {
	vocab map[string]int // term -> dimension index
}

// NewEmbedder creates an Embedder with an empty vocabulary.
//
//oro:testonly
func NewEmbedder() *Embedder {
	return &Embedder{vocab: make(map[string]int)}
}

// tokenize splits text into lowercase alphanumeric tokens.
func tokenize(text string) []string {
	lower := strings.ToLower(text)
	words := strings.FieldsFunc(lower, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	return words
}

// Embed computes a TF vector for the given text. New terms are added to the
// vocabulary. Returns a float32 slice normalized to unit length (L2 norm = 1).
// An empty input returns nil.
func (e *Embedder) Embed(text string) []float32 {
	tokens := tokenize(text)
	if len(tokens) == 0 {
		return nil
	}

	// Count term frequencies.
	tf := make(map[string]int, len(tokens))
	for _, t := range tokens {
		tf[t]++
	}

	// Grow vocabulary with new terms, up to the cap.
	for term := range tf {
		if _, ok := e.vocab[term]; !ok {
			if len(e.vocab) >= maxVocabSize {
				continue
			}
			e.vocab[term] = len(e.vocab)
		}
	}

	// Build dense vector. Skip terms not in vocabulary (beyond cap).
	vec := make([]float32, len(e.vocab))
	for term, count := range tf {
		if idx, ok := e.vocab[term]; ok {
			vec[idx] = float32(count)
		}
	}

	// L2-normalize.
	normalize32(vec)
	return vec
}

// VocabSize returns the current vocabulary size (number of dimensions).
//
//oro:testonly
func (e *Embedder) VocabSize() int {
	return len(e.vocab)
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

// CosineSimilarity computes the cosine similarity between two float32 vectors.
// If vectors have different lengths, the shorter one is zero-padded conceptually.
// Returns 0 if either vector is nil or empty.
func CosineSimilarity(a, b []float32) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}

	minLen := len(a)
	if len(b) < minLen {
		minLen = len(b)
	}

	// Use sub-slices to make bounds explicit for the linter.
	aShared := a[:minLen]
	bShared := b[:minLen]

	var dot float64
	var normA, normB float64
	for i, av := range aShared {
		bv := bShared[i]
		dot += float64(av) * float64(bv)
		normA += float64(av) * float64(av)
		normB += float64(bv) * float64(bv)
	}
	// Account for extra dimensions in longer vector.
	for _, av := range a[minLen:] {
		normA += float64(av) * float64(av)
	}
	for _, bv := range b[minLen:] {
		normB += float64(bv) * float64(bv)
	}

	denom := math.Sqrt(normA) * math.Sqrt(normB)
	if denom == 0 {
		return 0
	}
	return dot / denom
}

// MarshalEmbedding serializes a float32 slice to a compact binary BLOB
// using little-endian encoding (4 bytes per float32).
func MarshalEmbedding(v []float32) []byte {
	if len(v) == 0 {
		return nil
	}
	buf := make([]byte, len(v)*4)
	for i, f := range v {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(f))
	}
	return buf
}

// UnmarshalEmbedding deserializes a BLOB back to a float32 slice.
func UnmarshalEmbedding(data []byte) []float32 {
	if len(data) == 0 || len(data)%4 != 0 {
		return nil
	}
	n := len(data) / 4
	v := make([]float32, n)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(data[i*4:]))
	}
	return v
}

// RRFScore computes Reciprocal Rank Fusion score for an item given its ranks
// in two result lists. k is the smoothing constant (typically 60).
// Formula: 1/(k + textRank) + 1/(k + vectorRank)
// Ranks are 1-based. A rank of 0 means the item was not found in that list,
// in which case that component contributes 0 to the score.
func RRFScore(textRank, vectorRank int, k float64) float64 {
	var score float64
	if textRank > 0 {
		score += 1.0 / (k + float64(textRank))
	}
	if vectorRank > 0 {
		score += 1.0 / (k + float64(vectorRank))
	}
	return score
}
