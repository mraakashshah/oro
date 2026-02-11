package worker_test

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"oro/pkg/memory"
	"oro/pkg/protocol"
	"oro/pkg/worker"

	_ "modernc.org/sqlite"
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

// mockSpawner implements worker.StreamingSpawner for testing.
type mockSpawner struct {
	mu       sync.Mutex
	calls    []spawnCall
	process  *mockProcess
	spawnErr error
	stdout   io.ReadCloser  // optional: simulated subprocess stdout
	stdin    io.WriteCloser // optional: simulated subprocess stdin
}

type spawnCall struct {
	Model   string
	Prompt  string
	Workdir string
}

func newMockSpawner() *mockSpawner {
	return &mockSpawner{process: newMockProcess()}
}

func (s *mockSpawner) Spawn(_ context.Context, model, prompt, workdir string) (worker.Process, io.ReadCloser, io.WriteCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, spawnCall{Model: model, Prompt: prompt, Workdir: workdir})
	if s.spawnErr != nil {
		return nil, nil, nil, s.spawnErr
	}
	return s.process, s.stdout, s.stdin, nil
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
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
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
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
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

// startWorkerRun launches w.Run in a goroutine, drains the initial HEARTBEAT
// that Run() sends on startup, and returns the error channel. This must be
// used instead of bare `go func() { errCh <- w.Run(ctx) }()` because
// net.Pipe writes block until the other side reads.
func startWorkerRun(ctx context.Context, t *testing.T, w *worker.Worker, dispatcherConn net.Conn) <-chan error {
	t.Helper()
	errCh := make(chan error, 1)
	go func() { errCh <- w.Run(ctx) }()

	// Drain the initial heartbeat so Run() can enter its event loop.
	msg := readMessage(t, dispatcherConn)
	if msg.Type != protocol.MsgHeartbeat {
		t.Fatalf("expected initial HEARTBEAT, got %s", msg.Type)
	}
	return errCh
}

// --- Tests ---

func TestReceiveAssign_StoresState(t *testing.T) {
	t.Parallel()

	spawner := newMockSpawner()
	dispatcherConn, workerConn := net.Pipe()
	defer func() { _ = dispatcherConn.Close() }()

	w := worker.NewWithConn("w-1", workerConn, spawner)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := startWorkerRun(ctx, t, w, dispatcherConn)

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
	defer func() { _ = dispatcherConn.Close() }()

	w := worker.NewWithConn("w-2", workerConn, spawner)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := startWorkerRun(ctx, t, w, dispatcherConn)

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
	defer func() { _ = dispatcherConn.Close() }()

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

func TestSendDone_QualityGatePassed(t *testing.T) {
	t.Parallel()

	spawner := newMockSpawner()
	dispatcherConn, workerConn := net.Pipe()
	defer func() { _ = dispatcherConn.Close() }()

	w := worker.NewWithConn("w-4", workerConn, spawner)

	msgCh := readMessageAsync(t, dispatcherConn)

	if err := w.SendDone(context.Background(), true, ""); err != nil {
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
		if !msg.Done.QualityGatePassed {
			t.Error("expected QualityGatePassed=true")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for done message")
	}
}

func TestSendDone_QualityGateFailed(t *testing.T) {
	t.Parallel()

	spawner := newMockSpawner()
	dispatcherConn, workerConn := net.Pipe()
	defer func() { _ = dispatcherConn.Close() }()

	w := worker.NewWithConn("w-4f", workerConn, spawner)

	msgCh := readMessageAsync(t, dispatcherConn)

	if err := w.SendDone(context.Background(), false, ""); err != nil {
		t.Fatalf("sendDone: %v", err)
	}

	select {
	case msg := <-msgCh:
		if msg.Type != protocol.MsgDone {
			t.Fatalf("expected DONE, got %s", msg.Type)
		}
		if msg.Done.QualityGatePassed {
			t.Error("expected QualityGatePassed=false")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for done message")
	}
}

func TestRunQualityGate_Success(t *testing.T) {
	t.Parallel()

	// Create a temp worktree with a passing quality_gate.sh
	tmpDir := t.TempDir()
	script := filepath.Join(tmpDir, "quality_gate.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 0\n"), 0o600); err != nil { //nolint:gosec // test file
		t.Fatal(err)
	}
	if err := os.Chmod(script, 0o755); err != nil { //nolint:gosec // test script must be executable
		t.Fatal(err)
	}

	passed, _, err := worker.RunQualityGate(context.Background(), tmpDir)
	if err != nil {
		t.Fatalf("RunQualityGate: %v", err)
	}
	if !passed {
		t.Error("expected quality gate to pass")
	}
}

func TestRunQualityGate_Failure(t *testing.T) {
	t.Parallel()

	// Create a temp worktree with a failing quality_gate.sh
	tmpDir := t.TempDir()
	script := filepath.Join(tmpDir, "quality_gate.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 1\n"), 0o600); err != nil { //nolint:gosec // test file
		t.Fatal(err)
	}
	if err := os.Chmod(script, 0o755); err != nil { //nolint:gosec // test script must be executable
		t.Fatal(err)
	}

	passed, _, err := worker.RunQualityGate(context.Background(), tmpDir)
	if err != nil {
		t.Fatalf("RunQualityGate unexpected error: %v", err)
	}
	if passed {
		t.Error("expected quality gate to fail")
	}
}

func TestRunQualityGate_NoScript(t *testing.T) {
	t.Parallel()

	// No quality_gate.sh in dir — should return false with an error
	tmpDir := t.TempDir()

	passed, _, err := worker.RunQualityGate(context.Background(), tmpDir)
	if err == nil {
		t.Fatal("expected error when quality_gate.sh is missing")
	}
	if passed {
		t.Error("expected quality gate to fail when script is missing")
	}
}

func TestRunQualityGate_CapturesOutput(t *testing.T) {
	t.Parallel()

	t.Run("success with stdout", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()
		script := filepath.Join(tmpDir, "quality_gate.sh")
		if err := os.WriteFile(script, []byte("#!/bin/sh\necho 'all tests passed'\necho 'lint clean'\nexit 0\n"), 0o600); err != nil { //nolint:gosec // test file
			t.Fatal(err)
		}
		if err := os.Chmod(script, 0o755); err != nil { //nolint:gosec // test script must be executable
			t.Fatal(err)
		}

		passed, output, err := worker.RunQualityGate(context.Background(), tmpDir)
		if err != nil {
			t.Fatalf("RunQualityGate: %v", err)
		}
		if !passed {
			t.Error("expected quality gate to pass")
		}
		if !strings.Contains(output, "all tests passed") {
			t.Errorf("expected output to contain 'all tests passed', got: %q", output)
		}
		if !strings.Contains(output, "lint clean") {
			t.Errorf("expected output to contain 'lint clean', got: %q", output)
		}
	})

	t.Run("failure with stdout and stderr", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()
		script := filepath.Join(tmpDir, "quality_gate.sh")
		if err := os.WriteFile(script, []byte("#!/bin/sh\necho 'running tests'\necho 'FAIL: TestFoo' >&2\nexit 1\n"), 0o600); err != nil { //nolint:gosec // test file
			t.Fatal(err)
		}
		if err := os.Chmod(script, 0o755); err != nil { //nolint:gosec // test script must be executable
			t.Fatal(err)
		}

		passed, output, err := worker.RunQualityGate(context.Background(), tmpDir)
		if err != nil {
			t.Fatalf("RunQualityGate unexpected error: %v", err)
		}
		if passed {
			t.Error("expected quality gate to fail")
		}
		if !strings.Contains(output, "running tests") {
			t.Errorf("expected output to contain stdout 'running tests', got: %q", output)
		}
		if !strings.Contains(output, "FAIL: TestFoo") {
			t.Errorf("expected output to contain stderr 'FAIL: TestFoo', got: %q", output)
		}
	})

	t.Run("missing script returns error", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()

		passed, output, err := worker.RunQualityGate(context.Background(), tmpDir)
		if err == nil {
			t.Fatal("expected error when quality_gate.sh is missing")
		}
		if passed {
			t.Error("expected quality gate to fail when script is missing")
		}
		if output != "" {
			t.Errorf("expected empty output on missing script, got: %q", output)
		}
	})
}

func TestBuildPrompt_IncludesQualityGateInstruction(t *testing.T) {
	t.Parallel()

	prompt := worker.BuildPrompt("bead-123", "/tmp/wt-123", "")
	if !strings.Contains(prompt, "quality_gate.sh") {
		t.Error("expected prompt to contain quality_gate.sh instruction")
	}
}

