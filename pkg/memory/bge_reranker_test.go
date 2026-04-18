//go:build cgo && darwin

package memory_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/daulet/tokenizers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"oro/pkg/memory"
)

func TestBGERerankerConstructor(t *testing.T) {
	t.Run("missing model.onnx returns wrapped os.PathError", func(t *testing.T) {
		dir := t.TempDir()
		_, err := memory.NewBGEReranker(dir)
		require.Error(t, err)

		var pathErr *os.PathError
		assert.True(t, errors.As(err, &pathErr),
			"error must wrap os.PathError when model.onnx missing")
		assert.Contains(t, err.Error(), "oro models prefetch")
	})

	t.Run("missing tokenizer.json returns wrapped os.PathError", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "model.onnx"), []byte("x"), 0o600))

		_, err := memory.NewBGEReranker(dir)
		require.Error(t, err)

		var pathErr *os.PathError
		assert.True(t, errors.As(err, &pathErr),
			"error must wrap os.PathError when tokenizer.json missing")
		assert.Contains(t, err.Error(), "oro models prefetch")
		assert.Contains(t, err.Error(), "tokenizer")
	})

	t.Run("from parts returns non-nil BGEReranker", func(t *testing.T) {
		tok, err := tokenizers.FromFile(testdataTokenizerPath(t))
		require.NoError(t, err)
		defer tok.Close()

		sess := &fakeORTSession{output: []float32{0.5}}
		r := memory.NewBGERerankerFromParts(sess, tok)
		assert.NotNil(t, r)
	})
}

func TestBGERerankerRerankShape(t *testing.T) {
	tok, err := tokenizers.FromFile(testdataTokenizerPath(t))
	require.NoError(t, err)
	defer tok.Close()

	sess := &fakeORTSession{output: []float32{0.7}}
	r := memory.NewBGERerankerFromParts(sess, tok)

	t.Run("returns len(docs) scores", func(t *testing.T) {
		docs := []string{"first doc", "second doc", "third doc"}
		scores := r.Rerank("query", docs)
		assert.Len(t, scores, len(docs))
	})

	t.Run("nil docs returns empty slice no panic", func(t *testing.T) {
		scores := r.Rerank("query", nil)
		assert.Empty(t, scores)
	})

	t.Run("empty docs returns empty slice no panic", func(t *testing.T) {
		scores := r.Rerank("query", []string{})
		assert.Empty(t, scores)
	})

	t.Run("after Close returns zero scores no panic", func(t *testing.T) {
		tok2, err := tokenizers.FromFile(testdataTokenizerPath(t))
		require.NoError(t, err)

		sess2 := &fakeORTSession{output: []float32{0.9}}
		r2 := memory.NewBGERerankerFromParts(sess2, tok2)
		require.NoError(t, r2.Close())

		docs := []string{"doc a", "doc b"}
		scores := r2.Rerank("query", docs)
		assert.Len(t, scores, len(docs))
		for _, s := range scores {
			assert.Equal(t, float64(0), s)
		}
	})
}
