package codesearch_test

import (
	"context"
	"fmt"
	"testing"

	"oro/pkg/codesearch"
)

// mockSpawner returns canned output for reranker tests.
type mockSpawner struct {
	output string
	err    error
}

func (m *mockSpawner) Spawn(ctx context.Context, prompt string) (string, error) {
	return m.output, m.err
}

func TestRerank(t *testing.T) {
	chunks := []codesearch.Chunk{
		{FilePath: "a.go", Name: "handleAuth", Kind: codesearch.ChunkFunc, Content: "func handleAuth() {}"},
		{FilePath: "b.go", Name: "Server", Kind: codesearch.ChunkType, Content: "type Server struct{}"},
		{FilePath: "c.go", Name: "main", Kind: codesearch.ChunkFunc, Content: "func main() {}"},
	}

	t.Run("reorders chunks by ranking", func(t *testing.T) {
		spawner := &mockSpawner{
			output: `[{"id":1,"reason":"most relevant"},{"id":3,"reason":"entry point"},{"id":2,"reason":"less relevant"}]`,
		}
		r := codesearch.NewReranker(spawner)

		results, err := r.Rerank(context.Background(), "authentication", chunks, 3)
		if err != nil {
			t.Fatalf("Rerank: %v", err)
		}
		if len(results) != 3 {
			t.Fatalf("expected 3 results, got %d", len(results))
		}
		// Should be reordered: chunk[0], chunk[2], chunk[1]
		if results[0].Chunk.Name != "handleAuth" {
			t.Errorf("rank 1: want handleAuth, got %s", results[0].Chunk.Name)
		}
		if results[0].Rank != 1 {
			t.Errorf("rank 1: want Rank=1, got %d", results[0].Rank)
		}
		if results[0].Reason != "most relevant" {
			t.Errorf("rank 1: want reason 'most relevant', got %q", results[0].Reason)
		}
		if results[1].Chunk.Name != "main" {
			t.Errorf("rank 2: want main, got %s", results[1].Chunk.Name)
		}
		if results[2].Chunk.Name != "Server" {
			t.Errorf("rank 3: want Server, got %s", results[2].Chunk.Name)
		}
	})

	t.Run("topK limits results", func(t *testing.T) {
		spawner := &mockSpawner{
			output: `[{"id":1,"reason":"best"},{"id":3,"reason":"ok"},{"id":2,"reason":"meh"}]`,
		}
		r := codesearch.NewReranker(spawner)

		results, err := r.Rerank(context.Background(), "test", chunks, 2)
		if err != nil {
			t.Fatalf("Rerank: %v", err)
		}
		if len(results) != 2 {
			t.Errorf("expected 2 results (topK=2), got %d", len(results))
		}
	})

	t.Run("empty chunks returns nil", func(t *testing.T) {
		spawner := &mockSpawner{}
		r := codesearch.NewReranker(spawner)

		results, err := r.Rerank(context.Background(), "test", nil, 5)
		if err != nil {
			t.Fatalf("Rerank: %v", err)
		}
		if results != nil {
			t.Errorf("expected nil for empty chunks, got %v", results)
		}
	})

	t.Run("spawner error propagates", func(t *testing.T) {
		spawner := &mockSpawner{err: fmt.Errorf("claude crashed")}
		r := codesearch.NewReranker(spawner)

		_, err := r.Rerank(context.Background(), "test", chunks, 3)
		if err == nil {
			t.Fatal("expected error from spawner, got nil")
		}
	})

	t.Run("unparseable output returns error", func(t *testing.T) {
		spawner := &mockSpawner{output: "not json at all"}
		r := codesearch.NewReranker(spawner)

		_, err := r.Rerank(context.Background(), "test", chunks, 3)
		if err == nil {
			t.Fatal("expected error from bad JSON, got nil")
		}
	})

	t.Run("BuildPrompt includes query and chunks", func(t *testing.T) {
		r := codesearch.NewReranker(nil)
		prompt := r.BuildPrompt("find auth", chunks)

		if prompt == "" {
			t.Fatal("expected non-empty prompt")
		}
		// Prompt should contain the query
		if !contains(prompt, "find auth") {
			t.Error("prompt should contain query")
		}
		// Prompt should contain chunk content with IDs
		if !contains(prompt, "handleAuth") {
			t.Error("prompt should contain chunk content")
		}
		if !contains(prompt, "<chunk id=\"1\"") {
			t.Error("prompt should contain chunk IDs in XML tags")
		}
	})

	t.Run("context cancellation returns error", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		spawner := &mockSpawner{output: "[]"}
		r := codesearch.NewReranker(spawner)

		_, err := r.Rerank(ctx, "test", chunks, 3)
		if err == nil {
			t.Fatal("expected context error, got nil")
		}
	})
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && containsSubstring(s, sub)
}

func containsSubstring(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
