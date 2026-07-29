package beadstore_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"oro/pkg/beadstore"
	"oro/pkg/protocol"
)

func TestShadowStore(t *testing.T) {
	t.Run("conditionally writes only to primary", func(t *testing.T) {
		ctx := context.Background()
		primary := beadstore.NewFakeStore(protocol.Bead{ID: "cas", Status: "in_progress"})
		secondary := beadstore.NewFakeStore(protocol.Bead{ID: "cas", Status: "in_progress"})
		store := beadstore.NewShadowStore(primary, secondary)

		updated, err := store.UpdateStatusIf(ctx, "cas", "in_progress", "open")
		if err != nil || !updated {
			t.Fatalf("UpdateStatusIf = %t, %v; want true, nil", updated, err)
		}
		primaryBead, err := primary.Show(ctx, "cas")
		if err != nil {
			t.Fatalf("primary Show: %v", err)
		}
		secondaryBead, err := secondary.Show(ctx, "cas")
		if err != nil {
			t.Fatalf("secondary Show: %v", err)
		}
		if primaryBead.Status != "open" || secondaryBead.Status != "in_progress" {
			t.Fatalf("statuses = primary %q, secondary %q; want open, in_progress", primaryBead.Status, secondaryBead.Status)
		}
	})

	t.Run("dual reads every read method and returns primary results", func(t *testing.T) {
		ctx := context.Background()
		seed := []protocol.Bead{
			{ID: "ready", Title: "ready", Status: "open", Priority: 1, Metadata: map[string]any{"meta_finding_id": "finding-ready"}, UpdatedAt: "2026-04-28T09:00:00Z"},
			{ID: "progress", Title: "progress", Status: "in_progress", Priority: 2, Epic: "epic", Tags: []string{"phase"}, UpdatedAt: "2026-04-28T09:00:00Z"},
			{ID: "closed", Title: "closed", Status: "closed", Priority: 3, Epic: "epic", UpdatedAt: "2026-04-28T09:00:00Z"},
		}
		primary := &recordingStore{Store: beadstore.NewFakeStore(seed...)}
		secondary := &recordingStore{Store: beadstore.NewFakeStore(seed...)}
		store := beadstore.NewShadowStore(primary, secondary)

		ready, err := store.Ready(ctx)
		if err != nil {
			t.Fatalf("Ready: %v", err)
		}
		if len(ready) != 1 || ready[0].ID != "ready" {
			t.Fatalf("Ready returned %#v, want primary ready bead", ready)
		}
		if _, err := store.InProgress(ctx); err != nil {
			t.Fatalf("InProgress: %v", err)
		}
		if _, err := store.Blocked(ctx); err != nil {
			t.Fatalf("Blocked: %v", err)
		}
		if _, err := store.Closed(ctx, 1); err != nil {
			t.Fatalf("Closed: %v", err)
		}
		if shown, err := store.Show(ctx, "ready"); err != nil {
			t.Fatalf("Show: %v", err)
		} else if shown == nil || shown.ID != "ready" {
			t.Fatalf("Show returned %#v, want primary bead", shown)
		}
		if _, err := store.HasChildren(ctx, "epic"); err != nil {
			t.Fatalf("HasChildren: %v", err)
		}
		if _, err := store.AllChildrenClosed(ctx, "epic"); err != nil {
			t.Fatalf("AllChildrenClosed: %v", err)
		}
		if _, err := store.FindByParentAndTag(ctx, "epic", "phase"); err != nil {
			t.Fatalf("FindByParentAndTag: %v", err)
		}
		metadataMatches, err := store.FindByMetadataKey(ctx, "meta_finding_id")
		if err != nil {
			t.Fatalf("FindByMetadataKey: %v", err)
		}
		if len(metadataMatches) != 1 || metadataMatches[0] == nil || metadataMatches[0].ID != "ready" {
			t.Fatalf("FindByMetadataKey returned %#v, want primary ready bead", metadataMatches)
		}
		if _, err := store.Export(ctx); err != nil {
			t.Fatalf("Export: %v", err)
		}

		want := readCallCounts{ready: 1, inProgress: 1, blocked: 1, closed: 1, show: 1, hasChildren: 1, allChildrenClosed: 1, findByParentAndTag: 1, findByMetadataKey: 1, export: 1}
		if got := primary.readCalls(); got != want {
			t.Fatalf("primary read calls = %+v, want %+v", got, want)
		}
		if got := secondary.readCalls(); got != want {
			t.Fatalf("secondary read calls = %+v, want %+v", got, want)
		}
	})

	t.Run("classifies stable read mismatches as real divergence while primary wins", func(t *testing.T) {
		ctx := context.Background()
		updatedAt := "2026-04-28T09:00:00Z"
		primary := beadstore.NewFakeStore(protocol.Bead{ID: "same", Title: "primary", Status: "open", Priority: 1, UpdatedAt: updatedAt})
		secondary := beadstore.NewFakeStore(protocol.Bead{ID: "same", Title: "secondary", Status: "open", Priority: 1, UpdatedAt: updatedAt})
		var events []beadstore.ShadowDivergence
		store := beadstore.NewShadowStore(
			primary,
			secondary,
			beadstore.WithShadowStartedAt(mustParseTime(t, "2026-04-28T10:00:00Z")),
			beadstore.WithShadowDivergenceReporter(func(event beadstore.ShadowDivergence) {
				events = append(events, event)
			}),
		)

		ready, err := store.Ready(ctx)
		if err != nil {
			t.Fatalf("Ready: %v", err)
		}
		if len(ready) != 1 || ready[0].Title != "primary" {
			t.Fatalf("Ready returned %#v, want primary result", ready)
		}
		if len(events) != 1 {
			t.Fatalf("reported %d divergences, want 1: %#v", len(events), events)
		}
		if events[0].Operation != "Ready" || events[0].Kind != beadstore.ShadowDivergenceReal {
			t.Fatalf("divergence = %#v, want real Ready divergence", events[0])
		}
	})

	t.Run("reports closed-list divergences", func(t *testing.T) {
		ctx := context.Background()
		updatedAt := "2026-04-28T09:00:00Z"
		primary := beadstore.NewFakeStore(protocol.Bead{ID: "closed", Title: "primary", Status: "closed", ClosedAt: updatedAt, UpdatedAt: updatedAt})
		secondary := beadstore.NewFakeStore(protocol.Bead{ID: "closed", Title: "secondary", Status: "closed", ClosedAt: updatedAt, UpdatedAt: updatedAt})
		var events []beadstore.ShadowDivergence
		store := beadstore.NewShadowStore(
			primary,
			secondary,
			beadstore.WithShadowStartedAt(mustParseTime(t, "2026-04-28T10:00:00Z")),
			beadstore.WithShadowDivergenceReporter(func(event beadstore.ShadowDivergence) {
				events = append(events, event)
			}),
		)

		closed, err := store.Closed(ctx, 1)
		if err != nil {
			t.Fatalf("Closed: %v", err)
		}
		if len(closed) != 1 || closed[0].Title != "primary" {
			t.Fatalf("Closed returned %#v, want primary result", closed)
		}
		if len(events) != 1 {
			t.Fatalf("reported %d divergences, want 1: %#v", len(events), events)
		}
		if events[0].Operation != "Closed" || events[0].Kind != beadstore.ShadowDivergenceReal {
			t.Fatalf("divergence = %#v, want real Closed divergence", events[0])
		}
	})

	t.Run("reports show divergences", func(t *testing.T) {
		ctx := context.Background()
		updatedAt := "2026-04-28T09:00:00Z"
		primary := beadstore.NewFakeStore(protocol.Bead{ID: "shown", Title: "primary", Status: "open", UpdatedAt: updatedAt})
		secondary := beadstore.NewFakeStore(protocol.Bead{ID: "shown", Title: "secondary", Status: "open", UpdatedAt: updatedAt})
		var events []beadstore.ShadowDivergence
		store := beadstore.NewShadowStore(
			primary,
			secondary,
			beadstore.WithShadowStartedAt(mustParseTime(t, "2026-04-28T10:00:00Z")),
			beadstore.WithShadowDivergenceReporter(func(event beadstore.ShadowDivergence) {
				events = append(events, event)
			}),
		)

		shown, err := store.Show(ctx, "shown")
		if err != nil {
			t.Fatalf("Show: %v", err)
		}
		if shown == nil || shown.Title != "primary" {
			t.Fatalf("Show returned %#v, want primary result", shown)
		}
		if len(events) != 1 {
			t.Fatalf("reported %d divergences, want 1: %#v", len(events), events)
		}
		if events[0].Operation != "Show" || events[0].Kind != beadstore.ShadowDivergenceReal {
			t.Fatalf("divergence = %#v, want real Show divergence", events[0])
		}
	})

	t.Run("logs reported divergences when configured", func(t *testing.T) {
		ctx := context.Background()
		updatedAt := "2026-04-28T09:00:00Z"
		store := beadstore.NewShadowStore(
			beadstore.NewFakeStore(protocol.Bead{ID: "same", Title: "primary", Status: "open", UpdatedAt: updatedAt}),
			beadstore.NewFakeStore(protocol.Bead{ID: "same", Title: "secondary", Status: "open", UpdatedAt: updatedAt}),
			beadstore.WithShadowStartedAt(mustParseTime(t, "2026-04-28T10:00:00Z")),
			beadstore.WithShadowLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
		)

		ready, err := store.Ready(ctx)
		if err != nil {
			t.Fatalf("Ready: %v", err)
		}
		if len(ready) != 1 || ready[0].Title != "primary" {
			t.Fatalf("Ready returned %#v, want primary result", ready)
		}
	})

	t.Run("classifies primary updates during shadow as allowed drift", func(t *testing.T) {
		ctx := context.Background()
		primary := beadstore.NewFakeStore(protocol.Bead{ID: "hot", Title: "primary", Status: "open", Priority: 1, UpdatedAt: "2026-04-28T11:00:00Z"})
		secondary := beadstore.NewFakeStore(protocol.Bead{ID: "hot", Title: "secondary", Status: "open", Priority: 1, UpdatedAt: "2026-04-28T09:00:00Z"})
		var events []beadstore.ShadowDivergence
		store := beadstore.NewShadowStore(
			primary,
			secondary,
			beadstore.WithShadowStartedAt(mustParseTime(t, "2026-04-28T10:00:00Z")),
			beadstore.WithShadowDivergenceReporter(func(event beadstore.ShadowDivergence) {
				events = append(events, event)
			}),
		)

		if _, err := store.Ready(ctx); err != nil {
			t.Fatalf("Ready: %v", err)
		}
		if len(events) != 1 {
			t.Fatalf("reported %d divergences, want 1: %#v", len(events), events)
		}
		if events[0].Kind != beadstore.ShadowDivergenceDrift {
			t.Fatalf("divergence = %#v, want allowed drift", events[0])
		}
	})

	t.Run("classifies filtered secondary rows as drift when current primary changed during shadow", func(t *testing.T) {
		ctx := context.Background()
		primary := beadstore.NewFakeStore(protocol.Bead{ID: "claimed", Title: "claimed", Status: "in_progress", Priority: 1, UpdatedAt: "2026-04-28T11:00:00Z"})
		secondary := beadstore.NewFakeStore(protocol.Bead{ID: "claimed", Title: "claimed", Status: "open", Priority: 1, UpdatedAt: "2026-04-28T09:00:00Z"})
		var events []beadstore.ShadowDivergence
		store := beadstore.NewShadowStore(
			primary,
			secondary,
			beadstore.WithShadowStartedAt(mustParseTime(t, "2026-04-28T10:00:00Z")),
			beadstore.WithShadowDivergenceReporter(func(event beadstore.ShadowDivergence) {
				events = append(events, event)
			}),
		)

		ready, err := store.Ready(ctx)
		if err != nil {
			t.Fatalf("Ready: %v", err)
		}
		if len(ready) != 0 {
			t.Fatalf("Ready returned %#v, want primary filtered result", ready)
		}
		if len(events) != 1 {
			t.Fatalf("reported %d divergences, want 1: %#v", len(events), events)
		}
		if events[0].Kind != beadstore.ShadowDivergenceDrift {
			t.Fatalf("divergence = %#v, want drift for stale secondary ready row", events[0])
		}
	})

	t.Run("classifies stable secondary-only reads as real divergence", func(t *testing.T) {
		startedAt := mustParseTime(t, "2026-04-28T10:00:00Z")
		primary := []protocol.Bead{}
		secondary := []protocol.Bead{{ID: "missing-primary", Title: "stable", UpdatedAt: "2026-04-28T09:00:00Z"}}

		got := beadstore.ClassifyShadowDivergence(primary, secondary, startedAt)
		if got != beadstore.ShadowDivergenceReal {
			t.Fatalf("ClassifyShadowDivergence = %v, want %v", got, beadstore.ShadowDivergenceReal)
		}
	})

	t.Run("classifies secondary-only updates during shadow as allowed drift", func(t *testing.T) {
		startedAt := mustParseTime(t, "2026-04-28T10:00:00Z")
		primary := []protocol.Bead{}
		secondary := []protocol.Bead{{ID: "shadow-secondary", Title: "hot", UpdatedAt: "2026-04-28T11:00:00Z"}}

		got := beadstore.ClassifyShadowDivergence(primary, secondary, startedAt)
		if got != beadstore.ShadowDivergenceDrift {
			t.Fatalf("ClassifyShadowDivergence = %v, want %v", got, beadstore.ShadowDivergenceDrift)
		}
	})

	t.Run("classifies export mismatches with the read divergence classifier", func(t *testing.T) {
		ctx := context.Background()
		primary := beadstore.NewFakeStore(protocol.Bead{ID: "export-hot", Title: "primary", Status: "open", UpdatedAt: "2026-04-28T11:00:00Z"})
		secondary := beadstore.NewFakeStore(protocol.Bead{ID: "export-hot", Title: "secondary", Status: "open", UpdatedAt: "2026-04-28T09:00:00Z"})
		var events []beadstore.ShadowDivergence
		store := beadstore.NewShadowStore(
			primary,
			secondary,
			beadstore.WithShadowStartedAt(mustParseTime(t, "2026-04-28T10:00:00Z")),
			beadstore.WithShadowDivergenceReporter(func(event beadstore.ShadowDivergence) {
				events = append(events, event)
			}),
		)

		if _, err := store.Export(ctx); err != nil {
			t.Fatalf("Export: %v", err)
		}
		if len(events) != 1 {
			t.Fatalf("reported %d divergences, want 1: %#v", len(events), events)
		}
		if events[0].Operation != "Export" || events[0].Kind != beadstore.ShadowDivergenceDrift {
			t.Fatalf("divergence = %#v, want Export drift", events[0])
		}
	})

	t.Run("read errors are reported as real divergence and primary error wins", func(t *testing.T) {
		ctx := context.Background()
		primaryErr := errors.New("primary failed")
		secondary := &recordingStore{Store: beadstore.NewFakeStore(protocol.Bead{ID: "ready", Title: "ready", Status: "open"})}
		store := beadstore.NewShadowStore(
			errorStore{readyErr: primaryErr},
			secondary,
			beadstore.WithShadowDivergenceReporter(func(event beadstore.ShadowDivergence) {
				if event.Operation != "Ready" || event.Kind != beadstore.ShadowDivergenceReal {
					t.Fatalf("divergence = %#v, want real Ready error", event)
				}
			}),
		)

		got, err := store.Ready(ctx)
		if !errors.Is(err, primaryErr) {
			t.Fatalf("Ready error = %v, want primary error %v", err, primaryErr)
		}
		if got != nil {
			t.Fatalf("Ready returned %#v, want nil primary result", got)
		}
		if secondary.readyCalls != 1 {
			t.Fatalf("secondary Ready calls = %d, want 1", secondary.readyCalls)
		}
	})

	t.Run("secondary read errors are reported while primary result wins", func(t *testing.T) {
		ctx := context.Background()
		secondaryErr := errors.New("secondary failed")
		primary := beadstore.NewFakeStore(protocol.Bead{ID: "ready", Title: "ready", Status: "open"})
		var events []beadstore.ShadowDivergence
		store := beadstore.NewShadowStore(
			primary,
			errorStore{Store: beadstore.NewFakeStore(), readyErr: secondaryErr},
			beadstore.WithShadowDivergenceReporter(func(event beadstore.ShadowDivergence) {
				events = append(events, event)
			}),
		)

		got, err := store.Ready(ctx)
		if err != nil {
			t.Fatalf("Ready: %v", err)
		}
		if len(got) != 1 || got[0].ID != "ready" {
			t.Fatalf("Ready returned %#v, want primary result", got)
		}
		if len(events) != 1 {
			t.Fatalf("reported %d divergences, want 1: %#v", len(events), events)
		}
		if events[0].Operation != "Ready" || events[0].Kind != beadstore.ShadowDivergenceReal || events[0].Reason != "read error" {
			t.Fatalf("divergence = %#v, want real Ready read error", events[0])
		}
	})

	t.Run("writes go to primary only and write drift is not reported", func(t *testing.T) {
		ctx := context.Background()
		primary := &recordingStore{Store: beadstore.NewFakeStore()}
		secondary := &recordingStore{Store: beadstore.NewFakeStore()}
		var events []beadstore.ShadowDivergence
		store := beadstore.NewShadowStore(primary, secondary, beadstore.WithShadowDivergenceReporter(func(event beadstore.ShadowDivergence) {
			events = append(events, event)
		}))

		created, err := store.Create(ctx, beadstore.CreateParams{ID: "write", Title: "write", Type: "task", Priority: 1})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if created == nil || created.ID != "write" {
			t.Fatalf("Create returned %#v, want primary-created bead", created)
		}
		status := "in_progress"
		if err := store.Update(ctx, "write", beadstore.UpdateParams{Status: &status}); err != nil {
			t.Fatalf("Update: %v", err)
		}
		if err := store.Close(ctx, "write", "done"); err != nil {
			t.Fatalf("Close: %v", err)
		}

		if primary.createCalls != 1 || primary.updateCalls != 1 || primary.closeCalls != 1 {
			t.Fatalf("primary write calls create/update/close = %d/%d/%d, want 1/1/1", primary.createCalls, primary.updateCalls, primary.closeCalls)
		}
		if secondary.createCalls != 0 || secondary.updateCalls != 0 || secondary.closeCalls != 0 {
			t.Fatalf("secondary write calls create/update/close = %d/%d/%d, want 0/0/0", secondary.createCalls, secondary.updateCalls, secondary.closeCalls)
		}
		if len(events) != 0 {
			t.Fatalf("write drift reported divergences: %#v", events)
		}
		if secondaryBead, err := secondary.Show(ctx, "write"); err != nil {
			t.Fatalf("secondary Show: %v", err)
		} else if secondaryBead != nil {
			t.Fatalf("secondary was written to: %#v", secondaryBead)
		}
	})

	t.Run("defer operations go to primary only", func(t *testing.T) {
		ctx := context.Background()
		primary := beadstore.NewFakeStore(protocol.Bead{ID: "later", Title: "later", Status: "open"})
		secondary := beadstore.NewFakeStore(protocol.Bead{ID: "later", Title: "later", Status: "open"})
		store := beadstore.NewShadowStore(primary, secondary)

		if err := store.Defer(ctx, "later", "2026-04-28T15:00:00Z"); err != nil {
			t.Fatalf("Defer: %v", err)
		}
		primaryBead, err := primary.Show(ctx, "later")
		if err != nil {
			t.Fatalf("primary Show: %v", err)
		}
		if primaryBead.DeferUntil != "2026-04-28T15:00:00Z" {
			t.Fatalf("primary DeferUntil = %q, want deferred timestamp", primaryBead.DeferUntil)
		}
		secondaryBead, err := secondary.Show(ctx, "later")
		if err != nil {
			t.Fatalf("secondary Show: %v", err)
		}
		if secondaryBead.DeferUntil != "" {
			t.Fatalf("secondary DeferUntil = %q, want unchanged", secondaryBead.DeferUntil)
		}

		if err := store.Undefer(ctx, "later"); err != nil {
			t.Fatalf("Undefer: %v", err)
		}
		primaryBead, err = primary.Show(ctx, "later")
		if err != nil {
			t.Fatalf("primary Show after Undefer: %v", err)
		}
		if primaryBead.DeferUntil != "" {
			t.Fatalf("primary DeferUntil after Undefer = %q, want empty", primaryBead.DeferUntil)
		}
	})

	t.Run("defer operations require primary support", func(t *testing.T) {
		ctx := context.Background()
		store := beadstore.NewShadowStore(
			errorStore{Store: beadstore.NewFakeStore()},
			beadstore.NewFakeStore(),
		)

		if err := store.Defer(ctx, "later", "2026-04-28T15:00:00Z"); err == nil {
			t.Fatal("Defer with unsupported primary succeeded, want error")
		}
		if err := store.Undefer(ctx, "later"); err == nil {
			t.Fatal("Undefer with unsupported primary succeeded, want error")
		}
	})

	t.Run("journey operations delegate to primary", func(t *testing.T) {
		ctx := context.Background()
		parent := protocol.Bead{ID: "epic", Title: "epic", Type: "epic", Status: "open"}
		child := protocol.Bead{ID: "task", Title: "task", Epic: "epic", Status: "open"}
		primary := beadstore.NewFakeStore(parent, child)
		secondary := beadstore.NewFakeStore(parent)
		store := beadstore.NewShadowStore(primary, secondary)

		first := beadstore.JourneyEvent{
			Ts:    "2026-05-16T10:00:00Z",
			Actor: "worker",
			Event: "started",
		}
		second := beadstore.JourneyEvent{
			Ts:    "2026-05-16T10:01:00Z",
			Actor: "worker",
			Event: "finished",
		}
		if err := store.AppendJourney(ctx, "task", first); err != nil {
			t.Fatalf("AppendJourney first: %v", err)
		}
		if err := store.AppendJourney(ctx, "task", second); err != nil {
			t.Fatalf("AppendJourney second: %v", err)
		}

		events, err := store.Journey(ctx, "task", time.Date(2026, 5, 16, 10, 1, 0, 0, time.UTC))
		if err != nil {
			t.Fatalf("Journey: %v", err)
		}
		if len(events) != 1 || events[0].Event != "finished" {
			t.Fatalf("Journey returned %#v, want only finished event from primary", events)
		}

		latest, err := store.LatestJourney(ctx, "task", 2)
		if err != nil {
			t.Fatalf("LatestJourney: %v", err)
		}
		if len(latest) != 2 || latest[0].Event != "started" || latest[1].Event != "finished" {
			t.Fatalf("LatestJourney returned %#v, want primary events in order", latest)
		}

		if err := store.TransitionPipelineStage(ctx, "task", beadstore.PipelineStage(""), beadstore.StageAssess); err != nil {
			t.Fatalf("TransitionPipelineStage: %v", err)
		}
		latest, err = store.LatestJourney(ctx, "task", 1)
		if err != nil {
			t.Fatalf("LatestJourney after transition: %v", err)
		}
		if len(latest) != 1 || latest[0].Event != "pipeline_stage_changed" {
			t.Fatalf("latest after transition = %#v, want pipeline_stage_changed event", latest)
		}

		childCount, err := store.CountChildren(ctx, "epic")
		if err != nil {
			t.Fatalf("CountChildren: %v", err)
		}
		if childCount != 1 {
			t.Fatalf("CountChildren = %d, want primary child count 1", childCount)
		}

		secondaryEvents, err := secondary.LatestJourney(ctx, "task", 10)
		if err != nil {
			t.Fatalf("secondary LatestJourney: %v", err)
		}
		if len(secondaryEvents) != 0 {
			t.Fatalf("secondary events = %#v, want no journey writes", secondaryEvents)
		}
	})

	t.Run("WithReadTx passes closure errors through unwrapped", func(t *testing.T) {
		ctx := context.Background()
		store := beadstore.NewShadowStore(beadstore.NewFakeStore(), beadstore.NewFakeStore())

		closureErr := errors.New("LatestJourney(bead-X): boom")
		err := store.WithReadTx(ctx, func(_ beadstore.ReadTx) error {
			return closureErr
		})
		if !errors.Is(err, closureErr) {
			t.Fatalf("WithReadTx error = %v, want closure error %v", err, closureErr)
		}
		// The error should NOT be re-framed as "shadow primary with read tx: ...".
		if msg := err.Error(); strings.Contains(msg, "shadow primary") {
			t.Errorf("closure error was wrapped as primary failure: %q", msg)
		}
	})
}

