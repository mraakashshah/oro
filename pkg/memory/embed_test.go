package memory //nolint:testpackage // white-box tests for tokenize, normalize32, and internal embed helpers

import (
	"context"
	"math"
	"testing"
)

// ---------------------------------------------------------------------------
// Unit tests for Embedder, CosineSimilarity, Marshal/Unmarshal, RRFScore
// ---------------------------------------------------------------------------

func TestTokenize(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  int // expected token count
	}{
		{"empty", "", 0},
		{"single word", "hello", 1},
		{"mixed case", "Hello World", 2},
		{"punctuation stripped", "hello, world! foo-bar", 4},
		{"numbers kept", "go1.21 sqlite3", 3},
		{"only punctuation", "!@#$%", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tokenize(tt.input)
			if len(got) != tt.want {
				t.Errorf("tokenize(%q) len = %d, want %d (tokens: %v)", tt.input, len(got), tt.want, got)
			}
		})
	}
}

func TestEmbedder_Embed_Basic(t *testing.T) {
	e := NewEmbedder()

	// Empty input returns nil.
	if v := e.Embed(""); v != nil {
		t.Errorf("Embed empty: got %v, want nil", v)
	}

	// Single term should produce a unit vector.
	v := e.Embed("hello")
	if v == nil {
		t.Fatal("Embed('hello') returned nil")
	}
	if e.VocabSize() != 1 {
		t.Errorf("vocab size = %d, want 1", e.VocabSize())
	}
	// L2 norm should be 1.0.
	assertUnitVector(t, v)
}

func TestEmbedder_Embed_MultiTerm(t *testing.T) {
	e := NewEmbedder()
	v := e.Embed("hello world hello")
	if v == nil {
		t.Fatal("Embed returned nil")
	}
	if e.VocabSize() != 2 {
		t.Errorf("vocab size = %d, want 2", e.VocabSize())
	}
	assertUnitVector(t, v)
}

func TestEmbedder_Embed_VocabGrows(t *testing.T) {
	e := NewEmbedder()
	v1 := e.Embed("hello world")
	if e.VocabSize() != 2 {
		t.Errorf("after first embed: vocab = %d, want 2", e.VocabSize())
	}
	v2 := e.Embed("world foo bar")
	if e.VocabSize() != 4 {
		t.Errorf("after second embed: vocab = %d, want 4", e.VocabSize())
	}
	// v2 should be longer than v1 because vocab grew.
	if len(v2) <= len(v1) {
		t.Errorf("v2 len (%d) should be > v1 len (%d) after vocab growth", len(v2), len(v1))
	}
}

func TestCosineSimilarity_Identical(t *testing.T) {
	e := NewEmbedder()
	v := e.Embed("hello world")
	sim := CosineSimilarity(v, v)
	if math.Abs(sim-1.0) > 0.001 {
		t.Errorf("identical vectors: similarity = %f, want ~1.0", sim)
	}
}

func TestCosineSimilarity_Orthogonal(t *testing.T) {
	// Two vectors with no shared terms should have 0 similarity.
	e := NewEmbedder()
	v1 := e.Embed("hello world")
	v2 := e.Embed("foo bar")
	sim := CosineSimilarity(v1, v2)
	if math.Abs(sim) > 0.001 {
		t.Errorf("orthogonal vectors: similarity = %f, want ~0.0", sim)
	}
}

func TestCosineSimilarity_Partial(t *testing.T) {
	e := NewEmbedder()
	v1 := e.Embed("ruff pyright linting python")
	v2 := e.Embed("ruff linting checks")
	sim := CosineSimilarity(v1, v2)
	if sim <= 0.0 || sim >= 1.0 {
		t.Errorf("partial overlap: similarity = %f, want 0 < sim < 1", sim)
	}
}

func TestCosineSimilarity_DifferentLengths(t *testing.T) {
	// Shorter vector padded conceptually with zeros.
	a := []float32{1, 0, 0}
	b := []float32{1, 0}
	sim := CosineSimilarity(a, b)
	if math.Abs(sim-1.0) > 0.001 {
		t.Errorf("different lengths: similarity = %f, want ~1.0", sim)
	}
}

