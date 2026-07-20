package dispatcher

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

const remoteGateSchemaDDL = `
CREATE TABLE IF NOT EXISTS remote_gates (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    gate_key TEXT NOT NULL UNIQUE,
    bead_id TEXT NOT NULL,
    assignment_id INTEGER NOT NULL,
    candidate_sha TEXT NOT NULL,
    base_sha TEXT NOT NULL,
    target_branch TEXT NOT NULL,
    adoption_ref TEXT NOT NULL,
    state TEXT NOT NULL,
    version INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS remote_gate_presubmit_results (
    gate_id INTEGER NOT NULL REFERENCES remote_gates(id),
    action_name TEXT NOT NULL,
    candidate_sha TEXT NOT NULL,
    base_sha TEXT NOT NULL,
    command TEXT NOT NULL,
    profile TEXT NOT NULL,
    tool_hash TEXT NOT NULL,
    started_at TEXT NOT NULL,
    completed_at TEXT NOT NULL,
    outcome TEXT NOT NULL,
    logs TEXT NOT NULL DEFAULT '',
    resource_class TEXT NOT NULL,
    PRIMARY KEY (gate_id, action_name, candidate_sha, base_sha, command, profile, tool_hash)
);`

// ErrRemoteGateTransitionConflict reports a transition whose expected source
// state no longer matches the durable record.
var ErrRemoteGateTransitionConflict = errors.New("remote gate transition conflict")

// RemoteGateState is the dispatcher-owned lifecycle state of an adopted candidate.
type RemoteGateState string

// Remote gate states intentionally mirror the durable part of the remote-gate
// state machine. More specialized phases can be added without returning
// ownership to a worker.
const (
	RemoteGateStateCandidateAdopted     RemoteGateState = "candidate_adopted"
	RemoteGateStateLocalPresubmit       RemoteGateState = "local_presubmit"
	RemoteGateStateRebasing             RemoteGateState = "rebasing"
	RemoteGateStateLocalPresubmitRebase RemoteGateState = "local_presubmit_post_rebase"
	RemoteGateStateOpsReview            RemoteGateState = "ops_review"
	RemoteGateStatePublishing           RemoteGateState = "publishing"
	RemoteGateStateAwaitingRun          RemoteGateState = "awaiting_run"
	RemoteGateStateRunning              RemoteGateState = "running"
	RemoteGateStatePassed               RemoteGateState = "passed"
	RemoteGateStateFailed               RemoteGateState = "failed"
	RemoteGateStateReconciled           RemoteGateState = "reconciled"
)

var remoteGateTransitions = map[RemoteGateState][]RemoteGateState{ //nolint:gochecknoglobals // static state-machine configuration
	RemoteGateStateCandidateAdopted:     {RemoteGateStateLocalPresubmit},
	RemoteGateStateLocalPresubmit:       {RemoteGateStateRebasing, RemoteGateStateFailed},
	RemoteGateStateRebasing:             {RemoteGateStateLocalPresubmitRebase, RemoteGateStateFailed},
	RemoteGateStateLocalPresubmitRebase: {RemoteGateStateOpsReview, RemoteGateStateFailed},
	RemoteGateStateOpsReview:            {RemoteGateStatePublishing, RemoteGateStateFailed},
	RemoteGateStatePublishing:           {RemoteGateStateAwaitingRun, RemoteGateStateFailed},
	RemoteGateStateAwaitingRun:          {RemoteGateStateRunning, RemoteGateStateFailed},
	RemoteGateStateRunning:              {RemoteGateStatePassed, RemoteGateStateFailed},
	RemoteGateStatePassed:               {RemoteGateStateReconciled},
	RemoteGateStateFailed:               {RemoteGateStateReconciled},
}

// RemoteGateCandidate is the immutable dispatcher-owned identity adopted from
// a completed worker candidate.
type RemoteGateCandidate struct {
	Key          string
	BeadID       string
	AssignmentID int64
	CandidateSHA string
	BaseSHA      string
	TargetBranch string
	AdoptionRef  string
}

// RemoteGate is one durable remote-gate record.
type RemoteGate struct {
	ID        int64
	Candidate RemoteGateCandidate
	State     RemoteGateState
	Version   int64
}

