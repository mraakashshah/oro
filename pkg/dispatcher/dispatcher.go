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
	"sort"
	"strconv"
	"sync"
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

// Bead represents a ready work item from the bead source.
type Bead struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Priority int    `json:"priority"`
	Epic     string `json:"epic,omitempty"`       // parent epic ID for focus filtering
	Type     string `json:"issue_type,omitempty"` // task, bug, feature, epic
	Model    string `json:"model,omitempty"`      // claude model override; empty = auto-route by Type
}

// BeadDetail holds extended information about a single bead.
type BeadDetail struct {
	ID                 string `json:"id"`
	Title              string `json:"title"`
	AcceptanceCriteria string `json:"acceptance_criteria"`
	Model              string `json:"model,omitempty"`
}

// Model constants for routing.
const (
	ModelOpus   = "claude-opus-4-6"
	ModelSonnet = "claude-sonnet-4-5-20250929"
)

// DefaultModel is used when a bead has no explicit model set and no type-based
// routing applies. Kept as ModelOpus for backward compatibility.
const DefaultModel = ModelOpus

// ResolveModel returns the model to use for this bead. Priority:
//  1. Explicit Model field (bead-level override)
//  2. Type-based routing: epic/feature -> Opus, task/bug -> Sonnet
//  3. DefaultModel (Opus) as fallback
func (b Bead) ResolveModel() string {
	if b.Model != "" {
		return b.Model
	}
	switch b.Type {
	case "epic", "feature":
		return ModelOpus
	case "task", "bug":
		return ModelSonnet
	default:
		return DefaultModel
	}
}

// --- Interfaces for testability ---

