package dispatcher //nolint:testpackage // white-box mutation tests exercise assignment setup ownership

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"oro/pkg/beadstore"
	"oro/pkg/dbutil"
	"oro/pkg/protocol"
)

type assignmentBCWorktrees struct {
	currentBranch string
	currentErr    error
	createPath    string
	createBranch  string
	createErr     error
	createCalls   int
	branchExists  bool
	deleteErr     error
	removed       []string
}

func (w *assignmentBCWorktrees) Create(context.Context, string, string) (string, string, error) {
	w.createCalls++
	return w.createPath, w.createBranch, w.createErr
}

func (w *assignmentBCWorktrees) Remove(_ context.Context, path string) error {
	w.removed = append(w.removed, path)
	return nil
}

func (*assignmentBCWorktrees) Prune(context.Context) error                { return nil }
func (*assignmentBCWorktrees) DeleteBranch(context.Context, string) error { return nil }
func (w *assignmentBCWorktrees) DeleteBranchMergedInto(context.Context, string, string) error {
	return w.deleteErr
}
func (*assignmentBCWorktrees) ForceDeleteBranch(context.Context, string) error { return nil }
func (w *assignmentBCWorktrees) BranchExists(context.Context, string) (bool, error) {
	return w.branchExists, nil
}

func (*assignmentBCWorktrees) MergeFFOnly(context.Context, string, string) (string, error) {
	return "", nil
}
func (*assignmentBCWorktrees) UpdateBranchRef(context.Context, string, string) error { return nil }
func (*assignmentBCWorktrees) BranchHead(context.Context, string) (string, error) {
	return "assignment-bc-head", nil
}

func (*assignmentBCWorktrees) GCClosedWorktrees(context.Context, func(string) bool) error {
	return nil
}
func (*assignmentBCWorktrees) Exists(context.Context, string) bool { return true }
func (w *assignmentBCWorktrees) CurrentBranch(context.Context, string) (string, error) {
	return w.currentBranch, w.currentErr
}
func (*assignmentBCWorktrees) RebaseOnto(context.Context, string, string) error { return nil }
func (*assignmentBCWorktrees) PushBranch(context.Context, string) error         { return nil }
func (*assignmentBCWorktrees) CreateBranch(context.Context, string, string) error {
	return nil
}

type assignmentBCPreparingWorktrees struct {
	*assignmentBCWorktrees
	fastForwarded bool
	prepareErr    error
	prepareCalls  int
	rebaseErr     error
	rebaseCalls   int
}

func (w *assignmentBCPreparingWorktrees) PrepareExistingForReuse(
	context.Context,
	string,
	string,
	string,
) (bool, error) {
	w.prepareCalls++
	return w.fastForwarded, w.prepareErr
}

func (w *assignmentBCPreparingWorktrees) RebaseDivergedExistingForReuse(
	context.Context,
	string,
	string,
	string,
) error {
	w.rebaseCalls++
	return w.rebaseErr
}

type assignmentBCHarness struct {
	d     *Dispatcher
	db    *sql.DB
	beads *beadstore.SQLiteStore
}

