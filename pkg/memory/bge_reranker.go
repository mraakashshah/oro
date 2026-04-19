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

const (
	bgeRerankerMaxTokens = 512
	// XLM-RoBERTa special token IDs (bge-reranker-base). The model's
	// tokenizer.json post-processor emits pairs as [<s> A </s></s> B </s>],
	// but daulet/tokenizers has no pair-encoding API, so we tokenize query
	// and doc separately (addSpecialTokens=false) and assemble the sequence
	// from raw IDs here.
	xlmRobertaBOSID = 0 // <s>
	xlmRobertaEOSID = 2 // </s>
)

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
	if _, err := os.Stat(modelPath); err != nil { //nolint:gosec // G703: modelDir is operator-supplied config, not external input
		return nil, fmt.Errorf("BGEReranker: model not found (run oro models prefetch): %w", err)
	}

	tokPath := filepath.Join(modelDir, "tokenizer.json")
	if _, err := os.Stat(tokPath); err != nil { //nolint:gosec // G703: modelDir is operator-supplied config, not external input
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

	// Pre-tokenize query once (without special tokens). Each doc is
	// tokenized fresh so we can build the cross-encoder pair sequence
	// [<s> query </s></s> doc </s>] from raw IDs.
	qIDs, _ := r.tokenizer.Encode(query, false)

	scores := make([]float64, len(docs))
	for i, doc := range docs {
		dIDs, _ := r.tokenizer.Encode(doc, false)

		// Build pair: [<s>] + query + [</s>, </s>] + doc + [</s>]
		// Total: 4 + len(query) + len(doc) special/sequence tokens.
		fixed := 4
		budget := bgeRerankerMaxTokens - fixed
		// Reserve roughly half the budget for each side; truncate doc first
		// (it's usually longer and less information-dense than a query).
		qMax := budget / 2
		if len(qIDs) > qMax {
			qIDs = qIDs[:qMax]
		}
		dMax := budget - len(qIDs)
		if len(dIDs) > dMax {
			dIDs = dIDs[:dMax]
		}

		seqLen := fixed + len(qIDs) + len(dIDs)
		ids64 := make([]int64, 0, seqLen)
		ids64 = append(ids64, xlmRobertaBOSID)
		for _, id := range qIDs {
			ids64 = append(ids64, int64(id))
		}
		ids64 = append(ids64, xlmRobertaEOSID, xlmRobertaEOSID)
		for _, id := range dIDs {
			ids64 = append(ids64, int64(id))
		}
		ids64 = append(ids64, xlmRobertaEOSID)

		mask64 := make([]int64, len(ids64))
		for j := range mask64 {
			mask64[j] = 1
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
