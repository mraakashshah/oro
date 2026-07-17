package dispatcher_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"oro/pkg/dispatcher"
)

// waitFor polls condition every tick until it returns true or timeout expires.
// This is a local copy for the external test package (dispatcher_test).
func waitFor(t *testing.T, condition func() bool, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond) // short poll inside helper is OK
	}
	t.Fatalf("waitFor: condition not met within %v", timeout)
}

// TestExecProcessManager_Spawn_StoresProcessAndReturnsNonNil verifies that
// Spawn starts a real process (sleep 60), tracks it, and returns a non-nil
// *os.Process.
func TestExecProcessManager_Spawn_StoresProcessAndReturnsNonNil(t *testing.T) {
	pm := dispatcher.NewExecProcessManager("/tmp/test.sock")

	proc, err := pm.Spawn("w-01")
	if err != nil {
		t.Fatalf("Spawn returned error: %v", err)
	}
	if proc == nil { //nolint:staticcheck // checked below
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

func TestExecProcessManager_Spawn_DuplicateIDKillsExistingProcess(t *testing.T) {
	pm := dispatcher.NewExecProcessManager("/tmp/test.sock")

	first, err := pm.Spawn("w-dup")
	if err != nil {
		t.Fatalf("first Spawn returned error: %v", err)
	}
	firstPID := first.Pid

	second, err := pm.Spawn("w-dup")
	if err != nil {
		t.Fatalf("second Spawn returned error: %v", err)
	}
	secondPID := second.Pid
	t.Cleanup(func() { _ = pm.Kill("w-dup") })

	if firstPID == secondPID {
		t.Fatalf("duplicate Spawn reused PID %d, want replacement process", firstPID)
	}

	firstProc, _ := os.FindProcess(firstPID)
	waitFor(t, func() bool {
		return firstProc.Signal(syscall.Signal(0)) != nil
	}, 2*time.Second)

	if err := firstProc.Signal(syscall.Signal(0)); err == nil {
		t.Fatalf("first process PID %d survived duplicate Spawn for same worker ID", firstPID)
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

	// Wait for the process to die.
	p, _ := os.FindProcess(pid)
	waitFor(t, func() bool {
		return p.Signal(syscall.Signal(0)) != nil
	}, 2*time.Second)

	// After kill, the process should no longer be running.
	if err := p.Signal(syscall.Signal(0)); err == nil {
		t.Fatal("expected process to be dead after Kill, but signal 0 succeeded")
	}
}

// TestExecProcessManager_Kill_UnknownIDReturnsError verifies that calling
// Kill with an untracked ID returns an error.
func TestExecProcessManager_Kill_UnknownIDReturnsError(t *testing.T) {
	pm := dispatcher.NewExecProcessManager("/tmp/test.sock")
	scanCalled := false
	killCalled := false
	pm.SetResidualProcessHooks(
		func(context.Context, []string) ([]dispatcher.OwnedProcess, error) {
			scanCalled = true
			return nil, nil
		},
		func(context.Context, ...dispatcher.OwnedProcess) error {
			killCalled = true
			return nil
		},
	)

	err := pm.Kill("nonexistent")
	if err == nil {
		t.Fatal("expected error for unknown worker ID, got nil")
	}
	if scanCalled || killCalled {
		t.Fatalf("unknown-worker Kill called residual hooks: scan=%t kill=%t", scanCalled, killCalled)
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
	t.Setenv("GIT_DIR", "/repo/.git")
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
	for _, entry := range cmd.Env {
		if entry == "GIT_DIR=/repo/.git" {
			t.Fatal("worker process env leaked GIT_DIR")
		}
	}
}

func TestOroProcessManagerSpawnStripsGitEnv(t *testing.T) {
	tmpDir := t.TempDir()
	reportPath := filepath.Join(tmpDir, "worker-env.txt")
	fakeOro := filepath.Join(tmpDir, "fake-oro")
	script := fmt.Sprintf(`#!/bin/sh
printf 'PWD=%%s
GIT_DIR=%%s
GIT_WORK_TREE=%%s
GIT_INDEX_FILE=%%s
' "${PWD-unset}" "${GIT_DIR-unset}" "${GIT_WORK_TREE-unset}" "${GIT_INDEX_FILE-unset}" > %q
`, reportPath)
	if err := os.WriteFile(fakeOro, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake oro script: %v", err)
	}

	origArgs := os.Args
	os.Args = append([]string{fakeOro}, os.Args[1:]...)
	t.Cleanup(func() { os.Args = origArgs })

	mainRoot := filepath.Join(tmpDir, "main")
	t.Setenv("PWD", mainRoot)
	t.Setenv("GIT_DIR", filepath.Join(mainRoot, ".git"))
	t.Setenv("GIT_WORK_TREE", mainRoot)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(mainRoot, ".git", "index"))

	pm := dispatcher.NewOroProcessManager("/tmp/test.sock", "")
	proc, err := pm.Spawn("w-env")
	if err != nil {
		t.Fatalf("Spawn returned error: %v", err)
	}
	if proc == nil {
		t.Fatal("Spawn returned nil process")
	}
	pm.Wait()

	data, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	report := string(data)
	for _, forbidden := range []string{
		"PWD=" + mainRoot,
		"GIT_DIR=" + filepath.Join(mainRoot, ".git"),
		"GIT_WORK_TREE=" + mainRoot,
		"GIT_INDEX_FILE=" + filepath.Join(mainRoot, ".git", "index"),
	} {
		if strings.Contains(report, forbidden) {
			t.Fatalf("worker process env leaked %q in report:\n%s", forbidden, report)
		}
	}
	for _, expected := range []string{"GIT_DIR=unset", "GIT_WORK_TREE=unset", "GIT_INDEX_FILE=unset"} {
		if !strings.Contains(report, expected) {
			t.Fatalf("worker process env report missing %q:\n%s", expected, report)
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

	// Wait for the shell to spawn its child.
	var grandchildPID int
	waitFor(t, func() bool {
		out, err := exec.Command("pgrep", "-P", fmt.Sprintf("%d", parentPID)).Output() //nolint:gosec // test-only: PID from our own subprocess
		if err != nil {
			return false
		}
		_, scanErr := fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &grandchildPID)
		return scanErr == nil && grandchildPID > 0
	}, 2*time.Second)

	// Kill should terminate the entire process group.
	if err := pm.Kill("w-pgid"); err != nil {
		t.Fatalf("Kill returned error: %v", err)
	}

	// Wait for grandchild to die.
	p, _ := os.FindProcess(grandchildPID)
	waitFor(t, func() bool {
		return p.Signal(syscall.Signal(0)) != nil
	}, 2*time.Second)

	// Grandchild should be dead.
	if err := p.Signal(syscall.Signal(0)); err == nil {
		t.Errorf("grandchild process %d should be dead after Kill, but signal 0 succeeded", grandchildPID)
	}
}

func TestExecProcessManagerKillTerminatesDetachedOwnedProcess(t *testing.T) {
	if os.Getenv("ORO_TEST_DETACHED_OWNED_PROCESS_HELPER") == "1" {
		runDetachedOwnedProcessHelper(t)
		return
	}
	if os.Getenv("ORO_TEST_DETACHED_WORKER_HELPER") == "1" {
		runDetachedWorkerHelper(t)
		return
	}

	t.Setenv("ORO_SOCKET_PATH", "/tmp/wrong-project.sock")
	t.Setenv("ORO_WORKER_ID", "wrong-worker")
	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "project-a.sock")
	workerID := "worker-owned"
	pidPath := filepath.Join(tmpDir, "detached.pid")

	pm := dispatcher.NewOroProcessManager(socketPath, "")
	productionEnv := append([]string(nil), pm.CmdForWorker(workerID).Env...)
	spawnCount := 0
	pm.SetCmdFactory(func(id string) *exec.Cmd {
		spawnCount++
		if spawnCount == 1 {
			cmd := exec.Command(os.Args[0], "-test.run=^TestExecProcessManagerKillTerminatesDetachedOwnedProcess$") //nolint:gosec // test helper re-executes this binary
			cmd.Env = append(append([]string(nil), productionEnv...),
				"ORO_TEST_DETACHED_WORKER_HELPER=1",
				"ORO_TEST_DETACHED_PID_PATH="+pidPath,
			)
			return cmd
		}
		cmd := exec.Command("sleep", "3600") //nolint:gosec // test-only replacement process
		cmd.Env = testWorkerOwnershipEnv(os.Environ(), socketPath, id)
		return cmd
	})

	foreignSocket := startDetachedTestProcess(t, "/tmp/other-project.sock", workerID)
	foreignWorker := startDetachedTestProcess(t, socketPath, "worker-other")
	foreignArgv := startDetachedTestProcess(t, "/tmp/argv-only-project.sock", "worker-argv-only",
		"ORO_SOCKET_PATH="+socketPath, "ORO_WORKER_ID="+workerID)
	var ownedDetachedPID int
	t.Cleanup(func() {
		if ownedDetachedPID > 1 {
			_ = syscall.Kill(-ownedDetachedPID, syscall.SIGKILL)
			_ = syscall.Kill(ownedDetachedPID, syscall.SIGKILL)
		}
		_ = pm.Kill(workerID)
	})

	tracked, err := pm.Spawn(workerID)
	if err != nil {
		t.Fatalf("Spawn managed worker: %v", err)
	}
	ownedDetachedPID = waitForDetachedOwnership(t, pidPath, socketPath, workerID)

	if err := pm.Kill(workerID); err != nil {
		t.Fatalf("Kill managed worker: %v", err)
	}
	replacement, err := pm.Spawn(workerID)
	if err != nil {
		t.Fatalf("Spawn same-ID replacement: %v", err)
	}
	if replacement == nil {
		t.Fatal("Spawn same-ID replacement returned nil process")
	}

	if processAliveForTest(tracked.Pid) {
		t.Fatalf("tracked worker PID %d survived Kill", tracked.Pid)
	}
	if processAliveForTest(ownedDetachedPID) {
		t.Fatalf("detached owned PID %d survived Kill before same-ID Spawn returned", ownedDetachedPID)
	}
	if !processAliveForTest(foreignSocket.Process.Pid) {
		t.Fatalf("detached PID %d for another socket was killed", foreignSocket.Process.Pid)
	}
	if !processAliveForTest(foreignWorker.Process.Pid) {
		t.Fatalf("detached PID %d for another worker was killed", foreignWorker.Process.Pid)
	}
	if !processAliveForTest(foreignArgv.Process.Pid) {
		t.Fatalf("detached PID %d with ownership markers only in argv was killed", foreignArgv.Process.Pid)
	}

	for _, want := range []string{"ORO_SOCKET_PATH=" + socketPath, "ORO_WORKER_ID=" + workerID} {
		if !containsExactEnv(productionEnv, want) {
			t.Errorf("production worker environment missing exact ownership marker %q", want)
		}
	}
	for _, stale := range []string{"ORO_SOCKET_PATH=/tmp/wrong-project.sock", "ORO_WORKER_ID=wrong-worker"} {
		if containsExactEnv(productionEnv, stale) {
			t.Errorf("production worker environment retained stale ownership marker %q", stale)
		}
	}
}

func TestExecProcessManagerKillMacOSDetachedScanTerminatesOnlyExactSocketAndWorkerWithinFourSecondsWith128UnrelatedProcesses(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS kern.procargs2 performance regression")
	}

	tmpDir := t.TempDir()
	socketPath := filepath.Join(tmpDir, "project-a.sock")
	workerID := "worker-owned"
	pidPath := filepath.Join(tmpDir, "detached.pid")
	pm := dispatcher.NewOroProcessManager(socketPath, "")
	productionEnv := append([]string(nil), pm.CmdForWorker(workerID).Env...)
	pm.SetCmdFactory(func(string) *exec.Cmd {
		cmd := exec.Command(os.Args[0], "-test.run=^TestExecProcessManagerKillTerminatesDetachedOwnedProcess$") //nolint:gosec // test helper re-executes this binary
		cmd.Env = append(append([]string(nil), productionEnv...),
			"ORO_TEST_DETACHED_WORKER_HELPER=1",
			"ORO_TEST_DETACHED_PID_PATH="+pidPath,
		)
		return cmd
	})

	foreign := make([]*exec.Cmd, 0, 128)
	for index := range 128 {
		foreignSocket, foreignWorker := socketPath, workerID
		if index%2 == 0 {
			foreignSocket = filepath.Join(tmpDir, "other-project.sock")
		} else {
			foreignWorker = "worker-other"
		}
		cmd := exec.Command("sleep", "60") //nolint:gosec // controlled test helper
		cmd.Env = testWorkerOwnershipEnv(os.Environ(), foreignSocket, foreignWorker)
		if err := cmd.Start(); err != nil {
			t.Fatalf("start unrelated process %d: %v", index, err)
		}
		foreign = append(foreign, cmd)
	}
	t.Cleanup(func() {
		for _, cmd := range foreign {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}
	})

	var ownedDetachedPID int
	t.Cleanup(func() {
		if ownedDetachedPID > 1 {
			_ = syscall.Kill(-ownedDetachedPID, syscall.SIGKILL)
			_ = syscall.Kill(ownedDetachedPID, syscall.SIGKILL)
		}
		_ = pm.Kill(workerID)
	})
	if _, err := pm.Spawn(workerID); err != nil {
		t.Fatalf("spawn managed worker: %v", err)
	}
	ownedDetachedPID = waitForDetachedOwnership(t, pidPath, socketPath, workerID)

	started := time.Now()
	if err := pm.Kill(workerID); err != nil {
		t.Fatalf("kill managed worker: %v", err)
	}
	if elapsed := time.Since(started); elapsed >= 4*time.Second {
		t.Fatalf("detached scan took %v, want under 4s with 128 unrelated processes", elapsed)
	}
	if processAliveForTest(ownedDetachedPID) {
		t.Fatalf("exact owned detached PID %d survived Kill", ownedDetachedPID)
	}
	for index, cmd := range foreign {
		if !processAliveForTest(cmd.Process.Pid) {
			t.Fatalf("unrelated process %d (PID %d) was killed", index, cmd.Process.Pid)
		}
	}
}

func runDetachedWorkerHelper(t *testing.T) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestExecProcessManagerKillTerminatesDetachedOwnedProcess$") //nolint:gosec // test helper re-executes this binary
	cmd.Env = append(os.Environ(), "ORO_TEST_DETACHED_OWNED_PROCESS_HELPER=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start detached helper process: %v", err)
	}
	for {
		time.Sleep(time.Hour)
	}
}