// BeadSource provides ready work items. Production impl shells out to `bd ready`.
type BeadSource interface {
	Ready(ctx context.Context) ([]Bead, error)
	Show(ctx context.Context, id string) (*BeadDetail, error)
	Close(ctx context.Context, id string, reason string) error
	Sync(ctx context.Context) error
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

// --- Worker tracking ---

// WorkerState represents the state of a connected worker.
type WorkerState string

// Worker state constants.
const (
	WorkerIdle         WorkerState = "idle"
	WorkerBusy         WorkerState = "busy"
	WorkerReserved     WorkerState = "reserved" // transient: I/O in progress, heartbeat checker must skip
	WorkerReviewing    WorkerState = "reviewing"
	WorkerShuttingDown WorkerState = "shutting_down" // transient: handoff SHUTDOWN sent, not yet disconnected
)

// trackedWorker holds runtime state for a connected worker.
type trackedWorker struct {
	id       string
	conn     net.Conn
	state    WorkerState
	beadID   string
	worktree string
	model    string // resolved model for the current bead assignment
	lastSeen time.Time
	encoder  *json.Encoder
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
	MaxWorkers           int           // Worker pool size (default 5).
	HeartbeatTimeout     time.Duration // Worker heartbeat timeout (default 45s).
	PollInterval         time.Duration // bd ready poll interval (default 10s).
	FallbackPollInterval time.Duration // Fallback poll interval for fsnotify safety net (default 60s).
	ShutdownTimeout      time.Duration // Graceful shutdown timeout (default 10s).
}

func (c *Config) withDefaults() Config {
	out := *c
	if out.MaxWorkers == 0 {
		out.MaxWorkers = 5
	}
	if out.HeartbeatTimeout == 0 {
		out.HeartbeatTimeout = 45 * time.Second
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
	return out
}

// --- Dispatcher ---

// Dispatcher is the main orchestrator.
type Dispatcher struct {
	cfg       Config
	db        *sql.DB
	merger    *merge.Coordinator
	ops       *ops.Spawner
	beads     BeadSource
	worktrees WorktreeManager
	escalator Escalator
	memories  *memory.Store
	procMgr   ProcessManager

	mu              sync.Mutex
	state           State
	workers         map[string]*trackedWorker
	listener        net.Listener
	focusedEpic     string
	targetWorkers   int
	rejectionCounts map[string]int             // bead ID -> review rejection count
	handoffCounts   map[string]int             // bead ID -> ralph handoff count
	attemptCounts   map[string]int             // bead ID -> QG retry attempt count
	pendingHandoffs map[string]*pendingHandoff // bead ID -> pending handoff info
	qgStuckTracker  map[string]*qgHistory      // bead ID -> consecutive QG output hashes

	// beadsDir is the directory to watch for bead changes (defaults to ".beads")
	beadsDir string

	// nowFunc allows tests to control time.
	nowFunc func() time.Time

	// testUnlockHook, if non-nil, is called after releasing the lock in
	// registerWorker/handleQGFailure (before memory.ForPrompt). Tests use
	// this to inject a synchronization point that guarantees a concurrent
	// deletion occurs during the unlock window.
	testUnlockHook func()
}

// New creates a Dispatcher. It does NOT start listening or polling — call Run().
func New(cfg Config, db *sql.DB, merger *merge.Coordinator, opsSpawner *ops.Spawner, beads BeadSource, wt WorktreeManager, esc Escalator) *Dispatcher {
	resolved := cfg.withDefaults()
	return &Dispatcher{
		cfg:             resolved,
		db:              db,
		merger:          merger,
		ops:             opsSpawner,
		beads:           beads,
		worktrees:       wt,
		escalator:       esc,
		memories:        memory.NewStore(db),
		state:           StateInert,
		workers:         make(map[string]*trackedWorker),
		rejectionCounts: make(map[string]int),
		handoffCounts:   make(map[string]int),
		attemptCounts:   make(map[string]int),
		pendingHandoffs: make(map[string]*pendingHandoff),
		qgStuckTracker:  make(map[string]*qgHistory),
		beadsDir:        ".beads",
		nowFunc:         time.Now,
	}
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

// Run starts the Dispatcher event loop. It:
//  1. Initializes the SQLite schema
//  2. Starts the UDS listener
//  3. Polls for commands (directives) and ready beads
//  4. Monitors worker heartbeats
//
// Run blocks until ctx is cancelled.
func (d *Dispatcher) Run(ctx context.Context) error {
	// Init schema
	if _, err := d.db.ExecContext(ctx, protocol.SchemaDDL); err != nil {
		return fmt.Errorf("init schema: %w", err)
	}

	// Prune orphaned worktrees from a previous crash. Errors are logged
	// but non-fatal — they must not prevent dispatcher startup.
	if pruneErr := d.worktrees.Prune(ctx); pruneErr != nil {
		_ = d.logEvent(ctx, "worktree_prune_failed", "dispatcher", "", "", pruneErr.Error())
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
	d.mu.Lock()
	d.listener = ln
	d.mu.Unlock()

	// Accept connections
	go d.acceptLoop(ctx, ln)

	// Bead assignment loop
	go d.assignLoop(ctx)

	// Heartbeat monitor
	go d.heartbeatLoop(ctx)

	<-ctx.Done()

	// --- Graceful shutdown ---
	// Phase 1: cancel ops/merges, Phase 2: stop workers, Phase 3: remove worktrees.
	d.shutdownSequence()
	_ = ln.Close()
	return nil
}

// --- UDS server ---

// acceptLoop accepts new worker connections.
func (d *Dispatcher) acceptLoop(ctx context.Context, ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			continue
		}
		go d.handleConn(ctx, conn)
	}
}

// handleConn reads line-delimited JSON messages from a worker connection.
func (d *Dispatcher) handleConn(ctx context.Context, conn net.Conn) {
	scanner := bufio.NewScanner(conn)
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

// registerWorker adds or updates a tracked worker. If a pending handoff exists,
// the worker is immediately assigned that bead+worktree (ralph respawn).
func (d *Dispatcher) registerWorker(id string, conn net.Conn) {
	d.mu.Lock()
	if _, exists := d.workers[id]; !exists {
		d.workers[id] = &trackedWorker{
			id:       id,
			conn:     conn,
			state:    WorkerIdle,
			lastSeen: d.nowFunc(),
			encoder:  json.NewEncoder(conn),
		}
	} else {
		d.workers[id].conn = conn
		d.workers[id].lastSeen = d.nowFunc()
		d.workers[id].encoder = json.NewEncoder(conn)
	}

	// Check for pending ralph handoffs — assign immediately if one exists.
	var h *pendingHandoff
	for beadID, ph := range d.pendingHandoffs {
		h = ph
		delete(d.pendingHandoffs, beadID)
		break
	}

	if h != nil {
		w := d.workers[id]
		// Phase 1: Reserve the worker — heartbeat checker skips reserved workers.
		w.state = WorkerReserved
		w.beadID = h.beadID
		w.worktree = h.worktree
		w.model = h.model

		// Retrieve relevant memories (best-effort, outside lock).
		// We need to unlock before calling memory.ForPrompt.
		d.mu.Unlock()
		if d.testUnlockHook != nil {
			d.testUnlockHook()
		}

		var memCtx string
		if d.memories != nil {
			memCtx, _ = memory.ForPrompt(context.Background(), d.memories, nil, h.beadID, 0)
		}

		d.mu.Lock()
		// Phase 2: Verify reservation still valid, then transition to Busy.
		w, ok := d.workers[id]
		if !ok || w.state != WorkerReserved {
			d.mu.Unlock()
			return
		}
		w.state = WorkerBusy
		_ = d.sendToWorker(w, protocol.Message{
			Type: protocol.MsgAssign,
			Assign: &protocol.AssignPayload{
				BeadID:        h.beadID,
				Worktree:      h.worktree,
				Model:         h.model,
				MemoryContext: memCtx,
			},
		})
	}
	d.mu.Unlock()
}

// consumePendingHandoff returns and removes a single pending handoff, or nil
// if none exist. Used when a new worker connects to immediately assign a
// ralph-handoff bead+worktree.
func (d *Dispatcher) consumePendingHandoff() *pendingHandoff {
	d.mu.Lock()
	defer d.mu.Unlock()
	for beadID, h := range d.pendingHandoffs {
		delete(d.pendingHandoffs, beadID)
		return h
	}
	return nil
}

// --- Message handling ---

// handleMessage dispatches an incoming worker message.
func (d *Dispatcher) handleMessage(ctx context.Context, workerID string, msg protocol.Message) {
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
	}
	d.mu.Unlock()

	_ = d.logEvent(ctx, "heartbeat", workerID, msg.Heartbeat.BeadID, workerID, "")
}

func (d *Dispatcher) handleStatus(ctx context.Context, workerID string, msg protocol.Message) {
	if msg.Status == nil {
		return
	}
	_ = d.logEvent(ctx, "status", workerID, msg.Status.BeadID, workerID,
		fmt.Sprintf(`{"state":%q,"result":%q}`, msg.Status.State, msg.Status.Result))
}

func (d *Dispatcher) handleDone(ctx context.Context, workerID string, msg protocol.Message) {
	if msg.Done == nil {
		return
	}
	beadID := msg.Done.BeadID

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
		branch = "agent/" + beadID
		w.state = WorkerIdle
		w.beadID = ""
	}
	d.mu.Unlock()

	if !ok || worktree == "" {
		return
	}

	// Clear tracking state for completed bead.
	d.clearBeadTracking(beadID)

	// Merge in background
	go d.mergeAndComplete(ctx, beadID, workerID, worktree, branch)
}

// handleQGFailure processes a quality-gate failure: checks for stuck detection
// (repeated identical outputs), increments the attempt counter, escalates if
// either cap is reached, or re-assigns with feedback.
func (d *Dispatcher) handleQGFailure(ctx context.Context, workerID, beadID, qgOutput string) {
	_ = d.logEvent(ctx, "quality_gate_rejected", workerID, beadID, workerID,
		`{"reason":"QualityGatePassed=false"}`)

	// Check stuck detection: hash QGOutput and track consecutive identical hashes.
	if d.isQGStuck(beadID, qgOutput) {
		_ = d.logEvent(ctx, "qg_stuck_detected", workerID, beadID, workerID,
			fmt.Sprintf(`{"repeated_count":%d}`, maxStuckCount))
		_ = d.escalator.Escalate(ctx, FormatEscalation(EscStuck, beadID,
			fmt.Sprintf("QG output repeated %d times — worker stuck", maxStuckCount), qgOutput))
		d.clearBeadTracking(beadID)
		return
	}

	d.mu.Lock()
	d.attemptCounts[beadID]++
	attempt := d.attemptCounts[beadID]

	if attempt >= maxQGRetries {
		d.mu.Unlock()
		_ = d.logEvent(ctx, "qg_retry_escalated", workerID, beadID, workerID,
			fmt.Sprintf(`{"attempts":%d}`, attempt))
		_ = d.escalator.Escalate(ctx, FormatEscalation(EscStuck, beadID,
			fmt.Sprintf("quality gate failed %d times", attempt), qgOutput))
		d.clearBeadTracking(beadID)
		return
	}

	// Phase 1: Reserve the worker — heartbeat checker skips reserved workers.
	if w, ok := d.workers[workerID]; ok {
		w.state = WorkerReserved
	}
	d.mu.Unlock()

	d.qgRetryWithReservation(ctx, workerID, beadID, qgOutput, attempt)
}

// qgRetryWithReservation performs the I/O phase (memory retrieval) and
// completes the two-phase reservation for a QG retry. The worker must already
// be in WorkerReserved state before this is called.
func (d *Dispatcher) qgRetryWithReservation(ctx context.Context, workerID, beadID, qgOutput string, attempt int) {
	// Retrieve relevant memories for retry prompt (best-effort, outside lock).
	if d.testUnlockHook != nil {
		d.testUnlockHook()
	}
	memCtx := d.fetchBeadMemories(ctx, beadID)

	d.mu.Lock()
	// Phase 2: Verify reservation still valid, then transition to Busy.
	w, ok := d.workers[workerID]
	if !ok || w.state != WorkerReserved {
		d.mu.Unlock()
		return
	}
	_ = d.sendToWorker(w, protocol.Message{
		Type: protocol.MsgAssign,
		Assign: &protocol.AssignPayload{
			BeadID:        beadID,
			Worktree:      w.worktree,
			Model:         w.model,
			Attempt:       attempt,
			Feedback:      qgOutput,
			MemoryContext: memCtx,
		},
	})
	w.state = WorkerBusy
	w.beadID = beadID
	d.mu.Unlock()
}

// fetchBeadMemories retrieves relevant memories for a bead (best-effort).
// Returns empty string if memories are unavailable.
func (d *Dispatcher) fetchBeadMemories(ctx context.Context, beadID string) string {
	if d.memories == nil {
		return ""
	}
	searchTerm := beadID
	if detail, showErr := d.beads.Show(ctx, beadID); showErr == nil && detail != nil && detail.Title != "" {
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
			go d.handleMergeConflictResult(ctx, beadID, workerID, worktree, resultCh)
			_ = d.logEvent(ctx, "merge_conflict", "dispatcher", beadID, workerID,
				fmt.Sprintf(`{"files":%q}`, conflictErr.Files))
			return
		}
		// Non-conflict merge failure — escalate
		_ = d.escalator.Escalate(ctx, FormatEscalation(EscMergeConflict, beadID, "merge failed", err.Error()))
		_ = d.logEvent(ctx, "merge_failed", "dispatcher", beadID, workerID, err.Error())
		return
	}

	// Clean merge — close bead and complete assignment
	_ = d.beads.Close(ctx, beadID, fmt.Sprintf("Merged: %s", result.CommitSHA))
	_ = d.completeAssignment(ctx, beadID)
	_ = d.logEvent(ctx, "merged", "dispatcher", beadID, workerID,
		fmt.Sprintf(`{"sha":%q}`, result.CommitSHA))
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
			_ = d.escalator.Escalate(ctx, FormatEscalation(EscMergeConflict, beadID,
				"merge conflict resolution failed", result.Feedback))
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

	// Send SHUTDOWN to the old worker and capture worktree+model for respawn.
	d.mu.Lock()
	w, ok := d.workers[workerID]
	var worktree, model string
	if ok {
		worktree = w.worktree
		model = w.model
		_ = d.sendToWorker(w, protocol.Message{Type: protocol.MsgShutdown})
		w.state = WorkerShuttingDown // transient state — invisible to tryAssign
		w.beadID = ""
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
		go d.handleDiagnosisResult(ctx, beadID, workerID, resultCh)
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
			_ = d.escalator.Escalate(ctx, FormatEscalation(EscStuck, beadID,
				"diagnosis failed", result.Err.Error()))
			d.clearBeadTracking(beadID)
			return
		}

		// Diagnosis succeeded — log feedback and escalate with diagnosis context.
		_ = d.logEvent(ctx, "diagnosis_complete", "dispatcher", beadID, workerID, result.Feedback)
		_ = d.escalator.Escalate(ctx, FormatEscalation(EscStuck, beadID,
			"diagnosis complete", result.Feedback))
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

	_ = d.logEvent(ctx, "ready_for_review", workerID, beadID, workerID, "")

	d.mu.Lock()
	w, ok := d.workers[workerID]
	var worktree string
	if ok {
		w.state = WorkerReviewing
		worktree = w.worktree
	}
	d.mu.Unlock()

	if worktree == "" {
		return
	}

	// Spawn review ops agent
	resultCh := d.ops.Review(ctx, ops.ReviewOpts{
		BeadID:   beadID,
		Worktree: worktree,
	})

	// Handle review result asynchronously
	go d.handleReviewResult(ctx, workerID, beadID, resultCh)
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
			_ = d.escalator.Escalate(ctx, FormatEscalation(EscStuck, beadID, "review failed", result.Feedback))
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
		d.mu.Unlock()
		_ = d.logEvent(ctx, "review_escalated", "ops", beadID, workerID,
			fmt.Sprintf(`{"rejections":%d,"feedback":%q}`, count, feedback))
		_ = d.escalator.Escalate(ctx, FormatEscalation(EscStuck, beadID,
			fmt.Sprintf("review rejected %d times", count), feedback))
		d.clearBeadTracking(beadID)
		return
	}

	// Phase 1: Reserve the worker — heartbeat checker skips reserved workers.
	if w, wOK := d.workers[workerID]; wOK {
		w.state = WorkerReserved
	}
	d.mu.Unlock()

	// Re-assign with reviewer feedback.
	d.mu.Lock()
	// Phase 2: Verify reservation still valid, then transition to Busy.
	w, ok := d.workers[workerID]
	if ok && w.state == WorkerReserved {
		w.state = WorkerBusy
		_ = d.sendToWorker(w, protocol.Message{
			Type: protocol.MsgAssign,
			Assign: &protocol.AssignPayload{
				BeadID:   beadID,
				Worktree: w.worktree,
				Feedback: feedback,
			},
		})
	}
	d.mu.Unlock()
}

