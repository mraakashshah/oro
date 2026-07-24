package main

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"oro/pkg/dispatcher"
)

type recoveryQuarantineCLIRecord struct {
	ID           int64  `json:"id"`
	BeadID       string `json:"bead_id"`
	AssignmentID int64  `json:"assignment_id,omitempty"`
	WorkerID     string `json:"worker_id,omitempty"`
	Worktree     string `json:"worktree,omitempty"`
	Branch       string `json:"branch,omitempty"`
	PreservedRef string `json:"preserved_ref,omitempty"`
	Reason       string `json:"reason"`
	Details      string `json:"details"`
	Status       string `json:"status"`
	CreatedAt    string `json:"created_at"`
}

type recoveryAssignmentInspection struct {
	ID           int64  `json:"id"`
	BeadID       string `json:"bead_id"`
	WorkerID     string `json:"worker_id"`
	Worktree     string `json:"worktree"`
	Status       string `json:"status"`
	AssignedAt   string `json:"assigned_at"`
	CompletedAt  string `json:"completed_at,omitempty"`
	AttemptCount int    `json:"attempt_count"`
	HandoffCount int    `json:"handoff_count"`
}

type recoveryBeadInspection struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
	Type   string `json:"type"`
}

type recoveryBranchInspection struct {
	Name   string `json:"name,omitempty"`
	Exists bool   `json:"exists"`
	Head   string `json:"head,omitempty"`
	Ahead  int    `json:"ahead,omitempty"`
	Behind int    `json:"behind,omitempty"`
	Error  string `json:"error,omitempty"`
}

type recoveryWorktreeInspection struct {
	Path             string `json:"path,omitempty"`
	Exists           bool   `json:"exists"`
	CheckedOutBranch string `json:"checked_out_branch,omitempty"`
	Head             string `json:"head,omitempty"`
	Error            string `json:"error,omitempty"`
}

type recoveryDirtyInspection struct {
	Total     int      `json:"total"`
	Staged    int      `json:"staged"`
	Modified  int      `json:"modified"`
	Deleted   int      `json:"deleted"`
	Untracked int      `json:"untracked"`
	Sample    []string `json:"sample,omitempty"`
	Error     string   `json:"error,omitempty"`
}

type recoveryInspection struct {
	Quarantine        recoveryQuarantineCLIRecord   `json:"quarantine"`
	Assignment        *recoveryAssignmentInspection `json:"assignment,omitempty"`
	Bead              *recoveryBeadInspection       `json:"bead,omitempty"`
	Branch            recoveryBranchInspection      `json:"branch"`
	Worktree          recoveryWorktreeInspection    `json:"worktree"`
	Dirty             recoveryDirtyInspection       `json:"dirty"`
	RecommendedAction string                        `json:"recommended_action"`
}

func newRecoveryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "recovery",
		Short: "Inspect and resolve recovery quarantines",
	}
	cmd.AddCommand(newRecoveryListCmd(), newRecoveryInspectCmd(), newRecoveryResolveCmd(), newRecoveryAbandonStaleCmd())
	return cmd
}

func newRecoveryListCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List open recovery quarantines",
		RunE: func(cmd *cobra.Command, args []string) error {
			db, err := openRecoveryStateDB()
			if err != nil {
				return err
			}
			defer db.Close()
			records, err := listRecoveryQuarantines(cmd.Context(), db)
			if err != nil {
				return err
			}
			if jsonOut {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(records)
			}
			writeRecoveryQuarantineList(cmd.OutOrStdout(), records)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit open quarantines as JSON")
	return cmd
}

func newRecoveryInspectCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "inspect <id>",
		Short: "Inspect preserved work for a recovery quarantine",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil || id <= 0 {
				return fmt.Errorf("invalid recovery quarantine id %q", args[0])
			}
			db, err := openRecoveryStateDB()
			if err != nil {
				return err
			}
			defer db.Close()
			inspection, err := inspectRecoveryQuarantine(cmd.Context(), db, id)
			if err != nil {
				return err
			}
			if jsonOut {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(inspection)
			}
			writeRecoveryInspection(cmd.OutOrStdout(), inspection)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit inspection as JSON")
	return cmd
}

func newRecoveryResolveCmd() *cobra.Command {
	var mode string
	var preservedRef string
	var all bool
	var force bool
	cmd := &cobra.Command{
		Use:   "resolve [<id>]",
		Short: "Mark a recovery quarantine resolved (or all open quarantines with --all)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if all {
				return runRecoveryResolveAllCmd(cmd, args, mode, force)
			}
			return runRecoveryResolveSingleCmd(cmd, args, mode, preservedRef)
		},
	}
	cmd.Flags().StringVar(&mode, "mode", "", "resolution mode: requeue-preserved, retry-fresh-preserved, resolved-after-merge, human-owned, discard-empty-safe")
	cmd.Flags().StringVar(&preservedRef, "preserved-ref", "", "verified recovery/* ref retained when using retry-fresh-preserved")
	cmd.Flags().BoolVar(&all, "all", false, "resolve every open quarantine with --mode (bulk); requires confirmation")
	cmd.Flags().BoolVar(&force, "force", false, "skip interactive confirmation for --all (requires ORO_HUMAN_CONFIRMED=1)")
	return cmd
}

