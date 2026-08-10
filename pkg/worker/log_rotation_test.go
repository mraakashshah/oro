package worker //nolint:testpackage // Rotation barrier requires access to worker log state.

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"oro/pkg/protocol"
)

type rotationNoopProcess struct{}

func (rotationNoopProcess) Wait() error { return nil }
func (rotationNoopProcess) Kill() error { return nil }

type rotationBlockingWriter struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
	mu      sync.Mutex
	data    bytes.Buffer
}

func newRotationBlockingWriter() *rotationBlockingWriter {
	return &rotationBlockingWriter{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (w *rotationBlockingWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.started) })
	<-w.release
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.data.Write(p)
}

func (w *rotationBlockingWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.data.String()
}

func TestAssignmentResetSynchronizesPriorOutputLog(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	oldSink := newRotationBlockingWriter()
	w := &Worker{
		ID:        "rotation-worker",
		proc:      rotationNoopProcess{},
		logWriter: bufio.NewWriterSize(oldSink, 1),
		logFile:   nil,
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	firstDone := make(chan struct{})
	go func() {
		w.processOutputTextLine(ctx, "markerA")
		close(firstDone)
	}()

	select {
	case <-oldSink.started:
	case <-time.After(time.Second):
		t.Fatal("assignment A did not reach its blocked log write")
	}

	secondDone := make(chan struct{})
	go func() {
		w.resetForNewAssignment(&protocol.AssignPayload{
			BeadID:    "bead-b",
			Worktree:  t.TempDir(),
			TargetSHA: "target-b",
		}, WorkerExecutionContext{})
		close(secondDone)
	}()

	select {
	case <-secondDone:
		t.Fatal("assignment B rotated the log before assignment A was released")
	case <-time.After(50 * time.Millisecond):
	}

	close(oldSink.release)
	waitRotation(t, firstDone, "assignment A output")
	waitRotation(t, secondDone, "assignment B reset")

	w.processOutputTextLine(ctx, "markerB")
	w.closeLogFile()

	if got := oldSink.String(); got != "markerA\n" {
		t.Fatalf("old assignment log = %q, want %q", got, "markerA\n")
	}
	data, err := os.ReadFile(filepath.Join(home, ".oro", "workers", w.ID, "output.log"))
	if err != nil {
		t.Fatalf("read assignment B log: %v", err)
	}
	if got := string(data); got != "markerB\n" {
		t.Fatalf("new assignment log = %q, want %q", got, "markerB\n")
	}
	w.closeLogFile()

	nilWriterWorker := &Worker{}
	nilWriterWorker.closeLogFile()
	badHome := filepath.Join(home, "not-a-directory")
	if err := os.WriteFile(badHome, []byte("file"), 0o600); err != nil {
		t.Fatalf("write invalid HOME sentinel: %v", err)
	}
	t.Setenv("HOME", badHome)
	failedOpenWorker := &Worker{ID: "failed-open"}
	if err := failedOpenWorker.openLogFile(); err == nil {
		t.Fatal("openLogFile unexpectedly succeeded under a file HOME")
	}
	if failedOpenWorker.logFile != nil || failedOpenWorker.logWriter != nil {
		t.Fatal("failed open retained log state")
	}
}

func waitRotation(t *testing.T, done <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("%s did not finish", what)
	}
}
