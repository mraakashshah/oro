package factory //nolint:testpackage // Exercise cleanup exclusion against persisted authority corruption.

import (
	"bytes"
	"database/sql"
	"reflect"
	"testing"
	"time"
)

func TestManagedCleanupScopeIntegrity(t *testing.T) {
	t.Run("noncanonical cleanup kind cannot admit ordinary work", testCleanupIntegrityKind)
	t.Run("backend column cannot hide live host from reservation", testCleanupIntegrityBackend)
	t.Run("backend column cannot hide late peer from existing grant", testCleanupIntegrityLatePeer)
	t.Run("excluded rows still require complete canonical authority", testCleanupIntegrityExcluded)
	t.Run("canonical peers retire before work resumes", testCleanupIntegrityValid)
}

func testCleanupIntegrityKind(t *testing.T) {
	t.Helper()
	f := newCleanupFixtureState(t, "container", false)
	cleanup := reserveCleanupIntegrity(t, f)
	var body []byte
	if err := f.j.conn.QueryRowContext(t.Context(), "SELECT body FROM managed_scopes WHERE id=?", cleanup.ID).Scan(&body); err != nil {
		t.Fatal(err)
	}
	changed := bytes.Replace(body, []byte(`"kind"`), []byte(`"Kind"`), 1)
	if bytes.Equal(changed, body) {
		t.Fatal("fixture lacks the canonical lowercase kind key")
	}
	if _, err := f.j.conn.ExecContext(t.Context(), "UPDATE managed_scopes SET body=? WHERE id=?", changed, cleanup.ID); err != nil {
		t.Fatal(err)
	}
	before := snapshotCleanupIntegrity(t, f.j)
	assertCleanupIntegrityWorkDenied(t, f)
	assertCleanupIntegrityUnchanged(t, f.j, before)
}

