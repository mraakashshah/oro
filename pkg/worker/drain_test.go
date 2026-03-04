package worker_test

import (
	"bytes"
	"context"
	"encoding/json"
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

// --- stream-json test helpers ---

// textDeltaLine wraps text in a stream-json assistant text event.
func textDeltaLine(text string) string {
	b, _ := json.Marshal(map[string]interface{}{
		"type": "assistant",
		"message": map[string]interface{}{
			"content": []map[string]interface{}{
				{"type": "text", "text": text},
			},
		},
	})
	return string(b)
}

// toolUseLine wraps a tool name in a stream-json assistant tool_use event.
func toolUseLine(name string) string {
	b, _ := json.Marshal(map[string]interface{}{
		"type": "assistant",
		"message": map[string]interface{}{
			"content": []map[string]interface{}{
				{"type": "tool_use", "name": name},
			},
		},
	})
	return string(b)
}

// ndjsonInput joins stream-json lines with newlines into a single reader input.
func ndjsonInput(lines ...string) string {
	return strings.Join(lines, "\n") + "\n"
}

func TestDrainOutput_FormatsToolActivity(t *testing.T) {
	input := ndjsonInput(
		toolUseLine("Read"),
		textDeltaLine("reading file...\n"),
		toolUseLine("Bash"),
	)
	reader := io.NopCloser(strings.NewReader(input))
	var buf bytes.Buffer

	worker.DrainOutput(context.Background(), reader, nil, "oro-test", nil, &buf)

	got := buf.String()
	// Tool activity and text content both echoed to output.
	if !strings.Contains(got, "-> Read") {
		t.Fatalf("expected '-> Read' in output, got %q", got)
	}
	if !strings.Contains(got, "-> Bash") {
		t.Fatalf("expected '-> Bash' in output, got %q", got)
	}
	if !strings.Contains(got, "reading file...") {
		t.Fatalf("expected text echo in output, got %q", got)
	}
}

func TestDrainOutput_ExtractsMemoryMarkers(t *testing.T) {
	input := ndjsonInput(
		textDeltaLine("doing work\n"),
		textDeltaLine("[MEMORY] type=lesson: sqlite WAL mode is required for concurrent access\n"),
		textDeltaLine("more work\n"),
	)
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
	input := ndjsonInput(
		textDeltaLine("[MEMORY] type=lesson: should not panic\n"),
		textDeltaLine("regular line\n"),
	)
	reader := io.NopCloser(strings.NewReader(input))
	var buf bytes.Buffer

	// Should not panic with nil store.
	worker.DrainOutput(context.Background(), reader, nil, "oro-test", nil, &buf)

	// Text content should be echoed to output for debugging visibility.
	if !strings.Contains(buf.String(), "regular line") {
		t.Errorf("expected text echo in output, got %q", buf.String())
	}
}

func TestDrainOutput_MultiWriter(t *testing.T) {
	input := ndjsonInput(
		toolUseLine("Read"),
		toolUseLine("Edit"),
	)
	reader := io.NopCloser(strings.NewReader(input))
	var buf1, buf2 bytes.Buffer

	worker.DrainOutput(context.Background(), reader, nil, "oro-test", nil, &buf1, &buf2)

	want := "-> Read\n-> Edit\n"
	if buf1.String() != want {
		t.Fatalf("writer 1: expected %q, got %q", want, buf1.String())
	}
	if buf2.String() != want {
		t.Fatalf("writer 2: expected %q, got %q", want, buf2.String())
	}
}

func TestDrainOutput_NilWriterFiltered(t *testing.T) {
	input := ndjsonInput(toolUseLine("Bash"))
	reader := io.NopCloser(strings.NewReader(input))
	var buf bytes.Buffer

	// nil writer in slice should not panic
	worker.DrainOutput(context.Background(), reader, nil, "oro-test", nil, &buf, nil)

	want := "-> Bash\n"
	if buf.String() != want {
		t.Fatalf("expected %q, got %q", want, buf.String())
	}
}

func TestDrainOutput_NoWriters(t *testing.T) {
	input := ndjsonInput(textDeltaLine("hello\n"))
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

	input := ndjsonInput(
		textDeltaLine("doing work\n"),
		textDeltaLine("found a pattern\n"),
		textDeltaLine("finished\n"),
	)
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

	// Text content should be echoed to output for debugging visibility.
	if !strings.Contains(buf.String(), "doing work") {
		t.Errorf("expected text echo in output, got %q", buf.String())
	}
}

func TestDrainOutput_NilSpawner(t *testing.T) {
	// With nil spawner, ExtractWithLLM should be skipped (no panic, no LLM call).
	store := &mockMemStore{}
	input := ndjsonInput(
		textDeltaLine("doing work\n"),
		textDeltaLine("[MEMORY] type=lesson: explicit marker still captured\n"),
		textDeltaLine("more work\n"),
	)
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

	// Text content should be echoed to output for debugging visibility.
	if !strings.Contains(buf.String(), "doing work") {
		t.Errorf("expected text echo in output, got %q", buf.String())
	}
}
