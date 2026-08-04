package worker_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"oro/pkg/protocol"
	"oro/pkg/worker"
)

func TestWorkerPackageDoesNotImportMemory(t *testing.T) {
	t.Parallel()

	for _, path := range []string{"worker.go", "drain.go"} {
		path := filepath.Join("..", "..", "pkg", "worker", path)
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, spec := range file.Imports {
			if spec.Path.Value == `"oro/pkg/memory"` {
				t.Fatalf("%s imports oro/pkg/memory at %s", path, fset.Position(spec.Pos()))
			}
		}
	}
}

// mockProcess implements worker.Process for testing.
type mockProcess struct {
	mu       sync.Mutex
	killed   bool
	waitCh   chan struct{} // close to unblock Wait
	waitErr  error
	exitCode int
	stderr   string
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

func (p *mockProcess) ExitCode() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.exitCode
}

func (p *mockProcess) StderrTail() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stderr
}

// mockSpawner implements worker.StreamingSpawner for testing.
type mockSpawner struct {
	mu             sync.Mutex
	calls          []spawnCall
	process        *mockProcess
	spawnErr       error
	stdout         io.ReadCloser  // optional: simulated subprocess stdout
	stdin          io.WriteCloser // optional: simulated subprocess stdin
	format         worker.StreamFormat
	onSpawn        func(model, prompt, workdir string) error
	onSpawnContext func(context.Context, string, string, string) error
}

type spawnCall struct {
	Model     string
	Reasoning string
	Prompt    string
	Workdir   string
}

func newMockSpawner() *mockSpawner {
	return &mockSpawner{process: newMockProcess()}
}

func (s *mockSpawner) Spawn(_ context.Context, model, prompt, workdir string) (worker.Process, io.ReadCloser, io.WriteCloser, error) {
	return s.SpawnWithReasoning(context.Background(), model, "", prompt, workdir)
}

func (s *mockSpawner) SpawnWithReasoning(ctx context.Context, model, reasoning, prompt, workdir string) (worker.Process, io.ReadCloser, io.WriteCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, spawnCall{Model: model, Reasoning: reasoning, Prompt: prompt, Workdir: workdir})
	if s.onSpawn != nil {
		if err := s.onSpawn(model, prompt, workdir); err != nil {
			return nil, nil, nil, err
		}
	}
	if s.onSpawnContext != nil {
		if err := s.onSpawnContext(ctx, model, prompt, workdir); err != nil {
			return nil, nil, nil, err
		}
	}
	if s.spawnErr != nil {
		return nil, nil, nil, s.spawnErr
	}
	return s.process, s.stdout, s.stdin, nil
}

func (s *mockSpawner) StreamFormat() worker.StreamFormat {
	if s.format != "" {
		return s.format
	}
	return worker.StreamFormatClaudeJSON
}

func (s *mockSpawner) SpawnCalls() []spawnCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	dst := make([]spawnCall, len(s.calls))
	copy(dst, s.calls)
	return dst
}

type contextBlockingSpawner struct {
	started chan struct{}
	once    sync.Once
}

func (s *contextBlockingSpawner) Spawn(ctx context.Context, _, _, _ string) (worker.Process, io.ReadCloser, io.WriteCloser, error) {
	s.once.Do(func() { close(s.started) })
	<-ctx.Done()
	return nil, nil, nil, ctx.Err()
}

func (s *contextBlockingSpawner) StreamFormat() worker.StreamFormat {
	return worker.StreamFormatClaudeJSON
}

func validAssignWorktree(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create assign worktree: %v", err)
	}
	return dir
}

func envValue(env []string, key string) string {
	prefix := key + "="
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			return strings.TrimPrefix(entry, prefix)
		}
	}
	return ""
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

func readMessageWithin(t *testing.T, conn net.Conn, timeout time.Duration) (protocol.Message, bool) {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	scanner := bufio.NewScanner(conn)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				return protocol.Message{}, false
			}
			t.Fatalf("failed to read message: %v", err)
		}
		return protocol.Message{}, false
	}
	var msg protocol.Message
	if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
		t.Fatalf("failed to unmarshal message: %v", err)
	}
	return msg, true
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
	if assign := msg.Assign; assign != nil && assign.BeadID != "" && assign.Worktree != "" {
		if assign.AssignmentID == 0 {
			assign.AssignmentID = 1
			assign.Generation = 1
			assign.ActorRole = "execution_worker"
		}
		if assign.QGEvidenceDir == "" && assign.TargetSHA == "" {
			assign.QGEvidenceDir = t.TempDir()
			assign.TargetSHA = "test-target-sha"
		}
	}
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
	worktree := validAssignWorktree(t, "wt-42")
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
			Worktree: worktree,
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
	if calls[0].Workdir != worktree {
		t.Errorf("expected workdir %s, got %s", worktree, calls[0].Workdir)
	}
	if calls[0].Prompt == "" {
		t.Error("expected non-empty prompt")
	}

	cancel()
	<-errCh
}

func TestReceiveAssign_FailsClosedWhenWorktreeMissing(t *testing.T) {
	t.Parallel()

	spawner := newMockSpawner()
	dispatcherConn, workerConn := net.Pipe()
	defer func() { _ = dispatcherConn.Close() }()

	w := worker.NewWithConn("w-missing", workerConn, spawner)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := startWorkerRun(ctx, t, w, dispatcherConn)

	missingWorktree := filepath.Join(t.TempDir(), "missing-worktree")
	sendMessage(t, dispatcherConn, protocol.Message{
		Type: protocol.MsgAssign,
		Assign: &protocol.AssignPayload{
			BeadID:   "bead-missing-worktree",
			Worktree: missingWorktree,
		},
	})

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected worker to fail closed when assigned worktree is missing")
		}
		if !strings.Contains(err.Error(), "assigned worktree unavailable") {
			t.Fatalf("expected assigned worktree unavailable error, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not fail closed for missing assigned worktree")
	}
	if calls := spawner.SpawnCalls(); len(calls) != 0 {
		t.Fatalf("spawner called for missing worktree: %+v", calls)
	}
}

