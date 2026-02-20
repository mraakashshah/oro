package worker

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"oro/pkg/memory"
	"oro/pkg/protocol"
)

// StreamingSpawner abstracts claude -p invocation for testing.
// Spawn returns the process, stdout reader, stdin writer (both may be nil), and any error.
type StreamingSpawner interface {
	Spawn(ctx context.Context, model string, prompt string, workdir string) (Process, io.ReadCloser, io.WriteCloser, error)
}

// Process abstracts a running subprocess.
type Process interface {
	Wait() error
	Kill() error
}

// DefaultContextPollInterval controls how often the context watcher polls <worktree>/.oro/context_pct.
const DefaultContextPollInterval = 5 * time.Second

// DefaultHeartbeatInterval controls the minimum time between periodic heartbeats
// sent to the dispatcher. Must be well under the dispatcher's HeartbeatTimeout (45s).
const DefaultHeartbeatInterval = 10 * time.Second

// DefaultThreshold is the fallback context percentage when thresholds.json is missing or model unknown.
const DefaultThreshold = 50

// thresholds holds per-model context percentage thresholds loaded from <worktree>/.oro/thresholds.json.
type thresholds struct {
	models map[string]int
}

// For returns the threshold for the given model, falling back to DefaultThreshold.
func (t thresholds) For(model string) int {
	if v, ok := t.models[model]; ok {
		return v
	}
	return DefaultThreshold
}

// loadThresholds reads per-model thresholds from <dir>/thresholds.json.
// Returns defaults if the file is missing or unreadable.
func loadThresholds(dir string) thresholds {
	data, err := os.ReadFile(filepath.Join(dir, "thresholds.json")) //nolint:gosec // path constructed internally
	if err != nil {
		return thresholds{}
	}
	var models map[string]int
	if err := json.Unmarshal(data, &models); err != nil {
		return thresholds{}
	}
	return thresholds{models: models}
}

// reconnectBaseInterval is the base retry interval for reconnection.
const reconnectBaseInterval = 2 * time.Second

// reconnectJitter is the maximum jitter added to the reconnect interval.
const reconnectJitter = 500 * time.Millisecond

// maxBufferedMessages is the maximum number of messages buffered during reconnection.
const maxBufferedMessages = 100

// Worker is the Oro worker agent. It holds a UDS connection to the Dispatcher,
// manages a claude -p subprocess, and monitors context usage.
type Worker struct {
	ID                  string
	conn                net.Conn
	proc                Process
	beadID              string
	worktree            string
	model               string
	compacted           bool
	mu                  sync.Mutex
	spawner             StreamingSpawner
	socketPath          string // for reconnection
	buffer              *MessageBuffer
	disconnected        bool
	contextPollInterval time.Duration
	reconnectInterval   time.Duration // base retry interval for reconnection
	memStore            *memory.Store
	sessionText         strings.Builder
	outputWg            sync.WaitGroup // tracks processOutput goroutine completion
	reconnectDialHook   func(net.Conn) // test hook: called after dial, before sendMessage
	pendingQGOutput     string         // QG output stored while awaiting review result
	isEpicDecomposition bool           // true when current assignment is an epic decomposition
	subprocExitCh       chan struct{}  // closed when subprocess exits
	subprocExitClosed   bool           // true if subprocExitCh has been closed
	handleExitClaimed   bool           // true if a handler claimed subprocess exit handling
	subprocKilledByUs   bool           // true if we intentionally killed the subprocess
	connWriteMu         sync.Mutex     // serializes conn writes so heartbeat deadlines don't leak
	heartbeatInterval   time.Duration  // minimum time between periodic heartbeats
	logFile             *os.File       // per-worker output log file at ~/.oro/workers/<ID>/output.log
	logWriter           *bufio.Writer  // buffered writer for logFile to prevent blocking
}

// New creates a Worker that connects to the Dispatcher at socketPath.
func New(id, socketPath string, spawner StreamingSpawner) (*Worker, error) {
	conn, err := net.Dial("unix", socketPath) //nolint:noctx // UDS connect is instant, no context needed
	if err != nil {
		return nil, fmt.Errorf("connect to dispatcher: %w", err)
	}
	return &Worker{
		ID:                  id,
		conn:                conn,
		spawner:             spawner,
		socketPath:          socketPath,
		buffer:              NewMessageBuffer(maxBufferedMessages),
		contextPollInterval: DefaultContextPollInterval,
		reconnectInterval:   reconnectBaseInterval,
	}, nil
}

// NewWithConn creates a Worker with a pre-established connection (for testing).
//
//oro:testonly
func NewWithConn(id string, conn net.Conn, spawner StreamingSpawner) *Worker {
	return &Worker{
		ID:                  id,
		conn:                conn,
		spawner:             spawner,
		buffer:              NewMessageBuffer(maxBufferedMessages),
		contextPollInterval: DefaultContextPollInterval,
		reconnectInterval:   reconnectBaseInterval,
	}
}

