package dispatcher //nolint:testpackage // white-box test needs the unexported proposal-store method

import (
	"context"
	"testing"
)

func TestDispatcherStoreWorkProposalReplaysStoredResponse(t *testing.T) {
	t.Parallel()

	d := &Dispatcher{db: newTestDB(t)}
	payload := WorkProposalPayload{
		ClientProposalID:  "proposal-1",
		AssignmentID:      1,
		WorkerID:          "worker-1",
		BeadID:            "bead-1",
		EvidenceRunID:     "evidence-1",
		Fingerprint:       "fingerprint-1",
		Kind:              "prerequisite",
		Summary:           "missing prerequisite",
		SuggestedPriority: 2,
	}
	if _, err := d.db.ExecContext(context.Background(), `
INSERT INTO evidence_runs (id, assignment_id, worker_id, bead_id, kind, status)
VALUES (?, ?, ?, ?, ?, ?)`,
		payload.EvidenceRunID, payload.AssignmentID, payload.WorkerID, payload.BeadID, "diagnostic", "completed"); err != nil {
		t.Fatalf("seed evidence run: %v", err)
	}

	stored, err := d.storeWorkProposal(context.Background(), payload)
	if err != nil {
		t.Fatalf("store proposal: %v", err)
	}
	replayed, err := d.storeWorkProposal(context.Background(), payload)
	if err != nil {
		t.Fatalf("replay proposal: %v", err)
	}
	if replayed != stored {
		t.Fatalf("replayed result = %+v, want exact stored result %+v", replayed, stored)
	}
}

func TestDispatcherStoreWorkProposalRejectsUnavailableDatabase(t *testing.T) {
	t.Parallel()

	if _, err := (*Dispatcher)(nil).storeWorkProposal(context.Background(), WorkProposalPayload{}); err == nil {
		t.Fatal("nil dispatcher storeWorkProposal error = nil")
	}

	d := &Dispatcher{db: newTestDB(t)}
	if err := d.db.Close(); err != nil {
		t.Fatalf("close dispatcher database: %v", err)
	}
	if _, err := d.storeWorkProposal(context.Background(), WorkProposalPayload{
		ClientProposalID: "proposal-closed-db",
		AssignmentID:     1,
		WorkerID:         "worker-1",
		BeadID:           "bead-1",
		EvidenceRunID:    "evidence-1",
		Fingerprint:      "fingerprint-1",
		Kind:             "prerequisite",
		Summary:          "closed database",
	}); err == nil {
		t.Fatal("closed dispatcher database storeWorkProposal error = nil")
	}
}
