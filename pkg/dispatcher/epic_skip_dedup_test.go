package dispatcher //nolint:testpackage // white-box: asserts processEpicSkip dedups its log event

import (
	"context"
	"testing"

	"oro/pkg/protocol"
)

// TestProcessEpicSkipDedupsEvent regression-tests oro-cn6a.
//
// The assign loop runs every few seconds and every pass calls processEpicSkip
// for any epic still sitting in the ready queue. The pre-fix code logged
// non_executable_issue_type unconditionally on every call, producing one
// noise event per epic per assign tick. With ~5 epics in the queue, that's
// thousands of identical events per hour, drowning out signal events
// (qg_failed, escalated, etc) when querying `oro events`.
//
// Fix: per-epic dedup so the event fires once per dispatcher lifetime per
// epic. The auto-close branch (HasChildren / AllChildrenClosed) still runs
// every tick — only the log is deduped. The companion test
// TestCheckBeadReady_SkipsOversizedCheckForEpicType only asserts count > 0,
// so dedup preserves its semantics.
func TestProcessEpicSkipDedupsEvent(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	if _, err := d.db.ExecContext(ctx, protocol.SchemaDDL); err != nil {
		t.Fatalf("init schema: %v", err)
	}

	beadID := "bead-epic-dedup"
	beadSrc.mu.Lock()
	beadSrc.hasChildrenMap = map[string]bool{beadID: false}
	beadSrc.shown = map[string]*protocol.BeadDetail{
		beadID: {ID: beadID, Type: "epic"},
	}
	beadSrc.mu.Unlock()

	bead := protocol.Bead{ID: beadID, Type: "epic"}

	// Three identical calls — only the first should produce an event.
	d.processEpicSkip(ctx, bead)
	d.processEpicSkip(ctx, bead)
	d.processEpicSkip(ctx, bead)

	var count int
	if err := d.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM events WHERE type = ? AND bead_id = ?`,
		"non_executable_issue_type", beadID).Scan(&count); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 non_executable_issue_type event after 3 calls (dedup), got %d", count)
	}
}
