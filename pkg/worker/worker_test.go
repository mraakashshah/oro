package worker_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"oro/pkg/protocol"
	"oro/pkg/worker"
)

// mockProcess implements worker.Process for testing.
type mockProcess struct {
	mu      sync.Mutex
	killed  bool
	waitCh  chan struct{} // close to unblock Wait
	waitErr error
}

func newMockProcess() *mockProcess {
	return &mockProcess{waitCh: make(chan struct{})}
}

func (p *mockProcess) Wait() error {
	<-p.waitCh
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.waitErr
}

func (p *mockProcess) Kill() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.killed = true
	select {
	case <-p.waitCh:
	default:
		close(p.waitCh)
	}
	return nil
}

func (p *mockProcess) Killed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.killed
}

// mockSpawner implements worker.SubprocessSpawner for testing.
type mockSpawner struct {
	mu       sync.Mutex
	calls    []spawnCall
	process  *mockProcess
	spawnErr error
}

type spawnCall struct {
	Prompt  string
	Workdir string
}

func newMockSpawner() *mockSpawner {
	return &mockSpawner{process: newMockProcess()}
}

func (s *mockSpawner) Spawn(_ context.Context, prompt, workdir string) (worker.Process, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, spawnCall{Prompt: prompt, Workdir: workdir})
	if s.spawnErr != nil {
		return nil, s.spawnErr
	}
	return s.process, nil
}

func (s *mockSpawner) SpawnCalls() []spawnCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	dst := make([]spawnCall, len(s.calls))
	copy(dst, s.calls)
	return dst
}

// readMessage reads a single line-delimited JSON message from a connection.
func readMessage(t *testing.T, conn net.Conn) protocol.Message {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	scanner := bufio.NewScanner(conn)
	if !scanner.Scan() {
		t.Fatalf("failed to read message: %v", scanner.Err())
	}
	var msg protocol.Message
	if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
		t.Fatalf("failed to unmarshal message: %v", err)
	}
	return msg
}

// readMessageAsync reads a message in a goroutine and sends it on the returned channel.
// This is needed because net.Pipe writes block until the other side reads.
func readMessageAsync(t *testing.T, conn net.Conn) <-chan protocol.Message {
	t.Helper()
	ch := make(chan protocol.Message, 1)
	go func() {
		conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		scanner := bufio.NewScanner(conn)
		if scanner.Scan() {
			var msg protocol.Message
			if err := json.Unmarshal(scanner.Bytes(), &msg); err == nil {
				ch <- msg
			}
		}
	}()
	return ch
}

// sendMessage writes a line-delimited JSON message to a connection.
func sendMessage(t *testing.T, conn net.Conn, msg protocol.Message) {
	t.Helper()
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("failed to marshal message: %v", err)
	}
	data = append(data, '\n')
	if _, err := conn.Write(data); err != nil {
		t.Fatalf("failed to write message: %v", err)
	}
}

// --- Tests ---

func TestReceiveAssign_StoresState(t *testing.T) {
	t.Parallel()

	spawner := newMockSpawner()
	dispatcherConn, workerConn := net.Pipe()
	defer dispatcherConn.Close()

	w := worker.NewWithConn("w-1", workerConn, spawner)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- w.Run(ctx) }()

	// Send ASSIGN
	sendMessage(t, dispatcherConn, protocol.Message{
		Type: protocol.MsgAssign,
		Assign: &protocol.AssignPayload{
			BeadID:   "bead-42",
			Worktree: "/tmp/wt-42",
		},
	})

	// Read the STATUS message the worker should send after receiving ASSIGN
	msg := readMessage(t, dispatcherConn)
	if msg.Type != protocol.MsgStatus {
		t.Fatalf("expected STATUS, got %s", msg.Type)
	}
	if msg.Status.BeadID != "bead-42" {
		t.Errorf("expected bead_id bead-42, got %s", msg.Status.BeadID)
	}
	if msg.Status.WorkerID != "w-1" {
		t.Errorf("expected worker_id w-1, got %s", msg.Status.WorkerID)
	}
	if msg.Status.State != "running" {
		t.Errorf("expected state running, got %s", msg.Status.State)
	}

	// Verify subprocess was spawned with correct args
	calls := spawner.SpawnCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 spawn call, got %d", len(calls))
	}
	if calls[0].Workdir != "/tmp/wt-42" {
		t.Errorf("expected workdir /tmp/wt-42, got %s", calls[0].Workdir)
	}
	if calls[0].Prompt == "" {
		t.Error("expected non-empty prompt")
	}

	cancel()
	<-errCh
}

