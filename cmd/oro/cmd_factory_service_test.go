package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	"oro/pkg/factory"
)

func factoryServiceRegistrationTest(t *testing.T) {
	root, view, ref := factoryServiceRegistrationFixture(t, 30)
	access, err := lockFactoryService(t.Context(), root, view.ObjectiveID)
	if err != nil {
		t.Fatal(err)
	}
	defer access.Close()
	var calls int
	bootstrap := func(ctx context.Context, plist string) error {
		calls++
		if plist != filepath.Join(root, view.ObjectiveID, "service", "service.plist") {
			t.Fatalf("plist path: %s", plist)
		}
		_, reg, err := access.prepare(ctx, view, ref)
		if err != nil || reg.Phase != "dispatching" {
			t.Fatalf("bootstrap phase: %+v %v", reg, err)
		}
		return nil
	}
	if err := registerFactoryServiceLockedWithBootstrap(t.Context(), access, view, ref, bootstrap); err != nil {
		t.Fatal(err)
	}
	if err := registerFactoryServiceLockedWithBootstrap(t.Context(), access, view, ref, bootstrap); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("bootstrap calls: %d", calls)
	}
	_, reg, err := access.prepare(t.Context(), view, ref)
	if err != nil || reg.Phase != "registered" {
		t.Fatalf("registered phase: %+v %v", reg, err)
	}
}

func factoryServiceUncertainTest(t *testing.T) {
	root, view, ref := factoryServiceRegistrationFixture(t, 30)
	access, err := lockFactoryService(t.Context(), root, view.ObjectiveID)
	if err != nil {
		t.Fatal(err)
	}
	defer access.Close()
	intent, _, err := access.prepare(t.Context(), view, ref)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	bootstrap := func(context.Context, string) error { calls++; return errors.New("lost acknowledgement") }
	for range 2 {
		if err := registerFactoryServiceLockedWithBootstrap(t.Context(), access, view, ref, bootstrap); err == nil {
			t.Fatal("uncertainty accepted")
		}
	}
	actual, reg, err := access.prepare(t.Context(), view, ref)
	if err != nil || actual != intent || reg.Phase != "dispatching" || calls != 1 {
		t.Fatalf("uncertain replay: %+v %+v calls %d: %v", actual, reg, calls, err)
	}
}

func factoryServiceAckFailureTest(t *testing.T) {
	root, view, ref := factoryServiceRegistrationFixture(t, 30)
	access, err := lockFactoryService(t.Context(), root, view.ObjectiveID)
	if err != nil {
		t.Fatal(err)
	}
	defer access.Close()
	calls := 0
	stage := filepath.Join(root, view.ObjectiveID, "service", ".registration.stage")
	bootstrap := func(context.Context, string) error { calls++; return os.WriteFile(stage, []byte("interrupted"), 0o400) }
	for range 2 {
		if err := registerFactoryServiceLockedWithBootstrap(t.Context(), access, view, ref, bootstrap); err == nil {
			t.Fatal("uncertain acknowledgement accepted")
		}
	}
	data, err := os.ReadFile(stage)
	if err != nil || string(data) != "interrupted" || calls != 1 {
		t.Fatalf("stage changed or redispatched: %q calls %d: %v", data, calls, err)
	}
}

func factoryServiceConcurrentTest(t *testing.T) {
	root, view, ref := factoryServiceRegistrationFixture(t, 30)
	var calls atomic.Int32
	var wg sync.WaitGroup
	failures := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			access, err := lockFactoryService(t.Context(), root, view.ObjectiveID)
			if err != nil {
				failures <- err
				return
			}
			defer access.Close()
			failures <- registerFactoryServiceLockedWithBootstrap(t.Context(), access, view, ref, func(context.Context, string) error { calls.Add(1); return nil })
		}()
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("concurrent bootstrap calls = %d", calls.Load())
	}
}

func factoryServiceTransportTest(t *testing.T) {
	path := "/private/service.plist"
	var got []string
	run := func(_ context.Context, args ...string) (int, error) {
		got = append([]string(nil), args...)
		return 0, nil
	}
	if err := bootstrapFactoryServiceWithRunner(t.Context(), path, run); err != nil {
		t.Fatal(err)
	}
	want := []string{"bootstrap", "gui/" + strconv.Itoa(os.Getuid()), path}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("launchctl argv %q, want %q", got, want)
	}
	for _, result := range []struct {
		code int
		err  error
	}{{1, nil}, {-1, errors.New("transport")}} {
		if err := bootstrapFactoryServiceWithRunner(t.Context(), path, func(context.Context, ...string) (int, error) { return result.code, result.err }); err == nil {
			t.Fatal("launchctl failure accepted")
		}
	}
}

