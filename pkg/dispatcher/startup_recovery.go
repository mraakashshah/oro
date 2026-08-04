package dispatcher

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"oro/pkg/protocol"
)

// Run starts the Dispatcher event loop. It:
//  1. Initializes the SQLite schema
//  2. Starts the UDS listener
//  3. Polls for commands (directives) and ready beads
//  4. Monitors worker heartbeats
//
// Run blocks until ctx is cancelled.
func (d *Dispatcher) Run(ctx context.Context) error {
	// Defer-recover so a panic anywhere in Run() (or its synchronous callees)
	// leaves a breadcrumb on disk before the process dies. Background loops
	// are wrapped in safeGo and have their own panic handling; this catches
	// the rest. Re-panic so callers / Go runtime still see the panic
	// (oro-zxxn — silent dispatcher death gave us nothing to triage from).
	defer func() {
		if r := recover(); r != nil {
			d.writeExitMarker("panic", fmt.Sprint(r), debug.Stack())
			panic(r)
		}
	}()

	lock, err := acquirePIDLock(d.cfg.DBPath)
	if err != nil {
		d.writeExitMarker("fatal", "acquirePIDLock: "+err.Error(), nil)
		return err
	}
	defer func() { _ = lock.release() }()
	lockRefreshCtx, cancelLockRefresh := context.WithCancel(ctx)
	defer cancelLockRefresh()
	go lock.refreshLoop(lockRefreshCtx, pidLockMaxAge/2)

	d.mu.Lock()
	d.startTime = d.nowFunc()
	d.mu.Unlock()

	if err := d.startupRecovery(ctx); err != nil {
		d.writeExitMarker("fatal", "startupRecovery: "+err.Error(), nil)
		return err
	}

	ln, err := d.openSocket()
	if err != nil {
		d.writeExitMarker("fatal", "openSocket: "+err.Error(), nil)
		return err
	}

	d.spawnBackgroundLoops(ctx, ln)

	d.safeGo(func() { d.staleAssignmentSweepLoop(ctx) })

	exitReason := "shutdownCh"
	select {
	case <-ctx.Done():
		exitReason = "ctx_done"
	case <-d.shutdownCh:
	}

	_ = ln.Close()
	d.shutdownWithTimeout()
	d.writeExitMarker("normal", exitReason, nil)
	return nil
}

// writeExitMarker appends a timestamped line to dispatcher.exit.log alongside
// the dispatcher DB. Last-resort breadcrumb when the dispatcher dies — events
// table writes can fail during shutdown if SQLite is hosed, but a plain
// os.OpenFile + Write is robust. Filed by oro-zxxn after a silent dispatcher
// death on 2026-05-05 left no triage signal.
//
// kind is one of: "panic", "normal", "fatal". detail is a short human reason
// (panic message, "ctx_done", "openSocket: ...", etc). stack is optional —
// pass debug.Stack() inside a recover to capture goroutine state.
func (d *Dispatcher) writeExitMarker(kind, detail string, stack []byte) {
	if d == nil || d.cfg.DBPath == "" || d.cfg.DBPath == ":memory:" {
		return
	}
	path := filepath.Join(filepath.Dir(d.cfg.DBPath), "dispatcher.exit.log")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec // path derived from trusted d.cfg.DBPath set at dispatcher startup
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	pid := os.Getpid()
	_, _ = fmt.Fprintf(f, "%s pid=%d kind=%s detail=%q\n", ts, pid, kind, detail)
	if len(stack) > 0 {
		_, _ = f.Write(stack)
		if stack[len(stack)-1] != '\n' {
			_, _ = f.WriteString("\n")
		}
	}
	_, _ = f.WriteString("---\n")
}

// startupRecovery initializes the schema, prunes orphaned worktrees, and runs
// state-restoration / orphaned-bead reconciliation. Errors from prune and
// reconciliation are logged but non-fatal — only schema or state-restore
// failures abort startup.
func (d *Dispatcher) startupRecovery(ctx context.Context) error {
	if _, err := d.db.ExecContext(ctx, protocol.SchemaDDL); err != nil {
		return fmt.Errorf("init schema: %w", err)
	}
	if err := protocol.MigrateBeadSchema(ctx, d.db); err != nil {
		return fmt.Errorf("init bead schema: %w", err)
	}
	if err := d.repairBlockedEpicBranchRecoveries(ctx); err != nil {
		return fmt.Errorf("repair blocked epic branch recoveries: %w", err)
	}
	if pruneErr := d.worktrees.Prune(ctx); pruneErr != nil {
		_ = d.logEvent(ctx, "worktree_prune_failed", "dispatcher", "", "", pruneErr.Error())
	}
	d.logAssignmentInvariantViolations(ctx)
	d.detectAndResolveDuplicateActiveAssignments(ctx)

	recoverableBeads, recoveryStats, err := d.restoreState(ctx)
	if err != nil {
		return fmt.Errorf("restore state: %w", err)
	}
	if err := d.reconcileReviewIntegrationsOnStartup(ctx); err != nil {
		return fmt.Errorf("reconcile review integrations: %w", err)
	}
	if err := d.reconcileOpsRunsOnStartup(ctx); err != nil {
		return fmt.Errorf("reconcile ops runs: %w", err)
	}
	if err := d.routePendingRoutableEscalations(ctx); err != nil {
		return fmt.Errorf("route pending routable escalations: %w", err)
	}
	autoResolved := d.autoResolveEmptySafeRecoveryQuarantines(ctx)
	reopened, skipped := d.resetOrphanedBeads(ctx, recoverableBeads)
	_ = d.logEvent(ctx, "startup_reconciliation_summary", "dispatcher", "", "",
		fmt.Sprintf(`{"recovered_attempts":%d,"quarantined_assignments":%d,"auto_resolved_quarantines":%d,"retired_closed_assignments":%d,"reopened_beads":%d,"skipped_in_progress":%d}`,
			recoveryStats.recoverable, recoveryStats.quarantined, autoResolved, recoveryStats.retiredClosed, reopened, skipped))
	if d.shouldRunZombieDeferredRepair() {
		if fixed, err := d.detectZombieDeferred(ctx); err == nil && fixed > 0 {
			_ = d.logEvent(ctx, "startup_zombie_defer_summary", "dispatcher", "", "",
				fmt.Sprintf(`{"fixed":%d}`, fixed))
		}
	}
	return nil
}

