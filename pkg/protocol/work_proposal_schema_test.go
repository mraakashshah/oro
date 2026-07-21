package protocol_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
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

func TestWorkProposalStoreRejectsInvalidInputs(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	var nilStore *protocol.WorkProposalStore
	if _, err := protocol.NewWorkProposalStore(nil); err == nil {
		t.Fatal("NewWorkProposalStore(nil) error = nil, want rejection")
	}
	if got := nilStore.DB(); got != nil {
		t.Fatalf("nil store DB = %p, want nil", got)
	}
	if err := nilStore.Close(); err != nil {
		t.Fatalf("nil store Close() error = %v, want nil", err)
	}
	if err := nilStore.StoreEvidenceRun(ctx, protocol.EvidenceRun{}); err == nil {
		t.Fatal("nil store StoreEvidenceRun error = nil, want rejection")
	}
	if _, err := nilStore.StoreWorkProposal(ctx, protocol.WorkProposalPayload{}); err == nil {
		t.Fatal("nil store StoreWorkProposal error = nil, want rejection")
	}

	store := openWorkProposalStore(ctx, t, filepath.Join(t.TempDir(), "invalid.db"))
	if err := store.StoreEvidenceRun(ctx, protocol.EvidenceRun{}); err == nil {
		t.Fatal("incomplete evidence error = nil, want rejection")
	}
	if _, err := store.StoreWorkProposal(ctx, protocol.WorkProposalPayload{}); err == nil {
		t.Fatal("incomplete proposal error = nil, want rejection")
	}
}

func TestWorkProposalStoreReportsClosedDatabase(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := openWorkProposalStore(ctx, t, filepath.Join(t.TempDir(), "closed.db"))
	if err := store.DB().Close(); err != nil {
		t.Fatalf("close backing database: %v", err)
	}
	if err := store.StoreEvidenceRun(ctx, validEvidenceRun()); err == nil {
		t.Fatal("StoreEvidenceRun on closed database error = nil")
	}
	if _, err := store.StoreWorkProposal(ctx, validWorkProposalPayload()); err == nil {
		t.Fatal("StoreWorkProposal on closed database error = nil")
	}
}

func TestWorkProposalStoreRejectsCorruptReplay(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := openWorkProposalStore(ctx, t, filepath.Join(t.TempDir(), "corrupt.db"))
	payload := validWorkProposalPayload()
	seedWorkProposalEvidence(ctx, t, store, payload)
	if _, err := store.StoreWorkProposal(ctx, payload); err != nil {
		t.Fatalf("store proposal: %v", err)
	}
	if _, err := store.DB().ExecContext(ctx, `
UPDATE work_proposal_submissions SET response_json = '{'
WHERE assignment_id = ? AND client_proposal_id = ?`, payload.AssignmentID, payload.ClientProposalID); err != nil {
		t.Fatalf("corrupt stored response: %v", err)
	}
	if _, err := store.StoreWorkProposal(ctx, payload); err == nil || !strings.Contains(err.Error(), "decode stored response") {
		t.Fatalf("corrupt replay error = %v, want decode stored response", err)
	}
}

func TestWorkProposalStoreReportsDurabilityFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setupSQL  string
		wantError string
	}{
		{name: "load submission", setupSQL: "DROP TABLE work_proposal_submissions", wantError: "load submission"},
		{name: "load evidence", setupSQL: "DROP TABLE evidence_runs", wantError: "load evidence run"},
		{name: "insert proposal", setupSQL: failureTriggerSQL("work_proposals"), wantError: "insert proposal"},
		{name: "insert transition", setupSQL: failureTriggerSQL("work_proposal_transitions"), wantError: "insert transition"},
		{name: "insert event", setupSQL: failureTriggerSQL("work_proposal_events"), wantError: "insert event"},
		{name: "insert submission", setupSQL: failureTriggerSQL("work_proposal_submissions"), wantError: "insert submission"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			store := openWorkProposalStore(ctx, t, filepath.Join(t.TempDir(), "failure.db"))
			payload := validWorkProposalPayload()
			seedWorkProposalEvidence(ctx, t, store, payload)
			if _, err := store.DB().ExecContext(ctx, test.setupSQL); err != nil {
				t.Fatalf("install durability failure: %v", err)
			}
			if _, err := store.StoreWorkProposal(ctx, payload); err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("StoreWorkProposal error = %v, want %q", err, test.wantError)
			}
		})
	}
}

func failureTriggerSQL(table string) string {
	return "CREATE TRIGGER fail_insert BEFORE INSERT ON " + table +
		" BEGIN SELECT RAISE(ABORT, 'injected durability failure'); END"
}

func validWorkProposalPayload() protocol.WorkProposalPayload {
	return protocol.WorkProposalPayload{
		ClientProposalID:  "proposal-coverage",
		AssignmentID:      41,
		WorkerID:          "worker-coverage",
		BeadID:            "bead-coverage",
		EvidenceRunID:     "evidence-coverage",
		Fingerprint:       "fingerprint-coverage",
		ScopeHint:         "pkg/protocol",
		Kind:              "prerequisite",
		Summary:           "exercise durable failures",
		SuggestedTitle:    "Exercise durable failures",
		SuggestedType:     "task",
		SuggestedPriority: 2,
	}
}

func validEvidenceRun() protocol.EvidenceRun {
	return protocol.EvidenceRun{
		ID:           "evidence-coverage",
		AssignmentID: 41,
		WorkerID:     "worker-coverage",
		BeadID:       "bead-coverage",
		Kind:         "diagnostic",
		Status:       "completed",
	}
}

func seedWorkProposalEvidence(ctx context.Context, t *testing.T, store *protocol.WorkProposalStore, payload protocol.WorkProposalPayload) {
	t.Helper()
	run := validEvidenceRun()
	run.ID = payload.EvidenceRunID
	run.AssignmentID = payload.AssignmentID
	run.WorkerID = payload.WorkerID
	run.BeadID = payload.BeadID
	if err := store.StoreEvidenceRun(ctx, run); err != nil {
		t.Fatalf("seed evidence run: %v", err)
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