func TestFactoryServiceRegistration(t *testing.T) {
	if raw := os.Getenv("ORO_SERVICE_REGISTER_CHILD"); raw != "" {
		factoryServiceDeathChild(t, raw)
		return
	}
	t.Run("death", factoryServiceDeathTest)
	t.Run("invalid identity", factoryServiceInvalidIdentityTest)
	t.Run("lock ancestor", factoryServiceLockAncestorTest)
	t.Run("lock change during bootstrap", factoryServiceLockChangeDuringBootstrapTest)
	t.Run("pre-dispatch failure", factoryServicePreDispatchFailureTest)
	t.Run("deadline", factoryServiceRegistrationDeadlineTest)
	t.Run("registered", factoryServiceRegistrationTest)
	t.Run("uncertain", factoryServiceUncertainTest)
	t.Run("ack failure", factoryServiceAckFailureTest)
	t.Run("concurrent", factoryServiceConcurrentTest)
	t.Run("transport", factoryServiceTransportTest)
}

func factoryServiceDeathTest(t *testing.T) {
	root, view, ref := factoryServiceRegistrationFixture(t, 30)
	access, err := lockFactoryService(t.Context(), root, view.ObjectiveID)
	if err != nil {
		t.Fatal(err)
	}
	intent, _, err := access.prepare(t.Context(), view, ref)
	if err != nil {
		t.Fatal(err)
	}
	if err := access.Close(); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(struct {
		View factory.ManagedAdmissionView
		Ref  factory.ManagedRuntimeReference
	}{view, ref})
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestFactoryServiceRegistration$")
	child.Env = append(os.Environ(), "ORO_SERVICE_REGISTER_CHILD="+string(payload))
	if err := child.Run(); err == nil {
		t.Fatal("child survived simulated process death")
	} else {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 77 {
			t.Fatalf("child failed unexpectedly: %v", err)
		}
	}
	access, err = lockFactoryService(t.Context(), root, view.ObjectiveID)
	if err != nil {
		t.Fatal(err)
	}
	defer access.Close()
	actual, reg, err := access.prepare(t.Context(), view, ref)
	if err != nil || actual != intent || reg.Phase != "dispatching" {
		t.Fatalf("death state: %+v %+v %v", actual, reg, err)
	}
	calls := 0
	if err := registerFactoryServiceLockedWithBootstrap(t.Context(), access, view, ref, func(context.Context, string) error { calls++; return nil }); err == nil || calls != 0 {
		t.Fatalf("death replay calls %d: %v", calls, err)
	}
}

func factoryServiceDeathChild(t *testing.T, raw string) {
	t.Helper()
	var payload struct {
		View factory.ManagedAdmissionView
		Ref  factory.ManagedRuntimeReference
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatal(err)
	}
	access, err := lockFactoryService(t.Context(), payload.View.StateRoot, payload.View.ObjectiveID)
	if err != nil {
		t.Fatal(err)
	}
	if err := registerFactoryServiceLockedWithBootstrap(t.Context(), access, payload.View, payload.Ref, func(context.Context, string) error { os.Exit(77); return nil }); err != nil {
		t.Fatal(err)
	}
	t.Fatal("bootstrap did not execute")
}

func factoryServiceRegistrationDeadlineTest(t *testing.T) {
	root, view, ref := factoryServiceRegistrationFixture(t, 3)
	access, err := lockFactoryService(t.Context(), root, view.ObjectiveID)
	if err != nil {
		t.Fatal(err)
	}
	defer access.Close()
	calls := 0
	bootstrap := func(ctx context.Context, _ string) error {
		calls++
		deadline, ok := ctx.Deadline()
		if !ok || !deadline.Equal(view.Deadline) {
			t.Errorf("deadline = %v, %v", deadline, ok)
		}
		<-ctx.Done()
		return ctx.Err()
	}
	if err := registerFactoryServiceLockedWithBootstrap(t.Context(), access, view, ref, bootstrap); err == nil || calls != 1 {
		t.Fatalf("deadline bootstrap calls %d: %v", calls, err)
	}
	if err := registerFactoryServiceLockedWithBootstrap(t.Context(), access, view, ref, bootstrap); err == nil || calls != 1 {
		t.Fatalf("expired replay calls %d: %v", calls, err)
	}
}

