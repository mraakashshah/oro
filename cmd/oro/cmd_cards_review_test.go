package main

import (
	"context"
	"database/sql"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"oro/pkg/cards"
)

func TestCardsReview(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	t.Setenv("ORO_DB_PATH", dbPath)
	t.Setenv("ORO_PROJECT", "")

	db, err := openStateDB(dbPath)
	if err != nil {
		t.Fatalf("openStateDB: %v", err)
	}
	defer func() { _ = db.Close() }()
	insertCardsReviewBead(t, db, "oro-review-1")

	store, err := cards.NewStore(db)
	if err != nil {
		t.Fatalf("cards.NewStore: %v", err)
	}

	queuedID, err := store.AppendLearningPending(ctx, "oro-review-1", cards.CardCandidate{
		Type:        string(cards.CardTypePattern),
		Title:       "Use worktree oro for new CLI checks",
		BodySummary: "Run the worktree command when verifying new card CLI behavior.",
		BodyFull:    "The installed oro on PATH can lag the current worktree build.",
		Confidence:  0.8,
		Evidence:    []string{"go test ./cmd/oro/... -run TestCardsReview -count=1"},
		Tags:        []string{"cli", "cards"},
	})
	if err != nil {
		t.Fatalf("AppendLearningPending queued: %v", err)
	}
	if err := store.DeferToReviewQueue(ctx, queuedID, "needs human review"); err != nil {
		t.Fatalf("DeferToReviewQueue: %v", err)
	}
	unqueuedID, err := store.AppendLearningPending(ctx, "oro-review-1", cards.CardCandidate{
		Type:        string(cards.CardTypeFact),
		Title:       "Unqueued candidate",
		BodySummary: "This candidate is not in the review queue.",
		BodyFull:    "Only queued unresolved candidates should be listed.",
		Confidence:  0.5,
		Evidence:    []string{"fixture"},
	})
	if err != nil {
		t.Fatalf("AppendLearningPending unqueued: %v", err)
	}

	out, _, err := executeCommand("cards", "review-queue")
	if err != nil {
		t.Fatalf("cards review-queue: %v", err)
	}
	if !strings.Contains(out, strconv.FormatInt(queuedID, 10)) ||
		!strings.Contains(out, "Use worktree oro for new CLI checks") {
		t.Fatalf("review-queue output = %q, want queued candidate", out)
	}
	if strings.Contains(out, strconv.FormatInt(unqueuedID, 10)) ||
		strings.Contains(out, "Unqueued candidate") {
		t.Fatalf("review-queue output = %q, must not include unqueued candidate", out)
	}

	promoteOut, _, err := executeCommand("cards", "promote", strconv.FormatInt(queuedID, 10))
	if err != nil {
		t.Fatalf("cards promote: %v", err)
	}
	if !strings.Contains(promoteOut, "Promoted learning") {
		t.Fatalf("promote output = %q, want promotion confirmation", promoteOut)
	}
	if learning := getCardsReviewLearning(t, db, queuedID); learning.PromotedTo == nil {
		t.Fatalf("promoted learning = %+v, want promoted_to set", learning)
	}

	rejectID, err := store.AppendLearningPending(ctx, "oro-review-1", cards.CardCandidate{
		Type:        string(cards.CardTypePattern),
		Title:       "Reject noisy candidate",
		BodySummary: "Noisy candidates can be rejected.",
		BodyFull:    "Rejected candidates should get rejected_at and a reason.",
		Confidence:  0.7,
		Evidence:    []string{"fixture"},
	})
	if err != nil {
		t.Fatalf("AppendLearningPending reject: %v", err)
	}
	if err := store.DeferToReviewQueue(ctx, rejectID, "needs human review"); err != nil {
		t.Fatalf("DeferToReviewQueue reject: %v", err)
	}
	rejectOut, _, err := executeCommand("cards", "reject", strconv.FormatInt(rejectID, 10))
	if err != nil {
		t.Fatalf("cards reject: %v", err)
	}
	if !strings.Contains(rejectOut, "Rejected learning") {
		t.Fatalf("reject output = %q, want rejection confirmation", rejectOut)
	}
	rejected := getCardsReviewLearning(t, db, rejectID)
	if rejected.RejectedAt == nil {
		t.Fatalf("rejected learning = %+v, want rejected_at set", rejected)
	}

	_, _, err = executeCommand("cards", "promote", "not-an-id")
	if err == nil {
		t.Fatal("cards promote invalid id err = nil, want non-zero exit")
	}
}

func insertCardsReviewBead(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	_, err := db.ExecContext(
		context.Background(),
		`INSERT INTO beads (id, title, status, type) VALUES (?, 'cards review fixture', 'open', 'task')`,
		id,
	)
	if err != nil {
		t.Fatalf("insert bead fixture: %v", err)
	}
}

func getCardsReviewLearning(t *testing.T, db *sql.DB, id int64) cards.PendingLearning {
	t.Helper()
	store, err := cards.NewStore(db)
	if err != nil {
		t.Fatalf("cards.NewStore: %v", err)
	}
	queue, err := store.ReviewQueue(context.Background())
	if err != nil {
		t.Fatalf("ReviewQueue: %v", err)
	}
	for _, learning := range queue {
		if learning.ID == id {
			return learning
		}
	}
	row := db.QueryRowContext(
		context.Background(),
		`SELECT promoted_to, rejected_at, reason FROM bead_learnings_pending WHERE id = ?`,
		id,
	)
	var promotedTo sql.NullString
	var rejectedAt sql.NullString
	var reason sql.NullString
	if err := row.Scan(&promotedTo, &rejectedAt, &reason); err != nil {
		t.Fatalf("query learning %d: %v", id, err)
	}
	learning := cards.PendingLearning{ID: id}
	if promotedTo.Valid {
		learning.PromotedTo = &promotedTo.String
	}
	if rejectedAt.Valid {
		parsed := mustParseCardsReviewTime(t, rejectedAt.String)
		learning.RejectedAt = &parsed
	}
	if reason.Valid {
		learning.Reason = &reason.String
	}
	return learning
}

func mustParseCardsReviewTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		t.Fatalf("parse time %q: %v", value, err)
	}
	return parsed
}