// SetContextPollInterval overrides the context watcher poll interval (for testing).
//
//oro:testonly
func (w *Worker) SetContextPollInterval(d time.Duration) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.contextPollInterval = d
}

// SetHeartbeatInterval overrides the minimum time between periodic heartbeats (for testing).
//
//oro:testonly
func (w *Worker) SetHeartbeatInterval(d time.Duration) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.heartbeatInterval = d
}

// SetReconnectInterval overrides the base reconnect retry interval (for testing).
//
//oro:testonly
func (w *Worker) SetReconnectInterval(d time.Duration) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.reconnectInterval = d
}

// SetReconnectDialHook sets a function called after each successful dial during
// reconnection, before the RECONNECT message is sent. For testing only.
//
//oro:testonly
func (w *Worker) SetReconnectDialHook(fn func(net.Conn)) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.reconnectDialHook = fn
}

// SetMemoryStore attaches a memory store to the worker for memory extraction.
// When set, [MEMORY] markers in subprocess stdout are captured in real-time,
// and implicit patterns are extracted on handoff/completion.
func (w *Worker) SetMemoryStore(s *memory.Store) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.memStore = s
}

// SessionText returns the accumulated subprocess output text. Thread-safe.
//
//oro:testonly
func (w *Worker) SessionText() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.sessionText.String()
}

// Run is the main event loop. It reads messages from the UDS connection and
// dispatches them. It returns nil on clean shutdown or context cancellation.
func (w *Worker) Run(ctx context.Context) error {
	msgCh, errCh := w.readMessages()

	// Announce ourselves so the dispatcher can register this worker.
	if err := w.SendHeartbeat(ctx, 0); err != nil {
		return fmt.Errorf("send initial heartbeat: %w", err)
	}

	for {
		select {
		case <-ctx.Done():
			w.killProc()
			return nil

		case msg := <-msgCh:
			if done, err := w.handleMessage(ctx, msg); err != nil || done {
				if err != nil {
					return fmt.Errorf("handle message: %w", err)
				}
				return nil
			}

		case err := <-errCh:
			if handleErr := w.handleConnectionError(ctx, err); handleErr != nil {
				return fmt.Errorf("handle connection error: %w", handleErr)
			}
			return nil
		}
	}
}

// readMessages starts a goroutine that reads line-delimited JSON from the
// connection and sends parsed messages on msgCh. When the scanner stops
// (EOF or error), the cause is sent on errCh.
func (w *Worker) readMessages() (msgs <-chan protocol.Message, readErr <-chan error) {
	scanner := bufio.NewScanner(w.conn)
	// Configure scanner to accept messages up to MaxMessageSize (1MB).
	// Default scanner max is 64KB which is too small for large payloads.
	scanner.Buffer(make([]byte, 0, 64*1024), protocol.MaxMessageSize)
	msgCh := make(chan protocol.Message)
	errCh := make(chan error, 1)

	go func() {
		for scanner.Scan() {
			var msg protocol.Message
			if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
				continue // skip malformed messages
			}
			msgCh <- msg
		}
		if err := scanner.Err(); err != nil {
			errCh <- err
		} else {
			errCh <- fmt.Errorf("connection closed")
		}
	}()

	return msgCh, errCh
}

// handleConnectionError processes a connection drop. It returns nil when
// the context is already cancelled (clean shutdown), returns the original
// error when reconnection is impossible, or attempts to reconnect and
// restarts the event loop.
func (w *Worker) handleConnectionError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return nil //nolint:nilerr // context cancelled = clean shutdown, swallow connection error
	}
	if w.socketPath == "" {
		// No socketPath means we can't reconnect (test with net.Pipe)
		return fmt.Errorf("connection error (no reconnect possible): %w", err)
	}
	if reconnErr := w.reconnect(ctx); reconnErr != nil {
		return reconnErr
	}
	// After reconnect, restart the read loop with the new connection
	return w.Run(ctx)
}

// handleMessage processes a single incoming message. Returns (true, nil) on shutdown.
func (w *Worker) handleMessage(ctx context.Context, msg protocol.Message) (bool, error) {
	switch msg.Type {
	case protocol.MsgAssign:
		return false, w.handleAssign(ctx, msg)
	case protocol.MsgShutdown:
		w.killProc()
		return true, nil
	case protocol.MsgPrepareShutdown:
		return w.handlePrepareShutdown(ctx, msg)
	case protocol.MsgReviewResult:
		return false, w.handleReviewResult(ctx, msg)
	default:
		// Unknown message type, ignore
		return false, nil
	}
}

// handleReviewResult processes a REVIEW_RESULT message from the dispatcher.
// On approval, it sends DONE with the stored quality gate output.
func (w *Worker) handleReviewResult(ctx context.Context, msg protocol.Message) error {
	if msg.ReviewResult == nil {
		return nil
	}

	if msg.ReviewResult.Verdict == "approved" {
		w.mu.Lock()
		qgOutput := w.pendingQGOutput
		w.pendingQGOutput = ""
		w.mu.Unlock()

		return w.SendDone(ctx, true, qgOutput)
	}

	// Rejected or unknown verdict — the dispatcher handles rejection by
	// sending a new ASSIGN with feedback, so nothing to do here.
	return nil
}

