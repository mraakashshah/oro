package dispatcher //nolint:testpackage // internal white-box tests need access to unexported fields

import (
	"context"
	"errors"
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

// TestCheckDoltHealth_DetectsDown verifies that checkDoltHealth returns false
// when "bd dolt status" exits non-zero, true on success, and that it applies a
// 5-second context timeout to the command.
func TestCheckDoltHealth_DetectsDown(t *testing.T) {
	t.Run("returns false on non-zero exit", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		d.shutdownRunner = &mockCommandRunner{err: errors.New("exit status 1")}
		if d.checkDoltHealth(context.Background()) {
			t.Fatal("checkDoltHealth returned true on non-zero exit, want false")
		}
	})

	t.Run("returns true on zero exit", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		d.shutdownRunner = &mockCommandRunner{}
		if !d.checkDoltHealth(context.Background()) {
			t.Fatal("checkDoltHealth returned false on zero exit, want true")
		}
	})

	t.Run("uses 5s context timeout", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		var gotDeadline time.Time
		before := time.Now()
		d.shutdownRunner = &mockCommandRunner{
			callFn: func(ctx context.Context, _ string, _ ...string) ([]byte, error) {
				dl, ok := ctx.Deadline()
				if ok {
					gotDeadline = dl
				}
				return nil, nil
			},
		}
		d.checkDoltHealth(context.Background())
		if gotDeadline.IsZero() {
			t.Fatal("checkDoltHealth did not set a context deadline")
		}
		offset := gotDeadline.Sub(before)
		if offset < 4*time.Second || offset > 6*time.Second {
			t.Errorf("context deadline offset = %v, want ~5s", offset)
		}
	})
}
