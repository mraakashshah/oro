package dispatcher //nolint:testpackage // targeted white-box tests exercise assignment behavior

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"oro/pkg/protocol"
)

func assignmentBehaviorMutationFixture(
	t *testing.T,
	beadID string,
) (*Dispatcher, *fakeBeadStore, *mockWorktreeManager, *trackedWorker, *mockConn, protocol.Bead) {
	t.Helper()
	d, beads, worktrees, _, _, _ := newTestDispatcher(t)
	bead := protocol.Bead{ID: beadID, Title: "Mutation assignment", Type: "task", Status: "open"}
	beads.shown[beadID] = &protocol.BeadDetail{
		ID:                 beadID,
		Title:              bead.Title,
		Type:               bead.Type,
		Status:             bead.Status,
		AcceptanceCriteria: "Test: assignment behavior | Assert: durable state is exact",
	}
	conn := newMockConn()
	worker := &trackedWorker{
		id:      "mutation-worker-" + beadID,
		state:   protocol.WorkerIdle,
		conn:    conn,
		encoder: json.NewEncoder(conn),
	}
	d.mu.Lock()
	d.workers[worker.id] = worker
	d.mu.Unlock()
	return d, beads, worktrees, worker, conn, bead
}

func TestAssignmentBehaviorMutationReadinessStopsBeforeClaim(t *testing.T) {
	d, beads, worktrees, worker, _, bead := assignmentBehaviorMutationFixture(t, "mutation-readiness")
	beads.shown[bead.ID].Status = "closed"
	var claims []bool
	var outcomes []assignmentSetupOutcome

	if err := d.assignBeadWithClaim(context.Background(), worker, bead, nil,
		func(claimed bool) { claims = append(claims, claimed) },
		func(outcome assignmentSetupOutcome) { outcomes = append(outcomes, outcome) }); err != nil {
		t.Fatalf("assign closed bead: %v", err)
	}
	if !slices.Equal(claims, []bool{false}) || !slices.Equal(outcomes, []assignmentSetupOutcome{assignmentSetupNotDelivered}) {
		t.Fatalf("closed bead callbacks = claims %v outcomes %v", claims, outcomes)
	}
	worktrees.mu.Lock()
	created := len(worktrees.created)
	worktrees.mu.Unlock()
	if created != 0 || worker.state != protocol.WorkerIdle || eventCount(t, d.db, "assign") != 0 {
		t.Fatalf("closed bead side effects = worktrees %d state %q assign events %d",
			created, worker.state, eventCount(t, d.db, "assign"))
	}
}

func TestAssignmentBehaviorMutationReservedOwnerBlocksDuplicate(t *testing.T) {
	d, _, worktrees, worker, _, bead := assignmentBehaviorMutationFixture(t, "mutation-reserved-owner")
	owner := &trackedWorker{
		id:             "mutation-existing-owner",
		state:          protocol.WorkerReserved,
		beadID:         bead.ID,
		reservationGen: 4,
	}
	d.mu.Lock()
	d.workers[owner.id] = owner
	d.mu.Unlock()
	var claims []bool

	if err := d.assignBeadWithClaim(context.Background(), worker, bead, nil,
		func(claimed bool) { claims = append(claims, claimed) }, nil); err != nil {
		t.Fatalf("assign duplicate reserved bead: %v", err)
	}
	worktrees.mu.Lock()
	created := len(worktrees.created)
	worktrees.mu.Unlock()
	if !slices.Equal(claims, []bool{false}) || created != 0 || worker.state != protocol.WorkerIdle ||
		owner.state != protocol.WorkerReserved || owner.beadID != bead.ID || eventCount(t, d.db, "assignment_race_detected") != 1 {
		t.Fatalf("duplicate guard = claims %v worktrees %d candidate %q owner %q/%q race events %d",
			claims, created, worker.state, owner.state, owner.beadID, eventCount(t, d.db, "assignment_race_detected"))
	}
}

