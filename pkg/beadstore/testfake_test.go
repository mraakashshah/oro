package beadstore_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"oro/pkg/beadstore"
	"oro/pkg/cards"
	"oro/pkg/protocol"
)

func TestFakeStore(t *testing.T) {
	t.Run("satisfies store interface", func(t *testing.T) {
		var _ beadstore.Store = beadstore.NewFakeStore()
	})

	t.Run("creates shows updates and closes beads", func(t *testing.T) {
		ctx := context.Background()
		store := beadstore.NewFakeStore()

		created, err := store.Create(ctx, beadstore.CreateParams{
			ID:                 "oro-fake-1",
			Title:              "Implement fake store",
			Type:               "task",
			Priority:           1,
			Description:        "in-memory implementation",
			AcceptanceCriteria: "fake satisfies Store",
			ParentID:           "oro-epic",
			Tags:               []string{"phase-1"},
			Labels:             []string{"beadstore"},
			Metadata:           map[string]string{"branch": "bead/oro-nq1m"},
			EstimatedMinutes:   5,
		})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if created.ID != "oro-fake-1" || created.Status != "open" || created.Epic != "oro-epic" {
			t.Fatalf("Create returned unexpected bead: %#v", created)
		}

		shown, err := store.Show(ctx, "oro-fake-1")
		if err != nil {
			t.Fatalf("Show: %v", err)
		}
		if !reflect.DeepEqual(shown, created) {
			t.Fatalf("Show mismatch:\n got: %#v\nwant: %#v", shown, created)
		}

		// Returned beads must be copies so tests cannot mutate store state by accident.
		shown.Tags[0] = "mutated"
		shown.Metadata["branch"] = "mutated"
		again, err := store.Show(ctx, "oro-fake-1")
		if err != nil {
			t.Fatalf("Show after mutation: %v", err)
		}
		if again.Tags[0] != "phase-1" || again.Metadata["branch"] != "bead/oro-nq1m" {
			t.Fatalf("Show returned aliased state: %#v", again)
		}

		inProgress := "in_progress"
		priority := 2
		owner := "worker-1"
		if err := store.Update(ctx, "oro-fake-1", beadstore.UpdateParams{
			Status:   &inProgress,
			Priority: &priority,
			Owner:    &owner,
		}); err != nil {
			t.Fatalf("Update: %v", err)
		}
		updated, err := store.Show(ctx, "oro-fake-1")
		if err != nil {
			t.Fatalf("Show updated: %v", err)
		}
		if updated.Status != "in_progress" || updated.Priority != 2 || updated.Owner != "worker-1" {
			t.Fatalf("Update did not apply pointer fields: %#v", updated)
		}
		parentID := ""
		if err := store.Update(ctx, "oro-fake-1", beadstore.UpdateParams{ParentID: &parentID}); err != nil {
			t.Fatalf("Update clear parent: %v", err)
		}
		withoutParent, err := store.Show(ctx, "oro-fake-1")
		if err != nil {
			t.Fatalf("Show without parent: %v", err)
		}
		if withoutParent.Epic != "" {
			t.Fatalf("Update ParentID empty did not clear parent: %#v", withoutParent)
		}

		if err := store.Close(ctx, "oro-fake-1", "done"); err != nil {
			t.Fatalf("Close: %v", err)
		}
		closed, err := store.Show(ctx, "oro-fake-1")
		if err != nil {
			t.Fatalf("Show closed: %v", err)
		}
		if closed.Status != "closed" || closed.CloseReason != "done" || closed.ClosedAt == "" {
			t.Fatalf("Close did not mark bead closed: %#v", closed)
		}
	})

	t.Run("filters by status and dependency state", func(t *testing.T) {
		ctx := context.Background()
		store := beadstore.NewFakeStore(
			protocol.Bead{ID: "open", Title: "ready", Status: "open", Priority: 2},
			protocol.Bead{ID: "assigned", Title: "assigned", Status: "open", Priority: 1, WorkerID: "worker-1"},
			protocol.Bead{ID: "in-progress", Title: "active", Status: "in_progress", Priority: 1},
			protocol.Bead{ID: "closed", Title: "done", Status: "closed", Priority: 1},
			protocol.Bead{
				ID:       "child-metadata",
				Title:    "child metadata",
				Status:   "open",
				Priority: 1,
				Dependencies: []protocol.Dependency{{
					IssueID:     "child-metadata",
					DependsOnID: "open",
					Type:        "parent-child",
				}},
			},
			protocol.Bead{
				ID:         "deferred-child",
				Title:      "deferred child metadata",
				Status:     "open",
				Priority:   1,
				DeferUntil: "2999-01-01T00:00:00Z",
				Dependencies: []protocol.Dependency{{
					IssueID:     "deferred-child",
					DependsOnID: "dependency",
					Type:        "parent-child",
				}},
			},
			protocol.Bead{
				ID:       "blocked",
				Title:    "waiting",
				Status:   "open",
				Priority: 1,
				Dependencies: []protocol.Dependency{{
					IssueID:     "blocked",
					DependsOnID: "dependency",
					Type:        "blocks",
				}},
			},
			protocol.Bead{
				ID:         "deferred-hard-blocked",
				Title:      "deferred but hard blocked",
				Status:     "open",
				Priority:   1,
				DeferUntil: "2999-01-01T00:00:00Z",
				Dependencies: []protocol.Dependency{{
					IssueID:     "deferred-hard-blocked",
					DependsOnID: "dependency",
					Type:        "blocks",
				}},
			},
			protocol.Bead{
				ID:       "blocked-empty-type",
				Title:    "waiting on empty dependency type",
				Status:   "open",
				Priority: 1,
				Dependencies: []protocol.Dependency{{
					IssueID:     "blocked-empty-type",
					DependsOnID: "dependency",
				}},
			},
			protocol.Bead{
				ID:       "blocked-conditional",
				Title:    "waiting on conditional dependency",
				Status:   "open",
				Priority: 1,
				Dependencies: []protocol.Dependency{{
					IssueID:     "blocked-conditional",
					DependsOnID: "dependency",
					Type:        "conditional-blocks",
				}},
			},
			protocol.Bead{ID: "dependency", Title: "dependency", Status: "open", Priority: 1},
		)

		ready, err := store.Ready(ctx)
		if err != nil {
			t.Fatalf("Ready: %v", err)
		}
		assertIDs(t, ready, []string{"blocked-empty-type", "child-metadata", "dependency", "open"})

		inProgress, err := store.InProgress(ctx)
		if err != nil {
			t.Fatalf("InProgress: %v", err)
		}
		assertIDs(t, inProgress, []string{"assigned", "in-progress"})

		blocked, err := store.Blocked(ctx)
		if err != nil {
			t.Fatalf("Blocked: %v", err)
		}
		assertIDs(t, blocked, []string{"blocked", "blocked-conditional", "deferred-hard-blocked"})
		counts, err := store.CountByStatus(ctx)
		if err != nil {
			t.Fatalf("CountByStatus: %v", err)
		}
		if counts != (beadstore.StatusCounts{Open: 8, InProgress: 2, Closed: 1}) {
			t.Fatalf("CountByStatus = %#v, want active assignment counted as in_progress", counts)
		}

		if err := store.Close(ctx, "dependency", "unblocked"); err != nil {
			t.Fatalf("Close dependency: %v", err)
		}
		ready, err = store.Ready(ctx)
		if err != nil {
			t.Fatalf("Ready after dependency close: %v", err)
		}
		assertIDs(t, ready, []string{"blocked", "blocked-conditional", "blocked-empty-type", "child-metadata", "open"})
	})

	t.Run("reports child and tag queries", func(t *testing.T) {
		ctx := context.Background()
		store := beadstore.NewFakeStore(
			protocol.Bead{ID: "epic", Title: "epic", Status: "open", Type: "epic"},
			protocol.Bead{ID: "child-open", Title: "child", Status: "open", Epic: "epic", Tags: []string{"phase-1"}},
			protocol.Bead{ID: "child-closed", Title: "child", Status: "closed", Epic: "epic", Tags: []string{"phase-2"}},
		)

		hasChildren, err := store.HasChildren(ctx, "epic")
		if err != nil {
			t.Fatalf("HasChildren: %v", err)
		}
		if !hasChildren {
			t.Fatal("HasChildren = false, want true")
		}

		allClosed, err := store.AllChildrenClosed(ctx, "epic")
		if err != nil {
			t.Fatalf("AllChildrenClosed: %v", err)
		}
		if allClosed {
			t.Fatal("AllChildrenClosed = true with an open child")
		}

		if err := store.Close(ctx, "child-open", "done"); err != nil {
			t.Fatalf("Close child: %v", err)
		}
		allClosed, err = store.AllChildrenClosed(ctx, "epic")
		if err != nil {
			t.Fatalf("AllChildrenClosed after close: %v", err)
		}
		if !allClosed {
			t.Fatal("AllChildrenClosed = false after all children closed")
		}

		tagged, err := store.FindByParentAndTag(ctx, "epic", "phase-1")
		if err != nil {
			t.Fatalf("FindByParentAndTag: %v", err)
		}
		assertIDs(t, tagged, []string{"child-open"})
	})

	t.Run("copies nested metadata values", func(t *testing.T) {
		ctx := context.Background()
		store := beadstore.NewFakeStore(protocol.Bead{
			ID:     "metadata",
			Title:  "metadata",
			Status: "open",
			Metadata: map[string]any{
				"nested": map[string]any{"key": "value"},
				"tags":   []string{"initial"},
			},
		})

		shown, err := store.Show(ctx, "metadata")
		if err != nil {
			t.Fatalf("Show: %v", err)
		}
		shown.Metadata["nested"].(map[string]any)["key"] = "mutated"
		shown.Metadata["tags"].([]string)[0] = "mutated"

		again, err := store.Show(ctx, "metadata")
		if err != nil {
			t.Fatalf("Show again: %v", err)
		}
		if again.Metadata["nested"].(map[string]any)["key"] != "value" ||
			again.Metadata["tags"].([]string)[0] != "initial" {
			t.Fatalf("Show returned aliased nested metadata: %#v", again.Metadata)
		}
	})

	t.Run("leaves no-op updates unchanged", func(t *testing.T) {
		ctx := context.Background()
		store := beadstore.NewFakeStore(protocol.Bead{
			ID:        "noop",
			Title:     "noop",
			Status:    "open",
			UpdatedAt: "original",
		})

		if err := store.Update(ctx, "noop", beadstore.UpdateParams{}); err != nil {
			t.Fatalf("Update no-op: %v", err)
		}
		shown, err := store.Show(ctx, "noop")
		if err != nil {
			t.Fatalf("Show: %v", err)
		}
		if shown.UpdatedAt != "original" {
			t.Fatalf("no-op Update changed UpdatedAt to %q", shown.UpdatedAt)
		}
	})

	t.Run("returns not found errors for mutating missing beads", func(t *testing.T) {
		ctx := context.Background()
		store := beadstore.NewFakeStore()

		shown, err := store.Show(ctx, "missing")
		if err != nil {
			t.Fatalf("Show missing: %v", err)
		}
		if shown != nil {
			t.Fatalf("Show missing = %#v, want nil", shown)
		}

		status := "closed"
		err = store.Update(ctx, "missing", beadstore.UpdateParams{Status: &status})
		var notFound *protocol.BeadNotFoundError
		if !errors.As(err, &notFound) {
			t.Fatalf("Update missing error = %v, want BeadNotFoundError", err)
		}

		err = store.Close(ctx, "missing", "done")
		if !errors.As(err, &notFound) {
			t.Fatalf("Close missing error = %v, want BeadNotFoundError", err)
		}
	})

	t.Run("returns closed beads newest first capped by positive limit", func(t *testing.T) {
		ctx := context.Background()
		store := beadstore.NewFakeStore(
			protocol.Bead{ID: "old", Title: "old", Status: "closed", ClosedAt: "2026-04-28T10:00:00Z"},
			protocol.Bead{ID: "new-b", Title: "new b", Status: "closed", ClosedAt: "2026-04-28T12:00:00Z"},
			protocol.Bead{ID: "new-a", Title: "new a", Status: "closed", ClosedAt: "2026-04-28T12:00:00Z"},
			protocol.Bead{ID: "open", Title: "open", Status: "open"},
		)

		closed, err := store.Closed(ctx, 2)
		if err != nil {
			t.Fatalf("Closed: %v", err)
		}
		assertIDs(t, closed, []string{"new-a", "new-b"})

		closed, err = store.Closed(ctx, 0)
		if err != nil {
			t.Fatalf("Closed zero limit: %v", err)
		}
		assertIDs(t, closed, []string{})

		closed, err = store.Closed(ctx, -1)
		if err != nil {
			t.Fatalf("Closed negative limit: %v", err)
		}
		assertIDs(t, closed, []string{})
	})

	t.Run("exports jsonl snapshot", func(t *testing.T) {
		ctx := context.Background()
		store := beadstore.NewFakeStore(
			protocol.Bead{ID: "b", Title: "second", Status: "open"},
			protocol.Bead{ID: "a", Title: "first", Status: "closed"},
		)

		data, err := store.Export(ctx)
		if err != nil {
			t.Fatalf("Export: %v", err)
		}

		scanner := bufio.NewScanner(bytesReader(data))
		var got []string
		for scanner.Scan() {
			var bead protocol.Bead
			if err := json.Unmarshal(scanner.Bytes(), &bead); err != nil {
				t.Fatalf("export line is not JSON bead: %v", err)
			}
			got = append(got, bead.ID)
		}
		if err := scanner.Err(); err != nil {
			t.Fatalf("scan export: %v", err)
		}
		if !reflect.DeepEqual(got, []string{"a", "b"}) {
			t.Fatalf("Export IDs = %v, want [a b]", got)
		}
	})

	t.Run("test helpers clone state and track closed order", func(t *testing.T) {
		ctx := context.Background()
		store := beadstore.NewFakeStore(protocol.Bead{ID: "fake-1", Title: "seeded id", Status: "open"})

		created, err := store.Create(ctx, beadstore.CreateParams{Title: "generated id skips collision"})
		if err != nil {
			t.Fatalf("Create generated id: %v", err)
		}
		if created.ID != "fake-2" {
			t.Fatalf("generated fake ID = %q, want fake-2", created.ID)
		}

		input := []protocol.Bead{{
			ID:     "defaulted",
			Title:  "defaulted",
			Tags:   []string{"initial"},
			Labels: []string{"label"},
		}}
		store.SetBeads(input)
		input[0].Title = "mutated input"
		input[0].Tags[0] = "mutated"

		shown, err := store.Show(ctx, "defaulted")
		if err != nil {
			t.Fatalf("Show defaulted: %v", err)
		}
		if shown.Status != "open" || shown.AcceptanceCriteria != "Test: auto | Assert: PASS" {
			t.Fatalf("SetBeads defaulted bead = %#v", shown)
		}
		if shown.Title != "defaulted" || shown.Tags[0] != "initial" {
			t.Fatalf("SetBeads stored aliased input: %#v", shown)
		}

		if _, err := store.Create(ctx, beadstore.CreateParams{ID: "second", Title: "second"}); err != nil {
			t.Fatalf("Create second: %v", err)
		}
		if err := store.Close(ctx, "defaulted", "done"); err != nil {
			t.Fatalf("Close defaulted: %v", err)
		}
		if err := store.Close(ctx, "second", "done"); err != nil {
			t.Fatalf("Close second: %v", err)
		}

		closed := store.ClosedBeads()
		if !reflect.DeepEqual(closed, []string{"defaulted", "second"}) {
			t.Fatalf("ClosedBeads = %v, want close order", closed)
		}
		closed[0] = "mutated"
		if got := store.ClosedBeads(); !reflect.DeepEqual(got, []string{"defaulted", "second"}) {
			t.Fatalf("ClosedBeads returned aliased slice: %v", got)
		}
	})
}

