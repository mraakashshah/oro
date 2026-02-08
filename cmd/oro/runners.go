package main

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"oro/pkg/ops"
)

// ExecCommandRunner implements dispatcher.CommandRunner using os/exec.
// Used by CLIBeadSource, GitWorktreeManager, and TmuxEscalator.
type ExecCommandRunner struct{}

// Run executes a command and returns its stdout as bytes.
func (r *ExecCommandRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if ok := errors.As(err, &exitErr); ok {
			return nil, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, exitErr.Stderr)
		}
		return nil, fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return out, nil
}

// ExecGitRunner implements merge.GitRunner using os/exec.
type ExecGitRunner struct{}

// Run executes a git command in the given directory and returns stdout and stderr.
func (r *ExecGitRunner) Run(ctx context.Context, dir string, args ...string) (stdout, stderr string, err error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir

	var stdoutBuf, stderrBuf strings.Builder
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	err = cmd.Run()
	return stdoutBuf.String(), stderrBuf.String(), err
}

// ClaudeOpsSpawner implements ops.SubprocessSpawner using os/exec.
type ClaudeOpsSpawner struct{}

// Spawn starts a `claude -p` subprocess with the given model and prompt.
func (s *ClaudeOpsSpawner) Spawn(ctx context.Context, model, prompt, workdir string) (ops.Process, error) {
	cmd := exec.CommandContext(ctx, "claude", "-p", prompt, "--model", model)
	cmd.Dir = workdir

	var outBuf strings.Builder
	cmd.Stdout = &outBuf
	cmd.Stderr = &outBuf

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("spawn claude: %w", err)
	}
	return &opsProcess{cmd: cmd, output: &outBuf}, nil
}

// opsProcess wraps exec.Cmd to implement ops.Process.
type opsProcess struct {
	cmd    *exec.Cmd
	output *strings.Builder
}

func (p *opsProcess) Wait() error { return fmt.Errorf("wait: %w", p.cmd.Wait()) }
func (p *opsProcess) Kill() error { return fmt.Errorf("kill: %w", p.cmd.Process.Kill()) }
func (p *opsProcess) Output() (string, error) {
	return p.output.String(), nil
}
