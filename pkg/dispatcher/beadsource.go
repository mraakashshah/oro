package dispatcher

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"oro/pkg/beadstore"
	"oro/pkg/protocol"
)

// CommandRunner abstracts command execution for testability.
// Production implementation uses os/exec; tests provide a mock.
type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

var _ beadstore.Store = (*CLIStore)(nil)
var _ DeferredStore = (*CLIStore)(nil)

// CLIStore implements the bead store interfaces by shelling out to the oro CLI.
type CLIStore struct {
	runner      CommandRunner
	BdExtraArgs []string // optional args inserted after "bead" for legacy tests/config
}

// NewCLIStore creates a CLIStore backed by the given CommandRunner.
func NewCLIStore(runner CommandRunner) *CLIStore {
	return &CLIStore{runner: runner}
}

// beadArgs returns the full argument list for an oro bead invocation.
func (s *CLIStore) beadArgs(args ...string) []string {
	combined := make([]string, 0, 1+len(s.BdExtraArgs)+len(args))
	combined = append(combined, "bead")
	if len(s.BdExtraArgs) == 0 {
		return append(combined, args...)
	}
	combined = append(combined, s.BdExtraArgs...)
	combined = append(combined, args...)
	return combined
}

type cliBeadJSON struct {
	protocol.Bead
	ParentID string `json:"parent_id"`
	TypeName string `json:"type"`
}

func (b cliBeadJSON) toProtocol() protocol.Bead {
	bead := b.Bead
	if bead.Epic == "" {
		bead.Epic = b.ParentID
	}
	if bead.Type == "" {
		bead.Type = b.TypeName
	}
	return bead
}

func decodeBeadList(out []byte) ([]protocol.Bead, error) {
	var raw []cliBeadJSON
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, err
	}
	beads := make([]protocol.Bead, len(raw))
	for i, bead := range raw {
		beads[i] = bead.toProtocol()
	}
	return beads, nil
}

func decodeBeadDetail(out []byte) (*protocol.BeadDetail, error) {
	var arr []cliBeadJSON
	if err := json.Unmarshal(out, &arr); err == nil {
		if len(arr) == 0 {
			return nil, nil
		}
		detail := arr[0].toProtocol()
		return &detail, nil
	}

	var obj cliBeadJSON
	if err := json.Unmarshal(out, &obj); err != nil {
		return nil, err
	}
	detail := obj.toProtocol()
	return &detail, nil
}

func decodeBeadExport(out []byte) ([]protocol.Bead, error) {
	var rawArray []cliBeadJSON
	if err := json.Unmarshal(out, &rawArray); err == nil {
		beads := make([]protocol.Bead, len(rawArray))
		for i, bead := range rawArray {
			beads[i] = bead.toProtocol()
		}
		return beads, nil
	}

	decoder := json.NewDecoder(bytes.NewReader(out))
	var beads []protocol.Bead
	for {
		var raw cliBeadJSON
		if err := decoder.Decode(&raw); err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		beads = append(beads, raw.toProtocol())
	}
	return beads, nil
}

func (s *CLIStore) childrenForParent(ctx context.Context, epicID string) ([]protocol.Bead, error) {
	out, err := s.Export(ctx)
	if err != nil {
		return nil, err
	}
	beads, err := decodeBeadExport(out)
	if err != nil {
		return nil, fmt.Errorf("parse oro bead export output: %w", err)
	}
	var children []protocol.Bead
	for _, bead := range beads {
		if bead.Epic == epicID {
			children = append(children, bead)
		}
	}
	return children, nil
}

// Ready runs `oro bead ready --json` and parses the output into a slice of Bead.
func (s *CLIStore) Ready(ctx context.Context) ([]protocol.Bead, error) {
	out, err := s.runner.Run(ctx, "oro", s.beadArgs("ready", "--json")...)
	if err != nil {
		return nil, fmt.Errorf("oro bead ready: %w", err)
	}

	beads, err := decodeBeadList(out)
	if err != nil {
		return nil, fmt.Errorf("parse oro bead ready output: %w", err)
	}
	extractMetadataModel(beads)
	return beads, nil
}

// InProgress runs `oro bead list --status=in_progress --json` and parses the output into a slice of Bead.
// Returns nil slice (not empty slice) when the CLI reports no in-progress beads.
func (s *CLIStore) InProgress(ctx context.Context) ([]protocol.Bead, error) {
	out, err := s.runner.Run(ctx, "oro", s.beadArgs("list", "--status=in_progress", "--json")...)
	if err != nil {
		return nil, fmt.Errorf("oro bead list --status=in_progress: %w", err)
	}

	beads, err := decodeBeadList(out)
	if err != nil {
		return nil, fmt.Errorf("parse oro bead list output: %w", err)
	}
	if len(beads) == 0 {
		return nil, nil
	}
	extractMetadataModel(beads)
	return beads, nil
}