func TestFakeStoreFindByMetadataKey(t *testing.T) {
	ctx := context.Background()
	store := beadstore.NewFakeStore(
		protocol.Bead{ID: "open", Status: "open", Metadata: map[string]any{"meta_finding_id": "finding-open"}},
		protocol.Bead{ID: "closed", Status: "closed", Metadata: map[string]any{"meta_finding_id": "finding-closed"}},
		protocol.Bead{ID: "other", Status: "open", Metadata: map[string]any{"other": "value"}},
	)

	matches, err := store.FindByMetadataKey(ctx, "meta_finding_id")
	if err != nil {
		t.Fatalf("FindByMetadataKey: %v", err)
	}
	if len(matches) != 2 || matches[0] == nil || matches[1] == nil {
		t.Fatalf("FindByMetadataKey = %#v, want two non-nil bead pointers", matches)
	}
	if matches[0].ID != "closed" || matches[1].ID != "open" {
		t.Fatalf("FindByMetadataKey IDs = [%s %s], want [closed open]", matches[0].ID, matches[1].ID)
	}

	matches[0].Metadata["meta_finding_id"] = "mutated"
	again, err := store.FindByMetadataKey(ctx, "meta_finding_id")
	if err != nil {
		t.Fatalf("FindByMetadataKey after mutation: %v", err)
	}
	if again[0].Metadata["meta_finding_id"] != "finding-closed" {
		t.Fatalf("FindByMetadataKey returned aliased metadata: %#v", again[0].Metadata)
	}

	if _, err := store.FindByMetadataKey(ctx, " "); err == nil {
		t.Fatal("FindByMetadataKey(empty key) error = nil, want error")
	}
	none, err := store.FindByMetadataKey(ctx, "missing")
	if err != nil {
		t.Fatalf("FindByMetadataKey(missing): %v", err)
	}
	if none == nil || len(none) != 0 {
		t.Fatalf("FindByMetadataKey(missing) = %#v, want non-nil empty slice", none)
	}
}

