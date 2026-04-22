package ops

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// RuntimeSpec describes how an ops runtime launches a subprocess.
type RuntimeSpec struct {
	Command   string
	BuildArgs func(model, prompt string) []string
	BuildEnv  func() []string
}

// ExecSpawner implements BatchSpawner for a runtime-specific exec spec.
type ExecSpawner struct {
	spec RuntimeSpec
}

// NewExecSpawner creates a generic runtime-backed ops spawner.
func NewExecSpawner(spec RuntimeSpec) *ExecSpawner {
	return &ExecSpawner{spec: spec}
}

// Spawn starts a subprocess using the runtime spec.
func (s *ExecSpawner) Spawn(ctx context.Context, model, prompt, workdir string) (Process, error) {
	cmd := exec.CommandContext(ctx, s.spec.Command, s.spec.BuildArgs(model, prompt)...)
	cmd.Dir = workdir
	if s.spec.BuildEnv != nil {
		cmd.Env = s.spec.BuildEnv()
	}

	var outBuf strings.Builder
	cmd.Stdout = &outBuf
	cmd.Stderr = &outBuf

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("spawn %s: %w", s.spec.Command, err)
	}
	return &opsProcess{cmd: cmd, output: &outBuf}, nil
}

// ClaudeOpsSpawner implements BatchSpawner using Claude's subprocess contract.
type ClaudeOpsSpawner struct {
	*ExecSpawner
}

// NewClaudeOpsSpawner creates the Claude-specific ops spawner adapter.
func NewClaudeOpsSpawner() *ClaudeOpsSpawner {
	return &ClaudeOpsSpawner{
		ExecSpawner: NewExecSpawner(RuntimeSpec{
			Command:   "claude",
			BuildArgs: buildClaudeOpsArgs,
			BuildEnv:  filteredEnv,
		}),
	}
}

// Spawn starts a `claude -p` subprocess with the given model and prompt.
func (s *ClaudeOpsSpawner) Spawn(ctx context.Context, model, prompt, workdir string) (Process, error) {
	if s == nil || s.ExecSpawner == nil {
		s = NewClaudeOpsSpawner()
	}
	return s.ExecSpawner.Spawn(ctx, model, prompt, workdir)
}

// opsProcess wraps exec.Cmd to implement Process.
type opsProcess struct {
	cmd    *exec.Cmd
	output *strings.Builder
}

// Wait waits for the subprocess to exit.
func (p *opsProcess) Wait() error {
	if err := p.cmd.Wait(); err != nil {
		return fmt.Errorf("wait: %w", err)
	}
	return nil
}

// Kill sends SIGKILL to the subprocess.
func (p *opsProcess) Kill() error {
	if err := p.cmd.Process.Kill(); err != nil {
		return fmt.Errorf("kill: %w", err)
	}
	return nil
}
func (p *opsProcess) Output() (string, error) { return p.output.String(), nil } //nolint:revive // interface impl

func buildClaudeOpsArgs(model, prompt string) []string {
	return []string{"-p", prompt, "--model", model}
}

// filteredEnv returns the current environment with CLAUDECODE stripped,
// preventing the "nested Claude Code session" error when spawning ops agents.
func filteredEnv() []string {
	env := make([]string, 0, len(os.Environ()))
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "CLAUDECODE=") {
			env = append(env, e)
		}
	}
	return env
}
