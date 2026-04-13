package dispatcher //nolint:testpackage // internal white-box tests need access to unexported fields

import (
	"context"
	"testing"
	"time"

	"oro/pkg/protocol"
)

// TestTryAssignPausedDuringDoltRecovery verifies that tryAssign exits immediately
// (without calling beads.Ready) when doltRecovering is true.
func TestTryAssignPausedDuringDoltRecovery(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	startDispatcher(t, d)

	// Connect an idle worker.
	conn, _ := connectWorker(t, d.cfg.SocketPath)
	sendMsg(t, conn, protocol.Message{
		Type:      protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w1", ContextPct: 5},
	})
	waitForWorkers(t, d, 1, 1*time.Second)

	// Start the dispatcher so GetState() == StateRunning.
	sendDirective(t, d.cfg.SocketPath, "start")
	waitForState(t, d, StateRunning, 1*time.Second)

	// Wait for any initial tryAssign calls from the assign loop to settle,
	// then snapshot readyCalled.
	time.Sleep(100 * time.Millisecond)
	beadSrc.mu.Lock()
	before := beadSrc.readyCalled
	beadSrc.mu.Unlock()

	// Engage the dolt-recovery pause gate.
	d.doltRecovering.Store(true)

	// Call tryAssign directly — it must return without calling beads.Ready().
	d.tryAssign(context.Background())

	beadSrc.mu.Lock()
	after := beadSrc.readyCalled
	beadSrc.mu.Unlock()

	if after != before {
		t.Fatalf("tryAssign called beads.Ready() %d time(s) when doltRecovering=true; expected 0", after-before)
	}
}