func (d *Dispatcher) shouldRunZombieDeferredRepair() bool {
	mode := strings.ToLower(strings.TrimSpace(d.beadSourceMode))
	return mode != "sqlite" && mode != "shadow"
}

// openSocket cleans any stale socket, binds the UDS listener with 0600
// permissions (owner-only), and stashes the listener on the dispatcher.
func (d *Dispatcher) openSocket() (net.Listener, error) {
	if err := cleanStaleSocket(d.cfg.SocketPath); err != nil {
		return nil, fmt.Errorf("stale socket check %s: %w", d.cfg.SocketPath, err)
	}
	ln, err := net.Listen("unix", d.cfg.SocketPath) //nolint:noctx // UDS bind is instant
	if err != nil {
		return nil, fmt.Errorf("listen unix %s: %w", d.cfg.SocketPath, err)
	}
	if err := os.Chmod(d.cfg.SocketPath, 0o600); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("chmod socket %s: %w", d.cfg.SocketPath, err)
	}
	d.mu.Lock()
	d.listener = ln
	d.mu.Unlock()
	return ln, nil
}

// spawnBackgroundLoops starts the accept/assign/heartbeat/pane/escalation
// goroutines (each via safeGo for panic recovery) and the HTTP server when
// WebEnabled is true.
func (d *Dispatcher) spawnBackgroundLoops(ctx context.Context, ln net.Listener) {
	d.safeGo(func() { d.acceptLoop(ctx, ln) })
	d.safeGo(func() { d.assignLoop(ctx) })
	d.safeGo(func() { d.heartbeatLoop(ctx) })
	d.safeGo(func() { d.paneMonitorLoop(ctx) })
	d.safeGo(func() { d.escalationRetryLoop(ctx) })
	d.safeGo(func() { d.reviewMaintenanceLoop(ctx) })
	d.safeGo(func() { d.runPresubmitScheduler(ctx) })
	d.safeGo(func() { d.storageControllerLoop(ctx) })
	// oro-pcp9 replaces the package-level RunSweepLoop(..., SweepConfig{}) call with
	// the method form so the sweep honours d.cfg.SweepConfig instead of zero values.
	// storageControllerLoop is main-only and unaffected, so both loops start.
	d.safeGo(func() { d.runSweepLoop(ctx, d.cfg.SweepConfig) })
	if d.cfg.WebEnabled {
		d.startHTTPServer()
	}
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

	// Shut down HTTP server (if running) so its safeGo goroutine can exit
	// before wg.Wait() below.
	d.mu.Lock()
	srv := d.httpServer
	d.mu.Unlock()
	if srv != nil {
		_ = srv.Shutdown(shutdownCtx) //nolint:contextcheck // shutdownCtx is the right scope here
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

// connCloseCleanup runs the deferred connection teardown for handleConn.
// It guards against clobbering a reconnected worker: only cleans up if the
// stored conn still matches the one this goroutine was serving.
// workerID is captured by reference in the defer so it holds its final value.
// connCloseState is the snapshot connCloseCleanup takes under d.mu before it
// dispatches the unlocked cleanup work.
type connCloseState struct {
	beadID        string
	assignmentID  int64
	worktree      string
	baseBranch    string
	retryContext  QGRetryContext
	retryPending  bool
	retrySnapshot workerAssignmentSnapshot
	preempted     bool
}

// takeConnCloseState performs connCloseCleanup's locked phase: it verifies the
// connection still owns the worker row, snapshots the assignment, and removes the
// worker. proceed is false when there is nothing further to clean up; notify is
// true when the caller must still wake the assign loop. Extracted to keep
// connCloseCleanup under the funlen limit; behaviour is unchanged.
func (d *Dispatcher) takeConnCloseState(workerID string, conn net.Conn) (connCloseState, bool, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	w, exists := d.workers[workerID]
	if !exists || w.conn != conn {
		return connCloseState{}, false, false
	}
	if w.spawnFor && w.state == protocol.WorkerShuttingDown {
		w.lastSeen = d.nowFunc()
		return connCloseState{}, false, true
	}

	st := connCloseState{
		beadID:       w.beadID,
		assignmentID: w.assignmentID,
		worktree:     w.worktree,
		baseBranch:   w.baseBranch,
		retrySnapshot: workerAssignmentSnapshot{
			execution:    w.execution,
			worktree:     w.worktree,
			runtime:      w.runtime,
			model:        w.model,
			reasoning:    w.reasoning,
			epicID:       w.epicID,
			baseBranch:   w.baseBranch,
			targetBranch: w.targetBranch,
		},
		preempted: w.state == protocol.WorkerPreempting,
	}
	st.retryContext, st.retryPending = d.pendingQGRetries[workerID]
	if st.preempted && st.beadID != "" {
		// Keep the bead reserved while its durable assignment is terminalized.
		// Without this guard a concurrently idle replacement can create a second
		// active assignment after the worker is removed but before cleanup runs.
		d.assigningBeads[st.beadID] = true
	}
	delete(d.workers, workerID)
	return st, true, false
}

func (d *Dispatcher) connCloseCleanup(workerID string, conn net.Conn) {
	if workerID == "" {
		return
	}
	st, proceed, notify := d.takeConnCloseState(workerID, conn)
	if !proceed {
		if notify {
			d.notifyAssignLoop()
		}
		return
	}
	beadID, assignmentID, worktree, baseBranch := st.beadID, st.assignmentID, st.worktree, st.baseBranch
	retryContext, retryPending := st.retryContext, st.retryPending
	retrySnapshot := st.retrySnapshot
	preempted := st.preempted

	if preempted && beadID != "" {
		d.reconcilePreemptedDisconnect(workerID, beadID, assignmentID, worktree)
		return
	}
	if retryPending {
		if err := d.restoreQGRetryHandoff(context.Background(), workerID, beadID, assignmentID, retryContext, retrySnapshot); err != nil {
			_ = d.logEvent(context.Background(), "qg_retry_feedback_restore_failed", "dispatcher", beadID, workerID, err.Error())
			return
		}
		d.notifyAssignLoop()
		return
	}

	if beadID != "" {
		if d.quarantineDisconnectedPreservedAssignment(context.Background(), workerID, beadID, assignmentID, worktree, baseBranch, "") {
			d.clearBeadTracking(beadID)
			d.notifyAssignLoop()
			return
		}
		d.clearBeadTracking(beadID)
		d.safeGo(func() {
			_ = d.updateBeadStatus(context.Background(), beadID, "open")
		})
	}

	// Wake the assign loop so reconcileScale can spawn a replacement immediately
	// rather than waiting for the next fsnotify event or fallback tick.
	d.notifyAssignLoop()
}

func (d *Dispatcher) quarantineDisconnectedPreservedAssignment(ctx context.Context, workerID, beadID string, assignmentID int64, worktree, baseBranch, cause string) bool {
	if assignmentID <= 0 {
		return false
	}
	active, err := d.assignmentActive(ctx, assignmentID, beadID)
	if err != nil {
		_ = d.logEvent(ctx, "disconnected_assignment_lookup_failed", "dispatcher", beadID, workerID, err.Error())
		return true
	}
	if !active {
		return false
	}
	blocked, details, err := d.recoveryWorkBlocked(ctx, beadID, worktree, baseBranch)
	if err == nil && !blocked {
		return false
	}
	if err != nil {
		details = appendRecoveryDetail(details, "error: "+err.Error())
	}
	if details == "" {
		details = "disconnected worker left recovery state requiring preservation"
	}
	if cause != "" {
		details = appendRecoveryDetail(details, cause)
	}
	_, err = d.createRecoveryQuarantine(ctx, recoveryQuarantine{
		BeadID:       beadID,
		AssignmentID: assignmentID,
		WorkerID:     workerID,
		Worktree:     worktree,
		Branch:       protocol.BranchPrefix + beadID,
		Reason:       "stale_active_assignment",
		Details:      details,
	})
	if err != nil {
		_ = d.logEvent(ctx, "disconnected_assignment_quarantine_failed", "dispatcher", beadID, workerID, err.Error())
		return true
	}
	if err := d.updateBeadStatus(ctx, beadID, "blocked"); err != nil {
		_ = d.logEvent(ctx, "disconnected_assignment_block_failed", "dispatcher", beadID, workerID, err.Error())
		if restoreErr := d.restoreDisconnectedAssignmentActive(ctx, assignmentID); restoreErr != nil {
			_ = d.logEvent(ctx, "disconnected_assignment_restore_failed", "dispatcher", beadID, workerID, restoreErr.Error())
		}
	}
	return true
}

func (d *Dispatcher) assignmentActive(ctx context.Context, assignmentID int64, beadID string) (bool, error) {
	var active bool
	err := d.db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM assignments WHERE id=? AND bead_id=? AND status='active')`, assignmentID, beadID).Scan(&active)
	if err != nil {
		return false, fmt.Errorf("lookup disconnected assignment: %w", err)
	}
	return active, nil
}

func (d *Dispatcher) restoreDisconnectedAssignmentActive(ctx context.Context, assignmentID int64) error {
	res, err := d.db.ExecContext(ctx,
		`UPDATE assignments SET status='active', completed_at=NULL WHERE id=? AND status='quarantined'`, assignmentID)
	if err != nil {
		return fmt.Errorf("restore disconnected assignment active: %w", err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("restore disconnected assignment active rows: %w", err)
	}
	if rows != 1 {
		return fmt.Errorf("restore disconnected assignment active: assignment_id %d affected %d rows", assignmentID, rows)
	}
	return nil
}

func (d *Dispatcher) reconcilePreemptedDisconnect(workerID, beadID string, assignmentID int64, worktree string) {
	ctx := context.Background()
	if !d.terminalizePreemptedDisconnect(ctx, workerID, beadID, assignmentID, worktree) {
		return
	}
	if d.shouldReopenBead(ctx, beadID) {
		if err := d.updateBeadStatus(ctx, beadID, "open"); err != nil {
			_ = d.logEvent(ctx, "preempt_disconnect_bead_reset_failed", "dispatcher", beadID, workerID, err.Error())
		}
	}
	d.clearBeadTracking(beadID)
	d.mu.Lock()
	delete(d.assigningBeads, beadID)
	d.mu.Unlock()
	d.notifyAssignLoop()
}

func (d *Dispatcher) terminalizePreemptedDisconnect(ctx context.Context, workerID, beadID string, assignmentID int64, worktree string) bool {
	err := d.completeAssignment(ctx, assignmentID, beadID)
	if err == nil {
		return true
	}
	_ = d.logEvent(ctx, "preempt_disconnect_assignment_cleanup_failed", "dispatcher", beadID, workerID, err.Error())
	_, quarantineErr := d.createRecoveryQuarantine(ctx, recoveryQuarantine{
		BeadID:       beadID,
		AssignmentID: assignmentID,
		WorkerID:     workerID,
		Worktree:     worktree,
		Branch:       protocol.BranchPrefix + beadID,
		Reason:       "preempted_worker_disconnect",
		Details:      err.Error(),
	})
	if quarantineErr == nil {
		return true
	}
	_ = d.logEvent(ctx, "preempt_disconnect_assignment_quarantine_failed", "dispatcher", beadID, workerID, quarantineErr.Error())
	return false
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
		d.connCloseCleanup(workerID, conn)
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

		// Handle RERANK_BY_IDS_REQUEST from short-lived callers (e.g. search pipeline).
		if msg.Type == protocol.MsgRerankByIDsRequest {
			d.handleRerankByIDsWithResponse(ctx, conn, msg)
			return
		}

		if d.handleWorkRequestConn(ctx, conn, msg) {
			return
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
	case protocol.MsgCheckpointAck:
		d.handleCheckpointAck(ctx, workerID, msg)
	case protocol.MsgCapabilityRefreshACK:
		d.handleCapabilityRefreshAck(ctx, workerID, msg)
	}
}

func (d *Dispatcher) handleHeartbeat(ctx context.Context, workerID string, msg protocol.Message) {
	if msg.Heartbeat == nil {
		return
	}
	contextIncreased := false
	d.mu.Lock()
	if w, ok := d.workers[workerID]; ok {
		w.lastSeen = d.nowFunc()
		contextIncreased = msg.Heartbeat.ContextPct > w.contextPct
		w.contextPct = msg.Heartbeat.ContextPct
	}
	d.mu.Unlock()
	if contextIncreased {
		d.recordWorkerProgress(ctx, workerID, msg.Heartbeat.BeadID, "context_pct_increase")
	}

	d.broadcastEvent("heartbeat", msg.Heartbeat.BeadID, workerID)

	// Trigger a checkpoint when context usage crosses the configured threshold
	// and no checkpoint is already in-flight for this bead (§9.3).
	if d.cfg.CheckpointThreshold > 0 &&
		msg.Heartbeat.ContextPct >= d.cfg.CheckpointThreshold &&
		msg.Heartbeat.BeadID != "" &&
		d.checkpoints.get(msg.Heartbeat.BeadID) == nil {
		d.triggerCheckpoint(ctx, msg.Heartbeat.BeadID, workerID, msg.Heartbeat.ContextPct)
	}
}

func (d *Dispatcher) handleTypeChangedToEpic(ctx context.Context, workerID, beadID string, release doneWorkerRelease) bool {
	if release.isEpicDecomp {
		return false
	}
	detail, err := d.beads.Show(ctx, beadID)
	if err != nil || detail == nil || detail.Type != "epic" {
		return false
	}
	_ = d.logEvent(ctx, "type_changed_to_epic", workerID, beadID, workerID, "")
	d.quarantineUnsafeRecoveryWork(ctx, recoveryQuarantine{
		BeadID:       beadID,
		AssignmentID: release.assignmentID,
		WorkerID:     workerID,
		Worktree:     release.worktree,
		Branch:       release.branch,
		Reason:       "type_changed_to_epic",
		Details:      "worker completed a task that was promoted to epic before merge",
	})
	d.releaseWorkerAfterDoneTerminal(workerID, beadID, release.assignmentID)
	return true
}

func (d *Dispatcher) completeManualIntegration(ctx context.Context, beadID, workerID string, release doneWorkerRelease) {
	if err := d.completeAssignment(ctx, release.assignmentID, beadID); err != nil {
		_ = d.logEvent(ctx, "manual_integration_assignment_cleanup_failed", "dispatcher", beadID, workerID, err.Error())
	}
	if err := d.updateBeadStatus(ctx, beadID, "blocked"); err != nil {
		_ = d.logEvent(ctx, "manual_integration_status_failed", "dispatcher", beadID, workerID, err.Error())
	}
	detail := fmt.Sprintf(`{"branch":%q,"worktree":%q,"target_branch":%q}`, release.branch, release.worktree, release.targetBranch)
	_ = d.logEvent(ctx, "manual_integration_required", "dispatcher", beadID, workerID, detail)
	d.escalate(ctx, fmt.Sprintf("[ORO-DISPATCH] MANUAL_INTEGRATION: %s — review and merge %s from %s.", beadID, release.branch, release.worktree), beadID, workerID)
	d.releaseWorkerAfterDoneTerminal(workerID, beadID, release.assignmentID)
}

type doneWorkerRelease struct {
	worktree     string
	branch       string
	epicID       string
	targetBranch string
	assignmentID int64
	isEpicDecomp bool
	ok           bool
}

func (d *Dispatcher) releaseWorkerAfterDoneLocked(workerID, beadID string) doneWorkerRelease {
	w, ok := d.workers[workerID]
	if !ok {
		return doneWorkerRelease{}
	}

	release := doneWorkerRelease{
		worktree:     w.worktree,
		branch:       protocol.BranchPrefix + beadID,
		epicID:       w.epicID,
		targetBranch: w.targetBranch,
		assignmentID: w.assignmentID,
		isEpicDecomp: w.isEpicDecomp,
		ok:           true,
	}
	spawnFor := w.spawnFor

	if spawnFor {
		w.state = protocol.WorkerShuttingDown
	} else {
		w.state = protocol.WorkerReserved
	}

	if spawnFor {
		d.shutdownCompletedSpawnForWorkerLocked(w)
	}
	return release
}

func (d *Dispatcher) releaseWorkerAfterDoneTerminal(workerID, beadID string, assignmentID int64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	w, ok := d.workers[workerID]
	if !ok || w.beadID != beadID || w.assignmentID != assignmentID {
		return
	}
	if w.spawnFor {
		w.state = protocol.WorkerShuttingDown
		d.shutdownCompletedSpawnForWorkerLocked(w)
	} else {
		w.state = protocol.WorkerIdle
	}
	w.assignmentID = 0
	w.beadID = ""
	w.epicID = ""
	w.isEpicDecomp = false
	w.worktree = ""
	w.baseBranch = ""
	w.targetBranch = ""
	w.targetBeadID = ""
	w.lastProgress = d.nowFunc()
	d.notifyAssignLoop()
}

// storeRejectionFeedback persists reviewer feedback in the rejection_history
// table (not memories), so rejections accumulate across retry cycles without
// polluting the memory search index. Best-effort: errors are silently ignored.
func (d *Dispatcher) pruneStaleAgentBranches(ctx context.Context) {
	if d.repoRoot == "" {
		return
	}
	out, err := d.commandRunner().Run(ctx, "git", "-C", d.repoRoot, "branch", "--list", "agent/*")
	if err != nil {
		_ = d.logEvent(ctx, "startup_prune_branches_list_failed", "dispatcher", "", "", err.Error())
		return
	}
	for _, line := range strings.Split(string(out), "\n") {
		branch := strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "*+"))
		if branch == "" {
			continue
		}
		if _, delErr := d.commandRunner().Run(ctx, "git", "-C", d.repoRoot, "branch", "-d", branch); delErr != nil {
			_ = d.logEvent(ctx, "startup_prune_branch_delete_failed", "dispatcher", "", "", branch+": "+delErr.Error())
		}
	}
}

// deleteStaleAgentBranch deletes agent/<beadID> if it exists and is already
// merged into targetBranch, logging the outcome.
// Called before worktree.Create to ensure the new worktree always branches from
// the resolved assignment target HEAD.
// If git cannot safely delete the branch, the branch is recovery-quarantined
// and assignment aborts. Startup/retry recovery must preserve ambiguous branch
// state instead of force-deleting or removing the checked-out worktree.
func (d *Dispatcher) deleteStaleAgentBranch(ctx context.Context, beadID, workerID, targetBranch string) error {
	branch := protocol.BranchPrefix + beadID
	if targetBranch == "" {
		targetBranch = d.cfg.DefaultBranch
	}
	exists, err := d.worktrees.BranchExists(ctx, branch)
	if err != nil {
		return fmt.Errorf("check stale branch %s exists: %w", branch, err)
	}
	if !exists {
		return nil
	}
	preservedAssignmentID, preserved, err := d.resolvedPreservedMismatchForRequeuedBead(ctx, beadID)
	if err != nil {
		return fmt.Errorf("check stale branch %s preserved recovery state: %w", branch, err)
	}
	if preserved {
		_ = d.logEvent(ctx, "stale_agent_branch_cleanup_suppressed", "dispatcher", beadID, workerID,
			fmt.Sprintf(`{"branch":%q,"assignment_id":%d,"reason":"resolved_preserved_mismatch"}`,
				branch, preservedAssignmentID))
		return fmt.Errorf("%w: assignment %d", errResolvedPreservedMismatch, preservedAssignmentID)
	}
	err = d.worktrees.DeleteBranchMergedInto(ctx, branch, targetBranch)
	if err == nil {
		_ = d.logEvent(ctx, "stale_agent_branch_deleted", "dispatcher", beadID, workerID,
			fmt.Sprintf(`{"branch":%q,"target_branch":%q}`, branch, targetBranch))
		return nil
	}
	reason := "unsafe_stale_branch"
	if strings.Contains(strings.ToLower(err.Error()), "checked out") {
		reason = "branch_worktree_mismatch"
	}
	worktreePath := filepath.Join(d.repoRoot, ".worktrees", beadID)
	if _, qErr := d.createRecoveryQuarantine(ctx, recoveryQuarantine{
		BeadID:   beadID,
		WorkerID: workerID,
		Worktree: worktreePath,
		Branch:   branch,
		Reason:   reason,
		Details:  err.Error(),
	}); qErr != nil {
		_ = d.logEvent(ctx, "stale_branch_quarantine_failed", "dispatcher", beadID, workerID, qErr.Error())
		return fmt.Errorf("delete stale branch %s: %w", branch, err)
	}
	_ = d.logEvent(ctx, "stale_agent_branch_quarantined", "dispatcher", beadID, workerID,
		fmt.Sprintf(`{"branch":%q,"reason":%q,"error":%q}`, branch, reason, err.Error()))
	return fmt.Errorf("stale branch %s quarantined: %w", branch, err)
}

// resetOrphanedBeads resets recoverable dispatcher-owned in_progress beads back
// to open on startup. Human-owned in_progress beads are left untouched because
// they have no dispatcher-owned active assignment state to recover from.
// Errors are non-fatal — logged via logEvent and startup continues.
func (d *Dispatcher) resetOrphanedBeads(ctx context.Context, recoverable map[string]bool) (reopened, skipped int) {
	beads, err := d.beads.InProgress(ctx)
	if err != nil {
		_ = d.logEvent(ctx, "startup_reset_list_failed", "dispatcher", "", "", err.Error())
		return 0, 0
	}
	checkpointOwned, err := d.reviewCheckpointBlockedBeads(ctx)
	d.recordAssignmentObservation("review_checkpoint", err)
	if err != nil {
		_ = d.logEvent(ctx, "startup_reset_checkpoint_observation_failed", "dispatcher", "", "", err.Error())
		return 0, len(beads)
	}
	for _, b := range beads {
		if !recoverable[b.ID] || checkpointOwned[b.ID] {
			skipped++
			continue
		}
		if updateErr := d.updateBeadStatus(ctx, b.ID, "open"); updateErr != nil {
			_ = d.logEvent(ctx, "startup_reset_bead_failed", "dispatcher", b.ID, "", updateErr.Error())
			continue
		}
		reopened++
	}
	return reopened, skipped
}

// restoreState reconstructs the in-memory attemptCounts and handoffCounts maps
// from recoverable active assignments persisted in SQLite. This ensures
// tracking state survives a dispatcher restart without reopening inconsistent
// rows that would destroy or overwrite recoverable work.
type startupRecoveryStats struct {
	recoverable   int
	quarantined   int
	retiredClosed int
}

type restoredAssignment struct {
	id           int64
	beadID       string
	worktree     string
	attemptCount int
	handoffCount int
}

type quarantinedAssignment struct {
	id       int64
	beadID   string
	workerID string
	worktree string
	branch   string
	reason   string
}

type retiredClosedAssignment struct {
	id     int64
	beadID string
}

func (d *Dispatcher) restoreState(ctx context.Context) (map[string]bool, startupRecoveryStats, error) {
	restored, quarantined, retiredClosed, err := d.loadActiveAssignments(ctx)
	if err != nil {
		return nil, startupRecoveryStats{}, err
	}
	d.processRetiredClosedAssignments(ctx, retiredClosed)
	d.processQuarantined(ctx, quarantined)
	recoverable := d.applyRestoredAssignments(restored)
	d.restoreInflightCheckpoints(ctx, restored)
	stats := startupRecoveryStats{
		recoverable:   len(restored),
		quarantined:   len(quarantined),
		retiredClosed: len(retiredClosed),
	}
	return recoverable, stats, nil
}

// loadActiveAssignments reads active and shutdown-requeued assignments and
// partitions them into restorable (worktree + branch present) and quarantinable
// sets. Generic completed assignments are intentionally ignored.
func (d *Dispatcher) loadActiveAssignments(ctx context.Context) ([]restoredAssignment, []quarantinedAssignment, []retiredClosedAssignment, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT id, bead_id, worker_id, worktree, attempt_count, handoff_count FROM assignments WHERE status IN ('active', 'requeued')`)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("query active assignments: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var (
		restored      []restoredAssignment
		quarantined   []quarantinedAssignment
		retiredClosed []retiredClosedAssignment
	)
	for rows.Next() {
		var a restoredAssignment
		var workerID string
		if err := rows.Scan(&a.id, &a.beadID, &workerID, &a.worktree, &a.attemptCount, &a.handoffCount); err != nil {
			return nil, nil, nil, fmt.Errorf("scan assignment: %w", err)
		}
		if d.assignmentHasRetirableClosedBead(ctx, a.beadID, a) {
			retiredClosed = append(retiredClosed, retiredClosedAssignment{
				id:     a.id,
				beadID: a.beadID,
			})
			continue
		}
		if reason := d.classifyAssignment(ctx, a); reason != "" {
			if reason == "branch_worktree_mismatch" && d.resolvedPreservedMismatchAssignment(ctx, a.id) {
				continue
			}
			quarantined = append(quarantined, quarantinedAssignment{
				id:       a.id,
				beadID:   a.beadID,
				workerID: workerID,
				worktree: a.worktree,
				branch:   protocol.BranchPrefix + a.beadID,
				reason:   reason,
			})
			continue
		}
		restored = append(restored, a)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, nil, fmt.Errorf("iterate assignments: %w", err)
	}
	return restored, quarantined, retiredClosed, nil
}

