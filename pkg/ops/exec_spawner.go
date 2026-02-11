package ops

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// ClaudeOpsSpawner implements BatchSpawner using os/exec.
type ClaudeOpsSpawner struct{}

// Spawn starts a `claude -p` subprocess with the given model and prompt.
func (s *ClaudeOpsSpawner) Spawn(ctx context.Context, model, prompt, workdir string) (Process, error) {
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

// opsProcess wraps exec.Cmd to implement Process.
type opsProcess struct {
	cmd    *exec.Cmd
	output *strings.Builder
}

func (p *opsProcess) Wait() error             { return fmt.Errorf("wait: %w", p.cmd.Wait()) }         //nolint:revive // interface impl
func (p *opsProcess) Kill() error             { return fmt.Errorf("kill: %w", p.cmd.Process.Kill()) } //nolint:revive // interface impl
func (p *opsProcess) Output() (string, error) { return p.output.String(), nil }                       //nolint:revive // interface impl
