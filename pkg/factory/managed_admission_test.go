package factory //nolint:testpackage // Admission crash boundaries require private transaction and artifact seams.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestManagedAdmission(t *testing.T) {
	t.Run("immutable admission and no reset", testManagedAdmissionIdentity)
	t.Run("canonical containment", testManagedAdmissionContainment)
	t.Run("canonical aliases bind one identity", testManagedAdmissionAliases)
	t.Run("marker precedes objective and blocks legacy", testManagedAdmissionMarker)
	t.Run("unpublished artifact cannot accumulate retry allocations", testManagedAdmissionPendingArtifact)
	t.Run("snapshot and journal commit together", testManagedAdmissionAtomic)
	t.Run("frozen resume without original inputs", testManagedAdmissionFrozenResume)
	t.Run("changed or corrupt authority rejected", testManagedAdmissionCorruption)
	t.Run("bounded executables and canceled admission", testManagedAdmissionBounds)
	t.Run("shared supervisor lock excludes legacy", testManagedAdmissionLock)
	t.Run("legacy objective alias cannot adopt managed state", testManagedAdmissionLegacyAlias)
	t.Run("legacy ordinary and root alias paths remain supported", testManagedAdmissionLegacyPaths)
}

func TestManagedAdmissionRejectsFIFO(t *testing.T) {
	if path := os.Getenv("ORO_FACTORY_ADMISSION_FIFO"); path != "" {
		file, err := openManagedRegular(path, managedExecutableMaxBytes, true)
		if file != nil {
			_ = file.Close()
		}
		if err == nil {
			t.Fatal("FIFO accepted as a managed regular file")
		}
		return
	}
	path := filepath.Join(t.TempDir(), "program")
	if err := syscall.Mkfifo(path, 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestManagedAdmissionRejectsFIFO$", "-test.count=1")
	command.Env = append(os.Environ(), "ORO_FACTORY_ADMISSION_FIFO="+path)
	output, err := command.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("managed regular-file validation blocked opening a FIFO: %v", ctx.Err())
	}
	if err != nil {
		t.Fatalf("FIFO rejection: %v\n%s", err, output)
	}
}

func testManagedAdmissionLegacyAlias(t *testing.T) {
	t.Helper()
	root, spec, reservation := managedAdmissionFixture(t)
	interrupted := errors.New("interrupted before managed commit")
	_, err := admitManaged(t.Context(), root, spec, reservation, managedAdmissionHooks{beforeCommit: func(*sql.Tx) error { return interrupted }})
	if !errors.Is(err, interrupted) {
		t.Fatal(err)
	}
	aliasRoot := t.TempDir()
	if err := os.Symlink(filepath.Join(root, spec.ID), filepath.Join(aliasRoot, spec.ID)); err != nil {
		t.Fatal(err)
	}
	if status, err := Run(t.Context(), aliasRoot, managedLegacySpec(spec)); err == nil || !strings.Contains(err.Error(), "managed") {
		t.Fatalf("legacy objective alias bypassed managed admission: status=%+v error=%v", status, err)
	}
	if _, err := readSnapshot(t.Context(), filepath.Join(root, spec.ID)); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("legacy objective alias published a snapshot: %v", err)
	}
}

func testManagedAdmissionLegacyPaths(t *testing.T) {
	t.Helper()
	for _, alias := range []bool{false, true} {
		root, spec, _ := managedAdmissionFixture(t)
		if alias {
			if err := os.Mkdir(root, 0o700); err != nil {
				t.Fatal(err)
			}
			aliasRoot := filepath.Join(filepath.Dir(root), "state-alias")
			if err := os.Symlink(root, aliasRoot); err != nil {
				t.Fatal(err)
			}
			root = aliasRoot
		}
		status, err := Run(t.Context(), root, managedLegacySpec(spec))
		if err != nil || status.ID != spec.ID || status.State != "failed" || status.Attempts != spec.MaxAttempts {
			t.Fatalf("ordinary legacy admission (root alias=%v) changed: %+v, %v", alias, status, err)
		}
	}
}

func managedAdmissionFixture(t *testing.T) (string, ManagedSpec, HostReservation) {
	t.Helper()
	repo, _ := gitTestRepository(t)
	repo, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	spec := managedContractFixture()
	spec.Repository, spec.TargetBranch = repo, "delivery"
	spec.Runtime.ModelExecutable = filepath.Join(root, "model")
	gitTestWrite(t, spec.Runtime.ModelExecutable, "#!/bin/sh\nexit 73\n", 0o700)
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	spec.Runtime.GitExecutable, err = filepath.EvalSymlinks(git)
	if err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "state")
	limits, err := ManagedHostRetention(spec)
	if err != nil {
		t.Fatal(err)
	}
	spec.Runtime.HostRequiredBytes = max(spec.Runtime.HostRequiredBytes, limits.MinimumBytes)
	_, digest, err := PrepareManagedSpecIdentity(spec)
	if err != nil {
		t.Fatal(err)
	}
	reservation := HostReservation{ID: "reservation-1", StateRoot: state, ObjectiveID: spec.ID, Bytes: spec.Runtime.HostRequiredBytes, SpecSHA256: digest}
	return state, spec, reservation
}

