package protocol

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ErrWorkProposalSubmissionConflict means a client proposal ID was replayed
// with content different from its durable original submission.
var ErrWorkProposalSubmissionConflict = errors.New("work proposal submission conflicts with existing content")

// ErrWorkProposalEvidenceNotFound means a proposal did not cite durable
// evidence for its own assignment and worker identity.
var ErrWorkProposalEvidenceNotFound = errors.New("work proposal evidence run not found")

// WorkProposalStore persists provisional work proposals and their replay
// boundary. It does not derive canonical scope or materialize executable work.
type WorkProposalStore struct {
	db *sql.DB
}

// NewWorkProposalStore creates a durable proposal store over a database whose
// runtime schema has already been applied.
func NewWorkProposalStore(db *sql.DB) (*WorkProposalStore, error) {
	if db == nil {
		return nil, errors.New("new work proposal store: nil db")
	}
	return &WorkProposalStore{db: db}, nil
}

// DB exposes the backing database for integration boundaries that already own
// its lifecycle.
func (s *WorkProposalStore) DB() *sql.DB {
	if s == nil {
		return nil
	}
	return s.db
}

// Close closes the backing database.
func (s *WorkProposalStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("close work proposal store: %w", err)
	}
	return nil
}

// StoreEvidenceRun records durable evidence before a proposal cites it.
func (s *WorkProposalStore) StoreEvidenceRun(ctx context.Context, run EvidenceRun) error {
	if s == nil || s.db == nil {
		return errors.New("store evidence run: nil db")
	}
	run.ID = strings.TrimSpace(run.ID)
	run.WorkerID = strings.TrimSpace(run.WorkerID)
	run.BeadID = strings.TrimSpace(run.BeadID)
	run.Kind = strings.TrimSpace(run.Kind)
	run.Status = strings.TrimSpace(run.Status)
	if run.ID == "" || run.AssignmentID <= 0 || run.WorkerID == "" || run.BeadID == "" || run.Kind == "" || run.Status == "" {
		return errors.New("store evidence run: incomplete evidence")
	}
	if _, err := s.db.ExecContext(ctx, `
INSERT INTO evidence_runs (id, assignment_id, worker_id, bead_id, kind, status)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO NOTHING`, run.ID, run.AssignmentID, run.WorkerID, run.BeadID, run.Kind, run.Status); err != nil {
		return fmt.Errorf("store evidence run: insert: %w", err)
	}
	return nil
}

