package memory_test

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

	"oro/pkg/memory"
)

func TestModelPath(t *testing.T) {
	dir := "/some/model/dir"
	got := memory.ModelPath(dir, "bge-small-en-v1.5")
	want := filepath.Join(dir, "bge-small-en-v1.5", "model.onnx")
	if got != want {
		t.Errorf("ModelPath = %q, want %q", got, want)
	}
}

func TestPrefetchModelsVerifyDigest(t *testing.T) {
	fixture := []byte("fake model bytes for testing")
	sum := sha256.Sum256(fixture)
	digest := hex.EncodeToString(sum[:])

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(fixture)
	}))
	defer srv.Close()

	dir := t.TempDir()
	spec := memory.ModelSpec{
		Name:     "bge-small-en-v1.5",
		URL:      srv.URL + "/model.onnx",
		SHA256:   digest,
		Filename: "model.onnx",
	}

	if err := memory.PrefetchModels(context.Background(), dir, []memory.ModelSpec{spec}); err != nil {
		t.Fatalf("PrefetchModels: %v", err)
	}

	dest := filepath.Join(dir, "bge-small-en-v1.5", "model.onnx")
	data, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read dest: %v", err)
	}
	if string(data) != string(fixture) {
		t.Errorf("dest content = %q, want %q", data, fixture)
	}
}

func TestPrefetchModelsSkipIfCached(t *testing.T) {
	fixture := []byte("fake model bytes for testing")
	sum := sha256.Sum256(fixture)
	digest := hex.EncodeToString(sum[:])

	dir := t.TempDir()
	dest := filepath.Join(dir, "bge-small-en-v1.5", "model.onnx")
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(dest, fixture, 0o644); err != nil {
		t.Fatalf("write cached: %v", err)
	}

	requested := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requested = true
		_, _ = w.Write(fixture)
	}))
	defer srv.Close()

	spec := memory.ModelSpec{
		Name:     "bge-small-en-v1.5",
		URL:      srv.URL + "/model.onnx",
		SHA256:   digest,
		Filename: "model.onnx",
	}

	if err := memory.PrefetchModels(context.Background(), dir, []memory.ModelSpec{spec}); err != nil {
		t.Fatalf("PrefetchModels: %v", err)
	}
	if requested {
		t.Error("expected no HTTP request when file is cached, but server was hit")
	}
}

func TestPrefetchModelsQuarantineOnMismatch(t *testing.T) {
	fixture := []byte("model bytes that will not match the declared digest")
	// all-zeros digest is astronomically unlikely to match any real file
	wrongDigest := "0000000000000000000000000000000000000000000000000000000000000000"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(fixture)
	}))
	defer srv.Close()

	dir := t.TempDir()
	spec := memory.ModelSpec{
		Name:     "bge-small-en-v1.5",
		URL:      srv.URL + "/model.onnx",
		SHA256:   wrongDigest,
		Filename: "model.onnx",
	}

	err := memory.PrefetchModels(context.Background(), dir, []memory.ModelSpec{spec})
	if err == nil {
		t.Fatal("expected error on digest mismatch, got nil")
	}
	if !errors.Is(err, memory.ErrDigestMismatch) {
		t.Errorf("expected ErrDigestMismatch in error chain, got: %v", err)
	}

	dest := filepath.Join(dir, "bge-small-en-v1.5", "model.onnx")
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Error("model file should not exist at dest path after mismatch")
	}
	corrupt := dest + ".corrupt"
	if _, statErr := os.Stat(corrupt); statErr != nil {
		t.Errorf("corrupt file should exist at %s: %v", corrupt, statErr)
	}
}