func TestCosineSimilarity_NilAndEmpty(t *testing.T) {
	if CosineSimilarity(nil, []float32{1}) != 0 {
		t.Error("nil a: expected 0")
	}
	if CosineSimilarity([]float32{1}, nil) != 0 {
		t.Error("nil b: expected 0")
	}
	if CosineSimilarity(nil, nil) != 0 {
		t.Error("both nil: expected 0")
	}
	if CosineSimilarity([]float32{}, []float32{1}) != 0 {
		t.Error("empty a: expected 0")
	}
}

func TestCosineSimilarity_ZeroVector(t *testing.T) {
	a := []float32{0, 0, 0}
	b := []float32{1, 0, 0}
	if CosineSimilarity(a, b) != 0 {
		t.Error("zero vector: expected 0")
	}
}

func TestMarshalUnmarshalEmbedding(t *testing.T) {
	original := []float32{0.1, 0.2, 0.3, -0.5, 1.0}
	data := MarshalEmbedding(original)
	if len(data) != 20 { // 5 * 4 bytes
		t.Errorf("marshal: len = %d, want 20", len(data))
	}

	restored := UnmarshalEmbedding(data)
	if len(restored) != len(original) {
		t.Fatalf("unmarshal: len = %d, want %d", len(restored), len(original))
	}
	for i := range original {
		if restored[i] != original[i] {
			t.Errorf("unmarshal[%d] = %f, want %f", i, restored[i], original[i])
		}
	}
}

func TestMarshalEmbedding_Nil(t *testing.T) {
	if MarshalEmbedding(nil) != nil {
		t.Error("marshal nil: expected nil")
	}
	if MarshalEmbedding([]float32{}) != nil {
		t.Error("marshal empty: expected nil")
	}
}

func TestUnmarshalEmbedding_Invalid(t *testing.T) {
	if UnmarshalEmbedding(nil) != nil {
		t.Error("unmarshal nil: expected nil")
	}
	if UnmarshalEmbedding([]byte{}) != nil {
		t.Error("unmarshal empty: expected nil")
	}
	// Not a multiple of 4.
	if UnmarshalEmbedding([]byte{1, 2, 3}) != nil {
		t.Error("unmarshal 3 bytes: expected nil")
	}
}

func TestRRFScore(t *testing.T) {
	tests := []struct {
		name       string
		textRank   int
		vectorRank int
		k          float64
		want       float64
	}{
		{"both ranked 1", 1, 1, 60, 1.0/61.0 + 1.0/61.0},
		{"text only", 1, 0, 60, 1.0 / 61.0},
		{"vector only", 0, 3, 60, 1.0 / 63.0},
		{"neither", 0, 0, 60, 0},
		{"different k", 1, 1, 0, 2.0},
		{"high ranks", 100, 200, 60, 1.0/160.0 + 1.0/260.0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RRFScore(tt.textRank, tt.vectorRank, tt.k)
			if math.Abs(got-tt.want) > 0.0001 {
				t.Errorf("RRFScore(%d, %d, %f) = %f, want %f", tt.textRank, tt.vectorRank, tt.k, got, tt.want)
			}
		})
	}
}

func TestNormalize32_ZeroVector(t *testing.T) {
	v := []float32{0, 0, 0}
	normalize32(v) // should not panic
	for i, x := range v {
		if x != 0 {
			t.Errorf("normalize32 zero vector: v[%d] = %f, want 0", i, x)
		}
	}
}

// ---------------------------------------------------------------------------
// Integration tests: embedding stored on INSERT, hybrid search
// ---------------------------------------------------------------------------