// runRecoveryResolveSingleCmd resolves exactly one quarantine by positional id,
// preserving the original single-id behavior (including the empty-mode
// compatibility path and message).
func runRecoveryResolveSingleCmd(cmd *cobra.Command, args []string, mode, preservedRef string) error {
	if len(args) != 1 {
		return fmt.Errorf("recovery resolve requires exactly one <id> (or use --all)")
	}
	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil || id <= 0 {
		return fmt.Errorf("invalid recovery quarantine id %q", args[0])
	}
	db, err := openRecoveryStateDB()
	if err != nil {
		return err
	}
	defer db.Close()
	if err := resolveRecoveryQuarantineWithPreservedRef(cmd.Context(), db, id, mode, preservedRef); err != nil {
		return err
	}
	if mode == "" {
		fmt.Fprintf(cmd.OutOrStdout(), "resolved recovery quarantine %d (compatibility mode; prefer --mode requeue-preserved, resolved-after-merge, human-owned, or discard-empty-safe)\n", id)
	} else {
		fmt.Fprintf(cmd.OutOrStdout(), "resolved recovery quarantine %d with mode %s\n", id, mode)
	}
	return nil
}

// runRecoveryResolveAllCmd wires the guarded bulk resolve path: it rejects a
// stray positional id, opens the store, and delegates to runRecoveryResolveAll
// with an injected config so the run function stays testable without a TTY.
func runRecoveryResolveAllCmd(cmd *cobra.Command, args []string, mode string, force bool) error {
	if len(args) != 0 {
		return fmt.Errorf("recovery resolve --all does not take a positional <id>; it resolves every open quarantine")
	}
	db, err := openRecoveryStateDB()
	if err != nil {
		return err
	}
	defer db.Close()
	cfg := recoveryResolveAllConfig{
		db:    db,
		mode:  mode,
		force: force,
		w:     cmd.OutOrStdout(),
		stdin: os.Stdin,
		isTTY: isStdinTTY,
	}
	return runRecoveryResolveAll(cmd.Context(), cfg)
}

// recoveryResolveAllConfig holds injectable dependencies for the bulk resolve
// flow, mirroring recoveryAbandonConfig so the confirmation guard is testable
// without a real TTY.
type recoveryResolveAllConfig struct {
	db    *sql.DB
	mode  string
	force bool
	w     io.Writer
	stdin io.Reader
	isTTY func() bool
}

// runRecoveryResolveAll resolves every open quarantine using the given mode.
// It requires an explicit mode (bulk resolve must be intentional), is guarded
// by the same confirmation pattern as abandon-stale, and treats a per-row
// resolve failure (e.g. discard-empty-safe refusing a non-empty row) as a SKIP
// rather than a fatal abort.
func runRecoveryResolveAll(ctx context.Context, cfg recoveryResolveAllConfig) error {
	mode := strings.TrimSpace(cfg.mode)
	if mode == "" {
		return fmt.Errorf("--all requires an explicit --mode (requeue-preserved, resolved-after-merge, human-owned, or discard-empty-safe)")
	}
	if err := confirmRecoveryResolveAll(cfg); err != nil {
		return err
	}

	records, err := listRecoveryQuarantines(ctx, cfg.db)
	if err != nil {
		return err
	}

	resolved, skipped := 0, 0
	for _, r := range records {
		if err := resolveRecoveryQuarantine(ctx, cfg.db, r.ID, mode); err != nil {
			skipped++
			fmt.Fprintf(cfg.w, "  skipped #%d %s: %s\n", r.ID, r.BeadID, err)
			continue
		}
		resolved++
	}
	fmt.Fprintf(cfg.w, "resolved %d, skipped %d of %d open quarantines (mode: %s)\n",
		resolved, skipped, len(records), mode)
	return nil
}

