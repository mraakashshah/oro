package dispatcher_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"oro/pkg/dispatcher"
)

// TestExecProcessManager_Spawn_StoresProcessAndReturnsNonNil verifies that
// Spawn starts a real process (sleep 60), tracks it, and returns a non-nil
// *os.Process.
func TestExecProcessManager_Spawn_StoresProcessAndReturnsNonNil(t *testing.T) {
	pm := dispatcher.NewExecProcessManager("/tmp/test.sock")

	proc, err := pm.Spawn("w-01")
	if err != nil {
		t.Fatalf("Spawn returned error: %v", err)
	}
	if proc == nil {
		t.Fatal("Spawn returned nil process")
	}

	// Clean up: kill the spawned process.
	t.Cleanup(func() { _ = pm.Kill("w-01") })

	// Verify PID is valid (positive).
	if proc.Pid <= 0 {
		t.Fatalf("expected positive PID, got %d", proc.Pid)
	}
}

// TestExecProcessManager_Spawn_MultipleWorkers verifies that spawning
// multiple workers tracks each one independently.
func TestExecProcessManager_Spawn_MultipleWorkers(t *testing.T) {
	pm := dispatcher.NewExecProcessManager("/tmp/test.sock")

	ids := []string{"w-01", "w-02", "w-03"}
	pids := make(map[string]int)

	for _, id := range ids {
		proc, err := pm.Spawn(id)
		if err != nil {
			t.Fatalf("Spawn(%q) returned error: %v", id, err)
		}
		if proc == nil {
			t.Fatalf("Spawn(%q) returned nil process", id)
		}
		pids[id] = proc.Pid
	}

	t.Cleanup(func() {
		for _, id := range ids {
			_ = pm.Kill(id)
		}
	})

	// All PIDs should be unique.
	seen := make(map[int]bool)
	for id, pid := range pids {
		if seen[pid] {
			t.Fatalf("duplicate PID %d for worker %s", pid, id)
		}
		seen[pid] = true
	}
}

// TestExecProcessManager_Kill_SendsSignalToTrackedProcess verifies that
// Kill terminates a tracked process.
func TestExecProcessManager_Kill_SendsSignalToTrackedProcess(t *testing.T) {
	pm := dispatcher.NewExecProcessManager("/tmp/test.sock")

	proc, err := pm.Spawn("w-kill")
	if err != nil {
		t.Fatalf("Spawn returned error: %v", err)
	}
	pid := proc.Pid

	// Kill should succeed.
	if err := pm.Kill("w-kill"); err != nil {
		t.Fatalf("Kill returned error: %v", err)
	}

	// Give a moment for the process to die.
	time.Sleep(100 * time.Millisecond)

	// After kill, the process should no longer be running.
	// FindProcess always succeeds on Unix, so we send signal 0 to check.
	p, _ := os.FindProcess(pid)
	if err := p.Signal(syscall.Signal(0)); err == nil {
		t.Fatal("expected process to be dead after Kill, but signal 0 succeeded")
	}
}

// TestExecProcessManager_Kill_UnknownIDReturnsError verifies that calling
// Kill with an untracked ID returns an error.
func TestExecProcessManager_Kill_UnknownIDReturnsError(t *testing.T) {
	pm := dispatcher.NewExecProcessManager("/tmp/test.sock")

	err := pm.Kill("nonexistent")
	if err == nil {
		t.Fatal("expected error for unknown worker ID, got nil")
	}
}

// TestExecProcessManager_ConcurrentSpawn verifies that concurrent Spawn
// calls are safe (no data races or panics).
func TestExecProcessManager_ConcurrentSpawn(t *testing.T) {
	pm := dispatcher.NewExecProcessManager("/tmp/test.sock")

	const n = 10
	var wg sync.WaitGroup
	errs := make(chan error, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			_, err := pm.Spawn(id)
			if err != nil {
				errs <- err
			}
		}(fmt.Sprintf("w-concurrent-%d", i))
	}

	wg.Wait()
	close(errs)

	t.Cleanup(func() {
		for i := 0; i < n; i++ {
			_ = pm.Kill(fmt.Sprintf("w-concurrent-%d", i))
		}
	})

	for err := range errs {
		t.Fatalf("concurrent Spawn returned error: %v", err)
	}
}

