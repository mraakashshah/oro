package testhelpers_test

import (
	"math"
	"strings"
	"testing"
	"unicode"

	"oro/pkg/memory"
	"oro/pkg/memory/testhelpers"
)

// TestFakeEmbedderJaccard verifies that FakeEmbedder produces cosine similarities
// that approximate Jaccard similarity between token sets (within 0.15 tolerance —
// the hash-trick produces the Ochiai coefficient which upper-bounds Jaccard).
func TestFakeEmbedderJaccard(t *testing.T) {
	e := testhelpers.NewFakeEmbedder(0)

	// Dim() returns 128 when constructed with 0.
	if d := e.Dim(); d != 128 {
		t.Errorf("Dim() = %d, want 128", d)
	}

	// Name() returns "fake-jaccard".
	if n := e.Name(); n != "fake-jaccard" {
		t.Errorf("Name() = %q, want \"fake-jaccard\"", n)
	}

	// Empty text returns nil (matches TFIDFEmbedder contract).
	if v := e.Embed(""); v != nil {
		t.Errorf("Embed(\"\") = %v, want nil", v)
	}

	// FakeEmbedder must NOT implement VocabPersister.
	// Exercises Store type-assertion no-op path from oro-ot51.
	var iface interface{} = e
	type vocabPersister interface {
		ExportVocab() map[string]int
		ImportVocab(map[string]int)
	}
	if _, ok := iface.(vocabPersister); ok {
		t.Error("FakeEmbedder must NOT implement VocabPersister")
	}

	// Pair 1: similar sentences — high cosine ≈ high Jaccard.
	const a1, b1 = "cat sat on mat", "cats sit on mats"
	v1 := e.Embed(a1)
	v2 := e.Embed(b1)
	sim1 := memory.CosineSimilarity(v1, v2)
	jac1 := jaccardTokens(a1, b1)

	// Pair 2: dissimilar single tokens — low cosine ≈ low Jaccard.
	const a2, b2 = "cat", "dog"
	v3 := e.Embed(a2)
	v4 := e.Embed(b2)
	sim2 := memory.CosineSimilarity(v3, v4)
	jac2 := jaccardTokens(a2, b2)

	// Hash-trick produces Ochiai coefficient ≈ Jaccard; tolerance accounts for
	// systematic overestimation (Ochiai ≥ Jaccard for any A, B).
	const tol = 0.15
	if math.Abs(sim1-jac1) > tol {
		t.Errorf("pair1: cosine=%.3f jaccard=%.3f diff=%.3f > tol=%.2f", sim1, jac1, math.Abs(sim1-jac1), tol)
	}
	if math.Abs(sim2-jac2) > tol {
		t.Errorf("pair2: cosine=%.3f jaccard=%.3f diff=%.3f > tol=%.2f", sim2, jac2, math.Abs(sim2-jac2), tol)
	}

	// High-similarity pair must rank above low-similarity pair.
	if sim1 <= sim2 {
		t.Errorf("expected sim(%q,%q)=%.3f > sim(%q,%q)=%.3f", a1, b1, sim1, a2, b2, sim2)
	}
}

// jaccardTokens computes Jaccard similarity between token sets of two strings.
func jaccardTokens(a, b string) float64 {
	aSet := tokenSet(a)
	bSet := tokenSet(b)

	var intersection int
	union := len(aSet)
	for tok := range bSet {
		if aSet[tok] {
			intersection++
		} else {
			union++
		}
	}
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}

func tokenSet(text string) map[string]bool {
	lower := strings.ToLower(text)
	words := strings.FieldsFunc(lower, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	set := make(map[string]bool, len(words))
	for _, w := range words {
		set[w] = true
	}
	return set
}