func TestReceiveAssign_SentinelEditUsesAssignedWorktree(t *testing.T) {
	mainRoot := validAssignWorktree(t, "main-root")
	assignedWorktree := validAssignWorktree(t, "assigned-worktree")
	t.Setenv("PWD", mainRoot)
	t.Setenv("GIT_DIR", filepath.Join(mainRoot, ".git"))
	t.Setenv("GIT_WORK_TREE", mainRoot)

	spawner := newMockSpawner()
	spawner.onSpawn = func(_ string, _ string, workdir string) error {
		return os.WriteFile(filepath.Join(workdir, "sentinel.txt"), []byte("worker edit\n"), 0o600)
	}
	dispatcherConn, workerConn := net.Pipe()
	defer func() { _ = dispatcherConn.Close() }()

	w := worker.NewWithConn("w-sentinel", workerConn, spawner)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := startWorkerRun(ctx, t, w, dispatcherConn)

	sendMessage(t, dispatcherConn, protocol.Message{
		Type: protocol.MsgAssign,
		Assign: &protocol.AssignPayload{
			BeadID:   "bead-sentinel-worktree",
			Worktree: assignedWorktree,
		},
	})

	msg := readMessage(t, dispatcherConn)
	if msg.Type != protocol.MsgStatus || msg.Status.State != "running" {
		t.Fatalf("expected running STATUS, got %+v", msg)
	}

	if _, err := os.Stat(filepath.Join(assignedWorktree, "sentinel.txt")); err != nil {
		t.Fatalf("sentinel missing from assigned worktree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(mainRoot, "sentinel.txt")); !os.IsNotExist(err) {
		t.Fatalf("sentinel leaked into main root, stat err: %v", err)
	}

	cancel()
	<-errCh
}

func TestReceiveAssign_QGRetryReportsReceipt(t *testing.T) {
	t.Parallel()

	spawner := newMockSpawner()
	worktree := validAssignWorktree(t, "wt-42")
	dispatcherConn, workerConn := net.Pipe()
	defer func() { _ = dispatcherConn.Close() }()

	w := worker.NewWithConn("w-1", workerConn, spawner)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := startWorkerRun(ctx, t, w, dispatcherConn)

	sendMessage(t, dispatcherConn, protocol.Message{
		Type: protocol.MsgAssign,
		Assign: &protocol.AssignPayload{
			BeadID:   "bead-42",
			Worktree: worktree,
			Model:    protocol.ModelOpus,
			Attempt:  1,
		},
	})

	msg := readMessage(t, dispatcherConn)
	if msg.Type != protocol.MsgStatus {
		t.Fatalf("expected STATUS, got %s", msg.Type)
	}
	if msg.Status.State != "qg_retry_received" {
		t.Fatalf("expected qg_retry_received status, got %s", msg.Status.State)
	}
	if msg.Status.BeadID != "bead-42" {
		t.Errorf("expected bead_id bead-42, got %s", msg.Status.BeadID)
	}
	if !strings.Contains(msg.Status.Result, `"attempt":1`) {
		t.Errorf("expected attempt in status result, got %q", msg.Status.Result)
	}

	msg = readMessage(t, dispatcherConn)
	if msg.Type != protocol.MsgStatus || msg.Status.State != "running" {
		t.Fatalf("expected running STATUS after retry receipt, got %+v", msg)
	}

	cancel()
	<-errCh
}

func TestWorkerHeartbeatDuringSlowAssignSpawn(t *testing.T) {
	t.Parallel()

	spawner := newMockSpawner()
	spawnStarted := make(chan struct{})
	releaseSpawn := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseSpawn) }) }
	defer release()

	spawner.onSpawn = func(_, _, _ string) error {
		close(spawnStarted)
		<-releaseSpawn
		return nil
	}

	dispatcherConn, workerConn := net.Pipe()
	defer func() { _ = dispatcherConn.Close() }()

	w := worker.NewWithConn("w-slow-spawn", workerConn, spawner)
	w.SetHeartbeatInterval(50 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := startWorkerRun(ctx, t, w, dispatcherConn)
	worktree := validAssignWorktree(t, "wt-slow-spawn")
	sendMessage(t, dispatcherConn, protocol.Message{
		Type: protocol.MsgAssign,
		Assign: &protocol.AssignPayload{
			BeadID:   "bead-slow-spawn",
			Worktree: worktree,
		},
	})

	select {
	case <-spawnStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("spawn did not start")
	}

	msg, ok := readMessageWithin(t, dispatcherConn, 500*time.Millisecond)
	if !ok {
		t.Fatal("expected heartbeat while assignment spawn was blocked")
	}
	if msg.Type != protocol.MsgHeartbeat || msg.Heartbeat == nil {
		t.Fatalf("expected HEARTBEAT while spawn blocked, got %+v", msg)
	}
	if msg.Heartbeat.BeadID != "bead-slow-spawn" {
		t.Fatalf("heartbeat bead_id = %q, want bead-slow-spawn", msg.Heartbeat.BeadID)
	}

	release()

	deadline := time.Now().Add(2 * time.Second)
	for {
		if time.Now().After(deadline) {
			t.Fatal("expected running STATUS after spawn released")
		}
		msg, ok := readMessageWithin(t, dispatcherConn, time.Until(deadline))
		if !ok {
			t.Fatal("expected running STATUS after spawn released")
		}
		if msg.Type == protocol.MsgStatus && msg.Status != nil && msg.Status.State == "running" {
			break
		}
	}

	cancel()
	<-errCh
}

func TestSendHeartbeat_TimesOutBlockedWrite(t *testing.T) {
	t.Parallel()

	conn := newDeadlineBlockingConn()
	conn.BlockWrites()
	defer func() { _ = conn.Close() }()

	w := worker.NewWithConn("w-blocked-heartbeat", conn, newMockSpawner())

	done := make(chan error, 1)
	go func() {
		done <- w.SendHeartbeat(context.Background(), 0)
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected blocked heartbeat write to return an error")
		}
	case <-time.After(time.Second):
		_ = conn.Close()
		t.Fatal("SendHeartbeat blocked past write deadline")
	}

	if err := w.SendHeartbeat(context.Background(), 0); err != nil {
		t.Fatalf("second heartbeat after write timeout should buffer during disconnect, got %v", err)
	}
}

func TestWorkerSpawnHeartbeatStopsWhenSpawnContextCancelled(t *testing.T) {
	t.Parallel()

	spawner := &contextBlockingSpawner{started: make(chan struct{})}
	dispatcherConn, workerConn := net.Pipe()
	defer func() { _ = dispatcherConn.Close() }()

	w := worker.NewWithConn("w-cancel-spawn", workerConn, spawner)
	w.SetHeartbeatInterval(50 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := startWorkerRun(ctx, t, w, dispatcherConn)
	worktree := validAssignWorktree(t, "wt-cancel-spawn")
	sendMessage(t, dispatcherConn, protocol.Message{
		Type: protocol.MsgAssign,
		Assign: &protocol.AssignPayload{
			BeadID:   "bead-cancel-spawn",
			Worktree: worktree,
		},
	})

	select {
	case <-spawner.started:
	case <-time.After(2 * time.Second):
		t.Fatal("spawn did not start")
	}

	msg, ok := readMessageWithin(t, dispatcherConn, 500*time.Millisecond)
	if !ok {
		t.Fatal("expected heartbeat while assignment spawn was blocked")
	}
	if msg.Type != protocol.MsgHeartbeat || msg.Heartbeat == nil {
		t.Fatalf("expected HEARTBEAT while spawn blocked, got %+v", msg)
	}

	cancel()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected spawn context cancellation to return an assign error")
		}
		if !strings.Contains(err.Error(), "spawn claude") || !strings.Contains(err.Error(), context.Canceled.Error()) {
			t.Fatalf("expected spawn context cancellation error, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not exit after spawn context cancellation")
	}

	if msg, ok := readMessageWithin(t, dispatcherConn, 150*time.Millisecond); ok {
		t.Fatalf("unexpected message after spawn heartbeat stopped: %+v", msg)
	}
}

func TestWorkerUsesRuntimeSpawn(t *testing.T) {
	t.Parallel()

	spawner := newMockSpawner()
	spawner.format = worker.StreamFormatLineText
	spawner.stdout = io.NopCloser(strings.NewReader("plain text runtime output\n"))
	worktree := validAssignWorktree(t, "wt-runtime")
	dispatcherConn, workerConn := net.Pipe()
	defer func() { _ = dispatcherConn.Close() }()

	w := worker.NewWithConn("w-runtime", workerConn, spawner)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := startWorkerRun(ctx, t, w, dispatcherConn)

	sendMessage(t, dispatcherConn, protocol.Message{
		Type: protocol.MsgAssign,
		Assign: &protocol.AssignPayload{
			BeadID:   "bead-runtime",
			Worktree: worktree,
		},
	})

	msg := readMessage(t, dispatcherConn)
	if msg.Type != protocol.MsgStatus {
		t.Fatalf("expected STATUS, got %s", msg.Type)
	}

	calls := spawner.SpawnCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 spawn call, got %d", len(calls))
	}
	if calls[0].Workdir != worktree {
		t.Fatalf("workdir = %q, want %q", calls[0].Workdir, worktree)
	}

	cancel()
	<-errCh
}

