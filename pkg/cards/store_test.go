package cards_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"oro/pkg/cards"
	"oro/pkg/dbutil"
)

type recordingEmbedder struct {
	model string
	dim   int
	texts []string
}

func (e *recordingEmbedder) Embed(text string) []float32 {
	e.texts = append(e.texts, text)
	vec := make([]float32, e.dim)
	for i := range vec {
		vec[i] = float32(len(e.texts) + i)
	}
	return vec
}

func (e *recordingEmbedder) Dim() int {
	return e.dim
}

func (e *recordingEmbedder) Name() string {
	return e.model
}

func newTestStore(t *testing.T) *cards.SQLiteCardStore {
	t.Helper()
	store, _ := newTestStoreWithDB(t)
	return store
}

func newTestStoreWithDB(t *testing.T) (*cards.SQLiteCardStore, *sql.DB) {
	t.Helper()
	db, err := dbutil.OpenDB(":memory:")
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	store, err := cards.NewStore(db)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	return store, db
}

func mustCreate(t *testing.T, store *cards.SQLiteCardStore, p cards.CardCreateParams) *cards.Card {
	t.Helper()
	card, err := store.Create(context.Background(), p)
	if err != nil {
		t.Fatalf("create card: %v", err)
	}
	return card
}

func newTestStoreWithBeads(t *testing.T) (*cards.SQLiteCardStore, *sql.DB) {
	t.Helper()
	db, err := dbutil.OpenDB(":memory:")
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(context.Background(), `PRAGMA foreign_keys=ON`); err != nil {
		t.Fatalf("enable foreign keys: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), `
		CREATE TABLE beads (
			id TEXT PRIMARY KEY
		)`); err != nil {
		t.Fatalf("create beads table: %v", err)
	}
	store, err := cards.NewStore(db)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	return store, db
}

func TestCreate_EmbedsCard(t *testing.T) {
	ctx := context.Background()
	db, err := dbutil.OpenDB(":memory:")
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	embedder := &recordingEmbedder{model: "test-model", dim: 3}
	store, err := cards.NewStore(db, cards.WithEmbedder(embedder))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	card, err := store.Create(ctx, cards.CardCreateParams{
		Type:        cards.CardTypePattern,
		Title:       "Embed created cards",
		BodySummary: "Create should embed the compact recall text",
		BodyFull:    "Full text is intentionally not sent to the embedder.",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if len(embedder.texts) != 1 {
		t.Fatalf("embed calls = %d, want 1", len(embedder.texts))
	}
	wantText := "Embed created cards\nCreate should embed the compact recall text"
	if embedder.texts[0] != wantText {
		t.Fatalf("embedded text = %q, want %q", embedder.texts[0], wantText)
	}

	var raw []byte
	var model sql.NullString
	if err := db.QueryRowContext(ctx,
		`SELECT embedding, embedding_model FROM cards WHERE id = ?`, card.ID,
	).Scan(&raw, &model); err != nil {
		t.Fatalf("query embedding: %v", err)
	}
	if len(raw) != 12 {
		t.Fatalf("embedding bytes len = %d, want 12 for 3 float32 values", len(raw))
	}
	if !model.Valid || model.String != "test-model" {
		t.Fatalf("embedding_model = %v, want test-model", model)
	}
}

func TestReindex_BackfillsNull(t *testing.T) {
	ctx := context.Background()
	db, err := dbutil.OpenDB(":memory:")
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	storeWithoutEmbedder, err := cards.NewStore(db)
	if err != nil {
		t.Fatalf("new store without embedder: %v", err)
	}
	legacy, err := storeWithoutEmbedder.Create(ctx, cards.CardCreateParams{
		Type:        cards.CardTypePattern,
		Title:       "Legacy card",
		BodySummary: "Backfill this null embedding",
		BodyFull:    "Legacy rows start with embedding NULL.",
	})
	if err != nil {
		t.Fatalf("create legacy card: %v", err)
	}

	embedder := &recordingEmbedder{model: "backfill-model", dim: 2}
	store, err := cards.NewStore(db, cards.WithEmbedder(embedder))
	if err != nil {
		t.Fatalf("new store with embedder: %v", err)
	}
	alreadyEmbedded, err := store.Create(ctx, cards.CardCreateParams{
		Type:        cards.CardTypePattern,
		Title:       "Already embedded",
		BodySummary: "Create embeds this row immediately",
		BodyFull:    "This row should not be embedded twice.",
	})
	if err != nil {
		t.Fatalf("create embedded card: %v", err)
	}

	backfilled, err := store.Reindex(ctx)
	if err != nil {
		t.Fatalf("Reindex: %v", err)
	}
	if backfilled != 1 {
		t.Fatalf("Reindex backfilled = %d, want 1", backfilled)
	}
	wantTexts := []string{
		"Already embedded\nCreate embeds this row immediately",
		"Legacy card\nBackfill this null embedding",
	}
	if strings.Join(embedder.texts, "\x00") != strings.Join(wantTexts, "\x00") {
		t.Fatalf("embed texts = %#v, want %#v", embedder.texts, wantTexts)
	}

	assertCardEmbeddingModel(t, db, legacy.ID, "backfill-model")
	assertCardEmbeddingModel(t, db, alreadyEmbedded.ID, "backfill-model")
}

func assertCardEmbeddingModel(t *testing.T, db *sql.DB, id, want string) {
	t.Helper()
	var raw []byte
	var model sql.NullString
	if err := db.QueryRow(
		`SELECT embedding, embedding_model FROM cards WHERE id = ?`, id,
	).Scan(&raw, &model); err != nil {
		t.Fatalf("query embedding for %s: %v", id, err)
	}
	if len(raw) == 0 {
		t.Fatalf("card %s embedding is empty", id)
	}
	if !model.Valid || model.String != want {
		t.Fatalf("card %s embedding_model = %v, want %s", id, model, want)
	}
}

func TestAppendAndQueryPending(t *testing.T) {
	ctx := context.Background()
	store, db := newTestStoreWithBeads(t)
	if _, err := db.ExecContext(ctx, `INSERT INTO beads (id) VALUES (?)`, "bead-1"); err != nil {
		t.Fatalf("insert bead: %v", err)
	}
	candidate := cards.CardCandidate{
		Type:        string(cards.CardTypePattern),
		Title:       "Prefer focused card APIs",
		BodySummary: "Store pending learnings before promotion",
		BodyFull:    "Workers append card candidates to bead_learnings_pending for later review.",
		Confidence:  0.82,
		Evidence:    []string{"go test ./pkg/cards/..."},
		Tags:        []string{"cards", "learning"},
	}

	id, err := store.AppendLearningPending(ctx, "bead-1", candidate)
	if err != nil {
		t.Fatalf("AppendLearningPending: %v", err)
	}
	if id == 0 {
		t.Fatal("AppendLearningPending id = 0, want inserted row id")
	}

	var rawCandidate string
	if err := db.QueryRowContext(ctx,
		`SELECT candidate FROM bead_learnings_pending WHERE id = ?`, id,
	).Scan(&rawCandidate); err != nil {
		t.Fatalf("query raw candidate: %v", err)
	}
	var decoded cards.CardCandidate
	if err := json.Unmarshal([]byte(rawCandidate), &decoded); err != nil {
		t.Fatalf("candidate JSON: %v", err)
	}
	if decoded.Title != candidate.Title || decoded.Confidence != candidate.Confidence {
		t.Fatalf("candidate persisted = %+v, want %+v", decoded, candidate)
	}

	promotedID, err := store.AppendLearningPending(ctx, "bead-1", candidate)
	if err != nil {
		t.Fatalf("AppendLearningPending promoted fixture: %v", err)
	}
	rejectedID, err := store.AppendLearningPending(ctx, "bead-1", candidate)
	if err != nil {
		t.Fatalf("AppendLearningPending rejected fixture: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO cards (id, type, title, body_summary, body_full, tags, decay_anchor, created_at, updated_at)
		 VALUES ('card-terminal', 'pattern', 'terminal', 's', 'b', '[]', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
	); err != nil {
		t.Fatalf("insert terminal card: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`UPDATE bead_learnings_pending SET promoted_to = 'card-terminal' WHERE id = ?`, promotedID,
	); err != nil {
		t.Fatalf("mark promoted: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`UPDATE bead_learnings_pending SET rejected_at = '2026-01-01T00:00:00Z', reason = 'duplicate' WHERE id = ?`, rejectedID,
	); err != nil {
		t.Fatalf("mark rejected: %v", err)
	}

	pending, err := store.PendingLearnings(ctx, "bead-1")
	if err != nil {
		t.Fatalf("PendingLearnings: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("PendingLearnings count = %d, want 1", len(pending))
	}
	if pending[0].ID != id || pending[0].BeadID != "bead-1" {
		t.Fatalf("PendingLearnings row = %+v, want id %d bead-1", pending[0], id)
	}
	if pending[0].Candidate.Title != candidate.Title {
		t.Fatalf("PendingLearnings candidate title = %q, want %q", pending[0].Candidate.Title, candidate.Title)
	}

	_, err = store.AppendLearningPending(ctx, "missing-bead", candidate)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "foreign key") {
		t.Fatalf("AppendLearningPending missing bead err = %v, want foreign key error", err)
	}
}

func TestCardStoreRead(t *testing.T) {
	ctx := context.Background()

	t.Run("Show_returnsCard", func(t *testing.T) {
		store := newTestStore(t)
		card := mustCreate(t, store, cards.CardCreateParams{
			Type:        cards.CardTypeRule,
			Title:       "Always handle errors",
			BodySummary: "Wrap errors with %w",
			BodyFull:    "Use fmt.Errorf with %w to preserve context",
			Tags:        []string{"go", "errors"},
		})

		got, err := store.Show(ctx, card.ID)
		if err != nil {
			t.Fatalf("Show: %v", err)
		}
		if got.ID != card.ID {
			t.Errorf("ID: got %q, want %q", got.ID, card.ID)
		}
		if got.Type != cards.CardTypeRule {
			t.Errorf("Type: got %q, want %q", got.Type, cards.CardTypeRule)
		}
		if got.Title != "Always handle errors" {
			t.Errorf("Title: got %q", got.Title)
		}
		if got.BodySummary != "Wrap errors with %w" {
			t.Errorf("BodySummary: got %q", got.BodySummary)
		}
		if len(got.Tags) != 2 || got.Tags[0] != "go" || got.Tags[1] != "errors" {
			t.Errorf("Tags: got %v", got.Tags)
		}
		if got.Score != 1.0 {
			t.Errorf("Score: got %f, want 1.0", got.Score)
		}
	})

	t.Run("Show_notFound", func(t *testing.T) {
		store := newTestStore(t)
		_, err := store.Show(ctx, "card-nonexistent")
		if err == nil {
			t.Fatal("expected error for nonexistent card")
		}
	})

	t.Run("Show_allFieldsPersisted", func(t *testing.T) {
		store := newTestStore(t)
		deep := "Deep dive content with examples"
		conf := 0.87
		card := mustCreate(t, store, cards.CardCreateParams{
			Type:                cards.CardTypePattern,
			Title:               "Auth middleware pattern",
			BodySummary:         "Apply auth before validation",
			BodyFull:            "Detailed auth middleware explanation",
			BodyDeep:            &deep,
			Tags:                []string{"auth", "middleware"},
			PromotionConfidence: &conf,
		})

		got, err := store.Show(ctx, card.ID)
		if err != nil {
			t.Fatalf("Show: %v", err)
		}
		if got.BodyDeep == nil || *got.BodyDeep != deep {
			t.Errorf("BodyDeep: got %v, want %q", got.BodyDeep, deep)
		}
		if got.PromotionConfidence == nil || *got.PromotionConfidence != conf {
			t.Errorf("PromotionConfidence: got %v, want %f", got.PromotionConfidence, conf)
		}
	})

	t.Run("List_byType", func(t *testing.T) {
		store := newTestStore(t)
		mustCreate(t, store, cards.CardCreateParams{
			Type: cards.CardTypeRule, Title: "r1",
			BodySummary: "s", BodyFull: "b",
		})
		mustCreate(t, store, cards.CardCreateParams{
			Type: cards.CardTypePattern, Title: "p1",
			BodySummary: "s", BodyFull: "b",
		})
		mustCreate(t, store, cards.CardCreateParams{
			Type: cards.CardTypePattern, Title: "p2",
			BodySummary: "s", BodyFull: "b",
		})

		rules, err := store.List(ctx, cards.ListQuery{Type: cards.CardTypeRule})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(rules) != 1 {
			t.Errorf("rules count: got %d, want 1", len(rules))
		}
		if len(rules) > 0 && rules[0].Type != cards.CardTypeRule {
			t.Errorf("Type: got %q", rules[0].Type)
		}

		patterns, err := store.List(ctx, cards.ListQuery{Type: cards.CardTypePattern})
		if err != nil {
			t.Fatalf("List patterns: %v", err)
		}
		if len(patterns) != 2 {
			t.Errorf("patterns count: got %d, want 2", len(patterns))
		}
	})

	t.Run("List_allTypes_whenTypeEmpty", func(t *testing.T) {
		store := newTestStore(t)
		mustCreate(t, store, cards.CardCreateParams{
			Type: cards.CardTypeRule, Title: "r1",
			BodySummary: "s", BodyFull: "b",
		})
		mustCreate(t, store, cards.CardCreateParams{
			Type: cards.CardTypeFact, Title: "f1",
			BodySummary: "s", BodyFull: "b",
		})

		all, err := store.List(ctx, cards.ListQuery{})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(all) != 2 {
			t.Errorf("all count: got %d, want 2", len(all))
		}
	})

	t.Run("List_excludesRetiredByDefault", func(t *testing.T) {
		store := newTestStore(t)
		active := mustCreate(t, store, cards.CardCreateParams{
			Type: cards.CardTypeRule, Title: "active",
			BodySummary: "s", BodyFull: "b",
		})
		toRetire := mustCreate(t, store, cards.CardCreateParams{
			Type: cards.CardTypeRule, Title: "to-retire",
			BodySummary: "s", BodyFull: "b",
		})

		if err := store.Retire(ctx, toRetire.ID, "test reason", ""); err != nil {
			t.Fatalf("Retire: %v", err)
		}

		result, err := store.List(ctx, cards.ListQuery{})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(result) != 1 {
			t.Errorf("count: got %d, want 1 (active only)", len(result))
		}
		if len(result) > 0 && result[0].ID != active.ID {
			t.Errorf("wrong card returned")
		}
	})

	t.Run("List_includesRetiredWhenFlagSet", func(t *testing.T) {
		store := newTestStore(t)
		mustCreate(t, store, cards.CardCreateParams{
			Type: cards.CardTypeRule, Title: "active",
			BodySummary: "s", BodyFull: "b",
		})
		toRetire := mustCreate(t, store, cards.CardCreateParams{
			Type: cards.CardTypeRule, Title: "to-retire",
			BodySummary: "s", BodyFull: "b",
		})
		if err := store.Retire(ctx, toRetire.ID, "test reason", ""); err != nil {
			t.Fatalf("Retire: %v", err)
		}

		result, err := store.List(ctx, cards.ListQuery{IncludeRetired: true})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(result) != 2 {
			t.Errorf("count: got %d, want 2 (including retired)", len(result))
		}
	})

	t.Run("List_limitAndOffset", func(t *testing.T) {
		store := newTestStore(t)
		for i := range 5 {
			mustCreate(t, store, cards.CardCreateParams{
				Type: cards.CardTypeRule, Title: "card",
				BodySummary: "s", BodyFull: "b",
				// title duplicate is fine for this test
			})
			_ = i
		}

		result, err := store.List(ctx, cards.ListQuery{Limit: 3})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(result) != 3 {
			t.Errorf("limit: got %d, want 3", len(result))
		}

		page2, err := store.List(ctx, cards.ListQuery{Limit: 3, Offset: 3})
		if err != nil {
			t.Fatalf("List page2: %v", err)
		}
		if len(page2) != 2 {
			t.Errorf("offset: got %d, want 2", len(page2))
		}
	})

	// --- Scoring: pure function tests ---

	t.Run("DecayMultiplier_halfLife", func(t *testing.T) {
		// Rule half-life = 365 days; at exactly 365 days ago decay ≈ 0.5
		anchor := time.Now().Add(-365 * 24 * time.Hour)
		decay := cards.DecayMultiplier(cards.CardTypeRule, anchor, time.Now())
		if math.Abs(decay-0.5) > 0.01 {
			t.Errorf("decay at one half-life: got %f, want ~0.5", decay)
		}
	})

	t.Run("DecayMultiplier_fresh", func(t *testing.T) {
		// Just created → decay ≈ 1.0
		anchor := time.Now()
		decay := cards.DecayMultiplier(cards.CardTypeRule, anchor, time.Now())
		if decay < 0.99 {
			t.Errorf("fresh card decay: got %f, want ~1.0", decay)
		}
	})

	t.Run("DecayMultiplier_perType", func(t *testing.T) {
		// fact has 90-day half-life; rule has 365-day; after 90 days fact ≈ 0.5, rule ≈ higher
		anchor := time.Now().Add(-90 * 24 * time.Hour)
		now := time.Now()
		factDecay := cards.DecayMultiplier(cards.CardTypeFact, anchor, now)
		ruleDecay := cards.DecayMultiplier(cards.CardTypeRule, anchor, now)
		if math.Abs(factDecay-0.5) > 0.01 {
			t.Errorf("fact decay at 90 days: got %f, want ~0.5", factDecay)
		}
		if ruleDecay <= factDecay {
			t.Errorf("rule should decay slower than fact; rule=%f, fact=%f", ruleDecay, factDecay)
		}
	})

	t.Run("SuppressionMultiplier_neverContradicted", func(t *testing.T) {
		s := cards.SuppressionMultiplier(cards.CardTypeRule, nil, time.Now())
		if s != 1.0 {
			t.Errorf("nil contradiction: got %f, want 1.0", s)
		}
	})

	t.Run("SuppressionMultiplier_recentContradiction", func(t *testing.T) {
		ts := time.Now().Add(-1 * time.Hour)
		s := cards.SuppressionMultiplier(cards.CardTypeRule, &ts, time.Now())
		if s != 0.0 {
			t.Errorf("recent contradiction: got %f, want 0.0", s)
		}
	})

	t.Run("SuppressionMultiplier_expiredWindow", func(t *testing.T) {
		// Rule window = 14 days; contradiction 15 days ago → not suppressed
		ts := time.Now().Add(-15 * 24 * time.Hour)
		s := cards.SuppressionMultiplier(cards.CardTypeRule, &ts, time.Now())
		if s != 1.0 {
			t.Errorf("expired contradiction window: got %f, want 1.0", s)
		}
	})

	t.Run("SuppressionMultiplier_withinWindow", func(t *testing.T) {
		// Rule window = 14 days; contradiction 13 days ago → suppressed
		ts := time.Now().Add(-13 * 24 * time.Hour)
		s := cards.SuppressionMultiplier(cards.CardTypeRule, &ts, time.Now())
		if s != 0.0 {
			t.Errorf("contradiction within window: got %f, want 0.0", s)
		}
	})

	t.Run("SuppressionMultiplier_decisionWindowLonger", func(t *testing.T) {
		// Decision window = 30 days; contradiction 20 days ago → still suppressed
		ts := time.Now().Add(-20 * 24 * time.Hour)
		s := cards.SuppressionMultiplier(cards.CardTypeDecision, &ts, time.Now())
		if s != 0.0 {
			t.Errorf("decision within 30-day window: got %f, want 0.0", s)
		}
	})

	t.Run("EffectiveScore_decayAndSuppression", func(t *testing.T) {
		now := time.Now()
		anchor := now.Add(-365 * 24 * time.Hour) // one half-life ago

		c := &cards.Card{
			Type:               cards.CardTypeRule,
			Score:              2.0,
			DecayAnchor:        anchor,
			LastContradictedAt: nil, // never contradicted
		}
		eff := cards.EffectiveScore(c, now)
		// decay ≈ 0.5, suppression = 1.0 → effective ≈ 1.0
		if math.Abs(eff-1.0) > 0.05 {
			t.Errorf("effective score: got %f, want ~1.0", eff)
		}
	})

	t.Run("EffectiveScore_suppressedToZero", func(t *testing.T) {
		now := time.Now()
		ts := now.Add(-1 * time.Hour)
		c := &cards.Card{
			Type:               cards.CardTypeRule,
			Score:              4.8,
			DecayAnchor:        now,
			LastContradictedAt: &ts,
		}
		eff := cards.EffectiveScore(c, now)
		if eff != 0.0 {
			t.Errorf("suppressed card effective score: got %f, want 0.0", eff)
		}
	})

	t.Run("EffectiveScore_capAt5", func(t *testing.T) {
		now := time.Now()
		c := &cards.Card{
			Type:               cards.CardTypeRule,
			Score:              10.0, // above cap
			DecayAnchor:        now,
			LastContradictedAt: nil,
		}
		eff := cards.EffectiveScore(c, now)
		if eff > 5.0 {
			t.Errorf("effective score cap: got %f, want <= 5.0", eff)
		}
	})

	// --- Relevant API ---

	t.Run("Relevant_returnsDeckSortedByEffectiveScore", func(t *testing.T) {
		store := newTestStore(t)
		now := time.Now()

		// Create cards with different scores and decay anchors
		highScore := mustCreate(t, store, cards.CardCreateParams{
			Type: cards.CardTypeRule, Title: "high score rule",
			BodySummary: "high", BodyFull: "body",
			Tags: []string{"go"},
		})
		// Bump score up by recording acks
		for range 3 {
			if err := store.RecordCardEvent(ctx, cards.CardEvent{
				CardID: highScore.ID, Actor: "worker", Kind: "ack",
			}); err != nil {
				t.Fatalf("ack: %v", err)
			}
		}

		lowScore := mustCreate(t, store, cards.CardCreateParams{
			Type: cards.CardTypeRule, Title: "low score rule",
			BodySummary: "low", BodyFull: "body",
			Tags: []string{"go"},
		})

		q := cards.RelevanceQuery{
			BeadTags:  []string{"go"},
			BeadType:  "task",
			MaxTokens: 1000,
		}
		result, err := store.Relevant(ctx, q)
		if err != nil {
			t.Fatalf("Relevant: %v", err)
		}

		if len(result.Deck) < 2 {
			t.Fatalf("deck should have >= 2 cards, got %d", len(result.Deck))
		}
		// Find positions
		highIdx, lowIdx := -1, -1
		for i, c := range result.Deck {
			if c.ID == highScore.ID {
				highIdx = i
			}
			if c.ID == lowScore.ID {
				lowIdx = i
			}
		}
		if highIdx == -1 || lowIdx == -1 {
			t.Fatalf("expected both cards in deck: highIdx=%d lowIdx=%d", highIdx, lowIdx)
		}
		if highIdx > lowIdx {
			t.Errorf("high-score card should appear before low-score; highIdx=%d lowIdx=%d", highIdx, lowIdx)
		}
		_ = now
	})

	t.Run("Relevant_excludesSuppressedByDefault", func(t *testing.T) {
		store := newTestStore(t)

		suppressed := mustCreate(t, store, cards.CardCreateParams{
			Type: cards.CardTypeRule, Title: "suppressed card",
			BodySummary: "s", BodyFull: "b",
			Tags: []string{"go"},
		})
		// Record a contradiction to suppress it
		if err := store.RecordCardEvent(ctx, cards.CardEvent{
			CardID: suppressed.ID, Actor: "worker", Kind: "contradicted",
		}); err != nil {
			t.Fatalf("contradicted: %v", err)
		}

		normal := mustCreate(t, store, cards.CardCreateParams{
			Type: cards.CardTypeRule, Title: "normal card",
			BodySummary: "s", BodyFull: "b",
			Tags: []string{"go"},
		})

		result, err := store.Relevant(ctx, cards.RelevanceQuery{
			BeadTags:  []string{"go"},
			BeadType:  "task",
			MaxTokens: 1000,
		})
		if err != nil {
			t.Fatalf("Relevant: %v", err)
		}

		for _, c := range result.Deck {
			if c.ID == suppressed.ID {
				t.Errorf("suppressed card should not appear in default Relevant result")
			}
		}
		found := false
		for _, c := range result.Deck {
			if c.ID == normal.ID {
				found = true
			}
		}
		if !found {
			t.Errorf("normal card should appear in Relevant result")
		}
	})

	t.Run("Relevant_includesSuppressedWithFlag", func(t *testing.T) {
		store := newTestStore(t)

		suppressed := mustCreate(t, store, cards.CardCreateParams{
			Type: cards.CardTypeRule, Title: "suppressed card",
			BodySummary: "s", BodyFull: "b",
			Tags: []string{"go"},
		})
		if err := store.RecordCardEvent(ctx, cards.CardEvent{
			CardID: suppressed.ID, Actor: "worker", Kind: "contradicted",
		}); err != nil {
			t.Fatalf("contradicted: %v", err)
		}

		result, err := store.Relevant(ctx, cards.RelevanceQuery{
			BeadTags:          []string{"go"},
			BeadType:          "task",
			MaxTokens:         1000,
			IncludeSuppressed: true,
		})
		if err != nil {
			t.Fatalf("Relevant: %v", err)
		}

		found := false
		for _, c := range result.Deck {
			if c.ID == suppressed.ID {
				found = true
			}
		}
		if !found {
			t.Errorf("suppressed card should appear when IncludeSuppressed=true")
		}
	})

	t.Run("Relevant_excludesLowScoreByDefault", func(t *testing.T) {
		store := newTestStore(t)

		lowCard := mustCreate(t, store, cards.CardCreateParams{
			Type: cards.CardTypeRule, Title: "low score card",
			BodySummary: "s", BodyFull: "b",
			Tags: []string{"go"},
		})
		// Nack it to drop score below threshold
		for range 3 {
			if err := store.RecordCardEvent(ctx, cards.CardEvent{
				CardID: lowCard.ID, Actor: "worker", Kind: "nack",
			}); err != nil {
				t.Fatalf("nack: %v", err)
			}
		}
		// score = 1.0 - 3*0.5 = -0.5; effective_score * decay ≈ -0.5, below 0.1 threshold

		result, err := store.Relevant(ctx, cards.RelevanceQuery{
			BeadTags:  []string{"go"},
			BeadType:  "task",
			MaxTokens: 1000,
		})
		if err != nil {
			t.Fatalf("Relevant: %v", err)
		}

		for _, c := range result.Deck {
			if c.ID == lowCard.ID {
				t.Errorf("low-score card should not appear in default Relevant result")
			}
		}
	})

	t.Run("Relevant_includesLowScoreWithFlag", func(t *testing.T) {
		store := newTestStore(t)

		lowCard := mustCreate(t, store, cards.CardCreateParams{
			Type: cards.CardTypeRule, Title: "low score card",
			BodySummary: "s", BodyFull: "b",
			Tags: []string{"go"},
		})
		for range 3 {
			if err := store.RecordCardEvent(ctx, cards.CardEvent{
				CardID: lowCard.ID, Actor: "worker", Kind: "nack",
			}); err != nil {
				t.Fatalf("nack: %v", err)
			}
		}

		result, err := store.Relevant(ctx, cards.RelevanceQuery{
			BeadTags:        []string{"go"},
			BeadType:        "task",
			MaxTokens:       1000,
			IncludeLowScore: true,
		})
		if err != nil {
			t.Fatalf("Relevant: %v", err)
		}

		found := false
		for _, c := range result.Deck {
			if c.ID == lowCard.ID {
				found = true
			}
		}
		if !found {
			t.Errorf("low-score card should appear when IncludeLowScore=true")
		}
	})

	t.Run("Relevant_inlinedFitsWithinTokenBudget", func(t *testing.T) {
		store := newTestStore(t)
		mustCreate(t, store, cards.CardCreateParams{
			Type: cards.CardTypeRule, Title: "card1",
			BodySummary: "summary1",
			BodyFull:    "full body with extra words here one two three four five",
			Tags:        []string{"go"},
		})
		mustCreate(t, store, cards.CardCreateParams{
			Type: cards.CardTypeRule, Title: "card2",
			BodySummary: "summary2",
			BodyFull:    "full body with extra words here one two three four five",
			Tags:        []string{"go"},
		})

		// MaxTokens=0 means no inlining
		result, err := store.Relevant(ctx, cards.RelevanceQuery{
			BeadTags:  []string{"go"},
			BeadType:  "task",
			MaxTokens: 0,
		})
		if err != nil {
			t.Fatalf("Relevant: %v", err)
		}
		if len(result.Inlined) != 0 {
			t.Errorf("MaxTokens=0 should produce no inlined cards, got %d", len(result.Inlined))
		}
		if len(result.Deck) == 0 {
			t.Errorf("Deck should still be populated even with MaxTokens=0")
		}
	})

	t.Run("Relevant_excludesRetired", func(t *testing.T) {
		store := newTestStore(t)
		toRetire := mustCreate(t, store, cards.CardCreateParams{
			Type: cards.CardTypeRule, Title: "to retire",
			BodySummary: "s", BodyFull: "b",
			Tags: []string{"go"},
		})
		if err := store.Retire(ctx, toRetire.ID, "test", ""); err != nil {
			t.Fatalf("Retire: %v", err)
		}

		result, err := store.Relevant(ctx, cards.RelevanceQuery{
			BeadTags:        []string{"go"},
			BeadType:        "task",
			MaxTokens:       1000,
			IncludeLowScore: true,
		})
		if err != nil {
			t.Fatalf("Relevant: %v", err)
		}
		for _, c := range result.Deck {
			if c.ID == toRetire.ID {
				t.Errorf("retired card should not appear in Relevant result")
			}
		}
	})

	t.Run("RecordCardEvent_ackIncreasesScore", func(t *testing.T) {
		store := newTestStore(t)
		card := mustCreate(t, store, cards.CardCreateParams{
			Type: cards.CardTypeRule, Title: "ack test",
			BodySummary: "s", BodyFull: "b",
		})

		if err := store.RecordCardEvent(ctx, cards.CardEvent{
			CardID: card.ID, Actor: "worker", Kind: "ack",
		}); err != nil {
			t.Fatalf("ack: %v", err)
		}

		got, err := store.Show(ctx, card.ID)
		if err != nil {
			t.Fatalf("Show: %v", err)
		}
		expected := 1.0 + 0.3
		if math.Abs(got.Score-expected) > 0.001 {
			t.Errorf("score after ack: got %f, want %f", got.Score, expected)
		}
	})

	t.Run("RecordCardEvent_nackDecreasesScore", func(t *testing.T) {
		store := newTestStore(t)
		card := mustCreate(t, store, cards.CardCreateParams{
			Type: cards.CardTypeRule, Title: "nack test",
			BodySummary: "s", BodyFull: "b",
		})

		if err := store.RecordCardEvent(ctx, cards.CardEvent{
			CardID: card.ID, Actor: "worker", Kind: "nack",
		}); err != nil {
			t.Fatalf("nack: %v", err)
		}

		got, err := store.Show(ctx, card.ID)
		if err != nil {
			t.Fatalf("Show: %v", err)
		}
		expected := 1.0 - 0.5
		if math.Abs(got.Score-expected) > 0.001 {
			t.Errorf("score after nack: got %f, want %f", got.Score, expected)
		}
	})

	t.Run("RecordCardEvent_contradictedSuppresses", func(t *testing.T) {
		store := newTestStore(t)
		card := mustCreate(t, store, cards.CardCreateParams{
			Type: cards.CardTypeRule, Title: "contradicted test",
			BodySummary: "s", BodyFull: "b",
		})

		if err := store.RecordCardEvent(ctx, cards.CardEvent{
			CardID: card.ID, Actor: "worker", Kind: "contradicted",
		}); err != nil {
			t.Fatalf("contradicted: %v", err)
		}

		got, err := store.Show(ctx, card.ID)
		if err != nil {
			t.Fatalf("Show: %v", err)
		}
		if got.LastContradictedAt == nil {
			t.Error("last_contradicted_at should be set after contradicted event")
		}
		// effective score should be 0 (suppressed)
		eff := cards.EffectiveScore(got, time.Now())
		if eff != 0.0 {
			t.Errorf("suppressed effective score: got %f, want 0.0", eff)
		}
	})

	t.Run("RecordCardEvent_confirmedClearsSuppression", func(t *testing.T) {
		store := newTestStore(t)
		card := mustCreate(t, store, cards.CardCreateParams{
			Type: cards.CardTypeRule, Title: "confirmed clears suppression",
			BodySummary: "s", BodyFull: "b",
		})

		// First contradict
		if err := store.RecordCardEvent(ctx, cards.CardEvent{
			CardID: card.ID, Actor: "worker", Kind: "contradicted",
		}); err != nil {
			t.Fatalf("contradicted: %v", err)
		}
		// Then confirm
		if err := store.RecordCardEvent(ctx, cards.CardEvent{
			CardID: card.ID, Actor: "worker", Kind: "confirmed",
		}); err != nil {
			t.Fatalf("confirmed: %v", err)
		}

		got, err := store.Show(ctx, card.ID)
		if err != nil {
			t.Fatalf("Show: %v", err)
		}
		if got.LastContradictedAt != nil {
			t.Error("last_contradicted_at should be NULL after confirmed event (clears suppression)")
		}
	})

	t.Run("RecordCardEvent_scoreCapAt5", func(t *testing.T) {
		store := newTestStore(t)
		card := mustCreate(t, store, cards.CardCreateParams{
			Type: cards.CardTypeRule, Title: "cap test",
			BodySummary: "s", BodyFull: "b",
		})

		// 20 acks × 0.3 = +6.0; score should cap at 5.0
		for range 20 {
			if err := store.RecordCardEvent(ctx, cards.CardEvent{
				CardID: card.ID, Actor: "worker", Kind: "ack",
			}); err != nil {
				t.Fatalf("ack: %v", err)
			}
		}

		got, err := store.Show(ctx, card.ID)
		if err != nil {
			t.Fatalf("Show: %v", err)
		}
		if got.Score > 5.0 {
			t.Errorf("score cap: got %f, want <= 5.0", got.Score)
		}
		if math.Abs(got.Score-5.0) > 0.001 {
			t.Errorf("score should be exactly at cap 5.0, got %f", got.Score)
		}
	})

	t.Run("RecordCardEvent_autoRetireOnPersistentNack", func(t *testing.T) {
		store := newTestStore(t)
		card := mustCreate(t, store, cards.CardCreateParams{
			Type: cards.CardTypeRule, Title: "auto retire test",
			BodySummary: "s", BodyFull: "b",
		})

		// 5 nacks × (-0.5) = -2.5; after floor at -2.0, auto-retire threshold is -1.0
		// auto-retire triggers when score <= -1.0
		for range 5 {
			if err := store.RecordCardEvent(ctx, cards.CardEvent{
				CardID: card.ID, Actor: "worker", Kind: "nack",
			}); err != nil {
				t.Fatalf("nack: %v", err)
			}
		}

		got, err := store.Show(ctx, card.ID)
		if err != nil {
			t.Fatalf("Show: %v", err)
		}
		if got.RetiredAt == nil {
			t.Error("card should be auto-retired after persistent nacks")
		}
		if got.RetiredReason == nil || *got.RetiredReason != "auto: persistent nack" {
			t.Errorf("retire reason: got %v, want 'auto: persistent nack'", got.RetiredReason)
		}
	})

	t.Run("WithReadTx_readsConsistently", func(t *testing.T) {
		store := newTestStore(t)
		card := mustCreate(t, store, cards.CardCreateParams{
			Type: cards.CardTypeRule, Title: "tx test",
			BodySummary: "s", BodyFull: "b",
		})

		var got *cards.Card
		err := store.WithReadTx(ctx, func(tx cards.ReadTx) error {
			var err error
			got, err = tx.Show(ctx, card.ID)
			return err
		})
		if err != nil {
			t.Fatalf("WithReadTx: %v", err)
		}
		if got == nil || got.ID != card.ID {
			t.Errorf("WithReadTx Show: got %v", got)
		}
	})

	t.Run("WithReadTx_List_filtersAndPaginates", func(t *testing.T) {
		store := newTestStore(t)
		mustCreate(t, store, cards.CardCreateParams{
			Type: cards.CardTypeRule, Title: "r1",
			BodySummary: "s", BodyFull: "b",
		})
		mustCreate(t, store, cards.CardCreateParams{
			Type: cards.CardTypePattern, Title: "p1",
			BodySummary: "s", BodyFull: "b",
		})
		toRetire := mustCreate(t, store, cards.CardCreateParams{
			Type: cards.CardTypePattern, Title: "p2",
			BodySummary: "s", BodyFull: "b",
		})
		if err := store.Retire(ctx, toRetire.ID, "test", ""); err != nil {
			t.Fatalf("Retire: %v", err)
		}

		var rules, patternsAll []cards.Card
		var limited []cards.Card
		err := store.WithReadTx(ctx, func(tx cards.ReadTx) error {
			var err error
			rules, err = tx.List(ctx, cards.ListQuery{Type: cards.CardTypeRule})
			if err != nil {
				return err
			}
			patternsAll, err = tx.List(ctx, cards.ListQuery{Type: cards.CardTypePattern, IncludeRetired: true})
			if err != nil {
				return err
			}
			limited, err = tx.List(ctx, cards.ListQuery{Limit: 1, Offset: 1})
			return err
		})
		if err != nil {
			t.Fatalf("WithReadTx: %v", err)
		}
		if len(rules) != 1 {
			t.Errorf("rules: got %d, want 1", len(rules))
		}
		if len(patternsAll) != 2 {
			t.Errorf("patterns including retired: got %d, want 2", len(patternsAll))
		}
		if len(limited) != 1 {
			t.Errorf("limited: got %d, want 1", len(limited))
		}
	})

	t.Run("WithReadTx_Relevant_returnsScoredCards", func(t *testing.T) {
		store := newTestStore(t)
		mustCreate(t, store, cards.CardCreateParams{
			Type:        cards.CardTypeRule,
			Title:       "Wrap errors",
			BodySummary: "Use fmt.Errorf with %w to preserve context",
			BodyFull:    "Detailed body",
			Tags:        []string{"go", "errors"},
		})
		mustCreate(t, store, cards.CardCreateParams{
			Type:        cards.CardTypeDecision,
			Title:       "Use Postgres",
			BodySummary: "ACID guarantees matter for billing",
			BodyFull:    "Detailed",
			Tags:        []string{"db", "postgres"},
		})

		var result cards.RelevantCards
		err := store.WithReadTx(ctx, func(tx cards.ReadTx) error {
			var err error
			result, err = tx.Relevant(ctx, cards.RelevanceQuery{
				BeadType:        "feature",
				BeadTags:        []string{"go", "errors"},
				BeadDescription: "wrap errors with context",
				SymbolHints:     []string{"errors"},
				MaxTokens:       1000,
			})
			return err
		})
		if err != nil {
			t.Fatalf("WithReadTx Relevant: %v", err)
		}
		if len(result.Deck) == 0 {
			t.Fatal("Deck: got empty, want at least one card")
		}
		// Highest-scoring card should be the errors-tagged one.
		if result.Deck[0].Title != "Wrap errors" {
			t.Errorf("top card: got %q, want %q", result.Deck[0].Title, "Wrap errors")
		}
	})
}

func TestRelevant_SymbolHintsNowContributes(t *testing.T) {
	ctx := context.Background()
	store, db := newTestStoreWithDB(t)

	withSymbol := mustCreate(t, store, cards.CardCreateParams{
		Type:        cards.CardTypePattern,
		Title:       "with symbol",
		BodySummary: "same summary",
		BodyFull:    "same body",
		Tags:        []string{"same-tag"},
	})
	withoutSymbol := mustCreate(t, store, cards.CardCreateParams{
		Type:        cards.CardTypePattern,
		Title:       "without symbol",
		BodySummary: "same summary",
		BodyFull:    "same body",
		Tags:        []string{"same-tag"},
	})
	if _, err := db.ExecContext(ctx,
		`INSERT INTO card_symbols (card_id, symbol) VALUES (?, ?)`,
		withSymbol.ID, "pkg/x:Foo",
	); err != nil {
		t.Fatalf("insert card symbol: %v", err)
	}

	result, err := store.Relevant(ctx, cards.RelevanceQuery{
		BeadType:        "task",
		BeadTags:        []string{"same-tag"},
		BeadDescription: "same summary",
		SymbolHints:     []string{"pkg/x:Foo"},
		MaxTokens:       1000,
	})
	if err != nil {
		t.Fatalf("Relevant: %v", err)
	}

	withIdx, withoutIdx := -1, -1
	for i, c := range result.Deck {
		switch c.ID {
		case withSymbol.ID:
			withIdx = i
		case withoutSymbol.ID:
			withoutIdx = i
		}
	}
	if withIdx == -1 || withoutIdx == -1 {
		t.Fatalf("expected both cards in deck: withIdx=%d withoutIdx=%d", withIdx, withoutIdx)
	}
	if withIdx > withoutIdx {
		t.Fatalf("symbol-matching card ranked after non-matching card: withIdx=%d withoutIdx=%d", withIdx, withoutIdx)
	}
}
