package ops //nolint:testpackage // internal tests exercise unexported voice gate helpers

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"oro/pkg/cards"
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

func TestBuildGradePrompt_IncludesEvidence(t *testing.T) {
	proposal := cards.Card{
		ID:          "card-proposal",
		Type:        cards.CardTypePattern,
		Title:       "Verify retry state",
		BodySummary: "Run targeted acceptance before editing retry tasks.",
		BodyFull:    "On retry tasks, inspect the branch and run the acceptance command before changing code.",
		Tags:        []string{"retry", "qg"},
	}
	evidence := GradeEvidence{
		Events: []cards.CardEvent{
			{CardID: "card-prior", BeadID: "oro-old", Actor: "worker", Kind: "nack", Payload: "prior version contradicted acceptance output"},
			{CardID: "card-prior", BeadID: "oro-old", Actor: "ops", Kind: "contradicted", Payload: "review found stale retry assumption"},
		},
		SeeAlso: []cards.CardSummary{
			{
				ID:          "card-related",
				Type:        cards.CardTypePattern,
				Title:       "Verify retry state before editing",
				BodySummary: "Retry attempts can already contain the fix.",
				BodyFull:    "Run the exact acceptance command first and inspect branch state before editing retry tasks.",
				Score:       0.91,
				Tags:        []string{"retry"},
			},
		},
		VectorNeighbors: []cards.CardSummary{
			{
				ID:          "card-neighbor",
				Type:        cards.CardTypeDecision,
				Title:       "Keep prompt evidence local",
				BodySummary: "Grade prompts should cite local Oro evidence.",
				BodyFull:    "Use card history, related cards, vector neighbours, and bead outcomes.",
				Score:       0.87,
				Tags:        []string{"grade"},
			},
		},
		OriginatingBead: GradeBeadEvidence{
			ID:           "oro-s08-p3c",
			Title:        "P3c: grade prompt + oro-native evidence retriever",
			Type:         "task",
			QGOutcome:    "passed",
			MergeOutcome: "merged into epic/oro-spec08",
			Summary:      "acceptance test proved the prompt included evidence",
		},
	}

	got := buildGradePrompt(proposal, evidence)
	for _, want := range []string{
		"card-proposal",
		"Verify retry state",
		"On retry tasks, inspect the branch",
		"card_events history",
		"prior version contradicted acceptance output",
		"SeeAlso related cards",
		"card-related",
		"Phase 2 vector neighbours",
		"card-neighbor",
		"originating bead",
		"oro-s08-p3c",
		"passed",
		"merged into epic/oro-spec08",
		"Return only JSON",
		"verdict",
		"confidence",
		"reasoning",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("grade prompt missing %q:\n%s", want, got)
		}
	}

	parsed, ok := parseGradeWorkerOutput(`{"verdict":"correct","confidence":0.96,"reasoning":"Evidence supports the proposal."}`)
	if !ok {
		t.Fatal("parseGradeWorkerOutput rejected valid JSON")
	}
	if parsed.Verdict != GradeVerdictCorrect || parsed.Confidence != 0.96 || parsed.Reasoning != "Evidence supports the proposal." {
		t.Fatalf("parsed grade output = %#v", parsed)
	}
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
