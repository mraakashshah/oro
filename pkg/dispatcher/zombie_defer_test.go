package dispatcher //nolint:testpackage // white-box test needs access to detectZombieDeferred and dispatcher test mocks

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

func TestDetectZombieDeferred(t *testing.T) {
	t.Run("fixes open beads with defer_until", func(t *testing.T) {
		d, beadSrc, _, _, _, _ := newTestDispatcher(t)
		beadSrc.exportData = []byte(strings.Join([]string{
			`{"id":"oro-zombie","status":"open","defer_until":"2026-04-27T04:00:00Z"}`,
			`{"id":"oro-legit-deferred","status":"deferred","defer_until":"2099-01-01T00:00:00Z"}`,
			`{"id":"oro-clean","status":"open"}`,
		}, "\n"))

		fixed, err := d.detectZombieDeferred(context.Background())
		if err != nil {
			t.Fatalf("detectZombieDeferred returned error: %v", err)
		}
		if fixed != 1 {
			t.Fatalf("fixed = %d, want 1", fixed)
		}
		if len(beadSrc.deferCalls) != 1 {
			t.Fatalf("defer calls = %v, want one call", beadSrc.deferCalls)
		}
		if beadSrc.deferCalls[0].id != "oro-zombie" {
			t.Fatalf("defer id = %q, want oro-zombie", beadSrc.deferCalls[0].id)
		}
		if beadSrc.deferCalls[0].until == "" {
			t.Fatal("defer until is empty")
		}
		if got := beadSrc.undeferCalls; len(got) != 1 || got[0] != "oro-zombie" {
			t.Fatalf("undefer calls = %v, want [oro-zombie]", got)
		}
		wantOps := []string{"defer:oro-zombie", "undefer:oro-zombie"}
		if strings.Join(beadSrc.beadOps, ",") != strings.Join(wantOps, ",") {
			t.Fatalf("bead ops = %v, want %v", beadSrc.beadOps, wantOps)
		}
		if n := eventCount(t, d.db, "zombie_deferred_detected"); n != 1 {
			t.Fatalf("zombie_deferred_detected events = %d, want 1", n)
		}
	})

	t.Run("no zombies logs nothing", func(t *testing.T) {
		d, beadSrc, _, _, _, _ := newTestDispatcher(t)
		beadSrc.exportData = []byte(strings.Join([]string{
			`{"id":"oro-clean","status":"open"}`,
			`{"id":"oro-legit-deferred","status":"deferred","defer_until":"2099-01-01T00:00:00Z"}`,
		}, "\n"))

		fixed, err := d.detectZombieDeferred(context.Background())
		if err != nil {
			t.Fatalf("detectZombieDeferred returned error: %v", err)
		}
		if fixed != 0 {
			t.Fatalf("fixed = %d, want 0", fixed)
		}
		if n := eventCount(t, d.db, "zombie_deferred_detected"); n != 0 {
			t.Fatalf("zombie_deferred_detected events = %d, want 0", n)
		}
	})

	t.Run("export failure logs and returns error", func(t *testing.T) {
		d, beadSrc, _, _, _, _ := newTestDispatcher(t)
		beadSrc.exportErr = errors.New("boom")

		fixed, err := d.detectZombieDeferred(context.Background())
		if err == nil {
			t.Fatal("detectZombieDeferred returned nil error, want export error")
		}
		if fixed != 0 {
			t.Fatalf("fixed = %d, want 0", fixed)
		}
		if n := eventCount(t, d.db, "zombie_defer_check_failed"); n != 1 {
			t.Fatalf("zombie_defer_check_failed events = %d, want 1", n)
		}
	})
}

func latestEventID(t *testing.T, db *sql.DB, evType string) int64 {
	t.Helper()
	var id int64
	err := db.QueryRow(`SELECT id FROM events WHERE type=? ORDER BY id DESC LIMIT 1`, evType).Scan(&id)
	if err == sql.ErrNoRows {
		return 0
	}
	if err != nil {
		t.Fatalf("query event id %q: %v", evType, err)
	}
	return id
}

func latestEventPayload(t *testing.T, db *sql.DB, evType string) string {
	t.Helper()
	var payload string
	err := db.QueryRow(`SELECT payload FROM events WHERE type=? ORDER BY id DESC LIMIT 1`, evType).Scan(&payload)
	if err == sql.ErrNoRows {
		return ""
	}
	if err != nil {
		t.Fatalf("query event payload %q: %v", evType, err)
	}
	return payload
}

