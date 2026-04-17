package dispatcher //nolint:testpackage // internal white-box tests need access to unexported fields

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

// TestChangeDetectionBackup_TriggersOnDelta verifies that maybeChangeDetectionBackup
// calls backupFullState when bead count changes by >=5 since last backup, and does
// NOT call it when delta <5. It also verifies that lastBackupBeadCount is updated after backup.
func TestChangeDetectionBackup_TriggersOnDelta(t *testing.T) {
	t.Run("triggers backup when delta >= 5", func(t *testing.T) {
		d, beadSrc, _, _, _, _ := newTestDispatcher(t)
		beadsDir := t.TempDir()
		d.beadsDir = beadsDir

		// Set up initial state: 10 in-progress beads
		beadSrc.mu.Lock()
		beadSrc.inProgressBeads = make([]protocol.Bead, 10)
		for i := 0; i < 10; i++ {
			beadSrc.inProgressBeads[i] = protocol.Bead{ID: fmt.Sprintf("oro-%d", i)}
		}
		// Set export data so backupFullState has something to write
		beadSrc.exportData = []byte(`{"id":"oro-1","status":"open"}`)
		beadSrc.mu.Unlock()

		// Initialize lastBackupBeadCount to 0 (first backup)
		d.lastBackupBeadCount = 0

		// First call should trigger backup (delta = 10 >= 5)
		d.maybeChangeDetectionBackup(context.Background())

		// Verify backup was written
		backupPath := filepath.Join(beadsDir, "backup", "full-state.jsonl")
		if _, err := os.Stat(backupPath); err != nil {
			t.Fatalf("backup file not created after delta>=5: %v", err)
		}

		// Verify lastBackupBeadCount was updated
		if d.lastBackupBeadCount != 10 {
			t.Errorf("lastBackupBeadCount = %d, want 10", d.lastBackupBeadCount)
		}

		// Remove backup file for next test
		os.RemoveAll(filepath.Join(beadsDir, "backup"))

		// Add one more bead (delta = 1 < 5) — should NOT trigger backup
		beadSrc.mu.Lock()
		beadSrc.inProgressBeads = append(beadSrc.inProgressBeads, protocol.Bead{ID: "oro-11"})
		beadSrc.mu.Unlock()

		d.maybeChangeDetectionBackup(context.Background())

		// Verify backup was NOT written
		if _, err := os.Stat(backupPath); err == nil {
			t.Error("backup file created when delta<5, should not have")
		}

		// lastBackupBeadCount should remain unchanged
		if d.lastBackupBeadCount != 10 {
			t.Errorf("lastBackupBeadCount = %d, want 10 (unchanged)", d.lastBackupBeadCount)
		}
	})

	t.Run("triggers backup when count decreases by >=5", func(t *testing.T) {
		d, beadSrc, _, _, _, _ := newTestDispatcher(t)
		beadsDir := t.TempDir()
		d.beadsDir = beadsDir

		// Set up initial state: 15 in-progress beads
		beadSrc.mu.Lock()
		beadSrc.inProgressBeads = make([]protocol.Bead, 15)
		for i := 0; i < 15; i++ {
			beadSrc.inProgressBeads[i] = protocol.Bead{ID: fmt.Sprintf("oro-%d", i)}
		}
		beadSrc.exportData = []byte(`{"id":"oro-1","status":"open"}`)
		beadSrc.mu.Unlock()

		// Initialize lastBackupBeadCount to 10
		d.lastBackupBeadCount = 10

		// Count decreased to 15, delta = 5 >= 5 should trigger backup... wait, that's increasing
		// Let me re-read the spec. Ah, I need to test decreasing too.
		// Actually, let me reconsider. The current count is 15, last backup was at 10, so delta=5 upward.
		// Let me set it up differently for the decrease case.

		// Actually, let's keep this simple. Set lastBackupBeadCount = 20
		d.lastBackupBeadCount = 20

		// Current count is 15, delta = 5 downward, should trigger
		d.maybeChangeDetectionBackup(context.Background())

		backupPath := filepath.Join(beadsDir, "backup", "full-state.jsonl")
		if _, err := os.Stat(backupPath); err != nil {
			t.Fatalf("backup not created when count decreased by 5: %v", err)
		}

		if d.lastBackupBeadCount != 15 {
			t.Errorf("lastBackupBeadCount = %d, want 15", d.lastBackupBeadCount)
		}
	})
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
