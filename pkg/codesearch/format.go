package codesearch

import (
	"fmt"
	"strings"
)

// FormatResults formats code search results into markdown for prompt injection.
//
// Results with empty content are skipped. Returns empty string for nil/empty input.
//
//oro:testonly
func FormatResults(results []SearchResult) string {
	if len(results) == 0 {
		return ""
	}

	var b strings.Builder
	for _, r := range results {
		if r.Chunk.Content == "" {
			continue
		}
		fmt.Fprintf(&b, "### %s:%d-%d\n```\n%s\n```\n",
			r.Chunk.FilePath, r.Chunk.StartLine, r.Chunk.EndLine, r.Chunk.Content)
		if r.Reason != "" {
			fmt.Fprintf(&b, "_Relevance: %s_\n", r.Reason)
		}
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
}
