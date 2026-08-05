package dispatcher

import (
	"context"
	"strings"
	"testing"
)

func TestAcquireAssignmentSideEffectAdmissionRejectsInvalidInputs(t *testing.T) {
	ctx := context.Background()
	var nilDispatcher *Dispatcher
	if admission, err := nilDispatcher.acquireAssignmentSideEffectAdmission(ctx, "bead", "worker", "test"); err == nil || admission != nil {
		t.Fatalf("nil dispatcher admission = %#v, %v, want nil and error", admission, err)
	}
	if admission, err := (&Dispatcher{}).acquireAssignmentSideEffectAdmission(ctx, "bead", "worker", "test"); err == nil || admission != nil {
		t.Fatalf("nil database admission = %#v, %v, want nil and error", admission, err)
	}
	d, _, _, _, _, _ := newTestDispatcher(t)
	if admission, err := d.acquireAssignmentSideEffectAdmission(ctx, "", "worker", "test"); err == nil || admission != nil {
		t.Fatalf("empty bead admission = %#v, %v, want nil and error", admission, err)
	}
}

func TestAcquireAssignmentSideEffectAdmissionPersistsOwnedToken(t *testing.T) {
	ctx := context.Background()
	d, _, _, _, _, _ := newTestDispatcher(t)
	admission, err := d.acquireAssignmentSideEffectAdmission(ctx, "bead-owned", "worker", "test")
	if err != nil || admission == nil {
		t.Fatalf("acquire admission = %#v, %v", admission, err)
	}
	var token string
	if err := d.db.QueryRowContext(ctx,
		`SELECT owner_token FROM assignment_side_effect_admissions WHERE bead_id='bead-owned'`,
	).Scan(&token); err != nil {
		t.Fatalf("query admission: %v", err)
	}
	if token == "" || token != admission.token || admission.beadID != "bead-owned" {
		t.Fatalf("persisted token/admission = %q/%#v", token, admission)
	}
	d.releaseAssignmentSideEffectAdmission(ctx, admission)
}

func TestAcquireAssignmentSideEffectAdmissionBlocksAndAuditsReservedBead(t *testing.T) {
	ctx := context.Background()
	d, _, _, _, _, _ := newTestDispatcher(t)
	first, err := d.acquireAssignmentSideEffectAdmission(ctx, "bead-reserved", "worker-1", "first-stage")
	if err != nil || first == nil {
		t.Fatalf("first admission = %#v, %v", first, err)
	}
	t.Cleanup(func() { d.releaseAssignmentSideEffectAdmission(ctx, first) })

	blocked, err := d.acquireAssignmentSideEffectAdmission(ctx, "bead-reserved", "worker-2", "second-stage")
	if err != nil || blocked != nil {
		t.Fatalf("second admission = %#v, %v, want nil without error", blocked, err)
	}
	var payload, workerID string
	if err := d.db.QueryRowContext(ctx, `
		SELECT payload, worker_id FROM events
		WHERE type='review_checkpoint_assignment_blocked' AND bead_id='bead-reserved'
		ORDER BY id DESC LIMIT 1`).Scan(&payload, &workerID); err != nil {
		t.Fatalf("query blocked event: %v", err)
	}
	if workerID != "worker-2" || !strings.Contains(payload, `"stage":"second-stage"`) {
		t.Fatalf("blocked event worker/payload = %q/%q", workerID, payload)
	}
}

func TestAcquireAssignmentSideEffectAdmissionReportsStorageFailureAndObservation(t *testing.T) {
	ctx := context.Background()
	d, _, _, _, _, _ := newTestDispatcher(t)
	if _, err := d.db.ExecContext(ctx, `DROP TABLE assignment_side_effect_admissions`); err != nil {
		t.Fatalf("drop admission table: %v", err)
	}

	admission, err := d.acquireAssignmentSideEffectAdmission(ctx, "bead-storage-error", "worker", "test")
	if err == nil || admission != nil {
		t.Fatalf("admission = %#v, %v, want storage error", admission, err)
	}
	d.mu.Lock()
	observation := d.checkpointObservationError
	d.mu.Unlock()
	if !strings.Contains(observation, "assignment_side_effect_admissions") {
		t.Fatalf("checkpoint observation = %q, want storage failure", observation)
	}
}

func TestReleaseAssignmentSideEffectAdmissionHandlesNilInputs(t *testing.T) {
	ctx := context.Background()
	var nilDispatcher *Dispatcher
	nilDispatcher.releaseAssignmentSideEffectAdmission(ctx, &assignmentSideEffectAdmission{beadID: "bead", token: "token"})
	(&Dispatcher{}).releaseAssignmentSideEffectAdmission(ctx, &assignmentSideEffectAdmission{beadID: "bead", token: "token"})
	d, _, _, _, _, _ := newTestDispatcher(t)
	d.releaseAssignmentSideEffectAdmission(ctx, nil)
}

