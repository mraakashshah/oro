package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"oro/pkg/factory"
	"oro/pkg/storage"
)

func TestManagedAdmissionBudget(t *testing.T) {
	t.Run("fresh equality and durable idempotence", testFactoryBudgetFresh)
	t.Run("denial creates no state", testFactoryBudgetDenial)
	t.Run("active reservations and largest margin", testFactoryBudgetHeadroom)
	t.Run("same filesystem before probe", testFactoryBudgetFilesystem)
	t.Run("missing execution authority retains capacity", testFactoryBudgetMissingAuthority)
	t.Run("committed authority cannot reset", testFactoryBudgetNoReset)
	t.Run("release is exact and idempotent", testFactoryBudgetRelease)
	t.Run("cross process contention and survival", testFactoryBudgetProcesses)
	t.Run("crash atomic old or new image", testFactoryBudgetCrash)
	t.Run("cancellation during probe allocates nothing", testFactoryBudgetCanceledProbe)
	t.Run("bounded immutable authority", testFactoryBudgetAuthorityBounds)
	t.Run("special files fail without blocking", testFactoryBudgetFileTraps)
	t.Run("lock wait honors context", testFactoryBudgetLockContext)
	t.Run("canonical aliases share authority", testFactoryBudgetAliases)
	t.Run("active aggregate overflow", testFactoryBudgetOverflow)
}

func testFactoryBudgetOverflow(t *testing.T) {
	t.Helper()
	root, spec := factoryBudgetFixture(t)
	spec.Runtime.HostRequiredBytes = math.MaxInt64 - 100
	if _, err := admitFactoryHostBudget(t.Context(), root, spec, factoryBudgetProbe(math.MaxInt64)); err != nil {
		t.Fatal(err)
	}
	spec.ID = "second"
	called := false
	probe := func(context.Context, string) (storage.DiskUsage, error) {
		called = true
		return storage.DiskUsage{}, nil
	}
	if _, err := admitFactoryHostBudget(t.Context(), root, spec, probe); err == nil || called || !strings.Contains(err.Error(), "overflow") {
		t.Fatalf("overflow reached probe: called=%v, %v", called, err)
	}
}

func testFactoryBudgetCanceledProbe(t *testing.T) {
	t.Helper()
	root, spec := factoryBudgetFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	probe := func(context.Context, string) (storage.DiskUsage, error) {
		cancel()
		return storage.DiskUsage{CapacityBytes: math.MaxInt64, FreeBytes: math.MaxInt64}, nil
	}
	if _, err := admitFactoryHostBudget(ctx, root, spec, probe); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled probe admitted: %v", err)
	}
	if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled probe created authority: %v", err)
	}
}

