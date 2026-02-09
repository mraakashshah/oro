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
	Model    string `json:"model,omitempty"` // claude model override; default "claude-opus-4-6"
}

// BeadDetail holds extended information about a single bead.
type BeadDetail struct {
	ID                 string `json:"id"`
	Title              string `json:"title"`
	AcceptanceCriteria string `json:"acceptance_criteria"`
	Model              string `json:"model,omitempty"`
}

// DefaultModel is used when a bead has no explicit model set.
const DefaultModel = "claude-opus-4-6"

// ResolveModel returns the bead's model or DefaultModel if empty.
func (b Bead) ResolveModel() string {
	if b.Model != "" {
		return b.Model
	}
	return DefaultModel
}

// --- Interfaces for testability ---

// BeadSource provides ready work items. Production impl shells out to `bd ready`.
type BeadSource interface {
	Ready(ctx context.Context) ([]Bead, error)
	Show(ctx context.Context, id string) (*BeadDetail, error)
	Close(ctx context.Context, id string, reason string) error
}

// WorktreeManager creates and removes git worktrees.
type WorktreeManager interface {
	Create(ctx context.Context, beadID string) (path string, branch string, err error)
	Remove(ctx context.Context, path string) error
}

// Escalator sends messages to the Manager. Production impl uses tmux send-keys.
type Escalator interface {
	Escalate(ctx context.Context, msg string) error
}

// --- Worker tracking ---

// WorkerState represents the state of a connected worker.
type WorkerState string

// Worker state constants.
const (
	WorkerIdle      WorkerState = "idle"
	WorkerBusy      WorkerState = "busy"
	WorkerReviewing WorkerState = "reviewing"
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

	mu       sync.Mutex
	state    State
	workers  map[string]*trackedWorker
	listener net.Listener

	// beadsDir is the directory to watch for bead changes (defaults to ".beads")
	beadsDir string

	// nowFunc allows tests to control time.
	nowFunc func() time.Time
}

// New creates a Dispatcher. It does NOT start listening or polling — call Run().
func New(cfg Config, db *sql.DB, merger *merge.Coordinator, opsSpawner *ops.Spawner, beads BeadSource, wt WorktreeManager, esc Escalator) *Dispatcher {
	resolved := cfg.withDefaults()
	return &Dispatcher{
		cfg:       resolved,
		db:        db,
		merger:    merger,
		ops:       opsSpawner,
		beads:     beads,
		worktrees: wt,
		escalator: esc,
		memories:  memory.NewStore(db),
		state:     StateInert,
		workers:   make(map[string]*trackedWorker),
		beadsDir:  ".beads",
		nowFunc:   time.Now,
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
	d.shutdownCleanup()

	// Broadcast PREPARE_SHUTDOWN to all workers.
	// Collect worker IDs under lock.
	d.mu.Lock()
	workerIDs := make([]string, 0, len(d.workers))
	for id := range d.workers {
		workerIDs = append(workerIDs, id)
	}
	d.mu.Unlock()

	// Initiate graceful shutdown for each worker (non-blocking).
	for _, id := range workerIDs {
		d.GracefulShutdownWorker(id, d.cfg.ShutdownTimeout)
	}

	// Wait up to ShutdownTimeout for all workers to drain.
	shutdownDeadline := time.NewTimer(d.cfg.ShutdownTimeout)
	defer shutdownDeadline.Stop()

	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-shutdownDeadline.C:
			// Timeout expired — force-close all remaining connections.
			d.mu.Lock()
			for id, w := range d.workers {
				_ = w.conn.Close()
				delete(d.workers, id)
			}
			d.mu.Unlock()
			_ = ln.Close()
			return nil
		case <-ticker.C:
			if d.ConnectedWorkers() == 0 {
				_ = ln.Close()
				return nil
			}
		}
	}
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