// confirmRecoveryResolveAll mirrors confirmRecoveryAbandon: --force requires
// ORO_HUMAN_CONFIRMED=1, otherwise an interactive TTY must type YES.
func confirmRecoveryResolveAll(cfg recoveryResolveAllConfig) error {
	if cfg.force {
		if os.Getenv("ORO_HUMAN_CONFIRMED") != "1" {
			return fmt.Errorf("--force requires ORO_HUMAN_CONFIRMED=1 environment variable")
		}
		return nil
	}
	if cfg.isTTY != nil && !cfg.isTTY() {
		return fmt.Errorf("oro recovery resolve --all requires an interactive terminal (stdin is not a TTY)\n" +
			"Hint: use --force with ORO_HUMAN_CONFIRMED=1 for non-interactive use")
	}
	fmt.Fprint(cfg.w, "Type YES to resolve all open quarantines: ")
	scanner := bufio.NewScanner(cfg.stdin)
	if !scanner.Scan() {
		return fmt.Errorf("failed to read confirmation from stdin")
	}
	if strings.TrimSpace(scanner.Text()) != "YES" {
		return fmt.Errorf("aborted (expected YES)")
	}
	return nil
}

func openRecoveryStateDB() (*sql.DB, error) {
	paths, err := ResolveDaemonPaths()
	if err != nil {
		return nil, fmt.Errorf("resolve paths: %w", err)
	}
	db, err := openStateDB(paths.StateDBPath)
	if err != nil {
		return nil, fmt.Errorf("open state db: %w", err)
	}
	return db, nil
}