// handlePrepareShutdown processes a PREPARE_SHUTDOWN message by saving context
// via a HANDOFF message, then sending SHUTDOWN_APPROVED, and finally killing
// the subprocess. If the payload is nil, it falls back to hard shutdown.
func (w *Worker) handlePrepareShutdown(ctx context.Context, msg protocol.Message) (bool, error) {
	if msg.PrepareShutdown == nil {
		// No payload — fall back to hard shutdown
		w.killProc()
		return true, nil
	}

	// Save context by sending a HANDOFF with learnings/decisions
	_ = w.SendHandoff(ctx)

	// Signal that we're ready to be shut down
	_ = w.SendShutdownApproved(ctx)

	// Kill the subprocess
	w.killProc()

	return true, nil
}

// handleAssign processes an ASSIGN message: stores state, spawns subprocess,
// starts context watcher, and pipes stdout through memory extraction.
func (w *Worker) handleAssign(ctx context.Context, msg protocol.Message) error {
	if msg.Assign == nil {
		return fmt.Errorf("assign message missing payload")
	}

	// Validate the payload before processing.
	if err := msg.Assign.Validate(); err != nil {
		return fmt.Errorf("invalid assign payload: %w", err)
	}

	// Kill any existing subprocess from a previous assignment to prevent zombie leaks.
	w.killProc()

	w.mu.Lock()
	w.beadID = msg.Assign.BeadID
	w.worktree = msg.Assign.Worktree
	w.sessionText.Reset()
	w.pendingQGOutput = ""
	w.isEpicDecomposition = msg.Assign.IsEpicDecomposition
	w.mu.Unlock()

	// Truncate log file for new assignment (best-effort; continue if fails)
	w.closeLogFile()
	_ = w.openLogFile()

	prompt, model := BuildAssignPrompt(msg.Assign)
	proc, stdout, _, err := w.spawner.Spawn(ctx, model, prompt, msg.Assign.Worktree)
	if err != nil {
		return fmt.Errorf("spawn claude: %w", err)
	}

	w.mu.Lock()
	w.proc = proc
	w.model = model
	w.compacted = false
	w.subprocExitCh = make(chan struct{})
	w.subprocExitClosed = false
	w.handleExitClaimed = false
	w.subprocKilledByUs = false
	w.mu.Unlock()

	// Pipe subprocess stdout through memory marker extraction.
	if stdout != nil {
		w.outputWg.Add(1)
		go w.processOutput(ctx, stdout)
	}

	// Send STATUS running
	if err := w.SendStatus(ctx, "running", ""); err != nil {
		return fmt.Errorf("send status: %w", err)
	}

	// Start subprocess exit monitor
	go w.monitorSubprocessExit(proc)

	// Start context file watcher (also monitors subprocess health)
	go w.watchContext(ctx)

	go w.awaitSubprocessAndReport(ctx) // wait for exit, run QG, send DONE

	return nil
}

// BuildAssignPrompt constructs the prompt and resolves the model from an ASSIGN payload.
// When IsEpicDecomposition is true, it returns a planning-only prompt via
// BuildEpicDecompositionPrompt (no TDD/QG/worktree sections). Otherwise it
// returns the standard 12-section worker prompt.
func BuildAssignPrompt(a *protocol.AssignPayload) (prompt, model string) {
	switch {
	case a.IsEpicDecomposition:
		prompt = BuildEpicDecompositionPrompt(EpicPromptParams{
			BeadID:      a.BeadID,
			Title:       a.Title,
			Description: a.Description,
		})
	case a.Title != "":
		prompt = AssemblePrompt(PromptParams{
			BeadID:             a.BeadID,
			Title:              a.Title,
			Description:        a.Description,
			AcceptanceCriteria: a.AcceptanceCriteria,
			MemoryContext:      a.MemoryContext,
			CodeSearchContext:  a.CodeSearchContext,
			WorktreePath:       a.Worktree,
			Model:              a.Model,
			Attempt:            a.Attempt,
			Feedback:           a.Feedback,
		})
	default:
		prompt = BuildPrompt(a.BeadID, a.Worktree, a.MemoryContext)
	}
	model = a.Model
	if model == "" {
		model = protocol.DefaultModel
	}
	return prompt, model
}

// monitorSubprocessExit waits for the subprocess to exit and signals the exit channel.
func (w *Worker) monitorSubprocessExit(proc Process) {
	_ = proc.Wait()
	w.mu.Lock()
	if w.subprocExitCh != nil && !w.subprocExitClosed {
		close(w.subprocExitCh)
		w.subprocExitClosed = true
	}
	w.mu.Unlock()
}

