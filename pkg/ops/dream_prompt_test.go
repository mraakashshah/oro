package ops //nolint:testpackage // internal test needs access to unexported buildDreamPrompt

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestOpsDreamModel(t *testing.T) {
	if OpsDream.Model() != "haiku" {
		t.Fatalf("OpsDream.Model() = %q, want %q", OpsDream.Model(), "haiku")
	}
}

func TestOpsDreamTimeout(t *testing.T) {
	if OpsDream.Timeout() != 60*time.Second {
		t.Fatalf("OpsDream.Timeout() = %v, want %v", OpsDream.Timeout(), 60*time.Second)
	}
}

func TestDreamPromptContainsMemories(t *testing.T) {
	opts := DreamOpts{Memories: "memory: user prefers functional patterns"}
	prompt := buildDreamPrompt(opts)
	if !strings.Contains(prompt, "memory: user prefers functional patterns") {
		t.Errorf("prompt does not contain Memories content: %s", prompt)
	}
}

func TestDreamPromptEmptyMemoriesIsValid(t *testing.T) {
	opts := DreamOpts{Memories: ""}
	prompt := buildDreamPrompt(opts)
	if prompt == "" {
		t.Error("prompt should not be empty even with no memories")
	}
}

func TestDreamPromptIncludesActiveBiasTags(t *testing.T) {
	opts := DreamOpts{ActiveBiasTags: []string{"low_accuracy:rule:bug"}}
	prompt := buildDreamPrompt(opts)
	if !strings.Contains(prompt, "active_bias_tags: low_accuracy:rule:bug") {
		t.Fatalf("prompt missing active bias tags: %s", prompt)
	}
}

func TestDreamSpawnerReturnsChan(t *testing.T) {
	proc := newReadyMockProcess("", nil)
	mock := &mockBatchSpawner{process: proc}
	s := NewSpawner(mock)

	ch := s.Dream(context.Background(), DreamOpts{Memories: "some memory"})
	if ch == nil {
		t.Fatal("Dream() returned nil channel")
	}
	waitResult(t, ch)
}

func TestDreamSpawnerUsesTerraLow(t *testing.T) {
	proc := newReadyMockProcess("", nil)
	mock := &mockBatchSpawner{process: proc}
	s := NewSpawner(mock)

	ch := s.Dream(context.Background(), DreamOpts{Memories: "some memory"})
	waitResult(t, ch)

	calls := mock.getCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 spawn call, got %d", len(calls))
	}
	if calls[0].model != "gpt-5.6-terra" || calls[0].reasoning != "low" {
		t.Fatalf("Dream expected Terra low, got model=%q reasoning=%q", calls[0].model, calls[0].reasoning)
	}
}

func TestParseResultHandlesOpsDream(t *testing.T) {
	feedback := "dreamed about code patterns and refactoring opportunities"
	result := parseResult(OpsDream, "", feedback, nil)
	if result.Type != OpsDream {
		t.Fatalf("expected OpsDream, got %q", result.Type)
	}
	if result.Feedback != feedback {
		t.Fatalf("expected feedback %q, got %q", feedback, result.Feedback)
	}
}