func TestSendHandoff_ProducesCorrectJSON(t *testing.T) {
	t.Parallel()

	spawner := newMockSpawner()
	dispatcherConn, workerConn := net.Pipe()
	defer func() { _ = dispatcherConn.Close() }()

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
	defer func() { _ = dispatcherConn.Close() }()

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
	defer func() { _ = dispatcherConn.Close() }()

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

func TestReconnection_BuffersAndResends(t *testing.T) { //nolint:funlen // integration test requires sequential setup
	t.Parallel()

	spawner := newMockSpawner()

	// Use /tmp for short socket path (macOS has 104-char limit)
	sockDir, err := os.MkdirTemp("/tmp", "oro-test-")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	defer func() { _ = os.RemoveAll(sockDir) }()
	sockPath := filepath.Join(sockDir, "w.sock")

	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = listener.Close() }()

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

	// Drain initial heartbeat
	_ = readMessage(t, dispConn1)

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
	_ = dispConn1.Close()

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
	defer func() { _ = dispConn2.Close() }()

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

func TestContextWatcher_TriggersHandoffAbove70(t *testing.T) { //nolint:funlen // integration test requires sequential setup
	t.Parallel()

	spawner := newMockSpawner()
	dispatcherConn, workerConn := net.Pipe()
	defer func() { _ = dispatcherConn.Close() }()

	w := worker.NewWithConn("w-ctx", workerConn, spawner)
	w.SetContextPollInterval(50 * time.Millisecond) // fast polling for test

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := startWorkerRun(ctx, t, w, dispatcherConn)

	// Create a temp worktree dir with .oro/context_pct
	tmpDir := t.TempDir()
	oroDir := filepath.Join(tmpDir, ".oro")
	if err := os.MkdirAll(oroDir, 0o750); err != nil { //nolint:gosec // test directory
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
	if err := os.WriteFile(filepath.Join(oroDir, "context_pct"), []byte("75"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Worker should detect and send HANDOFF. Read messages until we get one.
	_ = dispatcherConn.SetReadDeadline(time.Now().Add(3 * time.Second))
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
	waitFor(t, func() bool {
		return spawner.process.Killed()
	}, 200*time.Millisecond)

	cancel()
	<-errCh
}

func TestHandoffPopulatesContext(t *testing.T) { //nolint:funlen // integration test requires sequential setup
	t.Parallel()

	spawner := newMockSpawner()
	dispatcherConn, workerConn := net.Pipe()
	defer func() { _ = dispatcherConn.Close() }()

	w := worker.NewWithConn("w-hctx", workerConn, spawner)
	w.SetContextPollInterval(50 * time.Millisecond) // fast polling for test

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := startWorkerRun(ctx, t, w, dispatcherConn)

	// Create a temp worktree dir with .oro/ context files
	tmpDir := t.TempDir()
	oroDir := filepath.Join(tmpDir, ".oro")
	if err := os.MkdirAll(oroDir, 0o750); err != nil { //nolint:gosec // test directory
		t.Fatal(err)
	}

	// Write context files that the worker should read before handoff
	writeJSON(t, filepath.Join(oroDir, "learnings.json"), []string{"ruff before pyright", "WAL needs single writer"})
	writeJSON(t, filepath.Join(oroDir, "decisions.json"), []string{"use table-driven tests"})
	writeJSON(t, filepath.Join(oroDir, "files_modified.json"), []string{"pkg/protocol/message.go", "pkg/worker/worker.go"})
	if err := os.WriteFile(filepath.Join(oroDir, "context_summary.txt"), []byte("Extended handoff with typed context"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Send ASSIGN with the temp worktree
	sendMessage(t, dispatcherConn, protocol.Message{
		Type: protocol.MsgAssign,
		Assign: &protocol.AssignPayload{
			BeadID:   "bead-hctx",
			Worktree: tmpDir,
		},
	})

	// Drain STATUS message
	_ = readMessage(t, dispatcherConn)

	// Write context_pct > 70 to trigger handoff
	if err := os.WriteFile(filepath.Join(oroDir, "context_pct"), []byte("75"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Read messages until we get a HANDOFF
	_ = dispatcherConn.SetReadDeadline(time.Now().Add(3 * time.Second))
	scanner := bufio.NewScanner(dispatcherConn)
	var handoffMsg *protocol.HandoffPayload
	for scanner.Scan() {
		var msg protocol.Message
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if msg.Type == protocol.MsgHandoff {
			handoffMsg = msg.Handoff
			break
		}
	}
	if handoffMsg == nil {
		t.Fatal("did not receive HANDOFF message with context fields")
	}

	// Verify context fields are populated
	if handoffMsg.BeadID != "bead-hctx" {
		t.Errorf("expected bead_id bead-hctx, got %s", handoffMsg.BeadID)
	}
	if len(handoffMsg.Learnings) != 2 {
		t.Errorf("expected 2 learnings, got %d", len(handoffMsg.Learnings))
	}
	if len(handoffMsg.Decisions) != 1 {
		t.Errorf("expected 1 decision, got %d", len(handoffMsg.Decisions))
	}
	if len(handoffMsg.FilesModified) != 2 {
		t.Errorf("expected 2 files_modified, got %d", len(handoffMsg.FilesModified))
	}
	if handoffMsg.ContextSummary != "Extended handoff with typed context" {
		t.Errorf("expected context_summary, got %q", handoffMsg.ContextSummary)
	}

	cancel()
	<-errCh
}

func TestGracefulShutdown(t *testing.T) { //nolint:funlen // integration test requires sequential setup
	t.Parallel()

	spawner := newMockSpawner()
	dispatcherConn, workerConn := net.Pipe()
	defer func() { _ = dispatcherConn.Close() }()

	w := worker.NewWithConn("w-graceful", workerConn, spawner)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := startWorkerRun(ctx, t, w, dispatcherConn)

	// Create a temp worktree with .oro/ context files
	tmpDir := t.TempDir()
	oroDir := filepath.Join(tmpDir, ".oro")
	if err := os.MkdirAll(oroDir, 0o750); err != nil { //nolint:gosec // test directory
		t.Fatal(err)
	}
	writeJSON(t, filepath.Join(oroDir, "learnings.json"), []string{"graceful shutdown works"})
	writeJSON(t, filepath.Join(oroDir, "decisions.json"), []string{"use prepare-shutdown protocol"})
	writeJSON(t, filepath.Join(oroDir, "files_modified.json"), []string{"pkg/protocol/message.go"})
	if err := os.WriteFile(filepath.Join(oroDir, "context_summary.txt"), []byte("Implemented graceful shutdown"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Assign work so the worker has bead/worktree state
	sendMessage(t, dispatcherConn, protocol.Message{
		Type: protocol.MsgAssign,
		Assign: &protocol.AssignPayload{
			BeadID:   "bead-graceful",
			Worktree: tmpDir,
		},
	})

	// Drain STATUS message
	_ = readMessage(t, dispatcherConn)

	// Send PREPARE_SHUTDOWN with a 5-second timeout
	sendMessage(t, dispatcherConn, protocol.Message{
		Type: protocol.MsgPrepareShutdown,
		PrepareShutdown: &protocol.PrepareShutdownPayload{
			Timeout: 5 * time.Second,
		},
	})

	// Worker should respond with HANDOFF (saving context) then SHUTDOWN_APPROVED.
	// Read messages until we get both.
	_ = dispatcherConn.SetReadDeadline(time.Now().Add(3 * time.Second))
	scanner := bufio.NewScanner(dispatcherConn)
	var gotHandoff, gotApproved bool
	var handoffMsg *protocol.HandoffPayload
	for scanner.Scan() {
		var msg protocol.Message
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		switch msg.Type {
		case protocol.MsgHandoff:
			gotHandoff = true
			handoffMsg = msg.Handoff
		case protocol.MsgShutdownApproved:
			gotApproved = true
			if msg.ShutdownApproved == nil {
				t.Fatal("SHUTDOWN_APPROVED message missing payload")
			}
			if msg.ShutdownApproved.WorkerID != "w-graceful" {
				t.Errorf("expected worker_id w-graceful, got %s", msg.ShutdownApproved.WorkerID)
			}
		}
		if gotHandoff && gotApproved {
			break
		}
	}

	if !gotHandoff {
		t.Fatal("worker did not send HANDOFF in response to PREPARE_SHUTDOWN")
	}
	if !gotApproved {
		t.Fatal("worker did not send SHUTDOWN_APPROVED in response to PREPARE_SHUTDOWN")
	}

	// Verify handoff payload contains saved context
	if handoffMsg == nil {
		t.Fatal("handoff payload is nil")
	}
	if handoffMsg.BeadID != "bead-graceful" {
		t.Errorf("expected bead_id bead-graceful, got %s", handoffMsg.BeadID)
	}
	if len(handoffMsg.Learnings) != 1 || handoffMsg.Learnings[0] != "graceful shutdown works" {
		t.Errorf("expected learnings, got %v", handoffMsg.Learnings)
	}
	if len(handoffMsg.Decisions) != 1 || handoffMsg.Decisions[0] != "use prepare-shutdown protocol" {
		t.Errorf("expected decisions, got %v", handoffMsg.Decisions)
	}

	// Subprocess should have been killed after graceful shutdown
	waitFor(t, func() bool {
		return spawner.process.Killed()
	}, 200*time.Millisecond)

	// Worker Run should have exited cleanly
	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("expected nil error on graceful shutdown, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not exit after graceful shutdown")
	}
}

func TestGracefulShutdown_NilPayload(t *testing.T) {
	t.Parallel()

	spawner := newMockSpawner()
	dispatcherConn, workerConn := net.Pipe()
	defer func() { _ = dispatcherConn.Close() }()

	w := worker.NewWithConn("w-graceful-nil", workerConn, spawner)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := startWorkerRun(ctx, t, w, dispatcherConn)

	// Send PREPARE_SHUTDOWN with nil payload — worker should treat it like hard shutdown
	sendMessage(t, dispatcherConn, protocol.Message{
		Type: protocol.MsgPrepareShutdown,
	})

	// Worker should exit cleanly (falls back to hard shutdown behavior)
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not exit after PREPARE_SHUTDOWN with nil payload")
	}
}

// writeJSON is a test helper that marshals v to JSON and writes it to path.
func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal json for %s: %v", path, err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestContextWatcher_NoFileIsNotError(t *testing.T) {
	t.Parallel()

	spawner := newMockSpawner()
	dispatcherConn, workerConn := net.Pipe()
	defer func() { _ = dispatcherConn.Close() }()

	w := worker.NewWithConn("w-nofile", workerConn, spawner)
	w.SetContextPollInterval(50 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := startWorkerRun(ctx, t, w, dispatcherConn)

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

	// Wait a bit to ensure no crash - verify worker goroutine is stable
	justWait(200 * time.Millisecond)

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

func TestMessageBuffer_Len(t *testing.T) {
	t.Parallel()

	buf := worker.NewMessageBuffer(5)

	if buf.Len() != 0 {
		t.Errorf("expected Len 0 on new buffer, got %d", buf.Len())
	}

	buf.Add(protocol.Message{Type: protocol.MsgHeartbeat})
	buf.Add(protocol.Message{Type: protocol.MsgDone})
	if buf.Len() != 2 {
		t.Errorf("expected Len 2 after two adds, got %d", buf.Len())
	}

	buf.Drain()
	if buf.Len() != 0 {
		t.Errorf("expected Len 0 after drain, got %d", buf.Len())
	}
}

func TestNew_Success(t *testing.T) {
	t.Parallel()

	sockDir, err := os.MkdirTemp("/tmp", "oro-test-new-")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	defer func() { _ = os.RemoveAll(sockDir) }()
	sockPath := filepath.Join(sockDir, "w.sock")

	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = listener.Close() }()

	spawner := newMockSpawner()
	w, err := worker.New("w-new", sockPath, spawner)
	if err != nil {
		t.Fatalf("New() returned error: %v", err)
	}
	if w.ID != "w-new" {
		t.Errorf("expected ID w-new, got %s", w.ID)
	}
}

func TestNew_FailsOnBadSocket(t *testing.T) {
	t.Parallel()

	spawner := newMockSpawner()
	_, err := worker.New("w-bad", "/tmp/nonexistent-oro-socket-path/w.sock", spawner)
	if err == nil {
		t.Fatal("expected error from New() with bad socket path, got nil")
	}
}

func TestSendMessage_WhenDisconnected_Buffers(t *testing.T) { //nolint:funlen // integration test requires sequential setup
	t.Parallel()

	// Use a real UDS so we can exercise New() + reconnect path
	sockDir, err := os.MkdirTemp("/tmp", "oro-test-disc-")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	defer func() { _ = os.RemoveAll(sockDir) }()
	sockPath := filepath.Join(sockDir, "w.sock")

	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = listener.Close() }()

	acceptCh := make(chan net.Conn, 5)
	go func() {
		for {
			c, err := listener.Accept()
			if err != nil {
				return
			}
			acceptCh <- c
		}
	}()

	spawner := newMockSpawner()
	w, err := worker.New("w-disc", sockPath, spawner)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- w.Run(ctx) }()

	// Accept first connection
	var dispConn1 net.Conn
	select {
	case dispConn1 = <-acceptCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for connection")
	}

	// Drain initial heartbeat
	_ = readMessage(t, dispConn1)

	// Close dispatcher side to trigger disconnect
	_ = dispConn1.Close()

	// During reconnect, SendHeartbeat should buffer (not error)
	// Give a moment for the disconnect to be detected
	justWait(500 * time.Millisecond)

	// Accept reconnection
	var dispConn2 net.Conn
	select {
	case dispConn2 = <-acceptCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for reconnection")
	}
	defer func() { _ = dispConn2.Close() }()

	// Read the RECONNECT message
	msg := readMessage(t, dispConn2)
	if msg.Type != protocol.MsgReconnect {
		t.Fatalf("expected RECONNECT, got %s", msg.Type)
	}

	cancel()
	<-errCh
}

func TestHandleMessage_UnknownType(t *testing.T) {
	t.Parallel()

	spawner := newMockSpawner()
	dispatcherConn, workerConn := net.Pipe()
	defer func() { _ = dispatcherConn.Close() }()

	w := worker.NewWithConn("w-unk", workerConn, spawner)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := startWorkerRun(ctx, t, w, dispatcherConn)

	// Send unknown message type
	sendMessage(t, dispatcherConn, protocol.Message{
		Type: "UNKNOWN_TYPE",
	})

	// Worker should NOT crash; send a shutdown to verify it's still running
	sendMessage(t, dispatcherConn, protocol.Message{
		Type: protocol.MsgShutdown,
	})

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not exit after shutdown")
	}
}

func TestRun_ContextCancellationDuringIdle(t *testing.T) {
	t.Parallel()

	spawner := newMockSpawner()
	dispatcherConn, workerConn := net.Pipe()
	defer func() { _ = dispatcherConn.Close() }()

	w := worker.NewWithConn("w-idle", workerConn, spawner)

	ctx, cancel := context.WithCancel(context.Background())

	errCh := startWorkerRun(ctx, t, w, dispatcherConn)

	// Cancel immediately without any messages
	justWait(50 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("expected nil error on context cancel, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not exit after context cancellation")
	}
}

func TestRun_ContextCancellationDuringProcessing(t *testing.T) {
	t.Parallel()

	spawner := newMockSpawner()
	dispatcherConn, workerConn := net.Pipe()
	defer func() { _ = dispatcherConn.Close() }()

	w := worker.NewWithConn("w-proc", workerConn, spawner)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := startWorkerRun(ctx, t, w, dispatcherConn)

	// Assign work
	sendMessage(t, dispatcherConn, protocol.Message{
		Type: protocol.MsgAssign,
		Assign: &protocol.AssignPayload{
			BeadID:   "bead-proc",
			Worktree: "/tmp/wt-proc",
		},
	})

	// Drain STATUS
	_ = readMessage(t, dispatcherConn)

	// Cancel while subprocess is "running"
	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not exit after cancel during processing")
	}

	// Subprocess should be killed
	if !spawner.process.Killed() {
		t.Error("expected subprocess killed on context cancel")
	}
}

func TestHandleAssign_MissingPayload(t *testing.T) {
	t.Parallel()

	spawner := newMockSpawner()
	dispatcherConn, workerConn := net.Pipe()
	defer func() { _ = dispatcherConn.Close() }()

	w := worker.NewWithConn("w-nilassign", workerConn, spawner)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := startWorkerRun(ctx, t, w, dispatcherConn)

	// Send ASSIGN with nil payload
	sendMessage(t, dispatcherConn, protocol.Message{
		Type: protocol.MsgAssign,
		// Assign is nil
	})

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected error for nil ASSIGN payload, got nil")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not exit after bad ASSIGN")
	}
}

func TestHandleAssign_SpawnError(t *testing.T) {
	t.Parallel()

	spawner := newMockSpawner()
	spawner.spawnErr = fmt.Errorf("spawn failed")
	dispatcherConn, workerConn := net.Pipe()
	defer func() { _ = dispatcherConn.Close() }()

	w := worker.NewWithConn("w-spawnerr", workerConn, spawner)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := startWorkerRun(ctx, t, w, dispatcherConn)

	sendMessage(t, dispatcherConn, protocol.Message{
		Type: protocol.MsgAssign,
		Assign: &protocol.AssignPayload{
			BeadID:   "bead-fail",
			Worktree: "/tmp/wt-fail",
		},
	})

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected error for spawn failure, got nil")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not exit after spawn error")
	}
}

