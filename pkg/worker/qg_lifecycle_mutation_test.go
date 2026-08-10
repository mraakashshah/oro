package worker //nolint:testpackage // mutation owners exercise unexported QG lifecycle methods.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"oro/pkg/protocol"
)

// TestWorkerQGLifecycleMutationOwners is the deterministic owner for the
// runQGAndReport and runQualityGateWithProgress mutation shards.
func TestWorkerQGLifecycleMutationOwners(t *testing.T) {
	t.Parallel()

	cases := []qgLifecycleCase{
		{
			name:       "passed QG sends READY",
			script:     "#!/bin/sh\nprintf 'quality gate passed\\n'\nexit 0\n",
			wantReady:  true,
			wantOutput: "quality gate passed",
		},
		{
			name:       "failed QG sends DONE",
			script:     "#!/bin/sh\nprintf 'quality gate failed\\n'\nexit 1\n",
			wantOutput: "quality gate failed",
		},
		{
			name:       "missing QG reports error",
			wantOutput: "quality gate script not found",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			worktree := newQGLifecycleWorktree(t, tc.script)
			targetSHA := qgLifecycleGit(t, worktree, "rev-parse", "HEAD")
			evidenceDir := t.TempDir()
			dispatcherConn, workerConn := net.Pipe()
			t.Cleanup(func() {
				_ = dispatcherConn.Close()
				_ = workerConn.Close()
			})

			w := NewWithConn("qg-owner", workerConn, nil)
			w.beadID = "bead-qg-owner"
			w.worktree = worktree
			w.assignmentID = 1
			w.qgEvidenceDir = evidenceDir
			w.targetSHA = targetSHA
			w.targetBranch = "main"

			messages := make(chan qgLifecycleReadResult, 1)
			go func() {
				messages <- readQGLifecycleMessages(dispatcherConn, tc.wantReady)
			}()

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			done := make(chan struct{})
			go func() {
				w.runQGAndReport(ctx)
				close(done)
			}()

			select {
			case <-done:
			case <-ctx.Done():
				t.Fatalf("runQGAndReport did not finish: %v", ctx.Err())
			}

			select {
			case result := <-messages:
				assertQGLifecycleResult(t, result, tc, worktree, targetSHA)
			case <-ctx.Done():
				t.Fatalf("terminal QG message was not received: %v", ctx.Err())
			}
		})
	}

	t.Run("quality gate start error returns without a message", func(t *testing.T) {
		worktree := filepath.Join(t.TempDir(), "missing-worktree")
		w := &Worker{}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		passed, output, err := w.runQualityGateWithProgress(
			ctx,
			worktree,
			filepath.Join(worktree, "missing-quality-gate.sh"),
			true,
			"main",
		)
		if err == nil {
			t.Fatal("expected missing quality gate to return an error")
		}
		if passed || output != "" {
			t.Fatalf("missing quality gate returned passed=%v output=%q", passed, output)
		}
	})
}

type qgLifecycleCase struct {
	name       string
	script     string
	wantReady  bool
	wantOutput string
}

func assertQGLifecycleResult(t *testing.T, result qgLifecycleReadResult, tc qgLifecycleCase, worktree, targetSHA string) {
	t.Helper()
	if result.err != nil {
		t.Fatal(result.err)
	}
	if len(result.messages) < 2 {
		t.Fatal("expected STATUS and terminal QG message")
	}
	statusSeen := false
	for _, msg := range result.messages[:len(result.messages)-1] {
		if msg.Type == protocol.MsgStatus && msg.Status != nil {
			statusSeen = true
			break
		}
	}
	if !statusSeen {
		t.Fatalf("expected STATUS before terminal message, got %v", result.messages)
	}
	terminal := result.messages[len(result.messages)-1]
	if tc.wantReady {
		assertQGLifecycleReady(t, terminal, worktree, targetSHA)
		return
	}
	assertQGLifecycleDone(t, terminal, tc.wantOutput)
}

