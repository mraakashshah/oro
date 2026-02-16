// Package dispatcher implements the Oro orchestrator — the core coordination
// engine that composes protocol, merge, worker, and ops packages into a
// unified runtime. The Dispatcher manages a UDS server for worker connections,
// SQLite WAL for runtime state, a priority queue from bd ready, worker
// lifecycle supervision, merge execution, ops agent spawning, command
// processing, and escalation to the Manager.
//
// The Dispatcher is INERT until it receives a "start" directive. After that
// it runs autonomously, polling for work and assigning beads to idle workers.
package dispatcher

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"oro/pkg/memory"
	"oro/pkg/merge"
	"oro/pkg/ops"
	"oro/pkg/protocol"

	"github.com/fsnotify/fsnotify"
)

// --- Dispatcher states ---

// State represents the dispatcher's operational state.
type State string

// Dispatcher state constants.
const (
	StateInert    State = "inert"    // Waiting for start directive.
	StateRunning  State = "running"  // Actively assigning work.
	StatePaused   State = "paused"   // Workers continue, no new assignments.
	StateStopping State = "stopping" // Finishing current work, no new assignments.
)

// --- Domain types ---

// Bead, BeadDetail, and model constants are now in pkg/protocol/types.go

// --- Interfaces for testability ---

// BeadSource provides ready work items. Production impl shells out to `bd ready`.
type BeadSource interface {
	Ready(ctx context.Context) ([]protocol.Bead, error)
	Show(ctx context.Context, id string) (*protocol.BeadDetail, error)
	Close(ctx context.Context, id string, reason string) error
	Create(ctx context.Context, title, beadType string, priority int, description, parent string) (string, error)
	Sync(ctx context.Context) error
	AllChildrenClosed(ctx context.Context, epicID string) (bool, error)
}

// WorktreeManager creates and removes git worktrees.
type WorktreeManager interface {
	Create(ctx context.Context, beadID string) (path string, branch string, err error)
	Remove(ctx context.Context, path string) error
	Prune(ctx context.Context) error
}

// Escalator sends messages to the Manager. Production impl uses tmux send-keys.
type Escalator interface {
	Escalate(ctx context.Context, msg string) error
}

// ProcessManager spawns and kills oro worker OS processes.
// Production implementations use exec.Command to run `oro worker`.
type ProcessManager interface {
	Spawn(id string) (*os.Process, error)
	Kill(id string) error
}

// TmuxSession provides operations for managing tmux panes.
// Used by pane monitor for restarting panes on handoff completion.
type TmuxSession interface {
	KillPane(paneTarget string) error
	RespawnPane(paneTarget, command string) error
}

// CodeIndex provides FTS5 code search for injecting relevant code into prompts.
type CodeIndex interface {
	FTS5Search(ctx context.Context, query string, limit int) ([]CodeChunk, error)
}

// CodeChunk represents a code search result.
type CodeChunk struct {
	FilePath  string
	Name      string
	Kind      string
	StartLine int
	EndLine   int
	Content   string
}

// --- Worker tracking ---

// WorkerState is now in pkg/protocol/types.go

// trackedWorker holds runtime state for a connected worker.
type trackedWorker struct {
	id             string
	conn           net.Conn
	state          protocol.WorkerState
	beadID         string
	epicID         string // parent epic ID if the assigned bead is a child of an epic
	worktree       string
	model          string // resolved model for the current bead assignment
	lastSeen       time.Time
	lastProgress   time.Time // last time meaningful progress was observed (DONE/READY_FOR_REVIEW/QG/first STATUS)
	contextPct     int       // context usage percentage from last heartbeat (0-100)
	encoder        *json.Encoder
	pendingMsgs    []protocol.Message // buffered messages for disconnected worker
	shutdownCancel context.CancelFunc // cancels previous shutdown goroutine (1nf.5)
}

// pendingHandoff holds context for a bead whose worker has been shut down
// during a ralph handoff. The next worker to connect will be assigned this
// bead+worktree instead of going through normal assignment.
type pendingHandoff struct {
	beadID   string
	worktree string
	model    string
}

// --- Config ---

// Config holds Dispatcher configuration.
type Config struct {
	SocketPath           string        // UDS socket path.
	DBPath               string        // SQLite database path.
	MaxWorkers           int           // Worker pool size (default 10).
	HeartbeatTimeout     time.Duration // Worker heartbeat timeout (default 45s).
	ProgressTimeout      time.Duration // Max time without meaningful progress before STUCK_WORKER escalation (default 15m).
	PollInterval         time.Duration // bd ready poll interval (default 10s).
	FallbackPollInterval time.Duration // Fallback poll interval for fsnotify safety net (default 60s).
	ShutdownTimeout      time.Duration // Graceful shutdown timeout (default 10s).
	ConsolidateAfterN    int           // Trigger context consolidation after N completed beads (default 5).
	PaneContextThreshold int           // Context percentage threshold for pane handoff (default 60).
	PaneMonitorInterval  time.Duration // Pane context_pct poll interval (default 5s).
}

func (c *Config) withDefaults() Config {
	out := *c
	if out.MaxWorkers == 0 {
		out.MaxWorkers = 10
	}
	if out.HeartbeatTimeout == 0 {
		out.HeartbeatTimeout = 45 * time.Second
	}
	if out.ProgressTimeout == 0 {
		out.ProgressTimeout = 15 * time.Minute
	}
	if out.PollInterval == 0 {
		out.PollInterval = 10 * time.Second
	}
	if out.FallbackPollInterval == 0 {
		out.FallbackPollInterval = 60 * time.Second
	}
	if out.ShutdownTimeout == 0 {
		out.ShutdownTimeout = 10 * time.Second
	}
	if out.ConsolidateAfterN == 0 {
		out.ConsolidateAfterN = 5
	}
	if out.PaneContextThreshold == 0 {
		out.PaneContextThreshold = 60
	}
	if out.PaneMonitorInterval == 0 {
		out.PaneMonitorInterval = 5 * time.Second
	}
	return out
}

// validate checks that all Config values are valid. Returns an error if any
// duration is <= 0 or if MaxWorkers is negative. Call this AFTER withDefaults().
func (c Config) validate() error {
	if c.MaxWorkers < 0 {
		return fmt.Errorf("MaxWorkers must be non-negative, got %d", c.MaxWorkers)
	}
	if c.HeartbeatTimeout <= 0 {
		return fmt.Errorf("HeartbeatTimeout must be positive, got %v", c.HeartbeatTimeout)
	}
	if c.ProgressTimeout <= 0 {
		return fmt.Errorf("ProgressTimeout must be positive, got %v", c.ProgressTimeout)
	}
	if c.PollInterval <= 0 {
		return fmt.Errorf("PollInterval must be positive, got %v", c.PollInterval)
	}
	if c.FallbackPollInterval <= 0 {
		return fmt.Errorf("FallbackPollInterval must be positive, got %v", c.FallbackPollInterval)
	}
	if c.ShutdownTimeout <= 0 {
		return fmt.Errorf("ShutdownTimeout must be positive, got %v", c.ShutdownTimeout)
	}
	return nil
}

// --- Dispatcher ---

// Dispatcher is the main orchestrator. Worker management and bead tracking
// are factored into embedded WorkerPool and BeadTracker structs whose fields
// are promoted so that existing callers (including tests) can access them
// directly (e.g. d.workers, d.attemptCounts). Both embedded structs share
// the Dispatcher-level mu for synchronisation.
type Dispatcher struct {
	cfg         Config
	db          *sql.DB
	merger      *merge.Coordinator
	ops         *ops.Spawner
	beads       BeadSource
	worktrees   WorktreeManager
	escalator   Escalator
	memories    *memory.Store
	codeIndex   CodeIndex // interface for FTS5 code search (nil means no search)
	procMgr     ProcessManager
	tmuxSession TmuxSession // interface for tmux pane operations (nil means no pane restart)

	// WorkerPool holds the connected-worker registry (embedded for field promotion).
	WorkerPool
	// BeadTracker holds per-bead counters and mappings (embedded for field promotion).
	BeadTracker

	mu                          sync.Mutex
	state                       State
	listener                    net.Listener
	focusedEpic                 string
	targetWorkers               int
	completionsSinceConsolidate int // counts completed beads since last context consolidation

	// beadsDir is the directory to watch for bead changes (defaults to protocol.BeadsDir)
	beadsDir string

	// panesDir is the directory to watch for pane context_pct files (defaults to ~/.oro/panes)
	panesDir string

	// signaledPanes tracks which panes have been signaled to avoid re-signaling
	signaledPanes map[string]bool

	// startTime records when Run() was called (for uptime).
	startTime time.Time

	// cachedQueueDepth stores the last-known count from beads.Ready() in the assign loop.
	cachedQueueDepth int

	// nowFunc allows tests to control time.
	nowFunc func() time.Time

	// testUnlockHook, if non-nil, is called after releasing the lock in
	// registerWorker/handleQGFailure (before memory.ForPrompt). Tests use
	// this to inject a synchronization point that guarantees a concurrent
	// deletion occurs during the unlock window.
	testUnlockHook func()

	// priorityBeads holds bead IDs that should be assigned before normal queue ordering.
	// Used by spawn-for directive to guarantee a specific bead gets the next idle worker.
	priorityBeads map[string]bool

	// shutdownCh is closed when a shutdown directive is received, causing Run() to exit.
	shutdownCh chan struct{}
	// shutdownAuthorized gates whether SIGTERM is honored by the signal handler.
	shutdownAuthorized atomic.Bool

	// wg tracks all goroutines spawned by Run() to ensure graceful shutdown
	wg sync.WaitGroup

	// acceptSem limits concurrent connection handlers in acceptLoop
	acceptSem chan struct{}
}

// New creates a Dispatcher. It does NOT start listening or polling — call Run().
// Returns nil and an error if the Config is invalid after applying defaults.
// codeIdx may be nil to disable code search context injection.
func New(cfg Config, db *sql.DB, merger *merge.Coordinator, opsSpawner *ops.Spawner, beads BeadSource, wt WorktreeManager, esc Escalator, codeIdx CodeIndex) (*Dispatcher, error) {
	resolved := cfg.withDefaults()
	if err := resolved.validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}
	return &Dispatcher{
		cfg:           resolved,
		db:            db,
		merger:        merger,
		ops:           opsSpawner,
		beads:         beads,
		worktrees:     wt,
		escalator:     esc,
		memories:      memory.NewStore(db),
		codeIndex:     codeIdx,
		state:         StateInert,
		targetWorkers: resolved.MaxWorkers,
		WorkerPool: WorkerPool{
			workers: make(map[string]*trackedWorker),
		},
		BeadTracker: BeadTracker{
			rejectionCounts:  make(map[string]int),
			handoffCounts:    make(map[string]int),
			attemptCounts:    make(map[string]int),
			pendingHandoffs:  make(map[string]*pendingHandoff),
			qgStuckTracker:   make(map[string]*qgHistory),
			escalatedBeads:   make(map[string]bool),
			worktreeFailures: make(map[string]time.Time),
			exhaustedBeads:   make(map[string]bool),
		},
		priorityBeads: make(map[string]bool),
		shutdownCh:    make(chan struct{}),
		beadsDir:      protocol.BeadsDir,
		panesDir:      filepath.Join(os.Getenv("HOME"), ".oro", "panes"),
		signaledPanes: make(map[string]bool),
		nowFunc:       time.Now,
		acceptSem:     make(chan struct{}, 100), // limit to 100 concurrent connection handlers
	}, nil
}

