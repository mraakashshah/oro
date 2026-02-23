package worker_test

import (
	"bytes"
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

// pipeReader simulates an io.ReadCloser that returns predefined lines
type pipeReader struct {
	*bytes.Buffer
}

func newPipeReader(lines []string) *pipeReader {
	var buf bytes.Buffer
	for _, line := range lines {
		buf.WriteString(line)
		buf.WriteString("\n")
	}
	return &pipeReader{Buffer: &buf}
}

func (p *pipeReader) Close() error {
	return nil
}

// TestProcessOutputWritesToLogFile verifies that processOutput tees
// subprocess stdout to ~/.oro/workers/<worker-id>/output.log
func TestProcessOutputWritesToLogFile(t *testing.T) {
	// Create temp home directory for test isolation
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	// Create mock spawner with simulated stdout
	spawner := newMockSpawner()
	spawner.stdout = newPipeReader([]string{
		"line 1: starting work",
		"line 2: doing something",
		"line 3: finished",
	})

	// Create worker with pipe connection
	dispatcherConn, workerConn := net.Pipe()
	defer func() { _ = dispatcherConn.Close() }()

	w := worker.NewWithConn("test-worker-1", workerConn, spawner)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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
			BeadID:   "test-bead",
			Worktree: "/fake/worktree",
		},
	})

	// Read STATUS response
	msg := readMessage(t, dispatcherConn)
	if msg.Type != protocol.MsgStatus {
		t.Fatalf("expected STATUS, got %s", msg.Type)
	}

	// Give processOutput time to complete
	time.Sleep(200 * time.Millisecond)

	// Verify log file exists and contains expected lines
	logPath := filepath.Join(tempHome, ".oro", "workers", "test-worker-1", "output.log")
	// #nosec G304 -- test code reading from test temp directory
	content, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("Failed to read log file at %s: %v", logPath, err)
	}

	logContent := string(content)
	expectedLines := []string{
		"line 1: starting work",
		"line 2: doing something",
		"line 3: finished",
	}
	for _, expectedLine := range expectedLines {
		if !strings.Contains(logContent, expectedLine) {
			t.Errorf("Log file missing expected line: %q\nLog content:\n%s", expectedLine, logContent)
		}
	}
}

// TestOpenLogFileAppendsInsteadOfTruncating verifies that openLogFile uses O_APPEND,
// preserving pre-existing log content on each assignment instead of truncating it.
func TestOpenLogFileAppendsInsteadOfTruncating(t *testing.T) {
	// Create temp home directory for test isolation
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	// Create mock spawner
	spawner := newMockSpawner()
	spawner.stdout = newPipeReader([]string{"first assignment output"})

	// Create worker with pipe connection
	dispatcherConn, workerConn := net.Pipe()
	defer func() { _ = dispatcherConn.Close() }()

	w := worker.NewWithConn("test-worker-append", workerConn, spawner)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Start worker in background
	errCh := startWorkerRun(ctx, t, w, dispatcherConn)
	defer func() {
		cancel()
		<-errCh // wait for worker to exit
	}()

	// First assignment
	sendMessage(t, dispatcherConn, protocol.Message{
		Type: protocol.MsgAssign,
		Assign: &protocol.AssignPayload{
			BeadID:   "first-bead",
			Worktree: "/fake/worktree1",
		},
	})

	// Read STATUS response
	msg := readMessage(t, dispatcherConn)
	if msg.Type != protocol.MsgStatus {
		t.Fatalf("expected STATUS, got %s", msg.Type)
	}

	// Give processOutput time to complete
	time.Sleep(200 * time.Millisecond)

	// Verify first log file has content
	logPath := filepath.Join(tempHome, ".oro", "workers", "test-worker-append", "output.log")
	// #nosec G304 -- test code reading from test temp directory
	firstContent, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("Failed to read first log file: %v", err)
	}
	if !strings.Contains(string(firstContent), "first assignment output") {
		t.Errorf("First log file missing expected content")
	}

	// Second assignment with different stdout
	spawner.stdout = newPipeReader([]string{"second assignment output"})
	sendMessage(t, dispatcherConn, protocol.Message{
		Type: protocol.MsgAssign,
		Assign: &protocol.AssignPayload{
			BeadID:   "second-bead",
			Worktree: "/fake/worktree2",
		},
	})

	// Read STATUS response
	msg = readMessage(t, dispatcherConn)
	if msg.Type != protocol.MsgStatus {
		t.Fatalf("expected STATUS after second ASSIGN, got %s", msg.Type)
	}

	// Give processOutput time to complete
	time.Sleep(200 * time.Millisecond)

	// Verify log file contains BOTH first and second assignment output (appended, not truncated)
	// #nosec G304 -- test code reading from test temp directory
	finalContent, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("Failed to read final log file: %v", err)
	}

	finalStr := string(finalContent)
	if !strings.Contains(finalStr, "first assignment output") {
		t.Errorf("Log file should contain first assignment output after second ASSIGN (appended, not truncated).\nContent:\n%s", finalStr)
	}
	if !strings.Contains(finalStr, "second assignment output") {
		t.Errorf("Log file should contain second assignment output.\nContent:\n%s", finalStr)
	}
}