func (d *Dispatcher) assignmentHasRetirableClosedBead(ctx context.Context, beadID string, a restoredAssignment) bool {
	detail, err := d.beads.Show(ctx, beadID)
	if err != nil {
		_ = d.logEvent(ctx, "startup_closed_assignment_lookup_failed", "dispatcher", beadID, "", err.Error())
		return false
	}
	if detail == nil || !strings.EqualFold(detail.Status, "closed") {
		return false
	}
	switch d.classifyAssignment(ctx, a) {
	case "missing_worktree", "missing_worktree_path", "missing_branch":
		return true
	default:
		return false
	}
}

// classifyAssignment returns "" if the assignment is recoverable, or a
// quarantine reason otherwise: missing_worktree | missing_worktree_path |
// branch_check_failed | missing_branch | branch_worktree_mismatch.
func (d *Dispatcher) classifyAssignment(ctx context.Context, a restoredAssignment) string {
	branch := protocol.BranchPrefix + a.beadID
	switch {
	case a.worktree == "":
		return "missing_worktree"
	case !d.worktrees.Exists(ctx, a.worktree):
		return "missing_worktree_path"
	}
	exists, branchErr := d.worktrees.BranchExists(ctx, branch)
	switch {
	case branchErr != nil:
		return "branch_check_failed"
	case !exists:
		return "missing_branch"
	}
	currentBranch, currentErr := d.worktrees.CurrentBranch(ctx, a.worktree)
	if currentErr != nil || currentBranch != branch {
		return "branch_worktree_mismatch"
	}
	return ""
}