// awaitSubprocessAndReport waits for the subprocess to exit, ensures stdout
// processing is complete, runs the quality gate, and either sends DONE (on
// failure) or READY_FOR_REVIEW (on pass) so the dispatcher can run an ops
// review before the worker signals completion.
func (w *Worker) awaitSubprocessAndReport(ctx context.Context) {
	// Wait for subprocess to exit (signaled by subprocExitCh closing)
	w.mu.Lock()
	exitCh := w.subprocExitCh
	pollInterval := w.contextPollInterval
	w.mu.Unlock()

	if exitCh != nil {
		<-exitCh
	}

	// Give watchContext a chance to detect unexpected death.
	// Wait 2x the poll interval before claiming (watchContext needs 2 ticks to detect),
	// but cap at 250ms to avoid slowing down normal subprocess completion.
	delay := 2 * pollInterval
	if delay > 250*time.Millisecond {
		delay = 250 * time.Millisecond
	}
	timer := time.NewTimer(delay)
	select {
	case <-timer.C:
	case <-ctx.Done():
		timer.Stop()
		return
	}

	// Claim responsibility for handling the subprocess exit.
	// CAS: read old value, conditionally set, check if we won the race.
	w.mu.Lock()
	alreadyClaimed := w.handleExitClaimed
	if !alreadyClaimed {
		w.handleExitClaimed = true
	}
	w.mu.Unlock()

	// If checkSubprocessHealth already claimed the exit, bail without sending DONE.
	if alreadyClaimed {
		return
	}

	// Wait for processOutput to finish so all stdout is captured.
	w.outputWg.Wait()

	// Don't run QG or send DONE if context was cancelled (shutdown).
	if ctx.Err() != nil {
		return
	}

	// Epic decomposition assignments skip the quality gate entirely.
	// The subprocess only plans/decomposes; there is no code to test or lint.
	w.mu.Lock()
	isEpicDecomp := w.isEpicDecomposition
	w.mu.Unlock()

	if isEpicDecomp {
		_ = w.SendDone(ctx, true, "")
		return
	}

	w.runQGAndReport(ctx)
}

// runQGAndReport runs the quality gate script and sends DONE or READY_FOR_REVIEW
// depending on the result. It is called by awaitSubprocessAndReport for non-epic
// assignments after the subprocess exits.
func (w *Worker) runQGAndReport(ctx context.Context) {
	// Send STATUS update to indicate subprocess has exited and worker is
	// transitioning to quality gate phase. This ensures the dispatcher knows
	// the subprocess is no longer running.
	_ = w.SendStatus(ctx, "awaiting_review", "")

	w.mu.Lock()
	wt := w.worktree
	w.mu.Unlock()

	passed, output, err := RunQualityGate(ctx, wt)
	if err != nil {
		// Script missing or cannot start — report as failed with error detail.
		_ = w.SendDone(ctx, false, err.Error())
		return
	}

	if !passed {
		// QG failed — send DONE immediately so the dispatcher can re-assign.
		_ = w.SendDone(ctx, false, output)
		return
	}

	// QG passed — store the output and send READY_FOR_REVIEW.
	// The worker waits for the dispatcher to send back a REVIEW_RESULT
	// (approved) or a new ASSIGN (rejected with feedback).
	w.mu.Lock()
	w.pendingQGOutput = output
	w.mu.Unlock()

	_ = w.SendReadyForReview(ctx)
}

// processOutput reads subprocess stdout line by line, accumulates session text
// for later implicit extraction, and extracts [MEMORY] markers in real-time.
// When stdout closes (subprocess exits), it extracts implicit memories so that
// learnings from failed attempts are persisted before the dispatcher re-assigns.
func (w *Worker) processOutput(ctx context.Context, stdout io.ReadCloser) {
	defer w.outputWg.Done()
	defer func() { _ = stdout.Close() }()

	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		line := scanner.Text()

		w.mu.Lock()
		w.sessionText.WriteString(line)
		w.sessionText.WriteString("\n")
		store := w.memStore
		workerID := w.ID
		beadID := w.beadID
		logWriter := w.logWriter
		w.mu.Unlock()

		// Tee line to log file (best-effort; don't block on I/O errors).
		// NOTE: No Flush() here — flushing every line blocks on disk I/O and
		// deadlocks the pipe when the buffer fills (root cause of oro-jyvo).
		// closeLogFile() flushes once when the subprocess exits.
		if logWriter != nil {
			_, _ = logWriter.WriteString(line)
			_, _ = logWriter.WriteString("\n")
		}

		// Extract [MEMORY] markers in real-time.
		if store != nil {
			if params := memory.ParseMarker(line); params != nil {
				params.WorkerID = workerID
				params.BeadID = beadID
				_, _ = store.Insert(ctx, *params) // best-effort; don't block on errors
			}
		}
	}

	// Flush log buffer once after all lines are processed (not per-line).
	// Per-line Flush() was the root cause of oro-jyvo: it blocked on disk I/O
	// and deadlocked the pipe when the OS buffer filled.
	w.mu.Lock()
	if w.logWriter != nil {
		_ = w.logWriter.Flush()
	}
	w.mu.Unlock()

	// Subprocess stdout closed — extract implicit memories so learnings from
	// failed attempts (e.g. QG failure) are persisted regardless of outcome.
	w.extractImplicitMemories(ctx)
}