func TestStore_InsertStoresEmbedding(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	store.SetEmbedder(NewEmbedder())
	ctx := context.Background()

	id, err := store.Insert(ctx, InsertParams{
		Content: "ruff pyright linting python", Type: "gotcha",
		Source: "self_report", Confidence: 0.9,
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	// Verify embedding was stored as BLOB in the database.
	var embeddingBlob []byte
	err = db.QueryRowContext(ctx, `SELECT embedding FROM memories WHERE id = ?`, id).Scan(&embeddingBlob)
	if err != nil {
		t.Fatalf("query embedding: %v", err)
	}
	if len(embeddingBlob) == 0 {
		t.Fatal("expected non-empty embedding BLOB")
	}

	vec := UnmarshalEmbedding(embeddingBlob)
	if len(vec) == 0 {
		t.Fatal("expected non-empty embedding vector after unmarshal")
	}
	assertUnitVector(t, vec)
}

func TestStore_InsertWithoutEmbedder(t *testing.T) {
	// When no embedder is set, insert should still work (no embedding stored).
	db := setupTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	id, err := store.Insert(ctx, InsertParams{
		Content: "no embedder test", Type: "lesson",
		Source: "self_report", Confidence: 0.8,
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	var embeddingBlob []byte
	err = db.QueryRowContext(ctx, `SELECT embedding FROM memories WHERE id = ?`, id).Scan(&embeddingBlob)
	if err != nil {
		t.Fatalf("query embedding: %v", err)
	}
	if len(embeddingBlob) != 0 {
		t.Errorf("expected no embedding when embedder is nil, got %d bytes", len(embeddingBlob))
	}
}

func TestStore_HybridSearch_BasicRanking(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	store.SetEmbedder(NewEmbedder())
	ctx := context.Background()

	// Insert memories with varying semantic overlap to query.
	_, err := store.Insert(ctx, InsertParams{
		Content: "ruff pyright linting always run ruff before pyright", Type: "gotcha",
		Source: "self_report", Confidence: 0.9,
	})
	if err != nil {
		t.Fatalf("insert 1: %v", err)
	}

	_, err = store.Insert(ctx, InsertParams{
		Content: "SQLite WAL mode requires single-writer for consistency", Type: "lesson",
		Source: "self_report", Confidence: 0.85,
	})
	if err != nil {
		t.Fatalf("insert 2: %v", err)
	}

	_, err = store.Insert(ctx, InsertParams{
		Content: "python linting tools are important for code quality", Type: "lesson",
		Source: "self_report", Confidence: 0.8,
	})
	if err != nil {
		t.Fatalf("insert 3: %v", err)
	}

	// Hybrid search for "ruff pyright linting" should rank the gotcha first.
	results, err := store.HybridSearch(ctx, "ruff pyright linting", SearchOpts{Limit: 10})
	if err != nil {
		t.Fatalf("hybrid search: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected results from hybrid search")
	}
	if results[0].Type != "gotcha" {
		t.Errorf("expected gotcha first, got type=%q content=%q", results[0].Type, results[0].Content)
	}
}

func TestStore_HybridSearch_EmptyQuery(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	store.SetEmbedder(NewEmbedder())
	ctx := context.Background()

	results, err := store.HybridSearch(ctx, "", SearchOpts{})
	if err != nil {
		t.Fatalf("hybrid search empty: %v", err)
	}
	if results != nil {
		t.Errorf("expected nil for empty query, got %v", results)
	}
}

func TestStore_HybridSearch_NoEmbedder(t *testing.T) {
	// Without embedder, HybridSearch should fall back to FTS5-only search.
	db := setupTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	_, err := store.Insert(ctx, InsertParams{
		Content: "fallback test unique_xyzzy_hybrid", Type: "lesson",
		Source: "self_report", Confidence: 0.8,
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	results, err := store.HybridSearch(ctx, "unique_xyzzy_hybrid", SearchOpts{Limit: 5})
	if err != nil {
		t.Fatalf("hybrid search: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected results from fallback FTS5 search")
	}
}

func TestStore_HybridSearch_TypeFilter(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	store.SetEmbedder(NewEmbedder())
	ctx := context.Background()

	_, err := store.Insert(ctx, InsertParams{
		Content: "hybrid type filter gotcha content unique789", Type: "gotcha",
		Source: "self_report", Confidence: 0.9,
	})
	if err != nil {
		t.Fatalf("insert 1: %v", err)
	}

	_, err = store.Insert(ctx, InsertParams{
		Content: "hybrid type filter lesson content unique789", Type: "lesson",
		Source: "self_report", Confidence: 0.8,
	})
	if err != nil {
		t.Fatalf("insert 2: %v", err)
	}

	results, err := store.HybridSearch(ctx, "unique789", SearchOpts{Type: "gotcha", Limit: 10})
	if err != nil {
		t.Fatalf("hybrid search: %v", err)
	}
	for _, r := range results {
		if r.Type != "gotcha" {
			t.Errorf("expected only gotcha, got type=%q", r.Type)
		}
	}
}

func TestStore_HybridSearch_TagFilter(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	store.SetEmbedder(NewEmbedder())
	ctx := context.Background()

	_, err := store.Insert(ctx, InsertParams{
		Content: "hybrid tag filter python test unique_tag_abc", Type: "lesson",
		Tags: []string{"python"}, Source: "self_report", Confidence: 0.9,
	})
	if err != nil {
		t.Fatalf("insert 1: %v", err)
	}

	_, err = store.Insert(ctx, InsertParams{
		Content: "hybrid tag filter go test unique_tag_abc", Type: "lesson",
		Tags: []string{"go"}, Source: "self_report", Confidence: 0.8,
	})
	if err != nil {
		t.Fatalf("insert 2: %v", err)
	}

	results, err := store.HybridSearch(ctx, "unique_tag_abc", SearchOpts{Tags: []string{"python"}, Limit: 10})
	if err != nil {
		t.Fatalf("hybrid search: %v", err)
	}
	for _, r := range results {
		tags := tagsFromJSON(r.Tags)
		if !anyTagMatch(tags, []string{"python"}) {
			t.Errorf("expected python tag, got tags=%s", r.Tags)
		}
	}
}

func TestStore_HybridSearch_MinScoreFilter(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	store.SetEmbedder(NewEmbedder())
	ctx := context.Background()

	_, err := store.Insert(ctx, InsertParams{
		Content: "hybrid min score unique_minscore_xyz", Type: "lesson",
		Source: "self_report", Confidence: 0.1,
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	// Very high min score should filter out low-confidence results.
	results, err := store.HybridSearch(ctx, "unique_minscore_xyz", SearchOpts{MinScore: 999, Limit: 10})
	if err != nil {
		t.Fatalf("hybrid search: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results with very high min score, got %d", len(results))
	}
}

func TestStore_HybridSearch_ClosedDB(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	store.SetEmbedder(NewEmbedder())
	_ = db.Close()

	_, err := store.HybridSearch(context.Background(), "test", SearchOpts{})
	if err == nil {
		t.Error("expected error from HybridSearch on closed DB")
	}
}

func TestStore_HybridSearch_VectorBoostsSemanticMatch(t *testing.T) {
	// This test verifies that the hybrid search improves ranking by using
	// vector similarity. We insert two memories: one with high text match
	// but low semantic relevance, and one with moderate text match but
	// high semantic overlap. The hybrid should favor the semantic match.
	db := setupTestDB(t)
	store := NewStore(db)
	store.SetEmbedder(NewEmbedder())
	ctx := context.Background()

	// Memory 1: shares one word with query but is about something else.
	_, err := store.Insert(ctx, InsertParams{
		Content: "testing is important for software quality assurance", Type: "lesson",
		Source: "self_report", Confidence: 0.9,
	})
	if err != nil {
		t.Fatalf("insert 1: %v", err)
	}

	// Memory 2: high semantic overlap with query terms.
	_, err = store.Insert(ctx, InsertParams{
		Content: "ruff linting pyright checks python formatting", Type: "gotcha",
		Source: "self_report", Confidence: 0.9,
	})
	if err != nil {
		t.Fatalf("insert 2: %v", err)
	}

	results, err := store.HybridSearch(ctx, "ruff pyright linting python", SearchOpts{Limit: 10})
	if err != nil {
		t.Fatalf("hybrid search: %v", err)
	}
	if len(results) < 2 {
		t.Fatalf("expected at least 2 results, got %d", len(results))
	}

	// The ruff/pyright memory should rank higher due to vector similarity boost.
	if results[0].Type != "gotcha" {
		t.Errorf("expected gotcha (semantic match) first, got type=%q content=%q", results[0].Type, results[0].Content)
	}
}

// assertUnitVector checks that a float32 vector has L2 norm ~1.0.
func assertUnitVector(t *testing.T, v []float32) {
	t.Helper()
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	norm := math.Sqrt(sum)
	if math.Abs(norm-1.0) > 0.01 {
		t.Errorf("expected unit vector (L2 norm ~1.0), got norm = %f", norm)
	}
}