func newAssignmentBCHarness(t *testing.T, worktrees WorktreeManager) *assignmentBCHarness {
	t.Helper()
	db, err := dbutil.OpenDB(filepath.Join(t.TempDir(), "dispatcher.db"))
	if err != nil {
		t.Fatalf("open assignment B+C database: %v", err)
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
	beads := beadstore.NewSQLiteStore(db)
	d, err := New(Config{
		RepoRoot:          t.TempDir(),
		ReviewEvidenceDir: filepath.Join(t.TempDir(), "review-evidence"),
		MaxWorkers:        1,
		DefaultBranch:     "main",
	}, db, nil, nil, beads, worktrees, nil, nil)
	if err != nil {
		t.Fatalf("create assignment B+C dispatcher: %v", err)
	}
	return &assignmentBCHarness{d: d, db: db, beads: beads}
}

func (h *assignmentBCHarness) seedInProgressBead(t *testing.T, beadID string) {
	t.Helper()
	if _, err := h.beads.Create(t.Context(), beadstore.CreateParams{
		ID:                 beadID,
		Title:              "Assignment survivor",
		Type:               "task",
		Status:             "in_progress",
		AcceptanceCriteria: "Test: assignment setup ownership remains exact",
	}); err != nil {
		t.Fatalf("seed assignment bead %s: %v", beadID, err)
	}
}

func (h *assignmentBCHarness) reserveWorker(
	workerID, beadID string,
	reservationGen uint64,
) *trackedWorker {
	w := &trackedWorker{
		id:             workerID,
		state:          protocol.WorkerReserved,
		beadID:         beadID,
		reservationGen: reservationGen,
	}
	h.d.mu.Lock()
	h.d.workers[workerID] = w
	h.d.assigningBeads[beadID] = true
	h.d.mu.Unlock()
	return w
}

func assignmentBCEventCount(t *testing.T, db *sql.DB, eventType, beadID string) int {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM events WHERE type=? AND bead_id=?`, eventType, beadID).Scan(&count); err != nil {
		t.Fatalf("count %s events for %s: %v", eventType, beadID, err)
	}
	return count
}

func assignmentBCBeadStatus(t *testing.T, h *assignmentBCHarness, beadID string) string {
	t.Helper()
	detail, err := h.beads.Show(t.Context(), beadID)
	if err != nil || detail == nil {
		t.Fatalf("show bead %s: detail=%+v err=%v", beadID, detail, err)
	}
	return detail.Status
}

func assignmentBCAssertLockAvailable(t *testing.T, d *Dispatcher) {
	t.Helper()
	acquired := make(chan struct{})
	go func() {
		d.mu.Lock()
		d.mu.Unlock() //nolint:staticcheck // lock/unlock completion is the bounded mutex-release assertion
		close(acquired)
	}()
	select {
	case <-acquired:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("assignment helper returned while retaining dispatcher mutex")
	}
}

func TestAssignmentBCReservationReleaseExactState(t *testing.T) {
	worktrees := &assignmentBCWorktrees{}
	h := newAssignmentBCHarness(t, worktrees)
	fixedNow := time.Date(2026, time.August, 5, 15, 0, 0, 0, time.UTC)
	h.d.nowFunc = func() time.Time { return fixedNow }
	w := h.reserveWorker("assignment-bc-release", "assignment-bc-bead", 41)
	w.assignmentID = 73
	w.epicID = "assignment-bc-epic"
	w.isEpicDecomp = true
	w.worktree = "/tmp/assignment-bc-release"
	w.baseBranch = "epic/assignment-bc-epic"
	w.targetBranch = "epic/assignment-bc-epic"
	w.runtime = "codex"
	w.model = "gpt-assignment-bc"
	w.reasoning = "high"
	w.lastProgress = fixedNow.Add(-time.Hour)
	w.setupReservedAt = fixedNow.Add(-time.Minute)

	h.d.mu.Lock()
	released := h.d.releaseAssignmentReservationLocked(w.id, w.beadID, w.reservationGen)
	h.d.mu.Unlock()
	if !released {
		t.Fatal("matching assignment reservation was not released")
	}
	if w.state != protocol.WorkerIdle || w.assignmentID != 0 || w.beadID != "" || w.epicID != "" ||
		w.isEpicDecomp || w.worktree != "" || w.baseBranch != "" || w.targetBranch != "" ||
		w.runtime != "" || w.model != "" || w.reasoning != "" || !w.lastProgress.Equal(fixedNow) ||
		!w.setupReservedAt.IsZero() || w.reservationGen != 42 {
		t.Fatalf("released reservation retained state: %+v", w)
	}
	assignmentBCAssertLockAvailable(t, h.d)

	for _, test := range []struct {
		name       string
		workerID   string
		beadID     string
		generation uint64
		mutate     func(*trackedWorker)
	}{
		{name: "missing worker", workerID: "assignment-bc-missing", beadID: "bead", generation: 1},
		{name: "wrong state", workerID: w.id, beadID: "", generation: 42, mutate: func(w *trackedWorker) { w.state = protocol.WorkerBusy }},
		{name: "wrong bead", workerID: w.id, beadID: "other", generation: 42},
		{name: "wrong generation", workerID: w.id, beadID: "", generation: 43},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.mutate != nil {
				test.mutate(w)
				defer func() { w.state = protocol.WorkerIdle }()
			}
			before := *w
			h.d.mu.Lock()
			got := h.d.releaseAssignmentReservationLocked(test.workerID, test.beadID, test.generation)
			h.d.mu.Unlock()
			if got || !reflect.DeepEqual(w, &before) {
				t.Fatalf("mismatched release = %v worker=%+v, want false and unchanged %+v", got, w, before)
			}
		})
	}

	for _, test := range []struct {
		name          string
		workerID      string
		reservedBead  string
		requestedBead string
		reservedGen   uint64
		requestedGen  uint64
	}{
		{
			name: "bead mismatch is independently rejected", workerID: "assignment-bc-release-bead-mismatch",
			reservedBead: "assignment-bc-release-owner", requestedBead: "assignment-bc-release-contender",
			reservedGen: 51, requestedGen: 51,
		},
		{
			name: "generation mismatch is independently rejected", workerID: "assignment-bc-release-gen-mismatch",
			reservedBead: "assignment-bc-release-generation", requestedBead: "assignment-bc-release-generation",
			reservedGen: 61, requestedGen: 62,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			reserved := h.reserveWorker(test.workerID, test.reservedBead, test.reservedGen)
			before := *reserved
			h.d.mu.Lock()
			got := h.d.releaseAssignmentReservationLocked(test.workerID, test.requestedBead, test.requestedGen)
			h.d.mu.Unlock()
			if got || !reflect.DeepEqual(reserved, &before) {
				t.Fatalf("independent mismatch release = %v worker=%+v, want false and unchanged %+v", got, reserved, before)
			}
		})
	}
}

func TestAssignmentBCAttachExactStateAndOwnership(t *testing.T) {
	h := newAssignmentBCHarness(t, &assignmentBCWorktrees{})
	w := h.reserveWorker("assignment-bc-attach", "assignment-bc-attach-bead", 7)
	attached := h.d.attachAssignmentToReservation(
		w.id, w.beadID, w.reservationGen, 88, "/tmp/assignment-bc-attach",
		"epic/assignment-bc", "epic/assignment-bc", "assignment-bc-epic", true,
	)
	if !attached || w.assignmentID != 88 || w.worktree != "/tmp/assignment-bc-attach" ||
		w.baseBranch != "epic/assignment-bc" || w.targetBranch != "epic/assignment-bc" ||
		w.epicID != "assignment-bc-epic" || !w.isEpicDecomp {
		t.Fatalf("attached assignment state = attached %v worker %+v", attached, w)
	}
	assignmentBCAssertLockAvailable(t, h.d)

	for _, test := range []struct {
		name       string
		workerID   string
		beadID     string
		generation uint64
	}{
		{name: "missing worker", workerID: "assignment-bc-attach-missing", beadID: w.beadID, generation: w.reservationGen},
		{name: "wrong bead", workerID: w.id, beadID: "assignment-bc-attach-other", generation: w.reservationGen},
		{name: "wrong generation", workerID: w.id, beadID: w.beadID, generation: w.reservationGen + 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			before := *w
			got := h.d.attachAssignmentToReservation(
				test.workerID, test.beadID, test.generation, 99, "/tmp/wrong", "wrong-base", "wrong-target", "wrong-epic", false,
			)
			if got || !reflect.DeepEqual(w, &before) {
				t.Fatalf("mismatched attach = %v worker=%+v, want false and unchanged %+v", got, w, before)
			}
		})
	}
}

func TestAssignmentBCPrepareWorktreeOutcomes(t *testing.T) {
	ctx := context.Background()

	t.Run("existing worktree is reused and audited", func(t *testing.T) {
		const beadID = "assignment-bc-existing"
		worktrees := &assignmentBCWorktrees{currentBranch: protocol.BranchPrefix + beadID}
		h := newAssignmentBCHarness(t, worktrees)
		h.seedInProgressBead(t, beadID)
		worktree, branch, created := h.d.prepareAssignmentWorktree(
			ctx, beadID, "worker-existing", 1, "/tmp/assignment-bc-existing", "main", "main",
		)
		if worktree != "/tmp/assignment-bc-existing" || branch != protocol.BranchPrefix+beadID || created ||
			assignmentBCEventCount(t, h.db, "worktree_reused", beadID) != 1 {
			t.Fatalf("existing preparation = %q/%q created %v events %d", worktree, branch, created,
				assignmentBCEventCount(t, h.db, "worktree_reused", beadID))
		}
	})

	t.Run("branch mismatch is durably quarantined and reopened", func(t *testing.T) {
		const beadID = "assignment-bc-mismatch"
		worktrees := &assignmentBCWorktrees{currentBranch: "wrong/branch"}
		h := newAssignmentBCHarness(t, worktrees)
		h.seedInProgressBead(t, beadID)
		h.d.mu.Lock()
		h.d.assigningBeads[beadID] = true
		h.d.mu.Unlock()
		worktree, branch, created := h.d.prepareAssignmentWorktree(
			ctx, beadID, "worker-mismatch", 1, "/tmp/assignment-bc-mismatch", "main", "main",
		)
		if worktree != "" || branch != "" || created {
			t.Fatalf("mismatch preparation = %q/%q created %v", worktree, branch, created)
		}
		var reason, details string
		if err := h.db.QueryRow(`SELECT reason, details FROM recovery_quarantines WHERE bead_id=? AND status='open'`, beadID).
			Scan(&reason, &details); err != nil {
			t.Fatalf("load mismatch quarantine: %v", err)
		}
		h.d.mu.Lock()
		_, assigning := h.d.assigningBeads[beadID]
		_, failed := h.d.worktreeFailures[beadID]
		tracked := h.d.worktreeByBead[beadID]
		h.d.mu.Unlock()
		if reason != "branch_worktree_mismatch" || !strings.Contains(details, "expected agent branch") ||
			assigning || !failed || tracked != "/tmp/assignment-bc-mismatch" || assignmentBCBeadStatus(t, h, beadID) != "open" ||
			assignmentBCEventCount(t, h.db, "recovery_work_quarantined", beadID) != 1 {
			t.Fatalf("mismatch durable state = reason %q details %q assigning %v failed %v tracked %q status %q",
				reason, details, assigning, failed, tracked, assignmentBCBeadStatus(t, h, beadID))
		}
		assignmentBCAssertLockAvailable(t, h.d)
	})

	t.Run("preparer fast forward is audited", func(t *testing.T) {
		const beadID = "assignment-bc-fast-forward"
		base := &assignmentBCWorktrees{currentBranch: protocol.BranchPrefix + beadID}
		worktrees := &assignmentBCPreparingWorktrees{assignmentBCWorktrees: base, fastForwarded: true}
		h := newAssignmentBCHarness(t, worktrees)
		h.seedInProgressBead(t, beadID)
		worktree, branch, created := h.d.prepareAssignmentWorktree(
			ctx, beadID, "worker-fast-forward", 1, "/tmp/assignment-bc-fast-forward", "main", "main",
		)
		if worktree == "" || branch != protocol.BranchPrefix+beadID || created || worktrees.prepareCalls != 1 ||
			assignmentBCEventCount(t, h.db, "worktree_fast_forwarded", beadID) != 1 {
			t.Fatalf("fast-forward preparation = %q/%q created %v calls %d events %d", worktree, branch, created,
				worktrees.prepareCalls, assignmentBCEventCount(t, h.db, "worktree_fast_forwarded", beadID))
		}
	})

	t.Run("unsafe prepare failure is durably quarantined", func(t *testing.T) {
		const beadID = "assignment-bc-unsafe"
		base := &assignmentBCWorktrees{currentBranch: protocol.BranchPrefix + beadID}
		worktrees := &assignmentBCPreparingWorktrees{
			assignmentBCWorktrees: base,
			prepareErr:            errors.New("injected unsafe preparation failure"),
		}
		h := newAssignmentBCHarness(t, worktrees)
		h.seedInProgressBead(t, beadID)
		h.d.mu.Lock()
		h.d.assigningBeads[beadID] = true
		h.d.mu.Unlock()
		worktree, branch, created := h.d.prepareAssignmentWorktree(
			ctx, beadID, "worker-unsafe", 1, "/tmp/assignment-bc-unsafe", "main", "main",
		)
		if worktree != "" || branch != "" || created {
			t.Fatalf("unsafe preparation = %q/%q created %v", worktree, branch, created)
		}
		var reason, details string
		if err := h.db.QueryRow(`SELECT reason, details FROM recovery_quarantines WHERE bead_id=? AND status='open'`, beadID).
			Scan(&reason, &details); err != nil {
			t.Fatalf("load unsafe quarantine: %v", err)
		}
		h.d.mu.Lock()
		_, assigning := h.d.assigningBeads[beadID]
		_, failed := h.d.worktreeFailures[beadID]
		h.d.mu.Unlock()
		if reason != "unsafe_stale_branch" || !strings.Contains(details, "injected unsafe preparation failure") ||
			assigning || !failed || assignmentBCBeadStatus(t, h, beadID) != "open" {
			t.Fatalf("unsafe durable state = reason %q details %q assigning %v failed %v status %q",
				reason, details, assigning, failed, assignmentBCBeadStatus(t, h, beadID))
		}
		assignmentBCAssertLockAvailable(t, h.d)
	})

	t.Run("fresh creation failure is audited and reopened", func(t *testing.T) {
		const beadID = "assignment-bc-create-error"
		worktrees := &assignmentBCWorktrees{createErr: errors.New("injected assignment B+C create failure")}
		h := newAssignmentBCHarness(t, worktrees)
		h.seedInProgressBead(t, beadID)
		h.d.mu.Lock()
		h.d.assigningBeads[beadID] = true
		h.d.mu.Unlock()
		worktree, branch, created := h.d.prepareAssignmentWorktree(ctx, beadID, "worker-create-error", 1, "", "main", "main")
		h.d.mu.Lock()
		_, assigning := h.d.assigningBeads[beadID]
		_, failed := h.d.worktreeFailures[beadID]
		h.d.mu.Unlock()
		if worktree != "" || branch != "" || created || assigning || !failed ||
			assignmentBCBeadStatus(t, h, beadID) != "open" || assignmentBCEventCount(t, h.db, "worktree_error", beadID) != 1 {
			t.Fatalf("create error state = %q/%q created %v assigning %v failed %v status %q events %d",
				worktree, branch, created, assigning, failed, assignmentBCBeadStatus(t, h, beadID),
				assignmentBCEventCount(t, h.db, "worktree_error", beadID))
		}
		assignmentBCAssertLockAvailable(t, h.d)
	})

	t.Run("rejected stale branch cannot continue to fresh creation", func(t *testing.T) {
		const beadID = "assignment-bc-stale-rejected"
		worktrees := &assignmentBCWorktrees{
			branchExists: true,
			deleteErr:    errors.New("injected stale branch safety rejection"),
			createPath:   "/tmp/assignment-bc-must-not-create",
			createBranch: protocol.BranchPrefix + beadID,
		}
		h := newAssignmentBCHarness(t, worktrees)
		h.seedInProgressBead(t, beadID)
		h.d.mu.Lock()
		h.d.assigningBeads[beadID] = true
		h.d.mu.Unlock()
		worktree, branch, created := h.d.prepareAssignmentWorktree(ctx, beadID, "worker-stale-rejected", 1, "", "main", "main")
		if worktree != "" || branch != "" || created || worktrees.createCalls != 0 ||
			assignmentBCBeadStatus(t, h, beadID) != "open" {
			t.Fatalf("rejected stale branch = %q/%q created %v create_calls %d status %q",
				worktree, branch, created, worktrees.createCalls, assignmentBCBeadStatus(t, h, beadID))
		}
		var reason string
		if err := h.db.QueryRow(`SELECT reason FROM recovery_quarantines WHERE bead_id=? AND status='open'`, beadID).
			Scan(&reason); err != nil || reason != "unsafe_stale_branch" {
			t.Fatalf("stale branch quarantine reason = %q err=%v", reason, err)
		}
	})

	t.Run("fresh creation publishes only to matching reservation", func(t *testing.T) {
		const beadID = "assignment-bc-create"
		worktrees := &assignmentBCWorktrees{
			createPath: "/tmp/assignment-bc-create", createBranch: protocol.BranchPrefix + beadID,
		}
		h := newAssignmentBCHarness(t, worktrees)
		h.seedInProgressBead(t, beadID)
		h.reserveWorker("worker-create", beadID, 9)
		worktree, branch, created := h.d.prepareAssignmentWorktree(ctx, beadID, "worker-create", 9, "", "main", "main")
		h.d.mu.Lock()
		tracked := h.d.worktreeByBead[beadID]
		h.d.mu.Unlock()
		if worktree != worktrees.createPath || branch != worktrees.createBranch || !created || tracked != worktrees.createPath {
			t.Fatalf("fresh preparation = %q/%q created %v tracked %q", worktree, branch, created, tracked)
		}
		assignmentBCAssertLockAvailable(t, h.d)
	})

	t.Run("lost reservation removes unpublished fresh worktree", func(t *testing.T) {
		const beadID = "assignment-bc-lost"
		worktrees := &assignmentBCWorktrees{
			createPath: "/tmp/assignment-bc-lost", createBranch: protocol.BranchPrefix + beadID,
		}
		h := newAssignmentBCHarness(t, worktrees)
		h.seedInProgressBead(t, beadID)
		h.reserveWorker("worker-lost", beadID, 10)
		worktree, branch, created := h.d.prepareAssignmentWorktree(ctx, beadID, "worker-lost", 11, "", "main", "main")
		h.d.mu.Lock()
		tracked := h.d.worktreeByBead[beadID]
		h.d.mu.Unlock()
		if worktree != "" || branch != "" || created || tracked != "" ||
			len(worktrees.removed) != 1 || worktrees.removed[0] != worktrees.createPath {
			t.Fatalf("lost preparation = %q/%q created %v tracked %q removed %v", worktree, branch, created, tracked, worktrees.removed)
		}
		assignmentBCAssertLockAvailable(t, h.d)
	})
}

func TestAssignmentBCValidateDivergedRecoveryOutcomes(t *testing.T) {
	ctx := context.Background()

	t.Run("successful rebase admits existing worktree", func(t *testing.T) {
		const beadID = "assignment-bc-rebase-success"
		base := &assignmentBCWorktrees{currentBranch: protocol.BranchPrefix + beadID}
		worktrees := &assignmentBCPreparingWorktrees{
			assignmentBCWorktrees: base,
			prepareErr:            errors.New("agent branch diverged from base"),
		}
		h := newAssignmentBCHarness(t, worktrees)
		h.seedInProgressBead(t, beadID)
		valid := h.d.validateExistingWorktreeForReuse(
			ctx, beadID, "worker-rebase-success", "/tmp/assignment-bc-rebase-success",
			protocol.BranchPrefix+beadID, "main",
		)
		var quarantines int
		if err := h.db.QueryRow(`SELECT COUNT(*) FROM recovery_quarantines WHERE bead_id=?`, beadID).Scan(&quarantines); err != nil {
			t.Fatalf("count successful rebase quarantines: %v", err)
		}
		if !valid || worktrees.rebaseCalls != 1 || quarantines != 0 ||
			assignmentBCEventCount(t, h.db, "worktree_rebased_for_reuse", beadID) != 1 {
			t.Fatalf("successful divergence recovery = valid %v rebase_calls %d quarantines %d events %d",
				valid, worktrees.rebaseCalls, quarantines,
				assignmentBCEventCount(t, h.db, "worktree_rebased_for_reuse", beadID))
		}
	})

	t.Run("failed rebase preserves combined diagnostic", func(t *testing.T) {
		const beadID = "assignment-bc-rebase-failure"
		base := &assignmentBCWorktrees{currentBranch: protocol.BranchPrefix + beadID}
		worktrees := &assignmentBCPreparingWorktrees{
			assignmentBCWorktrees: base,
			prepareErr:            errors.New("agent branch diverged from base"),
			rebaseErr:             errors.New("injected survivor rebase failure"),
		}
		h := newAssignmentBCHarness(t, worktrees)
		h.seedInProgressBead(t, beadID)
		h.d.mu.Lock()
		h.d.assigningBeads[beadID] = true
		h.d.mu.Unlock()
		valid := h.d.validateExistingWorktreeForReuse(
			ctx, beadID, "worker-rebase-failure", "/tmp/assignment-bc-rebase-failure",
			protocol.BranchPrefix+beadID, "main",
		)
		var reason, details string
		if err := h.db.QueryRow(`SELECT reason, details FROM recovery_quarantines WHERE bead_id=? AND status='open'`, beadID).
			Scan(&reason, &details); err != nil {
			t.Fatalf("load failed rebase quarantine: %v", err)
		}
		if valid || worktrees.rebaseCalls != 1 || reason != "unsafe_stale_branch" ||
			!strings.Contains(details, "agent branch diverged from base") ||
			!strings.Contains(details, "injected survivor rebase failure") {
			t.Fatalf("failed divergence recovery = valid %v calls %d reason %q details %q",
				valid, worktrees.rebaseCalls, reason, details)
		}
		assignmentBCAssertLockAvailable(t, h.d)
	})
}

func TestAssignmentBCValidateCurrentBranchError(t *testing.T) {
	const beadID = "assignment-bc-current-error"
	worktrees := &assignmentBCWorktrees{
		currentErr: errors.New("injected current branch failure"),
	}
	h := newAssignmentBCHarness(t, worktrees)
	h.seedInProgressBead(t, beadID)
	h.d.mu.Lock()
	h.d.assigningBeads[beadID] = true
	h.d.mu.Unlock()
	valid := h.d.validateExistingWorktreeForReuse(
		context.Background(), beadID, "worker-current-error", "/tmp/assignment-bc-current-error",
		protocol.BranchPrefix+beadID, "main",
	)
	var reason string
	if err := h.db.QueryRow(`SELECT reason FROM recovery_quarantines WHERE bead_id=? AND status='open'`, beadID).Scan(&reason); err != nil {
		t.Fatalf("load current-branch quarantine: %v", err)
	}
	if valid || reason != "branch_worktree_mismatch" || assignmentBCBeadStatus(t, h, beadID) != "open" {
		t.Fatalf("current-branch validation = %v reason %q status %q", valid, reason, assignmentBCBeadStatus(t, h, beadID))
	}
	assignmentBCAssertLockAvailable(t, h.d)
}
