package dispatcher //nolint:testpackage // white-box mutation tests exercise private assignment persistence state

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestCreateAssignmentReportsAdmissionFailure(t *testing.T) {
	d := &Dispatcher{}
	if id, err := d.createAssignment(context.Background(), "bead", "worker", "/tmp/worktree"); err == nil || id != 0 {
		t.Fatalf("create assignment = %d, %v, want zero and admission error", id, err)
	}
}

func TestCreateAssignmentPersistsExactIdentity(t *testing.T) {
	ctx := context.Background()
	d, _, _, _, _, _ := newTestDispatcher(t)
	id, err := d.createAssignment(ctx, "bead-create", "worker-create", "/tmp/create")
	if err != nil || id <= 0 {
		t.Fatalf("create assignment = %d, %v", id, err)
	}
	var beadID, workerID, worktree, status string
	if err := d.db.QueryRowContext(ctx, `
		SELECT bead_id, worker_id, worktree, status FROM assignments WHERE id=?`, id,
	).Scan(&beadID, &workerID, &worktree, &status); err != nil {
		t.Fatalf("query assignment: %v", err)
	}
	if beadID != "bead-create" || workerID != "worker-create" || worktree != "/tmp/create" || status != "active" {
		t.Fatalf("assignment = %q/%q/%q/%q", beadID, workerID, worktree, status)
	}
}

func TestCreateAssignmentRejectsDurableCheckpointWithoutRow(t *testing.T) {
	ctx := context.Background()
	d, _, _, _, _, _ := newTestDispatcher(t)
	if _, err := NewReviewCheckpointStore(d.db).CreateOrReuse(ctx, reviewCheckpointInput("bead-checkpoint")); err != nil {
		t.Fatalf("create checkpoint: %v", err)
	}
	id, err := d.createAssignment(ctx, "bead-checkpoint", "worker", "/tmp/checkpoint")
	if !errors.Is(err, errAssignmentBlockedByReviewCheckpoint) || id != 0 {
		t.Fatalf("create checkpoint-blocked assignment = %d, %v", id, err)
	}
	assertMutationAssignmentCount(t, d, "bead-checkpoint", 0)
}

func TestCreateAssignmentFailsClosedWhenCheckpointObservationFails(t *testing.T) {
	ctx := context.Background()
	d, _, _, _, _, _ := newTestDispatcher(t)
	breakAssignmentAndCheckpointObservation(t, d)

	id, err := d.createAssignment(ctx, "bead-observation", "worker", "/tmp/observation")
	if !errors.Is(err, errAssignmentAdmissionUnknown) || id != 0 {
		t.Fatalf("create assignment = %d, %v, want admission unknown", id, err)
	}
	d.mu.Lock()
	observation := d.checkpointObservationError
	d.mu.Unlock()
	if !strings.Contains(observation, "review_checkpoints_blocking_assignment") {
		t.Fatalf("checkpoint observation = %q", observation)
	}
}

func TestCreateAssignmentRollsBackCommitFailure(t *testing.T) {
	ctx := context.Background()
	d, _, _, _, _, _ := newTestDispatcher(t)
	installDeferredAssignmentCommitFailure(t, d)

	id, err := d.createAssignment(ctx, "bead-commit", "worker", "/tmp/commit")
	if err == nil || id != 0 {
		t.Fatalf("create assignment = %d, %v, want commit failure", id, err)
	}
	assertMutationAssignmentCount(t, d, "bead-commit", 0)
}

func TestCreateAssignmentWithEvidenceReportsTargetResolutionFailure(t *testing.T) {
	ctx := context.Background()
	d, _, wt, _, _, _ := newTestDispatcher(t)
	wt.branchHeadFn = func(string) (string, error) { return "", errors.New("injected branch head failure") }

	id, sha, err := d.createAssignmentWithEvidence(ctx, "bead-target-error", "worker", "/tmp/target", "main")
	if err == nil || id != 0 || sha != "" || !strings.Contains(err.Error(), "resolve assignment target SHA") {
		t.Fatalf("create with evidence = %d, %q, %v", id, sha, err)
	}
}

func TestCreateAssignmentWithEvidenceRejectsBlankTargetSHA(t *testing.T) {
	ctx := context.Background()
	for _, targetSHA := range []string{"", "  \n\t"} {
		t.Run(strings.TrimSpace(targetSHA), func(t *testing.T) {
			d, _, wt, _, _, _ := newTestDispatcher(t)
			wt.branchHeadFn = func(string) (string, error) { return targetSHA, nil }
			id, sha, err := d.createAssignmentWithEvidence(ctx, "bead-blank-target", "worker", "/tmp/blank", "main")
			if err == nil || id != 0 || sha != "" || !strings.Contains(err.Error(), "empty SHA") {
				t.Fatalf("create with evidence = %d, %q, %v, want blank SHA error", id, sha, err)
			}
		})
	}
}

