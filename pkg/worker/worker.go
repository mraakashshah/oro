package worker

import (
	"bufio"
	"bytes"
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
	"sync/atomic"
	"time"

	"oro/pkg/agentruntime"
	"oro/pkg/cards"
	"oro/pkg/processenv"
	"oro/pkg/protocol"
)

// StreamFormat identifies how a runtime emits subprocess stdout.
type StreamFormat string

const (
	// StreamFormatClaudeJSON is the Claude event stream format used by the legacy runtime.
	StreamFormatClaudeJSON StreamFormat = "claude_stream_json"
	// StreamFormatLineText is a plain-text line-oriented stream format.
	StreamFormatLineText StreamFormat = "line_text"
	// StreamFormatGeminiJSON is the Gemini CLI event stream format.
	StreamFormatGeminiJSON StreamFormat = "gemini_stream_json"
)

// StreamingSpawner abstracts runtime subprocess invocation for testing.
// Spawn returns the process, stdout reader, stdin writer (both may be nil), and any error.
type StreamingSpawner interface {
	Spawn(ctx context.Context, model string, prompt string, workdir string) (Process, io.ReadCloser, io.WriteCloser, error)
	StreamFormat() StreamFormat
}

// ReasoningStreamingSpawner accepts a runtime-specific reasoning effort in
// addition to the model.
type ReasoningStreamingSpawner interface {
	SpawnWithReasoning(ctx context.Context, model string, reasoning string, prompt string, workdir string) (Process, io.ReadCloser, io.WriteCloser, error)
}

// LaunchPolicy identifies the mutation boundary required for a runtime process.
type LaunchPolicy string

const (
	// LaunchPolicyDefault permits the runtime's normal mutation boundary.
	LaunchPolicyDefault LaunchPolicy = ""
	// LaunchPolicyReadOnly requires managed read-only hook activation.
	LaunchPolicyReadOnly LaunchPolicy = "read-only"
)

// LaunchPolicyStreamingSpawner starts a subprocess with an explicit policy.
type LaunchPolicyStreamingSpawner interface {
	SpawnWithLaunchPolicy(ctx context.Context, model, reasoning, prompt, workdir string, policy LaunchPolicy) (Process, io.ReadCloser, io.WriteCloser, error)
}

// RuntimeStreamingSpawner routes subprocess invocation by runtime.
type RuntimeStreamingSpawner interface {
	Spawn(ctx context.Context, runtime string, model string, reasoning string, prompt string, workdir string) (Process, io.ReadCloser, io.WriteCloser, StreamFormat, error)
}

type runtimeSpawnerRouter struct {
	claudeSpawner StreamingSpawner
	codexSpawner  StreamingSpawner
}

// NewRuntimeSpawnerRouter creates a runtime-aware router over Claude and Codex spawners.
func NewRuntimeSpawnerRouter(claudeSpawner, codexSpawner StreamingSpawner) RuntimeStreamingSpawner {
	return &runtimeSpawnerRouter{
		claudeSpawner: claudeSpawner,
		codexSpawner:  codexSpawner,
	}
}

func singleRuntimeSpawner(spawner StreamingSpawner) RuntimeStreamingSpawner {
	return &runtimeSpawnerRouter{
		claudeSpawner: spawner,
		codexSpawner:  spawner,
	}
}

// Spawn selects the configured runtime spawner and starts the assignment subprocess.
func (r *runtimeSpawnerRouter) Spawn(ctx context.Context, runtime, model, reasoning, prompt, workdir string) (Process, io.ReadCloser, io.WriteCloser, StreamFormat, error) {
	spawner, err := r.spawnerForRuntime(runtime)
	if err != nil {
		return nil, nil, nil, "", err
	}
	var proc Process
	var stdout io.ReadCloser
	var stdin io.WriteCloser
	if reasoningSpawner, ok := spawner.(ReasoningStreamingSpawner); ok {
		proc, stdout, stdin, err = reasoningSpawner.SpawnWithReasoning(ctx, model, reasoning, prompt, workdir)
	} else {
		proc, stdout, stdin, err = spawner.Spawn(ctx, model, prompt, workdir)
	}
	if err != nil {
		return nil, nil, nil, "", fmt.Errorf("spawn %s subprocess: %w", runtime, err)
	}
	return proc, stdout, stdin, spawner.StreamFormat(), nil
}

func (r *runtimeSpawnerRouter) spawnerForRuntime(runtime string) (StreamingSpawner, error) {
	switch runtime {
	case agentruntime.RuntimeClaude:
		if r.claudeSpawner == nil {
			return nil, fmt.Errorf("claude runtime spawner is not configured")
		}
		return r.claudeSpawner, nil
	case agentruntime.RuntimeCodex:
		if r.codexSpawner == nil {
			return nil, fmt.Errorf("codex runtime spawner is not configured")
		}
		return r.codexSpawner, nil
	default:
		return nil, fmt.Errorf("unknown agent runtime %q", runtime)
	}
}

// Process abstracts a running subprocess.
type Process interface {
	Wait() error
	Kill() error
}

type processExitDiagnostics interface {
	ExitCode() int
	StderrTail() string
}

// LearningSink abstracts pending learning insertion at the worker package boundary.
type LearningSink interface {
	AppendLearningPending(ctx context.Context, beadID string, c cards.CardCandidate) (int64, error)
}

// MemoryExtractSpawner starts an LLM extraction subprocess.
type MemoryExtractSpawner interface {
	Spawn(ctx context.Context, model, prompt string) (io.ReadCloser, error)
}

// WorkdirMemoryExtractSpawner binds extraction subprocesses to a worktree.
type WorkdirMemoryExtractSpawner interface {
	SpawnInWorkdir(ctx context.Context, model, prompt, workdir string) (io.ReadCloser, error)
}

type subprocessExitSnapshot struct {
	Runtime    string
	Model      string
	ExitCode   int
	ExitError  string
	StderrTail string
}

const subprocessDiedReason = "subprocess_died"

// DefaultContextPollInterval controls how often the context watcher polls <worktree>/.oro/context_pct.
const DefaultContextPollInterval = 5 * time.Second

// DefaultHeartbeatInterval controls the minimum time between periodic heartbeats
// sent to the dispatcher. Must be well under the dispatcher's HeartbeatTimeout (45s).
const DefaultHeartbeatInterval = 10 * time.Second

// DefaultThreshold is the fallback context percentage when thresholds.json is missing or model unknown.
const DefaultThreshold = 40

// thresholds holds per-model context percentage thresholds loaded from <worktree>/.oro/thresholds.json.
type thresholds struct {
	models map[string]int
}

