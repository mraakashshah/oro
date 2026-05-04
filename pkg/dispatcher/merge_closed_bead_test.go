package dispatcher //nolint:testpackage // white-box: asserts mergeAndComplete short-circuits before merger.Merge

import (
	"context"
	"testing"

	"oro/pkg/protocol"
)

// TestMergeAndCompleteAbortsOnClosedBead asserts that when a bead is already
// closed (e.g. by manager dedup) before the worker's review completes,
// mergeAndComplete must NOT call merger.Merge — otherwise an orphan commit can
// land on the target branch on top of an already-resolved bead.
//
// Regression: oro-jev9 — live observation 2026-05-04. Manager closed oro-0yse
// as duplicate of oro-mdrh while a worker was mid-review. Dispatcher proceeded
// to merge the worker's commit (8cc25431) to main on the already-closed bead.
func TestMergeAndCompleteAbortsOnClosedBead(t *testing.T) {
	d, beadSrc, _, _, gitRunner, _ := newTestDispatcher(t)
	ctx := context.Background()

	if _, err := d.db.ExecContext(ctx, protocol.SchemaDDL); err != nil {
		t.Fatalf("init schema: %v", err)
	}

	const beadID = "bead-already-closed"

	beadSrc.mu.Lock()
	beadSrc.shown = map[string]*protocol.BeadDetail{
		beadID: {ID: beadID, Status: "closed"},
	}
	beadSrc.mu.Unlock()

	d.mergeAndComplete(ctx, beadID, "w-x", "/tmp/wt-"+beadID, "agent/"+beadID, "", "", 0)

	if calls := gitRunner.RebaseCalls(); len(calls) != 0 {
		t.Fatalf("expected no rebase calls when bead is already closed, got %d: %v", len(calls), calls)
	}

	beadSrc.mu.Lock()
	closedCalls := append([]string(nil), beadSrc.closed...)
	beadSrc.mu.Unlock()
	for _, id := range closedCalls {
		if id == beadID {
			t.Fatalf("dispatcher must not re-close already-closed bead %q (would emit \"Merged: <sha>\" reason masking the dedup close)", beadID)
		}
	}
}
