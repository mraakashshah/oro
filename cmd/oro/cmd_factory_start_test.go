package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"oro/pkg/storage"

	"golang.org/x/sys/unix"

	"oro/pkg/factory"
)

func TestFactoryStart(t *testing.T) {
	command, _, err := newRootCmd().Find([]string{"factory", "start"})
	if err != nil || command.CommandPath() != "oro factory start" {
		t.Fatalf("start command missing: %v %v", command, err)
	}
	t.Run("actual", factoryStartActualTest)
	t.Run("active deadline", factoryStartActiveDeadlineTest)
	t.Run("pending", factoryStartPendingTest)
	t.Run("reservation mismatch", factoryStartReservationMismatchTest)
	t.Run("spec identity", factoryStartSpecIdentityTest)
	t.Run("historical digest", factoryStartHistoricalDigestTest)
	t.Run("spec mismatch same reservation", factoryStartSpecMismatchReservationTest)
	t.Run("strict", factoryStartStrictTest)
	t.Run("budget", factoryStartBudgetTest)
	t.Run("disk probe", factoryStartDiskProbeTest)
	t.Run("uncertain", factoryStartUncertainTest)
	t.Run("concurrent", factoryStartConcurrentTest)
	t.Run("missing runtime", factoryStartMissingRuntimeTest)
}

type factoryStartFixture struct {
	root, specPath, oro, docker, endpoint, git, model string
	spec                                              factory.ManagedSpec
}

