package dispatcher

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestReanchorAssignmentWithEvidencePreservesAdmissionAndCheckpointGate(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()

	d.assignmentAdmissionMu.Lock()
	result := make(chan error, 1)
	go func() {
		_, _, err := d.createAssignmentWithEvidence(ctx,
			"reanchor-admission", "reanchor-worker", t.TempDir(), "main")
		result <- err
	}()
	select {
	case err := <-result:
		d.assignmentAdmissionMu.Unlock()
		t.Fatalf("assignment with evidence bypassed canonical admission: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	d.assignmentAdmissionMu.Unlock()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("assignment with evidence after admission release: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("assignment with evidence remained blocked after admission release")
	}

	const blockedBeadID = "reanchor-checkpoint-blocked"
	originID, err := d.createAssignment(ctx, blockedBeadID, "review-worker", t.TempDir())
	if err != nil {
		t.Fatalf("create checkpoint origin assignment: %v", err)
	}
	if err := d.requeueAssignment(ctx, originID); err != nil {
		t.Fatalf("requeue checkpoint origin assignment: %v", err)
	}
	input := reviewCheckpointInput(blockedBeadID)
	input.OriginAssignmentID = originID
	input.CurrentAssignmentID = originID
	if _, err := NewReviewCheckpointStore(d.db).CreateOrReuse(ctx, input); err != nil {
		t.Fatalf("create blocking checkpoint: %v", err)
	}
	if _, _, err := d.createAssignmentWithEvidence(ctx,
		blockedBeadID, "ordinary-worker", t.TempDir(), "main"); !errors.Is(err, errAssignmentBlockedByReviewCheckpoint) {
		t.Fatalf("assignment with evidence error = %v, want checkpoint block", err)
	}
}

func TestReanchorTransactionalReadyCheckpointPreservesOpsRunIdentity(t *testing.T) {
	d, _, _, _, _, _ := newTestDispatcher(t)
	ctx := context.Background()
	const beadID = "reanchor-ready-ops-identity"
	originID, err := d.createAssignment(ctx, beadID, "ready-worker", t.TempDir())
	if err != nil {
		t.Fatalf("create READY origin assignment: %v", err)
	}
	input := reviewCheckpointInput(beadID)
	input.OriginAssignmentID = originID
	input.CurrentAssignmentID = originID
	input.OpsRunID = 991
	admission, err := d.acquireAssignmentSideEffectAdmission(ctx, beadID, "ready-worker", "reanchor-test")
	if err != nil || admission == nil {
		t.Fatalf("acquire side-effect admission = %#v, %v", admission, err)
	}
	created := make(chan struct {
		checkpoint ReviewCheckpoint
		err        error
	}, 1)
	go func() {
		checkpoint, createErr := NewReviewCheckpointStore(d.db).CreateOrReuse(ctx, input)
		created <- struct {
			checkpoint ReviewCheckpoint
			err        error
		}{checkpoint: checkpoint, err: createErr}
	}()
	select {
	case result := <-created:
		d.releaseAssignmentSideEffectAdmission(ctx, admission)
		t.Fatalf("checkpoint bypassed assignment side-effect admission: %v", result.err)
	case <-time.After(50 * time.Millisecond):
	}
	d.releaseAssignmentSideEffectAdmission(ctx, admission)
	select {
	case result := <-created:
		if result.err != nil {
			t.Fatalf("create checkpoint after side-effect admission release: %v", result.err)
		}
		if result.checkpoint.OpsRunID != input.OpsRunID {
			t.Fatalf("public checkpoint ops run ID = %d, want %d", result.checkpoint.OpsRunID, input.OpsRunID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("checkpoint did not resume after side-effect admission release")
	}

	const transactionalBeadID = "reanchor-transactional-ready-ops-identity"
	transactionalOriginID, err := d.createAssignment(ctx, transactionalBeadID, "ready-worker", t.TempDir())
	if err != nil {
		t.Fatalf("create transactional READY origin assignment: %v", err)
	}
	input = reviewCheckpointInput(transactionalBeadID)
	input.OriginAssignmentID = transactionalOriginID
	input.CurrentAssignmentID = transactionalOriginID
	input.OpsRunID = 992

	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin READY transaction: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	checkpoint, err := createOrReuseReviewCheckpoint(ctx, tx, input)
	if err != nil {
		t.Fatalf("create transactional READY checkpoint: %v", err)
	}
	if checkpoint.OpsRunID != input.OpsRunID {
		t.Fatalf("transactional READY ops run ID = %d, want %d", checkpoint.OpsRunID, input.OpsRunID)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit READY transaction: %v", err)
	}
}
