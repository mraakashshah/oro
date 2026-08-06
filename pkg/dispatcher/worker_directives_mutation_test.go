package dispatcher //nolint:testpackage // mutation owners exercise directive internals

import (
	"errors"
	"strings"
	"testing"
	"time"

	"oro/pkg/protocol"
)

const workerDirectiveTestTimeout = 500 * time.Millisecond

type workerDirectiveTestResult struct {
	detail string
	err    error
}

func invokeWorkerDirectiveBounded(
	t *testing.T,
	apply func(*Dispatcher, string) (string, error),
	d *Dispatcher,
	workerID string,
) workerDirectiveTestResult {
	t.Helper()

	done := make(chan workerDirectiveTestResult, 1)
	go func() {
		detail, err := apply(d, workerID)
		done <- workerDirectiveTestResult{detail: detail, err: err}
	}()

	// These fixtures never start dispatcher background work, so the directive
	// goroutine is the only possible mutex owner. The shared watchdog can safely
	// release a mutant-retained lock, wait for the call to return, and fail the
	// test without turning a missing unlock into a process-level timeout.
	return waitForDispatcherOperationWithin(t, d, done, workerDirectiveTestTimeout,
		"worker directive did not return within its bounded in-memory contract")
}

func assertWorkerDirectiveMutexReusable(t *testing.T, d *Dispatcher) {
	t.Helper()
	if !d.mu.TryLock() {
		t.Fatal("dispatcher mutex remained locked after worker directive returned")
	}
	d.mu.Unlock()
}

func assertWorkerDirectiveShutdown(t *testing.T, conn *mockConn) {
	t.Helper()
	msg, ok := firstWrittenMsg(conn)
	if !ok {
		t.Fatal("worker received no directive message, want SHUTDOWN")
	}
	if msg.Type != protocol.MsgShutdown {
		t.Fatalf("worker directive message type = %s, want %s", msg.Type, protocol.MsgShutdown)
	}
	conn.mu.Lock()
	writes := len(conn.written)
	conn.mu.Unlock()
	if writes != 1 {
		t.Fatalf("worker received %d directive messages, want one SHUTDOWN", writes)
	}
}

func workerDirectiveAssignmentStatus(t *testing.T, d *Dispatcher, assignmentID int64) string {
	t.Helper()
	var status string
	if err := d.db.QueryRow(`SELECT status FROM assignments WHERE id=?`, assignmentID).Scan(&status); err != nil {
		t.Fatalf("load assignment %d: %v", assignmentID, err)
	}
	return status
}