// safeGo runs fn in a tracked goroutine with panic recovery. If fn panics,
// the panic and stack trace are logged to the events table and the goroutine
// exits cleanly instead of crashing the process. The goroutine is tracked
// by d.wg for graceful shutdown.
func (d *Dispatcher) safeGo(fn func()) {
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		defer func() {
			if r := recover(); r != nil {
				_ = d.logEvent(context.Background(), "goroutine_panic", "dispatcher", "", "",
					fmt.Sprintf(`{"panic":%q,"stack":%q}`, fmt.Sprint(r), string(debug.Stack())))
			}
		}()
		fn()
	}()
}

// GetState returns the current dispatcher state.
func (d *Dispatcher) GetState() State {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.state
}

// setState transitions the dispatcher to a new state.
func (d *Dispatcher) setState(s State) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.state = s
}

// ShutdownAuthorized returns the atomic flag that gates SIGTERM handling.
// The signal handler checks this flag to decide whether to honor SIGTERM.
func (d *Dispatcher) ShutdownAuthorized() *atomic.Bool {
	return &d.shutdownAuthorized
}

// Run starts the Dispatcher event loop. It:
//  1. Initializes the SQLite schema
//  2. Starts the UDS listener
//  3. Polls for commands (directives) and ready beads
//  4. Monitors worker heartbeats
//
// Run blocks until ctx is cancelled.
func (d *Dispatcher) Run(ctx context.Context) error {
	d.mu.Lock()
	d.startTime = d.nowFunc()
	d.mu.Unlock()

	// Init schema
	if _, err := d.db.ExecContext(ctx, protocol.SchemaDDL); err != nil {
		return fmt.Errorf("init schema: %w", err)
	}

	// Prune orphaned worktrees from a previous crash. Errors are logged
	// but non-fatal — they must not prevent dispatcher startup.
	if pruneErr := d.worktrees.Prune(ctx); pruneErr != nil {
		_ = d.logEvent(ctx, "worktree_prune_failed", "dispatcher", "", "", pruneErr.Error())
	}

	// Restore in-memory tracking maps from active assignments persisted in SQLite.
	if err := d.restoreState(ctx); err != nil {
		return fmt.Errorf("restore state: %w", err)
	}

	// Clean up stale socket from a previous crash (if any). If another
	// dispatcher is actively listening, this returns an error so we don't
	// clobber it.
	if err := cleanStaleSocket(d.cfg.SocketPath); err != nil {
		return fmt.Errorf("stale socket check %s: %w", d.cfg.SocketPath, err)
	}

	// Start UDS listener
	ln, err := net.Listen("unix", d.cfg.SocketPath) //nolint:noctx // UDS bind is instant
	if err != nil {
		return fmt.Errorf("listen unix %s: %w", d.cfg.SocketPath, err)
	}
	// Restrict socket permissions to 0600 (owner-only access) to prevent
	// unauthorized local users from connecting and impersonating workers.
	if err := os.Chmod(d.cfg.SocketPath, 0o600); err != nil {
		_ = ln.Close()
		return fmt.Errorf("chmod socket %s: %w", d.cfg.SocketPath, err)
	}
	d.mu.Lock()
	d.listener = ln
	d.mu.Unlock()

	// Accept connections
	d.safeGo(func() { d.acceptLoop(ctx, ln) })

	// Bead assignment loop
	d.safeGo(func() { d.assignLoop(ctx) })

	// Heartbeat monitor
	d.safeGo(func() { d.heartbeatLoop(ctx) })

	// Pane context monitor
	d.safeGo(func() { d.paneMonitorLoop(ctx) })

	// Escalation retry loop — re-deliver unacked escalations every 2 minutes.
	d.safeGo(func() { d.escalationRetryLoop(ctx) })

	select {
	case <-ctx.Done():
	case <-d.shutdownCh:
	}

	// Close listener first so acceptLoop will exit
	_ = ln.Close()

	// --- Graceful shutdown ---
	d.shutdownWithTimeout()

	return nil
}

// shutdownWithTimeout orchestrates graceful shutdown with a hard timeout.
// It wraps shutdownSequence in a context with 2*ShutdownTimeout to prevent
// indefinite hangs if workers never respond to PREPARE_SHUTDOWN.
func (d *Dispatcher) shutdownWithTimeout() {
	// Wrap shutdownSequence in a hard timeout of 2*ShutdownTimeout to prevent
	// indefinite hangs if workers never respond to PREPARE_SHUTDOWN.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*d.cfg.ShutdownTimeout)
	defer shutdownCancel()

	shutdownDone := make(chan struct{})
	go func() {
		// Phase 1: cancel ops/merges, Phase 2: stop workers, Phase 3: remove worktrees.
		d.shutdownSequence()
		close(shutdownDone)
	}()

	select {
	case <-shutdownDone:
		// Shutdown sequence completed successfully
	case <-shutdownCtx.Done():
		// Hard timeout exceeded — force-close all connections and clear worker map
		d.mu.Lock()
		for id, w := range d.workers {
			_ = w.conn.Close()
			delete(d.workers, id)
		}
		d.mu.Unlock()
	}

	// Wait for all goroutines to finish with a 5s timeout
	done := make(chan struct{})
	go func() {
		d.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// All goroutines finished
	case <-time.After(5 * time.Second):
		// Timeout - goroutines did not finish in time
	}
}

// --- UDS server ---

// acceptLoop accepts new worker connections.
func (d *Dispatcher) acceptLoop(ctx context.Context, ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		// Acquire semaphore slot before spawning handler
		select {
		case d.acceptSem <- struct{}{}:
			d.safeGo(func() {
				defer func() { <-d.acceptSem }() // Release semaphore slot
				d.handleConn(ctx, conn)
			})
		case <-ctx.Done():
			_ = conn.Close()
			return
		}
	}
}

// handleConn reads line-delimited JSON messages from a worker connection.
func (d *Dispatcher) handleConn(ctx context.Context, conn net.Conn) {
	scanner := bufio.NewScanner(conn)
	// Configure scanner to accept messages up to MaxMessageSize (1MB).
	// Default scanner max is 64KB which is too small for large payloads.
	scanner.Buffer(make([]byte, 0, 64*1024), protocol.MaxMessageSize)
	var workerID string

	defer func() {
		_ = conn.Close()
		if workerID != "" {
			d.mu.Lock()
			delete(d.workers, workerID)
			d.mu.Unlock()
		}
	}()

	for scanner.Scan() {
		if ctx.Err() != nil {
			return
		}
		var msg protocol.Message
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			continue
		}

		// Handle DIRECTIVE messages from manager (short-lived connection).
		if msg.Type == protocol.MsgDirective {
			d.handleDirectiveWithACK(ctx, conn, msg)
			return // Manager disconnects after receiving ACK
		}

		// Extract workerID from the first message that carries one.
		if workerID == "" {
			workerID = extractWorkerID(msg)
			if workerID != "" {
				d.registerWorker(workerID, conn)
			}
		}

		d.handleMessage(ctx, workerID, msg)
	}
}

// extractWorkerID pulls the worker ID from any message payload.
func extractWorkerID(msg protocol.Message) string {
	switch {
	case msg.Heartbeat != nil:
		return msg.Heartbeat.WorkerID
	case msg.Status != nil:
		return msg.Status.WorkerID
	case msg.Done != nil:
		return msg.Done.WorkerID
	case msg.Handoff != nil:
		return msg.Handoff.WorkerID
	case msg.ReadyForReview != nil:
		return msg.ReadyForReview.WorkerID
	case msg.Reconnect != nil:
		return msg.Reconnect.WorkerID
	case msg.ShutdownApproved != nil:
		return msg.ShutdownApproved.WorkerID
	default:
		return ""
	}
}

// registerWorker, consumePendingHandoff → worker_pool.go

// --- Message handling ---

// extractBeadID extracts the bead ID from a message payload if present.
func extractBeadID(msg protocol.Message) string {
	switch msg.Type {
	case protocol.MsgHeartbeat:
		if msg.Heartbeat != nil {
			return msg.Heartbeat.BeadID
		}
	case protocol.MsgStatus:
		if msg.Status != nil {
			return msg.Status.BeadID
		}
	case protocol.MsgDone:
		if msg.Done != nil {
			return msg.Done.BeadID
		}
	case protocol.MsgHandoff:
		if msg.Handoff != nil {
			return msg.Handoff.BeadID
		}
	case protocol.MsgReadyForReview:
		if msg.ReadyForReview != nil {
			return msg.ReadyForReview.BeadID
		}
	case protocol.MsgReconnect:
		if msg.Reconnect != nil {
			return msg.Reconnect.BeadID
		}
	}
	return ""
}

// handleMessage dispatches an incoming worker message.
func (d *Dispatcher) handleMessage(ctx context.Context, workerID string, msg protocol.Message) {
	// Extract and validate bead ID from message payloads that carry one.
	beadID := extractBeadID(msg)

	// Validate bead ID if present (empty is allowed for some message types like SHUTDOWN_APPROVED).
	if beadID != "" {
		if err := protocol.ValidateBeadID(beadID); err != nil {
			_ = d.logEvent(ctx, "invalid_bead_id", workerID, beadID, workerID,
				fmt.Sprintf(`{"error":%q}`, err.Error()))
			return
		}
	}

	switch msg.Type {
	case protocol.MsgHeartbeat:
		d.handleHeartbeat(ctx, workerID, msg)
	case protocol.MsgStatus:
		d.handleStatus(ctx, workerID, msg)
	case protocol.MsgDone:
		d.handleDone(ctx, workerID, msg)
	case protocol.MsgHandoff:
		d.handleHandoff(ctx, workerID, msg)
	case protocol.MsgReadyForReview:
		d.handleReadyForReview(ctx, workerID, msg)
	case protocol.MsgReconnect:
		d.handleReconnect(ctx, workerID, msg)
	case protocol.MsgShutdownApproved:
		d.handleShutdownApproved(ctx, workerID, msg)
	}
}

func (d *Dispatcher) handleHeartbeat(ctx context.Context, workerID string, msg protocol.Message) {
	if msg.Heartbeat == nil {
		return
	}
	d.mu.Lock()
	if w, ok := d.workers[workerID]; ok {
		w.lastSeen = d.nowFunc()
		w.contextPct = msg.Heartbeat.ContextPct
	}
	d.mu.Unlock()

	_ = d.logEvent(ctx, "heartbeat", workerID, msg.Heartbeat.BeadID, workerID, "")
}

func (d *Dispatcher) handleStatus(ctx context.Context, workerID string, msg protocol.Message) {
	if msg.Status == nil {
		return
	}
	d.touchProgress(workerID)
	_ = d.logEvent(ctx, "status", workerID, msg.Status.BeadID, workerID,
		fmt.Sprintf(`{"state":%q,"result":%q}`, msg.Status.State, msg.Status.Result))
}

func (d *Dispatcher) handleDone(ctx context.Context, workerID string, msg protocol.Message) {
	if msg.Done == nil {
		return
	}
	beadID := msg.Done.BeadID

	d.touchProgress(workerID)
	_ = d.logEvent(ctx, "done", workerID, beadID, workerID, "")

	// Reject merge if quality gate did not pass — retry or escalate.
	if !msg.Done.QualityGatePassed {
		d.handleQGFailure(ctx, workerID, beadID, msg.Done.QGOutput)
		return
	}

	// Get worktree from tracked worker
	d.mu.Lock()
	w, ok := d.workers[workerID]
	var worktree, branch string
	if ok {
		worktree = w.worktree
		branch = protocol.BranchPrefix + beadID
		w.state = protocol.WorkerIdle
		w.beadID = ""
		w.epicID = ""
	}
	d.mu.Unlock()

	if !ok || worktree == "" {
		return
	}

	// Clear tracking state for completed bead.
	d.clearBeadTracking(beadID)

	// Merge in background
	d.safeGo(func() { d.mergeAndComplete(ctx, beadID, workerID, worktree, branch) })
}

