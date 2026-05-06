package cards_test

import (
	"context"
	"fmt"
	"testing"

	"oro/pkg/cards"
	"oro/pkg/dbutil"
	"oro/pkg/memory"
	"oro/pkg/protocol"
)

// newMemoryStore opens a fresh in-memory SQLite store with the memory schema applied.
func newMemoryStore(t *testing.T) *memory.Store {
	t.Helper()
	db, err := dbutil.OpenDB(":memory:")
	if err != nil {
		t.Fatalf("open memory db: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(protocol.SchemaDDL); err != nil {
		t.Fatalf("apply memory schema: %v", err)
	}
	return memory.NewStore(db)
}

// TestDualWrite verifies the D.3 dual-write window behaviour:
//   - every memory.Insert produces a matching pattern card
//   - drift detector reports zero failures
func TestDualWrite(t *testing.T) {
	ctx := context.Background()

	t.Run("every_insert_produces_matching_card", func(t *testing.T) {
		memStore := newMemoryStore(t)
		cardStore := newTestStore(t)
		writer := cards.NewLegacyWriter(memStore, cardStore)

		type result struct {
			memID  int64
			params memory.InsertParams
		}

		// Simulate 7 days of inserts (acceptance: ≥7 days window).
		// Content is intentionally distinct across days to avoid FTS5 write-time dedup
		// (dedupJaccardThreshold=0.7; these share <10% of terms).
		dayContents := [7]string{
			"RustLang ownership prevents use-after-free vulnerabilities at compile time",
			"Python GIL blocks thread parallelism multiprocessing required for CPU tasks",
			"Kubernetes pods restart policy OnFailure needed for batch job completion",
			"PostgreSQL MVCC maintains row versions for concurrent transaction isolation",
			"Redis cluster shards keyspace across nodes using CRC16 hash slot algorithm",
			"Terraform statefile tracks infrastructure drift from declared HCL configuration",
			"Prometheus cardinality explosion caused by high-cardinality label value combinations",
		}
		const days = 7
		var results []result
		for i := 0; i < days; i++ {
			p := memory.InsertParams{
				Content:    dayContents[i],
				Type:       "lesson",
				Tags:       []string{fmt.Sprintf("day-%d", i), "infra"},
				Source:     "self_report",
				Confidence: 0.8,
			}
			id, err := writer.Insert(ctx, p)
			if err != nil {
				t.Fatalf("day %d Insert: %v", i, err)
			}
			results = append(results, result{memID: id, params: p})
		}

		// For each memory insert, a matching pattern card must exist.
		allCards, err := cardStore.List(ctx, cards.ListQuery{Type: cards.CardTypePattern})
		if err != nil {
			t.Fatalf("List cards: %v", err)
		}

		for _, r := range results {
			card := findCardByMemID(allCards, r.memID)
			if card == nil {
				t.Errorf("no card found for memory ID %d", r.memID)
				continue
			}
			if card.Type != cards.CardTypePattern {
				t.Errorf("card for mem %d: type=%q, want %q", r.memID, card.Type, cards.CardTypePattern)
			}
			if !containsTag(card.Tags, "legacy_memory_dual_write") {
				t.Errorf("card for mem %d: missing 'legacy_memory_dual_write' tag; got %v", r.memID, card.Tags)
			}
			for _, orig := range r.params.Tags {
				if !containsTag(card.Tags, orig) {
					t.Errorf("card for mem %d: missing original tag %q; got %v", r.memID, orig, card.Tags)
				}
			}
			if card.BodyFull != r.params.Content {
				t.Errorf("card for mem %d: body_full=%q, want %q", r.memID, card.BodyFull, r.params.Content)
			}
		}
	})

	t.Run("drift_detector_zero_failures_over_7_days", func(t *testing.T) {
		memStore := newMemoryStore(t)
		cardStore := newTestStore(t)
		writer := cards.NewLegacyWriter(memStore, cardStore)

		// Insert 7 distinct memories via the dual-write shim.
		// Content is intentionally varied to avoid FTS5 write-time dedup.
		driftContents := [7]string{
			"SQLite WAL mode improves concurrent read throughput dramatically over rollback",
			"Nginx upstream keepalive reduces TCP handshake overhead for proxied services",
			"Docker multi-stage builds shrink final image by excluding build dependencies",
			"Grafana datasource proxy avoids CORS browser restrictions for API queries",
			"Vault dynamic secrets rotate database credentials automatically on each lease",
			"Jaeger trace sampling reduces storage overhead while preserving error spans",
			"Envoy sidecar intercepts service mesh traffic enabling mutual TLS encryption",
		}
		for i := 0; i < 7; i++ {
			_, err := writer.Insert(ctx, memory.InsertParams{
				Content:    driftContents[i],
				Type:       "gotcha",
				Tags:       []string{"infra", fmt.Sprintf("day-%d", i)},
				Source:     "self_report",
				Confidence: 0.9,
			})
			if err != nil {
				t.Fatalf("day %d Insert: %v", i, err)
			}
		}

		// Drift detector must find zero failures.
		failures, err := cards.CheckDrift(ctx, memStore, cardStore)
		if err != nil {
			t.Fatalf("CheckDrift: %v", err)
		}
		if len(failures) != 0 {
			t.Errorf("drift detector found %d failure(s), want 0: %+v", len(failures), failures)
		}
	})

	t.Run("cards_failure_does_not_fail_memory_insert", func(t *testing.T) {
		memStore := newMemoryStore(t)
		// Use a broken card store that always errors on Create.
		writer := cards.NewLegacyWriter(memStore, &alwaysFailCardStore{})

		id, err := writer.Insert(ctx, memory.InsertParams{
			Content:    "Resilience test: memory insert must succeed even when cards store is down",
			Type:       "lesson",
			Tags:       []string{"resilience"},
			Source:     "self_report",
			Confidence: 0.8,
		})
		if err != nil {
			t.Fatalf("Insert must succeed despite card store failure: %v", err)
		}
		if id == 0 {
			t.Error("Insert returned zero ID; expected valid memory ID")
		}
	})

	t.Run("drift_detector_finds_unmirrored_entries", func(t *testing.T) {
		memStore := newMemoryStore(t)
		cardStore := newTestStore(t)
		// Write directly to memory (bypassing dual-write shim) to simulate a mirror failure.
		_, err := memStore.Insert(ctx, memory.InsertParams{
			Content:    "Direct memory write without card mirror to test drift detection path",
			Type:       "lesson",
			Tags:       []string{"drift-test"},
			Source:     "self_report",
			Confidence: 0.8,
		})
		if err != nil {
			t.Fatalf("direct Insert: %v", err)
		}

		failures, err := cards.CheckDrift(ctx, memStore, cardStore)
		if err != nil {
			t.Fatalf("CheckDrift: %v", err)
		}
		if len(failures) == 0 {
			t.Error("drift detector should have found 1 unmirrored entry, got 0")
		}
	})
}

// findCardByMemID finds the first card whose tags contain mem-id:<memID>.
func findCardByMemID(all []cards.Card, memID int64) *cards.Card {
	tag := fmt.Sprintf("mem-id:%d", memID)
	for i := range all {
		if containsTag(all[i].Tags, tag) {
			return &all[i]
		}
	}
	return nil
}

func containsTag(tags []string, target string) bool {
	for _, t := range tags {
		if t == target {
			return true
		}
	}
	return false
}

// alwaysFailCardStore is a minimal cards.Store stub that always returns an error on writes.
type alwaysFailCardStore struct{}

func (s *alwaysFailCardStore) Relevant(_ context.Context, _ cards.RelevanceQuery) (cards.RelevantCards, error) {
	return cards.RelevantCards{}, nil
}

func (s *alwaysFailCardStore) Show(_ context.Context, _ string) (*cards.Card, error) {
	return nil, cards.ErrNotFound
}

func (s *alwaysFailCardStore) List(_ context.Context, _ cards.ListQuery) ([]cards.Card, error) {
	return nil, nil
}

func (s *alwaysFailCardStore) RecordCardEvent(_ context.Context, _ cards.CardEvent) error {
	return fmt.Errorf("alwaysFailCardStore: not implemented")
}

func (s *alwaysFailCardStore) Create(_ context.Context, _ cards.CardCreateParams) (*cards.Card, error) {
	return nil, fmt.Errorf("alwaysFailCardStore: injected create failure")
}

func (s *alwaysFailCardStore) Retire(_ context.Context, _, _ string, _ string) error {
	return fmt.Errorf("alwaysFailCardStore: not implemented")
}

func (s *alwaysFailCardStore) WithReadTx(_ context.Context, _ func(cards.ReadTx) error) error {
	return fmt.Errorf("alwaysFailCardStore: not implemented")
}
