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
	extractMetadataModel(beads)
	return beads, nil
}

// InProgress runs `bd list --status=in_progress --json` and parses the output into a slice of Bead.
// Returns nil slice (not empty slice) when bd reports no in-progress beads.
func (s *CLIBeadSource) InProgress(ctx context.Context) ([]protocol.Bead, error) {
	out, err := s.runner.Run(ctx, "bd", "list", "--status=in_progress", "--json")
	if err != nil {
		return nil, fmt.Errorf("bd list --status=in_progress: %w", err)
	}

	var beads []protocol.Bead
	if err := json.Unmarshal(out, &beads); err != nil {
		return nil, fmt.Errorf("parse bd list output: %w", err)
	}
	if len(beads) == 0 {
		return nil, nil
	}
	extractMetadataModel(beads)
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
	extractMetadataModelDetail(detail)
	return detail, nil
}

// isAllowedModel reports whether model is in the Claude model allowlist.
func isAllowedModel(model string) bool {
	switch model {
	case protocol.ModelOpus, protocol.ModelSonnet, protocol.ModelHaiku:
		return true
	}
	return false
}

// extractMetadataModel promotes metadata["model"] into Bead.Model for each bead
// that has no explicit top-level Model set. Only allowlisted values are accepted;
// nil metadata and non-string values are silently ignored.
func extractMetadataModel(beads []protocol.Bead) {
	for i := range beads {
		if beads[i].Model != "" || beads[i].Metadata == nil {
			continue
		}
		val, ok := beads[i].Metadata["model"]
		if !ok {
			continue
		}
		model, ok := val.(string)
		if !ok || !isAllowedModel(model) {
			continue
		}
		beads[i].Model = model
	}
}

// extractMetadataModelDetail promotes metadata["model"] into BeadDetail.Model
// when no explicit top-level Model is set. Allowlist and type rules match extractMetadataModel.
func extractMetadataModelDetail(detail *protocol.BeadDetail) {
	if detail == nil || detail.Model != "" || detail.Metadata == nil {
		return
	}
	val, ok := detail.Metadata["model"]
	if !ok {
		return
	}
	model, ok := val.(string)
	if !ok || !isAllowedModel(model) {
		return
	}
	detail.Model = model
}

// extractACFromDescription extracts the acceptance criteria section from a
// markdown description. It looks for "Acceptance Criteria" headers (case-insensitive)
// with or without "##" prefix, and returns everything after it up to the next H2 header
// or end of string.
func extractACFromDescription(desc string) string {
	descLower := strings.ToLower(desc)

	// Try "## acceptance criteria" (with hashes) at start of line.
	headerWithHash := "## acceptance criteria"
	idx := findHeaderAtLineStart(descLower, headerWithHash)
	headerLen := len(headerWithHash)

	// If not found, try plain "acceptance criteria" at start of line (for bd show output).
	if idx < 0 {
		headerNoHash := "acceptance criteria"
		idx = findHeaderAtLineStart(descLower, headerNoHash)
		headerLen = len(headerNoHash)
	}

	if idx < 0 {
		return ""
	}

	body := desc[idx+headerLen:]
	// Trim leading newlines.
	body = strings.TrimLeft(body, "\r\n")
	// Stop at the next H2 header if present.
	if next := strings.Index(body, "\n## "); next >= 0 {
		// body[next] is the '\n' immediately before '## '. Trim all trailing
		// newlines from the content portion (body[:next]), then re-add exactly
		// one '\n' only when the last content line terminated directly at the
		// ## boundary (no blank line between content and next header).
		// A blank line means body[next-1] == '\n' (two consecutive newlines).
		content := strings.TrimRight(body[:next], "\r\n")
		if next > 0 && body[next-1] != '\n' {
			// Single newline before ##: last content line ends here, keep terminator.
			body = content + "\n"
		} else {
			// Blank line (or start) before ##: drop trailing whitespace entirely.
			body = content
		}
	} else {
		// No following header: trim trailing whitespace for a clean result.
		body = strings.TrimRight(body, " \t\r\n")
	}
	return body
}

// findHeaderAtLineStart finds the header text at the start of a line
// (either at position 0 or after a newline).
func findHeaderAtLineStart(text, header string) int {
	// Check if it starts the text.
	if strings.HasPrefix(text, header) {
		return 0
	}
	// Check if it appears after a newline.
	search := "\n" + header
	if idx := strings.Index(text, search); idx >= 0 {
		return idx + 1 // Return position after the newline
	}
	return -1
}

// Close runs `bd close <id> --reason="<reason>"`.
func (s *CLIBeadSource) Close(ctx context.Context, id, reason string) error {
	_, err := s.runner.Run(ctx, "bd", "close", id, "--reason="+reason)
	if err != nil {
		return fmt.Errorf("bd close %s: %w", id, err)
	}
	return nil
}

// Update runs `bd update <id> --status=<status>`.
func (s *CLIBeadSource) Update(ctx context.Context, id, status string) error {
	_, err := s.runner.Run(ctx, "bd", "update", id, "--status="+status)
	if err != nil {
		return fmt.Errorf("bd update %s: %w", id, err)
	}
	return nil
}

// Create runs `bd create --title=... --type=... --priority=N --description=... --json`
// and optionally `--parent=...` if parent is non-empty and `--acceptance=...`
// if acceptanceCriteria is non-empty. It parses the JSON output to extract and return
// the new bead ID.
func (s *CLIBeadSource) Create(ctx context.Context, title, beadType string, priority int, description, parent, acceptanceCriteria string) (string, error) {
	// Bugs are always P0 — if it's not urgent, it should be a task or feature.
	if beadType == "bug" {
		priority = 0
	}
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
		args = append(args, "--acceptance="+acceptanceCriteria)
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

// Sync is a no-op. Beads syncing is now handled elsewhere.
func (s *CLIBeadSource) Sync(ctx context.Context) error {
	return nil
}

// HasChildren checks whether the given epic has any children (open or closed).
// Returns true if at least one child exists, false otherwise.
func (s *CLIBeadSource) HasChildren(ctx context.Context, epicID string) (bool, error) {
	out, err := s.runner.Run(ctx, "bd", "list", "--parent="+epicID, "--json")
	if err != nil {
		return false, fmt.Errorf("bd list --parent=%s: %w", epicID, err)
	}

	var children []protocol.Bead
	if err := json.Unmarshal(out, &children); err != nil {
		return false, fmt.Errorf("parse bd list output: %w", err)
	}

	return len(children) > 0, nil
}

// FindByParentAndTag runs `bd list --parent=<parentID> --tag=<tag> --json` and
// returns all matching beads. Returns an empty slice (not an error) when no
// beads match; returns a wrapped error on bd CLI failure or JSON parse failure.
func (s *CLIBeadSource) FindByParentAndTag(ctx context.Context, parentID, tag string) ([]protocol.Bead, error) {
	out, err := s.runner.Run(ctx, "bd", "list", "--parent="+parentID, "--tag="+tag, "--json")
	if err != nil {
		return nil, fmt.Errorf("bd list --parent=%s --tag=%s: %w", parentID, tag, err)
	}

	var beads []protocol.Bead
	if err := json.Unmarshal(out, &beads); err != nil {
		return nil, fmt.Errorf("parse bd list output: %w", err)
	}

	if len(beads) == 0 {
		return []protocol.Bead{}, nil
	}
	return beads, nil
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