func newFactoryStartFixture(t *testing.T, timeout ...int) factoryStartFixture {
	t.Helper()
	root, spec := factoryBudgetFixture(t)
	if len(timeout) > 0 {
		spec.TimeoutSeconds, spec.StepTimeoutSeconds = timeout[0], 1
	}
	for _, args := range [][]string{{"init", "-b", "main", spec.Repository}, {"-C", spec.Repository, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "fixture"}, {"-C", spec.Repository, "branch", "factory/first"}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git: %s: %v", out, err)
		}
	}
	parent := filepath.Dir(root)
	oro, docker, model := filepath.Join(parent, "oro"), filepath.Join(parent, "docker"), filepath.Join(parent, "model")
	for _, path := range []string{oro, docker, model} {
		if err := os.WriteFile(path, []byte("program"), 0o500); err != nil {
			t.Fatal(err)
		}
	}
	systemGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	git := filepath.Join(parent, "git-input")
	if err := os.Symlink(systemGit, git); err != nil {
		t.Fatal(err)
	}
	spec.Runtime.GitExecutable, spec.Runtime.ModelExecutable = git, model
	specPath := filepath.Join(parent, "spec.json")
	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(specPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	socketDir, err := os.MkdirTemp("/tmp", "oro-start-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
	socketDir, err = filepath.EvalSymlinks(socketDir)
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(socketDir, "docker.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	if err := os.Chmod(socket, 0o600); err != nil {
		t.Fatal(err)
	}
	return factoryStartFixture{root, specPath, oro, docker, "unix://" + socket, git, model, spec}
}

func runFactoryStartFixture(t *testing.T, f factoryStartFixture, deps factoryStartDependencies) (string, error) {
	t.Helper()
	root := newRootCmd()
	factoryCmd, _, err := root.Find([]string{"factory"})
	if err != nil {
		t.Fatal(err)
	}
	start, _, err := factoryCmd.Find([]string{"start"})
	if err != nil {
		t.Fatal(err)
	}
	factoryCmd.RemoveCommand(start)
	factoryCmd.AddCommand(buildFactoryStartCmd(deps))
	var output bytes.Buffer
	root.SetOut(&output)
	root.SetErr(io.Discard)
	root.SetArgs([]string{"factory", "start", "--spec", f.specPath, "--state-dir", f.root, "--docker", f.docker, "--docker-endpoint", f.endpoint})
	err = root.ExecuteContext(t.Context())
	return output.String(), err
}

func factoryStartActualTest(t *testing.T) {
	f := newFactoryStartFixture(t)
	calls := 0
	deps := factoryStartDependencies{probe: factoryBudgetProbe(math.MaxInt64), executable: func() (string, error) { return f.oro, nil }, supported: func() error { return nil }, bootstrap: func(_ context.Context, plist string) error {
		calls++
		if plist != filepath.Join(f.root, f.spec.ID, "service", "service.plist") {
			t.Errorf("plist %s", plist)
		}
		return nil
	}}
	output, err := runFactoryStartFixture(t, f, deps)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, `"registration":"registered"`) || calls != 1 {
		t.Fatalf("output %s calls %d", output, calls)
	}
	deps.probe = func(context.Context, string) (storage.DiskUsage, error) {
		t.Error("active replay probed capacity")
		return storage.DiskUsage{}, errors.New("unexpected probe")
	}
	refBefore, err := factory.ReadManagedRuntimeReference(t.Context(), f.root, f.spec.ID)
	if err != nil {
		t.Fatal(err)
	}
	ledgerPath := filepath.Join(f.root, ".managed", "host-reservations.json")
	before, err := os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(f.oro); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(f.model); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(f.git); err != nil {
		t.Fatal(err)
	}
	lock, err := os.OpenFile(filepath.Join(f.root, ".managed", "locks", f.spec.ID, "objective.lock"), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	f.docker = filepath.Join(filepath.Dir(f.root), "missing-docker")
	f.endpoint = "unix:///missing/socket"
	_, err = runFactoryStartFixture(t, f, deps)
	if err != nil || calls != 1 {
		t.Fatalf("active replay calls %d: %v", calls, err)
	}
	refAfter, err := factory.ReadManagedRuntimeReference(t.Context(), f.root, f.spec.ID)
	if err != nil || refAfter != refBefore {
		t.Fatalf("active reference changed: %+v %v", refAfter, err)
	}
	after, err := os.ReadFile(ledgerPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("reservation changed")
	}
}

func factoryStartStrictTest(t *testing.T) {
	for _, kind := range []string{"duplicate", "alias", "unknown", "trailing", "oversize", "unsupported"} {
		t.Run(kind, func(t *testing.T) {
			f := newFactoryStartFixture(t)
			original, err := os.ReadFile(f.specPath)
			if err != nil {
				t.Fatal(err)
			}
			var input []byte
			switch kind {
			case "duplicate":
				input = append([]byte(`{"id":"first",`), original[1:]...)
			case "alias":
				input = bytes.Replace(original, []byte(`"id":`), []byte(`"ID":`), 1)
			case "unknown":
				input = append([]byte(`{"unknown":1,`), original[1:]...)
			case "trailing":
				input = append(original, []byte(` {}`)...)
			case "oversize":
				input = bytes.Repeat([]byte(" "), (1<<20)+1)
			default:
				input = original
			}
			if err := os.WriteFile(f.specPath, input, 0o600); err != nil {
				t.Fatal(err)
			}
			calls := 0
			deps := factoryStartDependencies{probe: func(context.Context, string) (storage.DiskUsage, error) { calls++; return storage.DiskUsage{}, nil }, executable: func() (string, error) { t.Fatal("executable accessed"); return "", nil }, supported: func() error {
				if kind == "unsupported" {
					return errors.New("unsupported")
				}
				return nil
			}, bootstrap: func(context.Context, string) error { t.Fatal("bootstrap called"); return nil }}
			if _, err := runFactoryStartFixture(t, f, deps); err == nil || calls != 0 {
				t.Fatalf("invalid start: %v probe calls %d", err, calls)
			}
			if _, err := os.Lstat(f.root); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("invalid start created state: %v", err)
			}
		})
	}
}

func factoryStartBudgetTest(t *testing.T) {
	for _, kind := range []string{"low", "probe failure"} {
		t.Run(kind, func(t *testing.T) {
			f := newFactoryStartFixture(t)
			calls := 0
			deps := factoryStartDependencies{probe: func(context.Context, string) (storage.DiskUsage, error) {
				calls++
				if kind == "probe failure" {
					return storage.DiskUsage{}, errors.New("probe failed")
				}
				return storage.DiskUsage{CapacityBytes: 1, FreeBytes: 1}, nil
			}, executable: func() (string, error) { t.Fatal("executable accessed"); return "", nil }, supported: func() error { return nil }, bootstrap: func(context.Context, string) error { t.Fatal("bootstrap called"); return nil }}
			if _, err := runFactoryStartFixture(t, f, deps); err == nil || calls != 1 {
				t.Fatalf("budget failure: %v calls %d", err, calls)
			}
			if _, err := os.Lstat(f.root); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("budget created root: %v", err)
			}
		})
	}
}

