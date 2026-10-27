package factory //nolint:testpackage // Admission reads must verify private durable identities without changing them.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestManagedAdmissionView(t *testing.T) {
	t.Run("exact immutable producer inputs", testManagedAdmissionViewIdentity)
	t.Run("reads preserve delivery and ownership progress", testManagedAdmissionViewNoReset)
	t.Run("resume uses frozen programs only", testManagedAdmissionViewFrozen)
	t.Run("artifacts do not prove committed admission", testManagedAdmissionViewUncommitted)
	t.Run("canonical root and valid objective required", testManagedAdmissionViewPaths)
	t.Run("missing or corrupt artifacts fail closed", testManagedAdmissionViewArtifacts)
	t.Run("complete journal identity must match", testManagedAdmissionViewJournal)
	t.Run("immutable snapshot identity must match", testManagedAdmissionViewSnapshot)
	t.Run("expired stopped admission remains readable", testManagedAdmissionViewStopped)
	t.Run("canceled and bounded contexts", testManagedAdmissionViewContext)
}

func testManagedAdmissionViewIdentity(t *testing.T) {
	t.Helper()
	for _, branch := range []string{"delivery", "feature/delivery", "refs/heads/delivery"} {
		t.Run(branch, func(t *testing.T) {
			root, spec, reservation := managedAdmissionFixture(t)
			if branch != spec.TargetBranch {
				gitTestRun(t, spec.Repository, "branch", branch)
			}
			spec.TargetBranch = branch
			if strings.Contains(branch, "/") {
				spec.Runtime.ImageDigest = "registry.example/factory@sha256:" + strings.Repeat("b", 64)
			}
			reservation = reservationForIntendedSpec(t, reservation, spec)
			if _, err := AdmitManaged(t.Context(), root, spec, reservation); err != nil {
				t.Fatal(err)
			}
			manifest, err := loadManagedManifest(t.Context(), root, spec.ID)
			if err != nil {
				t.Fatal(err)
			}
			got, err := ReadManagedAdmission(t.Context(), root, spec.ID)
			want := ManagedAdmissionView{
				StateRoot: root, ObjectiveID: spec.ID, PolicyHash: manifest.PolicyHash,
				GitPath: filepath.Join(root, spec.ID, "git"), GitSHA256: manifest.Programs["git"].Digest,
				ModelPath: filepath.Join(root, spec.ID, "model"), ModelSHA256: manifest.Programs["model"].Digest,
				Repository: spec.Repository, TargetRef: "refs/heads/" + branch,
				Deadline: manifest.Deadline, HardStop: manifest.HardStop,
				CleanupTimeoutSeconds: spec.Recovery.CleanupTimeoutSeconds,
				DockerContext:         spec.Runtime.DockerContext, ImageDigest: spec.Runtime.ImageDigest,
			}
			if err != nil || got != want {
				t.Fatalf("admission view = %+v, %v; want %+v", got, err, want)
			}
			journal := openJournalFixture(t, root, spec.ID)
			if got.PolicyHash != journal.admission.ManifestHash {
				t.Fatal("view invented a different policy identity")
			}
		})
	}
}

func admittedViewFixture(t *testing.T) (string, ManagedSpec) {
	t.Helper()
	root, spec, reservation := managedAdmissionFixture(t)
	if _, err := AdmitManaged(t.Context(), root, spec, reservation); err != nil {
		t.Fatal(err)
	}
	return root, spec
}

