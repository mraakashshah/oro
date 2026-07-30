package dispatcher //nolint:testpackage // white-box: constructs Dispatcher with fake stores for CloseBead promotion wiring

import (
	"context"
	"errors"
	"testing"
	"time"

	"oro/pkg/beadstore"
	"oro/pkg/cards"
	"oro/pkg/protocol"
)

func TestBeadCloseRunsPromotion(t *testing.T) {
	ctx := context.Background()

	t.Run("pass promotes eligible learning", func(t *testing.T) {
		store := beadstore.NewFakeStore(protocol.Bead{ID: "bead-pass", Type: "task", Status: "open"})
		cardStore := newPromotionCardStore(cards.PendingLearning{
			ID:     11,
			BeadID: "bead-pass",
			Candidate: cards.CardCandidate{
				Type:        string(cards.CardTypePattern),
				Title:       "Close promotions",
				BodySummary: "Close runs promotion.",
				BodyFull:    "Closing a bead runs pending learning promotion.",
				Confidence:  0.9,
				Evidence:    []string{"go test ./pkg/dispatcher/..."},
			},
		})
		d := &Dispatcher{beads: store, cardStore: cardStore}

		if err := d.CloseBead(ctx, "bead-pass", "Merged: abc123"); err != nil {
			t.Fatalf("CloseBead: %v", err)
		}

		if got := cardStore.promoted; !equalInt64s(got, []int64{11}) {
			t.Fatalf("promoted learnings = %v, want [11]", got)
		}
		assertJourneyEvent(t, store, "bead-pass", "learning_promoted")
	})

	t.Run("pass queues eligible learning as proposal when grade gate enabled", func(t *testing.T) {
		store := beadstore.NewFakeStore(protocol.Bead{ID: "bead-proposed", Type: "task", Status: "open"})
		cardStore := newPromotionCardStore(cards.PendingLearning{
			ID:     14,
			BeadID: "bead-proposed",
			Candidate: cards.CardCandidate{
				Type:        string(cards.CardTypePattern),
				Title:       "Gate promotes to proposal",
				BodySummary: "Grade-gated promotion enters proposal queue.",
				BodyFull:    "When GradeGateEnabled is true, pending learning promotion creates a proposed card.",
				Confidence:  0.91,
				Evidence:    []string{"go test ./pkg/dispatcher/..."},
			},
		})
		d := &Dispatcher{
			beads:     store,
			cardStore: cardStore,
			cfg:       Config{GradeGateEnabled: true},
		}

		if err := d.CloseBead(ctx, "bead-proposed", "Merged: abc123"); err != nil {
			t.Fatalf("CloseBead: %v", err)
		}

		if got := cardStore.promoted; len(got) != 0 {
			t.Fatalf("direct promoted learnings = %v, want none", got)
		}
		if got := cardStore.proposed; !equalInt64s(got, []int64{14}) {
			t.Fatalf("proposed learnings = %v, want [14]", got)
		}
		assertJourneyEvent(t, store, "bead-proposed", "learning_promoted")
	})

	t.Run("fail rejects pending learning", func(t *testing.T) {
		store := beadstore.NewFakeStore(protocol.Bead{ID: "bead-fail", Type: "task", Status: "open"})
		cardStore := newPromotionCardStore(cards.PendingLearning{
			ID:     12,
			BeadID: "bead-fail",
			Candidate: cards.CardCandidate{
				Type:        string(cards.CardTypePattern),
				Title:       "Rejected learning",
				BodySummary: "Failed review rejects learning.",
				BodyFull:    "Failed review rejects pending learnings from the bead.",
				Confidence:  0.95,
				Evidence:    []string{"review failed"},
			},
		})
		d := &Dispatcher{beads: store, cardStore: cardStore}

		if err := d.CloseBead(ctx, "bead-fail", "review failed: missing tests"); err != nil {
			t.Fatalf("CloseBead: %v", err)
		}

		if got := cardStore.rejected; !equalInt64s(got, []int64{12}) {
			t.Fatalf("rejected learnings = %v, want [12]", got)
		}
	})

	t.Run("no verdict rejects pending learning", func(t *testing.T) {
		store := beadstore.NewFakeStore(protocol.Bead{ID: "bead-force", Type: "task", Status: "open"})
		cardStore := newPromotionCardStore(cards.PendingLearning{
			ID:     13,
			BeadID: "bead-force",
			Candidate: cards.CardCandidate{
				Type:        string(cards.CardTypePattern),
				Title:       "Force close learning",
				BodySummary: "Force close rejects learning.",
				BodyFull:    "Force-closing without a review verdict rejects the learning.",
				Confidence:  0.95,
				Evidence:    []string{"manual close"},
			},
		})
		d := &Dispatcher{beads: store, cardStore: cardStore}

		if err := d.CloseBead(ctx, "bead-force", "manual force close"); err != nil {
			t.Fatalf("CloseBead: %v", err)
		}

		if got := cardStore.rejected; !equalInt64s(got, []int64{13}) {
			t.Fatalf("rejected learnings = %v, want [13]", got)
		}
	})

	t.Run("no pending learnings is no-op", func(t *testing.T) {
		store := beadstore.NewFakeStore(protocol.Bead{ID: "bead-empty", Type: "task", Status: "open"})
		cardStore := newPromotionCardStore()
		d := &Dispatcher{beads: store, cardStore: cardStore}

		if err := d.CloseBead(ctx, "bead-empty", "Merged: def456"); err != nil {
			t.Fatalf("CloseBead: %v", err)
		}
		if cardStore.decisions != 0 {
			t.Fatalf("promotion decisions = %d, want 0", cardStore.decisions)
		}
	})

	t.Run("nil card store skips promotion", func(t *testing.T) {
		store := beadstore.NewFakeStore(protocol.Bead{ID: "bead-nil", Type: "task", Status: "open"})
		d := &Dispatcher{beads: store}

		if err := d.CloseBead(ctx, "bead-nil", "Merged: fed789"); err != nil {
			t.Fatalf("CloseBead: %v", err)
		}
	})
}

