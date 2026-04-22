// Package codex implements Oro runtime adapters for the Codex CLI.
package codex

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"oro/pkg/ops"
	"oro/pkg/worker"
)

const commandName = "codex"

// WorkerSpawner implements the worker.StreamingSpawner contract for Codex.
type WorkerSpawner struct {
	command string
}

// NewWorkerSpawner creates the Codex worker spawner.
func NewWorkerSpawner() *WorkerSpawner {
	return &WorkerSpawner{command: commandName}
}

// StreamFormat reports the Codex stdout contract used by Oro v1.
func (s *WorkerSpawner) StreamFormat() worker.StreamFormat {
	return worker.StreamFormatLineText
}

// Spawn starts a `codex exec` subprocess using a plain-text line stream.
func (s *WorkerSpawner) Spawn(ctx context.Context, model, prompt, workdir string) (worker.Process, io.ReadCloser, io.WriteCloser, error) {
	cmd := exec.CommandContext(ctx, s.binary(), buildExecArgs(model, BuildBootstrapPrompt(prompt, workdir))...) //nolint:gosec // args built internally
	cmd.Dir = workdir
	cmd.Stderr = os.Stderr

	devNull, err := os.Open(os.DevNull)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("open /dev/null: %w", err)
	}
	defer devNull.Close()
	cmd.Stdin = devNull

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, nil, nil, wrapStartError(err)
	}
	return &worker.CmdProcess{Cmd: cmd}, stdoutPipe, nil, nil
}

func (s *WorkerSpawner) binary() string {
	if s == nil || s.command == "" {
		return commandName
	}
	return s.command
}

// NewOpsSpawner creates the Codex ops spawner using the same subprocess contract.
func NewOpsSpawner() ops.BatchSpawner {
	return ops.NewExecSpawner(ops.RuntimeSpec{
		Command:   commandName,
		BuildArgs: buildExecArgs,
	})
}

func buildExecArgs(model, prompt string) []string {
	args := []string{"exec", "--skip-git-repo-check", "--sandbox", "workspace-write"}
	if model != "" {
		args = append(args, "--model", model)
	}
	args = append(args, prompt)
	return args
}

// BuildBootstrapPrompt prepends shared Oro guidance for Codex runs without relying
// on Claude hook surfaces. It prefers worktree-local ORO_AGENT.md and falls back
// to $ORO_HOME/ORO_AGENT.md when available.
func BuildBootstrapPrompt(prompt, workdir string) string {
	if shared := readSharedInstructions(workdir); shared != "" {
		return strings.TrimSpace(shared) + "\n\n## Task\n\n" + prompt
	}
	return prompt
}

func readSharedInstructions(workdir string) string {
	candidates := []string{}
	if workdir != "" {
		candidates = append(candidates, filepath.Join(workdir, "ORO_AGENT.md"))
	}
	if oroHome := os.Getenv("ORO_HOME"); oroHome != "" {
		candidates = append(candidates, filepath.Join(oroHome, "ORO_AGENT.md"))
	}
	for _, candidate := range candidates {
		data, err := os.ReadFile(candidate) //nolint:gosec // internally constructed candidate paths
		if err == nil && strings.TrimSpace(string(data)) != "" {
			return string(data)
		}
	}
	return ""
}

func wrapStartError(err error) error {
	if errors.Is(err, exec.ErrNotFound) {
		return fmt.Errorf("start codex: codex CLI not found; install Codex or set %s=%s: %w", "ORO_AGENT_RUNTIME", "claude", err)
	}
	return fmt.Errorf("start codex: %w", err)
}