// StoreWorkProposal persists one provisional proposal. Exact submissions
// replay their stored response; reusing the same client ID with different
// content is rejected before any new proposal row is written.
func (s *WorkProposalStore) StoreWorkProposal(ctx context.Context, payload WorkProposalPayload) (WorkProposalResult, error) {
	if s == nil || s.db == nil {
		return WorkProposalResult{}, errors.New("store work proposal: nil db")
	}
	payload = normalizedWorkProposalPayload(payload)
	if err := validateWorkProposalPayload(payload); err != nil {
		return WorkProposalResult{}, err
	}
	contentHash, err := workProposalContentHash(payload)
	if err != nil {
		return WorkProposalResult{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return WorkProposalResult{}, fmt.Errorf("store work proposal: begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	stored, found, err := loadWorkProposalSubmission(ctx, tx, payload.AssignmentID, payload.ClientProposalID)
	if err != nil {
		return WorkProposalResult{}, err
	}
	if found {
		if stored.contentHash != contentHash {
			return WorkProposalResult{}, ErrWorkProposalSubmissionConflict
		}
		return stored.result, nil
	}
	if err := proposalEvidenceExists(ctx, tx, payload); err != nil {
		return WorkProposalResult{}, err
	}

	result := WorkProposalResult{
		ProposalID: proposalIDForSubmission(payload.AssignmentID, payload.ClientProposalID),
		State:      "pending",
	}
	if err := insertWorkProposal(ctx, tx, result, payload); err != nil {
		return WorkProposalResult{}, err
	}
	if err := insertWorkProposalTransition(ctx, tx, result.ProposalID, payload); err != nil {
		return WorkProposalResult{}, err
	}
	response, err := json.Marshal(result)
	if err != nil {
		return WorkProposalResult{}, fmt.Errorf("store work proposal: marshal response: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO work_proposal_submissions
    (assignment_id, client_proposal_id, content_hash, proposal_id, response_json)
VALUES (?, ?, ?, ?, ?)`,
		payload.AssignmentID, payload.ClientProposalID, contentHash, result.ProposalID, string(response)); err != nil {
		return WorkProposalResult{}, fmt.Errorf("store work proposal: insert submission: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return WorkProposalResult{}, fmt.Errorf("store work proposal: commit: %w", err)
	}
	return result, nil
}

func proposalEvidenceExists(ctx context.Context, tx *sql.Tx, payload WorkProposalPayload) error {
	var count int
	if err := tx.QueryRowContext(ctx, `
SELECT COUNT(*) FROM evidence_runs
 WHERE id=? AND assignment_id=? AND worker_id=? AND bead_id=?`,
		payload.EvidenceRunID, payload.AssignmentID, payload.WorkerID, payload.BeadID,
	).Scan(&count); err != nil {
		return fmt.Errorf("store work proposal: load evidence run: %w", err)
	}
	if count == 0 {
		return ErrWorkProposalEvidenceNotFound
	}
	return nil
}

type storedWorkProposalSubmission struct {
	contentHash string
	result      WorkProposalResult
}

func loadWorkProposalSubmission(ctx context.Context, tx *sql.Tx, assignmentID int64, clientProposalID string) (storedWorkProposalSubmission, bool, error) {
	var stored storedWorkProposalSubmission
	var response string
	err := tx.QueryRowContext(ctx, `
SELECT content_hash, response_json
  FROM work_proposal_submissions
 WHERE assignment_id=? AND client_proposal_id=?`, assignmentID, clientProposalID,
	).Scan(&stored.contentHash, &response)
	if errors.Is(err, sql.ErrNoRows) {
		return storedWorkProposalSubmission{}, false, nil
	}
	if err != nil {
		return storedWorkProposalSubmission{}, false, fmt.Errorf("store work proposal: load submission: %w", err)
	}
	if err := json.Unmarshal([]byte(response), &stored.result); err != nil {
		return storedWorkProposalSubmission{}, false, fmt.Errorf("store work proposal: decode stored response: %w", err)
	}
	return stored, true, nil
}

func insertWorkProposal(ctx context.Context, tx *sql.Tx, result WorkProposalResult, payload WorkProposalPayload) error {
	_, err := tx.ExecContext(ctx, `
INSERT INTO work_proposals (
    id, assignment_id, worker_id, bead_id, evidence_run_id, fingerprint,
    provisional_scope_hint, kind, summary, suggested_title, suggested_type,
    suggested_priority, state
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		result.ProposalID, payload.AssignmentID, payload.WorkerID, payload.BeadID,
		payload.EvidenceRunID, payload.Fingerprint, payload.ScopeHint, payload.Kind,
		payload.Summary, payload.SuggestedTitle, payload.SuggestedType,
		payload.SuggestedPriority, result.State)
	if err != nil {
		return fmt.Errorf("store work proposal: insert proposal: %w", err)
	}
	return nil
}

func insertWorkProposalTransition(ctx context.Context, tx *sql.Tx, proposalID string, payload WorkProposalPayload) error {
	if _, err := tx.ExecContext(ctx, `
INSERT INTO work_proposal_transitions (proposal_id, generation, to_state, reason)
VALUES (?, 1, 'pending', 'received')`, proposalID); err != nil {
		return fmt.Errorf("store work proposal: insert transition: %w", err)
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("store work proposal: marshal event payload: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO work_proposal_events (proposal_id, generation, event_type, payload)
VALUES (?, 1, 'work_proposal_received', ?)`, proposalID, string(payloadJSON)); err != nil {
		return fmt.Errorf("store work proposal: insert event: %w", err)
	}
	return nil
}

func normalizedWorkProposalPayload(payload WorkProposalPayload) WorkProposalPayload {
	payload.ClientProposalID = strings.TrimSpace(payload.ClientProposalID)
	payload.WorkerID = strings.TrimSpace(payload.WorkerID)
	payload.BeadID = strings.TrimSpace(payload.BeadID)
	payload.EvidenceRunID = strings.TrimSpace(payload.EvidenceRunID)
	payload.Fingerprint = strings.TrimSpace(payload.Fingerprint)
	payload.ScopeHint = strings.TrimSpace(payload.ScopeHint)
	payload.Kind = strings.TrimSpace(payload.Kind)
	payload.Summary = strings.TrimSpace(payload.Summary)
	payload.SuggestedTitle = strings.TrimSpace(payload.SuggestedTitle)
	payload.SuggestedType = strings.TrimSpace(payload.SuggestedType)
	return payload
}

func validateWorkProposalPayload(payload WorkProposalPayload) error {
	if payload.ClientProposalID == "" || payload.AssignmentID <= 0 || payload.WorkerID == "" || payload.BeadID == "" ||
		payload.EvidenceRunID == "" || payload.Fingerprint == "" || payload.Kind == "" || payload.Summary == "" {
		return errors.New("store work proposal: incomplete payload")
	}
	return nil
}

func workProposalContentHash(payload WorkProposalPayload) (string, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("store work proposal: marshal content: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func proposalIDForSubmission(assignmentID int64, clientProposalID string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d\x00%s", assignmentID, clientProposalID)))
	return "proposal-" + hex.EncodeToString(sum[:16])
}