// handleQGFailure processes a quality-gate failure: checks for stuck detection
// (repeated identical outputs), increments the attempt counter, escalates if
// either cap is reached, or re-assigns with feedback.
func (d *Dispatcher) handleQGFailure(ctx context.Context, workerID, beadID, qgOutput string) {
	d.touchProgress(workerID)

	// Create typed QualityGateError for logging and potential error discrimination
	qgErr := &protocol.QualityGateError{
		BeadID:   beadID,
		WorkerID: workerID,
		Output:   qgOutput,
		Attempt:  0, // Will be updated after lock
	}

	_ = d.logEvent(ctx, "quality_gate_rejected", workerID, beadID, workerID,
		fmt.Sprintf(`{"reason":"QualityGatePassed=false","error":%q}`, qgErr.Error()))

	// Check stuck detection: hash QGOutput and track consecutive identical hashes.
	if d.isQGStuck(beadID, qgOutput) {
		_ = d.logEvent(ctx, "qg_stuck_detected", workerID, beadID, workerID,
			fmt.Sprintf(`{"repeated_count":%d}`, maxStuckCount))
		d.escalate(ctx, protocol.FormatEscalation(protocol.EscStuck, beadID,
			fmt.Sprintf("QG output repeated %d times — worker stuck", maxStuckCount), qgOutput), beadID, workerID)
		d.clearBeadTracking(beadID)
		return
	}

	d.mu.Lock()
	d.attemptCounts[beadID]++
	attempt := d.attemptCounts[beadID]
	qgErr.Attempt = attempt

	if attempt >= maxQGRetries {
		d.mu.Unlock()
		d.persistBeadCount(ctx, beadID, "attempt_count", attempt)

		// Create a P0 bug bead so the failure is tracked as actionable work.
		p0Title := fmt.Sprintf("P0: QG exhausted for %s", beadID)
		p0Desc := fmt.Sprintf("Quality gate failed %d times. Last output:\n%s", attempt, qgOutput)
		newID, createErr := d.beads.Create(ctx, p0Title, "bug", 0, p0Desc, beadID)
		if createErr != nil {
			_ = d.logEvent(ctx, "p0_bead_create_failed", workerID, beadID, workerID, createErr.Error())
		} else {
			_ = d.logEvent(ctx, "p0_bead_created", workerID, beadID, workerID,
				fmt.Sprintf(`{"new_bead_id":%q}`, newID))
		}

		_ = d.completeAssignment(ctx, beadID)
		_ = d.logEvent(ctx, "qg_retry_escalated", workerID, beadID, workerID,
			fmt.Sprintf(`{"attempts":%d,"error":%q}`, attempt, qgErr.Error()))
		d.escalate(ctx, protocol.FormatEscalation(protocol.EscStuck, beadID,
			fmt.Sprintf("quality gate failed %d times", attempt), qgOutput), beadID, workerID)
		d.clearBeadTracking(beadID)

		// Mark bead as exhausted so filterAssignable blocks re-assignment.
		d.mu.Lock()
		d.exhaustedBeads[beadID] = true
		d.mu.Unlock()
		return
	}

	// Phase 1: Reserve the worker — heartbeat checker skips reserved workers.
	if w, ok := d.workers[workerID]; ok {
		w.state = protocol.WorkerReserved
	}
	d.mu.Unlock()

	d.persistBeadCount(ctx, beadID, "attempt_count", attempt)
	d.qgRetryWithReservation(ctx, workerID, beadID, qgOutput, attempt)
}

// withReservation executes a two-phase reservation pattern for worker re-assignment:
// Phase 1 (caller): Reserve the worker (set state to WorkerReserved) under lock.
// Phase 2 (this helper): Run ioFn outside lock, then verify reservation still valid
// and call assignFn under lock. The worker must already be in WorkerReserved state
// before calling this helper.
//
// ioFn performs I/O operations (e.g., memory retrieval) and returns context string.
// assignFn receives the worker and I/O result, updates state, and sends ASSIGN message.
// assignFn returns true if the assignment succeeded, false if it failed.
//
// Returns true if assignment succeeded, false if worker was disconnected or assignment failed.
func (d *Dispatcher) withReservation(workerID string, ioFn func() string, assignFn func(w *trackedWorker, memCtx string) bool) bool {
	// I/O phase: run outside lock to avoid blocking other operations.
	if d.testUnlockHook != nil {
		d.testUnlockHook()
	}
	memCtx := ioFn()

	d.mu.Lock()
	defer d.mu.Unlock()

	// Phase 2: Verify reservation still valid, then call assignFn.
	w, ok := d.workers[workerID]
	if !ok || w.state != protocol.WorkerReserved {
		return false
	}

	return assignFn(w, memCtx)
}

// qgRetryWithReservation performs the I/O phase (memory retrieval) and
// completes the two-phase reservation for a QG retry. The worker must already
// be in protocol.WorkerReserved state before this is called.
func (d *Dispatcher) qgRetryWithReservation(ctx context.Context, workerID, beadID, qgOutput string, attempt int) {
	success := d.withReservation(workerID,
		// I/O function: fetch memories outside lock
		func() string {
			return d.fetchBeadMemories(ctx, beadID)
		},
		// Assign function: update state and send message under lock
		func(w *trackedWorker, memCtx string) bool {
			// Escalate to opus if not already opus.
			if w.model != protocol.ModelOpus {
				w.model = protocol.ModelOpus
				d.attemptCounts[beadID] = 0 // Reset so opus gets fresh retries
			}

			if err := d.sendToWorker(w, protocol.Message{
				Type: protocol.MsgAssign,
				Assign: &protocol.AssignPayload{
					BeadID:        beadID,
					Worktree:      w.worktree,
					Model:         w.model,
					Attempt:       attempt,
					Feedback:      qgOutput,
					MemoryContext: memCtx,
				},
			}); err != nil {
				// Worker is unreachable — release the bead back to the ready pool.
				w.state = protocol.WorkerIdle
				w.beadID = ""
				w.epicID = ""
				_ = d.logEvent(ctx, "qg_retry_send_failed", workerID, beadID, workerID,
					fmt.Sprintf(`{"error":%q,"attempt":%d}`, err.Error(), attempt))
				_ = d.completeAssignment(ctx, beadID)
				return false
			}
			w.state = protocol.WorkerBusy
			w.beadID = beadID
			w.lastProgress = d.nowFunc()
			return true
		},
	)

	// If assignment failed, clean up tracking state outside the lock.
	if !success {
		d.clearBeadTracking(beadID)
	}
}

// fetchBeadMemories retrieves relevant memories for a bead (best-effort).
// Returns empty string if memories are unavailable.
func (d *Dispatcher) fetchBeadMemories(ctx context.Context, beadID string) string {
	if d.memories == nil {
		return ""
	}
	searchTerm := beadID
	detail, showErr := d.beads.Show(ctx, beadID)
	if showErr != nil {
		// Log BeadNotFoundError for visibility (best-effort, non-fatal)
		bnfErr := &protocol.BeadNotFoundError{BeadID: beadID}
		_ = d.logEvent(ctx, "bead_lookup_failed", "dispatcher", beadID, "",
			fmt.Sprintf(`{"error":%q}`, bnfErr.Error()))
	} else if detail != nil && detail.Title != "" {
		searchTerm = detail.Title
	}
	memCtx, _ := memory.ForPrompt(ctx, d.memories, nil, searchTerm, 0)
	return memCtx
}

// mergeAndComplete runs merge.Coordinator.Merge and handles the result.
func (d *Dispatcher) mergeAndComplete(ctx context.Context, beadID, workerID, worktree, branch string) {
	result, err := d.merger.Merge(ctx, merge.Opts{
		Branch:   branch,
		Worktree: worktree,
		BeadID:   beadID,
	})
	if err != nil {
		var conflictErr *merge.ConflictError
		if errors.As(err, &conflictErr) {
			// Spawn ops agent to resolve conflict
			resultCh := d.ops.ResolveMergeConflict(ctx, ops.MergeOpts{
				BeadID:        beadID,
				Worktree:      worktree,
				ConflictFiles: conflictErr.Files,
			})
			d.safeGo(func() { d.handleMergeConflictResult(ctx, beadID, workerID, worktree, resultCh) })
			_ = d.logEvent(ctx, "merge_conflict", "dispatcher", beadID, workerID,
				fmt.Sprintf(`{"files":%q}`, conflictErr.Files))
			return
		}
		// Non-conflict merge failure — escalate
		d.escalate(ctx, protocol.FormatEscalation(protocol.EscMergeConflict, beadID, "merge failed", err.Error()), beadID, workerID)
		_ = d.logEvent(ctx, "merge_failed", "dispatcher", beadID, workerID, err.Error())
		return
	}

	// Clean merge — close bead, complete assignment, remove worktree.
	_ = d.beads.Close(ctx, beadID, fmt.Sprintf("Merged: %s", result.CommitSHA))
	_ = d.completeAssignment(ctx, beadID)
	_ = d.logEvent(ctx, "merged", "dispatcher", beadID, workerID,
		fmt.Sprintf(`{"sha":%q}`, result.CommitSHA))

	// Extract learnings from event payloads for this bead (async, non-blocking).
	d.safeGo(func() { d.extractAndStoreLearnings(ctx, beadID) })

	// Auto-close parent epic if all children are completed.
	d.autoCloseEpicIfComplete(ctx, workerID)

	// Remove worktree after merge — safe because QG already ran before DONE.
	if err := d.worktrees.Remove(ctx, worktree); err != nil {
		_ = d.logEvent(ctx, "worktree_cleanup_failed", "dispatcher", beadID, workerID, err.Error())
	}

	// Trigger memory consolidation after every N bead completions.
	d.mu.Lock()
	d.completionsSinceConsolidate++
	shouldConsolidate := d.cfg.ConsolidateAfterN > 0 && d.completionsSinceConsolidate >= d.cfg.ConsolidateAfterN
	if shouldConsolidate {
		d.completionsSinceConsolidate = 0
	}
	d.mu.Unlock()

	if shouldConsolidate {
		d.safeGo(func() {
			merged, pruned, err := memory.Consolidate(ctx, d.memories, memory.ConsolidateOpts{})
			if err != nil {
				_ = d.logEvent(ctx, "memory_consolidation_failed", "dispatcher", "", "",
					fmt.Sprintf(`{"error":%q}`, err.Error()))
				return
			}
			_ = d.logEvent(ctx, "memory_consolidation", "dispatcher", "", "",
				fmt.Sprintf(`{"merged":%d,"pruned":%d}`, merged, pruned))
		})
	}
}

// autoCloseEpicIfComplete checks if the worker's bead has a parent epic and
// auto-closes the epic if all children are completed. Runs in a goroutine.
func (d *Dispatcher) autoCloseEpicIfComplete(ctx context.Context, workerID string) {
	d.mu.Lock()
	var epicID string
	if w, ok := d.workers[workerID]; ok {
		epicID = w.epicID
	}
	d.mu.Unlock()

	if epicID != "" {
		d.safeGo(func() {
			allClosed, err := d.beads.AllChildrenClosed(ctx, epicID)
			if err != nil {
				_ = d.logEvent(ctx, "epic_auto_close_check_failed", "dispatcher", epicID, workerID,
					fmt.Sprintf(`{"error":%q}`, err.Error()))
				return
			}
			if allClosed {
				_ = d.beads.Close(ctx, epicID, "All children completed")
				_ = d.logEvent(ctx, "epic_auto_closed", "dispatcher", epicID, workerID, "")
			}
		})
	}
}

