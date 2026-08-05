package dispatcher //nolint:testpackage // targeted white-box tests exercise assignment behavior

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"oro/pkg/beadstore"
	"oro/pkg/dbutil"
	"oro/pkg/protocol"
)

// assignmentBehaviorStore keeps the harness on the production SQLite store
// while exposing the one fault seam needed by the status-failure contract.
type assignmentBehaviorStore struct {
	DeferredStore

	mu         sync.Mutex
	updateErrs map[string]error
	updated    map[string]string
}

func (s *assignmentBehaviorStore) Update(ctx context.Context, id string, params beadstore.UpdateParams) error {
	s.mu.Lock()
	err := s.updateErrs[id]
	s.mu.Unlock()
	if err != nil {
		return err
	}
	if err := s.DeferredStore.Update(ctx, id, params); err != nil {
		return err
	}
	if params.Status != nil {
		s.mu.Lock()
		s.updated[id] = *params.Status
		s.mu.Unlock()
	}
	return nil
}

func (s *assignmentBehaviorStore) setStatus(t *testing.T, id, status string) {
	t.Helper()
	if err := s.DeferredStore.Update(t.Context(), id, beadstore.UpdateParams{Status: &status}); err != nil {
		t.Fatalf("set bead %s status: %v", id, err)
	}
}

type assignmentBehaviorWorktrees struct {
	mu       sync.Mutex
	created  map[string]string
	removed  []string
	createFn func(context.Context, string, string) (string, string, error)
}

func (w *assignmentBehaviorWorktrees) setCreateFn(fn func(context.Context, string, string) (string, string, error)) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.createFn = fn
}

func (w *assignmentBehaviorWorktrees) removedCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.removed)
}

func (w *assignmentBehaviorWorktrees) removedSince(index int) []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.removed[index:])
}

func (w *assignmentBehaviorWorktrees) createdBead(beadID string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, ok := w.created[beadID]
	return ok
}

func (w *assignmentBehaviorWorktrees) Create(ctx context.Context, beadID, baseBranch string) (string, string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.createFn != nil {
		return w.createFn(ctx, beadID, baseBranch)
	}
	path := "/tmp/worktree-" + beadID
	w.created[beadID] = path
	return path, protocol.BranchPrefix + beadID, nil
}

func (w *assignmentBehaviorWorktrees) Remove(_ context.Context, path string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.removed = append(w.removed, path)
	return nil
}

func (*assignmentBehaviorWorktrees) Prune(context.Context) error                { return nil }
func (*assignmentBehaviorWorktrees) DeleteBranch(context.Context, string) error { return nil }
func (*assignmentBehaviorWorktrees) DeleteBranchMergedInto(context.Context, string, string) error {
	return nil
}
func (*assignmentBehaviorWorktrees) ForceDeleteBranch(context.Context, string) error { return nil }
func (*assignmentBehaviorWorktrees) BranchExists(context.Context, string) (bool, error) {
	return false, nil
}
func (*assignmentBehaviorWorktrees) MergeFFOnly(context.Context, string, string) (string, error) {
	return "", nil
}
func (*assignmentBehaviorWorktrees) UpdateBranchRef(context.Context, string, string) error {
	return nil
}
func (*assignmentBehaviorWorktrees) BranchHead(_ context.Context, branch string) (string, error) {
	return "sha-" + strings.TrimSpace(branch), nil
}
func (*assignmentBehaviorWorktrees) GCClosedWorktrees(context.Context, func(string) bool) error {
	return nil
}
func (*assignmentBehaviorWorktrees) Exists(context.Context, string) bool { return true }
func (*assignmentBehaviorWorktrees) CurrentBranch(_ context.Context, path string) (string, error) {
	return protocol.BranchPrefix + strings.TrimPrefix(filepath.Base(path), "worktree-"), nil
}
func (*assignmentBehaviorWorktrees) RebaseOnto(context.Context, string, string) error { return nil }
func (*assignmentBehaviorWorktrees) PushBranch(context.Context, string) error         { return nil }
func (*assignmentBehaviorWorktrees) CreateBranch(context.Context, string, string) error {
	return nil
}

type assignmentBehaviorConn struct {
	mu      sync.Mutex
	written [][]byte
	closed  bool
}

