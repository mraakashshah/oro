package dispatcher //nolint:testpackage // white-box: exercises the post-claim scheduling cursor

import (
	"testing"

	"oro/pkg/protocol"
)

func TestAdvanceAssignedGeneralIdleConsumesReportedClaimAfterAsyncRelease(t *testing.T) {
	const beadID = "oro-released-after-claim"
	d := &Dispatcher{priorityBeads: map[string]bool{beadID: true}}
	worker := &trackedWorker{state: protocol.WorkerIdle}
	idle := []idleWorker{{worker: worker}}

	// launchAssignment already reported claimed=true, but async admission setup
	// released the worker before the scheduling loop advanced its local cursor.
	claimed, nextIdleIdx := d.advanceAssignedGeneralIdle(idle, 0, beadID, map[string]bool{beadID: true})
	if worker.state != protocol.WorkerIdle {
		t.Fatalf("released worker state = %q, want Idle test precondition", worker.state)
	}
	if !claimed {
		t.Fatal("reported claim was lost after async setup released worker state")
	}
	if nextIdleIdx != 1 {
		t.Fatalf("next idle index = %d, want 1 after a reported claim", nextIdleIdx)
	}
	if d.priorityBeads[beadID] {
		t.Fatalf("priority bead %q was not cleared after its reported claim", beadID)
	}
}
