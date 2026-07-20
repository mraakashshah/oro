//nolint:testpackage // The transition owner is intentionally package-private.
package dispatcher

import (
	"context"
	"path/filepath"
	"testing"

	"oro/pkg/dbutil"
)

func TestRemoteGateStoreTransition(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "remote-gates.db")
	db, err := dbutil.OpenDB(dbPath)
	if err != nil {
		t.Fatalf("open DB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	store, err := NewStore(ctx, db)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	candidate := RemoteGateCandidate{
		Key:          "oro-repf:41:abc123",
		BeadID:       "oro-repf",
		AssignmentID: 41,
		CandidateSHA: "abc123",
		BaseSHA:      "base456",
		TargetBranch: "main",
		AdoptionRef:  "refs/oro/adopted/oro/oro-repf/41",
	}

	adopted, err := store.AdoptCandidate(ctx, candidate)
	if err != nil {
		t.Fatalf("AdoptCandidate: %v", err)
	}
	if adopted.State != RemoteGateStateCandidateAdopted {
		t.Fatalf("adopted state = %q, want %q", adopted.State, RemoteGateStateCandidateAdopted)
	}
	presubmit := PresubmitResult{
		GateID:        adopted.ID,
		ActionName:    "format",
		CandidateSHA:  candidate.CandidateSHA,
		BaseSHA:       candidate.BaseSHA,
		Command:       "gofumpt -l .",
		Profile:       "memory-safe",
		ToolHash:      "tools789",
		StartedAt:     "2026-07-20T12:00:00Z",
		CompletedAt:   "2026-07-20T12:00:01Z",
		Outcome:       "passed",
		Logs:          "clean",
		ResourceClass: "cpu_light",
	}
	if err := store.RecordPresubmitResult(ctx, presubmit); err != nil {
		t.Fatalf("RecordPresubmitResult: %v", err)
	}
	if err := store.RecordPresubmitResult(ctx, presubmit); err != nil {
		t.Fatalf("duplicate RecordPresubmitResult: %v", err)
	}
	var evidenceRows int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM remote_gate_presubmit_results WHERE gate_id = ?`, adopted.ID).Scan(&evidenceRows); err != nil {
		t.Fatalf("count presubmit evidence: %v", err)
	}
	if evidenceRows != 1 {
		t.Fatalf("presubmit evidence rows = %d, want 1 after duplicate completion", evidenceRows)
	}

	d := &Dispatcher{remoteGates: store}
	advanced, err := d.advanceRemoteGate(ctx, adopted.ID, RemoteGateStateCandidateAdopted, RemoteGateStateLocalPresubmit)
	if err != nil {
		t.Fatalf("advanceRemoteGate: %v", err)
	}
	if advanced.ID != adopted.ID || advanced.State != RemoteGateStateLocalPresubmit {
		t.Fatalf("advanced record = %+v, want adopted ID %d in %q", advanced, adopted.ID, RemoteGateStateLocalPresubmit)
	}

	duplicate, err := d.advanceRemoteGate(ctx, adopted.ID, RemoteGateStateCandidateAdopted, RemoteGateStateLocalPresubmit)
	if err != nil {
		t.Fatalf("duplicate advanceRemoteGate: %v", err)
	}
	if duplicate.ID != adopted.ID || duplicate.State != RemoteGateStateLocalPresubmit {
		t.Fatalf("duplicate record = %+v, want unchanged adopted record", duplicate)
	}

	if err := db.Close(); err != nil {
		t.Fatalf("close DB: %v", err)
	}
	reopenedDB, err := dbutil.OpenDB(dbPath)
	if err != nil {
		t.Fatalf("reopen DB: %v", err)
	}
	t.Cleanup(func() { _ = reopenedDB.Close() })
	reopenedStore, err := NewStore(ctx, reopenedDB)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	persisted, err := reopenedStore.RemoteGate(ctx, adopted.ID)
	if err != nil {
		t.Fatalf("load persisted gate: %v", err)
	}
	if persisted.State != RemoteGateStateLocalPresubmit {
		t.Fatalf("persisted state = %q, want %q", persisted.State, RemoteGateStateLocalPresubmit)
	}
}

func TestValidRemoteGateTransition(t *testing.T) {
	tests := []struct {
		from RemoteGateState
		to   RemoteGateState
		want bool
	}{
		{RemoteGateStateCandidateAdopted, RemoteGateStateLocalPresubmit, true},
		{RemoteGateStateLocalPresubmit, RemoteGateStateRebasing, true},
		{RemoteGateStateLocalPresubmit, RemoteGateStateFailed, true},
		{RemoteGateStateRebasing, RemoteGateStateLocalPresubmitRebase, true},
		{RemoteGateStateLocalPresubmitRebase, RemoteGateStateOpsReview, true},
		{RemoteGateStateOpsReview, RemoteGateStatePublishing, true},
		{RemoteGateStatePublishing, RemoteGateStateAwaitingRun, true},
		{RemoteGateStateAwaitingRun, RemoteGateStateRunning, true},
		{RemoteGateStateRunning, RemoteGateStatePassed, true},
		{RemoteGateStatePassed, RemoteGateStateReconciled, true},
		{RemoteGateStateCandidateAdopted, RemoteGateStateCandidateAdopted, true},
		{RemoteGateStateCandidateAdopted, RemoteGateStatePublishing, false},
		{RemoteGateStateReconciled, RemoteGateStateRunning, false},
	}
	for _, test := range tests {
		if got := validRemoteGateTransition(test.from, test.to); got != test.want {
			t.Errorf("validRemoteGateTransition(%q, %q) = %t, want %t", test.from, test.to, got, test.want)
		}
	}
}
