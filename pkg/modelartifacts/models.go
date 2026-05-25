// Package modelartifacts manages metadata, downloads, and digest verification
// for local model artifact files.
package modelartifacts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
)

// ErrDigestMismatch is returned when a downloaded model's SHA256 does not match
// the expected digest in the ModelSpec.
var ErrDigestMismatch = errors.New("model sha256 mismatch")

// ModelSpec describes a single model artifact to download and verify.
type ModelSpec struct {
	Name     string
	URL      string
	SHA256   string
	Filename string
}

// KnownModels lists the ONNX model artifacts required for semantic memory.
// SHA256 digests are pinned to specific releases; update when upgrading models.
var KnownModels = []ModelSpec{ //nolint:gochecknoglobals // static config table, read-only after init
	{
		Name:     "bge-small-en-v1.5",
		URL:      "https://huggingface.co/BAAI/bge-small-en-v1.5/resolve/main/onnx/model.onnx",
		SHA256:   "828e1496d7fabb79cfa4dcd84fa38625c0d3d21da474a00f08db0f559940cf35",
		Filename: "model.onnx",
	},
	{
		Name:     "bge-reranker-base",
		URL:      "https://huggingface.co/BAAI/bge-reranker-base/resolve/main/onnx/model.onnx",
		SHA256:   "15b9a8c3da82eddf263df571281166e00e9308fe19d077084b642ebfcaf06d2b",
		Filename: "model.onnx",
	},
	{
		// Reranker tokenizer is XLM-RoBERTa SentencePiece (Unigram), distinct
		// from the BERT WordPiece tokenizer used by bge-small-en-v1.5. Loading
		// bge-small's tokenizer against the reranker model produces nonsense
		// token IDs in its vocabulary and garbage relevance scores.
		Name:     "bge-reranker-tokenizer",
		URL:      "https://huggingface.co/BAAI/bge-reranker-base/resolve/main/tokenizer.json",
		SHA256:   "9eb652ac4e40cc093272bbbe0f55d521cf67570060227109b5cdc20945a4489e",
		Filename: "tokenizer.json",
	},
	{
		Name:     "bge-tokenizer",
		URL:      "https://huggingface.co/BAAI/bge-small-en-v1.5/resolve/main/tokenizer.json",
		SHA256:   "d241a60d5e8f04cc1b2b3e9ef7a4921b27bf526d9f6050ab90f9267a1f9e5c66",
		Filename: "tokenizer.json",
	},
}

// VerifyModel returns nil if the file at path has the expected SHA256 hex digest.
func VerifyModel(path, expectedSHA256 string) error {
	f, err := os.Open(path) //nolint:gosec // path is internally constructed from modelDir + spec fields
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("hash %s: %w", path, err)
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != expectedSHA256 {
		return fmt.Errorf("%w: got %s, want %s", ErrDigestMismatch, got, expectedSHA256)
	}
	return nil
}

// PrefetchModels downloads and verifies each spec into modelDir/<name>/<filename>.
// Empty modelDir defaults to ~/.oro/models.
// Skips specs whose file already exists with a matching digest.
// On digest mismatch, renames the file to <path>.corrupt and returns a wrapped ErrDigestMismatch.
// On context cancellation mid-download, removes the partial file and returns ctx.Err().
func PrefetchModels(ctx context.Context, modelDir string, specs []ModelSpec) error {
	if modelDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("resolve home dir: %w", err)
		}
		modelDir = filepath.Join(home, ".oro", "models")
	}

	for _, spec := range specs {
		dest := filepath.Join(modelDir, spec.Name, spec.Filename)
		if VerifyModel(dest, spec.SHA256) == nil {
			continue
		}
		if err := fetchAndVerify(ctx, dest, spec); err != nil {
			return err
		}
	}
	return nil
}

func fetchAndVerify(ctx context.Context, dest string, spec ModelSpec) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(dest), err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, spec.URL, http.NoBody)
	if err != nil {
		return fmt.Errorf("build request for %s: %w", spec.URL, err)
	}

	resp, err := http.DefaultClient.Do(req) //nolint:gosec // spec.URL comes from hardcoded KnownModels list, not user input
	if err != nil {
		return fmt.Errorf("download %s: %w", spec.URL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: http %d %s", spec.URL, resp.StatusCode, resp.Status)
	}

	tmp := dest + ".tmp"
	f, err := os.Create(tmp) //nolint:gosec // tmp path is internally constructed, not from user input
	if err != nil {
		return fmt.Errorf("create temp file %s: %w", tmp, err)
	}

	h := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(f, h), ctxReader{ctx: ctx, r: resp.Body})
	_ = f.Close()

	if copyErr != nil {
		_ = os.Remove(tmp)
		if ctx.Err() != nil {
			return fmt.Errorf("download cancelled: %w", ctx.Err())
		}
		return fmt.Errorf("write %s: %w", tmp, copyErr)
	}

	got := hex.EncodeToString(h.Sum(nil))
	if got != spec.SHA256 {
		_ = os.Rename(tmp, dest+".corrupt")
		return fmt.Errorf("%w: %s got %s, want %s", ErrDigestMismatch, spec.Name, got, spec.SHA256)
	}

	if err := os.Rename(tmp, dest); err != nil {
		return fmt.Errorf("rename %s → %s: %w", tmp, dest, err)
	}
	return nil
}

// ctxReader wraps an io.Reader to abort on context cancellation.
// io.EOF is passed through unwrapped so io.Copy recognises the sentinel.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (cr ctxReader) Read(p []byte) (int, error) {
	if err := cr.ctx.Err(); err != nil {
		return 0, fmt.Errorf("context cancelled: %w", err)
	}
	n, err := cr.r.Read(p)
	if err == io.EOF {
		return n, io.EOF
	}
	if err != nil {
		return n, fmt.Errorf("read: %w", err)
	}
	return n, nil
}