// clearRejectionCount removes the rejection counter for a bead (e.g., on approval or completion).
func (d *Dispatcher) clearRejectionCount(beadID string) {
	d.mu.Lock()
	delete(d.rejectionCounts, beadID)
	d.mu.Unlock()
}

// clearHandoffCount removes the handoff counter for a bead (e.g., on completion).
func (d *Dispatcher) clearHandoffCount(beadID string) {
	d.mu.Lock()
	delete(d.handoffCounts, beadID)
	d.mu.Unlock()
}

// clearBeadTracking removes all five tracking-map entries for a bead in a
// single lock acquisition. Call this on every terminal path (success,
// escalation, heartbeat timeout) to prevent map entry leaks.
func (d *Dispatcher) clearBeadTracking(beadID string) {
	d.mu.Lock()
	delete(d.attemptCounts, beadID)
	delete(d.handoffCounts, beadID)
	delete(d.rejectionCounts, beadID)
	delete(d.pendingHandoffs, beadID)
	delete(d.qgStuckTracker, beadID)
	d.mu.Unlock()
}

func (d *Dispatcher) handleReconnect(ctx context.Context, workerID string, msg protocol.Message) {
	if msg.Reconnect == nil {
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
			w.state = WorkerBusy
		default:
			w.state = WorkerIdle
		}
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
		w.state = WorkerIdle
		w.beadID = ""
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

// GracefulShutdownWorker initiates a graceful shutdown for a specific worker.
// It sends PREPARE_SHUTDOWN with the given timeout, then waits for SHUTDOWN_APPROVED.
// If the worker does not respond within the timeout, it sends a hard SHUTDOWN.
func (d *Dispatcher) GracefulShutdownWorker(workerID string, timeout time.Duration) {
	d.mu.Lock()
	w, ok := d.workers[workerID]
	if !ok {
		d.mu.Unlock()
		return
	}
	_ = d.sendToWorker(w, protocol.Message{
		Type: protocol.MsgPrepareShutdown,
		PrepareShutdown: &protocol.PrepareShutdownPayload{
			Timeout: timeout,
		},
	})
	d.mu.Unlock()

	// Wait for SHUTDOWN_APPROVED or timeout in background
	go func() {
		timer := time.NewTimer(timeout)
		defer timer.Stop()

		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-timer.C:
				// Timeout — send hard SHUTDOWN
				d.mu.Lock()
				w, ok := d.workers[workerID]
				if ok {
					_ = d.sendToWorker(w, protocol.Message{Type: protocol.MsgShutdown})
					w.state = WorkerIdle
					w.beadID = ""
				}
				d.mu.Unlock()
				return
			case <-ticker.C:
				// Check if approval was already received (worker state reset to idle)
				d.mu.Lock()
				w, ok := d.workers[workerID]
				approved := ok && w.state == WorkerIdle
				d.mu.Unlock()
				if approved {
					return
				}
			}
		}
	}()
}

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
		case <-ticker.C:
			d.tryAssign(ctx)
		}
	}
}