// processQuarantined records each unsafe recovery state in the durable
// recovery quarantine table and keeps the assignment visible as quarantined.
func (d *Dispatcher) processQuarantined(ctx context.Context, quarantined []quarantinedAssignment) {
	for _, q := range quarantined {
		if _, err := d.createRecoveryQuarantineWithEvent(ctx, recoveryQuarantine{
			BeadID:       q.beadID,
			AssignmentID: q.id,
			WorkerID:     q.workerID,
			Worktree:     q.worktree,
			Branch:       q.branch,
			Reason:       q.reason,
			Details:      "startup recovery could not prove branch/worktree consistency",
		}, recoveryQuarantineEvent{
			Type:    "startup_recovery_quarantined",
			Source:  "dispatcher",
			BeadID:  q.beadID,
			Payload: fmt.Sprintf(`{"assignment_id":%d,"reason":%q}`, q.id, q.reason),
		}); err != nil {
			_ = d.logEvent(ctx, "startup_recovery_quarantine_failed", "dispatcher", q.beadID, q.workerID, err.Error())
		}
	}
}

func (d *Dispatcher) processRetiredClosedAssignments(ctx context.Context, retired []retiredClosedAssignment) {
	for _, assignment := range retired {
		if err := d.completeAssignment(ctx, assignment.id, assignment.beadID); err != nil {
			_ = d.logEvent(ctx, "startup_closed_assignment_retire_failed", "dispatcher", assignment.beadID, "", err.Error())
			continue
		}
		_ = d.logEvent(ctx, "startup_closed_assignment_retired", "dispatcher", assignment.beadID, "",
			fmt.Sprintf(`{"assignment_id":%d,"reason":"closed_empty_state"}`, assignment.id))
	}
}

