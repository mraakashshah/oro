//go:build cgo && darwin

package memory

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"

	"github.com/daulet/tokenizers"
)

const bgeRerankerMaxTokens = 512

// BGEReranker re-scores candidate documents against a query using a
// cross-encoder ONNX model with WordPiece tokenization.
type BGEReranker struct {
	session   ortSession
	tokenizer *tokenizers.Tokenizer
	closed    bool
	mu        sync.RWMutex
}

// NewBGEReranker loads a BGE cross-encoder reranker from modelDir.
// modelDir must contain model.onnx and tokenizer.json.
// Returns a wrapped os.PathError with a "run oro models prefetch" hint when
// either file is missing.
//
//oro:testonly — wired into production by subsequent semantic-memory beads (HybridSearch rerank arm)
func NewBGEReranker(modelDir string) (*BGEReranker, error) {
	modelPath := filepath.Join(modelDir, "model.onnx")
	if _, err := os.Stat(modelPath); err != nil {
		return nil, fmt.Errorf("BGEReranker: model not found (run oro models prefetch): %w", err)
	}

	tokPath := filepath.Join(modelDir, "tokenizer.json")
	if _, err := os.Stat(tokPath); err != nil {
		return nil, fmt.Errorf("BGEReranker: tokenizer not found (run oro models prefetch): %w", err)
	}

	tok, err := tokenizers.FromFile(tokPath)
	if err != nil {
		return nil, fmt.Errorf("BGEReranker: load tokenizer: %w", err)
	}

	sess, err := newORTSession(modelPath)
	if err != nil {
		if cerr := tok.Close(); cerr != nil {
			return nil, fmt.Errorf("BGEReranker: init ORT session: %w; close tokenizer: %w", err, cerr)
		}
		return nil, fmt.Errorf("BGEReranker: init ORT session: %w", err)
	}

	return &BGEReranker{
		session:   sess,
		tokenizer: tok,
	}, nil
}

// newBGERerankerFromParts constructs a BGEReranker from pre-built components.
// Used in tests to inject a fake ortSession without loading a real ONNX model.
func newBGERerankerFromParts(sess ortSession, tok *tokenizers.Tokenizer) *BGEReranker {
	return &BGEReranker{
		session:   sess,
		tokenizer: tok,
	}
}

// Rerank scores each document in docs against query and returns a []float64 of
// the same length. Nil or empty docs returns an empty slice without calling the
// ORT session. After Close, returns zero scores and logs a warning.
func (r *BGEReranker) Rerank(query string, docs []string) []float64 {
	if len(docs) == 0 {
		return []float64{}
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	if r.closed {
		log.Printf("BGEReranker: Rerank called on closed reranker; returning zero scores")
		return make([]float64, len(docs))
	}

	scores := make([]float64, len(docs))
	for i, doc := range docs {
		enc := r.tokenizer.EncodeWithOptions(
			query+" "+doc,
			true,
			tokenizers.WithReturnAttentionMask(),
		)

		ids := enc.IDs
		mask := enc.AttentionMask
		if len(ids) > bgeRerankerMaxTokens {
			ids = ids[:bgeRerankerMaxTokens]
			mask = mask[:bgeRerankerMaxTokens]
		}

		ids64 := make([]int64, len(ids))
		for j, id := range ids {
			ids64[j] = int64(id)
		}
		mask64 := make([]int64, len(mask))
		for j, m := range mask {
			mask64[j] = int64(m)
		}

		out, err := r.session.Run(ids64, mask64)
		if err != nil || len(out) == 0 {
			continue
		}
		scores[i] = float64(out[0])
	}
	return scores
}

// Close releases the tokenizer and ORT session. Idempotent.
// Blocks until any in-flight Rerank calls complete.
func (r *BGEReranker) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true

	var sessErr, tokErr error
	if r.session != nil {
		if err := r.session.Close(); err != nil {
			sessErr = fmt.Errorf("BGEReranker: close ORT session: %w", err)
		}
	}
	if r.tokenizer != nil {
		if err := r.tokenizer.Close(); err != nil {
			tokErr = fmt.Errorf("BGEReranker: close tokenizer: %w", err)
		}
	}
	return errors.Join(sessErr, tokErr)
}