func TestApplyKillWorkerAdmissionAndEffects(t *testing.T) {
	t.Run("empty worker ID", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		result := invokeWorkerDirectiveBounded(t, (*Dispatcher).applyKillWorker, d, "")
		if result.err == nil || !strings.Contains(result.err.Error(), "worker ID required") {
			t.Fatalf("empty worker result = %q, %v, want required error", result.detail, result.err)
		}
		assertWorkerDirectiveMutexReusable(t, d)
	})

	t.Run("missing worker", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		result := invokeWorkerDirectiveBounded(t, (*Dispatcher).applyKillWorker, d, "missing-kill-worker")
		if result.err == nil || !strings.Contains(result.err.Error(), "worker not found") {
			t.Fatalf("missing worker result = %q, %v, want not-found error", result.detail, result.err)
		}
		assertWorkerDirectiveMutexReusable(t, d)
	})

	t.Run("reviewing assigned worker uses checkpoint release", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		d.targetWorkers = 2
		checkpointID, assignmentID, worker := seedCheckpointOwnedEdgeWorker(
			t, d, "mutation-kill-review", ReviewCheckpointStateReviewRunning, "active",
		)
		worker.managed = true
		drainCheckpointReleaseWakes(d)

		result := invokeWorkerDirectiveBounded(t, (*Dispatcher).applyKillWorker, d, worker.id)
		if result.err != nil {
			t.Fatalf("kill reviewing worker: %v", result.err)
		}
		assertWorkerDirectiveMutexReusable(t, d)
		assertCheckpointOwnedEdgeReleased(t, d, checkpointID, assignmentID, ReviewCheckpointStateReviewRunning)
		if d.targetWorkers != 1 {
			t.Fatalf("targetWorkers = %d, want 1", d.targetWorkers)
		}
	})

	t.Run("busy assigned worker uses ordinary lifecycle", func(t *testing.T) {
		d, beads, _, _, _, _ := newTestDispatcher(t)
		const (
			workerID = "mutation-kill-busy"
			beadID   = "mutation-kill-bead"
		)
		assignmentID := insertActiveAssignment(t, d, beadID, workerID, "/tmp/"+beadID)
		conn := newMockConn()
		d.mu.Lock()
		d.workers[workerID] = &trackedWorker{
			id:           workerID,
			conn:         conn,
			state:        protocol.WorkerBusy,
			beadID:       beadID,
			assignmentID: assignmentID,
			managed:      true,
		}
		d.attemptCounts[beadID] = 2
		d.handoffCounts[beadID] = 1
		d.rejectionCounts[beadID] = 3
		d.escalatedBeads[beadID] = true
		d.targetWorkers = 1
		d.mu.Unlock()

		result := invokeWorkerDirectiveBounded(t, (*Dispatcher).applyKillWorker, d, workerID)
		if result.err != nil {
			t.Fatalf("kill busy worker: %v", result.err)
		}
		if !strings.Contains(result.detail, workerID) {
			t.Fatalf("kill detail = %q, want worker ID", result.detail)
		}
		assertWorkerDirectiveMutexReusable(t, d)
		assertWorkerDirectiveShutdown(t, conn)

		d.mu.Lock()
		_, tracked := d.workers[workerID]
		targetWorkers := d.targetWorkers
		_, hasAttempts := d.attemptCounts[beadID]
		_, hasHandoffs := d.handoffCounts[beadID]
		_, hasRejections := d.rejectionCounts[beadID]
		_, hasEscalation := d.escalatedBeads[beadID]
		d.mu.Unlock()
		if tracked {
			t.Fatal("killed ordinary worker remains tracked")
		}
		if targetWorkers != 0 {
			t.Fatalf("targetWorkers = %d, want 0", targetWorkers)
		}
		if hasAttempts || hasHandoffs || hasRejections || hasEscalation {
			t.Fatalf("bead tracking retained: attempts=%t handoffs=%t rejections=%t escalation=%t",
				hasAttempts, hasHandoffs, hasRejections, hasEscalation)
		}
		beads.mu.Lock()
		beadStatus := beads.updated[beadID]
		beads.mu.Unlock()
		if beadStatus != "open" {
			t.Fatalf("bead status = %q, want open", beadStatus)
		}
		if status := workerDirectiveAssignmentStatus(t, d, assignmentID); status != "completed" {
			t.Fatalf("assignment status = %q, want completed", status)
		}
		if count := eventCount(t, d.db, "worker_killed"); count != 1 {
			t.Fatalf("worker_killed events = %d, want 1", count)
		}
	})

	t.Run("reviewing worker without bead uses ordinary lifecycle", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		const workerID = "mutation-kill-reviewing-empty"
		d.mu.Lock()
		d.workers[workerID] = &trackedWorker{
			id:    workerID,
			conn:  newMockConn(),
			state: protocol.WorkerReviewing,
		}
		d.mu.Unlock()

		result := invokeWorkerDirectiveBounded(t, (*Dispatcher).applyKillWorker, d, workerID)
		if result.err != nil {
			t.Fatalf("kill beadless reviewing worker: %v", result.err)
		}
		assertWorkerDirectiveMutexReusable(t, d)
		if got := trackedReleaseWorker(d, workerID); got != nil {
			t.Fatalf("beadless reviewing worker remains tracked: %p", got)
		}
	})

	t.Run("zero target does not become negative", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		const workerID = "mutation-kill-zero-target"
		d.mu.Lock()
		d.workers[workerID] = &trackedWorker{
			id:      workerID,
			conn:    newMockConn(),
			state:   protocol.WorkerIdle,
			managed: true,
		}
		d.targetWorkers = 0
		d.mu.Unlock()

		result := invokeWorkerDirectiveBounded(t, (*Dispatcher).applyKillWorker, d, workerID)
		if result.err != nil {
			t.Fatalf("kill zero-target worker: %v", result.err)
		}
		assertWorkerDirectiveMutexReusable(t, d)
		if d.targetWorkers != 0 {
			t.Fatalf("targetWorkers = %d, want 0", d.targetWorkers)
		}
	})

	t.Run("spawn-for worker remains stopped and outside target", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		const workerID = "mutation-kill-spawn-for"
		conn := newMockConn()
		worker := &trackedWorker{
			id:           workerID,
			conn:         conn,
			state:        protocol.WorkerBusy,
			managed:      true,
			spawnFor:     true,
			beadID:       "mutation-spawn-for-bead",
			assignmentID: 41,
			targetBeadID: "mutation-spawn-for-bead",
		}
		d.mu.Lock()
		d.workers[workerID] = worker
		d.targetWorkers = 2
		d.mu.Unlock()

		result := invokeWorkerDirectiveBounded(t, (*Dispatcher).applyKillWorker, d, workerID)
		if result.err != nil {
			t.Fatalf("kill spawn-for worker: %v", result.err)
		}
		assertWorkerDirectiveMutexReusable(t, d)
		assertWorkerDirectiveShutdown(t, conn)
		d.mu.Lock()
		current := d.workers[workerID]
		targetWorkers := d.targetWorkers
		d.mu.Unlock()
		if current != worker {
			t.Fatalf("spawn-for worker = %p, want retained %p", current, worker)
		}
		if worker.state != protocol.WorkerShuttingDown || worker.beadID != "" || worker.assignmentID != 0 || worker.targetBeadID != "" {
			t.Fatalf("spawn-for state = %s bead=%q assignment=%d target=%q, want stopped and cleared",
				worker.state, worker.beadID, worker.assignmentID, worker.targetBeadID)
		}
		if targetWorkers != 2 {
			t.Fatalf("targetWorkers = %d, want unchanged 2", targetWorkers)
		}
	})

	t.Run("bead reset failure is durable", func(t *testing.T) {
		d, beads, _, _, _, _ := newTestDispatcher(t)
		const (
			workerID = "mutation-kill-reset-failure"
			beadID   = "mutation-kill-reset-failure-bead"
		)
		beads.mu.Lock()
		beads.updateErrs = map[string]error{beadID: errors.New("injected reset failure")}
		beads.mu.Unlock()
		d.mu.Lock()
		d.workers[workerID] = &trackedWorker{
			id:     workerID,
			conn:   newMockConn(),
			state:  protocol.WorkerBusy,
			beadID: beadID,
		}
		d.mu.Unlock()

		result := invokeWorkerDirectiveBounded(t, (*Dispatcher).applyKillWorker, d, workerID)
		if result.err != nil {
			t.Fatalf("kill worker after bead reset failure: %v", result.err)
		}
		assertWorkerDirectiveMutexReusable(t, d)
		if count := eventCount(t, d.db, "kill_worker_bead_reset_failed"); count != 1 {
			t.Fatalf("kill_worker_bead_reset_failed events = %d, want 1", count)
		}
	})
}

