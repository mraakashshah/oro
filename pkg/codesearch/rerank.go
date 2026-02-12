package codesearch

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// RerankSpawner runs a prompt and returns the output string.
type RerankSpawner interface {
	Spawn(ctx context.Context, prompt string) (string, error)
}

// ScoredChunk is a chunk with a rank position and reason from the reranker.
type ScoredChunk struct {
	Chunk  Chunk
	Rank   int
	Reason string
}

// Reranker uses a Claude subprocess to rerank code search candidates by relevance.
type Reranker struct {
	spawner RerankSpawner
}

// NewReranker creates a reranker with the given spawner.
//
//oro:testonly
func NewReranker(spawner RerankSpawner) *Reranker {
	return &Reranker{spawner: spawner}
}

// rerankEntry is the JSON structure expected from the reranker output.
type rerankEntry struct {
	ID     int    `json:"id"`
	Reason string `json:"reason"`
}

// Rerank sends chunks to Claude for relevance ranking and returns reordered results.
// Empty chunks returns nil, nil. Errors from the spawner or unparseable output are returned.
func (r *Reranker) Rerank(ctx context.Context, query string, chunks []Chunk, topK int) ([]ScoredChunk, error) {
	if len(chunks) == 0 {
		return nil, nil
	}

	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("rerank: %w", err)
	}

	prompt := r.BuildPrompt(query, chunks)

	output, err := r.spawner.Spawn(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("rerank spawn: %w", err)
	}

	var entries []rerankEntry
	if err := json.Unmarshal([]byte(output), &entries); err != nil {
		return nil, fmt.Errorf("rerank parse output: %w (raw: %s)", err, output)
	}

	// Build lookup from chunk ID (1-based) to Chunk.
	chunkByID := make(map[int]Chunk, len(chunks))
	for i, c := range chunks {
		chunkByID[i+1] = c
	}

	var results []ScoredChunk
	for rank, entry := range entries {
		c, ok := chunkByID[entry.ID]
		if !ok {
			continue // skip unknown IDs
		}
		results = append(results, ScoredChunk{
			Chunk:  c,
			Rank:   rank + 1,
			Reason: entry.Reason,
		})
		if len(results) >= topK {
			break
		}
	}

	return results, nil
}

// BuildPrompt constructs the reranking prompt with XML-tagged chunks.
func (r *Reranker) BuildPrompt(query string, chunks []Chunk) string {
	var b strings.Builder
	b.WriteString("You are a code search reranker. Given a query and code chunks, rank them by relevance.\n\n")
	b.WriteString("<query>")
	b.WriteString(query)
	b.WriteString("</query>\n\n<chunks>\n")

	for i, c := range chunks {
		fmt.Fprintf(&b, "<chunk id=\"%d\" file=\"%s\" name=\"%s\">\n%s\n</chunk>\n", i+1, c.FilePath, c.Name, c.Content)
	}

	b.WriteString("</chunks>\n\n")
	b.WriteString("Return a JSON array of objects with \"id\" and \"reason\" fields, ordered by relevance (most relevant first). ")
	b.WriteString("Include all chunk IDs. Only output the JSON array, no other text.")

	return b.String()
}
