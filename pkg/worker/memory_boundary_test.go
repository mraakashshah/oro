//nolint:testpackage
package worker

import (
	"context"
	"io"
	"strings"
	"testing"

	"oro/pkg/cards"
)

type boundaryMemorySink struct {
	candidates []cards.CardCandidate
}

func (s *boundaryMemorySink) AppendLearningPending(_ context.Context, _ string, c cards.CardCandidate) (int64, error) {
	s.candidates = append(s.candidates, c)
	return int64(len(s.candidates)), nil
}

type sourceAwareSpawner struct {
	prompts []string
}

func (s *sourceAwareSpawner) Spawn(_ context.Context, _, prompt string) (io.ReadCloser, error) {
	s.prompts = append(s.prompts, prompt)
	if strings.Contains(prompt, "mid-session discovery survives threshold flush") {
		return io.NopCloser(strings.NewReader("[MEMORY] type=lesson tags=reader-source: mid-session discovery survives threshold flush\n")), nil
	}
	return io.NopCloser(strings.NewReader("")), nil
}

func TestExtractLearnings_AcceptsReaderSource(t *testing.T) {
	spawner := &sourceAwareSpawner{}
	src := strings.NewReader("reader source includes mid-session discovery survives threshold flush")

	candidates, err := ExtractMemoriesFromReader(context.Background(), src, spawner, "")
	if err != nil {
		t.Fatalf("ExtractMemoriesFromReader returned error: %v", err)
	}

	if len(candidates) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(candidates))
	}
	if !strings.Contains(candidates[0].BodyFull, "mid-session discovery survives threshold flush") {
		t.Fatalf("candidate BodyFull = %q, want reader-sourced discovery", candidates[0].BodyFull)
	}
	if len(spawner.prompts) != 1 {
		t.Fatalf("expected one extractor prompt, got %d", len(spawner.prompts))
	}
	if !strings.Contains(spawner.prompts[0], "reader source includes") {
		t.Fatalf("extractor prompt did not include reader source: %q", spawner.prompts[0])
	}
}

func TestFlush_CapturesMidSessionDiscovery(t *testing.T) {
	spawner := &sourceAwareSpawner{}
	sink := &boundaryMemorySink{}
	w := &Worker{
		ID:             "worker-memory-boundary",
		beadID:         "oro-memory-boundary",
		memStore:       sink,
		extractSpawner: spawner,
		streamFormat:   StreamFormatLineText,
	}

	w.processTextLine(context.Background(), "mid-session discovery survives threshold flush")
	fillerLine := strings.Repeat("trailing filler that should push old content out of the final 50KB", 40)
	for i := 0; i < maxMemorySessionBytes/len(fillerLine)+2; i++ {
		w.processTextLine(context.Background(), strings.Repeat("trailing filler that should push old content out of the final 50KB", 40))
	}
	w.processTextLine(context.Background(), "final tail has no useful insight")
	w.extractImplicitMemories(context.Background())

	if len(sink.candidates) != 1 {
		t.Fatalf("expected mid-session discovery candidate, got %d candidates: %#v", len(sink.candidates), sink.candidates)
	}
	if !strings.Contains(sink.candidates[0].BodyFull, "mid-session discovery survives threshold flush") {
		t.Fatalf("candidate BodyFull = %q, want mid-session discovery", sink.candidates[0].BodyFull)
	}
	if len(spawner.prompts) < 2 {
		t.Fatalf("expected threshold flush plus final flush, got %d extractor prompts", len(spawner.prompts))
	}
	if strings.Contains(spawner.prompts[len(spawner.prompts)-1], "mid-session discovery survives threshold flush") {
		t.Fatalf("final extractor prompt unexpectedly retained mid-session discovery after threshold flush")
	}
}
