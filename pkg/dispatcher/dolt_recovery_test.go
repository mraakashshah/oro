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

// TestRecoverDolt_FullSequence verifies the full recoverDolt sequence:
// sets doltRecovering, shells out bd dolt start + bd import, unsets on success,
// increments attempts and applies backoff on failure, escalates after 3 failures,
// and logs dolt_recovery_started / dolt_recovery_succeeded / dolt_recovery_failed.
func TestRecoverDolt_FullSequence(t *testing.T) {
	t.Run("success: sets doltRecovering, runs commands, unsets on success", func(t *testing.T) {
		d, _, _, esc, _, _ := newTestDispatcher(t)
		d.recoverDoltBackoffFn = func(_ int) time.Duration { return 0 }
		d.beadsDir = t.TempDir()

		var cmds []mockCall
		d.shutdownRunner = &mockCommandRunner{
			callFn: func(_ context.Context, name string, args ...string) ([]byte, error) {
				cmds = append(cmds, mockCall{Name: name, Args: args})
				return nil, nil
			},
		}

		d.recoverDolt(context.Background())

		if d.doltRecovering.Load() {
			t.Error("doltRecovering still true after success, want false")
		}
		if d.doltRecoveryAttempts != 0 {
			t.Errorf("doltRecoveryAttempts = %d, want 0 after success", d.doltRecoveryAttempts)
		}
		if msgs := esc.Messages(); len(msgs) != 0 {
			t.Errorf("unexpected escalation on success: %v", msgs)
		}
		if len(cmds) < 2 {
			t.Fatalf("expected >= 2 commands, got %d: %v", len(cmds), cmds)
		}
		if cmds[0].Name != "bd" || len(cmds[0].Args) < 2 || cmds[0].Args[0] != "dolt" || cmds[0].Args[1] != "start" {
			t.Errorf("first command = %s %v, want bd dolt start", cmds[0].Name, cmds[0].Args)
		}
		if cmds[1].Name != "bd" || len(cmds[1].Args) < 1 || cmds[1].Args[0] != "import" {
			t.Errorf("second command = %s %v, want bd import ...", cmds[1].Name, cmds[1].Args)
		}
		if len(cmds[1].Args) < 2 || cmds[1].Args[1] == "" {
			t.Errorf("bd import missing backup path arg, got %v", cmds[1].Args)
		}
		if eventCount(t, d.db, "dolt_recovery_started") < 1 {
			t.Error("dolt_recovery_started event not logged")
		}
		if eventCount(t, d.db, "dolt_recovery_succeeded") < 1 {
			t.Error("dolt_recovery_succeeded event not logged")
		}
	})

	t.Run("failure: doltRecovering stays true, increments attempts, logs event", func(t *testing.T) {
		d, _, _, esc, _, _ := newTestDispatcher(t)
		d.recoverDoltBackoffFn = func(_ int) time.Duration { return 0 }
		d.beadsDir = t.TempDir()
		d.shutdownRunner = &mockCommandRunner{err: errors.New("port conflict")}

		d.recoverDolt(context.Background())

		if !d.doltRecovering.Load() {
			t.Error("doltRecovering should be true after failed recovery")
		}
		if d.doltRecoveryAttempts != 1 {
			t.Errorf("doltRecoveryAttempts = %d, want 1 after first failure", d.doltRecoveryAttempts)
		}
		if msgs := esc.Messages(); len(msgs) != 0 {
			t.Errorf("unexpected escalation after first failure: %v", msgs)
		}
		if eventCount(t, d.db, "dolt_recovery_failed") < 1 {
			t.Error("dolt_recovery_failed event not logged")
		}
	})

	t.Run("backoff: recoverDoltBackoff returns 1s,2s,4s for attempts 1,2,3", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)

		cases := []struct {
			n    int
			want time.Duration
		}{
			{1, 1 * time.Second},
			{2, 2 * time.Second},
			{3, 4 * time.Second},
		}
		for _, c := range cases {
			got := d.recoverDoltBackoff(c.n)
			if got != c.want {
				t.Errorf("recoverDoltBackoff(%d) = %v, want %v", c.n, got, c.want)
			}
		}
	})

	t.Run("escalates after 3 consecutive failures", func(t *testing.T) {
		d, _, _, esc, _, _ := newTestDispatcher(t)
		d.recoverDoltBackoffFn = func(_ int) time.Duration { return 0 }
		d.beadsDir = t.TempDir()
		d.shutdownRunner = &mockCommandRunner{err: errors.New("bd dolt start failed")}

		d.recoverDolt(context.Background())
		d.recoverDolt(context.Background())
		d.recoverDolt(context.Background())

		msgs := esc.Messages()
		if len(msgs) == 0 {
			t.Fatal("expected escalation after 3 consecutive failures, got none")
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
