package modelartifacts_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"oro/pkg/modelartifacts"
)

func testDigest(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func TestVerifyModel(t *testing.T) {
	content := []byte("model bytes")
	path := filepath.Join(t.TempDir(), "model.onnx")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write model: %v", err)
	}

	if err := modelartifacts.VerifyModel(path, testDigest(content)); err != nil {
		t.Fatalf("VerifyModel matching digest: %v", err)
	}

	err := modelartifacts.VerifyModel(path, "0000000000000000000000000000000000000000000000000000000000000000")
	if !errors.Is(err, modelartifacts.ErrDigestMismatch) {
		t.Fatalf("VerifyModel mismatch error = %v, want ErrDigestMismatch", err)
	}
}

func TestPrefetchModelsDownloadsAndVerifies(t *testing.T) {
	content := []byte("downloaded model bytes")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(content)
	}))
	defer srv.Close()

	dir := t.TempDir()
	spec := modelartifacts.ModelSpec{
		Name:     "test-model",
		URL:      srv.URL + "/model.onnx",
		SHA256:   testDigest(content),
		Filename: "model.onnx",
	}

	if err := modelartifacts.PrefetchModels(context.Background(), dir, []modelartifacts.ModelSpec{spec}); err != nil {
		t.Fatalf("PrefetchModels: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dir, spec.Name, spec.Filename))
	if err != nil {
		t.Fatalf("read downloaded model: %v", err)
	}
	if string(got) != string(content) {
		t.Fatalf("downloaded content = %q, want %q", got, content)
	}
}