// extractImplicitMemories runs ExtractImplicit on accumulated session text
// and inserts results into the memory store. Called on handoff/completion.
func (w *Worker) extractImplicitMemories(ctx context.Context) {
	w.mu.Lock()
	store := w.memStore
	text := w.sessionText.String()
	workerID := w.ID
	beadID := w.beadID
	w.mu.Unlock()

	if store == nil || text == "" {
		return
	}

	results := memory.ExtractImplicit(text)
	for i := range results {
		results[i].WorkerID = workerID
		results[i].BeadID = beadID
		_, _ = store.Insert(ctx, results[i]) // best-effort
	}
}

// openLogFile creates or truncates ~/.oro/workers/<ID>/output.log and
// opens it for writing. If directory creation or file open fails, returns
// error but caller should continue without logging (best-effort).
func (w *Worker) openLogFile() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("get home dir: %w", err)
	}

	logDir := filepath.Join(home, ".oro", "workers", w.ID)
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return fmt.Errorf("create log dir: %w", err)
	}

	logPath := filepath.Join(logDir, "output.log")
	// O_TRUNC ensures we start fresh on each assignment
	// #nosec G304 -- logPath is constructed from home dir and worker ID, not user input
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}

	w.logFile = f
	w.logWriter = bufio.NewWriter(f)
	return nil
}

// closeLogFile flushes and closes the log file. Safe to call multiple times.
func (w *Worker) closeLogFile() {
	if w.logWriter != nil {
		_ = w.logWriter.Flush()
		w.logWriter = nil
	}
	if w.logFile != nil {
		_ = w.logFile.Close()
		w.logFile = nil
	}
}

// BuildPrompt constructs the prompt string for claude -p.
// It includes the instruction to run quality_gate.sh before completing.
// If memoryContext is non-empty, it is appended as a section so the worker
// benefits from cross-session memories retrieved by the dispatcher.
func BuildPrompt(beadID, worktree, memoryContext string) string {
	base := fmt.Sprintf("Execute bead %s in worktree %s. Before completing, run ./quality_gate.sh and ensure it passes.", beadID, worktree)
	if memoryContext == "" {
		return base
	}
	return base + "\n\n" + memoryContext
}

// watchContext polls .oro/context_pct in the current worktree and triggers
// a two-stage response when context usage exceeds the model-specific threshold:
//  1. First breach: send /compact to subprocess stdin, create .oro/compacted flag
//  2. Second breach: send HANDOFF and kill subprocess (ralph handoff)
//
// It also monitors subprocess health: if the subprocess dies unexpectedly
// (subprocess exits and remains unclaimed for one poll interval), send DONE(false) with error.
func (w *Worker) watchContext(ctx context.Context) {
	w.mu.Lock()
	interval := w.contextPollInterval
	wt := w.worktree
	model := w.model
	hbInterval := w.heartbeatInterval
	w.mu.Unlock()
	if hbInterval == 0 {
		hbInterval = DefaultHeartbeatInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Load per-model thresholds from worktree root.
	th := loadThresholds(wt)
	threshold := th.For(modelFamily(model))

	var subprocExitDetectedAt time.Time
	lastHeartbeat := time.Now() // start counting from now; first heartbeat after hbInterval

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Keep dispatcher alive — send periodic heartbeats so the
			// dispatcher doesn't declare us dead while claude -p runs.
			if time.Since(lastHeartbeat) >= hbInterval {
				w.trySendHeartbeat(ctx)
				lastHeartbeat = time.Now()
			}

			// Check for unexpected subprocess death
			if w.checkSubprocessHealth(ctx, &subprocExitDetectedAt) {
				return
			}

			w.mu.Lock()
			wt = w.worktree
			w.mu.Unlock()

			// Check context percentage and handle threshold breaches
			if w.handleContextThreshold(ctx, wt, threshold) {
				return
			}
		}
	}
}

// handleContextThreshold checks context percentage and handles threshold breaches.
// Returns true if handoff was triggered (caller should return).
func (w *Worker) handleContextThreshold(ctx context.Context, wt string, threshold int) bool {
	if wt == "" {
		return false
	}

	pctPath := filepath.Join(wt, protocol.OroDir, "context_pct")
	data, err := os.ReadFile(pctPath) //nolint:gosec // path is constructed internally, not user input
	if err != nil {
		return false
	}

	pct, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pct <= threshold {
		return false
	}

	w.mu.Lock()
	alreadyCompacted := w.compacted
	w.mu.Unlock()

	if !alreadyCompacted {
		// First breach: wait for Claude's auto-compaction to reduce context.
		w.mu.Lock()
		w.compacted = true
		w.mu.Unlock()
		oroDir := filepath.Join(wt, protocol.OroDir)
		_ = os.MkdirAll(oroDir, 0o700) //nolint:gosec // runtime directory
		_ = os.WriteFile(filepath.Join(oroDir, "compacted"), []byte("1"), 0o600)
		return false
	}

	// Second breach: auto-compact didn't help enough — handoff
	_ = w.SendHandoff(ctx)
	w.killProc()
	return true
}

