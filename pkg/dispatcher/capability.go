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
