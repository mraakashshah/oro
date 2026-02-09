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

// SubprocessSpawner abstracts claude -p invocation for testing.
// Spawn returns the process, a reader for its stdout (may be nil), and any error.
type SubprocessSpawner interface {
	Spawn(ctx context.Context, model string, prompt string, workdir string) (Process, io.ReadCloser, error)
}

// Process abstracts a running subprocess.
type Process interface {
	Wait() error
	Kill() error
}

// DefaultContextPollInterval controls how often the context watcher polls .oro/context_pct.
const DefaultContextPollInterval = 5 * time.Second

// DefaultThreshold is the fallback context percentage when thresholds.json is missing or model unknown.
const DefaultThreshold = 50

// Thresholds holds per-model context percentage thresholds loaded from .oro/thresholds.json.
type Thresholds struct {
	models map[string]int
}

// For returns the threshold for the given model, falling back to DefaultThreshold.
func (t Thresholds) For(model string) int {
	if v, ok := t.models[model]; ok {
		return v
	}
	return DefaultThreshold
}

// LoadThresholds reads per-model thresholds from <dir>/thresholds.json.
// Returns defaults if the file is missing or unreadable.
func LoadThresholds(dir string) Thresholds {
	data, err := os.ReadFile(filepath.Join(dir, "thresholds.json")) //nolint:gosec // path constructed internally
	if err != nil {
		return Thresholds{}
	}
	var models map[string]int
	if err := json.Unmarshal(data, &models); err != nil {
		return Thresholds{}
	}
	return Thresholds{models: models}
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
	mu                  sync.Mutex
	spawner             SubprocessSpawner
	socketPath          string // for reconnection
	buffer              *MessageBuffer
	disconnected        bool
	contextPollInterval time.Duration
	memStore            *memory.Store
	sessionText         strings.Builder
}

// New creates a Worker that connects to the Dispatcher at socketPath.
func New(id, socketPath string, spawner SubprocessSpawner) (*Worker, error) {
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
	}, nil
}

// NewWithConn creates a Worker with a pre-established connection (for testing).
func NewWithConn(id string, conn net.Conn, spawner SubprocessSpawner) *Worker {
	return &Worker{
		ID:                  id,
		conn:                conn,
		spawner:             spawner,
		buffer:              NewMessageBuffer(maxBufferedMessages),
		contextPollInterval: DefaultContextPollInterval,
	}
}

// SetContextPollInterval overrides the context watcher poll interval (for testing).
func (w *Worker) SetContextPollInterval(d time.Duration) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.contextPollInterval = d
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
func (w *Worker) SessionText() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.sessionText.String()
}

// Run is the main event loop. It reads messages from the UDS connection and
// dispatches them. It returns nil on clean shutdown or context cancellation.
func (w *Worker) Run(ctx context.Context) error {
	msgCh, errCh := w.readMessages()

	for {
		select {
		case <-ctx.Done():
			w.killProc()
			return nil

		case msg := <-msgCh:
			if done, err := w.handleMessage(ctx, msg); err != nil || done {
				return err
			}

		case err := <-errCh:
			return w.handleConnectionError(ctx, err)
		}
	}
}

// readMessages starts a goroutine that reads line-delimited JSON from the
// connection and sends parsed messages on msgCh. When the scanner stops
// (EOF or error), the cause is sent on errCh.
func (w *Worker) readMessages() (msgs <-chan protocol.Message, readErr <-chan error) {
	scanner := bufio.NewScanner(w.conn)
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
		return err
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
	default:
		// Unknown message type, ignore
		return false, nil
	}
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
		return fmt.Errorf("ASSIGN message missing payload")
	}

	w.mu.Lock()
	w.beadID = msg.Assign.BeadID
	w.worktree = msg.Assign.Worktree
	w.sessionText.Reset()
	w.mu.Unlock()

	prompt := BuildPrompt(msg.Assign.BeadID, msg.Assign.Worktree, msg.Assign.MemoryContext)
	model := msg.Assign.Model
	if model == "" {
		model = "claude-opus-4-6"
	}
	proc, stdout, err := w.spawner.Spawn(ctx, model, prompt, msg.Assign.Worktree)
	if err != nil {
		return fmt.Errorf("spawn claude: %w", err)
	}

	w.mu.Lock()
	w.proc = proc
	w.mu.Unlock()

	// Pipe subprocess stdout through memory marker extraction.
	if stdout != nil {
		go w.processOutput(ctx, stdout)
	}

	// Send STATUS running
	if err := w.SendStatus(ctx, "running", ""); err != nil {
		return err
	}

	// Start context file watcher
	go w.watchContext(ctx)

	// Wait for subprocess completion in background
	go func() {
		_ = proc.Wait()
	}()

	return nil
}

