package dispatcher

import (
	"context"
	"encoding/json"
	"fmt"
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
func (s *CLIBeadSource) Ready(ctx context.Context) ([]Bead, error) {
	out, err := s.runner.Run(ctx, "bd", "ready", "--json")
	if err != nil {
		return nil, fmt.Errorf("bd ready: %w", err)
	}

	var beads []Bead
	if err := json.Unmarshal(out, &beads); err != nil {
		return nil, fmt.Errorf("parse bd ready output: %w", err)
	}
	return beads, nil
}

// Show runs `bd show <id> --json` and parses the output into a BeadDetail.
func (s *CLIBeadSource) Show(ctx context.Context, id string) (*BeadDetail, error) {
	out, err := s.runner.Run(ctx, "bd", "show", id, "--json")
	if err != nil {
		return nil, fmt.Errorf("bd show %s: %w", id, err)
	}

	var detail BeadDetail
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

// Sync runs `bd sync --flush-only` to flush bead state to disk.
func (s *CLIBeadSource) Sync(ctx context.Context) error {
	_, err := s.runner.Run(ctx, "bd", "sync", "--flush-only")
	if err != nil {
		return fmt.Errorf("bd sync: %w", err)
	}
	return nil
}
