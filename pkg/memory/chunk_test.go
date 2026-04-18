package memory //nolint:testpackage // white-box tests for chunkContent internals

import (
	"strings"
	"testing"
)

// makeWords builds a space-joined string of n single-letter tokens ("w w w ...").
func makeWords(n int) string {
	words := make([]string, n)
	for i := range words {
		words[i] = "w"
	}
	return strings.Join(words, " ")
}

// TestChunkContentSingleChunkUnder512 verifies that content with ≤512 tokens
// returns exactly one Chunk with Index=0, Text equal to input, and
// TokenCount equal to len(tokens).
func TestChunkContentSingleChunkUnder512(t *testing.T) {
	t.Run("empty string returns nil", func(t *testing.T) {
		chunks := chunkContent("")
		if chunks != nil {
			t.Errorf("empty string: expected nil, got %v", chunks)
		}
	})

	t.Run("single token returns one chunk", func(t *testing.T) {
		input := "hello"
		chunks := chunkContent(input)
		if len(chunks) != 1 {
			t.Fatalf("single token: expected 1 chunk, got %d", len(chunks))
		}
		if chunks[0].Index != 0 {
			t.Errorf("single token: Index=%d, want 0", chunks[0].Index)
		}
		if chunks[0].Text != input {
			t.Errorf("single token: Text=%q, want %q", chunks[0].Text, input)
		}
		if chunks[0].TokenCount != 1 {
			t.Errorf("single token: TokenCount=%d, want 1", chunks[0].TokenCount)
		}
	})

	t.Run("short content returns one chunk with original text", func(t *testing.T) {
		input := "the quick brown fox jumps over the lazy dog"
		tokens := tokenize(input)
		chunks := chunkContent(input)
		if len(chunks) != 1 {
			t.Fatalf("short content: expected 1 chunk, got %d", len(chunks))
		}
		c := chunks[0]
		if c.Index != 0 {
			t.Errorf("Index=%d, want 0", c.Index)
		}
		if c.Text != input {
			t.Errorf("Text=%q, want %q", c.Text, input)
		}
		if c.TokenCount != len(tokens) {
			t.Errorf("TokenCount=%d, want %d", c.TokenCount, len(tokens))
		}
	})

	t.Run("100-token content returns one chunk", func(t *testing.T) {
		input := makeWords(100)
		chunks := chunkContent(input)
		if len(chunks) != 1 {
			t.Fatalf("100-token: expected 1 chunk, got %d", len(chunks))
		}
		if chunks[0].TokenCount != 100 {
			t.Errorf("100-token: TokenCount=%d, want 100", chunks[0].TokenCount)
		}
		if chunks[0].Text != input {
			t.Errorf("100-token: Text does not match input")
		}
	})
}

// TestChunkContentWindows256Overlap32 verifies that content with >512 tokens
// is split into multiple Chunks each with TokenCount≤256 and successive chunks
// overlapping by 32 tokens (chunk[i+1].start == chunk[i].start + 224).
func TestChunkContentWindows256Overlap32(t *testing.T) {
	const window = 256
	const overlap = 32
	const stride = window - overlap // 224

	input := makeWords(700)
	tokens := tokenize(input)
	total := len(tokens) // should be 700

	chunks := chunkContent(input)

	if len(chunks) < 2 {
		t.Fatalf("700-token input: expected multiple chunks, got %d", len(chunks))
	}

	// Verify sequential Index values.
	for i, c := range chunks {
		if c.Index != i {
			t.Errorf("chunk[%d].Index=%d, want %d", i, c.Index, i)
		}
	}

	// Verify each chunk has TokenCount ≤ 256.
	for i, c := range chunks {
		if c.TokenCount > window {
			t.Errorf("chunk[%d].TokenCount=%d exceeds window %d", i, c.TokenCount, window)
		}
		if c.TokenCount <= 0 {
			t.Errorf("chunk[%d].TokenCount=%d, want >0", i, c.TokenCount)
		}
	}

	// Verify start positions and TokenCounts match stride.
	for i, c := range chunks {
		start := i * stride
		end := start + window
		if end > total {
			end = total
		}
		expectedCount := end - start
		if c.TokenCount != expectedCount {
			t.Errorf("chunk[%d]: TokenCount=%d, want %d (start=%d end=%d)",
				i, c.TokenCount, expectedCount, start, end)
		}
	}

	// Verify the last chunk's TokenCount is ≤ 256.
	last := chunks[len(chunks)-1]
	if last.TokenCount > window {
		t.Errorf("last chunk TokenCount=%d, want ≤%d", last.TokenCount, window)
	}
}

// TestChunkContentBoundary512 verifies the exact boundary:
//   - exactly 512 tokens → single Chunk (≤512 path)
//   - exactly 513 tokens → multiple Chunks (>512 triggers sliding window)
func TestChunkContentBoundary512(t *testing.T) {
	t.Run("exactly 512 tokens returns one chunk", func(t *testing.T) {
		input := makeWords(512)
		chunks := chunkContent(input)
		if len(chunks) != 1 {
			t.Fatalf("512 tokens: expected 1 chunk, got %d", len(chunks))
		}
		if chunks[0].Index != 0 {
			t.Errorf("512 tokens: Index=%d, want 0", chunks[0].Index)
		}
		if chunks[0].TokenCount != 512 {
			t.Errorf("512 tokens: TokenCount=%d, want 512", chunks[0].TokenCount)
		}
		if chunks[0].Text != input {
			t.Errorf("512 tokens: Text does not match input")
		}
	})

	t.Run("exactly 513 tokens returns multiple chunks", func(t *testing.T) {
		input := makeWords(513)
		chunks := chunkContent(input)
		if len(chunks) <= 1 {
			t.Fatalf("513 tokens: expected multiple chunks, got %d", len(chunks))
		}
		// All non-last chunks must have TokenCount == 256.
		for i, c := range chunks[:len(chunks)-1] {
			if c.TokenCount != 256 {
				t.Errorf("513 tokens: chunk[%d].TokenCount=%d, want 256", i, c.TokenCount)
			}
		}
		// Last chunk must not exceed 256.
		last := chunks[len(chunks)-1]
		if last.TokenCount > 256 {
			t.Errorf("513 tokens: last chunk TokenCount=%d, want ≤256", last.TokenCount)
		}
	})
}