func listRecoveryQuarantines(ctx context.Context, db *sql.DB) ([]recoveryQuarantineCLIRecord, error) {
	rows, err := db.QueryContext(ctx, `
SELECT id, bead_id, COALESCE(assignment_id, 0), COALESCE(worker_id, ''), COALESCE(worktree, ''),
       COALESCE(branch, ''), COALESCE(preserved_ref, ''), reason, details, status, created_at
FROM recovery_quarantines
WHERE status IN ('open', 'human_owned')
ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list recovery quarantines: %w", err)
	}
	defer rows.Close()

	var records []recoveryQuarantineCLIRecord
	for rows.Next() {
		var r recoveryQuarantineCLIRecord
		if err := rows.Scan(&r.ID, &r.BeadID, &r.AssignmentID, &r.WorkerID, &r.Worktree, &r.Branch, &r.PreservedRef, &r.Reason, &r.Details, &r.Status, &r.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan recovery quarantine: %w", err)
		}
		records = append(records, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate recovery quarantines: %w", err)
	}
	return records, nil
}

func inspectRecoveryQuarantine(ctx context.Context, db *sql.DB, id int64) (recoveryInspection, error) {
	q, err := getRecoveryQuarantine(ctx, db, id)
	if err != nil {
		return recoveryInspection{}, err
	}
	assignment, err := getRecoveryAssignment(ctx, db, q)
	if err != nil {
		return recoveryInspection{}, err
	}
	bead, err := getRecoveryBead(ctx, db, q.BeadID)
	if err != nil {
		return recoveryInspection{}, err
	}
	worktree := inspectRecoveryWorktree(ctx, q.Worktree)
	gitDir := "."
	if worktree.Exists {
		gitDir = q.Worktree
	}
	branch := inspectRecoveryBranch(ctx, gitDir, q.Branch)
	dirty := inspectRecoveryDirty(ctx, q.Worktree, worktree.Exists)
	inspection := recoveryInspection{
		Quarantine: q,
		Assignment: assignment,
		Bead:       bead,
		Branch:     branch,
		Worktree:   worktree,
		Dirty:      dirty,
	}
	inspection.RecommendedAction = recommendedRecoveryAction(inspection)
	return inspection, nil
}

type recoveryQueryRower interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func getRecoveryQuarantine(ctx context.Context, db *sql.DB, id int64) (recoveryQuarantineCLIRecord, error) {
	return queryRecoveryQuarantine(ctx, db, id)
}

func queryRecoveryQuarantine(ctx context.Context, db recoveryQueryRower, id int64) (recoveryQuarantineCLIRecord, error) {
	var r recoveryQuarantineCLIRecord
	err := db.QueryRowContext(ctx, `
SELECT id, bead_id, COALESCE(assignment_id, 0), COALESCE(worker_id, ''), COALESCE(worktree, ''),
       COALESCE(branch, ''), COALESCE(preserved_ref, ''), reason, details, status, created_at
FROM recovery_quarantines
WHERE id=?`, id).Scan(&r.ID, &r.BeadID, &r.AssignmentID, &r.WorkerID, &r.Worktree, &r.Branch, &r.PreservedRef, &r.Reason, &r.Details, &r.Status, &r.CreatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return r, fmt.Errorf("recovery quarantine %d not found", id)
		}
		return r, fmt.Errorf("lookup recovery quarantine: %w", err)
	}
	return r, nil
}

func getRecoveryAssignment(ctx context.Context, db *sql.DB, q recoveryQuarantineCLIRecord) (*recoveryAssignmentInspection, error) {
	query := `
SELECT id, bead_id, worker_id, worktree, status, COALESCE(assigned_at, ''), COALESCE(completed_at, ''),
       COALESCE(attempt_count, 0), COALESCE(handoff_count, 0)
FROM assignments
WHERE id=?`
	args := []any{q.AssignmentID}
	if q.AssignmentID <= 0 {
		query = `
SELECT id, bead_id, worker_id, worktree, status, COALESCE(assigned_at, ''), COALESCE(completed_at, ''),
       COALESCE(attempt_count, 0), COALESCE(handoff_count, 0)
FROM assignments
WHERE bead_id=?
ORDER BY id DESC
LIMIT 1`
		args = []any{q.BeadID}
	}
	var a recoveryAssignmentInspection
	err := db.QueryRowContext(ctx, query, args...).Scan(&a.ID, &a.BeadID, &a.WorkerID, &a.Worktree, &a.Status, &a.AssignedAt, &a.CompletedAt, &a.AttemptCount, &a.HandoffCount)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("lookup recovery assignment: %w", err)
	}
	return &a, nil
}

func getRecoveryBead(ctx context.Context, db *sql.DB, beadID string) (*recoveryBeadInspection, error) {
	if beadID == "" {
		return nil, nil
	}
	var bead recoveryBeadInspection
	err := db.QueryRowContext(ctx,
		`SELECT id, title, status, type FROM beads WHERE id=?`,
		beadID).Scan(&bead.ID, &bead.Title, &bead.Status, &bead.Type)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("lookup recovery bead: %w", err)
	}
	return &bead, nil
}

func inspectRecoveryWorktree(ctx context.Context, worktree string) recoveryWorktreeInspection {
	result := recoveryWorktreeInspection{Path: worktree}
	if worktree == "" {
		return result
	}
	info, err := os.Stat(worktree)
	if err != nil {
		if !os.IsNotExist(err) {
			result.Error = err.Error()
		}
		return result
	}
	if !info.IsDir() {
		result.Error = "path is not a directory"
		return result
	}
	result.Exists = true
	if branch, err := runGitOutput(ctx, worktree, "rev-parse", "--abbrev-ref", "HEAD"); err == nil {
		result.CheckedOutBranch = strings.TrimSpace(branch)
	} else {
		result.Error = strings.TrimSpace(err.Error())
	}
	if head, err := runGitOutput(ctx, worktree, "rev-parse", "HEAD"); err == nil {
		result.Head = strings.TrimSpace(head)
	}
	return result
}

func inspectRecoveryBranch(ctx context.Context, gitDir, branch string) recoveryBranchInspection {
	result := recoveryBranchInspection{Name: branch}
	if branch == "" {
		return result
	}
	head, err := runGitOutput(ctx, gitDir, "rev-parse", "--verify", branch+"^{commit}")
	if err != nil {
		result.Error = strings.TrimSpace(err.Error())
		return result
	}
	result.Exists = true
	result.Head = strings.TrimSpace(head)
	if target := recoveryCompareTarget(ctx, gitDir); target != "" {
		counts, err := runGitOutput(ctx, gitDir, "rev-list", "--left-right", "--count", target+"..."+branch)
		if err == nil {
			fields := strings.Fields(counts)
			if len(fields) == 2 {
				result.Behind, _ = strconv.Atoi(fields[0])
				result.Ahead, _ = strconv.Atoi(fields[1])
			}
		}
	}
	return result
}

func recoveryCompareTarget(ctx context.Context, gitDir string) string {
	for _, candidate := range []string{"origin/main", "main"} {
		if _, err := runGitOutput(ctx, gitDir, "rev-parse", "--verify", candidate+"^{commit}"); err == nil {
			return candidate
		}
	}
	return ""
}

func inspectRecoveryDirty(ctx context.Context, worktree string, exists bool) recoveryDirtyInspection {
	var result recoveryDirtyInspection
	if worktree == "" || !exists {
		return result
	}
	status, err := runGitOutput(ctx, worktree, "status", "--short")
	if err != nil {
		result.Error = strings.TrimSpace(err.Error())
		return result
	}
	for _, line := range strings.Split(strings.TrimRight(status, "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		result.Total++
		if len(result.Sample) < 10 {
			result.Sample = append(result.Sample, line)
		}
		if strings.HasPrefix(line, "??") {
			result.Untracked++
			continue
		}
		if len(line) >= 1 && line[0] != ' ' {
			result.Staged++
		}
		if len(line) >= 2 {
			switch line[1] {
			case 'M', 'A', 'R', 'C', 'U':
				result.Modified++
			case 'D':
				result.Deleted++
			}
		}
	}
	return result
}

func runGitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	if dir == "" {
		dir = "."
	}
	cmd := exec.CommandContext(ctx, "git", args...) //nolint:gosec // inspector chooses fixed git subcommands and does not invoke a shell.
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git -C %s %s: %w: %s", dir, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func recommendedRecoveryAction(inspection recoveryInspection) string {
	q := inspection.Quarantine
	if q.Status == "resolved" {
		return "quarantine is already resolved; no monitor action is required"
	}
	if inspection.Dirty.Total > 0 {
		return "inspect and preserve dirty worktree changes; resolve with --mode requeue-preserved to retry, or --mode human-owned after taking ownership"
	}
	if inspection.Worktree.Exists && inspection.Branch.Exists && q.Reason == "stale_active_assignment" {
		return "worktree and branch are present; resolve with --mode requeue-preserved to make the preserved attempt retryable"
	}
	if !inspection.Worktree.Exists && inspection.Branch.Exists {
		return "worktree is missing but branch exists; preserve or merge the branch, then resolve with --mode human-owned or --mode resolved-after-merge"
	}
	if inspection.Worktree.Exists && !inspection.Branch.Exists {
		return "worktree exists but branch is missing; inspect the worktree HEAD before resolving as human-owned"
	}
	if !inspection.Worktree.Exists && !inspection.Branch.Exists {
		return "branch and worktree are absent; resolve with --mode discard-empty-safe only after confirming there is no remaining work"
	}
	return "inspect preserved branch/worktree and resolve with an explicit --mode"
}

func resolveRecoveryQuarantine(ctx context.Context, db *sql.DB, id int64, mode string) error {
	return resolveRecoveryQuarantineWithPreservedRef(ctx, db, id, mode, "")
}

func resolveRecoveryQuarantineWithPreservedRef(ctx context.Context, db *sql.DB, id int64, mode, preservedRef string) error {
	mode = strings.TrimSpace(mode)
	switch mode {
	case "":
		return markRecoveryQuarantineResolved(ctx, db, id)
	case "resolved-after-merge":
		return markRecoveryQuarantineResolved(ctx, db, id)
	case "human-owned":
		return markRecoveryQuarantineHumanOwned(ctx, db, id)
	case "requeue-preserved":
		return resolveRecoveryQuarantineRequeuePreserved(ctx, db, id)
	case "retry-fresh-preserved":
		return resolveRecoveryQuarantineRetryFreshPreserved(ctx, db, id, preservedRef)
	case "discard-empty-safe":
		inspection, err := inspectRecoveryQuarantine(ctx, db, id)
		if err != nil {
			return err
		}
		if !discardEmptySafe(inspection) {
			return fmt.Errorf("discard-empty-safe refused: branch/worktree still contains inspectable work")
		}
		return markRecoveryQuarantineResolved(ctx, db, id)
	default:
		return fmt.Errorf("unknown recovery resolve mode %q", mode)
	}
}

func resolveRecoveryQuarantineRetryFreshPreserved(ctx context.Context, db *sql.DB, id int64, preservedRef string) error {
	preservedRef, err := verifyRecoveryPreservedRef(ctx, preservedRef)
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin retry-fresh-preserved transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	q, err := queryRecoveryQuarantine(ctx, tx, id)
	if err != nil {
		return err
	}
	if q.Status != "open" && q.Status != "human_owned" && q.Status != "resolved" {
		return fmt.Errorf("retry-fresh-preserved recovery quarantine %d has status %q", id, q.Status)
	}
	if err := completeRetryFreshPreservedAssignment(ctx, tx, q); err != nil {
		return err
	}
	if err := reopenRecoveryBeadForRequeue(ctx, tx, q.BeadID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE recovery_quarantines
SET status='resolved', resolved_at=COALESCE(resolved_at, datetime('now')), preserved_ref=?
WHERE id=?`, preservedRef, id); err != nil {
		return fmt.Errorf("record retry-fresh-preserved recovery quarantine: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit retry-fresh-preserved transaction: %w", err)
	}
	return nil
}

