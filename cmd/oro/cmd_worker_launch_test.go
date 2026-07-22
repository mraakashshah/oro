package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"oro/pkg/protocol"

	"github.com/spf13/cobra"
)

// fakeWorkerSpawner records SpawnWorker calls for test assertions.
type fakeWorkerSpawner struct {
	calls     []workerSpawnCall
	returnErr error
	failOn    int
	failErr   error
}

type workerSpawnCall struct {
	socketPath string
	workerID   string
	logPath    string
}

func (f *fakeWorkerSpawner) SpawnWorker(socketPath, workerID, logPath string) error {
	f.calls = append(f.calls, workerSpawnCall{
		socketPath: socketPath,
		workerID:   workerID,
		logPath:    logPath,
	})
	if f.failOn > 0 && len(f.calls) == f.failOn {
		if f.failErr != nil {
			return f.failErr
		}
		return errors.New("spawn failed")
	}
	return f.returnErr
}

// createFakeSocket creates an empty file at sockPath to satisfy the socket-exists check.
func createFakeSocket(t *testing.T, sockPath string) {
	t.Helper()
	f, err := os.Create(sockPath) //nolint:gosec // test helper: path from t.TempDir()
	if err != nil {
		t.Fatalf("create fake socket: %v", err)
	}
	_ = f.Close()
}

func startWorkerLaunchReservationServer(t *testing.T, sockPath string, ack protocol.ACKPayload) <-chan protocol.DirectivePayload {
	t.Helper()
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen unix socket: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	received := make(chan protocol.DirectivePayload, 4)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		scanner := bufio.NewScanner(conn)
		if !scanner.Scan() {
			return
		}
		var msg protocol.Message
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil || msg.Directive == nil {
			return
		}
		received <- *msg.Directive
		resp := protocol.Message{Type: protocol.MsgACK, ACK: &ack}
		data, _ := json.Marshal(resp)
		_, _ = conn.Write(append(data, '\n'))
	}()
	return received
}

func startWorkerLaunchMultiDirectiveServer(t *testing.T, sockPath string, ack protocol.ACKPayload, count int) <-chan []protocol.DirectivePayload {
	t.Helper()
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen unix socket: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	done := make(chan []protocol.DirectivePayload, 1)
	go func() {
		received := make([]protocol.DirectivePayload, 0, count)
		defer func() { done <- received }()
		for len(received) < count {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			scanner := bufio.NewScanner(conn)
			if scanner.Scan() {
				var msg protocol.Message
				if err := json.Unmarshal(scanner.Bytes(), &msg); err == nil && msg.Directive != nil {
					received = append(received, *msg.Directive)
					resp := protocol.Message{Type: protocol.MsgACK, ACK: &ack}
					data, _ := json.Marshal(resp)
					_, _ = conn.Write(append(data, '\n'))
				}
			}
			_ = conn.Close()
		}
	}()
	return done
}

func shortWorkerLaunchSocketPath(t *testing.T) string {
	t.Helper()
	sockPath := filepath.Join(os.TempDir(), fmt.Sprintf("oro-launch-%d-%d.sock", os.Getpid(), time.Now().UnixNano()))
	_ = os.Remove(sockPath)
	t.Cleanup(func() { _ = os.Remove(sockPath) })
	return sockPath
}

