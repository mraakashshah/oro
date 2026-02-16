package dispatcher

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

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
	var detail *protocol.BeadDetail
	var arr []protocol.BeadDetail
	if err := json.Unmarshal(out, &arr); err == nil {
		if len(arr) == 0 {
			return nil, fmt.Errorf("bead %s not found", id)
		}
		detail = &arr[0]
	} else {
		var obj protocol.BeadDetail
		if err := json.Unmarshal(out, &obj); err != nil {
			return nil, fmt.Errorf("parse bd show output: %w", err)
		}
		detail = &obj
	}

	// bd show --json has no separate acceptance_criteria field; AC is embedded
	// as markdown in the description under "## Acceptance Criteria". Extract it.
	if detail.AcceptanceCriteria == "" && detail.Description != "" {
		detail.AcceptanceCriteria = extractACFromDescription(detail.Description)
	}
	return detail, nil
}

// extractACFromDescription extracts the acceptance criteria section from a
// markdown description. It looks for a "## Acceptance Criteria" header and
// returns everything after it up to the next H2 header or end of string.
func extractACFromDescription(desc string) string {
	const header = "## Acceptance Criteria"
	idx := strings.Index(desc, header)
	if idx < 0 {
		return ""
	}
	body := desc[idx+len(header):]
	// Trim leading newlines.
	body = strings.TrimLeft(body, "\r\n")
	// Stop at the next H2 header if present.
	if next := strings.Index(body, "\n## "); next >= 0 {
		body = body[:next]
	}
	return strings.TrimSpace(body)
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
// and optionally `--parent=...` if parent is non-empty and `--acceptance-criteria=...`
// if acceptanceCriteria is non-empty. It parses the JSON output to extract and return
// the new bead ID.
func (s *CLIBeadSource) Create(ctx context.Context, title, beadType string, priority int, description, parent, acceptanceCriteria string) (string, error) {
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
	if acceptanceCriteria != "" {
		args = append(args, "--acceptance-criteria="+acceptanceCriteria)
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

// AllChildrenClosed checks whether all children of the given epic are closed.
// Returns true if the epic has no open children (all children are closed),
// false if there are open children or the bead is not an epic.
func (s *CLIBeadSource) AllChildrenClosed(ctx context.Context, epicID string) (bool, error) {
	out, err := s.runner.Run(ctx, "bd", "list", "--parent="+epicID, "--status=open", "--json")
	if err != nil {
		return false, fmt.Errorf("bd list --parent=%s --status=open: %w", epicID, err)
	}

	var openChildren []protocol.Bead
	if err := json.Unmarshal(out, &openChildren); err != nil {
		return false, fmt.Errorf("parse bd list output: %w", err)
	}

	// If the list is empty, all children are closed.
	return len(openChildren) == 0, nil
}
