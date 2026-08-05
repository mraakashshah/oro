package cards_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"oro/pkg/cards"
)

func TestPromoteLearning(t *testing.T) {
	ctx := context.Background()

	t.Run("creates_card_resolves_learning_and_records_created_event", func(t *testing.T) {
		store, db := newTestStoreWithBeads(t)
		if _, err := db.ExecContext(ctx, `INSERT INTO beads (id) VALUES (?)`, "bead-1"); err != nil {
			t.Fatalf("insert bead: %v", err)
		}
		candidate := cards.CardCandidate{
			Type:        string(cards.CardTypePattern),
			Title:       "Use shared tx for promotion",
			BodySummary: "Promoting a learning resolves it atomically.",
			BodyFull:    "Create the card, mark the learning promoted, and write the created event in one transaction.",
			Confidence:  0.87,
			Evidence:    []string{"go test ./pkg/cards/... -run TestPromoteLearning -count=1"},
			Tags:        []string{"cards", "tx"},
		}
		learningID, err := store.AppendLearningPending(ctx, "bead-1", candidate)
		if err != nil {
			t.Fatalf("AppendLearningPending: %v", err)
		}

		cardID, err := store.PromoteLearning(ctx, learningID)
		if err != nil {
			t.Fatalf("PromoteLearning: %v", err)
		}
		if cardID == "" {
			t.Fatal("PromoteLearning cardID is empty")
		}

		card, err := store.Show(ctx, cardID)
		if err != nil {
			t.Fatalf("Show promoted card: %v", err)
		}
		if card.Title != candidate.Title || card.Type != cards.CardTypePattern {
			t.Fatalf("promoted card = %+v, want candidate %+v", card, candidate)
		}
		if card.PromotionConfidence == nil || *card.PromotionConfidence != candidate.Confidence {
			t.Fatalf("PromotionConfidence = %v, want %v", card.PromotionConfidence, candidate.Confidence)
		}
		if card.EmergedFrom == nil || *card.EmergedFrom != "bead-1" {
			t.Fatalf("EmergedFrom = %v, want bead-1", card.EmergedFrom)
		}

		var promotedTo string
		if err := db.QueryRowContext(ctx,
			`SELECT promoted_to FROM bead_learnings_pending WHERE id = ?`, learningID,
		).Scan(&promotedTo); err != nil {
			t.Fatalf("query promoted_to: %v", err)
		}
		if promotedTo != cardID {
			t.Fatalf("promoted_to = %q, want %q", promotedTo, cardID)
		}

		var eventCount int
		if err := db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM card_events WHERE card_id = ? AND kind = 'created'`, cardID,
		).Scan(&eventCount); err != nil {
			t.Fatalf("query created event: %v", err)
		}
		if eventCount != 1 {
			t.Fatalf("created event count = %d, want 1", eventCount)
		}
	})

	t.Run("already_resolved_learning_returns_ErrAlreadyResolved", func(t *testing.T) {
		store, db := newTestStoreWithBeads(t)
		if _, err := db.ExecContext(ctx, `INSERT INTO beads (id) VALUES (?)`, "bead-1"); err != nil {
			t.Fatalf("insert bead: %v", err)
		}
		learningID, err := store.AppendLearningPending(ctx, "bead-1", cards.CardCandidate{
			Type:        string(cards.CardTypePattern),
			Title:       "Already resolved",
			BodySummary: "Already resolved learnings are not promoted twice.",
			BodyFull:    "The store rejects a learning once promoted_to or rejected_at is set.",
			Confidence:  0.7,
			Evidence:    []string{"fixture"},
		})
		if err != nil {
			t.Fatalf("AppendLearningPending: %v", err)
		}
		if _, err := db.ExecContext(ctx,
			`UPDATE bead_learnings_pending SET rejected_at = '2026-01-01T00:00:00Z', reason = 'duplicate' WHERE id = ?`,
			learningID,
		); err != nil {
			t.Fatalf("mark rejected: %v", err)
		}

		_, err = store.PromoteLearning(ctx, learningID)
		if !errors.Is(err, cards.ErrAlreadyResolved) {
			t.Fatalf("PromoteLearning err = %v, want ErrAlreadyResolved", err)
		}
	})

	t.Run("rolls_back_when_card_create_fails", func(t *testing.T) {
		store, db := newTestStoreWithBeads(t)
		if _, err := db.ExecContext(ctx, `INSERT INTO beads (id) VALUES (?)`, "bead-1"); err != nil {
			t.Fatalf("insert bead: %v", err)
		}
		learningID, err := store.AppendLearningPending(ctx, "bead-1", cards.CardCandidate{
			Type:        "unknown",
			Title:       "Bad candidate",
			BodySummary: "Invalid card types cannot be promoted.",
			BodyFull:    "The learning must remain unresolved if card creation fails.",
			Confidence:  0.7,
			Evidence:    []string{"fixture"},
		})
		if err != nil {
			t.Fatalf("AppendLearningPending: %v", err)
		}

		_, err = store.PromoteLearning(ctx, learningID)
		if err == nil {
			t.Fatal("PromoteLearning err = nil, want create failure")
		}

		var promotedTo *string
		if err := db.QueryRowContext(ctx,
			`SELECT promoted_to FROM bead_learnings_pending WHERE id = ?`, learningID,
		).Scan(&promotedTo); err != nil {
			t.Fatalf("query promoted_to: %v", err)
		}
		if promotedTo != nil {
			t.Fatalf("promoted_to = %v, want nil after rollback", *promotedTo)
		}
		var cardCount, eventCount int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM cards`).Scan(&cardCount); err != nil {
			t.Fatalf("query cards count: %v", err)
		}
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM card_events`).Scan(&eventCount); err != nil {
			t.Fatalf("query card events count: %v", err)
		}
		if cardCount != 0 || eventCount != 0 {
			t.Fatalf("card/event counts = %d/%d, want 0/0 after rollback", cardCount, eventCount)
		}
	})
}

func TestPromoteLearningReportsMissingLearning(t *testing.T) {
	store, _ := newTestStoreWithBeads(t)

	_, err := store.PromoteLearning(context.Background(), 404)
	if !errors.Is(err, cards.ErrNotFound) {
		t.Fatalf("PromoteLearning err = %v, want ErrNotFound", err)
	}
}

func TestPromoteLearningChecksTerminalStateBeforeCreatingCard(t *testing.T) {
	ctx := context.Background()
	for _, terminal := range []string{"promoted", "rejected"} {
		t.Run(terminal, func(t *testing.T) {
			store, db := newTestStoreWithBeads(t)
			if _, err := db.ExecContext(ctx, `INSERT INTO beads (id) VALUES ('bead-1')`); err != nil {
				t.Fatalf("insert bead: %v", err)
			}
			learningID, err := store.AppendLearningPending(ctx, "bead-1", mutationLearningCandidate())
			if err != nil {
				t.Fatalf("AppendLearningPending: %v", err)
			}
			if terminal == "promoted" {
				if _, err := db.ExecContext(ctx, `
					INSERT INTO cards (id, type, title, body_summary, body_full, tags, decay_anchor, created_at, updated_at)
					VALUES ('existing-card', 'pattern', 'existing', 'summary', 'body', '[]',
						'2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z');
					UPDATE bead_learnings_pending SET promoted_to = 'existing-card' WHERE id = ?`, learningID); err != nil {
					t.Fatalf("mark promoted: %v", err)
				}
			} else if _, err := db.ExecContext(ctx,
				`UPDATE bead_learnings_pending SET rejected_at = '2026-01-01T00:00:00Z' WHERE id = ?`, learningID,
			); err != nil {
				t.Fatalf("mark rejected: %v", err)
			}
			if _, err := db.ExecContext(ctx, `
				CREATE TRIGGER reject_unexpected_card_insert
				BEFORE INSERT ON cards BEGIN
					SELECT RAISE(ABORT, 'card creation reached');
				END`); err != nil {
				t.Fatalf("create card insert trigger: %v", err)
			}

			_, err = store.PromoteLearning(ctx, learningID)
			if !errors.Is(err, cards.ErrAlreadyResolved) {
				t.Fatalf("PromoteLearning err = %v, want ErrAlreadyResolved before card creation", err)
			}
		})
	}
}

func TestPromoteLearningWithJourneyRollsBackOnJourneyFailures(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name       string
		payload    func(string) (string, error)
		breakStore bool
	}{
		{
			name: "payload construction",
			payload: func(string) (string, error) {
				return "", errors.New("injected payload failure")
			},
		},
		{
			name:       "journey insertion",
			payload:    func(string) (string, error) { return `{"source":"test"}`, nil },
			breakStore: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, db := newTestStoreWithBeads(t)
			if _, err := db.ExecContext(ctx, `
				INSERT INTO beads (id) VALUES ('bead-1');
				CREATE TABLE bead_journey (
					id INTEGER PRIMARY KEY AUTOINCREMENT,
					bead_id TEXT NOT NULL,
					ts TEXT NOT NULL,
					actor TEXT NOT NULL,
					event TEXT NOT NULL,
					payload TEXT
				)`); err != nil {
				t.Fatalf("create journey fixture: %v", err)
			}
			if tc.breakStore {
				if _, err := db.ExecContext(ctx, `
					CREATE TRIGGER reject_journey_insert
					BEFORE INSERT ON bead_journey BEGIN
						SELECT RAISE(ABORT, 'injected journey failure');
					END`); err != nil {
					t.Fatalf("create journey trigger: %v", err)
				}
			}
			learningID, err := store.AppendLearningPending(ctx, "bead-1", mutationLearningCandidate())
			if err != nil {
				t.Fatalf("AppendLearningPending: %v", err)
			}

			_, err = store.PromoteLearningWithJourney(ctx, learningID, false, cards.LearningPromotionJourney{
				BeadID:  "bead-1",
				Ts:      "2026-01-01T00:00:00Z",
				Actor:   "mutation-test",
				Event:   "learning_promoted",
				Payload: tc.payload,
			})
			if err == nil {
				t.Fatal("PromoteLearningWithJourney err = nil, want journey failure")
			}
			var promotedTo sql.NullString
			if err := db.QueryRowContext(ctx,
				`SELECT promoted_to FROM bead_learnings_pending WHERE id = ?`, learningID,
			).Scan(&promotedTo); err != nil {
				t.Fatalf("query promoted_to: %v", err)
			}
			if promotedTo.Valid {
				t.Fatalf("promoted_to = %q, want NULL after rollback", promotedTo.String)
			}
			var cardCount int
			if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM cards`).Scan(&cardCount); err != nil {
				t.Fatalf("query card count: %v", err)
			}
			if cardCount != 0 {
				t.Fatalf("card count = %d, want 0 after rollback", cardCount)
			}
		})
	}
}

