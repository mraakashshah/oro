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
)

const assignmentCapabilityLifetime = 20 * time.Minute

// ActorRole scopes the work an assignment capability may authorize.
type ActorRole string

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
	expiresAt := time.Now().UTC().Add(assignmentCapabilityLifetime)
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
) VALUES (?, ?, ?, ?, ?, ?, 'active')`,
		capabilityID, assignmentID, generation, string(role), hex.EncodeToString(tokenSum[:]), expiresAt.Format(time.RFC3339Nano)); err != nil {
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

func randomCapabilityValue(bytes int) (string, error) {
	value := make([]byte, bytes)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}
