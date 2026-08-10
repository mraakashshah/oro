package worker //nolint:testpackage // Rotation barrier requires access to worker log state.

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
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
	runAssignmentResetSynchronizesPriorOutputLog(t)
}

func runAssignmentResetSynchronizesPriorOutputLog(t *testing.T) {
	t.Helper()
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

func TestWorkerLogOutputMutationOwners(t *testing.T) {
	t.Run("rotation barrier", runAssignmentResetSynchronizesPriorOutputLog)

	t.Run("open text and close", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("HOME", home)
		w := &Worker{ID: "mutation-log-owner"}
		if err := w.openLogFile(); err != nil {
			t.Fatalf("open log: %v", err)
		}
		w.processOutputTextLine(context.Background(), "OPENAI_API_KEY=secret")
		w.closeLogFile()
		w.closeLogFile()

		data, err := os.ReadFile(filepath.Join(home, ".oro", "workers", w.ID, "output.log"))
		if err != nil {
			t.Fatalf("read log: %v", err)
		}
		if got, want := string(data), "OPENAI_API_KEY=[REDACTED]\n"; got != want {
			t.Fatalf("log = %q, want %q", got, want)
		}
	})

	t.Run("structured line", func(t *testing.T) {
		var sink bytes.Buffer
		w := &Worker{logWriter: bufio.NewWriter(&sink)}
		var sanitizer credentialLineSanitizer
		w.processStructuredStreamLine(
			context.Background(),
			[]byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","id":"toolu_01","input":{}}]}}`),
			&sanitizer,
		)
		if err := w.logWriter.Flush(); err != nil {
			t.Fatalf("flush log: %v", err)
		}
		if got := sink.String(); !strings.Contains(got, "-> Read\n") {
			t.Fatalf("structured log = %q, want Read activity", got)
		}
	})

	t.Run("output stream", func(t *testing.T) {
		var sink bytes.Buffer
		w := &Worker{
			logWriter:    bufio.NewWriter(&sink),
			streamFormat: StreamFormatClaudeJSON,
		}
		stdout := io.NopCloser(strings.NewReader(
			`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","id":"toolu_02","input":{}}]}}` + "\n",
		))
		w.outputWg.Add(1)
		go w.processOutput(context.Background(), stdout, 0)
		outputDone := make(chan struct{})
		go func() {
			w.outputWg.Wait()
			close(outputDone)
		}()
		waitRotation(t, outputDone, "output stream")
		if got := sink.String(); !strings.Contains(got, "-> Bash\n") {
			t.Fatalf("output log = %q, want Bash activity", got)
		}
	})
}

func waitRotation(t *testing.T, done <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("%s did not finish", what)
	}
}
