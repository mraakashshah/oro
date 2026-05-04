package dispatcher //nolint:testpackage // white-box test needs internal access

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"testing"
	"time"
)

func TestApplyHealth(t *testing.T) {
	// Create a test dispatcher
	d, _, _, _, _, _ := newTestDispatcher(t)
	cancel := startDispatcher(t, d)
	defer cancel()

	// Register a worker to verify it appears in health data
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	// Drain clientConn so writes don't block
	go func() {
		buf := make([]byte, 4096)
		for {
			if _, err := clientConn.Read(buf); err != nil {
				return
			}
		}
	}()

	d.registerWorker("worker-1", serverConn)

	// Call applyHealth
	result, err := d.applyHealth()
	if err != nil {
		t.Fatalf("applyHealth failed: %v", err)
	}

	// Verify it returns valid JSON
	var health SwarmHealth
	if err := json.Unmarshal([]byte(result), &health); err != nil {
		t.Fatalf("Failed to unmarshal health JSON: %v", err)
	}

	// Assert: Returns daemon PID
	if health.Daemon.PID != os.Getpid() {
		t.Errorf("Expected daemon PID %d, got %d", os.Getpid(), health.Daemon.PID)
	}

	// Assert: Returns daemon state
	if health.Daemon.State == "" {
		t.Error("Expected daemon state to be set")
	}

	// Assert: Returns worker statuses
	if len(health.Workers) != 1 {
		t.Errorf("Expected 1 worker, got %d", len(health.Workers))
	}

	if len(health.Workers) > 0 {
		worker := health.Workers[0]
		if worker.ID != "worker-1" {
			t.Errorf("Expected worker ID 'worker-1', got '%s'", worker.ID)
		}
	}

	// Assert: Daemon uptime is positive
	if health.Daemon.UptimeSeconds <= 0 {
		t.Errorf("Expected positive uptime, got %f", health.Daemon.UptimeSeconds)
	}

	// Note: PaneStatus assertions require DB setup and will be validated in integration tests
}

func TestApplyHealthViaDirective(t *testing.T) {
	// Create a test dispatcher
	d, _, _, _, _, _ := newTestDispatcher(t)
	cancel := startDispatcher(t, d)
	defer cancel()

	// Test that health directive is wired correctly
	result, err := d.applyDirective("health", "")
	if err != nil {
		t.Fatalf("applyDirective(health) failed: %v", err)
	}

	// Verify it returns valid JSON
	var health SwarmHealth
	if err := json.Unmarshal([]byte(result), &health); err != nil {
		t.Fatalf("Failed to unmarshal health JSON via directive: %v", err)
	}

	// Assert: Returns daemon PID
	if health.Daemon.PID != os.Getpid() {
		t.Errorf("Expected daemon PID %d, got %d", os.Getpid(), health.Daemon.PID)
	}
}