// checkSubprocessHealth checks if the subprocess has died unexpectedly.
// Returns true if unexpected death was detected and DONE was sent.
func (w *Worker) checkSubprocessHealth(ctx context.Context, detectedAt *time.Time) bool {
	w.mu.Lock()
	exitClosed := w.subprocExitClosed
	claimed := w.handleExitClaimed
	w.mu.Unlock()

	if !exitClosed || claimed {
		return false
	}

	// Subprocess has exited but hasn't been claimed yet
	if detectedAt.IsZero() {
		// First time detecting this - record the time
		*detectedAt = time.Now()
		return false
	}

	// Subprocess was dead on previous tick and still not claimed.
	// This is unexpected death - report it.
	w.mu.Lock()
	if !w.handleExitClaimed {
		w.handleExitClaimed = true
		w.mu.Unlock()
		_ = w.SendDone(ctx, false, "subprocess died unexpectedly")
		return true
	}
	w.mu.Unlock()
	return false
}

// modelFamily extracts the model family name (opus, sonnet, haiku) from a full model ID.
func modelFamily(model string) string {
	lower := strings.ToLower(model)
	for _, family := range []string{"opus", "sonnet", "haiku"} {
		if strings.Contains(lower, family) {
			return family
		}
	}
	return model
}

// killProc kills the current subprocess if one is running.
func (w *Worker) killProc() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.proc != nil {
		_ = w.proc.Kill()
		w.proc = nil
		w.subprocKilledByUs = true
	}
}

// reconnect attempts to re-establish the UDS connection to the Dispatcher.
// It retries every 2s with ±500ms jitter until success or context cancellation.
// The subprocess is NOT killed during reconnection.
func (w *Worker) reconnect(ctx context.Context) error {
	w.mu.Lock()
	w.disconnected = true
	w.mu.Unlock()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("worker reconnect: %w", ctx.Err())
		default:
		}

		w.mu.Lock()
		baseInterval := w.reconnectInterval
		w.mu.Unlock()
		jitter := time.Duration(rand.Int64N(int64(2*reconnectJitter))) - reconnectJitter //nolint:gosec // jitter doesn't need crypto rand
		wait := baseInterval + jitter

		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("worker reconnect: %w", ctx.Err())
		case <-timer.C:
		}

		conn, err := net.Dial("unix", w.socketPath) //nolint:noctx // UDS reconnect is instant
		if err != nil {
			continue
		}

		w.mu.Lock()
		w.conn = conn
		w.disconnected = false
		beadID := w.beadID
		state := "running"
		if w.proc == nil {
			state = "idle"
		}
		hook := w.reconnectDialHook
		w.mu.Unlock()

		if hook != nil {
			hook(conn)
		}

		// Send RECONNECT with buffered events
		buffered := w.buffer.Drain()
		reconnMsg := protocol.Message{
			Type: protocol.MsgReconnect,
			Reconnect: &protocol.ReconnectPayload{
				WorkerID:       w.ID,
				BeadID:         beadID,
				State:          state,
				BufferedEvents: buffered,
			},
		}
		if err := w.sendMessage(reconnMsg); err != nil {
			continue
		}

		return nil
	}
}

// sendMessage encodes and writes a protocol.Message as line-delimited JSON.
// If disconnected, the message is buffered instead.
func (w *Worker) sendMessage(msg protocol.Message) error {
	w.mu.Lock()
	disconnected := w.disconnected
	w.mu.Unlock()

	if disconnected {
		w.buffer.Add(msg)
		return nil
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal message: %w", err)
	}
	data = append(data, '\n')

	w.mu.Lock()
	conn := w.conn
	w.mu.Unlock()

	w.connWriteMu.Lock()
	_, err = conn.Write(data)
	w.connWriteMu.Unlock()

	if err != nil {
		return fmt.Errorf("write message: %w", err)
	}
	return nil
}

// trySendHeartbeat sends a best-effort heartbeat with a short write deadline.
// The connWriteMu ensures the deadline+write+clear is atomic with respect to
// other writers, preventing deadline leakage. If the write doesn't complete
// within 200ms (e.g. blocked net.Pipe in tests), it gives up rather than
// stalling the context watcher. Production UDS writes complete in microseconds.
func (w *Worker) trySendHeartbeat(_ context.Context) {
	w.mu.Lock()
	beadID := w.beadID
	conn := w.conn
	disconnected := w.disconnected
	w.mu.Unlock()

	if disconnected {
		return
	}

	data, err := json.Marshal(protocol.Message{
		Type: protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{
			BeadID:   beadID,
			WorkerID: w.ID,
		},
	})
	if err != nil {
		return
	}
	data = append(data, '\n')

	w.connWriteMu.Lock()
	_ = conn.SetWriteDeadline(time.Now().Add(200 * time.Millisecond))
	_, _ = conn.Write(data)
	_ = conn.SetWriteDeadline(time.Time{})
	w.connWriteMu.Unlock()
}