func TestShadowStoreShowReturnsPrimaryWhenSecondaryMemoryDiffers(t *testing.T) {
	ctx := context.Background()
	updatedAt := "2026-04-28T09:00:00Z"
	primary := beadstore.NewFakeStore(protocol.Bead{
		ID:        "shown",
		Title:     "primary",
		Status:    "open",
		Memory:    "primary memory",
		UpdatedAt: updatedAt,
	})
	secondary := beadstore.NewFakeStore(protocol.Bead{
		ID:        "shown",
		Title:     "secondary",
		Status:    "open",
		Memory:    "secondary memory",
		UpdatedAt: updatedAt,
	})
	var events []beadstore.ShadowDivergence
	store := beadstore.NewShadowStore(
		primary,
		secondary,
		beadstore.WithShadowStartedAt(mustParseTime(t, "2026-04-28T10:00:00Z")),
		beadstore.WithShadowDivergenceReporter(func(event beadstore.ShadowDivergence) {
			events = append(events, event)
		}),
	)

	shown, err := store.Show(ctx, "shown")
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if shown == nil {
		t.Fatal("Show returned nil, want primary bead")
	}
	if shown.Title != "primary" {
		t.Fatalf("Show Title = %q, want primary", shown.Title)
	}
	if shown.Memory != "primary memory" {
		t.Fatalf("Show Memory = %q, want primary memory unchanged", shown.Memory)
	}
	if len(events) != 1 {
		t.Fatalf("reported %d divergences, want 1: %#v", len(events), events)
	}
	if events[0].Operation != "Show" || events[0].Kind != beadstore.ShadowDivergenceReal {
		t.Fatalf("divergence = %#v, want real Show divergence", events[0])
	}
}

