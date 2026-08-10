package dispatcher //nolint:testpackage // mutation owner exercises directive internals

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"oro/pkg/protocol"
)

const restartWorkerIfStillOnBeadTestTimeout = 500 * time.Millisecond

func invokeRestartWorkerIfStillOnBeadBounded(
	t *testing.T,
	d *Dispatcher,
	workerID string,
	beadID string,
	reason string,
) bool {
	t.Helper()

	done := make(chan bool, 1)
	go func() {
		done <- d.restartWorkerIfStillOnBead(context.Background(), workerID, beadID, reason)
	}()

	select {
	case restarted := <-done:
		return restarted
	case <-time.After(restartWorkerIfStillOnBeadTestTimeout):
		t.Fatalf("restartWorkerIfStillOnBead did not return within %s", restartWorkerIfStillOnBeadTestTimeout)
		return false
	}
}

func assertRestartWorkerIfStillOnBeadMutexReusable(t *testing.T, d *Dispatcher) {
	t.Helper()
	if !d.mu.TryLock() {
		t.Fatal("dispatcher mutex remained locked after restartWorkerIfStillOnBead returned")
	}
	d.mu.Unlock()
}

func restartWorkerIfStillOnBeadEventPayload(
	t *testing.T,
	d *Dispatcher,
	eventType string,
	beadID string,
	workerID string,
) string {
	t.Helper()
	var payload string
	if err := d.db.QueryRow(`
		SELECT payload FROM events
		WHERE type=? AND bead_id=? AND worker_id=?
		ORDER BY id DESC LIMIT 1
	`, eventType, beadID, workerID).Scan(&payload); err != nil {
		t.Fatalf("load %s event: %v", eventType, err)
	}
	return payload
}