func verifyRecoveryPreservedRef(ctx context.Context, preservedRef string) (string, error) {
	preservedRef = strings.TrimSpace(preservedRef)
	if !strings.HasPrefix(preservedRef, "recovery/") || strings.ContainsAny(preservedRef, " \t\n\\") {
		return "", fmt.Errorf("retry-fresh-preserved requires a recovery/* ref")
	}
	if err := exec.CommandContext(ctx, "git", "show-ref", "--verify", "--quiet", "refs/heads/"+preservedRef).Run(); err != nil { //nolint:gosec // validated recovery ref is passed as one argv value beneath refs/heads, without a shell.
		return "", fmt.Errorf("verify retry-fresh-preserved ref %q: %w", preservedRef, err)
	}
	if err := exec.CommandContext(ctx, "git", "rev-parse", "--verify", "--quiet", preservedRef+"^{commit}").Run(); err != nil { //nolint:gosec // validated recovery ref is passed as one argv value, without a shell.
		return "", fmt.Errorf("verify retry-fresh-preserved commit %q: %w", preservedRef, err)
	}
	return preservedRef, nil
}

func completeRetryFreshPreservedAssignment(ctx context.Context, tx *sql.Tx, q recoveryQuarantineCLIRecord) error {
	if q.AssignmentID <= 0 {
		return fmt.Errorf("retry-fresh-preserved recovery quarantine %d has no linked assignment", q.ID)
	}
	res, err := tx.ExecContext(ctx, `
UPDATE assignments
SET status='completed', completed_at=COALESCE(completed_at, datetime('now'))
WHERE id=? AND bead_id=? AND status IN ('quarantined', 'requeued')`, q.AssignmentID, q.BeadID)
	if err != nil {
		return fmt.Errorf("complete retry-fresh-preserved assignment: %w", err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("complete retry-fresh-preserved assignment rows affected: %w", err)
	}
	if rows != 1 {
		return fmt.Errorf("retry-fresh-preserved assignment_id %d is not a linked quarantined or requeued assignment for bead %s", q.AssignmentID, q.BeadID)
	}
	return nil
}

func resolveRecoveryQuarantineRequeuePreserved(ctx context.Context, db *sql.DB, id int64) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin recovery resolve transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	q, err := queryRecoveryQuarantine(ctx, tx, id)
	if err != nil {
		return err
	}
	assignmentID, needsRequeue, err := recoveryAssignmentIDForRequeue(ctx, tx, id, q)
	if err != nil {
		return err
	}
	hasNewerActiveAssignment, err := recoveryBeadHasNewerActiveAssignment(ctx, tx, q.BeadID, assignmentID)
	if err != nil {
		return err
	}
	if !hasNewerActiveAssignment {
		if err := reopenRecoveryBeadForRequeue(ctx, tx, q.BeadID); err != nil {
			return err
		}
	}
	if needsRequeue {
		if err := requeuePreservedAssignment(ctx, tx, assignmentID, q.BeadID); err != nil {
			return err
		}
	}
	if err := markRecoveryQuarantineResolvedTx(ctx, tx, id); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit recovery resolve transaction: %w", err)
	}
	return nil
}