func (d *Dispatcher) recoveryWorkBlocked(ctx context.Context, beadID, worktree, baseBranch string) (blocked bool, details string, err error) {
	if worktree == "" {
		return true, "missing worktree path", nil
	}
	if !d.worktrees.Exists(ctx, worktree) {
		return true, "worktree path missing: " + worktree, nil
	}

	dirty, dirtyStatus, dirtyErr := d.worktreeDirty(ctx, beadID, worktree)
	if dirty || dirtyErr != nil {
		return dirty, dirtyStatus, dirtyErr
	}
	return d.branchHasUnmergedWork(ctx, beadID, worktree, baseBranch)
}

func (d *Dispatcher) worktreeDirty(ctx context.Context, beadID, worktree string) (dirty bool, status string, err error) {
	out, err := d.commandRunner().Run(ctx, "git", "-C", worktree, "status", "--porcelain")
	if err != nil {
		return false, "", fmt.Errorf("git status in %s: %w", worktree, err)
	}
	var remaining []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if len(line) >= 4 && line[:2] == "??" && isManagedQualityGateCachePath(beadID, strings.TrimSpace(line[3:])) {
			continue
		}
		if line != "" {
			remaining = append(remaining, line)
		}
	}
	status = strings.Join(remaining, "\n")
	return status != "", status, nil
}

