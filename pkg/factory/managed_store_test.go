package factory //nolint:testpackage // Exercise private durable journal transactions and authority CAS.

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestManagedScopeJournal(t *testing.T) {
	t.Run("atomic immutable admission and reopen", testManagedJournalAdmission)
	t.Run("authenticated grants and revoked epochs", testManagedJournalAuthority)
	t.Run("durable idempotent bounded reservations", testManagedJournalBudgets)
	t.Run("parallel reservations and event rollback", testManagedJournalTransactions)
	t.Run("authority never creates or resets state", testManagedJournalMissing)
	t.Run("recovery observes authenticated ownership", testManagedJournalObserve)
	t.Run("bounded storage and start backoff", testManagedJournalBounds)
	t.Run("late observers spend separate reservation", testManagedJournalObserver)
	t.Run("controller epochs preserve supervisor starts", testManagedJournalEpochs)
	t.Run("new supervisor rejects an existing ownership epoch", testManagedJournalSupervisorEpochCollision)
}

func journalFixture(t *testing.T) (string, managedJournalAdmission, *factoryStore) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	spec := managedContractFixture()
	deadline := time.Now().UTC().Add(time.Hour)
	a := managedJournalAdmission{
		ObjectiveID: spec.ID, ManifestHash: strings.Repeat("a", 64), Deadline: deadline,
		HardStop: deadline.Add(time.Duration(spec.Recovery.CleanupTimeoutSeconds+spec.Recovery.ReconciliationSeconds) * time.Second),
		Recovery: spec.Recovery, Runtime: spec.Runtime,
		Reservation: HostReservation{ID: "reservation1", StateRoot: root, ObjectiveID: spec.ID, Bytes: spec.Runtime.HostRequiredBytes},
	}
	dir := filepath.Join(root, a.ObjectiveID)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := openManagedFactoryStore(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.close() })
	if err := initManagedSchema(t.Context(), store.conn); err != nil {
		t.Fatal(err)
	}
	return root, a, store
}

