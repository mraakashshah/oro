package protocol_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"oro/pkg/dbutil"
	"oro/pkg/protocol"
)

func TestWorkProposalPersistenceAndSubmissionReplay(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "proposals.db")
	store := openWorkProposalStore(ctx, t, path)

	first := protocol.WorkProposalPayload{
		ClientProposalID:  "client-proposal-1",
		AssignmentID:      17,
		WorkerID:          "worker-a",
		BeadID:            "bead-a",
		EvidenceRunID:     "evidence-a",
		Fingerprint:       "same-fingerprint",
		ScopeHint:         "first provisional scope",
		Kind:              "prerequisite",
		Summary:           "first blocker",
		SuggestedTitle:    "First blocker",
		SuggestedType:     "task",
		SuggestedPriority: 2,
	}
	if _, err := store.StoreWorkProposal(ctx, first); !errors.Is(err, protocol.ErrWorkProposalEvidenceNotFound) {
		t.Fatalf("proposal without evidence error = %v, want ErrWorkProposalEvidenceNotFound", err)
	}
	if err := store.StoreEvidenceRun(ctx, protocol.EvidenceRun{
		ID:           first.EvidenceRunID,
		AssignmentID: first.AssignmentID,
		WorkerID:     first.WorkerID,
		BeadID:       first.BeadID,
		Kind:         "diagnostic",
		Status:       "completed",
	}); err != nil {
		t.Fatalf("store evidence run: %v", err)
	}
	stored, err := store.StoreWorkProposal(ctx, first)
	if err != nil {
		t.Fatalf("store first proposal: %v", err)
	}
	replayed, err := store.StoreWorkProposal(ctx, first)
	if err != nil {
		t.Fatalf("replay first proposal: %v", err)
	}
	if replayed != stored {
		t.Fatalf("replay = %+v, want exact stored response %+v", replayed, stored)
	}

	conflicting := first
	conflicting.Summary = "different content"
	if _, err := store.StoreWorkProposal(ctx, conflicting); !errors.Is(err, protocol.ErrWorkProposalSubmissionConflict) {
		t.Fatalf("conflicting replay error = %v, want ErrWorkProposalSubmissionConflict", err)
	}

	second := first
	second.ClientProposalID = "client-proposal-2"
	second.ScopeHint = "second provisional scope"
	second.Summary = "second blocker"
	secondStored, err := store.StoreWorkProposal(ctx, second)
	if err != nil {
		t.Fatalf("store second provisional proposal: %v", err)
	}
	if secondStored.ProposalID == stored.ProposalID {
		t.Fatalf("distinct provisional proposals collapsed to %q", stored.ProposalID)
	}
	if got := proposalCount(ctx, t, store.DB(), first.AssignmentID); got != 2 {
		t.Fatalf("provisional proposal count = %d, want 2", got)
	}

	if err := store.Close(); err != nil {
		t.Fatalf("close initial store: %v", err)
	}
	reopened := openWorkProposalStore(ctx, t, path)
	afterRestart, err := reopened.StoreWorkProposal(ctx, first)
	if err != nil {
		t.Fatalf("replay after restart: %v", err)
	}
	if afterRestart != stored {
		t.Fatalf("restart replay = %+v, want exact stored response %+v", afterRestart, stored)
	}
}

func openWorkProposalStore(ctx context.Context, t *testing.T, path string) *protocol.WorkProposalStore {
	t.Helper()
	db, err := dbutil.OpenDB(path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if _, err := db.ExecContext(ctx, protocol.SchemaDDL); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	store, err := protocol.NewWorkProposalStore(db)
	if err != nil {
		t.Fatalf("new work proposal store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func proposalCount(ctx context.Context, t *testing.T, db *sql.DB, assignmentID int64) int {
	t.Helper()
	var count int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM work_proposals WHERE assignment_id = ?", assignmentID,
	).Scan(&count); err != nil {
		t.Fatalf("count proposals: %v", err)
	}
	return count
}