// handleMergeConflictResult waits for the ops merge-conflict result and acts on it.
func (d *Dispatcher) handleMergeConflictResult(ctx context.Context, beadID, workerID, worktree string, resultCh <-chan ops.Result) {
	select {
	case <-ctx.Done():
		return
	case result := <-resultCh:
		switch result.Verdict {
		case ops.VerdictResolved:
			_ = d.logEvent(ctx, "merge_conflict_resolved", "ops", beadID, workerID, result.Feedback)
			// Resolution succeeded — retry the merge.
			d.mergeAndComplete(ctx, beadID, workerID, worktree, "main")
		default:
			// Resolution failed or unknown verdict — escalate.
			_ = d.logEvent(ctx, "merge_conflict_failed", "ops", beadID, workerID, result.Feedback)
			d.escalate(ctx, protocol.FormatEscalation(protocol.EscMergeConflict, beadID,
				"merge conflict resolution failed", result.Feedback), beadID, workerID)
		}
	}
}

// maxQGRetries is the number of quality-gate retry attempts before escalating
// to the Manager instead of re-assigning the bead to the worker.
const maxQGRetries = 3

// maxHandoffsBeforeDiagnosis is the number of ralph handoffs for the same bead
// before the dispatcher spawns a diagnosis agent instead of respawning.
const maxHandoffsBeforeDiagnosis = 2

func (d *Dispatcher) handleHandoff(ctx context.Context, workerID string, msg protocol.Message) {
	if msg.Handoff == nil {
		return
	}
	beadID := msg.Handoff.BeadID

	_ = d.logEvent(ctx, "handoff", workerID, beadID, workerID, "")

	// Persist learnings and decisions from the handoff payload as memories.
	d.persistHandoffContext(ctx, msg.Handoff)

	// Track handoff count per bead.
	d.mu.Lock()
	d.handoffCounts[beadID]++
	handoffCount := d.handoffCounts[beadID]
	d.mu.Unlock()

	d.persistBeadCount(ctx, beadID, "handoff_count", handoffCount)

	// Send SHUTDOWN to the old worker and capture worktree+model for respawn.
	d.mu.Lock()
	w, ok := d.workers[workerID]
	var worktree, model string
	if ok {
		worktree = w.worktree
		model = w.model
		_ = d.sendToWorker(w, protocol.Message{Type: protocol.MsgShutdown})
		w.state = protocol.WorkerShuttingDown // transient state — invisible to tryAssign
		w.beadID = ""
		w.epicID = ""
	}
	d.mu.Unlock()

	if worktree == "" {
		return
	}

	// On 2nd+ handoff for the same bead, spawn diagnosis agent instead of respawning.
	if handoffCount >= maxHandoffsBeforeDiagnosis {
		_ = d.logEvent(ctx, "diagnosis_spawned", "dispatcher", beadID, workerID,
			fmt.Sprintf(`{"handoff_count":%d}`, handoffCount))
		resultCh := d.ops.Diagnose(ctx, ops.DiagOpts{
			BeadID:   beadID,
			Worktree: worktree,
			Symptom:  fmt.Sprintf("worker stuck after %d ralph handoffs", handoffCount),
		})
		d.safeGo(func() { d.handleDiagnosisResult(ctx, beadID, workerID, resultCh) })

		// Create a continuation bead to capture remaining work from the exhausted handoff.
		contTitle := fmt.Sprintf("Continue: %s (handoff exhausted)", beadID)
		contDesc := fmt.Sprintf("Handoff exhausted after %d handoffs for %s.\n\nContext from last handoff:\n%s",
			handoffCount, beadID, msg.Handoff.ContextSummary)
		newID, createErr := d.beads.Create(ctx, contTitle, "task", 1, contDesc, beadID)
		if createErr != nil {
			_ = d.logEvent(ctx, "continuation_bead_create_failed", "dispatcher", beadID, workerID, createErr.Error())
		} else {
			_ = d.logEvent(ctx, "continuation_bead_created", "dispatcher", beadID, workerID,
				fmt.Sprintf(`{"new_bead_id":%q}`, newID))
		}

		return
	}

	d.respawnWorker(ctx, beadID, worktree, model)
}

// respawnWorker stores a pending handoff and spawns a fresh worker process.
func (d *Dispatcher) respawnWorker(ctx context.Context, beadID, worktree, model string) {
	d.mu.Lock()
	d.pendingHandoffs[beadID] = &pendingHandoff{
		beadID:   beadID,
		worktree: worktree,
		model:    model,
	}
	d.mu.Unlock()

	_ = d.logEvent(ctx, "handoff_pending", "dispatcher", beadID, "", worktree)

	if d.procMgr != nil {
		newID := fmt.Sprintf("worker-handoff-%d", d.nowFunc().UnixNano())
		if _, err := d.procMgr.Spawn(newID); err != nil {
			_ = d.logEvent(ctx, "handoff_spawn_failed", "dispatcher", beadID, newID, err.Error())
		} else {
			_ = d.logEvent(ctx, "handoff_spawned", "dispatcher", beadID, newID, worktree)
		}
	}
}

// handleDiagnosisResult waits for the ops diagnosis result. If diagnosis
// succeeds (non-empty feedback, no error), it logs the result. If diagnosis
// fails or is inconclusive, it escalates to the Manager.
func (d *Dispatcher) handleDiagnosisResult(ctx context.Context, beadID, workerID string, resultCh <-chan ops.Result) {
	select {
	case <-ctx.Done():
		return
	case result := <-resultCh:
		if result.Err != nil {
			// Diagnosis failed — escalate to manager.
			_ = d.logEvent(ctx, "diagnosis_escalated", "dispatcher", beadID, workerID,
				fmt.Sprintf(`{"error":%q}`, result.Err.Error()))
			d.escalate(ctx, protocol.FormatEscalation(protocol.EscStuck, beadID,
				"diagnosis failed", result.Err.Error()), beadID, workerID)
			d.clearBeadTracking(beadID)
			return
		}

		// Diagnosis succeeded — log feedback and escalate with diagnosis context.
		_ = d.logEvent(ctx, "diagnosis_complete", "dispatcher", beadID, workerID, result.Feedback)
		d.escalate(ctx, protocol.FormatEscalation(protocol.EscStuck, beadID,
			"diagnosis complete", result.Feedback), beadID, workerID)
		d.clearBeadTracking(beadID)
	}
}

// persistHandoffContext stores learnings and decisions from a HandoffPayload
// into the memory store for cross-session retrieval.
func (d *Dispatcher) persistHandoffContext(ctx context.Context, h *protocol.HandoffPayload) {
	if d.memories == nil {
		return
	}

	for _, learning := range h.Learnings {
		_, _ = d.memories.Insert(ctx, memory.InsertParams{
			Content:       learning,
			Type:          "lesson",
			Source:        "self_report",
			BeadID:        h.BeadID,
			WorkerID:      h.WorkerID,
			Confidence:    0.8,
			FilesModified: h.FilesModified,
		})
	}

	for _, decision := range h.Decisions {
		_, _ = d.memories.Insert(ctx, memory.InsertParams{
			Content:       decision,
			Type:          "decision",
			Source:        "self_report",
			BeadID:        h.BeadID,
			WorkerID:      h.WorkerID,
			Confidence:    0.8,
			FilesModified: h.FilesModified,
		})
	}

	// Persist structured session summary as type=summary for bead continuity.
	if h.Summary != nil {
		_, _ = d.memories.Insert(ctx, memory.InsertParams{
			Content:    h.Summary.FormatContent(),
			Type:       "summary",
			Source:     "self_report",
			BeadID:     h.BeadID,
			WorkerID:   h.WorkerID,
			Confidence: 0.9,
		})
	}
}

func (d *Dispatcher) handleReadyForReview(ctx context.Context, workerID string, msg protocol.Message) {
	if msg.ReadyForReview == nil {
		return
	}
	beadID := msg.ReadyForReview.BeadID

	d.touchProgress(workerID)
	_ = d.logEvent(ctx, "ready_for_review", workerID, beadID, workerID, "")

	d.mu.Lock()
	w, ok := d.workers[workerID]
	var worktree string
	if ok {
		w.state = protocol.WorkerReviewing
		worktree = w.worktree
	}
	d.mu.Unlock()

	if worktree == "" {
		return
	}

	// Look up bead details for the reviewer
	title, acceptance := d.lookupBeadDetail(ctx, beadID, workerID)

	// Spawn review ops agent
	resultCh := d.ops.Review(ctx, ops.ReviewOpts{
		BeadID:             beadID,
		BeadTitle:          title,
		Worktree:           worktree,
		AcceptanceCriteria: acceptance,
		BaseBranch:         "main",
		ProjectRoot:        worktree, // worktree is a full checkout with CLAUDE.md
	})

	// Handle review result asynchronously
	d.safeGo(func() { d.handleReviewResult(ctx, workerID, beadID, resultCh) })
}

// maxReviewRejections is the number of rejection cycles before escalating to
// the Manager instead of re-assigning the bead to the worker.
const maxReviewRejections = 2

// handleReviewResult waits for the ops review result and acts on it.
func (d *Dispatcher) handleReviewResult(ctx context.Context, workerID, beadID string, resultCh <-chan ops.Result) {
	select {
	case <-ctx.Done():
		return
	case result := <-resultCh:
		switch result.Verdict {
		case ops.VerdictApproved:
			_ = d.logEvent(ctx, "review_approved", "ops", beadID, workerID, result.Feedback)
			d.clearRejectionCount(beadID)

			// Capture anti-patterns from reviewer output
			patterns := ops.ExtractPatterns(result.Feedback)
			if len(patterns) > 0 {
				if err := d.appendReviewPatterns(ctx, beadID, workerID, patterns); err != nil {
					// Non-blocking: log the error but continue
					_ = d.logEvent(ctx, "append_review_patterns_failed", "ops", beadID, workerID, err.Error())
				}
			}

			// Notify worker to proceed to DONE.
			d.mu.Lock()
			w, ok := d.workers[workerID]
			if ok {
				_ = d.sendToWorker(w, protocol.Message{
					Type: protocol.MsgReviewResult,
					ReviewResult: &protocol.ReviewResultPayload{
						Verdict:  "approved",
						Feedback: result.Feedback,
					},
				})
			}
			d.mu.Unlock()
		case ops.VerdictRejected:
			d.handleReviewRejection(ctx, workerID, beadID, result.Feedback)
		default:
			_ = d.logEvent(ctx, "review_failed", "ops", beadID, workerID, result.Feedback)
			d.escalate(ctx, protocol.FormatEscalation(protocol.EscStuck, beadID, "review failed", result.Feedback), beadID, workerID)
			d.clearBeadTracking(beadID)
		}
	}
}

