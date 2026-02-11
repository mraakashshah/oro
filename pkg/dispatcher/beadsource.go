package dispatcher

import (
	"context"
	"encoding/json"
	"fmt"

	"oro/pkg/protocol"
)

// CommandRunner abstracts command execution for testability.
// Production implementation uses os/exec; tests provide a mock.
type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// CLIBeadSource implements BeadSource by shelling out to the bd CLI tool.
type CLIBeadSource struct {
	runner CommandRunner
}

// NewCLIBeadSource creates a CLIBeadSource backed by the given CommandRunner.
func NewCLIBeadSource(runner CommandRunner) *CLIBeadSource {
	return &CLIBeadSource{runner: runner}
}

// Ready runs `bd ready --json` and parses the output into a slice of Bead.
func (s *CLIBeadSource) Ready(ctx context.Context) ([]protocol.Bead, error) {
	out, err := s.runner.Run(ctx, "bd", "ready", "--json")
	if err != nil {
		return nil, fmt.Errorf("bd ready: %w", err)
	}

	var beads []protocol.Bead
	if err := json.Unmarshal(out, &beads); err != nil {
		return nil, fmt.Errorf("parse bd ready output: %w", err)
	}
	return beads, nil
}

// Show runs `bd show <id> --json` and parses the output into a BeadDetail.
func (s *CLIBeadSource) Show(ctx context.Context, id string) (*protocol.BeadDetail, error) {
	out, err := s.runner.Run(ctx, "bd", "show", id, "--json")
	if err != nil {
		return nil, fmt.Errorf("bd show %s: %w", id, err)
	}

	// bd show --json returns an array; try array first, fall back to object.
	var arr []protocol.BeadDetail
	if err := json.Unmarshal(out, &arr); err == nil {
		if len(arr) == 0 {
			return nil, fmt.Errorf("bead %s not found", id)
		}
		return &arr[0], nil
	}
	var detail protocol.BeadDetail
	if err := json.Unmarshal(out, &detail); err != nil {
		return nil, fmt.Errorf("parse bd show output: %w", err)
	}
	return &detail, nil
}

// Close runs `bd close <id> --reason="<reason>"`.
func (s *CLIBeadSource) Close(ctx context.Context, id, reason string) error {
	_, err := s.runner.Run(ctx, "bd", "close", id, "--reason="+reason)
	if err != nil {
		return fmt.Errorf("bd close %s: %w", id, err)
	}
	return nil
}

// Create runs `bd create --title=... --type=... --priority=N --description=... --json`
// and optionally `--parent=...` if parent is non-empty. It parses the JSON output
// to extract and return the new bead ID.
func (s *CLIBeadSource) Create(ctx context.Context, title, beadType string, priority int, description, parent string) (string, error) {
	args := []string{
		"create",
		"--title=" + title,
		"--type=" + beadType,
		fmt.Sprintf("--priority=%d", priority),
		"--description=" + description,
	}
	if parent != "" {
		args = append(args, "--parent="+parent)
	}
	args = append(args, "--json")

	out, err := s.runner.Run(ctx, "bd", args...)
	if err != nil {
		return "", fmt.Errorf("bd create: %w", err)
	}

	var result struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		return "", fmt.Errorf("parse bd create output: %w", err)
	}
	return result.ID, nil
}

// Sync runs `bd sync --flush-only` to flush bead state to disk.
func (s *CLIBeadSource) Sync(ctx context.Context) error {
	_, err := s.runner.Run(ctx, "bd", "sync", "--flush-only")
	if err != nil {
		return fmt.Errorf("bd sync: %w", err)
	}
	return nil
}