func TestFakeStoreDelete(t *testing.T) {
	ctx := context.Background()
	store := beadstore.NewFakeStore(protocol.Bead{ID: "delete-me", Title: "delete me", Status: "open"})

	if err := store.Delete(ctx, "delete-me", ""); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	shown, err := store.Show(ctx, "delete-me")
	if err != nil {
		t.Fatalf("Show deleted: %v", err)
	}
	if shown != nil {
		t.Fatalf("Show deleted = %#v, want nil", shown)
	}

	events, err := store.Journey(ctx, "delete-me", time.Time{})
	if err != nil {
		t.Fatalf("Journey deleted: %v", err)
	}
	if len(events) != 1 || events[0].Event != "deleted" || !strings.Contains(events[0].Payload, "deleted by user") {
		t.Fatalf("Delete journey = %#v, want deleted event with default reason", events)
	}

	var notFound *protocol.BeadNotFoundError
	if err := store.Delete(ctx, "delete-me", "again"); !errors.As(err, &notFound) {
		t.Fatalf("second Delete error = %v, want BeadNotFoundError", err)
	}
	if err := store.Delete(ctx, "missing", "missing"); !errors.As(err, &notFound) {
		t.Fatalf("missing Delete error = %v, want BeadNotFoundError", err)
	}
}