// SendHeartbeat sends a HEARTBEAT message to the Dispatcher.
func (w *Worker) SendHeartbeat(_ context.Context, contextPct int) error {
	w.mu.Lock()
	beadID := w.beadID
	w.mu.Unlock()

	return w.sendMessage(protocol.Message{
		Type: protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{
			BeadID:     beadID,
			WorkerID:   w.ID,
			ContextPct: contextPct,
		},
	})
}

// SendStatus sends a STATUS message to the Dispatcher.
func (w *Worker) SendStatus(_ context.Context, state, result string) error {
	w.mu.Lock()
	beadID := w.beadID
	w.mu.Unlock()

	return w.sendMessage(protocol.Message{
		Type: protocol.MsgStatus,
		Status: &protocol.StatusPayload{
			BeadID:   beadID,
			WorkerID: w.ID,
			State:    state,
			Result:   result,
		},
	})
}

// SendDone sends a DONE message to the Dispatcher with the quality gate result.
// Before sending, it extracts implicit memories from accumulated session text.
func (w *Worker) SendDone(ctx context.Context, qualityGatePassed bool, qgOutput string) error {
	w.extractImplicitMemories(ctx)

	w.mu.Lock()
	beadID := w.beadID
	w.mu.Unlock()

	return w.sendMessage(protocol.Message{
		Type: protocol.MsgDone,
		Done: &protocol.DonePayload{
			BeadID:            beadID,
			WorkerID:          w.ID,
			QualityGatePassed: qualityGatePassed,
			QGOutput:          qgOutput,
		},
	})
}

// SendHandoff sends a HANDOFF message to the Dispatcher.
// Before sending, it extracts implicit memories from accumulated session text
// and reads typed context files from .oro/ in the worktree to populate the
// HandoffPayload with learnings, decisions, files modified, and a context
// summary for cross-session memory persistence.
func (w *Worker) SendHandoff(ctx context.Context) error {
	w.extractImplicitMemories(ctx)
	w.mu.Lock()
	beadID := w.beadID
	worktree := w.worktree
	w.mu.Unlock()

	payload := protocol.HandoffPayload{
		BeadID:   beadID,
		WorkerID: w.ID,
	}

	// Populate context from .oro/ files (best-effort; missing files are not errors)
	if worktree != "" {
		oroDir := filepath.Join(worktree, protocol.OroDir)
		payload.Learnings = readJSONStringSlice(filepath.Join(oroDir, "learnings.json"))
		payload.Decisions = readJSONStringSlice(filepath.Join(oroDir, "decisions.json"))
		payload.FilesModified = readJSONStringSlice(filepath.Join(oroDir, "files_modified.json"))
		payload.ContextSummary = readFileString(filepath.Join(oroDir, "context_summary.txt"))
	}

	return w.sendMessage(protocol.Message{
		Type:    protocol.MsgHandoff,
		Handoff: &payload,
	})
}

// readJSONStringSlice reads a JSON file containing a []string and returns it.
// Returns nil on any error (file not found, invalid JSON, etc.).
func readJSONStringSlice(path string) []string {
	data, err := os.ReadFile(path) //nolint:gosec // path is constructed internally, not user input
	if err != nil {
		return nil
	}
	var result []string
	if err := json.Unmarshal(data, &result); err != nil {
		return nil
	}
	return result
}