func factoryStartUncertainTest(t *testing.T) {
	f := newFactoryStartFixture(t)
	calls := 0
	deps := factoryStartDependencies{probe: factoryBudgetProbe(math.MaxInt64), executable: func() (string, error) { return f.oro, nil }, supported: func() error { return nil }, bootstrap: func(ctx context.Context, _ string) error {
		calls++
		if _, err := factory.ReadManagedRuntimeReference(ctx, f.root, f.spec.ID); err != nil {
			t.Errorf("bootstrap before activation: %v", err)
		}
		return errors.New("lost acknowledgment")
	}}
	if _, err := runFactoryStartFixture(t, f, deps); err == nil {
		t.Fatal("uncertain bootstrap accepted")
	}
	ledgerPath := filepath.Join(f.root, ".managed", "host-reservations.json")
	before, err := os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runFactoryStartFixture(t, f, deps); err == nil || calls != 1 {
		t.Fatalf("uncertain replay calls %d: %v", calls, err)
	}
	after, err := os.ReadFile(ledgerPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("uncertain replay changed reservation")
	}
}

func factoryStartConcurrentTest(t *testing.T) {
	f := newFactoryStartFixture(t)
	var calls atomic.Int32
	deps := factoryStartDependencies{probe: factoryBudgetProbe(math.MaxInt64), executable: func() (string, error) { return f.oro, nil }, supported: func() error { return nil }, bootstrap: func(context.Context, string) error { calls.Add(1); return nil }}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := runFactoryStartFixture(t, f, deps); errs <- err }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("calls %d", calls.Load())
	}
	f.spec.Objective = "changed"
	data, err := json.Marshal(f.spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.specPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runFactoryStartFixture(t, f, deps); err == nil || calls.Load() != 1 {
		t.Fatalf("mismatched spec: %v calls %d", err, calls.Load())
	}
}

