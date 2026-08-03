package dispatcher //nolint:testpackage // white-box tests for lazy epic branch creation

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"oro/pkg/protocol"
)

// TestEpicBranchLazyCreation covers the six acceptance criteria for epic branch
// lazy creation wired into assignBead.
func TestEpicBranchLazyCreation(t *testing.T) {
	// setupEpic adds an epic bead to the store so resolveEpicBranch can find it.
	setupEpic := func(beadSrc *fakeBeadStore, epicID string) {
		beadSrc.mu.Lock()
		defer beadSrc.mu.Unlock()
		beadSrc.shown[epicID] = &protocol.BeadDetail{
			ID:     epicID,
			Title:  "Epic " + epicID,
			Type:   "epic",
			Status: "open",
		}
	}

	// --- AC1: branch missing, CreateBranch succeeds → child assigned, epic_branch_created logged ---
	t.Run("creates_branch_and_assigns_child", func(t *testing.T) {
		d, beadSrc, wtMgr, _, _, _ := newTestDispatcher(t)
		setupEpic(beadSrc, "epic-1")

		var createBranchName, createBranchFrom string
		var createBranchCallCount int64
		wtMgr.mu.Lock()
		wtMgr.createBranchFn = func(_ context.Context, name, from string) error {
			atomic.AddInt64(&createBranchCallCount, 1)
			createBranchName = name
			createBranchFrom = from
			return nil
		}
		// First BranchExists call → false (branch missing, triggers lazy creation).
		// Subsequent calls (if any) → true.
		var beCallCount int64
		wtMgr.branchExistsFn = func(_ context.Context, branch string) (bool, error) {
			if strings.HasPrefix(branch, "epic/") {
				n := atomic.AddInt64(&beCallCount, 1)
				if n == 1 {
					return false, nil
				}
				return true, nil
			}
			return true, nil
		}
		// Capture the base branch used for worktree creation.
		var capturedBase string
		wtMgr.createFn = func(_ context.Context, beadID, baseBranch string) (string, string, error) {
			if beadID == "child-1" {
				capturedBase = baseBranch
			}
			return "/tmp/wt-" + beadID, "agent/" + beadID, nil
		}
		wtMgr.mu.Unlock()

		startDispatcher(t, d)
		conn, _ := connectWorker(t, d.cfg.SocketPath)
		sendMsg(t, conn, protocol.Message{
			Type:      protocol.MsgHeartbeat,
			Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w1", ContextPct: 5},
		})
		waitForWorkers(t, d, 1, time.Second)
		sendDirective(t, d.cfg.SocketPath, "start")
		waitForState(t, d, StateRunning, time.Second)

		beadSrc.SetBeads([]protocol.Bead{
			{ID: "child-1", Title: "Child of epic-1", Priority: 1, Epic: "epic-1"},
		})

		// Must receive ASSIGN: branch lazily created, child proceeds to assignment.
		msg, ok := readMsg(t, conn, 2*time.Second)
		if !ok || msg.Type != protocol.MsgAssign {
			t.Fatalf("expected ASSIGN after lazy branch creation, got ok=%v type=%v", ok, msg.Type)
		}

		// CreateBranch must have been called exactly once with (epic/epic-1, main).
		if n := atomic.LoadInt64(&createBranchCallCount); n != 1 {
			t.Errorf("CreateBranch called %d times; want 1", n)
		}
		if createBranchName != "epic/epic-1" {
			t.Errorf("CreateBranch name = %q; want %q", createBranchName, "epic/epic-1")
		}
		if createBranchFrom != "main" {
			t.Errorf("CreateBranch from = %q; want %q", createBranchFrom, "main")
		}

		// Worktree must have been created from the epic branch.
		if capturedBase != "epic/epic-1" {
			t.Errorf("worktree baseBranch = %q; want %q", capturedBase, "epic/epic-1")
		}

		// epic_branch_created must appear in log events.
		found := false
		for _, e := range getLogEvents(t, d) {
			if strings.Contains(e, "epic_branch_created:") {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("epic_branch_created not found in log events: %v", getLogEvents(t, d))
		}
	})

	// --- AC2: race — CreateBranch fails but re-check shows branch exists → proceeds normally ---
	t.Run("race_resolved", func(t *testing.T) {
		d, beadSrc, wtMgr, _, _, _ := newTestDispatcher(t)
		setupEpic(beadSrc, "epic-2")

		// BranchExists: call 1 → false (triggers lazy creation), call 2 (re-check) → true (race resolved).
		var beCallCount int64
		wtMgr.mu.Lock()
		wtMgr.branchExistsFn = func(_ context.Context, branch string) (bool, error) {
			if strings.HasPrefix(branch, "epic/") {
				n := atomic.AddInt64(&beCallCount, 1)
				if n == 1 {
					return false, nil // initial check: branch not yet there
				}
				return true, nil // re-check after failed CreateBranch: another goroutine created it
			}
			return true, nil
		}
		// CreateBranch always fails — simulates the race loser.
		wtMgr.createBranchFn = func(_ context.Context, _, _ string) error {
			return errors.New("fatal: A branch named 'epic/epic-2' already exists")
		}
		wtMgr.mu.Unlock()

		startDispatcher(t, d)
		conn, _ := connectWorker(t, d.cfg.SocketPath)
		sendMsg(t, conn, protocol.Message{
			Type:      protocol.MsgHeartbeat,
			Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w1", ContextPct: 5},
		})
		waitForWorkers(t, d, 1, time.Second)
		sendDirective(t, d.cfg.SocketPath, "start")
		waitForState(t, d, StateRunning, time.Second)

		beadSrc.SetBeads([]protocol.Bead{
			{ID: "child-2", Title: "Child of epic-2", Priority: 1, Epic: "epic-2"},
		})

		// Must receive ASSIGN: race resolved, child proceeds normally.
		msg, ok := readMsg(t, conn, 2*time.Second)
		if !ok || msg.Type != protocol.MsgAssign {
			t.Fatalf("expected ASSIGN after race resolution, got ok=%v type=%v", ok, msg.Type)
		}

		// epic_branch_race_resolved must appear in log events.
		found := false
		for _, e := range getLogEvents(t, d) {
			if strings.Contains(e, "epic_branch_race_resolved:") {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("epic_branch_race_resolved not found in log events: %v", getLogEvents(t, d))
		}
	})

	// --- AC3: CreateBranch fails + re-check false → revert bead, clear assigningBeads,
	// recordAssignmentFailure, escalate as EscStuckWorker. ---
	t.Run("failure_escalates_and_reverts", func(t *testing.T) {
		d, beadSrc, wtMgr, esc, _, _ := newTestDispatcher(t)
		setupEpic(beadSrc, "epic-3")

		wtMgr.mu.Lock()
		// BranchExists always returns false — genuine failure, not a race.
		wtMgr.branchExistsFn = func(_ context.Context, _ string) (bool, error) {
			return false, nil
		}
		// CreateBranch fails with a non-race error.
		wtMgr.createBranchFn = func(_ context.Context, _, _ string) error {
			return errors.New("permission denied: cannot create epic branch")
		}
		wtMgr.mu.Unlock()

		startDispatcher(t, d)
		conn, _ := connectWorker(t, d.cfg.SocketPath)
		sendMsg(t, conn, protocol.Message{
			Type:      protocol.MsgHeartbeat,
			Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w1", ContextPct: 5},
		})
		waitForWorkers(t, d, 1, time.Second)
		sendDirective(t, d.cfg.SocketPath, "start")
		waitForState(t, d, StateRunning, time.Second)

		beadSrc.SetBeads([]protocol.Bead{
			{ID: "child-3", Title: "Child of epic-3", Priority: 1, Epic: "epic-3"},
		})

		// Must NOT receive ASSIGN.
		msg, ok := readMsg(t, conn, 500*time.Millisecond)
		if ok && msg.Type == protocol.MsgAssign {
			t.Fatal("should not receive ASSIGN when CreateBranch fails genuinely")
		}

		// Bead must be reverted to "open".
		waitFor(t, func() bool {
			beadSrc.mu.Lock()
			defer beadSrc.mu.Unlock()
			return beadSrc.updated["child-3"] == "open"
		}, 2*time.Second)

		// assigningBeads must be cleared for this bead.
		d.mu.Lock()
		stillAssigning := d.assigningBeads["child-3"]
		d.mu.Unlock()
		if stillAssigning {
			t.Error("assigningBeads should be cleared after failure")
		}

		// recordAssignmentFailure must have been called (worktreeFailures entry).
		d.mu.Lock()
		_, recorded := d.worktreeFailures["child-3"]
		d.mu.Unlock()
		if !recorded {
			t.Error("recordAssignmentFailure not called — worktreeFailures entry missing")
		}

		// Escalation must be sent as EscStuckWorker.
		waitFor(t, func() bool {
			for _, m := range esc.Messages() {
				if strings.Contains(m, string(protocol.EscStuckWorker)) {
					return true
				}
			}
			return false
		}, 2*time.Second)
	})

	// --- AC4: MetaBranch guard — bead with MetaBranch but resolvedEpicID='' skips lazy creation ---
	t.Run("metabranch_guard_skips_lazy_creation", func(t *testing.T) {
		d, beadSrc, wtMgr, _, _, _ := newTestDispatcher(t)

		var createBranchCalled bool
		wtMgr.mu.Lock()
		wtMgr.createBranchFn = func(_ context.Context, _, _ string) error {
			createBranchCalled = true
			return nil
		}
		// "develop" branch doesn't exist — but resolvedEpicID='' prevents lazy creation.
		wtMgr.branchExistsFn = func(_ context.Context, branch string) (bool, error) {
			if branch == "develop" {
				return false, nil
			}
			return true, nil
		}
		wtMgr.mu.Unlock()

		startDispatcher(t, d)
		conn, _ := connectWorker(t, d.cfg.SocketPath)
		sendMsg(t, conn, protocol.Message{
			Type:      protocol.MsgHeartbeat,
			Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w1", ContextPct: 5},
		})
		waitForWorkers(t, d, 1, time.Second)
		sendDirective(t, d.cfg.SocketPath, "start")
		waitForState(t, d, StateRunning, time.Second)

		// Bead with MetaBranch="develop" and no Epic parent → resolvedEpicID="".
		beadSrc.SetBeads([]protocol.Bead{
			{
				ID:       "meta-child",
				Title:    "Meta child with custom branch",
				Priority: 1,
				// No Epic: resolvedEpicID will be ""
				Metadata: map[string]any{MetaBranch: "develop"},
			},
		})

		// Still receives ASSIGN: guard skips lazy creation but worktree.Create succeeds.
		msg, ok := readMsg(t, conn, 2*time.Second)
		if !ok || msg.Type != protocol.MsgAssign {
			t.Fatalf("expected ASSIGN for MetaBranch bead, got ok=%v type=%v", ok, msg.Type)
		}

		// CreateBranch must NOT have been called.
		if createBranchCalled {
			t.Error("CreateBranch should not be called when resolvedEpicID is empty (MetaBranch guard)")
		}
	})

	// --- AC5: BranchExists error → routes to handleEpicBranchMissing (preserved path) ---
	t.Run("branch_exists_error_routes_to_handler", func(t *testing.T) {
		d, beadSrc, wtMgr, esc, _, _ := newTestDispatcher(t)

		// Epic is "open" → handleEpicBranchMissing logs epic_branch_pending, no escalation.
		beadSrc.mu.Lock()
		beadSrc.shown["epic-5"] = &protocol.BeadDetail{
			ID:     "epic-5",
			Title:  "Epic 5",
			Type:   "epic",
			Status: "open",
		}
		beadSrc.mu.Unlock()

		wtMgr.mu.Lock()
		wtMgr.branchExistsFn = func(_ context.Context, _ string) (bool, error) {
			return false, errors.New("git: repository is corrupt")
		}
		wtMgr.mu.Unlock()

		startDispatcher(t, d)
		conn, _ := connectWorker(t, d.cfg.SocketPath)
		sendMsg(t, conn, protocol.Message{
			Type:      protocol.MsgHeartbeat,
			Heartbeat: &protocol.HeartbeatPayload{WorkerID: "w1", ContextPct: 5},
		})
		waitForWorkers(t, d, 1, time.Second)
		sendDirective(t, d.cfg.SocketPath, "start")
		waitForState(t, d, StateRunning, time.Second)

		beadSrc.SetBeads([]protocol.Bead{
			{ID: "child-5", Title: "Child of epic-5", Priority: 1, Epic: "epic-5"},
		})

		// Must NOT receive ASSIGN: BranchExists error → handleEpicBranchMissing returns early.
		msg, ok := readMsg(t, conn, 500*time.Millisecond)
		if ok && msg.Type == protocol.MsgAssign {
			t.Fatal("should not receive ASSIGN when BranchExists returns error")
		}

		// epic_branch_pending must be logged (epic is "open").
		waitFor(t, func() bool {
			for _, e := range getLogEvents(t, d) {
				if strings.Contains(e, "epic_branch_pending:") {
					return true
				}
			}
			return false
		}, 2*time.Second)
		if got := eventCount(t, d.db, "epic_branch_prepare_failed"); got != 0 {
			t.Errorf("epic_branch_prepare_failed events = %d, want preserved pending path", got)
		}
		d.mu.Lock()
		_, cooldown := d.worktreeFailures["child-5"]
		d.mu.Unlock()
		if cooldown {
			t.Error("BranchExists pending path recorded an assignment cooldown")
		}

		// No STUCK_WORKER escalation for an open epic.
		for _, m := range esc.Messages() {
			if strings.Contains(m, string(protocol.EscStuckWorker)) {
				t.Errorf("unexpected STUCK_WORKER escalation for open epic: %s", m)
			}
		}
	})
}
