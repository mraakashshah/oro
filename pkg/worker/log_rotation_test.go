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
	"sync/atomic"
	"testing"
	"time"

	"oro/pkg/cards"
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

type rotationTrackingReadCloser struct {
	io.Reader
	closed chan struct{}
	once   sync.Once
}

type logScoreExtractSpawner struct {
	calls  int
	output string
}

func (s *logScoreExtractSpawner) Spawn(_ context.Context, _, _ string) (io.ReadCloser, error) {
	s.calls++
	return io.NopCloser(strings.NewReader(s.output)), nil
}

type logScoreMemorySink struct {
	calls int
}

func (s *logScoreMemorySink) AppendLearningPending(_ context.Context, _ string, _ cards.CardCandidate) (int64, error) {
	s.calls++
	return int64(s.calls), nil
}

func (r *rotationTrackingReadCloser) Close() error {
	r.once.Do(func() { close(r.closed) })
	return nil
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
	waitRotationRecoverLogMu(t, w, secondDone, "assignment B reset")
	assertLogMuReusable(t, w, "assignment B reset")

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
		assertLogMuReusable(t, w, "openLogFile")
		w.processPlaintextLine(context.Background(), "OPENAI_API_KEY=secret")
		assertLogMuReusable(t, w, "processPlaintextLine")
		if got, want := w.SessionText(), "OPENAI_API_KEY=[REDACTED]\n"; got != want {
			t.Fatalf("session text = %q, want %q", got, want)
		}
		if w.lastSubprocOutputAt.IsZero() {
			t.Fatal("lastSubprocOutputAt was not updated")
		}
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
		runStructured := func(line []byte, what string) {
			done := make(chan struct{})
			go func() {
				w.processStructuredStreamLine(context.Background(), line, &sanitizer)
				close(done)
			}()
			waitRotationRecoverLogMu(t, w, done, what)
			assertLogMuReusable(t, w, what)
		}
		runStructured(
			[]byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","id":"toolu_01","input":{}}]}}`),
			"structured activity",
		)
		runStructured(
			[]byte(`{"type":"content_block_delta","delta":{"type":"text_delta","text":"structured payload\n"}}`),
			"structured text",
		)
		if err := w.logWriter.Flush(); err != nil {
			t.Fatalf("flush log: %v", err)
		}
		if got := sink.String(); !strings.Contains(got, "-> Read\n") {
			t.Fatalf("structured log = %q, want Read activity", got)
		}
		if got := sink.String(); !strings.Contains(got, "structured payload\n") {
			t.Fatalf("structured log = %q, want forwarded text", got)
		}
	})

	t.Run("output stream", func(t *testing.T) {
		var sink bytes.Buffer
		w := &Worker{
			logWriter:            bufio.NewWriter(&sink),
			streamFormat:         StreamFormatClaudeJSON,
			assignmentGeneration: 42,
		}
		stdout := &rotationTrackingReadCloser{
			Reader: strings.NewReader(
				`{"event":"turn_end","context_pct":42}` + "\n" +
					`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","id":"toolu_02","input":{}}]}}` + "\n",
			),
			closed: make(chan struct{}),
		}
		w.outputWg.Add(1)
		go w.processOutput(context.Background(), stdout, 42)
		outputDone := make(chan struct{})
		go func() {
			w.outputWg.Wait()
			close(outputDone)
		}()
		waitRotation(t, outputDone, "output stream")
		waitRotationClosed(t, stdout.closed, "output stream stdout")
		assertLogMuReusable(t, w, "output stream")
		if got := sink.String(); !strings.Contains(got, "-> Bash\n") {
			t.Fatalf("output log = %q, want Bash activity", got)
		}
		if got := atomic.LoadInt32(&w.streamContextPct); got != 42 {
			t.Fatalf("stream context pct = %d, want 42", got)
		}
	})

	t.Run("structured EOF flushes trailing text", func(t *testing.T) {
		var sink bytes.Buffer
		extract := &logScoreExtractSpawner{
			output: "[MEMORY] type=lesson: EOF extraction reached\n",
		}
		memory := &logScoreMemorySink{}
		w := &Worker{
			beadID:         "log-score-bead",
			logWriter:      bufio.NewWriter(&sink),
			streamFormat:   StreamFormatClaudeJSON,
			memStore:       memory,
			extractSpawner: extract,
		}
		stdout := &rotationTrackingReadCloser{
			Reader: strings.NewReader(
				`{"type":"content_block_delta","delta":{"type":"text_delta","text":"trailing text"}}` + "\n",
			),
			closed: make(chan struct{}),
		}
		w.outputWg.Add(1)
		go w.processOutput(context.Background(), stdout, 0)
		outputDone := make(chan struct{})
		go func() {
			w.outputWg.Wait()
			close(outputDone)
		}()
		waitRotation(t, outputDone, "structured trailing output")
		waitRotationClosed(t, stdout.closed, "structured trailing stdout")
		assertLogMuReusable(t, w, "structured trailing output")
		if got := sink.String(); got != "trailing text\n" {
			t.Fatalf("trailing structured log = %q, want %q", got, "trailing text\n")
		}
		if extract.calls != 1 || memory.calls != 1 {
			t.Fatalf("EOF extraction calls = spawner:%d store:%d, want 1 each", extract.calls, memory.calls)
		}
	})

	t.Run("nil writer and file still drain stdout", func(t *testing.T) {
		w := &Worker{streamFormat: StreamFormatLineText}
		stdout := &rotationTrackingReadCloser{
			Reader: strings.NewReader("plain output without a log\n"),
			closed: make(chan struct{}),
		}
		w.outputWg.Add(1)
		go w.processOutput(context.Background(), stdout, 0)
		outputDone := make(chan struct{})
		go func() {
			w.outputWg.Wait()
			close(outputDone)
		}()
		waitRotation(t, outputDone, "nil-writer output")
		waitRotationClosed(t, stdout.closed, "nil-writer stdout")
		assertLogMuReusable(t, w, "nil-writer output")
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

func waitRotationClosed(t *testing.T, closed <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatalf("%s was not closed", what)
	}
}

func waitRotationRecoverLogMu(t *testing.T, w *Worker, done <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-done:
		return
	case <-time.After(time.Second):
		if w.logMu.TryLock() {
			w.logMu.Unlock()
			t.Fatalf("%s did not finish", what)
		}
		w.logMu.Unlock()
		waitRotation(t, done, what+" after releasing retained logMu")
		t.Fatalf("%s retained logMu", what)
	}
}

func assertLogMuReusable(t *testing.T, w *Worker, what string) {
	t.Helper()
	if !w.logMu.TryLock() {
		w.logMu.Unlock()
		t.Fatalf("%s retained logMu", what)
	}
	w.logMu.Unlock()
}
