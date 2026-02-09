package dispatcher_test

import (
	"fmt"
	"os"
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