// TestSpawnUsesCurrentBinary verifies that NewOroProcessManager spawns
// workers using os.Args[0] (the current binary path) instead of a
// hardcoded "oro" string. This ensures oro works without being on PATH.
func TestSpawnUsesCurrentBinary(t *testing.T) {
	pm := dispatcher.NewOroProcessManager("/tmp/test.sock", "")

	cmd := pm.CmdForWorker("w-test")
	if cmd == nil {
		t.Fatal("CmdForWorker returned nil")
	}

	want := os.Args[0]
	got := cmd.Args[0]
	if got != want {
		t.Fatalf("expected command to use os.Args[0] (%q), got %q", want, got)
	}

	// Also verify the remaining args are correct.
	expectedArgs := []string{want, "worker", "--socket", "/tmp/test.sock", "--id", "w-test"}
	if len(cmd.Args) != len(expectedArgs) {
		t.Fatalf("expected %d args, got %d: %v", len(expectedArgs), len(cmd.Args), cmd.Args)
	}
	for i, exp := range expectedArgs {
		if cmd.Args[i] != exp {
			t.Fatalf("arg[%d]: expected %q, got %q", i, exp, cmd.Args[i])
		}
	}
}

// TestExecProcessManager_Kill_AfterKillRemovesFromTracking verifies that
// a second Kill on the same ID returns an error (already removed).
func TestExecProcessManager_Kill_AfterKillRemovesFromTracking(t *testing.T) {
	pm := dispatcher.NewExecProcessManager("/tmp/test.sock")

	_, err := pm.Spawn("w-double-kill")
	if err != nil {
		t.Fatalf("Spawn returned error: %v", err)
	}

	// First kill should succeed.
	if err := pm.Kill("w-double-kill"); err != nil {
		t.Fatalf("first Kill returned error: %v", err)
	}

	// Second kill should return error (not tracked anymore).
	if err := pm.Kill("w-double-kill"); err == nil {
		t.Fatal("expected error on second Kill, got nil")
	}
}

// TestExecProcessManager_Kill_KillsProcessGroup verifies that Kill sends
// SIGTERM to the entire process group, not just the direct child. This
// prevents orphaned grandchild processes (e.g., claude spawning node/bash).
func TestExecProcessManager_Kill_KillsProcessGroup(t *testing.T) {
	pm := dispatcher.NewExecProcessManagerWithFactory("/tmp/test.sock", func(_ string) *exec.Cmd {
		// Shell spawns a background sleep, then waits. This creates a
		// process tree: sh → sleep. Without process group kill, the
		// sleep survives after sh is killed.
		return exec.Command("sh", "-c", "sleep 3600 & wait")
	})

	proc, err := pm.Spawn("w-pgid")
	if err != nil {
		t.Fatalf("Spawn returned error: %v", err)
	}
	parentPID := proc.Pid

	// Give the shell time to spawn its child.
	time.Sleep(200 * time.Millisecond)

	// Find the grandchild (sleep 3600) by parent PID via pgrep.
	out, pgrepErr := exec.Command("pgrep", "-P", fmt.Sprintf("%d", parentPID)).Output() //nolint:gosec // test-only: PID from our own subprocess
	if pgrepErr != nil {
		t.Fatalf("pgrep failed (no child of PID %d): %v", parentPID, pgrepErr)
	}
	var grandchildPID int
	if _, err := fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &grandchildPID); err != nil {
		t.Fatalf("parse grandchild PID from %q: %v", out, err)
	}

	// Kill should terminate the entire process group.
	if err := pm.Kill("w-pgid"); err != nil {
		t.Fatalf("Kill returned error: %v", err)
	}

	// Give processes time to die.
	time.Sleep(200 * time.Millisecond)

	// Grandchild should be dead.
	p, _ := os.FindProcess(grandchildPID)
	if err := p.Signal(syscall.Signal(0)); err == nil {
		t.Errorf("grandchild process %d should be dead after Kill, but signal 0 succeeded", grandchildPID)
	}
}

// TestSpawn_ReaperTracked verifies that the zombie reaper goroutine is
// tracked via a WaitGroup, allowing Wait() to block until all reapers finish.
func TestSpawn_ReaperTracked(t *testing.T) {
	// Use a short-lived process factory so the reaper completes quickly.
	pm := dispatcher.NewExecProcessManagerWithFactory("/tmp/test.sock", func(_ string) *exec.Cmd {
		return exec.Command("sleep", "0.1")
	})

	// Spawn a worker, triggering the reaper goroutine.
	proc, err := pm.Spawn("w-reaper")
	if err != nil {
		t.Fatalf("Spawn returned error: %v", err)
	}
	if proc == nil {
		t.Fatal("Spawn returned nil process")
	}

	// Wait should block until the reaper goroutine calls cmd.Wait().
	// The process exits after 0.1s, so Wait should return shortly after.
	done := make(chan struct{})
	go func() {
		pm.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Success: Wait returned, meaning the reaper goroutine finished.
	case <-time.After(3 * time.Second):
		t.Fatal("Wait() did not return within 3 seconds; reaper goroutine not tracked")
	}
}

