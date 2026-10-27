package factory //nolint:testpackage // Exercises the private journal transaction boundary.

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

func TestManagedTransactionContention(t *testing.T) {
	t.Run("busy then success", func(t *testing.T) {
		root, admission, store := journalFixture(t)
		insertJournalFixture(t, store, admission)
		j := openJournalFixture(t, root, admission.ObjectiveID)
		tx, err := store.conn.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		var callbacks atomic.Int32
		done := make(chan error, 1)
		go func() {
			done <- j.mutate(ctx, "contention-test", "once", func(tx *sql.Tx, _ *managedSupervision) (bool, error) {
				callbacks.Add(1)
				return reserveManagedToken(ctx, tx, "host", "only-once", 1, 10)
			})
		}()
		timer := time.NewTimer(1300 * time.Millisecond)
		<-timer.C
		if err := tx.Rollback(); err != nil {
			t.Fatal(err)
		}
		if err := <-done; err != nil {
			t.Fatalf("transient writer contention: %v", err)
		}
		if callbacks.Load() != 1 {
			t.Fatalf("callback count = %d", callbacks.Load())
		}
		var revision, tokens int
		if err := j.conn.QueryRowContext(t.Context(), "SELECT (SELECT revision FROM managed_supervision), (SELECT count(*) FROM managed_reservations WHERE kind='host' AND token='only-once')").Scan(&revision, &tokens); err != nil || revision != 2 || tokens != 1 {
			t.Fatalf("revision=%d tokens=%d: %v", revision, tokens, err)
		}
		var events int
		if err := j.conn.QueryRowContext(t.Context(), "SELECT count(*) FROM managed_events WHERE kind='contention-test' AND ref='once'").Scan(&events); err != nil || events != 1 {
			t.Fatalf("events = %d: %v", events, err)
		}
	})
	t.Run("local mutex cancellation", func(t *testing.T) {
		root, admission, store := journalFixture(t)
		insertJournalFixture(t, store, admission)
		j := openJournalFixture(t, root, admission.ObjectiveID)
		j.mu.Lock()
		ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
		defer cancel()
		done := make(chan error, 1)
		go func() {
			done <- j.mutate(ctx, "", "", func(*sql.Tx, *managedSupervision) (bool, error) { t.Error("canceled callback ran"); return false, nil })
		}()
		select {
		case err := <-done:
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("cancellation: %v", err)
			}
		case <-time.After(500 * time.Millisecond):
			t.Error("local mutex ignored cancellation")
		}
		j.mu.Unlock()
	})
	t.Run("writer deadline", func(t *testing.T) {
		root, a, store := journalFixture(t)
		insertJournalFixture(t, store, a)
		j := openJournalFixture(t, root, a.ObjectiveID)
		tx, err := store.conn.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
		defer cancel()
		start := time.Now()
		calls := 0
		err = j.mutate(ctx, "", "", func(*sql.Tx, *managedSupervision) (bool, error) { calls++; return false, nil })
		elapsed := time.Since(start)
		if !errors.Is(err, context.DeadlineExceeded) || calls != 0 || elapsed > 1500*time.Millisecond {
			t.Fatalf("deadline error=%v calls=%d elapsed=%s", err, calls, elapsed)
		}
		t.Logf("driver returned after %s with 150ms deadline", elapsed)
	})
	t.Run("close during busy", func(t *testing.T) {
		root, a, store := journalFixture(t)
		insertJournalFixture(t, store, a)
		j := openJournalFixture(t, root, a.ObjectiveID)
		tx, err := store.conn.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		var calls atomic.Int32
		done := make(chan error, 1)
		go func() {
			done <- j.mutate(ctx, "", "", func(*sql.Tx, *managedSupervision) (bool, error) { calls.Add(1); return false, nil })
		}()
		time.Sleep(150 * time.Millisecond)
		closed := make(chan error, 1)
		go func() { closed <- j.Close() }()
		if err := <-closed; err != nil {
			t.Fatal(err)
		}
		if err := <-done; err == nil || calls.Load() != 0 {
			t.Fatalf("closed: %v callbacks=%d", err, calls.Load())
		}
	})
	t.Run("callback error once", func(t *testing.T) {
		root, a, store := journalFixture(t)
		insertJournalFixture(t, store, a)
		j := openJournalFixture(t, root, a.ObjectiveID)
		calls := 0
		marker := errors.New("callback rejected")
		err := j.mutate(t.Context(), "callback", "once", func(*sql.Tx, *managedSupervision) (bool, error) { calls++; return false, marker })
		if !errors.Is(err, marker) || calls != 1 {
			t.Fatalf("callback %d: %v", calls, err)
		}
	})
	t.Run("commit error once", func(t *testing.T) {
		root, a, store := journalFixture(t)
		insertJournalFixture(t, store, a)
		j := openJournalFixture(t, root, a.ObjectiveID)
		reader := pinManagedReader(t, filepath.Join(root, a.ObjectiveID), "SELECT count(*) FROM managed_events")
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
		defer cancel()
		calls := 0
		err := j.mutate(ctx, "commit-test", "once", func(*sql.Tx, *managedSupervision) (bool, error) { calls++; return true, nil })
		if err == nil || !strings.Contains(err.Error(), "commit managed transition") || calls != 1 {
			t.Fatalf("commit %d: %v", calls, err)
		}
		if err := reader.Rollback(); err != nil {
			t.Fatal(err)
		}
		var n int
		if err := j.conn.QueryRowContext(t.Context(), "SELECT count(*) FROM managed_events WHERE kind='commit-test'").Scan(&n); err != nil || n != 0 {
			t.Fatalf("commit persisted %d: %v", n, err)
		}
	})
	t.Run("stop while waiting", func(t *testing.T) {
		root, a, store := journalFixture(t)
		insertJournalFixture(t, store, a)
		j := openJournalFixture(t, root, a.ObjectiveID)
		generation, _, err := j.beginGeneration(t.Context(), "supervisor", time.Now())
		if err != nil {
			t.Fatal(err)
		}
		stop := openJournalFixture(t, root, a.ObjectiveID)
		tx, err := store.conn.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		calls := 0
		done := make(chan error, 1)
		go func() {
			done <- j.mutate(ctx, "test-work", "once", func(_ *sql.Tx, state *managedSupervision) (bool, error) {
				calls++
				if err := activeManagedGeneration(state, generation, time.Now(), a.Deadline); err != nil {
					return false, err
				}
				return true, nil
			})
		}()
		time.Sleep(1100 * time.Millisecond)
		j.mu.Lock()
		if err := tx.Rollback(); err != nil {
			j.mu.Unlock()
			t.Fatal(err)
		}
		if err := stop.setDesired(t.Context(), generation, "stopping", "test", time.Time{}); err != nil {
			j.mu.Unlock()
			t.Fatal(err)
		}
		j.mu.Unlock()
		if err := <-done; err == nil || calls != 1 {
			t.Fatalf("stopped admission calls=%d err=%v", calls, err)
		}
		var n int
		if err := j.conn.QueryRowContext(t.Context(), "SELECT count(*) FROM managed_events WHERE kind='test-work'").Scan(&n); err != nil || n != 0 {
			t.Fatalf("work persisted %d: %v", n, err)
		}
	})
	t.Run("historical metadata after hard stop", func(t *testing.T) {
		root, spec, reservation := managedAdmissionFixture(t)
		spec.TimeoutSeconds = 5
		spec.StepTimeoutSeconds = 1
		spec.Recovery.CleanupTimeoutSeconds = 1
		spec.Recovery.ReconciliationSeconds = 1
		reservation = reservationForIntendedSpec(t, reservation, spec)
		if _, err := AdmitManaged(t.Context(), root, spec, reservation); err != nil {
			t.Fatal(err)
		}
		ref := ManagedRuntimeReference{Path: filepath.Join(root, spec.ID, "runtime", "runtime.json"), SHA256: strings.Repeat("a", 64)}
		if err := ActivateManaged(t.Context(), root, spec.ID, ref); err != nil {
			t.Fatal(err)
		}
		before, err := ReadManagedAdmission(t.Context(), root, spec.ID)
		if err != nil {
			t.Fatal(err)
		}
		timer := time.NewTimer(time.Until(before.HardStop) + 20*time.Millisecond)
		<-timer.C
		after, err := ReadManagedAdmission(t.Context(), root, spec.ID)
		if err != nil || after != before {
			t.Fatalf("historical admission changed: %v", err)
		}
		got, err := ReadManagedRuntimeReference(t.Context(), root, spec.ID)
		if err != nil || got != ref {
			t.Fatalf("historical runtime: %+v %v", got, err)
		}
		j := openJournalFixture(t, root, spec.ID)
		if _, _, err := j.beginGeneration(t.Context(), "too-late", time.Now()); err == nil {
			t.Fatal("expired generation admitted")
		}
	})
	t.Run("locked is not retried", func(t *testing.T) {
		query := url.Values{"cache": {"shared"}, "_txlock": {"immediate"}, "_pragma": {"busy_timeout(1000)"}}
		dsn := (&url.URL{Scheme: "file", Path: filepath.Join(t.TempDir(), "locked.db"), RawQuery: query.Encode()}).String()
		db, err := sql.Open("sqlite", dsn)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		db.SetMaxOpenConns(2)
		writer, err := db.Conn(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer writer.Close()
		reader, err := db.Conn(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer reader.Close()
		if _, err := writer.ExecContext(t.Context(), "CREATE TABLE t (id INTEGER)"); err != nil {
			t.Fatal(err)
		}
		tx, err := writer.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		j := &managedJournal{db: db, conn: reader}
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		defer cancel()
		start := time.Now()
		got, release, err := j.beginTransition(ctx)
		if release != nil {
			defer release()
		}
		if got != nil {
			defer got.Rollback()
		}
		var sqliteErr *sqlite.Error
		if !errors.As(err, &sqliteErr) || sqliteErr.Code()&0xff != sqlite3.SQLITE_LOCKED || time.Since(start) > 500*time.Millisecond {
			t.Fatalf("locked result=%v elapsed=%s", err, time.Since(start))
		}
	})
}

func TestManagedTransactionCrossesHardStop(t *testing.T) {
	root, a, store := journalFixture(t)
	a.HardStop = time.Now().UTC().Add(1500 * time.Millisecond)
	a.Deadline = a.HardStop.Add(-time.Duration(a.Recovery.CleanupTimeoutSeconds+a.Recovery.ReconciliationSeconds) * time.Second)
	insertJournalFixture(t, store, a)
	j := openJournalFixture(t, root, a.ObjectiveID)
	tx, err := store.conn.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	now := time.Now()
	done := make(chan error, 1)
	go func() { _, _, err := j.beginGeneration(context.Background(), "late-supervisor", now); done <- err }()
	timer := time.NewTimer(time.Until(a.HardStop) + 100*time.Millisecond)
	<-timer.C
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err == nil {
		t.Fatal("supervisor reserved after hard stop using stale time")
	}
	var revision, reservations, scopes, events int
	if err := j.conn.QueryRowContext(t.Context(), "SELECT (SELECT revision FROM managed_supervision), (SELECT count(*) FROM managed_reservations), (SELECT count(*) FROM managed_scopes), (SELECT count(*) FROM managed_events)").Scan(&revision, &reservations, &scopes, &events); err != nil || revision != 1 || reservations != 0 || scopes != 0 || events != 0 {
		t.Fatalf("late side effects revision=%d reservations=%d scopes=%d events=%d: %v", revision, reservations, scopes, events, err)
	}
}

func TestManagedTransactionCrossesAuthorityDeadline(t *testing.T) {
	for _, kind := range []string{"epoch", "budget", "scope"} {
		t.Run(kind, func(t *testing.T) {
			root, a, store := journalFixture(t)
			if kind == "epoch" {
				a.HardStop = time.Now().UTC().Add(1500 * time.Millisecond)
				a.Deadline = a.HardStop.Add(-time.Duration(a.Recovery.CleanupTimeoutSeconds+a.Recovery.ReconciliationSeconds) * time.Second)
			} else {
				a.Deadline = time.Now().UTC().Add(1500 * time.Millisecond)
				a.HardStop = a.Deadline.Add(time.Duration(a.Recovery.CleanupTimeoutSeconds+a.Recovery.ReconciliationSeconds) * time.Second)
			}
			insertJournalFixture(t, store, a)
			j := openJournalFixture(t, root, a.ObjectiveID)
			gen, _, err := j.beginGeneration(t.Context(), "supervisor", time.Now())
			if err != nil {
				t.Fatal(err)
			}
			query := `SELECT (SELECT revision FROM managed_supervision), (SELECT count(*) FROM managed_reservations), (SELECT count(*) FROM managed_scopes), (SELECT count(*) FROM managed_events)`
			var before [4]int
			if err := j.conn.QueryRowContext(t.Context(), query).Scan(&before[0], &before[1], &before[2], &before[3]); err != nil {
				t.Fatal(err)
			}
			tx, err := store.conn.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			now := time.Now()
			done := make(chan error, 1)
			go func() {
				var err error
				switch kind {
				case "epoch":
					_, _, err = j.advanceEpoch(context.Background(), "supervisor", "new-epoch", now)
				case "budget":
					_, err = j.reserveBudget(context.Background(), gen, "host", "new-budget", now)
				case "scope":
					_, err = j.reserveScope(context.Background(), journalScope("f", gen), a.Deadline, now)
				}
				done <- err
			}()
			bound := a.Deadline
			if kind == "epoch" {
				bound = a.HardStop
			}
			timer := time.NewTimer(time.Until(bound) + 100*time.Millisecond)
			<-timer.C
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err == nil {
				t.Fatalf("%s accepted after deadline", kind)
			}
			var after [4]int
			if err := j.conn.QueryRowContext(t.Context(), query).Scan(&after[0], &after[1], &after[2], &after[3]); err != nil || after != before {
				t.Fatalf("%s changed durable state %v -> %v: %v", kind, before, after, err)
			}
		})
	}
}
