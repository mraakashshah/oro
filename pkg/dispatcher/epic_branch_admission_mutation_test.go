package dispatcher //nolint:testpackage // mutation tests exercise white-box admission behavior

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"oro/pkg/beadstore"
	"oro/pkg/protocol"
)

type epicAdmissionMutationCreateFailureStore struct {
	DeferredStore
}

func (s epicAdmissionMutationCreateFailureStore) Create(context.Context, beadstore.CreateParams) (*protocol.Bead, error) {
	return nil, errors.New("injected recovery create failure")
}

func epicAdmissionMutationLease(t *testing.T, d *Dispatcher, branch string) epicBranchAdmission {
	t.Helper()
	fixedNow := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	d.nowFunc = func() time.Time { return fixedNow }
	if err := protocol.MigrateBeadSchema(context.Background(), d.db); err != nil {
		t.Fatalf("migrate admission schema: %v", err)
	}
	lease, acquired, err := newEpicBranchAdmissionStore(d.db).acquire(
		context.Background(), branch, "oro-mutation-epic", "main", "mutation-token", "mutation-worker",
		d.nowFunc(),
	)
	if err != nil || !acquired {
		t.Fatalf("acquire mutation lease: acquired=%v err=%v", acquired, err)
	}
	return lease
}

func TestEpicBranchAdmissionMutationRenewalOutcomes(t *testing.T) {
	t.Run("zero interval uses safe default", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		d.epicAdmissionRenewEvery = 0
		done := make(chan struct{})
		close(done)
		d.renewEpicBranchAdmission(context.Background(), epicBranchAdmission{}, done)
	})

	t.Run("lost lease cancels operation with cause and audit", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		lease := epicAdmissionMutationLease(t, d, "epic/mutation-renew-loss")
		if _, err := d.db.Exec(`UPDATE epic_branch_admissions SET generation=generation+1 WHERE branch=?`, lease.branch); err != nil {
			t.Fatalf("replace lease generation: %v", err)
		}
		operationCtx, cancel := context.WithCancelCause(context.Background())
		lease.operation = &epicBranchAdmissionOperation{cancel: cancel}
		d.epicAdmissionRenewEvery = time.Millisecond
		returned := make(chan struct{})
		go func() {
			d.renewEpicBranchAdmission(operationCtx, lease, make(chan struct{}))
			close(returned)
		}()
		select {
		case <-operationCtx.Done():
		case <-time.After(time.Second):
			t.Fatal("renewal ownership loss did not cancel operation")
		}
		if cause := context.Cause(operationCtx); cause == nil || !strings.Contains(cause.Error(), "compare-and-swap") {
			t.Fatalf("renewal cancellation cause = %v, want compare-and-swap failure", cause)
		}
		select {
		case <-returned:
		case <-time.After(time.Second):
			t.Fatal("renewal did not return after ownership loss")
		}
		if got := eventCount(t, d.db, "epic_branch_admission_renew_failed"); got != 1 {
			t.Fatalf("renew failure events = %d, want 1", got)
		}
	})

	t.Run("owned blocked lease stops quietly", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		lease := epicAdmissionMutationLease(t, d, "epic/mutation-renew-blocked")
		if _, err := newEpicBranchAdmissionStore(d.db).block(context.Background(), lease.branch, lease.leaseToken,
			lease.generation, "diverged", "", "branch-sha", "target-sha", "", "blocked", d.nowFunc()); err != nil {
			t.Fatalf("block held lease: %v", err)
		}
		operationCtx, cancel := context.WithCancelCause(context.Background())
		lease.operation = &epicBranchAdmissionOperation{cancel: cancel}
		d.epicAdmissionRenewEvery = time.Millisecond
		returned := make(chan struct{})
		go func() {
			d.renewEpicBranchAdmission(operationCtx, lease, make(chan struct{}))
			close(returned)
		}()
		select {
		case <-returned:
		case <-time.After(time.Second):
			t.Fatal("owned blocked renewal did not stop")
		}
		if operationCtx.Err() != nil || eventCount(t, d.db, "epic_branch_admission_renew_failed") != 0 {
			t.Fatalf("owned blocked renewal canceled=%v events=%d, want false/0",
				operationCtx.Err(), eventCount(t, d.db, "epic_branch_admission_renew_failed"))
		}
	})
}