func assertQGLifecycleReady(t *testing.T, terminal protocol.Message, worktree, targetSHA string) {
	t.Helper()
	ready := terminal.ReadyForReview
	if terminal.Type != protocol.MsgReadyForReview || ready == nil {
		t.Fatalf("expected READY_FOR_REVIEW, got %s", terminal.Type)
	}
	if ready.BeadID != "bead-qg-owner" || ready.WorkerID != "qg-owner" ||
		ready.AssignmentID != 1 || ready.Worktree != worktree || ready.TargetSHA != targetSHA {
		t.Fatalf("READY identity = bead=%q worker=%q assignment=%d worktree=%q target=%q",
			ready.BeadID, ready.WorkerID, ready.AssignmentID, ready.Worktree, ready.TargetSHA)
	}
	if ready.QGEvidence == nil || ready.QGEvidenceRef == nil {
		t.Fatal("READY must include inline QG evidence and evidence reference")
	}
	if ready.QGEvidence.BeadID != ready.BeadID ||
		ready.QGEvidence.WorkerID != ready.WorkerID ||
		ready.QGEvidence.AssignmentID != ready.AssignmentID ||
		ready.QGEvidence.TargetSHA != ready.TargetSHA ||
		ready.QGEvidenceRef.RunID != ready.QGEvidence.RunID ||
		ready.QGEvidenceRef.Path == "" || ready.QGEvidenceRef.SHA256 == "" {
		t.Fatalf("READY evidence identity mismatch: %+v ref=%+v", *ready.QGEvidence, *ready.QGEvidenceRef)
	}
}

func assertQGLifecycleDone(t *testing.T, terminal protocol.Message, wantOutput string) {
	t.Helper()
	if terminal.Type != protocol.MsgDone || terminal.Done == nil {
		t.Fatalf("expected DONE, got %s", terminal.Type)
	}
	if terminal.Done.QualityGatePassed {
		t.Fatal("expected failed QG to send QualityGatePassed=false")
	}
	if !strings.Contains(terminal.Done.QGOutput, wantOutput) {
		t.Fatalf("expected DONE output to contain %q, got %q", wantOutput, terminal.Done.QGOutput)
	}
}

type qgLifecycleReadResult struct {
	messages []protocol.Message
	err      error
}

func readQGLifecycleMessages(conn net.Conn, wantReady bool) qgLifecycleReadResult {
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return qgLifecycleReadResult{err: err}
	}
	scanner := bufio.NewScanner(conn)
	var messages []protocol.Message
	for scanner.Scan() {
		var msg protocol.Message
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			return qgLifecycleReadResult{err: err}
		}
		messages = append(messages, msg)
		if (wantReady && msg.Type == protocol.MsgReadyForReview) ||
			(!wantReady && msg.Type == protocol.MsgDone) {
			return qgLifecycleReadResult{messages: messages}
		}
	}
	if err := scanner.Err(); err != nil {
		return qgLifecycleReadResult{err: err}
	}
	return qgLifecycleReadResult{err: errors.New("QG lifecycle connection closed before terminal message")}
}

func newQGLifecycleWorktree(t *testing.T, script string) string {
	t.Helper()
	dir := t.TempDir()
	qgLifecycleGit(t, dir, "init", "-b", "main")
	qgLifecycleGit(t, dir, "config", "user.email", "qg-owner@oro.test")
	qgLifecycleGit(t, dir, "config", "user.name", "Oro QG Owner")
	if script != "" {
		path := filepath.Join(dir, "quality_gate.sh")
		if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		qgLifecycleGit(t, dir, "add", "quality_gate.sh")
	} else {
		marker := filepath.Join(dir, "fixture.txt")
		if err := os.WriteFile(marker, []byte("fixture\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		qgLifecycleGit(t, dir, "add", "fixture.txt")
	}
	qgLifecycleGit(t, dir, "commit", "-m", "create QG lifecycle fixture")
	return dir
}

func qgLifecycleGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...) //nolint:gosec // fixed test-only git commands
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}