// tryAssign attempts to assign ready beads to idle workers.
func (d *Dispatcher) tryAssign(ctx context.Context) {
	// Only assign in running state.
	if d.GetState() != StateRunning {
		return
	}

	// Find idle workers.
	d.mu.Lock()
	var idle []*trackedWorker
	for _, w := range d.workers {
		if w.state == WorkerIdle {
			idle = append(idle, w)
		}
	}
	d.mu.Unlock()

	if len(idle) == 0 {
		return
	}

	// Poll for ready beads.
	allBeads, err := d.beads.Ready(ctx)
	if err != nil {
		return
	}

	// Filter out epic beads — workers can only execute leaf tasks.
	beads := make([]Bead, 0, len(allBeads))
	for _, b := range allBeads {
		if b.Type != "epic" {
			beads = append(beads, b)
		}
	}

	// Sort by focused epic first (if set), then by priority (P0 first, P3 last).
	d.mu.Lock()
	epic := d.focusedEpic
	d.mu.Unlock()

	sort.SliceStable(beads, func(i, j int) bool {
		if epic != "" {
			iMatch := beads[i].Epic == epic
			jMatch := beads[j].Epic == epic
			if iMatch != jMatch {
				return iMatch // focused epic beads come first
			}
		}
		return beads[i].Priority < beads[j].Priority
	})

	// Assign beads to idle workers (1:1).
	for i, bead := range beads {
		if i >= len(idle) {
			break
		}
		d.assignBead(ctx, idle[i], bead)
	}
}