func testFactoryBudgetAuthorityBounds(t *testing.T) {
	t.Helper()
	root, spec := factoryBudgetFixture(t)
	if _, err := admitFactoryHostBudget(t.Context(), root, spec, factoryBudgetProbe(math.MaxInt64)); err != nil {
		t.Fatal(err)
	}
	access, err := lockFactoryBudget(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	ledger, _, err := access.load(t.Context())
	if err != nil {
		access.close()
		t.Fatal(err)
	}
	base := ledger.Entries[0]
	for _, kind := range []string{"duplicate ID", "duplicate objective", "revision rollback", "negative bytes", "unknown version", "extra row"} {
		t.Run(kind, func(t *testing.T) {
			candidate := ledger
			candidate.Entries = append([]factoryBudgetEntry(nil), ledger.Entries...)
			switch kind {
			case "duplicate ID":
				another := base
				another.ObjectiveID = "second"
				candidate.Entries = append(candidate.Entries, another)
				candidate.Revision++
			case "duplicate objective":
				another := base
				another.ID = strings.Repeat("b", 32)
				candidate.Entries = append(candidate.Entries, another)
				candidate.Revision++
			case "revision rollback":
				candidate.Revision--
			case "negative bytes":
				candidate.Entries[0].Bytes = -1
			case "unknown version":
				candidate.Version++
			case "extra row":
				candidate.Entries = make([]factoryBudgetEntry, factoryBudgetMaxEntries+1)
			}
			if _, err := encodeFactoryBudgetSnapshot(candidate); err == nil {
				t.Fatal("invalid history encoded")
			}
		})
	}
	encoded, err := encodeFactoryBudgetSnapshot(ledger)
	if err != nil {
		access.close()
		t.Fatal(err)
	}
	for _, tampered := range [][]byte{append(append([]byte(nil), encoded...), byte(' ')), []byte(strings.Replace(string(encoded), `"version":1`, `"version":1,"version":1`, 1)), []byte(strings.Replace(string(encoded), base.ID, strings.Repeat("b", 32), 1))} {
		if _, err := decodeFactoryBudgetSnapshot(tampered, ledger.RootSHA256); err == nil {
			access.close()
			t.Fatal("noncanonical or bad checksum accepted")
		}
	}
	if _, err := decodeFactoryBudgetSnapshot(encoded, strings.Repeat("c", 64)); err == nil {
		access.close()
		t.Fatal("cross-root authority accepted")
	}
	ledger.Entries = make([]factoryBudgetEntry, factoryBudgetMaxEntries)
	for i := range ledger.Entries {
		entry := base
		entry.ID = fmt.Sprintf("%032x", i)
		entry.ObjectiveID = fmt.Sprintf("objective%04d", i)
		entry.Released = true
		ledger.Entries[i] = entry
	}
	ledger.Revision = 2 * factoryBudgetMaxEntries
	if err := access.publish(t.Context(), ledger, factoryBudgetHooks{}); err != nil {
		access.close()
		t.Fatal(err)
	}
	access.close()
	if _, err := admitFactoryHostBudget(t.Context(), root, spec, factoryBudgetProbe(math.MaxInt64)); err == nil || !strings.Contains(err.Error(), "lifetime") {
		t.Fatalf("lifetime cap was reset: %v", err)
	}
	// The maximum legal row has fixed-width hashes/IDs, bounded objective ID and
	// two int64 fields. Its encoded size remains below the declared 1 KiB bound.
	base.ObjectiveID = strings.Repeat("x", 64)
	base.Bytes = math.MaxInt64
	base.ReserveBytes = math.MaxInt64
	row, err := json.Marshal(base)
	if err != nil || len(row) > 1024 {
		t.Fatalf("entry exceeds 1 KiB: %d %v", len(row), err)
	}
}

func testFactoryBudgetFileTraps(t *testing.T) {
	t.Helper()
	for _, kind := range []string{"symlink", "fifo", "oversize", "mode", "stage symlink", "stage oversize"} {
		t.Run(kind, func(t *testing.T) {
			root, spec := factoryBudgetFixture(t)
			probe := factoryBudgetProbe(math.MaxInt64)
			if _, err := admitFactoryHostBudget(t.Context(), root, spec, probe); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, ".managed", factoryBudgetSnapshotName)
			valid, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(kind, "stage") {
				path = filepath.Join(root, ".managed", factoryBudgetPendingName)
			} else {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
			switch kind {
			case "symlink", "stage symlink":
				target := filepath.Join(root, "valid-but-untrusted-copy")
				if err := os.WriteFile(target, valid, 0o400); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			case "fifo":
				if err := syscall.Mkfifo(path, 0o600); err != nil {
					t.Fatal(err)
				}
			case "oversize", "stage oversize":
				f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
				if err != nil {
					t.Fatal(err)
				}
				if err := f.Truncate(factoryBudgetSnapshotBytes + 1); err != nil {
					t.Fatal(err)
				}
				_ = f.Close()
			case "mode":
				if err := os.WriteFile(path, valid, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			if _, err := admitFactoryHostBudget(ctx, root, spec, probe); err == nil {
				t.Fatal("unsafe authority adopted")
			}
		})
	}
}

func testFactoryBudgetLockContext(t *testing.T) {
	t.Helper()
	root, spec := factoryBudgetFixture(t)
	access, err := lockFactoryBudget(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer access.close()
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
	defer cancel()
	if _, err := admitFactoryHostBudget(ctx, root, spec, factoryBudgetProbe(math.MaxInt64)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lock ignored deadline: %v", err)
	}
}

func testFactoryBudgetAliases(t *testing.T) {
	t.Helper()
	root, spec := factoryBudgetFixture(t)
	first, err := admitFactoryHostBudget(t.Context(), root, spec, factoryBudgetProbe(math.MaxInt64))
	if err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(filepath.Dir(root), "state-alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	repoAlias := filepath.Join(filepath.Dir(root), "repository-alias")
	if err := os.Symlink(spec.Repository, repoAlias); err != nil {
		t.Fatal(err)
	}
	spec.Repository = repoAlias
	got, err := admitFactoryHostBudget(t.Context(), alias, spec, nil)
	if err != nil || got != first {
		t.Fatalf("alias duplicated authority: %+v %v", got, err)
	}
}

func testFactoryBudgetNoReset(t *testing.T) {
	t.Helper()
	for _, kind := range []string{"missing snapshot", "corrupt snapshot", "missing root", "missing marker", "uncommitted staging"} {
		t.Run(kind, func(t *testing.T) {
			root, spec := factoryBudgetFixture(t)
			probe := factoryBudgetProbe(3*spec.Runtime.HostRequiredBytes + 100)
			first, err := admitFactoryHostBudget(t.Context(), root, spec, probe)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, ".managed", "host-reservations.json")
			switch kind {
			case "missing snapshot":
				err = os.Remove(path)
			case "corrupt snapshot":
				err = os.Chmod(path, 0o600)
				if err == nil {
					err = os.WriteFile(path, []byte("{"), 0o600)
				}
			case "missing root":
				err = os.RemoveAll(root)
			case "missing marker":
				var matches []string
				matches, err = filepath.Glob(filepath.Join(filepath.Dir(root), ".oro-factory-budget-*.init"))
				if err == nil && len(matches) == 1 {
					err = os.Remove(matches[0])
				} else {
					t.Fatalf("marker not unique: %v %v", matches, err)
				}
			case "uncommitted staging":
				err = os.WriteFile(filepath.Join(root, ".managed", ".pending-host-reservations.json"), []byte("partial"), 0o600)
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err := admitFactoryHostBudget(t.Context(), root, spec, probe)
			if kind == "uncommitted staging" {
				if err != nil || got != first {
					t.Fatalf("valid old authority not recovered: %+v %v", got, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("lost/corrupt authority reset: %+v", got)
			}
		})
	}
}

func testFactoryBudgetRelease(t *testing.T) {
	t.Helper()
	root, spec := factoryBudgetFixture(t)
	probe := factoryBudgetProbe(spec.Runtime.HostRequiredBytes + 100)
	r, err := admitFactoryHostBudget(t.Context(), root, spec, probe)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"active", "query error", "wrong tuple"} {
		query := func(context.Context, string, string) (factory.HostReservation, bool, error) {
			if kind == "query error" {
				return factory.HostReservation{}, false, errors.New("authority lost")
			}
			got := r
			if kind == "wrong tuple" {
				got.Bytes++
			}
			return got, kind != "active", nil
		}
		if err := releaseFactoryHostReservationWithQuery(t.Context(), root, r.ID, query); err == nil {
			t.Fatalf("%s released", kind)
		}
	}
	queryCalls := 0
	query := func(_ context.Context, gotRoot, id string) (factory.HostReservation, bool, error) {
		queryCalls++
		if gotRoot != root || id != spec.ID {
			t.Errorf("wrong query identity %s %s", gotRoot, id)
		}
		return r, true, nil
	}
	for range 2 {
		if err := releaseFactoryHostReservationWithQuery(t.Context(), root, r.ID, query); err != nil {
			t.Fatal(err)
		}
	}
	if queryCalls != 1 {
		t.Fatalf("released history was reinterpreted: %d", queryCalls)
	}
	if _, err := admitFactoryHostBudget(t.Context(), root, spec, probe); err == nil {
		t.Fatal("released ID reused")
	}
	spec.ID = "next"
	if _, err := admitFactoryHostBudget(t.Context(), root, spec, probe); err != nil {
		t.Fatalf("released capacity still charged: %v", err)
	}
}

type factoryBudgetChildResult struct {
	Reservation factory.HostReservation
	Error       string
}

func factoryBudgetChildCommand(t *testing.T, root string, spec factory.ManagedSpec, free int64, checkpoint string) *exec.Cmd {
	t.Helper()
	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(filepath.Dir(root), spec.ID+"-"+checkpoint+".json")
	if err := os.WriteFile(input, data, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestManagedAdmissionBudgetChild$", "--", root, input, strconv.FormatInt(free, 10), checkpoint)
	cmd.Env = append(os.Environ(), "TMPDIR="+filepath.Dir(root))
	return cmd
}

func factoryBudgetReadChild(t *testing.T, output []byte) factoryBudgetChildResult {
	t.Helper()
	for _, line := range strings.Split(string(output), "\n") {
		if strings.HasPrefix(line, "RESULT ") {
			var result factoryBudgetChildResult
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "RESULT ")), &result); err != nil {
				t.Fatal(err)
			}
			return result
		}
	}
	t.Fatalf("missing child result: %s", output)
	return factoryBudgetChildResult{}
}

func testFactoryBudgetProcesses(t *testing.T) {
	t.Helper()
	root, spec := factoryBudgetFixture(t)
	second := spec
	second.ID = "second"
	free := spec.Runtime.HostRequiredBytes + 100
	commands := []*exec.Cmd{factoryBudgetChildCommand(t, root, spec, free, ""), factoryBudgetChildCommand(t, root, second, free, "")}
	outputs := make([][]byte, 2)
	errs := make([]error, 2)
	var workers sync.WaitGroup
	for i, cmd := range commands {
		workers.Go(func() { outputs[i], errs[i] = cmd.CombinedOutput() })
	}
	workers.Wait()
	successes := 0
	var winner factory.HostReservation
	for i, output := range outputs {
		if errs[i] != nil {
			t.Fatalf("child failed: %v %s", errs[i], output)
		}
		r := factoryBudgetReadChild(t, output)
		if r.Error == "" {
			successes++
			winner = r.Reservation
		} else if !strings.Contains(r.Error, storage.ErrRuntimeBudgetDenied.Error()) {
			t.Fatalf("contending process failed for unrelated reason: %s", r.Error)
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent reservations exceeded capacity: %s", outputs)
	}
	if winner.ObjectiveID == "second" {
		spec = second
	}
	output, err := factoryBudgetChildCommand(t, root, spec, free, "").CombinedOutput()
	if err != nil {
		t.Fatalf("reopen child: %v %s", err, output)
	}
	r := factoryBudgetReadChild(t, output)
	if r.Error != "" || r.Reservation != winner {
		t.Fatalf("reservation lost across process exit: %+v", r)
	}
}

func testFactoryBudgetCrash(t *testing.T) {
	t.Helper()
	for _, checkpoint := range []string{"before", "after"} {
		t.Run(checkpoint, func(t *testing.T) {
			root, spec := factoryBudgetFixture(t)
			free := 2*spec.Runtime.HostRequiredBytes + 100
			if _, err := admitFactoryHostBudget(t.Context(), root, spec, factoryBudgetProbe(free)); err != nil {
				t.Fatal(err)
			}
			spec.ID = "crashed"
			cmd := factoryBudgetChildCommand(t, root, spec, free, checkpoint)
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = cmd.Process.Kill() })
			scanner := bufio.NewScanner(stdout)
			if !scanner.Scan() || scanner.Text() != "CHECKPOINT" {
				t.Fatalf("crash checkpoint not reached: %q %v", scanner.Text(), scanner.Err())
			}
			if err := cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			if err := cmd.Wait(); err == nil {
				t.Fatal("child was not killed")
			}
			// A competing new reservation fits only if the interrupted transition did
			// not rename. After rename, it must remain charged through process death.
			next := spec
			next.ID = "next"
			_, err = admitFactoryHostBudget(t.Context(), root, next, factoryBudgetProbe(free))
			if checkpoint == "before" && err != nil {
				t.Fatalf("old authority unrecoverable: %v", err)
			}
			if checkpoint == "after" && !errors.Is(err, storage.ErrRuntimeBudgetDenied) {
				t.Fatalf("renamed reservation lost: %v", err)
			}
			if checkpoint == "after" {
				if _, err := admitFactoryHostBudget(t.Context(), root, spec, factoryBudgetProbe(free)); err != nil {
					t.Fatalf("new authority unrecoverable: %v", err)
				}
			}
			if _, err := os.Lstat(filepath.Join(root, ".managed", ".pending-host-reservations.json")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("uncommitted staging retained: %v", err)
			}
		})
	}
}

func TestManagedAdmissionBudgetChild(t *testing.T) {
	args := os.Args
	index := -1
	for i, arg := range args {
		if arg == "--" {
			index = i + 1
			break
		}
	}
	if index < 0 {
		return
	}
	args = args[index:]
	if len(args) != 4 {
		t.Fatal("invalid child arguments")
	}
	data, err := os.ReadFile(args[1])
	if err != nil {
		t.Fatal(err)
	}
	var spec factory.ManagedSpec
	if err := json.Unmarshal(data, &spec); err != nil {
		t.Fatal(err)
	}
	free, err := strconv.ParseInt(args[2], 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	hooks := factoryBudgetHooks{}
	pause := func() error { fmt.Println("CHECKPOINT"); <-t.Context().Done(); return t.Context().Err() }
	if args[3] == "before" {
		hooks.beforeRename = pause
	}
	if args[3] == "after" {
		hooks.afterRename = pause
	}
	r, err := admitFactoryHostBudgetWithHooks(t.Context(), args[0], spec, factoryBudgetProbe(free), hooks)
	result := factoryBudgetChildResult{Reservation: r}
	if err != nil {
		result.Error = err.Error()
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("RESULT %s\n", encoded)
}

func factoryBudgetFixture(t *testing.T) (string, factory.ManagedSpec) {
	t.Helper()
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repository := filepath.Join(parent, "repository")
	if err := os.Mkdir(repository, 0o700); err != nil {
		t.Fatal(err)
	}
	spec := factory.ManagedSpec{
		ID: "first", Objective: "add numbers", Repository: repository, TargetBranch: "factory/first",
		RequiredChecks: []string{"addition"}, MaxAttempts: 1, TimeoutSeconds: 30, StepTimeoutSeconds: 10,
		Recovery: factory.RecoveryPolicy{MaxControllerStarts: 1, MaxSupervisorStarts: 1, RestartDelaySeconds: 1,
			CleanupTimeoutSeconds: 1, ReconciliationSeconds: 1, MaxReconciliationInvocations: 1, MaxCleanupInvocations: 1},
		Runtime: factory.RuntimePolicy{DockerContext: "factory-test", ImageDigest: "sha256:" + strings.Repeat("a", 64),
			MemoryBytes: 1024, ScratchBytes: 1024, SourceBytes: 1024, HostRequiredBytes: 1, HostReserveBytes: 100,
			CPUs: 1, PIDs: 1, MaxFiles: 1, SourceInodes: 1, ScratchInodes: 1, MaxModelCalls: 1, MaxModelTurns: 1,
			MaxToolCalls: 1, MaxHostInvocations: 1, Model: "test", ModelExecutable: "/usr/bin/true", GitExecutable: "/usr/bin/git", MaxModelInvocationUSD: "0.01"},
		Acceptance: []factory.Check{{ID: "addition", Args: []string{"/bin/true"}}}, ReviewInstructions: "verify",
	}
	limits, err := factory.ManagedHostRetention(spec)
	if err != nil {
		t.Fatal(err)
	}
	spec.Runtime.HostRequiredBytes = limits.MinimumBytes
	return filepath.Join(parent, "state"), spec
}

func factoryBudgetProbe(free int64) storage.DiskProbe {
	return func(context.Context, string) (storage.DiskUsage, error) {
		return storage.DiskUsage{CapacityBytes: uint64(free), FreeBytes: uint64(free)}, nil
	}
}

func testFactoryBudgetFresh(t *testing.T) {
	t.Helper()
	root, spec := factoryBudgetFixture(t)
	calls := 0
	probe := func(_ context.Context, path string) (storage.DiskUsage, error) {
		calls++
		if path != root {
			t.Errorf("probe root = %q, want %q", path, root)
		}
		if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("probe allocated state: %v", err)
		}
		return factoryBudgetProbe(spec.Runtime.HostRequiredBytes+spec.Runtime.HostReserveBytes)(t.Context(), path)
	}
	first, err := admitFactoryHostBudget(t.Context(), root, spec, probe)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == "" || first.StateRoot != root || first.ObjectiveID != spec.ID || first.Bytes != spec.Runtime.HostRequiredBytes {
		t.Fatalf("wrong tuple: %+v", first)
	}
	if _, err := os.Lstat(filepath.Join(root, spec.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("budget created objective: %v", err)
	}
	second, err := admitFactoryHostBudget(t.Context(), root, spec, probe)
	if err != nil || first != second || calls != 1 {
		t.Fatalf("idempotence: %+v, calls=%d, %v", second, calls, err)
	}
	spec.Objective += " changed"
	if _, err := admitFactoryHostBudget(t.Context(), root, spec, probe); err == nil {
		t.Fatal("changed objective reset reservation")
	}
}

func testFactoryBudgetDenial(t *testing.T) {
	t.Helper()
	for _, kind := range []string{"denied mode", "probe error", "invalid probe", "undersized policy", "canceled"} {
		t.Run(kind, func(t *testing.T) {
			root, spec := factoryBudgetFixture(t)
			ctx := t.Context()
			probe := factoryBudgetProbe(spec.Runtime.HostRequiredBytes + spec.Runtime.HostReserveBytes - 1)
			switch kind {
			case "probe error":
				probe = func(context.Context, string) (storage.DiskUsage, error) {
					return storage.DiskUsage{}, errors.New("probe unavailable")
				}
			case "invalid probe":
				probe = factoryBudgetProbe(0)
			case "undersized policy":
				spec.Runtime.HostRequiredBytes--
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if _, err := admitFactoryHostBudget(ctx, root, spec, probe); err == nil {
				t.Fatal("unsafe budget admitted")
			}
			if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("rejection allocated state: %v", err)
			}
		})
	}
}

func testFactoryBudgetHeadroom(t *testing.T) {
	t.Helper()
	root, first := factoryBudgetFixture(t)
	first.Runtime.HostReserveBytes = 1000
	if _, err := admitFactoryHostBudget(t.Context(), root, first, factoryBudgetProbe(first.Runtime.HostRequiredBytes+1000)); err != nil {
		t.Fatal(err)
	}
	second := first
	second.ID = "second"
	second.Runtime.HostReserveBytes = 1
	if _, err := admitFactoryHostBudget(t.Context(), root, second, factoryBudgetProbe(2*first.Runtime.HostRequiredBytes+999)); !errors.Is(err, storage.ErrRuntimeBudgetDenied) {
		t.Fatalf("lost active charge or largest margin: %v", err)
	}
	if _, err := admitFactoryHostBudget(t.Context(), root, second, factoryBudgetProbe(2*first.Runtime.HostRequiredBytes+1000)); err != nil {
		t.Fatalf("exact headroom denied: %v", err)
	}
}

func testFactoryBudgetFilesystem(t *testing.T) {
	t.Helper()
	root, spec := factoryBudgetFixture(t)
	spec.Repository = "/dev"
	called := false
	_, err := admitFactoryHostBudget(t.Context(), root, spec, func(context.Context, string) (storage.DiskUsage, error) {
		called = true
		return storage.DiskUsage{}, nil
	})
	if err == nil || called || !strings.Contains(err.Error(), "filesystem") {
		t.Fatalf("filesystem mismatch reached probe: called=%v, %v", called, err)
	}
}

func testFactoryBudgetMissingAuthority(t *testing.T) {
	t.Helper()
	root, spec := factoryBudgetFixture(t)
	r, err := admitFactoryHostBudget(t.Context(), root, spec, factoryBudgetProbe(spec.Runtime.HostRequiredBytes+100))
	if err != nil {
		t.Fatal(err)
	}
	if err := releaseFactoryHostReservation(t.Context(), root, r.ID); err == nil {
		t.Fatal("missing managed authority released capacity")
	}
	spec.ID = "next"
	if _, err := admitFactoryHostBudget(t.Context(), root, spec, factoryBudgetProbe(spec.Runtime.HostRequiredBytes+100)); !errors.Is(err, storage.ErrRuntimeBudgetDenied) {
		t.Fatalf("uncertain reservation lost: %v", err)
	}
}