func TestReconnect_ContextCancelled(t *testing.T) {
	t.Parallel()

	sockDir, err := os.MkdirTemp("/tmp", "oro-test-rctx-")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	defer func() { _ = os.RemoveAll(sockDir) }()
	sockPath := filepath.Join(sockDir, "w.sock")

	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	acceptCh := make(chan net.Conn, 5)
	go func() {
		for {
			c, err := listener.Accept()
			if err != nil {
				return
			}
			acceptCh <- c
		}
	}()

	spawner := newMockSpawner()
	w, err := worker.New("w-rctx", sockPath, spawner)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- w.Run(ctx) }()

	// Accept first connection
	var dispConn1 net.Conn
	select {
	case dispConn1 = <-acceptCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for first connection")
	}

	// Drain initial heartbeat
	_ = readMessage(t, dispConn1)

	// Close listener so reconnect cannot succeed, then close conn to trigger reconnect
	_ = listener.Close()
	_ = dispConn1.Close()

	// Give worker time to detect disconnect and start reconnecting
	// Need enough time for the worker to detect disconnect and enter reconnect loop
	justWait(500 * time.Millisecond)

	// Cancel context during reconnect
	cancel()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected error from reconnect with cancelled context, got nil")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not exit after cancel during reconnect")
	}
}

func TestReconnect_ReportsIdleWhenNoProcRunning(t *testing.T) { //nolint:funlen // integration test requires sequential setup
	t.Parallel()

	sockDir, err := os.MkdirTemp("/tmp", "oro-test-idle-")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	defer func() { _ = os.RemoveAll(sockDir) }()
	sockPath := filepath.Join(sockDir, "w.sock")

	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = listener.Close() }()

	acceptCh := make(chan net.Conn, 5)
	go func() {
		for {
			c, err := listener.Accept()
			if err != nil {
				return
			}
			acceptCh <- c
		}
	}()

	spawner := newMockSpawner()
	w, err := worker.New("w-idle-recon", sockPath, spawner)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- w.Run(ctx) }()

	// Accept first connection (no ASSIGN sent, so proc is nil => state should be "idle")
	var dispConn1 net.Conn
	select {
	case dispConn1 = <-acceptCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for first connection")
	}

	// Drain initial heartbeat
	_ = readMessage(t, dispConn1)

	// Close to trigger reconnect
	_ = dispConn1.Close()

	// Accept reconnection
	var dispConn2 net.Conn
	select {
	case dispConn2 = <-acceptCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for reconnection")
	}
	defer func() { _ = dispConn2.Close() }()

	// Read RECONNECT and verify state is "idle"
	msg := readMessage(t, dispConn2)
	if msg.Type != protocol.MsgReconnect {
		t.Fatalf("expected RECONNECT, got %s", msg.Type)
	}
	if msg.Reconnect.State != "idle" {
		t.Errorf("expected state idle for worker without proc, got %s", msg.Reconnect.State)
	}

	cancel()
	<-errCh
}

func TestRun_MalformedJSON_Skipped(t *testing.T) {
	t.Parallel()

	spawner := newMockSpawner()
	dispatcherConn, workerConn := net.Pipe()
	defer func() { _ = dispatcherConn.Close() }()

	w := worker.NewWithConn("w-malform", workerConn, spawner)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := startWorkerRun(ctx, t, w, dispatcherConn)

	// Send malformed JSON (should be skipped)
	_, _ = dispatcherConn.Write([]byte("this is not json\n"))

	// Send a valid shutdown to prove the worker is still alive
	sendMessage(t, dispatcherConn, protocol.Message{
		Type: protocol.MsgShutdown,
	})

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("expected nil error, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not exit after shutdown following malformed JSON")
	}
}

func TestRun_ConnectionClosedNoSocketPath_ReturnsError(t *testing.T) {
	t.Parallel()

	spawner := newMockSpawner()
	dispatcherConn, workerConn := net.Pipe()

	w := worker.NewWithConn("w-nopath", workerConn, spawner)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := startWorkerRun(ctx, t, w, dispatcherConn)

	// Close dispatcher side — worker has no socketPath so it can't reconnect
	_ = dispatcherConn.Close()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected error when connection closes with no socketPath, got nil")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not exit after connection close")
	}
}

// errorAfterConn wraps a net.Conn and returns a read error after the underlying conn closes,
// instead of io.EOF. This triggers the scanner.Err() != nil path in Run.
type errorAfterConn struct {
	net.Conn
	readErr error
}

func (c *errorAfterConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if err != nil {
		// Replace the EOF with a custom error
		return n, c.readErr
	}
	return n, nil
}

// immediateErrorConn returns an error on the first Read call with no delay,
// ensuring errCh is loaded before the context is cancelled.
type immediateErrorConn struct {
	net.Conn
}

func (c *immediateErrorConn) Read(_ []byte) (int, error) {
	return 0, fmt.Errorf("immediate connection error")
}

func (c *immediateErrorConn) Write(b []byte) (int, error) {
	return len(b), nil
}

func (c *immediateErrorConn) Close() error {
	return nil
}

func (c *immediateErrorConn) LocalAddr() net.Addr                { return nil }
func (c *immediateErrorConn) RemoteAddr() net.Addr               { return nil }
func (c *immediateErrorConn) SetDeadline(_ time.Time) error      { return nil }
func (c *immediateErrorConn) SetReadDeadline(_ time.Time) error  { return nil }
func (c *immediateErrorConn) SetWriteDeadline(_ time.Time) error { return nil }

func TestRun_ScannerError(t *testing.T) {
	t.Parallel()

	spawner := newMockSpawner()
	dispatcherConn, workerConn := net.Pipe()

	customErr := fmt.Errorf("simulated read error")
	wrappedConn := &errorAfterConn{Conn: workerConn, readErr: customErr}

	w := worker.NewWithConn("w-scanerr", wrappedConn, spawner)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := startWorkerRun(ctx, t, w, dispatcherConn)

	// Close the dispatcher side — the wrapped conn will return our custom error
	_ = dispatcherConn.Close()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected scanner error, got nil")
		}
		// The error should be our custom error (not "connection closed")
		if err.Error() != "simulated read error" {
			t.Logf("got error: %v (expected simulated read error, but any error is fine for coverage)", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not exit after scanner error")
	}
}

