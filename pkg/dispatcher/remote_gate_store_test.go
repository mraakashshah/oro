//nolint:testpackage // The transition owner is intentionally package-private.
package dispatcher

import (
	"context"
	"errors"
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

func TestRemoteGateStoreRejectsInvalidAndStaleTransitions(t *testing.T) {
	ctx := context.Background()
	if _, err := NewStore(ctx, nil); err == nil {
		t.Fatal("NewStore(nil) returned nil error")
	}

	db, err := dbutil.OpenDB(filepath.Join(t.TempDir(), "remote-gates.db"))
	if err != nil {
		t.Fatalf("open DB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := NewStore(ctx, db)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if _, err := store.AdoptCandidate(ctx, RemoteGateCandidate{}); err == nil {
		t.Fatal("AdoptCandidate with missing identity returned nil error")
	}
	if _, err := store.RemoteGate(ctx, 0); err == nil {
		t.Fatal("RemoteGate(0) returned nil error")
	}
	if err := store.RecordPresubmitResult(ctx, PresubmitResult{}); err == nil {
		t.Fatal("RecordPresubmitResult with missing identity returned nil error")
	}

	candidate := RemoteGateCandidate{
		Key:          "oro-repf:42:def456",
		BeadID:       "oro-repf",
		AssignmentID: 42,
		CandidateSHA: "def456",
		BaseSHA:      "base789",
		TargetBranch: "main",
		AdoptionRef:  "refs/oro/adopted/oro/oro-repf/42",
	}
	gate, err := store.AdoptCandidate(ctx, candidate)
	if err != nil {
		t.Fatalf("AdoptCandidate: %v", err)
	}
	reused, err := store.AdoptCandidate(ctx, candidate)
	if err != nil {
		t.Fatalf("duplicate AdoptCandidate: %v", err)
	}
	if reused.ID != gate.ID {
		t.Fatalf("reused ID = %d, want %d", reused.ID, gate.ID)
	}
	if _, err := store.AdvanceRemoteGate(ctx, gate.ID, RemoteGateStateCandidateAdopted, RemoteGateStatePublishing); err == nil {
		t.Fatal("invalid transition returned nil error")
	}
	if _, err := store.AdvanceRemoteGate(ctx, gate.ID, RemoteGateStateCandidateAdopted, RemoteGateStateLocalPresubmit); err != nil {
		t.Fatalf("first transition: %v", err)
	}
	if _, err := store.AdvanceRemoteGate(ctx, gate.ID, RemoteGateStateRebasing, RemoteGateStateLocalPresubmitRebase); !errors.Is(err, ErrRemoteGateTransitionConflict) {
		t.Fatalf("stale transition error = %v, want ErrRemoteGateTransitionConflict", err)
	}
}

func TestRemoteGateStorePersistenceFailures(t *testing.T) {
	ctx := context.Background()
	var nilStore *Store
	if _, err := nilStore.AdoptCandidate(ctx, RemoteGateCandidate{}); err == nil {
		t.Fatal("nil AdoptCandidate returned nil error")
	}
	if _, err := nilStore.RemoteGate(ctx, 1); err == nil {
		t.Fatal("nil RemoteGate returned nil error")
	}
	if _, err := nilStore.AdvanceRemoteGate(ctx, 1, RemoteGateStateCandidateAdopted, RemoteGateStateLocalPresubmit); err == nil {
		t.Fatal("nil AdvanceRemoteGate returned nil error")
	}
	if err := nilStore.RecordPresubmitResult(ctx, PresubmitResult{}); err == nil {
		t.Fatal("nil RecordPresubmitResult returned nil error")
	}

	db, err := dbutil.OpenDB(filepath.Join(t.TempDir(), "remote-gates.db"))
	if err != nil {
		t.Fatalf("open DB: %v", err)
	}
	store, err := NewStore(ctx, db)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if _, err := store.RemoteGate(ctx, 999); err == nil {
		t.Fatal("missing RemoteGate returned nil error")
	}

	candidate := RemoteGateCandidate{
		Key:          "oro-repf:43:ghi789",
		BeadID:       "oro-repf",
		AssignmentID: 43,
		CandidateSHA: "ghi789",
		BaseSHA:      "base000",
		TargetBranch: "main",
		AdoptionRef:  "refs/oro/adopted/oro/oro-repf/43",
	}
	gate, err := store.AdoptCandidate(ctx, candidate)
	if err != nil {
		t.Fatalf("AdoptCandidate: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE remote_gates SET candidate_sha = 'different' WHERE id = ?`, gate.ID); err != nil {
		t.Fatalf("change persisted candidate: %v", err)
	}
	if _, err := store.AdoptCandidate(ctx, candidate); err == nil {
		t.Fatal("conflicting candidate reuse returned nil error")
	}

	if err := db.Close(); err != nil {
		t.Fatalf("close DB: %v", err)
	}
	if _, err := NewStore(ctx, db); err == nil {
		t.Fatal("NewStore on closed DB returned nil error")
	}
	if err := store.RecordPresubmitResult(ctx, PresubmitResult{
		GateID: 1, ActionName: "format", CandidateSHA: "candidate", BaseSHA: "base", Command: "cmd",
		Profile: "profile", ToolHash: "tools", StartedAt: "start", CompletedAt: "end", Outcome: "passed", ResourceClass: "light",
	}); err == nil {
		t.Fatal("RecordPresubmitResult on closed DB returned nil error")
	}
}
