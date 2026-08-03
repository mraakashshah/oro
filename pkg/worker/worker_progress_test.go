package worker_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"oro/pkg/protocol"
	"oro/pkg/worker"
)

func TestWorkerEmitsProgressWhileSubprocessRuns(t *testing.T) {
	dispatcherConn, workerConn := net.Pipe()
	defer dispatcherConn.Close()
	defer workerConn.Close()

	proc := newMockProcess()
	spawner := &mockSpawner{process: proc}
	w := worker.NewWithConn("w-progress", workerConn, spawner)
	w.SetContextPollInterval(80 * time.Millisecond)
	w.SetHeartbeatInterval(time.Hour)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := startWorkerRun(ctx, t, w, dispatcherConn)

	worktree := validAssignWorktree(t, "progress")
	sendMessage(t, dispatcherConn, protocol.Message{
		Type: protocol.MsgAssign,
		Assign: &protocol.AssignPayload{
			BeadID:   "bead-progress",
			Worktree: worktree,
		},
	})

	decoder := json.NewDecoder(dispatcherConn)
	firstStatusAt, firstStatus := readNextStatus(t, dispatcherConn, decoder, 2*time.Second)
	if firstStatus.Status.State != "running" {
		t.Fatalf("first STATUS state = %q, want running", firstStatus.Status.State)
	}

	progressAt, progressStatus := readNextStatus(t, dispatcherConn, decoder, 2*time.Second)
	if progressStatus.Status.State != "running_progress" {
		t.Fatalf("progress STATUS state = %q, want running_progress", progressStatus.Status.State)
	}
	if progressAt.Sub(firstStatusAt) < 80*time.Millisecond {
		t.Fatalf("progress emitted after %v, want at least configured interval", progressAt.Sub(firstStatusAt))
	}
	if !strings.Contains(progressStatus.Status.Result, "command_age_ms") {
		t.Fatalf("progress result missing command age: %q", progressStatus.Status.Result)
	}
	if !strings.Contains(progressStatus.Status.Result, "last_output_age_ms") {
		t.Fatalf("progress result missing last output age: %q", progressStatus.Status.Result)
	}

	cancel()
	proc.Kill()
	<-errCh
}

func TestProgressTickWriteDoesNotBlockContextWatcher(t *testing.T) {
	workerConn := newDeadlineBlockingConn()
	defer workerConn.Close()

	proc := newMockProcess()
	spawner := &mockSpawner{process: proc}
	w := worker.NewWithConn("w-progress-deadline", workerConn, spawner)
	w.SetContextPollInterval(30 * time.Millisecond)
	w.SetHeartbeatInterval(70 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- w.Run(ctx) }()
	readNextWorkerMessage(t, workerConn, 2*time.Second, func(msg protocol.Message) bool {
		return msg.Type == protocol.MsgHeartbeat
	})

	worktree := validAssignWorktree(t, "progress-deadline")
	workerConn.QueueReadMessage(t, protocol.Message{
		Type: protocol.MsgAssign,
		Assign: &protocol.AssignPayload{
			BeadID:        "bead-progress-deadline",
			Worktree:      worktree,
			AssignmentID:  1,
			QGEvidenceDir: t.TempDir(),
			TargetSHA:     "test-target-sha",
			Generation:    1,
			ActorRole:     "execution_worker",
		},
	})

	firstStatus := readNextWorkerMessage(t, workerConn, 2*time.Second, func(msg protocol.Message) bool {
		return msg.Type == protocol.MsgStatus
	})
	if firstStatus.Status.State != "running" {
		t.Fatalf("first STATUS state = %q, want running", firstStatus.Status.State)
	}

	workerConn.BlockWrites()

	msg := readNextWorkerMessage(t, workerConn, 2*time.Second, func(msg protocol.Message) bool {
		return msg.Type == protocol.MsgHeartbeat
	})
	if msg.Heartbeat == nil {
		t.Fatal("heartbeat payload is nil")
	}

	cancel()
	proc.Kill()
	<-errCh
}

