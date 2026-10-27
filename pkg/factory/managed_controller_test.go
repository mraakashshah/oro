//nolint:testpackage // Exercises private journal transitions.
package factory

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func controllerFixture(t *testing.T) (*managedJournal, uint64) {
	t.Helper()
	return controllerFixtureWithLimit(t, 0)
}

func controllerFixtureWithLimit(t *testing.T, limit int) (*managedJournal, uint64) {
	t.Helper()
	root, spec, reservation := managedAdmissionFixture(t)
	if limit > 0 {
		spec.Recovery.MaxControllerStarts = limit
		reservation = reservationForIntendedSpec(t, reservation, spec)
	}
	if _, err := AdmitManaged(t.Context(), root, spec, reservation); err != nil {
		t.Fatal(err)
	}
	ref := ManagedRuntimeReference{Path: filepath.Join(root, spec.ID, "runtime", "runtime.json"), SHA256: strings.Repeat("a", 64)}
	if err := ActivateManaged(t.Context(), root, spec.ID, ref); err != nil {
		t.Fatal(err)
	}
	j, err := openManagedJournal(t.Context(), root, spec.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	gen, _, err := j.beginGeneration(t.Context(), "supervisor", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return j, gen
}

func assertControllerDeniedUnchanged(t *testing.T, j *managedJournal, generation uint64, reason string) {
	t.Helper()
	snapshot := func() string {
		var v string
		err := j.conn.QueryRowContext(t.Context(), `SELECT (SELECT revision FROM managed_supervision)||':'||(SELECT count(*) FROM managed_events)||':'||(SELECT count(*) FROM managed_scopes)||':'||(SELECT count(*) FROM managed_reservations WHERE kind='controller')`).Scan(&v)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	before := snapshot()
	scope, err := reserveManagedController(t.Context(), j, "supervisor", generation)
	if err == nil || !strings.Contains(err.Error(), reason) || scope != (ScopeHandle{}) {
		t.Fatalf("expected %q and zero scope, got %+v %v", reason, scope, err)
	}
	if after := snapshot(); after != before {
		t.Fatalf("failed reservation changed state: %s -> %s", before, after)
	}
}

func TestManagedControllerReservation(t *testing.T) {
	t.Run("atomic identity and concurrency", func(t *testing.T) {
		j, gen := controllerFixture(t)
		var wg sync.WaitGroup
		journals := make([]*managedJournal, 8)
		for i := range journals {
			var err error
			journals[i], err = openManagedJournal(t.Context(), j.admission.Reservation.StateRoot, j.admission.ObjectiveID)
			if err != nil {
				t.Fatal(err)
			}
			defer journals[i].Close()
		}
		results := make(chan ScopeHandle, 8)
		for _, contender := range journals {
			wg.Add(1)
			go func() {
				defer wg.Done()
				scope, err := reserveManagedController(t.Context(), contender, "supervisor", gen)
				if err == nil {
					results <- scope
				}
			}()
		}
		wg.Wait()
		close(results)
		var scopes []ScopeHandle
		for s := range results {
			scopes = append(scopes, s)
		}
		if len(scopes) != 1 {
			t.Fatalf("successful reservations: %d", len(scopes))
		}
		s := scopes[0]
		if s.Kind != "controller" || s.Backend != "launchd" || s.Name != "dev.getoro.factory."+cleanupObjectiveHash(j.admission)+".controller."+s.ID+"."+s.Nonce || len(s.ID) != 32 || len(s.Nonce) != 32 {
			t.Fatalf("bad scope: %+v", s)
		}
		record, err := loadManagedScope(t.Context(), j.conn, s)
		if err != nil || !record.Deadline.Equal(j.admission.Deadline) {
			t.Fatalf("scope deadline: %+v %v", record, err)
		}
		var n int
		if err := j.conn.QueryRowContext(t.Context(), "SELECT count(*) FROM managed_reservations WHERE kind='controller' AND token=? AND generation=?", s.ID, gen).Scan(&n); err != nil || n != 1 {
			t.Fatalf("token: %d %v", n, err)
		}
	})
	for _, tc := range []struct{ name, query string }{
		{"activation", "UPDATE managed_activation SET body='{}'"},
		{"event", "DELETE FROM managed_events WHERE revision=2"},
		{"supervisor", "DELETE FROM managed_reservations WHERE kind='supervisor'"},
		{"backoff", `UPDATE managed_supervision SET body=json_set(body,'$.NextStart','2999-01-01T00:00:00Z')`},
		{"scope rollback", `CREATE TRIGGER fail_scope BEFORE INSERT ON managed_scopes BEGIN SELECT RAISE(ABORT,'injected'); END`},
		{"missing activation", "DELETE FROM managed_activation"},
		{"event rollback", `CREATE TRIGGER fail_controller BEFORE INSERT ON managed_events BEGIN SELECT RAISE(ABORT,'injected'); END`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j, gen := controllerFixture(t)
			if _, err := j.conn.ExecContext(t.Context(), tc.query); err != nil {
				t.Fatal(err)
			}
			var beforeEvents, beforeRevision int
			if err := j.conn.QueryRowContext(t.Context(), "SELECT count(*) FROM managed_events").Scan(&beforeEvents); err != nil {
				t.Fatal(err)
			}
			if err := j.conn.QueryRowContext(t.Context(), "SELECT revision FROM managed_supervision WHERE id=1").Scan(&beforeRevision); err != nil {
				t.Fatal(err)
			}
			if _, err := reserveManagedController(t.Context(), j, "supervisor", gen); err == nil {
				t.Fatal("invalid authority accepted")
			}
			var n int
			if err := j.conn.QueryRowContext(t.Context(), "SELECT count(*) FROM managed_reservations WHERE kind='controller'").Scan(&n); err != nil || n != 0 {
				t.Fatalf("rollback: %d %v", n, err)
			}
			var scopes, events, revision int
			if err := j.conn.QueryRowContext(t.Context(), "SELECT count(*) FROM managed_scopes").Scan(&scopes); err != nil {
				t.Fatal(err)
			}
			if err := j.conn.QueryRowContext(t.Context(), "SELECT count(*) FROM managed_events").Scan(&events); err != nil {
				t.Fatal(err)
			}
			if err := j.conn.QueryRowContext(t.Context(), "SELECT revision FROM managed_supervision WHERE id=1").Scan(&revision); err != nil {
				t.Fatal(err)
			}
			if scopes != 0 || events != beforeEvents || revision != beforeRevision {
				t.Fatalf("partial transition: scopes=%d events=%d revision=%d", scopes, events, revision)
			}
		})
	}
	t.Run("pending activation", func(t *testing.T) {
		root, spec, _, _ := activationFixture(t)
		j, err := openManagedJournal(t.Context(), root, spec.ID)
		if err != nil {
			t.Fatal(err)
		}
		defer j.Close()
		gen, _, err := j.beginGeneration(t.Context(), "supervisor", time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := reserveManagedController(t.Context(), j, "supervisor", gen); err == nil {
			t.Fatal("pending activation accepted")
		}
	})
	for _, desired := range []string{"stopping", "stopped"} {
		t.Run(desired, func(t *testing.T) {
			j, gen := controllerFixture(t)
			if err := j.setDesired(t.Context(), gen, "stopping", "test", time.Time{}); err != nil {
				t.Fatal(err)
			}
			if desired == "stopped" {
				if err := j.setDesired(t.Context(), gen, "stopped", "test", time.Time{}); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := reserveManagedController(t.Context(), j, "supervisor", gen); err == nil {
				t.Fatal("non-running supervision accepted")
			}
		})
	}
	t.Run("expired original deadline", func(t *testing.T) {
		root, spec, reservation := managedAdmissionFixture(t)
		spec.TimeoutSeconds = 2
		spec.StepTimeoutSeconds = 1
		reservation = reservationForIntendedSpec(t, reservation, spec)
		if _, err := AdmitManaged(t.Context(), root, spec, reservation); err != nil {
			t.Fatal(err)
		}
		ref := ManagedRuntimeReference{Path: filepath.Join(root, spec.ID, "runtime", "runtime.json"), SHA256: strings.Repeat("a", 64)}
		if err := ActivateManaged(t.Context(), root, spec.ID, ref); err != nil {
			t.Fatal(err)
		}
		j, err := openManagedJournal(t.Context(), root, spec.ID)
		if err != nil {
			t.Fatal(err)
		}
		defer j.Close()
		gen, _, err := j.beginGeneration(t.Context(), "supervisor", time.Now())
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Until(j.admission.Deadline) + 30*time.Millisecond)
		if _, err := reserveManagedController(t.Context(), j, "supervisor", gen); err == nil {
			t.Fatal("expired work deadline accepted")
		}
	})
	t.Run("malformed quiescent scope", func(t *testing.T) {
		j, gen := controllerFixture(t)
		scope := journalScope("c", gen)
		if _, err := j.reserveScope(t.Context(), scope, j.admission.Deadline, time.Now()); err != nil {
			t.Fatal(err)
		}
		if err := j.MarkQuiescent(t.Context(), scope); err != nil {
			t.Fatal(err)
		}
		if _, err := j.conn.ExecContext(t.Context(), "UPDATE managed_scopes SET body='{}' WHERE id=?", scope.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := reserveManagedController(t.Context(), j, "supervisor", gen); err == nil {
			t.Fatal("malformed quiescent scope accepted")
		}
	})
	t.Run("active cleanup with quiescent ordinary scope", func(t *testing.T) {
		j, gen := controllerFixture(t)
		scope := journalScope("c", gen)
		if _, err := j.reserveScope(t.Context(), scope, j.admission.Deadline, time.Now()); err != nil {
			t.Fatal(err)
		}
		if err := j.MarkQuiescent(t.Context(), scope); err != nil {
			t.Fatal(err)
		}
		cleanup := journalScope("d", gen)
		cleanup.Kind = "cleanup"
		err := j.mutate(t.Context(), "test-cleanup", cleanup.ID, func(tx *sql.Tx, _ *managedSupervision) (bool, error) {
			return j.insertScope(t.Context(), tx, cleanup, j.admission.Deadline)
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := reserveManagedController(t.Context(), j, "supervisor", gen); err == nil {
			t.Fatal("active cleanup accepted")
		}
	})

	t.Run("wrong authority", func(t *testing.T) {
		j, gen := controllerFixture(t)
		for _, v := range []struct {
			token      string
			generation uint64
		}{{"other", gen}, {"supervisor", gen + 1}, {"supervisor", gen - 1}} {
			if _, err := reserveManagedController(t.Context(), j, v.token, v.generation); err == nil {
				t.Fatal("wrong authority accepted")
			}
		}
	})
	t.Run("advanced generation", func(t *testing.T) {
		j, gen := controllerFixture(t)
		next, _, err := j.advanceEpoch(t.Context(), "supervisor", strings.Repeat("a", 32), time.Now())
		if err != nil || next <= gen {
			t.Fatalf("advance: %d %v", next, err)
		}
		if _, err := reserveManagedController(t.Context(), j, "supervisor", next); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("retirement and reopen", func(t *testing.T) {
		j, gen := controllerFixture(t)
		first, err := reserveManagedController(t.Context(), j, "supervisor", gen)
		if err != nil {
			t.Fatal(err)
		}
		if err := j.MarkQuiescent(t.Context(), first); err != nil {
			t.Fatal(err)
		}
		next, _, err := j.advanceEpoch(t.Context(), "supervisor", strings.Repeat("b", 32), time.Now())
		if err != nil {
			t.Fatal(err)
		}
		reopened, err := openManagedJournal(t.Context(), j.admission.Reservation.StateRoot, j.admission.ObjectiveID)
		if err != nil {
			t.Fatal(err)
		}
		defer reopened.Close()
		second, err := reserveManagedController(t.Context(), reopened, "supervisor", next)
		if err != nil {
			t.Fatal(err)
		}
		if second.ID == first.ID {
			t.Fatal("reused identity")
		}
		var controllers, supervisors int
		if err := reopened.conn.QueryRowContext(t.Context(), "SELECT count(*) FROM managed_reservations WHERE kind='controller'").Scan(&controllers); err != nil {
			t.Fatal(err)
		}
		if err := reopened.conn.QueryRowContext(t.Context(), "SELECT count(*) FROM managed_reservations WHERE kind='supervisor'").Scan(&supervisors); err != nil {
			t.Fatal(err)
		}
		if controllers != 2 || supervisors != 1 {
			t.Fatalf("unexpected counts: controllers=%d supervisors=%d", controllers, supervisors)
		}
		for _, scope := range []ScopeHandle{first, second} {
			record, err := loadManagedScope(t.Context(), reopened.conn, scope)
			if err != nil || !record.Deadline.Equal(j.admission.Deadline) {
				t.Fatalf("retained deadline: %+v %v", record, err)
			}
		}
	})
	t.Run("budget", func(t *testing.T) {
		j, gen := controllerFixtureWithLimit(t, 1)
		first, err := reserveManagedController(t.Context(), j, "supervisor", gen)
		if err != nil {
			t.Fatal(err)
		}
		if err := j.MarkQuiescent(t.Context(), first); err != nil {
			t.Fatal(err)
		}
		if _, err := reserveManagedController(t.Context(), j, "supervisor", gen); err == nil {
			t.Fatal("exhausted budget accepted")
		}
	})
	t.Run("nil journal", func(t *testing.T) {
		if _, err := reserveManagedController(t.Context(), nil, "supervisor", 1); err == nil {
			t.Fatal("nil journal accepted")
		}
	})

	t.Run("scope cap", func(t *testing.T) {
		j, gen := controllerFixture(t)
		limit, err := journalRecordLimit(j.admission)
		if err != nil {
			t.Fatal(err)
		}
		tx, err := j.conn.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		for i := int64(0); i < limit; i++ {
			id := fmt.Sprintf("%032x", i+1)
			nonce := fmt.Sprintf("%032x", i+10000)
			scope := ScopeHandle{ID: id, Nonce: nonce, Kind: "host", Backend: "launchd", Name: "scope." + id, Generation: gen}
			if _, err := j.insertScope(t.Context(), tx, scope, j.admission.Deadline); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := tx.ExecContext(t.Context(), "UPDATE managed_scopes SET phase='quiescent',cleanup='positive retirement'"); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		assertControllerDeniedUnchanged(t, j, gen, "managed scope budget exhausted")
	})
	t.Run("event cap", func(t *testing.T) {
		j, gen := controllerFixture(t)
		padManagedEventsToCap(t, j)
		assertControllerDeniedUnchanged(t, j, gen, "managed event budget exhausted")
	})
	for _, phase := range []string{"granted", "revoked"} {
		t.Run(phase+" old scope", func(t *testing.T) {
			j, gen := controllerFixture(t)
			scope := journalScope("e", gen)
			if _, err := j.reserveScope(t.Context(), scope, j.admission.Deadline, time.Now()); err != nil {
				t.Fatal(err)
			}
			if err := j.RecordHello(t.Context(), scope, 101, 101); err != nil {
				t.Fatal(err)
			}
			if phase == "revoked" {
				var err error
				gen, _, err = j.advanceEpoch(t.Context(), "supervisor", strings.Repeat("c", 32), time.Now())
				if err != nil {
					t.Fatal(err)
				}
			}
			assertControllerDeniedUnchanged(t, j, gen, "positive scope quiescence")
		})
	}

	t.Run("commit rollback", func(t *testing.T) {
		j, gen := controllerFixture(t)
		db, err := sql.Open("sqlite", filepath.Join(j.admission.Reservation.StateRoot, j.admission.ObjectiveID, "state.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		reader, err := db.BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
		if err != nil {
			t.Fatal(err)
		}
		var revision int
		if err := reader.QueryRowContext(t.Context(), "SELECT revision FROM snapshots WHERE id=1").Scan(&revision); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
		defer cancel()
		scope, err := reserveManagedController(ctx, j, "supervisor", gen)
		if err == nil || !strings.Contains(err.Error(), "commit managed transition") || scope != (ScopeHandle{}) {
			t.Fatalf("expected commit failure and zero scope: %+v %v", scope, err)
		}
		if err := reader.Rollback(); err != nil {
			t.Fatal(err)
		}
		var n int
		if err := j.conn.QueryRowContext(t.Context(), "SELECT count(*) FROM managed_reservations WHERE kind='controller'").Scan(&n); err != nil || n != 0 {
			t.Fatalf("rollback: %d %v", n, err)
		}
		var scopes, events int
		if err := j.conn.QueryRowContext(t.Context(), "SELECT count(*) FROM managed_scopes").Scan(&scopes); err != nil {
			t.Fatal(err)
		}
		if err := j.conn.QueryRowContext(t.Context(), "SELECT count(*) FROM managed_events").Scan(&events); err != nil {
			t.Fatal(err)
		}
		if err := j.conn.QueryRowContext(t.Context(), "SELECT revision FROM managed_supervision").Scan(&revision); err != nil {
			t.Fatal(err)
		}
		if scopes != 0 || events != 2 || revision != 3 {
			t.Fatalf("partial commit: scopes=%d events=%d revision=%d", scopes, events, revision)
		}
	})
}