func managedLegacySpec(spec ManagedSpec) Spec {
	return Spec{
		ID: spec.ID, Objective: spec.Objective, Repository: spec.Repository, TargetBranch: spec.TargetBranch,
		RequiredChecks: spec.RequiredChecks, MaxAttempts: spec.MaxAttempts, TimeoutSeconds: spec.TimeoutSeconds, StepTimeoutSeconds: spec.StepTimeoutSeconds,
		Worker: Command{Path: spec.Runtime.ModelExecutable}, Acceptance: Command{Path: spec.Runtime.ModelExecutable}, Review: Command{Path: spec.Runtime.ModelExecutable},
	}
}

func testManagedAdmissionIdentity(t *testing.T) {
	t.Helper()
	root, spec, reservation := managedAdmissionFixture(t)
	status, err := AdmitManaged(t.Context(), root, spec, reservation)
	if err != nil || status.ID != spec.ID || status.State != "pending" || status.Attempts != 0 {
		t.Fatalf("admission = %+v, %v", status, err)
	}
	manifest, err := loadManagedManifest(t.Context(), root, spec.ID)
	if err != nil || !reflect.DeepEqual(manifest.Spec, spec) || manifest.Reservation != reservation {
		t.Fatalf("frozen identity = %+v, %v", manifest, err)
	}
	store, err := openManagedFactoryStore(t.Context(), filepath.Join(root, spec.ID))
	if err != nil {
		t.Fatal(err)
	}
	defer store.close()
	before, err := store.load(t.Context())
	if err != nil || before.PolicyHash != manifest.PolicyHash || !before.Deadline.Equal(manifest.Deadline) {
		t.Fatalf("snapshot is not bound to admission: %+v, %v", before, err)
	}
	next := before
	next.State, next.Attempts = "running", 1
	if err := store.save(t.Context(), next, before.Revision); err != nil {
		t.Fatal(err)
	}
	journal := openJournalFixture(t, root, spec.ID)
	if _, _, err := journal.beginGeneration(t.Context(), "supervisor1", time.Now()); err != nil {
		t.Fatal(err)
	}
	status, err = AdmitManaged(t.Context(), root, spec, reservation)
	if err != nil || status.State != "running" || status.Attempts != 1 {
		t.Fatalf("repeat admission reset progress: %+v, %v", status, err)
	}
	after, err := store.load(t.Context())
	if err != nil || after.Revision != before.Revision+1 || !after.Deadline.Equal(before.Deadline) {
		t.Fatalf("repeat admission changed revision/deadline: %+v, %v", after, err)
	}
	supervision, err := journal.supervision(t.Context())
	if err != nil || supervision.Generation != 1 {
		t.Fatalf("repeat admission reset generation: %+v, %v", supervision, err)
	}
}

