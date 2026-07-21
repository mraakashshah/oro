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