func TestApplyRestartWorkerAdmissionAndEffects(t *testing.T) {
	t.Run("empty worker ID", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		result := invokeWorkerDirectiveBounded(t, (*Dispatcher).applyRestartWorker, d, "")
		if result.err == nil || !strings.Contains(result.err.Error(), "worker ID required") {
			t.Fatalf("empty worker result = %q, %v, want required error", result.detail, result.err)
		}
		assertWorkerDirectiveMutexReusable(t, d)
	})

	t.Run("missing worker", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		result := invokeWorkerDirectiveBounded(t, (*Dispatcher).applyRestartWorker, d, "missing-restart-worker")
		if result.err == nil || !strings.Contains(result.err.Error(), "worker not found") {
			t.Fatalf("missing worker result = %q, %v, want not-found error", result.detail, result.err)
		}
		assertWorkerDirectiveMutexReusable(t, d)
	})

	t.Run("reviewing assigned worker uses checkpoint release", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		pm := &mockProcessManager{}
		d.procMgr = pm
		checkpointID, assignmentID, worker := seedCheckpointOwnedEdgeWorker(
			t, d, "mutation-restart-review", ReviewCheckpointStateReviewRunning, "active",
		)
		worker.managed = true
		drainCheckpointReleaseWakes(d)

		result := invokeWorkerDirectiveBounded(t, (*Dispatcher).applyRestartWorker, d, worker.id)
		if result.err != nil {
			t.Fatalf("restart reviewing worker: %v", result.err)
		}
		assertWorkerDirectiveMutexReusable(t, d)
		assertCheckpointOwnedEdgeReleased(t, d, checkpointID, assignmentID, ReviewCheckpointStateReviewRunning)
		if got := pm.Events(); len(got) != 2 || got[0] != "kill:"+worker.id || got[1] != "spawn:"+worker.id {
			t.Fatalf("review restart process events = %v, want kill then spawn", got)
		}
	})

	t.Run("busy assigned worker uses ordinary lifecycle", func(t *testing.T) {
		d, beads, _, _, _, _ := newTestDispatcher(t)
		pm := &mockProcessManager{}
		d.procMgr = pm
		const (
			workerID = "mutation-restart-busy"
			beadID   = "mutation-restart-bead"
		)
		assignmentID := insertActiveAssignment(t, d, beadID, workerID, "/tmp/"+beadID)
		d.mu.Lock()
		d.workers[workerID] = &trackedWorker{
			id:           workerID,
			conn:         newMockConn(),
			state:        protocol.WorkerBusy,
			beadID:       beadID,
			assignmentID: assignmentID,
			managed:      true,
		}
		d.attemptCounts[beadID] = 2
		d.targetWorkers = 3
		d.mu.Unlock()

		result := invokeWorkerDirectiveBounded(t, (*Dispatcher).applyRestartWorker, d, workerID)
		if result.err != nil {
			t.Fatalf("restart busy worker: %v", result.err)
		}
		if !strings.Contains(result.detail, workerID) {
			t.Fatalf("restart detail = %q, want worker ID", result.detail)
		}
		assertWorkerDirectiveMutexReusable(t, d)
		if got := pm.Events(); len(got) != 2 || got[0] != "kill:"+workerID || got[1] != "spawn:"+workerID {
			t.Fatalf("restart process events = %v, want kill then spawn", got)
		}
		d.mu.Lock()
		_, tracked := d.workers[workerID]
		pending := d.pendingManagedIDs[workerID]
		_, hasSince := d.pendingManagedSince[workerID]
		_, hasAttempts := d.attemptCounts[beadID]
		targetWorkers := d.targetWorkers
		d.mu.Unlock()
		if tracked || !pending || !hasSince || hasAttempts {
			t.Fatalf("restart state: tracked=%t pending=%t since=%t attempts=%t", tracked, pending, hasSince, hasAttempts)
		}
		if targetWorkers != 3 {
			t.Fatalf("targetWorkers = %d, want unchanged 3", targetWorkers)
		}
		if status := workerDirectiveAssignmentStatus(t, d, assignmentID); status != "completed" {
			t.Fatalf("assignment status = %q, want completed", status)
		}
		beads.mu.Lock()
		beadStatus := beads.updated[beadID]
		beads.mu.Unlock()
		if beadStatus != "open" {
			t.Fatalf("bead status = %q, want open", beadStatus)
		}
		for _, eventType := range []string{"restart_worker_assignment_recovered", "worker_restarted"} {
			if count := eventCount(t, d.db, eventType); count != 1 {
				t.Fatalf("%s events = %d, want 1", eventType, count)
			}
		}
	})

	t.Run("reviewing worker without bead uses ordinary lifecycle", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		const workerID = "mutation-restart-reviewing-empty"
		d.mu.Lock()
		d.workers[workerID] = &trackedWorker{
			id:    workerID,
			conn:  newMockConn(),
			state: protocol.WorkerReviewing,
		}
		d.mu.Unlock()

		result := invokeWorkerDirectiveBounded(t, (*Dispatcher).applyRestartWorker, d, workerID)
		if result.err != nil {
			t.Fatalf("restart beadless reviewing worker: %v", result.err)
		}
		assertWorkerDirectiveMutexReusable(t, d)
		if got := trackedReleaseWorker(d, workerID); got != nil {
			t.Fatalf("beadless reviewing worker remains tracked: %p", got)
		}
	})
}

