package factory //nolint:testpackage // Inspect retained private admission history.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestManagedActivation(t *testing.T) {
	t.Run("pending", testManagedActivationPending)
	t.Run("lifecycle", testManagedActivationLifecycle)
	t.Run("history", testManagedActivationHistory)
	t.Run("corruption", testManagedActivationCorruption)
	t.Run("concurrent replay", testManagedActivationConcurrent)
	t.Run("retained history", testManagedActivationRetainedHistory)
	t.Run("historical replay", testManagedActivationHistorical)
	t.Run("expired replay", testManagedActivationExpired)
	t.Run("capacity rollback", testManagedActivationCapacity)
	t.Run("invalid pending", testManagedActivationInvalidPending)
	t.Run("missing active history", testManagedActivationMissingActiveHistory)
	t.Run("snapshot integrity", testManagedActivationSnapshotIntegrity)
	t.Run("activation event integrity", testManagedActivationEventIntegrity)
	t.Run("rollback", testManagedActivationRollback)
}

func testManagedActivationPending(t *testing.T) {
	root, spec, reservation := managedAdmissionFixture(t)
	if _, err := AdmitManaged(t.Context(), root, spec, reservation); err != nil {
		t.Fatal(err)
	}
	store, err := openManagedFactoryStore(t.Context(), filepath.Join(root, spec.ID))
	if err != nil {
		t.Fatal(err)
	}
	defer store.close()
	var count int
	if err := store.conn.QueryRowContext(t.Context(), "SELECT count(*) FROM managed_activation").Scan(&count); err != nil {
		t.Fatalf("fresh admission lacks activation persistence: %v", err)
	}
	if count != 1 {
		t.Fatalf("fresh admission activation rows = %d, want 1", count)
	}
}

func testManagedActivationLifecycle(t *testing.T) {
	root, spec, reservation := managedAdmissionFixture(t)
	if _, err := AdmitManaged(t.Context(), root, spec, reservation); err != nil {
		t.Fatal(err)
	}
	ref := ManagedRuntimeReference{Path: filepath.Join(root, spec.ID, "runtime", "runtime.json"), SHA256: strings.Repeat("a", 64)}
	if _, err := ReadManagedRuntimeReference(t.Context(), root, spec.ID); err == nil {
		t.Fatal("pending reference read")
	}
	if err := ActivateManaged(t.Context(), root, spec.ID, ref); err != nil {
		t.Fatal(err)
	}
	got, err := ReadManagedRuntimeReference(t.Context(), root, spec.ID)
	if err != nil || got != ref {
		t.Fatalf("reference = %+v, %v", got, err)
	}
	if err := ActivateManaged(t.Context(), root, spec.ID, ref); err != nil {
		t.Fatal(err)
	}
	changed := ref
	changed.SHA256 = strings.Repeat("b", 64)
	if err := ActivateManaged(t.Context(), root, spec.ID, changed); err == nil {
		t.Fatal("changed reference accepted")
	}
	if _, err := AdmitManaged(t.Context(), root, spec, reservation); err != nil {
		t.Fatal(err)
	}
	got, err = ReadManagedRuntimeReference(t.Context(), root, spec.ID)
	if err != nil || got != ref {
		t.Fatalf("replay reset reference: %+v %v", got, err)
	}
}

