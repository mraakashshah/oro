package dispatcher //nolint:testpackage // white-box test needs internal access

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
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

	// --- Mutation-killing tests for applyWorkerLogs ---

	t.Run("empty args returns error", func(t *testing.T) {
		// Kills mutation: suppress 'worker-logs requires worker ID argument' error
		_, err := d.applyWorkerLogs("")
		if err == nil {
			t.Fatal("expected error for empty args")
		}
		if !strings.Contains(err.Error(), "worker ID") {
			t.Errorf("expected worker ID error, got: %v", err)
		}
	})

	t.Run("explicit count 1 returns only last line", func(t *testing.T) {
		// Kills mutation: len(parts) >= 1 vs > 1.
		// With the buggy >= 1, a single-arg call treats the workerID as count arg
		// position (off-by-one in slice logic), causing incorrect behaviour.
		result, err := d.applyWorkerLogs(fmt.Sprintf("%s 1", workerID))
		if err != nil {
			t.Fatalf("applyWorkerLogs count=1 failed: %v", err)
		}
		if !strings.Contains(result, "line 5") {
			t.Errorf("expected line 5, got: %s", result)
		}
		if strings.Contains(result, "line 4") || strings.Contains(result, "line 3") ||
			strings.Contains(result, "line 2") || strings.Contains(result, "line 1") {
			t.Errorf("expected only last 1 line, got: %s", result)
		}
	})

	t.Run("zero count returns error", func(t *testing.T) {
		// Kills mutation: suppress 'line count must be positive' for count <= 0
		_, err := d.applyWorkerLogs(fmt.Sprintf("%s 0", workerID))
		if err == nil {
			t.Fatal("expected error for count=0")
		}
		if !strings.Contains(err.Error(), "positive") {
			t.Errorf("expected positive-count error, got: %v", err)
		}
	})

	t.Run("negative count returns error", func(t *testing.T) {
		// Kills mutation: suppress 'line count must be positive' for count <= 0
		_, err := d.applyWorkerLogs(fmt.Sprintf("%s -5", workerID))
		if err == nil {
			t.Fatal("expected error for negative count")
		}
		if !strings.Contains(err.Error(), "positive") {
			t.Errorf("expected positive-count error, got: %v", err)
		}
	})

	t.Run("non-numeric count returns error", func(t *testing.T) {
		// Kills mutation: suppress 'invalid line count' error
		_, err := d.applyWorkerLogs(fmt.Sprintf("%s abc", workerID))
		if err == nil {
			t.Fatal("expected error for non-numeric count")
		}
		if !strings.Contains(err.Error(), "invalid line count") {
			t.Errorf("expected invalid line count error, got: %v", err)
		}
	})

	t.Run("illegal characters in worker ID returns error", func(t *testing.T) {
		// Kills mutation: suppress 'illegal characters' error.
		// IDs must be single tokens (no spaces) with illegal chars to hit ID validation.
		badIDs := []string{
			"worker@id",
			"worker!",
			"worker#name",
			"worker$1",
		}
		for _, id := range badIDs {
			_, err := d.applyWorkerLogs(id)
			if err == nil {
				t.Errorf("expected error for illegal worker ID: %q", id)
			}
			if !strings.Contains(err.Error(), "illegal characters") {
				t.Errorf("expected illegal characters error for %q, got: %v", id, err)
			}
		}
	})

	t.Run("worker not found returns error", func(t *testing.T) {
		// Kills mutation: suppress 'worker X not found' error
		_, err := d.applyWorkerLogs("valid-but-missing")
		if err == nil {
			t.Fatal("expected error for missing worker")
		}
		if !strings.Contains(err.Error(), "not found") {
			t.Errorf("expected not found error, got: %v", err)
		}
	})
}

// TestReadLastNLines tests readLastNLines directly to kill mutations in that function.
func TestReadLastNLines(t *testing.T) {
	t.Run("exact N lines returns all lines in order", func(t *testing.T) {
		f := t.TempDir() + "/test.log"
		lines := []string{"alpha", "beta", "gamma"}
		if err := os.WriteFile(f, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := readLastNLines(f, 3)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 3 {
			t.Fatalf("expected 3 lines, got %d: %v", len(got), got)
		}
		for i, want := range lines {
			if got[i] != want {
				t.Errorf("line %d: want %q, got %q", i, want, got[i])
			}
		}
	})

	t.Run("fewer than N lines returns all lines", func(t *testing.T) {
		f := t.TempDir() + "/test.log"
		if err := os.WriteFile(f, []byte("only one line\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := readLastNLines(f, 10)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("expected 1 line, got %d: %v", len(got), got)
		}
		if got[0] != "only one line" {
			t.Errorf("expected 'only one line', got %q", got[0])
		}
	})

	t.Run("more than N lines returns last N in order", func(t *testing.T) {
		f := t.TempDir() + "/test.log"
		var allLines []string
		for i := 1; i <= 10; i++ {
			allLines = append(allLines, fmt.Sprintf("line %d", i))
		}
		if err := os.WriteFile(f, []byte(strings.Join(allLines, "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := readLastNLines(f, 3)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 3 {
			t.Fatalf("expected 3 lines, got %d: %v", len(got), got)
		}
		want := []string{"line 8", "line 9", "line 10"}
		for i, w := range want {
			if got[i] != w {
				t.Errorf("line %d: want %q, got %q", i, w, got[i])
			}
		}
	})

	t.Run("empty file returns empty slice", func(t *testing.T) {
		f := t.TempDir() + "/empty.log"
		if err := os.WriteFile(f, []byte(""), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := readLastNLines(f, 5)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("expected empty slice, got: %v", got)
		}
	})

	t.Run("nonexistent file returns error", func(t *testing.T) {
		_, err := readLastNLines("/nonexistent/path/to/file.log", 5)
		if err == nil {
			t.Fatal("expected error for nonexistent file")
		}
		if !strings.Contains(err.Error(), "open file") {
			t.Errorf("expected 'open file' error, got: %v", err)
		}
	})

	t.Run("lines returned in correct order not reversed", func(t *testing.T) {
		f := t.TempDir() + "/order.log"
		lines := []string{"first", "second", "third", "fourth", "fifth"}
		if err := os.WriteFile(f, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := readLastNLines(f, 3)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// Must be in original file order: third, fourth, fifth
		if got[0] != "third" || got[1] != "fourth" || got[2] != "fifth" {
			t.Errorf("lines not in correct order: %v", got)
		}
	})

	t.Run("oversized line is returned intact with trailing lines", func(t *testing.T) {
		f := t.TempDir() + "/oversized.log"
		oversized := strings.Repeat("x", 1<<20)
		content := oversized + "\nordinary\nlast"
		if err := os.WriteFile(f, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := readLastNLines(f, 3)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := []string{oversized, "ordinary", "last"}
		if len(got) != len(want) {
			t.Fatalf("unexpected line count: got %d, want %d", len(got), len(want))
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("unexpected lines: got lengths [%d, %d, %d], want [%d, %d, %d]", len(got[0]), len(got[1]), len(got[2]), len(want[0]), len(want[1]), len(want[2]))
		}
	})
}