// TestWorkerLaunchSpawnsProcess is the acceptance-criteria test for oro-18c5.4.
// It verifies that `oro worker launch` with a mock spawner creates a detached
// subprocess with correct --socket and --id flags.
func TestWorkerLaunchSpawnsProcess(t *testing.T) {
	t.Run("spawns single worker with auto-generated ID", func(t *testing.T) {
		tmpDir := t.TempDir()
		sockPath := shortWorkerLaunchSocketPath(t)
		dbPath := filepath.Join(tmpDir, "state.db")
		startWorkerLaunchReservationServer(t, sockPath, protocol.ACKPayload{OK: true, Detail: "reserved 1 worker"})

		t.Setenv("ORO_SOCKET_PATH", sockPath)
		t.Setenv("ORO_DB_PATH", dbPath)
		t.Setenv("ORO_HOME", tmpDir)

		spawner := &fakeWorkerSpawner{}
		err := runWorkerLaunch(spawner, 1, "", "")
		if err != nil {
			t.Fatalf("runWorkerLaunch returned error: %v", err)
		}

		if len(spawner.calls) != 1 {
			t.Fatalf("expected 1 spawn call, got %d", len(spawner.calls))
		}
		call := spawner.calls[0]

		// Socket path must be resolved from ResolvePaths.
		if call.socketPath != sockPath {
			t.Errorf("expected socketPath=%q, got %q", sockPath, call.socketPath)
		}

		// Worker ID must be auto-generated as ext-<timestamp>-<i>.
		if !strings.HasPrefix(call.workerID, "ext-") {
			t.Errorf("expected auto-generated ID with prefix 'ext-', got %q", call.workerID)
		}
	})

	t.Run("spawns multiple workers with count flag", func(t *testing.T) {
		tmpDir := t.TempDir()
		sockPath := shortWorkerLaunchSocketPath(t)
		dbPath := filepath.Join(tmpDir, "state.db")
		startWorkerLaunchReservationServer(t, sockPath, protocol.ACKPayload{OK: true, Detail: "reserved 3 workers"})

		t.Setenv("ORO_SOCKET_PATH", sockPath)
		t.Setenv("ORO_DB_PATH", dbPath)
		t.Setenv("ORO_HOME", tmpDir)

		spawner := &fakeWorkerSpawner{}
		err := runWorkerLaunch(spawner, 3, "", "")
		if err != nil {
			t.Fatalf("runWorkerLaunch returned error: %v", err)
		}

		if len(spawner.calls) != 3 {
			t.Fatalf("expected 3 spawn calls, got %d", len(spawner.calls))
		}

		// All workers must share the same socket path, and each must have a unique ID.
		ids := make(map[string]bool)
		for i, call := range spawner.calls {
			if call.socketPath != sockPath {
				t.Errorf("call[%d]: expected socketPath=%q, got %q", i, sockPath, call.socketPath)
			}
			if !strings.HasPrefix(call.workerID, "ext-") {
				t.Errorf("call[%d]: expected auto-generated ID with prefix 'ext-', got %q", i, call.workerID)
			}
			if ids[call.workerID] {
				t.Errorf("duplicate worker ID %q", call.workerID)
			}
			ids[call.workerID] = true
		}
	})

	t.Run("uses provided ID when --id is set (count=1 only)", func(t *testing.T) {
		tmpDir := t.TempDir()
		sockPath := shortWorkerLaunchSocketPath(t)
		dbPath := filepath.Join(tmpDir, "state.db")
		startWorkerLaunchReservationServer(t, sockPath, protocol.ACKPayload{OK: true, Detail: "reserved 1 worker"})

		t.Setenv("ORO_SOCKET_PATH", sockPath)
		t.Setenv("ORO_DB_PATH", dbPath)
		t.Setenv("ORO_HOME", tmpDir)

		spawner := &fakeWorkerSpawner{}
		err := runWorkerLaunch(spawner, 1, "my-worker", "")
		if err != nil {
			t.Fatalf("runWorkerLaunch returned error: %v", err)
		}

		if len(spawner.calls) != 1 {
			t.Fatalf("expected 1 spawn call, got %d", len(spawner.calls))
		}
		if spawner.calls[0].workerID != "my-worker" {
			t.Errorf("expected workerID=%q, got %q", "my-worker", spawner.calls[0].workerID)
		}
	})

	t.Run("returns error when socket missing (dispatcher not running)", func(t *testing.T) {
		tmpDir := t.TempDir()
		sockPath := filepath.Join(tmpDir, "oro.sock") // NOT created
		dbPath := filepath.Join(tmpDir, "state.db")

		t.Setenv("ORO_SOCKET_PATH", sockPath)
		t.Setenv("ORO_DB_PATH", dbPath)
		t.Setenv("ORO_HOME", tmpDir)

		spawner := &fakeWorkerSpawner{}
		err := runWorkerLaunch(spawner, 1, "", "")
		if err == nil {
			t.Fatal("expected error when dispatcher socket is missing")
		}
		if !strings.Contains(err.Error(), "dispatcher") {
			t.Errorf("expected error to mention 'dispatcher', got: %v", err)
		}
		if len(spawner.calls) != 0 {
			t.Error("expected no spawn calls when socket is missing")
		}
	})

	t.Run("log path is under oroHome/workers directory", func(t *testing.T) {
		tmpDir := t.TempDir()
		sockPath := shortWorkerLaunchSocketPath(t)
		dbPath := filepath.Join(tmpDir, "state.db")
		startWorkerLaunchReservationServer(t, sockPath, protocol.ACKPayload{OK: true, Detail: "reserved 1 worker"})

		t.Setenv("ORO_SOCKET_PATH", sockPath)
		t.Setenv("ORO_DB_PATH", dbPath)
		t.Setenv("ORO_HOME", tmpDir)

		spawner := &fakeWorkerSpawner{}
		err := runWorkerLaunch(spawner, 1, "w-test", "")
		if err != nil {
			t.Fatalf("runWorkerLaunch returned error: %v", err)
		}

		if len(spawner.calls) != 1 {
			t.Fatalf("expected 1 spawn call, got %d", len(spawner.calls))
		}
		expectedDir := filepath.Join(tmpDir, "workers")
		if !strings.HasPrefix(spawner.calls[0].logPath, expectedDir) {
			t.Errorf("expected logPath under %q, got %q", expectedDir, spawner.calls[0].logPath)
		}
	})
}

