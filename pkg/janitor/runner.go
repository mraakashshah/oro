package janitor

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"os/exec"
	"time"

	"oro/pkg/processenv"
	"oro/pkg/storage"
)

// RunOption configures one janitor detector invocation.
type RunOption func(*runConfig)

type runConfig struct {
	runtime         *storage.RuntimeRequest
	directExecution bool
}

// WithRuntime routes detector subprocesses through the supplied lease
// template. Each child receives a distinct lease when the template ID is
// empty.
func WithRuntime(runtime storage.RuntimeRequest) RunOption {
	return func(config *runConfig) {
		config.runtime = &runtime
	}
}

// WithDirectExecutionForTest opts out of runtime leasing for detector unit
// tests that do not exercise storage lifecycle behavior.
//
//oro:testonly
func WithDirectExecutionForTest() RunOption {
	return func(config *runConfig) {
		config.directExecution = true
	}
}

type commandOutput struct {
	stdout []byte
	stderr []byte
}

type commandRunner interface {
	run(context.Context, string, ...string) (commandOutput, error)
}

// runner executes janitor subprocesses with a lease-protected worktree runtime.
type runner struct {
	runtime storage.RuntimeRequest
}

// newRunner constructs a janitor subprocess runner for one worktree runtime.
func newRunner(runtime storage.RuntimeRequest) runner {
	return runner{runtime: runtime}
}

// run starts one janitor command after its runtime lease is cataloged and
// releases that lease after the command exits. Standard error is retained in
// the returned error so detector failures preserve their diagnostics.
func (runner runner) run(ctx context.Context, path string, args ...string) (commandOutput, error) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	runtime := runner.runtime
	if runtime.Lease.ID == "" {
		runtime.Lease.ID = storage.LeaseID("janitor-command-" + rand.Text())
	}
	now := time.Now().UTC()
	runtime.Lease.AcquiredAt = now
	runtime.Lease.HeartbeatAt = now
	_, err := storage.RunLeasedCommand(ctx, storage.CommandRequest{
		Runtime: runtime,
		Path:    path,
		Args:    args,
		Stdout:  &stdout,
		Stderr:  &stderr,
	})
	output := commandOutput{stdout: stdout.Bytes(), stderr: stderr.Bytes()}
	if err != nil {
		if stderr.Len() == 0 {
			return output, fmt.Errorf("run janitor command %q: %w", path, err)
		}
		return output, fmt.Errorf("run janitor command %q: %w: %s", path, err, stderr.String())
	}
	return output, nil
}

type directRunner struct {
	worktree string
}

func (runner directRunner) run(ctx context.Context, path string, args ...string) (commandOutput, error) {
	cmd := exec.CommandContext(ctx, path, args...) //nolint:gosec // paths and arguments are fixed janitor detector definitions
	cmd.Dir = runner.worktree
	cmd.Env = processenv.ForWorkdir(os.Environ(), runner.worktree)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	output := commandOutput{stdout: stdout.Bytes(), stderr: stderr.Bytes()}
	if err != nil {
		if stderr.Len() == 0 {
			return output, fmt.Errorf("run janitor command %q: %w", path, err)
		}
		return output, fmt.Errorf("run janitor command %q: %w: %s", path, err, stderr.String())
	}
	return output, nil
}

func commandRunnerFor(worktree string, options []RunOption) (commandRunner, error) {
	config := runConfig{}
	for _, option := range options {
		option(&config)
	}
	if config.runtime == nil {
		if config.directExecution {
			return directRunner{worktree: worktree}, nil
		}
		return nil, fmt.Errorf("janitor command runtime lease is required")
	}
	runtime := *config.runtime
	runtime.Workdir = worktree
	if runtime.Env == nil {
		runtime.Env = os.Environ()
	}
	return newRunner(runtime), nil
}
