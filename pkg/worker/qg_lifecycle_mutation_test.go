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
				if tc.wantReady {
					w.mu.Lock()
					pendingOutput := w.pendingQGOutput
					w.mu.Unlock()
					if strings.TrimSpace(pendingOutput) != tc.wantOutput {
						t.Fatalf("pending QG output = %q, want %q", pendingOutput, tc.wantOutput)
					}
				}
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

		passed, output, err := runQualityGateWithTimeout(ctx, t, w, worktree,
			filepath.Join(worktree, "missing-quality-gate.sh"), true, "main")
		if err == nil {
			t.Fatal("expected missing quality gate to return an error")
		}
		if passed || output != "" {
			t.Fatalf("missing quality gate returned passed=%v output=%q", passed, output)
		}
		w.mu.Lock()
		procRecorded := w.proc != nil
		w.mu.Unlock()
		if procRecorded {
			t.Fatal("failed quality gate start recorded a process")
		}
	})

	t.Run("direct quality gate state and argument guards", func(t *testing.T) {
		t.Run("mutation argument and stderr are retained", func(t *testing.T) {
			worktree := t.TempDir()
			scriptPath := writeQGLifecycleScript(t, worktree, "#!/bin/sh\nprintf '%s\\n' \"$@\"\nprintf 'stderr output\\n' >&2\nexit 0\n")
			w := &Worker{}

			passed, output, err := runQualityGateWithTimeout(context.Background(), t, w, worktree, scriptPath, false, "main")
			if err != nil || !passed {
				t.Fatalf("quality gate result = passed=%v output=%q err=%v, want pass", passed, output, err)
			}
			if !strings.Contains(output, "--mutation-testing") {
				t.Fatalf("quality gate args = %q, want --mutation-testing", output)
			}
			if !strings.Contains(output, "stderr output") {
				t.Fatalf("quality gate output = %q, want stderr output", output)
			}
			lockQGLifecycleWorker(t, w)
			procRecorded := w.proc != nil
			w.mu.Unlock()
			if !procRecorded {
				t.Fatal("quality gate process was not recorded")
			}
		})

		t.Run("failed command records exit state and closes channel", func(t *testing.T) {
			worktree := t.TempDir()
			scriptPath := writeQGLifecycleScript(t, worktree, "#!/bin/sh\nprintf 'failed output\\n' >&2\nexit 7\n")
			w := &Worker{}

			passed, output, err := runQualityGateWithTimeout(context.Background(), t, w, worktree, scriptPath, true, "main")
			if err != nil || passed || !strings.Contains(output, "failed output") {
				t.Fatalf("quality gate result = passed=%v output=%q err=%v, want failed output", passed, output, err)
			}
			lockQGLifecycleWorker(t, w)
			exitCode := w.subprocExitCode
			exitErr := w.subprocExitErr
			handleClaimed := w.handleExitClaimed
			exitCh := w.subprocExitCh
			exitClosed := w.subprocExitClosed
			w.mu.Unlock()
			if exitCode != 7 || !strings.Contains(exitErr, "exit status 7") {
				t.Fatalf("exit state = code=%d err=%q, want code 7/status", exitCode, exitErr)
			}
			if !handleClaimed || !exitClosed || !channelClosed(exitCh) {
				t.Fatalf("exit coordination = claimed=%v closed=%v channelClosed=%v, want all true", handleClaimed, exitClosed, channelClosed(exitCh))
			}
		})

		t.Run("start error preserves error and no process", func(t *testing.T) {
			worktree := filepath.Join(t.TempDir(), "missing-worktree")
			w := &Worker{}

			_, _, err := runQualityGateWithTimeout(context.Background(), t, w, worktree, filepath.Join(worktree, "quality_gate.sh"), true, "main")
			if err == nil || !strings.Contains(err.Error(), "chdir") {
				t.Fatalf("start error = %v, want chdir error", err)
			}
			lockQGLifecycleWorker(t, w)
			procRecorded := w.proc != nil
			w.mu.Unlock()
			if procRecorded {
				t.Fatal("failed quality gate start recorded a process")
			}
		})

		t.Run("canceled command returns context error", func(t *testing.T) {
			worktree := t.TempDir()
			scriptPath := writeQGLifecycleScript(t, worktree, "#!/bin/sh\nprintf 'started\\n'\nwhile :; do :; done\n")
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			w := &Worker{}

			passed, output, err := runQualityGateWithTimeout(ctx, t, w, worktree, scriptPath, true, "main")
			if passed || !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled result = passed=%v output=%q err=%v, want context canceled", passed, output, err)
			}
		})
	})

	t.Run("run QG reports each failure boundary", func(t *testing.T) {
		t.Run("missing script sends failed DONE", func(t *testing.T) {
			worktree := t.TempDir()
			w, conn := newQGReportWorker(t, worktree, strings.Repeat("1", 40), t.TempDir())
			result := runQGReportAndRead(context.Background(), t, w, conn)
			assertQGLifecycleDone(t, result.messages[len(result.messages)-1], "quality gate script")
		})

		t.Run("missing git HEAD sends failed DONE", func(t *testing.T) {
			worktree := t.TempDir()
			writeQGLifecycleScript(t, worktree, "#!/bin/sh\nprintf 'QG passed without git\\n'\nexit 0\n")
			w, conn := newQGReportWorker(t, worktree, strings.Repeat("1", 40), t.TempDir())
			result := runQGReportAndRead(context.Background(), t, w, conn)
			assertQGLifecycleDone(t, result.messages[len(result.messages)-1], "post-quality-gate HEAD")
		})

		t.Run("invalid assignment evidence sends failed DONE", func(t *testing.T) {
			worktree := newQGLifecycleWorktree(t, "#!/bin/sh\nprintf 'QG passed\\n'\nexit 0\n")
			targetSHA := qgLifecycleGit(t, worktree, "rev-parse", "HEAD")
			w, conn := newQGReportWorker(t, worktree, targetSHA, "")
			result := runQGReportAndRead(context.Background(), t, w, conn)
			assertQGLifecycleDone(t, result.messages[len(result.messages)-1], "evidence directory")
		})

		t.Run("evidence publish failure sends failed DONE", func(t *testing.T) {
			worktree := newQGLifecycleWorktree(t, "#!/bin/sh\nprintf 'QG passed\\n'\nexit 0\n")
			targetSHA := qgLifecycleGit(t, worktree, "rev-parse", "HEAD")
			badRoot := filepath.Join(t.TempDir(), "evidence-file")
			if err := os.WriteFile(badRoot, []byte("not a directory"), 0o600); err != nil {
				t.Fatal(err)
			}
			w, conn := newQGReportWorker(t, worktree, targetSHA, badRoot)
			result := runQGReportAndRead(context.Background(), t, w, conn)
			assertQGLifecycleDone(t, result.messages[len(result.messages)-1], "publish QG evidence")
		})
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

func writeQGLifecycleScript(t *testing.T, worktree, body string) string {
	t.Helper()
	path := filepath.Join(worktree, "quality_gate.sh")
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func channelClosed(ch <-chan struct{}) bool {
	if ch == nil {
		return false
	}
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

type qgLifecycleCallResult struct {
	passed bool
	output string
	err    error
}

func runQualityGateWithTimeout(ctx context.Context, t *testing.T, w *Worker, worktree, scriptPath string, skipMutation bool, mutationBase string) (bool, string, error) {
	t.Helper()
	callCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	result := make(chan qgLifecycleCallResult, 1)
	go func() {
		passed, output, err := w.runQualityGateWithProgress(callCtx, worktree, scriptPath, skipMutation, mutationBase)
		result <- qgLifecycleCallResult{passed: passed, output: output, err: err}
	}()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case got := <-result:
		return got.passed, got.output, got.err
	case <-timer.C:
		cancel()
		select {
		case got := <-result:
			t.Fatalf("runQualityGateWithProgress did not finish within 5s: %#v", got.err)
		case <-time.After(time.Second):
			t.Fatal("runQualityGateWithProgress did not finish after cancellation")
		}
		return false, "", context.DeadlineExceeded
	}
}

func lockQGLifecycleWorker(t *testing.T, w *Worker) {
	t.Helper()
	if w.mu.TryLock() {
		return
	}
	// The caller has received the buffered result, so the worker invocation and
	// its goroutine have joined. Release exactly one retained post-return
	// mutant lock so cleanup cannot hang, then fail immediately.
	w.mu.Unlock()
	t.Fatal("quality gate lifecycle retained the worker mutex")
}

func newQGReportWorker(t *testing.T, worktree, targetSHA, evidenceDir string) (*Worker, net.Conn) {
	t.Helper()
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
	return w, dispatcherConn
}

func runQGReportAndRead(ctx context.Context, t *testing.T, w *Worker, conn net.Conn) qgLifecycleReadResult {
	t.Helper()
	messages := make(chan qgLifecycleReadResult, 1)
	go func() {
		messages <- readQGLifecycleMessages(conn, false)
	}()
	done := make(chan struct{})
	go func() {
		w.runQGAndReport(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("runQGAndReport did not finish")
	}
	select {
	case result := <-messages:
		if result.err != nil {
			t.Fatal(result.err)
		}
		return result
	case <-time.After(5 * time.Second):
		t.Fatal("runQGAndReport did not send terminal DONE")
		return qgLifecycleReadResult{}
	}
}
