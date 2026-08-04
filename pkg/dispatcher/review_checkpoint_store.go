package dispatcher

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"oro/pkg/reviewcontract"
)

// ErrCheckpointConflict reports that a checkpoint changed before a requested transition.
var ErrCheckpointConflict = errors.New("review checkpoint conflict")

// ReviewCheckpointState is the durable lifecycle state for a review checkpoint.
type ReviewCheckpointState string

// ReviewCheckpointState values define the durable checkpoint lifecycle.
const (
	ReviewCheckpointStateQGPassed                 ReviewCheckpointState = "qg_passed"
	ReviewCheckpointStateReviewRunning            ReviewCheckpointState = "review_running"
	ReviewCheckpointStateRejected                 ReviewCheckpointState = "rejected"
	ReviewCheckpointStateCorrectionAssigning      ReviewCheckpointState = "correction_assigning"
	ReviewCheckpointStateCorrectionAssigned       ReviewCheckpointState = "correction_assigned"
	ReviewCheckpointStateContractRepairRunning    ReviewCheckpointState = "contract_repair_running"
	ReviewCheckpointStateBlocked                  ReviewCheckpointState = "blocked"
	ReviewCheckpointStateFailed                   ReviewCheckpointState = "failed"
	ReviewCheckpointStateRecoveryRunning          ReviewCheckpointState = "recovery_running"
	ReviewCheckpointStateQuarantined              ReviewCheckpointState = "quarantined"
	ReviewCheckpointStateApproved                 ReviewCheckpointState = "approved"
	ReviewCheckpointStateManualIntegrationPending ReviewCheckpointState = "manual_integration_pending"
	ReviewCheckpointStateIntegrating              ReviewCheckpointState = "integrating"
	ReviewCheckpointStateIntegrated               ReviewCheckpointState = "integrated"
	ReviewCheckpointStateSuperseded               ReviewCheckpointState = "superseded"
)

// CheckpointInput contains the immutable identity and initial ownership of a review checkpoint.
type CheckpointInput struct {
	CheckpointKey       string
	BeadID              string
	OriginAssignmentID  int64
	CurrentAssignmentID int64
	WorkerID            string
	Worktree            string
	Branch              string
	TargetBranch        string
	HeadSHA             string
	TargetSHA           string
	AcceptanceHash      string
	QGScriptHash        string
	QGMode              string
	ReviewPolicyHash    string
	TriageRevision      string
	ReadyAttempt        string
	State               ReviewCheckpointState
}

// ReviewCheckpoint is a durable review checkpoint record.
type ReviewCheckpoint struct {
	ID int64
	CheckpointInput
}

// ReviewIntegrationCheckpoint adds the durable merge proof used to resume an
// approved checkpoint without repeating semantic side effects after a crash.
type ReviewIntegrationCheckpoint struct {
	ReviewCheckpoint
	IntegrationTargetBeforeSHA   string
	IntegrationApprovedHeadSHA   string
	IntegrationObservedTargetSHA string
	IntegrationStep              string
}

// ReviewCheckpointStore persists durable review checkpoint lifecycle changes.
type ReviewCheckpointStore struct {
	db *sql.DB
}

// NewReviewCheckpointStore constructs a checkpoint store over db.
//
//oro:testonly
func NewReviewCheckpointStore(db *sql.DB) *ReviewCheckpointStore {
	return &ReviewCheckpointStore{db: db}
}