func (d *Dispatcher) branchHasUnmergedWork(ctx context.Context, beadID, worktree, baseBranch string) (blocked bool, details string, err error) {
	if beadID == "" {
		return false, "", nil
	}
	if baseBranch == "" {
		baseBranch = d.cfg.DefaultBranch
	}
	if baseBranch == "" {
		baseBranch = "main"
	}
	branch := protocol.BranchPrefix + beadID
	out, err := d.commandRunner().Run(ctx, "git", "-C", worktree, "rev-list", "--count", baseBranch+".."+branch)
	if err != nil {
		return false, "", fmt.Errorf("git rev-list %s..%s in %s: %w", baseBranch, branch, worktree, err)
	}
	countText := strings.TrimSpace(string(out))
	if countText == "" {
		return false, "", nil
	}
	count, err := strconv.Atoi(countText)
	if err != nil {
		return false, "", fmt.Errorf("parse git rev-list count %q for %s..%s: %w", countText, baseBranch, branch, err)
	}
	if count == 0 {
		return false, "", nil
	}
	return true, fmt.Sprintf("%s has %d commit(s) not in %s", branch, count, baseBranch), nil
}

func appendRecoveryDetail(details, extra string) string {
	if details == "" {
		return extra
	}
	return details + "; " + extra
}