// TestOroProcessManagerWritesToWorkerLogFile verifies that Spawn creates a
// per-worker log file at oroHome/workers/<id>/output.log and redirects
// cmd.Stdout/Stderr to it (not os.Stdout/os.Stderr).
func TestOroProcessManagerWritesToWorkerLogFile(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

	// Track the cmd built by the factory.
	var builtCmd *exec.Cmd
	var mu sync.Mutex

	pm := dispatcher.NewOroProcessManager(sockPath, tmpDir)
	// Override the factory to expose the built cmd and use a dummy process.
	pm.SetCmdFactory(func(id string) *exec.Cmd {
		mu.Lock()
		defer mu.Unlock()
		cmd := exec.Command("sleep", "0.1") //nolint:gosec // test-only dummy
		builtCmd = cmd
		return cmd
	})

	_, err := pm.Spawn("w-test")
	if err != nil {
		t.Fatalf("Spawn returned error: %v", err)
	}

	// Verify log file exists.
	logPath := filepath.Join(tmpDir, "workers", "w-test", "output.log")
	if _, err := os.Stat(logPath); err != nil {
		t.Fatalf("expected log file at %s, got error: %v", logPath, err)
	}

	// Verify cmd.Stdout is NOT os.Stdout.
	mu.Lock()
	cmd := builtCmd
	mu.Unlock()
	if cmd == nil {
		t.Fatal("factory was never called")
	}
	if cmd.Stdout == os.Stdout {
		t.Error("expected cmd.Stdout to be log file, got os.Stdout")
	}
	if cmd.Stderr == os.Stderr {
		t.Error("expected cmd.Stderr to be log file, got os.Stderr")
	}

	// Cleanup.
	pm.Wait()
}

// TestOroProcessManagerEmptyOroHome verifies that when oroHome is empty,
// Spawn falls back to os.Stdout/os.Stderr (no log file created).
func TestOroProcessManagerEmptyOroHome(t *testing.T) {
	sockPath := "/tmp/test-empty-home.sock"

	pm := dispatcher.NewOroProcessManager(sockPath, "")
	pm.SetCmdFactory(func(_ string) *exec.Cmd {
		return exec.Command("sleep", "0.1") //nolint:gosec // test-only dummy
	})

	// Spawn should succeed even with empty oroHome.
	proc, err := pm.Spawn("w-fallback")
	if err != nil {
		t.Fatalf("Spawn returned error: %v", err)
	}
	if proc == nil {
		t.Fatal("Spawn returned nil process")
	}
	pm.Wait()
}

// TestKill_KillsProcessGroup is the acceptance-criteria test for oro-jmil.3.
// It verifies that:
//  1. Spawn sets Setpgid=true so each worker gets its own process group.
//  2. Kill sends SIGTERM to the entire process group (-pgid), so descendant
//     processes (e.g., grandchildren spawned by the worker shell) are also
//     terminated — preventing orphaned claude/node/bash subtrees.
//
// Process tree: sh → sleep 3600 (background child).
// Without Setpgid+group kill, "sleep 3600" survives after sh is killed.
func TestKill_KillsProcessGroup(t *testing.T) {
	pm := dispatcher.NewExecProcessManagerWithFactory("/tmp/test.sock", func(_ string) *exec.Cmd {
		// Shell spawns a background sleep, then waits. This creates a
		// process tree: sh → sleep. Without process group kill, the
		// sleep survives after sh is killed.
		return exec.Command("sh", "-c", "sleep 3600 & wait")
	})

	proc, err := pm.Spawn("w-pgid-acceptance")
	if err != nil {
		t.Fatalf("Spawn returned error: %v", err)
	}
	parentPID := proc.Pid

	// Give the shell time to spawn its child sleep process.
	time.Sleep(200 * time.Millisecond)

	// Find the grandchild (sleep 3600) by parent PID via pgrep.
	out, pgrepErr := exec.Command("pgrep", "-P", fmt.Sprintf("%d", parentPID)).Output() //nolint:gosec // test-only: PID from our own subprocess
	if pgrepErr != nil {
		t.Fatalf("pgrep failed (no child of PID %d): %v", parentPID, pgrepErr)
	}
	var grandchildPID int
	if _, scanErr := fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &grandchildPID); scanErr != nil {
		t.Fatalf("parse grandchild PID from %q: %v", out, scanErr)
	}

	// Kill should terminate the entire process group.
	if killErr := pm.Kill("w-pgid-acceptance"); killErr != nil {
		t.Fatalf("Kill returned error: %v", killErr)
	}

	// Give processes time to die.
	time.Sleep(200 * time.Millisecond)

	// Grandchild must be dead — process group kill worked.
	p, _ := os.FindProcess(grandchildPID)
	if sigErr := p.Signal(syscall.Signal(0)); sigErr == nil {
		t.Errorf("grandchild process %d should be dead after Kill (process group not killed), but signal 0 succeeded", grandchildPID)
	}
}