// assignBead creates a worktree and sends ASSIGN to the worker.
// If memories exist for the bead's description, they are included in the
// AssignPayload.MemoryContext field for cross-session continuity.
func (d *Dispatcher) assignBead(ctx context.Context, w *trackedWorker, bead Bead) {
	worktree, branch, err := d.worktrees.Create(ctx, bead.ID)
	if err != nil {
		_ = d.logEvent(ctx, "worktree_error", "dispatcher", bead.ID, w.id, err.Error())
		return
	}

	_ = d.createAssignment(ctx, bead.ID, w.id, worktree)
	_ = d.logEvent(ctx, "assign", "dispatcher", bead.ID, w.id,
		fmt.Sprintf(`{"worktree":%q,"branch":%q}`, worktree, branch))

	// Retrieve bead details for rich prompt (best-effort).
	var title, acceptance string
	if detail, showErr := d.beads.Show(ctx, bead.ID); showErr == nil && detail != nil {
		title = detail.Title
		acceptance = detail.AcceptanceCriteria
	}

	// Retrieve relevant memories for this bead (best-effort).
	var memCtx string
	if d.memories != nil {
		memCtx, _ = memory.ForPrompt(ctx, d.memories, nil, bead.Title, 0)
	}

	d.mu.Lock()
	w.state = WorkerBusy
	w.beadID = bead.ID
	w.worktree = worktree
	w.model = bead.ResolveModel()
	err = d.sendToWorker(w, protocol.Message{
		Type: protocol.MsgAssign,
		Assign: &protocol.AssignPayload{
			BeadID:             bead.ID,
			Worktree:           worktree,
			Model:              bead.ResolveModel(),
			MemoryContext:      memCtx,
			Title:              title,
			AcceptanceCriteria: acceptance,
		},
	})
	if err != nil {
		// Revert worker to Idle so it is not permanently stuck in Busy.
		w.state = WorkerIdle
		w.beadID = ""
		w.worktree = ""
		w.model = ""
	}
	d.mu.Unlock()

	if err != nil {
		_ = d.worktrees.Remove(ctx, worktree)
		_ = d.logEvent(ctx, "worktree_cleanup", "dispatcher", bead.ID, w.id, err.Error())
	}
}