func TestReleaseAssignmentSideEffectAdmissionDeletesOnlyOwnedToken(t *testing.T) {
	ctx := context.Background()
	d, _, _, _, _, _ := newTestDispatcher(t)
	admission, err := d.acquireAssignmentSideEffectAdmission(ctx, "bead-release", "worker", "test")
	if err != nil || admission == nil {
		t.Fatalf("acquire admission = %#v, %v", admission, err)
	}

	d.releaseAssignmentSideEffectAdmission(ctx, &assignmentSideEffectAdmission{beadID: admission.beadID, token: "wrong-token"})
	var count int
	if err := d.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM assignment_side_effect_admissions WHERE bead_id='bead-release'`,
	).Scan(&count); err != nil {
		t.Fatalf("count admission after wrong release: %v", err)
	}
	if count != 1 {
		t.Fatalf("admission count after wrong release = %d, want 1", count)
	}
	d.releaseAssignmentSideEffectAdmission(ctx, admission)
	if err := d.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM assignment_side_effect_admissions WHERE bead_id='bead-release'`,
	).Scan(&count); err != nil {
		t.Fatalf("count admission after owned release: %v", err)
	}
	if count != 0 {
		t.Fatalf("admission count after owned release = %d, want 0", count)
	}
}

func TestReleaseAssignmentSideEffectAdmissionAuditsStorageFailure(t *testing.T) {
	ctx := context.Background()
	d, _, _, _, _, _ := newTestDispatcher(t)
	if _, err := d.db.ExecContext(ctx, `DROP TABLE assignment_side_effect_admissions`); err != nil {
		t.Fatalf("drop admission table: %v", err)
	}
	d.releaseAssignmentSideEffectAdmission(ctx, &assignmentSideEffectAdmission{beadID: "bead-release-error", token: "token"})

	var payload string
	if err := d.db.QueryRowContext(ctx, `
		SELECT payload FROM events
		WHERE type='assignment_side_effect_admission_release_failed' AND bead_id='bead-release-error'
		ORDER BY id DESC LIMIT 1`).Scan(&payload); err != nil {
		t.Fatalf("query release failure event: %v", err)
	}
	if !strings.Contains(payload, "assignment_side_effect_admissions") {
		t.Fatalf("release failure payload = %q", payload)
	}
}

func TestClearStaleAssignmentSideEffectAdmissionsHandlesNilInputs(t *testing.T) {
	ctx := context.Background()
	var nilDispatcher *Dispatcher
	if err := nilDispatcher.clearStaleAssignmentSideEffectAdmissions(ctx); err != nil {
		t.Fatalf("nil dispatcher clear: %v", err)
	}
	if err := (&Dispatcher{}).clearStaleAssignmentSideEffectAdmissions(ctx); err != nil {
		t.Fatalf("nil database clear: %v", err)
	}
}

func TestClearStaleAssignmentSideEffectAdmissionsRemovesAllRows(t *testing.T) {
	ctx := context.Background()
	d, _, _, _, _, _ := newTestDispatcher(t)
	if _, err := d.db.ExecContext(ctx, `
		INSERT INTO assignment_side_effect_admissions (bead_id, owner_token)
		VALUES ('bead-a', 'token-a'), ('bead-b', 'token-b')`); err != nil {
		t.Fatalf("seed admissions: %v", err)
	}
	if err := d.clearStaleAssignmentSideEffectAdmissions(ctx); err != nil {
		t.Fatalf("clear admissions: %v", err)
	}
	var count int
	if err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM assignment_side_effect_admissions`).Scan(&count); err != nil {
		t.Fatalf("count admissions: %v", err)
	}
	if count != 0 {
		t.Fatalf("admission count = %d, want 0", count)
	}
}

func TestClearStaleAssignmentSideEffectAdmissionsReportsStorageFailure(t *testing.T) {
	ctx := context.Background()
	d, _, _, _, _, _ := newTestDispatcher(t)
	if _, err := d.db.ExecContext(ctx, `DROP TABLE assignment_side_effect_admissions`); err != nil {
		t.Fatalf("drop admission table: %v", err)
	}
	if err := d.clearStaleAssignmentSideEffectAdmissions(ctx); err == nil || !strings.Contains(err.Error(), "clear stale") {
		t.Fatalf("clear error = %v, want storage failure", err)
	}
}
