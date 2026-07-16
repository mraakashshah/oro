package worker_test

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"oro/pkg/protocol"
	"oro/pkg/worker"
)

func TestWorkerOutputRedactsCredentialAssignmentsEndToEnd(t *testing.T) {
	const sentinel = "credential-sentinel"
	const expected = "OPENAI_API_KEY=[REDACTED] MODE=development"

	for _, tc := range []struct {
		name   string
		format worker.StreamFormat
		lines  []string
	}{
		{
			name:   "plaintext",
			format: worker.StreamFormatLineText,
			lines:  []string{"ordinary text", "OPENAI_API_KEY=" + sentinel + " MODE=development"},
		},
		{
			name:   "structured key and value split across deltas",
			format: worker.StreamFormatClaudeJSON,
			lines: []string{
				textDeltaLine("ordinary text\nOPENAI_API_"),
				textDeltaLine("KEY=" + sentinel + " MODE=development\n"),
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)

			spawner := newMockSpawner()
			spawner.format = tc.format
			spawner.stdout = newPipeReader(tc.lines)
			worktree := validAssignWorktree(t, "redacted-output")
			dispatcherConn, workerConn := net.Pipe()
			defer func() { _ = dispatcherConn.Close() }()

			w := worker.NewWithConn("redacted-output", workerConn, spawner)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			errCh := startWorkerRun(ctx, t, w, dispatcherConn)
			defer func() {
				cancel()
				<-errCh
			}()

			sendMessage(t, dispatcherConn, protocol.Message{
				Type:   protocol.MsgAssign,
				Assign: &protocol.AssignPayload{BeadID: "oro-redact", Worktree: worktree},
			})
			if msg := readMessage(t, dispatcherConn); msg.Type != protocol.MsgStatus {
				t.Fatalf("expected STATUS, got %s", msg.Type)
			}

			waitForRedactedSessionText(t, w, expected)
			logPath := filepath.Join(home, ".oro", "workers", "redacted-output", "output.log")
			log, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatalf("read output log: %v", err)
			}
			for _, output := range []string{w.SessionText(), string(log)} {
				if strings.Contains(output, sentinel) {
					t.Fatalf("credential leaked in output: %q", output)
				}
				if !strings.Contains(output, expected) || !strings.Contains(output, "ordinary text") {
					t.Fatalf("output = %q, want ordinary and redacted text", output)
				}
			}
		})
	}
}

func waitForRedactedSessionText(t *testing.T, w *worker.Worker, expected string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(w.SessionText(), expected) {
		if time.Now().After(deadline) {
			t.Fatalf("session text = %q, want %q", w.SessionText(), expected)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