func requeuePreservedAssignment(ctx context.Context, tx *sql.Tx, assignmentID int64, beadID string) error {
	res, err := tx.ExecContext(ctx, `
UPDATE assignments
SET status='requeued', completed_at=datetime('now')
WHERE id=? AND bead_id=? AND status IN ('quarantined', 'completed')`, assignmentID, beadID)
	if err != nil {
		return fmt.Errorf("requeue preserved assignment: %w", err)
	}
	rows, rowsErr := res.RowsAffected()
	if rowsErr != nil {
		return fmt.Errorf("requeue preserved assignment rows affected: %w", rowsErr)
	}
	if rows != 1 {
		return fmt.Errorf("requeue-preserved assignment_id %d affected %d rows", assignmentID, rows)
	}
	return nil
}

func reopenRecoveryBeadForRequeue(ctx context.Context, tx *sql.Tx, beadID string) error {
	var beadStatus string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM beads WHERE id=? AND deleted=0`, beadID).Scan(&beadStatus); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("requeue-preserved bead %s does not exist", beadID)
		}
		return fmt.Errorf("lookup requeue-preserved bead %s: %w", beadID, err)
	}
	if beadStatus == "open" {
		return nil
	}
	if beadStatus == "blocked" {
		blocked, err := recoveryBeadHasUnresolvedDependency(ctx, tx, beadID)
		if err != nil {
			return err
		}
		if blocked {
			return fmt.Errorf("requeue-preserved bead %s has unresolved dependencies", beadID)
		}
	} else if beadStatus != "in_progress" {
		return fmt.Errorf("requeue-preserved bead %s has status %q", beadID, beadStatus)
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE beads SET status='open', updated_at=datetime('now') WHERE id=? AND deleted=0 AND status IN ('in_progress', 'blocked')`, beadID)
	if err != nil {
		return fmt.Errorf("reopen requeue-preserved bead: %w", err)
	}
	rows, rowsErr := res.RowsAffected()
	if rowsErr != nil {
		return fmt.Errorf("reopen requeue-preserved bead rows affected: %w", rowsErr)
	}
	if rows != 1 {
		return fmt.Errorf("requeue-preserved bead %s reopen affected %d rows", beadID, rows)
	}
	return nil
}

