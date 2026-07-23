//nolint:testpackage // White-box coverage verifies durable reconciliation state.
package storage

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestLegacyReconcileReadIsActuallyBounded(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	catalog, err := OpenCatalog(ctx, filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	t.Cleanup(func() { _ = catalog.Close() })

	entries := make([]LegacyEntry, 100_000)
	for index := range entries {
		entries[index] = LegacyEntry{Name: fmt.Sprintf("candidate-%06d", index)}
	}
	source := &recordingLegacyEntrySource{entries: entries}
	reconciler := NewLegacyReconciler(catalog, t.TempDir(), source)

	first, err := reconciler.Reconcile(ctx)
	if err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	if got, want := first.Examined, legacyReconcileEntryLimit; got != want {
		t.Fatalf("first pass examined = %d, want %d", got, want)
	}
	if first.Complete {
		t.Fatal("first pass marked a source with remaining entries complete")
	}
	if got, want := source.delivered, legacyReconcileEntryLimit+1; got != want {
		t.Fatalf("first pass source entries delivered = %d, want %d", got, want)
	}
	if got, want := source.calls, []legacyReadCall{{after: "", limit: legacyReconcileEntryLimit}, {after: first.Cursor, limit: 1}}; !equalLegacyReadCalls(got, want) {
		t.Fatalf("first pass ReadPage calls = %#v, want %#v", got, want)
	}
	if got := storedReconciliationCursor(ctx, t, catalog, reconciler.cursorName()); got != first.Cursor {
		t.Fatalf("stored cursor = %q, want %q", got, first.Cursor)
	}

	second, err := reconciler.Reconcile(ctx)
	if err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if got, want := second.Examined, legacyReconcileEntryLimit; got != want {
		t.Fatalf("second pass examined = %d, want %d", got, want)
	}
	if got, want := source.calls[2], (legacyReadCall{after: first.Cursor, limit: legacyReconcileEntryLimit}); got != want {
		t.Fatalf("second pass did not resume strictly after cursor: got %#v, want %#v", got, want)
	}

	exactEntries := entries[:legacyReconcileEntryLimit]
	exactSource := &recordingLegacyEntrySource{entries: exactEntries}
	exact := NewLegacyReconciler(catalog, t.TempDir(), exactSource)
	result, err := exact.Reconcile(ctx)
	if err != nil {
		t.Fatalf("exact-size reconcile: %v", err)
	}
	if !result.Complete || result.Cursor != "" {
		t.Fatalf("exact-size result = %+v, want completed result with an empty cursor", result)
	}
	if got, want := exactSource.calls, []legacyReadCall{{after: "", limit: legacyReconcileEntryLimit}, {after: exactEntries[len(exactEntries)-1].Name, limit: 1}}; !equalLegacyReadCalls(got, want) {
		t.Fatalf("exact-size ReadPage calls = %#v, want %#v", got, want)
	}

	failedSource := &recordingLegacyEntrySource{err: errors.New("source unavailable")}
	failed := NewLegacyReconciler(catalog, t.TempDir(), failedSource)
	if err := catalog.SaveReconciliationCursor(ctx, ReconciliationCursor{Name: failed.cursorName(), Cursor: "durable-cursor", UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("seed failed-source cursor: %v", err)
	}
	if _, err := failed.Reconcile(ctx); err == nil {
		t.Fatal("reconcile with source error = nil")
	}
	if got := storedReconciliationCursor(ctx, t, catalog, failed.cursorName()); got != "durable-cursor" {
		t.Fatalf("source error changed durable cursor to %q", got)
	}
}

type legacyReadCall struct {
	after string
	limit int
}

type recordingLegacyEntrySource struct {
	entries   []LegacyEntry
	err       error
	calls     []legacyReadCall
	delivered int
}

func (s *recordingLegacyEntrySource) ReadPage(_ context.Context, _ string, after string, limit int) (LegacyEntryPage, error) {
	s.calls = append(s.calls, legacyReadCall{after: after, limit: limit})
	if s.err != nil {
		return LegacyEntryPage{}, s.err
	}
	start := 0
	for start < len(s.entries) && s.entries[start].Name <= after {
		start++
	}
	end := start + limit
	if end > len(s.entries) {
		end = len(s.entries)
	}
	page := LegacyEntryPage{Entries: s.entries[start:end]}
	s.delivered += len(page.Entries)
	return page, nil
}

func storedReconciliationCursor(ctx context.Context, t *testing.T, catalog *Catalog, name string) string {
	t.Helper()
	cursor, err := catalog.ReconciliationCursor(ctx, name)
	if err != nil {
		t.Fatalf("load reconciliation cursor: %v", err)
	}
	return cursor.Cursor
}

func equalLegacyReadCalls(got, want []legacyReadCall) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}