// PresubmitResult is one exact local validation result for a durable gate.
// The candidate identity is duplicated deliberately so stale evidence cannot
// become valid merely because an action name matches a newer candidate.
type PresubmitResult struct {
	GateID        int64
	ActionName    string
	CandidateSHA  string
	BaseSHA       string
	Command       string
	Profile       string
	ToolHash      string
	StartedAt     string
	CompletedAt   string
	Outcome       string
	Logs          string
	ResourceClass string
}

// Store persists dispatcher-owned remote gate state and evidence.
type Store struct {
	db *sql.DB
}

// NewStore opens the remote gate persistence boundary and ensures its schema.
func NewStore(ctx context.Context, db *sql.DB) (*Store, error) {
	if db == nil {
		return nil, errors.New("open remote gate store: db is nil")
	}
	if _, err := db.ExecContext(ctx, remoteGateSchemaDDL); err != nil {
		return nil, fmt.Errorf("open remote gate store: migrate schema: %w", err)
	}
	return &Store{db: db}, nil
}

// AdoptCandidate creates or reuses the durable record for an exact candidate.
//
//oro:testonly — wired into production by the candidate-adoption remote gate step.
func (s *Store) AdoptCandidate(ctx context.Context, candidate RemoteGateCandidate) (RemoteGate, error) {
	if s == nil || s.db == nil {
		return RemoteGate{}, errors.New("adopt remote gate candidate: store is nil")
	}
	if err := validateRemoteGateCandidate(candidate); err != nil {
		return RemoteGate{}, err
	}
	if _, err := s.db.ExecContext(ctx, `
INSERT INTO remote_gates (gate_key, bead_id, assignment_id, candidate_sha, base_sha, target_branch, adoption_ref, state)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(gate_key) DO NOTHING`,
		candidate.Key, candidate.BeadID, candidate.AssignmentID, candidate.CandidateSHA,
		candidate.BaseSHA, candidate.TargetBranch, candidate.AdoptionRef, RemoteGateStateCandidateAdopted); err != nil {
		return RemoteGate{}, fmt.Errorf("adopt remote gate candidate: insert: %w", err)
	}
	gate, err := s.remoteGateByKey(ctx, candidate.Key)
	if err != nil {
		return RemoteGate{}, fmt.Errorf("adopt remote gate candidate: load: %w", err)
	}
	if gate.Candidate != candidate {
		return RemoteGate{}, fmt.Errorf("adopt remote gate candidate: key %q belongs to a different candidate", candidate.Key)
	}
	return gate, nil
}

// RemoteGate loads one durable remote gate record.
func (s *Store) RemoteGate(ctx context.Context, id int64) (RemoteGate, error) {
	if s == nil || s.db == nil {
		return RemoteGate{}, errors.New("load remote gate: store is nil")
	}
	if id <= 0 {
		return RemoteGate{}, fmt.Errorf("load remote gate: invalid id %d", id)
	}
	return scanRemoteGate(s.db.QueryRowContext(ctx, remoteGateSelect+` WHERE id = ?`, id))
}

