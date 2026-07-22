package dispatcher //nolint:testpackage // needs internal dispatcher retry state

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	"oro/pkg/protocol"
)

func TestHandleReviewRejectionStopsWhenBeadGainsBlocker(t *testing.T) {
	ctx := context.Background()
	d, beadSrc, _, _, _, _ := newTestDispatcher(t)
	const (
		workerID = "worker-review-blocked"
		parentID = "bead-review-parent"
		childID  = "bead-review-child"
	)

	server, client := net.Pipe()
	defer func() { _ = server.Close() }()
	defer func() { _ = client.Close() }()

	result, err := d.db.ExecContext(ctx,
		`INSERT INTO assignments (bead_id, worker_id, worktree, status) VALUES (?, ?, ?, 'active')`,
		parentID, workerID, t.TempDir())
	if err != nil {
		t.Fatalf("seed active assignment: %v", err)
	}
	assignmentID, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("read assignment ID: %v", err)
	}

	parent := protocol.Bead{
		ID:     parentID,
		Status: "open",
		Dependencies: []protocol.Dependency{{
			IssueID: parentID, DependsOnID: childID, Type: "blocks",
		}},
	}
	child := protocol.Bead{ID: childID, Status: "open"}
	beadSrc.SetBeads([]protocol.Bead{parent, child})
	beadSrc.mu.Lock()
	beadSrc.shown[parentID] = &parent
	beadSrc.shown[childID] = &child
	beadSrc.mu.Unlock()

	d.mu.Lock()
	d.workers[workerID] = &trackedWorker{
		id:           workerID,
		conn:         server,
		encoder:      json.NewEncoder(server),
		state:        protocol.WorkerReviewing,
		beadID:       parentID,
		assignmentID: assignmentID,
	}
	d.mu.Unlock()

	retry := make(chan protocol.Message, 1)
	go func() {
		var message protocol.Message
		if err := json.NewDecoder(client).Decode(&message); err == nil {
			retry <- message
		}
	}()
	handled := make(chan struct{})
	go func() {
		defer close(handled)
		d.handleReviewRejection(ctx, workerID, parentID, "missing checkpoint foundation")
	}()

	select {
	case <-handled:
	case <-time.After(time.Second):
		t.Fatal("handleReviewRejection did not finish")
	}
	select {
	case message := <-retry:
		t.Fatalf("unexpected retry message: %#v", message)
	case <-time.After(100 * time.Millisecond):
	}

	var assignmentStatus string
	if err := d.db.QueryRowContext(ctx, `SELECT status FROM assignments WHERE id=?`, assignmentID).Scan(&assignmentStatus); err != nil {
		t.Fatalf("read assignment status: %v", err)
	}
	if assignmentStatus != "completed" {
		t.Fatalf("assignment status = %q, want completed", assignmentStatus)
	}

	beadSrc.mu.Lock()
	parentStatus := beadSrc.updated[parentID]
	beadSrc.mu.Unlock()
	if parentStatus != "open" {
		t.Fatalf("parent status = %q, want open", parentStatus)
	}
	var findings int
	if err := d.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM rejection_history WHERE bead_id=? AND feedback=?`,
		parentID, "missing checkpoint foundation").Scan(&findings); err != nil {
		t.Fatalf("count persisted reviewer findings: %v", err)
	}
	if findings != 1 {
		t.Fatalf("persisted reviewer findings = %d, want 1", findings)
	}

	d.mu.Lock()
	worker := d.workers[workerID]
	d.mu.Unlock()
	if worker == nil || worker.state != protocol.WorkerIdle || worker.beadID != "" || worker.assignmentID != 0 {
		t.Fatalf("worker was not released: %#v", worker)
	}
	if got := eventCount(t, d.db, "review_retry_blocked_by_dependency"); got != 1 {
		t.Fatalf("review_retry_blocked_by_dependency events = %d, want 1", got)
	}

	candidates := d.filterAssignable(ctx, []protocol.Bead{parent, child})
	if len(candidates) != 1 || candidates[0].ID != childID {
		t.Fatalf("schedulable beads = %#v, want only %q", candidates, childID)
	}
}