// For returns the threshold for the given model, falling back to DefaultThreshold.
func (t thresholds) For(model string) int {
	if v, ok := t.models[model]; ok {
		if v <= 0 {
			return DefaultThreshold
		}
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

const workerMessageWriteTimeout = 200 * time.Millisecond

// Worker is the Oro worker agent. It holds a UDS connection to the Dispatcher,
// manages a subprocess (claude or codex), and monitors context usage.
type Worker struct {
	ID                     string
	conn                   net.Conn
	proc                   Process
	beadID                 string
	worktree               string
	assignmentID           int64
	qgEvidenceDir          string
	targetSHA              string
	qgEvidencePath         string
	qgEvidence             *protocol.QGEvidence
	qgEvidenceRef          *protocol.QGEvidenceRef
	runtime                string
	model                  string
	streamFormat           StreamFormat
	mu                     sync.Mutex
	spawner                RuntimeStreamingSpawner
	socketPath             string // for reconnection
	buffer                 *MessageBuffer
	disconnected           bool
	contextPollInterval    time.Duration
	reconnectInterval      time.Duration // base retry interval for reconnection
	memStore               LearningSink
	extractSpawner         MemoryExtractSpawner
	sessionText            strings.Builder
	outputWg               sync.WaitGroup         // tracks processOutput goroutine completion
	reconnectDialHook      func(net.Conn)         // test hook: called after dial, before sendMessage
	reconnectTimerStopHook func()                 // test hook: called when timer.Stop() fires on ctx cancel
	pendingQGOutput        string                 // QG output stored while awaiting review result
	isEpicDecomposition    bool                   // true when current assignment is an epic decomposition
	subprocExitCh          chan struct{}          // closed when subprocess exits
	subprocExitClosed      bool                   // true if subprocExitCh has been closed
	subprocExitErr         string                 // Process.Wait error captured for diagnostics
	subprocExitCode        int                    // process exit code captured after Wait
	subprocStderrTail      string                 // final runtime stderr tail captured after Wait
	handleExitClaimed      bool                   // true if a handler claimed subprocess exit handling
	subprocKilledByUs      bool                   // true if we intentionally killed the subprocess
	connWriteMu            sync.Mutex             // serializes conn writes so heartbeat deadlines don't leak
	heartbeatInterval      time.Duration          // minimum time between periodic heartbeats
	logMu                  sync.Mutex             // serializes output writes and log rotation
	logFile                *os.File               // per-worker output log file at ~/.oro/workers/<ID>/output.log
	logWriter              *bufio.Writer          // buffered writer for logFile to prevent blocking
	streamContextPct       int32                  // atomic: latest context_pct observed from stream output (0 = no signal yet)
	assignmentGeneration   uint64                 // guarded by mu: rejects stale stdout context from prior assignments
	execution              WorkerExecutionContext // guarded by mu: authority for the current assignment
	tier                   protocol.Tier          // routing tier from bead assignment; empty for legacy beads
	targetBranch           string                 // branch to rebase onto before QG; defaults to "main"
	subprocStartedAt       time.Time              // subprocess start time for progress diagnostics
	lastSubprocOutputAt    time.Time              // latest stdout activity for progress diagnostics
}

// New creates a Worker that connects to the Dispatcher at socketPath.
// spawner is treated as the claude runtime spawner; codex falls back to it when no codexSpawner is set.
func New(id, socketPath string, spawner StreamingSpawner) (*Worker, error) {
	return NewWithRuntimeSpawner(id, socketPath, singleRuntimeSpawner(spawner))
}

// NewWithRuntimeSpawner creates a Worker that connects to the Dispatcher at socketPath
// and routes each assignment through the provided runtime-aware spawner.
func NewWithRuntimeSpawner(id, socketPath string, spawner RuntimeStreamingSpawner) (*Worker, error) {
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
// spawner is treated as the claude runtime spawner.
//
//oro:testonly
func NewWithConn(id string, conn net.Conn, spawner StreamingSpawner) *Worker {
	return &Worker{
		ID:                  id,
		conn:                conn,
		spawner:             singleRuntimeSpawner(spawner),
		buffer:              NewMessageBuffer(maxBufferedMessages),
		contextPollInterval: DefaultContextPollInterval,
		reconnectInterval:   reconnectBaseInterval,
	}
}

// NewWithConnAndRuntimeSpawners creates a Worker that routes each assignment by payload runtime.
//
//oro:testonly
func NewWithConnAndRuntimeSpawners(id string, conn net.Conn, claudeSpawner, codexSpawner StreamingSpawner) *Worker {
	return &Worker{
		ID:                  id,
		conn:                conn,
		spawner:             NewRuntimeSpawnerRouter(claudeSpawner, codexSpawner),
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

// SetReconnectTimerStopHook sets a function called when timer.Stop() fires due
// to context cancellation during the reconnect sleep. For testing only.
//
//oro:testonly
func (w *Worker) SetReconnectTimerStopHook(fn func()) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.reconnectTimerStopHook = fn
}

// SetMemoryStore attaches a learning sink to the worker for learning extraction.
// When set, [MEMORY] markers in subprocess stdout are captured in real-time,
// and implicit patterns are extracted on handoff/completion.
func (w *Worker) SetMemoryStore(s LearningSink) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.memStore = s
}

// SetExtractSpawner attaches a spawner for LLM-based memory extraction.
// When set, extractImplicitMemories uses ExtractWithLLM instead of the regex-based fallback.
//
//oro:testonly — wired into production by bead oro-eyrq.8 (Wire CLISpawner into CLI commands)
func (w *Worker) SetExtractSpawner(s MemoryExtractSpawner) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.extractSpawner = s
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
	defer w.removeAssignmentCapabilityFile()
	msgCh, errCh := w.readMessages()

	// Announce ourselves so the dispatcher can register this worker.
	if err := w.SendHeartbeat(ctx, 0); err != nil {
		if w.socketPath == "" {
			return fmt.Errorf("send initial heartbeat: %w", err)
		}
	}

	// Idle heartbeat ticker keeps the dispatcher from timing us out
	// while we wait for assignment. Stopped when watchContext takes over.
	idleTicker := time.NewTicker(DefaultHeartbeatInterval)
	defer idleTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			w.killProc()
			return nil

		case <-idleTicker.C:
			_ = w.SendHeartbeat(ctx, 0)

		case msg := <-msgCh:
			if done, err := w.handleMessage(ctx, msg); err != nil || done {
				if err != nil {
					return fmt.Errorf("handle message: %w", err)
				}
				return nil
			}

		case err := <-errCh:
			nextMsgCh, nextErrCh, done, handleErr := w.restartReadLoopAfterConnectionError(ctx, err)
			if handleErr != nil {
				return handleErr
			}
			if done {
				return nil
			}
			msgCh, errCh = nextMsgCh, nextErrCh
		}
	}
}

func (w *Worker) restartReadLoopAfterConnectionError(ctx context.Context, err error) (msgs <-chan protocol.Message, readErr <-chan error, done bool, runErr error) {
	if handleErr := w.handleConnectionError(ctx, err); handleErr != nil {
		if ctx.Err() != nil {
			w.killProc()
			return nil, nil, true, nil //nolint:nilerr // context cancellation is clean shutdown
		}
		return nil, nil, false, fmt.Errorf("handle connection error: %w", handleErr)
	}
	if ctx.Err() != nil {
		w.killProc()
		return nil, nil, true, nil //nolint:nilerr // connection closed during context cancellation
	}
	msgCh, errCh := w.readMessages()
	return msgCh, errCh, false, nil
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
		w.removeAssignmentCapabilityFile()
		// No socketPath means we can't reconnect (test with net.Pipe)
		return fmt.Errorf("connection error (no reconnect possible): %w", err)
	}
	if reconnectErr := w.reconnect(ctx); reconnectErr != nil {
		w.removeAssignmentCapabilityFile()
		return reconnectErr
	}
	return nil
}

// handleMessage processes a single incoming message. Returns (true, nil) on shutdown.
func (w *Worker) handleMessage(ctx context.Context, msg protocol.Message) (bool, error) {
	switch msg.Type {
	case protocol.MsgAssign:
		return false, w.handleAssign(ctx, msg)
	case protocol.MsgCapabilityRefresh:
		if err := w.handleCapabilityRefresh(msg); err != nil {
			fmt.Fprintf(os.Stderr, "worker %s: ignore capability refresh: %v\n", w.ID, err)
		}
		return false, nil
	case protocol.MsgShutdown:
		w.removeAssignmentCapabilityFile()
		w.killProc()
		return true, nil
	case protocol.MsgPrepareShutdown:
		return w.handlePrepareShutdown(ctx, msg)
	case protocol.MsgPreempt:
		return w.handlePreempt(ctx)
	case protocol.MsgReviewResult:
		return false, w.handleReviewResult(ctx, msg)
	default:
		// Unknown message type, ignore
		return false, nil
	}
}