type recordingStore struct {
	beadstore.Store

	readyCalls              int
	inProgressCalls         int
	blockedCalls            int
	closedCalls             int
	showCalls               int
	createCalls             int
	updateCalls             int
	closeCalls              int
	hasChildrenCalls        int
	allChildrenClosedCalls  int
	findByParentAndTagCalls int
	findByMetadataKeyCalls  int
	exportCalls             int
}

func (s *recordingStore) Ready(ctx context.Context) ([]protocol.Bead, error) {
	s.readyCalls++
	return s.Store.Ready(ctx)
}

func (s *recordingStore) InProgress(ctx context.Context) ([]protocol.Bead, error) {
	s.inProgressCalls++
	return s.Store.InProgress(ctx)
}

func (s *recordingStore) Blocked(ctx context.Context) ([]protocol.Bead, error) {
	s.blockedCalls++
	return s.Store.Blocked(ctx)
}

func (s *recordingStore) Closed(ctx context.Context, limit int) ([]protocol.Bead, error) {
	s.closedCalls++
	return s.Store.Closed(ctx, limit)
}

func (s *recordingStore) Show(ctx context.Context, id string) (*protocol.Bead, error) {
	s.showCalls++
	return s.Store.Show(ctx, id)
}

func (s *recordingStore) Create(ctx context.Context, params beadstore.CreateParams) (*protocol.Bead, error) {
	s.createCalls++
	return s.Store.Create(ctx, params)
}