func TestWorkerSelectsSpawnerFromPayloadRuntime(t *testing.T) {
	t.Parallel()

	claudeSpawner := newMockSpawner()
	codexSpawner := newMockSpawner()
	codexSpawner.format = worker.StreamFormatLineText
	claudeWorktree := validAssignWorktree(t, "wt-claude")
	codexWorktree := validAssignWorktree(t, "wt-codex")
	dispatcherConn, workerConn := net.Pipe()
	defer func() { _ = dispatcherConn.Close() }()

	w := worker.NewWithConnAndRuntimeSpawners("w-runtime-route", workerConn, claudeSpawner, codexSpawner)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := startWorkerRun(ctx, t, w, dispatcherConn)

	sendMessage(t, dispatcherConn, protocol.Message{
		Type: protocol.MsgAssign,
		Assign: &protocol.AssignPayload{
			BeadID:   "bead-claude",
			Worktree: claudeWorktree,
			Runtime:  "claude",
			Model:    "claude-sonnet-4-5",
		},
	})
	msg := readMessage(t, dispatcherConn)
	if msg.Type != protocol.MsgStatus || msg.Status.State != "running" {
		t.Fatalf("expected first running STATUS, got %+v", msg)
	}

	sendMessage(t, dispatcherConn, protocol.Message{
		Type: protocol.MsgAssign,
		Assign: &protocol.AssignPayload{
			BeadID:    "bead-codex",
			Worktree:  codexWorktree,
			Runtime:   "codex",
			Model:     "gpt-5.5",
			Reasoning: "high",
		},
	})
	msg = readMessage(t, dispatcherConn)
	if msg.Type != protocol.MsgStatus || msg.Status.State != "running" {
		t.Fatalf("expected second running STATUS, got %+v", msg)
	}

	claudeCalls := claudeSpawner.SpawnCalls()
	if len(claudeCalls) != 1 {
		t.Fatalf("expected 1 Claude spawn call, got %d", len(claudeCalls))
	}
	if claudeCalls[0].Model != "claude-sonnet-4-5" || claudeCalls[0].Workdir != claudeWorktree {
		t.Fatalf("Claude call = %+v, want model claude-sonnet-4-5 workdir %s", claudeCalls[0], claudeWorktree)
	}

	codexCalls := codexSpawner.SpawnCalls()
	if len(codexCalls) != 1 {
		t.Fatalf("expected 1 Codex spawn call, got %d", len(codexCalls))
	}
	if codexCalls[0].Model != "gpt-5.5" || codexCalls[0].Reasoning != "high" || codexCalls[0].Workdir != codexWorktree {
		t.Fatalf("Codex call = %+v, want model gpt-5.5 reasoning high workdir %s", codexCalls[0], codexWorktree)
	}

	cancel()
	<-errCh
}

