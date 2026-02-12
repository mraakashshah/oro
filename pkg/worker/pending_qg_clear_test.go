package worker //nolint:testpackage // need access to pendingQGOutput

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"oro/pkg/protocol"
)

// TestHandleAssignClearsPendingQGOutput verifies that handleAssign resets
// pendingQGOutput so stale quality-gate output from a previous (rejected)
// assignment is not sent on the next approval.
func TestHandleAssignClearsPendingQGOutput(t *testing.T) {
	t.Parallel()

	clientConn, serverConn := net.Pipe()
	defer func() { _ = serverConn.Close() }()

	spawner := &mockSpawnerSimple{}
	w := NewWithConn("w-qg-clear", clientConn, spawner)

	// Pre-set pendingQGOutput to simulate stale data from a rejected QG pass.
	w.mu.Lock()
	w.pendingQGOutput = "stale quality gate output"
	w.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start worker in background.
	errCh := make(chan error, 1)
	go func() { errCh <- w.Run(ctx) }()

	// Drain the initial heartbeat so Run() enters its event loop.
	_ = serverConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	scanner := bufio.NewScanner(serverConn)
	if !scanner.Scan() {
		t.Fatalf("failed to read initial heartbeat: %v", scanner.Err())
	}

	// Send ASSIGN — this triggers handleAssign.
	msg := protocol.Message{
		Type: protocol.MsgAssign,
		Assign: &protocol.AssignPayload{
			BeadID:   "bead-qg-clear",
			Worktree: t.TempDir(),
		},
	}
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	data = append(data, '\n')
	if _, err := serverConn.Write(data); err != nil {
		t.Fatalf("write ASSIGN: %v", err)
	}

	// Drain the STATUS message that handleAssign sends after spawning.
	_ = serverConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if !scanner.Scan() {
		t.Fatalf("failed to read STATUS: %v", scanner.Err())
	}

	// Verify pendingQGOutput was cleared.
	w.mu.Lock()
	got := w.pendingQGOutput
	w.mu.Unlock()

	if got != "" {
		t.Errorf("pendingQGOutput not cleared after handleAssign: got %q, want empty", got)
	}

	cancel()
	<-errCh
}

// mockSpawnerSimple is a minimal spawner for internal tests that need
// access to unexported fields. It returns a process whose stdout closes
// immediately (empty output).
type mockSpawnerSimple struct{}

func (m *mockSpawnerSimple) Spawn(_ context.Context, _, _, _ string) (Process, io.ReadCloser, io.WriteCloser, error) {
	return &mockProcessSimple{}, io.NopCloser(strings.NewReader("")), nil, nil
}

type mockProcessSimple struct{}

func (m *mockProcessSimple) Wait() error { return nil }
func (m *mockProcessSimple) Kill() error { return nil }