// staleAssignmentSweepLoop sweeps stale active assignments after a startup
// grace window, then keeps sweeping periodically for long-lived dispatcher
// sessions. The initial grace preserves time for workers from a surviving
// restart to reconnect before their assignments are considered stale.
func (d *Dispatcher) staleAssignmentSweepLoop(ctx context.Context) {
	graceWindow := 3 * d.cfg.HeartbeatTimeout
	select {
	case <-time.After(graceWindow):
	case <-ctx.Done():
		return
	case <-d.shutdownCh:
		return
	}
	d.abandonStaleActiveAssignments(ctx)

	ticker := time.NewTicker(d.cfg.HeartbeatTimeout)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			d.abandonStaleActiveAssignments(ctx)
		case <-ctx.Done():
			return
		case <-d.shutdownCh:
			return
		}
	}
}

// abandonStaleActiveAssignments walks every status='active' assignment row
// and quarantines any whose worker_id is not currently in the connected pool.
// A silently dead worker can leave useful work in its worktree/branch, so the
// row stays visible as recovery-owned state until an operator resolves it.
//
// Filed by oro-tczh after a silent dispatcher death (oro-zxxn) left 9 dead
// workers' assignments still active. The new dispatcher's startupRecovery
// path only handled in_progress beads with recoverable worktrees and never
// abandoned the stranded rows; the queue silently dropped from many beads
// to one until manual SQL untangled it.
//
// Caller is responsible for the grace window — call after enough time has
// passed that any worker that was going to reconnect has done so.
func (d *Dispatcher) abandonStaleActiveAssignments(ctx context.Context) int {
	rows, err := d.db.QueryContext(ctx,
		`SELECT id, bead_id, worker_id, worktree FROM assignments WHERE status='active'`)
	if err != nil {
		_ = d.logEvent(ctx, "stale_assignment_scan_failed", "dispatcher", "", "", err.Error())
		return 0
	}
	type stale struct {
		id       int64
		beadID   string
		workerID string
		worktree string
	}
	var pending []stale
	for rows.Next() {
		var s stale
		if scanErr := rows.Scan(&s.id, &s.beadID, &s.workerID, &s.worktree); scanErr != nil {
			_ = rows.Close()
			_ = d.logEvent(ctx, "stale_assignment_scan_failed", "dispatcher", "", "", scanErr.Error())
			return 0
		}
		d.mu.Lock()
		_, connected := d.workers[s.workerID]
		d.mu.Unlock()
		if !connected {
			pending = append(pending, s)
		}
	}
	_ = rows.Close()

	abandoned := 0
	for _, s := range pending {
		branch := protocol.BranchPrefix + s.beadID
		if _, err := d.createRecoveryQuarantine(ctx, recoveryQuarantine{
			BeadID:       s.beadID,
			AssignmentID: s.id,
			WorkerID:     s.workerID,
			Worktree:     s.worktree,
			Branch:       branch,
			Reason:       "stale_active_assignment",
			Details:      "active assignment belongs to a disconnected worker",
		}); err != nil {
			_ = d.logEvent(ctx, "stale_assignment_quarantine_failed", "dispatcher", s.beadID, s.workerID, err.Error())
			continue
		}
		abandoned++
		_ = d.logEvent(ctx, "stale_assignment_quarantined", "dispatcher", s.beadID, s.workerID,
			fmt.Sprintf(`{"assignment_id":%d,"branch":%q,"worktree":%q}`, s.id, branch, s.worktree))
	}
	return abandoned
}

