package codesearch_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"oro/pkg/codesearch"
)

func TestMockEmbedder_Embed(t *testing.T) {
	emb := &codesearch.MockEmbedder{
		Dims: 3,
	}

	vec, err := emb.Embed(context.Background(), "hello world")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vec) != 3 {
		t.Fatalf("expected 3 dims, got %d", len(vec))
	}
}

func TestMockEmbedder_EmbedBatch(t *testing.T) {
	emb := &codesearch.MockEmbedder{
		Dims: 4,
	}

	vecs, err := emb.EmbedBatch(context.Background(), []string{"a", "b", "c"})
	if err != nil {
		t.Fatalf("EmbedBatch: %v", err)
	}
	if len(vecs) != 3 {
		t.Fatalf("expected 3 vectors, got %d", len(vecs))
	}
	for i, v := range vecs {
		if len(v) != 4 {
			t.Errorf("vector %d: expected 4 dims, got %d", i, len(v))
		}
	}
}

func TestOllamaEmbedder_Embed(t *testing.T) {
	// Mock Ollama API server.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embed" {
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}

		var req struct {
			Model string `json:"model"`
			Input string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		if req.Model != "nomic-embed-text" {
			t.Errorf("expected model nomic-embed-text, got %q", req.Model)
		}

		resp := map[string]any{
			"embeddings": [][]float64{{0.1, 0.2, 0.3}},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	emb := codesearch.NewOllamaEmbedder(srv.URL, "nomic-embed-text")
	vec, err := emb.Embed(context.Background(), "hello world")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vec) != 3 {
		t.Fatalf("expected 3 dims, got %d", len(vec))
	}
	if vec[0] != 0.1 || vec[1] != 0.2 || vec[2] != 0.3 {
		t.Errorf("unexpected vector values: %v", vec)
	}
}

func TestOllamaEmbedder_EmbedBatch(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		resp := map[string]any{
			"embeddings": [][]float64{{0.1, 0.2, 0.3}},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	emb := codesearch.NewOllamaEmbedder(srv.URL, "nomic-embed-text")
	vecs, err := emb.EmbedBatch(context.Background(), []string{"a", "b", "c"})
	if err != nil {
		t.Fatalf("EmbedBatch: %v", err)
	}
	if len(vecs) != 3 {
		t.Fatalf("expected 3 vectors, got %d", len(vecs))
	}
	if callCount != 3 {
		t.Errorf("expected 3 API calls, got %d", callCount)
	}
}

func TestOllamaEmbedder_Unavailable(t *testing.T) {
	// Point at a port that is not listening.
	emb := codesearch.NewOllamaEmbedder("http://127.0.0.1:1", "nomic-embed-text")
	_, err := emb.Embed(context.Background(), "hello")
	if err == nil {
		t.Fatal("expected error when Ollama is unavailable")
	}
}