func (s *recordingStore) Update(ctx context.Context, id string, params beadstore.UpdateParams) error {
	s.updateCalls++
	return s.Store.Update(ctx, id, params)
}

func (s *recordingStore) Close(ctx context.Context, id, reason string) error {
	s.closeCalls++
	return s.Store.Close(ctx, id, reason)
}

func (s *recordingStore) HasChildren(ctx context.Context, epicID string) (bool, error) {
	s.hasChildrenCalls++
	return s.Store.HasChildren(ctx, epicID)
}

func (s *recordingStore) AllChildrenClosed(ctx context.Context, epicID string) (bool, error) {
	s.allChildrenClosedCalls++
	return s.Store.AllChildrenClosed(ctx, epicID)
}

func (s *recordingStore) FindByParentAndTag(ctx context.Context, parentID, tag string) ([]protocol.Bead, error) {
	s.findByParentAndTagCalls++
	return s.Store.FindByParentAndTag(ctx, parentID, tag)
}

func (s *recordingStore) FindByMetadataKey(ctx context.Context, key string) ([]*protocol.Bead, error) {
	s.findByMetadataKeyCalls++
	return s.Store.FindByMetadataKey(ctx, key)
}

func (s *recordingStore) Export(ctx context.Context) ([]byte, error) {
	s.exportCalls++
	return s.Store.Export(ctx)
}