func (w *Worker) handleCapabilityRefresh(msg protocol.Message) error {
	refresh := msg.CapabilityRefresh
	if refresh == nil || refresh.AssignmentID <= 0 || refresh.Generation <= 0 || refresh.CapabilityID == "" || refresh.Capability == "" {
		return fmt.Errorf("invalid capability refresh")
	}
	w.mu.Lock()
	execution := w.execution
	w.mu.Unlock()
	if execution.AssignmentID != refresh.AssignmentID || execution.Generation != refresh.Generation || execution.CapabilityFile == "" {
		return fmt.Errorf("capability refresh does not match active assignment")
	}
	if err := ReplaceCapabilityFile(execution.CapabilityFile, AssignmentCredential{AssignmentID: refresh.AssignmentID, Generation: refresh.Generation, CapabilityID: refresh.CapabilityID, Token: refresh.Capability, ExpiresAt: refresh.ExpiresAt}); err != nil {
		return fmt.Errorf("install refreshed capability: %w", err)
	}
	installed, err := ReadCapabilityFile(execution.CapabilityFile)
	if err != nil {
		return fmt.Errorf("verify refreshed capability: %w", err)
	}
	if installed.CapabilityID != refresh.CapabilityID {
		return errors.New("verify refreshed capability: replacement ID mismatch")
	}
	return w.sendMessage(protocol.Message{Type: protocol.MsgCapabilityRefreshACK, CapabilityRefreshACK: &protocol.CapabilityRefreshACKPayload{AssignmentID: refresh.AssignmentID, CapabilityID: refresh.CapabilityID}})
}

// handlePreempt saves the current assignment context, stops any active
// subprocess, and exits so the dispatcher can reclaim this worker's slot.
func (w *Worker) handlePreempt(ctx context.Context) (bool, error) {
	_ = w.SendHandoff(ctx)
	w.removeAssignmentCapabilityFile()
	w.killProc()
	return true, nil
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
		w.removeAssignmentCapabilityFile()
		w.killProc()
		return true, nil
	}

	// Save context by sending a HANDOFF with learnings/decisions
	_ = w.SendHandoff(ctx)

	// Signal that we're ready to be shut down
	_ = w.SendShutdownApproved(ctx)

	// Kill the subprocess
	w.removeAssignmentCapabilityFile()
	w.killProc()

	return true, nil
}

// handleAssign processes an ASSIGN message: stores state, spawns subprocess,
// starts context watcher, and pipes stdout through memory extraction.
func (w *Worker) handleAssign(ctx context.Context, msg protocol.Message) error {
	execution, err := w.validateAssignMessage(msg)
	if err != nil {
		return err
	}

	if msg.Assign.Attempt > 0 {
		_ = w.sendMessage(protocol.Message{
			Type: protocol.MsgStatus,
			Status: &protocol.StatusPayload{
				BeadID:   msg.Assign.BeadID,
				WorkerID: w.ID,
				State:    "qg_retry_received",
				Result:   fmt.Sprintf(`{"attempt":%d,"model":%q}`, msg.Assign.Attempt, msg.Assign.Model),
			},
		})
	}
	if err := validateAssignedWorktree(msg.Assign.Worktree); err != nil {
		return err
	}

	w.resetForNewAssignment(msg.Assign, execution)
	if err := w.installAssignmentCredential(msg.Assign, execution); err != nil {
		return err
	}

	prompt, model := BuildAssignPrompt(msg.Assign)
	runtime := msg.Assign.Runtime
	if runtime == "" {
		runtime = agentruntime.ReadRuntime()
		model = protocol.DefaultModel
		fmt.Fprintf(os.Stderr, "oro worker: assign payload missing runtime; falling back to %s/%s\n", runtime, model)
	}
	stopSpawnHeartbeat := w.startSpawnHeartbeat(ctx)
	proc, stdout, _, format, err := w.spawner.Spawn(withAssignmentContext(ctx, execution, msg.Assign.BeadID), runtime, model, msg.Assign.Reasoning, prompt, msg.Assign.Worktree)
	stopSpawnHeartbeat()
	if err != nil {
		return fmt.Errorf("spawn %s: %w", runtime, err)
	}
	w.recordSpawnedProc(proc, runtime, model, format)

	if stdout != nil {
		w.mu.Lock()
		generation := w.assignmentGeneration
		w.mu.Unlock()
		w.outputWg.Add(1)
		go w.processOutput(ctx, stdout, generation)
	}
	if err := w.SendStatus(ctx, "running", ""); err != nil {
		return fmt.Errorf("send status: %w", err)
	}
	go w.monitorSubprocessExit(proc)
	go w.watchContext(ctx)
	go w.awaitSubprocessAndReport(ctx) // wait for exit, run QG, send DONE
	return nil
}

func (w *Worker) validateAssignMessage(msg protocol.Message) (WorkerExecutionContext, error) {
	if msg.Assign == nil {
		return WorkerExecutionContext{}, fmt.Errorf("assign message missing payload")
	}
	if err := msg.Assign.Validate(); err != nil {
		return WorkerExecutionContext{}, fmt.Errorf("invalid assign payload: %w", err)
	}
	if err := validateAssignmentEvidenceIdentity(msg.Assign); err != nil {
		return WorkerExecutionContext{}, fmt.Errorf("invalid assign evidence identity: %w", err)
	}
	execution, err := executionContextForAssign(msg.Assign, w.ID, w.socketPath)
	if err != nil {
		return WorkerExecutionContext{}, fmt.Errorf("invalid assign execution context: %w", err)
	}
	return execution, nil
}

func (w *Worker) startSpawnHeartbeat(ctx context.Context) func() {
	w.mu.Lock()
	interval := w.heartbeatInterval
	w.mu.Unlock()
	if interval == 0 {
		interval = DefaultHeartbeatInterval
	}

	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case <-ticker.C:
				w.trySendHeartbeat(ctx)
			}
		}
	}()

	return func() {
		close(done)
		<-stopped
	}
}