// CreateOrReuse returns the single active checkpoint for a canonical key.
//
//oro:testonly
func (s *ReviewCheckpointStore) CreateOrReuse(ctx context.Context, in CheckpointInput) (ReviewCheckpoint, error) {
	if s == nil || s.db == nil {
		return ReviewCheckpoint{}, errors.New("create or reuse review checkpoint: db is nil")
	}
	if err := validateCheckpointInput(in); err != nil {
		return ReviewCheckpoint{}, err
	}

	_, err := s.db.ExecContext(ctx, `
INSERT INTO review_checkpoints (
  checkpoint_key, bead_id, origin_assignment_id, current_assignment_id, worker_id,
  worktree, branch, target_branch, head_sha, target_sha, acceptance_hash,
  qg_script_hash, qg_mode, review_policy_hash, triage_revision, ready_attempt, state
) VALUES (?, ?, ?, NULLIF(?, 0), NULLIF(?, ''), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(checkpoint_key) WHERE state <> 'superseded' DO NOTHING`,
		in.CheckpointKey, in.BeadID, in.OriginAssignmentID, in.CurrentAssignmentID, in.WorkerID,
		in.Worktree, in.Branch, in.TargetBranch, in.HeadSHA, in.TargetSHA, in.AcceptanceHash,
		in.QGScriptHash, in.QGMode, in.ReviewPolicyHash, in.TriageRevision, in.ReadyAttempt, in.State)
	if err != nil {
		return ReviewCheckpoint{}, fmt.Errorf("insert review checkpoint: %w", err)
	}

	checkpoint, err := scanReviewCheckpoint(s.db.QueryRowContext(ctx, `
SELECT id, checkpoint_key, bead_id, origin_assignment_id, COALESCE(current_assignment_id, 0),
       COALESCE(worker_id, ''), worktree, branch, target_branch, head_sha, target_sha,
       acceptance_hash, qg_script_hash, qg_mode, review_policy_hash, triage_revision,
       ready_attempt, state
FROM review_checkpoints
WHERE checkpoint_key = ? AND state <> 'superseded'
ORDER BY id DESC
LIMIT 1`, in.CheckpointKey))
	if err != nil {
		return ReviewCheckpoint{}, fmt.Errorf("load active review checkpoint: %w", err)
	}
	return checkpoint, nil
}

// CompareAndSwap transitions a checkpoint only when it is still in from.
func (s *ReviewCheckpointStore) CompareAndSwap(ctx context.Context, id int64, from, to ReviewCheckpointState) error {
	if s == nil || s.db == nil {
		return errors.New("compare and swap review checkpoint: db is nil")
	}
	if id <= 0 || from == "" || to == "" {
		return fmt.Errorf("compare and swap review checkpoint: invalid transition %d %q -> %q", id, from, to)
	}

	result, err := s.db.ExecContext(ctx, `
UPDATE review_checkpoints
SET state = ?, updated_at = datetime('now')
WHERE id = ? AND state = ?`, to, id, from)
	if err != nil {
		return fmt.Errorf("compare and swap review checkpoint %d: %w", id, err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("count review checkpoint transition %d: %w", id, err)
	}
	if rows != 1 {
		return fmt.Errorf("compare and swap review checkpoint %d from %q to %q: %w", id, from, to, ErrCheckpointConflict)
	}
	return nil
}

// LoadOwningForBead returns the newest nonterminal checkpoint that owns beadID.
func (s *ReviewCheckpointStore) LoadOwningForBead(ctx context.Context, beadID string) (*ReviewCheckpoint, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("load owning review checkpoint: db is nil")
	}
	if beadID == "" {
		return nil, errors.New("load owning review checkpoint: bead ID is empty")
	}
	checkpoint, err := scanReviewCheckpoint(s.db.QueryRowContext(ctx, `
SELECT id, checkpoint_key, bead_id, origin_assignment_id, COALESCE(current_assignment_id, 0),
       COALESCE(worker_id, ''), worktree, branch, target_branch, head_sha, target_sha,
       acceptance_hash, qg_script_hash, qg_mode, review_policy_hash, triage_revision,
       ready_attempt, state
FROM review_checkpoints
WHERE bead_id = ? AND state NOT IN ('integrated', 'superseded')
ORDER BY id DESC
LIMIT 1`, beadID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load owning review checkpoint for %s: %w", beadID, err)
	}
	return &checkpoint, nil
}

// ListPendingIntegrations returns checkpoints that must be reconciled before
// ordinary ops routing or assignment admission begins.
func (s *ReviewCheckpointStore) ListPendingIntegrations(ctx context.Context) ([]ReviewIntegrationCheckpoint, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("list pending review integrations: db is nil")
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, checkpoint_key, bead_id, origin_assignment_id, COALESCE(current_assignment_id, 0),
       COALESCE(worker_id, ''), worktree, branch, target_branch, head_sha, target_sha,
       acceptance_hash, qg_script_hash, qg_mode, review_policy_hash, triage_revision,
       ready_attempt, state, COALESCE(integration_target_before_sha, ''),
       COALESCE(integration_approved_head_sha, ''), COALESCE(integration_observed_target_sha, ''),
       COALESCE(integration_step, '')
FROM review_checkpoints
WHERE state IN ('approved', 'manual_integration_pending', 'integrating')
ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list pending review integrations: %w", err)
	}
	defer rows.Close()
	var checkpoints []ReviewIntegrationCheckpoint
	for rows.Next() {
		var checkpoint ReviewIntegrationCheckpoint
		if err := rows.Scan(
			&checkpoint.ID, &checkpoint.CheckpointKey, &checkpoint.BeadID,
			&checkpoint.OriginAssignmentID, &checkpoint.CurrentAssignmentID, &checkpoint.WorkerID,
			&checkpoint.Worktree, &checkpoint.Branch, &checkpoint.TargetBranch,
			&checkpoint.HeadSHA, &checkpoint.TargetSHA, &checkpoint.AcceptanceHash,
			&checkpoint.QGScriptHash, &checkpoint.QGMode, &checkpoint.ReviewPolicyHash,
			&checkpoint.TriageRevision, &checkpoint.ReadyAttempt, &checkpoint.State,
			&checkpoint.IntegrationTargetBeforeSHA, &checkpoint.IntegrationApprovedHeadSHA,
			&checkpoint.IntegrationObservedTargetSHA, &checkpoint.IntegrationStep,
		); err != nil {
			return nil, fmt.Errorf("scan pending review integration: %w", err)
		}
		checkpoints = append(checkpoints, checkpoint)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate pending review integrations: %w", err)
	}
	return checkpoints, nil
}

