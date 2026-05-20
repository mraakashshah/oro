package worker_test

import (
	"context"
	"encoding/json"
	"net"
	"strings"
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