func validateAssignedWorktree(worktree string) error {
	info, err := os.Stat(worktree)
	if err != nil {
		return fmt.Errorf("assigned worktree unavailable: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("assigned worktree %q is not a directory", worktree)
	}
	return nil
}

// resetForNewAssignment kills any prior subprocess, clears worker state under
// the lock, removes stale assignment-local sentinels from the new worktree, and
// truncates the log file before the next subprocess spawns.
func (w *Worker) resetForNewAssignment(a *protocol.AssignPayload, execution WorkerExecutionContext) {
	w.killProc()
	target := a.TargetBranch
	if target == "" {
		target = "main"
	}
	w.mu.Lock()
	w.assignmentGeneration++
	w.execution = execution
	atomic.StoreInt32(&w.streamContextPct, 0)
	w.beadID = a.BeadID
	w.worktree = a.Worktree
	w.assignmentID = a.AssignmentID
	w.qgEvidenceDir = a.QGEvidenceDir
	w.targetSHA = a.TargetSHA
	w.qgEvidencePath = ""
	w.qgEvidence = nil
	w.qgEvidenceRef = nil
	w.tier = a.Tier
	w.targetBranch = target
	w.sessionText.Reset()
	w.pendingQGOutput = ""
	w.isEpicDecomposition = a.IsEpicDecomposition
	w.mu.Unlock()

	if a.Worktree != "" {
		clearAssignmentLocalState(a.Worktree)
	}
	w.closeLogFile()
	_ = w.openLogFile()
}

func clearAssignmentLocalState(worktree string) {
	oroDir := filepath.Join(worktree, protocol.OroDir)
	_ = os.Remove(filepath.Join(oroDir, "handoff_done"))
	_ = os.Remove(filepath.Join(oroDir, "context_pct"))
}

func (w *Worker) removeAssignmentCapabilityFile() {
	w.mu.Lock()
	path := w.execution.CapabilityFile
	w.mu.Unlock()
	_ = RemoveCapabilityFile(path)
}

func (w *Worker) installAssignmentCredential(a *protocol.AssignPayload, execution WorkerExecutionContext) error {
	if execution.CapabilityFile == "" {
		return nil
	}
	credential := AssignmentCredential{
		AssignmentID: a.AssignmentID,
		Generation:   a.Generation,
		CapabilityID: a.Capability,
		Token:        a.Capability,
	}
	if err := ReplaceCapabilityFile(execution.CapabilityFile, credential); err != nil {
		return fmt.Errorf("install assignment capability: %w", err)
	}
	return nil
}

// recordSpawnedProc captures the freshly spawned subprocess + model and resets
// the exit-coordination flags under the worker lock.
func (w *Worker) recordSpawnedProc(proc Process, runtime, model string, format StreamFormat) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.proc = proc
	w.runtime = runtime
	w.model = model
	w.streamFormat = format
	w.subprocExitCh = make(chan struct{})
	w.subprocExitClosed = false
	w.subprocExitErr = ""
	w.subprocExitCode = 0
	w.subprocStderrTail = ""
	w.handleExitClaimed = false
	w.subprocKilledByUs = false
	w.subprocStartedAt = time.Now()
	w.lastSubprocOutputAt = time.Time{}
}

// BuildAssignPrompt constructs the prompt and resolves the model from an ASSIGN payload.
// When IsEpicDecomposition is true, it returns a planning-only prompt via
// BuildEpicDecompositionPrompt (no TDD/QG/worktree sections). Otherwise it
// returns the standard 12-section worker prompt.
func BuildAssignPrompt(a *protocol.AssignPayload) (prompt, model string) {
	switch {
	case a.IsEpicDecomposition:
		prompt = BuildEpicDecompositionPrompt(EpicPromptParams{
			BeadID:             a.BeadID,
			Title:              a.Title,
			Description:        a.Description,
			AcceptanceCriteria: a.AcceptanceCriteria,
		})
	case a.Title != "":
		prompt = AssemblePrompt(PromptParams{
			BeadID:               a.BeadID,
			Title:                a.Title,
			Description:          a.Description,
			AcceptanceCriteria:   a.AcceptanceCriteria,
			MemoryContext:        a.MemoryContext,
			Cards:                a.Cards,
			CodeSearchContext:    a.CodeSearchContext,
			CodeStructureContext: a.CodeStructureContext,
			WorktreePath:         a.Worktree,
			Model:                a.Model,
			Attempt:              a.Attempt,
			Feedback:             a.Feedback,
			ProjectRoot:          a.ProjectRoot,
			TargetBranch:         a.TargetBranch,
			GitLog:               a.GitLog,
			WorkerProgram:        a.WorkerProgram,
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
	waitErr := proc.Wait()
	exitCode := 0
	stderrTail := ""
	if diagnostics, ok := proc.(processExitDiagnostics); ok {
		exitCode = diagnostics.ExitCode()
		stderrTail = trimLastLines(diagnostics.StderrTail(), 100)
	}
	waitErrText := ""
	if waitErr != nil {
		waitErrText = waitErr.Error()
	}
	w.mu.Lock()
	w.subprocExitErr = waitErrText
	w.subprocExitCode = exitCode
	w.subprocStderrTail = stderrTail
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
	target := w.targetBranch
	assignmentID := w.assignmentID
	w.mu.Unlock()

	// Rebase onto the target branch before QG so fixes already on main
	// (e.g. go.mod vulnerability patches) are visible to the quality gate.
	// If the rebase fails (conflict or no git repo), proceed anyway — QG will
	// surface legitimate failures without blocking the worker indefinitely.
	_ = rebaseOntoTarget(ctx, wt, target)

	scriptPath, script, err := loadQualityGateScript(ctx, wt)
	if err != nil {
		_ = w.SendDone(ctx, false, err.Error())
		return
	}
	startedAt := time.Now().UTC()
	passed, output, err := w.runQualityGateWithProgress(ctx, wt, scriptPath, true, target)
	if ctx.Err() != nil {
		return
	}
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
	finishedAt := time.Now().UTC()
	headSHA, err := gitHeadSHA(ctx, wt)
	if err != nil {
		_ = w.SendDone(ctx, false, err.Error())
		return
	}

	// QG passed — store the output and send READY_FOR_REVIEW.
	// The worker waits for the dispatcher to send back a REVIEW_RESULT
	// (approved) or a new ASSIGN (rejected with feedback).
	w.mu.Lock()
	w.pendingQGOutput = output
	w.mu.Unlock()
	evidence, err := w.buildQGEvidence(qgEvidenceOptions{
		RunID:      fmt.Sprintf("%d:1", assignmentID),
		HeadSHA:    headSHA,
		ScriptHash: sha256Hex(script),
		Output:     []byte(output),
		StartedAt:  startedAt,
		FinishedAt: finishedAt,
	})
	if err != nil {
		_ = w.SendDone(ctx, false, err.Error())
		return
	}
	if _, err := w.writeQGEvidence(evidence); err != nil {
		_ = w.SendDone(ctx, false, err.Error())
		return
	}

	_ = w.SendReadyForReview(ctx)
}

func loadQualityGateScript(ctx context.Context, worktree string) (scriptPath string, script []byte, err error) {
	scriptPath, err = findQualityGateScript(ctx, worktree)
	if err != nil {
		return "", nil, err
	}
	script, err = os.ReadFile(scriptPath) //nolint:gosec // selected from the assigned worktree
	if err != nil {
		return "", nil, fmt.Errorf("read quality gate script: %w", err)
	}
	return scriptPath, script, nil
}

func (w *Worker) runQualityGateWithProgress(ctx context.Context, worktree, scriptPath string, skipMutation bool, mutationBase string) (passed bool, output string, err error) {
	args := []string{scriptPath}
	if !skipMutation {
		args = append(args, "--mutation-testing")
	}
	cmd := exec.CommandContext(ctx, "bash", args...) //nolint:gosec // script path constructed from worktree, not user input
	cmd.Dir = worktree
	cmd.Env = qualityGateEnv(worktree, skipMutation, mutationBase)

	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	if err := cmd.Start(); err != nil {
		return false, "", fmt.Errorf("run quality gate: %w", err)
	}
	w.recordSpawnedProc(&commandProcess{cmd: cmd}, "quality_gate", "quality_gate", StreamFormatLineText)

	err = cmd.Wait()
	output = out.String()

	w.mu.Lock()
	w.subprocExitErr = ""
	w.subprocExitCode = 0
	w.handleExitClaimed = true
	if err != nil {
		w.subprocExitErr = err.Error()
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			w.subprocExitCode = exitErr.ExitCode()
		}
	}
	if w.subprocExitCh != nil && !w.subprocExitClosed {
		close(w.subprocExitCh)
		w.subprocExitClosed = true
	}
	w.mu.Unlock()

	if ctxErr := ctx.Err(); ctxErr != nil {
		return false, output, fmt.Errorf("run quality gate canceled: %w", ctxErr)
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return false, output, nil
		}
		return false, output, fmt.Errorf("run quality gate: %w", err)
	}
	return true, output, nil
}

func gitHeadSHA(ctx context.Context, worktree string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "HEAD^{commit}") //nolint:gosec // fixed git arguments
	cmd.Dir = worktree
	cmd.Env = processenv.ForWorkdir(os.Environ(), worktree)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("read post-quality-gate HEAD: %w", err)
	}
	head := strings.TrimSpace(string(out))
	if head == "" {
		return "", errors.New("read post-quality-gate HEAD: empty result")
	}
	return head, nil
}

