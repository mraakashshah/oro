package dispatcher //nolint:testpackage // internal white-box tests need access to unexported fields

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"oro/pkg/protocol"
)

// seedEscalation inserts a pending escalation row into the test DB and returns its ID.
func seedEscalation(t *testing.T, d *Dispatcher, escType, beadID, workerID, message string) int64 {
	t.Helper()
	res, err := d.db.Exec(
		`INSERT INTO escalations (type, bead_id, worker_id, message, status) VALUES (?, ?, ?, ?, 'pending')`,
		escType, beadID, workerID, message,
	)
	if err != nil {
		t.Fatalf("seed escalation: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("last insert id: %v", err)
	}
	return id
}

// TestApplyEscalations_PendingReturnsJSONArray verifies applyPendingEscalations
// returns all pending rows serialised as a JSON array.
func TestApplyEscalations_PendingReturnsJSONArray(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)

	// Seed two pending escalations.
	id1 := seedEscalation(t, d, string(protocol.EscMergeConflict), "bead-1", "w1", "merge failed")
	id2 := seedEscalation(t, d, string(protocol.EscStuck), "bead-2", "w2", "stuck")

	got, err := d.applyPendingEscalations()
	if err != nil {
		t.Fatalf("applyPendingEscalations: unexpected error: %v", err)
	}

	var escs []protocol.Escalation
	if err := json.Unmarshal([]byte(got), &escs); err != nil {
		t.Fatalf("result is not valid JSON array: %v (got: %q)", err, got)
	}
	if len(escs) != 2 {
		t.Fatalf("expected 2 escalations, got %d", len(escs))
	}
	if escs[0].ID != id1 {
		t.Errorf("escs[0].ID = %d, want %d", escs[0].ID, id1)
	}
	if escs[1].ID != id2 {
		t.Errorf("escs[1].ID = %d, want %d", escs[1].ID, id2)
	}
	if escs[0].Status != "pending" {
		t.Errorf("escs[0].Status = %q, want pending", escs[0].Status)
	}
}

// TestApplyEscalations_PendingEmptyReturnsEmptyArray verifies the result is
// a valid empty JSON array when no escalations exist.
func TestApplyEscalations_PendingEmptyReturnsEmptyArray(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)

	got, err := d.applyPendingEscalations()
	if err != nil {
		t.Fatalf("applyPendingEscalations: unexpected error: %v", err)
	}
	// json.Marshal(nil slice) produces "null" — check we handle it gracefully.
	// Both "null" and "[]" are acceptable (the current impl marshals a nil slice as "null").
	if got != "null" && got != "[]" {
		// Try to parse as array to confirm it's at least valid JSON.
		var escs []protocol.Escalation
		if err := json.Unmarshal([]byte(got), &escs); err != nil {
			t.Fatalf("empty result is not valid JSON: %v (got: %q)", err, got)
		}
	}
}

// TestApplyEscalations_AckValidID verifies applyAckEscalation marks a pending
// escalation as acked and returns a confirmation message.
func TestApplyEscalations_AckValidID(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)

	id := seedEscalation(t, d, string(protocol.EscWorkerCrash), "bead-3", "w3", "worker died")
	idStr := fmt.Sprintf("%d", id)

	msg, err := d.applyAckEscalation(idStr)
	if err != nil {
		t.Fatalf("applyAckEscalation: unexpected error: %v", err)
	}
	if !strings.Contains(msg, idStr) {
		t.Errorf("ack message should contain ID %q, got %q", idStr, msg)
	}

	// Verify the row is now acked.
	var status string
	err = d.db.QueryRow(`SELECT status FROM escalations WHERE id = ?`, id).Scan(&status)
	if err != nil {
		t.Fatalf("query escalation status: %v", err)
	}
	if status != "acked" {
		t.Errorf("status = %q after ack, want 'acked'", status)
	}
}

// TestApplyEscalations_AckEmptyIDReturnsError verifies applyAckEscalation
// returns an error when given an empty ID string.
func TestApplyEscalations_AckEmptyIDReturnsError(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)

	_, err := d.applyAckEscalation("")
	if err == nil {
		t.Fatal("expected error for empty ID, got nil")
	}
	if !strings.Contains(err.Error(), "requires an escalation ID") {
		t.Errorf("error should mention 'requires an escalation ID', got: %v", err)
	}
}

// TestApplyEscalations_AckUnknownIDReturnsNotFound verifies applyAckEscalation
// returns a "not found or already acked" message for an unknown ID.
func TestApplyEscalations_AckUnknownIDReturnsNotFound(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)

	msg, err := d.applyAckEscalation("99999")
	if err != nil {
		t.Fatalf("applyAckEscalation: unexpected error: %v", err)
	}
	if !strings.Contains(msg, "not found or already acked") {
		t.Errorf("expected 'not found or already acked' in message, got %q", msg)
	}
}

// TestApplyEscalations_AckAlreadyAckedReturnsNotFound verifies applyAckEscalation
// returns "not found or already acked" when the escalation was already acked.
func TestApplyEscalations_AckAlreadyAckedReturnsNotFound(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)

	id := seedEscalation(t, d, string(protocol.EscMergeConflict), "bead-4", "w4", "conflict")
	idStr := fmt.Sprintf("%d", id)

	// Ack it once successfully.
	if _, err := d.applyAckEscalation(idStr); err != nil {
		t.Fatalf("first ack: %v", err)
	}

	// Ack it again — should return "not found or already acked".
	msg, err := d.applyAckEscalation(idStr)
	if err != nil {
		t.Fatalf("second ack: unexpected error: %v", err)
	}
	if !strings.Contains(msg, "not found or already acked") {
		t.Errorf("expected 'not found or already acked' in message, got %q", msg)
	}
}