// AdvanceRemoteGate moves a durable record with compare-and-swap semantics.
// Repeating an already-completed transition is idempotent.
func (s *Store) AdvanceRemoteGate(ctx context.Context, id int64, from, to RemoteGateState) (RemoteGate, error) {
	if s == nil || s.db == nil {
		return RemoteGate{}, errors.New("advance remote gate: store is nil")
	}
	if id <= 0 || from == "" || to == "" {
		return RemoteGate{}, fmt.Errorf("advance remote gate: invalid transition %d %q -> %q", id, from, to)
	}
	if !validRemoteGateTransition(from, to) {
		return RemoteGate{}, fmt.Errorf("advance remote gate: invalid transition %q -> %q", from, to)
	}
	result, err := s.db.ExecContext(ctx, `
UPDATE remote_gates
SET state = ?, version = version + 1, updated_at = datetime('now')
WHERE id = ? AND state = ?`, to, id, from)
	if err != nil {
		return RemoteGate{}, fmt.Errorf("advance remote gate %d: %w", id, err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return RemoteGate{}, fmt.Errorf("advance remote gate %d: count rows: %w", id, err)
	}
	gate, err := s.RemoteGate(ctx, id)
	if err != nil {
		return RemoteGate{}, err
	}
	if changed == 1 || gate.State == to {
		return gate, nil
	}
	return RemoteGate{}, fmt.Errorf("advance remote gate %d from %q to %q: %w", id, from, to, ErrRemoteGateTransitionConflict)
}

// RecordPresubmitResult persists exact local presubmit evidence. Repeating the
// same completion is idempotent; callers must reject nonmatching identities.
//
//oro:testonly — wired into production by the presubmit-evidence remote gate step.
func (s *Store) RecordPresubmitResult(ctx context.Context, result PresubmitResult) error {
	if s == nil || s.db == nil {
		return errors.New("record presubmit result: store is nil")
	}
	if err := validatePresubmitResult(result); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `
INSERT INTO remote_gate_presubmit_results
  (gate_id, action_name, candidate_sha, base_sha, command, profile, tool_hash, started_at, completed_at, outcome, logs, resource_class)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(gate_id, action_name, candidate_sha, base_sha, command, profile, tool_hash) DO NOTHING`,
		result.GateID, result.ActionName, result.CandidateSHA, result.BaseSHA, result.Command,
		result.Profile, result.ToolHash, result.StartedAt, result.CompletedAt, result.Outcome,
		result.Logs, result.ResourceClass); err != nil {
		return fmt.Errorf("record presubmit result: %w", err)
	}
	return nil
}

const remoteGateSelect = `
SELECT id, gate_key, bead_id, assignment_id, candidate_sha, base_sha, target_branch, adoption_ref, state, version
FROM remote_gates`

func (s *Store) remoteGateByKey(ctx context.Context, key string) (RemoteGate, error) {
	return scanRemoteGate(s.db.QueryRowContext(ctx, remoteGateSelect+` WHERE gate_key = ?`, key))
}

func scanRemoteGate(row *sql.Row) (RemoteGate, error) {
	var gate RemoteGate
	if err := row.Scan(&gate.ID, &gate.Candidate.Key, &gate.Candidate.BeadID, &gate.Candidate.AssignmentID,
		&gate.Candidate.CandidateSHA, &gate.Candidate.BaseSHA, &gate.Candidate.TargetBranch,
		&gate.Candidate.AdoptionRef, &gate.State, &gate.Version); err != nil {
		return RemoteGate{}, fmt.Errorf("scan remote gate: %w", err)
	}
	return gate, nil
}

func validateRemoteGateCandidate(candidate RemoteGateCandidate) error {
	if candidate.AssignmentID <= 0 || strings.TrimSpace(candidate.Key) == "" || strings.TrimSpace(candidate.BeadID) == "" ||
		strings.TrimSpace(candidate.CandidateSHA) == "" || strings.TrimSpace(candidate.BaseSHA) == "" ||
		strings.TrimSpace(candidate.TargetBranch) == "" || strings.TrimSpace(candidate.AdoptionRef) == "" {
		return errors.New("adopt remote gate candidate: missing immutable identity")
	}
	return nil
}

func validatePresubmitResult(result PresubmitResult) error {
	if result.GateID <= 0 || strings.TrimSpace(result.ActionName) == "" || strings.TrimSpace(result.CandidateSHA) == "" ||
		strings.TrimSpace(result.BaseSHA) == "" || strings.TrimSpace(result.Command) == "" ||
		strings.TrimSpace(result.Profile) == "" || strings.TrimSpace(result.ToolHash) == "" ||
		strings.TrimSpace(result.StartedAt) == "" || strings.TrimSpace(result.CompletedAt) == "" ||
		strings.TrimSpace(result.Outcome) == "" || strings.TrimSpace(result.ResourceClass) == "" {
		return errors.New("record presubmit result: missing exact evidence identity")
	}
	return nil
}

func validRemoteGateTransition(from, to RemoteGateState) bool {
	if from == to {
		return true
	}
	for _, allowed := range remoteGateTransitions[from] {
		if to == allowed {
			return true
		}
	}
	return false
}
