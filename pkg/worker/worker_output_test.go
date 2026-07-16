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

	for _, tc := range []struct {
		name   string
		format worker.StreamFormat
		lines  []string
		want   string
	}{
		{
			name:   "plaintext",
			format: worker.StreamFormatLineText,
			lines:  []string{"ordinary text", "OPENAI_API_KEY=" + sentinel + " MODE=development"},
			want:   "OPENAI_API_KEY=[REDACTED] MODE=development",
		},
		{
			name:   "plaintext post-equals space",
			format: worker.StreamFormatLineText,
			lines:  []string{"ordinary text", "OPENAI_API_KEY = " + sentinel + " MODE=development"},
			want:   "OPENAI_API_KEY = [REDACTED] MODE=development",
		},
		{
			name:   "structured key and value split across deltas",
			format: worker.StreamFormatClaudeJSON,
			lines: []string{
				textDeltaLine("ordinary text\nOPENAI_API_"),
				textDeltaLine("KEY=" + sentinel + " MODE=development\n"),
			},
			want: "OPENAI_API_KEY=[REDACTED] MODE=development",
		},
		{
			name:   "structured post-equals tab",
			format: worker.StreamFormatClaudeJSON,
			lines: []string{
				textDeltaLine("ordinary text\nOPENAI_API_KEY\t="),
				textDeltaLine("\t" + sentinel + " MODE=development\n"),
			},
			want: "OPENAI_API_KEY\t=\t[REDACTED] MODE=development",
		},
		{
			name:   "structured escaped wrapper with internal quote",
			format: worker.StreamFormatClaudeJSON,
			lines: []string{
				textDeltaLine(`ordinary text` + "\n" + `OPENAI_API_KEY=\"` + sentinel + `\\\"still-secret\" MODE=development` + "\n"),
			},
			want: `OPENAI_API_KEY=\"[REDACTED]\" MODE=development`,
		},
		{
			name:   "structured escaped wrapper with internal quote followed by whitespace",
			format: worker.StreamFormatClaudeJSON,
			lines: []string{
				textDeltaLine(`ordinary text` + "\n" + `OPENAI_API_KEY=\"` + sentinel + `\\\" still-secret\" MODE=development` + "\n"),
			},
			want: `OPENAI_API_KEY=\"[REDACTED]\" MODE=development`,
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

			waitForRedactedSessionText(t, w, tc.want)
			logPath := filepath.Join(home, ".oro", "workers", "redacted-output", "output.log")
			log, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatalf("read output log: %v", err)
			}
			for _, output := range []string{w.SessionText(), string(log)} {
				if strings.Contains(output, sentinel) || strings.Contains(output, "still-secret") {
					t.Fatalf("credential leaked in output: %q", output)
				}
				if !strings.Contains(output, tc.want) || !strings.Contains(output, "ordinary text") {
					t.Fatalf("output = %q, want ordinary and redacted text", output)
				}
				if got := strings.Count(output, "[REDACTED]"); got != 1 {
					t.Fatalf("redaction count = %d, want 1: %q", got, output)
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