// BeginIntegration records immutable merge intent and changes state with one
// CAS. A successful call is the durable boundary before any merge attempt.
func (s *ReviewCheckpointStore) BeginIntegration(
	ctx context.Context,
	id int64,
	from, to ReviewCheckpointState,
	targetBeforeSHA, approvedHeadSHA string,
) error {
	if s == nil || s.db == nil {
		return errors.New("begin review integration: db is nil")
	}
	if id <= 0 || targetBeforeSHA == "" || approvedHeadSHA == "" {
		return errors.New("begin review integration: missing required identity")
	}
	result, err := s.db.ExecContext(ctx, `
UPDATE review_checkpoints
SET state=?, integration_target_before_sha=?, integration_approved_head_sha=?,
    integration_step='intent', updated_at=datetime('now')
WHERE id=? AND state=?`, to, targetBeforeSHA, approvedHeadSHA, id, from)
	if err != nil {
		return fmt.Errorf("begin review integration %d: %w", id, err)
	}
	return requireOneCheckpointRow(result, id, "begin review integration")
}

// PromoteManualIntegration records that external integration proof was
// observed and claims finalization through an exact-state CAS.
func (s *ReviewCheckpointStore) PromoteManualIntegration(ctx context.Context, id int64, observedSHA string) error {
	if s == nil || s.db == nil {
		return errors.New("promote manual review integration: db is nil")
	}
	result, err := s.db.ExecContext(ctx, `
UPDATE review_checkpoints
SET state='integrating', integration_observed_target_sha=?, integration_step='merge_observed',
    updated_at=datetime('now')
WHERE id=? AND state='manual_integration_pending'`, observedSHA, id)
	if err != nil {
		return fmt.Errorf("promote manual review integration %d: %w", id, err)
	}
	return requireOneCheckpointRow(result, id, "promote manual review integration")
}

// ObserveIntegration persists exact target proof before any database or bead
// completion side effect.
func (s *ReviewCheckpointStore) ObserveIntegration(ctx context.Context, id int64, observedSHA string) error {
	if s == nil || s.db == nil {
		return errors.New("observe review integration: db is nil")
	}
	result, err := s.db.ExecContext(ctx, `
UPDATE review_checkpoints
SET integration_observed_target_sha=?, integration_step='merge_observed', updated_at=datetime('now')
WHERE id=? AND state='integrating'`, observedSHA, id)
	if err != nil {
		return fmt.Errorf("observe review integration %d: %w", id, err)
	}
	return requireOneCheckpointRow(result, id, "observe review integration")
}

// AdvanceIntegrationStep records a completed idempotent finalization step.
func (s *ReviewCheckpointStore) AdvanceIntegrationStep(ctx context.Context, id int64, step string) error {
	if s == nil || s.db == nil {
		return errors.New("advance review integration step: db is nil")
	}
	result, err := s.db.ExecContext(ctx, `
UPDATE review_checkpoints SET integration_step=?, updated_at=datetime('now')
WHERE id=? AND state='integrating'`, step, id)
	if err != nil {
		return fmt.Errorf("advance review integration %d to %q: %w", id, step, err)
	}
	return requireOneCheckpointRow(result, id, "advance review integration")
}

// CompleteIntegration makes the checkpoint terminal after all finalization
// steps have durably completed.
func (s *ReviewCheckpointStore) CompleteIntegration(ctx context.Context, id int64) error {
	if s == nil || s.db == nil {
		return errors.New("complete review integration: db is nil")
	}
	result, err := s.db.ExecContext(ctx, `
UPDATE review_checkpoints
SET state='integrated', integration_step='integrated', completed_at=datetime('now'), updated_at=datetime('now')
WHERE id=? AND state='integrating'`, id)
	if err != nil {
		return fmt.Errorf("complete review integration %d: %w", id, err)
	}
	return requireOneCheckpointRow(result, id, "complete review integration")
}