func (c *assignmentBehaviorConn) Read([]byte) (int, error) { return 0, net.ErrClosed }
func (c *assignmentBehaviorConn) Write(data []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return 0, net.ErrClosed
	}
	c.written = append(c.written, slices.Clone(data))
	return len(data), nil
}
func (c *assignmentBehaviorConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	return nil
}
func (*assignmentBehaviorConn) LocalAddr() net.Addr              { return nil }
func (*assignmentBehaviorConn) RemoteAddr() net.Addr             { return nil }
func (*assignmentBehaviorConn) SetDeadline(time.Time) error      { return nil }
func (*assignmentBehaviorConn) SetReadDeadline(time.Time) error  { return nil }
func (*assignmentBehaviorConn) SetWriteDeadline(time.Time) error { return nil }

func assignmentBehaviorDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := dbutil.OpenDB(filepath.Join(t.TempDir(), "dispatcher.db"))
	if err != nil {
		t.Fatalf("open assignment harness db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(protocol.SchemaDDL); err != nil {
		t.Fatalf("initialize dispatcher schema: %v", err)
	}
	if err := protocol.InitializeBeadSchema(t.Context(), db); err != nil {
		t.Fatalf("initialize bead schema: %v", err)
	}
	if _, err := db.Exec(protocol.MigrateSemanticMemorySearchEvents); err != nil {
		t.Fatalf("initialize semantic search events: %v", err)
	}
	if _, err := db.Exec(protocol.MigrateSemanticMemoryReadEvents); err != nil {
		t.Fatalf("initialize semantic read events: %v", err)
	}
	return db
}