// processOutput reads subprocess stdout according to the runtime stream format,
// logs tool-call activity where available, and accumulates text content for
// memory extraction. When stdout closes (subprocess exits), it extracts
// implicit memories so that learnings from failed attempts are persisted
// before the dispatcher re-assigns.
func (w *Worker) processOutput(ctx context.Context, stdout io.ReadCloser, generation uint64) {
	defer w.outputWg.Done()
	defer func() { _ = stdout.Close() }()

	w.mu.Lock()
	format := w.streamFormat
	w.mu.Unlock()

	var sanitizer credentialLineSanitizer

	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		// Extract context% from each line; store the latest observed value.
		w.updateStreamContextPct(format, scanner.Bytes(), generation)
		switch format {
		case StreamFormatLineText:
			w.processPlaintextLine(ctx, scanner.Text())
		default:
			w.processStructuredStreamLine(ctx, scanner.Bytes(), &sanitizer)
		}
	}

	// Flush any remaining buffered text (incomplete final line) for structured streams.
	if format != StreamFormatLineText {
		if line, ok := sanitizer.flush(); ok {
			w.processOutputTextLine(ctx, line)
		}
	}

	// Flush log buffer once after all events are processed (not per-event).
	w.logMu.Lock()
	if w.logWriter != nil {
		_ = w.logWriter.Flush()
	}
	w.logMu.Unlock()

	// Subprocess stdout closed — extract implicit memories so learnings from
	// failed attempts (e.g. QG failure) are persisted regardless of outcome.
	w.extractImplicitMemories(ctx)
}

func (w *Worker) updateStreamContextPct(format StreamFormat, line []byte, generation uint64) {
	pct, ok := ContextPctFromLine(format, line)
	if !ok {
		return
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	if w.assignmentGeneration == generation {
		atomic.StoreInt32(&w.streamContextPct, int32(pct))
	}
}

func (w *Worker) processStructuredStreamLine(ctx context.Context, line []byte, sanitizer *credentialLineSanitizer) {
	activity := ParseStreamEvent(line)

	// Log formatted tool-call activity (best-effort; don't block on I/O errors).
	if formatted := FormatActivity(activity); formatted != "" {
		w.logMu.Lock()
		if w.logWriter != nil {
			_, _ = w.logWriter.WriteString(formatted)
			_, _ = w.logWriter.WriteString("\n")
		}
		w.logMu.Unlock()
	}

	for _, textLine := range sanitizer.append(activity.Text) {
		w.processOutputTextLine(ctx, textLine)
	}
}

func (w *Worker) processPlaintextLine(ctx context.Context, line string) {
	w.processOutputTextLine(ctx, line)
}

func (w *Worker) processOutputTextLine(ctx context.Context, line string) {
	line = redactCredentialAssignments(line)
	w.logMu.Lock()
	if w.logWriter != nil {
		_, _ = w.logWriter.WriteString(line)
		_, _ = w.logWriter.WriteString("\n")
	}
	w.logMu.Unlock()
	w.processTextLine(ctx, line)
}

// processTextLine appends a single text line to sessionText and extracts
// any [MEMORY] marker from it.
func (w *Worker) processTextLine(ctx context.Context, line string) {
	w.mu.Lock()
	w.lastSubprocOutputAt = time.Now()
	w.sessionText.WriteString(line)
	w.sessionText.WriteString("\n")
	store := w.memStore
	beadID := w.beadID
	w.mu.Unlock()

	appendMemoryMarker(ctx, store, beadID, line)
	w.flushImplicitMemories(ctx, false)
}

// extractImplicitMemories runs LLM-based memory extraction on accumulated
// session text and inserts results into the memory store. Called when
// processOutput finishes (subprocess stdout closes). Requires both
// extractSpawner and memStore to be set; no-op otherwise.
func (w *Worker) extractImplicitMemories(ctx context.Context) {
	w.flushImplicitMemories(ctx, true)
}

func (w *Worker) flushImplicitMemories(ctx context.Context, force bool) {
	w.mu.Lock()
	spawner := w.extractSpawner
	store := w.memStore
	if spawner == nil || store == nil {
		w.mu.Unlock()
		return
	}
	if !force && w.sessionText.Len() < maxMemorySessionBytes {
		w.mu.Unlock()
		return
	}
	text := w.sessionText.String()
	if text == "" {
		w.mu.Unlock()
		return
	}
	w.sessionText.Reset()
	beadID := w.beadID
	worktree := w.worktree
	w.mu.Unlock()

	candidates, err := ExtractMemoriesFromReader(ctx, strings.NewReader(text), spawner, worktree)
	if err != nil {
		fmt.Fprintf(os.Stderr, "worker %s: extract implicit memories: %v\n", w.ID, err)
		return
	}
	appendMemoryCandidates(ctx, store, beadID, candidates)
}

// openLogFile creates or opens ~/.oro/workers/<ID>/output.log for appending.
// Uses O_APPEND to preserve content from both dispatcher and worker writes.
// If directory creation or file open fails, returns error but caller should
// continue without logging (best-effort).
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
	// O_APPEND allows both dispatcher and worker to write without truncating each other
	// #nosec G304 -- logPath is constructed from home dir and worker ID, not user input
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}

	w.logMu.Lock()
	w.logFile = f
	w.logWriter = bufio.NewWriter(f)
	w.logMu.Unlock()
	return nil
}

// closeLogFile flushes and closes the log file. Safe to call multiple times.
func (w *Worker) closeLogFile() {
	w.logMu.Lock()
	defer w.logMu.Unlock()
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
// It delegates the authoritative full quality gate to the worker harness.
// If memoryContext is non-empty, it is appended as a section so the worker
// benefits from cross-session memories retrieved by the dispatcher.
func BuildPrompt(beadID, worktree, memoryContext string) string {
	base := fmt.Sprintf("Execute bead %s in worktree %s. Run the task acceptance tests and focused verification needed to validate your work. The worker harness owns and enforces the full quality gate; do not run the full quality gate yourself.", beadID, worktree)
	if memoryContext == "" {
		return base
	}
	return base + "\n\n" + memoryContext
}

// watchContext polls .oro/context_pct in the current worktree and triggers
// a single-stage hard stop when context usage exceeds the model-specific
// hard threshold (soft threshold + 10). Layer 1 prompt handles the soft
// threshold; the Go worker enforces the hard stop via SendHandoff + killProc.
//
// It also monitors subprocess health: if the subprocess dies unexpectedly
// (subprocess exits and remains unclaimed for one poll interval), send DONE(false) with error.
func (w *Worker) watchContext(ctx context.Context) {
	w.mu.Lock()
	interval := w.contextPollInterval
	wt := w.worktree
	model := w.model
	tier := w.tier
	hbInterval := w.heartbeatInterval
	w.mu.Unlock()
	if hbInterval == 0 {
		hbInterval = DefaultHeartbeatInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Load per-model/tier thresholds from worktree root.
	th := loadThresholds(wt)
	threshold := th.For(effectiveThresholdKey(tier, model))

	var subprocExitDetectedAt time.Time
	lastHeartbeat := time.Now() // start counting from now; first heartbeat after hbInterval
	lastProgress := time.Now()  // initial running STATUS was sent by handleAssign

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
			if w.checkSubprocessHealth(&subprocExitDetectedAt) {
				return
			}

			if time.Since(lastProgress) >= interval {
				w.trySendSubprocessProgress(ctx)
				lastProgress = time.Now()
			}

			w.mu.Lock()
			wt = w.worktree
			w.mu.Unlock()

			// Check for handoff_done file written by the agent
			if w.checkHandoffFile(ctx, wt) {
				return
			}

			// Check context percentage and handle threshold breaches
			if w.handleContextThreshold(ctx, wt, threshold) {
				return
			}
		}
	}
}

