package ops

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"oro/pkg/processenv"
	"oro/pkg/protocol"
	"oro/pkg/storage"
)

// RuntimeSpec describes how an ops runtime launches a subprocess.
type RuntimeSpec struct {
	Command                string
	BuildArgs              func(model, prompt string) []string
	BuildArgsWithReasoning func(model, reasoning, prompt string) []string
	BuildEnv               func() []string
	NewRuntime             func() storage.RuntimeRequest
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
	if s.spec.NewRuntime != nil {
		return s.spawnLeased(ctx, args, workdir)
	}
	cmd := exec.CommandContext(ctx, s.spec.Command, args...)
	// Give every ops subprocess its own process group. Runtime launchers such
	// as codex may spawn the actual agent as a child process, so cancellation
	// must be able to terminate the entire tree rather than only this launcher.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Dir = workdir
	if s.spec.BuildEnv != nil {
		cmd.Env = processenv.ForWorkdir(s.spec.BuildEnv(), workdir)
	} else {
		cmd.Env = processenv.ForWorkdir(os.Environ(), workdir)
	}

	proc := &opsProcess{cmd: cmd}
	cmd.Cancel = proc.Kill
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe %s: %w", s.spec.Command, err)
	}
	cmd.Stderr = proc

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("spawn %s: %w", s.spec.Command, err)
	}
	proc.stdoutDone = make(chan error, 1)
	go proc.scanStdout(stdoutPipe)
	return proc, nil
}

func (s *ExecSpawner) spawnLeased(ctx context.Context, args []string, workdir string) (Process, error) {
	stdoutReader, stdoutWriter := io.Pipe()
	runtime := s.spec.NewRuntime()
	runtime.Workdir = workdir
	if s.spec.BuildEnv != nil {
		runtime.Env = processenv.ForWorkdir(s.spec.BuildEnv(), workdir)
	} else {
		runtime.Env = processenv.ForWorkdir(os.Environ(), workdir)
	}

	proc := &opsProcess{stdoutDone: make(chan error, 1), stdoutCloser: stdoutWriter}
	command, err := storage.StartLeasedCommand(ctx, storage.CommandRequest{
		Runtime: runtime,
		Path:    s.spec.Command,
		Args:    args,
		Dir:     workdir,
		Stdout:  stdoutWriter,
		Stderr:  proc,
	})
	if err != nil {
		_ = stdoutWriter.Close()
		_ = stdoutReader.Close()
		return nil, fmt.Errorf("spawn %s: %w", s.spec.Command, err)
	}
	proc.leased = command
	go func() {
		proc.scanStdout(stdoutReader)
		_ = stdoutReader.Close()
	}()
	return proc, nil
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
			Command:                "claude",
			BuildArgs:              buildClaudeOpsArgs,
			BuildArgsWithReasoning: buildClaudeOpsArgsWithReasoning,
			BuildEnv:               filteredEnv,
		}),
	}
}

// NewClaudeReviewOpsSpawner creates the Claude-specific review ops spawner.
// Review output is streamed as JSON so callers can parse Claude event streams.
func NewClaudeReviewOpsSpawner() *ClaudeOpsSpawner {
	return &ClaudeOpsSpawner{
		ExecSpawner: NewExecSpawner(RuntimeSpec{
			Command:                "claude",
			BuildArgs:              buildClaudeReviewArgs,
			BuildArgsWithReasoning: buildClaudeReviewArgsWithReasoning,
			BuildEnv:               filteredEnv,
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
	mu           sync.Mutex
	cmd          *exec.Cmd
	leased       *storage.StartedCommand
	output       strings.Builder
	lastOutputAt time.Time
	stdoutDone   chan error
	stdoutCloser io.Closer
}

// Wait waits for the subprocess to exit.
func (p *opsProcess) Wait() error {
	if p.leased != nil {
		waitErr := p.leased.Wait()
		if p.stdoutCloser != nil {
			_ = p.stdoutCloser.Close()
		}
		stdoutErr := <-p.stdoutDone
		if stdoutErr != nil && waitErr == nil {
			return stdoutErr
		}
		if waitErr != nil {
			return fmt.Errorf("wait: %w", waitErr)
		}
		return nil
	}
	var stdoutErr error
	if p.stdoutDone != nil {
		stdoutErr = <-p.stdoutDone
	}
	waitErr := p.cmd.Wait()
	if stdoutErr != nil && waitErr == nil {
		return stdoutErr
	}
	if waitErr != nil {
		return fmt.Errorf("wait: %w", waitErr)
	}
	return nil
}

// Kill sends SIGKILL to the subprocess process group.
func (p *opsProcess) Kill() error {
	if p.leased != nil {
		if err := p.leased.Kill(); err != nil {
			return fmt.Errorf("kill leased ops process: %w", err)
		}
		return nil
	}
	pgid := p.cmd.Process.Pid
	if err := syscall.Kill(-pgid, syscall.SIGKILL); err == nil {
		return nil
	}
	if err := p.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("kill: %w", err)
	}
	return nil
}

func (p *opsProcess) scanStdout(stdout io.Reader) {
	scanner := bufio.NewScanner(stdout)
	// Ops stream records are line-delimited and may contain large review
	// payloads. Keep the limit explicit rather than relying on Scanner's
	// 64 KiB default.
	scanner.Buffer(make([]byte, 64*1024), protocol.MaxMessageSize)
	scanner.Split(scanLinesPreservingEnd)
	for scanner.Scan() {
		p.appendOutput(scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		p.stdoutDone <- fmt.Errorf("read stdout: %w", err)
		return
	}
	p.stdoutDone <- nil
}

func (p *opsProcess) Write(data []byte) (int, error) {
	return p.appendOutput(string(data)), nil
}

func (p *opsProcess) appendOutput(data string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n, _ := p.output.WriteString(data)
	if n > 0 {
		p.lastOutputAt = time.Now()
	}
	return n
}

func (p *opsProcess) Output() (string, error) { //nolint:revive // interface impl
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.output.String(), nil
}

// LastOutputAt returns when the subprocess last wrote stdout or stderr.
func (p *opsProcess) LastOutputAt() time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastOutputAt
}

func scanLinesPreservingEnd(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if i := bytes.IndexByte(data, '\n'); i >= 0 {
		return i + 1, data[:i+1], nil
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

func buildClaudeOpsArgs(model, prompt string) []string {
	return []string{"-p", prompt, "--model", model}
}

func buildClaudeReviewArgs(model, prompt string) []string {
	return []string{"-p", prompt, "--model", model, "--verbose", "--output-format", "stream-json"}
}

// appendEffort adds Claude's --effort flag when a reasoning level is set,
// leaving the args untouched for empty reasoning.
func appendEffort(args []string, reasoning string) []string {
	if reasoning = strings.TrimSpace(reasoning); reasoning != "" {
		return append(args, "--effort", reasoning)
	}
	return args
}

func buildClaudeOpsArgsWithReasoning(model, reasoning, prompt string) []string {
	return appendEffort(buildClaudeOpsArgs(model, prompt), reasoning)
}

func buildClaudeReviewArgsWithReasoning(model, reasoning, prompt string) []string {
	return appendEffort(buildClaudeReviewArgs(model, prompt), reasoning)
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