func insertJournalFixture(t *testing.T, store *factoryStore, a managedJournalAdmission) {
	t.Helper()
	tx, err := store.conn.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := insertManagedAdmission(t.Context(), tx, a); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func openJournalFixture(t *testing.T, root, id string) *managedJournal {
	t.Helper()
	j, err := openManagedJournal(t.Context(), root, id)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	return j
}

func testManagedJournalAdmission(t *testing.T) {
	t.Helper()
	root, a, store := journalFixture(t)
	tx, err := store.conn.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := insertManagedAdmission(t.Context(), tx, a); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenScopeAuthority(t.Context(), root, a.ObjectiveID); err == nil {
		t.Fatal("rolled back admission became authoritative")
	}
	insertJournalFixture(t, store, a)
	j := openJournalFixture(t, root, a.ObjectiveID)
	var synchronous int
	if err := j.conn.QueryRowContext(t.Context(), "PRAGMA synchronous").Scan(&synchronous); err != nil || synchronous != 2 {
		t.Fatalf("journal durability = %d, %v", synchronous, err)
	}
	generation, fresh, err := j.beginGeneration(t.Context(), "supervisor1", time.Now())
	if err != nil || !fresh || generation != 1 {
		t.Fatalf("reserve first incarnation = %d, %v, %v", generation, fresh, err)
	}
	insertJournalFixture(t, store, a)
	reopened := openJournalFixture(t, root, a.ObjectiveID)
	got, err := reopened.supervision(t.Context())
	if err != nil || got.Generation != 1 || got.DesiredState != "running" || !reflect.DeepEqual(reopened.admission, a) {
		t.Fatalf("reopen changed admission/counters: %+v, %v", got, err)
	}
	changed := a
	changed.Deadline = a.Deadline.Add(time.Minute)
	changed.HardStop = a.HardStop.Add(time.Minute)
	tx, err = store.conn.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := insertManagedAdmission(t.Context(), tx, changed); err == nil {
		t.Fatal("changed admission extended frozen deadlines")
	}
}

func journalScope(id string, generation uint64) ScopeHandle {
	return ScopeHandle{
		ID: strings.Repeat(id, 32), Nonce: strings.Repeat(id, 32), Generation: generation,
		Kind: "host", Backend: "launchd", Name: "dev.getoro.factory.0123456789abcdef.host." + strings.Repeat(id, 32) + "." + strings.Repeat(id, 32),
	}
}

func testManagedJournalAuthority(t *testing.T) {
	t.Helper()
	root, a, store := journalFixture(t)
	insertJournalFixture(t, store, a)
	j := openJournalFixture(t, root, a.ObjectiveID)
	gen, _, err := j.beginGeneration(t.Context(), "supervisor1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	scope := journalScope("b", gen)
	if err := j.RecordHello(t.Context(), scope, 101, 100); err == nil {
		t.Fatal("unjournaled scope received a grant")
	}
	deadline := time.Now().Add(time.Minute)
	if fresh, err := j.reserveScope(t.Context(), scope, deadline, time.Now()); err != nil || !fresh {
		t.Fatalf("scope reservation = %v, %v", fresh, err)
	}
	alias := journalScope("c", gen)
	alias.Name = scope.Name
	if _, err := j.reserveScope(t.Context(), alias, deadline, time.Now()); err == nil {
		t.Fatal("two scope identities claimed the same exact backend name")
	}
	if granted, err := j.Granted(t.Context(), scope); err != nil || granted {
		t.Fatalf("unacknowledged scope granted = %v, %v", granted, err)
	}
	for _, pid := range []int{-1, 0, 1} {
		if err := j.RecordHello(t.Context(), scope, pid, 100); err == nil {
			t.Fatalf("invalid authenticated PID %d accepted", pid)
		}
	}
	if err := j.RecordHello(t.Context(), scope, 101, 100); err != nil {
		t.Fatal(err)
	}
	if err := j.RecordHello(t.Context(), scope, 102, 100); err == nil {
		t.Fatal("grant replay changed peer identity")
	}
	if granted, err := j.Granted(t.Context(), scope); err != nil || !granted {
		t.Fatalf("authenticated scope grant = %v, %v", granted, err)
	}
	wrong := scope
	wrong.Nonce = strings.Repeat("c", 32)
	if granted, _ := j.Granted(t.Context(), wrong); granted {
		t.Fatal("forged nonce received GO")
	}
	gen, _, err = j.beginGeneration(t.Context(), "supervisor2", time.Now())
	if err != nil || gen != 2 {
		t.Fatalf("replacement epoch = %d, %v", gen, err)
	}
	if _, _, err := j.beginGeneration(t.Context(), "supervisor1", time.Now()); err == nil {
		t.Fatal("superseded supervisor token replay was accepted")
	}
	if granted, err := j.Granted(t.Context(), scope); err != nil || granted {
		t.Fatalf("revoked old grant = %v, %v", granted, err)
	}
	if err := j.RecordHello(t.Context(), scope, 101, 100); err == nil {
		t.Fatal("late HELLO revived a revoked scope")
	}
	if _, err := j.reserveBudget(t.Context(), gen, "controller", "controller2", time.Now()); err == nil {
		t.Fatal("replacement controller started before positive old-scope quiescence")
	}
	if err := j.MarkQuiescent(t.Context(), wrong); err == nil {
		t.Fatal("forged nonce marked cleanup complete")
	}
	if err := j.MarkQuiescent(t.Context(), scope); err != nil {
		t.Fatal(err)
	}
	if fresh, err := j.reserveBudget(t.Context(), gen, "controller", "controller2", time.Now()); err != nil || !fresh {
		t.Fatalf("controller after cleanup = %v, %v", fresh, err)
	}
	records, err := j.scopes(t.Context())
	if err != nil || len(records) != 1 || records[0].Phase != "quiescent" || records[0].CleanupEvidence == "" || !records[0].Deadline.Equal(deadline) {
		t.Fatalf("cleanup evidence or original deadline missing: %+v, %v", records, err)
	}
}

func testManagedJournalBudgets(t *testing.T) {
	t.Helper()
	root, a, store := journalFixture(t)
	a.Runtime.MaxModelCalls = 1
	a.Recovery.MaxSupervisorStarts = 2
	insertJournalFixture(t, store, a)
	j := openJournalFixture(t, root, a.ObjectiveID)
	gen, _, err := j.beginGeneration(t.Context(), "supervisor1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for n, wantFresh := range []bool{true, false} {
		fresh, err := j.reserveBudget(t.Context(), gen, "model", "call1", time.Now())
		if err != nil || fresh != wantFresh {
			t.Fatalf("reservation %d = %v, %v", n, fresh, err)
		}
	}
	if _, err := j.reserveBudget(t.Context(), gen, "model", "call2", time.Now()); err == nil {
		t.Fatal("exhausted model budget reset")
	}
	if got, fresh, err := j.beginGeneration(t.Context(), "supervisor1", time.Now()); err != nil || fresh || got != gen {
		t.Fatalf("duplicate supervisor spent another start = %d, %v, %v", got, fresh, err)
	}
	gen, _, err = j.beginGeneration(t.Context(), "supervisor2", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.reserveBudget(t.Context(), gen, "model", "call1", time.Now()); err == nil {
		t.Fatal("old reservation token was rebound to new generation")
	}
	if _, _, err := j.beginGeneration(t.Context(), "supervisor3", time.Now()); err == nil {
		t.Fatal("supervisor start limit bypassed")
	}
	if err := j.setDesired(t.Context(), gen, "stopping", "budget exhausted", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := j.setDesired(t.Context(), gen, "stopped", "budget exhausted", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if _, err := j.reserveBudget(t.Context(), gen, "host", "late-host", time.Now()); err == nil {
		t.Fatal("stopped supervision admitted work")
	}
	if err := j.setDesired(t.Context(), gen, "running", "", time.Time{}); err == nil {
		t.Fatal("terminal supervision reopened")
	}
}

func testManagedJournalTransactions(t *testing.T) {
	t.Helper()
	root, a, store := journalFixture(t)
	insertJournalFixture(t, store, a)
	j := openJournalFixture(t, root, a.ObjectiveID)
	other := openJournalFixture(t, root, a.ObjectiveID)
	gen, _, err := j.beginGeneration(t.Context(), "supervisor1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	fresh := make(chan bool, 2)
	errs := make(chan error, 2)
	for _, journal := range []*managedJournal{j, other} {
		wg.Go(func() {
			isNew, err := journal.reserveBudget(t.Context(), gen, "host", "same-call", time.Now())
			fresh <- isNew
			errs <- err
		})
	}
	wg.Wait()
	if err1, err2 := <-errs, <-errs; err1 != nil || err2 != nil {
		t.Fatalf("parallel reservation errors: %v, %v", err1, err2)
	}
	if first, second := <-fresh, <-fresh; first == second {
		t.Fatalf("same call reserved twice or never: %v, %v", first, second)
	}
	if _, err := store.conn.ExecContext(t.Context(), "CREATE TRIGGER reject_managed_event BEFORE INSERT ON managed_events BEGIN SELECT RAISE(ABORT, 'injected event failure'); END"); err != nil {
		t.Fatal(err)
	}
	if _, err := j.reserveBudget(t.Context(), gen, "host", "rollback-call", time.Now()); err == nil {
		t.Fatal("reservation survived failed audit event")
	}
	var count int
	if err := store.conn.QueryRowContext(t.Context(), "SELECT count(*) FROM managed_reservations WHERE token='rollback-call'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("reservation was not rolled back: %d, %v", count, err)
	}
	if _, err := store.load(t.Context()); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("journal wrote delivery snapshot: %v", err)
	}
}

func testManagedJournalMissing(t *testing.T) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "absent")
	if _, err := OpenScopeAuthority(t.Context(), root, "objective"); err == nil {
		t.Fatal("missing journal opened")
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("authority created missing state: %v", err)
	}
	if _, err := OpenScopeAuthority(context.Background(), t.TempDir(), "../escape"); err == nil {
		t.Fatal("unsafe objective ID accepted")
	}
}

func testManagedJournalObserve(t *testing.T) {
	t.Helper()
	root, a, store := journalFixture(t)
	insertJournalFixture(t, store, a)
	j := openJournalFixture(t, root, a.ObjectiveID)
	gen, _, err := j.beginGeneration(t.Context(), "supervisor1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	scope := journalScope("d", gen)
	deadline := time.Now().Add(time.Minute)
	if _, err := j.Observe(t.Context(), scope); err == nil {
		t.Fatal("unknown scope observation returned empty success")
	}
	if _, err := j.reserveScope(t.Context(), scope, deadline, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := j.RecordHello(t.Context(), scope, 421, 420); err != nil {
		t.Fatal(err)
	}
	if _, _, err := j.beginGeneration(t.Context(), "supervisor2", time.Now()); err != nil {
		t.Fatal(err)
	}
	authority, err := OpenScopeAuthority(t.Context(), root, a.ObjectiveID)
	if err != nil {
		t.Fatal(err)
	}
	defer authority.Close()
	got, err := authority.Observe(t.Context(), scope)
	if err != nil || got.Phase != "revoked" || got.PID != 421 || got.PGID != 420 || !got.Deadline.Equal(deadline) {
		t.Fatalf("recovered authenticated ownership = %+v, %v", got, err)
	}
	wrong := scope
	wrong.Name += "forged"
	if _, err := authority.Observe(t.Context(), wrong); err == nil {
		t.Fatal("mismatched exact identity observed another scope")
	}
	if granted, err := authority.Granted(t.Context(), scope); err != nil || granted {
		t.Fatalf("recovery observation re-granted old work = %v, %v", granted, err)
	}
}

func testManagedJournalBounds(t *testing.T) {
	t.Helper()
	root, a, store := journalFixture(t)
	insertJournalFixture(t, store, a)
	j := openJournalFixture(t, root, a.ObjectiveID)
	var pages, pageSize int64
	if err := j.conn.QueryRowContext(t.Context(), "PRAGMA max_page_count").Scan(&pages); err != nil {
		t.Fatal(err)
	}
	if err := j.conn.QueryRowContext(t.Context(), "PRAGMA page_size").Scan(&pageSize); err != nil || pages*pageSize > 64<<20 {
		t.Fatalf("journal database is not bounded to 64 MiB: pages=%d size=%d err=%v", pages, pageSize, err)
	}
	now := time.Now()
	gen, _, err := j.beginGeneration(t.Context(), "supervisor1", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.setDesired(t.Context(), gen, "running", "restart delay", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := j.reserveBudget(t.Context(), gen, "controller", "early-controller", now); err == nil {
		t.Fatal("controller reservation bypassed durable restart backoff")
	}
	if fresh, err := j.reserveBudget(t.Context(), gen, "controller", "eligible-controller", now.Add(2*time.Second)); err != nil || !fresh {
		t.Fatalf("eligible controller = %v, %v", fresh, err)
	}
	scope := journalScope("e", gen)
	deadline := now.Add(time.Minute)
	if _, err := j.reserveScope(t.Context(), scope, deadline, now); err != nil {
		t.Fatal(err)
	}
	var before int
	if err := j.conn.QueryRowContext(t.Context(), "SELECT count(*) FROM managed_events").Scan(&before); err != nil {
		t.Fatal(err)
	}
	for range 20 {
		if fresh, err := j.reserveScope(t.Context(), scope, deadline, now); err != nil || fresh {
			t.Fatalf("idempotent exact scope reservation = %v, %v", fresh, err)
		}
		if _, err := j.reserveScope(t.Context(), scope, deadline.Add(time.Second), now); err == nil {
			t.Fatal("reserved scope deadline extended")
		}
	}
	var after int
	if err := j.conn.QueryRowContext(t.Context(), "SELECT count(*) FROM managed_events").Scan(&after); err != nil || after != before {
		t.Fatalf("repeated/refused operation grew audit history: before=%d after=%d err=%v", before, after, err)
	}
}

func testManagedJournalObserver(t *testing.T) {
	t.Helper()
	root, a, store := journalFixture(t)
	now := time.Now().UTC()
	a.Deadline = now.Add(-time.Second)
	a.HardStop = a.Deadline.Add(time.Duration(a.Recovery.CleanupTimeoutSeconds+a.Recovery.ReconciliationSeconds) * time.Second)
	a.Recovery.MaxReconciliationInvocations = 1
	insertJournalFixture(t, store, a)
	j := openJournalFixture(t, root, a.ObjectiveID)
	gen, _, err := j.beginGeneration(t.Context(), "late-supervisor", now)
	if err != nil {
		t.Fatal(err)
	}
	scope := journalScope("f", gen)
	scope.Kind = "observer"
	scope.Name = strings.Replace(scope.Name, ".host.", ".observer.", 1)
	deadline := a.HardStop.Add(-time.Second)
	if _, err := j.reserveScope(t.Context(), scope, deadline, now); err == nil {
		t.Fatal("late observer launched without a reconciliation reservation")
	}
	if fresh, err := j.reserveBudget(t.Context(), gen, "reconciliation", scope.ID, now); err != nil || !fresh {
		t.Fatalf("bounded post-work observation reservation = %v, %v", fresh, err)
	}
	if _, err := j.reserveScope(t.Context(), scope, deadline, now); err != nil {
		t.Fatalf("reserved observer rejected after work deadline: %v", err)
	}
	if err := j.RecordHello(t.Context(), scope, 501, 500); err != nil {
		t.Fatal(err)
	}
	if err := j.MarkQuiescent(t.Context(), scope); err != nil {
		t.Fatal(err)
	}
	if _, err := j.reserveBudget(t.Context(), gen, "reconciliation", "extra-observer", now); err == nil {
		t.Fatal("exhausted reconciliation allowance reset")
	}
	if _, err := j.reserveScope(t.Context(), journalScope("a", gen), deadline, now); err == nil {
		t.Fatal("ordinary work admitted after original work deadline")
	}
	var ordinary int
	if err := j.conn.QueryRowContext(t.Context(), "SELECT count(*) FROM managed_reservations WHERE kind IN ('host','controller')").Scan(&ordinary); err != nil || ordinary != 0 {
		t.Fatalf("reconciliation spent controller/host work allowance = %d, %v", ordinary, err)
	}
}

func testManagedJournalEpochs(t *testing.T) {
	t.Helper()
	root, a, store := journalFixture(t)
	a.Recovery.MaxSupervisorStarts = 1
	a.Recovery.MaxControllerStarts = 2
	a.Recovery.MaxReconciliationInvocations = 1
	a.Recovery.MaxCleanupInvocations = 1
	insertJournalFixture(t, store, a)
	j := openJournalFixture(t, root, a.ObjectiveID)
	gen, _, err := j.beginGeneration(t.Context(), "one-supervisor", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.reserveBudget(t.Context(), gen, "controller", "controller1", time.Now()); err != nil {
		t.Fatal(err)
	}
	old := journalScope("a", gen)
	if _, err := j.reserveScope(t.Context(), old, time.Now().Add(time.Minute), time.Now()); err != nil {
		t.Fatal(err)
	}
	gen, fresh, err := j.advanceEpoch(t.Context(), "one-supervisor", "retry-epoch", time.Now())
	if err != nil || !fresh || gen != 2 {
		t.Fatalf("same-supervisor controller epoch = %d, %v, %v", gen, fresh, err)
	}
	if _, err := j.reserveBudget(t.Context(), gen, "controller", "controller2", time.Now()); err == nil {
		t.Fatal("revoked ungranted scope skipped positive cleanup")
	}
	if err := j.MarkQuiescent(t.Context(), old); err != nil {
		t.Fatal(err)
	}
	if _, err := j.reserveBudget(t.Context(), gen, "controller", "controller2", time.Now()); err != nil {
		t.Fatal(err)
	}
	if got, fresh, err := j.advanceEpoch(t.Context(), "one-supervisor", "retry-epoch", time.Now()); err != nil || fresh || got != gen {
		t.Fatalf("epoch token spent twice = %d, %v, %v", got, fresh, err)
	}
	if _, err := j.reserveBudget(t.Context(), gen, "controller", "controller3", time.Now()); err == nil {
		t.Fatal("controller starts exceeded frozen allowance")
	}
	for _, token := range []string{"epoch3", "epoch4", "cleanup-epoch"} {
		if _, _, err := j.advanceEpoch(t.Context(), "one-supervisor", token, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := j.advanceEpoch(t.Context(), "one-supervisor", "epoch5", time.Now()); err == nil {
		t.Fatal("ownership epochs grew beyond the frozen recovery/work bound")
	}
	var supervisors int
	if err := j.conn.QueryRowContext(t.Context(), "SELECT count(*) FROM managed_reservations WHERE kind='supervisor'").Scan(&supervisors); err != nil || supervisors != 1 {
		t.Fatalf("controller retries consumed supervisor starts = %d, %v", supervisors, err)
	}
	if _, _, err := j.advanceEpoch(t.Context(), "forged-supervisor", "forged-epoch", time.Now()); err == nil {
		t.Fatal("unreserved supervisor advanced ownership")
	}
}

func testManagedJournalSupervisorEpochCollision(t *testing.T) {
	t.Helper()
	root, a, store := journalFixture(t)
	insertJournalFixture(t, store, a)
	j := openJournalFixture(t, root, a.ObjectiveID)
	if _, _, err := j.beginGeneration(t.Context(), "supervisor1", time.Now()); err != nil {
		t.Fatal(err)
	}
	// An ordinary ownership token can collide with a later supervisor's first
	// epoch. That existing epoch must not authorize a change of supervisor.
	epochToken := contentHash([]byte("supervisor:supervisor2"))
	gen, fresh, err := j.advanceEpoch(t.Context(), "supervisor1", epochToken, time.Now())
	if err != nil || !fresh {
		t.Fatalf("reserve colliding ownership epoch = %d, %v, %v", gen, fresh, err)
	}
	scope := journalScope("a", gen)
	if _, err := j.reserveScope(t.Context(), scope, time.Now().Add(time.Minute), time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := j.RecordHello(t.Context(), scope, 601, 600); err != nil {
		t.Fatal(err)
	}
	before, err := j.supervision(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got, fresh, err := j.beginGeneration(t.Context(), "supervisor2", time.Now()); err == nil || fresh {
		t.Fatalf("new supervisor reused existing epoch: generation=%d fresh=%v err=%v", got, fresh, err)
	}
	after, err := j.supervision(t.Context())
	if err != nil || !reflect.DeepEqual(after, before) {
		t.Fatalf("rejected supervisor changed ownership or audit revision: before=%+v after=%+v err=%v", before, after, err)
	}
	var spent int
	if err := j.conn.QueryRowContext(t.Context(), "SELECT count(*) FROM managed_reservations WHERE kind='supervisor' AND token='supervisor2'").Scan(&spent); err != nil || spent != 0 {
		t.Fatalf("rejected supervisor consumed a start: count=%d err=%v", spent, err)
	}
	if granted, err := j.Granted(t.Context(), scope); err != nil || !granted {
		t.Fatalf("failed supervisor transition changed the current grant: granted=%v err=%v", granted, err)
	}
	if got, fresh, err := j.beginGeneration(t.Context(), "supervisor1", time.Now()); err != nil || fresh || got != gen {
		t.Fatalf("original supervisor lost its epoch: generation=%d fresh=%v err=%v", got, fresh, err)
	}
}