func TestCreateAssignmentWithEvidenceReportsAdmissionFailure(t *testing.T) {
	d := &Dispatcher{worktrees: &mockWorktreeManager{branchHeadFn: func(string) (string, error) { return "target-sha", nil }}}
	id, sha, err := d.createAssignmentWithEvidence(context.Background(), "bead", "worker", "/tmp/worktree", "main")
	if err == nil || id != 0 || sha != "" {
		t.Fatalf("create with evidence = %d, %q, %v, want admission error", id, sha, err)
	}
}

func TestCreateAssignmentWithEvidencePersistsTrimmedProof(t *testing.T) {
	ctx := context.Background()
	d, _, wt, _, _, _ := newTestDispatcher(t)
	wt.branchHeadFn = func(string) (string, error) { return "  target-proof-sha\n", nil }

	id, sha, err := d.createAssignmentWithEvidence(ctx, "bead-proof", "worker-proof", "/tmp/proof", "release")
	if err != nil || id <= 0 || sha != "target-proof-sha" {
		t.Fatalf("create with evidence = %d, %q, %v", id, sha, err)
	}
	var evidenceDir, storedSHA, targetBranch string
	if err := d.db.QueryRowContext(ctx, `
		SELECT qg_evidence_dir, target_sha, target_branch FROM assignments WHERE id=?`, id,
	).Scan(&evidenceDir, &storedSHA, &targetBranch); err != nil {
		t.Fatalf("query assignment proof: %v", err)
	}
	if evidenceDir != d.cfg.ReviewEvidenceDir || storedSHA != sha || targetBranch != "release" {
		t.Fatalf("stored proof = %q/%q/%q", evidenceDir, storedSHA, targetBranch)
	}
}

func TestCreateAssignmentWithEvidenceFailsClosedWhenCheckpointObservationFails(t *testing.T) {
	ctx := context.Background()
	d, _, _, _, _, _ := newTestDispatcher(t)
	breakAssignmentAndCheckpointObservation(t, d)

	id, sha, err := d.createAssignmentWithEvidence(ctx, "bead-proof-observation", "worker", "/tmp/proof", "main")
	if !errors.Is(err, errAssignmentAdmissionUnknown) || id != 0 || sha != "" {
		t.Fatalf("create with evidence = %d, %q, %v, want admission unknown", id, sha, err)
	}
	d.mu.Lock()
	observation := d.checkpointObservationError
	d.mu.Unlock()
	if !strings.Contains(observation, "review_checkpoints_blocking_assignment") {
		t.Fatalf("checkpoint observation = %q", observation)
	}
}

func TestCreateAssignmentWithEvidenceRollsBackCommitFailure(t *testing.T) {
	ctx := context.Background()
	d, _, _, _, _, _ := newTestDispatcher(t)
	installDeferredAssignmentCommitFailure(t, d)

	id, sha, err := d.createAssignmentWithEvidence(ctx, "bead-proof-commit", "worker", "/tmp/proof", "main")
	if err == nil || id != 0 || sha != "" {
		t.Fatalf("create with evidence = %d, %q, %v, want commit failure", id, sha, err)
	}
	assertMutationAssignmentCount(t, d, "bead-proof-commit", 0)
}

func breakAssignmentAndCheckpointObservation(t *testing.T, d *Dispatcher) {
	t.Helper()
	if _, err := d.db.Exec(`DROP TABLE assignments; DROP VIEW review_checkpoints_blocking_assignment`); err != nil {
		t.Fatalf("break assignment observation fixtures: %v", err)
	}
}

func installDeferredAssignmentCommitFailure(t *testing.T, d *Dispatcher) {
	t.Helper()
	if _, err := d.db.Exec(`
		PRAGMA foreign_keys=ON;
		CREATE TABLE mutation_assignment_parents (id INTEGER PRIMARY KEY);
		CREATE TABLE mutation_assignment_children (
			assignment_id INTEGER PRIMARY KEY,
			parent_id INTEGER NOT NULL REFERENCES mutation_assignment_parents(id) DEFERRABLE INITIALLY DEFERRED
		);
		CREATE TRIGGER mutation_assignment_commit_failure
		AFTER INSERT ON assignments BEGIN
			INSERT INTO mutation_assignment_children (assignment_id, parent_id) VALUES (new.id, 999);
		END`); err != nil {
		t.Fatalf("install deferred commit failure: %v", err)
	}
}

func assertMutationAssignmentCount(t *testing.T, d *Dispatcher, beadID string, want int) {
	t.Helper()
	var count int
	if err := d.db.QueryRow(`SELECT COUNT(*) FROM assignments WHERE bead_id=?`, beadID).Scan(&count); err != nil {
		t.Fatalf("count assignments: %v", err)
	}
	if count != want {
		t.Fatalf("assignment count for %s = %d, want %d", beadID, count, want)
	}
}