func TestEpicBranchAdmissionMutationBlockOutcomes(t *testing.T) {
	inspection := epicBranchInspection{BranchOID: "branch-sha", BaseOID: "target-sha", Relation: branchDiverged}

	t.Run("stale lease is quiet", func(t *testing.T) {
		d, beads, _, _, _, _ := newTestDispatcher(t)
		lease := epicAdmissionMutationLease(t, d, "epic/mutation-block-stale")
		if _, err := d.db.Exec(`UPDATE epic_branch_admissions SET generation=generation+1 WHERE branch=?`, lease.branch); err != nil {
			t.Fatalf("replace lease generation: %v", err)
		}
		d.mu.Lock()
		d.assigningBeads["oro-mutation-child"] = true
		d.mu.Unlock()
		d.blockEpicBranchAdmission(context.Background(), "oro-mutation-child", lease, "diverged", "", inspection, "blocked")
		if eventCount(t, d.db, "epic_branch_admission_block_failed") != 0 || eventCount(t, d.db, "epic_branch_prepare_failed") != 0 {
			t.Fatal("stale block emitted failure events")
		}
		d.mu.Lock()
		stillAssigning := d.assigningBeads["oro-mutation-child"]
		d.mu.Unlock()
		beads.mu.Lock()
		status := beads.updated["oro-mutation-child"]
		beads.mu.Unlock()
		if !stillAssigning || status != "" {
			t.Fatalf("stale block claim = assigning %v status %q, want true/empty", stillAssigning, status)
		}
	})

	t.Run("storage failure rejects and audits", func(t *testing.T) {
		d, beads, _, _, _, _ := newTestDispatcher(t)
		lease := epicAdmissionMutationLease(t, d, "epic/mutation-block-error")
		d.mu.Lock()
		d.assigningBeads["oro-mutation-child"] = true
		d.mu.Unlock()
		if _, err := d.db.Exec(`DROP TABLE epic_branch_admissions`); err != nil {
			t.Fatalf("drop admissions table: %v", err)
		}
		d.blockEpicBranchAdmission(context.Background(), "oro-mutation-child", lease, "diverged", "", inspection, "blocked")
		if eventCount(t, d.db, "epic_branch_admission_block_failed") != 1 || eventCount(t, d.db, "epic_branch_prepare_failed") != 1 {
			t.Fatalf("storage failure events block/prepare = %d/%d, want 1/1",
				eventCount(t, d.db, "epic_branch_admission_block_failed"), eventCount(t, d.db, "epic_branch_prepare_failed"))
		}
		d.mu.Lock()
		stillAssigning := d.assigningBeads["oro-mutation-child"]
		d.mu.Unlock()
		beads.mu.Lock()
		status := beads.updated["oro-mutation-child"]
		beads.mu.Unlock()
		if stillAssigning || status != "open" {
			t.Fatalf("storage failure claim = assigning %v status %q, want false/open", stillAssigning, status)
		}
	})

	t.Run("recovery creation failure is audited", func(t *testing.T) {
		d, beads, _, _, _, _ := newTestDispatcher(t)
		lease := epicAdmissionMutationLease(t, d, "epic/mutation-block-recovery-error")
		d.beads = epicAdmissionMutationCreateFailureStore{DeferredStore: beads}
		d.blockEpicBranchAdmission(context.Background(), "oro-mutation-child", lease, "diverged", "", inspection, "blocked")
		if got := eventCount(t, d.db, "epic_branch_recovery_ensure_failed"); got != 1 {
			t.Fatalf("recovery failure events = %d, want 1", got)
		}
	})
}

func TestEpicBranchAdmissionMutationBypassAndClaimPreservation(t *testing.T) {
	for _, tt := range []struct {
		name, branch, epicID string
	}{
		{name: "empty epic id bypasses epic prefix", branch: "epic/metadata-only", epicID: ""},
		{name: "non epic prefix bypasses populated epic", branch: "release/custom", epicID: "oro-mutation-epic"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			d, _, worktrees, _, _, _ := newTestDispatcher(t)
			if err := protocol.MigrateBeadSchema(context.Background(), d.db); err != nil {
				t.Fatalf("migrate admission schema: %v", err)
			}
			worktrees.branchExistsFn = func(context.Context, string) (bool, error) { return true, nil }
			if !d.withEpicBranchAdmission(context.Background(), protocol.Bead{ID: "oro-mutation-child"},
				"mutation-worker", tt.branch, tt.epicID, "main") {
				t.Fatal("legacy branch was not admitted")
			}
			var rows int
			if err := d.db.QueryRow(`SELECT COUNT(*) FROM epic_branch_admissions`).Scan(&rows); err != nil || rows != 0 {
				t.Fatalf("admission ledger rows = %d err=%v, want 0", rows, err)
			}
		})
	}

	t.Run("successful admission preserves active claim", func(t *testing.T) {
		d, beads, baseWorktrees, _, _, _ := newTestDispatcher(t)
		if err := protocol.MigrateBeadSchema(context.Background(), d.db); err != nil {
			t.Fatalf("migrate admission schema: %v", err)
		}
		continueInspection := make(chan struct{})
		close(continueInspection)
		d.worktrees = &admissionTestWorktreeManager{
			mockWorktreeManager: baseWorktrees,
			inspection:          epicBranchInspection{BranchOID: "same", BaseOID: "same", Relation: branchSame},
			inspectionStarted:   make(chan struct{}),
			continueInspection:  continueInspection,
		}
		d.mu.Lock()
		d.assigningBeads["oro-mutation-child"] = true
		d.mu.Unlock()
		if !d.withEpicBranchAdmission(context.Background(), protocol.Bead{ID: "oro-mutation-child", Epic: "oro-mutation-epic"},
			"mutation-worker", "epic/mutation-success", "oro-mutation-epic", "main") {
			t.Fatal("fresh admission did not succeed")
		}
		d.mu.Lock()
		stillAssigning := d.assigningBeads["oro-mutation-child"]
		d.mu.Unlock()
		beads.mu.Lock()
		status := beads.updated["oro-mutation-child"]
		beads.mu.Unlock()
		if !stillAssigning || status != "" {
			t.Fatalf("successful admission claim = assigning %v status %q, want true/empty", stillAssigning, status)
		}
	})
}
