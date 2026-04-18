package memory_test

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/daulet/tokenizers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"oro/pkg/memory"
)

// fakeORTSession records calls and returns a preset vector.
type fakeORTSession struct {
	capturedIDs  []int64
	capturedMask []int64
	output       []float32
	err          error
	closeErr     error
	closeCalls   int
}

func (f *fakeORTSession) Run(tokenIDs, attentionMask []int64) ([]float32, error) {
	f.capturedIDs = tokenIDs
	f.capturedMask = attentionMask
	if f.err != nil {
		return nil, f.err
	}
	out := make([]float32, len(f.output))
	copy(out, f.output)
	return out, nil
}

func (f *fakeORTSession) Close() error {
	f.closeCalls++
	return f.closeErr
}

// testdataTokenizerPath returns the path to the bundled test tokenizer.
func testdataTokenizerPath(tb testing.TB) string {
	tb.Helper()
	_, filename, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(filename), "testdata", "bge-tokenizer-test.json")
}

func TestBGEEmbedderDimAndName(t *testing.T) {
	tok, err := tokenizers.FromFile(testdataTokenizerPath(t))
	require.NoError(t, err)
	defer tok.Close()

	fixed := make([]float32, memory.BGEDim)
	fixed[0] = 1.0
	sess := &fakeORTSession{output: fixed}

	emb := memory.NewBGEEmbedderFromParts(sess, tok)

	assert.Equal(t, 384, emb.Dim())
	assert.Equal(t, "bge-small-en-v1.5", emb.Name())
}

func TestBGEEmbedderHelloWorldTokens(t *testing.T) {
	tok, err := tokenizers.FromFile(testdataTokenizerPath(t))
	require.NoError(t, err)
	defer tok.Close()

	// Pre-encode to get expected IDs.
	enc := tok.EncodeWithOptions("hello world", true,
		tokenizers.WithReturnTokens(),
		tokenizers.WithReturnAttentionMask())
	require.Equal(t, []string{"[CLS]", "hello", "world", "[SEP]"}, enc.Tokens,
		"tokenizer must produce expected WordPiece tokens for 'hello world'")

	fixed := make([]float32, memory.BGEDim)
	fixed[0] = 1.0
	sess := &fakeORTSession{output: fixed}
	emb := memory.NewBGEEmbedderFromParts(sess, tok)

	vec := emb.Embed("hello world")

	require.Len(t, vec, memory.BGEDim)
	// Verify the session received the expected token IDs.
	expected64 := make([]int64, len(enc.IDs))
	for i, id := range enc.IDs {
		expected64[i] = int64(id)
	}
	assert.Equal(t, expected64, sess.capturedIDs)
}

func TestBGEEmbedderEmptyStringZeroVec(t *testing.T) {
	tok, err := tokenizers.FromFile(testdataTokenizerPath(t))
	require.NoError(t, err)
	defer tok.Close()

	sess := &fakeORTSession{output: make([]float32, memory.BGEDim)}
	emb := memory.NewBGEEmbedderFromParts(sess, tok)

	vec := emb.Embed("")

	assert.Len(t, vec, memory.BGEDim)
	for _, v := range vec {
		assert.Equal(t, float32(0), v)
	}
	// Session should not have been called for empty input.
	assert.Nil(t, sess.capturedIDs)
}

func TestBGEEmbedderSessionError(t *testing.T) {
	tok, err := tokenizers.FromFile(testdataTokenizerPath(t))
	require.NoError(t, err)
	defer tok.Close()

	sess := &fakeORTSession{err: errors.New("ort failure")}
	emb := memory.NewBGEEmbedderFromParts(sess, tok)

	vec := emb.Embed("hello")

	// On session error, returns zero vector.
	assert.Len(t, vec, memory.BGEDim)
}

func TestBGEEmbedderNewMissingModel(t *testing.T) {
	dir := t.TempDir()
	_, err := memory.NewBGEEmbedder(dir)
	require.Error(t, err)

	var pathErr *os.PathError
	assert.True(t, errors.As(err, &pathErr),
		"error must wrap os.PathError when model.onnx missing")
	assert.Contains(t, err.Error(), "oro models prefetch")
}

func TestBGEEmbedderCloseIdempotent(t *testing.T) {
	tok, err := tokenizers.FromFile(testdataTokenizerPath(t))
	require.NoError(t, err)

	sess := &fakeORTSession{output: make([]float32, memory.BGEDim)}
	emb := memory.NewBGEEmbedderFromParts(sess, tok)

	assert.NoError(t, emb.Close())
	assert.NoError(t, emb.Close()) // double-close must not panic or error
	assert.Equal(t, 1, sess.closeCalls, "session.Close must be called exactly once")
}

func TestBGEEmbedderCloseReleasesSession(t *testing.T) {
	tok, err := tokenizers.FromFile(testdataTokenizerPath(t))
	require.NoError(t, err)

	sess := &fakeORTSession{output: make([]float32, memory.BGEDim)}
	emb := memory.NewBGEEmbedderFromParts(sess, tok)

	require.NoError(t, emb.Close())
	assert.Equal(t, 1, sess.closeCalls,
		"BGEEmbedder.Close must release the underlying ORT session")
}

func TestBGEEmbedderCloseJoinsErrors(t *testing.T) {
	tok, err := tokenizers.FromFile(testdataTokenizerPath(t))
	require.NoError(t, err)

	sessErr := errors.New("ort destroy failed")
	sess := &fakeORTSession{output: make([]float32, memory.BGEDim), closeErr: sessErr}
	emb := memory.NewBGEEmbedderFromParts(sess, tok)

	err = emb.Close()
	require.Error(t, err)
	assert.ErrorIs(t, err, sessErr,
		"Close must surface the session close error via errors.Join")
}
