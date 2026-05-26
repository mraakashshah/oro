package worker_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"oro/pkg/protocol"
	"oro/pkg/worker"
)

// mockMemStore captures Insert calls without a real DB.
type mockMemStore struct {
	inserted []protocol.MemoryInsertParams
}

func (m *mockMemStore) Insert(_ context.Context, p protocol.MemoryInsertParams) (int64, error) {
	m.inserted = append(m.inserted, p)
	return int64(len(m.inserted)), nil
}

// mockLLMSpawner implements worker.MemoryExtractSpawner for testing. It records whether Spawn
// was called and returns canned output.
type mockLLMSpawner struct {
	called      bool
	modelGiven  string
	promptGiven string
	output      string
}

func (m *mockLLMSpawner) Spawn(_ context.Context, model, prompt string) (io.ReadCloser, error) {
	m.called = true
	m.modelGiven = model
	m.promptGiven = prompt
	return io.NopCloser(strings.NewReader(m.output)), nil
}

type mockWorkdirLLMSpawner struct {
	mockLLMSpawner
	workdirCalled bool
	workdir       string
}

func (m *mockWorkdirLLMSpawner) SpawnInWorkdir(_ context.Context, model, prompt, workdir string) (io.ReadCloser, error) {
	m.workdirCalled = true
	m.called = true
	m.modelGiven = model
	m.promptGiven = prompt
	m.workdir = workdir
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

	worker.DrainOutput(context.Background(), reader, worker.StreamFormatClaudeJSON, nil, "oro-test", nil, &buf)

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

	worker.DrainOutput(context.Background(), reader, worker.StreamFormatClaudeJSON, store, "oro-bead1", nil, &buf)

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

func TestDrainOutput_FlushRemainingTrailingPartial(t *testing.T) {
	// Text deltas that never close with \n leave bytes in the line buffer.
	// drainFlushRemaining must flush them (and parse a [MEMORY] marker if
	// present) before DrainOutput returns.
	input := ndjsonInput(
		textDeltaLine("[MEMORY] type=lesson: trailing memory without newline"),
	)
	store := &mockMemStore{}
	worker.DrainOutput(context.Background(), io.NopCloser(strings.NewReader(input)),
		worker.StreamFormatClaudeJSON, store, "oro-bead-rem", nil, io.Discard)

	if len(store.inserted) != 1 {
		t.Fatalf("expected 1 memory inserted from flushed remainder, got %d", len(store.inserted))
	}
	if got := store.inserted[0].BeadID; got != "oro-bead-rem" {
		t.Errorf("BeadID = %q, want oro-bead-rem", got)
	}
	if !strings.Contains(store.inserted[0].Content, "trailing memory") {
		t.Errorf("content = %q, want substring 'trailing memory'", store.inserted[0].Content)
	}
}

func TestDrainOutput_FlushRemainingNoMarkerNoStore(t *testing.T) {
	// Trailing partial without a [MEMORY] marker and a nil store: covers the
	// non-marker / nil-store branches of drainFlushRemaining.
	input := ndjsonInput(textDeltaLine("plain trailing text without newline"))
	worker.DrainOutput(context.Background(), io.NopCloser(strings.NewReader(input)),
		worker.StreamFormatClaudeJSON, nil, "oro-bead-plain", nil, io.Discard)
}

func TestDrainOutput_NilStore(t *testing.T) {
	input := ndjsonInput(
		textDeltaLine("[MEMORY] type=lesson: should not panic\n"),
		textDeltaLine("regular line\n"),
	)
	reader := io.NopCloser(strings.NewReader(input))
	var buf bytes.Buffer

	// Should not panic with nil store.
	worker.DrainOutput(context.Background(), reader, worker.StreamFormatClaudeJSON, nil, "oro-test", nil, &buf)

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

	worker.DrainOutput(context.Background(), reader, worker.StreamFormatClaudeJSON, nil, "oro-test", nil, &buf1, &buf2)

	for i, buf := range []*bytes.Buffer{&buf1, &buf2} {
		s := buf.String()
		if !strings.Contains(s, "-> Read") || !strings.Contains(s, "-> Edit") {
			t.Fatalf("writer %d: expected '-> Read' and '-> Edit', got %q", i+1, s)
		}
	}
}

func TestDrainOutput_NilWriterFiltered(t *testing.T) {
	input := ndjsonInput(toolUseLine("Bash"))
	reader := io.NopCloser(strings.NewReader(input))
	var buf bytes.Buffer

	// nil writer in slice should not panic
	worker.DrainOutput(context.Background(), reader, worker.StreamFormatClaudeJSON, nil, "oro-test", nil, &buf, nil)

	if !strings.Contains(buf.String(), "-> Bash") {
		t.Fatalf("expected '-> Bash' in output, got %q", buf.String())
	}
}

func TestDrainOutput_NoWriters(t *testing.T) {
	input := ndjsonInput(textDeltaLine("hello\n"))
	reader := io.NopCloser(strings.NewReader(input))

	// empty writers slice — should not panic
	worker.DrainOutput(context.Background(), reader, worker.StreamFormatClaudeJSON, nil, "oro-test", nil)
}

func TestDrainOutput_EmptyInput(t *testing.T) {
	reader := io.NopCloser(strings.NewReader(""))
	var buf bytes.Buffer

	worker.DrainOutput(context.Background(), reader, worker.StreamFormatClaudeJSON, nil, "oro-test", nil, &buf)

	// Only stats line expected (no actual content).
	if !strings.Contains(buf.String(), "0 lines") {
		t.Fatalf("expected stats showing 0 lines, got %q", buf.String())
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

	worker.DrainOutput(context.Background(), reader, worker.StreamFormatClaudeJSON, store, "oro-llm1", spawner, &buf)

	// Spawner must have been called.
	if !spawner.called {
		t.Fatal("expected spawner.Spawn to be called after drain completes")
	}

	// The accumulated session text should contain all input lines.
	if spawner.modelGiven != "haiku" {
		t.Fatalf("extraction model = %q, want haiku", spawner.modelGiven)
	}
	if !strings.Contains(spawner.promptGiven, "insights — things a developer") {
		t.Fatalf("extraction prompt did not preserve memory extractor behavior: %q", spawner.promptGiven)
	}
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

func TestDrainOutputInWorkdir_BindsLLMExtractionToWorkdir(t *testing.T) {
	workdir := t.TempDir()
	spawner := &mockWorkdirLLMSpawner{
		mockLLMSpawner: mockLLMSpawner{
			output: "[MEMORY] type=lesson: extraction stayed in assigned worktree\n",
		},
	}
	store := &mockMemStore{}

	input := ndjsonInput(textDeltaLine("finished assigned task\n"))
	reader := io.NopCloser(strings.NewReader(input))
	var buf bytes.Buffer

	worker.DrainOutputInWorkdir(context.Background(), reader, worker.StreamFormatClaudeJSON, store, "oro-llm-wd", spawner, workdir, &buf)

	if !spawner.workdirCalled {
		t.Fatal("expected workdir-aware spawner to be used")
	}
	if spawner.workdir != workdir {
		t.Fatalf("spawner workdir = %q, want %q", spawner.workdir, workdir)
	}
	if len(store.inserted) == 0 {
		t.Fatal("expected LLM-extracted memory to be inserted")
	}
}

func TestDrainOutputInWorkdirCapturesMemoryMarkers(t *testing.T) {
	input := ndjsonInput(
		`{"type":"content_block_delta","delta":{"type":"text_delta","text":"starting drain\n"}}`,
		`{"type":"content_block_delta","delta":{"type":"text_delta","text":"[MEMORY] type=lesson tags=drain,claude-json: capture markers from JSON deltas\n"}}`,
		`{"type":"content_block_delta","delta":{"type":"text_delta","text":"normal stream output\n[MEMORY] type=gotcha: trailing partial marker"}}`,
	)
	store := &mockMemStore{}
	var buf bytes.Buffer

	worker.DrainOutputInWorkdir(context.Background(), io.NopCloser(strings.NewReader(input)),
		worker.StreamFormatClaudeJSON, store, "oro-cz9c", nil, t.TempDir(), &buf)

	if len(store.inserted) != 2 {
		t.Fatalf("expected 2 memory inserts, got %d: %#v", len(store.inserted), store.inserted)
	}
	for _, mem := range store.inserted {
		if mem.BeadID != "oro-cz9c" {
			t.Fatalf("memory BeadID = %q, want oro-cz9c", mem.BeadID)
		}
	}
	if store.inserted[0].Type != "lesson" {
		t.Fatalf("first memory Type = %q, want lesson", store.inserted[0].Type)
	}
	if !strings.Contains(store.inserted[0].Content, "capture markers from JSON deltas") {
		t.Fatalf("first memory Content = %q, want JSON delta marker content", store.inserted[0].Content)
	}
	if store.inserted[1].Type != "gotcha" {
		t.Fatalf("second memory Type = %q, want gotcha", store.inserted[1].Type)
	}

	got := buf.String()
	if !strings.Contains(got, "starting drain") || !strings.Contains(got, "normal stream output") {
		t.Fatalf("expected normal text deltas echoed to output, got %q", got)
	}

	worker.DrainOutputInWorkdir(context.Background(),
		io.NopCloser(strings.NewReader(ndjsonInput(
			`{"type":"content_block_delta","delta":{"type":"text_delta","text":"[MEMORY] type=lesson: nil store skips insert\n"}}`,
		))),
		worker.StreamFormatClaudeJSON, nil, "oro-cz9c", nil, t.TempDir(), io.Discard)
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

	worker.DrainOutput(context.Background(), reader, worker.StreamFormatClaudeJSON, store, "oro-nil-sp", nil, &buf)

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

func TestWorkerDrainSelectsParserByRuntimeFormat(t *testing.T) {
	store := &mockMemStore{}
	input := "[MEMORY] type=lesson: plain text markers still work\nregular text line\n"
	reader := io.NopCloser(strings.NewReader(input))
	var buf bytes.Buffer

	worker.DrainOutput(context.Background(), reader, worker.StreamFormatLineText, store, "oro-line", nil, &buf)

	if len(store.inserted) != 1 {
		t.Fatalf("expected 1 memory inserted, got %d", len(store.inserted))
	}
	if !strings.Contains(store.inserted[0].Content, "plain text markers still work") {
		t.Fatalf("memory content = %q, want plain text marker content", store.inserted[0].Content)
	}
	if !strings.Contains(buf.String(), "regular text line") {
		t.Fatalf("expected plain text output to be echoed, got %q", buf.String())
	}
}
