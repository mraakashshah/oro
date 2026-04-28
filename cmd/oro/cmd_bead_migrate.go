package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

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
				source, data, err := readBeadMigrationSource(opts)
				if err != nil {
					return writeBeadCommandErrorIfJSON(cmd, "reconcile", err)
				}
				report, err := runBeadReconcile(cmd.Context(), data, opts.apply)
				if err != nil {
					return writeBeadCommandErrorIfJSON(cmd, "reconcile", err)
				}
				writeBeadReconcileReport(cmd.OutOrStdout(), source, report, opts.apply)
				return nil
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
	cmd.Flags().BoolVar(&opts.apply, "apply", false, "apply reconcile changes; without this, --reconcile is a dry-run")
	cmd.Flags().StringVar(&opts.fromJSONL, "from-jsonl", "", "read bd export JSONL from a file instead of invoking bd")
	cmd.Flags().StringVar(&opts.fromFixture, "from-fixture", "", "read a test fixture directory or JSONL file instead of invoking bd")
	cmd.Flags().BoolVar(&opts.ignoreVersionDrift, "ignore-version-drift", false, "acknowledge bd/dolt version drift during migration")
	return cmd
}

type beadMigrateOptions struct {
	dryRun             bool
	reconcile          bool
	apply              bool
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

type beadReconcileReport struct {
	SourceBeads   int
	SQLiteBeads   int
	Inserts       int
	Updates       int
	Deletes       int
	Conflicts     int
	ConflictedIDs []string
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
	return writeMigratedBead(ctx, tx, bead, false)
}

func updateMigratedBead(ctx context.Context, tx *sql.Tx, bead bdExportBead) error {
	return writeMigratedBead(ctx, tx, bead, true)
}

func writeMigratedBead(ctx context.Context, tx *sql.Tx, bead bdExportBead, update bool) error {
	if strings.TrimSpace(bead.ID) == "" {
		return fmt.Errorf("bd export bead is missing id")
	}
	if strings.TrimSpace(bead.Title) == "" {
		return fmt.Errorf("bd export bead %s is missing title", bead.ID)
	}
	normalizedBead, err := normalizeBDExportBeadForMigration(bead)
	if err != nil {
		return fmt.Errorf("normalize migrated bead %s: %w", bead.ID, err)
	}
	bead = normalizedBead
	beadType := firstNonEmpty(bead.IssueType, bead.Type, "task")
	createdAt := firstNonEmpty(bead.CreatedAt, bead.UpdatedAt)
	updatedAt := firstNonEmpty(bead.UpdatedAt, bead.CreatedAt)
	if createdAt == "" || updatedAt == "" {
		return fmt.Errorf("bd export bead %s is missing created_at or updated_at", bead.ID)
	}

	if update {
		if _, err := tx.ExecContext(ctx, `
UPDATE beads SET
	title=?, description=?, acceptance_criteria=?, status=?, priority=?, type=?, parent_id=?,
	owner=?, estimated_minutes=?, tier=?, model=?, deferred_until=?, close_reason=?,
	created_at=?, updated_at=?, closed_at=?, deleted=0
WHERE id=?`,
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
			bead.ID,
		); err != nil {
			return fmt.Errorf("update migrated bead %s: %w", bead.ID, err)
		}
		for _, table := range []string{"bead_deps", "bead_tags", "bead_labels", "bead_metadata", "bead_notes"} {
			if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE bead_id=?`, bead.ID); err != nil {
				return fmt.Errorf("clear migrated %s for %s: %w", table, bead.ID, err)
			}
		}
	} else {
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

func runBeadReconcile(ctx context.Context, data []byte, apply bool) (beadReconcileReport, error) {
	rawBeads, err := decodeBDExport(data)
	if err != nil {
		return beadReconcileReport{}, err
	}
	sourceBeads := make(map[string]bdExportBead, len(rawBeads))
	for _, raw := range rawBeads {
		var bead bdExportBead
		if err := json.Unmarshal(raw, &bead); err != nil {
			return beadReconcileReport{}, fmt.Errorf("decode bd export bead: %w", err)
		}
		if strings.TrimSpace(bead.ID) == "" {
			return beadReconcileReport{}, fmt.Errorf("bd export bead is missing id")
		}
		sourceBeads[bead.ID] = bead
	}

	paths, err := ResolveProjectDBPaths()
	if err != nil {
		return beadReconcileReport{}, fmt.Errorf("resolve bead store paths: %w", err)
	}
	db, err := openReconcileStateDB(paths.StateDBPath, apply)
	if err != nil {
		return beadReconcileReport{}, err
	}
	if db == nil {
		return beadReconcileReport{SourceBeads: len(sourceBeads), Inserts: len(sourceBeads)}, nil
	}
	defer db.Close()

	sqliteBeads, err := loadSQLiteMigrationBeads(ctx, db)
	if err != nil {
		return beadReconcileReport{}, err
	}
	report := beadReconcileReport{SourceBeads: len(sourceBeads), SQLiteBeads: len(sqliteBeads)}
	inserts := map[string]bdExportBead{}
	updates := map[string]bdExportBead{}
	deletes := map[string]sqliteMigrationBead{}
	for id, source := range sourceBeads {
		current, ok := sqliteBeads[id]
		if !ok {
			report.Inserts++
			inserts[id] = source
			continue
		}
		if current.Deleted {
			report.Updates++
			updates[id] = source
			continue
		}
		cmp := compareMigrationTimestamps(source.UpdatedAt, current.UpdatedAt)
		switch {
		case cmp > 0:
			report.Updates++
			updates[id] = source
		case cmp == 0 && !migrationBeadsEquivalent(source, current):
			report.Conflicts++
			report.ConflictedIDs = append(report.ConflictedIDs, id)
			updates[id] = source
		}
	}
	for id, current := range sqliteBeads {
		if current.Deleted {
			continue
		}
		if _, ok := sourceBeads[id]; !ok {
			report.Deletes++
			deletes[id] = current
		}
	}
	if !apply {
		return report, nil
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return beadReconcileReport{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := setBeadParentTouchTriggers(ctx, tx, false); err != nil {
		return beadReconcileReport{}, err
	}
	for _, source := range inserts {
		if err := insertMigratedBead(ctx, tx, source); err != nil {
			return beadReconcileReport{}, err
		}
	}
	for _, source := range updates {
		if err := updateMigratedBead(ctx, tx, source); err != nil {
			return beadReconcileReport{}, err
		}
	}
	for id := range deletes {
		if _, err := tx.ExecContext(ctx, `UPDATE beads SET deleted=1 WHERE id=?`, id); err != nil {
			return beadReconcileReport{}, fmt.Errorf("soft-delete bead %s: %w", id, err)
		}
	}
	if err := setBeadParentTouchTriggers(ctx, tx, true); err != nil {
		return beadReconcileReport{}, err
	}
	if err := tx.Commit(); err != nil {
		return beadReconcileReport{}, err
	}
	return report, nil
}

type sqliteMigrationBead struct {
	BDExportBead bdExportBead
	UpdatedAt    string
	Deleted      bool
}

func loadSQLiteMigrationBeads(ctx context.Context, db *sql.DB) (map[string]sqliteMigrationBead, error) {
	rows, err := db.QueryContext(ctx, `
SELECT id, title, description, acceptance_criteria, status, priority, type, parent_id,
       owner, estimated_minutes, tier, model, deferred_until, close_reason,
       created_at, updated_at, closed_at, deleted
FROM beads`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]sqliteMigrationBead{}
	for rows.Next() {
		var bead bdExportBead
		var parentID, owner, tier, model, deferredUntil, closeReason, closedAt sql.NullString
		var estimatedMinutes sql.NullInt64
		var deleted int
		if err := rows.Scan(
			&bead.ID,
			&bead.Title,
			&bead.Description,
			&bead.AcceptanceCriteria,
			&bead.Status,
			&bead.Priority,
			&bead.IssueType,
			&parentID,
			&owner,
			&estimatedMinutes,
			&tier,
			&model,
			&deferredUntil,
			&closeReason,
			&bead.CreatedAt,
			&bead.UpdatedAt,
			&closedAt,
			&deleted,
		); err != nil {
			return nil, err
		}
		bead.ParentID = nullStringValue(parentID)
		bead.Owner = nullStringValue(owner)
		bead.EstimatedMinutes = int(estimatedMinutes.Int64)
		bead.Tier = nullStringValue(tier)
		bead.Model = nullStringValue(model)
		bead.DeferredUntil = nullStringValue(deferredUntil)
		bead.CloseReason = nullStringValue(closeReason)
		bead.ClosedAt = nullStringValue(closedAt)
		out[bead.ID] = sqliteMigrationBead{BDExportBead: bead, UpdatedAt: bead.UpdatedAt, Deleted: deleted != 0}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for id, bead := range out {
		deps, err := loadSQLiteMigrationDeps(ctx, db, id)
		if err != nil {
			return nil, err
		}
		tags, err := loadSQLiteStrings(ctx, db, "bead_tags", "tag", id)
		if err != nil {
			return nil, err
		}
		labels, err := loadSQLiteStrings(ctx, db, "bead_labels", "label", id)
		if err != nil {
			return nil, err
		}
		metadata, err := loadSQLiteMetadata(ctx, db, id)
		if err != nil {
			return nil, err
		}
		notes, err := loadSQLiteStrings(ctx, db, "bead_notes", "content", id)
		if err != nil {
			return nil, err
		}
		bead.BDExportBead.Dependencies = deps
		bead.BDExportBead.Tags = tags
		bead.BDExportBead.Labels = labels
		bead.BDExportBead.Metadata = metadata
		if len(notes) > 0 {
			encoded, err := json.Marshal(notes)
			if err != nil {
				return nil, err
			}
			bead.BDExportBead.Notes = encoded
		}
		out[id] = bead
	}
	return out, nil
}

func compareMigrationTimestamps(left, right string) int {
	return migrationTimestampSecond(left).Compare(migrationTimestampSecond(right))
}

func parseMigrationTimestamp(value string) time.Time {
	if t, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return t
	}
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return t
	}
	return time.Time{}
}

func migrationTimestampSecond(value string) time.Time {
	return parseMigrationTimestamp(value).UTC().Truncate(time.Second)
}

func openReconcileStateDB(path string, apply bool) (*sql.DB, error) {
	if apply {
		return openStateDB(path)
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("stat sqlite state db: %w", err)
	}
	dbURL := url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", dbURL.String())
	if err != nil {
		return nil, fmt.Errorf("open sqlite state db read-only: %w", err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite state db read-only: %w", err)
	}
	return db, nil
}

func migrationBeadsEquivalent(source bdExportBead, current sqliteMigrationBead) bool {
	normalizedSource := normalizeMigrationBeadForCompare(source)
	normalizedCurrent := normalizeMigrationBeadForCompare(current.BDExportBead)
	return normalizedSource == normalizedCurrent
}

func normalizeMigrationBeadForCompare(bead bdExportBead) string {
	normalizedBead, err := normalizeBDExportBeadForMigration(bead)
	if err == nil {
		bead = normalizedBead
	}

	type comparableBead struct {
		ID                 string
		Title              string
		Description        string
		AcceptanceCriteria string
		Status             string
		Priority           int
		Type               string
		ParentID           string
		Owner              string
		EstimatedMinutes   int
		Tier               string
		Model              string
		DeferredUntil      string
		CloseReason        string
		CreatedAt          string
		UpdatedAt          string
		ClosedAt           string
		Dependencies       []string
		Tags               []string
		Labels             []string
		Metadata           map[string]string
		Notes              []string
	}
	deps := make([]string, 0, len(bead.Dependencies))
	for _, dep := range bead.Dependencies {
		if strings.TrimSpace(dep.DependsOnID) == "" {
			continue
		}
		deps = append(deps, dep.DependsOnID+"\x00"+firstNonEmpty(dep.Type, "blocks"))
	}
	metadata := map[string]string{}
	for key, value := range bead.Metadata {
		if strings.TrimSpace(key) == "" {
			continue
		}
		encoded, err := migrationMetadataValue(value)
		if err == nil {
			metadata[key] = encoded
		}
	}
	notes, _ := migrationNotes(bead.Notes)
	normalized := comparableBead{
		ID:                 bead.ID,
		Title:              bead.Title,
		Description:        bead.Description,
		AcceptanceCriteria: bead.AcceptanceCriteria,
		Status:             normalizeMigrationInsertStatus(bead.Status),
		Priority:           bead.Priority,
		Type:               firstNonEmpty(bead.IssueType, bead.Type, "task"),
		ParentID:           firstNonEmpty(bead.ParentID, bead.Parent),
		Owner:              firstNonEmpty(bead.Owner, bead.Assignee),
		EstimatedMinutes:   bead.EstimatedMinutes,
		Tier:               bead.Tier,
		Model:              bead.Model,
		DeferredUntil:      firstNonEmpty(bead.DeferredUntil, bead.DeferUntil),
		CloseReason:        bead.CloseReason,
		CreatedAt:          firstNonEmpty(bead.CreatedAt, bead.UpdatedAt),
		UpdatedAt:          migrationTimestampSecond(firstNonEmpty(bead.UpdatedAt, bead.CreatedAt)).Format(time.RFC3339),
		ClosedAt:           bead.ClosedAt,
		Dependencies:       sortedCopy(deps),
		Tags:               sortedNonEmptyCopy(bead.Tags),
		Labels:             sortedNonEmptyCopy(bead.Labels),
		Metadata:           metadata,
		Notes:              sortedNonEmptyCopy(notes),
	}
	encoded, _ := json.Marshal(normalized)
	return string(encoded)
}

func normalizeBDExportBeadForMigration(bead bdExportBead) (bdExportBead, error) {
	extractedAC, description, err := beadstore.ExtractAndStripAC(bead.Description)
	if err != nil {
		return bead, err
	}
	bead.Description = description
	if strings.TrimSpace(bead.AcceptanceCriteria) == "" {
		bead.AcceptanceCriteria = extractedAC
	}
	return bead, nil
}

func loadSQLiteMigrationDeps(ctx context.Context, db *sql.DB, id string) ([]protocol.Dependency, error) {
	rows, err := db.QueryContext(ctx, `SELECT depends_on_id, type FROM bead_deps WHERE bead_id=? ORDER BY depends_on_id, type`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var deps []protocol.Dependency
	for rows.Next() {
		var dep protocol.Dependency
		if err := rows.Scan(&dep.DependsOnID, &dep.Type); err != nil {
			return nil, err
		}
		deps = append(deps, dep)
	}
	return deps, rows.Err()
}

func loadSQLiteStrings(ctx context.Context, db *sql.DB, table, column, id string) ([]string, error) {
	rows, err := db.QueryContext(ctx, fmt.Sprintf(`SELECT %s FROM %s WHERE bead_id=? ORDER BY %s`, column, table, column), id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func loadSQLiteMetadata(ctx context.Context, db *sql.DB, id string) (map[string]any, error) {
	rows, err := db.QueryContext(ctx, `SELECT key, value FROM bead_metadata WHERE bead_id=? ORDER BY key`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	metadata := map[string]any{}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		metadata[key] = value
	}
	return metadata, rows.Err()
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

func nullStringValue(value sql.NullString) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

func sortedCopy(values []string) []string {
	out := append([]string(nil), values...)
	sort.Strings(out)
	return out
}

func sortedNonEmptyCopy(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
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

func writeBeadReconcileReport(w io.Writer, source beadMigrationSource, report beadReconcileReport, apply bool) {
	fmt.Fprintln(w, "Reconcile plan")
	if source.path != "" {
		fmt.Fprintf(w, "source: %s (%s)\n", source.kind, source.path)
	} else {
		fmt.Fprintf(w, "source: %s\n", source.kind)
	}
	fmt.Fprintf(w, "bd beads: %d\n", report.SourceBeads)
	fmt.Fprintf(w, "sqlite beads: %d\n", report.SQLiteBeads)
	fmt.Fprintf(w, "inserts: %d\n", report.Inserts)
	fmt.Fprintf(w, "updates: %d\n", report.Updates)
	fmt.Fprintf(w, "deletes: %d\n", report.Deletes)
	fmt.Fprintf(w, "conflicts: %d\n", report.Conflicts)
	for _, id := range sortedCopy(report.ConflictedIDs) {
		fmt.Fprintf(w, "conflict: %s\n", id)
	}
	if apply {
		fmt.Fprintln(w, "APPLIED")
	} else {
		fmt.Fprintln(w, "DRY RUN -- pass --apply to write changes")
	}
}