func TestStartupCallsDetectZombieDeferred(t *testing.T) {
	t.Run("runs after reconciliation summary and before socket bind", func(t *testing.T) {
		d, beadSrc, _, _, _, _ := newTestDispatcher(t)
		beadSrc.exportData = []byte(`{"id":"oro-zombie","status":"open","defer_until":"2026-04-27T04:00:00Z"}` + "\n")

		sockPath := shortSockPath(t, "zombie-run")
		ln, err := net.Listen("unix", sockPath)
		if err != nil {
			t.Fatalf("listen active socket: %v", err)
		}
		t.Cleanup(func() { _ = ln.Close() })
		d.cfg.SocketPath = sockPath

		err = d.Run(context.Background())
		if err == nil {
			t.Fatal("Run returned nil error, want active socket error")
		}
		if len(beadSrc.deferCalls) != 1 {
			t.Fatalf("defer calls = %v, want one zombie fix before socket bind", beadSrc.deferCalls)
		}
		if got := beadSrc.undeferCalls; len(got) != 1 || got[0] != "oro-zombie" {
			t.Fatalf("undefer calls = %v, want [oro-zombie]", got)
		}

		reconciliationID := latestEventID(t, d.db, "startup_reconciliation_summary")
		zombieSummaryID := latestEventID(t, d.db, "startup_zombie_defer_summary")
		if reconciliationID == 0 {
			t.Fatal("startup_reconciliation_summary was not logged")
		}
		if zombieSummaryID == 0 {
			t.Fatal("startup_zombie_defer_summary was not logged")
		}
		if zombieSummaryID <= reconciliationID {
			t.Fatalf("zombie summary event id = %d, want after reconciliation id %d", zombieSummaryID, reconciliationID)
		}
		if count := latestEventPayload(t, d.db, "startup_zombie_defer_summary"); !strings.Contains(count, `"fixed":1`) {
			t.Fatalf("startup_zombie_defer_summary payload = %q, want fixed count 1", count)
		}
	})

	t.Run("skips repair in shadow mode", func(t *testing.T) {
		t.Setenv("ORO_BEADSOURCE_MODE", "shadow")
		d, beadSrc, _, _, _, _ := newTestDispatcher(t)
		beadSrc.exportData = []byte(`{"id":"oro-zombie","status":"open","defer_until":"2026-04-27T04:00:00Z"}` + "\n")

		sockPath := shortSockPath(t, "zombie-shadow")
		ln, err := net.Listen("unix", sockPath)
		if err != nil {
			t.Fatalf("listen active socket: %v", err)
		}
		t.Cleanup(func() { _ = ln.Close() })
		d.cfg.SocketPath = sockPath

		err = d.Run(context.Background())
		if err == nil {
			t.Fatal("Run returned nil error, want active socket error")
		}
		if len(beadSrc.deferCalls) != 0 {
			t.Fatalf("defer calls = %v, want none in shadow mode", beadSrc.deferCalls)
		}
		if len(beadSrc.undeferCalls) != 0 {
			t.Fatalf("undefer calls = %v, want none in shadow mode", beadSrc.undeferCalls)
		}
		if n := eventCount(t, d.db, "startup_zombie_defer_summary"); n != 0 {
			t.Fatalf("startup_zombie_defer_summary events = %d, want 0 in shadow mode", n)
		}
	})

	t.Run("skips repair in sqlite mode", func(t *testing.T) {
		d, beadSrc, _, _, _, _ := newTestDispatcher(t)
		d.beadSourceMode = "sqlite"
		beadSrc.exportData = []byte(`{"id":"oro-native-deferred","status":"open","defer_until":"2026-07-31T13:00:00Z"}` + "\n")

		sockPath := shortSockPath(t, "zombie-sqlite")
		ln, err := net.Listen("unix", sockPath)
		if err != nil {
			t.Fatalf("listen active socket: %v", err)
		}
		t.Cleanup(func() { _ = ln.Close() })
		d.cfg.SocketPath = sockPath

		err = d.Run(context.Background())
		if err == nil {
			t.Fatal("Run returned nil error, want active socket error")
		}
		if len(beadSrc.deferCalls) != 0 {
			t.Fatalf("defer calls = %v, want none in sqlite mode", beadSrc.deferCalls)
		}
		if len(beadSrc.undeferCalls) != 0 {
			t.Fatalf("undefer calls = %v, want none in sqlite mode", beadSrc.undeferCalls)
		}
		if n := eventCount(t, d.db, "startup_zombie_defer_summary"); n != 0 {
			t.Fatalf("startup_zombie_defer_summary events = %d, want 0 in sqlite mode", n)
		}
	})

	t.Run("export error logs and startup continues", func(t *testing.T) {
		d, beadSrc, _, _, _, _ := newTestDispatcher(t)
		beadSrc.exportErr = errors.New("boom")

		ctx, cancel := context.WithCancel(context.Background())
		errCh := make(chan error, 1)
		go func() { errCh <- d.Run(ctx) }()

		waitFor(t, func() bool {
			d.mu.Lock()
			defer d.mu.Unlock()
			return d.listener != nil
		}, 2*time.Second)
		cancel()

		select {
		case err := <-errCh:
			if err != nil {
				t.Fatalf("Run returned export error: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("Run did not stop after cancellation")
		}
		if n := eventCount(t, d.db, "zombie_defer_check_failed"); n != 1 {
			t.Fatalf("zombie_defer_check_failed events = %d, want 1", n)
		}
	})
}
