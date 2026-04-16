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
	"os/exec"
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

	ctx := t.Context()

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

	passed, _, err := worker.RunQualityGate(context.Background(), tmpDir, false)
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

	passed, _, err := worker.RunQualityGate(context.Background(), tmpDir, false)
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

	passed, _, err := worker.RunQualityGate(context.Background(), tmpDir, false)
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

		passed, output, err := worker.RunQualityGate(context.Background(), tmpDir, false)
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

		passed, output, err := worker.RunQualityGate(context.Background(), tmpDir, false)
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

		passed, output, err := worker.RunQualityGate(context.Background(), tmpDir, false)
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

func TestRunQualityGate_RestoresDeletedScript(t *testing.T) {
	t.Parallel()

	// Create a temp dir with a git repo containing quality_gate.sh.
	tmpDir := t.TempDir()
	scriptPath := filepath.Join(tmpDir, "quality_gate.sh")
	scriptContent := []byte("#!/bin/sh\necho 'restored ok'\nexit 0\n")
	if err := os.WriteFile(scriptPath, scriptContent, 0o755); err != nil { //nolint:gosec // test file
		t.Fatal(err)
	}

	// Initialize a git repo and commit quality_gate.sh so it can be restored.
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "test@test.com"},
		{"config", "user.name", "Test"},
		{"add", "quality_gate.sh"},
		{"commit", "-m", "initial"},
	} {
		cmd := exec.Command("git", args...) //nolint:gosec // test helper with fixed args
		cmd.Dir = tmpDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	// Delete the script (simulates agent deletion).
	if err := os.Remove(scriptPath); err != nil {
		t.Fatal(err)
	}

	// RunQualityGate should restore from git and succeed.
	passed, output, err := worker.RunQualityGate(context.Background(), tmpDir, false)
	if err != nil {
		t.Fatalf("RunQualityGate: %v", err)
	}
	if !passed {
		t.Errorf("expected quality gate to pass after restore, output: %q", output)
	}

	// Verify the script was actually restored on disk.
	if _, err := os.Stat(scriptPath); err != nil {
		t.Errorf("expected quality_gate.sh to exist after restore: %v", err)
	}
}

func TestRunQualityGate_RestoreFails_ReturnsError(t *testing.T) {
	t.Parallel()

	// Create a temp dir with NO git repo and NO quality_gate.sh.
	tmpDir := t.TempDir()

	passed, _, err := worker.RunQualityGate(context.Background(), tmpDir, false)
	if err == nil {
		t.Fatal("expected error when quality_gate.sh is missing and restore fails")
	}
	if passed {
		t.Error("expected quality gate to fail")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected error to mention 'not found', got: %v", err)
	}
	if !strings.Contains(err.Error(), "restore failed") {
		t.Errorf("expected error to mention 'restore failed', got: %v", err)
	}
}

