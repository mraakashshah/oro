package dispatcher //nolint:testpackage // white-box test needs internal access

import (
	"context"
	"net"
	"testing"
	"time"

	"oro/pkg/protocol"
)

func TestAssignBeadDoesNotSendWhenCreateAssignmentFails(t *testing.T) {
	d, beadSrc, wtMgr, _, _, _ := newTestDispatcher(t)
	wtMgr.branchExistsFn = func(context.Context, string) (bool, error) { return false, nil }

	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	d.registerWorker("w-db-fail", server)

	d.mu.Lock()
	w := d.workers["w-db-fail"]
	d.mu.Unlock()

	beadID := "bead-db-fail"
	beadSrc.mu.Lock()
	beadSrc.shown[beadID] = &protocol.BeadDetail{
		ID:                 beadID,
		Title:              "DB failure bead",
		AcceptanceCriteria: "Test: auto | Assert: PASS",
		Status:             "open",
	}
	beadSrc.mu.Unlock()

	if _, err := d.db.Exec(`
CREATE TRIGGER fail_assignment_insert
BEFORE INSERT ON assignments
BEGIN
    SELECT RAISE(FAIL, 'injected assignment persistence failure');
END`); err != nil {
		t.Fatalf("install assignment persistence failure: %v", err)
	}

	if err := d.assignBead(context.Background(), w, protocol.Bead{ID: beadID, Title: "DB failure bead", Priority: 1, Type: "task"}); err != nil {
		t.Fatalf("assignBead returned error: %v", err)
	}

	if err := client.SetReadDeadline(time.Now().Add(150 * time.Millisecond)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	buf := make([]byte, 1)
	if _, err := client.Read(buf); err == nil {
		t.Fatal("expected no ASSIGN to be sent when assignment persistence fails")
	}

	beadSrc.mu.Lock()
	lastStatus := beadSrc.updated[beadID]
	beadSrc.mu.Unlock()
	if lastStatus != "open" {
		t.Fatalf("bead status after persistence failure: got %q, want open", lastStatus)
	}

	wtMgr.mu.Lock()
	removed := append([]string(nil), wtMgr.removed...)
	wtMgr.mu.Unlock()
	if len(removed) != 1 || removed[0] != "/tmp/worktree-"+beadID {
		t.Fatalf("removed worktrees: got %v, want [%q]", removed, "/tmp/worktree-"+beadID)
	}

	d.mu.Lock()
	_, stillAssigning := d.assigningBeads[beadID]
	_, stillTracked := d.worktreeByBead[beadID]
	d.mu.Unlock()
	if stillAssigning {
		t.Fatal("assigningBeads entry leaked after persistence failure")
	}
	if stillTracked {
		t.Fatal("worktreeByBead entry leaked after persistence failure")
	}
}

func TestAssignBeadRollsBackStatusWithoutDeletingReusedWorktreeOnPersistenceFailure(t *testing.T) {
	d, beadSrc, wtMgr, _, _, _ := newTestDispatcher(t)

	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	d.registerWorker("w-db-reuse", server)

	d.mu.Lock()
	w := d.workers["w-db-reuse"]
	d.worktreeByBead["bead-db-reuse"] = "/tmp/reused-worktree"
	d.mu.Unlock()

	wtMgr.existsFn = func(_ context.Context, path string) bool {
		return path == "/tmp/reused-worktree"
	}

	beadSrc.mu.Lock()
	beadSrc.shown["bead-db-reuse"] = &protocol.BeadDetail{
		ID:                 "bead-db-reuse",
		Title:              "DB reuse bead",
		AcceptanceCriteria: "Test: auto | Assert: PASS",
		Status:             "open",
	}
	beadSrc.mu.Unlock()

	if _, err := d.db.Exec(`
CREATE TRIGGER fail_assignment_insert
BEFORE INSERT ON assignments
BEGIN
    SELECT RAISE(FAIL, 'injected assignment persistence failure');
END`); err != nil {
		t.Fatalf("install assignment persistence failure: %v", err)
	}

	if err := d.assignBead(context.Background(), w, protocol.Bead{ID: "bead-db-reuse", Title: "DB reuse bead", Priority: 1, Type: "task"}); err != nil {
		t.Fatalf("assignBead returned error: %v", err)
	}

	beadSrc.mu.Lock()
	lastStatus := beadSrc.updated["bead-db-reuse"]
	beadSrc.mu.Unlock()
	if lastStatus != "open" {
		t.Fatalf("bead status after persistence failure: got %q, want open", lastStatus)
	}

	wtMgr.mu.Lock()
	removed := append([]string(nil), wtMgr.removed...)
	wtMgr.mu.Unlock()
	if len(removed) != 0 {
		t.Fatalf("expected reused worktree to be preserved, removed=%v", removed)
	}
}