type readCallCounts struct {
	ready              int
	inProgress         int
	blocked            int
	closed             int
	show               int
	hasChildren        int
	allChildrenClosed  int
	findByParentAndTag int
	findByMetadataKey  int
	export             int
}

func (s *recordingStore) readCalls() readCallCounts {
	return readCallCounts{
		ready:              s.readyCalls,
		inProgress:         s.inProgressCalls,
		blocked:            s.blockedCalls,
		closed:             s.closedCalls,
		show:               s.showCalls,
		hasChildren:        s.hasChildrenCalls,
		allChildrenClosed:  s.allChildrenClosedCalls,
		findByParentAndTag: s.findByParentAndTagCalls,
		findByMetadataKey:  s.findByMetadataKeyCalls,
		export:             s.exportCalls,
	}
}

func mustParseTime(t *testing.T, raw string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		t.Fatalf("parse time %q: %v", raw, err)
	}
	return parsed
}

func TestShadowDivergenceClassifiesUpdatedPrimaryAsDrift(t *testing.T) {
	startedAt := mustParseTime(t, "2026-04-28T10:00:00Z")
	primary := []protocol.Bead{{ID: "hot", Title: "new", UpdatedAt: "2026-04-28T10:01:00Z"}}
	secondary := []protocol.Bead{{ID: "hot", Title: "old", UpdatedAt: "2026-04-28T09:59:00Z"}}

	got := beadstore.ClassifyShadowDivergence(primary, secondary, startedAt)
	want := beadstore.ShadowDivergenceDrift
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ClassifyShadowDivergence = %v, want %v", got, want)
	}
}