func TestApplyRestartWorkerFailureAndRetryEffects(t *testing.T) {
	t.Run("kill failure completes assignment but does not spawn", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		pm := &mockProcessManager{killErr: errors.New("injected kill failure")}
		d.procMgr = pm
		const (
			workerID = "mutation-restart-kill-failure"
			beadID   = "mutation-restart-kill-failure-bead"
		)
		assignmentID := insertActiveAssignment(t, d, beadID, workerID, "/tmp/"+beadID)
		d.mu.Lock()
		d.workers[workerID] = &trackedWorker{
			id:           workerID,
			conn:         newMockConn(),
			state:        protocol.WorkerBusy,
			beadID:       beadID,
			assignmentID: assignmentID,
			managed:      true,
		}
		d.mu.Unlock()

		result := invokeWorkerDirectiveBounded(t, (*Dispatcher).applyRestartWorker, d, workerID)
		if result.err == nil || !strings.Contains(result.err.Error(), "kill managed worker for restart") {
			t.Fatalf("kill failure result = %q, %v, want kill error", result.detail, result.err)
		}
		assertWorkerDirectiveMutexReusable(t, d)
		if got := pm.SpawnedIDs(); len(got) != 0 {
			t.Fatalf("kill failure spawned replacement: %v", got)
		}
		d.mu.Lock()
		pending := d.pendingManagedIDs[workerID]
		d.mu.Unlock()
		if pending {
			t.Fatal("kill failure retained pending-managed state")
		}
		if status := workerDirectiveAssignmentStatus(t, d, assignmentID); status != "completed" {
			t.Fatalf("assignment status = %q, want completed", status)
		}
		if count := eventCount(t, d.db, "restart_worker_kill_failed"); count != 1 {
			t.Fatalf("restart_worker_kill_failed events = %d, want 1", count)
		}
		if count := eventCount(t, d.db, "worker_restarted"); count != 0 {
			t.Fatalf("worker_restarted events = %d, want 0", count)
		}
	})

	t.Run("assignment completion failure forgets pending and does not spawn", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		pm := &mockProcessManager{}
		d.procMgr = pm
		const (
			workerID = "mutation-restart-completion-failure"
			beadID   = "mutation-restart-completion-failure-bead"
		)
		assignmentID := insertActiveAssignment(t, d, beadID, workerID, "/tmp/"+beadID)
		d.mu.Lock()
		d.workers[workerID] = &trackedWorker{
			id:           workerID,
			conn:         newMockConn(),
			state:        protocol.WorkerBusy,
			beadID:       beadID,
			assignmentID: assignmentID + 1,
			managed:      true,
		}
		d.mu.Unlock()

		result := invokeWorkerDirectiveBounded(t, (*Dispatcher).applyRestartWorker, d, workerID)
		if result.err == nil || !strings.Contains(result.err.Error(), "complete restart assignment") {
			t.Fatalf("completion failure result = %q, %v, want completion error", result.detail, result.err)
		}
		assertWorkerDirectiveMutexReusable(t, d)
		if got := pm.SpawnedIDs(); len(got) != 0 {
			t.Fatalf("completion failure spawned replacement: %v", got)
		}
		d.mu.Lock()
		pending := d.pendingManagedIDs[workerID]
		d.mu.Unlock()
		if pending {
			t.Fatal("completion failure retained pending-managed state")
		}
		if status := workerDirectiveAssignmentStatus(t, d, assignmentID); status != "active" {
			t.Fatalf("assignment status = %q, want active", status)
		}
		for _, eventType := range []string{"restart_worker_assignment_cleanup_failed", "restart_worker_assignment_completion_failed"} {
			if count := eventCount(t, d.db, eventType); count != 1 {
				t.Fatalf("%s events = %d, want 1", eventType, count)
			}
		}
	})

	t.Run("spawn failure is returned and logged", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		pm := &mockProcessManager{spawnErr: errors.New("injected spawn failure")}
		d.procMgr = pm
		const (
			workerID = "mutation-restart-spawn-failure"
			beadID   = "mutation-restart-spawn-failure-bead"
		)
		assignmentID := insertActiveAssignment(t, d, beadID, workerID, "/tmp/"+beadID)
		d.mu.Lock()
		d.workers[workerID] = &trackedWorker{
			id:           workerID,
			conn:         newMockConn(),
			state:        protocol.WorkerBusy,
			beadID:       beadID,
			assignmentID: assignmentID,
			managed:      true,
		}
		d.mu.Unlock()

		result := invokeWorkerDirectiveBounded(t, (*Dispatcher).applyRestartWorker, d, workerID)
		if result.err == nil || !strings.Contains(result.err.Error(), "spawn new worker") {
			t.Fatalf("spawn failure result = %q, %v, want spawn error", result.detail, result.err)
		}
		assertWorkerDirectiveMutexReusable(t, d)
		d.mu.Lock()
		pending := d.pendingManagedIDs[workerID]
		d.mu.Unlock()
		if !pending {
			t.Fatal("spawn failure lost pending-managed retry state")
		}
		if count := eventCount(t, d.db, "worker_spawn_failed"); count != 1 {
			t.Fatalf("worker_spawn_failed events = %d, want 1", count)
		}
		if count := eventCount(t, d.db, "worker_restarted"); count != 0 {
			t.Fatalf("worker_restarted events = %d, want 0", count)
		}
	})

	t.Run("pending QG retry takes preservation branch", func(t *testing.T) {
		d, _, _, _, _, _ := newTestDispatcher(t)
		pm := &mockProcessManager{}
		d.procMgr = pm
		const (
			workerID = "mutation-restart-qg-retry"
			beadID   = "mutation-restart-qg-retry-bead"
		)
		assignmentID := insertActiveAssignment(t, d, beadID, workerID, "/tmp/"+beadID)
		d.mu.Lock()
		d.workers[workerID] = &trackedWorker{
			id:           workerID,
			conn:         newMockConn(),
			state:        protocol.WorkerBusy,
			beadID:       beadID,
			assignmentID: assignmentID,
			worktree:     "/tmp/" + beadID,
			managed:      true,
		}
		d.pendingQGRetries[workerID] = QGRetryContext{Attempt: 1}
		d.mu.Unlock()

		result := invokeWorkerDirectiveBounded(t, (*Dispatcher).applyRestartWorker, d, workerID)
		if result.err == nil || !strings.Contains(result.err.Error(), "restore qg retry feedback") {
			t.Fatalf("QG retry result = %q, %v, want preservation error", result.detail, result.err)
		}
		assertWorkerDirectiveMutexReusable(t, d)
		if got := pm.Events(); len(got) != 0 {
			t.Fatalf("failed QG preservation touched process manager: %v", got)
		}
		d.mu.Lock()
		pending := d.pendingManagedIDs[workerID]
		d.mu.Unlock()
		if pending {
			t.Fatal("failed QG preservation retained pending-managed state")
		}
		if status := workerDirectiveAssignmentStatus(t, d, assignmentID); status != "active" {
			t.Fatalf("assignment status = %q, want active", status)
		}
	})
}
