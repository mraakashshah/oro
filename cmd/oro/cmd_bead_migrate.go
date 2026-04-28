package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"oro/pkg/beadstore"
	"oro/pkg/protocol"

	"github.com/spf13/cobra"
)

func newBeadMigrateFromDoltCmd(store beadstore.Store) *cobra.Command {
	var opts beadMigrateOptions

	cmd := &cobra.Command{
		Use:   "migrate-from-dolt",
		Short: "Plan or run a bd/dolt to native bead-store migration",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if opts.reconcile {
				return writeBeadCommandErrorIfJSON(cmd, "unsupported", errors.New("--reconcile is not implemented in this migration seam"))
			}
			if opts.ignoreVersionDrift {
				return writeBeadCommandErrorIfJSON(cmd, "unsupported", errors.New("--ignore-version-drift is not implemented in this migration seam"))
			}

			source, data, err := readBeadMigrationSource(opts)
			if err != nil {
				return writeBeadCommandErrorIfJSON(cmd, "migrate", err)
			}
			if !opts.dryRun {
				if err := runBeadMigration(cmd.Context(), data); err != nil {
					return writeBeadCommandErrorIfJSON(cmd, "migrate", err)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Migration complete\nsource: %s", source.kind)
				if source.path != "" {
					fmt.Fprintf(cmd.OutOrStdout(), " (%s)", source.path)
				}
				fmt.Fprintln(cmd.OutOrStdout())
				_ = store
				return nil
			}

			plan, err := planBeadMigration(data)
			if err != nil {
				return writeBeadCommandErrorIfJSON(cmd, "migrate", err)
			}
			writeBeadMigrationPlan(cmd.OutOrStdout(), source, plan)
			_ = store
			return nil
		},
	}
	cmd.Flags().BoolVar(&opts.dryRun, "dry-run", false, "print a migration plan without mutating SQLite")
	cmd.Flags().BoolVar(&opts.reconcile, "reconcile", false, "reconcile a previous migration against current dolt state")
	cmd.Flags().StringVar(&opts.fromJSONL, "from-jsonl", "", "read bd export JSONL from a file instead of invoking bd")
	cmd.Flags().StringVar(&opts.fromFixture, "from-fixture", "", "read a test fixture directory or JSONL file instead of invoking bd")
	cmd.Flags().BoolVar(&opts.ignoreVersionDrift, "ignore-version-drift", false, "acknowledge bd/dolt version drift during migration")
	return cmd
}

type beadMigrateOptions struct {
	dryRun             bool
	reconcile          bool
	fromJSONL          string
	fromFixture        string
	ignoreVersionDrift bool
}

type beadMigrationSource struct {
	kind string
	path string
}

type beadMigrationPlan struct {
	Beads           int
	Dependencies    int
	Tags            int
	Labels          int
	MetadataEntries int
	Notes           int
	UnknownFields   int
	StatusCounts    map[string]int
}

type bdExportBead struct {
	ID                 string                `json:"id"`
	Title              string                `json:"title"`
	Description        string                `json:"description"`
	AcceptanceCriteria string                `json:"acceptance_criteria"`
	Status             string                `json:"status"`
	Priority           int                   `json:"priority"`
	Type               string                `json:"type"`
	IssueType          string                `json:"issue_type"`
	Parent             string                `json:"parent"`
	ParentID           string                `json:"parent_id"`
	Owner              string                `json:"owner"`
	Assignee           string                `json:"assignee"`
	EstimatedMinutes   int                   `json:"estimated_minutes"`
	Tier               string                `json:"tier"`
	Model              string                `json:"model"`
	CreatedAt          string                `json:"created_at"`
	UpdatedAt          string                `json:"updated_at"`
	ClosedAt           string                `json:"closed_at"`
	CloseReason        string                `json:"close_reason"`
	DeferredUntil      string                `json:"deferred_until"`
	DeferUntil         string                `json:"defer_until"`
	Dependencies       []protocol.Dependency `json:"dependencies"`
	Tags               []string              `json:"tags"`
	Labels             []string              `json:"labels"`
	Metadata           map[string]any        `json:"metadata"`
	Notes              json.RawMessage       `json:"notes"`
}