// handleReviewRejection processes a rejected review verdict: increments the
// rejection counter, escalates if the cap is reached, or re-assigns the bead
// to the worker with reviewer feedback using the two-phase reservation pattern.
func (d *Dispatcher) handleReviewRejection(ctx context.Context, workerID, beadID, feedback string) {
	_ = d.logEvent(ctx, "review_rejected", "ops", beadID, workerID, feedback)

	// Increment rejection counter and reserve worker in a single lock.
	d.mu.Lock()
	d.rejectionCounts[beadID]++
	count := d.rejectionCounts[beadID]

	if count > maxReviewRejections {
		// Reset worker to Idle so it can receive new work instead of
		// remaining stuck in WorkerReviewing with a stale beadID.
		if w, wOK := d.workers[workerID]; wOK {
			w.state = protocol.WorkerIdle
			w.beadID = ""
			w.epicID = ""
			w.worktree = ""
			w.model = ""
		}
		d.mu.Unlock()

		_ = d.logEvent(ctx, "review_escalated", "ops", beadID, workerID,
			fmt.Sprintf(`{"rejections":%d,"feedback":%q}`, count, feedback))
		d.escalate(ctx, protocol.FormatEscalation(protocol.EscStuck, beadID,
			fmt.Sprintf("review rejected %d times", count), feedback), beadID, workerID)
		d.clearBeadTracking(beadID)

		// Kill the worker subprocess so a fresh process can take over.
		if d.procMgr != nil {
			_ = d.procMgr.Kill(workerID)
		}
		return
	}

	// Phase 1: Reserve the worker — heartbeat checker skips reserved workers.
	if w, wOK := d.workers[workerID]; wOK {
		w.state = protocol.WorkerReserved
	}
	d.mu.Unlock()

	d.withReservation(workerID,
		// I/O function: fetch memories outside lock
		func() string {
			return d.fetchBeadMemories(ctx, beadID)
		},
		// Assign function: update state and send message under lock
		func(w *trackedWorker, memCtx string) bool {
			w.state = protocol.WorkerBusy
			w.lastProgress = d.nowFunc()
			_ = d.sendToWorker(w, protocol.Message{
				Type: protocol.MsgAssign,
				Assign: &protocol.AssignPayload{
					BeadID:        beadID,
					Worktree:      w.worktree,
					Model:         "claude-opus-4-6", // escalate to Opus after review rejection
					Feedback:      feedback,
					MemoryContext: memCtx,
					Attempt:       count,
				},
			})
			return true
		},
	)
}

// appendReviewPatterns appends captured anti-patterns to .claude/review-patterns.md
// in the main repository root. Returns error if directory is unwritable or file cannot be opened.
func (d *Dispatcher) appendReviewPatterns(ctx context.Context, beadID, workerID string, patterns []string) error {
	// Derive project root from beadsDir (which is the repo root's .beads/)
	root := filepath.Dir(d.beadsDir)
	patternsFile := filepath.Join(root, ".claude", "review-patterns.md")

	// Ensure .claude/ directory exists
	claudeDir := filepath.Dir(patternsFile)
	//nolint:gosec // directory permissions for project config
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		_ = d.logEvent(ctx, "append_review_patterns_failed", "ops", beadID, workerID, fmt.Sprintf("mkdir failed: %v", err))
		return fmt.Errorf("create .claude directory: %w", err)
	}

	//nolint:gosec // patternsFile is derived from trusted beadsDir
	f, err := os.OpenFile(patternsFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		_ = d.logEvent(ctx, "append_review_patterns_failed", "ops", beadID, workerID, fmt.Sprintf("open file failed: %v", err))
		return fmt.Errorf("open review-patterns.md: %w", err)
	}
	defer f.Close()

	for _, p := range patterns {
		if _, err := f.WriteString(p + "\n"); err != nil {
			_ = d.logEvent(ctx, "append_review_patterns_failed", "ops", beadID, workerID, fmt.Sprintf("write failed: %v", err))
			return fmt.Errorf("write pattern: %w", err)
		}
	}
	return nil
}

// clearRejectionCount, clearHandoffCount, clearBeadTracking, pruneStaleTracking → bead_tracker.go

func (d *Dispatcher) handleReconnect(ctx context.Context, workerID string, msg protocol.Message) {
	if msg.Reconnect == nil {
		return
	}

	// Validate the reconnect payload to prevent unbounded buffered events
	if err := msg.Reconnect.Validate(); err != nil {
		_ = d.logEvent(ctx, "reconnect_rejected", workerID, msg.Reconnect.BeadID, workerID, err.Error())
		return
	}

	_ = d.logEvent(ctx, "reconnect", workerID, msg.Reconnect.BeadID, workerID, msg.Reconnect.State)

	d.mu.Lock()
	w, ok := d.workers[workerID]
	if ok {
		w.beadID = msg.Reconnect.BeadID
		w.lastSeen = d.nowFunc()
		switch msg.Reconnect.State {
		case "running":
			w.state = protocol.WorkerBusy
			w.lastProgress = d.nowFunc()
		default:
			w.state = protocol.WorkerIdle
		}

		// Replay any pending messages that were buffered during disconnect
		for _, pending := range w.pendingMsgs {
			_ = d.sendToWorker(w, pending)
		}
		w.pendingMsgs = nil // Clear buffer after replay
	}
	d.mu.Unlock()

	// Process any buffered events
	for _, buffered := range msg.Reconnect.BufferedEvents {
		d.handleMessage(ctx, workerID, buffered)
	}
}

func (d *Dispatcher) handleShutdownApproved(ctx context.Context, workerID string, msg protocol.Message) {
	if msg.ShutdownApproved == nil {
		return
	}

	_ = d.logEvent(ctx, "shutdown_approved", workerID, "", workerID, "")

	// Send hard SHUTDOWN to finalize
	d.mu.Lock()
	w, ok := d.workers[workerID]
	if ok {
		_ = d.sendToWorker(w, protocol.Message{Type: protocol.MsgShutdown})
		w.state = protocol.WorkerIdle
		w.beadID = ""
		w.epicID = ""
	}
	d.mu.Unlock()
}

// handleDirectiveWithACK handles a DIRECTIVE message from the manager and sends an ACK response.
// This is used for short-lived manager connections that send a directive and expect an ACK.
func (d *Dispatcher) handleDirectiveWithACK(ctx context.Context, conn net.Conn, msg protocol.Message) {
	if msg.Directive == nil {
		return
	}

	dir := protocol.Directive(msg.Directive.Op)
	args := msg.Directive.Args
	ack := protocol.ACKPayload{OK: true}

	if !dir.Valid() {
		ack.OK = false
		ack.Detail = "invalid directive"
	} else {
		detail, err := d.applyDirective(dir, args)
		if err != nil {
			ack.OK = false
			ack.Detail = err.Error()
		} else {
			_ = d.logEvent(ctx, "directive", "manager", "", "",
				fmt.Sprintf(`{"directive":%q,"args":%q}`, msg.Directive.Op, args))
			ack.Detail = detail
		}
	}

	// Send ACK response
	ackMsg := protocol.Message{
		Type: protocol.MsgACK,
		ACK:  &ack,
	}
	data, err := json.Marshal(ackMsg)
	if err != nil {
		return
	}
	data = append(data, '\n')
	_, _ = conn.Write(data)
}

// GracefulShutdownWorker, shutdownWaitLoop, handleShutdownTimeout, checkShutdownApproved → worker_pool.go

// --- Priority queue / assignment loop ---

// assignLoop watches .beads/ directory and assigns work when files change.
// Falls back to 60s polling as a safety net.
func (d *Dispatcher) assignLoop(ctx context.Context) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		// Fallback to pure polling if fsnotify fails
		d.assignLoopPoll(ctx)
		return
	}
	defer func() { _ = watcher.Close() }()

	if err := watcher.Add(d.beadsDir); err != nil {
		// Fallback to pure polling if watch fails
		d.assignLoopPoll(ctx)
		return
	}

	// Fallback poll as safety net (default 60s)
	fallbackTicker := time.NewTicker(d.cfg.FallbackPollInterval)
	defer fallbackTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-d.shutdownCh:
			return
		case <-watcher.Events:
			// File changed in .beads/ directory
			d.tryAssign(ctx)
		case err := <-watcher.Errors:
			if err != nil {
				_ = d.logEvent(ctx, "watcher_error", "dispatcher", "", "", err.Error())
			}
		case <-fallbackTicker.C:
			// Safety net poll
			d.tryAssign(ctx)
		}
	}
}

// assignLoopPoll is a fallback polling loop when fsnotify is unavailable.
func (d *Dispatcher) assignLoopPoll(ctx context.Context) {
	ticker := time.NewTicker(d.cfg.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-d.shutdownCh:
			return
		case <-ticker.C:
			d.tryAssign(ctx)
		}
	}
}

// sortBeadsByPriority sorts beads: spawn-for beads first, then focused epic,
// then by priority (P0 first). Returns a snapshot of priorityBeads for cleanup.
func (d *Dispatcher) sortBeadsByPriority(beads []protocol.Bead) map[string]bool {
	d.mu.Lock()
	epic := d.focusedEpic
	pbSnapshot := make(map[string]bool, len(d.priorityBeads))
	for id := range d.priorityBeads {
		pbSnapshot[id] = true
	}
	d.mu.Unlock()

	sort.SliceStable(beads, func(i, j int) bool {
		iPriority := pbSnapshot[beads[i].ID]
		jPriority := pbSnapshot[beads[j].ID]
		if iPriority != jPriority {
			return iPriority
		}
		if epic != "" {
			iMatch := beads[i].Epic == epic
			jMatch := beads[j].Epic == epic
			if iMatch != jMatch {
				return iMatch
			}
		}
		return beads[i].Priority < beads[j].Priority
	})
	return pbSnapshot
}

// tryAssign attempts to assign ready beads to idle workers.
func (d *Dispatcher) tryAssign(ctx context.Context) {
	// Only assign in running state.
	if d.GetState() != StateRunning {
		return
	}

	// Reconcile worker pool size (spawns/removes workers to match target).
	d.reconcileScale()

	// Find idle workers and count total workers.
	d.mu.Lock()
	var idle []*trackedWorker
	totalWorkers := 0
	for _, w := range d.workers {
		totalWorkers++
		if w.state == protocol.WorkerIdle {
			idle = append(idle, w)
		}
	}
	d.mu.Unlock()

	// Poll for ready beads.
	allBeads, err := d.beads.Ready(ctx)
	if err != nil {
		return
	}

	// Cache queue depth for status reporting.
	d.mu.Lock()
	d.cachedQueueDepth = len(allBeads)
	d.mu.Unlock()

	beads := d.filterAssignable(allBeads)

	pbSnapshot := d.sortBeadsByPriority(beads)

	// Auto-scale: if we have assignable beads but no idle workers, scale up to MaxWorkers.
	d.maybeAutoScale(ctx, len(beads), len(idle))

	// Check for P0 beads when all workers are busy (priority contention).
	if len(idle) == 0 && totalWorkers > 0 {
		d.checkPriorityContention(ctx, beads, totalWorkers)
		return
	}

	// Assign beads to idle workers (1:1).
	for i, bead := range beads {
		if i >= len(idle) {
			break
		}
		d.assignBead(ctx, idle[i], bead)
		// Clean up priority bead after assignment.
		if pbSnapshot[bead.ID] {
			d.mu.Lock()
			delete(d.priorityBeads, bead.ID)
			d.mu.Unlock()
		}
	}
}

// filterAssignable returns beads eligible for assignment: excludes epics and
// beads with recent worktree creation failures (within cooldown window).
func (d *Dispatcher) filterAssignable(allBeads []protocol.Bead) []protocol.Bead {
	now := d.nowFunc()
	d.mu.Lock()
	defer d.mu.Unlock()

	// Collect bead IDs already assigned to busy/reserved workers.
	activeBeads := make(map[string]bool)
	for _, w := range d.workers {
		if w.beadID != "" && w.state != protocol.WorkerIdle {
			activeBeads[w.beadID] = true
		}
	}

	out := make([]protocol.Bead, 0, len(allBeads))
	for _, b := range allBeads {
		if b.Type == "epic" {
			continue
		}
		if failedAt, ok := d.worktreeFailures[b.ID]; ok && now.Sub(failedAt) < worktreeFailureCooldown {
			continue
		}
		if activeBeads[b.ID] {
			continue
		}
		if d.exhaustedBeads[b.ID] {
			continue
		}
		out = append(out, b)
	}
	return out
}