func TestWorkerLaunchRejectsWhenMaxWorkersCapacityIsFull(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := shortWorkerLaunchSocketPath(t)
	dbPath := filepath.Join(tmpDir, "state.db")
	startWorkerLaunchReservationServer(t, sockPath, protocol.ACKPayload{OK: false, Detail: "max workers reached: requested=1 available=0 total=2 MaxWorkers=2"})

	t.Setenv("ORO_SOCKET_PATH", sockPath)
	t.Setenv("ORO_DB_PATH", dbPath)
	t.Setenv("ORO_HOME", tmpDir)

	spawner := &fakeWorkerSpawner{}
	err := runWorkerLaunch(spawner, 1, "", "")
	if err == nil {
		t.Fatal("expected worker launch to reject when MaxWorkers capacity is full")
	}
	if !strings.Contains(err.Error(), "max workers reached") {
		t.Fatalf("error = %v, want max workers reached", err)
	}
	if len(spawner.calls) != 0 {
		t.Fatalf("spawn calls = %d, want 0 when MaxWorkers capacity is full", len(spawner.calls))
	}
}

func TestWorkerLaunchCountsPendingWorkersAgainstMaxWorkers(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := shortWorkerLaunchSocketPath(t)
	dbPath := filepath.Join(tmpDir, "state.db")
	startWorkerLaunchReservationServer(t, sockPath, protocol.ACKPayload{OK: false, Detail: "max workers reached: requested=2 available=1 total=2 MaxWorkers=3"})

	t.Setenv("ORO_SOCKET_PATH", sockPath)
	t.Setenv("ORO_DB_PATH", dbPath)
	t.Setenv("ORO_HOME", tmpDir)

	spawner := &fakeWorkerSpawner{}
	err := runWorkerLaunch(spawner, 2, "", "")
	if err == nil {
		t.Fatal("expected worker launch to reject when pending workers consume capacity")
	}
	if !strings.Contains(err.Error(), "requested=2") || !strings.Contains(err.Error(), "available=1") {
		t.Fatalf("error = %v, want requested/available capacity detail", err)
	}
	if len(spawner.calls) != 0 {
		t.Fatalf("spawn calls = %d, want 0 when requested workers exceed available capacity", len(spawner.calls))
	}
}

func TestWorkerLaunchReservesCapacityThroughDispatcherBeforeSpawning(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := shortWorkerLaunchSocketPath(t)
	dbPath := filepath.Join(tmpDir, "state.db")
	received := startWorkerLaunchReservationServer(t, sockPath, protocol.ACKPayload{OK: true, Detail: "reserved 2 workers"})

	t.Setenv("ORO_SOCKET_PATH", sockPath)
	t.Setenv("ORO_DB_PATH", dbPath)
	t.Setenv("ORO_HOME", tmpDir)

	spawner := &fakeWorkerSpawner{}
	err := runWorkerLaunch(spawner, 2, "manual", "")
	if err != nil {
		t.Fatalf("runWorkerLaunch returned error: %v", err)
	}

	got := <-received
	if got.Op != "launch-workers" {
		t.Fatalf("directive op = %q, want launch-workers", got.Op)
	}
	if !strings.Contains(got.Args, "manual-0") || !strings.Contains(got.Args, "manual-1") {
		t.Fatalf("reservation args = %q, want both worker IDs", got.Args)
	}
	if len(spawner.calls) != 2 {
		t.Fatalf("spawn calls = %d, want 2 after reservation", len(spawner.calls))
	}
}