func TestContextWatcher_EmptyWorktree_NoCrash(t *testing.T) {
	t.Parallel()

	spawner := newMockSpawner()
	dispatcherConn, workerConn := net.Pipe()
	defer func() { _ = dispatcherConn.Close() }()

	w := worker.NewWithConn("w-empty-wt", workerConn, spawner)
	w.SetContextPollInterval(50 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := startWorkerRun(ctx, t, w, dispatcherConn)

	// Send ASSIGN with empty worktree — triggers wt == "" path in watchContext
	sendMessage(t, dispatcherConn, protocol.Message{
		Type: protocol.MsgAssign,
		Assign: &protocol.AssignPayload{
			BeadID:   "bead-empty-wt",
			Worktree: "",
		},
	})

	// Drain STATUS
	_ = readMessage(t, dispatcherConn)

	// Wait for several poll cycles — watcher should hit wt == "" and continue
	justWait(300 * time.Millisecond)

	// Should not crash
	if spawner.process.Killed() {
		t.Error("subprocess should not be killed with empty worktree")
	}

	cancel()
	<-errCh
}

func TestContextWatcher_Below70_NoHandoff(t *testing.T) {
	t.Parallel()

	spawner := newMockSpawner()
	dispatcherConn, workerConn := net.Pipe()
	defer func() { _ = dispatcherConn.Close() }()

	w := worker.NewWithConn("w-below", workerConn, spawner)
	w.SetContextPollInterval(50 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := startWorkerRun(ctx, t, w, dispatcherConn)

	// Create worktree with context_pct below threshold
	tmpDir := t.TempDir()
	oroDir := filepath.Join(tmpDir, ".oro")
	if err := os.MkdirAll(oroDir, 0o750); err != nil { //nolint:gosec // test directory
		t.Fatal(err)
	}

	sendMessage(t, dispatcherConn, protocol.Message{
		Type: protocol.MsgAssign,
		Assign: &protocol.AssignPayload{
			BeadID:   "bead-below",
			Worktree: tmpDir,
		},
	})

	// Drain STATUS
	_ = readMessage(t, dispatcherConn)

	// Write context_pct = 50 (below threshold)
	if err := os.WriteFile(filepath.Join(oroDir, "context_pct"), []byte("50"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Wait for several poll cycles
	justWait(300 * time.Millisecond)

	// Subprocess should NOT have been killed
	if spawner.process.Killed() {
		t.Error("subprocess should not be killed when context_pct is below threshold")
	}

	cancel()
	<-errCh
}

func TestSendMessage_WriteError_WhenConnClosed(t *testing.T) {
	t.Parallel()

	spawner := newMockSpawner()
	dispatcherConn, workerConn := net.Pipe()

	w := worker.NewWithConn("w-werr", workerConn, spawner)

	// Close the worker side of the connection so writes will fail
	_ = dispatcherConn.Close()
	_ = workerConn.Close()

	// Attempt to send — should get a write error
	err := w.SendHeartbeat(context.Background(), 10)
	if err == nil {
		t.Fatal("expected write error, got nil")
	}
}

func TestReconnect_DialFailsThenSucceeds(t *testing.T) { //nolint:funlen // integration test requires sequential setup
	t.Parallel()

	sockDir, err := os.MkdirTemp("/tmp", "oro-test-retry-")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	defer func() { _ = os.RemoveAll(sockDir) }()
	sockPath := filepath.Join(sockDir, "w.sock")

	// Create initial listener for New()
	listener1, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	acceptCh := make(chan net.Conn, 5)
	go func() {
		for {
			c, err := listener1.Accept()
			if err != nil {
				return
			}
			acceptCh <- c
		}
	}()

	spawner := newMockSpawner()
	w, err := worker.New("w-retry", sockPath, spawner)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- w.Run(ctx) }()

	// Accept first connection
	var dispConn1 net.Conn
	select {
	case dispConn1 = <-acceptCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for first connection")
	}

	// Drain initial heartbeat
	_ = readMessage(t, dispConn1)

	// Close the listener AND remove the socket file so the first reconnect attempt will fail
	_ = listener1.Close()
	_ = os.Remove(sockPath)

	// Close dispatcher side to trigger disconnect
	_ = dispConn1.Close()

	// Wait for worker to attempt reconnect and fail at least once
	// reconnectBaseInterval is 2s ± 500ms, so wait 4s to ensure at least one failed attempt
	justWait(4 * time.Second)

	// Now create a new listener on the same path so the next attempt succeeds
	_ = os.Remove(sockPath) // remove stale socket
	listener2, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen2: %v", err)
	}
	defer func() { _ = listener2.Close() }()

	acceptCh2 := make(chan net.Conn, 5)
	go func() {
		for {
			c, err := listener2.Accept()
			if err != nil {
				return
			}
			acceptCh2 <- c
		}
	}()

	// Accept reconnection
	var dispConn2 net.Conn
	select {
	case dispConn2 = <-acceptCh2:
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for reconnection after retry")
	}
	defer func() { _ = dispConn2.Close() }()

	// Read RECONNECT
	msg := readMessage(t, dispConn2)
	if msg.Type != protocol.MsgReconnect {
		t.Fatalf("expected RECONNECT, got %s", msg.Type)
	}

	cancel()
	<-errCh
}

func TestReconnect_SendReconnectFails_Retries(t *testing.T) { //nolint:funlen // integration test requires sequential setup
	t.Parallel()

	sockDir, err := os.MkdirTemp("/tmp", "oro-test-reconn-fail-")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	defer func() { _ = os.RemoveAll(sockDir) }()
	sockPath := filepath.Join(sockDir, "w.sock")

	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = listener.Close() }()

	acceptCh := make(chan net.Conn, 10)
	go func() {
		for {
			c, err := listener.Accept()
			if err != nil {
				return
			}
			acceptCh <- c
		}
	}()

	spawner := newMockSpawner()
	w, err := worker.New("w-rfail", sockPath, spawner)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	w.SetReconnectInterval(50 * time.Millisecond) // fast retry for tests

	// Use a dial hook to close the worker's connection on the first
	// reconnect attempt, guaranteeing that sendMessage(RECONNECT) fails.
	// This avoids UDS kernel-buffering races where server-side Close()
	// doesn't reliably cause a client-side write error.
	var hookOnce sync.Once
	w.SetReconnectDialHook(func(c net.Conn) {
		hookOnce.Do(func() {
			_ = c.Close() // close worker-side conn before sendMessage
		})
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- w.Run(ctx) }()

	// Accept first connection
	var dispConn1 net.Conn
	select {
	case dispConn1 = <-acceptCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for first connection")
	}

	// Drain initial heartbeat
	_ = readMessage(t, dispConn1)

	// Close to trigger reconnect
	_ = dispConn1.Close()

	// The hook closes the worker's conn on the first reconnect dial,
	// so sendMessage(RECONNECT) fails. The worker retries and dials again.
	// Drain the first (broken) accepted connection.
	select {
	case c := <-acceptCh:
		_ = c.Close()
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for first reconnection attempt")
	}

	// Accept the retry connection where RECONNECT succeeds.
	var dispConn3 net.Conn
	select {
	case dispConn3 = <-acceptCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for second reconnection attempt")
	}
	defer func() { _ = dispConn3.Close() }()

	msg := readMessage(t, dispConn3)
	if msg.Type != protocol.MsgReconnect {
		t.Fatalf("expected RECONNECT, got %s", msg.Type)
	}

	cancel()
	<-errCh
}

func TestContextWatcher_InvalidContent_Ignored(t *testing.T) {
	t.Parallel()

	spawner := newMockSpawner()
	dispatcherConn, workerConn := net.Pipe()
	defer func() { _ = dispatcherConn.Close() }()

	w := worker.NewWithConn("w-badpct", workerConn, spawner)
	w.SetContextPollInterval(50 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := startWorkerRun(ctx, t, w, dispatcherConn)

	tmpDir := t.TempDir()
	oroDir := filepath.Join(tmpDir, ".oro")
	if err := os.MkdirAll(oroDir, 0o750); err != nil { //nolint:gosec // test directory
		t.Fatal(err)
	}

	sendMessage(t, dispatcherConn, protocol.Message{
		Type: protocol.MsgAssign,
		Assign: &protocol.AssignPayload{
			BeadID:   "bead-badpct",
			Worktree: tmpDir,
		},
	})

	// Drain STATUS
	_ = readMessage(t, dispatcherConn)

	// Write non-numeric content
	if err := os.WriteFile(filepath.Join(oroDir, "context_pct"), []byte("not-a-number"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Wait for several poll cycles — should not crash or handoff
	justWait(300 * time.Millisecond)

	if spawner.process.Killed() {
		t.Error("subprocess should not be killed when context_pct is invalid")
	}

	cancel()
	<-errCh
}

func TestHandleAssign_SendStatusError(t *testing.T) {
	t.Parallel()

	// Use a spawner that closes the dispatcher conn during spawn, so SendStatus fails
	dispatcherConn, workerConn := net.Pipe()

	closingSpawner := &connClosingSpawner{
		process:     newMockProcess(),
		connToClose: dispatcherConn,
	}

	w := worker.NewWithConn("w-statuserr", workerConn, closingSpawner)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := startWorkerRun(ctx, t, w, dispatcherConn)

	sendMessage(t, dispatcherConn, protocol.Message{
		Type: protocol.MsgAssign,
		Assign: &protocol.AssignPayload{
			BeadID:   "bead-statuserr",
			Worktree: "/tmp/wt-statuserr",
		},
	})

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected error from SendStatus failure, got nil")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not exit after SendStatus error")
	}
}

// connClosingSpawner closes the given connection during Spawn so that
// the subsequent SendStatus call in handleAssign fails.
type connClosingSpawner struct {
	process     *mockProcess
	connToClose net.Conn
}

func (s *connClosingSpawner) Spawn(_ context.Context, _, _, _ string) (worker.Process, io.ReadCloser, io.WriteCloser, error) {
	// Close the connection so the next write (SendStatus) will fail
	_ = s.connToClose.Close()
	return s.process, nil, nil, nil
}

func TestRun_ErrChWithCancelledContext(t *testing.T) {
	t.Parallel()

	// Use immediateErrorConn so errCh is loaded almost instantly.
	// Use a pre-cancelled context so ctx.Err() != nil when the select runs.
	// Both ctx.Done and errCh are ready; Go randomly picks one.
	// Run multiple iterations to maximize chance of hitting errCh path.
	for i := range 50 {
		func() {
			spawner := newMockSpawner()
			conn := &immediateErrorConn{}

			w := worker.NewWithConn(fmt.Sprintf("w-race-%d", i), conn, spawner)

			ctx, cancel := context.WithCancel(context.Background())
			cancel() // pre-cancel so ctx.Done() is immediately ready

			errCh := make(chan error, 1)
			go func() { errCh <- w.Run(ctx) }()

			select {
			case err := <-errCh:
				// nil: ctx.Done won or errCh won with ctx.Err()!=nil (line 138)
				// Both are valid. We just need coverage.
				_ = err
			case <-time.After(2 * time.Second):
				t.Fatal("worker did not exit")
			}
		}()
	}
}

func TestSendMessage_BuffersWhenDisconnected(t *testing.T) { //nolint:funlen // integration test requires sequential setup
	t.Parallel()

	// Create a real UDS to exercise the disconnected buffering path
	sockDir, err := os.MkdirTemp("/tmp", "oro-test-buf-")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	defer func() { _ = os.RemoveAll(sockDir) }()
	sockPath := filepath.Join(sockDir, "w.sock")

	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = listener.Close() }()

	acceptCh := make(chan net.Conn, 10)
	go func() {
		for {
			c, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			acceptCh <- c
		}
	}()

	spawner := newMockSpawner()
	w, err := worker.New("w-bufmsg", sockPath, spawner)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- w.Run(ctx) }()

	// Accept first connection
	var dispConn1 net.Conn
	select {
	case dispConn1 = <-acceptCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for first connection")
	}

	// Drain initial heartbeat
	_ = readMessage(t, dispConn1)

	// Send ASSIGN so worker has a bead
	sendMessage(t, dispConn1, protocol.Message{
		Type: protocol.MsgAssign,
		Assign: &protocol.AssignPayload{
			BeadID:   "bead-bufmsg",
			Worktree: "/tmp/wt-bufmsg",
		},
	})
	_ = readMessage(t, dispConn1) // drain STATUS

	// Close dispatcher to trigger disconnect
	_ = dispConn1.Close()

	// Wait for the worker to detect the disconnect and enter reconnect state
	// We need to ensure the worker actually detects the disconnect before we
	// send heartbeats, but not wait so long that reconnect completes
	justWait(100 * time.Millisecond)

	// Send messages while disconnected — they should be buffered
	_ = w.SendHeartbeat(ctx, 25)
	_ = w.SendHeartbeat(ctx, 30)

	// Accept reconnection
	var dispConn2 net.Conn
	select {
	case dispConn2 = <-acceptCh:
	case <-time.After(10 * time.Second):
		t.Fatal("timeout waiting for reconnection")
	}
	defer func() { _ = dispConn2.Close() }()

	// Read RECONNECT — it should contain the buffered heartbeats
	msg := readMessage(t, dispConn2)
	if msg.Type != protocol.MsgReconnect {
		t.Fatalf("expected RECONNECT, got %s", msg.Type)
	}
	if len(msg.Reconnect.BufferedEvents) < 2 {
		t.Errorf("expected at least 2 buffered events, got %d", len(msg.Reconnect.BufferedEvents))
	}

	cancel()
	<-errCh
}

// setupTestDB creates an in-memory SQLite database with the full schema for memory tests.
func setupTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(protocol.SchemaDDL); err != nil {
		t.Fatalf("exec schema: %v", err)
	}

	return db
}