func TestAssignmentBehaviorMutationSuccessfulDeliveryPersistsProgress(t *testing.T) {
	d, _, _, worker, conn, bead := assignmentBehaviorMutationFixture(t, "mutation-delivery")
	fixedNow := time.Date(2026, 8, 5, 14, 0, 0, 0, time.UTC)
	d.nowFunc = func() time.Time { return fixedNow }
	d.mu.Lock()
	d.escalatedBeads[bead.ID] = true
	d.mu.Unlock()
	var claims []bool
	var outcomes []assignmentSetupOutcome

	if err := d.assignBeadWithClaim(context.Background(), worker, bead, nil,
		func(claimed bool) { claims = append(claims, claimed) },
		func(outcome assignmentSetupOutcome) { outcomes = append(outcomes, outcome) }); err != nil {
		t.Fatalf("assign deliverable bead: %v", err)
	}
	if !slices.Equal(claims, []bool{true}) || !slices.Equal(outcomes, []assignmentSetupOutcome{assignmentSetupDelivered}) {
		t.Fatalf("delivery callbacks = claims %v outcomes %v", claims, outcomes)
	}
	d.mu.Lock()
	_, assigning := d.assigningBeads[bead.ID]
	_, escalated := d.escalatedBeads[bead.ID]
	trackedWorktree := d.worktreeByBead[bead.ID]
	d.mu.Unlock()
	if worker.state != protocol.WorkerBusy || worker.beadID != bead.ID || worker.assignmentID <= 0 ||
		worker.worktree != "/tmp/worktree-"+bead.ID || worker.baseBranch != d.cfg.DefaultBranch ||
		worker.targetBranch != d.cfg.DefaultBranch || !worker.lastProgress.Equal(fixedNow) || assigning || escalated ||
		trackedWorktree != worker.worktree {
		t.Fatalf("delivered worker/state = %+v assigning=%v escalated=%v tracked=%q",
			worker, assigning, escalated, trackedWorktree)
	}
	if eventCount(t, d.db, "assign") != 1 || eventCount(t, d.db, "worker_progress") != 1 {
		t.Fatalf("delivery events assign/progress = %d/%d, want 1/1",
			eventCount(t, d.db, "assign"), eventCount(t, d.db, "worker_progress"))
	}
	var progressSource string
	if err := d.db.QueryRow(`SELECT source FROM events WHERE type='worker_progress' AND bead_id=?`, bead.ID).Scan(&progressSource); err != nil || progressSource != "assign" {
		t.Fatalf("worker progress source = %q err=%v, want assign", progressSource, err)
	}
	var activeWorker, activeWorktree, targetBranch string
	if err := d.db.QueryRow(`SELECT worker_id, worktree, target_branch FROM assignments WHERE id=? AND status='active'`,
		worker.assignmentID).Scan(&activeWorker, &activeWorktree, &targetBranch); err != nil {
		t.Fatalf("load active assignment: %v", err)
	}
	if activeWorker != worker.id || activeWorktree != worker.worktree || targetBranch != worker.targetBranch {
		t.Fatalf("active assignment = %q/%q/%q, want %q/%q/%q",
			activeWorker, activeWorktree, targetBranch, worker.id, worker.worktree, worker.targetBranch)
	}
	conn.mu.Lock()
	writes := append([][]byte(nil), conn.written...)
	conn.mu.Unlock()
	if len(writes) != 1 {
		t.Fatalf("ASSIGN writes = %d, want 1", len(writes))
	}
	var message protocol.Message
	if err := json.Unmarshal(writes[0], &message); err != nil || message.Type != protocol.MsgAssign || message.Assign == nil ||
		message.Assign.BeadID != bead.ID || message.Assign.Worktree != worker.worktree {
		t.Fatalf("ASSIGN message = %+v err=%v", message, err)
	}
}

func TestAssignmentBehaviorMutationStatusFailureReleasesClaimAndMutex(t *testing.T) {
	d, beads, _, worker, _, bead := assignmentBehaviorMutationFixture(t, "mutation-status-failure")
	beads.updateErrs = map[string]error{bead.ID: errors.New("injected status failure")}
	var claims []bool

	if err := d.assignBeadWithClaim(context.Background(), worker, bead, nil,
		func(claimed bool) { claims = append(claims, claimed) }, nil); err != nil {
		t.Fatalf("assign with status failure: %v", err)
	}
	d.mu.Lock()
	_, assigning := d.assigningBeads[bead.ID]
	_, failed := d.worktreeFailures[bead.ID]
	d.mu.Unlock()
	if !slices.Equal(claims, []bool{true}) || assigning || !failed || worker.state != protocol.WorkerIdle ||
		eventCount(t, d.db, "update_status_failed") != 1 {
		t.Fatalf("status failure cleanup = claims %v assigning %v failed %v worker %q events %d",
			claims, assigning, failed, worker.state, eventCount(t, d.db, "update_status_failed"))
	}
	lockAvailable := make(chan struct{})
	go func() {
		d.mu.Lock()
		d.mu.Unlock()
		close(lockAvailable)
	}()
	select {
	case <-lockAvailable:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("status failure returned while retaining dispatcher mutex")
	}
}