func testManagedActivationHistory(t *testing.T) {
	root, spec, reservation := managedAdmissionFixture(t)
	if _, err := AdmitManaged(t.Context(), root, spec, reservation); err != nil {
		t.Fatal(err)
	}
	j, err := openManagedJournal(t.Context(), root, spec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := j.beginGeneration(t.Context(), "supervisor", time.Now()); err != nil {
		t.Fatal(err)
	}
	_ = j.Close()
	ref := ManagedRuntimeReference{Path: filepath.Join(root, spec.ID, "runtime", "runtime.json"), SHA256: strings.Repeat("a", 64)}
	if err := ActivateManaged(t.Context(), root, spec.ID, ref); err == nil {
		t.Fatal("execution history accepted")
	}
}

func testManagedActivationCorruption(t *testing.T) {
	root, spec, reservation := managedAdmissionFixture(t)
	if _, err := AdmitManaged(t.Context(), root, spec, reservation); err != nil {
		t.Fatal(err)
	}
	store, err := openManagedFactoryStore(t.Context(), filepath.Join(root, spec.ID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.conn.ExecContext(t.Context(), "UPDATE managed_activation SET body='{}'"); err != nil {
		t.Fatal(err)
	}
	_ = store.close()
	ref := ManagedRuntimeReference{Path: filepath.Join(root, spec.ID, "runtime", "runtime.json"), SHA256: strings.Repeat("a", 64)}
	if err := ActivateManaged(t.Context(), root, spec.ID, ref); err == nil {
		t.Fatal("corrupt activation accepted")
	}
	if _, err := AdmitManaged(t.Context(), root, spec, reservation); err == nil {
		t.Fatal("corrupt activation replayed")
	}
}

func testManagedActivationConcurrent(t *testing.T) {
	root, spec, reservation := managedAdmissionFixture(t)
	if _, err := AdmitManaged(t.Context(), root, spec, reservation); err != nil {
		t.Fatal(err)
	}
	ref := ManagedRuntimeReference{Path: filepath.Join(root, spec.ID, "runtime", "runtime.json"), SHA256: strings.Repeat("a", 64)}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- ActivateManaged(t.Context(), root, spec.ID, ref) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	store, err := openManagedFactoryStore(t.Context(), filepath.Join(root, spec.ID))
	if err != nil {
		t.Fatal(err)
	}
	defer store.close()
	var n int
	if err := store.conn.QueryRowContext(t.Context(), "SELECT count(*) FROM managed_events").Scan(&n); err != nil || n != 1 {
		t.Fatalf("events = %d, %v", n, err)
	}
}

func testManagedActivationRetainedHistory(t *testing.T) {
	for _, tc := range []struct {
		name, query string
		active      bool
	}{
		{"initial event body", "UPDATE events SET body='{}' WHERE revision=1", false},
		{"initial event state", "UPDATE events SET state='failed' WHERE revision=1", false},
		{"active initial event", "UPDATE events SET body='{}' WHERE revision=1", true},
		{"missing active history", "DROP TABLE managed_docker_intents", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, spec, reservation := managedAdmissionFixture(t)
			if _, err := AdmitManaged(t.Context(), root, spec, reservation); err != nil {
				t.Fatal(err)
			}
			ref := ManagedRuntimeReference{Path: filepath.Join(root, spec.ID, "runtime", "runtime.json"), SHA256: strings.Repeat("a", 64)}
			if tc.active {
				if err := ActivateManaged(t.Context(), root, spec.ID, ref); err != nil {
					t.Fatal(err)
				}
			}
			store, err := openManagedFactoryStore(t.Context(), filepath.Join(root, spec.ID))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.conn.ExecContext(t.Context(), tc.query); err != nil {
				t.Fatal(err)
			}
			_ = store.close()
			if err := ActivateManaged(t.Context(), root, spec.ID, ref); err == nil {
				t.Fatal("corrupt retained history accepted")
			}
			if tc.active {
				if _, err := ReadManagedRuntimeReference(t.Context(), root, spec.ID); err == nil {
					t.Fatal("missing active history read")
				}
			}
		})
	}
}

func activationFixture(t *testing.T) (string, ManagedSpec, HostReservation, ManagedRuntimeReference) {
	t.Helper()
	root, spec, reservation := managedAdmissionFixture(t)
	if _, err := AdmitManaged(t.Context(), root, spec, reservation); err != nil {
		t.Fatal(err)
	}
	return root, spec, reservation, ManagedRuntimeReference{Path: filepath.Join(root, spec.ID, "runtime", "runtime.json"), SHA256: strings.Repeat("a", 64)}
}

func testManagedActivationHistorical(t *testing.T) {
	root, spec, _, ref := activationFixture(t)
	if err := ActivateManaged(t.Context(), root, spec.ID, ref); err != nil {
		t.Fatal(err)
	}
	store, err := openManagedFactoryStore(t.Context(), filepath.Join(root, spec.ID))
	if err != nil {
		t.Fatal(err)
	}
	progressed, err := store.load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	progressed.State = "running"
	progressed.Attempts = 1
	progressed.Feedback = "progress"
	if err := store.save(t.Context(), progressed, progressed.Revision); err != nil {
		t.Fatal(err)
	}
	_ = store.close()
	j, err := openManagedJournal(t.Context(), root, spec.ID)
	if err != nil {
		t.Fatal(err)
	}
	generation, _, err := j.beginGeneration(t.Context(), "supervisor", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := j.setDesired(t.Context(), generation, "stopping", "done", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := j.setDesired(t.Context(), generation, "stopped", "done", time.Time{}); err != nil {
		t.Fatal(err)
	}
	before, err := j.supervision(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	_ = j.Close()
	got, err := ReadManagedRuntimeReference(t.Context(), root, spec.ID)
	if err != nil || got != ref {
		t.Fatalf("historical read: %+v %v", got, err)
	}
	if err := ActivateManaged(t.Context(), root, spec.ID, ref); err != nil {
		t.Fatal(err)
	}
	j, err = openManagedJournal(t.Context(), root, spec.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	after, err := j.supervision(t.Context())
	if err != nil || after != before {
		t.Fatalf("replay changed supervision: %+v %+v %v", before, after, err)
	}
}

func testManagedActivationInvalidPending(t *testing.T) {
	cases := []struct{ name, query string }{
		{"missing row", "DELETE FROM managed_activation"},
		{"missing table", "DROP TABLE managed_activation"},
		{"null runtime", `UPDATE managed_activation SET body=replace(CAST(body AS TEXT), '"runtime":{"path":"","sha256":""}', '"runtime":null')`},
		{"duplicate field", `UPDATE managed_activation SET body=replace(CAST(body AS TEXT), '"phase":"pending"', '"phase":"pending","phase":"pending"')`},
		{"unknown field", `UPDATE managed_activation SET body=substr(CAST(body AS TEXT),1,length(body)-1)||',"extra":1}'`},
		{"noncanonical", "UPDATE managed_activation SET body=' '||CAST(body AS TEXT)"},
		{"snapshot state", `UPDATE snapshots SET body=replace(CAST(body AS TEXT), '"State":"pending"', '"State":"running"')`},
		{"supervision reason", `UPDATE managed_supervision SET body=replace(CAST(body AS TEXT), '"Reason":""', '"Reason":"changed"')`},
		{"retired reservation", "INSERT INTO managed_reservations(kind,token,generation) VALUES('host','retired',1)"},
	}
	for _, table := range []string{"managed_reservations", "managed_scopes", "managed_events", "managed_cleanup_permits", "managed_docker_intents", "managed_docker_keepers", "managed_docker_sources"} {
		cases = append(cases, struct{ name, query string }{"missing " + table, "DROP TABLE " + table})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, spec, reservation, ref := activationFixture(t)
			store, err := openManagedFactoryStore(t.Context(), filepath.Join(root, spec.ID))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.conn.ExecContext(t.Context(), tc.query); err != nil {
				t.Fatal(err)
			}
			_ = store.close()
			if err := ActivateManaged(t.Context(), root, spec.ID, ref); err == nil {
				t.Fatal("invalid pending state accepted")
			}
			if tc.name == "missing row" || tc.name == "missing table" || tc.name == "null runtime" || tc.name == "duplicate field" || tc.name == "unknown field" || tc.name == "noncanonical" {
				if _, err := AdmitManaged(t.Context(), root, spec, reservation); err == nil {
					t.Fatal("invalid activation admission replayed")
				}
			}
		})
	}
}

func testManagedActivationRollback(t *testing.T) {
	root, spec, _, ref := activationFixture(t)
	store, err := openManagedFactoryStore(t.Context(), filepath.Join(root, spec.ID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.conn.ExecContext(t.Context(), "CREATE TRIGGER fail_activation_event BEFORE INSERT ON managed_events BEGIN SELECT RAISE(ABORT, 'injected event failure'); END"); err != nil {
		t.Fatal(err)
	}
	_ = store.close()
	if err := ActivateManaged(t.Context(), root, spec.ID, ref); err == nil {
		t.Fatal("event failure accepted")
	}
	store, err = openManagedFactoryStore(t.Context(), filepath.Join(root, spec.ID))
	if err != nil {
		t.Fatal(err)
	}
	var body []byte
	if err := store.conn.QueryRowContext(t.Context(), "SELECT body FROM managed_activation WHERE id=1").Scan(&body); err != nil {
		t.Fatal(err)
	}
	var record managedActivation
	if err := json.Unmarshal(body, &record); err != nil || record.Phase != "pending" {
		t.Fatalf("event failure persisted activation: %s %v", body, err)
	}
	var n int
	if err := store.conn.QueryRowContext(t.Context(), "SELECT count(*) FROM managed_events").Scan(&n); err != nil || n != 0 {
		t.Fatalf("event failure wrote history: %d %v", n, err)
	}
	if _, err := store.conn.ExecContext(t.Context(), "DROP TRIGGER fail_activation_event"); err != nil {
		t.Fatal(err)
	}
	_ = store.close()
	db, err := sql.Open("sqlite", filepath.Join(root, spec.ID, "state.db"))
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
	err = ActivateManaged(ctx, root, spec.ID, ref)
	cancel()
	if err == nil || !strings.Contains(err.Error(), "commit managed transition") {
		t.Fatalf("pinned reader did not fail commit: %v", err)
	}
	if err := reader.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadManagedRuntimeReference(t.Context(), root, spec.ID); err == nil {
		t.Fatal("failed commit persisted activation")
	}
	if err := ActivateManaged(t.Context(), root, spec.ID, ref); err != nil {
		t.Fatalf("activation did not recover: %v", err)
	}
}

func testManagedActivationExpired(t *testing.T) {
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
	pendingRoot, pendingSpec, pendingReservation := managedAdmissionFixture(t)
	pendingSpec.TimeoutSeconds = 2
	pendingSpec.StepTimeoutSeconds = 1
	if _, err := AdmitManaged(t.Context(), pendingRoot, pendingSpec, pendingReservation); err != nil {
		t.Fatal(err)
	}
	pendingRef := ManagedRuntimeReference{Path: filepath.Join(pendingRoot, pendingSpec.ID, "runtime", "runtime.json"), SHA256: strings.Repeat("a", 64)}
	pendingView, err := ReadManagedAdmission(t.Context(), pendingRoot, pendingSpec.ID)
	if err != nil {
		t.Fatal(err)
	}
	view, err := ReadManagedAdmission(t.Context(), root, spec.ID)
	if err != nil {
		t.Fatal(err)
	}
	deadline := view.Deadline
	if pendingView.Deadline.After(deadline) {
		deadline = pendingView.Deadline
	}
	time.Sleep(time.Until(deadline) + 50*time.Millisecond)
	if err := ActivateManaged(t.Context(), pendingRoot, pendingSpec.ID, pendingRef); err == nil {
		t.Fatal("expired pending activation accepted")
	}
	if got, err := ReadManagedRuntimeReference(t.Context(), root, spec.ID); err != nil || got != ref {
		t.Fatalf("expired read: %+v %v", got, err)
	}
	if err := ActivateManaged(t.Context(), root, spec.ID, ref); err != nil {
		t.Fatalf("expired replay: %v", err)
	}
}

func testManagedActivationCapacity(t *testing.T) {
	root, spec, _, ref := activationFixture(t)
	store, err := openManagedFactoryStore(t.Context(), filepath.Join(root, spec.ID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.conn.ExecContext(t.Context(), "CREATE TABLE activation_capacity(body BLOB); CREATE TRIGGER activation_full BEFORE INSERT ON managed_events BEGIN INSERT INTO activation_capacity VALUES(zeroblob(70000000)); END"); err != nil {
		t.Fatal(err)
	}
	_ = store.close()
	if err := ActivateManaged(t.Context(), root, spec.ID, ref); err == nil {
		t.Fatal("capacity failure accepted")
	}
	store, err = openManagedFactoryStore(t.Context(), filepath.Join(root, spec.ID))
	if err != nil {
		t.Fatal(err)
	}
	defer store.close()
	var body []byte
	if err := store.conn.QueryRowContext(t.Context(), "SELECT body FROM managed_activation WHERE id=1").Scan(&body); err != nil {
		t.Fatal(err)
	}
	var a managedActivation
	if err := json.Unmarshal(body, &a); err != nil || a.Phase != "pending" {
		t.Fatalf("capacity failure persisted activation: %s %v", body, err)
	}
	var n int
	if err := store.conn.QueryRowContext(t.Context(), "SELECT count(*) FROM managed_events").Scan(&n); err != nil || n != 0 {
		t.Fatalf("capacity failure wrote events: %d %v", n, err)
	}
}

func testManagedActivationMissingActiveHistory(t *testing.T) {
	for _, table := range []string{"events", "managed_reservations", "managed_scopes", "managed_events", "managed_cleanup_permits", "managed_docker_intents", "managed_docker_keepers", "managed_docker_sources"} {
		t.Run(table, func(t *testing.T) {
			root, spec, _, ref := activationFixture(t)
			if err := ActivateManaged(t.Context(), root, spec.ID, ref); err != nil {
				t.Fatal(err)
			}
			store, err := openManagedFactoryStore(t.Context(), filepath.Join(root, spec.ID))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.conn.ExecContext(t.Context(), "DROP TABLE "+table); err != nil {
				t.Fatal(err)
			}
			_ = store.close()
			if _, err := ReadManagedRuntimeReference(t.Context(), root, spec.ID); err == nil {
				t.Fatal("missing history accepted by read")
			}
			if err := ActivateManaged(t.Context(), root, spec.ID, ref); err == nil {
				t.Fatal("missing history accepted by replay")
			}
		})
	}
}

func testManagedActivationSnapshotIntegrity(t *testing.T) {
	for _, active := range []bool{false, true} {
		for _, tc := range []struct{ name, query string }{
			{"revision column", "UPDATE snapshots SET revision=2 WHERE id=1"},
			{"unknown field", `UPDATE snapshots SET body=substr(CAST(body AS TEXT),1,length(body)-1)||',"extra":1}'`},
			{"noncanonical", "UPDATE snapshots SET body=' '||CAST(body AS TEXT)"},
		} {
			t.Run(fmt.Sprintf("active=%t/%s", active, tc.name), func(t *testing.T) {
				root, spec, _, ref := activationFixture(t)
				if active {
					if err := ActivateManaged(t.Context(), root, spec.ID, ref); err != nil {
						t.Fatal(err)
					}
				}
				store, err := openManagedFactoryStore(t.Context(), filepath.Join(root, spec.ID))
				if err != nil {
					t.Fatal(err)
				}
				if _, err := store.conn.ExecContext(t.Context(), tc.query); err != nil {
					t.Fatal(err)
				}
				_ = store.close()
				if err := ActivateManaged(t.Context(), root, spec.ID, ref); err == nil {
					t.Error("invalid snapshot accepted by activation")
				}
				if active {
					if _, err := ReadManagedRuntimeReference(t.Context(), root, spec.ID); err == nil {
						t.Fatal("invalid snapshot accepted by read")
					}
				}
			})
		}
	}
}

func testManagedActivationEventIntegrity(t *testing.T) {
	for _, tc := range []struct{ name, query string }{
		{"missing activation event", "DELETE FROM managed_events WHERE revision=2"},
		{"changed event digest", "UPDATE managed_events SET ref='bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb' WHERE revision=2"},
		{"changed activation digest", `UPDATE managed_activation SET body=replace(CAST(body AS TEXT), 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb')`},
		{"changed event body", "UPDATE managed_events SET body='{}' WHERE revision=2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, spec, _, ref := activationFixture(t)
			if err := ActivateManaged(t.Context(), root, spec.ID, ref); err != nil {
				t.Fatal(err)
			}
			j, err := openManagedJournal(t.Context(), root, spec.ID)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := j.beginGeneration(t.Context(), "supervisor", time.Now()); err != nil {
				t.Fatal(err)
			}
			_ = j.Close()
			store, err := openManagedFactoryStore(t.Context(), filepath.Join(root, spec.ID))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.conn.ExecContext(t.Context(), tc.query); err != nil {
				t.Fatal(err)
			}
			_ = store.close()
			if tc.name == "changed activation digest" {
				ref.SHA256 = strings.Repeat("b", 64)
			}
			if _, err := ReadManagedRuntimeReference(t.Context(), root, spec.ID); err == nil {
				t.Error("invalid activation event accepted by read")
			}
			if err := ActivateManaged(t.Context(), root, spec.ID, ref); err == nil {
				t.Fatal("invalid activation event accepted by replay")
			}
		})
	}
}