func runBeadMigration(ctx context.Context, data []byte) error {
	beads, err := decodeBDExport(data)
	if err != nil {
		return err
	}
	paths, err := ResolveProjectDBPaths()
	if err != nil {
		return fmt.Errorf("resolve bead store paths: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(paths.StateDBPath), 0o700); err != nil {
		return fmt.Errorf("create bead store dir: %w", err)
	}
	db, err := openStateDB(paths.StateDBPath)
	if err != nil {
		return err
	}
	defer db.Close()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if err := setBeadParentTouchTriggers(ctx, tx, false); err != nil {
		return err
	}
	for _, raw := range beads {
		var bead bdExportBead
		if err := json.Unmarshal(raw, &bead); err != nil {
			return fmt.Errorf("decode bd export bead: %w", err)
		}
		if err := insertMigratedBead(ctx, tx, bead); err != nil {
			return err
		}
	}
	if err := setBeadParentTouchTriggers(ctx, tx, true); err != nil {
		return err
	}
	return tx.Commit()
}

func insertMigratedBead(ctx context.Context, tx *sql.Tx, bead bdExportBead) error {
	if strings.TrimSpace(bead.ID) == "" {
		return fmt.Errorf("bd export bead is missing id")
	}
	if strings.TrimSpace(bead.Title) == "" {
		return fmt.Errorf("bd export bead %s is missing title", bead.ID)
	}
	beadType := firstNonEmpty(bead.IssueType, bead.Type, "task")
	createdAt := firstNonEmpty(bead.CreatedAt, bead.UpdatedAt)
	updatedAt := firstNonEmpty(bead.UpdatedAt, bead.CreatedAt)
	if createdAt == "" || updatedAt == "" {
		return fmt.Errorf("bd export bead %s is missing created_at or updated_at", bead.ID)
	}

	if _, err := tx.ExecContext(ctx, `
INSERT INTO beads (
	id, title, description, acceptance_criteria, status, priority, type, parent_id,
	owner, estimated_minutes, tier, model, deferred_until, close_reason,
	created_at, updated_at, closed_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		bead.ID,
		bead.Title,
		bead.Description,
		bead.AcceptanceCriteria,
		normalizeMigrationInsertStatus(bead.Status),
		bead.Priority,
		beadType,
		emptyStringToNil(firstNonEmpty(bead.ParentID, bead.Parent)),
		emptyStringToNil(firstNonEmpty(bead.Owner, bead.Assignee)),
		positiveIntToNil(bead.EstimatedMinutes),
		emptyStringToNil(bead.Tier),
		emptyStringToNil(bead.Model),
		emptyStringToNil(firstNonEmpty(bead.DeferredUntil, bead.DeferUntil)),
		emptyStringToNil(bead.CloseReason),
		createdAt,
		updatedAt,
		emptyStringToNil(bead.ClosedAt),
	); err != nil {
		return fmt.Errorf("insert migrated bead %s: %w", bead.ID, err)
	}
	for _, dep := range bead.Dependencies {
		depType := firstNonEmpty(dep.Type, "blocks")
		dependsOn := strings.TrimSpace(dep.DependsOnID)
		if dependsOn == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO bead_deps (bead_id, depends_on_id, type) VALUES (?, ?, ?)`, bead.ID, dependsOn, depType); err != nil {
			return fmt.Errorf("insert migrated dependency for %s: %w", bead.ID, err)
		}
	}
	for _, tag := range bead.Tags {
		if strings.TrimSpace(tag) == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO bead_tags (bead_id, tag) VALUES (?, ?)`, bead.ID, tag); err != nil {
			return fmt.Errorf("insert migrated tag for %s: %w", bead.ID, err)
		}
	}
	for _, label := range bead.Labels {
		if strings.TrimSpace(label) == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO bead_labels (bead_id, label) VALUES (?, ?)`, bead.ID, label); err != nil {
			return fmt.Errorf("insert migrated label for %s: %w", bead.ID, err)
		}
	}
	for key, value := range bead.Metadata {
		if strings.TrimSpace(key) == "" {
			continue
		}
		encoded, err := migrationMetadataValue(value)
		if err != nil {
			return fmt.Errorf("encode metadata %s for %s: %w", key, bead.ID, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO bead_metadata (bead_id, key, value) VALUES (?, ?, ?)`, bead.ID, key, encoded); err != nil {
			return fmt.Errorf("insert migrated metadata for %s: %w", bead.ID, err)
		}
	}
	notes, err := migrationNotes(bead.Notes)
	if err != nil {
		return fmt.Errorf("decode notes for %s: %w", bead.ID, err)
	}
	for _, note := range notes {
		if strings.TrimSpace(note) == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO bead_notes (bead_id, author, content, created_at) VALUES (?, 'bd', ?, ?)`, bead.ID, note, updatedAt); err != nil {
			return fmt.Errorf("insert migrated note for %s: %w", bead.ID, err)
		}
	}
	return nil
}

