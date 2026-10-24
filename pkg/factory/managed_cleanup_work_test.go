package factory //nolint:testpackage // Verify mutual exclusion through the real journal API.

import (
	"database/sql"
	"testing"
	"time"
)

func testManagedCleanupWorkGate(t *testing.T) {
	t.Helper()
	f := newCleanupFixtureState(t, "container", false)
	work := journalScope("e", f.target.Generation)
	if _, err := f.j.reserveScope(t.Context(), work, f.a.Deadline, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := reserveManagedCleanupInvocation(t.Context(), f.j, work.Generation, f.target, DockerCleanupInspect, f.binding, f.a.HardStop); err == nil {
		t.Fatal("cleanup bypassed prior live work")
	}
	if got := testCleanupCounts(t, f.j); got != ([3]int{0, 1, 0}) {
		t.Fatalf("rejected cleanup leaked authority: %v", got)
	}
	if err := f.j.MarkQuiescent(t.Context(), work); err != nil {
		t.Fatal(err)
	}
	cleanup, err := reserveManagedCleanupInvocation(t.Context(), f.j, work.Generation, f.target, DockerCleanupInspect, f.binding, f.a.HardStop)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"host", "model", "tool", "controller"} {
		if _, err := f.j.reserveBudget(t.Context(), work.Generation, kind, "blocked", time.Now()); err == nil {
			t.Errorf("live cleanup admitted %s work budget", kind)
		}
	}
	other := journalScope("f", work.Generation)
	if _, err := f.j.reserveScope(t.Context(), other, f.a.Deadline, time.Now()); err == nil {
		t.Fatal("live cleanup admitted an ordinary scope")
	}
	var leaked int
	if err := f.j.conn.QueryRowContext(t.Context(), "SELECT count(*) FROM managed_reservations WHERE token='blocked'").Scan(&leaked); err != nil || leaked != 0 {
		t.Fatalf("blocked work spent budget: %d %v", leaked, err)
	}
	// Simulate an older writer leaving a peer after reservation: HELLO must
	// independently recheck exclusion before granting the cleanup helper.
	if err := f.j.mutate(t.Context(), "test-peer", other.ID, func(tx *sql.Tx, _ *managedSupervision) (bool, error) {
		return f.j.insertScope(t.Context(), tx, other, f.a.Deadline)
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.j.RecordHello(t.Context(), cleanup, 101, 101); err == nil {
		t.Fatal("cleanup HELLO ignored a newly visible host peer")
	}
	if err := f.j.MarkQuiescent(t.Context(), other); err != nil {
		t.Fatal(err)
	}
	if err := f.j.RecordHello(t.Context(), cleanup, 101, 101); err != nil {
		t.Fatal(err)
	}
	if err := f.j.MarkQuiescent(t.Context(), cleanup); err != nil {
		t.Fatal(err)
	}
	if _, err := f.j.reserveBudget(t.Context(), work.Generation, "host", "after-cleanup", time.Now()); err != nil {
		t.Fatal(err)
	}
}

func testManagedCleanupAdmissionRace(t *testing.T) {
	t.Helper()
	for range 6 {
		f := newCleanupFixtureState(t, "container", false)
		other := openJournalFixture(t, f.root, f.a.ObjectiveID)
		work := journalScope("e", f.target.Generation)
		start := make(chan struct{})
		ordinary, cleanup := make(chan error, 1), make(chan error, 1)
		go func() {
			<-start
			_, err := f.j.reserveScope(t.Context(), work, f.a.Deadline, time.Now())
			ordinary <- err
		}()
		go func() {
			<-start
			_, err := reserveManagedCleanupInvocation(t.Context(), other, work.Generation, f.target, DockerCleanupInspect, f.binding, f.a.HardStop)
			cleanup <- err
		}()
		close(start)
		workErr, cleanupErr := <-ordinary, <-cleanup
		if (workErr == nil) == (cleanupErr == nil) {
			t.Fatalf("work and cleanup were not exclusive: work=%v cleanup=%v", workErr, cleanupErr)
		}
		want := [3]int{1, 1, 1}
		if workErr == nil {
			want = [3]int{0, 1, 0}
		}
		if got := testCleanupCounts(t, f.j); got != want {
			t.Fatalf("losing reservation leaked: got=%v want=%v", got, want)
		}
	}
}