// statusResponse is the JSON structure returned by the status directive.
type statusResponse struct {
	State       string            `json:"state"`
	WorkerCount int               `json:"worker_count"`
	QueueDepth  int               `json:"queue_depth"`
	Assignments map[string]string `json:"assignments"`
	FocusedEpic string            `json:"focused_epic,omitempty"`
}

// applyDirective transitions the dispatcher state machine and returns a detail
// string for the ACK response. Returns an error for invalid args (e.g. scale).
func (d *Dispatcher) applyDirective(dir protocol.Directive, args string) (string, error) {
	if dir == protocol.DirectiveScale {
		return d.applyScaleDirective(args)
	}
	switch dir {
	case protocol.DirectiveStart:
		d.setState(StateRunning)
		return "started", nil
	case protocol.DirectiveStop:
		d.setState(StateStopping)
		return "stopping", nil
	case protocol.DirectivePause:
		d.setState(StatePaused)
		return "paused", nil
	case protocol.DirectiveResume:
		if d.GetState() == StateRunning {
			return "already running", nil
		}
		d.setState(StateRunning)
		return "resumed", nil
	case protocol.DirectiveStatus:
		return d.buildStatusJSON(), nil
	case protocol.DirectiveFocus:
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
	default:
		return fmt.Sprintf("applied %s", dir), nil
	}
}