func runDetachedOwnedProcessHelper(t *testing.T) {
	t.Helper()
	pidPath := os.Getenv("ORO_TEST_DETACHED_PID_PATH")
	env := os.Environ()
	socketIndex, workerIndex := -1, -1
	for index, entry := range env {
		if strings.HasPrefix(entry, "ORO_SOCKET_PATH=") {
			socketIndex = index
		}
		if strings.HasPrefix(entry, "ORO_WORKER_ID=") {
			workerIndex = index
		}
	}
	report := fmt.Sprintf("%d\n%s\n%s\n%d/%d/%d", os.Getpid(), os.Getenv("ORO_SOCKET_PATH"), os.Getenv("ORO_WORKER_ID"), socketIndex, workerIndex, len(env))
	if err := os.WriteFile(pidPath, []byte(report), 0o600); err != nil {
		t.Fatalf("write detached PID: %v", err)
	}
	for {
		time.Sleep(time.Hour)
	}
}

func startDetachedTestProcess(t *testing.T, socketPath, workerID string, argv ...string) *exec.Cmd {
	t.Helper()
	pidPath := filepath.Join(t.TempDir(), "detached.pid")
	args := append([]string{"-test.run=^TestExecProcessManagerKillTerminatesDetachedOwnedProcess$"}, argv...)
	cmd := exec.Command(os.Args[0], args...) //nolint:gosec // test helper re-executes this binary with controlled arguments
	cmd.Env = append(testWorkerOwnershipEnv(os.Environ(), socketPath, workerID),
		"ORO_TEST_DETACHED_OWNED_PROCESS_HELPER=1",
		"ORO_TEST_DETACHED_PID_PATH="+pidPath,
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start detached test process: %v", err)
	}
	if got := waitForDetachedOwnership(t, pidPath, socketPath, workerID); got != cmd.Process.Pid {
		t.Fatalf("detached helper reported PID %d, want %d", got, cmd.Process.Pid)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	return cmd
}

func waitForDetachedOwnership(t *testing.T, pidPath, socketPath, workerID string) int {
	t.Helper()
	pid := 0
	waitFor(t, func() bool {
		data, err := os.ReadFile(pidPath)
		if err != nil {
			return false
		}
		fields := strings.Split(strings.TrimSpace(string(data)), "\n")
		if len(fields) < 4 || fields[1] != socketPath || fields[2] != workerID {
			return false
		}
		pid, err = strconv.Atoi(fields[0])
		return err == nil && pid > 1
	}, 2*time.Second)
	return pid
}

func testWorkerOwnershipEnv(env []string, socketPath, workerID string) []string {
	out := make([]string, 0, len(env)+2)
	for _, entry := range env {
		if strings.HasPrefix(entry, "ORO_SOCKET_PATH=") || strings.HasPrefix(entry, "ORO_WORKER_ID=") {
			continue
		}
		out = append(out, entry)
	}
	return append(out, "ORO_SOCKET_PATH="+socketPath, "ORO_WORKER_ID="+workerID)
}

func containsExactEnv(env []string, want string) bool {
	for _, entry := range env {
		if entry == want {
			return true
		}
	}
	return false
}

func processAliveForTest(pid int) bool {
	return pid > 1 && syscall.Kill(pid, syscall.Signal(0)) == nil
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

	// Verify cmd.Stdout is NOT os.Stdout and is NOT nil (must be redirected to log file).
	mu.Lock()
	cmd := builtCmd
	mu.Unlock()
	if cmd == nil {
		t.Fatal("factory was never called")
	}
	if cmd.Stdout == os.Stdout {
		t.Error("expected cmd.Stdout to be log file, got os.Stdout")
	}
	if cmd.Stdout == nil {
		t.Error("expected cmd.Stdout to be set to log file, but it is nil")
	}
	if cmd.Stderr == os.Stderr {
		t.Error("expected cmd.Stderr to be log file, got os.Stderr")
	}
	if cmd.Stderr == nil {
		t.Error("expected cmd.Stderr to be set to log file, but it is nil")
	}

	// Cleanup.
	pm.Wait()
}

// TestOroProcessManagerEmptyOroHome verifies that when oroHome is empty,
// Spawn falls back to os.Stdout/os.Stderr (no log file created) and that
// cmd.Stdout and cmd.Stderr are set to os.Stdout and os.Stderr respectively.
// This kills mutations that remove the cmd.Stdout = os.Stdout or
// cmd.Stderr = os.Stderr assignments in the fallback path.
func TestOroProcessManagerEmptyOroHome(t *testing.T) {
	sockPath := "/tmp/test-empty-home.sock"

	var capturedCmd *exec.Cmd
	var mu sync.Mutex

	pm := dispatcher.NewOroProcessManager(sockPath, "")
	pm.SetCmdFactory(func(_ string) *exec.Cmd {
		mu.Lock()
		defer mu.Unlock()
		cmd := exec.Command("sleep", "0.1") //nolint:gosec // test-only dummy
		capturedCmd = cmd
		return cmd
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

	// Verify that when oroHome is empty, cmd.Stdout and cmd.Stderr are set to
	// os.Stdout and os.Stderr (the fallback path).
	mu.Lock()
	cmd := capturedCmd
	mu.Unlock()
	if cmd == nil {
		t.Fatal("factory was never called")
	}
	if cmd.Stdout != os.Stdout {
		t.Errorf("expected cmd.Stdout to be os.Stdout in fallback, got %v", cmd.Stdout)
	}
	if cmd.Stderr != os.Stderr {
		t.Errorf("expected cmd.Stderr to be os.Stderr in fallback, got %v", cmd.Stderr)
	}
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

	// Wait for the shell to spawn its child sleep process.
	var grandchildPID int
	waitFor(t, func() bool {
		out, err := exec.Command("pgrep", "-P", fmt.Sprintf("%d", parentPID)).Output() //nolint:gosec // test-only: PID from our own subprocess
		if err != nil {
			return false
		}
		_, scanErr := fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &grandchildPID)
		return scanErr == nil && grandchildPID > 0
	}, 2*time.Second)

	// Kill should terminate the entire process group.
	if killErr := pm.Kill("w-pgid-acceptance"); killErr != nil {
		t.Fatalf("Kill returned error: %v", killErr)
	}

	// Wait for grandchild to die.
	p, _ := os.FindProcess(grandchildPID)
	waitFor(t, func() bool {
		return p.Signal(syscall.Signal(0)) != nil
	}, 2*time.Second)

	// Grandchild must be dead — process group kill worked.
	if sigErr := p.Signal(syscall.Signal(0)); sigErr == nil {
		t.Errorf("grandchild process %d should be dead after Kill (process group not killed), but signal 0 succeeded", grandchildPID)
	}
}
