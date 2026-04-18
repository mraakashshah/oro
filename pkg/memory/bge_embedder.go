//go:build cgo && darwin

package memory

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/daulet/tokenizers"
)

const (
	bgeModelName = "bge-small-en-v1.5"
	bgeDim       = 384
	bgeMaxTokens = 512
)

// ortSession abstracts the ONNX Runtime inference session for testability.
// Unit tests inject a fake; integration tests use a real ORT session via newORTSession.
type ortSession interface {
	Run(tokenIDs, attentionMask []int64) ([]float32, error)
	Close() error
}

// BGEEmbedder computes 384-dimensional L2-normalized embeddings using the
// BGE-small-en-v1.5 ONNX model with WordPiece tokenization.
type BGEEmbedder struct {
	session   ortSession
	tokenizer *tokenizers.Tokenizer
	closed    bool
	mu        sync.RWMutex
	dim       int
	name      string
}

// NewBGEEmbedder loads the BGE-small-en-v1.5 embedder from modelDir.
// modelDir must contain model.onnx and tokenizer.json.
// Returns a wrapped os.PathError with a "run oro models prefetch" hint when
// either file is missing.
//
//oro:testonly — wired into production by subsequent semantic-memory beads (dispatcher warmup, CLI fallback)
func NewBGEEmbedder(modelDir string) (*BGEEmbedder, error) {
	modelPath := filepath.Join(modelDir, "model.onnx")
	if _, err := os.Stat(modelPath); err != nil {
		return nil, fmt.Errorf("BGEEmbedder: model not found (run oro models prefetch): %w", err)
	}
	tokPath := filepath.Join(modelDir, "tokenizer.json")
	if _, err := os.Stat(tokPath); err != nil {
		return nil, fmt.Errorf("BGEEmbedder: tokenizer not found (run oro models prefetch): %w", err)
	}

	tok, err := tokenizers.FromFile(tokPath)
	if err != nil {
		return nil, fmt.Errorf("BGEEmbedder: load tokenizer: %w", err)
	}

	sess, err := newORTSession(modelPath)
	if err != nil {
		if cerr := tok.Close(); cerr != nil {
			return nil, fmt.Errorf("BGEEmbedder: init ORT session: %w; close tokenizer: %w", err, cerr)
		}
		return nil, fmt.Errorf("BGEEmbedder: init ORT session: %w", err)
	}

	return &BGEEmbedder{
		session:   sess,
		tokenizer: tok,
		dim:       bgeDim,
		name:      bgeModelName,
	}, nil
}

// newBGEEmbedderFromParts constructs a BGEEmbedder from pre-built components.
// Used in tests to inject a fake ortSession without loading a real ONNX model.
func newBGEEmbedderFromParts(sess ortSession, tok *tokenizers.Tokenizer) *BGEEmbedder {
	return &BGEEmbedder{
		session:   sess,
		tokenizer: tok,
		dim:       bgeDim,
		name:      bgeModelName,
	}
}

// Embed returns a 384-dimensional L2-normalized embedding for text.
// Empty input returns a zero vector of length 384 without calling the ORT session.
// Inputs longer than 512 tokens are truncated.
// Safe to call concurrently with other Embed calls; concurrent with Close
// it serializes — Close waits for in-flight Embeds and subsequent Embeds on
// a closed embedder return a zero vector (never dereferencing freed cgo handles).
func (b *BGEEmbedder) Embed(text string) []float32 {
	if text == "" {
		return make([]float32, bgeDim)
	}

	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.closed {
		return make([]float32, bgeDim)
	}

	enc := b.tokenizer.EncodeWithOptions(text, true,
		tokenizers.WithReturnAttentionMask())

	ids := enc.IDs
	mask := enc.AttentionMask
	if len(ids) > bgeMaxTokens {
		ids = ids[:bgeMaxTokens]
		mask = mask[:bgeMaxTokens]
	}

	ids64 := make([]int64, len(ids))
	for i, id := range ids {
		ids64[i] = int64(id)
	}
	mask64 := make([]int64, len(mask))
	for i, m := range mask {
		mask64[i] = int64(m)
	}

	vec, err := b.session.Run(ids64, mask64)
	if err != nil || len(vec) != bgeDim {
		return make([]float32, bgeDim)
	}

	normalize32(vec)
	return vec
}

// Dim returns the embedding dimension (384).
func (b *BGEEmbedder) Dim() int { return b.dim }

// Name returns the model identifier.
func (b *BGEEmbedder) Name() string { return b.name }

// Close releases the tokenizer and ORT session. Idempotent.
// Blocks until any in-flight Embed calls complete (write lock drains readers).
func (b *BGEEmbedder) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}
	b.closed = true
	var sessErr, tokErr error
	if b.session != nil {
		if err := b.session.Close(); err != nil {
			sessErr = fmt.Errorf("BGEEmbedder: close ORT session: %w", err)
		}
	}
	if b.tokenizer != nil {
		if err := b.tokenizer.Close(); err != nil {
			tokErr = fmt.Errorf("BGEEmbedder: close tokenizer: %w", err)
		}
	}
	return errors.Join(sessErr, tokErr)
}