// BlockIntegration creates one durable blocked condition. Repeated startup
// passes see the terminal-for-reconciliation state and cannot duplicate it.
func (s *ReviewCheckpointStore) BlockIntegration(ctx context.Context, id int64, reason string) (bool, error) {
	if s == nil || s.db == nil {
		return false, errors.New("block review integration: db is nil")
	}
	blockers, err := json.Marshal([]string{reason})
	if err != nil {
		return false, fmt.Errorf("marshal review integration blocker: %w", err)
	}
	result, err := s.db.ExecContext(ctx, `
UPDATE review_checkpoints
SET state='blocked', integration_step='blocked', summary=?, blockers_json=?, updated_at=datetime('now')
WHERE id=? AND state IN ('approved', 'manual_integration_pending', 'integrating')`, reason, blockers, id)
	if err != nil {
		return false, fmt.Errorf("block review integration %d: %w", id, err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("count blocked review integration %d: %w", id, err)
	}
	return rows == 1, nil
}

func requireOneCheckpointRow(result sql.Result, id int64, operation string) error {
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("count %s %d: %w", operation, id, err)
	}
	if rows != 1 {
		return fmt.Errorf("%s %d affected %d rows: %w", operation, id, rows, ErrCheckpointConflict)
	}
	return nil
}

