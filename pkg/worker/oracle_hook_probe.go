package worker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const oracleHookProbePollInterval = 10 * time.Millisecond

// ReplayableProcess owns one underlying process wait and replays its result to
// every later caller. This lets launch probes observe process termination
// without racing the worker monitor for exec.Cmd.Wait.
type ReplayableProcess struct {
	inner Process
	done  chan struct{}

	waitErr error
}

// NewReplayableProcess starts the sole owner of inner.Wait.
func NewReplayableProcess(inner Process) *ReplayableProcess {
	p := &ReplayableProcess{inner: inner, done: make(chan struct{})}
	go func() {
		if inner != nil {
			p.waitErr = inner.Wait()
		}
		close(p.done)
	}()
	return p
}

// Done closes once the underlying process wait has completed.
func (p *ReplayableProcess) Done() <-chan struct{} {
	if p == nil {
		return nil
	}
	return p.done
}

// Wait replays the result of the single underlying wait.
func (p *ReplayableProcess) Wait() error {
	if p == nil {
		return nil
	}
	<-p.done
	return p.waitErr
}

// Kill terminates the underlying process without consuming its wait result.
func (p *ReplayableProcess) Kill() error {
	if p == nil || p.inner == nil {
		return nil
	}
	if err := p.inner.Kill(); err != nil {
		return fmt.Errorf("kill replayable process: %w", err)
	}
	return nil
}

// ExitCode delegates runtime diagnostics unchanged.
func (p *ReplayableProcess) ExitCode() int {
	if p == nil {
		return 0
	}
	if diagnostics, ok := p.inner.(processExitDiagnostics); ok {
		return diagnostics.ExitCode()
	}
	return 0
}

// StderrTail delegates runtime diagnostics unchanged.
func (p *ReplayableProcess) StderrTail() string {
	if p == nil {
		return ""
	}
	if diagnostics, ok := p.inner.(processExitDiagnostics); ok {
		return diagnostics.StderrTail()
	}
	return ""
}

// OracleHookProbe is a private filesystem marker written by the managed
// SessionStart hook to confirm that the selected Oracle profile is active.
type OracleHookProbe struct {
	dir        string
	markerPath string
	cleanup    sync.Once
}

// NewOracleHookProbe creates a private, unique marker location for one launch.
func NewOracleHookProbe() (*OracleHookProbe, error) {
	dir, err := os.MkdirTemp("", "oro-oracle-hook-probe-")
	if err != nil {
		return nil, fmt.Errorf("create Oracle hook probe directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil { //nolint:gosec // directories need owner execute permission; 0700 keeps the probe private
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("secure Oracle hook probe directory: %w", err)
	}
	return &OracleHookProbe{dir: dir, markerPath: filepath.Join(dir, "session-start")}, nil
}

// MarkerPath returns the exact private path exported to the managed hook.
func (p *OracleHookProbe) MarkerPath() string {
	if p == nil {
		return ""
	}
	return p.markerPath
}

// Environment returns the launch-local marker assignment for the managed hook.
func (p *OracleHookProbe) Environment() string {
	if p == nil || p.markerPath == "" {
		return ""
	}
	return "ORO_HOOK_PROBE=" + p.markerPath
}

// Await waits for a hook marker, an early process exit, cancellation, or timeout.
// Failed live launches are killed and reaped through the replayable process.
func (p *OracleHookProbe) Await(ctx context.Context, proc *ReplayableProcess, timeout time.Duration) error {
	if p == nil || p.markerPath == "" {
		return errors.New("oracle hook probe is not configured")
	}
	if proc == nil {
		p.remove()
		return errors.New("oracle hook probe process is nil")
	}
	defer p.remove()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(oracleHookProbePollInterval)
	defer ticker.Stop()

	for {
		if markerExists(p.markerPath) {
			return nil
		}
		select {
		case <-proc.Done():
			return fmt.Errorf("oracle hook did not activate before process exit: %w", proc.Wait())
		case <-ctx.Done():
			return p.stopAndReap(proc, fmt.Errorf("await Oracle hook activation: %w", ctx.Err()))
		case <-timer.C:
			return p.stopAndReap(proc, errors.New("oracle hook did not activate within timeout"))
		case <-ticker.C:
		}
	}
}

func markerExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func (p *OracleHookProbe) stopAndReap(proc *ReplayableProcess, cause error) error {
	if err := proc.Kill(); err != nil {
		return errors.Join(cause, fmt.Errorf("terminate Oracle process: %w", err))
	}
	<-proc.Done()
	return cause
}

func (p *OracleHookProbe) remove() {
	if p == nil {
		return
	}
	p.cleanup.Do(func() { _ = os.RemoveAll(p.dir) })
}