// recordAssignmentFailure marks a bead as having failed assignment (worktree
// creation error, missing acceptance criteria, etc). The bead will be skipped
// for worktreeFailureCooldown to prevent infinite retry loops.
func (d *Dispatcher) recordAssignmentFailure(beadID string) {
	d.mu.Lock()
	d.worktreeFailures[beadID] = d.nowFunc()
	d.mu.Unlock()
}

// checkPriorityContention escalates P0 beads that are queued while all workers
// are busy. Each bead is only escalated once (tracked by escalatedBeads).
func (d *Dispatcher) checkPriorityContention(ctx context.Context, beads []protocol.Bead, totalWorkers int) {
	for _, bead := range beads {
		if bead.Priority != 0 {
			continue
		}
		d.mu.Lock()
		alreadyEscalated := d.escalatedBeads[bead.ID]
		if !alreadyEscalated {
			d.escalatedBeads[bead.ID] = true
		}
		d.mu.Unlock()

		if !alreadyEscalated {
			msg := protocol.FormatEscalation(protocol.EscPriorityContention,
				bead.ID,
				fmt.Sprintf("P0 bead queued but all %d workers busy", totalWorkers),
				"")
			d.escalate(ctx, msg, bead.ID, "")
			_ = d.logEvent(ctx, "priority_contention", "dispatcher", bead.ID, "",
				fmt.Sprintf(`{"total_workers":%d}`, totalWorkers))
		}
	}
}

// assignBead creates a worktree and sends ASSIGN to the worker.
// If memories exist for the bead's description, they are included in the
// AssignPayload.MemoryContext field for cross-session continuity.
// checkBeadReady validates bead ID and acceptance criteria. Returns title,
// acceptance, and true if the bead is ready for assignment. Escalates to
// manager if AC is missing.
func (d *Dispatcher) checkBeadReady(ctx context.Context, bead protocol.Bead, workerID string) (title, acceptance string, ok bool) {
	if err := protocol.ValidateBeadID(bead.ID); err != nil {
		_ = d.logEvent(ctx, "invalid_bead_id", "dispatcher", bead.ID, workerID,
			fmt.Sprintf(`{"error":%q}`, err.Error()))
		return "", "", false
	}
	title, acceptance = d.lookupBeadDetail(ctx, bead.ID, workerID)
	if acceptance == "" {
		_ = d.logEvent(ctx, "missing_acceptance", "dispatcher", bead.ID, workerID,
			"bead has no acceptance criteria — escalating to manager")
		d.escalate(ctx, protocol.FormatEscalation(protocol.EscMissingAC, bead.ID,
			"bead has no acceptance criteria — please add AC via bd update",
			fmt.Sprintf("title: %s", title)), bead.ID, workerID)
		d.recordAssignmentFailure(bead.ID)
		return title, "", false
	}
	return title, acceptance, true
}

func (d *Dispatcher) assignBead(ctx context.Context, w *trackedWorker, bead protocol.Bead) {
	title, acceptance, ok := d.checkBeadReady(ctx, bead, w.id)
	if !ok {
		return
	}

	d.mu.Lock()
	delete(d.escalatedBeads, bead.ID)
	d.mu.Unlock()

	worktree, branch, err := d.worktrees.Create(ctx, bead.ID)
	if err != nil {
		_ = d.logEvent(ctx, "worktree_error", "dispatcher", bead.ID, w.id, err.Error())
		d.recordAssignmentFailure(bead.ID)
		return
	}

	_ = d.createAssignment(ctx, bead.ID, w.id, worktree)
	_ = d.logEvent(ctx, "assign", "dispatcher", bead.ID, w.id,
		fmt.Sprintf(`{"worktree":%q,"branch":%q}`, worktree, branch))
	var memCtx string
	if d.memories != nil {
		memCtx, _ = memory.ForPrompt(ctx, d.memories, nil, bead.Title, 0)
	}
	var codeCtx string
	if d.codeIndex != nil {
		chunks, err := d.codeIndex.FTS5Search(ctx, bead.Title, 5)
		if err == nil && len(chunks) > 0 {
			codeCtx = formatCodeChunks(chunks)
		}
	}
	d.mu.Lock()
	w.state = protocol.WorkerBusy
	w.beadID = bead.ID
	w.epicID = bead.Epic // store parent epic ID for auto-close on merge
	w.worktree = worktree
	w.model = bead.ResolveModel()
	w.lastProgress = d.nowFunc()
	err = d.sendToWorker(w, protocol.Message{
		Type: protocol.MsgAssign,
		Assign: &protocol.AssignPayload{
			BeadID:             bead.ID,
			Worktree:           worktree,
			Model:              bead.ResolveModel(),
			MemoryContext:      memCtx,
			CodeSearchContext:  codeCtx,
			Title:              title,
			AcceptanceCriteria: acceptance,
		},
	})
	if err != nil {
		w.state = protocol.WorkerIdle
		w.beadID = ""
		w.epicID = ""
		w.worktree = ""
		w.model = ""
	}
	d.mu.Unlock()
	if err != nil {
		_ = d.worktrees.Remove(ctx, worktree)
		_ = d.logEvent(ctx, "worktree_cleanup", "dispatcher", bead.ID, w.id, err.Error())
	}
}

// lookupBeadDetail retrieves the title and acceptance criteria for a bead (best-effort).
func (d *Dispatcher) lookupBeadDetail(ctx context.Context, beadID, workerID string) (title, acceptance string) {
	detail, err := d.beads.Show(ctx, beadID)
	if err != nil {
		bnfErr := &protocol.BeadNotFoundError{BeadID: beadID}
		_ = d.logEvent(ctx, "bead_lookup_failed", "dispatcher", beadID, workerID,
			fmt.Sprintf(`{"error":%q}`, bnfErr.Error()))
		return "", ""
	}
	if detail != nil {
		return detail.Title, detail.AcceptanceCriteria
	}
	return "", ""
}

// workerStatus holds per-worker health info for the enriched status response.
type workerStatus struct {
	ID               string  `json:"id"`
	State            string  `json:"state"`
	BeadID           string  `json:"bead_id,omitempty"`
	LastProgressSecs float64 `json:"last_progress_secs"`
	ContextPct       int     `json:"context_pct"`
}

// statusResponse is the JSON structure returned by the status directive.
type statusResponse struct {
	State       string            `json:"state"`
	PID         int               `json:"pid"`
	WorkerCount int               `json:"worker_count"`
	QueueDepth  int               `json:"queue_depth"`
	Assignments map[string]string `json:"assignments"`
	FocusedEpic string            `json:"focused_epic,omitempty"`

	// Enriched fields (oro-vii8.1)
	Workers             []workerStatus `json:"workers"`
	ActiveCount         int            `json:"active_count"`
	IdleCount           int            `json:"idle_count"`
	TargetCount         int            `json:"target_count"`
	UptimeSeconds       float64        `json:"uptime_seconds"`
	PendingHandoffCount int            `json:"pending_handoff_count"`
	AttemptCounts       map[string]int `json:"attempt_counts,omitempty"`
	ProgressTimeoutSecs float64        `json:"progress_timeout_secs"`
}

// applyDirective transitions the dispatcher state machine and returns a detail
// string for the ACK response. Returns an error for invalid args (e.g. scale).
func (d *Dispatcher) applyDirective(dir protocol.Directive, args string) (string, error) {
	switch dir {
	case protocol.DirectiveScale:
		return d.applyScaleDirective(args)
	case protocol.DirectiveKillWorker:
		return d.applyKillWorker(args)
	case protocol.DirectiveSpawnFor:
		return d.applySpawnFor(args)
	case protocol.DirectiveRestartWorker:
		return d.applyRestartWorker(args)
	case protocol.DirectivePendingEscalations:
		return d.applyPendingEscalations()
	case protocol.DirectiveAckEscalation:
		return d.applyAckEscalation(args)
	case protocol.DirectiveStart:
		d.setState(StateRunning)
		return "started", nil
	case protocol.DirectiveStop:
		return "", fmt.Errorf("stop directive disabled; use 'oro stop' for graceful shutdown")
	case protocol.DirectivePause:
		d.setState(StatePaused)
		return "paused", nil
	case protocol.DirectiveResume:
		return d.applyResume()
	case protocol.DirectiveStatus:
		return d.buildStatusJSON(), nil
	case protocol.DirectiveFocus:
		return d.applyFocus(args)
	case protocol.DirectiveShutdown:
		// Reject shutdown via UDS directive — agents can bypass ORO_ROLE guards.
		// Legitimate shutdown uses SIGINT (oro stop) which the daemon always honors.
		return "", fmt.Errorf("shutdown directive rejected; use 'oro stop' (sends SIGINT)")
	default:
		return fmt.Sprintf("applied %s", dir), nil
	}
}

// applyResume transitions the dispatcher from paused to running.
func (d *Dispatcher) applyResume() (string, error) {
	if d.GetState() == StateRunning {
		return "already running", nil
	}
	d.setState(StateRunning)
	return "resumed", nil
}

// applyFocus sets the focused epic and resumes the dispatcher if paused.
func (d *Dispatcher) applyFocus(args string) (string, error) {
	d.mu.Lock()
	d.focusedEpic = args
	d.mu.Unlock()
	if d.GetState() != StateRunning {
		d.setState(StateRunning)
	}
	if args == "" {
		return "focus cleared", nil
	}
	return fmt.Sprintf("focused on %s", args), nil
}

// buildStatusJSON constructs the status response JSON string.
// snapshotWorkers builds the per-worker status slice, assignments map, and
// active/idle counts. Caller must hold d.mu.
func (d *Dispatcher) snapshotWorkers(now time.Time) (workers []workerStatus, assignments map[string]string, active, idle int) {
	assignments = make(map[string]string, len(d.workers))
	workers = make([]workerStatus, 0, len(d.workers))
	for id, w := range d.workers {
		if w.beadID != "" {
			assignments[id] = w.beadID
		}
		var progressSecs float64
		if !w.lastProgress.IsZero() {
			progressSecs = now.Sub(w.lastProgress).Seconds()
		}
		workers = append(workers, workerStatus{
			ID:               id,
			State:            string(w.state),
			BeadID:           w.beadID,
			LastProgressSecs: progressSecs,
			ContextPct:       w.contextPct,
		})
		if w.state == protocol.WorkerBusy || w.state == protocol.WorkerReserved {
			active++
		} else {
			idle++
		}
	}
	return workers, assignments, active, idle
}

func (d *Dispatcher) buildStatusJSON() string {
	now := d.nowFunc()

	d.mu.Lock()
	workers, assignments, activeCount, idleCount := d.snapshotWorkers(now)

	// Copy attempt counts.
	var attemptCounts map[string]int
	if len(d.attemptCounts) > 0 {
		attemptCounts = make(map[string]int, len(d.attemptCounts))
		for k, v := range d.attemptCounts {
			attemptCounts[k] = v
		}
	}

	resp := statusResponse{
		State:               string(d.state),
		PID:                 os.Getpid(),
		WorkerCount:         len(d.workers),
		QueueDepth:          d.cachedQueueDepth,
		Assignments:         assignments,
		FocusedEpic:         d.focusedEpic,
		Workers:             workers,
		ActiveCount:         activeCount,
		IdleCount:           idleCount,
		TargetCount:         d.targetWorkers,
		UptimeSeconds:       now.Sub(d.startTime).Seconds(),
		PendingHandoffCount: len(d.pendingHandoffs),
		AttemptCounts:       attemptCounts,
		ProgressTimeoutSecs: d.cfg.ProgressTimeout.Seconds(),
	}
	d.mu.Unlock()

	data, err := json.Marshal(resp)
	if err != nil {
		return fmt.Sprintf(`{"error":%q}`, err.Error())
	}
	return string(data)
}

