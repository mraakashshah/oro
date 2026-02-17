package worker //nolint:testpackage // need access to NewWithConn

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"oro/pkg/protocol"
)

// TestWorkerOversizeMessage verifies that the worker handles oversize messages
// from the dispatcher without crashing. When a message exceeds MaxMessageSize,
// the scanner returns an error and the connection is closed, but the worker
// handles this gracefully.
func TestWorkerOversizeMessage(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// Create a pipe to simulate UDS connection
	clientConn, serverConn := net.Pipe()

	// Create worker with mock spawner
	spawner := &mockSpawner{}
	w := NewWithConn("test-worker", clientConn, spawner)

	// Start worker in background
	errCh := make(chan error, 1)
	go func() {
		errCh <- w.Run(ctx)
	}()

	// Wait for initial heartbeat
	buf := make([]byte, 8192)
	_ = serverConn.SetReadDeadline(time.Now().Add(1 * time.Second))
	n, err := serverConn.Read(buf)
	if err != nil || n == 0 {
		t.Fatal("expected initial heartbeat")
	}

	// Send an oversized ASSIGN message (> MaxMessageSize)
	// We send it in a goroutine because the write will block when the pipe
	// buffer fills up, and the scanner on the other end will error before
	// consuming all the data.
	go func() {
		oversizePrompt := strings.Repeat("X", protocol.MaxMessageSize+1000)
		msg := protocol.Message{
			Type: protocol.MsgAssign,
			Assign: &protocol.AssignPayload{
				BeadID:   "bead-1",
				Worktree: "/tmp/wt",
				Title:    oversizePrompt,
				Model:    "opus",
			},
		}

		data, err := json.Marshal(msg)
		if err != nil {
			return
		}
		data = append(data, '\n')

		// Write may block or fail - either is acceptable
		_, _ = serverConn.Write(data)
	}()

	// The worker's scanner will encounter an error when trying to read the
	// oversized message. This causes the readMessages goroutine to send an
	// error on errCh, which triggers handleConnectionError. The important
	// test is that the worker doesn't crash or panic.

	// Wait for worker to exit with error
	select {
	case err := <-errCh:
		// We expect either a scanner error or connection closed error
		if err == nil {
			t.Fatal("expected error from oversized message")
		}
		// Success: worker exited cleanly with an error (not a panic)
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not exit after receiving oversized message")
	}

	_ = serverConn.Close()
	_ = clientConn.Close()
}

// Helper types and functions

type mockSpawner struct{}

func (m *mockSpawner) Spawn(ctx context.Context, model string, prompt string, workdir string) (Process, io.ReadCloser, io.WriteCloser, error) {
	// Return a mock process that does nothing
	return &mockProcess{}, io.NopCloser(strings.NewReader("")), nil, nil
}

type mockProcess struct{}

func (m *mockProcess) Wait() error { return nil }
func (m *mockProcess) Kill() error { return nil }

func TestLoadThresholds(t *testing.T) {
	t.Run("loads from file", func(t *testing.T) {
		dir := t.TempDir()
		data := `{"opus": 65, "sonnet": 50, "haiku": 40}`
		if err := os.WriteFile(filepath.Join(dir, "thresholds.json"), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}

		th := loadThresholds(dir)

		if th.For("opus") != 65 {
			t.Errorf("opus: got %d, want 65", th.For("opus"))
		}
		if th.For("sonnet") != 50 {
			t.Errorf("sonnet: got %d, want 50", th.For("sonnet"))
		}
		if th.For("haiku") != 40 {
			t.Errorf("haiku: got %d, want 40", th.For("haiku"))
		}
	})

	t.Run("falls back to default when file missing", func(t *testing.T) {
		dir := t.TempDir()
		th := loadThresholds(dir)

		if th.For("opus") != 50 {
			t.Errorf("opus default: got %d, want 50", th.For("opus"))
		}
	})

	t.Run("falls back to default for unknown model", func(t *testing.T) {
		dir := t.TempDir()
		data := `{"opus": 65, "sonnet": 50, "haiku": 40}`
		if err := os.WriteFile(filepath.Join(dir, "thresholds.json"), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}

		th := loadThresholds(dir)

		if th.For("unknown-model") != 50 {
			t.Errorf("unknown model: got %d, want 50", th.For("unknown-model"))
		}
	})
}