func factoryStartMissingRuntimeTest(t *testing.T) {
	f := newFactoryStartFixture(t)
	calls := 0
	deps := factoryStartDependencies{probe: factoryBudgetProbe(math.MaxInt64), executable: func() (string, error) { return f.oro, nil }, supported: func() error { return nil }, bootstrap: func(context.Context, string) error { calls++; return nil }}
	if _, err := runFactoryStartFixture(t, f, deps); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(f.root, f.spec.ID, "runtime", "runtime.json")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := runFactoryStartFixture(t, f, deps); err == nil || calls != 1 {
		t.Fatalf("missing active runtime accepted: %v", err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("active runtime recreated: %v", err)
	}
}

func factoryStartReservationMismatchTest(t *testing.T) {
	f := newFactoryStartFixture(t)
	reservation, err := admitFactoryHostBudget(t.Context(), f.root, f.spec, factoryBudgetProbe(math.MaxInt64))
	if err != nil {
		t.Fatal(err)
	}
	reservation.ID = "different-reservation"
	if _, err := factory.AdmitManaged(t.Context(), f.root, f.spec, reservation); err != nil {
		t.Fatal(err)
	}
	calls := 0
	deps := factoryStartDependencies{probe: factoryBudgetProbe(math.MaxInt64), executable: func() (string, error) { return f.oro, nil }, supported: func() error { return nil }, bootstrap: func(context.Context, string) error { calls++; return nil }}
	if _, err := runFactoryStartFixture(t, f, deps); err == nil || calls != 0 {
		t.Fatalf("mismatched reservation dispatched: %v calls %d", err, calls)
	}
}

func factoryStartPendingTest(t *testing.T) {
	f := newFactoryStartFixture(t)
	reservation, err := admitFactoryHostBudget(t.Context(), f.root, f.spec, factoryBudgetProbe(math.MaxInt64))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := factory.AdmitManaged(t.Context(), f.root, f.spec, reservation); err != nil {
		t.Fatal(err)
	}
	if _, err := factory.ReadManagedRuntimeReference(t.Context(), f.root, f.spec.ID); !errors.Is(err, factory.ErrManagedRuntimePending) {
		t.Fatalf("expected pending: %v", err)
	}
	calls := 0
	deps := factoryStartDependencies{probe: factoryBudgetProbe(math.MaxInt64), executable: func() (string, error) { return f.oro, nil }, supported: func() error { return nil }, bootstrap: func(ctx context.Context, _ string) error {
		calls++
		if _, err := factory.ReadManagedRuntimeReference(ctx, f.root, f.spec.ID); err != nil {
			t.Errorf("bootstrap before activation: %v", err)
		}
		return nil
	}}
	if _, err := runFactoryStartFixture(t, f, deps); err != nil || calls != 1 {
		t.Fatalf("pending start: %v calls %d", err, calls)
	}
}

func factoryStartDiskProbeTest(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	candidate := filepath.Join(root, "missing", "objective")
	usage, err := factoryStartDiskProbe(t.Context(), candidate)
	if err != nil || usage.CapacityBytes == 0 || usage.FreeBytes > usage.CapacityBytes {
		t.Fatalf("disk probe: %+v %v", usage, err)
	}
	if _, err := os.Lstat(filepath.Join(root, "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("probe created path: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := factoryStartDiskProbe(ctx, candidate); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled probe: %v", err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := factoryStartDiskProbe(t.Context(), filepath.Join(alias, "missing")); err == nil {
		t.Fatal("aliased probe ancestor accepted")
	}
}

func factoryStartActiveDeadlineTest(t *testing.T) {
	f := newFactoryStartFixture(t, 3)
	calls := 0
	deps := factoryStartDependencies{probe: factoryBudgetProbe(math.MaxInt64), executable: func() (string, error) { return f.oro, nil }, supported: func() error { return nil }, bootstrap: func(context.Context, string) error { calls++; return nil }}
	if _, err := runFactoryStartFixture(t, f, deps); err != nil {
		t.Fatal(err)
	}
	view, err := factory.ReadManagedAdmission(t.Context(), f.root, f.spec.ID)
	if err != nil {
		t.Fatal(err)
	}
	ledgerPath := filepath.Join(f.root, ".managed", "host-reservations.json")
	before, err := os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	journalBefore := publicationJournalState(t, filepath.Join(f.root, f.spec.ID))
	objective, lock, err := openFactoryPublication(t.Context(), view, false)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := runFactoryStartFixture(t, f, deps); done <- err }()
	received := false
	timer := time.NewTimer(time.Until(view.Deadline) + 500*time.Millisecond)
	defer timer.Stop()
	select {
	case err := <-done:
		received = true
		if err == nil {
			t.Error("expired replay accepted")
		}
	case <-timer.C:
		t.Error("active replay exceeded original deadline while publication lock held")
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if err := objective.Close(); err != nil {
		t.Fatal(err)
	}
	if !received {
		<-done
	}
	if calls != 1 {
		t.Errorf("new bootstrap calls %d", calls)
	}
	after, err := os.ReadFile(ledgerPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Error("reservation changed")
	}
	if got := publicationJournalState(t, filepath.Join(f.root, f.spec.ID)); got != journalBefore {
		t.Error("journal changed")
	}
}

func factoryStartSpecMismatchReservationTest(t *testing.T) {
	f := newFactoryStartFixture(t)
	reservation, err := admitFactoryHostBudget(t.Context(), f.root, f.spec, factoryBudgetProbe(math.MaxInt64))
	if err != nil {
		t.Fatal(err)
	}
	other := f.spec
	other.Objective = "different objective"
	if _, err := factory.AdmitManaged(t.Context(), f.root, other, reservation); err == nil {
		t.Fatal("A reservation admitted B")
	}
	if _, err := os.Lstat(filepath.Join(f.root, ".managed", f.spec.ID+".json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("mismatched admission wrote marker: %v", err)
	}
	_, digest, err := factory.PrepareManagedSpecIdentity(other)
	if err != nil {
		t.Fatal(err)
	}
	reservation.SpecSHA256 = digest
	if _, err := factory.AdmitManaged(t.Context(), f.root, other, reservation); err != nil {
		t.Fatal(err)
	}
	calls := 0
	deps := factoryStartDependencies{probe: factoryBudgetProbe(math.MaxInt64), executable: func() (string, error) { return f.oro, nil }, supported: func() error { return nil }, bootstrap: func(context.Context, string) error { calls++; return nil }}
	if _, err := runFactoryStartFixture(t, f, deps); err == nil || calls != 0 {
		t.Fatalf("different admitted spec accepted: %v calls %d", err, calls)
	}
}

func factoryStartSpecIdentityTest(t *testing.T) {
	f := newFactoryStartFixture(t)
	_, original, err := factory.PrepareManagedSpecIdentity(f.spec)
	if err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(filepath.Dir(f.root), "repo-alias")
	if err := os.Symlink(f.spec.Repository, alias); err != nil {
		t.Fatal(err)
	}
	other := f.spec
	other.Repository = alias
	_, same, err := factory.PrepareManagedSpecIdentity(other)
	if err != nil || same != original {
		t.Fatalf("repository alias identity %s: %v", same, err)
	}
	other = f.spec
	other.Runtime.ModelExecutable += "-different"
	_, changed, err := factory.PrepareManagedSpecIdentity(other)
	if err != nil || changed == original {
		t.Fatalf("program path identity %s: %v", changed, err)
	}
}

func factoryStartHistoricalDigestTest(t *testing.T) {
	for _, kind := range []string{"marker-missing", "marker-malformed", "journal-missing", "journal-malformed"} {
		t.Run(kind, func(t *testing.T) {
			f := newFactoryStartFixture(t)
			calls := 0
			deps := factoryStartDependencies{probe: factoryBudgetProbe(math.MaxInt64), executable: func() (string, error) { return f.oro, nil }, supported: func() error { return nil }, bootstrap: func(context.Context, string) error { calls++; return nil }}
			if _, err := runFactoryStartFixture(t, f, deps); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(f.root, ".managed", f.spec.ID+".json")
			dbPath := filepath.Join(f.root, f.spec.ID, "state.db")
			if strings.HasPrefix(kind, "marker") {
				mutateFactoryStartMarker(t, marker, kind)
			} else {
				mutateFactoryStartJournal(t, dbPath, kind)
			}
			before := readFactoryStartDigestArtifact(t, marker, dbPath, kind)
			if _, err := factory.ReadManagedAdmission(t.Context(), f.root, f.spec.ID); err == nil {
				t.Fatal("old or malformed admission accepted")
			}
			if _, err := factory.ReadManagedRuntimeReference(t.Context(), f.root, f.spec.ID); err == nil {
				t.Fatal("old or malformed runtime reference accepted")
			}
			after := readFactoryStartDigestArtifact(t, marker, dbPath, kind)
			if !bytes.Equal(before, after) {
				t.Fatal("historical authority was changed")
			}
			if calls != 1 {
				t.Fatalf("unexpected bootstrap: %d", calls)
			}
		})
	}
}

func mutateFactoryStartDigest(t *testing.T, data []byte, kind string) []byte {
	t.Helper()
	pattern := regexp.MustCompile(`,"spec_sha256":"[0-9a-f]{64}"`)
	if len(pattern.FindAll(data, -1)) != 1 {
		t.Fatal("missing original reservation digest")
	}
	replacement := []byte(nil)
	if strings.HasSuffix(kind, "malformed") {
		replacement = []byte(`,"spec_sha256":"invalid"`)
	}
	return pattern.ReplaceAll(data, replacement)
}

func mutateFactoryStartMarker(t *testing.T, path, kind string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data = mutateFactoryStartDigest(t, data, kind)
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o400); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
}

func mutateFactoryStartJournal(t *testing.T, path, kind string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var data []byte
	if err := db.QueryRowContext(t.Context(), "SELECT body FROM managed_admissions WHERE id=1").Scan(&data); err != nil {
		t.Fatal(err)
	}
	data = mutateFactoryStartDigest(t, data, kind)
	if _, err := db.ExecContext(t.Context(), "UPDATE managed_admissions SET body=? WHERE id=1", data); err != nil {
		t.Fatal(err)
	}
}

func readFactoryStartDigestArtifact(t *testing.T, marker, path, kind string) []byte {
	t.Helper()
	if strings.HasPrefix(kind, "marker") {
		data, err := os.ReadFile(marker)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var data []byte
	if err := db.QueryRowContext(t.Context(), "SELECT body FROM managed_admissions WHERE id=1").Scan(&data); err != nil {
		t.Fatal(err)
	}
	return data
}
