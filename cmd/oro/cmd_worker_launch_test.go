package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// fakeWorkerSpawner records SpawnWorker calls for test assertions.
type fakeWorkerSpawner struct {
	calls     []workerSpawnCall
	returnErr error
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

// TestWorkerLaunchSpawnsProcess is the acceptance-criteria test for oro-18c5.4.
// It verifies that `oro worker launch` with a mock spawner creates a detached
// subprocess with correct --socket and --id flags.
func TestWorkerLaunchSpawnsProcess(t *testing.T) {
	t.Run("spawns single worker with auto-generated ID", func(t *testing.T) {
		tmpDir := t.TempDir()
		sockPath := filepath.Join(tmpDir, "oro.sock")
		dbPath := filepath.Join(tmpDir, "state.db")
		createFakeSocket(t, sockPath)

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
		sockPath := filepath.Join(tmpDir, "oro.sock")
		dbPath := filepath.Join(tmpDir, "state.db")
		createFakeSocket(t, sockPath)

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
		sockPath := filepath.Join(tmpDir, "oro.sock")
		dbPath := filepath.Join(tmpDir, "state.db")
		createFakeSocket(t, sockPath)

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
		sockPath := filepath.Join(tmpDir, "oro.sock")
		dbPath := filepath.Join(tmpDir, "state.db")
		createFakeSocket(t, sockPath)

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

// TestGenerateWorkerID verifies the auto-generation logic.
func TestGenerateWorkerID(t *testing.T) {
	id1 := generateWorkerID(0)
	id2 := generateWorkerID(1)
	id3 := generateWorkerID(2)

	for i, id := range []string{id1, id2, id3} {
		if !strings.HasPrefix(id, "ext-") {
			t.Errorf("ID[%d] %q does not have prefix 'ext-'", i, id)
		}
		parts := strings.Split(id, "-")
		if len(parts) != 3 {
			t.Errorf("ID[%d] %q: expected 3 parts (ext-<ts>-<i>), got %d", i, id, len(parts))
		}
	}

	// All IDs must be unique.
	ids := map[string]bool{id1: true}
	if ids[id2] {
		t.Errorf("id1=%q and id2=%q are duplicate", id1, id2)
	}
	ids[id2] = true
	if ids[id3] {
		t.Errorf("id3=%q collides with earlier ID", id3)
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
