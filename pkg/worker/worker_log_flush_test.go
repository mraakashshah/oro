package worker_test

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"oro/pkg/protocol"
	"oro/pkg/worker"
)

// TestProcessOutputDoesNotBlockOnHighThroughput verifies that processOutput
// does NOT block on per-line flush calls, allowing high-throughput output
// to be processed without deadlock.
//
// This test simulates a subprocess generating many lines of output rapidly.
// Prior to the fix, Flush() on every line would cause blocking I/O and eventual
// hang after 2-3 minutes. After the fix, buffered writes should handle this
// without blocking.
func TestProcessOutputDoesNotBlockOnHighThroughput(t *testing.T) {
	// Create temp home directory for test isolation
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	// Generate many tool-use events to simulate high-throughput output.
	// Tool-use events produce "-> ToolName" log lines.
	const numLines = 1000
	lines := make([]string, numLines)
	for i := 0; i < numLines; i++ {
		lines[i] = toolUseLine("Read")
	}

	// Create mock spawner with high-throughput stdout
	spawner := newMockSpawner()
	spawner.stdout = newPipeReader(lines)

	// Create worker with pipe connection
	dispatcherConn, workerConn := net.Pipe()
	defer func() { _ = dispatcherConn.Close() }()

	w := worker.NewWithConn("test-worker-flush", workerConn, spawner)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Start worker in background
	errCh := startWorkerRun(ctx, t, w, dispatcherConn)
	defer func() {
		cancel()
		<-errCh // wait for worker to exit
	}()

	// Send ASSIGN to trigger processOutput
	sendMessage(t, dispatcherConn, protocol.Message{
		Type: protocol.MsgAssign,
		Assign: &protocol.AssignPayload{
			BeadID:   "high-throughput-bead",
			Worktree: "/fake/worktree",
		},
	})

	// Read STATUS response - should arrive quickly without blocking
	statusReceived := make(chan struct{})
	go func() {
		msg := readMessage(t, dispatcherConn)
		if msg.Type != protocol.MsgStatus {
			t.Errorf("expected STATUS, got %s", msg.Type)
		}
		close(statusReceived)
	}()

	select {
	case <-statusReceived:
		// Success - no blocking occurred
	case <-time.After(5 * time.Second):
		t.Fatal("Timeout waiting for STATUS - processOutput likely blocked on flush")
	}

	// Give processOutput time to write all buffered data
	time.Sleep(500 * time.Millisecond)

	// Verify log file contains output (buffering should still work)
	logPath := filepath.Join(tempHome, ".oro", "workers", "test-worker-flush", "output.log")
	// #nosec G304 -- test code reading from test temp directory
	content, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("Failed to read log file at %s: %v", logPath, err)
	}

	// Verify we got substantial output (should have all lines after buffer flush)
	logContent := string(content)
	lineCount := len(strings.Split(strings.TrimSpace(logContent), "\n"))
	if lineCount < numLines/2 {
		t.Errorf("Expected at least %d lines in log file, got %d", numLines/2, lineCount)
	}
}

// TestProcessOutputBuffersCorrectly verifies that log output is correctly
// buffered and eventually written without per-line flush.
func TestProcessOutputBuffersCorrectly(t *testing.T) {
	// Create temp home directory for test isolation
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	// Use tool-use events which produce "-> ToolName" log output.
	testLines := []string{
		toolUseLine("Read"),
		toolUseLine("Bash"),
		toolUseLine("Edit"),
		toolUseLine("Write"),
	}

	spawner := newMockSpawner()
	spawner.stdout = newPipeReader(testLines)

	dispatcherConn, workerConn := net.Pipe()
	defer func() { _ = dispatcherConn.Close() }()

	w := worker.NewWithConn("test-worker-buffer", workerConn, spawner)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	errCh := startWorkerRun(ctx, t, w, dispatcherConn)
	defer func() {
		cancel()
		<-errCh
	}()

	sendMessage(t, dispatcherConn, protocol.Message{
		Type: protocol.MsgAssign,
		Assign: &protocol.AssignPayload{
			BeadID:   "buffer-test-bead",
			Worktree: "/fake/worktree",
		},
	})

	// Read STATUS
	msg := readMessage(t, dispatcherConn)
	if msg.Type != protocol.MsgStatus {
		t.Fatalf("expected STATUS, got %s", msg.Type)
	}

	// Allow time for buffer flush
	time.Sleep(300 * time.Millisecond)

	// Verify all tool activities made it to the log file
	logPath := filepath.Join(tempHome, ".oro", "workers", "test-worker-buffer", "output.log")
	// #nosec G304 -- test code reading from test temp directory
	content, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("Failed to read log file: %v", err)
	}

	logContent := string(content)
	expectedActivities := []string{
		"-> Read",
		"-> Bash",
		"-> Edit",
		"-> Write",
	}
	for _, expected := range expectedActivities {
		if !strings.Contains(logContent, expected) {
			t.Errorf("Log file missing expected activity: %q\nLog content:\n%s", expected, logContent)
		}
	}
}