func TestShadowDivergenceRequiresPrimaryNewerTimestampForDrift(t *testing.T) {
	startedAt := mustParseTime(t, "2026-04-28T10:00:00Z")
	for _, tc := range []struct {
		name      string
		primary   string
		secondary string
		want      beadstore.ShadowDivergenceKind
	}{
		{
			name:      "same instant different formatting is real",
			primary:   "2026-04-28T10:01:00.000Z",
			secondary: "2026-04-28T10:01:00Z",
			want:      beadstore.ShadowDivergenceReal,
		},
		{
			name:      "secondary newer is real",
			primary:   "2026-04-28T10:01:00Z",
			secondary: "2026-04-28T10:02:00Z",
			want:      beadstore.ShadowDivergenceReal,
		},
		{
			name:      "primary newer during shadow is drift",
			primary:   "2026-04-28T10:02:00.123456Z",
			secondary: "2026-04-28T10:01:00Z",
			want:      beadstore.ShadowDivergenceDrift,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			primary := []protocol.Bead{{ID: "hot", Title: "primary", UpdatedAt: tc.primary}}
			secondary := []protocol.Bead{{ID: "hot", Title: "secondary", UpdatedAt: tc.secondary}}
			got := beadstore.ClassifyShadowDivergence(primary, secondary, startedAt)
			if got != tc.want {
				t.Fatalf("ClassifyShadowDivergence = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestShadowAggregateDivergence(t *testing.T) {
	t.Run("classifies aggregate mismatches from child updates during shadow as drift", func(t *testing.T) {
		ctx := context.Background()
		allClosedPrimary := beadstore.NewFakeStore(
			protocol.Bead{ID: "epic", Title: "primary epic", Status: "open", UpdatedAt: "2026-04-28T11:00:00Z"},
			protocol.Bead{ID: "child", Title: "closed child", Status: "closed", Epic: "epic", UpdatedAt: "2026-04-28T11:00:00Z"},
		)
		allClosedSecondary := beadstore.NewFakeStore(
			protocol.Bead{ID: "epic", Title: "secondary epic", Status: "open", UpdatedAt: "2026-04-28T09:00:00Z"},
			protocol.Bead{ID: "child", Title: "open child", Status: "open", Epic: "epic", UpdatedAt: "2026-04-28T09:00:00Z"},
		)
		var events []beadstore.ShadowDivergence
		allClosedStore := beadstore.NewShadowStore(
			allClosedPrimary,
			allClosedSecondary,
			beadstore.WithShadowStartedAt(mustParseTime(t, "2026-04-28T10:00:00Z")),
			beadstore.WithShadowDivergenceReporter(func(event beadstore.ShadowDivergence) {
				events = append(events, event)
			}),
		)

		got, err := allClosedStore.AllChildrenClosed(ctx, "epic")
		if err != nil {
			t.Fatalf("AllChildrenClosed: %v", err)
		}
		if !got {
			t.Fatalf("AllChildrenClosed returned false, want primary true")
		}
		if len(events) != 1 {
			t.Fatalf("reported %d divergences, want 1: %#v", len(events), events)
		}
		if events[0].Operation != "AllChildrenClosed" || events[0].Kind != beadstore.ShadowDivergenceDrift {
			t.Fatalf("divergence = %#v, want AllChildrenClosed drift", events[0])
		}

		hasChildrenPrimary := beadstore.NewFakeStore(
			protocol.Bead{ID: "epic", Title: "primary epic", Status: "open", UpdatedAt: "2026-04-28T09:00:00Z"},
			protocol.Bead{ID: "child", Title: "moved child", Status: "open", Epic: "other", UpdatedAt: "2026-04-28T11:00:00Z"},
		)
		hasChildrenSecondary := beadstore.NewFakeStore(
			protocol.Bead{ID: "epic", Title: "secondary epic", Status: "open", UpdatedAt: "2026-04-28T09:00:00Z"},
			protocol.Bead{ID: "child", Title: "open child", Status: "open", Epic: "epic", UpdatedAt: "2026-04-28T09:00:00Z"},
		)
		events = nil
		hasChildrenStore := beadstore.NewShadowStore(
			hasChildrenPrimary,
			hasChildrenSecondary,
			beadstore.WithShadowStartedAt(mustParseTime(t, "2026-04-28T10:00:00Z")),
			beadstore.WithShadowDivergenceReporter(func(event beadstore.ShadowDivergence) {
				events = append(events, event)
			}),
		)

		hasChildren, err := hasChildrenStore.HasChildren(ctx, "epic")
		if err != nil {
			t.Fatalf("HasChildren: %v", err)
		}
		if hasChildren {
			t.Fatalf("HasChildren returned true, want primary false")
		}
		if len(events) != 1 {
			t.Fatalf("reported %d divergences, want 1: %#v", len(events), events)
		}
		if events[0].Operation != "HasChildren" || events[0].Kind != beadstore.ShadowDivergenceDrift {
			t.Fatalf("divergence = %#v, want HasChildren drift", events[0])
		}
	})

	t.Run("decodes cli parent_id export shape for aggregate classification", func(t *testing.T) {
		ctx := context.Background()
		primary := beadstore.NewFakeStore(
			protocol.Bead{ID: "epic", Title: "primary epic", Status: "open", UpdatedAt: "2026-04-28T09:00:00Z"},
			protocol.Bead{ID: "child", Title: "moved child", Status: "open", Epic: "other", UpdatedAt: "2026-04-28T11:00:00Z"},
		)
		secondary := exportOverrideStore{
			Store: beadstore.NewFakeStore(
				protocol.Bead{ID: "epic", Title: "secondary epic", Status: "open", UpdatedAt: "2026-04-28T09:00:00Z"},
				protocol.Bead{ID: "child", Title: "open child", Status: "open", Epic: "epic", UpdatedAt: "2026-04-28T09:00:00Z"},
			),
			export: []byte(`{"id":"epic","title":"secondary epic","status":"open","updated_at":"2026-04-28T09:00:00Z"}` + "\n" +
				`{"id":"child","title":"open child","status":"open","parent_id":"epic","updated_at":"2026-04-28T09:00:00Z"}` + "\n"),
		}
		var events []beadstore.ShadowDivergence
		store := beadstore.NewShadowStore(
			primary,
			secondary,
			beadstore.WithShadowStartedAt(mustParseTime(t, "2026-04-28T10:00:00Z")),
			beadstore.WithShadowDivergenceReporter(func(event beadstore.ShadowDivergence) {
				events = append(events, event)
			}),
		)

		got, err := store.HasChildren(ctx, "epic")
		if err != nil {
			t.Fatalf("HasChildren: %v", err)
		}
		if got {
			t.Fatalf("HasChildren returned true, want primary false")
		}
		if len(events) != 1 {
			t.Fatalf("reported %d divergences, want 1: %#v", len(events), events)
		}
		if events[0].Operation != "HasChildren" || events[0].Kind != beadstore.ShadowDivergenceDrift {
			t.Fatalf("divergence = %#v, want HasChildren drift", events[0])
		}
	})

	t.Run("parent timestamp alone does not make aggregate mismatch drift", func(t *testing.T) {
		ctx := context.Background()
		primary := beadstore.NewFakeStore(protocol.Bead{ID: "epic", Title: "primary epic", Status: "open", UpdatedAt: "2026-04-28T11:00:00Z"})
		secondary := beadstore.NewFakeStore(
			protocol.Bead{ID: "epic", Title: "secondary epic", Status: "open", UpdatedAt: "2026-04-28T09:00:00Z"},
			protocol.Bead{ID: "child", Title: "stale child", Status: "open", Epic: "epic", UpdatedAt: "2026-04-28T09:00:00Z"},
		)
		var events []beadstore.ShadowDivergence
		store := beadstore.NewShadowStore(
			primary,
			secondary,
			beadstore.WithShadowStartedAt(mustParseTime(t, "2026-04-28T10:00:00Z")),
			beadstore.WithShadowDivergenceReporter(func(event beadstore.ShadowDivergence) {
				events = append(events, event)
			}),
		)

		got, err := store.HasChildren(ctx, "epic")
		if err != nil {
			t.Fatalf("HasChildren: %v", err)
		}
		if got {
			t.Fatalf("HasChildren returned true, want primary false")
		}
		if len(events) != 1 {
			t.Fatalf("reported %d divergences, want 1: %#v", len(events), events)
		}
		if events[0].Operation != "HasChildren" || events[0].Kind != beadstore.ShadowDivergenceReal {
			t.Fatalf("divergence = %#v, want HasChildren real divergence", events[0])
		}
	})

	t.Run("classifies stable aggregate mismatches as real", func(t *testing.T) {
		ctx := context.Background()
		primary := beadstore.NewFakeStore(protocol.Bead{ID: "epic", Title: "primary epic", Status: "open", UpdatedAt: "2026-04-28T09:00:00Z"})
		secondary := beadstore.NewFakeStore(
			protocol.Bead{ID: "epic", Title: "secondary epic", Status: "open", UpdatedAt: "2026-04-28T09:00:00Z"},
			protocol.Bead{ID: "child", Title: "stale child", Status: "open", Epic: "epic", UpdatedAt: "2026-04-28T09:00:00Z"},
		)
		var events []beadstore.ShadowDivergence
		store := beadstore.NewShadowStore(
			primary,
			secondary,
			beadstore.WithShadowStartedAt(mustParseTime(t, "2026-04-28T10:00:00Z")),
			beadstore.WithShadowDivergenceReporter(func(event beadstore.ShadowDivergence) {
				events = append(events, event)
			}),
		)

		got, err := store.HasChildren(ctx, "epic")
		if err != nil {
			t.Fatalf("HasChildren: %v", err)
		}
		if got {
			t.Fatalf("HasChildren returned true, want primary false")
		}
		if len(events) != 1 {
			t.Fatalf("reported %d divergences, want 1: %#v", len(events), events)
		}
		if events[0].Operation != "HasChildren" || events[0].Kind != beadstore.ShadowDivergenceReal {
			t.Fatalf("divergence = %#v, want HasChildren real divergence", events[0])
		}
	})

	t.Run("aggregate read errors remain real and primary error wins", func(t *testing.T) {
		ctx := context.Background()
		primaryErr := errors.New("primary aggregate failed")
		secondary := beadstore.NewFakeStore(protocol.Bead{ID: "epic", Title: "epic", Status: "open"})
		var events []beadstore.ShadowDivergence
		store := beadstore.NewShadowStore(
			errorStore{hasChildrenErr: primaryErr},
			secondary,
			beadstore.WithShadowDivergenceReporter(func(event beadstore.ShadowDivergence) {
				events = append(events, event)
			}),
		)

		got, err := store.HasChildren(ctx, "epic")
		if !errors.Is(err, primaryErr) {
			t.Fatalf("HasChildren error = %v, want primary error %v", err, primaryErr)
		}
		if got {
			t.Fatalf("HasChildren returned true, want primary false")
		}
		if len(events) != 1 {
			t.Fatalf("reported %d divergences, want 1: %#v", len(events), events)
		}
		if events[0].Operation != "HasChildren" || events[0].Kind != beadstore.ShadowDivergenceReal {
			t.Fatalf("divergence = %#v, want HasChildren real read error", events[0])
		}
	})
}

type errorStore struct {
	beadstore.Store
	readyErr       error
	hasChildrenErr error
}

func (s errorStore) Ready(ctx context.Context) ([]protocol.Bead, error) {
	return nil, s.readyErr
}

func (s errorStore) HasChildren(ctx context.Context, epicID string) (bool, error) {
	return false, s.hasChildrenErr
}

type exportOverrideStore struct {
	beadstore.Store
	export    []byte
	exportErr error
}

func (s exportOverrideStore) Export(ctx context.Context) ([]byte, error) {
	return s.export, s.exportErr
}