func recoveryBeadHasUnresolvedDependency(ctx context.Context, tx *sql.Tx, beadID string) (bool, error) {
	var blocked bool
	if err := tx.QueryRowContext(ctx, `
SELECT EXISTS (
    SELECT 1
    FROM bead_deps d
    LEFT JOIN beads parent ON parent.id = d.depends_on_id AND parent.deleted = 0
    WHERE d.bead_id = ?
      AND d.type IN ('blocks', 'conditional-blocks')
      AND (parent.id IS NULL OR parent.status != 'closed')
)`, beadID).Scan(&blocked); err != nil {
		return false, fmt.Errorf("lookup requeue-preserved bead dependencies: %w", err)
	}
	return blocked, nil
}

func recoveryBeadHasNewerActiveAssignment(ctx context.Context, tx *sql.Tx, beadID string, preservedAssignmentID int64) (bool, error) {
	var hasNewerActiveAssignment bool
	if err := tx.QueryRowContext(ctx, `
SELECT EXISTS (
    SELECT 1
    FROM assignments
    WHERE bead_id=? AND id>? AND status='active'
)`, beadID, preservedAssignmentID).Scan(&hasNewerActiveAssignment); err != nil {
		return false, fmt.Errorf("lookup newer active assignment for requeue-preserved bead %s: %w", beadID, err)
	}
	return hasNewerActiveAssignment, nil
}

func recoveryAssignmentIDForRequeue(ctx context.Context, tx *sql.Tx, quarantineID int64, q recoveryQuarantineCLIRecord) (assignmentID int64, needsRequeue bool, err error) {
	if q.AssignmentID > 0 {
		var assignmentStatus string
		if err := tx.QueryRowContext(ctx, `SELECT status FROM assignments WHERE id=? AND bead_id=?`, q.AssignmentID, q.BeadID).Scan(&assignmentStatus); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return 0, false, fmt.Errorf("requeue-preserved assignment_id %d is not a preserved assignment for bead %s", q.AssignmentID, q.BeadID)
			}
			return 0, false, fmt.Errorf("lookup preserved recovery assignment: %w", err)
		}
		switch assignmentStatus {
		case "quarantined":
			return q.AssignmentID, true, nil
		case "requeued":
			return q.AssignmentID, false, nil
		default:
			return 0, false, fmt.Errorf("requeue-preserved assignment_id %d has status %q", q.AssignmentID, assignmentStatus)
		}
	}

	var assignmentStatus string
	if err := tx.QueryRowContext(ctx, `
SELECT id, status
FROM assignments
WHERE bead_id=?
  AND (?='' OR worktree=?)
ORDER BY id DESC
LIMIT 1`, q.BeadID, q.Worktree, q.Worktree).Scan(&assignmentID, &assignmentStatus); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, false, fmt.Errorf("requeue-preserved found no preserved assignment for bead %s", q.BeadID)
		}
		return 0, false, fmt.Errorf("lookup preserved recovery assignment: %w", err)
	}
	if assignmentStatus != "quarantined" && assignmentStatus != "completed" && assignmentStatus != "requeued" {
		return 0, false, fmt.Errorf("requeue-preserved latest assignment_id %d has status %q", assignmentID, assignmentStatus)
	}
	res, err := tx.ExecContext(ctx, `
UPDATE recovery_quarantines
SET assignment_id=?
WHERE id=? AND assignment_id IS NULL AND status IN ('open', 'human_owned')`, assignmentID, quarantineID)
	if err != nil {
		return 0, false, fmt.Errorf("link preserved recovery assignment: %w", err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return 0, false, fmt.Errorf("link preserved recovery assignment rows affected: %w", err)
	}
	if rows != 1 {
		return 0, false, fmt.Errorf("link preserved recovery assignment affected %d rows", rows)
	}
	return assignmentID, assignmentStatus != "requeued", nil
}

func discardEmptySafe(inspection recoveryInspection) bool {
	return dispatcher.RecoveryQuarantineEmptySafe(
		inspection.Dirty.Total,
		inspection.Branch.Exists,
		inspection.Branch.Ahead,
	)
}

func markRecoveryQuarantineResolved(ctx context.Context, db *sql.DB, id int64) error {
	res, err := db.ExecContext(ctx,
		`UPDATE recovery_quarantines
SET status='resolved', resolved_at=datetime('now')
WHERE id=? AND status IN ('open', 'human_owned')`,
		id)
	if err != nil {
		return fmt.Errorf("resolve recovery quarantine: %w", err)
	}
	rows, rowsErr := res.RowsAffected()
	if rowsErr != nil {
		return fmt.Errorf("resolve recovery quarantine rows affected: %w", rowsErr)
	}
	if rows == 1 {
		return nil
	}
	var status string
	if err := db.QueryRowContext(ctx, `SELECT status FROM recovery_quarantines WHERE id=?`, id).Scan(&status); err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("recovery quarantine %d not found", id)
		}
		return fmt.Errorf("lookup recovery quarantine: %w", err)
	}
	if status == "resolved" {
		return nil
	}
	return fmt.Errorf("recovery quarantine %d has status %q", id, status)
}

