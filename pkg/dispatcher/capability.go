package dispatcher

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"oro/pkg/protocol"
)

const (
	assignmentCapabilityLifetime = 20 * time.Minute
	capabilityRefreshLead        = 5 * time.Minute
)

// ErrAssignmentCapabilityNonceConflict reports reuse of a consumed nonce with
// request content different from the original request.
var ErrAssignmentCapabilityNonceConflict = errors.New("assignment capability nonce conflict")

// ActorRole scopes the work an assignment capability may authorize.
type ActorRole string

// Supported assignment capability actor roles.
const (
	ActorRoleExecutionWorker    ActorRole = "execution_worker"
	ActorRoleEpicDecomposition  ActorRole = "epic_decomposition_worker"
	ActorRoleDispatcher         ActorRole = "dispatcher"
	ActorRoleOpsTaskcraftReview ActorRole = "ops_taskcraft_reviewer"
	ActorRoleHumanCLI           ActorRole = "human_cli"
)

// AssignmentCapability is the in-memory bearer credential for an assignment.
// Token is deliberately omitted from every durable representation.
type AssignmentCapability struct {
	ID           string
	AssignmentID int64
	Generation   int64
	Role         ActorRole
	Token        string
	ExpiresAt    time.Time
}

// issueAssignmentCapability mints and durably records a capability only after
// its assignment exists. The caller receives the raw token exactly once; the
// database retains only its SHA-256 hash.
func (d *Dispatcher) issueAssignmentCapability(
	ctx context.Context,
	assignmentID, generation int64,
	role ActorRole,
) (AssignmentCapability, error) {
	return d.issueAssignmentCapabilityWithState(ctx, assignmentID, generation, role, "active")
}

func (d *Dispatcher) issueAssignmentCapabilityWithState(
	ctx context.Context,
	assignmentID, generation int64,
	role ActorRole,
	state string,
) (AssignmentCapability, error) {
	if d == nil || d.db == nil {
		return AssignmentCapability{}, errors.New("issue assignment capability: database is nil")
	}
	if assignmentID <= 0 || generation <= 0 || role == "" {
		return AssignmentCapability{}, errors.New("issue assignment capability: invalid capability identity")
	}

	capabilityID, err := randomCapabilityValue(16)
	if err != nil {
		return AssignmentCapability{}, fmt.Errorf("generate capability id: %w", err)
	}
	token, err := randomCapabilityValue(32)
	if err != nil {
		return AssignmentCapability{}, fmt.Errorf("generate capability token: %w", err)
	}
	expiresAt := d.nowFunc().UTC().Add(assignmentCapabilityLifetime)
	tokenSum := sha256.Sum256([]byte(token))

	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return AssignmentCapability{}, fmt.Errorf("begin capability transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM assignments WHERE id = ?`, assignmentID).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return AssignmentCapability{}, fmt.Errorf("issue assignment capability: assignment %d not found", assignmentID)
		}
		return AssignmentCapability{}, fmt.Errorf("verify assignment for capability: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO assignment_capabilities (
  capability_id, assignment_id, generation, role, token_hash, expires_at, state
	) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		capabilityID, assignmentID, generation, string(role), hex.EncodeToString(tokenSum[:]), expiresAt.Format(time.RFC3339Nano), state); err != nil {
		return AssignmentCapability{}, fmt.Errorf("persist assignment capability: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return AssignmentCapability{}, fmt.Errorf("commit assignment capability: %w", err)
	}

	return AssignmentCapability{
		ID:           capabilityID,
		AssignmentID: assignmentID,
		Generation:   generation,
		Role:         role,
		Token:        token,
		ExpiresAt:    expiresAt,
	}, nil
}

// refreshExpiringCapabilities delivers durable pending replacements for live
// assignments. A persisted pending token cannot be recovered after restart, so
// a later sweep supersedes it and mints a new bearer before delivery.
func (d *Dispatcher) refreshExpiringCapabilities(ctx context.Context, now time.Time) error {
	if d == nil || d.db == nil {
		return errors.New("refresh assignment capabilities: database is nil")
	}
	d.mu.Lock()
	workers := make([]*trackedWorker, 0, len(d.workers))
	for _, worker := range d.workers {
		if worker.state == protocol.WorkerBusy && worker.assignmentID > 0 && worker.execution.ActorRole != "" {
			workers = append(workers, worker)
		}
	}
	d.mu.Unlock()

	for _, worker := range workers {
		capability, err := d.refreshCapabilityForWorker(ctx, worker, now)
		if err != nil || capability.ID == "" {
			if err != nil {
				return err
			}
			continue
		}
		d.mu.Lock()
		current := d.workers[worker.id]
		if current != nil && current.assignmentID == capability.AssignmentID {
			_ = d.sendToWorker(current, protocol.Message{Type: protocol.MsgCapabilityRefresh, CapabilityRefresh: &protocol.CapabilityRefreshPayload{
				AssignmentID: capability.AssignmentID, Generation: capability.Generation, CapabilityID: capability.ID, Capability: capability.Token, ExpiresAt: capability.ExpiresAt,
			}})
		}
		d.mu.Unlock()
	}
	return nil
}