func setBeadParentTouchTriggers(ctx context.Context, tx *sql.Tx, enabled bool) error {
	if enabled {
		_, err := tx.ExecContext(ctx, protocol.BeadParentTouchTriggerDDL)
		return err
	}
	for _, name := range protocol.BeadParentTouchTriggerNames {
		if _, err := tx.ExecContext(ctx, `DROP TRIGGER IF EXISTS `+name); err != nil {
			return err
		}
	}
	return nil
}

func readBeadMigrationSource(opts beadMigrateOptions) (beadMigrationSource, []byte, error) {
	if opts.fromFixture != "" && opts.fromJSONL != "" {
		return beadMigrationSource{}, nil, fmt.Errorf("--from-fixture and --from-jsonl are mutually exclusive")
	}
	if opts.fromFixture != "" {
		path, err := resolveMigrationFixturePath(opts.fromFixture)
		if err != nil {
			return beadMigrationSource{}, nil, err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return beadMigrationSource{}, nil, fmt.Errorf("read fixture export: %w", err)
		}
		return beadMigrationSource{kind: "fixture", path: path}, data, nil
	}
	if opts.fromJSONL != "" {
		data, err := os.ReadFile(opts.fromJSONL)
		if err != nil {
			return beadMigrationSource{}, nil, fmt.Errorf("read JSONL export: %w", err)
		}
		return beadMigrationSource{kind: "jsonl", path: opts.fromJSONL}, data, nil
	}

	out, err := exec.Command("bd", "export").Output()
	if err != nil {
		return beadMigrationSource{}, nil, fmt.Errorf("run bd export: %w", err)
	}
	return beadMigrationSource{kind: "bd export"}, out, nil
}

func resolveMigrationFixturePath(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("stat fixture: %w", err)
	}
	if !info.IsDir() {
		return path, nil
	}
	for _, name := range []string{"export.jsonl", "beads.jsonl"} {
		candidate := filepath.Join(path, name)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("fixture %s does not contain export.jsonl or beads.jsonl", path)
}

func planBeadMigration(data []byte) (beadMigrationPlan, error) {
	beads, err := decodeBDExport(data)
	if err != nil {
		return beadMigrationPlan{}, err
	}

	plan := beadMigrationPlan{StatusCounts: map[string]int{}}
	for _, raw := range beads {
		var bead bdExportBead
		if err := json.Unmarshal(raw, &bead); err != nil {
			return beadMigrationPlan{}, fmt.Errorf("decode bd export bead: %w", err)
		}

		plan.Beads++
		plan.Dependencies += len(bead.Dependencies)
		plan.Tags += len(bead.Tags)
		plan.Labels += len(bead.Labels)
		plan.MetadataEntries += len(bead.Metadata)
		notes, err := countMigrationNotes(bead.Notes)
		if err != nil {
			return beadMigrationPlan{}, fmt.Errorf("count notes for %s: %w", bead.ID, err)
		}
		plan.Notes += notes
		plan.StatusCounts[normalizeMigrationStatus(bead.Status)]++

		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return beadMigrationPlan{}, err
		}
		for field := range fields {
			if !knownBDExportField(field) {
				plan.UnknownFields++
			}
		}
	}
	return plan, nil
}