func assignmentBehaviorEventCount(t *testing.T, db *sql.DB, eventType string) int {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM events WHERE type=?`, eventType).Scan(&count); err != nil {
		t.Fatalf("count %s events: %v", eventType, err)
	}
	return count
}

type assignmentBehaviorHarness struct {
	d         *Dispatcher
	beads     *assignmentBehaviorStore
	worktrees *assignmentBehaviorWorktrees
}

func newAssignmentBehaviorHarness(t *testing.T) *assignmentBehaviorHarness {
	t.Helper()
	db := assignmentBehaviorDB(t)
	store := beadstore.NewSQLiteStore(db)
	beads := &assignmentBehaviorStore{
		DeferredStore: store,
		updateErrs:    make(map[string]error),
		updated:       make(map[string]string),
	}
	worktrees := &assignmentBehaviorWorktrees{created: make(map[string]string)}
	d, err := New(Config{
		RepoRoot:          t.TempDir(),
		ReviewEvidenceDir: filepath.Join(t.TempDir(), "review-evidence"),
		MaxWorkers:        1,
		DefaultBranch:     "main",
	}, db, nil, nil, beads, worktrees, nil, nil)
	if err != nil {
		t.Fatalf("create assignment dispatcher: %v", err)
	}
	return &assignmentBehaviorHarness{d: d, beads: beads, worktrees: worktrees}
}

func (h *assignmentBehaviorHarness) prepareCase(t *testing.T, beadID string) {
	t.Helper()
	h.worktrees.mu.Lock()
	h.worktrees.createFn = nil
	h.worktrees.mu.Unlock()
	h.beads.mu.Lock()
	clear(h.beads.updateErrs)
	h.beads.mu.Unlock()
	h.d.nowFunc = time.Now

	h.d.mu.Lock()
	_, assigning := h.d.assigningBeads[beadID]
	_, tracked := h.d.worktreeByBead[beadID]
	_, failed := h.d.worktreeFailures[beadID]
	h.d.mu.Unlock()
	var durableRows int
	if err := h.d.db.QueryRow(`SELECT COUNT(*) FROM assignments WHERE bead_id=?`, beadID).Scan(&durableRows); err != nil {
		t.Fatalf("check prior assignments for %s: %v", beadID, err)
	}
	if assigning || tracked || failed || durableRows != 0 {
		t.Fatalf("case %s inherited state: assigning=%v tracked=%v failed=%v durable_rows=%d",
			beadID, assigning, tracked, failed, durableRows)
	}
}

func (h *assignmentBehaviorHarness) fixture(
	t *testing.T,
	beadID string,
) (*Dispatcher, *assignmentBehaviorStore, *assignmentBehaviorWorktrees, *trackedWorker, *assignmentBehaviorConn, protocol.Bead) {
	t.Helper()
	h.prepareCase(t, beadID)
	bead, err := h.beads.Create(t.Context(), beadstore.CreateParams{
		ID:                 beadID,
		Title:              "Mutation assignment",
		Type:               "task",
		Status:             "open",
		AcceptanceCriteria: "Test: assignment behavior | Assert: durable state is exact",
	})
	if err != nil {
		t.Fatalf("seed assignment bead: %v", err)
	}
	conn := &assignmentBehaviorConn{}
	worker := &trackedWorker{
		id:      "mutation-worker-" + beadID,
		state:   protocol.WorkerIdle,
		conn:    conn,
		encoder: json.NewEncoder(conn),
	}
	h.d.mu.Lock()
	h.d.workers[worker.id] = worker
	h.d.mu.Unlock()
	return h.d, h.beads, h.worktrees, worker, conn, *bead
}

func assignmentBehaviorReadinessStopsBeforeClaim(t *testing.T, h *assignmentBehaviorHarness) {
	d, beads, worktrees, worker, _, bead := h.fixture(t, "mutation-readiness")
	beads.setStatus(t, bead.ID, "closed")
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
	created := worktrees.createdBead(bead.ID)
	if created || worker.state != protocol.WorkerIdle || assignmentBehaviorEventCount(t, d.db, "assign") != 0 {
		t.Fatalf("closed bead side effects = worktree %v state %q assign events %d",
			created, worker.state, assignmentBehaviorEventCount(t, d.db, "assign"))
	}
}

func assignmentBehaviorReservedOwnerBlocksDuplicate(t *testing.T, h *assignmentBehaviorHarness) {
	d, _, worktrees, worker, _, bead := h.fixture(t, "mutation-reserved-owner")
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
	created := worktrees.createdBead(bead.ID)
	if !slices.Equal(claims, []bool{false}) || created || worker.state != protocol.WorkerIdle ||
		owner.state != protocol.WorkerReserved || owner.beadID != bead.ID || assignmentBehaviorEventCount(t, d.db, "assignment_race_detected") != 1 {
		t.Fatalf("duplicate guard = claims %v worktree %v candidate %q owner %q/%q race events %d",
			claims, created, worker.state, owner.state, owner.beadID, assignmentBehaviorEventCount(t, d.db, "assignment_race_detected"))
	}
}

func assignmentBehaviorSuccessfulDeliveryPersistsProgress(t *testing.T, h *assignmentBehaviorHarness) {
	d, _, _, worker, conn, bead := h.fixture(t, "mutation-delivery")
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
	if assignmentBehaviorEventCount(t, d.db, "assign") != 1 || assignmentBehaviorEventCount(t, d.db, "worker_progress") != 1 {
		t.Fatalf("delivery events assign/progress = %d/%d, want 1/1",
			assignmentBehaviorEventCount(t, d.db, "assign"), assignmentBehaviorEventCount(t, d.db, "worker_progress"))
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

func assignmentBehaviorStatusFailureReleasesClaimAndMutex(t *testing.T, h *assignmentBehaviorHarness) {
	d, beads, _, worker, _, bead := h.fixture(t, "mutation-status-failure")
	beads.mu.Lock()
	beads.updateErrs[bead.ID] = errors.New("injected status failure")
	beads.mu.Unlock()
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
		assignmentBehaviorEventCount(t, d.db, "update_status_failed") != 1 {
		t.Fatalf("status failure cleanup = claims %v assigning %v failed %v worker %q events %d",
			claims, assigning, failed, worker.state, assignmentBehaviorEventCount(t, d.db, "update_status_failed"))
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

func assignmentBehaviorFocusChangeAbortsPreparedWork(t *testing.T, h *assignmentBehaviorHarness) {
	d, beads, worktrees, worker, conn, bead := h.fixture(t, "mutation-focus-abort")
	removedBefore := worktrees.removedCount()
	worktrees.setCreateFn(func(context.Context, string, string) (string, string, error) {
		d.mu.Lock()
		d.focusVersion++
		d.mu.Unlock()
		return "/tmp/worktree-" + bead.ID, protocol.BranchPrefix + bead.ID, nil
	})
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
	removed := worktrees.removedSince(removedBefore)
	conn.mu.Lock()
	writes := len(conn.written)
	conn.mu.Unlock()
	if !slices.Equal(outcomes, []assignmentSetupOutcome{assignmentSetupNotDelivered}) || worker.state != protocol.WorkerIdle ||
		assigning || tracked || status != "open" || !slices.Equal(removed, []string{"/tmp/worktree-" + bead.ID}) ||
		writes != 0 || assignmentBehaviorEventCount(t, d.db, "assignment_aborted_focus_changed") != 1 {
		t.Fatalf("focus abort = outcomes %v worker %q assigning %v tracked %v status %q removed %v writes %d events %d",
			outcomes, worker.state, assigning, tracked, status, removed, writes,
			assignmentBehaviorEventCount(t, d.db, "assignment_aborted_focus_changed"))
	}
}

func assignmentBehaviorAtomicObservationFailureIsAudited(t *testing.T, h *assignmentBehaviorHarness) {
	d, _, worktrees, worker, _, bead := h.fixture(t, "mutation-observation-failure")
	removedBefore := worktrees.removedCount()
	worktrees.setCreateFn(func(context.Context, string, string) (string, string, error) {
		if _, err := d.db.Exec(`DROP VIEW review_checkpoints_blocking_assignment`); err != nil {
			return "", "", err
		}
		return "/tmp/worktree-" + bead.ID, protocol.BranchPrefix + bead.ID, nil
	})

	if err := d.assignBeadWithClaim(context.Background(), worker, bead, nil, nil, nil); err != nil {
		t.Fatalf("assign with failed checkpoint observation: %v", err)
	}
	removed := worktrees.removedSince(removedBefore)
	if assignmentBehaviorEventCount(t, d.db, "review_checkpoint_assignment_recheck_failed") != 1 ||
		!slices.Equal(removed, []string{"/tmp/worktree-" + bead.ID}) || worker.state != protocol.WorkerIdle {
		t.Fatalf("observation failure = events %d removed %v worker %q",
			assignmentBehaviorEventCount(t, d.db, "review_checkpoint_assignment_recheck_failed"), removed, worker.state)
	}
}

func assignmentBehaviorCapabilityCleanupFailureIsAudited(t *testing.T, h *assignmentBehaviorHarness) {
	d, _, worktrees, worker, conn, bead := h.fixture(t, "mutation-capability-cleanup")
	removedBefore := worktrees.removedCount()
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
	removed := worktrees.removedSince(removedBefore)
	conn.mu.Lock()
	writes := len(conn.written)
	conn.mu.Unlock()
	if assignmentBehaviorEventCount(t, d.db, "assignment_capability_issue_failed") != 1 || assignmentBehaviorEventCount(t, d.db, "assignment_cleanup_failed") != 1 ||
		!slices.Equal(removed, []string{"/tmp/worktree-" + bead.ID}) || worker.state != protocol.WorkerIdle || writes != 0 {
		t.Fatalf("capability cleanup = issue events %d cleanup events %d removed %v worker %q writes %d",
			assignmentBehaviorEventCount(t, d.db, "assignment_capability_issue_failed"), assignmentBehaviorEventCount(t, d.db, "assignment_cleanup_failed"),
			removed, worker.state, writes)
	}
}

func TestAssignmentBehaviorMutation(t *testing.T) {
	harness := newAssignmentBehaviorHarness(t)
	tests := []struct {
		name string
		run  func(*testing.T, *assignmentBehaviorHarness)
	}{
		{name: "readiness stops before claim", run: assignmentBehaviorReadinessStopsBeforeClaim},
		{name: "reserved owner blocks duplicate", run: assignmentBehaviorReservedOwnerBlocksDuplicate},
		{name: "successful delivery persists progress", run: assignmentBehaviorSuccessfulDeliveryPersistsProgress},
		{name: "status failure releases claim and mutex", run: assignmentBehaviorStatusFailureReleasesClaimAndMutex},
		{name: "focus change aborts prepared work", run: assignmentBehaviorFocusChangeAbortsPreparedWork},
		{name: "capability cleanup failure is audited", run: assignmentBehaviorCapabilityCleanupFailureIsAudited},
		// This case drops a production schema view, so it must remain last.
		{name: "atomic observation failure is audited", run: assignmentBehaviorAtomicObservationFailureIsAudited},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.run(t, harness)
		})
	}
}

func TestStandaloneAssignmentBehaviorHarnessCaseIsolation(t *testing.T) {
	harness := newAssignmentBehaviorHarness(t)
	const priorBead = "mutation-prior-case"
	harness.worktrees.setCreateFn(func(context.Context, string, string) (string, string, error) {
		return "", "", errors.New("leaked create fault")
	})
	harness.beads.mu.Lock()
	harness.beads.updateErrs[priorBead] = errors.New("leaked update fault")
	harness.beads.mu.Unlock()
	harness.d.mu.Lock()
	harness.d.worktreeFailures[priorBead] = time.Now()
	harness.d.mu.Unlock()

	_, beads, worktrees, worker, _, bead := harness.fixture(t, "mutation-isolated-case")
	beads.mu.Lock()
	remainingFaults := len(beads.updateErrs)
	beads.mu.Unlock()
	worktrees.mu.Lock()
	createFaultReset := worktrees.createFn == nil
	worktrees.mu.Unlock()
	if remainingFaults != 0 || !createFaultReset || worker.state != protocol.WorkerIdle || bead.ID != "mutation-isolated-case" {
		t.Fatalf("case isolation = faults %d create_reset %v worker %q bead %q",
			remainingFaults, createFaultReset, worker.state, bead.ID)
	}
}
