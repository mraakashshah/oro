package dispatcher

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"
)

// CodeIndex provides code search for injecting relevant code into prompts.
type CodeIndex interface {
	FTS5Search(ctx context.Context, query string, limit int) ([]CodeChunk, error)
	Search(ctx context.Context, query string, topK int) ([]SearchResult, error)
}

// CodeChunk represents a code search result.
type CodeChunk struct {
	FilePath  string
	Name      string
	Kind      string
	StartLine int
	EndLine   int
	Content   string
}

// SearchResult pairs a CodeChunk with its relevance score and optional rerank reason.
type SearchResult struct {
	CodeChunk
	Score  float64
	Reason string
}

func (d *Dispatcher) searchCodeInWorkdir(ctx context.Context, query string, topK int, _ string) ([]SearchResult, error) {
	chunks, err := d.codeIndex.FTS5Search(ctx, query, topK)
	if err != nil {
		return nil, fmt.Errorf("search code fts5: %w", err)
	}
	results := make([]SearchResult, 0, len(chunks))
	for i, chunk := range chunks {
		results = append(results, SearchResult{
			CodeChunk: chunk,
			Score:     1.0 / float64(i+1),
		})
	}
	return results, nil
}

// formatSearchResults formats code search results into markdown for prompt injection.
// When a result has a non-empty Reason, it is included as a relevance note.
func formatSearchResults(results []SearchResult) string {
	var b strings.Builder
	for _, r := range results {
		fmt.Fprintf(&b, "### %s:%d-%d\n```\n%s\n```\n",
			r.FilePath, r.StartLine, r.EndLine, r.Content)
		if r.Reason != "" {
			fmt.Fprintf(&b, "_Relevance: %s_\n", r.Reason)
		}
		b.WriteString("\n")
		if b.Len() >= maxCodeSearchContextSize {
			return truncateCodeSearchContext(b.String())
		}
	}
	return strings.TrimSpace(b.String())
}

func truncateCodeSearchContext(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxCodeSearchContextSize {
		return s
	}

	truncated := trimValidUTF8(s[:maxCodeSearchContextSize])
	truncated = strings.TrimSpace(truncated)
	if strings.Count(truncated, "```")%2 != 0 {
		truncated += "\n```"
	}
	return truncated + "\n\n[code search context truncated]"
}

func trimValidUTF8(s string) string {
	for s != "" {
		r, size := utf8.DecodeLastRuneInString(s)
		if r != utf8.RuneError || size != 1 {
			return s
		}
		s = s[:len(s)-1]
	}
	return s
}

// buildSearchQuery combines a bead title and labels into a single search string.
// Labels are appended after the title, separated by spaces.
// Empty labels are ignored. If title is empty, only labels are joined.
func buildSearchQuery(title string, labels []string) string {
	parts := make([]string, 0, 1+len(labels))
	if title != "" {
		parts = append(parts, title)
	}
	parts = append(parts, labels...)
	return strings.Join(parts, " ")
}