func testManagedAdmissionContainment(t *testing.T) {
	t.Helper()
	for _, name := range []string{"state in source", "state alias in source", "nested repository", "model alias into source", "git alias into source", "objective alias"} {
		t.Run(name, func(t *testing.T) {
			root, spec, reservation := managedAdmissionFixture(t)
			switch name {
			case "state in source":
				root = filepath.Join(spec.Repository, "state")
			case "state alias in source":
				if err := os.Symlink(spec.Repository, root); err != nil {
					t.Fatal(err)
				}
			case "nested repository":
				spec.Repository = filepath.Join(spec.Repository, "nested")
				if err := os.Mkdir(spec.Repository, 0o700); err != nil {
					t.Fatal(err)
				}
			case "model alias into source", "git alias into source":
				unsafe := filepath.Join(spec.Repository, "program")
				gitTestWrite(t, unsafe, "#!/bin/sh\nexit 0\n", 0o700)
				alias := filepath.Join(filepath.Dir(root), "program-alias")
				if err := os.Symlink(unsafe, alias); err != nil {
					t.Fatal(err)
				}
				if name == "model alias into source" {
					spec.Runtime.ModelExecutable = alias
				} else {
					spec.Runtime.GitExecutable = alias
				}
			case "objective alias":
				if err := os.Mkdir(root, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(t.TempDir(), filepath.Join(root, spec.ID)); err != nil {
					t.Fatal(err)
				}
			}
			reservation.StateRoot = root
			if _, err := AdmitManaged(t.Context(), root, spec, reservation); err == nil {
				t.Fatal("unsafe admission accepted")
			}
			if _, err := os.Stat(filepath.Join(root, spec.ID, "state.db")); !os.IsNotExist(err) {
				t.Fatalf("rejected admission created objective state: %v", err)
			}
		})
	}
}

func testManagedAdmissionAliases(t *testing.T) {
	t.Helper()
	root, spec, reservation := managedAdmissionFixture(t)
	want := spec
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	aliasRoot := filepath.Join(filepath.Dir(root), "state-alias")
	spec.Repository = filepath.Join(filepath.Dir(root), "repo-alias")
	spec.Runtime.ModelExecutable = filepath.Join(filepath.Dir(root), "model-alias")
	for alias, target := range map[string]string{aliasRoot: root, spec.Repository: want.Repository, spec.Runtime.ModelExecutable: want.Runtime.ModelExecutable} {
		if err := os.Symlink(target, alias); err != nil {
			t.Fatal(err)
		}
	}
	reservation = reservationForIntendedSpec(t, reservation, spec)
	if _, err := AdmitManaged(t.Context(), aliasRoot, spec, reservation); err != nil {
		t.Fatalf("safe aliases rejected: %v", err)
	}
	manifest, err := loadManagedManifest(t.Context(), root, spec.ID)
	if err != nil || !reflect.DeepEqual(manifest.Spec, want) || manifest.Reservation.StateRoot != root {
		t.Fatalf("aliases did not bind canonical identity: %+v, %v", manifest, err)
	}
}

func testManagedAdmissionMarker(t *testing.T) {
	t.Helper()
	root, spec, reservation := managedAdmissionFixture(t)
	interrupted := errors.New("interrupted after marker")
	_, err := admitManaged(t.Context(), root, spec, reservation, managedAdmissionHooks{afterMarker: func() error {
		if _, err := os.Stat(filepath.Join(root, spec.ID)); !os.IsNotExist(err) {
			t.Fatalf("objective existed before marker boundary: %v", err)
		}
		return interrupted
	}})
	if !errors.Is(err, interrupted) {
		t.Fatalf("marker interruption = %v", err)
	}
	marker, err := readManagedMarker(root, spec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Run(t.Context(), root, managedLegacySpec(spec)); err == nil || !strings.Contains(err.Error(), "managed") {
		t.Fatalf("legacy adopted interrupted managed admission: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, spec.ID)); !os.IsNotExist(err) {
		t.Fatalf("legacy created managed objective state: %v", err)
	}
	if _, err := AdmitManaged(t.Context(), root, spec, reservation); err != nil {
		t.Fatal(err)
	}
	manifest, err := loadManagedManifest(t.Context(), root, spec.ID)
	if err != nil || !manifest.Deadline.Equal(marker.Deadline) || !manifest.CreatedAt.Equal(marker.CreatedAt) {
		t.Fatalf("retry extended interrupted admission: %+v, %v", manifest, err)
	}
}

func testManagedAdmissionPendingArtifact(t *testing.T) {
	t.Helper()
	root, spec, reservation := managedAdmissionFixture(t)
	interrupted := errors.New("interrupted after marker")
	_, err := admitManaged(t.Context(), root, spec, reservation, managedAdmissionHooks{afterMarker: func() error { return interrupted }})
	if !errors.Is(err, interrupted) {
		t.Fatal(err)
	}
	dir := filepath.Join(root, spec.ID)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	pending := filepath.Join(dir, ".pending-model")
	gitTestWrite(t, pending, "partial frozen program from interrupted publisher", 0o600)
	for range 2 {
		if _, err := AdmitManaged(t.Context(), root, spec, reservation); err == nil {
			t.Fatal("unpublished artifact was ignored and another allocation succeeded")
		}
		data, err := os.ReadFile(pending)
		if err != nil || string(data) != "partial frozen program from interrupted publisher" {
			t.Fatalf("retry replaced interrupted artifact: %q, %v", data, err)
		}
		if _, err := os.Stat(filepath.Join(dir, "model")); !os.IsNotExist(err) {
			t.Fatalf("retry published a second program allocation: %v", err)
		}
	}
}

func testManagedAdmissionAtomic(t *testing.T) {
	t.Helper()
	root, spec, reservation := managedAdmissionFixture(t)
	interrupted := errors.New("interrupted before transaction commit")
	_, err := admitManaged(t.Context(), root, spec, reservation, managedAdmissionHooks{beforeCommit: func(*sql.Tx) error { return interrupted }})
	if !errors.Is(err, interrupted) {
		t.Fatalf("transaction interruption = %v", err)
	}
	if _, err := readSnapshot(t.Context(), filepath.Join(root, spec.ID)); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("rollback published objective snapshot: %v", err)
	}
	if authority, err := OpenScopeAuthority(t.Context(), root, spec.ID); err == nil {
		_ = authority.Close()
		t.Fatal("rollback published journal authority")
	}
	if _, err := AdmitManaged(t.Context(), root, spec, reservation); err != nil {
		t.Fatalf("identical interrupted admission could not recover: %v", err)
	}
}

func testManagedAdmissionFrozenResume(t *testing.T) {
	t.Helper()
	root, spec, reservation := managedAdmissionFixture(t)
	input := filepath.Join(filepath.Dir(root), "input.json")
	encoded, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	gitTestWrite(t, input, string(encoded), 0o600)
	if _, err := AdmitManaged(t.Context(), root, spec, reservation); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{input, spec.Runtime.ModelExecutable} {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := loadManagedManifest(t.Context(), root, spec.ID); err != nil {
		t.Fatalf("frozen resume accessed original input: %v", err)
	}
	if _, err := AdmitManaged(t.Context(), root, spec, reservation); err != nil {
		t.Fatalf("identical admission depended on removed original program: %v", err)
	}
}

func testManagedAdmissionCorruption(t *testing.T) {
	t.Helper()
	root, spec, reservation := managedAdmissionFixture(t)
	if _, err := AdmitManaged(t.Context(), root, spec, reservation); err != nil {
		t.Fatal(err)
	}
	changed := spec
	changed.Objective += " changed"
	if _, err := AdmitManaged(t.Context(), root, changed, reservation); err == nil {
		t.Fatal("changed objective replaced frozen identity")
	}
	other := reservation
	other.ID += "-different"
	if _, err := AdmitManaged(t.Context(), root, spec, other); err == nil {
		t.Fatal("changed reservation replaced durable tuple")
	}
	marker := filepath.Join(root, ".managed", spec.ID+".json")
	if err := os.Chmod(marker, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := AdmitManaged(t.Context(), root, spec, reservation); err == nil {
		t.Fatal("writable managed authority marker accepted")
	}
	if err := os.Chmod(marker, 0o400); err != nil {
		t.Fatal(err)
	}
	program := filepath.Join(root, spec.ID, "model")
	if err := os.Chmod(program, 0o700); err != nil {
		t.Fatal(err)
	}
	gitTestWrite(t, program, "#!/bin/sh\nexit 0\n", 0o500)
	if _, err := loadManagedManifest(t.Context(), root, spec.ID); err == nil {
		t.Fatal("corrupt frozen program accepted")
	}
	if _, err := AdmitManaged(t.Context(), root, spec, reservation); err == nil {
		t.Fatal("admission silently replaced corrupt frozen program")
	}
}

func testManagedAdmissionBounds(t *testing.T) {
	t.Helper()
	root, spec, reservation := managedAdmissionFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := AdmitManaged(ctx, root, spec, reservation); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled admission = %v", err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("canceled admission wrote state: %v", err)
	}
	if err := os.Truncate(spec.Runtime.ModelExecutable, (512<<20)+1); err != nil {
		t.Fatal(err)
	}
	if _, err := AdmitManaged(t.Context(), root, spec, reservation); err == nil {
		t.Fatal("oversized executable accepted")
	}
	if _, err := os.Stat(filepath.Join(root, spec.ID)); !os.IsNotExist(err) {
		t.Fatalf("oversized executable created objective: %v", err)
	}
	if err := os.Truncate(spec.Runtime.ModelExecutable, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := AdmitManaged(t.Context(), root, spec, reservation); err == nil {
		t.Fatal("empty executable created an admission that cannot be resumed")
	}
}

func testManagedAdmissionLock(t *testing.T) {
	t.Helper()
	root, spec, reservation := managedAdmissionFixture(t)
	release, err := lockManagedSupervisor(root, spec.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := AdmitManaged(t.Context(), root, spec, reservation); err == nil {
		t.Fatal("admission ignored owned supervisor lock")
	}
	if _, err := Run(t.Context(), root, managedLegacySpec(spec)); err == nil {
		t.Fatal("legacy execution ignored owned supervisor lock")
	}
	if _, err := os.Stat(filepath.Join(root, spec.ID)); !os.IsNotExist(err) {
		t.Fatalf("lock contender created objective: %v", err)
	}
}

func reservationForIntendedSpec(t *testing.T, reservation HostReservation, spec ManagedSpec) HostReservation {
	t.Helper()
	_, digest, err := PrepareManagedSpecIdentity(spec)
	if err != nil {
		t.Fatal(err)
	}
	reservation.SpecSHA256 = digest
	return reservation
}
