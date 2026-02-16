package worker_test

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"oro/pkg/memory"
	"oro/pkg/worker"
)

// mockMemStore captures Insert calls without a real DB.
type mockMemStore struct {
	inserted []memory.InsertParams
}

func (m *mockMemStore) Insert(_ context.Context, p memory.InsertParams) (int64, error) {
	m.inserted = append(m.inserted, p)
	return int64(len(m.inserted)), nil
}

func TestDrainOutput_EchoesLines(t *testing.T) {
	input := "line one\nline two\nline three\n"
	reader := io.NopCloser(strings.NewReader(input))
	var buf bytes.Buffer

	worker.DrainOutput(context.Background(), reader, nil, "oro-test", &buf)

	got := buf.String()
	if got != input {
		t.Fatalf("expected echoed output %q, got %q", input, got)
	}
}

func TestDrainOutput_ExtractsMemoryMarkers(t *testing.T) {
	input := "doing work\n[MEMORY] type=lesson: sqlite WAL mode is required for concurrent access\nmore work\n"
	reader := io.NopCloser(strings.NewReader(input))
	store := &mockMemStore{}
	var buf bytes.Buffer

	worker.DrainOutput(context.Background(), reader, store, "oro-bead1", &buf)

	if len(store.inserted) != 1 {
		t.Fatalf("expected 1 memory inserted, got %d", len(store.inserted))
	}
	mem := store.inserted[0]
	if mem.BeadID != "oro-bead1" {
		t.Fatalf("expected BeadID=oro-bead1, got %q", mem.BeadID)
	}
	if mem.Type != "lesson" {
		t.Fatalf("expected Type=lesson, got %q", mem.Type)
	}
	if !strings.Contains(mem.Content, "sqlite WAL mode") {
		t.Fatalf("expected content to contain 'sqlite WAL mode', got %q", mem.Content)
	}
}

func TestDrainOutput_NilStore(t *testing.T) {
	input := "[MEMORY] type=lesson: should not panic\nregular line\n"
	reader := io.NopCloser(strings.NewReader(input))
	var buf bytes.Buffer

	worker.DrainOutput(context.Background(), reader, nil, "oro-test", &buf)

	got := buf.String()
	if got != input {
		t.Fatalf("expected echoed output %q, got %q", input, got)
	}
}

func TestDrainOutput_EmptyInput(t *testing.T) {
	reader := io.NopCloser(strings.NewReader(""))
	var buf bytes.Buffer

	worker.DrainOutput(context.Background(), reader, nil, "oro-test", &buf)

	if buf.Len() != 0 {
		t.Fatalf("expected empty output, got %q", buf.String())
	}
}