// registerWorker adds or updates a tracked worker.
func (d *Dispatcher) registerWorker(id string, conn net.Conn) {
	d.mu.Lock()
	defer d.mu.Unlock()
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

	// Reject merge if quality gate did not pass — log warning and reassign bead.
	if !msg.Done.QualityGatePassed {
		_ = d.logEvent(ctx, "quality_gate_rejected", workerID, beadID, workerID,
			`{"reason":"QualityGatePassed=false"}`)

		d.mu.Lock()
		w, ok := d.workers[workerID]
		if ok {
			// Re-assign the same bead back to the worker
			_ = d.sendToWorker(w, protocol.Message{
				Type: protocol.MsgAssign,
				Assign: &protocol.AssignPayload{
					BeadID:   beadID,
					Worktree: w.worktree,
					Model:    w.model,
				},
			})
			w.state = WorkerBusy
			w.beadID = beadID
		}
		d.mu.Unlock()
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

	// Merge in background
	go d.mergeAndComplete(ctx, beadID, workerID, worktree, branch)
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
			d.ops.ResolveMergeConflict(ctx, ops.MergeOpts{
				BeadID:        beadID,
				Worktree:      worktree,
				ConflictFiles: conflictErr.Files,
			})
			_ = d.logEvent(ctx, "merge_conflict", "dispatcher", beadID, workerID,
				fmt.Sprintf(`{"files":%q}`, conflictErr.Files))
			return
		}
		// Non-conflict merge failure — escalate
		_ = d.escalator.Escalate(ctx, fmt.Sprintf("merge failed for bead %s: %s", beadID, err))
		_ = d.logEvent(ctx, "merge_failed", "dispatcher", beadID, workerID, err.Error())
		return
	}

	// Clean merge — complete assignment
	_ = d.completeAssignment(ctx, beadID)
	_ = d.logEvent(ctx, "merged", "dispatcher", beadID, workerID,
		fmt.Sprintf(`{"sha":%q}`, result.CommitSHA))
}

func (d *Dispatcher) handleHandoff(ctx context.Context, workerID string, msg protocol.Message) {
	if msg.Handoff == nil {
		return
	}
	beadID := msg.Handoff.BeadID

	_ = d.logEvent(ctx, "handoff", workerID, beadID, workerID, "")

	// Persist learnings and decisions from the handoff payload as memories.
	d.persistHandoffContext(ctx, msg.Handoff)

	// Send SHUTDOWN to the old worker
	d.mu.Lock()
	w, ok := d.workers[workerID]
	var worktree string
	if ok {
		worktree = w.worktree
		_ = d.sendToWorker(w, protocol.Message{Type: protocol.MsgShutdown})
		w.state = WorkerIdle
		w.beadID = ""
	}
	d.mu.Unlock()

	if worktree == "" {
		return
	}

	// Re-assign the same bead+worktree to the next idle worker (or the same one reconnects)
	_ = d.logEvent(ctx, "handoff_pending", "dispatcher", beadID, "", worktree)
}

