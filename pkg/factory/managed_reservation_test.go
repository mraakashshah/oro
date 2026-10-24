package factory //nolint:testpackage // Eligibility must exercise durable private ownership transitions.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

type hostReleaseFixture struct {
	root      string
	admission managedJournalAdmission
	store     *factoryStore
	journal   *managedJournal
	scope     ScopeHandle
}

func newHostReleaseFixture(t *testing.T) hostReleaseFixture {
	t.Helper()
	root, admission, store := journalFixture(t)
	insertJournalFixture(t, store, admission)
	journal := openJournalFixture(t, root, admission.ObjectiveID)
	generation, _, err := journal.beginGeneration(t.Context(), "supervisor", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	scope := journalScope("a", generation)
	if _, err := journal.reserveScope(t.Context(), scope, admission.Deadline, time.Now()); err != nil {
		t.Fatal(err)
	}
	return hostReleaseFixture{root: root, admission: admission, store: store, journal: journal, scope: scope}
}

func TestReleasableHostReservation(t *testing.T) {
	t.Run("monotonic stopped and quiescent authority", testHostReleaseTerminal)
	t.Run("stopped before any execution", func(t *testing.T) {
		root, admission, store := journalFixture(t)
		insertJournalFixture(t, store, admission)
		journal := openJournalFixture(t, root, admission.ObjectiveID)
		if err := journal.setDesired(t.Context(), 0, "stopping", "user-stop", time.Time{}); err != nil {
			t.Fatal(err)
		}
		if err := journal.setDesired(t.Context(), 0, "stopped", "user-stop", time.Time{}); err != nil {
			t.Fatal(err)
		}
		got, eligible, err := ReleasableHostReservation(t.Context(), root, admission.ObjectiveID)
		if err != nil || !eligible || got != admission.Reservation {
			t.Fatalf("never-started stopped reservation = %+v, %v, %v", got, eligible, err)
		}
	})
	t.Run("unknown promotion stays unknown", testHostReleaseUnknownOutcome)
	t.Run("corrupt authority retains reservation", testHostReleaseCorruption)
	t.Run("missing canceled and invalid authority never initializes", testHostReleaseMissing)
}

func testHostReleaseTerminal(t *testing.T) {
	t.Helper()
	fixture := newHostReleaseFixture(t)
	completed := snapshot{Spec: managedSnapshotSpec(managedContractFixture()), State: "completed", Deadline: fixture.admission.Deadline}
	if err := fixture.store.save(t.Context(), completed, 0); err != nil {
		t.Fatal(err)
	}
	if err := fixture.journal.RecordHello(t.Context(), fixture.scope, 1001, 1001); err != nil {
		t.Fatal(err)
	}
	if err := fixture.journal.MarkQuiescent(t.Context(), fixture.scope); err != nil {
		t.Fatal(err)
	}
	// Completion and existing quiescence cannot release while execution can resume.
	assertHostReservationRetained(t, fixture)
	other := journalScope("b", fixture.scope.Generation)
	if _, err := fixture.journal.reserveScope(t.Context(), other, fixture.admission.Deadline, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := fixture.journal.setDesired(t.Context(), fixture.scope.Generation, "stopping", "completed", time.Time{}); err != nil {
		t.Fatal(err)
	}
	// A revoked scope remains uncertain until the backend records retirement.
	assertHostReservationRetained(t, fixture)
	if err := fixture.journal.MarkQuiescent(t.Context(), other); err != nil {
		t.Fatal(err)
	}
	if err := fixture.journal.setDesired(t.Context(), fixture.scope.Generation, "stopped", "completed", time.Time{}); err != nil {
		t.Fatal(err)
	}
	before, err := fixture.journal.supervision(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		got, eligible, err := ReleasableHostReservation(t.Context(), fixture.root, fixture.admission.ObjectiveID)
		if err != nil || !eligible || got != fixture.admission.Reservation {
			t.Fatalf("release identity = %+v, eligible=%v, error=%v", got, eligible, err)
		}
	}
	after, err := fixture.journal.supervision(t.Context())
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("eligibility query changed ownership: %+v, %v", after, err)
	}
	if err := fixture.journal.setDesired(t.Context(), fixture.scope.Generation, "running", "", time.Time{}); err == nil {
		t.Fatal("release-eligible objective resumed desired execution")
	}
	if _, _, err := fixture.journal.beginGeneration(t.Context(), "successor", time.Now()); err == nil {
		t.Fatal("release-eligible objective opened a new incarnation")
	}
	if _, err := fixture.journal.reserveScope(t.Context(), journalScope("c", fixture.scope.Generation), fixture.admission.Deadline, time.Now()); err == nil {
		t.Fatal("release-eligible objective created another execution scope")
	}
}

func assertHostReservationRetained(t *testing.T, fixture hostReleaseFixture) {
	t.Helper()
	got, eligible, err := ReleasableHostReservation(t.Context(), fixture.root, fixture.admission.ObjectiveID)
	if err != nil || eligible || got != (HostReservation{}) {
		t.Fatalf("nonterminal or uncertain authority released capacity: %+v, %v, %v", got, eligible, err)
	}
}

func stopHostReleaseFixture(t *testing.T, fixture hostReleaseFixture) {
	t.Helper()
	if err := fixture.journal.setDesired(t.Context(), fixture.scope.Generation, "stopping", "promotion_outcome_unknown", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.journal.MarkQuiescent(t.Context(), fixture.scope); err != nil {
		t.Fatal(err)
	}
	if err := fixture.journal.setDesired(t.Context(), fixture.scope.Generation, "stopped", "promotion_outcome_unknown", time.Time{}); err != nil {
		t.Fatal(err)
	}
}

func testHostReleaseUnknownOutcome(t *testing.T) {
	t.Helper()
	fixture := newHostReleaseFixture(t)
	promoting := snapshot{Spec: managedSnapshotSpec(managedContractFixture()), State: "promoting", Reason: "promotion_outcome_unknown", Deadline: fixture.admission.Deadline}
	if err := fixture.store.save(t.Context(), promoting, 0); err != nil {
		t.Fatal(err)
	}
	stopHostReleaseFixture(t, fixture)
	if _, eligible, err := ReleasableHostReservation(t.Context(), fixture.root, fixture.admission.ObjectiveID); err != nil || !eligible {
		t.Fatalf("stopped quiescent unknown outcome retained execution capacity: %v, %v", eligible, err)
	}
	actual, err := fixture.store.load(t.Context())
	if err != nil || actual.State != "promoting" || actual.Reason != "promotion_outcome_unknown" {
		t.Fatalf("release query relabeled delivery outcome: %+v, %v", actual, err)
	}
}

func testHostReleaseCorruption(t *testing.T) {
	t.Helper()
	for _, name := range []string{"admission", "supervision", "scope body", "scope columns", "cleanup proof", "invalid peer", "future scope", "missing admission"} {
		t.Run(name, func(t *testing.T) {
			fixture := newHostReleaseFixture(t)
			stopHostReleaseFixture(t, fixture)
			query, args := "", []any(nil)
			switch name {
			case "admission":
				query, args = "UPDATE managed_admissions SET body=?", []any{[]byte("{")}
			case "supervision":
				query, args = "UPDATE managed_supervision SET body=?", []any{[]byte("{}")}
			case "scope body":
				query, args = "UPDATE managed_scopes SET body=?", []any{[]byte("{")}
			case "scope columns":
				query = "UPDATE managed_scopes SET name='different-backend-name'"
			case "cleanup proof":
				query = "UPDATE managed_scopes SET cleanup=''"
			case "invalid peer":
				query = "UPDATE managed_scopes SET pid=-1,pgid=-1"
			case "future scope":
				future := fixture.scope
				future.Generation++
				body, err := json.Marshal(struct {
					ScopeHandle
					Deadline time.Time
				}{future, fixture.admission.Deadline})
				if err != nil {
					t.Fatal(err)
				}
				query, args = "UPDATE managed_scopes SET body=?", []any{body}
			case "missing admission":
				query = "DELETE FROM managed_admissions"
			}
			if _, err := fixture.store.conn.ExecContext(t.Context(), query, args...); err != nil {
				t.Fatal(err)
			}
			got, eligible, err := ReleasableHostReservation(t.Context(), fixture.root, fixture.admission.ObjectiveID)
			if err == nil || eligible || got != (HostReservation{}) {
				t.Fatalf("corrupt authority released capacity: %+v, %v, %v", got, eligible, err)
			}
		})
	}
}

func testHostReleaseMissing(t *testing.T) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"missing", "../invalid"} {
		if got, eligible, err := ReleasableHostReservation(t.Context(), root, id); err == nil || eligible || got != (HostReservation{}) {
			t.Fatalf("missing/invalid authority accepted: %+v, %v, %v", got, eligible, err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := ReleasableHostReservation(ctx, root, "missing"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled release = %v", err)
	}
	if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
		t.Fatalf("release query initialized missing authority: %v, %v", entries, err)
	}
}
