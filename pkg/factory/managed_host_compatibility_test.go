package factory //nolint:testpackage // Compatibility must exercise the private persisted admission and Git execution boundary.

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestManagedHostOldReservation(t *testing.T) {
	// Independent B32/B64 calculations for the finite policy below: six source
	// versions, five invocations and eleven scope records. Both request copies
	// are retained. The old value is a genuine complete 32 MiB-era reservation.
	const (
		fixed      = 1924136960
		source     = 2*3584 + 2*1024 + 128<<10 + 1<<20
		pending    = 4*(1<<20) + 16384
		retained32 = fixed + 6*source + 5*(2*(32<<20)+2*pending+2*(1<<20)) + 11*(1<<20)
		retained64 = fixed + 6*source + 5*(2*(64<<20)+2*pending+2*(1<<20)) + 11*(1<<20)
	)
	limits, err := ManagedHostRetention(hostRetentionFixture())
	if err != nil || retained32 != 2330941440 || retained64 != 2666485760 || retained32 >= retained64 || limits.MinimumBytes != retained64 {
		t.Fatalf("invalid historical/current bounds: B32=%d B64=%d limits=%+v, %v", retained32, retained64, limits, err)
	}
	for _, fixture := range []struct {
		name  string
		bytes int64
	}{
		{name: "historical complete B32 remains metadata only", bytes: retained32},
		{name: "complete B64 permits Git construction", bytes: retained64},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			testManagedHostReservationCompatibility(t, fixture.bytes, retained64)
		})
	}
}

func testManagedHostReservationCompatibility(t *testing.T, reserved, current int64) {
	t.Helper()
	root, spec, reservation := managedHostCompatibilityFixture(t, reserved)
	manifest, err := loadManagedManifest(t.Context(), root, spec.ID)
	if err != nil {
		t.Fatal(err)
	}
	journal, generation := spendManagedHostCompatibilityBudget(t, root, spec.ID)
	before := captureManagedHostCompatibility(t, root, spec.ID, journal)
	if before.model != 1 || before.supervisor != 1 || before.host != 0 || before.scopes != 0 || before.events < 2 ||
		before.snapshot.Attempts != 1 || before.snapshot.Feedback != "retain prior progress" || before.supervision.Generation != generation {
		t.Fatalf("compatibility fixture has no durable progress: %+v", before)
	}
	assertManagedHostCompatibilityView(t, root, manifest)
	host := &sourceGitRuntime{t: t, j: journal}
	repository, err := newManagedGitRepository(manifest, generation, journal, host)
	if reserved < current {
		if repository != nil || err == nil || !strings.Contains(err.Error(), "below its complete retention bound") {
			t.Fatalf("old admission became executable: repository=%v, error=%v", repository, err)
		}
	} else if err != nil || repository == nil || repository.limits.MinimumBytes != current {
		t.Fatalf("complete B64 admission rejected: repository=%v, error=%v", repository, err)
	}
	if len(host.calls) != 0 {
		t.Fatal("Git construction invoked the host runtime")
	}
	assertManagedHostCompatibilityUnchanged(t, root, spec.ID, journal, before)
	if _, err := AdmitManaged(t.Context(), root, spec, reservation); err != nil {
		t.Fatalf("identical admission could not resume its metadata: %v", err)
	}
	assertManagedHostCompatibilityUnchanged(t, root, spec.ID, journal, before)
	if reserved < current {
		spec.Runtime.HostRequiredBytes, reservation.Bytes = current, current
		reservation = reservationForIntendedSpec(t, reservation, spec)
		if _, err := AdmitManaged(t.Context(), root, spec, reservation); err == nil || !strings.Contains(err.Error(), "changed since admission") {
			t.Fatalf("larger incoming reservation amended historical admission: %v", err)
		}
		assertManagedHostCompatibilityView(t, root, manifest)
		assertManagedHostCompatibilityUnchanged(t, root, spec.ID, journal, before)
	}
}

func managedHostCompatibilityFixture(t *testing.T, reserved int64) (string, ManagedSpec, HostReservation) {
	t.Helper()
	root, paths, reservation := managedAdmissionFixture(t)
	spec := hostRetentionFixture()
	spec.Repository, spec.TargetBranch = paths.Repository, paths.TargetBranch
	spec.Runtime.ModelExecutable, spec.Runtime.GitExecutable = paths.Runtime.ModelExecutable, paths.Runtime.GitExecutable
	spec.Runtime.HostRequiredBytes, reservation.Bytes = reserved, reserved
	reservation = reservationForIntendedSpec(t, reservation, spec)
	// Keep the actual source tree inside this one-entry historical policy too.
	gitTestRun(t, spec.Repository, "rm", ".gitignore")
	gitTestRun(t, spec.Repository, "commit", "-m", "one-entry compatibility source")
	gitTestRun(t, spec.Repository, "branch", "-f", spec.TargetBranch, "HEAD")
	// Core admission preserves valid historical identities; current CLI capacity
	// allocation is a separate boundary and must not allocate this old B32 anew.
	if _, err := AdmitManaged(t.Context(), root, spec, reservation); err != nil {
		t.Fatal(err)
	}
	return root, spec, reservation
}

