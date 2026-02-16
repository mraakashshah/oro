package worker_test

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"oro/pkg/protocol"
	"oro/pkg/worker"
)

func TestSubprocessExitDetection_DuringReview(t *testing.T) {
	t.Parallel()

	t.Run("worker reports subprocess exit state after QG pass", func(t *testing.T) {
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

		w := worker.NewWithConn("w-exit-detect", workerConn, spawner)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		errCh := startWorkerRun(ctx, t, w, dispatcherConn)

		sendMessage(t, dispatcherConn, protocol.Message{
			Type: protocol.MsgAssign,
			Assign: &protocol.AssignPayload{
				BeadID:   "bead-exit-detect",
				Worktree: tmpDir,
			},
		})

		// Read STATUS running
		msg := readMessage(t, dispatcherConn)
		if msg.Type != protocol.MsgStatus {
			t.Fatalf("expected STATUS after ASSIGN, got %s", msg.Type)
		}
		if msg.Status.State != "running" {
			t.Errorf("expected state=running, got %s", msg.Status.State)
		}

		// Subprocess finishes (simulates normal exit)
		_, _ = pw.Write([]byte("implementation done\n"))
		_ = pw.Close()
		close(proc.waitCh)

		// Worker should send STATUS to indicate subprocess exit
		// (not just READY_FOR_REVIEW)
		msg = readMessage(t, dispatcherConn)
		if msg.Type == protocol.MsgReadyForReview {
			t.Fatal("worker sent READY_FOR_REVIEW without STATUS update after subprocess exit")
		}
		if msg.Type != protocol.MsgStatus {
			t.Fatalf("expected STATUS after subprocess exit, got %s", msg.Type)
		}
		if msg.Status.State == "running" {
			t.Error("worker should transition from 'running' state after subprocess exits")
		}
		// Acceptable states: "awaiting_review", "idle", "completed", etc.
		// The important thing is it's NOT still "running"
		t.Logf("Worker transitioned to state: %s", msg.Status.State)

		// Now expect READY_FOR_REVIEW
		msg = readMessage(t, dispatcherConn)
		if msg.Type != protocol.MsgReadyForReview {
			t.Fatalf("expected READY_FOR_REVIEW after subprocess exit, got %s", msg.Type)
		}
		if msg.ReadyForReview.BeadID != "bead-exit-detect" {
			t.Errorf("expected bead_id bead-exit-detect, got %s", msg.ReadyForReview.BeadID)
		}

		cancel()
		<-errCh
	})

	t.Run("worker includes subprocess state in status during review wait", func(t *testing.T) {
		t.Parallel()

		// This test verifies that the worker's status correctly reflects
		// that the subprocess has exited while waiting for review result.

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

		w := worker.NewWithConn("w-status-review", workerConn, spawner)
		// Set fast polling so test runs quickly
		w.SetContextPollInterval(50 * time.Millisecond)
		w.SetHeartbeatInterval(100 * time.Millisecond)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		errCh := startWorkerRun(ctx, t, w, dispatcherConn)

		sendMessage(t, dispatcherConn, protocol.Message{
			Type: protocol.MsgAssign,
			Assign: &protocol.AssignPayload{
				BeadID:   "bead-status-review",
				Worktree: tmpDir,
			},
		})

		_ = readMessage(t, dispatcherConn) // drain STATUS running

		// Subprocess exits normally
		_, _ = pw.Write([]byte("work done\n"))
		_ = pw.Close()
		close(proc.waitCh)

		// Collect messages until we see READY_FOR_REVIEW
		timeout := time.After(2 * time.Second)
		gotReadyForReview := false
		gotStatusAfterExit := false

		for !gotReadyForReview {
			select {
			case <-timeout:
				t.Fatal("timeout waiting for READY_FOR_REVIEW")
			default:
			}

			msg := readMessage(t, dispatcherConn)
			switch msg.Type {
			case protocol.MsgStatus:
				// Check if this STATUS message indicates subprocess has exited
				if msg.Status.State != "running" {
					gotStatusAfterExit = true
					t.Logf("Worker sent STATUS with state: %s", msg.Status.State)
				}
			case protocol.MsgReadyForReview:
				gotReadyForReview = true
			case protocol.MsgHeartbeat:
				// Heartbeats during review wait are okay
				t.Logf("Worker sent heartbeat during review wait")
			}
		}

		if !gotStatusAfterExit {
			t.Error("worker did not send STATUS update after subprocess exit before READY_FOR_REVIEW")
		}

		// Worker is now waiting for REVIEW_RESULT
		// Subprocess has already exited, so worker should be in a non-running state

		// Send approval to complete the flow
		sendMessage(t, dispatcherConn, protocol.Message{
			Type: protocol.MsgReviewResult,
			ReviewResult: &protocol.ReviewResultPayload{
				Verdict: "approved",
			},
		})

		msg := readMessage(t, dispatcherConn)
		if msg.Type != protocol.MsgDone {
			t.Fatalf("expected DONE after approval, got %s", msg.Type)
		}

		cancel()
		<-errCh
	})
}