// checkHandoffFile checks for .oro/handoff_done in the worktree. When found,
// it deletes the file, sends HANDOFF, and kills the subprocess.
// Returns true if the handoff was triggered (caller should return).
// Edges: file missing → no-op; subprocess already exited → killProc is a no-op.
func (w *Worker) checkHandoffFile(ctx context.Context, wt string) bool {
	if wt == "" {
		return false
	}
	handoffPath := filepath.Join(wt, protocol.OroDir, "handoff_done")
	if _, err := os.Stat(handoffPath); err != nil {
		return false // file missing → no-op
	}
	// Delete before sending to prevent double-trigger on next tick.
	_ = os.Remove(handoffPath)
	_ = w.SendHandoff(ctx)
	w.killProc()
	return true
}

// handleContextThreshold checks context percentage and handles threshold breaches.
// Single-stage hard stop: if pct > threshold+10, send handoff and kill.
// Layer 1 prompt handles the soft threshold (the raw threshold value);
// this function enforces the hard stop 10 points above.
// Returns true if handoff was triggered (caller should return).
func (w *Worker) handleContextThreshold(ctx context.Context, wt string, threshold int) bool {
	if wt == "" {
		return false
	}

	pctPath := filepath.Join(wt, protocol.OroDir, "context_pct")
	data, err := os.ReadFile(pctPath) //nolint:gosec // path is constructed internally, not user input

	var pct int
	if err == nil {
		pct, _ = strconv.Atoi(strings.TrimSpace(string(data)))
	}
	// Fall back to stream-parsed context_pct when the file is absent or unparseable.
	if pct == 0 {
		pct = int(atomic.LoadInt32(&w.streamContextPct))
	}
	if pct == 0 {
		return false
	}

	hardStop := threshold + 10
	if pct <= hardStop {
		return false
	}

	// Hard stop: handoff + kill
	_ = w.SendHandoff(ctx)
	w.killProc()
	return true
}

// checkSubprocessHealth checks if the subprocess has died unexpectedly.
// Returns true if unexpected death was detected and DONE was sent.
func (w *Worker) checkSubprocessHealth(detectedAt *time.Time) bool {
	w.mu.Lock()
	exitClosed := w.subprocExitClosed
	claimed := w.handleExitClaimed
	exitSnapshot := w.subprocessExitSnapshotLocked()
	w.mu.Unlock()

	if !exitClosed || claimed {
		return false
	}
	if exitSnapshot.ExitError == "" && exitSnapshot.ExitCode == 0 {
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
		_ = w.sendSubprocessDied(exitSnapshot)
		return true
	}
	w.mu.Unlock()
	return false
}

// modelFamily extracts the model family name (opus, sonnet, haiku) from a full model ID.
// Returns "balanced" for non-Claude models so they fall back to a known threshold key.
func modelFamily(model string) string {
	lower := strings.ToLower(model)
	for _, family := range []string{"opus", "sonnet", "haiku"} {
		if strings.Contains(lower, family) {
			return family
		}
	}
	return "balanced"
}

// effectiveThresholdKey returns the thresholds map key for the given tier and model.
// Priority: known tier → tier string; else → modelFamily (claude) or "balanced" (non-claude).
func effectiveThresholdKey(tier protocol.Tier, model string) string {
	if tier.IsKnown() {
		return string(tier)
	}
	return modelFamily(model)
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

// reconnectSleep blocks for d or until ctx is cancelled. On cancellation it
// calls reconnectTimerStopHook (if set) and returns a wrapped ctx.Err().
func (w *Worker) reconnectSleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	select {
	case <-ctx.Done():
		timer.Stop()
		w.mu.Lock()
		hook := w.reconnectTimerStopHook
		w.mu.Unlock()
		if hook != nil {
			hook()
		}
		return fmt.Errorf("worker reconnect: %w", ctx.Err())
	case <-timer.C:
		return nil
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

		if err := w.reconnectSleep(ctx, wait); err != nil {
			return err
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
		if w.qgEvidencePath != "" {
			state = "awaiting_review"
		} else if w.proc == nil {
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
				WorkerID:        w.ID,
				BeadID:          beadID,
				State:           state,
				BufferedEvents:  buffered,
				ProtocolVersion: protocol.WorkerProtocolVersion,
				Capabilities:    []string{protocol.CapabilityReadyEvidenceV1},
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
	deadlineSet := conn.SetWriteDeadline(time.Now().Add(workerMessageWriteTimeout)) == nil
	_, err = conn.Write(data)
	if deadlineSet {
		_ = conn.SetWriteDeadline(time.Time{})
	}
	w.connWriteMu.Unlock()

	if err != nil {
		w.handleWriteFailure(conn, msg)
		return fmt.Errorf("write message: %w", err)
	}
	return nil
}

func (w *Worker) handleWriteFailure(conn net.Conn, msg protocol.Message) {
	w.mu.Lock()
	if w.conn == conn {
		w.disconnected = true
	}
	w.mu.Unlock()
	w.buffer.Add(msg)
	_ = conn.Close()
}

// trySendHeartbeat sends a best-effort heartbeat with a short write deadline.
// The connWriteMu ensures the deadline+write+clear is atomic with respect to
// other writers, preventing deadline leakage. If the write doesn't complete
// within 200ms (e.g. blocked net.Pipe in tests), it gives up rather than
// stalling the context watcher. Production UDS writes complete in microseconds.
func (w *Worker) trySendHeartbeat(_ context.Context) {
	w.mu.Lock()
	beadID := w.beadID
	wt := w.worktree
	conn := w.conn
	disconnected := w.disconnected
	w.mu.Unlock()

	if disconnected {
		return
	}

	// Best-effort read of context_pct from worktree for dispatcher monitoring.
	var contextPct int
	if wt != "" {
		if data, err := os.ReadFile(filepath.Join(wt, protocol.OroDir, "context_pct")); err == nil { //nolint:gosec // path constructed internally
			if v, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
				contextPct = v
			}
		}
	}

	data, err := json.Marshal(protocol.Message{
		Type: protocol.MsgHeartbeat,
		Heartbeat: &protocol.HeartbeatPayload{
			BeadID:          beadID,
			WorkerID:        w.ID,
			ContextPct:      contextPct,
			ProtocolVersion: protocol.WorkerProtocolVersion,
			Capabilities:    []string{protocol.CapabilityReadyEvidenceV1},
		},
	})
	if err != nil {
		return
	}
	data = append(data, '\n')

	w.connWriteMu.Lock()
	_ = conn.SetWriteDeadline(time.Now().Add(workerMessageWriteTimeout))
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
			BeadID:          beadID,
			WorkerID:        w.ID,
			ContextPct:      contextPct,
			ProtocolVersion: protocol.WorkerProtocolVersion,
			Capabilities:    []string{protocol.CapabilityReadyEvidenceV1},
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

func (w *Worker) trySendSubprocessProgress(_ context.Context) {
	w.mu.Lock()
	procRunning := w.proc != nil && !w.subprocExitClosed
	startedAt := w.subprocStartedAt
	lastOutputAt := w.lastSubprocOutputAt
	beadID := w.beadID
	conn := w.conn
	disconnected := w.disconnected
	w.mu.Unlock()

	if !procRunning || startedAt.IsZero() {
		return
	}
	if disconnected {
		return
	}

	now := time.Now()
	data, err := json.Marshal(protocol.Message{
		Type: protocol.MsgStatus,
		Status: &protocol.StatusPayload{
			BeadID:   beadID,
			WorkerID: w.ID,
			State:    "running_progress",
			Result:   formatSubprocessProgressResult(now, startedAt, lastOutputAt),
		},
	})
	if err != nil {
		return
	}
	data = append(data, '\n')

	w.connWriteMu.Lock()
	_ = conn.SetWriteDeadline(time.Now().Add(workerMessageWriteTimeout))
	_, _ = conn.Write(data)
	_ = conn.SetWriteDeadline(time.Time{})
	w.connWriteMu.Unlock()
}

func formatSubprocessProgressResult(now, startedAt, lastOutputAt time.Time) string {
	lastOutputAge := now.Sub(startedAt)
	if !lastOutputAt.IsZero() {
		lastOutputAge = now.Sub(lastOutputAt)
	}
	return fmt.Sprintf(
		`{"command_age_ms":%d,"last_output_age_ms":%d}`,
		now.Sub(startedAt).Milliseconds(),
		lastOutputAge.Milliseconds(),
	)
}

func (w *Worker) subprocessExitSnapshotLocked() subprocessExitSnapshot {
	return subprocessExitSnapshot{
		Runtime:    w.runtime,
		Model:      w.model,
		ExitCode:   w.subprocExitCode,
		ExitError:  w.subprocExitErr,
		StderrTail: w.subprocStderrTail,
	}
}

func (w *Worker) sendSubprocessDied(snapshot subprocessExitSnapshot) error {
	return w.sendDone(false, formatSubprocessDiedOutput(snapshot), subprocessDiedReason, &protocol.SubprocessExitPayload{
		Runtime:    snapshot.Runtime,
		Model:      snapshot.Model,
		ExitCode:   snapshot.ExitCode,
		ExitError:  snapshot.ExitError,
		StderrTail: snapshot.StderrTail,
	})
}

// SendDone sends a DONE message to the Dispatcher with the quality gate result.
func (w *Worker) SendDone(ctx context.Context, qualityGatePassed bool, qgOutput string) error {
	return w.sendDone(qualityGatePassed, qgOutput, "", nil)
}

func (w *Worker) sendDone(qualityGatePassed bool, qgOutput, failureReason string, subprocessExit *protocol.SubprocessExitPayload) error {
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
			FailureReason:     failureReason,
			SubprocessExit:    subprocessExit,
		},
	})
}

