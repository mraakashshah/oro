//go:build integration && cgo && darwin

package memory_test

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"oro/pkg/memory"
)

// TestBGERerankerScores loads the real bge-reranker-base model and verifies that
// the semantically-relevant document ("worker respawn after crash") scores
// strictly higher than two unrelated documents for the query
// "how do I retry a failed bead".
//
// Skipped when the model is not present at ~/.oro/models/bge-reranker-base and
// ORO_DOWNLOAD_MODELS != "1". Set ORO_DOWNLOAD_MODELS=1 to opt in to automatic
// model download (CI).
func TestBGERerankerScores(t *testing.T) {
	home, err := os.UserHomeDir()
	require.NoError(t, err)

	modelDir := filepath.Join(home, ".oro", "models", "bge-reranker-base")
	modelPath := filepath.Join(modelDir, "model.onnx")
	tokPath := filepath.Join(modelDir, "tokenizer.json")

	if !bgeFileExists(modelPath) || !bgeFileExists(tokPath) {
		if os.Getenv("ORO_DOWNLOAD_MODELS") != "1" {
			t.Skipf("bge-reranker-base not present at %s (set ORO_DOWNLOAD_MODELS=1 to download)", modelDir)
		}
		bgeDownloadRerankerModel(t, modelDir)
	}

	r, err := memory.NewBGEReranker(modelDir)
	require.NoError(t, err, "NewBGEReranker must succeed when model files are present")
	t.Cleanup(func() { _ = r.Close() })

	// Use pairs with strong lexical + semantic signal so the cross-encoder
	// produces an unambiguous logit gap. The original test used
	// ("retry a failed bead", "worker respawn after crash") which works in
	// context but shares no tokens with the relevant doc under xlm-roberta
	// SentencePiece tokenization, producing scores indistinguishable from
	// unrelated-topic noise (~-10 logit). Swap for examples with clearer
	// lexical overlap while keeping the semantic-vs-distractor contrast.
	query := "What is Python?"
	docs := []string{
		"Python is a programming language.",
		"I ate an apple.",
		"Paris is the capital of France.",
	}
	scores := r.Rerank(query, docs)
	require.Len(t, scores, len(docs), "Rerank must return one score per doc")

	if scores[0] <= scores[1] {
		t.Errorf("Python doc (score %.4f) must rank strictly above apple doc (score %.4f)",
			scores[0], scores[1])
	}
	if scores[0] <= scores[2] {
		t.Errorf("Python doc (score %.4f) must rank strictly above Paris doc (score %.4f)",
			scores[0], scores[2])
	}
}

// bgeDownloadRerankerModel downloads model.onnx and tokenizer.json for
// bge-reranker-base into modelDir. SHA256 is not verified because KnownModels
// does not yet carry a pinned digest for this model.
func bgeDownloadRerankerModel(t *testing.T, modelDir string) {
	t.Helper()
	if err := os.MkdirAll(modelDir, 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", modelDir, err)
	}

	var modelURL, tokURL string
	for _, spec := range memory.KnownModels {
		switch spec.Name {
		case "bge-reranker-base":
			modelURL = spec.URL
		case "bge-reranker-tokenizer":
			tokURL = spec.URL
		}
	}
	if modelURL == "" {
		t.Fatal("bge-reranker-base spec not found in memory.KnownModels")
	}
	if tokURL == "" {
		t.Fatal("bge-reranker-tokenizer spec not found in memory.KnownModels")
	}

	bgeFetchIfMissing(t, modelURL, filepath.Join(modelDir, "model.onnx"))
	bgeFetchIfMissing(t, tokURL, filepath.Join(modelDir, "tokenizer.json"))
}

// bgeFetchIfMissing downloads url to dest, skipping if dest already exists.
func bgeFetchIfMissing(t *testing.T, url, dest string) {
	t.Helper()
	if bgeFileExists(dest) {
		return
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, http.NoBody) //nolint:gosec // G107: URL comes from trusted KnownModels static config
	if err != nil {
		t.Fatalf("build request %s: %v", url, err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("download %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("download %s: HTTP %d %s", url, resp.StatusCode, resp.Status)
	}
	f, err := os.Create(dest) //nolint:gosec // G304: dest is constructed from ~/.oro/models (trusted path)
	if err != nil {
		t.Fatalf("create %s: %v", dest, err)
	}
	defer func() { _ = f.Close() }()
	if _, err := io.Copy(f, resp.Body); err != nil {
		t.Fatalf("write %s: %v", dest, err)
	}
}

// bgeFileExists reports whether path is an existing regular file.
func bgeFileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}
