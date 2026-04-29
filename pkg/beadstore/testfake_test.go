package beadstore_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"oro/pkg/beadstore"
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
		assertIDs(t, blocked, []string{"blocked", "blocked-conditional"})

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