func TestWorkerEmitsProgressDuringQualityGate(t *testing.T) {
	dispatcherConn, workerConn := net.Pipe()
	defer dispatcherConn.Close()
	defer workerConn.Close()

	proc := newMockProcess()
	spawner := &mockSpawner{process: proc, stdout: io.NopCloser(strings.NewReader("assignment done\n"))}
	w := worker.NewWithConn("w-qg-progress", workerConn, spawner)
	w.SetContextPollInterval(40 * time.Millisecond)
	w.SetHeartbeatInterval(time.Hour)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := startWorkerRun(ctx, t, w, dispatcherConn)

	worktree := validAssignWorktree(t, "qg-progress")
	writeQualityGateScript(t, worktree, "#!/bin/sh\necho qg-start\nsleep 0.25\necho qg-done\nexit 0\n")
	sendMessage(t, dispatcherConn, protocol.Message{
		Type: protocol.MsgAssign,
		Assign: &protocol.AssignPayload{
			BeadID:   "bead-qg-progress",
			Worktree: worktree,
		},
	})

	decoder := json.NewDecoder(dispatcherConn)
	_, running := readNextStatus(t, dispatcherConn, decoder, 2*time.Second)
	if running.Status.State != "running" {
		t.Fatalf("first STATUS state = %q, want running", running.Status.State)
	}

	close(proc.waitCh)

	_, awaitingReview := readNextStatus(t, dispatcherConn, decoder, 2*time.Second)
	if awaitingReview.Status.State != "awaiting_review" {
		t.Fatalf("second STATUS state = %q, want awaiting_review", awaitingReview.Status.State)
	}

	_, progress := readNextStatus(t, dispatcherConn, decoder, 2*time.Second)
	if progress.Status.State != "running_progress" {
		t.Fatalf("quality gate STATUS state = %q, want running_progress", progress.Status.State)
	}
	assertProgressAges(t, progress)

	cancel()
	<-errCh
}

func TestWorkerEmitsProgressDuringCodexPlainTextSubprocess(t *testing.T) {
	dispatcherConn, workerConn := net.Pipe()
	defer dispatcherConn.Close()
	defer workerConn.Close()

	claudeSpawner := newMockSpawner()
	codexProc := newMockProcess()
	codexSpawner := &mockSpawner{
		process: codexProc,
		stdout:  io.NopCloser(strings.NewReader("codex plain text output\n")),
		format:  worker.StreamFormatLineText,
	}
	w := worker.NewWithConnAndRuntimeSpawners("w-codex-progress", workerConn, claudeSpawner, codexSpawner)
	w.SetContextPollInterval(40 * time.Millisecond)
	w.SetHeartbeatInterval(time.Hour)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := startWorkerRun(ctx, t, w, dispatcherConn)

	worktree := validAssignWorktree(t, "codex-progress")
	sendMessage(t, dispatcherConn, protocol.Message{
		Type: protocol.MsgAssign,
		Assign: &protocol.AssignPayload{
			BeadID:    "bead-codex-progress",
			Worktree:  worktree,
			Runtime:   "codex",
			Model:     "gpt-5.5",
			Reasoning: "medium",
		},
	})

	decoder := json.NewDecoder(dispatcherConn)
	_, running := readNextStatus(t, dispatcherConn, decoder, 2*time.Second)
	if running.Status.State != "running" {
		t.Fatalf("first STATUS state = %q, want running", running.Status.State)
	}

	_, progress := readNextStatus(t, dispatcherConn, decoder, 2*time.Second)
	if progress.Status.State != "running_progress" {
		t.Fatalf("codex STATUS state = %q, want running_progress", progress.Status.State)
	}
	assertProgressAges(t, progress)

	calls := codexSpawner.SpawnCalls()
	if len(calls) != 1 {
		t.Fatalf("codex spawn calls = %d, want 1", len(calls))
	}
	if calls[0].Reasoning != "medium" || calls[0].Workdir != worktree {
		t.Fatalf("codex spawn call = %+v, want reasoning medium workdir %s", calls[0], worktree)
	}
	if len(claudeSpawner.SpawnCalls()) != 0 {
		t.Fatalf("claude spawn calls = %d, want 0", len(claudeSpawner.SpawnCalls()))
	}

	cancel()
	codexProc.Kill()
	<-errCh
}

func readNextWorkerMessage(t *testing.T, conn *deadlineBlockingConn, timeout time.Duration, matches func(protocol.Message) bool) protocol.Message {
	t.Helper()

	deadline := time.After(timeout)
	for {
		select {
		case <-deadline:
			t.Fatal("timeout waiting for worker message")
		case data := <-conn.Writes():
			var msg protocol.Message
			if err := json.Unmarshal(data, &msg); err != nil {
				t.Fatalf("unmarshal worker message: %v", err)
			}
			if matches(msg) {
				return msg
			}
		}
	}
}