func testCleanupIntegrityBackend(t *testing.T) {
	t.Helper()
	f := newCleanupFixtureState(t, "container", false)
	peer := journalScope("e", f.target.Generation)
	if _, err := f.j.reserveScope(t.Context(), peer, f.a.Deadline, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := f.j.RecordHello(t.Context(), peer, 101, 100); err != nil {
		t.Fatal(err)
	}
	hideCleanupIntegrityPeer(t, f.j, peer)
	before := snapshotCleanupIntegrity(t, f.j)
	assertCleanupIntegrityReservationDenied(t, f)
	assertCleanupIntegrityUnchanged(t, f.j, before)
}

func testCleanupIntegrityLatePeer(t *testing.T) {
	t.Helper()
	f := newCleanupFixtureState(t, "container", false)
	cleanup := reserveCleanupIntegrity(t, f)
	if err := f.j.RecordHello(t.Context(), cleanup, 101, 100); err != nil {
		t.Fatal(err)
	}
	peer := journalScope("e", f.target.Generation)
	// As in the existing work-gate regression, model an older writer leaving a
	// canonical peer after cleanup reservation. Every grant read must recheck it.
	if err := f.j.mutate(t.Context(), "test-peer", peer.ID, func(tx *sql.Tx, _ *managedSupervision) (bool, error) {
		return f.j.insertScope(t.Context(), tx, peer, f.a.Deadline)
	}); err != nil {
		t.Fatal(err)
	}
	hideCleanupIntegrityPeer(t, f.j, peer)
	before := snapshotCleanupIntegrity(t, f.j)
	if permit, err := f.j.CleanupPermit(t.Context(), cleanup); err == nil || !reflect.DeepEqual(permit, DockerCleanupPermit{}) {
		t.Errorf("hidden late peer returned cleanup permit: %+v %v", permit, err)
	}
	if err := f.j.RecordHello(t.Context(), cleanup, 101, 100); err == nil {
		t.Error("hidden late peer accepted cleanup HELLO replay")
	}
	if granted, err := f.j.Granted(t.Context(), cleanup); err == nil || granted {
		t.Errorf("hidden late peer preserved cleanup grant: %v %v", granted, err)
	}
	assertCleanupIntegrityUnchanged(t, f.j, before)
}

func hideCleanupIntegrityPeer(t *testing.T, j *managedJournal, peer ScopeHandle) {
	t.Helper()
	if _, err := j.conn.ExecContext(t.Context(), "UPDATE managed_scopes SET backend='docker' WHERE id=?", peer.ID); err != nil {
		t.Fatal(err)
	}
}

func testCleanupIntegrityExcluded(t *testing.T) {
	t.Helper()
	for _, corruption := range []string{"unknown", "duplicate", "case alias", "quiet missing evidence", "oversized"} {
		t.Run(corruption+"/ordinary", func(t *testing.T) {
			f := cleanupIntegrityRetiredPeer(t, corruption)
			before := snapshotCleanupIntegrity(t, f.j)
			assertCleanupIntegrityWorkDenied(t, f)
			assertCleanupIntegrityUnchanged(t, f.j, before)
		})
		t.Run(corruption+"/cleanup", func(t *testing.T) {
			f := cleanupIntegrityRetiredPeer(t, corruption)
			before := snapshotCleanupIntegrity(t, f.j)
			assertCleanupIntegrityReservationDenied(t, f)
			assertCleanupIntegrityUnchanged(t, f.j, before)
		})
	}
}

func cleanupIntegrityRetiredPeer(t *testing.T, corruption string) cleanupFixture {
	t.Helper()
	f := newCleanupFixtureState(t, "container", false)
	peer := journalScope("e", f.target.Generation)
	if _, err := f.j.reserveScope(t.Context(), peer, f.a.Deadline, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := f.j.MarkQuiescent(t.Context(), peer); err != nil {
		t.Fatal(err)
	}
	if corruption == "quiet missing evidence" {
		if _, err := f.j.conn.ExecContext(t.Context(), "UPDATE managed_scopes SET cleanup='' WHERE id=?", peer.ID); err != nil {
			t.Fatal(err)
		}
	} else {
		corruptScopeIntegrity(t, f.j, peer, corruption)
	}
	return f
}

func assertCleanupIntegrityWorkDenied(t *testing.T, f cleanupFixture) {
	t.Helper()
	for _, kind := range []string{"host", "model", "tool"} {
		if fresh, err := f.j.reserveBudget(t.Context(), f.target.Generation, kind, "corrupt-peer", time.Now()); err == nil || fresh {
			t.Errorf("malformed scope admitted ordinary %s budget: fresh=%v error=%v", kind, fresh, err)
		}
	}
	other := journalScope("f", f.target.Generation)
	if fresh, err := f.j.reserveScope(t.Context(), other, f.a.Deadline, time.Now()); err == nil || fresh {
		t.Errorf("malformed scope admitted ordinary scope: fresh=%v error=%v", fresh, err)
	}
}

func assertCleanupIntegrityReservationDenied(t *testing.T, f cleanupFixture) {
	t.Helper()
	scope, err := reserveManagedCleanupInvocation(t.Context(), f.j, f.target.Generation, f.target, DockerCleanupInspect, f.binding, f.a.HardStop)
	if err == nil || scope != (ScopeHandle{}) {
		t.Errorf("malformed peer admitted cleanup scope: %+v %v", scope, err)
	}
}

func reserveCleanupIntegrity(t *testing.T, f cleanupFixture) ScopeHandle {
	t.Helper()
	scope, err := reserveManagedCleanupInvocation(t.Context(), f.j, f.target.Generation, f.target, DockerCleanupInspect, f.binding, f.a.HardStop)
	if err != nil {
		t.Fatal(err)
	}
	return scope
}

func snapshotCleanupIntegrity(t *testing.T, j *managedJournal) string {
	t.Helper()
	var permits string
	if err := j.conn.QueryRowContext(t.Context(), "SELECT coalesce(group_concat(scope_id||':'||hex(body),'|'),'') FROM (SELECT * FROM managed_cleanup_permits ORDER BY scope_id)").Scan(&permits); err != nil {
		t.Fatal(err)
	}
	return snapshotScopeIntegrity(t, j) + "\n" + permits
}

func assertCleanupIntegrityUnchanged(t *testing.T, j *managedJournal, before string) {
	t.Helper()
	if snapshotCleanupIntegrity(t, j) != before {
		t.Error("denial changed scope rows, permits, reservations, revision or events")
	}
}

func testCleanupIntegrityValid(t *testing.T) {
	t.Helper()
	f := newCleanupFixtureState(t, "container", false)
	if err := f.j.RecordHello(t.Context(), f.target, 101, 100); err != nil {
		t.Fatal(err)
	}
	peer := journalScope("e", f.target.Generation)
	if _, err := f.j.reserveScope(t.Context(), peer, f.a.Deadline, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := f.j.RecordHello(t.Context(), peer, 103, 102); err != nil {
		t.Fatal(err)
	}
	before := snapshotCleanupIntegrity(t, f.j)
	assertCleanupIntegrityReservationDenied(t, f)
	assertCleanupIntegrityUnchanged(t, f.j, before)
	if err := f.j.MarkQuiescent(t.Context(), peer); err != nil {
		t.Fatal(err)
	}
	cleanup := reserveCleanupIntegrity(t, f)
	if permit, err := f.j.CleanupPermit(t.Context(), cleanup); err != nil || permit.Scope != cleanup {
		t.Fatalf("canonical reserved self permit: %+v %v", permit, err)
	}
	if err := f.j.RecordHello(t.Context(), cleanup, 105, 104); err != nil {
		t.Fatal(err)
	}
	before = snapshotCleanupIntegrity(t, f.j)
	if err := f.j.RecordHello(t.Context(), cleanup, 105, 104); err != nil {
		t.Fatal(err)
	}
	if granted, err := f.j.Granted(t.Context(), cleanup); err != nil || !granted {
		t.Fatalf("canonical granted self denied: %v %v", granted, err)
	}
	assertCleanupIntegrityUnchanged(t, f.j, before)
	if err := f.j.MarkQuiescent(t.Context(), cleanup); err != nil {
		t.Fatal(err)
	}
	if fresh, err := f.j.reserveBudget(t.Context(), peer.Generation, "host", "resumed-work", time.Now()); err != nil || !fresh {
		t.Fatalf("real cleanup retirement did not resume work budget: %v %v", fresh, err)
	}
	if fresh, err := f.j.reserveScope(t.Context(), journalScope("f", peer.Generation), f.a.Deadline, time.Now()); err != nil || !fresh {
		t.Fatalf("real cleanup retirement did not resume work scope: %v %v", fresh, err)
	}
}
