package dispatcher //nolint:testpackage // needs access to internal test helpers

import (
	"strings"
	"testing"
	"time"

	"oro/pkg/protocol"
)

// TestHandoffContinuationInheritsAC verifies that when a handoff exhaustion
// creates a continuation bead, it inherits the parent bead's acceptance criteria.
func TestHandoffContinuationInheritsAC(t *testing.T) {
	d, conn, _, spawnMock := setupHandoffDiagnosis(t)

	// Set diagnosis output so the diagnosis goroutine completes.
	spawnMock.mu.Lock()
	spawnMock.verdict = "Root cause: context limit exceeded"
	spawnMock.mu.Unlock()

	// Setup parent bead with acceptance criteria in mockBeadSource.
	beadSrc, ok := d.beads.(*mockBeadSource)
	if !ok {
		t.Fatal("beads is not *mockBeadSource")
	}
	beadSrc.mu.Lock()
	if beadSrc.shown == nil {
		beadSrc.shown = make(map[string]*protocol.BeadDetail)
	}
	beadSrc.shown["bead-parent"] = &protocol.BeadDetail{
		Title:              "Implement feature X",
		AcceptanceCriteria: "- [ ] Test: TestFeatureX passes\n- [ ] Feature X is documented",
	}
	beadSrc.mu.Unlock()

	// Pre-set handoff count to 1 (simulating first handoff already happened).
	d.mu.Lock()
	d.handoffCounts["bead-parent"] = 1
	d.mu.Unlock()

	// Second handoff — triggers diagnosis AND should create continuation bead.
	sendMsg(t, conn, protocol.Message{
		Type: protocol.MsgHandoff,
		Handoff: &protocol.HandoffPayload{
			BeadID:         "bead-parent",
			WorkerID:       "w1",
			ContextSummary: "Implemented 2 of 3 parts; remaining: final integration",
		},
	})

	// Consume SHUTDOWN message sent to the old worker.
	msg, msgOk := readMsg(t, conn, 2*time.Second)
	if !msgOk {
		t.Fatal("expected SHUTDOWN after handoff exhaustion")
	}
	if msg.Type != protocol.MsgShutdown {
		t.Fatalf("expected SHUTDOWN, got %s", msg.Type)
	}

	// Wait for BeadSource.Create to be called with continuation bead that inherits AC.
	waitFor(t, func() bool {
		beadSrc.mu.Lock()
		calls := make([]createCall, len(beadSrc.created))
		copy(calls, beadSrc.created)
		beadSrc.mu.Unlock()

		for _, c := range calls {
			if c.parent == "bead-parent" && c.beadType == "task" {
				return eventCount(t, d.db, "continuation_bead_created") > 0
			}
		}
		return false
	}, 3*time.Second)

	// Verify the continuation bead details.
	beadSrc.mu.Lock()
	calls := make([]createCall, len(beadSrc.created))
	copy(calls, beadSrc.created)
	beadSrc.mu.Unlock()

	for _, c := range calls {
		if c.parent == "bead-parent" && c.beadType == "task" {
			expectedAC := "- [ ] Test: TestFeatureX passes\n- [ ] Feature X is documented"
			if c.acceptanceCriteria != expectedAC {
				t.Fatalf("continuation bead AC = %q; want %q", c.acceptanceCriteria, expectedAC)
			}
			if !strings.Contains(c.description, "Implement feature X") {
				t.Fatalf("continuation bead description should include parent title; got %q", c.description)
			}
			return // SUCCESS
		}
	}
	t.Fatal("expected continuation bead with inherited AC")
}

// TestHandoffContinuation_NoAC verifies graceful degradation when parent has no AC.
func TestHandoffContinuation_NoAC(t *testing.T) {
	d, conn, _, spawnMock := setupHandoffDiagnosis(t)

	// Set diagnosis output.
	spawnMock.mu.Lock()
	spawnMock.verdict = "Root cause: context limit"
	spawnMock.mu.Unlock()

	// Setup parent bead WITHOUT acceptance criteria.
	beadSrc, ok := d.beads.(*mockBeadSource)
	if !ok {
		t.Fatal("beads is not *mockBeadSource")
	}
	beadSrc.mu.Lock()
	if beadSrc.shown == nil {
		beadSrc.shown = make(map[string]*protocol.BeadDetail)
	}
	beadSrc.shown["bead-noac"] = &protocol.BeadDetail{
		Title:              "Some task",
		AcceptanceCriteria: "", // No AC
	}
	beadSrc.mu.Unlock()

	// Pre-set handoff count to 1.
	d.mu.Lock()
	d.handoffCounts["bead-noac"] = 1
	d.mu.Unlock()

	// Trigger handoff exhaustion.
	sendMsg(t, conn, protocol.Message{
		Type: protocol.MsgHandoff,
		Handoff: &protocol.HandoffPayload{
			BeadID:         "bead-noac",
			WorkerID:       "w1",
			ContextSummary: "Some progress",
		},
	})

	// Consume SHUTDOWN.
	msg, msgOk := readMsg(t, conn, 2*time.Second)
	if !msgOk {
		t.Fatal("expected SHUTDOWN")
	}
	if msg.Type != protocol.MsgShutdown {
		t.Fatalf("expected SHUTDOWN, got %s", msg.Type)
	}

	// Wait for continuation bead creation (should still work with empty AC).
	waitFor(t, func() bool {
		beadSrc.mu.Lock()
		calls := make([]createCall, len(beadSrc.created))
		copy(calls, beadSrc.created)
		beadSrc.mu.Unlock()

		for _, c := range calls {
			if c.parent == "bead-noac" && c.beadType == "task" {
				return true
			}
		}
		return false
	}, 3*time.Second)

	// Verify the continuation bead details.
	beadSrc.mu.Lock()
	calls := make([]createCall, len(beadSrc.created))
	copy(calls, beadSrc.created)
	beadSrc.mu.Unlock()

	for _, c := range calls {
		if c.parent == "bead-noac" && c.beadType == "task" {
			if c.acceptanceCriteria != "" {
				t.Fatalf("continuation bead AC should be empty; got %q", c.acceptanceCriteria)
			}
			if !strings.Contains(c.description, "Some task") {
				t.Fatalf("continuation bead description should include parent title; got %q", c.description)
			}
			return // SUCCESS
		}
	}
	t.Fatal("expected continuation bead to be created even without parent AC")
}