// processOutput reads subprocess stdout line by line, accumulates session text
// for later implicit extraction, and extracts [MEMORY] markers in real-time.
func (w *Worker) processOutput(ctx context.Context, stdout io.ReadCloser) {
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
		w.mu.Unlock()

		// Extract [MEMORY] markers in real-time.
		if store != nil {
			if params := memory.ParseMarker(line); params != nil {
				params.WorkerID = workerID
				params.BeadID = beadID
				_, _ = store.Insert(ctx, *params) // best-effort; don't block on errors
			}
		}
	}
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
// handoff if context usage exceeds the threshold.
func (w *Worker) watchContext(ctx context.Context) {
	w.mu.Lock()
	interval := w.contextPollInterval
	w.mu.Unlock()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.mu.Lock()
			wt := w.worktree
			w.mu.Unlock()

			if wt == "" {
				continue
			}

			pctPath := filepath.Join(wt, ".oro", "context_pct")
			data, err := os.ReadFile(pctPath) //nolint:gosec // path is constructed internally, not user input
			if err != nil {
				// File doesn't exist or unreadable — not an error
				continue
			}

			pct, err := strconv.Atoi(strings.TrimSpace(string(data)))
			if err != nil {
				continue
			}

			if pct > DefaultThreshold {
				_ = w.SendHandoff(ctx)
				w.killProc()
				return
			}
		}
	}
}

// killProc kills the current subprocess if one is running.
func (w *Worker) killProc() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.proc != nil {
		_ = w.proc.Kill()
		w.proc = nil
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

		jitter := time.Duration(rand.Int64N(int64(2*reconnectJitter))) - reconnectJitter //nolint:gosec // jitter doesn't need crypto rand
		wait := reconnectBaseInterval + jitter

		select {
		case <-ctx.Done():
			return fmt.Errorf("worker reconnect: %w", ctx.Err())
		case <-time.After(wait):
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
		w.mu.Unlock()

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

	if _, err := conn.Write(data); err != nil {
		return fmt.Errorf("write message: %w", err)
	}
	return nil
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
func (w *Worker) SendDone(ctx context.Context, qualityGatePassed bool) error {
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
		oroDir := filepath.Join(worktree, ".oro")
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
// It returns (true, nil) if the script exits 0, (false, nil) if it exits non-zero,
// and (false, err) if the script cannot be found or started.
func RunQualityGate(ctx context.Context, worktree string) (bool, error) {
	scriptPath := filepath.Join(worktree, "quality_gate.sh")
	if _, err := os.Stat(scriptPath); err != nil {
		return false, fmt.Errorf("quality gate script not found: %w", err)
	}

	cmd := exec.CommandContext(ctx, "bash", scriptPath) //nolint:gosec // script path constructed from worktree, not user input
	cmd.Dir = worktree

	if err := cmd.Run(); err != nil {
		// Non-zero exit is not an error — it means the gate failed
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return false, nil
		}
		return false, fmt.Errorf("run quality gate: %w", err)
	}
	return true, nil
}

// ClaudeSpawner is the production SubprocessSpawner that invokes `claude -p`.
type ClaudeSpawner struct{}

// Spawn starts a `claude -p` subprocess with the given prompt and working directory.
// Returns the process and a ReadCloser for its stdout stream.
func (s *ClaudeSpawner) Spawn(ctx context.Context, model, prompt, workdir string) (Process, io.ReadCloser, error) {
	cmd := exec.CommandContext(ctx, "claude", "-p", prompt, "--model", model)
	cmd.Dir = workdir
	cmd.Stderr = os.Stderr

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("start claude: %w", err)
	}
	return &cmdProcess{cmd: cmd}, stdoutPipe, nil
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