// SaveRejectedFindings atomically replaces the structured findings persisted for a rejected checkpoint.
//
//oro:testonly
func (s *ReviewCheckpointStore) SaveRejectedFindings(ctx context.Context, checkpointID int64, findings []reviewcontract.Finding) error {
	if s == nil || s.db == nil {
		return errors.New("save rejected findings: db is nil")
	}
	if checkpointID <= 0 {
		return fmt.Errorf("save rejected findings: invalid checkpoint ID %d", checkpointID)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin save rejected findings: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM review_checkpoint_findings WHERE checkpoint_id = ?`, checkpointID); err != nil {
		return fmt.Errorf("clear rejected findings: %w", err)
	}
	for _, finding := range findings {
		if err := insertRejectedFinding(ctx, tx, checkpointID, finding); err != nil {
			return err
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE review_checkpoints SET updated_at = datetime('now') WHERE id = ?`, checkpointID)
	if err != nil {
		return fmt.Errorf("touch review checkpoint after rejected findings: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("count touched review checkpoint after rejected findings: %w", err)
	}
	if rows != 1 {
		return fmt.Errorf("save rejected findings: checkpoint %d not found", checkpointID)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit rejected findings: %w", err)
	}
	return nil
}

func insertRejectedFinding(ctx context.Context, tx *sql.Tx, checkpointID int64, finding reviewcontract.Finding) error {
	if finding.ID == "" {
		return errors.New("save rejected findings: finding ID is empty")
	}
	compact, err := json.Marshal(finding)
	if err != nil {
		return fmt.Errorf("marshal rejected finding %q: %w", finding.ID, err)
	}
	file, line := compactFindingLocation(finding)
	if _, err := tx.ExecContext(ctx, `
INSERT INTO review_checkpoint_findings (
  checkpoint_id, finding_id, severity, file, line, contract_impact, required_action, compact_json
) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, checkpointID, finding.ID, finding.Severity, file, line,
		finding.ContractImpact, finding.RequiredAction, compact); err != nil {
		return fmt.Errorf("insert rejected finding %q: %w", finding.ID, err)
	}
	return nil
}

func compactFindingLocation(finding reviewcontract.Finding) (file string, line any) {
	if len(finding.Evidence) == 0 {
		return "", nil
	}
	return finding.Evidence[0].File, finding.Evidence[0].LineStart
}

func validateCheckpointInput(in CheckpointInput) error {
	if in.OriginAssignmentID <= 0 || in.State == "" || missingCheckpointIdentity(in) || missingCheckpointMetadata(in) {
		return errors.New("create or reuse review checkpoint: missing required input")
	}
	return nil
}

func missingCheckpointIdentity(in CheckpointInput) bool {
	return in.CheckpointKey == "" || in.BeadID == "" || in.Worktree == "" || in.Branch == "" ||
		in.TargetBranch == "" || in.HeadSHA == "" || in.TargetSHA == "" || in.AcceptanceHash == ""
}

func missingCheckpointMetadata(in CheckpointInput) bool {
	return in.QGScriptHash == "" || in.QGMode == "" || in.ReviewPolicyHash == "" ||
		in.TriageRevision == "" || in.ReadyAttempt == ""
}

func scanReviewCheckpoint(row *sql.Row) (ReviewCheckpoint, error) {
	var checkpoint ReviewCheckpoint
	err := row.Scan(
		&checkpoint.ID,
		&checkpoint.CheckpointKey,
		&checkpoint.BeadID,
		&checkpoint.OriginAssignmentID,
		&checkpoint.CurrentAssignmentID,
		&checkpoint.WorkerID,
		&checkpoint.Worktree,
		&checkpoint.Branch,
		&checkpoint.TargetBranch,
		&checkpoint.HeadSHA,
		&checkpoint.TargetSHA,
		&checkpoint.AcceptanceHash,
		&checkpoint.QGScriptHash,
		&checkpoint.QGMode,
		&checkpoint.ReviewPolicyHash,
		&checkpoint.TriageRevision,
		&checkpoint.ReadyAttempt,
		&checkpoint.State,
	)
	if err != nil {
		return ReviewCheckpoint{}, fmt.Errorf("scan review checkpoint: %w", err)
	}
	return checkpoint, nil
}

// ArtifactRef identifies an artifact eligible for retention pruning.
type ArtifactRef struct {
	Path string
}

// ListPrunableArtifacts returns artifacts whose every checkpoint reference is
// terminal and older than olderThan. Shared artifacts are retained until all
// references become eligible.
func (s *ReviewCheckpointStore) ListPrunableArtifacts(ctx context.Context, olderThan time.Time) ([]ArtifactRef, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("list prunable review artifacts: db is nil")
	}

	rows, err := s.db.QueryContext(ctx, `
WITH artifact_references AS (
  SELECT artifact_path AS path, state, COALESCE(completed_at, updated_at, created_at) AS terminal_at
  FROM review_checkpoints
  WHERE COALESCE(artifact_path, '') <> ''
  UNION ALL
  SELECT recovery_artifact_path AS path, state, COALESCE(completed_at, updated_at, created_at) AS terminal_at
  FROM review_checkpoints
  WHERE COALESCE(recovery_artifact_path, '') <> ''
)
SELECT DISTINCT candidate.path
FROM artifact_references AS candidate
WHERE candidate.state IN (?, ?)
  AND datetime(candidate.terminal_at) < datetime(?)
  AND NOT EXISTS (
    SELECT 1
    FROM artifact_references AS reference
    WHERE reference.path = candidate.path
      AND (reference.state NOT IN (?, ?) OR datetime(reference.terminal_at) >= datetime(?))
  )
ORDER BY candidate.path`,
		ReviewCheckpointStateIntegrated,
		ReviewCheckpointStateSuperseded,
		olderThan.UTC().Format(time.RFC3339Nano),
		ReviewCheckpointStateIntegrated,
		ReviewCheckpointStateSuperseded,
		olderThan.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, fmt.Errorf("query prunable review artifacts: %w", err)
	}
	defer func() { _ = rows.Close() }()

	artifacts := make([]ArtifactRef, 0)
	for rows.Next() {
		var artifact ArtifactRef
		if err := rows.Scan(&artifact.Path); err != nil {
			return nil, fmt.Errorf("scan prunable review artifact: %w", err)
		}
		artifacts = append(artifacts, artifact)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate prunable review artifacts: %w", err)
	}
	return artifacts, nil
}

// ClearPrunedArtifact removes durable references after an artifact was deleted.
// A missing file is also acknowledged so a restart after deletion does not retry it.
func (s *ReviewCheckpointStore) ClearPrunedArtifact(ctx context.Context, path string) error {
	if s == nil || s.db == nil {
		return errors.New("clear pruned review artifact: db is nil")
	}
	if path == "" {
		return errors.New("clear pruned review artifact: path is empty")
	}

	_, err := s.db.ExecContext(ctx, `
UPDATE review_checkpoints
SET artifact_path = CASE WHEN artifact_path = ? THEN NULL ELSE artifact_path END,
    recovery_artifact_path = CASE WHEN recovery_artifact_path = ? THEN NULL ELSE recovery_artifact_path END,
    updated_at = datetime('now')
WHERE artifact_path = ? OR recovery_artifact_path = ?`, path, path, path, path)
	if err != nil {
		return fmt.Errorf("clear pruned review artifact %q: %w", path, err)
	}
	return nil
}
