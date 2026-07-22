package worker_test

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"oro/pkg/worker"
)

func TestOracleHookProbeReplayableProcess(t *testing.T) {
	t.Parallel()

	t.Run("marker success replays one owned wait and diagnostics", func(t *testing.T) {
		t.Parallel()

		wantWait := errors.New("runtime failed")
		inner := newProbeProcess(wantWait, 23, "runtime stderr")
		proc := worker.NewReplayableProcess(inner)
		probe, err := worker.NewOracleHookProbe()
		if err != nil {
			t.Fatalf("new probe: %v", err)
		}

		if err := os.WriteFile(probe.MarkerPath(), []byte("started"), 0o600); err != nil { //nolint:gosec // private test probe marker
			t.Fatalf("write marker: %v", err)
		}
		if err := probe.Await(context.Background(), proc, time.Second); err != nil {
			t.Fatalf("await marker: %v", err)
		}
		if _, err := os.Stat(probe.MarkerPath()); !os.IsNotExist(err) {
			t.Fatalf("marker remains after success: %v", err)
		}

		inner.Finish()
		if got := proc.Wait(); got != wantWait {
			t.Fatalf("first replayed Wait = %v, want original %v", got, wantWait)
		}
		if got := proc.Wait(); got != wantWait {
			t.Fatalf("second replayed Wait = %v, want original %v", got, wantWait)
		}
		if got := inner.WaitCalls(); got != 1 {
			t.Fatalf("inner Wait calls = %d, want 1", got)
		}
		if got := proc.ExitCode(); got != 23 {
			t.Fatalf("ExitCode = %d, want 23", got)
		}
		if got := proc.StderrTail(); got != "runtime stderr" {
			t.Fatalf("StderrTail = %q", got)
		}
	})

	for _, tc := range []struct {
		name  string
		await func(context.Context, *worker.OracleHookProbe, *worker.ReplayableProcess) error
		setup func(*probeProcess)
	}{
		{
			name: "early exit",
			await: func(ctx context.Context, probe *worker.OracleHookProbe, proc *worker.ReplayableProcess) error {
				return probe.Await(ctx, proc, time.Second)
			},
			setup: func(inner *probeProcess) { inner.Finish() },
		},
		{
			name: "timeout",
			await: func(ctx context.Context, probe *worker.OracleHookProbe, proc *worker.ReplayableProcess) error {
				return probe.Await(ctx, proc, 10*time.Millisecond)
			},
			setup: func(_ *probeProcess) {},
		},
		{
			name: "cancellation",
			await: func(_ context.Context, probe *worker.OracleHookProbe, proc *worker.ReplayableProcess) error {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return probe.Await(ctx, proc, time.Second)
			},
			setup: func(_ *probeProcess) {},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			inner := newProbeProcess(nil, 0, "")
			proc := worker.NewReplayableProcess(inner)
			probe, err := worker.NewOracleHookProbe()
			if err != nil {
				t.Fatalf("new probe: %v", err)
			}
			tc.setup(inner)

			if err := tc.await(context.Background(), probe, proc); err == nil {
				t.Fatal("Await succeeded without a marker")
			}
			if !inner.Killed() && tc.name != "early exit" {
				t.Fatal("live process was not killed")
			}
			if _, err := os.Stat(probe.MarkerPath()); !os.IsNotExist(err) {
				t.Fatalf("marker remains after cleanup: %v", err)
			}
			if got := inner.WaitCalls(); got != 1 {
				t.Fatalf("inner Wait calls = %d, want 1", got)
			}
		})
	}
}

type probeProcess struct {
	mu        sync.Mutex
	finish    chan struct{}
	waitErr   error
	waitCalls int
	killed    bool
	exitCode  int
	stderr    string
}

func newProbeProcess(waitErr error, exitCode int, stderr string) *probeProcess {
	return &probeProcess{finish: make(chan struct{}), waitErr: waitErr, exitCode: exitCode, stderr: stderr}
}

func (p *probeProcess) Wait() error {
	p.mu.Lock()
	p.waitCalls++
	p.mu.Unlock()
	<-p.finish
	return p.waitErr
}

func (p *probeProcess) Kill() error {
	p.mu.Lock()
	p.killed = true
	p.mu.Unlock()
	p.Finish()
	return nil
}

func (p *probeProcess) ExitCode() int { return p.exitCode }

func (p *probeProcess) StderrTail() string { return p.stderr }

func (p *probeProcess) Finish() {
	select {
	case <-p.finish:
	default:
		close(p.finish)
	}
}

func (p *probeProcess) WaitCalls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.waitCalls
}

func (p *probeProcess) Killed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.killed
}