// applyRestoredAssignments populates worktreeByBead/attemptCounts/handoffCounts
// from the recovered assignments and returns the set of recoverable bead IDs.
func (d *Dispatcher) applyRestoredAssignments(restored []restoredAssignment) map[string]bool {
	recoverable := make(map[string]bool, len(restored))
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, a := range restored {
		recoverable[a.beadID] = true
		d.worktreeByBead[a.beadID] = a.worktree
		if a.attemptCount > 0 {
			d.attemptCounts[a.beadID] = a.attemptCount
		}
		if a.handoffCount > 0 {
			d.handoffCounts[a.beadID] = a.handoffCount
		}
	}
	return recoverable
}

func (d *Dispatcher) logAssignmentInvariantViolations(ctx context.Context) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT bead_id, COUNT(*) FROM assignments WHERE status='active' GROUP BY bead_id HAVING COUNT(*) > 1`)
	if err != nil {
		return
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var beadID string
		var activeCount int
		if err := rows.Scan(&beadID, &activeCount); err != nil {
			return
		}
		_ = d.logEvent(ctx, "assignment_invariant_violation", "dispatcher", beadID, "",
			fmt.Sprintf(`{"active_assignments":%d}`, activeCount))
	}
}

// releasePriorAssignment finalizes a worker's previous bead assignment when
// it is being reassigned to a different bead without a clean DONE. It returns
// the prior bead to a retryable state and preserves the branch/worktree so a
// later assignment can resume or inspect the abandoned attempt. Without this,
// a worker reassigned mid-run leaves the prior bead stuck in_progress with
// worker_id still pointing at the worker (oro-xqrh).
//
// The caller must NOT hold d.mu. Safe to call when the worker has no prior
// bead. If in-memory bead state was already cleared but assignmentID remains,