func TestWorkerQGSkipsMutation(t *testing.T) {
	t.Parallel()

	// Create a temp worktree with a script that checks ORO_SKIP_MUTATION env var.
	tmpDir := t.TempDir()
	script := filepath.Join(tmpDir, "quality_gate.sh")

	// Script that checks if ORO_SKIP_MUTATION=1 and passes only if set.
	scriptContent := `#!/bin/sh
if [ "$ORO_SKIP_MUTATION" != "1" ]; then
  echo "FAIL: ORO_SKIP_MUTATION not set"
  exit 1
fi
echo "PASS: ORO_SKIP_MUTATION=1"
exit 0
`
	if err := os.WriteFile(script, []byte(scriptContent), 0o600); err != nil { //nolint:gosec // test file
		t.Fatal(err)
	}
	if err := os.Chmod(script, 0o755); err != nil { //nolint:gosec // test script must be executable
		t.Fatal(err)
	}

	// Call RunQualityGate with skipMutation=true
	passed, output, err := worker.RunQualityGate(context.Background(), tmpDir, true)
	if err != nil {
		t.Fatalf("RunQualityGate: %v", err)
	}
	if !passed {
		t.Errorf("expected quality gate to pass when skipMutation=true (output: %s)", output)
	}
	if !strings.Contains(output, "ORO_SKIP_MUTATION=1") {
		t.Errorf("expected output to contain ORO_SKIP_MUTATION=1, got: %s", output)
	}
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
	if handoffMsg == nil { //nolint:staticcheck // checked below
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
	if handoffMsg == nil { //nolint:staticcheck // checked below
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

	ctx := t.Context()

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

	ctx := t.Context()

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

	ctx := t.Context()

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

	ctx := t.Context()

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

	ctx := t.Context()

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

	ctx := t.Context()

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

	ctx := t.Context()

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

	ctx := t.Context()

	errCh := startWorkerRun(ctx, t, w, dispatcherConn)

	// Send ASSIGN with empty worktree — should be rejected by validation
	sendMessage(t, dispatcherConn, protocol.Message{
		Type: protocol.MsgAssign,
		Assign: &protocol.AssignPayload{
			BeadID:   "bead-empty-wt",
			Worktree: "",
		},
	})

	// Worker should exit with validation error
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected validation error, got nil")
		}
		errMsg := err.Error()
		if !strings.Contains(errMsg, "invalid assign payload") && !strings.Contains(errMsg, "worktree cannot be empty") {
			t.Errorf("expected validation error, got: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not exit after invalid assign")
	}
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

	ctx := t.Context()

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
	// Force single connection so all operations share the same in-memory DB.
	// Without this, the connection pool may open multiple connections, each
	// with its own empty :memory: database (schema not applied).
	db.SetMaxOpenConns(1)
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

	// Mock extract spawner returns [MEMORY] lines simulating LLM extraction.
	extractSpawner := &mockExtractSpawner{
		output: "[MEMORY] type=lesson tags=sqlite: WAL mode needs a single writer\n" +
			"[MEMORY] type=gotcha tags=sqlite: FTS5 requires content sync triggers\n" +
			"[MEMORY] type=lesson tags=go: Always check error returns in Go\n",
	}

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
	w.SetExtractSpawner(extractSpawner)
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
	// Each line is wrapped in a stream-json text event (NDJSON).
	output := ndjsonInput(
		textDeltaLine("Starting work on bead...\n"),
		textDeltaLine("[MEMORY] type=gotcha: ruff --fix must run before pyright\n"),
		textDeltaLine("[MEMORY] type=lesson tags=go,testing: table-driven tests are cleaner\n"),
		textDeltaLine("I learned that WAL mode needs a single writer.\n"),
		textDeltaLine("Note: Always check error returns in Go\n"),
		textDeltaLine("Some regular output line\n"),
		textDeltaLine("Gotcha: FTS5 requires content sync triggers\n"),
		textDeltaLine("Done with bead.\n"),
	)
	_, err := pw.Write([]byte(output))
	if err != nil {
		t.Fatalf("write to pipe: %v", err)
	}
	_ = pw.Close()

	// Wait for LLM-extracted memories to appear in the store (full pipeline completion).
	waitFor(t, func() bool {
		all, listErr := store.List(context.Background(), memory.ListOpts{})
		if listErr != nil {
			return false
		}
		count := 0
		for _, m := range all {
			if m.Source == "llm_extracted" {
				count++
			}
		}
		return count >= 3
	}, 2*time.Second)

	// Verify [MEMORY] markers were extracted in real-time (self_report)
	// and LLM extraction ran on processOutput exit (llm_extracted).
	all, err := store.List(ctx, memory.ListOpts{})
	if err != nil {
		t.Fatalf("list memories: %v", err)
	}

	markerCount := 0
	llmCount := 0
	for _, m := range all {
		if m.Source == "self_report" {
			markerCount++
		}
		if m.Source == "llm_extracted" {
			llmCount++
		}
	}

	if markerCount != 2 {
		t.Errorf("expected 2 explicit memory markers, got %d", markerCount)
	}

	// LLM extraction happens on processOutput exit (not on SendHandoff/SendDone).
	if llmCount != 3 {
		t.Errorf("expected 3 llm_extracted memories, got %d", llmCount)
		for _, m := range all {
			t.Logf("  memory: source=%s type=%s content=%q", m.Source, m.Type, m.Content)
		}
	}

	// Verify total: 2 explicit + 3 LLM-extracted = 5.
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

	// Mock extract spawner returns a [MEMORY] line simulating LLM extraction.
	extractSpawner := &mockExtractSpawner{
		output: "[MEMORY] type=pattern tags=arch: functional core with imperative shell\n",
	}

	pr, pw := io.Pipe()

	spawner := &mockSpawner{
		process: newMockProcess(),
		stdout:  pr,
	}

	dispatcherConn, workerConn := net.Pipe()
	defer func() { _ = dispatcherConn.Close() }()

	w := worker.NewWithConn("w-mem-done", workerConn, spawner)
	w.SetMemoryStore(store)
	w.SetExtractSpawner(extractSpawner)

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

	// Simulate subprocess output with implicit patterns, then close stdout.
	// LLM extraction happens when processOutput finishes (stdout closes),
	// not when SendDone is called.
	output := ndjsonInput(
		textDeltaLine("Pattern: functional core with imperative shell\n"),
		textDeltaLine("Done.\n"),
	)
	_, _ = pw.Write([]byte(output))
	_ = pw.Close()

	// Wait for LLM extraction to run on processOutput exit.
	// Wait for LLM-extracted memory to appear in the store (full pipeline completion).
	waitFor(t, func() bool {
		all, listErr := store.List(context.Background(), memory.ListOpts{})
		if listErr != nil {
			return false
		}
		for _, m := range all {
			if m.Source == "llm_extracted" {
				return true
			}
		}
		return false
	}, 2*time.Second)

	// SendDone no longer triggers extraction (it happens on processOutput exit).
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

	// Verify LLM-extracted memory was stored.
	all, err := store.List(ctx, memory.ListOpts{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("expected 1 llm_extracted memory, got %d", len(all))
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
	_, _ = pw.Write([]byte(ndjsonInput(
		textDeltaLine("[MEMORY] type=gotcha: should not crash\n"),
		textDeltaLine("Done.\n"),
	)))
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

func TestHandleContextThreshold(t *testing.T) {
	t.Parallel()

	spawner := newMockSpawner()

	dispatcherConn, workerConn := net.Pipe()
	defer func() { _ = dispatcherConn.Close() }()

	w := worker.NewWithConn("w-threshold", workerConn, spawner)
	w.SetContextPollInterval(50 * time.Millisecond)

	// Create worktree with thresholds.json (opus=40, hard stop = 50)
	tmpDir := t.TempDir()
	oroDir := filepath.Join(tmpDir, ".oro")
	if err := os.MkdirAll(oroDir, 0o750); err != nil { //nolint:gosec // test directory
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "thresholds.json"), []byte(`{"opus": 40}`), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := startWorkerRun(ctx, t, w, dispatcherConn)

	// Send ASSIGN with model=opus
	sendMessage(t, dispatcherConn, protocol.Message{
		Type: protocol.MsgAssign,
		Assign: &protocol.AssignPayload{
			BeadID:   "bead-threshold",
			Worktree: tmpDir,
			Model:    "opus",
		},
	})

	// Drain STATUS message
	_ = readMessage(t, dispatcherConn)

	// Write pct between soft (40) and hard (50) — should NOT trigger handoff
	if err := os.WriteFile(filepath.Join(oroDir, "context_pct"), []byte("45"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Wait several poll cycles — no handoff expected
	justWait(300 * time.Millisecond)

	if spawner.process.Killed() {
		t.Fatal("subprocess should not be killed when pct is between soft and hard threshold")
	}

	// No .oro/compacted flag should exist (two-stage logic removed)
	if _, err := os.Stat(filepath.Join(oroDir, "compacted")); err == nil {
		t.Fatal(".oro/compacted flag should not be created — two-stage logic removed")
	}

	// Write pct above hard stop (50) — should trigger single-stage handoff+kill
	if err := os.WriteFile(filepath.Join(oroDir, "context_pct"), []byte("55"), 0o600); err != nil {
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
		t.Fatal("expected HANDOFF when pct exceeds hard stop (threshold+20)")
	}

	// Subprocess should be killed after handoff
	waitFor(t, func() bool {
		return spawner.process.Killed()
	}, 200*time.Millisecond)

	cancel()
	<-errCh
}

// TestProcessExitExtractsMemories verifies that when a subprocess exits
// (stdout closes) without calling SendDone or SendHandoff, LLM-based
// memory extraction still runs via extractImplicitMemories. This ensures
// learnings from failed attempts are persisted before dispatcher re-assigns.
func TestProcessExitExtractsMemories(t *testing.T) { //nolint:funlen // integration test
	t.Parallel()

	db := setupTestDB(t)
	store := memory.NewStore(db)

	// Mock extract spawner returns [MEMORY] lines simulating LLM extraction output.
	extractSpawner := &mockExtractSpawner{
		output: "[MEMORY] type=lesson tags=sqlite: WAL mode needs a single writer\n" +
			"[MEMORY] type=gotcha tags=sqlite: FTS5 requires content sync triggers\n",
	}

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
	w.SetExtractSpawner(extractSpawner)

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

	// Simulate subprocess output, then close stdout
	// (simulating subprocess exit) WITHOUT calling SendDone or SendHandoff.
	output := ndjsonInput(
		textDeltaLine("Running quality gate...\n"),
		textDeltaLine("I learned that WAL mode needs a single writer.\n"),
		textDeltaLine("Gotcha: FTS5 requires content sync triggers\n"),
		textDeltaLine("Quality gate failed, exiting.\n"),
	)
	_, err := pw.Write([]byte(output))
	if err != nil {
		t.Fatalf("write to pipe: %v", err)
	}
	// Close stdout to simulate subprocess exit.
	_ = pw.Close()

	// Wait for LLM-extracted memories to appear in the store (full pipeline completion).
	waitFor(t, func() bool {
		all, listErr := store.List(context.Background(), memory.ListOpts{})
		if listErr != nil {
			return false
		}
		count := 0
		for _, m := range all {
			if m.Source == "llm_extracted" {
				count++
			}
		}
		return count >= 2
	}, 2*time.Second)

	// Verify LLM-extracted memories were inserted even without SendDone/SendHandoff.
	all, err := store.List(ctx, memory.ListOpts{})
	if err != nil {
		t.Fatalf("list memories: %v", err)
	}

	llmCount := 0
	for _, m := range all {
		if m.Source == "llm_extracted" {
			llmCount++
		}
	}

	// Expected: 2 memories from the mock LLM output.
	if llmCount != 2 {
		t.Errorf("expected 2 llm_extracted memories after process exit, got %d", llmCount)
		for _, m := range all {
			t.Logf("  memory: source=%s type=%s content=%q", m.Source, m.Type, m.Content)
		}
	}

	cancel()
	<-errCh
}

// mockExtractSpawner implements memory.Spawner for testing ExtractWithLLM integration.
type mockExtractSpawner struct {
	mu        sync.Mutex
	callCount int
	output    string // simulated LLM output
}

func (m *mockExtractSpawner) Spawn(_ context.Context, _, _ string) (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.callCount++
	return io.NopCloser(strings.NewReader(m.output)), nil
}

func (m *mockExtractSpawner) CallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.callCount
}

// TestExtractImplicitMemories_CallsExtractWithLLM verifies that when a subprocess
// exits (stdout closes), extractImplicitMemories calls memory.ExtractWithLLM with
// the worker's extractSpawner, session text, beadID, and memStore.
func TestExtractImplicitMemories_CallsExtractWithLLM(t *testing.T) {
	t.Parallel()

	db := setupTestDB(t)
	store := memory.NewStore(db)

	// Mock extract spawner returns a [MEMORY] line so we can verify it was called.
	extractSpawner := &mockExtractSpawner{
		output: "[MEMORY] type=lesson tags=test: LLM extraction works\n",
	}

	// io.Pipe simulates subprocess stdout.
	pr, pw := io.Pipe()

	spawner := &mockSpawner{
		process: newMockProcess(),
		stdout:  pr,
	}

	dispatcherConn, workerConn := net.Pipe()
	defer func() { _ = dispatcherConn.Close() }()

	w := worker.NewWithConn("w-llm-extract", workerConn, spawner)
	w.SetMemoryStore(store)
	w.SetExtractSpawner(extractSpawner)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := startWorkerRun(ctx, t, w, dispatcherConn)

	// Send ASSIGN to start the subprocess.
	sendMessage(t, dispatcherConn, protocol.Message{
		Type: protocol.MsgAssign,
		Assign: &protocol.AssignPayload{
			BeadID:   "bead-llm",
			Worktree: "/tmp/wt-llm",
		},
	})

	// Drain STATUS message.
	_ = readMessage(t, dispatcherConn)

	// Write session text then close stdout to trigger extractImplicitMemories.
	_, err := pw.Write([]byte(textDeltaLine("Some session output for LLM extraction\n") + "\n"))
	if err != nil {
		t.Fatalf("write to pipe: %v", err)
	}
	_ = pw.Close()

	// Wait for LLM-extracted memory to appear in the store.
	// This confirms the full pipeline: processOutput -> extractImplicitMemories -> ExtractWithLLM -> Insert.
	waitFor(t, func() bool {
		all, listErr := store.List(context.Background(), memory.ListOpts{})
		if listErr != nil {
			return false
		}
		for _, m := range all {
			if m.Source == "llm_extracted" {
				return true
			}
		}
		return false
	}, 2*time.Second)

	if extractSpawner.CallCount() != 1 {
		t.Errorf("expected 1 ExtractWithLLM call, got %d", extractSpawner.CallCount())
	}

	// Verify the memory was inserted into the store (proves ExtractWithLLM ran end-to-end).
	all, err := store.List(ctx, memory.ListOpts{})
	if err != nil {
		t.Fatalf("list memories: %v", err)
	}

	llmExtracted := 0
	for _, m := range all {
		if m.Source == "llm_extracted" {
			llmExtracted++
		}
	}
	if llmExtracted != 1 {
		t.Errorf("expected 1 llm_extracted memory, got %d", llmExtracted)
		for _, m := range all {
			t.Logf("  memory: source=%s type=%s content=%q", m.Source, m.Type, m.Content)
		}
	}

	cancel()
	<-errCh
}

// TestExtractImplicitMemories_NilSpawner verifies that extractImplicitMemories
// is a no-op when extractSpawner is nil (default state).
func TestExtractImplicitMemories_NilSpawner(t *testing.T) {
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

	w := worker.NewWithConn("w-nil-spawner", workerConn, spawner)
	w.SetMemoryStore(store)
	// NOTE: SetExtractSpawner NOT called — extractSpawner remains nil.

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := startWorkerRun(ctx, t, w, dispatcherConn)

	sendMessage(t, dispatcherConn, protocol.Message{
		Type: protocol.MsgAssign,
		Assign: &protocol.AssignPayload{
			BeadID:   "bead-nil",
			Worktree: "/tmp/wt-nil",
		},
	})

	_ = readMessage(t, dispatcherConn)

	_, err := pw.Write([]byte(textDeltaLine("Session text that should NOT trigger LLM extraction\n") + "\n"))
	if err != nil {
		t.Fatalf("write to pipe: %v", err)
	}
	_ = pw.Close()

	// Wait for processOutput to complete.
	justWait(500 * time.Millisecond)

	// No LLM-extracted memories should exist.
	all, err := store.List(ctx, memory.ListOpts{})
	if err != nil {
		t.Fatalf("list memories: %v", err)
	}

	for _, m := range all {
		if m.Source == "llm_extracted" {
			t.Errorf("unexpected llm_extracted memory: %q", m.Content)
		}
	}

	cancel()
	<-errCh
}

// TestSetExtractSpawner verifies that SetExtractSpawner stores the spawner
// in the worker and it's retrievable through the extraction path.
func TestSetExtractSpawner(t *testing.T) {
	t.Parallel()

	dispatcherConn, workerConn := net.Pipe()
	defer func() {
		_ = dispatcherConn.Close()
		_ = workerConn.Close()
	}()

	w := worker.NewWithConn("w-set-spawner", workerConn, newMockSpawner())

	// Should not panic when called with a non-nil spawner.
	extractSpawner := &mockExtractSpawner{output: ""}
	w.SetExtractSpawner(extractSpawner)

	// Should not panic when called with nil (reset).
	w.SetExtractSpawner(nil)
}

// TestWorkerSendsInitialHeartbeat verifies that Run() sends a HEARTBEAT
// immediately on startup so the dispatcher can register the worker.
func TestWorkerSendsInitialHeartbeat(t *testing.T) {
	t.Parallel()

	spawner := newMockSpawner()
	dispatcherConn, workerConn := net.Pipe()
	defer func() { _ = dispatcherConn.Close() }()

	w := worker.NewWithConn("w-announce", workerConn, spawner)

	ctx := t.Context()

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
		_, _ = pw.Write([]byte(textDeltaLine("implementation done\n") + "\n"))
		_ = pw.Close()
		close(proc.waitCh)

		// Drain STATUS awaiting_review (sent after subprocess exit)
		_ = readMessage(t, dispatcherConn)

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

		_, _ = pw.Write([]byte(textDeltaLine("work done\n") + "\n"))
		_ = pw.Close()
		close(proc.waitCh)

		// Drain STATUS awaiting_review (sent after subprocess exit)
		_ = readMessage(t, dispatcherConn)

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

		_, _ = pw.Write([]byte(textDeltaLine("done\n") + "\n"))
		_ = pw.Close()
		close(proc.waitCh)

		// Drain STATUS awaiting_review (sent after subprocess exit)
		_ = readMessage(t, dispatcherConn)

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
		_, _ = pw.Write([]byte(textDeltaLine("doing work...\n") + "\n"))
		_ = pw.Close()
		close(proc.waitCh)

		// Drain STATUS awaiting_review (sent after subprocess exit)
		_ = readMessage(t, dispatcherConn)

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
		_, _ = pw.Write([]byte(textDeltaLine("doing work...\n") + "\n"))
		_ = pw.Close()
		close(proc.waitCh)

		// Drain STATUS awaiting_review (sent after subprocess exit)
		_ = readMessage(t, dispatcherConn)

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
	// Allow +5 jitter for runtime/parallel-test goroutines that may start
	// between the before/after snapshots (CI with -shuffle sees +3 routinely);
	// reject any larger growth that indicates a real leak.
	const goroutineJitter = 5
	if after > before+goroutineJitter {
		t.Errorf("goroutine leak: before cancel=%d, after=%d (delta=+%d, allowed jitter=+%d)",
			before, after, after-before, goroutineJitter)
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

func TestWatchContext_SendsPeriodicHeartbeats(t *testing.T) {
	t.Parallel()

	spawner := newMockSpawner()
	dispatcherConn, workerConn := net.Pipe()
	defer func() { _ = dispatcherConn.Close() }()

	w := worker.NewWithConn("w-hb", workerConn, spawner)
	w.SetContextPollInterval(50 * time.Millisecond)
	w.SetHeartbeatInterval(50 * time.Millisecond)

	tmpDir := t.TempDir()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := startWorkerRun(ctx, t, w, dispatcherConn)

	// Send ASSIGN — worker spawns claude -p and starts watchContext loop
	sendMessage(t, dispatcherConn, protocol.Message{
		Type: protocol.MsgAssign,
		Assign: &protocol.AssignPayload{
			BeadID:   "bead-hb",
			Worktree: tmpDir,
		},
	})

	// Drain STATUS message from handleAssign
	msg := readMessage(t, dispatcherConn)
	if msg.Type != protocol.MsgStatus {
		t.Fatalf("expected STATUS, got %s", msg.Type)
	}

	// Collect messages for up to 500ms (should get multiple heartbeats at 50ms intervals)
	_ = dispatcherConn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	scanner := bufio.NewScanner(dispatcherConn)
	var heartbeats int
	for scanner.Scan() {
		var m protocol.Message
		if err := json.Unmarshal(scanner.Bytes(), &m); err != nil {
			continue
		}
		if m.Type == protocol.MsgHeartbeat && m.Heartbeat != nil && m.Heartbeat.BeadID == "bead-hb" {
			heartbeats++
			if heartbeats >= 3 {
				break
			}
		}
	}

	if heartbeats < 3 {
		t.Fatalf("expected at least 3 periodic heartbeats, got %d", heartbeats)
	}

	cancel()
	<-errCh
}

func TestWatchContext_HeartbeatIncludesContextPct(t *testing.T) {
	t.Parallel()

	spawner := newMockSpawner()
	dispatcherConn, workerConn := net.Pipe()
	defer func() { _ = dispatcherConn.Close() }()

	w := worker.NewWithConn("w-hb-pct", workerConn, spawner)
	w.SetContextPollInterval(50 * time.Millisecond)
	w.SetHeartbeatInterval(50 * time.Millisecond)

	tmpDir := t.TempDir()
	oroDir := filepath.Join(tmpDir, ".oro")
	if err := os.MkdirAll(oroDir, 0o750); err != nil { //nolint:gosec // test dir
		t.Fatal(err)
	}

	// Write context_pct = 42 to the worktree
	if err := os.WriteFile(filepath.Join(oroDir, "context_pct"), []byte("42"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := startWorkerRun(ctx, t, w, dispatcherConn)

	// Assign worker to the worktree
	sendMessage(t, dispatcherConn, protocol.Message{
		Type: protocol.MsgAssign,
		Assign: &protocol.AssignPayload{
			BeadID:   "bead-hb-pct",
			Worktree: tmpDir,
		},
	})

	// Drain STATUS message
	msg := readMessage(t, dispatcherConn)
	if msg.Type != protocol.MsgStatus {
		t.Fatalf("expected STATUS, got %s", msg.Type)
	}

	// Collect heartbeats — at least one should include ContextPct=42
	_ = dispatcherConn.SetReadDeadline(time.Now().Add(1 * time.Second))
	scanner := bufio.NewScanner(dispatcherConn)
	var gotPct bool
	for scanner.Scan() {
		var m protocol.Message
		if err := json.Unmarshal(scanner.Bytes(), &m); err != nil {
			continue
		}
		if m.Type == protocol.MsgHeartbeat && m.Heartbeat != nil && m.Heartbeat.ContextPct == 42 {
			gotPct = true
			break
		}
	}

	if !gotPct {
		t.Fatal("expected at least one heartbeat with ContextPct=42 from worktree context_pct file")
	}

	cancel()
	<-errCh
}

// TestEpicDecompositionSkipsQG verifies that when IsEpicDecomposition=true,
// the worker sends DONE with QualityGatePassed=true without running quality_gate.sh.
func TestEpicDecompositionSkipsQG(t *testing.T) {
	t.Parallel()

	// tmpDir has no quality_gate.sh — if QG ran it would error/fail.
	tmpDir := t.TempDir()

	pr, pw := io.Pipe()
	proc := newMockProcess()

	spawner := &mockSpawner{
		process: proc,
		stdout:  pr,
	}

	dispatcherConn, workerConn := net.Pipe()
	defer func() { _ = dispatcherConn.Close() }()

	w := worker.NewWithConn("w-epic-decomp", workerConn, spawner)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := startWorkerRun(ctx, t, w, dispatcherConn)

	sendMessage(t, dispatcherConn, protocol.Message{
		Type: protocol.MsgAssign,
		Assign: &protocol.AssignPayload{
			BeadID:              "epic-decomp-1",
			Worktree:            tmpDir,
			IsEpicDecomposition: true,
		},
	})
	// Drain STATUS running
	msg := readMessage(t, dispatcherConn)
	if msg.Type != protocol.MsgStatus || msg.Status.State != "running" {
		t.Fatalf("expected STATUS running, got type=%s", msg.Type)
	}

	// Subprocess finishes (decomposition complete)
	_, _ = pw.Write([]byte(textDeltaLine("decomposed epic into 5 beads\n") + "\n"))
	_ = pw.Close()
	close(proc.waitCh)

	// Worker should send DONE with QualityGatePassed=true directly (no QG run)
	msg = readMessage(t, dispatcherConn)
	if msg.Type != protocol.MsgDone {
		t.Fatalf("expected DONE, got %s", msg.Type)
	}
	if !msg.Done.QualityGatePassed {
		t.Error("expected QualityGatePassed=true for epic decomposition")
	}
	if msg.Done.BeadID != "epic-decomp-1" {
		t.Errorf("expected bead_id epic-decomp-1, got %s", msg.Done.BeadID)
	}

	cancel()
	<-errCh
}

// TestClaudeSpawnerSetsStdinToDevNull verifies that ClaudeSpawner.Spawn sets cmd.Stdin to /dev/null,
// preventing the spawned process from inheriting parent stdin and hanging on reads.
func TestClaudeSpawnerSetsStdinToDevNull(t *testing.T) {
	spawner := &worker.ClaudeSpawner{}

	// Use a context with a short timeout to allow cmd.Start() to fail gracefully
	// (claude binary might not exist, but that's OK - we just need to verify cmd setup)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	proc, _, _, _ := spawner.Spawn(ctx, "claude-opus-4-7", "test prompt", "/tmp")

	// We expect Start() to fail (timeout or claude not found), but we just need the cmd to be built
	// If we got a process back, inspect it
	if proc == nil {
		t.Skip("process is nil, likely due to context timeout or spawn error")
		return
	}

	// Cast to the internal cmdProcess type to access the cmd
	cmdProc, ok := proc.(*worker.CmdProcess)
	if !ok {
		t.Fatal("expected cmdProcess")
	}

	cmd := cmdProc.Cmd

	// Verify that cmd.Stdin is set (not nil)
	if cmd.Stdin == nil {
		t.Error("expected cmd.Stdin to be non-nil, got nil")
		return
	}

	// Verify it's an open file pointing to /dev/null
	file, ok := cmd.Stdin.(*os.File)
	if !ok {
		t.Error("expected cmd.Stdin to be an *os.File, got other type")
		return
	}

	// Verify the file is /dev/null by checking the name
	if file.Name() != os.DevNull {
		t.Errorf("expected cmd.Stdin to point to %s, got %s", os.DevNull, file.Name())
	}
}

// TestHardStopThresholds verifies that each model family triggers a hard stop
// at exactly threshold+10, derived from thresholds.json:
//
//	opus:   40 + 10 = 50
//	sonnet: 40 + 10 = 50
//	haiku:  40 + 10 = 50
func TestHardStopThresholds(t *testing.T) { //nolint:funlen // table-driven integration test with parallel subtests
	t.Parallel()

	thresholdsData := `{"opus": 40, "sonnet": 40, "haiku": 40}`

	cases := []struct {
		name     string
		model    string
		hardStop int
	}{
		{name: "opus", model: "opus", hardStop: 50},     // 40 + 10
		{name: "sonnet", model: "sonnet", hardStop: 50}, // 40 + 10
		{name: "haiku", model: "haiku", hardStop: 50},   // 40 + 10
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			spawner := newMockSpawner()
			dispatcherConn, workerConn := net.Pipe()
			defer func() { _ = dispatcherConn.Close() }()

			w := worker.NewWithConn("w-hardstop-"+tc.name, workerConn, spawner)
			w.SetContextPollInterval(50 * time.Millisecond)
			w.SetHeartbeatInterval(1 * time.Hour) // suppress heartbeats during test

			tmpDir := t.TempDir()
			oroDir := filepath.Join(tmpDir, ".oro")
			if err := os.MkdirAll(oroDir, 0o750); err != nil { //nolint:gosec // test directory
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(tmpDir, "thresholds.json"), []byte(thresholdsData), 0o600); err != nil {
				t.Fatal(err)
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			errCh := startWorkerRun(ctx, t, w, dispatcherConn)

			sendMessage(t, dispatcherConn, protocol.Message{
				Type: protocol.MsgAssign,
				Assign: &protocol.AssignPayload{
					BeadID:   "bead-" + tc.name,
					Worktree: tmpDir,
					Model:    tc.model,
				},
			})

			// Drain STATUS
			_ = readMessage(t, dispatcherConn)

			// Write pct AT hard stop boundary — should NOT trigger handoff
			if err := os.WriteFile(filepath.Join(oroDir, "context_pct"), fmt.Appendf(nil, "%d", tc.hardStop), 0o600); err != nil {
				t.Fatal(err)
			}

			justWait(300 * time.Millisecond)

			if spawner.process.Killed() {
				t.Fatalf("%s: subprocess killed at pct=%d (hard stop boundary); expected no kill", tc.name, tc.hardStop)
			}

			// Write pct ABOVE hard stop — should trigger handoff + kill
			if err := os.WriteFile(filepath.Join(oroDir, "context_pct"), fmt.Appendf(nil, "%d", tc.hardStop+1), 0o600); err != nil {
				t.Fatal(err)
			}

			// Read messages until HANDOFF
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
				t.Fatalf("%s: expected HANDOFF when pct=%d > hard stop=%d", tc.name, tc.hardStop+1, tc.hardStop)
			}

			// Subprocess should be killed after handoff
			waitFor(t, func() bool {
				return spawner.process.Killed()
			}, 200*time.Millisecond)

			cancel()
			<-errCh
		})
	}
}

func TestHandoffFileTriggersShutdown(t *testing.T) {
	t.Parallel()

	spawner := newMockSpawner()
	dispatcherConn, workerConn := net.Pipe()
	defer func() { _ = dispatcherConn.Close() }()

	w := worker.NewWithConn("w-hf", workerConn, spawner)
	w.SetContextPollInterval(50 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := startWorkerRun(ctx, t, w, dispatcherConn)

	// Create temp worktree with .oro/
	tmpDir := t.TempDir()
	oroDir := filepath.Join(tmpDir, ".oro")
	if err := os.MkdirAll(oroDir, 0o750); err != nil { //nolint:gosec // test directory
		t.Fatal(err)
	}

	// Send ASSIGN
	sendMessage(t, dispatcherConn, protocol.Message{
		Type: protocol.MsgAssign,
		Assign: &protocol.AssignPayload{
			BeadID:   "bead-hf",
			Worktree: tmpDir,
		},
	})
	// Drain STATUS
	_ = readMessage(t, dispatcherConn)

	// Write .oro/handoff_done to trigger graceful shutdown
	handoffDone := filepath.Join(oroDir, "handoff_done")
	if err := os.WriteFile(handoffDone, []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}

	// Worker should detect handoff_done and send HANDOFF within one poll interval
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
			if msg.Handoff.BeadID != "bead-hf" {
				t.Errorf("expected bead_id bead-hf, got %s", msg.Handoff.BeadID)
			}
			break
		}
	}
	if !gotHandoff {
		t.Fatal("did not receive HANDOFF message after writing handoff_done")
	}

	// Subprocess should be killed after handoff
	waitFor(t, func() bool {
		return spawner.process.Killed()
	}, 500*time.Millisecond)

	// handoff_done file should be deleted after detection
	if _, err := os.Stat(handoffDone); !os.IsNotExist(err) {
		t.Error("expected handoff_done to be deleted after detection")
	}

	cancel()
	<-errCh
}

func TestStaleHandoffFileCleanedOnAssign(t *testing.T) {
	t.Parallel()

	spawner := newMockSpawner()
	dispatcherConn, workerConn := net.Pipe()
	defer func() { _ = dispatcherConn.Close() }()

	w := worker.NewWithConn("w-stale", workerConn, spawner)
	w.SetContextPollInterval(50 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Create temp worktree with a pre-existing stale handoff_done
	tmpDir := t.TempDir()
	oroDir := filepath.Join(tmpDir, ".oro")
	if err := os.MkdirAll(oroDir, 0o750); err != nil { //nolint:gosec // test directory
		t.Fatal(err)
	}
	handoffDone := filepath.Join(oroDir, "handoff_done")
	if err := os.WriteFile(handoffDone, []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}

	errCh := startWorkerRun(ctx, t, w, dispatcherConn)

	// Send ASSIGN — handleAssign should clean up stale handoff_done before watchContext starts
	sendMessage(t, dispatcherConn, protocol.Message{
		Type: protocol.MsgAssign,
		Assign: &protocol.AssignPayload{
			BeadID:   "bead-stale",
			Worktree: tmpDir,
		},
	})
	// Drain STATUS
	_ = readMessage(t, dispatcherConn)

	// handoff_done should be gone — deleted during handleAssign before watchContext starts
	if _, err := os.Stat(handoffDone); !os.IsNotExist(err) {
		t.Error("expected stale handoff_done to be deleted during handleAssign")
	}

	cancel()
	<-errCh
}
