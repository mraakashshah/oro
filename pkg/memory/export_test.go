// Package memory — test-only exports for white-box BGEEmbedder tests.
package memory

import "github.com/daulet/tokenizers"

// BGEDim exposes the bgeDim constant for use in external test packages.
const BGEDim = bgeDim

// NewBGEEmbedderFromParts exposes the internal constructor for tests that
// inject a fake ortSession without loading a real ONNX model.
func NewBGEEmbedderFromParts(sess interface {
	Run(tokenIDs, attentionMask []int64) ([]float32, error)
}, tok *tokenizers.Tokenizer,
) *BGEEmbedder {
	return newBGEEmbedderFromParts(sess, tok)
}
