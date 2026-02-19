package dispatcher

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// ExecProcessManager implements ProcessManager by spawning worker
// subprocesses and tracking them for lifecycle management.
//
// Thread-safe: all access to the process map is protected by a mutex.
type ExecProcessManager struct {
	socketPath string
	oroHome    string
	mu         sync.Mutex
	procs      map[string]*os.Process
	wg         sync.WaitGroup

	// cmdFactory builds the exec.Cmd for a given worker ID.
	// Defaults to spawning `oro worker --socket <socketPath> --id <id>`.
	// Tests can override this to spawn a dummy command like `sleep`.
	cmdFactory func(id string) *exec.Cmd
}

// NewExecProcessManager creates a new ExecProcessManager that spawns
// `sleep 3600` subprocesses (suitable for unit tests). For production use,
// call NewOroProcessManager which spawns real `oro worker` processes.
//
//oro:testonly
func NewExecProcessManager(socketPath string) *ExecProcessManager {
	pm := &ExecProcessManager{
		socketPath: socketPath,
		procs:      make(map[string]*os.Process),
	}
	// Default to sleep for unit testing (no actual oro binary needed).
	pm.cmdFactory = func(_ string) *exec.Cmd {
		//nolint:gosec // test-only dummy process
		return exec.CommandContext(context.Background(), "sleep", "3600")
	}
	return pm
}

// NewOroProcessManager creates an ExecProcessManager that spawns real
// `oro worker` processes with the given socket path and worker ID.
// When oroHome is non-empty, each spawned worker writes its output to
// oroHome/workers/<id>/output.log; if oroHome is empty, output falls back
// to os.Stdout/os.Stderr with a warning.
func NewOroProcessManager(socketPath, oroHome string) *ExecProcessManager {
	pm := &ExecProcessManager{
		socketPath: socketPath,
		oroHome:    oroHome,
		procs:      make(map[string]*os.Process),
	}
	self := os.Args[0]
	pm.cmdFactory = func(id string) *exec.Cmd {
		//nolint:gosec // intentionally spawning worker subprocess
		return exec.CommandContext(context.Background(), self, "worker", "--socket", socketPath, "--id", id)
	}
	return pm
}

// NewExecProcessManagerWithFactory creates an ExecProcessManager with a
// custom command factory. Useful for tests that need to control the
// subprocess (e.g., to verify process group isolation).
//
//oro:testonly
func NewExecProcessManagerWithFactory(socketPath string, factory func(id string) *exec.Cmd) *ExecProcessManager {
	return &ExecProcessManager{
		socketPath: socketPath,
		procs:      make(map[string]*os.Process),
		cmdFactory: factory,
	}
}

// SetCmdFactory replaces the command factory on an existing ExecProcessManager.
// This is used by tests to inject a controllable factory after construction.
//
//oro:testonly
func (pm *ExecProcessManager) SetCmdFactory(factory func(id string) *exec.Cmd) {
	pm.cmdFactory = factory
}

// CmdForWorker returns the exec.Cmd that would be used to spawn a worker
// with the given ID, without actually starting it. Useful for testing.
//
//oro:testonly
func (pm *ExecProcessManager) CmdForWorker(id string) *exec.Cmd {
	return pm.cmdFactory(id)
}

// Spawn starts a new worker process with the given ID and tracks it.
// Each worker gets its own process group (Setpgid) so Kill can terminate
// the entire tree (worker + claude + node + bash descendants).
//
// When oroHome is set, stdout/stderr are redirected to
// oroHome/workers/<id>/output.log (created if needed). If oroHome is empty,
// output falls back to os.Stdout/os.Stderr with a warning.
func (pm *ExecProcessManager) Spawn(id string) (*os.Process, error) {
	cmd := pm.cmdFactory(id)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if pm.oroHome == "" {
		// No oroHome configured — fall back to daemon log with warning.
		fmt.Fprintf(os.Stderr, "warning: oroHome not set; worker %s output goes to daemon log\n", id)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return pm.startAndTrack(id, cmd, nil)
	}

	// Create per-worker log directory.
	logDir := filepath.Join(pm.oroHome, "workers", id)
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return nil, fmt.Errorf("create worker log dir %s: %w", logDir, err)
	}

	logPath := filepath.Join(logDir, "output.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec // log path is deterministic
	if err != nil {
		return nil, fmt.Errorf("open worker log %s: %w", logPath, err)
	}

	cmd.Stdout = logFile
	cmd.Stderr = logFile

	return pm.startAndTrack(id, cmd, logFile)
}

// startAndTrack starts cmd, optionally closes logFile after Start, tracks the
// process in pm.procs, and launches a reaper goroutine.
func (pm *ExecProcessManager) startAndTrack(id string, cmd *exec.Cmd, logFile *os.File) (*os.Process, error) {
	if err := cmd.Start(); err != nil {
		if logFile != nil {
			_ = logFile.Close()
		}
		return nil, fmt.Errorf("spawn worker %s: %w", id, err)
	}
	// logFile fd is inherited by the child; parent can close its copy.
	if logFile != nil {
		_ = logFile.Close()
	}

	proc := cmd.Process

	pm.mu.Lock()
	pm.procs[id] = proc
	pm.mu.Unlock()

	// Reap the child process in the background to avoid zombies.
	pm.wg.Add(1)
	go func() {
		defer pm.wg.Done()
		_ = cmd.Wait()
	}()

	return proc, nil
}

// Kill sends SIGTERM to the tracked worker process, waits a short grace
// period, and then sends SIGKILL if the process is still alive. The worker
// is removed from tracking regardless of outcome.
func (pm *ExecProcessManager) Kill(id string) error {
	pm.mu.Lock()
	proc, ok := pm.procs[id]
	if !ok {
		pm.mu.Unlock()
		return fmt.Errorf("unknown worker %s", id)
	}
	delete(pm.procs, id)
	pm.mu.Unlock()

	// Send SIGTERM to the entire process group (negative PID) so that
	// descendant processes (claude, node, bash) are also terminated.
	// If SIGTERM fails (process already exited), force-kill as best-effort.
	pgid := proc.Pid
	termErr := syscall.Kill(-pgid, syscall.SIGTERM)
	if termErr != nil {
		_ = proc.Kill()
		return nil //nolint:nilerr // SIGTERM failure means process already exited; not an error
	}

	// Wait up to 3 seconds for graceful exit.
	done := make(chan struct{})
	go func() {
		_, _ = proc.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Exited gracefully.
	case <-time.After(3 * time.Second):
		// Grace period expired; force-kill the entire process group.
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		<-done
	}

	return nil
}

// Wait blocks until all zombie reaper goroutines have completed.
// This is useful for testing and for ensuring clean shutdown.
func (pm *ExecProcessManager) Wait() {
	pm.wg.Wait()
}

// SetProcessManager sets the ProcessManager on a Dispatcher.
// This is used by cmd/oro to wire up the production ProcessManager
// after constructing the Dispatcher.
func (d *Dispatcher) SetProcessManager(pm ProcessManager) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.procMgr = pm
}
