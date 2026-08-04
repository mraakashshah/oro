package dispatcher //nolint:testpackage // white-box test exercises unexported capability persistence helpers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"oro/pkg/protocol"
)

func TestAssignBeadIssuesPersistedCapability(t *testing.T) {
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()
	const beadID = "capability-wire-bead"

	beadSrc.mu.Lock()
	beadSrc.shown[beadID] = &protocol.BeadDetail{
		ID:                 beadID,
		Title:              "Capability wire bead",
		AcceptanceCriteria: "Test: capability wire | Assert: bearer is delivered",
		Status:             "open",
	}
	beadSrc.mu.Unlock()

	worker := &trackedWorker{
		id:    "capability-wire-worker",
		state: protocol.WorkerIdle,
		conn:  newMockConn(),
	}
	d.mu.Lock()
	d.workers[worker.id] = worker
	d.mu.Unlock()

	if err := d.assignBead(ctx, worker, protocol.Bead{
		ID:       beadID,
		Title:    "Capability wire bead",
		Status:   "open",
		Type:     "task",
		Priority: 1,
	}); err != nil {
		t.Fatalf("assign bead: %v", err)
	}

	msg := lastMockConnMessage(t, worker.conn.(*mockConn))
	if msg.Type != protocol.MsgAssign || msg.Assign == nil {
		t.Fatalf("worker message = %#v, want ASSIGN", msg)
	}
	if msg.Assign.Capability == "" {
		t.Fatal("ASSIGN capability is empty")
	}

	var tokenHash, role, expiresAt string
	var generation int64
	if err := d.db.QueryRowContext(ctx, `
SELECT token_hash, role, generation, expires_at
FROM assignment_capabilities
WHERE assignment_id = ?`, msg.Assign.AssignmentID,
	).Scan(&tokenHash, &role, &generation, &expiresAt); err != nil {
		t.Fatalf("load assignment capability: %v", err)
	}
	wantHash := sha256.Sum256([]byte(msg.Assign.Capability))
	if tokenHash != hex.EncodeToString(wantHash[:]) {
		t.Fatalf("persisted token hash = %q, want hash of delivered capability", tokenHash)
	}
	if role != msg.Assign.ActorRole || generation != msg.Assign.Generation {
		t.Fatalf("persisted identity = (%q, %d), payload = (%q, %d)",
			role, generation, msg.Assign.ActorRole, msg.Assign.Generation)
	}
	if _, err := time.Parse(time.RFC3339Nano, expiresAt); err != nil {
		t.Fatalf("persisted expiry %q is not RFC3339Nano: %v", expiresAt, err)
	}
}

func TestIssueAssignmentCapabilityPersistsHashWithoutBearerToken(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	assignmentID, err := d.createAssignment(ctx, "capability-bead", "capability-worker", "/tmp/capability")
	if err != nil {
		t.Fatalf("create assignment: %v", err)
	}

	capability, err := d.issueAssignmentCapability(ctx, assignmentID, 1, ActorRoleExecutionWorker)
	if err != nil {
		t.Fatalf("issue assignment capability: %v", err)
	}
	if capability.Token == "" {
		t.Fatal("issued capability token is empty")
	}
	if capability.ExpiresAt.Before(time.Now().UTC().Add(19 * time.Minute)) {
		t.Fatalf("capability expiry %s is too soon", capability.ExpiresAt)
	}

	var tokenHash string
	if err := d.db.QueryRowContext(ctx, `
SELECT token_hash FROM assignment_capabilities WHERE capability_id = ?`, capability.ID,
	).Scan(&tokenHash); err != nil {
		t.Fatalf("load persisted capability: %v", err)
	}
	if tokenHash == capability.Token || strings.Contains(tokenHash, capability.Token) {
		t.Fatal("raw bearer token persisted instead of a hash")
	}
	if tokenHash == "" {
		t.Fatal("persisted token hash is empty")
	}
}

func TestIssueAssignmentCapabilityRejectsMissingAssignmentWithoutPersistingCapability(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	if _, err := d.issueAssignmentCapability(ctx, 999, 1, ActorRoleExecutionWorker); err == nil {
		t.Fatal("issue capability for missing assignment succeeded")
	}

	var count int
	if err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM assignment_capabilities`).Scan(&count); err != nil {
		t.Fatalf("count capabilities: %v", err)
	}
	if count != 0 {
		t.Fatalf("persisted capabilities after missing assignment = %d, want 0", count)
	}
}

func TestIssueAssignmentCapabilityHonorsAssignmentAdmission(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	assignmentID, err := d.createAssignment(t.Context(), "capability-admission-bead", "capability-admission-worker", t.TempDir())
	if err != nil {
		t.Fatalf("create assignment: %v", err)
	}

	d.assignmentAdmissionMu.Lock()
	result := make(chan error, 1)
	go func() {
		_, issueErr := d.issueAssignmentCapability(t.Context(), assignmentID, 1, ActorRoleExecutionWorker)
		result <- issueErr
	}()

	select {
	case issueErr := <-result:
		d.assignmentAdmissionMu.Unlock()
		t.Fatalf("capability issuance bypassed assignment admission: %v", issueErr)
	case <-time.After(100 * time.Millisecond):
	}
	d.assignmentAdmissionMu.Unlock()

	select {
	case issueErr := <-result:
		if issueErr != nil {
			t.Fatalf("issue capability after assignment admission released: %v", issueErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("capability issuance remained blocked after assignment admission released")
	}
}

func TestRecordAssignmentCapabilityNonceReplaysStoredResponseAndRejectsDifferentContent(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	assignmentID, err := d.createAssignment(ctx, "nonce-bead", "nonce-worker", "/tmp/nonce")
	if err != nil {
		t.Fatalf("create assignment: %v", err)
	}
	capability, err := d.issueAssignmentCapability(ctx, assignmentID, 1, ActorRoleExecutionWorker)
	if err != nil {
		t.Fatalf("issue assignment capability: %v", err)
	}

	request := []byte(`{"action":"propose","bead":"nonce-bead"}`)
	wantResponse := []byte(`{"proposal_id":"proposal-1"}`)
	got, err := d.recordAssignmentCapabilityNonce(ctx, capability.ID, "nonce-1", request, wantResponse)
	if err != nil {
		t.Fatalf("record nonce response: %v", err)
	}
	if string(got) != string(wantResponse) {
		t.Fatalf("first response = %q, want %q", got, wantResponse)
	}

	replayed, err := d.recordAssignmentCapabilityNonce(
		ctx,
		capability.ID,
		"nonce-1",
		request,
		[]byte(`{"proposal_id":"must-not-replace"}`),
	)
	if err != nil {
		t.Fatalf("replay nonce response: %v", err)
	}
	if string(replayed) != string(wantResponse) {
		t.Fatalf("replayed response = %q, want stored %q", replayed, wantResponse)
	}

	_, err = d.recordAssignmentCapabilityNonce(
		ctx,
		capability.ID,
		"nonce-1",
		[]byte(`{"action":"different"}`),
		[]byte(`{"proposal_id":"proposal-2"}`),
	)
	if !errors.Is(err, ErrAssignmentCapabilityNonceConflict) {
		t.Fatalf("different-content replay error = %v, want ErrAssignmentCapabilityNonceConflict", err)
	}
}