func markRecoveryQuarantineHumanOwned(ctx context.Context, db *sql.DB, id int64) error {
	res, err := db.ExecContext(ctx,
		`UPDATE recovery_quarantines SET status='human_owned', resolved_at=datetime('now') WHERE id=? AND status='open'`,
		id)
	if err != nil {
		return fmt.Errorf("mark recovery quarantine human-owned: %w", err)
	}
	rows, rowsErr := res.RowsAffected()
	if rowsErr != nil {
		return fmt.Errorf("mark recovery quarantine human-owned rows affected: %w", rowsErr)
	}
	if rows == 1 {
		return nil
	}
	var status string
	if err := db.QueryRowContext(ctx, `SELECT status FROM recovery_quarantines WHERE id=?`, id).Scan(&status); err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("recovery quarantine %d not found", id)
		}
		return fmt.Errorf("lookup recovery quarantine: %w", err)
	}
	if status == "human_owned" {
		return nil
	}
	return fmt.Errorf("recovery quarantine %d has status %q", id, status)
}

func markRecoveryQuarantineResolvedTx(ctx context.Context, tx *sql.Tx, id int64) error {
	res, err := tx.ExecContext(ctx,
		`UPDATE recovery_quarantines SET status='resolved', resolved_at=datetime('now') WHERE id=? AND status IN ('open', 'human_owned')`,
		id)
	if err != nil {
		return fmt.Errorf("resolve recovery quarantine: %w", err)
	}
	rows, rowsErr := res.RowsAffected()
	if rowsErr != nil {
		return fmt.Errorf("resolve recovery quarantine rows affected: %w", rowsErr)
	}
	if rows == 1 {
		return nil
	}
	return fmt.Errorf("recovery quarantine %d was not open", id)
}

func writeRecoveryQuarantineList(w io.Writer, records []recoveryQuarantineCLIRecord) {
	if len(records) == 0 {
		fmt.Fprintln(w, "No open recovery quarantines.")
		return
	}
	for _, r := range records {
		fmt.Fprintf(w, "#%d %s %s", r.ID, r.BeadID, r.Reason)
		if r.Branch != "" {
			fmt.Fprintf(w, " branch=%s", r.Branch)
		}
		if r.PreservedRef != "" {
			fmt.Fprintf(w, " preserved_ref=%s", r.PreservedRef)
		}
		if r.Worktree != "" {
			fmt.Fprintf(w, " worktree=%s", r.Worktree)
		}
		fmt.Fprintln(w)
	}
}

func writeRecoveryInspection(w io.Writer, inspection recoveryInspection) {
	q := inspection.Quarantine
	fmt.Fprintf(w, "#%d %s %s status=%s\n", q.ID, q.BeadID, q.Reason, q.Status)
	if q.Details != "" {
		fmt.Fprintf(w, "  details: %s\n", q.Details)
	}
	if inspection.Bead != nil {
		fmt.Fprintf(w, "  bead: %s (%s, %s)\n", inspection.Bead.Title, inspection.Bead.Status, inspection.Bead.Type)
	}
	if inspection.Assignment != nil {
		fmt.Fprintf(w, "  assignment: #%d %s worker=%s attempts=%d\n",
			inspection.Assignment.ID, inspection.Assignment.Status, inspection.Assignment.WorkerID, inspection.Assignment.AttemptCount)
	}
	fmt.Fprintf(w, "  branch: %s exists=%t", inspection.Branch.Name, inspection.Branch.Exists)
	if inspection.Branch.Ahead > 0 || inspection.Branch.Behind > 0 {
		fmt.Fprintf(w, " ahead=%d behind=%d", inspection.Branch.Ahead, inspection.Branch.Behind)
	}
	if inspection.Branch.Error != "" {
		fmt.Fprintf(w, " error=%q", inspection.Branch.Error)
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "  worktree: %s exists=%t", inspection.Worktree.Path, inspection.Worktree.Exists)
	if inspection.Worktree.CheckedOutBranch != "" {
		fmt.Fprintf(w, " branch=%s", inspection.Worktree.CheckedOutBranch)
	}
	if inspection.Worktree.Error != "" {
		fmt.Fprintf(w, " error=%q", inspection.Worktree.Error)
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "  dirty: total=%d staged=%d modified=%d deleted=%d untracked=%d\n",
		inspection.Dirty.Total, inspection.Dirty.Staged, inspection.Dirty.Modified, inspection.Dirty.Deleted, inspection.Dirty.Untracked)
	for _, sample := range inspection.Dirty.Sample {
		fmt.Fprintf(w, "    %s\n", sample)
	}
	fmt.Fprintf(w, "  action: %s\n", inspection.RecommendedAction)
}