func TestWorkerLaunchCancelsFailedAndUnspawnedReservationsOnPartialSpawnFailure(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := shortWorkerLaunchSocketPath(t)
	dbPath := filepath.Join(tmpDir, "state.db")
	received := startWorkerLaunchMultiDirectiveServer(t, sockPath, protocol.ACKPayload{OK: true, Detail: "ok"}, 2)

	t.Setenv("ORO_SOCKET_PATH", sockPath)
	t.Setenv("ORO_DB_PATH", dbPath)
	t.Setenv("ORO_HOME", tmpDir)

	spawner := &fakeWorkerSpawner{failOn: 2, failErr: errors.New("boom")}
	err := runWorkerLaunch(spawner, 3, "manual", "")
	if err == nil {
		t.Fatal("expected partial worker launch failure")
	}
	if !strings.Contains(err.Error(), "manual-1") {
		t.Fatalf("error = %v, want failed worker ID", err)
	}
	if len(spawner.calls) != 2 {
		t.Fatalf("spawn calls = %d, want first success and second failure", len(spawner.calls))
	}

	var directives []protocol.DirectivePayload
	select {
	case directives = <-received:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for launch reservation and cancellation directives")
	}
	if len(directives) != 2 {
		t.Fatalf("directives = %d, want launch plus cancel", len(directives))
	}
	if directives[0].Op != "launch-workers" {
		t.Fatalf("first directive op = %q, want launch-workers", directives[0].Op)
	}
	if directives[1].Op != "cancel-worker-launch" {
		t.Fatalf("second directive op = %q, want cancel-worker-launch", directives[1].Op)
	}

	var launch, cancel workerLaunchReservation
	if err := json.Unmarshal([]byte(directives[0].Args), &launch); err != nil {
		t.Fatalf("unmarshal launch reservation: %v", err)
	}
	if err := json.Unmarshal([]byte(directives[1].Args), &cancel); err != nil {
		t.Fatalf("unmarshal cancel reservation: %v", err)
	}
	wantLaunch := []string{"manual-0", "manual-1", "manual-2"}
	wantCancel := []string{"manual-1", "manual-2"}
	if strings.Join(launch.WorkerIDs, ",") != strings.Join(wantLaunch, ",") {
		t.Fatalf("launch worker IDs = %v, want %v", launch.WorkerIDs, wantLaunch)
	}
	if strings.Join(cancel.WorkerIDs, ",") != strings.Join(wantCancel, ",") {
		t.Fatalf("cancel worker IDs = %v, want failed and unspawned reservations %v", cancel.WorkerIDs, wantCancel)
	}
	pending := make(map[string]bool, len(launch.WorkerIDs))
	for _, id := range launch.WorkerIDs {
		pending[id] = true
	}
	for _, id := range cancel.WorkerIDs {
		delete(pending, id)
	}
	if len(pending) != 1 || !pending["manual-0"] {
		t.Fatalf("remaining reservations = %v, want only successfully spawned worker manual-0", pending)
	}
}

// TestWorkerLaunchCmdStructure verifies the cobra command hierarchy for worker launch.
func TestWorkerLaunchCmdStructure(t *testing.T) {
	cmd := newWorkerCmd()

	if cmd.Use != "worker" {
		t.Errorf("expected Use='worker', got %q", cmd.Use)
	}

	launchCmd := findSubcmd(cmd.Commands(), "launch")
	if launchCmd == nil {
		t.Fatal("expected 'launch' subcommand under 'worker'")
	}

	assertFlag(t, launchCmd, "count", "1")
	assertFlagExists(t, launchCmd, "id")
	assertFlagExists(t, launchCmd, "bead")
}

// TestWorkerLaunchBeadFlag verifies that --bead flag causes a spawn-for directive
// instead of a plain worker spawn.
func TestWorkerLaunchBeadFlag(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "oro.sock")
	dbPath := filepath.Join(tmpDir, "state.db")
	createFakeSocket(t, sockPath)

	t.Setenv("ORO_SOCKET_PATH", sockPath)
	t.Setenv("ORO_DB_PATH", dbPath)
	t.Setenv("ORO_HOME", tmpDir)

	// When --bead is set, spawner should NOT be called (directive sent instead).
	// Socket is a plain file (not a UDS listener), so dialing it fails.
	spawner := &fakeWorkerSpawner{}
	err := runWorkerLaunch(spawner, 1, "", "test-bead-id")
	if err == nil {
		t.Fatal("expected error when sending spawn-for directive to non-listening socket")
	}

	// Spawner must NOT be called when --bead is set.
	if len(spawner.calls) != 0 {
		t.Errorf("expected no spawn calls when --bead is set, got %d", len(spawner.calls))
	}
}

