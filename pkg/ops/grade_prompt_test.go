package ops //nolint:testpackage // internal tests exercise unexported voice gate helpers

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
)

type sequenceBatchSpawner struct {
	mu      sync.Mutex
	outputs []string
	calls   []spawnCall
}

func (s *sequenceBatchSpawner) Spawn(_ context.Context, model, prompt, workdir string) (Process, error) {
	return s.spawn(spawnCall{model: model, prompt: prompt, workdir: workdir})
}

func (s *sequenceBatchSpawner) SpawnRuntime(_ context.Context, runtime, model, reasoning, prompt, workdir string) (Process, error) {
	return s.spawn(spawnCall{runtime: runtime, model: model, reasoning: reasoning, prompt: prompt, workdir: workdir})
}

func (s *sequenceBatchSpawner) spawn(call spawnCall) (Process, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.outputs) == 0 {
		return nil, errors.New("unexpected spawn")
	}
	out := s.outputs[0]
	s.outputs = s.outputs[1:]
	s.calls = append(s.calls, call)
	return newReadyMockProcess(out, nil), nil
}

func (s *sequenceBatchSpawner) getCalls() []spawnCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]spawnCall(nil), s.calls...)
}

func TestVoiceGate_RejectsOffVoiceProse(t *testing.T) {
	original := CardCandidate{
		Type:        "pattern",
		Title:       "Retry state",
		BodySummary: "Always check existing retry state before editing.",
		BodyFull:    "This awesome solution totally crushes the bug and makes everything amazing.",
		Confidence:  0.8,
		Evidence:    []string{"worker output"},
		Tags:        []string{"retry"},
	}
	spawner := &sequenceBatchSpawner{outputs: []string{
		`{"score":1,"feedback":"marketing hype; use terse operational prose"}`,
		`{"type":"pattern","title":"Retry state","body_summary":"Verify retry state before editing.","body_full":"On retry tasks, run the targeted acceptance test and inspect the branch before changing code; prior attempts may already satisfy the task.","confidence":0.8,"evidence":["worker output"],"tags":["retry"]}`,
		`{"score":5,"feedback":"fits the project voice"}`,
	}}

	got, err := voiceGate(context.Background(), spawner, original)
	if err != nil {
		t.Fatalf("voiceGate returned error: %v", err)
	}
	if strings.Contains(got.BodyFull, "awesome solution") {
		t.Fatalf("voiceGate kept off-voice body: %q", got.BodyFull)
	}
	if got.BodyFull != "On retry tasks, run the targeted acceptance test and inspect the branch before changing code; prior attempts may already satisfy the task." {
		t.Fatalf("voiceGate body = %q", got.BodyFull)
	}

	calls := spawner.getCalls()
	if len(calls) != 3 {
		t.Fatalf("spawn calls = %d, want 3", len(calls))
	}
	for _, call := range calls {
		if call.model != "haiku" {
			t.Fatalf("voice gate should use haiku, got model %q", call.model)
		}
	}
	if !strings.Contains(calls[1].prompt, "marketing hype") {
		t.Fatalf("regeneration prompt missing judge feedback: %s", calls[1].prompt)
	}
}

func TestVoiceGate_ParseFailureFallsBackToRaw(t *testing.T) {
	original := CardCandidate{
		Type:        "decision",
		Title:       "Keep raw learning",
		BodySummary: "Keep raw learning when parsing fails.",
		BodyFull:    "Raw extracted lesson text that must not be dropped.",
		Confidence:  0.7,
		Evidence:    []string{"session"},
		Tags:        []string{"voice-gate"},
	}
	spawner := &sequenceBatchSpawner{outputs: []string{"not json"}}

	got, err := voiceGate(context.Background(), spawner, original)
	if err != nil {
		t.Fatalf("voiceGate returned error: %v", err)
	}
	if !reflect.DeepEqual(got, original) {
		t.Fatalf("voiceGate parse fallback = %#v, want original %#v", got, original)
	}
}

func TestBuildVoiceGatePromptIncludesRubricAndCard(t *testing.T) {
	card := CardCandidate{Title: "Short title", BodyFull: "raw body"}
	got := buildVoiceGatePrompt(card)
	for _, want := range []string{"score", "1", "5", "terse", "Short title", "raw body"} {
		if !strings.Contains(got, want) {
			t.Fatalf("prompt missing %q:\n%s", want, got)
		}
	}
}
