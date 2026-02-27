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

func TestDrainOutput_MultiWriter(t *testing.T) {
	input := "line one\nline two\n"
	reader := io.NopCloser(strings.NewReader(input))
	var buf1, buf2 bytes.Buffer

	worker.DrainOutput(context.Background(), reader, nil, "oro-test", &buf1, &buf2)

	if buf1.String() != input {
		t.Fatalf("writer 1: expected %q, got %q", input, buf1.String())
	}
	if buf2.String() != input {
		t.Fatalf("writer 2: expected %q, got %q", input, buf2.String())
	}
}

func TestDrainOutput_NilWriterFiltered(t *testing.T) {
	input := "hello\n"
	reader := io.NopCloser(strings.NewReader(input))
	var buf bytes.Buffer

	// nil writer in slice should not panic
	worker.DrainOutput(context.Background(), reader, nil, "oro-test", &buf, nil)

	if buf.String() != input {
		t.Fatalf("expected %q, got %q", input, buf.String())
	}
}

func TestDrainOutput_NoWriters(t *testing.T) {
	input := "hello\n"
	reader := io.NopCloser(strings.NewReader(input))

	// empty writers slice — should not panic
	worker.DrainOutput(context.Background(), reader, nil, "oro-test")
}

func TestDrainOutput_ImplicitExtraction_Lesson(t *testing.T) {
	input := "doing work\nI learned that FTS5 triggers must be on INSERT only\nmore work\n"
	reader := io.NopCloser(strings.NewReader(input))
	store := &mockMemStore{}
	var buf bytes.Buffer

	worker.DrainOutput(context.Background(), reader, store, "oro-impl1", &buf)

	if len(store.inserted) != 1 {
		t.Fatalf("expected 1 memory inserted, got %d", len(store.inserted))
	}
	mem := store.inserted[0]
	if mem.Type != "lesson" {
		t.Errorf("expected Type=lesson, got %q", mem.Type)
	}
	if mem.Source != "worker_implicit" {
		t.Errorf("expected Source=worker_implicit, got %q", mem.Source)
	}
	if mem.BeadID != "oro-impl1" {
		t.Errorf("expected BeadID=oro-impl1, got %q", mem.BeadID)
	}
	if !strings.Contains(mem.Content, "FTS5 triggers") {
		t.Errorf("expected content about FTS5 triggers, got %q", mem.Content)
	}
}

func TestDrainOutput_ImplicitExtraction_Gotcha(t *testing.T) {
	input := "Gotcha: ruff --fix must run BEFORE pyright or types break\n"
	reader := io.NopCloser(strings.NewReader(input))
	store := &mockMemStore{}
	var buf bytes.Buffer

	worker.DrainOutput(context.Background(), reader, store, "oro-impl2", &buf)

	if len(store.inserted) != 1 {
		t.Fatalf("expected 1 memory inserted, got %d", len(store.inserted))
	}
	mem := store.inserted[0]
	if mem.Type != "gotcha" {
		t.Errorf("expected Type=gotcha, got %q", mem.Type)
	}
	if mem.Source != "worker_implicit" {
		t.Errorf("expected Source=worker_implicit, got %q", mem.Source)
	}
}

func TestDrainOutput_ImplicitExtraction_NilStore(t *testing.T) {
	input := "I learned that this should not panic\n"
	reader := io.NopCloser(strings.NewReader(input))
	var buf bytes.Buffer

	// nil store should not panic
	worker.DrainOutput(context.Background(), reader, nil, "oro-test", &buf)

	if buf.String() != input {
		t.Fatalf("expected echoed output %q, got %q", input, buf.String())
	}
}

func TestDrainOutput_ExplicitMarkerStillWorks(t *testing.T) {
	// Regression: explicit [MEMORY] markers must still work alongside implicit extraction
	input := "[MEMORY] type=gotcha tags=go: WAL mode required\nI learned that concurrent writes need WAL\n"
	reader := io.NopCloser(strings.NewReader(input))
	store := &mockMemStore{}
	var buf bytes.Buffer

	worker.DrainOutput(context.Background(), reader, store, "oro-regr", &buf)

	if len(store.inserted) != 2 {
		t.Fatalf("expected 2 memories (1 explicit + 1 implicit), got %d", len(store.inserted))
	}
	// First should be the explicit marker
	if store.inserted[0].Source != "self_report" {
		t.Errorf("first memory Source=%q, want self_report", store.inserted[0].Source)
	}
	// Second should be the implicit extraction
	if store.inserted[1].Source != "worker_implicit" {
		t.Errorf("second memory Source=%q, want worker_implicit", store.inserted[1].Source)
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