// TestWorkerLaunchCountValidation verifies that count < 1 returns an error.
func TestWorkerLaunchCountValidation(t *testing.T) {
	for _, count := range []int{0, -1, -100} {
		spawner := &fakeWorkerSpawner{}
		err := runWorkerLaunch(spawner, count, "", "")
		if err == nil {
			t.Errorf("count=%d: expected error, got nil", count)
		}
		if len(spawner.calls) != 0 {
			t.Errorf("count=%d: expected no spawn calls, got %d", count, len(spawner.calls))
		}
	}
}

// TestWorkerLaunchCmdRegisteredInRoot verifies worker launch subcommand is reachable from root.
func TestWorkerLaunchCmdRegisteredInRoot(t *testing.T) {
	root := newRootCmd()

	workerCmd := findSubcmd(root.Commands(), "worker")
	if workerCmd == nil {
		t.Fatal("expected 'worker' subcommand in root")
	}

	if findSubcmd(workerCmd.Commands(), "launch") == nil {
		t.Error("expected 'launch' subcommand under 'worker'")
	}
}

// TestExecWorkerSpawnerImplementsInterface verifies ExecWorkerSpawner satisfies WorkerSpawner.
func TestExecWorkerSpawnerImplementsInterface(t *testing.T) {
	var _ WorkerSpawner = &ExecWorkerSpawner{}
}

func TestExecWorkerSpawnerUsesResolvedSelfExecutable(t *testing.T) {
	repoRoot := t.TempDir()
	installedOro := filepath.Join(t.TempDir(), "oro")

	got, err := resolveTrustedSelfExecutable(
		repoRoot,
		"oro",
		func() (string, error) { return installedOro, nil },
		func(string) (string, error) { return installedOro, nil },
	)
	if err != nil {
		t.Fatalf("resolveTrustedSelfExecutable returned error: %v", err)
	}
	want := cleanExecutablePath(installedOro)
	if got != want {
		t.Fatalf("resolved executable = %q, want %q", got, want)
	}
}

func TestExternalWorkerPropagatesOracleRuntimeIdentity(t *testing.T) {
	projectRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(projectRoot, ".oro"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, ".oro", "config.yaml"), []byte("project: launch-project\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(projectRoot)
	t.Setenv("ORO_HOME", "")
	t.Setenv("ORO_PROJECT", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	sockPath := shortWorkerLaunchSocketPath(t)
	startWorkerLaunchReservationServer(t, sockPath, protocol.ACKPayload{OK: true})
	t.Setenv("ORO_SOCKET_PATH", sockPath)

	spawner := &fakeWorkerSpawner{}
	if err := runWorkerLaunch(spawner, 1, "worker", ""); err != nil {
		t.Fatalf("runWorkerLaunch returned error: %v", err)
	}
	if len(spawner.calls) != 1 {
		t.Fatalf("spawn calls = %d, want 1", len(spawner.calls))
	}
	if got, want := os.Getenv("ORO_HOME"), filepath.Join(home, ".oro"); got != want {
		t.Fatalf("ORO_HOME = %q, want %q", got, want)
	}
	if got := os.Getenv("ORO_PROJECT"); got != "launch-project" {
		t.Fatalf("ORO_PROJECT = %q, want launch-project", got)
	}
}

// --- helpers ---

// findSubcmd returns the first cobra.Command with the given name, or nil.
func findSubcmd(cmds []*cobra.Command, name string) *cobra.Command {
	for _, c := range cmds {
		if c.Name() == name {
			return c
		}
	}
	return nil
}

// assertFlag checks that a flag exists on cmd and has the expected default value.
func assertFlag(t *testing.T, cmd *cobra.Command, name, wantDefault string) {
	t.Helper()
	f := cmd.Flags().Lookup(name)
	if f == nil {
		t.Fatalf("expected --%s flag on %s", name, cmd.Name())
	}
	if f.DefValue != wantDefault {
		t.Errorf("--%s default: want %q, got %q", name, wantDefault, f.DefValue)
	}
}

// assertFlagExists checks that a flag exists on cmd.
func assertFlagExists(t *testing.T, cmd *cobra.Command, name string) {
	t.Helper()
	if cmd.Flags().Lookup(name) == nil {
		t.Fatalf("expected --%s flag on %s", name, cmd.Name())
	}
}