// Blocked runs `oro bead list --status=blocked --json` and parses the output into a slice of Bead.
// Returns nil slice (not empty slice) when the CLI reports no blocked beads.
func (s *CLIStore) Blocked(ctx context.Context) ([]protocol.Bead, error) {
	out, err := s.runner.Run(ctx, "oro", s.beadArgs("list", "--status=blocked", "--json")...)
	if err != nil {
		return nil, fmt.Errorf("oro bead list --status=blocked: %w", err)
	}

	beads, err := decodeBeadList(out)
	if err != nil {
		return nil, fmt.Errorf("parse oro bead list output: %w", err)
	}
	if len(beads) == 0 {
		return nil, nil
	}
	extractMetadataModel(beads)
	return beads, nil
}

// Closed runs `oro bead list --status=closed --json --limit=<limit>` and parses the output into a slice of Bead.
// Returns nil slice (not empty slice) when the CLI reports no closed beads.
func (s *CLIStore) Closed(ctx context.Context, limit int) ([]protocol.Bead, error) {
	out, err := s.runner.Run(ctx, "oro", s.beadArgs("list", "--status=closed", fmt.Sprintf("--limit=%d", limit), "--json")...)
	if err != nil {
		return nil, fmt.Errorf("oro bead list --status=closed: %w", err)
	}

	beads, err := decodeBeadList(out)
	if err != nil {
		return nil, fmt.Errorf("parse oro bead list output: %w", err)
	}
	if len(beads) == 0 {
		return nil, nil
	}
	extractMetadataModel(beads)
	return beads, nil
}

// Show runs `oro bead show <id> --json` and parses the output into a Bead.
func (s *CLIStore) Show(ctx context.Context, id string) (*protocol.Bead, error) {
	out, err := s.runner.Run(ctx, "oro", s.beadArgs("show", id, "--json")...)
	if err != nil {
		return nil, fmt.Errorf("oro bead show %s: %w", id, err)
	}

	detail, err := decodeBeadDetail(out)
	if err != nil {
		return nil, fmt.Errorf("parse oro bead show output: %w", err)
	}
	if detail == nil {
		return nil, fmt.Errorf("bead %s not found", id)
	}

	// Some CLI output has no separate acceptance_criteria field; AC is embedded
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

	// If not found, try plain "acceptance criteria" at start of line.
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

// Close runs `oro bead close <id> --reason="<reason>"`.
func (s *CLIStore) Close(ctx context.Context, id, reason string) error {
	_, err := s.runner.Run(ctx, "oro", s.beadArgs("close", id, "--reason="+reason)...)
	if err != nil {
		return fmt.Errorf("oro bead close %s: %w", id, err)
	}
	return nil
}

// Defer runs `oro bead defer <id> --until=<until>`.
func (s *CLIStore) Defer(ctx context.Context, id, until string) error {
	_, err := s.runner.Run(ctx, "oro", s.beadArgs("defer", id, "--until="+until)...)
	if err != nil {
		return fmt.Errorf("oro bead defer %s: %w", id, err)
	}
	return nil
}

// Undefer runs `oro bead undefer <id>`.
func (s *CLIStore) Undefer(ctx context.Context, id string) error {
	_, err := s.runner.Run(ctx, "oro", s.beadArgs("undefer", id)...)
	if err != nil {
		return fmt.Errorf("oro bead undefer %s: %w", id, err)
	}
	return nil
}

// Update runs `oro bead update <id> ...` then re-reads the bead to verify
// the status was actually persisted. The CLI can exit 0 on a no-op (e.g. cwd
// mismatch, wrong db path), so we must verify explicitly rather than trust exit code.
func (s *CLIStore) Update(ctx context.Context, id string, params beadstore.UpdateParams) error {
	args := []string{"update", id}
	if params.Status != nil {
		args = append(args, "--status="+*params.Status)
	}
	if params.Priority != nil {
		args = append(args, fmt.Sprintf("--priority=%d", *params.Priority))
	}
	if params.Type != nil {
		args = append(args, "--type="+*params.Type)
	}
	if params.ParentID != nil {
		args = append(args, "--parent="+*params.ParentID)
	}
	if params.Owner != nil {
		args = append(args, "--owner="+*params.Owner)
	}
	if params.AcceptanceCriteria != nil {
		args = append(args, "--acceptance="+*params.AcceptanceCriteria)
	}
	if params.Notes != nil {
		args = append(args, "--notes="+*params.Notes)
	}

	_, err := s.runner.Run(ctx, "oro", s.beadArgs(args...)...)
	if err != nil {
		return fmt.Errorf("oro bead update %s: %w", id, err)
	}
	if params.Status != nil {
		detail, err := s.Show(ctx, id)
		if err != nil {
			return fmt.Errorf("oro bead update %s: post-update verify failed: %w", id, err)
		}
		if detail.Status != *params.Status {
			return fmt.Errorf("oro bead update %s: status not persisted (got %q, want %q) — possible cwd mismatch or wrong db path", id, detail.Status, *params.Status)
		}
	}
	return nil
}

// Create runs `oro bead create --title=... --type=... --priority=N --description=... --json`
// and optionally `--parent=...` if parent is non-empty and `--acceptance=...`
// if acceptanceCriteria is non-empty. It parses the JSON output to extract and return
// the new bead ID.
func (s *CLIStore) Create(ctx context.Context, params beadstore.CreateParams) (*protocol.Bead, error) {
	// Bugs are always P0 — if it's not urgent, it should be a task or feature.
	if params.Type == "bug" {
		params.Priority = 0
	}
	args := []string{
		"create",
		"--title=" + params.Title,
		"--type=" + params.Type,
		fmt.Sprintf("--priority=%d", params.Priority),
		"--description=" + params.Description,
	}
	if params.ParentID != "" {
		args = append(args, "--parent="+params.ParentID)
	}
	if params.AcceptanceCriteria != "" {
		args = append(args, "--acceptance="+params.AcceptanceCriteria)
	}
	args = append(args, "--json")

	out, err := s.runner.Run(ctx, "oro", s.beadArgs(args...)...)
	if err != nil {
		return nil, fmt.Errorf("oro bead create: %w", err)
	}

	var result struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		return nil, fmt.Errorf("parse oro bead create output: %w", err)
	}
	if result.ID == "" {
		return nil, fmt.Errorf("parse oro bead create output: missing id")
	}
	return &protocol.Bead{ID: result.ID}, nil
}