// TestHealthPaneAlive verifies that applyHealth sets Alive=true for panes whose
// pane_activity last_seen is within 60s, and Alive=false otherwise.
func TestHealthPaneAlive(t *testing.T) {
	fixedNow := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	t.Run("alive when last_seen within 60s", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		d.nowFunc = func() time.Time { return fixedNow }

		recentTS := fixedNow.Unix() - 30 // 30 seconds ago — within the 60s window
		if _, err := d.db.Exec(`INSERT OR REPLACE INTO pane_activity (pane, last_seen) VALUES (?, ?)`, "manager", recentTS); err != nil {
			t.Fatalf("insert manager: %v", err)
		}

		result, err := d.applyHealth()
		if err != nil {
			t.Fatalf("applyHealth: %v", err)
		}
		var health SwarmHealth
		if err := json.Unmarshal([]byte(result), &health); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}

		if !health.ManagerPane.Alive {
			t.Error("expected ManagerPane.Alive=true for recent pane_activity row")
		}
	})

	t.Run("not alive when last_seen older than 60s", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		d.nowFunc = func() time.Time { return fixedNow }

		staleTS := fixedNow.Unix() - 90 // 90 seconds ago — outside the 60s window
		if _, err := d.db.Exec(`INSERT OR REPLACE INTO pane_activity (pane, last_seen) VALUES (?, ?)`, "manager", staleTS); err != nil {
			t.Fatalf("insert manager: %v", err)
		}

		result, err := d.applyHealth()
		if err != nil {
			t.Fatalf("applyHealth: %v", err)
		}
		var health SwarmHealth
		if err := json.Unmarshal([]byte(result), &health); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}

		if health.ManagerPane.Alive {
			t.Error("expected ManagerPane.Alive=false for stale pane_activity row")
		}
	})

	t.Run("not alive when no pane_activity row", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)

		result, err := d.applyHealth()
		if err != nil {
			t.Fatalf("applyHealth: %v", err)
		}
		var health SwarmHealth
		if err := json.Unmarshal([]byte(result), &health); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}

		if health.ManagerPane.Alive {
			t.Error("expected ManagerPane.Alive=false when no pane_activity row")
		}
	})

	// Kill mutant: `<= 60` -> `< 60`. Source uses inclusive `<= 60`, so delta==60 is alive.
	// The mutant changes to `< 60` which would make delta==60 return false (Alive=false).
	t.Run("alive when last_seen exactly 60s ago", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		d.nowFunc = func() time.Time { return fixedNow }

		exactTS := fixedNow.Unix() - 60 // exactly 60 seconds ago — on the inclusive boundary
		if _, err := d.db.Exec(`INSERT OR REPLACE INTO pane_activity (pane, last_seen) VALUES (?, ?)`, "manager", exactTS); err != nil {
			t.Fatalf("insert manager: %v", err)
		}

		result, err := d.applyHealth()
		if err != nil {
			t.Fatalf("applyHealth: %v", err)
		}
		var health SwarmHealth
		if err := json.Unmarshal([]byte(result), &health); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}

		if !health.ManagerPane.Alive {
			t.Error("expected ManagerPane.Alive=true when last_seen exactly 60s ago (inclusive <= 60 window)")
		}
	})

	// Confirm boundary: 61 seconds ago is NOT alive.
	t.Run("not alive when last_seen 61s ago", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		d.nowFunc = func() time.Time { return fixedNow }

		staleTS := fixedNow.Unix() - 61
		if _, err := d.db.Exec(`INSERT OR REPLACE INTO pane_activity (pane, last_seen) VALUES (?, ?)`, "manager", staleTS); err != nil {
			t.Fatalf("insert manager: %v", err)
		}

		result, err := d.applyHealth()
		if err != nil {
			t.Fatalf("applyHealth: %v", err)
		}
		var health SwarmHealth
		if err := json.Unmarshal([]byte(result), &health); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}

		if health.ManagerPane.Alive {
			t.Error("expected ManagerPane.Alive=false when last_seen 61s ago")
		}
	})
}

// TestPaneAliveDirect tests the paneAlive function directly to kill boundary
// and error-path mutants that survive through applyHealth.
func TestPaneAliveDirect(t *testing.T) {
	ctx := context.Background()

	t.Run("returns false when no row exists", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		// No rows inserted — QueryRow returns sql.ErrNoRows
		got := paneAlive(ctx, d.db, 1000)
		if got {
			t.Error("expected false when pane_activity row is missing")
		}
	})

	// Kill mutant 0: removing `return false` from the error branch makes
	// paneAlive fall through to `return nowUnix-lastSeen <= 60` with lastSeen=0.
	// If nowUnix is small (e.g. 50), then 50-0=50 <= 60 = true (wrong).
	// This test uses nowUnix=50 to expose that incorrect true.
	t.Run("returns false when DB error and nowUnix is small", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		// No row — DB returns error. With nowUnix=50, lastSeen=0 gives 50-0=50<=60=true after mutation.
		got := paneAlive(ctx, d.db, 50)
		if got {
			t.Error("expected false on DB error regardless of nowUnix value")
		}
	})

	// Source uses inclusive `<= 60`, so delta==60 IS alive. Mutant changes to `< 60`.
	t.Run("returns true when last_seen exactly 60s ago", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		nowUnix := int64(1000)
		exactTS := nowUnix - 60
		if _, err := d.db.Exec(`INSERT OR REPLACE INTO pane_activity (pane, last_seen) VALUES (?, ?)`, "manager", exactTS); err != nil {
			t.Fatalf("insert: %v", err)
		}
		got := paneAlive(ctx, d.db, nowUnix)
		if !got {
			t.Error("expected true when last_seen exactly 60s ago (inclusive <= 60 window)")
		}
	})

	t.Run("returns false when last_seen 61s ago", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		nowUnix := int64(1000)
		staleTS := nowUnix - 61
		if _, err := d.db.Exec(`INSERT OR REPLACE INTO pane_activity (pane, last_seen) VALUES (?, ?)`, "manager", staleTS); err != nil {
			t.Fatalf("insert: %v", err)
		}
		got := paneAlive(ctx, d.db, nowUnix)
		if got {
			t.Error("expected false when last_seen 61s ago")
		}
	})

	t.Run("returns true when last_seen 59s ago", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		nowUnix := int64(1000)
		recentTS := nowUnix - 59
		if _, err := d.db.Exec(`INSERT OR REPLACE INTO pane_activity (pane, last_seen) VALUES (?, ?)`, "manager", recentTS); err != nil {
			t.Fatalf("insert: %v", err)
		}
		got := paneAlive(ctx, d.db, nowUnix)
		if !got {
			t.Error("expected true when last_seen 59s ago")
		}
	})
}