func TestReceiveShutdown_ExitsCleanly(t *testing.T) {
	t.Parallel()

	spawner := newMockSpawner()
	dispatcherConn, workerConn := net.Pipe()
	defer dispatcherConn.Close()

	w := worker.NewWithConn("w-2", workerConn, spawner)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- w.Run(ctx) }()

	// First assign so we have a running subprocess
	sendMessage(t, dispatcherConn, protocol.Message{
		Type: protocol.MsgAssign,
		Assign: &protocol.AssignPayload{
			BeadID:   "bead-99",
			Worktree: "/tmp/wt-99",
		},
	})
	// Drain the STATUS message
	_ = readMessage(t, dispatcherConn)

	// Send SHUTDOWN
	sendMessage(t, dispatcherConn, protocol.Message{
		Type: protocol.MsgShutdown,
	})

	// Worker should exit
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("expected nil error on shutdown, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not exit after SHUTDOWN")
	}

	// Subprocess should have been killed
	if !spawner.process.Killed() {
		t.Error("expected subprocess to be killed on shutdown")
	}
}

func TestSendHeartbeat_ProducesCorrectJSON(t *testing.T) {
	t.Parallel()

	spawner := newMockSpawner()
	dispatcherConn, workerConn := net.Pipe()
	defer dispatcherConn.Close()

	w := worker.NewWithConn("w-3", workerConn, spawner)

	// Start async reader BEFORE sending (net.Pipe blocks write until read)
	msgCh := readMessageAsync(t, dispatcherConn)

	ctx := context.Background()
	if err := w.SendHeartbeat(ctx, 55); err != nil {
		t.Fatalf("sendHeartbeat: %v", err)
	}

	select {
	case msg := <-msgCh:
		if msg.Type != protocol.MsgHeartbeat {
			t.Fatalf("expected HEARTBEAT, got %s", msg.Type)
		}
		if msg.Heartbeat.WorkerID != "w-3" {
			t.Errorf("expected worker_id w-3, got %s", msg.Heartbeat.WorkerID)
		}
		if msg.Heartbeat.ContextPct != 55 {
			t.Errorf("expected context_pct 55, got %d", msg.Heartbeat.ContextPct)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for heartbeat message")
	}
}

func TestSendDone_ProducesCorrectJSON(t *testing.T) {
	t.Parallel()

	spawner := newMockSpawner()
	dispatcherConn, workerConn := net.Pipe()
	defer dispatcherConn.Close()

	w := worker.NewWithConn("w-4", workerConn, spawner)

	msgCh := readMessageAsync(t, dispatcherConn)

	if err := w.SendDone(context.Background()); err != nil {
		t.Fatalf("sendDone: %v", err)
	}

	select {
	case msg := <-msgCh:
		if msg.Type != protocol.MsgDone {
			t.Fatalf("expected DONE, got %s", msg.Type)
		}
		if msg.Done.WorkerID != "w-4" {
			t.Errorf("expected worker_id w-4, got %s", msg.Done.WorkerID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for done message")
	}
}

func TestSendHandoff_ProducesCorrectJSON(t *testing.T) {
	t.Parallel()

	spawner := newMockSpawner()
	dispatcherConn, workerConn := net.Pipe()
	defer dispatcherConn.Close()

	w := worker.NewWithConn("w-5", workerConn, spawner)

	msgCh := readMessageAsync(t, dispatcherConn)

	if err := w.SendHandoff(context.Background()); err != nil {
		t.Fatalf("sendHandoff: %v", err)
	}

	select {
	case msg := <-msgCh:
		if msg.Type != protocol.MsgHandoff {
			t.Fatalf("expected HANDOFF, got %s", msg.Type)
		}
		if msg.Handoff.WorkerID != "w-5" {
			t.Errorf("expected worker_id w-5, got %s", msg.Handoff.WorkerID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for handoff message")
	}
}

func TestSendReadyForReview_ProducesCorrectJSON(t *testing.T) {
	t.Parallel()

	spawner := newMockSpawner()
	dispatcherConn, workerConn := net.Pipe()
	defer dispatcherConn.Close()

	w := worker.NewWithConn("w-6", workerConn, spawner)

	msgCh := readMessageAsync(t, dispatcherConn)

	if err := w.SendReadyForReview(context.Background()); err != nil {
		t.Fatalf("sendReadyForReview: %v", err)
	}

	select {
	case msg := <-msgCh:
		if msg.Type != protocol.MsgReadyForReview {
			t.Fatalf("expected READY_FOR_REVIEW, got %s", msg.Type)
		}
		if msg.ReadyForReview.WorkerID != "w-6" {
			t.Errorf("expected worker_id w-6, got %s", msg.ReadyForReview.WorkerID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for ready_for_review message")
	}
}

func TestSendStatus_ProducesCorrectJSON(t *testing.T) {
	t.Parallel()

	spawner := newMockSpawner()
	dispatcherConn, workerConn := net.Pipe()
	defer dispatcherConn.Close()

	w := worker.NewWithConn("w-7", workerConn, spawner)

	msgCh := readMessageAsync(t, dispatcherConn)

	if err := w.SendStatus(context.Background(), "paused", "context_limit"); err != nil {
		t.Fatalf("sendStatus: %v", err)
	}

	select {
	case msg := <-msgCh:
		if msg.Type != protocol.MsgStatus {
			t.Fatalf("expected STATUS, got %s", msg.Type)
		}
		if msg.Status.State != "paused" {
			t.Errorf("expected state paused, got %s", msg.Status.State)
		}
		if msg.Status.Result != "context_limit" {
			t.Errorf("expected result context_limit, got %s", msg.Status.Result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for status message")
	}
}

func TestReconnection_BuffersAndResends(t *testing.T) {
	t.Parallel()

	spawner := newMockSpawner()

	// Use /tmp for short socket path (macOS has 104-char limit)
	sockDir, err := os.MkdirTemp("/tmp", "oro-test-")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	defer os.RemoveAll(sockDir)
	sockPath := filepath.Join(sockDir, "w.sock")

	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	// Accept connections in background
	type connResult struct {
		conn net.Conn
		err  error
	}
	acceptCh := make(chan connResult, 5)
	go func() {
		for {
			c, err := listener.Accept()
			acceptCh <- connResult{conn: c, err: err}
			if err != nil {
				return
			}
		}
	}()

	w, err := worker.New("w-recon", sockPath, spawner)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- w.Run(ctx) }()

	// Get first dispatcher connection
	var firstResult connResult
	select {
	case firstResult = <-acceptCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for first connection")
	}
	if firstResult.err != nil {
		t.Fatalf("accept: %v", firstResult.err)
	}
	dispConn1 := firstResult.conn

	// Send ASSIGN on first connection
	sendMessage(t, dispConn1, protocol.Message{
		Type: protocol.MsgAssign,
		Assign: &protocol.AssignPayload{
			BeadID:   "bead-recon",
			Worktree: "/tmp/wt-recon",
		},
	})

	// Drain STATUS message
	_ = readMessage(t, dispConn1)

	// Close the dispatcher side to simulate disconnect
	dispConn1.Close()

	// Worker should reconnect. Accept the new connection.
	var secondResult connResult
	select {
	case secondResult = <-acceptCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for reconnection")
	}
	if secondResult.err != nil {
		t.Fatalf("accept reconnect: %v", secondResult.err)
	}
	dispConn2 := secondResult.conn
	defer dispConn2.Close()

	// Worker should send a RECONNECT message with current state
	msg := readMessage(t, dispConn2)
	if msg.Type != protocol.MsgReconnect {
		t.Fatalf("expected RECONNECT, got %s", msg.Type)
	}
	if msg.Reconnect.WorkerID != "w-recon" {
		t.Errorf("expected worker_id w-recon, got %s", msg.Reconnect.WorkerID)
	}
	if msg.Reconnect.BeadID != "bead-recon" {
		t.Errorf("expected bead_id bead-recon, got %s", msg.Reconnect.BeadID)
	}

	// Subprocess should NOT have been killed during reconnection
	if spawner.process.Killed() {
		t.Error("subprocess should not be killed during reconnection")
	}

	cancel()
	<-errCh
}

func TestContextWatcher_TriggersHandoffAbove70(t *testing.T) {
	t.Parallel()

	spawner := newMockSpawner()
	dispatcherConn, workerConn := net.Pipe()
	defer dispatcherConn.Close()

	w := worker.NewWithConn("w-ctx", workerConn, spawner)
	w.SetContextPollInterval(50 * time.Millisecond) // fast polling for test

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- w.Run(ctx) }()

	// Create a temp worktree dir with .oro/context_pct
	tmpDir := t.TempDir()
	oroDir := filepath.Join(tmpDir, ".oro")
	if err := os.MkdirAll(oroDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Send ASSIGN with the temp worktree
	sendMessage(t, dispatcherConn, protocol.Message{
		Type: protocol.MsgAssign,
		Assign: &protocol.AssignPayload{
			BeadID:   "bead-ctx",
			Worktree: tmpDir,
		},
	})

	// Drain STATUS message
	_ = readMessage(t, dispatcherConn)

	// Write context_pct > 70
	if err := os.WriteFile(filepath.Join(oroDir, "context_pct"), []byte("75"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Worker should detect and send HANDOFF. Read messages until we get one.
	dispatcherConn.SetReadDeadline(time.Now().Add(3 * time.Second))
	scanner := bufio.NewScanner(dispatcherConn)
	gotHandoff := false
	for scanner.Scan() {
		var msg protocol.Message
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if msg.Type == protocol.MsgHandoff {
			gotHandoff = true
			if msg.Handoff.BeadID != "bead-ctx" {
				t.Errorf("expected bead_id bead-ctx, got %s", msg.Handoff.BeadID)
			}
			break
		}
	}
	if !gotHandoff {
		t.Fatal("did not receive HANDOFF message after context_pct > 70")
	}

	// Subprocess should have been killed after handoff (poll briefly for goroutine to complete killProc)
	killed := false
	for range 20 {
		if spawner.process.Killed() {
			killed = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !killed {
		t.Error("expected subprocess to be killed after handoff")
	}

	cancel()
	<-errCh
}

func TestContextWatcher_NoFileIsNotError(t *testing.T) {
	t.Parallel()

	spawner := newMockSpawner()
	dispatcherConn, workerConn := net.Pipe()
	defer dispatcherConn.Close()

	w := worker.NewWithConn("w-nofile", workerConn, spawner)
	w.SetContextPollInterval(50 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- w.Run(ctx) }()

	// Send ASSIGN with a nonexistent worktree (no .oro/context_pct)
	sendMessage(t, dispatcherConn, protocol.Message{
		Type: protocol.MsgAssign,
		Assign: &protocol.AssignPayload{
			BeadID:   "bead-nofile",
			Worktree: "/tmp/nonexistent-worktree-path",
		},
	})

	// Drain STATUS
	_ = readMessage(t, dispatcherConn)

	// Wait a bit to ensure no crash
	time.Sleep(200 * time.Millisecond)

	// Worker should still be running (context cancellation should work)
	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not exit after cancel")
	}
}

func TestMessageBuffer_MaxCapacity(t *testing.T) {
	t.Parallel()

	buf := worker.NewMessageBuffer(3)

	for i := range 5 {
		buf.Add(protocol.Message{
			Type: protocol.MsgHeartbeat,
			Heartbeat: &protocol.HeartbeatPayload{
				BeadID:     fmt.Sprintf("bead-%d", i),
				WorkerID:   "w-buf",
				ContextPct: i * 10,
			},
		})
	}

	msgs := buf.Drain()
	// Should only have last 3 (FIFO eviction of oldest when full)
	if len(msgs) != 3 {
		t.Fatalf("expected 3 buffered messages, got %d", len(msgs))
	}
	// Oldest surviving should be bead-2
	if msgs[0].Heartbeat.BeadID != "bead-2" {
		t.Errorf("expected first buffered bead-2, got %s", msgs[0].Heartbeat.BeadID)
	}

	// After drain, buffer should be empty
	if len(buf.Drain()) != 0 {
		t.Error("expected empty buffer after drain")
	}
}