// applyScaleDirective parses the target count from args, stores it, and
// calls reconcileScale. Returns the ACK detail string.
func (d *Dispatcher) applyScaleDirective(args string) (string, error) {
	target, err := strconv.Atoi(args)
	if err != nil {
		return "", fmt.Errorf("invalid scale args %q: %w", args, err)
	}
	if target < 0 {
		return "", fmt.Errorf("invalid scale target %d: must be non-negative", target)
	}

	d.mu.Lock()
	d.targetWorkers = target
	connected := len(d.workers)
	d.mu.Unlock()

	detail := d.reconcileScale()
	if detail == "" {
		detail = fmt.Sprintf("target=%d, current=%d, no change", target, connected)
	}
	return detail, nil
}

// applyKillWorker terminates a specific worker, returns its bead to the ready
// queue, and decrements targetWorkers. Returns an error if args is empty or
// the worker ID is not found.
func (d *Dispatcher) applyKillWorker(args string) (string, error) {
	if args == "" {
		return "", fmt.Errorf("worker ID required")
	}

	workerID := args
	ctx := context.Background()

	d.mu.Lock()
	w, ok := d.workers[workerID]
	if !ok {
		d.mu.Unlock()
		return "", fmt.Errorf("worker not found")
	}

	// Capture bead ID before removing worker
	beadID := w.beadID

	// Close connection and remove worker from pool
	_ = w.conn.Close()
	delete(d.workers, workerID)

	// Decrement target count
	if d.targetWorkers > 0 {
		d.targetWorkers--
	}
	d.mu.Unlock()

	// Return bead to queue by completing the assignment
	if beadID != "" {
		_ = d.completeAssignment(ctx, beadID)
		_ = d.logEvent(ctx, "worker_killed", "dispatcher", beadID, workerID,
			`{"reason":"kill-worker directive"}`)
	}

	return fmt.Sprintf("worker %s killed", workerID), nil
}

// applySpawnFor spawns a dedicated worker for a specific bead. The bead is
// added to priorityBeads so tryAssign assigns it before normal queue ordering.
func (d *Dispatcher) applySpawnFor(args string) (string, error) {
	if args == "" {
		return "", fmt.Errorf("bead ID required")
	}
	beadID := args

	d.mu.Lock()
	for _, w := range d.workers {
		if w.beadID == beadID {
			workerID := w.id
			d.mu.Unlock()
			return "", fmt.Errorf("bead %s already assigned to %s", beadID, workerID)
		}
	}
	d.priorityBeads[beadID] = true
	d.targetWorkers++
	d.mu.Unlock()

	if d.procMgr == nil {
		d.mu.Lock()
		delete(d.priorityBeads, beadID)
		d.targetWorkers--
		d.mu.Unlock()
		return "", fmt.Errorf("no process manager configured")
	}

	newID := fmt.Sprintf("worker-spawnfor-%d", d.nowFunc().UnixNano())
	if _, err := d.procMgr.Spawn(newID); err != nil {
		d.mu.Lock()
		delete(d.priorityBeads, beadID)
		d.targetWorkers--
		d.mu.Unlock()
		return "", fmt.Errorf("spawn failed: %w", err)
	}

	_ = d.logEvent(context.Background(), "spawn_for", "dispatcher", beadID, newID, "")
	return fmt.Sprintf("spawned worker %s for bead %s", newID, beadID), nil
}

// applyRestartWorker terminates a specific worker, returns its bead to the
// ready queue, spawns a new worker with the same ID, and keeps targetWorkers
// unchanged. Returns an error if args is empty, the worker ID is not found,
// or spawning the new worker fails.
func (d *Dispatcher) applyRestartWorker(args string) (string, error) {
	if args == "" {
		return "", fmt.Errorf("worker ID required")
	}

	workerID := args
	ctx := context.Background()

	d.mu.Lock()
	w, ok := d.workers[workerID]
	if !ok {
		d.mu.Unlock()
		return "", fmt.Errorf("worker not found")
	}

	// Capture bead ID before removing worker
	beadID := w.beadID

	// Close connection and remove worker from pool
	_ = w.conn.Close()
	delete(d.workers, workerID)

	// Target count remains unchanged (unlike kill-worker)
	procMgr := d.procMgr
	d.mu.Unlock()

	// Return bead to queue by completing the assignment
	if beadID != "" {
		_ = d.completeAssignment(ctx, beadID)
		_ = d.logEvent(ctx, "worker_restarted", "dispatcher", beadID, workerID,
			`{"reason":"restart-worker directive"}`)
	}

	// Spawn new worker process with same ID
	if procMgr != nil {
		_, err := procMgr.Spawn(workerID)
		if err != nil {
			_ = d.logEvent(ctx, "worker_spawn_failed", "dispatcher", beadID, workerID,
				fmt.Sprintf(`{"error":%q}`, err.Error()))
			return "", fmt.Errorf("spawn new worker: %w", err)
		}
	}

	return fmt.Sprintf("worker %s restarted", workerID), nil
}

// maybeAutoScale increases targetWorkers when assignable beads exist but no
// idle workers are available. Scales up to min(queue depth, MaxWorkers).
func (d *Dispatcher) maybeAutoScale(ctx context.Context, queueDepth, idleCount int) {
	if queueDepth == 0 || idleCount > 0 {
		return
	}

	d.mu.Lock()
	currentTarget := d.targetWorkers
	maxWorkers := d.cfg.MaxWorkers
	d.mu.Unlock()

	if currentTarget >= maxWorkers {
		return
	}

	// Scale to min(queue depth, MaxWorkers)
	newTarget := queueDepth
	if newTarget > maxWorkers {
		newTarget = maxWorkers
	}

	if newTarget > currentTarget {
		d.mu.Lock()
		d.targetWorkers = newTarget
		d.mu.Unlock()
		d.reconcileScale()
		_ = d.logEvent(ctx, "auto_scale", "dispatcher", "", "",
			fmt.Sprintf("scaled to %d workers (queue depth: %d)", newTarget, queueDepth))
	}
}

// reconcileScale compares target vs connected workers and spawns or shuts down
// workers to reach the target. Returns a detail string describing the action taken.
func (d *Dispatcher) reconcileScale() string {
	d.mu.Lock()
	target := d.targetWorkers
	connected := len(d.workers)
	d.mu.Unlock()

	switch {
	case connected < target:
		return d.scaleUp(target, connected)
	case connected > target:
		return d.scaleDown(target, connected)
	default:
		return ""
	}
}

// scaleUp spawns (target - connected) new worker processes.
func (d *Dispatcher) scaleUp(target, connected int) string {
	toSpawn := target - connected
	if d.procMgr == nil {
		return fmt.Sprintf("target=%d, need %d workers but no ProcessManager configured", target, toSpawn)
	}

	spawned := 0
	for i := 0; i < toSpawn; i++ {
		id := fmt.Sprintf("worker-%d-%d", time.Now().UnixNano(), i)
		if _, err := d.procMgr.Spawn(id); err != nil {
			continue
		}
		spawned++
	}
	return fmt.Sprintf("target=%d, spawning %d", target, spawned)
}

// scaleDown initiates graceful shutdown for excess workers, preferring idle
// workers first, then newest busy workers.
func (d *Dispatcher) scaleDown(target, connected int) string {
	toRemove := connected - target

	d.mu.Lock()
	// Partition into idle and busy workers.
	var idle, busy []string
	for id, w := range d.workers {
		if w.state == protocol.WorkerIdle {
			idle = append(idle, id)
		} else {
			busy = append(busy, id)
		}
	}
	d.mu.Unlock()

	// Build removal list: idle first, then busy (newest = end of slice).
	var victims []string
	victims = append(victims, idle...)
	victims = append(victims, busy...)

	// Trim to the number we need to remove.
	if len(victims) > toRemove {
		victims = victims[:toRemove]
	}

	for _, id := range victims {
		d.GracefulShutdownWorker(id, d.cfg.ShutdownTimeout)
	}

	return fmt.Sprintf("target=%d, shutting down %d", target, len(victims))
}

// heartbeatLoop, checkHeartbeats → worker_pool.go

// --- SQLite helpers ---

func (d *Dispatcher) logEvent(ctx context.Context, evType, source, beadID, workerID, payload string) error {
	_, err := d.db.ExecContext(ctx,
		`INSERT INTO events (type, source, bead_id, worker_id, payload) VALUES (?, ?, ?, ?, ?)`,
		evType, source, beadID, workerID, payload)
	if err != nil {
		return fmt.Errorf("log event: %w", err)
	}
	return nil
}

// logEventLocked is logEvent but expects the caller already holds d.mu. It runs
// the SQL in a goroutine to avoid blocking while holding the lock.
func (d *Dispatcher) logEventLocked(ctx context.Context, evType, source, beadID, workerID, payload string) error {
	_, err := d.db.ExecContext(ctx,
		`INSERT INTO events (type, source, bead_id, worker_id, payload) VALUES (?, ?, ?, ?, ?)`,
		evType, source, beadID, workerID, payload)
	if err != nil {
		return fmt.Errorf("log event: %w", err)
	}
	return nil
}

// escalate sends a message to the Manager via the escalator and logs any
// delivery failures to the events table. This prevents silent failures when
// the tmux session is dead.
//
// For escalation types that have playbooks (STUCK_WORKER, MERGE_CONFLICT,
// PRIORITY_CONTENTION, MISSING_AC), it also spawns a one-shot claude -p
// agent to take corrective action autonomously.
func (d *Dispatcher) escalate(ctx context.Context, msg, beadID, workerID string) {
	// Persist escalation to SQLite before attempting tmux delivery.
	escType := ""
	if t := parseEscalationType(msg); t != "" {
		escType = t
	}
	_, _ = d.db.ExecContext(ctx,
		`INSERT INTO escalations (type, bead_id, worker_id, message) VALUES (?, ?, ?, ?)`,
		escType, beadID, workerID, msg)

	if err := d.escalator.Escalate(ctx, msg); err != nil {
		_ = d.logEvent(ctx, "escalation_failed", "dispatcher", beadID, workerID,
			fmt.Sprintf(`{"error":%q,"message":%q}`, err.Error(), msg))
	}

	// Spawn one-shot manager agent for actionable escalation types.
	if d.ops != nil {
		if escType != "" {
			d.spawnEscalationOneShot(ctx, escType, beadID, workerID, msg)
		}
	}
}

// applyPendingEscalations returns all unacked escalations as JSON.
func (d *Dispatcher) applyPendingEscalations() (string, error) {
	rows, err := d.db.QueryContext(context.Background(),
		`SELECT id, type, bead_id, worker_id, message, status, created_at, retry_count
		 FROM escalations WHERE status = 'pending' ORDER BY id`)
	if err != nil {
		return "", fmt.Errorf("query pending escalations: %w", err)
	}
	defer rows.Close()

	var escs []protocol.Escalation
	for rows.Next() {
		var e protocol.Escalation
		if err := rows.Scan(&e.ID, &e.Type, &e.BeadID, &e.WorkerID, &e.Message, &e.Status, &e.CreatedAt, &e.RetryCount); err != nil {
			return "", fmt.Errorf("scan escalation: %w", err)
		}
		escs = append(escs, e)
	}

	b, err := json.Marshal(escs)
	if err != nil {
		return "", fmt.Errorf("marshal escalations: %w", err)
	}
	return string(b), nil
}

