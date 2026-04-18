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
	"strings"
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

func TestPrefetchModelsHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	dir := t.TempDir()
	spec := memory.ModelSpec{
		Name:     "bge-small-en-v1.5",
		URL:      srv.URL + "/model.onnx",
		SHA256:   "0000000000000000000000000000000000000000000000000000000000000000",
		Filename: "model.onnx",
	}

	err := memory.PrefetchModels(context.Background(), dir, []memory.ModelSpec{spec})
	if err == nil {
		t.Fatal("expected error on HTTP 500, got nil")
	}
	if errors.Is(err, memory.ErrDigestMismatch) {
		t.Errorf("expected HTTP error, got ErrDigestMismatch: %v", err)
	}
	if !strings.Contains(err.Error(), spec.URL) {
		t.Errorf("error should include URL %q, got: %v", spec.URL, err)
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error should include status code 500, got: %v", err)
	}

	dest := filepath.Join(dir, "bge-small-en-v1.5", "model.onnx")
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Error("no file should be written on HTTP error")
	}
	if _, statErr := os.Stat(dest + ".corrupt"); !os.IsNotExist(statErr) {
		t.Error("no .corrupt file should be written on HTTP error")
	}
}

func TestRerankerModelEntry(t *testing.T) {
	// Assert the bge-reranker-base entry is registered in KnownModels with
	// the expected filename. This is the registry membership check the test
	// name promises — bypassing it would leave the entry untested.
	var registered *memory.ModelSpec
	for i := range memory.KnownModels {
		if memory.KnownModels[i].Name == "bge-reranker-base" {
			registered = &memory.KnownModels[i]
			break
		}
	}
	if registered == nil {
		t.Fatal("bge-reranker-base not present in memory.KnownModels registry")
	}
	if registered.Filename != "model.onnx" {
		t.Errorf("KnownModels[bge-reranker-base].Filename = %q, want %q", registered.Filename, "model.onnx")
	}
	if registered.URL == "" {
		t.Error("KnownModels[bge-reranker-base].URL must not be empty")
	}
	if registered.SHA256 == "" {
		t.Error("KnownModels[bge-reranker-base].SHA256 must not be empty")
	}

	rerankerBytes := []byte("fake reranker model bytes for testing")
	rerankerDigest := hex.EncodeToString(func() []byte { s := sha256.Sum256(rerankerBytes); return s[:] }())
	tokenizerBytes := []byte(`{"tokenizer": "fake"}`)
	tokenizerDigest := hex.EncodeToString(func() []byte { s := sha256.Sum256(tokenizerBytes); return s[:] }())

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "tokenizer.json") {
			_, _ = w.Write(tokenizerBytes)
			return
		}
		_, _ = w.Write(rerankerBytes)
	}))
	defer srv.Close()

	dir := t.TempDir()
	rerankerSpec := memory.ModelSpec{
		Name:     registered.Name,
		URL:      srv.URL + "/model.onnx",
		SHA256:   rerankerDigest,
		Filename: registered.Filename,
	}
	tokenizerSpec := memory.ModelSpec{
		Name:     "bge-reranker-base",
		URL:      srv.URL + "/tokenizer.json",
		SHA256:   tokenizerDigest,
		Filename: "tokenizer.json",
	}

	// Test ModelPath returns correct path
	modelPath := memory.ModelPath(dir, registered.Name)
	expectedPath := filepath.Join(dir, "bge-reranker-base", "model.onnx")
	if modelPath != expectedPath {
		t.Errorf("ModelPath = %q, want %q", modelPath, expectedPath)
	}

	// Test PrefetchModels downloads reranker model + tokenizer.json when absent
	if err := memory.PrefetchModels(context.Background(), dir, []memory.ModelSpec{rerankerSpec, tokenizerSpec}); err != nil {
		t.Fatalf("PrefetchModels: %v", err)
	}

	// Verify the reranker model.onnx landed with correct content
	data, err := os.ReadFile(modelPath)
	if err != nil {
		t.Fatalf("read model file: %v", err)
	}
	if string(data) != string(rerankerBytes) {
		t.Errorf("model content = %q, want %q", data, rerankerBytes)
	}

	// Verify tokenizer.json co-downloaded alongside the reranker
	tokenizerPath := filepath.Join(dir, "bge-reranker-base", "tokenizer.json")
	tokData, err := os.ReadFile(tokenizerPath)
	if err != nil {
		t.Fatalf("read tokenizer file: %v", err)
	}
	if string(tokData) != string(tokenizerBytes) {
		t.Errorf("tokenizer content = %q, want %q", tokData, tokenizerBytes)
	}

	// Test SHA256 verification on mismatch
	wrongDigest := "0000000000000000000000000000000000000000000000000000000000000000"
	badSpec := memory.ModelSpec{
		Name:     "bge-reranker-base",
		URL:      srv.URL + "/model.onnx",
		SHA256:   wrongDigest,
		Filename: "model.onnx",
	}

	badDir := t.TempDir()
	err = memory.PrefetchModels(context.Background(), badDir, []memory.ModelSpec{badSpec})
	if err == nil {
		t.Fatal("expected error on SHA256 mismatch, got nil")
	}
	if !errors.Is(err, memory.ErrDigestMismatch) {
		t.Errorf("expected ErrDigestMismatch in error chain, got: %v", err)
	}

	// Verify model.onnx does not exist after mismatch
	badModelPath := filepath.Join(badDir, "bge-reranker-base", "model.onnx")
	if _, statErr := os.Stat(badModelPath); !os.IsNotExist(statErr) {
		t.Error("model.onnx should not exist at dest path after SHA256 mismatch")
	}

	// Verify .corrupt file exists
	corruptPath := badModelPath + ".corrupt"
	if _, statErr := os.Stat(corruptPath); statErr != nil {
		t.Errorf("corrupt file should exist at %s: %v", corruptPath, statErr)
	}
}