func readNextStatus(t *testing.T, conn net.Conn, decoder *json.Decoder, timeout time.Duration) (time.Time, protocol.Message) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for {
		if err := conn.SetReadDeadline(deadline); err != nil {
			t.Fatalf("set read deadline: %v", err)
		}
		var msg protocol.Message
		if err := decoder.Decode(&msg); err != nil {
			t.Fatalf("decode message: %v", err)
		}
		if msg.Type == protocol.MsgStatus {
			return time.Now(), msg
		}
	}
}

func assertProgressAges(t *testing.T, msg protocol.Message) {
	t.Helper()

	if !strings.Contains(msg.Status.Result, "command_age_ms") {
		t.Fatalf("progress result missing command age: %q", msg.Status.Result)
	}
	if !strings.Contains(msg.Status.Result, "last_output_age_ms") {
		t.Fatalf("progress result missing last output age: %q", msg.Status.Result)
	}
}

func writeQualityGateScript(t *testing.T, worktree, body string) {
	t.Helper()

	script := filepath.Join(worktree, "quality_gate.sh")
	if err := os.WriteFile(script, []byte(body), 0o600); err != nil {
		t.Fatalf("write quality gate script: %v", err)
	}
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatalf("chmod quality gate script: %v", err)
	}
}

type timeoutError struct{}

func (timeoutError) Error() string   { return "write deadline exceeded" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

type deadlineBlockingConn struct {
	mu            sync.Mutex
	readBuf       bytes.Buffer
	readNotify    chan struct{}
	writes        chan []byte
	closed        chan struct{}
	blockWrites   bool
	writeDeadline time.Time
}

func newDeadlineBlockingConn() *deadlineBlockingConn {
	return &deadlineBlockingConn{
		readNotify: make(chan struct{}, 1),
		writes:     make(chan []byte, 32),
		closed:     make(chan struct{}),
	}
}

func (c *deadlineBlockingConn) QueueReadMessage(t *testing.T, msg protocol.Message) {
	t.Helper()

	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal queued message: %v", err)
	}
	data = append(data, '\n')

	c.mu.Lock()
	_, _ = c.readBuf.Write(data)
	c.mu.Unlock()

	select {
	case c.readNotify <- struct{}{}:
	default:
	}
}

func (c *deadlineBlockingConn) Writes() <-chan []byte {
	return c.writes
}

func (c *deadlineBlockingConn) BlockWrites() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.blockWrites = true
}

func (c *deadlineBlockingConn) Read(p []byte) (int, error) {
	for {
		c.mu.Lock()
		if c.readBuf.Len() > 0 {
			n, err := c.readBuf.Read(p)
			c.mu.Unlock()
			return n, err
		}
		c.mu.Unlock()

		select {
		case <-c.closed:
			return 0, io.EOF
		case <-c.readNotify:
		}
	}
}

func (c *deadlineBlockingConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	block := c.blockWrites
	deadline := c.writeDeadline
	c.mu.Unlock()

	if !block {
		c.writes <- append([]byte(nil), p...)
		return len(p), nil
	}

	if deadline.IsZero() {
		<-c.closed
		return 0, net.ErrClosed
	}
	c.writes <- append([]byte(nil), p...)

	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()

	select {
	case <-c.closed:
		return 0, net.ErrClosed
	case <-timer.C:
		return 0, timeoutError{}
	}
}

func (c *deadlineBlockingConn) Close() error {
	select {
	case <-c.closed:
	default:
		close(c.closed)
	}
	return nil
}

func (c *deadlineBlockingConn) LocalAddr() net.Addr  { return dummyAddr("local") }
func (c *deadlineBlockingConn) RemoteAddr() net.Addr { return dummyAddr("remote") }
func (c *deadlineBlockingConn) SetDeadline(t time.Time) error {
	_ = c.SetReadDeadline(t)
	return c.SetWriteDeadline(t)
}
func (c *deadlineBlockingConn) SetReadDeadline(_ time.Time) error { return nil }
func (c *deadlineBlockingConn) SetWriteDeadline(t time.Time) error {
	c.mu.Lock()
	c.writeDeadline = t
	c.mu.Unlock()
	return nil
}

type dummyAddr string

func (a dummyAddr) Network() string { return string(a) }
func (a dummyAddr) String() string  { return string(a) }

var (
	_ net.Conn  = (*deadlineBlockingConn)(nil)
	_ net.Error = timeoutError{}
)
