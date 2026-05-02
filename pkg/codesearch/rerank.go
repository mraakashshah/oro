package codesearch

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// RerankSpawner runs a prompt and returns the output string.
type RerankSpawner interface {
	Spawn(ctx context.Context, prompt string) (string, error)
}

// WorkdirRerankSpawner binds reranker subprocess execution to a worktree.
type WorkdirRerankSpawner interface {
	SpawnInWorkdir(ctx context.Context, prompt, workdir string) (string, error)
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
func NewReranker(spawner RerankSpawner) *Reranker {
	return &Reranker{spawner: spawner}
}

// flexIntID accepts both JSON integers (1) and JSON strings ("1") during unmarshaling.
// Claude occasionally returns IDs as strings rather than integers.
type flexIntID int

// UnmarshalJSON implements json.Unmarshaler; called implicitly by encoding/json, not by name.
// Accepts both JSON integers (1) and JSON strings ("1") for compatibility with Claude's output.
//
//oro:testonly
func (f *flexIntID) UnmarshalJSON(data []byte) error {
	// Try integer first (common case).
	var i int
	if err := json.Unmarshal(data, &i); err == nil {
		*f = flexIntID(i)
		return nil
	}
	// Fall back to string.
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return fmt.Errorf("rerankEntry.ID: expected number or string, got %s", data)
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return fmt.Errorf("rerankEntry.ID: cannot parse %q as integer", s)
	}
	*f = flexIntID(n)
	return nil
}

// rerankEntry is the JSON structure expected from the reranker output.
type rerankEntry struct {
	ID     flexIntID `json:"id"`
	Reason string    `json:"reason"`
}

// Rerank sends chunks to Claude for relevance ranking and returns reordered results.
// Empty chunks returns nil, nil. Errors from the spawner or unparseable output are returned.
func (r *Reranker) Rerank(ctx context.Context, query string, chunks []Chunk, topK int) ([]ScoredChunk, error) {
	return r.RerankInWorkdir(ctx, query, chunks, topK, "")
}

// RerankInWorkdir sends chunks for relevance ranking from workdir when the
// configured spawner supports workdir binding.
func (r *Reranker) RerankInWorkdir(ctx context.Context, query string, chunks []Chunk, topK int, workdir string) ([]ScoredChunk, error) {
	if len(chunks) == 0 {
		return nil, nil
	}

	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("rerank: %w", err)
	}

	prompt := r.BuildPrompt(query, chunks)

	output, err := spawnReranker(ctx, r.spawner, prompt, workdir)
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
		c, ok := chunkByID[int(entry.ID)]
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

func spawnReranker(ctx context.Context, spawner RerankSpawner, prompt, workdir string) (string, error) {
	if workdir != "" {
		if workdirSpawner, ok := spawner.(WorkdirRerankSpawner); ok {
			return workdirSpawner.SpawnInWorkdir(ctx, prompt, workdir)
		}
	}
	return spawner.Spawn(ctx, prompt)
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
