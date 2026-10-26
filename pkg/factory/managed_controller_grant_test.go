//nolint:testpackage // Exercises private managed authority.
package factory

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func grantFixture(t *testing.T) (*managedJournal, ScopeHandle) {
	t.Helper()
	j, gen := controllerFixture(t)
	scope, err := reserveManagedController(t.Context(), j, "supervisor", gen)
	if err != nil {
		t.Fatal(err)
	}
	return j, scope
}

func grantState(t *testing.T, j *managedJournal) string {
	t.Helper()
	var value string
	err := j.conn.QueryRowContext(t.Context(), `SELECT coalesce((SELECT hex(body) FROM managed_activation),'missing')||':'||(SELECT hex(body) FROM managed_supervision)||':'||(SELECT coalesce(group_concat(revision||kind||ref||hex(body),'|'),'') FROM managed_events)||':'||(SELECT coalesce(group_concat(kind||token||generation,'|'),'') FROM managed_reservations)||':'||(SELECT coalesce(group_concat(id||nonce||backend||name||hex(body)||phase||pid||pgid||cleanup,'|'),'') FROM managed_scopes)`).Scan(&value)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestManagedControllerGrant(t *testing.T) {
	t.Run("generic denied", func(t *testing.T) {
		j, gen := controllerFixture(t)
		scope := journalScope("a", gen)
		scope.Kind = "controller"
		before := grantState(t, j)
		if _, err := j.reserveScope(t.Context(), scope, j.admission.Deadline, time.Now()); err == nil {
			t.Fatal("generic controller scope accepted")
		}
		if grantState(t, j) != before {
			t.Fatal("generic denial mutated state")
		}
	})
	t.Run("valid replay and reopen", func(t *testing.T) {
		j, scope := grantFixture(t)
		if err := j.RecordHello(t.Context(), scope, 101, 101); err != nil {
			t.Fatal(err)
		}
		before := grantState(t, j)
		if err := j.RecordHello(t.Context(), scope, 101, 101); err != nil {
			t.Fatal(err)
		}
		if err := j.RecordHello(t.Context(), scope, 102, 102); err == nil {
			t.Fatal("different peer accepted")
		}
		if grantState(t, j) != before {
			t.Fatal("replay changed state")
		}
		reopened, err := openManagedJournal(t.Context(), j.admission.Reservation.StateRoot, j.admission.ObjectiveID)
		if err != nil {
			t.Fatal(err)
		}
		defer reopened.Close()
		ok, err := reopened.Granted(t.Context(), scope)
		if err != nil || !ok {
			t.Fatalf("reopened grant: %v %v", ok, err)
		}
		child := journalScope("b", scope.Generation)
		if _, err := j.reserveScope(t.Context(), child, j.admission.Deadline, time.Now()); err != nil {
			t.Fatal(err)
		}
		ok, err = j.Granted(t.Context(), scope)
		if err != nil || !ok {
			t.Fatalf("child blocked grant: %v %v", ok, err)
		}
	})
	t.Run("nongranted and unknown", func(t *testing.T) {
		j, scope := grantFixture(t)
		before := grantState(t, j)
		if ok, err := j.Granted(t.Context(), scope); err != nil || ok {
			t.Fatalf("reserved: %v %v", ok, err)
		}
		unknown := journalScope("f", scope.Generation)
		if ok, err := j.Granted(t.Context(), unknown); err == nil || ok {
			t.Fatalf("unknown: %v %v", ok, err)
		}
		if grantState(t, j) != before {
			t.Fatal("read changed state")
		}
	})

	t.Run("persisted unequal peer", func(t *testing.T) {
		j, scope := grantFixture(t)
		if err := j.RecordHello(t.Context(), scope, 101, 101); err != nil {
			t.Fatal(err)
		}
		if _, err := j.conn.ExecContext(t.Context(), "UPDATE managed_scopes SET pgid=102 WHERE id=?", scope.ID); err != nil {
			t.Fatal(err)
		}
		before := grantState(t, j)
		if ok, err := j.Granted(t.Context(), scope); err == nil || ok {
			t.Fatalf("unequal persisted peer: %v %v", ok, err)
		}
		if grantState(t, j) != before {
			t.Fatal("unequal persisted peer changed state")
		}
	})

	t.Run("unequal peer", func(t *testing.T) {
		j, scope := grantFixture(t)
		before := grantState(t, j)
		if err := j.RecordHello(t.Context(), scope, 101, 102); err == nil {
			t.Fatal("unequal peer accepted")
		}
		if grantState(t, j) != before {
			t.Fatal("unequal peer changed state")
		}
	})
	for _, tc := range []struct{ name, query string }{
		{"missing controller", "DELETE FROM managed_reservations WHERE kind='controller'"},
		{"wrong controller generation", "UPDATE managed_reservations SET generation=generation+1 WHERE kind='controller'"},
		{"missing supervisor", "DELETE FROM managed_reservations WHERE kind='supervisor'"},
		{"wrong supervisor generation", "UPDATE managed_reservations SET generation=generation+1 WHERE kind='supervisor'"},
		{"missing activation", "DELETE FROM managed_activation"},
		{"corrupt activation", "UPDATE managed_activation SET body='{}'"},
		{"missing event", "DELETE FROM managed_events WHERE revision=2"},
		{"corrupt event", "UPDATE managed_events SET body='{}' WHERE revision=2"},
	} {
		for _, after := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s after=%t", tc.name, after), func(t *testing.T) {
				j, scope := grantFixture(t)
				if after {
					if err := j.RecordHello(t.Context(), scope, 101, 101); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := j.conn.ExecContext(t.Context(), tc.query); err != nil {
					t.Fatal(err)
				}
				before := grantState(t, j)
				if after {
					if ok, err := j.Granted(t.Context(), scope); err == nil || ok {
						t.Fatalf("tampered grant: %v %v", ok, err)
					}
					if err := j.RecordHello(t.Context(), scope, 101, 101); err == nil {
						t.Fatal("tampered identical-peer replay accepted")
					}
				} else if err := j.RecordHello(t.Context(), scope, 101, 101); err == nil {
					t.Fatal("tampered hello accepted")
				}
				if grantState(t, j) != before {
					t.Fatal("denied operation changed state")
				}
			})
		}
	}
	t.Run("pending activation", func(t *testing.T) {
		j, scope := grantFixture(t)
		a, err := loadManagedActivation(t.Context(), j.conn, j.identity, j.admission.Reservation.StateRoot, j.admission.ObjectiveID)
		if err != nil {
			t.Fatal(err)
		}
		a.Phase = "pending"
		a.Runtime = ManagedRuntimeReference{}
		body, err := json.Marshal(a)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := j.conn.ExecContext(t.Context(), "UPDATE managed_activation SET body=?", body); err != nil {
			t.Fatal(err)
		}
		before := grantState(t, j)
		if err := j.RecordHello(t.Context(), scope, 101, 101); err == nil {
			t.Fatal("pending accepted")
		}
		if grantState(t, j) != before {
			t.Fatal("pending changed state")
		}
	})
	for _, field := range []string{"name", "backend", "deadline"} {
		t.Run("wrong "+field, func(t *testing.T) {
			j, scope := grantFixture(t)
			deadline := j.admission.Deadline
			switch field {
			case "name":
				scope.Name = "wrong"
			case "backend":
				scope.Backend = "docker"
			case "deadline":
				deadline = deadline.Add(-time.Second)
			}
			body, err := json.Marshal(struct {
				ScopeHandle
				Deadline time.Time
			}{scope, deadline})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := j.conn.ExecContext(t.Context(), "UPDATE managed_scopes SET name=?,backend=?,body=? WHERE id=?", scope.Name, scope.Backend, body, scope.ID); err != nil {
				t.Fatal(err)
			}
			before := grantState(t, j)
			if err := j.RecordHello(t.Context(), scope, 101, 101); err == nil {
				t.Fatal("wrong scope accepted")
			}
			if grantState(t, j) != before {
				t.Fatal("wrong scope changed state")
			}
		})
	}
	for _, phase := range []string{"quiescent", "revoked", "stopping"} {
		t.Run(phase, func(t *testing.T) {
			j, scope := grantFixture(t)
			if err := j.RecordHello(t.Context(), scope, 101, 101); err != nil {
				t.Fatal(err)
			}
			switch phase {
			case "quiescent":
				if err := j.MarkQuiescent(t.Context(), scope); err != nil {
					t.Fatal(err)
				}
			case "revoked":
				if _, _, err := j.advanceEpoch(t.Context(), "supervisor", strings.Repeat("d", 32), time.Now()); err != nil {
					t.Fatal(err)
				}
			case "stopping":
				if err := j.setDesired(t.Context(), scope.Generation, "stopping", "test", time.Time{}); err != nil {
					t.Fatal(err)
				}
			}
			before := grantState(t, j)
			ok, err := j.Granted(t.Context(), scope)
			if err != nil || ok {
				t.Fatalf("inactive grant: %v %v", ok, err)
			}
			if err := j.RecordHello(t.Context(), scope, 101, 101); err == nil {
				t.Fatal("inactive hello accepted")
			}
			if grantState(t, j) != before {
				t.Fatal("inactive changed state")
			}
		})
	}
	t.Run("active cleanup", func(t *testing.T) {
		j, scope := grantFixture(t)
		if err := j.RecordHello(t.Context(), scope, 101, 101); err != nil {
			t.Fatal(err)
		}
		cleanup := journalScope("f", scope.Generation)
		cleanup.Kind = "cleanup"
		err := j.mutate(t.Context(), "cleanup-test", cleanup.ID, func(tx *sql.Tx, _ *managedSupervision) (bool, error) {
			return j.insertScope(t.Context(), tx, cleanup, j.admission.Deadline)
		})
		if err != nil {
			t.Fatal(err)
		}
		before := grantState(t, j)
		if ok, err := j.Granted(t.Context(), scope); err == nil || ok {
			t.Fatalf("cleanup grant: %v %v", ok, err)
		}
		if err := j.RecordHello(t.Context(), scope, 101, 101); err == nil {
			t.Fatal("cleanup replay accepted")
		}
		if grantState(t, j) != before {
			t.Fatal("cleanup denial changed state")
		}
	})
	t.Run("host unequal peer remains valid", func(t *testing.T) {
		j, gen := controllerFixture(t)
		host := journalScope("b", gen)
		if _, err := j.reserveScope(t.Context(), host, j.admission.Deadline, time.Now()); err != nil {
			t.Fatal(err)
		}
		if err := j.RecordHello(t.Context(), host, 101, 102); err != nil {
			t.Fatal(err)
		}
		ok, err := j.Granted(t.Context(), host)
		if err != nil || !ok {
			t.Fatalf("host grant: %v %v", ok, err)
		}
	})
	t.Run("expired", func(t *testing.T) {
		root, spec, reservation := managedAdmissionFixture(t)
		spec.TimeoutSeconds = 2
		spec.StepTimeoutSeconds = 1
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
		scope, err := reserveManagedController(t.Context(), j, "supervisor", gen)
		if err != nil {
			t.Fatal(err)
		}
		if err := j.RecordHello(t.Context(), scope, 101, 101); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Until(j.admission.Deadline) + 30*time.Millisecond)
		before := grantState(t, j)
		if ok, err := j.Granted(t.Context(), scope); err != nil || ok {
			t.Fatalf("expired: %v %v", ok, err)
		}
		if err := j.RecordHello(t.Context(), scope, 101, 101); err == nil {
			t.Fatal("expired hello accepted")
		}
		if grantState(t, j) != before {
			t.Fatal("expiry changed state")
		}
	})
}