// applyAckEscalation marks an escalation as acknowledged by ID.
func (d *Dispatcher) applyAckEscalation(args string) (string, error) {
	id := strings.TrimSpace(args)
	if id == "" {
		return "", fmt.Errorf("ack-escalation requires an escalation ID")
	}

	res, err := d.db.ExecContext(context.Background(),
		`UPDATE escalations SET status = 'acked', acked_at = datetime('now') WHERE id = ? AND status = 'pending'`,
		id)
	if err != nil {
		return "", fmt.Errorf("ack escalation: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Sprintf("escalation %s not found or already acked", id), nil
	}
	return fmt.Sprintf("acked escalation %s", id), nil
}

// escalationRetryLoop periodically re-delivers unacked escalations via tmux.
// Runs every 2 minutes, retries up to 5 times with exponential backoff.
func (d *Dispatcher) escalationRetryLoop(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.retryPendingEscalations(ctx)
		}
	}
}

func (d *Dispatcher) retryPendingEscalations(ctx context.Context) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT id, message FROM escalations
		 WHERE status = 'pending' AND retry_count < 5
		 ORDER BY id`)
	if err != nil {
		return
	}
	defer rows.Close()

	for rows.Next() {
		var id int64
		var msg string
		if err := rows.Scan(&id, &msg); err != nil {
			continue
		}
		_ = d.escalator.Escalate(ctx, msg)
		_, _ = d.db.ExecContext(ctx,
			`UPDATE escalations SET retry_count = retry_count + 1, last_retry_at = datetime('now') WHERE id = ?`,
			id)
	}
}

// parseEscalationType extracts the escalation type from a formatted
// [ORO-DISPATCH] message. Returns empty string if not a recognized type
// that has a one-shot playbook.
func parseEscalationType(msg string) string {
	// Format: [ORO-DISPATCH] TYPE: bead-id — summary.
	const prefix = "[ORO-DISPATCH] "
	_, after, found := strings.Cut(msg, prefix)
	if !found {
		return ""
	}
	escType, _, found := strings.Cut(after, ":")
	if !found {
		return ""
	}
	switch protocol.EscalationType(escType) {
	case protocol.EscStuckWorker, protocol.EscMergeConflict,
		protocol.EscPriorityContention, protocol.EscMissingAC:
		return escType
	default:
		return ""
	}
}

// spawnEscalationOneShot launches a one-shot claude -p process to handle
// the escalation. The result is logged asynchronously.
func (d *Dispatcher) spawnEscalationOneShot(ctx context.Context, escType, beadID, workerID, msg string) {
	// Look up bead details for context (best-effort).
	var beadTitle, beadContext string
	if beadID != "" {
		if detail, err := d.beads.Show(ctx, beadID); err == nil && detail != nil {
			beadTitle = detail.Title
			beadContext = detail.Description
		}
	}

	resultCh := d.ops.Escalate(ctx, ops.EscalationOpts{
		EscalationType: escType,
		BeadID:         beadID,
		BeadTitle:      beadTitle,
		BeadContext:    beadContext,
		RecentHistory:  msg,
		Workdir:        ".",
	})

	d.safeGo(func() {
		d.handleEscalationResult(ctx, escType, beadID, workerID, resultCh)
	})
}

// handleEscalationResult logs the one-shot escalation agent's outcome.
// If the one-shot fails (timeout, error, or non-zero exit), it escalates
// to the persistent manager for manual intervention.
func (d *Dispatcher) handleEscalationResult(ctx context.Context, escType, beadID, workerID string, resultCh <-chan ops.Result) {
	result := <-resultCh
	if result.Err != nil {
		_ = d.logEvent(ctx, "oneshot_escalation_failed", "ops", beadID, workerID,
			fmt.Sprintf(`{"type":%q,"error":%q}`, escType, result.Err.Error()))

		// Escalate to persistent manager when one-shot fails.
		failMsg := fmt.Sprintf("[ORO-DISPATCH] ONESHOT_FAILED: %s — One-shot %s agent failed: %v",
			beadID, escType, result.Err)
		if err := d.escalator.Escalate(ctx, failMsg); err != nil {
			_ = d.logEvent(ctx, "escalation_failed", "dispatcher", beadID, workerID,
				fmt.Sprintf(`{"error":%q,"message":%q}`, err.Error(), failMsg))
		}
		return
	}
	_ = d.logEvent(ctx, "oneshot_escalation_complete", "ops", beadID, workerID,
		fmt.Sprintf(`{"type":%q,"verdict":%q,"feedback":%q}`, escType, result.Verdict, result.Feedback))
}

func (d *Dispatcher) createAssignment(ctx context.Context, beadID, workerID, worktree string) error {
	_, err := d.db.ExecContext(ctx,
		`INSERT INTO assignments (bead_id, worker_id, worktree) VALUES (?, ?, ?)`,
		beadID, workerID, worktree)
	if err != nil {
		return fmt.Errorf("create assignment: %w", err)
	}
	return nil
}

// persistBeadCount updates a counter column on the active assignment row for a bead.
// column must be one of "attempt_count" or "handoff_count". This is a best-effort
// operation: errors are logged but do not propagate.
func (d *Dispatcher) persistBeadCount(ctx context.Context, beadID, column string, value int) {
	if d.db == nil {
		return
	}
	// Allowlist columns to prevent SQL injection.
	switch column {
	case "attempt_count", "handoff_count":
	default:
		return
	}
	_, err := d.db.ExecContext(ctx,
		fmt.Sprintf(`UPDATE assignments SET %s=? WHERE bead_id=? AND status='active'`, column),
		value, beadID)
	if err != nil {
		_ = d.logEvent(ctx, "persist_count_failed", "dispatcher", beadID, "",
			fmt.Sprintf(`{"column":%q,"value":%d,"error":%q}`, column, value, err.Error()))
	}
}

// restoreState reconstructs the in-memory attemptCounts and handoffCounts maps
// from active assignments persisted in SQLite. This ensures tracking state
// survives a dispatcher restart.
func (d *Dispatcher) restoreState(ctx context.Context) error {
	rows, err := d.db.QueryContext(ctx,
		`SELECT bead_id, attempt_count, handoff_count FROM assignments WHERE status='active'`)
	if err != nil {
		return fmt.Errorf("query active assignments: %w", err)
	}
	defer func() { _ = rows.Close() }()

	d.mu.Lock()
	defer d.mu.Unlock()

	for rows.Next() {
		var beadID string
		var attemptCount, handoffCount int
		if err := rows.Scan(&beadID, &attemptCount, &handoffCount); err != nil {
			return fmt.Errorf("scan assignment: %w", err)
		}
		if attemptCount > 0 {
			d.attemptCounts[beadID] = attemptCount
		}
		if handoffCount > 0 {
			d.handoffCounts[beadID] = handoffCount
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate assignments: %w", err)
	}
	return nil
}

func (d *Dispatcher) completeAssignment(ctx context.Context, beadID string) error {
	_, err := d.db.ExecContext(ctx,
		`UPDATE assignments SET status='completed', completed_at=datetime('now') WHERE bead_id=? AND status='active'`,
		beadID)
	if err != nil {
		return fmt.Errorf("complete assignment: %w", err)
	}
	return nil
}

func (d *Dispatcher) pendingCommands(ctx context.Context) ([]protocol.CommandRow, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT id, directive, args, status, created_at, COALESCE(processed_at, '') FROM commands WHERE status='pending' ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("query pending commands: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var cmds []protocol.CommandRow
	for rows.Next() {
		var c protocol.CommandRow
		if err := rows.Scan(&c.ID, &c.Directive, &c.Args, &c.Status, &c.CreatedAt, &c.ProcessedAt); err != nil {
			return nil, fmt.Errorf("scan command: %w", err)
		}
		cmds = append(cmds, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate commands: %w", err)
	}
	return cmds, nil
}

func (d *Dispatcher) markCommandProcessed(ctx context.Context, id int64) error {
	_, err := d.db.ExecContext(ctx,
		`UPDATE commands SET status='processed', processed_at=datetime('now') WHERE id=?`, id)
	if err != nil {
		return fmt.Errorf("mark command processed: %w", err)
	}
	return nil
}

// sendToWorker, maxPendingMessages → worker_pool.go

// shutdownSequence orchestrates the three-phase graceful shutdown:
//  1. Cancel ops agents and abort in-flight merges (safe before worker stop).
//  2. Send PREPARE_SHUTDOWN to all workers, wait for drain or force-kill.
//  3. Remove worktrees and flush bead state (safe after workers are stopped).
func (d *Dispatcher) shutdownSequence() {
	// Phase 1: Cancel ops agents and abort in-flight merges.
	d.shutdownCancelOps()

	// Phase 2: Send PREPARE_SHUTDOWN to all workers and wait for them to drain.
	// Collect worker IDs and worktree paths under lock BEFORE the wait loop,
	// because workers will be deleted from the map as they disconnect.
	d.mu.Lock()
	workerIDs := make([]string, 0, len(d.workers))
	var worktreePaths []string
	for id, w := range d.workers {
		workerIDs = append(workerIDs, id)
		if w.worktree != "" {
			worktreePaths = append(worktreePaths, w.worktree)
		}
	}
	d.mu.Unlock()

	for _, id := range workerIDs {
		d.GracefulShutdownWorker(id, d.cfg.ShutdownTimeout)
	}

	d.shutdownWaitForWorkers()

	// Phase 3: Workers are stopped — now safe to remove worktrees and flush state.
	d.shutdownRemoveWorktrees(worktreePaths)
}

// shutdownWaitForWorkers → worker_pool.go

// shutdownCancelOps cancels active ops agents and aborts in-flight merges.
// Safe to call before workers are stopped.
func (d *Dispatcher) shutdownCancelOps() {
	for _, taskID := range d.ops.Active() {
		if err := d.ops.Cancel(taskID); err == nil {
			_ = d.logEvent(context.Background(), "ops_cancelled", "dispatcher", "", "", taskID)
		}
	}
	d.merger.Abort()
}

// shutdownRemoveWorktrees removes the given worktrees and flushes bead state.
// Must be called AFTER all workers have been stopped so their working
// directories are no longer in use.
func (d *Dispatcher) shutdownRemoveWorktrees(paths []string) {
	// Remove worktrees best-effort (don't block shutdown).
	ctx := context.Background()
	for _, p := range paths {
		if err := d.worktrees.Remove(ctx, p); err != nil {
			_ = d.logEvent(ctx, "worktree_cleanup_failed", "dispatcher", "", "", err.Error())
		} else {
			_ = d.logEvent(ctx, "worktree_removed", "dispatcher", "", "", p)
		}
	}

	// Flush bead state to disk before exiting.
	if err := d.beads.Sync(ctx); err != nil {
		_ = d.logEvent(ctx, "bead_sync_failed", "dispatcher", "", "", err.Error())
	} else {
		_ = d.logEvent(ctx, "bead_synced", "dispatcher", "", "", "")
	}
}

// formatCodeChunks formats code search results into markdown for prompt injection.
func formatCodeChunks(chunks []CodeChunk) string {
	var b strings.Builder
	for _, chunk := range chunks {
		fmt.Fprintf(&b, "### %s:%d-%d\n```%s\n%s\n```\n\n",
			chunk.FilePath, chunk.StartLine, chunk.EndLine,
			"", chunk.Content) // Empty lang specifier for now
	}
	return strings.TrimSpace(b.String())
}

// ConnectedWorkers, TargetWorkers, WorkerInfo, WorkerModel → worker_pool.go