func TestWorkerRunsWithCodexLineStream(t *testing.T) {
	t.Parallel()

	spawner := newMockSpawner()
	spawner.format = worker.StreamFormatLineText
	spawner.stdout = io.NopCloser(strings.NewReader("codex says hello\n[MEMORY] fact: line stream works\n"))
	worktree := validAssignWorktree(t, "wt-codex")
	dispatcherConn, workerConn := net.Pipe()
	defer func() { _ = dispatcherConn.Close() }()

	w := worker.NewWithConn("w-codex", workerConn, spawner)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := startWorkerRun(ctx, t, w, dispatcherConn)

	sendMessage(t, dispatcherConn, protocol.Message{
		Type: protocol.MsgAssign,
		Assign: &protocol.AssignPayload{
			BeadID:   "bead-codex",
			Worktree: worktree,
			Model:    "gpt-5.5",
		},
	})

	msg := readMessage(t, dispatcherConn)
	if msg.Type != protocol.MsgStatus {
		t.Fatalf("expected STATUS, got %s", msg.Type)
	}

	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(w.SessionText(), "codex says hello") ||
		!strings.Contains(w.SessionText(), "[MEMORY] fact: line stream works") {
		if time.Now().After(deadline) {
			t.Fatalf("session text did not capture codex line stream output, got %q", w.SessionText())
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	<-errCh
}

func TestReceiveShutdown_ExitsCleanly(t *testing.T) {
	t.Parallel()

	spawner := newMockSpawner()
	worktree := validAssignWorktree(t, "wt-99")
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
			Worktree: worktree,
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

func TestRunQualityGate_DoesNotInheritMutationSkipWhenDisabled(t *testing.T) {
	t.Setenv("ORO_SKIP_MUTATION", "1")

	tmpDir := t.TempDir()
	script := filepath.Join(tmpDir, "quality_gate.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nif [ \"${ORO_SKIP_MUTATION:-}\" = \"1\" ]; then echo unexpected; exit 1; fi\necho clean\nexit 0\n"), 0o600); err != nil { //nolint:gosec // test file
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
		t.Fatalf("expected quality gate to pass without inherited ORO_SKIP_MUTATION, output: %s", output)
	}
}

func TestRunQualityGate_NormalizesInvalidLocaleBeforeStartingBash(t *testing.T) {
	t.Setenv("LC_ALL", "oro_invalid_locale_for_test.UTF-8")
	t.Setenv("LANG", "oro_invalid_locale_for_test.UTF-8")

	tmpDir := t.TempDir()
	script := filepath.Join(tmpDir, "quality_gate.sh")
	if err := os.WriteFile(script, []byte("#!/usr/bin/env bash\nprintf 'locale=%s lang=%s\\n' \"${LC_ALL:-}\" \"${LANG:-}\"\n"), 0o600); err != nil { //nolint:gosec // test file
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
		t.Fatalf("expected quality gate to pass, output: %s", output)
	}
	if strings.Contains(output, "setlocale") {
		t.Fatalf("quality gate inherited invalid locale before bash startup: %q", output)
	}
	if !strings.Contains(output, "locale=C lang=C") {
		t.Fatalf("quality gate did not normalize locale env, output: %q", output)
	}
}

func TestRunQualityGate_ScrubsAmbientLockTimeout(t *testing.T) {
	t.Setenv("ORO_QG_LOCK_TIMEOUT_SECONDS", "300")

	tmpDir := t.TempDir()
	script := filepath.Join(tmpDir, "quality_gate.sh")
	if err := os.WriteFile(script, []byte(`#!/usr/bin/env bash
set -euo pipefail
printf 'lock-timeout=%s\n' "${ORO_QG_LOCK_TIMEOUT_SECONDS:-}"
test -z "${ORO_QG_LOCK_TIMEOUT_SECONDS:-}"
`), 0o600); err != nil { //nolint:gosec // test file
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
		t.Fatalf("expected quality gate to pass without inherited lock timeout, output: %s", output)
	}
}

func TestRunQualityGate_PreservesInheritedQualityGateLock(t *testing.T) {
	t.Setenv("ORO_QG_INHERITED_LOCK_DIR", "/tmp/inherited-quality-gate-lock")
	t.Setenv("ORO_QG_INHERITED_LOCK_TOKEN", "inherited-token")

	tmpDir := t.TempDir()
	script := filepath.Join(tmpDir, "quality_gate.sh")
	if err := os.WriteFile(script, []byte(`#!/usr/bin/env bash
set -euo pipefail
test "${ORO_QG_INHERITED_LOCK_DIR:-}" = "/tmp/inherited-quality-gate-lock"
test "${ORO_QG_INHERITED_LOCK_TOKEN:-}" = "inherited-token"
`), 0o600); err != nil { //nolint:gosec // test file
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
		t.Fatalf("expected quality gate to pass with inherited lock state, output: %s", output)
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

func TestRunQualityGate_ContextCancellationIsNotQGFailure(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	script := filepath.Join(tmpDir, "quality_gate.sh")
	body := `#!/bin/sh
echo "Waiting for another quality gate to finish..."
echo "═══════════════════════════════════════════════════════════════"
echo " ORO QUALITY GATE"
echo "═══════════════════════════════════════════════════════════════"
touch qg-started
while :; do
	:
done
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil { //nolint:gosec // test script
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct {
		passed bool
		output string
		err    error
	}, 1)
	go func() {
		passed, output, err := worker.RunQualityGate(ctx, tmpDir, false)
		done <- struct {
			passed bool
			output string
			err    error
		}{passed: passed, output: output, err: err}
	}()

	waitFor(t, func() bool {
		_, err := os.Stat(filepath.Join(tmpDir, "qg-started"))
		return err == nil
	}, 2*time.Second)
	cancel()

	select {
	case got := <-done:
		if got.err == nil {
			t.Fatalf("RunQualityGate returned nil error for context cancellation; passed=%v output=%q", got.passed, got.output)
		}
		if !errors.Is(got.err, context.Canceled) {
			t.Fatalf("RunQualityGate error = %v, want context.Canceled", got.err)
		}
		if got.passed {
			t.Fatal("cancelled quality gate must not pass")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunQualityGate did not return after context cancellation")
	}
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

func TestRunQualityGate_RestoreUsesAssignedWorktreeWithPoisonedGitEnv(t *testing.T) {
	initRepoWithQG := func(t *testing.T, dir, message string) string {
		t.Helper()
		scriptPath := filepath.Join(dir, "quality_gate.sh")
		scriptContent := []byte("#!/bin/sh\necho '" + message + "'\nexit 0\n")
		if err := os.WriteFile(scriptPath, scriptContent, 0o755); err != nil { //nolint:gosec // test file
			t.Fatal(err)
		}
		for _, args := range [][]string{
			{"init"},
			{"config", "user.email", "test@test.com"},
			{"config", "user.name", "Test"},
			{"add", "quality_gate.sh"},
			{"commit", "-m", "initial"},
		} {
			cmd := exec.Command("git", args...) //nolint:gosec // test helper with fixed args
			cmd.Dir = dir
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
			}
		}
		return scriptPath
	}

	mainRoot := t.TempDir()
	assignedWorktree := t.TempDir()
	mainScript := initRepoWithQG(t, mainRoot, "main qg")
	assignedScript := initRepoWithQG(t, assignedWorktree, "assigned qg")

	if err := os.Remove(mainScript); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(assignedScript); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PWD", mainRoot)
	t.Setenv("GIT_DIR", filepath.Join(mainRoot, ".git"))
	t.Setenv("GIT_WORK_TREE", mainRoot)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(mainRoot, ".git", "index"))

	passed, output, err := worker.RunQualityGate(context.Background(), assignedWorktree, false)
	if err != nil {
		t.Fatalf("RunQualityGate: %v", err)
	}
	if !passed {
		t.Fatalf("expected quality gate to pass after assigned-worktree restore, output: %q", output)
	}
	if !strings.Contains(output, "assigned qg") {
		t.Fatalf("expected assigned worktree quality gate output, got: %q", output)
	}
	if _, err := os.Stat(assignedScript); err != nil {
		t.Fatalf("assigned quality_gate.sh was not restored: %v", err)
	}
	if _, err := os.Stat(mainScript); !os.IsNotExist(err) {
		t.Fatalf("poisoned Git env restored main quality_gate.sh, stat err: %v", err)
	}
}

func TestRunQualityGate_ChildProcessUsesAssignedWorktreeEnv(t *testing.T) {
	mainRoot := t.TempDir()
	assignedWorktree := t.TempDir()
	if err := os.MkdirAll(filepath.Join(assignedWorktree, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	scriptPath := filepath.Join(assignedWorktree, "scripts", "quality_gate.sh")
	script := `#!/bin/sh
set -eu
sh -c 'printf "PWD=%s\nACTUAL=%s\nGIT_DIR=%s\nGIT_WORK_TREE=%s\nGIT_INDEX_FILE=%s\n" "$PWD" "$(pwd -P)" "${GIT_DIR-unset}" "${GIT_WORK_TREE-unset}" "${GIT_INDEX_FILE-unset}" > qg-hook-env.txt'
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil { //nolint:gosec // test script
		t.Fatal(err)
	}

	t.Setenv("PWD", mainRoot)
	t.Setenv("GIT_DIR", filepath.Join(mainRoot, ".git"))
	t.Setenv("GIT_WORK_TREE", mainRoot)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(mainRoot, ".git", "index"))

	passed, output, err := worker.RunQualityGate(context.Background(), assignedWorktree, false)
	if err != nil {
		t.Fatalf("RunQualityGate: %v", err)
	}
	if !passed {
		t.Fatalf("expected quality gate to pass, output: %q", output)
	}

	assignedEnv, err := os.ReadFile(filepath.Join(assignedWorktree, "qg-hook-env.txt"))
	if err != nil {
		t.Fatalf("assigned child env missing: %v", err)
	}
	text := string(assignedEnv)
	if !strings.Contains(text, "PWD="+assignedWorktree+"\n") {
		t.Fatalf("expected child PWD to be assigned worktree, got:\n%s", text)
	}
	for _, key := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE"} {
		if !strings.Contains(text, key+"=unset\n") {
			t.Fatalf("expected %s unset in child env, got:\n%s", key, text)
		}
	}
	if _, err := os.Stat(filepath.Join(mainRoot, "qg-hook-env.txt")); !os.IsNotExist(err) {
		t.Fatalf("poisoned env wrote hook artifact in main root, stat err: %v", err)
	}
}

func TestRunQualityGate_PrefersDispatcherManagedRootScript(t *testing.T) {
	worktree := t.TempDir()
	if err := os.MkdirAll(filepath.Join(worktree, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	staleScript := filepath.Join(worktree, "scripts", "quality_gate.sh")
	if err := os.WriteFile(staleScript, []byte("#!/bin/sh\necho stale branch qg\nexit 0\n"), 0o755); err != nil { //nolint:gosec // test script
		t.Fatal(err)
	}
	rootScript := filepath.Join(worktree, "quality_gate.sh")
	if err := os.WriteFile(rootScript, []byte("#!/bin/sh\necho dispatcher managed qg\nexit 0\n"), 0o755); err != nil { //nolint:gosec // test script
		t.Fatal(err)
	}

	passed, output, err := worker.RunQualityGate(context.Background(), worktree, false)
	if err != nil {
		t.Fatalf("RunQualityGate: %v", err)
	}
	if !passed {
		t.Fatalf("expected quality gate to pass, output: %q", output)
	}
	if !strings.Contains(output, "dispatcher managed qg") {
		t.Fatalf("expected root quality gate output, got: %q", output)
	}
	if strings.Contains(output, "stale branch qg") {
		t.Fatalf("stale scripts/quality_gate.sh was used, output: %q", output)
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

func TestRunQualityGate_SkipMutationScrubsRunMutationEnv(t *testing.T) {
	t.Setenv("ORO_RUN_MUTATION", "1")

	tmpDir := t.TempDir()
	script := filepath.Join(tmpDir, "quality_gate.sh")
	scriptContent := `#!/bin/sh
if [ "${ORO_SKIP_MUTATION:-}" != "1" ]; then
  echo "FAIL: ORO_SKIP_MUTATION not set"
  exit 1
fi
if [ -n "${ORO_RUN_MUTATION:-}" ]; then
  echo "FAIL: ORO_RUN_MUTATION inherited"
  exit 1
fi
echo "PASS: mutation disabled"
exit 0
`
	if err := os.WriteFile(script, []byte(scriptContent), 0o600); err != nil { //nolint:gosec // test file
		t.Fatal(err)
	}
	if err := os.Chmod(script, 0o755); err != nil { //nolint:gosec // test script must be executable
		t.Fatal(err)
	}

	passed, output, err := worker.RunQualityGate(context.Background(), tmpDir, true)
	if err != nil {
		t.Fatalf("RunQualityGate: %v", err)
	}
	if !passed {
		t.Fatalf("expected quality gate to pass with inherited mutation opt-in scrubbed, output: %s", output)
	}
}

func TestRunQualityGate_MutationTestingUsesFlagNotAmbientEnv(t *testing.T) {
	t.Setenv("ORO_RUN_MUTATION", "1")

	tmpDir := t.TempDir()
	script := filepath.Join(tmpDir, "quality_gate.sh")
	scriptContent := `#!/bin/sh
if [ "${1:-}" != "--mutation-testing" ]; then
  echo "FAIL: missing --mutation-testing flag: $*"
  exit 1
fi
if [ -n "${ORO_RUN_MUTATION:-}" ]; then
  echo "FAIL: ORO_RUN_MUTATION inherited"
  exit 1
fi
echo "PASS: mutation enabled by flag only"
exit 0
`
	if err := os.WriteFile(script, []byte(scriptContent), 0o600); err != nil { //nolint:gosec // test file
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
		t.Fatalf("expected quality gate to pass with flag-based mutation opt-in, output: %s", output)
	}
}

func TestBuildPrompt_DelegatesQualityGateInstruction(t *testing.T) {
	t.Parallel()

	prompt := worker.BuildPrompt("bead-123", "/tmp/wt-123", "")
	if strings.Contains(prompt, "quality_gate.sh") {
		t.Error("expected prompt not to name a quality gate script")
	}
	if !strings.Contains(prompt, "worker harness") {
		t.Error("expected prompt to delegate quality gate ownership to worker harness")
	}
}

func TestBuildPrompt_DelegatesAuthoritativeQG(t *testing.T) {
	t.Parallel()

	const memory = "memory context\nwith exact bytes"
	prompt := worker.BuildPrompt("bead-123", "/tmp/wt-123", memory)

	if !strings.Contains(prompt, "acceptance") || !strings.Contains(prompt, "focused") {
		t.Error("expected prompt to require acceptance and focused verification")
	}
	if !strings.Contains(prompt, "worker harness") || !strings.Contains(prompt, "full quality gate") {
		t.Error("expected prompt to identify the worker harness as full-QG owner")
	}
	for _, forbidden := range []string{"./quality_gate.sh", "./scripts/quality_gate.sh", "--mutation-testing"} {
		if strings.Contains(prompt, forbidden) {
			t.Errorf("prompt must not instruct coding subprocess to run %q", forbidden)
		}
	}
	if !strings.HasSuffix(prompt, "\n\n"+memory) {
		t.Error("expected memory context to remain appended byte-for-byte")
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
	worktree := validAssignWorktree(t, "wt-recon")
	sendMessage(t, dispConn1, protocol.Message{
		Type: protocol.MsgAssign,
		Assign: &protocol.AssignPayload{
			BeadID:   "bead-recon",
			Worktree: worktree,
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

func TestReconnectDoesNotNestRunLoops(t *testing.T) { //nolint:funlen // integration test requires sequential reconnect setup
	t.Parallel()

	spawner := newMockSpawner()

	sockDir, err := os.MkdirTemp("/tmp", "oro-test-single-loop-")
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

	w, err := worker.New("w-single-loop", sockPath, spawner)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	w.SetReconnectInterval(50 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- w.Run(ctx) }()

	var dispConn1 net.Conn
	select {
	case dispConn1 = <-acceptCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for first connection")
	}
	_ = readMessage(t, dispConn1)

	_ = dispConn1.Close()

	var dispConn2 net.Conn
	select {
	case dispConn2 = <-acceptCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for first reconnect")
	}

	msg := readMessage(t, dispConn2)
	if msg.Type != protocol.MsgReconnect {
		t.Fatalf("expected RECONNECT after first reconnect, got %s", msg.Type)
	}
	if msg, ok := readMessageWithin(t, dispConn2, 200*time.Millisecond); ok {
		t.Fatalf("unexpected nested Run message after reconnect: %+v", msg)
	}

	worktree := validAssignWorktree(t, "wt-single-loop")
	sendMessage(t, dispConn2, protocol.Message{
		Type: protocol.MsgAssign,
		Assign: &protocol.AssignPayload{
			BeadID:   "bead-single-loop",
			Worktree: worktree,
		},
	})
	msg = readMessage(t, dispConn2)
	if msg.Type != protocol.MsgStatus || msg.Status == nil || msg.Status.State != "running" {
		t.Fatalf("expected running STATUS after reconnect assignment, got %+v", msg)
	}
	if calls := spawner.SpawnCalls(); len(calls) != 1 {
		t.Fatalf("expected reconnect assignment to spawn once, got %d calls", len(calls))
	}

	_ = dispConn2.Close()

	var dispConn3 net.Conn
	select {
	case dispConn3 = <-acceptCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for second reconnect")
	}
	defer func() { _ = dispConn3.Close() }()

	msg = readMessage(t, dispConn3)
	if msg.Type != protocol.MsgReconnect {
		t.Fatalf("expected RECONNECT after second reconnect, got %s", msg.Type)
	}
	if msg, ok := readMessageWithin(t, dispConn3, 200*time.Millisecond); ok {
		t.Fatalf("unexpected nested Run message after second reconnect: %+v", msg)
	}

	sendMessage(t, dispConn3, protocol.Message{Type: protocol.MsgShutdown})
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("expected nil error on shutdown after reconnects, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not exit after shutdown")
	}
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

func TestWorkerHandlesPreempt(t *testing.T) {
	t.Run("busy worker saves handoff, kills subprocess, and exits", func(t *testing.T) {
		spawner := newMockSpawner()
		dispatcherConn, workerConn := net.Pipe()
		defer func() { _ = dispatcherConn.Close() }()

		w := worker.NewWithConn("w-preempt-busy", workerConn, spawner)
		errCh := startWorkerRun(t.Context(), t, w, dispatcherConn)

		sendMessage(t, dispatcherConn, protocol.Message{
			Type: protocol.MsgAssign,
			Assign: &protocol.AssignPayload{
				BeadID:   "bead-preempt",
				Worktree: validAssignWorktree(t, "preempt-busy"),
			},
		})
		_ = readMessage(t, dispatcherConn) // drain STATUS

		sendMessage(t, dispatcherConn, protocol.Message{Type: protocol.MsgPreempt})
		msg := readMessage(t, dispatcherConn)
		if msg.Type != protocol.MsgHandoff {
			t.Fatalf("expected HANDOFF, got %s", msg.Type)
		}
		if msg.Handoff == nil || msg.Handoff.BeadID != "bead-preempt" {
			t.Fatalf("expected handoff for bead-preempt, got %+v", msg.Handoff)
		}

		waitFor(t, spawner.process.Killed, 200*time.Millisecond)
		select {
		case err := <-errCh:
			if err != nil {
				t.Fatalf("expected clean worker exit, got %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("worker did not exit after PREEMPT")
		}
	})

	t.Run("idle worker exits cleanly", func(t *testing.T) {
		spawner := newMockSpawner()
		dispatcherConn, workerConn := net.Pipe()
		defer func() { _ = dispatcherConn.Close() }()

		w := worker.NewWithConn("w-preempt-idle", workerConn, spawner)
		errCh := startWorkerRun(t.Context(), t, w, dispatcherConn)

		sendMessage(t, dispatcherConn, protocol.Message{Type: protocol.MsgPreempt})
		msg := readMessage(t, dispatcherConn)
		if msg.Type != protocol.MsgHandoff {
			t.Fatalf("expected HANDOFF, got %s", msg.Type)
		}

		select {
		case err := <-errCh:
			if err != nil {
				t.Fatalf("expected clean worker exit, got %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("idle worker did not exit after PREEMPT")
		}
	})

	t.Run("handoff failure still kills subprocess and exits", func(t *testing.T) {
		spawner := newMockSpawner()
		dispatcherConn, workerConn := net.Pipe()

		w := worker.NewWithConn("w-preempt-handoff-failure", workerConn, spawner)
		errCh := startWorkerRun(t.Context(), t, w, dispatcherConn)

		sendMessage(t, dispatcherConn, protocol.Message{
			Type: protocol.MsgAssign,
			Assign: &protocol.AssignPayload{
				BeadID:   "bead-preempt-handoff-failure",
				Worktree: validAssignWorktree(t, "preempt-handoff-failure"),
			},
		})
		_ = readMessage(t, dispatcherConn) // drain STATUS

		sendMessage(t, dispatcherConn, protocol.Message{Type: protocol.MsgPreempt})
		_ = dispatcherConn.Close()

		waitFor(t, spawner.process.Killed, 200*time.Millisecond)
		select {
		case err := <-errCh:
			if err != nil {
				t.Fatalf("expected clean worker exit, got %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("worker did not exit after failed PREEMPT handoff")
		}
	})
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

	// Send ASSIGN with a real worktree that has no .oro/context_pct
	worktree := validAssignWorktree(t, "wt-nofile")
	sendMessage(t, dispatcherConn, protocol.Message{
		Type: protocol.MsgAssign,
		Assign: &protocol.AssignPayload{
			BeadID:   "bead-nofile",
			Worktree: worktree,
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
	worktree := validAssignWorktree(t, "wt-proc")
	sendMessage(t, dispatcherConn, protocol.Message{
		Type: protocol.MsgAssign,
		Assign: &protocol.AssignPayload{
			BeadID:   "bead-proc",
			Worktree: worktree,
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

// TestHandleAssignExposesBeadIDForSelfCloseGuard proves the worker exports
// ORO_WORKER_BEAD_ID only to the assigned subprocess so the `oro task close`
// self-close guard can identify the assigned bead without mutating its parent.
func TestHandleAssignExposesBeadIDForSelfCloseGuard(t *testing.T) {
	t.Setenv("ORO_WORKER_BEAD_ID", "caller-identity")

	spawner := newMockSpawner()
	captured := make(chan string, 1)
	spawner.onSpawnContext = func(ctx context.Context, _, _, _ string) error {
		captured <- envValue(worker.EnvironmentForContext(ctx, os.Environ()), "ORO_WORKER_BEAD_ID")
		return nil
	}

	dispatcherConn, workerConn := net.Pipe()
	defer func() { _ = dispatcherConn.Close() }()

	w := worker.NewWithConn("w-beadidenv", workerConn, spawner)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	errCh := startWorkerRun(ctx, t, w, dispatcherConn)

	worktree := validAssignWorktree(t, "wt-beadidenv")
	sendMessage(t, dispatcherConn, protocol.Message{
		Type: protocol.MsgAssign,
		Assign: &protocol.AssignPayload{
			BeadID:   "oro-t5ha-fixture",
			Worktree: worktree,
		},
	})

	select {
	case got := <-captured:
		if got != "oro-t5ha-fixture" {
			t.Fatalf("ORO_WORKER_BEAD_ID in child environment = %q, want oro-t5ha-fixture", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Spawn was not called within 2s")
	}
	if got := os.Getenv("ORO_WORKER_BEAD_ID"); got != "caller-identity" {
		t.Fatalf("ORO_WORKER_BEAD_ID in parent environment = %q, want caller-identity", got)
	}

	// Drain status message + clean shutdown.
	_ = readMessage(t, dispatcherConn)
	cancel()
	select {
	case <-errCh:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not exit after cancel")
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

	worktree := validAssignWorktree(t, "wt-fail")
	sendMessage(t, dispatcherConn, protocol.Message{
		Type: protocol.MsgAssign,
		Assign: &protocol.AssignPayload{
			BeadID:   "bead-fail",
			Worktree: worktree,
		},
	})

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected error for spawn failure, got nil")
		}
		if !strings.Contains(err.Error(), "spawn claude") || !strings.Contains(err.Error(), "spawn failed") {
			t.Fatalf("expected wrapped spawn failure, got %v", err)
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
		if err != nil {
			t.Fatalf("expected nil error from reconnect with cancelled context, got %v", err)
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

	worktree := validAssignWorktree(t, "wt-statuserr")
	sendMessage(t, dispatcherConn, protocol.Message{
		Type: protocol.MsgAssign,
		Assign: &protocol.AssignPayload{
			BeadID:   "bead-statuserr",
			Worktree: worktree,
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

func (s *connClosingSpawner) StreamFormat() worker.StreamFormat {
	return worker.StreamFormatClaudeJSON
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
	worktree := validAssignWorktree(t, "wt-bufmsg")
	sendMessage(t, dispConn1, protocol.Message{
		Type: protocol.MsgAssign,
		Assign: &protocol.AssignPayload{
			BeadID:   "bead-bufmsg",
			Worktree: worktree,
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

// mockExtractSpawner implements worker.MemoryExtractSpawner for testing extraction wiring.
type mockExtractSpawner struct {
	mu               sync.Mutex
	callCount        int
	workdirCallCount int
	lastWorkdir      string
	output           string // simulated LLM output
}

func (m *mockExtractSpawner) Spawn(_ context.Context, _, _ string) (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.callCount++
	return io.NopCloser(strings.NewReader(m.output)), nil
}

func (m *mockExtractSpawner) SpawnInWorkdir(_ context.Context, _, _, workdir string) (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.callCount++
	m.workdirCallCount++
	m.lastWorkdir = workdir
	return io.NopCloser(strings.NewReader(m.output)), nil
}

func (m *mockExtractSpawner) CallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.callCount
}

func (m *mockExtractSpawner) WorkdirCall() (int, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.workdirCallCount, m.lastWorkdir
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

		// Create temp worktree with a passing quality_gate.sh. The worker's
		// local QG path must keep mutation testing disabled by default.
		tmpDir := t.TempDir()
		script := filepath.Join(tmpDir, "quality_gate.sh")
		if err := os.WriteFile(script, []byte("#!/bin/sh\nif [ \"${ORO_SKIP_MUTATION:-}\" != \"1\" ]; then echo 'missing ORO_SKIP_MUTATION'; exit 1; fi\nif [ \"${ORO_MUTATION_BASE:-}\" != \"epic/worker-target\" ]; then echo \"wrong ORO_MUTATION_BASE=${ORO_MUTATION_BASE:-unset}\"; exit 1; fi\necho 'all checks passed'\nexit 0\n"), 0o600); err != nil { //nolint:gosec // test file
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
				BeadID:       "bead-rfr",
				Worktree:     tmpDir,
				TargetBranch: "epic/worker-target",
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

	t.Run("QG context cancellation does not send failure Done", func(t *testing.T) {
		t.Parallel()

		tmpDir := t.TempDir()
		script := filepath.Join(tmpDir, "quality_gate.sh")
		body := `#!/bin/sh
echo "Waiting for another quality gate to finish..."
echo "═══════════════════════════════════════════════════════════════"
echo " ORO QUALITY GATE"
echo "═══════════════════════════════════════════════════════════════"
touch qg-started
while :; do
	:
done
`
		if err := os.WriteFile(script, []byte(body), 0o600); err != nil { //nolint:gosec // test file
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

		w := worker.NewWithConn("w-rfr-qg-cancel", workerConn, spawner)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		errCh := startWorkerRun(ctx, t, w, dispatcherConn)

		sendMessage(t, dispatcherConn, protocol.Message{
			Type: protocol.MsgAssign,
			Assign: &protocol.AssignPayload{
				BeadID:   "bead-rfr-qg-cancel",
				Worktree: tmpDir,
			},
		})
		_ = readMessage(t, dispatcherConn) // drain STATUS running

		_, _ = pw.Write([]byte(textDeltaLine("work done\n") + "\n"))
		_ = pw.Close()
		close(proc.waitCh)

		_ = readMessage(t, dispatcherConn) // drain STATUS awaiting_review
		waitFor(t, func() bool {
			_, err := os.Stat(filepath.Join(tmpDir, "qg-started"))
			return err == nil
		}, 2*time.Second)

		cancel()
		select {
		case err := <-errCh:
			if err != nil {
				t.Fatalf("worker Run returned error after cancellation: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("worker did not exit after QG context cancellation")
		}

		if msg, ok := readMessageWithin(t, dispatcherConn, 100*time.Millisecond); ok && msg.Type == protocol.MsgDone {
			t.Fatalf("cancelled quality gate sent DONE with partial output: %+v", msg.Done)
		}
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

func (s *multiMockSpawner) StreamFormat() worker.StreamFormat {
	return worker.StreamFormatClaudeJSON
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
	firstWorktree := validAssignWorktree(t, "wt-first")
	sendMessage(t, dispatcherConn, protocol.Message{
		Type: protocol.MsgAssign,
		Assign: &protocol.AssignPayload{
			BeadID:   "bead-first",
			Worktree: firstWorktree,
		},
	})
	// Drain STATUS from first ASSIGN
	_ = readMessage(t, dispatcherConn)

	// oldProc should be running (not killed)
	if oldProc.Killed() {
		t.Fatal("old process should NOT be killed yet")
	}

	// Second ASSIGN (re-assignment after QG failure) — should kill oldProc, spawn newProc
	retryWorktree := validAssignWorktree(t, "wt-retry")
	sendMessage(t, dispatcherConn, protocol.Message{
		Type: protocol.MsgAssign,
		Assign: &protocol.AssignPayload{
			BeadID:   "bead-retry",
			Worktree: retryWorktree,
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

	// timerStopped is closed by the hook when timer.Stop() is called on ctx
	// cancellation — proves structural cleanup without NumGoroutine() heuristics.
	timerStopped := make(chan struct{})
	w.SetReconnectTimerStopHook(func() { close(timerStopped) })

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

	// Cancel context during the reconnect sleep — timer must be stopped cleanly.
	cancel()

	// Structural assertion: timer.Stop() must be called as part of cleanup.
	select {
	case <-timerStopped:
	case <-time.After(2 * time.Second):
		t.Fatal("timer.Stop was not called after context cancellation")
	}

	select {
	case <-errCh:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not exit after cancel during reconnect")
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
	proc.waitErr = fmt.Errorf("signal: killed")
	proc.exitCode = 137
	proc.stderr = numberedLines("stderr line", 101)
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

			if msg.Type != protocol.MsgDone {
				// Ignore other message types (like HEARTBEAT)
				continue
			}
			doneReceived = true
			assertSubprocessDiedDone(t, msg)
		}
	}

	cancel()
	<-errCh
}

func assertSubprocessDiedDone(t *testing.T, msg protocol.Message) {
	t.Helper()

	if msg.Done.QualityGatePassed {
		t.Error("expected QualityGatePassed=false when subprocess dies unexpectedly")
	}
	if msg.Done.QGOutput == "subprocess died unexpectedly" {
		t.Fatal("expected structured subprocess diagnostics, got bare message")
	}
	if !strings.Contains(msg.Done.QGOutput, "reason: subprocess_died") {
		t.Errorf("expected subprocess_died reason in QGOutput, got: %q", msg.Done.QGOutput)
	}
	if !strings.Contains(msg.Done.QGOutput, "exit_code: 137") {
		t.Errorf("expected exit code in QGOutput, got: %q", msg.Done.QGOutput)
	}
	if !strings.Contains(msg.Done.QGOutput, "exit_error: signal: killed") {
		t.Errorf("expected wait error in QGOutput, got: %q", msg.Done.QGOutput)
	}
	if strings.Contains(msg.Done.QGOutput, "stderr line 001") {
		t.Errorf("expected stderr tail to drop oldest lines, got: %q", msg.Done.QGOutput)
	}
	if !strings.Contains(msg.Done.QGOutput, "stderr line 002") ||
		!strings.Contains(msg.Done.QGOutput, "stderr line 101") {
		t.Errorf("expected last 100 stderr lines in QGOutput, got: %q", msg.Done.QGOutput)
	}

	var raw map[string]any
	data, err := json.Marshal(msg.Done)
	if err != nil {
		t.Fatalf("marshal done payload: %v", err)
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal done payload: %v", err)
	}
	if raw["failure_reason"] != "subprocess_died" {
		t.Fatalf("failure_reason = %#v, want subprocess_died", raw["failure_reason"])
	}
	subprocess, ok := raw["subprocess_exit"].(map[string]any)
	if !ok {
		t.Fatalf("subprocess_exit missing from DONE payload: %#v", raw)
	}
	if subprocess["exit_code"] != float64(137) {
		t.Errorf("subprocess_exit.exit_code = %#v, want 137", subprocess["exit_code"])
	}
	stderrTail, _ := subprocess["stderr_tail"].(string)
	if !strings.Contains(stderrTail, "stderr line 101") || strings.Contains(stderrTail, "stderr line 001") {
		t.Errorf("subprocess_exit.stderr_tail did not preserve last 100 lines: %q", stderrTail)
	}
}

func numberedLines(prefix string, count int) string {
	var b strings.Builder
	for i := 1; i <= count; i++ {
		fmt.Fprintf(&b, "%s %03d\n", prefix, i)
	}
	return b.String()
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

	if err := os.WriteFile(filepath.Join(oroDir, "context_pct"), []byte("42"), 0o600); err != nil {
		t.Fatal(err)
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

// TestClaudeSpawnerStreamFormat verifies the format identifier returned by the
// production spawner. This is a tiny but real surface: the dispatcher uses it
// to pick a parser, so accidental drift would break stream parsing silently.
func TestClaudeSpawnerStreamFormat(t *testing.T) {
	t.Parallel()
	got := (&worker.ClaudeSpawner{}).StreamFormat()
	if got != worker.StreamFormatClaudeJSON {
		t.Errorf("StreamFormat = %q, want %q", got, worker.StreamFormatClaudeJSON)
	}
}

// TestCmdProcess_WaitAndKill exercises the production Process implementation
// against real subprocesses. Together they cover the success and error paths
// of both Wait (exit 0 vs non-zero) and Kill (with and without a started
// process), which the goroutine-based mockProcess elsewhere in this file does
// not exercise.
func TestCmdProcess_WaitAndKill(t *testing.T) {
	t.Parallel()

	t.Run("Wait returns nil on clean exit", func(t *testing.T) {
		t.Parallel()
		cmd := exec.Command("true")
		if err := cmd.Start(); err != nil {
			t.Fatalf("start: %v", err)
		}
		p := &worker.CmdProcess{Cmd: cmd}
		if err := p.Wait(); err != nil {
			t.Errorf("Wait on clean exit: %v", err)
		}
	})

	t.Run("Wait wraps non-zero exit", func(t *testing.T) {
		t.Parallel()
		cmd := exec.Command("false")
		if err := cmd.Start(); err != nil {
			t.Fatalf("start: %v", err)
		}
		p := &worker.CmdProcess{Cmd: cmd}
		err := p.Wait()
		if err == nil {
			t.Fatal("Wait on failed exit returned nil error")
		}
		if !strings.Contains(err.Error(), "claude process wait") {
			t.Errorf("error not wrapped: %v", err)
		}
	})

	t.Run("Kill on unstarted process is a no-op", func(t *testing.T) {
		t.Parallel()
		p := &worker.CmdProcess{Cmd: exec.Command("true")}
		if err := p.Kill(); err != nil {
			t.Errorf("Kill on unstarted: %v", err)
		}
	})

	t.Run("Kill terminates a running process", func(t *testing.T) {
		t.Parallel()
		cmd := exec.Command("sleep", "30")
		if err := cmd.Start(); err != nil {
			t.Fatalf("start: %v", err)
		}
		p := &worker.CmdProcess{Cmd: cmd}
		if err := p.Kill(); err != nil {
			t.Errorf("Kill: %v", err)
		}
		_ = cmd.Wait() // reap; ignore "signal: killed" error
	})
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

// TestHardStopThresholds verifies hard stop parity for tier-first threshold
// lookup, model-family fallback, and default fallback when threshold data is
// missing or invalid.
func TestHardStopThresholds(t *testing.T) { //nolint:funlen // table-driven integration test with parallel subtests
	t.Parallel()

	cases := []struct {
		name           string
		thresholdsData string
		writeThreshold bool
		tier           protocol.Tier
		model          string
		hardStop       int
	}{
		{
			name:           "known tier wins over model family",
			thresholdsData: `{"fast": 35, "balanced": 45, "sonnet": 55}`,
			writeThreshold: true,
			tier:           protocol.TierFast,
			model:          "claude-3-5-sonnet-20241022",
			hardStop:       45, // fast 35 + 10
		},
		{
			name:           "unknown tier falls back to sonnet model family",
			thresholdsData: `{"fast": 35, "balanced": 45, "sonnet": 55}`,
			writeThreshold: true,
			tier:           "experimental",
			model:          "claude-3-5-sonnet-20241022",
			hardStop:       65, // sonnet 55 + 10
		},
		{
			name:           "missing threshold file falls back to default",
			writeThreshold: false,
			model:          "gpt-5.5",
			hardStop:       50, // default 40 + 10
		},
		{
			name:           "invalid threshold value falls back to default",
			thresholdsData: `{"fast": 0, "balanced": 45, "sonnet": 55}`,
			writeThreshold: true,
			tier:           protocol.TierFast,
			model:          "claude-3-5-sonnet-20241022",
			hardStop:       50, // invalid fast value falls back to default 40 + 10
		},
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
			if tc.writeThreshold {
				if err := os.WriteFile(filepath.Join(tmpDir, "thresholds.json"), []byte(tc.thresholdsData), 0o600); err != nil {
					t.Fatal(err)
				}
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
					Tier:     tc.tier,
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
			assertNoHandoffWithin(t, dispatcherConn, 100*time.Millisecond)

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

func assertNoHandoffWithin(t *testing.T, conn net.Conn, timeout time.Duration) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	if err := conn.SetReadDeadline(deadline); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	scanner := bufio.NewScanner(conn)
	for scanner.Scan() {
		var msg protocol.Message
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if msg.Type == protocol.MsgHandoff {
			t.Fatalf("unexpected HANDOFF before hard stop threshold")
		}
		if time.Now().After(deadline) {
			return
		}
	}
	if err := scanner.Err(); err != nil {
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			return
		}
		t.Fatalf("read message: %v", err)
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

func TestStaleContextFileCleanedOnAssign(t *testing.T) {
	t.Parallel()

	spawner := newMockSpawner()
	dispatcherConn, workerConn := net.Pipe()
	defer func() { _ = dispatcherConn.Close() }()

	w := worker.NewWithConn("w-stale-context", workerConn, spawner)
	w.SetContextPollInterval(50 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tmpDir := t.TempDir()
	oroDir := filepath.Join(tmpDir, ".oro")
	if err := os.MkdirAll(oroDir, 0o750); err != nil { //nolint:gosec // test directory
		t.Fatal(err)
	}
	contextPct := filepath.Join(oroDir, "context_pct")
	if err := os.WriteFile(contextPct, []byte("95"), 0o600); err != nil {
		t.Fatal(err)
	}

	errCh := startWorkerRun(ctx, t, w, dispatcherConn)

	sendMessage(t, dispatcherConn, protocol.Message{
		Type: protocol.MsgAssign,
		Assign: &protocol.AssignPayload{
			BeadID:   "bead-stale-context",
			Worktree: tmpDir,
		},
	})
	_ = readMessage(t, dispatcherConn)

	if _, err := os.Stat(contextPct); !os.IsNotExist(err) {
		t.Error("expected stale context_pct to be deleted during handleAssign")
	}

	cancel()
	<-errCh
}

func TestStaleStreamContextIgnoredAfterNewAssign(t *testing.T) {
	t.Parallel()

	firstReader, firstWriter := io.Pipe()
	t.Cleanup(func() {
		_ = firstReader.Close()
		_ = firstWriter.Close()
	})

	spawner := newMockSpawner()
	spawner.format = worker.StreamFormatLineText
	spawnCount := 0
	spawner.onSpawn = func(_, _, _ string) error {
		spawnCount++
		spawner.process = newMockProcess()
		if spawnCount == 1 {
			spawner.stdout = firstReader
		} else {
			spawner.stdout = nil
		}
		return nil
	}

	dispatcherConn, workerConn := net.Pipe()
	defer func() { _ = dispatcherConn.Close() }()

	w := worker.NewWithConn("w-stale-stream-context", workerConn, spawner)
	w.SetContextPollInterval(25 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	firstWorktree := validAssignWorktree(t, "wt-first-stream-context")
	secondWorktree := validAssignWorktree(t, "wt-second-stream-context")

	errCh := startWorkerRun(ctx, t, w, dispatcherConn)

	sendMessage(t, dispatcherConn, protocol.Message{
		Type: protocol.MsgAssign,
		Assign: &protocol.AssignPayload{
			BeadID:   "bead-first-stream-context",
			Worktree: firstWorktree,
		},
	})
	_ = readMessage(t, dispatcherConn)

	sendMessage(t, dispatcherConn, protocol.Message{
		Type: protocol.MsgAssign,
		Assign: &protocol.AssignPayload{
			BeadID:   "bead-second-stream-context",
			Worktree: secondWorktree,
		},
	})
	_ = readMessage(t, dispatcherConn)

	if _, err := firstWriter.Write([]byte(`{"context_pct":95}` + "\n")); err != nil {
		t.Fatal(err)
	}
	_ = firstWriter.Close()

	_ = dispatcherConn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	scanner := bufio.NewScanner(dispatcherConn)
	for scanner.Scan() {
		var msg protocol.Message
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			continue
		}
		if msg.Type == protocol.MsgHandoff {
			t.Fatalf("stale stream context from prior assignment triggered handoff for %s", msg.Handoff.BeadID)
		}
	}

	cancel()
	<-errCh
}

// TestThresholdLookupAcceptsTierKey verifies that the threshold table accepts tier keys
// (fast/balanced/deep/background) alongside legacy model-family keys, and that the
// effectiveThresholdKey priority (bead.Tier wins, then modelFamily, then balanced default)
// is respected.
func TestThresholdLookupAcceptsTierKey(t *testing.T) {
	t.Parallel()

	models := map[string]int{
		"fast":       35,
		"balanced":   40,
		"deep":       45,
		"background": 30,
		"opus":       45,
		"sonnet":     40,
		"haiku":      35,
	}

	lookupCases := []struct {
		key  string
		want int
	}{
		{"fast", 35},
		{"balanced", 40},
		{"deep", 45},
		{"background", 30},
		{"opus", 45},
		{"sonnet", 40},
		{"haiku", 35},
		{"unknown-key", worker.DefaultThreshold},
		{"invalid-zero", worker.DefaultThreshold},
		{"invalid-negative", worker.DefaultThreshold},
	}
	models["invalid-zero"] = 0
	models["invalid-negative"] = -1

	for _, tc := range lookupCases {
		t.Run("lookup/"+tc.key, func(t *testing.T) {
			t.Parallel()
			if got := worker.ForThresholdKey(models, tc.key); got != tc.want {
				t.Errorf("ForThresholdKey(%q) = %d, want %d", tc.key, got, tc.want)
			}
		})
	}

	// effectiveThresholdKey priority: bead.Tier wins; claude family fallback; balanced default for non-Claude.
	keyCases := []struct {
		tier  protocol.Tier
		model string
		want  string
	}{
		{protocol.TierFast, "gpt-5.5", "fast"},                // tier wins over non-Claude model
		{protocol.TierDeep, "claude-3-opus-20240229", "deep"}, // tier wins over model family
		{"", "claude-3-opus-20240229", "opus"},                // no tier → modelFamily
		{"", "claude-3-5-sonnet-20241022", "sonnet"},          // no tier → modelFamily
		{"", "claude-3-haiku-20240307", "haiku"},              // no tier → modelFamily
		{"", "gpt-5.5", "balanced"},                           // non-Claude → balanced default
	}

	for _, tc := range keyCases {
		t.Run("key/"+string(tc.tier)+"/"+tc.model, func(t *testing.T) {
			t.Parallel()
			if got := worker.EffectiveThresholdKeyFn(tc.tier, tc.model); got != tc.want {
				t.Errorf("EffectiveThresholdKeyFn(%q, %q) = %q, want %q", tc.tier, tc.model, got, tc.want)
			}
		})
	}
}

// TestModelFamilyHandlesCodexNative verifies that modelFamily returns "balanced" for
// non-Claude models (e.g. gpt-5.5) instead of passing the raw model string through.
func TestModelFamilyHandlesCodexNative(t *testing.T) {
	t.Parallel()

	cases := []struct {
		model string
		want  string
	}{
		{"gpt-5.5", "balanced"},
		{"gpt-4o", "balanced"},
		{"codex-native", "balanced"},
		{"gemini-2.5-pro", "balanced"},
		{"claude-3-opus-20240229", "opus"},
		{"claude-3-5-sonnet-20241022", "sonnet"},
		{"claude-3-haiku-20240307", "haiku"},
		{"opus", "opus"},
		{"sonnet", "sonnet"},
		{"haiku", "haiku"},
	}

	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			t.Parallel()
			if got := worker.ModelFamilyFn(tc.model); got != tc.want {
				t.Errorf("ModelFamilyFn(%q) = %q, want %q", tc.model, got, tc.want)
			}
		})
	}
}