func (d *Dispatcher) refreshCapabilityForWorker(ctx context.Context, worker *trackedWorker, now time.Time) (AssignmentCapability, error) {
	var expiresAt string
	var activeID string
	err := d.db.QueryRowContext(ctx, `SELECT capability_id, expires_at FROM assignment_capabilities WHERE assignment_id=? AND generation=? AND role=? AND state='active' ORDER BY created_at DESC LIMIT 1`, worker.assignmentID, worker.execution.Generation, worker.execution.ActorRole).Scan(&activeID, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return AssignmentCapability{}, nil
	}
	if err != nil {
		return AssignmentCapability{}, fmt.Errorf("load active assignment capability: %w", err)
	}
	expires, err := time.Parse(time.RFC3339Nano, expiresAt)
	if err != nil {
		return AssignmentCapability{}, fmt.Errorf("parse capability expiry: %w", err)
	}
	if expires.After(now.Add(capabilityRefreshLead)) {
		return AssignmentCapability{}, nil
	}
	if _, err := d.db.ExecContext(ctx, `UPDATE assignment_capabilities SET state='superseded', superseded_at=? WHERE assignment_id=? AND state='pending'`, now.UTC().Format(time.RFC3339Nano), worker.assignmentID); err != nil {
		return AssignmentCapability{}, fmt.Errorf("supersede pending capability: %w", err)
	}
	capability, err := d.issueAssignmentCapabilityWithState(ctx, worker.assignmentID, worker.execution.Generation, ActorRole(worker.execution.ActorRole), "pending")
	if err != nil {
		return AssignmentCapability{}, err
	}
	if _, err := d.db.ExecContext(ctx, `UPDATE assignment_capabilities SET pending_replacement_id=? WHERE capability_id=?`, capability.ID, activeID); err != nil {
		return AssignmentCapability{}, fmt.Errorf("link capability replacement: %w", err)
	}
	return capability, nil
}

func (d *Dispatcher) handleCapabilityRefreshAck(ctx context.Context, workerID string, msg protocol.Message) {
	ack := msg.CapabilityRefreshACK
	if ack == nil || ack.AssignmentID <= 0 || ack.CapabilityID == "" {
		return
	}
	d.mu.Lock()
	worker := d.workers[workerID]
	valid := worker != nil && worker.assignmentID == ack.AssignmentID
	d.mu.Unlock()
	if !valid {
		return
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return
	}
	defer func() { _ = tx.Rollback() }()
	var predecessor string
	if err := tx.QueryRowContext(ctx, `SELECT capability_id FROM assignment_capabilities WHERE assignment_id=? AND pending_replacement_id=? AND state='active'`, ack.AssignmentID, ack.CapabilityID).Scan(&predecessor); err != nil {
		return
	}
	if _, err := tx.ExecContext(ctx, `UPDATE assignment_capabilities SET state='active', acknowledged_at=? WHERE capability_id=? AND state='pending'`, d.nowFunc().UTC().Format(time.RFC3339Nano), ack.CapabilityID); err != nil {
		return
	}
	if _, err := tx.ExecContext(ctx, `UPDATE assignment_capabilities SET state='revoked', revoked_at=? WHERE capability_id=?`, d.nowFunc().UTC().Format(time.RFC3339Nano), predecessor); err != nil {
		return
	}
	_ = tx.Commit()
}

func (d *Dispatcher) recordAssignmentCapabilityNonce(
	ctx context.Context,
	capabilityID, nonce string,
	request, response []byte,
) ([]byte, error) {
	if d == nil || d.db == nil {
		return nil, errors.New("record assignment capability nonce: database is nil")
	}
	if capabilityID == "" || nonce == "" || len(request) == 0 {
		return nil, errors.New("record assignment capability nonce: invalid identity")
	}

	requestSum := sha256.Sum256(request)
	requestHash := hex.EncodeToString(requestSum[:])
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin assignment capability nonce transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `
INSERT INTO assignment_capability_nonces (capability_id, nonce, request_hash, response)
VALUES (?, ?, ?, ?)
ON CONFLICT(capability_id, nonce) DO NOTHING`,
		capabilityID, nonce, requestHash, string(response)); err != nil {
		return nil, fmt.Errorf("persist assignment capability nonce: %w", err)
	}

	var storedRequestHash, storedResponse string
	if err := tx.QueryRowContext(ctx, `
SELECT request_hash, response
FROM assignment_capability_nonces
WHERE capability_id = ? AND nonce = ?`, capabilityID, nonce,
	).Scan(&storedRequestHash, &storedResponse); err != nil {
		return nil, fmt.Errorf("load assignment capability nonce: %w", err)
	}
	if storedRequestHash != requestHash {
		return nil, fmt.Errorf("record assignment capability nonce %q: %w", nonce, ErrAssignmentCapabilityNonceConflict)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit assignment capability nonce: %w", err)
	}
	return []byte(storedResponse), nil
}

func randomCapabilityValue(bytes int) (string, error) {
	value := make([]byte, bytes)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("read random capability bytes: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}
