package protocol_test

import (
	"context"
	"testing"

	"oro/pkg/dbutil"
	"oro/pkg/protocol"
)

func TestAssignmentCapabilityPersistence(t *testing.T) {
	ctx := context.Background()
	dbPath := t.TempDir() + "/capabilities.sqlite"

	db, err := dbutil.OpenDB(dbPath)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	if _, err := db.ExecContext(ctx, protocol.SchemaDDL); err != nil {
		_ = db.Close()
		t.Fatalf("initialize schema: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO assignments (id, bead_id, worker_id, worktree) VALUES (?, ?, ?, ?)`,
		17, "capability-bead", "capability-worker", "/tmp/capability"); err != nil {
		_ = db.Close()
		t.Fatalf("seed assignment: %v", err)
	}

	const rawToken = "never-persist-this-bearer-token"
	if _, err := db.ExecContext(ctx, `
INSERT INTO assignment_capabilities (
  capability_id, assignment_id, generation, role, token_hash, expires_at, state, pending_replacement_id
) VALUES (?, ?, ?, ?, ?, ?, ?, ?), (?, ?, ?, ?, ?, ?, ?, ?), (?, ?, ?, ?, ?, ?, ?, ?);`,
		"cap-active", 17, 3, "execution_worker", "hash-active", "2030-01-02T03:04:05Z", "active", nil,
		"cap-pending", 17, 4, "execution_worker", "hash-pending", "2030-01-02T03:09:05Z", "pending", nil,
		"cap-superseded", 17, 2, "execution_worker", "hash-old", "2030-01-02T02:04:05Z", "superseded", "cap-pending",
	); err != nil {
		_ = db.Close()
		t.Fatalf("seed capability persistence state: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO assignment_capability_nonces (
  capability_id, nonce, request_hash, response
) VALUES (?, ?, ?, ?)`, "cap-active", "nonce-1", "request-hash", `{"result":"stored"}`); err != nil {
		_ = db.Close()
		t.Fatalf("seed consumed nonce: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close initial database: %v", err)
	}

	reopened, err := dbutil.OpenDB(dbPath)
	if err != nil {
		t.Fatalf("reopen database: %v", err)
	}
	defer func() { _ = reopened.Close() }()

	var activeHash, activeRole, activeExpiry, pendingState, supersededBy, response string
	var generation int64
	if err := reopened.QueryRowContext(ctx, `
SELECT token_hash, role, generation, expires_at
FROM assignment_capabilities
WHERE capability_id = ?`, "cap-active",
	).Scan(&activeHash, &activeRole, &generation, &activeExpiry); err != nil {
		t.Fatalf("load active capability: %v", err)
	}
	if activeHash != "hash-active" || activeRole != "execution_worker" || generation != 3 || activeExpiry != "2030-01-02T03:04:05Z" {
		t.Fatalf("active capability = hash %q role %q generation %d expiry %q",
			activeHash, activeRole, generation, activeExpiry)
	}
	if err := reopened.QueryRowContext(ctx, `
SELECT state FROM assignment_capabilities WHERE capability_id = ?`, "cap-pending",
	).Scan(&pendingState); err != nil {
		t.Fatalf("load pending capability: %v", err)
	}
	if pendingState != "pending" {
		t.Fatalf("pending capability state = %q, want pending", pendingState)
	}
	if err := reopened.QueryRowContext(ctx, `
SELECT pending_replacement_id FROM assignment_capabilities WHERE capability_id = ?`, "cap-superseded",
	).Scan(&supersededBy); err != nil {
		t.Fatalf("load supersession: %v", err)
	}
	if supersededBy != "cap-pending" {
		t.Fatalf("supersession target = %q, want cap-pending", supersededBy)
	}
	if err := reopened.QueryRowContext(ctx, `
SELECT response FROM assignment_capability_nonces WHERE capability_id = ? AND nonce = ?`, "cap-active", "nonce-1",
	).Scan(&response); err != nil {
		t.Fatalf("load consumed nonce response: %v", err)
	}
	if response != `{"result":"stored"}` {
		t.Fatalf("nonce response = %q", response)
	}

	var rawTokenRows int
	if err := reopened.QueryRowContext(ctx, `
SELECT COUNT(*) FROM assignment_capabilities WHERE token_hash = ?`, rawToken,
	).Scan(&rawTokenRows); err != nil {
		t.Fatalf("search capabilities for raw token: %v", err)
	}
	if rawTokenRows != 0 {
		t.Fatalf("raw token found in persisted capability data")
	}
}