// persistHandoffContext stores learnings and decisions from a HandoffPayload
// into the memory store for cross-session retrieval.
func (d *Dispatcher) persistHandoffContext(ctx context.Context, h *protocol.HandoffPayload) {
	if d.memories == nil {
		return
	}

	for _, learning := range h.Learnings {
		_, _ = d.memories.Insert(ctx, memory.InsertParams{
			Content:    learning,
			Type:       "lesson",
			Source:     "self_report",
			BeadID:     h.BeadID,
			WorkerID:   h.WorkerID,
			Confidence: 0.8,
		})
	}

	for _, decision := range h.Decisions {
		_, _ = d.memories.Insert(ctx, memory.InsertParams{
			Content:    decision,
			Type:       "decision",
			Source:     "self_report",
			BeadID:     h.BeadID,
			WorkerID:   h.WorkerID,
			Confidence: 0.8,
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

// handleReviewResult waits for the ops review result and acts on it.
func (d *Dispatcher) handleReviewResult(ctx context.Context, workerID, beadID string, resultCh <-chan ops.Result) {
	select {
	case <-ctx.Done():
		return
	case result := <-resultCh:
		switch result.Verdict {
		case ops.VerdictApproved:
			_ = d.logEvent(ctx, "review_approved", "ops", beadID, workerID, result.Feedback)
			// Worker can now proceed to DONE
		case ops.VerdictRejected:
			_ = d.logEvent(ctx, "review_rejected", "ops", beadID, workerID, result.Feedback)
			// Send feedback to worker via STATUS-like mechanism
			d.mu.Lock()
			w, ok := d.workers[workerID]
			if ok {
				w.state = WorkerBusy
				_ = d.sendToWorker(w, protocol.Message{
					Type: protocol.MsgAssign,
					Assign: &protocol.AssignPayload{
						BeadID:   beadID,
						Worktree: w.worktree,
					},
				})
			}
			d.mu.Unlock()
		default:
			_ = d.logEvent(ctx, "review_failed", "ops", beadID, workerID, result.Feedback)
			_ = d.escalator.Escalate(ctx, fmt.Sprintf("review failed for bead %s: %s", beadID, result.Feedback))
		}
	}
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
	ack := protocol.ACKPayload{OK: true}

	if !dir.Valid() {
		ack.OK = false
		ack.Detail = "invalid directive"
	} else {
		d.applyDirective(dir)
		_ = d.logEvent(ctx, "directive", "manager", "", "",
			fmt.Sprintf(`{"directive":%q}`, msg.Directive.Op))
		ack.Detail = fmt.Sprintf("applied %s", msg.Directive.Op)
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
	beads, err := d.beads.Ready(ctx)
	if err != nil {
		return
	}

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
			BeadID:        bead.ID,
			Worktree:      worktree,
			Model:         bead.ResolveModel(),
			MemoryContext: memCtx,
		},
	})
	d.mu.Unlock()

	if err != nil {
		_ = d.worktrees.Remove(ctx, worktree)
		_ = d.logEvent(ctx, "worktree_cleanup", "dispatcher", bead.ID, w.id, err.Error())
	}
}

// applyDirective transitions the dispatcher state machine.
func (d *Dispatcher) applyDirective(dir protocol.Directive) {
	switch dir {
	case protocol.DirectiveStart:
		d.setState(StateRunning)
	case protocol.DirectiveStop:
		d.setState(StateStopping)
	case protocol.DirectivePause:
		d.setState(StatePaused)
	case protocol.DirectiveFocus:
		// Focus keeps running state but could filter beads — for now just ensure running.
		d.setState(StateRunning)
	}
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
		if w.state != WorkerIdle && now.Sub(w.lastSeen) > d.cfg.HeartbeatTimeout {
			dead = append(dead, id)
		}
	}
	// Remove dead workers and collect their beadIDs for reassignment
	for _, id := range dead {
		w := d.workers[id]
		_ = d.logEventLocked(ctx, "heartbeat_timeout", "dispatcher", w.beadID, id, "")
		_ = w.conn.Close()
		delete(d.workers, id)
	}
	d.mu.Unlock()
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

// shutdownCleanup cancels active ops agents, aborts in-flight merges,
// and removes active worktrees.
func (d *Dispatcher) shutdownCleanup() {
	for _, taskID := range d.ops.Active() {
		if err := d.ops.Cancel(taskID); err == nil {
			_ = d.logEvent(context.Background(), "ops_cancelled", "dispatcher", "", "", taskID)
		}
	}
	d.merger.Abort()

	// Collect worktree paths under lock.
	d.mu.Lock()
	var paths []string
	for _, w := range d.workers {
		if w.worktree != "" {
			paths = append(paths, w.worktree)
		}
	}
	d.mu.Unlock()

	// Remove worktrees best-effort (don't block shutdown).
	ctx := context.Background()
	for _, p := range paths {
		if err := d.worktrees.Remove(ctx, p); err != nil {
			_ = d.logEvent(ctx, "worktree_cleanup_failed", "dispatcher", "", "", err.Error())
		} else {
			_ = d.logEvent(ctx, "worktree_removed", "dispatcher", "", "", p)
		}
	}
}

// ConnectedWorkers returns the number of currently connected workers.
func (d *Dispatcher) ConnectedWorkers() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.workers)
}

// WorkerInfo returns state info for a tracked worker (for testing).
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
func (d *Dispatcher) WorkerModel(id string) (model string, ok bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	w, exists := d.workers[id]
	if !exists {
		return "", false
	}
	return w.model, true
}