func factoryServiceRegistrationFixture(t *testing.T, timeout int) (string, factory.ManagedAdmissionView, factory.ManagedRuntimeReference) {
	t.Helper()

	root, spec := factoryBudgetFixture(t)
	spec.TimeoutSeconds, spec.StepTimeoutSeconds = timeout, 1
	for _, args := range [][]string{{"init", "-b", "main", spec.Repository}, {"-C", spec.Repository, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "fixture"}, {"-C", spec.Repository, "branch", "factory/first"}} {
		if output, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git: %s: %v", output, err)
		}
	}
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	spec.Runtime.GitExecutable, err = filepath.EvalSymlinks(git)
	if err != nil {
		t.Fatal(err)
	}
	model := filepath.Join(filepath.Dir(root), "model")
	if err := os.WriteFile(model, []byte("model"), 0o700); err != nil {
		t.Fatal(err)
	}
	spec.Runtime.ModelExecutable = model
	reservation, err := admitFactoryHostBudget(t.Context(), root, spec, factoryBudgetProbe(math.MaxInt64))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := factory.AdmitManaged(t.Context(), root, spec, reservation); err != nil {
		t.Fatal(err)
	}
	view, err := factory.ReadManagedAdmission(t.Context(), root, spec.ID)
	if err != nil {
		t.Fatal(err)
	}
	oro, docker := filepath.Join(filepath.Dir(root), "oro-input"), filepath.Join(filepath.Dir(root), "docker-input")
	for _, path := range []string{oro, docker} {
		if err := os.WriteFile(path, []byte("program"), 0o500); err != nil {
			t.Fatal(err)
		}
	}
	socketDir, err := os.MkdirTemp("/tmp", "oro-service-")
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
	inputs := factoryRuntimeInputs{OroPath: oro, OroSHA256: factoryHostDigest([]byte("program")), DockerPath: docker, DockerSHA256: factoryHostDigest([]byte("program")), DockerEndpoint: "unix://" + socket}
	ref, err := publishFactoryRuntime(t.Context(), view, inputs)
	if err != nil {
		t.Fatal(err)
	}
	if err := factory.ActivateManaged(t.Context(), root, spec.ID, ref); err != nil {
		t.Fatal(err)
	}
	ledgerPath := filepath.Join(root, ".managed", "host-reservations.json")
	ledger, err := os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		after, err := os.ReadFile(ledgerPath)
		if err != nil || !bytes.Equal(ledger, after) {
			t.Errorf("registration changed host reservation ledger: %v", err)
		}
	})

	return root, view, ref
}

func factoryServiceInvalidIdentityTest(t *testing.T) {
	root, view, ref := factoryServiceRegistrationFixture(t, 30)
	access, err := lockFactoryService(t.Context(), root, view.ObjectiveID)
	if err != nil {
		t.Fatal(err)
	}
	defer access.Close()
	lockPath := filepath.Join(root, ".managed", "locks", view.ObjectiveID, "service.lock")
	if err := os.Chmod(lockPath, 0o644); err != nil {
		t.Fatal(err)
	}
	calls := 0
	bootstrap := func(context.Context, string) error { calls++; return nil }
	if err := registerFactoryServiceLockedWithBootstrap(t.Context(), access, view, ref, bootstrap); err == nil || calls != 0 {
		t.Fatalf("nonprivate lock accepted: %v", err)
	}
	if err := os.Chmod(lockPath, 0o600); err != nil {
		t.Fatal(err)
	}
	intent, _, err := access.prepare(t.Context(), view, ref)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, view.ObjectiveID, "service", "intent.json")
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{}"), 0o400); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	if err := registerFactoryServiceLockedWithBootstrap(t.Context(), access, view, ref, bootstrap); err == nil || calls != 0 {
		t.Fatalf("corrupt intent dispatched: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := registerFactoryServiceLockedWithBootstrap(t.Context(), access, view, ref, bootstrap); err == nil || calls != 0 {
		t.Fatalf("missing intent dispatched: %v", err)
	}
	if intent.Nonce == "" {
		t.Fatal("missing original nonce")
	}
}