func formatSubprocessDiedOutput(snapshot subprocessExitSnapshot) string {
	var b strings.Builder
	b.WriteString("oro worker failure\n")
	b.WriteString("reason: subprocess_died\n")
	if snapshot.Runtime != "" {
		fmt.Fprintf(&b, "runtime: %s\n", snapshot.Runtime)
	}
	if snapshot.Model != "" {
		fmt.Fprintf(&b, "model: %s\n", snapshot.Model)
	}
	fmt.Fprintf(&b, "exit_code: %d\n", snapshot.ExitCode)
	if snapshot.ExitError != "" {
		fmt.Fprintf(&b, "exit_error: %s\n", snapshot.ExitError)
	}
	if snapshot.StderrTail != "" {
		b.WriteString("stderr_tail:\n")
		b.WriteString(snapshot.StderrTail)
		if !strings.HasSuffix(snapshot.StderrTail, "\n") {
			b.WriteString("\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func trimLastLines(s string, maxLines int) string {
	s = strings.TrimRight(s, "\r\n")
	if s == "" || maxLines <= 0 {
		return s
	}
	lines := strings.Split(s, "\n")
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return strings.Join(lines, "\n")
}

// SendHandoff sends a HANDOFF message to the Dispatcher.
// It reads typed context files from .oro/ in the worktree to populate the
// HandoffPayload with learnings, decisions, files modified, and a context
// summary for cross-session memory persistence.
func (w *Worker) SendHandoff(ctx context.Context) error {
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
	ready := &protocol.ReadyForReviewPayload{
		BeadID:         w.beadID,
		WorkerID:       w.ID,
		AssignmentID:   w.assignmentID,
		Worktree:       w.worktree,
		QGEvidencePath: w.qgEvidencePath,
		TargetSHA:      w.targetSHA,
		ReadyAttempt:   "1",
	}
	if w.qgEvidence != nil {
		evidence := *w.qgEvidence
		ready.QGEvidence = &evidence
	}
	if w.qgEvidenceRef != nil {
		ref := *w.qgEvidenceRef
		ready.QGEvidenceRef = &ref
	}
	w.mu.Unlock()

	return w.sendMessage(protocol.Message{
		Type:           protocol.MsgReadyForReview,
		ReadyForReview: ready,
	})
}

// rebaseOntoTarget runs "git rebase <target>" in worktree so the branch picks
// up commits already on the target (e.g. a go.mod vuln-fix that landed on main
// after the worktree was branched). On conflict it aborts the rebase to leave
// the worktree clean. Returns nil when the worktree has no git repo (no-op).
func rebaseOntoTarget(ctx context.Context, worktree, target string) error {
	cmd := exec.CommandContext(ctx, "git", "rebase", target) //nolint:gosec // target is from trusted protocol payload
	cmd.Dir = worktree
	cmd.Env = processenv.ForWorkdir(os.Environ(), worktree)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	// Treat "not a git repository" as a no-op — the worktree may be plain dir.
	if strings.Contains(string(out), "not a git repository") {
		return nil
	}
	// Conflict or other rebase failure: abort to leave the worktree clean.
	abort := exec.CommandContext(ctx, "git", "rebase", "--abort") //nolint:gosec // constant args
	abort.Dir = worktree
	abort.Env = processenv.ForWorkdir(os.Environ(), worktree)
	_ = abort.Run()
	return fmt.Errorf("rebase onto %s: %w\n%s", target, err, out)
}

// findQualityGateScript locates quality_gate.sh in the worktree, trying the
// dispatcher-managed root quality_gate.sh first, then scripts/quality_gate.sh.
// If neither exists, it attempts a git restore before giving up.
func findQualityGateScript(ctx context.Context, worktree string) (string, error) {
	candidates := []string{
		filepath.Join(worktree, "quality_gate.sh"),
		filepath.Join(worktree, "scripts", "quality_gate.sh"),
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	// Neither found — try git restore for each candidate in order.
	gitPaths := []string{"quality_gate.sh", "scripts/quality_gate.sh"}
	for i, gitPath := range gitPaths {
		restoreCmd := exec.CommandContext(ctx, "git", "checkout", "HEAD", "--", gitPath) //nolint:gosec // gitPath is from hardcoded constant slice above
		restoreCmd.Dir = worktree
		restoreCmd.Env = processenv.ForWorkdir(os.Environ(), worktree)
		if restoreCmd.Run() == nil {
			if _, err := os.Stat(candidates[i]); err == nil {
				return candidates[i], nil
			}
		}
	}
	return "", fmt.Errorf("quality gate script not found in scripts/quality_gate.sh or quality_gate.sh (restore failed)")
}

// RunQualityGate executes ./quality_gate.sh in the given worktree directory.
// It returns (true, output, nil) if the script exits 0, (false, output, nil) if
// it exits non-zero, and (false, "", err) if the script cannot be found or started.
// Output contains combined stdout and stderr from the script.
// When skipMutation is true, ORO_SKIP_MUTATION=1 is set so the script skips
// the slow mutation-testing tiers. When false, --mutation-testing is passed
// explicitly; ambient ORO_RUN_MUTATION never enables mutation testing.
func RunQualityGate(ctx context.Context, worktree string, skipMutation bool) (passed bool, output string, err error) {
	// Canonical location is scripts/quality_gate.sh; fall back to root for legacy repos.
	scriptPath, statErr := findQualityGateScript(ctx, worktree)
	if statErr != nil {
		return false, "", statErr
	}

	args := []string{scriptPath}
	if !skipMutation {
		args = append(args, "--mutation-testing")
	}
	cmd := exec.CommandContext(ctx, "bash", args...) //nolint:gosec // script path constructed from worktree, not user input
	cmd.Dir = worktree
	cmd.Env = qualityGateEnv(worktree, skipMutation, "")

	out, err := cmd.CombinedOutput()
	output = string(out)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return false, output, fmt.Errorf("run quality gate canceled: %w", ctxErr)
	}
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

type commandProcess struct {
	cmd *exec.Cmd
}

// Wait waits for the wrapped command to exit.
func (p *commandProcess) Wait() error {
	if err := p.cmd.Wait(); err != nil {
		return fmt.Errorf("wait command: %w", err)
	}
	return nil
}

// Kill terminates the wrapped command process.
func (p *commandProcess) Kill() error {
	if p.cmd.Process == nil {
		return nil
	}
	if err := p.cmd.Process.Kill(); err != nil {
		return fmt.Errorf("kill command: %w", err)
	}
	return nil
}

func qualityGateEnv(worktree string, skipMutation bool, mutationBase string) []string {
	env := make([]string, 0, len(os.Environ())+1)
	for _, kv := range os.Environ() {
		if processenv.StripQualityGateEnv(kv) {
			continue
		}
		env = append(env, kv)
	}
	if skipMutation {
		env = append(env, "ORO_SKIP_MUTATION=1")
	}
	if mutationBase != "" {
		env = append(env, "ORO_MUTATION_BASE="+mutationBase)
	}
	return processenv.ForWorkdir(env, worktree)
}

// ClaudeSpawner is the production StreamingSpawner that invokes `claude -p`.
type ClaudeSpawner struct{}

// StreamFormat reports the stdout event format emitted by the Claude runtime.
func (s *ClaudeSpawner) StreamFormat() StreamFormat { return StreamFormatClaudeJSON }

// buildClaudeArgs constructs the argument slice for the claude command.
// When both ORO_HOME and ORO_PROJECT env vars are set, it appends
// --add-dir and --settings flags to point claude at the shared oro config.
func buildClaudeArgs(model, prompt string) []string {
	return buildClaudeArgsWithReasoning(model, "", prompt)
}

func buildClaudeArgsWithReasoning(model, reasoning, prompt string) []string {
	args := []string{"-p", prompt, "--model", model, "--verbose", "--output-format", "stream-json"}
	if reasoning != "" {
		args = append(args, "--effort", reasoning)
	}

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
func buildClaudeEnv(workdir string) []string {
	return buildClaudeEnvForExecution(workdir, WorkerExecutionContext{})
}

func buildClaudeEnvForExecution(workdir string, execution WorkerExecutionContext) []string {
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
	env = append(env, "ORO_WORKER=1")
	return EnvironmentForExecution(processenv.ForWorkdir(env, workdir), execution)
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
	return s.SpawnWithReasoning(ctx, model, "", prompt, workdir)
}

// SpawnWithReasoning starts a `claude -p` subprocess with the configured effort.
func (s *ClaudeSpawner) SpawnWithReasoning(ctx context.Context, model, reasoning, prompt, workdir string) (Process, io.ReadCloser, io.WriteCloser, error) {
	return s.SpawnWithLaunchPolicy(ctx, model, reasoning, prompt, workdir, LaunchPolicyDefault)
}

// SpawnWithLaunchPolicy starts Claude and verifies managed hook activation for
// read-only Oracle launches before returning the process.
func (s *ClaudeSpawner) SpawnWithLaunchPolicy(ctx context.Context, model, reasoning, prompt, workdir string, policy LaunchPolicy) (Process, io.ReadCloser, io.WriteCloser, error) {
	if policy != LaunchPolicyDefault && policy != LaunchPolicyReadOnly {
		return nil, nil, nil, fmt.Errorf("unknown launch policy %q", policy)
	}
	args := buildClaudeArgsWithReasoning(model, reasoning, prompt)
	cmd := exec.CommandContext(ctx, "claude", args...) //nolint:gosec // args are constructed internally by buildClaudeArgs, not user input
	cmd.Dir = workdir
	stderrTail := NewLineTailBuffer(100)
	cmd.Stderr = io.MultiWriter(os.Stderr, stderrTail)
	cmd.Env = EnvironmentForContext(ctx, buildClaudeEnvForExecution(workdir, WorkerExecutionContext{}))
	var probe *OracleHookProbe
	if policy == LaunchPolicyReadOnly {
		var err error
		probe, err = NewOracleHookProbe()
		if err != nil {
			return nil, nil, nil, err
		}
		cmd.Env = append(cmd.Env, probe.Environment())
	}

	// Open /dev/null for stdin to prevent the spawned process from inheriting parent stdin,
	// which can cause claude -p to hang if the parent's stdin is a pipe.
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("open /dev/null: %w", err)
	}
	defer devNull.Close() // fd is dup'd into child by Start(); safe to close our copy on return
	cmd.Stdin = devNull

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		if probe != nil {
			probe.remove()
		}
		return nil, nil, nil, fmt.Errorf("start claude: %w", err)
	}
	proc := Process(&CmdProcess{Cmd: cmd, Runtime: agentruntime.RuntimeClaude, Stderr: stderrTail})
	if probe != nil {
		replayable := NewReplayableProcess(proc)
		if err := probe.Await(ctx, replayable, 5*time.Second); err != nil {
			return nil, nil, nil, err
		}
		proc = replayable
	}
	return proc, stdoutPipe, nil, nil
}

// CmdProcess wraps *exec.Cmd to implement the Process interface.
type CmdProcess struct {
	Cmd     *exec.Cmd
	Runtime string
	Stderr  *LineTailBuffer
}

// Wait blocks until the subprocess exits.
func (p *CmdProcess) Wait() error {
	if err := p.Cmd.Wait(); err != nil {
		runtime := p.Runtime
		if runtime == "" {
			runtime = agentruntime.RuntimeClaude
		}
		return fmt.Errorf("%s process wait: %w", runtime, err)
	}
	return nil
}

// ExitCode returns the subprocess exit code after Wait has completed.
func (p *CmdProcess) ExitCode() int {
	if p.Cmd == nil || p.Cmd.ProcessState == nil {
		return 0
	}
	return p.Cmd.ProcessState.ExitCode()
}

// StderrTail returns the retained subprocess stderr tail.
func (p *CmdProcess) StderrTail() string {
	if p.Stderr == nil {
		return ""
	}
	return p.Stderr.String()
}

// Kill terminates the subprocess immediately.
func (p *CmdProcess) Kill() error {
	if p.Cmd.Process == nil {
		return nil
	}
	if err := p.Cmd.Process.Kill(); err != nil {
		return fmt.Errorf("kill claude process: %w", err)
	}
	return nil
}
