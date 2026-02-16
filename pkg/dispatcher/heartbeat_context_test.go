package dispatcher //nolint:testpackage // internal white-box tests need access to unexported fields

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"oro/pkg/protocol"
)

// TestHeartbeatContextPctStorage verifies that heartbeat context_pct is stored
// in trackedWorker and returned in status response.
func TestHeartbeatContextPctStorage(t *testing.T) {
	t.Parallel()
	d, _, _, _, _, _ := newTestDispatcher(t)
	defer func() {
		if d.db != nil {
			_ = d.db.Close()
		}
	}()

	ctx := context.Background()

	// Register a worker
	workerID := "test-worker-1"
	w := &trackedWorker{
		id:           workerID,
		state:        protocol.WorkerIdle,
		lastSeen:     time.Now(),
		lastProgress: time.Now(),
	}
	d.mu.Lock()
	d.workers[workerID] = w
	d.mu.Unlock()

	// Send heartbeat with context_pct
	heartbeatMsg := protocol.Message{
		Type: protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{
			WorkerID:   workerID,
			BeadID:     "oro-test1",
			ContextPct: 45,
		},
	}

	d.handleHeartbeat(ctx, workerID, heartbeatMsg)

	// Verify trackedWorker has ContextPct field updated
	d.mu.Lock()
	worker := d.workers[workerID]
	if worker.contextPct != 45 {
		t.Errorf("expected contextPct=45, got %d", worker.contextPct)
	}
	d.mu.Unlock()

	// Verify status response includes context_pct
	statusJSON := d.buildStatusJSON()
	var resp statusResponse
	if err := json.Unmarshal([]byte(statusJSON), &resp); err != nil {
		t.Fatalf("failed to unmarshal status: %v", err)
	}

	if len(resp.Workers) != 1 {
		t.Fatalf("expected 1 worker in status, got %d", len(resp.Workers))
	}

	if resp.Workers[0].ContextPct != 45 {
		t.Errorf("expected context_pct=45 in status response, got %d", resp.Workers[0].ContextPct)
	}
}

// TestHeartbeatContextPctDefaultsToZero verifies that context_pct defaults
// to 0 when no heartbeat has been received.
func TestHeartbeatContextPctDefaultsToZero(t *testing.T) {
	t.Parallel()
	d, _, _, _, _, _ := newTestDispatcher(t)
	defer func() {
		if d.db != nil {
			_ = d.db.Close()
		}
	}()

	// Register a worker without sending a heartbeat
	workerID := "test-worker-2"
	w := &trackedWorker{
		id:           workerID,
		state:        protocol.WorkerIdle,
		lastSeen:     time.Now(),
		lastProgress: time.Now(),
	}
	d.mu.Lock()
	d.workers[workerID] = w
	d.mu.Unlock()

	// Verify trackedWorker has ContextPct defaulting to 0
	d.mu.Lock()
	worker := d.workers[workerID]
	if worker.contextPct != 0 {
		t.Errorf("expected contextPct=0 by default, got %d", worker.contextPct)
	}
	d.mu.Unlock()

	// Verify status response includes context_pct=0
	statusJSON := d.buildStatusJSON()
	var resp statusResponse
	if err := json.Unmarshal([]byte(statusJSON), &resp); err != nil {
		t.Fatalf("failed to unmarshal status: %v", err)
	}

	if len(resp.Workers) != 1 {
		t.Fatalf("expected 1 worker in status, got %d", len(resp.Workers))
	}

	if resp.Workers[0].ContextPct != 0 {
		t.Errorf("expected context_pct=0 in status response by default, got %d", resp.Workers[0].ContextPct)
	}
}

// TestHeartbeatContextPctMultipleUpdates verifies that context_pct is updated
// with each heartbeat message.
func TestHeartbeatContextPctMultipleUpdates(t *testing.T) {
	t.Parallel()
	d, _, _, _, _, _ := newTestDispatcher(t)
	defer func() {
		if d.db != nil {
			_ = d.db.Close()
		}
	}()

	ctx := context.Background()

	// Register a worker
	workerID := "test-worker-3"
	w := &trackedWorker{
		id:           workerID,
		state:        protocol.WorkerIdle,
		lastSeen:     time.Now(),
		lastProgress: time.Now(),
	}
	d.mu.Lock()
	d.workers[workerID] = w
	d.mu.Unlock()

	// Send first heartbeat with context_pct=25
	heartbeat1 := protocol.Message{
		Type: protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{
			WorkerID:   workerID,
			BeadID:     "oro-test1",
			ContextPct: 25,
		},
	}
	d.handleHeartbeat(ctx, workerID, heartbeat1)

	d.mu.Lock()
	if d.workers[workerID].contextPct != 25 {
		t.Errorf("expected contextPct=25 after first heartbeat, got %d", d.workers[workerID].contextPct)
	}
	d.mu.Unlock()

	// Send second heartbeat with context_pct=60
	heartbeat2 := protocol.Message{
		Type: protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{
			WorkerID:   workerID,
			BeadID:     "oro-test1",
			ContextPct: 60,
		},
	}
	d.handleHeartbeat(ctx, workerID, heartbeat2)

	d.mu.Lock()
	if d.workers[workerID].contextPct != 60 {
		t.Errorf("expected contextPct=60 after second heartbeat, got %d", d.workers[workerID].contextPct)
	}
	d.mu.Unlock()

	// Verify status response reflects latest value
	statusJSON := d.buildStatusJSON()
	var resp statusResponse
	if err := json.Unmarshal([]byte(statusJSON), &resp); err != nil {
		t.Fatalf("failed to unmarshal status: %v", err)
	}

	if resp.Workers[0].ContextPct != 60 {
		t.Errorf("expected context_pct=60 in status response, got %d", resp.Workers[0].ContextPct)
	}
}