func factoryServicePreDispatchFailureTest(t *testing.T) {
	root, view, ref := factoryServiceRegistrationFixture(t, 30)
	access, err := lockFactoryService(t.Context(), root, view.ObjectiveID)
	if err != nil {
		t.Fatal(err)
	}
	defer access.Close()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := registerFactoryServiceLocked(ctx, access, view, ref); err == nil {
		t.Fatal("canceled or unsupported platform accepted")
	}
	intent, _, err := access.prepare(t.Context(), view, ref)
	if err != nil {
		t.Fatal(err)
	}
	stage := filepath.Join(root, view.ObjectiveID, "service", ".registration.stage")
	if err := os.WriteFile(stage, []byte("partial"), 0o400); err != nil {
		t.Fatal(err)
	}
	calls := 0
	if err := registerFactoryServiceLockedWithBootstrap(t.Context(), access, view, ref, func(context.Context, string) error { calls++; return nil }); err == nil || calls != 0 {
		t.Fatalf("pre-dispatch uncertainty: %v, calls %d", err, calls)
	}
	data, err := os.ReadFile(stage)
	if err != nil || string(data) != "partial" {
		t.Fatalf("stage changed: %q %v", data, err)
	}
	if intent.Nonce == "" {
		t.Fatal("missing nonce")
	}
}

func factoryServiceLockAncestorTest(t *testing.T) {
	for _, kind := range []string{"symlink", "permissions", "missing"} {
		t.Run(kind, func(t *testing.T) {
			root, view, ref := factoryServiceRegistrationFixture(t, 30)
			access, err := lockFactoryService(t.Context(), root, view.ObjectiveID)
			if err != nil {
				t.Fatal(err)
			}
			defer access.Close()
			intent, before, err := access.prepare(t.Context(), view, ref)
			if err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(root, ".managed", "locks", view.ObjectiveID)
			moved := dir + "-moved"
			switch kind {
			case "symlink", "missing":
				if err := os.Rename(dir, moved); err != nil {
					t.Fatal(err)
				}
				if kind == "symlink" {
					if err := os.Symlink(moved, dir); err != nil {
						t.Fatal(err)
					}
				}
			case "permissions":
				if err := os.Chmod(dir, 0o777); err != nil {
					t.Fatal(err)
				}
			}
			calls := 0
			err = registerFactoryServiceLockedWithBootstrap(t.Context(), access, view, ref, func(context.Context, string) error { calls++; return nil })
			switch kind {
			case "symlink":
				if err := os.Remove(dir); err != nil {
					t.Fatal(err)
				}
				fallthrough
			case "missing":
				if err := os.Rename(moved, dir); err != nil {
					t.Fatal(err)
				}
			case "permissions":
				if err := os.Chmod(dir, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if err == nil || calls != 0 {
				t.Fatalf("%s lock ancestor accepted: %v, calls %d", kind, err, calls)
			}
			afterIntent, after, err := access.prepare(t.Context(), view, ref)
			if err != nil || afterIntent != intent || after != before {
				t.Fatalf("phase changed: %+v %+v %v", afterIntent, after, err)
			}
		})
	}
}

func factoryServiceLockChangeDuringBootstrapTest(t *testing.T) {
	root, view, ref := factoryServiceRegistrationFixture(t, 30)
	access, err := lockFactoryService(t.Context(), root, view.ObjectiveID)
	if err != nil {
		t.Fatal(err)
	}
	defer access.Close()
	intent, _, err := access.prepare(t.Context(), view, ref)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, ".managed", "locks", view.ObjectiveID)
	calls := 0
	err = registerFactoryServiceLockedWithBootstrap(t.Context(), access, view, ref, func(context.Context, string) error { calls++; return os.Chmod(dir, 0o777) })
	if chmodErr := os.Chmod(dir, 0o700); chmodErr != nil {
		t.Fatal(chmodErr)
	}
	if err == nil || calls != 1 {
		t.Fatalf("lock change acknowledged: %v, calls %d", err, calls)
	}
	actual, reg, err := access.prepare(t.Context(), view, ref)
	if err != nil || actual != intent || reg.Phase != "dispatching" {
		t.Fatalf("changed lock state: %+v %+v %v", actual, reg, err)
	}
}