func TestFakeStoreCards(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()

	seeded := cards.Card{
		ID:          "card-abc",
		Type:        cards.CardTypeRule,
		Title:       "Always write tests",
		BodySummary: "Rules for testing",
		BodyFull:    "Test all the things",
		Tags:        []string{"testing"},
		Score:       1.0,
		DecayAnchor: now,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	store := beadstore.NewFakeStore()
	store.SetCards([]cards.Card{seeded})

	var gotCardTx cards.ReadTx
	if err := store.WithReadTx(ctx, func(tx beadstore.ReadTx) error {
		gotCardTx = tx.Cards()
		return nil
	}); err != nil {
		t.Fatalf("WithReadTx: %v", err)
	}

	if gotCardTx == nil {
		t.Fatal("Cards() returned nil, want non-nil cards.ReadTx")
	}

	t.Run("Show returns seeded card", func(t *testing.T) {
		got, err := gotCardTx.Show(ctx, "card-abc")
		if err != nil {
			t.Fatalf("Show: %v", err)
		}
		if got == nil || got.ID != "card-abc" {
			t.Fatalf("Show = %v, want card-abc", got)
		}
	})

	t.Run("Show returns ErrNotFound for missing id", func(t *testing.T) {
		_, err := gotCardTx.Show(ctx, "missing")
		if !errors.Is(err, cards.ErrNotFound) {
			t.Fatalf("Show missing = %v, want cards.ErrNotFound", err)
		}
	})

	t.Run("List returns seeded cards", func(t *testing.T) {
		listed, err := gotCardTx.List(ctx, cards.ListQuery{})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(listed) != 1 || listed[0].ID != "card-abc" {
			t.Fatalf("List = %v, want [card-abc]", listed)
		}
	})

	t.Run("Relevant returns seeded card in Deck", func(t *testing.T) {
		rel, err := gotCardTx.Relevant(ctx, cards.RelevanceQuery{IncludeLowScore: true})
		if err != nil {
			t.Fatalf("Relevant: %v", err)
		}
		if len(rel.Deck) != 1 || rel.Deck[0].ID != "card-abc" {
			t.Fatalf("Relevant.Deck = %v, want [card-abc]", rel.Deck)
		}
	})
}

func TestFakeReadTxDelegatesStoreReads(t *testing.T) {
	ctx := context.Background()
	base := time.Now().UTC().Add(-time.Minute)
	store := beadstore.NewFakeStore(
		protocol.Bead{ID: "epic", Title: "epic", Status: "open"},
		protocol.Bead{
			ID:       "child-open",
			Title:    "open child",
			Status:   "open",
			Epic:     "epic",
			Tags:     []string{"ready"},
			Metadata: map[string]any{"source": "test"},
		},
		protocol.Bead{ID: "child-closed", Title: "closed child", Status: "closed", Epic: "epic"},
	)
	for i, event := range []string{"created", "updated"} {
		if err := store.AppendJourney(ctx, "child-open", beadstore.JourneyEvent{
			Ts:    base.Add(time.Duration(i) * time.Second).Format(time.RFC3339Nano),
			Actor: "test",
			Event: event,
		}); err != nil {
			t.Fatalf("AppendJourney(%q): %v", event, err)
		}
	}
	store.SetCards([]cards.Card{{ID: "card", Type: cards.CardTypeRule, Title: "card"}})

	if err := store.WithReadTx(ctx, func(tx beadstore.ReadTx) error {
		ready, err := tx.Ready(ctx)
		if err != nil {
			return err
		}
		if len(ready) != 2 || ready[0].ID != "child-open" || ready[1].ID != "epic" {
			t.Fatalf("Ready() = %#v, want child-open then epic", ready)
		}

		inProgress, err := tx.InProgress(ctx)
		if err != nil {
			return err
		}
		if len(inProgress) != 0 {
			t.Fatalf("InProgress() = %#v, want no in-progress beads", inProgress)
		}

		blocked, err := tx.Blocked(ctx)
		if err != nil {
			return err
		}
		if len(blocked) != 0 {
			t.Fatalf("Blocked() = %#v, want no blocked beads", blocked)
		}

		closed, err := tx.Closed(ctx, 1)
		if err != nil {
			return err
		}
		if len(closed) != 1 || closed[0].ID != "child-closed" {
			t.Fatalf("Closed(1) = %#v, want child-closed", closed)
		}
		noClosed, err := tx.Closed(ctx, 0)
		if err != nil {
			return err
		}
		if len(noClosed) != 0 {
			t.Fatalf("Closed(0) = %#v, want empty", noClosed)
		}

		shown, err := tx.Show(ctx, "child-open")
		if err != nil {
			return err
		}
		if shown == nil || shown.ID != "child-open" {
			t.Fatalf("Show(child-open) = %#v", shown)
		}
		hasChildren, err := tx.HasChildren(ctx, "epic")
		if err != nil {
			return err
		}
		if !hasChildren {
			t.Fatal("HasChildren(epic) = false, want true")
		}
		allClosed, err := tx.AllChildrenClosed(ctx, "epic")
		if err != nil {
			return err
		}
		if allClosed {
			t.Fatal("AllChildrenClosed(epic) = true, want false")
		}

		byTag, err := tx.FindByParentAndTag(ctx, "epic", "ready")
		if err != nil {
			return err
		}
		if len(byTag) != 1 || byTag[0].ID != "child-open" {
			t.Fatalf("FindByParentAndTag = %#v, want child-open", byTag)
		}
		byMetadata, err := tx.FindByMetadataKey(ctx, "source")
		if err != nil {
			return err
		}
		if len(byMetadata) != 1 || byMetadata[0].ID != "child-open" {
			t.Fatalf("FindByMetadataKey = %#v, want child-open", byMetadata)
		}

		journey, err := tx.Journey(ctx, "child-open", base)
		if err != nil {
			return err
		}
		if len(journey) != 2 {
			t.Fatalf("Journey() = %#v, want two events", journey)
		}
		latest, err := tx.LatestJourney(ctx, "child-open", 1)
		if err != nil {
			return err
		}
		if len(latest) != 1 || latest[0].Event != "updated" {
			t.Fatalf("LatestJourney(1) = %#v, want updated", latest)
		}
		card, err := tx.Cards().Show(ctx, "card")
		if err != nil {
			return err
		}
		if card == nil || card.ID != "card" {
			t.Fatalf("Cards().Show(card) = %#v", card)
		}
		return nil
	}); err != nil {
		t.Fatalf("WithReadTx: %v", err)
	}
}

func TestFakeCardsReadTx_Filters(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	retiredNow := now

	active := cards.Card{
		ID: "active", Type: cards.CardTypeRule, Title: "A",
		BodyFull: "body-active", Score: 1.0, DecayAnchor: now, CreatedAt: now, UpdatedAt: now,
	}
	retired := cards.Card{
		ID: "retired", Type: cards.CardTypeRule, Title: "R",
		BodyFull: "body-retired", Score: 1.0, DecayAnchor: now, CreatedAt: now, UpdatedAt: now,
		RetiredAt: &retiredNow,
	}
	pattern := cards.Card{
		ID: "pattern-1", Type: cards.CardTypePattern, Title: "P",
		BodyFull: "body-pattern", Score: 1.0, DecayAnchor: now, CreatedAt: now, UpdatedAt: now,
	}

	store := beadstore.NewFakeStore()
	store.SetCards([]cards.Card{active, retired, pattern})

	getTx := func(t *testing.T) cards.ReadTx {
		t.Helper()
		var tx cards.ReadTx
		if err := store.WithReadTx(ctx, func(readTx beadstore.ReadTx) error {
			tx = readTx.Cards()
			return nil
		}); err != nil {
			t.Fatalf("WithReadTx: %v", err)
		}
		return tx
	}

	t.Run("List excludes retired by default", func(t *testing.T) {
		got, err := getTx(t).List(ctx, cards.ListQuery{})
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range got {
			if c.ID == "retired" {
				t.Fatal("retired card included in default List")
			}
		}
	})

	t.Run("List includes retired when IncludeRetired=true", func(t *testing.T) {
		got, err := getTx(t).List(ctx, cards.ListQuery{IncludeRetired: true})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 3 {
			t.Fatalf("List(IncludeRetired) = %d cards, want 3", len(got))
		}
	})

	t.Run("List filters by Type", func(t *testing.T) {
		got, err := getTx(t).List(ctx, cards.ListQuery{Type: cards.CardTypePattern})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].ID != "pattern-1" {
			t.Fatalf("List(Type=pattern) = %v, want [pattern-1]", got)
		}
	})

	t.Run("List Offset skips cards", func(t *testing.T) {
		got, err := getTx(t).List(ctx, cards.ListQuery{Offset: 1})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 {
			t.Fatalf("List(Offset=1) = %d cards, want 1", len(got))
		}
	})

	t.Run("List Offset beyond end returns nil", func(t *testing.T) {
		got, err := getTx(t).List(ctx, cards.ListQuery{Offset: 100})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 0 {
			t.Fatalf("List(Offset=100) = %v, want empty", got)
		}
	})

	t.Run("List Limit caps results", func(t *testing.T) {
		got, err := getTx(t).List(ctx, cards.ListQuery{Limit: 1})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 {
			t.Fatalf("List(Limit=1) = %d cards, want 1", len(got))
		}
	})

	t.Run("Relevant with MaxTokens inlines within budget", func(t *testing.T) {
		rel, err := getTx(t).Relevant(ctx, cards.RelevanceQuery{
			IncludeLowScore: true,
			MaxTokens:       1000,
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(rel.Deck) == 0 {
			t.Fatal("Relevant.Deck is empty")
		}
		if len(rel.Inlined) == 0 {
			t.Fatal("Relevant.Inlined is empty with MaxTokens=1000")
		}
	})
}

func TestFakeStoreDependencyCyclesReflectActiveBlockingDependencies(t *testing.T) {
	ctx := context.Background()
	store := beadstore.NewFakeStore(
		protocol.Bead{ID: "alpha", Status: "open"},
		protocol.Bead{ID: "bravo", Status: "open"},
		protocol.Bead{ID: "charlie", Status: "open"},
		protocol.Bead{ID: "closed", Status: "closed"},
	)
	for _, edge := range []struct {
		beadID      string
		dependsOnID string
		depType     string
	}{
		{beadID: "alpha", dependsOnID: "bravo", depType: "blocks"},
		{beadID: "bravo", dependsOnID: "charlie", depType: "conditional-blocks"},
		{beadID: "charlie", dependsOnID: "alpha", depType: "blocks"},
		{beadID: "alpha", dependsOnID: "closed", depType: "blocks"},
		{beadID: "bravo", dependsOnID: "closed", depType: "parent-child"},
	} {
		if err := store.AddDependency(ctx, edge.beadID, edge.dependsOnID, edge.depType); err != nil {
			t.Fatalf("AddDependency(%q, %q, %q): %v", edge.beadID, edge.dependsOnID, edge.depType, err)
		}
	}

	cycles, err := store.DependencyCycles(ctx)
	if err != nil {
		t.Fatalf("DependencyCycles: %v", err)
	}
	if len(cycles) != 1 {
		t.Fatalf("DependencyCycles() = %#v, want one active blocking cycle", cycles)
	}
	if len(cycles[0]) != 4 || cycles[0][0] != cycles[0][3] {
		t.Fatalf("DependencyCycles() = %#v, want a closed three-node cycle", cycles)
	}

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := store.DependencyCycles(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("DependencyCycles(canceled) error = %v, want context.Canceled", err)
	}
}

func TestFakeStoreDeepClonesMetadata(t *testing.T) {
	ctx := context.Background()
	store := beadstore.NewFakeStore(protocol.Bead{
		ID:     "metadata",
		Status: "open",
		Metadata: map[string]any{
			"map":     map[string]any{"inner": "original"},
			"strings": map[string]string{"inner": "original"},
			"any":     []any{map[string]any{"inner": "original"}},
			"slice":   []string{"original"},
		},
	})

	shown, err := store.Show(ctx, "metadata")
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	shown.Metadata["map"].(map[string]any)["inner"] = "mutated"
	shown.Metadata["strings"].(map[string]any)["inner"] = "mutated"
	shown.Metadata["any"].([]any)[0].(map[string]any)["inner"] = "mutated"
	shown.Metadata["slice"].([]string)[0] = "mutated"

	again, err := store.Show(ctx, "metadata")
	if err != nil {
		t.Fatalf("Show after metadata mutation: %v", err)
	}
	if got := again.Metadata["map"].(map[string]any)["inner"]; got != "original" {
		t.Fatalf("map metadata = %q, want original", got)
	}
	if got := again.Metadata["strings"].(map[string]any)["inner"]; got != "original" {
		t.Fatalf("string-map metadata = %q, want original", got)
	}
	if got := again.Metadata["any"].([]any)[0].(map[string]any)["inner"]; got != "original" {
		t.Fatalf("slice metadata = %q, want original", got)
	}
	if got := again.Metadata["slice"].([]string)[0]; got != "original" {
		t.Fatalf("string-slice metadata = %q, want original", got)
	}
}

func TestFakeStoreRejectsCanceledContexts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	store := beadstore.NewFakeStore(protocol.Bead{ID: "bead", Status: "open"})

	checks := map[string]func() error{
		"Ready": func() error {
			_, err := store.Ready(ctx)
			return err
		},
		"InProgress": func() error {
			_, err := store.InProgress(ctx)
			return err
		},
		"Blocked": func() error {
			_, err := store.Blocked(ctx)
			return err
		},
		"Closed": func() error {
			_, err := store.Closed(ctx, 1)
			return err
		},
		"Show": func() error {
			_, err := store.Show(ctx, "bead")
			return err
		},
		"Create": func() error {
			_, err := store.Create(ctx, beadstore.CreateParams{ID: "created"})
			return err
		},
		"Update":           func() error { return store.Update(ctx, "bead", beadstore.UpdateParams{}) },
		"Close":            func() error { return store.Close(ctx, "bead", "reason") },
		"Delete":           func() error { return store.Delete(ctx, "bead", "reason") },
		"AddDependency":    func() error { return store.AddDependency(ctx, "bead", "other", "blocks") },
		"RemoveDependency": func() error { return store.RemoveDependency(ctx, "bead", "other") },
		"ListDependencies": func() error {
			_, err := store.ListDependencies(ctx, "bead")
			return err
		},
		"CountByStatus": func() error {
			_, err := store.CountByStatus(ctx)
			return err
		},
		"Defer":   func() error { return store.Defer(ctx, "bead", time.Now().Add(time.Hour).Format(time.RFC3339Nano)) },
		"Undefer": func() error { return store.Undefer(ctx, "bead") },
		"HasChildren": func() error {
			_, err := store.HasChildren(ctx, "bead")
			return err
		},
		"AllChildrenClosed": func() error {
			_, err := store.AllChildrenClosed(ctx, "bead")
			return err
		},
		"FindByParentAndTag": func() error {
			_, err := store.FindByParentAndTag(ctx, "bead", "tag")
			return err
		},
		"FindByMetadataKey": func() error {
			_, err := store.FindByMetadataKey(ctx, "key")
			return err
		},
		"CountChildren": func() error {
			_, err := store.CountChildren(ctx, "bead")
			return err
		},
		"Export": func() error {
			_, err := store.Export(ctx)
			return err
		},
	}
	for name, check := range checks {
		t.Run(name, func(t *testing.T) {
			if err := check(); !errors.Is(err, context.Canceled) {
				t.Fatalf("error = %v, want context.Canceled", err)
			}
		})
	}
}

func TestCardsRelevantDeckOmitsBodyFull(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	store := beadstore.NewFakeStore()
	store.SetCards([]cards.Card{
		{
			ID: "card-with-body", Type: cards.CardTypePattern, Title: "wire shape",
			BodySummary: "summary",
			BodyFull:    "DECK_FULL_BODY_SENTINEL INLINE_FULL_BODY_SENTINEL",
			Score:       1.0,
			DecayAnchor: now,
			CreatedAt:   now,
			UpdatedAt:   now,
		},
	})

	var rel cards.RelevantCards
	if err := store.WithReadTx(ctx, func(readTx beadstore.ReadTx) error {
		got, err := readTx.Cards().Relevant(ctx, cards.RelevanceQuery{
			IncludeLowScore: true,
			MaxTokens:       1000,
		})
		rel = got
		return err
	}); err != nil {
		t.Fatalf("Relevant: %v", err)
	}

	deckJSON, err := json.Marshal(rel.Deck)
	if err != nil {
		t.Fatalf("marshal deck: %v", err)
	}
	if bytes.Contains(deckJSON, []byte("DECK_FULL_BODY_SENTINEL")) {
		t.Fatalf("Relevant.Deck JSON includes full body: %s", deckJSON)
	}

	inlinedJSON, err := json.Marshal(rel.Inlined)
	if err != nil {
		t.Fatalf("marshal inlined: %v", err)
	}
	if !bytes.Contains(inlinedJSON, []byte("INLINE_FULL_BODY_SENTINEL")) {
		t.Fatalf("Relevant.Inlined JSON = %s, want full body sentinel", inlinedJSON)
	}
}

func assertIDs(t *testing.T, beads []protocol.Bead, want []string) {
	t.Helper()
	got := make([]string, 0, len(beads))
	for _, bead := range beads {
		got = append(got, bead.ID)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("IDs = %v, want %v", got, want)
	}
}

func bytesReader(data []byte) *bufio.Reader {
	return bufio.NewReader(bytes.NewReader(data))
}