// buildStatusJSON constructs the status response JSON string.
func (d *Dispatcher) buildStatusJSON() string {
	d.mu.Lock()
	state := d.state
	workerCount := len(d.workers)
	assignments := make(map[string]string, len(d.workers))
	for id, w := range d.workers {
		if w.beadID != "" {
			assignments[id] = w.beadID
		}
	}
	epic := d.focusedEpic
	d.mu.Unlock()

	resp := statusResponse{
		State:       string(state),
		WorkerCount: workerCount,
		QueueDepth:  0, // bd ready count not cached; zero for now
		Assignments: assignments,
		FocusedEpic: epic,
	}
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
		if w.state == WorkerIdle {
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

// --- Heartbeat monitoring ---

// heartbeatLoop checks for workers that have exceeded the heartbeat timeout.
func (d *Dispatcher) heartbeatLoop(ctx context.Context) {
	ticker := time.NewTicker(d.cfg.HeartbeatTimeout / 3)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.checkHeartbeats(ctx)
		}
	}
}

// checkHeartbeats finds workers that have timed out and marks them dead.
func (d *Dispatcher) checkHeartbeats(ctx context.Context) {
	now := d.nowFunc()
	d.mu.Lock()
	var dead []string
	for id, w := range d.workers {
		if w.state != WorkerIdle && w.state != WorkerReserved && now.Sub(w.lastSeen) > d.cfg.HeartbeatTimeout {
			dead = append(dead, id)
		}
	}
	// Remove dead workers and collect info for escalation after unlock.
	type deadInfo struct {
		workerID string
		beadID   string
	}
	deadWorkers := make([]deadInfo, 0, len(dead))
	for _, id := range dead {
		w := d.workers[id]
		deadWorkers = append(deadWorkers, deadInfo{workerID: id, beadID: w.beadID})
		_ = d.logEventLocked(ctx, "heartbeat_timeout", "dispatcher", w.beadID, id, "")
		_ = w.conn.Close()
		delete(d.workers, id)
	}
	d.mu.Unlock()

	// Escalate outside the lock and clear tracking maps for abandoned beads.
	for _, dw := range deadWorkers {
		_ = d.escalator.Escalate(ctx, FormatEscalation(EscWorkerCrash, dw.beadID, "worker disconnected", "heartbeat timeout for worker "+dw.workerID))
		if dw.beadID != "" {
			d.clearBeadTracking(dw.beadID)
		}
	}
}

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

