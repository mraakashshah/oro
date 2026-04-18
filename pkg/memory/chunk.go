package memory

import "strings"

const (
	chunkWindow    = 256
	chunkOverlap   = 32
	chunkMaxSingle = 512
)

// Chunk is a windowed segment of text produced by chunkContent.
type Chunk struct {
	Index      int
	Text       string
	TokenCount int
}

// chunkContent splits text into overlapping Chunks using a 256-token window
// and 32-token overlap (stride = 224). Texts with ≤512 tokens are returned as
// a single Chunk preserving the original text verbatim. Longer texts are
// segmented with the sliding window; the last Chunk may be shorter than 256
// tokens. Empty input returns nil.
func chunkContent(text string) []Chunk {
	tokens := tokenize(text)
	n := len(tokens)
	if n == 0 {
		return nil
	}
	if n <= chunkMaxSingle {
		return []Chunk{{Index: 0, Text: text, TokenCount: n}}
	}
	stride := chunkWindow - chunkOverlap
	var chunks []Chunk
	for start := 0; start < n; start += stride {
		end := start + chunkWindow
		if end > n {
			end = n
		}
		chunks = append(chunks, Chunk{
			Index:      len(chunks),
			Text:       strings.Join(tokens[start:end], " "),
			TokenCount: end - start,
		})
	}
	return chunks
}
