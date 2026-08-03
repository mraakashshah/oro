package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"oro/pkg/dispatcher"
)

type epicBranchInspector interface {
	InspectEpicBranch(ctx context.Context, branch, targetBranch string) error
}

type epicBranchAdmissionCLIRecord struct {
	Branch         string `json:"branch"`
	EpicID         string `json:"epic_id"`
	TargetBranch   string `json:"target_branch"`
	State          string `json:"state"`
	Generation     int64  `json:"generation"`
	LeaseOwner     string `json:"lease_owner"`
	LeaseExpiresAt string `json:"lease_expires_at"`
	BlockerKind    string `json:"blocker_kind"`
	CheckoutPath   string `json:"checkout_path"`
	BranchSHA      string `json:"branch_sha"`
	TargetSHA      string `json:"target_sha"`
	RecoveryBeadID string `json:"recovery_bead_id"`
	Details        string `json:"details"`
	ResolvedAt     string `json:"resolved_at"`
}

func newEpicBranchCmd() *cobra.Command {
	inspector := dispatcher.NewGitWorktreeManager(currentRepoRoot(), "", "", &dispatcher.ExecCommandRunner{})
	return newEpicBranchCmdWithInspector(inspector)
}

func newEpicBranchCmdWithInspector(inspector epicBranchInspector) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "epic-branch",
		Short: "Inspect and resolve durable epic-branch blockers",
	}
	cmd.AddCommand(newEpicBranchListCmd(), newEpicBranchResolveCmd(inspector))
	return cmd
}

func newEpicBranchListCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List blocked epic-branch admissions",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			db, err := openRecoveryStateDB()
			if err != nil {
				return err
			}
			defer db.Close()

			records, err := listEpicBranchAdmissions(cmd.Context(), db)
			if err != nil {
				return err
			}
			if jsonOut {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(records)
			}
			writeEpicBranchAdmissionList(cmd.OutOrStdout(), records)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit blocked admissions as JSON")
	return cmd
}

func listEpicBranchAdmissions(ctx context.Context, db *sql.DB) ([]epicBranchAdmissionCLIRecord, error) {
	rows, err := db.QueryContext(ctx, `
SELECT branch, epic_id, target_branch, state, generation,
       COALESCE(lease_owner, ''), COALESCE(lease_expires_at, ''),
       COALESCE(blocker_kind, ''), COALESCE(checkout_path, ''),
       branch_sha, target_sha, COALESCE(recovery_bead_id, ''), details,
       COALESCE(resolved_at, '')
FROM epic_branch_admissions
WHERE state = 'blocked'
ORDER BY branch`)
	if err != nil {
		return nil, fmt.Errorf("list epic-branch admissions: %w", err)
	}
	defer rows.Close()

	records := make([]epicBranchAdmissionCLIRecord, 0)
	for rows.Next() {
		var record epicBranchAdmissionCLIRecord
		if err := rows.Scan(
			&record.Branch,
			&record.EpicID,
			&record.TargetBranch,
			&record.State,
			&record.Generation,
			&record.LeaseOwner,
			&record.LeaseExpiresAt,
			&record.BlockerKind,
			&record.CheckoutPath,
			&record.BranchSHA,
			&record.TargetSHA,
			&record.RecoveryBeadID,
			&record.Details,
			&record.ResolvedAt,
		); err != nil {
			return nil, fmt.Errorf("scan epic-branch admission: %w", err)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate epic-branch admissions: %w", err)
	}
	return records, nil
}

func writeEpicBranchAdmissionList(w io.Writer, records []epicBranchAdmissionCLIRecord) {
	for _, record := range records {
		fmt.Fprintf(w, "%s\t%s\tgeneration=%d\t%s\n", record.Branch, record.BlockerKind, record.Generation, record.Details)
	}
}

func newEpicBranchResolveCmd(inspector epicBranchInspector) *cobra.Command {
	var generation int64
	cmd := &cobra.Command{
		Use:   "resolve <branch>",
		Short: "Resolve one epic-branch blocker after fresh safety inspection",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !cmd.Flags().Changed("generation") || generation <= 0 {
				return errors.New("--generation must be provided and greater than zero")
			}
			db, err := openRecoveryStateDB()
			if err != nil {
				return err
			}
			defer db.Close()

			persistedGeneration, err := resolveEpicBranchAdmission(cmd.Context(), db, inspector, args[0], generation)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "resolved epic branch %s generation %d\n", args[0], persistedGeneration)
			return nil
		},
	}
	cmd.Flags().Int64Var(&generation, "generation", 0, "required blocker generation from epic-branch list")
	return cmd
}

func resolveEpicBranchAdmission(
	ctx context.Context,
	db *sql.DB,
	inspector epicBranchInspector,
	branch string,
	generation int64,
) (int64, error) {
	if !strings.HasPrefix(branch, "epic/") || len(strings.TrimPrefix(branch, "epic/")) == 0 {
		return 0, fmt.Errorf("invalid epic branch %q", branch)
	}
	if generation <= 0 {
		return 0, errors.New("generation must be greater than zero")
	}
	if inspector == nil {
		return 0, errors.New("epic-branch inspector is required")
	}

	var targetBranch, state string
	var persistedGeneration int64
	err := db.QueryRowContext(ctx, `
SELECT target_branch, state, generation
FROM epic_branch_admissions
WHERE branch = ?`, branch).Scan(&targetBranch, &state, &persistedGeneration)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("epic branch %q has no durable admission", branch)
	}
	if err != nil {
		return 0, fmt.Errorf("read epic-branch admission %q: %w", branch, err)
	}
	if state != "blocked" {
		return 0, fmt.Errorf("epic branch %q is %s, not blocked", branch, state)
	}
	if persistedGeneration != generation {
		return 0, fmt.Errorf("stale generation for epic branch %q: got %d, current %d", branch, generation, persistedGeneration)
	}
	if err := inspector.InspectEpicBranch(ctx, branch, targetBranch); err != nil {
		return 0, fmt.Errorf("inspect epic branch %q: %w", branch, err)
	}

	resolvedAt := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := db.ExecContext(ctx, `
UPDATE epic_branch_admissions
SET state = 'resolved',
    lease_token = NULL,
    lease_owner = NULL,
    lease_expires_at = NULL,
    updated_at = ?,
    resolved_at = ?
WHERE branch = ? AND state = 'blocked' AND generation = ?`,
		resolvedAt, resolvedAt, branch, generation)
	if err != nil {
		return 0, fmt.Errorf("resolve epic-branch admission %q: %w", branch, err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("read resolved epic-branch row count %q: %w", branch, err)
	}
	if rowsAffected != 1 {
		return 0, fmt.Errorf("epic branch %q changed during inspection; blocker was not resolved", branch)
	}

	var resolvedState string
	if err := db.QueryRowContext(ctx, `
SELECT state, generation
FROM epic_branch_admissions
WHERE branch = ?`, branch).Scan(&resolvedState, &persistedGeneration); err != nil {
		return 0, fmt.Errorf("read resolved epic-branch admission %q: %w", branch, err)
	}
	if resolvedState != "resolved" {
		return 0, fmt.Errorf("epic branch %q persisted unexpected state %q", branch, resolvedState)
	}
	return persistedGeneration, nil
}
