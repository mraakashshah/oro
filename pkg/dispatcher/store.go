package dispatcher

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const maxPresubmitLogBytes = 64 * 1024

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
	ResourceClass ResourceClass
}

// PresubmitEvidencePlan is the complete exact identity required to admit a
// local presubmit plan. Actions not represented by matching terminal evidence
// keep the plan non-passing.
//
//oro:testonly — wired into production by the post-rebase invalidation step.
type PresubmitEvidencePlan struct {
	GateID       int64
	CandidateSHA string
	BaseSHA      string
	Profile      string
	ToolHash     string
	Actions      []PresubmitAction
}

// Store persists dispatcher-owned remote gate state and evidence.
type Store struct {
	db *sql.DB
}

// advanceRemoteGate advances dispatcher-owned candidate state without relying
// on the worker that originally produced the candidate.
func (d *Dispatcher) advanceRemoteGate(ctx context.Context, gateID int64, from, to RemoteGateState) (RemoteGate, error) {
	if d == nil || d.remoteGates == nil {
		return RemoteGate{}, errors.New("advance remote gate: store is unavailable")
	}
	return d.remoteGates.AdvanceRemoteGate(ctx, gateID, from, to)
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
	logs := result.Logs
	if len(logs) > maxPresubmitLogBytes {
		logs = logs[:maxPresubmitLogBytes]
	}
	if _, err := s.db.ExecContext(ctx, `
INSERT INTO remote_gate_presubmit_results
  (gate_id, action_name, candidate_sha, base_sha, command, profile, tool_hash, started_at, completed_at, outcome, logs, resource_class)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(gate_id, action_name, candidate_sha, base_sha, command, profile, tool_hash) DO NOTHING`,
		result.GateID, result.ActionName, result.CandidateSHA, result.BaseSHA, result.Command,
		result.Profile, result.ToolHash, result.StartedAt, result.CompletedAt, result.Outcome,
		logs, result.ResourceClass); err != nil {
		return fmt.Errorf("record presubmit result: %w", err)
	}
	return nil
}

// PresubmitPlanPassed reports whether every action in plan has exact, terminal
// passing evidence for the durable candidate. Stale and non-passing rows are
// retained for audit but never satisfy the current plan.
//
//oro:testonly — wired into production by the post-rebase invalidation step.
func (s *Store) PresubmitPlanPassed(ctx context.Context, plan PresubmitEvidencePlan) (bool, error) {
	if s == nil || s.db == nil {
		return false, errors.New("check presubmit plan: store is nil")
	}
	if err := validatePresubmitEvidencePlan(plan); err != nil {
		return false, err
	}
	gate, err := s.RemoteGate(ctx, plan.GateID)
	if err != nil {
		return false, fmt.Errorf("check presubmit plan: load gate: %w", err)
	}
	if gate.Candidate.CandidateSHA != plan.CandidateSHA || gate.Candidate.BaseSHA != plan.BaseSHA {
		return false, nil
	}

	for _, action := range plan.Actions {
		var startedAt, completedAt, outcome string
		err := s.db.QueryRowContext(ctx, `
SELECT started_at, completed_at, outcome
FROM remote_gate_presubmit_results
WHERE gate_id = ?
  AND action_name = ?
  AND candidate_sha = ?
  AND base_sha = ?
  AND command = ?
  AND profile = ?
  AND tool_hash = ?
  AND resource_class = ?`,
			plan.GateID, action.Name, plan.CandidateSHA, plan.BaseSHA, action.Command,
			plan.Profile, plan.ToolHash, action.ResourceClass).Scan(&startedAt, &completedAt, &outcome)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("check presubmit action %q: %w", action.Name, err)
		}
		if outcome != "passed" || !validPresubmitTimestamps(startedAt, completedAt) {
			return false, nil
		}
	}
	return true, nil
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
		strings.TrimSpace(result.Outcome) == "" || strings.TrimSpace(string(result.ResourceClass)) == "" {
		return errors.New("record presubmit result: missing exact evidence identity")
	}
	if err := validatePresubmitTimestamps(result.StartedAt, result.CompletedAt); err != nil {
		return fmt.Errorf("record presubmit result: %w", err)
	}
	return nil
}

func validatePresubmitEvidencePlan(plan PresubmitEvidencePlan) error {
	if plan.GateID <= 0 || strings.TrimSpace(plan.CandidateSHA) == "" || strings.TrimSpace(plan.BaseSHA) == "" ||
		strings.TrimSpace(plan.Profile) == "" || strings.TrimSpace(plan.ToolHash) == "" || len(plan.Actions) == 0 {
		return errors.New("check presubmit plan: missing exact plan identity")
	}
	seen := make(map[string]struct{}, len(plan.Actions))
	for _, action := range plan.Actions {
		if strings.TrimSpace(action.Name) == "" || strings.TrimSpace(action.Command) == "" || action.ResourceClass == "" {
			return errors.New("check presubmit plan: incomplete action identity")
		}
		if _, exists := seen[action.Name]; exists {
			return fmt.Errorf("check presubmit plan: duplicate action %q", action.Name)
		}
		seen[action.Name] = struct{}{}
	}
	return nil
}

func validatePresubmitTimestamps(startedAt, completedAt string) error {
	started, err := time.Parse(time.RFC3339Nano, startedAt)
	if err != nil {
		return fmt.Errorf("invalid started_at: %w", err)
	}
	completed, err := time.Parse(time.RFC3339Nano, completedAt)
	if err != nil {
		return fmt.Errorf("invalid completed_at: %w", err)
	}
	if completed.Before(started) {
		return errors.New("completed_at precedes started_at")
	}
	return nil
}

func validPresubmitTimestamps(startedAt, completedAt string) bool {
	return validatePresubmitTimestamps(startedAt, completedAt) == nil
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