func mutationLearningCandidate() cards.CardCandidate {
	return cards.CardCandidate{
		Type:        string(cards.CardTypePattern),
		Title:       "Mutation boundary",
		BodySummary: "Promotion remains atomic across every failure boundary.",
		BodyFull:    "A failed journey write must roll back the card and learning mutation.",
		Confidence:  0.8,
		Evidence:    []string{"mutation survivor"},
	}
}

func TestRejectAndReviewQueue(t *testing.T) {
	ctx := context.Background()
	store, db := newTestStoreWithBeads(t)
	if _, err := db.ExecContext(ctx, `INSERT INTO beads (id) VALUES (?)`, "bead-1"); err != nil {
		t.Fatalf("insert bead: %v", err)
	}
	candidate := cards.CardCandidate{
		Type:        string(cards.CardTypePattern),
		Title:       "Review queued learnings",
		BodySummary: "Only unresolved queued learnings are reviewable.",
		BodyFull:    "ReviewQueue uses the same terminal-state filter as the review SLA sweeper.",
		Confidence:  0.8,
		Evidence:    []string{"go test ./pkg/cards/... -run TestRejectAndReviewQueue -count=1"},
		Tags:        []string{"cards", "review"},
	}

	rejectedID, err := store.AppendLearningPending(ctx, "bead-1", candidate)
	if err != nil {
		t.Fatalf("AppendLearningPending rejected fixture: %v", err)
	}
	queuedID, err := store.AppendLearningPending(ctx, "bead-1", candidate)
	if err != nil {
		t.Fatalf("AppendLearningPending queued fixture: %v", err)
	}
	unqueuedID, err := store.AppendLearningPending(ctx, "bead-1", candidate)
	if err != nil {
		t.Fatalf("AppendLearningPending unqueued fixture: %v", err)
	}
	promotedQueuedID, err := store.AppendLearningPending(ctx, "bead-1", candidate)
	if err != nil {
		t.Fatalf("AppendLearningPending promoted fixture: %v", err)
	}
	rejectedQueuedID, err := store.AppendLearningPending(ctx, "bead-1", candidate)
	if err != nil {
		t.Fatalf("AppendLearningPending rejected queued fixture: %v", err)
	}

	if err := store.RejectLearning(ctx, rejectedID, "duplicate"); err != nil {
		t.Fatalf("RejectLearning: %v", err)
	}
	assertRejectedLearning(t, db, rejectedID, "duplicate")

	if err := store.DeferToReviewQueue(ctx, queuedID, "needs human review"); err != nil {
		t.Fatalf("DeferToReviewQueue: %v", err)
	}
	assertQueuedLearning(t, db, queuedID, "needs human review")

	if _, err := db.ExecContext(ctx,
		`INSERT INTO cards (id, type, title, body_summary, body_full, tags, decay_anchor, created_at, updated_at)
		 VALUES ('card-promoted', 'pattern', 'terminal', 's', 'b', '[]', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
	); err != nil {
		t.Fatalf("insert terminal card: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`UPDATE bead_learnings_pending SET queued_for_review_at = ?, promoted_to = 'card-promoted' WHERE id = ?`,
		time.Now().UTC().Format(time.RFC3339Nano), promotedQueuedID,
	); err != nil {
		t.Fatalf("mark promoted queued: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`UPDATE bead_learnings_pending SET queued_for_review_at = ?, rejected_at = ?, reason = 'noisy' WHERE id = ?`,
		time.Now().UTC().Format(time.RFC3339Nano),
		time.Now().UTC().Format(time.RFC3339Nano),
		rejectedQueuedID,
	); err != nil {
		t.Fatalf("mark rejected queued: %v", err)
	}

	queue, err := store.ReviewQueue(ctx)
	if err != nil {
		t.Fatalf("ReviewQueue: %v", err)
	}
	if len(queue) != 1 {
		t.Fatalf("ReviewQueue count = %d, want 1: %+v", len(queue), queue)
	}
	if queue[0].ID != queuedID {
		t.Fatalf("ReviewQueue ID = %d, want queued ID %d; unqueued ID was %d", queue[0].ID, queuedID, unqueuedID)
	}
	if queue[0].QueuedForReviewAt == nil || queue[0].PromotedTo != nil || queue[0].RejectedAt != nil {
		t.Fatalf("ReviewQueue row terminal fields = %+v, want queued unresolved", queue[0])
	}

	if err := store.RejectLearning(ctx, rejectedID, "again"); !errors.Is(err, cards.ErrAlreadyResolved) {
		t.Fatalf("RejectLearning resolved err = %v, want ErrAlreadyResolved", err)
	}
	if err := store.DeferToReviewQueue(ctx, rejectedID, "again"); !errors.Is(err, cards.ErrAlreadyResolved) {
		t.Fatalf("DeferToReviewQueue resolved err = %v, want ErrAlreadyResolved", err)
	}
}

func assertRejectedLearning(t *testing.T, db *sql.DB, id int64, wantReason string) {
	t.Helper()
	var rejectedAt, reason sql.NullString
	if err := db.QueryRowContext(context.Background(),
		`SELECT rejected_at, reason FROM bead_learnings_pending WHERE id = ?`, id,
	).Scan(&rejectedAt, &reason); err != nil {
		t.Fatalf("query rejected learning: %v", err)
	}
	if !rejectedAt.Valid {
		t.Fatal("rejected_at is NULL, want timestamp")
	}
	if _, err := time.Parse(time.RFC3339Nano, rejectedAt.String); err != nil {
		t.Fatalf("rejected_at = %q, want RFC3339Nano: %v", rejectedAt.String, err)
	}
	if !reason.Valid || reason.String != wantReason {
		t.Fatalf("reason = %v/%q, want %q", reason.Valid, reason.String, wantReason)
	}
}

func assertQueuedLearning(t *testing.T, db *sql.DB, id int64, wantReason string) {
	t.Helper()
	var queuedAt, reason sql.NullString
	if err := db.QueryRowContext(context.Background(),
		`SELECT queued_for_review_at, reason FROM bead_learnings_pending WHERE id = ?`, id,
	).Scan(&queuedAt, &reason); err != nil {
		t.Fatalf("query queued learning: %v", err)
	}
	if !queuedAt.Valid {
		t.Fatal("queued_for_review_at is NULL, want timestamp")
	}
	if _, err := time.Parse(time.RFC3339Nano, queuedAt.String); err != nil {
		t.Fatalf("queued_for_review_at = %q, want RFC3339Nano: %v", queuedAt.String, err)
	}
	if !reason.Valid || reason.String != wantReason {
		t.Fatalf("reason = %v/%q, want %q", reason.Valid, reason.String, wantReason)
	}
}
