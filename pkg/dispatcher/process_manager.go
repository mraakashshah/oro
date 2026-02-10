package dispatcher

import (
	"context"
	"fmt"
	"os"
	"os/exec"
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
	mu         sync.Mutex
	procs      map[string]*os.Process

	// cmdFactory builds the exec.Cmd for a given worker ID.
	// Defaults to spawning `oro worker --socket <socketPath> --id <id>`.
	// Tests can override this to spawn a dummy command like `sleep`.
	cmdFactory func(id string) *exec.Cmd
}

// NewExecProcessManager creates a new ExecProcessManager that spawns
// `sleep 3600` subprocesses (suitable for unit tests). For production use,
// call NewOroProcessManager which spawns real `oro worker` processes.
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
func NewOroProcessManager(socketPath string) *ExecProcessManager {
	pm := &ExecProcessManager{
		socketPath: socketPath,
		procs:      make(map[string]*os.Process),
	}
	self := os.Args[0]
	pm.cmdFactory = func(id string) *exec.Cmd {
		//nolint:gosec // intentionally spawning worker subprocess
		return exec.CommandContext(context.Background(), self, "worker", "--socket", socketPath, "--id", id)
	}
	return pm
}

// CmdForWorker returns the exec.Cmd that would be used to spawn a worker
// with the given ID, without actually starting it. Useful for testing.
func (pm *ExecProcessManager) CmdForWorker(id string) *exec.Cmd {
	return pm.cmdFactory(id)
}

// Spawn starts a new worker process with the given ID and tracks it.
func (pm *ExecProcessManager) Spawn(id string) (*os.Process, error) {
	cmd := pm.cmdFactory(id)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("spawn worker %s: %w", id, err)
	}

	proc := cmd.Process

	pm.mu.Lock()
	pm.procs[id] = proc
	pm.mu.Unlock()

	// Reap the child process in the background to avoid zombies.
	go func() { _ = cmd.Wait() }()

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

	// Send SIGTERM for graceful shutdown.
	// If SIGTERM fails (process already exited), force-kill as best-effort.
	termErr := proc.Signal(syscall.SIGTERM)
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
		// Grace period expired; force-kill.
		_ = proc.Kill()
		<-done
	}

	return nil
}

// SetProcessManager sets the ProcessManager on a Dispatcher.
// This is used by cmd/oro to wire up the production ProcessManager
// after constructing the Dispatcher.
func (d *Dispatcher) SetProcessManager(pm ProcessManager) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.procMgr = pm
}