type promotionCardStore struct {
	pending   []cards.PendingLearning
	promoted  []int64
	proposed  []int64
	rejected  []int64
	deferred  []int64
	decisions int
}

func newPromotionCardStore(pending ...cards.PendingLearning) *promotionCardStore {
	return &promotionCardStore{pending: pending}
}

func (s *promotionCardStore) Relevant(context.Context, cards.RelevanceQuery) (cards.RelevantCards, error) {
	return cards.RelevantCards{}, nil
}

func (s *promotionCardStore) Show(context.Context, string) (*cards.Card, error) {
	return nil, errors.New("not implemented")
}

func (s *promotionCardStore) List(context.Context, cards.ListQuery) ([]cards.Card, error) {
	return nil, nil
}

func (s *promotionCardStore) ListProposed(context.Context) ([]cards.Card, error) {
	return nil, errors.New("not implemented")
}

func (s *promotionCardStore) PendingLearnings(_ context.Context, beadID string) ([]cards.PendingLearning, error) {
	var out []cards.PendingLearning
	for _, learning := range s.pending {
		if learning.BeadID == beadID {
			out = append(out, learning)
		}
	}
	return out, nil
}

func (s *promotionCardStore) ReviewQueue(context.Context) ([]cards.PendingLearning, error) {
	return nil, errors.New("not implemented")
}

func (s *promotionCardStore) RecordCardEvent(context.Context, cards.CardEvent) error {
	return errors.New("not implemented")
}

func (s *promotionCardStore) AppendLearningPending(context.Context, string, cards.CardCandidate) (int64, error) {
	return 0, errors.New("not implemented")
}

func (s *promotionCardStore) PromoteLearning(_ context.Context, id int64) (string, error) {
	s.decisions++
	s.promoted = append(s.promoted, id)
	return "card-promoted", nil
}

func (s *promotionCardStore) PromoteLearningAsProposal(_ context.Context, id int64) (string, error) {
	s.decisions++
	s.proposed = append(s.proposed, id)
	return "card-proposed", nil
}

func (s *promotionCardStore) ResolveProposal(context.Context, string, cards.GradeOutcome) error {
	return errors.New("not implemented")
}

func (s *promotionCardStore) RejectLearning(_ context.Context, id int64, _ string) error {
	s.decisions++
	s.rejected = append(s.rejected, id)
	return nil
}

func (s *promotionCardStore) DeferToReviewQueue(_ context.Context, id int64, _ string) error {
	s.decisions++
	s.deferred = append(s.deferred, id)
	return nil
}

func (s *promotionCardStore) Create(context.Context, cards.CardCreateParams) (*cards.Card, error) {
	return nil, errors.New("not implemented")
}

func (s *promotionCardStore) Retire(context.Context, string, string, string) error {
	return errors.New("not implemented")
}

func (s *promotionCardStore) AddRelation(context.Context, string, string, cards.RelationSignal) error {
	return errors.New("not implemented")
}

func (s *promotionCardStore) SeeAlso(context.Context, string, int) ([]cards.CardSummary, error) {
	return nil, errors.New("not implemented")
}

func (s *promotionCardStore) Lineage(context.Context, string) ([]cards.Card, error) {
	return nil, errors.New("not implemented")
}

func (s *promotionCardStore) LatestInChain(context.Context, string) (*cards.Card, error) {
	return nil, errors.New("not implemented")
}

func (s *promotionCardStore) Reindex(context.Context) (int, error) {
	return 0, errors.New("not implemented")
}

func (s *promotionCardStore) WithReadTx(context.Context, func(cards.ReadTx) error) error {
	return errors.New("not implemented")
}

func assertJourneyEvent(t *testing.T, store *beadstore.FakeStore, beadID, event string) {
	t.Helper()

	events, err := store.Journey(context.Background(), beadID, time.Time{})
	if err != nil {
		t.Fatalf("Journey: %v", err)
	}
	for _, evt := range events {
		if evt.Event == event {
			return
		}
	}
	t.Fatalf("journey missing event %q: %+v", event, events)
}

func equalInt64s(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
