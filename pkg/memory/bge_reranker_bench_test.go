//go:build cgo && darwin

package memory_test

import (
	"os"
	"path/filepath"
	"testing"

	"oro/pkg/memory"
)

// BenchmarkBGERerankPair measures the latency of reranking a single query-document pair.
// Loads the reranker once, then benchmarks b.N iterations of Rerank(query, [doc]).
func BenchmarkBGERerankPair(b *testing.B) {
	modelDir := filepath.Join(os.Getenv("HOME"), ".oro", "models", "bge-reranker-base")

	reranker, err := memory.NewBGEReranker(modelDir)
	if err != nil {
		b.Fatalf("NewBGEReranker: %v", err)
	}
	defer reranker.Close()

	query := "What is a worker?"
	docs := []string{"A worker is a process that executes tasks."}

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_ = reranker.Rerank(query, docs)
	}
}