// Sync is a no-op retained for older callers that still treat the CLI adapter
// as a flushable boundary.
func (s *CLIStore) Sync(ctx context.Context) error {
	return nil
}

// HasChildren checks whether the given epic has any children (open or closed).
// Returns true if at least one child exists, false otherwise.
func (s *CLIStore) HasChildren(ctx context.Context, epicID string) (bool, error) {
	children, err := s.childrenForParent(ctx, epicID)
	if err != nil {
		return false, fmt.Errorf("oro bead children for parent %s: %w", epicID, err)
	}

	return len(children) > 0, nil
}

// FindByParentAndTag runs `oro bead list --parent=<parentID> --tag=<tag> --json` and
// returns all matching beads. Returns an empty slice (not an error) when no
// beads match; returns a wrapped error on CLI failure or JSON parse failure.
func (s *CLIStore) FindByParentAndTag(ctx context.Context, parentID, tag string) ([]protocol.Bead, error) {
	out, err := s.runner.Run(ctx, "oro", s.beadArgs("list", "--parent="+parentID, "--tag="+tag, "--json")...)
	if err != nil {
		return nil, fmt.Errorf("oro bead list --parent=%s --tag=%s: %w", parentID, tag, err)
	}

	beads, err := decodeBeadList(out)
	if err != nil {
		return nil, fmt.Errorf("parse oro bead list output: %w", err)
	}

	if len(beads) == 0 {
		return []protocol.Bead{}, nil
	}
	return beads, nil
}

// Export runs `oro bead export` and returns the raw JSONL output containing all issues
// (both open and closed). Returns an error if the command fails.
func (s *CLIStore) Export(ctx context.Context) ([]byte, error) {
	out, err := s.runner.Run(ctx, "oro", s.beadArgs("export")...)
	if err != nil {
		return nil, fmt.Errorf("oro bead export: %w", err)
	}
	return out, nil
}

// AllChildrenClosed checks whether all children of the given epic are closed.
// Fetches all children from export and checks status locally to avoid CLI query filter quirks.
// Returns true only if every child has status "closed".
func (s *CLIStore) AllChildrenClosed(ctx context.Context, epicID string) (bool, error) {
	children, err := s.childrenForParent(ctx, epicID)
	if err != nil {
		return false, fmt.Errorf("oro bead children for parent %s: %w", epicID, err)
	}

	if len(children) == 0 {
		return false, nil // no children → not "all closed"
	}

	for _, child := range children {
		if child.Status != "closed" {
			return false, nil
		}
	}
	return true, nil
}