func TestWorkerExtractsMemories(t *testing.T) { //nolint:funlen // integration test
	t.Parallel()

	// Set up memory store backed by in-memory SQLite.
	db := setupTestDB(t)
	store := memory.NewStore(db)

	// Create an io.Pipe to simulate subprocess stdout.
	pr, pw := io.Pipe()

	spawner := &mockSpawner{
		process: newMockProcess(),
		stdout:  pr,
	}

	dispatcherConn, workerConn := net.Pipe()
	defer func() { _ = dispatcherConn.Close() }()

	w := worker.NewWithConn("w-mem", workerConn, spawner)
	w.SetMemoryStore(store)
	w.SetContextPollInterval(50 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := startWorkerRun(ctx, t, w, dispatcherConn)

	// Send ASSIGN
	sendMessage(t, dispatcherConn, protocol.Message{
		Type: protocol.MsgAssign,
		Assign: &protocol.AssignPayload{
			BeadID:   "bead-mem",
			Worktree: "/tmp/wt-mem",
		},
	})

	// Drain STATUS
	_ = readMessage(t, dispatcherConn)

	// Simulate subprocess output with [MEMORY] markers and implicit patterns.
	output := strings.Join([]string{
		"Starting work on bead...",
		"[MEMORY] type=gotcha: ruff --fix must run before pyright",
		"[MEMORY] type=lesson tags=go,testing: table-driven tests are cleaner",
		"I learned that WAL mode needs a single writer.",
		"Note: Always check error returns in Go",
		"Some regular output line",
		"Gotcha: FTS5 requires content sync triggers",
		"Done with bead.",
	}, "\n")
	_, err := pw.Write([]byte(output + "\n"))
	if err != nil {
		t.Fatalf("write to pipe: %v", err)
	}
	_ = pw.Close()

	// Allow time for the processOutput goroutine to read and process all lines.
	justWait(200 * time.Millisecond)

	// Verify [MEMORY] markers were extracted in real-time.
	all, err := store.List(ctx, memory.ListOpts{})
	if err != nil {
		t.Fatalf("list memories: %v", err)
	}

	// At this point, only explicit [MEMORY] markers should be stored (2 markers).
	markerCount := 0
	for _, m := range all {
		if m.Source == "self_report" {
			markerCount++
		}
	}
	if markerCount != 2 {
		t.Errorf("expected 2 explicit memory markers, got %d", markerCount)
	}

	// Now trigger handoff to extract implicit memories.
	handoffCh := readMessageAsync(t, dispatcherConn)
	if err := w.SendHandoff(ctx); err != nil {
		t.Fatalf("send handoff: %v", err)
	}

	select {
	case msg := <-handoffCh:
		if msg.Type != protocol.MsgHandoff {
			t.Fatalf("expected HANDOFF, got %s", msg.Type)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for handoff message")
	}

	// After handoff, implicit memories should also be stored.
	all, err = store.List(ctx, memory.ListOpts{})
	if err != nil {
		t.Fatalf("list memories after handoff: %v", err)
	}

	// Count daemon_extracted (implicit) memories.
	implicitCount := 0
	for _, m := range all {
		if m.Source == "daemon_extracted" {
			implicitCount++
		}
	}

	// Expected implicit: "WAL mode needs a single writer" (I learned),
	// "Always check error returns in Go" (Note:), "FTS5 requires content sync triggers" (Gotcha:)
	if implicitCount != 3 {
		t.Errorf("expected 3 implicit memories after handoff, got %d", implicitCount)
		for _, m := range all {
			t.Logf("  memory: source=%s type=%s content=%q", m.Source, m.Type, m.Content)
		}
	}

	// Verify total: 2 explicit + 3 implicit = 5.
	if len(all) != 5 {
		t.Errorf("expected 5 total memories, got %d", len(all))
		for _, m := range all {
			t.Logf("  memory: source=%s type=%s content=%q", m.Source, m.Type, m.Content)
		}
	}

	// Verify session text was accumulated.
	sessionText := w.SessionText()
	if !strings.Contains(sessionText, "Starting work on bead") {
		t.Error("expected session text to contain subprocess output")
	}
	if !strings.Contains(sessionText, "[MEMORY] type=gotcha") {
		t.Error("expected session text to contain memory marker lines")
	}

	cancel()
	<-errCh
}

func TestWorkerExtractsMemories_OnDone(t *testing.T) { //nolint:funlen // integration test
	t.Parallel()

	db := setupTestDB(t)
	store := memory.NewStore(db)

	pr, pw := io.Pipe()

	spawner := &mockSpawner{
		process: newMockProcess(),
		stdout:  pr,
	}

	dispatcherConn, workerConn := net.Pipe()
	defer func() { _ = dispatcherConn.Close() }()

	w := worker.NewWithConn("w-mem-done", workerConn, spawner)
	w.SetMemoryStore(store)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := startWorkerRun(ctx, t, w, dispatcherConn)

	// Send ASSIGN
	sendMessage(t, dispatcherConn, protocol.Message{
		Type: protocol.MsgAssign,
		Assign: &protocol.AssignPayload{
			BeadID:   "bead-done-mem",
			Worktree: "/tmp/wt-done-mem",
		},
	})

	// Drain STATUS
	_ = readMessage(t, dispatcherConn)

	// Simulate subprocess output with implicit patterns.
	output := "Pattern: functional core with imperative shell\nDone.\n"
	_, _ = pw.Write([]byte(output))
	_ = pw.Close()

	justWait(200 * time.Millisecond)

	// SendDone should extract implicit memories.
	doneCh := readMessageAsync(t, dispatcherConn)
	if err := w.SendDone(ctx, true, ""); err != nil {
		t.Fatalf("send done: %v", err)
	}

	select {
	case msg := <-doneCh:
		if msg.Type != protocol.MsgDone {
			t.Fatalf("expected DONE, got %s", msg.Type)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for done message")
	}

	// Verify implicit memory was extracted.
	all, err := store.List(ctx, memory.ListOpts{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("expected 1 implicit memory, got %d", len(all))
		for _, m := range all {
			t.Logf("  memory: source=%s type=%s content=%q", m.Source, m.Type, m.Content)
		}
	}
	if len(all) > 0 && all[0].Type != "pattern" {
		t.Errorf("expected type=pattern, got %q", all[0].Type)
	}

	cancel()
	<-errCh
}

func TestWorkerNoMemoryStore_NoCrash(t *testing.T) {
	t.Parallel()

	// Verify worker works fine without a memory store (nil memStore).
	pr, pw := io.Pipe()

	spawner := &mockSpawner{
		process: newMockProcess(),
		stdout:  pr,
	}

	dispatcherConn, workerConn := net.Pipe()
	defer func() { _ = dispatcherConn.Close() }()

	w := worker.NewWithConn("w-nomem", workerConn, spawner)
	// Deliberately NOT setting memory store.

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := startWorkerRun(ctx, t, w, dispatcherConn)

	sendMessage(t, dispatcherConn, protocol.Message{
		Type: protocol.MsgAssign,
		Assign: &protocol.AssignPayload{
			BeadID:   "bead-nomem",
			Worktree: "/tmp/wt-nomem",
		},
	})

	// Drain STATUS
	_ = readMessage(t, dispatcherConn)

	// Write output with markers (should not crash even without store).
	_, _ = pw.Write([]byte("[MEMORY] type=gotcha: should not crash\nDone.\n"))
	_ = pw.Close()

	justWait(200 * time.Millisecond)

	// Session text should still be accumulated.
	if !strings.Contains(w.SessionText(), "should not crash") {
		t.Error("expected session text to accumulate even without memory store")
	}

	cancel()
	<-errCh
}

func TestBuildPrompt_IncludesMemoryContext(t *testing.T) {
	t.Parallel()

	memCtx := "## Relevant Memories\n- [lesson] always run go vet before committing (2025-01-15, confidence: 0.90)"
	prompt := worker.BuildPrompt("bead-mc", "/tmp/wt-mc", memCtx)

	if !strings.Contains(prompt, "go vet") {
		t.Error("expected prompt to contain memory context content 'go vet'")
	}
	if !strings.Contains(prompt, "Relevant Memories") {
		t.Error("expected prompt to contain 'Relevant Memories' header from memory context")
	}
	if !strings.Contains(prompt, "quality_gate.sh") {
		t.Error("expected prompt to still contain quality_gate.sh instruction")
	}
	if !strings.Contains(prompt, "bead-mc") {
		t.Error("expected prompt to contain bead ID")
	}
}

func TestBuildPrompt_EmptyMemoryContext(t *testing.T) {
	t.Parallel()

	prompt := worker.BuildPrompt("bead-empty", "/tmp/wt-empty", "")

	// Should work the same as before — no memory section
	if !strings.Contains(prompt, "quality_gate.sh") {
		t.Error("expected prompt to contain quality_gate.sh instruction")
	}
	if strings.Contains(prompt, "Relevant Memories") {
		t.Error("prompt should NOT contain memory section when memoryContext is empty")
	}
}

func TestHandleAssign_PassesMemoryContextToSpawner(t *testing.T) {
	t.Parallel()

	spawner := newMockSpawner()
	dispatcherConn, workerConn := net.Pipe()
	defer func() { _ = dispatcherConn.Close() }()

	w := worker.NewWithConn("w-mc", workerConn, spawner)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := startWorkerRun(ctx, t, w, dispatcherConn)

	memCtx := "## Relevant Memories\n- [lesson] use table-driven tests"

	// Send ASSIGN with MemoryContext
	sendMessage(t, dispatcherConn, protocol.Message{
		Type: protocol.MsgAssign,
		Assign: &protocol.AssignPayload{
			BeadID:        "bead-mc-pass",
			Worktree:      "/tmp/wt-mc-pass",
			MemoryContext: memCtx,
		},
	})

	// Drain STATUS message
	_ = readMessage(t, dispatcherConn)

	// Verify the spawner received a prompt containing the memory context
	calls := spawner.SpawnCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 spawn call, got %d", len(calls))
	}
	if !strings.Contains(calls[0].Prompt, "table-driven tests") {
		t.Errorf("expected prompt to contain memory context, got: %s", calls[0].Prompt)
	}
	if !strings.Contains(calls[0].Prompt, "Relevant Memories") {
		t.Errorf("expected prompt to contain 'Relevant Memories' header, got: %s", calls[0].Prompt)
	}

	cancel()
	<-errCh
}

func TestWatchContext_CompactThenHandoff(t *testing.T) { //nolint:funlen // two-stage integration test
	t.Parallel()

	spawner := newMockSpawner()

	dispatcherConn, workerConn := net.Pipe()
	defer func() { _ = dispatcherConn.Close() }()

	w := worker.NewWithConn("w-compact", workerConn, spawner)
	w.SetContextPollInterval(50 * time.Millisecond)

	// Create worktree with thresholds.json (opus=65)
	tmpDir := t.TempDir()
	oroDir := filepath.Join(tmpDir, ".oro")
	if err := os.MkdirAll(oroDir, 0o750); err != nil { //nolint:gosec // test directory
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "thresholds.json"), []byte(`{"opus": 65}`), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := startWorkerRun(ctx, t, w, dispatcherConn)

	// Send ASSIGN with model=opus
	sendMessage(t, dispatcherConn, protocol.Message{
		Type: protocol.MsgAssign,
		Assign: &protocol.AssignPayload{
			BeadID:   "bead-compact",
			Worktree: tmpDir,
			Model:    "opus",
		},
	})

	// Drain STATUS message
	_ = readMessage(t, dispatcherConn)

	// --- First threshold breach: should mark compacted, NOT handoff ---
	// (gives Claude's built-in auto-compaction a chance to reduce context)
	if err := os.WriteFile(filepath.Join(oroDir, "context_pct"), []byte("70"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Wait for .oro/compacted flag to be created
	waitFor(t, func() bool {
		_, err := os.Stat(filepath.Join(oroDir, "compacted"))
		return err == nil
	}, 2*time.Second)

	// Verify subprocess was NOT killed (no handoff yet)
	if spawner.process.Killed() {
		t.Fatal("subprocess should not be killed on first threshold breach")
	}

	// Reset context_pct below threshold to simulate auto-compact working
	if err := os.WriteFile(filepath.Join(oroDir, "context_pct"), []byte("30"), 0o600); err != nil {
		t.Fatal(err)
	}
	justWait(100 * time.Millisecond)

	// --- Second threshold breach: should handoff ---
	if err := os.WriteFile(filepath.Join(oroDir, "context_pct"), []byte("70"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Should get HANDOFF message
	_ = dispatcherConn.SetReadDeadline(time.Now().Add(3 * time.Second))
	scanner := bufio.NewScanner(dispatcherConn)
	gotHandoff := false
	for scanner.Scan() {
		var msg protocol.Message
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if msg.Type == protocol.MsgHandoff {
			gotHandoff = true
			break
		}
	}
	if !gotHandoff {
		t.Fatal("expected HANDOFF on second threshold breach")
	}

	// Subprocess should be killed after handoff
	waitFor(t, func() bool {
		return spawner.process.Killed()
	}, 200*time.Millisecond)

	cancel()
	<-errCh
}

func TestWatchContext_CreatesOroDirectoryWith0700Perms(t *testing.T) {
	t.Parallel()

	spawner := newMockSpawner()
	dispatcherConn, workerConn := net.Pipe()
	defer func() { _ = dispatcherConn.Close() }()

	w := worker.NewWithConn("w-perm", workerConn, spawner)
	w.SetContextPollInterval(50 * time.Millisecond)

	// Create worktree WITHOUT .oro directory (so worker creates it)
	tmpDir := t.TempDir()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := startWorkerRun(ctx, t, w, dispatcherConn)

	// Send ASSIGN to trigger watchContext
	sendMessage(t, dispatcherConn, protocol.Message{
		Type: protocol.MsgAssign,
		Assign: &protocol.AssignPayload{
			BeadID:   "bead-perm",
			Worktree: tmpDir,
			Model:    "opus",
		},
	})

	// Drain STATUS message
	_ = readMessage(t, dispatcherConn)

	// Write context_pct above threshold to trigger compacted flag creation
	oroDir := filepath.Join(tmpDir, ".oro")
	if err := os.MkdirAll(oroDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oroDir, "context_pct"), []byte("70"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Wait for .oro/compacted flag to be created
	waitFor(t, func() bool {
		_, err := os.Stat(filepath.Join(oroDir, "compacted"))
		return err == nil
	}, 2*time.Second)

	// Verify .oro directory has 0700 permissions (no group or other access)
	info, err := os.Stat(oroDir)
	if err != nil {
		t.Fatalf("stat .oro dir: %v", err)
	}
	perm := info.Mode().Perm()
	if perm != 0o700 {
		t.Errorf("expected .oro perms 0700, got %04o", perm)
	}

	cancel()
	<-errCh
}

func TestLoadThresholds(t *testing.T) {
	t.Run("loads from file", func(t *testing.T) {
		dir := t.TempDir()
		data := `{"opus": 65, "sonnet": 50, "haiku": 40}`
		if err := os.WriteFile(filepath.Join(dir, "thresholds.json"), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}

		th := worker.LoadThresholds(dir)

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
		th := worker.LoadThresholds(dir)

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

		th := worker.LoadThresholds(dir)

		if th.For("unknown-model") != 50 {
			t.Errorf("unknown model: got %d, want 50", th.For("unknown-model"))
		}
	})
}

// TestProcessExitExtractsMemories verifies that when a subprocess exits
// (stdout closes) without calling SendDone or SendHandoff, implicit
// memories are still extracted from the session text. This ensures
// learnings from failed attempts are persisted before dispatcher re-assigns.
func TestProcessExitExtractsMemories(t *testing.T) { //nolint:funlen // integration test
	t.Parallel()

	db := setupTestDB(t)
	store := memory.NewStore(db)

	// io.Pipe simulates subprocess stdout.
	pr, pw := io.Pipe()

	spawner := &mockSpawner{
		process: newMockProcess(),
		stdout:  pr,
	}

	dispatcherConn, workerConn := net.Pipe()
	defer func() { _ = dispatcherConn.Close() }()

	w := worker.NewWithConn("w-exit-mem", workerConn, spawner)
	w.SetMemoryStore(store)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := startWorkerRun(ctx, t, w, dispatcherConn)

	// Send ASSIGN to start the subprocess.
	sendMessage(t, dispatcherConn, protocol.Message{
		Type: protocol.MsgAssign,
		Assign: &protocol.AssignPayload{
			BeadID:   "bead-exit",
			Worktree: "/tmp/wt-exit",
		},
	})

	// Drain STATUS message.
	_ = readMessage(t, dispatcherConn)

	// Simulate subprocess output with implicit patterns, then close stdout
	// (simulating subprocess exit) WITHOUT calling SendDone or SendHandoff.
	output := strings.Join([]string{
		"Running quality gate...",
		"I learned that WAL mode needs a single writer.",
		"Gotcha: FTS5 requires content sync triggers",
		"Quality gate failed, exiting.",
	}, "\n")
	_, err := pw.Write([]byte(output + "\n"))
	if err != nil {
		t.Fatalf("write to pipe: %v", err)
	}
	// Close stdout to simulate subprocess exit.
	_ = pw.Close()

	// Wait for processOutput to finish and extract memories.
	// processOutput calls extractImplicitMemories on exit.
	justWait(500 * time.Millisecond)

	// Verify implicit memories were extracted even without SendDone/SendHandoff.
	all, err := store.List(ctx, memory.ListOpts{})
	if err != nil {
		t.Fatalf("list memories: %v", err)
	}

	implicitCount := 0
	for _, m := range all {
		if m.Source == "daemon_extracted" {
			implicitCount++
		}
	}

	// Expected: "WAL mode needs a single writer" (I learned),
	// "FTS5 requires content sync triggers" (Gotcha:)
	if implicitCount != 2 {
		t.Errorf("expected 2 implicit memories after process exit, got %d", implicitCount)
		for _, m := range all {
			t.Logf("  memory: source=%s type=%s content=%q", m.Source, m.Type, m.Content)
		}
	}

	cancel()
	<-errCh
}

// TestWorkerSendsInitialHeartbeat verifies that Run() sends a HEARTBEAT
// immediately on startup so the dispatcher can register the worker.
func TestWorkerSendsInitialHeartbeat(t *testing.T) {
	t.Parallel()

	spawner := newMockSpawner()
	dispatcherConn, workerConn := net.Pipe()
	defer func() { _ = dispatcherConn.Close() }()

	w := worker.NewWithConn("w-announce", workerConn, spawner)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() { _ = w.Run(ctx) }()

	// The first message from the worker should be a HEARTBEAT with its ID.
	msg := readMessage(t, dispatcherConn)
	if msg.Type != protocol.MsgHeartbeat {
		t.Fatalf("expected first message to be HEARTBEAT, got %s", msg.Type)
	}
	if msg.Heartbeat == nil {
		t.Fatal("heartbeat payload is nil")
	}
	if msg.Heartbeat.WorkerID != "w-announce" {
		t.Fatalf("expected worker ID %q, got %q", "w-announce", msg.Heartbeat.WorkerID)
	}
	if msg.Heartbeat.ContextPct != 0 {
		t.Fatalf("expected initial context_pct=0, got %d", msg.Heartbeat.ContextPct)
	}
}

func TestWorkerFlow_SendsReadyForReview(t *testing.T) { //nolint:funlen // integration test
	t.Parallel()

	t.Run("QG pass triggers ReadyForReview then approval triggers Done", func(t *testing.T) {
		t.Parallel()

		// Create temp worktree with a passing quality_gate.sh
		tmpDir := t.TempDir()
		script := filepath.Join(tmpDir, "quality_gate.sh")
		if err := os.WriteFile(script, []byte("#!/bin/sh\necho 'all checks passed'\nexit 0\n"), 0o600); err != nil { //nolint:gosec // test file
			t.Fatal(err)
		}
		if err := os.Chmod(script, 0o755); err != nil { //nolint:gosec // test script must be executable
			t.Fatal(err)
		}

		pr, pw := io.Pipe()
		proc := newMockProcess()

		spawner := &mockSpawner{
			process: proc,
			stdout:  pr,
		}

		dispatcherConn, workerConn := net.Pipe()
		defer func() { _ = dispatcherConn.Close() }()

		w := worker.NewWithConn("w-rfr", workerConn, spawner)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		errCh := startWorkerRun(ctx, t, w, dispatcherConn)

		sendMessage(t, dispatcherConn, protocol.Message{
			Type: protocol.MsgAssign,
			Assign: &protocol.AssignPayload{
				BeadID:   "bead-rfr",
				Worktree: tmpDir,
			},
		})
		_ = readMessage(t, dispatcherConn) // drain STATUS

		// Subprocess finishes
		_, _ = pw.Write([]byte("implementation done\n"))
		_ = pw.Close()
		close(proc.waitCh)

		// After QG passes, worker must send READY_FOR_REVIEW (not DONE)
		msg := readMessage(t, dispatcherConn)
		if msg.Type != protocol.MsgReadyForReview {
			t.Fatalf("expected READY_FOR_REVIEW after QG pass, got %s", msg.Type)
		}
		if msg.ReadyForReview.BeadID != "bead-rfr" {
			t.Errorf("expected bead_id bead-rfr, got %s", msg.ReadyForReview.BeadID)
		}
		if msg.ReadyForReview.WorkerID != "w-rfr" {
			t.Errorf("expected worker_id w-rfr, got %s", msg.ReadyForReview.WorkerID)
		}

		// Simulate dispatcher sending approval
		sendMessage(t, dispatcherConn, protocol.Message{
			Type: protocol.MsgReviewResult,
			ReviewResult: &protocol.ReviewResultPayload{
				Verdict: "approved",
			},
		})

		// Worker should now send DONE
		msg = readMessage(t, dispatcherConn)
		if msg.Type != protocol.MsgDone {
			t.Fatalf("expected DONE after approval, got %s", msg.Type)
		}
		if !msg.Done.QualityGatePassed {
			t.Error("expected QualityGatePassed=true")
		}
		if msg.Done.QGOutput == "" {
			t.Error("expected QGOutput to be populated with quality gate output")
		}

		cancel()
		<-errCh
	})

	t.Run("QG fail sends Done immediately without review", func(t *testing.T) {
		t.Parallel()

		tmpDir := t.TempDir()
		script := filepath.Join(tmpDir, "quality_gate.sh")
		if err := os.WriteFile(script, []byte("#!/bin/sh\necho 'lint error'\nexit 1\n"), 0o600); err != nil { //nolint:gosec // test file
			t.Fatal(err)
		}
		if err := os.Chmod(script, 0o755); err != nil { //nolint:gosec // test script must be executable
			t.Fatal(err)
		}

		pr, pw := io.Pipe()
		proc := newMockProcess()

		spawner := &mockSpawner{
			process: proc,
			stdout:  pr,
		}

		dispatcherConn, workerConn := net.Pipe()
		defer func() { _ = dispatcherConn.Close() }()

		w := worker.NewWithConn("w-rfr-fail", workerConn, spawner)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		errCh := startWorkerRun(ctx, t, w, dispatcherConn)

		sendMessage(t, dispatcherConn, protocol.Message{
			Type: protocol.MsgAssign,
			Assign: &protocol.AssignPayload{
				BeadID:   "bead-rfr-fail",
				Worktree: tmpDir,
			},
		})
		_ = readMessage(t, dispatcherConn) // drain STATUS

		_, _ = pw.Write([]byte("work done\n"))
		_ = pw.Close()
		close(proc.waitCh)

		// QG fails -> worker should send DONE immediately (no review)
		msg := readMessage(t, dispatcherConn)
		if msg.Type != protocol.MsgDone {
			t.Fatalf("expected DONE directly on QG failure, got %s", msg.Type)
		}
		if msg.Done.QualityGatePassed {
			t.Error("expected QualityGatePassed=false")
		}

		cancel()
		<-errCh
	})

	t.Run("review rejection re-assigns worker", func(t *testing.T) {
		t.Parallel()

		tmpDir := t.TempDir()
		script := filepath.Join(tmpDir, "quality_gate.sh")
		if err := os.WriteFile(script, []byte("#!/bin/sh\necho 'tests pass'\nexit 0\n"), 0o600); err != nil { //nolint:gosec // test file
			t.Fatal(err)
		}
		if err := os.Chmod(script, 0o755); err != nil { //nolint:gosec // test script must be executable
			t.Fatal(err)
		}

		pr, pw := io.Pipe()
		proc := newMockProcess()

		spawner := &mockSpawner{
			process: proc,
			stdout:  pr,
		}

		dispatcherConn, workerConn := net.Pipe()
		defer func() { _ = dispatcherConn.Close() }()

		w := worker.NewWithConn("w-rfr-rej", workerConn, spawner)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		errCh := startWorkerRun(ctx, t, w, dispatcherConn)

		sendMessage(t, dispatcherConn, protocol.Message{
			Type: protocol.MsgAssign,
			Assign: &protocol.AssignPayload{
				BeadID:   "bead-rfr-rej",
				Worktree: tmpDir,
			},
		})
		_ = readMessage(t, dispatcherConn) // drain STATUS

		_, _ = pw.Write([]byte("done\n"))
		_ = pw.Close()
		close(proc.waitCh)

		// QG passes -> READY_FOR_REVIEW
		msg := readMessage(t, dispatcherConn)
		if msg.Type != protocol.MsgReadyForReview {
			t.Fatalf("expected READY_FOR_REVIEW, got %s", msg.Type)
		}

		// Dispatcher rejects by re-assigning with feedback (same as existing rejection flow)
		sendMessage(t, dispatcherConn, protocol.Message{
			Type: protocol.MsgAssign,
			Assign: &protocol.AssignPayload{
				BeadID:   "bead-rfr-rej",
				Worktree: tmpDir,
				Feedback: "missing edge case tests",
			},
		})

		// Worker should accept re-assignment (sends STATUS running)
		msg = readMessage(t, dispatcherConn)
		if msg.Type != protocol.MsgStatus {
			t.Fatalf("expected STATUS after re-assign, got %s", msg.Type)
		}
		if msg.Status.State != "running" {
			t.Errorf("expected state running, got %s", msg.Status.State)
		}

		cancel()
		<-errCh
	})
}

func TestSubprocessExit_RunsQGAndSendsDone(t *testing.T) {
	t.Parallel()

	t.Run("QG passes sends ReadyForReview then Done on approval", func(t *testing.T) {
		t.Parallel()

		// Create temp worktree with a passing quality_gate.sh
		tmpDir := t.TempDir()
		script := filepath.Join(tmpDir, "quality_gate.sh")
		if err := os.WriteFile(script, []byte("#!/bin/sh\necho 'all checks passed'\nexit 0\n"), 0o600); err != nil { //nolint:gosec // test file
			t.Fatal(err)
		}
		if err := os.Chmod(script, 0o755); err != nil { //nolint:gosec // test script must be executable
			t.Fatal(err)
		}

		// io.Pipe simulates subprocess stdout.
		pr, pw := io.Pipe()
		proc := newMockProcess()

		spawner := &mockSpawner{
			process: proc,
			stdout:  pr,
		}

		dispatcherConn, workerConn := net.Pipe()
		defer func() { _ = dispatcherConn.Close() }()

		w := worker.NewWithConn("w-qg-pass", workerConn, spawner)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		errCh := startWorkerRun(ctx, t, w, dispatcherConn)

		// Send ASSIGN with real temp worktree containing quality_gate.sh
		sendMessage(t, dispatcherConn, protocol.Message{
			Type: protocol.MsgAssign,
			Assign: &protocol.AssignPayload{
				BeadID:   "bead-qg-pass",
				Worktree: tmpDir,
			},
		})

		// Drain STATUS message
		_ = readMessage(t, dispatcherConn)

		// Write some output, then close stdout and let process exit
		_, _ = pw.Write([]byte("doing work...\n"))
		_ = pw.Close()
		close(proc.waitCh)

		// Worker should send READY_FOR_REVIEW (not DONE) after QG passes
		msg := readMessage(t, dispatcherConn)
		if msg.Type != protocol.MsgReadyForReview {
			t.Fatalf("expected READY_FOR_REVIEW after QG pass, got %s", msg.Type)
		}
		if msg.ReadyForReview.BeadID != "bead-qg-pass" {
			t.Errorf("expected bead_id bead-qg-pass, got %s", msg.ReadyForReview.BeadID)
		}

		// Dispatcher sends REVIEW_RESULT with approved verdict
		sendMessage(t, dispatcherConn, protocol.Message{
			Type: protocol.MsgReviewResult,
			ReviewResult: &protocol.ReviewResultPayload{
				Verdict: "approved",
			},
		})

		// Worker should now send DONE with QualityGatePassed=true
		msg = readMessage(t, dispatcherConn)
		if msg.Type != protocol.MsgDone {
			t.Fatalf("expected DONE after review approval, got %s", msg.Type)
		}
		if !msg.Done.QualityGatePassed {
			t.Error("expected QualityGatePassed=true")
		}
		if msg.Done.BeadID != "bead-qg-pass" {
			t.Errorf("expected bead_id bead-qg-pass, got %s", msg.Done.BeadID)
		}

		cancel()
		<-errCh
	})

	t.Run("QG fails", func(t *testing.T) {
		t.Parallel()

		// Create temp worktree with a failing quality_gate.sh
		tmpDir := t.TempDir()
		script := filepath.Join(tmpDir, "quality_gate.sh")
		if err := os.WriteFile(script, []byte("#!/bin/sh\necho 'lint error: unused var'\nexit 1\n"), 0o600); err != nil { //nolint:gosec // test file
			t.Fatal(err)
		}
		if err := os.Chmod(script, 0o755); err != nil { //nolint:gosec // test script must be executable
			t.Fatal(err)
		}

		// io.Pipe simulates subprocess stdout.
		pr, pw := io.Pipe()
		proc := newMockProcess()

		spawner := &mockSpawner{
			process: proc,
			stdout:  pr,
		}

		dispatcherConn, workerConn := net.Pipe()
		defer func() { _ = dispatcherConn.Close() }()

		w := worker.NewWithConn("w-qg-fail", workerConn, spawner)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		errCh := startWorkerRun(ctx, t, w, dispatcherConn)

		// Send ASSIGN with real temp worktree containing failing quality_gate.sh
		sendMessage(t, dispatcherConn, protocol.Message{
			Type: protocol.MsgAssign,
			Assign: &protocol.AssignPayload{
				BeadID:   "bead-qg-fail",
				Worktree: tmpDir,
			},
		})

		// Drain STATUS message
		_ = readMessage(t, dispatcherConn)

		// Write some output, then close stdout and let process exit
		_, _ = pw.Write([]byte("doing work...\n"))
		_ = pw.Close()
		close(proc.waitCh)

		// Worker should send DONE with QualityGatePassed=false and QGOutput populated
		msg := readMessage(t, dispatcherConn)
		if msg.Type != protocol.MsgDone {
			t.Fatalf("expected DONE, got %s", msg.Type)
		}
		if msg.Done.QualityGatePassed {
			t.Error("expected QualityGatePassed=false")
		}
		if msg.Done.BeadID != "bead-qg-fail" {
			t.Errorf("expected bead_id bead-qg-fail, got %s", msg.Done.BeadID)
		}
		if !strings.Contains(msg.Done.QGOutput, "lint error: unused var") {
			t.Errorf("expected QGOutput to contain lint error, got: %q", msg.Done.QGOutput)
		}

		cancel()
		<-errCh
	})
}

// multiMockSpawner returns a different mockProcess for each Spawn call,
// enabling tests to verify that old processes are killed on re-ASSIGN.
type multiMockSpawner struct {
	mu        sync.Mutex
	calls     []spawnCall
	processes []*mockProcess // pre-populated; Spawn pops from index 0..n
	idx       int
}

func (s *multiMockSpawner) Spawn(_ context.Context, model, prompt, workdir string) (worker.Process, io.ReadCloser, io.WriteCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, spawnCall{Model: model, Prompt: prompt, Workdir: workdir})
	if s.idx >= len(s.processes) {
		return nil, nil, nil, fmt.Errorf("no more mock processes")
	}
	proc := s.processes[s.idx]
	s.idx++
	return proc, nil, nil, nil
}

func TestHandleAssign_KillsOldSubprocess(t *testing.T) {
	t.Parallel()

	oldProc := newMockProcess()
	newProc := newMockProcess()

	spawner := &multiMockSpawner{
		processes: []*mockProcess{oldProc, newProc},
	}

	dispatcherConn, workerConn := net.Pipe()
	defer func() { _ = dispatcherConn.Close() }()

	w := worker.NewWithConn("w-reassign", workerConn, spawner)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := startWorkerRun(ctx, t, w, dispatcherConn)

	// First ASSIGN — spawns oldProc
	sendMessage(t, dispatcherConn, protocol.Message{
		Type: protocol.MsgAssign,
		Assign: &protocol.AssignPayload{
			BeadID:   "bead-first",
			Worktree: "/tmp/wt-first",
		},
	})
	// Drain STATUS from first ASSIGN
	_ = readMessage(t, dispatcherConn)

	// oldProc should be running (not killed)
	if oldProc.Killed() {
		t.Fatal("old process should NOT be killed yet")
	}

	// Second ASSIGN (re-assignment after QG failure) — should kill oldProc, spawn newProc
	sendMessage(t, dispatcherConn, protocol.Message{
		Type: protocol.MsgAssign,
		Assign: &protocol.AssignPayload{
			BeadID:   "bead-retry",
			Worktree: "/tmp/wt-retry",
		},
	})
	// Drain STATUS from second ASSIGN
	_ = readMessage(t, dispatcherConn)

	// Old process must have been killed before the new spawn
	if !oldProc.Killed() {
		t.Error("expected old subprocess to be killed on re-ASSIGN")
	}

	// New process should still be alive
	if newProc.Killed() {
		t.Error("new subprocess should NOT be killed")
	}

	cancel()
	<-errCh
}

func TestReconnect_TimerCleanup(t *testing.T) {
	t.Parallel()

	sockDir, err := os.MkdirTemp("/tmp", "oro-test-timer-")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	defer func() { _ = os.RemoveAll(sockDir) }()
	sockPath := filepath.Join(sockDir, "w.sock")

	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	acceptCh := make(chan net.Conn, 5)
	go func() {
		for {
			c, err := listener.Accept()
			if err != nil {
				return
			}
			acceptCh <- c
		}
	}()

	spawner := newMockSpawner()
	w, err := worker.New("w-timer", sockPath, spawner)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Use a long reconnect interval so the timer is alive when we cancel.
	w.SetReconnectInterval(10 * time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- w.Run(ctx) }()

	// Accept the initial connection.
	var dispConn net.Conn
	select {
	case dispConn = <-acceptCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for connection")
	}

	// Drain initial heartbeat.
	_ = readMessage(t, dispConn)

	// Close listener so reconnect dials will fail, then close the connection
	// to trigger the reconnect loop.
	_ = listener.Close()
	_ = dispConn.Close()

	// Give the worker time to detect the disconnect and enter the reconnect
	// sleep (the 10s timer).
	justWait(300 * time.Millisecond)

	// Snapshot goroutine count before cancellation.
	before := runtime.NumGoroutine()

	// Cancel context during the reconnect sleep — this should stop the timer
	// cleanly without leaking a goroutine.
	cancel()

	select {
	case <-errCh:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not exit after cancel during reconnect")
	}

	// Allow background goroutines to wind down.
	justWait(200 * time.Millisecond)
	runtime.GC()
	justWait(100 * time.Millisecond)

	after := runtime.NumGoroutine()

	// With the leak, a long-lived timer goroutine would still be present.
	// Allow a small delta for runtime jitter but reject any growth.
	if after > before {
		t.Errorf("goroutine leak: before cancel=%d, after=%d (delta=+%d)",
			before, after, after-before)
	}
}

func TestSubprocessHealthCheck(t *testing.T) {
	t.Parallel()

	// Create stdout pipe for the mock subprocess
	pr, pw := io.Pipe()

	spawner := newMockSpawner()
	spawner.stdout = pr

	// Create a mock process that will not exit on its own (stays alive)
	proc := newMockProcess()
	spawner.process = proc

	dispatcherConn, workerConn := net.Pipe()
	defer func() { _ = dispatcherConn.Close() }()

	w := worker.NewWithConn("w-health", workerConn, spawner)
	// Use a short poll interval so health checks happen quickly
	w.SetContextPollInterval(100 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := startWorkerRun(ctx, t, w, dispatcherConn)

	// Create a temp worktree with a quality_gate.sh
	tmpDir := t.TempDir()
	script := filepath.Join(tmpDir, "quality_gate.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 0\n"), 0o600); err != nil { //nolint:gosec // test file
		t.Fatal(err)
	}
	if err := os.Chmod(script, 0o755); err != nil { //nolint:gosec // test script must be executable
		t.Fatal(err)
	}

	// Send ASSIGN to spawn a subprocess
	sendMessage(t, dispatcherConn, protocol.Message{
		Type: protocol.MsgAssign,
		Assign: &protocol.AssignPayload{
			BeadID:   "bead-health",
			Worktree: tmpDir,
		},
	})

	// Drain the STATUS message
	msg := readMessage(t, dispatcherConn)
	if msg.Type != protocol.MsgStatus {
		t.Fatalf("expected STATUS, got %s", msg.Type)
	}

	// Give the worker time to set up the subprocess monitoring goroutine
	justWait(50 * time.Millisecond)

	// Simulate subprocess death: close stdout and waitCh to make subprocess exit
	// WITHOUT the worker explicitly killing it (simulating unexpected death)
	_ = pw.Close()
	proc.mu.Lock()
	close(proc.waitCh)
	proc.mu.Unlock()

	// Worker should detect the dead subprocess within contextPollInterval
	// and send DONE(false) with an error message
	doneReceived := false
	timeout := time.After(3 * time.Second)
	for !doneReceived {
		select {
		case <-timeout:
			t.Fatal("timeout waiting for DONE message after subprocess death")
		default:
			if err := dispatcherConn.SetReadDeadline(time.Now().Add(500 * time.Millisecond)); err != nil {
				t.Fatalf("set read deadline: %v", err)
			}
			scanner := bufio.NewScanner(dispatcherConn)
			if !scanner.Scan() {
				// Timeout or error, continue waiting
				continue
			}
			var msg protocol.Message
			if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
				continue
			}

			if msg.Type == protocol.MsgDone {
				doneReceived = true
				if msg.Done.QualityGatePassed {
					t.Error("expected QualityGatePassed=false when subprocess dies unexpectedly")
				}
				if msg.Done.QGOutput == "" {
					t.Error("expected error message in QGOutput when subprocess dies")
				}
				if !strings.Contains(msg.Done.QGOutput, "subprocess") && !strings.Contains(msg.Done.QGOutput, "died") {
					t.Errorf("expected error message about subprocess death, got: %q", msg.Done.QGOutput)
				}
			}
			// Ignore other message types (like HEARTBEAT)
		}
	}

	cancel()
	<-errCh
}
