package ops

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"oro/pkg/processenv"
)

// RuntimeSpec describes how an ops runtime launches a subprocess.
type RuntimeSpec struct {
	Command                string
	BuildArgs              func(model, prompt string) []string
	BuildArgsWithReasoning func(model, reasoning, prompt string) []string
	BuildEnv               func() []string
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
	return s.SpawnWithReasoning(ctx, model, "", prompt, workdir)
}

// SpawnWithReasoning starts a subprocess, passing reasoning only when the
// runtime spec supports it.
func (s *ExecSpawner) SpawnWithReasoning(ctx context.Context, model, reasoning, prompt, workdir string) (Process, error) {
	buildArgs := s.spec.BuildArgs
	args := []string{}
	if s.spec.BuildArgsWithReasoning != nil {
		args = s.spec.BuildArgsWithReasoning(model, reasoning, prompt)
	} else if buildArgs != nil {
		args = buildArgs(model, prompt)
	}
	cmd := exec.CommandContext(ctx, s.spec.Command, args...)
	cmd.Dir = workdir
	if s.spec.BuildEnv != nil {
		cmd.Env = processenv.ForWorkdir(s.spec.BuildEnv(), workdir)
	} else {
		cmd.Env = processenv.ForWorkdir(os.Environ(), workdir)
	}

	var outBuf strings.Builder
	cmd.Stdout = &outBuf
	cmd.Stderr = &outBuf

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("spawn %s: %w", s.spec.Command, err)
	}
	return &opsProcess{cmd: cmd, output: &outBuf}, nil
}

// RuntimeSpawnerRouter selects a Claude or Codex ops spawner per role resolution.
type RuntimeSpawnerRouter struct {
	claude BatchSpawner
	codex  BatchSpawner
}

// NewRuntimeSpawnerRouter creates an ops spawner that routes each call by runtime.
func NewRuntimeSpawnerRouter(claude, codex BatchSpawner) *RuntimeSpawnerRouter {
	return &RuntimeSpawnerRouter{claude: claude, codex: codex}
}

// Spawn preserves the BatchSpawner interface by defaulting to Claude.
func (r *RuntimeSpawnerRouter) Spawn(ctx context.Context, model, prompt, workdir string) (Process, error) {
	return r.SpawnRuntime(ctx, "claude", model, "", prompt, workdir)
}

// SpawnRuntime routes an ops subprocess to the requested runtime spawner.
func (r *RuntimeSpawnerRouter) SpawnRuntime(ctx context.Context, runtime, model, reasoning, prompt, workdir string) (Process, error) {
	var spawner BatchSpawner
	switch runtime {
	case "claude":
		spawner = r.claude
	case "codex":
		spawner = r.codex
	default:
		return nil, fmt.Errorf("unknown ops runtime %q", runtime)
	}
	if spawner == nil {
		return nil, fmt.Errorf("%s ops spawner is not configured", runtime)
	}
	if reasoningSpawner, ok := spawner.(ReasoningBatchSpawner); ok {
		proc, err := reasoningSpawner.SpawnWithReasoning(ctx, model, reasoning, prompt, workdir)
		if err != nil {
			return nil, fmt.Errorf("spawn %s ops subprocess: %w", runtime, err)
		}
		return proc, nil
	}
	proc, err := spawner.Spawn(ctx, model, prompt, workdir)
	if err != nil {
		return nil, fmt.Errorf("spawn %s ops subprocess: %w", runtime, err)
	}
	return proc, nil
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
