package data

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"oro/pkg/beadstore"
	"oro/pkg/protocol"
)

// SourceMode indicates how issues are loaded.
type SourceMode int

const (
	SourceJSONL SourceMode = iota // Legacy: read from .beads/issues.jsonl (or --path)
	SourceCLI                     // Preferred: read from beadstore.Store
)

// Source describes how mg loads its issue data.
type Source struct {
	Mode       SourceMode
	Path       string // JSONL file path (SourceJSONL) or empty (SourceCLI)
	ProjectDir string // Project root directory
	Explicit   bool   // True if --path was used
	Store      beadstore.Store
	Err        error
}

// newSource creates a store-backed Source for projectDir.
func newSource(store beadstore.Store, projectDir string) Source {
	return Source{
		Mode:       SourceCLI,
		ProjectDir: projectDir,
		Store:      store,
	}
}

// Label returns a display string for the footer.
func (s Source) Label() string {
	if s.Mode == SourceCLI {
		return "bead store"
	}
	if s.Path != "" {
		return filepath.Base(s.Path)
	}
	return "issues.jsonl"
}

// FetchIssues reads the full issue set from store.Export.
//
//oro:testonly
func FetchIssues(store beadstore.Store) ([]Issue, error) {
	if store == nil {
		return nil, fmt.Errorf("bead store is nil")
	}
	out, err := store.Export(context.Background())
	if err != nil {
		return nil, fmt.Errorf("export beads: %w", err)
	}
	return parseIssuesJSONL(out)
}

// FetchActiveIssues fetches only non-closed issues. Used by the poll loop.
func FetchActiveIssues(store beadstore.Store) ([]Issue, error) {
	issues, err := FetchIssues(store)
	if err != nil {
		return nil, err
	}
	active := issues[:0]
	for _, issue := range issues {
		if issue.Status != StatusClosed {
			active = append(active, issue)
		}
	}
	return active, nil
}

// fetchRecentClosed fetches the N most recently closed issues.
func fetchRecentClosed(store beadstore.Store, limit int) ([]Issue, error) {
	if store == nil {
		return nil, fmt.Errorf("bead store is nil")
	}
	beads, err := store.Closed(context.Background(), limit)
	if err != nil {
		return nil, fmt.Errorf("fetch closed beads: %w", err)
	}
	return issuesFromBeads(beads)
}

// FetchAllClosed fetches all closed issues (for background hydration).
func FetchAllClosed(store beadstore.Store) ([]Issue, error) {
	issues, err := FetchIssues(store)
	if err != nil {
		return nil, err
	}
	closed := issues[:0]
	for _, issue := range issues {
		if issue.Status == StatusClosed {
			closed = append(closed, issue)
		}
	}
	return closed, nil
}

func parseIssuesJSONL(out []byte) ([]Issue, error) {
	var issues []Issue
	scanner := bufio.NewScanner(bytes.NewReader(out))
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		bead, err := parseExportBead(line)
		if err != nil {
			return nil, fmt.Errorf("parse bead export: %w", err)
		}
		issue, err := issueFromBead(bead)
		if err != nil {
			return nil, err
		}
		issues = append(issues, issue)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan bead export: %w", err)
	}
	SortIssues(issues)
	return issues, nil
}

func parseExportBead(line []byte) (protocol.Bead, error) {
	var row struct {
		protocol.Bead
		NativeParentID string `json:"parent_id"`
		NativeType     string `json:"type"`
	}
	if err := json.Unmarshal(line, &row); err != nil {
		return protocol.Bead{}, err
	}
	bead := row.Bead
	if bead.Epic == "" {
		bead.Epic = row.NativeParentID
	}
	if bead.Type == "" {
		bead.Type = row.NativeType
	}
	return bead, nil
}

func issuesFromBeads(beads []protocol.Bead) ([]Issue, error) {
	issues := make([]Issue, 0, len(beads))
	seen := make(map[string]struct{}, len(beads))
	for _, bead := range beads {
		if _, ok := seen[bead.ID]; ok {
			continue
		}
		seen[bead.ID] = struct{}{}
		issue, err := issueFromBead(bead)
		if err != nil {
			return nil, err
		}
		issues = append(issues, issue)
	}
	SortIssues(issues)
	return issues, nil
}

func issueFromBead(bead protocol.Bead) (Issue, error) {
	createdAt, err := parseBeadTime(bead.CreatedAt)
	if err != nil {
		return Issue{}, fmt.Errorf("parse created_at for %s: %w", bead.ID, err)
	}
	updatedAt, err := parseBeadTime(bead.UpdatedAt)
	if err != nil {
		return Issue{}, fmt.Errorf("parse updated_at for %s: %w", bead.ID, err)
	}
	closedAt, err := parseOptionalBeadTime(bead.ClosedAt)
	if err != nil {
		return Issue{}, fmt.Errorf("parse closed_at for %s: %w", bead.ID, err)
	}
	deferUntil, err := parseOptionalBeadTime(bead.DeferUntil)
	if err != nil {
		return Issue{}, fmt.Errorf("parse defer_until for %s: %w", bead.ID, err)
	}
	return Issue{
		ID:                 bead.ID,
		Title:              bead.Title,
		Description:        bead.Description,
		Status:             Status(bead.Status),
		Priority:           Priority(bead.Priority),
		IssueType:          IssueType(bead.Type),
		ParentIDValue:      bead.Epic,
		Owner:              bead.Owner,
		CreatedAt:          createdAt,
		UpdatedAt:          updatedAt,
		ClosedAt:           closedAt,
		CloseReason:        bead.CloseReason,
		Dependencies:       dependenciesFromBead(bead.Dependencies),
		Notes:              bead.Notes,
		AcceptanceCriteria: bead.AcceptanceCriteria,
		Labels:             append([]string(nil), bead.Labels...),
		DeferUntil:         deferUntil,
		Metadata:           metadataFromBead(bead.Metadata),
		Tags:               append([]string(nil), bead.Tags...),
	}, nil
}