func testManagedAdmissionViewNoReset(t *testing.T) {
	t.Helper()
	root, spec := admittedViewFixture(t)
	store, err := openManagedFactoryStore(t.Context(), filepath.Join(root, spec.ID))
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	state.State, state.Attempts, state.Feedback = "running", 1, "keep progress"
	if err := store.save(t.Context(), state, state.Revision); err != nil {
		t.Fatal(err)
	}
	if err := store.close(); err != nil {
		t.Fatal(err)
	}
	journal := openJournalFixture(t, root, spec.ID)
	generation, _, err := journal.beginGeneration(t.Context(), "supervisor1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	scope := journalScope("c", generation)
	if _, err := journal.reserveScope(t.Context(), scope, time.Now().Add(time.Minute), time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := journal.RecordHello(t.Context(), scope, 101, 100); err != nil {
		t.Fatal(err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	database := filepath.Join(root, spec.ID, "state.db")
	before := readAdmissionViewFile(t, database)
	first, err := ReadManagedAdmission(t.Context(), root, spec.ID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ReadManagedAdmission(t.Context(), root, spec.ID)
	if err != nil || first != second || !bytes.Equal(before, readAdmissionViewFile(t, database)) {
		t.Fatalf("metadata read changed persisted delivery/ownership state: %v", err)
	}
}

func readAdmissionViewFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func testManagedAdmissionViewFrozen(t *testing.T) {
	t.Helper()
	root, spec, reservation := managedAdmissionFixture(t)
	git := spec.Runtime.GitExecutable
	spec.Runtime.GitExecutable = filepath.Join(filepath.Dir(root), "original-git")
	invoked := filepath.Join(filepath.Dir(root), "git-invoked")
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }
	gitTestWrite(t, spec.Runtime.GitExecutable, "#!/bin/sh\necho invoked >> "+quote(invoked)+"\nexec "+quote(git)+" \"$@\"\n", 0o700)
	specPath := filepath.Join(filepath.Dir(root), "input.json")
	encoded, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	gitTestWrite(t, specPath, string(encoded), 0o600)
	reservation = reservationForIntendedSpec(t, reservation, spec)
	if _, err := AdmitManaged(t.Context(), root, spec, reservation); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{specPath, spec.Runtime.GitExecutable, spec.Runtime.ModelExecutable, invoked} {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	if view, err := ReadManagedAdmission(t.Context(), root, spec.ID); err != nil || view.GitPath != filepath.Join(root, spec.ID, "git") {
		t.Fatalf("resume depends on original inputs: %+v, %v", view, err)
	}
	if _, err := os.Lstat(invoked); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("metadata read executed frozen Git: %v", err)
	}
}

func testManagedAdmissionViewUncommitted(t *testing.T) {
	t.Helper()
	for _, stage := range []string{"marker", "frozen artifacts"} {
		t.Run(stage, func(t *testing.T) {
			root, spec, reservation := managedAdmissionFixture(t)
			interrupted := errors.New("admission interrupted")
			hooks := managedAdmissionHooks{afterMarker: func() error { return interrupted }}
			if stage == "frozen artifacts" {
				hooks = managedAdmissionHooks{beforeCommit: func(*sql.Tx) error { return interrupted }}
			}
			if _, err := admitManaged(t.Context(), root, spec, reservation, hooks); !errors.Is(err, interrupted) {
				t.Fatal(err)
			}
			if stage == "frozen artifacts" {
				if _, err := loadManagedManifest(t.Context(), root, spec.ID); err != nil {
					t.Fatalf("fixture did not publish valid frozen artifacts: %v", err)
				}
			}
			assertAdmissionViewRejected(t, root, spec.ID)
			if stage == "marker" {
				if _, err := os.Lstat(filepath.Join(root, spec.ID)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("view initialized the missing objective: %v", err)
				}
			} else if _, err := readSnapshot(t.Context(), filepath.Join(root, spec.ID)); !errors.Is(err, sql.ErrNoRows) {
				t.Fatalf("view repaired the rolled-back admission: %v", err)
			}
		})
	}
}

func assertAdmissionViewRejected(t *testing.T, root, id string) {
	t.Helper()
	if view, err := ReadManagedAdmission(t.Context(), root, id); err == nil || view != (ManagedAdmissionView{}) {
		t.Fatalf("unverified admission exposed metadata: %+v, %v", view, err)
	}
}

func testManagedAdmissionViewPaths(t *testing.T) {
	t.Helper()
	root, spec := admittedViewFixture(t)
	alias := filepath.Join(filepath.Dir(root), "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{alias, root + "/.", "state", "/", filepath.Join(filepath.Dir(root), "missing")} {
		assertAdmissionViewRejected(t, path, spec.ID)
	}
	for _, id := range []string{"", "../" + spec.ID, spec.ID + "/"} {
		assertAdmissionViewRejected(t, root, id)
	}
	if _, err := os.Lstat(filepath.Join(filepath.Dir(root), "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read initialized missing root: %v", err)
	}
}

func testManagedAdmissionViewArtifacts(t *testing.T) {
	t.Helper()
	for _, name := range []string{"marker", "manifest", "git", "model", "database"} {
		for _, damage := range []string{"missing", "corrupt", "alias"} {
			t.Run(name+"/"+damage, func(t *testing.T) {
				root, spec := admittedViewFixture(t)
				path := filepath.Join(root, spec.ID, name)
				switch name {
				case "marker":
					path = filepath.Join(root, ".managed", spec.ID+".json")
				case "manifest":
					path = filepath.Join(root, spec.ID, "managed.json")
				case "database":
					path = filepath.Join(root, spec.ID, "state.db")
				}
				if err := os.Rename(path, path+".original"); err != nil {
					t.Fatal(err)
				}
				switch damage {
				case "corrupt":
					gitTestWrite(t, path, "invalid", 0o500)
				case "alias":
					if err := os.Symlink(path+".original", path); err != nil {
						t.Fatal(err)
					}
				}
				assertAdmissionViewRejected(t, root, spec.ID)
				if damage == "missing" {
					if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("read recreated missing %s: %v", name, err)
					}
				}
			})
		}
	}
}

func testManagedAdmissionViewJournal(t *testing.T) {
	t.Helper()
	cases := map[string]func(*managedJournalAdmission){
		"objective":   func(a *managedJournalAdmission) { a.ObjectiveID = "other" },
		"policy hash": func(a *managedJournalAdmission) { a.ManifestHash = strings.Repeat("c", 64) },
		"deadlines": func(a *managedJournalAdmission) {
			a.Deadline = a.Deadline.Add(time.Second)
			a.HardStop = a.HardStop.Add(time.Second)
		},
		"recovery":    func(a *managedJournalAdmission) { a.Recovery.MaxControllerStarts++ },
		"runtime":     func(a *managedJournalAdmission) { a.Runtime.DockerContext = "other-context" },
		"reservation": func(a *managedJournalAdmission) { a.Reservation.ID = "other-reservation" },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			root, spec := admittedViewFixture(t)
			journal := openJournalFixture(t, root, spec.ID)
			admission := journal.admission
			change(&admission)
			writeAdmissionViewRow(t, journal.conn, "UPDATE managed_admissions SET body=? WHERE id=1", admission)
			assertAdmissionViewRejected(t, root, spec.ID)
		})
	}
	for _, table := range []string{"managed_admissions", "managed_supervision", "snapshots"} {
		for _, damage := range []string{"missing", "corrupt"} {
			t.Run(table+"/"+damage, func(t *testing.T) {
				root, spec := admittedViewFixture(t)
				journal := openJournalFixture(t, root, spec.ID)
				query := "DELETE FROM " + table
				if damage == "corrupt" {
					query = "UPDATE " + table + " SET body='not JSON'"
				}
				if _, err := journal.conn.ExecContext(t.Context(), query); err != nil {
					t.Fatal(err)
				}
				assertAdmissionViewRejected(t, root, spec.ID)
			})
		}
	}
}

func writeAdmissionViewRow(t *testing.T, conn *sql.Conn, query string, value any) {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(t.Context(), query, body); err != nil {
		t.Fatal(err)
	}
}

func testManagedAdmissionViewSnapshot(t *testing.T) {
	t.Helper()
	cases := map[string]func(*snapshot){
		"policy hash": func(s *snapshot) { s.PolicyHash = strings.Repeat("d", 64) },
		"objective":   func(s *snapshot) { s.Spec.Objective = "another objective" },
		"branch":      func(s *snapshot) { s.Spec.TargetBranch = "other-branch" },
		"checks":      func(s *snapshot) { s.Spec.RequiredChecks = []string{"other-check"} },
		"created":     func(s *snapshot) { s.CreatedAt = s.CreatedAt.Add(time.Second) },
		"deadline":    func(s *snapshot) { s.Deadline = s.Deadline.Add(time.Second) },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			root, spec := admittedViewFixture(t)
			journal := openJournalFixture(t, root, spec.ID)
			state, err := decodeSnapshot(journal.conn.QueryRowContext(t.Context(), "SELECT body FROM snapshots WHERE id=1"))
			if err != nil {
				t.Fatal(err)
			}
			change(&state)
			writeAdmissionViewRow(t, journal.conn, "UPDATE snapshots SET body=? WHERE id=1", state)
			assertAdmissionViewRejected(t, root, spec.ID)
		})
	}
}

