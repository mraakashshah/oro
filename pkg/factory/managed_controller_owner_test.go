//nolint:testpackage // Exercises private managed snapshot ownership.
package factory

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func ownerFixture(t *testing.T) (*factoryStore, *managedJournal, ScopeHandle) {
	t.Helper()
	j, gen := controllerFixture(t)
	scope, err := reserveManagedController(t.Context(), j, "supervisor", gen)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.RecordHello(t.Context(), scope, os.Getpid(), os.Getpid()); err != nil {
		t.Fatal(err)
	}
	root := j.admission.Reservation.StateRoot
	id := j.admission.ObjectiveID
	manifest, err := loadManagedManifest(t.Context(), root, id)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := ReadManagedRuntimeReference(t.Context(), root, id)
	if err != nil {
		t.Fatal(err)
	}
	store, err := openManagedFactoryStore(t.Context(), filepath.Join(root, id))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.close() })
	store.owner = &managedControllerOwner{journal: j, manifest: manifest, scope: scope, runtime: ref}
	return store, j, scope
}

func ownerState(t *testing.T, s *factoryStore) string {
	t.Helper()
	var state string
	if err := s.conn.QueryRowContext(t.Context(), `SELECT (SELECT revision||':'||hex(body) FROM snapshots)||':'||(SELECT coalesce(group_concat(revision||state||hex(body),'|'),'') FROM events)`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	return state
}

func deniedOwnerSave(t *testing.T, s *factoryStore, next snapshot) {
	t.Helper()
	before := ownerState(t, s)
	if err := s.save(t.Context(), next, next.Revision); err == nil {
		t.Fatal("invalid owner save accepted")
	}
	if ownerState(t, s) != before {
		t.Fatal("denied save changed snapshot or events")
	}
}

func TestManagedControllerOwner(t *testing.T) {
	if os.Getenv("ORO_OWNER_LEADER") != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestManagedControllerOwner$", "-test.v")
		cmd.Env = append(os.Environ(), "ORO_OWNER_LEADER=1")
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("leader subprocess: %v\n%s", err, out)
		}
		return
	}
	if pgid, err := syscall.Getpgid(0); err != nil || pgid != os.Getpid() {
		t.Fatalf("not leader: %d %v", pgid, err)
	}
	t.Run("valid and stale CAS", func(t *testing.T) {
		s, _, _ := ownerFixture(t)
		state, err := s.load(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		next := state
		next.State = "running"
		next.Attempts = 1
		if err := s.save(t.Context(), next, state.Revision); err != nil {
			t.Fatal(err)
		}
		before := ownerState(t, s)
		if err := s.save(t.Context(), next, state.Revision); err == nil || !strings.Contains(err.Error(), "stale") {
			t.Fatalf("stale: %v", err)
		}
		if ownerState(t, s) != before {
			t.Fatal("stale changed state")
		}
	})
	for _, tc := range []struct{ name, query string }{
		{"controller reservation", "DELETE FROM managed_reservations WHERE kind='controller'"}, {"supervisor reservation", "DELETE FROM managed_reservations WHERE kind='supervisor'"}, {"activation", "UPDATE managed_activation SET body='{}'"}, {"admission", "UPDATE managed_admissions SET body='{}'"}, {"snapshot", "UPDATE snapshots SET body='{}'"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, j, _ := ownerFixture(t)
			next, err := s.load(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := j.conn.ExecContext(t.Context(), tc.query); err != nil {
				t.Fatal(err)
			}
			deniedOwnerSave(t, s, next)
		})
	}
	t.Run("cross connection revocation", func(t *testing.T) {
		s, j, _ := ownerFixture(t)
		next, err := s.load(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		other, err := openManagedJournal(t.Context(), j.admission.Reservation.StateRoot, j.admission.ObjectiveID)
		if err != nil {
			t.Fatal(err)
		}
		defer other.Close()
		if _, _, err := other.advanceEpoch(t.Context(), "supervisor", "revocation", time.Now()); err != nil {
			t.Fatal(err)
		}
		deniedOwnerSave(t, s, next)
	})
	for _, field := range []string{"spec", "hash", "created", "deadline", "created offset", "deadline offset", "nil digests", "nonempty digests"} {
		t.Run("next "+field, func(t *testing.T) {
			s, _, _ := ownerFixture(t)
			next, err := s.load(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			switch field {
			case "spec":
				next.Spec.Objective = "changed"
			case "hash":
				next.PolicyHash = "changed"
			case "created":
				next.CreatedAt = next.CreatedAt.Add(time.Second)
			case "deadline":
				next.Deadline = next.Deadline.Add(time.Second)
			case "created offset":
				next.CreatedAt = next.CreatedAt.In(time.FixedZone("offset", 3600))
			case "deadline offset":
				next.Deadline = next.Deadline.In(time.FixedZone("offset", 3600))
			case "nil digests":
				next.PolicyDigests = map[string]string{}
			case "nonempty digests":
				next.PolicyDigests = map[string]string{"x": "y"}
			}
			deniedOwnerSave(t, s, next)
		})
	}
	for _, field := range []string{"digests", "nonempty digests", "offset", "deadline offset", "spec", "hash", "created", "deadline"} {
		t.Run("current "+field, func(t *testing.T) {
			s, _, _ := ownerFixture(t)
			next, err := s.load(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			current := next
			switch field {
			case "digests":
				current.PolicyDigests = map[string]string{}
			case "nonempty digests":
				current.PolicyDigests = map[string]string{"x": "y"}
			case "offset":
				current.CreatedAt = current.CreatedAt.In(time.FixedZone("offset", 3600))
			case "deadline offset":
				current.Deadline = current.Deadline.In(time.FixedZone("offset", 3600))
			case "spec":
				current.Spec.Objective = "changed"
			case "hash":
				current.PolicyHash = "changed"
			case "created":
				current.CreatedAt = current.CreatedAt.Add(time.Second)
			case "deadline":
				current.Deadline = current.Deadline.Add(time.Second)
			}
			body, err := json.Marshal(current)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.conn.ExecContext(t.Context(), "UPDATE snapshots SET body=?", body); err != nil {
				t.Fatal(err)
			}
			deniedOwnerSave(t, s, next)
		})
	}
	t.Run("owner runtime mismatch", func(t *testing.T) {
		s, _, _ := ownerFixture(t)
		next, err := s.load(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		s.owner.runtime.SHA256 = strings.Repeat("b", 64)
		deniedOwnerSave(t, s, next)
	})
	t.Run("cached admission mismatch", func(t *testing.T) {
		s, _, _ := ownerFixture(t)
		next, err := s.load(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		s.owner.journal.identity = []byte("bad")
		deniedOwnerSave(t, s, next)
	})
	t.Run("manifest mismatch", func(t *testing.T) {
		s, _, _ := ownerFixture(t)
		next, err := s.load(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		s.owner.manifest.Spec.Objective = "changed"
		deniedOwnerSave(t, s, next)
	})

	t.Run("wrong actual peer", func(t *testing.T) {
		s, j, scope := ownerFixture(t)
		next, err := s.load(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := j.conn.ExecContext(t.Context(), "UPDATE managed_scopes SET pid=?,pgid=? WHERE id=?", os.Getpid()+1, os.Getpid()+1, scope.ID); err != nil {
			t.Fatal(err)
		}
		deniedOwnerSave(t, s, next)
	})
	t.Run("wrong actual process group", func(t *testing.T) {
		s, j, scope := ownerFixture(t)
		next, err := s.load(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := j.conn.ExecContext(t.Context(), "UPDATE managed_scopes SET pgid=? WHERE id=?", os.Getpid()+1, scope.ID); err != nil {
			t.Fatal(err)
		}
		deniedOwnerSave(t, s, next)
	})

	t.Run("owner on legacy", func(t *testing.T) {
		s, _, _ := ownerFixture(t)
		next, err := s.load(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		s.managed = false
		deniedOwnerSave(t, s, next)
	})
	t.Run("event rollback", func(t *testing.T) {
		s, _, _ := ownerFixture(t)
		next, err := s.load(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.conn.ExecContext(t.Context(), `CREATE TRIGGER fail_owner BEFORE INSERT ON events BEGIN SELECT RAISE(ABORT,'injected'); END`); err != nil {
			t.Fatal(err)
		}
		deniedOwnerSave(t, s, next)
	})
	t.Run("commit rollback", func(t *testing.T) {
		s, j, _ := ownerFixture(t)
		next, err := s.load(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		db, err := sql.Open("sqlite", filepath.Join(j.admission.Reservation.StateRoot, j.admission.ObjectiveID, "state.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		reader, err := db.BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
		if err != nil {
			t.Fatal(err)
		}
		var n int
		if err := reader.QueryRowContext(t.Context(), "SELECT revision FROM snapshots").Scan(&n); err != nil {
			t.Fatal(err)
		}
		before := ownerState(t, s)
		ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
		defer cancel()
		err = s.save(ctx, next, next.Revision)
		if err == nil || !strings.Contains(err.Error(), "commit factory transition") {
			t.Fatalf("expected commit failure: %v", err)
		}
		if err := reader.Rollback(); err != nil {
			t.Fatal(err)
		}
		if ownerState(t, s) != before {
			t.Fatal("commit failure changed state")
		}
	})
}
