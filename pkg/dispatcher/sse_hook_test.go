package dispatcher //nolint:testpackage // internal white-box tests need access to unexported fields

import (
	"context"
	"sync"
	"testing"
)

// mockSSEBroadcaster captures Send() calls for testing.
type mockSSEBroadcaster struct {
	mu        sync.Mutex
	sendCalls []SSESendCall
}

type SSESendCall struct {
	EventType string
	BeadID    string
	WorkerID  string
}

func (m *mockSSEBroadcaster) Send(eventType, beadID, workerID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sendCalls = append(m.sendCalls, SSESendCall{
		EventType: eventType,
		BeadID:    beadID,
		WorkerID:  workerID,
	})
}

func TestLogEventCallsSSE(t *testing.T) {
	// Setup: create dispatcher with mock SSEBroadcaster
	db := newTestDB(t)
	defer db.Close()

	d := &Dispatcher{
		db:             db,
		sseBroadcaster: &mockSSEBroadcaster{},
	}

	ctx := context.Background()

	// Test logEvent calls sseBroadcaster.Send
	eventType := "test_event"
	beadID := "oro-test1"
	workerID := "worker-1"
	source := "dispatcher"
	payload := `{"status":"ok"}`

	err := d.logEvent(ctx, eventType, source, beadID, workerID, payload)
	if err != nil {
		t.Fatalf("logEvent failed: %v", err)
	}

	// Verify Send was called with correct arguments
	mockBc := d.sseBroadcaster.(*mockSSEBroadcaster)
	if len(mockBc.sendCalls) != 1 {
		t.Errorf("expected 1 Send call, got %d", len(mockBc.sendCalls))
	}

	if len(mockBc.sendCalls) > 0 {
		call := mockBc.sendCalls[0]
		if call.EventType != eventType {
			t.Errorf("Send eventType = %q, want %q", call.EventType, eventType)
		}
		if call.BeadID != beadID {
			t.Errorf("Send beadID = %q, want %q", call.BeadID, beadID)
		}
		if call.WorkerID != workerID {
			t.Errorf("Send workerID = %q, want %q", call.WorkerID, workerID)
		}
	}
}

func TestLogEventLockedCallsSSE(t *testing.T) {
	// Setup: create dispatcher with mock SSEBroadcaster
	db := newTestDB(t)
	defer db.Close()

	d := &Dispatcher{
		db:             db,
		sseBroadcaster: &mockSSEBroadcaster{},
	}

	ctx := context.Background()

	// Test logEventLocked calls sseBroadcaster.Send
	eventType := "locked_event"
	beadID := "oro-test2"
	workerID := "worker-2"
	source := "dispatcher"
	payload := `{"status":"locked"}`

	err := d.logEventLocked(ctx, eventType, source, beadID, workerID, payload)
	if err != nil {
		t.Fatalf("logEventLocked failed: %v", err)
	}

	// Verify Send was called with correct arguments
	mockBc := d.sseBroadcaster.(*mockSSEBroadcaster)
	if len(mockBc.sendCalls) != 1 {
		t.Errorf("expected 1 Send call, got %d", len(mockBc.sendCalls))
	}

	if len(mockBc.sendCalls) > 0 {
		call := mockBc.sendCalls[0]
		if call.EventType != eventType {
			t.Errorf("Send eventType = %q, want %q", call.EventType, eventType)
		}
		if call.BeadID != beadID {
			t.Errorf("Send beadID = %q, want %q", call.BeadID, beadID)
		}
		if call.WorkerID != workerID {
			t.Errorf("Send workerID = %q, want %q", call.WorkerID, workerID)
		}
	}
}

func TestSSEBroadcasterInitializedInNew(t *testing.T) {
	// Setup: create a Dispatcher with newTestDispatcher (which uses New())
	d, _, _, _, _, _ := newTestDispatcher(t)

	// Verify sseBroadcaster is initialized and not nil
	if d.sseBroadcaster == nil {
		t.Error("sseBroadcaster is nil, expected to be initialized")
	}
}