// readFileString reads a file and returns its trimmed contents as a string.
// Returns empty string on any error.
func readFileString(path string) string {
	data, err := os.ReadFile(path) //nolint:gosec // path is constructed internally, not user input
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// SendShutdownApproved sends a SHUTDOWN_APPROVED message to the Dispatcher,
// indicating that the worker has saved its context and is ready to be killed.
func (w *Worker) SendShutdownApproved(_ context.Context) error {
	return w.sendMessage(protocol.Message{
		Type: protocol.MsgShutdownApproved,
		ShutdownApproved: &protocol.ShutdownApprovedPayload{
			WorkerID: w.ID,
		},
	})
}

// SendReadyForReview sends a READY_FOR_REVIEW message to the Dispatcher.
//
//oro:testonly
func (w *Worker) SendReadyForReview(_ context.Context) error {
	w.mu.Lock()
	beadID := w.beadID
	w.mu.Unlock()

	return w.sendMessage(protocol.Message{
		Type: protocol.MsgReadyForReview,
		ReadyForReview: &protocol.ReadyForReviewPayload{
			BeadID:   beadID,
			WorkerID: w.ID,
		},
	})
}

// RunQualityGate executes ./quality_gate.sh in the given worktree directory.
// It returns (true, output, nil) if the script exits 0, (false, output, nil) if
// it exits non-zero, and (false, "", err) if the script cannot be found or started.
// Output contains combined stdout and stderr from the script.
func RunQualityGate(ctx context.Context, worktree string) (passed bool, output string, err error) {
	scriptPath := filepath.Join(worktree, "quality_gate.sh")
	if _, err := os.Stat(scriptPath); err != nil {
		// Agent may have deleted quality_gate.sh — try restoring from git.
		restoreCmd := exec.CommandContext(ctx, "git", "checkout", "HEAD", "--", "quality_gate.sh")
		restoreCmd.Dir = worktree
		if restoreErr := restoreCmd.Run(); restoreErr != nil {
			return false, "", fmt.Errorf("quality gate script not found: %w (restore failed: %w)", err, restoreErr)
		}
		// Verify restoration succeeded.
		if _, err := os.Stat(scriptPath); err != nil {
			return false, "", fmt.Errorf("quality gate script not found after restore: %w", err)
		}
	}

	cmd := exec.CommandContext(ctx, "bash", scriptPath) //nolint:gosec // script path constructed from worktree, not user input
	cmd.Dir = worktree

	out, err := cmd.CombinedOutput()
	output = string(out)
	if err != nil {
		// Non-zero exit is not an error — it means the gate failed
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return false, output, nil
		}
		return false, output, fmt.Errorf("run quality gate: %w", err)
	}
	return true, output, nil
}

// ClaudeSpawner is the production StreamingSpawner that invokes `claude -p`.
type ClaudeSpawner struct{}

// buildClaudeArgs constructs the argument slice for the claude command.
// When both ORO_HOME and ORO_PROJECT env vars are set, it appends
// --add-dir and --settings flags to point claude at the shared oro config.
func buildClaudeArgs(model, prompt string) []string {
	args := []string{"-p", prompt, "--model", model}

	oroHome := os.Getenv("ORO_HOME")
	oroProject := os.Getenv("ORO_PROJECT")
	if oroHome != "" && oroProject != "" {
		args = append(args, "--add-dir", oroHome, "--settings", filepath.Join(oroHome, "projects", oroProject, "settings.json"))
	}

	return args
}

// buildClaudeEnv returns the environment slice for the claude subprocess.
// Always builds an explicit env (never nil) so that CLAUDECODE is stripped
// unconditionally — a nil Env would cause exec.Cmd to inherit the parent
// environment, leaking CLAUDECODE and triggering the nested-session guard.
// When ORO_PROJECT is set, also appends CLAUDE_CODE_ADDITIONAL_DIRECTORIES_CLAUDE_MD=1
// so claude picks up CLAUDE.md from directories added via --add-dir.
func buildClaudeEnv() []string {
	env := make([]string, 0, len(os.Environ())+1)
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "CLAUDECODE=") ||
			strings.HasPrefix(e, "CLAUDE_CODE_ADDITIONAL_DIRECTORIES") {
			continue
		}
		env = append(env, e)
	}
	if os.Getenv("ORO_PROJECT") != "" {
		env = append(env, "CLAUDE_CODE_ADDITIONAL_DIRECTORIES_CLAUDE_MD=1")
	}
	return env
}

// Spawn starts a `claude -p` subprocess with the given prompt and working directory.
// Returns the process, stdout reader, stdin writer (nil), and any error.
//
// Stdin is NOT piped. Claude Code uses Ink (a React-for-CLI framework) which
// calls setRawMode on process.stdin at startup. When stdin is a pipe, setRawMode
// blocks indefinitely, causing `claude -p` to hang with zero output. Connecting
// stdin to /dev/null avoids this. The trade-off: sendCompact() becomes a no-op,
// so context overflow triggers handoff instead of in-place compaction.
func (s *ClaudeSpawner) Spawn(ctx context.Context, model, prompt, workdir string) (Process, io.ReadCloser, io.WriteCloser, error) {
	args := buildClaudeArgs(model, prompt)
	cmd := exec.CommandContext(ctx, "claude", args...) //nolint:gosec // args are constructed internally by buildClaudeArgs, not user input
	cmd.Dir = workdir
	cmd.Stderr = os.Stderr
	cmd.Env = buildClaudeEnv()

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, nil, nil, fmt.Errorf("start claude: %w", err)
	}
	return &cmdProcess{cmd: cmd}, stdoutPipe, nil, nil
}

// cmdProcess wraps *exec.Cmd to implement the Process interface.
type cmdProcess struct {
	cmd *exec.Cmd
}

// Wait blocks until the subprocess exits.
func (p *cmdProcess) Wait() error {
	if err := p.cmd.Wait(); err != nil {
		return fmt.Errorf("claude process wait: %w", err)
	}
	return nil
}

// Kill terminates the subprocess immediately.
func (p *cmdProcess) Kill() error {
	if p.cmd.Process == nil {
		return nil
	}
	if err := p.cmd.Process.Kill(); err != nil {
		return fmt.Errorf("kill claude process: %w", err)
	}
	return nil
}
