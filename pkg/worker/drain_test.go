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

// mockLLMSpawner implements memory.Spawner for testing. It records whether Spawn
// was called and returns canned output.
type mockLLMSpawner struct {
	called      bool
	promptGiven string
	output      string
}

func (m *mockLLMSpawner) Spawn(_ context.Context, _, prompt string) (io.ReadCloser, error) {
	m.called = true
	m.promptGiven = prompt
	return io.NopCloser(strings.NewReader(m.output)), nil
}

func TestDrainOutput_EchoesLines(t *testing.T) {
	input := "line one\nline two\nline three\n"
	reader := io.NopCloser(strings.NewReader(input))
	var buf bytes.Buffer

	worker.DrainOutput(context.Background(), reader, nil, "oro-test", nil, &buf)

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

	worker.DrainOutput(context.Background(), reader, store, "oro-bead1", nil, &buf)

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

	worker.DrainOutput(context.Background(), reader, nil, "oro-test", nil, &buf)

	got := buf.String()
	if got != input {
		t.Fatalf("expected echoed output %q, got %q", input, got)
	}
}

func TestDrainOutput_MultiWriter(t *testing.T) {
	input := "line one\nline two\n"
	reader := io.NopCloser(strings.NewReader(input))
	var buf1, buf2 bytes.Buffer

	worker.DrainOutput(context.Background(), reader, nil, "oro-test", nil, &buf1, &buf2)

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
	worker.DrainOutput(context.Background(), reader, nil, "oro-test", nil, &buf, nil)

	if buf.String() != input {
		t.Fatalf("expected %q, got %q", input, buf.String())
	}
}

func TestDrainOutput_NoWriters(t *testing.T) {
	input := "hello\n"
	reader := io.NopCloser(strings.NewReader(input))

	// empty writers slice — should not panic
	worker.DrainOutput(context.Background(), reader, nil, "oro-test", nil)
}

func TestDrainOutput_EmptyInput(t *testing.T) {
	reader := io.NopCloser(strings.NewReader(""))
	var buf bytes.Buffer

	worker.DrainOutput(context.Background(), reader, nil, "oro-test", nil, &buf)

	if buf.Len() != 0 {
		t.Fatalf("expected empty output, got %q", buf.String())
	}
}

func TestDrainOutput_LLMExtraction(t *testing.T) {
	// Spawner returns a canned [MEMORY] line so ExtractWithLLM parses it into store.
	spawner := &mockLLMSpawner{
		output: "[MEMORY] type=lesson tags=go: tests should use table-driven patterns\n",
	}
	store := &mockMemStore{}

	input := "doing work\nfound a pattern\nfinished\n"
	reader := io.NopCloser(strings.NewReader(input))
	var buf bytes.Buffer

	worker.DrainOutput(context.Background(), reader, store, "oro-llm1", spawner, &buf)

	// Spawner must have been called.
	if !spawner.called {
		t.Fatal("expected spawner.Spawn to be called after drain completes")
	}

	// The accumulated session text should contain all input lines.
	if !strings.Contains(spawner.promptGiven, "doing work") {
		t.Errorf("expected prompt to contain accumulated text, got %q", spawner.promptGiven)
	}
	if !strings.Contains(spawner.promptGiven, "finished") {
		t.Errorf("expected prompt to contain 'finished', got %q", spawner.promptGiven)
	}

	// ExtractWithLLM should have inserted a memory from the canned output.
	found := false
	for _, m := range store.inserted {
		if m.Source == "llm_extracted" && strings.Contains(m.Content, "table-driven") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected llm_extracted memory with 'table-driven', got %v", store.inserted)
	}

	// Output should still be echoed.
	if buf.String() != input {
		t.Errorf("expected echoed output %q, got %q", input, buf.String())
	}
}

func TestDrainOutput_NilSpawner(t *testing.T) {
	// With nil spawner, ExtractWithLLM should be skipped (no panic, no LLM call).
	store := &mockMemStore{}
	input := "doing work\n[MEMORY] type=lesson: explicit marker still captured\nmore work\n"
	reader := io.NopCloser(strings.NewReader(input))
	var buf bytes.Buffer

	worker.DrainOutput(context.Background(), reader, store, "oro-nil-sp", nil, &buf)

	// Explicit [MEMORY] markers should still be captured via ParseMarker.
	if len(store.inserted) != 1 {
		t.Fatalf("expected 1 memory (explicit marker only), got %d", len(store.inserted))
	}
	if store.inserted[0].Source != "self_report" {
		t.Errorf("expected Source=self_report, got %q", store.inserted[0].Source)
	}

	// Output should still be echoed.
	if buf.String() != input {
		t.Errorf("expected echoed output %q, got %q", input, buf.String())
	}
}
