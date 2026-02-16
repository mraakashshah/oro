package dispatcher //nolint:testpackage // white-box test needs internal access

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyWorkerLogsDirective(t *testing.T) {
	// Create a test dispatcher
	d, _, _, _, _, _ := newTestDispatcher(t)
	cancel := startDispatcher(t, d)
	defer cancel()

	// Register a worker
	workerID := "test-worker-logs"
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	// Drain clientConn so writes don't block
	go func() {
		buf := make([]byte, 4096)
		for {
			if _, err := clientConn.Read(buf); err != nil {
				return
			}
		}
	}()

	d.registerWorker(workerID, serverConn)

	// Create log file with test content
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("get home dir: %v", err)
	}
	logDir := filepath.Join(home, ".oro", "workers", workerID)
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		t.Fatalf("create log dir: %v", err)
	}
	defer os.RemoveAll(filepath.Join(home, ".oro", "workers", workerID))

	logPath := filepath.Join(logDir, "output.log")
	testLines := []string{
		"line 1",
		"line 2",
		"line 3",
		"line 4",
		"line 5",
	}
	content := strings.Join(testLines, "\n") + "\n"
	if err := os.WriteFile(logPath, []byte(content), 0o600); err != nil {
		t.Fatalf("write log file: %v", err)
	}

	t.Run("returns last N lines from worker output.log file", func(t *testing.T) {
		// Test default (20 lines, but we only have 5)
		result, err := d.applyWorkerLogs(workerID)
		if err != nil {
			t.Fatalf("applyWorkerLogs failed: %v", err)
		}
		if !strings.Contains(result, "line 1") || !strings.Contains(result, "line 5") {
			t.Errorf("expected all lines, got: %s", result)
		}

		// Test with explicit count
		result, err = d.applyWorkerLogs(fmt.Sprintf("%s 3", workerID))
		if err != nil {
			t.Fatalf("applyWorkerLogs with count failed: %v", err)
		}
		if !strings.Contains(result, "line 3") || !strings.Contains(result, "line 5") {
			t.Errorf("expected last 3 lines, got: %s", result)
		}
		if strings.Contains(result, "line 1") || strings.Contains(result, "line 2") {
			t.Errorf("expected only last 3 lines, got: %s", result)
		}
	})

	t.Run("invalid worker ID returns error", func(t *testing.T) {
		_, err := d.applyWorkerLogs("nonexistent-worker")
		if err == nil {
			t.Fatal("expected error for unknown worker ID")
		}
	})

	t.Run("missing log file returns no output available", func(t *testing.T) {
		// Register another worker without a log file
		workerID2 := "test-worker-nolog"
		serverConn2, clientConn2 := net.Pipe()
		defer serverConn2.Close()
		defer clientConn2.Close()

		// Drain clientConn2
		go func() {
			buf := make([]byte, 4096)
			for {
				if _, err := clientConn2.Read(buf); err != nil {
					return
				}
			}
		}()

		d.registerWorker(workerID2, serverConn2)

		result, err := d.applyWorkerLogs(workerID2)
		if err != nil {
			t.Fatalf("applyWorkerLogs should not error on missing file: %v", err)
		}
		if !strings.Contains(result, "no output available") {
			t.Errorf("expected 'no output available', got: %s", result)
		}
	})

	t.Run("path traversal attempt returns error", func(t *testing.T) {
		tests := []string{
			"../../../etc/passwd",
			"worker/../../../etc/passwd",
			"/etc/passwd",
			"worker-id; rm -rf /",
		}
		for _, badID := range tests {
			_, err := d.applyWorkerLogs(badID)
			if err == nil {
				t.Errorf("expected error for path traversal attempt with ID: %s", badID)
			}
		}
	})
}