func testManagedAdmissionViewStopped(t *testing.T) {
	t.Helper()
	root, spec := admittedViewFixture(t)
	manifest, err := loadManagedManifest(t.Context(), root, spec.ID)
	if err != nil {
		t.Fatal(err)
	}
	manifest.CreatedAt = manifest.CreatedAt.Add(-time.Hour)
	manifest.Deadline = manifest.Deadline.Add(-time.Hour)
	manifest.HardStop = manifest.HardStop.Add(-time.Hour)
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(root, ".managed", spec.ID+".json"), filepath.Join(root, spec.ID, "managed.json")} {
		if err := os.Chmod(path, 0o600); err != nil {
			t.Fatal(err)
		}
		gitTestWrite(t, path, string(encoded), 0o400)
		if err := os.Chmod(path, 0o400); err != nil {
			t.Fatal(err)
		}
	}
	journal := openJournalFixture(t, root, spec.ID)
	state, err := decodeSnapshot(journal.conn.QueryRowContext(t.Context(), "SELECT body FROM snapshots WHERE id=1"))
	if err != nil {
		t.Fatal(err)
	}
	state.CreatedAt, state.Deadline = manifest.CreatedAt, manifest.Deadline
	writeAdmissionViewRow(t, journal.conn, "UPDATE snapshots SET body=? WHERE id=1", state)
	writeAdmissionViewRow(t, journal.conn, "UPDATE managed_admissions SET body=? WHERE id=1", managedJournalIdentity(manifest))
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	journal = openJournalFixture(t, root, spec.ID)
	for _, desired := range []string{"stopping", "stopped"} {
		if err := journal.setDesired(t.Context(), 0, desired, "test retirement", time.Time{}); err != nil {
			t.Fatal(err)
		}
	}
	view, err := ReadManagedAdmission(t.Context(), root, spec.ID)
	if err != nil || !view.Deadline.Equal(manifest.Deadline) || !view.HardStop.Equal(manifest.HardStop) || view.HardStop.After(time.Now()) {
		t.Fatalf("expired stopped admission was reset or rejected: %+v, %v", view, err)
	}
}

func testManagedAdmissionViewContext(t *testing.T) {
	t.Helper()
	root, spec := admittedViewFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if view, err := ReadManagedAdmission(ctx, root, spec.ID); !errors.Is(err, context.Canceled) || view != (ManagedAdmissionView{}) {
		t.Fatalf("canceled read = %+v, %v", view, err)
	}
	ctx, cancel = context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()
	if view, err := ReadManagedAdmission(ctx, root, spec.ID); !errors.Is(err, context.DeadlineExceeded) || view != (ManagedAdmissionView{}) {
		t.Fatalf("expired read = %+v, %v", view, err)
	}
	journal := openJournalFixture(t, root, spec.ID)
	tx, err := journal.conn.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	ctx, cancel = context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	view, err := ReadManagedAdmission(ctx, root, spec.ID)
	// SQLite may report SQLITE_BUSY after its one-second busy timeout instead of
	// context.DeadlineExceeded. Either must fail closed within that bounded wait.
	if err == nil || !errors.Is(ctx.Err(), context.DeadlineExceeded) || view != (ManagedAdmissionView{}) || time.Since(started) > 3*time.Second {
		t.Fatalf("bounded read waited beyond its context: %+v, %v", view, err)
	}
}