func dependenciesFromBead(deps []protocol.Dependency) []Dependency {
	if len(deps) == 0 {
		return nil
	}
	out := make([]Dependency, len(deps))
	for i, dep := range deps {
		out[i] = Dependency{
			IssueID:     dep.IssueID,
			DependsOnID: dep.DependsOnID,
			Type:        dep.Type,
		}
	}
	return out
}

func metadataFromBead(metadata map[string]any) map[string]interface{} {
	if metadata == nil {
		return nil
	}
	out := make(map[string]interface{}, len(metadata))
	for key, value := range metadata {
		out[key] = value
	}
	return out
}

func parseBeadTime(raw string) (time.Time, error) {
	if strings.TrimSpace(raw) == "" {
		return time.Time{}, nil
	}
	return parseStoreTime(raw)
}

func parseOptionalBeadTime(raw string) (*time.Time, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	parsed, err := parseStoreTime(raw)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func parseStoreTime(raw string) (time.Time, error) {
	if parsed, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return parsed, nil
	}
	return time.Parse(time.RFC3339, raw)
}

// ParseIssuesJSON parses Beads/Oro issue arrays through mg's CLI JSON path.
func ParseIssuesJSON(out []byte, expectedPrefix string) ([]Issue, error) {
	return parseIssuesCLIOutput(out, expectedPrefix)
}

func parseIssuesCLIOutput(out []byte, expectedPrefix string) ([]Issue, error) {
	var issues []Issue
	if err := json.Unmarshal(out, &issues); err != nil {
		// Check if the legacy list source returned tree-formatted text instead of JSON.
		trimmed := strings.TrimSpace(string(out))
		if trimmed != "" && !strings.HasPrefix(trimmed, "[") && !strings.HasPrefix(trimmed, "{") {
			return nil, fmt.Errorf("legacy issue list returned non-JSON output (tree format?)")
		}
		return nil, fmt.Errorf("legacy issue list parse: %w", err)
	}
	if err := validateIssuePrefixes(issues, expectedPrefix); err != nil {
		return nil, err
	}
	SortIssues(issues)
	return issues, nil
}

// StoreContext holds workspace identity for the active native issue store.
type StoreContext struct {
	StoreDir     string `json:"store_dir"`
	RepoRoot     string `json:"repo_root"`
	IsRedirected bool   `json:"is_redirected"`
	Backend      string `json:"backend"`
	Database     string `json:"database"`
	Role         string `json:"role"`
}

func validateIssuePrefixes(issues []Issue, expectedPrefix string) error {
	expectedPrefix = strings.TrimSpace(expectedPrefix)
	if expectedPrefix == "" || len(issues) == 0 {
		return nil
	}

	seenExpected := false
	mismatched := make(map[string]bool)
	for _, issue := range issues {
		prefix := issuePrefixFromID(issue.ID)
		if prefix == "" {
			continue
		}
		if prefix == expectedPrefix {
			seenExpected = true
			continue
		}
		if prefix == "hq" {
			continue
		}
		mismatched[prefix] = true
	}

	if seenExpected || len(mismatched) != 1 {
		return nil
	}

	for prefix := range mismatched {
		return fmt.Errorf("legacy issue list returned %q issues, but this workspace expects %q", prefix, expectedPrefix)
	}
	return nil
}

func issuePrefixFromID(id string) string {
	prefix, _, ok := strings.Cut(strings.TrimSpace(id), "-")
	if !ok {
		return ""
	}
	return prefix
}

// FetchIssueDetail loads a single issue from the bead store.
func FetchIssueDetail(store beadstore.Store, issueID string) (*Issue, error) {
	if store == nil {
		return nil, fmt.Errorf("bead store is nil")
	}
	bead, err := store.Show(context.Background(), issueID)
	if err != nil {
		return nil, fmt.Errorf("show bead: %w", err)
	}
	if bead == nil {
		return nil, fmt.Errorf("bead %s not found", issueID)
	}
	issue, err := issueFromBead(*bead)
	if err != nil {
		return nil, err
	}
	return &issue, nil
}

// FetchIssueDetailFromIssues loads detail from an already-loaded JSONL snapshot.
func FetchIssueDetailFromIssues(issues []Issue, issueID string) (*Issue, error) {
	for i := range issues {
		if issues[i].ID == issueID {
			issue := issues[i]
			return &issue, nil
		}
	}
	return nil, fmt.Errorf("issue %s not found in loaded issue snapshot", issueID)
}

// FetchIssuesNow returns a tea.Cmd that fetches active issues via Store
// immediately (no timer delay). Emits ActiveIssuesMsg for merge with cached
// closed issues, or FileWatchErrorMsg on failure.
func FetchIssuesNow(store beadstore.Store) tea.Cmd {
	return func() tea.Msg {
		issues, err := FetchActiveIssues(store)
		if err != nil {
			return FileWatchErrorMsg{Err: err}
		}
		return ActiveIssuesMsg{Issues: issues}
	}
}