func decodeBDExport(data []byte) ([]json.RawMessage, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("bd export is empty")
	}
	if trimmed[0] == '[' {
		var rows []json.RawMessage
		if err := json.Unmarshal(trimmed, &rows); err != nil {
			return nil, fmt.Errorf("decode bd export JSON array: %w", err)
		}
		return rows, nil
	}

	dec := json.NewDecoder(bytes.NewReader(trimmed))
	var rows []json.RawMessage
	for {
		var row json.RawMessage
		if err := dec.Decode(&row); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("decode bd export JSONL: %w", err)
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func countMigrationNotes(raw json.RawMessage) (int, error) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return 0, nil
	}
	var noteString string
	if err := json.Unmarshal(raw, &noteString); err == nil {
		if strings.TrimSpace(noteString) == "" {
			return 0, nil
		}
		return 1, nil
	}
	var notes []json.RawMessage
	if err := json.Unmarshal(raw, &notes); err != nil {
		return 0, err
	}
	count := 0
	for _, note := range notes {
		if len(bytes.TrimSpace(note)) > 0 && !bytes.Equal(bytes.TrimSpace(note), []byte("null")) {
			count++
		}
	}
	return count, nil
}

func normalizeMigrationStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "", "open", "pending", "to-do":
		return "open"
	case "in_progress", "blocked", "closed":
		return strings.ToLower(strings.TrimSpace(status))
	default:
		return "open"
	}
}

func normalizeMigrationInsertStatus(status string) string {
	switch normalizeMigrationStatus(status) {
	case "in_progress", "closed":
		return normalizeMigrationStatus(status)
	default:
		return "open"
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func emptyStringToNil(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func positiveIntToNil(value int) any {
	if value <= 0 {
		return nil
	}
	return value
}

func migrationMetadataValue(value any) (string, error) {
	if s, ok := value.(string); ok {
		return s, nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func migrationNotes(raw json.RawMessage) ([]string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	var noteString string
	if err := json.Unmarshal(trimmed, &noteString); err == nil {
		return []string{noteString}, nil
	}
	var noteStrings []string
	if err := json.Unmarshal(trimmed, &noteStrings); err == nil {
		return noteStrings, nil
	}
	var notes []struct {
		Content string `json:"content"`
		Text    string `json:"text"`
	}
	if err := json.Unmarshal(trimmed, &notes); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(notes))
	for _, note := range notes {
		out = append(out, firstNonEmpty(note.Content, note.Text))
	}
	return out, nil
}

func knownBDExportField(field string) bool {
	switch field {
	case "id", "title", "description", "acceptance_criteria", "status", "priority",
		"type", "issue_type", "parent", "parent_id", "owner", "assignee", "estimated_minutes",
		"tier", "model", "created_at", "updated_at", "closed_at", "close_reason",
		"deferred_until", "defer_until", "dependencies", "tags", "labels",
		"metadata", "notes":
		return true
	default:
		return false
	}
}

func writeBeadMigrationPlan(w io.Writer, source beadMigrationSource, plan beadMigrationPlan) {
	fmt.Fprintln(w, "Migration plan")
	if source.path != "" {
		fmt.Fprintf(w, "source: %s (%s)\n", source.kind, source.path)
	} else {
		fmt.Fprintf(w, "source: %s\n", source.kind)
	}
	fmt.Fprintf(w, "beads: %d\n", plan.Beads)
	fmt.Fprintf(w, "dependencies: %d\n", plan.Dependencies)
	fmt.Fprintf(w, "tags: %d\n", plan.Tags)
	fmt.Fprintf(w, "labels: %d\n", plan.Labels)
	fmt.Fprintf(w, "metadata entries: %d\n", plan.MetadataEntries)
	fmt.Fprintf(w, "notes: %d\n", plan.Notes)
	if plan.UnknownFields > 0 {
		fmt.Fprintf(w, "unknown fields: %d\n", plan.UnknownFields)
	}
	fmt.Fprintln(w, "DRY RUN -- no writes performed")
}
