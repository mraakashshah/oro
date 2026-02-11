package dispatcher //nolint:testpackage // internal white-box tests need access to unexported fields

import (
	"context"
	"database/sql"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	"oro/pkg/merge"
	"oro/pkg/ops"
	"oro/pkg/protocol"

	_ "modernc.org/sqlite"
)

// TestOversizeMessage verifies that messages exceeding MaxMessageSize are
// handled gracefully without crashing the dispatcher. When the scanner
// encounters a message that exceeds MaxMessageSize, it returns an error
// and closes that connection, but the dispatcher continues to accept new
// connections.
func TestOversizeMessage(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	db := mustOpenDB(t)
	defer func() { _ = db.Close() }()

	// Setup dispatcher with in-memory UDS socket
	cfg := Config{
		SocketPath:       tempSocket(t),
		DBPath:           ":memory:",
		MaxWorkers:       1,
		HeartbeatTimeout: 10 * time.Second,
		PollInterval:     1 * time.Second,
	}
	d := New(cfg, db, merge.NewCoordinator(nil), ops.NewSpawner(nil),
		&mockBeadSource{}, &mockWorktreeManager{}, &mockEscalator{})

	go func() { _ = d.Run(ctx) }()

	// Wait for listener to be ready
	waitForListener(t, cfg.SocketPath)

	// Test 1: Send an oversized message and verify connection closes
	conn1, err := net.Dial("unix", cfg.SocketPath) //nolint:noctx // test setup
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	// Construct a message larger than MaxMessageSize (1MB)
	oversizePayload := strings.Repeat("X", protocol.MaxMessageSize+1000)
	msg := protocol.Message{
		Type: protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{
			BeadID:     oversizePayload,
			WorkerID:   "test-worker",
			ContextPct: 50,
		},
	}

	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	data = append(data, '\n')

	// The write may succeed or fail depending on timing - either is acceptable
	_, _ = conn1.Write(data)
	_ = conn1.Close()

	// Wait a bit for the dispatcher to process the error
	time.Sleep(200 * time.Millisecond)

	// Test 2: Verify dispatcher is still running by establishing a new connection
	// and sending a normal-sized message
	conn2, err := net.Dial("unix", cfg.SocketPath) //nolint:noctx // test setup
	if err != nil {
		t.Fatalf("dispatcher crashed or is unresponsive after oversize message: %v", err)
	}
	defer func() { _ = conn2.Close() }()

	// Send a normal heartbeat on the new connection
	normalMsg := protocol.Message{
		Type: protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{
			BeadID:     "bead-123",
			WorkerID:   "test-worker-2",
			ContextPct: 50,
		},
	}
	normalData, err := json.Marshal(normalMsg)
	if err != nil {
		t.Fatalf("marshal normal: %v", err)
	}
	normalData = append(normalData, '\n')

	if _, err := conn2.Write(normalData); err != nil {
		t.Fatalf("write normal to new connection: %v", err)
	}

	// Success: dispatcher is still running and accepting connections
	// The test passes if we can establish a new connection and send a message
}

// TestReconnectPayloadValidation verifies that a ReconnectPayload with more
// than maxBufferedMessages is rejected.
func TestReconnectPayloadValidation(t *testing.T) {
	// Create a ReconnectPayload with 200 buffered events
	buffered := make([]protocol.Message, 200)
	for i := range buffered {
		buffered[i] = protocol.Message{
			Type: protocol.MsgHeartbeat,
			Heartbeat: &protocol.HeartbeatPayload{
				BeadID:     "bead-1",
				WorkerID:   "worker-1",
				ContextPct: 50,
			},
		}
	}

	payload := protocol.ReconnectPayload{
		WorkerID:       "worker-1",
		BeadID:         "bead-1",
		State:          "running",
		ContextPct:     50,
		BufferedEvents: buffered,
	}

	// Validate should return an error
	err := payload.Validate()
	if err == nil {
		t.Fatal("expected validation error for 200 buffered events, got nil")
	}

	expectedErrSubstring := "too many buffered events"
	if !strings.Contains(err.Error(), expectedErrSubstring) {
		t.Errorf("expected error containing %q, got %q", expectedErrSubstring, err.Error())
	}
}

// TestReconnectPayloadValidationPass verifies that Validate() passes for
// valid payloads.
func TestReconnectPayloadValidationPass(t *testing.T) {
	// Valid payload with 100 buffered events (exactly at the limit)
	buffered := make([]protocol.Message, 100)
	for i := range buffered {
		buffered[i] = protocol.Message{
			Type: protocol.MsgHeartbeat,
			Heartbeat: &protocol.HeartbeatPayload{
				BeadID:     "bead-1",
				WorkerID:   "worker-1",
				ContextPct: 50,
			},
		}
	}

	payload := protocol.ReconnectPayload{
		WorkerID:       "worker-1",
		BeadID:         "bead-1",
		State:          "running",
		ContextPct:     50,
		BufferedEvents: buffered,
	}

	if err := payload.Validate(); err != nil {
		t.Errorf("expected no error for 100 buffered events, got %v", err)
	}

	// Valid payload with 50 buffered events
	payload.BufferedEvents = buffered[:50]
	if err := payload.Validate(); err != nil {
		t.Errorf("expected no error for 50 buffered events, got %v", err)
	}

	// Valid payload with 0 buffered events
	payload.BufferedEvents = nil
	if err := payload.Validate(); err != nil {
		t.Errorf("expected no error for 0 buffered events, got %v", err)
	}
}

// Helper functions

func mustOpenDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	return db
}

func tempSocket(t *testing.T) string {
	t.Helper()
	return t.TempDir() + "/oro-test.sock"
}

func waitForListener(t *testing.T, socketPath string) {
	t.Helper()
	for i := 0; i < 50; i++ {
		conn, err := net.Dial("unix", socketPath) //nolint:noctx // test setup
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("listener did not become ready")
}
