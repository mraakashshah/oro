package dispatcher //nolint:testpackage // white-box test needs access to detectZombieDeferred and dispatcher test mocks

import (
	"context"
	"errors"
	"strings"
	"testing"
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