func TestAssignmentBehaviorMutationFocusChangeAbortsPreparedWork(t *testing.T) {
	d, beads, worktrees, worker, conn, bead := assignmentBehaviorMutationFixture(t, "mutation-focus-abort")
	worktrees.createFn = func(context.Context, string, string) (string, string, error) {
		d.mu.Lock()
		d.focusVersion++
		d.mu.Unlock()
		return "/tmp/worktree-" + bead.ID, protocol.BranchPrefix + bead.ID, nil
	}
	var outcomes []assignmentSetupOutcome

	if err := d.assignBeadWithClaim(context.Background(), worker, bead, []uint64{0}, nil,
		func(outcome assignmentSetupOutcome) { outcomes = append(outcomes, outcome) }); err != nil {
		t.Fatalf("assign across focus change: %v", err)
	}
	d.mu.Lock()
	_, assigning := d.assigningBeads[bead.ID]
	_, tracked := d.worktreeByBead[bead.ID]
	d.mu.Unlock()
	beads.mu.Lock()
	status := beads.updated[bead.ID]
	beads.mu.Unlock()
	worktrees.mu.Lock()
	removed := slices.Clone(worktrees.removed)
	worktrees.mu.Unlock()
	conn.mu.Lock()
	writes := len(conn.written)
	conn.mu.Unlock()
	if !slices.Equal(outcomes, []assignmentSetupOutcome{assignmentSetupNotDelivered}) || worker.state != protocol.WorkerIdle ||
		assigning || tracked || status != "open" || !slices.Equal(removed, []string{"/tmp/worktree-" + bead.ID}) ||
		writes != 0 || eventCount(t, d.db, "assignment_aborted_focus_changed") != 1 {
		t.Fatalf("focus abort = outcomes %v worker %q assigning %v tracked %v status %q removed %v writes %d events %d",
			outcomes, worker.state, assigning, tracked, status, removed, writes,
			eventCount(t, d.db, "assignment_aborted_focus_changed"))
	}
}

func TestAssignmentBehaviorMutationAtomicObservationFailureIsAudited(t *testing.T) {
	d, _, worktrees, worker, _, bead := assignmentBehaviorMutationFixture(t, "mutation-observation-failure")
	worktrees.createFn = func(context.Context, string, string) (string, string, error) {
		if _, err := d.db.Exec(`DROP VIEW review_checkpoints_blocking_assignment`); err != nil {
			return "", "", err
		}
		return "/tmp/worktree-" + bead.ID, protocol.BranchPrefix + bead.ID, nil
	}

	if err := d.assignBeadWithClaim(context.Background(), worker, bead, nil, nil, nil); err != nil {
		t.Fatalf("assign with failed checkpoint observation: %v", err)
	}
	worktrees.mu.Lock()
	removed := slices.Clone(worktrees.removed)
	worktrees.mu.Unlock()
	if eventCount(t, d.db, "review_checkpoint_assignment_recheck_failed") != 1 ||
		!slices.Equal(removed, []string{"/tmp/worktree-" + bead.ID}) || worker.state != protocol.WorkerIdle {
		t.Fatalf("observation failure = events %d removed %v worker %q",
			eventCount(t, d.db, "review_checkpoint_assignment_recheck_failed"), removed, worker.state)
	}
}

func TestAssignmentBehaviorMutationCapabilityCleanupFailureIsAudited(t *testing.T) {
	d, _, worktrees, worker, conn, bead := assignmentBehaviorMutationFixture(t, "mutation-capability-cleanup")
	if _, err := d.db.Exec(`
CREATE TRIGGER mutation_fail_capability
BEFORE INSERT ON assignment_capabilities
BEGIN
  SELECT RAISE(ABORT, 'injected capability failure');
END;
CREATE TRIGGER mutation_fail_assignment_completion
BEFORE UPDATE OF status ON assignments
WHEN NEW.status = 'completed'
BEGIN
  SELECT RAISE(ABORT, 'injected assignment completion failure');
END;`); err != nil {
		t.Fatalf("install cleanup failure triggers: %v", err)
	}

	if err := d.assignBeadWithClaim(context.Background(), worker, bead, nil, nil, nil); err != nil {
		t.Fatalf("assign with capability failure: %v", err)
	}
	worktrees.mu.Lock()
	removed := slices.Clone(worktrees.removed)
	worktrees.mu.Unlock()
	conn.mu.Lock()
	writes := len(conn.written)
	conn.mu.Unlock()
	if eventCount(t, d.db, "assignment_capability_issue_failed") != 1 || eventCount(t, d.db, "assignment_cleanup_failed") != 1 ||
		!slices.Equal(removed, []string{"/tmp/worktree-" + bead.ID}) || worker.state != protocol.WorkerIdle || writes != 0 {
		t.Fatalf("capability cleanup = issue events %d cleanup events %d removed %v worker %q writes %d",
			eventCount(t, d.db, "assignment_capability_issue_failed"), eventCount(t, d.db, "assignment_cleanup_failed"),
			removed, worker.state, writes)
	}
}