func (d *Dispatcher) createAssignment(ctx context.Context, beadID, workerID, worktree string) error {
	_, err := d.db.ExecContext(ctx,
		`INSERT INTO assignments (bead_id, worker_id, worktree) VALUES (?, ?, ?)`,
		beadID, workerID, worktree)
	if err != nil {
		return fmt.Errorf("create assignment: %w", err)
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

// --- UDS send helper ---

// sendToWorker sends a message to a tracked worker. Caller must hold d.mu.
func (d *Dispatcher) sendToWorker(w *trackedWorker, msg protocol.Message) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal message: %w", err)
	}
	data = append(data, '\n')
	_, err = w.conn.Write(data)
	if err != nil {
		return fmt.Errorf("write to worker %s: %w", w.id, err)
	}
	return nil
}

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

// shutdownWaitForWorkers waits up to ShutdownTimeout for all workers to drain,
// then force-closes any remaining connections.
func (d *Dispatcher) shutdownWaitForWorkers() {
	deadline := time.NewTimer(d.cfg.ShutdownTimeout)
	defer deadline.Stop()

	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-deadline.C:
			// Timeout expired — force-close all remaining connections.
			d.mu.Lock()
			for id, w := range d.workers {
				_ = w.conn.Close()
				delete(d.workers, id)
			}
			d.mu.Unlock()
			return
		case <-ticker.C:
			if d.ConnectedWorkers() == 0 {
				return
			}
		}
	}
}

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

// ConnectedWorkers returns the number of currently connected workers.
func (d *Dispatcher) ConnectedWorkers() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.workers)
}

// TargetWorkers returns the target worker pool size set by a scale directive.
//
//oro:testonly
func (d *Dispatcher) TargetWorkers() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.targetWorkers
}

// WorkerInfo returns state info for a tracked worker (for testing).
//
//oro:testonly
func (d *Dispatcher) WorkerInfo(id string) (state WorkerState, beadID string, ok bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	w, exists := d.workers[id]
	if !exists {
		return "", "", false
	}
	return w.state, w.beadID, true
}

// WorkerModel returns the stored model for a tracked worker (for testing).
//
//oro:testonly
func (d *Dispatcher) WorkerModel(id string) (model string, ok bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	w, exists := d.workers[id]
	if !exists {
		return "", false
	}
	return w.model, true
}
