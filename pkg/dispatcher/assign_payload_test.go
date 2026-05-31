package dispatcher //nolint:testpackage // internal white-box tests need access to unexported fields

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"oro/pkg/cards"
	"oro/pkg/protocol"
)

// TestAssignPayloadUsesProjectPaths verifies that buildAssignPayload reads the
// worker-program.md from cfg.WorkerProgram (populated from ProjectPaths) rather
// than the hardcoded filepath.Join(cfg.RepoRoot, "worker-program.md").
func TestAssignPayloadUsesProjectPaths(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)

	repoRoot := t.TempDir()
	d.cfg.RepoRoot = repoRoot

	// Place worker-program.md at a custom path that differs from
	// repoRoot/worker-program.md. This proves cfg.WorkerProgram is used.
	customDir := t.TempDir()
	customWorkerProgramPath := filepath.Join(customDir, "worker-program.md")
	wpContent := "# Project-Specific Worker Program\nThis is NOT at repoRoot."
	if err := os.WriteFile(customWorkerProgramPath, []byte(wpContent), 0o600); err != nil {
		t.Fatal(err)
	}

	d.cfg.WorkerProgram = customWorkerProgramPath

	// Explicitly do NOT write worker-program.md at repoRoot so that if
	// buildAssignPayload falls back to the hardcoded path it gets empty content.

	beadSrc.shown["bead-wp"] = &protocol.BeadDetail{Title: "Bead WP"}

	w := &trackedWorker{
		id:     "worker-1",
		beadID: "bead-wp",
	}
	d.shutdownRunner = &mockCommandRunner{output: []byte("abc git log")}

	got := d.buildAssignPayload(context.Background(), w, 1, "", "")

	if got.WorkerProgram != wpContent {
		t.Errorf("WorkerProgram = %q, want %q\n(cfg.WorkerProgram should be used, not filepath.Join(repoRoot, \"worker-program.md\"))",
			got.WorkerProgram, wpContent)
	}
}

func TestBuildCardContextKeepsAssignPayloadUnderProtocolLimit(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)

	deck := make([]cards.DeckCard, 0, 5000)
	for i := 0; i < cap(deck); i++ {
		deck = append(deck, cards.DeckCard{
			ID:          "card-large-deck",
			Type:        cards.CardTypePattern,
			Title:       "Large card deck",
			BodySummary: strings.Repeat("summary ", 30),
			Score:       1.0,
			Tags:        []string{"dispatcher", "cards"},
		})
	}
	inlined := []cards.InlinedCard{
		{
			ID:          "card-large-inline",
			Type:        cards.CardTypePattern,
			Title:       "Large inline card",
			BodySummary: strings.Repeat("summary ", 30),
			BodyFull:    "INLINE_ONLY_FULL_BODY_SENTINEL " + strings.Repeat("full ", 40),
			Score:       1.0,
			Tags:        []string{"dispatcher", "cards"},
		},
	}
	d.cardStore = &staticRelevantCardStore{
		result: cards.RelevantCards{
			Deck:    deck,
			Inlined: inlined,
		},
	}

	got := d.buildCardContext(context.Background(), protocol.Bead{ID: "bead-large-cards", Title: "large card deck"})
	if len(got.Deck) >= len(deck) {
		t.Fatalf("card deck length = %d, want capped below %d", len(got.Deck), len(deck))
	}
	if len(got.Deck) == 0 {
		t.Fatal("expected capped deck to retain top cards")
	}
	if got.Deck[0].ID != "card-large-deck" {
		t.Fatalf("first deck card = %q, want card-large-deck", got.Deck[0].ID)
	}

	msg := protocol.Message{
		Type: protocol.MsgAssign,
		Assign: &protocol.AssignPayload{
			BeadID:            "bead-large-cards",
			Worktree:          "/tmp/bead-large-cards",
			WorkerProgram:     strings.Repeat("w", maxWorkerProgramSize),
			CodeSearchContext: strings.Repeat("c", maxCodeSearchContextSize),
			Cards:             got,
		},
	}
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal assign message: %v", err)
	}
	if len(data) >= protocol.MaxMessageSize {
		t.Fatalf("ASSIGN message size = %d, want < MaxMessageSize %d", len(data), protocol.MaxMessageSize)
	}
	marshaled := string(data)
	if strings.Contains(marshaled, "DECK_ONLY_FULL_BODY_SENTINEL") {
		t.Fatal("marshaled ASSIGN includes deck-only full body sentinel")
	}
	if !strings.Contains(marshaled, "INLINE_ONLY_FULL_BODY_SENTINEL") {
		t.Fatal("marshaled ASSIGN excludes inline full body sentinel")
	}
}

func TestTrimAssignmentCardContextEdges(t *testing.T) {
	deck := []cards.DeckCard{{ID: "deck-1"}}
	inlined := []cards.InlinedCard{{ID: "inline-1"}}

	if got := trimDeckCardsByJSONSize(deck, 0); got != nil {
		t.Fatalf("trimDeckCardsByJSONSize maxSize=0 = %#v, want nil", got)
	}
	if got := trimInlinedCardsByJSONSize(inlined, 0); got != nil {
		t.Fatalf("trimInlinedCardsByJSONSize maxSize=0 = %#v, want nil", got)
	}
}

type staticRelevantCardStore struct {
	result cards.RelevantCards
}

func (s *staticRelevantCardStore) Relevant(context.Context, cards.RelevanceQuery) (cards.RelevantCards, error) {
	return s.result, nil
}

func (s *staticRelevantCardStore) Show(context.Context, string) (*cards.Card, error) {
	return nil, errors.New("not implemented")
}

func (s *staticRelevantCardStore) List(context.Context, cards.ListQuery) ([]cards.Card, error) {
	return nil, errors.New("not implemented")
}

func (s *staticRelevantCardStore) PendingLearnings(context.Context, string) ([]cards.PendingLearning, error) {
	return nil, errors.New("not implemented")
}

func (s *staticRelevantCardStore) ReviewQueue(context.Context) ([]cards.PendingLearning, error) {
	return nil, errors.New("not implemented")
}

func (s *staticRelevantCardStore) RecordCardEvent(context.Context, cards.CardEvent) error {
	return errors.New("not implemented")
}

func (s *staticRelevantCardStore) AppendLearningPending(context.Context, string, cards.CardCandidate) (int64, error) {
	return 0, errors.New("not implemented")
}

func (s *staticRelevantCardStore) PromoteLearning(context.Context, int64) (string, error) {
	return "", errors.New("not implemented")
}

func (s *staticRelevantCardStore) RejectLearning(context.Context, int64, string) error {
	return errors.New("not implemented")
}

func (s *staticRelevantCardStore) DeferToReviewQueue(context.Context, int64, string) error {
	return errors.New("not implemented")
}

func (s *staticRelevantCardStore) Create(context.Context, cards.CardCreateParams) (*cards.Card, error) {
	return nil, errors.New("not implemented")
}

func (s *staticRelevantCardStore) Retire(context.Context, string, string, string) error {
	return errors.New("not implemented")
}

func (s *staticRelevantCardStore) WithReadTx(context.Context, func(cards.ReadTx) error) error {
	return errors.New("not implemented")
}