func spendManagedHostCompatibilityBudget(t *testing.T, root, id string) (*managedJournal, uint64) {
	t.Helper()
	store, err := openManagedFactoryStore(t.Context(), filepath.Join(root, id))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.close() })
	state, err := store.load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	state.State, state.Attempts, state.Feedback = "running", 1, "retain prior progress"
	if err := store.save(t.Context(), state, state.Revision); err != nil {
		t.Fatal(err)
	}
	if err := store.close(); err != nil {
		t.Fatal(err)
	}
	journal := openJournalFixture(t, root, id)
	generation, fresh, err := journal.beginGeneration(t.Context(), "compat-supervisor", time.Now())
	if err != nil || !fresh || generation == 0 {
		t.Fatalf("reserve prior supervisor: generation=%d fresh=%t, %v", generation, fresh, err)
	}
	if fresh, err := journal.reserveBudget(t.Context(), generation, "model", "compat-model", time.Now()); err != nil || !fresh {
		t.Fatalf("spend prior model call: fresh=%t, %v", fresh, err)
	}
	return journal, generation
}

type managedHostCompatibilityState struct {
	files                                   map[string]managedFrozenProgram
	snapshot                                snapshot
	supervision                             managedSupervision
	model, supervisor, host, scopes, events int
	deliveryEvents                          int
}

func captureManagedHostCompatibility(t *testing.T, root, id string, journal *managedJournal) managedHostCompatibilityState {
	t.Helper()
	state := managedHostCompatibilityState{files: make(map[string]managedFrozenProgram)}
	for _, name := range []string{
		filepath.Join(".managed", id+".json"), filepath.Join(".managed", id+".state.init"),
		filepath.Join(id, "managed.json"), filepath.Join(id, "model"), filepath.Join(id, "git"),
		filepath.Join(id, "state.db"), filepath.Join(id, "state.init"),
	} {
		digest, err := hashManagedProgram(t.Context(), filepath.Join(root, name), managedExecutableMaxBytes, false)
		if err != nil {
			t.Fatal(err)
		}
		state.files[name] = digest
	}
	var err error
	state.snapshot, err = decodeSnapshot(journal.conn.QueryRowContext(t.Context(), "SELECT body FROM snapshots WHERE id=1"))
	if err != nil {
		t.Fatal(err)
	}
	state.supervision, err = journal.supervision(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	err = journal.conn.QueryRowContext(t.Context(), `SELECT
		(SELECT count(*) FROM managed_reservations WHERE kind='model'),
		(SELECT count(*) FROM managed_reservations WHERE kind='supervisor'),
		(SELECT count(*) FROM managed_reservations WHERE kind='host'),
		(SELECT count(*) FROM managed_scopes),
		(SELECT count(*) FROM managed_events),
		(SELECT count(*) FROM events)`).Scan(&state.model, &state.supervisor, &state.host, &state.scopes, &state.events, &state.deliveryEvents)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func assertManagedHostCompatibilityUnchanged(t *testing.T, root, id string, journal *managedJournal, before managedHostCompatibilityState) {
	t.Helper()
	after := captureManagedHostCompatibility(t, root, id, journal)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("compatibility boundary changed immutable bytes, progress, budgets or events:\nbefore=%+v\nafter=%+v", before, after)
	}
}

func assertManagedHostCompatibilityView(t *testing.T, root string, manifest managedManifest) {
	t.Helper()
	want := ManagedAdmissionView{
		StateRoot: root, ObjectiveID: manifest.Spec.ID, PolicyHash: manifest.PolicyHash,
		GitPath: filepath.Join(root, manifest.Spec.ID, "git"), GitSHA256: manifest.Programs["git"].Digest,
		Repository: manifest.Spec.Repository, TargetRef: "refs/heads/" + manifest.Spec.TargetBranch,
		Deadline: manifest.Deadline, HardStop: manifest.HardStop,
		CleanupTimeoutSeconds: manifest.Spec.Recovery.CleanupTimeoutSeconds,
		DockerContext:         manifest.Spec.Runtime.DockerContext, ImageDigest: manifest.Spec.Runtime.ImageDigest,
	}
	if got, err := ReadManagedAdmission(t.Context(), root, manifest.Spec.ID); err != nil || got != want {
		t.Fatalf("historical admission metadata changed or became unreadable: got=%+v want=%+v, %v", got, want, err)
	}
}