func TestRestartWorkerIfStillOnBeadAdmissionAndEffects(t *testing.T) {
	for _, tc := range []struct {
		name          string
		workerPresent bool
		reviewToken   uint64
		workerBeadID  string
		requestedBead string
		state         protocol.WorkerState
	}{
		{
			name:          "missing worker",
			requestedBead: "mutation-focus-missing-bead",
		},
		{
			name:          "review release token",
			workerPresent: true,
			reviewToken:   9,
			workerBeadID:  "mutation-focus-token-bead",
			requestedBead: "mutation-focus-token-bead",
			state:         protocol.WorkerBusy,
		},
		{
			name:          "wrong bead",
			workerPresent: true,
			workerBeadID:  "mutation-focus-current-bead",
			requestedBead: "mutation-focus-stale-bead",
			state:         protocol.WorkerBusy,
		},
		{
			name:          "nonpreemptable state",
			workerPresent: true,
			workerBeadID:  "mutation-focus-idle-bead",
			requestedBead: "mutation-focus-idle-bead",
			state:         protocol.WorkerIdle,
		},
	} {
		t.Run("rejects "+tc.name, func(t *testing.T) {
			d, _, _, _, _, _ := newTestDispatcher(t)
			workerID := "mutation-focus-worker-" + strings.ReplaceAll(tc.name, " ", "-")
			var (
				worker       *trackedWorker
				conn         *mockConn
				assignmentID int64
			)
			if tc.workerPresent {
				conn = newMockConn()
				assignmentID = insertActiveAssignment(t, d, tc.workerBeadID, workerID, "/tmp/"+tc.workerBeadID)
				worker = &trackedWorker{
					id:                 workerID,
					conn:               conn,
					state:              tc.state,
					beadID:             tc.workerBeadID,
					assignmentID:       assignmentID,
					managed:            true,
					reviewReleaseToken: tc.reviewToken,
				}
				d.mu.Lock()
				d.workers[workerID] = worker
				d.mu.Unlock()
			}

			if restarted := invokeRestartWorkerIfStillOnBeadBounded(
				t, d, workerID, tc.requestedBead, "mutation rejection",
			); restarted {
				t.Fatal("restartWorkerIfStillOnBead accepted rejected worker generation")
			}
			assertRestartWorkerIfStillOnBeadMutexReusable(t, d)

			d.mu.Lock()
			current := d.workers[workerID]
			pending := d.pendingManagedIDs[workerID]
			d.mu.Unlock()
			if !tc.workerPresent && current != nil {
				t.Fatalf("missing worker unexpectedly installed: %p", current)
			}
			if pending {
				t.Fatal("rejected worker gained pending-managed state")
			}
			if count := eventCount(t, d.db, "worker_restarted"); count != 0 {
				t.Fatalf("rejected worker logged %d worker_restarted events, want 0", count)
			}
			if !tc.workerPresent {
				return
			}
			if current != worker {
				t.Fatalf("rejected worker = %p, want unchanged %p", current, worker)
			}
			conn.mu.Lock()
			closed := conn.closed
			conn.mu.Unlock()
			if closed {
				t.Fatal("rejected worker connection was closed")
			}
			if status := workerDirectiveAssignmentStatus(t, d, assignmentID); status != "active" {
				t.Fatalf("rejected assignment status = %q, want active", status)
			}
		})
	}

	t.Run("valid managed worker clears durable and memory state", func(t *testing.T) {
		d, beads, _, _, _, _ := newTestDispatcher(t)
		pm := &mockProcessManager{}
		d.procMgr = pm
		const (
			workerID = "mutation-focus-valid-worker"
			beadID   = "mutation-focus-valid-bead"
			reason   = "focus --immediate mutation"
		)
		beads.mu.Lock()
		beads.shown[beadID] = &protocol.BeadDetail{ID: beadID, Status: "in_progress"}
		beads.mu.Unlock()
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
		d.mu.Unlock()

		if restarted := invokeRestartWorkerIfStillOnBeadBounded(t, d, workerID, beadID, reason); !restarted {
			t.Fatal("valid managed worker was not restarted")
		}
		assertRestartWorkerIfStillOnBeadMutexReusable(t, d)

		d.mu.Lock()
		_, tracked := d.workers[workerID]
		pending := d.pendingManagedIDs[workerID]
		_, hasSince := d.pendingManagedSince[workerID]
		_, hasAttempts := d.attemptCounts[beadID]
		_, hasHandoffs := d.handoffCounts[beadID]
		_, hasRejections := d.rejectionCounts[beadID]
		_, hasEscalation := d.escalatedBeads[beadID]
		d.mu.Unlock()
		if tracked || !pending || !hasSince {
			t.Fatalf("managed restart state: tracked=%t pending=%t since=%t", tracked, pending, hasSince)
		}
		if hasAttempts || hasHandoffs || hasRejections || hasEscalation {
			t.Fatalf("bead tracking retained: attempts=%t handoffs=%t rejections=%t escalation=%t",
				hasAttempts, hasHandoffs, hasRejections, hasEscalation)
		}
		conn.mu.Lock()
		closed := conn.closed
		conn.mu.Unlock()
		if !closed {
			t.Fatal("restarted worker connection remains open")
		}
		if got := pm.SpawnedIDs(); len(got) != 1 || got[0] != workerID {
			t.Fatalf("spawned workers = %v, want [%s]", got, workerID)
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
		if payload := restartWorkerIfStillOnBeadEventPayload(t, d, "worker_restarted", beadID, workerID); payload != `{"reason":"focus --immediate mutation"}` {
			t.Fatalf("worker_restarted payload = %q, want exact reason", payload)
		}
	})

	t.Run("bead reset failure logs exact event", func(t *testing.T) {
		d, beads, _, _, _, _ := newTestDispatcher(t)
		const (
			workerID = "mutation-focus-reset-failure-worker"
			beadID   = "mutation-focus-reset-failure-bead"
		)
		beads.mu.Lock()
		beads.shown[beadID] = &protocol.BeadDetail{ID: beadID, Status: "in_progress"}
		beads.updateErrs = map[string]error{beadID: errors.New("injected focus reset failure")}
		beads.mu.Unlock()
		assignmentID := insertActiveAssignment(t, d, beadID, workerID, "/tmp/"+beadID)
		d.mu.Lock()
		d.workers[workerID] = &trackedWorker{
			id:           workerID,
			conn:         newMockConn(),
			state:        protocol.WorkerBusy,
			beadID:       beadID,
			assignmentID: assignmentID,
		}
		d.mu.Unlock()

		if restarted := invokeRestartWorkerIfStillOnBeadBounded(t, d, workerID, beadID, "reset failure"); !restarted {
			t.Fatal("valid worker was not restarted after bead reset failure")
		}
		assertRestartWorkerIfStillOnBeadMutexReusable(t, d)
		payload := restartWorkerIfStillOnBeadEventPayload(
			t, d, "focus_immediate_bead_reset_failed", beadID, workerID,
		)
		if !strings.Contains(payload, "injected focus reset failure") {
			t.Fatalf("focus_immediate_bead_reset_failed payload = %q, want injected error", payload)
		}
	})

	t.Run("spawn failure logs exact event", func(t *testing.T) {
		d, beads, _, _, _, _ := newTestDispatcher(t)
		pm := &mockProcessManager{spawnErr: errors.New("injected focus spawn failure")}
		d.procMgr = pm
		const (
			workerID = "mutation-focus-spawn-failure-worker"
			beadID   = "mutation-focus-spawn-failure-bead"
		)
		beads.mu.Lock()
		beads.shown[beadID] = &protocol.BeadDetail{ID: beadID, Status: "in_progress"}
		beads.mu.Unlock()
		assignmentID := insertActiveAssignment(t, d, beadID, workerID, "/tmp/"+beadID)
		d.mu.Lock()
		d.workers[workerID] = &trackedWorker{
			id:           workerID,
			conn:         newMockConn(),
			state:        protocol.WorkerReviewing,
			beadID:       beadID,
			assignmentID: assignmentID,
			managed:      true,
		}
		d.mu.Unlock()

		if restarted := invokeRestartWorkerIfStillOnBeadBounded(t, d, workerID, beadID, "spawn failure"); !restarted {
			t.Fatal("valid worker was not restarted after spawn failure")
		}
		assertRestartWorkerIfStillOnBeadMutexReusable(t, d)
		if got := pm.SpawnedIDs(); len(got) != 0 {
			t.Fatalf("failed spawn recorded successful workers: %v", got)
		}
		payload := restartWorkerIfStillOnBeadEventPayload(t, d, "worker_spawn_failed", beadID, workerID)
		if !strings.Contains(payload, "injected focus spawn failure") {
			t.Fatalf("worker_spawn_failed payload = %q, want injected error", payload)
		}
	})
}
